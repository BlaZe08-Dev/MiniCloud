package worker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

// TestAgent_Run_ProductionPathStartsRenewals drives the entry point cmd/worker uses (Agent.Run:
// register, poll, execute) with the real clock and the real sleep task. The coordinator advertises
// a 3 s lease, so the worker must renew every second while the 2.5 s task runs, then deliver.
func TestAgent_Run_ProductionPathStartsRenewals(t *testing.T) {
	var renews, results, polls atomic.Int64
	var mu sync.Mutex
	var renewPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workers/register", r.URL.Path == "/v1/workers/w-prod/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
		case r.URL.Path == "/v1/workers/w-prod/poll":
			if polls.Add(1) > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"assignment": coordinator.WorkAssignment{
				JobID: "job-p", AttemptID: "att-p", AttemptNo: 1, LeaseToken: "tok-p", TaskType: "sleep",
				Payload: json.RawMessage(`{"duration_ms":2500}`), TimeoutSeconds: 30, LeaseDurationSeconds: 3}})
		case r.URL.Path == "/v1/jobs/job-p/attempts/att-p/renew":
			renews.Add(1)
			mu.Lock()
			renewPath = r.URL.Path
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "RENEWED", "cancel_requested": false})
		case r.URL.Path == "/v1/jobs/job-p/attempts/att-p/result":
			results.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "w-prod", CoordinatorURL: ts.URL, HeartbeatInterval: time.Second, TotalSlots: 1}, executor.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = agent.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for results.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if results.Load() != 1 {
		t.Fatalf("results = %d, want 1", results.Load())
	}
	if renews.Load() < 2 {
		t.Fatalf("renewals = %d through Agent.Run, want at least 2 during a 2.5 s task on a 3 s lease", renews.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if renewPath != "/v1/jobs/job-p/attempts/att-p/renew" {
		t.Fatalf("renewal path = %q", renewPath)
	}
}
