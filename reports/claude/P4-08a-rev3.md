Task: P4-08a revision 3, orchestrator items N6 to N8 on ADR-0017 (rate limit and cost of the refresh, config single source, record format)
Branch: agent/claude/P4-08a
Work tip (last commit before this report): 1a991f6
Date: 2026-10-31

Design only. Edited: `docs/adr/0017-assignment-round-robin.md` and this report. No code, no tests, no migration. No PR, no merge.

## N6. Refresh rate limit, single flight, and the held outcome

**Mechanism (Section 3.2, "Rate limit, single flight and the held outcome (N6)").** Three values in coordinator memory, read and written under `s.mu`:
- `refreshInFlight`: true while an attempt runs. Under `s.mu` at most one can run, so the flag makes the rule explicit and guards any later move of the refresh outside the mutex.
- `refreshLastAttemptAt`: process clock, zero before the first attempt.
- `refreshLastFailed`: set by each attempt to its outcome. A success clears it.

A poll attempts only if the heartbeat is older than 2 × `MINICLOUD_ASSIGNMENT_INTERVAL`, no attempt is in flight, and either no attempt has run in this process or `now − refreshLastAttemptAt ≥ INTERVAL`. A skipped poll changes none of the three values. A restart resets them to zero.

**The age arm** is open for a poll only if that poll ran the attempt in this call and `refreshLastFailed` is true afterwards.

**Deviation from the literal N6 wording, with reason (further defect 23).** You asked that the age arm require "the most recent refresh attempt FAILED". Taken literally, that reopens the N1 gap. An outage can end less than one interval after its last failed attempt. The first poll after recovery is then rate-limited, sees the outage's failure, and opens the age arm while no pass has succeeded. The premise "the first poll after a restart or outage always attempts" is therefore false: it holds after a restart, and after an outage only when no attempt ran in the last interval. The age arm therefore requires this poll's own failed attempt.

**Argument (Section 3.2, items 1 to 5).**
- An age claim needs the poll's own attempt to have failed in the same call. The claim also ran, so the database answered in that call. The only way to claim by age after a database answer without a successful refresh is a refresh that fails while the claim in the same call succeeds. That is a single-call blip, and it is the residual below.
- After a restart the state is zero, so the first stale poll attempts. Success closes the age arm.
- After an outage, a poll after the rate window attempts. If the database is back, the attempt succeeds. A poll inside the window is skipped and cannot claim by age.
- FS-21 stays live. Under a persistently failing pass with the database reachable, each interval has one attempt. Its poll may claim one aged job it supports. So the age arm fires at most once per interval.
- A stalled loop with a working pass is resolved by the attempt itself, which refreshes the heartbeat.

**Threshold change (further defect 25).** The refresh threshold is 2 × `INTERVAL`, not 1 ×. The timer refreshes the heartbeat every `INTERVAL`, so a healthy heartbeat is routinely about `INTERVAL + D` old. A one-interval threshold would start attempts in normal operation. Validation requires `FALLBACK_AGE > 2 × INTERVAL`.

**Cost of a persistently failing pass (Section 6.2, "Cost of a persistently failing pass (N6)").** Notation: `I` = interval (default 1 s), `T` = task types per pass, `W` = reservations per pass (at most one per eligible worker), `D` = pass duration (not measured). Worst case per attempt is a pass that fails at its last statement: at most `6 + 2T + 4W` round trips. This comes from the pass structure. It is not a measurement.

| Polling workers | Attempts per second (refresh) | Round trips per second, refresh only (T = 1) | With timer pass too | Polls delayed by an attempt per second | Expected extra wait per poll |
|---|---|---|---|---|---|
| 10 | at most 1/I = 1.0 | at most 48 (`W ≤ 10`) | at most 96 | about 50·D | at most (D/I)·(D/2) |
| 100 | at most 1/I = 1.0 | at most 408 (`W ≤ 100`) | at most 816 | about 500·D | at most (D/I)·(D/2) |

The attempt rate and its round trips do not grow with polling workers. The polls each attempt delays do grow tenfold. Poll arrivals assume the 200 ms idle interval (about 5 per second per worker). The limit: if `D` reaches `I`, the timer pass is itself late and the threshold is reached in normal operation. Slice 5c must stop and report that.

**Residual (Section 6.2).** A refresh that fails while the claim in the same call succeeds allows one age claim per such call. It is rate-limited by the same rule.

**Liveness wording updated (further defect 24).** INV-14, the FS-21 bound and the Limits line previously promised `FALLBACK_AGE` plus one poll gap for "any healthy polling worker". Under the rate limit, aged jobs come one per attempt, and only the attempting poll must support the type. The FS-21 bound is now `FALLBACK_AGE + k × INTERVAL` plus one poll gap, for the k-th claimable job in FIFO order.

**Planned tests and mutations (Section 6.3 and 6.4).**
- INV-23: `TestPoll_RefreshAttempts_AtMostOnePerInterval` (planned test, slice 5c). A persistently failing pass; 1,000 polls from 10 workers, then from 100, over ten intervals. Pass count at most 11. Mutation: remove the `refreshLastAttemptAt` check.
- INV-23: `TestPoll_RefreshSingleFlight` (planned test, slice 5a). Mutation: release `s.mu` around the refresh.
- INV-24: `TestPoll_RateLimitedPoll_NoAgeClaim` (planned test, slices 5a and 5b). Failed attempt 1 ms before recovery; rate-limited poll gets 204. Mutation: use `refreshLastFailed` alone.
- INV-24: `TestPoll_FirstPollAfterOutage_AttemptsAndCloses` (planned test, slice 5a). Mutation: skip the attempt while `refreshLastAttemptAt` is zero.
- INV-19 (existing) now also covers the rate window.
- FS-21: `TestFault_PersistentPassFailure_AgeClaimRate` (planned test, slice 5c). FS-23: `TestFault_OutageRecovery_RateWindow` (planned test, slice 5c).

None of these exists or has been run.

## N7. Config single source

Section 5.5 is a new table with all seven variables: the existing `MINICLOUD_DISPATCH_POLICY` and six new ones. For each: default, allowed range, validation rule, and the section that derives it. Ranges and rules are proposals, not measurements.

**Decided in this revision, to confirm:**
- `MINICLOUD_ASSIGNMENT_INTERVAL`: range 100 ms to 10 s. The ADR had a default but no range or rule (further defect 26). The 100 ms floor caps attempts at 10 per second.
- `MINICLOUD_ASSIGNMENT_FALLBACK_AGE`: upper bound 10 min (new), and must exceed `2 × INTERVAL`.

## N8. Revision 3 record

Section 9 has a revision 3 subsection in the same format as revision 2: one table (N6 to N8) and one numbered list of further defects. The intro now gives the counts: revision 1 has 18, revision 2 has 4 (items 19 to 22), and revision 3 has 4 (items 23 to 26).

**Further defects found in revision 3.**
23. The literal "most recent attempt failed" rule reopens the N1 gap. Fixed: own-attempt rule.
24. Stale liveness wording in INV-14, FS-21 and the Limits line. Fixed.
25. Refresh threshold equal to the timer interval. Fixed: 2 × INTERVAL.
26. `INTERVAL` had no range or validation. Fixed in Section 5.5.

**Re-sweep of Sections 6.3 and 7.** Done. Affected: INV-14 (bound and attempting poll), INV-19 (threshold and rate window), new INV-23 and INV-24, 6.4 (new FS-21 and FS-23 tests), the Consequences line for the stalled pass and the invariant count (now INV-1 to INV-24), 7 (5a and 5b tests and mutation lists, 5c scope, tests and audit). The 5a configuration bullet now points at Section 5.5 and gives `2 × INTERVAL`.

## Verification

Command: `make test-docs`, run once after the last ADR edit. Output saved at `/tmp/claude-1000/-home-blazeee-MiniCloud-claude/ae697998-5f00-431e-92fc-d461cd2e57b3/scratchpad/test-docs-rev3.log`:

```
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	1.294s
```
Exit code 0.

Evidence labels: everything is **designed, not executed**. No Go code changed, so `gofmt`, `go vet`, `go build` and the Go tests were not run. The round-trip figures are worst-case formulas. `D` is not measured.

## Not done or open

- Please confirm the proposed ranges (INTERVAL 100 ms to 10 s; FALLBACK_AGE at most 10 min).
- The age arm now claims at most one aged job per interval under a failing pass. This caps fallback throughput, which is a change from revision 2. The alternative is to let every poll claim by age while the last attempt failed. That reopens the N1 gap for up to one interval, and longer if no poll arrived during the outage. I recommend the cap. Please confirm.
- Residual single-call blip (Section 6.2) is accepted as stated, unless you want it closed.
- Multi-coordinator operation is still not claimed (Q9).
