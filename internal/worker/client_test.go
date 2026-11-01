package worker_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

func TestIsNonRetryableReportError(t *testing.T) {
	tests := []struct {
		statusCode     int
		isNonRetryable bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusForbidden, true},            // 403 WORKER_MISMATCH
		{http.StatusNotFound, true},             // 404 NOT_FOUND (restarted coordinator missing in-memory job/attempt)
		{http.StatusConflict, true},             // 409 STALE_ATTEMPT_REJECTED / JOB_ALREADY_TERMINAL
		{http.StatusInternalServerError, false}, // 500 transient
		{http.StatusBadGateway, false},          // 502 transient
		{http.StatusServiceUnavailable, false},  // 503 transient
		{http.StatusGatewayTimeout, false},      // 504 transient
	}

	for _, tt := range tests {
		got := worker.IsNonRetryableReportError(tt.statusCode)
		if got != tt.isNonRetryable {
			t.Errorf("IsNonRetryableReportError(%d) = %v, want %v", tt.statusCode, got, tt.isNonRetryable)
		}
	}
}

func TestAgent_ReportResult_RejectionHandling(t *testing.T) {
	tests := []struct {
		name               string
		serverStatus       int
		expectErr          bool
		expectNonRetryable bool
	}{
		{
			name:               "200 OK success",
			serverStatus:       http.StatusOK,
			expectErr:          false,
			expectNonRetryable: false,
		},
		{
			name:               "403 Forbidden worker mismatch",
			serverStatus:       http.StatusForbidden,
			expectErr:          true,
			expectNonRetryable: true,
		},
		{
			name:               "404 Not Found restarted coordinator without durable state",
			serverStatus:       http.StatusNotFound,
			expectErr:          true,
			expectNonRetryable: true,
		},
		{
			name:               "409 Conflict stale attempt",
			serverStatus:       http.StatusConflict,
			expectErr:          true,
			expectNonRetryable: true,
		},
		{
			name:               "500 Internal Server Error retryable",
			serverStatus:       http.StatusInternalServerError,
			expectErr:          true,
			expectNonRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.serverStatus)
			}))
			defer ts.Close()

			cfg := &config.WorkerConfig{
				WorkerID:          "worker-test",
				CoordinatorURL:    ts.URL,
				HeartbeatInterval: 1 * time.Second,
				TotalSlots:        1,
			}
			reg := executor.NewRegistry()
			agent := worker.NewAgent(cfg, reg)

			status, err := agent.ReportResult(context.Background(), "job-1", "att-1", "lease-1", 0, "out", "", "")
			if status != tt.serverStatus {
				t.Fatalf("expected status %d, got %d", tt.serverStatus, status)
			}

			if tt.expectErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.expectNonRetryable != worker.IsNonRetryableReportError(status) {
				t.Fatalf("IsNonRetryableReportError(%d) = %v, want %v", status, worker.IsNonRetryableReportError(status), tt.expectNonRetryable)
			}
		})
	}
}

func TestAgent_Run_WorkerUnhealthy_KeepsPollingAndHeartbeating(t *testing.T) {
	var pollCount atomic.Int64
	var heartbeatCount atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workers/register":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/workers/test-worker/heartbeat":
			heartbeatCount.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/workers/test-worker/poll":
			pollCount.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"WORKER_UNHEALTHY","message":"worker is unhealthy"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cfg := &config.WorkerConfig{
		WorkerID:          "test-worker",
		CoordinatorURL:    ts.URL,
		HeartbeatInterval: 25 * time.Millisecond,
		TotalSlots:        1,
	}
	reg := executor.NewRegistry()
	agent := worker.NewAgent(cfg, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := agent.Run(ctx)
	if err != nil {
		t.Fatalf("expected Agent.Run to exit cleanly on context cancellation, got: %v", err)
	}

	if pollCount.Load() < 2 {
		t.Fatalf("expected agent to keep polling despite WORKER_UNHEALTHY, got %d polls", pollCount.Load())
	}
	if heartbeatCount.Load() < 2 {
		t.Fatalf("expected agent to keep heartbeating despite WORKER_UNHEALTHY, got %d heartbeats", heartbeatCount.Load())
	}
}

func TestAgent_AutoReRegistration_On404HeartbeatAndPoll(t *testing.T) {
	var registerCount atomic.Int64
	var heartbeatCount atomic.Int64
	var pollCount atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workers/register":
			registerCount.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/workers/rereg-worker/heartbeat":
			count := heartbeatCount.Add(1)
			if count == 1 {
				// First heartbeat returns 404 WORKER_NOT_FOUND to trigger re-registration
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"WORKER_NOT_FOUND","message":"worker not found"}}`))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		case r.URL.Path == "/v1/workers/rereg-worker/poll":
			count := pollCount.Add(1)
			if count == 1 {
				// First poll returns 404 WORKER_NOT_FOUND to trigger re-registration
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"WORKER_NOT_FOUND","message":"worker not found"}}`))
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cfg := &config.WorkerConfig{
		WorkerID:          "rereg-worker",
		CoordinatorURL:    ts.URL,
		HeartbeatInterval: 25 * time.Millisecond,
		TotalSlots:        1,
	}
	reg := executor.NewRegistry()
	agent := worker.NewAgent(cfg, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := agent.Run(ctx)
	if err != nil {
		t.Fatalf("expected Agent.Run to exit cleanly on context cancellation, got: %v", err)
	}

	// Initial registration is 1; 404 on heartbeat and poll trigger additional registrations
	if registerCount.Load() < 2 {
		t.Fatalf("expected worker to re-register on 404, got %d registrations", registerCount.Load())
	}
}

func TestAgent_Run_DatabaseOutage_DistinguishedFromWorkerUnhealthy(t *testing.T) {
	var pollCount atomic.Int64
	var sawServiceUnavailable atomic.Bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workers/register":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/workers/outage-worker/heartbeat":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/workers/outage-worker/poll":
			pollCount.Add(1)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			sawServiceUnavailable.Store(true)
			_, _ = w.Write([]byte(`{"error":{"code":"SERVICE_UNAVAILABLE","message":"service temporarily unavailable"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cfg := &config.WorkerConfig{
		WorkerID:          "outage-worker",
		CoordinatorURL:    ts.URL,
		HeartbeatInterval: 25 * time.Millisecond,
		TotalSlots:        1,
	}
	reg := executor.NewRegistry()
	agent := worker.NewAgent(cfg, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := agent.Run(ctx)
	if err != nil {
		t.Fatalf("expected Agent.Run to exit cleanly on context cancellation, got: %v", err)
	}

	if !sawServiceUnavailable.Load() {
		t.Fatalf("expected coordinator to return SERVICE_UNAVAILABLE")
	}
	if pollCount.Load() < 1 {
		t.Fatalf("expected at least 1 poll attempt, got %d", pollCount.Load())
	}
}

func TestComputeBackoffWithJitter(t *testing.T) {
	base := 100 * time.Millisecond
	max := 2 * time.Second

	for i := 1; i <= 10; i++ {
		backoff := worker.ComputeBackoffWithJitter(i, base, max)
		if backoff <= 0 {
			t.Fatalf("backoff must be positive, got %v", backoff)
		}
		if backoff > max {
			t.Fatalf("backoff %v exceeded max %v", backoff, max)
		}
	}
}

func TestComputeBackoffWithJitter_Table(t *testing.T) {
	base := 100 * time.Millisecond
	max := 5 * time.Second

	// Counts 0..200 plus 62, 63, 64, 65, 127, 128 and math.MaxInt.
	counts := make([]int, 0, 205)
	for i := 0; i <= 200; i++ {
		counts = append(counts, i)
	}
	for _, extra := range []int{62, 63, 64, 65, 127, 128, math.MaxInt} {
		found := false
		for _, c := range counts {
			if c == extra {
				found = true
				break
			}
		}
		if !found {
			counts = append(counts, extra)
		}
	}

	for _, count := range counts {
		backoff := worker.ComputeBackoffWithJitter(count, base, max)
		if backoff <= 0 {
			t.Fatalf("count %d: expected backoff > 0, got %v", count, backoff)
		}
		if backoff > max {
			t.Fatalf("count %d: backoff %v exceeded max %v", count, backoff, max)
		}

		if count <= 0 {
			if backoff != base {
				t.Fatalf("count %d: expected exact base backoff %v, got %v", count, base, backoff)
			}
		} else {
			expMult := 16
			if count < 5 {
				expMult = 1 << (count - 1)
			}
			expDur := base * time.Duration(expMult)
			if expDur > max {
				expDur = max
			}
			minJitter := time.Duration(float64(expDur) * 0.5)
			if backoff < minJitter || backoff > expDur {
				t.Fatalf("count %d: backoff %v outside expected jitter range [%v, %v]", count, backoff, minJitter, expDur)
			}
		}
	}
}

func TestComputeBackoffWithJitter_Property(t *testing.T) {
	cases := []struct {
		name string
		base time.Duration
		max  time.Duration
	}{
		{"standard_cap_exceeds_16x", 100 * time.Millisecond, 5 * time.Second},
		{"worker_default_cap_below_16x", 200 * time.Millisecond, 3 * time.Second},
		{"small_cap", 50 * time.Millisecond, 500 * time.Millisecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for count := 0; count <= 1000; count++ {
				backoff := worker.ComputeBackoffWithJitter(count, tc.base, tc.max)
				if backoff <= 0 {
					t.Fatalf("count %d: expected backoff > 0, got %v", count, backoff)
				}
				if backoff > tc.max {
					t.Fatalf("count %d: backoff %v exceeded max %v", count, backoff, tc.max)
				}

				if count <= 0 {
					if backoff != tc.base {
						t.Fatalf("count %d: expected base %v, got %v", count, tc.base, backoff)
					}
				} else {
					expMult := 16
					if count < 5 {
						expMult = 1 << (count - 1)
					}
					expDur := tc.base * time.Duration(expMult)
					if expDur > tc.max {
						expDur = tc.max
					}
					minJitter := time.Duration(float64(expDur) * 0.5)
					if backoff < minJitter || backoff > expDur {
						t.Fatalf("count %d: backoff %v outside [%v, %v]", count, backoff, minJitter, expDur)
					}
				}
			}
		})
	}
}
