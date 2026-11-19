# MiniCloud Field Limits & Boundary Validation Specification

## Mission & Threat Context
MiniCloud enforces strict defense-in-depth boundary validation across all entrypoints. Untrusted input is validated at the coordinator/API boundary using typed domain validations, and backed by PostgreSQL database-level `CHECK` constraints (migrations 000001, 000002, 000003, 000004, 000005). 

This document defines the normative limits across all entity types (Jobs, Workers, Attempts, Results), documenting the exact byte/character limits, regular expression character sets, default values, database constraint names, and HTTP error mappings.

---

## 1. Field Limit & Constraint Matrix

| Entity | Field | Type / Representation | Min Bound | Max Bound | Default Value | Allowed Charset / Format | Database Constraint | HTTP Violation Status |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Job** | `id` | `string` / `TEXT` | 1 char | 128 chars | Auto-generated (`job_<seq>_<hex>`) | `^[a-zA-Z0-9_-]+$` | `jobs_pkey` (PRIMARY KEY) | `400 INVALID_REQUEST` |
| **Job** | `task_type` | `string` / `VARCHAR(64)` | 1 char | 64 chars | N/A (Required) | Allowlisted tasks (`echo`, `sleep`, `sha256_loop`) | None (Domain allowlist) | `400 UNKNOWN_TASK_TYPE` |
| **Job** | `payload` | `[]byte` / `BYTEA` | 0 bytes | 65,536 bytes (64 KiB) | N/A (Required) | Arbitrary binary / JSON. The 64 KiB limit now holds on the wire as well: responses are no longer HTML-escaped (SEC-067 fixed, V1x-02), so the worker receives the submitted bytes. | `chk_jobs_payload_size` | `413 PAYLOAD_TOO_LARGE` |
| **Job** | `max_attempts` | `int` / `INTEGER` | 1 | 20 | 3 | Integer in `[1, 20]` | `chk_jobs_max_attempts` | `400 INVALID_REQUEST` |
| **Job** | `timeout_seconds` | `int` / `INTEGER` | 1 | 86,400 (24h) | 60 | Integer in `[1, 86400]` | `chk_jobs_timeout` | `400 INVALID_REQUEST` |
| **Job** | `state` | `string` / `VARCHAR(32)` | N/A | 32 chars | `SUBMITTED` | Central FSM states | `chk_jobs_state` | `400 INVALID_STATE_TRANSITION` |
| **Job** | `assigned_worker_id` | `string` / `TEXT` | 1 char | 128 chars | `NULL` | `^[a-zA-Z0-9_-]+$` | `chk_jobs_assignment_shape` | n/a (coordinator-written) |
| **Job** | `assignment_lapses` | `int` / `INTEGER` | 0 | Unbounded | 0 | Integer >= 0 | `chk_jobs_assignment_lapses` | n/a (coordinator-written) |
| **Job** | `assignment_fallback_reason` | `string` / `VARCHAR(16)` | N/A | 16 chars | `NULL` | `'lapses'`, `'age'` | `chk_jobs_fallback_reason` | n/a (coordinator-written) |
| **Worker** | `id` | `string` / `TEXT` | 1 char | 128 chars | N/A (Required) | `^[a-zA-Z0-9_-]+$` | `workers_pkey` (PRIMARY KEY) | `400 INVALID_REQUEST` |
| **Worker** | `total_slots` | `int` / `INTEGER` | 1 | 1,024 | N/A (Required) | Integer in `[1, 1024]` | `chk_workers_total_slots_range` | `400 INVALID_REQUEST` |
| **Worker** | `version` | `string` / `VARCHAR(64)` | 0 chars | 64 chars | `""` (Empty allowed) | Printable ASCII / semver | `chk_workers_version_length` | `400 INVALID_REQUEST` |
| **Worker** | `supported_tasks` | `[]string` / `TEXT[]` | 0 items | 16 items | `[]` (Empty allowed) | Array of task identifiers (deduplicated) | `chk_workers_supported_tasks_count` | `400 INVALID_REQUEST` |
| **Worker** | Task item | `string` / `TEXT` | 1 char | 64 chars | N/A | `^[a-z0-9_]{1,64}$` | None (Domain validation) | `400 INVALID_REQUEST` |
| **Worker** | `status` | `string` / `VARCHAR(32)` | N/A | 32 chars | `HEALTHY` | `HEALTHY`, `UNHEALTHY`, `DRAINING`, `TERMINATED` | `chk_workers_status` | `503 WORKER_UNHEALTHY` / `400` |
| **Attempt**| `id` | `string` / `TEXT` | 1 char | 128 chars | Auto-generated (`att_<seq>_<hex>`) | `^[a-zA-Z0-9_-]+$` | `attempts_pkey` (PRIMARY KEY) | `400 INVALID_REQUEST` |
| **Attempt**| `attempt_no` | `int` / `INTEGER` | 1 | Monotonic | Derived: `MAX(attempt_no)+1` | Positive integer | `chk_attempts_attempt_no` | `400 INVALID_REQUEST` |
| **Attempt**| `lease_token_hash`| `string` / `CHAR(64)`| 64 chars| 64 chars | N/A | SHA-256 hex digest | None | `403 FORBIDDEN` / `409 CONFLICT` |
| **Attempt**| `output` | `string` / `TEXT` | 0 bytes | 65,536 bytes (64 KiB) | N/A | Structured JSON or text | `chk_attempts_output_size` | `400 OUTPUT_TOO_LARGE` |
| **Attempt**| `last_error` | `string` / `TEXT` | 0 bytes | 10,240 bytes (10 KiB) | `NULL` | Opaque error message | `chk_attempts_last_error_size` | `400 INVALID_REQUEST` |
| **Attempt**| `error_class` | `string` / `VARCHAR(32)` | N/A | 32 chars | `NULL` | `'PERMANENT'`, `'TRANSIENT'`, `'UNKNOWN'`, `'LEASE_EXPIRED'` | `chk_attempts_error_class` | n/a (coordinator-written) |
| **Result** | `job_id` | `string` / `TEXT` | 1 char | 128 chars | Foreign Key | `^[a-zA-Z0-9_-]+$` | `results_pkey` (PRIMARY KEY) | `409 JOB_ALREADY_TERMINAL` |

---

## 2. Worker Registration Boundary Rules

### 2.1 Slots Bounds (`total_slots`) (SEC-015)
- **Minimum**: 1. Workers with 0 or negative slots are rejected with `domain.ErrInvalidRequest` (`total_slots must be between 1 and 1024`).
- **Maximum**: 1,024. Prevents malicious or misconfigured workers from advertising near-infinite capacity that skews scheduling heuristics or triggers integer overflows.
- **Database Defense**: `chk_workers_total_slots_range CHECK (total_slots >= 1 AND total_slots <= 1024) NOT VALID; VALIDATE CONSTRAINT ...`.

### 2.2 Version String Length (`version`) (SEC-016)
- **Minimum**: 0 characters. Omitted or empty version strings are explicitly permitted and default to `""`, preserving compatibility with unversioned workers and lightweight test harnesses.
- **Maximum**: 64 characters. Prevents multi-megabyte version strings from consuming memory or violating SQL column bounds.
- **Database Defense**: `chk_workers_version_length CHECK (length(version) <= 64) NOT VALID; VALIDATE CONSTRAINT ...` and SQLSTATE `22001` (string right truncation) mapping.

### 2.3 Supported Tasks Array (`supported_tasks`) (SEC-017)
- **Array Length**: At most 16 unique tasks. Arrays exceeding 16 unique items are rejected with `domain.ErrInvalidRequest` (`supported_tasks cannot exceed 16 tasks`).
- **Deduplication**: Duplicate task names in registration requests are automatically deduplicated while preserving order.
- **Empty Array**: An empty slice `[]` is explicitly permitted upon registration. Workers with 0 supported tasks register successfully, but cannot be dispatched any jobs because claim/dispatch requires task type matching.
- **Task Identifier Charset**: Each task identifier must be between 1 and 64 characters in length and strictly match `^[a-z0-9_]{1,64}$` (lowercase alphanumeric and underscore only; uppercase characters, hyphens, spaces, and shell metacharacters are rejected).
- **Rolling Upgrades**: Unknown but well-formed task names (e.g., `gpu_mult_v2`) are accepted upon worker registration to permit zero-downtime rolling upgrades across worker fleets where newer worker versions announce tasks before corresponding jobs are submitted.
- **Database Defense**: `chk_workers_supported_tasks_count CHECK (coalesce(cardinality(supported_tasks), 0) <= 16) NOT VALID; VALIDATE CONSTRAINT ...`.

---

## 3. SQL Error Mapping & Client Sanitization

To ensure server-side diagnostics do not leak sensitive infrastructure internals to clients, all PostgreSQL driver and check violations are intercepted by `internal/storage/postgres/repository.go:mapPgError` and `internal/api/handlers.go:mapDomainErrorToHTTP`:

| PostgreSQL SQLSTATE / Error | Specific Constraint Name | Typed Domain Error | HTTP Status & Error Code | Client Error Envelope |
| :--- | :--- | :--- | :--- | :--- |
| `23514` (check_violation) | `chk_jobs_payload_size` | `domain.ErrPayloadTooLarge` | `413 PAYLOAD_TOO_LARGE` | `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"payload exceeds maximum allowed size"}}` |
| `23514` (check_violation) | `chk_attempts_output_size` | `domain.ErrOutputTooLarge` | `400 OUTPUT_TOO_LARGE` | `{"error":{"code":"OUTPUT_TOO_LARGE","message":"output exceeds maximum allowed size"}}` |
| `23514` (check_violation) | `chk_jobs_max_attempts` | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"max_attempts must be between 1 and 20"}}` |
| `23514` (check_violation) | `chk_jobs_timeout` | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"timeout_seconds must be between 1 and 86400"}}` |
| `23514` (check_violation) | `chk_workers_total_slots_range` | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"total_slots must be between 1 and 1024"}}` |
| `23514` (check_violation) | `chk_workers_version_length` | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"version length must not exceed 64 characters"}}` |
| `23514` (check_violation) | `chk_workers_supported_tasks_count`| `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"supported_tasks must contain at most 16 tasks"}}` |
| `23514` (check_violation) | `chk_attempts_last_error_size` | `domain.ErrInvalidRequest` (generic fall-through, no custom message) | `400 INVALID_REQUEST` | Generic fall-through, no custom message |
| `23514` (check_violation) | `chk_attempts_error_class` | `domain.ErrInvalidRequest` (generic fall-through, no custom message) | `400 INVALID_REQUEST` | Column is coordinator-written, not client-supplied; violation indicates an internal bug, not bad client input (generic fall-through, no custom message) |
| `23514` (check_violation) | `chk_jobs_assignment_shape` | `domain.ErrInvalidRequest` (generic fall-through, no custom message) | `400 INVALID_REQUEST` | Column is coordinator-written, not client-supplied; violation indicates an internal bug, not bad client input (generic fall-through, no custom message) |
| `23514` (check_violation) | `chk_jobs_assignment_lapses` | `domain.ErrInvalidRequest` (generic fall-through, no custom message) | `400 INVALID_REQUEST` | Column is coordinator-written, not client-supplied; violation indicates an internal bug, not bad client input (generic fall-through, no custom message) |
| `23514` (check_violation) | `chk_jobs_fallback_reason` | `domain.ErrInvalidRequest` (generic fall-through, no custom message) | `400 INVALID_REQUEST` | Column is coordinator-written, not client-supplied; violation indicates an internal bug, not bad client input (generic fall-through, no custom message) |
| `22001` (string_data_right_truncation) | Any | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"field length exceeds maximum allowed characters"}}` |
| `22003` (numeric_value_out_of_range) | Any | `domain.ErrInvalidRequest` | `400 INVALID_REQUEST` | `{"error":{"code":"INVALID_REQUEST","message":"numeric value out of range"}}` |
| Connection / I/O Timeout | Dial / DeadlineExceeded | Connection / Timeout Error | `503 SERVICE_UNAVAILABLE` | `{"error":{"code":"SERVICE_UNAVAILABLE","message":"service temporarily unavailable"}}` (Header: `Retry-After: 1`) |
| Unmapped Driver Error | Unhandled SQLSTATE | Unmapped internal error | `500 INTERNAL_ERROR` | `{"error":{"code":"INTERNAL_ERROR","message":"internal server error"}}` (Scrubbed server logs) |
