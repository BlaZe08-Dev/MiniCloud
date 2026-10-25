# Security Findings Register

This document tracks security and adversarial testing findings for MiniCloud. Every finding is assigned a unique identifier (`SEC-xxx`), categorized by severity, and evaluated against the evidence rules in [security-testing.md](security-testing.md).

For findings of **Medium** severity and above, the full structured report format is required. For **Low** and **Informational** findings, entries are summarized in the register table.

---

## Findings Summary Register

| ID | Severity | Status | Affected Component | Summary | Regression Test / Verification Step |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **SEC-001** | Medium | Confirmed, Fixed | Coordinator Logging | Worker mismatch log misattributed assigned worker ID to the reporting worker. | `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:210`), `TestAPI_StaleAttemptRejection` (`internal/api/api_test.go:104`) |
| **SEC-002** | Low | Confirmed, Fixed | Dispatch Precedence | In-memory repository checked worker health before job state, causing dead jobs to be released back to queue. | `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`), Conformance Suite (`internal/storage/storagetest/conformance.go:64`) |
| **SEC-003** | Informational | Confirmed, Fixed (Slice 3d) | Docker Compose | Compose `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` lacked fail-fast syntax `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` (`deployments/docker-compose.yml:8`), causing empty password warning. Confirmed by execution. | Fixed via fail-fast `${POSTGRES_PASSWORD:?...}` and `--env-file .env` in Makefile and integration test helpers. |
| **SEC-004** | Medium | Accepted Risk (V1) | HTTP API & Workers | HTTP API and worker endpoints lack authentication and authorization; open to local network. | Deferred to V3 (ADR-0004, ADR-0012); mitigated by `127.0.0.1` bind default. Verification step: future V3 mTLS/auth suite. |
| **SEC-005** | Low | Confirmed, Fixed | AST SQL Checker | Initial AST check scanned only `repository.go` and accepted identifier variables; coverage was overstated. | `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`) |
| **SEC-006** | Informational | Open (inferred from code review, unconfirmed) | Test Helpers | Test helper builds `CREATE SCHEMA` / `DROP SCHEMA` with `fmt.Sprintf` from in-process generated hex string (`internal/storage/postgres/testhelper_test.go:52-61`). | Verification step: add schema name validation against `^[a-z0-9_]+$` and quote via `pgx.Identifier`. |
| **SEC-007** | Low | Fixed (Slice 3d) | HTTP API Error Handling | Unmapped errors mapped to generic HTTP 500 `INTERNAL_ERROR` with opaque message; credentials/passwords scrubbed in server logs; malformed JSON returns fixed message (`internal/api/handlers.go:19-75`). | `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`, `TestAPI_OutageMapping_503RetryAfterOpaqueBody`, `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`) |
| **SEC-008** | Low | Fixed (Slice 3d) | Coordinator & API Validation | Worker IDs and route parameters strictly bounded ($\le 128$ chars) and validated against `^[a-zA-Z0-9_-]+$` at API boundary (`internal/api/handlers.go:27-39,105-248`). | `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`) |
| **SEC-009** | Low | Fixed (Slice 3d) | HTTP Server Timeouts | HTTP server enforces explicit `ReadHeaderTimeout` (2s) mitigating Slowloris delays (`internal/api/server.go:15,42`). | `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`) |
| **SEC-010** | Low | Open (inferred from code review, unconfirmed) | In-Memory Collections | Unbounded in-memory collections in `InMemoryRepository` (maps/events) and `InMemoryQueue` (slice) lack size caps and eviction (`internal/storage/inmemory.go:19-25`, `internal/queue/inmemory.go:18-24`). | Verification step: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test submitting high volume of jobs asserting memory ceiling or backpressure). |
| **SEC-011** | Informational | Fixed (Slice 3d) | Migration CLI | Migration CLI prioritizes `MINICLOUD_DATABASE_URL` environment variable; `-database-url` flag emits deprecation warning about process table exposure (`cmd/migrate/main.go:16-33`). | `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`, `TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`, `TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`) |
| **SEC-012** | Informational | Open (inferred from code review, unconfirmed) | Worker Client | Worker execution timeout is not clamped above from coordinator `TimeoutSeconds` (`internal/worker/client.go:267-268`). | Verification step: `TestWorker_ExcessiveTimeoutClamped` (future test returning 24-hour timeout from coordinator asserting worker clamps execution context to maximum threshold). |
| **SEC-013** | Informational | Open (inferred from code review, unconfirmed) | Coordinator ID Generation | Job and attempt ID format (`prefix_counter_random`) exposes monotonic sequence counter, leaking total job submission volume (`internal/coordinator/service.go:26`). | Verification step: `TestCoordinator_IDLeakage` (future test verifying whether sequence counters are exposed or replaced with opaque cryptographically random identifiers). |
| **SEC-014** | Medium/Low | Confirmed, Fixed (Slice 3d) | Coordinator & API Boundary | `POST /v1/jobs` accepted negative or zero `max_attempts` and `timeout_seconds` returning 201 in memory mode and violating PostgreSQL check constraints (`internal/coordinator/service.go:119-125`, `internal/api/handlers.go:138-158`, `internal/storage/postgres/repository.go:1080-1085`). | `TestAPI_SubmitJob_BoundaryValues`, `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:476-608`), `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809-883`) |
| **SEC-015** | Low | Confirmed, Fixed (Slice 3d) | Worker Registration | `total_slots` is bounded to `[1, 1024]` at domain boundary and backed by database constraint `chk_workers_total_slots_range` (`internal/coordinator/service.go:127,256`, migration 000003). | `TestAPI_RegisterWorker_SlotsUpperLimit` (`internal/api/api_test.go:811`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`) |
| **SEC-016** | Low | Confirmed, Fixed (Slice 3d) | Worker Registration | `version` string length capped at 64 chars at domain boundary and backed by database constraint `chk_workers_version_length` (`internal/coordinator/service.go:129,259`, migration 000003). Empty version allowed. | `TestAPI_RegisterWorker_VersionLengthLimit` (`internal/api/api_test.go:866`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`) |
| **SEC-017** | Low | Confirmed, Fixed (Slice 3d) | Worker Registration | `supported_tasks` slice bounded to $\le 16$ unique items (duplicates deduplicated), individual task names bounded to 64 chars `^[a-z0-9_]{1,64}$`, backed by `chk_workers_supported_tasks_count` (`internal/coordinator/service.go:130,262`, migration 000003). | `TestAPI_RegisterWorker_SupportedTasksAllowlist` (`internal/api/api_test.go:921`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`) |

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

### SEC-014: POST /v1/jobs Accepts Negative or Out-of-Bound Numeric Parameters (max_attempts, timeout_seconds)

- **Finding**: `POST /v1/jobs` accepted `max_attempts = -5` and `timeout_seconds = -1` and returned `201 Created` in memory mode (observed by curl probe against live coordinator).
- **Severity**: Medium/Low
- **Affected Component**: Coordinator & API Handlers (`internal/coordinator/service.go:119-125`, `internal/api/handlers.go:138-158`, `internal/storage/postgres/repository.go:1080-1085`)
- **Preconditions**: Unauthenticated HTTP caller submits job creation payload with negative, zero, or out-of-bounds numeric fields.
- **Reproduction Steps**:
  1. Start coordinator in memory mode.
  2. Issue HTTP POST:
     ```bash
     curl -i -X POST http://127.0.0.1:8080/v1/jobs \
       -H "Content-Type: application/json" \
       -d '{"task_type":"echo","payload":{"message":"test"},"max_attempts":-5,"timeout_seconds":-1}'
     ```
  3. Observe HTTP `201 Created` with job created having `max_attempts: 3` and `timeout_seconds: 60` due to silent internal override.
  4. In PostgreSQL mode, issue the same request: observe unmapped SQLSTATE `23514` check constraint violation returned as an opaque HTTP 500 `INTERNAL_ERROR` instead of HTTP 400 `INVALID_REQUEST`.
- **Evidence**: Probe observation confirmed negative values were accepted in memory mode returning 201 Created. Static inspection showed coordinator used `if maxAttempts <= 0 { maxAttempts = 3 }`, silently masking invalid values while PostgreSQL enforced `chk_jobs_max_attempts` ($1..20$) and `chk_jobs_timeout` ($1..86400$), turning invalid client requests into HTTP 500 internal errors.
- **Impact**: Invalid values reach the retry logic planned for Phase 4. Under PostgreSQL storage, check constraint violations surfaced as opaque 500 internal server errors rather than actionable 400 client errors.
- **Root Cause**: The coordinator validated payload byte size and task type but did not validate numeric parameter boundaries, silently overriding non-positive numbers with defaults in memory mode while leaving upper bounds unvalidated until the database rejected them. Additionally, API request struct `SubmitJobRequest` used plain `int` rather than pointers (`*int`), preventing clean distinction between absent/null values (where defaults apply) and explicit zero or negative values.
- **Recommended Fix**:
  1. Validate `max_attempts` ($1..20$, default 3 when omitted or null) and `timeout_seconds` ($1..86400$, default 60 when omitted or null) at API and coordinator boundaries with typed `domain.ErrInvalidRequest` mapped to HTTP 400 `INVALID_REQUEST`.
  2. Map PostgreSQL check constraint violations (`chk_jobs_max_attempts` and `chk_jobs_timeout` under SQLSTATE `23514`) directly to typed `domain.ErrInvalidRequest` so check violations always map to HTTP 400 even if coordinator checks are bypassed.
- **Regression Tests**:
  - `TestAPI_SubmitJob_BoundaryValues` (`internal/api/api_test.go:476`)
  - `TestAPI_PostgresCheckConstraint_MapsToClientErrorNever500` (`internal/api/api_test.go:567`)
  - `TestPostgresRepository_Security_CheckConstraints_NumericBounds` (`internal/storage/postgres/security_test.go:809`)
- **Status**: Confirmed, Fixed (Slice 3d).

---

## Low and Informational Findings Notes

### SEC-002: Dispatch Precedence Discrepancy (In-Memory vs. Postgres)
- **Status**: Confirmed, Fixed (Slice 3c).
- **Detail**: In-memory repository checked worker health prior to checking job queue state during claim. If a worker became unhealthy while a dead/stale job was dequeued, the in-memory engine returned an error that caused the coordinator to release the dead job back to the queue instead of dropping and acknowledging it. Fixed in slice 3c to align in-memory precedence with PostgreSQL.
- **Regression Test**: `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:458`), plus shared repository conformance tests (`internal/storage/storagetest/conformance.go:64`).

### SEC-003: Compose POSTGRES_PASSWORD Missing Fail-Fast Expansion
- **Status**: Informational, Confirmed, Fixed (Slice 3d).
- **Detail**: Confirmed by execution: `env -u POSTGRES_PASSWORD docker compose -f deployments/docker-compose.yml ps` emitted warning `The "POSTGRES_PASSWORD" variable is not set. Defaulting to a blank string.` and proceeded. Fixed by updating `deployments/docker-compose.yml:8` to `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` and passing `--env-file .env` explicitly in `Makefile` and integration test helpers. Execution without `.env` now fails fast with `required variable POSTGRES_PASSWORD is missing a value: set POSTGRES_PASSWORD in .env`. Note: `docs/testing-postgres.md` previously showed a stale snippet with fallback credentials, since corrected.
- **Verification Step**: Run `env -u POSTGRES_PASSWORD docker compose -f deployments/docker-compose.yml ps` without `--env-file` (assert exit code 1 with error message); run `make db-up`, `make db-down`, and integration tests with `--env-file .env` (assert clean exit code 0).

### SEC-005: AST SQL Concatenation Checker Coverage Overstatement
- **Status**: Confirmed, Fixed (Slice 3c follow-up).
- **Detail**: Process finding. Initial implementation of `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` parsed only `repository.go`, covered only `Exec/Query/QueryRow`, and treated all AST identifier variables as safe. A variable constructed via `fmt.Sprintf` would pass. Fixed in slice 3c follow-up by scanning all non-test `.go` files in `internal/storage/postgres`, resolving identifiers to `const` declarations, and adding negative fixtures. Residual limit: SQL assembled across helper function boundaries returning dynamic strings is not detected.
- **Regression Test**: `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` (`internal/storage/postgres/security_test.go:569`).

### SEC-006: In-Process Random Schema Name Assembly in Test Helpers
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/storage/postgres/testhelper_test.go:52-61` uses `fmt.Sprintf("CREATE SCHEMA %s;", schemaName)`. This is safe because `schemaName` is generated in-process from random hex (`fmt.Sprintf("test_repo_%s", hex.EncodeToString(b))`), but dynamic SQL in test fixtures sets an inconsistent pattern. Recommended: validate generated name against `^[a-z0-9_]+$` before execution and sanitize using `pgx.Identifier{schemaName}.Sanitize()`.
- **Verification Step**: Run AST or regex scan over test helper schema creation statements.

### SEC-007: Unmapped Errors Fall Through to HTTP 400 with Internal Error String
- **Status**: Low, Fixed (Slice 3d).
- **Detail**: In `internal/api/handlers.go:19-75`, `mapDomainErrorToHTTP` now routes all unmapped errors to HTTP 500 `INTERNAL_ERROR` with an opaque `"internal server error"` message. Server-side structured logs sanitize connection URIs and credential parameters (`password=***`). Furthermore, JSON decoding errors in `HandleSubmitJob`, `HandleRegisterWorker`, `HandleWorkerHeartbeat`, and `HandleReportResult` return a fixed `"malformed JSON request body"` message, preventing decoder internals or input offsets from reaching clients.
- **Regression Test**: `TestAPI_UnmappedError_SimulatedDriverError_SanitizedInternalError500`, `TestAPI_OutageMapping_503RetryAfterOpaqueBody`, `TestAPI_SubmitJob_MalformedJSON_FixedMessage` (`internal/api/api_test.go:212-321`).

### SEC-008: No Character Set or Length Validation on Worker IDs and Route Parameters
- **Status**: Low, Fixed (Slice 3d).
- **Detail**: In `internal/api/handlers.go:27-39`, `isValidIdentifier` enforces that all worker IDs, route IDs (`{id}`, `{attempt_id}`), and report request worker IDs are bounded ($\le 128$ characters) and strictly match `^[a-zA-Z0-9_-]+$`. Requests with 100 KiB identifiers, control characters, newlines, or path traversal strings (`../../`) are rejected at the API boundary with HTTP 400 `INVALID_REQUEST`.
- **Regression Test**: `TestAPI_IdentifierValidation_NegativeCases` (`internal/api/api_test.go:323-412`).

### SEC-009: HTTP Server ReadHeaderTimeout Unset
- **Status**: Low, Fixed (Slice 3d).
- **Detail**: In `internal/api/server.go:15,42`, `DefaultReadHeaderTimeout` is explicitly configured to `2 * time.Second` on `http.Server`. Connections trickling request headers beyond the 2-second timeout window are aborted by the server.
- **Regression Test**: `TestAPI_Slowloris_ReadHeaderTimeout` (`internal/api/api_test.go:414-473`).

### SEC-010: Unbounded In-Memory Collections under Sustained Load
- **Status**: Low, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/storage/inmemory.go:19-25` retains all jobs, attempts, workers, and events in Go maps and slices without eviction or size caps. `internal/queue/inmemory.go:18-24` maintains an unbounded slice. A high volume of submissions will consume heap memory indefinitely.
- **Verification Step**: `TestInMemoryRepository_MemoryExhaustion_Cap` (future test simulating 100,000 job submissions asserting memory cap or backpressure rejection).

### SEC-011: Database URL Accepted as CLI Argument in Migration Binary
- **Status**: Informational, Fixed (Slice 3d).
- **Detail**: In `cmd/migrate/main.go:16-33`, the migration CLI prioritizes the `MINICLOUD_DATABASE_URL` environment variable. The `-database-url` flag is marked deprecated and emits an explicit warning stating that passing credentials via command-line flags exposes them in the system process table.
- **Regression Test**: `TestMigrateCLI_ResolveDatabaseURL_EnvironmentVariable`, `TestMigrateCLI_ResolveDatabaseURL_DeprecatedFlagWarning`, `TestMigrateCLI_ResolveDatabaseURL_MissingURL` (`cmd/migrate/main_test.go:8-61`).

### SEC-012: Worker Execution Timeout Not Clamped Above
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/worker/client.go:267-270` directly converts `assignment.TimeoutSeconds` into a `time.Duration` without an upper bound clamp. If a compromised coordinator provides an enormous timeout, worker execution context will remain held until process termination.
- **Verification Step**: `TestWorker_ExcessiveTimeoutClamped` (future test returning a 24-hour timeout from coordinator asserting worker clamps execution context to maximum threshold).

### SEC-013: Monotonic Sequence Counter Exposed in Job and Attempt IDs
- **Status**: Informational, Open (inferred from code review, unconfirmed).
- **Detail**: `internal/coordinator/service.go:24-30` constructs IDs using `fmt.Sprintf("%s_%d_%s", prefix, seq, hex.EncodeToString(b))`. While the 48 bits of randomness prevent brute-force guessing, the sequence counter leaks total job submission volume since coordinator boot.
- **Verification Step**: `TestCoordinator_IDLeakage` (future test asserting whether sequential counters are exposed or replaced with opaque cryptographically random identifiers).

### SEC-015: Unbounded total_slots Upper Bound in Worker Registration
- **Status**: Low, Confirmed, Fixed (Slice 3d).
- **Detail**: In `POST /v1/workers/register` (`internal/coordinator/service.go:253-255`), `worker.TotalSlots` is validated to ensure it falls within `[1, 1024]`. Requests with `total_slots < 1` or `total_slots > 1024` return HTTP 400 `INVALID_REQUEST`. As defense-in-depth, PostgreSQL migration 000003 enforces `chk_workers_total_slots_range CHECK (total_slots >= 1 AND total_slots <= 1024)`.
- **Regression Test**: `TestAPI_RegisterWorker_SlotsUpperLimit` (`internal/api/api_test.go:811`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), `TestSchemaInvariants_SQLViolations/WorkerSlotsRange_RejectedByCheckConstraints` (`internal/storage/postgres/migrator_test.go:481`).

### SEC-016: Unbounded version String Length in Worker Registration
- **Status**: Low, Confirmed, Fixed (Slice 3d).
- **Detail**: In `POST /v1/workers/register` (`internal/coordinator/service.go:256-258`), `worker.Version` length is bounded to at most 64 characters (`len(worker.Version) <= 64`). Empty version strings (`""`) are explicitly allowed (defaulting to unversioned worker). As defense-in-depth, PostgreSQL migration 000003 enforces `chk_workers_version_length CHECK (length(version) <= 64)` and SQLSTATE `22001` (string truncation) is mapped to HTTP 400 `INVALID_REQUEST`.
- **Regression Test**: `TestAPI_RegisterWorker_VersionLengthLimit` (`internal/api/api_test.go:866`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), `TestSchemaInvariants_SQLViolations/WorkerVersionLength_RejectedByCheckConstraint` (`internal/storage/postgres/migrator_test.go:507`).

### SEC-017: Unbounded Slice and Unvalidated Task Types in supported_tasks
- **Status**: Low, Confirmed, Fixed (Slice 3d).
- **Detail**: In `POST /v1/workers/register` (`internal/coordinator/service.go:259-267`), `worker.SupportedTasks` array length is bounded to at most 16 unique tasks (duplicates are deduplicated). Individual task names are validated to ensure they are 1..64 characters in length and match `^[a-z0-9_]{1,64}$`. Empty slices are allowed (worker registers with 0 tasks). Unknown-but-well-formed task names are accepted upon registration to support rolling worker fleet upgrades. As defense-in-depth, PostgreSQL migration 000003 enforces `chk_workers_supported_tasks_count CHECK (coalesce(cardinality(supported_tasks), 0) <= 16)`.
- **Regression Test**: `TestAPI_RegisterWorker_SupportedTasksAllowlist` (`internal/api/api_test.go:921`), `TestPostgresRepository_Security_WorkerCheckConstraints` (`internal/storage/postgres/security_test.go:888`), `TestSchemaInvariants_SQLViolations/WorkerSupportedTasksCount_RejectedByCheckConstraint` (`internal/storage/postgres/migrator_test.go:522`).

