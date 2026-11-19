Task: V2-0b-audit
Branch: agent/claude/V2-0b-audit
Work tip (last commit before this report): 2cae65e
Date: 2026-11-19

V2-0b audit of b5aec6f: **APPROVE WITH FIXES**. No blocker or major finding, 5 minor. Review: docs/agents/reviews/V2-0b.md, evidence in docs/agents/reviews/V2-0b-evidence/.
- Rule (executed): over HTTP against a real coordinator (re-register loops, flag flips, 30 parallel polls, 20 sybil IDs, total_slots 1024), a worker never held more than 8 reservations. A+R>S appeared only right after a re-register to S=1 (the FS-28 caveat).
- Mutants (executed, my own): M4a/b/pg, M5/pg, M6/pg, M8a and M9a/b were all killed, and the restored code passes. M8b and M8c (the re-check's R terms) survived, and so did the ADR's M8 as written (A-2).
- A-1 (executed): the rollback story is wrong. After a rollback to 26b440a, a v1.0.0 worker re-registering keeps the stored flag at TRUE. On roll-forward the serial worker got 1 running attempt plus 3 reservations. The fix is a doc change.
- A-3: a refused fallback claim returns 503, so the worker backs off instead of taking the 200 ms idle sleep. A-4: the "Decided" labels have no record from the orchestrator, and the task entry is still a placeholder. A-5: the PROJECT_STATE label is stale.
- Flaky tests on an idle machine: 0 failures on the branch and on 26b440a (RenewalPanic 200 runs + 100 with -race; PollTiming 50 + 50 with -race). Not a regression.
- Burst (1 worker, 8 slots, 60 jobs): a burst of 8 claims took at most 250 ms (1 s jobs) or 329 ms (100 ms jobs), with 0 lapses.
- make test-docs rc 0 and gofmt -l empty. Full suites were not run (the owner runs the gates).
