# ADR 0020: Round-Robin Uses the Worker's Real Slots

## Status
Accepted (2026-11-19, end of task V2-0b Stage B, branch `agent/claude/V2-0b`, awaiting audit and merge). Proposed in Stage A; approved by the orchestrator ("ADR approved") with the decisions below; implemented in Stage B. Evidence: `docs/agents/handoffs/V2-0b.md` and `docs/agents/handoffs/V2-0b-evidence/`.

**Decided (orchestrator, 2026-11-19; the owner can overrule):**
- Q1: option A. Optional register field `executes_concurrently`, migration `000006`; V2-1 moves to `000007`.
- Q2: a boolean named `executes_concurrently`; no integer count.
- Q3: `domain.MaxReservationsPerWorker = 8`, a fixed constant in `internal/domain`; no setting.
- Q4: no coordinator-wide switch; operators use `MINICLOUD_WORKER_SLOTS=1` on workers; `internal/config` is not touched.
- Q5: update SEC-015 (`total_slots`) and correct the ADR-0017 Section 6.2 citation that said SEC-016.
- Q6: the new abuse finding is registered as SEC-068 (Low, within SEC-004).
- Q7: no immediate revoke; a reservation of a re-registered worker lapses normally (FS-28).
- Q8: no new slice; one candidate line on the task board.

**Deviations in Stage B (each small, recorded here):** `TestReservationCap_FitsAcceptWindow` lives in `internal/worker/real_slots_test.go`, beside the existing timing test's package, not in `internal/coordinator` (the existing `TestPollTiming_AcceptWindowExceedsBackoff` is in `internal/worker`). `TestMigrator_Migration000005_FreshSchema` pinned 000005 as the last migration; it now applies the first five migrations only and the last-migration pin moved to `TestMigrator_Migration000006_FreshSchema`. A fallback claim refused by Section 2.4 returns `ErrWorkerNoCapacity` in both repositories, as planned. The PostgreSQL slot predicate is written out twice (snapshot and re-check), because the repository's SQL-safety test forbids building SQL by string concatenation.

Amends [ADR 0017](0017-assignment-round-robin.md) Section 2.4 (one effective slot), invariant INV-10 and question Q5. Builds on [ADR 0019](0019-worker-concurrency-semantics.md) (worker concurrency, slice V2-0), Section 2.2 and its question Q1. Version gate: V2 (ADR-0004, ADR-0018 roadmap), before V2-1.

### Evidence labels
Labels follow `AGENTS.md` section 2. Statements about today's code were read on `main` at 26b440a and are **confirmed by code reading** unless labelled otherwise. Measurements quoted from the V2-0 audit are **executed** by that audit, not by this task ([docs/agents/reviews/V2-0.md](../agents/reviews/V2-0.md)). Sections 1 and 2 were written in Stage A as design; the tests they name were **executed** in Stage B (real output in `docs/agents/handoffs/V2-0b.md`). Every test named below exists on the branch.

## 1. Context

### 1.1 The defect
ADR-0019 made the worker run up to `MINICLOUD_WORKER_SLOTS` attempts at once and report `available_slots` live. Under the default `round_robin` policy the assignment pass still treats every worker as having one slot:

| # | Fact | Where |
|---|---|---|
| F1 | `domain.EffectiveWorkerSlots = 1`, documented as "removed when worker concurrency lands (Q5)". | `internal/domain/assignment.go` |
| F2 | In-memory pass: a worker is skipped when `activeAttemptsLocked(id) >= domain.EffectiveWorkerSlots`, or when `liveReservationLocked` finds any live reservation. | `internal/storage/inmemory_assignment.go:68`, func `eligibleWorkersLocked` |
| F3 | PostgreSQL pass: the snapshot in func `assignChooseWorker` and the re-check under the worker row lock in func `assignLockAndRecheck` both require `count(PENDING, RUNNING) < $4` with `$4 = domain.EffectiveWorkerSlots`, `total_slots - active > 0`, and `NOT EXISTS` a live reservation. | `internal/storage/postgres/assignment.go:396` and `:485` |
| F4 | Both claim paths (func `ClaimForPoll` and func `claimTx` in PostgreSQL, func `ClaimForPoll` in memory) check only `total_slots - active > 0`. They do not count reservations. Own arm first, then the lapse and age fallback arms. | same files |
| F5 | After a poll that returns an assignment, the worker loop starts the attempt and polls again at once if a slot is free. It sleeps `IdlePollInterval` (200 ms) after a 204 or an unhealthy poll, and a jittered backoff of at most `MaxPollBackoff` (3 s) after an error. A worker with no free slot does not poll. | `internal/worker/client.go` func `Run`, func `acquireSlot` |
| F6 | `PollWork` holds the service mutex `s.mu` for the whole claim; the assignment pass runs under the same mutex (ADR-0017 Section 4.1). | `internal/coordinator/service.go:417` |
| F7 | The worker registers with a hard-coded `"version": "v0.1.0"` in both v1.0.0 and `main`. | `internal/worker/client.go:352`; `git show v1.0.0:internal/worker/client.go` line 227 |
| F8 | The register body is decoded straight into `domain.Worker` (unknown fields are ignored), and `UpsertWorker` stores `version`, `total_slots`, `status` and `supported_tasks` only. | `internal/api/handlers.go` func `HandleRegisterWorker`; `internal/storage/postgres/repository.go` func `UpsertWorker` |
| F9 | `total_slots` is bounded to `[1, 1024]` (SEC-015; `coordinator.MaxWorkerSlots`; CHECK `chk_workers_total_slots_range`; test `TestAPI_RegisterWorker_SlotsUpperLimit`). | `internal/coordinator/service.go` func `RegisterWorker` |

### 1.2 Measured effect (V2-0 audit, executed by the audit)
Eight `sleep` jobs of 5 s on one worker, real binaries ([V2-0 review](../agents/reviews/V2-0.md), observation O-1, script `docs/agents/reviews/V2-0-evidence/run-binary.sh`):

| Policy | Slots | Max concurrent | Wall time |
|---|---|---|---|
| `fifo_pull` | 4 | 4 | 10.30 s |
| `round_robin` | 4 | 1 | 46.60 s |
| `fifo_pull` | 1 | 1 | 40.41 s |
| `round_robin` | 1 | 1 | 46.63 s |

The 6 s gap between the two one-slot rows is the round-robin overhead the task board asks this slice to measure again. *Inferred:* after each job the worker's first poll returns 204, because the next reservation is made only by the next pass (timer 1 s), so each job waits about half an interval plus one idle poll sleep.

## 2. Decisions

### 2.1 Capacity rule (item 1)

Notation, for a worker `w` at the moment of one decision, all on database time (repository time in memory):
- `A(w)` = attempts of `w` in `PENDING` or `RUNNING` (the `worker_capacity` predicate);
- `R(w)` = jobs with `assigned_worker_id = w` and `assignment_expires_at > now` (**live** reservations only);
- `S(w)` = the worker's **assignable slots**: `total_slots` when the worker declared concurrent execution at its latest registration (Section 2.6), otherwise **1**;
- `M` = `domain.MaxReservationsPerWorker` = **8** (Section 2.5).

**Rule.** `w` is eligible for a reservation when the existing checks hold (healthy, fresh heartbeat, fresh poll, supports the type; ADR-0017 Section 2.2) **and**

    A(w) + R(w) < S(w)   and   R(w) < M

The separate `total_slots - A(w) > 0` check stays in the SQL as a defence; it is implied because `S(w) <= total_slots`.

**Accepted starting point, with one change.** The proposed rule (`active + outstanding < declared slots`) is kept. The evidence adds two terms: the reservation cap `M` (Section 2.5 and Section 2.3 show why it is needed for timing, not only for abuse), and `S(w) = 1` for a worker that did not declare concurrency (Section 2.6). An undeclared worker therefore gets exactly today's rule: `A + R < 1` means no attempt and no reservation, which is INV-10 as it stands.

**Why live reservations only.** An expired reservation is already not reserved for the claim predicate (ADR-0017 Section 1.3) and is claimable by the fallback arms. Counting it would hold a slot for a job that the worker can no longer take by its own arm, until the next revoke. So `R` uses the same `assignment_expires_at > now` test as the own arm.

**Within one pass.** Neither repository keeps a separate free-slot counter, and this ADR does not add one, because a second copy of the count can drift from the rows. Each decision recomputes `A` and `R`:
- PostgreSQL: func `assignChooseWorker` runs its eligibility query once per decision inside the pass transaction. The transaction sees its own uncommitted `UPDATE jobs SET assigned_worker_id = ...`, so a reservation made earlier in the pass raises `R(w)` for every later decision. The `NOT EXISTS` becomes `(SELECT COUNT(*) ...)`, served by `idx_jobs_assigned_worker` (migration 000005). The re-check under the worker row lock uses the same predicate text.
- Memory: func `eligibleWorkersLocked` runs per decision under `r.mu`; `liveReservationLocked` (a boolean today) becomes a count of live reservations.

So each reservation "decrements the free count" by raising `R(w)` by one for the rest of the pass, and the rule holds within a pass and across passes. Across passes, claims convert a reservation into an attempt (`R - 1`, `A + 1`, sum unchanged) and finished attempts lower `A`.

**Pure predicate.** The rule is one domain function, so property tests can drive it without storage: `domain.AssignableSlots(executesConcurrently bool, totalSlots int) int` and `domain.CanReserve(assignable, active, reserved int) bool` (names proposed). `domain.EffectiveWorkerSlots` is removed.

### 2.2 Rotation is unchanged (item 2)

Per decision, per task type, one cursor per type, successor of the cursor among the eligible set sorted by ID (ADR-0017 Section 2.3, INV-4 and INV-4b). One turn per decision. No weighting by slots; weighting stays a V2-3 question. The only change is **who is in the eligible set**: a worker with free assignable slots stays in it after it is selected.

**Example (lone idle 4-slot worker).** `w_a` declared 4 concurrent slots, `A = 0`, poll fresh; six `echo` jobs; empty cursor.

| Decision | Eligible set | Cursor before | Selected | `R(w_a)` after |
|---|---|---|---|---|
| 1 | {w_a} | "" | w_a (min of set) | 1 |
| 2 | {w_a} | w_a | w_a (no ID after w_a, wrap) | 2 |
| 3 | {w_a} | w_a | w_a | 3 |
| 4 | {w_a} | w_a | w_a | 4 |
| 5 | {} (`A + R = 4`) | w_a | none: the rest of the batch waits (ADR-0017 Section 2.5) | 4 |

INV-4 (constant set, counts within one) holds with `|S| = 1`. INV-4b and statement S1 (no double selection while another eligible worker waits) hold vacuously, because no other worker is eligible.

**Second example (two workers).** `w_a` 4 concurrent slots, `w_b` undeclared (one slot), both idle and fresh, six jobs: d1 `w_a`, d2 `w_b` (now full), d3 `w_a` (wrap), d4 `w_a`, d5 `w_a`, d6 none. Between the selections of `w_a` at d1 and d3, `w_b` was eligible at d2 and was selected, so S1 holds. From d3 on, `w_b` is not eligible, so S1 does not require it to be selected. The pure function `scheduler.NextAfterCursor` is not changed, so the existing property tests `TestProperty_RoundRobin_CountsWithinOne` and `TestProperty_RoundRobin_NoDoubleSelectionPerType` keep guarding it.

### 2.3 Accept window, lapses and fallback with several reservations (item 3)

**Claiming N reservations in time.** From F5 and F6, a worker that holds `N` live reservations and has at least `N` free local slots claims them back to back: each successful poll is followed at once by the next poll, with no sleep, as long as a slot is free. With the rule of Section 2.1, `N <= S - A`, and in normal operation the database count `A` equals the worker's local `running` count (ADR-0019 Section 2.2), so `N` is at most the free local slots. The time to claim all `N` is `N x (round trip + claim transaction + wait for s.mu)`.

Worst case, inferred from code reading and the measurements in ADR-0017 Section 6.2:
- `N <= M = 8`.
- A claim that finds its own reservation row locked (`FOR UPDATE SKIP LOCKED`, for example by a concurrent cancel) gets 204, and the worker sleeps `IdlePollInterval` (200 ms) before the next poll.
- One poll error in the burst adds at most one `MaxPollBackoff` (3 s).
- So the bound pinned by a test is `MaxPollBackoff + M x IdlePollInterval = 3 s + 8 x 200 ms = 4.6 s < MinAcceptWindow = 5 s`. Nine is the largest `M` for which this holds (`(5 s - 3 s) / 200 ms = 10`, strict); 8 keeps 0.4 s for round trips.
- The wait for `s.mu` is not inside that bound. A poll waits at most one pass duration `D` (ADR-0017 Section 2.1); measured `D` was at most 283 ms at 50 reservations (ADR-0017 Section 6.2, one machine). If every claim of a burst waited a full pass (possible only while repeated passes run back to back under a deep backlog), 8 claims could take about 2.3 s, plus the 3 s backoff case, which exceeds 5 s. A reservation that misses its window is a counted lapse and the job moves on (FS-22 behaviour), so correctness is unaffected; only latency rises. Stage B measures the claim time and a burst under a deep backlog and reports the numbers; if the burst does not fit, `M` is lowered, not the window.

**What must change in the worker:** nothing. The V2-0 loop already polls immediately after launching an attempt.

**A reservation the worker cannot start yet.** A reservation needs `A + R < S` in the database. It can reach a worker whose local slots are all busy only when the database `A` is below the local count:
- *Commit-to-release window* (ADR-0019 Section 2.2): the coordinator closed the attempt; the worker frees the slot a few statements later and its next poll takes the reservation. No lapse.
- *Zombie attempts* (invariant 6): an attempt is `LOST` in the database while the worker still executes it. Then the worker is full locally and does not poll (F5), so its `last_poll_at` stops moving. Reservations can be made to it only while that poll is fresh, that is within one accept window after its last poll, and only up to `S - A` (at most `M`). Each such reservation lapses once (counted) and goes to the next eligible worker. After one window the worker is no longer eligible until it polls again. This is the same class of exposure ADR-0019 Section 2.2 bounded for one reservation; the bound is now `min(S - A, M)` lapses per occurrence instead of 1.

No change is needed to prevent it: the poll-freshness check already stops new reservations to a worker that has stopped polling.

**Lapses.** Lapses stay per job (ADR-0017 Section 3.3). A worker that stops polling while holding `N` reservations causes `N` jobs to lapse once each, not one job to lapse `N` times, so no job reaches `MINICLOUD_ASSIGNMENT_MAX_LAPSES` faster than today. A worker that dies is revoked without a counted lapse once the reaper marks it `UNHEALTHY` (unchanged); a reservation whose window ends before the reaper tick is a counted lapse, as today.

**Revoke backlog.** The revoke step reads at most `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` (50) rows per pass. Today at most one reservation per worker can expire at once; with the cap, up to `8 x workers`. An expired, not yet revoked reservation is not live, so it does not hold a slot (Section 2.1) and is claimable by the fallback arms, but its job is not a candidate for the pass until it is revoked (the candidate read requires `assigned_worker_id IS NULL`). The cap keeps this backlog bounded; with 6 workers or fewer it fits in one pass.

**Fallback age arm.** Unchanged. It never creates a reservation. Its claim is bounded by Section 2.4.

### 2.4 Claim re-check under the row lock (item 4, replaces INV-10)

The claim keeps its lock order (`jobs` then `workers`, ADR-0017 Section 4.3) and re-checks under the worker row lock:
- **Own arm:** `total_slots - A > 0`, unchanged. The reservation is already counted in `A + R`; the claim moves it from `R` to `A`, so the sum does not grow.
- **Fallback arms (lapse and age):** **`A + R < S`** under the worker row lock, the same predicate as the pass without the cap (a fallback claim creates no reservation). Otherwise the claim returns `ErrWorkerNoCapacity`, as the existing capacity check does.

**Why the fallback needs the new check.** Own-first preference means a poll normally reaches the fallback arms only with `R = 0`. Two paths reach them with `R > 0`: the own reservation row is locked by another transaction (`SKIP LOCKED` skips it), or the worker re-registered without the reserved job's task type (the own arm filters by `supported_tasks`). Today's check (`total_slots - A > 0`) would then let `A + R` exceed `S`, and the reservation would later fail its own claim or lapse. With the new check a fallback claim never makes `A + R` exceed `S`, and never makes `A` exceed `total_slots`.

**Behaviour change for undeclared workers (stated plainly).** With `S = 1`, a fallback claim needs `A = 0` and `R = 0`. Today a 4-slot undeclared worker with one `RUNNING` attempt in the database can claim a fallback job. A v1.0.0 worker polls with a `RUNNING` attempt in the database only when its result delivery gave up (the attempt stays `RUNNING` until its lease expires), so the change only removes a second job from a serial worker. `fifo_pull` (`DispatchNextJob`) is not touched.

**INV-10 replacement (INV-10r).** No pass decision and no claim makes `A(w) + R(w)` exceed `S(w)` or `R(w)` exceed `M`. A reservation is made only while `A + R < S` and `R < M` under the worker row lock (PostgreSQL) or `r.mu` (memory). *Caveat:* a re-registration that lowers `total_slots` or drops the declaration can leave `A + R > S` for at most one accept window; no new reservation or fallback claim is made until the sum is below `S` again, and the own arm still bounds `A <= total_slots` (FS-28). INV-10b (the re-check runs under the worker row lock) is unchanged and keeps `TestPostgresRepository_Assign_SkipsWorkerLockedByClaim`.

### 2.5 Abuse and the named bound (item 5)

**The old row is no longer true.** ADR-0017 Section 6.2 says "a worker registers with a large `total_slots` to look attractive" is ignored because of the one effective slot. With this ADR a worker that declares concurrency and 1024 slots stays eligible after every honest worker is full, so it receives every remaining decision.

**What a hostile client can already do today (SEC-004, unauthenticated API):**
- Under `fifo_pull`, register one ID with `total_slots = 1024` and claim up to 1024 jobs by polling; each abandoned attempt is reaped to `LOST` after the lease (30 s) and consumes one attempt of `max_attempts`.
- Under `round_robin`, register many IDs (sybil, ADR-0017 Section 6.2) and hold one reservation each; claim lapse-arm jobs up to `total_slots`; poll as another worker ID and take its reservations.

**What this ADR adds.** One ID can hold many reservations without sybil registrations, and after it claims and abandons an attempt it stays eligible (today `A = 1` makes it ineligible for a whole lease). Under `round_robin` its claim rate becomes like `fifo_pull`'s today, still bounded by `total_slots <= 1024` and `max_attempts`.

A worker that declares 1024 slots and **never polls** gets no reservation at all: eligibility needs a poll within the accept window. A worker that polls **once** and then stops could, without a bound, be reserved up to 1024 jobs for one window (the pass repeats while batches are full and reservations are made), and every one would lapse.

**Bound.** `domain.MaxReservationsPerWorker = 8` live reservations per worker, a constant, not configuration (like the event candidate cap of 10, ADR-0017 INV-18). Reasons:
1. Timing (Section 2.3): `MaxPollBackoff + 8 x IdlePollInterval = 4.6 s < 5 s`, so an honest worker can claim every reservation it holds inside the window even after one error backoff.
2. Lapse cost: a worker that polls once and stops can make at most 8 jobs lapse per window, the same per-ID exposure order as today's sybil case with 8 IDs. Each job is still bounded by `MAX_LAPSES x (window + interval)` before fallback (18 s by default).
3. Revoke backlog (Section 2.3): at most `8 x workers` reservations can expire together.
4. Ramp-up cost for honest large workers is small: a 32-slot worker fills in four rounds of eight claims; a pass that reserves and reads a full batch repeats after `MINICLOUD_ASSIGNMENT_MIN_YIELD` (10 ms).

The bound does **not** stop a hostile worker that polls repeatedly from claiming up to its `total_slots`; that is the pre-existing SEC-004 and SEC-015 bound (1024), closed only by worker authentication (V3-2).

**Tests that prove the bound (executed in Stage B):**
- `TestConformance_Assign_ReservationCapPerWorker` (executed): a worker with 1024 declared slots, one fresh poll, 50 jobs; a pass reserves exactly 8; a second pass reserves 0 while those are live. Runs against both repositories.
- `TestAbuse_Register_LargeDeclaredSlots_ReservationCap` (executed, `tests/abuse/assignment_test.go`): over HTTP, the same registration; the job views show at most 8 jobs assigned to that worker; after the window they lapse and an honest worker gets them.
- `TestReservationRoom_Table` (executed, domain): the predicate returns false at `R = M` for any `A` and `S`.
- Mutant "drop the bound" (Section 2.8) must fail the first two.

**Rows updated in Stage B** (ADR-0017 Section 6.2 note, SEC-015, SEC-068):
- ADR-0017 Section 6.2, row "A worker registers with a large `total_slots` to look attractive": *"Effective since ADR-0020 for a worker that declares concurrency: it stays eligible while `A + R < total_slots`. Reservations are bounded by `MaxReservationsPerWorker` (8); claims by `total_slots <= 1024` (SEC-015) and `max_attempts`. Pre-existing under `fifo_pull`. Closed by worker authentication (V3-2)."* A short amendment note pointing here is added to ADR-0017 in Stage A.
- `docs/security/findings.md` **SEC-015** (the `total_slots` bound): add that under `round_robin` the bound now also limits reservations and claims of one declared-concurrent worker, and cite the tests above (done). The task text names SEC-016, but SEC-016 is the `version` length cap (Section 2.6 option B would make it relevant); ADR-0017 Section 6.2 also cites "SEC-016 CHECK" for `total_slots`, which was a numbering slip (owner decision Q5: SEC-015 updated, the ADR-0017 citation corrected).
- A new finding, registered as SEC-068: "one registration can hold up to 8 reservations and claim up to `total_slots` under `round_robin`", Low, within SEC-004.

### 2.6 Mixed versions (item 6): how old workers keep one slot

Both the v1.0.0 worker and the current `main` (V2-0) worker register with `total_slots` from `MINICLOUD_WORKER_SLOTS` (default 4) and `available_slots = total_slots` at registration time. v1.0.0 runs one attempt at a time. If the coordinator trusted `total_slots`, it would reserve up to 4 jobs to a serial worker. Three of them would lapse every time the worker took one, which costs one window each, pushes jobs toward the lapse fallback, and takes those jobs away from other workers for 5 s.

| Option | How old workers stay at one slot | Evidence for | Evidence against | Verdict |
|---|---|---|---|---|
| **A. Optional register field** `executes_concurrently` (boolean; absent means false). Stored on `workers` by migration `000006`. `S = total_slots` when true, else 1. The new worker sends `true`. | Absent field: the v1.0.0 and `main` workers do not send it (F7, F8), so they keep `S = 1` with no change to them. | Additive to `/v1` (unknown fields are already ignored, F8). Explicit, one meaning, no parsing. Re-registration always rewrites it, so the latest binary under an ID wins. A new worker against an old coordinator: the field is ignored and the old one-slot rule applies, which is today's behaviour. Survives a coordinator restart (stored). | Needs migration `000006`, so V2-1 moves to `000007` and the docs that name `000006` for V2-1 change in Stage B. Unauthenticated like every register field (Section 2.5). | **Recommended.** |
| **B. Version-string rule** (for example `version >= v2.0.0` means concurrent). | The new worker would send a new version. | No migration (`version` is stored). | Both old workers send the same string `"v0.1.0"` (F7), so the rule only works if every future worker keeps a parseable, maintained version; the string is free-form text up to 64 characters (SEC-016) and today hard-coded. Overloads a descriptive field with a scheduling meaning. A third-party worker that copies a version string gets the behaviour by accident. | Rejected. |
| **C. Accept the degradation** (trust `total_slots` for everyone). | Operators set `MINICLOUD_WORKER_SLOTS=1` on v1.0.0 workers. | No protocol or schema change. The V2-0 worker benefits at once. | A v1.0.0 worker at the default 4 slots gets up to 4 reservations whenever it is idle; up to 3 lapse per job it runs (5 s each, counted), and those jobs are delayed and moved toward the fallback. A silent misconfiguration with no error. | Rejected. |
| *D. Infer from behaviour* (mark a worker concurrent once it polls while it has a `RUNNING` attempt; evaluated beyond the three asked for). | v1.0.0 never polls while executing. | No protocol change. | False positive: a v1.0.0 worker whose result delivery gave up polls while its attempt is still `RUNNING` in the database until the lease expires, and would then be treated as concurrent. Process-local state after a restart. Hidden rule. | Rejected. |

**Recommendation: A** (the orchestrator decides). Details for Stage B if A is chosen:
- Wire: `POST /v1/workers/register` gains optional `"executes_concurrently": true|false`. A non-boolean value fails JSON decoding and returns the existing 400 `MALFORMED_JSON`. No other request or response changes. `GET /v1/workers` shows the stored value (additive).
- Domain: `domain.Worker.ExecutesConcurrently bool` with JSON tag `executes_concurrently`.
- Storage: migration `000006_worker_executes_concurrently.sql`: `ALTER TABLE workers ADD COLUMN executes_concurrently BOOLEAN NOT NULL DEFAULT FALSE;` (a constant default, so existing rows read FALSE with no rewrite on PostgreSQL 16). `UpsertWorker` writes it on insert and on conflict, so an old binary re-registering under the same ID resets it to FALSE. The in-memory repository stores the field on its worker copy.
- Worker: func `register` sends `"executes_concurrently": true`. A worker started with `MINICLOUD_WORKER_SLOTS=1` gets `S = 1` either way.
- Docs that name `000006` for V2-1, to change in Stage B only if A is chosen: `docs/agents/TASKS.md` (reserved touch point, V2-1 row and section), `docs/roadmap.md`, `docs/post-v1/ROADMAP.md`, `docs/post-v1/IMPLEMENTATION_PLAN.md`, `docs/post-v1/ARCHITECTURE.md`, `docs/post-v1/WORKFLOW_MODEL.md`, `docs/PROJECT_STATE.md` section 0.

**Both mixed pairs** are tested at the end (Section 2.9): an old worker (v1.0.0, and `main` at 26b440a) with the new coordinator keeps one slot; the new worker with the old coordinator (`main` at 26b440a) keeps one slot.

### 2.7 In-memory and PostgreSQL parity (item 7)

Both repositories implement Section 2.1 and Section 2.4 with the same predicate, through `domain.AssignableSlots` and `domain.CanReserve`, and the shared runner in `internal/storage/storagetest/assignment.go` checks both.

**Existing INV-10 conformance tests: kept, names unchanged.** `TestConformance_Assign_OneReservationPerWorker` (worker with 1 slot) and `TestConformance_Assign_SkipsWorkerWithRunningAttempt` (worker with 2 slots, no declaration) register workers without the new field, so under this ADR they still expect exactly one reservation and none while `RUNNING`. They become the guard that an **undeclared** worker keeps one slot. Their names stay because historical records cite them (for example `docs/agents/handoffs/P4-08b-evidence/mutation-index.md` and `docs/agents/handoffs/P4-08c-evidence/mutation-index.md`), and renaming would break `make test-docs` against files this slice must not edit. Their doc comments in `storagetest` and the ADR-0017 INV-10 row are updated to say "undeclared worker". `TestConformance_Claim_RespectsWorkerCapacity` stays unchanged.

**New conformance tests (executed, both repositories; `internal/storage/storagetest/assignment_slots.go`):**

| Test | Proves |
|---|---|
| `TestConformance_Assign_ReservationsUpToDeclaredSlots` (executed) | A declared 3-slot idle worker and 5 jobs: one pass reserves 3; a second pass reserves 0. |
| `TestConformance_Assign_ActiveAttemptsCountAgainstSlots` (executed) | A declared 3-slot worker with 1 `RUNNING` attempt gets 2 reservations; with 3 `RUNNING` it gets none. |
| `TestConformance_Assign_ReservationCapPerWorker` (executed) | The bound of Section 2.5. |
| `TestConformance_Assign_ExpiredReservationDoesNotHoldSlot` (executed) | With batch limit 1, one expired reservation stays unrevoked; it does not count, so the worker is reserved up to `S` live ones. |
| `TestConformance_Assign_UndeclaredFourSlotWorkerKeepsOneSlot` (executed) | `total_slots = 4` without the field: one reservation, none while `RUNNING`. |
| `TestConformance_Assign_RotationWithMultiSlotWorkers` (executed) | The two-worker example of Section 2.2, decision by decision, with the `JOB_ASSIGNED` `cursor_before` values. |
| `TestConformance_Claim_FallbackRespectsReservedSlots` (executed) | A declared 2-slot worker with `A = 1` and one live reservation it cannot claim by its own arm (re-registered without that task type): a lapse-arm claim returns `ErrWorkerNoCapacity`. |
| `TestConformance_Assign_ReRegisterLowerSlots_NoNewReservations` (executed) | FS-28: re-registering with fewer slots or without the declaration stops new reservations while `A + R >= S`. |
| `TestConformance_Worker_ExecutesConcurrentlyRoundTrip` (executed) | Option A only: the field is stored, read back, and reset to false by a re-registration without it. |

**PostgreSQL only:** `TestPostgresRepository_Assign_RecheckUsesSlotRule` (executed): a second connection holds the worker row lock and inserts a `RUNNING` attempt that fills the last slot; the pass's snapshot sees the worker as eligible; the connection commits while the pass waits on the lock; the re-check must reject the worker. It pins that the re-check uses the new predicate, not only the snapshot.

### 2.8 Test plan (item 8)

**Pure-function tests (`internal/domain/assignment_test.go`):** `TestReservationRoom_Table` (executed) covers `CanReserve` at the edges (`A + R = S - 1`, `= S`, `R = M - 1`, `= M`, `S = 1`). `TestAssignableSlots_UndeclaredIsOne` (executed) covers `AssignableSlots(false, n) = 1` for `n` in 1..1024 and `AssignableSlots(true, n) = n`. `TestReservationCap_FitsAcceptWindow` (executed, `internal/worker/real_slots_test.go`, the package of `TestPollTiming_AcceptWindowExceedsBackoff`) pins `MaxPollBackoff + MaxReservationsPerWorker x IdlePollInterval < MinAcceptWindow`.

**Property test:** `TestProperty_AssignmentSubState_ModelConformance` checks INV-10 after every step; its INV-10 check is replaced by INV-10r, and its worker set gains `w_c`, which declares 3 slots, and a vacuity guard requires a worker holding two or more reservations (executed).

**Fault suite (rows FS-25 to FS-28, recorded in `docs/fault-injection-plan.md`; tests in `tests/faultinject/real_slots_test.go`):**

| Row | Fault | Planned test |
|---|---|---|
| FS-25 | A worker holding several reservations dies: the reaper marks it `UNHEALTHY` and the next pass revokes all of them without a counted lapse; jobs go to another worker. | `TestFault_MultiReservationWorkerDies_AllRevoked` (executed) |
| FS-26 | Reservations reach a locally full worker (zombie attempt window): each lapses once, the worker becomes ineligible after one window, jobs complete elsewhere. | `TestFault_ReservationToFullWorker_LapsesOnce` (executed) |
| FS-27 | Mixed versions: an undeclared worker with 4 slots under `round_robin` never holds more than one reservation and never has a lapse caused by an extra reservation. | `TestFault_UndeclaredWorker_NoExtraReservations` (executed) |
| FS-28 | Re-registration lowers slots or drops the declaration while reservations are live. | covered by `TestConformance_Assign_ReRegisterLowerSlots_NoNewReservations` (executed) |

The existing fault tests `TestFault_AssignedWorkerDiesBeforePoll`, `TestFault_AssignmentLapseLoop_BoundedByLimit` and `TestFault_PollRoundTripBeyondWindow` must pass unchanged.

**Abuse suite (`tests/abuse/real_slots_test.go`):** `TestAbuse_Register_LargeDeclaredSlots_ReservationCap` (executed, Section 2.5); `TestAbuse_Register_ExecutesConcurrentlyNotBoolean_400` (executed): a string or number value returns 400 `MALFORMED_JSON`. `TestAbuse_Poll_ForeignAssignment_Returns204` must pass unchanged.

**API and worker:** `TestAPI_RegisterWorker_ExecutesConcurrentlyOptional` (executed): absent stores false, `true` stores true, existing response unchanged. `TestRegister_SendsExecutesConcurrently` (executed, `internal/worker`). `TestConcurrency_SingleSlotIsSerial` must pass unchanged.

**Migration (option A only):** `TestMigrator_Migration000006_FreshSchema` (executed) and `TestMigrator_Migration000006_UpgradeFrom000005_WithExistingRows` (executed), following `TestMigrator_Migration000005_FreshSchema`; existing worker rows read `executes_concurrently = false`. Stage B checks whether `internal/storage/postgres/migrator_test.go` pins the latest version.

**Mutants (Stage B, each in `~/mc-mut-claude`, each must fail at least one named test, then pass after restore):**

| # | Mutation | Must fail |
|---|---|---|
| M1 | Ignore reservations in the count (`A < S`) | `TestConformance_Assign_ReservationsUpToDeclaredSlots`, `TestConformance_Assign_OneReservationPerWorker` (executed) |
| M2 | Ignore active attempts (`R < S`) | `TestConformance_Assign_ActiveAttemptsCountAgainstSlots`, `TestConformance_Assign_SkipsWorkerWithRunningAttempt` (executed) |
| M3a | Ignore the declaration, always `S = total_slots` | `TestConformance_Assign_UndeclaredFourSlotWorkerKeepsOneSlot`, `TestConformance_Assign_SkipsWorkerWithRunningAttempt` (executed) |
| M3b | Ignore the declaration, always `S = 1` | `TestConformance_Assign_ReservationsUpToDeclaredSlots` (executed) |
| M4 | Drop the bound `R < M` | `TestConformance_Assign_ReservationCapPerWorker`, `TestAbuse_Register_LargeDeclaredSlots_ReservationCap` (executed) |
| M5 | Count a lapsed reservation (drop `assignment_expires_at > now` from `R`) | `TestConformance_Assign_ExpiredReservationDoesNotHoldSlot` (executed) |
| M6 | Fallback claim keeps today's check (`total_slots - A > 0`) | `TestConformance_Claim_FallbackRespectsReservedSlots` (executed) |
| M7 | `<=` instead of `<` in the rule | `TestConformance_Assign_ReservationsUpToDeclaredSlots`, `TestReservationRoom_Table` (executed) |
| M8a | Re-check under the worker row lock drops the whole sum clause | `TestPostgresRepository_Assign_RecheckUsesSlotRule` (executed by the auditor) |
| M8b | Re-check drops only the live-reservation term `R` from the sum | `TestPostgresRepository_Assign_RecheckCountsLiveReservations/last_slot_filled_by_reservation` (executed in fix round 1) |
| M8c | Re-check drops the cap `R < $4` (the parameter stays in use) | `TestPostgresRepository_Assign_RecheckCountsLiveReservations/eighth_reservation` (executed in fix round 1) |
| M8d | Re-check keeps the `26b440a` predicate (snapshot changed only) | `TestPostgresRepository_Assign_RecheckUsesSlotRule` and the positive-path conformance tests `TestConformance_Assign_ReservationsUpToDeclaredSlots`, `_ActiveAttemptsCountAgainstSlots`, `_ReservationCapPerWorker`, `_RotationWithMultiSlotWorkers`, `_ReRegisterLowerSlots_NoNewReservations` (executed in fix round 1; the auditor ran one test only) |

M8b and M8c are not equivalent mutants. With one coordinator the advisory lock keeps `R` from growing between the pass snapshot and the re-check, but `TestPostgresRepository_Assign_RecheckCountsLiveReservations` makes it grow: a second connection holds the worker row lock while the pass is blocked on it, then commits a live reservation (the last free slot, or the 8th) and releases. The re-check must reject in each case, so the `R` terms are tested, not defence-only. M8d is stricter than a safe re-check on the positive path (it refuses a multi-slot worker the new rule accepts), so it fails the tests that expect those assignments.
| M9 | `UpsertWorker` does not overwrite the field on conflict | `TestConformance_Worker_ExecutesConcurrentlyRoundTrip` (executed) |

New and changed tests run at `-race -count=50`.

### 2.9 Measurement to repeat (item 9)

Real binaries on the builder's own database, the V2-0 audit set-up (`docs/agents/reviews/V2-0-evidence/run-binary.sh` adapted): 8 `sleep` jobs of 5 s, one worker.

| Run | Coordinator | Worker | Policy | Slots |
|---|---|---|---|---|
| 1, 2 | V2-0b | V2-0b | `round_robin`, `fifo_pull` | 4 |
| 3, 4 | V2-0b | V2-0b | `round_robin`, `fifo_pull` | 1 |
| 5, 6 | V2-0b | v1.0.0 (built from tag `v1.0.0`) | `round_robin`, `fifo_pull` | 4 |
| 7 | V2-0b | `main` 26b440a | `round_robin` | 4 |
| 8 | `main` 26b440a | V2-0b | `round_robin` | 4 |

Report for each: wall time, maximum concurrent attempts, attempts per job, lapse events (`JOB_ASSIGNMENT_LAPSED`) and fallback claims. **Expectation to check, not assume:** run 1 about 10 s to 12 s with 4 concurrent (*inferred:* two waves of 5 s; after the first wave the full worker's poll is stale, so its first poll returns 204, and the next pass plus one idle poll sleep add about 0.5 s to 1.2 s); runs 5, 7 and 8 match today's one-slot `round_robin` (about 46.6 s, max 1, zero lapses caused by extra reservations); run 2 matches 10.30 s. Real numbers go into the Stage B handoff.

### 2.10 Rollback and compatibility (item 10)

- **Back to today's behaviour without a code change:** start workers with `MINICLOUD_WORKER_SLOTS=1` (then `S = 1` whatever is declared), or run a worker binary that does not send the field (any v1.0.0 or `main` worker). `MINICLOUD_DISPATCH_POLICY=fifo_pull` remains the policy-level rollback (ADR-0017 Section 4.5).
- **No coordinator-wide switch** is proposed: it would need `internal/config`, which is outside the Stage B scope (question Q4).
- **Code rollback:** revert the Stage B commits. Migration `000006` is forward-only (ADR-0010); the column stays and the older coordinator code neither reads nor writes it. It is not fully inert: a v1.0.0 worker that re-registers under the OLD coordinator leaves the stored flag `TRUE` (the old upsert does not touch it), so after a later roll-forward that worker would be given reservations although it cannot use them. **Decided** (audit evidence `mixed-rollback/summary.txt`). Operator step: Before rolling forward again, run `UPDATE workers SET executes_concurrently = false;` (workers that are still running send `true` again on their next register), or restart all workers.
- **`/v1`:** additive only: one optional request field, one additive field on the worker view. No route, status code or error code changes.
- **`fifo_pull`:** no change. `DispatchNextJob` and its tests, including `TestConformance_FifoPull_IgnoresAssignmentColumns`, stay as they are.

## 3. Consequences
- Under `round_robin`, a declared worker runs up to `total_slots` attempts at once, as under `fifo_pull`; round-robin turns stay per decision.
- Old workers keep exactly today's behaviour (option A).
- A hostile registration can now hold up to 8 reservations and keep claiming under `round_robin` (Section 2.5); the bound is named, tested and recorded.
- The `round_robin` per-wave overhead (Section 1.2) is not removed by this slice; it is measured again.
- [Proposed] A reservation of a worker that re-registered without the reserved task type, or with fewer slots, is not revoked at once; it lapses after the window (owner decision Q7, FS-28). A later slice may revoke it in the pass.

## 4. Open questions for the owner (item 11); answered in the Status section
- **Q1.** Item 6: option A (optional `executes_concurrently` register field and migration `000006`, V2-1 moves to `000007`), B or C?
- **Q2.** If A: the field name and type (boolean `executes_concurrently`, or an integer count of concurrent slots that must be at most `total_slots`)?
- **Q3.** The reservation bound: 8, as a constant? Or another value, or a setting (a setting needs `internal/config`)?
- **Q4.** Should a coordinator-wide switch force one assignable slot per worker (operator rollback without touching workers)? It needs `internal/config`, which is outside the Stage B scope.
- **Q5.** The task names SEC-016 for the `total_slots` abuse row; the register `total_slots` bound is SEC-015, and ADR-0017 Section 6.2 cites "SEC-016 CHECK" for it. Update SEC-015 (and fix the ADR-0017 citation), or SEC-016 as written?
- **Q6.** Register the new abuse finding (Section 2.5) with the next SEC number in Stage B?
- **Q7.** Should the pass revoke at once a reservation whose worker re-registered without that task type or with fewer slots, instead of letting it lapse (FS-28)?
- **Q8.** The per-wave overhead (about half an interval plus one idle poll sleep after a full worker frees slots) stays. Is a later slice wanted to trigger a pass when a full worker frees a slot?
