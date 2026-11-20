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
  - Planned tests for each path are at :504-522.
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
