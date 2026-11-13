# P4-08g predictions (written before the final gates; counts only)

New top-level tests: 3 in `internal/storage` (memory: ClaimAgeArm_NeedsSilentPass, ClaimFallback_LiveReservationOfOther, AssignRevoke_BatchLimit) and the same 3 in `internal/storage/postgres`. No test was renamed or added in faultinject or abuse, so `tests/security-tests.list` is unchanged.

| Gate | Baseline (P4-08f) | Predicted |
|---|---|---|
| gofmt, vet, build, test-docs | clean | clean |
| unset, `go test -v -race -count=1 ./...` | 379 / 87 / 0 | 382 PASS / 90 SKIP / 0 FAIL (3 memory tests pass, 3 PostgreSQL tests skip) |
| make test-security | 72 / 0 / 0 | 72 / 0 / 0 |
| make test-db, three runs | 157 / 0 / 0 | 160 / 0 / 0 each |
| race, 5 groups (coordinator, storage, cmd, worker, api), own URL set, -p 1 | 310 / 0 / 0 | 316 / 0 / 0 |
