Task: V2-1-ADR (design the node and capability model, design only)
Branch: agent/claude/V2-1-ADR
Work tip (last commit before this report): b7e802d
Date: 2026-11-20

# V2-1-ADR report

## What I did

1. Wrote docs/adr/0022-node-capability-model.md, status Proposed (docs/adr/0022-node-capability-model.md:3-5). It has all the sections the task asked for: context (:20), decision (:38), data model (:45, with `node_tools` designed at :131 and created in V4-3), protocol (:159), identity (:226), platform scope (:244), resource model (:289), field limits (:368), unknown-data rules (:392), legacy workers (:405), migration 000007 and rollback (:412, rollback at :429), threat model (:436), test plan (:456, mutants at :524), implementation slices (:549), open items and owner questions (:565). Every statement is labelled Decided (with its source) or Proposed (:9-14).
2. Copied /home/blazeee/Downloads/v2-1-adr-decisions.txt to docs/agents/handoffs/V2-1-orchestrator-decisions.md. `cmp` found no difference ("identical").
3. In docs/agents/TASKS.md I changed only the status bullet of the V2-1 section (docs/agents/TASKS.md:111). It now points at ADR-0022 (Proposed).
4. Commits: cd7ec34 (handoff copy), 1f2fdab (ADR), b7e802d (TASKS), then this report in its own commit.

## Main design points (all in ADR-0022)

- **Identity, as the owner decided (OD-19):** a random v4 UUID kept in a 0600 file. Registration is idempotent. `display_name` is a label with no scheduling meaning. V3-2 adds a credential column later without changing V2-1 (:226-243). The ADR says plainly that impersonation by a known ID is not solved before V3-2 (:239, threat T1 at :442).
- **Platform, as the owner decided (OD-08):** a versioned contract in `internal/domain`, and a `Probe` interface in a new internal/platform package with a Linux file and a `!linux` fallback that reports only os and arch. A Windows or macOS adapter is one new build-tagged file, with no change to the contract (:251-276).
- **Resources, as the owner decided (OD-09):**
  - The reserved amount is computed from PENDING/RUNNING attempts plus live round-robin reservations. It is never a stored counter.
  - It is reserved atomically under a `FOR NO KEY UPDATE` lock on the node row. The new lock order is jobs, nodes, workers, attempts (:326-334).
  - The release table covers completion, failure, cancellation, lease expiry and coordinator restart, and cites the code that changes each state (:335-347).
  - Planned tests for each path are at :507-522.
  - Reservations are built in V2-3, not V2-1, because the per-job resource demand depends on OD-24, which is still open (:43, :301).
- **OD-23:** the ADR sets no TTL or interval values. It says how they will be derived from measurement (:572).

## Things I found

- **Doc conflict (confirmed by code reading).** docs/protocol.md:225 shows the register key `"worker_id"`. The code uses `"id"`: internal/domain/worker.go:9 and internal/worker/client.go:354. The ADR proposes fixing it in slice V2-1c (:36). I did not edit docs/protocol.md.
- **New finding warranted, not registered.** The ADR proposes an entry for T1, provisionally SEC-070 (the register currently ends at SEC-069; docs/security/findings.md:81). I did not register it because this task does not own findings.md. That is owner question OQ-7 (:582).

## Owner questions (not chosen; ADR-0022:574-582)

- OQ-1: the retention bound for hardware history. This blocks V2-1b.
- OQ-2: whether node reporting is opt-in or on by default, and the state directory.
- OQ-3: the default telemetry interval.
- OQ-4: GPU discovery scope.
- OQ-5: OS headroom.
- OQ-6: whether node read views ship before V3-2.
- OQ-7: how the finding is registered, and by which task.

## Gates (executed, output copied from the log)

```
gofmt: clean
--- go vet
rc=0
--- go build
rc=0
--- make test-docs
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	1.867s
rc=0
```

I did not run `go test -race ./...` or `make test-db`, because the task changes no Go code and no schema, and the task did not list them.

## What I did not verify

- None of the tests named in ADR-0022 exist. All are planned, and none was run.
- I did not run any mutant.
- The SQL in the ADR has not been run against PostgreSQL. In particular, these are inferred, not executed:
  - the claim that `FOR KEY SHARE` from the FK check does not conflict with `FOR NO KEY UPDATE` (ADR-0022:332);
  - the claim that adding a nullable column causes no rewrite on PostgreSQL 16.
- I have not tested the Linux probe sources (`/proc`, cgroup v2 files, `Statfs`) on this host.
- I did not check whether Go 1.22.2 (go.mod:3) can create the ID file with an exclusive rename on every target platform. The ADR describes the mechanism only.
- I checked the existing test names cited in ADR-0022 section 11 with `grep -rln "func <name>("`, not with `go test -list`.
- docs/agents/TASKS.md:26 (the Active table row for V2-1) still says "needs the V2-1 ADR". I left it as it is because the task said to change nothing else.

The branch is pushed. I did not open a PR or merge anything.

# Fix round 1 (task V2-1-ADR-FIX1)

Date: 2026-11-20. Branch agent/claude/V2-1-ADR, started from tip 846f882. Docs only. Audit: reports/gemini/V2-1-ADR-audit.md on agent/gemini/V2-1-ADR-audit (tip 2146b07). Files changed: docs/adr/0022-node-capability-model.md, this report, docs/agents/TASKS.md (row 26 only). Line numbers below are in the ADR after this round. The decisions are labelled Proposed in the ADR; the owner accepts the ADR later.

## Per finding

- **F-01 (Critical).** `reserved(node)` no longer depends on `workers.node_id`.
  - docs/adr/0022-node-capability-model.md Section 7.2 (:299-319): reserved sums `attempts.node_id` (written once at claim) and `jobs.assigned_node_id` (written at reservation, cleared with `assigned_worker_id`). Both columns come with the V2-3 migration.
  - Section 4.3 (:212-213): registration may change `node_id` freely and never rejects it; running attempts and live reservations keep their node.
  - Section 7.4 (:338): the claim reads `node_id` unlocked, locks the node, locks the worker, and re-reads `workers.node_id`; a mismatch rolls back. A reservation whose `assigned_node_id` differs from the worker's current node is not honoured. This race was not in the audit; I found it while placing the snapshot.
  - Section 7.5: new row for re-registration while holding attempts or reservations. Section 13: new threat T10 (links T1). Section 14.4: new rows for re-registration with attempts, with a live reservation, and the claim race.
  - Self-check of the other rows: the OD-09 restart row and the hardware-change row had the same class of problem only through the join. Restart: now says the snapshot columns are durable, so a post-restart re-registration moves nothing. Hardware change: now says capacity is read from `node_hardware` by the snapshot node ID, never through `workers`. Rows from before V2-3 have NULL node ID and zero demand; stated in Section 7.2.
- **F-02 (Major).** Section 7.5 cites SQL now. Completion and failure: repository.go:1236-1252 (`UPDATE attempts`) and :1257-1264 (`UPDATE jobs`); late terminal commit :1347-1352. Cancellation: :1456-1459 and :1465-1466 (the `UPDATE jobs` statements). Revocation and lapse: assignment.go:216, :231. Claim: assignment.go:701 (`INSERT INTO attempts`) and :719-721 (`UPDATE jobs`). Reaper: :503-510 (`UPDATE workers`). I re-read each range on origin/main (588fcfd) with `git show`.
- **F-03 (Minor).** Lease expiry cites leases.go:302-317 (`closeAttempt`, `UPDATE attempts` at :310) and the switch arms :354-414 (calls at :361, :371, :392, :412). Re-read on origin/main.
- **F-04 (Major).** Section 14.5 gains R8 (lock order, test `TestPostgresRepository_NodeLock_HierarchyEnforced`), R9 (join on `workers.node_id`, tests `TestConformance_Worker_ReRegisterWithActiveAttemptsKeepsNodeDemand` and `..._ReRegisterWithLiveReservationKeepsNodeDemand`), R10 (claim skips the post-lock re-read, test `TestPostgresRepository_NodeReserve_ClaimRechecksWorkerNodeAfterLock`) and N12 (`ON DELETE RESTRICT`, test `TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced`, also added to Section 14.2). R10 and N12 go beyond the exact list in the task; R10 covers the race I added, N12 is the `ON DELETE RESTRICT` mutant the task asked for. All are planned tests.
- **F-05 (Minor).** Planned test `TestConformance_NodeReserve_LiveReservationBlocksPlacement` added to Section 14.4 and paired with R2 (R2 keeps its old test too).
- **In-memory equivalents.** Section 7.5 has a second table: inmemory.go:720, :724, :784-788, :857-861, :1026, :1047, :1061, :350-364, :284-302; inmemory_assignment.go:136, :150, :307, :338. Lines checked on origin/main with `git show` and `awk`.
- **Section 15, V2-3 row.** States the V2-3 migration adds `attempts.node_id`, `jobs.assigned_node_id` and the demand snapshot columns, so V2-1's tables need no change.
- **TASKS.md row 26.** Only the phrase "needs the V2-1 ADR with OD-19 answered" changed, to "the V2-1 ADR (ADR-0022) is written and Proposed, with OD-19 answered".
- **Section 16.** OQ-1 to OQ-7 and the open-items list are unchanged. No Decided statement was touched. I added one sentence to the Status section naming the audit and this round.

## Gates (executed; log copied verbatim)

```
gofmt: clean
--- go vet
rc=0
--- go build
rc=0
--- make test-docs
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	1.890s
rc=0
```

## Not verified

- No planned test or mutant exists or was run. The new SQL ideas (snapshot columns, re-read after the worker lock) are designed, not executed against PostgreSQL.
- The `NOWAIT` technique in `TestPostgresRepository_NodeLock_HierarchyEnforced` is a design; I did not try it.
- I did not check the in-memory line numbers against the audit; I re-read them myself on origin/main only.
- I did not run `go test -race ./...` or `make test-db` (no Go code or schema changed; not in the task gates).
- The audit's PostgreSQL 16 lock-compatibility quote was not re-checked by me.
- Earlier "did not verify" items from the first report still stand, except that the TASKS.md:26 item is now fixed.
