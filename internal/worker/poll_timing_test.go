package worker_test

import (
	"errors"
	"os"
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

// TestPollTiming_AcceptWindowExceedsBackoff is INV-17 (ADR-0017 Section 3.1, R6). The accept
// window floor must exceed the worker's maximum poll backoff, and both the configuration and the
// service must refuse a window below the floor.
//
// The ADR states the requirement as "window > maximum poll backoff + round trip + margin" and gives
// no number for the round trip or the margin (implementation note 27), so the relation pinned here
// is the strict one the ADR's own example uses (5 s > 3 s), and the slack is logged.
func TestPollTiming_AcceptWindowExceedsBackoff(t *testing.T) {
	floor := coordinator.MinAcceptWindow

	t.Run("relation", func(t *testing.T) {
		if floor <= worker.MaxPollBackoff {
			t.Fatalf("accept window floor %v does not exceed the maximum poll backoff %v", floor, worker.MaxPollBackoff)
		}
		if floor <= worker.IdlePollInterval {
			t.Fatalf("accept window floor %v does not exceed the idle poll interval %v", floor, worker.IdlePollInterval)
		}
		t.Logf("slack for round trip and margin: %v (floor %v, max poll backoff %v)", floor-worker.MaxPollBackoff, floor, worker.MaxPollBackoff)
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
