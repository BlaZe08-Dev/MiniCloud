-- MiniCloud Phase 4 Migration
-- Migration: 000004_leases_retries_cancellation.sql
-- Adds attempt leases, retry backoff scheduling, failure classification and cancellation tracking
-- (docs/schema.md section 5, ADR-0014, ADR-0015, ADR-0016). Forward-only (ADR-0010).
--
-- Existing rows: attempts that already exist (in any state) receive a lease_expires_at of
-- "migration time + 30 seconds" from the temporary column default below, so a RUNNING attempt of a
-- pre-upgrade worker is not instantly reapable and keeps the Phase 3 commit guarantee. The default
-- is dropped immediately afterwards: every later INSERT must state its lease explicitly.
-- The check constraints are added NOT VALID and then VALIDATED, as in 000003.
-- The unique index on DIAGNOSTIC_LATE_RESULT events fails (loudly, rolling the whole migration
-- back) if duplicate diagnostics already exist; the shipped code never writes duplicates.

-- Step 1: attempts.lease_expires_at, backfilled, then the default is removed
ALTER TABLE attempts
    ADD COLUMN lease_expires_at TIMESTAMPTZ NOT NULL
    DEFAULT (CURRENT_TIMESTAMP + INTERVAL '30 seconds');

ALTER TABLE attempts
    ALTER COLUMN lease_expires_at DROP DEFAULT;

-- Step 2: failure classification and error tracking on attempts
ALTER TABLE attempts
    ADD COLUMN error_class VARCHAR(32),
    ADD COLUMN last_error TEXT;

ALTER TABLE attempts
    ADD CONSTRAINT chk_attempts_error_class
    CHECK (error_class IS NULL OR error_class IN ('PERMANENT', 'TRANSIENT', 'UNKNOWN', 'LEASE_EXPIRED'))
    NOT VALID;
ALTER TABLE attempts VALIDATE CONSTRAINT chk_attempts_error_class;

ALTER TABLE attempts
    ADD CONSTRAINT chk_attempts_last_error_size
    CHECK (last_error IS NULL OR octet_length(last_error) <= 10240)
    NOT VALID;
ALTER TABLE attempts VALIDATE CONSTRAINT chk_attempts_last_error_size;

-- Step 3: retry backoff and cancellation tracking on jobs
ALTER TABLE jobs
    ADD COLUMN not_before TIMESTAMPTZ,
    ADD COLUMN cancel_requested_at TIMESTAMPTZ;

-- Step 4: lease expiry sweeper candidate scan
CREATE INDEX idx_attempts_lease_expiry
    ON attempts (lease_expires_at ASC)
    WHERE state = 'RUNNING';

-- Step 5: retry-eligible dispatch
CREATE INDEX idx_jobs_retrying_dispatch
    ON jobs (not_before ASC, created_at ASC)
    WHERE state = 'RETRYING';

-- Step 6: at most one DIAGNOSTIC_LATE_RESULT per (job, attempt); arbitrates ON CONFLICT DO NOTHING
-- and bounds unauthenticated audit-table growth (SEC-004).
CREATE UNIQUE INDEX idx_job_events_unique_diagnostic_late_result
    ON job_events (job_id, (details->>'attempt_id'))
    WHERE event_type = 'DIAGNOSTIC_LATE_RESULT';
