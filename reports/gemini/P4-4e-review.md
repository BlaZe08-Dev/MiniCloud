Task: P4-4e audit
Branch: agent/gemini/P4-4e-review
Work tip (last commit before this report): 1402671
Date: 2026-10-29

# P4-4e Audit Review Summary

## Verdict
**ACCEPT WITH CONDITIONS**

## Counts
- **Claims checked**: 21 checked, 1 wrong/refuted (builder claim regarding M18 unkillability / F-4e-20), 0 unsupported.
- **Mutations executed**: 29 total
  - *Builder mutations (M01–M19)*: 19 verified against evidence (17 killed, 2 survived in builder's run; M18 refuted as killed by existing database suite).
  - *PostgreSQL repository mutations (PG-M01–PG-M10)*: 10 executed independently, 9 killed, 1 survived (PG-M01: `FOR UPDATE SKIP LOCKED;` in `DispatchNextJob`).
- **Attacks executed**: 7 live attack categories executed against PostgreSQL coordinator binary:
  1. Payload fuzzing (NUL escapes, invalid UTF-8, lone surrogates, duplicate keys, 64 KiB field boundary, 1 MiB HTTP body limit).
  2. Worker text fields validation (NUL byte rejection, length checks, invalid UTF-8 across all endpoints).
  3. Path parameter identifier fuzzing (SQL injection, quotes, percent-encoded NUL, raw NUL, 100 KiB ID, Cyrillic, path traversal, CRLF across all routes).
  4. Numeric boundary validation (`max_attempts`, `timeout_seconds`, `total_slots`, `available_slots`, `exit_code`).
  5. Oversize output (>64 KiB) delivery end-to-end trace (demonstrating poison loop).
  6. Concurrency stress test: 200 jobs raced under 10 polling workers, lease sweeper ticks, and concurrent cancel requests.
  7. Hostile terminal escape demonstration (F-4e-18 executed via `TestAbuse_MCOutput_HostileErrorText`).
- **Auditor findings (AUD-4e-01 to AUD-4e-07)**:
  - **AUD-4e-01** (Major): Builder claim M18 (F-4e-20) refuted. Reversing sweeper lock order is already killed by `TestPostgresRepository_Sweeper_CanonicalLockOrder` in `internal/storage/postgres/sweeper_test.go`.
  - **AUD-4e-02** (Major): Test gap on PostgreSQL dispatch `FOR UPDATE SKIP LOCKED;`. Removing it survived all existing test suites.
  - **AUD-4e-03** (Informational): Slow healthy DB queries (>8s) trip tick timeout and open a 30s outage grace window, delaying lease reclamation.
  - **AUD-4e-04** (Medium): Worker registration accepts invalid UTF-8 in `version` and stores it into PostgreSQL text column, causing downstream JSON client decode errors.
  - **AUD-4e-05** (Low): Missing boundary validation on heartbeat `available_slots` (accepts negative and arbitrary int64 values).
  - **AUD-4e-06** (Medium): Deterministic oversize task output (>64 KiB) creates a worker poison retry loop across all `max_attempts`.
  - **AUD-4e-07** (Low): `make test-security` false-green guard checks test count only (44) rather than test names.

## Conditions for Accept
1. **Address PostgreSQL dispatch test gap (AUD-4e-02)**: Add a test with multiple concurrent workers contending on multiple queued jobs to prove non-blocking FIFO dispatch with `FOR UPDATE SKIP LOCKED`.
2. **Correct handoff claim regarding M18 and F-4e-20 (AUD-4e-01)**: Acknowledge that canonical sweeper lock order is already enforced and tested by `TestPostgresRepository_Sweeper_CanonicalLockOrder`.
3. **Schedule CLI fix for F-4e-18**: Ensure `cmd/mc` raw terminal escape sanitization is addressed in the CLI task.
4. **Harden input validation (AUD-4e-04, AUD-4e-05)**: Enforce UTF-8 validation on worker version and non-negative boundary check on heartbeat available slots.
