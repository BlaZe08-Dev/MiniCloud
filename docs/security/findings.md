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
| **SEC-018** | Informational | Confirmed, Remediated (Slice 3d) | Process / Reporting Integrity | Slice 3d follow-up report contained reconstructed test output, nonexistent test names, nonexistent ADR citation, and wrong supported_tasks limit (1..32 vs 16); remedied by mandatory file redirection (`> /tmp/<name>.log 2>&1`) and raw grep evidence. | Disclosed in SEC-018 detailed section below; all citations verified against `go test -list '.*' ./...` |
| **SEC-019** | Informational | Confirmed by mutation, Fixed (Slice 3d) | Test Quality / Pool Saturation Verification | `TestSecurity_PartD_PoolExhaustion` relied on a weak latency control (`waitedNearHold >= 10`) that passed even when the lock hold was removed. Replaced with primary admin `pg_stat_activity` Lock wait sampling (`>= pool_size - 2`) and secondary relative max latency margin over 3-run unlocked baseline median (`>= 0.25 * holdDuration`), verified by mutation. | `TestSecurity_PartD_PoolExhaustion` (`tests/integration/part_d_security_test.go:45`) |
| **SEC-020** | Low | Confirmed, Remediated by Tooling | Documentation Integrity / Citation Checker | Prior documentation and reports cited nonexistent test names, invalid repository file paths, and obsolete API routes without automated verification. Remediated by standard-library static citation checker (`TestCitations`, `TestFixtures`) and strict allowlist (`tests/docs/citation_allowlist.txt`). | `TestCitations` (`tests/docs/citations_test.go:695`), `TestFixtures` (`tests/docs/citations_test.go:754`) |
| **SEC-021** | Informational | Confirmed by owner mutation; remediated for unrelated names; residual limitation documented | Documentation Citation Checker / Rule C Exemption Scope | Rule C's exemption keywords (`proposed`, `future`, `planned`, `designed not executed`, `does not exist`, `nonexistent`, `invented`) initially exempted every test name on the line. Remediated by tightening exemption evaluation to individual table cells and sentence clauses (`[|;]|\. |\.\t|<br\s*/?>`) with identifier token stripping. Residual limitation documented: a fabricated name carrying its own label in the same clause passes by design; exemption is a convention requiring human review. | `TestFixtures/RuleC_SeparateClause_Fail`, `TestFixtures/RuleC_ProposedExemption_Pass` (`tests/docs/citations_test.go:857-888`) |
| **SEC-022** | Low | Open (confirmed by code reading, NOT executed) | Storage Layer (In-Memory & Postgres) | Duplicate CancelJob on CANCEL_REQUESTED moves job directly to CANCELLED via fallback branch, bypassing worker cooperative context and rejecting later worker commit. | `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) |

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

### SEC-018: Inaccuracies and Reconstructed Test Output in Slice 3d Progress Report
- **Finding**: The slice 3d follow-up progress report contained reconstructed test output with nonexistent test names, an invalid ADR citation, and an incorrect upper limit for worker supported tasks. Furthermore, an initial remediation attempt in the findings register contained a second-order failure by mapping some nonexistent names to other nonexistent names (`TestAPI_SubmitJob_Success`, `TestAPI_JobLifecycle`, `TestService_SubmitJob_Success`, `TestWorkerAgent_SingleJobSuccess`, `TestMigrator_AppliesAllMigrations`, `TestMigrator_ConcurrentRunners_AllMigrationsAppliedOnce`).
- **Severity**: Informational
- **Category**: Process / Reporting Integrity
- **Status**: Confirmed, Remediated (Slice 3d).
- **Preconditions**: Generating progress reports or remediation summaries from memory or inference rather than cross-checking against `go test -list '.*' ./...` and filesystem paths.
- **Evidence of Inaccuracies**:
  1. **Nonexistent test names cited in prior report and remediation**:
     - `TestAPI_JobLifecycle_InMemory` (does not exist (invented in an earlier report); actual real test asserting in-memory lifecycle: `TestInMemoryRepository_JobLifecycle` in `internal/storage/inmemory_test.go:15`)
     - `TestAPI_JobLifecycle` (does not exist (invented in an earlier report); initial remediation falsely listed as real name)
     - `TestAPI_SubmitJob_Success` (does not exist (invented in an earlier report); initial remediation falsely listed as real name)
     - `TestJob_Transitions` (does not exist (invented in an earlier report); actual real tests asserting transition matrix: `TestJob_TransitionTo` in `internal/domain/transition_test.go:79`, `TestJobTransitionMatrix_AllPairs` in `internal/domain/transition_test.go:10`)
     - `TestCoordinator_Service` (does not exist (invented in an earlier report); actual real tests asserting coordinator service behavior: `TestService_WorkerLiveness_ReaperAndRecovery` in `internal/coordinator/service_test.go:54`, `TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released` in `internal/coordinator/service_test.go:764`)
     - `TestService_SubmitJob_Success` (does not exist (invented in an earlier report); initial remediation falsely listed as real name)
     - `TestAgent_Run_SingleJobSuccess` (does not exist (invented in an earlier report); actual real integration/agent tests: `TestLocalCore_MultiProcessIntegration` in `tests/integration/local_core_test.go:25`, `TestAgent_Run_WorkerUnhealthy_KeepsPollingAndHeartbeating` in `internal/worker/client_test.go:114`)
     - `TestWorkerAgent_SingleJobSuccess` (does not exist (invented in an earlier report); initial remediation falsely listed as real name)
     - `TestAPI_WorkerRegistration_Limits` (does not exist (invented in an earlier report); actual real tests asserting worker registration limits: `TestAPI_RegisterWorker_SlotsUpperLimit` in `internal/api/api_test.go:811`, `TestAPI_RegisterWorker_VersionLengthLimit` in `internal/api/api_test.go:866`, `TestAPI_RegisterWorker_SupportedTasksAllowlist` in `internal/api/api_test.go:921`, `TestPostgresRepository_Security_WorkerCheckConstraints` in `internal/storage/postgres/security_test.go:888`)
     - `TestMigrator_ApplyAll_CleanDatabase` (does not exist (invented in an earlier report); actual real migration tests: `TestMigrator_FreshMigrate` in `internal/storage/postgres/migrator_test.go:281`, `TestMigrator_IdempotentReRun` in `internal/storage/postgres/migrator_test.go:329`)
     - `TestMigrator_AppliesAllMigrations` (does not exist (invented in an earlier report); initial remediation falsely listed as real name)
     - `TestMigrator_ConcurrentRunners_AllMigrationsAppliedOnce` (does not exist (invented in an earlier report); actual real test asserting serialized concurrent migrators: `TestMigrator_ConcurrentMigrators_ExactlyOneApplies` in `internal/storage/postgres/migrator_test.go:374`)
     - `TestAPI_ReadHeaderTimeout_SlowLorisProtection` (does not exist (invented in an earlier report); actual real test: `TestAPI_Slowloris_ReadHeaderTimeout` in `internal/api/api_test.go:414`)
     - `TestAPI_JobNumericBounds_Validation` (does not exist (invented in an earlier report); actual real test: `TestAPI_SubmitJob_BoundaryValues` in `internal/api/api_test.go:476`)
     - `TestAPI_WorkerID_ValidationBounds` (does not exist (invented in an earlier report); actual real test: `TestAPI_IdentifierValidation_NegativeCases` in `internal/api/api_test.go:323`)
     Verification: `go test -list '.*' ./...` verifies exactly 101 tests in the repository. Every real test cited above exists and was verified by opening the test implementation to confirm the assertions it makes.
  2. **Nonexistent ADR citation**: The report cited `docs/adr/0013-worker-registration-bounds-and-rejection-codes.md` (does not exist, nonexistent path cited in earlier report). The actual repository file is `docs/adr/0013-durable-dispatch-and-coordinator-recovery.md`.
  3. **Incorrect parameter limit**: The report claimed `supported_tasks` limit was `1..32`. The real limit enforced in domain validation and database constraint `chk_workers_supported_tasks_count` is `16` (`MaxWorkerSupportedTasks = 16`).
- **Second-Order Failure & Disclosure**:
  During the initial remediation of SEC-018, the remediation text itself introduced non-existent test names into the "real name" mappings (`TestAPI_SubmitJob_Success`, `TestAPI_JobLifecycle`, `TestService_SubmitJob_Success`, `TestWorkerAgent_SingleJobSuccess`, `TestMigrator_AppliesAllMigrations`) and claimed `TestMigrator_ConcurrentRunners_AllMigrationsAppliedOnce` was a verified real name when the actual test is `TestMigrator_ConcurrentMigrators_ExactlyOneApplies`. This second-order failure was discovered by the owner's verification scan comparing all cited names against `go test -list '.*' ./...`. Every non-existent name has been corrected, verified against active test source code, and explicitly labeled.
- **Impact**: Progress reports and audit registers failed to provide verifiable, reproducible audit records. Reconstructed outputs obscure test failures or skips.
- **Root Cause**: Summarizing test execution and mapping test names from model inference or memory instead of piping raw command output to disk and verifying with `go test -list '.*' ./...`.
- **Remediation & Enforced Protocol**:
  1. All test runs MUST be redirected to a temporary file (`> /tmp/<name>.log 2>&1`).
  2. Reports MUST paste directly from the redirected file (or its head/tail) alongside exact grep counts: `grep -cE '^--- (PASS|FAIL|SKIP)' /tmp/<name>.log`.
  3. No test output, test names, or logs may be summarized, retyped, or reconstructed.
  4. Standing rule: Every cited test name must come from `go test -list '.*' ./...` and every cited path must exist in the repository filesystem.

### SEC-019: Weak Positive Control in Pool Saturation Security Test
- **Finding**: In `TestSecurity_PartD_PoolExhaustion`, the positive control for pool saturation was a weak threshold asserting that at least 10 requests had latency $\ge 0.8 \times \text{holdDuration}$. Adversarial mutation testing revealed that commenting out `time.Sleep(holdDuration)` (releasing the table lock immediately) still passed the test because normal queuing delay of 500 requests contending for 10 connections resulted in >100 requests waiting >320ms even without lock contention.
- **Severity**: Informational
- **Category**: Test Quality / Verification Rigor
- **Status**: Confirmed by mutation, Fixed (Slice 3d).
- **Affected Component**: Integration Test Suite (`tests/integration/part_d_security_test.go:45`)
- **Remediation**:
  Replaced the weak count-based control with dual rigorous controls, both of which must hold:
  1. **Primary Proof (Database Lock-Wait Sampling)**: A background admin sampler queries `pg_stat_activity` every 25ms during the hold window filtered by the coordinator pool's unique `application_name = coord_pool_<schemaName>`, asserting the maximum number of connections observed with `wait_event_type = 'Lock'` is at least `pool_size - 2` (8 out of 10).
  2. **Secondary Proof (Relative Latency Margin)**: The test runs 3 baseline 500-request batches without the lock held to calculate the median max latency; the locked run's max latency must exceed the baseline median by at least `0.25 * holdDuration` (with requests pre-spawned on a start barrier and `holdDuration = 800ms`).
- **Mutation Verification**:
  - **Mutation (i)**: Commenting out `time.Sleep(holdDuration)` failed immediately on Primary Control (`observed maximum was 0 lock-waiters, threshold >= 8`).
  - **Mutation (ii)**: Setting `holdDuration = 1ms` failed immediately on Primary Control (`observed maximum was 0 lock-waiters, threshold >= 8`).
- **Regression Test**: `TestSecurity_PartD_PoolExhaustion` (`tests/integration/part_d_security_test.go:45`).

### SEC-020: Documentation Citation Drift and Nonexistent Citations
- **Finding**: Documentation files (`README.md`, `GEMINI.md`, `CHANGELOG.md`, `docs/**/*.md`) and progress reports accumulated citation drift over time, including absolute file URI schemes (`file:` triple slash), invalid relative paths to migrations (`migrations/` instead of `internal/storage/postgres/migrations/`), legacy API route prefixes (`/api` preceding `/v1`), and fabricated or nonexistent test names.
- **Severity**: Low
- **Category**: Documentation Integrity / Tooling
- **Status**: Confirmed, Remediated by Tooling (`docs/citation-check`).
- **Affected Component**: Documentation Verification Tooling (`tests/docs/citations_test.go`, `tests/docs/citation_allowlist.txt`, `Makefile: test-docs`).
- **Evidence**: Static AST-based and regex scanning revealed invalid file URI schemes in `README.md`, incorrect migration directory path in `docs/adr/0010-migrations-policy.md`, and 7 historical nonexistent test/path citations in `docs/security/findings.md`.
- **Root Cause**: Manual maintenance of documentation without automated test verification of repository paths, test identifiers, line anchors, ADR sequences, and API routes.
- **Remediation**:
  1. Standard library test `TestCitations` in `tests/docs/citations_test.go` enforcing Rules A–F:
     - Rule A: Repository paths must exist on disk.
     - Rule B: Line number anchors must be within bounds.
     - Rule C: Test function identifiers must exist in repository test suites parsed via `go/parser`.
     - Rule D: Forbidden local file URI schemes (`file:` triple slash) and legacy route prefixes (`/api` preceding `/v1`).
     - Rule E: ADR citations must reference valid ADR numbers with zero gap sequence.
     - Rule F: Allowlist (`tests/docs/citation_allowlist.txt`) format, non-empty reasons ($\ge 15$ chars), and strict non-staleness check.
  2. Integrated `make test-docs` target into Makefile and developer documentation.
- **Regression Test**: `TestCitations` (`tests/docs/citations_test.go:695`), `TestFixtures` (`tests/docs/citations_test.go:754`).

### SEC-021: Whole-Line Exemption Scoping in Documentation Citation Checker (Rule C)
- **Finding**: In the initial implementation of Rule C (`tests/docs/citations_test.go`), checking for exemption keywords (`proposed`, `future`, `planned`, `designed not executed`, `does not exist`, `nonexistent`, `invented`) was performed across the entire line. As a result, the presence of an exemption keyword anywhere on a line inadvertently exempted all test identifiers on that line, even fabricated names in unrelated clauses or table cells.
- **Severity**: Informational
- **Category**: Test Verification Rigor / Citation Scoping
- **Status**: Confirmed by owner mutation; remediated for unrelated names; residual limitation documented
- **Affected Component**: Documentation Citation Checker (`tests/docs/citations_test.go:478-534`)
- **Evidence (Executed Owner Mutation Suite)**:
  The owner executed an adversarial mutation suite in a throwaway worktree against `docs/security/test-plan.md:93` testing the tightened clause-scoping behavior across four cases:
  1. **Separate sentence**: Appending `"Verified by TestFab_One (nonexistent). This is planned."` failed with `[RuleC] cited test "TestFab_One" does not exist`.
  2. **Semicolon clause**: Appending `"Verified by TestFab_Two (nonexistent); coverage is planned."` failed with `[RuleC] cited test "TestFab_Two" does not exist`.
  3. **Different table cell**: Appending `"| TestFab_Three (nonexistent) | planned |"` failed with `[RuleC] cited test "TestFab_Three" does not exist`.
  4. **Same clause / self-labeled**: Appending `"Verified by TestFab_Four (planned)."` passed, confirming that attaching an exemption label to the identifier within the same clause permits it by design.
- **Root Cause**: Exemption regex matching was evaluated against the entire line buffer rather than scoping to the specific table cell or sentence clause attached to the identifier.
- **Remediation**:
  1. Tightened Rule C exemption scoping in `tests/docs/citations_test.go`:
     - Lines are segmented into clauses using delimiter regex `[|;]|\. |\.\t|<br\s*/?>`, cleanly isolating markdown table cells and distinct sentences.
     - Within each clause, test identifier names are stripped (`testIdentRe.ReplaceAllString(clause, "")`) before checking for exemption keywords, preventing test names containing substrings like `Nonexistent` from self-exempting.
     - An exemption keyword only exempts test names residing in the exact same clause or table cell.
  2. Added unit test fixtures in `TestFixtures`:
     - `RuleC_SeparateClause_Fail`: Asserts that `TestFabricated_Foo (nonexistent test); planned feature` in separate clauses fails because `TestFabricated_Foo` does not inherit `planned` across clause boundaries.
     - `RuleC_ProposedExemption_Pass`: Asserts that `TestLegitimate_Future (planned)` passes when the keyword is in the same clause.
- **Residual Limitation & Operational Policy**:
  A fabricated name that carries its own exemption label in the same clause still passes by design, because a label-based static exemption cannot distinguish a real future test from a fabricated one. The exemption is a documentation convention, not a proof of legitimacy; every "planned", "future", or "proposed" test name must be human-reviewed when a milestone slice claims coverage. The three genuine labeled future tests in use today are `TestCoordinator_IDLeakage` (future test), `TestInMemoryRepository_MemoryExhaustion_Cap` (future test), and `TestWorker_ExcessiveTimeoutClamped` (future test).
- **Regression Test**: `TestFixtures/RuleC_SeparateClause_Fail`, `TestFixtures/RuleC_ProposedExemption_Pass` (`tests/docs/citations_test.go:857-888`).

### SEC-022: Duplicate CancelJob on CANCEL_REQUESTED Prematurely Finalizes Job

- **Finding**: Calling `CancelJob` a second time on a job currently in `CANCEL_REQUESTED` moves it directly to terminal `CANCELLED`. This bypasses the cooperative worker cancellation protocol: the job is marked terminal in storage while the worker process is still executing the task. When the worker finishes and issues `CommitAttempt`, the commit is rejected as terminal (`JOB_ALREADY_TERMINAL`), wasting completed work.
- **Severity**: Low
- **Category**: State Transition Logic / Concurrency Defect
- **Status**: Open (confirmed by code reading, NOT executed)
- **Affected Component**: In-Memory and PostgreSQL Repositories (`internal/storage/inmemory.go:658-683`, `internal/storage/postgres/repository.go:1135-1215`)
- **Preconditions**: A job is in `RUNNING` state; a first `CancelJob` request transitions it to `CANCEL_REQUESTED`; while the worker is cooperatively executing or acknowledging cancellation, a second `CancelJob` request arrives.
- **Reproduction Steps**: (confirmed by code reading, NOT executed)
  1. Submit and dispatch a long-running job so it enters `RUNNING`.
  2. Issue a first cancellation request: `job.State` transitions to `CANCEL_REQUESTED`.
  3. Issue a second cancellation request for the same job before the worker acknowledges cancellation.
  4. Observe repository state: `CancelJob` evaluates the `else` fallback branch and transitions the job to `CANCELLED`.
  5. Worker completes task and issues `CommitAttempt`: rejected with HTTP 409 Conflict (`JOB_ALREADY_TERMINAL`).
- **Evidence**: Confirmed by code inspection of `internal/storage/inmemory.go:679-682` and `internal/storage/postgres/repository.go:1190-1193`. In both repositories, the cancellation state branching logic is:
  ```go
  if job.State == domain.StateQueued {
      targetState = domain.StateCancelled
      eventType = domain.JobEventCancelled
  } else if job.State == domain.StateRunning {
      targetState = domain.StateCancelRequested
      eventType = domain.JobEventCancelRequested
  } else {
      targetState = domain.StateCancelled
      eventType = domain.JobEventCancelled
  }
  ```
  Neither repository checks for `job.State == domain.StateCancelRequested`. Because `StateCancelRequested` is neither `StateQueued` nor `StateRunning`, execution enters the `else` fallback branch, setting `targetState = domain.StateCancelled`. In `internal/domain/transition.go:21`, the FSM explicitly permits `StateCancelRequested -> StateCancelled`, so the transition succeeds and terminates the job prematurely. (Confirmed by code reading, NOT executed).
- **Impact**: Premature termination of jobs during cooperative cancellation windows. Discards completed work if the worker produces a valid result before receiving the cancel signal, violating ADR-0016 Section 5 (Race 1: Cancellation vs Completion) and Race 3 (Duplicate Cancellation Requests).
- **Root Cause**: Fallback `else` branch treats every non-`QUEUED`, non-`RUNNING` active state as cancellable to `CANCELLED`, omitting explicit handling for `CANCEL_REQUESTED`.
- **Authorized Fix (Scheduled for Slice 4a)**:
  Update `internal/storage/inmemory.go` and `internal/storage/postgres/repository.go` to explicitly handle `job.State == domain.StateCancelRequested`:
  ```go
  if job.State == domain.StateQueued {
      targetState = domain.StateCancelled
      eventType = domain.JobEventCancelled
  } else if job.State == domain.StateRunning {
      targetState = domain.StateCancelRequested
      eventType = domain.JobEventCancelRequested
  } else if job.State == domain.StateCancelRequested {
      // Idempotent duplicate cancel: remain in CANCEL_REQUESTED with zero mutation
      return job, nil
  } else {
      targetState = domain.StateCancelled
      eventType = domain.JobEventCancelled
  }
  ```
  The HTTP API handler will return HTTP 202 `Accepted` with `state: "CANCEL_REQUESTED"`, `message: "cancellation already requested"`.
- **Regression Test**: `TestConformance_CancelJob_IdempotentCancelRequested` (planned test).

---

## Part D Security Testing Verification Summary

The Part D security verification tests were implemented and executed against PostgreSQL on `milestone/3d-postgres-wiring`:

1. **Genuine Pool Saturation (`TestSecurity_PartD_PoolExhaustion` in `tests/integration/part_d_security_test.go:45`)**:
   - 500 concurrent HTTP requests (167 `POST /v1/jobs`, 167 `GET /readyz`, 166 `GET /v1/jobs/nonexistent`) against a 10-connection pool.
   - Genuinely saturates pool by holding an `ACCESS EXCLUSIVE` table lock on `jobs` for 800ms (`holdDuration`) across requests pre-spawned on a start barrier.
   - Asserts bounded latency, zero transport crashes, every response 2xx/4xx or opaque 503 with header `Retry-After: 1`, zero 500s, and immediate recovery (<2ms).
   - Primary proof: Admin sampling of `pg_stat_activity` every 25ms verified 10/10 connections reached `wait_event_type = 'Lock'` (threshold $\ge 8$).
   - Secondary proof: Locked run max latency exceeded 3-run unlocked baseline median by $\ge 0.25 \times \text{holdDuration}$ (200ms).
   - Validated by adversarial mutation testing: commenting out `time.Sleep(holdDuration)` and setting `holdDuration = 1ms` both produced definitive test failures. Consistently passes across `-count=5` clean runs.
2. **Slow Query Context Deadline (`TestSecurity_PartD_SlowQuery_ContextDeadline` in `tests/integration/part_d_security_test.go:260`)**:
   - PostgreSQL lock contention forcing `statement_timeout` under 200ms query deadline.
   - Outcome: PASSED. Returned HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1` and opaque body `{"error":{"code":"SERVICE_UNAVAILABLE","message":"service temporarily unavailable"}}`; no hang, no 500.
3. **End-to-End SQL Injection Challenge (`TestSecurity_PartD_SQLInjection_EndToEnd` in `tests/integration/part_d_security_test.go:382`)**:
   - Injections across job payload, task_type, worker ID, version, and supported_tasks.
   - Outcome: PASSED. Neutralized as literal string data (201) or rejected at boundary (4xx); zero 5xx; 0 schema tables or columns altered.
4. **Outage Mid-Dispatch & Mid-Commit (`TestSecurity_PartD_Outage_MidDispatchAndMidCommit` in `tests/integration/part_d_security_test.go:707`)**:
   - Stopped database container mid-execution and restored it.
   - Outcome: PASSED. Worker backed off and committed cleanly after restore; 1 attempt, 1 result, 0 duplicate commits, 0 lost jobs, 0 partial rows.
5. **Real Base64URL Lease Token Leak Scan (`TestSecurity_PartD_LeakScan` in `tests/integration/part_d_security_test.go:907`)**:
   - Submits a job, registers a worker, polls to receive assignment, and extracts the real raw 32-byte base64url lease token (`domain.NewLeaseToken`).
   - Scans all captured client responses (from submit, poll, get, cancel, error) and coordinator logs across full outage cycle.
   - Asserts zero occurrences of connection strings, passwords, SQL fragments, database socket details (`:5433`, `:5432`), or the real raw base64url lease token string.
   - Coordinator's own listen address `addr=127.0.0.1:<coordPort>` is explicitly handled to distinguish coordinator listen logs from database host/port leaks.
   - Sub-test positive control (`PositiveControl_PlantedTokenDetected`): Plants a synthetic base64url lease token in a simulated log buffer and asserts that the leak scanner detects it and fails.
6. **Migrator Query Timeout Isolation (`TestMigrator_NotSubjectToCoordinatorQueryTimeout` in `internal/storage/postgres/migrator_test.go:640`)**:
   - Verifies that migration runner connections (`cmd/migrate` / `postgres.Migrator`) connect with unconstrained session timeouts (`statement_timeout = 0`, `lock_timeout = 0`).
   - Successfully executes a slow migration with `pg_sleep(0.6)` without cancellation, proving migration isolation from the coordinator's short query timeouts.

---

## Documentation Scan Allowlist

The following entries are permitted to appear in audit scans (`grep -rhoE 'Test[A-Z][A-Za-z0-9_]+'` and path checks) of documentation files:

### 1. Future Test Specifications
These test names do not yet exist in `go test -list '.*' ./...` because they represent designed verification steps for Open, unmitigated findings scheduled for future phases:
- `TestInMemoryRepository_MemoryExhaustion_Cap`: Future verification test for SEC-010 (in-memory collection bounds).
- `TestWorker_ExcessiveTimeoutClamped`: Future verification test for SEC-012 (worker execution timeout clamping).
- `TestCoordinator_IDLeakage`: Future verification test for SEC-013 (coordinator sequential ID exposure).

### 2. Historical Nonexistent Test Names in SEC-018
These names do not exist in the test suite and are cited exclusively within SEC-018 as documented negative examples of invented names from prior reports and earlier flawed remediation attempts:
- `TestAgent_Run_SingleJobSuccess` (does not exist (invented in an earlier report))
- `TestAPI_JobLifecycle` (does not exist (invented in an earlier report))
- `TestAPI_JobLifecycle_InMemory` (does not exist (invented in an earlier report))
- `TestAPI_JobNumericBounds_Validation` (does not exist (invented in an earlier report))
- `TestAPI_ReadHeaderTimeout_SlowLorisProtection` (does not exist (invented in an earlier report))
- `TestAPI_SubmitJob_Success` (does not exist (invented in an earlier report))
- `TestAPI_WorkerID_ValidationBounds` (does not exist (invented in an earlier report))
- `TestAPI_WorkerRegistration_Limits` (does not exist (invented in an earlier report))
- `TestCoordinator_Service` (does not exist (invented in an earlier report))
- `TestJob_Transitions` (does not exist (invented in an earlier report))
- `TestMigrator_AppliesAllMigrations` (does not exist (invented in an earlier report))
- `TestMigrator_ApplyAll_CleanDatabase` (does not exist (invented in an earlier report))
- `TestMigrator_ConcurrentRunners_AllMigrationsAppliedOnce` (does not exist (invented in an earlier report))
- `TestService_SubmitJob_Success` (does not exist (invented in an earlier report))
- `TestWorkerAgent_SingleJobSuccess` (does not exist (invented in an earlier report))

### 3. Nonexistent Path Citations in SEC-018
- `docs/adr/0013-worker-registration-bounds-and-rejection-codes.md`: Nonexistent file path cited exclusively in SEC-018 as documented disclosure of an invalid citation in a prior progress report. The real repository file is `docs/adr/0013-durable-dispatch-and-coordinator-recovery.md`.



