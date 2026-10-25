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
| **HTTP API (`internal/api`)** | Coordinator availability, HTTP request handling, response data | Public/Local Network to Coordinator HTTP server | **Oversized request body (DoS)**: Attacker sends giant JSON to exhaust memory. | `http.MaxBytesReader(w, r.Body, 1048576)` (`internal/api/server.go:36`). Tested in `TestAPI_JobPayloadLimit` (`internal/api/handlers_test.go:42`). | covered by a named test |
| | | | **Slowloris / Idle connection exhaustion**: Slow header write exhausts file descriptors. | `ReadTimeout: 5s`, `WriteTimeout: 10s`, `IdleTimeout: 60s` (`internal/api/server.go:42-44`). `ReadHeaderTimeout` unset. | inferred from code review (unconfirmed) |
| | | | **Unauthenticated API Access**: Anyone reaching port 8080 can submit/cancel jobs (SEC-004). | Coordinator binds to `127.0.0.1:8080` by default (`internal/config/config.go:43`). | accepted risk |
| | | | **Internal Error / Schema Leakage**: Unmapped errors return raw driver strings in HTTP 400 responses. | `mapDomainErrorToHTTP` maps domain errors; unmapped defaults to `err.Error()` (`internal/api/handlers.go:68`). | inferred from code review (unconfirmed) |
| **Coordinator (`internal/coordinator`)** | Job coordination state, dispatch logic, attempt sequencing | HTTP Handlers to Domain Services & Storage | **Stale Attempt / Rogue Worker Results**: Worker commits results for an expired lease or wrong worker ID (SEC-001). | Atomic check of worker ID and lease token; `domain.ErrWorkerMismatch` and `domain.ErrStaleAttempt`. Tested in `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:215`) and `TestAPI_StaleAttemptRejection` (`internal/api/handlers_test.go:121`). | covered by a named test |
| | | | **Predictable Job/Attempt IDs**: Attacker guesses IDs to cancel or commit jobs. | IDs combine atomic monotonic sequence + 6 cryptographically random bytes (`crypto/rand`) (`internal/coordinator/service.go:24-30`). | inferred from code review (unconfirmed) |
| | | | **Worker ID Spoofing / Malformed IDs**: Registration accepts unbounded length or control chars. | Worker ID checked only for `""` (`internal/coordinator/service.go:198`). No regex or length bound. | inferred from code review (unconfirmed) |
| | | | **Unbounded In-Memory Growth (DoS)**: Monotonic map growth under load without rate limits. | None in `InMemoryRepository` (`internal/storage/inmemory.go`). | inferred from code review (unconfirmed) |
| **Worker Protocol (`internal/worker`)** | Attempt lease tokens, worker availability | Remote Worker process to Coordinator API | **Forged Attempt Commit**: Attacker crafts commit with guessed token. | Cryptographic UUIDv4 lease tokens verified atomically against database state on commit. | covered by a named test (`TestPostgresRepository_CommitAttempt_StaleAttemptRejection`) |
| | | | **Unauthenticated Worker Registration / Hijacking**: Impersonating workers to steal jobs. | Localhost binding only in V1 (`internal/config/config.go:43`). Full mTLS and auth deferred to V3 (SEC-004). | accepted risk |
| | | | **Coordinator Timeout Manipulation**: Malicious coordinator sends huge `TimeoutSeconds`. | Fallback timeout checked (`<= 0 -> 30s`), but max timeout unconstrained (`internal/worker/client.go:268`). | inferred from code review (unconfirmed) |
| **Scheduler (`internal/scheduler`)** | Dispatch fairness, worker assignment | Coordinator to Registered Worker pool | **Deadlock on Unhealthy Worker**: Dispatch assigning work to dead workers. | Worker health filtered via `HeartbeatTimeout`; reaper marks dead workers. Tested in `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:375`). | covered by a named test |
| | | | **Priority Starvation / Resource Exhaustion**: High-demand jobs starving normal jobs. | Round-robin scheduler baseline in V1. Resource-aware scheduling deferred to V2. | designed-not-executed |
| **Job State Machine (`internal/domain`)** | Job & Attempt state invariants, transition validity | Service layer to State Machine | **Illegal State Transitions**: Bypassing pending/running to force completion. | Centralized FSM transition validation (`internal/domain/state.go`). Tested in `TestStateTransitions_Valid` and `TestStateTransitions_Invalid` (`internal/domain/state_test.go:10-70`). | covered by a named test |
| | | | **Oversized Payloads & Outputs**: Jobs with massive payloads crashing workers. | Validated in domain: `MaxPayloadBytes` (64 KiB), `MaxOutputBytes` (64 KiB), `MaxRetriesLimit` (10) (`internal/domain/validation.go:13-17`). Tested in `TestValidateJobSubmissionRequest` (`internal/domain/validation_test.go:13`). | covered by a named test |
| **PostgreSQL Persistence (`internal/storage/postgres`)** | Durable job records, execution attempts, lease consistency | Repository to PostgreSQL Engine | **SQL Injection**: Untrusted input concatenated into raw queries. | Strictly parameterized SQL queries. Automated AST check enforces zero string concatenation in queries. Tested in `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:41`). | covered by a named test |
| | | | **Connection Pool Exhaustion**: High concurrency starving connection pool. | `pgxpool.Pool` bounded by config (`MaxConns` default 10). | inferred from code review (unconfirmed) |
| | | | **Dynamic SQL in Test Fixtures (SEC-006)**: Dynamic schema names in test helpers. | Test helper uses `fmt.Sprintf("CREATE SCHEMA %s;", schemaName)` with in-process random hex (`internal/storage/postgres/testhelper_test.go:55`). | inferred from code review (unconfirmed) |
| **Task Executor (`internal/executor`)** | Host Operating System security | Worker process to Host OS | **Arbitrary Remote Code Execution (RCE)**: Attacker submits shell commands as tasks. | Host command execution strictly forbidden in V1. Tasks restricted to in-process allowlist (`echo`, `sleep`). Tested in `TestRegistry_Execute_UnknownTask` (`internal/executor/registry_test.go:33`). | covered by a named test |
| **Configuration & Secrets (`internal/config`, `deployments`)** | DB passwords, listening ports, connection URLs | Environment / Flags to Runtime | **Database Credential Leakage in CLI**: `-database-url` passed via CLI flag visible in `ps aux` (`cmd/migrate/main.go:18`). | Flag usage documented; `.env` file git-ignored. | inferred from code review (unconfirmed) |
| | | | **Compose Missing Fail-Fast Password (SEC-003)**: Empty password fallback in compose file. | PostgreSQL bound to localhost (`127.0.0.1:5433:5432`) in `deployments/docker-compose.yml:11`. | accepted risk |
| **Logs & Metrics** | Audit trail, debugging logs | Internal loggers to stdout | **Lease Token / Credential Exposure**: Sensitive tokens printed to stdout. | Coordinator logger explicitly excludes lease tokens, payloads, and task outputs (`internal/coordinator/service.go`). | inferred from code review (unconfirmed) |
| **Redis Streams** *(not yet introduced)* | Job queue messages, at-least-once dispatch | Coordinator to Redis Broker | **Message Tampering / Stale Queue Delivery / Split-Brain**: Unacknowledged messages redelivered to stale workers. | Not yet introduced. Deferred to V2 (ADR-0004, ADR-0006). | designed-not-executed |
| **Docker Isolation** *(not yet introduced)* | Host OS containment, container runtime | Worker to Container Engine | **Container Escape / Host Resource Starvation**: Malicious task breaking out of container. | Not yet introduced. Deferred to V3 (ADR-0005). | designed-not-executed |

## 3. Read-Only Code Review Findings (Unconfirmed Items)

The following items were identified during the read-only static inspection of the existing codebase. In accordance with the honesty rules, they are labeled **inferred from code review (unconfirmed)** until demonstrated by an automated test:

1. **HTTP Server Header Timeouts (`internal/api/server.go:41-45`)**:
   - `ReadTimeout` is set to 5s, `WriteTimeout` to 10s, and `IdleTimeout` to 60s.
   - `ReadHeaderTimeout` is not explicitly initialized. While `ReadTimeout` provides an upper bound, omitting `ReadHeaderTimeout` allows slow-header clients to occupy connection slots for up to 5 seconds.
   - `MaxHeaderBytes` is unset, falling back to Go's default 1 MiB limit.

2. **Unvalidated Route Parameters and Worker IDs (`internal/coordinator/service.go:197-202`, `internal/api/handlers.go:140-145`)**:
   - `RegisterWorker` checks only `worker.ID == ""`. There is no character-set constraint (`^[a-zA-Z0-9_-]+$`) or length limit (e.g., 128 characters).
   - Route parameters `{id}` in job queries are forwarded directly without character filtering.

3. **Storage Error Information Disclosure (`internal/api/handlers.go:39-70`)**:
   - `mapDomainErrorToHTTP` converts recognized domain errors into clean HTTP status codes.
   - Any unrecognized error (including PostgreSQL database constraint violations or driver connection errors) falls through to default: `writeJSON(w, http.StatusBadRequest, ErrorResponse{Code: "INVALID_REQUEST", Message: err.Error()})`. This could disclose database table structures or internal error strings to API clients.

4. **CLI Database URL in Process Arguments (`cmd/migrate/main.go:18-27`)**:
   - The migration CLI accepts the database URL via `-database-url` flag. On multi-user systems, command-line arguments are visible in `/proc` and `ps` output.

5. **Unbounded In-Memory Collections (`internal/storage/inmemory.go`, `internal/queue/inmemory.go`)**:
   - `InMemoryRepository` stores jobs, workers, and events in unbounded Go maps and slices without eviction or size caps. A high volume of submissions will consume memory indefinitely.

6. **Sequential Component in ID Generation (`internal/coordinator/service.go:24-30`)**:
   - `generateID()` combines an atomic sequence counter with 6 random bytes (`job-<counter>-<random>`). While the 48 bits of randomness prevent brute-force guessing, the sequence counter reveals the total volume of jobs created since coordinator startup.
