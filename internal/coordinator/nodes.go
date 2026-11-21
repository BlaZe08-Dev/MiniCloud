package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Node settings defaults (ADR-0022 Section 4.1, audit A-04). Both are placeholders, not measured;
// cmd/coordinator overrides them from MINICLOUD_NODE_HARDWARE_MIN_INTERVAL and
// MINICLOUD_NODE_MAX_COUNT.
const (
	DefaultNodeHardwareMinInterval = 60 * time.Second
	DefaultNodeMaxCount            = 10000
)

// SetNodeSettings sets the hardware update interval and the node ceiling. Both must be positive.
func (s *Service) SetNodeSettings(minInterval time.Duration, maxCount int) error {
	if minInterval <= 0 {
		return fmt.Errorf("node hardware min interval must be positive, got %v", minInterval)
	}
	if maxCount <= 0 {
		return fmt.Errorf("node max count must be positive, got %d", maxCount)
	}
	s.nodeMinInterval, s.nodeMaxCount = minInterval, maxCount
	return nil
}

// RegisterNode validates a node registration (ADR-0022 Section 9) and stores it. The node ceiling is
// soft: before a new ID is registered, CountNodes is compared with the ceiling, but the check and
// the insert are not one transaction, so concurrent registrations of new IDs may overshoot it by at
// most the number of concurrent requests. A node that already exists is never refused by it.
func (s *Service) RegisterNode(ctx context.Context, reg domain.NodeRegistration) (*domain.NodeRegistrationResult, error) {
	if err := domain.ValidateNodeID(reg.NodeID); err != nil {
		return nil, err
	}
	if !domain.IsSupportedContractVersion(reg.ContractVersion) {
		return nil, domain.ErrUnsupportedContractVersion
	}
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetNode(ctx, reg.NodeID); err != nil {
		if !errors.Is(err, domain.ErrNodeNotFound) {
			return nil, err
		}
		n, err := s.repo.CountNodes(ctx)
		if err != nil {
			return nil, err
		}
		if n >= s.nodeMaxCount {
			slog.Warn("node limit reached; new node refused", "node_id", reg.NodeID, "class", "node_limit_reached")
			return nil, domain.ErrNodeLimitReached
		}
	}
	res, err := s.repo.RegisterNode(ctx, reg, s.nodeMinInterval)
	if err != nil {
		if errors.Is(err, domain.ErrNodeRateLimited) {
			slog.Info("node registration rate limited", "node_id", reg.NodeID, "class", "rate_limited")
		}
		return nil, err
	}
	return res, nil
}

// RecordNodeTelemetry validates and stores a telemetry report. The rule that a sample age must not
// put sampled_at before the node's created_at is not enforced by storage (the stored sampled_at is
// repository time minus the age), so it is checked here against the node read through GetNode with
// the coordinator clock. An unknown node returns domain.ErrNodeNotFound before anything is written.
func (s *Service) RecordNodeTelemetry(ctx context.Context, nodeID string, report domain.NodeTelemetryReport) error {
	if err := domain.ValidateNodeID(nodeID); err != nil {
		return err
	}
	if !domain.IsSupportedContractVersion(report.ContractVersion) {
		return domain.ErrUnsupportedContractVersion
	}
	if err := report.Validate(); err != nil {
		return err
	}
	node, err := s.repo.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	sampledAt := s.clock.Now().Add(-time.Duration(report.SampleAgeMS) * time.Millisecond)
	if sampledAt.Before(node.CreatedAt) {
		return fmt.Errorf("%w: sample_age_ms puts sampled_at before the node was created", domain.ErrInvalidRequest)
	}
	return s.repo.RecordNodeTelemetry(ctx, nodeID, report)
}

// GetNode returns the read model of one node.
func (s *Service) GetNode(ctx context.Context, nodeID string) (*domain.NodeView, error) {
	if err := domain.ValidateNodeID(nodeID); err != nil {
		return nil, err
	}
	return s.repo.GetNode(ctx, nodeID)
}

// ListNodes returns one page of nodes after afterID ("" for the start), at most limit.
func (s *Service) ListNodes(ctx context.Context, afterID string, limit int) ([]*domain.NodeView, error) {
	if err := domain.ValidateNodePageLimit(limit); err != nil {
		return nil, err
	}
	if afterID != "" {
		if err := domain.ValidateNodeID(afterID); err != nil {
			return nil, err
		}
	}
	return s.repo.ListNodes(ctx, afterID, limit)
}

// NodeNow returns the coordinator clock, used by the API for the display-only telemetry age.
func (s *Service) NodeNow() time.Time { return s.clock.Now() }
