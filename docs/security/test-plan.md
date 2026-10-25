# MiniCloud Security and Reliability Test Plan

This document establishes the concrete adversarial, security, and distributed failure test plan across Milestone V1, V2, and V3.

In accordance with [security-testing.md](security-testing.md), every test entry is labeled either with its **exact existing test name and file path**, or explicitly marked as **designed, not executed**.

---

## 1. Milestone V1: Core Correctness, API Boundaries, and In-Process Safety

### 1.1 API Input Validation & Malformed Input
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Go Native Fuzzing** | Native fuzz testing (`testing.F`) feeding malformed UTF-8, null bytes, escaped JSON, and boundary strings to API handlers. | Coordinator never panics; rejects malformed bodies with HTTP 400. | **designed, not executed** |
| **Oversized Payloads & Bodies** | Submitting request body $> 1\text{ MiB}$ and job payload $> 64\text{ KiB}$. | Request rejected with HTTP 400 or HTTP 413; payload memory bounded. | `TestExecutor_PayloadSizeLimit` (`internal/executor/executor_test.go:96`)<br>`TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits` (`internal/storage/postgres/security_test.go:104`)<br>**designed, not executed** *(HTTP API 1 MiB body cutoff)* |
| **Header & Slowloris Abuse** | Slow header trickling (1 byte/sec), gigantic headers ($> 1\text{ MiB}$), header injection. | Server closes connection via timeout; prevents file descriptor exhaustion (SEC-009). | `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414`) |
| **End-to-End SQL Injection** | Malicious SQL payloads injected via HTTP API job submission (task types, payload, tags). | Parameterized storage engine neutralizes input without execution. | `TestPostgresRepository_Security_SQLInjectionChallenge` (`internal/storage/postgres/security_test.go:22`) *(Unit/Repo level)*<br>`TestSecurity_PartD_SQLInjection_EndToEnd` (`tests/integration/part_d_security_test.go:343`) *(End-to-end HTTP API level)* |
| **AST SQL Concatenation Check** | Static AST analysis of all non-test Go source files in PostgreSQL repository package. | Enforce zero dynamic SQL string assembly or formatting across all queries. | `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`) |
| **Error & Log Leakage Scans** | Triggering database constraint failures, unmapped domain errors, driver failures, and malformed JSON. | HTTP responses return generic 500 or fixed JSON message; credentials sanitized in logs (SEC-007). Real raw base64url lease token confirmed absent with planted token positive control. | `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`<br>`TestAPI_OutageMapping_503RetryAfterOpaqueBody`<br>`TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`)<br>`TestSecurity_PartD_LeakScan` (`tests/integration/part_d_security_test.go:907`) |
| **Identifier & Route Parameter Validation** | 100 KiB worker and job IDs, control characters, newlines, and path traversal strings (`../../`). | API boundary validates $\le 128$ chars and `^[a-zA-Z0-9_-]+$`, returning HTTP 400 (SEC-008). | `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`) |
| **CLI Credentials in Process Table** | Migration CLI accepting database connection URL via command-line flags. | Prioritize `MINICLOUD_DATABASE_URL` environment variable; deprecation warning on flag usage (SEC-011). | `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`<br>`TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`<br>`TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`) |
| **Numeric Parameter Boundaries** | Submitting boundary values (0, -1, 1, 20, 21, max int, absent, null, wrong type, float) for `max_attempts` and `timeout_seconds`. | API and coordinator enforce $1..20$ and $1..86400$; DB check violations map to 400 never 500 (SEC-014). | `TestAPI_SubmitJob_BoundaryValues`<br>`TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`)<br>`TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`) |

### 1.2 State Machine, Concurrency & Attempt Safety
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **State Machine Invariants** | Fuzzing and asserting all combinations of $(S_{\text{from}}, S_{\text{to}})$ state transitions. | Centralized FSM rejects invalid transitions with typed errors. | `TestJobTransitionMatrix_AllPairs` (`internal/domain/transition_test.go:10`)<br>`TestJob_TransitionTo` (`internal/domain/transition_test.go:48`) |
| **Stale Attempt Rejection** | Worker submits result for an attempt that was superseded, timed out, or cancelled. | Commit rejected with HTTP 409 or `ErrStaleAttempt`; state preserved. | `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`)<br>`CommitAttempt_ValidationsAndOutcome` in `internal/storage/storagetest/conformance.go:206` |
| **Worker Mismatch Protection** | Rogue worker attempts to commit results for an attempt leased to another worker. | Commit rejected with `ErrWorkerMismatch`; logs assigned worker truthfully. | `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`)<br>`TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`)<br>`CommitAttempt_ValidationsAndOutcome` in `internal/storage/storagetest/conformance.go:206` |
| **Forged Tokens & Replay Attacks** | Submitting commit with random 32-byte base64url token or replaying valid lease token twice. | Only the active attempt lease token hash matches; replay rejected. | `TestPostgresRepository_Security_ForgedTokenAndReplayAttacks` (`internal/storage/postgres/security_test.go:192`)<br>`TestInMemoryRepository_LeaseTokenHashVerification` (`internal/storage/inmemory_test.go:212`) |
| **Concurrent Dispatch Races** | Multiple workers attempting to claim the exact same job concurrently. | Atomic transaction ensures exactly one worker acquires lease; attempt numbers are monotonic. | `TestPostgresRepository_Concurrency_SameJobDispatchRaced` (`internal/storage/postgres/concurrency_test.go:19`)<br>`TestInMemoryRepository_ConcurrentDispatch_ExactlyOneAttempt` (`internal/storage/inmemory_test.go:120`) |
| **Worker Capacity Races** | Dispatching jobs concurrently to a worker near maximum slot capacity. | Worker capacity limits never violated under concurrent claims. | `TestPostgresRepository_Concurrency_WorkerCapacityRaced` (`internal/storage/postgres/concurrency_test.go:107`) |
| **Cancellation Context Atomicity** | Cancelling a job concurrently with worker completion or database commit. | Database state transitions atomically; no partial commit or stranded running attempt. | `TestPostgresRepository_Security_ContextCancellationAtomicity` (`internal/storage/postgres/security_test.go:288`) |

### 1.3 Fault Injection & Failure Recovery
| Test Category | Description / Attack Vector | Target Invariant | Existing Test / Status |
| :--- | :--- | :--- | :--- |
| **Worker Crash & Reaper Recovery** | Worker process dies silently while executing an active attempt lease. | Reaper identifies heartbeat timeout, marks worker unhealthy, expires lease, requeues job. | `TestService_WorkerLiveness_ReaperAndRecovery` (`internal/coordinator/service_test.go:54`)<br>`TestWorkerLiveness_DeadWorkerReapedAndWorkContinues` (`tests/integration/worker_liveness_test.go:27`) |
| **Coordinator Restart** | Coordinator process killed and restarted while workers are executing jobs. | Workers detect 404/connection drop, auto-re-register, and complete leased work. | `TestWorker_CoordinatorRestart_AutoReRegistrationAndCompletion` (`tests/integration/coordinator_restart_test.go:54`) |
| **PostgreSQL Transient Outage** | Database connectivity severed during claim dispatch or attempt commit. | Coordinator gracefully surfaces storage error; worker retries or cleanly rejects; no duplicate commit, no lost job, no partial rows. | `TestService_ClaimOutcome_TransientFailure_QueueReleased` (`internal/coordinator/service_test.go:665`)<br>`TestPostgresWiring_DatabaseOutageAndRecovery` (`tests/integration/postgres_wiring_test.go:350`)<br>`TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:707`) |
| **Database Pool Exhaustion** | Heavy concurrent query pressure exhausting `pgxpool.Pool` connection limits under held connections. | Process remains alive, latency bounded, every response 2xx/4xx or opaque 503; positive control verifies >=10 requests waited >= 320ms; recovers immediately. | `TestSecurity_PartD_PoolExhaustion` (`tests/integration/part_d_security_test.go:48`) |
| **Slow Query Under Deadline** | Forcing statement timeout or lock contention under short query context deadline. | Returns HTTP 503 SERVICE_UNAVAILABLE with Retry-After: 1 and opaque envelope; no hang, no 500. | `TestSecurity_PartD_SlowQuery_ContextDeadline` (`tests/integration/part_d_security_test.go:260`) |
| **Migration Timeout Isolation** | Long-running schema migrations (e.g. DDL or pg_sleep) executed by migrator. | Migrator runs on dedicated connection unconstrained by coordinator's statement_timeout or lock_timeout. | `TestMigrator_NotSubjectToCoordinatorQueryTimeout` (`internal/storage/postgres/migrator_test.go:640`) |
| **Allowlisted Host Safety** | Submitting arbitrary shell command execution attempts to worker executor. | Tasks outside allowlist (`echo`, `sleep`, `sha256_loop`) rejected without execution. | `TestExecutor_UnknownTaskType` (`internal/executor/executor_test.go:80`) |

---

## 2. Milestone V2: Queue Transport, Scheduling & Load Generation

### 2.1 Queue Transport & Dispatch Comparison
| Test Category | Description / Attack Vector | Target Invariant | Status |
| :--- | :--- | :--- | :--- |
| **Duplicate Message Delivery** | Simulating duplicate delivery of job IDs from Redis Streams or Postgres queue. | Coordinator and storage idempotently acknowledge duplicate without creating duplicate attempts. | `TestService_Claim_Redelivery_NoDuplicateAttempt` (`internal/coordinator/service_test.go:526`) *(In-memory queue)*<br>**designed, not executed** *(Redis Streams transport)* |
| **Queue Precedence & Invariant Alignment** | Dispatch claiming dead jobs with unhealthy workers across both queue engines. | Unhealthy worker with dead job dropped and acknowledged, never released back to queue (SEC-002). | `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`), Conformance Suite (`internal/storage/storagetest/conformance.go:64`) |
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
