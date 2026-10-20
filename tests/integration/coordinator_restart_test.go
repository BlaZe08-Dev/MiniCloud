package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func listenWithReuse(addr string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			err := c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			if err != nil {
				return err
			}
			return opErr
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}

// TestWorker_CoordinatorRestart_AutoReRegistrationAndCompletion verifies Scope 5 & Adjustment 6:
// 1. Worker is running against coordinator 1 and claims a job.
// 2. Coordinator 1 is shut down and restarted as coordinator 2 with a fresh in-memory repository
//    (simulating process restart and in-memory state loss before Slice 3c PostgreSQL durability).
// 3. When worker reports the in-flight job outcome to restarted coordinator 2, it receives HTTP 404.
//    Worker classifies 404 as non-retryable, logs warning, and does NOT loop forever.
// 4. On subsequent heartbeat or poll, worker receives HTTP 404 (WORKER_NOT_FOUND), automatically
//    re-registers itself with coordinator 2, and continues polling.
// 5. A newly submitted job to coordinator 2 is claimed by the re-registered worker and completed successfully.
//
// NOTE (Slice 3a Invariant): In-memory coordinator state (including in-flight jobs submitted before restart)
// is lost on process restart until Slice 3c brings durable PostgreSQL storage. This test does NOT assert
// recovery of pre-restart jobs.
func TestWorker_CoordinatorRestart_AutoReRegistrationAndCompletion(t *testing.T) {
	// 1. Obtain an ephemeral port with SO_REUSEADDR
	initListener, err := listenWithReuse("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	coordAddr := initListener.Addr().String()
	coordURL := "http://" + coordAddr

	// 2. Start Coordinator 1
	repo1 := storage.NewInMemoryRepository()
	sched1 := scheduler.NewRoundRobinScheduler()
	reg1 := executor.NewRegistry()
	svc1 := coordinator.NewService(repo1, sched1, reg1, 5*time.Second)
	apiServer1 := api.NewServer(coordAddr, svc1)
	httpServer1 := &http.Server{Handler: apiServer1.Handler()}

	go func() {
		_ = httpServer1.Serve(initListener)
	}()

	// 3. Build Worker binary
	tmpDir := t.TempDir()
	workerBin := filepath.Join(tmpDir, "worker")
	buildCmd := exec.Command("go", "build", "-o", workerBin, "../../cmd/worker")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build worker binary: %v", err)
	}

	// 4. Launch Worker Process (surviving process across coordinator restart)
	workerID := "surviving-worker-1"
	workerCmd := exec.Command(workerBin)
	workerCmd.Env = append(os.Environ(),
		"MINICLOUD_COORDINATOR_URL="+coordURL,
		"MINICLOUD_WORKER_ID="+workerID,
		"MINICLOUD_WORKER_SLOTS=2",
		"MINICLOUD_HEARTBEAT_INTERVAL=100ms",
	)
	if err := workerCmd.Start(); err != nil {
		t.Fatalf("failed to start worker process: %v", err)
	}
	defer func() {
		if workerCmd.Process != nil {
			_ = workerCmd.Process.Kill()
		}
	}()

	// 5. Wait for worker to register with Coordinator 1
	client := &http.Client{Timeout: 5 * time.Second}
	var registered bool
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		resp, err := client.Get(coordURL + "/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var result struct {
				Workers []domain.Worker `json:"workers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&result)
			_ = resp.Body.Close()
			for _, w := range result.Workers {
				if w.ID == workerID {
					registered = true
					break
				}
			}
			if registered {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !registered {
		t.Fatalf("worker failed to register with coordinator 1 within 5s")
	}

	// 6. Submit in-flight job to Coordinator 1 (sleep 1s so it is actively in-flight during restart)
	reqBody, _ := json.Marshal(map[string]any{
		"task_type": "sleep",
		"payload":   map[string]any{"duration_ms": 1000},
	})
	resp, err := client.Post(coordURL+"/v1/jobs", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to submit in-flight job: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to submit in-flight job, status %d", resp.StatusCode)
	}
	var inFlightJob domain.Job
	_ = json.NewDecoder(resp.Body).Decode(&inFlightJob)
	_ = resp.Body.Close()

	if inFlightJob.ID == "" {
		t.Fatalf("failed to obtain job ID for in-flight job")
	}

	// Wait for worker to claim the job (job state becomes RUNNING)
	claimed := false
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		resp, err := client.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, inFlightJob.ID))
		if err == nil && resp.StatusCode == http.StatusOK {
			var cur domain.Job
			_ = json.NewDecoder(resp.Body).Decode(&cur)
			_ = resp.Body.Close()
			if cur.State == domain.StateRunning {
				claimed = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !claimed {
		t.Fatalf("in-flight job was not claimed by worker in coordinator 1")
	}

	// 7. Shut down Coordinator 1
	_ = httpServer1.Shutdown(context.Background())
	time.Sleep(100 * time.Millisecond)

	// 8. Start Coordinator 2 on the exact same port with a fresh in-memory repository (state reset)
	listener2, err := listenWithReuse(coordAddr)
	if err != nil {
		t.Fatalf("failed to bind listener for restarted coordinator 2: %v", err)
	}
	repo2 := storage.NewInMemoryRepository()
	sched2 := scheduler.NewRoundRobinScheduler()
	reg2 := executor.NewRegistry()
	svc2 := coordinator.NewService(repo2, sched2, reg2, 5*time.Second)
	apiServer2 := api.NewServer(coordAddr, svc2)
	httpServer2 := &http.Server{Handler: apiServer2.Handler()}

	go func() {
		_ = httpServer2.Serve(listener2)
	}()
	defer func() {
		_ = httpServer2.Shutdown(context.Background())
	}()

	// 9. Verify worker re-registers with restarted Coordinator 2
	// The worker's result report for the pre-restart job will fail with 404 (non-retryable).
	// Its heartbeat or poll will fail with 404 (WORKER_NOT_FOUND) and trigger automatic re-registration.
	reregistered := false
	for start := time.Now(); time.Since(start) < 8*time.Second; {
		resp, err := client.Get(coordURL + "/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var result struct {
				Workers []domain.Worker `json:"workers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&result)
			_ = resp.Body.Close()
			for _, w := range result.Workers {
				if w.ID == workerID && w.Status == domain.WorkerStatusHealthy {
					reregistered = true
					break
				}
			}
			if reregistered {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !reregistered {
		t.Fatalf("worker failed to re-register with restarted coordinator 2 within 8s")
	}

	// 10. Submit a new job to Coordinator 2
	reqBody2, _ := json.Marshal(map[string]any{
		"task_type": "echo",
		"payload":   map[string]string{"message": "hello-after-restart"},
	})
	resp2, err := client.Post(coordURL+"/v1/jobs", "application/json", bytes.NewReader(reqBody2))
	if err != nil {
		t.Fatalf("failed to submit new job to coordinator 2: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("failed to submit new job to coordinator 2, status %d", resp2.StatusCode)
	}
	var newJob domain.Job
	_ = json.NewDecoder(resp2.Body).Decode(&newJob)
	_ = resp2.Body.Close()

	if newJob.ID == "" {
		t.Fatalf("failed to obtain job ID for new job")
	}

	// 11. Assert that the re-registered worker claims and completes the new job
	newJobCompleted := false
	for start := time.Now(); time.Since(start) < 8*time.Second; {
		resp, err := client.Get(fmt.Sprintf("%s/v1/jobs/%s", coordURL, newJob.ID))
		if err == nil && resp.StatusCode == http.StatusOK {
			var cur domain.Job
			_ = json.NewDecoder(resp.Body).Decode(&cur)
			_ = resp.Body.Close()
			if cur.State == domain.StateSucceeded {
				newJobCompleted = true
				if cur.Result == nil || cur.Result.ExitCode != 0 {
					t.Fatalf("unexpected result: %+v", cur.Result)
				}
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !newJobCompleted {
		t.Fatalf("new job submitted after restart was not completed by re-registered worker")
	}
}
