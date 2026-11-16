Task: V2-0 Stage A (ADR-0019, docs only)
Branch: agent/claude/V2-0
Work tip (last commit before this report): 6c91a00
Date: 2026-11-16

## Final message

V2-0 Stage A is done: ADR-0019 is written with status Proposed, and I stopped there (6c91a00 on agent/claude/V2-0). No code changed. `make test-docs` exits 0; no other gates were run.

Decisions:
1. **Slots:** a slot is one attempt running at a time. `available_slots` = `TotalSlots` − running attempts, read live in register, re-register and heartbeat. Poll gets no new field.
2. **Poll gating:** the worker takes a slot before every poll. A full worker does not poll.
3. **Slot release:** one deferred release in each attempt goroutine. It also recovers panics. A recovered panic reports no result, and the lease expires.
4. **Isolation:** every piece of shared state is listed with why it is safe.
5. **Shutdown:** waits at most `ResultDeliveryBudget` (60 s, an existing constant).
6. **Upper bound (proposal):** 1024, matching the coordinator's existing limit (SEC-015). No value that works today would be rejected.
7. **Rollback:** `MINICLOUD_WORKER_SLOTS=1` keeps today's order. Only the heartbeat number and the shutdown bound differ.

Coordinator finding (item 2), confirmed by reading the code, not by running it:
- **Correctness: no coordinator change is needed.** The coordinator ignores the `available_slots` a worker reports. It works out capacity from PENDING and RUNNING attempts, and checks it again under a row lock when a job is claimed.
- **Reservations:** at most one per worker, and only when the worker has zero active attempts. A reservation can expire unaccepted only in a rare case that today's serial worker already has (every running attempt has lost its lease and the lease is shorter than the accept window), and it is bounded.
- **But the extra slots mostly go unused under the default `round_robin` policy.** `EffectiveWorkerSlots = 1` (ADR-0017 §2.4) means a busy worker is reserved nothing more. The only extra work it gets is from fallback claims. ADR-0017 gives the removal of this constant to the worker-concurrency slice, but V2-0 excludes coordinator code. So "4 parallel attempts" is true only for `fifo_pull`.

Questions:
- **Q1:** Should a follow-up slice remove `EffectiveWorkerSlots`, and should the README and CHANGELOG say this caveat?
- **Q2:** Is 1024 the right upper bound, or do you want a lower one?
- **Q3:** After a recovered panic, report nothing, or send one PERMANENT failure report?
- **Q4:** If a test under `tests/` assumes a serial worker, may Stage B edit it?

## Detail

### What changed
- `docs/adr/0019-worker-concurrency-semantics.md` (new, Proposed): all ten items of the Stage A spec, plus observations O1 and O2.
- `docs/agents/TASKS.md`: PV-01 status set to "complete, merged in PR #55" (table row and detail line). V2-0 row set to branch `agent/claude/V2-0`, status "ADR proposed". The detail line points to the ADR.

### Commands run (executed)
- `git fetch origin`; `git checkout -b agent/claude/V2-0 origin/main` (origin/main = 6c2ef78).
- `make test-docs` → `ok  github.com/BlaZe08-Dev/Minicloud/tests/docs 1.563s` (rc=0), then again after the commits with `go test -count=1 ./tests/docs` → `ok  github.com/BlaZe08-Dev/Minicloud/tests/docs 1.536s`.
- `git push -u origin agent/claude/V2-0` → new branch created.
- Not run (docs-only stage): gofmt, vet, build, the race suite, test-db, test-security. No Go file changed. The database preflight was not run because Stage A uses no database.

### Evidence for the coordinator finding (confirmed by code reading)
- The reported value is discarded: `RecordHeartbeat(ctx, workerID string, _ int)` in both `internal/storage/postgres/repository.go` and `internal/storage/inmemory.go`. `docs/protocol.md` and SEC-055 say the same.
- Capacity is derived: the `worker_capacity` view (migration 000002) and `computeAvailableSlotsLocked`. They are checked at claim time in `DispatchNextJob` (fifo_pull) and in `ClaimForPoll`/`claimTx` (round_robin), and checked again under the worker row lock.
- Reservation eligibility: `assignChooseWorker` and its re-check use `domain.EffectiveWorkerSlots` (= 1), "no live reservation" and fresh poll. Its doc comment says "removed when worker concurrency lands (Q5)".
- Fallback arms in `claimTx`: a lapse count of at least MaxLapses, or the age arm (only when this poll's own refresh pass failed). This is why busy-but-not-full workers get little round-robin work.
- The default policy is `round_robin` (P4-17).

### Risks and things to double-check
- **Panic recovery is a behaviour change.** Today a task panic crashes the worker process. I propose recovering it, but it is not decided (Q3).
- **Integration tests start the worker binary with 2 or 10 slots, and will now run attempts in parallel.** Any serial assumption in them would surface in Stage B. `tests/` is outside the Stage B scope (Q4).
- **O1, inferred and not tested, may be an existing bug.** The worker keeps the payload as a `json.RawMessage` with its HTML escapes, so a legal payload can be longer than 64 KiB and `Registry.Execute` would reject it. It needs its own check. I did not fix it.
- The 1024 resource estimate (about 3,000 goroutines, up to about 450 MiB) is inferred, not measured.
