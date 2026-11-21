package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Node storage (ADR-0022, slice V2-1b).
//
// Locking: node registration locks the node row FOR NO KEY UPDATE and never FOR UPDATE
// (ADR-0022 Section 7.4: the foreign keys take FOR KEY SHARE, which does not conflict with it).
// Telemetry takes FOR KEY SHARE on the node row. No existing path takes a node lock, and the
// documented lock order (jobs, workers, attempts, results, job_events) is unchanged.

// fkWorkersNode is the constraint of workers.node_id. mapPgError maps a violation of exactly this
// constraint to domain.ErrNodeNotFound (task V2-1b clarification K2).
const fkWorkersNode = "fk_workers_node"

// fkNodeTelemetryNode is the default name PostgreSQL gives the inline foreign key of
// node_telemetry.node_id; RecordNodeTelemetry maps exactly this one to ErrNodeNotFound as defence
// in depth behind its existence check.
const fkNodeTelemetryNode = "node_telemetry_node_id_fkey"

// RegisterNode implements storage.Repository, following ADR-0022 Section 8.1 step by step. Database
// time is read once, after the node row lock, and that single value serves the rate-limit decision
// and every timestamp the transaction writes, so the decision and the stored time agree.
func (r *Repository) RegisterNode(ctx context.Context, reg domain.NodeRegistration, minInterval time.Duration) (*domain.NodeRegistrationResult, error) {
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	hash := reg.Hardware.ContentHash()

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Step 1: create the node if it is new; write nothing for an existing one.
	if _, err := tx.Exec(ctx, `
		INSERT INTO nodes (id, display_name, created_at, last_seen_at)
		VALUES ($1, $2, clock_timestamp(), clock_timestamp())
		ON CONFLICT (id) DO NOTHING;`, reg.NodeID, reg.DisplayName); err != nil {
		return nil, mapPgError(err)
	}

	// Step 2: serialise concurrent registrations of this ID without a key update.
	var storedName string
	var lastSeenAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT display_name, last_seen_at FROM nodes WHERE id = $1 FOR NO KEY UPDATE;`, reg.NodeID).Scan(&storedName, &lastSeenAt); err != nil {
		return nil, mapPgError(err)
	}

	// Database time, once, after the lock.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp();`).Scan(&now); err != nil {
		return nil, mapPgError(err)
	}

	// Step 3: the newest hardware row.
	var (
		haveRow    bool
		newestVer  int
		newestHash string
		reportedAt time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT version, content_hash, reported_at FROM node_hardware
		WHERE node_id = $1 ORDER BY version DESC LIMIT 1;`, reg.NodeID).Scan(&newestVer, &newestHash, &reportedAt)
	switch {
	case err == nil:
		haveRow = true
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, mapPgError(err)
	}

	updateNode := func() error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET display_name = $2, last_seen_at = $3 WHERE id = $1;`, reg.NodeID, reg.DisplayName, now)
		return err
	}
	insertHardware := func(version int) error {
		hw := reg.Hardware
		_, err := tx.Exec(ctx, `
			INSERT INTO node_hardware (node_id, version, contract_version, agent_version, os, arch, cpu_model,
				cpu_logical_cores, cpu_physical_cores, memory_total_bytes, disk_total_bytes, gpu_count, gpu_model,
				gpu_vram_total_bytes, content_hash, reported_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);`,
			reg.NodeID, version, reg.ContractVersion, reg.AgentVersion, hw.OS, hw.Arch, hw.CPUModel,
			hw.CPULogicalCores, hw.CPUPhysicalCores, hw.MemoryTotalBytes, hw.DiskTotalBytes, hw.GPUCount, hw.GPUModel,
			hw.GPUVRAMTotalBytes, hash, now)
		return err
	}

	// Step 4: decide on database time.
	version := newestVer
	switch {
	case !haveRow:
		if err := updateNode(); err != nil {
			return nil, mapPgError(err)
		}
		version = 1
		if err := insertHardware(version); err != nil {
			return nil, mapPgError(err)
		}
	case newestHash == hash:
		// Same hash: no hardware row, never limited. Write the nodes row only when the name
		// changed or last_seen_at is at least one interval old.
		_, seenRecently := domain.HardwareUpdateLimit(lastSeenAt, now, minInterval)
		if storedName != reg.DisplayName || !seenRecently {
			if err := updateNode(); err != nil {
				return nil, mapPgError(err)
			}
		}
	default:
		if retryAfter, limited := domain.HardwareUpdateLimit(reportedAt, now, minInterval); limited {
			// Nothing was written for an existing node; the deferred rollback also covers the
			// no-op insert of step 1.
			return nil, &domain.NodeRateLimitedError{RetryAfter: retryAfter}
		}
		if err := updateNode(); err != nil {
			return nil, mapPgError(err)
		}
		version = newestVer + 1
		if err := insertHardware(version); err != nil {
			return nil, mapPgError(err)
		}
		// Keep exactly the newest MaxHardwareHistory rows; the current row is never pruned.
		if _, err := tx.Exec(ctx, `DELETE FROM node_hardware WHERE node_id = $1 AND version <= $2;`,
			reg.NodeID, version-domain.MaxHardwareHistory); err != nil {
			return nil, mapPgError(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}
	return &domain.NodeRegistrationResult{HardwareVersion: version}, nil
}

// RecordNodeTelemetry implements storage.Repository (ADR-0022 Section 3.3). One transaction: an
// existence check with FOR KEY SHARE (no row is ErrNodeNotFound and the upsert never runs), then
// the conditional upsert. Zero returned rows means the report was rejected and nothing changed.
func (r *Repository) RecordNodeTelemetry(ctx context.Context, nodeID string, report domain.NodeTelemetryReport) error {
	if err := report.Validate(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM nodes WHERE id = $1 FOR KEY SHARE;`, nodeID).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNodeNotFound
		}
		return mapPgError(err)
	}

	var seq int64
	err = tx.QueryRow(ctx, `
		WITH c(ts) AS (SELECT clock_timestamp())
		INSERT INTO node_telemetry AS t
			(node_id, contract_version, session_id, seq, received_at, sampled_at,
			 cpu_load_1m_milli, memory_available_bytes, disk_free_bytes)
		SELECT $1, $2, $3, $4, c.ts, c.ts - ($5 * interval '1 millisecond'), $6, $7, $8 FROM c
		ON CONFLICT (node_id) DO UPDATE SET
			contract_version = EXCLUDED.contract_version, session_id = EXCLUDED.session_id,
			seq = EXCLUDED.seq, received_at = EXCLUDED.received_at, sampled_at = EXCLUDED.sampled_at,
			cpu_load_1m_milli = EXCLUDED.cpu_load_1m_milli,
			memory_available_bytes = EXCLUDED.memory_available_bytes,
			disk_free_bytes = EXCLUDED.disk_free_bytes
		WHERE t.session_id <> EXCLUDED.session_id OR t.seq < EXCLUDED.seq
		RETURNING seq;`,
		nodeID, report.ContractVersion, report.SessionID, report.Seq, report.SampleAgeMS,
		report.CPULoad1mMilli, report.MemoryAvailableBytes, report.DiskFreeBytes).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrTelemetryOutOfOrder
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == fkNodeTelemetryNode {
			return domain.ErrNodeNotFound
		}
		return mapPgError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapPgError(err)
	}
	return nil
}

// GetNode implements storage.Repository.
func (r *Repository) GetNode(ctx context.Context, nodeID string) (*domain.NodeView, error) {
	views, err := r.loadNodeViews(ctx, &nodeID, "", 0)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, domain.ErrNodeNotFound
	}
	return views[0], nil
}

// ListNodes implements storage.Repository: it selects the page of node IDs first (id > afterID,
// ORDER BY id, LIMIT), then loads hardware, telemetry and worker IDs only for those IDs in the same
// read-only REPEATABLE READ transaction.
func (r *Repository) ListNodes(ctx context.Context, afterID string, limit int) ([]*domain.NodeView, error) {
	if err := domain.ValidateNodePageLimit(limit); err != nil {
		return nil, err
	}
	if strings.IndexByte(afterID, 0) >= 0 {
		return nil, fmt.Errorf("%w: cursor contains NUL", domain.ErrInvalidRequest)
	}
	return r.loadNodeViews(ctx, nil, afterID, limit)
}

// CountNodes implements storage.Repository.
func (r *Repository) CountNodes(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM nodes;`).Scan(&n); err != nil {
		return 0, mapPgError(err)
	}
	return n, nil
}

// loadNodeViews reads one node (only != nil) or one page of nodes (id > afterID, at most limit)
// with their current hardware, latest telemetry and worker IDs inside one read-only REPEATABLE READ
// transaction, so the view is one consistent snapshot. Rows are ordered by node ID and the result
// is never nil. The three child queries are restricted to the selected IDs.
func (r *Repository) loadNodeViews(ctx context.Context, only *string, afterID string, limit int) ([]*domain.NodeView, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var idQuery string
	var idArgs []any
	if only != nil {
		idQuery, idArgs = `SELECT id FROM nodes WHERE id = $1;`, []any{*only}
	} else {
		idQuery, idArgs = `SELECT id FROM nodes WHERE id > $1 ORDER BY id LIMIT $2;`, []any{afterID, limit}
	}
	idRows, err := tx.Query(ctx, idQuery, idArgs...)
	if err != nil {
		return nil, mapPgError(err)
	}
	ids := []string{}
	for idRows.Next() {
		var id string
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			return nil, mapPgError(err)
		}
		ids = append(ids, id)
	}
	idRows.Close()
	if err := idRows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	views := []*domain.NodeView{}
	if len(ids) == 0 {
		return views, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT id, display_name, created_at, last_seen_at FROM nodes
		WHERE id = ANY($1) ORDER BY id;`, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	byID := map[string]*domain.NodeView{}
	for rows.Next() {
		v := &domain.NodeView{WorkerIDs: []string{}}
		if err := rows.Scan(&v.ID, &v.DisplayName, &v.CreatedAt, &v.LastSeenAt); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		v.CreatedAt, v.LastSeenAt = v.CreatedAt.UTC(), v.LastSeenAt.UTC()
		views = append(views, v)
		byID[v.ID] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	hwRows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (node_id) node_id, version, contract_version, agent_version, os, arch, cpu_model,
		       cpu_logical_cores, cpu_physical_cores, memory_total_bytes, disk_total_bytes, gpu_count, gpu_model,
		       gpu_vram_total_bytes, reported_at
		FROM node_hardware
		WHERE node_id = ANY($1)
		ORDER BY node_id, version DESC;`, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	for hwRows.Next() {
		var nodeID string
		var h domain.NodeHardwareView
		if err := hwRows.Scan(&nodeID, &h.Version, &h.ContractVersion, &h.AgentVersion, &h.OS, &h.Arch, &h.CPUModel,
			&h.CPULogicalCores, &h.CPUPhysicalCores, &h.MemoryTotalBytes, &h.DiskTotalBytes, &h.GPUCount, &h.GPUModel,
			&h.GPUVRAMTotalBytes, &h.ReportedAt); err != nil {
			hwRows.Close()
			return nil, mapPgError(err)
		}
		h.ReportedAt = h.ReportedAt.UTC()
		if v := byID[nodeID]; v != nil {
			v.Hardware = &h
		}
	}
	hwRows.Close()
	if err := hwRows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	tRows, err := tx.Query(ctx, `
		SELECT node_id, contract_version, received_at, sampled_at, cpu_load_1m_milli, memory_available_bytes, disk_free_bytes
		FROM node_telemetry WHERE node_id = ANY($1);`, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	for tRows.Next() {
		var nodeID string
		var t domain.NodeTelemetryView
		if err := tRows.Scan(&nodeID, &t.ContractVersion, &t.ReceivedAt, &t.SampledAt,
			&t.CPULoad1mMilli, &t.MemoryAvailableBytes, &t.DiskFreeBytes); err != nil {
			tRows.Close()
			return nil, mapPgError(err)
		}
		t.ReceivedAt, t.SampledAt = t.ReceivedAt.UTC(), t.SampledAt.UTC()
		if v := byID[nodeID]; v != nil {
			v.Telemetry = &t
		}
	}
	tRows.Close()
	if err := tRows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	wRows, err := tx.Query(ctx, `
		SELECT node_id, id FROM workers
		WHERE node_id = ANY($1) ORDER BY id;`, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	for wRows.Next() {
		var nodeID, workerID string
		if err := wRows.Scan(&nodeID, &workerID); err != nil {
			wRows.Close()
			return nil, mapPgError(err)
		}
		if v := byID[nodeID]; v != nil {
			v.WorkerIDs = append(v.WorkerIDs, workerID)
		}
	}
	wRows.Close()
	if err := wRows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return views, nil
}
