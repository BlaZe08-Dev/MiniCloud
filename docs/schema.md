# MiniCloud PostgreSQL Database Schema Specification

## 1. Overview & Architectural Role

In MiniCloud V1 Phase 3, PostgreSQL serves as the authoritative, durable system of record for all jobs, execution attempts, registered workers, execution results, and lifecycle audit events.

### Core Database Design Principles
1. **Invariants Enforced in SQL:** Rather than relying exclusively on application memory checks, critical distributed system invariants (such as at most one active attempt per job, single terminal result, and non-overlapping attempt numbers) are enforced by database schema constraints and partial unique indexes.
2. **Bounded Row Storage:** In accordance with MiniCloud's bounded resource invariant, all variable-length fields (`payload`, `output`, `error`) have explicit `CHECK` constraints enforcing hard length limits. Unbounded blobs (`TEXT` or `BYTEA` without length checks) are strictly prohibited.
3. **Database Clock Authority & Transaction Timestamp Semantics:**
   - In PostgreSQL, `now()` and `CURRENT_TIMESTAMP` return the **start time of the current transaction**, which remains frozen across all statements within that transaction block.
   - Where sub-statement execution freshness inside a multi-statement transaction matters (e.g., audit event recording or precise dispatch duration logging), the schema and queries use **`clock_timestamp()`**.
   - All lease tracking, heartbeat evaluations, and timeout checks stay strictly on database time. Clock drift between worker nodes or coordinator processes cannot corrupt lease or heartbeat validity.
4. **Dynamic Capacity Derivation (No Counter Drift):** Storing mutable integer counters (`available_slots`) that are incremented on commit and decremented on dispatch is vulnerable to counter drift during coordinator crashes, dropped connections, or worker reaps. MiniCloud derives available capacity directly from active running attempts in SQL.
5. **Audit Immutability:** Execution attempts, audit events, and committed results are append-only. Foreign keys prevent accidental orphaned data or cascading deletions during normal operations.

---

## 2. Entity-Relationship Model

```mermaid
erDiagram
    jobs ||--o{ attempts : "has history of"
    jobs ||--o| results : "has at most one"
    jobs ||--o{ job_events : "generates audit stream"
    workers ||--o{ attempts : "executes"
    attempts ||--o| results : "produces"

    jobs {
        text id PK
        varchar task_type
        bytea payload
        varchar state
        int max_attempts
        int timeout_seconds
        text current_attempt_id
        timestamptz created_at
        timestamptz updated_at
    }

    attempts {
        text id PK
        text job_id FK
        int attempt_no
        text worker_id FK
        char lease_token_hash
        varchar state
        timestamptz started_at
        timestamptz finished_at
        int exit_code
        text output
        text error
    }

    results {
        text job_id PK, FK
        text attempt_id FK
        int exit_code
        text output
        text error
        timestamptz committed_at
    }

    workers {
        text id PK
        varchar version
        int total_slots
        varchar status
        text[] supported_tasks
        timestamptz registered_at
        timestamptz last_heartbeat_at
    }

    job_events {
        bigserial id PK
        text job_id FK
        text attempt_id FK
        varchar event_type
        varchar from_state
        varchar to_state
        jsonb details
        timestamptz created_at
    }
```

---

## 3. Detailed Table Definitions & SQL DDL

### 3.1 Table: `jobs`
The root entity representing submitted computational work.

```sql
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    task_type VARCHAR(64) NOT NULL,
    payload BYTEA NOT NULL,
    state VARCHAR(32) NOT NULL,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    timeout_seconds INTEGER NOT NULL DEFAULT 60,
    current_attempt_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Size bounds (Invariant: bounded payload 64 KiB)
    CONSTRAINT chk_jobs_payload_size 
        CHECK (octet_length(payload) <= 65536),

    -- Valid FSM states
    CONSTRAINT chk_jobs_state 
        CHECK (state IN (
            'SUBMITTED', 'QUEUED', 'RUNNING', 
            'CANCEL_REQUESTED', 'CANCELLED', 
            'SUCCEEDED', 'FAILED', 'RETRYING', 'DEAD_LETTER'
        )),

    -- Logical constraints
    CONSTRAINT chk_jobs_max_attempts 
        CHECK (max_attempts > 0 AND max_attempts <= 20),
    CONSTRAINT chk_jobs_timeout 
        CHECK (timeout_seconds > 0 AND timeout_seconds <= 86400)
);

-- Indexes for dispatch and querying
CREATE INDEX idx_jobs_dispatch 
    ON jobs (created_at ASC) 
    WHERE state = 'QUEUED';

CREATE INDEX idx_jobs_task_dispatch 
    ON jobs (task_type, created_at ASC) 
    WHERE state = 'QUEUED';

CREATE INDEX idx_jobs_created_at 
    ON jobs (created_at DESC);
```

### 3.2 Table: `workers` & Dynamic Capacity View
Tracks registered worker processes and static capacity.

```sql
CREATE TABLE workers (
    id TEXT PRIMARY KEY,
    version VARCHAR(64) NOT NULL,
    total_slots INTEGER NOT NULL,
    status VARCHAR(32) NOT NULL,
    supported_tasks TEXT[] NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT chk_workers_slots 
        CHECK (total_slots > 0),
    CONSTRAINT chk_workers_status 
        CHECK (status IN ('HEALTHY', 'UNHEALTHY', 'DRAINING', 'TERMINATED'))
);

-- Index for background reaper sweep
CREATE INDEX idx_workers_liveness 
    ON workers (last_heartbeat_at ASC) 
    WHERE status = 'HEALTHY';

-- Dynamic Capacity View: Derives available slots from active non-terminal (PENDING, RUNNING) attempts (Hardened in 000002)
-- Eliminates counter drift caused by coordinator crashes, transaction rollbacks, or dead workers.
CREATE VIEW worker_capacity AS
SELECT 
    w.id,
    w.version,
    w.total_slots,
    w.status,
    w.supported_tasks,
    w.registered_at,
    w.last_heartbeat_at,
    GREATEST(0, w.total_slots - COUNT(a.id) FILTER (WHERE a.state IN ('PENDING', 'RUNNING')))::integer AS available_slots
FROM workers w
LEFT JOIN attempts a ON a.worker_id = w.id
GROUP BY w.id;
```

### 3.3 Table: `attempts`
Represents an individual execution attempt assigned to a worker under a specific lease.

```sql
CREATE TABLE attempts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    attempt_no INTEGER NOT NULL,
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE RESTRICT,
    lease_token_hash CHAR(64) NOT NULL,
    state VARCHAR(32) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ,
    exit_code INTEGER,
    output TEXT,
    error TEXT,

    -- Monotonic attempt numbering per job
    CONSTRAINT chk_attempts_attempt_no 
        CHECK (attempt_no > 0),

    -- Distinct attempt sequence per job
    CONSTRAINT uq_attempts_job_attempt_no 
        UNIQUE (job_id, attempt_no),

    -- Valid attempt states
    CONSTRAINT chk_attempts_state 
        CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'LOST')),

    -- Bounded output and error size (Invariant: bounded output 64 KiB, error 10 KiB)
    CONSTRAINT chk_attempts_output_size 
        CHECK (output IS NULL OR octet_length(output) <= 65536),
    CONSTRAINT chk_attempts_error_size 
        CHECK (error IS NULL OR octet_length(error) <= 10240)
);

-- CRITICAL DISTRIBUTED INVARIANT: At most one active non-terminal (PENDING, RUNNING) attempt per job (Hardened in 000002)
CREATE UNIQUE INDEX idx_attempts_active_per_job 
    ON attempts (job_id) 
    WHERE state IN ('PENDING', 'RUNNING');

CREATE INDEX idx_attempts_job_id 
    ON attempts (job_id, attempt_no ASC);

CREATE INDEX idx_attempts_worker_liveness 
    ON attempts (worker_id, state);
```

### 3.4 Table: `results`
Holds the definitive, final committed outcome for a completed or failed job.

```sql
CREATE TABLE results (
    job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE RESTRICT,
    exit_code INTEGER NOT NULL,
    output TEXT NOT NULL,
    error TEXT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Bounded output and error size
    CONSTRAINT chk_results_output_size 
        CHECK (octet_length(output) <= 65536),
    CONSTRAINT chk_results_error_size 
        CHECK (octet_length(error) <= 10240)
);

CREATE INDEX idx_results_committed_at 
    ON results (committed_at DESC);
```

### 3.5 Table: `job_events`
Immutable, append-only lifecycle event stream for auditing and debugging.

```sql
CREATE TABLE job_events (
    id BIGSERIAL PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    attempt_id TEXT REFERENCES attempts(id) ON DELETE SET NULL,
    event_type VARCHAR(64) NOT NULL,
    from_state VARCHAR(32),
    to_state VARCHAR(32),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_job_events_job_timeline 
    ON job_events (job_id, created_at ASC);

CREATE INDEX idx_job_events_type 
    ON job_events (event_type, created_at DESC);
```

---

## 4. Invariants Enforced Directly in SQL

| Invariant | Implementation in Schema | Violation Behavior |
|---|---|---|
| **At most one active attempt per job** | `CREATE UNIQUE INDEX idx_attempts_active_per_job ON attempts (job_id) WHERE state IN ('PENDING', 'RUNNING');` | Database rejects concurrent dispatch with `SQLSTATE 23505` (unique constraint violation). |
| **No Capacity Counter Drift** | `worker_capacity` view deriving `available_slots = total_slots - count(attempts WHERE state IN ('PENDING', 'RUNNING'))`. | Capacity is physically derived from active attempts; zero counter drift across restarts or crashes. |
| **Exactly one committed result per job** | `PRIMARY KEY (job_id)` on `results` table with foreign key to `jobs(id)`. | Stale or duplicate commits fail immediately at the database layer. |
| **Monotonically unique attempt numbers** | `CONSTRAINT uq_attempts_job_attempt_no UNIQUE (job_id, attempt_no)`. | Attempt numbers for a job can never duplicate or collide. |
| **Bounded Payload (64 KiB)** | `CHECK (octet_length(payload) <= 65536)`. | Rejects oversized payloads at SQL insert time. |
| **Bounded Output (64 KiB)** | `CHECK (octet_length(output) <= 65536)`. | Prevents rogue worker output buffer exhaustion in storage. |
| **Bounded Error Summary (10 KiB)** | `CHECK (octet_length(error) <= 10240)`. | Prevents massive stack traces or core dumps from ballooning database size. |
| **Database-Authoritative Timestamps** | `CURRENT_TIMESTAMP` for transaction boundaries; `clock_timestamp()` for event sequencing. | Eliminates worker/coordinator clock skew vulnerability. |
| **Token Privacy** | `lease_token_hash CHAR(64)` storing SHA-256 digest. | Database dumps never reveal live bearer credentials. |

---

## 5. PROPOSED Phase 4 Migration Outline (`000004_leases_retries_cancellation.sql`)

*Status: PROPOSED (Design only; no SQL migration files created until Slice 4b)*

To support attempt leases, retry backoff scheduling, structured failure classification, and cooperative cancellation without downtime, Migration 000004 is planned with forward-only schema enhancements:

```sql
-- Step 1: Add lease_expires_at to attempts
-- Added with a safe default for existing historical rows, then constraint tightened.
ALTER TABLE attempts 
    ADD COLUMN lease_expires_at TIMESTAMPTZ NOT NULL 
    DEFAULT (CURRENT_TIMESTAMP + INTERVAL '30 seconds');

-- Step 2: Add failure classification and error tracking to attempts
ALTER TABLE attempts 
    ADD COLUMN error_class VARCHAR(32),
    ADD COLUMN last_error TEXT;

-- Enforce check constraints with NOT VALID then VALIDATE to avoid exclusive lock contention
ALTER TABLE attempts 
    ADD CONSTRAINT chk_attempts_error_class 
    CHECK (error_class IS NULL OR error_class IN ('PERMANENT', 'RETRYABLE', 'UNKNOWN')) 
    NOT VALID;
ALTER TABLE attempts VALIDATE CONSTRAINT chk_attempts_error_class;

ALTER TABLE attempts 
    ADD CONSTRAINT chk_attempts_last_error_size 
    CHECK (last_error IS NULL OR octet_length(last_error) <= 10240) 
    NOT VALID;
ALTER TABLE attempts VALIDATE CONSTRAINT chk_attempts_last_error_size;

-- Step 3: Add retry backoff and cancellation tracking to jobs
ALTER TABLE jobs 
    ADD COLUMN not_before TIMESTAMPTZ,
    ADD COLUMN cancel_requested_at TIMESTAMPTZ;

-- Step 4: Index for high-performance coordinator lease expiry sweeper
CREATE INDEX idx_attempts_lease_expiry 
    ON attempts (lease_expires_at ASC) 
    WHERE state = 'RUNNING';

-- Step 5: Index for retry-eligible job dispatch
CREATE INDEX idx_jobs_retrying_dispatch 
    ON jobs (not_before ASC, created_at ASC) 
    WHERE state = 'RETRYING';
```

### Canonical State Names & Schema Alignment
- **Canonical Attempt States**: Migration `000001_initial_schema.sql:97` already defines `chk_attempts_state` with `CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMED_OUT', 'CANCELLED', 'LOST'))`. No schema migration is required for attempt states.
- **State Reconciliation & Mapping**:
  - `LOST`: Worker lease expired without renewal (replaces legacy domain `EXPIRED`). Written by sweeper transaction.
  - `TIMED_OUT`: Attempt exceeded cumulative `timeout_seconds`. Written by timeout monitor or sweeper.
  - `CANCELLED`: Canonical double-L spelling across both job and attempt states. Written by worker cancel acknowledgment or sweeper.

### Sweeper Lock Order, Candidate Selection, and Correctness Re-Check
In accordance with ADR-0014, the sweeper evaluates candidate attempts and executes per-candidate atomic transitions under strict lock ordering:

1. **Candidate Scan (Unlocked Read)**:
   ```sql
   SELECT a.id, a.job_id, a.worker_id
   FROM attempts a
   WHERE a.state = 'RUNNING'
     AND a.lease_expires_at < clock_timestamp()
   ORDER BY a.lease_expires_at ASC
   LIMIT 50;
   ```
   *Why Candidate Scan is Unlocked*: The candidate scan performs an unlocked read against `idx_attempts_lease_expiry` to avoid holding long-lived transaction locks across batch queries.

2. **Per-Candidate Lock Acquisition Order (`jobs -> workers -> attempts`)**:
   For each candidate, the sweeper begins an isolated transaction and acquires row locks in canonical order using `SKIP LOCKED` / `NOWAIT`:
   ```sql
   -- Step 1: Lock parent job row
   SELECT id, state, max_attempts FROM jobs WHERE id = $1 FOR UPDATE SKIP LOCKED;

   -- Step 2: Lock assigned worker row
   SELECT id FROM workers WHERE id = $1 FOR UPDATE SKIP LOCKED;

   -- Step 3: Lock attempt row and execute correctness re-check
   SELECT id, state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE SKIP LOCKED;
   ```

3. **Lock Failure Handling**:
   If any row lock cannot be acquired immediately (for example, if a worker is currently executing `CommitAttempt` or `RenewAttemptLease` on the same rows), `SKIP LOCKED` ensures the sweeper does not block or deadlock. The sweeper cleanly skips that candidate without mutating state, rolls back the sub-transaction, and re-evaluates the candidate on the subsequent sweeper tick.

4. **Re-Check Inside the Locks (Correctness Invariants)**:
   Under exclusive row locks, the sweeper verifies:
   - `attempts.state == 'RUNNING'`: Ensures the worker has not already committed.
   - `attempts.lease_expires_at < clock_timestamp()`: Ensures a concurrent renewal has not extended the lease.
   - **Grace Conditions**: Ensures the coordinator is not within `MINICLOUD_STARTUP_GRACE_PERIOD` (30s after startup) or `MINICLOUD_OUTAGE_GRACE_WINDOW` (30s after database recovery).
   If any condition is not met, the transaction rolls back cleanly with zero mutation.

5. **Resolving `CANCEL_REQUESTED` Jobs**:
   If `jobs.state == 'CANCEL_REQUESTED'`, the sweeper transitions the attempt to `LOST` (or `CANCELLED`) and transitions the job directly to `CANCELLED` (terminal), suppressing automated retry redelivery even if `attempt_no < max_attempts`.

### Startup and Outage Grace Mechanisms
To protect Phase 3 surviving-worker commit guarantees across coordinator restarts and database outages (`TestPostgresWiring_CoordinatorRestartRecovery`, `TestSecurity_PartD_Outage_MidDispatchAndMidCommit`), the coordinator sweeper suppresses reaping passes for 30 seconds following startup (`MINICLOUD_STARTUP_GRACE_PERIOD`) or database outage restoration (`MINICLOUD_OUTAGE_GRACE_WINDOW`). Both periods must be $\ge \text{lease duration}$ (default 30s).

### Operational Invariants & Rollback Policy
- **Forward-Only**: In accordance with [ADR 0010: Database Migrations Policy and Runner Architecture](adr/0010-migrations-policy.md), MiniCloud strictly employs forward-only migrations; no down migrations are supported.
- **Zero Lock Contention**: Constraint addition uses `NOT VALID` followed by `VALIDATE CONSTRAINT` ensuring table rewrites and exclusive table locks are avoided during coordinator execution (ADR-0013).

