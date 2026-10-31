Task: P4-12 Fix Round 1
Branch: agent/gemini/P4-12
Work tip (last commit before this report): c485ef6
Date: 2026-11-01

## P4-12 Fix Round 1 Summary

### 1. Guard Script & Messages Fixes
- tests/require-own-db.sh: Reordered guards so empty/unset MINICLOUD_TEST_DATABASE_URL always refuses (exit code 2). ALLOW_SHARED_DB=1 bypasses only the shared-name refusal (minicloud_test).
- Added database name _test suffix validation (exit code 2), which cannot be bypassed by ALLOW_SHARED_DB=1.
- State real reason in error messages: tests/internal/sectest/pg.go guardDatabaseName unconditionally refuses minicloud_test and non-_test database names with a fatal SAFETY VIOLATION (t.Fatalf).
- Updated all 14 test cases in docs/agents/handoffs/P4-12-evidence.txt with raw stdout, stderr, and exit codes. Handoff trimmed to 37 lines.

### 2. Status Sync (Slice 5b / P4-08c)
- docs/agents/TASKS.md: Updated P4-08c active row, section, and Done table to merged in PR #38 at 3b12ae5. Did not touch 5c placeholder.
- docs/PROJECT_STATE.md: Added slice 5b entry under Section 2 Done with audit findings (F-R4 major, F-R2/4/5/6 fixed in round 2, F-R1 accepted in ADR note 15, F-R3 assigned to 5c) and orchestrator-verified quality gates; recorded main 3b12ae5 in Section 8.

### 3. Verification Gates
- test -z "$(gofmt -l .)": clean (rc 0)
- go vet ./...: clean (rc 0)
- go build ./...: clean (rc 0)
- make test-docs: PASS (1.478s)
- env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...: PASS 324 SKIP 81 FAIL 0
- make test-security: PASS 54 SKIP 0 FAIL 0 (on own database minicloud_gemini_test)
- make test-db: PASS 134 SKIP 0 FAIL 0 (on own database minicloud_gemini_test)
- Zero Go code modified; allowlist untouched; no line numbers in live docs.
