-- MiniCloud Initial Schema Migration
-- Migration: 000001_initial_schema.sql
-- Forward-only initial schema definition adhering strictly to docs/schema.md.
--
-- Note on Column Types and Size Checks (Adjustment 2):
-- - jobs.payload: BYTEA (size bounded to 64 KiB via octet_length(payload) <= 65536)
-- - attempts.output & results.output: TEXT (size bounded to 64 KiB via octet_length(output) <= 65536)
-- - attempts.error & results.error: TEXT (size bounded to 10 KiB via octet_length(error) <= 10240)
-- PostgreSQL octet_length() accurately measures byte length for both BYTEA and TEXT.

-- 1. Table: jobs
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

CREATE INDEX idx_jobs_dispatch 
    ON jobs (created_at ASC) 
    WHERE state = 'QUEUED';

CREATE INDEX idx_jobs_task_dispatch 
    ON jobs (task_type, created_at ASC) 
    WHERE state = 'QUEUED';

CREATE INDEX idx_jobs_created_at 
    ON jobs (created_at DESC);

-- 2. Table: workers
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

CREATE INDEX idx_workers_liveness 
    ON workers (last_heartbeat_at ASC) 
    WHERE status = 'HEALTHY';

-- 3. Table: attempts
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

-- CRITICAL DISTRIBUTED INVARIANT: At most one active (RUNNING) attempt per job
CREATE UNIQUE INDEX idx_attempts_active_per_job 
    ON attempts (job_id) 
    WHERE state = 'RUNNING';

CREATE INDEX idx_attempts_job_id 
    ON attempts (job_id, attempt_no ASC);

CREATE INDEX idx_attempts_worker_liveness 
    ON attempts (worker_id, state);

-- 4. Dynamic Capacity View: Derives available slots from active RUNNING attempts
CREATE VIEW worker_capacity AS
SELECT 
    w.id,
    w.version,
    w.total_slots,
    w.status,
    w.supported_tasks,
    w.registered_at,
    w.last_heartbeat_at,
    GREATEST(0, w.total_slots - COUNT(a.id) FILTER (WHERE a.state = 'RUNNING'))::integer AS available_slots
FROM workers w
LEFT JOIN attempts a ON a.worker_id = w.id
GROUP BY w.id;

-- 5. Table: results
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

-- 6. Table: job_events
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
