Task: V2-2-AUDIT
Branch: agent/gemini/V2-2-audit
Work tip (last commit before this report): 9383daf
Date: 2026-11-20

## Summary of Commands & Outputs

- Scope check: `git diff --stat origin/main...HEAD`
  Result: 24 files changed, 2656 insertions(+), 1 deletion(-). Confirmed no changes under `internal/`, `go.mod`, `go.sum`, `tests/docs/citation_allowlist.txt`, or migrations.
- Guard tests: `go test -run 'TestCheckDatabaseURL|TestCheckLoopbackURL' -count=1 ./cmd/mcbench`
  Result: PASS (`ok github.com/BlaZe08-Dev/Minicloud/cmd/mcbench 0.006s`).
- Result integrity: Recomputed min, median, p95 (nearest-rank), and max from raw values across `s1`, `s2`, `s3`, `s4` JSON runs.
  Result: Matches `.md` tables exactly. Warm-up runs excluded. `dirty: false`, `source: "real run"`.
- S1 Reproduction: `make bench SCENARIO=s1 BENCH_ALLOWED_DB=minicloud_gemini_test BENCH_OUT=/tmp/v22-audit-s1`
  Result: Refused by idle check (`1-minute load average 1.49 exceeds --max-load 1.00`). Per instructions, limit was not raised and wall time was not measured.
- Docs test suite: `make test-docs`
  Result: PASS (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 2.206s`).

## Findings Summary

- F-V2-2-01 (Major): `cmd/mcbench/guard.go:26` - Database guard bypass via query parameters (`?database=` or `?dbname=`).
- F-V2-2-02 (Minor): `docs/benchmarks/s2-20261119-deaa293.md:13` - `s2_poll_204_while_job_queued` reports 0.000 ms on N=0.
- F-V2-2-03 (Minor): `cmd/mcbench/idle.go:32` - 1-minute load average alone does not guarantee idle host (15-min was ~0.95).
- F-V2-2-04 (Minor): `docs/benchmarks/README.md:21` - Multi-component concurrency competition on dual-core host not stated in prose.
- Observations: O-1 wire key mismatch (`id` vs `attempt_id`); O-2 unauthenticated `lease_token_hash` and `output` exposure.

## Final Message

Tip: 9383daf
Verdict: APPROVE WITH FIXES
Findings: 1 Major, 3 Minor, 2 Observations (0 Blocker)
S1 Wall Time: Kept median 3848.436 ms (min 3135.626 ms, max 4053.443 ms); audit run refused by idle check (1-min load 1.49 > 1.00, limit not raised).
