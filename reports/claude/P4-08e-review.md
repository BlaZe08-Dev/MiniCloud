Task: P4-08e-review (independent audit of P4-08e)
Branch: agent/claude/P4-08e-review
Work tip (last commit before this report): 7a5c008
Date: 2026-11-02

Verdict: **CHANGES REQUIRED**. The diff has no blocker or major defect. The changes required are the orchestrator's fix-round decisions Q1 to Q3, each backed by a surviving mutant. Review: `docs/agents/reviews/P4-08e.md`. Evidence: `docs/agents/handoffs/P4-08e-evidence/audit*`.

Commit audited: 74df64d. No production code or test was changed. One test process at a time; no docker.

## Gates (predicted before any gate ran: `audit-predictions.md`)
| Gate | Predicted | Real |
|---|---|---|
| gofmt / vet / build | clean | 0 files / rc 0 / rc 0 |
| make test-docs | rc 0 | rc 0 |
| unset suite, -race | 355/86/0 | 355/86/0, 0 DATA RACE |
| make test-security | 54/0/0 | 54/0/0 |
| make test-db | 156/0/0 | 156/0/0 |
| race, URL set (4 package sets) | 276/0/0 | 276/0/0, 0 DATA RACE |

## Mutants
- I re-ran 13 of the builder's 31, all killed: E29, E08, E09, E10, E11, E12, E13, E16, E17, E18, E20, E21, E22. They cover every PostgreSQL parity mutant and the six per-arm value mutants.
- I ran 9 mutants of my own:
  - killed: audit-I1 (floor 3 s), audit-I2 (backoff 5 s), audit-I3 (both floors 3 s), audit-M1 and audit-M1b (field rename), audit-M3p (production clock halved, caught by the probe);
  - survived: audit-I4 (backoff 4.999 s, F-R1), audit-M2 (memory backend reports `age` when both fallback arms qualify, F-R2), audit-M3 (production clock halved, none of the 38 worker tests catch it, F-R4).

## Checks
- Scope: the loop, sweeper and grace files have 0 changed lines (`audit-scope.txt`).
- Real-clock waits: a probe ran on main and on the tip. Idle gaps were 201 to 203 ms on both, and all backoffs fell inside their jitter bounds (`audit-probe-realsleep-*.log`).
- No new test can pass while skipping.
- No leak of other workers' data, `assigned_at` or `last_poll_at`.

## Findings
- F-R1, minor: INV-17 pins no margin.
- F-R2, minor: the precedence between the lapse and age arms is not pinned.
- F-R3, minor: the 201 submit body carries the four fields, against the "No change" in Section 5.1, and no test pins it.
- F-R4, minor: no test covers `realClock`, which the poll loop now waits on.
- F-R5 = F-5c2-1, major, pre-existing: confirmed, the backoff is 0 s from 64 failures.
  - Minimal fix (diff in `audit-fix-F-5c2-1.diff`): clamp `failureCount` to 5 before the shift. With it, the probe and the worker package both pass.
  - Exposed: the poll loop (both error arms) and `renewLoop`.
  - Not reachable: `deliverResult`, because its 60 s budget runs out before 64 failures (at least 95.9 s needed).

## Orchestrator decisions
- Q1: agree. Record the 1 s margin in the ADR and name it in the test; audit-I4 must then die.
- Q2: agree. Amend ADR Section 5.1 and `docs/protocol.md`, and pin the 201 body (null/0, no `assigned_at`).
- Q3: agree. Add a shared case for both backends so that audit-M2 dies.
- Q4: agree with P4-14. It must cover `renewLoop`, and `TestPollTiming_RunSleepsWithinMaxBackoff` should then gain the lower bound.
