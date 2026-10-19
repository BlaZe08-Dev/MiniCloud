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
	ID               string          `json:"id"`
	TaskType         string          `json:"task_type"`
	Payload          json.RawMessage `json:"payload"`
	State            JobState        `json:"state"`
	Priority         int             `json:"priority"`
	MaxAttempts      int             `json:"max_attempts"`
	TimeoutSeconds   int             `json:"timeout_seconds"`
	CurrentAttemptID string          `json:"current_attempt_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Result           *Result         `json:"result,omitempty"`
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
