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
	ExitCode       int          `json:"exit_code"`
	Output         string       `json:"output"`
	Error          string       `json:"error,omitempty"`
}

// AttemptOutcome captures the structured result of an attempt execution.
type AttemptOutcome struct {
	ExitCode            int
	Output              string
	Error               string
	ErrorClassification string // placeholder for future classification
}
