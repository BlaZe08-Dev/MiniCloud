Task: P4-08e fix round 1 (after the independent audit)
Branch: agent/claude/P4-08e
Work tip (last commit before this report): d2600bd
Date: 2026-11-02

No production code changed. F-R5 (backoff overflow, `ComputeBackoffWithJitter`) was not touched: it is P4-14.

## Findings
| Finding | Status | What | Proof |
|---|---|---|---|
| F-R1 | fixed | Constant `acceptWindowMargin` (1 s) in `internal/worker/poll_timing_test.go`; the `relation` subtest requires `MinAcceptWindow - MaxPollBackoff >= margin`. ADR note 5c-2.4 gives the value and why; changing either constant must re-justify it. | E32 (audit I4) killed |
| F-R2 | fixed | Shared case `TestConformance_ClaimPolicy_BothArms` on memory and PostgreSQL: both report `lapses`. ADR note 5c-2.4: current behaviour, not mandated by Section 3.2, a change needs an amendment. | E33 (audit M2), E34 (PostgreSQL twin) killed |
| F-R3 | fixed | ADR Section 5.1 table and `docs/protocol.md` Section 3.1 amended: the 201 body is the job view with the four fields unset; "No change" removed for the submit endpoint; the stale 201 example replaced with the real encoding. Test `TestAPI_SubmitJob_ResponseCarriesAssignmentFields`. | E35 (`assignment_lapses` removed from the encoding) killed |
| F-R4 | fixed | `TestPollTiming_RealClockSleeps` (the audit probe, about 3.6 s, normal test). | E36 (audit M3p), E37 (audit M3, whole worker package) killed |
| F-R5 | not mine | P4-14 | none |

## Mutation rows (`docs/agents/handoffs/P4-08e-evidence/mutation-index.md`, run alone at 8c904f1 in `~/mc-mut-claude`, removed afterwards)
All red FAIL, green PASS, verdict KILLED. Total now 37 mutants, 37 killed, 0 survived.
- E32 INV-17: poll backoff 4.999 s, killed by `TestPollTiming_AcceptWindowExceedsBackoff`
- E33 memory reports `age` when both arms qualify, killed by `TestConformance_ClaimPolicy_BothArms`
- E34 PostgreSQL twin of E33, same test (URL set)
- E35 `assignment_lapses` dropped from the encoding, killed by `TestAPI_SubmitJob_ResponseCarriesAssignmentFields`
- E36 production clock halved, killed by `TestPollTiming_RealClockSleeps`
- E37 the same mutant, whole `./internal/worker/` package, killed by the same test

## Gates (predictions: `fix1-predictions.md`, written before any gate ran; run once each after the last edit)
| Gate | Predicted | Real |
|---|---|---|
| gofmt / vet / build | clean | clean |
| make test-docs | rc 0 | rc 0 (the first run failed RuleA on a handoff cite of a path that exists only on the review branch; fixed in d2600bd, then re-run, with the unset suite) |
| unset suite -race | 358/87/0 | 358/87/0, 0 DATA RACE |
| make test-security | 54/0/0 | 54/0/0 |
| make test-db, 3 runs | 157/0/0 | 157/0/0, 157/0/0, 157/0/0 |
| race, URL set, coordinator + storage/... + cmd/... + worker/... | 279/0/0 | 279/0/0, 0 DATA RACE |
| race, URL set, ./internal/api/... (run separately) | 28/0/0 | 28/0/0, 0 DATA RACE |
The race set grows with the api package: 279 + 28 = 307/0/0 in total. Every row ran after d2600bd (the last edit before this report). Adding this report changed only a docs file; `make test-docs` was re-run after it: rc 0.

## Diff stat of the fix commits (74df64d..d2600bd, evidence files excluded)
9 files changed, 190 insertions, 14 deletions: ADR-0017, protocol.md, TASKS.md (own row), handoff, poll_timing_test.go, assignment_wire_test.go (api), storagetest/assignment_wire.go, and the two backend test files. Evidence files are added under `P4-08e-evidence/` (`fix1-predictions.md`, `fix1_mut.py.txt`, `mut-E32` to `mut-E37` logs and diffs, `mut-results-fix1.tsv`).

Allowlist `tests/docs/citation_allowlist.txt`: identical to origin/main (empty diff). `PROJECT_STATE.md` untouched. No line numbers in live docs. No history rewrite. Review branch untouched. No docker.

New tip: see the push below (this report is committed alone on top of d2600bd).
