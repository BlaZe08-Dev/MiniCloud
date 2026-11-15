Task: P4-08d audit (ADR-0017 slice 5c, part 1)
Branch: agent/claude/P4-08d-review (based on agent/claude/P4-08d at b5ac949)
Work tip (last commit before this report): ef7c041
Date: 2026-11-01

## Verdict: CHANGES REQUIRED

The full review is `docs/agents/reviews/P4-08d.md`, and the evidence is in `docs/agents/handoffs/P4-08d-evidence/` (files named `audit*`). I changed no production code and no existing test.

## Gates (predicted before running; all match)

| Gate | Result |
|---|---|
| gofmt / vet / build / test-docs | clean / rc 0 / rc 0 / rc 0 |
| unset suite | 343/84/0, 0 DATA RACE |
| make test-security | 54/0/0 |
| make test-db | 149/0/0, 0 DATA RACE |
| race on coordinator, storage and cmd, URL set | 228/0/0, 0 DATA RACE |

## Findings

- **F-R1 (major): the pending grace of note 16 never times out.**
  - If the sweeper never succeeds (every `SweepExpiredLeases` fails, or it never ticks), no lapse is ever counted. The pass keeps working, so the age arm stays closed.
  - A job whose chosen worker is alive but never claims in time therefore stays in round-robin forever, and FS-19 no longer holds.
  - The probe ran 20 windows (100 s): `assignment_lapses=0`, 20 uncounted lapse events, no fallback. The control run reached 3 lapses and fell back.
  - Fix suggestion: bound the pending state on the repository clock.
- **F-R2 to F-R5 (minor): four new mutants survive the whole coordinator and cmd suites.**
  - A05: a failed tick no longer marks grace pending.
  - A04: the submit trigger buffer is raised to 64.
  - A03: the ERROR window is cut to 50 s.
  - A02: the real yield does not wait.

## Checks A to K

- **Pass:** A, B, D, E, F, G, J and K.
  - A: the report's claims hold. One exception: the builder's first test-docs run (rc 2) could not be re-run, so that claim is not verified.
  - B: a 60 s PostgreSQL run under `-race` (23,125 submits, 1,564 polls, 394 passes) gave 0 races and 0 `40P01` deadlocks. No poll ever had two passes inside its call.
- **Fail:** C (F-R1).
- **Mutations:** 14 run, 10 killed, 4 survived.
  - Re-runs: D01, D04, D06, D07, D11, D18, D21 and D24, all killed.
  - New: A01 and A06 killed, A02 to A05 survived.
- **F-5c-1 data** (PostgreSQL, `-race`, 8 workers):

| Run | Submits/s | Passes/s | Poll p50 / p99 | xact/s | tup_fetched/s |
|---|---|---|---|---|---|
| Flood | 385 | 6.6 | 138 / 325 ms | 556 | 153,770 |
| Paced | 18.7 | 17.0 | 22 / 99 ms | 325 | 7,075 |

## Tip

Review tip `ef7c041`, pushed to `origin/agent/claude/P4-08d-review`. This report is the next commit.
