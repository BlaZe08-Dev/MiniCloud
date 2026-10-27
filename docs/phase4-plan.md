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
    2. *Database Outage Grace Window*: If repository health checks or inter-tick database gaps detect database unavailability, the sweeper grants an outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s) upon recovery before sweeping expired leases.
- **Existing-Test Impact List**:
  1. *Cancellation Duplicate-Call Parity & SEC-022 Authorized Fix Scope*:
     - *Current Behavior*: Today, `internal/storage/inmemory.go:658-683` and `internal/storage/postgres/repository.go:1135-1215` behave identically: terminal job -> `ErrJobAlreadyTerminal`; `QUEUED` -> `CANCELLED`; `RUNNING` -> `CANCEL_REQUESTED` (attempt unmodified in `RUNNING`); and any subsequent non-terminal state (including `CANCEL_REQUESTED` and `RETRYING`) transitions to `CANCELLED` via fallback `else` branch.
     - *Phase 4 Behavior Change (SEC-022 Authorized Fix)*:
       - (a) A duplicate cancel on a `CANCEL_REQUESTED` job becomes an idempotent no-op returning HTTP 200 with state `CANCEL_REQUESTED` (same response body as the first cancellation request), preventing the job from prematurely terminating while the worker is actively running and preventing subsequent result commits from being rejected as terminal.
       - (b) `CommitAttempt` on a terminal job must close the attempt and free the worker capacity slot in the same transaction as the rejection, record a diagnostic event, and still return HTTP 409 `JOB_ALREADY_TERMINAL`.
       - (c) The regression test is the audit probe `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (planned test), to be cherry-picked from branch `agent/claude/P4-00-review` in slice 4a and moved into the normal test tree.
       - (d) Enforce Invariant 4 (`worker_capacity` accounting) in `TestPostgresRepository_Concurrency_MixedOperationsStress` (`docs/fault-injection-plan.md:141`).
     - *Existing Test Inspection & Impact*:
       - The existing test `CancelJob_QueuedAndRunning` in `internal/storage/storagetest/conformance.go:299-363` (specifically line 335) asserts that calling `repo.CancelJob` a second time on a terminal `CANCELLED` job returns `domain.ErrJobAlreadyTerminal`.
       - The existing test `TestInMemoryRepository_SubmitJob_And_CancelJob_Events` in `internal/storage/inmemory_test.go:559` (specifically line 607) asserts that calling `repo.CancelJob` a second time on a terminal `CANCELLED` job returns `domain.ErrJobAlreadyTerminal`.
       - Slice 4a changes duplicate-cancel behavior on non-terminal `CANCEL_REQUESTED` jobs (the job remains in `CANCEL_REQUESTED` rather than prematurely jumping to `CANCELLED`), but changes zero HTTP status codes (HTTP 200 continues to be returned for all valid cancellation requests). No existing test in the test suite asserts a second-cancel response on `CANCEL_REQUESTED`.
       - *Impact*: Zero existing tests are broken by making duplicate cancel on `CANCEL_REQUESTED` an idempotent no-op returning HTTP 200.
       - *New Conformance Test*: `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) will be added to `storagetest/conformance.go`.
  2. *FSM Transition Matrix Updates (`TestJobTransitionMatrix_AllPairs` in `internal/domain/transition_test.go`)*:
     - Three matrix cells change validity (marked "candidate for owner approval"):
       - `RETRYING -> QUEUED`: was valid in initial matrix, becomes invalid (candidate for owner approval; jobs in `RETRYING` do not transition back to `QUEUED`, they dispatch directly `RETRYING -> RUNNING` via `DispatchNextJob`).
       - `RETRYING -> RUNNING`: was invalid in initial matrix, becomes valid (candidate for owner approval; direct dispatch).
       - `CANCEL_REQUESTED -> CANCEL_REQUESTED`: self-transition remains invalid in the domain FSM transition table (diagonal is "-" across all states in domain FSM), but handled at the service/handler layer as an idempotent HTTP 200 no-op (candidate for owner approval).
     - *Impact*: `TestJobTransitionMatrix_AllPairs` cell assertions are updated in Slice 4a.
  3. *HTTP API Cancel Status Code Parity (Unchanged from Today)*:
     - All cancellation requests for valid jobs (`QUEUED`, `RETRYING`, `RUNNING`, or duplicate cancels on `CANCEL_REQUESTED`) return HTTP 200 `OK`. Status codes are completely unchanged from today. There is no HTTP 202 `Accepted`, no ADR-0004 exception, and no `duplicate` response field.
     - Planned test: `TestAPI_CancelJob_SuccessReturns200` (planned test).
  4. *In-Memory Retry Dispatch & Coordinator Rewiring (R-05)*:
     - `RETRYING` jobs dispatch through `repo.DispatchNextJob` in both modes. The memory-mode coordinator (`internal/coordinator/service.go:401-409`) is rewired to call `repo.DispatchNextJob` in slice 4a, achieving full dispatch parity with PostgreSQL.
     - *Integration Tests using Memory Driver*:
       - `tests/integration/local_core_test.go:25` and `tests/integration/multi_worker_batch_test.go:26`: use memory repository and coordinator. When `RETRYING` jobs exist, memory coordinator now properly claims them via `DispatchNextJob` without stranding them.
       - `internal/api/api_test.go:21` and `internal/coordinator/service_test.go` (e.g. `TestService_Claim_Redelivery_NoDuplicateAttempt`): use memory coordinator. `ClaimWork` routes through `DispatchNextJob`, so dispatch behavior and attempt tracking match postgres repository.
       - *Impact on each*: Existing tests continue to pass because `DispatchNextJob` claims `QUEUED` jobs identically and dispatches `RETRYING` jobs whose `not_before` has arrived.
  5. *Restart & Outage Recovery Tests*:
     - `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`) assert that surviving workers can commit results after restarts and outages.
     - With the 30s startup and outage grace windows, surviving workers commit within the grace window, keeping test assertions valid without weakening any existing test assertion.
  6. *RETRYING Cancellation Conformance*:
     - When a job is in `RETRYING`, cancel immediately transitions the job to `CANCELLED` (terminal), clearing `not_before` and recording a `job.cancelled` event.
     - Planned test: `TestConformance_CancelJob_RetryingImmediatelyCancelled` (planned test).
- **Scope**:
  - Reconcile attempt state naming: canonical states are `'PENDING'`, `'RUNNING'`, `'COMPLETED'`, `'FAILED'`, `'TIMED_OUT'`, `'CANCELLED'`, `'LOST'`, matching migration `000001_initial_schema.sql:97`. Map/rename domain constants `AttemptState` in `internal/domain/state.go:22-27` (`EXPIRED` -> `LOST`, `CANCELED` -> `CANCELLED`).
  - Update `internal/domain`:
    - Add `AttemptOutcome.ErrorClassification` (`ClassPermanent`, `ClassTransient`, `ClassUnknown`, `ClassLeaseExpired`). Exit code 0 is `AttemptCompleted` without error class. Validation failures of allowlisted tasks are `ClassPermanent`.
    - Add `Attempt.LeaseExpiresAt`, `Job.NotBefore`, `Job.CancelRequestedAt`.
    - Validate transitions centrally in FSM with typed errors: `RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING` via direct dispatch.
  - Update `internal/storage.InMemoryRepository`:
    - Implement `RenewLease`, `SweepExpiredLeases`, and dual grace timers.
    - Evaluate `not_before <= clock.Now()` in `DispatchJob`, `DispatchNextJob`, and `ListQueuedJobs`.
    - Handle idempotent duplicate cancel on `CANCEL_REQUESTED`.
    - Rewire in-memory coordinator to call `repo.DispatchNextJob`.
  - Expand shared conformance suite in `internal/storage/storagetest/conformance.go`.
  - Property test: `TestProperty_StateMachine_ModelConformance` (planned test) driving reference model and repository with injectable clock.
  - Remove the three probe allowlist entries in `tests/docs/citation_allowlist.txt` when moving the audit probe into the normal test tree.
- **Exit Criteria**:
  - Domain FSM centrally validates all transitions with typed domain errors.
  - In-memory repository passes expanded shared conformance suite.
  - Model-based tests pass cleanly under race detector (`-race -count=1`).
  - **Mutation Proofs (Mandatory)**:
    - Mutating duplicate cancel logic to return error or transition to `CANCELLED` must cause `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) to fail.
    - Mutating FSM transition table to reject `RETRYING -> RUNNING` must cause dispatch tests to fail.
    - Mutating in-memory dispatch to ignore `not_before` must cause retry backoff tests to fail.
- **Gating Tests**:
  - `TestJobTransitionMatrix_AllPairs` (updated with 3 approved cell changes)
  - `TestConformance_CancelJob_IdempotentCancelRequested` (planned test)
  - `TestConformance_CancelJob_RetryingImmediatelyCancelled` (planned test)
  - `TestAPI_CancelJob_SuccessReturns200` (planned test)
  - `TestReviewP400_SEC022_DoubleCancelLeaksWorkerSlot` (planned test)
  - `TestInMemoryRepository_LeaseRenewalAndExpiry` (planned test)
  - `TestInMemoryRepository_RetryBackoff` (planned test)
  - `TestProperty_StateMachine_ModelConformance` (planned test)
- **Risks & Mitigations**:
  - Clock advancement complexity in unit tests. *Mitigation:* Inject fake clock (`storage.WithClock`, `coordinator.SetClock`).

---

### Slice 4b: Schema Migration 000004 & PostgreSQL Repository Durable Implementation
- **Scope**:
  - Author embedded migration `000004_leases_retries_cancellation.sql` (proposed):
    - Columns: `attempts.lease_expires_at` (with temporary default for historical row backfill, dropped immediately after), `attempts.error_class` (strictly checked for `'PERMANENT'`, `'TRANSIENT'`, `'UNKNOWN'`, `'LEASE_EXPIRED'`), `attempts.last_error`, `jobs.not_before`, `jobs.cancel_requested_at`.
    - Indexes: `idx_attempts_lease_expiry`, `idx_jobs_retrying_dispatch`.
    - Check constraints: `chk_attempts_error_class`, `chk_attempts_last_error_size` using `NOT VALID` then `VALIDATE CONSTRAINT`.
  - Implement durable repository methods in `internal/storage/postgres.Repository`:
    - `RenewLease`: atomic transaction verifying attempt ownership, unexpired lease, and constant-time token comparison.
    - `SweepExpiredLeases`: single-transaction lock-ordered candidate sweep (`jobs -> workers -> attempts -> results -> job_events`).
    - *Canonical Sweeper Transaction (R-08)*:
      1. Candidate scan (unlocked read against index `idx_attempts_lease_expiry`).
      2. Per-candidate lock order `jobs -> workers -> attempts`: `SELECT id, state, max_attempts FROM jobs WHERE id = $1 FOR UPDATE SKIP LOCKED` (0 rows = skip and retry next tick), then workers and attempts rows with plain `FOR UPDATE` under PostgreSQL `lock_timeout` (default 5s). One grace check inside locks. `SKIP LOCKED` and `NOWAIT` are never combined.
      3. Sweeper state resolution rules (R-02):
         - If parent job is already terminal (`COMPLETED`, `FAILED`, `CANCELLED`): close attempt as `CANCELLED` (if job is `CANCELLED`) or `LOST` (otherwise), leave job state unchanged, free worker slot, and record diagnostic event.
         - If `CANCEL_REQUESTED`: close attempt as `CANCELLED`, transition job to `CANCELLED`, free worker slot, record `job.cancelled` event.
         - If `RUNNING` and `attempt_no < max_attempts`: close attempt as `LOST` (`error_class = 'LEASE_EXPIRED'`), set exponential backoff `not_before`, transition job to `RETRYING`, free worker slot, record `attempt.lost` and `job.retrying` events.
         - If `RUNNING` and `attempt_no >= max_attempts`: close attempt as `LOST` (`error_class = 'LEASE_EXPIRED'`), transition job to `FAILED`, free worker slot, record `attempt.lost` and `job.failed` events.
    - Update `DispatchNextJob`: query evaluates `WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))` with direct transition to `RUNNING`.
    - Update `CommitAttempt`: enforce 409 evaluation order:
      1. Terminal job -> close attempt, free worker slot, record diagnostic event, and return 409 `JOB_ALREADY_TERMINAL`.
      2. Attempt swept to `LOST` -> return 409 `LEASE_EXPIRED`.
      3. Superseded attempt or token mismatch -> return 409 `STALE_ATTEMPT_REJECTED`.
    - Update `CancelJob`: idempotent no-op on `CANCEL_REQUESTED`, immediate `CANCELLED` on `RETRYING`.
  - Run shared conformance test suite against live PostgreSQL container.
- **Exit Criteria**:
  - Forward-only migration 000004 applies cleanly to fresh and migrated databases.
  - PostgreSQL repository passes all shared conformance tests under `-race`.
  - Zero deadlocks under concurrent mixed operations stress test (`-race -count=20`), with Invariant 4 (`worker_capacity` accounting) strictly verified.
  - **Mutation Proofs (Mandatory)**:
    - Bypassing row lock or re-check in `SweepExpiredLeases` must fail `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test).
    - Removing startup grace check in sweeper must cause `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test) to fail.
    - Mutating terminal job attempt closing in `CommitAttempt` must cause `TestPostgresRepository_Sweeper_TerminalJobClosesAttempt` (planned test) to fail.
- **Gating Tests**:
  - `TestMigrator_FreshMigrate`
  - `TestPostgresRepository_Conformance` (expanded with Phase 4 tests)
  - `TestPostgresRepository_Sweeper_CanonicalLockOrder` (planned test)
  - `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test)
  - `TestPostgresRepository_Sweeper_TerminalJobClosesAttempt` (planned test)
  - `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test)
  - `TestPostgresRepository_Concurrency_MixedOperationsStress` (expanded with leases, cancels, and Invariant 4 check)
- **Risks & Mitigations**:
  - Sweeper lock contention with active workers. *Mitigation:* Small batches (LIMIT 50) and strict lock order `jobs -> workers -> attempts`.

---

### Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring
- **Scope**:
  - HTTP API (`internal/api`):
    - Implement dedicated `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` endpoint.
    - Return `{"status": "RENEWED", "lease_expires_at": "...", "cancel_requested": false}`.
    - Single cancellation channel: renewal response returns `cancel_requested: true` when job is in `CANCEL_REQUESTED`.
    - **Heartbeat Cleanup**: Remove `cancelled_job_ids` from worker heartbeat response. Heartbeats remain purely for worker liveness (`{"status": "ACK"}`).
    - Expand `POST /v1/workers/{id}/poll` assignment response with `lease_expires_at` and `lease_duration_seconds`.
    - Rate Limiting Defense Documentation: renewal flooding is formally recorded as designed, not implemented (R-23); in-place defenses are fast state short-circuit, indexed lookup on primary key, and statement timeout; accepted risk under SEC-004.
  - Worker Agent (`internal/worker`):
    - Dedicated background renewal goroutine per running attempt ticking every 10s (1/3 lease duration; `MINICLOUD_LEASE_DURATION / 3`).
    - Renewal response error handling (R-10):
      - 200 with `cancel_requested: true`: initiate cooperative cancellation via `context.Context` and report `CANCELLED` result.
      - 403 `WORKER_MISMATCH` or 409 (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`): terminal rejection. Immediately abort task, cancel context, and do not report result.
      - 404: inspect body. If recognized not-found code (`JOB_NOT_FOUND`, `ATTEMPT_NOT_FOUND`, `WORKER_NOT_FOUND`), terminal rejection: abort task and do not report result. If missing or unrecognized error envelope (old coordinator), log warning once, disable renewals for this attempt, keep running task to completion, and report result via `POST /result`.
      - 400 (`INVALID_REQUEST`, `WORKER_TASK_UNSUPPORTED`): log error, stop renewing, keep running task to completion, report result via `POST /result`.
      - 5xx / transport error: transient failure. Keep running task, retry renewal next interval until lease expiry.
    - Worker result delivery (R-11): 60-second bounded retry loop with exponential backoff on transport errors; immediate exit on 4xx terminal rejections (`JOB_ALREADY_TERMINAL`, `LEASE_EXPIRED`, `STALE_ATTEMPT_REJECTED`).
  - Coordinator Service (`internal/coordinator`):
    - Implement `StartLeaseSweeper(ctx, interval)` ticking every 5s (`MINICLOUD_SWEEPER_INTERVAL`).
    - Outage detection (R-09): any failed tick, or database-time gap > 3x sweep interval between successful ticks, activates 30s grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`) at first successful tick; 30s cooldown caps re-entry.
- **Exit Criteria**:
  - Long-running jobs (> 30s) maintain continuous lease renewals and commit successfully.
  - Cancellation delivered exclusively via renewal response within 10s.
  - Worker receiving lease loss aborts task and suppresses result reporting.
  - Terminating a worker process triggers automated lease expiry and job reclamation.
  - Worker renewal handles missing endpoints (old coordinator) and 400 errors without failing the executing task.
  - **Mutation Proofs (Mandatory)**:
    - Mutating worker renewal response handler to ignore `cancel_requested: true` must cause cooperative cancellation test to fail.
    - Disabling worker abort upon renewal rejection must cause worker zombie isolation test to fail.
    - Mutating outage gap threshold or cooldown must cause outage grace recovery tests to fail.
- **Gating Tests**:
  - `TestAPI_RenewLease_SuccessAndStaleRejection` (planned test)
  - `TestAgent_PeriodicLeaseRenewal_Success` (planned test)
  - `TestAgent_LeaseRenewal_ReceivesCancelRequested` (planned test)
  - `TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt` (planned test)
  - `TestCoordinator_LeaseSweeper_SlowTickOutageGrace` (planned test)
  - `TestCoordinator_LeaseSweeper_CooldownCap` (planned test)
  - `TestWorker_Client_RenewOldCoordinatorGracefulDegradation` (planned test)
  - `TestWorker_Client_Renew400KeepsRunning` (planned test)
  - `TestWorker_Client_ResultDeliveryLoop_BoundedRetry` (planned test)
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
  - Implement exponential backoff with full jitter ([0.5, 1.5]), base 1s, cap 30s, and calculate `jobs.not_before`.
  - Cooperative cancellation:
    - Worker agent attaches `context.WithCancel` to executor context.
    - Preempted executor returns `context.Canceled`.
    - Worker commits result with cancel status; coordinator transitions job from `CANCEL_REQUESTED` to `CANCELLED`.
  - Resolve cancellation races (cancel vs completion, cancel vs lease expiry, duplicate cancels).
- **Exit Criteria**:
  - Transient failures retry up to `max_attempts` with verified backoff delay.
  - Permanent failures transition immediately to `FAILED` without retrying.
  - Cancelled jobs preempt running workers cooperatively within one renewal interval (10s).
  - Zero jobs stranded in `CANCEL_REQUESTED`.
  - **Mutation Proofs (Mandatory)**:
    - Mutating classification of allowlisted task validation error to `ClassTransient` must cause failure classification test to fail.
    - Mutating jitter formula to exceed [0.5, 1.5] bounds must cause jitter distribution test to fail.
- **Gating Tests**:
  - `TestCoordinator_RetryBackoffWithJitter` (planned test)
  - `TestWorker_CooperativeCancellation_ContextPreemption` (planned test)
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
  - Execute property-based state machine test harness (`TestProperty_StateMachine_ModelConformance` (planned test)) for >= 1,000 iterations.
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
