package storage

import (
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// HoldPassLockForTest takes the in-memory pass lock (the twin of the PostgreSQL advisory lock,
// ADR-0017 INV-22) and returns the function that releases it. It exists so the external tests can
// check that a pass which cannot take the lock skips.
func (r *InMemoryRepository) HoldPassLockForTest() (release func()) {
	r.passMu.Lock()
	return r.passMu.Unlock
}

// NodeRawHardwareForTest is one stored hardware row of the in-memory repository.
type NodeRawHardwareForTest struct {
	Version         int
	ContractVersion int
	AgentVersion    string
	Hardware        domain.NodeHardware
	ContentHash     string
	ReportedAt      time.Time
}

// NodeRawTelemetryForTest is the stored telemetry row of the in-memory repository.
type NodeRawTelemetryForTest struct {
	ContractVersion int
	SessionID       string
	Seq             int64
	ReceivedAt      time.Time
	SampledAt       time.Time
	Telemetry       domain.NodeTelemetry
}

// NodeRawForTest is the raw stored state of one in-memory node, including the columns the NodeView
// hides. It exists so the shared node conformance suite can compare stored state byte for byte.
type NodeRawForTest struct {
	Exists      bool
	DisplayName string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	Hardware    []NodeRawHardwareForTest
	Telemetry   *NodeRawTelemetryForTest
}

// NodeRawForTest returns a deep copy of the raw stored state of a node.
func (r *InMemoryRepository) NodeRawForTest(id string) NodeRawForTest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok {
		return NodeRawForTest{}
	}
	out := NodeRawForTest{Exists: true, DisplayName: n.displayName, CreatedAt: n.createdAt, LastSeenAt: n.lastSeenAt}
	for _, h := range n.hardware {
		out.Hardware = append(out.Hardware, NodeRawHardwareForTest{
			Version: h.version, ContractVersion: h.contractVersion, AgentVersion: h.agentVersion,
			Hardware: cloneHardware(h.hardware), ContentHash: h.contentHash, ReportedAt: h.reportedAt,
		})
	}
	if t := n.telemetry; t != nil {
		out.Telemetry = &NodeRawTelemetryForTest{
			ContractVersion: t.contractVersion, SessionID: t.sessionID, Seq: t.seq,
			ReceivedAt: t.receivedAt, SampledAt: t.sampledAt, Telemetry: cloneTelemetry(t.telemetry),
		}
	}
	return out
}

// WorkerEntriesVisitedForTest runs f and returns how many worker entries the node read paths
// visited while building worker-ID lists (audit A-03). It is deterministic: a count, not a timing.
func (r *InMemoryRepository) WorkerEntriesVisitedForTest(f func()) int {
	var n int
	workerScanHook = func() { n++ }
	defer func() { workerScanHook = nil }()
	f()
	return n
}
