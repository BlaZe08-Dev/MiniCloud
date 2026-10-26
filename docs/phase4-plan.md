# MiniCloud Phase 4 Implementation Plan: Leases, Bounded Retries, and Failure Recovery

## 1. Overview & Milestone Scope
Phase 4 elevates MiniCloud from a persistent durable store (Phase 3) to a self-healing, fault-tolerant distributed execution platform.

In Phase 3, coordinator restarts and storage outages were made resilient, but a dead or partitioned worker with in-flight attempts leaves jobs stranded in `RUNNING` indefinitely ([docs/failure-model.md](failure-model.md)). Phase 4 implements the distributed machinery to safely detect worker loss, bound execution attempts, recover through exponential backoff retries, reject late zombie results, and provide cooperative cancellation.

### Core Objectives & Accepted Parameters
1. **Durable Attempt Leases**: Leases on database time (`lease_expires_at`), periodic worker renewal, and background sweeper reclamation ([ADR 0014](adr/0014-lease-model-and-expiry.md)). Accepted parameters: 30s attempt lease, 10s renewal interval ($1/3$ lease), 5s sweeper interval, batch size 50, dedicated renewal endpoint (`POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew`).
2. **Dual Grace Mechanisms**: Coordinator startup grace period (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30s) and database outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30s), protecting surviving-worker commit guarantees pinned in `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`).
3. **Late-Commit Semantics**: Option 2 (accept-until-swept while attempt is `RUNNING` in database, even if database timestamp slightly exceeds `lease_expires_at`). Once swept to `LOST`, late commits are strictly rejected with HTTP 409 `STALE_ATTEMPT_REJECTED` and zero attempt resurrection.
4. **Sweeper Correctness Under Lock**: Sweeper executes row-locked re-check (`SELECT state, lease_expires_at FROM attempts WHERE id = $1 FOR UPDATE;` in lock order `jobs -> workers -> attempts`), skipping mutation if a renewal landed concurrently.
5. **Bounded Retries with Backoff**: Structured failure classification (`ClassPermanent`, `ClassRetryable`, `ClassUnknown`), exponential backoff with full jitter ($[0.5, 1.5]$), base 1s, cap 30s, default 3 max attempts, `jobs.not_before` scheduling, and direct `RETRYING -> RUNNING` dispatch ([ADR 0015](adr/0015-retry-policy-and-failure-classification.md)). Validation failures of allowlisted tasks are permanent; success is `AttemptCompleted` (not a failure class); `LOST` attempts consume 1 attempt.
6. **Single-Channel Cooperative Cancellation**: Renewal response (`cancel_requested: true`) is the sole cancellation delivery channel ([ADR 0016](adr/0016-cancellation-semantics.md)); removed `cancelled_job_ids` from heartbeat response. Worst-case latency is bounded to 10s (one renewal period). Duplicate cancel on `CANCEL_REQUESTED` becomes an idempotent no-op returning HTTP 202 `CANCEL_REQUESTED`.
7. **Synchronized Deployment Rule**: V1 coordinator and workers deploy together; non-renewing legacy workers are unsupported for tasks longer than 30s.
8. **Hard Gate Foundation**: Serves as the direct foundation for post-Phase 4 Assignment-Based Round-Robin ([ADR 0011](adr/0011-postgres-dispatch-and-recovery.md)).

---

## 2. Proposed Sub-Slices

### Slice 4a: Grace Design, Existing-Test Impact, Domain State Machine & In-Memory Parity
- **Grace Design & Rationale**:
  - Leases expire strictly on database time (`lease_expires_at = clock_timestamp() + 30s`).
  - Without grace, any coordinator restart or database outage lasting $>30$s causes the background sweeper to mass-expire healthy running attempts to `LOST`, scheduling redundant retry attempts and causing duplicate execution.
  - This directly violates the Phase 3 recovery invariant verified in `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`), where surviving workers holding valid tasks successfully commit after the control plane recovers.
  - *Mechanism*: The sweeper enforces two grace conditions before sweeping:
    1. *Startup Grace Period*: Sweeper pauses reclamation for `MINICLOUD_STARTUP_GRACE_PERIOD` (30s) after coordinator boot, allowing surviving workers to issue renewals.
    2. *Database Outage Grace Window*: If repository health checks detect database unavailability lasting $\Delta t$, the sweeper grants an outage grace window (`MINICLOUD_OUTAGE_GRACE_WINDOW`, 30s) before sweeping expired leases.
- **Existing-Test Impact List**:
  1. *Cancellation Duplicate-Call Parity*:
     - *Current Behavior*: Today, `internal/storage/inmemory.go:658-683` and `internal/storage/postgres/repository.go:1135-1215` behave identically: terminal job $\rightarrow$ `ErrJobAlreadyTerminal`; `QUEUED` $\rightarrow$ `CANCELLED`; `RUNNING` $\rightarrow$ `CANCEL_REQUESTED` (attempt unmodified in `RUNNING`); and any subsequent non-terminal state (including `CANCEL_REQUESTED` and `RETRYING`) transitions to `CANCELLED`.
     - *Phase 4 Behavior Change*: A duplicate cancel on a `CANCEL_REQUESTED` job becomes an idempotent no-op returning the same HTTP 202 `CANCEL_REQUESTED`, preventing the job from terminating while the worker is actively running.
     - *Existing Test Inspection*: The repository test suite was inspected (`grep`). The existing test `CancelJob_QueuedAndRunning` in `internal/storage/storagetest/conformance.go:299-363` tests cancelling a `QUEUED` job (transitions to `CANCELLED`), asserts that a second cancel on that terminal job fails with `ErrJobAlreadyTerminal` (line 335), and tests cancelling a `RUNNING` job (transitions to `CANCEL_REQUESTED`, line 356-362). It **does not** test a duplicate cancel on `CANCEL_REQUESTED`.
     - *Impact*: Zero existing tests are broken by making duplicate cancel on `CANCEL_REQUESTED` an idempotent no-op.
     - *New Conformance Test*: `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) will be added to `storagetest/conformance.go` asserting identical idempotent behavior across both in-memory and PostgreSQL implementations.
  2. *Restart & Outage Recovery Tests*:
     - `TestPostgresWiring_CoordinatorRestartRecovery` (`tests/integration/postgres_wiring_test.go:167`) and `TestSecurity_PartD_Outage_MidDispatchAndMidCommit` (`tests/integration/part_d_security_test.go:794`) assert that surviving workers can commit results after restarts and outages.
     - With the 30s startup and outage grace windows, surviving workers commit within the grace window, keeping test assertions valid without weakening any existing test assertion.
- **Scope**:
  - Reconcile attempt state naming: canonical states are `'PENDING'`, `'RUNNING'`, `'COMPLETED'`, `'FAILED'`, `'TIMED_OUT'`, `'CANCELLED'`, `'LOST'`, matching migration `000001_initial_schema.sql:97`. Map/rename domain constants `AttemptState` in `internal/domain/state.go:22-27` (`EXPIRED` $\rightarrow$ `LOST`, `CANCELED` $\rightarrow$ `CANCELLED`).
  - Update `internal/domain`:
    - Add `AttemptOutcome.ErrorClassification` (`ClassPermanent`, `ClassRetryable`, `ClassUnknown`). Exit code 0 is `AttemptCompleted` without error class. Validation failures of allowlisted tasks are `ClassPermanent`.
    - Add `Attempt.LeaseExpiresAt`, `Job.NotBefore`, `Job.CancelRequestedAt`.
    - Validate transitions centrally in FSM with typed errors: `RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING` via direct dispatch.
  - Update `internal/storage.InMemoryRepository`:
    - Implement `RenewLease`, `SweepExpiredLeases`, and dual grace timers.
    - Evaluate `not_before <= clock.Now()` in `DispatchJob` and `ListQueuedJobs`.
    - Handle idempotent duplicate cancel on `CANCEL_REQUESTED`.
  - Expand shared conformance suite in `internal/storage/storagetest/conformance.go`.
  - Property test: `TestProperty_StateMachine_ModelConformance` (planned test) driving reference model and repository with injectable clock.
- **Exit Criteria**:
  - Domain FSM centrally validates all transitions with typed domain errors.
  - In-memory repository passes expanded shared conformance suite.
  - Model-based tests pass cleanly under race detector (`-race -count=1`).
  - **Mutation Proofs (Mandatory)**:
    - Mutating duplicate cancel logic to return error or transition to `CANCELLED` must cause `TestConformance_CancelJob_IdempotentCancelRequested` (planned test) to fail.
    - Mutating FSM transition table to reject `RETRYING -> RUNNING` must cause dispatch tests to fail.
    - Mutating in-memory dispatch to ignore `not_before` must cause retry backoff tests to fail.
- **Gating Tests**:
  - `TestJobTransitionMatrix_AllPairs` (expanded)
  - `TestConformance_CancelJob_IdempotentCancelRequested` (planned test)
  - `TestInMemoryRepository_LeaseRenewalAndExpiry` (planned test)
  - `TestInMemoryRepository_RetryBackoff` (planned test)
  - `TestProperty_StateMachine_ModelConformance` (planned test)
- **Risks & Mitigations**:
  - Clock advancement complexity in unit tests. *Mitigation:* Inject fake clock (`storage.WithClock`, `coordinator.SetClock`).

---

### Slice 4b: Schema Migration 000004 & PostgreSQL Repository Durable Implementation
- **Scope**:
  - Author embedded migration `000004_leases_retries_cancellation.sql` (proposed):
    - Columns: `attempts.lease_expires_at`, `attempts.error_class`, `attempts.last_error`, `jobs.not_before`, `jobs.cancel_requested_at`.
    - Indexes: `idx_attempts_lease_expiry`, `idx_jobs_retrying_dispatch`.
    - Check constraints: `chk_attempts_error_class`, `chk_attempts_last_error_size` using `NOT VALID` then `VALIDATE CONSTRAINT`.
  - Implement durable repository methods in `internal/storage/postgres.Repository`:
    - `RenewLease`: atomic transaction verifying attempt ownership, unexpired lease, and constant-time token comparison.
    - `SweepExpiredLeases`: single-transaction lock-ordered candidate sweep (`jobs -> workers -> attempts -> results -> job_events`).
    - *Sweeper Correctness Re-Check Under Row Lock*:
      ```sql
      SELECT state, lease_expires_at
      FROM attempts
      WHERE id = $1
      FOR UPDATE;
      ```
      If state is not `RUNNING` or `lease_expires_at > clock_timestamp()`, skip mutation (renewal won the race).
    - Update `DispatchNextJob`: query evaluates `WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))` with direct transition to `RUNNING`.
    - Update `CommitAttempt`: enforce Option 2 (accept while `RUNNING` in database, even if slightly expired). If swept to `LOST`, reject with `STALE_ATTEMPT_REJECTED` and record diagnostic event.
    - Update `CancelJob`: idempotent no-op on `CANCEL_REQUESTED`, immediate `CANCELLED` on `RETRYING`.
  - Run shared conformance test suite against live PostgreSQL container.
- **Exit Criteria**:
  - Forward-only migration 000004 applies cleanly to fresh and migrated databases.
  - PostgreSQL repository passes all shared conformance tests under `-race`.
  - Zero deadlocks under concurrent mixed operations stress test (`-race -count=20`).
  - **Mutation Proofs (Mandatory)**:
    - Bypassing row lock or re-check in `SweepExpiredLeases` must fail `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test).
    - Removing startup grace check in sweeper must cause `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test) to fail.
    - Mutating late-commit acceptance while `RUNNING` to immediate rejection must cause late-commit race tests to fail.
- **Gating Tests**:
  - `TestMigrator_FreshMigrate`
  - `TestPostgresRepository_Conformance` (expanded with Phase 4 tests)
  - `TestPostgresRepository_Sweeper_ConcurrentRenewalRecheck` (planned test)
  - `TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit` (planned test)
  - `TestPostgresRepository_Concurrency_MixedOperationsStress` (expanded with leases and cancels)
- **Risks & Mitigations**:
  - Sweeper lock contention with active workers. *Mitigation:* Small batches (LIMIT 50) and strict lock order `jobs -> workers -> attempts`.

---

### Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring
- **Scope**:
  - HTTP API (`internal/api`):
    - Implement dedicated `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` endpoint.
    - Return `{"renewed": true, "lease_expires_at": "...", "cancel_requested": false}`.
    - Single cancellation channel: renewal response returns `cancel_requested: true` when job is in `CANCEL_REQUESTED`.
    - **Heartbeat Cleanup**: Remove `cancelled_job_ids` from worker heartbeat response. Heartbeats remain purely for worker liveness.
    - Expand `POST /v1/workers/{id}/poll` assignment response with `lease_expires_at`.
    - Rate Limiting Defense Documentation: renewal flooding is formally recorded as designed, not implemented; in-place defenses are fast state short-circuit, indexed lookup on primary key, and statement timeout; accepted risk under SEC-004.
  - Worker Agent (`internal/worker`):
    - Dedicated background renewal goroutine per running attempt ticking every 10s ($1/3$ lease duration).
    - When renewal returns `cancel_requested: true`, trigger attempt context cancellation.
    - When renewal fails with 403 `WORKER_MISMATCH` or 409 `STALE_ATTEMPT_REJECTED` (lease lost), abort task execution immediately via context cancellation and do NOT report a result. Worst-case cancellation delivery latency is bounded to 10s.
  - Coordinator Service (`internal/coordinator`):
    - Implement `StartLeaseSweeper(ctx, interval)` ticking every 5s (`MINICLOUD_LEASE_SWEEPER_INTERVAL`), enforcing 30s startup grace and 30s outage grace before sweeping.
- **Exit Criteria**:
  - Long-running jobs (>30s) maintain continuous lease renewals and commit successfully.
  - Cancellation delivered exclusively via renewal response within 10s.
  - Worker receiving lease loss aborts task and suppresses result reporting.
  - Terminating a worker process triggers automated lease expiry and job reclamation.
  - **Mutation Proofs (Mandatory)**:
    - Mutating worker renewal response handler to ignore `cancel_requested: true` must cause cooperative cancellation test to fail.
    - Disabling worker abort upon renewal rejection must cause worker zombie isolation test to fail.
- **Gating Tests**:
  - `TestAPI_RenewLease_SuccessAndStaleRejection` (planned test)
  - `TestAgent_PeriodicLeaseRenewal_Success` (planned test)
  - `TestAgent_LeaseRenewal_ReceivesCancelRequested` (planned test)
  - `TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt` (planned test)
  - `TestAPI_RenewalFlood_FastShortCircuit` (planned test)
- **Risks & Mitigations**:
  - Renewal goroutine leaks. *Mitigation:* Goroutine terminates unconditionally on attempt context cancellation.

---

### Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation
- **Scope**:
  - Implement 3-tier failure classification:
    - Success (exit code 0) is `AttemptCompleted` without error classification.
    - Validation failures of allowlisted tasks (`echo`, `sleep`, `sha256_loop`) are `ClassPermanent`.
    - Worker classifies abnormal terminations (OOM, SIGKILL, context deadline).
    - Unknown/unclassified errors default to `ClassRetryable`.
    - `LOST` attempts consume 1 attempt of `max_attempts` budget.
  - Implement exponential backoff with full jitter ($[0.5, 1.5]$), base 1s, cap 30s, and calculate `jobs.not_before`.
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
    - Mutating classification of allowlisted task validation error to `ClassRetryable` must cause failure classification test to fail.
    - Mutating jitter formula to exceed $[0.5, 1.5]$ bounds must cause jitter distribution test to fail.
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
  - Execute property-based state machine test harness (`TestProperty_StateMachine_ModelConformance` (planned test)) for $\ge 1,000$ iterations.
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
