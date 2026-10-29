# Review P4-4e (Round 1 & Round 2 Revision)

**Commit reviewed**: 7789605 (fbe841b + report commit)  
**Auditor**: Gemini  
**Date**: 2026-10-29  

---

## Verdict

**ACCEPT WITH CONDITIONS**

Slice P4-4e is an extensive, engineering-serious fault-injection and security-hardening slice. The builder implemented 44 fault-injection and abuse tests across memory and PostgreSQL backends, a configurable timeout and grace-period architecture, and detailed mitigations for hung connections, payload boundaries, and adversarial inputs.

The conditions for merge are:
1. **Address PostgreSQL dispatch test gap (AUD-4e-02)**: Add a test with multiple concurrent workers contending on multiple queued jobs to prove non-blocking FIFO dispatch with `FOR UPDATE SKIP LOCKED`.
2. **Correct claim regarding M18 and withdraw F-4e-20 (AUD-4e-01)**: Correct the handoff claim that reversing sweeper lock order survived and was impossible to test. Acknowledge that `TestPostgresRepository_Sweeper_CanonicalLockOrder` in `internal/storage/postgres/sweeper_test.go` already checks lock order and kills this mutation under `make test-db`, and withdraw finding F-4e-20.
3. **Schedule CLI fix for F-4e-18**: Ensure raw terminal escape sequence sanitization in `cmd/mc` is scheduled for the task owning the CLI post-merge.
4. **Strengthen false-green guard in Makefile (AUD-4e-07)**: Update the Makefile guard for `test-security` to compare sorted test names against an expected list rather than checking only the test count.
5. **Verify oversize output worker trace (AUD-4e-06)**: Builder to trace result reporting with the real worker when output exceeds 64 KiB, and demonstrate recovery/exhaustion behavior to be verified in the re-audit.

Conditions removed from round 1:
- AUD-4e-04 (worker version UTF-8 validation): Removed. Re-execution of attack (b) confirmed that Go's standard library JSON decoder safely replaces raw invalid UTF-8 bytes with Unicode replacement characters U+FFFD (`\ufffd`), resulting in valid UTF-8 text stored in PostgreSQL and decoded cleanly by downstream API clients.
- AUD-4e-05 (heartbeat available_slots validation): Removed. Code inspection confirms both PostgreSQL and in-memory backends deliberately discard the parameter (`_ int`) because worker capacity is computed dynamically from active attempts per ADR-0011.

---

## Claim Verification Table

Every checkable claim made in `docs/agents/handoffs/P4-4e.md` and `reports/claude/P4-4e.md` was inspected and verified against the repository code and executed test evidence.

| # | Claim | Stated In | Verification Method | Result | Notes / Details |
|---|---|---|---|---|---|
| 1 | Unset gates: PASS 232, SKIP 47, FAIL 0 | handoff, report | Executed `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | Confirmed | Exact match in `gate-unset.txt`. |
| 2 | DB test pass lines: 212 total, 80 top-level | handoff, report | Executed `make test-db` | Confirmed | Exact match in `test-db.txt` (80 top-level PASS, 212 total `--- PASS` lines). |
| 3 | Security suite: 43 top-level PASS, 1 SKIP, 0 FAIL | handoff, report | Executed `make test-security` twice | Confirmed | Exact match in `test-security-run1.txt` and `test-security-run2.txt`; 0 flakes between runs. |
| 4 | Coverage matrix: 38 rows (37 executed, 1 deferred) | handoff section 3 | Inspected test names and runs | Confirmed | All 37 executed tests exist and run in `make test-security`. |
| 5 | FS-09 executed partly (crash during commit with surviving commit) | matrix row FS-09 | Code inspection of `TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit` | Confirmed | Real crash-recovery test; verified startup grace accepts surviving commit. |
| 6 | FS-07 executed through proxy (DB outage and recovery) | matrix row FS-07 | Code inspection of `TestFault_DBOutage_ProxyModes` | Confirmed | Uses `faultproxy.Proxy` to inject connection drops/delays and proves outage grace protection. |
| 7 | FS-06 deferred status | matrix row FS-06 | Code inspection of `TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit` | Confirmed | Documented as deferred to V2 benchmark harness; covered partly by startup grace tests. |
| 8 | Builder mutations M01–M19 (18 killed, 1 survived) | handoff section 4 | Read from evidence files `mutations-run2.txt` and `mutations-run3-m15-m18-m19.txt` | Confirmed | Corrected from round 1: builder evidence shows 18 killed and 1 survived (M18). |
| 9 | M05 killed by final tests | handoff section 4 | Read from evidence file `mutations-run2.txt` | Confirmed | Killed by `TestFault_CancelRacesLeaseExpiry`. |
| 10 | M15 killed by final tests | handoff section 4 | Read from evidence file `mutations-run3-m15-m18-m19.txt` | Confirmed | Killed by `TestFault_WorkerCapacityNeverExceeded`. |
| 11 | M18 survived in faultinject suite | handoff section 4 | Read from evidence file `mutations-run3-m15-m18-m19.txt` | Confirmed / Refuted | Survived faultinject suite, but killed by database test suite `TestPostgresRepository_Sweeper_CanonicalLockOrder`. |
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

## Coverage Matrix Row Verification (38 Rows)

All 38 rows in `docs/agents/handoffs/P4-4e.md` were cross-checked mechanically using `docs/agents/reviews/P4-4e-round1-evidence/scripts/matrix_check.py.txt` against `go test -list '.*' ./...` (`go-test-list.txt`, confirming 264 total repository tests) and `test-security-run1.txt`. In addition, test bodies were inspected by hand for FS-06, FS-07, FS-09, all X rows (X-01 to X-10), and 5 named AB rows (AB-01, AB-04, AB-06, AB-16, AB-17).

| ID | Scenario / Class | Cited Test Names | Names Exist | Status in test-security | Judgement from Code Inspection |
|---|---|---|---|---|---|
| **FS-01** | Worker dies before or while accepting | `TestFault_WorkerKill9_LeaseSweepAndRetry`, `TestPostgresRepository_Concurrency_SameJobDispatchRaced` | Yes | PASS (realproc), NOT_IN_TEST_SECURITY (storage) | Exercises SIGKILL of worker during task with lease expiry, sweeper reclamation, and retry on survivor. |
| **FS-02** | Worker `kill -9` mid task, sweep, retry elsewhere | `TestFault_WorkerKill9_LeaseSweepAndRetry` | Yes | PASS | Verifies attempt 1 marked LOST on killed worker, LEASE_EXPIRED event emitted, and attempt 2 succeeds. |
| **FS-03** | Result lost or delayed, partition | `TestFault_WorkerPartition_ProxyBlackhole_ZombieAbortsAndLateCommit409` | Yes | PASS | Probes real worker partitioned via blackhole proxy, verifying zombie aborts and late commit is rejected. |
| **FS-04** | Old worker returns after reassignment | `TestFault_LateResult_AfterSweep` | Yes | PASS | Verifies late result from swept attempt returns 409 LEASE_EXPIRED with single DIAGNOSTIC_LATE_RESULT event. |
| **FS-05** | Coordinator restart, startup grace | `TestFault_CoordinatorKill9_StartupGrace_SurvivingCommit`, `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` | Yes | PASS (realproc), NOT_IN_TEST_SECURITY (wiring) | Exercises coordinator SIGKILL with lapsed lease, testing commit acceptance during startup grace and rejection after. |
| **FS-06** | Crash around the SQL COMMIT | `TestFault_CoordinatorKill9_DuringCommitStorm_InvariantsAfterRestart`, `TestFault_AmbiguousCommit_ReplayIsSafe` | Yes | PASS (both) | Evaluates commit storm kill -9 and 503 deadline replay as substitute approximations for SQL COMMIT failpoint. |
| **FS-07** | Database outage and recovery | `TestFault_DBOutage_ProxyModes`, `TestFault_DBOutage_OutageGraceWindow_ProtectsSurvivingCommit`, `TestFault_DBOutage_HalfOpen`, `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` | Yes | PASS (first 3), NOT_IN_TEST_SECURITY (container restart) | Exercises connection drops, resets, delays, and blackhole hangs via fault proxy; container test designed-not-executed. |
| **FS-08** | Heartbeat duplicated or delayed | `TestFault_Heartbeat_DuplicateAndDelayed` | Yes | PASS | Tests 20 concurrent duplicate heartbeats and delayed heartbeat recovery from UNHEALTHY to HEALTHY. |
| **FS-09** | Timeout, huge output, poison repeat | `TestFault_RetriesExhausted`, `TestAbuse_OversizeErrorText_RetryPath`, `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` | Yes | PASS (first 2), NOT_IN_TEST_SECURITY (worker) | Exercises retry budget exhaustion and 10 KiB error bound; 10 MiB real-binary output deferred to V2 benchmark. |
| **FS-10** | Cancel racing completion and expiry | `TestFault_CancelRacesCompletion`, `TestFault_CancelRacesLeaseExpiry` | Yes | PASS (both) | Tests 40 concurrent jobs racing commits against duplicate cancels, and deterministic cancel-then-sweep races. |
| **X-01** | Stale, foreign-job, foreign-attempt, foreign-worker tokens | `TestFault_StaleTokenMatrix` | Yes | PASS | Probes 11 matrix cases across `/result` and `/renew`, verifying 409/403/404 rejections without state mutation. |
| **X-02** | Duplicate and replayed commit | `TestFault_DuplicateAndReplayedCommit` | Yes | PASS | Exercises sequential replays, forged token replays, and 30-way concurrent duplicate commits. |
| **X-03** | Retries exhausted | `TestFault_RetriesExhausted` | Yes | PASS | Verifies transient failure loops and lost lease sweeps up to max_attempts, refusing further dispatch. |
| **X-04** | Many workers, many jobs, invariants | `TestFault_ManyWorkersManyJobs_Invariants`, `TestFault_WorkerCapacityNeverExceeded` | Yes | PASS (both) | Stress tests 80 jobs under 8 workers with random failures, cancels, and lease lapses, and tests slot enforcement. |
| **X-05** | Lease lost stops worker and suppresses result (F-4c-a-A2) | `TestFault_WorkerPartition_ProxyBlackhole_ZombieAbortsAndLateCommit409`, `TestWorkerLeaseE2E_WorkerWhoseRenewalsStopIsDeclaredLostAndAborts` | Yes | PASS (realproc), NOT_IN_TEST_SECURITY (e2e) | Proves real partitioned worker aborts task execution and emits 0 result events after lease expiration. |
| **X-06** | Sweeper lock order deadlock | `TestFault_CancelRacesLeaseExpiry`, `TestFault_ManyWorkersManyJobs_Invariants` | Yes | PASS (both) | Evaluated via mutations M18 (survived builder fault tests, killed by storage suite) and M19 (killed by cancel race). |
| **X-07** | Sweeper and reaper resume after a half-open database | `TestFault_Sweeper_ResumesAfterHalfOpenDatabase` | Yes | PASS | Verifies background sweeper and reaper loops resume normal cycles within 60s of network partition healing. |
| **X-08** | Request deadline does not cut off legitimate requests | `TestFault_RequestDeadline_NoLegitimateRequestCutOff` | Yes | PASS | Validates all API endpoints under 100ms round-trip latency to prove none exceed the 8s request deadline. |
| **X-09** | One hung call does not stall the rest | `TestFault_DBOutage_HalfOpen_OtherRequestsNotStalled` | Yes | PASS | Proves `/healthz` answers 200 immediately while requests waiting on hung DB connection fail within 8s. |
| **X-10** | Shutdown with a half-open database | `TestFault_CoordinatorShutdown_HalfOpenDatabase_Bounded` | Yes | PASS | Measures graceful SIGTERM shutdown of coordinator process under half-open connection (bounded at ~17s). |
| **AB-01** | Malformed, truncated JSON, BOM, NUL, wrong type, trailing data | `TestAbuse_MalformedBodies`, `TestAbuse_TrailingDuplicateUnknownFields` | Yes | PASS (both) | Validates 13 malformed body variations across all 5 mutating endpoints, verifying 400 rejection and 0 state changes. |
| **AB-02** | Wrong or missing Content-Type | `TestAbuse_ContentTypes` | Yes | PASS | Verifies JSON decoding occurs regardless of Content-Type header without CSRF or parsing side effects. |
| **AB-03** | Wrong methods, unknown paths | `TestAbuse_MethodsAndPaths` | Yes | PASS | Validates 404/405 error responses for invalid paths and methods across all routes. |
| **AB-04** | Oversized bodies | `TestAbuse_OversizedBodies` | Yes | PASS | Verifies 1 MiB body cap, 64 KiB payload boundary, and rejection of 500 KB task_type without reflection. |
| **AB-05** | Deep nesting | `TestAbuse_DeepNesting` | Yes | PASS | Tests 9,000 to 500,000 JSON nesting levels, ensuring server remains healthy without stack overflow panics. |
| **AB-06** | Huge or negative numbers | `TestAbuse_NumericBounds` | Yes | PASS | Fuzzes numeric limits for max_attempts [1, 20], timeout_seconds [1, 86400], total_slots [1, 1024], and exit_code. |
| **AB-07** | Unknown and duplicate fields | `TestAbuse_TrailingDuplicateUnknownFields` | Yes | PASS | Verifies duplicate JSON keys use last-value-wins and unknown fields are ignored without reflection. |
| **AB-08** | Traversal, control chars, unicode tricks, long ids | `TestAbuse_PathParameters` | Yes | PASS | Tests 20 hostile identifier patterns across 7 parameterized routes, ensuring clean 400 INVALID_REQUEST rejections. |
| **AB-09** | Header abuse and framing | `TestAbuse_HeaderAbuse` | Yes | PASS | Tests 17 raw HTTP header abuse patterns including 1.25 MiB chunked bodies without server corruption. |
| **AB-10** | IDs of another resource | `TestAbuse_CrossResourceIDs` | Yes | PASS | Verifies attempts, jobs, and workers from different resources cannot cross-reference in state mutations. |
| **AB-11** | Unauthenticated calls to every endpoint | `TestAbuse_UnauthenticatedEndpoints` | Yes | PASS | Documents accepted V1 SEC-004 gap (all endpoints answer anonymous callers identically). |
| **AB-12** | Slow headers, slow body, slowloris | `TestAbuse_SlowClients` | Yes | PASS | Verifies slowloris and stalled header/body connections are reaped within configured timeouts. |
| **AB-13** | Many simultaneous connections | `TestAbuse_ManyConnections` | Yes | PASS | Probes 400 idle connections reaped cleanly and 300 concurrent requests succeeding without socket leaks. |
| **AB-14** | Error responses and logs echo input or leak internals | `TestAbuse_ErrorLeakScan`, `TestAbuse_RealBinaryBattery`, `TestFault_DBOutage_ProxyModes` | Yes | PASS (all 3) | Scans all responses, logs, stdout, and stderr for leaked credentials, SQL queries, or internal stack traces. |
| **AB-15** | Missing rate, size, count limits | `TestAbuse_MissingLimits` | Yes | PASS | Records missing limits under 2,000 memory jobs and 300 PostgreSQL jobs (SEC-010 related). |
| **AB-16** | Oversize error text through the retry path | `TestAbuse_OversizeErrorText_RetryPath` | Yes | PASS | Verifies attempts with error text exceeding 10,240 bytes return 400 INVALID_REQUEST and remain RUNNING. |
| **AB-17** | Hostile error text in API and `mc` output | `TestAbuse_HostileErrorText_API`, `TestAbuse_MCOutput_HostileErrorText` | Yes | PASS (API), SKIP (`mc`) | API test confirms JSON escapes control characters; CLI test skipped by default and fails when enabled (F-4e-18). |
| **AB-18** | NUL bytes in worker text | `TestAbuse_NULBytes` | Yes | PASS | Verifies NUL bytes in worker version, output, and error return 400 INVALID_REQUEST across both backends. |

---

## Independent Runs & Mutation Accounting

### Quality Gates Executed
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

### Builder Mutation Evidence Accounting (M01–M19)
The builder's mutations were read from evidence (`mutations-run2.txt` and `mutations-run3-m15-m18-m19.txt`), not re-executed:
- In `mutations-run2.txt`, 16 mutations were killed (M01–M14, M16, M17) and 2 survived (M15, M18).
- In `mutations-run3-m15-m18-m19.txt`, the builder added `TestFault_WorkerCapacityNeverExceeded`, which killed M15, re-tested M18 (survived), and added M19 (killed).
- Final builder evidence outcome: **18 killed, 1 survived (M18)**. The round-1 review mistakenly counted 17 killed and 2 survived because it evaluated `mutations-run2.txt` before M15's killing test was accounted for.

### Independent PostgreSQL Mutations Executed (PG-M01–PG-M10)
Ten mutations against the production PostgreSQL storage repository and coordinator service were executed independently. Each mutation was diffed, tested against `make test-db` or `make test-security`, and restored cleanly.

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
| **PG-M09** | Candidate 9 substitute (`internal/coordinator/service.go`) | Loosened `MaxMaxAttempts` from 20 to 200 (coordinator-level substitute because ceiling validation is in service, not PostgreSQL repo) | **KILLED** | `TestAPI_SubmitJob_BoundaryValues/max_attempts_twenty_one_exceeds_upper_bound` | `mut-pg-09.txt` |
| **PG-M10** | Candidate 10 (`internal/storage/postgres/repository.go`) | Skipped writing results table row on terminal attempt commit | **KILLED** | `TestPostgresRepository_Concurrency_DuplicateCommitRaced`, `TestConformance_CommitAttempt_TerminalJob` | `mut-pg-10.txt` |

#### Discarded Attempts for PG-M09
Candidate 9 originally specified mutating the `max_attempts` ceiling check. Because this check does not exist in `internal/storage/postgres`, several coordinator and API mutation texts were tried before the final accepted mutation:
1. **Discarded Attempt 1**: In `internal/coordinator/service.go`, removed `|| maxAttempts > MaxMaxAttempts` from `SubmitJob`. Tested with `go test -v -race ./internal/coordinator/... ./tests/abuse/...`. The test passed because API submission bounds testing is located in `internal/api`.
2. **Discarded Attempt 2**: In `internal/api/handlers.go`, removed `|| *req.MaxAttempts > coordinator.MaxMaxAttempts` from `handleSubmitJob`. Tested with `go test -v -race -run 'TestAPI_SubmitJob_BoundaryValues' ./internal/api/...`. The test still passed because `SubmitJob` in `internal/coordinator/service.go` independently enforced the ceiling, returning `domain.ErrInvalidRequest` which the handler mapped to 400 `INVALID_REQUEST`.
3. **Final Accepted Mutation**: In `internal/coordinator/service.go`, loosened constant `MaxMaxAttempts = 20` to `MaxMaxAttempts = 200`. Tested with `go test -v -race -run 'TestAPI_SubmitJob_BoundaryValues' ./internal/api/...`, which failed with `TestAPI_SubmitJob_BoundaryValues/max_attempts_twenty_one_exceeds_upper_bound` (attempting `max_attempts=21` returned 201 Created instead of 400 `INVALID_REQUEST`), successfully killing the mutation.

---

## Attacks Attempted & Redo Evidence

All attack scripts are saved under `docs/agents/reviews/P4-4e-round1-evidence/scripts/` matching what was run.

### (a) Job Payload Fuzzing
- `\u0000` Unicode escape: Accepted into `jobs.payload` (`BYTEA`) with 201 Created.
- Invalid UTF-8 (`\xff\xfe`): Accepted into `BYTEA` with 201 Created.
- Lone surrogate (`\uD800`): Accepted into `BYTEA` with 201 Created.
- Duplicate keys: Accepted (last value wins) with 201 Created.
- 64 KiB boundary: Exactly 64 KiB accepted (201 Created); 70 KiB rejected with 413 `PAYLOAD_TOO_LARGE`.
- 1 MiB HTTP body limit: Exceeding 1 MiB rejected with 400 `MALFORMED_JSON`.
- Evidence: `attack-a-payloads.txt` (script `attack_a.sh.txt`).

### (b) Worker Text Fields Validation (Redo with Matching Attempt & Version Readback)
In round 1, steps 4d, 4e, 4f, 5a, and 5b mismatched `job_id` and `attempt_id`, receiving 409 `STALE_ATTEMPT_REJECTED` before reaching validation. Redone with fresh, matching IDs from polling via `attack_b_round2.py.txt`:
- **Worker version NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"version must not contain NUL bytes"}}`.
- **Supported tasks NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"...invalid task name \"echo\\x00\"..."}}`.
- **Version >64 chars**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"...version length cannot exceed 64 characters"}}`.
- **Invalid UTF-8 in version**: Registration accepted with 200 OK.
  - **API Readback**: `GET /v1/workers` returned status 200. Inspecting the returned JSON showed `version='\ufffd\ufffd'` (hex `efbfbdefbfbd`). Go's standard library `json.NewDecoder` decodes invalid UTF-8 bytes to the valid Unicode replacement character U+FFFD. The stored value in PostgreSQL is valid UTF-8 text, and downstream JSON decoders parse it cleanly without error.
- **Worker ID path NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"invalid worker id"}}`.
- **Output NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"output and error must not contain NUL bytes"}}`.
- **Error NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"output and error must not contain NUL bytes"}}`.
- **Error classification NUL**: Rejected with 400 `{"error":{"code":"INVALID_REQUEST","message":"output and error must not contain NUL bytes"}}`.
- **Output over 64 KiB (70,000 bytes)**: Rejected with HTTP 400 `{"error":{"code":"OUTPUT_TOO_LARGE","message":"output exceeds maximum allowed size"}}`.
- **Error over 10 KiB (15,000 bytes)**: Rejected with HTTP 400 `{"error":{"code":"INVALID_REQUEST","message":"invalid request payload or parameters"}}`.
- **Invalid UTF-8 in output (`bad \xff\xfe utf8`)**: Accepted with HTTP 200 `{"job_state":"SUCCEEDED","status":"COMMITTED"}` (decoded safely into UTF-8 replacement characters).
- **Lease token NUL in renew**: Returns HTTP 409 `{"error":{"code":"STALE_ATTEMPT_REJECTED","message":"stale attempt: attempt ID or lease token does not match active attempt"}}`.
- **Very long lease token (2,000 chars) in renew**: Returns HTTP 409 `{"error":{"code":"STALE_ATTEMPT_REJECTED","message":"stale attempt: attempt ID or lease token does not match active attempt"}}`.
- Evidence: `attack-b-round2-worker-text-fields.txt` (script `attack_b_round2.py.txt`).

### (c) Path Parameter Identifier Fuzzing
- Fuzzed route parameters across all routes with SQL injection, quotes, percent-encoded NUL, raw NUL, 100 KiB IDs, Cyrillic, traversal, and CRLF.
- 100% rejected with HTTP 400 `INVALID_REQUEST` by `isValidIdentifier`. 0 server 5xx errors.
- Evidence: `attack-c-path-params.txt` (script `attack_c.py.txt`).

### (d) Numeric Boundaries
- `max_attempts`: Bounded strictly to [1, 20]; values <1 or >20 return 400 `INVALID_REQUEST`.
- `timeout_seconds`: Bounded strictly to [1, 86400]; values <1 or >86400 return 400 `INVALID_REQUEST`.
- `total_slots`: Bounded strictly to [1, 1024]; values <1 or >1024 return 400 `INVALID_REQUEST`.
- `available_slots` in heartbeat: Accepted -1, -100, and max int64 with HTTP 200 `{"status":"ACK"}`. Confirmed by code reading that both PostgreSQL `internal/storage/postgres/repository.go` and in-memory `internal/storage/inmemory.go` declare `RecordHeartbeat(ctx context.Context, workerID string, _ int) error` and discard the parameter. Available capacity is computed dynamically per ADR-0011.
- `exit_code`: Bounded strictly to 32-bit signed integers; values > math.MaxInt32 return 400 `INVALID_REQUEST`.
- Evidence: `attack-d-numeric-boundaries.txt` (script `attack_d.py.txt`).

### (e) Oversize Result Delivery (>64 KiB) Behavior
- **What execution showed**: Sending HTTP POST to `/result` with output >64 KiB returns HTTP 400 with code `OUTPUT_TOO_LARGE`. The attempt remains in `RUNNING` state until its lease expires.
- **What execution did NOT show**: Execution did not demonstrate a poison loop. The round-1 test script used a hand-crafted client that polled a different job, never renewed the lease, and terminated after 14 seconds with the job still RUNNING (default lease is 30 seconds).
- **Loop hypothesis (confirmed by code reading)**:
  - In `internal/worker/client.go`, `deliverResult` calls `isTerminalDelivery(status)` which returns true for HTTP 400 (`status == http.StatusBadRequest`). On receiving 400, `deliverResult` logs `slog.Warn("coordinator permanently rejected result (non-retryable)")` and returns immediately without retrying or marking the attempt failed.
  - In `Agent.ExecuteAndReport` (`internal/worker/client.go`), deferred `stopRenew()` stops lease renewal when `deliverResult` returns. The attempt sits `RUNNING` in the database until the lease duration lapses, after which `LeaseSweeper.Tick` sweeps it to `LOST` and retries the job.
  - Output truncation in executors: In `internal/executor/executor.go`, `Registry.Execute` enforces `MaxOutputSizeBytes` (64 * 1024) and truncates output (`if len(output) > MaxOutputSizeBytes { output = output[:MaxOutputSizeBytes] }`).
  - Therefore, built-in allowlisted tasks (`EchoTask`, `SleepTask`, `SHA256LoopTask`) executed through `Registry.Execute` can never produce output exceeding 64 KiB.
  - Only a custom, external, or buggy worker bypassing `Registry.Execute` could produce >64 KiB output and encounter this behavior.
  - The builder will trace this with the real worker and the auditor will verify it during re-audit. Severity is Low.
- Evidence: `attack-e-oversize-output-loop.txt` (script `attack_e.py.txt`).

### (f) Concurrency Attack: 200 Jobs Under Race
- Executed 200 concurrent jobs against coordinator binary with 10 polling workers, sweeper ticks, and concurrent cancel requests.
- Final states: 14 SUCCEEDED, 186 CANCELLED. Zero database inconsistencies, zero dropped attempts, zero panics or deadlocks.
- Evidence: `attack-f-concurrency-200jobs.txt` (script `attack_f.go.txt`).

### (g) Hostile Terminal Escape Demonstration (F-4e-18)
- Executed `TestAbuse_MCOutput_HostileErrorText` with `MINICLOUD_SEC_DEMO_F4E18=1`.
- Confirmed that `mc` prints raw terminal escape sequence `0x1b` when rendering error output.
- Evidence: `attack-f4e18-demo.txt`.

---

## Findings

### Builder's Findings Review (F-4e-1 to F-4e-20)
- **F-4e-1 to F-4e-17**: **AGREE** with findings and builder's proposed severities.
- **F-4e-18** (Hostile error text in CLI, Low): **AGREE**. Confirmed via executed test `TestAbuse_MCOutput_HostileErrorText`.
- **F-4e-19** (NUL bytes cause PostgreSQL driver error 500, Low): **AGREE**. Fix validated.
- **F-4e-20** (Sweeper lock order test not satisfiable, Informational): **DISAGREE / WITHDRAWN**. Sweeper lock order is already tested by `TestPostgresRepository_Sweeper_CanonicalLockOrder`.

### Auditor Findings (AUD-4e-01 to AUD-4e-07)

- **AUD-4e-01**: **Builder Claim M18 (F-4e-20) Refuted**
  - **Evidence Label**: executed (`mut-pg-05.txt`)
  - **Severity**: Informational
  - **Description**: The builder claimed M18 survived and that a lock-order test was impossible due to `SKIP LOCKED`. In reality, `internal/storage/postgres/sweeper_test.go` has `TestPostgresRepository_Sweeper_CanonicalLockOrder` which queries PostgreSQL lock tables and fails immediately when lock order is reversed. The builder only tested M18 against `tests/faultinject`. The product is better than claimed.
  - **Suggested Fix**: Update handoff and withdraw finding F-4e-20.

- **AUD-4e-02**: **PostgreSQL Dispatch SKIP LOCKED Test Gap**
  - **Evidence Label**: executed (`mut-pg-01.txt`)
  - **Severity**: Low
  - **Description**: Removing `FOR UPDATE SKIP LOCKED;` from `DispatchNextJob` in `internal/storage/postgres/repository.go` survived both `make test-db` and `make test-security`. The service-wide mutex already serialises dispatch calls inside one coordinator, so the effect is not observable through the API with one coordinator; it matters with several coordinators.
  - **Suggested Fix**: Add a database test with multiple concurrent workers contending on multiple queued jobs to prove non-blocking FIFO dispatch with `FOR UPDATE SKIP LOCKED`.

- **AUD-4e-03**: **Slow Healthy Database Induces 30s Lease Reclamation Delay**
  - **Evidence Label**: confirmed by code reading (`internal/coordinator/sweeper.go` and `internal/coordinator/service.go`)
  - **Severity**: Informational
  - **Description**: `BackgroundCallTimeout` is fixed at 8 seconds. A slow database transaction taking >8s trips tick timeout, causing the next tick to open a 30s `outageWindowUntil`, suppressing lease recovery for 30 seconds.
  - **Suggested Fix**: Distinguish connection outages from slow queries, or make `BackgroundCallTimeout` configurable in future phases.

- **AUD-4e-04**: **Worker Registration Decodes Raw Non-UTF-8 Bytes to U+FFFD**
  - **Evidence Label**: executed (`attack-b-round2-worker-text-fields.txt`)
  - **Severity**: Informational
  - **Description**: Registering a worker with raw non-UTF-8 bytes (`\xff\xfe`) is decoded by Go's standard JSON library to Unicode replacement characters (`\ufffd\ufffd`). The stored value in PostgreSQL is valid UTF-8 text and decodes cleanly via `GET /v1/workers`.
  - **Suggested Fix**: None required for V1; optional input validation in future hardening.

- **AUD-4e-05**: **Heartbeat Available Slots Value Discarded by Backends**
  - **Evidence Label**: confirmed by code reading (`internal/storage/postgres/repository.go`, `internal/storage/inmemory.go`)
  - **Severity**: Informational
  - **Description**: Both storage repositories declare `RecordHeartbeat(ctx context.Context, workerID string, _ int) error` and discard the available slots parameter. Worker capacity is computed dynamically from total slots and active attempts (ADR-0011).
  - **Suggested Fix**: None required; field is ignored by design.

- **AUD-4e-06**: **Oversize Output Rejection Traced to Worker Abandonment**
  - **Evidence Label**: confirmed by code reading (`internal/worker/client.go`, `internal/executor/executor.go`)
  - **Severity**: Low
  - **Description**: When a worker reports output exceeding 64 KiB, the coordinator returns 400 `OUTPUT_TOO_LARGE`. `isTerminalDelivery` treats 400 as non-retryable and `deliverResult` stops renewing the lease, leaving the attempt RUNNING until lease expiration. Because `Registry.Execute` truncates output for all built-in tasks at 64 KiB, this only affects custom or buggy third-party workers.
  - **Suggested Fix**: Builder will trace this with the real worker process; auditor will verify during re-audit.

- **AUD-4e-07**: **False-Green Guard in Makefile Checks Count Only**
  - **Evidence Label**: confirmed by code reading (`Makefile`)
  - **Severity**: Low
  - **Description**: The Makefile guard for `test-security` checks `$(SECURITY_TEST_COUNT) == 44`. If a test is renamed/deleted and another added simultaneously, the count matches and masks the regression.
  - **Suggested Fix**: Compare sorted test names against an expected manifest file.

---

## Items Not Verified

1. **Hardware-level network partition fault injection**: Tests use an in-process fault proxy (`faultproxy.Proxy`) rather than physical iptables or Linux network namespaces.
2. **Multi-host network latency distributions**: Tests run against local loopback (`127.0.0.1:5433`).
3. **PostgreSQL container restart tests under `make test-outage`**: Intentionally skipped per standing safety instructions (shared container must not be restarted).
