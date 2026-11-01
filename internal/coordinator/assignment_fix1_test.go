package coordinator_test

// P4-08d fix round 1: the pending-grace cap (review F-R1, ADR-0017 note 25) and the killing tests
// for the audit's surviving mutants A02 to A05 (F-R2 to F-R5).

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

var errCapSweep = errors.New("injected sweep failure")

// capSweepRepo fails SweepExpiredLeases while failSweep is set; Now and the assignment pass keep
// working, which is the F-R1 trigger.
type capSweepRepo struct {
	*loopRepo
	failSweep bool
}

func (r *capSweepRepo) SweepExpiredLeases(ctx context.Context, p domain.SweepParams) (*domain.SweepResult, error) {
	if r.failSweep {
		return nil, errCapSweep
	}
	return r.loopRepo.SweepExpiredLeases(ctx, p)
}

// capRun drives reservations that all lapse (the chosen worker stays fresh and never claims) and
// returns the "counted" flag of each lapse event in order, the job's lapse count, and whether a
// second worker got the job by the fallback. mode "never-ticked": the sweeper is built and never
// ticked (cap = startup grace). mode "failed-sweep": one good tick, then every sweep fails (cap =
// outage window). mode "control": every tick succeeds, with no grace.
func capRun(t *testing.T, mode string, startup, outage time.Duration, cycles int) (counted []bool, lapses int, fallback bool) {
	t.Helper()
	clk := newMockClock(loopT0)
	base := newLoopRepo(clk)
	repo := &capSweepRepo{loopRepo: base}
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	if mode == "control" {
		startup, outage = 0, 0
	}
	svc.SetGracePeriods(startup, outage)
	sw := svc.NewLeaseSweeper(time.Minute)
	loop := svc.NewAssignmentLoop()
	ctx := context.Background()
	if mode == "failed-sweep" {
		if err := sw.Tick(ctx); err != nil {
			t.Fatalf("first tick: %v", err)
		}
		repo.failSweep = true
		if err := sw.Tick(ctx); !errors.Is(err, errCapSweep) {
			t.Fatalf("failing tick: err=%v, want the injected failure", err)
		}
	}
	loopWorker(t, svc, base, "w_1", "echo")
	job := rrSubmit(t, svc, "echo")
	for i := 0; i < cycles; i++ {
		if mode == "control" {
			if err := sw.Tick(ctx); err != nil {
				t.Fatalf("tick: %v", err)
			}
		}
		if err := svc.Heartbeat(ctx, "w_1", 1); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		if err := base.RecordPoll(ctx, "w_1"); err != nil {
			t.Fatalf("RecordPoll: %v", err)
		}
		loop.Round(ctx)
		clk.Advance(rrWindow + time.Millisecond)
		loop.Round(ctx)
	}
	for _, e := range lapseEvents(t, base, job.ID) {
		c, _ := e.Details["counted"].(bool)
		counted = append(counted, c)
	}
	lapses = getJob(t, base, job.ID).AssignmentLapses // before the claim, which resets it
	rrHeartbeat(t, svc, "w_2", "echo")
	wa, err := svc.PollWork(ctx, "w_2")
	return counted, lapses, err == nil && wa != nil && wa.JobID == job.ID
}

// F-R1 (major): the pending grace ends at its cap on the repository clock. Reservations lapse every
// window (5 s plus 1 ms). Before a first sweeper tick the cap is the startup grace (12 s): lapses
// at 5.001 s and 10.002 s are held back, the one at 15.003 s counts. After a failed sweep the cap
// is the outage window (7 s): 5.001 s is held back, 10.002 s counts. Three counted lapses end
// round-robin for the job (L = 3), and a second worker then takes it by the fallback (FS-19).
func TestCoordinator_AssignmentLoop_PendingGraceEndsAtCap(t *testing.T) {
	const startup, outage = 12 * time.Second, 7 * time.Second
	for _, c := range []struct {
		mode    string
		cycles  int
		counted []bool
	}{
		{"control", 3, []bool{true, true, true}},
		{"never-ticked", 6, []bool{false, false, true, true, true}},
		{"failed-sweep", 6, []bool{false, true, true, true}},
	} {
		counted, lapses, fb := capRun(t, c.mode, startup, outage, c.cycles)
		if len(counted) != len(c.counted) {
			t.Fatalf("%s: lapse events counted=%v, want %v", c.mode, counted, c.counted)
		}
		for i := range counted {
			if counted[i] != c.counted[i] {
				t.Fatalf("%s: lapse events counted=%v, want %v", c.mode, counted, c.counted)
			}
		}
		if lapses != 3 {
			t.Fatalf("%s: assignment_lapses=%d, want 3", c.mode, lapses)
		}
		if !fb {
			t.Fatalf("%s: the second worker did not get the job by the fallback after 3 lapses (FS-19)", c.mode)
		}
	}
}

// F-R2: a failed sweep tick marks the pass grace pending (note 16, "after a failed tick"). A
// sweeper that had succeeded once and then fails must hold the next lapse back; with the pending
// publish removed, the stale ready state would count it at once.
func TestCoordinator_AssignmentLoop_FailedSweepMarksGracePending(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := &capSweepRepo{loopRepo: newLoopRepo(clk)}
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	svc.SetGracePeriods(0, time.Minute)
	sw := svc.NewLeaseSweeper(time.Minute)
	loop := svc.NewAssignmentLoop()
	ctx := context.Background()
	if err := sw.Tick(ctx); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	loop.Round(ctx)
	if p := repo.params[len(repo.params)-1]; p.GracePending {
		t.Fatalf("pass after a good tick: GracePending=true, want false")
	}
	repo.failSweep = true
	if err := sw.Tick(ctx); err == nil {
		t.Fatalf("tick with a failing sweep returned nil")
	}
	loop.Round(ctx)
	p := repo.params[len(repo.params)-1]
	if !p.GracePending {
		t.Fatalf("pass after a failed sweep tick: GracePending=false, want true (note 16)")
	}
	if p.GracePendingUntil.IsZero() {
		t.Fatalf("pass after a failed sweep tick: no end of the pending state (note 25)")
	}
}

// F-R3, note 19: the submit trigger is a buffer of one, so a burst of submits adds at most one
// round. Five jobs are submitted before the loop runs; the loop then runs its start round and one
// trigger round, and no more. With a larger buffer it would run one round per submit.
func TestCoordinator_AssignmentLoop_SubmitBurstAddsOneRound(t *testing.T) {
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	settings := coordinator.AssignmentSettings{
		AcceptWindow: 10 * time.Second, Interval: 10 * time.Second, MaxLapses: 3,
		FallbackAge: time.Minute, BatchLimit: 50, MinYield: 10 * time.Millisecond,
	}
	svc := loopService(t, repo, clk, settings)
	loopWorker(t, svc, repo, "w_1", "echo")
	for i := 0; i < 5; i++ {
		rrSubmit(t, svc, "echo")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.StartAssignmentLoop(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "the start round and the trigger round", func() bool { return repo.passes() >= 2 })
	time.Sleep(200 * time.Millisecond) // a larger buffer would have run its extra rounds by now
	if got := repo.passes(); got != 2 {
		t.Fatalf("passes after a burst of 5 submits = %d, want 2 (start round + one trigger round)", got)
	}
}

// F-R4, A2: the stale heartbeat ERROR window is one minute. Every pass fails. Rounds at 0 s (logs),
// 50 s (inside the window: no line) and 61 s (a new line).
func TestCoordinator_AssignmentLoop_StaleHeartbeatWindowIsOneMinute(t *testing.T) {
	const msg = "assignment pass heartbeat is stale"
	logs := captureLogs(t)
	clk := newMockClock(loopT0)
	repo := newLoopRepo(clk)
	repo.failing = true
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	loop := svc.NewAssignmentLoop()
	ctx := context.Background()
	step := func(advance time.Duration, want int, at string) {
		t.Helper()
		clk.Advance(advance)
		loop.Round(ctx)
		if got := logs.count(slog.LevelError, msg); got != want {
			t.Fatalf("at %s: %d stale heartbeat ERROR lines, want %d", at, got, want)
		}
	}
	step(0, 1, "0 s")
	step(50*time.Second, 1, "50 s")
	step(11*time.Second, 2, "61 s")
}

// F-R5, INV-20: the production yield waits. The tests that inject a yield cannot see this: the
// function NewAssignmentLoop installs must block for the requested time, and must return early
// with the context's error when the context ends.
func TestAssignmentLoop_RealYield_Waits(t *testing.T) {
	clk := newMockClock(loopT0)
	svc := loopService(t, newLoopRepo(clk), clk, coordinator.DefaultAssignmentSettings())
	yield := svc.NewAssignmentLoop().YieldForTest()
	const d = 100 * time.Millisecond
	start := time.Now()
	if err := yield(context.Background(), d); err != nil {
		t.Fatalf("yield: %v", err)
	}
	if elapsed := time.Since(start); elapsed < d {
		t.Fatalf("real yield returned after %v, want at least %v", elapsed, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if err := yield(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("yield on a cancelled context: err=%v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("yield on a cancelled context took %v", elapsed)
	}
}
