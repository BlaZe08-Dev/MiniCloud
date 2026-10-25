package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

// ErrServiceUnavailable indicates an upstream coordinator or database outage (HTTP 503/5xx).
var ErrServiceUnavailable = errors.New("service unavailable: database outage or coordinator failure")

// ComputeBackoffWithJitter calculates exponential backoff with full jitter.
func ComputeBackoffWithJitter(failureCount int, base, max time.Duration) time.Duration {
	if failureCount <= 0 {
		return base
	}
	multiplier := 1 << (failureCount - 1)
	if multiplier > 16 {
		multiplier = 16
	}
	dur := base * time.Duration(multiplier)
	if dur > max {
		dur = max
	}
	jitterFrac := 0.5 + rand.Float64()*0.5
	return time.Duration(float64(dur) * jitterFrac)
}

// Agent runs an independent worker process communicating strictly over HTTP.
type Agent struct {
	mu         sync.Mutex
	cfg        *config.WorkerConfig
	registry   *executor.Registry
	httpClient *http.Client
}

// NewAgent constructs a new Worker Agent.
func NewAgent(cfg *config.WorkerConfig, registry *executor.Registry) *Agent {
	return &Agent{
		cfg:      cfg,
		registry: registry,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Run executes the worker lifecycle: register, start heartbeats, and poll for work.
func (a *Agent) Run(ctx context.Context) error {
	slog.Info("starting worker agent",
		"worker_id", a.cfg.WorkerID,
		"coordinator_url", a.cfg.CoordinatorURL,
		"slots", a.cfg.TotalSlots,
	)

	// 1. Register with coordinator
	if err := a.register(ctx); err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	// 2. Start periodic heartbeat loop
	heartbeatTicker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeatTicker.C:
				if err := a.sendHeartbeat(ctx); err != nil {
					slog.Warn("heartbeat failed", "worker_id", a.cfg.WorkerID, "err", err)
				}
			}
		}
	}()

	// 3. Work polling loop
	consecutiveFailures := 0
	baseBackoff := 200 * time.Millisecond
	maxBackoff := 3 * time.Second

	for {
		select {
		case <-ctx.Done():
			slog.Info("worker agent shutting down", "worker_id", a.cfg.WorkerID)
			return nil
		default:
			assignment, err := a.pollWork(ctx)
			if err != nil {
				if errors.Is(err, domain.ErrWorkerUnhealthy) {
					consecutiveFailures = 0
					slog.Info("worker is marked unhealthy by coordinator; waiting for heartbeat to restore health", "worker_id", a.cfg.WorkerID)
					time.Sleep(baseBackoff)
				} else if errors.Is(err, domain.ErrNoQueuedJobs) {
					consecutiveFailures = 0
					time.Sleep(baseBackoff)
				} else if errors.Is(err, ErrServiceUnavailable) {
					consecutiveFailures++
					backoff := ComputeBackoffWithJitter(consecutiveFailures, baseBackoff, maxBackoff)
					slog.Warn("coordinator or database unavailable; backing off before next poll", "worker_id", a.cfg.WorkerID, "failures", consecutiveFailures, "backoff", backoff)
					time.Sleep(backoff)
				} else {
					consecutiveFailures++
					backoff := ComputeBackoffWithJitter(consecutiveFailures, baseBackoff, maxBackoff)
					slog.Warn("work poll error", "err", err, "backoff", backoff)
					time.Sleep(backoff)
				}
				continue
			}

			consecutiveFailures = 0
			if assignment == nil {
				time.Sleep(baseBackoff)
				continue
			}

			// Execute claimed work assignment
			a.executeAndReport(ctx, assignment)
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"id":              a.cfg.WorkerID,
		"version":         "v0.1.0",
		"total_slots":     a.cfg.TotalSlots,
		"available_slots": a.cfg.TotalSlots,
		"supported_tasks": []string{"echo", "sleep", "sha256_loop"},
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/v1/workers/register", a.cfg.CoordinatorURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registration returned HTTP status %d", resp.StatusCode)
	}

	slog.Info("worker successfully registered with coordinator", "worker_id", a.cfg.WorkerID)
	return nil
}

func (a *Agent) sendHeartbeat(ctx context.Context) error {
	payload, _ := json.Marshal(map[string]any{
		"available_slots": a.cfg.TotalSlots,
	})

	url := fmt.Sprintf("%s/v1/workers/%s/heartbeat", a.cfg.CoordinatorURL, a.cfg.WorkerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		slog.Warn("worker not found on coordinator during heartbeat (404); re-registering", "worker_id", a.cfg.WorkerID)
		if regErr := a.register(ctx); regErr != nil {
			return fmt.Errorf("re-registration failed after 404 heartbeat: %w", regErr)
		}
		return nil
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("heartbeat failed with server error %d: %w", resp.StatusCode, ErrServiceUnavailable)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat returned status %d", resp.StatusCode)
	}
	return nil
}

func (a *Agent) pollWork(ctx context.Context) (*coordinator.WorkAssignment, error) {
	url := fmt.Sprintf("%s/v1/workers/%s/poll", a.cfg.CoordinatorURL, a.cfg.WorkerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		slog.Warn("worker not found on coordinator during poll (404); re-registering", "worker_id", a.cfg.WorkerID)
		if regErr := a.register(ctx); regErr != nil {
			return nil, fmt.Errorf("re-registration failed after 404 poll: %w", regErr)
		}
		return nil, domain.ErrNoQueuedJobs
	}

	if resp.StatusCode == http.StatusNoContent {
		return nil, domain.ErrNoQueuedJobs
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		var errEnvelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errEnvelope); err == nil {
			if errEnvelope.Error.Code == "WORKER_UNHEALTHY" {
				return nil, domain.ErrWorkerUnhealthy
			}
			if errEnvelope.Error.Code == "SERVICE_UNAVAILABLE" {
				return nil, ErrServiceUnavailable
			}
		}
		return nil, domain.ErrNoWorkersAvailable
	}
	if resp.StatusCode >= 500 {
		return nil, ErrServiceUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll returned HTTP %d", resp.StatusCode)
	}

	var res struct {
		Assignment *coordinator.WorkAssignment `json:"assignment"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.Assignment, nil
}

// IsNonRetryableReportError returns true if coordinator rejected the commit permanently
// (e.g. 403 Forbidden for WORKER_MISMATCH, 404 Not Found for missing job/attempt on restarted coordinator,
// or 409 Conflict for STALE_ATTEMPT_REJECTED / JOB_ALREADY_TERMINAL).
func IsNonRetryableReportError(statusCode int) bool {
	return statusCode == http.StatusForbidden || statusCode == http.StatusConflict || statusCode == http.StatusNotFound
}

// ReportResult sends the execution outcome to the coordinator.
func (a *Agent) ReportResult(ctx context.Context, jobID, attemptID, leaseToken string, exitCode int, output, errSummary string) (int, error) {
	reportReq, _ := json.Marshal(map[string]any{
		"lease_token": leaseToken,
		"worker_id":   a.cfg.WorkerID,
		"exit_code":   exitCode,
		"output":      output,
		"error":       errSummary,
	})

	url := fmt.Sprintf("%s/v1/jobs/%s/attempts/%s/result", a.cfg.CoordinatorURL, jobID, attemptID)
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reportReq))
	if reqErr != nil {
		return 0, reqErr
	}
	req.Header.Set("Content-Type", "application/json")

	resp, respErr := a.httpClient.Do(req)
	if respErr != nil {
		return 0, respErr
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, nil
	}

	if IsNonRetryableReportError(resp.StatusCode) {
		return resp.StatusCode, fmt.Errorf("non-retryable report rejection (HTTP %d)", resp.StatusCode)
	}

	return resp.StatusCode, fmt.Errorf("report failed with status %d", resp.StatusCode)
}

func (a *Agent) executeAndReport(ctx context.Context, assignment *coordinator.WorkAssignment) {
	slog.Info("executing job assignment",
		"job_id", assignment.JobID,
		"attempt_id", assignment.AttemptID,
		"task_type", assignment.TaskType,
		"timeout_seconds", assignment.TimeoutSeconds,
	)

	timeout := time.Duration(assignment.TimeoutSeconds) * time.Second
	output, exitCode, err := a.registry.Execute(ctx, assignment.TaskType, assignment.Payload, timeout)

	errSummary := ""
	if err != nil {
		errSummary = err.Error()
	}

	var status int
	var reportErr error
	for attempt := 0; attempt < 5; attempt++ {
		status, reportErr = a.ReportResult(ctx, assignment.JobID, assignment.AttemptID, assignment.LeaseToken, exitCode, output, errSummary)
		if reportErr == nil {
			slog.Info("result successfully committed",
				"job_id", assignment.JobID,
				"attempt_id", assignment.AttemptID,
				"exit_code", exitCode,
			)
			return
		}
		if IsNonRetryableReportError(status) {
			slog.Warn("coordinator permanently rejected result (non-retryable)",
				"job_id", assignment.JobID,
				"attempt_id", assignment.AttemptID,
				"status", status,
				"err", reportErr,
			)
			return
		}
		slog.Warn("transient error reporting result to coordinator, retrying",
			"job_id", assignment.JobID,
			"attempt_id", assignment.AttemptID,
			"status", status,
			"err", reportErr,
			"attempt", attempt+1,
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(100*(1<<attempt)) * time.Millisecond):
		}
	}
	slog.Error("failed to report result after retries",
		"job_id", assignment.JobID,
		"attempt_id", assignment.AttemptID,
		"status", status,
		"err", reportErr,
	)
}
