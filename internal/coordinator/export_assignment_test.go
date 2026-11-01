package coordinator

import (
	"context"
	"time"
)

// SetRefreshInFlightForTest sets the single-flight flag of the poll-time refresh (ADR-0017 N6).
// It exists so the external coordinator tests can check that a poll never starts an attempt
// while one is in flight.
func (s *Service) SetRefreshInFlightForTest(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshInFlight = v
}

// SetAssignmentSettingsUncheckedForTest stores settings without Validate. It exists so the
// external tests can check that SetDispatchPolicy("round_robin") refuses invalid settings, a
// state SetAssignmentSettings itself cannot produce (F-5a-1).
func (s *Service) SetAssignmentSettingsUncheckedForTest(a AssignmentSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignment = a
}

// SetYieldForTest replaces the wait between a pass and its repeat (ADR-0017 N2, INV-20), so the
// external tests can run code inside the yield and measure it on an injected clock.
func (l *AssignmentLoop) SetYieldForTest(yield func(ctx context.Context, d time.Duration) error) {
	l.yield = yield
}

// YieldForTest returns the wait between a pass and its repeat that NewAssignmentLoop installed, so
// a test can run the production function instead of an injected one.
func (l *AssignmentLoop) YieldForTest() func(ctx context.Context, d time.Duration) error {
	return l.yield
}
