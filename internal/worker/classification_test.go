package worker_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// lastResult returns the body of the one result the rig's fake coordinator received.
func (r *rig) lastResult() map[string]any {
	r.t.Helper()
	r.coord.mu.Lock()
	defer r.coord.mu.Unlock()
	if len(r.coord.resultBodies) != 1 {
		r.t.Fatalf("result bodies = %d, want exactly 1", len(r.coord.resultBodies))
	}
	return r.coord.resultBodies[0]
}

// TestWorker_ExecuteAndReport_ReportsFailureClassification: the worker classifies each failed
// outcome and sends it as error_classification (docs/protocol.md section 6): a validation failure
// is PERMANENT, a task deadline TRANSIENT, a cancelled task UNKNOWN, and a success carries none.
func TestWorker_ExecuteAndReport_ReportsFailureClassification(t *testing.T) {
	t.Run("validation_failure_is_PERMANENT", func(t *testing.T) {
		r := newRig(t, renewed(false), okResult)
		// A real allowlisted task given a payload its validation rejects.
		r.asg.TaskType = "echo"
		r.asg.Payload = json.RawMessage(`{"message": oops`)
		r.agent.ExecuteAndReport(context.Background(), r.asg)
		got := r.lastResult()
		if got["error_classification"] != "PERMANENT" || got["exit_code"] != float64(1) {
			t.Fatalf("result = %v, want exit_code 1 and error_classification PERMANENT", got)
		}
		if msg, _ := got["error"].(string); !strings.Contains(msg, "valid JSON") {
			t.Fatalf("result error %q lacks the validation message", msg)
		}
	})

	t.Run("task_deadline_is_TRANSIENT", func(t *testing.T) {
		r := newRig(t, renewed(false), okResult)
		r.asg.TimeoutSeconds = 1 // the block task never finishes, so the real 1 s deadline fires
		r.start()
		r.waitDone()
		if got := r.lastResult(); got["error_classification"] != "TRANSIENT" || got["exit_code"] != float64(1) {
			t.Fatalf("result = %v, want exit_code 1 and error_classification TRANSIENT", got)
		}
	})

	t.Run("success_carries_no_classification", func(t *testing.T) {
		r := newRig(t, renewed(false), okResult)
		r.start()
		close(r.task.release)
		r.waitDone()
		got := r.lastResult()
		if _, present := got["error_classification"]; present || got["exit_code"] != float64(0) {
			t.Fatalf("result = %v, want exit_code 0 and no error_classification", got)
		}
	})
}

// TestWorker_CooperativeCancellation_ContextPreemption: cancel_requested on a renewal cancels the
// context handed to the executor, the preempted task returns context.Canceled, and the worker still
// reports the (failed, UNKNOWN) outcome, which the coordinator resolves as CANCELLED (ADR-0016).
// The task is preempted within the renewal interval, not at its own pace.
func TestWorker_CooperativeCancellation_ContextPreemption(t *testing.T) {
	r := newRig(t, renewed(true), okResult)
	r.start()
	r.tick(10*time.Second, 1)
	r.waitDone()

	if !r.task.cancelled.Load() {
		t.Fatal("the executor's context was not cancelled by cancel_requested")
	}
	got := r.lastResult()
	if got["exit_code"] != float64(1) || got["error_classification"] != "UNKNOWN" {
		t.Fatalf("result = %v, want exit_code 1 and error_classification UNKNOWN", got)
	}
	if msg, _ := got["error"].(string); !strings.Contains(msg, context.Canceled.Error()) {
		t.Fatalf("result error %q does not report the cancellation", msg)
	}
}
