# Node Capability Registry and Node Agent Specification

Status: designed, not executed. Slices: V2-1 (nodes, hardware, telemetry, `workers.node_id`), V2-3 (resource-aware policy), V3-2 (authenticated enrollment), V3-3 (Node Agent), V4-3 (`node_tools`, tool-aware placement). See [`ROADMAP.md`](ROADMAP.md).

## Device model (owner decision D3)

| Concept | Storage | Changes | Slice |
|---|---|---|---|
| Node (one physical machine) | `nodes`: identity only (stable node ID, display name, created/last-seen times) | rarely | V2-1 |
| Hardware inventory | `node_hardware`: versioned static inventory (OS, arch, CPU, RAM total, disk, GPU optional) | rarely; a new version row when it changes | V2-1 |
| Telemetry | `node_telemetry`: latest dynamic sample with `sampled_at` (CPU load, free RAM, free disk) | continuously | V2-1 |
| Installed tools | `node_tools`: adapter ID, CLI version, readiness enum, last probe time; never credentials | independently of hardware | designed in the V2-1 ADR, created in V4-3 |
| Worker (MiniCloud execution agent) | existing `workers` table plus nullable `workers.node_id` | per registration | V2-1 |

Rules:

- The three node concerns never share one wide `nodes` row. Telemetry updates never rewrite the hardware row.
- Absent data means "unknown", never "zero" or "unlimited".
- A worker with `workers.node_id` NULL is a legacy V1 worker; it stays valid and eligible under the existing policies.
- Registering a node and registering a worker are separate protocol steps; the V1 worker registration is unchanged.
- Resource limits (CPU, RAM, disk, GPU) belong to the node and are shared by its workers: the V2-3 policy reserves and checks resources at node level and slots at worker level (OD-09).
- Tool inventory is per node, because tools are installed on the machine, not in a worker process.
- Claude Code, Codex and Gemini CLI are tools that a worker launches through adapters during an attempt. Starting a CLI never creates a worker.
- How a stable node ID is chosen and kept before V3 authentication (for example a configured ID or a persisted generated ID) is OD-19, answered in the V2-1 ADR; V3-2 later binds it to an authenticated enrollment.
- How many worker processes a node runs, and whether one Node Agent process hosts several worker identities, is open (OD-07, OD-20) until V3-3. V2-1 only needs a worker to report which node it runs on.

## Node Agent responsibility

A Node Agent (V3-3) is the node-local runtime responsible for:

- stable node identity and secure enrollment
- capability discovery and periodic reporting
- installed CLI adapter discovery
- local tool readiness checks
- receiving authenticated assignments
- launching and supervising registered adapters
- enforcing local concurrency/resource limits
- collecting logs, results, patches, and artifacts
- reporting task lifecycle and health

The intended direction is to evolve the existing worker runtime rather than create two unrelated daemons competing for the same node. Keep the V1 worker protocol compatible while the new protocol is introduced.

## Capability categories

### Static hardware/software inventory (`node_hardware`, V2-1)

- node ID, agent version, OS, architecture
- CPU vendor/model, physical/logical core counts
- total RAM
- GPU vendor/model, VRAM, driver/runtime details where discoverable
- disk capacity and available space for the configured workspace volume
- network interface class/throughput estimate only where measured reliably
- installed adapter names and versions belong to tool readiness below (`node_tools`), not to this table

### Dynamic telemetry (`node_telemetry`, V2-1)

- CPU utilization/load
- available and used RAM
- GPU utilization and available VRAM where supported
- disk free space
- active task count and configured execution slots
- per-adapter active process count
- node health and last successful capability refresh

Telemetry is advisory and time-sensitive. The scheduler must account for sample age and use conservative safety margins.

### Tool readiness (`node_tools`, V4-3)

For each installed tool, report:

- canonical adapter ID
- executable discovery result and version
- supported invocation modes
- supported task capabilities
- model options exposed by the adapter
- authentication readiness enum: `READY`, `NEEDS_LOGIN`, `EXPIRED`, `UNKNOWN`, `UNAVAILABLE`
- last probe time and probe result category

Never report API keys, OAuth tokens, credential file contents, environment dumps, or raw authentication database contents.

## Hardware-aware placement

Use hard constraints first:
- tool/adapter exists and is ready
- node is online and authenticated
- task capability is supported
- enough free RAM/VRAM/disk/CPU budget exists
- a compatible workspace is available
- execution policy allows the requested operation

Then rank eligible candidates using deterministic policy inputs such as:
- current resource headroom
- estimated task resource profile
- current queue/active workload
- data/workspace locality
- user preference and fairness
- historical task duration/resource observations

A more powerful device is not automatically the best placement. A 16 GB RAM / 6 GB VRAM device may be suitable for some tasks, but the decision depends on the specific CLI, model, workload, current utilization, and required resources.

## Placement algorithm (proposed)

Status: **[Proposed]**, designed, not executed. Slice V2-3; needs the V2-3 ADR and owner approval (OD-21 to OD-25 in [`OPEN_DECISIONS.md`](OPEN_DECISIONS.md)). Recorded by PV-02 after the owner's question of 2026-11-17. Nothing here is implemented.

### Shape: filter, then score, then tie-break

| Step | Rule | Label |
|---|---|---|
| 1. Filter | Drop every worker that cannot run the job: task not in `supported_tasks`; OS or architecture mismatch; required tool missing (V4-3); no free slot; too little free RAM. A job constraint field would be additive to `/v1`. | [Proposed] |
| 2. Score | Rank the survivors by free capacity and measured speed. The function is open (OD-21). | [Proposed] |
| 3. Tie-break | Stable round-robin in ADR-0017 order: worker ID in byte order after the per-task-type cursor, wrapping. | [Proposed]; the rotation rule itself is [Decided, ADR-0017 section 2.3] |

### Inputs

| Input | Rule | Label |
|---|---|---|
| Speed | V2-2 benchmark results, the median of N runs (N open, OD-22). Not datasheet specs. | [Proposed] |
| Telemetry (CPU load, free RAM) | Smoothed with an exponential moving average plus hysteresis (parameters set from V2-2 measurements, OD-22). | [Proposed] |
| Freshness | Every capability and telemetry record has a time-to-live (values open, OD-23). An expired record counts as unknown. Unknown fails a hard constraint and never passes it. | [Proposed]; "absent data means unknown, never zero or unlimited" and "stale telemetry is never treated as current" are [Decided, ADR-0018 / V2-1 exit criterion in `ROADMAP.md`] |

### What already exists (V1 code, not part of the proposal)

| Fact | Where | Label |
|---|---|---|
| The assignment pass filters eligible workers by `supported_tasks`, health, heartbeat and poll recency, and free slots | `internal/storage/postgres/assignment.go:387` and `:476` (`$1::text = ANY(w.supported_tasks)`) | [Decided] confirmed by code reading |
| The `fifo_pull` claim filters queued jobs by the worker's `supported_tasks` | `internal/storage/postgres/repository.go:848` (`task_type = ANY($1::text[])`) | [Decided] confirmed by code reading |
| The fallback claim filters by `supported_tasks` | `internal/storage/postgres/assignment.go:578` | [Decided] confirmed by code reading |
| Round-robin assignment with one cursor per task type | [ADR-0017](../adr/0017-assignment-round-robin.md) section 2.3 | [Decided] |
| Capped exponential backoff with jitter on the worker poll loop | `internal/worker/client.go:74` (`ComputeBackoffWithJitter`) | [Decided] confirmed by code reading |
| Capped exponential backoff with jitter for job retries | `internal/domain/retry.go` (`ComputeBackoff`, ADR-0015) | [Decided] confirmed by code reading |

No V1 code filters by OS, architecture, tool or RAM, and nothing scores workers by speed or capacity.

### V1 rules that stay

- PostgreSQL is the source of truth; the coordinator decides placement; the worker only pulls ([AGENTS.md](../../AGENTS.md) hard rule 4 and the engineering rules).
- The ADR-0017 invariants (INV-1 onward) must still hold; V2-3 does not change them without an ADR amendment.
- The `/v1` API stays additive-only: a job constraint field is a new optional field, never a changed one (ADR-0004). `round_robin` and `fifo_pull` stay selectable.

## Registration and heartbeat

Node identity and capability registration must be authenticated before any remote node is accepted (V3-2). Before V3, nodes exist only on the localhost deployment (SEC-004 mitigation unchanged). Separate slow-changing inventory from fast-changing telemetry to avoid rewriting large capability blobs on every heartbeat.

The server should store:
- the current hardware inventory version and its timestamp
- current health/readiness
- tool inventory versions
- resource telemetry timestamp
- active reservations/leases

Use versioned capability documents. Unknown fields should be safely ignored only where compatibility rules permit; required unknown capability versions should fail closed.

## Slot semantics

A slot represents a real concurrently supervised execution capacity, not merely a configured number. Do not advertise N slots while the agent serially executes one task. The V1.0.0 worker did exactly that (`available_slots` equalled `TotalSlots` while one task ran at a time; confirmed by code reading in PV-01). Slice V2-0 (ADR-0019) fixes it: the worker runs up to `TotalSlots` attempts concurrently and reports `TotalSlots` minus the running attempts. Introduce concurrency only with explicit per-tool and per-node limits, resource accounting, cancellation behavior, and race tests.

## Platform support

The design should be cross-platform (a design goal, not a V1 capability; support status in [`ARCHITECTURE.md`](ARCHITECTURE.md) section 12). Process discovery and launching must account for Linux, macOS, and Windows differences. Do not hardcode PowerShell or a POSIX shell into the common domain model; implement platform-specific process handling behind the Node Agent/adapter boundary.
