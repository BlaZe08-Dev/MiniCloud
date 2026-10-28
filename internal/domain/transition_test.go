package domain_test

import (
	"errors"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func TestJobTransitionMatrix_AllPairs(t *testing.T) {
	allStates := domain.AllJobStates()

	// expectedValid defines the exact set of valid transitions per domain specification.
	expectedValid := map[domain.JobState]map[domain.JobState]bool{
		domain.StateSubmitted: {
			domain.StateQueued:   true,
			domain.StateRejected: true,
		},
		domain.StateQueued: {
			domain.StateRunning:   true,
			domain.StateCancelled: true,
		},
		domain.StateRunning: {
			domain.StateSucceeded:       true,
			domain.StateRetrying:        true,
			domain.StateFailed:          true,
			domain.StateCancelRequested: true,
		},
		domain.StateCancelRequested: {
			domain.StateCancelled: true,
			domain.StateSucceeded: true,
			domain.StateFailed:    true,
		},
		domain.StateRetrying: {
			domain.StateRunning:   true,
			domain.StateFailed:    true,
			domain.StateCancelled: true,
		},
		domain.StateSucceeded: {},
		domain.StateFailed:    {},
		domain.StateCancelled: {},
		domain.StateRejected:  {},
	}

	for _, from := range allStates {
		for _, to := range allStates {
			fromState := from
			toState := to
			shouldBeValid := expectedValid[fromState][toState]

			t.Run(string(fromState)+"->"+string(toState), func(t *testing.T) {
				err := domain.ValidateJobTransition(fromState, toState)
				isValid := domain.IsValidJobTransition(fromState, toState)

				if shouldBeValid {
					if err != nil {
						t.Errorf("expected transition %s -> %s to be valid, but got error: %v", fromState, toState, err)
					}
					if !isValid {
						t.Errorf("expected IsValidJobTransition(%s, %s) to be true", fromState, toState)
					}
				} else {
					if err == nil {
						t.Errorf("expected transition %s -> %s to be rejected, but got nil error", fromState, toState)
					}
					var targetErr *domain.InvalidStateTransitionError
					if !errors.As(err, &targetErr) {
						t.Errorf("expected error to be *domain.InvalidStateTransitionError, got %T", err)
					}
					if isValid {
						t.Errorf("expected IsValidJobTransition(%s, %s) to be false", fromState, toState)
					}
				}
			})
		}
	}
}

func TestJob_TransitionTo(t *testing.T) {
	job := &domain.Job{
		ID:    "job_test_1",
		State: domain.StateSubmitted,
	}

	// Valid transition
	if err := job.TransitionTo(domain.StateQueued); err != nil {
		t.Fatalf("expected valid transition from SUBMITTED to QUEUED, got: %v", err)
	}
	if job.State != domain.StateQueued {
		t.Fatalf("expected job state to be QUEUED, got: %s", job.State)
	}

	// Invalid transition directly to SUCCEEDED
	err := job.TransitionTo(domain.StateSucceeded)
	if err == nil {
		t.Fatalf("expected invalid transition error from QUEUED to SUCCEEDED")
	}
	if job.State != domain.StateQueued {
		t.Fatalf("job state mutated despite invalid transition: %s", job.State)
	}
}

func TestJobState_IsTerminal(t *testing.T) {
	terminals := map[domain.JobState]bool{
		domain.StateSucceeded: true,
		domain.StateFailed:    true,
		domain.StateCancelled: true,
		domain.StateRejected:  true,
	}

	for _, s := range domain.AllJobStates() {
		expected := terminals[s]
		if actual := s.IsTerminal(); actual != expected {
			t.Errorf("state %s IsTerminal() expected %v, got %v", s, expected, actual)
		}
	}
}
