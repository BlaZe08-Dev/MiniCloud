Task: P4-08b-review (independent audit of ADR-0017 slice 5a, builder tip 46ca30a)
Branch: agent/claude/P4-08b-review
Work tip (last commit before this report): b88af1d
Date: 2026-10-31

## Verdict

CHANGES REQUIRED: one major finding, no blocker. The full review is `docs/agents/reviews/P4-08b.md`; the evidence is in `docs/agents/reviews/P4-08b-evidence/`. No production file was changed.

## Major

- **F-5a-1.** If `SetDispatchPolicy("round_robin")` is called without `SetAssignmentSettings`, the service runs with zero-value settings, and nothing rejects them. With `MaxLapses` at 0, every poll claims the head job as `round_robin_fallback` with reason `lapses`. With `BatchLimit` at 0, the pass reserves nothing. Round-robin silently becomes first-poller-wins. Executed: the probe `TestAudit_Probe_RoundRobinWithoutSettings` (proposed test) fails on 46ca30a. The 5a binary cannot reach this path, but the 5c wiring can. Fix: default the settings in `NewService` and validate them when they are set.

## Minor and nit

- 12 of my 21 new single-site mutations survived the existing suites. Each is a finding (F-5a-2 to F-5a-12): the claim capacity guard, the lapse-limit exclusion at L and the L-1 boundary, no lapse for an unhealthy holder, the exact-expiry boundary, `not_before` in the age arm, candidate ID order in the event payload, the memory TryLock (already disclosed), the A1 equality edge, the policy-name guard, the refresh threshold and the poll-write throttle (the last two have planned 5c tests).
- I wrote a probe for 9 of the survivors. Every probe passes on the unmutated tree and kills its mutation. The builder can adopt them; they are not committed into `internal/`.
- F-5a-8: malformed values such as `MINICLOUD_ASSIGNMENT_MAX_LAPSES=three` silently fall back to the default. Executed, probe fails. The cause is the pre-existing env helpers.
- F-5a-13: the refresh staleness check compares the coordinator clock with repository time. That is harmless in memory, but it becomes a skew hazard in 5b. Confirmed by code reading.
- Nits: `refreshLastFailed` is never read; the handoff counts 13 config rejection cases where there are 14; the claim mutates the job before generating the lease token (a pattern already in `DispatchNextJob`); the single-flight test is white-box only.

## Claims re-checked (Section A)

- All 31 test names exist.
- The 11 PostgreSQL skips are explicit.
- All 25 mutation diffs and red/green logs match the index.
- The commit hashes and the 74eb433 base are confirmed.
- api, cmd and worker are untouched.
- The gate counts are identical to the builder's.
- One count is wrong: 13 config rejection cases, actually 14.
- M20 also kills the INV-19 outage test. My mutation A19 is the first outage-specific mutation, and it is killed.
- The red-first stub history cannot be verified from the tree.

## Stress (Section D)

- `go test -race -count=20` on coordinator, scheduler and storage: ok, ok, ok, EXIT=0, 0 DATA RACE.
- New concurrent invariant test (evidence file only): 60 of 60 seeds pass under `-race`. As a positive control, it fails every seed when the reservation clearing at claim is removed.

## Gates (Section F, review branch, tree clean)

- gofmt: empty. vet: clean. build: ok. `make test-docs`: ok.
- Unset suite, `-race`: PASS 296, SKIP 58, FAIL 0.
- `make test-security`: PASS 54, SKIP 0, FAIL 0.
- `make test-db` (`minicloud_claude_test`): PASS 91, SKIP 11, FAIL 0, EXIT 0.

## Process notes

- `git diff --stat 46ca30a..HEAD` before this report: 110 files changed, 19936 insertions(+), all under `docs/agents/reviews/`.
- Mutations and probes ran in `~/MiniCloud-claude`, per the session instruction, not in `~/mc-mut-claude`. Each file was restored with `git checkout`, and the tree was clean after every run.
- Logs over 100 KB were trimmed (from 81 MB to 2 MB). The filter is stated in each log's header, and the counts were re-derived from the trimmed logs.
- The first background race run overlapped with a second copy, so I discarded it and re-ran it alone.
- Pushed tip: the commit that carries this report on `origin/agent/claude/P4-08b-review`.
