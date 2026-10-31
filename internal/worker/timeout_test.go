package worker_test

import (
	"context"
	"encoding/json"
	"math"
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

func TestClampExecutionTimeout(t *testing.T) {
	overflowCase := int(math.MaxInt64/int64(time.Second)) + 1

	tests := []struct {
		name     string
		input    int
		expected time.Duration
	}{
		{
			name:     "zero_clamped_to_min",
			input:    0,
			expected: 1 * time.Second,
		},
		{
			name:     "negative_clamped_to_min",
			input:    -1,
			expected: 1 * time.Second,
		},
		{
			name:     "minimum_valid_unchanged",
			input:    1,
			expected: 1 * time.Second,
		},
		{
			name:     "maximum_valid_unchanged",
			input:    86400,
			expected: 86400 * time.Second,
		},
		{
			name:     "above_maximum_clamped_to_max",
			input:    86401,
			expected: 86400 * time.Second,
		},
		{
			name:     "max_int32_clamped_to_max",
			input:    math.MaxInt32,
			expected: 86400 * time.Second,
		},
		{
			name:     "max_int64_clamped_to_max",
			input:    math.MaxInt64,
			expected: 86400 * time.Second,
		},
		{
			name:     "multiplication_overflow_clamped_to_max",
			input:    overflowCase,
			expected: 86400 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := worker.ClampExecutionTimeout(tc.input)
			if got != tc.expected {
				t.Fatalf("ClampExecutionTimeout(%d) = %v; want %v", tc.input, got, tc.expected)
			}
		})
	}
}

type deadlineObserverTask struct {
	inner       executor.Task
	deadline    time.Time
	hasDeadline bool
	mu          sync.Mutex
}

func (o *deadlineObserverTask) Name() string {
	return o.inner.Name()
}

func (o *deadlineObserverTask) Validate(payload []byte) error {
	return o.inner.Validate(payload)
}

func (o *deadlineObserverTask) Execute(ctx context.Context, payload []byte) (string, error) {
	if dl, ok := ctx.Deadline(); ok {
		o.mu.Lock()
		o.deadline = dl
		o.hasDeadline = true
		o.mu.Unlock()
	}
	return o.inner.Execute(ctx, payload)
}

func (o *deadlineObserverTask) observed() (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.deadline, o.hasDeadline
}

func TestWorker_ExcessiveTimeoutClamped(t *testing.T) {
	var polls, results atomic.Int64
	// 10 years in seconds: 10 * 365 * 86400 = 315,360,000 seconds
	const tenYearsSeconds = 10 * 365 * 86400

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workers/register", r.URL.Path == "/v1/workers/w-excess/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
		case r.URL.Path == "/v1/workers/w-excess/poll":
			if polls.Add(1) > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"assignment": coordinator.WorkAssignment{
					JobID:                "job-excess",
					AttemptID:            "att-excess",
					AttemptNo:            1,
					LeaseToken:           "tok-excess",
					TaskType:             "sleep",
					Payload:              json.RawMessage(`{"duration_ms":10}`),
					TimeoutSeconds:       tenYearsSeconds,
					LeaseDurationSeconds: 30,
				},
			})
		case r.URL.Path == "/v1/jobs/job-excess/attempts/att-excess/renew":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "RENEWED", "cancel_requested": false})
		case r.URL.Path == "/v1/jobs/job-excess/attempts/att-excess/result":
			results.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	obs := &deadlineObserverTask{inner: &executor.SleepTask{}}
	reg := executor.NewRegistry()
	reg.Register(obs)

	cfg := &config.WorkerConfig{
		WorkerID:          "w-excess",
		CoordinatorURL:    ts.URL,
		HeartbeatInterval: time.Second,
		TotalSlots:        1,
	}
	agent := worker.NewAgent(cfg, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = agent.Run(ctx)
	}()

	waitDeadline := time.Now().Add(5 * time.Second)
	for results.Load() == 0 && time.Now().Before(waitDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if results.Load() != 1 {
		t.Fatalf("results = %d, want 1", results.Load())
	}

	dl, hasDL := obs.observed()
	if !hasDL {
		t.Fatal("task was executed without a context deadline")
	}

	// Execution deadline must be at most 86400 s (MaxExecutionTimeoutSeconds) from start.
	// Allow small 5 s margin for test scheduling latency.
	maxAllowedDeadline := start.Add(worker.MaxExecutionTimeout + 5*time.Second)
	if dl.After(maxAllowedDeadline) {
		t.Fatalf("observed execution deadline %v exceeds maximum allowed %v (remaining: %v, max: %v)",
			dl, maxAllowedDeadline, dl.Sub(start), worker.MaxExecutionTimeout)
	}
}
