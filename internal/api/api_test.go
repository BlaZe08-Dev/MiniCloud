package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	svc := coordinator.NewService(repo, sched, reg)
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
