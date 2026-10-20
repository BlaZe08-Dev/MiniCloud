# ADR 0005: Defer Docker Isolation to MiniCloud V3

## Status
Accepted by project owner (2026-10-20).

## Context
In the original project blueprint, Docker execution with CPU/memory/time limits was scheduled in Phase 5, before scheduling research (Phase 6). However, scheduling research and baseline benchmark reproducibility on Linux Mint can be achieved with in-process bounded Go tasks (`echo`, `sleep`, `sha256_loop`).

We need to decide when and how container isolation should be introduced into the MiniCloud architecture, and what constraints must be placed on the executor abstraction to prevent premature coupling.

## Decision
1. **Defer Docker isolation to MiniCloud V3:**
   - V1 remains focused on core execution semantics, leases, retries, PostgreSQL persistence, and fault recovery.
   - V2 completes resource-aware scheduling research and reproducible benchmarks.
   - Docker container execution arrives in V3 as the execution isolation mechanism for multi-machine deployments.
2. **Strict Host Safety Invariant:**
   - Arbitrary untrusted shell commands are NEVER executed directly on the host machine.
   - Until Docker isolation exists in V3, MiniCloud workers execute ONLY predefined, allowlisted task types with validated schemas and hard resource bounds (timeouts, payload caps, and output truncation).
3. **Interface Requirement (No In-Process Assumptions):**
   - The executor interface (`internal/executor`) must NOT assume in-process execution (e.g., must not pass pointers or shared memory).
   - The interface must operate purely on byte slices/JSON payloads, context deadlines, exit codes, and string outputs so a `DockerExecutor` or remote container runner can be swapped in without modifying coordinator orchestration, scheduler logic, or job lifecycle state machines.

## Consequences
- **Positive:**
  - Avoids Docker daemon dependencies, socket mounting complexities, and container cleanup overhead during early core correctness and scheduler research.
  - Keeps tests fast, reproducible, and runnable in any standard Go environment without root/docker permissions.
  - Ensures clean interface boundary for the task executor.
- **Negative:**
  - Workers cannot run arbitrary binary workloads in V1 and V2; workloads are limited to the allowlisted tasks registered in the executor.

## Alternatives Considered
- *Docker in V1 or V2:* Rejected because container startup overhead, daemon failure modes, and image management distract from proving core state transitions and scheduler comparison.
- *MicroVMs (Firecracker / gVisor):* Deferred as unnecessary complexity for a single-developer learning platform; Docker provides standard isolation suited for V3.

## Known Gaps for V3 Container Executor

A review of the current `internal/executor` abstraction (`Task` interface and `Registry.Execute`) against future container requirements identifies the following design gaps to address in V3:

1. **Task-to-Image Mapping & Environment Configuration:**
   - Current: Tasks are statically registered Go structs implementing `Name()`, `Validate([]byte)`, and `Execute(ctx, []byte)`.
   - V3 Gap: A container executor needs to map `TaskType` or job configuration to container images (e.g., `docker.io/...`), entrypoints, command-line arguments, and environment variables.

2. **Resource Constraint Envelopes (CPU, Memory, PIDs):**
   - Current: `Execute` enforces execution duration bounds via `context.WithTimeout` and payload/output bounds via buffer size checks (64 KiB).
   - V3 Gap: Container execution requires specifying and enforcing cgroup limits (e.g., memory limits, CPU shares/quotas, and PID ceilings) per attempt, passed down from job definition or worker capacity profiles.

3. **Output & Log Streaming vs Buffered Payloads:**
   - Current: Output is returned as a single in-memory string capped at 64 KiB upon task completion.
   - V3 Gap: Containers may emit voluminous standard out/err streams or log artifacts requiring stream multiplexing, chunked collection, or log storage rather than a single synchronous return string.

4. **Lifecycle & Cleanup Under Cancellation/Failure:**
   - Current: In-process tasks check `ctx.Done()`.
   - V3 Gap: Container executors must reliably handle container cleanup (`docker stop`, `docker rm -f`, or OCI runtime kills) even when workers crash, coordinator leases expire, or SIGKILL occurs, preventing orphaned containers from consuming host resources.

5. **Daemon Connectivity & Security Isolation:**
   - Current: In-process execution runs with the worker's UID and privileges.
   - V3 Gap: Interfacing with Docker requires daemon socket access or TLS client authentication (`DOCKER_HOST`, `DOCKER_CERT_PATH`), rootless container execution policies, and dropped privileges (e.g., `no-new-privileges`, seccomp, AppArmor).

