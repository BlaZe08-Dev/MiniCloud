package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/storagetest"
)

// shiftNodeTimestampsBack moves every stored node timestamp back by d. The database clock cannot be
// moved, so this is how the node conformance cases let time pass, as shiftTimestampsBack does for
// the assignment cases: every comparison the repository makes is "stored time against
// clock_timestamp()". chk_node_telemetry_sample_order stays true because received_at and sampled_at
// move together.
func shiftNodeTimestampsBack(t *testing.T, pool *pgxpool.Pool, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE nodes SET created_at = created_at - $1::interval, last_seen_at = last_seen_at - $1::interval`,
		`UPDATE node_hardware SET reported_at = reported_at - $1::interval`,
		`UPDATE node_telemetry SET received_at = received_at - $1::interval, sampled_at = sampled_at - $1::interval`,
	} {
		if _, err := pool.Exec(ctx, q, d); err != nil {
			t.Fatalf("shift node timestamps back by %v: %v\n%s", d, err, q)
		}
	}
}

func fmtTS(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// dumpNode reads the raw stored state of one node straight from the tables.
func dumpNode(t *testing.T, pool *pgxpool.Pool, nodeID string) storagetest.NodeDump {
	t.Helper()
	ctx := context.Background()
	var d storagetest.NodeDump
	var created, seen time.Time
	err := pool.QueryRow(ctx, `SELECT display_name, created_at, last_seen_at FROM nodes WHERE id = $1`, nodeID).Scan(&d.DisplayName, &created, &seen)
	if err != nil {
		return storagetest.NodeDump{} // no such node
	}
	d.Exists, d.CreatedAt, d.LastSeenAt = true, fmtTS(created), fmtTS(seen)

	rows, err := pool.Query(ctx, `
		SELECT version, contract_version, agent_version, os, arch, cpu_model, cpu_logical_cores, cpu_physical_cores,
		       memory_total_bytes, disk_total_bytes, gpu_count, gpu_model, gpu_vram_total_bytes, content_hash, reported_at
		FROM node_hardware WHERE node_id = $1 ORDER BY version`, nodeID)
	if err != nil {
		t.Fatalf("dump hardware: %v", err)
	}
	for rows.Next() {
		var r storagetest.NodeHardwareRow
		var reported time.Time
		hw := &r.Hardware
		if err := rows.Scan(&r.Version, &r.ContractVersion, &r.AgentVersion, &hw.OS, &hw.Arch, &hw.CPUModel, &hw.CPULogicalCores,
			&hw.CPUPhysicalCores, &hw.MemoryTotalBytes, &hw.DiskTotalBytes, &hw.GPUCount, &hw.GPUModel, &hw.GPUVRAMTotalBytes,
			&r.ContentHash, &reported); err != nil {
			rows.Close()
			t.Fatalf("dump hardware scan: %v", err)
		}
		r.ReportedAt = fmtTS(reported)
		d.Hardware = append(d.Hardware, r)
	}
	rows.Close()

	var tr storagetest.NodeTelemetryRow
	var recv, samp time.Time
	err = pool.QueryRow(ctx, `
		SELECT contract_version, session_id, seq, received_at, sampled_at, cpu_load_1m_milli, memory_available_bytes, disk_free_bytes
		FROM node_telemetry WHERE node_id = $1`, nodeID).Scan(&tr.ContractVersion, &tr.SessionID, &tr.Seq, &recv, &samp,
		&tr.Telemetry.CPULoad1mMilli, &tr.Telemetry.MemoryAvailableBytes, &tr.Telemetry.DiskFreeBytes)
	if err == nil {
		tr.ReceivedAt, tr.SampledAt = fmtTS(recv), fmtTS(samp)
		d.Telemetry = &tr
	}
	return d
}

func newPostgresNodeHarnessWithPool(t *testing.T) (storagetest.NodeHarness, *pgxpool.Pool, *postgres.Repository) {
	pool, _ := setupTestPool(t)
	repo := postgres.NewRepository(pool)
	return storagetest.NodeHarness{
		Repo:    repo,
		Advance: func(t *testing.T, d time.Duration) { shiftNodeTimestampsBack(t, pool, d) },
		Dump:    func(t *testing.T, nodeID string) storagetest.NodeDump { return dumpNode(t, pool, nodeID) },
	}, pool, repo
}

func newPostgresNodeHarness(t *testing.T) storagetest.NodeHarness {
	h, _, _ := newPostgresNodeHarnessWithPool(t)
	return h
}

// The shared node conformance cases (ADR-0022 Section 14.2), run against PostgreSQL.

func TestConformance_Node_RegisterIdempotent(t *testing.T) {
	storagetest.RunNodeRegisterIdempotentConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_InvalidRegistrationRejected(t *testing.T) {
	storagetest.RunNodeInvalidRegistrationConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_HardwareVersionOnChangeOnly(t *testing.T) {
	storagetest.RunNodeHardwareVersionOnChangeOnlyConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_HardwareHistoryKeepsLatest10(t *testing.T) {
	storagetest.RunNodeHardwareHistoryKeepsLatest10Conformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_RejectsChangeInsideInterval(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitRejectsInsideIntervalConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_AllowsAfterInterval(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitAllowsAfterIntervalConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_HardwareRateLimit_UnchangedNeverLimited(t *testing.T) {
	storagetest.RunNodeHardwareRateLimitUnchangedNeverLimitedConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_RateLimit_ErrorCarriesRetryAfter(t *testing.T) {
	storagetest.RunNodeRateLimitErrorCarriesRetryAfterConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Telemetry_OutOfOrderRejected(t *testing.T) {
	storagetest.RunNodeTelemetryOutOfOrderRejectedConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Telemetry_ReplaySameSeqRejected(t *testing.T) {
	storagetest.RunNodeTelemetryReplaySameSeqRejectedConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Telemetry_NewSessionAcceptedAfterRestart(t *testing.T) {
	storagetest.RunNodeTelemetryNewSessionAcceptedAfterRestartConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Telemetry_OlderSessionAfterNewerIsAccepted(t *testing.T) {
	storagetest.RunNodeTelemetryOlderSessionAfterNewerIsAcceptedConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Telemetry_UnknownNodeNotFound(t *testing.T) {
	storagetest.RunNodeTelemetryUnknownNodeConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_TelemetryDoesNotTouchHardware(t *testing.T) {
	storagetest.RunNodeTelemetryDoesNotTouchHardwareConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_UnknownStaysNull(t *testing.T) {
	storagetest.RunNodeUnknownStaysNullConformance(t, newPostgresNodeHarness)
}

func TestConformance_Node_Views(t *testing.T) {
	storagetest.RunNodeViewsConformance(t, newPostgresNodeHarness)
}

func TestConformance_Worker_NodeIDRoundTrip(t *testing.T) {
	storagetest.RunWorkerNodeIDRoundTripConformance(t, newPostgresNodeHarness)
}

func TestConformance_Worker_UnknownNodeRejected(t *testing.T) {
	storagetest.RunWorkerUnknownNodeRejectedConformance(t, newPostgresNodeHarness)
}

// TestPostgresRepository_Node_TelemetryUnknownNodeNeverReturns409: a telemetry report for a node
// that does not exist returns ErrNodeNotFound and never ErrTelemetryOutOfOrder (the 409 path), for a
// first report, a high seq and a repeated report, and writes no node_telemetry row. The existence
// check (FOR KEY SHARE) must run before the upsert (mutant N31).
func TestPostgresRepository_Node_TelemetryUnknownNodeNeverReturns409(t *testing.T) {
	h, pool, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx := context.Background()
	unknown := storagetest.NodeTestID(77)
	session := storagetest.NodeTestID(100)
	for _, seq := range []int64{1, 1, 2, 1 << 50, domain.MaxTelemetrySeq} {
		err := repo.RecordNodeTelemetry(ctx, unknown, domain.NodeTelemetryReport{ContractVersion: 1, SessionID: session, Seq: seq})
		if errors.Is(err, domain.ErrTelemetryOutOfOrder) {
			t.Fatalf("seq %d for a missing node returned ErrTelemetryOutOfOrder (the 409 path)", seq)
		}
		if !errors.Is(err, domain.ErrNodeNotFound) {
			t.Fatalf("seq %d for a missing node: err = %v, want ErrNodeNotFound", seq, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM node_telemetry) + (SELECT count(*) FROM nodes)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a rejected report left rows: n=%d err=%v", n, err)
	}
}

// TestPostgresRepository_Node_TelemetrySeq_ConcurrentSameSession_OneWins: concurrent reports with the
// same session and the same seq: exactly one is accepted, the rest are ErrTelemetryOutOfOrder, and the
// stored row is the winner's. The accept test and the write are one statement (mutant N24). Run under
// -race -count=50.
func TestPostgresRepository_Node_TelemetrySeq_ConcurrentSameSession_OneWins(t *testing.T) {
	h, pool, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx := context.Background()
	id := storagetest.NodeTestID(1)
	if _, err := repo.RegisterNode(ctx, domain.NodeRegistration{NodeID: id, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	session := storagetest.NodeTestID(100)
	const workers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			load := int64(i)
			<-start
			results[i] = repo.RecordNodeTelemetry(ctx, id, domain.NodeTelemetryReport{
				ContractVersion: 1, SessionID: session, Seq: 1, NodeTelemetry: domain.NodeTelemetry{CPULoad1mMilli: &load}})
		}(i)
	}
	close(start)
	wg.Wait()
	accepted, rejected, winner := 0, 0, int64(-1)
	for i, err := range results {
		switch {
		case err == nil:
			accepted++
			winner = int64(i)
		case errors.Is(err, domain.ErrTelemetryOutOfOrder):
			rejected++
		default:
			t.Fatalf("unexpected error from reporter %d: %v", i, err)
		}
	}
	if accepted != 1 || rejected != workers-1 {
		t.Fatalf("accepted=%d rejected=%d, want exactly 1 accepted and %d rejected", accepted, rejected, workers-1)
	}
	d := dumpNode(t, pool, id)
	if d.Telemetry == nil || *d.Telemetry.Telemetry.CPULoad1mMilli != winner {
		t.Fatalf("stored sample is not the winner's: %+v (winner %d)", d.Telemetry, winner)
	}
}

// TestPostgresRepository_Node_ConcurrentRegister_GapFreeVersions: concurrent registrations of one node
// with different contents (the interval is zero so every change is accepted) serialise on the node
// row lock: the versions returned are unique and exactly 1..N, and after pruning the newest 10
// remain.
func TestPostgresRepository_Node_ConcurrentRegister_GapFreeVersions(t *testing.T) {
	h, pool, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx := context.Background()
	id := storagetest.NodeTestID(1)
	const n = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	versions := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mem := int64(1000 + i)
			reg := domain.NodeRegistration{NodeID: id, DisplayName: fmt.Sprintf("n%d", i), ContractVersion: 1,
				Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64", MemoryTotalBytes: &mem}}
			<-start
			res, err := repo.RegisterNode(ctx, reg, 0)
			errs[i] = err
			if err == nil {
				versions[i] = res.HardwareVersion
			}
		}(i)
	}
	close(start)
	wg.Wait()
	seen := map[int]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("registration %d: %v", i, errs[i])
		}
		if seen[versions[i]] || versions[i] < 1 || versions[i] > n {
			t.Fatalf("version %d returned twice or out of range: %v", versions[i], versions)
		}
		seen[versions[i]] = true
	}
	d := dumpNode(t, pool, id)
	if len(d.Hardware) != domain.MaxHardwareHistory {
		t.Fatalf("hardware rows = %v, want the newest %d", hwVersionList(d), domain.MaxHardwareHistory)
	}
	for i, r := range d.Hardware {
		if want := n - domain.MaxHardwareHistory + 1 + i; r.Version != want {
			t.Fatalf("surviving versions = %v, want %d..%d", hwVersionList(d), n-domain.MaxHardwareHistory+1, n)
		}
	}
	var nodes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&nodes); err != nil || nodes != 1 {
		t.Fatalf("nodes = %d (err=%v), want 1", nodes, err)
	}
}

func hwVersionList(d storagetest.NodeDump) []int {
	var out []int
	for _, r := range d.Hardware {
		out = append(out, r.Version)
	}
	return out
}

// TestPostgresRepository_Node_ConcurrentSameContentRegister_OneVersion: concurrent first
// registrations of one new node with identical content create one node row and one hardware version.
func TestPostgresRepository_Node_ConcurrentSameContentRegister_OneVersion(t *testing.T) {
	h, pool, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx := context.Background()
	id := storagetest.NodeTestID(1)
	const n = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mem := int64(5)
			reg := domain.NodeRegistration{NodeID: id, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64", MemoryTotalBytes: &mem}}
			<-start
			_, errs[i] = repo.RegisterNode(ctx, reg, time.Minute)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if d := dumpNode(t, pool, id); len(d.Hardware) != 1 || d.Hardware[0].Version != 1 {
		t.Fatalf("hardware rows = %v, want exactly [1]", hwVersionList(d))
	}
}

// TestPostgresRepository_Node_RegisterLocksNoKeyUpdate: the registration locks the node row FOR NO
// KEY UPDATE and never FOR UPDATE (ADR-0022 Section 7.4). A transaction holding FOR KEY SHARE on the
// node row (what a foreign key check takes) must not block a registration (mutant R6 for this slice).
func TestPostgresRepository_Node_RegisterLocksNoKeyUpdate(t *testing.T) {
	h, pool, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx := context.Background()
	id := storagetest.NodeTestID(1)
	reg := domain.NodeRegistration{NodeID: id, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}
	if _, err := repo.RegisterNode(ctx, reg, time.Minute); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM nodes WHERE id = $1 FOR KEY SHARE`, id).Scan(&one); err != nil {
		t.Fatal(err)
	}
	reg.DisplayName = "renamed"
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := repo.RegisterNode(callCtx, reg, time.Minute); err != nil {
		t.Fatalf("RegisterNode blocked or failed while another transaction held FOR KEY SHARE on the node row: %v", err)
	}
}

// TestPostgresRepository_Node_MixedConcurrencyNoDeadlock: registrations, telemetry and worker
// registrations that name the node run concurrently on one node and all finish (no deadlock, no
// unexpected error). The foreign keys take FOR KEY SHARE, which does not conflict with the
// registration's FOR NO KEY UPDATE. Run under -race -count=50.
func TestPostgresRepository_Node_MixedConcurrencyNoDeadlock(t *testing.T) {
	h, _, repo := newPostgresNodeHarnessWithPool(t)
	_ = h
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := storagetest.NodeTestID(1)
	if _, err := repo.RegisterNode(ctx, domain.NodeRegistration{NodeID: id, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			mem := int64(i)
			_, err := repo.RegisterNode(ctx, domain.NodeRegistration{NodeID: id, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64", MemoryTotalBytes: &mem}}, 0)
			if err != nil {
				errc <- fmt.Errorf("register %d: %w", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			err := repo.RecordNodeTelemetry(ctx, id, domain.NodeTelemetryReport{ContractVersion: 1, SessionID: storagetest.NodeTestID(200 + i), Seq: 1})
			if err != nil && !errors.Is(err, domain.ErrTelemetryOutOfOrder) {
				errc <- fmt.Errorf("telemetry %d: %w", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			node := id
			err := repo.UpsertWorker(ctx, &domain.Worker{ID: fmt.Sprintf("w%d", i), Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &node})
			if err != nil {
				errc <- fmt.Errorf("worker %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
}

// TestPostgresRepository_Node_ForeignKeyMapping_ExactConstraintOnly (clarification K2): a foreign key
// violation of exactly fk_workers_node maps to domain.ErrNodeNotFound and every other foreign key
// violation keeps its mapping. The errors are real PostgreSQL errors from real violations.
func TestPostgresRepository_Node_ForeignKeyMapping_ExactConstraintOnly(t *testing.T) {
	_, pool, repo := newPostgresNodeHarnessWithPool(t)
	ctx := context.Background()

	// 1. workers.node_id -> fk_workers_node, raised by raw SQL.
	_, err := pool.Exec(ctx, `INSERT INTO workers (id, version, total_slots, status, supported_tasks, node_id)
		VALUES ('w_fk', 'v', 1, 'HEALTHY', ARRAY['echo'], $1)`, storagetest.NodeTestID(88))
	if got := postgres.MapPgError(err); !errors.Is(got, domain.ErrNodeNotFound) {
		t.Fatalf("a violation of fk_workers_node mapped to %v (raw: %v), want ErrNodeNotFound", got, err)
	}

	// 2. attempts.worker_id -> attempts_worker_id_fkey keeps its mapping (ErrWorkerNotFound).
	if _, err := pool.Exec(ctx, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j_fk', 'echo', '\x00', 'QUEUED')`); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO attempts (id, job_id, worker_id, attempt_no, state, lease_token_hash, lease_expires_at)
		VALUES ('a_fk', 'j_fk', 'no_such_worker', 1, 'PENDING', repeat('a', 64), now())`)
	if err == nil {
		t.Fatal("expected a foreign key violation on attempts.worker_id")
	}
	got := postgres.MapPgError(err)
	if errors.Is(got, domain.ErrNodeNotFound) || !errors.Is(got, domain.ErrWorkerNotFound) {
		t.Fatalf("a violation of attempts_worker_id_fkey mapped to %v (raw: %v), want ErrWorkerNotFound and not ErrNodeNotFound", got, err)
	}

	// 3. node_hardware.node_id -> node_hardware_node_id_fkey keeps the generic mapping.
	_, err = pool.Exec(ctx, `INSERT INTO node_hardware (node_id, version, contract_version, os, arch, content_hash)
		VALUES ($1, 1, 1, 'linux', 'amd64', repeat('a', 64))`, storagetest.NodeTestID(89))
	got = postgres.MapPgError(err)
	if errors.Is(got, domain.ErrNodeNotFound) || !errors.Is(got, domain.ErrInvalidRequest) {
		t.Fatalf("a violation of node_hardware_node_id_fkey mapped to %v (raw: %v), want ErrInvalidRequest and not ErrNodeNotFound", got, err)
	}

	// 4. Through the repository: a worker naming an unknown node.
	node := storagetest.NodeTestID(90)
	if err := repo.UpsertWorker(ctx, &domain.Worker{ID: "w_repo", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &node}); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("UpsertWorker with an unknown node: %v, want ErrNodeNotFound", err)
	}
}
