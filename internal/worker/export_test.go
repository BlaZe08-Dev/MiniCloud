package worker

import "context"

// The result-delivery backoff settings are unexported production constants. The external tests read
// them through these names so a row for that caller tracks the production values.
const (
	DeliveryBackoffBaseForTest = deliveryBackoffBase
	DeliveryBackoffMaxForTest  = deliveryBackoffMax
)

// Test seams for the bounded pool (ADR-0019). Hooks must be set before Run.

// SetSlotWaitHook sets the hook called when the poll loop finds no free slot and blocks.
func SetSlotWaitHook(a *Agent, f func()) { a.onSlotWait = f }

// SetAttemptDoneHook sets the hook called by an attempt goroutine right after it released its slot.
func SetAttemptDoneHook(a *Agent, f func()) { a.onAttemptDone = f }

// RunningForTest is the number of attempts currently holding a slot.
func RunningForTest(a *Agent) int64 { return a.running.Load() }

// AvailableSlotsForTest is the value register and heartbeat report now.
func AvailableSlotsForTest(a *Agent) int { return a.availableSlots() }

// SendHeartbeatForTest sends one heartbeat, as the heartbeat goroutine does on a tick.
func SendHeartbeatForTest(ctx context.Context, a *Agent) error { return a.sendHeartbeat(ctx) }

// CloseIdleConnectionsForTest closes the agent's idle HTTP connections (goroutine-leak checks).
func CloseIdleConnectionsForTest(a *Agent) { a.httpClient.CloseIdleConnections() }
