ADR APPROVED. Start Stage B of V2-0b on branch agent/claude/V2-0b (tip 4e4d638). Follow ADR-0020 as written, with these decisions (orchestrator decisions; the owner can overrule):

Q1 Option A: optional executes_concurrently on register, migration 000006, V2-1 moves to 000007 (update every doc listed in ADR-0020 2.6).
Q2 Boolean named executes_concurrently. No integer count.
Q3 MaxReservationsPerWorker = 8, a fixed constant in domain. No setting.
Q4 No coordinator-wide switch. Operators use MINICLOUD_WORKER_SLOTS=1 on workers. Do not touch internal/config.
Q5 Update SEC-015 (total_slots). Fix the ADR-0017 6.2 citation that wrongly says SEC-016 for total_slots.
Q6 Yes: register the new abuse finding in docs/security/findings.md with the next free SEC number, Low, within SEC-004, using that file's own status words. Cite only tests that exist.
Q7 No. A reservation for a re-registered worker lapses normally (FS-28 test stays as planned). Add a one-line Proposed note in the ADR consequences.
Q8 No new slice. Add one Proposed candidate line in docs/agents/TASKS.md (trigger a pass when a full worker frees a slot).

Then: set ADR-0020 status to Accepted with date and the decisions above, as Decided.

USAGE (Claude is at peak hours, keep it small):
- Do not re-read ADR-0017/0019 or whole files; read only the functions you edit.
- While building, run only the targeted tests for the package you touched (go test -run ... -count=1). Run -race -count=50 only on the new and changed tests.
- Run each mutant against its named tests only, not the full suite.
- Run the full gates ONCE at the end: make test-docs, unset suite, make test-race, make test-db, make test-security (own database only). Never run make test-outage; the owner does.
- The 8-run measurement of 2.9 is required; run it once, report the table.
- Commit in small steps and push after each stage so a usage cutoff loses nothing.
- If you hit a limit mid-way, write what is done and what remains to reports/claude/V2-0b-progress.md before stopping.

Report in chat under 15 lines: tip, gate counts, mutant results, measurement table, any ADR deviation.
