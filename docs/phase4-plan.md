# MiniCloud Phase 4 Implementation Plan: Leases, Bounded Retries, and Failure Recovery

## 1. Overview & Milestone Scope
Phase 4 elevates MiniCloud from a persistent durable store (Phase 3) to a self-healing, fault-tolerant distributed execution platform.

In Phase 3, coordinator restarts and storage outages were made resilient, but a dead or partitioned worker with in-flight attempts leaves jobs stranded in `RUNNING` indefinitely ([docs/failure-model.md](failure-model.md)). Phase 4 implements the distributed machinery to safely detect worker loss, bound execution attempts, recover through exponential backoff retries, reject late zombie results, and provide cooperative cancellation.

### Core Objectives & Accepted Parameters
1. **Durable Attempt Leases**: Leases on database time (`lease_expires_at`), periodic worker renewal, and background sweeper reclamation ([ADR 0014](adr/0014-lease-model-and-expiry.md)). Accepted parameters: 30s attempt lease (`MINICLOUD_LEASE_DURATION`), 10s renewal interval (1/3 lease duration), 5s sweeper interval (`MINICLOUD_SWEEPER_INTERVAL`), batch size 50, dedicated renewal endpoint (`POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew`).
2. **Dual Grace Mechanisms**: Coordinator startup grace period (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30s) and database outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30s), protecting surviving-worker commit guarantees pinned in `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`).
3. **Late-Commit Semantics**: Option 2 (accept-until-swept while attempt is `RUNNING` in database, even if database timestamp slightly exceeds `lease_expires_at`). Once swept to `LOST`, late commits are strictly rejected with HTTP 409 `LEASE_EXPIRED` or `STALE_ATTEMPT_REJECTED` and zero attempt resurrection.
4. **Sweeper Correctness Under Lock**: Sweeper executes canonical row-locked re-check (`SELECT id, state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE;` in lock order `jobs -> workers -> attempts`, with `jobs FOR UPDATE SKIP LOCKED` and `workers / attempts FOR UPDATE` under `lock_timeout`), skipping mutation if a renewal landed concurrently.
5. **Bounded Retries with Backoff**: Structured failure classification (`ClassPermanent`, `ClassTransient`, `ClassUnknown`, `ClassLeaseExpired`), exponential backoff with full jitter ([0.5, 1.5]), base 1s, cap 30s, default 3 max attempts, `jobs.not_before` scheduling, and direct `RETRYING -> RUNNING` dispatch ([ADR 0015](adr/0015-retry-policy-and-failure-classification.md)). Validation failures of allowlisted tasks are permanent; success is `AttemptCompleted` (not a failure class); `LOST` attempts consume 1 attempt.
6. **Single-Channel Cooperative Cancellation**: Renewal response (`cancel_requested: true`) is the sole cancellation delivery channel ([ADR 0016](adr/0016-cancellation-semantics.md)); removed `cancelled_job_ids` from heartbeat response. Worst-case latency is bounded to 10s (one renewal period). Duplicate cancel on `CANCEL_REQUESTED` becomes an idempotent no-op returning HTTP 200 with state `CANCEL_REQUESTED` (identical response body as the first cancel).
7. **Synchronized Deployment Rule**: V1 coordinator and workers deploy together; non-renewing legacy workers are unsupported for tasks longer than 30s.
8. **Hard Gate Foundation**: Serves as the direct foundation for post-Phase 4 Assignment-Based Round-Robin ([ADR 0011](adr/0011-postgres-dispatch-and-recovery.md)).

---

## 2. Proposed Sub-Slices

### Slice 4a: Grace Design, Existing-Test Impact, Domain State Machine & In-Memory Parity
- **Grace Design & Rationale**:
  - Leases expire strictly on database time (`lease_expires_at = clock_timestamp() + 30s`).
  - Without grace, any coordinator restart or database outage lasting > 30s causes the background sweeper to mass-expire healthy running attempts to `LOST`, scheduling redundant retry attempts and causing duplicate execution.
  - This directly violates the Phase 3 recovery invariant verified in `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`), where surviving workers holding valid tasks successfully commit after the control plane recovers.
  - *Mechanism*: The sweeper enforces two grace conditions before sweeping:
    1. *Startup Grace Period*: Sweeper pauses reclamation for `MINICLOUD_STARTUP_GRACE_PERIOD` (30s) after coordinator boot, allowing surviving workers to issue renewals.
    2. *Database Outage Grace Window*: The sweeper opens an outage grace window of MINICLOUD_OUTAGE_GRACE_WINDOW (30 s) at the first successful tick after either (a) any failed sweeper tick, or (b) a database-time gap over 3x the sweep interval between consecutive successful ticks. A lock timeout or SKIP LOCKED skip on a single candidate is not a failed tick. After a window ends, a cooldown of the same length (30 s) begins, during which no new window opens and reaping proceeds, so suppression of reaping never exceeds one window length. The only authoritative grace check is the one inside the sweeper transaction lock.
- **Existing-Test Impact List**:
  1. *Cancellation Duplicate-Call Parity & SEC-022 Authorized Fix Scope*:
     - *Current Behavior*: In `CancelJob` (`internal/storage/inmemory.go` func `CancelJob` and `internal/storage/postgres/repository.go` func `CancelJob`), terminal jobs return `ErrJobAlreadyTerminal`; `QUEUED` transitions to `CANCELLED`; `RUNNING` transitions to `CANCEL_REQUESTED` (attempt unmodified in `RUNNING`); duplicate cancel on `CANCEL_REQUESTED` is an idempotent no-op; and `RETRYING` transitions immediately to `CANCELLED`.
     - *Phase 4 Behavior Change (SEC-022 Authorized Fix)*:
       - (a) A duplicate cancel on a `CANCEL_REQUESTED` job becomes an idempotent no-op returning HTTP 200 with state `CANCEL_REQUESTED` (same response body as the first cancellation request), preventing the job from prematurely terminating while the worker is actively running and preventing subsequent result commits from being rejected as terminal.
       - (b) `CommitAttempt` on a terminal job must close the attempt (only if the attempt is still `RUNNING`) and free the worker capacity slot in the same transaction as the rejection, record a diagnostic event, and still return HTTP 409 `JOB_ALREADY_TERMINAL`.
       - (c) The regression test is the audit probe `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (audit probe, executed on `agent/claude/P4-00-review`; regression in slice 4a), to be cherry-picked from branch `agent/claude/P4-00-review` in slice 4a and moved into the normal test tree.
       - (d) Enforce Invariant 4 ("No Active Attempt After Terminal State") in `TestPostgresRepository_Concurrency_MixedOperationsStress` (`docs/fault-injection-plan.md:141`).
     - *Existing Test Inspection & Impact*:
       - The existing test `CancelJob_QueuedAndRunning` in `internal/storage/storagetest/conformance.go` (in `RunRepositoryConformanceTests`) asserts that calling `repo.CancelJob` a second time on a terminal `CANCELLED` job returns `domain.ErrJobAlreadyTerminal`.
       - The existing test `TestInMemoryRepository_SubmitJob_And_CancelJob_Events` in `internal/storage/inmemory_test.go` asserts that calling `repo.CancelJob` a second time on a terminal `CANCELLED` job returns `domain.ErrJobAlreadyTerminal`.
       - Slice 4a changes duplicate-cancel behavior on non-terminal `CANCEL_REQUESTED` jobs (the job remains in `CANCEL_REQUESTED` rather than prematurely jumping to `CANCELLED`), but changes zero HTTP status codes (HTTP 200 continues to be returned for all valid cancellation requests). No existing test in the test suite asserts a second-cancel response on `CANCEL_REQUESTED`.
       - *Impact*: Zero existing tests are broken by making duplicate cancel on `CANCEL_REQUESTED` an idempotent no-op returning HTTP 200.
       - *New Conformance Test*: `TestConformance_CancelJob_IdempotentCancelRequested` (added in slice 4a to `internal/storage/storagetest/conformance.go`).
  2. *FSM Transition Matrix Updates (`TestJobTransitionMatrix_AllPairs` in `internal/domain/transition_test.go`)*:
     - Exactly two matrix cells change validity (marked "candidate for owner approval"):
       - `RETRYING -> QUEUED`: was valid in initial matrix (`true`), becomes invalid (`false`) (candidate for owner approval; jobs in `RETRYING` do not transition back to `QUEUED`, they dispatch directly `RETRYING -> RUNNING` via `DispatchNextJob`).
       - `RETRYING -> RUNNING`: was invalid in initial matrix (`false`), becomes valid (`true`) (candidate for owner approval; direct dispatch).
     - Note: `CANCEL_REQUESTED -> CANCEL_REQUESTED` remains invalid in the domain FSM transition table (`internal/domain/transition.go` table `allowedTransitions`), so its cell does NOT flip. Duplicate cancel is handled in repository `CancelJob` under row lock as an idempotent no-op (returning the job in `CANCEL_REQUESTED` unchanged).
     - *Impact*: `TestJobTransitionMatrix_AllPairs` cell assertions for the two flipped cells are updated in Slice 4a.
  3. *HTTP API Cancel Status Code Parity (Unchanged from Today)*:
     - All cancellation requests for valid jobs (`QUEUED`, `RETRYING`, `RUNNING`, or duplicate cancels on `CANCEL_REQUESTED`) return HTTP 200 `OK`. Status codes are completely unchanged from today. There is no HTTP 202 `Accepted`, no ADR-0004 exception, and no `duplicate` response field.
     - Test: `TestAPI_CancelJob_SuccessReturns200` (executed in slice 4a).
  4. *In-Memory Retry Dispatch & Coordinator Rewiring (R-05, D-R2-01 & R3-09)*:
     - `RETRYING` jobs dispatch through `repo.DispatchNextJob` in both modes. The memory-mode coordinator (`PollWork` in `internal/coordinator/service.go` func `PollWork`) is rewired to call `repo.DispatchNextJob` in slice 4a, achieving full dispatch parity with PostgreSQL.
     - *Coordinator Queue Operations & Constructor Fate*:
       - When DB dispatch replaces queue operations in Slice 4a, queue operations (`s.queue.Enqueue`, `s.queue.Ack`) are completely removed from `SubmitJob`, `CommitResult`, and `CancelJob` in `internal/coordinator/service.go` with the memory-dispatch rewire.
       - `NewServiceWithQueue` in `internal/coordinator/service.go` and its queue parameter are retired (deleted per D3; `tests/integration/multi_worker_batch_test.go` func `TestThreeWorkerBatch_Integration` constructs via `NewService`).
       - Callers constructing memory coordinators without queue assertions remain unaffected: `internal/coordinator/service_test.go` (helper `setupTestCoordinator`), `internal/api/api_test.go` (helpers `setupTestServer` and `setupErrorServer`), `tests/integration/coordinator_restart_test.go`, and `tests/integration/worker_liveness_test.go`.
     - *Integration Tests using Memory Driver*:
       - `tests/integration/local_core_test.go` (`TestLocalCore_MultiProcessIntegration`) and `tests/integration/multi_worker_batch_test.go` (`TestThreeWorkerBatch_Integration`): use memory repository and coordinator. When `RETRYING` jobs exist, memory coordinator now properly claims them via `DispatchNextJob` without stranding them.
       - `internal/api/api_test.go` (helper `setupTestServer`): uses memory coordinator. `ClaimWork` routes through `DispatchNextJob`, so dispatch behavior and attempt tracking match postgres repository.
     - *Existing-Test Impact on Coordinator Service Tests*:
       - Moving memory dispatch to `repo.DispatchNextJob` directly impacts coordinator tests in `internal/coordinator/service_test.go`:
         - The historical `spyQueue` helper was eliminated, and seven tests were rewritten to assert observable domain outcomes (job state, attempt state, worker slot capacity) instead of queue method calls:
           1. `TestService_Claim_JobNoLongerQueued_Dropped` in `internal/coordinator/service_test.go`
           2. `TestService_Claim_JobNotQueuedAndWorkerUnhealthy_DroppedAndAckedNeverReleased` in `internal/coordinator/service_test.go`
           3. `TestService_Claim_Redelivery_NoDuplicateAttempt` in `internal/coordinator/service_test.go`
           4. `TestService_CommitResult_AckOnlyOnAccepted` in `internal/coordinator/service_test.go`
           5. `TestService_ClaimOutcome_TransientFailure_QueueReleased` in `internal/coordinator/service_test.go`
           6. `TestService_ClaimOutcome_JobNotFoundOrNotQueued_AckedNeverReleased` in `internal/coordinator/service_test.go`
           7. `TestService_ClaimOutcome_WorkerLacksCapacityOrUnhealthy_Released` in `internal/coordinator/service_test.go`
  5. *Restart & Outage Recovery Tests*:
     - `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`) assert that surviving workers can commit results after restarts and outages.
     - With the 30s startup and outage grace windows, surviving workers commit within the grace window, keeping test assertions valid without weakening any existing test assertion.
  6. *RETRYING Cancellation Conformance*:
     - When a job is in `RETRYING`, cancel immediately transitions the job to `CANCELLED` (terminal), clearing `not_before` and recording a `JOB_CANCELLED` event.
     - Test: `TestConformance_CancelJob_RetryingImmediatelyCancelled` (executed in slice 4a).
- **Scope**:
  - Reconcile attempt state naming: canonical states are `'PENDING'`, `'RUNNING'`, `'COMPLETED'`, `'FAILED'`, `'TIMED_OUT'`, `'CANCELLED'`, `'LOST'`, matching migration `000001_initial_schema.sql` CHECK constraint `chk_attempts_state`. Map/rename domain constants `AttemptState` in `internal/domain/state.go` (`EXPIRED` -> `LOST`, `CANCELED` -> `CANCELLED`).
  - Update `internal/domain`:
    - Add `AttemptOutcome.ErrorClassification` (`ClassPermanent`, `ClassTransient`, `ClassUnknown`, `ClassLeaseExpired`). Exit code 0 is `AttemptCompleted` without error class. Validation failures of allowlisted tasks are `ClassPermanent`.
    - Add `Attempt.LeaseExpiresAt`, `Job.NotBefore`, `Job.CancelRequestedAt`.
    - Validate transitions centrally in FSM with typed errors: `RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING` via direct dispatch.
  - Update `internal/storage.InMemoryRepository`:
    - (Moved out of Slice 4a by owner decision: in-memory `RenewLease` and `SweepExpiredLeases`, with the in-lock grace check, move to Slice 4b; the sweeper loop and its grace/cooldown window state move to Slice 4c.)
    - Evaluate `not_before <= clock.Now()` in `DispatchJob`, `DispatchNextJob`, and `ListQueuedJobs`.
    - Handle idempotent duplicate cancel on `CANCEL_REQUESTED`.
    - Rewire in-memory coordinator to call `repo.DispatchNextJob`.
  - Expand shared conformance suite in `internal/storage/storagetest/conformance.go` (`TestConformance_CancelJob_IdempotentCancelRequested`, `TestConformance_CancelJob_RetryingImmediatelyCancelled`, `TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt`).
  - Property test: `TestProperty_StateMachine_ModelConformance` driving reference model and repository with injectable clock.
  - Audit probe allowlist cleanup: `TestReviewP400_SEC022` stays allowlisted permanently (it is the `-run` pattern string inside verbatim SEC-022 evidence). Slice 4a removes only the entries that go stale when the probe moves into the normal test tree (the path entry and the two real test names), decided by the checker's stale-entry detection.
- **Exit Criteria**:
  - Domain FSM centrally validates all transitions with typed domain errors.
  - In-memory repository passes expanded shared conformance suite.
  - Model-based tests pass cleanly under race detector (`-race -count=1`).
  - **Mutation Proofs (Mandatory)**:
    - Mutating duplicate cancel logic to return error or transition to `CANCELLED` must cause `TestConformance_CancelJob_IdempotentCancelRequested` to fail.
    - Mutating FSM transition table to reject `RETRYING -> RUNNING` must cause dispatch tests to fail.
    - Mutating in-memory dispatch to ignore `not_before` must cause retry backoff tests to fail.
- **Gating Tests**:
  - `TestJobTransitionMatrix_AllPairs` (executed; updated with 2 approved cell changes)
  - `TestConformance_CancelJob_IdempotentCancelRequested` (executed, memory and PostgreSQL)
  - `TestConformance_CancelJob_RetryingImmediatelyCancelled` (executed, memory and PostgreSQL)
  - `TestConformance_CommitAttempt_NonRunningAttempt_ReturnsStaleAttempt` (executed, memory and PostgreSQL; owner item R6-01)
  - `TestAPI_CancelJob_SuccessReturns200` (executed, memory-backed server)
  - `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (regression test, executed on memory and PostgreSQL)
  - `TestInMemoryRepository_RetryBackoff` (executed; covers `not_before` dispatch eligibility gating only; no backoff computation per R-4a-01)
  - `TestProperty_StateMachine_ModelConformance` (executed, memory and PostgreSQL)
- **Risks & Mitigations**:
  - Clock advancement complexity in unit tests. *Mitigation:* Inject fake clock (`storage.WithClock`, `coordinator.SetClock`).

---

### Slice 4b: Schema Migration 000004, Lease Renewal and Sweep (In-Memory and PostgreSQL) & Pure Backoff Function
- **Scope**:
  - Author embedded migration `000004_leases_retries_cancellation.sql` (proposed):
    - Columns: `attempts.lease_expires_at` (with temporary default for historical row backfill, dropped immediately after), `attempts.error_class` (strictly checked for `'PERMANENT'`, `'TRANSIENT'`, `'UNKNOWN'`, `'LEASE_EXPIRED'`), `attempts.last_error`, `jobs.not_before`, `jobs.cancel_requested_at`.
    - Indexes: `idx_attempts_lease_expiry`, `idx_jobs_retrying_dispatch`, and partial unique index `idx_job_events_unique_diagnostic_late_result` on `(job_id, (details->>'attempt_id')) WHERE event_type = 'DIAGNOSTIC_LATE_RESULT'`.
    - Check constraints: `chk_attempts_error_class`, `chk_attempts_last_error_size` using `NOT VALID` then `VALIDATE CONSTRAINT`.
  - Implement durable repository methods in `internal/storage/postgres.Repository`:
    - Add `RenewLease` and `SweepExpiredLeases` to the `storage.Repository` interface and implement both in `internal/storage.InMemoryRepository` (evaluating time on the injected repository clock) and in `internal/storage/postgres.Repository` (database time), with identical semantics.
    - `RenewLease`: atomic transaction verifying attempt ownership, active `RUNNING` attempt (symmetric accept-until-swept rule), and constant-time token comparison.
    - `SweepExpiredLeases`: single-transaction lock-ordered candidate sweep (`jobs -> workers -> attempts -> results -> job_events`). The method receives the current grace state (startup-grace deadline, open outage window, cooldown end) as an input value from its caller and performs the only authoritative grace check inside the row locks; it neither owns nor advances that state.
    - Add a pure job-retry backoff function in `internal/domain` (ADR-0015 parameters: base 1 s, cap 30 s, jitter factor in [0.5, 1.5], default max_attempts 3, randomness injected so tests are deterministic) with planned test `TestDomain_ComputeBackoff_FullJitter` (planned test); Slice 4d reuses it unchanged.
    - The sweep decides a lost attempt from `attempt_no` versus `max_attempts` with class `LEASE_EXPIRED`, without slice 4d's classification.
    - Shared conformance tests for `RenewLease` and `SweepExpiredLeases` in `internal/storage/storagetest/conformance.go`, run against both repositories.
    - *Canonical Sweeper Transaction (R-08)*:
      1. Candidate scan (unlocked read against index `idx_attempts_lease_expiry`).
      2. Per-candidate lock order `jobs -> workers -> attempts`: `SELECT id, state, max_attempts FROM jobs WHERE id = $1 FOR UPDATE SKIP LOCKED` (0 rows = skip and retry next tick), then workers and attempts rows with plain `FOR UPDATE` under PostgreSQL `lock_timeout` (default 5s). One grace check inside locks. `SKIP LOCKED` and `NOWAIT` are never combined.
      3. Sweeper state resolution rules (R-02):
         - If parent job is already terminal (`SUCCEEDED`, `FAILED`, `CANCELLED`): close attempt as `CANCELLED` (if job is `CANCELLED`) or `LOST` (otherwise), leave job state unchanged, free worker slot, and record `LEASE_EXPIRED` event.
         - If `CANCEL_REQUESTED`: close attempt as `CANCELLED`, transition job to `CANCELLED`, free worker slot, record `JOB_CANCELLED` event.
         - If `RUNNING` and `attempt_no < max_attempts`: close attempt as `LOST` (`error_class = 'LEASE_EXPIRED'`), set exponential backoff `not_before`, transition job to `RETRYING`, free worker slot, record `LEASE_EXPIRED` and `JOB_RETRYING` events.
         - If `RUNNING` and `attempt_no >= max_attempts`: close attempt as `LOST` (`error_class = 'LEASE_EXPIRED'`), transition job to `FAILED`, free worker slot, record `LEASE_EXPIRED` and `JOB_FAILED` events.
    - Update `DispatchNextJob`: query evaluates `WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))` with direct transition to `RUNNING`.
    - Update `CommitAttempt`: enforce 409 evaluation order:
      1. Terminal job -> close attempt (strictly if still in `RUNNING`), free worker slot, record diagnostic event, and return 409 `JOB_ALREADY_TERMINAL` (the single stored result stands; an attempt already terminal is never overwritten).
      2. Attempt swept to `LOST` -> return 409 `LEASE_EXPIRED` (`domain.ErrLeaseExpired`).
      3. Superseded attempt, token mismatch, or attempt not `RUNNING` (`domain.ErrStaleAttempt`) -> return 409 `STALE_ATTEMPT_REJECTED` (already returned by `CommitAttempt` in `internal/storage/inmemory.go` and `internal/storage/postgres/repository.go`, and mapped by `mapDomainErrorToHTTP` in `internal/api/handlers.go`).
    - `CommitAttempt` on `CANCEL_REQUESTED` (built in Slice 4d): success moves attempt `COMPLETED` and job `SUCCEEDED` (`CommitAttempt` in `internal/storage/inmemory.go` func `CommitAttempt`, `internal/storage/postgres/repository.go` func `CommitAttempt`, `internal/domain/transition.go` table `allowedTransitions`); failure moves attempt `FAILED` and job `CANCELLED` (Behavior change from today, D-R2-03, ADR-0016 section 1 item 4).
    - Update `CancelJob`: idempotent no-op on `CANCEL_REQUESTED`, immediate `CANCELLED` on `RETRYING`.
  - Run shared conformance test suite against live PostgreSQL container.
- **Exit Criteria**:
  - Forward-only migration 000004 applies cleanly to fresh and migrated databases.
  - PostgreSQL repository passes all shared conformance tests under `-race`.
  - Zero deadlocks under concurrent mixed operations stress test (`-race -count=20`), with Invariant 4 (No Active Attempt After Terminal State, [fault-injection-plan.md](fault-injection-plan.md#L141)) strictly verified.
  - **Mutation Proofs (Mandatory)**:
    - Bypassing row lock or re-check in `SweepExpiredLeases` must fail `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test).
    - Removing the in-lock grace check in `SweepExpiredLeases` (driven by the grace-state input) must fail `TestConformance_SweepExpiredLeases_GraceInputSuppressesReaping` (planned test).
    - Mutating the jitter factor outside [0.5, 1.5] or removing the 30 s cap in the pure backoff function must fail `TestDomain_ComputeBackoff_FullJitter` (planned test).
    - Mutating terminal job attempt closing in `CommitAttempt` must cause `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` (planned test) to fail.
- **Gating Tests**:
  - `TestMigrator_FreshMigrate`
  - `TestPostgresRepository_Conformance` (expanded with Phase 4 tests)
  - `TestPostgresRepository_Sweeper_CanonicalLockOrder` (planned test)
  - `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test)
  - `TestPostgresRepository_CommitAttempt_TerminalJobClosesAttempt` (planned test)
  - `TestPostgresRepository_Concurrency_MixedOperationsStress` (expanded with leases, cancels, and Invariant 4 check)
  - `TestDomain_ComputeBackoff_FullJitter` (planned test)
  - `TestInMemoryRepository_LeaseRenewalAndExpiry` (planned test)
  - `TestConformance_RenewLease_AcceptUntilSwept` (planned test)
  - `TestConformance_RenewLease_RejectedAfterLost` (planned test)
  - `TestConformance_SweepExpiredLeases_RetryOrFail` (planned test)
  - `TestConformance_SweepExpiredLeases_GraceInputSuppressesReaping` (planned test)
- **Risks & Mitigations**:
  - Sweeper lock contention with active workers. *Mitigation:* Small batches (LIMIT 50) and strict lock order `jobs -> workers -> attempts`.

---

### Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring
- **Scope**:
  - HTTP API (`internal/api`):
    - Dedicated `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` endpoint.
    - Return `{"status": "RENEWED", "lease_expires_at": "...", "cancel_requested": false}` on active job.
    - Single cancellation channel: renewal response returns `{"status": "RENEWED", "cancel_requested": true, "lease_expires_at": "<current_expiry>"}` when job is in `CANCEL_REQUESTED`, without extending `lease_expires_at`.
    - **Heartbeat Cleanup**: Remove `cancelled_job_ids` from worker heartbeat response. Heartbeats remain purely for worker liveness (`{"status": "ACK"}`).
    - Expand `POST /v1/workers/{id}/poll` assignment response with `lease_expires_at` and `lease_duration_seconds`.
    - Rate Limiting Defense Documentation: renewal flooding is formally recorded as designed, not yet implemented (R-23); planned endpoint defenses are fast state short-circuit, indexed lookup on primary key, and statement timeout; accepted risk under SEC-004.
  - Worker Agent (`internal/worker`):
    - Dedicated background renewal goroutine per running attempt ticking every 10s (1/3 lease duration; `MINICLOUD_LEASE_DURATION / 3`).
    - Renewal response error handling (R-10):
      - 200 with `cancel_requested: true`: worker cancels task context, ceases renewals, and still reports its result. If task ignores cancellation, lease expires within 30s and sweeper reaps attempt as `CANCELLED` and job to `CANCELLED` (never `RETRYING`).
      - 403 `WORKER_MISMATCH` or 409 (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`): terminal rejection. Immediately abort task, cancel context, and do not report result.
      - 404: inspect body. If recognized not-found code (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`), terminal rejection: abort task and do not report result. If missing or unrecognized error envelope (old coordinator), log warning once, disable renewals for this attempt, keep running task to completion, and report result via `POST /result`.
      - 400 (`INVALID_REQUEST`, `WORKER_TASK_UNSUPPORTED`): log error, stop renewing, keep running task to completion, report result via `POST /result`.
      - 5xx / transport error: transient failure. Keep running task, retry renewal next interval until lease expiry.
    - Worker result delivery (R-11): 60-second bounded retry loop with exponential backoff on transport errors and ongoing concurrent lease renewals; immediate exit on 4xx terminal rejections (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `WORKER_MISMATCH`, deterministic 400 `INVALID_REQUEST`, 404).
  - Coordinator Service (`internal/coordinator`):
    - Component owns: `StartLeaseSweeper(ctx, interval)` cadence (5s, `MINICLOUD_SWEEPER_INTERVAL`), failed-tick tracking, the outage grace window and cooldown state (R-09), and the startup grace period, and passes that state to the 4b `SweepExpiredLeases` on every tick.
    - Outage detection (R-09): The sweeper opens an outage grace window of MINICLOUD_OUTAGE_GRACE_WINDOW (30 s) at the first successful tick after either (a) any failed sweeper tick, or (b) a database-time gap over 3x the sweep interval between consecutive successful ticks. A lock timeout or SKIP LOCKED skip on a single candidate is not a failed tick. After a window ends, a cooldown of the same length (30 s) begins, during which no new window opens and reaping proceeds, so suppression of reaping never exceeds one window length. The only authoritative grace check is the one inside the sweeper transaction lock.
- **Exit Criteria**:
  - Long-running jobs (> 30s) maintain continuous lease renewals and commit successfully.
  - Cancellation delivered exclusively via renewal response within 10s.
  - Result finished during a 45s coordinator/database outage commits within 60s delivery bound once coordinator/database recovers.
  - Worker receiving lease loss aborts task and suppresses result reporting.
  - Terminating a worker process triggers automated lease expiry and job reclamation.
  - Worker renewal handles missing endpoints (old coordinator) and 400 errors without failing the executing task.
  - **Mutation Proofs (Mandatory)**:
    - Mutating worker renewal response handler to ignore `cancel_requested: true` must cause cooperative cancellation test to fail.
    - Disabling worker abort upon renewal rejection must cause worker zombie isolation test to fail.
    - Mutating outage gap threshold or cooldown must cause outage grace recovery tests to fail.
    - Removing the startup grace period from the sweeper loop must cause `TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping` (planned test) and `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test) to fail.
- **Gating Tests**:
  - `TestAPI_RenewLease_SuccessAndStaleRejection` (planned test)
  - `TestAgent_PeriodicLeaseRenewal_Success` (planned test)
  - `TestAgent_LeaseRenewal_ReceivesCancelRequested` (planned test)
  - `TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt` (planned test)
  - `TestCoordinator_LeaseSweeper_StartupGrace_SuppressesReaping` (planned test)
  - `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test)
  - `TestCoordinator_LeaseSweeper_InterTickGapExceedsThreshold_EntersGrace` (planned test)
  - `TestCoordinator_LeaseSweeper_InterTickGapBelowThreshold_EntersNoGrace` (planned test)
  - `TestCoordinator_LeaseSweeper_FailedTick_EntersGrace` (planned test)
  - `TestCoordinator_LeaseSweeper_CandidateLockTimeout_EntersNoGrace` (planned test)
  - `TestCoordinator_LeaseSweeper_CooldownPreventsConsecutiveGrace` (planned test)
  - `TestWorker_Client_RenewOldCoordinatorGracefulDegradation` (planned test)
  - `TestWorker_Client_Renew400KeepsRunning` (planned test)
  - `TestWorker_Client_ResultDeliveryLoop_BoundedRetry` (planned test)
  - `TestWorker_ExecuteAndReport_RetryDuringCoordinatorDowntime_Success` (planned test)
  - `TestWorker_ExecuteAndReport_OngoingRenewalWhileRetryingReport` (planned test)
  - `TestWorker_ExecuteAndReport_BoundedTimeout_GivesUpAfterLeaseAndGrace` (planned test)
  - `TestWorker_ExecuteAndReport_TerminalRejection_HaltsImmediately` (planned test)
- **Risks & Mitigations**:
  - Renewal goroutine leaks. *Mitigation:* Goroutine terminates unconditionally on attempt context cancellation.

---

### Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation
- **Scope**:
  - Implement failure classification:
    - Success (exit code 0) is `AttemptCompleted` without error classification.
    - Validation failures of allowlisted tasks (`echo`, `sleep`, `sha256_loop`) are `ClassPermanent`.
    - Worker classifies abnormal terminations (OOM, SIGKILL, context deadline).
    - Allowed `error_class` values: `PERMANENT`, `TRANSIENT`, `UNKNOWN` (defaults to `UNKNOWN`).
    - `LOST` attempts consume 1 attempt of `max_attempts` budget (`LEASE_EXPIRED`).
  - Reuse the pure backoff function built in slice 4b (`internal/domain`) to calculate `jobs.not_before` for classified retries; no second implementation.
  - Cooperative cancellation:
    - Worker agent attaches `context.WithCancel` to executor context.
    - Preempted executor returns `context.Canceled`.
    - Any commit on `CANCEL_REQUESTED` closes attempt with reported outcome (Behavior change from today, D-R2-03, ADR-0016 section 1 item 4; Slice 4d builds this cooperative cancellation outcome in `CommitAttempt`): success moves `CANCEL_REQUESTED -> SUCCEEDED` per today's code (`CommitAttempt` in `internal/storage/inmemory.go` func `CommitAttempt` and `internal/storage/postgres/repository.go` func `CommitAttempt`, permitted by `internal/domain/transition.go` table `allowedTransitions`); failure moves attempt `FAILED` and `CANCEL_REQUESTED -> CANCELLED` (behavior change from today: today `internal/storage/inmemory.go` func `CommitAttempt` and `internal/storage/postgres/repository.go` func `CommitAttempt` move the job to `FAILED`; in Phase 4 cancel wins, never `RETRYING` or `FAILED`). No existing test in the test suite asserts the old outcome (`StateCancelRequested` is tested in `internal/domain/transition_test.go` and `internal/storage/storagetest/conformance.go`). The coordinator requires no special cancel acknowledgement wire signal.
  - Resolve cancellation races (cancel vs completion, cancel vs lease expiry, duplicate cancels).
- **Exit Criteria**:
  - Transient failures retry up to `max_attempts` with verified backoff delay.
  - Permanent failures transition immediately to `FAILED` without retrying.
  - Cancelled jobs preempt running workers cooperatively within one renewal interval (10s).
  - Zero jobs stranded in `CANCEL_REQUESTED`.
  - **Mutation Proofs (Mandatory)**:
    - Mutating classification of allowlisted task validation error to `ClassTransient` must cause failure classification test to fail.
    - Mutating the retry path to bypass the 4b backoff function must cause `TestCoordinator_RetryBackoffWithJitter` (planned test) to fail.
- **Gating Tests**:
  - `TestCoordinator_RetryBackoffWithJitter` (planned test)
  - `TestWorker_CooperativeCancellation_ContextPreemption` (planned test)
  - `TestConformance_CommitAttempt_FailureOnCancelRequestedYieldsCancelledJob` (planned test)
  - `TestAPI_CancelJob_RunningRaces` (planned test)
  - `TestExecutor_AllowlistValidation_PermanentFailure` (planned test)
- **Risks & Mitigations**:
  - Tasks that ignore context cancellation. *Mitigation:* Predefined tasks explicitly inspect `ctx.Done()` on every iteration.

---

### Slice 4e: End-to-End Fault-Injection Suite, Adversarial Testing & Phase 4 Security Review
- **Scope**:
  - Author and execute complete integration fault suite in `tests/integration/` and `tests/fault/`:
    - `TestFault_WorkerCrash_BeforeAccept` (planned test)
    - `TestFault_WorkerCrash_DuringExecution` (planned test)
    - `TestFault_ResultReport_NetworkDropOrDelay` (planned test)
    - `TestFault_OldWorker_LateCommitAfterReassignment` (planned test)
    - `TestFault_CoordinatorCrash_AfterSubmit` (planned test)
    - `TestFault_CoordinatorCrash_MidResultCommit` (planned test)
    - `TestFault_PostgresOutage_DuringLeaseExecution` (planned test)
    - `TestFault_Heartbeat_DelayedOrDuplicated` (planned test)
    - `TestFault_Task_TimeoutAndPoisonRepeat` (planned test)
    - `TestFault_Cancellation_RaceWithCompletion` (planned test)
  - Execute property-based state machine test harness (`TestProperty_StateMachine_ModelConformance` (executed in slice 4a)) for >= 1,000 iterations.
  - Perform formal security review per [ADR 0012](adr/0012-security-and-adversarial-testing-workflow.md):
    - Update `docs/security/findings.md` and `docs/security/threat-model.md`.
    - Validate that late results never overwrite newer results.
- **Exit Criteria**:
  - All 10 fault scenarios pass with concrete logs and autonomous recovery verified.
  - Full test suite passes `-race -count=1` with zero data corruption or unhandled errors.
  - Phase 4 security gate certified before release tagging.
  - **Mutation Proofs (Mandatory)**:
    - Bypassing stale attempt rejection in repository must cause late commit fault test to fail.
    - Mutating sweeper lock order to `attempts -> workers -> jobs` must cause deadlock in stress test.
- **Gating Tests**:
  - Complete planned fault suite (all 10 fault scenarios).
- **Risks & Mitigations**:
  - Timing sensitivity in multi-process integration tests. *Mitigation:* Use explicit barrier synchronization and process control signals (`SIGSTOP`/`SIGCONT`) rather than wall-clock sleeps.

---

## 3. Interaction with Post-Phase 4 Hard Gate (Assignment-Based Round-Robin)
- **Hard Gate Rule**: As established in [ADR 0011](adr/0011-postgres-dispatch-and-recovery.md) Section 2 and [docs/roadmap.md](roadmap.md), V1 is not complete until scheduler-driven assignment with a deterministic round-robin policy exists (Candidate A).
- **Phase 4 Machinery Reused**:
  - The assignment slice introduces `assigned_worker_id` and `assignment_expires_at` on queued jobs.
  - **Shared Lease Sweeper**: The Phase 4 lease sweeper machinery (`StartLeaseSweeper`) directly powers assignment expiration: if an assigned worker fails to accept a job within its bounded accept window (e.g. 5 seconds), the sweeper revokes the assignment (`assigned_worker_id = NULL`) and re-evaluates the round-robin scheduler.
  - Building assignment after Phase 4 ensures zero throwaway or duplicate recovery logic.
