Task: P4-08a revision 2, orchestrator items N1 to N5 on ADR-0017, and a re-sweep of Sections 5.3, 6.3 and 7
Branch: agent/claude/P4-08a
Work tip (last commit before this report): 7f7535965a8a8290dd048446250d5b125acc31e0
Date: 2026-10-31

Design only. Edited: `docs/adr/0017-assignment-round-robin.md` and this report. No code, no tests, no migration. No PR, no merge.

## Items

**N1. Age arm after restart or outage (fixed in Sections 3.2, 4.4, 6.1, 6.3, 6.4).** The requested suppression ("suppress while a startup or outage grace window is active and until one pass has completed since process start") is not used. Reasons, recorded in Section 3.2: a window length is a guess about recovery time, a window shorter than the outage reopens the gap, and a rule that waits for a completed pass would also suppress the age arm when a pass never completes, which breaks FS-21.
Chosen mechanism: before its claim, a poll whose `assignment_pass_state.last_completed_at` is older than `MINICLOUD_ASSIGNMENT_INTERVAL` runs one refresh pass under `s.mu` (in `PollWork`). A successful refresh closes the age arm for that poll, so the age arm is reachable only after a refresh has failed. If the database is down, the claim fails too. The check runs in `PollWork` and the predicate stays in the claim SQL.
Also: FS-23 row; INV-14 test setup changed (further defect 22); INV-19 with planned tests and mutation (remove the refresh from `PollWork`); 6.4 rows.

**N2. Pass gap (fixed in Sections 2.1, 4.4, 6.1, 6.3, 6.4, 7).** New config `MINICLOUD_ASSIGNMENT_MIN_YIELD`, default 10 ms, range 1 ms to 1 s, not above `MINICLOUD_ASSIGNMENT_INTERVAL`. The loop releases `s.mu` for the yield before any repeat, so a waiting poll takes the mutex at that release.
Bounds stated in Section 2.1, with pass duration `D` not measured: poll latency added by passes is at most `D` plus the claim; commit latency for a job with `p` unassigned jobs of its type ahead of it is at most `p` repeats, each at most `D + MIN_YIELD`. The yield is not a rate limit.
Failure row FS-24; INV-20 (yield, mutation: zero gap and mutex held across the repeat); INV-21 (repeat needs progress, mutation: repeat on any full batch); planned tests in 6.4.

**N3. Advisory lock (fixed in Sections 2.1, 2.3, 4.2, 4.3, 5.4, 6.2, 6.3).** `pg_try_advisory_xact_lock(key)` is the pass's first statement. On false the pass returns `skipped`, reads and writes no row (including `assignment_pass_state`), logs one line and does not repeat. Section 4.2 and 2.3 said "blocks"; that wording is corrected (further defect 20). INV-22 with a planned test and mutation (blocking call).
Checked: `internal/storage/postgres/migrator.go` uses a blocking `pg_advisory_xact_lock` (confirmed by code reading). The pass's try-lock is a separate choice.

**N4. Count of further defects (fixed in Section 9).** Each item is listed once, one list per revision:
- Revision 1: **18**. The ADR's R5 list had 15, and R2 to R4 each marked further items (two of them not in that list). The report had 17 items: item 16 duplicated item 2, and item 17 (late-accept table) was not in the ADR. The corrected list is Section 9 R5, items 1 to 18.
- Revision 2: **4**, items 19 to 22 (listed below).

**N5. Orchestrator answers (Section 8.3 added; status line set).** R-a yes. R-b yes. R-c yes, 5 s to 30 s. R-d accept the proposed defaults, with measured values recorded in slice 5c. R-e accept. Status line: "Proposed, revision 2, orchestrator answers recorded."

## Further defects found in revision 2 (19 to 22)

19. **Repeat spin.** The repeat rule re-ran on any full batch. When no worker of a type is eligible, a full batch of unplaceable jobs repeated back to back with no reservation. Fixed: a repeat needs a reservation (INV-21).
20. **Blocking wording.** Sections 2.3 and 4.2 said a second pass blocks on the advisory lock. Contradicts N3. Fixed.
21. **Amplification by the N1 refresh.** While the pass fails with the database reachable, each poll runs one failed pass attempt under `s.mu`. Not rate-limited, because a rate limit could skip the refresh just after recovery and reopen the gap. Residual stated in Section 6.2, to measure in slice 5c.
22. **INV-14 setup.** As first planned ("pass disabled"), the test would fail on a correct implementation, because the poll's refresh would still reserve the job. Fixed: the test must also make the refresh fail.

## Re-sweep of Sections 5.3, 6.3 and 7

- 5.3: no DDL change. Step 5 comment and notes updated (heartbeat written only by a completed pass).
- 6.3: INV-14 setup (22); INV-19 to INV-22 added; the Consequences line now reads INV-1 to INV-22.
- 7: config is now seven variables (`MIN_YIELD` added; `FALLBACK_AGE` must exceed `INTERVAL`); tests and mutation proofs added to slices 5a, 5b and 5c; slice 5c scope and audit updated.
- Also updated where N1 to N3 required it: Sections 2.3, 4.1, 4.2, 4.3, 4.4, 5.4, 6.1 (FS-21 wording), 6.2 (two abuse rows), D9, D11, D14.

## Verification

Command: `make test-docs` (output saved at `/tmp/claude-1000/-home-blazeee-MiniCloud-claude/ae697998-5f00-431e-92fc-d461cd2e57b3/scratchpad/test-docs-rev2.log`), run once after the last ADR edit:

```
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	1.115s
```
Exit code 0.

Evidence labels: all design statements are **designed, not executed**. No Go code changed, so `gofmt`, `go vet`, `go build` and the Go tests were not run. No mutation proof was run (design only). The bounds `D`, the yield default and the refresh cost are not measured.

## Not done or open

- The refresh (N1) adds one failed pass attempt per poll while the pass fails with the database reachable (further defect 21). I chose not to rate-limit it, and the residual is recorded for slice 5c. Please confirm that choice.
- N1 departs from the suggested suppression mechanism, as described above. Please confirm.
- Multi-coordinator operation is still not claimed (Q9).
