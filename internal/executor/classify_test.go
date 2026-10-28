package executor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

// TestExecutor_AllowlistValidation_PermanentFailure: every validation-style failure of an
// allowlisted task is classified PERMANENT (ADR-0015 section 1): repeating the same payload
// reproduces it, so retrying only wastes capacity.
func TestExecutor_AllowlistValidation_PermanentFailure(t *testing.T) {
	reg := executor.NewRegistry()
	big := strings.Repeat("a", executor.MaxPayloadSizeBytes+1)
	tests := []struct {
		name    string
		task    string
		payload string
	}{
		{"sleep negative duration", "sleep", `{"duration_ms":-5}`},
		{"sleep duration over the maximum", "sleep", fmt.Sprintf(`{"duration_ms":%d}`, executor.MaxSleepDurationMs+1)},
		{"sleep malformed JSON", "sleep", `{"duration_ms":`},
		{"sleep wrong type", "sleep", `{"duration_ms":"ten"}`},
		{"sha256_loop zero iterations", "sha256_loop", `{"iterations":0,"input":"x"}`},
		{"sha256_loop negative iterations", "sha256_loop", `{"iterations":-1,"input":"x"}`},
		{"sha256_loop iterations over the maximum", "sha256_loop", fmt.Sprintf(`{"iterations":%d,"input":"x"}`, executor.MaxSHA256Iterations+1)},
		{"sha256_loop unparseable payload", "sha256_loop", `not json`},
		{"echo invalid JSON", "echo", `{"message": oops`},
		{"unknown task type", "rm_rf", `{}`},
		{"oversized payload", "echo", `"` + big + `"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, exit, err := reg.Execute(context.Background(), tc.task, []byte(tc.payload), time.Second)
			if err == nil || exit == 0 {
				t.Fatalf("expected a failure, got exit=%d err=%v", exit, err)
			}
			if got := executor.Classify(err); got != domain.ClassPermanent {
				t.Fatalf("Classify(%v) = %q, want PERMANENT", err, got)
			}
		})
	}
}

// TestExecutor_Classify pins the class of every other outcome: a deadline is TRANSIENT (the work
// may fit another time or on another worker), a cancellation or anything unrecognised is UNKNOWN
// (retryable by default), and success carries no class.
func TestExecutor_Classify(t *testing.T) {
	reg := executor.NewRegistry()
	_, _, timeoutErr := reg.Execute(context.Background(), "sleep", []byte(`{"duration_ms":5000}`), 20*time.Millisecond)
	if !errors.Is(timeoutErr, domain.ErrTaskTimeout) {
		t.Fatalf("setup: expected ErrTaskTimeout, got %v", timeoutErr)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, cancelErr := reg.Execute(cancelled, "sleep", []byte(`{"duration_ms":5000}`), time.Minute)
	if !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("setup: expected context.Canceled, got %v", cancelErr)
	}

	tests := []struct {
		name string
		err  error
		want domain.ErrorClass
	}{
		{"success has no class", nil, ""},
		{"task timeout", timeoutErr, domain.ClassTransient},
		{"wrapped task timeout", fmt.Errorf("run: %w", domain.ErrTaskTimeout), domain.ClassTransient},
		{"context deadline", context.DeadlineExceeded, domain.ClassTransient},
		{"cooperative cancellation", cancelErr, domain.ClassUnknown},
		{"unrecognised error", errors.New("kernel said no"), domain.ClassUnknown},
		{"wrapped validation error", fmt.Errorf("x: %w", domain.ErrInvalidRequest), domain.ClassPermanent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := executor.Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
