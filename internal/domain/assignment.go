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

// EffectiveWorkerSlots is the number of slots the round-robin pass treats a worker as having
// (ADR-0017 Section 2.4). The worker loop is synchronous (ADR-0017 C12), so a worker with a
// PENDING or RUNNING attempt has no free slot for a reservation. The pass uses this constant
// rather than worker_capacity, and it is removed when worker concurrency lands (Q5).
const EffectiveWorkerSlots = 1

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
