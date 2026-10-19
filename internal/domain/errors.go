package domain

import (
	"errors"
	"fmt"
)

var (
	ErrJobNotFound        = errors.New("job not found")
	ErrAttemptNotFound    = errors.New("attempt not found")
	ErrWorkerNotFound     = errors.New("worker not found")
	ErrStaleAttempt       = errors.New("stale attempt: attempt ID or lease token does not match active attempt")
	ErrUnknownTaskType    = errors.New("unknown or unsupported task type")
	ErrPayloadTooLarge    = errors.New("payload exceeds maximum allowed size")
	ErrOutputTooLarge     = errors.New("output exceeds maximum allowed size")
	ErrTaskTimeout        = errors.New("task execution timed out")
	ErrNoWorkersAvailable = errors.New("no healthy workers with available capacity")
	ErrNoQueuedJobs       = errors.New("no queued jobs available")
	ErrInvalidRequest     = errors.New("invalid request payload or parameters")
	ErrJobAlreadyTerminal = errors.New("job is already in a terminal state")
)

// InvalidStateTransitionError is returned when a requested state transition violates the state machine.
type InvalidStateTransitionError struct {
	From JobState
	To   JobState
}

func (e *InvalidStateTransitionError) Error() string {
	return fmt.Sprintf("invalid job state transition from %s to %s", e.From, e.To)
}

// NewInvalidTransitionError creates a typed InvalidStateTransitionError.
func NewInvalidTransitionError(from, to JobState) error {
	return &InvalidStateTransitionError{From: from, To: to}
}
