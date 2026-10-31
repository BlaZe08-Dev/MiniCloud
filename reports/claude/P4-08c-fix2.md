Task: P4-08c fix round 2 (independent audit of 8a2ce99)
Branch: agent/claude/P4-08c
Work tip (last commit before this report): 51d1b52
Date: 2026-10-31

# P4-08c fix round 2

Inputs: the review `docs/agents/reviews/P4-08c.md` and the probes `docs/agents/handoffs/P4-08c-evidence/audit_gap_probes_test.go.txt`, both read from `origin/agent/claude/P4-08c-review`. The review branch was not modified. `~/MiniCloud` was not touched, docker was not run, and only `minicloud_claude_test` was used, by one test process at a time.

## Status per finding

| Finding | Status | What was done |
|---|---|---|
| F-R4 (major) | fixed (test only) | `TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses` in `internal/storage/postgres/assignment_test.go`. It is PostgreSQL only because it uses a restart and reads the stored `assignment_cursor` row. It kills A01 (row F04). |
| F-R5 (minor) | fixed (test only) | Shared case `RunAssignEventCursorBeforeAdvancesWithinPassConformance`, run by both backends as `TestConformance_AssignEvent_CursorBeforeAdvancesWithinPass`. It kills A01b (row F05). |
| F-R6 (minor) | fixed (test only) | Shared case `RunClaimFallbackExpiredUnrevokedReservationConformance`, run by both backends as `TestConformance_ClaimFallback_ExpiredUnrevokedReservation`. It kills A06b (row F06). |
| F-R2 (minor) | fixed (docs) | Handoff: the `AssignQueuedJobs` bullet and design decision 2 now say `clock_timestamp()`, and the test count is 84 (the real `go test -list` count after this change). The same kind of drift was fixed too: shared cases 19 to 21, mutation totals 35/34, and the first-round gate counts labelled as first-round. |
| F-R1 (minor) | no code change, as instructed | ADR-0017 implementation note 15: the pass lock is database-wide on purpose (INV-22). One sentence was added to the `Makefile` comment above `test-db` and to `docs/testing-postgres.md` (Guardrail 3): two test processes on one database make each other's passes skip. Both files are outside this task's owned-file list; the fix round 2 instructions ask for them. |
| F-R3 (minor, pre-existing) | no code change, as instructed | `docs/agents/TASKS.md` had no slice 5c row. I added a placeholder row and section. They say the coordinator binary must apply `MINICLOUD_DISPATCH_POLICY` and the assignment settings at startup, validated, with a log line of the effective policy. I left the task ID, builder, auditor and spec to the orchestrator. I re-checked by grep: there is no `SetDispatchPolicy`, `SetAssignmentSettings` or `DispatchPolicy` reference under `cmd/`. |

The audit's fourth probe (`Audit_Pass_ReservesOldestOfTypeFirst`) is not adopted: the review says existing tests already kill its mutant, A05.

## New mutation rows (exact auditor mutants, each new test run alone)

Throwaway worktree `~/mc-mut-claude` at 2b32089, with this worktree's `.env`, removed afterwards. Harness output (executed):

```
A01 | red PASS=0 FAIL=1 | green PASS=1 FAIL=0 | KILLED |
A01b | red PASS=0 FAIL=1 | green PASS=1 FAIL=0 | KILLED |
A06b | red PASS=0 FAIL=1 | green PASS=1 FAIL=0 | KILLED |
```

| Row | Audit mutant | Sites | Test (run alone) | Red | Green | Verdict |
|---|---|---|---|---|---|---|
| F04 | A01: the cursor upsert keeps the old `last_worker_id` | 1 | `TestPostgresRepository_AssignmentCursor_UpdatePathPersistsAcrossPasses` | FAIL ("pass 2 left the stored cursor at \"w_a\", want \"w_b\"") | PASS | KILLED |
| F05 | A01b: `cursor = selected` replaced by `_ = selected` | 1 | `TestConformance_AssignEvent_CursorBeforeAdvancesWithinPass` | FAIL (`cursor_before=""`, want w_a) | PASS | KILLED |
| F06 | A06b: fallback arm `assignment_expires_at <= clock_timestamp() - interval '2 seconds'` | 1 | `TestConformance_ClaimFallback_ExpiredUnrevokedReservation` | FAIL ("no queued jobs available") | PASS | KILLED |

Mutation index: fix round 2 is 3 runs, 3 KILLED, 0 SURVIVED. Totals over all rounds: 35 runs, 34 killed, 1 equivalent mutant (P07). There were no survivors. The diffs and red/green logs are `P4-08c-evidence/mut-F0{4,5,6}{.diff,-red.log,-green.log}`.

## Counts after the last edit (tip 51d1b52)

I wrote the predictions before any run. The baseline is the audit's numbers at 8a2ce99.

| Gate | Predicted | Real | Match |
|---|---|---|---|
| `go test -list` in `internal/storage/postgres` | 84 | 84 | yes |
| new tests alone, memory | 2/0 | 2/0 | yes |
| new tests alone, PostgreSQL | 3/0 | 3/0 | yes |
| gofmt, vet, build | clean | clean | yes |
| unset suite `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | 324/81/0 | 324/81/0, rc 0, 0 `DATA RACE` | yes |
| `make test-db` (each of 5 runs) | 134/0/0 | 134/0/0 | yes |
| `make test-security` | 54/0/0 | 54/0/0, rc 0 | yes |
| race `MINICLOUD_TEST_DATABASE_URL=<.env> go test -race -count=1 -v ./internal/coordinator ./internal/storage/...` | 184/0/0 | 184/0/0, rc 0, 0 `DATA RACE` | yes |
| `make test-docs` (after `go clean -testcache`) | rc 0 | rc 0 | yes |
| `git diff origin/main -- tests/docs/citation_allowlist.txt` | 0 lines | 0 lines | yes |

The counts are top-level `--- PASS/SKIP/FAIL` lines.

## Five consecutive `make test-db` runs (alone, after the last edit)

Executed, from the gate script summary. Before each run, a `ps` check found no other `go test` or test binary process.

```
db run 1 start 22:51:01 other go-test procs before: 0
db run 1 rc=0 end 22:52:53 PASS 134 SKIP 0 FAIL 0 DATA_RACE 0
db run 2 start 22:52:53 other go-test procs before: 0
db run 2 rc=0 end 22:55:14 PASS 134 SKIP 0 FAIL 0 DATA_RACE 0
db run 3 start 22:55:14 other go-test procs before: 0
db run 3 rc=0 end 22:57:06 PASS 134 SKIP 0 FAIL 0 DATA_RACE 0
db run 4 start 22:57:06 other go-test procs before: 0
db run 4 rc=0 end 22:59:04 PASS 134 SKIP 0 FAIL 0 DATA_RACE 0
db run 5 start 22:59:05 other go-test procs before: 0
db run 5 rc=0 end 23:00:56 PASS 134 SKIP 0 FAIL 0 DATA_RACE 0
```

All five passed. No failure to report.

## Fix commits

```
2b32089 test(storage): guard cursor update path, in-pass cursor_before and fallback over an expired reservation
273ea22 docs(evidence): P4-08c mutation rows F04 to F06 (audit mutants A01, A01b, A06b)
fd60a29 docs(adr): ADR-0017 note 15, the pass lock is database-wide on purpose
16b6adf docs(testing): one test process per test database (pass lock is database-wide)
d2ff33d docs(tasks): slice 5c placeholder must apply dispatch policy and assignment settings at startup
51d1b52 docs(handoff): P4-08c fix round 2, correct stale heartbeat and test-count lines
```

`git diff --stat 8a2ce99..51d1b52`:

```
 Makefile                                           |  3 ++
 docs/adr/0017-assignment-round-robin.md            |  6 +++
 docs/agents/TASKS.md                               |  5 +++
 .../handoffs/P4-08c-evidence/mut-F04-green.log     |  4 ++
 .../handoffs/P4-08c-evidence/mut-F04-red.log       |  6 +++
 docs/agents/handoffs/P4-08c-evidence/mut-F04.diff  | 13 ++++++
 .../handoffs/P4-08c-evidence/mut-F05-green.log     |  4 ++
 .../handoffs/P4-08c-evidence/mut-F05-red.log       |  6 +++
 docs/agents/handoffs/P4-08c-evidence/mut-F05.diff  | 13 ++++++
 .../handoffs/P4-08c-evidence/mut-F06-green.log     |  4 ++
 .../handoffs/P4-08c-evidence/mut-F06-red.log       |  6 +++
 docs/agents/handoffs/P4-08c-evidence/mut-F06.diff  | 13 ++++++
 .../handoffs/P4-08c-evidence/mutation-index.md     | 18 ++++++++-
 docs/agents/handoffs/P4-08c.md                     | 27 ++++++++++---
 docs/testing-postgres.md                           |  2 +-
 internal/storage/inmemory_assignment_test.go       | 11 ++++++
 internal/storage/postgres/assignment_test.go       | 46 ++++++++++++++++++++++
 internal/storage/storagetest/assignment.go         | 45 +++++++++++++++++++++
 18 files changed, 224 insertions(+), 8 deletions(-)
```

No production code changed. No history was rewritten. New tip: 51d1b52 (work), followed by this report commit.

## Notes for the orchestrator

- The `Makefile` and `docs/testing-postgres.md` are not in the P4-08c owned-file list. I edited them only because the fix round 2 instructions ask for it.
- The slice 5c entry in TASKS.md is a placeholder with no task ID. Assign the ID, builder, auditor and owned files.
- No in-memory mutation was run for the two new shared cases; the mutants in this round are the auditor's PostgreSQL mutants.
