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

// AssignmentSettings are the round-robin inputs of the coordinator (ADR-0017 Section 5.5).
// internal/config validates the environment variables at startup; Validate applies the same rules
// again at the service boundary, so a caller that bypasses the config cannot run round_robin with
// out-of-range values.
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

// Validate applies the ADR-0017 Section 5.5 ranges and the A1 cross-checks, the same rules as
// validateAssignment in internal/config. It returns domain.ErrInvalidRequest naming the first rule
// that fails.
func (a AssignmentSettings) Validate() error {
	if a.AcceptWindow < 5*time.Second || a.AcceptWindow > 30*time.Second {
		return fmt.Errorf("%w: assignment accept window must be between 5s and 30s (got %v)", domain.ErrInvalidRequest, a.AcceptWindow)
	}
	if a.Interval < 100*time.Millisecond || a.Interval > 10*time.Second {
		return fmt.Errorf("%w: assignment interval must be between 100ms and 10s (got %v)", domain.ErrInvalidRequest, a.Interval)
	}
	if a.MaxLapses < 1 || a.MaxLapses > 10 {
		return fmt.Errorf("%w: assignment max lapses must be between 1 and 10 (got %d)", domain.ErrInvalidRequest, a.MaxLapses)
	}
	lapsePathMinimum := time.Duration(a.MaxLapses) * (a.AcceptWindow + a.Interval)
	if a.FallbackAge < lapsePathMinimum || a.FallbackAge > 10*time.Minute {
		return fmt.Errorf("%w: assignment fallback age must be at least max lapses x (accept window + interval) (%v) and at most 10m (got %v)", domain.ErrInvalidRequest, lapsePathMinimum, a.FallbackAge)
	}
	if a.BatchLimit < 1 || a.BatchLimit > 500 {
		return fmt.Errorf("%w: assignment batch limit must be between 1 and 500 (got %d)", domain.ErrInvalidRequest, a.BatchLimit)
	}
	if a.MinYield < time.Millisecond || a.MinYield > time.Second {
		return fmt.Errorf("%w: assignment min yield must be between 1ms and 1s (got %v)", domain.ErrInvalidRequest, a.MinYield)
	}
	if a.MinYield > a.Interval {
		return fmt.Errorf("%w: assignment min yield (%v) must not exceed the interval (%v)", domain.ErrInvalidRequest, a.MinYield, a.Interval)
	}
	// A1 cross-checks (ADR-0017 Section 5.5).
	if a.Interval > a.AcceptWindow {
		return fmt.Errorf("%w: assignment interval (%v) must not exceed the accept window (%v)", domain.ErrInvalidRequest, a.Interval, a.AcceptWindow)
	}
	if a.FallbackAge <= 2*a.Interval+a.AcceptWindow {
		return fmt.Errorf("%w: assignment fallback age (%v) must be greater than 2 x interval plus the accept window (%v)", domain.ErrInvalidRequest, a.FallbackAge, 2*a.Interval+a.AcceptWindow)
	}
	return nil
}

// SetDispatchPolicy selects fifo_pull (the default) or round_robin (ADR-0017 Section 5.2). Any
// other name is rejected. round_robin is rejected when the repository does not implement
// storage.AssignmentRepository and storage.AssignmentPassStaleness (both the in-memory and the
// PostgreSQL repositories implement them since slice 5b), so the failure is at configuration.
// round_robin is also rejected when the current assignment settings fail Validate (F-5a-1).
func (s *Service) SetDispatchPolicy(name string) error {
	if !domain.IsValidDispatchPolicy(name) {
		return fmt.Errorf("%w: dispatch policy %q", domain.ErrInvalidRequest, name)
	}
	if name == domain.DispatchPolicyRoundRobin {
		if _, ok := s.repo.(storage.AssignmentRepository); !ok {
			return fmt.Errorf("%w: dispatch policy round_robin needs an assignment-capable repository", domain.ErrInvalidRequest)
		}
		// The poll-time refresh decides staleness on the repository clock (implementation note 8).
		if _, ok := s.repo.(storage.AssignmentPassStaleness); !ok {
			return fmt.Errorf("%w: dispatch policy round_robin needs a repository that reports pass staleness", domain.ErrInvalidRequest)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == domain.DispatchPolicyRoundRobin {
		if err := s.assignment.Validate(); err != nil {
			return fmt.Errorf("dispatch policy round_robin: %w", err)
		}
	}
	s.policy = name
	return nil
}

// DispatchPolicy returns the policy in force: fifo_pull until SetDispatchPolicy selects another.
func (s *Service) DispatchPolicy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policy == "" {
		return domain.DispatchPolicyFifoPull
	}
	return s.policy
}

// AssignmentSettings returns the round-robin settings in force.
func (s *Service) AssignmentSettings() AssignmentSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assignment
}

// SetAssignmentSettings sets the round-robin inputs after Validate. Invalid settings are rejected
// with domain.ErrInvalidRequest and the previous settings stay in force. NewService starts from
// DefaultAssignmentSettings.
func (s *Service) SetAssignmentSettings(a AssignmentSettings) error {
	if err := a.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignment = a
	return nil
}

// assignParamsLocked returns the inputs of one assignment pass, for the loop and the poll-time
// refresh alike: the settings, and the grace state of the lease sweeper (ADR-0017 Section 4.4).
// Every input is a duration or a count, except the grace instants, which are repository time read
// by the sweeper (implementation note 16). The caller holds s.mu.
func (s *Service) assignParamsLocked() storage.AssignParams {
	grace, pending := s.passGraceInput()
	return storage.AssignParams{
		AcceptWindow:  s.assignment.AcceptWindow,
		WorkerTimeout: s.workerTimeout,
		BatchLimit:    s.assignment.BatchLimit,
		MaxLapses:     s.assignment.MaxLapses,
		Grace:         grace,
		GracePending:  pending,
	}
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
//
// Staleness is decided by the repository on its own clock (ADR-0017 implementation note 8): the
// database clock in PostgreSQL. The process clock is read only for the rate limit, which compares
// two process-clock readings.
func (s *Service) refreshIfStaleLocked(ctx context.Context, ar storage.AssignmentRepository) bool {
	interval := s.assignment.Interval
	staleness, ok := ar.(storage.AssignmentPassStaleness)
	if !ok {
		return false
	}
	stale, err := staleness.AssignmentPassStale(ctx, 2*interval)
	if err != nil || !stale {
		return false
	}
	now := s.clock.Now()
	if s.refreshInFlight {
		return false
	}
	if !s.refreshLastAttemptAt.IsZero() && now.Sub(s.refreshLastAttemptAt) < interval {
		return false
	}

	s.refreshInFlight = true
	s.refreshLastAttemptAt = now
	_, err = ar.AssignQueuedJobs(ctx, s.assignParamsLocked())
	s.refreshInFlight = false
	s.refreshLastFailed = err != nil
	if err != nil {
		slog.Warn("assignment pass refresh failed", "error", err)
	}
	return err != nil
}
