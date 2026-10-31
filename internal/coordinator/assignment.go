package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// AssignmentSettings are the round-robin inputs of the coordinator (ADR-0017 Section 5.5). They
// are validated by internal/config before they reach the service.
type AssignmentSettings struct {
	AcceptWindow time.Duration
	Interval     time.Duration
	MaxLapses    int
	FallbackAge  time.Duration
	BatchLimit   int
	MinYield     time.Duration
}

// DefaultAssignmentSettings returns the ADR-0017 Section 5.5 defaults.
func DefaultAssignmentSettings() AssignmentSettings {
	return AssignmentSettings{
		AcceptWindow: 5 * time.Second,
		Interval:     1 * time.Second,
		MaxLapses:    3,
		FallbackAge:  30 * time.Second,
		BatchLimit:   50,
		MinYield:     10 * time.Millisecond,
	}
}

// SetDispatchPolicy selects fifo_pull (the default) or round_robin (ADR-0017 Section 5.2). Any
// other name is rejected. round_robin is rejected when the repository does not implement
// storage.AssignmentRepository (PostgreSQL until slice 5b), so the failure is at configuration.
func (s *Service) SetDispatchPolicy(name string) error {
	if !domain.IsValidDispatchPolicy(name) {
		return fmt.Errorf("%w: dispatch policy %q", domain.ErrInvalidRequest, name)
	}
	if name == domain.DispatchPolicyRoundRobin {
		if _, ok := s.repo.(storage.AssignmentRepository); !ok {
			return fmt.Errorf("%w: dispatch policy round_robin needs an assignment-capable repository", domain.ErrInvalidRequest)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = name
	return nil
}

// SetAssignmentSettings sets the round-robin inputs. It is used only under the round_robin policy.
func (s *Service) SetAssignmentSettings(a AssignmentSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignment = a
}

// pollRoundRobinLocked is the round_robin poll (ADR-0017 Sections 3.2 and 3.5). The caller holds
// s.mu.
//
// The poll first records its own freshness, so a HEALTHY polling worker is eligible for the
// reservations its refresh makes. It then runs the poll-time refresh (N1, N6), and claims: its own
// reservation first, then the fallback arms. The age arm is open only for a poll whose own refresh
// attempt failed in this call.
func (s *Service) pollRoundRobinLocked(ctx context.Context, workerID string) (*WorkAssignment, error) {
	ar, ok := s.repo.(storage.AssignmentRepository)
	if !ok {
		return nil, fmt.Errorf("%w: dispatch policy round_robin needs an assignment-capable repository", domain.ErrInvalidRequest)
	}
	if err := ar.RecordPoll(ctx, workerID); err != nil {
		return nil, err
	}

	ageArm := s.refreshIfStaleLocked(ctx, ar)
	claim, err := ar.ClaimForPoll(ctx, workerID, storage.ClaimOptions{
		MaxLapses:   s.assignment.MaxLapses,
		FallbackAge: s.assignment.FallbackAge,
		AgeArmOpen:  ageArm,
	})
	if err != nil {
		if errors.Is(err, domain.ErrWorkerNoCapacity) {
			return nil, domain.ErrNoWorkersAvailable
		}
		return nil, err
	}

	slog.Info("job dispatched to worker",
		"job_id", claim.Job.ID,
		"attempt_id", claim.Attempt.ID,
		"attempt_no", claim.Attempt.AttemptNo,
		"worker_id", workerID,
		"policy", claim.Policy,
		"fallback_reason", claim.FallbackReason,
	)
	return &WorkAssignment{
		JobID:          claim.Job.ID,
		AttemptID:      claim.Attempt.ID,
		AttemptNo:      claim.Attempt.AttemptNo,
		LeaseToken:     claim.Attempt.LeaseToken,
		TaskType:       claim.Job.TaskType,
		Payload:        claim.Job.Payload,
		TimeoutSeconds: claim.Job.TimeoutSeconds,

		LeaseExpiresAt:       claim.Attempt.LeaseExpiresAt,
		LeaseDurationSeconds: int(s.leaseDuration / time.Second),
		Policy:               claim.Policy,
		FallbackReason:       claim.FallbackReason,
	}, nil
}

// refreshIfStaleLocked runs one assignment pass before a claim when the pass heartbeat is older
// than 2 x MINICLOUD_ASSIGNMENT_INTERVAL (ADR-0017 Section 3.2). It returns true only when this
// call ran the attempt and the attempt failed: the only case in which the age arm may open (N6).
//
// Attempts are single-flight and rate-limited to one per interval. The three values it reads and
// writes (refreshInFlight, refreshLastAttemptAt, refreshLastFailed) are coordinator memory guarded
// by s.mu. A skipped pass (another pass holds the lock) is not a failure.
func (s *Service) refreshIfStaleLocked(ctx context.Context, ar storage.AssignmentRepository) bool {
	last, err := ar.AssignmentPassState(ctx)
	if err != nil {
		return false
	}
	now := s.clock.Now()
	interval := s.assignment.Interval
	if now.Sub(last) <= 2*interval {
		return false
	}
	if s.refreshInFlight {
		return false
	}
	if !s.refreshLastAttemptAt.IsZero() && now.Sub(s.refreshLastAttemptAt) < interval {
		return false
	}

	s.refreshInFlight = true
	s.refreshLastAttemptAt = now
	_, err = ar.AssignQueuedJobs(ctx, storage.AssignParams{
		AcceptWindow:  s.assignment.AcceptWindow,
		WorkerTimeout: s.workerTimeout,
		BatchLimit:    s.assignment.BatchLimit,
		MaxLapses:     s.assignment.MaxLapses,
	})
	s.refreshInFlight = false
	s.refreshLastFailed = err != nil
	if err != nil {
		slog.Warn("assignment pass refresh failed", "error", err)
	}
	return err != nil
}
