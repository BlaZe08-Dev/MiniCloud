Task: P4-4e fix round (audit items A to G)
Branch: agent/claude/P4-4e
Work tip (last commit before this report): f0d8ed9
Date: 2026-10-29

P4-4e fix round is done and pushed on agent/claude/P4-4e; handoff docs/agents/handoffs/P4-4e.md, evidence in docs/agents/handoffs/P4-4e-evidence/ (fix-* files).

A, oversize output: it was a poison loop. Built-in echo with a 65,535-byte payload ending in 3-byte runes: the executor cut at 64 KiB mid-rune, JSON turned the stray byte into U+FFFD, 65,538 bytes reached the coordinator, 400 OUTPUT_TOO_LARGE, the worker dropped the result (terminal 400, renewal stops), all 3 attempts LOST, 0 result rows, job FAILED after 6.4 s on PostgreSQL and 6.5 s on memory (lease expiry forced after 1.2 s; default-lease duration inferred, not run). A failing task with 100 KiB output and 40 KiB error text did the same. Fixed with ClampText (rune-safe cut, invalid bytes replaced as JSON does, truncation marker) in the executor (success and failure path) and the worker (output and error text). Red then green on both backends: jobs end SUCCEEDED or FAILED in under 1 s, nothing LOST. Mutation proofs M21, M22.
B: TestPostgresRepository_DispatchNextJob_SkipsLockedHead. Without SKIP LOCKED make test-db fails it (blocked 6.0 s); restored passes.
C: make test-security compares sorted names with tests/security-tests.list; renaming one test, and rename plus add, both fail with missing and extra names.
D: M18 is killed by TestPostgresRepository_Sweeper_CanonicalLockOrder under make test-db (3 subtests fail); F-4e-20 withdrawn; handoff corrected (22 mutations, 22 killed, 0 survived).
E: stored worker version is valid text with two U+FFFD (API and PostgreSQL agree, no 500, no code change). F: available_slots is unvalidated and ignored by both backends (code reading), Informational.

Gates: gofmt, vet, build, test-docs rc 0. Unset URL: PASS 238 SKIP 48 FAIL 0 (was 232/47/0: +6 PASS = 2 executor tests, 3 oversize tests, 1 version test; +1 SKIP = the SKIP LOCKED test without a database). make test-db: PASS 81 SKIP 0 FAIL 0 (was 80). make test-security: PASS 47 SKIP 1 FAIL 0 (48 tests, was 43+1). -race -count=5 on the new tests: all ok.

Not verified: default 30 s lease timing of the loop; PostgreSQL production mutations other than M18 and M20 were not re-run by me against make test-db (listed in the handoff); fix-round tests ran 5x, not 20x; cmd/mc untouched (F-4e-18 goes to another agent).
