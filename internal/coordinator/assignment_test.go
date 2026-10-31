package coordinator_test

import (
	"context"
	"errors"
	"math/rand"
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
	return p.InMemoryRepository.AssignQueuedJobs(ctx, params)
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
	svc.SetAssignmentSettings(coordinator.DefaultAssignmentSettings())
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

// Property: the sub-state model. Random submit, poll, pass, cancel, reap, stall, heartbeat,
// finish and clock advance. After every step the in-memory state must satisfy INV-1, INV-6,
// INV-8 and INV-10. INV-3 is checked as far as a poll's result shows it (the claiming worker holds
// the attempt). The liveness half of INV-14 is covered by TestFault_AssignmentPassStalled_JobRunsWithinBound.
func TestProperty_AssignmentSubState_ModelConformance(t *testing.T) {
	const iterations = 1000
	reservations, rrClaims := 0, 0
	defer func() {
		if reservations == 0 || rrClaims == 0 {
			t.Errorf("model exercised no reservations (%d) or round-robin claims (%d); the property is vacuous", reservations, rrClaims)
		}
	}()
	for seed := int64(1); seed <= iterations; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		clk := newMockClock(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
		repo := newPassFaultRepo(clk)
		svc := newRRService(t, repo, clk)
		ctx := context.Background()
		workers := []string{"w_a", "w_b", "w_c"}
		for _, w := range workers {
			rrHeartbeat(t, svc, w, "echo")
		}
		var jobs []string
		cancelled := map[string]bool{}
		running := map[string]string{} // job -> lease token, for finishing

		for step := 0; step < 40; step++ {
			w := workers[rnd.Intn(len(workers))]
			switch rnd.Intn(8) {
			case 0:
				jobs = append(jobs, rrSubmit(t, svc, "echo").ID)
			case 1, 2:
				got, err := svc.PollWork(ctx, w)
				if err == nil {
					if cancelled[got.JobID] {
						t.Fatalf("seed %d step %d: cancelled job %s was claimed", seed, step, got.JobID)
					}
					if got.Policy == domain.DispatchPolicyRoundRobin {
						rrClaims++
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
				rrHeartbeat(t, svc, w, "echo")
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
			checkSubStateInvariants(t, repo, clk, seed, step, workers, jobs)
		}
	}
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

func checkSubStateInvariants(t *testing.T, repo *passFaultRepo, clk *mockClock, seed int64, step int, workers, jobs []string) {
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
	for w, n := range perWorker {
		if n > 1 {
			t.Fatalf("seed %d step %d: INV-10: worker %s holds %d live reservations", seed, step, w, n)
		}
		for _, id := range jobs {
			job, _ := repo.GetJob(ctx, id)
			if job.AssignedWorkerID != nil && *job.AssignedWorkerID == w && job.AssignmentExpiresAt != nil && job.AssignmentExpiresAt.After(now) {
				for _, a := range mustAttempts(t, repo, jobs) {
					if a.WorkerID == w && a.State == domain.AttemptRunning {
						t.Fatalf("seed %d step %d: INV-10: live reservation to %s while it runs attempt %s", seed, step, w, a.ID)
					}
				}
			}
		}
	}
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
