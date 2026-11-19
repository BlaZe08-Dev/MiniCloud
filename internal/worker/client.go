package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

const (
	// DefaultRenewalInterval is the renewal interval when the assignment carries no lease duration
	// (one third of the default 30 s lease, ADR-0014) and also the ceiling of the derived interval.
	DefaultRenewalInterval = 10 * time.Second
	// MinRenewalInterval floors the interval derived from a very short advertised lease so a
	// misconfigured coordinator cannot make the worker hammer the renew endpoint.
	MinRenewalInterval = time.Second
	// ResultDeliveryBudget bounds how long a finished result is retried (protocol 6.1, R-11).
	ResultDeliveryBudget = 60 * time.Second

	// MaxExecutionTimeoutSeconds is the maximum accepted execution timeout in seconds,
	// referencing the coordinator's upper bound (86400 s, SEC-012, SEC-014).
	MaxExecutionTimeoutSeconds = coordinator.MaxTimeoutSeconds
	// MaxExecutionTimeout is the maximum accepted execution timeout as a time.Duration.
	MaxExecutionTimeout = time.Duration(MaxExecutionTimeoutSeconds) * time.Second
	// MinExecutionTimeout is the minimum accepted execution timeout as a time.Duration (1 s).
	MinExecutionTimeout = time.Second

	maxDurationSeconds = int(math.MaxInt64 / int64(time.Second))
	minDurationSeconds = int(math.MinInt64 / int64(time.Second))

	// MaxPollResponseBytes bounds the HTTP response body read in pollWork (SEC-060).
	// The coordinator's writeJSON (internal/api/handlers.go) uses json.NewEncoder with HTML
	// escaping enabled: a legal task payload of up to executor.MaxPayloadSizeBytes (64 KiB)
	// containing characters like '<', '>', or '&' is re-encoded as 6-byte escape sequences
	// (e.g. '\u003c', '\u003e', '\u0026'), expanding up to 6x on the wire (~384 KiB).
	// We size MaxPollResponseBytes to 6*executor.MaxPayloadSizeBytes plus 64 KiB of headroom
	// for the JSON envelope, identifiers, and lease metadata (448 KiB / 458,752 bytes total).
	MaxPollResponseBytes = 6*executor.MaxPayloadSizeBytes + 64*1024

	renewBackoffBase    = time.Second
	deliveryBackoffBase = 200 * time.Millisecond
	deliveryBackoffMax  = 5 * time.Second

	// IdlePollInterval is the work loop's sleep after an empty poll, after a WORKER_UNHEALTHY poll
	// and after a nil assignment; it is also the base of the poll error backoff (ADR-0017
	// Section 3.1).
	IdlePollInterval = 200 * time.Millisecond
	// MaxPollBackoff caps the work loop's sleep after a failed poll. ComputeBackoffWithJitter
	// jitters into [0.5, 1.0) of the cap, so no poll backoff exceeds it. The assignment accept
	// window must exceed it plus a round trip (ADR-0017 Section 3.1, INV-17), which
	// TestPollTiming_AcceptWindowExceedsBackoff pins.
	MaxPollBackoff = 3 * time.Second
)

// ErrServiceUnavailable indicates an upstream coordinator or database outage (HTTP 503/5xx).
var ErrServiceUnavailable = errors.New("service unavailable: database outage or coordinator failure")

// ComputeBackoffWithJitter calculates exponential backoff with full jitter.
func ComputeBackoffWithJitter(failureCount int, base, max time.Duration) time.Duration {
	if failureCount <= 0 {
		return base
	}
	if failureCount > 5 {
		failureCount = 5
	}
	multiplier := 1 << (failureCount - 1)
	dur := base * time.Duration(multiplier)
	if dur > max {
		dur = max
	}
	jitterFrac := 0.5 + rand.Float64()*0.5
	return time.Duration(float64(dur) * jitterFrac)
}

// Clock abstracts the timers the agent waits on (renewal interval, delivery backoff and budget) so
// tests drive them deterministically. Wall-clock time is never read from it: lease expiry is
// decided by the coordinator (ADR-0014, worker clocks are untrusted).
type Clock interface {
	NewTimer(d time.Duration) Timer
}

// Timer is the part of time.Timer the agent uses.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realClock struct{}

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// Agent runs an independent worker process communicating strictly over HTTP.
type Agent struct {
	mu         sync.Mutex
	cfg        *config.WorkerConfig
	registry   *executor.Registry
	httpClient *http.Client
	clock      Clock

	// running counts the attempts handed to an attempt goroutine that have not finished
	// (ADR-0019 Section 2.1). It changes only while the attempt holds a slot token, so
	// 0 <= running <= TotalSlots at every instant.
	running atomic.Int64
	// onSlotWait, when set, is called each time the poll loop finds no free slot and is about
	// to block (tests only; set before Run).
	onSlotWait func()
	// onAttemptDone, when set, is called by each attempt goroutine right after it released its
	// slot (tests only; set before Run).
	onAttemptDone func()
}

// availableSlots is TotalSlots minus the attempts currently running (ADR-0019 Section 2.1), the
// value register and heartbeat report. The clamp keeps the wire value inside [0, TotalSlots]
// even if the invariant on running were ever broken; tests assert the invariant on running itself.
func (a *Agent) availableSlots() int {
	return min(max(a.cfg.TotalSlots-int(a.running.Load()), 0), a.cfg.TotalSlots)
}

// SetClock replaces the timer source (tests only; call before running work).
func (a *Agent) SetClock(c Clock) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clock = c
}

// pollSleep blocks for d between work polls. It waits on the agent clock, which is the real
// timer in production (the same wait as time.Sleep), so a test can observe every poll sleep and
// check it against MaxPollBackoff (ADR-0017 INV-17).
func (a *Agent) pollSleep(d time.Duration) {
	<-a.clock.NewTimer(d).C()
}

// NewAgent constructs a new Worker Agent.
func NewAgent(cfg *config.WorkerConfig, registry *executor.Registry) *Agent {
	return &Agent{
		cfg:      cfg,
		registry: registry,
		clock:    realClock{},
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

	// 2. Start periodic heartbeat loop. Run waits for it before returning (ADR-0019 Section 2.5).
	heartbeatTicker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()
	var heartbeatWG sync.WaitGroup
	defer heartbeatWG.Wait()

	heartbeatWG.Add(1)
	go func() {
		defer heartbeatWG.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeatTicker.C:
				a.heartbeatTick(ctx)
			}
		}
	}()

	// 3. Work polling loop: a bounded pool of TotalSlots concurrent attempts (ADR-0019). A slot is
	// acquired before every poll, so a worker with no free slot never polls (Section 2.2).
	consecutiveFailures := 0
	sem := make(chan struct{}, a.cfg.TotalSlots)
	var attempts sync.WaitGroup

	for {
		if !a.acquireSlot(ctx, sem) {
			slog.Info("worker agent shutting down", "worker_id", a.cfg.WorkerID)
			a.drain(&attempts)
			return nil
		}
		assignment, err := a.pollWork(ctx)
		if err != nil {
			<-sem // no attempt: the poll loop still owns the token and gives it back
			if errors.Is(err, domain.ErrWorkerUnhealthy) {
				consecutiveFailures = 0
				slog.Info("worker is marked unhealthy by coordinator; waiting for heartbeat to restore health", "worker_id", a.cfg.WorkerID)
				a.pollSleep(IdlePollInterval)
			} else if errors.Is(err, domain.ErrNoQueuedJobs) {
				consecutiveFailures = 0
				a.pollSleep(IdlePollInterval)
			} else if errors.Is(err, ErrServiceUnavailable) {
				consecutiveFailures++
				backoff := ComputeBackoffWithJitter(consecutiveFailures, IdlePollInterval, MaxPollBackoff)
				slog.Warn("coordinator or database unavailable; backing off before next poll", "worker_id", a.cfg.WorkerID, "failures", consecutiveFailures, "backoff", backoff)
				a.pollSleep(backoff)
			} else {
				consecutiveFailures++
				backoff := ComputeBackoffWithJitter(consecutiveFailures, IdlePollInterval, MaxPollBackoff)
				slog.Warn("work poll error", "err", err, "backoff", backoff)
				a.pollSleep(backoff)
			}
			continue
		}

		consecutiveFailures = 0
		if assignment == nil {
			<-sem
			a.pollSleep(IdlePollInterval)
			continue
		}

		// Hand the token to the attempt goroutine, which releases it exactly once.
		a.running.Add(1)
		attempts.Add(1)
		go a.runAttempt(ctx, sem, &attempts, assignment)
	}
}

// heartbeatTick sends one heartbeat. A panic in it is recovered and logged, and the heartbeat loop
// goes on with the next tick (ADR-0019 Section 2.3): a missed heartbeat is harmless, a dead process
// would end every running attempt.
func (a *Agent) heartbeatTick(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			logAgentPanic("heartbeat", nil, p)
		}
	}()
	if err := a.sendHeartbeat(ctx); err != nil {
		slog.Warn("heartbeat failed", "worker_id", a.cfg.WorkerID, "err", err)
	}
}

// logAgentPanic logs a recovered panic of an agent goroutine (not task code; ADR-0019 Section 2.3):
// only the goroutine's name, the job and attempt IDs when it serves one attempt, and the panic's
// type. Never the panic's value, the lease token or the payload.
func logAgentPanic(goroutine string, asg *coordinator.WorkAssignment, p any) {
	attrs := []any{"goroutine", goroutine}
	if asg != nil {
		attrs = append(attrs, "job_id", asg.JobID, "attempt_id", asg.AttemptID)
	}
	attrs = append(attrs, "panic_type", fmt.Sprintf("%T", p))
	slog.Error("agent goroutine panicked; recovered", attrs...)
}

// acquireSlot takes one slot token for the next poll. It returns false, holding nothing, when ctx
// is done. When no slot is free it calls the onSlotWait hook and blocks until an attempt releases
// one or ctx ends.
func (a *Agent) acquireSlot(ctx context.Context, sem chan struct{}) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case sem <- struct{}{}:
		return true
	default:
	}
	if a.onSlotWait != nil {
		a.onSlotWait()
	}
	select {
	case sem <- struct{}{}:
		if ctx.Err() != nil {
			<-sem
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

// runAttempt executes one assignment in its own goroutine. Its single deferred function is the
// only release site of the attempt's slot (ADR-0019 Section 2.3): it runs once on every exit,
// including a panic, which it recovers so one attempt cannot kill the others. After a recovered
// panic no result is reported; renewal has stopped (ExecuteAndReport's defers ran during the
// unwinding), so the lease expires and the coordinator retries the job. Only the IDs and the
// panic's type are logged, never its value, the lease token or the payload.
func (a *Agent) runAttempt(ctx context.Context, sem chan struct{}, attempts *sync.WaitGroup, asg *coordinator.WorkAssignment) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("attempt panicked; no result reported, the lease will expire and the job be retried",
				"job_id", asg.JobID, "attempt_id", asg.AttemptID, "panic_type", fmt.Sprintf("%T", p))
		}
		a.running.Add(-1)
		<-sem
		if a.onAttemptDone != nil {
			a.onAttemptDone()
		}
		attempts.Done()
	}()
	a.ExecuteAndReport(ctx, asg)
}

// drain waits for the running attempts after shutdown (ADR-0019 Section 2.5). They were cancelled
// through ctx, the existing path. The wait is bounded by ResultDeliveryBudget on the agent clock;
// with nothing running no timer is created.
func (a *Agent) drain(attempts *sync.WaitGroup) {
	if a.running.Load() == 0 {
		// Every attempt goroutine is past its decrement and only has wg.Done left.
		attempts.Wait()
		return
	}
	done := make(chan struct{})
	go func() {
		attempts.Wait()
		close(done)
	}()
	t := a.clock.NewTimer(ResultDeliveryBudget)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C():
		slog.Error("shutdown drain bound reached; attempts still running are abandoned (their leases will expire)",
			"worker_id", a.cfg.WorkerID, "running", a.running.Load(), "bound", ResultDeliveryBudget)
	}
}

func (a *Agent) register(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"id":              a.cfg.WorkerID,
		"version":         "v0.1.0",
		"total_slots":     a.cfg.TotalSlots,
		"available_slots": a.availableSlots(),
		"supported_tasks": []string{"echo", "sleep", "sha256_loop"},
		// This worker executes up to total_slots attempts at once (ADR-0019), so the round-robin
		// pass may reserve it that many jobs (ADR-0020 Section 2.6). Older workers omit the field
		// and keep one slot.
		"executes_concurrently": true,
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
		"available_slots": a.availableSlots(),
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
		if err := json.NewDecoder(io.LimitReader(resp.Body, MaxPollResponseBytes)).Decode(&errEnvelope); err == nil {
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxPollResponseBytes)).Decode(&res); err != nil {
		return nil, err
	}
	if res.Assignment == nil {
		return nil, errors.New("poll returned 200 OK without an assignment")
	}

	return res.Assignment, nil
}

// IsNonRetryableReportError returns true if coordinator rejected the commit permanently
// (e.g. 403 Forbidden for WORKER_MISMATCH, 404 Not Found for missing job/attempt on restarted coordinator,
// or 409 Conflict for STALE_ATTEMPT_REJECTED / JOB_ALREADY_TERMINAL).
func IsNonRetryableReportError(statusCode int) bool {
	return statusCode == http.StatusForbidden || statusCode == http.StatusConflict || statusCode == http.StatusNotFound
}

// ReportResult sends the execution outcome to the coordinator. errorClass is the worker's
// classification of a failed outcome (PERMANENT, TRANSIENT or UNKNOWN) and is omitted from the
// request for a success; the coordinator validates it and never trusts it blindly.
func (a *Agent) ReportResult(ctx context.Context, jobID, attemptID, leaseToken string, exitCode int, output, errSummary string, errorClass domain.ErrorClass) (int, error) {
	status, _, err := a.reportResult(ctx, jobID, attemptID, leaseToken, exitCode, output, errSummary, errorClass)
	return status, err
}

// reportResult is ReportResult that also returns the error code of the coordinator's envelope
// ("" when the answer has none).
func (a *Agent) reportResult(ctx context.Context, jobID, attemptID, leaseToken string, exitCode int, output, errSummary string, errorClass domain.ErrorClass) (int, string, error) {
	body := map[string]any{
		"lease_token": leaseToken,
		"worker_id":   a.cfg.WorkerID,
		"exit_code":   exitCode,
		"output":      output,
		"error":       errSummary,
	}
	if errorClass != "" {
		body["error_classification"] = string(errorClass)
	}
	reportReq, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/v1/jobs/%s/attempts/%s/result", a.cfg.CoordinatorURL, url.PathEscape(jobID), url.PathEscape(attemptID))
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reportReq))
	if reqErr != nil {
		return 0, "", reqErr
	}
	req.Header.Set("Content-Type", "application/json")

	resp, respErr := a.httpClient.Do(req)
	if respErr != nil {
		return 0, "", respErr
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, "", nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	code := errorCode(raw)

	if IsNonRetryableReportError(resp.StatusCode) {
		return resp.StatusCode, code, fmt.Errorf("non-retryable report rejection (HTTP %d)", resp.StatusCode)
	}

	return resp.StatusCode, code, fmt.Errorf("report failed with status %d", resp.StatusCode)
}

// renewalInterval is one third of the lease duration the coordinator advertised in the poll
// assignment (protocol 6.2), clamped to [MinRenewalInterval, DefaultRenewalInterval] so a long or
// hostile advertised lease cannot make the worker renew rarely; the default applies when the
// coordinator advertised none (old coordinator, sub-second lease).
func renewalInterval(a *coordinator.WorkAssignment) time.Duration {
	if a.LeaseDurationSeconds <= 0 {
		return DefaultRenewalInterval
	}
	return min(max(time.Duration(a.LeaseDurationSeconds)*time.Second/3, MinRenewalInterval), DefaultRenewalInterval)
}

type renewVerdict int

const (
	renewOK        renewVerdict = iota // lease extended, keep going
	renewCancel                        // 200 with cancel_requested: cancel the task, stop renewing, report
	renewLost                          // coordinator declared the attempt lost: abort, report nothing
	renewDisable                       // endpoint missing or request rejected as malformed: stop renewing, keep running
	renewTransient                     // transport error, 5xx or anything unexpected: keep running, retry
)

// recognizedNotFound are the 404 codes of protocol 6.3 that mean the attempt is gone.
var recognizedNotFound = map[string]bool{"JOB_NOT_FOUND": true, "ATTEMPT_NOT_FOUND": true, "WORKER_NOT_FOUND": true}

// renewOnce sends one renewal and classifies the answer (protocol 6.1 "Worker Renewal Response
// Handling"). It never reads the lease token into a log line.
func (a *Agent) renewOnce(ctx context.Context, asg *coordinator.WorkAssignment, timeout time.Duration) renewVerdict {
	body, _ := json.Marshal(map[string]string{"worker_id": a.cfg.WorkerID, "lease_token": asg.LeaseToken})
	url := fmt.Sprintf("%s/v1/jobs/%s/attempts/%s/renew", a.cfg.CoordinatorURL, url.PathEscape(asg.JobID), url.PathEscape(asg.AttemptID))
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return renewTransient
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		slog.Warn("lease renewal transport error", "job_id", asg.JobID, "attempt_id", asg.AttemptID, "err", err)
		return renewTransient
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode == http.StatusOK:
		var ok struct {
			CancelRequested bool `json:"cancel_requested"`
		}
		if err := json.Unmarshal(raw, &ok); err != nil {
			slog.Warn("lease renewal answered 200 with an unreadable body", "job_id", asg.JobID, "attempt_id", asg.AttemptID)
			return renewTransient
		}
		if ok.CancelRequested {
			return renewCancel
		}
		return renewOK
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusForbidden:
		return renewLost
	case resp.StatusCode == http.StatusNotFound:
		if recognizedNotFound[errorCode(raw)] {
			return renewLost
		}
		slog.Warn("coordinator has no renew endpoint (404 without a recognised error code); running to completion without renewals",
			"job_id", asg.JobID, "attempt_id", asg.AttemptID)
		return renewDisable
	case resp.StatusCode == http.StatusBadRequest:
		slog.Error("coordinator rejected the lease renewal as malformed; running to completion without renewals",
			"job_id", asg.JobID, "attempt_id", asg.AttemptID, "code", errorCode(raw))
		return renewDisable
	default:
		slog.Warn("lease renewal failed, will retry", "job_id", asg.JobID, "attempt_id", asg.AttemptID, "status", resp.StatusCode)
		return renewTransient
	}
}

// errorCode extracts error.code from a protocol error envelope, or "" when there is none.
func errorCode(raw []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	return env.Error.Code
}

// attemptRun is the shared state of one executing attempt.
type attemptRun struct {
	lost     chan struct{}
	lostOnce sync.Once
}

func (r *attemptRun) markLost() { r.lostOnce.Do(func() { close(r.lost) }) }

func (r *attemptRun) isLost() bool {
	select {
	case <-r.lost:
		return true
	default:
		return false
	}
}

// renewLoop renews the lease until ctx ends or the coordinator's answer ends renewal. Transient
// failures retry with bounded exponential backoff (never longer than the interval) and never
// abort the attempt on their own.
func (a *Agent) renewLoop(ctx context.Context, run *attemptRun, asg *coordinator.WorkAssignment, interval time.Duration, cancelTask context.CancelFunc) {
	failures := 0
	wait := interval
	for {
		t := a.clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		switch a.renewOnce(ctx, asg, interval) {
		case renewOK:
			failures, wait = 0, interval
		case renewCancel:
			slog.Info("cancellation requested by coordinator; cancelling task", "job_id", asg.JobID, "attempt_id", asg.AttemptID)
			cancelTask()
			return
		case renewLost:
			slog.Warn("coordinator declared the attempt lost; aborting task and suppressing the result", "job_id", asg.JobID, "attempt_id", asg.AttemptID)
			run.markLost()
			cancelTask()
			return
		case renewDisable:
			return
		case renewTransient:
			if ctx.Err() != nil {
				return
			}
			failures++
			wait = min(ComputeBackoffWithJitter(failures, renewBackoffBase, interval), interval)
		}
	}
}

// isValidationRejection reports a 400 whose code says the content of this result is unacceptable
// (and will be on every retry). MALFORMED_JSON is deliberately not included: the worker encodes the
// body with json.Marshal, so that answer means the body was corrupted in transit, a transient fault
// that must not turn a successful result into a permanent failure, as opposed to a rejection that means the result is no longer wanted
// (stale attempt, terminal job, lost lease, state transition).
func isValidationRejection(status int, code string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	switch code {
	case "INVALID_REQUEST", "OUTPUT_TOO_LARGE":
		return true
	}
	return false
}

// isTerminalDelivery reports a rejection the coordinator will never answer differently: the 409
// family, 403, 404 and a deterministic 400 (protocol 6.3, R-11).
func isTerminalDelivery(status int) bool {
	return IsNonRetryableReportError(status) || status == http.StatusBadRequest
}

// ClampExecutionTimeout maps a coordinator-supplied timeout in seconds to an execution
// time.Duration. It enforces bounds:
//   - Above MaxExecutionTimeoutSeconds (86400 s): clamped to MaxExecutionTimeout (86400 s).
//   - Below 1 s: clamped up to 1 s (never zero or negative). A task execution context must
//     have a bounded positive deadline; non-positive timeouts are invalid at the coordinator
//     boundary (SEC-014) and must never cause immediate deadline cancellation or disable timeouts.
//   - Multiplication overflow: guarded so values exceeding int64 duration limits do not overflow.
//
// A warning is logged whenever clamping occurs, logging only numeric values (no attacker-sized strings).
func ClampExecutionTimeout(timeoutSeconds int) time.Duration {
	var dur time.Duration
	clamped := false

	// Guard against integer multiplication overflow when converting to time.Duration.
	if timeoutSeconds > maxDurationSeconds {
		clamped = true
		dur = MaxExecutionTimeout
	} else if timeoutSeconds < minDurationSeconds {
		clamped = true
		dur = MinExecutionTimeout
	} else {
		dur = time.Duration(timeoutSeconds) * time.Second
	}

	// Clamp above the maximum execution timeout.
	if dur > MaxExecutionTimeout {
		clamped = true
		dur = MaxExecutionTimeout
	}

	// Clamp below 1 second up to 1 second (never zero or negative).
	if dur < MinExecutionTimeout {
		clamped = true
		dur = MinExecutionTimeout
	}

	if clamped {
		slog.Warn("worker execution timeout clamped to bounds",
			"configured_seconds", timeoutSeconds,
			"effective_duration", dur,
		)
	}

	return dur
}

// ExecuteAndReport runs one claimed assignment and delivers its outcome. A renewal goroutine keeps
// the lease alive from the start of execution until delivery ends; it ends unconditionally with
// this call.
func (a *Agent) ExecuteAndReport(ctx context.Context, assignment *coordinator.WorkAssignment) {
	slog.Info("executing job assignment",
		"job_id", assignment.JobID,
		"attempt_id", assignment.AttemptID,
		"task_type", assignment.TaskType,
		"timeout_seconds", assignment.TimeoutSeconds,
	)

	run := &attemptRun{lost: make(chan struct{})}
	taskCtx, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	renewCtx, stopRenew := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// A panic in the renewal goroutine is recovered in the fail-safe direction (ADR-0019
		// Section 2.3): with no renewals the lease cannot be trusted, so the attempt is treated as
		// lost. The task is cancelled, the result dropped, and the slot freed by runAttempt as usual.
		defer func() {
			if p := recover(); p != nil {
				logAgentPanic("renewal", assignment, p)
				run.markLost()
				cancelTask()
			}
		}()
		a.renewLoop(renewCtx, run, assignment, renewalInterval(assignment), cancelTask)
	}()
	defer func() {
		stopRenew()
		wg.Wait()
	}()

	timeout := ClampExecutionTimeout(assignment.TimeoutSeconds)
	output, exitCode, err := a.registry.Execute(taskCtx, assignment.TaskType, assignment.Payload, timeout)

	// Defensive clamp at the wire boundary: the coordinator answers a deterministic 400 for an
	// oversize field and the result would be dropped, the attempt lost and retried until the
	// budget is spent. Whatever an executor returned, what leaves the worker fits the limits.
	output = executor.ClampText(output, executor.MaxOutputSizeBytes)
	errSummary := ""
	if err != nil {
		errSummary = executor.ClampText(err.Error(), executor.MaxErrorSizeBytes)
	}
	// A failed outcome carries its class; a success carries none (ADR-0015).
	errorClass := executor.Classify(err)
	// deliverResult drops the result without any request when the attempt was declared lost.
	a.deliverResult(ctx, run, assignment, exitCode, output, errSummary, errorClass)
}

// deliverResult retries ReportResult with bounded backoff for at most ResultDeliveryBudget. It
// stops at the first success, a terminal rejection, a lost lease, shutdown or the budget.
func (a *Agent) deliverResult(ctx context.Context, run *attemptRun, asg *coordinator.WorkAssignment, exitCode int, output, errSummary string, errorClass domain.ErrorClass) {
	budget := a.clock.NewTimer(ResultDeliveryBudget)
	defer budget.Stop()
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var expired atomic.Bool
	go func() {
		// A panic here is recovered in the fail-safe direction (ADR-0019 Section 2.3): the budget
		// can no longer be enforced, so delivery is treated as expired now.
		defer func() {
			if p := recover(); p != nil {
				logAgentPanic("delivery_budget", asg, p)
				expired.Store(true)
				cancel()
			}
		}()
		select {
		case <-budget.C():
			expired.Store(true)
			cancel()
		case <-dctx.Done():
		}
	}()

	var status int
	var code string
	var reportErr error
	fallbackSent := false
	for n := 1; ; n++ {
		if run.isLost() {
			slog.Warn("lease lost while delivering; dropping the result", "job_id", asg.JobID, "attempt_id", asg.AttemptID)
			return
		}
		status, code, reportErr = a.reportResult(dctx, asg.JobID, asg.AttemptID, asg.LeaseToken, exitCode, output, errSummary, errorClass)
		if reportErr == nil {
			slog.Info("result successfully committed", "job_id", asg.JobID, "attempt_id", asg.AttemptID, "exit_code", exitCode)
			return
		}
		if !fallbackSent && isValidationRejection(status, code) {
			// Last resort: the coordinator will never accept this body. One failure report for the
			// same attempt and lease, PERMANENT so the job ends FAILED now instead of losing every
			// attempt. The renewal goroutine keeps running until it is delivered (it ends with
			// ExecuteAndReport). If this is rejected too, the branch below behaves as before.
			fallbackSent = true
			exitCode, output = 1, ""
			errSummary = executor.ClampText(fmt.Sprintf("result rejected by coordinator: HTTP %d %s", status, code), executor.MaxErrorSizeBytes)
			errorClass = domain.ClassPermanent
			slog.Warn("coordinator rejected the result as invalid; sending one fallback failure report",
				"job_id", asg.JobID, "attempt_id", asg.AttemptID, "status", status, "code", code)
			continue
		}
		if isTerminalDelivery(status) {
			slog.Warn("coordinator permanently rejected result (non-retryable)",
				"job_id", asg.JobID, "attempt_id", asg.AttemptID, "status", status, "err", reportErr)
			return
		}
		slog.Warn("transient error reporting result to coordinator, retrying",
			"job_id", asg.JobID, "attempt_id", asg.AttemptID, "status", status, "err", reportErr, "attempt", n)
		t := a.clock.NewTimer(ComputeBackoffWithJitter(n, deliveryBackoffBase, deliveryBackoffMax))
		select {
		case <-dctx.Done():
			t.Stop()
		case <-run.lost:
			t.Stop()
		case <-t.C():
			continue
		}
		if run.isLost() {
			continue
		}
		break
	}
	if expired.Load() {
		slog.Error("gave up delivering result after the delivery budget; the lease will expire and the job be reclaimed",
			"job_id", asg.JobID, "attempt_id", asg.AttemptID, "budget", ResultDeliveryBudget, "status", status, "err", reportErr)
		return
	}
	slog.Warn("result delivery interrupted by shutdown", "job_id", asg.JobID, "attempt_id", asg.AttemptID)
}
