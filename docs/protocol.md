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
Requests cancellation of a queued or running job.

**Response (200 OK):**
```json
{
  "job_id": "job_01j7xyz...",
  "state": "CANCELLED",
  "message": "job cancelled successfully"
}
```

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

**Request:**
```json
{
  "lease_token": "lease_sec_991823...",
  "worker_id": "worker-1",
  "exit_code": 0,
  "output": "{\"hash\":\"e3b0c442...\"}",
  "error": ""
}
```

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
*Note:* Permanent error. Worker clients treat 403 as non-retryable.

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
*Note:* Permanent error. Worker clients treat 409 as non-retryable.

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

## 6. PROPOSED Phase 4 Protocol Extensions & Compatibility Matrix

*Status: PROPOSED (Owner Approval Pending, ADR-0014, ADR-0015, ADR-0016)*

### 6.1 `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` — Attempt Lease Renewal
Invoked periodically by the executing worker (default: every 10 seconds) to extend its lease.

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

**Response (200 OK — Renewed with Cancellation Requested):**
```json
{
  "status": "RENEWED",
  "lease_expires_at": "2026-10-26T20:30:00Z",
  "cancel_requested": true
}
```
*Note:* When `cancel_requested` is true, the worker aborts task execution cooperatively via `context.Context` and reports cancellation outcome.

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
    "code": "STALE_ATTEMPT_REJECTED",
    "message": "attempt lease has already expired or attempt is no longer active",
    "details": null
  }
}
```

---

### 6.2 Additive Changes to Existing Endpoints

#### `POST /v1/workers/{worker_id}/heartbeat` Response Expansion
The coordinator response expands additively to include active cancellation requests:
```json
{
  "status": "ACK",
  "cancelled_job_ids": ["job_01j7xyz..."]
}
```

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
The report result payload accepts an optional `error_classification` field:
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
*Allowed `error_classification` values:* `"PERMANENT"`, `"RETRYABLE"`, `"UNKNOWN"` (defaults to `"UNKNOWN"` if omitted).

---

### 6.3 Standardized Error Code Additions
Phase 4 introduces explicit error codes in the standard JSON envelope:
- `LEASE_EXPIRED` (HTTP 409 Conflict): The attempt lease expired before completion or renewal.
- `ATTEMPT_NOT_RUNNING` (HTTP 409 Conflict): Attempt is in a terminal or non-running state.
- `JOB_CANCELLED` (HTTP 409 Conflict): The target job was cancelled.

---

### 6.4 Backwards Compatibility Matrix

| Client / Worker Combination | Behavior & Compatibility Guarantee |
|---|---|
| **Old Worker (Phase 1–3) vs New Coordinator (Phase 4)** | **Fully Supported**: Old worker does not call `/renew`. Tasks completing within the initial 30s lease commit successfully. Tasks running $>30$s expire and receive 409 `STALE_ATTEMPT_REJECTED`, which old workers already treat as non-retryable. Old worker ignores `cancelled_job_ids` in heartbeat and completes tasks to terminal state without cooperative preemption. |
| **New Worker (Phase 4) vs Old Coordinator (Phase 1–3)** | **Graceful Degradation**: New worker attempts `POST /renew`, which returns HTTP 404/405 on old coordinator. New worker logs a single compatibility warning, disables periodic renewal for that attempt, and falls back to Phase 3 run-to-completion behavior. New worker handles static `{"status": "ACK"}` heartbeat response without errors. |


