# ADR 0017: Assignment-Based Round-Robin Dispatch (Candidate A), Design

## Status
Accepted for implementation (orchestrator, revision 3 plus A1 and A2). Slice 5a (domain, pure policy and in-memory parity, no database) is the first implementation slice; the amendments are recorded in Section 9.

Implementation status: slice 5a is merged (PR #35). Slice 5b (migration `000005` and the PostgreSQL assignment repository) is built on branch `agent/claude/P4-08c` and pushed for audit; see [the P4-08c handoff](../agents/handoffs/P4-08c.md). Slice 5c is not started. This note changes no decision in this ADR.

- Formalises **Candidate A** (pre-assignment with a bounded accept window), the leading design named in [ADR 0011](0011-postgres-dispatch-and-recovery.md) Section 2 and in [docs/phase4-plan.md](../phase4-plan.md) Section 3. It is the post-Phase 4 hard gate for V1 completion.
- Depends on the Phase 4 machinery: attempt leases and the sweeper ([ADR 0014](0014-lease-model-and-expiry.md)), retries ([ADR 0015](0015-retry-policy-and-failure-classification.md)), and cancellation ([ADR 0016](0016-cancellation-semantics.md)). Migration `000004_leases_retries_cancellation.sql` is already in the tree; this design adds migration `000005`.
- Does not change the `fifo_pull` baseline (ADR 0011 Candidate C). It stays selectable.

### Revision 1 record
The orchestrator reviewed revision 0 and raised items R1 to R7. Each is fixed in the section named here.

| Item | Defect in revision 0 | Where fixed in this revision |
|---|---|---|
| R1 | One shared cursor broke per-task-type fairness under interleaved types. | Sections 2.3, 2.6, 5.3, 6.3 (INV-4, INV-4b), Section 8 Q7 |
| R2 | A worker that had just claimed still looked assignable and could be reserved more jobs. | Sections 2.2, 2.4 (one effective slot), 1.7, 4.3, 6.3 (INV-10) |
| R3 | No liveness path when the assignment pass stalled. | Sections 3.2, 3.3, 6.1 (FS-21), 6.3 (INV-14, INV-16), 6.4 |
| R4 | No per-pass bounds, no index for the scan, no cap on the event payload. | Sections 2.1, 2.5, 5.3, 6.2, 6.3 (INV-15, INV-18) |
| R5 | Consistency sweep, plus further defects found during it. | Section 9 (new) lists every change and each further defect |
| R6 | Poll timing constants were assumed, not checked. | Section 3.1, Section 6.3 (INV-17), Section 6.4, Section 9 |
| R7 | Open questions had no recorded answers. | Section 8 (answer column) |

### Revision 2 record
Revision 2 answers the orchestrator's items N1 to N5 and re-reads Sections 5.3, 6.3 and 7 for anything they affect. Further defects found in revision 2 are numbered 19 to 22 in Section 9.

| Item | Change | Where fixed in this revision |
|---|---|---|
| N1 | Age arm after a restart or outage. A poll refreshes a stale heartbeat before the age arm, instead of the requested grace-window suppression. | Sections 3.2, 4.1, 4.4, 6.1 (FS-21, FS-23), 6.2, 6.3 (INV-14, INV-19), 6.4 |
| N2 | No minimum yield between repeated passes, and the repeat rule re-ran on a full batch with no progress. | Sections 2.1, 4.4, 6.1 (FS-24), 6.3 (INV-20, INV-21), 6.4, 7 |
| N3 | Advisory lock blocked a second pass. It is now `pg_try_advisory_xact_lock`: a second pass skips and logs. | Sections 2.1, 2.3, 4.2, 4.3, 5.4, 6.3 (INV-22) |
| N4 | The count of further defects disagreed between the ADR, the report and the message. | Section 9 (one list per revision, each item once) |
| N5 | Orchestrator answers R-a to R-e recorded. | Section 8.2 and Section 8.3 |

---

## Context

### Evidence labels
Every statement about current behaviour below was read from the code or docs on branch `agent/claude/P4-08a` (based on `origin/main` at e93f484). **Nothing was executed**: no database test, no Go test and no benchmark was run for this design. Each claim is labelled `confirmed by code reading` (I read it in the named function or file) or `inferred` (reasoning from what I read). Functions are cited by name only.

### Verified current behaviour

| # | Fact | Label |
|---|---|---|
| C1 | `DispatchNextJob` in `internal/storage/postgres/repository.go` selects the head job with `state = 'QUEUED' OR (state = 'RETRYING' AND not_before <= clock_timestamp())`, filtered by `task_type = ANY(worker.supported_tasks)`, `ORDER BY created_at ASC, id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`. It locks the job first, then the worker row (`FOR UPDATE`), then recounts capacity. | confirmed by code reading |
| C2 | The same function inserts the attempt with `attempt_no = MAX + 1`, a hashed lease token and `lease_expires_at`, sets the job to `RUNNING`, and writes a `JOB_DISPATCHED` event with `policy = fifo_pull`. Nothing is assigned to a worker before a poll. | confirmed by code reading |
| C3 | The in-memory `DispatchNextJob` in `internal/storage/inmemory.go` scans `jobOrder` (insertion order) with `isDispatchableLocked`, which uses the injected clock for `not_before`. | confirmed by code reading |
| C4 | `PollWork` in `internal/coordinator/service.go` holds `s.mu` for the whole call and calls only `repo.DispatchNextJob`. `scheduler.Scheduler` is not called on the dispatch path. `cmd/coordinator/main.go` constructs the round-robin scheduler and passes it in, and it is unused. | confirmed by code reading |
| C5 | `RoundRobinScheduler.SelectWorker` in `internal/scheduler/round_robin.go` keeps an **index** (`lastIndex`) in process memory over the sorted eligible list. An index does not survive a worker joining or leaving. | confirmed by code reading |
| C6 | The `worker_capacity` view (`000002_schema_hardening.sql`) computes `available_slots = total_slots - count(attempts in PENDING or RUNNING)`. The partial unique index `idx_attempts_active_per_job` covers `PENDING` and `RUNNING`. `attempts` has a `worker_id` column (`000001_initial_schema.sql`). | confirmed by code reading |
| C7 | `chk_jobs_state` in `000001_initial_schema.sql` lists `SUBMITTED, QUEUED, RUNNING, CANCEL_REQUESTED, CANCELLED, SUCCEEDED, FAILED, RETRYING, DEAD_LETTER`. It does **not** list `REJECTED`, which `internal/domain/transition.go` defines. I found no migration that adds it. | confirmed by code reading |
| C8 | `job_events.event_type` is `VARCHAR(64)` with no CHECK constraint, so new event types need no DDL. | confirmed by code reading |
| C9 | `workers` has `status` (`HEALTHY`, `UNHEALTHY`, `DRAINING`, `TERMINATED`) and `last_heartbeat_at`. `DRAINING` exists in `internal/domain/state.go`; I found no code path that sets it or any deregistration API. | confirmed by code reading |
| C10 | `ReapStaleWorkers` (Postgres) and `ReapInactiveWorkers` (service) set `HEALTHY` workers to `UNHEALTHY` when `last_heartbeat_at` is older than the worker timeout, on the reaper tick. Config: `MINICLOUD_WORKER_TIMEOUT` (default 10 s) and reaper interval (default timeout / 2) in `internal/config/config.go`. | confirmed by code reading |
| C11 | The sweeper (`LeaseSweeper.Tick` in `internal/coordinator/sweeper.go`) keeps its grace state (startup deadline, outage window, cooldown) in process memory and passes it to `SweepExpiredLeases` as `domain.SweepGraceState`. The Postgres sweeper selects only `attempts.state = 'RUNNING'` rows and locks `jobs` with `SKIP LOCKED`, then `workers`, then `attempts`. | confirmed by code reading |
| C12 | The worker's `Run` loop in `internal/worker/client.go` calls `ExecuteAndReport` **synchronously** after a successful poll. A worker therefore polls only while it is executing nothing. | confirmed by code reading |
| C13 | Heartbeats run on their own loop, so a busy worker keeps its heartbeat fresh while it does not poll. | confirmed by code reading |
| C14 | `CancelJob` in both repositories handles `QUEUED` (to `CANCELLED`), `RETRYING` (to `CANCELLED`), and `RUNNING` (to `CANCEL_REQUESTED`), and makes a duplicate cancel on `CANCEL_REQUESTED` a no-op. | confirmed by code reading |
| C15 | Poll response codes today are 200 with an `assignment`, 204 `NO_QUEUED_JOBS`, and 503 `WORKER_UNHEALTHY` ([docs/protocol.md](../protocol.md) Section 4.3). | confirmed by code reading (doc) |
| C16 | In `Run` (`internal/worker/client.go`) the idle sleep and the maximum poll backoff are **local variables** (`baseBackoff` = 200 ms, `maxBackoff` = 3 s), not package constants. A poll error or a `ErrServiceUnavailable` sleeps `ComputeBackoffWithJitter(failures, baseBackoff, maxBackoff)`. An empty poll (204) or a `WORKER_UNHEALTHY` poll sleeps `baseBackoff` with no jitter. | confirmed by code reading |
| C17 | `ComputeBackoffWithJitter` in `internal/worker/client.go` multiplies the base by a power of two (capped at 16x), caps it at `max`, then scales by a factor in **[0.5, 1.0)**. It is equal-range jitter, not "full" jitter (which would be [0, 1)). The largest value it returns is therefore `max` = 3 s. | confirmed by code reading |
| C18 | `NewAgent` in `internal/worker/client.go` builds the poll HTTP client with `Timeout: 15 * time.Second`. A poll that takes longer than the window is possible in principle; see Section 3.1. | confirmed by code reading |
| C19 | `idx_jobs_task_dispatch` (`000001_initial_schema.sql`) indexes `(task_type, created_at)` **only for `state = 'QUEUED'`**. `idx_jobs_retrying_dispatch` (`000004_leases_retries_cancellation.sql`) indexes `(not_before, created_at)` only for `RETRYING`. No existing index serves a per-task-type scan of `RETRYING` rows. | confirmed by code reading |
| C20 | `jobs.not_before` was added by `000004_leases_retries_cancellation.sql`. It is set for `RETRYING` jobs and is the gate `DispatchNextJob` applies. | confirmed by code reading |
| C21 | `docs/phase4-plan.md` Section 3 states that "the Phase 4 lease sweeper machinery (`StartLeaseSweeper`) directly powers assignment expiration" and that the sweeper "revokes the assignment". This design does **not** do that: assignment expiry is handled by a separate assignment pass (Section 2.1). | confirmed by code reading (doc) |

### What is missing
Dispatch is first-poller-wins (ADR 0011 Section 2.1). Blueprint Section 6 requires scheduler-driven round-robin. This ADR decides how a scheduler can choose a worker before the worker claims a job, without reintroducing the rejected hold window (Candidate B, ADR 0011 Section 2.5).

## Decision summary

| # | Question | Decision |
|---|---|---|
| D1 | What is an assignment? | Four nullable columns on `jobs` (worker, assigned time, expiry, lapse counter) plus a fallback reason, and a sub-state of `QUEUED`/`RETRYING`. **No new job state, no new attempt state.** |
| D2 | Does assignment create an attempt? | **No.** Assignment only reserves. The attempt, its number, its lease token and its lease are created at claim (the poll), as today. |
| D3 | Who picks the worker? | A coordinator-owned **assignment pass**, a transaction separate from polls, run on a timer and after submit. Not inside the poll. |
| D4 | Round-robin order | **One cursor per task type.** Next eligible worker by ID after the cursor of that type, wrapping. Cursors are rows in `assignment_cursor`, read and written inside the pass transaction. (R1) |
| D5 | Who may claim? | A poll claims, in order: (a) a job assigned to it while the assignment is unexpired on database time; else (b) a fallback job (D9). A poll never claims a live reservation of another worker. |
| D6 | Accept window | `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`, default **5 s**, allowed **5 s to 30 s**. The floor was 2 s in revision 0; it is raised by R6 because the worker can back off for 3 s. Window lapse is a lapse, not an attempt. |
| D7 | Eligibility | Healthy status, fresh heartbeat, fresh **poll** (`workers.last_poll_at` within the window), **one effective slot** (no `PENDING` or `RUNNING` attempt for the worker, and no outstanding reservation), and task support. Re-checked under the worker row lock before each reservation. (R2) |
| D8 | Lapses | Counted per job in `assignment_lapses`. Not counted during a grace window or for a worker that is no longer healthy. Lapses do not consume `max_attempts`. |
| D9 | Fallback | A job leaves round-robin and becomes claimable by any polling worker when **either** (i) `assignment_lapses >= MINICLOUD_ASSIGNMENT_MAX_LAPSES` (default **3**), **or** (ii) it has been dispatchable for at least `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` (default **30 s**) **and** the assignment pass has been silent for that long, after this poll's own refresh attempt has failed (N1, N6, Section 3.2). Both are logged as `round_robin_fallback` with the reason. (R3, R4) |
| D10 | Cancel | QUEUED-assigned and RETRYING-assigned jobs go straight to `CANCELLED` and clear the assignment in the same transaction. |
| D11 | Concurrency | Single coordinator: correct by the service mutex and row locks. Several coordinators: correct by row locks and state-guarded `UPDATE`s; the pass takes `pg_try_advisory_xact_lock`, so only one pass runs at a time and a second pass skips rather than blocks (N3). Multi-coordinator operation is not claimed (Q9). |
| D12 | `fifo_pull` | Stays selectable as `MINICLOUD_DISPATCH_POLICY=fifo_pull`. The default flips to `round_robin` in its own reviewed change after the audit (Q1). |
| D13 | Protocol | Additive only: a `policy` field on the poll assignment and assignment fields on the job view. No new endpoint and no new error code. |
| D14 | Batch bound | Per pass, at most `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` (default **50**) candidates are read and reserved **per task type**, and the revoke step reads at most the same number. A repeat within a timer tick needs a reservation and a yield (N2, Section 2.1). (R4, N2) |

---

## 1. What "assignment" is

### 1.1 Representation: columns on `jobs`, not a table, not a state

Options considered:

| Option | Description | Verdict |
|---|---|---|
| A. New job state `ASSIGNED` | A job state between `QUEUED` and `RUNNING`. | **Rejected.** It needs a `chk_jobs_state` rewrite, new FSM edges in `internal/domain/transition.go`, and a new value in the state matrix in [docs/state-machine.md](../state-machine.md). The assignment is an attribute of a queued job, not a lifecycle stage a client should see. |
| B. Separate `job_assignments` table | One row per active assignment. | **Rejected for V1.** It adds a join to the claim and a second place to keep consistent with `jobs.state`. Only one live assignment per job is needed. A table would also need the same `FOR UPDATE SKIP LOCKED` on its own rows and a lock-order entry. |
| **C. Columns on `jobs`** | `assigned_worker_id`, `assigned_at`, `assignment_expires_at`, `assignment_lapses`, `assignment_fallback_reason`. A CHECK constraint ties them to `state`. | **Chosen.** The claim already locks the job row first (C1), so the assignment is read and cleared under the lock that already protects the job. ADR 0011 Section 2.4 proposed the same columns. |

Rules the schema must enforce (see Section 5.3):

- The three assignment columns (`assigned_worker_id`, `assigned_at`, `assignment_expires_at`) are all NULL or all non-NULL.
- Non-NULL assignment columns are allowed only when `state IN ('QUEUED', 'RETRYING')`. Every path that moves a job to `RUNNING`, `CANCELLED`, `CANCEL_REQUESTED`, `SUCCEEDED`, `FAILED` or `REJECTED` must clear them in the same statement. The CHECK is the backstop: a path that forgets fails loudly rather than leaving a stale reservation.
- `assignment_fallback_reason` is set only by a fallback claim and is never cleared (it is history for the job view).

### 1.2 Assignment reserves; the attempt is created at claim

Options:

| Option | Description | Verdict |
|---|---|---|
| A. Create a `PENDING` attempt at assignment | The attempt row, its number and its token exist before the worker accepts. | **Rejected.** A `PENDING` attempt counts against worker capacity (C6), is covered by `idx_attempts_active_per_job`, and consumes an `attempt_no`. An assignment that lapses would leave an attempt that never ran, and the lapse would then consume budget. The `PENDING` value exists in `chk_attempts_state` but no code path writes it today. This design keeps it unused and leaves the question of removing it from the docs to a follow-up (Section 8, Q8). |
| B. Create the attempt at claim (chosen) | The attempt, its number, its token and its lease are created in the same transaction as the `RUNNING` transition, exactly as in `DispatchNextJob` today. | **Chosen.** Attempts still mean "a worker has started or been handed the job". Lapses leave no trace in `attempts`. |

Consequences:
- A token is minted only inside the claim transaction and returned in the same response. No token is ever issued to a worker that was not the claimant, so there is no "late accept of a stale token" to handle (Section 3.5).
- `attempt_no` and `max_attempts` accounting is unchanged (Section 1.4).

### 1.3 Transitions

No job-state transition is added. The matrix in [docs/state-machine.md](../state-machine.md) and `allowedTransitions` in `internal/domain/transition.go` are unchanged.

Assignment is a sub-state of job states `QUEUED` and `RETRYING`. `U` = unassigned, `A(w)` = assigned to worker `w`, `Exp(w)` = assigned to `w` with `assignment_expires_at <= clock_timestamp()`.

Expiry is observed by a predicate on database time (every claim and every pass evaluates it). The **write** that clears an expired reservation and counts its lapse happens in the next pass. Between the two, the row is `Exp(w)`: the claim predicate treats it as not reserved to `w`, and as reserved to nobody for fallback purposes.

| From | Trigger | To | Job `state` change | Writes | Audit event (new event types in `internal/domain/events.go`) |
|---|---|---|---|---|---|
| U (`QUEUED` or dispatchable `RETRYING`) | Assignment pass selects `w` (Section 2) | A(w), expires at `now + window` | none | set the assignment columns | `JOB_ASSIGNED` (payload in Section 2.1) |
| A(w) | Poll by `w` while `assignment_expires_at > clock_timestamp()` | `RUNNING` (claim) | `QUEUED`/`RETRYING` to `RUNNING` | insert attempt; clear assignment; `assignment_lapses = 0`; `not_before = NULL` | `JOB_DISPATCHED` `{policy: "round_robin"}` |
| A(w) | Poll by any other worker | unchanged | none | none | none (poll returns 204 to that worker, unless it qualifies for fallback below) |
| A(w) or `Exp(w)` | Pass observes expiry, `w` `HEALTHY`, **not** in grace | U | none | clear assignment; `assignment_lapses + 1` | `JOB_ASSIGNMENT_LAPSED` `{worker_id, lapses, counted: true}` |
| A(w) or `Exp(w)` | Pass observes expiry, grace window active | U | none | clear assignment; lapses unchanged | `JOB_ASSIGNMENT_LAPSED` `{worker_id, counted: false, reason: "grace"}` |
| A(w) | `w` is no longer `HEALTHY` (reaper, Section 3.4) | U | none | clear assignment; lapses unchanged | `JOB_ASSIGNMENT_REVOKED` `{worker_id, reason: "worker_unhealthy"}` |
| A(w) or U | `CancelJob` on a `QUEUED` or `RETRYING` job | `CANCELLED` (terminal) | to `CANCELLED` | clear assignment | `JOB_CANCELLED` (existing) |
| U or `Exp(w)`, and fallback condition holds (Section 3.2) | Poll by any healthy worker with capacity, in fallback order | `RUNNING` (claim) | to `RUNNING` | as claim; set `assignment_fallback_reason` | `JOB_DISPATCHED` `{policy: "round_robin_fallback", fallback_reason: "lapses" or "age"}` |
| any | `RUNNING`, `CANCEL_REQUESTED`, terminal | any | (not an assignment transition) | assignment columns must already be NULL | CHECK constraint (Section 5.3) |

Attempt states: no new transition. An attempt is created only at claim, as today.

Non-transitions that a reviewer might expect and that do **not** exist: a re-assignment of a live reservation (the pass skips jobs whose reservation is unexpired), a `PENDING` attempt, and any lapse that changes `attempt_no`.

### 1.4 Interaction with `attempt_no` and `max_attempts`

- A lapse creates no attempt, so it does not move `attempt_no`. It does not consume `max_attempts`.
- A `LOST` attempt (ADR 0014 sweeper, after the claim) still consumes one attempt, as ADR 0015 Section 2 decides.
- A claim whose response is lost in transit (Section 3.6) creates an attempt that never runs. It is then reaped to `LOST` and **does** consume one attempt. This is the existing lease behaviour applied to a claim. **Decided by the orchestrator (Q3): yes, it consumes `max_attempts`, the same as `fifo_pull` today.**

### 1.5 Interaction with `LOST` and `RETRYING`

- The sweeper sets a `RUNNING` job to `RETRYING` (with `not_before`) or `FAILED` (ADR 0014 Section 4). The assignment columns were cleared at claim, so the transition adds no assignment work.
- A `RETRYING` job is eligible for assignment once `not_before <= clock_timestamp()`, the same gate as `DispatchNextJob`. It then goes through the same pass as a `QUEUED` job (Q6: both).
- Assignment of a `RETRYING` job never sets `not_before`.

### 1.6 Interaction with cancellation

| Job state when cancelled | Result | Assignment | Worker effect |
|---|---|---|---|
| `QUEUED`, assigned or not | `CANCELLED` immediately | cleared in the cancel transaction | none (no attempt exists). A poll that races the cancel either sees the row after the cancel and returns 204, or claims it before the cancel and the cancel then sees `RUNNING`. The job row lock serialises the two. |
| `RETRYING`, assigned or not | `CANCELLED` immediately | cleared | none |
| `RUNNING` | `CANCEL_REQUESTED` (ADR 0016), unchanged | not applicable (cleared at claim) | renewal delivers `cancel_requested` |
| `CANCEL_REQUESTED` | idempotent no-op (ADR 0016) | not applicable | none |

### 1.7 Interaction with leases and renewal

Leases, renewal and the sweeper are unchanged. Assignment has its own clock, `assignment_expires_at`, on database time, following ADR 0014 Section 1. The two clocks never interact: assignment expiry concerns a job that has no attempt; lease expiry concerns an attempt that exists.

The one exception is the **eligibility read**. The pass reads `attempts` (only in `PENDING` or `RUNNING`, for the worker, the same predicate as `worker_capacity`, C6) to decide whether a worker has its one effective slot (Section 2.4). That read is a plain snapshot read. It is safe only because the pass re-reads it under the worker row lock before it writes a reservation (Section 4.3). The pass writes no `attempts` row and takes no lock on `attempts`.

### Rejected alternatives for Section 1 (summary)
- New `ASSIGNED` job state: rejected (state matrix and CHECK churn; the assignment is not client-visible lifecycle).
- Separate assignments table: rejected for V1 (extra join and lock surface; one live assignment per job suffices).
- `PENDING` attempt at assignment: rejected (capacity and uniqueness side effects; lapses would consume budget or leave ghost attempts).

---

## 2. The scheduler

### 2.1 Who picks the worker: a pass in the coordinator, not in the poll

Options:

| Option | Description | Verdict |
|---|---|---|
| A. Inside the poll transaction | When worker `W` polls, the transaction decides which queued job `W` gets, using a global order. | **Rejected.** A poll sees only its own worker. Deciding for other workers inside a poll is exactly the hold-window of Candidate B, which ADR 0011 rejected (test determinism, idle-worker penalty, and Redis incompatibility). |
| B. At submit only | Assign each job when it is submitted. | **Rejected.** It ignores workers that join, leave, become unhealthy, or free slots later, and it cannot handle `RETRYING` jobs. |
| **C. Separate assignment pass** | A coordinator loop runs one transaction per tick. It (1) revokes expired or orphaned reservations, bounded by the batch limit; (2) reserves dispatchable jobs per task type in FIFO order; (3) writes the per-type cursors and the pass heartbeat. Runs on a timer (default 1 s, `MINICLOUD_ASSIGNMENT_INTERVAL`) and immediately after a submit. | **Chosen.** It keeps the scheduler a writer of reservations only, leaves the poll cheap and claim-only, and reuses the lease sweeper's cadence and failure handling (Section 4.4). It is **not** the lease sweeper (C21). |

The pass is a method on the Postgres repository (`AssignQueuedJobs`) and its in-memory twin. The loop lives in the coordinator beside the sweeper (Section 5.4). Blueprint Section 6 "recording structured audit logs for every decision" is satisfied because the pass writes one `JOB_ASSIGNED` event per decision.

**Batch bounds (R4).** Per pass and per task type `t`:

- The pass reads at most `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` (default **50**) candidate jobs of type `t`, ordered by `created_at, id`, with `FOR UPDATE SKIP LOCKED LIMIT B`. The candidate read is **per type**, so jobs of a type with no eligible worker never occupy the batch of another type. (A single `LIMIT B` over all types would let a head of unplaceable jobs starve every other type; this is why the limit is per type.)
- The revoke step reads at most `B` expired reservations per pass.
- **Repeat rule (N2).** The loop runs the pass again without waiting for the timer only when, in that pass, some type's candidate read returned a full batch **and** at least one reservation was made. A full batch with no reservation (for example, every candidate's type has no eligible worker) does not repeat (further defect 19, INV-21). Before a repeat the loop waits `MINICLOUD_ASSIGNMENT_MIN_YIELD` (default **10 ms**, 1 ms to 1 s, not above `MINICLOUD_ASSIGNMENT_INTERVAL`) with `s.mu` released (INV-20). Backlog drains at `B` per type per transaction while reservations are possible.
- The event payload is capped (Section 2.1 event shape below).

**Head-of-type guarantee.** In every pass where at least one worker of type `t` is eligible at the start of its decisions, the oldest dispatchable unassigned job of type `t` is assigned. It is the first candidate of its type, and at its decision there is an eligible worker. So a newer job of type `t` cannot overtake it, and no other type can take its batch slot. (Test INV-15.)

**Advisory lock semantics (N3).** The pass's first statement is `pg_try_advisory_xact_lock(key)` with the fixed key of Section 4.2. It never blocks. If it returns false, the pass returns `skipped` without reading or writing any row (not `assignment_pass_state` either), logs one line stating that another pass holds the lock, and does not repeat. The next timer tick or submit trigger tries again. A skip is not a failure. It leaves `last_completed_at` unchanged, and the lock holder's commit refreshes it (INV-22).

**Yield and latency bounds under a deep backlog (N2).** Let `D` be one pass duration. `D` is not measured, and no number is given for it (Limits of this design). `s.mu` is held for a pass, a refresh and a claim (Section 4.1). A repeat is preceded by `MINICLOUD_ASSIGNMENT_MIN_YIELD` with `s.mu` released, so a poll waiting for the mutex takes it at that release. This relies on the release, not on fairness in the Go runtime, and is not measured.
- **Poll latency** added by passes is at most `D` (the pass in progress when the poll arrives), plus the claim itself. A poll never waits behind two passes.
- **Commit latency** (submit to reservation) for a job with `p` unassigned jobs of its type ahead of it is at most `p` repeated passes, each costing at most `D + MINICLOUD_ASSIGNMENT_MIN_YIELD`, because each repeat reserves at least one job ahead of it. The chain stops at the first pass with no reservation. After that the job waits for the timer and for an eligible worker. With few eligible workers, the drain is paced by worker polls (Section 2.4), not by the pass. An untriggered job waits at most one timer interval plus `D` (Section 2.6).
- The yield is a pause between passes, not a rate limit. It does not cap throughput. It bounds how long the mutex is held without a gap.

**`JOB_ASSIGNED` payload.** `{worker_id, expires_at, policy: "round_robin", task_type, cursor_before, selected, candidates: [first 10 eligible worker IDs for t in ID order], candidate_count, reason}`. The cap of 10 is a constant, not configuration. It keeps the event row bounded when many workers are eligible (INV-18).

### 2.2 Eligibility of a worker

A worker `w` is eligible for a decision for job `j` of type `t` when all hold at the moment of the decision:

| Check | Source | Why |
|---|---|---|
| `status = 'HEALTHY'` | `workers.status` | Reaper-maintained health. |
| `last_heartbeat_at > clock_timestamp() - MINICLOUD_WORKER_TIMEOUT` | `workers.last_heartbeat_at` | Status changes only on the reaper tick (C10), so a worker can still be `HEALTHY` for up to the timeout plus one reaper interval after its heartbeats stop. This check closes that gap. |
| `last_poll_at > clock_timestamp() - MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` | new column `workers.last_poll_at` | **Poll freshness.** A worker that is executing does not poll (C12). Assigning to it would lapse the window. Only workers that polled within the window can accept. |
| **No `PENDING` or `RUNNING` attempt for `w`** | `attempts` (snapshot, re-read under the worker row lock, Section 4.3) | **One effective slot (R2, Section 2.4).** A worker that has just claimed is `RUNNING`, so it is not reserved more work even though its poll is fresh. |
| **No outstanding reservation for `w`** | `jobs.assigned_worker_id = w` with an unexpired `assignment_expires_at` | At most one reservation per worker (Section 2.4). |
| `worker_capacity.available_slots > 0` | view `worker_capacity` (C6) | The real capacity check the claim also applies. |
| `j.task_type IN supported_tasks(w)` | `workers.supported_tasks` | Same rule as `DispatchNextJob`. |

`last_poll_at` is set by every poll that reaches the worker's row while it is `HEALTHY`: on a claim (inside the claim transaction) and on a 204. The 204 write is a standalone `UPDATE workers` in its own transaction, which holds no other lock and so cannot form a cycle. It is throttled to at most one write per second per worker (`last_poll_at < now - 1 s`). Polls from `UNHEALTHY` workers keep their 503 and do not refresh `last_poll_at` (protocol Section 4.3).

The claim's refresh of `last_poll_at` is not needed for R2: the `RUNNING` check excludes the worker while it executes, whatever `last_poll_at` says.

### 2.3 Round-robin: the rule, its state and its restart behaviour

**Rule.** For one decision about job `j` with task type `t`, let `E(t)` be the workers eligible for `t` (Section 2.2) at the moment of the decision, sorted by worker ID in byte order. Let `c_t` be the cursor for `t`, the ID of the worker chosen by the most recent decision **for `t`** (empty string when none). Choose

    w = min { x in E(t) : x > c_t }   if that set is non-empty
    w = min E(t)                      otherwise

then set `c_t = w`. Decisions for other task types do not read or write `c_t`.

**Why per type (R1).** A single cursor shared across task types lets interleaved jobs drive one worker into every echo and another into every sleep. Each type's cursor moves only on that type's decisions, so the rotation among workers that support `t` is independent of the order in which other types arrive.

**Why worker ID and not an index.** The current `RoundRobinScheduler.SelectWorker` uses `lastIndex` modulo the eligible-list length (C5). When a worker joins or leaves, the index points at a different worker, so the rotation skews. A cursor over worker IDs is stable under joins and leaves: the successor of the cursor is well defined whatever the set is.

**Cursor storage.** Table `assignment_cursor` (Section 5.3) has one row per task type: `task_type` (primary key), `last_worker_id` (nullable, foreign key to `workers`, `ON DELETE SET NULL`), `updated_at`. A missing row is an empty cursor. The pass reads the rows for the task types in its batch at the start of the transaction, and upserts each changed row at its end, in the same transaction as the reservations. Consequences:

- **Restart.** A restarted coordinator reads each type's cursor from the database, so each rotation continues where it stopped. No in-memory state exists to lose.
- **Crash mid-pass.** The pass is one transaction, so either all its reservations and all changed cursors commit, or none do.
- **Several coordinators.** Only one pass runs at a time, enforced by `pg_try_advisory_xact_lock` (Section 2.1, Section 4.2). A second pass skips and does not block. No two passes write a cursor concurrently.
- **Cursor cleanup.** If a cursor's worker is deleted, the foreign key sets it to NULL and that type's rule starts from the lowest ID. Workers are not deleted in V1 (C9), so this is a corner case only.

**Join and leave.**
- A worker that joins with an ID after a type's cursor is picked up at its turn. One that joins with a smaller ID is picked up on the next wrap.
- A worker that leaves (UNHEALTHY, stops polling, or gets a reservation) drops out of `E(t)`. The cursor then selects the successor of its last value, which is the intended rotation.
- A worker that leaves while a cursor is on it does not stall the rotation: the successor rule applies to the cursor value, not to a position.

**Tie rules.** Worker IDs are unique (primary key on `workers.id`), so the sort has no ties. Candidates are taken in `created_at, id` order (Section 2.5), so the job order has no ties either.

**Pure function.** The rule is implemented as a pure function over a snapshot (cursor for `t`, sorted candidate IDs for `t`) → chosen ID. It has no clock, no I/O and no state beyond its arguments, so the property tests (Section 6.3) can drive it directly. It lives in a new file beside `internal/scheduler/round_robin.go`, which is left untouched so its unit tests keep passing.

### 2.4 One effective slot (R2)

**Rule.** While the worker loop is synchronous (C12), every worker has **one effective slot**. This is a named constant in the coordinator, `effectiveWorkerSlots = 1`, and its doc comment states that it is tied to the worker-concurrency slice (Q5). When that slice lands, the constant is removed and the rule becomes `total_slots - running - reserved`.

*Amendment (implementation notes, decision 4):* the constant is `domain.EffectiveWorkerSlots`, not a coordinator constant, because the assignment pass runs in the storage layer.


A worker holds at most one reservation, and it receives no reservation while it has a `PENDING` or `RUNNING` attempt.

**Why this is needed.** Revision 0 derived free slots from `worker_capacity.available_slots`. A worker that has just claimed one job still has `total_slots - 1` free slots, its poll is fresh for one window, and so the pass could reserve it more jobs. A synchronous worker never polls while it executes, so those reservations would lapse, one window each, and then go to fallback. The effective-slot rule removes that failure at its source.

**Accounting across one pass.** Each reservation makes that worker ineligible for the rest of the pass, in memory, so the one-slot rule holds within a pass as well as across passes. The pass decides against the eligible set at the moment of each decision (Section 2.3), and this is why the fairness statement in Section 2.6 is stated over decisions rather than over constant sets.

**Race with the claim (R2, found in review).** A plain snapshot read of `attempts` is not enough. The pass could read "no `RUNNING` attempt for `w`" while `w` is claiming a fallback job, and then reserve a job to `w` after that claim commits. The fix: before each reservation the pass takes `FOR UPDATE` on the worker row (with the bounded `lock_timeout` already used by the sweeper) and re-runs the eligibility checks. The claim takes the same worker row lock, so the two serialise. If the lock times out, the worker is skipped for the rest of this pass. Test INV-10b (Section 6.3) pins this.

**Claim preference.** A poll always tries its own live reservation before any fallback job (Section 3.2). Together with the rules above, a worker can never hold a reservation while it has a `RUNNING` attempt: the reservation is never created while a `RUNNING` attempt exists, and once it exists, the worker's poll takes it before anything else.

### 2.5 FIFO order within a type

Candidates are taken in `ORDER BY created_at ASC, id ASC`, the same order as `DispatchNextJob` (C1), per task type (Section 2.1). Consequences:

- Within a task type, jobs are reserved in submission order, and a worker's reservations are claimed in submission order.
- A job that cannot be placed (no eligible worker for its type) is **skipped**, not blocking. Jobs of other types are still reserved. Within one type, once `E(t)` is empty, the rest of that type's batch is left for the next pass; no job is passed over in favour of a newer one.

*Erratum (implementation notes, decision 1):* the candidate read also excludes a job whose `assignment_lapses` has reached `MINICLOUD_ASSIGNMENT_MAX_LAPSES`, because such a job leaves round-robin (D9). See the implementation notes at the end of this ADR.


### 2.6 Fairness and starvation

The first draft of this section stated "counts differ by at most 1 over `K` decisions with a constant `E`". That statement is **not** what the mechanism guarantees once a reservation removes a worker from `E(t)` (Section 2.4), so it is replaced by the statement below. The constant-set form is kept only as a corollary for the pure function.

**Statement S1 (no double selection while another eligible worker waits).** Fix a task type `t` and two decisions `d1 < d2` for `t` that both select worker `y`. Let `z ≠ y` be a worker that is eligible for `t` at every decision `d` with `d1 < d <= d2`. Then `z` is selected for `t` at some decision strictly between `d1` and `d2`.

*Argument.* After `d1` the cursor `c_t` is `y`. Each later decision for `t` selects the first eligible worker after the cursor, so the cursor moves forward around the ring of IDs and never jumps over a worker that is eligible at that decision. Suppose `z` is not selected in `(d1, d2)`. Then the cursor passes `z`'s position only if `z` was ineligible at each decision where it passed it, which contradicts the assumption. If `z` lies on the arc from the cursor at `d2 - 1` to `y`, then `z` is eligible at `d2`, so `d2` would have selected `z` (or someone before `z` on that arc), not `y`. Either way `z` must be selected in `(d1, d2)`. The argument uses only decisions for `t`, so other types cannot affect it.

**Corollary S2 (constant set).** If `E(t)` is the same set `S` for `K` consecutive decisions for `t`, with no member removed, then each member is selected `floor(K/|S|)` or `ceil(K/|S|)` times. This is the form INV-4 tests with the pure function. Under the pass it applies only where no reservation removes a member, so it is not a guarantee of the pass.

**Scope.**
- S1 holds per task type. Workers that support different task types get different shares. This is intended: round-robin fairness is among workers eligible for the same work.
- S1 counts **decisions**, not executions. A lapse is a decision followed by a non-execution. Lapses move the rotation on by design.
- S1 is an ordering guarantee, not a bound on the wait of a worker that is not continuously eligible.

**Starvation.** A worker that is eligible at every decision in an interval waits at most one selection of each other such worker before its own next selection (S1). A worker that never becomes eligible is not owed work.

**Idle worker while work waits.** The rule skips workers that are not eligible (Section 2.2): busy workers, workers with a reservation, and workers with a stale poll. A busy worker is never chosen over an idle polling one. The cost of a wrong choice is one accept window per lapse, bounded by the lapse limit (Section 3.3).

**Weighting.** Slots do not weight the rotation. Each eligible worker gets one turn per cycle, whatever `total_slots` says. Weighted round-robin is a V2 policy question (Q5).

**Latency at submit.** A submit triggers a pass immediately. The worst case without a trigger is one timer interval (1 s by default) plus the worker's poll interval (200 ms when idle, C12).

### Rejected alternatives for Section 2 (summary)
- Assignment inside the poll: rejected (it is Candidate B under another name).
- Assignment only at submit: rejected (no reaction to joins, leaves or freed slots; no `RETRYING` support).
- In-memory cursor (as `RoundRobinScheduler` does): rejected (lost on restart; wrong under several coordinators; index-based, see C5).
- One cursor for all task types: rejected (R1: interleaved types break per-type fairness).
- Cursor by index over the sorted list: rejected (rotation skews on join or leave).
- Free slots from `worker_capacity` minus reservations: rejected for V1 (R2: a claiming worker still reads as having free slots).
- Weighting by slots: not chosen (V2 question, Q5).

---

## 3. The accept window

### 3.1 Length, name and the timing argument (R6)

- Config: `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`, default **5 s**, allowed **5 s to 30 s**, validated at startup in `internal/config/config.go`. Out-of-range values are a startup error.
- **The floor is 5 s, not 2 s.** Revision 0 allowed 2 s. The worker can sleep up to 3 s between polls after an error (C17), so a 2 s window would lapse every time a worker backs off. The floor is therefore set above that maximum.
- **The constants the window depends on.** Confirmed by code reading (C16, C17, C18):
  - idle poll sleep: 200 ms (`baseBackoff` in `Run`);
  - maximum poll backoff: 3 s (`maxBackoff` in `Run`), the cap of `ComputeBackoffWithJitter`, which jitters into [0.5, 1.0) of the cap, so the largest sleep is 3 s, not "3 s with full jitter";
  - the poll HTTP client timeout: 15 s (`NewAgent`).
- **Requirement.** The window must exceed the maximum poll backoff plus a round trip, with margin. With the values above: `5 s > 3 s + round trip + margin`. The round trip is typically milliseconds. The design does **not** depend on the margin for correctness: a poll that arrives after the window lapses gets a 204 and the job is lapsed and counted (Section 3.3). Only the lapse cost rises.
- **Freshness gap.** `last_poll_at` is written on each successful poll, throttled to once per second (Section 2.2). The gap between writes while a worker polls is therefore `max(1 s, poll gap)`, not the sum of the two. The poll gap is at most the maximum backoff plus the round trip.
- **Named constants (planned prerequisite).** The values are locals in `Run` today (C16), so no test can read them. Slice 5c extracts them to package constants in `internal/worker/client.go`: an idle poll interval and a maximum poll backoff. Behaviour does not change. The test in INV-17 then pins the relation.
- The same window is the poll-freshness horizon (Section 2.2), so one knob governs both.

### 3.2 What the worker does on poll (R3)

A poll by healthy worker `W` with capacity tries the following in order. Both arms use the dispatchable gate of `DispatchNextJob` (C1, C20).

1. **Own reservation.** `assigned_worker_id = W AND assignment_expires_at > clock_timestamp()` and `task_type IN supported_tasks(W)`. Order `created_at, id`. Claim the first.
2. **Fallback.** If no own reservation was claimed, take the first job (order `created_at, id`) with:
   - `task_type IN supported_tasks(W)`;
   - no live reservation: `assigned_worker_id IS NULL OR assignment_expires_at <= clock_timestamp()`;
   - and **either** (a) `assignment_lapses >= MINICLOUD_ASSIGNMENT_MAX_LAPSES` (reason `lapses`), **or** (b) `dispatchable_since <= clock_timestamp() - MINICLOUD_ASSIGNMENT_FALLBACK_AGE` **and** the pass is silent **and** this poll's own refresh attempt has failed (reason `age`, below; N6).

   `dispatchable_since` is `GREATEST(created_at, COALESCE(not_before, created_at))`. It needs no new column.

   "The pass is silent" means `assignment_pass_state.last_completed_at <= clock_timestamp() - MINICLOUD_ASSIGNMENT_FALLBACK_AGE`. The pass writes that row as its last statement, in its own transaction, so a pass that fails or never runs leaves it stale.

**Refresh before the age arm (N1).** Before step 1, the poll reads `assignment_pass_state.last_completed_at`. If it is older than **2 × `MINICLOUD_ASSIGNMENT_INTERVAL`** (N6, further defect 25), and the rate limit below allows it, `PollWork` runs one assignment pass under `s.mu` before the claim. A successful refresh writes a fresh heartbeat, so the silence in step 2 is then below `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` and the age arm is closed for this poll. The own arm then sees the reservations that pass just made. So the age arm can be evaluated only after this poll's own refresh attempt has **failed** (N6). If the database is down, the refresh and the claim both fail, and no claim proceeds. A claim proceeds after a failed refresh only when the database answers and the pass does not, which is the stalled-pass case of FS-21.

This is the mechanism chosen for N1 in place of the suggested suppression, which was "suppress while a startup or outage grace window is active and until one pass has completed since process start". Two reasons. A window length is a guess about how long recovery takes, and a window shorter than the outage reopens the gap. A rule that waits for a completed pass would also suppress the age arm when a pass never completes after a restart, which is the FS-21 case. The refresh needs no clock and no in-memory state. The restart case is covered by the same rule: the first poll after a start refreshes a stale heartbeat before any age claim. Its cost is one failed pass attempt per poll while the pass fails with the database reachable (Section 6.2).

**Rate limit, single flight and the held outcome (N6).** Refresh attempts are single-flight and rate-limited. Three values are held in coordinator memory, read and written only under `s.mu`:
- `refreshInFlight` (bool): true while an attempt runs. Under `s.mu` at most one attempt can run, so this value makes the rule explicit and guards any later move of the refresh outside the mutex.
- `refreshLastAttemptAt` (process clock; zero before the first attempt).
- `refreshLastFailed` (bool): set by each attempt to that attempt's outcome. A success clears it; a failure sets it.

A poll runs an attempt only if the heartbeat is older than 2 × `MINICLOUD_ASSIGNMENT_INTERVAL`, no attempt is in flight, and either no attempt has run in this process or `now − refreshLastAttemptAt ≥ MINICLOUD_ASSIGNMENT_INTERVAL`. A poll that is stale but inside the rate window is skipped, and a skip changes none of the three values.

The age arm is open for a poll only if that poll ran the attempt in this call **and** `refreshLastFailed` is true after it. A rate-limited poll never claims by age, even when `refreshLastFailed` is true from an earlier attempt.

The threshold is 2 × INTERVAL, not 1 × INTERVAL. The timer pass refreshes the heartbeat every INTERVAL, so a healthy heartbeat is routinely about INTERVAL plus one pass duration old. A threshold of one INTERVAL would start refresh attempts in normal operation (further defect 25). Validation requires `FALLBACK_AGE > 2 × INTERVAL` (Section 5.5), so the age arm is never reachable while the heartbeat is inside the threshold.

State is process memory. A restart resets it to zero, so the first stale poll attempts. The rate limit uses the process clock because it throttles the coordinator's own work. The age-arm decision does not depend on that clock: it depends on this poll's attempt. A clock error can change how often attempts run, not whether an age claim follows a failed attempt.

**Why the stored flag alone is not used (N6, further defect 23).** The first poll after an outage does not always attempt. If the last failed attempt is less than INTERVAL old when the database returns, that poll is rate-limited. A rule that opened the age arm on `refreshLastFailed` alone would then claim aged jobs in FIFO order while the database is back and no pass has succeeded, which is the N1 gap. So the premise "the first poll after a restart or outage always attempts" holds after a restart (the state is zero), and after an outage only when no attempt ran in the last INTERVAL. The rule above does not depend on that premise.

**Argument: the N1 gap stays closed and FS-21 stays live (N6).**
1. An age claim by poll P needs P's own attempt in the same call, and that attempt must have failed. The claim also ran, so the database answered during that call. The only way an age claim follows a database answer without a successful refresh is a refresh that fails while the claim in the same call succeeds. That is a single-call blip, and it is the residual in Section 6.2.
2. After a restart, `refreshLastAttemptAt` is zero, so the first stale poll attempts. If the attempt succeeds, the age arm is closed.
3. After an outage, a poll that arrives after the rate window attempts. If the database has recovered, the attempt succeeds. A poll inside the window after a failure is skipped and cannot claim by age. So no age claim follows a recovery without a successful refresh first, except the blip in item 1.
4. Under a pass that fails persistently while the database is reachable, each interval has one attempt. The attempting poll fails. If its silence is at least `FALLBACK_AGE` and it supports the type of an aged job, it claims one job by the age arm. So the age arm keeps firing, at most once per interval. The bound is in FS-21 (Section 6.1).
5. If the loop has stopped but the pass works, the attempt is a successful pass: the heartbeat is refreshed, the age arm closes, and round-robin resumes.

**Why the age arm is gated on pass silence (refinement found in review).** Revision 0 made the age arm unconditional. Under a healthy, saturated queue every job waits more than 30 s, so the unconditional rule would turn the whole backlog into FIFO fallback and quietly turn `round_robin` into `fifo_pull`. Gating on pass silence keeps the age arm for the case it exists for: the pass is not running. Test INV-16 pins this.

**Response.** The 200 body is unchanged except for an additive `policy` field (`round_robin`, `round_robin_fallback`, or `fifo_pull` under that policy) and, on a fallback claim, `fallback_reason`. Otherwise the worker sees the same `assignment` as today: lease token, lease expiry and duration (protocol Section 6.2).

**Poll with nothing to claim.** 204 `NO_QUEUED_JOBS`, meaning "no job reserved to you and no fallback job". The worker's existing 200 ms sleep applies.

**Poll from an UNHEALTHY worker.** 503 `WORKER_UNHEALTHY`, unchanged.

**Fallback decision (D9, Q2).** After `L` lapses, a job is no longer round-robin-assigned, so the round-robin guarantee for that job is traded for a bounded wait. The age arm is a liveness backstop for a stalled pass. Both are logged as `round_robin_fallback`, and both are visible on the job view: `assignment_lapses` is always there, and the new `assignment_fallback_reason` field (Section 5.1) records which arm fired.

Why the claim is a single transaction that uses the existing dispatch core: it is `DispatchNextJob` with a different candidate predicate (own, then fallback), plus clearing of the assignment columns, `assignment_fallback_reason` on fallback, and `last_poll_at` maintenance. The shared core keeps the lock order and capacity re-check identical to the path that already passes the existing tests.

### 3.3 What happens when the window lapses

1. The next pass finds `assignment_expires_at <= clock_timestamp()` on an assignment whose worker is `HEALTHY`. The claim predicate has already stopped treating it as reserved to `w` (Section 1.3).
2. The pass clears the assignment and increments `assignment_lapses`, unless a grace window is active (Section 4.4). It writes `JOB_ASSIGNMENT_LAPSED`.
3. The same pass, if it reaches the job, assigns it to the **next** eligible worker. The cursor for the type was left on the lapsed worker, so the successor rule moves past it. "Next worker" therefore needs no per-job memory of tried workers. If the lapsed worker is the only eligible worker, it is selected again; each such selection costs one window and one counted lapse.
4. Each lapse costs one window plus at most one pass interval. After `L` lapses (default 3) the job becomes fallback-claimable (Section 3.2).

**Counting.** Lapses are per job, reset to 0 on any successful claim. They are separate from `attempt_no` and `max_attempts`.

**Poison-by-lapse loop.** Without a bound, a job could cycle forever between reservation and lapse if every chosen worker is alive but never polls. No attempt is created, so `max_attempts` does not apply. The lapse limit is the bound. Worst case per job before fallback, with a live pass and defaults: `L x (window + interval)` = 3 x (5 s + 1 s) = 18 s, plus up to one poll gap (about 3 s) to claim. Test INV-7b proves the bound.

### 3.4 When the assigned worker dies or deregisters

- **Dies (heartbeats stop).** The reaper marks it `UNHEALTHY` (C10). The next pass revokes its reservations at once (`JOB_ASSIGNMENT_REVOKED`), **without** incrementing lapses. The job is then reassigned to a healthy worker. A dead worker does not push a job toward fallback. If the pass itself is dead, the revoke does not happen; the job then waits for the age arm or for a healthy poller (Section 3.2, FS-21).
- **Deregisters.** There is no deregistration API (C9). If one is added, it must set `UNHEALTHY` or a new terminal status, and the same revoke path applies. Q11: no `DRAINING` path in V1.
- **Slow (heartbeats fine, no poll).** The window lapses and the lapse is counted. This is the case the lapse counter exists for.

### 3.5 Late accept after reassignment (stale token handling)

Assignment never issues a token; the claim does. The possible late accepts are:

| Situation | Claim result | Why |
|---|---|---|
| Old worker `W1` polls after its window lapsed, job not yet revoked, age and lapse arms not met | 204 | Its own arm requires `assignment_expires_at > clock_timestamp()`, and the fallback arm is not met. |
| Old worker `W1` polls after its window lapsed, and a fallback arm **is** met | claims as fallback | A healthy worker may claim a fallback job. The token is issued by that claim, to `W1`, so no stale token exists. |
| Old worker `W1` polls after revoke, job reserved to `W2` | 204 | `W2`'s reservation is live, so `W1` is neither the reserved worker nor a fallback. |
| `W1` polls while the pass holds the job lock | 204 | The claim uses `FOR UPDATE SKIP LOCKED` (as today), so it skips the locked row. Safe: the poll retries after 200 ms. |
| `W1` polls after its claim committed | 204 | The job is `RUNNING` with `W1`'s attempt; `W1` already has the token from the first response. |

So no "stale token" exists to reject: a token is only ever given to the worker that claimed, and the claim is the only issuer. Any later commit or renewal by `W1` with that token goes through the unchanged attempt checks (ADR 0014 Section 5, protocol Section 6.3).

### 3.6 Idempotency of poll retries

A poll is **not** idempotent, and this design does not make it so.

- If the claim commits and its response is lost (network drop, worker crash), the job is `RUNNING` with an attempt whose token the worker never received. The worker's retry gets 204. The attempt is then reaped by the lease sweeper to `LOST` after `lease_expires_at` (30 s by default), and it **consumes one attempt** (Section 1.4). This is FS-16 in Section 6.1.
- Storing the raw token to make the retry return it would contradict ADR 0011 Section 5 (only hashes are stored). A client-generated `poll_id` stored on the attempt would let the coordinator recognise a retry, but it could not return the token, so it would still lose the attempt. It was rejected.
- A two-step accept (poll returns the assignment without claiming, then `POST` accept claims it) would not remove the lost-response case either. It also adds a state with no lease and a new endpoint. Rejected for V1.

**Decided (Q3):** a lost claim response consumes `max_attempts`, the same as `fifo_pull` today. The cost is recorded in Section 8 and in the report.

### Rejected alternatives for Section 3 (summary)
- Two-step accept endpoint: rejected (same lost-response hole; extra state and endpoint).
- Storing the token so a poll retry can return it: rejected (contradicts ADR 0011 Section 5).
- Unbounded reassignment: rejected (poison-by-lapse loop, see Section 3.3).
- Lapses counted as attempts: rejected (a lapse is not an execution; a slow poll would exhaust `max_attempts` without any task having run).
- Unconditional age fallback: rejected (R3 refinement: a saturated healthy queue would become FIFO fallback).
- Age fallback from a fixed `created_at` with no pass gate: rejected for the same reason.

---

## 4. Concurrency

### 4.1 One coordinator

`PollWork` holds the service mutex `s.mu` for the whole claim (C4). The assignment pass and the sweeper run under the same mutex in the single-coordinator build, so the pass and the claim never interleave inside one process. The poll-time refresh (Section 3.2) runs under the same mutex. The repeat yield (Section 2.1) releases it. A pass delays a poll by at most one batch (Section 2.1). Correctness does not rely on the mutex alone: each path also takes database row locks and uses state-guarded updates, so the design stays correct if the mutex is later removed.

### 4.2 Several coordinators (stance)

Design stance: **the database is the only correctness boundary.** The mutex is a performance measure, not a guarantee.

| Hazard | Protection |
|---|---|
| Two claims take the same job | `FOR UPDATE SKIP LOCKED` on the candidate and the state predicate in the `UPDATE`; the partial unique index `idx_attempts_active_per_job` is the backstop. |
| Two passes write cursors or double-reserve a job | `pg_try_advisory_xact_lock` with a fixed key, taken first in every pass. The migrator takes a blocking `pg_advisory_xact_lock` (`internal/storage/postgres/migrator.go`), but a pass must not wait on it. A second pass returns `skipped` at once and writes nothing (Section 2.1, INV-22). |
| Pass reserves a job while a claim or cancel holds it | The pass locks candidates with `FOR UPDATE SKIP LOCKED` and skips locked rows. |
| Pass reserves a worker while a claim of that worker is in flight | The pass takes `FOR UPDATE` on the worker row, bounded by `lock_timeout`, and re-runs the eligibility checks under it (Section 4.3). The claim takes the same row lock. |
| Revoke races a claim | Both take the job row lock; the claim's predicate decides the winner. |

Multi-coordinator operation is **not claimed as tested** by this design (Q9: not in V1). It is designed for and is safe by inspection of the locks, but no test asserts it.

### 4.3 Lock order

ADR 0013 and ADR 0014 fix the canonical order `jobs -> workers -> attempts -> results -> job_events`. The new paths follow it:

| Path | Lock sequence | Notes |
|---|---|---|
| Advisory pass lock | `pg_try_advisory_xact_lock(key)` first. A false result means skip, with no further statement (N3). | Not a row lock, and never waited on. Taken before any row lock, so no cycle with row-lock holders. |
| Assignment pass: revoke | `jobs` (`FOR UPDATE SKIP LOCKED`, `LIMIT B`), then `workers` read (no lock) | Writes `jobs`, `job_events`. |
| Assignment pass: reserve, per type | `jobs` (candidates, `SKIP LOCKED`, `LIMIT B`); then per reserved worker `workers` (`FOR UPDATE`, `lock_timeout`), then a fresh read of `attempts` for that worker, then the eligibility re-check; then `jobs` update and `job_events` insert | Lock order is `jobs -> workers`, same as the claim. On a lock timeout the worker is skipped for the rest of the pass. |
| Assignment pass: cursors and heartbeat | `assignment_cursor` upserts, then `assignment_pass_state` update, last | Written only under the advisory lock. |
| Poll-time refresh (Section 3.2, N1) | The same sequence as the pass above, in its own transaction, then the claim in its own transaction. | Nothing is held between the two. Both run under `s.mu`. The refresh takes no lock the claim does not take in the same order. At most one refresh is in flight; `refreshInFlight` makes that explicit (N6). |
| Claim (poll 200) | `jobs` (`SKIP LOCKED`) then `workers` (`FOR UPDATE`) then `attempts` insert, `job_events` insert | Identical to `DispatchNextJob` today. |
| Poll 204 freshness write | `workers` only, own transaction | Holds nothing else, so it cannot close a cycle. |
| Cancel of queued or retrying job | `jobs` then `job_events` | Clears assignment in the same statement. |
| Sweeper (unchanged) | `jobs` (`SKIP LOCKED`), `workers`, `attempts`, `results`, `job_events` | Unchanged, ADR 0014 Section 4. |

The pass takes `FOR UPDATE` on `workers`, so it can wait on a claim holding that row. The wait is bounded by `lock_timeout`, the same mechanism the sweeper uses (`lockTimeoutSetting` in `internal/storage/postgres/leases.go`). The pass never waits on a job. The pass never locks `attempts`. The sweeper never locks `assignment_cursor` or `assignment_pass_state`.

The claim reads `assignment_pass_state` (for the age arm) without a lock. A plain read takes no lock, so it adds no edge to the order.

Revision 0 said the pass read workers with no lock and never touched `attempts`. That was unsafe for R2 (Section 2.4 race), and it is corrected here.

### 4.4 Crash points, recovery and grace

| Crash or fault point | State after | Recovery |
|---|---|---|
| Before the pass commits | Nothing changed (one transaction). | Next pass redoes the decisions. |
| After the pass commits, before anything else | Reservations, cursors and pass heartbeat durable. | Workers poll; windows run on database time; expiry is handled by a later pass. |
| During a claim, before commit | Nothing changed. | Worker's poll gets an error; retries. |
| After claim commit, response lost | `RUNNING` attempt, no executor. | Lease expires; sweeper reaps to `LOST`; retry consumes one attempt (Section 3.6). |
| Coordinator restart | Cursors, reservations, lapse counts and pass heartbeat durable. Grace state in memory is lost. | Startup grace (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30 s) starts on the first tick and gates lapse counting only. The first poll refreshes a stale heartbeat before any age claim (N1, Section 3.2). See below. |
| Database outage | Passes fail; the heartbeat goes stale. Expiry continues on database time. | Outage grace (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30 s) opens on the first successful tick after the failure (ADR 0014 Section 4) and gates lapse counting only. Polls also fail during an outage. On recovery the first poll refreshes a stale heartbeat before any age claim (N1, Section 3.2). A poll inside the rate window after a failed attempt is skipped and takes no age claim (N6). |
| Assignment pass stalled (bug, repeated error, or the pass loop stopped) | No new reservations; heartbeat goes stale; expired reservations are not revoked. | Age arm after `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` of silence, once the poll's refresh has failed (Section 3.2, FS-21). |
| Pass errors repeatedly with the database reachable | Refresh attempts fail, at most one per `MINICLOUD_ASSIGNMENT_INTERVAL` (N6). | The poll whose attempt fails may claim one aged job by the age arm (FS-21). Other polls get 204 until the next attempt. The cost is bounded in Section 6.2 (further defect 21). |
| A second pass holds the advisory lock (not claimed for V1 across coordinators; possible with a long transaction) | The second pass skips and writes nothing. | The holder's commit refreshes the heartbeat. The next timer tick retries (Section 2.1, INV-22). |

**Grace applies to lapse counting, not to revocation.** Two rules:

- During an active grace window (startup or outage), an expired assignment is still **revoked** (so the job is not stuck), but the lapse is **not counted** (`counted: false`). A worker that could not poll because the control plane was down must not be pushed toward fallback by it.
- Outside grace, a lapse counts only for a `HEALTHY` worker (Section 3.3).
- Grace never gates the age arm. The poll-time refresh does that (Section 3.2, N1).

The grace state is passed into the pass as an input value, exactly as `SweepExpiredLeases` receives `domain.SweepGraceState`. The pass neither owns nor advances it. The only authoritative check is inside the pass transaction, under the job locks.

**Why revoke during grace and not freeze the assignment.** Freezing would keep a job reserved to a worker that may be gone for the whole grace period (up to 30 s), which is a latency cost with no safety benefit, since no attempt exists to protect.

### 4.5 Operator note: stale pass heartbeat (A2)
While the pass heartbeat is stale, meaning `assignment_pass_state.last_completed_at` is older than `2 × MINICLOUD_ASSIGNMENT_INTERVAL` (the threshold of Section 3.2), the coordinator logs at ERROR at most once a minute. The documented remedy is to set `MINICLOUD_DISPATCH_POLICY=fifo_pull` and restart the coordinator. Under `fifo_pull` the pass does not run (Section 5.2), so a stalled pass no longer affects dispatch. The rate-limited ERROR log is implemented in slice 5c (Section 7). Operator-facing docs are owned by the docs task (Section 7).

---

## 5. Compatibility

### 5.1 Protocol changes

All changes are additive under the `/v1` rule (protocol Section 5.1). No endpoint is added or removed. No error code is added.

| Surface | Change | Exact form | Status codes |
|---|---|---|---|
| `POST /v1/workers/{worker_id}/poll` 200 | Additive field in `assignment`. | `"policy": "round_robin"`, `"round_robin_fallback"`, or `"fifo_pull"`. On a fallback claim, `"fallback_reason": "lapses"` or `"age"`. | unchanged (200) |
| `POST /v1/workers/{worker_id}/poll` 204 | Meaning narrowed: no job reserved to this worker and no fallback job. Wire form unchanged. | `NO_QUEUED_JOBS` | unchanged (204) |
| `POST /v1/workers/{worker_id}/poll` 503 | Unchanged. | `WORKER_UNHEALTHY` | 503 |
| `GET /v1/jobs/{job_id}` | Additive fields on the job view (`domain.Job` JSON). | `assigned_worker_id` (nullable), `assignment_expires_at` (nullable), `assignment_lapses` (integer, 0 when none), **`assignment_fallback_reason` (nullable: `lapses` or `age`)**. | unchanged |
| `POST /v1/jobs` 201 | Additive fields (note 5c-2.4). The 201 body is the same job view as `GET /v1/jobs/{job_id}`, so it carries the four fields above at their unset values. | `assigned_worker_id: null`, `assignment_expires_at: null`, `assignment_lapses: 0`, `assignment_fallback_reason: null`; never `assigned_at`. | unchanged (201) |
| `POST /v1/workers/{id}/heartbeat`, `/renew`, `/result`, `POST /v1/jobs/{id}/cancel` | No change. | | unchanged |

**Note on the fourth job-view field.** Q10 approved "the additive fields" and Q2 asks that fallback be visible on the job view. The three fields named in revision 0 cannot show an age-based fallback, so `assignment_fallback_reason` is added. It is the one field not named in revision 0. The orchestrator should confirm it (Section 8, remaining question R-a).

Worker-side: the current worker decodes the poll response by field name, so it ignores the new `policy` and `fallback_reason` fields. No worker change is needed for correctness, except the named-constant extraction in Section 3.1.

### 5.2 `fifo_pull` stays selectable

- `MINICLOUD_DISPATCH_POLICY` accepts `round_robin` and `fifo_pull`. Default is `round_robin` (flipped in P4-17, note 37; before that it was `fifo_pull` until the gate was passed; Q1: flipped in its own small reviewed change after the audit, not inside slice 5c).
- The policy selects the **claim predicate and whether the pass runs**. Under `fifo_pull` the pass does not run, and `DispatchNextJob` is used unchanged except for one addition: its `UPDATE` clears the assignment columns (Section 5.3), so a policy switch cannot leave a non-NULL assignment on a `RUNNING` job (rejected by the CHECK).
- A switch from `round_robin` to `fifo_pull` leaves existing reservations in place. `fifo_pull` ignores them (INV-12), so they are claimed or cancelled like any queued job, and the job view shows the old `assigned_worker_id` until then. This is harmless and is not cleaned up in V1. The reservations expire on database time and are never acted on by any pass.
- `fifo_pull` is kept for three reasons: it is the Phase 3 baseline; ADR 0011 requires identical FIFO pull semantics for the V2 Redis comparison; and it is the rollback path.
- Log lines differ by policy, as ADR 0011 requires: `fifo_pull` logs `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"` and nothing about a scheduler. The round-robin pass logs its candidates and selection, because the scheduler is making the decision there.

### 5.3 Migration `000005` (forward-only, ADR 0010)

Name: `000005_assignment_round_robin.sql`, in `internal/storage/postgres/migrations/` beside `000004_leases_retries_cancellation.sql`. Exact DDL sketch. The NOT VALID then VALIDATE pattern follows `000003` and `000004`.

```sql
-- 000005_assignment_round_robin.sql (proposed; not in tree)

-- Step 1: assignment columns on jobs. All nullable: existing QUEUED and RETRYING rows
-- become unassigned, which is the correct starting state.
ALTER TABLE jobs
    ADD COLUMN assigned_worker_id          TEXT REFERENCES workers(id) ON DELETE SET NULL,
    ADD COLUMN assigned_at                 TIMESTAMPTZ,
    ADD COLUMN assignment_expires_at       TIMESTAMPTZ,
    ADD COLUMN assignment_lapses           INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN assignment_fallback_reason  VARCHAR(16);

-- Step 2: the three assignment columns are all set or all NULL; a non-NULL assignment is
-- only valid on a job waiting for dispatch; lapses are never negative; the fallback reason
-- is one of two values.
ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_assignment_shape CHECK (
        (assigned_worker_id IS NULL AND assigned_at IS NULL AND assignment_expires_at IS NULL)
        OR (assigned_worker_id IS NOT NULL AND assigned_at IS NOT NULL
            AND assignment_expires_at IS NOT NULL
            AND assignment_expires_at >= assigned_at
            AND state IN ('QUEUED', 'RETRYING'))
    ) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_assignment_shape;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_assignment_lapses CHECK (assignment_lapses >= 0) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_assignment_lapses;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_fallback_reason CHECK (
        assignment_fallback_reason IS NULL OR assignment_fallback_reason IN ('lapses', 'age')
    ) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_fallback_reason;

-- Step 3: poll freshness. NULL means never polled, so the worker is not assignable.
ALTER TABLE workers
    ADD COLUMN last_poll_at TIMESTAMPTZ;

-- Step 4: per-task-type round-robin cursors (R1). A missing row is an empty cursor.
-- Written only by the assignment pass, under the advisory lock.
CREATE TABLE assignment_cursor (
    task_type      VARCHAR(64) PRIMARY KEY,
    last_worker_id TEXT REFERENCES workers(id) ON DELETE SET NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Step 5: the pass heartbeat (R3). One row. Written by the pass as its last statement,
-- written by a pass as its last statement, only when the pass completes. A failed,
-- stopped or skipped pass leaves it stale. The fallback age arm and the poll-time
-- refresh (Section 3.2) both read it.
CREATE TABLE assignment_pass_state (
    singleton         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_completed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO assignment_pass_state (singleton) VALUES (TRUE);

-- Step 6: indexes.
-- Pass scan (R4): unassigned QUEUED and RETRYING jobs of one type, in created_at, id order.
-- idx_jobs_task_dispatch (000001) covers QUEUED only, and nothing covers RETRYING by type (C19).
CREATE INDEX idx_jobs_dispatch_by_type
    ON jobs (task_type, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NULL AND state IN ('QUEUED', 'RETRYING');

-- Assigned-job lookup for the claim's own arm.
CREATE INDEX idx_jobs_assigned_worker
    ON jobs (assigned_worker_id, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NOT NULL;

-- Expiry scan for the revoke step.
CREATE INDEX idx_jobs_assignment_expiry
    ON jobs (assignment_expires_at ASC)
    WHERE assigned_worker_id IS NOT NULL;
```

Notes on the sketch:
- `job_events` needs no DDL (C8). The new event types are Go constants only.
- No change to `chk_jobs_state`. The `REJECTED` gap (C7) is pre-existing and is raised as Q8, not fixed here.
- No change to `chk_attempts_state` or to any attempt index.
- The fallback claim's predicate (Section 3.2) reads `assignment_lapses` and `dispatchable_since` across task types. The plan for that query is **not verified**. It is checked with `EXPLAIN` in slice 5b. The per-type index above is for the pass and is the only index this design relies on for the pass scan.
- `ON DELETE SET NULL` on `assigned_worker_id` matches ADR 0011 Section 2.4. Workers are not deleted in V1, so it is a backstop.
- The shape CHECK is the enforcement that every state-changing path clears the columns. Mutation INV-2b (Section 6.3) proves a path that forgets is caught.
- N1 to N3 change no DDL. The advisory key is a constant in code, not a column. A skipped pass (Section 2.1) writes nothing, so it needs no column.

**Migration test.** A `migration_000005_test.go` (proposed) should follow the shape of the existing `migration_000004_test.go`. Check first whether any test pins the latest migration number (for example in `migrator_test.go`); I did not verify this and it must be checked in slice 5b.

### 5.4 Existing tests and in-memory parity

- **Default policy in tests.** `NewService` and the repositories default to `fifo_pull` behaviour (the claim predicate and pass are opt-in). Existing coordinator and API tests therefore run unchanged. The default changes only in `cmd/coordinator/main.go` configuration after the gate (Q1).
- **Existing dispatch tests.** `DispatchNextJob` changes by one clause (clearing the assignment columns), which is a no-op when they are NULL. The existing skip-locked test `TestPostgresRepository_DispatchNextJob_SkipsLockedHead` (in `internal/storage/postgres/dispatch_skiplocked_test.go`) is the regression gate for this change.
- **Scheduler tests.** `internal/scheduler/round_robin_test.go` tests the index-based `RoundRobinScheduler`, which is unchanged.
- **Coordinator constructors.** `NewService` keeps its `scheduler.Scheduler` parameter. The new pure function is not reached through that interface.
- **In-memory parity.** Every behaviour in this ADR is implemented twice: in `internal/storage/inmemory.go` (with the injected clock, a mutex instead of locks, and `jobOrder`, which equals creation order, so FIFO ties agree) and in the Postgres repository. Parity is enforced by shared conformance tests in `internal/storage/storagetest/conformance.go` run against both repositories, as the earlier Phase 4 slices do for leases. The per-type cursor map, the pass heartbeat and the batch limit exist in both. The try-lock skip (the in-memory pass models it with a non-blocking `TryLock`), the repeat rule with its yield, and the poll-time refresh exist in both.
- **Known parity difference (existing, not created here).** Postgres orders by `created_at, id`; memory orders by insertion. They agree when `created_at` is strictly increasing. This is recorded so the conformance tests do not assume tie behaviour.

### 5.5 Configuration: single source of truth (N7)

Every new variable, with its default, allowed range, validation rule and the section that derives it. Startup fails on any rule violation. `MINICLOUD_DISPATCH_POLICY` is listed because it selects the policy; it already exists.

| Variable | Default | Allowed range | Validation rule (startup error otherwise) | Derived in |
|---|---|---|---|---|
| `MINICLOUD_DISPATCH_POLICY` (existing) | `round_robin` (was `fifo_pull` until P4-17, note 37) | `round_robin`, `fifo_pull` | One of the two values. The default flips only in its own change (Q1). | D12, Section 5.2 |
| `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` | 5 s | 5 s to 30 s | In range. Must exceed the maximum poll backoff (3 s) plus margin (INV-17). | D6, Section 3.1 |
| `MINICLOUD_ASSIGNMENT_INTERVAL` | 1 s | 100 ms to 10 s (proposed, N7) | In range. At least `MINICLOUD_ASSIGNMENT_MIN_YIELD`. Less than half of `MINICLOUD_ASSIGNMENT_FALLBACK_AGE`. It sets the timer and the refresh rate limit, so at most `1/INTERVAL` attempts per second; the 100 ms floor caps that at 10 per second. | Section 2.1, Section 3.2 (N6), Section 6.2 |
| `MINICLOUD_ASSIGNMENT_MAX_LAPSES` | 3 | 1 to 10 | In range. | D9, Section 3.3 |
| `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` | 30 s | At least `MAX_LAPSES × (ACCEPT_WINDOW + INTERVAL)` (18 s by default) and at most 10 min (proposed, N7) | At least the lapse-path minimum, and greater than `2 × MINICLOUD_ASSIGNMENT_INTERVAL` (N1, N6). | D9, Section 3.2, FS-21 |
| `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` | 50 | 1 to 500 | In range. | D14, Section 2.1 |
| `MINICLOUD_ASSIGNMENT_MIN_YIELD` | 10 ms | 1 ms to 1 s | In range. Not above `MINICLOUD_ASSIGNMENT_INTERVAL` (N2). | Section 2.1 |

The values are proposals, not measurements (R-d, slice 5c).

**Startup cross-validation between variables (A1).** The per-variable rules above do not catch a combination that breaks the timing argument. Startup fails, with a message naming both variables, unless both of these hold:

- `MINICLOUD_ASSIGNMENT_INTERVAL` does not exceed `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`;
- `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` is greater than `2 × MINICLOUD_ASSIGNMENT_INTERVAL` plus `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`.

These are checked when the configuration is loaded, so they are implemented in slice 5a, because Section 7 places config validation in 5a. A1 adds no invariant to Section 6.3, and this amendment assigns no test name; slice 5a adds its own config tests. With the defaults both hold (`1 s` does not exceed `5 s`; `30 s` is greater than `2 × 1 s + 5 s = 7 s`).

---

## 6. Failure and abuse model

### 6.1 New failure rows for [docs/failure-model.md](../failure-model.md)

These are proposed text for the failure model. This ADR does not edit that file.

| ID | Fault | Expected behaviour | Recovery | Scope |
|---|---|---|---|---|
| **FS-14** | Assigned worker is alive but never polls | Window lapses; lapse counted (outside grace); job reassigned to the next eligible worker. No attempt created. | Automatic; after `L` lapses the job becomes fallback-claimable. | Slice 5b and 5c |
| **FS-15** | Assigned worker dies before polling | Reaper marks `UNHEALTHY`; pass revokes without counting a lapse; job reassigned. | Automatic, within one pass interval after the reaper tick **while the pass runs**. If the pass is stalled, see FS-21. | Slice 5b and 5c |
| **FS-16** | Claim committed; poll response lost | Attempt `RUNNING` with no executor; lease expires; sweeper reaps to `LOST`; consumes one attempt (Q3). | Automatic, bounded by `max_attempts`. | Existing lease path, now reachable through claim |
| **FS-17** | Coordinator crashes between assignment commit and claim | Reservation durable; expiry on database time; grace suppresses lapse counting on restart. | Automatic. | Slice 5c |
| **FS-18** | Cancel races with claim of a reserved job | Row lock serialises. Either `CANCELLED` and claim returns 204, or the claim wins and cancel sees `RUNNING` (`CANCEL_REQUESTED`). | Automatic. | Slice 5b |
| **FS-19** | Assignment lapse loop (every chosen worker lapses) | Bounded by `L`; then fallback. | Automatic after `L` windows. | Slice 5c |
| **FS-20** | Database outage during pass | Pass fails; the pass heartbeat goes stale; reservations expire on database time; lapses not counted during grace. | Automatic. | Slice 5c |
| **FS-21** | Assignment pass stalled (bug, repeated error, or loop stopped) while workers poll | No reservations; expired reservations are not revoked; no lapses counted. The age arm (Section 3.2) lets the one poll per interval whose own refresh attempt failed claim one aged job, once the job has been dispatchable and the pass has been silent for `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` (N1, N6). Other polls get 204 in that interval. | Automatic. Under a persistently failing pass, age claims come at most one per attempt, so the k-th aged, claimable job in FIFO order waits at most `FALLBACK_AGE + k × INTERVAL` plus one poll gap (about 3 s), counted from the silence beginning (further defect 24). With the defaults, 30 s + k × 1 s + about 3 s. A stalled loop with a working pass is not this case: the attempt runs the pass and round-robin resumes (Section 3.2, item 5). The bound holds only while an attempting poll supports the type. Attempts rotate over polling workers, so it holds while some healthy worker that supports the type keeps polling. | Slice 5c |
| **FS-22** | Poll round trip exceeds the window margin (Section 3.1) | The worker's poll arrives after the window; the job lapses and is counted (outside grace). Correctness is unaffected. | Automatic. Cost: one lapse per occurrence. | Slice 5c |
| **FS-23** | Coordinator restarts, or the database is down for longer than `MINICLOUD_ASSIGNMENT_FALLBACK_AGE`, and polls resume before a pass has completed (heartbeat stale) | Without the refresh, the age arm would claim aged jobs in FIFO order before the first pass reserves them, so round-robin is skipped. With the refresh (Section 3.2), the poll's own pass runs first, and the age arm closes when it succeeds. | Automatic. A poll inside the rate window after a failed attempt gets 204 until the next attempt, at most `MINICLOUD_ASSIGNMENT_INTERVAL` later (N6). Cost: one pass duration on the attempting poll (Section 2.1, `D`). | Slice 5a (memory), slice 5b (Postgres), slice 5c (loop) |
| **FS-24** | Deep backlog with full batches | Without a yield, the loop would hold `s.mu` back to back and starve polls, and a full batch with no reservation would spin. With the repeat rule (reservation required) and `MINICLOUD_ASSIGNMENT_MIN_YIELD`, each repeat follows a released mutex. | Automatic. A poll waits at most one pass duration `D` (not measured, Section 2.1). | Slice 5c |

### 6.2 Abuse cases

Claims are unauthenticated in V1 (SEC-004, accepted gap). The cases below are only those the new design introduces or changes. Existing SEC-004 consequences are **not** re-reported here.

| Case | Effect | Bound or mitigation |
|---|---|---|
| A worker never polls, to waste reservations | Each reserved job waits one window per lapse. | `L` lapses then fallback (Section 3.3). Worst wait per job about 21 s with defaults (18 s of lapses plus one poll gap). |
| A worker polls constantly to stay eligible and to win every decision | Its share of decisions equals every other eligible worker's (Section 2.6), no more. | Round-robin gives equal turns, not more. |
| A client registers many worker IDs (sybil) to take a larger share of jobs | Each fake worker that polls gets equal decisions. | SEC-004 accepted gap (unauthenticated registration). Not mitigated in this design. Recorded so the auth work (Phase 7) knows about it. |
| A worker registers with a large `total_slots` to look attractive | Ignored in V1: the one effective slot (Section 2.4) means `total_slots` does not change turns or reservations. | Per-worker turns, bounded by the existing `total_slots` limit (SEC-016 CHECK). |
| A worker polls on behalf of another worker ID | It can claim that worker's reservations. | Same root cause as SEC-004. Accepted gap. Not made worse by this design: today any poller claims any queued job. |
| Poll flood to force writes to `workers` | Each 204 may write `last_poll_at`. | Throttled to one write per worker per second (Section 2.2). |
| Poll flood to hold worker row locks | A claim holds the worker row briefly; the pass waits at most `lock_timeout`. | Bounded wait; a timed-out worker is skipped for that pass (Section 4.3). |
| Poison payload that repeatedly crashes its worker | Each crash is a `LOST` attempt, bounded by `max_attempts` (unchanged). | ADR 0015 Section 2. Assignment adds no new path to it. |
| Coordinator-level flood of submits triggering passes | Each submit triggers a pass. | Each pass reads and reserves at most `B` jobs per type and revokes at most `B`, serialised by the advisory try-lock (D14). A submit storm delays passes but cannot start unbounded work. A skipped pass is not a lost trigger: the next timer tick (`MINICLOUD_ASSIGNMENT_INTERVAL`) covers it (N3). |
| Event flood: a pass with many eligible workers writes large payloads | Each `JOB_ASSIGNED` carries the candidate list. | Cap of 10 IDs plus a count (Section 2.1, INV-18). |
| Poll flood while the pass fails with the database reachable (the N1 refresh) | Refresh attempts are single-flight and rate-limited to one per `MINICLOUD_ASSIGNMENT_INTERVAL` (N6). Each poll inside the window is skipped. | Attempts per second are bounded whatever the poll rate, and the cost table below gives the round trips. The rate limit does not reopen the N1 gap, because only the attempting poll can claim by age (Section 3.2). Residual: a refresh that fails while the claim in the same call succeeds (a single-call database blip) allows one age claim per such call. To measure in slice 5c, together with `D` against `I` (further defect 21, closed by N6). |

**Cost of a persistently failing pass (N6).** Notation: `I` = `MINICLOUD_ASSIGNMENT_INTERVAL` (default 1 s); `T` = task types in a pass; `W` = reservations in a pass, at most one per eligible worker (Section 2.4); `D` = one pass duration; `V` = expired reservations a pass lapses. The worst case per attempt is a pass that fails at its last statement, so it costs as much as a full pass. Measured on PostgreSQL with `T = 1` and `B = 50` (note 35, `P4-08f-evidence/measure-5c3.log`): an idle pass takes 7 round trips, each reservation about 7 more (81 for `W` = 10, 360 for `W` = 50) and each lapse about 2 more (101 for `W` = 10 with `V` = 10, 460 for `W` = 50 with `V` = 50). The bound is therefore `7 + 8W + 2V` round trips (87, 407, 107 and 507 for those four runs, each above the measurement), where `W` is at most `B` per task type per pass. The earlier figure `6 + 2T + 4W` was derived from the pass structure and was too low (P4-08f audit); the per-type term for `T` > 1 was not measured.
- **Attempts per second:** at most `1/I` from polls (1.0 per second by default), however many workers poll, because attempts are single-flight and rate-limited. The timer pass adds at most `1/I` more.
- **Round trips per second from refresh attempts:** at most `(7 + 8W + 2V)/I`. With `T = 1`, 10 polling workers (`W ≤ 10`, `V ≤ 10`) give at most 107 per second. 100 polling workers are capped at `B` = 50 per pass (`W ≤ 50`, `V ≤ 50`) and give at most 507 per second (460 measured). Adding the timer pass gives at most 214 and 1,014 per second.
- **Poll latency:** a refresh holds `s.mu` for at most `D` per attempt, so refreshes hold the mutex for at most `D/I` of each second. Polls arrive at about 5 per second per worker at the 200 ms idle interval: about 50 per second at 10 workers and 500 at 100. A poll that arrives during an attempt waits at most `D`. Measured `D` at `B` = 50 was at most 283 ms, 0.28 × `I` (note 35). The number of polls delayed by an attempt is therefore about `50·D` per second at 10 workers and `500·D` at 100 workers. The attempt rate does not grow with the worker count, but the number of polls each attempt delays grows tenfold. The expected extra wait per poll is at most `(D/I)·(D/2)`.
- **Limit:** if `D` reaches `I`, the timer pass is itself late, the `2 × I` threshold is reached in normal operation, and slice 5c must stop and report. Measured `D` stayed below `I` (note 35); one machine, one run, a lightly loaded database.

### 6.3 Invariants, tests and mutations

Each invariant names the test that would catch its violation and the mutation that the test must kill. Test names are **planned**; none exist yet. Every new guard needs a mutation proof run in a throwaway worktree under `~/mc-mut-claude`, as [AGENTS.md](../../AGENTS.md) Section 6 requires.

| ID | Invariant | Test (planned) | Mutation that must fail the test |
|---|---|---|---|
| INV-1 | A job is never executed by two workers at once: at most one `RUNNING` attempt per job, and each claim has exactly one winner. | `TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner` (planned test): N concurrent polls, exactly one 200, the rest 204, one attempt row. | Remove both `FOR UPDATE SKIP LOCKED` and the `state IN ('QUEUED','RETRYING')` predicate from the claim. The second claim must then fail on `idx_attempts_active_per_job` (error other than 204), or insert a second attempt; either fails the test. |
| INV-2 | Assignment is a reservation: it creates no attempt and changes no `attempt_no` or `max_attempts`. | `TestConformance_Assign_CreatesNoAttempt` (planned test): attempt count before and after the pass is equal. | Insert a `PENDING` attempt in the pass. |
| INV-2b | Every state-changing path clears the assignment columns (the shape CHECK holds). | `TestPostgresRepository_CancelAssigned_ClearsAssignment` (planned test) and one case per path (commit, sweep, `DispatchNextJob`, cancel). | Remove the assignment-column clearing from `CancelJob`. The CHECK must then reject the update and the test must fail. |
| INV-3 | Only the assigned worker, inside the window, gets its own reservation; no poll claims a live reservation of another worker; own reservation is preferred to any fallback job. | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` (planned test): wrong worker gets 204; expired reservation with the age and lapse arms unmet gets 204 to its own worker; a worker holding a live reservation with a fallback job available claims the reservation. | (a) drop `assigned_worker_id = $W` from the own arm; (b) drop `assignment_expires_at > clock_timestamp()` from the own arm; (c) order the fallback arm before the own arm. Each must fail a separate case. |
| INV-4 | Round-robin, pure function: over `K` decisions with a constant set `S` and no removal, counts differ by at most 1 (Section 2.6, S2). | `TestProperty_RoundRobin_CountsWithinOne` (planned test): at least 1,000 random constant-set segments, using the pure function. | Reset the cursor to the lowest ID on every decision; or use an index instead of the worker-ID cursor. |
| INV-4b | **No double selection while another eligible worker waits (S1), per task type, with interleaved types (R1).** | `TestProperty_RoundRobin_NoDoubleSelectionPerType` (planned test): at least 1,000 random sequences that interleave two or three task types, each with random per-decision eligibility; checks S1 for every pair of selections of the same worker. | Use one cursor for all task types (the R1 defect). The interleaved sequences then produce a double selection with a waiting eligible worker, and the test fails. A second mutation: reset the cursor on each pass instead of persisting it per type. |
| INV-5 | The rotation survives coordinator restart, per task type. | `TestPostgresRepository_AssignmentCursor_SurvivesRestart` (planned test): pass for two types, new repository instance, pass; each type's second pick continues its own cycle. | Keep cursors in process memory only (as `RoundRobinScheduler.lastIndex` is). |
| INV-6 | No reservation stays past expiry plus one pass interval while the pass runs. | `TestCoordinator_AssignmentSweep_RevokesWithinOneInterval` (planned test), with an injected clock. | Remove the revoke branch from the pass. |
| INV-7a | A lapse consumes no attempt budget. | `TestConformance_AssignmentLapse_ConsumesNoAttempt` (planned test): `attempt_no` and `max_attempts` unchanged after lapse. | Increment `attempt_no` or insert a placeholder attempt on lapse. |
| INV-7b | Lapses are bounded: after `L` lapses the job is fallback-claimable with reason `lapses`. | `TestConformance_AssignmentLapse_FallbackAfterLimit` (planned test) and `TestFault_AssignmentLapseLoop_BoundedByLimit` (planned test). | Remove the limit check (lapses grow without bound and no fallback occurs). |
| INV-8 | Cancel wins over assignment: a cancelled job is never claimed. | `TestConformance_CancelAssigned_NeverClaimed` (planned test). | Remove the state predicate from the claim and stop `CancelJob` clearing the assignment. The claim then returns a `CANCELLED` job; the test fails. |
| INV-9 | Grace suppresses lapse counting but not revocation. | `TestCoordinator_AssignmentSweep_GraceDoesNotCountLapse` (planned test) and `TestCoordinator_AssignmentSweep_GraceStillRevokes` (planned test). | Ignore the grace input in the pass, so lapses are counted during grace; a second mutation: skip revocation under grace (job stuck). |
| INV-10 | **One effective slot (R2): no worker holds more than one reservation, and no reservation is given to a worker with a `PENDING` or `RUNNING` attempt.** | `TestConformance_Assign_OneReservationPerWorker` and `TestConformance_Assign_SkipsWorkerWithRunningAttempt` (planned tests): a second pass assigns nothing to a worker that already holds a reservation; a worker with a `RUNNING` attempt and a fresh poll gets none. | (a) drop the outstanding-reservation check from eligibility; (b) drop the `PENDING`/`RUNNING` check. Each must fail its own case. |
| INV-10b | **The eligibility re-check runs under the worker row lock (R2 race).** | `TestPostgresRepository_Assign_SkipsWorkerLockedByClaim` (planned test): a second connection holds the worker row lock (simulating an in-flight claim); the pass must not reserve that worker, must not block past `lock_timeout`, and must report no `40P01`. | Remove the `FOR UPDATE` on the worker row in the pass. The pass then reserves the locked worker, and the test fails. |
| INV-11 | Only fresh, healthy, polling workers are eligible. | `TestConformance_Assign_SkipsStaleWorkers` (planned test): stale poll, stale heartbeat, `UNHEALTHY` each skipped. | Drop each freshness predicate in turn. |
| INV-12 | `fifo_pull` behaviour is unchanged: its claim ignores assignment columns. | `TestConformance_FifoPull_IgnoresAssignmentColumns` (planned test), plus the existing `DispatchNextJob` tests. | Make the fifo claim honour `assigned_worker_id`. |
| INV-13 | Lock order `jobs -> workers -> attempts` holds for the new paths: no deadlock with the sweeper or a claim. | `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim` (planned test): a second connection holds a worker row lock while the pass runs; the test asserts the pass completes or skips that worker within `lock_timeout`, and that PostgreSQL reports no `40P01`. | Make the pass lock `workers` before `jobs`. PostgreSQL then reports `40P01` against a concurrent claim, and the test fails. |
| INV-14 | **Liveness (R3): with the pass silent and the attempting poll supporting the type, the age arm admits one aged job per attempt, and attempts run at most once per `MINICLOUD_ASSIGNMENT_INTERVAL` (N6). So the k-th claimable job in FIFO order is claimable within `FALLBACK_AGE + k × INTERVAL` plus one poll gap of the silence beginning, while an attempting poll supports its type (further defect 24).** | `TestFault_AssignmentPassStalled_JobRunsWithinBound` (planned test, memory variant in slice 5a, Postgres variant in 5b): pass disabled, and the poll's refresh fails too so that the age arm is reached (further defect 22); a job is submitted; the injected clock advances past `FALLBACK_AGE`; a poll claims it with `policy: round_robin_fallback`, `fallback_reason: age`. Second case: a reservation expires with the pass disabled; the job is claimable after the same bound. | Remove the age arm from the fallback predicate. The job is never claimed; the test fails. |
| INV-15 | **Batch and head-of-type (R4): per pass, at most `B` candidates are read and reserved per type; the oldest dispatchable job of a type is reserved in every pass where some worker of that type is eligible.** | `TestProperty_BatchHeadNeverStarved` (planned test): random multi-type submissions with batches smaller than the backlog; asserts the head of each type is reserved in every pass with an eligible worker, and no pass reads more than `B` per type. | Replace the per-type candidate read with a single `LIMIT B` over all types. A head of unplaceable jobs then blocks the other types, and the test fails. |
| INV-16 | **No age fallback while the pass is live (R3 refinement).** A saturated healthy queue never produces an `age` fallback. | `TestProperty_SaturatedPass_NoAgeFallback` (planned test): a live pass and a backlog older than `FALLBACK_AGE`; no poll claims an `age` fallback. | Drop the pass-silence condition from the age arm. The backlog is then claimed as fallback, and the test fails. |
| INV-17 | **Poll timing (R6): the accept window exceeds the maximum poll backoff plus the margin, and configuration enforces the floor.** | `TestPollTiming_AcceptWindowExceedsBackoff` (planned test, slice 5c): asserts that the configured minimum window is greater than the worker's maximum poll backoff plus the round-trip margin, using the extracted constants; and that config validation rejects a window below the minimum. | Raise the worker's maximum poll backoff constant above the window floor. The relation fails, and the test fails. A second mutation: lower the config floor back to 2 s. |
| INV-18 | **Event payload cap (R4):** a `JOB_ASSIGNED` payload carries at most 10 candidate IDs plus a count. | `TestConformance_AssignEvent_CapsCandidates` (planned test): a pass with more than 10 eligible workers writes 10 IDs and the correct `candidate_count`. | Remove the cap. The test fails. |
| INV-19 | **Restart and outage (N1): an age-arm claim never precedes a refresh that succeeded in the same poll.** A poll whose stored heartbeat is older than 2 × `MINICLOUD_ASSIGNMENT_INTERVAL` runs a refresh before its claim, if the rate limit allows (N6), and the age arm closes when the refresh commits. | `TestPoll_AgeArm_AfterRestart_RefreshesFirst` and `TestPoll_AgeArm_AfterOutage_RefreshesFirst` (planned tests; memory variant in slice 5a, Postgres variant in slice 5b): a heartbeat stale for longer than `FALLBACK_AGE`, and a job older than `FALLBACK_AGE`, then one poll. The worker the refresh reserved the job to claims with `policy: round_robin`. Any other poll gets 204. Neither is `round_robin_fallback`. A poll inside the rate window after a failed attempt gets 204 (N6). | Remove the refresh step from `PollWork`. The age arm then claims with `fallback_reason: age`, and the test fails. |
| INV-20 | **Yield (N2): consecutive repeated passes are separated by at least `MINICLOUD_ASSIGNMENT_MIN_YIELD` with `s.mu` released, so a waiting poll runs between them.** | `TestCoordinator_AssignmentRepeat_YieldsToPolls` (planned test, slice 5c): with a backlog that forces repeats, a poll blocked on `s.mu` acquires it before the next pass starts, and the gap between the end of one pass and the start of the next is at least `MIN_YIELD` on the injected clock. | Set the gap to zero and keep `s.mu` across the repeat (the N2 defect). The poll does not acquire the mutex between passes, and the test fails. |
| INV-21 | **Repeat only with progress (further defect 19): a repeat follows only a pass in which some type's batch was full and at least one reservation was made.** | `TestCoordinator_AssignmentRepeat_OnlyWithProgress` (planned test, slice 5c): a full batch of unplaceable jobs gives exactly one pass per timer tick; a full batch with a reservation repeats. | Repeat on a full batch regardless of reservations. The unplaceable case then runs passes back to back, the pass count exceeds one per tick, and the test fails. |
| INV-22 | **Skip, not block (N3): a second pass that finds the advisory lock held returns `skipped` at once and writes nothing.** | `TestPostgresRepository_AssignmentPass_SecondPassSkips` (planned test, slice 5b): a second connection holds the key; the pass returns within a 1 s context deadline with `skipped`; no row in `assignment_pass_state`, `assignment_cursor`, `jobs` or `job_events` changes. | Replace `pg_try_advisory_xact_lock` with `pg_advisory_xact_lock`. The pass blocks until the deadline, and the test fails. |
| INV-23 | **Refresh attempts are single-flight and at most one per `MINICLOUD_ASSIGNMENT_INTERVAL` (N6).** | `TestPoll_RefreshAttempts_AtMostOnePerInterval` (planned test, slice 5c): a persistently failing pass; 1,000 polls from 10 workers, then from 100 workers, over ten intervals on the injected clock. The pass counter from polls is at most 11 in each case. `TestPoll_RefreshSingleFlight` (planned test, slice 5a): a pass blocked inside its refresh; a second poll starts no second attempt. | Remove the `refreshLastAttemptAt` check. Attempts then equal polls and the first test fails. Release `s.mu` around the refresh. The second test then sees two attempts in flight and fails. |
| INV-24 | **The age arm requires this poll's own attempt to have failed. A rate-limited or successful poll never claims by age (N6).** | `TestPoll_RateLimitedPoll_NoAgeClaim` (planned test, slice 5a memory, slice 5b Postgres): a failed attempt 1 ms before recovery; the heartbeat older than `FALLBACK_AGE`; an aged job; the next poll is rate-limited and gets 204. `TestPoll_FirstPollAfterOutage_AttemptsAndCloses` (planned test, slice 5a): a long outage, then the first poll attempts and succeeds; no `round_robin_fallback`. | Use `refreshLastFailed` alone as the age-arm condition. The rate-limited poll then claims with `fallback_reason: age`, and the first test fails. Skip the attempt while `refreshLastAttemptAt` is zero. The second test then gets an age claim and fails. |

### 6.4 Fault-injection and API abuse tests (all planned)

| Test (planned test) | Proves | Fault or abuse |
|---|---|---|
| `TestFault_AssignedWorkerNeverPolls` (planned test) | FS-14: reassignment after one lapse; the lapse is counted; no attempt is created. | Fault |
| `TestFault_AssignedWorkerDiesBeforePoll` (planned test) | FS-15: revoke on `UNHEALTHY` without a counted lapse. | Fault |
| `TestFault_PollResponseLostAfterClaim` (planned test) | FS-16: the claim commits, the response is dropped, the attempt is reaped to `LOST` and consumes one attempt. Documents the Q3 cost. | Fault |
| `TestFault_CoordinatorCrash_BetweenAssignAndPoll` (planned test) | FS-17: reservation survives restart; grace prevents counting. | Fault |
| `TestFault_CancelRacesAccept` (planned test) | FS-18: row-lock serialisation of cancel and claim. | Fault |
| `TestFault_AssignmentLapseLoop_BoundedByLimit` (planned test) | FS-19: the fallback happens after `L` lapses and the job completes. | Fault |
| `TestFault_DatabaseOutage_DuringPass` (planned test) | FS-20: pass failure and outage grace; no counted lapses. | Fault |
| `TestFault_AssignmentPassStalled_JobRunsWithinBound` (planned test) | FS-21 and INV-14: the age arm fires after the pass has been silent, and the job completes within the bound. | Fault |
| `TestFault_PollRoundTripBeyondWindow` (planned test) | FS-22: with the pass heartbeat fresh (the loop is running), a poll past the window gets 204, the lapse is counted, and the job still completes through the next pass. A late poll that finds the pass heartbeat stale refreshes it itself and can claim the job it lapsed, by design (note 32). | Fault |
| `TestAPI_Poll_ForeignAssignment_Returns204` (planned test) | A poll by a non-assigned worker never receives another worker's reservation (fallback aside). | API abuse |
| `TestAPI_Poll_UnhealthyWorker_Still503` (planned test) | Unchanged 503 `WORKER_UNHEALTHY`. | API compatibility |
| `TestAPI_Poll_Flood_ThrottlesFreshnessWrites` (planned test) | At most one `last_poll_at` write per worker per second under a poll flood. Measured by counting writes through a repository wrapper, not by timing. | API abuse |
| `TestAPI_Job_AssignmentFieldsAreAdditive` (planned test) | `GET /v1/jobs/{job_id}` gains the four fields and existing fields are unchanged. | Protocol compatibility |
| `TestConformance_PollResponse_PolicyField` (planned test) | The 200 poll body carries `policy` with the correct value per path (`round_robin`, `round_robin_fallback`, `fifo_pull`), and `fallback_reason` on fallback claims. | Protocol |
| `TestFault_RestartThenPoll_NoAgeClaim` (planned test) | FS-23 and INV-19: a restart with a stale heartbeat; the first poll refreshes and no age claim occurs. | Fault |
| `TestFault_OutageRecovery_NoAgeClaim` (planned test) | FS-23 and INV-19: the database is down past `FALLBACK_AGE`, then recovers; the first poll refreshes and no age claim occurs. | Fault |
| `TestFault_DeepBacklog_PollsNotStarved` (planned test) | FS-24 and INV-20, INV-21: a deep backlog with repeats; polls get `s.mu` between passes, and a full batch with no reservation does not repeat. | Fault |
| `TestFault_PersistentPassFailure_AgeClaimRate` (planned test) | FS-21 and INV-23, INV-24: a persistently failing pass; one aged job is claimed per interval, by the attempting poll; other polls get 204; the FS-21 bound holds. | Fault |
| `TestFault_OutageRecovery_RateWindow` (planned test) | FS-23 and INV-24: a failed attempt just before recovery; polls inside the window get 204; the first poll after the window attempts and closes the age arm. | Fault |

Property tests: INV-4, INV-4b, INV-15 and INV-16 run at least 1,000 iterations. The sub-state conformance test `TestProperty_AssignmentSubState_ModelConformance` drives random sequences of submit, pass, poll, cancel, reap, pass stall, heartbeat, commit and clock advance over the in-memory repository and checks INV-1, INV-3, INV-6, INV-8 and INV-10 after every step and INV-14 at the end of each seed. It is in-memory only and has no reference model: it checks invariants of the state, not equality with a second implementation. A reference model, and the PostgreSQL backend, are V2 work (note 34).

---

## 7. Slice plan

Three slices. Each is independently mergeable behind `MINICLOUD_DISPATCH_POLICY=fifo_pull` (the default until the gate, flipped in P4-17, note 37), so `main` stays on `fifo_pull` throughout the slices.

*Amendments (implementation notes, decisions 2 and 6):* the in-memory poll-time refresh is part of slice 5a; the PostgreSQL counterpart is part of slices 5b and 5c. The assignment cases stay in `internal/storage` tests until slice 5b moves them into the shared `storagetest` runner.


### Slice 5a: domain, pure policy and in-memory parity (no database)

- **Scope.** Assignment fields, the poll freshness field and the fallback reason on the domain types. Three new event-type constants. The pure round-robin function (per type, Section 2.3) and the lapse and fallback predicates (Section 3.2). The effective-slot constant (Section 2.4). In-memory assignment pass with per-type cursors, batch limit, revoke, claim, cancel clearing and pass heartbeat. Coordinator policy wiring and config validation. No migration, no Postgres code.
- **Config (validated in 5a).** The cross-validation rules of A1 (Section 5.5) apply in this slice. Seven variables: `MINICLOUD_DISPATCH_POLICY` (existing), `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` (5 s, 5 s to 30 s), `MINICLOUD_ASSIGNMENT_INTERVAL` (1 s), `MINICLOUD_ASSIGNMENT_MAX_LAPSES` (3, 1 to 10), `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` (30 s, and at least `MAX_LAPSES x (ACCEPT_WINDOW + INTERVAL)`, which is 18 s by default), `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` (50, 1 to 500), and `MINICLOUD_ASSIGNMENT_MIN_YIELD` (10 ms, 1 ms to 1 s, not above `MINICLOUD_ASSIGNMENT_INTERVAL`) (N2). `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` must exceed `2 × MINICLOUD_ASSIGNMENT_INTERVAL` (N1, N6). Section 5.5 is the single source for every variable (N7).
- **Files (existing, to edit):** `internal/domain/job.go` (assignment fields, JSON tags `omitempty`), `internal/domain/worker.go` (`LastPollAt`), `internal/domain/events.go` (three event types), `internal/storage/inmemory.go` (assignment pass, claim predicates, revoke, cancel clearing), `internal/storage/storagetest/conformance.go` (shared tests), `internal/coordinator/service.go` (policy selection in `PollWork`; constructor unchanged), `internal/config/config.go` (six variables, validated).
- **Files (new, proposed):** a domain file for assignment rules, a pure function file beside `internal/scheduler/round_robin.go`, a coordinator loop file beside `internal/coordinator/sweeper.go`, and the in-memory and conformance tests for these.
- **Tests (planned).** INV-1 (memory variant), INV-2, INV-3, INV-4, INV-4b, INV-6, INV-7a, INV-7b, INV-8, INV-10, INV-11, INV-12, INV-14 (memory variant), INV-15, INV-16, INV-18, INV-19 (memory variant), INV-23 (single-flight test), INV-24 (memory variant), `TestConformance_PollResponse_PolicyField`, and `TestProperty_AssignmentSubState_ModelConformance` (memory only).
- **Verification commands.**
  - `test -z "$(gofmt -l .)"`
  - `go vet ./...`
  - `go build ./...`
  - `make test-docs`
  - `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` (memory only; database tests skip)
  - Mutation proofs for INV-3, INV-4b, INV-7b, INV-8, INV-10, INV-14, INV-15, INV-16, INV-19, INV-23, INV-24 in `~/mc-mut-claude`, each showing the test fails under the mutation and passes after restore.
- **Independent audit.** The pure round-robin function (INV-4 and INV-4b, and the argument in Section 2.6), the lapse and fallback predicates (Section 3.2), and the one-slot rule (Section 2.4).

### Slice 5b: migration 000005 and the Postgres repository

- **Scope.** Migration `000005`, Postgres assignment pass with the advisory lock, per-type candidate reads, worker row locking in the reserve step, claim through the shared dispatch core (own arm, then fallback arm), revoke step, cancel and sweeper clearing, `DispatchNextJob` clearing of the columns, the freshness write, and the pass heartbeat. Shared conformance run against Postgres. Concurrency tests. An `EXPLAIN` check of the fallback query.
- **Files (existing, to edit):** `internal/storage/postgres/repository.go` (`DispatchNextJob` clearing; `CancelJob` clearing; claim core; freshness write), `internal/storage/postgres/leases.go` (sweeper clearing on terminal transitions), `internal/storage/repository.go` (new interface methods), `internal/storage/postgres/migrator_test.go` (only if it pins the latest version; see Section 5.3).
- **Files (new, proposed):** `000005_assignment_round_robin.sql` in `internal/storage/postgres/migrations/`, a Postgres assignment file in `internal/storage/postgres/`, and a migration test file following `migration_000004_test.go`.
- **Tests (planned).** INV-1 (Postgres variant), INV-2b, INV-5, INV-9, INV-10b, INV-13, INV-19 (Postgres variant), INV-22, INV-24 (Postgres variant), `TestPostgresRepository_AssignmentCursor_SurvivesRestart`, `TestPostgresRepository_CancelAssigned_ClearsAssignment`, INV-14 (Postgres variant), and a migration test that applies `000005` to a migrated database and to a fresh one.
- **Verification commands.**
  - All slice 5a commands
  - `make test-db`, with its output redirected to a log file in the session scratchpad and the tail pasted verbatim. It needs the shared test database, so it runs where that database is available. This design makes no claim about its result.
  - Mutation proofs for INV-1 (Postgres), INV-2b, INV-10b, INV-13, INV-19 (Postgres), INV-22, INV-24 (Postgres).
- **Independent audit.** The claim and pass SQL, the worker-lock re-check (Section 2.4), the lock order (Section 4.3), and the CHECK-driven clearing (INV-2b). This slice must be audited by an agent that did not build it (AGENTS.md Section 7).

### Slice 5c: coordinator loop, grace, API fields, fault and abuse suites, worker timing

- **Scope.** The assignment loop in the coordinator (timer, submit trigger, and the repeat rule of Section 2.1 with its yield), the rate-limited ERROR log for a stale pass heartbeat (A2, Section 4.5), the poll-time refresh with its single flight and rate limit (N6), grace state wiring, startup validation, job view fields, the poll `policy` and `fallback_reason` fields, the worker constant extraction and the timing test (INV-17), the fault and API abuse suites in Section 6.4, and the property tests at 1,000 iterations.
- **Files (existing, to edit):** `internal/coordinator/service.go` (submit trigger; loop start), `internal/coordinator/sweeper.go` (grace state passed to the pass), `cmd/coordinator/main.go` (start the loop; policy from config), `internal/api/handlers.go` (poll `policy` and `fallback_reason` fields; job fields are carried by the domain JSON), `internal/worker/client.go` (extract the idle poll interval and maximum poll backoff to named constants; no behaviour change).
- **Files (new, proposed):** fault tests under the `tests/` integration and fault directories, following the existing `tests/integration/` layout, and the API abuse tests beside `internal/api`.
- **Tests (planned).** INV-6 (end to end), INV-9 (loop variant), INV-17, INV-18 (loop variant), INV-20, INV-21, INV-23 (cost variant), every test in Section 6.4 (including the FS-23 and FS-24 tests), and `TestProperty_AssignmentSubState_ModelConformance` at 1,000 iterations.
- **Verification commands.** All previous commands, plus `make test-db` as above and `make test-race` (or the `-race -count=1` form). Report PASS, SKIP and FAIL counts separately (AGENTS.md Section 5), because database tests skip without the URL.
- **Independent audit.** Grace interaction (INV-9), the fallback behaviour under failure (FS-19, FS-20, FS-21), and the timing relation (INV-17). The audit must also re-run the mutation proofs from slices 5a and 5b, and the mutation proofs for INV-20, INV-21 and INV-23 in this slice, and INV-24 at loop level.

**Follow-up slices, not in 5a to 5c.**
- **Worker concurrency (Q5).** Scheduled before the V2 comparison. It removes `effectiveWorkerSlots` (Section 2.4) and makes the worker loop run jobs concurrently, so `total_slots` means something in practice. It is not part of this ADR's slices.
- **`REJECTED` in `chk_jobs_state` (Q8).** A separate task, after confirming whether any path persists `REJECTED`.

**Owner gate and docs.** After slice 5c passes and is audited, the default policy may flip (Q1) in a separate, small, reviewed change, before the v1.0.0 tag. The docs that must change then are owned by a docs task, not by this ADR: [docs/protocol.md](../protocol.md) (Sections 4.3 and 6), [docs/state-machine.md](../state-machine.md) (the `PENDING` assignment reference, Section 4), [docs/schema.md](../schema.md), [docs/failure-model.md](../failure-model.md) (FS-14 to FS-22), [docs/phase4-plan.md](../phase4-plan.md) Section 3, and the roadmap.

Specifically, [docs/phase4-plan.md](../phase4-plan.md) Section 3 says the lease sweeper powers assignment expiry: "the Phase 4 lease sweeper machinery (`StartLeaseSweeper`) directly powers assignment expiration" and the sweeper "revokes the assignment". That is wrong for this design (C21). Assignment expiry is handled by a separate assignment pass, and the lease sweeper is not involved. The docs task must correct that paragraph.

---

## 8. Open questions

### 8.1 Orchestrator answers (revision 1)

The orchestrator answered every question in revision 1 (R7). The answers below are the decisions of record. Where an answer changes this design, the change is noted.

| # | Question | Recommendation (rev 0) | **Orchestrator answer (rev 1)** | Effect on this design |
|---|---|---|---|---|
| **Q1** | When does `round_robin` become the default dispatch policy? | Flip after slice 5c is green and audited, behind an owner sign-off. | **Flip the default to `round_robin` before the v1.0.0 tag, in its own small reviewed change after the audit. `fifo_pull` stays selectable.** | Section 5.2, Section 7 owner gate. |
| **Q2** | After `L` lapses, should a job fall back to first-poller-wins, or keep being reassigned round-robin? | Fall back, with `L = 3`, logged as `round_robin_fallback`. | **Fall back after `L = 3` lapses, logged, visible on the job view, excluded from the distribution invariant.** | Section 3.2, Section 5.1 (`assignment_fallback_reason`), INV-4b scope. |
| **Q3** | Should a claim whose response was lost (FS-16) consume `max_attempts`? | Yes, for now. Document the cost. | **Yes. A lost claim response consumes `max_attempts`, the same as `fifo_pull` today.** | Section 1.4, Section 3.6, FS-16. |
| **Q4** | Is `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW = 5 s` acceptable? | Yes (range 2 s to 30 s). | **Accepted, subject to R6.** R6 raises the floor from 2 s to 5 s, because the worker can back off for 3 s. | Section 3.1, D6, INV-17. |
| **Q5** | Keep one job per worker in V1, or schedule a worker-concurrency slice? | Keep for V1; schedule concurrency before the V2 comparison. | **Keep one job per worker in V1. Schedule a worker-concurrency slice before the V2 comparison.** | Section 2.4 (`effectiveWorkerSlots`), Section 7 follow-up. |
| **Q6** | Should `RETRYING` jobs be round-robin assigned, or only `QUEUED`? | Both. | **Both `QUEUED` and `RETRYING`.** | Section 1.5, Section 2.1. |
| **Q7** | Should the round-robin guarantee cover all task types as one cycle, or per task type? | Per task type. | **Per task type, with a per-type cursor (R1).** | Section 2.3, Section 2.6, INV-4b, Section 5.3. |
| **Q8** | `chk_jobs_state` omits `REJECTED` (C7). Fix in `000005`, or in a separate task? | Separate task. | **Separate task.** | Section 7 follow-up. |
| **Q9** | Must V1 support several coordinators? | Not for V1. | **No multi-coordinator claim in V1.** | Section 4.2, D11. |
| **Q10** | Expose assignment fields on `GET /v1/jobs/{job_id}`, and the `policy` field on poll? | Yes, both additive. | **Yes, expose the additive fields.** | Section 5.1. See remaining question R-a on the fourth field. |
| **Q11** | Should `DRAINING` be given a code path in this work? | No. | **No `DRAINING` path in V1.** | Section 3.4. |

### 8.2 Remaining questions raised by revision 1

The orchestrator answered each of these in revision 2 (Section 8.3). The recommendation given here is the answer of record.

| # | Question | Recommendation | Why it is a decision |
|---|---|---|---|
| **R-a** | Revision 1 adds a fourth job-view field, `assignment_fallback_reason`. Q10 named three. Confirm the fourth. | Yes. Without it an age-based fallback is invisible on the job view, and Q2 asks for fallback to be visible. | It is a new public field. |
| **R-b** | The age arm of the fallback (R3) is gated on pass silence, not on age alone (Section 3.2). This is a refinement of R3's wording. Confirm. | Yes. An ungated age rule turns a saturated healthy queue into FIFO fallback. | It changes which jobs fall back under load. |
| **R-c** | The floor of `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` moves from 2 s to 5 s (R6). Q4 was accepted "subject to R6", so this follows, but it changes a config range. | Confirm 5 s to 30 s. | It changes the accepted range. |
| **R-d** | `FALLBACK_AGE` (default 30 s) and `BATCH_LIMIT` (default 50) are new config values. The values are proposed, derived in Sections 3.2 and 2.1, and not measured. | Accept the proposed defaults; tune after the V2 benchmark. | They set the liveness bound and the pass cost. |
| **R-e** | The worst-case liveness bound (FS-21) holds only while a healthy polling worker supports the type. That is a stated limit, not a fix. | Accept. | A job for a type with no live worker cannot run; no design fixes that. |

### 8.3 Orchestrator answers recorded (revision 2)

| Item | Orchestrator answer (revision 2) | Effect |
|---|---|---|
| R-a | Yes. The fourth job-view field `assignment_fallback_reason` stays. | Section 5.1 |
| R-b | Yes. The age arm is gated on pass silence, and it is also closed while a poll's refresh succeeds (N1). | Sections 3.2, 6.3 |
| R-c | Yes. The accept window is 5 s to 30 s. | Section 3.1, D6 |
| R-d | Accept the proposed defaults (`MINICLOUD_ASSIGNMENT_FALLBACK_AGE` 30 s, `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` 50). Measured values are recorded in slice 5c. | Sections 2.1, 3.2, Section 7 (slice 5c) |
| R-e | Accept. | Section 6.1 (FS-21), Limits of this design |

---

## 9. Change records: revision 1 and revision 2 (further defects)

Section 9 is the traceability record. A **further defect** is one the orchestrator did not list. Each further defect is numbered once, in one list per revision: revision 1 has **18** (items 1 to 18, in the R5 list below), revision 2 has **4** (items 19 to 22, in its subsection), and revision 3 has **4** (items 23 to 26, in the last subsection). Items marked **further** in R2 to R4 are the same list, and their numbers are given in R5. Revision 1's own count was inconsistent (15 in its R5 list, 17 in its report with one duplicate). The list below is the corrected count.

### R1: per-task-type cursor
- Rule (Section 2.3) now reads and writes `c_t` per type.
- Storage (Section 2.3, Section 5.3): table `assignment_cursor` keyed by `task_type`, read and written inside the pass transaction.
- Fairness (Section 2.6): S1 replaces the constant-set statement. S2 is kept as a corollary.
- INV-4 kept; INV-4b added (interleaved types, property test; mutation: single cursor).

### R2: one effective slot
- Eligibility (Section 2.2): no `PENDING` or `RUNNING` attempt, and no outstanding reservation.
- Free slots (Section 2.4): replaced by the constant `effectiveWorkerSlots = 1`.
- Race found while doing this (**further**, Section 2.4 and Section 4.3): a snapshot read of `attempts` is unsafe without the worker row lock. The pass now takes `FOR UPDATE` on the worker row and re-checks under it. Revision 0 said the pass never read `attempts` (Section 1.7); that is corrected.
- Claim preference (Section 3.2): own reservation before any fallback.
- Invariants: INV-10 (one reservation, no reservation with `RUNNING`), INV-10b (lock re-check). Mutations listed.

### R3: liveness
- Fallback (Section 3.2): lapses arm, plus an age arm (`dispatchable_since` older than `FALLBACK_AGE`).
- Refinement (**further**, Section 3.2): the age arm is gated on the pass heartbeat (`assignment_pass_state`). Without the gate, a saturated healthy queue becomes FIFO fallback.
- Expired reservations (**further**, Section 1.3): an expired reservation is treated as unreserved for fallback. Revision 0 left it stuck when the pass was dead.
- Failure row FS-21, invariant INV-14, INV-16 (gate), test `TestFault_AssignmentPassStalled_JobRunsWithinBound` (planned test), mutation: remove the age arm.
- Worst-case wait stated (Section 6.1, FS-21).

### R4: pass bounds
- Batch limit `MINICLOUD_ASSIGNMENT_BATCH_LIMIT`, default 50, per type (Section 2.1, D14).
- Per-type candidate read (**further**, Section 2.1): a single `LIMIT B` over all types would let unplaceable heads starve other types. The read is per type.
- Index `idx_jobs_dispatch_by_type` in migration `000005` (Section 5.3). Revision 0's `idx_jobs_unassigned_fallback` is replaced. Finding **further** (C19): no existing index covers `RETRYING` rows by task type.
- Head-of-type guarantee (Section 2.1), INV-15.
- Event cap: 10 IDs plus a count (Section 2.1), INV-18.
- What happens beyond the batch: next pass, and a repeat after the yield when the batch was full and a reservation was made (Section 2.1, N2). The head-of-type guarantee shows that older jobs are not starved by newer ones within a type.

### R5: consistency sweep and further defects
Sections 4, 5, 6 and 7 were re-read in full. Each statement that R1 to R4 affect is updated. Further defects, numbered 1 to 18 (the count of record for revision 1). Item 2 also covers the lock-order row of Section 4.3, so it is listed once. Items 16 to 18 are the R3 and R4 further items and the late-accept row, which were not in the earlier 15-item list. Each is fixed in the section named:

1. **S-constant premise** (Section 2.6): the constant-set fairness statement does not hold once a reservation removes a worker. Replaced by S1.
2. **Pass reads `attempts` and workers without a lock** (Section 1.7, Section 4.3): unsafe for R2. Fixed by the worker row lock and re-check.
3. **Expired reservations stuck** (Section 1.3, Section 3.2): with a dead pass, no claim path accepted them. Fixed by the fallback arm.
4. **Fallback claim of a job reserved to a busy worker** (Section 2.4, Section 3.2): prevented by the own-first claim preference.
5. **Claim wins a fallback while the worker holds a reservation** (Section 2.4): prevented by the same preference.
6. **Expiry observed but not written** (Section 1.3): the gap between the predicate and the pass write is now stated.
7. **`idx_jobs_task_dispatch` covers QUEUED only** (C19): RETRYING had no per-type index. Fixed by the new partial index.
8. **Docs claim** (C21): `docs/phase4-plan.md` Section 3 says the lease sweeper powers assignment expiry. Added to the docs-to-update list in Section 7.
9. **Stated guarantees that the mechanism does not support**: "an idle, polling worker is not skipped in favour of a busy one" is kept, and now argued (Section 2.6). "Every eligible worker is selected within `|E|` decisions" is restated as the S1 starvation statement, because `E` changes within a pass.
10. **Section 6.2 batch row** still said "50 candidates per transaction", with no per-type rule and no revoke bound. Replaced with D14.
11. **`total_slots` abuse row** (Section 6.2): stale after R2. Updated.
12. **`PollWork` mutex and the pass** (Section 4.1, Section 2.1): a pass delays a poll by one batch. Stated. This is a liveness dependency on the single loop; the bound is one batch.
13. **Freshness gap** (Section 3.1): `last_poll_at` writes are throttled. The gap is `max(throttle, poll gap)`, not a sum. Stated so the window margin is not over-claimed.
14. **Expired assignments and policy switch** (Section 5.2): an old reservation after a switch to `fifo_pull` is left in place and is harmless. Stated.
15. **Fallback reason not visible for age-based fallback** (Section 3.2, Section 5.1): fixed by the fourth field (R-a).
16. **Age arm unconditional** (R3 refinement, Section 3.2): under a saturated healthy queue every job is older than `FALLBACK_AGE`, so the whole backlog became FIFO fallback. Gated on pass silence (INV-16).
17. **One `LIMIT B` across all types** (R4, Section 2.1): a head of unplaceable jobs of one type fills the batch and starves other types. The candidate read is per type (INV-15).
18. **Late-accept table said a lapsed worker always gets 204** (Section 3.5): a healthy worker may claim a fallback job. The table is corrected. No stale token results, because the claim is still the only issuer.

### R6: poll timing
- Verified in code (Section 3.1, C16 to C18): idle sleep 200 ms and maximum backoff 3 s are **locals** in `Run`, not constants; the jitter is [0.5, 1.0), not full; the poll HTTP client timeout is 15 s.
- The ADR now states the requirement: window > maximum backoff + round trip + margin. The margin is not needed for correctness (Section 3.1).
- Config floor raised from 2 s to 5 s, because a 2 s window lapses on a 3 s backoff (D6, Q4).
- Constants to be extracted to named constants in slice 5c (Section 3.1, Section 7).
- Test `TestPollTiming_AcceptWindowExceedsBackoff` (planned test) and mutation (INV-17).

### R7: orchestrator answers
Recorded in Section 8.1 with the effect of each answer. Remaining questions were answered in revision 2 (Section 8.3).

### Revision 2 record (N1 to N5, further defects 19 to 22)

| Item | Change (the orchestrator's item unless marked) | Where |
|---|---|---|
| N1 | Age arm after a restart or an outage. A poll refreshes a stale heartbeat before its claim. The age arm is evaluated only after a refresh has failed. The requested grace-window suppression is not used (reasons in Section 3.2). | Sections 3.2, 4.1, 4.4, 6.1 (FS-21, FS-23), 6.2, 6.3 (INV-14, INV-19), 6.4 |
| N2 | `MINICLOUD_ASSIGNMENT_MIN_YIELD` (default 10 ms). Repeat only after a full batch with a reservation (further defect 19). Poll and commit latency bounds, with `D` not measured. | Sections 2.1, 4.4, 6.1 (FS-24), 6.3 (INV-20, INV-21), 6.4, 7 |
| N3 | `pg_try_advisory_xact_lock`: a second pass skips and logs, and writes nothing. Its interaction with the submit trigger and the timer is stated. | Sections 2.1, 2.3, 4.2, 4.3, 5.4, 6.2, 6.3 (INV-22) |
| N4 | One list per revision, each item once. Revision 1: 18 (Section 9 R5). Revision 2: 4 (below). The report and the message use the same numbers. | Section 9 |
| N5 | Orchestrator answers R-a to R-e recorded. | Section 8.3 and the status line |

**Further defects found in revision 2 (numbered 19 to 22).**

19. **Repeat spin.** The repeat rule re-ran on any full batch. When no worker of a type is eligible, a full batch of unplaceable jobs repeated back to back with no reservation (Section 2.1, Section 2.5). Fixed: a repeat needs a reservation (INV-21).
20. **Blocking wording.** Sections 2.3 and 4.2 said a second pass blocks on the advisory lock, which contradicts N3. Fixed by the try-lock wording (Sections 2.3, 4.2).
21. **Amplification by the refresh.** The N1 refresh runs one pass attempt per poll while the pass fails with the database reachable. Found while checking N1 for side effects. It is not rate-limited, because a rate limit could skip the refresh just after recovery and reopen the gap. Residual stated in Section 6.2, to measure in slice 5c.
22. **INV-14 setup.** As first planned ("pass disabled"), INV-14 would fail on a correct implementation, because the poll's refresh would still reserve the job. Fixed: the test must make the refresh fail too (Section 6.3).

Item 21's residual was closed by N6 (rate limit, single flight, own-attempt age arm). The cost is in Section 6.2.

### Revision 3 record (N6 to N8, further defects 23 to 26)

| Item | Change (the orchestrator's item unless marked) | Where |
|---|---|---|
| N6 | Refresh attempts single-flight and rate-limited to one per `MINICLOUD_ASSIGNMENT_INTERVAL`. The outcome is held in `refreshLastFailed`. The age arm requires this poll's own failed attempt. The cost of a failing pass is bounded. The rate-limit argument and planned tests are in Sections 3.2 and 6.3. | Sections 3.2, 4.3, 4.4, 6.1 (FS-21, FS-23), 6.2 (cost, residual), 6.3 (INV-14, INV-19, INV-23, INV-24), 6.4, 7 (slice 5a and 5c) |
| N7 | Single source of truth for every new config variable, with default, range, validation and derivation. | Section 5.5 |
| N8 | Revision 3 record in this format; re-sweep of Sections 6.3 and 7. | This section |

**Further defects found in revision 3 (numbered 23 to 26).**

23. **Literal rule reopens the N1 gap.** The requested rule "the most recent refresh attempt failed" would let a rate-limited first poll after an outage open the age arm on the outage's failure. The premise "the first poll after an outage always attempts" is false. Fixed: the age arm requires this poll's own failed attempt; `refreshLastFailed` alone is rejected (Section 3.2).
24. **Stale liveness wording.** INV-14, the FS-21 bound and the Limits line promised `FALLBACK_AGE` plus one poll gap, and liveness for "any healthy polling worker". Under the rate limit, aged jobs come one per attempt, and only the attempting poll must support the type. Fixed in those three places.
25. **Refresh threshold equal to the timer interval.** With a threshold of one `INTERVAL`, a healthy heartbeat (up to `INTERVAL` plus `D` old) would start refresh attempts in normal operation and double the pass load. Fixed: threshold 2 × `INTERVAL`, and `FALLBACK_AGE > 2 × INTERVAL` (Sections 3.2, 5.5).
26. **`INTERVAL` had no range or validation rule.** The ADR gave only a default, and the rate limit depends on it. Fixed in Section 5.5: proposed range 100 ms to 10 s. The floor caps attempts at 10 per second.

### Amendment record: A1 and A2 (orchestrator, after revision 3)

The orchestrator accepted revision 3 for implementation and added two amendments. They are not further defects and do not change the count of 26.

| Item | Change | Where |
|---|---|---|
| A1 | Startup cross-validation: `MINICLOUD_ASSIGNMENT_INTERVAL` must not exceed `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`, and `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` must be greater than `2 × INTERVAL + ACCEPT_WINDOW`. Startup fails otherwise. Implemented in slice 5a, because config validation is in 5a (Section 7). | Section 5.5, Section 7 (slice 5a) |
| A2 | Operator note: while the pass heartbeat is stale, the coordinator logs at ERROR at most once a minute. The remedy is `MINICLOUD_DISPATCH_POLICY=fifo_pull` and a restart. Implemented in slice 5c. | Section 4.5, Section 7 (slice 5c) |

The status line at the top of this ADR now reads "Accepted for implementation (orchestrator, revision 3 plus A1 and A2)".

---

## Consequences

### Positive
- Round-robin placement, with per-type persisted cursors that survive restarts and joins or leaves, using the existing lease machinery for timing and a separate pass for assignment.
- Claims stay the only issuer of tokens and attempts. Assignment adds no attempt, no token and no lease to unaccepted work.
- A stalled pass cannot stop dispatch: the age arm bounds the wait (FS-21).
- `fifo_pull` is untouched in behaviour, so the V2 benchmark baseline stays valid.
- Every change is additive on the wire and forward-only in the database.

### Negative and trade-offs
- A lost claim response costs one lease period and one attempt (Q3).
- A slow poller costs up to one window per lapse, bounded by `L` (Q2).
- Each `last_poll_at` write adds a write per worker per second under polling.
- Under the synchronous worker loop (C12), `total_slots` has no effect on turns or reservations in V1 (Q5).
- A stalled pass falls back to first-poller-wins for aged jobs, at most one per attempt interval (N6). That is the intended trade for liveness, and it is bounded by the pass-silence gate and the rate limit. A pass that fails persistently gets a bounded fallback rate, not a full one.
- The pass takes worker row locks, so it can wait on a claim (bounded by `lock_timeout`).
- More moving parts in the coordinator: a pass, per-type cursors, a pass heartbeat, an advisory try-lock, a poll-time refresh, a repeat yield, and new grace interactions.

### Invariants protected
- INV-1 to INV-24 (Section 6.3). ADR 0011 Section 1 (`SKIP LOCKED` dispatch), ADR 0013 (lock order and restart), ADR 0014 (lease and grace), ADR 0015 (attempt budget), ADR 0016 (cancel semantics).

## Limits of this design

- **Not measured.** No pass latency, no poll-freshness write cost, no distribution under load. Any number would be invented, so none is given (AGENTS.md Section 2). The defaults for `FALLBACK_AGE` and `BATCH_LIMIT` are proposals (R-d).
- **Not tested.** No test in this ADR exists or has been run. All test names are planned.
- **Assumes database time** (ADR 0011 Section 5 item 5) and a single source of worker poll cadence (C12).
- **Fairness is over decisions and per task type** (Section 2.6, S1), not over executions and not over slots.
- **Liveness under a failing pass is bounded only while the attempting poll supports the job's type, at one aged claim per interval** (FS-21, R-e, N6).
- **Unauthenticated workers** (SEC-004). Sybil and impersonation are not defended here.
- **Multi-coordinator is designed for, not claimed** (Q9).
- **Pre-existing gaps noted, not fixed:** `REJECTED` missing from `chk_jobs_state` (Q8); the in-memory and Postgres tie order (Section 5.4); the synchronous worker loop (Q5); the fact that `idx_jobs_task_dispatch` covers only QUEUED (C19, folded into migration `000005` for the new scan only).
- **Docs not updated.** Per the task scope, only this file is changed. The protocol, state-machine, schema, failure-model and phase-plan changes listed in Section 7 are for the docs task.

## Implementation notes, slice 5a

These decisions were made by the orchestrator in fix round 1 of the P4-08b review (tip 37110db). Each one amends the section it names.

1. **Lapse limit leaves round-robin (D9 wins; erratum to Section 2.1).** A job whose `assignment_lapses` has reached `MINICLOUD_ASSIGNMENT_MAX_LAPSES` leaves round-robin. The candidate read of the assignment pass must exclude it, so it stays unassigned and becomes claimable by the lapse arm of Section 3.2.
2. **Poll-time refresh: memory in 5a, PostgreSQL in 5b and 5c (amends Section 7).** The in-memory poll-time refresh (Sections 3.2, N1 and N6) is implemented in slice 5a, because the 5a memory tests for INV-19, INV-23 and INV-24 need it. Slices 5b and 5c implement its PostgreSQL counterpart and the loop side.
3. **Freshness before refresh.** A poll writes its own `last_poll_at` (Section 2.2) before the refresh runs, so the polling worker is eligible for the reservations its own refresh makes. INV-19 depends on this.
4. **`EffectiveWorkerSlots` lives in domain (amends Section 2.4).** The constant is `domain.EffectiveWorkerSlots`, not a coordinator constant, because the assignment pass runs in the storage layer.
5. **Memory test name for INV-1.** The in-memory variant of INV-1 is `TestConformance_ClaimAssigned_ConcurrentPollsOneWinner`. The PostgreSQL variant keeps the name `TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner`.
6. **Location of the 5a assignment cases.** The assignment cases live in `internal/storage` tests (package `storage_test`) until slice 5b moves them into the shared `storagetest` runner. The 11 PostgreSQL skips in `assignment_5a_skips_test.go` (PostgreSQL storage package; deleted in slice 5b by commit b67db2a) become 11 passing tests in slice 5b.

### Implementation notes, P4-08b fix round 2

These notes come from the P4-08b audit (review file on branch `agent/claude/P4-08b-review`, findings F-5a-1, F-5a-12 and F-5a-13) and the orchestrator's fix round 2 instructions.

7. **Settings are defaulted and validated at the service (F-5a-1).** `coordinator.NewService` starts from `DefaultAssignmentSettings()`. `SetAssignmentSettings` applies the Section 5.5 ranges and the A1 cross-checks (`AssignmentSettings.Validate`, the same rules as the config) and returns `domain.ErrInvalidRequest` for settings that fail them, keeping the previous settings. `SetDispatchPolicy("round_robin")` refuses settings that fail `Validate`.
8. **Database clock for the pass heartbeat and staleness (requirement for slice 5b, F-5a-13).** In PostgreSQL, the pass heartbeat (`assignment_pass_state.last_completed_at`) is written with the database clock (`now()`), and every staleness comparison against it (the `2 × MINICLOUD_ASSIGNMENT_INTERVAL` refresh threshold of Section 3.2, the age arm's pass-silence check, and the stale-heartbeat log of Section 4.5) reads the database clock too, so the write and the read use the same clock. The Service never compares its own clock with a time read from the repository. The process clock stays only for the refresh rate limit (`refreshLastAttemptAt`), which compares two process-clock readings. In slice 5a the in-memory `refreshIfStaleLocked` still compares `s.clock.Now()` with the in-memory heartbeat; this is safe in memory only, and slice 5b must change it.
9. **Refresh threshold and poll-write throttle stay deferred to slice 5c (F-5a-12).** The `2 × interval` refresh threshold in `refreshIfStaleLocked` and the one-write-per-second poll freshness throttle (`assignmentPollWriteThrottle` in `internal/storage/inmemory_assignment.go`) have no pinning test in 5a; the audit's mutations of both survive. Their tests are `TestPoll_RefreshAttempts_AtMostOnePerInterval` (planned test, slice 5c; INV-23 cost variant) and `TestAPI_Poll_Flood_ThrottlesFreshnessWrites` (planned test, slice 5c; Section 6.4).

### Implementation notes, P4-08c fix round 1

These notes record the orchestrator's decisions on the P4-08c (slice 5b) review. They add to the notes above and change no older text.

10. **INV-9 grace belongs to slice 5c (erratum to Section 7).** Section 7 lists INV-9 among the slice 5b tests; that is wrong. Grace wiring, and with it INV-9 and its tests, is slice 5c. Slice 5b counts every lapse (`counted: true`).
11. **INV-13 mutation (erratum to Section 6.3).** The Section 6.3 mutation "make the pass lock `workers` before `jobs`" is an equivalent mutant: the pass reads its job candidates with `FOR UPDATE SKIP LOCKED`, so it never waits on a job row, and a wait cycle with a claim needs the pass to wait on a job. The INV-13 mutation is the removal of `SKIP LOCKED` from the per-type candidate read; it produces `40P01` against a claim and is killed by `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim` and `TestPostgresRepository_AssignmentPass_ConcurrentMixedOperations`.
12. **Pass heartbeat time (amends note 8).** The pass writes `assignment_pass_state.last_completed_at` with `clock_timestamp()` in its last statement, not `now()` (the transaction start), so the value is the completion time of the pass. Every staleness comparison uses `clock_timestamp()`. Pinned by `TestPostgresRepository_AssignmentPass_HeartbeatIsCompletionTime`.
13. **Staleness interface (amends note 8).** The separate interface `storage.AssignmentPassStaleness` (`AssignmentPassStale(ctx, threshold)`, implemented by both repositories) is accepted as the way to meet note 8; `storage.AssignmentRepository` stays as slice 5a defined it. `SetDispatchPolicy("round_robin")` refuses a repository without it (`TestSetDispatchPolicy_RoundRobinRequiresPassStaleness`).
14. **Mixed clocks (accepted for V1).** `jobs.created_at` is stamped by the coordinator's process clock (`Service.SubmitJob`; PostgreSQL stores the value it is given), while the age arm (`dispatchable_since`) and the staleness checks compare with the database clock. In PostgreSQL, `jobs.not_before` is computed from the database clock (`CommitAttempt` and `SweepExpiredLeases` read `clock_timestamp()`), so only `created_at` mixes the clocks there. This is harmless on one host. Unifying the clocks is a V3 requirement before multi-machine operation. No code change in V1.

### Implementation notes, P4-08c fix round 2

These notes record the orchestrator's decisions on the independent P4-08c audit (review file on branch `agent/claude/P4-08c-review`). They add to the notes above and change no older text.

15. **The pass lock is database-wide on purpose (P4-08c review F-R1).** `AssignmentPassLockID` is one fixed advisory key per database, as Section 4.2 and INV-22 require: one assignment pass at a time per database, whichever coordinator or schema runs it. Advisory locks are not scoped to a schema, so two test processes that share one test database (each test in its own schema) also share the key, and a pass in one process skips while the other holds it. That is the designed behaviour, not a defect, and the key does not change. The test rule follows from it: one test process at a time per test database (`make test-db` and the race runs of the PostgreSQL packages).

### Implementation notes, P4-08d (slice 5c, part 1)

These notes record the builder's decisions on points this ADR leaves open, for the coordinator loop, grace wiring and binary wiring of slice 5c (task P4-08d). They add to the notes above and change no older text. The orchestrator may overrule any of them.

16. **Grace is pending until the sweeper's first successful tick (Section 4.4).** The ADR starts the startup grace on the sweeper's first tick and is silent about passes that run before it. Once a lease sweeper exists for the coordinator, the pass receives `GracePending: true` until that sweeper's first successful tick, and again after a failed tick until the next successful one; a pending grace counts no lapse. Reason: a restart or a database outage must not count lapses in the gap before the sweeper has decided the grace window (FS-17, FS-20). A coordinator with no sweeper (library and test use) passes no grace and counts every lapse, as slices 5a and 5b did. The binary builds the sweeper before it starts the loop. Pinned by `TestCoordinator_AssignmentLoop_GracePendingUntilFirstSweep`.
17. **Grace instants are repository time.** Section 4.4 says the pass receives the grace state exactly as `SweepExpiredLeases` does, so `storage.AssignParams` carries a `domain.SweepGraceState`. Its instants are read by the sweeper from the repository clock (`repo.Now`, the database clock in PostgreSQL), never from the process clock, so note 8 holds. They are the only time values the coordinator passes to the pass; every other input is a duration or a count. The decision (`storage.LapseCounts`, which applies `SweepGraceState.Suppresses`) runs inside the pass on the time read with the job row lock: in PostgreSQL the revoke statement returns `clock_timestamp()` with each locked row.
18. **Lapse event under grace.** An expired reservation revoked under grace writes `JOB_ASSIGNMENT_LAPSED` with `counted: false` and the unchanged `lapses` value. `storage.AssignResult` reports it in `LapsedUncounted`; `Lapsed` counts only counted lapses.
19. **Loop shape.** The loop runs one round at start, then one per timer tick (`MINICLOUD_ASSIGNMENT_INTERVAL`) and one per submit trigger. The trigger is a channel with a buffer of one, so a burst of submits adds at most one round. Each pass runs under `s.mu` with a deadline of `BackgroundCallTimeout`, as a sweeper tick does. A failed or skipped pass ends the round with no repeat. Loop passes do not read or write the poll-time refresh state (`refreshInFlight`, `refreshLastAttemptAt`, `refreshLastFailed`), so a failing loop pass never opens the age arm (INV-24 at loop level).
20. **Stale heartbeat log (Section 4.5).** The loop checks the heartbeat after every round through `storage.AssignmentPassStaleness` with the threshold `2 × MINICLOUD_ASSIGNMENT_INTERVAL` (database clock, note 8). The first stale check logs at once; later lines wait at least one minute on the process clock, which compares two process-clock readings. If the loop itself has stopped, nothing logs; that case is the "pass loop stopped" row of Section 4.4.
21. **Binary wiring (P4-08c review F-R3).** `cmd/coordinator` applies the assignment settings, then the policy (round_robin is refused with invalid settings), and exits on either error. It logs one line with the effective policy and the six settings. It starts the loop only under round_robin; the loop itself does not check the policy. The default stays `fifo_pull` (Q1).
22. **One validator.** `internal/config` keeps its own copy of the Section 5.5 and A1 rules (`validateAssignment`), because its messages name the environment variables, as A1 requires, and the package imports only the standard library. The two copies are pinned together by a cross-test over the edge values of every rule and over the policy names, `TestMain_AssignmentSettings_ConfigAndServiceAgree`, which compares `config.LoadCoordinatorConfig` with the service checks as the binary chains them.
23. **Test names for note 9.** The refresh threshold is pinned by `TestPoll_RefreshThreshold_TwiceInterval` (in-memory in the coordinator package; PostgreSQL in the PostgreSQL storage package). The cost variant `TestPoll_RefreshAttempts_AtMostOnePerInterval` cannot pin it, because with a failing pass the heartbeat is stale at any threshold. The poll-write throttle is pinned at the repository by the shared case `TestConformance_RecordPoll_ThrottlesFreshnessWrites` (both backends). The API flood test of Section 6.4, `TestAPI_Poll_Flood_ThrottlesFreshnessWrites` (planned test), stays with part 2 (P4-08e).
24. **INV-6 end-to-end name.** The slice 5a name `TestCoordinator_AssignmentSweep_RevokesWithinOneInterval` is taken by the pass-level memory test. The end-to-end variant with the running loop is `TestCoordinator_AssignmentLoop_RevokesWithinOneInterval_EndToEnd`.

### Implementation notes, P4-08d fix round 1

These notes record the orchestrator's decisions after the independent P4-08d audit (review file on branch `agent/claude/P4-08d-review`, findings F-R1 to F-R5 and F-5c-1). They add to the notes above and change no older text.

25. **The pending grace has a cap (amends note 16; review F-R1).** Note 16 left the pending state without an end: while the sweeper never ticked, or every tick failed, no lapse was counted, so a job whose chosen worker stayed fresh but never claimed never reached `MAX_LAPSES` and FS-19 ("bounded by `L`; then fallback") did not hold. The pending state now ends after a cap, measured on the repository clock from the moment the pending state began. After the cap, expired reservations are counted as lapses as normal. Section 4.4 defines two grace lengths and no third: the startup grace (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30 s) and the outage grace (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30 s). The cap is the one that matches the cause: the startup grace for the pending state before the sweeper's first successful tick, and the outage window for the pending state after a failed tick. Both are the values configured on the service (`SetGracePeriods`). A failed tick while the state is already pending does not restart or extend it. Mechanism: `storage.AssignParams.GracePendingUntil` is the instant the pending state ends; the coordinator anchors the start on the first read of the repository clock (`repo.Now`) by a pass or poll after the pending state began, so the anchor lags the real start by at most one pass interval. If that read fails, no end is passed and the state stays pending, which is safe because a pass that works implies a clock that works. The decision (`storage.LapseCounts`) runs inside the pass on repository time; no process-clock value enters it. Worst-case time to fallback in FS-19 with the cap: `L x (accept window + pass interval) + cap + one pass interval`. The last term is the anchor lag: the cap anchor is later than the real start of the pending state by at most one pass interval, so the bound must include it. With the defaults (`L` = 3, window 5 s, interval 1 s, cap 30 s) that is 3 x 6 s + 30 s + 1 s = 49 s; with a working sweeper there is no pending state and the bound is the first term alone. Pinned by `TestCoordinator_AssignmentLoop_PendingGraceEndsAtCap` (the sequence of counted flags in the three modes, and the fallback claim after three lapses), `TestCoordinator_AssignmentLoop_FailedSweepMarksGracePending` and `TestLapseCounts_PendingEndsAtUntil`. The test `TestCoordinator_AssignmentLoop_GracePendingUntilFirstSweep` now uses a 10 s startup grace, because with a cap of zero the pending state ends at once; its tail asserts that the pass is no longer pending after the first tick, and the claim that a lapse counts again when no grace applies is carried by the control mode of the cap test.
26. **Submit flood: no throttle in V1 (review F-5c-1; no code change).** Executed by the auditor on PostgreSQL under `-race` with 8 polling workers (whole-test-database figures, review check I): in a flood of submits the loop ran 385 submits/s, 6.6 passes/s, with a poll p50 of 138 ms and p99 of 325 ms; paced at 2 x 10 submits/s it ran 22 ms p50 and 99 ms p99. Decision: the trigger buffer of one (note 19) already coalesces a burst, so passes are bounded by the pass cost and not by the submit rate; no throttle is added in V1. The submit endpoint is unauthenticated until V3 (SEC-004). Revisit after the V2 benchmark. The part 2 measurements (P4-08e) reuse these numbers as the baseline; I did not re-measure them in this round.

### Implementation notes, P4-08e (slice 5c, part 2)

These notes record the builder's decisions for the API fields and the worker timing of slice 5c (task P4-08e). They add to the notes above and change no older text. The orchestrator may overrule any of them.

27. **5c-2.1 The Section 5.1 fields were already on the wire; this slice pins them.** The JSON names of the four job-view fields (`domain.Job`) and the poll fields `policy` and `fallback_reason` (`coordinator.WorkAssignment`) came with slice 5a, without a wire test. This slice adds no field and changes no value. Presence rules as built and now pinned: every 200 poll carries `policy` (`fifo_pull` under that policy); `fallback_reason` is present only on a `round_robin_fallback` claim and absent otherwise; the job view always carries the four fields, `null` or `0` when unset; `assigned_at` is never part of it; a fallback reason survives a retry and a later own-reservation claim (Section 1.1, "never cleared"). Pinned on the wire by `TestAPI_Poll_PolicyFieldOnWire` and `TestAPI_Job_AssignmentFieldsAreAdditive`, and per backend by the shared cases `TestConformance_ClaimPolicy_PerArm` and `TestConformance_JobView_AssignmentFields` (memory and PostgreSQL). The slice 5a test `TestConformance_PollResponse_PolicyField` stays as it is (memory, coordinator package); the coordinator copies the claim's policy and reason unchanged, so the repository cases carry the PostgreSQL parity.
28. **5c-2.2 INV-17 constants and the margin.** The worker constants are `worker.IdlePollInterval` (200 ms) and `worker.MaxPollBackoff` (3 s); the window floor is `coordinator.MinAcceptWindow` (5 s), used by `AssignmentSettings.Validate`. `internal/config` keeps its own literal floor (note 22). Section 3.1 states the requirement as "window > maximum poll backoff + round trip + margin" and gives no number for the round trip or the margin, so `TestPollTiming_AcceptWindowExceedsBackoff` pins a named margin (note 5c-2.4: 1 s, so `MinAcceptWindow` minus `MaxPollBackoff` must be at least 1 s; the slack today is 2 s), checks that no jittered backoff exceeds `MaxPollBackoff`, and checks that the configuration and the service both reject the floor minus 1 ms and accept the floor. The margin was an open question in the first version of this note; note 5c-2.4 settles it.
29. **5c-2.3 Poll sleeps wait on the agent clock.** `Agent.Run` waits between polls through the agent's `Clock` (`NewTimer`) instead of `time.Sleep`. With the real clock the wait is the same and is still not interrupted by shutdown, as before. Reason: without it no test can see the sleeps `Run` takes, so a loop that ignored the constants would pass the INV-17 relation test. `TestPollTiming_RunSleepsWithinMaxBackoff` drives `Run` through two streaks of 300 failed polls (separated by one empty poll, which resets the failure count), then 20 `WORKER_UNHEALTHY` polls and empty polls, and checks that every sleep is at most `MaxPollBackoff` and that an idle or `WORKER_UNHEALTHY` poll sleeps exactly `IdlePollInterval`.
30. **5c-2.4 Fix round 1 decisions: the INV-17 margin, the arm precedence, the submit body (P4-08e audit F-R1 to F-R4).**
    - **Margin (F-R1).** Section 3.1 gives no number for "round trip + margin". The V1 choice is 1 s: `coordinator.MinAcceptWindow` minus `worker.MaxPollBackoff` must be at least 1 s (today 5 s minus 3 s = 2 s). It is a policy value, not a proof: the worker's poll HTTP timeout is 15 s, so no floor of 15 s or less can cover a worst-case round trip. The test holds it as the constant `acceptWindowMargin` next to `TestPollTiming_AcceptWindowExceedsBackoff`. Changing either constant so that the difference drops below 1 s must re-justify the margin here. Killing proof: a poll backoff of 4.999 s (1 ms of slack) fails the test.
    - **Both fallback arms (F-R2).** When a job meets both arms of Section 3.2 at once (lapses at or above `L`, and older than the fallback age with the pass silent and the age arm open), both backends report `lapses`. This is current behaviour, not mandated by Section 3.2, which lists the arms without ordering them; a change needs an ADR amendment. Pinned on both backends by the shared case `TestConformance_ClaimPolicy_BothArms`.
    - **Submit body (F-R3).** Section 5.1 first said `POST /v1/jobs` has no change. Its 201 body is the `domain.Job` the service returns (the same encoding as the job view), so it has carried the four assignment fields since slice 5a. The decision is to keep them: they are always unset at submit (`null`, `null`, `0`, `null`), so they expose nothing, and `assigned_at` stays absent. The Section 5.1 table and `docs/protocol.md` (Section 3.1) are amended. Pinned by `TestAPI_SubmitJob_ResponseCarriesAssignmentFields`.
    - **Real clock (F-R4).** `TestPollTiming_RealClockSleeps` runs `Agent.Run` on the production clock and bounds the real gaps between polls (about 3.5 s), so the clock the poll loop waits on is covered.
    - **Not in this round (F-R5).** The backoff overflow in `ComputeBackoffWithJitter` (0 s from 64 failures) is pre-existing and belongs to P4-14.

### Implementation notes, P4-08f (slice 5c, part 3)

These notes record the builder's decisions and findings for the fault, abuse and property suites and the measurements of slice 5c (task P4-08f). They add to the notes above and change no older text. The orchestrator may overrule any of them. No production code changed in this task.

31. **5c-3.1 Names and scope of the Section 6.4 suite.** The fault tests are `TestFault_AssignedWorkerNeverPolls` (FS-14), `TestFault_AssignedWorkerDiesBeforePoll` (FS-15), `TestFault_PollResponseLostAfterClaim` (FS-16), `TestFault_CoordinatorCrash_BetweenAssignAndPoll` (FS-17), `TestFault_CancelRacesAccept` (FS-18), `TestFault_AssignmentLapseLoop_BoundedByLimit` (FS-19), `TestFault_DatabaseOutage_DuringPass` (FS-20), `TestFault_PollRoundTripBeyondWindow` (FS-22), `TestFault_RestartThenPoll_NoAgeClaim` and `TestFault_OutageRecovery_NoAgeClaim` (FS-23, INV-19), `TestFault_DeepBacklog_PollsNotStarved` (FS-24, INV-20, INV-21), `TestFault_PersistentPassFailure_AgeClaimRate` (FS-21, INV-23, INV-24) and `TestFault_OutageRecovery_RateWindow` (FS-23, INV-24). All are in `tests/faultinject` and run on the in-memory and the PostgreSQL backend. The ADR names three tests with an API prefix; `make test-security` selects only the fault and abuse prefixes (see the Makefile), so they are `TestAbuse_Poll_ForeignAssignment_Returns204`, `TestAbuse_Poll_UnhealthyWorker_Still503` and `TestAbuse_Poll_Flood_ThrottlesFreshnessWrites` in `tests/abuse`. Two Section 6.2 rows that Section 6.4 does not list also have a test: `TestAbuse_ConstantPoller_NoLargerShare` and `TestAbuse_LargeTotalSlots_NoLargerShare`. The Section 6.4 rows that already had a test keep it where it is: `TestFault_AssignmentPassStalled_JobRunsWithinBound` (coordinator package; memory and PostgreSQL), `TestAPI_Job_AssignmentFieldsAreAdditive` (`internal/api`) and `TestConformance_PollResponse_PolicyField` (coordinator package). `tests/security-tests.list` goes from 54 to 72 names.
32. **5c-3.2 How the suite drives the coordinator, and one behaviour it showed.** `sectest.NewRRRig` builds a `round_robin` service over the real handler and either repository, behind a wrapper (`sectest.FaultRepo`) that counts passes and polls and can fail passes or the whole assignment path. The loop is not started: a test runs one round with `Pass`, so each step is deterministic. Time passes by `Elapse` (the fake clock in memory; every stored timestamp moved back in PostgreSQL, as the repository tests do). Two facts follow from the eligibility rule (a worker is eligible only if it polled within the accept window, Section 2.2) and are now exercised. First, a silent worker stops being eligible one window after its last poll, so in FS-14 and FS-19 the job moves on only to a worker that keeps polling. Second, the 204 for FS-22 is worded in the Section 6.4 test row of `TestFault_PollRoundTripBeyondWindow` ("a poll past the window gets 204"), not in Section 6.1 (which says only that the job lapses and is counted), and it holds when the loop keeps the pass heartbeat fresh. A late poll that finds the heartbeat stale (loop stopped, or no pass for more than `2 × MINICLOUD_ASSIGNMENT_INTERVAL`) runs the refresh itself (Section 3.2). That refresh counts the lapse, may reserve the job to the polling worker, and the same call then claims it with `policy: round_robin`: the first version of `TestFault_PollRoundTripBeyondWindow` saw `200` here. This is the designed behaviour (note 3), not a defect; the test runs the pass first, as the loop does. In short (FS-22): a late poll with a stale pass heartbeat refreshes it itself and can claim the job it lapsed, by design; the Section 6.4 row now carries the precondition "pass heartbeat fresh".
33. **5c-3.3 Grace in the PostgreSQL variants.** The sweeper keeps its grace instants in process memory as repository time. `Elapse` moves stored timestamps, not those instants, so a PostgreSQL test cannot skip over a grace window. `TestFault_CoordinatorCrash_BetweenAssignAndPoll` therefore uses a 2 s startup grace and waits 2.2 s of real time in the PostgreSQL variant (the fake clock moves in memory). The pending state before the new sweeper's first tick counts no lapse; after the tick and the grace, a lapse counts again (events `[uncounted, counted]`). `TestFault_DatabaseOutage_DuringPass` fails the sweeper tick and the pass together, so the first successful tick opens the outage window and the revoke after recovery is uncounted.
34. **5c-3.4 The property test.** `TestProperty_AssignmentSubState_ModelConformance` (coordinator package, 1,000 seeds, 40 random steps each) already ran in slice 5a with INV-1, INV-8 and INV-10 only. It now checks INV-3 (no poll claims a job that is live-reserved to another worker; a `round_robin` claim was the claimant's own live reservation; when the poll ran the refresh pass, the reservations compared are those read right after that pass, before the claim, so all claims are checked and not only those of polls without a pass, P4-08g), INV-6 (right after a successful pass no reservation is held at or past its expiry), INV-8 (also: a cancelled job holds no reservation), INV-1 and INV-10 as before, and INV-14: after every seed the pass is stalled and a fresh worker polls once per interval; every job still `QUEUED` must be claimed within `FALLBACK_AGE + (jobs + 1) × interval + 3 s`. A failure logs the seed, and `MINICLOUD_PROPERTY_SEED=<n>` replays one seed. The test fails if it reserves nothing, makes no `round_robin` claim or never drains a stalled backlog (vacuity guard). INV-4, INV-4b, INV-15 and INV-16 keep their 1,000-iteration tests from slices 5a and 5b. Scope, stated plainly: the test is **sub-state conformance over the in-memory repository only, with no reference model** (a reference model is V2). INV-14 runs once at the end of each seed, not after every step. It does not reach RETRYING and `not_before`, LOST and the sweeper, the grace state, the lapse limit and the lapse fallback, skipped passes, several task types, repeated rounds, a binding batch limit or concurrent claims; those are covered by the fault tests and the repository conformance tests, on both backends.
35. **5c-3.5 Measurements.** Reused from the P4-08d audit (note 26), not re-measured: submit flood 385 submits/s, 6.6 passes/s, poll p50 138 ms and p99 325 ms; paced at 2 × 10 submits/s, poll p50 22 ms and p99 99 ms. The same audit run (`audit-B-flood-60s.log`, `audit-B-paced-30s.log` in the P4-08d evidence) recorded the pass duration `D`: flood p50 56 ms, p99 164 ms, max 245 ms; paced p50 14 ms, p99 39 ms, max 336 ms; in both, `D` stayed below `I` = 1 s. New in this task (`P4-08f-evidence/measure-5c3.log`, probe `measure_5c3_test.go.txt`, PostgreSQL, one process, no `-race`, one run): with `T` = 1 and `B` = 50, a pass that reserves `W` = 10 workers took 61 ms and a pass that lapses 10 expired reservations and reserves 10 took 43.4 to 53.2 ms (5 passes); with 100 workers a pass is capped at `B` = 50 reservations and took 192 ms, and a pass that lapsed 50 and reserved 50 took 242.4 to 283.1 ms (11 passes); a pass that did nothing (every worker holds a reservation) took 2.3 to 6.0 ms, with two outliers of 26.7 ms and 57.8 ms, so idle passes took up to 57.8 ms. The "lapse" passes are counted lapses of expired windows (`revoked=0 lapsed=10` in the log), not UNHEALTHY revokes, which the probe did not run. So `D` at the default `B` was at most about 0.28 × `I` in these runs, and the limit of Section 6.2 ("if `D` reaches `I`, stop") was not reached. It is one machine, one run and a lightly loaded database; a larger `B` was not measured. Round trips per pass (a pgx tracer counts every statement and batch): an idle pass 7; a pass that reserves `R` workers 81 for `R` = 10 and 360 for `R` = 50, about 7.3 and 7.0 per reservation; a pass that lapses `V` and reserves `R` 101 for 10 and 10, and 460 for 50 and 50, so about 2 per lapse. This is **a finding about the ADR text**: the cost bound of Section 6.2, `6 + 2T + 4W`, gives 48 for `W` = 10 and 208 for `W` = 50, and the measured passes took 81 to 101 and 360 to 460. The per-reservation term is about 7, not 4, and lapses add about 2 each. The bound was labelled "from the pass structure, not a measurement", so nothing is contradicted, but the figures derived from it (48 and 408 round trips per second for 10 and 100 workers, doubled with the timer pass) are low by about a factor of 1.7 at `W` = 10 and understate the lapse term. The attempt rate, `1/I`, does not depend on the bound and is pinned by `TestFault_PersistentPassFailure_AgeClaimRate`. Section 6.2 now states the bound from these measurements, `7 + 8W + 2V` round trips (about 7 per reservation and 2 per lapse, rounded up to 8 per reservation so it stays above every measured pass). The flood of `TestAbuse_Poll_Flood_ThrottlesFreshnessWrites` shows the throttle: 300 polls per worker wrote `last_poll_at` 28 times in 30 s of fake time (memory) and 4 times in 3.5 s of real time (PostgreSQL), where a write per poll would be 300.
36. **5c-3.6 Worker test tidy-ups (audit follow-ups).** `TestPollTiming_RunSleepsWithinMaxBackoff` now also requires every recorded poll sleep to be greater than 0 (valid since P4-14 clamped the failure count). `TestComputeBackoffWithJitter_CallerSettings` has a `result_delivery_loop` row (base 200 ms, maximum 5 s, counts 60 to 70) that reads the unexported constants through `internal/worker/export_test.go`, and its two poll-loop rows use `worker.IdlePollInterval` and `worker.MaxPollBackoff`.
37. **5c-3.7 The default dispatch policy flipped to `round_robin` (P4-17, Q1).** `MINICLOUD_DISPATCH_POLICY` unset or set but empty now selects `round_robin` (`internal/config`, `dispatchPolicyFromEnv`; the one place the default is chosen). `fifo_pull` stays selectable by setting it explicitly (any letter case); any other value is still a startup error. A set but empty value used to be a startup error and now means the default. `NewService` and the repositories still default to `fifo_pull` behaviour until `SetDispatchPolicy` runs, so tests that build a service directly are unchanged; only the binary's configuration changed. Real-binary security tests (`sectest.CoordinatorEnv`) and `scripts/demo.sh` now state their policy explicitly (`fifo_pull` and `round_robin` respectively). Tests: `TestLoadCoordinatorConfig_DispatchPolicySelection`, `TestMain_DispatchPolicyFromEnv_EndToEnd`. Mutation proofs: `docs/agents/handoffs/P4-17-evidence/mutation-index.md`. Docs that still say `fifo_pull` is the default are listed in `docs/agents/handoffs/P4-17.md` for the docs sync.
