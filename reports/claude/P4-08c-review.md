Task: P4-08c audit (ADR-0017 slice 5b, builder tip 8a2ce99)
Branch: agent/claude/P4-08c-review
Work tip (last commit before this report): 39352c9
Date: 2026-10-31

## Verdict: CHANGES REQUIRED

The full review is in `docs/agents/reviews/P4-08c.md`; the evidence is the `audit*` files in `docs/agents/handoffs/P4-08c-evidence/`. No production code or existing test was changed.

The PostgreSQL code held under every attack. The verdict is about missing tests: three of my eight new mutants survive all 48 assignment tests.

## Findings

| ID | Severity | What | Evidence |
|---|---|---|---|
| F-R4 | major | INV-5 cursor **update** path unguarded. Mutant A01 (the upsert never changes an existing cursor row) survives. Such a regression would break round-robin across passes (S1). | executed; killed by probe `Audit_Cursor_UpdatePathPersistsAcrossPasses` |
| F-R5 | minor | In-pass cursor advance (`cursor_before` in `JOB_ASSIGNED`) unguarded. A01b survives. | executed; killed by probe `Audit_Event_CursorBeforeAdvancesWithinPass` |
| F-R6 | minor | The fallback arm's expired-reservation bound is unguarded. A06b survives; the impact is latency only. | executed; killed by probe `Audit_Fallback_ExpiredUnrevokedReservationIsClaimable` |
| F-R1 | minor | The pass advisory key is per database, so two test processes on one test database make passes `Skipped`. My first `make test-db` failed 4 tests that way. | executed (cross-schema probe); which process held the key in run 1 is inferred, not identified |
| F-R2 | minor | Two handoff lines are stale: the heartbeat "with `now()`" (it is `clock_timestamp()`), and "79 top-level tests" (81 now). | executed |
| F-R3 | minor, pre-existing | `cmd/coordinator` never applies `MINICLOUD_DISPATCH_POLICY` or the assignment settings, so the binary always runs fifo_pull. Slice 5c scope. | code reading, grep |

## Gates (predicted before running)

| Gate | Predicted | Real |
|---|---|---|
| `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | 322/78/0 | 322/78/0, 0 DATA RACE |
| `make test-security` | 54/0/0 | 54/0/0 |
| `make test-db` run 1 | 131/0/0 | 126/0/4 (another `make test-db` was using the same database; F-R1) |
| `make test-db` run 2, alone | 131/0/0 | 131/0/0 |
| `MINICLOUD_TEST_DATABASE_URL=<.env> go test -race -count=1 -v ./internal/coordinator ./internal/storage/...` | clean | 179/0/0, 0 DATA RACE |
| `gofmt -l .`, `go vet ./...`, `go build ./...` | clean | clean |
| `make test-docs` / `go test -count=1 ./tests/docs` (after adding the review) | rc 0 | rc 0 |

## Checks A to K (one verdict each)

- **A** (claims, counts, mutation index, notes 10-14, not_before): PASS, except the two stale handoff lines (F-R2).
- **B** (every jobs write, shape CHECK, REJECTED): PASS. No path persists REJECTED, and the CHECK refuses it with 23514.
- **C** (lock order, 60 s stress under `-race`): PASS. 36,716 operations, 0 `40P01`.
- **D** (clocks): PASS. Only `created_at` mixes the process and database clocks (note 14, accepted for V1).
- **E** (000005 on every job state): PASS. All three CHECKs are validated, and fifo order is unchanged.
- **F** (memory/PostgreSQL parity): PASS. 19 identical cases, all run on both backends. Observation: 3 new PostgreSQL tests report PASS with all subtests skipped when the URL is unset, the same as 14 tests on main.
- **G** (200 concurrent pollers): PASS. One refresh per interval, at most one age claim, distinct tokens, one attempt per job.
- **H** (fifo SQL vs origin/main): PASS. Only the documented clearing `SET` clause was added.
- **I** (mutations): CHANGES REQUIRED.
  - The 8 index re-runs (P01, P04, P05, P07, P08, P15, P16, P24) match the index. P07 survives, and I agree it is an equivalent mutant.
  - New mutants A02, A03, A04, A05 and A06 are killed. A01, A01b and A06b survive (F-R4 to F-R6).
- **J** (SetDispatchPolicy, cmd wiring, A1 edges): PASS on all boundaries. Gap F-R3.
- **K** (docs): PASS. No line numbers; the allowlist is identical to origin/main; test-docs rc 0; F-R2 aside.

## Exact commands (gates)

```
test -z "$(gofmt -l .)"; go vet ./...; go build ./...; make test-docs
env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./... > gate-unset.log 2>&1
make test-security > gate-security.log 2>&1
make test-db > gate-db.log 2>&1            # run 1 (contended), then again alone as run 2
MINICLOUD_TEST_DATABASE_URL="$URL" go test -race -count=1 -v ./internal/coordinator ./internal/storage/... > gate-race.log 2>&1
go test -count=1 ./tests/docs
counts: grep -c '^--- PASS' / '^--- SKIP' / '^--- FAIL' on each log; grep -c 'DATA RACE'
```

Probes were copied into their package as `_test.go` files, run with `-race -count=1 -v -run '^TestAudit_...'`, then deleted. Mutations ran in `~/mc-mut-claude` (removed afterwards), driven by `audit_mut_run.sh.txt` and `audit_probe_vs_mutant.sh.txt`. Test processes ran one at a time, except for one `go test -list` (which runs no test and uses no database) during the race gate.

## Notes for the owner

- During my first `make test-db`, another `make test-db` was started in `~/MiniCloud-claude` from a desktop terminal (pid 416410), against `minicloud_claude_test`. I waited for it to exit before running anything else.
- Recommended fix round: adopt the four probes in `audit_gap_probes_test.go.txt` (or equivalent tests) with mutation proofs against A01, A01b and A06b, and correct the two handoff lines. F-R1 and F-R3 are for the orchestrator to decide.

Review branch tip: the commit carrying this report, one after 39352c9.
