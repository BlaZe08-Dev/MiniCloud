package storage

// HoldPassLockForTest takes the in-memory pass lock (the twin of the PostgreSQL advisory lock,
// ADR-0017 INV-22) and returns the function that releases it. It exists so the external tests can
// check that a pass which cannot take the lock skips.
func (r *InMemoryRepository) HoldPassLockForTest() (release func()) {
	r.passMu.Lock()
	return r.passMu.Unlock
}
