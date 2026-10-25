package storage

import "time"

// Clock provides time abstraction for deterministic testing in InMemoryRepository.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
