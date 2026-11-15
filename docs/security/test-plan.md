# MiniCloud Security and Reliability Test Plan

This document establishes the concrete adversarial, security, and distributed failure test plan across Milestone V1, V2, and V3.

In accordance with [security-testing.md](security-testing.md), every test entry is labeled either with its **exact existing test name and file path**, or explicitly marked as **designed, not executed**.

---

## 1. Milestone V1: Core Correctness, API Boundaries, and In-Process Safety

### 1.1 API Input Validation & Malformed Input
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Go Native Fuzzing** | Native fuzz testing (`testing.F`) feeding malformed UTF-8, null bytes, escaped JSON, and boundary strings to API handlers. | Coordinator never panics; rejects malformed bodies with HTTP 400. | **designed, not executed** |
| **Oversized Payloads & Bodies** | Submitting request body > 1 MiB and job payload > 64 KiB. | Request rejected with HTTP 400 or HTTP 413; payload memory bounded. | `TestExecutor_PayloadSizeLimit` (`internal/executor/executor_test.go:96`)<br>`TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits` (`internal/storage/postgres/security_test.go:104`)<br>**designed, not executed** *(HTTP API 1 MiB body cutoff)* |
| **Header & Slowloris Abuse** | Slow header trickling (1 byte/sec), gigantic headers (> 1 MiB), header injection. | Server closes connection via timeout; prevents file descriptor exhaustion (SEC-009). | `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go`) |
| **End-to-End SQL Injection** | Malicious SQL payloads injected via HTTP API job submission (task types, payload, tags). | Parameterized storage engine neutralizes input without execution. | `TestPostgresRepository_Security_SQLInjectionChallenge` (`internal/storage/postgres/security_test.go:22`) *(Unit/Repo level)*<br>`TestSecurity_PartD_SQLInjection_EndToEnd` (`tests/integration/part_d_security_test.go`) *(End-to-end HTTP API level)* |
| **AST SQL Concatenation Check** | Static AST analysis of all non-test Go source files in PostgreSQL repository package. | Enforce zero dynamic SQL string assembly or formatting across all queries. | `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`) |
| **Error & Log Leakage Scans** | Triggering database constraint failures, unmapped domain errors, driver failures, and malformed JSON. | HTTP responses return generic 500 or fixed JSON message; credentials sanitized in logs (SEC-007). Real raw base64url lease token confirmed absent with planted token positive control. | `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`<br>`TestAPI_OutageMapping_503RetryAfterOpaqueBody`<br>`TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go`)<br>`TestSecurity_PartD_LeakScan` (`tests/integration/part_d_security_test.go`) |
| **Identifier & Route Parameter Validation** | 100 KiB worker and job IDs, control characters, newlines, and path traversal strings (`../../`). | API boundary validates <= 128 chars and `^[a-zA-Z0-9_-]+$`, returning HTTP 400 (SEC-008). | `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go`) |
| **CLI Credentials in Process Table** | Migration CLI accepting database connection URL via command-line flags. | Prioritize `MINICLOUD_DATABASE_URL` environment variable; deprecation warning on flag usage (SEC-011). | `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`<br>`TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`<br>`TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`) |
| **Numeric Parameter Boundaries** | Submitting boundary values (0, -1, 1, 20, 21, max int, absent, null, wrong type, float) for `max_attempts` and `timeout_seconds`. | API and coordinator enforce 1..20 and 1..86400; DB check violations map to 400 never 500 (SEC-014). | `TestAPI_SubmitJob_BoundaryValues`<br>`TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go`)<br>`TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`) |

### 1.2 State Machine, Concurrency & Attempt Safety
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **State Machine Invariants** | Fuzzing and asserting all combinations of (from, to) state transitions. | Centralized FSM rejects invalid transitions with typed errors. | `TestJobTransitionMatrix_AllPairs` (`internal/domain/transition_test.go:10`)<br>`TestJob_TransitionTo` (`internal/domain/transition_test.go`) |
| **Stale Attempt Rejection** | Worker submits result for an attempt that was superseded, timed out, or cancelled. | Commit rejected with HTTP 409 or `ErrStaleAttempt`; state preserved. | `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go`)<br>`CommitAttempt_ValidationsAndOutcome` in `internal/storage/storagetest/conformance.go` |
| **Worker Mismatch Protection** | Rogue worker attempts to commit results for an attempt leased to another worker. | Commit rejected with `ErrWorkerMismatch`; logs assigned worker truthfully. | `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go`)<br>`TestAPI_StaleAttemptRejection` (`internal/api/api_test.go`)<br>`CommitAttempt_ValidationsAndOutcome` in `internal/storage/storagetest/conformance.go` |
| **Forged Tokens & Replay Attacks** | Submitting commit with random 32-byte base64url token or replaying valid lease token twice. | Only the active attempt lease token hash matches; replay rejected. | `TestPostgresRepository_Security_ForgedTokenAndReplayAttacks` (`internal/storage/postgres/security_test.go:192`)<br>`TestInMemoryRepository_LeaseTokenHashVerification` (`internal/storage/inmemory_test.go`) |
| **Concurrent Dispatch Races** | Multiple workers attempting to claim the exact same job concurrently. | Atomic transaction ensures exactly one worker acquires lease; attempt numbers are monotonic. | `TestPostgresRepository_Concurrency_SameJobDispatchRaced` (`internal/storage/postgres/concurrency_test.go:19`)<br>`TestInMemoryRepository_ConcurrentDispatch_ExactlyOneAttempt` (`internal/storage/inmemory_test.go`) |
| **Worker Capacity Races** | Dispatching jobs concurrently to a worker near maximum slot capacity. | Worker capacity limits never violated under concurrent claims. | `TestPostgresRepository_Concurrency_WorkerCapacityRaced` (`internal/storage/postgres/concurrency_test.go:107`) |
| **Cancellation Context Atomicity** | Cancelling a job concurrently with worker completion or database commit. | Database state transitions atomically; no partial commit or stranded running attempt. | `TestPostgresRepository_Security_ContextCancellationAtomicity` (`internal/storage/postgres/security_test.go:288`) |

### 1.3 Fault Injection & Failure Recovery
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Worker Crash & Reaper Recovery** | Worker process dies silently while executing an active attempt lease. | Reaper identifies heartbeat timeout, marks worker unhealthy, expires lease, requeues job. | `TestService_WorkerLiveness_ReaperAndRecovery` (`internal/coordinator/service_test.go`)<br>`TestWorkerLiveness_DeadWorkerReapedAndWorkContinues` (`tests/integration/worker_liveness_test.go:27`) |
| **Coordinator Restart** | Coordinator process killed and restarted while workers are executing jobs. | Workers detect 404/connection drop, auto-re-register, and complete leased work. | `TestWorker_CoordinatorRestart_AutoReRegistrationAndCompletion` (`tests/integration/coordinator_restart_test.go:54`) |
| **PostgreSQL Transient Outage** | Database connectivity severed during claim dispatch or attempt commit. | Coordinator gracefully surfaces storage error; worker retries or cleanly rejects; no duplicate commit, no lost job, no partial rows. | `TestService_ClaimOutcome_TransientFailure_QueueReleased` (`internal/coordinator/service_test.go`)<br>`TestPostgresWiring_DatabaseOutageAndRecovery` (`tests/integration/postgres_wiring_test.go:350`)<br>`TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`) |
| **Database Pool Exhaustion** | Heavy concurrent query pressure exhausting `pgxpool.Pool` connection limits under held connections. | Process remains alive, latency bounded, every response 2xx/4xx or opaque 503; primary proof verifies admin pg_stat_activity sampling observed >= pool_size-2 Lock-waiters; secondary proof verifies locked max latency exceeds 3-run unlocked baseline median by >= 0.25 * holdDuration (SEC-019); recovers immediately. | `TestSecurity_PartD_PoolExhaustion` (`tests/integration/part_d_security_test.go:45`) |
| **Slow Query Under Deadline** | Forcing statement timeout or lock contention under short query context deadline. | Returns HTTP 503 SERVICE_UNAVAILABLE with Retry-After: 1 and opaque envelope; no hang, no 500. | `TestSecurity_PartD_SlowQuery_ContextDeadline` (`tests/integration/part_d_security_test.go`) |
| **Migration Timeout Isolation** | Long-running schema migrations (e.g. DDL or pg_sleep) executed by migrator. | Migrator runs on dedicated connection unconstrained by coordinator's statement_timeout or lock_timeout. | `TestMigrator_NotSubjectToCoordinatorQueryTimeout` (`internal/storage/postgres/migrator_test.go`) |
| **Allowlisted Host Safety** | Submitting arbitrary shell command execution attempts to worker executor. | Tasks outside allowlist (`echo`, `sleep`, `sha256_loop`) rejected without execution. | `TestExecutor_UnknownTaskType` (`internal/executor/executor_test.go:80`) |

### 1.4 Phase 4: Leases, Bounded Retries, and Cancellation Fault Tests
*Detailed test specifications and execution mechanics are defined in [docs/fault-injection-plan.md](../fault-injection-plan.md).*

| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Worker Crash During Execution** | Worker process dies (`SIGKILL`) mid-task; lease expires. | Sweeper transitions attempt to `LOST` (consuming budget) and job to `RETRYING` with backoff; next attempt dispatches. | `TestFault_WorkerKill9_LeaseSweepAndRetry` (`tests/faultinject/realproc_test.go`, executed) |
| **Sweeper Concurrent Renewal Re-Check** | Worker renews lease while sweeper holds candidate in unlocked batch. | Sweeper re-checks `attempts` under row lock; skips state mutation if renewed (`clock_timestamp() < lease_expires_at`). | `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (`internal/storage/postgres/sweeper_test.go`, executed) |
| **Coordinator Restart Grace** | Coordinator restarts while workers are executing; sweeper suppresses reaping for 30s. | Surviving worker commit accepted with HTTP 200 COMMITTED without mass expiration; preserves Phase 3 guarantee. | `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (`tests/integration/postgres_restart_grace_test.go`, executed) |
| **Late Result Stale Rejection** | Expired worker returns result after newer attempt already dispatched. | Result rejected with HTTP 409 `STALE_ATTEMPT_REJECTED`; diagnostic event logged; zero resurrection. | `TestFault_LateResult_AfterSweep` (`tests/faultinject/core_test.go`, executed) |
| **Cooperative Cancellation Races** | Client cancels job concurrently with worker completion or lease lapse. | Atomic row locking on `jobs` serializes outcome; worker receives `cancel_requested` via renewal response. | `TestFault_CancelRacesCompletion` (`tests/faultinject/core_test.go`, executed) |
| **Duplicate Cancellation Idempotency** | Multiple concurrent or sequential cancel requests on a `CANCEL_REQUESTED` job. | Idempotently returns HTTP 200 with state `CANCEL_REQUESTED` (same response body as first cancel) with zero duplicate audit events; attempt remains running. | `TestConformance_CancelJob_IdempotentCancelRequested` (`internal/storage/inmemory_test.go`, `internal/storage/postgres/repository_test.go`, executed) |
| **Renewal Flood Resistance** | High-frequency renewal spamming without valid authentication (SEC-004). | State short-circuit and indexed lookup reject unknown/stale attempts without heavy locks; statement_timeout protects pool. | `TestAPI_RenewalFlood_FastShortCircuit` (planned test, designed, not executed) |
| **Bounded Retries & Backoff Jitter** | Transient task failure retries up to `max_attempts` with exponential backoff and jitter. | Backoff enforces `not_before` in DB; attempts strictly bounded <= 20; zero thundering herds. | `TestFault_Task_TimeoutAndPoisonRepeat` (planned test, designed, not executed) |
| **Result Report Network Drop** | Network drops result report; worker executes 60s bounded delivery loop with capped jittered backoff. | Worker recovers autonomously within 60s bounded delivery loop; commits with HTTP 200 `COMMITTED` as long as attempt is still `RUNNING` (not swept). | `TestFault_ResultReport_NetworkDropOrDelay` (planned test, designed, not executed) |
| **Model-Based Invariant Conformance** | Random event sequence generator driving reference model and repository in parallel. | Zero stale commits, <= 1 result per job, monotonic attempt numbers, zero active attempts after terminal. | `TestProperty_StateMachine_ModelConformance` (`internal/storage/inmemory_test.go`, `internal/storage/postgres/repository_test.go`, executed) |

### 1.5 ADR-0017 Assignment-Based Round-Robin (default policy since P4-17)
Assignment tests that exist, by property (names verified in the repository on main at d7a397c). The ADR scenario identifiers are in [fault-injection-plan.md](../fault-injection-plan.md) Section 4.

| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Reservation Lapse and Fallback** | Assigned worker alive but never polls, dies before polling, or polls after the accept window (FS-14, FS-15, FS-22). | Window lapses, job reassigned, bounded lapses, no attempt created for a lapsed reservation. | `TestFault_AssignedWorkerNeverPolls`, `TestFault_AssignedWorkerDiesBeforePoll`, `TestFault_PollRoundTripBeyondWindow`, `TestFault_AssignmentLapseLoop_BoundedByLimit` (`tests/faultinject/assignment_test.go`, executed) |
| **Claim, Crash and Cancel** | Poll response lost after claim; coordinator crash between assignment and poll; cancel races the claim (FS-16, FS-17, FS-18). | Reservation durable; one attempt only; row lock serialises cancel and claim. | `TestFault_PollResponseLostAfterClaim`, `TestFault_CoordinatorCrash_BetweenAssignAndPoll`, `TestFault_CancelRacesAccept` (`tests/faultinject/assignment_test.go`, executed) |
| **Outage and Restart Under Assignment** | Database outage during a pass; restart or outage then immediate poll; persistent pass failure (FS-20, FS-21, FS-23). | No age-arm claim before a successful refresh; refresh attempts rate-limited (INV-19, INV-23, INV-24). | `TestFault_DatabaseOutage_DuringPass`, `TestFault_PersistentPassFailure_AgeClaimRate`, `TestFault_RestartThenPoll_NoAgeClaim`, `TestFault_OutageRecovery_NoAgeClaim`, `TestFault_OutageRecovery_RateWindow` (`tests/faultinject/assignment_test.go`, executed) |
| **Deep Backlog** | Backlog deeper than one batch with polls waiting (FS-24). | Polls not starved; yield between repeated passes (INV-20, INV-21). | `TestFault_DeepBacklog_PollsNotStarved` (`tests/faultinject/assignment_test.go`, executed) |
| **Assignment Abuse** | Poll for a job reserved to another worker, poll flood, constant poller and very large `total_slots`. | Foreign poll gets 204; unhealthy worker still 503; freshness writes throttled; no larger share for the greedy worker. | `TestAbuse_Poll_ForeignAssignment_Returns204`, `TestAbuse_Poll_UnhealthyWorker_Still503`, `TestAbuse_Poll_Flood_ThrottlesFreshnessWrites`, `TestAbuse_ConstantPoller_NoLargerShare`, `TestAbuse_LargeTotalSlots_NoLargerShare` (`tests/abuse/assignment_test.go`, executed) |
| **Fairness and Sub-State Model** | Random operation sequences against the round-robin rule and the assignment sub-state. | Counts within one per task type; no double selection; sub-state model conformance. | `TestProperty_RoundRobin_CountsWithinOne`, `TestProperty_RoundRobin_NoDoubleSelectionPerType` (`internal/scheduler/round_robin_rule_test.go`), `TestProperty_AssignmentSubState_ModelConformance` (`internal/coordinator/assignment_test.go`), `TestProperty_BatchHeadNeverStarved`, `TestProperty_SaturatedPass_NoAgeFallback` (`internal/storage/inmemory_assignment_property_test.go`), executed |
| **Poll Timing (INV-17)** | Accept window versus the maximum poll backoff. | Accept window exceeds the maximum poll backoff plus the margin. | `TestPollTiming_AcceptWindowExceedsBackoff`, `TestPollTiming_RunSleepsWithinMaxBackoff` (`internal/worker/poll_timing_test.go`, executed) |

### 1.6 Operating Rules and Current Counts
- **One test process per database.** The assignment pass lock is one advisory key per database (ADR-0017 implementation note 15). Two test processes sharing a database also share the key and a pass in one skips. Run one database-backed `make` target at a time per test database. `make test`, `make test-race` and `make test-security` pass `-p 1` (see the `Makefile`).
- **`make test-outage` is destructive.** It stops and starts the shared `minicloud-postgres` container and refuses to run without `CONFIRM=minicloud-postgres`. Run it only with all agents idle: `make test-outage CONFIRM=minicloud-postgres`.
- **Counts on main at d7a397c** (from the P4-19 task message; not re-run in this docs task): `make test-security` 75 PASS, 0 SKIP, 0 FAIL; unset-URL suite 401 PASS, 91 SKIP, 0 FAIL (a skipped test is not a passed test); `make test-db` 163 PASS, 0 SKIP, 0 FAIL; `make test-outage CONFIRM=minicloud-postgres` 3 PASS, 0 FAIL.
- **Not yet executed (labels unchanged):** `TestAPI_RenewalFlood_FastShortCircuit` (planned test), `TestFault_Task_TimeoutAndPoisonRepeat` (planned test) and `TestFault_ResultReport_NetworkDropOrDelay` (planned test) have no test of that behaviour; their rows in Section 1.4 keep the label.

---


## 2. Milestone V2: Queue Transport, Scheduling & Load Generation

### 2.1 Queue Transport & Dispatch Comparison
| Test Category | Description / Attack Vector | Target Invariant | Status |
| :--- | :--- | :--- | :--- |
| **Duplicate Message Delivery** | Simulating duplicate delivery of job IDs from Redis Streams or Postgres queue. | Coordinator and storage idempotently acknowledge duplicate without creating duplicate attempts. | `TestService_Claim_Redelivery_NoDuplicateAttempt` (`internal/coordinator/service_test.go`) *(In-memory queue)*<br>**designed, not executed** *(Redis Streams transport)* |
| **Queue Precedence & Invariant Alignment** | Dispatch claiming dead jobs with unhealthy workers across both queue engines. | Unhealthy worker with dead job dropped and acknowledged, never released back to queue (SEC-002). | `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go`), Conformance Suite (`internal/storage/storagetest/conformance.go`) |
| **Broker Interruption & Reconnection** | Message broker disconnected during continuous job submission and polling. | System buffers or returns 503; auto-reconnects without loss of durable state. | **designed, not executed** |

### 2.2 Controlled Load Generation
| Test Category | Description / Methodology | Target Metric / Invariant | Status |
| :--- | :--- | :--- | :--- |
| **Plain Go Load Generator** | In-repo plain Go workload generator executing sustained concurrent job submissions and completions. | Baseline jobs/sec throughput, p50/p95/p99 latency, queue depth on local host. | **designed, not executed** |
| **Submission Burst & Saturation** | Simulating sudden submission bursts of 1,000+ jobs against local coordinator. | Queue absorbs backlog; memory usage remains bounded; no goroutine leakage. | **designed, not executed** |
| **Worker Saturation Stress** | 10+ local worker processes contending for continuous job queue stream. | Fair scheduling distribution, no starvation, zero orphaned attempts under high contention. | **designed, not executed** |

---

## 3. Milestone V3: Isolation, Multi-Machine Networking & Authentication

### 3.1 Container Isolation & Host Protection
| Test Category | Description / Attack Vector | Target Invariant | Status |
| :--- | :--- | :--- | :--- |
| **Container Escape Probing** | Task attempting privilege escalation, host filesystem mounting, or cgroup breakout. | Execution confined strictly inside ephemeral unprivileged Docker container. | **designed, not executed** |
| **Resource Limit Constraints** | Task allocating excessive memory (OOM bomb) or spinning 100% CPU. | Container terminated by Docker cgroup memory/CPU limits; host and worker remain healthy. | **designed, not executed** |
| **Filesystem Write Barrier** | Task attempting to overwrite host files or modify root container image. | Read-only container rootfs; writes isolated to temporary ephemeral volume. | **designed, not executed** |

### 3.2 Multi-Machine Networking & Authentication
| Test Category | Description / Attack Vector | Target Invariant | Status |
| :--- | :--- | :--- | :--- |
| **Worker Authentication Enforcement** | Unauthenticated worker attempting to poll jobs or send heartbeats (SEC-004). | Request rejected with HTTP 401 Unauthorized; requires valid TLS cert or token. | **designed, not executed** |
| **Client API Authorization** | Submitting job cancellation or inspection without authorized tenant token. | Request rejected with HTTP 403 Forbidden; tenant isolation verified. | **designed, not executed** |
| **Network Partition / Split-Brain** | Simulating asymmetric network loss between coordinator and remote worker machines. | Leases expire cleanly; unreached workers drop execution context without committing. | **designed, not executed** |
| **TLS Certificate Revocation** | Revoked or expired worker certificate attempting coordinator connection. | Handshake fails at TLS layer before HTTP processing begins. | **designed, not executed** |

---

## 4. Execution Principles and Tooling Scope

1. **Standard Library First**: Security tests utilize Go standard library tooling without external dependencies:
   - Race detector: `go test -race ./...`
   - Static analysis: `go vet ./...`
   - Native fuzz testing: `go test -fuzz=Fuzz...`
2. **Controlled Local Scale**: All load tests and fault simulations execute within the physical capacity of the local machine. No invented throughput or latency numbers are permitted; baseline numbers must be established and cited from real runs.
3. **Automated Regressions**: Any confirmed vulnerability (from [findings.md](findings.md)) must be paired with an automated test in the repository before being marked fixed.
4. **Documentation Citation Scoping & Adversarial Mutation Proof (SEC-020, SEC-021)**:
   Documentation citation invariants are verified by `TestCitations` (`tests/docs/citations_test.go:695`) and `TestFixtures` (`tests/docs/citations_test.go:754`). Adversarial mutation testing in a throwaway worktree proved the tightened clause-level scoping behavior of Rule C across four executed cases:
   - **Case 1 (Separate sentence)**: Appending `"Verified by TestFab_One (nonexistent). This is planned."` failed with `[RuleC] cited test "TestFab_One" does not exist`.
   - **Case 2 (Semicolon clause)**: Appending `"Verified by TestFab_Two (nonexistent); coverage is planned."` failed with `[RuleC] cited test "TestFab_Two" does not exist`.
   - **Case 3 (Different table cell)**: Appending `"| TestFab_Three (nonexistent) | planned |"` failed with `[RuleC] cited test "TestFab_Three" does not exist`.
   - **Case 4 (Same clause / self-labeled)**: Appending `"Verified by TestFab_Four (planned)."` passed, confirming that attaching an exemption label to the identifier within the same clause permits it by design.
   - **Residual Limitation & Operational Policy**: Label-based static exemptions cannot distinguish genuine future tests (`TestCoordinator_IDLeakage` (future test) and `TestInMemoryRepository_MemoryExhaustion_Cap` (future test); `TestWorker_ExcessiveTimeoutClamped` now exists in `internal/worker/timeout_test.go`) from fabricated ones carrying their own label; human review is required when any milestone claims coverage.
