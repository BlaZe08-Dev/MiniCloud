Task: P4-08f-review
Branch: agent/claude/P4-08f-review
Work tip (last commit before this report): adba37c
Date: 2026-11-14

P4-08f audit: CHANGES REQUIRED (test-only). Review: docs/agents/reviews/P4-08f.md; evidence: docs/agents/reviews/P4-08f-evidence/.
(g) No production code changed (only tests, docs, Makefile -p 1). Security list 72 names, matches go test -list.
(b) 18 PostgreSQL mutants on the 18 P4-08f tests: 11 killed, 7 survived (Q05, Q09, Q10, Q11, Q12, Q15, Q18). The postgres package kills Q05, Q09, Q12, Q15; Q10, Q11, Q18 survive everywhere.
High: H1 PostgreSQL age-arm pass-silence predicate (INV-16 mutation, Q11) unguarded. H2 property test skips INV-3 for 735 of 795 claims (refresh polls).
Medium: M1 fault suite alone misses 4 PostgreSQL arms; M2 DeepBacklog misses the INV-20 mutant (C01 passes 3/3; the coordinator test kills it); M3 property test is memory-only, no reference model, never hits RETRYING/LOST/grace/lapse limit/multi-type; M4 (c) pass-lock interference: any two DB runs on one database (test-db + test-security, mutation worktree sharing .env, ./... with URL exported); test-db alone, unset suite and test-outage are not exposed; lock confirmed per database.
Low: tautological FS-21 bound; (d) passUntilReserved masks a pass that intermittently reserves nothing (H01); revoke LIMIT unguarded; outage NoAgeClaim equals the restart path; flood lower bound weak on PostgreSQL.
(f) Note 35 figures match measure-5c3.log except ranges (43.4-53.2 ms, 242.4-283.1 ms, idle up to 57.8 ms) and "revoke" passes are lapses. Cost bound too low: confirmed (7 RT per reservation, 2 per lapse). FS-22 wording: confirmed by probe (stale heartbeat, the late poll returns 200); the "204" is in the 6.4 row, not 6.1.
