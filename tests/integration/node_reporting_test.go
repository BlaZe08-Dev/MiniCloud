package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

// nrStart runs a real coordinator (in-memory storage) behind a real API server and one worker.
func nrStart(t *testing.T, localOnly bool, cfg config.WorkerConfig) (url string, stop func()) {
	t.Helper()
	svc := coordinator.NewService(storage.NewInMemoryRepository(), scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc, api.WithLocalOnly(localOnly)).Handler())
	cfg.WorkerID, cfg.CoordinatorURL, cfg.TotalSlots, cfg.HeartbeatInterval = "node-it-worker", ts.URL, 1, 200*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.NewAgent(&cfg, executor.NewRegistry()).Run(ctx) }()
	return ts.URL, func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned %v", err)
		}
		ts.Close()
	}
}

func nrGet(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func nrWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNodeReporting_LocalOnlyAttachesAndLegacyWithout(t *testing.T) {
	t.Run("local-only coordinator: node registered, attached, telemetry sent", func(t *testing.T) {
		url, stop := nrStart(t, true, config.WorkerConfig{
			NodeReportingEnabled: true, NodeStateDir: filepath.Join(t.TempDir(), "state"), NodeTelemetryInterval: 50 * time.Millisecond})
		defer stop()
		var workers struct {
			Workers []map[string]any `json:"workers"`
		}
		var nodeID string
		nrWait(t, "the worker to show its node_id", func() bool {
			workers.Workers = nil
			nrGet(t, url+"/v1/workers", &workers)
			if len(workers.Workers) == 1 {
				nodeID, _ = workers.Workers[0]["node_id"].(string)
			}
			return nodeID != ""
		})
		if err := domain.ValidateNodeID(nodeID); err != nil {
			t.Fatal(err)
		}
		var node map[string]any
		nrWait(t, "a telemetry block", func() bool {
			node = nil
			nrGet(t, url+"/v1/nodes/"+nodeID, &node)
			return node["telemetry"] != nil
		})
		ids, _ := node["worker_ids"].([]any)
		if len(ids) != 1 || ids[0] != "node-it-worker" {
			t.Fatalf("worker_ids = %v", node["worker_ids"])
		}
		if node["hardware"] == nil {
			t.Fatalf("no hardware in %v", node)
		}
	})

	t.Run("same worker against a coordinator that is not local-only: legacy, job runs", func(t *testing.T) {
		url, stop := nrStart(t, false, config.WorkerConfig{
			NodeReportingEnabled: true, NodeStateDir: filepath.Join(t.TempDir(), "state"), NodeTelemetryInterval: 50 * time.Millisecond})
		defer stop()
		body, _ := json.Marshal(map[string]any{"task_type": "echo", "payload": map[string]string{"message": "hi"}})
		nrWait(t, "the worker to register", func() bool {
			var w struct {
				Workers []map[string]any `json:"workers"`
			}
			nrGet(t, url+"/v1/workers", &w)
			return len(w.Workers) == 1
		})
		resp, err := http.Post(url+"/v1/jobs", "application/json", bytes.NewReader(body))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("submit: %v %v", resp, err)
		}
		var job domain.Job
		_ = json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		nrWait(t, "the job to succeed", func() bool {
			var j domain.Job
			nrGet(t, url+"/v1/jobs/"+job.ID, &j)
			return j.State == domain.StateSucceeded
		})
		var raw struct {
			Workers []map[string]any `json:"workers"`
		}
		nrGet(t, url+"/v1/workers", &raw)
		if _, has := raw.Workers[0]["node_id"]; has {
			t.Fatalf("GET /v1/workers has a node_id key: %v", raw.Workers[0])
		}
		if code := nrGet(t, url+"/v1/nodes", nil); code != http.StatusNotFound {
			t.Fatalf("GET /v1/nodes = %d, want 404", code)
		}
	})
}
