Task: V2-1-ADR-audit
Branch: agent/gemini/V2-1-ADR-audit
Work tip (last commit before this report): 846f882
Date: 2026-11-20

# V2-1-ADR-audit: Read-only Audit of ADR-0022 (Node and Capability Model)

Audit of `docs/adr/0022-node-capability-model.md` on branch `agent/claude/V2-1-ADR` (tip `846f882`), audited against `origin/main` (`588fcfd`).
Auditor: Gemini (Antigravity). Read-only audit; no production code, ADRs, or tests were modified.

---

## Verification Analysis by Requirement

### 1. Code Citations in ADR-0022 against `origin/main` (`588fcfd`)

Every path and line citation in ADR-0022 was verified directly against `origin/main` at commit `588fcfd`:

- `internal/storage/postgres/repository.go:24-33` (ADR line 29): **Accurate**. Matches the distributed locking order invariant comment defining the lock hierarchy (`jobs -> workers -> attempts -> results -> job_events`).
- `internal/domain/worker.go:9` (ADR line 31): **Accurate**. Line 9 is `ID string `json:"id"``.
- `internal/worker/client.go:354` (ADR line 31): **Accurate**. Line 354 is `"id": a.cfg.WorkerID,`.
- `internal/api/handlers.go:289-293` (ADR line 31): **Accurate**. Decodes worker registration JSON into `domain.Worker` ignoring unknown fields.
- `internal/api/handlers.go:52-66` (ADR line 32): **Accurate**. Defines `isValidIdentifier` (1..128 chars, `^[a-zA-Z0-9_-]+$`).
- `internal/coordinator/service.go:193-197` (ADR line 32): **Accurate**. Defines `MinWorkerSlots` (1), `MaxWorkerSlots` (1024), `MaxWorkerVersionLength` (64), `MaxWorkerSupportedTasks` (16), `MaxTaskNameLength` (64).
- `internal/coordinator/service.go:326-348` (ADR line 32): **Accurate**. Validates worker `total_slots`, `version`, and deduplicated `supported_tasks`.
- `internal/storage/postgres/repository.go:383-392` (ADR line 212): **Accurate**. `UpsertWorker` query containing `ON CONFLICT (id) DO UPDATE SET version = ..., total_slots = ..., executes_concurrently = ..., status = ..., supported_tasks = ..., last_heartbeat_at = CURRENT_TIMESTAMP;`.
- `internal/storage/postgres/assignment.go:721` (ADR line 311): **Accurate**. `ClaimAssignedWork` query resetting `assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL, assignment_lapses = 0`.
- `internal/coordinator/service.go:416-418` (ADR line 333): **Accurate**. `PollWork` acquires `s.mu.Lock()` and defers `s.mu.Unlock()`.
- `internal/storage/postgres/repository.go:1197-1198` (ADR line 341): **Inexact / Misleading**. Points to local in-memory variable assignment `targetAttemptState, targetJobState, jobEventType = domain.AttemptCompleted, ...` inside a Go `switch`, rather than the SQL database state update (`internal/storage/postgres/repository.go:1236-1252` and `:1257-1264`). (Finding F-02).
- `internal/storage/postgres/repository.go:1199-1204` (ADR line 342): **Inexact / Misleading**. Points to local in-memory variable assignment for retry, cancelled, and failed branches, rather than the database update at lines 1236-1252 and 1257-1264. (Finding F-02).
- `internal/storage/postgres/repository.go:1347-1352` (ADR line 342): **Accurate**. Line 1352 executes `UPDATE attempts SET state = $1, finished_at = CURRENT_TIMESTAMP WHERE id = $2;` for late terminal commit.
- `internal/storage/postgres/repository.go:1459` and `:1466` (ADR line 343): **Accurate**. Line 1459 clears assignment (`assigned_worker_id = NULL...`) for queued/retrying jobs, and line 1466 sets `cancel_requested_at` for running jobs.
- `internal/storage/postgres/leases.go:357-411` (ADR line 344): **Inexact / Truncated**. Lines 357-411 cover the sweeper switch arms, but the final case (`jobState == domain.StateRunning`) executes `closeAttempt(finalAttemptState)` at line 412 (and `closeAttempt` itself executes `UPDATE attempts` at lines 302-317). Line 411 is only variable assignment. (Finding F-03).
- `internal/storage/postgres/assignment.go:216` and `:231` (ADR line 344): **Accurate**. SQL updates clearing `assigned_worker_id` on unhealthy worker revocation (line 216) and lapsed reservation (line 231).
- `internal/storage/postgres/repository.go:503-510` (ADR line 344): **Accurate**. `ReapStaleWorkers` implementation transitioning healthy stale workers to `UNHEALTHY`.
- `internal/coordinator/sweeper.go:19` (ADR line 345): **Accurate**. `DefaultStartupGracePeriod = 30 * time.Second`.
- `internal/worker/client.go` func `ComputeBackoffWithJitter` (ADR line 402): **Accurate**. Defined at `internal/worker/client.go:77-106`.
- `docs/security/findings.md:81` (ADR line 454): **Accurate**. Line 81 is SEC-069, the final entry in the findings table on `origin/main`.
- All 8 existing test functions cited in ADR line 410 exist on `origin/main` (`internal/storage/inmemory_assignment_test.go`, `internal/storage/postgres/assignment_test.go`, `internal/api/register_concurrent_test.go`, `internal/api/api_test.go`, and `tests/integration/postgres_wiring_test.go`).
- All 15 existing files cited in Section 15 exist on `origin/main`.

---

### 2. Lock Order (Section 7.4), Storage Paths, and PostgreSQL 16 Documentation

1. **Inspection of existing PostgreSQL storage paths in `internal/storage/postgres/`:**
   - Every existing transaction touching `jobs` and `workers` acquires locks in strict sequence `jobs -> workers`:
     - `DispatchJob` (`repository.go:616-640`): locks `jobs` row `FOR UPDATE`, then locks `workers` row `FOR UPDATE`, then inserts into `attempts`.
     - `DispatchNextJob` (`repository.go:850-890`): candidate `jobs` row locked `FOR UPDATE SKIP LOCKED`, then locks `workers` row `FOR UPDATE`, then inserts into `attempts`.
     - `SweepExpiredLeases` (`leases.go:222-245`): locks `jobs` row `FOR UPDATE`, then locks `workers` row `FOR UPDATE`, then locks `attempts` row `FOR UPDATE`.
     - `AssignQueuedJobs` (`assignment.go:24-26, 460`): takes advisory lock, then `jobs FOR UPDATE SKIP LOCKED`, then one worker row `FOR UPDATE` under `lock_timeout`.
     - `ClaimAssignedWork` (`assignment.go:607, 636`): locks `jobs` row `FOR UPDATE SKIP LOCKED`, then locks `workers` row `FOR UPDATE`, then inserts into `attempts`.
   - **No existing path locks `workers` before `jobs`.**
   - Single-table worker paths (`UpsertWorker`, `RecordHeartbeat`, `ReapStaleWorkers`) do not lock `jobs` or `attempts`.

2. **Does any existing path lock a worker row and then need the node row?**
   - When migration 000007 adds `CONSTRAINT fk_workers_node FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT`, any `INSERT` or `UPDATE` on `workers` specifying a non-null `node_id` (`UpsertWorker` at `repository.go:384-392`) locks the referenced `nodes` row via foreign key constraint verification while holding the `workers` row lock.
   - Thus, `UpsertWorker` acquires: `workers` (row lock) -> `nodes` (`FOR KEY SHARE` via FK).
   - In contrast, reservation/claim in V2-3 acquires: `jobs` -> `nodes` (`FOR NO KEY UPDATE`) -> `workers` (`FOR UPDATE`).

3. **Is the claim "FOR KEY SHARE (FK) does not conflict with FOR NO KEY UPDATE" consistent with PostgreSQL 16 documentation?**
   - **Yes, fully consistent.**
   - Quoting PostgreSQL 16 Documentation, Chapter 13 (*Concurrency Control*), Section 13.3.2 (*Row-Level Locks*):
     > **`FOR NO KEY UPDATE`**
     > "Behaves similarly to `FOR UPDATE`, except that the lock acquired is weaker: this lock will not block `SELECT FOR KEY SHARE` commands that attempt to acquire a lock on the same rows. This lock mode is also acquired by any `UPDATE` that does not acquire a `FOR UPDATE` lock."
     >
     > **`FOR KEY SHARE`**
     > "Behaves similarly to `FOR SHARE`, except that the lock is weaker: `SELECT FOR UPDATE` is blocked, but not `SELECT FOR NO KEY UPDATE`. A key-shared lock blocks other transactions from performing `DELETE` or any `UPDATE` that changes the key values, but it does not block other `UPDATE`, nor does it prevent `SELECT FOR NO KEY UPDATE`, `SELECT FOR SHARE`, or `SELECT FOR KEY SHARE`."
     >
     > **Table 13.3: Conflicting Row-Level Locks**
     > | Requested Lock Mode | Current Lock Mode: `FOR KEY SHARE` | Current Lock Mode: `FOR NO KEY UPDATE` |
     > | :--- | :---: | :---: |
     > | **`FOR KEY SHARE`** | Allowed | Allowed |
     > | **`FOR NO KEY UPDATE`** | Allowed | **X** (Conflict) |
   - Because `FOR KEY SHARE` and `FOR NO KEY UPDATE` do not conflict, concurrent `UpsertWorker` and node-locking claims do not deadlock. If `FOR UPDATE` were used instead, Table 13.3 shows a direct conflict (**X**), which would produce deadlocks.

---

### 3. Worker Re-registration and Dynamic `reserved(node)` Derivation (Section 7.2 vs 4.3)

1. **The Mechanism:**
   - Section 7.2 defines:
     `reserved(node, dim) = sum of demand snapshot of every attempt in PENDING or RUNNING whose worker has node_id = node + sum of demand of every job with a live round-robin reservation ... to a worker with node_id = node`.
   - Section 4.3 states:
     `Every registration rewrites node_id (as UpsertWorker already rewrites every field, internal/storage/postgres/repository.go:383-392), so the latest registration under a worker ID wins.`
2. **What happens when a worker with running attempts re-registers with a different `node_id` or `NULL`?**
   - Because `node_id` is NOT snapshotted onto the `attempts` row (Section 7.2 only snapshots demand), the calculation of `reserved(node)` dynamically joins `attempts` against `workers.node_id`.
   - **Case A: Worker re-registers with `NULL`** (e.g. fallback to legacy mode described in Section 10 line 403, or temporary misconfiguration):
     `workers.node_id` becomes `NULL`. The demand of all currently running attempts on that worker immediately evaporates from `reserved(node)`. The original node's reserved count drops, allowing new jobs to be scheduled onto that node despite the physical capacity still being occupied, causing physical resource exhaustion (OOM).
   - **Case B: Worker re-registers with a different `node_id`:**
     The running attempts immediately detach from node 1 and attach to node 2. Node 1 has its reservations dropped (permitting overcommitment), while node 2 gains phantom reservations for attempts physically running on node 1.
   - **Security Impact:** Because worker registration is unauthenticated (SEC-004 / T1), any network caller on the bind address can issue a registration for an existing worker ID with `node_id = NULL` or a forged node ID, instantly wiping out or corrupting node resource accounting.
3. **Does the ADR cover it?**
   - **No.** The ADR does not mention, analyze, or mitigate this failure mode. (Finding F-01, Critical/Major).

---

### 4. OD-09 Paths (Completion, Failure, Cancellation, Lease Expiry, Restart) & Edge Paths

1. **State change points:**
   - **Completion & Failure**: Lines 1197-1204 of `repository.go` set in-memory Go variables. The actual transactional state changes occur at `repository.go:1236-1252` (`UPDATE attempts`) and `:1257-1267` (`UPDATE jobs`).
   - **Cancellation**: Lines 1459 and 1466 correctly identify the SQL SET clauses for `jobs` (clearing `assigned_worker_id` for queued/retrying, and setting `cancel_requested_at` for running). Running attempts keep their reservation until commit or lease sweep, adhering to Invariant 6.
   - **Lease Expiry**: Sweeper switch arms at `leases.go:357-414` execute `closeAttempt` (defined at lines 302-317) which marks attempts `LOST` or `CANCELLED`. Line 411 in the ADR citation omitted line 412 where the final `closeAttempt` is invoked.
   - **Restart**: Handled correctly by durable rows and startup grace periods.
2. **Missing / Edge Paths:**
   - **Worker UNHEALTHY**: Covered correctly. ADR line 344 notes that `ReapStaleWorkers` only marks workers `UNHEALTHY` (`repository.go:503-510`) and does not release demand; attempts retain reservations until swept.
   - **Job Timeout**: MiniCloud enforces job execution timeout worker-side (`ClampExecutionTimeout` in `internal/worker/client.go:790`). Timeout results in a failed attempt report, entering the standard `CommitAttempt` failure path. If the worker hangs, the lease sweeper handles it. No separate coordinator job-timeout path is missing.
   - **Retry**: Handled correctly. Attempt fails, demand is released, job transitions to `RETRYING` with `not_before` set in future. A retrying job holds 0 demand until reassigned.
   - **In-Memory Repository**: Section 7.5 only cites PostgreSQL repository files, omitting equivalent points in `internal/storage/inmemory.go` and `internal/storage/inmemory_assignment.go`.

---

### 5. Section 9 Limits vs SEC-008, SEC-015, SEC-016, SEC-017, and `docs/security/field-limits.md`

- **SEC-008 (Identifier Limits)**: Bounded at <= 128 chars matching `^[a-zA-Z0-9_-]+$`. `node_id` is 36 bytes lowercase RFC 9562 v4 UUID (`chk_nodes_id_uuid`), which is a strict subset of SEC-008 charset and length. `{node_id}` route parameters pass `isValidIdentifier` and undergo UUID format checks.
- **SEC-015 (`total_slots`)**: `[1, 1024]`. Retained unchanged.
- **SEC-016 (`version`)**: 0..64 chars. `agent_version` uses the same bound (`VARCHAR(64)` with `chk_node_hardware_agent_version`).
- **SEC-017 (`supported_tasks`)**: <= 16 unique tasks, each matching `^[a-z0-9_]{1,64}$`. Unaffected by V2-1.
- **`field-limits.md` Consistency**:
  - Request body limit is 1 MiB, matching `docs/protocol.md` section 2.
  - Integer caps for counts (0..2^31 - 1) and bytes (0..2^53 - 1) preserve IEEE 754 precision in JSON and prevent BIGINT overflow across up to 1024 workers.
  - Section 9 states new rows will be added to `docs/security/field-limits.md` in slice V2-1c. No contradictions exist.

---

### 6. Section 14 Tests and Mutants Analysis

1. **Do named tests fail under their mutants?**
   - **N1-N11**: All 11 mutants correctly target assertions in their paired planned tests.
   - **R1, R3-R7**: Correctly target their corresponding planned tests.
   - **R2**: Mutant R2 ("`reserved` ignores live round-robin reservations") is paired with `TestConformance_NodeReserve_ClaimMovesDemandWithoutDoubleCount`. That test verifies claim transition, but does not explicitly prove that an unconsumed live round-robin reservation blocks subsequent placement. (Finding F-05).
2. **Unlisted Dangerous Mutants:**
   - **Lock Inversion in Claim/Reservation**: What if a claim locks `workers` row before `nodes` row? This would invert lock order against concurrent reservations, deadlocking in production. (Finding F-04).
   - **Worker Re-registration altering `node_id` with active attempts**: No mutant verifies that re-registration cannot detach running attempts or corrupt `reserved(node)`. (Finding F-04).
   - **`ON DELETE RESTRICT` on FKs**: No mutant verifies that deleting a node fails when workers or hardware rows exist.

---

### 7. Contradictions with Existing Documentation and ADRs

- **`docs/protocol.md`**: Section 4.1 shows worker registration key `"worker_id"`, whereas the codebase uses `"id"`. ADR-0022 lines 36-37 correctly identifies this pre-existing documentation bug and schedules correction in slice V2-1c.
- **`docs/schema.md`**: Section 1 states lock order as `jobs -> workers -> attempts -> results -> job_events`. ADR-0022 Section 7.4 amends this to `jobs -> nodes -> workers -> attempts -> results -> job_events`. This is an intentional architectural amendment.
- **`ADR-0020`**: ADR-0022 builds directly on ADR-0020's `(A + R)` concept, extending it from worker slot reservations to node-level multi-dimensional resource reservations. No contradictions found.

---

## Table of Findings

| ID | Severity | File:Line | What | Why it matters | Suggested fix wording |
|---|---|---|---|---|---|
| **F-01** | **Critical** | `docs/adr/0022-node-capability-model.md:212`, `:305-309` | Worker re-registration rewrites `node_id` unconditionally while attempts are running; `reserved(node)` dynamically joins `attempts` via `workers.node_id`. | If a worker with running attempts re-registers with `NULL` or a different `node_id`, its running attempts detach from the original node, evaporating reserved demand and permitting physical node overcommitment (OOM). Unauthenticated callers (SEC-004/T1) can forge registration to corrupt node reservations. | In Section 7.2, specify that `node_id` must be snapshotted onto `attempts` at claim time (`attempts.node_id TEXT`), and `reserved(node)` must sum attempts where `attempts.node_id = node`. In Section 4.3, specify that worker registration rejects changing or clearing `node_id` while the worker has active attempts or live reservations. |
| **F-02** | **Major** | `docs/adr/0022-node-capability-model.md:341-342` | Table 7.5 cites `internal/storage/postgres/repository.go:1197-1198` and `:1199-1204` as the code that changes state for completion and failure. | On `origin/main` (588fcfd), lines 1197-1204 only assign local in-memory variables in a Go `switch`. The actual database state change and demand release occurs at lines 1236-1252 (`UPDATE attempts`) and lines 1257-1267 (`UPDATE jobs`). Citing local variable assignment misidentifies the transactional commit boundary. | In line 341, update citation to `internal/storage/postgres/repository.go:1236-1252` (attempt) and `:1257-1264` (job). In line 342, update citation to `internal/storage/postgres/repository.go:1236-1252`, `:1257-1264`, and `:1347-1352`. |
| **F-03** | **Minor** | `docs/adr/0022-node-capability-model.md:344` | Table 7.5 cites `internal/storage/postgres/leases.go:357-411` for lease expiry attempt closure. | Truncates the switch block before line 412, where `closeAttempt(finalAttemptState)` is actually invoked for the terminal `jobState == domain.StateRunning` case. Furthermore, `closeAttempt` executes `UPDATE attempts` at lines 302-317. | In line 344, update citation to `internal/storage/postgres/leases.go:302-317, 357-414`. |
| **F-04** | **Major** | `docs/adr/0022-node-capability-model.md:541-547` | Section 14.5 omits dangerous mutants for lock inversion and worker re-registration isolation. | A mutation where a claim locks `workers` before `nodes` would invert lock order against the assignment pass, causing deadlocks. A mutation allowing `node_id` change during active execution would corrupt reservation accounting. | Add mutant R8 ("Claim or reservation locks worker row before node row; must fail `TestPostgresRepository_NodeLock_HierarchyEnforced`") and mutant R9 ("Worker re-registration alters node_id while attempts are running; must fail `TestConformance_Worker_ReRegisterWithActiveAttemptsKeepsNodeDemand`"). |
| **F-05** | **Minor** | `docs/adr/0022-node-capability-model.md:542` | Mutant R2 paired with `TestConformance_NodeReserve_ClaimMovesDemandWithoutDoubleCount` does not test that live reservations block placement before claim. | If `reserved` ignores live reservations before claim, concurrent assignment passes can overcommit the node before the first worker claims its reservation. | Add planned test `TestConformance_NodeReserve_LiveReservationBlocksPlacement` and cite it for Mutant R2. |
| **F-06** | **Informational** | `docs/adr/0022-node-capability-model.md:36`, `docs/protocol.md:225` | `docs/protocol.md` section 4.1 documents worker registration payload key as `"worker_id"`, but code requires `"id"`. | Pre-existing documentation defect. Correctly noted in ADR-0022 line 36 and scheduled for correction in slice V2-1c. | Retain planned correction in slice V2-1c. |
| **F-07** | **Informational** | `docs/adr/0022-node-capability-model.md:331`, `docs/schema.md:8` | Current documentation defines lock order as `jobs -> workers -> attempts -> results -> job_events`. ADR-0022 amends it to `jobs -> nodes -> workers -> attempts -> results -> job_events`. | Intentional architectural modification required for V2-3 node locking. | Retain planned documentation updates in slice V2-1c. |

---

## Not Verified

1. **No planned tests or mutants were executed**: As stated in ADR-0022 Section 14, all tests in ADR-0022 are planned tests for future implementation slices (V2-1a through V2-3) and do not exist on the branch or `origin/main`. No mutants were applied.
2. **Platform probes on non-Linux kernels**: Probes for `/proc/cpuinfo`, `/proc/meminfo`, and cgroup v2 paths were evaluated by code review and specification design only; they were not executed against macOS, Windows, or varied Linux container environments.
3. **Hardware hash uniqueness under exotic CPU architectures**: The SHA-256 canonical hardware content hash mechanism was verified by algorithmic review, not by fuzzing across heterogeneous ARM/x86 topology fixtures.
4. **PostgreSQL 16 performance under high node lock contention**: The atomicity and non-blocking compatibility of `FOR KEY SHARE` and `FOR NO KEY UPDATE` were verified against PostgreSQL 16 official documentation (Chapter 13.3.2, Table 13.3) and lock theory; high-concurrency benchmarks were not executed under load.

---

## Preflight and Quality Gates (Executed)

- Preflight:
  - Worktree: `/home/blazeee/MiniCloud-gemini`
  - Test DB: `minicloud_gemini_test` (`preflight OK`)
  - Go version: `go1.22.2 linux/amd64`
- Gates:
  - `gofmt -l .`: clean
  - `go vet ./...`: rc=0
  - `go build ./...`: rc=0
  - `make test-docs`: PASS (ok `github.com/BlaZe08-Dev/Minicloud/tests/docs` 2.152s)
  - `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...`: PASS 457, SKIP 101, FAIL 0
  - `make test-db`: PASS (all database tests passed on `minicloud_gemini_test`)
  - DB commit counter verification: `minicloud_gemini_test` incremented to 12952; isolated from other worktrees.
