# Handoff P4-4c-a

Branch / last commit: `agent/claude/P4-4c-a`, based on `origin/main` at `7e58d4a` (merge of slice 4b). Round 1 code and tests were complete at `03739f3`; round 2 (fix-up, see the section "Round 2" below) follows. The last commit is the handoff commit. Nothing merged, `main` untouched.

**Status: coordinator side of slice 4c built and wired into the coordinator binary, all gates green, 50 mutation proofs run (three survived at first, tests were strengthened, then they died). Round 2 resolved every open question that was blocking; read "Round 2" first.**

## What changed (by package)

- `internal/storage`: `Repository` gains `Now(ctx) (time.Time, error)` (owner decision Q-1). In-memory returns its injected clock; PostgreSQL runs `SELECT clock_timestamp()` and maps errors with `mapPgError`. Shared conformance `RunNowConformance` (`storagetest`).
- `internal/config`: `CoordinatorConfig` gains `LeaseDuration` (30 s, `MINICLOUD_LEASE_DURATION`), `SweeperInterval` (5 s, `MINICLOUD_SWEEPER_INTERVAL`), `StartupGracePeriod` (30 s, `MINICLOUD_STARTUP_GRACE_PERIOD`), `OutageGraceWindow` (30 s, `MINICLOUD_OUTAGE_GRACE_WINDOW`); `ErrInvalidGraceConfiguration`. Validation: lease and sweeper interval positive; startup grace and outage window each `>=` lease duration (equality allowed).
- `internal/coordinator`
  - New `sweeper.go`: `LeaseSweeper` (`Tick`, `Run`), `Service.NewLeaseSweeper`, `Service.StartLeaseSweeper(ctx, interval)`, `Service.SetGracePeriods`, `Service.SetSweepRandom`. The sweeper owns and advances the grace state; the repository only reads it.
  - `service.go`: `Service.RenewLease` (logs rejections, no event, no service-wide lock), `Service.SetLeaseDuration`, and `WorkAssignment` gains the additive `lease_expires_at` and `lease_duration_seconds` (protocol 6.2).
- `internal/api`: route `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` and `HandleRenewLease`; error statuses come from the existing `mapDomainErrorToHTTP` (no change to it).
- `tests/integration`: new `postgres_restart_grace_test.go`.
- Not touched in round 1: `internal/worker`, `cmd/*` (round 2 edits `cmd/coordinator`), every design doc, `TASKS.md`, `CHANGELOG.md`, `PROJECT_STATE.md`, `findings.md`, `internal/domain`, `SweepExpiredLeases` semantics.

## Design decisions and deviations (with reasons)

- **D1 Clock domain (Q-1).** Smallest repository method that keeps one clock domain: `Now(ctx)`. Every grace instant (startup deadline, outage window end, cooldown end) is `repo.Now()` plus a duration; the in-lock check compares with the same clock. Pinned by `TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock` (the fake repository clock is in 2031, so a process-clock implementation fails; mutation c16). Alternative considered: pass durations instead of instants. Rejected: it changes the 4b `SweepGraceState` contract.
- **D2 State machine of the sweeper** (all on repository time): a tick reads `Now`; arms the cooldown (`windowEnd + window`) as soon as it sees the window over; opens a window (`now + window`) only if a trigger exists (a failed tick since the last success, or a gap over 3x the interval between consecutive successful ticks, strictly over) **and** no window and no cooldown is active; the trigger is consumed by every successful tick either way (a failure during a window or cooldown does not queue a later window; test `..._HealthyTicksAfterRecoveryNeverReopenWindow`, mutation c07). The cooldown is **not** set while the window is open, because the 4b in-lock rule says an active cooldown means reaping proceeds and would defeat the window (mutation c12). The tick works on a copy of the state and commits it only if the sweep returned no error, so a window decided on a tick whose sweep then fails is discarded and decided again on the next successful tick (the failure flag stays set).
- **D3 Only a returned error is a failed tick**: an error from `Now` or from `SweepExpiredLeases` (even with a partial result). `SkippedLocked` is never a failure (`..._CandidateLockTimeout_EntersNoGrace`, c08). `SkippedChanged > 0` is logged at warn level (c20).
- **D4 Startup grace anchor.** The first tick runs immediately when the sweeper starts, and the grace ends at the **first successful repository time read** plus the grace period. If the database is down at boot the anchor (and the grace) is later than "30 s from process start"; this is the conservative direction and the outage window opens on recovery anyway. Recorded as a deviation from the literal "from coordinator start".
- **D5 Defaults are fail-safe.** A `Service` built without `SetGracePeriods` has 30 s startup grace and 30 s window, so forgetting to configure grace does not reap healthy attempts after a restart (4b finding S3 is fail-open in the repository by design).
- **D6 `Service.RenewLease` takes no service-wide lock.** `CommitResult` and `PollWork` hold `s.mu` for the whole repository call; a renewal every 10 s per attempt should not queue behind a slow commit. All atomicity is the repository's (row locks). It writes no event (Q-8 confirmed by the owner): tests assert event counts do not change on a rejected renewal, on a terminal job and on a LOST attempt.
- **D7 Renew error mapping** is the shared `mapDomainErrorToHTTP` table, exactly protocol 6.3: terminal job 409 `JOB_ALREADY_TERMINAL`; LOST attempt 409 `LEASE_EXPIRED`; unknown attempt id, wrong token, superseded 409 `STALE_ATTEMPT_REJECTED` (as for commit, Q-5); wrong worker 403 `WORKER_MISMATCH`; unknown job 404 `JOB_NOT_FOUND`; outage 503 `SERVICE_UNAVAILABLE` with `Retry-After: 1`.
- **D8 Request validation** mirrors the result endpoint: job id, attempt id and worker id must match the identifier rule (1..128 of `[a-zA-Z0-9_-]`), malformed JSON is 400 `MALFORMED_JSON`, the global 1 MiB body limit applies. The lease token is not length-limited or required non-empty (the result endpoint does not either); it is hashed and compared in constant time by the repository and never logged. See Q5.
- **D9 Poll expansion.** `lease_duration_seconds` is the integer number of seconds of the lease duration given by `SetLeaseDuration` (default 30). Integer seconds: a sub-second lease advertises 0 and the field is omitted. The workers derive their renewal interval from it (one third), so there is no renewal-interval setting.
- **D10 Heartbeat cleanup** was already complete (the response is the static `{"status":"ACK"}`; no `cancelled_job_ids` exists in the code). `TestAPI_Heartbeat_ResponseIsAckOnly` pins it with a cancelled job present.
- **D11 Immediate first tick** in `Run`, then every interval. The ticker is not reset after a slow tick (Go ticker drops ticks); a slow sweep shows up as a database-time gap, which is the intended trigger.
- **D12 Sweeper batch size** is the `domain.DefaultSweepBatchSize` (50). ADR-0014 names `MINICLOUD_SWEEPER_BATCH_SIZE`, the plan does not list it; not added (Q4).

## Gating tests from the plan (coordinator side of 4c)

| Plan test | Status |
|---|---|
| `TestAPI_RenewLease_SuccessAndStaleRejection` | executed (memory-backed server) |
| `TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt` | executed |
| `TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping` | executed |
| `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` | executed against `minicloud_claude_test` (with a no-grace control subtest); skipped when the database URL is unset, so it is not in the DB-unset pass count. Run with a narrow `-run` (see Commands); `make test-db` does not include `./tests/integration` |
| `TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace` | executed |
| `TestCoordinator_LeaseSweeper_InterTickGapBelowThreshold_EntersNoGrace` | executed (gaps of 1x, 2x and exactly 3x) |
| `TestCoordinator_LeaseSweeper_FailedTick_EntersGrace` | executed (sweep error and database-time read error) |
| `TestCoordinator_LeaseSweeper_CandidateLockTimeout_EntersNoGrace` | executed |
| `TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace` | executed |
| The worker-side tests of the plan's 4c list (agent renewal and result delivery) | not mine (Gemini) |

Tests I added beyond the plan list are in the next section.

## Tests added (name, file, property, mutation proof)

Mutation proofs are all in the appendix; ids in brackets. All were run in `~/mc-mut-claude` (a throwaway worktree with a copied `.env`, removed afterwards).

- `internal/coordinator/sweeper_test.go`
  - `..._StartupGraceInstantComesFromRepositoryClock`: Q-1 and the anchor does not slide [c15 c16 c17].
  - `..._PassesBackoffWithInjectedRandomness`: `SweepParams.Backoff` is non-nil and uses the injected randomness [c18 c22].
  - `..._InterTickGapExceedsThreshold_EntersGrace`, `..._InterTickGapBelowThreshold_EntersNoGrace`: the gap threshold is strictly over 3x [c01 c02 c03 c04].
  - `..._FailedTick_EntersGrace`, `..._PartialResultWithErrorIsFailedTick`, `..._FailedFirstTickThenRecovery`: failed-tick tracking [c05 c06].
  - `..._CandidateLockTimeout_EntersNoGrace`: skips are not failures [c08].
  - `..._CooldownPreventsConsecutiveGrace`: cooldown starts when the window ends, lasts one window length, opens no window for a failure or gap, and is not active while the window is open [c09 c10 c11 c12].
  - `..._FailureWhileWindowOpenDoesNotExtendIt`: suppression never exceeds one window [c13 c14].
  - `..._HealthyTicksAfterRecoveryNeverReopenWindow`: a consumed trigger stays consumed [c07; first survived, see appendix].
  - `..._SkippedChangedLoggedAtWarn` [c20]; `..._RunSurvivesFailedTicksAndStopsOnCancel` [c21].
  - End to end through the real in-memory sweep: `..._ReclaimsAbandonedAttempt` (reap, retry with the injected backoff, other worker claims attempt 2, late commit rejected), `..._StartupGrace_SuppressesReaping` (held back, surviving commit accepted, reaped after the grace) [c15], `..._OutageWindow_SuppressesReapingAfterRecovery` (failed tick, expired attempt held back for exactly one window, then reaped) [c19].
- `internal/api/api_test.go`: `TestAPI_RenewLease_SuccessAndStaleRejection`, `..._AcceptUntilSweptAndCancelDelivery`, `..._RequestValidation`, `..._OutageMapsTo503`, `TestAPI_PollAssignment_CarriesLeaseMetadata`, `TestAPI_Heartbeat_ResponseIsAckOnly` [a01..a12].
- `internal/config/config_test.go`: `TestLoadCoordinatorConfig_LeaseDefaults`, `..._LeaseSettingsFromEnv`, `TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected` (startup and outage, below, zero, equal), `TestConfig_Validation_NonPositiveLeaseSettings_Rejected` [f01..f08].
- `internal/storage`: `TestConformance_Now_IsRepositoryClock` (both repositories; brackets, monotonic, same timeline as the dispatch lease), `TestInMemoryRepository_Now_UsesInjectedClock` [r01 r02 r03].
- `tests/integration/postgres_restart_grace_test.go`: `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` [c15].

## Commands run + real output

Static gates at the final commit:

```
== gofmt
rc=0
== vet
rc=0
== build
rc=0
== test-docs
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	0.614s
rc=0
```

Baseline before any change, DB URL unset (`go test -v -race -count=1 ./...`):

```
PASS 117 SKIP 34 FAIL 0
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/migrate	1.038s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	3.840s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.032s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.137s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.173s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.089s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	1.026s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.025s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.455s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	1.106s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	2.614s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	1.040s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	12.499s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	8.371s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/review	1.067s
```

Final, DB URL unset (same command):

```
PASS 146 SKIP 35 FAIL 0
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/migrate	1.026s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	4.642s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.041s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.170s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.144s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.105s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	1.035s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.027s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.410s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	1.125s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	2.662s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	1.061s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	12.204s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	8.407s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/review	1.048s
```

(+29 PASS; the one new SKIP is `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit`, which needs the database. A skip is not a pass.)

`make test-db` (runs `./internal/storage/postgres/...` only), baseline then final; counts include subtests as in the 4b handoff:

```
baseline: PASS 146 FAIL 0 SKIP 0   (41 top-level)
final:    PASS 147 FAIL 0 SKIP 0   (+1: TestConformance_Now_IsRepositoryClock)
--- PASS: TestPostgresRepository_Sweeper_LockTimeoutDoesNotLeakToPool (0.41s)
PASS
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	61.505s
```

The restart-grace test, run on its own against `minicloud_claude_test` with an anchored `-run` so that no other test of the package (in particular the outage test that restarts the shared container) can match; `go test ./...` was never run with the URL set:

```
$ MINICLOUD_TEST_DATABASE_URL=<from .env> go test -count=1 -race -v -run '^TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit$' ./tests/integration/
--- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit (1.08s)
    --- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit/startup_grace_protects_the_surviving_worker (0.62s)
    --- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit/control_without_grace_reaps_and_rejects_the_late_commit (0.46s)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	2.108s
```

Stability, DB URL unset: `go test -count=20 -race ./internal/coordinator/ ./internal/api/ ./internal/config/`:

```
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	3.584s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	71.641s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.144s
```

Red before green (commands whose first run failed, before the code existed): the `Now` conformance did not compile (`h.Repo.Now undefined (type storage.Repository has no field or method Now)`), the config tests did not compile (`cfg.LeaseDuration undefined`), and with the poll fields present but no route the renew tests failed:

```
--- FAIL: TestAPI_RenewLease_SuccessAndStaleRejection (0.00s)
--- FAIL: TestAPI_RenewLease_AcceptUntilSweptAndCancelDelivery (0.00s)
--- FAIL: TestAPI_RenewLease_RequestValidation (0.51s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/malformed_json (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/empty_body (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/missing_worker_id (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/worker_id_with_illegal_characters (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/worker_id_too_long (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/job_id_with_illegal_characters (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/attempt_id_with_illegal_characters (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/attempt_id_too_long (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/body_over_the_1_MiB_server_limit (0.00s)
    --- FAIL: TestAPI_RenewLease_RequestValidation/only_POST_is_routed (0.00s)
--- FAIL: TestAPI_RenewLease_OutageMapsTo503 (0.00s)
FAIL
FAIL	github.com/BlaZe08-Dev/Minicloud/internal/api	0.529s
```

## Known gaps / TODOs

- (Round 1 gap, closed in round 2) the coordinator binary did not start the sweeper.
- No test runs the sweeper against a real database outage (forbidden by the hard rule: it restarts the shared container). Outage behaviour is covered with fake repositories that fail on demand, and the restart behaviour with a real PostgreSQL via the lease-expiry-by-SQL scenario.
- Grace state is per coordinator process (see finding F-4c-a-3).
- A tick has no timeout of its own; in production the pool's `statement_timeout` bounds each query.
- The in-memory `CommitAttempt`/`DispatchJob` wall-clock gap from 4b is unchanged.

## Open questions for the orchestrator

- **Q1 (ownership) RESOLVED in round 2.** `cmd/coordinator/main.go` must wire the sweeper.** I do not own `cmd/**`. Needed, about eight lines: pass `postgres.WithLeaseDuration(cfg.LeaseDuration)` and `storage.WithLeaseDuration(cfg.LeaseDuration)` to the repositories; `svc.SetLeaseDuration(cfg.LeaseDuration)`; `svc.SetGracePeriods(cfg.StartupGracePeriod, cfg.OutageGraceWindow)`; and `go svc.StartLeaseSweeper(sweeperCtx, cfg.SweeperInterval)` next to `StartReaper`. The memory driver should start it too. Without it `Terminating a worker process triggers automated lease expiry` (4c exit criterion) cannot hold end to end.
- **Q2 RESOLVED in round 2 (F-4b-A1 withdrawn).** (audit finding F-4b-A1 vs this task).** The 4b audit says reaping must be suppressed during the cooldown and asks to change `SweepGraceState.Suppresses`. ADR-0014, the plan and this task's brief say the opposite: during the cooldown no new window opens **and reaping proceeds**, and startup grace wins over the cooldown. 4b was merged with the latter and I did not touch it. This slice is built to that reading (the sweeper never has a window and a cooldown active at once, so the two readings differ only in how a cooldown overlapping a window is treated, which this sweeper never produces). If the owner wants the audit's reading, the repository check and the cooldown tests change, not this slice's behaviour. Needs an owner ruling so the audit finding can be closed.
- **Q3 RESOLVED in round 2 (strict rule kept).** (docs) contradiction in ADR-0014.** The validation rule rejects a grace shorter than the lease, but the "Impact on Existing Tests" paragraph says micro-lease tests "must explicitly configure `MINICLOUD_STARTUP_GRACE_PERIOD=0s`". Both cannot hold with the env path. I implemented the rule (strict, as the brief says). Tests that need no grace use `Service.SetGracePeriods(0, ...)` in code, which bypasses config.
- **Q4 (docs)** ADR-0014 lists `MINICLOUD_SWEEPER_BATCH_SIZE`; the plan's 4c does not. Not added. Say if wanted.
- **Q5 (protocol) lease token bounds.** field-limits.md has no limit for the request `lease_token`; I mirrored the result endpoint (1 MiB body cap only, empty accepted and rejected as stale). A cheap bound (for example 256 bytes, 400 `INVALID_REQUEST`) would be a design change and a worker-visible one (400 makes the worker stop renewing and keep running), so I left it.
- **Q6 (docs) protocol 6.3 row 5.** The table lists a 404 `WORKER_NOT_FOUND` for a reporting worker that does not exist. Both repositories compare the worker id with the attempt's and return `WORKER_MISMATCH` (403) for any other id, existing or not (4b D5). Row 5 is unreachable for renewals and commits. Worker-side code that handles `WORKER_NOT_FOUND` as terminal is harmless.
- **Q7 (docs pass).** (a) ADR-0014 "Planned Grace Verification Tests" names `TestPostgresRepository_Sweeper_*` for the outage/cooldown tests; the implemented tests are the lease sweeper tests in the coordinator package (names starting with the coordinator and lease sweeper prefix) because the state machine lives in the coordinator. (b) Plan item R-23 (renewal flooding documented as designed, not implemented) is a docs item; no code. (c) `make test-docs` reported nothing stale or unlabeled after my changes, so I edited no doc and no allowlist.
- **Q8 (worker side).** The worker should read `lease_duration_seconds` from the poll assignment (renewal interval = one third) and treat a missing field (old coordinator, or a sub-second test lease) as "use the default 30 s". The renew request body is exactly `{"worker_id","lease_token"}`. A 200 with `cancel_requested: true` carries the unextended expiry.
- **Q9 (4d).** Nothing of 4d was built: no cancel-wins commit outcome, no SEC-012 clamp, no classification.

## Security review

**Assumptions challenged**
- A1 *The grace state and the in-lock check share a clock.* Challenge: a coordinator that computed instants from process time would be wrong exactly when the clocks differ. Evidence: **executed** (`TestConformance_Now_IsRepositoryClock`, `..._StartupGraceInstantComesFromRepositoryClock` with a fake repository clock 5 years away; mutations c16, r01, r02, r03). A same-host wall clock mutation of the PostgreSQL `Now` cannot be told from the database clock (same machine), so r03 is a skewed-clock mutation, not a wall-clock one.
- A2 *A lock timeout or skip must not look like an outage.* **executed** (`..._CandidateLockTimeout_EntersNoGrace`, c08). Conversely a sweep that returns an error together with a partial result is a failed tick (`..._PartialResultWithErrorIsFailedTick`, c06).
- A3 *Suppression must be bounded.* A flapping database could keep reaping off forever if windows extended or re-triggered. **executed**: failures during a window do not extend it (c13, c14), a failure or gap during the cooldown opens nothing and is not deferred (c09, c12, c07), and 200 s of healthy ticks open no window (`..._HealthyTicksAfterRecoveryNeverReopenWindow`). The first run of that last test did not exist and mutation c07 **survived**; I added the test and c07 then failed (appendix).
- A4 *Grace must not be forgotten.* The repository check is fail-open for a zero `SweepGraceState` (4b S3). **executed**: `..._StartupGrace_SuppressesReaping`, `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` and its no-grace control (the control shows the same scenario reaping and answering the late commit with 409, so the test would fail without grace; c15). Defaults are grace-on (D5). Round 1 left this open (nothing started the sweeper in the binary); closed in round 2.
- A5 *Renewal as an unauthenticated oracle and a lock source.* Within SEC-004 (accepted). New, specific to this route: `RenewLease` locks the job row `FOR UPDATE` **before** the token is checked (4b D5/D6), so an unauthenticated caller who knows a job id can take that row lock repeatedly with wrong tokens and contend with the job's commit, cancel and dispatch. See F-4c-a-1.

**Attacks tried (real tests)**: path ids with illegal characters, over-long ids, an over-long worker id, a missing worker id, malformed and empty bodies, a body over 1 MiB (all 400, none 500; `TestAPI_RenewLease_RequestValidation`); wrong worker, wrong token, unknown attempt, unknown job, terminal job and swept attempt on renewal, with event counts unchanged (`TestAPI_RenewLease_SuccessAndStaleRejection`); GET on the renew path (405); a renewal after a rejected one still returns the unmoved lease; renewal of a lapsed unswept lease (extended) and of a cancelled job (flag set, expiry unchanged) (`..._AcceptUntilSweptAndCancelDelivery`); a connection error in the repository (503, opaque body, no address leaked; `..._OutageMapsTo503`); coordinator restart with an expired lease against real PostgreSQL; repeated sweeper failures and recoveries with window, cooldown and gap combinations; sweeper loop with a repository that always fails.

**Not tested**: a real database outage under a running sweeper (the hard rule forbids restarting the shared container), a flood of renewals (no load test; R-23 is documented not implemented), two coordinator processes against one database, a sweep tick that hangs without a deadline, clock jumps of the database itself, `ctx` cancellation in the middle of a PostgreSQL sweep, a renewal body with a token of 1 MiB through the real server and PostgreSQL (the 1 MiB body test is memory-backed).

**Findings**
- **F-4c-a-1 (Low, within SEC-004).** An unauthenticated caller who knows (or derives, 4b F-4b-2: ids are `att_<jobID>_<n>`) a job id can send renewals with any token. The repository takes the job row lock before the token comparison, so a flood serializes with that job's commit, cancel and dispatch, and a LOST attempt answers `LEASE_EXPIRED` instead of `STALE_ATTEMPT_REJECTED` (the 4b F-4b-1 oracle). Evidence: **confirmed by code reading** (`RenewLease` in the PostgreSQL repository); the ordering is covered by 4b tests but the flooding effect was not measured. Suggested: token check before the row lock needs a lock-free read, a design change; rate limiting is the planned defence (R-23).
- **F-4c-a-2 (Informational).** Startup grace is anchored at the first successful database read, not at process start (D4). If the database is down at boot the grace starts later, which only suppresses reaping longer, bounded by the first successful tick. Evidence: **executed** (`..._FailedFirstTickThenRecovery`).
- **F-4c-a-3 (Low).** Grace state lives in the coordinator process. A second coordinator against the same database has its own state, so one coordinator's startup grace does not stop the other from reaping. V1 assumes one coordinator. Evidence: **confirmed by code reading** (`LeaseSweeper.state`), not executed.
- **F-4c-a-4 (Informational).** While reaping is held back by grace, the sweeper logs one info line per tick when candidates are suppressed (up to six per window with the default cadence). Not attacker-triggerable: candidates exist only for attempts that were dispatched. Evidence: **executed** (visible in the output of `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` run with `-v`).
- No new unauthenticated behaviour beyond SEC-004 apart from F-4c-a-1; I did not edit `findings.md`. Next free numbers are SEC-027 and up; the orchestrator assigns.

## Round 2 (fix-up after the orchestrator's answers)

Every authorisation below was authorised by the orchestrator in the planning session; the owner accepts it by merging. None of it is recorded here as an owner decision.

**Resolutions of the open questions**
- **Q1 resolved.** Editing `cmd/coordinator/main.go` (and a test for it) was authorised by the orchestrator in the planning session; the owner accepts it by merging. Done, see below.
- **Q2 resolved.** F-4b-A1 was withdrawn (authorised by the orchestrator in the planning session; the owner accepts it by merging). My reading stands: reaping proceeds during the cooldown and startup grace wins over the cooldown. Nothing in 4b changed.
- **Q3 resolved.** The strict rule (grace at least the lease) stays; micro-lease tests set the lease and the grace both small (authorised by the orchestrator in the planning session; the owner accepts it by merging). The wiring tests use a 40 ms lease and a 40 ms startup grace. The ADR wording is for the docs pass.
- **F-4c-a-1 and F-4c-a-3** stay as findings in this handoff; the owner registers them. Unchanged.
- **Gate gap (item 5)** resolved by a minimal edit of the `test-db` target, authorised by the orchestrator in the planning session; the owner accepts it by merging.

**What changed in round 2**
- `cmd/coordinator/main.go`: new `newMemoryRepository`, `configureService` and `startBackground`. `startBackground` starts the worker reaper and the lease sweeper once each under one cancellable context and returns a `stop` that cancels and waits for both loops (safe to call twice). `main` calls it once, and calls `stop` after the HTTP server has shut down and before the PostgreSQL pool closes (also on the shutdown-error exit path). Both storage drivers get the configured lease (`postgres.WithLeaseDuration`, `storage.WithLeaseDuration`); `configureService` passes the lease and the two grace settings to the service; the sweeper runs at `cfg.SweeperInterval`.
- `cmd/coordinator/main_test.go` (new): see the tests below.
- `Makefile`: the `test-db` target now runs `./internal/storage/postgres/... ./internal/coordinator/...` and then, as a separate command, `./tests/integration/` with the anchored `-run` for the restart-grace test only. The restart-grace test lives in `tests/integration` (not in the coordinator package), whose package also holds the outage test that restarts the shared container, so a whole-package run was not an option; the anchored pattern matches exactly one test (output below).

**New deviations**: `cmd/**` and the `Makefile` edited with the authorisation above (both outside my original ownership list). `make test-db` now also runs `./tests/integration/` with the anchored pattern; this is wider than the "coordinator package" you named and is the only way to make the restart-grace test part of the gate without running the outage test.

**Confirmation that nothing in the coordinator package restarts the container** (and that the anchored pattern matches one test):

```
== tests matched by the anchored -run in tests/integration:
TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	0.010s
== container-control calls in internal/coordinator (expect none):
rc=1
```

**New tests** (`cmd/coordinator/main_test.go`, real clock at millisecond scale, no server, no container):
- `TestMain_BackgroundLoops_ReapAbandonedAttempt`: end to end on the in-memory driver; a worker claims a job and never renews; the sweeper started by `startBackground` reaps the attempt (LOST) and the job goes RETRYING [w1 w2 w3].
- `TestMain_BackgroundLoops_StartupGraceFromConfig`: a one-hour startup grace from the config keeps the same expired attempt RUNNING [w2].
- `TestMain_BackgroundLoops_StopWaitsForLoops`: a sweep that ignores cancellation is in flight when `stop` is called; `stop` returns only after it finished [w4].
- `TestMain_BackgroundLoops_ReaperRuns`: the worker reaper is started by the same call [w5].
- `TestMain_NewMemoryRepository_UsesConfiguredLease` [w3].
- Not covered by a test: that `main` itself calls `startBackground` (it would need a running server). That call is two lines and was checked by running the real binary (smoke output below).

**Smoke run of the real binary** (in-memory driver, no database, no container): started with `MINICLOUD_STORAGE_DRIVER=memory MINICLOUD_HTTP_ADDR=127.0.0.1:18089 MINICLOUD_SWEEPER_INTERVAL=1s`, `/readyz` returned 200, then SIGTERM:

```
readyz=200
exit=0
{"time":"2026-10-28T18:52:26.692638757+05:30","level":"INFO","msg":"starting MiniCloud coordinator..."}
{"time":"2026-10-28T18:52:26.692759223+05:30","level":"INFO","msg":"using in-memory storage driver"}
{"time":"2026-10-28T18:52:26.693130219+05:30","level":"INFO","msg":"coordinator listening for requests","addr":"127.0.0.1:18089"}
{"time":"2026-10-28T18:52:28.738536341+05:30","level":"INFO","msg":"received termination signal, initiating graceful shutdown","signal":"terminated"}
{"time":"2026-10-28T18:52:28.7387248+05:30","level":"INFO","msg":"coordinator stopped cleanly"}
```

**Red before green** for the wiring tests (first run, before `startBackground` existed):

```
# github.com/BlaZe08-Dev/Minicloud/cmd/coordinator
# [github.com/BlaZe08-Dev/Minicloud/cmd/coordinator]
vet: cmd/coordinator/main_test.go:37:10: undefined: newMemoryRepository
```

**Mutation proofs of the wiring** (throwaway worktree `~/mc-mut-claude`, `.env` copied in, both removed afterwards). Mutations w4 and w5 **survived** the first versions of the tests (the stop test only counted ticks and no test covered the reaper); the tests above were strengthened and both then failed. Driver summary, verbatim:

```
w1-sweeper-not-started: mutated rc=1 (want 1), restored rc=0 (want 0)  [cmd/coordinator/main.go]
w2-grace-config-not-applied: mutated rc=1 (want 1), restored rc=0 (want 0)  [cmd/coordinator/main.go]
w3-lease-config-not-applied-to-repo: mutated rc=1 (want 1), restored rc=0 (want 0)  [cmd/coordinator/main.go]
w4-stop-does-not-wait, w5-reaper-dropped (first run, before the stronger tests): SURVIVED mutated rc=0
w4-stop-does-not-wait: mutated rc=1 (want 1), restored rc=0 (want 0)  [cmd/coordinator/main.go]
w5-reaper-dropped: mutated rc=1 (want 1), restored rc=0 (want 0)  [cmd/coordinator/main.go]
```

Failing tests of the mutated runs, verbatim (line numbers omitted from the failure messages, which are not shown):

```
w1-sweeper-not-started:
--- FAIL: TestMain_BackgroundLoops_ReapAbandonedAttempt (10.00s)
rc=1
w2-grace-config-not-applied:
--- FAIL: TestMain_BackgroundLoops_ReapAbandonedAttempt (10.00s)
rc=1
w3-lease-config-not-applied-to-repo:
--- FAIL: TestMain_BackgroundLoops_ReapAbandonedAttempt (10.00s)
--- FAIL: TestMain_NewMemoryRepository_UsesConfiguredLease (0.00s)
rc=1
w4-stop-does-not-wait:
--- FAIL: TestMain_BackgroundLoops_StopWaitsForLoops (0.00s)
rc=1
w5-reaper-dropped:
--- FAIL: TestMain_BackgroundLoops_ReaperRuns (10.02s)
rc=1
```

**Gates at the final commit** (round 2). Static:

```
== gofmt
rc=0
== vet
rc=0
== build
rc=0
```

`make test-docs` is re-run after this file is committed (see the end of this section). Unset run (`env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...`):

```
PASS 151 SKIP 35 FAIL 0
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/coordinator	1.491s
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/migrate	1.034s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/api	4.473s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/config	1.029s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.154s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/domain	1.143s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/executor	1.086s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/queue	1.026s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/scheduler	1.020s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage	1.445s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	1.097s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/worker	2.541s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/architecture	1.028s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	15.400s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	10.055s
ok  	github.com/BlaZe08-Dev/Minicloud/tests/review	1.067s
```

Counts before and after round 2, DB URL unset: round 1 PASS 146 SKIP 35 FAIL 0; round 2 PASS 151 SKIP 35 FAIL 0 (+5 passing tests in `cmd/coordinator`). The one SKIP is still the restart-grace test; it is now run by `make test-db`.

`make test-db` (new target; counts include subtests as before): round 1 target PASS 147 FAIL 0; round 2 target:

```
PASS 180 FAIL 0 SKIP 0
ok  	github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres	64.047s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	1.159s
--- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit (1.01s)
    --- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit/startup_grace_protects_the_surviving_worker (0.51s)
    --- PASS: TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit/control_without_grace_reaps_and_rejects_the_late_commit (0.49s)
ok  	github.com/BlaZe08-Dev/Minicloud/tests/integration	2.031s
```

(+33: the coordinator package tests with their subtests, and the three restart-grace lines.) Stability, DB URL unset, `go test -count=20 -race ./cmd/coordinator/ ./internal/coordinator/`:

```
ok  	github.com/BlaZe08-Dev/Minicloud/cmd/coordinator	12.141s
ok  	github.com/BlaZe08-Dev/Minicloud/internal/coordinator	3.838s
```

**Still open after round 2**: Q4 to Q9 above (docs, protocol and 4d/worker items) are unchanged; Q1 to Q3 are resolved as listed. Mutation of the `Makefile` target was not done (it is a script, not a guarded property); its behaviour is shown by the `make test-db` output above.

## Appendix: mutation proofs (driver summary and failing tests, from the real logs)

Raw per-mutation logs (mutated and restored, with the test command) are in the scratchpad `mut` folder of this session and are not committed. Line numbers in test failure messages are omitted here on purpose. The columns "mutated rc=1" and "restored rc=0" mean the test run failed with the mutation and passed once the file was restored with `git checkout`.

### Mutation summary (verbatim from the driver)

```
c07-trigger-never-consumed (first run, before HealthyTicksAfterRecovery test existed): SURVIVED mutated rc=0
c01-gap-gte: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c02-gap-factor-4: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c03-gap-factor-2: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c04-gap-detection-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c05-now-error-not-failed-tick: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c06-sweep-error-not-failed-tick: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c08-lock-skip-is-failed-tick: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c09-cooldown-ignored: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c10-cooldown-never-armed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c11-cooldown-half-length: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c12-cooldown-armed-at-open: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c13-window-extended-while-open: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c14-window-double-length: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c15-startup-grace-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c16-startup-grace-process-clock: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c17-startup-anchor-slides: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c18-backoff-nil: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c19-outage-window-not-passed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c20-skipped-changed-silent: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c21-loop-stops-on-failed-tick: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
c22-backoff-ignores-injected-random: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
a01-renew-route-missing: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/server.go]
a02-cancel-requested-always-false: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a03-stale-maps-to-404: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a04-worker-mismatch-maps-to-409: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a05-terminal-maps-to-400: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a06-lease-expired-code-swapped: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a07-job-not-found-maps-to-500: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a08-renew-worker-id-unvalidated: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a09-renew-attempt-id-unvalidated: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a10-renew-status-string: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/api/handlers.go]
a11-poll-lease-duration-zero: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/service.go]
a12-renew-service-swallows-error: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/service.go]
f01-startup-grace-check-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f02-outage-window-check-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f03-startup-equal-rejected: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f04-outage-equal-rejected: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f05-lease-positive-check-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f06-sweeper-positive-check-removed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f07-default-lease-20s: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
f08-default-sweeper-10s: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/config/config.go]
r01-mem-now-process-clock: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/storage/inmemory.go]
r02-mem-now-skewed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/storage/inmemory.go]
r03-pg-now-skewed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/storage/postgres/repository.go]
c07-trigger-never-consumed: mutated rc=1 (want 1), restored rc=0 (want 0)  [internal/coordinator/sweeper.go]
```

### Failing tests per mutation (verbatim `--- FAIL` lines of the mutated run; the restored run passed in every case)

- **a01-renew-route-missing** (`internal/api/server.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.01s); TestAPI_RenewLease_AcceptUntilSweptAndCancelDelivery (0.01s); TestAPI_RenewLease_RequestValidation (0.52s); TestAPI_RenewLease_OutageMapsTo503 (0.00s) — rc=1
- **a02-cancel-requested-always-false** (`internal/api/handlers.go`): TestAPI_RenewLease_AcceptUntilSweptAndCancelDelivery (0.01s) — rc=1
- **a03-stale-maps-to-404** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.02s) — rc=1
- **a04-worker-mismatch-maps-to-409** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.02s) — rc=1
- **a05-terminal-maps-to-400** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.03s) — rc=1
- **a06-lease-expired-code-swapped** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.03s) — rc=1
- **a07-job-not-found-maps-to-500** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.04s) — rc=1
- **a08-renew-worker-id-unvalidated** (`internal/api/handlers.go`): TestAPI_RenewLease_RequestValidation (0.68s) — rc=1
- **a09-renew-attempt-id-unvalidated** (`internal/api/handlers.go`): TestAPI_RenewLease_RequestValidation (0.69s) — rc=1
- **a10-renew-status-string** (`internal/api/handlers.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.01s); TestAPI_RenewLease_AcceptUntilSweptAndCancelDelivery (0.01s) — rc=1
- **a11-poll-lease-duration-zero** (`internal/coordinator/service.go`): TestAPI_PollAssignment_CarriesLeaseMetadata (0.01s) — rc=1
- **a12-renew-service-swallows-error** (`internal/coordinator/service.go`): TestAPI_RenewLease_SuccessAndStaleRejection (0.02s); TestAPI_RenewLease_OutageMapsTo503 (0.00s) — rc=1
- **c01-gap-gte** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapBelowThreshold_EntersNoGrace (0.00s) — rc=1
- **c02-gap-factor-4** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace (0.00s) — rc=1
- **c03-gap-factor-2** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapBelowThreshold_EntersNoGrace (0.00s) — rc=1
- **c04-gap-detection-removed** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace (0.00s) — rc=1
- **c05-now-error-not-failed-tick** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_FailedTick_EntersGrace (0.00s) — rc=1
- **c06-sweep-error-not-failed-tick** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_FailedTick_EntersGrace (0.00s); TestCoordinator_LeaseSweeper_PartialResultWithErrorIsFailedTick (0.00s); TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s) — rc=1
- **c07-trigger-never-consumed** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_HealthyTicksAfterRecoveryNeverReopenWindow (0.00s) — rc=1
- **c08-lock-skip-is-failed-tick** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CandidateLockTimeout_EntersNoGrace (0.00s) — rc=1
- **c09-cooldown-ignored** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s) — rc=1
- **c10-cooldown-never-armed** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c11-cooldown-half-length** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s) — rc=1
- **c12-cooldown-armed-at-open** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c13-window-extended-while-open** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt (0.00s) — rc=1
- **c14-window-double-length** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace (0.00s); TestCoordinator_LeaseSweeper_FailedTick_EntersGrace (0.00s); TestCoordinator_LeaseSweeper_PartialResultWithErrorIsFailedTick (0.00s); TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c15-startup-grace-removed** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s); TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping (0.00s); TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit (1.34s) — rc=1
- **c16-startup-grace-process-clock** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s); TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt (0.00s); TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c17-startup-anchor-slides** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_StartupGraceInstantComesFromRepositoryClock (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s); TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping (0.00s) — rc=1
- **c18-backoff-nil** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_PassesBackoffWithInjectedRandomness (0.00s); TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt (0.00s); TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c19-outage-window-not-passed** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace (0.00s); TestCoordinator_LeaseSweeper_FailedTick_EntersGrace (0.00s); TestCoordinator_LeaseSweeper_PartialResultWithErrorIsFailedTick (0.00s); TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace (0.00s); TestCoordinator_LeaseSweeper_FailureWhileWindowOpenDoesNotExtendIt (0.00s); TestCoordinator_LeaseSweeper_FailedFirstTickThenRecovery (0.00s); TestCoordinator_LeaseSweeper_OutageWindow_SuppressesReapingAfterRecovery (0.00s) — rc=1
- **c20-skipped-changed-silent** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_SkippedChangedLoggedAtWarn (0.00s) — rc=1
- **c21-loop-stops-on-failed-tick** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_RunSurvivesFailedTicksAndStopsOnCancel (5.00s) — rc=1
- **c22-backoff-ignores-injected-random** (`internal/coordinator/sweeper.go`): TestCoordinator_LeaseSweeper_PassesBackoffWithInjectedRandomness (0.00s) — rc=1
- **f01-startup-grace-check-removed** (`internal/config/config.go`): TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected (0.00s) — rc=1
- **f02-outage-window-check-removed** (`internal/config/config.go`): TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected (0.00s) — rc=1
- **f03-startup-equal-rejected** (`internal/config/config.go`): TestLoadCoordinatorConfig_Defaults (0.00s); TestLoadCoordinatorConfig_LeaseDefaults (0.00s); TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected (0.00s) — rc=1
- **f04-outage-equal-rejected** (`internal/config/config.go`): TestLoadCoordinatorConfig_Defaults (0.00s); TestLoadCoordinatorConfig_LeaseDefaults (0.00s); TestLoadCoordinatorConfig_LeaseSettingsFromEnv (0.00s); TestConfig_Validation_GracePeriodLessThanLeaseDuration_Rejected (0.00s) — rc=1
- **f05-lease-positive-check-removed** (`internal/config/config.go`): TestConfig_Validation_NonPositiveLeaseSettings_Rejected (0.00s) — rc=1
- **f06-sweeper-positive-check-removed** (`internal/config/config.go`): TestConfig_Validation_NonPositiveLeaseSettings_Rejected (0.00s) — rc=1
- **f07-default-lease-20s** (`internal/config/config.go`): TestLoadCoordinatorConfig_LeaseDefaults (0.00s) — rc=1
- **f08-default-sweeper-10s** (`internal/config/config.go`): TestLoadCoordinatorConfig_LeaseDefaults (0.00s) — rc=1
- **r01-mem-now-process-clock** (`internal/storage/inmemory.go`): TestInMemoryRepository_Now_UsesInjectedClock (0.00s) — rc=1
- **r02-mem-now-skewed** (`internal/storage/inmemory.go`): TestConformance_Now_IsRepositoryClock (0.00s); TestInMemoryRepository_Now_UsesInjectedClock (0.00s) — rc=1
- **r03-pg-now-skewed** (`internal/storage/postgres/repository.go`): TestConformance_Now_IsRepositoryClock (0.50s) — rc=1
