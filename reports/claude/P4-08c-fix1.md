Task: P4-08c fix round 1 (orchestrator review of tip 1a236f7)
Branch: agent/claude/P4-08c
Work tip (last commit before this report): 49a9bb8
Date: 2026-10-31

## Status per item

1. **Done.** `tests/docs/citation_allowlist.txt` is identical to `origin/main` (`git diff origin/main -- tests/docs/citation_allowlist.txt` is empty).
   - The live docs that cited the deleted 5a skip file by path now give the file name and commit b67db2a instead: ADR-0017 implementation note 6, the P4-08b and P4-08c handoffs, and TASKS.md.
   - In note 6, which is older ADR text, only the code span holding the path changed.
   - `make test-docs` passes with the allowlist unchanged.
2. **Done.** The pass heartbeat is now written with `clock_timestamp()` in the pass's last statement; the other staleness comparisons already used `clock_timestamp()`.
   - New test `TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime`. A second connection holds the only eligible worker, so the pass waits out a 300 ms `lock_timeout`. The heartbeat must then be at least 250 ms after the database time read before the pass, and not earlier than the lapse event the pass wrote.
   - Mutation F01 (`clock_timestamp()` to `now()`) is killed deterministically. Red: "heartbeat is 262µs after the pre-pass time".
   - Not changed: the initial heartbeat row inserted by the migration still takes the column default `CURRENT_TIMESTAMP`. No pass writes that row.
3. **Done.** The code already refused a repository without `storage.AssignmentPassStaleness` (I added that guard in round 0), but it had no test.
   - New test `TestSetDispatchPolicy_RoundRobinRequiresPassStaleness`. Its wrapper type has `AssignmentRepository` but not the staleness interface; a control checks that the plain in-memory repository is accepted.
   - Mutation F02 (guard disabled) is killed.
4. **Done. Not a defect.**
   - New test `TestPostgresRepository_DeleteWorkerWithReservation_Rejected`. The delete is rejected: `ERROR: new row for relation "jobs" violates check constraint "chk_jobs_assignment_shape" (SQLSTATE 23514)`. The worker row and the reservation (worker, `assigned_at`, expiry) are unchanged afterwards; a control deletes a worker that holds no reservation.
   - Mutation F03 (`ON DELETE SET NULL` to `ON DELETE CASCADE` in 000005) is killed.
5. **Done**, as ADR-0017 implementation notes 10 to 14 in a new subsection "Implementation notes, P4-08c fix round 1". No older ADR text changed except the note 6 code span from item 1.
   - Note 10: INV-9 belongs to 5c (erratum).
   - Note 11: the "workers before jobs" mutant is equivalent; removing `SKIP LOCKED` is the INV-13 mutation.
   - Note 12: heartbeat written with `clock_timestamp()`.
   - Note 13: the staleness interface is accepted.
   - Note 14: mixed clocks.
   - The evidence coverage table marks P07 as equivalent and lists F01 to F03.
   - **Correction to the item 5(e) wording, checked in the code:** in PostgreSQL, `jobs.not_before` is not stamped by the coordinator clock. `CommitAttempt` reads `SELECT clock_timestamp()` and `SweepExpiredLeases` reads `clock_timestamp()` under its locks, and both add the backoff to that. Note 14 therefore says that only `created_at` (stamped in `Service.SubmitJob`) mixes the clocks in PostgreSQL. The rest of 5(e) is recorded as given: harmless on one host, a V3 requirement, no V1 code change.
6. This report.

## Mutations (throwaway worktree `~/mc-mut-claude` at 1351839, removed afterwards)

| Run | Change | Test | Red | Green | Verdict |
|---|---|---|---|---|---|
| F01 | heartbeat `clock_timestamp()` to `now()` | `TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime` | FAIL | PASS | KILLED |
| F02 | `SetDispatchPolicy` staleness guard disabled | `TestSetDispatchPolicy_RoundRobinRequiresPassStaleness` | FAIL | PASS | KILLED |
| F03 | `assigned_worker_id ... ON DELETE CASCADE` | `TestPostgresRepository_DeleteWorkerWithReservation_Rejected` | FAIL | PASS | KILLED |

Logs and diffs are in `docs/agents/handoffs/P4-08c-evidence/mut-F0*`; the largest of these files is 847 bytes. Across both rounds: 32 runs, 31 killed, and 1 equivalent mutant (P07).

## Gates after the last edit (49a9bb8)

Predictions were written before the runs. Counts are top-level `--- PASS/SKIP/FAIL` lines.

| Gate | Baseline (1a236f7) | Predicted | Real |
|---|---|---|---|
| gofmt / vet / build | clean | clean | clean |
| `make test-docs` | ok | ok | ok |
| unset `go test -v -race -count=1 ./...` | 321/76/0 | 322/78/0 | 322/78/0, exit 0 |
| `make test-security` | 54/0/0 | 54/0/0 | 54/0/0, exit 0 |
| `make test-db` | 128/0/0 | 131/0/0 | 131/0/0, exit 0 |
| race `./internal/coordinator ./internal/storage/...` (URL from `.env`) | 176/0/0 | 179/0/0 | 179/0/0, exit 0, 0 `DATA RACE` |

Why these predictions:
- **Unset:** the new coordinator test runs (+1 PASS) and the two new PostgreSQL tests skip (+2 SKIP).
- **test-db and race:** all three new tests run (+3).

The baseline column is the first round's real results at 1a236f7, re-run as item 0 asked.

## Fix commits and `git diff --stat 1a236f7..49a9bb8`

Commits: c3dba73 (item 1), 1351839 (items 2, 3, 4: code and tests), 49a9bb8 (item 5, the evidence and the handoff section). This report is committed on its own after 49a9bb8.

```
 docs/adr/0017-assignment-round-robin.md            | 12 ++-
 docs/agents/TASKS.md                               |  4 +-
 docs/agents/handoffs/P4-08b.md                     |  4 +-
 .../handoffs/P4-08c-evidence/mut-F01-green.log     |  5 ++
 .../handoffs/P4-08c-evidence/mut-F01-red.log       |  7 ++
 docs/agents/handoffs/P4-08c-evidence/mut-F01.diff  | 13 ++++
 .../handoffs/P4-08c-evidence/mut-F02-green.log     |  5 ++
 .../handoffs/P4-08c-evidence/mut-F02-red.log       |  7 ++
 docs/agents/handoffs/P4-08c-evidence/mut-F02.diff  | 13 ++++
 .../handoffs/P4-08c-evidence/mut-F03-green.log     |  6 ++
 .../handoffs/P4-08c-evidence/mut-F03-red.log       |  7 ++
 docs/agents/handoffs/P4-08c-evidence/mut-F03.diff  | 13 ++++
 .../handoffs/P4-08c-evidence/mutation-index.md     | 22 +++++-
 docs/agents/handoffs/P4-08c.md                     | 14 +++-
 internal/coordinator/assignment_test.go            | 29 +++++++
 internal/storage/postgres/assignment.go            | 13 ++--
 internal/storage/postgres/assignment_test.go       | 90 ++++++++++++++++++++++
 tests/docs/citation_allowlist.txt                  |  3 -
 18 files changed, 248 insertions(+), 19 deletions(-)
```

No history was rewritten. The new tip is the report commit, which comes one commit after 49a9bb8.
