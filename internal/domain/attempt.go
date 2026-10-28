package domain

import (
	"time"
)

// Attempt represents a single execution attempt of a job by a worker.
type Attempt struct {
	ID             string       `json:"id"`
	JobID          string       `json:"job_id"`
	AttemptNo      int          `json:"attempt_no"`
	WorkerID       string       `json:"worker_id"`
	LeaseToken     string       `json:"lease_token,omitempty"`
	LeaseTokenHash string       `json:"lease_token_hash,omitempty"`
	State          AttemptState `json:"state"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     *time.Time   `json:"finished_at,omitempty"`
	LeaseExpiresAt *time.Time   `json:"lease_expires_at,omitempty"`
	// ErrorClass is set only by the lease sweeper in slice 4b (LEASE_EXPIRED on a LOST attempt).
	ErrorClass ErrorClass `json:"error_class,omitempty"`
	ExitCode   int        `json:"exit_code"`
	Output     string     `json:"output"`
	Error      string     `json:"error,omitempty"`
}

// AttemptOutcome captures the structured result of an attempt execution.
type AttemptOutcome struct {
	ExitCode            int
	Output              string
	Error               string
	ErrorClassification ErrorClass
}

// ErrorClass classifies a failed attempt for the retry policy (ADR-0015). Success carries
// no class. The values match the chk_attempts_error_class constraint planned for
// migration 000004.
type ErrorClass string

const (
	ClassPermanent    ErrorClass = "PERMANENT"
	ClassTransient    ErrorClass = "TRANSIENT"
	ClassUnknown      ErrorClass = "UNKNOWN"
	ClassLeaseExpired ErrorClass = "LEASE_EXPIRED"
)
