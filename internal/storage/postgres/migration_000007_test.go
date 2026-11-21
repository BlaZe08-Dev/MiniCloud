package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// All tests in this file build their schema with setupTestSchema, which refuses any database whose
// name does not end in "_test" (SEC-023).

const migration000007Name = "000007_node_capability_model.sql"

const testNodeID = "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23"

func scanStrings(t *testing.T, conn *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, sql)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrator_Migration000007_FreshSchema applies every embedded migration to an empty schema and
// checks ADR-0022 Sections 3 and 12: the three tables, workers.node_id, the named CHECKs, every
// foreign key ON DELETE RESTRICT, the validated fk_workers_node, the partial index, and that no
// node_tools table and no reservation column exists. It pins 000007 as the seventh and last
// embedded migration (000006 stays pinned as the sixth by TestMigrator_Migration000006_FreshSchema).
func TestMigrator_Migration000007_FreshSchema(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m := postgres.NewMigrator()
	all, err := m.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations failed: %v", err)
	}
	if len(all) != 7 || all[6].Name != migration000007Name {
		t.Fatalf("expected the seventh and last embedded migration to be %s, got %d migrations (last %q)", migration000007Name, len(all), all[len(all)-1].Name)
	}
	if n, err := m.Migrate(ctx, conn); err != nil || n != 7 {
		t.Fatalf("Migrate: applied=%d err=%v", n, err)
	}

	wantChecks := map[string][]string{
		"nodes": {"chk_nodes_display_name", "chk_nodes_id_uuid"},
		"node_hardware": {"chk_node_hardware_agent_version", "chk_node_hardware_bytes", "chk_node_hardware_counts",
			"chk_node_hardware_models", "chk_node_hardware_os_arch", "chk_node_hardware_version"},
		"node_telemetry": {"chk_node_telemetry_bytes", "chk_node_telemetry_load", "chk_node_telemetry_sample_order",
			"chk_node_telemetry_seq", "chk_node_telemetry_session"},
	}
	for table, want := range wantChecks {
		got := scanStrings(t, conn, `SELECT conname FROM pg_constraint WHERE conrelid = ($1 || '.' || $2)::regclass AND contype = 'c' ORDER BY conname`, schema, table)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s CHECKs = %v, want %v", table, got, want)
		}
	}

	// Every foreign key to nodes is ON DELETE RESTRICT (confdeltype 'r'); fk_workers_node is validated.
	fks := scanStrings(t, conn, `
		SELECT conrelid::regclass::text || ':' || conname || ':' || confdeltype::text || ':' || convalidated::text
		FROM pg_constraint WHERE contype = 'f' AND confrelid = ($1 || '.nodes')::regclass ORDER BY 1`, schema)
	wantFKs := []string{
		"node_hardware:node_hardware_node_id_fkey:r:true",
		"node_telemetry:node_telemetry_node_id_fkey:r:true",
		"workers:fk_workers_node:r:true",
	}
	if strings.Join(fks, "|") != strings.Join(wantFKs, "|") {
		t.Errorf("foreign keys to nodes = %v, want %v", fks, wantFKs)
	}

	var typ, nullable, def string
	if err := conn.QueryRow(ctx, `SELECT data_type, is_nullable, COALESCE(column_default, '') FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'workers' AND column_name = 'node_id'`, schema).Scan(&typ, &nullable, &def); err != nil {
		t.Fatalf("column workers.node_id: %v", err)
	}
	if typ != "text" || nullable != "YES" || def != "" {
		t.Errorf("workers.node_id: type=%q nullable=%q default=%q, want text YES (no default)", typ, nullable, def)
	}
	if err := conn.QueryRow(ctx, `SELECT COALESCE(column_default, '') FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'node_telemetry' AND column_name = 'received_at'`, schema).Scan(&def); err != nil || !strings.Contains(def, "clock_timestamp()") {
		t.Errorf("node_telemetry.received_at default = %q (err=%v), want clock_timestamp()", def, err)
	}

	var idxDef string
	if err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = 'idx_workers_node'`, schema).Scan(&idxDef); err != nil {
		t.Fatalf("index idx_workers_node: %v", err)
	}
	if !strings.Contains(idxDef, "(node_id)") || !strings.Contains(idxDef, "node_id IS NOT NULL") {
		t.Errorf("idx_workers_node = %q, want a partial index on node_id", idxDef)
	}

	// Not in this migration: node_tools (V4-3) and reservation columns (V2-3).
	var toolsTable *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1 || '.node_tools')::text`, schema).Scan(&toolsTable); err != nil || toolsTable != nil {
		t.Errorf("node_tools exists after 000007 (err=%v, regclass=%v)", err, toolsTable)
	}
	var reservation int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = $1
		AND ((table_name = 'attempts' AND column_name = 'node_id') OR (table_name = 'jobs' AND column_name = 'assigned_node_id'))`, schema).Scan(&reservation); err != nil || reservation != 0 {
		t.Errorf("reservation columns exist after 000007 (err=%v, n=%d)", err, reservation)
	}
}

// TestMigrator_Migration000007_UpgradeFrom000006_WithExistingRows applies 000007 to a database at
// 000006 that already holds workers, a job and an attempt. It proves that VALIDATE CONSTRAINT
// fk_workers_node succeeds on those rows, that existing workers read node_id NULL and keep every
// other value, and (clarification K6) that the stored checksums of 000001 to 000006 are unchanged by
// applying 000007 and that no ErrChecksumMismatch occurs on the upgrade or on a re-run.
func TestMigrator_Migration000007_UpgradeFrom000006_WithExistingRows(t *testing.T) {
	conn, schema := setupTestSchema(t)
	ctx := context.Background()

	m6, all := migrationsUpTo(t, 6)
	if n, err := m6.Migrate(ctx, conn); err != nil || n != 6 {
		t.Fatalf("migrating to 000006: applied=%d err=%v", n, err)
	}
	mustExec(t, conn, `INSERT INTO workers (id, version, total_slots, status, supported_tasks, executes_concurrently) VALUES
		('w_a', 'v0.1.0', 4, 'HEALTHY', ARRAY['echo'], TRUE), ('w_b', 'v0.1.0', 1, 'UNHEALTHY', ARRAY['sleep'], FALSE)`)
	mustExec(t, conn, `INSERT INTO jobs (id, task_type, payload, state) VALUES ('j_q', 'echo', '\x00', 'QUEUED')`)

	const snapshotSQL = `SELECT id || '|' || version || '|' || total_slots || '|' || status || '|' || supported_tasks::text || '|' || executes_concurrently::text
		FROM workers ORDER BY id`
	before := scanStrings(t, conn, snapshotSQL)
	checksumsSQL := `SELECT version::text || ':' || sha256_checksum FROM schema_migrations ORDER BY version`
	checksumsBefore := scanStrings(t, conn, checksumsSQL)
	if len(checksumsBefore) != 6 {
		t.Fatalf("schema_migrations holds %d rows at 000006, want 6", len(checksumsBefore))
	}

	if len(all) != 7 || all[6].Name != migration000007Name {
		t.Fatalf("expected %s as the seventh migration, got %d migrations", migration000007Name, len(all))
	}
	m7 := postgres.NewMigrator(postgres.WithLockID(0x12345678))
	n, err := m7.Migrate(ctx, conn)
	if errors.Is(err, postgres.ErrChecksumMismatch) {
		t.Fatalf("upgrade 000006 -> 000007 reported a checksum mismatch: %v", err)
	}
	if err != nil || n != 1 {
		t.Fatalf("upgrading 000006 -> 000007: applied=%d err=%v", n, err)
	}

	checksumsAfter := scanStrings(t, conn, checksumsSQL)
	if len(checksumsAfter) != 7 {
		t.Fatalf("schema_migrations holds %d rows after 000007, want 7", len(checksumsAfter))
	}
	for i := 0; i < 6; i++ {
		if checksumsAfter[i] != checksumsBefore[i] {
			t.Errorf("stored checksum of migration %d changed: %s -> %s", i+1, checksumsBefore[i], checksumsAfter[i])
		}
		if want := fmt.Sprintf("%d:%s", i+1, all[i].Checksum); checksumsAfter[i] != want {
			t.Errorf("stored checksum of migration %d = %s, embedded file says %s", i+1, checksumsAfter[i], want)
		}
	}
	if want := fmt.Sprintf("7:%s", all[6].Checksum); checksumsAfter[6] != want {
		t.Errorf("stored checksum of migration 7 = %s, want %s", checksumsAfter[6], want)
	}

	if after := scanStrings(t, conn, snapshotSQL); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("worker rows changed by the upgrade:\nbefore %v\nafter  %v", before, after)
	}
	var nulls, total, jobs int
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE node_id IS NULL), count(*), (SELECT count(*) FROM jobs) FROM workers`).Scan(&nulls, &total, &jobs); err != nil {
		t.Fatal(err)
	}
	if total != 2 || nulls != 2 || jobs != 1 {
		t.Fatalf("after upgrade: workers=%d with NULL node_id=%d jobs=%d, want 2, 2, 1", total, nulls, jobs)
	}
	var validated bool
	if err := conn.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'fk_workers_node' AND conrelid = ($1 || '.workers')::regclass`, schema).Scan(&validated); err != nil || !validated {
		t.Fatalf("fk_workers_node validated=%v err=%v, want validated", validated, err)
	}

	// A re-run applies nothing and still finds no checksum mismatch.
	if n, err := m7.Migrate(ctx, conn); err != nil || n != 0 {
		t.Fatalf("re-run after 000007: applied=%d err=%v, want 0 and nil", n, err)
	}
}

// insertCase is one row of TestSchemaInvariants_NodeChecks: sql is run against a schema that already
// holds the base node, and wantConstraint is the CHECK that must reject it ("" means it must be
// accepted).
type insertCase struct {
	name           string
	sql            string
	wantConstraint string
}

const hwInsert = `INSERT INTO node_hardware (node_id, version, contract_version, agent_version, os, arch, cpu_model,
	cpu_logical_cores, cpu_physical_cores, memory_total_bytes, disk_total_bytes, gpu_count, gpu_model, gpu_vram_total_bytes, content_hash)
	VALUES ('` + testNodeID + `', %d, 1, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, repeat('a', 64))`

// hw builds a node_hardware insert; each argument is a SQL literal.
func hw(version int, agent, os, arch, cpuModel, logical, physical, mem, disk, gpuCount, gpuModel, vram string) string {
	return fmt.Sprintf(hwInsert, version, agent, os, arch, cpuModel, logical, physical, mem, disk, gpuCount, gpuModel, vram)
}

const telInsert = `INSERT INTO node_telemetry (node_id, contract_version, session_id, seq, received_at, sampled_at,
	cpu_load_1m_milli, memory_available_bytes, disk_free_bytes)
	VALUES ('` + testNodeID + `', 1, %s, %s, now(), %s, %s, %s, %s)`

func tel(session, seq, sampled, load, mem, disk string) string {
	return fmt.Sprintf(telInsert, session, seq, sampled, load, mem, disk)
}

// TestSchemaInvariants_NodeChecks proves by SQL that each named CHECK of ADR-0022 Section 9 rejects
// an out-of-range row (SQLSTATE 23514 naming that constraint), and that the edge values are accepted.
func TestSchemaInvariants_NodeChecks(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()
	if _, err := postgres.NewMigrator().Migrate(ctx, conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mustExec(t, conn, `INSERT INTO nodes (id) VALUES ('`+testNodeID+`')`)

	const (
		okSession = `'b6d1e0c4-2a57-4f0e-8d3b-6c9a41f2e7a0'`
		maxSafe   = "9007199254740991"
		overSafe  = "9007199254740992"
	)
	two33 := "'" + strings.Repeat("é", 33) + "'" // 33 characters, 66 bytes: passes VARCHAR(64), fails the byte CHECK
	cases := []insertCase{
		// nodes
		{"nodes id uppercase", `INSERT INTO nodes (id) VALUES ('3F1C9A2E-6B7D-4E21-9C4A-0D5E8F7A1B23')`, "chk_nodes_id_uuid"},
		{"nodes id version nibble 3", `INSERT INTO nodes (id) VALUES ('3f1c9a2e-6b7d-3e21-9c4a-0d5e8f7a1b23')`, "chk_nodes_id_uuid"},
		{"nodes id variant nibble c", `INSERT INTO nodes (id) VALUES ('3f1c9a2e-6b7d-4e21-cc4a-0d5e8f7a1b23')`, "chk_nodes_id_uuid"},
		{"nodes id 35 bytes", `INSERT INTO nodes (id) VALUES ('3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b2')`, "chk_nodes_id_uuid"},
		{"nodes id empty", `INSERT INTO nodes (id) VALUES ('')`, "chk_nodes_id_uuid"},
		{"nodes display_name tab", `INSERT INTO nodes (id, display_name) VALUES ('4f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23', E'a\tb')`, "chk_nodes_display_name"},
		{"nodes display_name 66 bytes", `INSERT INTO nodes (id, display_name) VALUES ('4f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23', ` + two33 + `)`, "chk_nodes_display_name"},
		{"nodes display_name 64 ascii accepted", `INSERT INTO nodes (id, display_name) VALUES ('5f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23', repeat('x', 64))`, ""},
		// node_hardware
		{"hardware version 0", hw(0, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_version"},
		{"hardware agent_version 66 bytes", hw(1, two33, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_agent_version"},
		{"hardware os uppercase", hw(1, `''`, `'Linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_os_arch"},
		{"hardware arch empty", hw(1, `''`, `'linux'`, `''`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_os_arch"},
		{"hardware arch hyphen", hw(1, `''`, `'linux'`, `'x-86'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_os_arch"},
		{"hardware cpu_model empty", hw(1, `''`, `'linux'`, `'amd64'`, `''`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_models"},
		{"hardware cpu_model newline", hw(1, `''`, `'linux'`, `'amd64'`, `E'a\nb'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_models"},
		{"hardware gpu_model non-ASCII", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `'é'`, `NULL`), "chk_node_hardware_models"},
		{"hardware logical cores 0", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `0`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_counts"},
		{"hardware physical cores 0", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `0`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_counts"},
		{"hardware gpu_count -1", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `-1`, `NULL`, `NULL`), "chk_node_hardware_counts"},
		{"hardware logical < physical", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `2`, `4`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_counts"},
		{"hardware memory -1", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `-1`, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_bytes"},
		{"hardware memory 2^53", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, overSafe, `NULL`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_bytes"},
		{"hardware disk -1", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `-1`, `NULL`, `NULL`, `NULL`), "chk_node_hardware_bytes"},
		{"hardware vram 2^53", hw(1, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, overSafe), "chk_node_hardware_bytes"},
		{"hardware edges accepted", hw(2, `''`, `'linux'`, `'amd64'`, `repeat('c', 128)`, `4`, `4`, maxSafe, `0`, `0`, `repeat('g', 128)`, maxSafe), ""},
		{"hardware all unknown accepted", hw(3, `''`, `'linux'`, `'amd64'`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`, `NULL`), ""},
		// node_telemetry
		{"telemetry sampled after received", tel(okSession, `1`, `now() + interval '1 hour'`, `NULL`, `NULL`, `NULL`), "chk_node_telemetry_sample_order"},
		{"telemetry session uppercase", tel(`'B6D1E0C4-2A57-4F0E-8D3B-6C9A41F2E7A0'`, `1`, `now() - interval '1 second'`, `NULL`, `NULL`, `NULL`), "chk_node_telemetry_session"},
		{"telemetry session not v4", tel(`'b6d1e0c4-2a57-3f0e-8d3b-6c9a41f2e7a0'`, `1`, `now() - interval '1 second'`, `NULL`, `NULL`, `NULL`), "chk_node_telemetry_session"},
		{"telemetry seq 0", tel(okSession, `0`, `now() - interval '1 second'`, `NULL`, `NULL`, `NULL`), "chk_node_telemetry_seq"},
		{"telemetry seq 2^53", tel(okSession, overSafe, `now() - interval '1 second'`, `NULL`, `NULL`, `NULL`), "chk_node_telemetry_seq"},
		{"telemetry load -1", tel(okSession, `1`, `now() - interval '1 second'`, `-1`, `NULL`, `NULL`), "chk_node_telemetry_load"},
		{"telemetry memory -1", tel(okSession, `1`, `now() - interval '1 second'`, `NULL`, `-1`, `NULL`), "chk_node_telemetry_bytes"},
		{"telemetry disk 2^53", tel(okSession, `1`, `now() - interval '1 second'`, `NULL`, `NULL`, overSafe), "chk_node_telemetry_bytes"},
		{"telemetry edges accepted", tel(okSession, maxSafe, `now() - interval '1 second'`, `2147483647`, maxSafe, `0`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A failed statement outside a transaction leaves no state behind. The telemetry table has
			// one row per node, so an accepted telemetry row is removed again.
			_, err := conn.Exec(ctx, c.sql)
			if c.wantConstraint == "" {
				if err != nil {
					t.Fatalf("expected the row to be accepted, got %v", err)
				}
				mustExec(t, conn, `DELETE FROM node_telemetry WHERE node_id = '`+testNodeID+`'`)
				return
			}
			var pgErr interface{ SQLState() string }
			if !errors.As(err, &pgErr) || pgErr.SQLState() != "23514" || !strings.Contains(err.Error(), c.wantConstraint) {
				t.Fatalf("expected a 23514 violation of %s, got %v", c.wantConstraint, err)
			}
		})
	}
}

// TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced proves that every foreign key to nodes is
// ON DELETE RESTRICT: DELETE FROM nodes fails with SQLSTATE 23503 while a workers, node_hardware or
// node_telemetry row references the node, and succeeds once none does (mutant N12).
func TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced(t *testing.T) {
	conn, _ := setupTestSchema(t)
	ctx := context.Background()
	if _, err := postgres.NewMigrator().Migrate(ctx, conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	cases := []struct {
		name    string
		node    string
		insert  string
		cleanup string
	}{
		{"workers", "6f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23",
			`INSERT INTO workers (id, version, total_slots, status, supported_tasks, node_id) VALUES ('w_ref', 'v0', 1, 'HEALTHY', ARRAY['echo'], '6f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23')`,
			`DELETE FROM workers WHERE id = 'w_ref'`},
		{"node_hardware", "7f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23",
			`INSERT INTO node_hardware (node_id, version, contract_version, os, arch, content_hash) VALUES ('7f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23', 1, 1, 'linux', 'amd64', repeat('a', 64))`,
			`DELETE FROM node_hardware WHERE node_id = '7f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23'`},
		{"node_telemetry", "8f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23",
			`INSERT INTO node_telemetry (node_id, contract_version, session_id, seq, sampled_at) VALUES ('8f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23', 1, 'b6d1e0c4-2a57-4f0e-8d3b-6c9a41f2e7a0', 1, now() - interval '1 second')`,
			`DELETE FROM node_telemetry WHERE node_id = '8f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustExec(t, conn, `INSERT INTO nodes (id) VALUES ('`+c.node+`')`)
			mustExec(t, conn, c.insert)
			_, err := conn.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, c.node)
			if got := pgCode(err); got != "23503" {
				t.Fatalf("DELETE FROM nodes while referenced by %s: SQLSTATE %q (err=%v), want 23503", c.name, got, err)
			}
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id = $1`, c.node).Scan(&n); err != nil || n != 1 {
				t.Fatalf("node row survived? count=%d err=%v, want 1", n, err)
			}
			mustExec(t, conn, c.cleanup)
			mustExec(t, conn, `DELETE FROM nodes WHERE id = $1`, c.node)
		})
	}
}
