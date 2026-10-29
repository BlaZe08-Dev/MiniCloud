package faultinject_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

const (
	realLease = 3 * time.Second
	realGrace = 3 * time.Second // startup grace must be at least the lease (config validation)
)

type realEnv struct {
	t      *testing.T
	pg     *sectest.PGSchema
	reader *sectest.Rig // read-only view of the same schema (its own in-process service is never called)
	bin    string
	addr   string
	url    string
}

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	pg := sectest.NewPGSchema(t) // skips when the database URL is unset
	e := &realEnv{t: t, pg: pg, bin: sectest.Binaries(t), addr: sectest.FreePort(t)}
	e.url = "http://" + e.addr
	e.reader = sectest.NewRig(t, sectest.KindPostgres, sectest.Options{PG: pg})
	return e
}

func (e *realEnv) startCoordinator(grace time.Duration) *sectest.Proc {
	e.t.Helper()
	p := sectest.StartProc(e.t, "coordinator", filepath.Join(e.bin, "coordinator"), sectest.CoordinatorEnv(e.addr, e.pg.SchemaURL, realLease, grace))
	if !sectest.WaitFor(15*time.Second, func() bool {
		return sectest.DoRaw(e.url+"/readyz") == http.StatusOK
	}) {
		e.t.Fatalf("coordinator not ready; output:\n%s", redact(p.Out.String(), e.pg.SchemaURL))
	}
	return p
}

func (e *realEnv) startWorker(id string, slots int, coordURL string) *sectest.Proc {
	e.t.Helper()
	return sectest.StartProc(e.t, "worker-"+id, filepath.Join(e.bin, "worker"), sectest.WorkerEnv(id, coordURL, slots))
}

func redact(s, secret string) string { return strings.ReplaceAll(s, secret, "<db-url>") }

func (e *realEnv) submit(task, payload string, maxAttempts int) string {
	e.t.Helper()
	resp := sectest.DoURL(e.t, http.DefaultClient, e.url, "POST", "/v1/jobs", fmt.Sprintf(`{"task_type":%q,"payload":%s,"max_attempts":%d}`, task, payload, maxAttempts))
	if resp.Status != http.StatusCreated {
		e.t.Fatalf("submit: %s", resp)
	}
	return sectest.JobID(resp)
}

func (e *realEnv) job(id string) *domain.Job {
	j, err := e.reader.Repo.GetJob(context.Background(), id)
	if err != nil {
		e.t.Fatalf("get job %s: %v", id, err)
	}
	return j
}

func (e *realEnv) attempts(id string) []*domain.Attempt {
	a, err := e.reader.Repo.ListAttemptsByJob(context.Background(), id)
	if err != nil {
		e.t.Fatalf("attempts %s: %v", id, err)
	}
	return a
}

func (e *realEnv) waitJob(id string, timeout time.Duration, cond func(*domain.Job, []*domain.Attempt) bool, what string) {
	e.t.Helper()
	if !sectest.WaitFor(timeout, func() bool { return cond(e.job(id), e.attempts(id)) }) {
		j, as := e.job(id), e.attempts(id)
		var st []string
		for _, a := range as {
			st = append(st, fmt.Sprintf("#%d %s on %s", a.AttemptNo, a.State, a.WorkerID))
		}
		e.t.Fatalf("timed out waiting for %s: job %s attempts %v", what, j.State, st)
	}
}

func hasRunningOn(worker string) func(*domain.Job, []*domain.Attempt) bool {
	return func(_ *domain.Job, as []*domain.Attempt) bool {
		for _, a := range as {
			if a.State == domain.AttemptRunning && a.WorkerID == worker {
				return true
			}
		}
		return false
	}
}

// TestFault_WorkerKill9_LeaseSweepAndRetry is FS-01/FS-02 with real binaries: worker A is killed
// with SIGKILL while it runs a task, the lease lapses, the sweeper marks attempt 1 LOST and the job
// retries on worker B, which completes it.
func TestFault_WorkerKill9_LeaseSweepAndRetry(t *testing.T) {
	e := newRealEnv(t)
	e.startCoordinator(realGrace)
	wA := e.startWorker("wa", 1, e.url)
	job := e.submit("sleep", `{"duration_ms":2500}`, 3)
	e.waitJob(job, 15*time.Second, func(j *domain.Job, as []*domain.Attempt) bool { return hasRunningOn("wa")(j, as) }, "attempt 1 running on wa")

	wA.Kill9()
	e.startWorker("wb", 1, e.url)
	e.waitJob(job, 40*time.Second, func(j *domain.Job, as []*domain.Attempt) bool { return j.State == domain.StateSucceeded }, "job to succeed on the surviving worker")

	as := e.attempts(job)
	if len(as) != 2 || as[0].State != domain.AttemptLost || as[0].WorkerID != "wa" || as[1].State != domain.AttemptCompleted || as[1].WorkerID != "wb" {
		t.Fatalf("attempts = %+v, want LOST on wa then COMPLETED on wb", summarize(as))
	}
	if e.reader.CountEvents(job, domain.JobEventLeaseExpired) != 1 || e.reader.CountEvents(job, domain.JobEventRetrying) != 1 {
		t.Fatalf("events lease_expired=%d retrying=%d, want 1 and 1", e.reader.CountEvents(job, domain.JobEventLeaseExpired), e.reader.CountEvents(job, domain.JobEventRetrying))
	}
	if e.reader.ResultRows(job) != 1 {
		t.Fatalf("result rows = %d, want 1", e.reader.ResultRows(job))
	}
	sectest.CheckInvariants(t, e.reader, []string{job})
}

func summarize(as []*domain.Attempt) string {
	var b strings.Builder
	for _, a := range as {
		fmt.Fprintf(&b, "[#%d %s %s]", a.AttemptNo, a.State, a.WorkerID)
	}
	return b.String()
}

// TestFault_WorkerPartition_ProxyBlackhole_ZombieAbortsAndLateCommit409 partitions a real worker
// from a real coordinator with the fault proxy. The lease lapses and the attempt is swept; when the
// partition heals the zombie learns it was lost, aborts and sends no result (4c-b A2, end to end);
// the job then finishes through attempt 2 and the stored state shows no trace of the zombie.
func TestFault_WorkerPartition_ProxyBlackhole_ZombieAbortsAndLateCommit409(t *testing.T) {
	e := newRealEnv(t)
	e.startCoordinator(realGrace)
	px := sectest.NewFaultProxy(t, e.addr)
	e.startWorker("wz", 1, "http://"+px.Addr())
	job := e.submit("sleep", `{"duration_ms":9000}`, 3)
	var first *domain.Attempt
	e.waitJob(job, 15*time.Second, func(j *domain.Job, as []*domain.Attempt) bool {
		if hasRunningOn("wz")(j, as) {
			first = as[0]
			return true
		}
		return false
	}, "attempt 1 running on wz")

	px.SetMode(sectest.ModeBlackhole)
	e.waitJob(job, 30*time.Second, func(j *domain.Job, as []*domain.Attempt) bool {
		return as[0].State == domain.AttemptLost
	}, "attempt 1 swept to LOST during the partition")

	px.SetMode(sectest.ModeForward)
	e.waitJob(job, 60*time.Second, func(j *domain.Job, as []*domain.Attempt) bool { return j.State == domain.StateSucceeded }, "job to succeed after the partition heals")

	as := e.attempts(job)
	if as[0].ID != first.ID || as[0].State != domain.AttemptLost {
		t.Fatalf("attempt 1 = %s %s, want LOST and never resurrected", as[0].ID, as[0].State)
	}
	if n := e.reader.CountEvents(job, domain.JobEventDiagnosticLateResult); n != 0 {
		t.Fatalf("DIAGNOSTIC_LATE_RESULT events = %d: the zombie reported a result instead of aborting", n)
	}
	if e.reader.ResultRows(job) != 1 {
		t.Fatalf("result rows = %d, want 1 (attempt 2 only)", e.reader.ResultRows(job))
	}
	if got := e.pg.Count(t, "SELECT count(*) FROM results r JOIN attempts a ON a.job_id = r.job_id WHERE a.id = $1 AND a.attempt_no = 1 AND a.state = 'COMPLETED'", first.ID); got != 0 {
		t.Fatalf("attempt 1 shows COMPLETED")
	}
	sectest.CheckInvariants(t, e.reader, []string{job})
}

// TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit kills the real coordinator with SIGKILL
// while a worker holds an attempt, lets the lease lapse, and restarts it: with the startup grace
// the surviving worker's commit is accepted; the control waits for the grace to end, the attempt is
// reaped and the same commit is rejected.
func TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		waitReaped bool
	}{{"within grace", false}, {"control after grace ends", true}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := newRealEnv(t)
			c1 := e.startCoordinator(realGrace)
			if resp := sectest.DoURL(t, http.DefaultClient, e.url, "POST", "/v1/workers/register",
				`{"id":"w1","version":"sec","total_slots":1,"supported_tasks":["echo"]}`); resp.Status != http.StatusOK {
				t.Fatalf("register: %s", resp)
			}
			job := e.submit("echo", `{"message":"x"}`, 3)
			poll := sectest.DoURL(t, http.DefaultClient, e.url, "POST", "/v1/workers/w1/poll", "")
			jobID, attID, tok := sectest.AssignmentOf(poll)
			if jobID != job {
				t.Fatalf("poll = %s", poll)
			}
			c1.Kill9()
			e.pg.Exec(t, "UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE state = 'RUNNING'")

			c2 := e.startCoordinator(realGrace)
			if tc.waitReaped {
				e.waitJob(job, 20*time.Second, func(_ *domain.Job, as []*domain.Attempt) bool { return as[0].State == domain.AttemptLost }, "attempt to be reaped once the grace ends")
			} else if !sectest.WaitFor(10*time.Second, func() bool { return strings.Contains(c2.Out.String(), "held back by grace") }) {
				t.Fatalf("the sweeper never reported holding the expired attempt back; output:\n%s", redact(c2.Out.String(), e.pg.SchemaURL))
			}
			resp := sectest.DoURL(t, http.DefaultClient, e.url, "POST", "/v1/jobs/"+jobID+"/attempts/"+attID+"/result",
				map[string]any{"lease_token": tok, "worker_id": "w1", "exit_code": 0, "output": "o"})
			if tc.waitReaped {
				if resp.Status != http.StatusConflict {
					t.Fatalf("late commit after the grace = %s, want 409", resp)
				}
			} else {
				if resp.Status != http.StatusOK {
					t.Fatalf("surviving commit within grace = %s, want 200", resp)
				}
				if j := e.job(job); j.State != domain.StateSucceeded {
					t.Fatalf("job %s, want SUCCEEDED", j.State)
				}
			}
			sectest.CheckInvariants(t, e.reader, []string{job})
		})
	}
}

// TestFault_CoordinatorKill9_DuringCommitStorm_InvariantsAfterRestart is the executed
// approximation of FS-06 (no failpoint exists): real coordinator and three real workers process a
// continuous stream of submissions; the coordinator is killed with SIGKILL at a random point and
// restarted on the same schema; every job must end in exactly one terminal state and the stored
// state must satisfy the invariants. The kill point is drawn from a seed that is logged.
func TestFault_CoordinatorKill9_DuringCommitStorm_InvariantsAfterRestart(t *testing.T) {
	e := newRealEnv(t)
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	t.Logf("seed %d", seed)
	killAfter := 15 + rng.Intn(45) // jobs submitted before the kill
	const total = 120

	c1 := e.startCoordinator(realGrace)
	for _, id := range []string{"s1", "s2", "s3"} {
		e.startWorker(id, 4, e.url)
	}
	var mu sync.Mutex
	submitted := 0
	killed := make(chan struct{})
	var once sync.Once
	var atKill int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			resp := sectest.PostRaw(e.url, `{"task_type":"sleep","payload":{"duration_ms":150},"max_attempts":5}`)
			mu.Lock()
			if resp == http.StatusCreated {
				submitted++
			}
			n := submitted
			mu.Unlock()
			// Kill once enough submissions are acknowledged AND attempts are in flight, so the
			// kill lands in the middle of executions and commits.
			if n >= killAfter && e.runningAttempts() > 0 {
				once.Do(func() {
					atKill = e.runningAttempts()
					c1.Kill9()
					close(killed)
				})
			}
			select {
			case <-killed:
				// the coordinator is down until the main goroutine restarts it; refused submits are not counted
				sectest.WaitFor(20*time.Second, func() bool { return sectest.DoRaw(e.url+"/healthz") == http.StatusOK })
			default:
			}
		}
	}()
	select {
	case <-killed:
	case <-time.After(60 * time.Second):
		t.Fatal("kill point never reached")
	}
	e.startCoordinator(realGrace)
	wg.Wait()

	ids := e.allJobIDs()
	if len(ids) < killAfter {
		t.Fatalf("only %d jobs stored, %d were acknowledged before the kill", len(ids), killAfter)
	}
	if !sectest.WaitFor(90*time.Second, func() bool {
		for _, id := range ids {
			if !e.job(id).State.IsTerminal() {
				return false
			}
		}
		return true
	}) {
		counts := map[domain.JobState]int{}
		for _, id := range ids {
			counts[e.job(id).State]++
		}
		t.Fatalf("not every job reached a terminal state: %v", counts)
	}
	for _, id := range ids {
		if j := e.job(id); j.State != domain.StateSucceeded {
			t.Errorf("job %s ended %s, want SUCCEEDED (echo never fails; lost attempts retry)", id, j.State)
		}
	}
	sectest.CheckInvariants(t, e.reader, ids)
	if !sectest.WaitFor(20*time.Second, func() bool {
		ws, _ := e.reader.Repo.ListWorkers(context.Background())
		for _, w := range ws {
			if w.AvailableSlots != w.TotalSlots {
				return false
			}
		}
		return len(ws) == 3
	}) {
		ws, _ := e.reader.Repo.ListWorkers(context.Background())
		t.Fatalf("worker capacity leaked after the storm: %+v", ws)
	}
	if atKill == 0 {
		t.Fatal("the coordinator was killed with no attempt in flight: the scenario was vacuous")
	}
	t.Logf("%d jobs stored, kill after >= %d acknowledged submissions with %d attempts RUNNING", len(ids), killAfter, atKill)
}

func (e *realEnv) allJobIDs() []string {
	rows, err := e.pg.Pool.Query(context.Background(), "SELECT id FROM jobs ORDER BY created_at")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

// runningAttempts counts RUNNING attempts straight from the database; it never fails the test (it
// is called from a helper goroutine) and returns 0 on error.
func (e *realEnv) runningAttempts() int {
	var n int
	if err := e.pg.Pool.QueryRow(context.Background(), "SELECT count(*) FROM attempts WHERE state = 'RUNNING'").Scan(&n); err != nil {
		return 0
	}
	return n
}
