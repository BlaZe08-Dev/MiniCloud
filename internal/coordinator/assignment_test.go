package coordinator_test

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

var errPassFault = errors.New("injected assignment pass failure")

// passFaultRepo wraps the in-memory repository and can make every assignment pass fail. It stands
// in for a stalled or failing pass (ADR-0017 FS-21) and, when failing is switched off again, for
// the recovery of the pass after a restart or an outage (FS-23).
type passFaultRepo struct {
	*storage.InMemoryRepository
	mu      sync.Mutex
	failing bool
	passes  int
	// afterPass, when set, runs after every pass that ran to completion on the inner repository
	// (not failed, not skipped), outside p.mu. The property test snapshots the reservations there.
	afterPass func()
}

func (p *passFaultRepo) setFailing(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failing = v
}

func (p *passFaultRepo) passCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.passes
}

func (p *passFaultRepo) AssignQueuedJobs(ctx context.Context, params storage.AssignParams) (storage.AssignResult, error) {
	p.mu.Lock()
	p.passes++
	fail := p.failing
	p.mu.Unlock()
	if fail {
		return storage.AssignResult{}, errPassFault
	}
	res, err := p.InMemoryRepository.AssignQueuedJobs(ctx, params)
	if err == nil && !res.Skipped && p.afterPass != nil {
		p.afterPass()
	}
	return res, err
}

const (
	rrWindow      = 5 * time.Second
	rrTimeout     = 10 * time.Second
	rrFallbackAge = 30 * time.Second
	rrInterval    = 1 * time.Second
)

// newRRService builds a round-robin coordinator over repo with the ADR-0017 defaults.
func newRRService(t *testing.T, repo storage.Repository, clk *mockClock) *coordinator.Service {
	t.Helper()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	svc.SetClock(clk)
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		t.Fatalf("SetDispatchPolicy: %v", err)
	}
	if err := svc.SetAssignmentSettings(coordinator.DefaultAssignmentSettings()); err != nil {
		t.Fatalf("SetAssignmentSettings: %v", err)
	}
	return svc
}

func rrSubmit(t *testing.T, svc *coordinator.Service, taskType string) *domain.Job {
	t.Helper()
	job, err := svc.SubmitJob(context.Background(), taskType, []byte(`{}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	return job
}

func rrHeartbeat(t *testing.T, svc *coordinator.Service, workerID string, tasks ...string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{
		ID: workerID, Version: "v1.0.0", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: tasks,
	}); err != nil {
		t.Fatalf("RegisterWorker(%s): %v", workerID, err)
	}
	if err := svc.Heartbeat(ctx, workerID, 1); err != nil {
		t.Fatalf("Heartbeat(%s): %v", workerID, err)
	}
}

// rrHeartbeatDeclared registers a worker that declares executes_concurrently with the given slots
// (ADR-0020 Section 2.6), then heartbeats it.
func rrHeartbeatDeclared(t *testing.T, svc *coordinator.Service, workerID string, slots int, tasks ...string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{
		ID: workerID, Version: "v0.1.0", TotalSlots: slots, Status: domain.WorkerStatusHealthy, SupportedTasks: tasks, ExecutesConcurrently: true,
	}); err != nil {
		t.Fatalf("RegisterWorker(%s): %v", workerID, err)
	}
	if err := svc.Heartbeat(ctx, workerID, slots); err != nil {
		t.Fatalf("Heartbeat(%s): %v", workerID, err)
	}
}

func newPassFaultRepo(clk *mockClock) *passFaultRepo {
	return &passFaultRepo{InMemoryRepository: storage.NewInMemoryRepository(storage.WithClock(clk))}
}

// INV-6: no reservation stays past its expiry plus one pass interval while the pass runs. The
// expired reservation is cleared by the next pass, counted as a lapse, and an event is written.
func TestCoordinator_AssignmentSweep_RevokesWithinOneInterval(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	ctx := context.Background()
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	if _, err := svc.SubmitJob(ctx, "echo", []byte(`{}`), 30, 3); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	jobs, _ := repo.ListQueuedJobs(ctx)
	jobID := jobs[0].ID
	if err := repo.RecordPoll(ctx, "w_1"); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}
	if res, err := repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: rrWindow, WorkerTimeout: rrTimeout, BatchLimit: 50, MaxLapses: 3}); err != nil || res.Reserved != 1 {
		t.Fatalf("pass: reserved=%d err=%v", res.Reserved, err)
	}

	clk.Advance(rrWindow + time.Millisecond)
	if _, err := repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: rrWindow, WorkerTimeout: rrTimeout, BatchLimit: 50, MaxLapses: 3}); err != nil {
		t.Fatalf("pass after expiry: %v", err)
	}
	job, err := repo.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.AssignedWorkerID != nil {
		t.Fatalf("expired reservation still set after one pass interval (%v)", rrInterval)
	}
	if job.AssignmentLapses != 1 {
		t.Fatalf("assignment_lapses = %d, want 1", job.AssignmentLapses)
	}
}

// Poll-time refresh, single flight (INV-23, memory): an attempt already in flight blocks a second
// attempt. White-box: it reads the in-flight flag the poll path sets.
func TestPoll_RefreshSingleFlight(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")
	clk.Advance(rrFallbackAge + time.Second)
	rrHeartbeat(t, svc, "w_1", "echo")

	// Control: with no attempt in flight, the stale poll does start one.
	before := repo.passCount()
	_, _ = svc.PollWork(context.Background(), "w_1")
	if got := repo.passCount(); got != before+1 {
		t.Fatalf("control: stale poll with no attempt in flight ran %d attempts, want 1", got-before)
	}

	// Stale again, with an attempt in flight: the poll must not start another.
	clk.Advance(rrFallbackAge + time.Second)
	rrHeartbeat(t, svc, "w_1", "echo")
	before = repo.passCount()
	svc.SetRefreshInFlightForTest(true)
	_, _ = svc.PollWork(context.Background(), "w_1")
	if got := repo.passCount(); got != before {
		t.Fatalf("a poll started a refresh attempt while one was in flight: passes %d -> %d", before, got)
	}
}

// Restart (INV-19, memory): a fresh coordinator with a stale pass heartbeat refreshes before the
// age arm. The poll's worker is the one the refresh reserves to, so it claims by its own arm.
func TestPoll_AgeArm_AfterRestart_RefreshesFirst(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	before := newRRService(t, repo, clk)
	rrHeartbeat(t, before, "w_1", "echo")
	rrSubmit(t, before, "echo")

	clk.Advance(rrFallbackAge + time.Second)
	// The restarted coordinator starts with zeroed refresh state.
	restarted := newRRService(t, repo, clk)
	rrHeartbeat(t, restarted, "w_1", "echo")
	got, err := restarted.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("PollWork after restart: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("first poll after restart claimed under %q, want round_robin (age arm must not claim)", got.Policy)
	}
}

// Outage (INV-19, memory): the database failed, then recovered. The first poll after recovery
// refreshes and claims by its own arm, not by the age arm.
func TestPoll_AgeArm_AfterOutage_RefreshesFirst(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")

	repo.setFailing(true)
	if _, err := svc.PollWork(context.Background(), "w_1"); errors.Is(err, errPassFault) {
		t.Fatalf("poll during outage should not surface a pass error")
	}
	clk.Advance(rrFallbackAge + time.Second)
	repo.setFailing(false)
	rrHeartbeat(t, svc, "w_1", "echo")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("first poll after outage: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("first poll after outage claimed under %q, want round_robin", got.Policy)
	}
}

// Rate limit (INV-24, memory): a failed attempt, then a poll inside the rate window. The poll is
// rate-limited, so the age arm is closed for it even though refreshLastFailed is still set.
func TestPoll_RateLimitedPoll_NoAgeClaim(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrHeartbeat(t, svc, "w_2", "echo")
	rrSubmit(t, svc, "echo")
	rrSubmit(t, svc, "echo")

	clk.Advance(rrFallbackAge + time.Second)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrHeartbeat(t, svc, "w_2", "echo")
	repo.setFailing(true)
	// This poll runs the attempt, which fails, so its own age arm is open and it claims one job.
	first, err := svc.PollWork(context.Background(), "w_1")
	if err != nil || first.FallbackReason != domain.FallbackReasonAge {
		t.Fatalf("first poll: err=%v reason=%v, want an age claim", err, first)
	}

	// Recovery 1 ms later: the next poll is inside the rate window and must not claim by age.
	repo.setFailing(false)
	clk.Advance(time.Millisecond)
	rrHeartbeat(t, svc, "w_2", "echo")
	if _, err := svc.PollWork(context.Background(), "w_2"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("rate-limited poll claimed by age: err=%v", err)
	}
}

// Rate limit (INV-24, memory): the first poll after an outage attempts, and the attempt closes
// the age arm.
func TestPoll_FirstPollAfterOutage_AttemptsAndCloses(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")

	repo.setFailing(true)
	if _, err := svc.PollWork(context.Background(), "w_1"); err == nil {
		t.Fatalf("poll with no job during an outage unexpectedly claimed")
	}
	clk.Advance(rrFallbackAge + time.Second)
	repo.setFailing(false)
	rrHeartbeat(t, svc, "w_1", "echo")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("first poll after outage: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("policy %q, want round_robin (attempt succeeded, age arm closed)", got.Policy)
	}
}

// FS-21 and INV-14 (memory variant): the pass is disabled and the poll's refresh fails too, so the
// age arm is reached. A job submitted and left unassigned is claimed within the bound: the
// fallback age plus one pass interval plus one poll gap (about 3 s, the maximum poll backoff of
// ADR-0017 C17).
func TestFault_AssignmentPassStalled_JobRunsWithinBound(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	repo.setFailing(true)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")
	start := clk.Now()
	bound := rrFallbackAge + rrInterval + 3*time.Second

	for {
		rrHeartbeat(t, svc, "w_1", "echo")
		got, err := svc.PollWork(context.Background(), "w_1")
		if err == nil {
			if got.Policy != domain.DispatchPolicyRoundRobinFallback || got.FallbackReason != domain.FallbackReasonAge {
				t.Fatalf("claimed under %q/%q, want round_robin_fallback/age", got.Policy, got.FallbackReason)
			}
			if waited := clk.Now().Sub(start); waited > bound {
				t.Fatalf("claimed after %v, bound is %v", waited, bound)
			}
			return
		}
		if clk.Now().Sub(start) > bound+10*time.Second {
			t.Fatalf("job never claimed; last error %v", err)
		}
		clk.Advance(time.Second)
	}
}

// TestConformance_PollResponse_PolicyField: the 200 body reports the policy per path.
func TestConformance_PollResponse_PolicyField(t *testing.T) {
	t.Run("fifo_pull", func(t *testing.T) {
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := storage.NewInMemoryRepository(storage.WithClock(clk))
		svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
		svc.SetClock(clk)
		rrHeartbeat(t, svc, "w_1", "echo")
		rrSubmit(t, svc, "echo")
		got, err := svc.PollWork(context.Background(), "w_1")
		if err != nil || got.Policy != domain.DispatchPolicyFifoPull || got.FallbackReason != "" {
			t.Fatalf("fifo poll: err=%v policy=%q reason=%q", err, got.Policy, got.FallbackReason)
		}
	})
	t.Run("round_robin own reservation", func(t *testing.T) {
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := storage.NewInMemoryRepository(storage.WithClock(clk))
		svc := newRRService(t, repo, clk)
		rrHeartbeat(t, svc, "w_1", "echo")
		rrSubmit(t, svc, "echo")
		got, err := svc.PollWork(context.Background(), "w_1")
		if err != nil || got.Policy != domain.DispatchPolicyRoundRobin || got.FallbackReason != "" {
			t.Fatalf("own poll: err=%v policy=%q reason=%q", err, got.Policy, got.FallbackReason)
		}
	})
	t.Run("round_robin_fallback age", func(t *testing.T) {
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := newPassFaultRepo(clk)
		svc := newRRService(t, repo, clk)
		repo.setFailing(true)
		rrHeartbeat(t, svc, "w_1", "echo")
		rrSubmit(t, svc, "echo")
		clk.Advance(rrFallbackAge + time.Second)
		rrHeartbeat(t, svc, "w_1", "echo")
		got, err := svc.PollWork(context.Background(), "w_1")
		if err != nil || got.Policy != domain.DispatchPolicyRoundRobinFallback || got.FallbackReason != domain.FallbackReasonAge {
			t.Fatalf("age poll: err=%v policy=%q reason=%q", err, got.Policy, got.FallbackReason)
		}
	})
	t.Run("round_robin_fallback lapses", func(t *testing.T) {
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := storage.NewInMemoryRepository(storage.WithClock(clk))
		svc := newRRService(t, repo, clk)
		rrHeartbeat(t, svc, "w_1", "echo")
		job := rrSubmit(t, svc, "echo")
		stored, _ := repo.GetJob(context.Background(), job.ID)
		stored.AssignmentLapses = 3
		if err := repo.UpdateJob(context.Background(), stored); err != nil {
			t.Fatalf("UpdateJob: %v", err)
		}
		got, err := svc.PollWork(context.Background(), "w_1")
		if err != nil || got.Policy != domain.DispatchPolicyRoundRobinFallback || got.FallbackReason != domain.FallbackReasonLapses {
			t.Fatalf("lapses poll: err=%v policy=%q reason=%q", err, got.Policy, got.FallbackReason)
		}
	})
}

// Property: the sub-state model (ADR-0017 Section 6.4, 1,000 iterations). Random submit, poll,
// pass, cancel, reap, stall, heartbeat, finish and clock advance, one seed per iteration. After
// every step the in-memory state must satisfy:
//   - INV-1: at most one RUNNING attempt per job;
//   - INV-3: a poll never claims a job that is live-reserved to another worker, and a round_robin
//     claim was the claimant's own live reservation. When the poll ran the refresh pass, the
//     reservations are those read right after that pass, before the claim (P4-08g H2);
//   - INV-6: right after a successful pass, no reservation is held at or past its expiry;
//   - INV-8: a cancelled job is never claimed and holds no reservation;
//   - INV-10r (ADR-0020 Section 2.4, replaces INV-10): for every worker, active attempts plus live
//     reservations never exceed its assignable slots (1 for the undeclared w_a and w_b, 3 for w_c,
//     which declares executes_concurrently), and no worker holds more than
//     domain.MaxReservationsPerWorker live reservations.
//
// INV-14 (liveness) closes every iteration: the pass is stalled, a fresh worker polls once per
// interval, and every job still QUEUED must be claimed within FALLBACK_AGE plus one interval per job
// plus the 3 s poll gap. The failing seed is logged; MINICLOUD_PROPERTY_SEED=<n> replays one seed.
func TestProperty_AssignmentSubState_ModelConformance(t *testing.T) {
	const iterations = 1000
	first, last := int64(1), int64(iterations)
	if v := os.Getenv("MINICLOUD_PROPERTY_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("MINICLOUD_PROPERTY_SEED=%q: %v", v, err)
		}
		first, last = n, n
	}
	reservations, rrClaims, drained, maxReserved := 0, 0, 0, 0
	claims, checkedAfterPass := 0, 0
	var current int64
	defer func() {
		if t.Failed() {
			t.Logf("property test failed at seed %d: rerun with MINICLOUD_PROPERTY_SEED=%d", current, current)
			return
		}
		t.Logf("claims %d, of which INV-3 checked against the post-refresh reservations %d (all %d are checked)", claims, checkedAfterPass, claims)
		if first == 1 && (reservations == 0 || rrClaims == 0 || drained == 0 || checkedAfterPass == 0 || maxReserved < 2) {
			t.Errorf("model exercised no reservations (%d), round-robin claims (%d), stalled drains (%d), claims after a refresh (%d) or a worker holding two or more reservations (max %d); the property is vacuous", reservations, rrClaims, drained, checkedAfterPass, maxReserved)
		}
	}()
	for seed := first; seed <= last; seed++ {
		current = seed
		rnd := rand.New(rand.NewSource(seed))
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := newPassFaultRepo(clk)
		svc := newRRService(t, repo, clk)
		ctx := context.Background()
		var jobs []string
		var postPass map[string]reservation // reservations right after the last completed pass
		repo.afterPass = func() { postPass = reservationsOf(t, repo, jobs) }
		workers := []string{"w_a", "w_b", "w_c"}
		heartbeat := func(w string) {
			if w == "w_c" {
				rrHeartbeatDeclared(t, svc, w, 3, "echo")
				return
			}
			rrHeartbeat(t, svc, w, "echo")
		}
		for _, w := range workers {
			heartbeat(w)
		}
		cancelled := map[string]bool{}
		running := map[string]string{} // job -> lease token, for finishing

		for step := 0; step < 40; step++ {
			w := workers[rnd.Intn(len(workers))]
			switch rnd.Intn(8) {
			case 0:
				jobs = append(jobs, rrSubmit(t, svc, "echo").ID)
			case 1, 2:
				before := reservationsOf(t, repo, jobs)
				postPass = nil
				pollAt := clk.Now()
				got, err := svc.PollWork(ctx, w)
				if err == nil {
					if cancelled[got.JobID] {
						t.Fatalf("seed %d step %d: INV-8: cancelled job %s was claimed", seed, step, got.JobID)
					}
					// What the claim saw: the state right after the poll's own pass, if it ran one.
					seen := before
					if postPass != nil {
						seen = postPass
						checkedAfterPass++
					}
					claims++
					if r, ok := seen[got.JobID]; ok && r.holder != w && r.expires.After(pollAt) {
						t.Fatalf("seed %d step %d: INV-3: %s claimed %s, live-reserved to %s until %v", seed, step, w, got.JobID, r.holder, r.expires)
					}
					if got.Policy == domain.DispatchPolicyRoundRobin {
						rrClaims++
						r, ok := seen[got.JobID]
						if !ok || r.holder != w || !r.expires.After(pollAt) {
							t.Fatalf("seed %d step %d: INV-3: round_robin claim of %s by %s without its live reservation (%+v)", seed, step, got.JobID, w, r)
						}
						att, _ := repo.GetAttempt(ctx, got.AttemptID)
						if att.WorkerID != w {
							t.Fatalf("seed %d step %d: claim by %s recorded worker %s", seed, step, w, att.WorkerID)
						}
					}
					running[got.JobID] = got.LeaseToken
				}
			case 3:
				if res, err := repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: rrWindow, WorkerTimeout: rrTimeout, BatchLimit: 50, MaxLapses: 3}); err == nil {
					reservations += res.Reserved
					if !res.Skipped {
						for id, r := range reservationsOf(t, repo, jobs) {
							if !r.expires.After(clk.Now()) {
								t.Fatalf("seed %d step %d: INV-6: job %s still reserved to %s at expiry %v after a pass at %v", seed, step, id, r.holder, r.expires, clk.Now())
							}
						}
					}
				}
			case 4:
				if len(jobs) > 0 {
					id := jobs[rnd.Intn(len(jobs))]
					if _, err := svc.CancelJob(ctx, id); err == nil {
						cancelled[id] = true
					}
				}
			case 5:
				repo.setFailing(rnd.Intn(2) == 0)
			case 6:
				heartbeat(w)
			case 7:
				for id, token := range running {
					att := attemptOf(t, repo, id)
					if att != nil && att.State == domain.AttemptRunning {
						_, _ = svc.CommitResult(ctx, id, att.ID, token, att.WorkerID, 0, "ok", "", "")
					}
					delete(running, id)
					break
				}
			}
			clk.Advance(time.Duration(rnd.Intn(3)) * time.Second)
			if rnd.Intn(4) == 0 {
				_, _ = svc.ReapInactiveWorkers(ctx)
			}
			if m := checkSubStateInvariants(t, repo, clk, seed, step, workers, jobs); m > maxReserved {
				maxReserved = m
			}
			for _, id := range jobs {
				if j, _ := repo.GetJob(ctx, id); j.State == domain.StateCancelled && j.AssignedWorkerID != nil {
					t.Fatalf("seed %d step %d: INV-8: cancelled job %s holds a reservation to %s", seed, step, id, *j.AssignedWorkerID)
				}
			}
		}
		if livenessAfterStall(t, repo, svc, clk, seed, jobs) {
			drained++
		}
	}
}

type reservation struct {
	holder  string
	expires time.Time
}

// reservationsOf returns the live and expired-but-held reservations of the given jobs.
func reservationsOf(t *testing.T, repo storage.Repository, jobs []string) map[string]reservation {
	t.Helper()
	out := map[string]reservation{}
	for _, id := range jobs {
		j, err := repo.GetJob(context.Background(), id)
		if err != nil {
			t.Fatalf("GetJob(%s): %v", id, err)
		}
		if j.AssignedWorkerID != nil && j.AssignmentExpiresAt != nil {
			out[id] = reservation{holder: *j.AssignedWorkerID, expires: *j.AssignmentExpiresAt}
		}
	}
	return out
}

// livenessAfterStall is INV-14: with the pass stalled, every job still QUEUED is claimed by a fresh
// polling worker within FALLBACK_AGE + one interval per job + the 3 s poll gap. The worker polls
// once per second and commits each claim, so it is free again for the next interval. It returns
// whether any job had to be drained.
func livenessAfterStall(t *testing.T, repo *passFaultRepo, svc *coordinator.Service, clk *mockClock, seed int64, jobs []string) bool {
	t.Helper()
	ctx := context.Background()
	var queued []string
	for _, id := range jobs {
		if j, _ := repo.GetJob(ctx, id); j.State == domain.StateQueued {
			queued = append(queued, id)
		}
	}
	if len(queued) == 0 {
		return false
	}
	repo.setFailing(true)
	start := clk.Now()
	bound := rrFallbackAge + time.Duration(len(queued)+1)*rrInterval + 3*time.Second
	left := map[string]bool{}
	for _, id := range queued {
		left[id] = true
	}
	for len(left) > 0 {
		if waited := clk.Now().Sub(start); waited > bound {
			t.Fatalf("seed %d: INV-14: %d of %d queued jobs unclaimed after %v with the pass stalled (bound %v)", seed, len(left), len(queued), waited, bound)
		}
		rrHeartbeat(t, svc, "w_drain", "echo")
		if got, err := svc.PollWork(ctx, "w_drain"); err == nil {
			delete(left, got.JobID)
			_, _ = svc.CommitResult(ctx, got.JobID, got.AttemptID, got.LeaseToken, "w_drain", 0, "ok", "", "")
		}
		clk.Advance(time.Second)
	}
	return true
}

// attemptOf returns the job's current attempt, or nil.
func attemptOf(t *testing.T, repo storage.Repository, jobID string) *domain.Attempt {
	t.Helper()
	job, err := repo.GetJob(context.Background(), jobID)
	if err != nil || job.CurrentAttemptID == "" {
		return nil
	}
	att, err := repo.GetAttempt(context.Background(), job.CurrentAttemptID)
	if err != nil {
		return nil
	}
	return att
}

func checkSubStateInvariants(t *testing.T, repo *passFaultRepo, clk *mockClock, seed int64, step int, workers, jobs []string) int {
	t.Helper()
	ctx := context.Background()
	now := clk.Now()
	perWorker := map[string]int{}
	for _, id := range jobs {
		attempts, err := repo.ListAttemptsByJob(ctx, id)
		if err != nil {
			t.Fatalf("ListAttemptsByJob: %v", err)
		}
		running := 0
		for _, a := range attempts {
			if a.State == domain.AttemptRunning {
				running++
			}
		}
		if running > 1 {
			t.Fatalf("seed %d step %d: INV-1: job %s has %d RUNNING attempts", seed, step, id, running)
		}
		job, _ := repo.GetJob(ctx, id)
		if job.AssignedWorkerID != nil && job.AssignmentExpiresAt != nil && job.AssignmentExpiresAt.After(now) {
			perWorker[*job.AssignedWorkerID]++
			if job.State != domain.StateQueued && job.State != domain.StateRetrying {
				t.Fatalf("seed %d step %d: reservation on job %s in state %s", seed, step, id, job.State)
			}
		}
	}
	// INV-10r: active + live reservations <= assignable slots, live reservations <= the cap.
	attempts := mustAttempts(t, repo, jobs)
	maxReserved := 0
	for _, w := range workers {
		wk, err := repo.GetWorker(ctx, w)
		if err != nil {
			t.Fatalf("GetWorker(%s): %v", w, err)
		}
		active := 0
		for _, a := range attempts {
			if a.WorkerID == w && (a.State == domain.AttemptRunning || a.State == domain.AttemptPending) {
				active++
			}
		}
		reserved := perWorker[w]
		if reserved > maxReserved {
			maxReserved = reserved
		}
		if s := domain.AssignableSlots(wk.ExecutesConcurrently, wk.TotalSlots); active+reserved > s {
			t.Fatalf("seed %d step %d: INV-10r: worker %s has %d active attempts + %d live reservations, above its %d assignable slots", seed, step, w, active, reserved, s)
		}
		if reserved > domain.MaxReservationsPerWorker {
			t.Fatalf("seed %d step %d: INV-10r: worker %s holds %d live reservations, above the cap %d", seed, step, w, reserved, domain.MaxReservationsPerWorker)
		}
	}
	return maxReserved
}

func mustAttempts(t *testing.T, repo *passFaultRepo, jobs []string) []*domain.Attempt {
	t.Helper()
	var out []*domain.Attempt
	for _, id := range jobs {
		as, err := repo.ListAttemptsByJob(context.Background(), id)
		if err != nil {
			t.Fatalf("ListAttemptsByJob: %v", err)
		}
		out = append(out, as...)
	}
	return out
}

// noAssignmentRepo implements storage.Repository but not storage.AssignmentRepository. Embedding
// the interface promotes only the Repository methods, so it stands in for a repository without
// the assignment surface, such as the PostgreSQL repository in slice 5a.
type noAssignmentRepo struct {
	storage.Repository
}

// SetDispatchPolicy must refuse round_robin when the repository cannot run an assignment pass,
// so the failure is at configuration time and not on the first poll. fifo_pull stays accepted.
func TestSetDispatchPolicy_RoundRobinRequiresAssignmentRepository(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := &noAssignmentRepo{Repository: storage.NewInMemoryRepository(storage.WithClock(clk))}
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)

	err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin)
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("round_robin on a repository without assignment: err=%v, want ErrInvalidRequest", err)
	}
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyFifoPull); err != nil {
		t.Fatalf("fifo_pull rejected on a repository without assignment: %v", err)
	}
}

// F-5a-1 (P4-08b audit): round_robin selected without SetAssignmentSettings must run with the
// ADR-0017 Section 5.5 defaults, not the zero AssignmentSettings. With the zero value, MaxLapses 0
// makes every job lapse-claimable and BatchLimit 0 makes the pass reserve nothing, so every poll
// would claim under round_robin_fallback with reason "lapses". The ADR outcome for one HEALTHY
// polling worker and one job is an own-reservation claim (policy round_robin).
func TestSetDispatchPolicy_RoundRobinWithoutSettings_UsesDefaults(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	svc.SetClock(clk)
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		t.Fatalf("SetDispatchPolicy: %v", err)
	}
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("PollWork: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin || got.FallbackReason != "" {
		t.Fatalf("round_robin without settings claimed under policy=%q reason=%q, want round_robin and no reason", got.Policy, got.FallbackReason)
	}
}

// AssignmentSettings.Validate applies the ADR-0017 Section 5.5 ranges and the A1 cross-checks.
// Each row breaks exactly one rule of the defaults; the two "accepted" rows pin the equality
// edges that the rules allow.
func TestAssignmentSettings_Validate(t *testing.T) {
	type mod func(*coordinator.AssignmentSettings)
	cases := []struct {
		name  string
		mod   mod
		valid bool
	}{
		{"defaults", func(*coordinator.AssignmentSettings) {}, true},
		{"zero value", func(a *coordinator.AssignmentSettings) { *a = coordinator.AssignmentSettings{} }, false},
		{"accept window below 5s", func(a *coordinator.AssignmentSettings) { a.AcceptWindow = 5*time.Second - time.Nanosecond }, false},
		{"accept window above 30s", func(a *coordinator.AssignmentSettings) { a.AcceptWindow = 31 * time.Second }, false},
		{"interval below 100ms", func(a *coordinator.AssignmentSettings) { a.Interval = 99 * time.Millisecond }, false},
		{"interval above 10s", func(a *coordinator.AssignmentSettings) { a.Interval = 10*time.Second + time.Millisecond }, false},
		{"max lapses zero", func(a *coordinator.AssignmentSettings) { a.MaxLapses = 0 }, false},
		{"max lapses above 10", func(a *coordinator.AssignmentSettings) { a.MaxLapses = 11 }, false},
		{"fallback age below the lapse path minimum", func(a *coordinator.AssignmentSettings) { a.FallbackAge = 17 * time.Second }, false},
		{"fallback age above 10 min", func(a *coordinator.AssignmentSettings) { a.FallbackAge = 601 * time.Second }, false},
		{"batch limit zero", func(a *coordinator.AssignmentSettings) { a.BatchLimit = 0 }, false},
		{"batch limit above 500", func(a *coordinator.AssignmentSettings) { a.BatchLimit = 501 }, false},
		{"min yield below 1ms", func(a *coordinator.AssignmentSettings) { a.MinYield = 0 }, false},
		{"min yield above 1s", func(a *coordinator.AssignmentSettings) { a.MinYield = 1001 * time.Millisecond }, false},
		{"min yield above interval", func(a *coordinator.AssignmentSettings) {
			a.Interval = 100 * time.Millisecond
			a.MinYield = 200 * time.Millisecond
		}, false},
		// A1 rule 1. 40 s clears the lapse-path minimum (3 x 11 s) and the rule 2 bound.
		{"interval above accept window", func(a *coordinator.AssignmentSettings) {
			a.Interval = 6 * time.Second
			a.FallbackAge = 40 * time.Second
		}, false},
		// A1 rule 1 equality. 31 s clears 3 x 10 s and 2 x 5 s + 5 s.
		{"interval equal to accept window", func(a *coordinator.AssignmentSettings) {
			a.Interval = 5 * time.Second
			a.FallbackAge = 31 * time.Second
		}, true},
		// A1 rule 2 equality. With one lapse the lapse-path minimum is 6 s, so only rule 2 fails.
		{"fallback age equal to 2 x interval plus window", func(a *coordinator.AssignmentSettings) {
			a.MaxLapses = 1
			a.FallbackAge = 7 * time.Second
		}, false},
		{"fallback age just above 2 x interval plus window", func(a *coordinator.AssignmentSettings) {
			a.MaxLapses = 1
			a.FallbackAge = 8 * time.Second
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := coordinator.DefaultAssignmentSettings()
			c.mod(&a)
			err := a.Validate()
			if c.valid && err != nil {
				t.Fatalf("valid settings rejected: %v", err)
			}
			if !c.valid && !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("invalid settings %+v: err=%v, want ErrInvalidRequest", a, err)
			}
		})
	}
}

// SetAssignmentSettings rejects invalid settings and keeps the previous ones in force: after a
// rejected zero value, round_robin still claims by the own reservation under the defaults.
func TestSetAssignmentSettings_RejectsInvalidAndKeepsPrevious(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	svc := newRRService(t, repo, clk)
	if err := svc.SetAssignmentSettings(coordinator.AssignmentSettings{}); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("zero AssignmentSettings: err=%v, want ErrInvalidRequest", err)
	}
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("PollWork: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin || got.FallbackReason != "" {
		t.Fatalf("after a rejected update: policy=%q reason=%q, want round_robin and no reason", got.Policy, got.FallbackReason)
	}
}

// SetDispatchPolicy("round_robin") refuses settings that fail Validate, and leaves the policy
// unchanged. fifo_pull does not read the settings and stays selectable.
func TestSetDispatchPolicy_RoundRobinRejectsInvalidSettings(t *testing.T) {
	repo := storage.NewInMemoryRepository()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	svc.SetAssignmentSettingsUncheckedForTest(coordinator.AssignmentSettings{})
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("round_robin with zero settings: err=%v, want ErrInvalidRequest", err)
	}
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyFifoPull); err != nil {
		t.Fatalf("fifo_pull rejected: %v", err)
	}
}

// SetDispatchPolicy accepts only the two setting names (ADR-0017 Section 5.2). The output-only
// name round_robin_fallback and the empty name are rejected (F-5a-11).
func TestSetDispatchPolicy_RejectsInvalidPolicyName(t *testing.T) {
	repo := storage.NewInMemoryRepository()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	for _, name := range []string{domain.DispatchPolicyRoundRobinFallback, "", "random"} {
		if err := svc.SetDispatchPolicy(name); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("SetDispatchPolicy(%q): err=%v, want ErrInvalidRequest", name, err)
		}
	}
}

// assignOnlyRepo has the assignment surface but not storage.AssignmentPassStaleness: it embeds the
// two interfaces, not the in-memory type, so AssignmentPassStale is not promoted.
type assignOnlyRepo struct {
	storage.Repository
	storage.AssignmentRepository
}

// SetDispatchPolicy("round_robin") must refuse a repository that implements
// storage.AssignmentRepository but not storage.AssignmentPassStaleness: without it the poll-time
// refresh could not judge staleness on the repository clock (ADR-0017 implementation note 8).
// Control: the same in-memory repository, unwrapped, is accepted.
func TestSetDispatchPolicy_RoundRobinRequiresPassStaleness(t *testing.T) {
	clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	mem := storage.NewInMemoryRepository(storage.WithClock(clk))
	var _ storage.AssignmentRepository = assignOnlyRepo{}
	if _, ok := any(assignOnlyRepo{}).(storage.AssignmentPassStaleness); ok {
		t.Fatalf("test setup: assignOnlyRepo must not implement AssignmentPassStaleness")
	}

	svc := coordinator.NewService(assignOnlyRepo{Repository: mem, AssignmentRepository: mem}, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("round_robin without pass staleness: err=%v, want ErrInvalidRequest", err)
	}
	control := coordinator.NewService(mem, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	if err := control.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		t.Fatalf("control: round_robin on the in-memory repository: %v", err)
	}
}
