# Security Findings Register

This document tracks security and adversarial testing findings for MiniCloud. Every finding is assigned a unique identifier (`SEC-xxx`), categorized by severity, and evaluated against the evidence rules in [security-testing.md](security-testing.md).

For findings of **Medium** severity and above, the full structured report format is required. For **Low** and **Informational** findings, entries are summarized in the register table.

---

## Findings Summary Register

| ID | Severity | Status | Affected Component | Summary | Regression Test / Reference |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **SEC-001** | Medium | Confirmed, Fixed | Coordinator Logging | Worker mismatch log misattributed assigned worker ID to the reporting worker. | `TestService_WorkerMismatch_Rejected`, `TestAPI_StaleAttemptRejection` |
| **SEC-002** | Low | Confirmed, Fixed | Dispatch Precedence | In-memory repository checked worker health before job state, causing dead jobs to be released back to queue. | `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased`, Conformance Suite |
| **SEC-003** | Informational | Open | Docker Compose | Compose `POSTGRES_PASSWORD` lacked fail-fast syntax `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}`. | `deployments/docker-compose.yml` |
| **SEC-004** | Medium | Accepted Risk (V1) | HTTP API & Workers | HTTP API and worker endpoints lack authentication and authorization; open to local network. | Deferred to V3 (ADR-0004); mitigated by `127.0.0.1` bind default. |
| **SEC-005** | Low | Confirmed, Fixed | AST SQL Checker | Initial AST check scanned only `repository.go` and accepted identifier variables; coverage was overstated. | `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` |
| **SEC-006** | Informational | Open | Test Helpers | Test helper builds `CREATE SCHEMA` / `DROP SCHEMA` with `fmt.Sprintf` from in-process generated hex string. | `internal/storage/postgres/testhelper_test.go:55` |

---

## Detailed Findings (Medium and Above)

### SEC-001: Worker Mismatch Log Misattributed Assigned Worker ID

- **Finding**: When a worker attempted to commit results for an attempt assigned to a different worker, the coordinator logged the reporting worker's ID as `assigned_worker`.
- **Severity**: Medium
- **Affected Component**: Coordinator Service (`internal/coordinator/service.go`)
- **Preconditions**: A rogue or misconfigured worker attempts to commit results for an attempt currently leased by another active worker.
- **Reproduction Steps**:
  1. Dispatch attempt for job $J$ to worker $W_1$.
  2. Issue a commit request for that attempt from worker $W_2$.
  3. Inspect coordinator service structured logs.
- **Evidence**: In slice 3c review, test log inspection revealed `assigned_worker=rogue-worker reporting_worker=rogue-worker`, failing to identify that the job was assigned to `worker-1`.
- **Impact**: Audit logs were misleading during incident response, masking the identity of the legitimate worker that held the lease and misdiagnosing concurrency conflicts.
- **Root Cause**: `internal/coordinator/service.go` passed the incoming request's `workerID` to both `assigned_worker` and `reporting_worker` log fields when handling `ErrWorkerMismatch`.
- **Recommended Fix**: Return a typed `domain.WorkerMismatchError` carrying the actual assigned worker ID from repository storage to the service, and log the true assigned worker while keeping the HTTP API response generic to avoid leaking worker IDs to callers.
- **Regression Test**: `TestService_WorkerMismatch_Rejected` (`internal/coordinator/service_test.go:215`), `TestAPI_StaleAttemptRejection` (`internal/api/handlers_test.go:121`).
- **Status**: Confirmed, Fixed (Slice 3c).

---

### SEC-004: Unauthenticated HTTP API and Worker Endpoints

- **Finding**: All coordinator HTTP endpoints (`/api/v1/jobs`, `/api/v1/workers`, `/api/v1/workers/{id}/heartbeat`, `/api/v1/workers/{id}/poll`, `/api/v1/attempts/{id}/commit`) lack authentication and authorization.
- **Severity**: Medium
- **Affected Component**: HTTP API Server (`internal/api/server.go`)
- **Preconditions**: Network route accessibility to the coordinator listening port (port 8080).
- **Reproduction Steps**:
  1. Send `POST /api/v1/jobs` without an `Authorization` header.
  2. Send `POST /api/v1/workers` to register an arbitrary worker ID without credentials.
  3. Send `POST /api/v1/workers/{id}/poll` to receive assigned attempts.
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
- **Regression Test**: `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` (`internal/coordinator/service_test.go:375`), plus shared repository conformance tests (`internal/storage/conformance_test.go`).

### SEC-003: Compose POSTGRES_PASSWORD Missing Fail-Fast Expansion
- **Status**: Informational, Open.
- **Detail**: `deployments/docker-compose.yml:18` provides a fallback password default (`${POSTGRES_PASSWORD:-minicloud_dev_password}`). Recommended: update to fail-fast form `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` to prevent accidental container startup with default credentials.

### SEC-005: AST SQL Concatenation Checker Coverage Overstatement
- **Status**: Confirmed, Fixed (Slice 3c follow-up).
- **Detail**: Process finding. Initial implementation of `TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation` parsed only `repository.go`, covered only `Exec/Query/QueryRow`, and treated all AST identifier variables as safe. A variable constructed via `fmt.Sprintf` would pass. Fixed in slice 3c follow-up by scanning all non-test `.go` files in `internal/storage/postgres`, resolving identifiers to `const` declarations, and adding negative fixtures. Residual limit: SQL assembled across helper function boundaries returning dynamic strings is not detected.
- **Process Rule**: Tool-reported or agent-reported coverage claims must be verified against actual code or mutation tests before claiming complete coverage.

### SEC-006: In-Process Random Schema Name Assembly in Test Helpers
- **Status**: Informational, Open (Inferred from code review, confirmed by inspection).
- **Detail**: `internal/storage/postgres/testhelper_test.go:55` uses `fmt.Sprintf("CREATE SCHEMA %s;", schemaName)`. This is safe because `schemaName` is generated in-process from random hex (`fmt.Sprintf("test_repo_%s", hex.EncodeToString(b[:]))`), but dynamic SQL in test fixtures sets a poor precedent. Recommended: validate generated name against `^[a-z0-9_]+$` before execution and sanitize using `pgx.Identifier{schemaName}.Sanitize()`.
