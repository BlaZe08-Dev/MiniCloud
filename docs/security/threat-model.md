# MiniCloud Threat Model

## 1. Overview and Trust Boundaries

MiniCloud is a distributed job execution system. This threat model evaluates the system's security posture, assets, threat vectors, trust boundaries, and existing mitigations across V1, V2, and V3.

### Trust Boundary Architecture

```
[ Untrusted Clients / Operators ]
            │ HTTP (Plaintext / Localhost in V1)
            ▼
┌────────────────────────────────────────────────────────┐
│ HTTP API & Coordinator Boundary (internal/api)         │
│  - Max body size: 1 MiB                                │
│  - Unauthenticated in V1 (Accepted Risk)               │
└──────────────────────────────────┬─────────────────────┘
                                   │
      ┌────────────────────────────┼────────────────────────────┐
      ▼                            ▼                            ▼
┌──────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│ State Machine    │     │ Scheduler        │     │ Storage Layer    │
│ (internal/domain)│     │ (Round-Robin V1) │     │ (Postgres/InMem) │
└──────────────────┘     └──────────────────┘     └──────────────────┘
                                   ▲
                                   │ HTTP Polling (Localhost in V1)
┌──────────────────────────────────┴─────────────────────┐
│ Worker Boundary (internal/worker)                      │
│  - Task Executor (Allowlist-only in V1)                │
└────────────────────────────────────────────────────────┘
```

## 2. Component Threat Matrix

| Component | Assets | Trust Boundary | Threats | Existing Mitigations & Citations | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **CLI (`cmd/mc`)** | Operator commands, job submission params | Host user shell to Coordinator HTTP API | Parameter tampering, accidental exposure of arguments in shell history, unauthenticated job cancel/submission. | Payload bounds enforced by client CLI before submission; client binds to local endpoint. | inferred from code review (unconfirmed) |
| **HTTP API (`internal/api`)** | Coordinator availability, HTTP request handling, response data | Public/Local Network to Coordinator HTTP server | **Oversized request body (DoS)**: Attacker sends giant JSON to exhaust memory. | `maxBytesMiddleware(MaxRequestSizeBytes)` (`internal/api/server.go:36`, `internal/api/handlers.go:303`). Payload limit tested in `TestExecutor_PayloadSizeLimit` (`internal/executor/executor_test.go:96`) and `TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits` (`internal/storage/postgres/security_test.go:104`). HTTP-level 1 MiB cutoff test is designed-not-executed. | designed-not-executed |
| | | | **Slowloris / Idle connection exhaustion**: Slow header write exhausts file descriptors. | `ReadTimeout: 5s`, `WriteTimeout: 10s`, `IdleTimeout: 60s`, `ReadHeaderTimeout: 2s` (`internal/api/server.go:15,42`). Tested in `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`). | covered by a named test |
| | | | **Unauthenticated API Access**: Anyone reaching port 8080 can submit/cancel jobs (SEC-004). | Coordinator binds to `127.0.0.1:8080` by default (`internal/config/config.go:43`). | accepted risk |
| | | | **Internal Error / Schema Leakage**: Unmapped errors return raw driver strings in HTTP responses (SEC-007). | `mapDomainErrorToHTTP` maps unmapped errors to HTTP 500 `INTERNAL_ERROR` with opaque message; sanitized server logging; fixed malformed JSON response. Tested in `TestAPI_UnmappedError_*` and `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`). | covered by a named test |
| | | | **Invalid Numeric Parameters in Job Submission**: Negative or out-of-bounds `max_attempts` or `timeout_seconds` accepted or mapped to 500 (SEC-014). | Explicit boundary validation ($1..20$ and $1..86400$) at API and coordinator returning 400; PostgreSQL check constraints mapped to `domain.ErrInvalidRequest`. Tested in `TestAPI_SubmitJob_BoundaryValues`, `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`), `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`). | covered by a named test |
| **Coordinator (`internal/coordinator`)** | Job coordination state, dispatch logic, attempt sequencing | HTTP Handlers to Domain Services & Storage | **Stale Attempt / Rogue Worker Results**: Worker commits results for an expired lease or wrong worker ID (SEC-001). | Atomic check of worker ID and lease token; `domain.ErrWorkerMismatch` and `domain.ErrStaleAttempt`. Tested in `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`) and `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`). | covered by a named test |
| | | | **Predictable Job/Attempt IDs**: Attacker guesses IDs to cancel or commit jobs (SEC-013). | IDs combine atomic monotonic sequence + 6 cryptographically random bytes (`crypto/rand`) (`internal/coordinator/service.go:24-30`). | inferred from code review (unconfirmed) |
| | | | **Worker ID Spoofing / Malformed IDs**: Registration accepts unbounded length or control chars (SEC-008). | `isValidIdentifier` enforces $\le 128$ chars and `^[a-zA-Z0-9_-]+$` across worker registration and routes (`internal/api/handlers.go:27-39`). Tested in `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`). | covered by a named test |
| | | | **Unbounded Worker Registration Parameters (Slots, Version, Tasks)**: Extreme values or arrays skew scheduling or trigger errors (SEC-015, SEC-016, SEC-017). | Registration validates `TotalSlots > 0` and worker ID, but slots upper bound, version length, and `supported_tasks` slice/contents lack bounds (`internal/coordinator/service.go:207`, `internal/api/handlers.go:179-195`). | inferred from code review (unconfirmed) |
| | | | **Unbounded In-Memory Growth (DoS)**: Monotonic map growth under load without rate limits (SEC-010). | None in `InMemoryRepository` (`internal/storage/inmemory.go:19-25`) or `InMemoryQueue` (`internal/queue/inmemory.go:18-24`). | inferred from code review (unconfirmed) |
| **Worker Protocol (`internal/worker`)** | Attempt lease tokens, worker availability | Remote Worker process to Coordinator API | **Forged Attempt Commit**: Attacker crafts commit with guessed token. | Cryptographic UUIDv4 lease tokens verified atomically against database state on commit. Tested in `TestPostgresRepository_Security_ForgedTokenAndReplayAttacks` (`internal/storage/postgres/security_test.go:192`) and `internal/storage/storagetest/conformance.go:206`. | covered by a named test |
| | | | **Unauthenticated Worker Registration / Hijacking**: Impersonating workers to steal jobs. | Localhost binding only in V1 (`internal/config/config.go:43`). Full mTLS and auth deferred to V3 (SEC-004). | accepted risk |
| | | | **Coordinator Timeout Manipulation**: Malicious coordinator sends huge `TimeoutSeconds` (SEC-012). | Worker converts coordinator timeout directly (`internal/worker/client.go:267-268`); fallback checked (`<= 0 -> 30s`), but max timeout unconstrained. | inferred from code review (unconfirmed) |
| **Scheduler (`internal/scheduler`)** | Dispatch fairness, worker assignment | Coordinator to Registered Worker pool | **Deadlock on Unhealthy Worker**: Dispatch assigning work to dead workers. | Worker health filtered via `HeartbeatTimeout`; reaper marks dead workers. Tested in `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`). | covered by a named test |
| | | | **Priority Starvation / Resource Exhaustion**: High-demand jobs starving normal jobs. | Round-robin scheduler baseline in V1. Resource-aware scheduling deferred to V2. | designed-not-executed |
| **Job State Machine (`internal/domain`)** | Job & Attempt state invariants, transition validity | Service layer to State Machine | **Illegal State Transitions**: Bypassing pending/running to force completion. | Centralized FSM transition validation (`internal/domain/transition.go:15-46`). Tested in `TestJobTransitionMatrix_AllPairs` and `TestJob_TransitionTo` (`internal/domain/transition_test.go:10-100`). | covered by a named test |
| | | | **Oversized Payloads & Outputs**: Jobs with massive payloads crashing workers. | Validated in executor (`MaxPayloadSizeBytes` 64 KiB, `MaxOutputSizeBytes` 64 KiB in `internal/executor/executor.go:14-15,74`) and coordinator (`internal/coordinator/service.go:97`). Dedicated domain-level validation package is proposed, does not exist yet. Tested in `TestExecutor_PayloadSizeLimit` (`internal/executor/executor_test.go:96`) and `TestPostgresRepository_Security_PayloadAndOutputBoundaryLimits` (`internal/storage/postgres/security_test.go:104`). | covered by a named test |
| **PostgreSQL Persistence (`internal/storage/postgres`)** | Durable job records, execution attempts, lease consistency | Repository to PostgreSQL Engine | **SQL Injection**: Untrusted input concatenated into raw queries. | Strictly parameterized SQL queries. Automated AST check enforces zero string concatenation in queries. Tested in `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`). | covered by a named test |
| | | | **Connection Pool Exhaustion**: High concurrency starving connection pool. | `pgxpool.Pool` bounded by config (`MaxConns` default 10). | inferred from code review (unconfirmed) |
| | | | **Dynamic SQL in Test Fixtures (SEC-006)**: Dynamic schema names in test helpers. | Test helper uses `fmt.Sprintf("CREATE SCHEMA %s;", schemaName)` with in-process random hex (`internal/storage/postgres/testhelper_test.go:52-61`). | inferred from code review (unconfirmed) |
| **Task Executor (`internal/executor`)** | Host Operating System security | Worker process to Host OS | **Arbitrary Remote Code Execution (RCE)**: Attacker submits shell commands as tasks. | Host command execution strictly forbidden in V1. Tasks restricted to in-process allowlist (`echo`, `sleep`, `sha256_loop`). Tested in `TestExecutor_UnknownTaskType` (`internal/executor/executor_test.go:80`). | covered by a named test |
| **Configuration & Secrets (`internal/config`, `deployments`)** | DB passwords, listening ports, connection URLs | Environment / Flags to Runtime | **Database Credential Leakage in CLI**: `-database-url` passed via CLI flag visible in `ps aux` (SEC-011). | CLI prioritizes `MINICLOUD_DATABASE_URL` environment variable; `-database-url` emits deprecation warning (`cmd/migrate/main.go:16-33`). Tested in `TestMigrateCLI_ResolveDatabaseURL_*` (`cmd/migrate/main_test.go:8-61`). | covered by a named test |
| | | | **Compose Missing Fail-Fast Password (SEC-003)**: Compose file specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` without fail-fast form (`${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}`) (`deployments/docker-compose.yml:8`). | PostgreSQL bound to localhost (`127.0.0.1:5433:5432` in `deployments/docker-compose.yml:11`). Note: `docs/testing-postgres.md` previously showed a stale snippet with fallback credentials, since corrected. | accepted risk |
| **Logs & Metrics** | Audit trail, debugging logs | Internal loggers to stdout | **Lease Token / Credential Exposure**: Sensitive tokens printed to stdout. | Coordinator logger explicitly excludes lease tokens, payloads, and task outputs (`internal/coordinator/service.go`). | inferred from code review (unconfirmed) |
| **Redis Streams** *(not yet introduced)* | Job queue messages, at-least-once dispatch | Coordinator to Redis Broker | **Message Tampering / Stale Queue Delivery / Split-Brain**: Unacknowledged messages redelivered to stale workers. | Not yet introduced. Deferred to V2 (ADR-0004, ADR-0006). | designed-not-executed |
| **Docker Isolation** *(not yet introduced)* | Host OS containment, container runtime | Worker to Container Engine | **Container Escape / Host Resource Starvation**: Malicious task breaking out of container. | Not yet introduced. Deferred to V3 (ADR-0005). | designed-not-executed |

## 3. Read-Only Code Review Findings (Unconfirmed Items)

The following items were identified during static inspection of the codebase. Items fixed in Slice 3d are noted with their regression tests; remaining items are labeled **inferred from code review (unconfirmed)** until demonstrated by an automated test:

1. **HTTP Server Header Timeouts (`internal/api/server.go:15,42`)** (SEC-009, Fixed in Slice 3d):
   - Configured `DefaultReadHeaderTimeout = 2 * time.Second` on `http.Server`.
   - Verified by `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`).

2. **Unvalidated Route Parameters and Worker IDs (`internal/api/handlers.go:27-39,105-248`)** (SEC-008, Fixed in Slice 3d):
   - Added `isValidIdentifier` enforcing $\le 128$ characters and character set `^[a-zA-Z0-9_-]+$` across worker registration and route IDs.
   - Verified by `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`).

3. **Storage Error Information Disclosure (`internal/api/handlers.go:19-75`)** (SEC-007, Fixed in Slice 3d):
   - Unmapped repository and driver errors map to HTTP 500 `INTERNAL_ERROR` with opaque `"internal server error"` message while logging sanitized error server-side.
   - JSON decoding errors return fixed `"malformed JSON request body"`.
   - Verified by `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`, `TestAPI_UnmappedError_DatabaseOutage_SanitizedInternalError500`, `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`).

4. **CLI Database URL in Process Arguments (`cmd/migrate/main.go:16-33`)** (SEC-011, Fixed in Slice 3d):
   - The migration CLI prioritizes `MINICLOUD_DATABASE_URL` and issues a deprecation warning when `-database-url` is passed on the command line.
   - Verified by `TestMigrateCLI_ResolveDatabaseURL_*` (`cmd/migrate/main_test.go:8-61`).

5. **Unbounded In-Memory Collections (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`)** (SEC-010):
   - `InMemoryRepository` stores jobs, workers, and events in unbounded Go maps and slices without eviction or size caps. `InMemoryQueue` slice is unbounded. A high volume of submissions will consume memory indefinitely.
   - Verification step: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test).

6. **Sequential Component in ID Generation (`internal/coordinator/service.go:24-30`)** (SEC-013):
   - `generateID()` combines an atomic sequence counter with 6 random bytes (`job_<counter>_<random>`). While the 48 bits of randomness prevent brute-force guessing, the sequence counter reveals the total volume of jobs created since coordinator startup.
   - Verification step: `TestCoordinator_IDLeakage` (future test).

7. **Worker Client Unbounded Execution Timeout (`internal/worker/client.go:267-270`)** (SEC-012):
   - Worker client sets context timeout directly from coordinator's `TimeoutSeconds`. It enforces a minimum fallback (`<= 0 -> 30s`), but does not clamp an upper bound.
   - Verification step: `TestWorker_ExcessiveTimeoutClamped` (future test).

8. **Unvalidated Numeric Boundaries in Job Submission (`internal/api/handlers.go:138-158`, `internal/coordinator/service.go:119-125`)** (SEC-014, Fixed in Slice 3d):
   - `SubmitJobRequest` validates `max_attempts` (1..20, default 3) and `timeout_seconds` (1..86400, default 60) at API and coordinator boundary with typed errors returning HTTP 400. PostgreSQL check constraints (`chk_jobs_max_attempts`, `chk_jobs_timeout`) mapped to `domain.ErrInvalidRequest`.
   - Verified by `TestAPI_SubmitJob_BoundaryValues`, `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`), and `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`).

9. **Unbounded Slots Upper Bound in Worker Registration (`internal/coordinator/service.go:253-255`)** (SEC-015):
   - Worker registration now validates `TotalSlots` in `[1, 1024]` with HTTP 400 client rejection, backed by PostgreSQL `chk_workers_total_slots_range`.
   - Verified by `TestAPI_RegisterWorker_SlotsUpperLimit` (`internal/api/api_test.go:811`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), and `TestSchemaInvariants_SQLViolations/WorkerSlotsRange_RejectedByCheckConstraints` (`internal/storage/postgres/migrator_test.go:481`).

10. **Unbounded Version String Length in Worker Registration (`internal/domain/worker.go:10`)** (SEC-016):
    - Worker version string is bounded to $\le 64$ characters (empty string explicitly permitted) with HTTP 400 rejection, backed by PostgreSQL `chk_workers_version_length` and SQLSTATE `22001` mapping.
    - Verified by `TestAPI_RegisterWorker_VersionLengthLimit` (`internal/api/api_test.go:866`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), and `TestSchemaInvariants_SQLViolations/WorkerVersionLength_RejectedByCheckConstraint` (`internal/storage/postgres/migrator_test.go:507`).

11. **Unbounded Slice and Unvalidated Task Types in Supported Tasks (`internal/domain/worker.go:14`)** (SEC-017):
    - Worker `SupportedTasks` array length is bounded to $\le 16$ items (empty array permitted), individual task names validated to `^[a-zA-Z0-9_-]+$` with length 1..64 chars, and unknown well-formed tasks accepted for rolling upgrades. Backed by PostgreSQL `chk_workers_supported_tasks_count`.
    - Verified by `TestAPI_RegisterWorker_SupportedTasksAllowlist` (`internal/api/api_test.go:921`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), and `TestSchemaInvariants_SQLViolations/WorkerSupportedTasksCount_RejectedByCheckConstraint` (`internal/storage/postgres/migrator_test.go:522`).

