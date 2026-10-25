-- MiniCloud Worker Limits Hardening Migration
-- Migration: 000003_worker_limits.sql
-- Enforces database-level CHECK constraints for worker registration bounds (SEC-015, SEC-016, SEC-017).
-- Uses ADD CONSTRAINT ... NOT VALID followed by VALIDATE CONSTRAINT to avoid exclusive table locks
-- and ensure safe, forward-only validation against existing tables.
-- Task names are validated at the domain boundary against ^[a-z0-9_]{1,64}$.

-- 1. SEC-015: Bounded worker slots (between 1 and 1024)
ALTER TABLE workers ADD CONSTRAINT chk_workers_total_slots_range 
    CHECK (total_slots >= 1 AND total_slots <= 1024) NOT VALID;
ALTER TABLE workers VALIDATE CONSTRAINT chk_workers_total_slots_range;

-- 2. SEC-016: Bounded worker version string (at most 64 characters)
ALTER TABLE workers ADD CONSTRAINT chk_workers_version_length 
    CHECK (length(version) <= 64) NOT VALID;
ALTER TABLE workers VALIDATE CONSTRAINT chk_workers_version_length;

-- 3. SEC-017: Bounded worker supported_tasks array length (at most 16 tasks)
ALTER TABLE workers ADD CONSTRAINT chk_workers_supported_tasks_count 
    CHECK (coalesce(cardinality(supported_tasks), 0) <= 16) NOT VALID;
ALTER TABLE workers VALIDATE CONSTRAINT chk_workers_supported_tasks_count;
