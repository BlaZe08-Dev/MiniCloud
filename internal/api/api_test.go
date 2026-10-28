package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

func setupTestServer() (*httptest.Server, *coordinator.Service) {
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, 10*time.Second)
	apiServer := api.NewServer("127.0.0.1:0", svc)
	ts := httptest.NewServer(apiServer.Handler())
	return ts, svc
}

type errorInjectingRepository struct {
	storage.Repository
	err error
}

func (r *errorInjectingRepository) SubmitJob(ctx context.Context, job *domain.Job) error {
	return r.err
}

func (r *errorInjectingRepository) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return nil, r.err
}

func (r *errorInjectingRepository) Ping(ctx context.Context) error {
	return r.err
}

func setupErrorServer(injectErr error) (*httptest.Server, *coordinator.Service) {
	baseRepo := storage.NewInMemoryRepository()
	repo := &errorInjectingRepository{Repository: baseRepo, err: injectErr}
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, 10*time.Second)
	apiServer := api.NewServer("127.0.0.1:0", svc)
	ts := httptest.NewServer(apiServer.Handler())
	return ts, svc
}

func TestAPI_HealthProbes(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	respReady, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer respReady.Body.Close()
	if respReady.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", respReady.StatusCode)
	}
}

func TestAPI_SubmitJob_Validation(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	// 1. Valid submission
	validReq := []byte(`{"task_type":"echo","payload":{"message":"hello"}}`)
	resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", bytes.NewReader(validReq))
	if err != nil {
		t.Fatalf("POST /v1/jobs failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var created domain.Job
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if created.ID == "" || created.State != domain.StateQueued {
		t.Fatalf("unexpected job response: %+v", created)
	}

	// 2. Unknown task type
	invalidTaskReq := []byte(`{"task_type":"unknown_shell","payload":{}}`)
	respInv, err := http.Post(ts.URL+"/v1/jobs", "application/json", bytes.NewReader(invalidTaskReq))
	if err != nil {
		t.Fatalf("POST /v1/jobs failed: %v", err)
	}
	defer respInv.Body.Close()
	if respInv.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", respInv.StatusCode)
	}

	var errResp api.ErrorResponse
	_ = json.NewDecoder(respInv.Body).Decode(&errResp)
	if errResp.Error.Code != "UNKNOWN_TASK_TYPE" {
		t.Fatalf("expected error code UNKNOWN_TASK_TYPE, got %s", errResp.Error.Code)
	}

	// 3. Not found on query
	respNF, err := http.Get(ts.URL + "/v1/jobs/non_existent_id")
	if err != nil {
		t.Fatalf("GET /v1/jobs failed: %v", err)
	}
	defer respNF.Body.Close()
	if respNF.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found, got %d", respNF.StatusCode)
	}
}

func TestAPI_StaleAttemptRejection(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	// 1. Register worker
	regWorker := []byte(`{"id":"worker-1","version":"v0.1.0","total_slots":2,"supported_tasks":["echo"]}`)
	resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(regWorker))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("worker registration failed: %v, status: %d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Submit job
	subJob := []byte(`{"task_type":"echo","payload":{"message":"test"}}`)
	resp, err = http.Post(ts.URL+"/v1/jobs", "application/json", bytes.NewReader(subJob))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("job submission failed: %v", err)
	}
	var job domain.Job
	_ = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()

	// 3. Poll work
	resp, err = http.Post(ts.URL+"/v1/workers/worker-1/poll", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("poll work failed: %v, status: %d", err, resp.StatusCode)
	}
	var pollRes struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&pollRes)
	resp.Body.Close()

	assignment := pollRes.Assignment
	if assignment.JobID != job.ID {
		t.Fatalf("expected assignment for job %s, got %s", job.ID, assignment.JobID)
	}

	// 4. Stale attempt rejection: Report result with fake attempt ID
	fakeReport := []byte(`{"lease_token":"` + assignment.LeaseToken + `","worker_id":"worker-1","exit_code":0,"output":"ok"}`)
	resp, err = http.Post(ts.URL+"/v1/jobs/"+job.ID+"/attempts/fake_attempt_id/result", "application/json", bytes.NewReader(fakeReport))
	if err != nil {
		t.Fatalf("report result request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict for stale attempt, got %d", resp.StatusCode)
	}
	var errResp api.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Code != "STALE_ATTEMPT_REJECTED" {
		t.Fatalf("expected STALE_ATTEMPT_REJECTED error code, got %s", errResp.Error.Code)
	}

	// 5. Stale attempt rejection: Report result with wrong lease token
	wrongLeaseReport := []byte(`{"lease_token":"wrong_lease_token","worker_id":"worker-1","exit_code":0,"output":"ok"}`)
	respWrongLease, err := http.Post(ts.URL+"/v1/jobs/"+job.ID+"/attempts/"+assignment.AttemptID+"/result", "application/json", bytes.NewReader(wrongLeaseReport))
	if err != nil {
		t.Fatalf("report result request failed: %v", err)
	}
	defer respWrongLease.Body.Close()

	if respWrongLease.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict for wrong lease token, got %d", respWrongLease.StatusCode)
	}

	// 5b. Worker mismatch rejection: Report result from different worker returns 403 WORKER_MISMATCH
	wrongWorkerReport := []byte(`{"lease_token":"` + assignment.LeaseToken + `","worker_id":"rogue-worker","exit_code":0,"output":"ok"}`)
	respWrongWorker, err := http.Post(ts.URL+"/v1/jobs/"+job.ID+"/attempts/"+assignment.AttemptID+"/result", "application/json", bytes.NewReader(wrongWorkerReport))
	if err != nil {
		t.Fatalf("report result request failed: %v", err)
	}
	defer respWrongWorker.Body.Close()
	if respWrongWorker.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for worker mismatch, got %d", respWrongWorker.StatusCode)
	}
	var errMismatch api.ErrorResponse
	_ = json.NewDecoder(respWrongWorker.Body).Decode(&errMismatch)
	if errMismatch.Error.Code != "WORKER_MISMATCH" {
		t.Fatalf("expected WORKER_MISMATCH error code, got %s", errMismatch.Error.Code)
	}
	if strings.Contains(errMismatch.Error.Message, "worker-1") {
		t.Fatalf("SECURITY VIOLATION: assigned worker ID leaked to unauthorized caller: %s", errMismatch.Error.Message)
	}

	// 6. Valid report succeeds
	validReport := []byte(`{"lease_token":"` + assignment.LeaseToken + `","worker_id":"worker-1","exit_code":0,"output":"ok"}`)
	respValid, err := http.Post(ts.URL+"/v1/jobs/"+job.ID+"/attempts/"+assignment.AttemptID+"/result", "application/json", bytes.NewReader(validReport))
	if err != nil {
		t.Fatalf("report result request failed: %v", err)
	}
	defer respValid.Body.Close()
	if respValid.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK for valid attempt report, got %d", respValid.StatusCode)
	}

	// 7. Duplicate report after job is terminal returns 409
	respDup, err := http.Post(ts.URL+"/v1/jobs/"+job.ID+"/attempts/"+assignment.AttemptID+"/result", "application/json", bytes.NewReader(validReport))
	if err != nil {
		t.Fatalf("duplicate report failed: %v", err)
	}
	defer respDup.Body.Close()
	if respDup.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict for duplicate commit on terminal job, got %d", respDup.StatusCode)
	}
}

// TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500 verifies SEC-007:
// unmapped driver errors must never reach clients, but return an opaque 500 INTERNAL_ERROR.
func TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500(t *testing.T) {
	driverErr := errors.New("pq: driver connection lost unexpectedly: host=pg.internal:5432 user=secret_user password=secret_password")
	ts, _ := setupErrorServer(driverErr)
	defer ts.Close()

	validReq := []byte(`{"task_type":"echo","payload":{"message":"hello"}}`)
	resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", bytes.NewReader(validReq))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500, got %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	var errResp api.ErrorResponse
	if err := json.Unmarshal(bodyBytes, &errResp); err != nil {
		t.Fatalf("failed to decode JSON error envelope: %v", err)
	}
	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("expected code INTERNAL_ERROR, got %q", errResp.Error.Code)
	}
	if errResp.Error.Message != "internal server error" {
		t.Fatalf("expected opaque message 'internal server error', got %q", errResp.Error.Message)
	}
	if strings.Contains(bodyStr, "secret_password") {
		t.Fatalf("SECURITY VIOLATION: password leaked in error response: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "driver connection lost") {
		t.Fatalf("SECURITY VIOLATION: driver error string leaked in error response: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "pg.internal") {
		t.Fatalf("SECURITY VIOLATION: database host leaked in error response: %s", bodyStr)
	}
}

// TestAPI_OutageMapping_503RetryAfterOpaqueBody verifies that connection failures and context deadline
// expiry return 503 SERVICE_UNAVAILABLE with Retry-After: 1 and an opaque body, on both job endpoints and /readyz.
func TestAPI_OutageMapping_503RetryAfterOpaqueBody(t *testing.T) {
	outageCases := []struct {
		name        string
		err         error
		leakMarkers []string
	}{
		{
			name: "connection refused with internal host and credentials",
			err:  errors.New("dial tcp db-internal.cluster.local:5432: connect: connection refused; user=minicloud_app password=super_secret_db_pass"),
			leakMarkers: []string{
				"connection refused",
				"5432",
				"db-internal.cluster.local",
				"minicloud_app",
				"super_secret_db_pass",
				"password=",
			},
		},
		{
			name: "context deadline exceeded with query context",
			err:  fmt.Errorf("query timeout while accessing host postgresql://user:secret123@db.prod.internal:5432/minicloud: %w", context.DeadlineExceeded),
			leakMarkers: []string{
				"context deadline exceeded",
				"DeadlineExceeded",
				"secret123",
				"db.prod.internal",
				"5432",
				"postgresql://",
			},
		},
		{
			name: "broken pipe socket error with hostname and password pattern",
			err:  errors.New("write tcp 10.0.0.5:45678->db-node-01.internal:5432: write: broken pipe (password: secret_token_xyz)"),
			leakMarkers: []string{
				"broken pipe",
				"db-node-01.internal",
				"45678",
				"secret_token_xyz",
			},
		},
		{
			name: "connection reset by peer with auth token",
			err:  errors.New("read tcp 127.0.0.1:5432: read: connection reset by peer (auth token: tok_987654321)"),
			leakMarkers: []string{
				"connection reset",
				"tok_987654321",
				"5432",
			},
		},
	}

	universalForbiddenPatterns := []string{
		"password",
		"secret",
		"5432",
		"cluster.local",
		"internal",
		"dial tcp",
		"connect:",
	}

	for _, tc := range outageCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := setupErrorServer(tc.err)
			defer ts.Close()

			// 1. GET /readyz should return 503 SERVICE_UNAVAILABLE with Retry-After: 1
			readyResp, err := http.Get(ts.URL + "/readyz")
			if err != nil {
				t.Fatalf("readyz request failed: %v", err)
			}
			defer readyResp.Body.Close()

			if readyResp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected HTTP 503 on readyz, got %d", readyResp.StatusCode)
			}
			if retryAfter := readyResp.Header.Get("Retry-After"); retryAfter != "1" {
				t.Fatalf("expected Retry-After header '1', got %q", retryAfter)
			}

			readyBodyBytes, _ := io.ReadAll(readyResp.Body)
			readyBodyStr := string(readyBodyBytes)

			var readyErrResp api.ErrorResponse
			if err := json.Unmarshal(readyBodyBytes, &readyErrResp); err != nil {
				t.Fatalf("failed to decode readyz JSON error envelope: %v", err)
			}
			if readyErrResp.Error.Code != "SERVICE_UNAVAILABLE" {
				t.Fatalf("expected code SERVICE_UNAVAILABLE on readyz, got %q", readyErrResp.Error.Code)
			}
			if readyErrResp.Error.Message != "service temporarily unavailable" {
				t.Fatalf("expected opaque message 'service temporarily unavailable' on readyz, got %q", readyErrResp.Error.Message)
			}

			// 2. GET /v1/jobs/job_12345 should return 503 SERVICE_UNAVAILABLE with Retry-After: 1
			resp, err := http.Get(ts.URL + "/v1/jobs/job_12345")
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected HTTP 503, got %d", resp.StatusCode)
			}
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "1" {
				t.Fatalf("expected Retry-After header '1', got %q", retryAfter)
			}

			bodyBytes, _ := io.ReadAll(resp.Body)
			bodyStr := string(bodyBytes)

			var errResp api.ErrorResponse
			if err := json.Unmarshal(bodyBytes, &errResp); err != nil {
				t.Fatalf("failed to decode JSON error envelope: %v", err)
			}
			if errResp.Error.Code != "SERVICE_UNAVAILABLE" {
				t.Fatalf("expected code SERVICE_UNAVAILABLE, got %q", errResp.Error.Code)
			}
			if errResp.Error.Message != "service temporarily unavailable" {
				t.Fatalf("expected opaque message 'service temporarily unavailable', got %q", errResp.Error.Message)
			}

			// Check leak markers on both readyz and job endpoints
			for _, marker := range tc.leakMarkers {
				if strings.Contains(readyBodyStr, marker) {
					t.Fatalf("SECURITY VIOLATION: outage detail %q leaked in readyz response: %s", marker, readyBodyStr)
				}
				if strings.Contains(bodyStr, marker) {
					t.Fatalf("SECURITY VIOLATION: outage detail %q leaked in job response: %s", marker, bodyStr)
				}
			}
			for _, pat := range universalForbiddenPatterns {
				if strings.Contains(strings.ToLower(readyBodyStr), pat) {
					t.Fatalf("SECURITY VIOLATION: pattern %q leaked in readyz response: %s", pat, readyBodyStr)
				}
				if strings.Contains(strings.ToLower(bodyStr), pat) {
					t.Fatalf("SECURITY VIOLATION: pattern %q leaked in job response: %s", pat, bodyStr)
				}
			}
		})
	}
}

// TestAPI_SubmitJob_MalformedJSON_FixedMessage verifies SEC-007:
// JSON decoding failures return a fixed message without echoing decoder internals or offsets.
func TestAPI_SubmitJob_MalformedJSON_FixedMessage(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	// 1. SubmitJob with malformed JSON
	resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", strings.NewReader(`{"task_type":"echo", "payload": {`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for malformed JSON, got %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	var errResp api.ErrorResponse
	if err := json.Unmarshal(bodyBytes, &errResp); err != nil {
		t.Fatalf("failed to decode error envelope: %v", err)
	}
	if errResp.Error.Code != "MALFORMED_JSON" {
		t.Fatalf("expected code MALFORMED_JSON, got %q", errResp.Error.Code)
	}
	if errResp.Error.Message != "malformed JSON request body" {
		t.Fatalf("expected fixed message 'malformed JSON request body', got %q", errResp.Error.Message)
	}
	if strings.Contains(bodyStr, "unexpected EOF") {
		t.Fatalf("SECURITY VIOLATION: Go JSON decoder internals leaked: %s", bodyStr)
	}

	// 2. RegisterWorker with malformed JSON
	respReg, err := http.Post(ts.URL+"/v1/workers/register", "application/json", strings.NewReader(`{invalid_json`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respReg.Body.Close()

	if respReg.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for malformed worker register, got %d", respReg.StatusCode)
	}
	var errRespReg api.ErrorResponse
	if err := json.NewDecoder(respReg.Body).Decode(&errRespReg); err != nil {
		t.Fatalf("failed to decode error envelope: %v", err)
	}
	if errRespReg.Error.Code != "MALFORMED_JSON" || errRespReg.Error.Message != "malformed JSON request body" {
		t.Fatalf("expected fixed message, got %+v", errRespReg)
	}
}

// TestAPI_IdentifierValidation_NegativeCases verifies SEC-008:
// worker IDs and route parameter IDs are strictly bounded (<= 128 chars) and validated
// against [a-zA-Z0-9_-], rejecting 100 KiB IDs, control characters, newlines, and path traversal strings.
func TestAPI_IdentifierValidation_NegativeCases(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	hugeID := strings.Repeat("a", 100*1024) // 100 KiB identifier
	testCases := []struct {
		name string
		id   string
	}{
		{"100_kib_id", hugeID},
		{"control_null_byte", "worker\x00id"},
		{"control_escape", "worker\x1bid"},
		{"newline_lf", "worker\nid"},
		{"newline_crlf", "worker\r\nid"},
		{"path_traversal_dotdot", "../../etc/passwd"},
		{"path_traversal_slash", "../worker-1"},
		{"sql_injection_semicolon", "worker;DROP TABLE jobs;--"},
		{"special_space", "worker 1"},
		{"special_symbols", "worker$!#"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Worker registration with invalid worker ID
			regBody, _ := json.Marshal(map[string]any{
				"id":              tc.id,
				"version":         "v0.1.0",
				"total_slots":     2,
				"supported_tasks": []string{"echo"},
			})
			resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(regBody))
			if err != nil {
				t.Fatalf("worker registration request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected HTTP 400 for registration with invalid ID %q, got %d", tc.name, resp.StatusCode)
			}
			var errResp api.ErrorResponse
			_ = json.NewDecoder(resp.Body).Decode(&errResp)
			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("expected INVALID_REQUEST error code, got %s", errResp.Error.Code)
			}

			// 2. Route query GET /v1/jobs/{id}
			escapedID := url.PathEscape(tc.id)
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/jobs/"+escapedID, nil)
			if err == nil {
				respGet, err := http.DefaultClient.Do(req)
				if err == nil {
					defer respGet.Body.Close()
					if respGet.StatusCode != http.StatusBadRequest && respGet.StatusCode != http.StatusNotFound {
						t.Fatalf("expected HTTP 400 or 404 for invalid job route ID %q, got %d", tc.name, respGet.StatusCode)
					}
					if respGet.StatusCode == http.StatusBadRequest {
						var getErr api.ErrorResponse
						_ = json.NewDecoder(respGet.Body).Decode(&getErr)
						if getErr.Error.Code != "INVALID_REQUEST" {
							t.Fatalf("expected INVALID_REQUEST, got %s", getErr.Error.Code)
						}
					}
				}
			}

			// 3. Worker heartbeat POST /v1/workers/{id}/heartbeat
			reqHB, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/workers/"+escapedID+"/heartbeat", strings.NewReader(`{"available_slots":1}`))
			if err == nil {
				reqHB.Header.Set("Content-Type", "application/json")
				respHB, err := http.DefaultClient.Do(reqHB)
				if err == nil {
					defer respHB.Body.Close()
					if respHB.StatusCode != http.StatusBadRequest && respHB.StatusCode != http.StatusNotFound {
						t.Fatalf("expected HTTP 400 or 404 for invalid worker heartbeat ID, got %d", respHB.StatusCode)
					}
				}
			}

			// 4. Worker poll POST /v1/workers/{id}/poll
			reqPoll, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/workers/"+escapedID+"/poll", nil)
			if err == nil {
				respPoll, err := http.DefaultClient.Do(reqPoll)
				if err == nil {
					defer respPoll.Body.Close()
					if respPoll.StatusCode != http.StatusBadRequest && respPoll.StatusCode != http.StatusNotFound {
						t.Fatalf("expected HTTP 400 or 404 for invalid worker poll ID, got %d", respPoll.StatusCode)
					}
				}
			}
		})
	}
}

// TestAPI_Slowloris_ReadHeaderTimeout verifies SEC-009:
// the coordinator HTTP server enforces ReadHeaderTimeout, aborting connections that
// trickle request headers beyond the allowed 2-second window.
func TestAPI_Slowloris_ReadHeaderTimeout(t *testing.T) {
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, 10*time.Second)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	srv := api.NewServer(ln.Addr().String(), svc)
	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// 1. Slowloris attack: send partial headers and pause for 2.5s (DefaultReadHeaderTimeout is 2s)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\nX-Slow-Header: trickling"))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Sleep past the 2s ReadHeaderTimeout
	time.Sleep(2500 * time.Millisecond)

	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, readErr := conn.Read(buf)
	// Server must have closed the socket due to ReadHeaderTimeout (returning EOF, read timeout, or 408)
	if readErr == nil && n > 0 {
		respStr := string(buf[:n])
		if !strings.Contains(respStr, "408") && strings.Contains(respStr, "200 OK") {
			t.Fatalf("SECURITY VIOLATION: server waited past ReadHeaderTimeout and completed slowloris request: %s", respStr)
		}
	}

	// 2. Normal prompt client completes headers within timeout and succeeds
	conn2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial 2 failed: %v", err)
	}
	defer conn2.Close()

	_, err = conn2.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
	if err != nil {
		t.Fatalf("write 2 failed: %v", err)
	}

	_ = conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n2, err := io.ReadAll(conn2)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read 2 failed: %v", err)
	}
	if !strings.Contains(string(n2), "200 OK") {
		t.Fatalf("expected HTTP 200 OK for prompt client, got %s", string(n2))
	}
}

// TestAPI_SubmitJob_BoundaryValues verifies SEC-014:
// boundary testing of max_attempts (1..20, default 3) and timeout_seconds (1..86400, default 60),
// testing 0, -1, 1, 20, 21, max int, absent, null, wrong JSON type, and float.
func TestAPI_SubmitJob_BoundaryValues(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	testCases := []struct {
		name               string
		body               string
		expectedStatus     int
		expectedCode       string
		expectedAttempts   int
		expectedTimeoutSec int
	}{
		// max_attempts boundary values
		{
			name:           "max_attempts_zero",
			body:           `{"task_type":"echo","payload":{},"max_attempts":0}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:           "max_attempts_negative_one",
			body:           `{"task_type":"echo","payload":{},"max_attempts":-1}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:             "max_attempts_one_valid_lower_bound",
			body:             `{"task_type":"echo","payload":{},"max_attempts":1}`,
			expectedStatus:   http.StatusCreated,
			expectedAttempts: 1,
		},
		{
			name:             "max_attempts_twenty_valid_upper_bound",
			body:             `{"task_type":"echo","payload":{},"max_attempts":20}`,
			expectedStatus:   http.StatusCreated,
			expectedAttempts: 20,
		},
		{
			name:           "max_attempts_twenty_one_exceeds_upper_bound",
			body:           `{"task_type":"echo","payload":{},"max_attempts":21}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:           "max_attempts_max_int",
			body:           `{"task_type":"echo","payload":{},"max_attempts":2147483647}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:               "max_attempts_absent_defaults_to_3",
			body:               `{"task_type":"echo","payload":{}}`,
			expectedStatus:     http.StatusCreated,
			expectedAttempts:   3,
			expectedTimeoutSec: 60,
		},
		{
			name:               "max_attempts_null_defaults_to_3",
			body:               `{"task_type":"echo","payload":{},"max_attempts":null}`,
			expectedStatus:     http.StatusCreated,
			expectedAttempts:   3,
			expectedTimeoutSec: 60,
		},
		{
			name:           "max_attempts_wrong_json_type_string",
			body:           `{"task_type":"echo","payload":{},"max_attempts":"three"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "MALFORMED_JSON",
		},
		{
			name:           "max_attempts_float",
			body:           `{"task_type":"echo","payload":{},"max_attempts":3.14}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "MALFORMED_JSON",
		},

		// timeout_seconds boundary values
		{
			name:           "timeout_seconds_zero",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":0}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:           "timeout_seconds_negative_one",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":-1}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:               "timeout_seconds_one_valid_lower_bound",
			body:               `{"task_type":"echo","payload":{},"timeout_seconds":1}`,
			expectedStatus:     http.StatusCreated,
			expectedTimeoutSec: 1,
			expectedAttempts:   3,
		},
		{
			name:               "timeout_seconds_eighty_six_four_hundred_valid_upper_bound",
			body:               `{"task_type":"echo","payload":{},"timeout_seconds":86400}`,
			expectedStatus:     http.StatusCreated,
			expectedTimeoutSec: 86400,
			expectedAttempts:   3,
		},
		{
			name:           "timeout_seconds_eighty_six_four_hundred_one_exceeds_upper_bound",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":86401}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:           "timeout_seconds_max_int",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":2147483647}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_REQUEST",
		},
		{
			name:               "timeout_seconds_null_defaults_to_60",
			body:               `{"task_type":"echo","payload":{},"timeout_seconds":null}`,
			expectedStatus:     http.StatusCreated,
			expectedTimeoutSec: 60,
			expectedAttempts:   3,
		},
		{
			name:           "timeout_seconds_wrong_json_type_string",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":"sixty"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "MALFORMED_JSON",
		},
		{
			name:           "timeout_seconds_float",
			body:           `{"task_type":"echo","payload":{},"timeout_seconds":60.5}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "MALFORMED_JSON",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected status %d, got %d; body: %s", tc.expectedStatus, resp.StatusCode, string(body))
			}

			if tc.expectedStatus == http.StatusCreated {
				var created domain.Job
				if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
					t.Fatalf("failed to decode created job: %v", err)
				}
				if tc.expectedAttempts > 0 && created.MaxAttempts != tc.expectedAttempts {
					t.Fatalf("expected max_attempts %d, got %d", tc.expectedAttempts, created.MaxAttempts)
				}
				if tc.expectedTimeoutSec > 0 && created.TimeoutSeconds != tc.expectedTimeoutSec {
					t.Fatalf("expected timeout_seconds %d, got %d", tc.expectedTimeoutSec, created.TimeoutSeconds)
				}
			} else {
				var errResp api.ErrorResponse
				if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
					t.Fatalf("failed to decode error response: %v", err)
				}
				if errResp.Error.Code != tc.expectedCode {
					t.Fatalf("expected code %q, got %q", tc.expectedCode, errResp.Error.Code)
				}
			}
		})
	}
}

// TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500 verifies SEC-014:
// if coordinator validation was bypassed and the database layer rejects max_attempts or timeout_seconds
// with a check constraint violation (SQLSTATE 23514), the API error mapper maps the typed domain error
// to HTTP 400 INVALID_REQUEST and NEVER exposes an opaque HTTP 500 INTERNAL_ERROR to the client.
func TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500(t *testing.T) {
	testCases := []struct {
		name        string
		simErr      error
		expectedMsg string
	}{
		{
			name:        "chk_jobs_max_attempts_check_violation",
			simErr:      fmt.Errorf("%w: max_attempts must be between 1 and 20", domain.ErrInvalidRequest),
			expectedMsg: "max_attempts must be between 1 and 20",
		},
		{
			name:        "chk_jobs_timeout_check_violation",
			simErr:      fmt.Errorf("%w: timeout_seconds must be between 1 and 86400", domain.ErrInvalidRequest),
			expectedMsg: "timeout_seconds must be between 1 and 86400",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := setupErrorServer(tc.simErr)
			defer ts.Close()

			validReq := `{"task_type":"echo","payload":{"message":"hello"}}`
			resp, err := http.Post(ts.URL+"/v1/jobs", "application/json", strings.NewReader(validReq))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected HTTP 400 Bad Request, got %d (never 500); body: %s", resp.StatusCode, string(body))
			}

			var errResp api.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if errResp.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("expected error code INVALID_REQUEST, got %s", errResp.Error.Code)
			}
			if !strings.Contains(errResp.Error.Message, tc.expectedMsg) {
				t.Fatalf("expected message to contain %q, got: %s", tc.expectedMsg, errResp.Error.Message)
			}
		})
	}
}

// TestAPI_RegisterWorker_SlotsUpperLimit verifies SEC-015:
// Worker registration bounds total_slots between 1 and 1024.
// Values <= 0 or > 1024 return HTTP 400 INVALID_REQUEST.
func TestAPI_RegisterWorker_SlotsUpperLimit(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	testCases := []struct {
		name       string
		slots      int
		wantStatus int
	}{
		{"slots_zero", 0, http.StatusBadRequest},
		{"slots_negative", -1, http.StatusBadRequest},
		{"slots_one_valid_lower", 1, http.StatusOK},
		{"slots_ten_valid", 10, http.StatusOK},
		{"slots_1024_valid_upper", 1024, http.StatusOK},
		{"slots_1025_exceeds_upper", 1025, http.StatusBadRequest},
		{"slots_ten_thousand_excessive", 10000, http.StatusBadRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workerID := fmt.Sprintf("worker-slots-%s", tc.name)
			body, _ := json.Marshal(map[string]any{
				"id":              workerID,
				"version":         "v0.1.0",
				"total_slots":     tc.slots,
				"supported_tasks": []string{"echo"},
			})

			resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.wantStatus, resp.StatusCode)
			}

			if tc.wantStatus == http.StatusBadRequest {
				var errResp api.ErrorResponse
				_ = json.NewDecoder(resp.Body).Decode(&errResp)
				if errResp.Error.Code != "INVALID_REQUEST" {
					t.Fatalf("expected code INVALID_REQUEST, got %s", errResp.Error.Code)
				}
				if !strings.Contains(errResp.Error.Message, "total_slots must be between 1 and 1024") {
					t.Fatalf("unexpected message: %s", errResp.Error.Message)
				}
			}
		})
	}
}

// TestAPI_RegisterWorker_VersionLengthLimit verifies SEC-016:
// Worker registration bounds version string to at most 64 characters.
// An empty version string is allowed (default). Versions exceeding 64 chars return HTTP 400 INVALID_REQUEST.
func TestAPI_RegisterWorker_VersionLengthLimit(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	testCases := []struct {
		name       string
		version    string
		wantStatus int
	}{
		{"version_empty_allowed", "", http.StatusOK},
		{"version_standard", "v0.1.0", http.StatusOK},
		{"version_64_chars_valid_upper", strings.Repeat("v", 64), http.StatusOK},
		{"version_65_chars_exceeds", strings.Repeat("v", 65), http.StatusBadRequest},
		{"version_100kib_oversized", strings.Repeat("x", 102400), http.StatusBadRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workerID := fmt.Sprintf("worker-ver-%s", tc.name)
			body, _ := json.Marshal(map[string]any{
				"id":              workerID,
				"version":         tc.version,
				"total_slots":     2,
				"supported_tasks": []string{"echo"},
			})

			resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.wantStatus, resp.StatusCode)
			}

			if tc.wantStatus == http.StatusBadRequest {
				var errResp api.ErrorResponse
				_ = json.NewDecoder(resp.Body).Decode(&errResp)
				if errResp.Error.Code != "INVALID_REQUEST" {
					t.Fatalf("expected code INVALID_REQUEST, got %s", errResp.Error.Code)
				}
				if !strings.Contains(errResp.Error.Message, "version length cannot exceed 64 characters") {
					t.Fatalf("unexpected message: %s", errResp.Error.Message)
				}
			}
		})
	}
}

// TestAPI_RegisterWorker_SupportedTasksAllowlist verifies SEC-017:
// Worker registration bounds supported_tasks array length (at most 16 unique tasks), individual task name
// length (at most 64 chars), and character set (strictly ^[a-z0-9_]{1,64}$).
// Empty supported_tasks is allowed (cannot claim jobs). Unknown-but-well-formed task names are accepted
// upon registration to support rolling worker fleet upgrades. Duplicates in supported_tasks are removed.
func TestAPI_RegisterWorker_SupportedTasksAllowlist(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	testCases := []struct {
		name       string
		tasks      []string
		wantStatus int
		errMsg     string
	}{
		{
			name:       "empty_tasks_allowed",
			tasks:      []string{},
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid_tasks_standard",
			tasks:      []string{"echo", "sleep"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid_16_tasks_upper_bound",
			tasks:      []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12", "t13", "t14", "t15", "t16"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "excess_17_unique_tasks_rejected",
			tasks:      []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12", "t13", "t14", "t15", "t16", "t17"},
			wantStatus: http.StatusBadRequest,
			errMsg:     "supported_tasks cannot exceed 16 tasks",
		},
		{
			name:       "17_tasks_with_duplicates_succeeds_after_dedup",
			tasks:      []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12", "t13", "t14", "t15", "t16", "t1"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "duplicates_removed_standard",
			tasks:      []string{"echo", "sleep", "echo"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown_well_formed_task_accepted_for_rolling_upgrade",
			tasks:      []string{"gpu_matrix_mult_v2", "custom_filter"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "task_name_64_chars_accepted",
			tasks:      []string{strings.Repeat("a", 64)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "task_name_65_chars_rejected",
			tasks:      []string{strings.Repeat("a", 65)},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
		{
			name:       "task_name_uppercase_rejected",
			tasks:      []string{"Echo"},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
		{
			name:       "task_name_hyphen_rejected",
			tasks:      []string{"a-b"},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
		{
			name:       "task_name_empty_rejected",
			tasks:      []string{""},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
		{
			name:       "task_name_invalid_charset_injection",
			tasks:      []string{"echo;rm -rf /"},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
		{
			name:       "task_name_with_spaces_rejected",
			tasks:      []string{"echo task"},
			wantStatus: http.StatusBadRequest,
			errMsg:     "invalid task name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workerID := fmt.Sprintf("worker-tasks-%s", tc.name)
			body, _ := json.Marshal(map[string]any{
				"id":              workerID,
				"version":         "v0.1.0",
				"total_slots":     2,
				"supported_tasks": tc.tasks,
			})

			resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.wantStatus, resp.StatusCode)
			}

			if tc.wantStatus == http.StatusBadRequest {
				var errResp api.ErrorResponse
				_ = json.NewDecoder(resp.Body).Decode(&errResp)
				if errResp.Error.Code != "INVALID_REQUEST" {
					t.Fatalf("expected code INVALID_REQUEST, got %s", errResp.Error.Code)
				}
				if tc.errMsg != "" && !strings.Contains(errResp.Error.Message, tc.errMsg) {
					t.Fatalf("expected message to contain %q, got: %s", tc.errMsg, errResp.Error.Message)
				}
			} else if tc.name == "duplicates_removed_standard" {
				// Verify in repository that duplicates were indeed removed
				listResp, err := http.Get(ts.URL + "/v1/workers")
				if err != nil {
					t.Fatalf("failed to query workers: %v", err)
				}
				defer listResp.Body.Close()
				var workers []domain.Worker
				_ = json.NewDecoder(listResp.Body).Decode(&workers)
				for _, w := range workers {
					if w.ID == workerID {
						if len(w.SupportedTasks) != 2 || w.SupportedTasks[0] != "echo" || w.SupportedTasks[1] != "sleep" {
							t.Fatalf("expected duplicates removed to [echo sleep], got: %v", w.SupportedTasks)
						}
					}
				}
			}
		})
	}
}

// TestAPI_CancelJob_SuccessReturns200 pins the cancel status codes: every valid cancellation,
// including a duplicate cancel on a CANCEL_REQUESTED job, returns HTTP 200 with the same body
// shape, and the duplicate returns exactly the body of the first request (SEC-022).
func TestAPI_CancelJob_SuccessReturns200(t *testing.T) {
	ts, svc := setupTestServer()
	defer ts.Close()
	ctx := context.Background()

	cancel := func(jobID string) (int, string) {
		t.Helper()
		resp, err := http.Post(ts.URL+"/v1/jobs/"+jobID+"/cancel", "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("POST cancel failed: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// QUEUED -> CANCELLED
	queued, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"q"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	status, body := cancel(queued.ID)
	if status != http.StatusOK || !strings.Contains(body, `"state":"CANCELLED"`) {
		t.Fatalf("cancel of QUEUED job: expected 200 CANCELLED, got %d %s", status, body)
	}
	if status, body = cancel(queued.ID); status != http.StatusConflict || !strings.Contains(body, "JOB_ALREADY_TERMINAL") {
		t.Fatalf("cancel of CANCELLED job: expected 409 JOB_ALREADY_TERMINAL, got %d %s", status, body)
	}

	// RUNNING -> CANCEL_REQUESTED, duplicates are 200 with an identical body
	if err := svc.RegisterWorker(ctx, &domain.Worker{ID: "w_cancel_api", TotalSlots: 1, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}
	running, err := svc.SubmitJob(ctx, "echo", []byte(`{"message":"r"}`), 30, 3)
	if err != nil {
		t.Fatalf("SubmitJob failed: %v", err)
	}
	assignment, err := svc.PollWork(ctx, "w_cancel_api")
	if err != nil || assignment.JobID != running.ID {
		t.Fatalf("PollWork: %v (assignment=%+v)", err, assignment)
	}
	firstStatus, firstBody := cancel(running.ID)
	if firstStatus != http.StatusOK || !strings.Contains(firstBody, `"state":"CANCEL_REQUESTED"`) {
		t.Fatalf("first cancel of RUNNING job: expected 200 CANCEL_REQUESTED, got %d %s", firstStatus, firstBody)
	}
	for i := 0; i < 2; i++ {
		dupStatus, dupBody := cancel(running.ID)
		if dupStatus != http.StatusOK || dupBody != firstBody {
			t.Fatalf("duplicate cancel: expected 200 with body %q, got %d %q", firstBody, dupStatus, dupBody)
		}
	}

	// The worker can still commit: the duplicates did not finalise the job.
	if _, err := svc.CommitResult(ctx, running.ID, assignment.AttemptID, assignment.LeaseToken, "w_cancel_api", 0, "ok", ""); err != nil {
		t.Fatalf("commit after duplicate cancels must be accepted, got %v", err)
	}
}

type apiTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *apiTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *apiTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestAPI_CommitResult_AfterLeaseSweep_Returns409LeaseExpired: a result reported for an attempt the
// sweeper already marked LOST is HTTP 409 LEASE_EXPIRED (protocol 6.3, priority 2), not 500 and
// not the generic STALE_ATTEMPT_REJECTED; a result for a terminal job stays JOB_ALREADY_TERMINAL.
func TestAPI_CommitResult_AfterLeaseSweep_Returns409LeaseExpired(t *testing.T) {
	clock := &apiTestClock{now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	repo := storage.NewInMemoryRepository(storage.WithClock(clock))
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc).Handler())
	defer ts.Close()

	post := func(path, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s failed: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := post("/v1/workers/register", `{"id":"worker-1","version":"v","total_slots":1,"supported_tasks":["echo"]}`); code != http.StatusOK {
		t.Fatalf("register: %d %s", code, body)
	}
	code, body := post("/v1/jobs", `{"task_type":"echo","payload":{"message":"x"},"max_attempts":1}`)
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var job domain.Job
	_ = json.Unmarshal([]byte(body), &job)
	code, body = post("/v1/workers/worker-1/poll", ``)
	if code != http.StatusOK {
		t.Fatalf("poll: %d %s", code, body)
	}
	var poll struct {
		Assignment coordinator.WorkAssignment `json:"assignment"`
	}
	_ = json.Unmarshal([]byte(body), &poll)
	a := poll.Assignment
	report := `{"lease_token":"` + a.LeaseToken + `","worker_id":"worker-1","exit_code":0,"output":"late"}`
	resultPath := "/v1/jobs/" + job.ID + "/attempts/" + a.AttemptID + "/result"

	// Lease lapses and the sweeper reaps the attempt. max_attempts is 1, so the job FAILS.
	clock.Advance(domain.DefaultLeaseDuration + time.Second)
	if res, err := repo.SweepExpiredLeases(context.Background(), domain.SweepParams{Backoff: func(int) time.Duration { return 0 }}); err != nil || len(res.Swept) != 1 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if code, body := post(resultPath, report); code != http.StatusConflict || !strings.Contains(body, "JOB_ALREADY_TERMINAL") {
		t.Fatalf("late result on the FAILED job: %d %s, want 409 JOB_ALREADY_TERMINAL (terminal job is evaluated first)", code, body)
	}

	// Second job with retries left: the job is RETRYING, so the swept attempt is what is rejected.
	_ = repo.SubmitJob(context.Background(), &domain.Job{ID: "job-retry", TaskType: "echo", Payload: []byte(`{}`), State: domain.StateQueued, MaxAttempts: 3, TimeoutSeconds: 30, CreatedAt: clock.Now()})
	_, att, err := repo.DispatchNextJob(context.Background(), "worker-1")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	clock.Advance(domain.DefaultLeaseDuration + time.Second)
	if res, err := repo.SweepExpiredLeases(context.Background(), domain.SweepParams{Backoff: func(int) time.Duration { return time.Hour }}); err != nil || len(res.Swept) != 1 {
		t.Fatalf("second sweep = %+v, %v", res, err)
	}
	late := `{"lease_token":"` + att.LeaseToken + `","worker_id":"worker-1","exit_code":0,"output":"late"}`
	code, body = post("/v1/jobs/job-retry/attempts/"+att.ID+"/result", late)
	if code != http.StatusConflict || !strings.Contains(body, `"LEASE_EXPIRED"`) {
		t.Fatalf("late result on a swept attempt: %d %s, want 409 LEASE_EXPIRED", code, body)
	}
}
