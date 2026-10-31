package coordinator

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
