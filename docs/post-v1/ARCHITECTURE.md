# MiniCloud Post-V1 Architecture

Status: designed, not executed. Nothing below is implemented in V1.0.0. Version placement of each component is in section 11 and in [`ROADMAP.md`](ROADMAP.md).

## 1. Mission

MiniCloud remains a distributed job-execution platform. The post-V1 extension adds an **AI-assisted intelligent dispatch layer** that can route well-defined subtasks to existing AI CLI applications installed on connected nodes.

The platform coordinates work. It does not become the coding agent itself.

## 2. Non-negotiable continuity with V1

The following remain authoritative:

- Go-based control plane and node-side agent.
- PostgreSQL as the durable source of truth for accepted jobs, attempts, and workflow metadata.
- Existing job/attempt state machine and lease/retry/cancellation semantics.
- At-least-once execution semantics. Never promise exactly-once external side effects.
- Existing `round_robin` and `fifo_pull` policies remain available as baselines.
- Existing `/v1/jobs` API remains backward compatible.
- Existing allowlisted task execution remains supported.
- Domain logic remains independent of HTTP, database drivers, CLI vendors, and model-provider SDKs.
- New features are added incrementally to the same codebase; no rewrite or replacement of the V1 core.

## 3. Logical architecture

```text
User / Primary AI CLI / SDK / mc CLI / MiniCloud Chat
                         |
                  Authenticated Ingress
                         |
              Workflow & Job API Layer
                         |
               Workflow Orchestrator
                         |
             Intelligence Dispatcher
        (interpret prompt, plan, create DAG)
                         |
              Validated Task Graph
                         |
          Deterministic Placement Scheduler
      (hard constraints, capacity, fairness, policy)
                         |
              Durable PostgreSQL State
          (workflows, tasks, jobs, attempts,
             leases, events, artifact metadata)
                         |
               Node Assignment Protocol
                         |
       +-----------------+------------------+
       |                 |                  |
   Node Agent A      Node Agent B       Node Agent N
       |                 |                  |
 Capability Registry  Capability Registry  Capability Registry
       |                 |                  |
 CLI Adapter Registry  CLI Adapter Registry CLI Adapter Registry
  Claude/Codex/...      Gemini/Codex/...     Shell tasks*
       |                 |                  |
 Isolated Workspace  Isolated Workspace  Isolated Workspace
       |                 |                  |
 Result / Patch / Artifact Manifest / Logs
                         |
                Artifact Service
                         |
              Workflow Integration Step
```

`*` Shell/script execution is not an unrestricted command channel. It is subject to the security model and explicit execution policy.

## 4. Component ownership

| Component | Responsibility | Must not own |
|---|---|---|
| Ingress adapters | Accept a user request or structured plan from API, CLI, SDK, chat, or an external AI agent | Scheduling authority or direct database writes |
| Workflow API | Validate requests, authenticate/authorize, expose workflow resources | LLM reasoning or process execution |
| Workflow orchestrator | Persist workflow/task state, enforce dependencies, advance ready tasks | Arbitrary host command execution |
| Intelligence Dispatcher | Interpret prompt, propose decomposition, task constraints, tool preferences, and dependency graph | Direct DB mutations, leases, leader state, or unreviewed node commands |
| Capability Registry | Maintain node hardware, runtime capacity, installed tool metadata, and readiness | Secrets, raw credentials, or job lifecycle authority |
| Deterministic Scheduler | Filter eligible nodes/tools and commit placements according to policy and capacity | Prompt interpretation or unchecked LLM recommendations |
| Node Agent | Report capabilities, receive assignments, launch approved adapters, supervise processes, return results | Global workflow state or direct database access |
| CLI Adapter | Encapsulate provider-specific CLI discovery, invocation, cancellation, and result parsing | Shell-string construction or credential extraction |
| Workspace/Artifact Service | Create isolated task workspaces and transfer immutable artifacts/patches | Concurrent shared-directory mutation |
| PostgreSQL repository | Durable source of truth and transactional invariants | Transient process execution |
| Chat UI | Submit requests, show workflow/task status, logs, approvals, and outputs | IDE/editor functionality or a separate agent runtime |

## 5. Planning versus placement

Use two distinct stages:

1. **Intelligence planning:** an LLM-backed planner interprets the request and produces a typed, schema-validated task graph. It may recommend a tool family or resource profile.
2. **Deterministic placement:** the scheduler checks hard constraints and live node/tool readiness, then chooses the actual node and adapter. It owns capacity reservation and assignment.

The LLM is advisory. It cannot override resource limits, security policy, task dependencies, authentication, leases, or scheduler invariants.

## 6. Node model (owner decision D3)

- A physical device is a **Node**, a separate entity. The `nodes` table holds identity only.
- Hardware inventory (changes rarely), dynamic telemetry (CPU load, free RAM, free disk; changes continuously) and installed CLI tools (change independently of hardware) are **three separate concerns in separate tables** (`node_hardware`, `node_telemetry`, `node_tools`), never one wide `nodes` row. Telemetry updates never rewrite the hardware row.
- A **worker is the MiniCloud execution agent** and belongs to at most one node (`workers.node_id`, nullable). A V1 worker with no node is a legacy worker; it stays valid and eligible under the existing policies.
- Claude Code, Codex and Gemini CLI are **tools**. A worker launches a tool through an adapter during an attempt. Starting a CLI never creates a worker.
- Registering a node and registering a worker are separate protocol steps; the V1 worker registration is unchanged.
- Resource limits (CPU, RAM, disk, GPU) belong to the node and are shared by its workers: the V2-3 policy reserves and checks resources at node level and slots at worker level. Tool inventory is per node, because tools are installed on the machine, not in a worker process.
- A worker has multiple concurrent execution slots only when it can actually supervise that concurrency (V2-0).
- How many worker processes a node runs, and whether one Node Agent process hosts several worker identities, is open (OD-07, OD-20) until V3-3. V2-1 only needs a worker to report which node it runs on.

Keep V1's `Worker` contract working. Do not overload V1's `supported_tasks` field with hardware or provider metadata. Details: [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md).

## 7. Workflow model

A workflow is a parent request. It contains a DAG of tasks. Each ready task is dispatched as a normal durable execution unit and uses the existing attempt/lease/retry principles. A workflow task is not considered complete until its required artifacts are committed and all dependency conditions are satisfied.

Existing simple jobs remain valid without a workflow parent.

## 8. Storage evolution

PostgreSQL remains authoritative. New schema concepts are expected for:

- workflows and workflow-level state
- workflow task definitions and dependency edges
- task-to-job linkage
- node identity (`nodes`), versioned hardware inventory (`node_hardware`) and the latest telemetry sample (`node_telemetry`), from V2-1; nullable `workers.node_id`
- installed CLI tool inventory/readiness (`node_tools`, designed in the V2-1 ADR, created in V4-3)
- resource reservations or capacity accounting
- workspace snapshots and artifact metadata
- approval/audit records
- coordinator leadership epoch only if/when failover is designed

All changes require forward-only migrations (the next free number is `000006`), an ADR, lock-order analysis, concurrency tests, and compatibility review. Large artifact bytes should not be placed in ordinary job rows; store content-addressed artifact objects separately and keep metadata/checksums in PostgreSQL.

## 9. Coordinator availability

V1 has one active coordinator process. PostgreSQL restart recovery is not leader election.

Automatic coordinator failover is a later architecture milestone. Raft is a candidate, not an implementation commitment. Before adopting consensus, separately design:
- membership and quorum
- leader epoch/fencing tokens
- single-writer enforcement for dispatch and workflow transitions
- durable state replication/availability for PostgreSQL
- behavior during partitions and quorum loss
- recovery and operator procedures

Raft alone does not make the PostgreSQL database highly available.

## 10. UI boundary

The MiniCloud chat interface is a lightweight job/workflow interface:
- request submission
- task plan preview and approval
- workflow progress
- node/tool status
- logs and artifact/result links
- cancellation and retry controls

It is not a code editor, IDE, agentic workspace, or replacement for Claude Code/Codex/Gemini.

## 11. Layering, version placement and compatibility rules

A layer may only depend on layers below it:

1. **V1 core, unchanged:** domain state machine, jobs, attempts, leases, retries, cancellation, recovery, assignment-based dispatch (ADR-0017), PostgreSQL.
2. **V2:** capability registry (static inventory plus telemetry) and capability-aware policy inside the assignment pass.
3. **V3:** identity and authorization, isolation (Docker), Node Agent, remote transport.
4. **V4:** workflow orchestrator, workspace/artifact service, CLI adapters, Intelligence Dispatcher, interfaces.

Compatibility rules for every slice:

- `/v1` stays additive-only; new workflow and node endpoints are additive under `/v1`; a breaking change needs `/v2`, an ADR and a deprecation note (ADR-0004).
- Migrations are forward-only and start at `000006`; each needs an ADR, lock-order analysis and concurrency tests.
- `domain.Worker` `supported_tasks` is not overloaded with hardware or provider metadata. `jobs.task_type` stays an allowlist; AI CLI work is a registered execution kind with a typed spec, not a free task name.
- Device model (D3): see section 6.
- The 64 KiB V1 payload and output limits stay; prompts and artifacts are referenced, not inlined.
- A workflow task maps onto the existing job/attempt lifecycle. No second lease, retry or cancellation system.
- The planner is untrusted input. It proposes; the deterministic scheduler validates and decides. It never writes authoritative state.
- Provider credentials stay on their node; the coordinator only stores readiness metadata.
- Trust model (D2): AI CLI tasks may run on the host only for the owner's own projects, under a dedicated restricted OS account, inside a per-task workspace, read-only profile first, with approval for file changes. Any other or untrusted workload waits for Docker isolation (V3-1). See ADR-0018.
