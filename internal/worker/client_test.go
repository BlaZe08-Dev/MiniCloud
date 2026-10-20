package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

			status, err := agent.ReportResult(context.Background(), "job-1", "att-1", "lease-1", 0, "out", "")
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
