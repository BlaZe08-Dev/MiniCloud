package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func (h *renewHarness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := http.Get(h.ts.URL + path)
	if err != nil {
		h.t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (h *renewHarness) job(jobID string) domain.Job {
	h.t.Helper()
	code, body := h.get("/v1/jobs/" + jobID)
	if code != http.StatusOK {
		h.t.Fatalf("GET job: %d %s", code, body)
	}
	var j domain.Job
	if err := json.Unmarshal([]byte(body), &j); err != nil {
		h.t.Fatalf("decode job: %v", err)
	}
	return j
}

func (h *renewHarness) attempts(jobID string) []*domain.Attempt {
	h.t.Helper()
	code, body := h.get("/v1/jobs/" + jobID + "/attempts")
	if code != http.StatusOK {
		h.t.Fatalf("GET attempts: %d %s", code, body)
	}
	var res struct {
		Attempts []*domain.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		h.t.Fatalf("decode attempts: %v", err)
	}
	return res.Attempts
}

func (h *renewHarness) poll() coordinator.WorkAssignment {
	h.t.Helper()
	code, body := h.post("/v1/workers/worker-1/poll", ``)
	if code != http.StatusOK {
		h.t.Fatalf("poll: %d %s", code, body)
	}
	var res struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	_ = json.Unmarshal([]byte(body), &res)
	return res.Assignment
}

func resultPath(jobID, attemptID string) string {
	return "/v1/jobs/" + jobID + "/attempts/" + attemptID + "/result"
}

func failureBody(token, class string) string {
	extra := ""
	if class != "" {
		extra = fmt.Sprintf(`,"error_classification":%q`, class)
	}
	return fmt.Sprintf(`{"lease_token":%q,"worker_id":"worker-1","exit_code":1,"output":"","error":"boom"%s}`, token, extra)
}

// TestAPI_ReportResult_ErrorClassification: POST /result accepts only PERMANENT, TRANSIENT and
// UNKNOWN (docs/protocol.md section 6), rejects LEASE_EXPIRED and anything else with 400
// INVALID_REQUEST without changing the job, and resolves the accepted class end to end.
func TestAPI_ReportResult_ErrorClassification(t *testing.T) {
	h := newRenewHarness(t)
	jobID, a := h.start(3)
	path := resultPath(jobID, a.AttemptID)

	for _, bad := range []string{"LEASE_EXPIRED", "BANANA", "transient"} {
		code, body := h.post(path, failureBody(a.LeaseToken, bad))
		if code != http.StatusBadRequest || errorCode(t, body) != "INVALID_REQUEST" {
			t.Fatalf("class %q: %d %s, want 400 INVALID_REQUEST", bad, code, body)
		}
	}
	if code, body := h.post(path, fmt.Sprintf(`{"lease_token":%q,"worker_id":"worker-1","exit_code":0,"output":"ok","error_classification":"TRANSIENT"}`, a.LeaseToken)); code != http.StatusBadRequest {
		t.Fatalf("class on a success: %d %s, want 400", code, body)
	}
	if j := h.job(jobID); j.State != domain.StateRunning {
		t.Fatalf("rejected results must leave the job RUNNING, got %s", j.State)
	}

	// TRANSIENT with attempts left: 200 COMMITTED, job RETRYING (no result yet).
	code, body := h.post(path, failureBody(a.LeaseToken, "TRANSIENT"))
	if code != http.StatusOK || !strings.Contains(body, `"job_state":"RETRYING"`) {
		t.Fatalf("TRANSIENT: %d %s, want 200 with job_state RETRYING", code, body)
	}
	if j := h.job(jobID); j.State != domain.StateRetrying || j.Result != nil {
		t.Fatalf("job = %+v, want RETRYING without result", j)
	}

	// Inside the backoff nothing is dispatched; after it, attempt 2 runs and fails PERMANENT.
	if code, _ := h.post("/v1/workers/worker-1/poll", ``); code != http.StatusNoContent {
		t.Fatalf("poll inside the backoff: %d, want 204", code)
	}
	h.clock.Advance(time.Minute)
	b := h.poll()
	if b.JobID != jobID || b.AttemptNo != 2 {
		t.Fatalf("second assignment = %+v, want attempt 2 of %s", b, jobID)
	}
	code, body = h.post(resultPath(jobID, b.AttemptID), failureBody(b.LeaseToken, "PERMANENT"))
	if code != http.StatusOK || !strings.Contains(body, `"job_state":"FAILED"`) {
		t.Fatalf("PERMANENT: %d %s, want 200 with job_state FAILED", code, body)
	}
	j := h.job(jobID)
	if j.State != domain.StateFailed || j.Result == nil || j.Result.Error != "boom" {
		t.Fatalf("job = %+v, want FAILED with the worker's error", j)
	}
	atts := h.attempts(jobID)
	if len(atts) != 2 || atts[0].ErrorClass != domain.ClassTransient || atts[1].ErrorClass != domain.ClassPermanent ||
		atts[0].State != domain.AttemptFailed || atts[1].State != domain.AttemptFailed {
		t.Fatalf("attempts = %+v, want two FAILED attempts classed TRANSIENT then PERMANENT", atts)
	}
}

// TestAPI_SubmitJob_DefaultMaxAttempts: a job submitted without max_attempts gets the configured
// default (MINICLOUD_DEFAULT_MAX_ATTEMPTS), an explicit value overrides it, and out-of-range values
// are still 400.
func TestAPI_SubmitJob_DefaultMaxAttempts(t *testing.T) {
	h := newRenewHarness(t)
	if err := h.svc.SetDefaultMaxAttempts(5); err != nil {
		t.Fatalf("SetDefaultMaxAttempts failed: %v", err)
	}
	submit := func(body string) (int, domain.Job) {
		t.Helper()
		code, raw := h.post("/v1/jobs", body)
		var j domain.Job
		_ = json.Unmarshal([]byte(raw), &j)
		return code, j
	}
	if code, j := submit(`{"task_type":"echo","payload":{"message":"x"}}`); code != http.StatusCreated || j.MaxAttempts != 5 {
		t.Fatalf("omitted max_attempts: %d, max_attempts=%d, want 201 with 5", code, j.MaxAttempts)
	}
	if code, j := submit(`{"task_type":"echo","payload":{"message":"x"},"max_attempts":null}`); code != http.StatusCreated || j.MaxAttempts != 5 {
		t.Fatalf("null max_attempts: %d, max_attempts=%d, want 201 with 5", code, j.MaxAttempts)
	}
	if code, j := submit(`{"task_type":"echo","payload":{"message":"x"},"max_attempts":2}`); code != http.StatusCreated || j.MaxAttempts != 2 {
		t.Fatalf("explicit max_attempts: %d, max_attempts=%d, want 201 with 2", code, j.MaxAttempts)
	}
	if code, _ := submit(`{"task_type":"echo","payload":{"message":"x"},"max_attempts":21}`); code != http.StatusBadRequest {
		t.Fatalf("max_attempts 21: %d, want 400", code)
	}
}

// TestAPI_CancelJob_RunningRaces resolves cancel against the worker's result deterministically
// (ADR-0016 section 5, race 1): a failure after a cancel request ends CANCELLED and is never
// retried, a success after a cancel request still SUCCEEDS, a cancel after the result is 409, and
// duplicate cancels in between change nothing.
func TestAPI_CancelJob_RunningRaces(t *testing.T) {
	cancel := func(h *renewHarness, jobID string) (int, string) {
		t.Helper()
		return h.post("/v1/jobs/"+jobID+"/cancel", `{}`)
	}

	t.Run("failure_after_cancel_request_yields_CANCELLED", func(t *testing.T) {
		h := newRenewHarness(t)
		jobID, a := h.start(3)
		first, firstBody := cancel(h, jobID)
		if first != http.StatusOK || !strings.Contains(firstBody, `"state":"CANCEL_REQUESTED"`) {
			t.Fatalf("cancel: %d %s", first, firstBody)
		}
		if code, body := cancel(h, jobID); code != http.StatusOK || body != firstBody {
			t.Fatalf("duplicate cancel: %d %s, want the identical 200", code, body)
		}
		code, body := h.post(resultPath(jobID, a.AttemptID), failureBody(a.LeaseToken, "TRANSIENT"))
		if code != http.StatusOK || !strings.Contains(body, `"job_state":"CANCELLED"`) {
			t.Fatalf("failure on CANCEL_REQUESTED: %d %s, want 200 with job_state CANCELLED", code, body)
		}
		if j := h.job(jobID); j.State != domain.StateCancelled || j.NotBefore != nil {
			t.Fatalf("job = %+v, want CANCELLED without not_before", j)
		}
		h.clock.Advance(time.Hour)
		if code, _ := h.post("/v1/workers/worker-1/poll", ``); code != http.StatusNoContent {
			t.Fatalf("a cancelled job must never be re-dispatched, poll = %d", code)
		}
		if code, body := cancel(h, jobID); code != http.StatusConflict || errorCode(t, body) != "JOB_ALREADY_TERMINAL" {
			t.Fatalf("cancel after the outcome: %d %s, want 409 JOB_ALREADY_TERMINAL", code, body)
		}
		if atts := h.attempts(jobID); len(atts) != 1 || atts[0].State != domain.AttemptFailed {
			t.Fatalf("attempts = %+v, want the one attempt FAILED", atts)
		}
	})

	t.Run("success_after_cancel_request_still_succeeds", func(t *testing.T) {
		h := newRenewHarness(t)
		jobID, a := h.start(3)
		cancel(h, jobID)
		code, body := h.post(resultPath(jobID, a.AttemptID), fmt.Sprintf(`{"lease_token":%q,"worker_id":"worker-1","exit_code":0,"output":"done"}`, a.LeaseToken))
		if code != http.StatusOK || !strings.Contains(body, `"job_state":"SUCCEEDED"`) {
			t.Fatalf("success on CANCEL_REQUESTED: %d %s, want 200 SUCCEEDED", code, body)
		}
	})

	t.Run("cancel_after_a_failed_job_is_409", func(t *testing.T) {
		h := newRenewHarness(t)
		jobID, a := h.start(3)
		h.post(resultPath(jobID, a.AttemptID), failureBody(a.LeaseToken, "PERMANENT"))
		if code, body := cancel(h, jobID); code != http.StatusConflict || errorCode(t, body) != "JOB_ALREADY_TERMINAL" {
			t.Fatalf("cancel of a FAILED job: %d %s, want 409 JOB_ALREADY_TERMINAL", code, body)
		}
	})
}

// TestAPI_SweeperResolvedJob_PresentsFailure: a job failed or cancelled by the lease sweeper has no
// results row, yet the API still says why through fields it already serves: GET job gives the
// state and max_attempts, GET attempts gives each attempt's state, error_class (LEASE_EXPIRED) and
// error text.
func TestAPI_SweeperResolvedJob_PresentsFailure(t *testing.T) {
	sweep := func(h *renewHarness, backoff time.Duration) {
		t.Helper()
		h.clock.Advance(domain.DefaultLeaseDuration + time.Second)
		res, err := h.repo.SweepExpiredLeases(context.Background(), domain.SweepParams{Backoff: func(int) time.Duration { return backoff }})
		if err != nil || len(res.Swept) != 1 {
			t.Fatalf("sweep = %+v, %v", res, err)
		}
	}

	t.Run("lost_attempts_exhaust_max_attempts", func(t *testing.T) {
		h := newRenewHarness(t)
		jobID, _ := h.start(2)
		sweep(h, 0)
		if j := h.job(jobID); j.State != domain.StateRetrying {
			t.Fatalf("after the first loss the job is %s, want RETRYING", j.State)
		}
		h.poll()
		sweep(h, 0)

		j := h.job(jobID)
		if j.State != domain.StateFailed || j.MaxAttempts != 2 || j.Result != nil {
			t.Fatalf("job = %+v, want FAILED, max_attempts 2, no result row", j)
		}
		atts := h.attempts(jobID)
		if len(atts) != 2 {
			t.Fatalf("attempts = %d, want 2 used of max_attempts 2", len(atts))
		}
		for i, a := range atts {
			if a.AttemptNo != i+1 || a.State != domain.AttemptLost || a.ErrorClass != domain.ClassLeaseExpired || !strings.Contains(a.Error, "lease expired") {
				t.Fatalf("attempt %d = %+v, want LOST / LEASE_EXPIRED / 'lease expired...'", i+1, a)
			}
		}
	})

	t.Run("cancel_requested_job_resolved_by_the_sweeper", func(t *testing.T) {
		h := newRenewHarness(t)
		jobID, _ := h.start(3)
		h.post("/v1/jobs/"+jobID+"/cancel", `{}`)
		sweep(h, 0)
		j := h.job(jobID)
		if j.State != domain.StateCancelled || j.Result != nil {
			t.Fatalf("job = %+v, want CANCELLED without a result", j)
		}
		atts := h.attempts(jobID)
		if len(atts) != 1 || atts[0].State != domain.AttemptCancelled || atts[0].ErrorClass != "" {
			t.Fatalf("attempts = %+v, want one CANCELLED attempt with no error class", atts)
		}
	})
}
