// Package workertest holds deterministic test doubles for the worker agent.
package workertest

import (
	"sort"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
)

// FakeClock is a manually advanced clock. It satisfies worker.Clock for the agent's timers and the
// Now() contract of the coordinator and storage clocks, so one instance can drive the worker, the
// coordinator service and the in-memory repository of an end-to-end test.
type FakeClock struct {
	mu        sync.Mutex
	now       time.Time
	timers    []*fakeTimer
	requested []time.Duration
}

type fakeTimer struct {
	clock    *FakeClock
	deadline time.Time
	ch       chan time.Time
}

// NewFakeClock returns a clock frozen at start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer registers a timer that fires when the clock is advanced past d.
func (c *FakeClock) NewTimer(d time.Duration) worker.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.requested = append(c.requested, d)
	return t
}

// Advance moves the clock forward by d and fires every timer that is due, earliest first.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].deadline.Before(c.timers[j].deadline) })
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.deadline.After(c.now) {
			kept = append(kept, t)
			continue
		}
		t.ch <- c.now
	}
	c.timers = kept
}

// Pending is the number of timers that have not fired and are not stopped.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Requested lists the duration of every timer ever created, in creation order.
func (c *FakeClock) Requested() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.requested...)
}

// BlockUntil waits (in millisecond polls, bounded by timeout) until at least n timers are pending,
// which means the goroutines under test have reached their next wait.
func (c *FakeClock) BlockUntil(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Pending() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return c.Pending() >= n
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	for i, x := range t.clock.timers {
		if x == t {
			t.clock.timers = append(t.clock.timers[:i], t.clock.timers[i+1:]...)
			return true
		}
	}
	return false
}
