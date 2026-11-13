package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

// pollTimingConfig loads the coordinator configuration with the memory driver, every round-robin
// variable at its default, and MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW set to window.
func pollTimingConfig(t *testing.T, window string) (*config.CoordinatorConfig, error) {
	t.Helper()
	t.Setenv("MINICLOUD_STORAGE_DRIVER", "memory")
	for _, k := range []string{
		"MINICLOUD_DISPATCH_POLICY", "MINICLOUD_ASSIGNMENT_INTERVAL", "MINICLOUD_ASSIGNMENT_MAX_LAPSES",
		"MINICLOUD_ASSIGNMENT_FALLBACK_AGE", "MINICLOUD_ASSIGNMENT_BATCH_LIMIT", "MINICLOUD_ASSIGNMENT_MIN_YIELD",
	} {
		// t.Setenv restores the previous value at cleanup; Unsetenv lets the default apply.
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW", window)
	return config.LoadCoordinatorConfig()
}

// acceptWindowMargin is the slack INV-17 requires between the accept window floor and the worker's
// maximum poll backoff, for the poll round trip and clock slack (ADR-0017 Section 3.1 and note
// 5c-2.4). The ADR gives no number; 1 s is the V1 choice. Changing MinAcceptWindow or
// MaxPollBackoff so that the difference drops below it must re-justify this value in the ADR.
const acceptWindowMargin = 1 * time.Second

// TestPollTiming_AcceptWindowExceedsBackoff is INV-17 (ADR-0017 Section 3.1, R6). The accept
// window floor must exceed the worker's maximum poll backoff by at least acceptWindowMargin, and
// both the configuration and the service must refuse a window below the floor.
//
// The ADR states the requirement as "window > maximum poll backoff + round trip + margin" and gives
// no number for the round trip or the margin, so the margin is the named constant above.
func TestPollTiming_AcceptWindowExceedsBackoff(t *testing.T) {
	floor := coordinator.MinAcceptWindow

	t.Run("relation", func(t *testing.T) {
		if slack := floor - worker.MaxPollBackoff; slack < acceptWindowMargin {
			t.Fatalf("accept window floor %v exceeds the maximum poll backoff %v by %v, less than the margin %v", floor, worker.MaxPollBackoff, slack, acceptWindowMargin)
		}
		if floor <= worker.IdlePollInterval {
			t.Fatalf("accept window floor %v does not exceed the idle poll interval %v", floor, worker.IdlePollInterval)
		}
		t.Logf("slack for round trip and margin: %v (floor %v, max poll backoff %v, required margin %v)", floor-worker.MaxPollBackoff, floor, worker.MaxPollBackoff, acceptWindowMargin)
	})

	t.Run("backoff never exceeds the cap", func(t *testing.T) {
		// The cap is the largest sleep only if the jittered backoff never exceeds it (Section 3.1:
		// jitter into [0.5, 1.0) of the cap). 1,000 draws per failure count; with the cap removed,
		// 200 ms x 16 = 3.2 s exceeds 3 s whenever the jitter is above 0.9375 (one draw in eight).
		for failures := 1; failures <= 64; failures++ {
			for i := 0; i < 1000; i++ {
				if d := worker.ComputeBackoffWithJitter(failures, worker.IdlePollInterval, worker.MaxPollBackoff); d > worker.MaxPollBackoff {
					t.Fatalf("poll backoff %v after %d failures exceeds the maximum %v", d, failures, worker.MaxPollBackoff)
				}
			}
		}
	})

	t.Run("config enforces the floor", func(t *testing.T) {
		below := (floor - time.Millisecond).String()
		if _, err := pollTimingConfig(t, below); err == nil {
			t.Fatalf("config accepted MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW=%s below the floor %v", below, floor)
		}
		cfg, err := pollTimingConfig(t, floor.String())
		if err != nil {
			t.Fatalf("config rejected the floor %v: %v", floor, err)
		}
		if cfg.AssignmentAcceptWindow != floor {
			t.Fatalf("config accept window = %v, want %v", cfg.AssignmentAcceptWindow, floor)
		}
	})

	t.Run("service enforces the floor", func(t *testing.T) {
		s := coordinator.DefaultAssignmentSettings()
		s.AcceptWindow = floor - time.Millisecond
		if err := s.Validate(); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("service accepted accept window %v below the floor %v: err=%v", s.AcceptWindow, floor, err)
		}
		s.AcceptWindow = floor
		if err := s.Validate(); err != nil {
			t.Fatalf("service rejected the floor %v: %v", floor, err)
		}
	})
}

// recordingClock fires every timer at once and records the requested durations, so a test sees
// each poll sleep of Agent.Run without waiting for it.
type recordingClock struct {
	mu        sync.Mutex
	requested []time.Duration
}

type firedTimer struct{ ch chan time.Time }

func (t firedTimer) C() <-chan time.Time { return t.ch }
func (t firedTimer) Stop() bool          { return false }

func (c *recordingClock) NewTimer(d time.Duration) worker.Timer {
	c.mu.Lock()
	c.requested = append(c.requested, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return firedTimer{ch: ch}
}

func (c *recordingClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.requested...)
}

// TestPollTiming_RunSleepsWithinMaxBackoff is the INV-17 relation at the worker loop: every sleep
// Agent.Run takes between polls is at most MaxPollBackoff, and an empty or WORKER_UNHEALTHY poll
// sleeps exactly IdlePollInterval. The coordinator answers 300 polls with 500 (the outage path),
// one with 204 (which resets the failure count), 300 with 400 (the other error path), 20 with 503
// WORKER_UNHEALTHY, then 204. Each error streak must reach the capped region on its own.
func TestPollTiming_RunSleepsWithinMaxBackoff(t *testing.T) {
	const outage, other, unhealthy, total = 300, 300, 20, 680
	const otherStart = outage + 1 // sleep index of the first 400 poll
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var polls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers/register", "/v1/workers/w-timing/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
		case "/v1/workers/w-timing/poll":
			n := polls.Add(1)
			switch {
			case n >= total:
				cancel()
				w.WriteHeader(http.StatusNoContent)
			case n <= outage:
				w.WriteHeader(http.StatusInternalServerError)
			case n == outage+1:
				w.WriteHeader(http.StatusNoContent)
			case n <= outage+1+other:
				w.WriteHeader(http.StatusBadRequest)
			case n <= outage+1+other+unhealthy:
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "WORKER_UNHEALTHY", "message": "x"}})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	clk := &recordingClock{}
	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "w-timing", CoordinatorURL: ts.URL, HeartbeatInterval: time.Hour, TotalSlots: 1}, nil)
	agent.SetClock(clk)
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(60 * time.Second):
		cancel()
		t.Fatalf("Run did not finish; %d polls", polls.Load())
	}

	sleeps := clk.sleeps()
	if len(sleeps) < total-1 {
		t.Fatalf("recorded %d poll sleeps, want at least %d", len(sleeps), total-1)
	}
	var longestOutage, longestOther time.Duration
	for i, d := range sleeps {
		if d <= 0 {
			t.Fatalf("poll sleep %d is %v, want a positive sleep", i, d)
		}
		if d > worker.MaxPollBackoff {
			t.Fatalf("poll sleep %d is %v, above the maximum poll backoff %v", i, d, worker.MaxPollBackoff)
		}
		if i < outage && d > longestOutage {
			longestOutage = d
		}
		if i >= otherStart && i < otherStart+other && d > longestOther {
			longestOther = d
		}
	}
	// Each failure streak must reach the capped region, or the bound above was never exercised.
	if longestOutage <= worker.MaxPollBackoff/2 || longestOther <= worker.MaxPollBackoff/2 {
		t.Fatalf("longest error backoff (outage %v, other %v) never reached the capped region above %v", longestOutage, longestOther, worker.MaxPollBackoff/2)
	}
	if sleeps[outage] != worker.IdlePollInterval {
		t.Fatalf("poll sleep %d after the 204 between the streaks is %v, want %v", outage, sleeps[outage], worker.IdlePollInterval)
	}
	for i := otherStart + other; i < total-1; i++ {
		if sleeps[i] != worker.IdlePollInterval {
			t.Fatalf("poll sleep %d after an idle or WORKER_UNHEALTHY poll is %v, want %v", i, sleeps[i], worker.IdlePollInterval)
		}
	}
}

// TestPollTiming_RealClockSleeps runs Agent.Run on the production clock (no SetClock) against a
// fake coordinator and measures the real gap between polls, so the clock the poll loop waits on is
// covered (review F-R4; an earlier version of this loop slept with time.Sleep). The gap is the
// sleep plus one local round trip. Sequence: 5 x 204, 4 x 500, 1 x 503 WORKER_UNHEALTHY, then 204
// with a cancel. After a 204 or WORKER_UNHEALTHY the gap is in [IdlePollInterval, IdlePollInterval
// + slack]; after the k-th 500 it is in [c_k/2, c_k + slack], c_k = min(IdlePollInterval x
// 2^(k-1), MaxPollBackoff). A clock that fires early fails the lower bounds. About 3.5 s.
func TestPollTiming_RealClockSleeps(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var at []time.Time
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers/register", "/v1/workers/w-realclock/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ACK"})
		case "/v1/workers/w-realclock/poll":
			mu.Lock()
			at = append(at, time.Now())
			n := len(at)
			mu.Unlock()
			switch {
			case n <= 5:
				w.WriteHeader(http.StatusNoContent)
			case n <= 9:
				w.WriteHeader(http.StatusInternalServerError)
			case n == 10:
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "WORKER_UNHEALTHY", "message": "x"}})
			default:
				cancel()
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "w-realclock", CoordinatorURL: ts.URL, HeartbeatInterval: time.Hour, TotalSlots: 1}, nil)
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("Run did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(at) < 11 {
		t.Fatalf("only %d polls", len(at))
	}
	const slack = 150 * time.Millisecond
	for i := 1; i <= 10; i++ {
		gap := at[i].Sub(at[i-1])
		lo, hi := worker.IdlePollInterval, worker.IdlePollInterval+slack
		if i >= 6 && i <= 9 { // the gap after the (i-5)-th 500
			c := worker.IdlePollInterval << (i - 6)
			if c > worker.MaxPollBackoff {
				c = worker.MaxPollBackoff
			}
			lo, hi = c/2, c+slack
		}
		t.Logf("gap %2d: %v, bound [%v, %v]", i, gap, lo, hi)
		if gap < lo || gap > hi {
			t.Errorf("gap %d = %v outside [%v, %v]", i, gap, lo, hi)
		}
	}
}
