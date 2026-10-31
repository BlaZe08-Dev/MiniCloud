package coordinator

// SetRefreshInFlightForTest sets the single-flight flag of the poll-time refresh (ADR-0017 N6).
// It exists so the external coordinator tests can check that a poll never starts an attempt
// while one is in flight.
func (s *Service) SetRefreshInFlightForTest(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshInFlight = v
}
