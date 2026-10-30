package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func setupTestIO(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	oldStdout, oldStderr, oldExit := stdout, stderr, exitFunc
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	stdout = outBuf
	stderr = errBuf
	exitFunc = func(code int) {}
	t.Cleanup(func() {
		stdout = oldStdout
		stderr = oldStderr
		exitFunc = oldExit
	})
	return outBuf, errBuf
}

func TestHandleSubmit_SanitizesTaskType(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job := domain.Job{
			ID:          "job_1_test",
			TaskType:    "\x1b[31mecho\x1b[0m",
			State:       domain.StateQueued,
			MaxAttempts: 3,
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer srv.Close()

	handleSubmit([]string{"--coordinator-url", srv.URL, "--task", "echo", "--payload", "{}"})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleSubmit printed raw escape sequence: %q", out)
	}
	if !strings.Contains(out, `\x1b[31mecho\x1b[0m`) {
		t.Fatalf("handleSubmit missing expected sanitized task type: %q", out)
	}
}

func TestHandleGet_SanitizesTaskType(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job := domain.Job{
			ID:        "job_1_test",
			TaskType:  "\x1b[31mecho\x1b[0m",
			State:     domain.StateQueued,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleGet([]string{"job_1_test"})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleGet printed raw escape sequence in task type: %q", out)
	}
	if !strings.Contains(out, `\x1b[31mecho\x1b[0m`) {
		t.Fatalf("handleGet missing expected sanitized task type: %q", out)
	}
}

func TestHandleGet_SanitizesResultOutput(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job := domain.Job{
			ID:        "job_1_test",
			TaskType:  "echo",
			State:     domain.StateSucceeded,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Result: &domain.Result{
				ExitCode: 0,
				Output:   "\x1b[32mOK\x1b[0m",
			},
		}
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleGet([]string{"job_1_test"})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleGet printed raw escape sequence in result output: %q", out)
	}
	if !strings.Contains(out, `\x1b[32mOK\x1b[0m`) {
		t.Fatalf("handleGet missing expected sanitized output: %q", out)
	}
}

func TestHandleGet_SanitizesResultError(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job := domain.Job{
			ID:        "job_1_test",
			TaskType:  "echo",
			State:     domain.StateFailed,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Result: &domain.Result{
				ExitCode: 1,
				Error:    "\x1b[31mFAIL\x1b[0m",
			},
		}
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleGet([]string{"job_1_test"})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleGet printed raw escape sequence in result error: %q", out)
	}
	if !strings.Contains(out, `\x1b[31mFAIL\x1b[0m`) {
		t.Fatalf("handleGet missing expected sanitized error: %q", out)
	}
}

func TestHandleAttempts_SanitizesWorkerID(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := struct {
			JobID    string            `json:"job_id"`
			Attempts []*domain.Attempt `json:"attempts"`
		}{
			JobID: "job_1_test",
			Attempts: []*domain.Attempt{
				{
					AttemptNo: 1,
					ID:        "att_1_test",
					WorkerID:  "\x1b[33mworker-1\x1b[0m",
					State:     domain.AttemptRunning,
					StartedAt: time.Now(),
				},
			},
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleAttempts([]string{"job_1_test"})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleAttempts printed raw escape sequence in worker ID: %q", out)
	}
	if !strings.Contains(out, `\x1b[33mworker-1\x1b[0m`) {
		t.Fatalf("handleAttempts missing expected sanitized worker ID: %q", out)
	}
}

func TestHandleWorkers_SanitizesWorkerID(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := struct {
			Workers []*domain.Worker `json:"workers"`
		}{
			Workers: []*domain.Worker{
				{
					ID:               "\x1b[34mworker-1\x1b[0m",
					Status:           domain.WorkerStatusHealthy,
					AvailableSlots:   1,
					TotalSlots:       2,
					LastHeartbeatAt:  time.Now(),
					SupportedTasks:   []string{"echo"},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleWorkers([]string{})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleWorkers printed raw escape sequence in worker ID: %q", out)
	}
	if !strings.Contains(out, `\x1b[34mworker-1\x1b[0m`) {
		t.Fatalf("handleWorkers missing expected sanitized worker ID: %q", out)
	}
}

func TestHandleWorkers_SanitizesSupportedTasks(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := struct {
			Workers []*domain.Worker `json:"workers"`
		}{
			Workers: []*domain.Worker{
				{
					ID:               "worker-1",
					Status:           domain.WorkerStatusHealthy,
					AvailableSlots:   1,
					TotalSlots:       2,
					LastHeartbeatAt:  time.Now(),
					SupportedTasks:   []string{"\x1b[35mtask-1\x1b[0m"},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleWorkers([]string{})

	out := outBuf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("handleWorkers printed raw escape sequence in supported tasks: %q", out)
	}
	if !strings.Contains(out, `\x1b[35mtask-1\x1b[0m`) {
		t.Fatalf("handleWorkers missing expected sanitized supported task: %q", out)
	}
}

func TestHandleRawJSON_SanitizesC1AndDEL(t *testing.T) {
	outBuf, _ := setupTestIO(t)
	// Server returns raw JSON that does not unmarshal into domain.Job (e.g. array instead of object).
	hostileJSON := "[\"item\x7f\", \"item\u0080\u009b\"]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(hostileJSON))
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleGet([]string{"job_1_test"})

	out := outBuf.String()
	if strings.Contains(out, "\x7f") || strings.Contains(out, "\u0080") || strings.Contains(out, "\u009b") {
		t.Fatalf("raw JSON printed unescaped DEL or C1 characters: %q", out)
	}
	if !strings.Contains(out, `\u007f`) || !strings.Contains(out, `\u0080`) || !strings.Contains(out, `\u009b`) {
		t.Fatalf("raw JSON missing expected \\u00xx escapes: %q", out)
	}
}

func TestHandleHTTPError_SanitizesErrorBody(t *testing.T) {
	_, errBuf := setupTestIO(t)
	hostileErr := "\x1b[2J\x1b[31minternal server failure\x1b[0m"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(hostileErr))
	}))
	defer srv.Close()
	t.Setenv("MINICLOUD_COORDINATOR_URL", srv.URL)

	handleGet([]string{"job_1_test"})

	errOut := errBuf.String()
	if strings.Contains(errOut, "\x1b") {
		t.Fatalf("handleGet printed raw escape sequence in HTTP error body: %q", errOut)
	}
	if !strings.Contains(errOut, `\x1b[2J\x1b[31minternal server failure\x1b[0m`) {
		t.Fatalf("handleGet missing expected sanitized HTTP error body: %q", errOut)
	}
}
