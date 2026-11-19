Task: ARCH-REVIEW-1
Branch: agent/gemini/ARCH-REVIEW-1
Work tip (last commit before this report): 9931d89
Date: 2026-11-19

# MiniCloud Independent Devil's Advocate Architecture Review

## 1. Executive Verdict
MiniCloud is a disciplined, well-tested educational prototype exploring distributed job execution primitives (domain FSM, atomic leases, bounded retries, round-robin dispatch) in Go and PostgreSQL. Its code hygiene, mutation proofs, and test-driven rigor are exceptional.

However, as a production distributed execution platform or remote AI agent orchestrator, MiniCloud is in a fragile pre-alpha state. It operates with zero authentication (SEC-004), relies on a single coordinator process that constitutes a single point of failure (SPOF), possesses no dynamic resource awareness (CPU/RAM/GPU), executes tasks in-process on the host without sandboxing, and suffers from database call amplification (~20 SQL flights per work claim, SEC-037). 

The planned post-V1 "AI Dispatch Layer" (V4) introduces severe architectural dissonance: it attempts to layer multi-agent DAG planning, unauthenticated remote tool execution, and shared repository modification on top of a core engine that cannot yet isolate filesystem workspaces, fence split-brain coordinators, or sandbox arbitrary code execution. Recommendation: Freeze all V4 AI dispatch implementation until V2 (resource/telemetry) and V3 (Docker isolation, mTLS, node identity) establish a secure, performant execution substrate.

---

## 2. Critical Architectural Weaknesses Ranked by Severity

### 1. Critical: Unauthenticated Control Plane & Remote Code Execution Vector
- **File/Location**: `internal/api/handlers.go`, `internal/api/server.go`, ADR-0004 §4, `docs/security/findings.md` (SEC-004).
- **Failure Scenario**: Any network entity reaching port 8080 can submit arbitrary jobs, forge worker registrations (`POST /v1/workers/register`), steal assignments, spoof lease renewals, or forge execution results (`POST /v1/jobs/{id}/attempts/{attempt_id}/result`). While mitigated in V1 by a `127.0.0.1` bind default, any multi-machine or remote node deployment immediately turns MiniCloud into an open, unauthenticated remote code execution (RCE) platform.
- **Architectural Correction**: [Proposed, Soon] Implement mutual TLS (mTLS) with client certificates for worker-to-coordinator communication, bearer token authentication for client API operations, and cryptographically verified node identity before multi-machine dispatch.

### 2. Critical: Unsandboxed Execution & Lack of Workspace Isolation
- **File/Location**: `internal/executor/registry.go`, `internal/executor/executor.go`, ADR-0005 §1, ADR-0018 §4, `docs/post-v1/ARCHITECTURE.md` §3 & §11.
- **Failure Scenario**: In V1, only allowlisted in-process tasks run (`echo`, `sleep`, `sha256_loop`). Under owner decision D2 (ADR-0018 §4), host execution of registered AI CLIs is permitted only from V4-2 after the V3-1 Docker isolation gate, for the owner's own projects only, under a dedicated restricted OS account, in an isolated per-task workspace, with a read-only profile first, and requiring approval for file changes; any other or untrusted workload requires Docker isolation. Because `internal/executor` currently has zero workspace abstraction or filesystem isolation, concurrent tasks (enabled via `MINICLOUD_WORKER_SLOTS` in V2-0/V2-0b) would collide on shared directories if run without the per-task workspaces specified in D2.
- **Architectural Correction**: [Proposed, Soon] Maintain the strict sequencing of D2 (gate host CLI execution behind V3-1 isolation, restricted OS user, detached git worktree workspaces, and approval gates).

### 3. High: Coordinator Single Point of Failure (SPOF) & Split-Brain Incoherence
- **File/Location**: `cmd/coordinator/main.go`, `internal/coordinator/service.go` (`Service.mu`), `internal/coordinator/sweeper.go` (`s.startupDeadline`, SEC-028), ADR-0013 §2, ADR-0018 §9.
- **Failure Scenario**: The coordinator has no clustering, leader election, or consensus. If it crashes, all dispatch and renewals halt; surviving workers back off for up to 60s and drop results if downtime exceeds their delivery budget. If an operator launches two coordinator processes against the same database, assignment passes are protected by a coarse advisory lock (`AssignmentPassLockID`, `internal/storage/postgres/assignment.go:134`), but lease sweepers run independently with process-local memory grace states (`SEC-028`), causing desynchronized, premature `LOST` sweeping during outages.
- **Architectural Correction**: [Proposed, Soon] Explicitly document MiniCloud as an active-passive architecture. Persist sweeper grace epochs in PostgreSQL rather than process memory, and utilize PostgreSQL transaction advisory locks to guarantee single-active coordinator leadership.

### 4. Informational: Database Flight Amplification & Global Lock Contention
- **File/Location**: `internal/storage/postgres/assignment.go` (`assignChooseWorker`, `ClaimForPoll`), `internal/coordinator/service.go` (`Service.mu`, SEC-036), `tests/faultinject/deadline_test.go:45` (SEC-037).
- **Failure Scenario**: Claiming a single job under `round_robin` takes ~20 database round trips (SEC-037). `PollWork` and `CommitResult` acquire the global in-process mutex `s.mu` (`internal/coordinator/service.go:417, 466`) across database calls (SEC-036), and `AssignmentPassLockID` is a database-wide advisory lock (`internal/storage/postgres/assignment.go:134`). However, contention is bounded by the 8 s request deadline (`requestDeadlineMiddleware`), and `TestFault_DBOutage_HalfOpen_OtherRequestsNotStalled` (`tests/faultinject/deadline_test.go:121`) verifies that hung database calls do not stall unrelated requests indefinitely. Furthermore, the V2-0b audit measured an 8-claim burst completing in at most 329 ms under local conditions (`docs/agents/reviews/V2-0b.md`). V2-2 (the comparative benchmark generator) is the planned place to measure whether this flight amplification imposes a real throughput bottleneck.
- **Architectural Correction**: [Proposed, Later] Consolidate multi-query poll claims into a single atomic PostgreSQL CTE or stored transaction (reducing network flights to <= 2), and measure under the V2-2 benchmark harness before altering locking semantics.

### 5. Medium: Resource-Blind Dispatch (CPU/RAM/GPU/Disk Sched Blindness)
- **File/Location**: `internal/domain/worker.go` (`domain.Worker`), `internal/storage/postgres/assignment.go` (`assignChooseWorker`), ADR-0020 §2.1.
- **Failure Scenario**: Dispatch decisions consider only slot counts (`A + R < S`). The coordinator has zero awareness of host CPU saturation, memory exhaustion, disk space, or GPU availability. A worker with free slots on a machine under 100% CPU or 98% RAM load will continue to be assigned tasks, triggering out-of-memory (OOM) kills, thrashing, and cascading lease timeouts.
- **Architectural Correction**: [Proposed, Soon] Implement V2-1/V2-3 telemetry reporting (`node_telemetry` table) with configurable resource thresholds (max CPU %, min free RAM/disk) that gate slot eligibility in `assignChooseWorker`.

### 6. Medium: At-Least-Once Re-Execution Hazards on Zombie Workers
- **File/Location**: `internal/worker/client.go` (`renewLoop`, `ExecuteAndReport`, `deliverResult`), `internal/storage/postgres/repository.go` (`CommitAttempt`), ADR-0014 §2.
- **Failure Scenario**: When network partition isolates a worker beyond the 30s lease duration, the coordinator sweeps the attempt to `LOST` and re-dispatches the job to another worker. On lease loss, the worker client cancels the running task context (`internal/worker/client.go` `renewLoop` and `ExecuteAndReport` `cancelTask`) and drops the result. However, if an external side effect was already performed prior to cancellation, or if an external process fails to handle context cancellation gracefully, duplicate side effects can occur when the rescheduled attempt runs on a second worker under at-least-once semantics. Forcefully terminating or issuing SIGKILL to an external child process tree is a V4 CLI adapter concern.
- **Architectural Correction**: [Proposed, Later] Design workflow tasks with external idempotency barriers, and ensure V4 CLI adapters supervise child process trees and issue SIGKILL if an external process fails to exit upon context cancellation.

---

## 3. Implemented vs. Planned Feature Assessment

| Feature Area | Implementation Status | Evidence / Repository Location | Architectural Reality |
|---|---|---|---|
| **Domain FSM** | Implemented | `internal/domain/transition.go`, `domain/state.go` | Central entity lifecycle validation for jobs and attempts; strictly enforced by transition matrix. |
| **Durable Storage** | Implemented | `internal/storage/postgres/` (migrations 000001–000006) | PostgreSQL 16 durable store; transactional DDL, row-level locking (`SKIP LOCKED`), advisory locks. |
| **Task Execution** | Implemented | `internal/executor/registry.go` | Strictly in-process allowlisted tasks (`echo`, `sleep`, `sha256_loop`). Host CLI/Docker is NOT implemented. |
| **Dispatch Policies** | Implemented | `internal/storage/postgres/assignment.go`, `repository.go` | `round_robin` (default) with accept windows and fallback arms; `fifo_pull` selectable. |
| **Leases & Retries** | Implemented | `internal/coordinator/sweeper.go`, `internal/worker/client.go` | 30s leases, 10s renewal loop, sweeper with startup/outage grace, jittered exponential backoff. |
| **Worker Concurrency**| Implemented | `internal/worker/client.go`, ADR-0019, ADR-0020 | Worker executes up to `MINICLOUD_WORKER_SLOTS` concurrently; real slot reservations in round-robin. |
| **Node Model (V2-1)**| Planned | `docs/post-v1/NODE_CAPABILITIES.md`, `ROADMAP.md` | `nodes`, `node_hardware`, `node_telemetry` tables; migration 000007. No code in repo. |
| **Resource Placement**| Planned | `docs/post-v1/NODE_CAPABILITIES.md` (V2-3) | CPU/RAM/GPU-aware scheduling filters. No code in repo. |
| **Redis Dispatch** | Planned | ADR-0006, `docs/roadmap.md` (V2-4) | Redis Streams transport comparison. No code in repo. |
| **Docker Sandboxing** | Planned | ADR-0005, `docs/roadmap.md` (V3-1) | Containerized executor isolation. No code in repo. |
| **Node Identity/mTLS**| Planned | `docs/post-v1/SECURITY.md`, `ROADMAP.md` (V3-2) | Mutual TLS, cryptographic identity, worker tokens. No code in repo. |
| **Coordinator HA** | Planned | `docs/post-v1/ARCHITECTURE.md` §9 | Leader election, consensus, failover. No code in repo. Single process today. |
| **AI Dispatch Layer** | Planned | `docs/post-v1/` (WORKFLOW_MODEL, CLI_ADAPTERS, V4) | DAG orchestrator, CLI adapters, workspace service, LLM dispatcher. No code in repo. |

---

## 4. Corrected Architecture

The corrected architecture enforces strict physical and logical boundaries across three decoupled layers:

```text
+---------------------------------------------------------------------------------+
|                        AI Planning & Ingress Layer (Untrusted)                  |
|  - CLI / SDK / Chat Ingress (Authenticated)                                     |
|  - Intelligence Dispatcher: Decomposes prompt into declarative task graph (DAG) |
|  - Emits immutable workflow manifest with typed validation assertions           |
+---------------------------------------------------------------------------------+
                                       |
                                       v JSON DAG Spec
+---------------------------------------------------------------------------------+
|                      MiniCloud Execution Core (Authoritative)                   |
|  - Coordinator: Validates FSM transitions, orchestrates dependency barriers     |
|  - Deterministic Placement Engine:                                              |
|      1. Hard constraint filter (task allowlist, OS/arch, tool availability)     |
|      2. Dynamic telemetry filter (node CPU < 85%, available RAM > requirement)  |
|      3. Deterministic round-robin / fair-share assignment                       |
|  - Single-Transaction Dispatch: Atomic CTE claim (<= 2 network flights)          |
|  - PostgreSQL: Durable source of truth (jobs, attempts, node telemetry, leases)|
|  - Lease Sweeper: Epoch-based grace tracking synchronized via DB timestamps     |
+---------------------------------------------------------------------------------+
                                       |
                                       v Pull / mTLS Heartbeat
+---------------------------------------------------------------------------------+
|                       Physical Node Agent Layer (Sandboxed)                     |
|  - NodeAgent Daemon: Collects host telemetry (/proc/pressure, memory, CPU)      |
|  - Ephemeral Workspace Service: Dedicated git worktree per attempt              |
|      (git worktree add --detach; auto-cleanup on termination)                    |
|  - Sandboxed Runner: Docker container or unprivileged OS user (minicloud-exec)  |
|  - Process Supervisor: Cancels task context on lease loss; process-tree         |
|      SIGKILL supervision is a V4 adapter concern                                 |
|  - Automated Validation Step: Executes verification suite before commit (exit 0)|
+---------------------------------------------------------------------------------+
```

### Architectural Principles:
1. **Decoupled Planning**: The Intelligence Dispatcher is an unprivileged client of MiniCloud. It proposes DAGs; the coordinator's deterministic scheduler validates and commits them. The LLM never writes to durable state directly.
2. **Deterministic Placement**: Scheduling filters on live telemetry (`node_telemetry`) before assigning tasks to a node, avoiding overload.
3. **Fail-Safe Execution Sandboxing**: Task execution must occur in isolated containers or detached git worktrees. Leases lost due to network disconnection cancel the running task context.
4. **Automated Verification**: AI-generated code must pass an automated integration test / lint suite before the attempt is accepted as `COMPLETED`.

---

## 5. Security Recommendations (Minimum Production Baseline)

Before MiniCloud can be deployed in any multi-user, multi-machine, or production environment, the following baseline is mandatory:

1. **Mutual TLS & Node Authentication (Eliminate SEC-004)** [Proposed, Soon]
   - Require TLS 1.3 with mutual certificate verification (mTLS) for all worker-to-coordinator interactions.
   - Enforce cryptographic node identity: workers authenticate using node certificates signed by a private cluster CA; unauthenticated HTTP endpoints must be rejected.
2. **Containerized Sandboxing & Process Isolation** [Proposed, Soon]
   - Isolate all execution within rootless Docker/Podman containers or Linux cgroups/seccomp profiles (V3-1).
   - Drop all Linux capabilities, set read-only root filesystems, enforce non-root UID, and bound memory and CPU allocations per container.
   - Prohibit running AI CLIs directly on the host under normal user accounts.
3. **Workspace Virtualization & Git Worktree Isolation** [Proposed, Soon]
   - Every execution modifying files must operate inside an isolated temporary git worktree (`git worktree add --detach`) created by the NodeAgent.
   - Changes are exported strictly as patch artifacts. Merging to shared branches requires automated conflict detection and approval.
4. **Node-Local Credential Confinement** [Proposed, Now]
   - Third-party AI model provider credentials (API keys, OAuth tokens) must remain exclusively on the node where the CLI is installed.
   - Never transmit credentials through the coordinator, database, job payloads, or event logs.
5. **API Authentication, RBAC, and Rate Limiting (SEC-047, SEC-063)** [Proposed, Soon]
   - Protect client APIs (`cmd/mc`) with scoped bearer tokens (`submitter`, `operator`, `auditor`).
   - Implement token-bucket rate limiting on `POST /v1/jobs` and `POST /v1/workers/register` to prevent database pool exhaustion and denial-of-service.

---

## 6. Competitive Differentiation Assessment

| Platform | What It Solves | Overlap with MiniCloud | MiniCloud Differentiation | Why Choose MiniCloud? |
|---|---|---|---|---|
| **HashiCorp Nomad** | Production container, microservice & batch scheduling across data centers via Raft clustering. | Job scheduling, dispatch queue, worker health tracking. | Nomad is a general-purpose orchestrator; MiniCloud is a lightweight, embedded Go engine focused on local CLI tools and fine-grained attempt leases. | MiniCloud avoids heavy cluster infrastructure when orchestrating localized developer machines and AI CLIs. |
| **Kubernetes** | Massive enterprise distributed OS for containerized microservices and pod networking. | Remote task execution and resource allocation. | K8s is vastly complex with heavy container startup latency; MiniCloud uses single-binary Go agents with direct SQL durability. | Simpler operational model for developers needing direct task dispatch without etcd or overlay networking. |
| **Ray** | Distributed Python computing, dynamic tensor sharing, and distributed ML/actor pipelines. | Distributed compute task distribution. | Ray specializes in high-throughput tensor/memory streaming; MiniCloud focuses on developer tasks, CLI workflows, and durability. | Lightweight coordination across heterogeneous developer laptops rather than monolithic GPU compute clusters. |
| **Temporal** | Fault-tolerant, durable workflow orchestration with event-sourced replaying. | State machine lifecycle, leases, retries, and cancellation. | Temporal requires separate backend clusters (Cassandra/Elasticsearch) and complex deterministic workflow code; MiniCloud is simple SQL state. | Developers who need transparent SQL state inspection and straightforward job execution without event-sourcing runtime constraints. |
| **Microsoft Agent Framework** | LLM agent conversations, multi-agent chat loops, and prompt chaining. | Multi-agent task planning and tool selection. | (Unverified claim) Agent frameworks focus on prompt chaining and memory, whereas MiniCloud provides a durable distributed systems task execution substrate. | Provides a durable systems substrate (unverified claim whether developers need both combined in this topology). |

**Is differentiation strong enough?**
Yes, but ONLY IF MiniCloud stays focused on its core niche: a lightweight, durable task execution substrate for heterogeneous developer machines and local AI CLIs. If MiniCloud tries to become a general-purpose container orchestrator (like Nomad/K8s) or an event-sourced workflow engine (like Temporal), it will fail under the weight of solved problems.

---

## 7. Five Highest-Priority Engineering Tasks & Failure Scenarios

### Five Highest-Priority Engineering Tasks (Ranked)
1. **Task 1: V3-2 Mutual TLS & Worker Authentication (Eliminate SEC-004)** [Proposed, Soon]
   - Implement TLS 1.3 with client certificates on all coordinator endpoints; cryptographically bind attempt claims to authenticated worker identities.
2. **Task 2: V3-1 Containerized Execution Sandboxing (ADR-0005)** [Proposed, Soon]
   - Replace in-process execution with rootless Docker container isolation, enforcing CPU/RAM cgroups and read-only filesystems.
3. **Task 3: Measure Dispatch SQL Flight Overhead under V2-2 Benchmark Generator (SEC-036, SEC-037)** [Proposed, Later]
   - Measure real flight overhead under the V2-2 benchmark harness; if needed, collapse multi-query poll claims into a single atomic SQL transaction.
4. **Task 4: Dynamic Resource Awareness (V2-1 & V2-3 Node Telemetry)** [Proposed, Soon]
   - Implement `node_telemetry` table and collection; gate slot eligibility in `assignChooseWorker` by CPU (<85%) and free RAM thresholds.
5. **Task 5: Ephemeral Git Worktree Virtualization for AI Workflows (V4-1/V4-2)** [Proposed, Later]
   - Implement `internal/workspace` providing isolated `git worktree add --detach` directories per attempt with automated verification prior to commit.

### The 10 Mandatory Failure Scenarios: Today's Reality vs. Architectural Gap

| # | Failure Scenario | What Happens TODAY in the Code | Architectural Gap |
|---|---|---|---|
| 1 | **Coordinator crashes during active execution** | Surviving workers retry delivery for 60s (`internal/worker/client.go:deliverResult`); restarted coordinator honors 30s startup grace (`internal/coordinator/sweeper.go`), committing without loss (`tests/integration/postgres_wiring_test.go:167`). | Downtime > 60s causes workers to drop results; sweeper then sweeps attempt to `LOST`, burning retry budget. |
| 2 | **Two devices believe they are coordinator** | Both connect to same DB; `AssignmentPassLockID` advisory lock serializes assignment passes (`internal/storage/postgres/assignment.go:134`). | Sweeper grace states are in-process memory (`internal/coordinator/sweeper.go`, SEC-028); independent sweepers desynchronize grace and cause premature `LOST` sweeping. |
| 3 | **Worker completes a job but crashes before acknowledging** | Attempt remains `RUNNING` until lease expires; sweeper marks attempt `LOST` and reschedules `RETRYING` (`internal/storage/postgres/leases.go:SweepExpiredLeases`). | At-least-once duplicate execution; any external side effects already performed are re-executed by a second worker. |
| 4 | **Worker disconnects during a task** | Lease renewal fails; worker cancels the task context (`internal/worker/client.go` `renewLoop` and `ExecuteAndReport` `cancelTask`) and suppresses result delivery; sweeper marks attempt `LOST` after lease + grace (`internal/storage/postgres/leases.go`). | Tasks honoring context abort cleanly; terminating an uncooperative external process tree with SIGKILL is a V4 CLI adapter concern. |
| 5 | **Resource availability changes during scheduling** | Coordinator assigns work strictly on slot counts (`A + R < S`, `internal/storage/postgres/assignment.go:assignChooseWorker`). | Zero visibility into CPU/RAM/GPU load; a saturated host with free slots still receives jobs, causing OOM kills. |
| 6 | **Two AI agents modify shared project files** | Only allowlisted in-memory tasks run (`internal/executor/registry.go`). | No filesystem workspace isolation exists; concurrent multi-slot tasks touching the repo collide, race, and corrupt git state. |
| 7 | **An AI CLI has expired authentication** | no evidence in repo | Process exits non-zero; coordinator treats failure as `UNKNOWN` or `TRANSIENT`, blindly burning attempts in a retry loop. |
| 8 | **Malicious task attempts unauthorized filesystem access** | Only allowlisted tasks run (`echo`, `sleep`, `sha256_loop` in `internal/executor/registry.go`); arbitrary tasks rejected at API boundary. | If host CLI execution is enabled without a sandbox, tasks run with full OS user permissions, exposing private SSH keys and files. |
| 9 | **Network partition separates coordinator and workers** | Coordinator reaps workers to `UNHEALTHY` (`internal/storage/postgres/repository.go:ReapStaleWorkers`); attempts swept to `LOST`; workers re-register on reconnect (`internal/worker/client.go:Run`). | In-flight tasks on partitioned workers are re-dispatched to others, causing simultaneous concurrent duplicate execution. |
| 10 | **AI-generated output passes execution but fails integration tests** | Success is decided strictly by `domain.IsFailedOutcome` (`internal/domain/failure.go:7`; `ExitCode == 0` and empty `Error`) evaluated in `CommitAttempt` (`internal/storage/postgres/repository.go:1179`). | No post-execution validation stage exists; syntactically broken or regressive code is accepted and committed as `SUCCEEDED`. |

---

## 8. Questions Requiring Clarification from the Project Owner

1. **Host Execution vs. Sandboxing (Owner Decision D2 - Already Decided)**: ADR-0018 §4 (D2) has already decided the exact gating: host execution of registered AI CLIs is deferred to V4-2 (after the V3-1 Docker isolation gate passes), strictly restricted to the owner's own projects under a dedicated OS account in an isolated per-task workspace (read-only profile first, approval required for file changes), with all untrusted workloads requiring Docker isolation. (Marked: Decided by owner in ADR-0018; no open question remains).
2. **Coordinator Availability Model (Active-Passive vs. Active-Active)**: Is MiniCloud intended to remain a single-coordinator active-passive service backed by a managed PostgreSQL instance, or is active-active clustering with distributed consensus (Raft) genuinely required? If active-passive is sufficient, can we explicitly remove speculative multi-coordinator complexity?
3. **Database Network Flight Overhead (SEC-037)**: Given that a work claim currently requires ~20 database round trips and holds a global service mutex, what is the target cluster scale (number of worker nodes and jobs/sec) before Redis Streams dispatch (V2-4, ADR-0006) is evaluated?
4. **Workspace Isolation Model for AI Tasks**: For V4 AI workflows, will MiniCloud mandate ephemeral git worktrees per attempt (`git worktree add --detach`), or is there an expectation to support non-git file directories? How will multi-agent branch merging and conflict resolution be handled?
5. **Worker Process Model (Single Daemon vs. Process-per-Attempt)**: With V2-0 and V2-0b introducing concurrent in-process attempt execution (`MINICLOUD_WORKER_SLOTS`), is the long-term vision to keep multi-slot execution inside a single Go process, or should NodeAgent spawn independent worker child processes per attempt to achieve memory and crash isolation?
