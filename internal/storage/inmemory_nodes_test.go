package storage_test

import (
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// newInMemoryNodeHarness builds the shared node harness over a fresh in-memory repository with an
// injected clock, which is the in-memory "database time".
func newInMemoryNodeHarness(t *testing.T) storagetest.NodeHarness {
	repo, clk := newAssignRepo(t)
	return storagetest.NodeHarness{
		Repo:    repo,
		Advance: func(t *testing.T, d time.Duration) { clk.Advance(d) },
		Dump: func(t *testing.T, nodeID string) storagetest.NodeDump {
			t.Helper()
			raw := repo.NodeRawForTest(nodeID)
			d := storagetest.NodeDump{Exists: raw.Exists, DisplayName: raw.DisplayName}
			if !raw.Exists {
				return d
			}
			d.CreatedAt, d.LastSeenAt = ts(raw.CreatedAt), ts(raw.LastSeenAt)
			for _, h := range raw.Hardware {
				d.Hardware = append(d.Hardware, storagetest.NodeHardwareRow{
					Version: h.Version, ContractVersion: h.ContractVersion, AgentVersion: h.AgentVersion,
					Hardware: h.Hardware, ContentHash: h.ContentHash, ReportedAt: ts(h.ReportedAt),
				})
			}
			if r := raw.Telemetry; r != nil {
				d.Telemetry = &storagetest.NodeTelemetryRow{
					ContractVersion: r.ContractVersion, SessionID: r.SessionID, Seq: r.Seq,
					ReceivedAt: ts(r.ReceivedAt), SampledAt: ts(r.SampledAt), Telemetry: r.Telemetry,
				}
			}
			return d
		},
	}
}

var _ storage.Repository = (*storage.InMemoryRepository)(nil)

func TestConformance_Node_RegisterIdempotent(t *testing.T) {
	storagetest.RunNodeRegisterIdempotentConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_InvalidRegistrationRejected(t *testing.T) {
	storagetest.RunNodeInvalidRegistrationConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_HardwareVersionOnChangeOnly(t *testing.T) {
	storagetest.RunNodeHardwareVersionOnChangeOnlyConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_HardwareHistoryKeepsLatest10(t *testing.T) {
	storagetest.RunNodeHardwareHistoryKeepsLatest10Conformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_RejectsChangeInsideInterval(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitRejectsInsideIntervalConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_AllowsAfterInterval(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitAllowsAfterIntervalConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_UnchangedNeverLimited(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitUnchangedNeverLimitedConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_RateLimit_ErrorCarriesRetryAfter(t *testing.T) {
	storagetest.RunNodeRateLimitErrorCarriesRetryAfterConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_OutOfOrderRejected(t *testing.T) {
	storagetest.RunNodeTelemetryOutOfOrderRejectedConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_ReplaySameSeqRejected(t *testing.T) {
	storagetest.RunNodeTelemetryReplaySameSeqRejectedConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_NewSessionAcceptedAfterRestart(t *testing.T) {
	storagetest.RunNodeTelemetryNewSessionAcceptedAfterRestartConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_OlderSessionAfterNewerIsAccepted(t *testing.T) {
	storagetest.RunNodeTelemetryOlderSessionAfterNewerIsAcceptedConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_UnknownNodeNotFound(t *testing.T) {
	storagetest.RunNodeTelemetryUnknownNodeConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_TelemetryDoesNotTouchHardware(t *testing.T) {
	storagetest.RunNodeTelemetryDoesNotTouchHardwareConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_UnknownStaysNull(t *testing.T) {
	storagetest.RunNodeUnknownStaysNullConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Views(t *testing.T) {
	storagetest.RunNodeViewsConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Worker_NodeIDRoundTrip(t *testing.T) {
	storagetest.RunWorkerNodeIDRoundTripConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Worker_UnknownNodeRejected(t *testing.T) {
	storagetest.RunWorkerUnknownNodeRejectedConformance(t, newInMemoryNodeHarness)
}
