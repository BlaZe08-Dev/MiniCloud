Task: P4-4e audit (round 2 revision)
Branch: agent/gemini/P4-4e-review
Work tip (last commit before this report): 63f69ac
Date: 2026-10-29

# P4-4e Audit Review Summary (Round 2)

## Verdict
**ACCEPT WITH CONDITIONS**

## Counts
- **Matrix rows checked / failing**: 38 rows checked, 0 failing (37 executed in test-security, 1 deferred as documented; all 264 test names confirmed existing in repo via `go-test-list.txt`).
- **Mutations executed vs evidence read**:
  - *PostgreSQL repository mutations executed independently*: 10 executed (PG-M01–PG-M10: 9 killed, 1 survived: PG-M01 `FOR UPDATE SKIP LOCKED;` in `DispatchNextJob`). Candidate 9 ceiling check substitute PG-M09 executed at coordinator service level.
  - *Builder mutations read from evidence*: 19 read from builder evidence (`mutations-run2.txt` and `mutations-run3-m15-m18-m19.txt`): 18 killed, 1 survived (M18 in faultinject suite).
- **Attacks re-executed**: Attack (b) re-executed with matching job_id, attempt_id, lease_token, and worker_id from fresh polls, plus worker version API readback:
  - Output over 64 KiB: returned HTTP 400 with code `OUTPUT_TOO_LARGE`.
  - Error over 10 KiB: returned HTTP 400 with code `INVALID_REQUEST`.
  - Invalid UTF-8 in output: returned HTTP 200 `COMMITTED` (safe JSON decoding into valid text).
  - NUL in lease_token / very long lease_token: returned HTTP 409 `STALE_ATTEMPT_REJECTED`.
  - Worker registration with raw invalid UTF-8 bytes (`\xff\xfe`): accepted (200 OK); read back through `GET /v1/workers` confirms stored version is valid UTF-8 string `\ufffd\ufffd` (hex `efbfbdefbfbd`, Unicode replacement characters).
  - All original attack scripts (a, b, c, d, e, f) preserved under `docs/agents/reviews/P4-4e-round1-evidence/scripts/`.

## Corrected Finding List with Severities
- **AUD-4e-01** (Informational): Builder claim M18 (F-4e-20) refuted. Reversing sweeper lock order is already killed by `TestPostgresRepository_Sweeper_CanonicalLockOrder` in `internal/storage/postgres/sweeper_test.go`. The product is better than claimed.
- **AUD-4e-02** (Low): PostgreSQL dispatch `FOR UPDATE SKIP LOCKED` test gap. Removing it survived all existing test suites. The service-wide mutex already serialises dispatch calls inside one coordinator, so the effect is not observable through the API with one coordinator; it matters with several coordinators.
- **AUD-4e-03** (Informational): Slow healthy DB queries (>8s) trip tick timeout and open a 30s outage grace window, delaying lease reclamation.
- **AUD-4e-04** (Informational): Worker registration with raw non-UTF-8 bytes is decoded by Go's JSON parser into valid UTF-8 replacement characters (`\ufffd\ufffd`), stored safely in PostgreSQL and parsed without error by API clients.
- **AUD-4e-05** (Informational): Heartbeat `available_slots` value is discarded (`_ int`) by both PostgreSQL and in-memory repository backends; capacity is computed dynamically from active attempts per ADR-0011.
- **AUD-4e-06** (Low): Worker result delivery with output exceeding 64 KiB returns HTTP 400 `OUTPUT_TOO_LARGE`. `isTerminalDelivery` treats 400 as non-retryable and `deliverResult` stops renewing the lease, leaving the attempt RUNNING until lease expiry. Built-in tasks executed via `Registry.Execute` are truncated at 64 KiB, so this is isolated to custom or buggy third-party workers. Builder will trace with real worker for verification in re-audit.
- **AUD-4e-07** (Low): Makefile false-green guard for `test-security` checks test count only (44) rather than test names.

## Conditions for Accept
1. **Address PostgreSQL dispatch test gap (AUD-4e-02)**: Add a database test with multiple concurrent workers contending on multiple queued jobs to prove non-blocking FIFO dispatch with `FOR UPDATE SKIP LOCKED`.
2. **Correct handoff claim regarding M18 and withdraw F-4e-20 (AUD-4e-01)**: Acknowledge that canonical sweeper lock order is already enforced and tested by `TestPostgresRepository_Sweeper_CanonicalLockOrder`, and withdraw F-4e-20.
3. **Schedule CLI fix for F-4e-18**: Ensure `cmd/mc` raw terminal escape sanitization is scheduled post-merge for the task owning the CLI.
4. **Strengthen false-green guard in Makefile (AUD-4e-07)**: Compare sorted test names against an expected manifest file rather than checking only the count.
5. **Verify oversize output worker trace (AUD-4e-06)**: Builder to trace result reporting with the real worker when output exceeds 64 KiB, to be verified in the re-audit.
