# Review P4-4e (Round 2 Re-Audit)

Commit reviewed: `b976a76d613618335d89cf43cdefc0137db30b04` (tip of `origin/agent/claude/P4-4e`).
Branch: `agent/gemini/P4-4e-review` merged with `origin/agent/claude/P4-4e`.
Auditor: Gemini.

---

## 1. Verdict

**Verdict: ACCEPT**

The builder (Claude Code) has addressed all findings and conditions raised during rounds 1 and 2 across three fix cycles (fix rounds 1, 2, and 3):
1. **Oversize and Hostile Output Truncation**: Handled defensively via `ClampText` in `internal/executor/truncate.go` at rune boundaries with `\n[truncated]` and replacing NUL/invalid UTF-8 bytes with `\uFFFD`, protecting against poison loops on oversized or malformed output.
2. **Worker Fallback Reporting**: Implemented in `deliverResult` (`internal/worker/client.go`), sending exactly one permanent failure report when coordinator validation rejects a result with `INVALID_REQUEST` or `OUTPUT_TOO_LARGE`. `MALFORMED_JSON` was appropriately excluded in fix round 3 to prevent transient transport corruption from marking successful computations as permanent failures.
3. **PostgreSQL SKIP LOCKED Coverage (AUD-4e-02)**: Fully guarded by `TestPostgresRepository_DispatchNextJob_SkipsLockedHead` in `internal/storage/postgres/dispatch_skiplocked_test.go`, which reliably kills the mutation where `FOR UPDATE SKIP LOCKED` is removed.
4. **Security Test Guard (AUD-4e-07)**: Hardened in `Makefile` to compare sorted test names against `tests/security-tests.list`, preventing false-green passes from test renaming or additions.
5. **Mutation Registry & Handoff Corrections**: M18 handoff correction verified (killed by `TestPostgresRepository_Sweeper_CanonicalLockOrder` under `make test-db`, F-4e-20 properly withdrawn). All 25 builder mutations and 12 auditor re-audit mutations are killed.
6. **Real-Worker Execution**: The real binaries (`coordinator` and `worker`) running against PostgreSQL completed all four edge cases without any lost attempts.

---

## 2. Mutations on the New Code

A total of 12 targeted mutations were applied one-by-one to the new code paths, tested for failure (red), restored, and proven clean (green). All 12 mutations were killed.

| # | Mutation Description | Target File | Test Command Executed | Red Result | Green Restore Result | Verdict |
|---|---|---|---|---|---|---|
| 01 | `ClampText`: remove NUL replacement | `internal/executor/truncate.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 02 | `ClampText`: measure length before replacement pass | `internal/executor/truncate.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 03 | `ClampText`: cut without rune-boundary loop | `internal/executor/truncate.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestClampText` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 04 | `executor.Execute`: remove clamp on success path | `internal/executor/executor.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestExecutor_OutputCapped` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 05 | `executor.Execute`: remove clamp on failure path | `internal/executor/executor.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/executor -run TestRegistryExecute_OutputCapOnFailure` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 06 | `worker.ExecuteAndReport`: remove output/error clamp | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./tests/abuse -run TestAbuse_OversizeOutput_ErrorPathUncapped` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 07 | `fallback`: remove `fallbackSent` guard (infinite loop) | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_FallbackAlsoRejected_NoLoop` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 08 | `fallback`: add `MALFORMED_JSON` back to `isValidationRejection` | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_MalformedJSON_NoFallback` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 09 | `fallback`: keep original output instead of empty | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 10 | `fallback`: error class not `PERMANENT` | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 11 | `fallback`: stop renewals early during delivery | `internal/worker/client.go` | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./internal/worker -run TestWorker_ResultRejected400_SendsOneFallbackFailureReport` | FAIL (rc 1) | PASS (rc 0) | KILLED |
| 12 | `PostgreSQL SKIP LOCKED` removal from `DispatchNextJob` | `internal/storage/postgres/repository.go` | `make test-db` | FAIL (rc 2) | PASS (rc 0) | KILLED |

All diffs, red execution outputs, and green restoration outputs are preserved in `docs/agents/reviews/P4-4e-round2-evidence/`.

*Note on Mutation Log Preservation*: The first Part 1 mutation run was overwritten after the mutation 12 failures were addressed; the log in `docs/agents/reviews/P4-4e-round2-evidence/part1_mutations_log.txt` reflects the second complete run.

---

## 3. Real-Worker Execution

The real binaries `bin/coordinator` and `bin/worker` were built and run under supervision against the PostgreSQL test database (`minicloud_gemini_test`) on port 18092. The built-in `echo` task was executed across four test scenarios.

| Scenario | Payload Description | Job Outcome | Attempts | Result Rows | Stored Output | Elapsed Time | Lost Attempts |
|---|---|---|---|---|---|---|---|
| (a) | 65,535-byte payload ending in 3-byte characters (`€`, mid-rune cut) | SUCCEEDED | 1 (COMPLETED) | 1 | 65,535 bytes ending with `\n[truncated]` | 0.242s | 0 |
| (b) | JSON string containing escape `\u0000` (`{"m":"a\u0000b"}`) | SUCCEEDED | 1 (COMPLETED) | 1 | 25 bytes (`{"echo":{"m":"a\u0000b"}}`) | 0.232s | 0 |
| (c) | Non-object payload producing invalid UTF-8 (`"a\xffb"`) | SUCCEEDED | 1 (COMPLETED) | 1 | 7 bytes (`"a\ufffdb"`) | 0.219s | 0 |
| (d1) | ASCII payload at exactly the 64 KiB limit (65,536 bytes) | SUCCEEDED | 1 (COMPLETED) | 1 | 65,536 bytes ending with `\n[truncated]` | 0.428s | 0 |
| (d2) | ASCII payload at one byte over limit (65,537 bytes) | Rejected (413) | 0 | 0 | `PAYLOAD_TOO_LARGE` at submission | 0.009s | 0 |

**Finding**: Exactly 0 attempts were LOST across all scenarios. Payloads within limits succeeded with correctly truncated or escaped outputs, and payload over the limit was rejected immediately at the API boundary with HTTP 413.

Evidence files: `docs/agents/reviews/P4-4e-round2-evidence/part2_real_worker_log.txt`, `docs/agents/reviews/P4-4e-round2-evidence/part2_real_worker_results.json`.

---

## 4. Conditions Re-Verified

### AUD-4e-02: PostgreSQL SKIP LOCKED Test
- **Evidence Label**: executed
- **Verification**: `TestPostgresRepository_DispatchNextJob_SkipsLockedHead` in `internal/storage/postgres/dispatch_skiplocked_test.go` locks the head job (`j_sl1`) in a separate connection and verifies `DispatchNextJob` skips it immediately to dispatch `j_sl2`. When `SKIP LOCKED` is removed (Mutation 12), the test times out after 6 seconds and fails (`mut_12_red.txt`). Restored, it passes in 0.50s (`mut_12_green.txt`). Condition satisfied.

### AUD-4e-07: Makefile Security Test Guard Compares Names
- **Evidence Label**: executed
- **Verification**: `Makefile` now extracts test names using `go test -list` and compares them against `tests/security-tests.list` with `comm`. Tested both a single rename (`aud_4e_07_rename_one.txt`) and a rename-plus-add (`aud_4e_07_rename_and_add.txt`). Both fail immediately with `FALSE-GREEN GUARD` printing the exact missing and extra test names, exiting with code 2. Condition satisfied.

### M18 Handoff Correction and F-4e-20 Withdrawal
- **Evidence Label**: evidence read and verified
- **Verification**: Builder verified that M18 (reversing sweeper lock order to take attempts lock before jobs lock) is killed by `TestPostgresRepository_Sweeper_CanonicalLockOrder` under `make test-db` (`fix-D-m18-test-db-FAIL.txt`), failing three subtests. The original survivor claim arose because the mutation driver only ran security suites, not `make test-db`. F-4e-20 is formally withdrawn. Total builder mutation count is 25 executed, 25 killed, 0 survived.

### AUD-4e-06: Raw NUL Generation in Built-in Tasks
- **Evidence Label**: confirmed by code reading
- **Verification**: Evaluated all three allowlisted tasks in `internal/executor`:
  - `EchoTask` (`internal/executor/echo.go`): `Validate` requires `json.Valid(payload)`, which rejects unescaped NUL bytes (< 0x20) in JSON strings per RFC 8259. When payload is an object, `json.Marshal` serializes NUL as `\u0000`. When payload is not an object, it cannot contain raw NUL because `Validate` already rejected it.
  - `SleepTask` (`internal/executor/sleep.go`): Produces structured output containing numbers and JSON delimiters (`{"slept_ms": ...}`).
  - `SHA256LoopTask` (`internal/executor/sha256_loop.go`): Produces hex-encoded digests and numbers.
  None of the built-in tasks can produce raw NUL bytes. `ClampText` in `internal/executor/truncate.go` replaces any raw NUL with `\uFFFD`, ensuring defense-in-depth for future or third-party tasks.

---

## 5. Fallback Design Review

Comprehensive design analysis of the worker fallback mechanism in `internal/worker/client.go`:

1. **Can fallback ever replace an accepted result?**
   - **No**: In `deliverResult`, the fallback block is only reachable if `reportResult` returned an error and `isValidationRejection(status, code)` evaluates to true. If the coordinator accepts the result, `reportErr == nil`, and the function returns immediately.
2. **Can it send more than one report?**
   - **No**: Guarded by `fallbackSent` boolean in `deliverResult`. The flag is set to `true` prior to issuing the fallback request. Any subsequent rejection bypasses the fallback block and enters `isTerminalDelivery`. Guarded by `TestWorker_ResultRejected400_FallbackAlsoRejected_NoLoop`.
3. **Does the lease keep renewing until delivery, and what happens if the lease is already lost?**
   - **Yes**: In `ExecuteAndReport`, the renewal goroutine runs under `renewCtx` with deferred `stopRenew()` and `wg.Wait()`, keeping renewals active while `deliverResult` retries. If the lease is marked lost (`run.isLost()`), `deliverResult` immediately drops the report and returns without making further network calls.
4. **What does a second 400 do?**
   - If the fallback report itself receives an HTTP 400 rejection, `isTerminalDelivery(status)` evaluates to true, logging a warning and returning cleanly without retrying or looping.
5. **Is the error text clamped?**
   - **Yes**: In `deliverResult`, the fallback error string is clamped: `errSummary = executor.ClampText(fmt.Sprintf(...), executor.MaxErrorSizeBytes)`.
6. **Can a SUCCEEDED computation turn into FAILED via transient fault?**
   - **No**: `isValidationRejection` only matches deterministic validation codes: `INVALID_REQUEST` and `OUTPUT_TOO_LARGE`. `MALFORMED_JSON` was explicitly removed in fix round 3 because transport corruption can cause a coordinator to fail to parse a well-formed report body. On `MALFORMED_JSON`, no fallback is sent; the result is dropped, the lease expires, and the job is retried safely.
7. **Idempotency and Size Bounds of `ClampText`**:
   - A scratch test (`scratch_clamp_test.go`) was written and executed outside the committed tree. It evaluated 500 adversarial iterations with random lengths, multibyte runes, raw invalid bytes, and NULs across 8 limits.
   - Verified that `ClampText(ClampText(s, limit), limit) == ClampText(s, limit)` in all cases.
   - Verified that length is strictly `<= limit` (when limit >= length of marker).
   - Verified that JSON round-tripping preserves exact byte length.
   - The test passed cleanly in 29.30s (`clamptext_scratch_test.txt`) and was deleted immediately.

---

## 6. Independent Runs

All quality gates and test suites were executed independently from a clean git working tree:

| Gate | Command Executed | Result | Reference Evidence File |
|---|---|---|---|
| Formatting | `gofmt -l .` | Clean (rc 0) | `gate_gofmt.txt` |
| Static Analysis | `go vet ./...` | Clean (rc 0) | `gate_govet.txt` |
| Documentation Citations | `make test-docs` | PASS (rc 0) | `gate_test_docs.txt` |
| Unit Suite (No DB) | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | PASS 248, SKIP 48, FAIL 0 (rc 0) | `gate_unset_v.txt`, `gate_unset_counts.txt` |
| Database Suite | `make test-db` | PASS 81 top-level (rc 0) | `gate_test_db.txt` |
| Security Suite (Run 1) | `make test-security` | PASS 53, SKIP 1, FAIL 0 (rc 0) | `gate_test_security_run1.txt` |
| Security Suite (Run 2) | `make test-security` | PASS 53, SKIP 1, FAIL 0 (rc 0) | `gate_test_security_run2.txt` |

The counts match the builder's reported numbers exactly (248/48/0 unit, 81 database, 53/1/0 security run twice identically).

---

## 7. Round 2 Gap Disclosures and Assessments

1. **Detailed Assessments of FS-06, FS-07, and FS-09**:
   - **FS-06 (Crash around SQL COMMIT)**: No failpoint hook exists in production code to crash precisely between SQL statement execution and commit completion. The executed test `TestFault_CoordinatorKill9_DuringCommitStorm_InvariantsAfterRestart` approximates this by killing the coordinator under heavy concurrent commit load, and `TestFault_AmbiguousCommit_ReplayIsSafe` covers post-commit network failure replay. This satisfies V1 requirements without modifying production code.
   - **FS-07 (Database Outage and Recovery)**: Handled via the TCP fault proxy in `TestFault_DBOutage_ProxyModes` (testing connection drop, reset, and delay) and `TestFault_DBOutage_HalfOpen`. This verifies application-level retry backoff, request deadlines (8s), and the outage grace window. The physical Docker container restart test (`make test-outage`) remains reserved for the orchestrator at the V1 milestone boundary.
   - **FS-09 (Timeout, Huge Output, Poison Repeat)**: Fully covered for V1 limits: execution timeout is enforced via context deadline, output and error limits are enforced via `ClampText`, and poison repeated validation failures are terminated via the single fallback failure report. High-throughput 10 MiB benchmarks are deferred to V2 per ADR-0004.
2. **Script Re-creation Disclosure**:
   - In review rounds 1 and 2, certain ad-hoc verification scripts were executed interactively and reconstructed into evidence directories retroactively. Note that scripts deleted during round 1 cleanup were recreated for re-audit and evidence preservation. In round 3, all runner scripts (`run_part1_mutations.py`, `run_part2_real_worker.py`, `query_db.go.txt`, `test_aud_4e_07.sh`, `run_independent_gates.sh`) were authored and saved directly in `docs/agents/reviews/P4-4e-round2-evidence/scripts/` prior to execution.
3. **Docker Execution Rule Disclosure**:
   - In review round 1, `docker exec` was invoked against the shared container `minicloud-postgres` to inspect tables and execute queries, violating the standing rule against running docker commands. In this audit round, zero docker commands were executed; all database interactions were conducted solely via PostgreSQL clients connecting to `minicloud_gemini_test`.

---

## 8. Findings (AUD-4e-08 onward)

### AUD-4e-08: Transient Database Connection Exhaustion on Back-to-Back Test Suites
- **Evidence Label**: executed
- **Severity**: Informational
- **Description**: When executing heavy PostgreSQL test suites (such as `make test-db`) consecutively with no delay, `pgxpool` connections from concurrent stress subtests (such as `TestPostgresRepository_Concurrency_SameJobDispatchRaced`) may take up to 1-2 seconds to close asynchronously. This can briefly surface as `server error: FATAL: sorry, too many clients already (SQLSTATE 53300)` on the very first subtest of the subsequent run.
- **Suggested Fix**: Adding a 2-second settle pause between full test suite runs resolves the transient connection spikes.

---

## 9. Items Not Verified

1. **Physical Container Outage (`make test-outage`)**: Intentionally skipped per standing safety rules to protect the shared container environment.
2. **V2/V3 Features**: Redis transport dispatch, Docker task isolation, worker mutual TLS, and multi-machine network latency remain out of scope for V1.
3. **F-4e-18 Terminal Escape Sequence Rendering**: Verified bytewise in memory; actual visual rendering in specific terminal emulators was not tested.
