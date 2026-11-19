package domain

// Dispatch policy names (ADR-0017 Section 5.2). MINICLOUD_DISPATCH_POLICY selects one of the
// first two; the poll response reports the third for a round-robin claim that was made as a
// fallback (Section 3.2).
const (
	DispatchPolicyFifoPull           = "fifo_pull"
	DispatchPolicyRoundRobin         = "round_robin"
	DispatchPolicyRoundRobinFallback = "round_robin_fallback"
)

// Fallback reasons (ADR-0017 D9, Section 3.2). A job records the reason of the arm that claimed
// it in AssignmentFallbackReason, and the poll response reports it as fallback_reason.
const (
	FallbackReasonLapses = "lapses"
	FallbackReasonAge    = "age"
)

// MaxReservationsPerWorker bounds the live reservations one worker may hold (ADR-0020 Section
// 2.5). It is a constant, not configuration. MaxPollBackoff + MaxReservationsPerWorker x
// IdlePollInterval (3 s + 8 x 200 ms = 4.6 s) stays below the 5 s accept-window floor, so a worker
// can claim every reservation it holds inside the window; it also caps how many jobs one
// registration that polls once and stops can make lapse per window.
const MaxReservationsPerWorker = 8

// AssignableSlots is the number of slots the round-robin pass and the fallback claim give a worker
// (ADR-0020 Section 2.1): totalSlots when the worker declared concurrent execution at its latest
// registration, otherwise 1 (the v1.0.0 and V2-0 workers do not declare it).
func AssignableSlots(executesConcurrently bool, totalSlots int) int {
	if executesConcurrently {
		return totalSlots
	}
	return 1
}

// CanReserve reports whether a worker with assignable slots, active PENDING or RUNNING attempts
// and live reservations may receive one more reservation (ADR-0020 Section 2.1, INV-10r):
// active + reserved < assignable and reserved < MaxReservationsPerWorker.
func CanReserve(assignable, active, reserved int) bool {
	return active+reserved < assignable && reserved < MaxReservationsPerWorker
}

// HasFallbackRoom reports whether a fallback claim may give the worker one more attempt (ADR-0020
// Section 2.4): active + reserved < assignable. A fallback claim creates no reservation, so the
// reservation cap does not apply.
func HasFallbackRoom(assignable, active, reserved int) bool {
	return active+reserved < assignable
}

// IsValidDispatchPolicy reports whether name selects a dispatch policy that may be configured.
// The fallback policy name is an output of a claim, not a setting, so it is not valid here.
func IsValidDispatchPolicy(name string) bool {
	return name == DispatchPolicyFifoPull || name == DispatchPolicyRoundRobin
}

// ClearAssignment removes a reservation from the job. It is called in the same step as every
// transition that leaves QUEUED or RETRYING (ADR-0017 Section 1.1). AssignmentLapses and
// AssignmentFallbackReason are not touched here: a claim resets lapses explicitly, and the
// fallback reason is history.
func (j *Job) ClearAssignment() {
	j.AssignedWorkerID = nil
	j.AssignedAt = nil
	j.AssignmentExpiresAt = nil
}
