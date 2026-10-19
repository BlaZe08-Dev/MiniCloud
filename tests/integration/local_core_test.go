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

func TestLocalCore_MultiProcessIntegration(t *testing.T) {
	// 1. Determine free local port for Coordinator
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	coordAddr := listener.Addr().String()
	coordURL := "http://" + coordAddr

	// 2. Start in-process Coordinator HTTP Server
	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg)
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

	// 4. Start TWO independent worker processes
	worker1Cmd := exec.Command(workerBin)
	worker1Cmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID=worker-integration-1",
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL=500ms",
	)
	if err := worker1Cmd.Start(); err != nil {
		t.Fatalf("failed to start worker 1: %v", err)
	}
	defer func() { _ = worker1Cmd.Process.Kill() }()

	worker2Cmd := exec.Command(workerBin)
	worker2Cmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID=worker-integration-2",
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL=500ms",
	)
	if err := worker2Cmd.Start(); err != nil {
		t.Fatalf("failed to start worker 2: %v", err)
	}
	defer func() { _ = worker2Cmd.Process.Kill() }()

	// 5. Wait for both workers to register with coordinator
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

			if len(wRes.Workers) >= 2 {
				workersRegistered = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !workersRegistered {
		t.Fatalf("timed out waiting for 2 workers to register with coordinator")
	}

	// 6. Submit multiple jobs across different task types
	jobRequests := []map[string]any{
		{
			"task_type": "echo",
			"payload":   map[string]string{"message": "hello-distributed-1"},
		},
		{
			"task_type": "sleep",
			"payload":   map[string]int{"duration_ms": 100},
		},
		{
			"task_type": "sha256_loop",
			"payload":   map[string]any{"iterations": 1000, "input": "minicloud-bench"},
		},
		{
			"task_type": "echo",
			"payload":   map[string]string{"message": "hello-distributed-4"},
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

	// 7. Poll and assert that all jobs reach SUCCEEDED with valid results
	assignedWorkers := make(map[string]bool)
	jobDeadline := time.Now().Add(15 * time.Second)

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
						assignedWorkers[lastAttempt.WorkerID] = true
					}
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}

		if !succeeded {
			// Query last state for failure reporting
			resp, _ := client.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, jobID))
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("job %s did not reach SUCCEEDED within deadline. Last response: %s", jobID, string(body))
		}
	}

	// 8. Assert work was distributed across BOTH worker processes
	if len(assignedWorkers) < 2 {
		t.Fatalf("expected jobs to be distributed across both workers, but only observed: %v", assignedWorkers)
	}
	t.Logf("multi-process integration test verified across workers: %v", assignedWorkers)
}
