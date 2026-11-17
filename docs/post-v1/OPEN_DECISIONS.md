# Post-V1 Open Architecture Decisions

These questions are intentionally not guessed. Resolve them before the implementation slice that depends on them. The "Needed before" column uses the merged slice names of [`ROADMAP.md`](ROADMAP.md).

Already decided by the owner (2026-11-15, recorded in ADR-0018): D1 version placement, D2 trust model for AI CLI tasks (narrows OD-13 for the owner-only case), D3 device model (frames OD-07, OD-08, OD-09).

## By slice

| Needed before | Decisions |
|---|---|
| V2-1 | OD-08 telemetry libraries and platform support (Linux first is acceptable), OD-09 reservation model (node-level resources, worker-level slots), OD-19 node identity before V3 authentication, OD-23 time-to-live values (telemetry part) |
| V2-3 | OD-21 score function and tie-break, OD-22 smoothing and hysteresis parameters (after V2-2), OD-23 time-to-live values, OD-24 job constraint fields |
| V3-2 / V3-3 | OD-06 node and API authentication, OD-07 Node Agent deployment model, OD-20 workers per node and agent-process model, OD-13 sandbox (D2 covers the owner-only case), OD-25 first non-Linux target |
| V4-0 / V4-1 | OD-10 workflow-to-job mapping, OD-11 artifact store, OD-12 workspace isolation |
| V4-2 | OD-04 first provider, OD-05 verified CLI versions and invocation modes, OD-14 approval policy |
| V4-4 | OD-02 planner model location, OD-03 planner credential ownership |
| V4-5 | OD-01 primary-agent ingress, OD-15 UI technology, OD-16 SDK language |
| Later | OD-17 coordinator failover; OD-18 Redis is covered by V2-4 |

## Decision table

| ID | Decision | Recommendation / constraint | Needed before |
|---|---|---|---|
| OD-01 | How does the user's primary AI agent submit work? | Start with stable HTTP API and `mc` CLI. Consider MCP as an optional adapter after verifying support in the chosen primary agent. Never assume process interception. | V4-5 |
| OD-02 | Where does the Intelligence Dispatcher LLM run? | Provider-neutral interface; allow a hosted model or a local model later. Keep credentials out of domain/job records. | V4-4 |
| OD-03 | Who pays for/owns planner model credentials? | Explicit owner-level configuration and quota policy; do not silently reuse a user's local CLI credentials. | V4-4 |
| OD-04 | First supported AI CLI adapter? | Choose one provider for the first end-to-end vertical slice, then add the others behind the same interface. | V4-0 (used by V4-2) |
| OD-05 | Exact CLI versions and invocation modes? | Verify official docs and installed binary behavior per adapter/version. Do not guess flags. | V4-2 |
| OD-06 | Authentication for remote nodes? | mTLS or another reviewed authenticated node identity mechanism, plus API client authorization. V1 unauthenticated endpoints are not acceptable remotely. | V3-2 (before any remote execution) |
| OD-07 | Node Agent deployment model? | Evolve/extend the worker runtime while preserving V1 worker compatibility; avoid two daemons with conflicting responsibilities. | V3-3 |
| OD-08 | Hardware telemetry libraries and platform support? | Define Linux-first support if necessary, but design versioned cross-platform capability contracts. | V2-1 |
| OD-09 | Resource reservation model? | Hard constraints and reservation accounting in deterministic scheduler; do not use stale telemetry as a hard guarantee. | V2-1 (used by V2-3) |
| OD-10 | Workflow-to-job relational model? | Prefer workflow parent + task DAG with each runnable task mapped to existing job/attempt lifecycle. Validate schema and lock-order implications before migration. | V4-1 |
| OD-11 | Artifact store for initial deployment? | Local filesystem content-addressed store with PostgreSQL metadata is a reasonable first step; keep an object-store interface. | V4-1 |
| OD-12 | Workspace isolation mechanism? | Git worktrees for Git repos, provider abstraction for other workspaces; do not share mutable working directories. | V4-1 |
| OD-13 | Execution sandbox? | Least-privilege OS user plus explicit workspace boundary; container isolation before accepting untrusted arbitrary project execution. | V3-2 / V3-3 (D2 covers the owner-only case) |
| OD-14 | Approval policy? | Default to approval for writes, scripts, external network actions, and protected-branch changes. | V4-2 |
| OD-15 | UI technology? | Keep it a small API client. Choose stack only when the UI task is approved; no IDE features. | V4-5 |
| OD-16 | SDK language? | Go SDK first is the natural default; add other languages only with concrete need. | V4-5 |
| OD-17 | Coordinator failover? | Defer. If needed, evaluate Raft or database-backed leader leases with fencing. Design PostgreSQL HA separately. | Later |
| OD-18 | Redis Streams? | Preserve the existing V2 evaluation. Do not introduce Redis as source of truth or before a measured need. | V2-4 |
| OD-19 | Node identity before V3 authentication | How a stable node ID is chosen and kept (for example a configured ID or a persisted generated ID). V3-2 later binds it to an authenticated enrollment. | V2-1 |
| OD-20 | Workers per node | How many worker processes one node runs, and whether one agent process hosts several worker identities. | V3-3 |
| OD-21 | Placement score function and tie-break (proposed filter-and-score, [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md)) | Options if known: weighted free capacity and measured speed; speed-only; capacity-only. Tie-break is proposed as ADR-0017 round-robin order. Not answered. Owner: owner. | V2-3 |
| OD-22 | Smoothing parameters, hysteresis thresholds and N for median-of-N benchmarks | To be set from V2-2 measurements, not guessed. Options: exponential moving average factor and band widths. Not answered. Owner: owner. | V2-3 (after V2-2) |
| OD-23 | Time-to-live values for capability and telemetry records | No options yet; derive from the measured heartbeat and sample interval. Not answered. Owner: owner. | V2-1 (telemetry) / V2-3 |
| OD-24 | Job constraint field names and syntax (additive `/v1`) | Options: structured optional fields on job submit; label selectors. Must be additive (ADR-0004). Not answered. Owner: owner. | V2-3 |
| OD-25 | First non-Linux target, and when it is built and tested | Options: Linux arm64, macOS, Windows. Needs its own build, tests and approved task. Not answered. Owner: owner. | V3-3 |

## Approval rule

A recommendation in this table is not an implementation decision. Record the owner's answer in a numbered ADR amendment or a new ADR before changing code.
