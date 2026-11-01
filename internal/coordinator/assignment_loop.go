package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// StaleHeartbeatLogEvery is the rate limit of the stale pass heartbeat ERROR log (ADR-0017
// Section 4.5, A2): at most one line per minute.
const StaleHeartbeatLogEvery = time.Minute

// AssignmentLoop runs the assignment pass of ADR-0017 Section 2.1 beside the lease sweeper: on a
// timer (MINICLOUD_ASSIGNMENT_INTERVAL), immediately after a submit, and again after a pass that
// made progress on a full batch (the repeat rule). It is driven by a single goroutine; it is not
// safe for concurrent use.
//
// The loop makes no time decision of its own. Expiry, eligibility, grace and staleness are decided
// by the repository on its clock (the database clock in PostgreSQL); the loop passes durations,
// counts and the sweeper's grace state. The process clock is read only for the rate limit of the
// stale heartbeat log, which compares two process-clock readings.
type AssignmentLoop struct {
	svc *Service
	// yield waits between a pass and its repeat, with s.mu released (INV-20). Tests replace it.
	yield func(ctx context.Context, d time.Duration) error
	// lastStaleLog is the process time of the last stale heartbeat ERROR line.
	lastStaleLog time.Time
}

// NewAssignmentLoop builds the assignment loop of the service.
func (s *Service) NewAssignmentLoop() *AssignmentLoop {
	return &AssignmentLoop{svc: s, yield: sleepContext}
}

// StartAssignmentLoop runs the assignment loop until ctx is cancelled. The coordinator binary
// starts it only under the round_robin policy.
func (s *Service) StartAssignmentLoop(ctx context.Context) {
	s.NewAssignmentLoop().Run(ctx)
}

// Run runs a round immediately and then on every timer tick and every submit trigger, until ctx is
// cancelled. A failed pass is logged; it never stops the loop.
func (l *AssignmentLoop) Run(ctx context.Context) {
	l.svc.mu.Lock()
	interval := l.svc.assignment.Interval
	l.svc.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		l.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-l.svc.assignKick:
		}
	}
}

// Round runs one pass and repeats it, after a yield, while the repeat rule holds (ADR-0017
// Section 2.1, N2): the pass read a full batch for some task type and made at least one
// reservation. A pass that fails, is skipped, or makes no progress ends the round (INV-21). It then
// checks the pass heartbeat (Section 4.5) and returns the number of passes it ran.
func (l *AssignmentLoop) Round(ctx context.Context) int {
	passes := 0
	for ctx.Err() == nil {
		res, minYield, batchLimit, err := l.pass(ctx)
		passes++
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("assignment pass failed", "err", err)
			}
			break
		}
		if res.Skipped {
			slog.Info("assignment pass skipped: another pass holds the pass lock")
			break
		}
		if !repeatsAfter(res, batchLimit) {
			break
		}
		// The yield runs with s.mu released, so a poll waiting for the mutex takes it before the
		// next pass (INV-20).
		if err := l.yield(ctx, minYield); err != nil {
			break
		}
	}
	l.checkStaleHeartbeat(ctx)
	return passes
}

// pass runs one assignment pass under s.mu (ADR-0017 Section 4.1) and returns its result with the
// yield and batch limit in force for it.
func (l *AssignmentLoop) pass(ctx context.Context) (storage.AssignResult, time.Duration, int, error) {
	s := l.svc
	passCtx, cancel := context.WithTimeout(ctx, BackgroundCallTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, ok := s.repo.(storage.AssignmentRepository)
	if !ok {
		return storage.AssignResult{}, 0, 0, fmt.Errorf("%w: the assignment loop needs an assignment-capable repository", domain.ErrInvalidRequest)
	}
	params := s.assignParamsLocked()
	res, err := ar.AssignQueuedJobs(passCtx, params)
	return res, s.assignment.MinYield, params.BatchLimit, err
}

// repeatsAfter is the repeat rule of ADR-0017 Section 2.1 (N2, further defect 19): some task
// type's candidate read returned a full batch, and the pass made at least one reservation.
func repeatsAfter(res storage.AssignResult, batchLimit int) bool {
	if res.Skipped || res.Reserved == 0 {
		return false
	}
	for _, n := range res.CandidatesRead {
		if n >= batchLimit {
			return true
		}
	}
	return false
}

// checkStaleHeartbeat logs at ERROR, at most once per StaleHeartbeatLogEvery, while the pass
// heartbeat is older than 2 x MINICLOUD_ASSIGNMENT_INTERVAL (ADR-0017 Section 4.5, A2). Staleness
// is decided by the repository on its clock (implementation note 8); the rate limit compares two
// process-clock readings.
func (l *AssignmentLoop) checkStaleHeartbeat(ctx context.Context) {
	s := l.svc
	st, ok := s.repo.(storage.AssignmentPassStaleness)
	if !ok || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	threshold := 2 * s.assignment.Interval
	clk := s.clock
	s.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, BackgroundCallTimeout)
	defer cancel()
	stale, err := st.AssignmentPassStale(checkCtx, threshold)
	if err != nil || !stale {
		return
	}
	now := clk.Now()
	if !l.lastStaleLog.IsZero() && now.Sub(l.lastStaleLog) < StaleHeartbeatLogEvery {
		return
	}
	l.lastStaleLog = now
	slog.Error("assignment pass heartbeat is stale: no assignment pass has completed within the threshold; "+
		"remedy: set MINICLOUD_DISPATCH_POLICY=fifo_pull and restart the coordinator",
		"threshold", threshold)
}

// sleepContext waits d or until ctx is cancelled.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
