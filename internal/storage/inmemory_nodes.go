package storage

import (
	"context"
	"sort"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// In-memory node storage (ADR-0022, slice V2-1b). It follows the rules of the PostgreSQL
// implementation under the repository mutex, with the injected Clock standing in for database
// time, so both repositories behave identically through the conformance suite
// (storagetest.RunNodeConformanceTests).

type inMemoryHardwareRow struct {
	version         int
	contractVersion int
	agentVersion    string
	hardware        domain.NodeHardware
	contentHash     string
	reportedAt      time.Time
}

type inMemoryTelemetryRow struct {
	contractVersion int
	sessionID       string
	seq             int64
	receivedAt      time.Time
	sampledAt       time.Time
	telemetry       domain.NodeTelemetry
}

type inMemoryNode struct {
	id          string
	displayName string
	createdAt   time.Time
	lastSeenAt  time.Time
	hardware    []inMemoryHardwareRow // ascending version; the last element is current
	telemetry   *inMemoryTelemetryRow
}

func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneString(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneHardware(h domain.NodeHardware) domain.NodeHardware {
	h.CPUModel = cloneString(h.CPUModel)
	h.CPULogicalCores = cloneInt64(h.CPULogicalCores)
	h.CPUPhysicalCores = cloneInt64(h.CPUPhysicalCores)
	h.MemoryTotalBytes = cloneInt64(h.MemoryTotalBytes)
	h.DiskTotalBytes = cloneInt64(h.DiskTotalBytes)
	h.GPUCount = cloneInt64(h.GPUCount)
	h.GPUModel = cloneString(h.GPUModel)
	h.GPUVRAMTotalBytes = cloneInt64(h.GPUVRAMTotalBytes)
	return h
}

func cloneTelemetry(t domain.NodeTelemetry) domain.NodeTelemetry {
	t.CPULoad1mMilli = cloneInt64(t.CPULoad1mMilli)
	t.MemoryAvailableBytes = cloneInt64(t.MemoryAvailableBytes)
	t.DiskFreeBytes = cloneInt64(t.DiskFreeBytes)
	return t
}

// RegisterNode implements storage.Repository (ADR-0022 Section 8.1) under the repository mutex.
func (r *InMemoryRepository) RegisterNode(ctx context.Context, reg domain.NodeRegistration, minInterval time.Duration) (*domain.NodeRegistrationResult, error) {
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock.Now().UTC()
	hash := reg.Hardware.ContentHash()
	newRow := func(version int) inMemoryHardwareRow {
		return inMemoryHardwareRow{
			version:         version,
			contractVersion: reg.ContractVersion,
			agentVersion:    reg.AgentVersion,
			hardware:        cloneHardware(reg.Hardware),
			contentHash:     hash,
			reportedAt:      now,
		}
	}

	n, exists := r.nodes[reg.NodeID]
	if !exists {
		r.nodes[reg.NodeID] = &inMemoryNode{
			id:          reg.NodeID,
			displayName: reg.DisplayName,
			createdAt:   now,
			lastSeenAt:  now,
			hardware:    []inMemoryHardwareRow{newRow(1)},
		}
		return &domain.NodeRegistrationResult{HardwareVersion: 1}, nil
	}
	if len(n.hardware) == 0 {
		n.displayName, n.lastSeenAt = reg.DisplayName, now
		n.hardware = []inMemoryHardwareRow{newRow(1)}
		return &domain.NodeRegistrationResult{HardwareVersion: 1}, nil
	}

	newest := n.hardware[len(n.hardware)-1]
	if newest.contentHash == hash {
		// Unchanged hardware: never limited, never a hardware row. The nodes row is written only
		// when the name changed or last_seen_at is at least one interval old.
		_, seenRecently := domain.HardwareUpdateLimit(n.lastSeenAt, now, minInterval)
		if n.displayName != reg.DisplayName || !seenRecently {
			n.displayName, n.lastSeenAt = reg.DisplayName, now
		}
		return &domain.NodeRegistrationResult{HardwareVersion: newest.version}, nil
	}

	if retryAfter, limited := domain.HardwareUpdateLimit(newest.reportedAt, now, minInterval); limited {
		return nil, &domain.NodeRateLimitedError{RetryAfter: retryAfter}
	}
	n.displayName, n.lastSeenAt = reg.DisplayName, now
	n.hardware = append(n.hardware, newRow(newest.version+1))
	if over := len(n.hardware) - domain.MaxHardwareHistory; over > 0 {
		n.hardware = append([]inMemoryHardwareRow(nil), n.hardware[over:]...)
	}
	return &domain.NodeRegistrationResult{HardwareVersion: newest.version + 1}, nil
}

// RecordNodeTelemetry implements storage.Repository (ADR-0022 Section 3.3) under the repository
// mutex. The existence check comes first, so a missing node can never return ErrTelemetryOutOfOrder.
func (r *InMemoryRepository) RecordNodeTelemetry(ctx context.Context, nodeID string, report domain.NodeTelemetryReport) error {
	if err := report.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	n, exists := r.nodes[nodeID]
	if !exists {
		return domain.ErrNodeNotFound
	}
	if t := n.telemetry; t != nil && t.sessionID == report.SessionID && t.seq >= report.Seq {
		return domain.ErrTelemetryOutOfOrder
	}
	now := r.clock.Now().UTC()
	n.telemetry = &inMemoryTelemetryRow{
		contractVersion: report.ContractVersion,
		sessionID:       report.SessionID,
		seq:             report.Seq,
		receivedAt:      now,
		sampledAt:       now.Add(-time.Duration(report.SampleAgeMS) * time.Millisecond),
		telemetry:       cloneTelemetry(report.NodeTelemetry),
	}
	return nil
}

// GetNode implements storage.Repository.
func (r *InMemoryRepository) GetNode(ctx context.Context, nodeID string) (*domain.NodeView, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, exists := r.nodes[nodeID]
	if !exists {
		return nil, domain.ErrNodeNotFound
	}
	return r.nodeViewLocked(n), nil
}

// ListNodes implements storage.Repository.
func (r *InMemoryRepository) ListNodes(ctx context.Context) ([]*domain.NodeView, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	views := make([]*domain.NodeView, 0, len(ids))
	for _, id := range ids {
		views = append(views, r.nodeViewLocked(r.nodes[id]))
	}
	return views, nil
}

// nodeViewLocked builds the read model of n. Must be called with mu held.
func (r *InMemoryRepository) nodeViewLocked(n *inMemoryNode) *domain.NodeView {
	v := &domain.NodeView{
		ID:          n.id,
		DisplayName: n.displayName,
		CreatedAt:   n.createdAt,
		LastSeenAt:  n.lastSeenAt,
		WorkerIDs:   []string{},
	}
	if len(n.hardware) > 0 {
		cur := n.hardware[len(n.hardware)-1]
		v.Hardware = &domain.NodeHardwareView{
			Version:         cur.version,
			ContractVersion: cur.contractVersion,
			AgentVersion:    cur.agentVersion,
			ReportedAt:      cur.reportedAt,
			NodeHardware:    cloneHardware(cur.hardware),
		}
	}
	if t := n.telemetry; t != nil {
		v.Telemetry = &domain.NodeTelemetryView{
			ContractVersion: t.contractVersion,
			ReceivedAt:      t.receivedAt,
			SampledAt:       t.sampledAt,
			NodeTelemetry:   cloneTelemetry(t.telemetry),
		}
	}
	for id, w := range r.workers {
		if w.NodeID != nil && *w.NodeID == n.id {
			v.WorkerIDs = append(v.WorkerIDs, id)
		}
	}
	sort.Strings(v.WorkerIDs)
	return v
}
