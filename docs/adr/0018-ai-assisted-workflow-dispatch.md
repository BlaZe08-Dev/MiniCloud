# ADR-0018: AI-Assisted Workflow Planning and Heterogeneous CLI Dispatch

## Status

**Accepted as the post-V1 product/architecture direction** (owner decisions D1 to D3, 2026-11-15; recorded by task PV-01, effective when the owner merges it). Implementation details remain gated by the open decisions in [`OPEN_DECISIONS.md`](../post-v1/OPEN_DECISIONS.md). This ADR does not claim that any post-V1 component is implemented (designed, not executed).

Supporting design: [`docs/post-v1/README.md`](../post-v1/README.md). Sequence: [`docs/post-v1/ROADMAP.md`](../post-v1/ROADMAP.md) and [`docs/roadmap.md`](../roadmap.md).

## Amends ADR-0004

This ADR amends [ADR 0004](0004-version-boundaries-and-compatibility.md) section 1 (owner decision D1). ADR-0004's V1, V2 and V3 boundaries stay as written, with these additions by affinity: capability inventory and capability-aware scheduling join **V2**; node identity and authentication, Node Agent evolution of the worker runtime, isolation and remote nodes join **V3**; the AI workflow layer (workflows, workspaces and artifacts, CLI adapters, tool-aware placement, the Intelligence Dispatcher, interfaces) is a new **V4** that builds on V3; coordinator HA stays **later** and design-only. ADR-0004's compatibility and evolution policy (section 2) applies unchanged to V2, V3 and V4: additive-only `/v1`, forward-only migrations (next free number `000006`), architecture tests. ADR-0017 is unchanged.

## Context

MiniCloud V1.0.0 provides durable distributed job execution, worker leases, retries, cancellation, recovery, and assignment-based round-robin dispatch. It currently executes only a small allowlist of built-in tasks.

The next product direction is to let a user submit a development or computational request from a primary AI agent or MiniCloud interface. MiniCloud should decompose the request into dependent tasks and distribute them across connected devices that have different hardware and different locally installed AI CLI applications.

Examples of local tools include Claude Code CLI, OpenAI Codex CLI, and Gemini CLI. These are examples, not a closed vendor list. A device may have more than one tool installed.

## Decision

1. Preserve the V1 distributed execution core and its lifecycle guarantees.
2. Add a workflow/task-graph layer for multi-step requests.
3. Add an Intelligence Dispatcher that converts natural-language requests into a typed, validated task graph.
4. Add a Node Capability Registry for hardware, current resource availability, installed tools, versions, capabilities, and readiness.
5. Add a provider-neutral CLI Adapter interface with provider-specific implementations.
6. Extend the worker-side runtime into a Node Agent capable of launching approved local CLI applications even when their terminal UI is not already open.
7. Add deterministic capability-aware placement. The scheduler remains authoritative; LLM recommendations are advisory only.
8. Add isolated workspaces and an artifact/patch exchange path. Concurrent agents must not mutate the same working directory.
9. Provide four interaction surfaces over the same backend model: HTTP API, CLI, SDK, and a simple chat/job-monitoring UI.
10. Support integration from an external primary AI agent through an explicit authenticated ingress contract. Do not assume that an agent running on the coordinator is automatically connected to MiniCloud.
11. Defer automatic coordinator failover/consensus until a separate design proves the need and accounts for PostgreSQL availability.

## Trust model for AI CLI tasks (owner decision D2)

AI CLI tasks may run on the host only for the owner's own projects, under a dedicated restricted OS account, inside a per-task workspace, read-only profile first; file changes need approval. Any other user's or untrusted workload waits for Docker isolation (V3-1, ADR-0005).

Relation to the existing rules (no rule is relaxed): `AGENTS.md` invariant 7 ("only allowlisted tasks run until V3 Docker isolation") and hard rule 3 ("never run untrusted task payloads directly on the host") stay in force. AI CLI execution arrives in V4-2, after the V3-1 isolation gate; it is a registered execution kind with a typed spec, not a free task name; and D2 limits host execution to the owner's own projects, which the owner treats as trusted. The prompt and the CLI's output remain untrusted data. An AI CLI may itself run commands inside its workspace; the read-only profile, the restricted account and the workspace boundary bound what it can reach, and the CLI's own permission flags are an additional layer, not the boundary.

## Device model (owner decision D3)

- A **Node** is one physical machine and a separate entity. The `nodes` table is identity only.
- Hardware inventory (changes rarely), dynamic telemetry (CPU load, free RAM; changes continuously) and installed CLI tools (change independently of hardware) are three separate concerns in separate tables (`node_hardware`, `node_telemetry`, `node_tools`), never one wide `nodes` row.
- A **worker is the MiniCloud execution agent** and belongs to at most one node (`workers.node_id`, nullable). Claude Code, Codex and Gemini CLI are **tools** that a worker launches through adapters. Starting a CLI never creates a worker.

Consequences:

- A V1 worker with no node (`workers.node_id` NULL) is a legacy worker; it stays valid and eligible under the existing policies.
- Registering a node and registering a worker are separate protocol steps; the V1 worker registration is unchanged.
- Resource limits (CPU, RAM, disk, GPU) belong to the node and are shared by all of its workers, so the V2-3 policy reserves and checks resources at node level and slots at worker level.
- Tool inventory is per node, because the tools are installed on the machine, not in a worker process.
- How many worker processes a node runs, and whether one Node Agent process hosts several worker identities, is left open (OD-07, OD-20) until V3-3. V2-1 only needs a worker to report which node it runs on.

## Core invariants

- PostgreSQL remains the source of truth.
- Existing V1 job and attempt semantics remain in force.
- A workflow is not an excuse to weaken stale-attempt rejection, leases, retries, or cancellation.
- An LLM cannot write directly to authoritative state or assign work without deterministic validation.
- A node only launches registered adapters and approved task profiles.
- No arbitrary shell command string is accepted from an LLM and passed to a shell.
- Credentials remain on the node that owns them; the coordinator stores readiness metadata, not local CLI secrets.
- Every task has an immutable input/workspace reference and a bounded execution policy.
- Artifacts are checksummed and associated with a task attempt.
- Failed or duplicated attempts cannot overwrite a newer committed result.
- Network-connected nodes must be authenticated before remote execution is enabled.
- One mutable workspace per attempt; no two attempts share a mutable working directory.

## Alternatives rejected

- **Replace MiniCloud with a new AI coding platform:** rejected. The existing distributed systems project remains the foundation.
- **Give the LLM direct scheduler or database authority:** rejected due to correctness, security, and reproducibility risks.
- **Run arbitrary `sh -c` / PowerShell command strings:** rejected. Adapters invoke known executables using argument arrays and policy-controlled environments.
- **Let all agents edit one shared checkout:** rejected due to concurrent file corruption and nondeterministic integration.
- **Treat a GPU-heavy machine as always best:** rejected. Placement is based on per-task requirements and current capacity; many coding CLI workloads are CPU/RAM/network bound rather than GPU bound.
- **Implement Raft immediately:** rejected. Leader election does not solve database HA and adds a separate consensus failure domain.

## Consequences

Positive:
- Existing MiniCloud correctness work is reused.
- Heterogeneous devices and AI CLI providers become first-class execution capabilities.
- Planning and scheduling are separated, making placement auditable and testable.
- Provider-specific behavior is isolated behind adapters.

Costs:
- New workflow, node, capability, tool, workspace, artifact, and approval models are required.
- Local AI CLI tools have different noninteractive modes, authentication states, permissions, and output formats.
- Remote execution materially increases the security threat model.
- Resource scheduling requires real telemetry and benchmark evidence.
- The API and database will need carefully staged additive evolution.

## Implementation boundary

This ADR approves the direction, not immediate unrestricted execution. Each slice in the merged roadmap needs an owner-approved task and, where the roadmap says so, its own ADR or ADR amendment (for example the V2-0 concurrency ADR and the V2-1 node-model ADR that answers OD-19). First deliver the design, threat model, API contracts, data model, and test plan. Do not start a production implementation until the owner approves the concrete implementation task and its owned files.
