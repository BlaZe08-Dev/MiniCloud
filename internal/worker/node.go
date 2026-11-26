package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/logsafe"
	"github.com/BlaZe08-Dev/Minicloud/internal/platform"
	"github.com/BlaZe08-Dev/Minicloud/internal/version"
)

// Worker node reporting (ADR-0022 Sections 4.2, 4.3, 4.5 and 10, V2-1d). Opt-in: nothing in this
// file runs unless MINICLOUD_NODE_REPORTING_ENABLED is true.
const (
	// nodeBackoffBase and nodeBackoffMax are the failure backoff of node calls. Placeholders, not
	// measured (OD-23).
	nodeBackoffBase = time.Second
	nodeBackoffMax  = 30 * time.Second
	// defaultRetryAfter applies when a 429 carries no usable Retry-After. Placeholder, not measured.
	defaultRetryAfter = 60 * time.Second
	// minRetryAfter floors "Retry-After: 0" so a 429 can never become a hot loop.
	minRetryAfter = time.Second
	// maxRetryAfter caps the header so the Duration cannot overflow.
	maxRetryAfter = 24 * time.Hour
	// maxNodeResponseBytes bounds every node response body read (D12).
	maxNodeResponseBytes = 64 << 10
)

type nodeOutcome int

const (
	outAttached    nodeOutcome = iota // 200: the node is registered
	outRateLimited                    // 429 RATE_LIMITED: the node exists; register again after Retry-After
	outLegacy                         // 404, 400 or 429 NODE_LIMIT_REACHED: legacy for the life of the process
	outRetry                          // 5xx, transport error or anything else: stay detached, retry with backoff
)

// nodeRun is the state of node reporting. After startNode returns, only the node goroutine touches
// it (D8); the attached node ID is shared with register() through Agent.nodeAttached.
type nodeRun struct {
	a        *Agent
	probe    platform.Probe
	nodeID   string
	interval time.Duration // 0 = telemetry unset

	session string
	seq     int64

	regFailures int // consecutive node registration failures
	telFailures int // consecutive telemetry failures
	legacy      bool

	regT, telT, resendT Timer
	telNow, resendNow   bool
}

// startNode implements D9, D10 and the synchronous first registration of D2. It returns nil when
// reporting is off. Any error is a startup configuration error: no HTTP request has been sent.
func (a *Agent) startNode(ctx context.Context) (*nodeRun, error) {
	if !a.cfg.NodeReportingEnabled {
		if a.cfg.NodeStateDir != "" {
			slog.Warn("MINICLOUD_NODE_STATE_DIR is ignored because MINICLOUD_NODE_REPORTING_ENABLED is not true")
		}
		return nil, nil
	}
	dir := a.cfg.NodeStateDir
	if dir == "" {
		d, err := platform.DefaultStateDir()
		if err != nil {
			return nil, fmt.Errorf("MINICLOUD_NODE_REPORTING_ENABLED is true but no node state directory can be resolved (set MINICLOUD_NODE_STATE_DIR): %w", err)
		}
		dir = d
	}
	id, err := platform.LoadOrCreateNodeID(dir)
	if err != nil {
		return nil, fmt.Errorf("MINICLOUD_NODE_REPORTING_ENABLED is true but the node identity cannot be loaded: %w", err)
	}
	n := &nodeRun{a: a, probe: a.newProbe(dir), nodeID: id, interval: a.cfg.NodeTelemetryInterval}
	if n.interval > 0 {
		if n.session, err = newSessionID(); err != nil {
			return nil, fmt.Errorf("node telemetry session: %w", err)
		}
	} else {
		slog.Info("node reporting is on but MINICLOUD_NODE_TELEMETRY_INTERVAL is not set; no telemetry will be sent")
	}
	out, retryAfter := n.registerNode(ctx)
	n.apply(ctx, out, retryAfter, false)
	return n, nil
}

// newSessionID draws a telemetry session_id: 16 bytes from crypto/rand, validated.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := domain.FormatNodeID(b)
	if err := domain.ValidateSessionID(id); err != nil {
		return "", err
	}
	return id, nil
}

// postNode sends one node request and reads at most 64 KiB of the answer (D12).
func (a *Agent) postNode(ctx context.Context, path string, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.CoordinatorURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponseBytes))
	return resp.StatusCode, resp.Header, raw, err
}

// safeCode returns a coordinator error code only if it looks like one (upper case, digits,
// underscore, at most 64 bytes); a hostile coordinator cannot put free text in a log line.
func safeCode(c string) string {
	if len(c) > 64 {
		return ""
	}
	for i := 0; i < len(c); i++ {
		if ch := c[i]; !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return ""
		}
	}
	return c
}

// parseRetryAfter reads whole seconds. Absent, invalid or negative is 60 s (placeholder, not
// measured); the result is floored at 1 s and capped at 24 h.
func parseRetryAfter(h http.Header) time.Duration {
	secs, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64)
	if err != nil || secs < 0 {
		return defaultRetryAfter
	}
	return min(max(time.Duration(min(secs, int64(maxRetryAfter/time.Second)))*time.Second, minRetryAfter), maxRetryAfter)
}

// registerNode sends POST /v1/nodes/register and classifies the answer (D3).
func (n *nodeRun) registerNode(ctx context.Context) (nodeOutcome, time.Duration) {
	hw, err := n.probe.Hardware(ctx)
	if err != nil {
		slog.Warn("node hardware probe failed; node registration will be retried")
		return outRetry, 0
	}
	body, err := json.Marshal(domain.NodeRegistration{
		NodeID:          n.nodeID,
		DisplayName:     "",
		ContractVersion: domain.ContractVersion,
		AgentVersion:    version.Version,
		Hardware:        hw,
	})
	if err != nil {
		slog.Warn("node registration body could not be built; it will be retried")
		return outRetry, 0
	}
	status, hdr, raw, err := n.a.postNode(ctx, "/v1/nodes/register", body)
	if err != nil {
		slog.Warn("node registration request failed; it will be retried", logsafe.Err(err))
		return outRetry, 0
	}
	code := safeCode(errorCode(raw))
	switch {
	case status == http.StatusOK:
		return outAttached, 0
	case status == http.StatusTooManyRequests && code == "RATE_LIMITED":
		return outRateLimited, parseRetryAfter(hdr)
	case status == http.StatusNotFound, status == http.StatusBadRequest,
		status == http.StatusTooManyRequests && code == "NODE_LIMIT_REACHED":
		slog.Warn("node reporting is off for this process: the coordinator does not take this node registration", "status", status, "code", code)
		return outLegacy, 0
	}
	slog.Warn("node registration was not accepted; it will be retried", "status", status, "code", code)
	return outRetry, 0
}

// detach clears the attached node ID and everything that only makes sense while attached.
func (n *nodeRun) detach() {
	n.a.nodeAttached.Store(nil)
	n.stop(&n.telT)
	n.stop(&n.resendT)
	n.telNow, n.resendNow = false, false
}

func (n *nodeRun) stop(t *Timer) {
	if *t != nil {
		(*t).Stop()
		*t = nil
	}
}

func (n *nodeRun) stopAll() {
	n.stop(&n.regT)
	n.stop(&n.telT)
	n.stop(&n.resendT)
}

func (n *nodeRun) retryRegistration() {
	n.regFailures++
	n.regT = n.a.clock.NewTimer(ComputeBackoffWithJitter(n.regFailures, nodeBackoffBase, nodeBackoffMax))
}

// apply moves the state machine on one registration outcome. inLoop is false for the synchronous
// registration at start, where the first worker registration follows and carries the node ID.
func (n *nodeRun) apply(ctx context.Context, out nodeOutcome, retryAfter time.Duration, inLoop bool) {
	switch out {
	case outAttached, outRateLimited:
		n.regFailures = 0
		n.stop(&n.regT)
		n.stop(&n.resendT)
		n.resendNow = false
		if out == outRateLimited {
			n.resendT = n.a.clock.NewTimer(retryAfter)
		}
		wasAttached := n.a.nodeAttached.Load() != nil
		id := n.nodeID
		n.a.nodeAttached.Store(&id)
		if wasAttached {
			return // a re-sent registration: the worker row already has the node ID
		}
		slog.Info("node attached", "node_id", id)
		n.telNow = n.interval > 0
		if inLoop {
			// Give the worker row its node_id. If the coordinator rejects it, register() has
			// already fallen back, detached and woken us; back off instead of looping (D5).
			if err := n.a.register(ctx); err != nil {
				slog.Warn("worker re-registration with the node failed", logsafe.Err(err))
			}
			select {
			case <-n.a.nodeWake:
			default:
			}
			if n.a.nodeAttached.Load() == nil {
				n.detach()
				n.retryRegistration()
			}
		}
	case outLegacy:
		n.legacy = true
		n.stopAll()
		if n.a.nodeAttached.Load() != nil {
			n.detach()
			if inLoop {
				if err := n.a.register(ctx); err != nil {
					slog.Warn("worker re-registration without the node failed", logsafe.Err(err))
				}
			}
		}
	case outRetry:
		if n.a.nodeAttached.Load() != nil { // a re-sent registration failed: stay attached, send it again later
			n.regFailures++
			n.stop(&n.resendT)
			n.resendT = n.a.clock.NewTimer(ComputeBackoffWithJitter(n.regFailures, nodeBackoffBase, nodeBackoffMax))
			return
		}
		n.retryRegistration()
	}
}

// loop is the node goroutine (D8): it owns the session, seq, failure counts and the legacy flag,
// waits only on agent-clock timers and the wake channel, and returns when ctx ends or the process
// is legacy for life.
func (n *nodeRun) loop(ctx context.Context) {
	defer n.stopAll()
	timerC := func(t Timer) <-chan time.Time {
		if t == nil {
			return nil
		}
		return t.C()
	}
	for {
		if ctx.Err() != nil || n.legacy {
			return
		}
		attached := n.a.nodeAttached.Load() != nil
		switch {
		case !attached && n.regT == nil:
			n.guard(func() {
				out, ra := n.registerNode(ctx)
				n.apply(ctx, out, ra, true)
			})
			continue
		case attached && n.resendNow:
			n.resendNow = false
			n.guard(func() {
				out, ra := n.registerNode(ctx)
				n.apply(ctx, out, ra, true)
			})
			continue
		case attached && n.telNow:
			n.telNow = false
			n.guard(func() { n.sendTelemetry(ctx) })
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-timerC(n.regT):
			n.regT = nil
		case <-timerC(n.telT):
			n.telT = nil
			n.telNow = true
		case <-timerC(n.resendT):
			n.resendT = nil
			n.resendNow = true
		case <-n.a.nodeWake:
			if !attached {
				n.stop(&n.regT) // register() lost the node: register it again at once
			}
		}
	}
}

// guard runs one step; a panic is recovered and logged (as heartbeatTick does) and the step is
// scheduled again after a wait, so a panicking step cannot spin.
func (n *nodeRun) guard(f func()) {
	defer func() {
		if p := recover(); p != nil {
			logAgentPanic("node", nil, p)
			switch {
			case n.a.nodeAttached.Load() == nil:
				if n.regT == nil {
					n.retryRegistration()
				}
			case n.interval > 0 && n.telT == nil && !n.telNow:
				n.telT = n.a.clock.NewTimer(n.interval)
			}
		}
	}()
	f()
}

// sendTelemetry takes one sample and sends one report (D6).
func (n *nodeRun) sendTelemetry(ctx context.Context) {
	wait := n.interval
	defer func() {
		if n.a.nodeAttached.Load() != nil && ctx.Err() == nil {
			n.telT = n.a.clock.NewTimer(wait)
		}
	}()
	sample, err := n.probe.Telemetry(ctx)
	if err != nil {
		slog.Warn("node telemetry sample failed; this report is skipped")
		return
	}
	sampled := time.Now()
	n.seq++
	age := min(max(time.Since(sampled).Milliseconds(), 0), domain.MaxSampleAgeMS)
	body, err := json.Marshal(domain.NodeTelemetryReport{
		ContractVersion: domain.ContractVersion,
		SessionID:       n.session,
		Seq:             n.seq,
		SampleAgeMS:     age,
		NodeTelemetry:   sample,
	})
	if err != nil {
		slog.Warn("node telemetry body could not be built; this report is skipped")
		return
	}
	status, _, raw, err := n.a.postNode(ctx, "/v1/nodes/"+n.nodeID+"/telemetry", body)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("node telemetry request failed", logsafe.Err(err))
		}
		n.telFailed(&wait)
		return
	}
	code := safeCode(errorCode(raw))
	switch {
	case status == http.StatusOK:
		n.telFailures = 0
	case status == http.StatusConflict && code == "TELEMETRY_OUT_OF_ORDER":
		slog.Warn("node telemetry rejected as out of order; starting a new session", "status", status, "code", code)
		if sid, err := newSessionID(); err == nil {
			n.session, n.seq = sid, 0
		}
	case status == http.StatusNotFound:
		slog.Warn("coordinator does not know the node; registering it again", "status", status, "code", code)
		n.detach() // the loop registers the node (D3), then the worker, then reports again
	case status == http.StatusBadRequest:
		slog.Warn("node telemetry rejected as invalid", "status", status, "code", code)
	default:
		slog.Warn("node telemetry was not accepted", "status", status, "code", code)
		n.telFailed(&wait)
	}
}

// telFailed counts a failed report and lengthens the wait to max(interval, backoff).
func (n *nodeRun) telFailed(wait *time.Duration) {
	n.telFailures++
	*wait = max(n.interval, ComputeBackoffWithJitter(n.telFailures, nodeBackoffBase, nodeBackoffMax))
}
