package worker_test

import (
	"net/http"
	"testing"
	"time"
)

// A validation rejection of the result (400 INVALID_REQUEST, OUTPUT_TOO_LARGE) is a
// deterministic answer to this exact body. Dropping the result would leave the attempt RUNNING until
// its lease expires and burn every attempt. The worker sends ONE fallback failure report for the
// same attempt and lease token, class PERMANENT, and keeps renewing the lease until it is delivered.
func TestWorker_ResultRejected400_SendsOneFallbackFailureReport(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "OUTPUT_TOO_LARGE"} {
		code := code
		t.Run(code, func(t *testing.T) {
			r := newRig(t, renewed(false), nil)
			pendingAtFallback := -1
			r.coord.result = func(n int, w http.ResponseWriter, req *http.Request) {
				if n == 1 {
					rejectWith(400, code)(n, w, req)
					return
				}
				pendingAtFallback = r.clk.Pending() // renewal timer and delivery budget still armed
				okResult(n, w, req)
			}
			r.start()
			close(r.task.release)
			r.waitDone()
			if got := r.coord.results(); got != 2 {
				t.Fatalf("result requests = %d, want the original and exactly one fallback", got)
			}
			first, fb := r.coord.resultBodies[0], r.coord.resultBodies[1]
			if r.coord.resultPaths[0] != r.coord.resultPaths[1] {
				t.Fatalf("fallback went to %s, original to %s: must be the same attempt", r.coord.resultPaths[1], r.coord.resultPaths[0])
			}
			if first["output"] != "done" || first["exit_code"] != float64(0) {
				t.Fatalf("original report = %v", first)
			}
			if fb["lease_token"] != "tok-1" || fb["worker_id"] != "worker-1" {
				t.Fatalf("fallback token/worker = %v / %v", fb["lease_token"], fb["worker_id"])
			}
			if fb["error_classification"] != "PERMANENT" || fb["exit_code"] != float64(1) || fb["output"] != "" {
				t.Fatalf("fallback = %v, want PERMANENT, exit 1, empty output", fb)
			}
			if want := "result rejected by coordinator: HTTP 400 " + code; fb["error"] != want {
				t.Fatalf("fallback error = %q, want %q", fb["error"], want)
			}
			if pendingAtFallback < 2 {
				t.Fatalf("only %d timers pending when the fallback arrived: renewals stopped before it was delivered", pendingAtFallback)
			}
		})
	}
}

// If the fallback is rejected as well the worker behaves as before: it stops. No loop, no third report.
func TestWorker_ResultRejected400_FallbackAlsoRejected_NoLoop(t *testing.T) {
	r := newRig(t, renewed(false), rejectWith(400, "INVALID_REQUEST"))
	r.start()
	close(r.task.release)
	r.waitDone()
	r.clk.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if got := r.coord.results(); got != 2 {
		t.Fatalf("result requests = %d, want 2 (original and one fallback, never a third)", got)
	}
}

// Rejections that mean the result is no longer wanted (stale attempt, terminal job, lost lease, a
// 400 that is not about the content) never trigger a fallback.
func TestWorker_ResultRejected_NoFallbackWhenResultNoLongerWanted(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{400, "INVALID_STATE_TRANSITION"},
		{400, ""},
		{409, "STALE_ATTEMPT_REJECTED"},
		{409, "JOB_ALREADY_TERMINAL"},
		{409, "LEASE_EXPIRED"},
		{403, "WORKER_MISMATCH"},
		{404, "JOB_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.code+"_"+http.StatusText(tc.status), func(t *testing.T) {
			r := newRig(t, renewed(false), rejectWith(tc.status, tc.code))
			r.start()
			close(r.task.release)
			r.waitDone()
			if got := r.coord.results(); got != 1 {
				t.Fatalf("result requests = %d, want exactly 1 (no fallback)", got)
			}
		})
	}
}

// The worker builds the report with json.Marshal, so a 400 MALFORMED_JSON means the body was corrupted
// in transit: a transient fault, not a verdict on the content. A PERMANENT fallback would turn one
// corrupted successful result into a FAILED job. The result is dropped as before the fallback
// existed, the attempt is reclaimed and the task re-runs (at-least-once).
func TestWorker_ResultRejected400_MalformedJSON_NoFallback(t *testing.T) {
	r := newRig(t, renewed(false), rejectWith(400, "MALFORMED_JSON"))
	r.start()
	close(r.task.release)
	r.waitDone()
	r.clk.Advance(time.Minute)
	time.Sleep(20 * time.Millisecond)
	if got := r.coord.results(); got != 1 {
		t.Fatalf("result requests = %d, want exactly 1 (no fallback)", got)
	}
}
