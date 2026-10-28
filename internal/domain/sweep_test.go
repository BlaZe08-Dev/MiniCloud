package domain

import (
	"testing"
	"time"
)

func TestDomain_SweepGraceState_Suppresses(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	later := now.Add(10 * time.Second)
	earlier := now.Add(-10 * time.Second)

	cases := []struct {
		name  string
		grace SweepGraceState
		want  bool
	}{
		{"zero_state_never_suppresses", SweepGraceState{}, false},
		{"startup_grace_in_future", SweepGraceState{StartupGraceUntil: later}, true},
		{"startup_grace_elapsed", SweepGraceState{StartupGraceUntil: earlier}, false},
		{"startup_grace_ends_exactly_now", SweepGraceState{StartupGraceUntil: now}, false},
		{"outage_window_open", SweepGraceState{OutageWindowUntil: later}, true},
		{"outage_window_closed", SweepGraceState{OutageWindowUntil: earlier}, false},
		{"cooldown_active_reaping_proceeds_despite_open_window", SweepGraceState{OutageWindowUntil: later, CooldownUntil: later}, false},
		{"cooldown_elapsed_window_still_open", SweepGraceState{OutageWindowUntil: later, CooldownUntil: earlier}, true},
		{"cooldown_does_not_cancel_startup_grace", SweepGraceState{StartupGraceUntil: later, CooldownUntil: later}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.grace.Suppresses(now); got != tc.want {
				t.Fatalf("Suppresses(%+v) = %v, want %v", tc.grace, got, tc.want)
			}
		})
	}
}
