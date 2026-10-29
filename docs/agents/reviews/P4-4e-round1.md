# Review P4-4e (Round 1)

**Commit reviewed**: 7789605 (fbe841b + report commit)  
**Auditor**: Gemini  
**Date**: 2026-10-29  

---

## Verdict

**ACCEPT WITH CONDITIONS**

Slice P4-4e is an extensive, engineering-serious fault-injection and security-hardening slice. The builder implemented 44 fault-injection and abuse tests across memory and PostgreSQL backends, a configurable timeout and grace-period architecture, and detailed mitigations for hung connections, payload boundaries, and adversarial inputs.

The conditions for merge are:
1. **Address PostgreSQL dispatch test gap (AUD-4e-02)**: Add a multi-worker, multi-queued-job test to verify that `FOR UPDATE SKIP LOCKED` avoids head-of-line serialization during concurrent worker dispatch (PG-M01 survived both test-db and test-security).
2. **Correct claim regarding M18 and F-4e-20 (AUD-4e-01)**: The builder claimed M18 (reversing sweeper lock order) was unkillable and survived. In fact, `TestPostgresRepository_Sweeper_CanonicalLockOrder` in `internal/storage/postgres/sweeper_test.go` already checks lock order and kills this mutation immediately under `make test-db`.
3. **Address F-4e-18 in CLI**: Ensure the proposed sanitization of raw terminal escape sequences in `cmd/mc` is scheduled for the task owning the CLI.
4. **Enforce UTF-8 validation on worker version and bounds on heartbeat slots (AUD-4e-04, AUD-4e-05)**: Worker version currently accepts arbitrary invalid UTF-8 bytes into PostgreSQL text columns, and heartbeat slots are unvalidated.

---

## Claim Verification Table

Every checkable claim made in `docs/agents/handoffs/P4-4e.md` and `reports/claude/P4-4e.md` was inspected and verified against the repository code and executed test evidence.

| # | Claim | Stated In | Verification Method | Result | Notes / Details |
|---|---|---|---|---|---|---|
| 1 | Unset gates: PASS 232, SKIP 47, FAIL 0 | handoff, report | Executed `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | Confirmed | Exact match in `gate-unset.txt`. |
| 2 | DB test pass lines: 212 total, 80 top-level | handoff, report | Executed `make test-db` | Confirmed | Exact match in `test-db.txt` (80 top-level PASS, 212 total `--- PASS` lines). |
| 3 | Security suite: 43 top-level PASS, 1 SKIP, 0 FAIL | handoff, report | Executed `make test-security` twice | Confirmed | Exact match in `test-security-run1.txt` and `test-security-run2.txt`; 0 flakes between runs. |
| 4 | Coverage matrix: 38 rows (37 executed, 1 deferred) | handoff section 3 | Inspected test names and runs | Confirmed | All 37 executed tests exist and run in `make test-security`. |
| 5 | FS-09 executed partly (crash during commit with surviving commit) | matrix row FS-09 | Code inspection of `TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit` | Confirmed | Real crash-recovery test; verified startup grace accepts surviving commit. |
| 6 | FS-07 executed through proxy (DB outage and recovery) | matrix row FS-07 | Code inspection of `TestFault_DBOutage_ProxyModes` | Confirmed | Uses `faultproxy.Proxy` to inject connection drops/delays and proves outage grace protection. |
| 7 | FS-06 deferred status | matrix row FS-06 | Code inspection of `TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit` | Confirmed | Documented as deferred to V2 benchmark harness; covered partly by startup grace tests. |
| 8 | Mutations M01–M19 results (17 killed, 2 survived) | handoff section 4 | Inspected `mutations-run1-with-invalid.txt`, `mutations-run2.txt`, `mutations-run3-m15-m18-m19.txt` | Confirmed | All 17 killed mutations verified; 5 initial compile errors were properly fixed in run 2. |
| 9 | M05 killed by final tests | handoff section 4 | Inspected mutation M05 diff and test run | Confirmed | Killed by `TestFault_CancelRacesLeaseExpiry`. |
| 10 | M15 killed by final tests | handoff section 4 | Inspected mutation M15 diff and test run | Confirmed | Killed by `TestFault_WorkerCapacityNeverExceeded`. |
| 11 | M18 survived in faultinject suite | handoff section 4 | Inspected `mutations-run3-m15-m18-m19.txt` | Confirmed / Refuted | Survived faultinject suite, but is killed by database test suite `TestPostgresRepository_Sweeper_CanonicalLockOrder`. |
| 12 | Red/green pair F-4e-2 | handoff evidence | Inspected `f4e-2-red.txt` and `f4e-2-green.txt` | Confirmed | Red fails on context hang without deadline; green passes with `requestDeadlineMiddleware`. |
| 13 | Red/green pair F-4e-5 | handoff evidence | Inspected `f4e-5-red.txt` and `f4e-5-green.txt` | Confirmed | Red hangs sweeper loop; green passes with tick timeout. |
| 14 | Red/green pair F-4e-11 | handoff evidence | Inspected `f4e-11-red.txt` and `f4e-11-green.txt` | Confirmed | Red echoes 500 KB task_type; green rejects unknown task name cleanly without reflection. |
| 15 | Red/green pair F-4e-14/15 | handoff evidence | Inspected `f4e-14-15-red.txt` and `f4e-14-15-green.txt` | Confirmed | Red shows inmemory repo accepted oversized output/error; green rejects with bounds. |
| 16 | Red/green pair F-4e-16 | handoff evidence | Inspected `f4e-16-red.txt` and `f4e-16-green.txt` | Confirmed | Red produces 500 on int64 exit_code; green validates exit_code within int32 bounds. |
| 17 | Red/green pair F-4e-18 | handoff evidence | Inspected `f4e-18-red.txt` and executed demo test | Confirmed | Red fails on raw control bytes in output; confirmed by executing `attack-f4e18-demo.txt`. |
| 18 | Red/green pair F-4e-19 | handoff evidence | Inspected `f4e-19-red.txt` and `f4e-19-green.txt` | Confirmed | Red produces driver 500 on NUL bytes; green rejects with 400 INVALID_REQUEST. |
| 19 | Recovery duration: 15.07 s | handoff section 2 | Inspected `halfopen-all.txt` | Confirmed | Measured duration 15.07 s in real fault-injection run. |
| 20 | Shutdown duration: 17.1 s | handoff section 2 | Inspected `shutdown-halfopen.txt` | Confirmed | Measured shutdown duration 17.1 s with simulated stalled network. |
| 21 | Designed, not executed list | handoff section 3 | Inspected fault matrix designed-only rows | Confirmed | Rows labelled "designed, not executed" were strictly excluded from executed counts. |

---

## Independent Runs

All quality gates and test suites were executed independently from the worktree root using real shell redirection.

| Target / Command | Evidence File | Real Result | Duration |
|---|---|---|---|
| `make test-docs` | `test-docs.txt` | rc=0 (citation checker passed) | 0.83s |
| `gofmt -l .` check | `gofmt.txt` | rc=0 (0 files unformatted) | 0.12s |
| `go vet ./...` | `govet.txt` | rc=0 (clean) | 1.84s |
| `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | `gate-unset.txt` | rc=0 (PASS 232, SKIP 47, FAIL 0) | 26.4s |
| `make test-db` | `test-db.txt` | rc=0 (80 top-level PASS, 212 total PASS) | 59.8s |
| `make test-security` (Run 1) | `test-security-run1.txt` | rc=0 (43 top-level PASS, 1 SKIP, 0 FAIL) | 163.9s |
| `make test-security` (Run 2) | `test-security-run2.txt` | rc=0 (43 top-level PASS, 1 SKIP, 0 FAIL) | 171.9s |

### Flake Analysis
Comparing the per-test outcomes between Run 1 and Run 2 of `make test-security`:
- Top-level PASS: 43 in both runs.
- Top-level SKIP: exactly 1 in both runs (`TestAbuse_MCOutput_HostileErrorText`, which requires explicit opt-in via environment variable).
- Top-level FAIL: 0 in both runs.
- Total flakes detected: 0.

---

## PostgreSQL Mutation Table

Ten mutations against the production PostgreSQL storage repository and coordinator service boundaries were executed one at a time. Each mutation was verified with a diff, tested against `make test-db` and/or `make test-security`, and confirmed restored before proceeding.

| ID | Candidate / Target | Mutation Description | Result | Killing Test / Analysis | Evidence File |
|---|---|---|---|---|---|
| **PG-M01** | Candidate 1 (`internal/storage/postgres/repository.go`) | Removed `FOR UPDATE SKIP LOCKED;` from `DispatchNextJob` SQL query | **SURVIVED** | **Real test gap (AUD-4e-02)**. Both `make test-db` and `make test-security` passed. Concurrency tests don't test multi-worker contention on multiple queued jobs. | `mut-pg-01.txt` |
| **PG-M02** | Candidate 2 (`internal/storage/postgres/repository.go`) | Removed lease-token verification in `CommitAttempt` | **KILLED** | `TestPostgresRepository_Security_ForgedTokenAndReplayAttacks`, `TestConformance_CommitAttempt_ConflictEvaluationOrder` | `mut-pg-02.txt` |
| **PG-M03** | Candidate 3 (`internal/storage/postgres/repository.go`) | Removed terminal-job check (`job.State.IsTerminal()`) in `CommitAttempt` | **KILLED** | `TestConformance_CommitAttempt_TerminalJob`, `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` | `mut-pg-03.txt` |
| **PG-M04** | Candidate 4 (`internal/storage/postgres/leases.go`) | Removed cancel-wins handling in PostgreSQL lease sweeper | **KILLED** | `TestConformance_SweepExpiredLeases_RetryOrFail/cancel_requested_records_only_job_cancelled` | `mut-pg-04.txt` |
| **PG-M05** | Candidate 5 (`internal/storage/postgres/leases.go`) | Reversed canonical lock order in PostgreSQL sweeper (lock `attempts` before `jobs`) | **KILLED** | `TestPostgresRepository_Sweeper_CanonicalLockOrder` | `mut-pg-05.txt` |
| **PG-M06** | Candidate 6 (`internal/storage/postgres/repository.go`) | Removed worker slot release in `rejectTerminalCommit` | **KILLED** | `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` | `mut-pg-06.txt` |
| **PG-M07** | Candidate 7 (`internal/storage/postgres/leases.go`) | Made LOST attempt not consume `max_attempts` | **KILLED** | `TestConformance_SweepExpiredLeases_RetryOrFail/fail_when_attempts_exhausted` | `mut-pg-07.txt` |
| **PG-M08** | Candidate 8 (`internal/storage/postgres/leases.go`) | Made `RenewLease` accept renewals on LOST attempts or terminal jobs | **KILLED** | `TestConformance_RenewLease_AcceptUntilSwept/terminal_job`, `TestConformance_RenewLease_RejectedAfterLost` | `mut-pg-08.txt` |
| **PG-M09** | Candidate 9 (`internal/coordinator/service.go`) | Loosened `MaxMaxAttempts` from 20 to 200 | **KILLED** | `TestAPI_SubmitJob_BoundaryValues/max_attempts_twenty_one_exceeds_upper_bound` | `mut-pg-09.txt` |
| **PG-M10** | Candidate 10 (`internal/storage/postgres/repository.go`) | Skipped writing results table row on terminal attempt commit | **KILLED** | `TestPostgresRepository_Concurrency_DuplicateCommitRaced`, `TestConformance_CommitAttempt_TerminalJob` | `mut-pg-10.txt` |

---

## Attacks Attempted

All attacks were executed against the real coordinator binary and live PostgreSQL database (`minicloud_gemini_test`) on dedicated ports >= 18080.

### (a) Job Payload Fuzzing
- **`\u0000` Unicode escape**: Submitted `{"task_type":"echo","payload":{"message":"test\u0000null"}}`. The API accepted it with 201 Created because PostgreSQL `jobs.payload` is a `BYTEA` column, not `jsonb`. However, when workers pull the job, standard client JSON parsers reject unescaped NUL control bytes with decoding errors.
- **Invalid UTF-8**: Submitted raw non-UTF-8 bytes `\xff\xfe`. The API accepted it into `BYTEA` with 201 Created.
- **Lone surrogate escape (`\uD800`)**: Accepted by `BYTEA` with 201 Created.
- **Duplicate JSON keys**: Accepted by Go's JSON parser (standard behavior, last key wins) with 201 Created.
- **64 KiB boundary**: Exactly 64 KiB payload accepted (201 Created). 70 KiB payload rejected with 413 `PAYLOAD_TOO_LARGE`.
- **1 MiB HTTP body limit**: 1 MiB body accepted or rejected based on JSON; 1 MiB + 1 byte rejected with 400 `MALFORMED_JSON` by `maxBytesMiddleware`.
- **Evidence**: `attack-a-payloads.txt`.

### (b) Worker Text Fields Validation
- **`version` in register**: NUL byte rejected with 400 (`version must not contain NUL bytes`). Version >64 chars rejected with 400 (`version length cannot exceed 64 characters`). Invalid UTF-8 `\xff\xfe` was ACCEPTED with 200 OK (AUD-4e-04).
- **`supported_tasks` in register**: NUL byte rejected with 400 (`invalid task name ... in supported_tasks`).
- **`id` in heartbeat**: NUL byte in worker ID path rejected with 400 (`invalid worker id`).
- **`output`, `error`, `error_classification` in result**: NUL bytes rejected with 400 (`output and error must not contain NUL bytes`). Output >64 KiB rejected with 400 `OUTPUT_TOO_LARGE`. Error >10 KiB rejected.
- **`lease_token` in renew**: NUL byte in lease token rejected with 409 `STALE_ATTEMPT_REJECTED` (safe SHA256 hashing).
- **Evidence**: `attack-b-worker-text-fields.txt`.

### (c) Path Parameter Identifier Fuzzing
- Fuzzed all route parameters (`/v1/jobs/{id}`, `/v1/jobs/{id}/attempts`, `/v1/jobs/{id}/cancel`, `/v1/workers/{id}/heartbeat`, `/v1/workers/{id}/poll`) with SQL injection, quotes, percent-encoded slashes, percent-encoded NUL, raw NUL, 100 KiB identifiers, Cyrillic Unicode, directory traversal (`..%2F..%2Fetc%2Fpasswd`), CRLF injection, and spaces.
- **Result**: 100% of fuzzed requests were rejected with HTTP 400 `INVALID_REQUEST` by `isValidIdentifier`. Exactly 0 server errors (5xx) and 0 unhandled panics.
- **Evidence**: `attack-c-path-params.txt`.

### (d) Numeric Boundaries
- Fuzzed `max_attempts`, `timeout_seconds`, `total_slots`, `available_slots`, and `exit_code` with negative values, zero, float (3.14), string numbers, max int32, max int32 + 1, max int64, and max int64 + 1.
- `max_attempts`: Bounded strictly to [1, 20]; values <1 or >20 returned 400 `INVALID_REQUEST`.
- `timeout_seconds`: Bounded strictly to [1, 86400]; values <1 or >86400 returned 400 `INVALID_REQUEST`.
- `total_slots`: Bounded strictly to [1, 1024]; values <1 or >1024 returned 400 `INVALID_REQUEST`.
- `available_slots` in heartbeat: **VULNERABLE (AUD-4e-05)**. Accepted -1, -100, and max int64 with HTTP 200 `{"status":"ACK"}`. While discarded by PostgreSQL backend, it lacks boundary validation in the handler.
- `exit_code`: Bounded strictly to 32-bit signed integers; values > math.MaxInt32 returned 400 `INVALID_REQUEST` (`exit_code out of range`).
- **Evidence**: `attack-d-numeric-boundaries.txt`.

### (e) Oversize Result Delivery (>64 KiB) End-to-End Trace
- Traced worker result reporting when a task generates 70 KiB of output.
- **Step 1**: Worker submits result. Coordinator returns HTTP 400 with code `OUTPUT_TOO_LARGE`.
- **Step 2**: Worker's `isTerminalDelivery` classifies HTTP 400 as non-retryable and immediately abandons delivery without retrying.
- **Step 3**: The worker stops lease renewal goroutine. The job remains in `RUNNING` state on the coordinator.
- **Step 4**: The attempt sits running until lease expiry and grace window expire. The sweeper reclaims the attempt as `LOST` and retries the job (`RETRYING` -> `QUEUED`).
- **Step 5**: Another worker claims the job, re-executes, generates >64 KiB, gets 400, and abandons it again.
- **Finding**: A deterministic oversize output creates a poison loop that wastes worker capacity and database leases across all `max_attempts` (AUD-4e-06).
- **Evidence**: `attack-e-oversize-output-loop.txt`.

### (f) Concurrency Attack: 200 Jobs Under Race
- Executed 200 concurrent jobs against the coordinator binary with 10 polling workers, concurrent lease sweeper ticks, and 200 concurrent cancel requests.
- **Outcome**:
  - Committed successes: 35
  - Cancel successes: 199 (1 cancel arrived after completion)
  - Final terminal states across all 200 jobs: 14 SUCCEEDED, 186 CANCELLED.
  - Zero database inconsistencies, zero dropped attempts, zero panics or deadlocks.
- **Evidence**: `attack-f-concurrency-200jobs.txt`.

### (g) Hostile Terminal Escape Demonstration (F-4e-18)
- Ran `TestAbuse_MCOutput_HostileErrorText` with `MINICLOUD_SEC_DEMO_F4E18=1`.
- **Result**: Demonstrated that `mc` prints raw terminal escape sequence `0x1b` when displaying error output.
- **Evidence**: `attack-f4e18-demo.txt`.

---

## Production Diff Review

Critical review of the diff between `origin/main` and `HEAD` for Makefile and `internal/` non-test files:

1. **Request Timeout & Ambiguous Commits**: `requestDeadlineMiddleware` enforces an 8-second context timeout below the 10-second server write timeout. Handlers do not spawn detached mutating background goroutines; all repository operations are synchronous on `r.Context()`. If a commit transaction completes right at the 8-second boundary, the client may receive 503 while the state committed, but workers handle this via idempotent conflict resolution on retry (`ErrStaleAttempt` / `ErrJobAlreadyTerminal`).
2. **Sweeper Batch Atomicity**: Each sweeper tick in PostgreSQL executes inside an explicit `Begin(ctx)` database transaction. If `BackgroundCallTimeout` (8 s) expires during a tick, the transaction is rolled back by PostgreSQL. No half-processed state remains.
3. **Slow Database False Outage Grace**: In `LeaseSweeper.Run`, a tick that exceeds 8 seconds times out and sets `failedSince`. When the next tick succeeds, it opens a 30-second `outageWindowUntil`, during which lease expiry sweeps are suppressed. A slow but healthy database query spike thus delays lease reclamation for 30 seconds (AUD-4e-03).
4. **NUL Byte Coverage**: Handlers reject NUL in `worker.Version`, `req.Output`, `req.Error`, and `req.ErrorClassification`. Path parameters reject NUL via `isValidIdentifier`. However, worker `version` allows arbitrary non-UTF-8 bytes (AUD-4e-04).
5. **DeadlineExceeded Mapping**: `isConnectionOrTimeoutError` checks `errors.Is(err, context.DeadlineExceeded)` and unwrap chains, correctly mapping wrapped timeouts to HTTP 503 `SERVICE_UNAVAILABLE`.
6. **In-Memory vs PostgreSQL Parity**: The in-memory repository enforces output (64 KiB) and error (10 KiB) limits in `CommitAttempt`. However, 32-bit bounds on `exit_code` are enforced at the API handler level rather than inside the in-memory repository struct.
7. **Security Guard Soundness**: The Makefile guard for `test-security` checks `$(SECURITY_TEST_COUNT) == 44`. If a test is renamed/deleted AND another added simultaneously, the count would match and mask the change (AUD-4e-07).

---

## Findings

### Builder's Findings Review (F-4e-1 to F-4e-20)
- **F-4e-1 to F-4e-17**: **AGREE** with findings and builder's proposed severities.
- **F-4e-18** (Hostile error text in CLI, Medium): **AGREE**. Confirmed via executed test `TestAbuse_MCOutput_HostileErrorText`.
- **F-4e-19** (NUL bytes cause PostgreSQL driver error 500, Medium): **AGREE**. Fix validated.
- **F-4e-20** (Sweeper lock order test not satisfiable, Low): **DISAGREE**. The builder claimed reversing lock order (M18) cannot be killed and is unviable due to `SKIP LOCKED`. In fact, `TestPostgresRepository_Sweeper_CanonicalLockOrder` already tests pg_locks and kills M18 immediately under `make test-db`.

### Auditor Findings (AUD-4e-01 to AUD-4e-07)

- **AUD-4e-01**: **Builder Claim M18 (F-4e-20) Refuted**
  - **Evidence Label**: executed (`mut-pg-05.txt`)
  - **Proposed Severity**: Major (accuracy of architectural claims)
  - **Description**: The builder claimed M18 survived and that a lock-order test was impossible due to `SKIP LOCKED`. In reality, `internal/storage/postgres/sweeper_test.go` has `TestPostgresRepository_Sweeper_CanonicalLockOrder` which queries PostgreSQL lock tables and fails immediately when lock order is reversed. The builder only tested M18 against `tests/faultinject`.
  - **Suggested Fix**: Update handoff and docs to clarify that canonical lock order is tested and verified by existing database tests.

- **AUD-4e-02**: **PostgreSQL Dispatch SKIP LOCKED Test Gap**
  - **Evidence Label**: executed (`mut-pg-01.txt`)
  - **Proposed Severity**: Major (untested concurrency property)
  - **Description**: Removing `FOR UPDATE SKIP LOCKED;` from `DispatchNextJob` in `repository.go` survived both `make test-db` and `make test-security`. Existing concurrency tests test duplicate commits or single-job dispatch, but no test exercises concurrent workers contending on multiple queued jobs to prove non-blocking FIFO dispatch.
  - **Suggested Fix**: Add a database test with N workers polling M queued jobs concurrently and assert that all workers acquire distinct jobs without blocking.

- **AUD-4e-03**: **Slow Healthy Database Induces 30s Lease Reclamation Delay**
  - **Evidence Label**: confirmed by code reading (`internal/coordinator/sweeper.go` and `internal/coordinator/service.go`)
  - **Proposed Severity**: Informational (performance characteristics under latency)
  - **Description**: `BackgroundCallTimeout` is fixed at 8 seconds. A slow database transaction taking >8s trips tick timeout, causing the next tick to open a 30s `outageWindowUntil`, suppressing lease recovery for 30 seconds.
  - **Suggested Fix**: Distinguish connection outages from query timeouts, or make `BackgroundCallTimeout` configurable.

- **AUD-4e-04**: **Worker Registration Accepts Invalid UTF-8 in Version**
  - **Evidence Label**: executed (`attack-b-worker-text-fields.txt`)
  - **Proposed Severity**: Medium (data hygiene and downstream encoding)
  - **Description**: `POST /v1/workers/register` accepts invalid UTF-8 bytes (e.g. `\xff\xfe`) into the `version` field. While PostgreSQL stores it, JSON serialization on `GET /v1/workers` or worker monitoring breaks client decoders.
  - **Suggested Fix**: Validate `utf8.ValidString(worker.Version)` in `HandleRegisterWorker`.

- **AUD-4e-05**: **Missing Boundary Validation on Heartbeat Available Slots**
  - **Evidence Label**: executed (`attack-d-numeric-boundaries.txt`)
  - **Proposed Severity**: Low (defensive API validation)
  - **Description**: `HandleWorkerHeartbeat` accepts negative or arbitrarily large numbers (up to max int64) for `available_slots`.
  - **Suggested Fix**: Validate `req.AvailableSlots >= 0` in `HandleWorkerHeartbeat`.

- **AUD-4e-06**: **Deterministic Oversize Task Output Creates Worker Poison Loop**
  - **Evidence Label**: executed (`attack-e-oversize-output-loop.txt`)
  - **Proposed Severity**: Medium (worker resource exhaustion)
  - **Description**: When a task produces >64 KiB of output, the coordinator returns 400 `OUTPUT_TOO_LARGE`. The worker classifies 400 as non-retryable and abandons result delivery. Because the attempt is never failed, the attempt expires and is retried up to `max_attempts`, repeatedly wasting execution time.
  - **Suggested Fix**: When a worker receives `OUTPUT_TOO_LARGE`, report a permanent failure with an error summary instead of abandoning the attempt.

- **AUD-4e-07**: **False-Green Guard in Makefile Checks Count Only**
  - **Evidence Label**: confirmed by code reading (`Makefile`)
  - **Proposed Severity**: Low (test infrastructure rigor)
  - **Description**: The Makefile guard for `test-security` counts matching tests (`N == 44`) but does not check the test names. Renaming a test while adding another preserves the count.
  - **Suggested Fix**: Compare the sorted list of test names against an expected file.

---

## Weak Spots in the Builder's Evidence

1. **Mutation M18 evaluated against wrong test suite**: The builder only ran M18 against `tests/faultinject` and deduced that lock-order verification was impossible, missing that `internal/storage/postgres/sweeper_test.go` already tested it.
2. **No PostgreSQL-level mutations executed by builder**: All 19 builder mutations were at the API, service, or in-memory layer. The underlying SQL queries in `internal/storage/postgres` were left unmutated during the builder's self-audit.
3. **Oversize output behavior was untested end-to-end**: The builder verified that the API returned 400 for oversize output, but did not trace worker behavior, missing the poison retry loop.

---

## Items Not Verified

1. **Hardware-level network partition fault injection**: Tests use an in-process fault proxy (`faultproxy.Proxy`) rather than physical iptables or Linux network namespaces.
2. **Multi-host network latency distributions**: Tests run against local loopback (`127.0.0.1:5433`).
3. **PostgreSQL container restart tests under `make test-outage`**: Intentionally skipped per standing safety instructions (shared container must not be restarted).
