# ADR 0004: Version Boundaries (V1/V2/V3) and Compatibility Policy

## Status
Accepted by project owner (2026-10-20).

## Context
MiniCloud began with a monolithic 9-phase roadmap (Phases 0–8). As the platform progresses, we need explicit milestone grouping and version boundaries to prevent scope creep, maintain backwards compatibility, and establish clear criteria for stability, experimentation, and production hardening.

Furthermore, we need a formal compatibility policy governing API changes, storage migrations, and architectural invariants across releases.

## Decision

### 1. Version Boundaries (V1 / V2 / V3)
We partition project development into three distinct major platform releases built incrementally on the same codebase:

- **MiniCloud V1 — Core Distributed Execution & Reliability (Phases 0–4):**
  - **Phase 0:** Requirements, formal FSM, protocol, and failure model specifications.
  - **Phase 1:** In-memory local core coordinator, worker protocol, CLI, and allowlisted task execution.
  - **Phase 2:** Independent worker processes, heartbeats, worker failure detection loop, and attempt tracking.
  - **Phase 3:** PostgreSQL durable persistence, forward-only migrations, and coordinator restart recovery.
  - **Phase 4:** Leases, bounded retries, stale-attempt rejection, and automated failure-recovery fault testing.
  - *Dispatch Baseline:* Pull-based dispatch with PostgreSQL storage baseline.

- **MiniCloud V2 — Scheduling Research & Transport Evaluation (Phase 6 + Redis Evaluation):**
  - Resource-aware scheduling policies compared empirically against the round-robin baseline.
  - Repeatable workload generator and benchmark suite.
  - Evaluation of Redis Streams as a dispatch transport against the PostgreSQL pull baseline (see [ADR 0006](0006-redis-streams-dispatch-evaluation-in-v2.md)).
  - Fault-injection testing harness under heavy load.

- **MiniCloud V3 — Execution Isolation, Multi-Node, and Hardening (Phase 5 Isolation + Phases 7–8):**
  - Docker container execution isolation with CPU/memory/PID constraints (see [ADR 0005](0005-docker-isolation-in-v3.md)).
  - Multi-machine networking, worker authentication, and mutual TLS.
  - Observability hardening (Prometheus metrics and structured logging audit).
  - Reproducible release verification from a clean checkout.

### 2. Compatibility & Evolution Policy
1. **API Compatibility:**
   - All changes within the `/v1` HTTP API must be strictly backwards-compatible and **additive-only** (adding optional fields or new endpoints).
   - Removing endpoints, modifying field types, or altering existing response schemas is prohibited within `/v1`.
   - Any breaking change requires a new API version prefix (`/v2`), accompanied by an approved ADR and deprecation notice.
2. **Storage Migrations:**
   - Database migrations (starting in Phase 3) must be strictly **forward-only**.
   - Destructive migrations (dropping columns or tables without multi-phase deprecation) are forbidden.
   - Schema migrations must be idempotent and non-blocking.
3. **Architectural Invariant Enforcement:**
   - Architectural boundaries are codified as automated Go tests (`tests/architecture/`).
   - Domain logic must remain permanently decoupled from transport, storage, HTTP, and executor packages.

## Consequences
- **Positive:**
  - Provides sharp focus for V1: establishing provably correct state transitions, leases, retries, and persistence before introducing scheduling research or container daemons.
  - Eliminates premature infrastructure: Redis Streams and Docker are deferred to their justified phases.
  - Protects users and operators through guaranteed additive compatibility within `/v1`.
- **Negative:**
  - Deferred Docker container isolation means untrusted user code cannot be run on worker hosts in V1; workers are strictly limited to predefined allowlisted tasks (`echo`, `sleep`, `sha256_loop`).

## Alternatives Considered
- *Strict Phase-by-Phase Delivery without Versions:* Rejected because having 9 flat phases lacked clear milestones for benchmark comparisons and stability guarantees.
- *Introducing Redis and Docker in Phase 5 ahead of Scheduling:* Rejected by project owner; scheduler research on Linux Mint processes provides faster feedback than container orchestration overhead.
