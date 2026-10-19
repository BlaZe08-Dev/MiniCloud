package executor

import (
	"context"
	"encoding/json"
	"fmt"
)

// EchoTask returns the provided message or raw payload.
type EchoTask struct{}

func (t *EchoTask) Name() string {
	return "echo"
}

func (t *EchoTask) Validate(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	// Verify that payload is valid JSON if provided
	if !json.Valid(payload) {
		return fmt.Errorf("echo payload must be valid JSON")
	}
	return nil
}

func (t *EchoTask) Execute(ctx context.Context, payload []byte) (string, error) {
	if len(payload) == 0 {
		return `{"echo":""}`, nil
	}
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return string(payload), nil
	}
	res, err := json.Marshal(map[string]any{
		"echo": data,
	})
	if err != nil {
		return "", err
	}
	return string(res), nil
}
