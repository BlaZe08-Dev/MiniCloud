package domain

import "fmt"

// IsFailedOutcome reports whether an attempt outcome is a failure: a non-zero exit code or a
// non-empty error summary. Success carries no error class (ADR-0015).
func IsFailedOutcome(o AttemptOutcome) bool {
	return o.ExitCode != 0 || o.Error != ""
}

// NormalizeWorkerErrorClass validates the error class a worker reported with an outcome and
// returns the class to store. A worker may report only PERMANENT, TRANSIENT or UNKNOWN, and only
// with a failure; an omitted class on a failure defaults to UNKNOWN. LEASE_EXPIRED is assigned
// by the coordinator's lease sweeper alone and is rejected here, as is any other value or a class
// on a success (protocol section 6, ADR-0015). The rejected value is never echoed back.
func NormalizeWorkerErrorClass(c ErrorClass, failed bool) (ErrorClass, error) {
	if !failed {
		if c != "" {
			return "", fmt.Errorf("%w: error_classification is only allowed on a failed outcome", ErrInvalidRequest)
		}
		return "", nil
	}
	switch c {
	case "":
		return ClassUnknown, nil
	case ClassPermanent, ClassTransient, ClassUnknown:
		return c, nil
	default:
		return "", fmt.Errorf("%w: error_classification must be one of PERMANENT, TRANSIENT, UNKNOWN", ErrInvalidRequest)
	}
}

// FailureResolution is the decision for a failed attempt: the job's next state, and why.
type FailureResolution struct {
	// JobState is RETRYING, FAILED or CANCELLED.
	JobState JobState
	// Retry is true when the job goes to RETRYING and a backoff delay must be scheduled.
	Retry bool
	// Exhausted is true when a retryable failure found no attempt left in the max_attempts budget.
	Exhausted bool
}

// ResolveFailure decides what a failed attempt does to its job (ADR-0015, ADR-0016). The job's
// state is the state at commit time. A job in CANCEL_REQUESTED always ends CANCELLED: cancel wins
// and never retries. Otherwise a PERMANENT failure ends FAILED at once, and a retryable class
// (TRANSIENT, UNKNOWN) retries while attemptNo < maxAttempts and ends FAILED when the budget is
// used up. LOST attempts are counted by the sweeper under the same budget.
func ResolveFailure(job JobState, class ErrorClass, attemptNo, maxAttempts int) FailureResolution {
	if job == StateCancelRequested {
		return FailureResolution{JobState: StateCancelled}
	}
	if class == ClassPermanent {
		return FailureResolution{JobState: StateFailed}
	}
	if attemptNo < maxAttempts {
		return FailureResolution{JobState: StateRetrying, Retry: true}
	}
	return FailureResolution{JobState: StateFailed, Exhausted: true}
}
