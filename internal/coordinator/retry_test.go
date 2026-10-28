package coordinator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// retryFixture is a coordinator over the in-memory repository with one worker, one job of
// maxAttempts attempts and a fake clock shared by the service and the repository.
type retryFixture struct {
	t    *testing.T
	svc  *coordinator.Service
	repo storage.Repository
	clk  *mockClock
	job  *domain.Job
}

func newRetryFixture(t *testing.T, maxAttempts int) *retryFixture {
	t.Helper()
	clk := newMockClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	repo := storage.NewInMemoryRepository(storage.WithClock(clk))
	svc := newDispatchCoordinator(t, repo, "w1", 1)
	svc.SetClock(clk)
	job, err := svc.SubmitJob(context.Background(), "echo", []byte(`{"message":"x"}`), 30, maxAttempts)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	return &retryFixture{t: t, svc: svc, repo: repo, clk: clk, job: job}
}

// poll claims the next attempt for w1.
func (f *retryFixture) poll() *coordinator.WorkAssignment {
	f.t.Helper()
	asg, err := f.svc.PollWork(context.Background(), "w1")
	if err != nil {
		f.t.Fatalf("PollWork failed: %v", err)
	}
	return asg
}

// fail commits a failed outcome of the given class for asg.
func (f *retryFixture) fail(asg *coordinator.WorkAssignment, class string) (*domain.Job, error) {
	f.t.Helper()
	return f.svc.CommitResult(context.Background(), asg.JobID, asg.AttemptID, asg.LeaseToken, "w1", 1, "", "boom", class)
}

func (f *retryFixture) notBefore() time.Time {
	f.t.Helper()
	j, err := f.repo.GetJob(context.Background(), f.job.ID)
	if err != nil || j.NotBefore == nil {
		f.t.Fatalf("GetJob = %+v, %v; want a not_before", j, err)
	}
	return *j.NotBefore
}

// TestCoordinator_RetryBackoffWithJitter: a retryable failure schedules the retry at exactly the
// delay domain.ComputeBackoff gives for the failed attempt number, with the jitter drawn from the
// service's random source, and the job is not dispatchable before then. With the default random
// source the delay stays inside [0.5, 1.5] * base for the first retry.
func TestCoordinator_RetryBackoffWithJitter(t *testing.T) {
	ctx := context.Background()
	policy := domain.DefaultRetryPolicy()

	t.Run("deterministic_jitter_follows_ComputeBackoff_per_attempt", func(t *testing.T) {
		f := newRetryFixture(t, 4)
		draws := []float64{0.0, 1.0, 0.5}
		next := 0
		f.svc.SetSweepRandom(func() float64 { v := draws[next%len(draws)]; next++; return v })

		for n := 1; n <= 3; n++ {
			start := f.clk.Now()
			asg := f.poll()
			if asg.AttemptNo != n {
				t.Fatalf("attempt_no = %d, want %d", asg.AttemptNo, n)
			}
			job, err := f.fail(asg, "TRANSIENT")
			if err != nil || job.State != domain.StateRetrying {
				t.Fatalf("fail #%d = %+v, %v; want RETRYING", n, job, err)
			}
			want := domain.ComputeBackoff(policy, n, func() float64 { return draws[n-1] })
			if got := f.notBefore().Sub(start); got != want {
				t.Fatalf("attempt %d: not_before - now = %v, want ComputeBackoff = %v", n, got, want)
			}
			// Not dispatchable one nanosecond early, dispatchable at not_before.
			f.clk.Advance(want - time.Nanosecond)
			if _, err := f.svc.PollWork(ctx, "w1"); !errors.Is(err, domain.ErrNoQueuedJobs) {
				t.Fatalf("attempt %d: a retry inside its backoff must not dispatch, got %v", n, err)
			}
			f.clk.Advance(time.Nanosecond)
		}
		if next != 3 {
			t.Fatalf("expected exactly one jitter draw per scheduled retry, got %d", next)
		}
	})

	t.Run("default_random_source_stays_inside_the_jitter_range", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			f := newRetryFixture(t, 3)
			start := f.clk.Now()
			if _, err := f.fail(f.poll(), "TRANSIENT"); err != nil {
				t.Fatalf("fail failed: %v", err)
			}
			d := f.notBefore().Sub(start)
			if d < policy.BaseBackoff/2 || d > policy.BaseBackoff*3/2 {
				t.Fatalf("first retry delay %v outside [0.5s, 1.5s]", d)
			}
		}
	})
}

// TestCoordinator_CommitResult_FailureClassification: the class a worker reports decides retry or
// failure, an omitted class means UNKNOWN (retryable), and the coordinator rejects classes a worker
// may not send before anything changes.
func TestCoordinator_CommitResult_FailureClassification(t *testing.T) {
	t.Run("PERMANENT_fails_without_retry", func(t *testing.T) {
		f := newRetryFixture(t, 3)
		job, err := f.fail(f.poll(), "PERMANENT")
		if err != nil || job.State != domain.StateFailed {
			t.Fatalf("job = %+v, %v; want FAILED", job, err)
		}
		requireAttempts(t, f.repo, f.job.ID, 1)
	})

	t.Run("omitted_class_is_UNKNOWN_and_retries", func(t *testing.T) {
		f := newRetryFixture(t, 3)
		asg := f.poll()
		job, err := f.fail(asg, "")
		if err != nil || job.State != domain.StateRetrying {
			t.Fatalf("job = %+v, %v; want RETRYING", job, err)
		}
		att, _ := f.repo.GetAttempt(context.Background(), asg.AttemptID)
		if att.ErrorClass != domain.ClassUnknown {
			t.Fatalf("stored class = %q, want UNKNOWN", att.ErrorClass)
		}
	})

	t.Run("budget_is_bounded_by_max_attempts", func(t *testing.T) {
		f := newRetryFixture(t, 2)
		f.svc.SetSweepRandom(func() float64 { return 0 })
		if job, err := f.fail(f.poll(), "TRANSIENT"); err != nil || job.State != domain.StateRetrying {
			t.Fatalf("first failure = %+v, %v; want RETRYING", job, err)
		}
		f.clk.Advance(time.Minute)
		job, err := f.fail(f.poll(), "TRANSIENT")
		if err != nil || job.State != domain.StateFailed {
			t.Fatalf("second failure = %+v, %v; want FAILED", job, err)
		}
		requireAttempts(t, f.repo, f.job.ID, 2)
	})

	t.Run("rejected_classes_change_nothing", func(t *testing.T) {
		f := newRetryFixture(t, 3)
		asg := f.poll()
		for _, bad := range []string{"LEASE_EXPIRED", "BANANA", "permanent", strings.Repeat("X", 70000)} {
			if _, err := f.fail(asg, bad); !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("class %.20q: expected ErrInvalidRequest, got %v", bad, err)
			}
		}
		// A class on a success is rejected as well.
		if _, err := f.svc.CommitResult(context.Background(), asg.JobID, asg.AttemptID, asg.LeaseToken, "w1", 0, "ok", "", "TRANSIENT"); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("class on success: expected ErrInvalidRequest, got %v", err)
		}
		requireJobState(t, f.repo, f.job.ID, domain.StateRunning)
		att, _ := f.repo.GetAttempt(context.Background(), asg.AttemptID)
		if att.State != domain.AttemptRunning || att.ErrorClass != "" {
			t.Fatalf("attempt changed by rejected commits: %+v", att)
		}
	})
}

// TestCoordinator_CommitResult_CancelWinsOverRetry: a failure for a CANCEL_REQUESTED job ends the
// job CANCELLED through the service, with no retry scheduled.
func TestCoordinator_CommitResult_CancelWinsOverRetry(t *testing.T) {
	f := newRetryFixture(t, 3)
	asg := f.poll()
	if _, err := f.svc.CancelJob(context.Background(), f.job.ID); err != nil {
		t.Fatalf("CancelJob failed: %v", err)
	}
	job, err := f.fail(asg, "TRANSIENT")
	if err != nil || job.State != domain.StateCancelled {
		t.Fatalf("job = %+v, %v; want CANCELLED", job, err)
	}
	f.clk.Advance(time.Hour)
	if _, err := f.svc.PollWork(context.Background(), "w1"); !errors.Is(err, domain.ErrNoQueuedJobs) {
		t.Fatalf("a cancelled job must never be re-dispatched, got %v", err)
	}
	requireAttempts(t, f.repo, f.job.ID, 1)
}

// TestCoordinator_DefaultMaxAttempts_Configurable: the service default (MINICLOUD_DEFAULT_MAX_ATTEMPTS)
// is configurable within the 1..20 range and out-of-range values are refused, leaving the current
// default in place.
func TestCoordinator_DefaultMaxAttempts_Configurable(t *testing.T) {
	f := newRetryFixture(t, 3)
	if got := f.svc.DefaultMaxAttempts(); got != coordinator.DefaultMaxAttempts {
		t.Fatalf("initial default = %d, want %d", got, coordinator.DefaultMaxAttempts)
	}
	if err := f.svc.SetDefaultMaxAttempts(7); err != nil {
		t.Fatalf("SetDefaultMaxAttempts(7) failed: %v", err)
	}
	if got := f.svc.DefaultMaxAttempts(); got != 7 {
		t.Fatalf("default = %d, want 7", got)
	}
	for _, bad := range []int{0, -1, 21, 1000} {
		if err := f.svc.SetDefaultMaxAttempts(bad); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("SetDefaultMaxAttempts(%d): expected ErrInvalidRequest, got %v", bad, err)
		}
	}
	if got := f.svc.DefaultMaxAttempts(); got != 7 {
		t.Fatalf("a rejected value changed the default to %d", got)
	}
}

// TestCoordinator_TransientFailureThenSuccess: a transient failure is retried after its backoff and
// the second attempt's success completes the job, with both attempts recorded.
func TestCoordinator_TransientFailureThenSuccess(t *testing.T) {
	f := newRetryFixture(t, 3)
	if _, err := f.fail(f.poll(), "TRANSIENT"); err != nil {
		t.Fatalf("fail failed: %v", err)
	}
	f.clk.Advance(time.Minute)
	asg := f.poll()
	if asg.AttemptNo != 2 {
		t.Fatalf("attempt_no = %d, want 2", asg.AttemptNo)
	}
	job, err := f.svc.CommitResult(context.Background(), asg.JobID, asg.AttemptID, asg.LeaseToken, "w1", 0, "ok", "", "")
	if err != nil || job.State != domain.StateSucceeded || job.Result == nil || job.Result.Output != "ok" {
		t.Fatalf("job = %+v, %v; want SUCCEEDED with the second attempt's result", job, err)
	}
	atts := requireAttempts(t, f.repo, f.job.ID, 2)
	if atts[0].State != domain.AttemptFailed && atts[1].State != domain.AttemptFailed {
		t.Fatalf("attempts = %+v, want one FAILED and one COMPLETED", atts)
	}
}
