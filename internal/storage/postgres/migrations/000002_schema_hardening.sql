-- MiniCloud Schema Hardening Migration
-- Migration: 000002_schema_hardening.sql
-- Hardens attempt active index to cover all non-terminal states (PENDING, RUNNING)
-- and updates worker_capacity view to count all non-terminal attempts.

-- 1. Drop and recreate partial index to cover all non-terminal attempt states (PENDING, RUNNING)
DROP INDEX idx_attempts_active_per_job;
CREATE UNIQUE INDEX idx_attempts_active_per_job 
    ON attempts (job_id) 
    WHERE state IN ('PENDING', 'RUNNING');

-- 2. Replace worker_capacity view to count both PENDING and RUNNING attempts
CREATE OR REPLACE VIEW worker_capacity AS
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
