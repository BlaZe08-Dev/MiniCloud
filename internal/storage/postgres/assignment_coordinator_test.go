package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// PostgreSQL variants of the slice 5a poll-time refresh tests (ADR-0017 INV-14, INV-19, INV-24):
// the coordinator's round_robin poll over the PostgreSQL repository. Pass staleness, the pass
// silence of the age arm and job age are all judged on the database clock (implementation note 8);
// the coordinator's own clock (pgRRClock) drives only the refresh rate limit. Letting time pass
// moves both: the stored timestamps back (shiftTimestampsBack) and the process clock forward.

var errPGPassFault = errors.New("injected assignment pass failure")

// pgPassFaultRepo wraps the PostgreSQL repository and can make every assignment pass fail without
// touching the database, which stands in for a stalled or failing pass (FS-21) and, once switched
// off, for its recovery after a restart or an outage (FS-23).
type pgPassFaultRepo struct {
	*postgres.Repository
	mu      sync.Mutex
	failing bool
}

func (p *pgPassFaultRepo) setFailing(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failing = v
}

func (p *pgPassFaultRepo) AssignQueuedJobs(ctx context.Context, params storage.AssignParams) (storage.AssignResult, error) {
	p.mu.Lock()
	fail := p.failing
	p.mu.Unlock()
	if fail {
		return storage.AssignResult{}, errPGPassFault
	}
	return p.Repository.AssignQueuedJobs(ctx, params)
}

type pgRRClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *pgRRClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

type pgRRFixture struct {
	pool *pgxpool.Pool
	repo *pgPassFaultRepo
	clk  *pgRRClock
}

const (
	pgRRTimeout     = 10 * time.Second
	pgRRFallbackAge = 30 * time.Second
	pgRRInterval    = 1 * time.Second
)

// newPGRRFixture starts the process clock at database time. Service.SubmitJob stamps created_at
// with the process clock, and PostgreSQL stores that value, so a process clock far from the
// database clock would make job age (the age arm's dispatchable_since) wrong; see the P4-08c
// report, finding on created_at.
func newPGRRFixture(t *testing.T) *pgRRFixture {
	pool, _ := setupTestPool(t)
	return &pgRRFixture{
		pool: pool,
		repo: &pgPassFaultRepo{Repository: postgres.NewRepository(pool)},
		clk:  &pgRRClock{now: dbNow(t, pool).UTC()},
	}
}

// advance lets d pass on both clocks.
func (f *pgRRFixture) advance(t *testing.T, d time.Duration) {
	t.Helper()
	shiftTimestampsBack(t, f.pool, d)
	f.clk.mu.Lock()
	f.clk.now = f.clk.now.Add(d)
	f.clk.mu.Unlock()
}

// service builds a round_robin coordinator with the ADR-0017 defaults. A new service is a
// coordinator restart: its refresh state starts at zero.
func (f *pgRRFixture) service(t *testing.T) *coordinator.Service {
	t.Helper()
	svc := coordinator.NewService(f.repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), pgRRTimeout)
	svc.SetClock(f.clk)
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		t.Fatalf("SetDispatchPolicy: %v", err)
	}
	if err := svc.SetAssignmentSettings(coordinator.DefaultAssignmentSettings()); err != nil {
		t.Fatalf("SetAssignmentSettings: %v", err)
	}
	return svc
}

func pgRRSubmit(t *testing.T, svc *coordinator.Service) {
	t.Helper()
	if _, err := svc.SubmitJob(context.Background(), "echo", []byte(`{}`), 30, 3); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
}

func pgRRHeartbeat(t *testing.T, svc *coordinator.Service, workerID string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{
		ID: workerID, Version: "v1.0.0", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"},
	}); err != nil {
		t.Fatalf("RegisterWorker(%s): %v", workerID, err)
	}
	if err := svc.Heartbeat(ctx, workerID, 1); err != nil {
		t.Fatalf("Heartbeat(%s): %v", workerID, err)
	}
}

// Restart (INV-19, PostgreSQL): a fresh coordinator over a database whose pass heartbeat is older
// than the fallback age, with a job older than the fallback age. The first poll refreshes first,
// on the database clock, and claims by its own arm (round_robin), not by the age arm.
func TestPoll_AgeArm_AfterRestart_RefreshesFirst(t *testing.T) {
	f := newPGRRFixture(t)
	before := f.service(t)
	pgRRHeartbeat(t, before, "w_1")
	pgRRSubmit(t, before)

	f.advance(t, pgRRFallbackAge+time.Second)
	restarted := f.service(t)
	pgRRHeartbeat(t, restarted, "w_1")
	got, err := restarted.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("PollWork after restart: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("first poll after restart claimed under %q/%q, want round_robin (age arm must not claim)", got.Policy, got.FallbackReason)
	}
}

// Outage (INV-19, PostgreSQL): the pass failed, then recovered after the fallback age. The first
// poll after recovery refreshes and claims by its own arm.
func TestPoll_AgeArm_AfterOutage_RefreshesFirst(t *testing.T) {
	f := newPGRRFixture(t)
	svc := f.service(t)
	pgRRHeartbeat(t, svc, "w_1")
	pgRRSubmit(t, svc)

	f.repo.setFailing(true)
	if _, err := svc.PollWork(context.Background(), "w_1"); errors.Is(err, errPGPassFault) {
		t.Fatalf("poll during the outage surfaced the pass error")
	}
	f.advance(t, pgRRFallbackAge+time.Second)
	f.repo.setFailing(false)
	pgRRHeartbeat(t, svc, "w_1")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("first poll after the outage: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("first poll after the outage claimed under %q/%q, want round_robin", got.Policy, got.FallbackReason)
	}
}

// Rate limit (INV-24, PostgreSQL): a failed attempt opens the age arm for its own poll only. The
// next poll, 1 ms later on the process clock, is inside the rate window: it runs no attempt, so
// its age arm stays closed, although the heartbeat is still stale on the database clock and a job
// is still aged.
func TestPoll_RateLimitedPoll_NoAgeClaim(t *testing.T) {
	f := newPGRRFixture(t)
	svc := f.service(t)
	pgRRHeartbeat(t, svc, "w_1")
	pgRRHeartbeat(t, svc, "w_2")
	pgRRSubmit(t, svc)
	pgRRSubmit(t, svc)

	f.advance(t, pgRRFallbackAge+time.Second)
	pgRRHeartbeat(t, svc, "w_1")
	pgRRHeartbeat(t, svc, "w_2")
	f.repo.setFailing(true)
	first, err := svc.PollWork(context.Background(), "w_1")
	if err != nil || first.FallbackReason != domain.FallbackReasonAge {
		t.Fatalf("first poll: err=%v assignment=%+v, want an age claim", err, first)
	}

	f.repo.setFailing(false)
	f.clk.mu.Lock()
	f.clk.now = f.clk.now.Add(time.Millisecond)
	f.clk.mu.Unlock()
	pgRRHeartbeat(t, svc, "w_2")
	if got, err := svc.PollWork(context.Background(), "w_2"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("rate-limited poll: err=%v assignment=%+v, want 204 (no age claim)", err, got)
	}
}

// Rate limit (INV-24, PostgreSQL): the first poll after an outage attempts, and the successful
// attempt closes the age arm.
func TestPoll_FirstPollAfterOutage_AttemptsAndCloses(t *testing.T) {
	f := newPGRRFixture(t)
	svc := f.service(t)
	pgRRHeartbeat(t, svc, "w_1")
	pgRRSubmit(t, svc)

	f.repo.setFailing(true)
	_, _ = svc.PollWork(context.Background(), "w_1")
	f.advance(t, pgRRFallbackAge+time.Second)
	f.repo.setFailing(false)
	pgRRHeartbeat(t, svc, "w_1")
	got, err := svc.PollWork(context.Background(), "w_1")
	if err != nil {
		t.Fatalf("first poll after the outage: %v", err)
	}
	if got.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("policy %q/%q, want round_robin (attempt succeeded, age arm closed)", got.Policy, got.FallbackReason)
	}
}

// FS-21 and INV-14 (PostgreSQL): the pass is disabled and the poll's refresh fails too, so the age
// arm is reached. A submitted job is claimed with round_robin_fallback/age within the bound: the
// fallback age plus one pass interval plus one poll gap (about 3 s, the maximum poll backoff of
// ADR-0017 C17), measured in the time the test lets pass.
func TestFault_AssignmentPassStalled_JobRunsWithinBound(t *testing.T) {
	f := newPGRRFixture(t)
	svc := f.service(t)
	f.repo.setFailing(true)
	pgRRHeartbeat(t, svc, "w_1")
	pgRRSubmit(t, svc)
	start := f.clk.Now()
	bound := pgRRFallbackAge + pgRRInterval + 3*time.Second

	for {
		pgRRHeartbeat(t, svc, "w_1")
		got, err := svc.PollWork(context.Background(), "w_1")
		if err == nil {
			if got.Policy != domain.DispatchPolicyRoundRobinFallback || got.FallbackReason != domain.FallbackReasonAge {
				t.Fatalf("claimed under %q/%q, want round_robin_fallback/age", got.Policy, got.FallbackReason)
			}
			if waited := f.clk.Now().Sub(start); waited > bound {
				t.Fatalf("claimed after %v, bound is %v", waited, bound)
			}
			return
		}
		if !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("poll: unexpected error %v", err)
		}
		if f.clk.Now().Sub(start) > bound+10*time.Second {
			t.Fatalf("job never claimed; last error %v", err)
		}
		f.advance(t, time.Second)
	}
}

// The poll-time refresh threshold on the database clock (ADR-0017 Section 3.2, implementation
// notes 8 and 9): a poll runs a refresh only when the pass heartbeat is older than 2 x interval.
// The database clock moves between statements, so the exact 2 x instant is not tested here (the
// in-memory variant tests it); 1.5 x must not refresh and 2.5 x must. A refresh shows as a new
// heartbeat.
func TestPoll_RefreshThreshold_TwiceInterval(t *testing.T) {
	f := newPGRRFixture(t)
	svc := f.service(t)
	pgRRHeartbeat(t, svc, "w_1")
	ctx := context.Background()
	if _, err := f.repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: 5 * time.Second, WorkerTimeout: pgRRTimeout, BatchLimit: 50, MaxLapses: 3}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	heartbeatAge := func() time.Duration {
		t.Helper()
		var age time.Duration
		if err := f.pool.QueryRow(ctx, `SELECT (EXTRACT(EPOCH FROM clock_timestamp() - last_completed_at) * 1000000)::bigint FROM assignment_pass_state WHERE singleton;`).Scan(&age); err != nil {
			t.Fatalf("read heartbeat age: %v", err)
		}
		return age * time.Microsecond
	}
	poll := func() {
		t.Helper()
		if _, err := svc.PollWork(ctx, "w_1"); err != nil && !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("PollWork: %v", err)
		}
	}

	f.advance(t, pgRRInterval*3/2)
	poll()
	if age := heartbeatAge(); age < pgRRInterval*3/2 {
		t.Fatalf("heartbeat 1.5 x interval old: the poll refreshed (heartbeat age now %v)", age)
	}
	f.advance(t, pgRRInterval)
	poll()
	if age := heartbeatAge(); age >= pgRRInterval {
		t.Fatalf("heartbeat 2.5 x interval old: the poll did not refresh (heartbeat age %v)", age)
	}
}
