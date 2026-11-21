Task: V2-1-ADR-AUDIT2
Branch: agent/gemini/V2-1-ADR-audit2
Work tip (last commit before this report): 17ba5c1
Date: 2026-11-21

# V2-1-ADR-AUDIT2: Second Read-Only Audit of ADR-0022 (Node and Capability Model)

Read-only audit of `docs/adr/0022-node-capability-model.md` as merged on `origin/main` (`17ba5c1`), focusing on revisions and additions from fix round 1 (commit `3392ec4`) and fix round 2 (commit `8612b88`).
Auditor: Gemini (Antigravity). Read-only audit; no production code, ADRs, or tests were modified.

---

## 1. Re-check of Initial Audit Findings (F-01 through F-05)

The findings reported in `reports/gemini/V2-1-ADR-audit.md` were re-verified against the merged text of ADR-0022:

- **F-01 (Critical — Worker re-registration detach vulnerability)**: **TRULY FIXED**. Section 7.2 (lines 380–394) specifies that `reserved(node, dim)` sums `attempts.node_id` (snapshot column written at claim time) and `jobs.assigned_node_id` (snapshot column written at reservation time). It explicitly forbids reading `workers.node_id` for reservation accounting (`reserved(node) never reads workers.node_id`). Section 4.3 (lines 259–260) states that `workers.node_id` is mutable and not an accounting key, ensuring re-registration cannot evaporate or move running demand. Section 7.4 (line 412) enforces that claims re-check `workers.node_id` after acquiring the worker row lock.
- **F-02 (Major — Inexact commit/failure line citations)**: **TRULY FIXED**. Section 7.5 Table (lines 423–424) now cites `internal/storage/postgres/repository.go:1236-1252` (`UPDATE attempts`) and `:1257-1264` (`UPDATE jobs`), and line 424 cites `:1347-1352` for late terminal commit attempt closure.
- **F-03 (Minor — Truncated sweeper line citation)**: **TRULY FIXED**. Section 7.5 Table (line 426) now cites `internal/storage/postgres/leases.go:302-317` (`closeAttempt`, executing `UPDATE attempts` at line 310) and switch arms `:354-414` (invocations at lines 361, 371, 392, and 412).
- **F-04 (Major — Missing lock inversion and re-registration mutants)**: **TRULY FIXED**. Section 14.5 includes mutant R8 (claim locks worker row before node row, paired with `TestPostgresRepository_NodeLock_HierarchyEnforced`), mutant R9 (`reserved` joins `workers.node_id`, paired with `TestConformance_Worker_ReRegisterWithActiveAttemptsKeepsNodeDemand` and `TestConformance_Worker_ReRegisterWithLiveReservationKeepsNodeDemand`), and mutant R10 (claim omits re-reading `workers.node_id` after lock, paired with `TestPostgresRepository_NodeReserve_ClaimRechecksWorkerNodeAfterLock`).
- **F-05 (Minor — Live reservation placement blocking mutant coverage)**: **TRULY FIXED**. Section 14.4 (line 704) defines `TestConformance_NodeReserve_LiveReservationBlocksPlacement`, and Section 14.5 (line 749) pairs it with mutant R2.

---

## 2. Verification Analysis by Scope Requirement

### Requirement 1: Telemetry Sequence Design (Sections 3.3, 4.2, T1, T11)

1. **PostgreSQL READ COMMITTED Concurrency**:
   - The conditional upsert statement (Section 3.3, lines 130–143) uses:
     ```sql
     WITH c(ts) AS (SELECT clock_timestamp())
     INSERT INTO node_telemetry AS t (...)
     SELECT ... FROM c
     ON CONFLICT (node_id) DO UPDATE SET ...
     WHERE t.session_id <> EXCLUDED.session_id OR t.seq < EXCLUDED.seq
     RETURNING seq;
     ```
   - **First report ever**: If no row exists, the `INSERT` branch executes without conflict and returns the inserted `seq`. If two concurrent requests arrive simultaneously for a new node, one transaction inserts and commits; the second transaction encounters the primary key conflict on `node_id`, locks the row, re-evaluates the `WHERE` clause against the newly committed row, and either updates (if `t.session_id <> EXCLUDED.session_id` or `t.seq < EXCLUDED.seq`) or suppresses the update (0 rows returned).
   - **Same session, concurrent reports**: PostgreSQL's `ON CONFLICT DO UPDATE` locks the existing row before evaluating the `WHERE` condition. If report with `seq = 10` and report with `seq = 11` race, whichever transaction acquires the row lock first updates the row. If `seq = 11` commits first, the transaction for `seq = 10` sees `t.seq = 11 < EXCLUDED.seq = 10` evaluate to `false`. The update is skipped, 0 rows are returned, and the coordinator returns HTTP 409 `TELEMETRY_OUT_OF_ORDER`.
   - **Different session**: If `t.session_id <> EXCLUDED.session_id`, the condition evaluates to `true` regardless of `seq`, correctly allowing a new session to establish baseline telemetry.
2. **Honest Worker Lockout**:
   - An honest worker sends reports sequentially with strictly monotonic sequence numbers (`seq` starting at 1).
   - Retries after network timeouts use a new, incremented `seq` (Section 4.2 line 247: "uses a new seq for every attempt, including a retry after a timeout, so an honest sender is never rejected for reusing one").
   - If an honest worker encounters HTTP 409 (e.g. from network reordering or an attacker forging an inflated `seq` under T11), Section 4.2 line 247 defines that the worker draws a new `session_id`, resets to `seq = 1`, and resumes reporting. An honest worker cannot be permanently locked out.
3. **Session ID Reuse on Restart**:
   - Section 3.3 line 124 and Section 9 line 528 define `session_id` as a random RFC 9562 v4 UUID generated via `crypto/rand` (122 random bits).
   - The collision probability across process restarts is cryptographically negligible ($2^{-61}$ after $10^9$ sessions). A process restart will not reuse an active or historical session ID.
4. **Row Preservation on HTTP 409**:
   - When the `WHERE` clause evaluates to `false`, PostgreSQL skips the `SET` expressions entirely. No column in the existing `node_telemetry` row is modified (not even `received_at` or `sampled_at`). `RETURNING seq` produces 0 rows, signaling rejection to the coordinator. The stored row remains byte-identical.

### Requirement 2: Local-Only Rule (Q6, Sections 4.4, 13)

1. **Loopback Decision Mechanism**:
   - Evaluated once at coordinator startup from `MINICLOUD_HTTP_ADDR` (Section 4.4 line 268) by extracting the host portion and evaluating literal loopback IP representation via Go's `net.IP.IsLoopback` (`127.0.0.0/8` or `::1`).
   - The boolean flag is cached for the process lifetime and logged once. It is never recomputed per request and ignores client headers (`Host`, `X-Forwarded-For`).
2. **Behavior Matrix for Network Bind Inputs**:
   - `":8080"` (empty host, binds all interfaces): Host is empty string; `net.ParseIP("")` is nil. ADR explicitly lists `":8080"` as not local-only; fails closed (node routes not served, HTTP 404).
   - `"0.0.0.0:8080"`: `net.ParseIP("0.0.0.0").IsLoopback()` is `false`. Fails closed (HTTP 404).
   - `"[::1]:8080"`: `net.ParseIP("::1").IsLoopback()` is `true`. Local-only (routes served).
   - `"127.0.0.2:8080"`: `net.ParseIP("127.0.0.2").IsLoopback()` is `true` (`127.0.0.0/8`). Local-only (routes served).
   - `"localhost:8080"`: Hostname, not an IP literal; `net.ParseIP("localhost")` is nil. ADR explicitly specifies that hostnames fail closed because names are not resolved at startup and depend on runtime DNS/resolver configuration. Fails closed (HTTP 404).
   - `"[::]:8080"`: `net.ParseIP("::").IsLoopback()` is `false`. Fails closed (HTTP 404).
   - Hostnames (e.g. `example.com:8080`, `192.168.1.5:8080`): Fail closed (HTTP 404).
   - Planned test `TestConfig_NodeLocalOnly_Table` (Section 14.3 line 675) verifies this exact input set.
3. **Route Registration and Data Exposure Paths**:
   - The check is evaluated in `internal/api/server.go` (`NewServer`). When not local-only, node routes (`POST /v1/nodes/register`, `POST /v1/nodes/{id}/telemetry`, `GET /v1/nodes`, `GET /v1/nodes/{id}`) are not registered on `s.mux` and return standard HTTP 404.
   - `GET /v1/workers` omits the `node_id` key entirely (V1 response shape, not `null`).
   - `POST /v1/workers/register` ignores any incoming `node_id` and persists NULL.
   - `mc` CLI queries to `/v1/nodes` receive HTTP 404; worker listings show no `node_id`.
   - Structured logs omit sensitive hardware and telemetry metrics (Section 4.4 line 272).
   - No path serves node data when the bind address is non-loopback.

### Requirement 3: Rate Limit (Q1, Sections 3.2, 4.1, 8.1)

1. **Protocol and Versioning Compatibility (ADR-0004 & `docs/protocol.md`)**:
   - ADR-0004 Section 2.1 establishes that `/v1` evolution must be additive-only. `POST /v1/nodes/register` is an entirely new endpoint; introducing HTTP 429 `RATE_LIMITED` and a `Retry-After` header on this new route does not alter any existing endpoint's behavior or contract.
   - `docs/protocol.md` already specifies `Retry-After` headers for HTTP 503 responses (Section 2, line 21). Adding HTTP 429 with standard `Retry-After` is compatible with MiniCloud HTTP error envelope standards.
2. **Limit Bypass Analysis**:
   - **Changing `display_name`**: `display_name` is stored in `nodes`, whereas the rate limit evaluates the content hash of the `node_hardware` payload (`os, arch, cpu_model, cpu_logical_cores, cpu_physical_cores, memory_total_bytes, disk_total_bytes, gpu_count, gpu_model, gpu_vram_total_bytes`). Modifying `display_name` does not alter the hardware hash and does not trigger a new hardware version write. If a new hardware version is submitted alongside an altered `display_name` inside the rate limit window, Section 8.1 step 4 rolls back the entire transaction without writing anything ("Nothing is written, not even display_name").
   - **Changing `node_id`**: The rate limit is keyed by `node_id` in the database. Generating arbitrary new UUIDs creates new node rows, bypassing the per-node rate limiter (sybil threat T2). This is documented in Section 4.1 line 222 and Section 13 T2 as an accepted limitation of unauthenticated endpoints prior to V3-2 node enrolment.
3. **Retention Interaction and Restart Recovery**:
   - The rate limit guarantees that `node_hardware` versions cannot be inserted faster than once per `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`.
   - On valid version insertion, Section 8.1 step 4 prunes versions where `version <= newest - 10`, ensuring at most 10 hardware rows exist per node (`MaxHardwareHistory`).
   - On coordinator restart, workers re-register with identical hardware. The coordinator computes the content hash, detects an identical hash, skips hardware insertion, does not apply the rate limit, and returns HTTP 200 (Section 4.1 line 223, Section 8.1 step 4). Idempotent restart is unaffected.

### Requirement 4: Headroom Model (Section 7.7, Q5)

1. **Effective Capacity Consistency**:
   - Section 7.7 defines:
     `effective_capacity(node, dim) = max(0, hardware_total(node, dim) - headroom(node, dim))`
     `available_for_new_demand(node, dim) = effective_capacity(node, dim) - reserved(node, dim)`
   - This matches Section 7.3 Rule 2: `reserved(n) + d <= effective_capacity(n)`.
   - Section 7.7 line 464 affirms: "Headroom and telemetry only refuse. Headroom is subtracted and telemetry is a further bound; neither adds capacity, and unknown or stale input never satisfies a bound. OD-09 stays intact: capacity is never raised by an observation." Full consistency is maintained.
2. **Configuration Naming and Validation Style**:
   - The six variables:
     - `MINICLOUD_RESOURCE_MEMORY_STRATEGY` (`dynamic`)
     - `MINICLOUD_RESOURCE_MEMORY_MIN_FREE_RESERVE_MB` (int, non-negative)
     - `MINICLOUD_RESOURCE_MEMORY_RESERVE_PERCENT` (int, 0..99)
     - `MINICLOUD_RESOURCE_CPU_STRATEGY` (`pressure_aware`)
     - `MINICLOUD_RESOURCE_CPU_MAX_UTILIZATION_PERCENT` (int, 1..100)
     - `MINICLOUD_RESOURCE_CPU_MIN_IDLE_CORES` (int, non-negative)
   - These follow the exact naming convention (`MINICLOUD_` prefix, uppercase, underscores) and strict error formatting (`"%s must be an integer (got %q)"`) implemented in `internal/config/config.go` (SEC-062).
3. **Open Item D-1 (Worker Declines Claimed Attempt)**:
   - Owner answer Q5 states that the worker makes the final decision before starting a job.
   - ADR-0022 Section 7.7 line 482 and Section 16 line 800 properly identify that declining a claimed attempt involves state machine and lifecycle questions that belong to V2-3.
   - Specifically, D-1 explicitly highlights the potential conflict points:
     - State machine transition in `internal/domain` (Invariant 2);
     - Whether a decline increments attempt count toward `max_attempts` (ADR-0015);
     - Immediate release of attempt demand from `reserved(node)` (Section 7.5);
     - Interaction with worker capacity slot reclamation (ADR-0019) and assignment lapses / fallback (ADR-0020).
   - Because ADR-0022 does not implement D-1 in V2-1 and defers its design to V2-3 with these exact constraints cited, it introduces no contradiction with ADR-0019, ADR-0020, or AGENTS.md.

### Requirement 5: Opt-In Rule (Q2, Sections 4.5, 5)

1. **Behavior Matrix Completeness**:
   - `MINICLOUD_NODE_REPORTING_ENABLED=false` (or unset), directory unset: Reporting off; no filesystem inspection; no network calls; byte-identical V1 worker register body.
   - `MINICLOUD_NODE_REPORTING_ENABLED=false`, directory set: Reporting off; directory is not touched or stat-ed; exactly one warning logged that setting is ignored without consent.
   - `MINICLOUD_NODE_REPORTING_ENABLED=true`, directory unset: Directory defaults to `~/.minicloud` (0700 dir, 0600 file).
   - `MINICLOUD_NODE_REPORTING_ENABLED=true`, directory set: Validated as absolute path; used as state directory.
   - `MINICLOUD_NODE_REPORTING_ENABLED=true`, directory unset, home unresolvable: Refuses to start with an immediate startup configuration error naming both variables (fails closed).
   - Existing `node_id` file with wide permissions (0644): Refuses to start with an error.
   - *Omission identified*: Unwritable state directory (e.g. permission denied creating `~/.minicloud` or writing the temporary file) is not explicitly itemized in the behavior text or test plan (Finding F-10).
2. **Consent Invariant**:
   - In all non-opt-in configurations, no hardware probes run, no state files are accessed or created, and no node endpoints are called. Hardware and telemetry observations are collected and transmitted strictly upon explicit consent.

### Requirement 6: Consistency & Test Plan

1. **Owner Answers**:
   - Q1 through Q7 are accurately recorded in Section 16 (lines 783–792).
   - OQ-8 (60s default for `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`) and the `sample_age_ms` plus database receive time approach were confirmed by the owner on 2026-11-20 evening; their continued notation as open/proposed in ADR-0022 lines 123, 221, and 799 is noted as an Informational update for the next fix round (Findings F-11, F-12).
2. **Section References**:
   - All 90+ internal section references across ADR-0022 were audited against the document's section headings and confirmed semantically accurate.
3. **Planned Tests and Mutants**:
   - All 86 planned test definitions across Sections 14.1–14.4 have unique names; no duplicates exist.
   - Mutant `N12` (line 761) is placed out of sequence at the end of Section 14.5 after `R13`, rather than between `N11` and `N13` (Finding F-08).
   - 8 test entries in Section 14.4 (lines 695–698, 705–708) list test names next to path headers without explicit expectation clauses (Finding F-09).
   - 42 planned tests in Sections 14.1–14.4 (including validation matrices, migration upgrades, and abuse limit pins) do not have corresponding entries in Table 14.5 (Finding F-13).

### Requirement 7: Code Citations against `origin/main` (`17ba5c1`)

A sample of 30 code citations across the codebase was verified verbatim against `origin/main` at commit `17ba5c1`:

1. `internal/storage/postgres/repository.go:24-33`: Distributed locking order comment (`jobs -> workers -> attempts -> results -> job_events`).
2. `internal/domain/worker.go:9`: `ID string `json:"id"``.
3. `internal/worker/client.go:354`: `"id": a.cfg.WorkerID,`.
4. `internal/api/handlers.go:289-293`: `HandleRegisterWorker` JSON decode into `domain.Worker`.
5. `internal/api/handlers.go:52-66`: `isValidIdentifier` (1..128 chars, `^[a-zA-Z0-9_-]+$`).
6. `internal/coordinator/service.go:193-197`: Constants `MinWorkerSlots` (1) through `MaxTaskNameLength` (64).
7. `internal/storage/postgres/repository.go:383-392`: `UpsertWorker` SQL with `ON CONFLICT (id) DO UPDATE SET ... last_heartbeat_at = CURRENT_TIMESTAMP;`.
8. `internal/storage/inmemory.go:284-302`: `InMemoryRepository.UpsertWorker` map update.
9. `internal/storage/postgres/assignment.go:701`: `INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state ...)` in `ClaimAssignedWork`.
10. `internal/storage/postgres/assignment.go:719-721`: `UPDATE jobs SET state = 'RUNNING' ... assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL, assignment_lapses = 0` in `ClaimAssignedWork`.
11. `internal/coordinator/service.go:416-418`: `PollWork` acquires `s.mu.Lock()` and defers `s.mu.Unlock()`.
12. `internal/storage/postgres/repository.go:1236-1252`: `UPDATE attempts SET state = $1, error = $2, error_class = $3, finished_at = CURRENT_TIMESTAMP ...` in `CommitAttempt`.
13. `internal/storage/postgres/repository.go:1257-1264`: `UPDATE jobs SET state = $1, not_before = $2, updated_at = $3 WHERE id = $4 RETURNING updated_at;` in `CommitAttempt`.
14. `internal/storage/postgres/repository.go:1347-1352`: Late terminal commit closure `UPDATE attempts SET state = $1, finished_at = CURRENT_TIMESTAMP WHERE id = $2;`.
15. `internal/storage/postgres/repository.go:1456-1459`: `UPDATE jobs SET state = $1 ... assigned_worker_id = NULL ... WHERE id = $2 AND state IN ('QUEUED', 'RETRYING')`.
16. `internal/storage/postgres/repository.go:1465-1466`: `UPDATE jobs SET state = $1, updated_at = CURRENT_TIMESTAMP, cancel_requested_at = CURRENT_TIMESTAMP WHERE id = $2 AND state = 'RUNNING'`.
17. `internal/storage/postgres/leases.go:302-317`: `closeAttempt := func(state domain.AttemptState) error { ... UPDATE attempts ... }`.
18. `internal/storage/postgres/leases.go:354-414`: `SweepExpiredLeases` switch cases invoking `closeAttempt`.
19. `internal/storage/postgres/assignment.go:216`: Revocation SQL clearing `assigned_worker_id` on unhealthy worker.
20. `internal/storage/postgres/assignment.go:231`: Lapse SQL incrementing `assignment_lapses` and clearing `assigned_worker_id`.
21. `internal/storage/postgres/repository.go:503-510`: `ReapStaleWorkers` SQL query transitioning stale workers to `UNHEALTHY`.
22. `internal/coordinator/sweeper.go:19`: `DefaultStartupGracePeriod = 30 * time.Second`.
23. `internal/storage/inmemory.go:720, 724`: `job.TransitionTo(targetState)` and `att.State = attState` in in-memory `CommitAttempt`.
24. `internal/storage/inmemory.go:784-788`: Late terminal commit attempt closure in `rejectTerminalCommitLocked`.
25. `internal/storage/inmemory.go:857-861`: In-memory cancellation logic in `CancelJob`.
26. `internal/storage/inmemory.go:1026-1061`: In-memory attempt closure and transition handling in `SweepExpiredLeases`.
27. `internal/storage/inmemory_assignment.go:136, 150`: In-memory assignment revocation and lapse handling in `AssignQueuedJobs`.
28. `internal/storage/inmemory_assignment.go:307, 338`: `j.ClearAssignment()` and `r.attempts[attemptID] = stored` in `ClaimAssignedWork`.
29. `internal/storage/inmemory.go:350-364`: `ReapStaleWorkers` in-memory scan.
30. `internal/config/config.go:139, 170`: `MINICLOUD_HTTP_ADDR` default and non-empty check.

**Citations added in Fix Round 2**:
- `docs/agents/handoffs/V2-1-owner-answers.md`: verified on disk.
- `docs/security/findings.md` SEC-070: verified on disk at `docs/security/findings.md:82` and `:643-655`.

---

## 3. Table of Findings

| ID | Severity | File:Line | What | Why it matters | Suggested fix wording |
|---|---|---|---|---|---|
| **F-08** | **Minor** | `docs/adr/0022-node-capability-model.md:761` | Mutant `N12` ("A foreign key to `nodes` uses `ON DELETE CASCADE` (or no action) instead of `RESTRICT`") is placed at the end of Section 14.5 Table (after `R13`), breaking mutant sequence ordering (`N1..N11, N13..N29, R1..R13, N12`). | Disrupts table organization and grouping of node vs resource mutants, creating potential confusion during mutation test verification. | Move mutant `N12` row up between `N11` and `N13` in Section 14.5. |
| **F-09** | **Minor** | `docs/adr/0022-node-capability-model.md:695-698`, `:705-708` | In Section 14.4, eight table rows list test names without an explicit descriptive expectation sentence (e.g. `TestConformance_NodeReserve_ReleasedOnCommitSuccess` has only the path header "Completion"). | Unlike other rows in Table 14.4 that specify exact failure/success criteria, these rows require inferring expectations from Section 7.5. Unambiguous expectation sentences eliminate ambiguity for future V2-3 test implementers. | Add explicit behavioral expectation clauses for each of the eight rows in Section 14.4 (e.g. for line 695: `TestConformance_NodeReserve_ReleasedOnCommitSuccess (planned test): successful attempt commit transitions attempt to COMPLETED and releases attempt demand from reserved(node), returning node demand to 0`). |
| **F-10** | **Minor** | `docs/adr/0022-node-capability-model.md:288-289`, `:301`, `:621-624` | Sections 4.5 and 5 detail opt-in consent and state directory resolution, but do not explicitly specify behavior when the state directory is unwritable (e.g. permission denied creating `~/.minicloud` or writing the temporary ID file), and Section 14.1 lacks a planned test. | If the state directory cannot be created or written to, the worker must fail fast at startup with a configuration error rather than panicking or falling back to legacy mode. | In Section 4.5 line 289 and Section 5 line 301, state that failure to create the state directory or write the initial node ID due to filesystem permissions causes the worker to refuse startup with a configuration error. Add planned test `TestNodeIDFile_DirectoryUnwritable_Refuses` in Section 14.1. |
| **F-11** | **Info** | `docs/adr/0022-node-capability-model.md:221`, `:799` | `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` placeholder default of 60 s was confirmed by the owner on 2026-11-20 evening (OQ-8), but Section 4.1 line 221 and Section 16 line 799 still label it as open / proposed. | Known documentation synchronization edit for the next fix round. | Update Section 4.1 line 221 and Section 16 to record OQ-8 as decided with a 60 s default, moving OQ-8 to the "Answered by the owner" table. |
| **F-12** | **Info** | `docs/adr/0022-node-capability-model.md:122-123` | Section 3.3 line 123 labels `sample_age_ms` plus database receive time as "[Proposed, a deviation from the literal wording that the owner may reverse]". The owner confirmed this approach on 2026-11-20 evening. | Known documentation synchronization edit for the next fix round. | Update label in Section 3.3 line 123 to `[Decided: owner confirmation, 2026-11-20]`. |
| **F-13** | **Info** | `docs/adr/0022-node-capability-model.md:614-687`, `:718-761` | 42 planned tests in Sections 14.1–14.4 do not have corresponding mutant entries in Table 14.5 (e.g. input validation tables, abuse attack pins, migration schema checks). | Mutants in Table 14.5 target architectural invariants (R1–R13, N1–N29). Noting why direct assertion tests do not have mutants clarifies the testing strategy. | In Section 14.5 preamble, add a note clarifying that Table 14.5 focuses on core architectural invariant mutations, while input bounds matrices, schema constraints, and abuse tests rely on direct test case assertions. |

---

## 4. Not Verified

1. **No planned tests or mutants were executed**: As stated in ADR-0022 Section 14, all tests are planned tests for future implementation slices (V2-1a through V2-3) and do not exist on the branch or `origin/main`.
2. **Platform probes on non-Linux operating systems**: Probe behavior on Windows, macOS, and BSD was evaluated by code review and specification design only; non-Linux kernels were not tested.
3. **Database execution under live PostgreSQL**: SQL statements for conditional telemetry upsert and node row locking were evaluated by theoretical relational lock analysis under PostgreSQL 16 semantics; no live queries were run against a database.

---

## 5. Preflight and Quality Gates (Executed)

- **Preflight**:
  - Worktree: `/home/blazeee/MiniCloud-gemini`
  - Branch: `agent/gemini/V2-1-ADR-audit2`
  - Baseline commit: `origin/main` (`17ba5c1`)
  - Go version: `go1.22.2 linux/amd64`
- **Gates**:
  - `gofmt -l .`: clean (0 unformatted files)
  - `go vet ./...`: rc=0 (clean)
  - `go build ./...`: rc=0 (clean)
  - `make test-docs`: PASS (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 2.847s`)
