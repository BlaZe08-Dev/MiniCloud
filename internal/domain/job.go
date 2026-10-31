package domain

import (
	"encoding/json"
	"time"
)

// Result represents the committed outcome of a job execution.
type Result struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

// Job represents a unit of computation submitted to MiniCloud.
type Job struct {
	ID                string          `json:"id"`
	TaskType          string          `json:"task_type"`
	Payload           json.RawMessage `json:"payload"`
	State             JobState        `json:"state"`
	Priority          int             `json:"priority"`
	MaxAttempts       int             `json:"max_attempts"`
	TimeoutSeconds    int             `json:"timeout_seconds"`
	CurrentAttemptID  string          `json:"current_attempt_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	NotBefore         *time.Time      `json:"not_before,omitempty"`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at,omitempty"`
	Result            *Result         `json:"result,omitempty"`

	// Assignment (ADR-0017 Section 1.1). A reservation is all-or-nothing: the three assignment
	// times are set together and only while the job is QUEUED or RETRYING. The wire names are the
	// ones Section 5.1 lists; AssignedAt is internal and is not part of the job view.
	AssignedWorkerID         *string    `json:"assigned_worker_id"`
	AssignedAt               *time.Time `json:"-"`
	AssignmentExpiresAt      *time.Time `json:"assignment_expires_at"`
	AssignmentLapses         int        `json:"assignment_lapses"`
	AssignmentFallbackReason *string    `json:"assignment_fallback_reason"`
}

// TransitionTo validates and applies a state transition to the job.
func (j *Job) TransitionTo(target JobState) error {
	if err := ValidateJobTransition(j.State, target); err != nil {
		return err
	}
	j.State = target
	j.UpdatedAt = time.Now().UTC()
	return nil
}
