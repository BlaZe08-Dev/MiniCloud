# MiniCloud Phase 4 Implementation Plan: Leases, Bounded Retries, and Failure Recovery

## 1. Overview & Milestone Scope
Phase 4 elevates MiniCloud from a persistent durable store (Phase 3) to a self-healing, fault-tolerant distributed execution platform.

In Phase 3, coordinator restarts and storage outages were made resilient, but a dead or partitioned worker with in-flight attempts would leave jobs stranded in `RUNNING` indefinitely (`docs/failure-model.md`). Phase 4 implements the distributed machinery to safely detect worker loss, bound execution attempts, recover through exponential backoff retries, reject late zombie results, and provide cooperative cancellation.

### Core Objectives
1. **Durable Attempt Leases**: Leases on database time (`lease_expires_at`), periodic worker renewal, and background sweeper reclamation (ADR-0014).
2. **Bounded Retries with Backoff**: Structured failure classification (`PERMANENT` vs `RETRYABLE`), exponential backoff with full jitter, `jobs.not_before` scheduling, and at-least-once delivery (ADR-0015).
3. **Cooperative Cancellation**: Signal delivery via renewal/heartbeat, Go context preemption, and deterministic race resolution (ADR-0016).
4. **Complete Fault-Injection & Property Testing**: Automated test suite executing all blueprint fault scenarios (FS-01 through FS-10) and property-based reference model testing.
5. **Hard Gate Foundation**: Serves as the direct foundation for the post-Phase 4 Assignment-Based Round-Robin slice (Candidate A in ADR-0011).

---

## 2. Proposed Sub-Slices

### Slice 4a: Domain State Machine, Attempt Lease Primitives, In-Memory Parity & Property Test
- **Scope**:
  - Update `internal/domain`:
    - Add `AttemptState` transition validation and helper predicates.
    - Add `AttemptOutcome.ErrorClassification` (`ClassPermanent`, `ClassRetryable`, `ClassUnknown`).
    - Add `Attempt.LeaseExpiresAt`, `Job.NotBefore`, `Job.CancelRequestedAt`.
    - Update FSM transition matrix with new transitions (`RUNNING -> RETRYING`, `CANCEL_REQUESTED -> CANCELLED`, `RETRYING -> RUNNING/QUEUED`).
  - Update `internal/storage.InMemoryRepository`:
    - Add `RenewLease`, `SweepExpiredLeases`.
    - Respect `not_before` in `DispatchNextJob` and `ListQueuedJobs`.
    - Support cancellation state transitions.
  - Expand shared conformance test suite (`internal/storage/storagetest/conformance.go`) verifying identical behavior for renewals, expiry sweeps, backoff scheduling, and cancellation.
  - Implement property/model-based test harness (`TestProperty_StateMachine_ModelConformance` (planned test)) driving reference model and repository in parallel with injectable clock.
- **Exit Criteria**:
  - Domain FSM centrally validates all new transitions with typed errors.
  - In-memory repository passes expanded shared conformance suite.
  - Model-based tests pass cleanly under race detector (`-race -count=1`).
- **Gating Tests**:
  - `TestJobTransitionMatrix_AllPairs` (expanded)
  - `TestInMemoryRepository_LeaseRenewalAndExpiry` (planned test)
  - `TestInMemoryRepository_RetryBackoff` (planned test)
  - `TestProperty_StateMachine_ModelConformance` (planned test)
- **Risks & Mitigations**:
  - Clock advancement complexity in unit tests. *Mitigation:* Reuse existing injectable `Clock` (`storage.WithClock`, `coordinator.SetClock`).

---

### Slice 4b: Schema Migration 000004 & PostgreSQL Repository Durable Implementation
- **Scope**:
  - Author embedded migration `000004_leases_retries_cancellation.sql` (proposed):
    - Columns: `attempts.lease_expires_at`, `attempts.error_class`, `attempts.last_error`, `jobs.not_before`, `jobs.cancel_requested_at`.
    - Indexes: `idx_attempts_lease_expiry`, `idx_jobs_retrying_dispatch`.
    - Check constraints: `chk_attempts_error_class`, `chk_attempts_last_error_size` using `NOT VALID` then `VALIDATE CONSTRAINT` for zero exclusive lock contention.
  - Implement durable repository methods in `internal/storage/postgres.Repository`:
    - `RenewLease`: atomic transaction verifying attempt ownership, unexpired lease, and constant-time token comparison.
    - `SweepExpiredLeases`: single-transaction lock-ordered candidate sweep (`jobs -> workers -> attempts -> results -> job_events`).
    - Update `DispatchNextJob`: query evaluates `WHERE (state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp()))`.
    - Update `CommitAttempt`: enforce lease validity check (`clock_timestamp() < lease_expires_at`), handle cancellation confirmation, and insert `diagnostic_late_result` on stale commit.
    - Update `CancelJob`: handle `RETRYING` cancellation and store `cancel_requested_at`.
  - Run shared conformance test suite against live PostgreSQL container.
- **Exit Criteria**:
  - Forward-only migration 000004 applies cleanly to fresh and migrated databases.
  - PostgreSQL repository passes all shared conformance tests under `-race`.
  - Zero deadlocks under concurrent mixed operations stress test (`-race -count=20`).
- **Gating Tests**:
  - `TestMigrator_FreshMigrate`
  - `TestPostgresRepository_Conformance` (expanded with Phase 4 tests)
  - `TestPostgresRepository_Concurrency_MixedOperationsStress` (expanded with leases and cancels)
- **Risks & Mitigations**:
  - Sweeper lock contention with active workers. *Mitigation:* Sweeper processes small batches (LIMIT 50) and strictly obeys the canonical lock order `jobs -> workers -> attempts`.

---

### Slice 4c: HTTP API Protocol, Worker Lease Renewal & Coordinator Sweeper Wiring
- **Scope**:
  - HTTP API (`internal/api`):
    - Implement `POST /v1/jobs/{job_id}/attempts/{attempt_id}/renew` handler with boundary validation.
    - Expand `POST /v1/workers/{id}/heartbeat` response with `cancelled_job_ids`.
    - Expand `POST /v1/workers/{id}/poll` assignment response with `lease_expires_at`.
  - Worker Agent (`internal/worker`):
    - Spawn dedicated background renewal goroutine per running attempt ticking every 10s ($1/3$ lease duration).
    - Handle renewal responses: update local lease expiry, observe `cancel_requested: true`.
    - Auto-terminate attempt on 403 `WORKER_MISMATCH` or 409 `STALE_ATTEMPT_REJECTED`.
  - Coordinator Service (`internal/coordinator`):
    - Implement `StartLeaseSweeper(ctx, interval)` ticking every 5s (`MINICLOUD_LEASE_SWEEPER_INTERVAL`).
- **Exit Criteria**:
  - Long-running jobs (>30s) maintain continuous lease renewals and commit successfully.
  - Terminating a worker process during task execution triggers automated lease expiry and job reclamation.
  - Backwards-compatible interaction between old/new worker and coordinator versions verified.
- **Gating Tests**:
  - `TestAPI_RenewLease_SuccessAndStaleRejection` (planned test)
  - `TestAgent_PeriodicLeaseRenewal_Success` (planned test)
  - `TestCoordinator_LeaseSweeper_ReclaimsAbandonedAttempt` (planned test)
- **Risks & Mitigations**:
  - Renewal goroutine leaks if worker crashes or finishes. *Mitigation:* Goroutines cancel cleanly upon attempt context termination.

---

### Slice 4d: Structured Retries, Failure Classification & Cooperative Cancellation
- **Scope**:
  - Implement 3-tier failure classification:
    - Predefined tasks (`echo`, `sleep`, `sha256_loop`) set `AttemptOutcome.ErrorClassification`.
    - Worker classifies abnormal terminations (OOM, SIGKILL, context deadline).
    - Coordinator evaluates classification and `max_attempts` budget.
  - Implement exponential backoff with full jitter ($[0.5, 1.5]$) and `not_before` calculation.
  - Cooperative cancellation:
    - Worker agent attaches `context.WithCancel` to executor execution context.
    - When cancel signal is observed, worker cancels context; executor returns `context.Canceled`.
    - Worker commits result with cancel status; coordinator transitions job from `CANCEL_REQUESTED` to `CANCELLED`.
  - Resolve all cancellation races (cancel vs completion, cancel vs lease expiry, duplicate cancels).
- **Exit Criteria**:
  - Transient failures retry up to `max_attempts` with verified backoff delay.
  - Permanent failures transition immediately to `FAILED` without retrying.
  - Cancelled jobs preempt running workers cooperatively within one heartbeat/renewal interval.
  - Zero jobs stranded in `CANCEL_REQUESTED`.
- **Gating Tests**:
  - `TestCoordinator_RetryBackoffWithJitter` (planned test)
  - `TestWorker_CooperativeCancellation_ContextPreemption` (planned test)
  - `TestAPI_CancelJob_RunningRaces` (planned test)
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
