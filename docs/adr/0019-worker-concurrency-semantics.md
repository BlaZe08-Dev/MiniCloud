# ADR 0019: Worker Concurrency Semantics

## Status
Accepted (2026-11-16, end of task V2-0 Stage B). Proposed in Stage A; approved by the orchestrator with the answers recorded in Section 6; implemented on `agent/claude/V2-0`; audited (APPROVE WITH FIXES, review V2-0 on branch `agent/claude/V2-0-audit`), fix round 1 applied (audit findings A-1 to A-4), awaiting merge.

Builds on ADR-0003 (pull protocol), ADR-0007 (liveness), ADR-0014 (leases), ADR-0016 (cancellation) and ADR-0017 (assignment round-robin, Section 2.4 and question Q5). Version gate: first V2 slice (ADR-0004, ADR-0018 roadmap).

Evidence labels follow `AGENTS.md` section 2. Sections 1 and 2 were written in Stage A against the V1 code and are **confirmed by code reading** unless labelled otherwise. The tests of Section 3 were **executed** in Stage B; the real output is in `docs/agents/handoffs/V2-0.md`.

## 1. Context

### 1.1 The defect
The worker runs one attempt at a time. `internal/worker/client.go` func `Run` calls `pollWork`, then calls `ExecuteAndReport` inline and polls again only when it returns (ADR-0017 C12 records the same fact). Yet func `register` sends `available_slots = TotalSlots` and func `sendHeartbeat` sends `available_slots = TotalSlots` on every beat. `TotalSlots` comes from `MINICLOUD_WORKER_SLOTS` (default 4, `internal/config/config.go` func `LoadWorkerConfig`, validated only as positive). The worker therefore advertises capacity it does not execute (`docs/post-v1/CURRENT_BASELINE.md`, finding carried into V2).

### 1.2 What the coordinator does with the number today
The coordinator **ignores** the reported `available_slots`:
- `internal/api/handlers.go` decodes the heartbeat field and passes it to `Service.Heartbeat`, which passes it to `RecordHeartbeat`. Both repositories discard it: `internal/storage/postgres/repository.go` func `RecordHeartbeat` and `internal/storage/inmemory.go` func `RecordHeartbeat` take it as `_ int`. `docs/protocol.md` ("Note on Available Slots") and SEC-055 record this.
- The register body is decoded into `domain.Worker`; `UpsertWorker` stores `total_slots` only.
- Capacity is derived from attempts: `available_slots = GREATEST(0, total_slots - count(attempts in PENDING or RUNNING))` in the `worker_capacity` view (migration 000002), and `computeAvailableSlotsLocked` in `internal/storage/inmemory.go` with the same rule.
- `domain.Worker.HasCapacity` (`internal/domain/worker.go`) reads `AvailableSlots`, which is always the derived value when it comes from a repository. Its only other production reader is `internal/scheduler/round_robin.go`, which is not called on the dispatch path (ADR-0011, ADR-0003 amendment).

So the wrong number is **informational today**: it reaches no dispatch decision. It matters because V2-1 (node capacity) and V2-3 (resource-aware placement) are planned to reason about worker capacity, and any consumer that trusts the reported field would inherit the error.

### 1.3 Decision already taken by the orchestrator
Real bounded concurrency: a slot is one concurrently executing attempt. This ADR records how. The alternative (clamp `TotalSlots` to 1) was rejected by the orchestrator: the config name, the README and ADR-0003 ("claim pending job attempts according to available capacity slots", "workers only request work when they have idle slots") already promise concurrency.

## 2. Decision

### 2.1 Definitions and where each is reported (item 1)

| Term | Definition | Reported in |
|---|---|---|
| Slot | Permission for one attempt to execute concurrently in this worker process. One slot is held from the moment the poll loop decides to ask for work until the attempt that poll produced has finished delivery (or the poll produced none). | not on the wire |
| `TotalSlots` | `MINICLOUD_WORKER_SLOTS`, the maximum number of attempts the worker executes at the same time. Name, default (4) and positive validation unchanged. Upper bound: Section 2.6. | `total_slots` in `POST /v1/workers/register` (unchanged) |
| running | Attempts handed to an attempt goroutine and not yet finished (`ExecuteAndReport` has not returned). An atomic counter, incremented by the poll loop before it starts the goroutine, decremented in that goroutine's deferred release. | not on the wire |
| `available_slots` | `TotalSlots - running`, read live at the moment the request body is built; never negative, never above `TotalSlots` (both guaranteed by construction, Section 2.3, and asserted in tests). | `available_slots` in register (initial and every re-register after a 404) and in every heartbeat |

The poll request (`POST /v1/workers/{id}/poll`) carries no body and stays that way; no slot field is added. A slot held by an in-flight poll that has not yet returned an assignment is **not** subtracted from `available_slots`: the field means attempts executing, as the orchestrator specified. The coordinator still ignores the field (Section 1.2), so this choice has no dispatch effect; it is recorded so V2-1 and V2-3 read the number with the right meaning.

### 2.2 Poll gating and the coordinator analysis (item 2)

**Rule.** The poll loop acquires a slot **before** `pollWork`. If no slot is free it blocks on the semaphore (or on `ctx.Done()`), and it sends no poll. A worker with zero free slots never polls. A poll that returns no assignment (204, `WORKER_UNHEALTHY`, an error, a nil assignment) releases its slot before the existing `pollSleep` or backoff, so the sleep and backoff code is unchanged.

**What the coordinator already enforces (confirmed by code reading).**
1. *Claim-time capacity, both policies.* fifo_pull: `internal/storage/postgres/repository.go` func `DispatchNextJob` pre-checks `worker_capacity.available_slots > 0`, then re-checks `total_slots - count(PENDING, RUNNING) > 0` under the worker row lock. round_robin: `internal/storage/postgres/assignment.go` func `ClaimForPoll` pre-checks the same view, and func `claimTx` re-checks under the worker row lock. The in-memory repository applies the same rule through `computeAvailableSlotsLocked`. So the coordinator never gives one worker more RUNNING attempts than its registered `total_slots`, whatever the worker reports.
2. *Reservations (ADR-0017 pass).* `assignChooseWorker` and the re-check under the worker lock require, besides health, heartbeat and poll freshness: fewer than `domain.EffectiveWorkerSlots` (= 1) PENDING or RUNNING attempts, `total_slots - active > 0`, and no live reservation. So at most **one** reservation per worker, and only for a worker with **zero** active attempts in the database. The in-memory pass does the same (`internal/storage/inmemory_assignment.go`).

**Can work be pre-assigned beyond real capacity and then expire unaccepted?** Real capacity is the worker's local free slots. A reservation needs zero active attempts in the database. The worker's local count exceeds the database count only in two windows:
- *Commit-to-release window:* the coordinator closes the attempt when it commits the result; the worker releases the slot a few statements later, when `ExecuteAndReport` returns. A reservation made in that window is accepted by the poll that follows the release, well inside the accept window (5 s minimum, `coordinator.MinAcceptWindow`). No lapse.
- *Zombie attempts (invariant 6):* every attempt the worker still executes was declared LOST in the database (lease expired while the worker kept running, for example during a partition). If all `TotalSlots` slots are held by such attempts, the worker does not poll, its `last_poll_at` was written by the poll that filled its last slot, and that attempt must itself have become LOST, which needs at least one lease duration without a successful renewal. With the default lease (30 s) and accept window (5 s) the poll is stale long before, so the worker is not eligible. Only a configuration with `MINICLOUD_LEASE_DURATION` shorter than the accept window could leave a fresh poll on a full zombie worker; the lease duration is validated only as positive. In that case one reservation can lapse per window, at most `MINICLOUD_ASSIGNMENT_MAX_LAPSES` times, after which the job goes to the lapse fallback arm and any polling worker with capacity claims it. This is **the same exposure as today's serial worker** (one zombie attempt, `TotalSlots` ignored), it is bounded, and it never breaks INV-1 (a claim needs the claimant's own poll).

**Conclusion (correctness): the coordinator needs no change to stay correct.** No new over-assignment path appears: the claim-time check bounds RUNNING attempts by `total_slots`, and worker-side poll gating ensures a full worker does not ask. This is **confirmed by code reading, not executed**.

**Finding for the orchestrator (effectiveness, not correctness).** `domain.EffectiveWorkerSlots = 1` (`internal/domain/assignment.go`) is documented as "removed when worker concurrency lands (Q5)", and ADR-0017 Section 2.4 and Section 7 ("Worker concurrency (Q5) ... removes `effectiveWorkerSlots`") assign that removal to this slice. V2-0 excludes coordinator, storage and domain code, so the constant stays. Consequence under the default policy `round_robin` (since P4-17): a worker with one active attempt is never reserved another job, and a busy-but-not-full worker that polls receives work only from the fallback arms (lapse limit reached, or the age arm while the pass is failing). In normal operation each worker therefore runs about **one round-robin job at a time**, and the extra slots are used only under `fifo_pull` or by fallback claims. ADR-0017 premise C12 ("a worker that is executing does not poll") also stops being true; the only rule that relied on it, poll freshness for eligibility (Section 2.2 of ADR-0017), stays safe because the one-slot rule already excludes busy workers. The poll rate of a busy-but-not-full worker equals today's idle poll rate (`IdlePollInterval`, 200 ms), and the `last_poll_at` write throttle already bounds its database cost. This is reported as open question Q1; it is not fixed here.

### 2.3 Slot lifecycle and exactly-once release (item 3)

Mechanism (Stage B):
- `sem := make(chan struct{}, TotalSlots)`; acquire is `select { case sem <- struct{}{}: case <-ctx.Done(): }` at the top of each poll-loop iteration.
- No assignment: the poll loop does `<-sem` itself, exactly once, on that path, then sleeps as today.
- Assignment: ownership of the token moves to the attempt. The poll loop increments `running`, calls `wg.Add(1)`, and starts the attempt goroutine. It never touches that token again.
- The attempt goroutine's first statement is one `defer` that, in this order, recovers a panic (Section 2.4), decrements `running`, does `<-sem`, calls the test-only attempt-done hook when one is set, and calls `wg.Done()`. `ExecuteAndReport` runs after it, unchanged.

**Why exactly once.** The token has a single owner at any instant (poll loop until the hand-off, the attempt goroutine after it). Each owner has exactly one release site on each path: the poll loop's no-assignment branch, and the attempt goroutine's single deferred function. A deferred function runs once when the goroutine's function returns or panics. Every exit is a return or a panic of that function:

| Exit | Path to the deferred release |
|---|---|
| Success | `deliverResult` returns after a 200, `ExecuteAndReport` returns |
| Task error, timeout | `registry.Execute` returns an error; delivery as today; returns |
| Panic in the task or in `ExecuteAndReport` | `ExecuteAndReport`'s own defers stop renewal and cancel the task while the panic unwinds; the goroutine's defer recovers, logs and releases |
| Lost lease (renew 403/409/recognised 404) | `renewLoop` marks lost and cancels the task; `deliverResult` drops the result; returns |
| Cancellation requested | `renewLoop` cancels the task; the task returns `context.Canceled`; delivery as today; returns |
| Terminal rejection, delivery budget spent | `deliverResult` returns |
| Shutdown | the task context and delivery context derive from the cancelled `ctx`; `ExecuteAndReport` returns promptly for every task that honours its context (all three registered tasks do: `internal/executor/sleep.go`, `internal/executor/sha256_loop.go`; `echo` does not block) |

The `running` counter changes only while the token is held (increment after acquire, decrement before `<-sem`), so `0 <= running <= TotalSlots` holds at every instant and `available_slots` is never negative and never above `TotalSlots`.

**Panic policy (a behaviour change, stated plainly).** Today a panic inside `ExecuteAndReport` is not recovered: it runs on the `Run` goroutine and crashes the worker process. With concurrency that would kill every other running attempt. The attempt goroutine will recover the panic, log at ERROR with `job_id`, `attempt_id` and the panic's type only (owner answer Q3: never the panic value, which could carry payload bytes, the lease token or the payload), report **no** result, and release the slot. Renewal has already stopped (the defers of `ExecuteAndReport` run during unwinding), so the lease expires and the coordinator recovers the attempt through the existing LOST path and retries it under ADR-0015 (at-least-once, invariant 5). Owner answer Q3: report nothing; revisit when new executors arrive (V3-1, V4).

**Agent goroutine panics (fix round 1, audit finding A-2).** Three goroutines run agent code, not task code, outside the attempt goroutine's recover: the renewal goroutine of each attempt, the delivery-budget goroutine of each `deliverResult`, and the heartbeat goroutine. In V1 a panic in any of them crashed the process and ended the one running attempt; with the pool it would end up to `TotalSlots` attempts. No reachable panic is known in them (audit, code reading); each now recovers in the fail-safe direction:
- Renewal goroutine: the lease is treated as lost. The goroutine marks the attempt lost and cancels its task, so `deliverResult` drops the result and the slot is freed by the attempt goroutine's normal deferred release. The lease expires and the job is retried as for any lost attempt.
- Delivery-budget goroutine: delivery is treated as expired at once (the budget can no longer be enforced). The delivery context is cancelled, `deliverResult` ends on its "gave up delivering result after the delivery budget" path, and the slot is freed as usual.
- Heartbeat goroutine: the panic is logged and the loop goes on with the next tick (one heartbeat is missed, no attempt is touched).

Each logs one ERROR line, "agent goroutine panicked; recovered", with only the goroutine name (`renewal`, `delivery_budget`, `heartbeat`), the job and attempt IDs for the two per-attempt goroutines, and the panic's type; never the panic value, the lease token or the payload. Tests (executed, faults injected through the agent `Clock` seam and, for the heartbeat, through the log handler): `TestConcurrency_RenewalPanicTreatsLeaseAsLost`, `TestConcurrency_DeliveryBudgetPanicExpiresDelivery`, `TestConcurrency_HeartbeatPanicIsRecovered`; each shows a concurrent attempt surviving and the slot count staying exact.

### 2.4 Isolation between concurrent attempts (item 4)

Per attempt (created inside `ExecuteAndReport` or its callees, never shared): the `attemptRun` (`lost` channel and `sync.Once`), `taskCtx` and `cancelTask`, `renewCtx` and `stopRenew`, the renewal `WaitGroup`, the renewal interval (from that assignment's `LeaseDurationSeconds`), the execution timeout (`ClampExecutionTimeout` of that assignment), the executor's own `context.WithTimeout`, the output and error clamps, the delivery budget timer, `dctx`, the `expired` flag and the fallback-report state in `deliverResult`. Lease token, job ID and attempt ID are read from that attempt's `*coordinator.WorkAssignment`, which the poll loop decodes fresh for every poll and never reuses. Renewal, cancellation, output limits, timeouts and result delivery therefore stay per attempt with no change to `ExecuteAndReport`.

Shared state reachable from concurrent attempts, and why each is safe:

| Shared item | Access | Why safe |
|---|---|---|
| `Agent.cfg` (`*config.WorkerConfig`) | read only after `NewAgent` | never written after construction |
| `Agent.registry` (`*executor.Registry`) | `Execute` from many goroutines | `Get` takes the registry's `sync.RWMutex` read lock; the three tasks are empty structs with no fields (`EchoTask`, `SleepTask`, `SHA256LoopTask`) |
| `Agent.httpClient` (`*http.Client`) | concurrent `Do` | `http.Client` and its default transport are safe for concurrent use. Note: the default transport keeps 2 idle connections per host, so with more than 2 concurrent attempts extra connections are closed after use and re-dialled; a cost, not a correctness issue (observation O2) |
| `Agent.clock` | read by `pollSleep`, `renewLoop`, `deliverResult` without a lock | written only by `SetClock`, whose contract is "tests only; call before running work"; under that contract every read happens after the write. The race detector checks this in the new tests |
| `Agent.mu` | held by `SetClock` and by `register` for the whole registration request | `register` can be entered from the heartbeat goroutine and from the poll loop (re-register after 404); the mutex serialises them. Attempt goroutines never take it |
| `running` counter, semaphore, attempt `WaitGroup` (new) | atomic / channel / `WaitGroup` | concurrency-safe by type; the invariant is in Section 2.3 |
| `slog` default logger | concurrent writes | `slog` handlers are safe for concurrent use |
| `math/rand` top-level functions in `ComputeBackoffWithJitter` | concurrent calls | the top-level source is safe for concurrent use |
| `consecutiveFailures` in `Run` | poll-loop goroutine only | not shared |

No other package-level mutable variable exists in `internal/worker/client.go` (`recognizedNotFound` is a map that is only read). Stage B adds no shared mutable state beyond the three items in the "new" row.

### 2.5 Shutdown (item 5)

1. `ctx` cancellation (SIGINT/SIGTERM in `cmd/worker/main.go`, unchanged) stops polling: the acquire `select` and the in-flight `pollWork` request both observe `ctx`. `pollSleep` (unchanged V1 code) does not: a poll loop that is sleeping after an empty or failed poll finishes that sleep first, at most `MaxPollBackoff` (3 s). A poll in flight at cancellation fails with `context canceled` and is logged as a `work poll error` WARN followed by one backoff sleep (audit finding A-4, cosmetic, pre-existing).
2. Running attempts are cancelled through the existing path: `taskCtx`, `renewCtx` and `dctx` all derive from `ctx`. Today's behaviour for an interrupted attempt is kept: `deliverResult` stops with "result delivery interrupted by shutdown"; the lease expires and the coordinator recovers the attempt (ADR-0014). `ExecuteAndReport` is not changed.
3. `Run` waits for the attempt `WaitGroup`, bounded by **`ResultDeliveryBudget` (60 s, an existing constant in `internal/worker/client.go`)**. No new environment variable. Reason: it is already the longest the worker will spend handing back one outcome, and every attempt that honours its context finishes in far less (cancellation of the three registered tasks is immediate or every 1,024 hash iterations, and delivery under a cancelled context returns on its first iteration). The wait uses the agent `Clock`, so a test drives it with the fake clock. With nothing running at shutdown no timer is created (Stage B detail: the poll-sleep timing test records every agent timer, and an idle worker must not add one).
4. The heartbeat goroutine is also added to a `WaitGroup` that `Run` waits for (unbounded, because it exits on `ctx.Done()` and its request uses `ctx`). Today `Run` returns without waiting for it. `TestConcurrency_RunWaitsForHeartbeatGoroutine` (executed, fix round 1) fails when the wait is removed.
5. If the bound expires, `Run` logs at ERROR the number of attempts still running and returns `nil`; the process then exits from `main`. Those goroutines belong to a task that ignores cancellation, which no registered task does; a goroutine leak is therefore possible only for a future task that breaks the executor contract, and the process exit ends it.

Shutdown therefore takes at most the remaining poll sleep (up to `MaxPollBackoff`, 3 s) plus the 60 s drain bound. Today `Run` returns only after an inline `ExecuteAndReport` returns, so a task that ignored cancellation would hold shutdown indefinitely; the bound is a strict improvement. Section 4 lists every difference visible with `MINICLOUD_WORKER_SLOTS=1`.

### 2.6 Upper bound on `MINICLOUD_WORKER_SLOTS` (item 6, proposal for the orchestrator)

**Facts.** The worker validates only `> 0`. The coordinator already rejects `total_slots` outside `[1, 1024]` at registration (`coordinator.MaxWorkerSlots`, func `RegisterWorker`, SEC-015, database CHECK `chk_workers_total_slots_range` in migration 000003, test `TestAPI_RegisterWorker_SlotsUpperLimit`). So a worker started with `MINICLOUD_WORKER_SLOTS=100000` today starts, fails registration with 400, and `Run` returns "failed to register worker" (exit status 1). It never reaches a state where 100000 goroutines could start; the semaphore is a buffered channel of that size (about 0 bytes per element, a few hundred KiB at most) and goroutines are created per assigned attempt, which the coordinator caps at `total_slots`.

**Proposal: yes, validate `1 <= MINICLOUD_WORKER_SLOTS <= 1024` at worker startup** in `internal/config/config.go` func `LoadWorkerConfig`, with a literal 1024 and a comment naming `coordinator.MaxWorkerSlots` (`internal/config` keeps its own copy of coordinator limits, ADR-0017 implementation note 22), plus a cross-test that the two values are equal.

- *Is it a behaviour change?* Only in where and how a bad value fails: at startup with a message naming the variable, instead of at registration with an HTTP 400. No value that produces a working worker today is rejected, because every value above 1024 already fails registration against every V1 coordinator.
- *Resource bound at 1024* (inferred, not measured): per attempt about three goroutines (attempt, renewal, delivery budget watcher) and up to about 450 KiB of buffers (payload held as `json.RawMessage`, which can be up to 6x the 64 KiB limit after HTML escaping, plus 64 KiB output and 10 KiB error), so about 3,000 goroutines and up to about 450 MiB at the extreme; renewals at about 100 requests per second. Bounded, but large for one host. A lower operational cap is open question Q2.

Approved (owner answer Q2) and applied in Stage B: `config.MaxWorkerSlots = 1024`, with `TestLoadWorkerConfig_SlotsUpperBound` (executed) pinning it equal to `coordinator.MaxWorkerSlots`.

### 2.7 Protocol compatibility (item 7)

- No JSON field name, type or meaning changes on the wire; no `/v1` route changes; no migration; no API change. `available_slots` keeps its type and field name; only its value becomes truthful.
- *New worker, V1 coordinator:* the coordinator ignores `available_slots` and enforces `total_slots` at claim time (Section 2.2), so a concurrent worker gets at most `total_slots` RUNNING attempts. Renewal and result endpoints are keyed by job ID, attempt ID and lease token per request, so concurrent attempts of one worker are independent on the coordinator. Under `round_robin` the effectiveness limit of Section 2.2 applies.
- *V1 worker, new coordinator:* V2-0 does not change the coordinator, so this is the V1 pair.
- `docs/protocol.md` still says coordinator and worker deploy together as a synchronized release and mixed versions are not a supported long-term mode; this ADR does not change that, it only records that the mixed pair is safe.

### 2.8 Visible behaviour change for V1 users
With the default `MINICLOUD_WORKER_SLOTS=4` a worker now executes **up to 4 attempts in parallel** instead of one. Under `fifo_pull` that is what happens when work is queued. Under the default `round_robin` the coordinator still reserves one job per idle worker (Section 2.2), so parallelism above one per worker comes only from fallback claims until Q1 is resolved. Stage B must state both facts in `CHANGELOG.md` [Unreleased] and in the README configuration table; writing "4 parallel attempts" without the round-robin qualifier would be inaccurate.

## 3. Test plan (item 8)

All tests below are in `internal/worker/concurrency_test.go` (executed in Stage B, including `-race -count=20`); the config bound test lives there too because it compares `internal/config` with `internal/coordinator`. Deterministic means: no sleep decides a verdict; a wait ends on a positive event (a channel signal, a request reaching the fake coordinator, a fake-clock timer being created); a deadline exists only to turn a hang into a failure. The instrumented executor is a test task registered in the `executor.Registry` that increments an atomic `current`, raises an atomic high-water mark by compare-and-swap, signals `started` on a channel, blocks until the test releases it (or until its context ends, by test choice: honour or ignore), and decrements `current`. The fake coordinator is an `httptest.Server` that counts polls, hands out a scripted list of assignments with distinct job IDs, attempt IDs and lease tokens, records every heartbeat and register body, and records a violation if a poll arrives while it counts `TotalSlots` assignments without a result.

| Planned test | What it proves | How it stays deterministic |
|---|---|---|
| `TestConcurrency_MaxInFlightNeverExceedsSlots` (executed) | With `TotalSlots` = 3 and 9 queued assignments, the high-water mark equals 3 exactly: never above (the bound) and reached (vacuity guard: concurrency really happens) | Waits for 3 `started` signals, checks, releases one task at a time and waits for the next `started`; no timing |
| `TestConcurrency_HeartbeatTracksRunningAttempts` (executed) | `available_slots` in the heartbeat body equals `TotalSlots - running` at 0, 1, 2 and 3 running and back down; also never negative and never above `TotalSlots` | Calls the heartbeat directly through an `export_test.go` helper at each step; the fake coordinator hands out one assignment at a time on the way up, and an attempt-done hook marks each release on the way down after the matching `started` or done signal, so no ticker timing is involved |
| `TestConcurrency_FullWorkerDoesNotPoll` (executed) | With all slots busy the poll loop blocks on the semaphore and sends no poll | A test hook (unexported field set through `export_test.go`) signals when the loop blocks on acquire; the test waits on a `select` between that signal (pass) and the fake coordinator's "poll while full" signal (fail). Both are events, not timeouts |
| `TestConcurrency_SlotReleasedOnEveryExit` (executed), subtests success, task error, panic, lost lease, cancel requested, terminal rejection | Each exit releases the slot exactly once: with `TotalSlots` = 1, a second poll arrives after the attempt ends, and `available_slots` returns to 1 and never exceeds it | The second poll reaching the fake coordinator is the event; the panic subtest uses a task that panics; lost lease answers the renewal with 409 (fake clock fires the renewal timer); cancel answers `cancel_requested: true` |
| `TestConcurrency_ShutdownDrainsWithoutLeaks` (executed) | Cancelling `ctx` with 3 running attempts stops polling, cancels them, `Run` returns, and the goroutine count returns to the baseline taken before the agent started | Tasks honour cancellation; after `Run` returns, the test closes the server and idle client connections, then waits for `runtime.NumGoroutine` to reach the baseline (deadline only for failure) |
| `TestConcurrency_ShutdownDrainIsBounded` (executed) | A task that ignores cancellation does not hold `Run` past the drain bound; `Run` returns after the bound and logs one remaining attempt | The bound waits on the agent `Clock`; the fake clock fires it; the task is released afterwards so the test itself leaks nothing |
| `TestConcurrency_ResultsNeverCrossBetweenAttempts` (executed) | Two concurrent attempts with different lease tokens: every renew and result request carries the token, job ID and attempt ID of its own assignment, and each output matches its own payload | Tasks are released in reverse order of start; the fake coordinator checks every request against its scripted assignment table |
| `TestConcurrency_LostLeaseIsolatedToOneAttempt` (executed) | A 409 on attempt A's renewal cancels A and suppresses A's result only; B keeps renewing and delivers | Fake clock drives renewals; events are the requests seen by the fake coordinator |
| `TestConcurrency_ReRegisterReportsLiveSlots` (executed) | A re-registration after a heartbeat 404 sends `available_slots = TotalSlots - running`, not `TotalSlots` | Fake coordinator answers one heartbeat 404 while 2 attempts are blocked; the heartbeat is invoked directly |
| `TestConcurrency_SingleSlotIsSerial` (executed) | Rollback (Section 4): with `TotalSlots` = 1 the request sequence is exactly poll, (renewals), result, poll, ... with no poll between an assignment and its result, and the high-water mark is 1 | The fake coordinator records the ordered request log for 3 assignments; the check is on the log |
| `TestConcurrency_RunWaitsForHeartbeatGoroutine` (executed, fix round 1) | After cancel, `Run` does not return while the heartbeat goroutine is busy (Section 2.5 item 4) | The heartbeat goroutine is held inside its log call; the test waits for the poll loop's shutdown log, then checks `Run` has not returned within a 300 ms window (a slow machine can only make a broken build pass, never fail a correct one) |
| `TestConcurrency_RenewalPanicTreatsLeaseAsLost` (executed, fix round 1) | A renewal-goroutine panic cancels that attempt, drops its result and frees its slot; the concurrent attempt renews and delivers | Fake clock whose `NewTimer` panics once for the panicking attempt's renewal interval |
| `TestConcurrency_DeliveryBudgetPanicExpiresDelivery` (executed, fix round 1) | A delivery-budget-goroutine panic expires that delivery at once and frees its slot; the concurrent attempt delivers | Fake clock whose budget timer panics once in `C`; the fake clock is never advanced, so only the expiry can free the slot |
| `TestConcurrency_HeartbeatPanicIsRecovered` (executed, fix round 1) | A heartbeat-goroutine panic is logged, the loop sends further heartbeats reporting 0 free slots, both running attempts deliver | Log handler panics once inside the heartbeat's re-registration log line |
| `TestLoadWorkerConfig_SlotsUpperBound` (executed) | 1024 accepted, 1025 and 100000 rejected with a message naming the variable, and the literal equals `coordinator.MaxWorkerSlots` | Table test, no I/O |

Race detector: every test above runs under `-race`; Stage B runs the new tests with `-race -count=20` and reports the real output. Mutation proof (Stage B, three at least, each must fail a test above): remove the semaphore (fails the high-water and full-worker tests), release the slot twice (fails the slot-release and high-water tests, or hangs the drain test), report `TotalSlots` instead of the live number (fails the heartbeat and re-register tests). Existing worker tests run unchanged; they all use `TotalSlots: 1` or call `ExecuteAndReport` directly.

**Stage B result.** No existing test under `tests/` needed a change: the unset suite, `make test-race`, `make test-db` and `make test-security` passed with their baseline counts plus the new tests. **Risk as written in Stage A (inferred, not executed).** Several existing integration and security tests start the real worker binary with `MINICLOUD_WORKER_SLOTS` above 1 (`tests/integration/local_core_test.go`, `tests/integration/postgres_wiring_test.go`, `tests/integration/multi_worker_batch_test.go`, `tests/internal/sectest/proc.go` and others). They will now execute in parallel. If one of them encoded the serial assumption it will fail; files under `tests/` are outside the Stage B scope, so the builder stops and reports instead of editing them (open question Q4).

## 4. Rollback (item 9)
`MINICLOUD_WORKER_SLOTS=1` reproduces today's execution order exactly: the single token is held from the poll until the attempt finishes, so the loop polls only when nothing executes, as in `Run` today. The differences, all intended: (1) the heartbeat reports `available_slots = 0` while the attempt runs (today 1; the coordinator ignores it); (2) shutdown waits at most `ResultDeliveryBudget` for a task that ignores cancellation (today unbounded); (3) a task panic is recovered instead of crashing the process (Section 2.3); (4) a panic in the renewal, delivery-budget or heartbeat goroutine is recovered in the fail-safe direction instead of crashing the process (Section 2.3, fix round 1); (5) `Run` waits for the heartbeat goroutine before returning (Section 2.5 item 4). `TestConcurrency_SingleSlotIsSerial` (executed) proves the order. Code rollback is a revert of the Stage B commits; no data or schema is involved.

## 5. Consequences
- Advertised capacity equals executed capacity; V2-1 and V2-3 can read `available_slots` with a defined meaning (though the coordinator itself should keep deriving capacity from attempts, which is drift-free).
- Under `fifo_pull`, throughput per worker rises up to `TotalSlots`. Under `round_robin`, it does not rise much until `EffectiveWorkerSlots` is replaced (Q1).
- A task panic no longer kills the worker process; it costs one lease duration before the retry. A panic in an agent goroutine (renewal, delivery budget, heartbeat) no longer kills it either (Section 2.3).
- Shutdown is bounded.
- More concurrent HTTP traffic per worker (renewals scale with running attempts, about one request per renewal interval per attempt, ADR-0014 overhead estimate).

## 6. Open questions for the owner (item 10) and the answers (orchestrator, 2026-11-16)
- **Q1.** ADR-0017 Section 2.4 and Section 7 assign the removal of `domain.EffectiveWorkerSlots` (rule becomes `total_slots - running - reserved`) to the worker-concurrency slice, but V2-0 excludes coordinator, storage and domain code. Should a follow-up slice (for example "V2-0b") do it before V2-1/V2-3 and before any benchmark, and should the README/CHANGELOG wording for V2-0 say that round-robin parallelism is still one per worker until then?
- **Q1 answer:** yes. Follow-up slice V2-0b (remove `EffectiveWorkerSlots`, amend ADR-0017, coordinator/storage/tests, Opus audit, before V2-1). README and CHANGELOG state that under `round_robin` parallelism stays one job per worker until V2-0b and that `fifo_pull` gets up to `TotalSlots`.
- **Q2.** Is the worker-side upper bound 1024 (equal to the coordinator limit, Section 2.6), or does the owner want a lower operational cap?
- **Q2 answer:** keep 1024, with the cross-test against `coordinator.MaxWorkerSlots`.
- **Q3.** On a recovered task panic, report nothing and let the lease expire (proposed), or send one PERMANENT failure report so the job fails at once?
- **Q3 answer:** report nothing after a recovered panic; never log or send the lease token or payload; revisit when new executors arrive (V3-1, V4).
- **Q4.** If an existing test under `tests/` turns out to assume a serial worker, may Stage B edit it (without weakening it), or must that become a separate task?
- **Q4 answer:** only if it truly assumes a serial worker, with the evidence first and no assertion weakened, deleted or skipped. Outcome: no such test was found; nothing under `tests/` changed.

## 7. Observations recorded, not acted on
- **O1** (inferred, not tested): `coordinator.WorkAssignment.Payload` is a `json.RawMessage`, which keeps the escaped bytes as sent; `internal/api` encodes with HTML escaping, so a legal payload with many `<`, `>` or `&` characters may arrive longer than 64 KiB, and `executor.Registry.Execute` rejects `len(payload) > MaxPayloadSizeBytes` before decoding. If true, such a job would fail on the worker although the coordinator accepted it. Not part of V2-0; recorded as a V1.0.x candidate in `docs/agents/TASKS.md`.
- **O2** (confirmed by code reading): `NewAgent` uses the default HTTP transport (2 idle connections per host). Tuning it is not part of V2-0.
