package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// MaxSleepDurationMs limits sleep task to 10 seconds to prevent resource exhaustion.
	MaxSleepDurationMs = 10000
)

// SleepPayload defines the schema for the sleep task.
type SleepPayload struct {
	DurationMs int `json:"duration_ms"`
}

// SleepTask pauses execution for a bounded duration.
type SleepTask struct{}

func (t *SleepTask) Name() string {
	return "sleep"
}

func (t *SleepTask) Validate(payload []byte) error {
	var p SleepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("invalid sleep payload: %w", err)
	}
	if p.DurationMs < 0 {
		return fmt.Errorf("duration_ms cannot be negative")
	}
	if p.DurationMs > MaxSleepDurationMs {
		return fmt.Errorf("duration_ms %d exceeds maximum allowed %d ms", p.DurationMs, MaxSleepDurationMs)
	}
	return nil
}

func (t *SleepTask) Execute(ctx context.Context, payload []byte) (string, error) {
	var p SleepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}

	sleepDur := time.Duration(p.DurationMs) * time.Millisecond
	select {
	case <-time.After(sleepDur):
		out, _ := json.Marshal(map[string]any{
			"slept_ms": p.DurationMs,
		})
		return string(out), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
