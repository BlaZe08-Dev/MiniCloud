package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// TestWorkerLiveness_DeadWorkerReapedAndWorkContinues verifies that when a worker process
// dies abruptly (SIGKILL), the coordinator reaps it as UNHEALTHY after the timeout,
// excludes it from scheduling, and routes new jobs exclusively to surviving healthy workers.
func TestWorkerLiveness_DeadWorkerReapedAndWorkContinues(t *testing.T) {
	// 1. Coordinator configuration with short timings
	workerTimeout := 1 * time.Second
	reaperInterval := 300 * time.Millisecond
	heartbeatInterval := "200ms"

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	coordAddr := listener.Addr().String()
	coordURL := "http://" + coordAddr

	repo := storage.NewInMemoryRepository()
	sched := scheduler.NewRoundRobinScheduler()
	reg := executor.NewRegistry()
	svc := coordinator.NewService(repo, sched, reg, workerTimeout)
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

	// Start reaper loop
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	defer reaperCancel()
	go svc.StartReaper(reaperCtx, reaperInterval)

	// 2. Build Worker binary
	tmpDir := t.TempDir()
	workerBin := filepath.Join(tmpDir, "worker")
	buildCmd := exec.Command("go", "build", "-o", workerBin, "../../cmd/worker")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build worker binary: %v", err)
	}

	// 3. Launch Worker 1 (to be killed) and Worker 2 (survivor)
	worker1ID := "worker-liveness-1"
	worker2ID := "worker-liveness-2"

	worker1Cmd := exec.Command(workerBin)
	worker1Cmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID="+worker1ID,
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL="+heartbeatInterval,
	)
	if err := worker1Cmd.Start(); err != nil {
		t.Fatalf("failed to start worker 1: %v", err)
	}
	defer func() {
		if worker1Cmd.Process != nil {
			_ = worker1Cmd.Process.Kill()
		}
	}()

	worker2Cmd := exec.Command(workerBin)
	worker2Cmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID="+worker2ID,
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL="+heartbeatInterval,
	)
	if err := worker2Cmd.Start(); err != nil {
		t.Fatalf("failed to start worker 2: %v", err)
	}
	defer func() {
		if worker2Cmd.Process != nil {
			_ = worker2Cmd.Process.Kill()
		}
	}()

	client := &http.Client{Timeout: 3 * time.Second}

	// 4. Poll until both workers are registered and HEALTHY
	registrationDeadline := time.Now().Add(5 * time.Second)
	bothRegistered := false
	for time.Now().Before(registrationDeadline) {
		resp, err := client.Get(coordURL + "/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var wRes struct {
				Workers []*domain.Worker `json:"workers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&wRes)
			resp.Body.Close()

			var hasW1, hasW2 bool
			for _, w := range wRes.Workers {
				if w.ID == worker1ID && w.Status == domain.WorkerStatusHealthy {
					hasW1 = true
				}
				if w.ID == worker2ID && w.Status == domain.WorkerStatusHealthy {
					hasW2 = true
				}
			}
			if hasW1 && hasW2 {
				bothRegistered = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !bothRegistered {
		t.Fatalf("timed out waiting for both workers to register and be HEALTHY")
	}

	// 5. Abruptly kill Worker 1 via SIGKILL
	if err := worker1Cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("failed to send SIGKILL to worker 1: %v", err)
	}
	_ = worker1Cmd.Wait() // Reaped by OS

	// 6. Poll with deadline until coordinator reaper marks Worker 1 UNHEALTHY
	reapDeadline := time.Now().Add(5 * time.Second)
	worker1Reaped := false
	for time.Now().Before(reapDeadline) {
		resp, err := client.Get(coordURL + "/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var wRes struct {
				Workers []*domain.Worker `json:"workers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&wRes)
			resp.Body.Close()

			for _, w := range wRes.Workers {
				if w.ID == worker1ID && w.Status == domain.WorkerStatusUnhealthy {
					worker1Reaped = true
				}
			}
			if worker1Reaped {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !worker1Reaped {
		t.Fatalf("worker 1 was not reaped to UNHEALTHY within deadline")
	}

	// 7. Verify Worker 2 remains HEALTHY
	resp, err := client.Get(coordURL + "/v1/workers")
	if err != nil {
		t.Fatalf("failed to query workers: %v", err)
	}
	var wRes struct {
		Workers []*domain.Worker `json:"workers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&wRes)
	resp.Body.Close()

	var worker2Healthy bool
	for _, w := range wRes.Workers {
		if w.ID == worker2ID && w.Status == domain.WorkerStatusHealthy {
			worker2Healthy = true
		}
	}
	if !worker2Healthy {
		t.Fatalf("expected worker 2 to remain HEALTHY")
	}

	// 8. Submit new jobs AFTER the kill and assert only on those
	var submittedJobIDs []string
	for i := 1; i <= 2; i++ {
		jobReq := map[string]any{
			"task_type": "echo",
			"payload": map[string]string{
				"message": "job after worker 1 death",
			},
			"timeout_seconds": 10,
			"max_attempts":    3,
		}
		body, _ := json.Marshal(jobReq)
		subResp, err := client.Post(coordURL+"/v1/jobs", "application/json", bytes.NewReader(body))
		if err != nil || subResp.StatusCode != http.StatusCreated {
			t.Fatalf("failed to submit post-kill job %d: status=%v, err=%v", i, subResp.StatusCode, err)
		}
		var job domain.Job
		_ = json.NewDecoder(subResp.Body).Decode(&job)
		subResp.Body.Close()
		submittedJobIDs = append(submittedJobIDs, job.ID)
	}

	// 9. Poll with deadline until all new jobs succeed
	jobsDeadline := time.Now().Add(6 * time.Second)
	allSucceeded := false
	for time.Now().Before(jobsDeadline) {
		completed := 0
		for _, jid := range submittedJobIDs {
			getResp, err := client.Get(coordURL + "/v1/jobs/" + jid)
			if err == nil && getResp.StatusCode == http.StatusOK {
				var job domain.Job
				_ = json.NewDecoder(getResp.Body).Decode(&job)
				getResp.Body.Close()
				if job.State == domain.StateSucceeded {
					completed++
				}
			}
		}
		if completed == len(submittedJobIDs) {
			allSucceeded = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !allSucceeded {
		t.Fatalf("not all new jobs succeeded within deadline")
	}

	// 10. Verify attempts for new jobs were executed exclusively by the surviving worker (worker 2)
	for _, jid := range submittedJobIDs {
		attResp, err := client.Get(coordURL + "/v1/jobs/" + jid + "/attempts")
		if err != nil || attResp.StatusCode != http.StatusOK {
			t.Fatalf("failed to get attempts for job %s: status=%v, err=%v", jid, attResp.StatusCode, err)
		}
		var attRes struct {
			Attempts []*domain.Attempt `json:"attempts"`
		}
		_ = json.NewDecoder(attResp.Body).Decode(&attRes)
		attResp.Body.Close()

		if len(attRes.Attempts) == 0 {
			t.Fatalf("no attempts found for job %s", jid)
		}
		for _, att := range attRes.Attempts {
			if att.WorkerID != worker2ID {
				t.Fatalf("job %s was executed by %s, expected exclusively %s", jid, att.WorkerID, worker2ID)
			}
		}
	}
}
