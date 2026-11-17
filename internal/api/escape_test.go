package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

// SEC-067 regression tests: the API must not HTML-escape < > & U+2028 U+2029 in JSON responses,
// because the worker checks the raw poll payload bytes against executor.MaxPayloadSizeBytes.

func registerEchoWorker(t *testing.T, ts *httptest.Server, id string) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"total_slots":100,"supported_tasks":["echo"]}`, id)
	resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register worker status %d: %s", resp.StatusCode, b)
	}
}

// submitEcho submits an echo job and returns the HTTP status and body.
func submitEcho(t *testing.T, ts *httptest.Server, payload []byte) (int, []byte) {
	t.Helper()
	body := append([]byte(`{"task_type":"echo","payload":`), payload...)
	body = append(body, '}')
	resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/jobs: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// pollRaw polls as workerID and returns the raw response body (must be 200).
func pollRaw(t *testing.T, ts *httptest.Server, workerID string) []byte {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/v1/workers/%s/poll", ts.URL, workerID), "application/json", nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll status %d: %s", resp.StatusCode, b)
	}
	return b
}

func decodeAssignment(t *testing.T, raw []byte) *coordinator.WorkAssignment {
	t.Helper()
	var res struct {
		Assignment *coordinator.WorkAssignment `json:"assignment"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Assignment == nil {
		t.Fatalf("decode poll response: %v (%s)", err, truncate(raw))
	}
	return res.Assignment
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

// echoPayload builds {"message":"<unit repeated>"} of exactly total bytes (total-14 must divide by len(unit)).
func echoPayload(unit string, total int) []byte {
	const overhead = len(`{"message":""}`)
	return []byte(`{"message":"` + strings.Repeat(unit, (total-overhead)/len(unit)) + `"}`)
}

func TestAPI_PollPayloadIsNotHTMLEscaped(t *testing.T) {
	cases := []struct {
		name string
		unit string
	}{
		{"less-than", "<"},
		{"greater-than", ">"},
		{"ampersand", "&"},
		{"U+2028", " "},
		{"U+2029", " "},
		{"mixed", "<>&  "},
		{"plain letters", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := setupTestServer()
			defer ts.Close()
			registerEchoWorker(t, ts, "w1")

			// Largest size <= 65,000 that the unit divides evenly.
			total := 65000
			for (total-len(`{"message":""}`))%len(tc.unit) != 0 {
				total--
			}
			payload := echoPayload(tc.unit, total)
			if len(payload) != total || total < 64990 {
				t.Fatalf("test payload length %d, want %d (>= 64990)", len(payload), total)
			}

			if status, b := submitEcho(t, ts, payload); status != http.StatusCreated {
				t.Fatalf("submit status %d: %s", status, truncate(b))
			}
			a := decodeAssignment(t, pollRaw(t, ts, "w1"))

			if len(a.Payload) != len(payload) {
				t.Fatalf("received payload length %d, submitted %d", len(a.Payload), len(payload))
			}
			if !bytes.Equal(a.Payload, payload) {
				t.Fatalf("received payload bytes differ from submitted payload")
			}
			if _, _, err := executor.NewRegistry().Execute(context.Background(), a.TaskType, a.Payload, 5*time.Second); err != nil {
				t.Fatalf("worker-side Execute rejected the payload: %v", err)
			}
		})
	}
}

func TestAPI_PollPayloadMaxSizeBoundary(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()
	registerEchoWorker(t, ts, "w1")

	// 65,536 bytes exactly, all '<' inside the echo shape {"message":"<...<"}.
	payload := echoPayload("<", executor.MaxPayloadSizeBytes)
	if len(payload) != 65536 {
		t.Fatalf("payload length %d, want 65536", len(payload))
	}
	if status, b := submitEcho(t, ts, payload); status != http.StatusCreated {
		t.Fatalf("submit of exactly 65536 bytes: status %d: %s", status, truncate(b))
	}
	a := decodeAssignment(t, pollRaw(t, ts, "w1"))
	if !bytes.Equal(a.Payload, payload) {
		t.Fatalf("polled payload differs: length %d, want 65536", len(a.Payload))
	}
	if _, _, err := executor.NewRegistry().Execute(context.Background(), a.TaskType, a.Payload, 5*time.Second); err != nil {
		t.Fatalf("worker-side Execute rejected 65536-byte payload: %v", err)
	}

	// 65,537 bytes still rejected at submit.
	over := echoPayload("<", executor.MaxPayloadSizeBytes+1)
	if len(over) != 65537 {
		t.Fatalf("over payload length %d, want 65537", len(over))
	}
	status, b := submitEcho(t, ts, over)
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(b), `"PAYLOAD_TOO_LARGE"`) {
		t.Fatalf("65537 bytes: status %d body %s, want 413 PAYLOAD_TOO_LARGE", status, truncate(b))
	}
}

func TestAPI_NosniffHeaderOnEveryWriteJSONResponse(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()
	errTS, _ := setupErrorServer(context.DeadlineExceeded) // GetJob / SubmitJob / Ping fail as an outage
	defer errTS.Close()

	do := func(method, url, body string) *http.Response {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	cases := []struct {
		name   string
		method string
		url    string
		body   string
		want   int
	}{
		{"200 healthz", "GET", ts.URL + "/healthz", "", 200},
		{"201 submit", "POST", ts.URL + "/v1/jobs", `{"task_type":"echo","payload":{}}`, 201},
		{"400 invalid", "POST", ts.URL + "/v1/jobs", `{"task_type":""}`, 400},
		{"404 job not found", "GET", ts.URL + "/v1/jobs/nope", "", 404},
		{"503 outage", "GET", errTS.URL + "/v1/jobs/abc", "", 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(tc.method, tc.url, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := resp.Header.Values("X-Content-Type-Options"); len(got) != 1 {
				t.Fatalf("X-Content-Type-Options values = %v, want exactly one", got)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
		})
	}
	// The 503 path must keep Retry-After.
	if got := do("GET", errTS.URL+"/v1/jobs/abc", "").Header.Get("Retry-After"); got != "1" {
		t.Fatalf("503 Retry-After = %q, want 1", got)
	}
}

// legacyEscape re-encodes a decoded JSON value the way the old writeJSON did (HTML escaping on).
func legacyEscape(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAPI_ResponseWireChangeIsJSONEquivalent(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()
	registerEchoWorker(t, ts, "w1")
	payload := []byte(`{"message":"a<b>c&d e` + " " + `f"}`)
	if status, b := submitEcho(t, ts, payload); status != http.StatusCreated {
		t.Fatalf("submit status %d: %s", status, truncate(b))
	}
	raw := pollRaw(t, ts, "w1")

	var newVal any
	if err := json.Unmarshal(raw, &newVal); err != nil {
		t.Fatalf("new form is not valid JSON: %v", err)
	}
	old := legacyEscape(t, newVal)
	var oldVal any
	if err := json.Unmarshal(old, &oldVal); err != nil {
		t.Fatalf("old form is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(oldVal, newVal) {
		t.Fatalf("old and new forms decode to different values")
	}
	// Sanity: the forms really differ on the wire, and the new one carries raw characters.
	if bytes.Equal(old, raw) {
		t.Fatalf("old escaped form equals the new form; the wire did not change")
	}
	if !bytes.Contains(raw, []byte("<")) || bytes.Contains(raw, []byte("\\u003c")) {
		t.Fatalf("new form still contains escaped '<': %s", truncate(raw))
	}
	if !bytes.HasSuffix(raw, []byte("}\n")) {
		t.Fatalf("response lost its trailing newline")
	}
}

func TestAPI_ErrorBodyIsNotHTMLEscaped(t *testing.T) {
	const msg = "bad <tag> & more end"
	errTS, _ := setupErrorServer(fmt.Errorf("%w: %s", domain.ErrInvalidRequest, msg))
	defer errTS.Close()

	resp, err := http.Post(errTS.URL+"/v1/jobs", "application/json", strings.NewReader(`{"task_type":"echo","payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var er api.ErrorResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		t.Fatalf("error body is not valid JSON: %v (%s)", err, raw)
	}
	if want := domain.ErrInvalidRequest.Error() + ": " + msg; er.Error.Message != want {
		t.Fatalf("decoded message %q, want %q", er.Error.Message, want)
	}
	// encoding/json always escapes U+2028/U+2029 inside plain string values, even with HTML escaping off;
	// only < > & are asserted raw here. Poll payloads are json.RawMessage and stay byte-exact (T1).
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(string(raw), esc) {
			t.Fatalf("error body still escapes %s: %s", esc, raw)
		}
	}
	if !strings.Contains(string(raw), "<tag> & more") {
		t.Fatalf("error body missing raw characters: %s", raw)
	}
}
