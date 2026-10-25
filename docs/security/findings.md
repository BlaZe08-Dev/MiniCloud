# Security Findings Register

This document tracks security and adversarial testing findings for MiniCloud. Every finding is assigned a unique identifier (`SEC-xxx`), categorized by severity, and evaluated against the evidence rules in [security-testing.md](security-testing.md).

For findings of **Medium** severity and above, the full structured report format is required. For **Low** and **Informational** findings, entries are summarized in the register table.

---

## Findings Summary Register

| ID | Severity | Status | Affected Component | Summary | Regression Test / Verification Step |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **SEC-001** | Medium | Confirmed, Fixed | Coordinator Logging | Worker mismatch log misattributed assigned worker ID to the reporting worker. | `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`), `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`) |
| **SEC-002** | Low | Confirmed, Fixed | Dispatch Precedence | In-memory repository checked worker health before job state, causing dead jobs to be released back to queue. | `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`), Conformance Suite (`internal/storage/storagetest/conformance.go:64`) |
| **SEC-003** | Informational | Open (inferred from code review, unconfirmed) | Docker Compose | Compose `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` has no fail-fast syntax `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` (`deployments/docker-compose.yml:8`). Stale snippet in `docs/testing-postgres.md` previously showed fallback credentials, since corrected. | Verification step: inspect `deployments/docker-compose.yml:8` and test compose parser failure behavior. |
| **SEC-004** | Medium | Accepted Risk (V1) | HTTP API & Workers | HTTP API and worker endpoints lack authentication and authorization; open to local network. | Deferred to V3 (ADR-0004, ADR-0012); mitigated by `127.0.0.1` bind default. Verification step: future V3 mTLS/auth suite. |
| **SEC-005** | Low | Confirmed, Fixed | AST SQL Checker | Initial AST check scanned only `repository.go` and accepted identifier variables; coverage was overstated. | `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`) |
| **SEC-006** | Informational | Open (inferred from code review, unconfirmed) | Test Helpers | Test helper builds `CREATE SCHEMA` / `DROP SCHEMA` with `fmt.Sprintf` from in-process generated hex string (`internal/storage/postgres/testhelper_test.go:52-61`). | Verification step: add schema name validation against `^[a-z0-9_]+$` and quote via `pgx.Identifier`. |
| **SEC-007** | Low | Open (inferred from code review, unconfirmed) | HTTP API Error Handling | Unmapped errors fall through to HTTP 400 with `err.Error()` in API error mapper (`internal/api/handlers.go:68,104`). Becomes critical once Postgres is wired in slice 3d, because raw driver errors could reach clients. | Verification step: `TestAPI_UnmappedError_InformationDisclosure` (future test asserting unmapped storage errors return generic HTTP 500 without leaking SQL text). |
| **SEC-008** | Low | Open (inferred from code review, unconfirmed) | Coordinator & API Validation | Worker IDs and route parameters lack length and character set validation (`internal/coordinator/service.go:198`, `internal/api/handlers.go:182`). | Verification step: `TestAPI_WorkerID_ValidationBounds` (future test sending 100 KiB worker ID, newlines, and path traversal strings to `POST /v1/workers/register` asserting rejection). |
| **SEC-009** | Low | Open (inferred from code review, unconfirmed) | HTTP Server Timeouts | HTTP server `ReadHeaderTimeout` unset (`internal/api/server.go:42-45`), exposing server to slowloris header delays up to 5s. | Verification step: `TestAPI_Slowloris_HeaderTimeout` (future integration test asserting slow header trickling closes connection within configured header timeout). |
| **SEC-010** | Low | Open (inferred from code review, unconfirmed) | In-Memory Collections | Unbounded in-memory collections in `InMemoryRepository` (maps/events) and `InMemoryQueue` (slice) lack size caps and eviction (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`). | Verification step: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test submitting high volume of jobs asserting memory ceiling or backpressure). |
| **SEC-011** | Informational | Open (inferred from code review, unconfirmed) | Migration CLI | Migration CLI accepts `-database-url` as command-line flag, visible in global process table (`cmd/migrate/main.go:18-27`). | Verification step: `TestMigrateCLI_ProcessTableExposure` (future test asserting credential flag deprecation in favor of `MINICLOUD_DATABASE_URL`). |
| **SEC-012** | Informational | Open (inferred from code review, unconfirmed) | Worker Client | Worker execution timeout is not clamped above from coordinator `TimeoutSeconds` (`internal/worker/client.go:267-268`). | Verification step: `TestWorker_ExcessiveTimeoutClamped` (future test returning 24-hour timeout from coordinator asserting worker clamps execution context to maximum threshold). |
| **SEC-013** | Informational | Open (inferred from code review, unconfirmed) | Coordinator ID Generation | Job and attempt ID format (`prefix_counter_random`) exposes monotonic sequence counter, leaking total job submission volume (`internal/coordinator/service.go:26`). | Verification step: `TestCoordinator_IDLeakage` (future test verifying whether sequence counters are exposed or replaced with opaque cryptographically random identifiers). |

---

## Detailed Findings (Medium and Above)

### SEC-001: Worker Mismatch Log Misattributed Assigned Worker ID

- **Finding**: When a worker attempted to commit results for an attempt assigned to a different worker, the coordinator logged the reporting worker's ID as `assigned_worker`.
- **Severity**: Medium
- **Affected Component**: Coordinator Service (`internal/coordinator/service.go:230-245`)
- **Preconditions**: A rogue or misconfigured worker attempts to commit results for an attempt currently leased by another active worker.
- **Reproduction Steps**:
  1. Dispatch attempt for job $J$ to worker $W_1$.
  2. Issue a commit request for that attempt from worker $W_2$.
  3. Inspect coordinator service structured logs.
- **Evidence**: In slice 3c review, test log inspection revealed `assigned_worker=rogue-worker reporting_worker=rogue-worker`, failing to identify that the job was assigned to `worker-1`.
- **Impact**: Audit logs were misleading during incident response, masking the identity of the legitimate worker that held the lease and misdiagnosing concurrency conflicts.
- **Root Cause**: `internal/coordinator/service.go` passed the incoming request's `workerID` to both `assigned_worker` and `reporting_worker` log fields when handling `ErrWorkerMismatch`.
- **Recommended Fix**: Return a typed `domain.WorkerMismatchError` carrying the actual assigned worker ID from repository storage to the service, and log the true assigned worker while keeping the HTTP API response generic to avoid leaking worker IDs to callers.
- **Regression Test**: `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`), `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`).
- **Status**: Confirmed, Fixed (Slice 3c).

---

### SEC-004: Unauthenticated HTTP API and Worker Endpoints

- **Finding**: All coordinator HTTP endpoints (`GET /healthz`, `GET /readyz`, `POST /v1/jobs`, `GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/attempts`, `POST /v1/jobs/{id}/cancel`, `GET /v1/workers`, `POST /v1/workers/register`, `POST /v1/workers/{id}/heartbeat`, `POST /v1/workers/{id}/poll`, `POST /v1/jobs/{id}/attempts/{attempt_id}/result`) lack authentication and authorization.
- **Severity**: Medium
- **Affected Component**: HTTP API Server (`internal/api/server.go:52-66`)
- **Preconditions**: Network route accessibility to the coordinator listening port (port 8080).
- **Reproduction Steps**: (designed, not executed)
  1. Submit a job without authorization headers:
     ```bash
     curl -i -X POST http://127.0.0.1:8080/v1/jobs \
       -H "Content-Type: application/json" \
       -d '{"task_type":"echo","payload":{"message":"hello"}}'
     ```
  2. Register a worker with arbitrary ID and no credentials:
     ```bash
     curl -i -X POST http://127.0.0.1:8080/v1/workers/register \
       -H "Content-Type: application/json" \
       -d '{"id":"rogue-worker-1","version":"v0.1.0","total_slots":2,"supported_tasks":["echo"]}'
     ```
  3. Send worker heartbeat without authentication:
     ```bash
     curl -i -X POST http://127.0.0.1:8080/v1/workers/rogue-worker-1/heartbeat \
       -H "Content-Type: application/json" \
       -d '{"available_slots":2}'
     ```
  4. Poll for work as the unauthenticated worker:
     ```bash
     curl -i -X POST http://127.0.0.1:8080/v1/workers/rogue-worker-1/poll \
       -H "Content-Type: application/json"
     ```
- **Evidence**: Static inspection of `internal/api/server.go:50-67` confirms routes are registered on standard `http.ServeMux` without authentication middleware or token verification.
- **Impact**: Any actor who can send packets to the coordinator can submit arbitrary jobs, cancel active jobs, register fake workers, poll and starve work from legitimate workers, or submit forged task results (subject to lease token constraints).
- **Root Cause**: Architectural decision to defer authentication and TLS to Milestone V3 (ADR-0004) to keep V1 and V2 focused on core distributed state transitions, storage, and scheduling.
- **Recommended Fix**: For V1, strictly bind coordinator and database listeners to `127.0.0.1` by default (`internal/config/config.go:43`). In V3, introduce mutual TLS (mTLS) for worker communication and bearer token/API key authentication for client endpoints.
- **Regression Test**: Designed, not executed (planned for V3 authentication suite).
- **Status**: Accepted risk until V3 (documented in ADR-0004 and ADR-0012).

---

## Low and Informational Findings Notes

### SEC-002: Dispatch Precedence Discrepancy (In-Memory vs. Postgres)
- **Status**: Confirmed, Fixed (Slice 3c).
- **Detail**: In-memory repository checked worker health prior to checking job queue state during claim. If a worker became unhealthy while a dead/stale job was dequeued, the in-memory engine returned an error that caused the coordinator to release the dead job back to the queue instead of dropping and acknowledging it. Fixed in slice 3c to align in-memory precedence with PostgreSQL.
- **Regression Test**: `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`), plus shared repository conformance tests (`internal/storage/storagetest/conformance.go:64`).

### SEC-003: Compose POSTGRES_PASSWORD Missing Fail-Fast Expansion
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `deployments/docker-compose.yml:8` specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` without fail-fast variable expansion syntax. If `POSTGRES_PASSWORD` is unset in the shell environment or `.env`, docker compose evaluates the variable as an empty string. Recommended: update to fail-fast form `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` to prevent accidental container startup with empty password. Note: `docs/testing-postgres.md` previously showed a stale snippet with fallback credentials, since corrected.
- **Verification Step**: Inspect `deployments/docker-compose.yml:8` and verify compose parser rejects startup when `POSTGRES_PASSWORD` is unset.

### SEC-005: AST SQL Concatenation Checker Coverage Overstatement
- **Status**: Confirmed, Fixed (Slice 3c follow-up).
- **Detail**: Process finding. Initial implementation of `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` parsed only `repository.go`, covered only `Exec/Query/QueryRow`, and treated all AST identifier variables as safe. A variable constructed via `fmt.Sprintf` would pass. Fixed in slice 3c follow-up by scanning all non-test `.go` files in `internal/storage/postgres`, resolving identifiers to `const` declarations, and adding negative fixtures. Residual limit: SQL assembled across helper function boundaries returning dynamic strings is not detected.
- **Regression Test**: `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`).

### SEC-006: In-Process Random Schema Name Assembly in Test Helpers
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/storage/postgres/testhelper_test.go:52-61` uses `fmt.Sprintf("CREATE SCHEMA %s;", schemaName)`. This is safe because `schemaName` is generated in-process from random hex (`fmt.Sprintf("test_repo_%s", hex.EncodeToString(b))`), but dynamic SQL in test fixtures sets an inconsistent pattern. Recommended: validate generated name against `^[a-z0-9_]+$` before execution and sanitize using `pgx.Identifier{schemaName}.Sanitize()`.
- **Verification Step**: Run AST or regex scan over test helper schema creation statements.

### SEC-007: Unmapped Errors Fall Through to HTTP 400 with Internal Error String
- **Status**: Low, Open (inferred from code review, unconfirmed).
- **Detail**: In `internal/api/handlers.go:68,104`, `mapDomainErrorToHTTP` defaults unrecognized errors to HTTP 400 `INVALID_REQUEST` with `err.Error()` passed directly to the JSON response. While currently operating against in-memory storage, once PostgreSQL persistence is wired to the coordinator in slice 3d, unmapped database driver errors, constraint names, or query fragments could be exposed directly to API callers. Recommended: map unhandled errors to HTTP 500 `INTERNAL_ERROR` with an opaque message while logging details internally.
- **Verification Step**: `TestAPI_UnmappedError_InformationDisclosure` (future test injecting an unmapped repository error and asserting response message is sanitized).

### SEC-008: No Character Set or Length Validation on Worker IDs and Route Parameters
- **Status**: Low, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/coordinator/service.go:197-202` and `internal/api/handlers.go:182-194` check only `worker.ID == ""`. There are no length bounds (e.g., max 128 characters) or character set whitelist regex checks (e.g., `^[a-zA-Z0-9_-]+$`). Similarly, route parameters `{id}` in job queries are forwarded directly without character filtering.
- **Verification Step**: `TestAPI_WorkerID_ValidationBounds` (future test attempting registration with 100 KiB worker ID, control characters, and newlines asserting HTTP 400 rejection).

### SEC-009: HTTP Server ReadHeaderTimeout Unset
- **Status**: Low, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/api/server.go:42-45` configures `ReadTimeout: 5s`, `WriteTimeout: 10s`, and `IdleTimeout: 60s`, but leaves `ReadHeaderTimeout` unset. While `ReadTimeout` provides a 5s upper bound, omitting an explicit `ReadHeaderTimeout` leaves the connection open to slow header trickling for up to 5 seconds per socket.
- **Verification Step**: `TestAPI_Slowloris_HeaderTimeout` (future integration test trickling headers at 1 byte/2sec asserting server closes connection within configured header timeout).

### SEC-010: Unbounded In-Memory Collections under Sustained Load
- **Status**: Low, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/storage/inmemory.go:19-25` retains all jobs, attempts, workers, and events in Go maps and slices without eviction or size caps. `internal/queue/inmemory.go:18-24` maintains an unbounded slice. A high volume of submissions will consume heap memory indefinitely.
- **Verification Step**: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test simulating 100,000 job submissions asserting memory cap or backpressure rejection).

### SEC-011: Database URL Accepted as CLI Argument in Migration Binary
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `cmd/migrate/main.go:18-27` accepts the database URL via `-database-url` command-line flag. On multi-tenant systems, command-line arguments are visible in global process tables (`ps aux`, `/proc`).
- **Verification Step**: `TestMigrateCLI_ProcessTableExposure` (future test verifying flag deprecation in favor of `MINICLOUD_DATABASE_URL`).

### SEC-012: Worker Execution Timeout Not Clamped Above
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/worker/client.go:267-270` directly converts `assignment.TimeoutSeconds` into a `time.Duration` without an upper bound clamp. If a compromised coordinator provides an enormous timeout, worker execution context will remain held until process termination.
- **Verification Step**: `TestWorker_ExcessiveTimeoutClamped` (future test returning a 24-hour timeout from coordinator asserting worker clamps execution context to maximum threshold).

### SEC-013: Monotonic Sequence Counter Exposed in Job and Attempt IDs
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/coordinator/service.go:24-30` constructs IDs using `fmt.Sprintf("%s_%d_%s", prefix, seq, hex.EncodeToString(b))`. While the 48 bits of randomness prevent brute-force guessing, the sequence counter leaks total job submission volume since coordinator boot.
- **Verification Step**: `TestCoordinator_IDLeakage` (future test asserting whether sequential counters are exposed or replaced with opaque cryptographically random identifiers).
