Task: P4-08a revision 1, orchestrator review of ADR-0017 (items R1 to R7)
Branch: agent/claude/P4-08a (revision 0 at 428032e)
Date: 2026-10-31

Design only. Edited: `docs/adr/0017-assignment-round-robin.md` and this report. No production code, test code, migration file, or `tests/docs/citation_allowlist.txt` change. No docker commands. The ADR now has status "Proposed, orchestrator answers recorded (revision 1, 2026-10-31)". Section 9 of the ADR is the traceability record for these changes, with the same items as this report.

**Verification status.** Nothing was executed except `make test-docs`, which exits 0 (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs`). Every current-behaviour claim is labelled `confirmed by code reading` or `inferred` in the ADR's evidence table. New facts from this round are C16 to C21 (worker poll constants, jitter range, HTTP client timeout, `task_type` index coverage, `not_before`, and the phase-4 plan wording).

---

## R1. Shared cursor breaks per-task-type fairness

**What changed.**
- The rule (ADR Section 2.3) now reads and writes a cursor `c_t` per task type. Decisions for other types do not touch it.
- Cursor storage (Section 2.3) is a table `assignment_cursor` keyed by `task_type`. Missing row means an empty cursor. The pass reads the rows for the types in its batch at the start of its transaction and upserts changed rows at the end, in the same transaction as the reservations. Restart, crash and several-coordinator paragraphs are updated.
- Fairness (Section 2.6) no longer states "counts differ by at most 1 with a constant set". Under the one-slot rule (R2) a reservation removes a worker from the eligible set, so that premise does not hold inside a pass. The new statement S1 is "no double selection while another eligible worker waits", with an argument over decisions for one type. The constant-set form is kept as corollary S2, which is what the pure-function test checks.
- Migration sketch (Section 5.3): `assignment_cursor` has `task_type` as primary key and no singleton row.
- INV-4 kept (pure function, constant set). INV-4b added: S1 with interleaved types, a property test of at least 1,000 sequences. Mutation: one cursor for all types, which the test must catch as a double selection.

**Could not verify.** The property test does not exist yet. The mutation is planned, not run.

## R2. A worker that just claimed still looks assignable

**What changed.**
- Eligibility (Section 2.2): a worker needs no `PENDING` or `RUNNING` attempt, and no outstanding reservation. `worker_capacity.available_slots > 0` stays as the real check.
- Free slots (Section 2.4): replaced by a named constant, `effectiveWorkerSlots = 1`. Its doc comment ties it to the worker-concurrency slice (Q5). Each reservation removes the worker from eligibility for the rest of the pass.
- Claim preference (Section 3.2): a poll tries its own live reservation before any fallback job. With the eligibility rule, a worker never holds a reservation while it has a `RUNNING` attempt.
- Invariants: INV-10 (one reservation per worker, and no reservation to a worker with `PENDING` or `RUNNING`) with mutations on each predicate. Test names are planned.

**Further defect found while doing this (R5 item 2, Section 2.4 and Section 4.3).** The pass could read "no `RUNNING` attempt" from a snapshot while the worker claims a fallback job, then reserve a job after that claim commits. The fix: the pass takes `FOR UPDATE` on each worker row it reserves to (bounded by `lock_timeout`), and re-runs the eligibility checks under the lock. The claim takes the same row lock. INV-10b and its mutation are added. Revision 0 said the pass never read `attempts` (Section 1.7). That is corrected: the read is needed, and it is safe only under the worker lock.

**Could not verify.** The race is argued from the lock order, not reproduced. INV-10b is the planned test that would show it.

## R3. Liveness hole when the pass stalls

**What changed.**
- Fallback (Section 3.2) now has two arms. (a) `assignment_lapses >= MINICLOUD_ASSIGNMENT_MAX_LAPSES` (default 3), reason `lapses`. (b) The job has been dispatchable for at least `MINICLOUD_ASSIGNMENT_FALLBACK_AGE`, **and** the assignment pass has been silent for that long, reason `age`. "Dispatchable since" is `GREATEST(created_at, COALESCE(not_before, created_at))`, so no new column is needed.
- **Proposed value.** `MINICLOUD_ASSIGNMENT_FALLBACK_AGE`, default **30 s**, with startup validation that it is at least `MAX_LAPSES x (ACCEPT_WINDOW + INTERVAL)`, which is 18 s by default. Justification: a healthy job that still has lapse budget must not be aged out by its own lapse loop, and 30 s leaves margin over the 18 s lapse path plus one poll gap. The value is a proposal, not measured (R-d).
- **Pass heartbeat.** Migration `000005` adds `assignment_pass_state` (one row, `last_completed_at`). The pass writes it as its last statement in its own transaction. A failed or stopped pass leaves it stale. The age arm reads it.
- Expired reservations (Section 1.3, Section 3.2) are treated as unreserved for fallback. Revision 0 left them stuck when the pass was dead.
- **Worst-case wait when the pass is dead** (Section 6.1, FS-21). For a job that arrives while the pass is silent: `FALLBACK_AGE` (30 s) plus one poll gap (about 3 s). For a job already waiting when the pass stops: `FALLBACK_AGE` after the silence begins, plus the poll gap. The bound holds only while a healthy worker that supports the type polls (R-e).
- Failure row FS-21. Invariant INV-14 (liveness). Test `TestFault_AssignmentPassStalled_JobRunsWithinBound` (planned test): pass disabled, job submitted, clock advanced past the age bound, job claimed with `fallback_reason: age`. A second case covers an expired reservation with the pass disabled. Mutation: remove the age arm.

**Further refinement (R5 item 3 and item 1 of the sweep, Section 3.2).** Revision 0's age rule was unconditional. Under a healthy saturated queue every job waits more than 30 s, so the unconditional rule turns the whole backlog into FIFO fallback and makes `round_robin` behave like `fifo_pull`. The age arm is now gated on pass silence. INV-16 and its test `TestProperty_SaturatedPass_NoAgeFallback` (planned test) pin this, with a mutation that drops the gate.

**Could not verify.** Whether `dispatchable_since` gives the right age for `RETRYING` jobs under real retry schedules. The bound is argued from the code paths, not measured.

## R4. Pass bounds

**What changed.**
- Batch limit `MINICLOUD_ASSIGNMENT_BATCH_LIMIT`, **default 50**, range 1 to 500, applied **per task type** (Section 2.1, D14). The revoke step reads at most the same number.
- **Further defect (Section 2.1).** A single `LIMIT B` over all types would let a head of unplaceable jobs (a type with no eligible worker) fill the batch and starve other types. The candidate read is per type.
- The batch re-runs immediately when a type's read returned a full batch, so backlog drains at `B` per type per transaction. The pass still holds `s.mu` in the single-coordinator build, so a poll waits at most one batch (Section 4.1). This is a liveness dependency on the single loop, stated with its bound.
- **Index (Section 5.3).** Migration `000005` adds `idx_jobs_dispatch_by_type` on `(task_type, created_at, id)`, partial on unassigned `QUEUED` and `RETRYING`. The revision-0 index `idx_jobs_unassigned_fallback` is removed. **Further finding (C19):** `idx_jobs_task_dispatch` in `000001_initial_schema.sql` covers `QUEUED` only, and `idx_jobs_retrying_dispatch` in `000004_leases_retries_cancellation.sql` covers `RETRYING` by `not_before` only. No existing index serves a per-type scan of `RETRYING` rows.
- **Head-of-type guarantee (Section 2.1).** In every pass where some worker of the type is eligible, the oldest dispatchable unassigned job of that type is reserved. Invariant INV-15, property test `TestProperty_BatchHeadNeverStarved` (planned test), mutation: single `LIMIT B` across types.
- **Beyond the batch.** Jobs beyond the batch wait for the next pass. The immediate re-run and the head-of-type guarantee together mean that a newer job within a type cannot pass an older one, and no other type can take its slots.
- **Event cap (Section 2.1).** `JOB_ASSIGNED` carries the first 10 eligible worker IDs and `candidate_count`. The cap is a constant. INV-18, planned test `TestConformance_AssignEvent_CapsCandidates`.

**Could not verify.** The fallback query's plan across task types. It is marked for `EXPLAIN` in slice 5b (Section 5.3). Pass latency at `B = 50` is not measured.

## R5. Consistency sweep and further defects

Sections 4, 5, 6 and 7 were re-read in full. Each statement that R1 to R4 affect was updated. The further defects found, with where each is fixed:

1. **Constant-set fairness premise** (Section 2.6). Invalid once a reservation removes a worker. Replaced by S1.
2. **Pass reads `attempts` and workers without a lock** (Section 1.7, Section 4.3). Unsafe for R2. Fixed by the worker row lock and the re-check under it.
3. **Expired reservations stuck with a dead pass** (Section 1.3, Section 3.2). Fixed by the fallback arm.
4. **Fallback claim of a job reserved to a busy worker** (Section 2.4, Section 3.2). Prevented by the own-first claim preference.
5. **Claim takes a fallback job while the worker holds a reservation** (Section 2.4). Same preference.
6. **Expiry observed but not written** (Section 1.3). The gap between the claim predicate and the pass write is now stated, with the state it leaves.
7. **`RETRYING` jobs had no per-type index** (C19). Fixed by the partial index in migration `000005`.
8. **Phase-4 plan wording** (C21). `docs/phase4-plan.md` Section 3 says the lease sweeper powers assignment expiry and revokes assignments. That is not this design, which uses a separate pass. Added to the docs-to-update list (ADR Section 7) with the exact sentence.
9. **Stated guarantees the mechanism does not support.** "Every eligible worker is selected within `|E|` decisions" is restated as S1, because `E` changes inside a pass. "An idle, polling worker is not skipped in favour of a busy one" is kept, and now argued (Section 2.6).
10. **Section 6.2 batch row** still said "50 candidates per transaction", with no per-type rule and no revoke bound. Replaced with D14.
11. **`total_slots` abuse row** (Section 6.2) was stale after R2. Updated.
12. **Pass and poll share the mutex** (Section 4.1, Section 2.1). A pass delays a poll by one batch. Stated as a bounded liveness dependency.
13. **Freshness gap** (Section 3.1). `last_poll_at` writes are throttled to one per second, so the gap is `max(throttle, poll gap)`, not a sum. Stated so the window margin is not overclaimed.
14. **Policy switch leaves reservations** (Section 5.2). A switch from `round_robin` to `fifo_pull` leaves old reservations in place. `fifo_pull` ignores them and claims them like any queued job, and the job view shows the old assignment until then. Harmless, and stated.
15. **Age-based fallback invisible on the job view** (Section 3.2, Section 5.1). Fixed by a fourth additive field, `assignment_fallback_reason`. This goes beyond Q10's three named fields, so it is listed as R-a below.
16. **Lock-order table** (Section 4.3) said the pass takes no worker lock. Now it takes `FOR UPDATE` on reserved workers, with `lock_timeout`. Test INV-13 (existing, updated) and INV-10b cover it.
17. **Claim predicate versus reservation interaction** (Section 3.5). The "late accept" table said a lapsed worker gets 204 in every case. A healthy worker may now claim as fallback. The table is corrected; no stale token results, because the claim is still the only issuer.

## R6. Poll timing assumption

**Verified in code** (Section 3.1; evidence rows C16, C17, C18 in the ADR):
- The idle sleep (200 ms) and the maximum poll backoff (3 s) are **local variables** in `Run` (`baseBackoff`, `maxBackoff`), not constants. A test cannot read them today.
- `ComputeBackoffWithJitter` scales the capped value by a factor in **[0.5, 1.0)**. It is not "full" jitter. The largest sleep is 3 s, so the ADR's worst-case value holds, but the wording "3 s with full jitter" in revision 0 was wrong.
- `NewAgent` sets the poll HTTP client timeout to 15 s. A poll can therefore take longer than the window in principle.

**What changed.**
- The ADR states the requirement: window > maximum backoff + round trip + margin. It also states that correctness does not depend on the margin: a late poll gets 204, the job lapses and is counted, and only the cost rises (FS-22).
- Config floor raised from 2 s to **5 s** (D6, Section 3.1). A 2 s window lapses on a 3 s backoff. Q4 was accepted "subject to R6", so this follows, and it is listed as R-c.
- Constant extraction (planned, slice 5c, Section 3.1): the idle interval and the maximum backoff become package constants in `internal/worker/client.go`. No behaviour change.
- Test `TestPollTiming_AcceptWindowExceedsBackoff` (planned test), INV-17. It asserts the window floor exceeds the maximum backoff plus margin, and that config validation rejects a window below the floor. Mutation: raise the backoff constant above the floor; the test must fail.

**Could not verify.** The round-trip margin in practice. The 15 s client timeout means the margin is not guaranteed by the code. The extraction and the test are not written.

## R7. Orchestrator answers recorded

Section 8.1 of the ADR is a table of all eleven answers with the rev-0 recommendation, the orchestrator's answer, and the effect on the design. Effects summarised:
- Q1: default flips to `round_robin` before the v1.0.0 tag, in its own reviewed change. `fifo_pull` stays selectable.
- Q2: fallback after `L = 3`, logged, visible on the job view (through the new fourth field).
- Q3: a lost claim response consumes `max_attempts`.
- Q4: 5 s accepted, subject to R6 (floor now 5 s).
- Q5: one job per worker in V1. A worker-concurrency slice is scheduled before the V2 comparison (follow-up, Section 7).
- Q6: both `QUEUED` and `RETRYING`.
- Q7: per task type, with a per-type cursor.
- Q8: separate task.
- Q9: no multi-coordinator claim in V1.
- Q10: yes, the additive fields.
- Q11: no `DRAINING` path in V1.

---

## Remaining open questions

These are not decided by the orchestrator. Each has a recommendation in ADR Section 8.2.

- **R-a.** Confirm the fourth job-view field `assignment_fallback_reason`. Q10 named three fields, and Q2 requires fallback to be visible. Recommendation: yes.
- **R-b.** Confirm that the age arm is gated on pass silence, not on age alone (R3 refinement). Recommendation: yes. Without the gate, a saturated healthy queue becomes FIFO fallback.
- **R-c.** Confirm the accept-window range moves from 2 s to 5 s to 30 s (R6). Recommendation: yes.
- **R-d.** `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` (30 s) and `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` (50) are new config values. They are proposed, not measured. Recommendation: accept the defaults, and tune after the V2 benchmark.
- **R-e.** The liveness bound (FS-21) holds only while a healthy polling worker supports the type. That is a stated limit. A job for a type with no live worker cannot run, and no design fixes that. Recommendation: accept.

Not yet decided, and needing a pass in a later slice rather than an orchestrator answer:
- The fallback query's plan across task types (`EXPLAIN` in slice 5b).
- Whether `migration_000005_test.go`'s neighbours pin the latest migration number (Section 5.3, unverified, as in revision 0).
- The round-trip margin in practice, against the 15 s client timeout (R6).

## What I could not verify (summary)

- Nothing in the design has been run. Every test name is planned.
- The worker constant extraction, the new config, the new tables and the migration are proposals in the ADR. None is in the tree.
- The race in R2 is argued from the lock order, not reproduced. INV-10b is the test that would show it.
- The worst-case liveness bounds in R3 and the per-type index plan in R4 are reasoned from code, not measured.
- `make test-docs` is the only command run. `gofmt`, `go vet`, `go build` and the Go tests were not run, because no Go code changed.
