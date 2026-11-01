package coordinator

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

const (
	// DefaultSweeperInterval is the lease sweeper cadence (ADR-0014).
	DefaultSweeperInterval = 5 * time.Second
	// DefaultStartupGracePeriod and DefaultOutageGraceWindow are the ADR-0014 defaults. Grace is on
	// unless explicitly shortened: a Service that forgets to configure it must not reap healthy
	// attempts after a restart.
	DefaultStartupGracePeriod = 30 * time.Second
	DefaultOutageGraceWindow  = 30 * time.Second

	// gapIntervals is the database-time gap, in sweep intervals, over which consecutive successful
	// ticks mean the sweeper (or its database) was away (D-R3-01).
	gapIntervals = 3
)

// SetGracePeriods sets the startup grace period and the outage grace window (which is also the
// length of the cooldown that follows a window). The values are validated against the lease
// duration by internal/config; a negative value is treated as zero.
func (s *Service) SetGracePeriods(startup, outageWindow time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupGrace = max(startup, 0)
	s.outageWindow = max(outageWindow, 0)
}

// SetSweepRandom injects the randomness behind the retry backoff jitter, a source of values in
// [0, 1). Tests use it for deterministic backoff.
func (s *Service) SetSweepRandom(rnd func() float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rnd != nil {
		s.sweepRand = rnd
	}
}

// retryBackoffLocked is the job-retry backoff shared by the lease sweeper and the result path: the
// ADR-0015 policy (domain.ComputeBackoff) over the service's random source. The caller holds s.mu.
func (s *Service) retryBackoffLocked() domain.BackoffFunc {
	return domain.DefaultRetryPolicy().BackoffFunc(s.sweepRand)
}

// LeaseSweeper owns the sweeper's grace state and advances it tick by tick (ADR-0014, D-R3-01).
// The repository only reads that state inside its row locks; it never advances it.
//
// All grace instants are computed from repository time (database time in PostgreSQL), the same
// clock the in-lock check uses (owner decision Q-1), never from process time. A LeaseSweeper is
// driven by a single goroutine; it is not safe for concurrent use.
type LeaseSweeper struct {
	svc          *Service
	interval     time.Duration
	startupGrace time.Duration
	window       time.Duration
	backoff      domain.BackoffFunc

	state sweepState
}

// sweepState is the part of the sweeper that persists between ticks. It is a plain value so a tick
// can work on a copy and commit it only when the tick succeeded.
type sweepState struct {
	startupSet    bool
	startupUntil  time.Time
	windowUntil   time.Time
	cooldownUntil time.Time
	lastOK        time.Time
	hasLastOK     bool
	failedSince   bool // a tick failed since the last successful one
}

// NewLeaseSweeper builds a sweeper with the Service's grace settings and retry policy. A
// non-positive interval means DefaultSweeperInterval.
func (s *Service) NewLeaseSweeper(interval time.Duration) *LeaseSweeper {
	if interval <= 0 {
		interval = DefaultSweeperInterval
	}
	// From now on the assignment pass takes its grace state from this sweeper; until its first
	// successful tick the state is pending and no lapse is counted (implementation note 16).
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishPassGrace(domain.SweepGraceState{}, false, s.startupGrace)
	return &LeaseSweeper{
		svc:          s,
		interval:     interval,
		startupGrace: s.startupGrace,
		window:       s.outageWindow,
		backoff:      s.retryBackoffLocked(),
	}
}

// StartLeaseSweeper runs the lease sweeper until ctx is cancelled. It ticks once immediately, which
// anchors the startup grace at coordinator start, and then every interval.
func (s *Service) StartLeaseSweeper(ctx context.Context, interval time.Duration) {
	s.NewLeaseSweeper(interval).Run(ctx)
}

// Run ticks immediately and then every interval until ctx is cancelled. A failed tick is logged
// and counted by the grace state; it never stops the loop.
func (w *LeaseSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		// A deadline per tick: a database connection that went half-open must fail the tick, not
		// park this loop for good (F-4e-5). A timed-out tick is a failed tick and so opens the
		// outage grace window on the first success afterwards.
		tickCtx, cancel := context.WithTimeout(ctx, BackgroundCallTimeout)
		err := w.Tick(tickCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("lease sweeper tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one sweep. Only a returned error (reading the repository clock, or
// SweepExpiredLeases) is a failed tick; a lock timeout or SKIP LOCKED skip is reported in the
// result and is not. On a failed tick the state is not advanced except for the failure flag, so
// the first successful tick after it opens the outage window.
func (w *LeaseSweeper) Tick(ctx context.Context) error {
	now, err := w.svc.repo.Now(ctx)
	if err != nil {
		w.state.failedSince = true
		w.svc.publishPassGrace(domain.SweepGraceState{}, false, w.window)
		return err
	}
	if !w.state.startupSet {
		w.state.startupSet = true
		w.state.startupUntil = now.Add(w.startupGrace)
	}

	next, opened := w.advance(w.state, now)
	grace := domain.SweepGraceState{
		StartupGraceUntil: next.startupUntil,
		OutageWindowUntil: next.windowUntil,
		CooldownUntil:     next.cooldownUntil,
	}
	res, err := w.svc.repo.SweepExpiredLeases(ctx, domain.SweepParams{
		Limit:   domain.DefaultSweepBatchSize,
		Grace:   grace,
		Backoff: w.backoff,
	})
	if err != nil {
		// The window decided for this tick is discarded; the next successful tick decides again.
		w.state.failedSince = true
		w.svc.publishPassGrace(domain.SweepGraceState{}, false, w.window)
		return err
	}
	// The assignment pass gets the same grace state as the sweep (ADR-0017 Section 4.4).
	w.svc.publishPassGrace(grace, true, 0)

	next.lastOK = now
	next.hasLastOK = true
	next.failedSince = false
	w.state = next
	if opened {
		slog.Warn("lease sweeper opened an outage grace window: reaping suppressed",
			"until", next.windowUntil, "window", w.window)
	}
	w.report(res)
	return nil
}

// advance computes the state for a successful tick at database time now: it arms the cooldown once
// a window has ended, then opens a new window if a failed tick or a gap over 3x the interval
// triggered one and neither a window nor a cooldown is active. The trigger is consumed either way.
func (w *LeaseSweeper) advance(st sweepState, now time.Time) (sweepState, bool) {
	// Cooldown starts exactly when the window ends, whichever tick notices first.
	if !st.windowUntil.IsZero() && !now.Before(st.windowUntil) && st.cooldownUntil.Before(st.windowUntil) {
		st.cooldownUntil = st.windowUntil.Add(w.window)
	}

	gap := st.hasLastOK && now.Sub(st.lastOK) > gapIntervals*w.interval
	trigger := st.failedSince || gap
	if trigger && !now.Before(st.windowUntil) && !now.Before(st.cooldownUntil) {
		st.windowUntil = now.Add(w.window)
		return st, true
	}
	return st, false
}

func (w *LeaseSweeper) report(res *domain.SweepResult) {
	if res == nil {
		return
	}
	for _, o := range res.Swept {
		slog.Info("lease sweeper closed an expired attempt",
			"job_id", o.JobID,
			"attempt_id", o.AttemptID,
			"attempt_state", o.AttemptState,
			"job_state", o.JobState,
		)
	}
	if res.SuppressedByGrace > 0 {
		slog.Info("lease sweeper: expired attempts held back by grace",
			"suppressed", res.SuppressedByGrace)
	}
	if res.SkippedChanged > 0 {
		slog.Warn("lease sweeper: candidates changed state outside every sweep rule",
			"skipped_changed", res.SkippedChanged, "candidates", res.Candidates)
	}
}

// publishPassGrace records the grace state the assignment pass receives (ADR-0017 Section 4.4).
// ready is false before the sweeper's first successful tick and after a failed tick; the pass then
// counts no lapse, but only for pendingCap on the repository clock (implementation notes 16 and
// 25). A pending state that is already running keeps its start and its cap: a further failure does
// not extend it. The caller must not hold graceMu.
func (s *Service) publishPassGrace(g domain.SweepGraceState, ready bool, pendingCap time.Duration) {
	s.graceMu.Lock()
	defer s.graceMu.Unlock()
	if !ready && (!s.graceSource || s.graceReady) {
		s.pendingSince = time.Time{}
		s.pendingCap = max(pendingCap, 0)
	}
	s.graceSource = true
	s.graceReady = ready
	s.passGrace = g
}

// passGraceInput returns the grace input of an assignment pass. With no lease sweeper the pass has
// no grace and counts every lapse, as in slices 5a and 5b. While the state is pending, the cap is
// anchored on the first read of the repository clock after it began and the pass receives the
// instant it ends (implementation note 25); if the clock cannot be read the pass gets no end and
// stays pending, and a pass that works implies a clock that works.
func (s *Service) passGraceInput(ctx context.Context) (grace domain.SweepGraceState, pending bool, pendingUntil time.Time) {
	s.graceMu.Lock()
	defer s.graceMu.Unlock()
	if !s.graceSource {
		return domain.SweepGraceState{}, false, time.Time{}
	}
	if s.graceReady {
		return s.passGrace, false, time.Time{}
	}
	if s.pendingSince.IsZero() {
		if now, err := s.repo.Now(ctx); err == nil {
			s.pendingSince = now
		}
	}
	if !s.pendingSince.IsZero() {
		pendingUntil = s.pendingSince.Add(s.pendingCap)
	}
	return s.passGrace, true, pendingUntil
}

func newSweepRand() func() float64 { return rand.Float64 }
