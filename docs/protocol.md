# MiniCloud Communication Protocol Specification

## 1. Overview
MiniCloud uses HTTP/1.1 with JSON payloads for both the public Client API and the internal Coordinator <-> Worker protocol. All communication is strictly pull-based for workers (see [ADR 0003](adr/0003-pull-based-worker-protocol.md)).

---

## 2. Global Protocol Rules & Limits

- **Content-Type:** `application/json` required for all request and response bodies.
- **Maximum Request Body Size:** 1 MiB (hard server limit via `http.MaxBytesReader`).
- **Maximum Task Payload Size:** 64 KiB.
- **Maximum Task Output Size:** 64 KiB (output truncated if task exceeds limit).
- **Default Server Timeouts:**
  - Read Timeout: 5s
  - Write Timeout: 10s
  - Idle Timeout: 60s
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

**Response (201 Created):**
```json
{
  "job_id": "job_01j7xyz...",
  "state": "QUEUED",
  "task_type": "sha256_loop",
  "created_at": "2026-10-19T22:00:00Z"
}
```

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
  }
}
```

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
      "attempt_id": "att_01j7abc...",
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
  "supported_tasks": ["echo", "sleep", "sha256_loop"]
}
```

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
*Note:* The `available_slots` field in the heartbeat request is accepted for `/v1` protocol compatibility but ignored by the coordinator; capacity is dynamically derived from active `RUNNING` attempts to guarantee zero counter drift (ADR-0011).

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

*Note on Diagnostic Events and Renewal Rejections*: Renewal rejections (`POST /renew`) write no event to `job_events` under any circumstance; diagnostic events (`DIAGNOSTIC_LATE_RESULT`) are written exclusively on result commits (`POST /result`). An unknown attempt ID on renew returns 409 `STALE_ATTEMPT_REJECTED`, exactly as for commit (orchestrator-authorised deviation).

*Worker Action on Renewal Responses*:
- **403 Forbidden (`WORKER_MISMATCH`) and Recognised 404 (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`)**: Treated as lease-lost for the worker. The worker halts execution immediately, cancels context, frees its capacity slot, and does not report a result.
- **Unrecognised or Empty 404 Body**: Indicates an older coordinator lacking the `/renew` route. The worker logs a warning once, disables renewals for that attempt, and keeps running to completion without renewals, reporting the outcome via `POST /result`.
- **409 Conflict (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`)**: Final rejection; the worker halts execution, frees slot, and does not report result.

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
