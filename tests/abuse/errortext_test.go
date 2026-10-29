package abuse_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestAbuse_OversizeErrorText_RetryPath sends a failing attempt with error text around and far above
// the 10,240-byte limit of docs/security/field-limits.md through the retry path. Required: text at
// the limit is accepted, text above it is rejected as 400 INVALID_REQUEST without touching stored
// state (the attempt stays RUNNING), nothing above the limit is ever stored, no 5xx, and the retry
// still works afterwards with a valid commit.
func TestAbuse_OversizeErrorText_RetryPath(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		job := r.Submit(3)
		a := r.Poll("w1")
		for _, n := range []int{10241, 65537, 300_000, 900_000} {
			resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, strings.Repeat("e", n), "TRANSIENT")
			if resp.Status != http.StatusBadRequest || resp.ErrCode() != "INVALID_REQUEST" {
				t.Errorf("[%s] error text of %d bytes = %d %.120s, want 400 INVALID_REQUEST", kind, n, resp.Status, resp.Body)
			}
			if len(resp.Body) > 1024 {
				t.Errorf("response to a %d-byte error text is %d bytes long: it reflects the input", n, len(resp.Body))
			}
			at := r.Attempts(job)[0]
			if at.State != domain.AttemptRunning || len(at.Error) > 10240 {
				t.Fatalf("after the %d-byte error text the attempt is %s with %d stored error bytes, want untouched RUNNING", n, at.State, len(at.Error))
			}
		}
		if resp := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, strings.Repeat("e", 10240), "TRANSIENT"); resp.Status != http.StatusOK {
			t.Fatalf("error text of exactly 10240 bytes = %s, want 200", resp)
		}
		at := r.Attempts(job)[0]
		if at.State != domain.AttemptFailed || len(at.Error) != 10240 {
			t.Fatalf("stored attempt %s with %d error bytes, want FAILED with 10240", at.State, len(at.Error))
		}
		if j := r.Job(job); j.State != domain.StateRetrying {
			t.Fatalf("job %s, want RETRYING (the retry path must still work)", j.State)
		}
		r.MakeRetryDue()
		b := r.Poll("w1")
		if b == nil || b.AttemptNo != 2 {
			t.Fatalf("retry dispatch = %+v", b)
		}
		if resp := r.Result(job, b.AttemptID, b.LeaseToken, "w1", 0, "", ""); resp.Status != http.StatusOK {
			t.Fatalf("retry commit = %s", resp)
		}
		// Output bound on the same path.
		job2 := r.Submit(1)
		c := r.Poll("w1")
		over := r.Do("POST", "/v1/jobs/"+job2+"/attempts/"+c.AttemptID+"/result", map[string]any{"lease_token": c.LeaseToken, "worker_id": "w1", "exit_code": 0, "output": strings.Repeat("o", 65537)})
		if over.Status >= 500 || over.Status == http.StatusOK {
			t.Errorf("[%s] output of 65537 bytes = %s, want a 4xx rejection", kind, over)
		}
		if at := r.Attempts(job2)[0]; len(at.Output) > 65536 {
			t.Errorf("stored %d output bytes, limit is 65536", len(at.Output))
		}
		sectest.CheckInvariants(t, r, []string{job, job2})
	})
}
