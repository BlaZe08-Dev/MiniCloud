Task: O1-CHECK
Branch: agent/gemini/O1-check
Work tip (last commit before this report): 9530e65ca8c4d5ea1f7375b15e320ae48f9d1990
Date: 2026-11-16

# O1-CHECK: HTML Escaping Payload Expansion Investigation

## 1. Verdict

**CONFIRMED**

The suspicion is confirmed in full by empirical testing across both the isolated in-memory API stack (Step 1) and the production-compiled coordinator/worker binaries running against PostgreSQL database `minicloud_gemini_test` (Step 2).

---

## 2. Root Cause Analysis

1. **Coordinator Response Writer**:
   In `internal/api/handlers.go`, `writeJSON()` writes HTTP responses using `json.NewEncoder(w).Encode(data)`. Go's `json.NewEncoder` defaults to `escapeHTML = true`. Consequently, any characters `<`, `>`, `&` are escaped to 6-byte Unicode escape sequences (`\u003c`, `\u003e`, `\u0026`), and Unicode line/paragraph separators U+2028 and U+2029 (3 bytes in UTF-8) are escaped to 6-byte sequences `\u2028` and `\u2029`.

2. **Poll Response Wire Bloat**:
   When a worker polls via `POST /v1/workers/{id}/poll`, `internal/coordinator/service.go` delivers `WorkAssignment`, in which `Payload` is defined as `json.RawMessage`. Because `Payload` is serialized through `writeJSON()`, Go's JSON encoder re-escapes the raw JSON payload bytes on the wire.

3. **Worker Pre-Execution Size Guard**:
   In `internal/worker/client.go`, the worker client decodes `WorkAssignment` from the poll response. Because `WorkAssignment.Payload` is `json.RawMessage`, `json.NewDecoder` leaves the payload as raw wire bytes without unescaping. Before invoking the task or unmarshaling JSON, `executor.Registry.Execute()` in `internal/executor/executor.go` enforces:
   ```go
   if len(payload) > MaxPayloadSizeBytes { // 65,536 bytes
       return "", 1, fmt.Errorf("%w: payload size %d exceeds %d bytes", domain.ErrPayloadTooLarge, len(payload), MaxPayloadSizeBytes)
   }
   ```
   The expanded wire bytes exceed 65,536 bytes, triggering `ErrPayloadTooLarge`.

4. **Permanent Rejection Without Retry**:
   `executor.Classify(err)` in `internal/executor/classify.go` classifies `domain.ErrPayloadTooLarge` as `domain.ClassPermanent`. The worker reports this error classification back to the coordinator. The coordinator commits the attempt as `FAILED` and permanently transitions the job to `FAILED` on attempt 1, bypassing any remaining retry attempts (`max_attempts`).

---

## 3. Step 1: In-Memory API Stack Reproduction

Executed using the real API handler stack (`api.NewServer`) and in-memory repository (`storage.NewInMemoryRepository`).

Target payload: `{"message":"..."}` with content sized close to 64 KiB (submitted payload length = 65,000 bytes, well below the coordinator limit of 65,536 bytes).

```
================================================================================
STEP 1: In-memory API stack reproduction of HTML escaping payload expansion
================================================================================
CASE: less-than '<'
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  389930 bytes (expansion: 6.00x)
  Worker Execute Exit Code: 1
  Worker Execute Error:     payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Error Classification:     PERMANENT

CASE: greater-than '>'
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  389930 bytes (expansion: 6.00x)
  Worker Execute Exit Code: 1
  Worker Execute Error:     payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Error Classification:     PERMANENT

CASE: ampersand '&'
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  389930 bytes (expansion: 6.00x)
  Worker Execute Exit Code: 1
  Worker Execute Error:     payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Error Classification:     PERMANENT

CASE: U+2028 line separator
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  129986 bytes (expansion: 2.00x)
  Worker Execute Exit Code: 1
  Worker Execute Error:     payload exceeds maximum allowed size: payload size 129986 exceeds 65536 bytes
  Error Classification:     PERMANENT

CASE: U+2029 paragraph separator
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  129986 bytes (expansion: 2.00x)
  Worker Execute Exit Code: 1
  Worker Execute Error:     payload exceeds maximum allowed size: payload size 129986 exceeds 65536 bytes
  Error Classification:     PERMANENT

CASE: control letters 'a'
  Submitted Payload Length: 65000 bytes
  Received Payload Length:  65000 bytes (expansion: 1.00x)
  Worker Execute Exit Code: 0
  Worker Execute Error:     <nil>
```

Evidence artifact: `docs/agents/reviews/O1-check-evidence/step1_output.txt`.

---

## 4. Step 2: Real Binaries + PostgreSQL (`minicloud_gemini_test`)

Real `bin/coordinator` and `bin/worker` processes were launched pointing at `minicloud_gemini_test`. Jobs were submitted over HTTP using `curl`, and query endpoints `GET /v1/jobs/{id}` and `GET /v1/jobs/{id}/attempts` were evaluated upon completion.

```
================================================================================
STEP 2 RESULTS (Real Binaries + minicloud_gemini_test Database):
================================================================================
TEST CASE: less-than '<'
  Job ID:                   job_1_bbe624454675
  Submitted Payload Length: 65000 bytes
  Final Job State:          FAILED
  Total Attempts:           1
    - Attempt #1 State: FAILED, ExitCode: 1, ErrorClass: PERMANENT
      Error: payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Job Result: ExitCode=1, OutputLen=0, Error=payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes

TEST CASE: greater-than '>'
  Job ID:                   job_2_de5607f0115a
  Submitted Payload Length: 65000 bytes
  Final Job State:          FAILED
  Total Attempts:           1
    - Attempt #1 State: FAILED, ExitCode: 1, ErrorClass: PERMANENT
      Error: payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Job Result: ExitCode=1, OutputLen=0, Error=payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes

TEST CASE: ampersand '&'
  Job ID:                   job_3_b46e07d1884d
  Submitted Payload Length: 65000 bytes
  Final Job State:          FAILED
  Total Attempts:           1
    - Attempt #1 State: FAILED, ExitCode: 1, ErrorClass: PERMANENT
      Error: payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes
  Job Result: ExitCode=1, OutputLen=0, Error=payload exceeds maximum allowed size: payload size 389930 exceeds 65536 bytes

TEST CASE: U+2028 line separator
  Job ID:                   job_4_7d1a8a54a14f
  Submitted Payload Length: 65000 bytes
  Final Job State:          FAILED
  Total Attempts:           1
    - Attempt #1 State: FAILED, ExitCode: 1, ErrorClass: PERMANENT
      Error: payload exceeds maximum allowed size: payload size 129986 exceeds 65536 bytes
  Job Result: ExitCode=1, OutputLen=0, Error=payload exceeds maximum allowed size: payload size 129986 exceeds 65536 bytes

TEST CASE: control letters 'a'
  Job ID:                   job_5_7b4b4004a832
  Submitted Payload Length: 65000 bytes
  Final Job State:          SUCCEEDED
  Total Attempts:           1
    - Attempt #1 State: COMPLETED, ExitCode: 0, ErrorClass: 
  Job Result: ExitCode=0, OutputLen=65009, Error=None

TEST CASE: boundary 10,920 '<' (executes)
  Job ID:                   job_6_c3ac5b27358c
  Submitted Payload Length: 10934 bytes
  Final Job State:          SUCCEEDED
  Total Attempts:           1
    - Attempt #1 State: COMPLETED, ExitCode: 0, ErrorClass: 
  Job Result: ExitCode=0, OutputLen=65536, Error=None

TEST CASE: boundary 10,921 '<' (fails)
  Job ID:                   job_7_aad06a52cedd
  Submitted Payload Length: 10935 bytes
  Final Job State:          FAILED
  Total Attempts:           1
    - Attempt #1 State: FAILED, ExitCode: 1, ErrorClass: PERMANENT
      Error: payload exceeds maximum allowed size: payload size 65540 exceeds 65536 bytes
  Job Result: ExitCode=1, OutputLen=0, Error=payload exceeds maximum allowed size: payload size 65540 exceeds 65536 bytes
```

Key Finding: Every affected job failed permanently on Attempt 1 with `error_class: PERMANENT`. Bounded retries (`max_attempts: 3`) were never triggered.

Evidence artifact: `docs/agents/reviews/O1-check-evidence/step2_output.txt`.

---

## 5. Step 3: Exact Boundary Limits

Each `<` character expands by +5 bytes (from 1 byte to 6 bytes: `\u003c`).

### 1. Object shape: `{"message":"<...<"}` (wrapper overhead = 14 bytes)
- **Largest number of `<` that executes successfully**:
  - **10,920** `<` characters.
  - Submitted payload size: `14 + 10,920 = 10,934 bytes`.
  - Wire size on poll: `14 + 6 * 10,920 = 65,534 bytes` (<= 65,536).
  - Outcome: `SUCCEEDED`, ExitCode=0.
- **Smallest object payload that is accepted at submission but fails on worker**:
  - **10,921** `<` characters.
  - Submitted payload size: `14 + 10,921 = 10,935 bytes`.
  - Wire size on poll: `14 + 6 * 10,921 = 65,540 bytes` (> 65,536).
  - Outcome: `FAILED`, ExitCode=1, `PERMANENT`.

### 2. Bare JSON string shape: `"<...<"` (wrapper overhead = 2 bytes)
- **Largest number of `<` that executes successfully**:
  - **10,922** `<` characters.
  - Submitted payload size: `2 + 10,922 = 10,924 bytes`.
  - Wire size on poll: `2 + 6 * 10,922 = 65,534 bytes`.
  - Outcome: `SUCCEEDED`, ExitCode=0.
- **Absolute smallest submitted payload that is accepted but fails on worker**:
  - **10,923** `<` characters.
  - Submitted payload size: `2 + 10,923 = 10,925 bytes` (~10.67 KiB).
  - Wire size on poll: `2 + 6 * 10,923 = 65,540 bytes`.
  - Outcome: `FAILED`, ExitCode=1, `PERMANENT`.

Any valid submitted payload between 10,925 bytes and 65,536 bytes containing sufficient `< > &` characters is accepted at submission but permanently fails on the worker.

---

## 6. Step 4: Other Affected Paths

`writeJSON()` in `internal/api/handlers.go` is called across all HTTP response paths:
1. `POST /v1/jobs` (201 Created): Returns `job.Payload` with HTML escaping.
2. `GET /v1/jobs/{id}` (200 OK): Returns `job.Payload` and `job.Result.Output`/`Error` with HTML escaping. Clients reading back their submitted payload receive altered bytes.
3. `GET /v1/jobs/{id}/attempts` (200 OK): Returns attempt `Output` and `Error` with HTML escaping.
4. `GET /v1/workers` (200 OK): Worker version strings or custom task names containing `< > &` are escaped.
5. `writeError()` (4xx / 5xx): Error messages containing mathematical or comparison symbols (`<`, `>`) or XML/HTML tags are escaped into `\u003c`, `\u003e`, `\u0026`.

---

## 7. Step 5: Fix Proposals and Recommendation

### Option (a): Set `enc.SetEscapeHTML(false)` in `writeJSON()`
- **Mechanism**:
  In `internal/api/handlers.go`:
  ```go
  func writeJSON(w http.ResponseWriter, status int, data any) {
      w.Header().Set("Content-Type", "application/json")
      w.WriteHeader(status)
      enc := json.NewEncoder(w)
      enc.SetEscapeHTML(false)
      _ = enc.Encode(data)
  }
  ```
- **Files touched**: `internal/api/handlers.go` (1 file, lines 79-83).
- **Compatibility on /v1**:
  - Additive-only, fully backward and forward compatible.
  - RFC 8259 Section 7 states `< > &` are valid unescaped JSON string characters. All standard JSON decoders parse both raw and escaped characters identically.
  - Preserves exact byte fidelity between submitted `json.RawMessage`, PostgreSQL `BYTEA` storage, worker assignments, and client GET queries.
- **Tests needed**:
  - Unit test in `internal/api/api_test.go` submitting a 65,000-byte payload with `< > & \u2028 \u2029`, verifying `assignment.Payload` received by worker retains exact submitted length and executes without error.
  - Test verifying `GET /v1/jobs/{id}` returns raw payload matching submitted payload.
- **Risk**: Very low. MiniCloud is a REST JSON API, not an HTML template generator; JSON HTML-escaping was designed for inline HTML `<script>` tags, which MiniCloud does not serve.

### Option (b): Worker limit applied after decoding instead of raw bytes
- **Mechanism**:
  Defer or replace `len(payload) > MaxPayloadSizeBytes` in `executor.Registry.Execute()` until after task-specific decoding/unmarshaling.
- **Files touched**: `internal/executor/executor.go`, `internal/executor/echo.go`, `internal/executor/sleep.go`, `internal/executor/sha256.go`, `internal/worker/client.go`.
- **Compatibility on /v1**:
  - High risk of architectural regression: violates defense-in-depth boundary enforcement where untrusted payload size is bounded before invoking deserialization.
  - Does not resolve payload corruption on `GET /v1/jobs/{id}` or `POST /v1/jobs`.
  - Exposes workers to resource exhaustion (decompression/expansion bombs) during unmarshaling before size bounds are checked.
- **Tests needed**: Comprehensive fuzz and benchmark tests across all tasks for decoded size limits.
- **Risk**: High.

### Recommendation
**Adopt Option (a)** (`enc.SetEscapeHTML(false)`). It addresses the bug at its single point of origin, guarantees wire byte fidelity, requires touching only one file, and eliminates the defect across all API endpoints simultaneously.

### Security Finding Proposal
- **Needs Security Finding ID**: Yes.
- **Severity**: **Medium** (CWE-172: Encoding Error / Denial of Service on Valid Workloads).
- **Proposed Finding Wording**:
  - *Title*: Coordinator HTTP JSON encoder HTML-escapes raw payloads, expanding legal payloads beyond worker execution limit.
  - *Summary*: In `internal/api/handlers.go:writeJSON`, responses are encoded using `json.NewEncoder(w).Encode(data)` without `SetEscapeHTML(false)`. Go's encoder escapes `<`, `>`, `&`, and U+2028/U+2029 into 6-byte escape sequences. When a worker polls for work, `WorkAssignment.Payload` (`json.RawMessage`) expands up to 6x on the wire. A legal payload under the 64 KiB coordinator limit (as small as 10,925 bytes) expands past 65,536 bytes. The worker's `executor.Registry.Execute` checks raw byte length before decoding and permanently rejects the assignment with `ErrPayloadTooLarge` (`PERMANENT` error class), aborting valid jobs without retries.
