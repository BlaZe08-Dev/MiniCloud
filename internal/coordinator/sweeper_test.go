package coordinator_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

const (
	sweepInterval = 5 * time.Second
	sweepWindow   = 30 * time.Second
)

// sweepBase is far from the process clock on purpose: every grace instant must come from the
// repository clock (owner decision Q-1), so a coordinator that used time.Now() is caught.
var sweepBase = time.Date(2031, 6, 1, 12, 0, 0, 0, time.UTC)

// sweepFakeRepo is a repository whose clock and sweep outcome the test controls. It records the
// SweepParams of every call, which is exactly what the sweeper owns and passes to the repository.
type sweepFakeRepo struct {
	storage.Repository

	mu       sync.Mutex
	now      time.Time
	nowErr   error
	sweepErr error
	result   domain.SweepResult
	params   []domain.SweepParams
}

func newSweepFakeRepo() *sweepFakeRepo {
	return &sweepFakeRepo{Repository: storage.NewInMemoryRepository(), now: sweepBase}
}

func (r *sweepFakeRepo) Now(ctx context.Context) (time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nowErr != nil {
		return time.Time{}, r.nowErr
	}
	return r.now, nil
}

func (r *sweepFakeRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.params = append(r.params, p)
	res := r.result
	if r.sweepErr != nil {
		return &res, r.sweepErr
	}
	return &res, nil
}

func (r *sweepFakeRepo) set(now time.Time, nowErr, sweepErr error, res domain.SweepResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now, r.nowErr, r.sweepErr, r.result = now, nowErr, sweepErr, res
}

func (r *sweepFakeRepo) lastGrace(t *testing.T) domain.SweepGraceState {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.params) == 0 {
		t.Fatal("SweepExpiredLeases was never called")
	}
	return r.params[len(r.params)-1].Grace
}

func (r *sweepFakeRepo) sweepCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.params)
}

var errDBDown = errors.New("dial tcp 127.0.0.1:5433: connect: connection refused")

// sweeperHarness drives ticks by hand at chosen repository times.
type sweeperHarness struct {
	t    *testing.T
	repo *sweepFakeRepo
	sw   *coordinator.LeaseSweeper
}

func newSweeperHarness(t *testing.T, startupGrace time.Duration) *sweeperHarness {
	t.Helper()
	repo := newSweepFakeRepo()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetGracePeriods(startupGrace, sweepWindow)
	return &sweeperHarness{t: t, repo: repo, sw: svc.NewLeaseSweeper(sweepInterval)}
}

// tickAt runs one sweeper tick with the repository clock at sweepBase+at and the given outcomes.
func (h *sweeperHarness) tickAt(at time.Duration, nowErr, sweepErr error, res domain.SweepResult) error {
	h.t.Helper()
	h.repo.set(sweepBase.Add(at), nowErr, sweepErr, res)
	return h.sw.Tick(context.Background())
}

func (h *sweeperHarness) okTickAt(at time.Duration) domain.SweepGraceState {
	h.t.Helper()
	if err := h.tickAt(at, nil, nil, domain.SweepResult{}); err != nil {
		h.t.Fatalf("tick at +%v failed: %v", at, err)
	}
	return h.repo.lastGrace(h.t)
}

func at(d time.Duration) time.Time { return sweepBase.Add(d) }

func requireWindow(t *testing.T, g domain.SweepGraceState, until time.Duration) {
	t.Helper()
	if !g.OutageWindowUntil.Equal(at(until)) {
		t.Fatalf("OutageWindowUntil = %v, want %v (+%v)", g.OutageWindowUntil, at(until), until)
	}
}

func requireNoWindowAt(t *testing.T, g domain.SweepGraceState, now time.Duration) {
	t.Helper()
	if at(now).Before(g.OutageWindowUntil) {
		t.Fatalf("an outage window is open at +%v (OutageWindowUntil %v), want none", now, g.OutageWindowUntil)
	}
}

// TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock pins Q-1: the startup
// grace deadline is the repository time of the first tick plus the grace period, never process
// time.
func TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock(t *testing.T) {
	h := newSweeperHarness(t, 30*time.Second)
	g := h.okTickAt(0)
	if !g.StartupGraceUntil.Equal(at(30 * time.Second)) {
		t.Fatalf("StartupGraceUntil = %v, want repository time + 30s = %v", g.StartupGraceUntil, at(30*time.Second))
	}
	// The anchor is the first tick: later ticks must not slide it.
	g = h.okTickAt(5 * time.Second)
	if !g.StartupGraceUntil.Equal(at(30 * time.Second)) {
		t.Fatalf("StartupGraceUntil moved to %v on a later tick", g.StartupGraceUntil)
	}
}

func TestCoordinator_LeaseSweeper_PassesBackoffWithInjectedRandomness(t *testing.T) {
	repo := newSweepFakeRepo()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetGracePeriods(0, sweepWindow)
	svc.SetSweepRandom(func() float64 { return 0 }) // jitter factor at the low end: 0.5
	sw := svc.NewLeaseSweeper(sweepInterval)
	if err := sw.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	p := repo.params[0]
	repo.mu.Unlock()
	if p.Backoff == nil {
		t.Fatal("SweepParams.Backoff is nil: the repository rejects that and no tick would ever reap")
	}
	if got := p.Backoff(1); got != 500*time.Millisecond {
		t.Fatalf("Backoff(1) = %v with rnd=0, want 500ms (base 1s x jitter 0.5 from the injected source)", got)
	}
	if p.Limit != 0 && p.Limit != domain.DefaultSweepBatchSize {
		t.Fatalf("Limit = %d, want the default batch size %d", p.Limit, domain.DefaultSweepBatchSize)
	}
}

func TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)
	// One millisecond over 3x the sweep interval of database time between consecutive successes.
	gap := 3*sweepInterval + time.Millisecond
	g := h.okTickAt(gap)
	requireWindow(t, g, gap+sweepWindow)
}

func TestCoordinator_LeaseSweeper_InterTickGapBelowThreshold_EntersNoGrace(t *testing.T) {
	for _, gap := range []time.Duration{sweepInterval, 2 * sweepInterval, 3 * sweepInterval} { // exactly 3x is not "over 3x"
		h := newSweeperHarness(t, 0)
		h.okTickAt(0)
		g := h.okTickAt(gap)
		requireNoWindowAt(t, g, gap)
	}
}

func TestCoordinator_LeaseSweeper_FailedTick_EntersGrace(t *testing.T) {
	cases := []struct {
		name             string
		nowErr, sweepErr error
	}{
		{"sweep returned an error", nil, errDBDown},
		{"database time could not be read", errDBDown, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSweeperHarness(t, 0)
			h.okTickAt(0)
			// A failed tick one interval later: no gap, only the failure.
			if err := h.tickAt(sweepInterval, tc.nowErr, tc.sweepErr, domain.SweepResult{}); err == nil {
				t.Fatal("tick succeeded, want the repository error returned")
			}
			g := h.okTickAt(2 * sweepInterval)
			requireWindow(t, g, 2*sweepInterval+sweepWindow)
		})
	}
}

// TestCoordinator_LeaseSweeper_PartialResultWithErrorIsFailedTick: the repository returns the
// swept-so-far result together with the error; only the error decides.
func TestCoordinator_LeaseSweeper_PartialResultWithErrorIsFailedTick(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)
	partial := domain.SweepResult{Candidates: 3, Swept: []domain.SweepOutcome{{JobID: "j", AttemptID: "a"}}}
	if err := h.tickAt(sweepInterval, nil, errDBDown, partial); err == nil {
		t.Fatal("want error")
	}
	requireWindow(t, h.okTickAt(2*sweepInterval), 2*sweepInterval+sweepWindow)
}

func TestCoordinator_LeaseSweeper_CandidateLockTimeout_EntersNoGrace(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)
	// Lock timeouts and SKIP LOCKED skips are reported in the result with a nil error.
	if err := h.tickAt(sweepInterval, nil, nil, domain.SweepResult{Candidates: 4, SkippedLocked: 4}); err != nil {
		t.Fatalf("a skipped candidate must not fail the tick: %v", err)
	}
	g := h.okTickAt(2 * sweepInterval)
	requireNoWindowAt(t, g, 2*sweepInterval)
	if err := h.tickAt(3*sweepInterval, nil, nil, domain.SweepResult{Candidates: 1, SkippedLocked: 1}); err != nil {
		t.Fatal(err)
	}
	requireNoWindowAt(t, h.repo.lastGrace(t), 3*sweepInterval)
}

// TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace pins D-R3-01: after a window
// ends a cooldown of the same length follows, during which a new failure opens no window and
// reaping proceeds; once the cooldown ends a new failure opens a new window.
func TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)

	// Failure, then recovery at +10s: window [10s, 40s).
	_ = h.tickAt(5*time.Second, nil, errDBDown, domain.SweepResult{})
	g := h.okTickAt(10 * time.Second)
	requireWindow(t, g, 10*time.Second+sweepWindow)
	if !g.CooldownUntil.Before(at(10 * time.Second)) {
		t.Fatalf("cooldown %v is active while the window is open: it would lift the suppression", g.CooldownUntil)
	}

	// Inside the window nothing extends it, however often the database blips.
	_ = h.tickAt(15*time.Second, nil, errDBDown, domain.SweepResult{})
	g = h.okTickAt(20 * time.Second)
	requireWindow(t, g, 10*time.Second+sweepWindow)

	// The window ends at +40s and the cooldown runs until +70s.
	g = h.okTickAt(45 * time.Second)
	if !g.CooldownUntil.Equal(at(70 * time.Second)) {
		t.Fatalf("CooldownUntil = %v, want window end + window length = %v", g.CooldownUntil, at(70*time.Second))
	}
	requireWindow(t, g, 10*time.Second+sweepWindow) // unchanged, already over

	// A database-time gap inside the cooldown opens no window.
	h.okTickAt(46 * time.Second)
	g = h.okTickAt(46*time.Second + 3*sweepInterval + time.Millisecond) // +61.001s, cooldown until +70s
	requireWindow(t, g, 10*time.Second+sweepWindow)
	if !at(61 * time.Second).Before(g.CooldownUntil) {
		t.Fatalf("cooldown not active at +61s: CooldownUntil %v", g.CooldownUntil)
	}

	// A failure inside the cooldown opens no window either (the trigger is consumed, not deferred).
	_ = h.tickAt(65*time.Second, nil, errDBDown, domain.SweepResult{})
	g = h.okTickAt(66 * time.Second)
	requireWindow(t, g, 10*time.Second+sweepWindow)

	// After the cooldown ended, the next failure opens a new window.
	_ = h.tickAt(75*time.Second, nil, errDBDown, domain.SweepResult{})
	g = h.okTickAt(80 * time.Second)
	requireWindow(t, g, 80*time.Second+sweepWindow)
}

// TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt: suppression never exceeds
// one window length.
func TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)
	_ = h.tickAt(5*time.Second, nil, errDBDown, domain.SweepResult{})
	h.okTickAt(10 * time.Second) // window until +40s
	for _, d := range []time.Duration{15, 20, 25, 30} {
		_ = h.tickAt(d*time.Second, nil, errDBDown, domain.SweepResult{})
		g := h.okTickAt(d*time.Second + time.Second)
		requireWindow(t, g, 10*time.Second+sweepWindow)
	}
}

// TestCoordinator_LeaseSweeper_HealthyTicksAfterRecoveryNeverReopenWindow: a failure opens exactly
// one window. Once it and the cooldown are over, healthy ticks must never open another; a trigger
// that is not consumed would suppress reaping forever.
func TestCoordinator_LeaseSweeper_HealthyTicksAfterRecoveryNeverReopenWindow(t *testing.T) {
	h := newSweeperHarness(t, 0)
	h.okTickAt(0)
	_ = h.tickAt(5*time.Second, nil, errDBDown, domain.SweepResult{})
	first := h.okTickAt(10 * time.Second).OutageWindowUntil // window until +40s, cooldown until +70s
	for d := 15 * time.Second; d <= 200*time.Second; d += sweepInterval {
		g := h.okTickAt(d)
		if !g.OutageWindowUntil.Equal(first) {
			t.Fatalf("a healthy tick at +%v changed OutageWindowUntil from %v to %v: the failure trigger was not consumed", d, first, g.OutageWindowUntil)
		}
	}
}

// TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery: a sweep that fails on the
// very first tick still leaves startup grace anchored at the first successful database read, and
// the recovery tick opens the outage window too.
func TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery(t *testing.T) {
	h := newSweeperHarness(t, 30*time.Second)
	_ = h.tickAt(0, nil, errDBDown, domain.SweepResult{})
	g := h.okTickAt(5 * time.Second)
	if !g.StartupGraceUntil.Equal(at(30 * time.Second)) {
		t.Fatalf("StartupGraceUntil = %v, want %v (anchored at the first successful database read)", g.StartupGraceUntil, at(30*time.Second))
	}
	requireWindow(t, g, 5*time.Second+sweepWindow)
}

func TestCoordinator_LeaseSweeper_SkippedChangedLoggedAtWarn(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	h := newSweeperHarness(t, 0)
	if err := h.tickAt(0, nil, nil, domain.SweepResult{Candidates: 2, SkippedChanged: 2}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "skipped_changed=2") {
		t.Fatalf("SkippedChanged anomaly not logged at warn level with its count; log:\n%s", out)
	}
}

func TestCoordinator_LeaseSweeper_RunSurvivesFailedTicksAndStopsOnCancel(t *testing.T) {
	repo := newSweepFakeRepo()
	repo.set(sweepBase, nil, errDBDown, domain.SweepResult{})
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetGracePeriods(0, sweepWindow)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.StartLeaseSweeper(ctx, 2*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for repo.sweepCalls() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := repo.sweepCalls(); n < 3 {
		t.Fatalf("only %d sweep calls: a failed tick must not stop the loop", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartLeaseSweeper did not return after the context was cancelled")
	}
}

// --- End-to-end through the in-memory repository --------------------------------------------

type e2eHarness struct {
	clock *mockClock
	repo  *storage.InMemoryRepository
	svc   *coordinator.Service
}

func newE2EHarness(t *testing.T, lease, startupGrace time.Duration) *e2eHarness {
	t.Helper()
	clk := newMockClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk), storage.WithLeaseDuration(lease))
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
	svc.SetClock(clk)
	svc.SetLeaseDuration(lease)
	svc.SetGracePeriods(startupGrace, sweepWindow)
	svc.SetSweepRandom(func() float64 { return 0.5 }) // jitter factor 1.0
	ctx := context.Background()
	for _, id := range []string{"w1", "w2"} {
		if err := svc.RegisterWorker(ctx, &domain.Worker{ID: id, TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
			t.Fatal(err)
		}
	}
	return &e2eHarness{clock: clk, repo: repo, svc: svc}
}

// runFor advances the repository clock by total in sweep-interval steps, ticking after each, as a
// healthy coordinator would. A single large jump is a database-time gap and opens an outage window
// (that path has its own tests); these tests are about startup grace and reaping.
func (h *e2eHarness) runFor(t *testing.T, sw *coordinator.LeaseSweeper, total time.Duration) {
	t.Helper()
	for elapsed := time.Duration(0); elapsed < total; elapsed += sweepInterval {
		step := min(sweepInterval, total-elapsed)
		h.clock.Advance(step)
		if err := sw.Tick(context.Background()); err != nil {
			t.Fatalf("tick failed: %v", err)
		}
	}
}

func (h *e2eHarness) submitAndClaim(t *testing.T, worker string) *coordinator.WorkAssignment {
	t.Helper()
	ctx := context.Background()
	if _, err := h.svc.SubmitJob(ctx, "echo", []byte(`{"message":"x"}`), 30, 3); err != nil {
		t.Fatal(err)
	}
	a, err := h.svc.PollWork(ctx, worker)
	if err != nil {
		t.Fatalf("PollWork(%s): %v", worker, err)
	}
	return a
}

func TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt(t *testing.T) {
	ctx := context.Background()
	h := newE2EHarness(t, 30*time.Second, 0)
	a := h.submitAndClaim(t, "w1")
	sw := h.svc.NewLeaseSweeper(sweepInterval)

	// w1 never renews. Before the lease lapses nothing happens.
	h.runFor(t, sw, 29*time.Second)
	if att, _ := h.repo.GetAttempt(ctx, a.AttemptID); att.State != domain.AttemptRunning {
		t.Fatalf("attempt %s before expiry, want RUNNING", att.State)
	}

	// Lease lapses: the next tick reaps the attempt and schedules the retry with the injected backoff.
	h.runFor(t, sw, 2*time.Second)
	att, _ := h.repo.GetAttempt(ctx, a.AttemptID)
	job, _ := h.repo.GetJob(ctx, a.JobID)
	if att.State != domain.AttemptLost || job.State != domain.StateRetrying {
		t.Fatalf("after expiry: attempt %s, job %s, want LOST / RETRYING", att.State, job.State)
	}
	if want := h.clock.Now().Add(time.Second); job.NotBefore == nil || !job.NotBefore.Equal(want) {
		t.Fatalf("not_before = %v, want now + 1s backoff (injected jitter 1.0) = %v", job.NotBefore, want)
	}

	// After the backoff another worker claims attempt 2, and w1's late commit is rejected.
	h.clock.Advance(2 * time.Second)
	a2, err := h.svc.PollWork(ctx, "w2")
	if err != nil || a2.JobID != a.JobID || a2.AttemptNo != 2 {
		t.Fatalf("retry dispatch = %+v, %v, want attempt 2 of the same job", a2, err)
	}
	if _, err := h.svc.CommitResult(ctx, a.JobID, a.AttemptID, a.LeaseToken, "w1", 0, "late", "", ""); !errors.Is(err, domain.ErrStaleAttempt) && !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("late commit from the abandoned attempt = %v, want rejection", err)
	}
}

func TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping(t *testing.T) {
	ctx := context.Background()
	h := newE2EHarness(t, 10*time.Second, 30*time.Second)
	a := h.submitAndClaim(t, "w1")
	sw := h.svc.NewLeaseSweeper(sweepInterval)

	// First tick anchors the grace; the lease lapses at +10s but grace runs until +30s.
	if err := sw.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h.runFor(t, sw, 20*time.Second)
	if att, _ := h.repo.GetAttempt(ctx, a.AttemptID); att.State != domain.AttemptRunning {
		t.Fatalf("attempt %s inside startup grace, want RUNNING (a surviving worker must still be able to finish)", att.State)
	}
	// The surviving worker's commit is accepted (accept-until-swept).
	if _, err := h.svc.CommitResult(ctx, a.JobID, a.AttemptID, a.LeaseToken, "w1", 0, "ok", "", ""); err != nil {
		t.Fatalf("commit inside grace: %v", err)
	}

	// A second abandoned attempt is reaped once the grace is over.
	h2 := newE2EHarness(t, 10*time.Second, 30*time.Second)
	b := h2.submitAndClaim(t, "w1")
	sw2 := h2.svc.NewLeaseSweeper(sweepInterval)
	_ = sw2.Tick(ctx)
	h2.runFor(t, sw2, 31*time.Second)
	if att, _ := h2.repo.GetAttempt(ctx, b.AttemptID); att.State != domain.AttemptLost {
		t.Fatalf("attempt %s after the grace, want LOST", att.State)
	}
}

// TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery drives the whole chain
// through the real in-memory sweep: a failed tick, recovery, an expired attempt that is held back
// for exactly one window, and then reaped, even though a fresh failure arrives during cooldown.
func TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery(t *testing.T) {
	ctx := context.Background()
	h := newE2EHarness(t, 10*time.Second, 0)
	flaky := &flakyRepo{InMemoryRepository: h.repo}
	svc := coordinator.NewService(flaky, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), time.Hour)
	svc.SetClock(h.clock)
	svc.SetGracePeriods(0, sweepWindow)
	svc.SetSweepRandom(func() float64 { return 0.5 })
	sw := svc.NewLeaseSweeper(sweepInterval)

	a := h.submitAndClaim(t, "w1")
	if err := sw.Tick(ctx); err != nil { // anchors; nothing expired
		t.Fatal(err)
	}
	flaky.fail = true
	h.clock.Advance(sweepInterval)
	if err := sw.Tick(ctx); err == nil {
		t.Fatal("want failed tick")
	}
	flaky.fail = false
	h.clock.Advance(15 * time.Second) // lease (10s) has lapsed during the outage
	if err := sw.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if att, _ := h.repo.GetAttempt(ctx, a.AttemptID); att.State != domain.AttemptRunning {
		t.Fatalf("attempt %s on the recovery tick, want RUNNING (outage window)", att.State)
	}
	h.clock.Advance(sweepWindow + time.Second) // window over, cooldown running
	if err := sw.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if att, _ := h.repo.GetAttempt(ctx, a.AttemptID); att.State != domain.AttemptLost {
		t.Fatalf("attempt %s after the window, want LOST (suppression ends after one window)", att.State)
	}
}

type flakyRepo struct {
	*storage.InMemoryRepository
	fail bool
}

func (r *flakyRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	if r.fail {
		return nil, errDBDown
	}
	return r.InMemoryRepository.SweepExpiredLeases(ctx, p)
}
