# MiniCloud Platform Roadmap: V1, V2, and V3

## 1. Overview
MiniCloud development is structured into three major releases. Each version builds directly upon the previous release on the same codebase, ensuring continuous architectural compatibility and verifiable invariants.

---

## 2. Phase-to-Version Mapping & Exit Criteria

| Version | Phase | Scope | Exit Criteria | Status |
|---|---|---|---|:---:|
| **V1** | **Phase 0: Design** | Requirements, state machine, invariants, failure model, protocol. | Design docs reviewed; risks and non-goals explicit. | **DONE** |
| **V1** | **Phase 1: Local Core** | Go module, CLI/API, allowlisted task types (`echo`, `sleep`, `sha256_loop`), multi-process workers. | End-to-end jobs execute across independent processes; unit & integration tests pass `-race`. | **DONE** |
| **V1** | **Phase 2: Independent Workers** | Worker failure detection loop, heartbeat reaper, attempt IDs, capacity tracking. | Worker processes communicate only through HTTP protocol; dead workers detected and evicted. | *Next* |
| **V1** | **Phase 3: Durable State** | PostgreSQL schema, forward-only migrations, coordinator restart recovery. | Accepted jobs recover correctly across coordinator restarts; DB is authoritative truth. | Planned |
| **V1** | **Phase 4: Retry & Recovery** | Leases, bounded retries, stale-attempt rejection, automated fault injection suite. | Fault suite verifies invariants; expired leases revoked; no stale result wins. | Planned |
| **V2** | **Phase 6: Scheduling Research** | Resource-aware scheduling policy, repeatable workload generator, comparative metrics. | Fair comparison with round-robin; p50/p95/p99 latency, queue wait, and throughput recorded. | Planned |
| **V2** | **Transport Evaluation** | Evaluate Redis Streams dispatch against PostgreSQL pull baseline via `internal/queue`. | Benchmark report comparing throughput, recovery, and duplicate delivery; ADR records choice. | Planned |
| **V3** | **Execution Isolation** | Docker container executor with CPU, memory, PID, and hard runtime limits. | Constrained task execution; non-root isolation; no shell execution on host. | Planned |
| **V3** | **Phase 7: Multi-Machine** | Network deployment, worker authentication, mutual TLS, network fault testing. | Workers on separate physical/virtual hosts execute jobs safely over untrusted network. | Planned |
| **V3** | **Phase 8: Hardening & Release** | Prometheus metrics, operational docs, performance tuning, reproducible release. | Clean checkout setup succeeds from README; reproducible benchmark suite tagged `v1.0.0`. | Planned |

---

## 3. Scope & Version Gate Rules
1. **Incremental Complexity:** Infrastructure is introduced only when its phase is active. No PostgreSQL in Phase 2; no Redis in V1; no Docker until V3.
2. **Interface Decoupling:**
   - Scheduling policies implement `scheduler.Scheduler` without modifying job lifecycle logic.
   - Dispatch transport implements `queue.Transport` so PostgreSQL pull and Redis Streams remain interchangeable.
   - Task execution implements `executor.Task` with no in-process assumptions, allowing container execution in V3.
3. **Compatibility Guarantee:** Additive-only changes within `/v1` API; forward-only database migrations.
