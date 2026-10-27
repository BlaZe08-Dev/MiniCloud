package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

func TestThreeWorkerBatch_Integration(t *testing.T) {
	// 1. Determine free local port for Coordinator
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	coordAddr := listener.Addr().String()
	coordURL := "http://" + coordAddr

	// 2. Start in-process Coordinator HTTP Server using NewService
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, 10*time.Second)
	apiServer := api.NewServer(coordAddr, svc)

	httpServer := &http.Server{
		Handler: apiServer.Handler(),
	}

	go func() {
		_ = httpServer.Serve(listener)
	}()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	// 3. Compile Worker binary into temp directory
	tmpDir := t.TempDir()
	workerBin := filepath.Join(tmpDir, "worker")
	buildCmd := exec.Command("go", "build", "-o", workerBin, "../../cmd/worker")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build worker binary for integration test: %v", err)
	}

	// 4. Start THREE independent worker processes
	workerCmds := make([]*exec.Cmd, 3)
	for i := 1; i <= 3; i++ {
		workerID := fmt.Sprintf("worker-batch-%d", i)
		cmd := exec.Command(workerBin)
		cmd.Env = append(os.Environ(),
			"MINICLOUD_COORDINATOR_URL="+coordURL,
			"MINICLOUD_WORKER_ID="+workerID,
			"MINICLOUD_WORKER_SLOTS=2",
			"MINICLOUD_HEARTBEAT_INTERVAL=300ms",
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start %s: %v", workerID, err)
		}
		workerCmds[i-1] = cmd
		defer func(c *exec.Cmd) {
			_ = c.Process.Kill()
		}(cmd)
	}

	// 5. Wait for all 3 workers to register with coordinator
	client := &http.Client{Timeout: 5 * time.Second}
	workersRegistered := false
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get(coordURL + "/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var wRes struct {
				Workers []*domain.Worker `json:"workers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&wRes)
			resp.Body.Close()

			if len(wRes.Workers) >= 3 {
				workersRegistered = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !workersRegistered {
		t.Fatalf("timed out waiting for 3 workers to register with coordinator")
	}

	// 6. Submit a batch of 6 jobs
	jobRequests := []map[string]any{
		{
			"task_type": "echo",
			"payload":   map[string]string{"message": "batch-1"},
		},
		{
			"task_type": "sleep",
			"payload":   map[string]int{"duration_ms": 100},
		},
		{
			"task_type": "sha256_loop",
			"payload":   map[string]any{"iterations": 500, "input": "batch-3"},
		},
		{
			"task_type": "echo",
			"payload":   map[string]string{"message": "batch-4"},
		},
		{
			"task_type": "sleep",
			"payload":   map[string]int{"duration_ms": 100},
		},
		{
			"task_type": "sha256_loop",
			"payload":   map[string]any{"iterations": 500, "input": "batch-6"},
		},
	}

	var submittedJobIDs []string
	for _, reqData := range jobRequests {
		payloadBytes, _ := json.Marshal(reqData)
		resp, err := client.Post(coordURL+"/v1/jobs", "application/json", bytes.NewReader(payloadBytes))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("failed to submit job: %v, status: %d", err, resp.StatusCode)
		}
		var job domain.Job
		_ = json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		submittedJobIDs = append(submittedJobIDs, job.ID)
	}

	// 7. Poll and assert that all 6 jobs reach SUCCEEDED with valid results
	assignedWorkers := make(map[string]int)
	jobDeadline := time.Now().Add(20 * time.Second)

	for _, jobID := range submittedJobIDs {
		succeeded := false
		for time.Now().Before(jobDeadline) {
			resp, err := client.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
			if err == nil && resp.StatusCode == http.StatusOK {
				var j domain.Job
				_ = json.NewDecoder(resp.Body).Decode(&j)
				resp.Body.Close()

				if j.State == domain.StateSucceeded {
					succeeded = true
					if j.Result == nil || j.Result.ExitCode != 0 || j.Result.Output == "" {
						t.Fatalf("job %s succeeded but has invalid result: %+v", jobID, j.Result)
					}

					// Verify attempt records
					attResp, err := client.Get(fmt.Sprintf("%s/v1/jobs/%s/attempts", coordURL, jobID))
					if err == nil && attResp.StatusCode == http.StatusOK {
						var aRes struct {
							Attempts []*domain.Attempt `json:"attempts"`
						}
						_ = json.NewDecoder(attResp.Body).Decode(&aRes)
						attResp.Body.Close()

						if len(aRes.Attempts) == 0 {
							t.Fatalf("job %s has no attempt records", jobID)
						}
						lastAttempt := aRes.Attempts[len(aRes.Attempts)-1]
						if lastAttempt.State != domain.AttemptCompleted {
							t.Fatalf("attempt state expected COMPLETED, got %s", lastAttempt.State)
						}
						assignedWorkers[lastAttempt.WorkerID]++
					}
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}

		if !succeeded {
			resp, _ := client.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("job %s did not reach SUCCEEDED within deadline. Last response: %s", jobID, string(body))
		}
	}

	// 8. Assert work was distributed across > 1 worker process
	if len(assignedWorkers) <= 1 {
		t.Fatalf("expected jobs to be distributed across > 1 worker, but only observed: %v", assignedWorkers)
	}
	t.Logf("batch execution across 3 workers verified successfully: %v", assignedWorkers)
}
