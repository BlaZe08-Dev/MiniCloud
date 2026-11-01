package storage

import (
	"context"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// AssignmentRepository is the assignment surface of ADR-0017 that slice 5a implements in memory.
// It is deliberately not part of Repository: PostgreSQL does not implement it until slice 5b, and
// the coordinator asserts for it at run time. Time comes from the repository clock.
type AssignmentRepository interface {
	// RecordPoll writes the poll freshness of a HEALTHY worker, throttled to one write per second
	// (ADR-0017 Section 2.2). An UNHEALTHY or unknown worker is rejected and not written.
	RecordPoll(ctx context.Context, workerID string) error
	// AssignQueuedJobs runs one assignment pass (ADR-0017 Section 2.1): revoke, then reserve per
	// task type, then write the cursors and the pass heartbeat. A pass that cannot take the pass
	// lock returns Skipped and writes nothing.
	AssignQueuedJobs(ctx context.Context, params AssignParams) (AssignResult, error)
	// ClaimForPoll claims a job for a poll by workerID, own reservation first, then the fallback
	// arms (ADR-0017 Section 3.2). It returns ErrNoQueuedJobs when neither arm has a job.
	ClaimForPoll(ctx context.Context, workerID string, opts ClaimOptions) (*AssignedClaim, error)
	// AssignmentPassState returns the time the last completed assignment pass finished. The zero
	// time means no pass has completed in this process.
	AssignmentPassState(ctx context.Context) (time.Time, error)
}

// AssignParams are the inputs of one assignment pass (ADR-0017 Sections 2.1 to 2.2, config
// Section 5.5).
type AssignParams struct {
	AcceptWindow  time.Duration
	WorkerTimeout time.Duration
	BatchLimit    int
	// MaxLapses is MINICLOUD_ASSIGNMENT_MAX_LAPSES. A job whose assignment_lapses reached it has
	// left round-robin (ADR-0017 D9): the pass does not reserve it, so it stays unassigned and
	// becomes claimable by the fallback arm.
	MaxLapses int
	// Grace is the lease sweeper's grace state, passed in exactly as SweepExpiredLeases receives
	// it (ADR-0017 Section 4.4). The pass neither owns nor advances it. While it suppresses
	// reaping at repository time (domain.SweepGraceState.Suppresses), an expired reservation is
	// still revoked, but the lapse is not counted (INV-9). Its instants are repository time (the
	// database clock in PostgreSQL), read by the sweeper from the repository.
	Grace domain.SweepGraceState
	// GracePending is true while the sweeper has not yet produced a grace state: before its first
	// successful tick, and after a failed tick until the next successful one. No lapse is counted
	// then (ADR-0017 implementation note 16). The zero value counts lapses as before.
	GracePending bool
}

// LapseCounts reports whether an expired reservation revoked at repository time now counts as a
// lapse: not while the grace state is pending, and not while it suppresses reaping (ADR-0017
// Section 4.4, implementation note 16). Both repositories call it inside the pass, under the job
// lock, with their own clock (the database clock in PostgreSQL).
func LapseCounts(p AssignParams, now time.Time) bool {
	return !p.GracePending && !p.Grace.Suppresses(now)
}

// AssignResult reports what one pass did. CandidatesRead holds, per task type, the number of
// candidates the pass read, which is at most BatchLimit (ADR-0017 D14).
type AssignResult struct {
	Skipped  bool
	Reserved int
	Revoked  int
	// Lapsed counts expired reservations whose lapse was counted. LapsedUncounted counts expired
	// reservations revoked under grace, whose lapse was not counted (INV-9).
	Lapsed          int
	LapsedUncounted int
	CandidatesRead  map[string]int
}

// ClaimOptions are the fallback inputs of a poll (ADR-0017 Section 3.2).
type ClaimOptions struct {
	MaxLapses   int
	FallbackAge time.Duration
	// AgeArmOpen is true only for a poll whose own refresh attempt failed in this call (N6).
	AgeArmOpen bool
}

// AssignedClaim is a successful poll claim, with the policy that made it.
type AssignedClaim struct {
	Job            *domain.Job
	Attempt        *domain.Attempt
	Policy         string
	FallbackReason string
}
