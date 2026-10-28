package domain

import "time"

// DefaultSweepBatchSize is the number of expired-lease candidates examined per sweep call
// (ADR-0014: batch size 50).
const DefaultSweepBatchSize = 50

// LeaseRenewal is the outcome of a successful RenewLease call.
type LeaseRenewal struct {
	// LeaseExpiresAt is the attempt's lease expiry after the call. When CancelRequested is true
	// it is the current, unextended expiry.
	LeaseExpiresAt time.Time
	// CancelRequested is true when the job is CANCEL_REQUESTED. It is the sole cancellation
	// delivery channel to the worker (ADR-0016).
	CancelRequested bool
}

// SweepGraceState is the grace state the sweeper's caller hands to SweepExpiredLeases. The
// repository neither owns nor advances it; it only performs the single authoritative check inside
// the row locks (ADR-0014). All instants are compared with the repository clock (database time in
// PostgreSQL); a zero time means "not set".
type SweepGraceState struct {
	// StartupGraceUntil suppresses reaping until this instant (coordinator startup grace).
	StartupGraceUntil time.Time
	// OutageWindowUntil suppresses reaping until this instant (open outage grace window).
	OutageWindowUntil time.Time
	// CooldownUntil marks the cooldown that follows a window: while it lasts, no outage window
	// applies and reaping proceeds, so suppression never exceeds one window length. It does not
	// lift the startup grace.
	CooldownUntil time.Time
}

// Suppresses reports whether reaping must be skipped at now.
func (g SweepGraceState) Suppresses(now time.Time) bool {
	if now.Before(g.StartupGraceUntil) {
		return true
	}
	if now.Before(g.CooldownUntil) {
		return false
	}
	return now.Before(g.OutageWindowUntil)
}

// SweepParams are the inputs of SweepExpiredLeases.
type SweepParams struct {
	// Limit bounds the candidates examined; 0 means DefaultSweepBatchSize.
	Limit int
	// Grace is the caller-owned grace state, checked inside the row locks.
	Grace SweepGraceState
	// Backoff computes the retry delay for a lost attempt (ComputeBackoff bound to a policy and a
	// random source). It is required: a nil Backoff is rejected with ErrInvalidRequest so that no
	// hidden randomness is used.
	Backoff BackoffFunc
}

// SweepOutcome describes one attempt closed by the sweeper.
type SweepOutcome struct {
	JobID        string
	AttemptID    string
	AttemptState AttemptState
	// JobState is the job's state after the sweep (unchanged when the job was already terminal).
	JobState JobState
}

// SweepResult summarises one SweepExpiredLeases call. A lock timeout or a SKIP LOCKED skip on a
// single candidate is counted in SkippedLocked and is not an error: only a returned error is a
// failed tick (ADR-0014).
type SweepResult struct {
	// Candidates is the number of expired RUNNING attempts found by the unlocked scan.
	Candidates int
	// Swept lists the attempts closed in this call.
	Swept []SweepOutcome
	// SkippedLocked counts candidates skipped because a row lock was not available.
	SkippedLocked int
	// SkippedChanged counts candidates the in-lock re-check found already resolved or renewed.
	SkippedChanged int
	// SuppressedByGrace counts candidates skipped by the in-lock grace check.
	SuppressedByGrace int
}
