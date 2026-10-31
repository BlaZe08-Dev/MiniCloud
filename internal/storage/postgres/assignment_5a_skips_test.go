package postgres

import "testing"

// Postgres sides of the slice 5a assignment cases (ADR-0017 Section 7). The in-memory repository
// runs the 5a cases; the PostgreSQL assignment repository is slice 5b, and no assignment method
// exists on the PostgreSQL repository yet. Each case is a visible skip, not a pass.
const assignment5bSkipReason = "slice 5b: PostgreSQL assignment (migration 000005, AssignQueuedJobs, ClaimForPoll) is not implemented; ADR-0017 Section 7"

func TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_Assign_CreatesNoAttempt(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_ClaimAssigned_OnlyAssignedWithinWindow(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_AssignmentLapse_ConsumesNoAttempt(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_AssignmentLapse_FallbackAfterLimit(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_CancelAssigned_NeverClaimed(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_Assign_OneReservationPerWorker(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_Assign_SkipsWorkerWithRunningAttempt(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_Assign_SkipsStaleWorkers(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_FifoPull_IgnoresAssignmentColumns(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}

func TestConformance_AssignEvent_CapsCandidates(t *testing.T) {
	t.Skip(assignment5bSkipReason)
}
