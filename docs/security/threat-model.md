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
| | | | **Slowloris / Idle connection exhaustion**: Slow header write exhausts file descriptors. | `ReadTimeout: 5s`, `WriteTimeout: 10s`, `IdleTimeout: 60s` (`internal/api/server.go:42-44`). `ReadHeaderTimeout` unset. | inferred from code review (unconfirmed) |
| | | | **Unauthenticated API Access**: Anyone reaching port 8080 can submit/cancel jobs (SEC-004). | Coordinator binds to `127.0.0.1:8080` by default (`internal/config/config.go:43`). | accepted risk |
| | | | **Internal Error / Schema Leakage**: Unmapped errors return raw driver strings in HTTP 400 responses (SEC-007). | `mapDomainErrorToHTTP` maps domain errors; unmapped defaults to `err.Error()` (`internal/api/handlers.go:68,104`). | inferred from code review (unconfirmed) |
| **Coordinator (`internal/coordinator`)** | Job coordination state, dispatch logic, attempt sequencing | HTTP Handlers to Domain Services & Storage | **Stale Attempt / Rogue Worker Results**: Worker commits results for an expired lease or wrong worker ID (SEC-001). | Atomic check of worker ID and lease token; `domain.ErrWorkerMismatch` and `domain.ErrStaleAttempt`. Tested in `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`) and `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`). | covered by a named test |
| | | | **Predictable Job/Attempt IDs**: Attacker guesses IDs to cancel or commit jobs (SEC-013). | IDs combine atomic monotonic sequence + 6 cryptographically random bytes (`crypto/rand`) (`internal/coordinator/service.go:24-30`). | inferred from code review (unconfirmed) |
| | | | **Worker ID Spoofing / Malformed IDs**: Registration accepts unbounded length or control chars (SEC-008). | Worker ID checked only for `""` (`internal/coordinator/service.go:198`, `internal/api/handlers.go:182`). No regex or length bound. | inferred from code review (unconfirmed) |
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
| **Configuration & Secrets (`internal/config`, `deployments`)** | DB passwords, listening ports, connection URLs | Environment / Flags to Runtime | **Database Credential Leakage in CLI**: `-database-url` passed via CLI flag visible in `ps aux` (SEC-011). | Flag usage supported in `cmd/migrate/main.go:18-27`; `.env` file git-ignored. | inferred from code review (unconfirmed) |
| | | | **Compose Missing Fail-Fast Password (SEC-003)**: Compose file specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` without fail-fast form (`${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}`) (`deployments/docker-compose.yml:8`). | PostgreSQL bound to localhost (`127.0.0.1:5433:5432` in `deployments/docker-compose.yml:11`). Note: `docs/testing-postgres.md` previously showed a stale snippet with fallback credentials, since corrected. | accepted risk |
| **Logs & Metrics** | Audit trail, debugging logs | Internal loggers to stdout | **Lease Token / Credential Exposure**: Sensitive tokens printed to stdout. | Coordinator logger explicitly excludes lease tokens, payloads, and task outputs (`internal/coordinator/service.go`). | inferred from code review (unconfirmed) |
| **Redis Streams** *(not yet introduced)* | Job queue messages, at-least-once dispatch | Coordinator to Redis Broker | **Message Tampering / Stale Queue Delivery / Split-Brain**: Unacknowledged messages redelivered to stale workers. | Not yet introduced. Deferred to V2 (ADR-0004, ADR-0006). | designed-not-executed |
| **Docker Isolation** *(not yet introduced)* | Host OS containment, container runtime | Worker to Container Engine | **Container Escape / Host Resource Starvation**: Malicious task breaking out of container. | Not yet introduced. Deferred to V3 (ADR-0005). | designed-not-executed |

## 3. Read-Only Code Review Findings (Unconfirmed Items)

The following items were identified during the read-only static inspection of the existing codebase. In accordance with the honesty rules, they are labeled **inferred from code review (unconfirmed)** until demonstrated by an automated test:

1. **HTTP Server Header Timeouts (`internal/api/server.go:41-45`)** (SEC-009):
   - `ReadTimeout` is set to 5s, `WriteTimeout` to 10s, and `IdleTimeout` to 60s.
   - `ReadHeaderTimeout` is not explicitly initialized. While `ReadTimeout` provides an upper bound, omitting `ReadHeaderTimeout` allows slow-header clients to occupy connection slots for up to 5 seconds.
   - `MaxHeaderBytes` is unset, falling back to Go's default 1 MiB limit.
   - Verification step: `TestAPI_Slowloris_HeaderTimeout` (future integration test).

2. **Unvalidated Route Parameters and Worker IDs (`internal/coordinator/service.go:197-202`, `internal/api/handlers.go:182-194`)** (SEC-008):
   - `RegisterWorker` checks only `worker.ID == ""`. There is no character-set constraint (`^[a-zA-Z0-9_-]+$`) or length limit (e.g., 128 characters).
   - Route parameters `{id}` in job queries are forwarded directly without character filtering.
   - Verification step: `TestAPI_WorkerID_ValidationBounds` (future test).

3. **Storage Error Information Disclosure (`internal/api/handlers.go:39-70,103-104`)** (SEC-007):
   - `mapDomainErrorToHTTP` converts recognized domain errors into clean HTTP status codes.
   - Any unrecognized error (including PostgreSQL database constraint violations or driver connection errors) falls through to default: `writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())`. Once PostgreSQL is wired in slice 3d, this could disclose database table structures or internal error strings to API clients.
   - Verification step: `TestAPI_UnmappedError_InformationDisclosure` (future test).

4. **CLI Database URL in Process Arguments (`cmd/migrate/main.go:18-27`)** (SEC-011):
   - The migration CLI accepts the database URL via `-database-url` flag. On multi-user systems, command-line arguments are visible in `/proc` and `ps` output.
   - Verification step: `TestMigrateCLI_ProcessTableExposure` (future test).

5. **Unbounded In-Memory Collections (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`)** (SEC-010):
   - `InMemoryRepository` stores jobs, workers, and events in unbounded Go maps and slices without eviction or size caps. `InMemoryQueue` slice is unbounded. A high volume of submissions will consume memory indefinitely.
   - Verification step: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test).

6. **Sequential Component in ID Generation (`internal/coordinator/service.go:24-30`)** (SEC-013):
   - `generateID()` combines an atomic sequence counter with 6 random bytes (`job_<counter>_<random>`). While the 48 bits of randomness prevent brute-force guessing, the sequence counter reveals the total volume of jobs created since coordinator startup.
   - Verification step: `TestCoordinator_IDLeakage` (future test).

7. **Worker Client Unbounded Execution Timeout (`internal/worker/client.go:267-270`)** (SEC-012):
   - Worker client sets context timeout directly from coordinator's `TimeoutSeconds`. It enforces a minimum fallback (`<= 0 -> 30s`), but does not clamp an upper bound.
   - Verification step: `TestWorker_ExcessiveTimeoutClamped` (future test).
