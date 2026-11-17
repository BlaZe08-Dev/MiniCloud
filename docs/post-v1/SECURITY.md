# Post-V1 Security and Trust Model

Status: designed, not executed. Gates: V3-1 (isolation), V3-2 (authentication, closes SEC-004), V4 security gate. Remote execution stays disabled until V3-1 and V3-2 pass (V3-4).

## Security posture

V1.0.0 explicitly accepts unauthenticated HTTP endpoints (SEC-004) and defaults the coordinator to localhost. That is not an acceptable configuration for a multi-device remote-execution cluster.

**Do not connect remote nodes or expose the V1 coordinator to an untrusted network.** Authentication, authorization, secure node enrollment, and transport protection are prerequisites for remote execution.

## Trust boundaries

1. User/primary AI agent to MiniCloud ingress.
2. Planner/model provider to workflow orchestrator.
3. Coordinator to Node Agent.
4. Node Agent to local CLI executable.
5. CLI executable to workspace, filesystem, network, and local credentials.
6. Node to node artifact transfer.
7. Workflow/task result to artifact integration and protected repository.

Natural-language prompts, LLM output, CLI output, filenames, patches, and artifacts are all untrusted data.

## Required controls before remote execution

- authenticated API clients
- node identity and authenticated enrollment
- worker/node authentication and TLS (mTLS is a candidate)
- authorization scopes for submit, inspect, cancel, approve, enroll, and administer
- replay protection and request limits
- leader/epoch fencing if multiple coordinators are ever supported
- audit events for approvals, tool launches, cancellations, artifact commits, and administrative changes

The precise auth design is an open decision. Do not assume the V1 unauthenticated endpoints become safe merely because devices are on the same LAN.

## Trust model for AI CLI tasks (owner decision D2)

Exception, owner decision D2 (ADR-0018): from V4-2, after the V3-1 isolation gate, a registered AI CLI execution kind may run on the host for the owner's own projects only, under a dedicated restricted OS account, in a per-task workspace, read-only profile first, file changes needing approval; any other user's or untrusted workload still requires Docker isolation.

The owner approved this as an explicit exception, recorded in `AGENTS.md` invariant 7 and hard rule 3 in the same words.

## Standing rules (also in `AGENTS.md`)

- No LLM-generated shell strings: adapters run a registered executable with an argument array.
- Provider credentials never leave their node.
- One mutable workspace per attempt; no two attempts share a mutable working directory.

## Local process execution controls

- No LLM-generated shell string execution.
- Invoke registered executables directly with argument arrays.
- Restrict working directory and filesystem access to the task workspace.
- Run the Node Agent and CLI under a least-privilege OS account.
- Use explicit environment allowlists.
- Never export local CLI credentials to the coordinator or another node.
- Use explicit execution profiles and approval modes.
- Default to deny for unsupported adapters, unknown flags, missing authentication, or unrecognized capabilities.
- Bound CPU, memory, runtime, disk, output, process count, and concurrent slots.
- Kill the full child-process tree on cancellation/lease loss where platform APIs allow.
- Keep network access restricted according to task profile.
- Treat an AI CLI's own sandbox/approval flags as an additional layer, not the only boundary.

## Approval policy

Human approval should be required at minimum for:
- first-time enrollment of a node/tool
- execution profiles with write access
- arbitrary project scripts or build/test commands not in an approved profile
- network access outside approved destinations
- actions that modify protected branches or external systems
- retries that may repeat non-idempotent side effects

Read-only analysis can have a less restrictive policy once identity and workspace controls are proven.

## Secret handling

- Provider credentials stay local to the node and the provider CLI.
- Store only readiness status and last probe metadata centrally.
- Never log raw tokens, API keys, credential files, environment dumps, or full command lines if they contain secrets.
- Redact process output before persistence.
- Do not put secrets in workflow manifests or task prompts.

## Threats to test

- forged node identity and replayed registration
- unauthorized task submission and cancellation
- malicious prompt requesting filesystem escape or credential exfiltration
- malicious adapter output and terminal escape sequences
- executable path replacement/PATH hijacking
- workspace symlink/path traversal
- archive bomb and oversized artifact
- stale/duplicate task result
- worker kill during CLI execution
- coordinator outage during artifact upload
- task retry after partial external side effect
- node resource exhaustion and process-tree leakage
- split-brain coordinator and stale leader writes

## AI noise controls (proposed)

Status: the table mixes labels on purpose. [Decided] rows cite their source; [Proposed] rows need an ADR and owner approval. Nothing here is implemented. Recorded by PV-02 (owner question of 2026-11-17). Pipeline: [`INTELLIGENCE_DISPATCHER.md`](INTELLIGENCE_DISPATCHER.md) section "Validation pipeline (proposed)".

| Problem | Control | Label |
|---|---|---|
| Malformed model output | Strict schema validation, bounded repair retries, then a clear failure | [Proposed] |
| Invented tasks or tools | Fixed allowlist; no model-written shell strings | [Decided, ADR-0018] |
| Cyclic or dangling plan | Acyclicity check by topological sort before anything runs | [Proposed] |
| Prompt injection from repo or file content | Model output is untrusted advisory data; the deterministic scheduler decides; writes need approval | [Decided, ADR-0018 and D2] |
| Noisy tool output | Normalize and truncate. Today `mc` strips terminal escapes from worker-supplied text (SEC-048, `docs/security/findings.md`, fixed in P4-07) and task output is capped at 64 KiB (`AGENTS.md` invariant 4). Applying the same normalization to AI CLI output is not built. | [Decided] for the SEC-048 `mc` sanitization and the 64 KiB cap; [Proposed] for AI CLI output |
| Jumping CPU/RAM readings | Moving average with hysteresis | [Proposed] |
| Stale device data | Time-to-live; expired counts as unknown | [Proposed] |
| Noisy benchmark timings | Median of several runs | [Proposed] |
| Repeated side effects on retry | One mutable workspace per attempt, plus backoff with jitter (worker poll loop `internal/worker/client.go:74`; job retries ADR-0015). Retry can still repeat external side effects; semantics stay at-least-once. | [Decided] (`AGENTS.md` post-V1 standing rules, invariant 5) |
| Cost and run-time variance | Per-task timeouts and budgets enforced by the scheduler | [Proposed] for budgets; the V1 per-job timeout is [Decided] (`AGENTS.md` invariant 4) |

## V1 controls remain

Preserve the existing V1 limits, lease semantics, state-machine validation, database lock ordering, secret redaction, security test workflow, and break-before-fix process. New AI execution must not weaken them.
