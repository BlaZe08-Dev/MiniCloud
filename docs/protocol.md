# MiniCloud Communication Protocol Specification

## 1. Overview
MiniCloud uses HTTP/1.1 with JSON payloads for both the public Client API and the internal Coordinator <-> Worker protocol. All communication is strictly pull-based for workers (see [ADR 0003](adr/0003-pull-based-worker-protocol.md)).

---

## 2. Global Protocol Rules & Limits

- **Content-Type:** `application/json` required for all request and response bodies.
- **Maximum Request Body Size:** 1 MiB (hard server limit via `http.MaxBytesReader`).
- **Maximum Task Payload Size:** 64 KiB.
- **Maximum Task Output Size:** 64 KiB (clamped via `ClampText`).
- **Maximum Attempt Error Text Size:** 10 KiB (10,240 bytes clamped via `ClampText`).
- **ClampText Semantics:** Task outputs and error strings are clamped defensively using `ClampText` (`internal/executor/truncate.go`). Any raw NUL (`\x00`) bytes and invalid UTF-8 byte sequences are replaced with `\uFFFD` (Unicode Replacement Character, 3 bytes in UTF-8). String length is measured after byte replacement; if it exceeds the limit (64 KiB for output, 10 KiB for error text), it is cut backward to the nearest valid UTF-8 rune boundary and suffixed with `\n[truncated]` such that the final total byte length does not exceed the limit.
- **Default Server Timeouts & Deadlines:**
  - Read Timeout: 5s
  - Write Timeout: 10s
  - Idle Timeout: 60s
  - Read Header Timeout: 2s (mitigating Slowloris delays)
  - Request Timeout (`RequestTimeout`): 8s context deadline enforced across all HTTP API requests (`requestDeadlineMiddleware` in `internal/api/handlers.go`). Requests that block on database queries or locks for >8s are aborted and return HTTP 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1` and an opaque error body. Documented limit (F-4e-3 / SEC-033): After a half-open database partition heals, pgx connection recovery may take up to ~15s due to internal `asyncClose` cleanup.
  - Background Call Timeout (`BackgroundCallTimeout`): 8s context deadline per iteration on background sweeper ticks and worker reaper iterations (`internal/coordinator/service.go`), preventing hung database connections from blocking background maintenance indefinitely.
- **Work-Poll Database Cost:** A work poll dispatch costs approximately 20 client-to-database network round trips (flights) when claiming a job, an empty poll costs 4 round trips, and a job submission costs 5 round trips. On networks with latency >400ms per round trip, polling operations approach the 8s request deadline.
- **Structured Error Format:** All non-2xx responses return a standardized JSON error envelope:
```json
{
  "error": {
    "code": "INVALID_STATE_TRANSITION",
    "message": "cannot transition job from SUCCEEDED to RUNNING",
    "details": null
  }
}
```

---

## 3. Client API Endpoints

### 3.1 `POST /v1/jobs` — Submit Job
Submits a computational job for asynchronous execution.

**Request:**
```json
{
  "task_type": "sha256_loop",
  "payload": {
    "iterations": 10000,
    "input": "minicloud-benchmark"
  },
  "max_attempts": 3,
  "timeout_seconds": 30
}
```

**Response (201 Created):** the same job view as `GET /v1/jobs/{job_id}` (Section 3.2), for a job that has just been queued.
```json
{
  "id": "job_01j7xyz...",
  "task_type": "sha256_loop",
  "payload": {"iterations": 10000, "input": "minicloud-benchmark"},
  "state": "QUEUED",
  "priority": 0,
  "max_attempts": 3,
  "timeout_seconds": 30,
  "created_at": "2026-10-19T22:00:00Z",
  "updated_at": "2026-10-19T22:00:00Z",
  "assigned_worker_id": null,
  "assignment_expires_at": null,
  "assignment_lapses": 0,
  "assignment_fallback_reason": null
}
```

The 201 body carries the four assignment fields of Section 3.2 at their unset values (ADR-0017 Section 5.1, note 5c-2.4): `assigned_worker_id` `null`, `assignment_expires_at` `null`, `assignment_lapses` `0`, `assignment_fallback_reason` `null`. It never carries the internal `assigned_at`.

**Errors:**
- `400 Bad Request`: `UNKNOWN_TASK_TYPE`, `PAYLOAD_TOO_LARGE`, `INVALID_TIMEOUT`.

---

### 3.2 `GET /v1/jobs/{job_id}` — Get Job Details
Fetches current state, timestamps, and latest attempt summary.

**Response (200 OK):**
```json
{
  "id": "job_01j7xyz...",
  "task_type": "sha256_loop",
  "state": "SUCCEEDED",
  "current_attempt_id": "att_01j7abc...",
  "max_attempts": 3,
  "created_at": "2026-10-19T22:00:00Z",
  "updated_at": "2026-10-19T22:00:05Z",
  "result": {
    "exit_code": 0,
    "output": "{\"hash\":\"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\"}",
    "error": ""
  },
  "assigned_worker_id": null,
  "assignment_expires_at": null,
  "assignment_lapses": 0,
  "assignment_fallback_reason": "lapses"
}
```

**Assignment fields (ADR-0017 Section 5.1, additive).** The job view always carries these four keys, under either dispatch policy:

| Field | Type | Value |
|---|---|---|
| `assigned_worker_id` | string or `null` | The worker holding a live reservation of the job (`round_robin` only); `null` when no reservation is held. A registered worker ID, so at most 128 characters of `[A-Za-z0-9_-]`. |
| `assignment_expires_at` | RFC 3339 timestamp or `null` | End of the accept window of that reservation; `null` exactly when `assigned_worker_id` is `null`. |
| `assignment_lapses` | integer | Counted lapses since the last claim, `0` when none; reset to `0` by every claim. |
| `assignment_fallback_reason` | `"lapses"`, `"age"` or `null` | Set by a `round_robin_fallback` claim and never cleared afterwards (history); `null` until such a claim. |

The internal reservation time (`assigned_at`) is not part of the view.

**Errors:**
- `404 Not Found`: `JOB_NOT_FOUND`.

---

### 3.3 `GET /v1/jobs/{job_id}/attempts` — List Job Attempts
Returns the complete attempt history for a job.

**Response (200 OK):**
```json
{
  "job_id": "job_01j7xyz...",
  "attempts": [
    {
      "id": "att_01j7abc...",
      "attempt_no": 1,
      "worker_id": "worker-1",
      "state": "COMPLETED",
      "started_at": "2026-10-19T22:00:01Z",
      "finished_at": "2026-10-19T22:00:04Z",
      "exit_code": 0,
      "error": ""
    }
  ]
}
```

On failed, lost, or timed-out attempts, the attempt object also exposes `error_class` (`"PERMANENT"`, `"TRANSIENT"`, `"UNKNOWN"`, or `"LEASE_EXPIRED"`). For successful attempts (`exit_code == 0`), `error_class` is omitted (`omitempty`).

---

### 3.4 `POST /v1/jobs/{job_id}/cancel` — Cancel Job
Requests cancellation of a queued, retrying, or running job.

**Response (200 OK — Queued or Retrying Job Cancelled Immediately):**
```json
{
  "job_id": "job_01j7xyz...",
  "state": "CANCELLED",
  "message": "cancellation processed"
}
```

**Response (200 OK — Running Job Cancellation Requested):**
```json
{
  "job_id": "job_01j7xyz...",
  "state": "CANCEL_REQUESTED",
  "message": "cancellation processed"
}
```

**Response (200 OK — Duplicate Cancellation on Running Job):**
A duplicate cancel on a job in `CANCEL_REQUESTED` returns HTTP 200 with the exact same response body as the first cancellation request, without modifying job or attempt state:
```json
{
  "job_id": "job_01j7xyz...",
  "state": "CANCEL_REQUESTED",
  "message": "cancellation processed"
}
```

**Errors:**
- `400 Bad Request`: `INVALID_REQUEST` (invalid job ID format or length).
- `404 Not Found`: `JOB_NOT_FOUND`.
- `409 Conflict`: `JOB_ALREADY_TERMINAL` (job is already in a terminal state: `SUCCEEDED`, `FAILED`, or `CANCELLED`).
- `503 Service Unavailable`: `SERVICE_UNAVAILABLE` (database connection failure, lock timeout, or coordinator outage; includes `Retry-After: 1` header).

*Note on Cancel Status Codes (Unchanged from Today):* In Milestone 1–3 and continuing through Phase 4, the cancellation handler (`internal/api/handlers.go:267`) unconditionally returns HTTP 200 `OK` with `{"job_id": "...", "state": "...", "message": "cancellation processed"}` for all valid jobs (`QUEUED`, `RETRYING`, `RUNNING`, or duplicate cancels on `CANCEL_REQUESTED`). Cancel status codes are completely unchanged from today: every successful cancel returns HTTP 200 `OK`. There is no HTTP 202 `Accepted`, no ADR-0004 compatibility exception, and no `duplicate` response field.

---

### 3.5 `GET /v1/workers` — List Workers
Returns all registered workers with health status and available capacity.

**Response (200 OK):**
```json
{
  "workers": [
    {
      "worker_id": "worker-1",
      "version": "v0.1.0",
      "status": "HEALTHY",
      "total_slots": 4,
      "available_slots": 3,
      "last_heartbeat_at": "2026-10-19T22:00:10Z",
      "registered_at": "2026-10-19T21:55:00Z"
    }
  ]
}
```

---

### 3.6 `GET /healthz` & `GET /readyz` — Health Probes
- `GET /healthz`: Process liveness (returns `{"status":"ok"}`).
- `GET /readyz`: Coordinator readiness (returns `{"status":"ready"}`).

---

## 4. Coordinator <-> Worker Protocol

### 4.1 `POST /v1/workers/register` — Worker Registration
Invoked when a worker starts up.

**Request:**
```json
{
  "worker_id": "worker-1",
  "version": "v0.1.0",
  "total_slots": 4,
  "supported_tasks": ["echo", "sleep", "sha256_loop"],
  "executes_concurrently": true
}
```

`executes_concurrently` (optional boolean, additive since V2-0b, [ADR-0020](adr/0020-round-robin-real-slots.md) Section 2.6): `true` means the worker executes up to `total_slots` attempts at once, so the `round_robin` pass may reserve it up to `total_slots` jobs (at most 8 live reservations). Absent or `false` means one assignable slot, which is what the v1.0.0 and V2-0 workers, which do not send it, get. A non-boolean value is rejected with 400 `MALFORMED_JSON`. Each registration rewrites the stored value. `GET /v1/workers` shows it.

**Response (200 OK):**
```json
{
  "status": "REGISTERED",
  "heartbeat_interval_seconds": 2
}
```

---

### 4.2 `POST /v1/workers/{worker_id}/heartbeat` — Heartbeat
Invoked periodically by the worker (default: every 2 seconds).

**Request:**
```json
{
  "available_slots": 3
}
```
*Note on Available Slots (Informational):* The `available_slots` field in the heartbeat request is accepted for `/v1` protocol compatibility (any integer value accepted without validation) but ignored by both in-memory and PostgreSQL coordinator backends; capacity is dynamically derived from active `RUNNING` attempts to guarantee zero counter drift (ADR-0011, SEC-055, Informational).

**Response (200 OK):**
```json
{
  "status": "ACK"
}
```
*Note:* Heartbeats are strictly dedicated to worker node liveness and do not carry task cancellation arrays (ADR-0016).

---

### 4.3 `POST /v1/workers/{worker_id}/poll` — Claim / Poll Work
Worker polls for an eligible task assignment.

**Response (200 OK — Work Available):**
```json
{
  "assignment": {
    "job_id": "job_01j7xyz...",
    "attempt_id": "att_01j7abc...",
    "attempt_no": 1,
    "lease_token": "lease_sec_991823...",
    "task_type": "sha256_loop",
    "payload": {
      "iterations": 10000,
      "input": "minicloud-benchmark"
    },
    "timeout_seconds": 30
  }
}
```

**Response (204 No Content):**
No queued work available for this worker (`NO_QUEUED_JOBS`).

**Response (503 Service Unavailable — Worker Unhealthy):**
```json
{
  "error": {
    "code": "WORKER_UNHEALTHY",
    "message": "worker is marked UNHEALTHY; heartbeats required before polling",
    "details": null
  }
}
```
*Note (Phase 2 correction):* A poll from an `UNHEALTHY` worker does **not** count as liveness telemetry and returns HTTP 503 `WORKER_UNHEALTHY` until a heartbeat restores `HEALTHY` status. Worker clients handle `WORKER_UNHEALTHY` explicitly by continuing their heartbeat and polling loops without terminating.

---

### 4.4 `POST /v1/jobs/{job_id}/attempts/{attempt_id}/result` — Report Outcome
Worker reports task completion or failure.

**Request (Success):**
```json
{
  "lease_token": "lease_sec_991823...",
  "worker_id": "worker-1",
  "exit_code": 0,
  "output": "{\"hash\":\"e3b0c442...\"}",
  "error": ""
}
```

**Request (Failure with Error Classification):**
```json
{
  "lease_token": "lease_sec_991823...",
  "worker_id": "worker-1",
  "exit_code": 1,
  "output": "",
  "error": "syntax error in input payload",
  "error_classification": "PERMANENT"
}
```
*Note:* `error_classification` is only supplied when `exit_code != 0` or an execution fault occurred; success is `AttemptCompleted` and does not carry an error classification (ADR-0015).

**Response (200 OK — Accepted):**
```json
{
  "status": "COMMITTED",
  "job_state": "SUCCEEDED"
}
```
*Note:* A worker marked `UNHEALTHY` during execution still has its result accepted if `attempt_id`, `lease_token`, and `worker_id` match.

**Response (403 Forbidden — Worker Mismatch):**
```json
{
  "error": {
    "code": "WORKER_MISMATCH",
    "message": "worker mismatch: reporting worker is not assigned to attempt",
    "details": null
  }
}
```
*Note:* Permanent error. Worker clients treat 403 as non-retryable and abort immediately.

**Response (409 Conflict — Stale Attempt Rejected):**
```json
{
  "error": {
    "code": "STALE_ATTEMPT_REJECTED",
    "message": "attempt att_01j7abc is no longer active for job job_01j7xyz",
    "details": null
  }
}
```
*Note:* Permanent error. When an attempt lease has expired and been reaped to `LOST`, late results are strictly rejected and never committed to `results`. Worker clients treat 409 as non-retryable and must not loop.

**Response (400 Bad Request — Validation Rejection & Worker Fallback):**
When the coordinator rejects a result report with HTTP 400 and error code `INVALID_REQUEST` or `OUTPUT_TOO_LARGE`:
- The worker sends exactly **one fallback failure report** for the same attempt and lease: exit code 1, empty output, error class `PERMANENT`, and error summary `result rejected by coordinator: HTTP 400 <code>`.
- The worker's renewal goroutine continues renewing the lease until fallback delivery completes.
- If accepted, the job transitions to `FAILED` immediately on attempt 1, recording a single result row and preventing a poison loop of repeated lease expiries.
- If the fallback report itself receives an HTTP 400 or other terminal rejection, the worker halts delivery and exits with status 1.
- *Exclusion of `MALFORMED_JSON`*: When the coordinator answers 400 `MALFORMED_JSON`, no fallback report is sent. Because the worker always marshals valid JSON via `json.Marshal`, a malformed JSON response indicates transport-level corruption. The worker drops the result, the lease expires and is reaped to `LOST`, and the task re-runs safely (at-least-once).

**Response (409 Conflict — Ambiguous Commit Replay / Job Already Terminal):**
When a worker retries a commit whose original request succeeded in the database but whose response was dropped or delayed across a network partition, the coordinator returns HTTP 409 `JOB_ALREADY_TERMINAL`. The single stored result from the initial commit stands, no duplicate result rows or attempt records are created, and the retry is safe and idempotent (SEC-031, Informational).

---

## 5. Compatibility Policy

### 5.1 Additive-Only Evolution within `/v1`
All changes to the MiniCloud HTTP API and Worker Protocol within the `/v1` namespace must be strictly backwards-compatible:
1. **Request Schemas:** New fields added to requests must be optional. Omitting a newly introduced field must preserve existing behavior.
2. **Response Schemas:** New fields may be added to responses. Clients and workers must ignore unrecognized fields.
3. **No Field Removal:** Existing request/response fields cannot be renamed, removed, or have their types/semantics altered.
4. **Status Codes:** Existing endpoint status code semantics cannot be changed without an API major version bump.

### 5.2 Deprecation Process
If an endpoint or parameter is planned for removal or replacement:
1. An ADR must document the deprecation rationale, migration path, and timeline.
2. The endpoint or parameter will be marked deprecated in `docs/protocol.md` and `CHANGELOG.md`.
3. The coordinator will log deprecation warnings when deprecated features are accessed.
4. Deprecated functionality will remain fully operational until the next major API version (`/v2`).

### 5.3 Breaking Changes & Version Migration
Any change that cannot be expressed additively requires:
1. An approved Architecture Decision Record (ADR).
2. A new URL namespace prefix (`/v2/...`).
3. Parallel coexistence during a migration window where the coordinator serves both `/v1` and `/v2` endpoints.

---

## 6. Phase 4 Protocol Extensions & Compatibility Matrix

*Status: Accepted by Owner (2026-10-26, ADR-0014, ADR-0015, ADR-0016)*

### 6.1 `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` — Attempt Lease Renewal
Invoked periodically by the executing worker (default: every 10 seconds) to extend its lease. This is the **sole downstream delivery channel** for cooperative cancellation requests.

**Request:**
```json
{
  "worker_id": "worker-1",
  "lease_token": "lease_sec_991823..."
}
```

**Response (200 OK — Renewed):**
```json
{
  "status": "RENEWED",
  "lease_expires_at": "2026-10-26T20:30:00Z",
  "cancel_requested": false
}
```

**Response (200 OK — Cancellation Requested, Lease Not Extended):**
```json
{
  "status": "RENEWED",
  "lease_expires_at": "2026-10-26T20:30:00Z",
  "cancel_requested": true
}
```
*Note on Cancellation Delivery:* When `cancel_requested` is true, the renewal response returns the current expiry without extending `lease_expires_at`. The worker cancels the task execution context cooperatively via `context.Context`, ceases further renewals, and reports its result outcome. If an uncooperative task ignores context cancellation, the lease expires within at most one lease duration (30 seconds default) from the last renewal, and the coordinator sweeper reaps the attempt, moving the job to `CANCELLED` (never `RETRYING`). Worst-case delivery latency is bounded by exactly **one renewal interval (10 seconds default)**. (Accepted by owner).

**Response (403 Forbidden — Worker Mismatch):**
```json
{
  "error": {
    "code": "WORKER_MISMATCH",
    "message": "worker mismatch: reporting worker is not assigned to attempt",
    "details": null
  }
}
```

**Response (409 Conflict — Stale Attempt / Lease Expired):**
```json
{
  "error": {
    "code": "LEASE_EXPIRED",
    "message": "attempt lease expired and was marked lost by sweeper",
    "details": null
  }
}
```
*Note on Symmetric Accept-Until-Swept Rule:* While `attempts.state == 'RUNNING'`, renewals (`POST /renew`) and commits (`POST /result`) are accepted even if `lease_expires_at` has elapsed on database time (for example during sweeper grace periods or ticker intervals). Renewal extends the lease from current database time (`clock_timestamp() + lease_duration`), except on a `CANCEL_REQUESTED` job, where the renewal returns the current expiry unchanged without extending the lease. HTTP 409 Conflict (`LEASE_EXPIRED` / `STALE_ATTEMPT_REJECTED` / `JOB_ALREADY_TERMINAL`) is returned only once the attempt has already been swept to `LOST` by the coordinator lease sweeper or the job has reached a terminal state.
The sweeper opens an outage grace window of MINICLOUD_OUTAGE_GRACE_WINDOW (30 s) at the first successful tick after either (a) any failed sweeper tick, or (b) a database-time gap over 3x the sweep interval between consecutive successful ticks. A lock timeout or SKIP LOCKED skip on a single candidate is not a failed tick. After a window ends, a cooldown of the same length (30 s) begins, during which no new window opens and reaping proceeds, so suppression of reaping never exceeds one window length. The only authoritative grace check is the one inside the sweeper transaction lock.

*Worker Renewal Response Handling:*
- **200 OK (`cancel_requested: true`)**: The worker initiates cooperative task cancellation via `context.Context`, stops renewing the lease, and reports the finished outcome via `POST /result`.
- **403 Forbidden (`WORKER_MISMATCH`) or 409 Conflict (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`)**: Terminal rejection. The worker immediately halts task execution, aborts context, frees its execution slot, and **does not report a result** to the coordinator.
- **404 Not Found**: The worker inspects the response body error code:
  - *Recognized code* (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`): Terminal rejection. The worker halts execution, aborts context, and does not report a result.
  - *Unrecognized code or missing envelope* (e.g. HTTP 404 from older coordinator lacking the renew route): Treated as endpoint missing. The worker logs a warning once, disables further renewals for that attempt, keeps running the task to completion, and reports the result normally via `POST /result`.
- **400 Bad Request (`INVALID_REQUEST`, `WORKER_TASK_UNSUPPORTED`)**: The worker logs an error, disables further renewals for that attempt, keeps running the task to completion, and reports the result normally.
- **5xx / Transport / Connection Error**: Transient failure. The worker logs the error, continues task execution, and retries lease renewal on the next interval until the lease expires.

---

### 6.2 Additive Changes to Existing Endpoints

#### `POST /v1/workers/{worker_id}/heartbeat` Response
The heartbeat response remains dedicated strictly to node liveness (`{"status": "ACK"}`). Cancellation requests are not delivered via heartbeats (ADR-0016).

#### `POST /v1/workers/{worker_id}/poll` Assignment Expansion
The assignment payload expands additively with lease metadata:
```json
{
  "assignment": {
    "job_id": "job_01j7xyz...",
    "attempt_id": "att_01j7abc...",
    "attempt_no": 1,
    "lease_token": "lease_sec_991823...",
    "lease_expires_at": "2026-10-26T20:30:00Z",
    "lease_duration_seconds": 30,
    "task_type": "sha256_loop",
    "payload": {
      "iterations": 10000,
      "input": "minicloud-benchmark"
    },
    "timeout_seconds": 30
  }
}
```

*Dispatch policy fields (ADR-0017 Section 5.1, additive)*:
Every 200 poll response carries `assignment.policy`, the path that claimed the job: `"fifo_pull"` under the `fifo_pull` policy; `"round_robin"` for a claim of the worker's own reservation; `"round_robin_fallback"` for a claim by a fallback arm. `assignment.fallback_reason` is present only on a `round_robin_fallback` claim, with the value `"lapses"` (the job reached `MINICLOUD_ASSIGNMENT_MAX_LAPSES`) or `"age"` (the age arm of ADR-0017 Section 3.2); on any other claim the key is absent. Both values are fixed strings chosen by the coordinator (at most 20 bytes), never client input. Under `round_robin`, a 204 means no job reserved to this worker and no fallback job; the wire form is unchanged.
```json
{
  "assignment": {
    "job_id": "job_01j7xyz...",
    "policy": "round_robin_fallback",
    "fallback_reason": "lapses"
  }
}
```
(Other assignment keys as above, omitted here.)

*Worker Execution Timeout Clamping (SEC-012)*:
The worker clamps the coordinator-supplied `timeout_seconds` to `[1, 86400]` seconds (`MaxExecutionTimeoutSeconds = coordinator.MaxTimeoutSeconds`). If `timeout_seconds` exceeds 86400 s or integer overflow bounds, it is clamped to 86400 s (24 hours). If `timeout_seconds` is less than 1, it is clamped to 1 s (tasks must have a bounded positive execution deadline). A warning is logged whenever clamping occurs. The worker bounds the poll response size and escapes path IDs.

#### `POST /v1/jobs/{job_id}/attempts/{attempt_id}/result` Request Expansion
The report result payload accepts an optional `error_classification` field when reporting a non-zero exit code or execution fault:
```json
{
  "lease_token": "lease_sec_991823...",
  "worker_id": "worker-1",
  "exit_code": 1,
  "output": "",
  "error": "syntax error in input payload",
  "error_classification": "PERMANENT"
}
```
*Allowed `error_classification` values:* `"PERMANENT"`, `"TRANSIENT"`, `"UNKNOWN"` (defaults to `"UNKNOWN"` if omitted on failure). Omitted on successful runs (exit code 0). Note that `"LEASE_EXPIRED"` is an internal failure class assigned exclusively by the coordinator sweeper upon lease expiration and is not accepted in worker result submissions (rejected with HTTP 400 `INVALID_REQUEST`).

---

### 6.3 Standardized Error Code Additions & HTTP 409 Conflict Evaluation Order
When processing attempt results (`POST /result`) and lease renewals (`POST /renew`), conflicting states and validation failures are evaluated in strict priority order:

| Priority | Condition | HTTP Status | Error Code | Diagnostic Event Written? | Worker Action |
|---|---|---|---|:---:|---|
| 1 | Target Job is already in a terminal state (`SUCCEEDED`, `FAILED`, `CANCELLED`) | 409 Conflict | `JOB_ALREADY_TERMINAL` | Yes on commit (`DIAGNOSTIC_LATE_RESULT`); None on renew | Final: Worker halts execution, frees capacity slot, does not report result |
| 2 | Attempt state is `LOST` (swept by coordinator lease sweeper) | 409 Conflict | `LEASE_EXPIRED` | Yes on commit (`DIAGNOSTIC_LATE_RESULT`); None on renew | Final: Worker halts execution, frees capacity slot, does not report result |
| 3 | Attempt state is not `RUNNING`, attempt superseded, lease token mismatch, or unknown attempt ID (`domain.ErrStaleAttempt`) | 409 Conflict | `STALE_ATTEMPT_REJECTED` | No | Final: Worker halts execution, frees capacity slot, does not report result |
| 4 | Reporting worker ID does not match assigned worker | 403 Forbidden | `WORKER_MISMATCH` | No | Final: Worker halts execution, does not report result |
| 5 | Reporting worker does not exist | 404 Not Found | `WORKER_NOT_FOUND` | No | Final: Worker halts execution, does not report result |
| 6 | Target job does not exist | 404 Not Found | `JOB_NOT_FOUND` | No | Final: Worker halts execution, does not report result |

*Note on Diagnostic Events and Renewal Rejections*: Renewal rejections (`POST /renew`) write no event to `job_events` under any circumstance (confirmed by code reading in `internal/storage/postgres/leases.go` func `RenewLease` and `internal/storage/inmemory.go` func `RenewLease`); diagnostic events (`DIAGNOSTIC_LATE_RESULT`) are written exclusively on result commits (`POST /result`). An unknown attempt ID on renew under a real job returns 409 `STALE_ATTEMPT_REJECTED` (Priority 3), not 404 `ATTEMPT_NOT_FOUND`, exactly as for commit (orchestrator decision; see `docs/phase4-plan.md` section "Orchestrator decisions recorded for Phase 4 implementation" and test `TestAPI_RenewLease_SuccessAndStaleRejection`). Row 6 (`JOB_NOT_FOUND`) applies only when the target job itself does not exist.

*Worker Action on Renewal Responses*:
- **403 Forbidden or 409 Conflict**: Any 403 or 409 response to a renewal means the lease is lost and the worker aborts: it halts execution immediately, cancels context, marks the attempt lost, frees its capacity slot, and suppresses result delivery.
- **404 Not Found**: Treated as lease lost only when the response body carries a recognised error code (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`), causing the worker to abort and suppress the result. Otherwise (an unrecognised or empty 404 body, indicating an older coordinator lacking the `/renew` route), the worker logs a warning once, stops renewing, runs to completion, and delivers the outcome via `POST /result`.
- **400 Bad Request**: Stops renewals without aborting the task; the worker runs to completion and delivers the outcome via `POST /result`.
- **Transient Errors (5xx, Network)**: Retried with jittered backoff starting from 1 s and capped at the renewal interval (`renewLoop` in `internal/worker/client.go`) until context ends or the lease lapses.

*Terminal Job Result Rejection Behavior (D-R3-02)*: When a commit (including a retry of a commit that already succeeded) on an already-terminal job is rejected with 409 `JOB_ALREADY_TERMINAL`, the stored result from the initial commit is the single stored result; the retry is rejected; and the worker treats the 409 as final. The repository closes the attempt only if it is still `RUNNING` (and it is the job's current attempt); otherwise no attempt is modified (an attempt already `COMPLETED`, `FAILED`, `CANCELLED` or `LOST` is never overwritten). If the attempt was `RUNNING`, it sets attempt `state = 'CANCELLED'` if `jobs.state == 'CANCELLED'`, or `state = 'LOST'` otherwise, frees the worker capacity slot, and records a diagnostic event (`DIAGNOSTIC_LATE_RESULT`). (Known gap: replaying an identical commit as HTTP 200 is a possible future improvement, not Phase 4).

*Commit Outcomes on `CANCEL_REQUESTED` (D-R3-06, D-R4-03)*:
When committing a result on a job in `CANCEL_REQUESTED`:
- A success commit (exit code 0 and empty error) transitions attempt to `COMPLETED` and job to `SUCCEEDED`, recording `JOB_COMPLETED`, matching today's verified behavior in `internal/storage/inmemory.go` func `CommitAttempt` and `internal/storage/postgres/repository.go` func `CommitAttempt` (permitted by `internal/domain/transition.go` table `allowedTransitions`).
- A failed commit (non-zero exit code or error string) transitions attempt to `FAILED` and job to `CANCELLED`, recording `JOB_CANCELLED` (cancel intent wins, never `RETRYING` or `FAILED`).
- If the lease expires before commit, the sweeper reaps the attempt as `CANCELLED` and job as `CANCELLED`, recording `JOB_CANCELLED`.

The worker treats all 409 Conflict and 403 Forbidden codes as final terminal rejections. Legacy or redundant codes (`ATTEMPT_NOT_RUNNING`, `JOB_CANCELLED`) are deprecated and consolidated into `JOB_ALREADY_TERMINAL` (when job is terminal) or `STALE_ATTEMPT_REJECTED` (when attempt is not running).

---

### 6.4 Backwards Compatibility Matrix & Deployment Policy

#### Deployment Rule (Accepted by Owner)
MiniCloud V1 coordinator and worker binaries **deploy together as a synchronized release**. Running mixed versions across release boundaries is not a supported long-term operating mode.

| Client / Worker Combination | Behavior & Compatibility Guarantee |
|---|---|
| **Old Worker (Phase 1–3) vs New Coordinator (Phase 4)** | **Unsupported for tasks longer than lease duration (30 s)**: Old workers do not call `/renew`. Tasks completing within the initial 30s lease commit successfully. Tasks running > 30s expire, are marked `LOST` by the sweeper, and receive HTTP 409 `LEASE_EXPIRED` or `STALE_ATTEMPT_REJECTED` on late commit. Furthermore, because `/renew` is the single cancellation delivery channel, old workers never receive cooperative cancellation requests; cancelled jobs resolve only through sweeper expiry or worker commit. Coordinator and workers must deploy together in V1. |
| **New Worker (Phase 4) vs Old Coordinator (Phase 1–3)** | **Graceful Degradation**: New worker attempts `POST /renew`. On an old coordinator lacking the endpoint, a 404 response without a recognized error envelope is returned. The new worker logs a single compatibility warning, disables periodic renewal for that attempt, and falls back to Phase 3 run-to-completion behavior, reporting the outcome via `POST /result`. New worker handles static `{"status": "ACK"}` heartbeat response without errors. |

---

## 7. Node Routes (V2-1c, ADR-0022)

Additive within `/v1`. The four node routes are **registered only when the coordinator is local-only** (Section 7.5). Otherwise they do not exist and the router's normal 404 for an unknown route applies. The body limit (1 MiB), the JSON error envelope and "unknown JSON fields are ignored" are the same as everywhere in `/v1`. A node ID is a lowercase RFC 9562 version 4 UUID in canonical form, and it is validated before any repository call, in the body and in the path.

### 7.1 `POST /v1/nodes/register`

**Request:** `node_id`, `display_name` (0..64 bytes, valid UTF-8, no control character), `contract_version` (must be `1`), `agent_version` (0..64 bytes, no NUL) and `hardware` (`os`, `arch` and the optional `cpu_model`, `cpu_logical_cores`, `cpu_physical_cores`, `memory_total_bytes`, `disk_total_bytes`, `gpu_count`, `gpu_model`, `gpu_vram_total_bytes`). An absent or `null` measurement is unknown (stored NULL); `0` is a measured zero. Limits: `docs/security/field-limits.md` section 4.

**Response (200 OK):** `{"status":"REGISTERED","node_id":"...","hardware_version":3}`. Idempotent per `node_id`. A new hardware version is created only when the hardware content hash changed; at most the newest 10 versions are kept.

| Status | Code | When |
|---|---|---|
| 400 | `MALFORMED_JSON` | the body cannot be decoded, including a value of the wrong JSON type (a string where a number is expected, a fraction or an integer beyond 64 bits) |
| 400 | `INVALID_REQUEST` | a bad node ID, string or out-of-range value |
| 400 | `UNSUPPORTED_CONTRACT_VERSION` | `contract_version` is absent or not in the supported set |
| 429 | `RATE_LIMITED` | the hardware changed less than `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` after the newest version. `Retry-After` is whole seconds, rounded up, from the repository clock. The body is opaque (no stored hardware) and nothing is written. |
| 429 | `NODE_LIMIT_REACHED` | the node does not exist and the node count is at or above `MINICLOUD_NODE_MAX_COUNT`. **No `Retry-After` header.** Existing nodes still register and update. |

The node ceiling is **soft**: the count check and the insert are not one transaction, so concurrent registrations of new IDs may overshoot it by at most the number of concurrent requests.

### 7.2 `POST /v1/nodes/{node_id}/telemetry`

**Request:** `contract_version` (1), `session_id` (UUID, required), `seq` (1..2^53-1, required), `sample_age_ms` (0..2^31-1, default 0) and the optional `cpu_load_1m_milli`, `memory_available_bytes`, `disk_free_bytes`. `sampled_at` is the repository (database) receive time minus `sample_age_ms`; no client clock is used. A `sample_age_ms` that would put `sampled_at` before the node's `created_at` is rejected with 400 `INVALID_REQUEST` (checked by the coordinator with its own clock).

**Response (200 OK):** `{"status":"ACK"}`.

| Status | Code | When |
|---|---|---|
| 400 | `MALFORMED_JSON`, `INVALID_REQUEST`, `UNSUPPORTED_CONTRACT_VERSION` | as in 7.1, plus a missing or invalid `session_id` or `seq` |
| 404 | `NODE_NOT_FOUND` | the node has not registered (first report or any `seq`; never 409 or 500) |
| 409 | `TELEMETRY_OUT_OF_ORDER` | same session and `seq` not greater than the stored one. Opaque body; the stored sample is not touched. |

### 7.3 `GET /v1/nodes?limit=&after=`

Summary only, ordered by `node_id` ascending. `limit` is 1..500 (default 100); `after` is a canonical node ID and only IDs greater than it are returned. Anything else is 400 `INVALID_REQUEST` (an empty `limit=` or `after=` included). Both defaults are placeholders, not measured.

**Response (200 OK):** `{"nodes":[{"node_id","display_name","os","arch","hardware_version","worker_count","telemetry_age_ms"}],"next_cursor":"<last node_id>"|null}`. `next_cursor` is the last `node_id` of the page when the page is full (more may follow; the next page can still be empty), otherwise `null`. `os`, `arch` and `hardware_version` are `null` for a node without hardware; `telemetry_age_ms` is `null` without telemetry. It is display only: the coordinator clock minus the stored `received_at`.

### 7.4 `GET /v1/nodes/{node_id}`

The full view: `node_id`, `display_name`, `created_at`, `last_seen_at`, `hardware` (`version`, `contract_version`, `agent_version`, `reported_at` and the hardware fields, or `null`), `telemetry` (`contract_version`, `received_at`, `sampled_at` and the three values, or `null`) and `worker_ids`. It carries neither `session_id` nor `seq`. Unknown values are JSON `null`, never `0`. This is the only response that carries the sensitive fields: `cpu_model`, `gpu_model`, the core and GPU counts, `memory_total_bytes`, `disk_total_bytes`, `gpu_vram_total_bytes`, `agent_version` and every telemetry value. 404 `NODE_NOT_FOUND` for an unknown node.

### 7.5 The local-only rule and its honest limits

At startup the coordinator decides once, from `MINICLOUD_HTTP_ADDR`, whether it is local-only: true if and only if the host part is a literal loopback IP address (`net.IP.IsLoopback`). An empty host, `0.0.0.0`, `[::]`, any other IP, any host name (`localhost` included) and an unparsable address are not local-only. The boolean is logged once, kept for the process lifetime, never recomputed per request and never read from a header. There is no override setting.

When not local-only: the four routes return 404; `GET /v1/workers` has no `node_id` key (the V1 shape); `POST /v1/workers/register` ignores `node_id` completely, whatever its JSON type, and stores NULL.

When local-only: `GET /v1/workers` shows `node_id` on every worker, a string or `null`. `POST /v1/workers/register` takes `node_id` absent or `null` as NULL; a string must be a valid node ID of a registered node, else 400 `INVALID_REQUEST`; any other JSON type is 400 `INVALID_REQUEST`.

This is a network restriction, not authentication. Anything that reaches the loopback address passes (every local process, a container with host networking, a port forward). A reverse proxy on the same machine defeats it, because the coordinator cannot see that the client is remote. The check reads the configured address, not the sockets actually opened. A UUID is not proof of identity, so a caller who can reach the routes can impersonate a node. Authentication is V3-2.
