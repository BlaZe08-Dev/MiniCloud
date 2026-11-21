package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
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

func TestConformance_Node_ListEmpty(t *testing.T) {
	storagetest.RunNodeListEmptyConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Paging(t *testing.T) {
	storagetest.RunNodePagingConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Worker_NodeIDRoundTrip(t *testing.T) {
	storagetest.RunWorkerNodeIDRoundTripConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Worker_UnknownNodeRejected(t *testing.T) {
	storagetest.RunWorkerUnknownNodeRejectedConformance(t, newInMemoryNodeHarness)
}

// TestInMemoryRepository_NodeReads_VisitEachWorkerOncePerCall pins audit A-03: GetNode and ListNodes
// build one node-to-worker map per call, so the number of worker entries visited equals the worker
// count and not nodes x workers. A counter, not a timing assertion.
func TestInMemoryRepository_NodeReads_VisitEachWorkerOncePerCall(t *testing.T) {
	repo, _ := newAssignRepo(t)
	ctx := context.Background()
	const nodes, workers = 12, 7
	for n := 1; n <= nodes; n++ {
		reg := domain.NodeRegistration{NodeID: storagetest.NodeTestID(n), DisplayName: "n", ContractVersion: domain.ContractVersion, AgentVersion: "v", Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}
		if _, err := repo.RegisterNode(ctx, reg, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	for w := 0; w < workers; w++ {
		node := storagetest.NodeTestID(1 + w%nodes)
		if err := repo.UpsertWorker(ctx, &domain.Worker{ID: fmt.Sprintf("w_%d", w), Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &node}); err != nil {
			t.Fatal(err)
		}
	}
	var listed []*domain.NodeView
	if got := repo.WorkerEntriesVisitedForTest(func() {
		var err error
		if listed, err = repo.ListNodes(ctx, "", domain.MaxNodePageSize); err != nil {
			t.Fatal(err)
		}
	}); got != workers || len(listed) != nodes {
		t.Fatalf("ListNodes of %d nodes visited %d worker entries (nodes=%d), want exactly the worker count %d", nodes, got, len(listed), workers)
	}
	if got := repo.WorkerEntriesVisitedForTest(func() {
		if _, err := repo.GetNode(ctx, storagetest.NodeTestID(1)); err != nil {
			t.Fatal(err)
		}
	}); got != workers {
		t.Fatalf("GetNode visited %d worker entries, want %d", got, workers)
	}
	if got := repo.WorkerEntriesVisitedForTest(func() {
		if _, err := repo.ListNodes(ctx, storagetest.NodeTestID(10), 5); err != nil {
			t.Fatal(err)
		}
	}); got != workers {
		t.Fatalf("a short page visited %d worker entries, want %d", got, workers)
	}
}

func TestConformance_Node_Telemetry_SampleAgeBeforeCreatedAtRejected(t *testing.T) {
	storagetest.RunNodeTelemetrySampleAgeBeforeCreatedAtRejectedConformance(t, newInMemoryNodeHarness)
}

func TestConformance_Node_Telemetry_SampledAtEqualsCheckedValue(t *testing.T) {
	storagetest.RunNodeTelemetrySampledAtEqualsCheckedValueConformance(t, newInMemoryNodeHarness)
}
