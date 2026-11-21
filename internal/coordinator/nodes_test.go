package coordinator_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// telemetrySpyRepo embeds the real in-memory repository and counts the calls that the telemetry
// path must make (exactly one RecordNodeTelemetry) and must never make (node view reads).
type telemetrySpyRepo struct {
	storage.Repository
	getNode, listNodes, countNodes, record atomic.Int64
}

func (r *telemetrySpyRepo) GetNode(ctx context.Context, id string) (*domain.NodeView, error) {
	r.getNode.Add(1)
	return r.Repository.GetNode(ctx, id)
}
func (r *telemetrySpyRepo) ListNodes(ctx context.Context, after string, limit int) ([]*domain.NodeView, error) {
	r.listNodes.Add(1)
	return r.Repository.ListNodes(ctx, after, limit)
}
func (r *telemetrySpyRepo) CountNodes(ctx context.Context) (int, error) {
	r.countNodes.Add(1)
	return r.Repository.CountNodes(ctx)
}
func (r *telemetrySpyRepo) RecordNodeTelemetry(ctx context.Context, id string, rep domain.NodeTelemetryReport) error {
	r.record.Add(1)
	return r.Repository.RecordNodeTelemetry(ctx, id, rep)
}

// TestCoordinator_RecordNodeTelemetry_NoNodeViewReads guards audit F-01: the telemetry path reads
// no node view (GetNode, ListNodes, CountNodes) and calls the repository exactly once per report,
// for accepted and for rejected reports alike. (Mutant: the coordinator calls GetNode again.)
func TestCoordinator_RecordNodeTelemetry_NoNodeViewReads(t *testing.T) {
	ctx := context.Background()
	clk := newMockClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	spy := &telemetrySpyRepo{Repository: storage.NewInMemoryRepository(storage.WithClock(clk))}
	svc := coordinator.NewService(spy, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetClock(clk)

	id, session := "abc00001-0000-4000-8000-000000000001", "abc00064-0000-4000-8000-000000000064"
	mem := int64(100)
	reg := domain.NodeRegistration{NodeID: id, DisplayName: "n", ContractVersion: domain.ContractVersion, AgentVersion: "v1",
		Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64", MemoryTotalBytes: &mem}}
	if _, err := svc.RegisterNode(ctx, reg); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	spy.getNode.Store(0)
	spy.listNodes.Store(0)
	spy.countNodes.Store(0)

	report := func(seq, age int64) domain.NodeTelemetryReport {
		return domain.NodeTelemetryReport{ContractVersion: domain.ContractVersion, SessionID: session, Seq: seq, SampleAgeMS: age}
	}
	calls := 0
	for _, c := range []struct {
		seq, age int64
		wantErr  error
	}{
		{1, 0, nil},
		{2, 1000, nil},
		{2, 0, domain.ErrTelemetryOutOfOrder},
		{3, 3600000, domain.ErrInvalidRequest}, // older than the node: rejected by storage
	} {
		err := svc.RecordNodeTelemetry(ctx, id, report(c.seq, c.age))
		calls++
		if c.wantErr == nil && err != nil {
			t.Fatalf("seq %d: %v", c.seq, err)
		}
		if c.wantErr != nil && !errors.Is(err, c.wantErr) {
			t.Fatalf("seq %d: err = %v, want %v", c.seq, err, c.wantErr)
		}
		if got := spy.record.Load(); got != int64(calls) {
			t.Fatalf("after %d reports RecordNodeTelemetry was called %d times, want %d (one per report)", calls, got, calls)
		}
	}
	if g, l, n := spy.getNode.Load(), spy.listNodes.Load(), spy.countNodes.Load(); g != 0 || l != 0 || n != 0 {
		t.Fatalf("telemetry path read node views: GetNode %d, ListNodes %d, CountNodes %d, want 0 0 0", g, l, n)
	}
}
