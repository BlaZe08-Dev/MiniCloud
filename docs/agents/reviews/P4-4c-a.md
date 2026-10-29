# Review P4-4c-a

Commit reviewed: 85b2246
Verdict: APPROVED WITH MINORS

## Summary

Task P4-4c-a implements Phase 4 Slice 4c on the coordinator side: lease sweeper cadence and lifecycle, dual-grace state machine (startup grace period and database outage grace window with cooldown), renew endpoint (`POST /v1/jobs/{id}/attempts/{attempt_id}/renew`), additive poll assignment lease metadata (`lease_expires_at` and `lease_duration_seconds`), coordinator binary background loop wiring (`cmd/coordinator`), and the `Makefile` `test-db` target update.

The implementation is well-tested and adheres to the state machine specifications in ADR-0014 (D-R3-01) and ADR-0016. All clock decisions correctly use repository database time (`Repository.Now`), no `time.Now()` leaks into lease expiration or grace calculations, and 409 conflict priority orders strictly match section 6.3 of `docs/protocol.md`. Six separate mutation proofs were constructed and verified to fail when broken and pass when restored. Concurrency stress tests with race detector (`-race -count=20`) passed cleanly without flakes.

Two findings are recorded: one Minor finding regarding a silent false-green risk in the `Makefile` `test-db` integration test recipe, and one Nit regarding the unexercised top-level `startBackground` invocation in `main()`. Neither finding blocks approval.

## Handoff claims re-run

| Claim | Command | Real Output | Match? |
|---|---|---|---|
| gofmt clean | `gofmt -l .` | empty output, rc=0 | Yes |
| go vet passes | `go vet ./...` | empty output, rc=0 | Yes |
| go build passes | `go build ./...` | empty output, rc=0 | Yes |
| documentation tests pass | `make test-docs` | `ok github.com/BlaZe08-Dev/Minicloud/tests/docs 0.789s`, rc=0 | Yes |
| DB URL unset test counts | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...` | `PASS 151 SKIP 35 FAIL 0`, rc=0 | Yes |
| Anchored integration test pattern | `go test -list '^TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit$' ./tests/integration/` | lists exactly `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit`, rc=0 | Yes |
| No docker/container calls in coordinator | `git grep -i -E "docker\|container" internal/coordinator/ cmd/coordinator/` | only a comment in `main_test.go`, no control calls | Yes |
| Concurrency stability (20x race) | `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=20 ./internal/coordinator/... ./cmd/coordinator/...` | `internal/coordinator` (3.805s), `cmd/coordinator` (11.783s), rc=0 | Yes |

## Findings

### F-4c-a-A1: Silent false-green risk in Makefile test-db target if target integration test is missing or renamed
- **Severity**: Minor
- **Evidence Label**: executed
- **Location**: `Makefile` target `test-db`
- **Evidence**:
  The `test-db` recipe runs:
  `MINICLOUD_TEST_DATABASE_URL="$$URL" go test -v -race -count=1 -run '^TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit$' ./tests/integration/`
  When verified in a throwaway copy where `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` was renamed, running this command printed `ok github.com/BlaZe08-Dev/Minicloud/tests/integration [no tests to run]` and exited with code 0. Because `make` checks only process exit status, if the test is ever renamed, moved, or deleted, `make test-db` will pass silently without running the test.
- **Suggested Fix**:
  In the `Makefile` recipe, verify that the test actually ran, for example by checking that `go test -list` outputs the target test name before running, or verifying the test output with `grep`, or failing if no tests were executed.

### F-4c-a-A2: Top-level invocation of startBackground in main() is not exercised by tests
- **Severity**: Nit
- **Evidence Label**: executed
- **Location**: `cmd/coordinator/main.go` function `main`
- **Evidence**:
  In `cmd/coordinator/main.go`, `stopBackground := startBackground(context.Background(), svc, cfg)` is called during startup. While `startBackground` itself is covered by unit tests in `cmd/coordinator/main_test.go`, `main()` is not executed during testing. When tested in a throwaway copy where `startBackground` and its shutdown hooks were commented out in `main()`, all tests in `cmd/coordinator` still passed with code 0 (`rc=0`).
- **Suggested Fix**:
  Add an end-to-end smoke test in `cmd/coordinator/main_test.go` that builds and launches the binary briefly with `MINICLOUD_STORAGE_DRIVER=memory` and validates background loops start, or accept as an acknowledged gap verified via manual binary smoke run.

## Assessment of Builder findings

- **F-4c-a-1 (Renew takes row lock before token comparison)**:
  Assessment: Agree with severity Low and deferral. In V1, the HTTP API is unauthenticated by design under SEC-004 on localhost. A lock-free token check in Postgres would require an unindexed or non-transactional read that introduces racing complexity. Rate limiting (R-23) in V2 and full worker authentication in V3 are the appropriate architectural mitigations.
- **F-4c-a-3 (Grace state is process-local; multi-coordinator limitation)**:
  Assessment: Agree with severity Low and deferral. MiniCloud V1 explicitly specifies a single coordinator process. Centralized distributed grace coordination across multiple coordinator instances belongs to multi-node coordination in V2/V3.

## Attacks attempted that did NOT find issues

1. **D-R3-01 Outage Window and Cooldown State Machine**:
   - Constructed tick sequences including failed tick -> success -> consecutive tick failures -> gap recovery. Verified that an outage window suppresses reaping for exactly one window length, that the cooldown arms upon window expiration, that reaping proceeds during cooldown, and that consecutive outage windows cannot open during cooldown.
   - Verified that `domain.SweepGraceState.Suppresses` checks `StartupGraceUntil` first, then `CooldownUntil` (allowing reaping to proceed), then `OutageWindowUntil`.
2. **Clock Domain Leakage**:
   - Grepped codebase for `time.Now()` across `internal/coordinator` and `internal/storage`.
   - Verified that `LeaseSweeper.Tick` derives all grace instants (`startupUntil`, `windowUntil`, `cooldownUntil`) and comparisons from `Repository.Now(ctx)`.
   - Verified that both `InMemoryRepository` and PostgreSQL `Repository` implement `Now(ctx)` on their respective storage timelines.
3. **Configuration Validation**:
   - Tested invalid, zero, and negative values for lease settings and grace periods.
   - Verified that `StartupGracePeriod < LeaseDuration` and `OutageGraceWindow < LeaseDuration` return `ErrInvalidGraceConfiguration`.
   - Verified that non-positive lease durations and sweeper intervals are rejected.
4. **Renew Route, Handlers, and HTTP 409 Priority Order**:
   - Verified that `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` enforces identifier validation and body limits.
   - Verified that 409 conflict responses evaluate in protocol priority order: terminal job returns `JOB_ALREADY_TERMINAL`, swept/LOST attempt returns `LEASE_EXPIRED`, superseded or token mismatch returns `STALE_ATTEMPT_REJECTED`.
   - Verified that rejected renewals modify no state and emit no events.
   - Verified that `CANCEL_REQUESTED` job returns `{"status":"RENEWED","cancel_requested":true}` without extending `lease_expires_at`.
5. **Poll Work Lease Metadata**:
   - Verified that `WorkAssignment` additively includes `lease_expires_at` and `lease_duration_seconds`.
   - Verified that heartbeat response remains static `{"status":"ACK"}` with no cancellation data.
6. **Background Loop Lifecycle and Cleanup**:
   - Verified that `startBackground` launches both the worker reaper and lease sweeper.
   - Verified that `stopBackground` cancels context, waits on loops via `sync.WaitGroup`, is idempotent / safe to call twice, and is called before the PostgreSQL pool closes on both normal shutdown and shutdown-error paths.

## Mutation Spot Checks

Six mutations were evaluated in a throwaway copy and confirmed to fail when broken and pass when restored:
1. **Cooldown bypass in domain grace check**: removing cooldown check in `internal/domain/sweep.go` failed `TestDomain_SweepGraceState_Suppresses` (rc=1).
2. **Repository clock replaced with wall clock**: replacing `repo.Now(ctx)` with `time.Now().UTC()` in `internal/coordinator/sweeper.go` failed 10 tests including `TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock` (rc=1).
3. **Grace duration validation bypassed**: removing `StartupGracePeriod < LeaseDuration` check in `internal/config/config.go` failed `TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected` (rc=1).
4. **Poll assignment metadata dropped**: omitting `lease_expires_at` in `internal/coordinator/service.go` failed `TestAPI_PollAssignment_CarriesLeaseMetadata` (rc=1).
5. **Renew response status mutated**: changing status to `"OK"` in `internal/api/handlers.go` failed `TestAPI_RenewLease_SuccessAndStaleRejection` and `TestAPI_RenewLease_AcceptUntilSweptAndCancelDelivery` (rc=1).
6. **Cooldown ignored when opening outage window**: removing cooldown check in `advance` in `internal/coordinator/sweeper.go` failed `TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace` (rc=1).

## Tests added

No tests were added directly to production code by the auditor in this review, as the builder's test coverage across `sweeper_test.go`, `main_test.go`, `config_test.go`, and `api_test.go` was verified to be thorough and comprehensive. All audit verification was conducted via non-destructive inspection, static analysis, command re-runs, and throwaway mutation worktrees.
