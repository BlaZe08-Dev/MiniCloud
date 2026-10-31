-- MiniCloud ADR-0017 Migration
-- Migration: 000005_assignment_round_robin.sql
-- Assignment-based round-robin dispatch (ADR-0017 Section 5.3). Forward-only (ADR-0010).
--
-- Existing rows: every new jobs column is nullable or has a default, so existing QUEUED and
-- RETRYING jobs become unassigned with zero lapses, which is the correct starting state. Existing
-- workers get last_poll_at NULL (never polled, so not assignable until their next poll).
-- The check constraints are added NOT VALID and then VALIDATED, as in 000003 and 000004.

-- Step 1: assignment columns on jobs.
ALTER TABLE jobs
    ADD COLUMN assigned_worker_id          TEXT REFERENCES workers(id) ON DELETE SET NULL,
    ADD COLUMN assigned_at                 TIMESTAMPTZ,
    ADD COLUMN assignment_expires_at       TIMESTAMPTZ,
    ADD COLUMN assignment_lapses           INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN assignment_fallback_reason  VARCHAR(16);

-- Step 2: the three assignment columns are all set or all NULL; a non-NULL assignment is only
-- valid on a job waiting for dispatch; lapses are never negative; the fallback reason is one of
-- two values. The shape CHECK is the backstop that makes a path which forgets to clear the
-- reservation fail loudly (ADR-0017 Section 1.1).
ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_assignment_shape CHECK (
        (assigned_worker_id IS NULL AND assigned_at IS NULL AND assignment_expires_at IS NULL)
        OR (assigned_worker_id IS NOT NULL AND assigned_at IS NOT NULL
            AND assignment_expires_at IS NOT NULL
            AND assignment_expires_at >= assigned_at
            AND state IN ('QUEUED', 'RETRYING'))
    ) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_assignment_shape;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_assignment_lapses CHECK (assignment_lapses >= 0) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_assignment_lapses;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_fallback_reason CHECK (
        assignment_fallback_reason IS NULL OR assignment_fallback_reason IN ('lapses', 'age')
    ) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_fallback_reason;

-- Step 3: poll freshness. NULL means never polled, so the worker is not assignable.
ALTER TABLE workers
    ADD COLUMN last_poll_at TIMESTAMPTZ;

-- Step 4: per-task-type round-robin cursors (ADR-0017 Section 2.3). A missing row is an empty
-- cursor. Written only by the assignment pass, under the advisory lock.
CREATE TABLE assignment_cursor (
    task_type      VARCHAR(64) PRIMARY KEY,
    last_worker_id TEXT REFERENCES workers(id) ON DELETE SET NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Step 5: the pass heartbeat (ADR-0017 Section 3.2). One row, written by a pass as its last
-- statement, only when the pass completes. A failed, stopped or skipped pass leaves it stale. The
-- fallback age arm and the poll-time refresh both read it.
CREATE TABLE assignment_pass_state (
    singleton         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_completed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO assignment_pass_state (singleton) VALUES (TRUE);

-- Step 6: indexes.
-- Pass scan: unassigned QUEUED and RETRYING jobs of one type, in created_at, id order.
-- idx_jobs_task_dispatch (000001) covers QUEUED only, and nothing covers RETRYING by type.
CREATE INDEX idx_jobs_dispatch_by_type
    ON jobs (task_type, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NULL AND state IN ('QUEUED', 'RETRYING');

-- Assigned-job lookup for the claim's own arm.
CREATE INDEX idx_jobs_assigned_worker
    ON jobs (assigned_worker_id, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NOT NULL;

-- Expiry scan for the revoke step.
CREATE INDEX idx_jobs_assignment_expiry
    ON jobs (assignment_expires_at ASC)
    WHERE assigned_worker_id IS NOT NULL;
