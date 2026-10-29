package abuse_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// leakNeedles are fragments that must never appear in a response or a log: stack traces, SQL,
// driver names, file paths and connection details (docs/security/field-limits.md section 3).
var leakNeedles = []string{
	"goroutine ", "runtime error", "panic:", ".go:", "/home/", "/usr/", "SELECT ", "INSERT ", "UPDATE ", "SQLSTATE",
	"pgx", "pgconn", "postgres://", "password", "127.0.0.1:5433", "dial tcp", "search_path",
}

func scanLeak(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, n := range leakNeedles {
		if strings.Contains(text, n) {
			t.Errorf("%s leaks %q: %.200s", what, n, text)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%s contains a secret value", what)
		}
	}
}

// safe asserts the generic defence on every abusive request: no 5xx, no leaked internals, and an
// error envelope for application-level errors.
func safe(t *testing.T, name string, resp sectest.Resp) {
	t.Helper()
	if resp.Status >= 500 {
		t.Errorf("%s: server error %s", name, resp)
	}
	scanLeak(t, name, string(resp.Body))
	if resp.Status >= 400 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && resp.ErrCode() == "" {
		t.Errorf("%s: JSON error without an error.code: %s", name, resp)
	}
}

func queued(t *testing.T, r *sectest.Rig) int {
	t.Helper()
	jobs, err := r.Repo.ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(jobs)
}

const validSubmit = `{"task_type":"echo","payload":{"message":"x"}}`

// TestAbuse_MalformedBodies (AB-01): malformed, truncated and wrongly typed bodies on every
// body-taking endpoint are rejected 4xx with the fixed message and create nothing.
func TestAbuse_MalformedBodies(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		job := r.Submit(3)
		a := r.Poll("w1")
		before := queued(t, r)
		bodies := map[string]string{
			"empty": "", "open brace": "{", "truncated key": `{"task_type":`, "truncated string": `{"task_type":"ec`, "array": "[]",
			"string": `"x"`, "number": "123", "true": "true", "NUL byte": "\x00", "BOM": "\xef\xbb\xbf" + validSubmit,
			"unclosed payload": `{"task_type":"echo","payload":{`, "comment": `{/*x*/"task_type":"echo"}`, "single quotes": `{'task_type':'echo'}`,
		}
		endpoints := map[string]string{
			"submit":    "/v1/jobs",
			"register":  "/v1/workers/register",
			"heartbeat": "/v1/workers/w1/heartbeat",
			"result":    "/v1/jobs/" + job + "/attempts/" + a.AttemptID + "/result",
			"renew":     "/v1/jobs/" + job + "/attempts/" + a.AttemptID + "/renew",
		}
		for en, path := range endpoints {
			for bn, body := range bodies {
				resp := r.Do("POST", path, body)
				safe(t, en+" "+bn, resp)
				if resp.Status != http.StatusBadRequest && !(en == "heartbeat" && resp.Status == 200) {
					// A body that is valid JSON of the wrong shape ("null") may reach field validation;
					// every case here must still be a 400, except null-like bodies on heartbeat.
					t.Errorf("%s with %s body = %s, want 400", en, bn, resp)
				}
				if resp.Status == http.StatusBadRequest && en != "submit" || bn == "empty" {
					if c := resp.ErrCode(); c != "MALFORMED_JSON" && c != "INVALID_REQUEST" {
						t.Errorf("%s with %s body: code %q", en, bn, c)
					}
				}
			}
		}
		if after := queued(t, r); after != before {
			t.Errorf("rejected bodies changed the queue: %d -> %d", before, after)
		}
		if j := r.Job(job); j.State != "RUNNING" {
			t.Errorf("job state changed to %s", j.State)
		}
	})
}

// TestAbuse_TrailingDuplicateUnknownFields records three accepted-by-design quirks of
// encoding/json decoding, each one a finding: F-4e-8 trailing data after the JSON value is ignored,
// F-4e-9 duplicate keys resolve to the last value and unknown keys are ignored. The test pins
// the observed behaviour and proves the ignored parts have no effect on stored state.
func TestAbuse_TrailingDuplicateUnknownFields(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		resp := r.Do("POST", "/v1/jobs", validSubmit+` {"task_type":"sleep","payload":{"duration_ms":1}}`)
		if resp.Status != http.StatusCreated || queued(t, r) != 1 {
			t.Fatalf("trailing second object: %s, queued %d (F-4e-8: accepted, only the first value is used)", resp, queued(t, r))
		}
		if got := r.Job(sectest.JobID(resp)).TaskType; got != "echo" {
			t.Errorf("task type %q, want the first object's echo", got)
		}
		if resp := r.Do("POST", "/v1/jobs", validSubmit+"garbage"); resp.Status != http.StatusCreated {
			t.Errorf("trailing garbage: %s", resp)
		}
		resp = r.Do("POST", "/v1/jobs", `{"task_type":"echo","task_type":"sleep","payload":{"duration_ms":1}}`)
		if resp.Status != http.StatusCreated || r.Job(sectest.JobID(resp)).TaskType != "sleep" {
			t.Errorf("duplicate task_type: %s (F-4e-9: last value wins)", resp)
		}
		resp = r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"state":"SUCCEEDED","id":"job_forged","priority":99,"admin":true,"max_attempts":2,"max_attempts":3}`)
		if resp.Status != http.StatusCreated {
			t.Fatalf("unknown fields: %s", resp)
		}
		j := r.Job(sectest.JobID(resp))
		if j.ID == "job_forged" || j.State != "QUEUED" || j.Priority != 0 || j.MaxAttempts != 3 {
			t.Errorf("client-supplied server fields took effect: id=%s state=%s priority=%d max_attempts=%d", j.ID, j.State, j.Priority, j.MaxAttempts)
		}
		if strings.Contains(string(resp.Body), "admin") {
			t.Errorf("unknown field echoed back: %s", resp.Body)
		}
	})
}

// TestAbuse_ContentTypes (F-4e-12): protocol 2 requires application/json but the server decodes the
// body whatever the Content-Type says. The impact is nil for the JSON-only API (no form or multipart
// parsing, no CSRF-able side effect beyond SEC-004); pinned as observed.
func TestAbuse_ContentTypes(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/json; charset=utf-16", "application/xml", ""} {
			resp := r.Do("POST", "/v1/jobs", validSubmit, "Content-Type", ct)
			safe(t, "content-type "+ct, resp)
			if resp.Status != http.StatusCreated {
				t.Errorf("Content-Type %q = %s; F-4e-12 records that the type is not enforced", ct, resp)
			}
		}
		if got := r.Do("GET", "/healthz", nil).Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("response Content-Type %q, want application/json", got)
		}
	})
}

// TestAbuse_NumericBounds (AB-06): huge, negative, overflowing and mistyped numbers.
func TestAbuse_NumericBounds(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		sub := func(extra string) sectest.Resp {
			return r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},`+extra+`}`)
		}
		for _, c := range []struct{ extra, code string }{
			{`"max_attempts":9223372036854775807`, "INVALID_REQUEST"}, {`"max_attempts":-9223372036854775808`, "INVALID_REQUEST"},
			{`"max_attempts":0`, "INVALID_REQUEST"}, {`"max_attempts":-1`, "INVALID_REQUEST"}, {`"max_attempts":21`, "INVALID_REQUEST"},
			{`"max_attempts":99999999999999999999`, "MALFORMED_JSON"}, {`"max_attempts":1e999`, "MALFORMED_JSON"},
			{`"max_attempts":1.5`, "MALFORMED_JSON"}, {`"max_attempts":"3"`, "MALFORMED_JSON"}, {`"max_attempts":true`, "MALFORMED_JSON"},
			{`"timeout_seconds":0`, "INVALID_REQUEST"}, {`"timeout_seconds":-5`, "INVALID_REQUEST"}, {`"timeout_seconds":86401`, "INVALID_REQUEST"},
			{`"timeout_seconds":9223372036854775807`, "INVALID_REQUEST"}, {`"timeout_seconds":1e400`, "MALFORMED_JSON"},
		} {
			resp := sub(c.extra)
			safe(t, c.extra, resp)
			if resp.Status != http.StatusBadRequest || resp.ErrCode() != c.code {
				t.Errorf("%s = %s, want 400 %s", c.extra, resp, c.code)
			}
		}
		if n := queued(t, r); n != 0 {
			t.Errorf("%d jobs created by rejected numbers", n)
		}
		for _, slots := range []string{"-1", "0", "1025", "9223372036854775807", "99999999999999999999", "1.5", `"4"`} {
			resp := r.Do("POST", "/v1/workers/register", `{"id":"nw","total_slots":`+slots+`,"supported_tasks":["echo"]}`)
			safe(t, "slots "+slots, resp)
			if resp.Status != http.StatusBadRequest {
				t.Errorf("total_slots %s = %s, want 400", slots, resp)
			}
		}
		// available_slots is ignored (ADR-0011): extreme values change nothing.
		for _, v := range []string{"-9223372036854775808", "9223372036854775807"} {
			resp := r.Do("POST", "/v1/workers/w1/heartbeat", `{"available_slots":`+v+`}`)
			if resp.Status != http.StatusOK {
				t.Errorf("heartbeat available_slots %s = %s", v, resp)
			}
		}
		if ws, _ := r.Repo.ListWorkers(context.Background()); len(ws) != 1 || ws[0].AvailableSlots != 1 || ws[0].TotalSlots != 1 {
			t.Errorf("heartbeat values changed capacity: %+v", ws)
		}
		// exit_code extremes with a valid lease must not produce a 5xx or corrupt state.
		for _, code := range []int64{9223372036854775807, -9223372036854775808, 4294967296, -1} {
			job := r.Submit(2)
			a := r.Poll("w1")
			resp := r.Do("POST", "/v1/jobs/"+job+"/attempts/"+a.AttemptID+"/result", map[string]any{"lease_token": a.LeaseToken, "worker_id": "w1", "exit_code": code, "output": "", "error": "e", "error_classification": "PERMANENT"})
			safe(t, fmt.Sprintf("exit_code %d", code), resp)
			if resp.Status != http.StatusOK && resp.Status != http.StatusBadRequest {
				t.Errorf("exit_code %d = %s, want 200 or 400", code, resp)
			}
			if resp.Status == http.StatusBadRequest {
				// rejected: the attempt must still be committable with a sane value
				if ok := r.Result(job, a.AttemptID, a.LeaseToken, "w1", 1, "e", "PERMANENT"); ok.Status != http.StatusOK {
					t.Errorf("attempt unusable after a rejected exit_code %d: %s", code, ok)
				}
			}
		}
	})
}

// TestAbuse_OversizedBodies (AB-04, F-4e-11, F-4e-13).
func TestAbuse_OversizedBodies(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		big := strings.Repeat("a", 1024*1024+1)
		resp := r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+big+`"}`)
		safe(t, "1MiB+1 body", resp)
		if resp.Status != http.StatusBadRequest && resp.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("1 MiB + 1 body = %s, want 400 or 413 (F-4e-13: answered 400 MALFORMED_JSON, not 413)", resp)
		}
		if queued(t, r) != 0 {
			t.Error("an oversized body created a job")
		}
		if resp := r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+strings.Repeat("a", 65536-2)+`"}`); resp.Status != http.StatusCreated {
			t.Errorf("payload of exactly 65536 bytes = %d, want 201", resp.Status)
		}
		resp = r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":"`+strings.Repeat("a", 65536-1)+`"}`)
		if resp.Status != http.StatusRequestEntityTooLarge || resp.ErrCode() != "PAYLOAD_TOO_LARGE" {
			t.Errorf("payload of 65537 bytes = %s, want 413 PAYLOAD_TOO_LARGE", resp)
		}
		// F-4e-11: a huge task_type must not be reflected into the error message.
		resp = r.Do("POST", "/v1/jobs", `{"task_type":"`+strings.Repeat("t", 500000)+`","payload":{}}`)
		if resp.Status != http.StatusBadRequest || resp.ErrCode() != "UNKNOWN_TASK_TYPE" {
			t.Errorf("500 KB task_type = %d %s", resp.Status, resp.ErrCode())
		}
		if len(resp.Body) > 1024 {
			t.Errorf("response to a 500 KB task_type is %d bytes: the input is reflected (F-4e-11)", len(resp.Body))
		}
		// Registration: a huge version or task list is bounded by validation.
		resp = r.Do("POST", "/v1/workers/register", `{"id":"bigw","version":"`+strings.Repeat("v", 200000)+`","total_slots":1}`)
		if resp.Status != http.StatusBadRequest || len(resp.Body) > 1024 {
			t.Errorf("huge version = %d (%d bytes)", resp.Status, len(resp.Body))
		}
		many := make([]string, 5000)
		for i := range many {
			many[i] = "echo"
		}
		raw, _ := json.Marshal(map[string]any{"id": "bigw2", "total_slots": 1, "supported_tasks": many})
		if resp := r.Do("POST", "/v1/workers/register", raw); resp.Status != http.StatusBadRequest {
			t.Errorf("5000 supported_tasks = %s, want 400", resp)
		}
	})
}

// TestAbuse_DeepNesting (AB-05): pathological nesting is rejected or stored without harming the
// server, within a bounded time.
func TestAbuse_DeepNesting(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r := sectest.NewRig(t, kind)
		r.Register("w1", 1)
		for _, depth := range []int{9000, 100000, 500000} { // 500000 '[' + ']' is just under 1 MiB
			deep := strings.Repeat("[", depth) + strings.Repeat("]", depth)
			start := time.Now()
			resp := r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":`+deep+`}`)
			el := time.Since(start)
			safe(t, fmt.Sprintf("depth %d", depth), resp)
			if resp.Status != http.StatusCreated && resp.Status != http.StatusBadRequest && resp.Status != http.StatusRequestEntityTooLarge {
				t.Errorf("depth %d = %s", depth, resp)
			}
			if el > 5*time.Second {
				t.Errorf("depth %d took %v", depth, el)
			}
			if depth > 10000 && resp.Status == http.StatusCreated {
				t.Errorf("depth %d accepted although encoding/json's nesting limit is 10000", depth)
			}
		}
		// Same nesting in an unknown field and in the register body.
		deep := strings.Repeat("{\"a\":", 20000) + "1" + strings.Repeat("}", 20000)
		safe(t, "deep unknown field", r.Do("POST", "/v1/jobs", `{"task_type":"echo","payload":{},"x":`+deep+`}`))
		safe(t, "deep register", r.Do("POST", "/v1/workers/register", `{"id":"dw","total_slots":1,"x":`+deep+`}`))
		if resp := r.Do("GET", "/readyz", nil); resp.Status != http.StatusOK {
			t.Errorf("server unhealthy after deep nesting: %s", resp)
		}
	})
}
