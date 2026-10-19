package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	// MaxSHA256Iterations limits CPU-bound work to 1,000,000 iterations.
	MaxSHA256Iterations = 1000000
)

// SHA256LoopPayload defines the schema for the sha256_loop task.
type SHA256LoopPayload struct {
	Iterations int    `json:"iterations"`
	Input      string `json:"input"`
}

// SHA256LoopTask performs bounded CPU-bound hashing.
type SHA256LoopTask struct{}

func (t *SHA256LoopTask) Name() string {
	return "sha256_loop"
}

func (t *SHA256LoopTask) Validate(payload []byte) error {
	var p SHA256LoopPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("invalid sha256_loop payload: %w", err)
	}
	if p.Iterations <= 0 {
		return fmt.Errorf("iterations must be greater than 0")
	}
	if p.Iterations > MaxSHA256Iterations {
		return fmt.Errorf("iterations %d exceeds maximum limit of %d", p.Iterations, MaxSHA256Iterations)
	}
	return nil
}

func (t *SHA256LoopTask) Execute(ctx context.Context, payload []byte) (string, error) {
	var p SHA256LoopPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}

	current := []byte(p.Input)
	for i := 0; i < p.Iterations; i++ {
		// Check context cancellation every 1024 iterations
		if i%1024 == 0 && ctx.Err() != nil {
			return "", ctx.Err()
		}
		hash := sha256.Sum256(current)
		current = hash[:]
	}

	resultHex := hex.EncodeToString(current)
	out, err := json.Marshal(map[string]any{
		"iterations": p.Iterations,
		"hash":       resultHex,
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}
