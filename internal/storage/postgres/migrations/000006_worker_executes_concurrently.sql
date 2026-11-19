-- MiniCloud worker concurrency declaration
-- Migration: 000006_worker_executes_concurrently.sql (ADR-0020 Section 2.6, task V2-0b)
-- Forward-only (ADR-0010). Stores the optional register field executes_concurrently. A worker that
-- does not send it (v1.0.0, V2-0) keeps FALSE, and the round-robin pass then gives it one slot.
-- A constant default adds the column without rewriting existing rows; every existing worker reads
-- FALSE until it re-registers.
ALTER TABLE workers
    ADD COLUMN executes_concurrently BOOLEAN NOT NULL DEFAULT FALSE;
