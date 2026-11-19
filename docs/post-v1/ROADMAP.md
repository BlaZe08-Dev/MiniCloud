# MiniCloud Post-V1 Roadmap

Status: designed, not executed. V2-0 and V2-0b are merged; every row after V2-0b is planned; none is implemented.

## Baseline

`v1.0.0` is released (tag on 166c719, 2026-11-15; code baseline d7a397c). V1 is frozen as the stable distributed job-execution baseline. New functionality is additive and must preserve existing job/attempt semantics.

This roadmap replaces the pack's original Gate 1 to Gate 9 sequence with the merged plan of the post-V1 integration spec (section 4, owner decision D1). Version boundaries other than the V4 addition are unchanged (ADR-0004, amended by ADR-0018). Slice names are proposals for the task board (`docs/agents/TASKS.md`).

## Merged roadmap

| Version | Slice | Scope | Exit criterion |
|---|---|---|---|
| V1.0.x | maintenance | the v1.0.1 list (minors F-5a-14/16/17, `GetWorker` not reading `last_poll_at`, in-memory F05/F06 mutation gap, `logsafe` in `cmd/migrate`, repo cleanup) | no behaviour change beyond the listed fixes |
| Gate 0 | PV-01 | documentation reconciliation and design freeze | `make test-docs` rc 0; hierarchy and links verified; no code touched |
| **V2** | V2-0 | **Worker concurrency truth.** ADR-0019; worker executes attempts concurrently. Merged in PR #57 and PR #58. | advertised capacity equals real concurrency; race tests; V1 worker protocol still compatible |
| V2 | V2-0b | **Round-robin uses every free slot.** ADR-0020; migration `000006`. Merged in PR #63 and PR #64. | under `round_robin` a worker with free slots is reserved work up to its capacity; no reservation beyond it; ADR-0017 invariants still pass |
| V2 | V2-1 | **Node and capability model (D3).** Additive forward-only migration `000007`+ (`000006` is taken by V2-0b, ADR-0020) with separate tables: `nodes` (identity only), `node_hardware` (versioned static inventory: OS, arch, CPU, RAM total, disk, GPU optional), `node_telemetry` (latest dynamic sample with `sampled_at`: CPU load, free RAM, free disk), and nullable `workers.node_id`. The `node_tools` table is designed in the V2-1 ADR but created in V4 (no empty unused table). Absent data means "unknown"; legacy workers (NULL node) stay eligible under the existing policies. Telemetry updates must not rewrite the hardware row. | stale telemetry is never treated as current; V1 workers register unchanged; hardware rows are not rewritten by telemetry |
| V2 | V2-2 | Workload generator and benchmark harness (built-in tasks only, controlled hardware, no invented numbers). Proposed: also record median-of-N speed per task type per worker, the input of the proposed V2-3 score ([`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md) section "Placement algorithm (proposed)"). | repeatable runs, recorded environment |
| V2 | V2-3 | **Resource-aware policy** plugged into the ADR-0017 assignment pass: hard constraints first, deterministic ranking, decision audit record. Proposed design (filter, score, round-robin tie-break; EMA with hysteresis; TTL): [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md) section "Placement algorithm (proposed)", questions OD-21 to OD-25. Compared with `round_robin` and `fifo_pull` on p50/p95/p99, queue wait, throughput, fairness. | placement reproducible from recorded inputs; resource limits never violated in tests |
| V2 | V2-4 | Redis Streams versus the PostgreSQL baseline (ADR-0006) with broker-restart, network-drop and duplicate-delivery tests. PostgreSQL stays the source of truth. | benchmark report with real measurements; ADR recorded |
| V2 | gate | V2 security and fault gate | per `docs/security/test-plan.md` |
| **V3** | V3-1 | Docker executor with CPU, memory, PID and runtime limits (ADR-0005) | escape and limit tests |
| V3 | V3-2 | **Authentication**: API clients, node enrollment and identity, authorization scopes, transport security (mTLS candidate), audit events. Closes SEC-004. | an unauthenticated device cannot enroll, submit, inspect or execute |
| V3 | V3-3 | **Node Agent evolution**: extend the worker runtime (no second daemon), keep the V1 worker protocol compatible | old and new workers coexist |
| V3 | V3-4 | Multi-machine networking and network fault tests. Remote execution stays disabled until V3-1 and V3-2 pass. | partitions and reconnects tested |
| V3 | V3-5 | Prometheus metrics, operational docs, reproducible release from a clean checkout | clean-checkout setup from the README |
| V3 | gate | V3 isolation and authentication gate | per ADR-0012 |
| **V4 (new)** | V4-0 | Design freeze: resolve the open decisions that block V4-1 (see [`OPEN_DECISIONS.md`](OPEN_DECISIONS.md)), contracts, threat model, test plan | no open decision blocks V4-1 |
| V4 | V4-1 | **Workflow model, workspaces and artifacts** (single machine). Workflow parent plus task DAG, each runnable task mapped onto the existing job/attempt lifecycle; isolated per-attempt workspaces from immutable snapshots; local content-addressed artifact store with PostgreSQL metadata. Additive forward-only migration, lock-order analysis, ADR amendment. | no shared-directory mutation; stale artifact never accepted |
| V4 | V4-2 | **CLI adapter contract and one provider adapter** (provider chosen in V4-0). Single machine, restricted account (D2), read-only profile first, writes with approval, direct executable plus argument array, environment allowlist, process-tree cancellation, bounded logs. | adapter contract tests, no credential leakage, no shell strings |
| V4 | V4-3 | **Tool-aware placement**: create `node_tools` (adapter ID, CLI version, readiness enum, last probe time; never credentials) and add tool readiness and execution profile as hard constraints to the V2-3 policy | placement decisions explain themselves from recorded inputs |
| V4 | V4-4 | **Intelligence Dispatcher**: planner interface (provider-neutral), schema-validated DAG, plan preview and approval, replanning as a new immutable revision | untrusted planner output cannot bypass validation, auth, scheduler constraints or approval |
| V4 | V4-5 | Interfaces: additive `/v1` workflow and node endpoints, `mc workflow` commands, Go SDK, simple chat UI, explicit external-agent ingress (HTTP/CLI/SDK first, MCP optional) | all four interfaces share one backend and one authorization model |
| V4 | V4-6 | Multi-device AI execution demo (requires V3 gates), artifact transfer with checksum verification, node-loss and stale-result tests | demo survives a node loss and rejects stale results |
| V4 | gate | V4 security gate: prompt injection, malicious CLI output, workspace escape, replayed enrollment, partial side effects on retry | per the threat list in [`SECURITY.md`](SECURITY.md) |
| Later | HA | Coordinator failover (Raft or database leases with fencing) only after need is shown and database HA is designed with it | split-brain tests |

## How the pack's original gates map

Gate 0 = PV-01; Gate 1 = V3-2; Gate 2 = V2-1 (plus the `tools` part in V4-3); Gate 3 = V4-2; Gate 4 = V4-1; Gate 5 = V2-3 and V4-3; Gate 6 = V4-4; Gate 7 = V4-5 and V4-6; Gate 8 = V3-1, V3-5 and V2-4; Gate 9 = Later.

## Where this differs from the pack, and why

- The scheduler (pack Gate 5) moves before the adapters, because resource-aware scheduling is already a V2 deliverable and does not need AI tools.
- Isolation and authentication stay in V3, before remote execution, which matches the pack's own prerequisite. The pack's later position for Docker (its Gate 8) is not used.
- Redis evaluation and the benchmark harness stay in V2, not in the pack's late gate.
- The node model (pack Gate 2) comes before authentication (pack Gate 1) because V2 scheduling needs it. Node identity before authentication is therefore an open decision (OD-19) to answer in the V2-1 ADR; V3-2 later binds that identity to an authenticated enrollment.
- V2 starts with V2-0 (worker concurrency truth), which the pack did not list, because a resource-aware scheduler and a fair benchmark both need real concurrency semantics.

## Sequencing rule

Each slice needs an owner-approved task entry and, where listed, an ADR before code. Authentication and isolation (V3-1, V3-2) are prerequisites for remote execution and for any untrusted workload; AI CLI work on the host follows the trust model D2 in ADR-0018.
