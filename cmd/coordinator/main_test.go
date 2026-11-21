package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
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

		// The ADR-0017 Section 5.5 defaults, as LoadCoordinatorConfig fills them.
		DispatchPolicy:         domain.DispatchPolicyFifoPull,
		AssignmentAcceptWindow: 5 * time.Second,
		AssignmentInterval:     time.Second,
		AssignmentMaxLapses:    3,
		AssignmentFallbackAge:  30 * time.Second,
		AssignmentBatchLimit:   50,
		AssignmentMinYield:     10 * time.Millisecond,

		// V2-1c node settings, as LoadCoordinatorConfig fills them.
		NodeHardwareMinInterval: config.DefaultNodeHardwareMinInterval,
		NodeMaxCount:            config.DefaultNodeMaxCount,
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

// logCapture records slog records for the duration of a test.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// roundRobinConfig is testConfig under round_robin with settings that differ from every default.
func roundRobinConfig() *config.CoordinatorConfig {
	cfg := testConfig()
	cfg.DispatchPolicy = domain.DispatchPolicyRoundRobin
	cfg.AssignmentAcceptWindow = 6 * time.Second
	cfg.AssignmentInterval = 100 * time.Millisecond
	cfg.AssignmentMaxLapses = 2
	cfg.AssignmentFallbackAge = 40 * time.Second
	cfg.AssignmentBatchLimit = 7
	cfg.AssignmentMinYield = 5 * time.Millisecond
	return cfg
}

func newTestService(cfg *config.CoordinatorConfig, repo storage.Repository) *coordinator.Service {
	return coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), cfg.WorkerTimeout)
}

// F-R3: configureService applies the assignment settings and the dispatch policy from the
// configuration, and logs one line with the effective policy and every setting.
func TestMain_ConfigureService_AppliesDispatchPolicyAndSettings(t *testing.T) {
	logs := captureLogs(t)
	cfg := roundRobinConfig()
	svc := newTestService(cfg, newMemoryRepository(cfg))
	if err := configureService(svc, cfg); err != nil {
		t.Fatalf("configureService: %v", err)
	}
	if got := svc.DispatchPolicy(); got != domain.DispatchPolicyRoundRobin {
		t.Fatalf("effective policy = %q, want round_robin", got)
	}
	if got, want := svc.AssignmentSettings(), assignmentSettings(cfg); got != want {
		t.Fatalf("service settings = %+v, want %+v", got, want)
	}

	logs.mu.Lock()
	defer logs.mu.Unlock()
	var lines []slog.Record
	for _, r := range logs.records {
		if r.Message == "dispatch policy configured" {
			lines = append(lines, r)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("policy log lines = %d, want 1", len(lines))
	}
	attrs := map[string]string{}
	lines[0].Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	want := map[string]string{
		"policy":                   "round_robin",
		"assignment_accept_window": "6s",
		"assignment_interval":      "100ms",
		"assignment_max_lapses":    "2",
		"assignment_fallback_age":  "40s",
		"assignment_batch_limit":   "7",
		"assignment_min_yield":     "5ms",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Fatalf("log attribute %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
	if len(attrs) != len(want) {
		t.Fatalf("log attributes = %v, want exactly %v", attrs, want)
	}
}

// F-R3, startup failure: settings that SetAssignmentSettings rejects make configureService fail,
// so main exits; the policy stays fifo_pull.
func TestMain_ConfigureService_FailsOnInvalidAssignmentSettings(t *testing.T) {
	cfg := roundRobinConfig()
	cfg.AssignmentInterval = 50 * time.Millisecond // below the 100 ms floor
	svc := newTestService(cfg, newMemoryRepository(cfg))
	err := configureService(svc, cfg)
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("configureService with an interval of 50ms: err=%v, want ErrInvalidRequest", err)
	}
	if got := svc.DispatchPolicy(); got != domain.DispatchPolicyFifoPull {
		t.Fatalf("policy after the failure = %q, want fifo_pull", got)
	}
}

// fifoOnlyRepo exposes only storage.Repository, so it implements no assignment interface.
type fifoOnlyRepo struct{ storage.Repository }

// F-R3, startup failure: a policy that SetDispatchPolicy rejects makes configureService fail: an
// unknown name, and round_robin over a repository without the assignment interfaces.
func TestMain_ConfigureService_FailsOnRejectedPolicy(t *testing.T) {
	cfg := testConfig()
	cfg.DispatchPolicy = "lottery"
	if err := configureService(newTestService(cfg, newMemoryRepository(cfg)), cfg); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("unknown policy: err=%v, want ErrInvalidRequest", err)
	}
	cfg = roundRobinConfig()
	svc := newTestService(cfg, fifoOnlyRepo{newMemoryRepository(cfg)})
	if err := configureService(svc, cfg); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("round_robin without an assignment repository: err=%v, want ErrInvalidRequest", err)
	}
	if got := svc.DispatchPolicy(); got != domain.DispatchPolicyFifoPull {
		t.Fatalf("policy after the failure = %q, want fifo_pull", got)
	}
}

// F-R3: startBackground runs the assignment loop under round_robin and not under fifo_pull. Real
// time: the loop interval is 100 ms. Under round_robin a submitted job is reserved without any
// poll; under fifo_pull no JOB_ASSIGNED event appears within ten intervals.
func TestMain_BackgroundLoops_AssignmentLoopOnlyForRoundRobin(t *testing.T) {
	for _, policy := range []string{domain.DispatchPolicyRoundRobin, domain.DispatchPolicyFifoPull} {
		t.Run(policy, func(t *testing.T) {
			cfg := roundRobinConfig()
			cfg.DispatchPolicy = policy
			repo := newMemoryRepository(cfg)
			svc := newTestService(cfg, repo)
			if err := configureService(svc, cfg); err != nil {
				t.Fatalf("configureService: %v", err)
			}
			ctx := context.Background()
			if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "w1", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
				t.Fatal(err)
			}
			if err := repo.RecordPoll(ctx, "w1"); err != nil {
				t.Fatal(err)
			}
			job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"x"}`), 30, 3)
			if err != nil {
				t.Fatal(err)
			}

			stop := startBackground(ctx, svc, cfg)
			defer stop()

			assigned := func() bool {
				events, err := repo.ListEventsByJob(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if e.EventType == domain.JobEventAssigned {
						return true
					}
				}
				return false
			}
			if policy == domain.DispatchPolicyRoundRobin {
				deadline := time.Now().Add(5 * time.Second)
				for !assigned() {
					if time.Now().After(deadline) {
						t.Fatal("round_robin: the assignment loop never reserved the job")
					}
					time.Sleep(5 * time.Millisecond)
				}
				return
			}
			time.Sleep(10 * cfg.AssignmentInterval)
			if assigned() {
				t.Fatal("fifo_pull: an assignment pass ran (JOB_ASSIGNED written)")
			}
		})
	}
}

// One validator (P4-08d item 5): config.LoadCoordinatorConfig must accept exactly the assignment
// settings and policies that the service accepts (coordinator.AssignmentSettings.Validate through
// SetAssignmentSettings, and SetDispatchPolicy, as configureService chains them). internal/config
// keeps its own copy of the Section 5.5 and A1 rules (validateAssignment); this cross-test pins the
// two copies together at the edge values of every rule. When config accepts, the binary's mapping
// (assignmentSettings) yields exactly the configured values.
func TestMain_AssignmentSettings_ConfigAndServiceAgree(t *testing.T) {
	d := coordinator.DefaultAssignmentSettings()
	with := func(f func(*coordinator.AssignmentSettings)) coordinator.AssignmentSettings {
		s := d
		f(&s)
		return s
	}
	const ns = time.Nanosecond
	type tc struct {
		name   string
		policy string
		s      coordinator.AssignmentSettings
	}
	cases := []tc{{"defaults", "round_robin", d}}
	add := func(name string, f func(*coordinator.AssignmentSettings)) {
		cases = append(cases, tc{name, "round_robin", with(f)})
	}
	// Accept window: 5 s to 30 s (fallback age raised so only this rule is at its edge).
	for _, aw := range []time.Duration{5*time.Second - ns, 5 * time.Second, 30 * time.Second, 30*time.Second + ns} {
		add("accept window "+aw.String(), func(s *coordinator.AssignmentSettings) { s.AcceptWindow = aw; s.FallbackAge = 10 * time.Minute })
	}
	// Interval: 100 ms to 10 s.
	for _, iv := range []time.Duration{100*time.Millisecond - ns, 100 * time.Millisecond, 10 * time.Second, 10*time.Second + ns} {
		add("interval "+iv.String(), func(s *coordinator.AssignmentSettings) {
			s.Interval = iv
			s.AcceptWindow = 30 * time.Second
			s.FallbackAge = 10 * time.Minute
		})
	}
	// Max lapses: 1 to 10.
	for _, l := range []int{0, 1, 10, 11} {
		add(fmt.Sprintf("max lapses %d", l), func(s *coordinator.AssignmentSettings) { s.MaxLapses = l; s.FallbackAge = 10 * time.Minute })
	}
	// Fallback age: at least max lapses x (accept window + interval) = 18 s, at most 10 min.
	for _, fa := range []time.Duration{18*time.Second - ns, 18 * time.Second, 10 * time.Minute, 10*time.Minute + ns} {
		add("fallback age "+fa.String(), func(s *coordinator.AssignmentSettings) { s.FallbackAge = fa })
	}
	// Batch limit: 1 to 500.
	for _, b := range []int{0, 1, 500, 501} {
		add(fmt.Sprintf("batch limit %d", b), func(s *coordinator.AssignmentSettings) { s.BatchLimit = b })
	}
	// Min yield: 1 ms to 1 s.
	for _, y := range []time.Duration{time.Millisecond - ns, time.Millisecond, time.Second, time.Second + ns} {
		add("min yield "+y.String(), func(s *coordinator.AssignmentSettings) { s.MinYield = y })
	}
	// Min yield not above the interval.
	for _, y := range []time.Duration{100 * time.Millisecond, 100*time.Millisecond + ns} {
		add("min yield vs interval "+y.String(), func(s *coordinator.AssignmentSettings) { s.Interval = 100 * time.Millisecond; s.MinYield = y })
	}
	// A1: the interval does not exceed the accept window.
	for _, iv := range []time.Duration{5 * time.Second, 5*time.Second + ns} {
		add("A1 interval vs accept window "+iv.String(), func(s *coordinator.AssignmentSettings) { s.Interval = iv; s.FallbackAge = 10 * time.Minute })
	}
	// A1: the fallback age is greater than 2 x interval + accept window (7 s here; the lapse-path
	// minimum is 6 s with one lapse, so only A1 is at its edge).
	for _, fa := range []time.Duration{7 * time.Second, 7*time.Second + ns} {
		add("A1 fallback age "+fa.String(), func(s *coordinator.AssignmentSettings) { s.MaxLapses = 1; s.FallbackAge = fa })
	}
	// Policy names (config lowercases the value).
	for _, p := range []string{"fifo_pull", "ROUND_ROBIN", "lottery"} {
		cases = append(cases, tc{"policy " + p, p, d})
	}

	accepted, rejected := 0, 0
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("MINICLOUD_STORAGE_DRIVER", "memory")
			t.Setenv("MINICLOUD_DISPATCH_POLICY", c.policy)
			t.Setenv("MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", c.s.AcceptWindow.String())
			t.Setenv("MINICLOUD_ASSIGNMENT_INTERVAL", c.s.Interval.String())
			t.Setenv("MINICLOUD_ASSIGNMENT_MAX_LAPSES", strconv.Itoa(c.s.MaxLapses))
			t.Setenv("MINICLOUD_ASSIGNMENT_FALLBACK_AGE", c.s.FallbackAge.String())
			t.Setenv("MINICLOUD_ASSIGNMENT_BATCH_LIMIT", strconv.Itoa(c.s.BatchLimit))
			t.Setenv("MINICLOUD_ASSIGNMENT_MIN_YIELD", c.s.MinYield.String())
			loaded, cfgErr := config.LoadCoordinatorConfig()

			manual := testConfig()
			manual.DispatchPolicy = strings.ToLower(c.policy)
			manual.AssignmentAcceptWindow = c.s.AcceptWindow
			manual.AssignmentInterval = c.s.Interval
			manual.AssignmentMaxLapses = c.s.MaxLapses
			manual.AssignmentFallbackAge = c.s.FallbackAge
			manual.AssignmentBatchLimit = c.s.BatchLimit
			manual.AssignmentMinYield = c.s.MinYield
			svcErr := configureService(newTestService(manual, newMemoryRepository(manual)), manual)

			if (cfgErr == nil) != (svcErr == nil) {
				t.Fatalf("config and service disagree: config err=%v, service err=%v", cfgErr, svcErr)
			}
			if cfgErr == nil {
				accepted++
				if got := assignmentSettings(loaded); got != c.s {
					t.Fatalf("assignmentSettings(loaded config) = %+v, want %+v", got, c.s)
				}
			} else {
				rejected++
			}
		})
	}
	// Each rule contributes at least one accepted and one rejected value.
	if accepted < 15 || rejected < 15 {
		t.Fatalf("accepted %d and rejected %d cases; the edge table lost its edges", accepted, rejected)
	}
}

// P4-17 (ADR-0017 Q1), end to end from the environment: the policy the binary runs is the one
// config.LoadCoordinatorConfig derives from MINICLOUD_DISPATCH_POLICY, through configureService and
// startBackground. Unset or empty runs round_robin (the loop reserves a job with no poll); an
// explicit fifo_pull runs no pass and a poll claims the job as fifo_pull; an invalid value fails
// startup in the loader.
func TestMain_DispatchPolicyFromEnv_EndToEnd(t *testing.T) {
	load := func(t *testing.T, set bool, val string) (*config.CoordinatorConfig, error) {
		t.Helper()
		t.Setenv("MINICLOUD_STORAGE_DRIVER", "memory")
		t.Setenv("MINICLOUD_ASSIGNMENT_INTERVAL", "100ms")
		t.Setenv("MINICLOUD_DISPATCH_POLICY", "")
		if !set {
			os.Unsetenv("MINICLOUD_DISPATCH_POLICY")
		} else {
			t.Setenv("MINICLOUD_DISPATCH_POLICY", val)
		}
		return config.LoadCoordinatorConfig()
	}
	cases := []struct {
		name     string
		set      bool
		val      string
		want     string
		wantFail bool
	}{
		{"unset", false, "", domain.DispatchPolicyRoundRobin, false},
		{"empty", true, "", domain.DispatchPolicyRoundRobin, false},
		{"explicit round_robin", true, "round_robin", domain.DispatchPolicyRoundRobin, false},
		{"explicit fifo_pull", true, "fifo_pull", domain.DispatchPolicyFifoPull, false},
		{"invalid", true, "lottery", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := load(t, c.set, c.val)
			if c.wantFail {
				if err == nil {
					t.Fatal("invalid policy accepted at startup")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadCoordinatorConfig: %v", err)
			}
			repo := newMemoryRepository(cfg)
			svc := newTestService(cfg, repo)
			if err := configureService(svc, cfg); err != nil {
				t.Fatalf("configureService: %v", err)
			}
			if got := svc.DispatchPolicy(); got != c.want {
				t.Fatalf("effective policy = %q, want %q", got, c.want)
			}
			ctx := context.Background()
			if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "w1", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
				t.Fatal(err)
			}
			if err := repo.RecordPoll(ctx, "w1"); err != nil {
				t.Fatal(err)
			}
			job, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"x"}`), 30, 3)
			if err != nil {
				t.Fatal(err)
			}
			stop := startBackground(ctx, svc, cfg)
			defer stop()
			assigned := func() bool {
				events, err := repo.ListEventsByJob(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if e.EventType == domain.JobEventAssigned {
						return true
					}
				}
				return false
			}
			if c.want == domain.DispatchPolicyRoundRobin {
				deadline := time.Now().Add(5 * time.Second)
				for !assigned() {
					if time.Now().After(deadline) {
						t.Fatal("round_robin: the assignment loop never reserved the job")
					}
					time.Sleep(5 * time.Millisecond)
				}
				return
			}
			time.Sleep(5 * cfg.AssignmentInterval)
			if assigned() {
				t.Fatal("fifo_pull: an assignment pass ran")
			}
			got, err := svc.PollWork(ctx, "w1")
			if err != nil || got == nil || got.JobID != job.ID || got.Policy != domain.DispatchPolicyFifoPull {
				t.Fatalf("fifo_pull poll = %+v err %v, want the job claimed as fifo_pull", got, err)
			}
		})
	}
}
