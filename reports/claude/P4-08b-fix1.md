Task: P4-08b fix round 1 (review of tip 37110db)
Branch: agent/claude/P4-08b
Work tip (last commit before this report): 588c9ca
Date: 2026-10-31

This follow-up does not replace `reports/claude/P4-08b.md`. That report is kept as written, and this one is authoritative where they differ. Its mutation count of "25 runs" was wrong (see item 2).

## Items

1. **ADR-0017 implementation notes.** A section "Implementation notes, slice 5a" is at the end of `docs/adr/0017-assignment-round-robin.md`, with the six decisions. Each decision names the section it amends. One-line pointers were added at Section 2.1 (erratum, the lapse-limit exclusion), Section 2.4 (EffectiveWorkerSlots in domain) and Section 7 (memory refresh in 5a, and the 5a assignment-case location). `make test-docs` passed after the edit.

2. **Mutation count.** The earlier count was wrong. At tip 37110db there were 24 runs: M01 to M22, with M14 as three variants (a, b, c). No 25th run existed then. This round added M23 (item 5), so there are 25 runs now. The handoff and the index show 25 with this breakdown. The original report `reports/claude/P4-08b.md` still says "22 mutation IDs, 25 single-site runs"; it is kept unedited, and this report corrects it.

3. **Coverage table.** `docs/agents/handoffs/P4-08b-evidence/mutation-index.md` has a table with one row for each of the 28 labels: INV-1 to INV-24, with the sub-labels 2b, 4b, 7a, 7b, 10 and 10b. Each row names a mutation that kills the invariant in memory, or gives a reason it is deferred or not mutable in memory. Nothing is missing. The table reports the status of the INV-19 outage variant as "no own mutation", because M20 kills only the restart test.

4. **Trimmed logs.** `red-coordinator.log` went from 21507 lines to 101 lines, and `red-storage.log` from 354 lines to 204. Each has a header with the command and the original line count. The filter is exactly the pattern given, `^(=== RUN|--- (FAIL|PASS)|FAIL|ok|panic)`. Its limit: indented subtest lines and assertion messages are dropped. The original full logs are still in git history at commit 2c09365, and no history was rewritten. `red-scheduler.log` and `red-config.log` were not trimmed, because the prompt asked only for the coordinator and storage logs.

5. **SetDispatchPolicy guard.** Before this round, `SetDispatchPolicy("round_robin")` accepted the name for any repository. A repository without `storage.AssignmentRepository` (PostgreSQL in slice 5a) therefore failed only on the first poll, when `pollRoundRobinLocked` returned its error. Now `SetDispatchPolicy` returns an error wrapping `domain.ErrInvalidRequest` when the policy is `round_robin` and the repository does not implement `storage.AssignmentRepository`. The check is a type assertion on `s.repo`. `fifo_pull` is accepted for any repository. `pollRoundRobinLocked` keeps its own check.
   - Test: `TestSetDispatchPolicy_RoundRobinRequiresAssignmentRepository` (`internal/coordinator/assignment_test.go`). It uses `noAssignmentRepo`, which embeds only the `storage.Repository` interface. Run before the fix, it failed: `err=<nil>, want ErrInvalidRequest`. After the fix it passes.
   - Mutation M23: removes the guard by inserting `false &&` in the check. Red: the new test fails. Green: the test passes after restore. Evidence: `mut-M23.diff`, `mut-M23-red.log`, `mut-M23-green.log`. M23 is a policy guard, not an ADR invariant, so the coverage table does not list it.

6. **Diff, gates and tip.**
   - Fix commit `ecb642e` (the only `.go` change, forced by item 5):
     ```
      internal/coordinator/assignment.go      |  8 +++++++-
      internal/coordinator/assignment_test.go | 23 +++++++++++++++++++++++
      2 files changed, 30 insertions(+), 1 deletion(-)
     ```
   - Docs commit `588c9ca` (ADR notes, index, trimmed logs, M23 evidence, handoff count):
     ```
      docs/adr/0017-assignment-round-robin.md            |    20 +
      .../handoffs/P4-08b-evidence/mut-M23-green.log     |     4 +
      .../handoffs/P4-08b-evidence/mut-M23-red.log       |     6 +
      docs/agents/handoffs/P4-08b-evidence/mut-M23.diff  |    11 +
      .../handoffs/P4-08b-evidence/mutation-index.md     |    38 +
      .../handoffs/P4-08b-evidence/red-coordinator.log   | 21410 +------------------
      .../handoffs/P4-08b-evidence/red-storage.log       |   154 +-
      docs/agents/handoffs/P4-08b.md                     |     2 +-
      8 files changed, 84 insertions(+), 21561 deletions(-)
     ```

## Gates after the last edit

The last code edit is `ecb642e`. The last docs edit is in `588c9ca`. All gates ran on that tree, and it was clean afterwards.

- `gofmt -l .`: empty. `go vet ./...`: clean. `go build ./...`: ok.
- `make test-docs`: first run failed (see below), then passed: `go test ./tests/docs`, exit 0.
- `env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...`: PASS 296, SKIP 58, FAIL 0, exit 0. Baseline 295/58/0. Expected +1 PASS for the new test, and it matched.
- `make test-security`: PASS 54, SKIP 0, FAIL 0, EXIT=0. Baseline 54/0/0.
- `make test-db`: PASS 91, SKIP 11, FAIL 0, EXIT=0. Baseline 90/11/0. Expected +1 PASS (the coordinator test also runs under the database URL), and it matched.

On the first gate run, two failures appeared, both from the docs checker. The mutation index named `TestPostgresRepository_CancelAssigned_ClearsAssignment`, a slice 5b test that does not exist yet, without the "(planned test)" label that AGENTS.md requires. The unset suite failed only because it runs the docs checker. I added the label, re-ran `make test-docs`, and then re-ran every gate, which produced the counts above.

New tip: see the `git log` entry for the commit that carries this report (pushed to `origin/agent/claude/P4-08b`).
