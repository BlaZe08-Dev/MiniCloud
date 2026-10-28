package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func att(no int, st domain.AttemptState, class domain.ErrorClass, msg string) *domain.Attempt {
	return &domain.Attempt{AttemptNo: no, State: st, ErrorClass: class, Error: msg}
}

func TestFailureSummary(t *testing.T) {
	nb := time.Date(2026, 1, 1, 12, 0, 5, 0, time.UTC)
	tests := []struct {
		name     string
		job      domain.Job
		attempts []*domain.Attempt
		want     []string // substrings, each must appear
		wantNone bool
	}{
		{
			name: "sweeper failed job with no results row",
			job:  domain.Job{State: domain.StateFailed, MaxAttempts: 2},
			attempts: []*domain.Attempt{
				att(1, domain.AttemptLost, domain.ClassLeaseExpired, "lease expired: worker failed to renew"),
				att(2, domain.AttemptLost, domain.ClassLeaseExpired, "lease expired: worker failed to renew"),
			},
			want: []string{"FAILED", "2 of 2 attempts", "LEASE_EXPIRED", "lease expired: worker failed to renew"},
		},
		{
			name:     "permanent worker failure stops after one attempt",
			job:      domain.Job{State: domain.StateFailed, MaxAttempts: 3, Result: &domain.Result{ExitCode: 1, Error: "bad payload"}},
			attempts: []*domain.Attempt{att(1, domain.AttemptFailed, domain.ClassPermanent, "bad payload")},
			want:     []string{"FAILED", "1 of 3 attempts", "PERMANENT", "not retried"},
		},
		{
			name:     "retrying shows the next retry time",
			job:      domain.Job{State: domain.StateRetrying, MaxAttempts: 3, NotBefore: &nb},
			attempts: []*domain.Attempt{att(1, domain.AttemptFailed, domain.ClassTransient, "task execution timed out")},
			want:     []string{"RETRYING", "1 of 3 attempts", "TRANSIENT", "12:00:05"},
		},
		{
			name:     "cancelled after the worker reported a failure",
			job:      domain.Job{State: domain.StateCancelled, MaxAttempts: 3},
			attempts: []*domain.Attempt{att(1, domain.AttemptFailed, domain.ClassUnknown, "context canceled")},
			want:     []string{"CANCELLED", "cancel was requested", "UNKNOWN", "context canceled"},
		},
		{
			name:     "cancelled by the sweeper",
			job:      domain.Job{State: domain.StateCancelled, MaxAttempts: 3},
			attempts: []*domain.Attempt{att(1, domain.AttemptCancelled, "", "")},
			want:     []string{"CANCELLED", "cancel was requested", "did not report"},
		},
		{
			name: "no attempts yet",
			job:  domain.Job{State: domain.StateFailed, MaxAttempts: 3},
			want: []string{"FAILED", "0 of 3 attempts"},
		},
		{name: "succeeded job has no failure section", job: domain.Job{State: domain.StateSucceeded, MaxAttempts: 3}, wantNone: true},
		{name: "running job has no failure section", job: domain.Job{State: domain.StateRunning, MaxAttempts: 3}, wantNone: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := tc.job
			got := strings.Join(failureSummary(&j, tc.attempts), "\n")
			if tc.wantNone {
				if got != "" {
					t.Fatalf("expected no summary, got %q", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("summary %q lacks %q", got, w)
				}
			}
		})
	}
}
