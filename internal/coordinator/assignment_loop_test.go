package coordinator_test

import (
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

// Slice 5c, part 1 (P4-08d): the assignment loop of ADR-0017 Section 2.1, its grace input
// (Section 4.4), the stale heartbeat log (Section 4.5) and the poll-time refresh threshold
// (Section 3.2). Time is a mockClock shared by the service and the in-memory repository, except
// where a test says it uses the real ticker of Run.

// loopRepo wraps the in-memory repository and records every assignment pass: its parameters,
// result, and the injected-clock time at its start and end. onStart, when set, runs at the start of
// pass n (1-based), while the caller holds s.mu.
type loopRepo struct {
	*storage.InMemoryRepository
	clk *mockClock

	mu      sync.Mutex
	failing bool
	params  []storage.AssignParams
	results []storage.AssignResult
	starts  []time.Time
	ends    []time.Time
	onStart func(n int)
}

func newLoopRepo(clk *mockClock) *loopRepo {
	return &loopRepo{InMemoryRepository: storage.NewInMemoryRepository(storage.WithClock(clk)), clk: clk}
}

func (r *loopRepo) AssignQueuedJobs(ctx context.Context, p storage.AssignParams) (storage.AssignResult, error) {
	r.mu.Lock()
	r.params = append(r.params, p)
	r.starts = append(r.starts, r.clk.Now())
	n := len(r.starts)
	hook := r.onStart
	fail := r.failing
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	var res storage.AssignResult
	var err error
	if fail {
		err = errPassFault
	} else {
		res, err = r.InMemoryRepository.AssignQueuedJobs(ctx, p)
	}
	r.mu.Lock()
	r.results = append(r.results, res)
	r.ends = append(r.ends, r.clk.Now())
	r.mu.Unlock()
	return res, err
}

func (r *loopRepo) passes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *loopRepo) result(n int) storage.AssignResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results[n-1]
}

// loopService builds a round_robin service over repo with settings.
func loopService(t *testing.T, repo storage.Repository, clk *mockClock, settings coordinator.AssignmentSettings) *coordinator.Service {
	t.Helper()
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), rrTimeout)
	svc.SetClock(clk)
	if err := svc.SetAssignmentSettings(settings); err != nil {
		t.Fatalf("SetAssignmentSettings: %v", err)
	}
	if err := svc.SetDispatchPolicy(domain.DispatchPolicyRoundRobin); err != nil {
		t.Fatalf("SetDispatchPolicy: %v", err)
	}
	return svc
}

// loopWorker registers a HEALTHY worker with a fresh heartbeat and a fresh poll, so the pass may
// reserve to it.
func loopWorker(t *testing.T, svc *coordinator.Service, repo storage.AssignmentRepository, id string, tasks ...string) {
	t.Helper()
	rrHeartbeat(t, svc, id, tasks...)
	if err := repo.RecordPoll(context.Background(), id); err != nil {
		t.Fatalf("RecordPoll(%s): %v", id, err)
	}
}

func lapseEvents(t *testing.T, repo storage.Repository, jobID string) []*domain.JobEvent {
	t.Helper()
	events, err := repo.ListEventsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListEventsByJob: %v", err)
	}
	var out []*domain.JobEvent
	for _, e := range events {
		if e.EventType == domain.JobEventAssignmentLapsed {
			out = append(out, e)
		}
	}
	return out
}

func getJob(t *testing.T, repo storage.Repository, id string) *domain.Job {
	t.Helper()
	j, err := repo.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s): %v", id, err)
	}
	return j
}

var loopT0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// INV-6 end to end: with the loop running (Run, real ticker), a reservation that expires is revoked
// by the first pass that starts after its expiry, and the job is reserved again and claimed by the
// next worker in the rotation. The submit itself triggers the first reservation.
func TestCoordinator_AssignmentLoop_RevokesWithinOneInterval_EndToEnd(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	settings := coordinator.DefaultAssignmentSettings()
	settings.Interval = 100 * time.Millisecond
	svc := loopService(t, repo, clk, settings)
	loopWorker(t, svc, repo, "w_1", "echo")
	loopWorker(t, svc, repo, "w_2", "echo")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.StartAssignmentLoop(ctx) }()
	defer func() { cancel(); <-done }()

	job := rrSubmit(t, svc, "echo")
	waitFor(t, "first reservation", func() bool { return getJob(t, repo, job.ID).AssignedWorkerID != nil })
	holder := *getJob(t, repo, job.ID).AssignedWorkerID
	other := "w_1"
	if holder == "w_1" {
		other = "w_2"
	}

	// The other worker polls one second before the expiry (poll freshness is one accept window),
	// so it is eligible when the reservation lapses; the holder never polls.
	clk.Advance(settings.AcceptWindow - time.Second)
	if err := repo.RecordPoll(context.Background(), other); err != nil {
		t.Fatalf("RecordPoll(%s): %v", other, err)
	}
	clk.Advance(time.Second + time.Millisecond)
	expiredAt := clk.Now()
	// The first pass that starts at or after the expiry must revoke the reservation (lapse).
	waitFor(t, "a pass after the expiry", func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		for i, s := range repo.starts {
			if !s.Before(expiredAt) && i < len(repo.results) {
				return true
			}
		}
		return false
	})
	repo.mu.Lock()
	first := -1
	for i, s := range repo.starts {
		if !s.Before(expiredAt) {
			first = i
			break
		}
	}
	firstRes := repo.results[first]
	repo.mu.Unlock()
	if firstRes.Lapsed != 1 {
		t.Fatalf("first pass after the expiry: lapsed=%d, want 1 (INV-6: revoke within one pass)", firstRes.Lapsed)
	}

	waitFor(t, "the reservation to the other worker", func() bool {
		j := getJob(t, repo, job.ID)
		return j.AssignedWorkerID != nil && *j.AssignedWorkerID == other
	})
	a, err := svc.PollWork(context.Background(), other)
	if err != nil {
		t.Fatalf("PollWork(%s): %v", other, err)
	}
	if a.JobID != job.ID || a.Policy != domain.DispatchPolicyRoundRobin {
		t.Fatalf("claim = job %s policy %q, want job %s by round_robin", a.JobID, a.Policy, job.ID)
	}
}

// waitFor polls cond for up to 5 s of real time.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// graceFixture: a round_robin service with a lease sweeper (startup grace 30 s), the loop, one
// fresh worker and one job reserved to it by the loop. The sweeper interval is one minute, so a
// 35 s jump of the clock is no inter-tick gap that opens an outage window.
type graceFixture struct {
	clk   *mockClock
	repo  *loopRepo
	svc   *coordinator.Service
	sw    *coordinator.LeaseSweeper
	loop  *coordinator.AssignmentLoop
	jobID string
}

func newGraceFixture(t *testing.T) *graceFixture {
	t.Helper()
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	svc.SetGracePeriods(30*time.Second, 30*time.Second)
	sw := svc.NewLeaseSweeper(time.Minute)
	if err := sw.Tick(context.Background()); err != nil {
		t.Fatalf("sweeper tick: %v", err)
	}
	loopWorker(t, svc, repo, "w_1", "echo")
	job := rrSubmit(t, svc, "echo")
	loop := svc.NewAssignmentLoop()
	loop.Round(context.Background())
	if getJob(t, repo, job.ID).AssignedWorkerID == nil {
		t.Fatalf("control: the loop did not reserve the job")
	}
	return &graceFixture{clk: clk, repo: repo, svc: svc, sw: sw, loop: loop, jobID: job.ID}
}

// reserveAndExpire refreshes the worker's heartbeat and poll, lets the loop reserve the job to it,
// then lets the reservation expire and runs one more round.
func reserveAndExpire(t *testing.T, svc *coordinator.Service, repo *loopRepo, loop *coordinator.AssignmentLoop, clk *mockClock, workerID string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.Heartbeat(ctx, workerID, 1); err != nil {
		t.Fatalf("Heartbeat(%s): %v", workerID, err)
	}
	if err := repo.RecordPoll(ctx, workerID); err != nil {
		t.Fatalf("RecordPoll(%s): %v", workerID, err)
	}
	loop.Round(ctx)
	if res := repo.result(repo.passes()); res.Reserved != 1 {
		t.Fatalf("control: the loop reserved %d jobs, want 1", res.Reserved)
	}
	clk.Advance(rrWindow + time.Millisecond)
	loop.Round(ctx)
}

// INV-9 (loop variant): inside the startup grace that the sweeper hands to the pass, an expired
// reservation's lapse is not counted. Control: after the grace ends, the same expiry is counted.
func TestCoordinator_AssignmentSweep_GraceDoesNotCountLapse(t *testing.T) {
	f := newGraceFixture(t)
	ctx := context.Background()

	f.clk.Advance(rrWindow + time.Millisecond) // expired, inside the 30 s startup grace
	f.loop.Round(ctx)
	if got := getJob(t, f.repo, f.jobID).AssignmentLapses; got != 0 {
		t.Fatalf("lapse counted inside the startup grace: assignment_lapses=%d (INV-9)", got)
	}
	ev := lapseEvents(t, f.repo, f.jobID)
	if len(ev) != 1 || ev[0].Details["counted"] != false {
		t.Fatalf("lapse events inside grace = %d, counted=%v; want one with counted=false", len(ev), ev[len(ev)-1].Details["counted"])
	}

	// Control: past the startup grace, after a sweeper tick, an expiry is counted.
	f.clk.Advance(30 * time.Second)
	if err := f.sw.Tick(ctx); err != nil {
		t.Fatalf("sweeper tick: %v", err)
	}
	reserveAndExpire(t, f.svc, f.repo, f.loop, f.clk, "w_1")
	if got := getJob(t, f.repo, f.jobID).AssignmentLapses; got != 1 {
		t.Fatalf("control: lapse outside grace not counted: assignment_lapses=%d, want 1", got)
	}
}

// INV-9 (loop variant): grace never stops revocation. The expired reservation is cleared by the
// pass inside the startup grace (a lapse event is written, and any reservation the job holds
// afterwards is a new one).
func TestCoordinator_AssignmentSweep_GraceStillRevokes(t *testing.T) {
	f := newGraceFixture(t)
	ctx := context.Background()
	oldExpiry := *getJob(t, f.repo, f.jobID).AssignmentExpiresAt

	f.clk.Advance(rrWindow + time.Millisecond)
	f.loop.Round(ctx)
	res := f.repo.result(f.repo.passes())
	if res.LapsedUncounted != 1 {
		t.Fatalf("pass inside grace: uncounted lapses=%d, want 1 (the expired reservation revoked)", res.LapsedUncounted)
	}
	if n := len(lapseEvents(t, f.repo, f.jobID)); n != 1 {
		t.Fatalf("lapse events = %d, want 1", n)
	}
	if j := getJob(t, f.repo, f.jobID); j.AssignmentExpiresAt != nil && !j.AssignmentExpiresAt.After(oldExpiry) {
		t.Fatalf("expired reservation kept under grace (expires %v)", j.AssignmentExpiresAt)
	}
}

// Implementation note 16: until the sweeper's first successful tick the grace state is pending and
// the pass counts no lapse; after the tick (with no startup grace) it counts again.
func TestCoordinator_AssignmentLoop_GracePendingUntilFirstSweep(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	svc.SetGracePeriods(0, 0)
	sw := svc.NewLeaseSweeper(time.Minute) // built, not yet ticked
	loop := svc.NewAssignmentLoop()
	ctx := context.Background()
	loopWorker(t, svc, repo, "w_1", "echo")
	job := rrSubmit(t, svc, "echo")
	loop.Round(ctx)
	if p := repo.params[0]; !p.GracePending {
		t.Fatalf("pass before the first sweeper tick: GracePending=false, want true")
	}

	clk.Advance(rrWindow + time.Millisecond)
	loop.Round(ctx)
	if got := getJob(t, repo, job.ID).AssignmentLapses; got != 0 {
		t.Fatalf("lapse counted before the first sweeper tick: %d", got)
	}

	if err := sw.Tick(ctx); err != nil {
		t.Fatalf("sweeper tick: %v", err)
	}
	reserveAndExpire(t, svc, repo, loop, clk, "w_1")
	if got := getJob(t, repo, job.ID).AssignmentLapses; got != 1 {
		t.Fatalf("lapse after the first sweeper tick: assignment_lapses=%d, want 1", got)
	}
}

// INV-18 (loop variant): a pass run by the loop with more than 10 eligible workers writes a
// JOB_ASSIGNED payload with 10 candidate IDs and the full candidate_count.
func TestCoordinator_AssignmentLoop_EventCapsCandidates(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	for i := 0; i < 12; i++ {
		loopWorker(t, svc, repo, "w_"+string(rune('a'+i)), "echo")
	}
	job := rrSubmit(t, svc, "echo")
	svc.NewAssignmentLoop().Round(context.Background())

	events, err := repo.ListEventsByJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("ListEventsByJob: %v", err)
	}
	var assigned *domain.JobEvent
	for _, e := range events {
		if e.EventType == domain.JobEventAssigned {
			assigned = e
		}
	}
	if assigned == nil {
		t.Fatalf("no JOB_ASSIGNED event")
	}
	cands, ok := assigned.Details["candidates"].([]string)
	if !ok || len(cands) != 10 {
		t.Fatalf("candidates = %v, want 10 IDs (INV-18)", assigned.Details["candidates"])
	}
	if assigned.Details["candidate_count"] != 12 {
		t.Fatalf("candidate_count = %v, want 12", assigned.Details["candidate_count"])
	}
}

// INV-20: with a backlog that forces repeats, a poll that is blocked on s.mu while a pass runs
// acquires the mutex before the next pass starts, and the gap between the end of one pass and the
// start of the next is at least MIN_YIELD on the injected clock.
func TestCoordinator_AssignmentRepeat_YieldsToPolls(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	settings := coordinator.DefaultAssignmentSettings()
	settings.BatchLimit = 1
	svc := loopService(t, repo, clk, settings)
	for _, w := range []string{"w_a", "w_b", "w_c"} {
		loopWorker(t, svc, repo, w, "echo")
	}
	rrHeartbeat(t, svc, "w_idle", "sleep") // supports no queued type: its polls get 204
	for i := 0; i < 3; i++ {
		rrSubmit(t, svc, "echo")
	}

	var (
		mu    sync.Mutex
		order []string
		polls = map[int]chan struct{}{}
	)
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	repo.mu.Lock()
	repo.onStart = func(n int) {
		record("pass")
		ch := make(chan struct{})
		mu.Lock()
		polls[n] = ch
		mu.Unlock()
		go func() { // blocks on s.mu, which the loop holds for this pass
			_, err := svc.PollWork(context.Background(), "w_idle")
			if err != nil && !errors.Is(err, domain.ErrNoQueuedJobs) {
				t.Errorf("idle poll: %v", err)
			}
			record("poll")
			close(ch)
		}()
	}
	repo.mu.Unlock()

	loop := svc.NewAssignmentLoop()
	yields := 0
	loop.SetYieldForTest(func(ctx context.Context, d time.Duration) error {
		yields++
		mu.Lock()
		ch := polls[yields]
		mu.Unlock()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Errorf("yield %d: the poll blocked during pass %d did not get s.mu during the yield (INV-20)", yields, yields)
			return errors.New("poll starved")
		}
		clk.Advance(d)
		return nil
	})
	passes := loop.Round(context.Background())
	if t.Failed() {
		return
	}
	if passes != 4 {
		t.Fatalf("passes = %d, want 4 (three reservations with a batch of one, then an empty pass)", passes)
	}
	mu.Lock()
	last := polls[passes]
	mu.Unlock()
	<-last

	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if want := "pass,poll,pass,poll,pass,poll,pass,poll"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for i := 1; i < len(repo.starts); i++ {
		if gap := repo.starts[i].Sub(repo.ends[i-1]); gap < settings.MinYield {
			t.Fatalf("gap before pass %d = %v, want at least MIN_YIELD %v (INV-20)", i+1, gap, settings.MinYield)
		}
	}
}

// INV-21: a repeat follows only a pass in which some type's batch was full and at least one
// reservation was made.
func TestCoordinator_AssignmentRepeat_OnlyWithProgress(t *testing.T) {
	cases := []struct {
		name       string
		batch      int
		jobs       int
		workers    int
		wantPasses int
	}{
		{"full batch of unplaceable jobs: one pass", 1, 2, 0, 1},
		{"full batch with a reservation repeats until no progress", 1, 3, 3, 4},
		{"reservation without a full batch: one pass", 2, 1, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newMockClock(loopT0)
			repo := newLoopRepo(clk)
			settings := coordinator.DefaultAssignmentSettings()
			settings.BatchLimit = tc.batch
			svc := loopService(t, repo, clk, settings)
			for i := 0; i < tc.workers; i++ {
				loopWorker(t, svc, repo, "w_"+string(rune('a'+i)), "echo")
			}
			for i := 0; i < tc.jobs; i++ {
				rrSubmit(t, svc, "echo")
			}
			loop := svc.NewAssignmentLoop()
			yields := 0
			loop.SetYieldForTest(func(ctx context.Context, d time.Duration) error {
				yields++
				clk.Advance(d)
				if yields > 10 { // a repeat without progress would never stop
					return errors.New("repeat bound reached")
				}
				return nil
			})
			if got := loop.Round(context.Background()); got != tc.wantPasses {
				t.Fatalf("passes in one round = %d, want %d (INV-21)", got, tc.wantPasses)
			}
		})
	}
}

// INV-23 (cost variant): with a persistently failing pass, 1,000 polls from 10 workers, then from
// 100 workers, spread over ten intervals of the injected clock, start at most 11 refresh attempts.
func TestPoll_RefreshAttempts_AtMostOnePerInterval(t *testing.T) {
	for _, workers := range []int{10, 100} {
		clk := newMockClock(loopT0)
		repo := newPassFaultRepo(clk)
		repo.setFailing(true)
		svc := newRRService(t, repo, clk)
		ids := make([]string, workers)
		for i := range ids {
			ids[i] = "w_" + strings.Repeat("x", i/26) + string(rune('a'+i%26))
			rrHeartbeat(t, svc, ids[i], "echo")
		}
		const polls = 1000
		step := 10 * rrInterval / polls
		for i := 0; i < polls; i++ {
			_, err := svc.PollWork(context.Background(), ids[i%workers])
			if err != nil && !errors.Is(err, domain.ErrNoQueuedJobs) {
				t.Fatalf("%d workers, poll %d: %v", workers, i, err)
			}
			clk.Advance(step)
		}
		if got := repo.passCount(); got > 11 || got < 10 {
			t.Fatalf("%d workers: %d refresh attempts from %d polls over ten intervals, want 10 or 11 (INV-23)", workers, got, polls)
		}
	}
}

// INV-24 at loop level: the loop's own failing passes never open the age arm. With the loop
// running and failing, a poll whose own attempt failed may claim one aged job; the next poll,
// inside the rate window, is rate-limited and gets 204 even though the loop's latest pass failed.
func TestCoordinator_AssignmentLoop_RateLimitedPoll_NoAgeClaim(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	loop := svc.NewAssignmentLoop()
	ctx := context.Background()
	rrHeartbeat(t, svc, "w_1", "echo")
	rrHeartbeat(t, svc, "w_2", "echo")
	rrSubmit(t, svc, "echo")
	rrSubmit(t, svc, "echo")
	repo.mu.Lock()
	repo.failing = true
	repo.mu.Unlock()

	clk.Advance(rrFallbackAge + time.Second)
	for _, w := range []string{"w_1", "w_2"} {
		if err := svc.Heartbeat(ctx, w, 1); err != nil {
			t.Fatalf("Heartbeat(%s): %v", w, err)
		}
	}
	loop.Round(ctx) // the loop's pass fails
	a, err := svc.PollWork(ctx, "w_1")
	if err != nil {
		t.Fatalf("control: first poll after the failing loop pass: %v", err)
	}
	if a.Policy != domain.DispatchPolicyRoundRobinFallback || a.FallbackReason != domain.FallbackReasonAge {
		t.Fatalf("control: first poll claimed by %q/%q, want an age fallback (its own attempt failed)", a.Policy, a.FallbackReason)
	}

	clk.Advance(time.Millisecond)
	loop.Round(ctx) // fails again, inside the poll's rate window
	if a, err := svc.PollWork(ctx, "w_2"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("rate-limited poll after a failing loop pass: assignment=%+v err=%v, want 204 (INV-24)", a, err)
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
	c.records = append(c.records, r)
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) count(level slog.Level, substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.records {
		if r.Level == level && strings.Contains(r.Message, substr) {
			n++
		}
	}
	return n
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// A2 (ADR-0017 Section 4.5): while the pass heartbeat is stale, the loop logs at ERROR at most
// once a minute. Over 181 rounds one second apart (three minutes of the injected clock) with every
// pass failing: exactly four lines (at 0, 60, 120 and 180 s). Control: a healthy pass logs none.
func TestCoordinator_AssignmentLoop_StaleHeartbeatErrorRateLimited(t *testing.T) {
	const msg = "assignment pass heartbeat is stale"
	for _, failing := range []bool{true, false} {
		logs := captureLogs(t)
		clk := newMockClock(loopT0)
		repo := newLoopRepo(clk)
		repo.failing = failing
		svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
		loop := svc.NewAssignmentLoop()
		for i := 0; i <= 180; i++ {
			loop.Round(context.Background())
			clk.Advance(time.Second)
		}
		want := 4
		if !failing {
			want = 0
		}
		if got := logs.count(slog.LevelError, msg); got != want {
			t.Fatalf("failing=%v: %d stale heartbeat ERROR lines over 181 rounds, want %d", failing, got, want)
		}
	}
}

// The poll-time refresh threshold (ADR-0017 Section 3.2; implementation note 9 deferred its test to
// slice 5c): a poll refreshes only when the pass heartbeat is older than 2 x interval. At 1.5 x and
// at exactly 2 x no pass runs; 1 ms past 2 x the poll runs one.
func TestPoll_RefreshThreshold_TwiceInterval(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newPassFaultRepo(clk)
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	ctx := context.Background()
	if _, err := repo.AssignQueuedJobs(ctx, storage.AssignParams{AcceptWindow: rrWindow, WorkerTimeout: rrTimeout, BatchLimit: 50, MaxLapses: 3}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	poll := func() {
		t.Helper()
		if _, err := svc.PollWork(ctx, "w_1"); err != nil && !errors.Is(err, domain.ErrNoQueuedJobs) {
			t.Fatalf("PollWork: %v", err)
		}
	}
	for _, step := range []struct {
		advance time.Duration
		want    int
	}{
		{rrInterval * 3 / 2, 1}, // 1.5 x: fresh
		{rrInterval / 2, 1},     // exactly 2 x: not older than the threshold
		{time.Millisecond, 2},   // 2 x + 1 ms: stale, one refresh
	} {
		clk.Advance(step.advance)
		poll()
		if got := repo.passCount(); got != step.want {
			t.Fatalf("heartbeat age %v: %d passes, want %d", clk.Now().Sub(loopT0), got, step.want)
		}
	}
}

// The submit trigger (ADR-0017 Section 2.1): with a timer far away, a submit runs a pass at once.
func TestCoordinator_AssignmentLoop_SubmitTriggersPass(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	settings := coordinator.AssignmentSettings{
		AcceptWindow: 10 * time.Second, Interval: 10 * time.Second, MaxLapses: 3,
		FallbackAge: time.Minute, BatchLimit: 50, MinYield: 10 * time.Millisecond,
	}
	svc := loopService(t, repo, clk, settings)
	loopWorker(t, svc, repo, "w_1", "echo")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.StartAssignmentLoop(ctx) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "the first round at start", func() bool { return repo.passes() >= 1 })
	job := rrSubmit(t, svc, "echo")
	start := time.Now()
	waitFor(t, "a pass after the submit", func() bool { return getJob(t, repo, job.ID).AssignedWorkerID != nil })
	if elapsed := time.Since(start); elapsed >= settings.Interval {
		t.Fatalf("reservation took %v, not before the 10 s timer", elapsed)
	}
}
