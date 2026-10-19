package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

// LargeOutputTask is a mock task designed to test output size capping.
type LargeOutputTask struct{}

func (t *LargeOutputTask) Name() string                  { return "large_output" }
func (t *LargeOutputTask) Validate(payload []byte) error { return nil }
func (t *LargeOutputTask) Execute(ctx context.Context, payload []byte) (string, error) {
	return strings.Repeat("A", 100*1024), nil // 100 KiB
}

func TestExecutor_EchoTask(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	payload := []byte(`{"message":"test-echo"}`)
	out, exitCode, err := reg.Execute(ctx, "echo", payload, 2*time.Second)
	if err != nil {
		t.Fatalf("echo execution failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("failed to parse echo output JSON: %v", err)
	}
	if res["echo"] == nil {
		t.Fatalf("expected echo field in response, got %s", out)
	}
}

func TestExecutor_SleepTask_Success(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	payload := []byte(`{"duration_ms":10}`)
	out, exitCode, err := reg.Execute(ctx, "sleep", payload, 2*time.Second)
	if err != nil {
		t.Fatalf("sleep execution failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(out, `"slept_ms":10`) {
		t.Fatalf("unexpected sleep output: %s", out)
	}
}

func TestExecutor_SHA256LoopTask_Success(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	payload := []byte(`{"iterations":100,"input":"minicloud"}`)
	out, exitCode, err := reg.Execute(ctx, "sha256_loop", payload, 5*time.Second)
	if err != nil {
		t.Fatalf("sha256_loop execution failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(out, `"hash":`) {
		t.Fatalf("expected hash in output: %s", out)
	}
}

func TestExecutor_UnknownTaskType(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	_, exitCode, err := reg.Execute(ctx, "bash_exec", []byte(`{"cmd":"ls"}`), 2*time.Second)
	if err == nil {
		t.Fatalf("expected error for unknown task type, got nil")
	}
	if !errors.Is(err, domain.ErrUnknownTaskType) {
		t.Fatalf("expected ErrUnknownTaskType, got %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestExecutor_PayloadSizeLimit(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	oversizedPayload := make([]byte, executor.MaxPayloadSizeBytes+1)
	_, exitCode, err := reg.Execute(ctx, "echo", oversizedPayload, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error for oversized payload, got nil")
	}
	if !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestExecutor_TimeoutEnforcement(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	// Sleep 500ms with a 50ms timeout
	payload := []byte(`{"duration_ms":500}`)
	_, exitCode, err := reg.Execute(ctx, "sleep", payload, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, domain.ErrTaskTimeout) {
		t.Fatalf("expected ErrTaskTimeout, got %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestExecutor_OutputCapped(t *testing.T) {
	reg := executor.NewRegistry()
	reg.Register(&LargeOutputTask{})
	ctx := context.Background()

	out, exitCode, err := reg.Execute(ctx, "large_output", []byte(`{}`), 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if len(out) != executor.MaxOutputSizeBytes {
		t.Fatalf("expected output length capped at %d, got %d", executor.MaxOutputSizeBytes, len(out))
	}
}

func TestExecutor_ValidationBounds(t *testing.T) {
	reg := executor.NewRegistry()
	ctx := context.Background()

	// Excessive sleep duration > 10,000 ms
	excessSleep := []byte(`{"duration_ms":20000}`)
	_, _, err := reg.Execute(ctx, "sleep", excessSleep, 2*time.Second)
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for excess sleep duration, got %v", err)
	}

	// Negative sleep duration
	negSleep := []byte(`{"duration_ms":-5}`)
	_, _, err = reg.Execute(ctx, "sleep", negSleep, 2*time.Second)
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for negative sleep duration, got %v", err)
	}

	// Excess SHA256 iterations > 1,000,000
	excessSHA := []byte(`{"iterations":2000000,"input":"foo"}`)
	_, _, err = reg.Execute(ctx, "sha256_loop", excessSHA, 2*time.Second)
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for excess iterations, got %v", err)
	}
}
