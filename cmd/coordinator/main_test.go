package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// testConfig is a micro-lease configuration: lease and both grace settings small (grace must be
// at least the lease), a fast sweeper, and no outage window so a scheduling hiccup between two
// 5 ms ticks cannot suppress reaping.
func testConfig() *config.CoordinatorConfig {
	return &config.CoordinatorConfig{
		DefaultMaxAttempts: 3,
		LeaseDuration:      40 * time.Millisecond,
		SweeperInterval:    5 * time.Millisecond,
		StartupGracePeriod: 40 * time.Millisecond,
		OutageGraceWindow:  0,
		WorkerTimeout:      time.Hour,
		ReaperInterval:     time.Hour,
	}
}

// TestMain_BackgroundLoops_ReapAbandonedAttempt proves the wiring end to end on the in-memory
// driver: a worker claims a job and never renews; the sweeper started by startBackground, with the
// configured lease and grace, reaps the attempt and schedules the retry. Time is the real clock at
// millisecond scale; nothing starts a server or touches a container.
func TestMain_BackgroundLoops_ReapAbandonedAttempt(t *testing.T) {
	cfg := testConfig()
	repo := newMemoryRepository(cfg)
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), cfg.WorkerTimeout)
	configureService(svc, cfg)

	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "w1", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"x"}`), 30, 3); err != nil {
		t.Fatal(err)
	}
	a, err := svc.PollWork(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if a.LeaseDurationSeconds != 0 { // 40 ms advertises as 0 whole seconds (omitted)
		t.Logf("lease_duration_seconds = %d", a.LeaseDurationSeconds)
	}

	stop := startBackground(ctx, svc, cfg)
	defer stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		att, _ := repo.GetAttempt(ctx, a.AttemptID)
		if att != nil && att.State == domain.AttemptLost {
			job, _ := repo.GetJob(ctx, a.JobID)
			if job.State != domain.StateRetrying {
				t.Fatalf("job %s after reaping, want RETRYING", job.State)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the running sweeper never reaped an attempt whose worker stopped renewing")
}

// TestMain_BackgroundLoops_StartupGraceFromConfig shows the configured startup grace reaches the
// sweeper: with a long grace the same abandoned attempt is not reaped.
func TestMain_BackgroundLoops_StartupGraceFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.StartupGracePeriod = time.Hour
	repo := newMemoryRepository(cfg)
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), cfg.WorkerTimeout)
	configureService(svc, cfg)
	ctx := context.Background()
	_ = svc.RegisterWorker(ctx, &domain.Worker{ID: "w1", TotalSlots: 1, SupportedTasks: []string{"echo"}})
	_, _ = svc.SubmitJob(ctx, "echo", []byte(`{"message":"x"}`), 30, 3)
	a, err := svc.PollWork(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	stop := startBackground(ctx, svc, cfg)
	defer stop()
	time.Sleep(300 * time.Millisecond) // many 5 ms ticks, lease long expired
	if att, _ := repo.GetAttempt(ctx, a.AttemptID); att.State != domain.AttemptRunning {
		t.Fatalf("attempt %s inside the configured startup grace, want RUNNING", att.State)
	}
}

// TestMain_BackgroundLoops_StopWaitsForLoops: stop cancels the loops and returns only after an
// in-flight sweep has finished (the repository call ignores cancellation, like a slow query), and
// is safe to call twice.
func TestMain_BackgroundLoops_StopWaitsForLoops(t *testing.T) {
	cfg := testConfig()
	repo := &slowSweepRepo{Repository: newMemoryRepository(cfg)}
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), cfg.WorkerTimeout)
	configureService(svc, cfg)
	stop := startBackground(context.Background(), svc, cfg)
	deadline := time.Now().Add(5 * time.Second)
	for repo.inflight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if repo.inflight.Load() == 0 {
		t.Fatal("the sweeper never started a sweep")
	}
	stop()
	if n := repo.inflight.Load(); n != 0 {
		t.Fatalf("stop returned with %d sweep(s) still in flight: the loop exit was not awaited", n)
	}
	stop()
}

type slowSweepRepo struct {
	storage.Repository
	inflight atomic.Int32
}

func (r *slowSweepRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	r.inflight.Add(1)
	defer r.inflight.Add(-1)
	time.Sleep(80 * time.Millisecond) // ignores ctx on purpose
	return r.Repository.SweepExpiredLeases(ctx, p)
}

// TestMain_BackgroundLoops_ReaperRuns: the worker reaper is started by the same call.
func TestMain_BackgroundLoops_ReaperRuns(t *testing.T) {
	cfg := testConfig()
	cfg.WorkerTimeout = 30 * time.Millisecond
	cfg.ReaperInterval = 5 * time.Millisecond
	repo := newMemoryRepository(cfg)
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), cfg.WorkerTimeout)
	configureService(svc, cfg)
	ctx := context.Background()
	if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "quiet", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	stop := startBackground(ctx, svc, cfg)
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if w, _ := repo.GetWorker(ctx, "quiet"); w != nil && w.Status == domain.WorkerStatusUnhealthy {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the worker reaper never marked a silent worker UNHEALTHY")
}

func TestMain_NewMemoryRepository_UsesConfiguredLease(t *testing.T) {
	cfg := testConfig()
	cfg.LeaseDuration = 7 * time.Second
	repo := newMemoryRepository(cfg)
	ctx := context.Background()
	_ = repo.UpsertWorker(ctx, &domain.Worker{ID: "w", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}})
	_ = repo.SubmitJob(ctx, &domain.Job{ID: "j", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 1, TimeoutSeconds: 5})
	now, _ := repo.Now(ctx)
	_, att, err := repo.DispatchNextJob(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if d := att.LeaseExpiresAt.Sub(now); d < 6*time.Second || d > 8*time.Second {
		t.Fatalf("lease = %v, want the configured 7s", d)
	}
}

// TestMain_ConfigureService_AppliesDefaultMaxAttempts proves MINICLOUD_DEFAULT_MAX_ATTEMPTS reaches
// the service that stamps max_attempts on a job submitted without one, and that a value the job
// range cannot hold is an error instead of being silently ignored.
func TestMain_ConfigureService_AppliesDefaultMaxAttempts(t *testing.T) {
	cfg := testConfig()
	cfg.DefaultMaxAttempts = 6
	svc := coordinator.NewService(newMemoryRepository(cfg), scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
	if err := configureService(svc, cfg); err != nil {
		t.Fatalf("configureService failed: %v", err)
	}
	if got := svc.DefaultMaxAttempts(); got != 6 {
		t.Fatalf("service default max_attempts = %d, want the configured 6", got)
	}

	cfg.DefaultMaxAttempts = 21
	if err := configureService(svc, cfg); err == nil {
		t.Fatal("configureService accepted a default max_attempts of 21")
	}
}
