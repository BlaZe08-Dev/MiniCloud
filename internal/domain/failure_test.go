package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeWorkerErrorClass(t *testing.T) {
	tests := []struct {
		name    string
		class   ErrorClass
		failed  bool
		want    ErrorClass
		wantErr bool
	}{
		{"failure with no class defaults to UNKNOWN", "", true, ClassUnknown, false},
		{"failure PERMANENT", ClassPermanent, true, ClassPermanent, false},
		{"failure TRANSIENT", ClassTransient, true, ClassTransient, false},
		{"failure UNKNOWN", ClassUnknown, true, ClassUnknown, false},
		{"LEASE_EXPIRED is coordinator-assigned only", ClassLeaseExpired, true, "", true},
		{"unrecognised value", "BANANA", true, "", true},
		{"lowercase is not accepted", "permanent", true, "", true},
		{"padded value is not accepted", " PERMANENT", true, "", true},
		{"oversized value", ErrorClass(strings.Repeat("A", 100000)), true, "", true},
		{"success carries no class", "", false, "", false},
		{"success with a class is rejected", ClassTransient, false, "", true},
		{"success with LEASE_EXPIRED is rejected", ClassLeaseExpired, false, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeWorkerErrorClass(tc.class, tc.failed)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("expected ErrInvalidRequest, got %v", err)
				}
				if err != nil && len(err.Error()) > 200 {
					t.Fatalf("error message echoes unbounded input (%d bytes)", len(err.Error()))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsFailedOutcome(t *testing.T) {
	if IsFailedOutcome(AttemptOutcome{}) {
		t.Fatal("exit 0 with no error is a success")
	}
	if !IsFailedOutcome(AttemptOutcome{ExitCode: 1}) {
		t.Fatal("non-zero exit code is a failure")
	}
	if !IsFailedOutcome(AttemptOutcome{Error: "boom"}) {
		t.Fatal("an error string is a failure")
	}
}

func TestResolveFailure(t *testing.T) {
	tests := []struct {
		name        string
		job         JobState
		class       ErrorClass
		attemptNo   int
		maxAttempts int
		wantState   JobState
		wantRetry   bool
		wantExhaust bool
	}{
		{"transient with attempts left retries", StateRunning, ClassTransient, 1, 3, StateRetrying, true, false},
		{"unknown with attempts left retries", StateRunning, ClassUnknown, 2, 3, StateRetrying, true, false},
		{"permanent fails at once", StateRunning, ClassPermanent, 1, 3, StateFailed, false, false},
		{"transient on the last attempt fails, exhausted", StateRunning, ClassTransient, 3, 3, StateFailed, false, true},
		{"unknown on the last attempt fails, exhausted", StateRunning, ClassUnknown, 3, 3, StateFailed, false, true},
		{"attempt number beyond max still fails", StateRunning, ClassTransient, 4, 3, StateFailed, false, true},
		{"max_attempts 1 never retries", StateRunning, ClassTransient, 1, 1, StateFailed, false, true},
		{"permanent on the last attempt is not 'exhausted'", StateRunning, ClassPermanent, 3, 3, StateFailed, false, false},
		{"cancel wins over a retryable class", StateCancelRequested, ClassTransient, 1, 3, StateCancelled, false, false},
		{"cancel wins over unknown", StateCancelRequested, ClassUnknown, 1, 3, StateCancelled, false, false},
		{"cancel wins over permanent", StateCancelRequested, ClassPermanent, 1, 3, StateCancelled, false, false},
		{"cancel wins on the last attempt", StateCancelRequested, ClassTransient, 3, 3, StateCancelled, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveFailure(tc.job, tc.class, tc.attemptNo, tc.maxAttempts)
			if got.JobState != tc.wantState || got.Retry != tc.wantRetry || got.Exhausted != tc.wantExhaust {
				t.Fatalf("ResolveFailure(%s,%s,%d,%d) = %+v, want state=%s retry=%v exhausted=%v",
					tc.job, tc.class, tc.attemptNo, tc.maxAttempts, got, tc.wantState, tc.wantRetry, tc.wantExhaust)
			}
			if err := ValidateJobTransition(tc.job, got.JobState); err != nil {
				t.Fatalf("resolution is not an FSM-valid transition: %v", err)
			}
		})
	}
}
