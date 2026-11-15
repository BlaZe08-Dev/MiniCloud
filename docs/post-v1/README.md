# MiniCloud Post-V1 Architecture Pack

**Purpose:** establish the approved architectural direction after the `v1.0.0` release and give Claude Code, Gemini, and human contributors one coherent set of implementation constraints.

**Status:** design only (designed, not executed). Nothing in this directory is implemented. The pack was written by the owner from a static review of `main` at 166c719 (tests not re-run) and merged into the repository by task PV-01 with the corrections of the post-V1 integration spec. The decision record is [ADR-0018](../adr/0018-ai-assisted-workflow-dispatch.md).

## Where this fits (owner decision D1)

The pack does not replace the V2 and V3 roadmap; it is merged into it by affinity:

- **V2** gains the node and capability model (static inventory plus telemetry) and capability-aware scheduling inside the ADR-0017 assignment pass.
- **V3** gains node identity and authentication, Node Agent evolution of the worker runtime, isolation and remote nodes.
- **V4 (new)** is the AI workflow layer: workflows, workspaces and artifacts, CLI adapters, tool-aware placement, the Intelligence Dispatcher and the user interfaces.
- **Later:** coordinator HA, design-only until the need is shown.

The slice-by-slice sequence is in [`ROADMAP.md`](ROADMAP.md) and in the project roadmap [`docs/roadmap.md`](../roadmap.md).

## Terms used in every document here (owner decision D3)

- **Node:** one physical machine. The `nodes` table is identity only.
- **Hardware inventory, telemetry and installed tools** are three separate concerns with separate tables and update rates (`node_hardware`, `node_telemetry`, `node_tools`), never one wide node row.
- **Worker:** the MiniCloud execution agent. A worker belongs to at most one node (`workers.node_id`, nullable; a worker with no node is a valid legacy V1 worker).
- **Tool:** an AI CLI such as Claude Code, Codex or Gemini CLI. A worker launches a tool through an adapter during an attempt. Starting a tool never creates a worker.

## Read in this order

1. [`CURRENT_BASELINE.md`](CURRENT_BASELINE.md) — what V1 actually contains and what this review verified.
2. [`ARCHITECTURE.md`](ARCHITECTURE.md) — the system-level extension while preserving the V1 core.
3. [`ADR-0018`](../adr/0018-ai-assisted-workflow-dispatch.md) — the architectural decision, the trust model (D2) and the device model (D3).
4. [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md) — nodes, hardware inventory, telemetry and tool readiness (V2-1, V3-3, V4-3).
5. [`WORKFLOW_MODEL.md`](WORKFLOW_MODEL.md) — workflow/task DAG, manifests, dependencies, and lifecycle mapping (V4-1).
6. [`WORKSPACE_ARTIFACTS.md`](WORKSPACE_ARTIFACTS.md) — isolated workspaces, patches, and artifact exchange (V4-1).
7. [`CLI_ADAPTERS.md`](CLI_ADAPTERS.md) — safe provider-specific CLI process integration (V4-2).
8. [`INTELLIGENCE_DISPATCHER.md`](INTELLIGENCE_DISPATCHER.md) — prompt interpretation and task planning (V4-4).
9. [`INTERFACES.md`](INTERFACES.md) — API, CLI, SDK, chat interface, and external-agent ingress (V4-5).
10. [`SECURITY.md`](SECURITY.md) — trust boundaries and mandatory security gates.
11. [`ROADMAP.md`](ROADMAP.md) and [`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md) — sequencing and acceptance gates.
12. [`OPEN_DECISIONS.md`](OPEN_DECISIONS.md) — decisions that must be answered before their implementation.

## Authority and compatibility

- `AGENTS.md` sets the source-of-truth precedence. In short: `AGENTS.md`, then the released V1 source code and contracts, then accepted ADRs, then this directory, then `docs/roadmap.md` and `docs/PROJECT_STATE.md`. If two documents contradict each other, report the conflict and propose the smallest correction; do not silently choose one.
- V1.0.0 is a released baseline. Do not rewrite its historical behavior, release notes, test evidence, or migrations.
- The existing V1 job lifecycle, attempt leases, PostgreSQL source of truth, retries, cancellation, recovery, and dispatch policies remain the foundation.
- This pack describes a **future extension**. None of the AI dispatcher, node capability registry, CLI adapters, workflow DAG, workspace service, chat UI, SDK, or coordinator failover described here should be represented as implemented.
- Existing `AGENTS.md` security, worktree, review, and testing rules remain in force. This pack adds direction; it does not waive those rules.
- If a design statement conflicts with V1 source code, the V1 code and released contract remain authoritative until a reviewed ADR and migration explicitly change them.

## What this is not

MiniCloud is not becoming an IDE, a general-purpose coding assistant, or a clone of another agent product. The chat surface is a simple job/workflow submission and monitoring interface. The actual coding agents are existing local CLI applications invoked by node-local adapters.
