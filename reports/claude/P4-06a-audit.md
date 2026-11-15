Task: P4-06a audit (narrow)
Branch: agent/claude/P4-06a-review
Work tip (last commit before this report): 0311c50
Date: 2026-10-29

P4-06a audit done. Audited tip 459f88f2a6f3b107194d5cadec552f56b9f722dd (agent/gemini/P4-06a), fresh clone, no database, no docker. Review: docs/agents/reviews/P4-06a-round1.md, evidence: docs/agents/reviews/P4-06a-round1-evidence.txt.

Verdict: ACCEPT WITH FIXES.

Confirmed: scope (only AGENTS.md, README.md, the allowlist and the sec022 test changed outside docs/CHANGELOG/reports; the sec022 change is one comment line); 14 of 14 imports byte-identical, all additions, no collision, no P4-4e file; all 11 changed statements true to the code (README default and range 1..20, error_class values and omission on success, 409 STALE_ATTEMPT_REJECTED for an unknown attempt id under a real job, TestAPI_RenewLease_SuccessAndStaleRejection PASS in memory with 6 subtests, task deadline TRANSIENT with TestExecutor_Classify PASS, cancel-wins results row in both repositories, SEC-029 and SEC-030, the retired P4-4d label, the CHANGELOG and renewal arithmetic, the sec022 comment); PR #28 and c85a58f correct. make test-docs PASS; unset go test PASS 204 SKIP 35 FAIL 0.

Wrong items (5): (1) PROJECT_STATE SEC-029 entry cites Makefile line numbers; (2) the builder's handoff (4 statements) and report (5) carry README, CHANGELOG and sec022 line numbers, which the checker does not catch; (3) the handoff says the ADR-0016 results-row statement is verified by the conformance test, but that test does not assert the stored result (code reading only); (4) the AGENTS report-file rule does not say how follow-up reports are named while the repository already uses -review, -roundN and -recheck suffixes; (5) the handoff calls the 4e round 1 poison loop unsubstantiated, but the P4-4e fix round reproduced it (F-4e-21).

Allowlist: not authorised by the task. Its 7 entries match exact strings globally in every scanned document (6 scratch names plus the bare name of the integration wiring test family, which could hide a future truncated citation). Recommendation: DROP the P4-T1 review record from this import. Experiment: with the allowlist reverted, the P4-T1 review and evidence removed and two handoff lines neutralised, make test-docs passes; the record stays on agent/gemini/P4-T1-review for a later import with an explicit checker decision. If the orchestrator keeps it, refuse at least the bare-prefix entry.

F-4c-b-2 and F-4c-b-3: closing without registration is justified (design-intended per ADR-0016 race 2 and invariant 3, outcome safe); TASKS still lists both as follow-ups, so one must change; after the P4-4e merge both have executed end-to-end evidence.

Counts: 6 items checked, 5 wrong items, 7 allowlist entries, 14 imports identical.
