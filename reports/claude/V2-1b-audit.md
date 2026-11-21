Task: V2-1b-AUDIT, independent read-only audit of slice V2-1b (ADR-0022, migration 000007 and node storage)
Branch: agent/claude/V2-1b-audit (from origin/agent/claude/V2-1b)
Work tip (last commit before this report): d0893db4fbe80b609e04313ecf342ca455de2cba
Date: 2026-11-21

# V2-1b audit

Auditor: Claude Code, Opus 5.5, separate session from the builder. Commit audited: d0893db (builder tip). No production code, test, ADR or builder report was changed. This file is the only commit.

Evidence labels (AGENTS.md section 2): **executed** = I ran it and read the output; **confirmed by code reading** = read, not run; **inferred** = reasoning only. Line numbers refer to d0893db.

Throwaway work, all deleted: probe tests (`zz_audit_*_test.go` in `internal/domain`, `internal/storage`, `internal/storage/postgres`, `internal/api`) existed only in the throwaway worktree `~/mc-mut-claude-v21b-audit`. That worktree was created new (the path did not exist), used for probes and mutants, and removed with `git worktree remove` (afterwards `ls` reports no such directory). Probe SQL ran in a scratch schema `zz_audit_v21b` (dropped) and in a scratch database `minicloud_claudeaudit_c_test` (C locale, created by me, dropped; a later `pg_database` count returned 0). Every other database action used `minicloud_claude_test` only.

## 1. Verdict

**Merge as is.** No Critical or Major finding. Every gate reproduces the builder's counts exactly. The ten mutants the task named all fail a test and pass again after restore. The builder's equivalence claim for the one survivor is wrong (A-06): a lock-observation test kills it. The code matches ADR 3, 8.1, 8.3, 9 and 12. I tried lost updates, double version inserts, writes on the limited path, lock waits on existing paths and deadlocks, and none appeared (section 4).

Conditions of the merge (not fixes to this branch):
1. Cut no release or tag from `main` between V2-1b and V2-1c. On this branch `POST /v1/workers/register` with any non-null `node_id` gives 500, where `main` gives 200 (A-01).
2. V2-1c must not expose `ListNodes` or `GetNode` until A-03 (in-memory cost) and A-04 (no cap) are fixed.
3. The ADR-0022 amendment must take A-02, A-08, A-09 and the K4 weakness.

Recommended but not blocking: add the A-06 lock-observation test and the A-11 test assertions, and correct the builder's report gap 4 (A-07).

## 2. Findings

| ID | Sev | file:line | What | Why it matters | How verified | Suggested fix wording |
|---|---|---|---|---|---|---|
| A-01 | Minor | internal/api/handlers.go:289-300, :276-286; internal/domain/worker.go:27 | Interim `/v1` behaviour change. On `main`, `node_id` is an unknown field and is ignored (200). On this branch: an unknown or malformed `node_id` gives **500 INTERNAL_ERROR**; a non-string `node_id` (e.g. `5`) gives **400 MALFORMED_JSON**; a NUL byte gives PostgreSQL 22021, and a 300 000-byte value gives 54000 (index row too large), both 500. If a node row exists by any route (a rollback from V2-1c, or a manual insert), `node_id` is stored and returned by `GET /v1/workers` on **any** bind address, `0.0.0.0` included, which ADR 4.4 forbids. | It breaks the "unknown fields ignored" rule (C8) for one field, and the ADR 4.3 promise that a new worker gets legacy behaviour from a coordinator that does not serve nodes. No shipped client sends `node_id`, so nothing breaks today. | **executed**: throwaway API test, in-memory coordinator. Branch: `unknown node_id -> 500`, `malformed -> 500`, `node_id:5 -> 400 MALFORMED_JSON`, `null -> 200`, absent `-> 200`. origin/main: all five `-> 200`. With a node created through the repository and bind `0.0.0.0:0`, `GET /v1/workers` returned `..."node_id":"3f1c9a2e-..."`. PostgreSQL errors from a direct repository probe: `SQLSTATE 22021`, `SQLSTATE 54000`. | V2-1c: validate `node_id` in the handler (`domain.ValidateNodeID`), map `ErrNodeNotFound` to 400 `INVALID_REQUEST`, and store NULL and omit the key when not local-only (ADR 4.4). Until then: no release from `main`. |
| A-02 | Minor | internal/storage/postgres/nodes.go:113-121; internal/storage/inmemory_nodes.go:117-125; internal/domain/node.go:350-388 | Because `agent_version` and `contract_version` are not hashed, an agent upgrade on unchanged hardware writes nothing. The stored `agent_version` and `contract_version` of the current row stay those of the first registration of that content, forever, and the full view returns them (ADR 4.4 lists `agent_version` as a full-view field). | The view reports a stale agent version, so operators read wrong data. The builder's "Beyond the ADR" item 1 lists the decision but not this consequence. No test asserts what the view shows after an agent-only change: `RunNodeHardwareVersionOnChangeOnlyConformance` (storagetest/nodes.go:221) checks only the version number. | **confirmed by code reading**: the same-hash branch writes only `nodes` (nodes.go:117-120). | ADR amendment: either "agent_version shows the agent of the registration that created the current hardware version" (document it), or keep `agent_version` and `contract_version` on `nodes`, or update the current row's `agent_version` in the same-hash branch. Add a conformance assertion either way. |
| A-03 | Minor (latent; Major once V2-1c serves the routes) | internal/storage/inmemory_nodes.go:194-226 (loop at 220-224), :178-191 | In-memory `nodeViewLocked` scans **all workers** for every node, so `ListNodes` is O(nodes × workers) and `GetNode` is O(workers). Both run under the repository read lock, which blocks every writer (`UpsertWorker`, heartbeats, claims). | One unauthenticated `GET /v1/nodes` can stall the whole in-memory coordinator for seconds (T2). PostgreSQL does one worker query (nodes.go:305-324). | **executed** (throwaway test; 4-core i3-7020U, 3 GiB RAM, Go 1.22.2; the gate suite was running in parallel, so the numbers are inflated by contention): nodes=10000 workers=0: 11 ms; 10000×10000: **3.156 s**; 100000×0: 170 ms; 20000×20000: **22.753 s**. | Build one map from node ID to worker IDs per call (one pass over workers), or keep a per-node index under `mu`. Add a cost test before V2-1c. |
| A-04 | Minor | internal/storage/postgres/nodes.go:215-326; internal/storage/repository.go:84-85 | `ListNodes` has no limit and no pagination. It returns every node with full hardware, telemetry and worker IDs. Node creation is unauthenticated and unbounded (T2). | Memory and time grow linearly with nodes, and one call materialises everything. | **executed** (PostgreSQL 16.15, rows seeded by SQL, 10 hardware rows, 1 telemetry row and 1 worker per node): 10 000 nodes: ListNodes **304 ms**, 12.9 MiB allocated, JSON 7.1 MiB, tables and indexes 35.8 MiB on disk. 100 000 nodes: **4.712 s**, 128.7 MiB allocated (78.0 MiB live after the call), JSON 1.114 s and 71.5 MiB, **356.5 MiB** on disk. **inferred**: 1 000 000 nodes would be about 1.3 GiB of allocation per call. Hardware rows are bounded at 10 per node (mutants and A-10 below) and telemetry at 1 per node (primary key), so storage is linear in node count: about 3.6 KiB per node measured. | V2-1c: the summary route returns only the summary fields (ADR 4.4), with a hard page size (for example 500) and a cursor by node ID. Count nodes and refuse registration of new IDs above a configured ceiling, or rate-limit new IDs globally, until V3-2. Record the measured cost in SEC-070/T2. |
| A-05 | Minor | internal/storage/postgres/nodes.go:235, :39-49; internal/storage/inmemory_nodes.go:186, :101-108 | Observable differences between the PostgreSQL and in-memory repositories (full list in section 5). Two matter: (a) `ListNodes` with no nodes returns **nil** in PostgreSQL and `[]` in memory, so a handler that marshals the slice prints `null` with one backend and `[]` with the other; (b) a node ID or `node_id` containing NUL returns a raw 22021 error in PostgreSQL but `ErrNodeNotFound` in memory. | The conformance suite claims parity (K5). (a) changes the API output once V2-1c marshals the slice; (b) gives 500 versus 400 or 404. | **executed**: probes printed `PARITY pg ListNodes(empty) nil=true` and `PARITY mem ListNodes(empty) nil=false`; `pg UpsertWorker(node_id with NUL) err=... (SQLSTATE 22021) isNodeNotFound=false` and `mem ... err=node not found isNodeNotFound=true`; the same 22021 for `RecordNodeTelemetry` and `GetNode`. | Initialise `views := []*domain.NodeView{}` in `loadNodeViews` and add a conformance case for an empty list. V2-1c validates every node ID in the handler before the repository is called. |
| A-06 | Minor (test gap) | internal/storage/postgres/nodes.go:161-167 | The survivor "existence check removed" is **not equivalent**. On the update path (a telemetry row already exists) the FK is not re-checked, so without the `FOR KEY SHARE` check telemetry takes no lock on the node row at all. | The builder's report section 6 calls it equivalent and unkillable; it is killable. The lock is what ADR 3.3 relies on to hold the node against deletion. | **executed**: another transaction held the node row `FOR UPDATE`, then a second report (seq 2) was sent. Original code: `blocked=true err=timeout`. Mutant (`SELECT 1 WHERE $1::text IS NOT NULL`): `blocked=false err=<nil>`. My mutant run of the full node suite: survived, `pg exit 0`, 80 PASS 0 FAIL. | Add `TestPostgresRepository_Node_TelemetryTakesKeyShareOnNode` (planned test): hold the node row `FOR UPDATE`, send a second report with a 2 s context, and assert it blocks. |
| A-07 | Info (report accuracy) | reports/claude/V2-1b.md gap 4 | The builder's gap 4 says `[[:cntrl:]]` depends on the database locale and that a C-locale database "may not match C1 characters". On this PostgreSQL 16.15 it is locale-independent: it matches exactly U+0001–U+001F and U+007F–U+009F (64 code points) under both `en_US.utf8` and `C`. That is exactly Go's `unicode.IsControl` (64 code points, same ranges), so the database can neither reject a name the domain accepts nor accept one it rejects. U+2028, U+2029, U+00A0, U+FEFF and U+FFF9 are accepted by both. Invalid UTF-8 and NUL cannot reach the column (the UTF8 database encoding rejects them). **Not a defect.** | The report states an unverified risk as a gap. | **executed**: every Unicode scalar value 1..0x10FFFF (no surrogates) tested with `chr(cp) ~ '[[:cntrl:]]'`, plus INSERTs through the real CHECK into a temp table, in `minicloud_claude_test` (`en_US.utf8`, musl build) and in the scratch C-locale database: both printed ranges `1-1f (31)` and `7f-9f (33)`. A Go program enumerating `unicode.IsControl` printed `1-1f`, `7f-9f`, `count 64`. | Correct gap 4 in the builder's next report. Optionally pin the CHECK with a C1 row (U+0085) in `TestSchemaInvariants_NodeChecks` (A-11). |
| A-08 | Info (ADR conflict) | docs/adr/0022-node-capability-model.md:220 vs :496 | ADR 4.1 says registration is `INSERT ... ON CONFLICT (id) DO UPDATE`; ADR 8.1 step 1 says `ON CONFLICT (id) DO NOTHING`. The code follows 8.1 and the task (nodes.go:46-49), which is the correct choice: an update before the lock would write on the 429 path. | The ADR contradicts itself, and the builder did not report it. | **confirmed by code reading** | Amend 4.1 to "DO NOTHING, then lock and decide (Section 8.1)". |
| A-09 | Info (ADR text) | internal/storage/postgres/migrations/000007_node_capability_model.sql:16, :39; ADR line 63 | The migration CHECKs differ from the ADR's literal SQL (`octet_length(display_name) <= 64 AND ...`, plus `chk_node_hardware_agent_version`). The difference is correct (VARCHAR counts characters, the domain counts bytes; tested with 33 × "é"). | The ADR shows CHECK text the schema does not have. | **confirmed by code reading**; `TestSchemaInvariants_NodeChecks` passes (gate run). | Copy the built CHECK text into ADR 3.1, 3.2 and 9 in the amendment. |
| A-10 | Info | internal/domain/node.go:397-420 | `ceilSeconds` overflows when `minInterval - elapsed` > MaxInt64 − 1 s + 1 ns. The result goes negative and is clamped to **1 s**, while `limited` stays true. Huge intervals below that bound round correctly. | It only returns a wrong (too short) Retry-After for intervals near 292 years; it never wrongly accepts. The ADR's positive-only rule has no upper bound yet. | **executed** (throwaway test): interval MaxInt64, elapsed 0: `limited=true retryAfter=1s`. Interval MaxInt64, elapsed 1 s: `2562047h47m16s`. 100 years: `876000h0m0s`. Exact boundary: `limited=false`. Clock back 1 ns: `limited=true 1m0s`. | V2-1c: set an upper bound on `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` (for example 24 h), or saturate in `ceilSeconds`. |
| A-11 | Minor (test gaps) | internal/storage/postgres/migration_000007_test.go:227-301, :124-128, :48-126; internal/domain/node_storage_test.go:178-185 | (a) `TestSchemaInvariants_NodeChecks` tests only a tab for `chk_nodes_display_name`, not DEL or C1 (U+0085), and has no CHECK-level DEL (0x7f) case for the models. (b) No test asserts that `worker_capacity` is unchanged or that `workers` is not rewritten (ADR 3.4 and 12). (c) `TestDomain_NodeHardwareContentHash_VersionsNotHashed` is tautological: the versions are not fields of `NodeHardware`. (d) The docstring of `..._UpgradeFrom000006_WithExistingRows` says "workers, a job and an attempt", but no attempt is inserted. (e) No stored-`agent_version` assertion (A-02). | A regression in these properties would go unseen. | (b) **executed** by probe: after 000006 plus a worker row, then 000007: `workers filenode before=8873141 after=8873141 same=true; worker_capacity viewdef same=true`, and the view uses `node_id` in 0 columns. The rest: **confirmed by code reading**. | Add the cases in (a), the relfilenode and viewdef assertions of (b) to `TestMigrator_Migration000007_UpgradeFrom000006_WithExistingRows`, and fix the docstring in (d). |
| A-12 | Info | internal/storage/postgres/migrations/000007_node_capability_model.sql:76-85; internal/storage/postgres/migrator.go:146-220 | The migrator applies each file in one transaction, so `ADD COLUMN` (ACCESS EXCLUSIVE on `workers`) is held through `ADD CONSTRAINT ... NOT VALID`, `VALIDATE` and `CREATE INDEX` until commit. NOT VALID then VALIDATE inside one transaction reduces no lock. This is the existing convention (000003:10-16, 000004:30-37) and ADR 3.4 requires the pattern. | ADR 12's "short locks on workers" holds only because `workers` is small (at most a few thousand rows). Not a V2-1b defect. | **confirmed by code reading**. The no-rewrite part was **executed** (A-11 b). | No change for V2-1b; note it in `docs/schema.md` section 5 when V2-1c touches it. |
| A-13 | Info | internal/domain/errors.go:54-57 | `RetryAfterSeconds` truncates (`int(RetryAfter / time.Second)`). It is exact today because `HardwareUpdateLimit` always returns whole seconds, but a hand-built 1.5 s gives 1. | A future caller could under-report Retry-After. | **executed** (`RetryAfterSeconds(1.5s)=1`). | Round up in `RetryAfterSeconds`, or document "whole seconds only". |

## 3. Audit items 1 to 12

### Item 1. Migration 000007
- CHECK names and bounds match ADR Section 9 one for one (migration lines 15-16, 38-52, 66-72): UUID regex ×2; `version > 0`; os/arch `^[a-z0-9_]{1,32}$`; models `^[ -~]{1,128}$`; cores 1..2^31−1; gpu_count 0..; logical ≥ physical; bytes 0..2^53−1 ×5; seq 1..2^53−1; load ≥ 0 (INTEGER caps 2^31−1); `sampled_at <= received_at`. **confirmed by code reading**, and behaviour by `TestSchemaInvariants_NodeChecks` (gate run, PASS).
- All three FKs to `nodes` are `ON DELETE RESTRICT` (lines 21, 57, 81). `fk_workers_node` is NOT VALID (80-81), then VALIDATE (82). Partial index at 85. No `node_tools` and no reservation columns. **confirmed by code reading**, and catalog checks by `TestMigrator_Migration000007_FreshSchema` (PASS).
- No table rewrite, and `worker_capacity` unchanged: **executed** (A-11 b).
- Checksums of 000001 to 000006 unchanged: `git diff origin/main..HEAD -- internal/storage/postgres/migrations/` shows only the new file (**executed**), and `TestMigrator_Migration000007_UpgradeFrom000006_WithExistingRows` compares stored and embedded checksums (PASS in `make test-db`).
- `[[:cntrl:]]`: see A-07. Not a defect.

### Item 2. RegisterNode (postgres/nodes.go:33-146) against ADR 8.1
- Order: validate (34), `INSERT ... ON CONFLICT DO NOTHING` (46-49), lock `FOR NO KEY UPDATE` (56-57; `FOR UPDATE` appears nowhere in `nodes.go`), `clock_timestamp()` read once after the lock (62-65), newest hardware row (74-83), decision (104-140). **confirmed by code reading**.
- Decision table: no row inserts v1 (105-112); same hash writes `nodes` only when the name changed or `HardwareUpdateLimit(lastSeenAt, now, interval)` is not limited (116-121); different hash and limited returns `NodeRateLimitedError` before any write (123-127), and the deferred rollback discards the no-op step 1; different hash and accepted updates `nodes`, inserts version+1 and deletes `version <= new−10` (128-139).
- On a 429, `display_name`, `last_seen_at` and history stay byte-identical: asserted with `sameDump` by `RunNodeHardwareRateLimitRejectsInsideIntervalConformance` (storagetest/nodes.go:299-327). The limited path has no write statement before the return. **confirmed by code reading**, test PASS.
- Pruning leaves exactly versions new−9..new and never the current row (the bound excludes it). Mutants prune11_pg and prune11_mem were killed (section 6).
- Lost update, double insert of a version: none. The `nodes` row lock serialises every reader of the newest version. Under READ COMMITTED the hardware SELECT after the lock takes a new snapshot that sees the previous holder's committed row. `(node_id, version)` is the primary key, so a duplicate would fail rather than corrupt. Brand-new race: the second `INSERT ... DO NOTHING` waits on the first transaction's uncommitted unique entry, then locks and reads its v1. **confirmed by code reading**. **executed**: `..._ConcurrentRegister_GapFreeVersions` (24 concurrent, starts with no node) and `..._ConcurrentSameContentRegister_OneVersion` (12 concurrent first registrations), 50/50 each under `-race`.
- The transaction holds the lock only across database round trips. No external call and no sleep run inside it. **confirmed by code reading**.

### Item 3. RecordNodeTelemetry (nodes.go:151-200)
- The SQL is the ADR 3.3 statement verbatim (171-183). It runs `SELECT 1 ... FOR KEY SHARE` first; no row gives `ErrNodeNotFound` (162-167). Zero rows give `ErrTelemetryOutOfOrder` (187-189). A 23503 on exactly `node_telemetry_node_id_fkey` maps to `ErrNodeNotFound` (190-193).
- Parameter types: **executed** by PREPARE of the same statement and a read of `pg_prepared_statements`: `{text,smallint,text,bigint,"double precision",integer,bigint,bigint}`. `$5` (`sample_age_ms`) is float8, exact for every allowed value. The largest age 2^31−1 stored `received_at − sampled_at = 24 days 20:31:23.647` = `2147483647.000000` ms exactly. `sampled_at` can therefore be up to 24.8 days old and before `created_at`, which ADR 9 leaves to the handler (builder item 11).
- Concurrency: the conditional `ON CONFLICT DO UPDATE` re-evaluates `WHERE` on the locked latest row. **executed**: `..._ConcurrentSameSession_OneWins` 50/50 under `-race`. With a concurrent `RegisterNode` holding `FOR NO KEY UPDATE`, telemetry did not block (lock probe: `holder=FOR NO KEY UPDATE RecordNodeTelemetry blocked=false`).
- A rejected report changes nothing, `received_at` included: mutants rej_writes_recv_pg and rej_writes_recv_mem were killed by `..._OutOfOrderRejected` and `..._ReplaySameSeqRejected`.

### Item 4. Deadlocks and lock order
- No existing path takes a node lock. **executed**: another transaction held the node row `FOR UPDATE` (stronger than anything V2-1b or V2-3 takes). For a worker on that node, `RecordHeartbeat`, `DispatchNextJob`, `RenewLease`, `CommitAttempt`, `ListWorkers`, `GetNode` and a re-registration with the same `node_id` all returned in under 30 ms (`blocked=false`). Only the paths that must take a node lock waited: a new worker naming the node (FK KEY SHARE), telemetry, and registration. Code reading agrees: `grep` of `internal/storage/postgres` shows `nodes` used only in `nodes.go`. The RI trigger skips the check when `node_id` is unchanged.
- Under a `FOR NO KEY UPDATE` holder (what `RegisterNode` takes), a new worker on the node and telemetry did not block; only a second registration did. So the FK `KEY SHARE` of `UpsertWorker` cannot wait on `RegisterNode`. `RegisterNode` never touches `workers` or `node_telemetry`, and telemetry never touches `workers`, so no lock cycle exists (**confirmed by code reading** plus probe). `..._MixedConcurrencyNoDeadlock` passed 50/50.
- SEC-036 (service mutex) and SEC-037 (poll round trips) are unchanged. V2-1b adds no coordinator path. `GetWorker` and `ListWorkers` gain a column, not a round trip. `TestFault_PollCost_DatabaseRoundTrips` and `TestFault_DBOutage_HalfOpen_OtherRequestsNotStalled` pass inside `make test-security` (80/0/0). **executed**.

### Item 5. Parity (PostgreSQL versus in-memory)
| Difference | Matters? |
|---|---|
| `ListNodes` empty: nil versus `[]` (A-05) | yes, before V2-1c |
| NUL in node ID or `node_id`: 22021 versus `ErrNodeNotFound`; a 300 KB `node_id`: 54000 versus `ErrNodeNotFound` (A-05, A-01) | yes, the handler must validate |
| In-memory view cost O(N×W) under the repository lock (A-03) | yes, before V2-1c |
| Time: microseconds versus nanoseconds; new node `last_seen_at − created_at` = 1.586 ms in PostgreSQL (executed) versus 0 in memory | no (RetryAfter is whole seconds) |
| Ordering: `ORDER BY id` uses the database collation, Go uses bytewise `sort.Strings`. On this container (musl `en_US.utf8`) both printed `["B1" "a-c" "a.b" "a1" "a_b" "ab"]` (executed). A glibc locale would order worker IDs like `a-c` and `ab` differently; node IDs are fixed-format UUIDs and order identically (inferred). | minor, only on glibc locales; use `ORDER BY id COLLATE "C"` |
| Validation: identical (both call `Validate()` first) | no |
| Aliasing: in memory, mutating a returned view did not change stored state, and mutating the input after `RegisterNode` did not either (executed: `first="B1" os="linux"`, `stored memory=5`) | no |
| Mutex: in memory, `mu` is the whole repository; PostgreSQL uses a row lock per node | expected |
| `WorkerIDs`: `[]` (not nil) in both | no |

### Item 6. Domain
- `HardwareUpdateLimit`: boundary exact (−1 ns limited 1 s; equal accepted; +1 ns accepted); rounds up; a clock that moved back is limited with the interval rounded up; overflow at huge intervals (A-10). **executed**.
- K1 golden hashes **recomputed independently**: a Python script written from the K1 text (not the builder's) and a `printf | sha256sum` byte stream for the minimal vector. All five golden values matched (`6f699dd7…`, `3685802d…`, `ecdec599…`, `19a8f4be…`, `868bfc2f…`); sha256sum gave `3685802d…` for the minimal vector. The length prefixes and markers make the encoding unambiguous for strings and integers. Versions are not hashed (A-02 for the consequence).
- `NodeTelemetryView` and `NodeView` carry no `session_id` and no `seq` (node.go:439-457). **confirmed by code reading**.
- Legacy Worker JSON: **executed**. The same throwaway marshal test run on origin/main and on d0893db produced byte-identical output (`cmp` reported IDENTICAL) for a populated legacy worker and the zero value.

### Item 7. mapPgError and the worker SQL
- Only `ConstraintName == fk_workers_node` maps to `ErrNodeNotFound` (postgres/repository.go:1598-1602). The fk_any23503 mutant is killed by `..._ForeignKeyMapping_ExactConstraintOnly`.
- `UpsertWorker` writes `node_id` on insert and on conflict (384-399); the upsert_no_nodeid_pg mutant is killed by four tests. `GetWorker` and `ListWorkers` read it through the subselect (431-432, 466-467).
- Every other `workers` read (assignment.go:75, 190, 388, 460, 477, 534, 636; leases.go:238; repository.go:645, 820, 894) selects named columns. No `SELECT *` exists in production code (`grep` empty), so nothing else breaks or leaks `node_id`. **confirmed by code reading**.

### Item 8. Builder finding 1
- Reproduced (A-01, **executed**).
- Is there any path before V2-1c that stores `node_id` for a real node or exposes it? **Not through the API on a deployment from `main`**: no route or coordinator method calls `RegisterNode` (`grep` shows callers only in storage and tests), so no node row exists, the FK or the in-memory check rejects every `node_id`, and the response never carries it. **Yes, through two non-API routes**: (1) a rollback from V2-1c (or later) to this code leaves node rows, and then this code stores `node_id` and `GET /v1/workers` shows it on any bind address (executed with bind `0.0.0.0`); (2) an operator inserts a `nodes` row by SQL.
- Acceptable for a merge to main? **Yes, conditionally.** No shipped client sends `node_id`. The 500 is logged with a fixed message and echoes no input (handlers.go:152-154). The bind default stays `127.0.0.1` (SEC-004). The rollback route needs V2-1c to have shipped first. The conditions are in section 1: no release between V2-1b and V2-1c, and the ADR 12 rollback note should add "a rollback to V2-1b exposes `node_id` in `GET /v1/workers` regardless of the bind address".

### Item 9. Abuse
See A-03 and A-04 (both measured). Hardware growth per node is bounded at 10 rows: the prune11 mutants were killed, and `GapFreeVersions` holds exactly 10 after 24 versions. Telemetry growth is 1 row per node (primary key `node_id`). Node rows themselves are unbounded (T2). Recommendation for V2-1c: (1) a summary-only, paginated `GET /v1/nodes` with a hard page cap; (2) fix the in-memory O(N×W) cost; (3) a ceiling on the total node count, or a global rate limit on new node IDs, until V3-2 authentication; (4) handler validation of every node ID; (5) an upper bound on the hardware interval.

### Item 10. Tests and mutants
Assertion review: every ADR 14.2 test exists and asserts what the ADR says. Weak or missing assertions are in A-06 and A-11. The K3 boundary is proven only by the pure-function table; the conformance tests use ±1 s margins, which is acceptable as stated in K3.

Mutants, run by me in `~/mc-mut-claude-v21b-audit`. Each run used `go test -count=1 -v ./internal/domain/ ./internal/storage/` (no URL) and `-run '^(TestConformance_|TestPostgresRepository_Node_|TestSchemaInvariants_Node|TestMigrator_Migration000007)' ./internal/storage/postgres/` (own database). After each mutant: `git checkout -- internal/`, a clean-tree check, then the failing tests re-run.

| Mutant | Mutation | Failing top-level tests | After restore |
|---|---|---|---|
| prune11_pg | `version-MaxHardwareHistory-1` | `TestConformance_Node_HardwareHistoryKeepsLatest10`, `TestPostgresRepository_Node_ConcurrentRegister_GapFreeVersions` | 3 PASS 0 FAIL |
| prune11_mem | keep `MaxHardwareHistory+1` | `TestConformance_Node_HardwareHistoryKeepsLatest10` | 2 PASS 0 FAIL |
| hul_le | `elapsed >= minInterval` → `>` (the `<` to `<=` limit) | `TestDomain_HardwareUpdateLimit` | 1 PASS 0 FAIL |
| fk_any23503 | `ConstraintName == fkWorkersNode \|\| true` | `TestPostgresRepository_Node_ForeignKeyMapping_ExactConstraintOnly` | 1 PASS 0 FAIL |
| seq_le_pg | `t.seq <= EXCLUDED.seq` | `..._Telemetry_NewSessionAcceptedAfterRestart`, `..._Telemetry_ReplaySameSeqRejected`, `TestPostgresRepository_Node_TelemetrySeq_ConcurrentSameSession_OneWins` | 5 PASS 0 FAIL |
| seq_le_mem | `t.seq > report.Seq` | `..._NewSessionAcceptedAfterRestart`, `..._ReplaySameSeqRejected` | 4 PASS 0 FAIL |
| nosession_pg | `WHERE t.seq < EXCLUDED.seq` only | `..._NewSessionAcceptedAfterRestart`, `..._OlderSessionAfterNewerIsAccepted` | 4 PASS 0 FAIL |
| nosession_mem | session comparison removed | same two | 4 PASS 0 FAIL |
| rej_writes_recv_pg | 409 path updates `received_at` and commits | `..._Telemetry_OutOfOrderRejected`, `..._ReplaySameSeqRejected` | 4 PASS 0 FAIL |
| rej_writes_recv_mem | 409 path sets `receivedAt` | same two | 4 PASS 0 FAIL |
| upsert_no_nodeid_pg | no `node_id = EXCLUDED.node_id`, NULL inserted | `TestConformance_Node_Views`, `TestConformance_Worker_NodeIDRoundTrip`, `TestConformance_Worker_UnknownNodeRejected`, `..._ForeignKeyMapping_ExactConstraintOnly` | 7 PASS 0 FAIL |
| upsert_no_nodeid_mem | `copied.NodeID = nil` | `..._Node_Views`, `..._Worker_NodeIDRoundTrip`, `..._Worker_UnknownNodeRejected` | 6 PASS 0 FAIL |
| rl_unchanged_pg | rate limit checked in the same-hash branch | `..._HardwareRateLimit_UnchangedNeverLimited`, `..._RegisterIdempotent`, `..._ConcurrentSameContentRegister_OneVersion`, `..._RegisterLocksNoKeyUpdate` | 6 PASS 0 FAIL |
| rl_unchanged_mem | same in memory | `..._UnchangedNeverLimited`, `..._RegisterIdempotent` | 4 PASS 0 FAIL |
| hash_nosep | separator and zero byte removed | `TestDomain_NodeHardwareContentHash` | 1 PASS 0 FAIL |
| for_update | node lock `FOR UPDATE` | `TestPostgresRepository_Node_RegisterLocksNoKeyUpdate` (killed) | 1 PASS 0 FAIL |
| noexist_pg | existence check replaced by `SELECT 1 WHERE $1::text IS NOT NULL` | **survived** (mem 119/0, pg 80/0) | n/a; killable, see A-06 |

The builder's survivor: they are right that no behaviour test in the suite kills it. They are wrong that it is equivalent (A-06).

### Item 11. Report accuracy
- Sampled **46** citations in reports/claude/V2-1b.md (migration 10, 20, 56, 76, 80, 82, 85; node.go 350, 354, 397, 414, 423, 429, 439, 449; errors.go 34, 37, 40, 46, 59; worker.go 27; repository.go 76, 85; postgres/repository.go 384, 390, 432, 467, 1598-1602; inmemory.go 293-297; storagetest/nodes.go 149, 221, 329, 738; nodes.go 116, 123, 136, 151, 162, 188, 191, 222; inmemory_nodes.go 127; migration_000007_test.go 48, 129, 227, 304; nodes_test.go 179, 204, 254; node_storage_test.go 23, 251; handlers.go 148-154, 289-293). **All correct.** The one imprecision: "encoding at 354 to 388" ends on a blank line. Counts verified: 52 new `func Test`, 18 `Run...` functions, 0 deleted tests, 0 lines of `go.mod`/`go.sum` diff (**executed**).
- "Beyond the ADR":

| # | Decision | Assessment |
|---|---|---|
| 1 | versions not hashed | acceptable; **needs ADR amendment** for the stale-view consequence (A-02) |
| 2 | K1–K7 | acceptable |
| 3 | repositories validate | acceptable |
| 4 | `last_seen_at` refresh at `elapsed >= interval` | acceptable; amendment wording ("at least", not "older than") |
| 5 | octet_length CHECKs | acceptable; **needs ADR amendment** for the CHECK text (A-09) |
| 6 | telemetry 23503 by exact name | acceptable |
| 7 | read model, ordering, REPEATABLE READ | acceptable, but nil versus `[]` (A-05) and the collation note (item 5) |
| 8 | `RetryAfter` Duration and `RetryAfterSeconds` | acceptable (A-13 nit) |
| 9 | `created_at` versus `last_seen_at` | acceptable (measured 1.586 ms) |
| 10 | `minInterval` 0 accepted | acceptable; negative values are also accepted and mean "no limit", so V2-1c must validate positive with an upper bound (A-10) |
| 11 | sample-age rule left to the handler | acceptable (ADR 9 says handler only) |
| 12 | test-only exports | acceptable |

- Gaps 1–3: correct. Gap 4 is wrong (A-07). **Missing from the list:** A-02, A-03, A-04, A-05, A-08, A-10, and the rollback exposure route of A-01.

### Item 12. Gates (executed in ~/MiniCloud-claude at d0893db after `go clean -testcache`; database `minicloud_claude_test`)

| Gate | Result | Builder |
|---|---|---|
| `gofmt -l .` | empty | empty |
| `go vet ./...` | exit 0 | exit 0 |
| `go build ./...` | exit 0 | exit 0 |
| `make test-docs` | `ok .../tests/docs`, exit 0 | ok |
| `env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...` | PASS 526 SKIP 130 FAIL 0, exit 0 | 526/130/0 |
| `env -u MINICLOUD_TEST_DATABASE_URL make test-race` | PASS 526 SKIP 130 FAIL 0, DATA RACE 0, exit 0 | 526/130/0, 0 |
| `make test-db` | PASS 205 SKIP 0 FAIL 0, DATA RACE 0, exit 0 | 205/0/0 |
| `make test-security` | PASS 80 SKIP 0 FAIL 0, exit 0 | 80/0/0 |
| five concurrency tests `-race -count=50` | 250 PASS (50 each) 0 FAIL, DATA RACE 0, `ok ... 218.021s` | 250/0 |

Counts are top-level `^--- PASS/SKIP/FAIL`, taken with `grep -c` from the logs. No flake, nothing rerun. `make test-outage` was not run (owner).

## 4. Attacks tried that found nothing
- A write on the 429 path: none (code reading; `sameDump` test; rl and N15-class mutants).
- Two registrations inserting the same version: impossible under the row lock plus the primary key (50× race test).
- A lost update of `display_name` or `last_seen_at`: none; the values are read under the lock.
- A node lock on an existing path: none (lock probe).
- A deadlock between FK KEY SHARE and NO KEY UPDATE: none (probe plus the 50× mixed test).
- A display-name CHECK that disagrees with the domain: none (A-07).
- A `node_id` leak in other worker scans: none (item 7).
- A rejected telemetry report touching `received_at`: none (mutants).
- Telemetry float typing of the age: exact.

## 5. Not verified
- `make test-outage` (owner).
- PostgreSQL on a glibc locale: the collation ordering note in item 5 is inferred. `[[:cntrl:]]` was verified only on this 16.15 musl build (en_US.utf8 and C).
- In-memory scale numbers were taken while the gate suite ran in parallel. They are upper bounds on this machine, not clean benchmarks. The 1 M-node figure is extrapolated.
- `VALIDATE CONSTRAINT` cost on a large `workers` table.
- Any V2-1c API behaviour: it does not exist yet. A-01 was probed only with the current handler.
- Behaviour under real network latency (SEC-037 class) for the new node paths.
