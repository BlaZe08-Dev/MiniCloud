package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

// TestReservationCap_FitsAcceptWindow: ADR-0020 Section 2.3, a worker can claim every reservation
// it may hold inside the accept window even if one poll error backoff and one idle poll sleep per
// claim fall inside the burst: MaxPollBackoff + MaxReservationsPerWorker x IdlePollInterval <
// MinAcceptWindow (3 s + 8 x 200 ms = 4.6 s < 5 s).
func TestReservationCap_FitsAcceptWindow(t *testing.T) {
	burst := worker.MaxPollBackoff + time.Duration(domain.MaxReservationsPerWorker)*worker.IdlePollInterval
	if burst >= coordinator.MinAcceptWindow {
		t.Fatalf("claim burst bound %v (max backoff %v + %d x idle poll %v) does not fit the accept window floor %v",
			burst, worker.MaxPollBackoff, domain.MaxReservationsPerWorker, worker.IdlePollInterval, coordinator.MinAcceptWindow)
	}
	t.Logf("claim burst bound %v, accept window floor %v, slack %v", burst, coordinator.MinAcceptWindow, coordinator.MinAcceptWindow-burst)
}

// TestRegister_SendsExecutesConcurrently: ADR-0020 Section 2.6, the worker's register body carries
// "executes_concurrently": true, so a V2-0b coordinator gives it total_slots assignable slots. The
// registration is reached through a heartbeat 404, the re-register path.
func TestRegister_SendsExecutesConcurrently(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v1/workers/register":
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(raw, &body)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"REGISTERED"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "w-reg", CoordinatorURL: ts.URL, HeartbeatInterval: time.Second, TotalSlots: 4}, executor.NewRegistry())
	if err := worker.SendHeartbeatForTest(context.Background(), agent); err != nil {
		t.Fatalf("heartbeat with re-register: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if body == nil {
		t.Fatal("no register request reached the coordinator")
	}
	if v, ok := body["executes_concurrently"].(bool); !ok || !v {
		t.Fatalf("register body executes_concurrently = %#v, want true", body["executes_concurrently"])
	}
	if v, _ := body["total_slots"].(float64); v != 4 {
		t.Fatalf("register body total_slots = %v, want 4", body["total_slots"])
	}
}
