# ADR 0017: Assignment-Based Round-Robin Dispatch (Candidate A), Design

## Status
Proposed (2026-10-29). Design only: no production code, test code or migration is part of this ADR.

- Formalises **Candidate A** (pre-assignment with a bounded accept window), the leading design named in [ADR 0011](0011-postgres-dispatch-and-recovery.md) Section 2 and in [docs/phase4-plan.md](../phase4-plan.md) Section 3. It is the post-Phase 4 hard gate for V1 completion.
- Depends on the Phase 4 machinery: attempt leases and the sweeper ([ADR 0014](0014-lease-model-and-expiry.md)), retries ([ADR 0015](0015-retry-policy-and-failure-classification.md)), and cancellation ([ADR 0016](0016-cancellation-semantics.md)). Migration `000004_leases_retries_cancellation.sql` is already in the tree; this design adds migration `000005`.
- Does not change the `fifo_pull` baseline (ADR 0011 Candidate C). It stays selectable.

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
| C6 | The `worker_capacity` view (`000002_schema_hardening.sql`) computes `available_slots = total_slots - count(attempts in PENDING or RUNNING)`. The partial unique index `idx_attempts_active_per_job` covers `PENDING` and `RUNNING`. | confirmed by code reading |
| C7 | `chk_jobs_state` in `000001_initial_schema.sql` lists `SUBMITTED, QUEUED, RUNNING, CANCEL_REQUESTED, CANCELLED, SUCCEEDED, FAILED, RETRYING, DEAD_LETTER`. It does **not** list `REJECTED`, which `internal/domain/transition.go` defines. I found no migration that adds it. | confirmed by code reading |
| C8 | `job_events.event_type` is `VARCHAR(64)` with no CHECK constraint, so new event types need no DDL. | confirmed by code reading |
| C9 | `workers` has `status` (`HEALTHY`, `UNHEALTHY`, `DRAINING`, `TERMINATED`) and `last_heartbeat_at`. `DRAINING` exists in `internal/domain/state.go`; I found no code path that sets it or any deregistration API. | confirmed by code reading |
| C10 | `ReapStaleWorkers` (Postgres) and `ReapInactiveWorkers` (service) set `HEALTHY` workers to `UNHEALTHY` when `last_heartbeat_at` is older than the worker timeout, on the reaper tick. Config: `MINICLOUD_WORKER_TIMEOUT` (default 10 s) and reaper interval (default timeout / 2) in `internal/config/config.go`. | confirmed by code reading |
| C11 | The sweeper (`LeaseSweeper.Tick` in `internal/coordinator/sweeper.go`) keeps its grace state (startup deadline, outage window, cooldown) in process memory and passes it to `SweepExpiredLeases` as `domain.SweepGraceState`. The Postgres sweeper selects only `attempts.state = 'RUNNING'` rows and locks `jobs` with `SKIP LOCKED`, then `workers`, then `attempts`. | confirmed by code reading |
| C12 | The worker's `Run` loop in `internal/worker/client.go` calls `ExecuteAndReport` **synchronously** after a successful poll. A worker therefore polls only while it is executing nothing. On an empty poll it sleeps 200 ms. On a poll error it backs off up to 3 s. | confirmed by code reading |
| C13 | Heartbeats run on their own loop, so a busy worker keeps its heartbeat fresh while it does not poll. | confirmed by code reading |
| C14 | `CancelJob` in both repositories handles `QUEUED` (to `CANCELLED`), `RETRYING` (to `CANCELLED`), and `RUNNING` (to `CANCEL_REQUESTED`), and makes a duplicate cancel on `CANCEL_REQUESTED` a no-op. | confirmed by code reading |
| C15 | Poll response codes today are 200 with an `assignment`, 204 `NO_QUEUED_JOBS`, and 503 `WORKER_UNHEALTHY` ([docs/protocol.md](../protocol.md) Section 4.3). | confirmed by code reading (doc) |

### What is missing
Dispatch is first-poller-wins (ADR 0011 Section 2.1). Blueprint Section 6 requires scheduler-driven round-robin. This ADR decides how a scheduler can choose a worker before the worker claims a job, without reintroducing the rejected hold window (Candidate B, ADR 0011 Section 2.5).

## Decision summary

| # | Question | Decision |
|---|---|---|
| D1 | What is an assignment? | Three nullable columns on `jobs` plus a sub-state of `QUEUED`/`RETRYING`. **No new job state, no new attempt state.** |
| D2 | Does assignment create an attempt? | **No.** Assignment only reserves. The attempt, its number, its lease token and its lease are created at claim (the poll), as today. |
| D3 | Who picks the worker? | A coordinator-owned **assignment pass**, a transaction separate from polls, run on a timer and after submit. Not inside the poll. |
| D4 | Round-robin order | Next eligible worker **by ID after the last assigned worker**, wrapping. Cursor persisted in a singleton row written in the pass transaction. |
| D5 | Who may claim an assignment? | Only the assigned worker, only while `assignment_expires_at` is in the future on database time. Poll never claims unassigned work, except the bounded fallback in D9. |
| D6 | Accept window | `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`, default **5 s**, allowed 2 s to 30 s. Window lapse is a lapse, not an attempt. |
| D7 | Eligibility | Healthy status, fresh heartbeat, fresh **poll** (`workers.last_poll_at` within the window), free slots after counting reservations, and task support. |
| D8 | Lapses | Counted per job in `assignment_lapses`. Not counted during a grace window or for a worker that is no longer healthy. Lapses do not consume `max_attempts`. |
| D9 | Poison-by-lapse bound | After `MINICLOUD_ASSIGNMENT_MAX_LAPSES` (default **3**) lapses the job leaves round-robin and becomes claimable by any polling worker (fallback). **Open question Q2.** |
| D10 | Cancel | QUEUED-assigned and RETRYING-assigned jobs go straight to `CANCELLED` and clear the assignment in the same transaction. |
| D11 | Concurrency | Single coordinator: correct by the service mutex and row locks. Several coordinators: correct by `FOR UPDATE SKIP LOCKED` and state-guarded `UPDATE`s; the pass also takes a transaction advisory lock so only one pass runs at a time. |
| D12 | `fifo_pull` | Stays selectable as `MINICLOUD_DISPATCH_POLICY=fifo_pull`. Default flips to `round_robin` only after the gate (Q1). |
| D13 | Protocol | Additive only: a `policy` field on the poll assignment and assignment fields on the job view. No new endpoint and no new error code. |

---

## 1. What "assignment" is

### 1.1 Representation: columns on `jobs`, not a table, not a state

Options considered:

| Option | Description | Verdict |
|---|---|---|
| A. New job state `ASSIGNED` | A job state between `QUEUED` and `RUNNING`. | **Rejected.** It needs a `chk_jobs_state` rewrite, new FSM edges in `internal/domain/transition.go`, and a new value in the state matrix in [docs/state-machine.md](../state-machine.md). The assignment is an attribute of a queued job, not a lifecycle stage a client should see. |
| B. Separate `job_assignments` table | One row per active assignment. | **Rejected for V1.** It adds a join to the claim and a second place to keep consistent with `jobs.state`. Only one live assignment per job is needed. A table would also need the same `FOR UPDATE SKIP LOCKED` on its own rows and a lock-order entry. |
| **C. Columns on `jobs`** | `assigned_worker_id`, `assigned_at`, `assignment_expires_at`, plus a lapse counter. A CHECK constraint ties them to `state`. | **Chosen.** The claim already locks the job row first (C1), so the assignment is read and cleared under the lock that already protects the job. ADR 0011 Section 2.4 proposed the same columns. |

Rules the schema must enforce (see Section 5.3):

- The three assignment columns are all NULL or all non-NULL.
- Non-NULL assignment columns are allowed only when `state IN ('QUEUED', 'RETRYING')`. Every path that moves a job to `RUNNING`, `CANCELLED`, `CANCEL_REQUESTED`, `SUCCEEDED`, `FAILED` or `REJECTED` must clear them in the same statement. The CHECK is the backstop: a path that forgets fails loudly rather than leaving a stale reservation.

### 1.2 Assignment reserves; the attempt is created at claim

Options:

| Option | Description | Verdict |
|---|---|---|
| A. Create a `PENDING` attempt at assignment | The attempt row, its number and its token exist before the worker accepts. | **Rejected.** A `PENDING` attempt counts against worker capacity (C6), is covered by `idx_attempts_active_per_job`, and consumes an `attempt_no`. An assignment that lapses would leave an attempt that never ran, and the lapse would then consume budget. The `PENDING` value exists in `chk_attempts_state` but no code path writes it today. This design keeps it unused and leaves the open question of removing it from the docs to a follow-up (Section 8, Q8). |
| B. Create the attempt at claim (chosen) | The attempt, its number, its token and its lease are created in the same transaction as the `RUNNING` transition, exactly as in `DispatchNextJob` today. | **Chosen.** Attempts still mean "a worker has started or been handed the job". Lapses leave no trace in `attempts`. |

Consequences:
- A token is minted only inside the claim transaction and returned in the same response. No token is ever issued to a worker that was not the claimant, so there is no "late accept of a stale token" to handle (Section 3.5).
- `attempt_no` and `max_attempts` accounting is unchanged (Section 1.4).

### 1.3 Transitions

No job-state transition is added. The matrix in [docs/state-machine.md](../state-machine.md) and `allowedTransitions` in `internal/domain/transition.go` are unchanged.

Assignment is a sub-state of job states `QUEUED` and `RETRYING`. The table below is the full set of assignment transitions. `U` = unassigned, `A(w)` = assigned to worker `w`.

| From | Trigger | To | Job `state` change | Writes | Audit event (new event types in `internal/domain/events.go`) |
|---|---|---|---|---|---|
| U (`QUEUED` or eligible `RETRYING`) | Assignment pass selects `w` (Section 2) | A(w), expires at `now + window` | none | set the three assignment columns | `JOB_ASSIGNED` `{worker_id, expires_at, policy: "round_robin", candidates, selected, reason}` |
| A(w) | Poll by `w` while `assignment_expires_at > clock_timestamp()` | `RUNNING` (claim) | `QUEUED`/`RETRYING` to `RUNNING` | insert attempt; clear assignment; `lapses = 0`; `not_before = NULL` | `JOB_DISPATCHED` `{policy: "round_robin"}` |
| A(w) | Poll by any other worker | unchanged | none | none | none (poll returns 204 to that worker) |
| A(w) | Expiry passed, `w` `HEALTHY`, **not** in grace | U | none | clear assignment; `assignment_lapses + 1` | `JOB_ASSIGNMENT_LAPSED` `{worker_id, lapses, counted: true}` |
| A(w) | Expiry passed, grace window active | U | none | clear assignment; lapses unchanged | `JOB_ASSIGNMENT_LAPSED` `{worker_id, counted: false, reason: "grace"}` |
| A(w) | `w` is no longer `HEALTHY` (reaper, Section 3.4) | U | none | clear assignment; lapses unchanged | `JOB_ASSIGNMENT_REVOKED` `{worker_id, reason: "worker_unhealthy"}` |
| A(w) or U | `CancelJob` on a `QUEUED` or `RETRYING` job | `CANCELLED` (terminal) | to `CANCELLED` | clear assignment | `JOB_CANCELLED` (existing) |
| U with `assignment_lapses >= L` | Poll by any healthy worker with capacity (fallback, Section 3.2) | `RUNNING` (claim) | to `RUNNING` | as claim | `JOB_DISPATCHED` `{policy: "round_robin_fallback"}` |
| any | `RUNNING`, `CANCEL_REQUESTED`, terminal | any | (not an assignment transition) | assignment columns must already be NULL | CHECK constraint (Section 5.3) |

Attempt states: no new transition. An attempt is created only at claim, as today.

Non-transitions that a reviewer might expect and that do **not** exist: a re-assignment of an already valid assignment (the pass skips jobs whose assignment is valid), a `PENDING` attempt, and any lapse that changes `attempt_no`.

### 1.4 Interaction with `attempt_no` and `max_attempts`

- A lapse creates no attempt, so it does not move `attempt_no`. It does not consume `max_attempts`.
- A `LOST` attempt (ADR 0014 sweeper, after the claim) still consumes one attempt, as ADR 0015 Section 2 decides.
- A claim whose response is lost in transit (Section 3.6) creates an attempt that never runs. It is then reaped to `LOST` and **does** consume one attempt. This is the existing lease behaviour applied to a claim, and it is open question Q3.

### 1.5 Interaction with `LOST` and `RETRYING`

- The sweeper sets a `RUNNING` job to `RETRYING` (with `not_before`) or `FAILED` (ADR 0014 Section 4). The assignment columns were cleared at claim, so the transition adds no assignment work.
- A `RETRYING` job is eligible for assignment once `not_before <= clock_timestamp()`, the same gate as `DispatchNextJob`. It then goes through the same pass as a `QUEUED` job (decision on Q6, recommended yes).
- Assignment of a `RETRYING` job never sets `not_before`.

### 1.6 Interaction with cancellation

| Job state when cancelled | Result | Assignment | Worker effect |
|---|---|---|---|
| `QUEUED`, assigned or not | `CANCELLED` immediately | cleared in the cancel transaction | none (no attempt exists). A poll that races the cancel either sees the row after the cancel and returns 204, or claims it before the cancel and the cancel then sees `RUNNING`. The job row lock serialises the two. |
| `RETRYING`, assigned or not | `CANCELLED` immediately | cleared | none |
| `RUNNING` | `CANCEL_REQUESTED` (ADR 0016), unchanged | not applicable (cleared at claim) | renewal delivers `cancel_requested` |
| `CANCEL_REQUESTED` | idempotent no-op (ADR 0016) | not applicable | none |

### 1.7 Interaction with leases and renewal

Leases, renewal and the sweeper are unchanged. Assignment has its own clock, `assignment_expires_at`, on database time, following ADR 0014 Section 1. The two clocks never interact: assignment expiry concerns a job that has no attempt; lease expiry concerns an attempt that exists. The assignment pass never reads or writes `attempts`.

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
| **C. Separate assignment pass** | A coordinator loop runs one transaction that revokes expired or orphaned assignments and assigns eligible jobs in FIFO order to workers chosen by the round-robin rule. Runs on a timer (default 1 s, `MINICLOUD_ASSIGNMENT_INTERVAL`) and immediately after a submit. | **Chosen.** It keeps the scheduler a writer of reservations only, leaves the poll cheap and claim-only, and reuses the lease sweeper's cadence and failure handling (Section 4.4). |

The pass is a method on the Postgres repository (`AssignQueuedJobs`) and its in-memory twin. The loop lives in the coordinator beside the sweeper (Section 5.4). Blueprint Section 6 "recording structured audit logs for every decision" is satisfied because the pass writes one `JOB_ASSIGNED` event per decision with the candidate list.

### 2.2 Eligibility of a worker

A worker `w` is eligible for job `j` at pass time when all hold:

| Check | Source | Why |
|---|---|---|
| `status = 'HEALTHY'` | `workers.status` | Reaper-maintained health. |
| `last_heartbeat_at > clock_timestamp() - MINICLOUD_WORKER_TIMEOUT` | `workers.last_heartbeat_at` | Status changes only on the reaper tick (C10), so a worker can still be `HEALTHY` for up to the timeout plus one reaper interval after its heartbeats stop. This check closes that gap. |
| `last_poll_at > clock_timestamp() - MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` | new column `workers.last_poll_at` | **Poll freshness.** A worker that is executing does not poll (C12). Assigning to it would lapse the window every time. Only workers that polled within the window can accept, so busy workers are skipped by construction, which also handles slot accounting in practice (Section 2.4). |
| `available_slots - reservations(w) > 0` | `worker_capacity.available_slots` minus pending assignments | Section 2.4. |
| `j.task_type IN supported_tasks(w)` | `workers.supported_tasks` | Same rule as `DispatchNextJob`. |

`last_poll_at` is set by every poll that reaches the worker's row while it is `HEALTHY`: on a claim (inside the claim transaction) and on a 204. The 204 write is a standalone `UPDATE workers` in its own transaction, which holds no other lock and so cannot form a cycle. It is throttled to at most one write per second per worker (`last_poll_at < now - 1 s`), so an idle worker at a 200 ms cadence writes about once per second, not five times. Polls from `UNHEALTHY` workers keep their 503 and do not refresh `last_poll_at`, consistent with the rule that such polls are not liveness (protocol Section 4.3).

### 2.3 Deterministic round-robin: the rule, its state and its restart behaviour

**Rule.** For one decision about job `j` with task type `t`, let `E(t)` be the eligible workers that support `t` and have free slots at the start of the pass, sorted by worker ID in byte order. Let `c` be the cursor, the ID of the worker chosen by the most recent decision (empty string when none). Choose

    w = min { x in E(t) : x > c }   if that set is non-empty
    w = min E(t)                    otherwise

then set `c = w`.

**Why worker ID and not an index.** The current `RoundRobinScheduler.SelectWorker` uses `lastIndex` modulo the eligible-list length (C5). When a worker joins or leaves, the index points at a different worker, so the rotation skews. A cursor over worker IDs is stable under joins and leaves: the successor of the cursor is well defined whatever the set is.

**Cursor storage.** A singleton table `assignment_cursor` (Section 5.3) holds `last_worker_id`. It is read at the start of the pass and written once at its end, in the same transaction as the assignments. Consequences:

- **Restart.** A restarted coordinator reads the cursor from the database, so the rotation continues where it stopped. No in-memory state exists to lose.
- **Crash mid-pass.** The pass is one transaction, so either all its assignments and the new cursor commit, or none do.
- **Several coordinators.** Only one pass runs at a time, enforced by `pg_advisory_xact_lock` (Section 4.2). The cursor is never written by a second pass concurrently.
- **Cursor cleanup.** If the cursor's worker is deleted, the foreign key sets it to NULL and the rule starts from the lowest ID. Workers are not deleted in V1 (C9), so this is a corner case only.

**Join and leave.**
- A worker that joins with an ID after the cursor is picked up at its turn. One that joins with a smaller ID is picked up on the next wrap.
- A worker that leaves (UNHEALTHY, or stops polling) drops out of `E(t)`. The cursor then selects the successor of its last value, which is the intended rotation.
- A worker that leaves while the cursor is on it does not stall the rotation: the successor rule applies to the cursor value, not to a position.

**Tie rules.** Worker IDs are unique (primary key on `workers.id`), so the sort has no ties. Jobs are taken in `created_at, id` order (below), so the job order has no ties either.

**Supported-task matching.** The rule is applied per job, over the subset of `E` that supports the job's task type. The cursor is shared across task types: a decision for a job of type `t` moves the cursor even though other types' rotations are not affected. Consequence: the round-robin guarantee below is per task type over its own eligible set (Section 2.6).

**Pure function.** The rule is implemented as a pure function over a snapshot (cursor, sorted candidate IDs) → chosen ID. It has no clock, no I/O and no state beyond its arguments, so the property test (Section 6.3, INV-4) can drive it directly. It lives in a new file under `internal/scheduler` beside the existing `RoundRobinScheduler`, which is left untouched so its unit tests keep passing.

### 2.4 Free-slot accounting

Reservations are the core of the accounting. Without them one worker with one free slot could be given five jobs and lapse four of them.

At pass start, for each candidate worker:

    free(w) = worker_capacity.available_slots(w)
              - count(jobs j where j.assigned_worker_id = w
                      and j.assignment_expires_at > now
                      and j.state in (QUEUED, RETRYING))

Each decision in the pass decrements `free(w)` in memory, so a single pass cannot over-assign. The worker row is not locked by the pass (Section 4.3), so `free(w)` may be briefly stale. This is safe because the claim re-checks capacity under the worker's `FOR UPDATE` lock (as `DispatchNextJob` does today, C1). A stale estimate can at worst produce one avoidable lapse, and it is not a correctness fault.

### 2.5 FIFO order within the queue

The pass takes candidates in `ORDER BY created_at ASC, id ASC`, the same order as `DispatchNextJob` (C1). The claim takes a worker's own assignments in the same order. Consequences:

- Within a task type, jobs are assigned in submission order, and a worker's assignments are claimed in submission order.
- A job that cannot be placed (no eligible worker for its type) is **skipped**, not blocking. Jobs of other types behind it are still assigned. This avoids head-of-line blocking across task types. Within one type, a job with no eligible worker blocks nothing, because every job of that type sees the same empty `E`.

### 2.6 Starvation and fairness

**Statement (round-robin distribution).** Let `K` consecutive decisions for one task type `t` all see the same eligible set `E(t)`. Then the number of those decisions assigned to any two workers in `E(t)` differs by at most 1.

*Argument.* Within a constant `E`, the successor function is a fixed cyclic order over `E`. Each decision moves the cursor one step along that cycle. So after `K` steps each worker has been selected `floor(K/|E|)` or `ceil(K/|E|)` times. The cursor is persisted (Section 2.3), so the cycle position carries across passes and restarts.

**Scope of the statement.**
- It holds per task type. Workers that support different task types get different shares. This is intended: round-robin fairness is among workers eligible for the same work.
- It holds over segments where `E` is constant. When `E` changes (join, leave, slot exhaustion), the cycle re-anchors. Over a window containing `m` changes the imbalance is bounded by roughly `m + 1`; the test (INV-4) asserts the constant-`E` form only.
- It counts **decisions**, not executions. A lapse is a decision followed by a non-execution. Lapses move the rotation on by design (the lapsed worker is not picked again until the cycle returns), so under lapses the counts of *executions* can diverge from the counts of *decisions*. The invariant is stated on decisions because that is what the scheduler controls.

**Starvation.** Within a constant `E(t)`, every eligible worker is selected within `|E(t)|` decisions for type `t`. A worker that is eligible is never skipped more than once per cycle. A worker is starved only if it never becomes eligible (no fresh poll, no slots, or unhealthy), and in that case it is not owed work.

**Idle worker while work waits.** The rule skips non-polling workers (Section 2.2), so an idle, polling worker is not skipped in favour of a busy one. The cost of a wrong choice is one accept window per lapse, bounded by the lapse limit (Section 3.3). Under Candidate B the cost was a hold on every poll; here the cost is paid only when a chosen worker fails to poll.

**Latency at submit.** A submit triggers a pass immediately. The worst case without a trigger is one timer interval (1 s by default) plus the worker's poll interval (200 ms when idle, C12).

### Rejected alternatives for Section 2 (summary)
- Assignment inside the poll: rejected (it is Candidate B under another name).
- Assignment only at submit: rejected (no reaction to joins, leaves or freed slots; no `RETRYING` support).
- In-memory cursor (as `RoundRobinScheduler` does): rejected (lost on restart; wrong under several coordinators; index-based, see C5).
- Cursor by index over the sorted list: rejected (rotation skews on join or leave).
- Weighting by slots: not chosen. The rule gives each eligible worker one turn per cycle regardless of slot count. Weighted round-robin is a V2 policy question, not part of this slice.

---

## 3. The accept window

### 3.1 Length and name

- Config: `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW`, default **5 s**, allowed **2 s to 30 s**, validated at startup in `internal/config/config.go`. Out-of-range values are a startup error.
- Justification for 5 s: a worker in its worst poll backoff (3 s, C12) plus one request round trip must still land inside the window. The window must also exceed the worker's idle cadence (200 ms) with margin. 5 s is the value the Phase 4 plan named as an example (docs/phase4-plan.md Section 3).
- The same window is the poll-freshness horizon (Section 2.2), so one knob governs both.

### 3.2 What the worker does on poll

- **Poll returns assigned work only, plus the fallback.** A poll by `w` claims a job that is either (a) assigned to `w` with `assignment_expires_at > clock_timestamp()`, or (b) unassigned with `assignment_lapses >= L` (fallback, D9). It never claims an unassigned job below the lapse limit and never claims a job assigned to another worker.
- **Response.** The 200 body is unchanged except for an additive `policy` field (`round_robin` or `round_robin_fallback`, Section 5.1). Otherwise the worker sees the same `assignment` as today: lease token, lease expiry and duration (protocol Section 6.2).
- **Poll with nothing to claim.** 204 `NO_QUEUED_JOBS`, meaning "no job assigned to you and no fallback job". The worker's existing 200 ms sleep applies.
- **Poll from an UNHEALTHY worker.** 503 `WORKER_UNHEALTHY`, unchanged.
- **Fallback decision (D9).** After `L` lapses a job is no longer round-robin-assigned. The trade-off: a round-robin guarantee weakens for that job, in exchange for bounded wait. Recommended; see Q2.

Why the claim is a single transaction that uses the existing dispatch core: it is `DispatchNextJob` with a different candidate predicate (assigned-to-me or fallback), plus clearing of the assignment columns and `last_poll_at` maintenance. The shared core keeps the lock order and capacity re-check identical to the path that already passes the existing tests.

### 3.3 What happens when the window lapses

1. The next pass (or the sweep step it contains) finds `assignment_expires_at <= clock_timestamp()` on an assignment whose worker is `HEALTHY`.
2. It clears the assignment and increments `assignment_lapses`, unless a grace window is active (Section 4.4). It writes `JOB_ASSIGNMENT_LAPSED`.
3. The same pass, if it reaches the job, assigns it to the **next** eligible worker. The cursor was left on the lapsed worker, so the successor rule moves past it. "Next worker" therefore needs no per-job memory of tried workers.
4. Each lapse costs at least one window (5 s). After `L` lapses (default 3) the job becomes fallback-claimable (Section 3.2).

**Counting.** Lapses are per job, reset to 0 on any successful claim. They are separate from `attempt_no` and `max_attempts`.

**Poison-by-lapse loop.** Without a bound, a job could cycle forever between assignment and lapse if every chosen worker is alive but never polls (for example, a worker stuck after its heartbeat but not its poll loop). No attempt is created, so the existing `max_attempts` bound does not apply. The lapse limit is the bound. Worst case per job before fallback: `L` windows plus `L` pass intervals, about 15 s + 3 s with defaults. The test `TestFault_AssignmentLapseLoop_BoundedByLimit` (planned, Section 6.4) proves the bound.

### 3.4 When the assigned worker dies or deregisters

- **Dies (heartbeats stop).** The reaper marks it `UNHEALTHY` (C10). The next pass revokes its assignments at once (`JOB_ASSIGNMENT_REVOKED`), **without** incrementing lapses. The job is then reassigned to a healthy worker. This is not the chosen worker's fault in a way the lapse counter should punish, and a dead worker should not push a job toward fallback.
- **Deregisters.** There is no deregistration API (C9). If one is added, it must set `UNHEALTHY` or a new terminal status, and the same revoke path applies.
- **Slow (heartbeats fine, no poll).** The window lapses and the lapse is counted. This is the case the lapse counter exists for.

### 3.5 Late accept after reassignment (stale token handling)

Assignment never issues a token; the claim does. The possible late accepts are:

| Situation | Claim result | Why |
|---|---|---|
| Old worker `W1` polls after its window lapsed, job not yet revoked | 204 | The predicate requires `assignment_expires_at > clock_timestamp()` on database time. |
| Old worker `W1` polls after revoke, job reassigned to `W2` | 204 | The predicate requires `assigned_worker_id = W1`. |
| `W1` polls while the pass holds the job lock | 204 | The claim uses `FOR UPDATE SKIP LOCKED` (as today), so it skips the locked row and returns 204. Safe: the poll retries after 200 ms. |
| `W1` polls after its claim committed | 204 | The job is `RUNNING` with `W1`'s attempt; `W1` already has the token from the first response. |

So no "stale token" exists to reject: a token is only ever given to the worker that claimed, and the claim is the only issuer. Any later commit or renewal by `W1` with that token goes through the unchanged attempt checks (ADR 0014 Section 5, protocol Section 6.3).

### 3.6 Idempotency of poll retries

A poll is **not** idempotent, and this design does not make it so.

- If the claim commits and its response is lost (network drop, worker crash), the job is `RUNNING` with an attempt whose token the worker never received. The worker's retry gets 204. The attempt is then reaped by the lease sweeper to `LOST` after `lease_expires_at` (30 s by default), and it **consumes one attempt** (Section 1.4). This is FS-16 in Section 6.1.
- Storing the raw token to make the retry return it would contradict ADR 0011 Section 5 (only hashes are stored). A client-generated `poll_id` stored on the attempt would let the coordinator recognise a retry, but it could not return the token, so it would still lose the attempt. It was rejected.
- A two-step accept (poll returns the assignment without claiming, then `POST` accept claims it) would not remove the lost-response case either: the response to the accept can be lost in the same way. It also adds a state with no lease and a new endpoint. Rejected for V1.

The residual cost (one lease period and one attempt per lost claim response) is recorded as open question Q3.

### Rejected alternatives for Section 3 (summary)
- Two-step accept endpoint: rejected (same lost-response hole; extra state and endpoint).
- Storing the token so a poll retry can return it: rejected (contradicts ADR 0011 Section 5).
- Unbounded reassignment: rejected (poison-by-lapse loop, see Section 3.3).
- Lapses counted as attempts: rejected (a lapse is not an execution; a slow poll would exhaust `max_attempts` without any task having run).

---

## 4. Concurrency

### 4.1 One coordinator

`PollWork` holds the service mutex `s.mu` for the whole claim (C4). The assignment pass and the sweeper run under the same mutex in the single-coordinator build, so the pass and the claim never interleave inside one process. Correctness does not rely on the mutex alone: each path also takes database row locks and uses state-guarded updates, so the design stays correct if the mutex is later removed.

### 4.2 Several coordinators (stance)

Design stance: **the database is the only correctness boundary.** The mutex is a performance measure, not a guarantee.

| Hazard | Protection |
|---|---|
| Two claims take the same job | `FOR UPDATE SKIP LOCKED` on the candidate and the state predicate in the `UPDATE`; the partial unique index `idx_attempts_active_per_job` is the backstop. |
| Two passes write the cursor or double-assign a job | `pg_advisory_xact_lock` with a fixed key (the same mechanism the migrator uses, `internal/storage/postgres/migrator.go`), taken first in every pass. A second pass blocks until the first commits, then reads the new cursor. |
| Pass assigns a job while a claim or cancel holds it | The pass locks candidates with `FOR UPDATE SKIP LOCKED` and skips locked rows. |
| Revoke races a claim | Both take the job row lock; the claim's predicate on `assigned_worker_id` and `assignment_expires_at` decides the winner. |

Multi-coordinator operation is **not claimed as tested** by this design. It is listed as a limit (Section 8, Q9). V1 scope and the blueprint's Phase 7 (ADR 0004) decide whether it must be supported.

### 4.3 Lock order

ADR 0013 and ADR 0014 fix the canonical order `jobs -> workers -> attempts -> results -> job_events`. The new paths follow it:

| Path | Lock sequence | Notes |
|---|---|---|
| Advisory pass lock | `pg_advisory_xact_lock(key)` first | Not a row lock. Taken before any row lock, so no cycle with row-lock holders. |
| Assignment pass: revoke | `jobs` (`FOR UPDATE SKIP LOCKED`, subquery) then `workers` read (no `FOR UPDATE`) | Writes `jobs`, `job_events`. |
| Assignment pass: assign | `jobs` (candidates, `SKIP LOCKED`), worker snapshot read (no lock), `jobs` update, `job_events` insert, then `assignment_cursor` update last | `assignment_cursor` is written only under the advisory lock and read by no other path, so it needs no place in the order. |
| Claim (poll 200) | `jobs` (`SKIP LOCKED`) then `workers` (`FOR UPDATE`) then `attempts` insert, `job_events` insert | Identical to `DispatchNextJob` today. |
| Poll 204 freshness write | `workers` only, own transaction | Holds nothing else, so it cannot close a cycle. |
| Cancel of queued or retrying job | `jobs` then `job_events` | Clears assignment in the same statement. |
| Sweeper (unchanged) | `jobs` (`SKIP LOCKED`), `workers`, `attempts`, `results`, `job_events` | Unchanged, ADR 0014 Section 4. |

The pass never locks `attempts` and the sweeper never locks `assignment_cursor`, so the two loops cannot wait on each other in a cycle.

The pass uses `SKIP LOCKED` on every job lock. It therefore never waits on a job, only on a worker row lock taken by a claim (which holds that lock briefly). The pass sets `lock_timeout` on the worker read to bound that wait, as the sweeper does (`lockTimeoutSetting` in `internal/storage/postgres/leases.go`).

### 4.4 Crash points, recovery and grace

| Crash or fault point | State after | Recovery |
|---|---|---|
| Before the pass commits | Nothing changed (one transaction). | Next pass redoes the decisions. |
| After the pass commits, before anything else | Assignments and cursor durable. | Workers poll; windows run on database time; expiry is handled by a later pass. |
| During a claim, before commit | Nothing changed. | Worker's poll gets an error; retries. |
| After claim commit, response lost | `RUNNING` attempt, no executor. | Lease expires; sweeper reaps to `LOST`; retry consumes one attempt (Section 3.6). |
| Coordinator restart | Cursor, assignments, lapse counts durable. Grace state in memory is lost. | Startup grace (`MINICLOUD_STARTUP_GRACE_PERIOD`, default 30 s) starts on the first tick. See below. |
| Database outage | Passes fail. Expiry continues on database time. | Outage grace (`MINICLOUD_OUTAGE_GRACE_WINDOW`, default 30 s) opens on the first successful tick after the failure (ADR 0014 Section 4). |

**Grace applies to lapse counting, not to revocation.** Two rules:

- During an active grace window (startup or outage), an expired assignment is still **revoked** (so the job is not stuck), but the lapse is **not counted** (`counted: false`). A worker that could not poll because the control plane was down must not be pushed toward fallback by it.
- Outside grace, a lapse counts only for a `HEALTHY` worker (Section 3.3).

The grace state is passed into the pass as an input value, exactly as `SweepExpiredLeases` receives `domain.SweepGraceState`. The pass neither owns nor advances it. The only authoritative check is inside the pass transaction, under the job locks.

**Why revoke during grace and not freeze the assignment.** Freezing would keep a job assigned to a worker that may be gone for the whole grace period (up to 30 s), which is a latency cost with no safety benefit, since no attempt exists to protect.

---

## 5. Compatibility

### 5.1 Protocol changes

All changes are additive under the `/v1` rule (protocol Section 5.1). No endpoint is added or removed. No error code is added.

| Surface | Change | Exact form | Status codes |
|---|---|---|---|
| `POST /v1/workers/{worker_id}/poll` 200 | Additive field in `assignment`. | `"policy": "round_robin"` or `"round_robin_fallback"`; `"fifo_pull"` when the policy is `fifo_pull`. | unchanged (200) |
| `POST /v1/workers/{worker_id}/poll` 204 | Meaning narrowed: no job assigned to this worker and no fallback job. Wire form unchanged. | `NO_QUEUED_JOBS` | unchanged (204) |
| `POST /v1/workers/{worker_id}/poll` 503 | Unchanged. | `WORKER_UNHEALTHY` | 503 |
| `GET /v1/jobs/{job_id}` | Additive fields on the job view (`domain.Job` JSON). | `assigned_worker_id` (nullable), `assignment_expires_at` (nullable), `assignment_lapses` (integer, 0 when none). | unchanged |
| `POST /v1/workers/{id}/heartbeat`, `/renew`, `/result`, `POST /v1/jobs`, `POST /v1/jobs/{id}/cancel` | No change. | | unchanged |

Worker-side: the current worker decodes the poll response by field name, so it ignores the new `policy` field. No worker change is needed for correctness. The synchronous poll loop (C12) is a separate issue, Q5.

### 5.2 `fifo_pull` stays selectable

- `MINICLOUD_DISPATCH_POLICY` accepts `round_robin` and `fifo_pull`. Default is `fifo_pull` until the gate is passed; then `round_robin` (Q1).
- The policy selects the **claim predicate and whether the pass runs**. Under `fifo_pull` the pass does not run, and `DispatchNextJob` is used unchanged except for one addition: its `UPDATE` clears the three assignment columns (Section 5.3), so a policy switch cannot leave a non-NULL assignment on a `RUNNING` job (rejected by the CHECK).
- `fifo_pull` is kept for three reasons: it is the Phase 3 baseline; ADR 0011 requires identical FIFO pull semantics for the V2 Redis comparison; and it is the rollback path.
- Log lines differ by policy, as ADR 0011 requires: `fifo_pull` logs `policy="fifo_pull"`, `reason="claimed by polling worker with capacity"` and nothing about a scheduler. The round-robin pass logs its candidates and selection, because the scheduler is actually making the decision there.

### 5.3 Migration `000005` (forward-only, ADR 0010)

Name: `000005_assignment_round_robin.sql`, in `internal/storage/postgres/migrations/` beside `000004_leases_retries_cancellation.sql`. Exact DDL sketch. The NOT VALID then VALIDATE pattern follows `000003` and `000004`.

```sql
-- 000005_assignment_round_robin.sql (proposed; not in tree)

-- Step 1: assignment columns on jobs. All nullable: existing QUEUED and RETRYING rows
-- become unassigned, which is the correct starting state.
ALTER TABLE jobs
    ADD COLUMN assigned_worker_id    TEXT REFERENCES workers(id) ON DELETE SET NULL,
    ADD COLUMN assigned_at           TIMESTAMPTZ,
    ADD COLUMN assignment_expires_at TIMESTAMPTZ,
    ADD COLUMN assignment_lapses     INTEGER NOT NULL DEFAULT 0;

-- Step 2: the three assignment columns are all set or all NULL; a non-NULL assignment is
-- only valid on a job waiting for dispatch; lapses are never negative.
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

-- Step 3: poll freshness. NULL means never polled, so the worker is not assignable.
ALTER TABLE workers
    ADD COLUMN last_poll_at TIMESTAMPTZ;

-- Step 4: the round-robin cursor. One row, written only by the assignment pass.
CREATE TABLE assignment_cursor (
    singleton      BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_worker_id TEXT REFERENCES workers(id) ON DELETE SET NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO assignment_cursor (singleton) VALUES (TRUE);

-- Step 5: indexes. Assigned-job lookup for the claim and the pass revoke step; expiry scan;
-- the claim's candidate scan for the fallback case.
CREATE INDEX idx_jobs_assigned_worker
    ON jobs (assigned_worker_id, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NOT NULL;

CREATE INDEX idx_jobs_assignment_expiry
    ON jobs (assignment_expires_at ASC)
    WHERE assigned_worker_id IS NOT NULL;

CREATE INDEX idx_jobs_unassigned_fallback
    ON jobs (assignment_lapses, created_at ASC, id ASC)
    WHERE assigned_worker_id IS NULL AND state IN ('QUEUED', 'RETRYING');
```

Notes on the sketch:
- `job_events` needs no DDL (C8). The new event types are Go constants only.
- No change to `chk_jobs_state`. The `REJECTED` gap (C7) is pre-existing and is raised as Q8, not fixed here.
- No change to `chk_attempts_state` or to any attempt index.
- `ON DELETE SET NULL` on `assigned_worker_id` matches ADR 0011 Section 2.4. Workers are not deleted in V1, so it is a backstop.
- The shape CHECK is the enforcement that every state-changing path clears the columns. Mutation INV-2b (Section 6.3) proves a path that forgets is caught.

**Migration test.** A `migration_000005_test.go` (proposed) should follow the shape of the existing `migration_000004_test.go`. Check first whether any test pins the latest migration number (for example in `migrator_test.go`); I did not verify this and it must be checked in slice 5b.

### 5.4 Existing tests and in-memory parity

- **Default policy in tests.** `NewService` and the repositories default to `fifo_pull` behaviour (the claim predicate and pass are opt-in). Existing coordinator and API tests therefore run unchanged. The default changes only in `cmd/coordinator/main.go` configuration after the gate.
- **Existing dispatch tests.** `DispatchNextJob` changes by one clause (clearing the three columns), which is a no-op when they are NULL. The existing skip-locked test `TestPostgresRepository_DispatchNextJob_SkipsLockedHead` (in `internal/storage/postgres/dispatch_skiplocked_test.go`) is the regression gate for this change.
- **Scheduler tests.** `internal/scheduler/round_robin_test.go` tests the index-based `RoundRobinScheduler`, which is unchanged.
- **Coordinator constructors.** `NewService` keeps its `scheduler.Scheduler` parameter. The new pure function is not reached through that interface.
- **In-memory parity.** Every behaviour in this ADR is implemented twice: in `internal/storage/inmemory.go` (with the injected clock, a mutex instead of locks, and `jobOrder`, which equals creation order, so FIFO ties agree) and in the Postgres repository. Parity is enforced by shared conformance tests in `internal/storage/storagetest/conformance.go` run against both repositories, as the earlier Phase 4 slices do for leases.
- **Known parity difference (existing, not created here).** Postgres orders by `created_at, id`; memory orders by insertion. They agree when `created_at` is strictly increasing. This is recorded so the conformance tests do not assume tie behaviour.

---

## 6. Failure and abuse model

### 6.1 New failure rows for [docs/failure-model.md](../failure-model.md)

These are proposed text for the failure model. This ADR does not edit that file.

| ID | Fault | Expected behaviour | Recovery | Scope |
|---|---|---|---|---|
| **FS-14** | Assigned worker is alive but never polls | Window lapses; lapse counted (outside grace); job reassigned to the next eligible worker. No attempt created. | Automatic; after `L` lapses the job becomes fallback-claimable. | Slice 5b and 5c |
| **FS-15** | Assigned worker dies before polling | Reaper marks `UNHEALTHY`; pass revokes at once without counting a lapse; job reassigned. | Automatic, within one pass interval after the reaper tick. | Slice 5b and 5c |
| **FS-16** | Claim committed; poll response lost | Attempt `RUNNING` with no executor; lease expires; sweeper reaps to `LOST`; consumes one attempt. | Automatic, bounded by `max_attempts`. | Existing lease path, now reachable through claim |
| **FS-17** | Coordinator crashes between assignment commit and claim | Assignment durable; expiry on database time; grace suppresses lapse counting on restart. | Automatic. | Slice 5c |
| **FS-18** | Cancel races with claim of an assigned job | Row lock serialises. Either `CANCELLED` and claim returns 204, or the claim wins and cancel sees `RUNNING` (`CANCEL_REQUESTED`). | Automatic. | Slice 5b |
| **FS-19** | Assignment lapse loop (every chosen worker lapses) | Bounded by `L`; then fallback. | Automatic after `L` windows. | Slice 5c |
| **FS-20** | Database outage during pass | Pass fails; failed tick opens outage grace on the next success; assignments expire on database time, lapses not counted during grace. | Automatic. | Slice 5c |

### 6.2 Abuse cases

Claims are unauthenticated in V1 (SEC-004, accepted gap). The cases below are only those the new design introduces or changes. Existing SEC-004 consequences are **not** re-reported here.

| Case | Effect | Bound or mitigation |
|---|---|---|
| A worker never polls, to waste assignments | Each assigned job waits one window per lapse. | `L` lapses then fallback (Section 3.3). Worst wait per job about 15 s with defaults. |
| A worker polls constantly to stay eligible and to win every decision | Its share of decisions equals every other eligible worker's (Section 2.6), no more. | Round-robin gives equal turns, not more. |
| A client registers many worker IDs (sybil) to take a larger share of jobs | Each fake worker that polls gets equal decisions. | SEC-004 accepted gap (unauthenticated registration). Not mitigated in this design. Recorded so the auth work (Phase 7) knows about it. |
| A worker registers with a large `total_slots` to look attractive | Round-robin ignores slot count for the turn (Section 2.3, rejected weighting). Slots only cap how many it can hold at once. | Per-worker turns, bounded by the existing `total_slots` limit (SEC-016 CHECK). |
| A worker polls on behalf of another worker ID | It can claim that worker's assignments. | Same root cause as SEC-004. Accepted gap. Not made worse by this design: today any poller claims any queued job. |
| Poll flood to force writes to `workers` | Each 204 may write `last_poll_at`. | Throttled to one write per worker per second (Section 2.2). |
| Poison payload that repeatedly crashes its worker | Each crash is a `LOST` attempt, bounded by `max_attempts` (unchanged). | ADR 0015 Section 2. Assignment adds no new path to it. |
| Coordinator-level flood of submits triggering passes | Each submit triggers a pass. | Pass is batch-limited to 50 candidates per transaction and serialised by the advisory lock. A submit storm delays passes but cannot start unbounded work. |

### 6.3 Invariants, tests and mutations

Each invariant names the test that would catch its violation and the mutation that the test must kill. Test names are **planned**; none exist yet.

| ID | Invariant | Test (planned) | Mutation that must fail the test |
|---|---|---|---|
| INV-1 | A job is never executed by two workers at once: at most one `RUNNING` attempt per job, and each claim has exactly one winner. | `TestPostgresRepository_ClaimAssigned_ConcurrentPollsOneWinner` (planned test): N concurrent polls, exactly one 200, the rest 204, one attempt row. | Remove both `FOR UPDATE SKIP LOCKED` and the `state IN ('QUEUED','RETRYING')` predicate from the claim. The second claim must then fail on `idx_attempts_active_per_job` (error other than 204), or insert a second attempt; either fails the test. |
| INV-2 | Assignment is a reservation: it creates no attempt and changes no `attempt_no` or `max_attempts`. | `TestConformance_Assign_CreatesNoAttempt` (planned test): attempt count before and after the pass is equal. | Insert a `PENDING` attempt in the pass. |
| INV-2b | Every state-changing path clears the assignment columns (the shape CHECK holds). | `TestPostgresRepository_CancelAssigned_ClearsAssignment` (planned test) and one case per path (commit, sweep, `DispatchNextJob`, cancel). | Remove `assigned_worker_id = NULL` from `CancelJob`. The CHECK must then reject the update and the test must fail. |
| INV-3 | Only the assigned worker, and only inside the window, can claim an assignment. | `TestConformance_ClaimAssigned_OnlyAssignedWithinWindow` (planned test): wrong worker gets 204; expired assignment gets 204. | (a) drop `assigned_worker_id = $W` from the claim predicate; (b) drop `assignment_expires_at > clock_timestamp()`. Each must fail a separate case. |
| INV-4 | Round-robin distribution: over `K` decisions with a constant eligible set, counts differ by at most 1 (Section 2.6). | `TestProperty_RoundRobin_CountsWithinOne` (planned test): at least 1,000 random constant-`E` segments, using the pure function. | Reset the cursor to the lowest ID on every decision; or use an index instead of the worker-ID cursor. |
| INV-5 | The rotation survives coordinator restart. | `TestPostgresRepository_AssignmentCursor_SurvivesRestart` (planned test): pass, new repository instance, pass; the second pick continues the cycle. | Keep the cursor in process memory only (as `RoundRobinScheduler.lastIndex` is). |
| INV-6 | No job stays assigned past expiry plus one pass interval (database available). | `TestCoordinator_AssignmentSweep_RevokesWithinOneInterval` (planned test), with an injected clock. | Remove the revoke branch from the pass. |
| INV-7a | A lapse consumes no attempt budget. | `TestConformance_AssignmentLapse_ConsumesNoAttempt` (planned test): `attempt_no` and `max_attempts` unchanged after lapse. | Increment `attempt_no` or insert a placeholder attempt on lapse. |
| INV-7b | Lapses are bounded: after `L` lapses the job is fallback-claimable. | `TestConformance_AssignmentLapse_FallbackAfterLimit` (planned test) and `TestFault_AssignmentLapseLoop_BoundedByLimit` (planned test). | Remove the limit check (lapses grow without bound and no fallback occurs). |
| INV-8 | Cancel wins over assignment: a cancelled job is never claimed. | `TestConformance_CancelAssigned_NeverClaimed` (planned test). | Remove the state predicate from the claim and stop `CancelJob` clearing the assignment. The claim then returns a `CANCELLED` job; the test fails. |
| INV-9 | Grace suppresses lapse counting but not revocation. | `TestCoordinator_AssignmentSweep_GraceDoesNotCountLapse` (planned test) and `TestCoordinator_AssignmentSweep_GraceStillRevokes` (planned test). | Ignore the grace input in the pass, so lapses are counted during grace; a second mutation: skip revocation under grace (job stuck). |
| INV-10 | Reservations count against slots: no worker is assigned more than its free slots. | `TestConformance_Assign_ReservationsCountAgainstSlots` (planned test): one-slot worker, two jobs, one assignment. | Subtract running attempts only, ignoring reservations. The second assignment then succeeds; the test fails. |
| INV-11 | Only fresh, healthy, polling workers are eligible. | `TestConformance_Assign_SkipsStaleWorkers` (planned test): stale poll, stale heartbeat, `UNHEALTHY` each skipped. | Drop each freshness predicate in turn. |
| INV-12 | `fifo_pull` behaviour is unchanged: its claim ignores assignment columns. | `TestConformance_FifoPull_IgnoresAssignmentColumns` (planned test), plus the existing `DispatchNextJob` tests. | Make the fifo claim honour `assigned_worker_id`. |
| INV-13 | Lock order `jobs -> workers -> attempts` holds for the new paths: no deadlock with the sweeper or a claim. | `TestPostgresRepository_AssignmentPass_NoDeadlockWithClaim` (planned test): a second connection holds a worker row lock while the pass runs; the test asserts the pass completes or times out, and that PostgreSQL reports no `40P01`. | Make the pass lock `workers` before `jobs`. PostgreSQL then reports `40P01` against a concurrent claim, and the test fails. |

### 6.4 Fault-injection and API abuse tests (all planned)

| Test (planned test) | Proves | Fault or abuse |
|---|---|---|
| `TestFault_AssignedWorkerNeverPolls` (planned test) | FS-14: reassignment after one lapse; the lapse is counted; no attempt is created. | Fault |
| `TestFault_AssignedWorkerDiesBeforePoll` (planned test) | FS-15: revoke on `UNHEALTHY` without a counted lapse. | Fault |
| `TestFault_PollResponseLostAfterClaim` (planned test) | FS-16: the claim commits, the response is dropped, the attempt is reaped to `LOST` and consumes one attempt. Documents the Q3 cost. | Fault |
| `TestFault_CoordinatorCrash_BetweenAssignAndPoll` (planned test) | FS-17: assignment survives restart; grace prevents counting. | Fault |
| `TestFault_CancelRacesAccept` (planned test) | FS-18: row-lock serialisation of cancel and claim. | Fault |
| `TestFault_AssignmentLapseLoop_BoundedByLimit` (planned test) | FS-19: the fallback happens after `L` lapses and the job completes. | Fault |
| `TestFault_DatabaseOutage_DuringPass` (planned test) | FS-20: pass failure and outage grace; no counted lapses. | Fault |
| `TestAPI_Poll_ForeignAssignment_Returns204` (planned test) | A poll by a non-assigned worker never receives another worker's job. | API abuse |
| `TestAPI_Poll_UnhealthyWorker_Still503` (planned test) | Unchanged 503 `WORKER_UNHEALTHY`. | API compatibility |
| `TestAPI_Poll_Flood_ThrottlesFreshnessWrites` (planned test) | At most one `last_poll_at` write per worker per second under a poll flood. Measured by counting writes through a repository wrapper, not by timing. | API abuse |
| `TestAPI_Job_AssignmentFieldsAreAdditive` (planned test) | `GET /v1/jobs/{job_id}` gains the three fields and existing fields are unchanged. | Protocol compatibility |
| `TestConformance_PollResponse_PolicyField` (planned test) | The 200 poll body carries `policy` with the correct value per path (`round_robin`, `round_robin_fallback`, `fifo_pull`). | Protocol |

Property tests: INV-4 runs at least 1,000 iterations. The state-machine model test for the assignment sub-state (planned test `TestProperty_AssignmentSubState_ModelConformance`) drives random sequences of submit, pass, poll, cancel, reap, and clock advance against a reference model and checks INV-1, INV-3, INV-6 and INV-8 after every step.

Every new guard needs a mutation proof run in a throwaway worktree under `~/mc-mut-claude`, as [AGENTS.md](../../AGENTS.md) Section 6 requires. The mutations above are the set to run.

---

## 7. Slice plan

Three slices. Each is independently mergeable behind `MINICLOUD_DISPATCH_POLICY=fifo_pull` (the default until the gate), so `main` stays on `fifo_pull` throughout.

### Slice 5a: domain, pure policy and in-memory parity (no database)

- **Scope.** Assignment fields and the poll freshness field on the domain types. Three new event-type constants. The pure round-robin function and the lapse-policy function. In-memory assignment pass, claim, revoke, and cancel clearing. Coordinator policy wiring and config validation. No migration, no Postgres code.
- **Files (existing, to edit):** `internal/domain/job.go` (assignment fields, JSON tags `omitempty`), `internal/domain/worker.go` (`LastPollAt`), `internal/domain/events.go` (three event types), `internal/storage/inmemory.go` (assignment pass, claim predicate, revoke, cancel clearing), `internal/storage/storagetest/conformance.go` (shared tests), `internal/coordinator/service.go` (policy selection in `PollWork`; constructor unchanged), `internal/config/config.go` (four variables, validated).
- **Files (new, proposed):** a domain file for assignment rules, a pure function file beside `internal/scheduler/round_robin.go`, a coordinator loop file beside `internal/coordinator/sweeper.go`, and the in-memory and conformance tests for these.
- **Tests (planned).** INV-1 (memory variant), INV-2, INV-3, INV-4 (property), INV-6, INV-7a, INV-7b, INV-8, INV-10, INV-11, INV-12, `TestConformance_PollResponse_PolicyField`, and `TestProperty_AssignmentSubState_ModelConformance` (memory only).
- **Verification commands.**
  - `test -z "$(gofmt -l .)"`
  - `go vet ./...`
  - `go build ./...`
  - `make test-docs`
  - `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...` (memory only; database tests skip)
  - Mutation proofs for INV-3, INV-4, INV-7b, INV-8, INV-10 in `~/mc-mut-claude`, each showing the test fails under the mutation and passes after restore.
- **Independent audit.** The pure round-robin function (INV-4 and the argument in Section 2.6) and the lapse and fallback rules (Section 3.3).

### Slice 5b: migration 000005 and the Postgres repository

- **Scope.** Migration `000005`, Postgres assignment pass with the advisory lock, claim through the shared dispatch core, revoke step, cancel and sweeper clearing, `DispatchNextJob` clearing of the three columns, and the freshness write. Shared conformance run against Postgres. Concurrency tests.
- **Files (existing, to edit):** `internal/storage/postgres/repository.go` (`DispatchNextJob` clearing; `CancelJob` clearing; claim core; freshness write), `internal/storage/postgres/leases.go` (sweeper clearing on terminal transitions), `internal/storage/repository.go` (new interface methods), `internal/storage/postgres/migrator_test.go` (only if it pins the latest version; see Section 5.3).
- **Files (new, proposed):** `000005_assignment_round_robin.sql` in `internal/storage/postgres/migrations/`, a Postgres assignment file in `internal/storage/postgres/`, and a migration test file following `migration_000004_test.go`.
- **Tests (planned).** INV-1 (Postgres variant), INV-2b, INV-5, INV-9, INV-13, `TestPostgresRepository_AssignmentCursor_SurvivesRestart`, `TestPostgresRepository_CancelAssigned_ClearsAssignment`, and a migration test that applies `000005` to a migrated database and to a fresh one.
- **Verification commands.**
  - All slice 5a commands
  - `make test-db`, with its output redirected to a log file in the session scratchpad and the tail pasted verbatim. It needs the shared test database, so it runs where that database is available. This design makes no claim about its result.
  - Mutation proofs for INV-1 (Postgres), INV-2b, INV-13.
- **Independent audit.** The claim and pass SQL and the lock order (Section 4.3), and the CHECK-driven clearing (INV-2b). This slice must be audited by an agent that did not build it (AGENTS.md Section 7).

### Slice 5c: coordinator loop, grace, API fields, fault and abuse suites

- **Scope.** The assignment loop in the coordinator (timer plus submit trigger), grace state wiring, startup validation of the four variables, job view fields, the fault and API abuse suites in Section 6.4, and the property test at 1,000 iterations.
- **Files (existing, to edit):** `internal/coordinator/service.go` (submit trigger; loop start), `internal/coordinator/sweeper.go` (grace state passed to the pass), `cmd/coordinator/main.go` (start the loop; policy from config), `internal/api/handlers.go` (poll `policy` field; job fields are carried by the domain JSON).
- **Files (new, proposed):** fault tests under the `tests/` integration and fault directories, following the existing `tests/integration/` layout, and the API abuse tests beside `internal/api`.
- **Tests (planned).** INV-9 (loop variant), INV-6 (end to end), every test in Section 6.4, and `TestProperty_AssignmentSubState_ModelConformance` at 1,000 iterations.
- **Verification commands.** All previous commands, plus `make test-db` as above and `make test-race` (or the `-race -count=1` form). Report PASS, SKIP and FAIL counts separately (AGENTS.md Section 5), because database tests skip without the URL.
- **Independent audit.** Grace interaction (INV-9) and the fallback behaviour under failure (FS-19, FS-20). The audit must also re-run the mutation proofs from slices 5a and 5b.

**Owner gate.** After slice 5c passes and is audited, the default policy may flip (Q1). The docs that must change then are owned by a docs task, not by this ADR: [docs/protocol.md](../protocol.md) (Sections 4.3 and 6), [docs/state-machine.md](../state-machine.md) (the `PENDING` assignment reference, Section 4), [docs/schema.md](../schema.md), [docs/failure-model.md](../failure-model.md) (FS-14 to FS-20), [docs/phase4-plan.md](../phase4-plan.md) Section 3, and the roadmap.

---

## 8. Open questions for the orchestrator

Product and policy decisions. Each has a recommendation. None is decided silently in this ADR.

| # | Question | Recommendation | Why it is a decision |
|---|---|---|---|
| **Q1** | When does `round_robin` become the default dispatch policy? | Flip after slice 5c is green and audited, behind an owner sign-off. Keep `fifo_pull` selectable permanently. | It changes the behaviour every deployment sees, and the Phase gate is the owner's. |
| **Q2** | After `L` lapses, should a job fall back to first-poller-wins, or keep being reassigned round-robin? | Fall back, with `L = 3`, logged as `round_robin_fallback`. | It trades the round-robin guarantee for a bounded wait for that job. The owner must accept that trade. |
| **Q3** | Should a claim whose response was lost (FS-16) consume `max_attempts`? | Yes, for now. Document the cost. Revisit when there is evidence from real runs. | It changes the retry budget semantics. The alternative needs a way to recognise a non-executed attempt, which the design cannot provide without storing tokens (ADR 0011 Section 5). |
| **Q4** | Is `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW = 5 s` (range 2 s to 30 s) acceptable? | Yes. | It sets the lapse cost and the freshness horizon. |
| **Q5** | The worker's poll loop is synchronous (C12), so a worker executes one job at a time whatever its `total_slots`. Keep this for V1, or schedule a worker-concurrency slice before V2 benchmarking? | Keep for V1 (this design is correct with it, since busy workers are skipped). Schedule concurrency before the V2 comparison, because fair benchmarking needs slot-level utilisation. | It changes worker throughput and the meaning of `total_slots` in practice. |
| **Q6** | Should `RETRYING` jobs be round-robin assigned, or only `QUEUED`? | Both, with the same eligibility as `DispatchNextJob`. | It changes which jobs get the guarantee. |
| **Q7** | Should the round-robin guarantee cover all task types as one cycle, or per task type? | Per task type (Section 2.3). | Stated in the rule; the owner should confirm it matches the blueprint's intent. |
| **Q8** | `chk_jobs_state` omits `REJECTED`, which `internal/domain/transition.go` defines (C7). Fix in migration `000005`, or in a separate task? | Separate task after confirming whether any code path persists `REJECTED`. I did not verify that. | It is a pre-existing gap, not part of this design, and fixing it in `000005` widens that migration. |
| **Q9** | Must V1 support several coordinators? | Not for V1. This design is database-safe for several coordinators but makes no tested claim (Section 4.2). | It is a scope decision (ADR 0004, Phase 7). |
| **Q10** | Expose assignment fields on `GET /v1/jobs/{job_id}`, and the `policy` field on poll? | Yes, both additive. | It makes assignments visible to clients and tests. The owner should approve the new public fields. |
| **Q11** | Should `DRAINING` be given a code path (a graceful deregister that revokes assignments) in this work? | No. Keep it out of V1 scope; revocation is already covered by `UNHEALTHY`. | It adds a new worker lifecycle, which is a product scope decision. |

---

## Consequences

### Positive
- Round-robin placement, with a persisted cursor that survives restarts and joins or leaves, using the existing lease and sweeper machinery for timing.
- Claims stay the only issuer of tokens and attempts. Assignment adds no attempt, no token and no lease to unaccepted work.
- `fifo_pull` is untouched in behaviour, so the V2 benchmark baseline stays valid.
- Every change is additive on the wire and forward-only in the database.

### Negative and trade-offs
- A lost claim response costs one lease period and one attempt (Q3).
- A slow poller costs up to one window per lapse, bounded by `L` (Q2).
- Each `last_poll_at` write adds a write per worker per second under polling.
- Under the synchronous worker loop (C12), slot counts rarely exceed one in practice (Q5).
- More moving parts in the coordinator: a pass, a cursor, an advisory lock, and new grace interactions.

### Invariants protected
- INV-1 to INV-13 (Section 6.3). ADR 0011 Section 1 (`SKIP LOCKED` dispatch), ADR 0013 (lock order and restart), ADR 0014 (lease and grace), ADR 0015 (attempt budget), ADR 0016 (cancel semantics).

## Limits of this design

- **Not measured.** No pass latency, no poll-freshness write cost, no distribution under load. Any number would be invented, so none is given (AGENTS.md Section 2).
- **Not tested.** No test in this ADR exists or has been run. All test names are planned.
- **Assumes database time** (ADR 0011 Section 5 item 5) and a single source of worker poll cadence (C12).
- **Fairness is over decisions and per task type** (Section 2.6), not over executions and not over slots.
- **Unauthenticated workers** (SEC-004). Sybil and impersonation are not defended here.
- **Multi-coordinator is designed for, not claimed** (Section 4.2).
- **Pre-existing gaps noted, not fixed:** `REJECTED` missing from `chk_jobs_state` (Q8); the in-memory and Postgres tie order (Section 5.4); the synchronous worker loop (Q5).
- **Docs not updated.** Per the task scope, only this file is new. The protocol, state-machine, schema, failure-model and phase-plan changes listed in Section 7 are for the docs task.
