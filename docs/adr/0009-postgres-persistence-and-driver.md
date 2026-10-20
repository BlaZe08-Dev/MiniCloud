# ADR 0009: PostgreSQL Persistence and Driver Selection

## Status
Accepted by Owner (2026-10-20, with Staged Conditions)

## Context
In Milestones 1 and 2 (Phases 0–2), MiniCloud operated with an in-memory storage repository (`internal/storage/inmemory.go`). While sufficient for validating domain finite state machine (FSM) transitions, worker HTTP protocol polling, and liveness reaper semantics, in-memory state is non-durable: any coordinator process restart loses all registered workers, submitted jobs, execution attempts, and audit logs.

Phase 3 introduces durable state via PostgreSQL. As mandated by `GEMINI.md`, the platform enforces a strict dependency rule:
> Standard library only in Milestone 1–2. PostgreSQL driver (Phase 3) and Redis client (V2) require explicit owner approval and an ADR before introduction. No unapproved dependencies.

To implement the PostgreSQL repository, we must select:
1. The Go database driver and connection pooling mechanism.
2. The pinned PostgreSQL container image version for development and automated testing.

## Driver Options Evaluated

### Option 1: `jackc/pgx/v5` with `pgxpool` (Selected)
- **Architecture:** Pure Go PostgreSQL driver and toolkit providing native wire protocol v3 support, connection pooling (`pgxpool`), and direct binary encoding/decoding.
- **Performance & Features:** Native support for PostgreSQL binary format, parameterized queries with typed arrays, batched queries (`pgx.Batch`), native transaction management (`pgx.Tx`), direct `COPY` protocol for bulk operations, and structured error inspection with SQLSTATE error code extraction (`*pgconn.PgError`).
- **Dependency Footprint:** Minimal and modern pure Go dependencies (`github.com/jackc/pgx/v5`, `github.com/jackc/pgconn`, `github.com/jackc/puddle/v2` for pooling). Zero Cgo dependencies; builds cleanly across all platforms with `-race`.
- **Standard Library Interoperability:** While `pgxpool` provides a superior, concurrency-optimized API, pgx also provides `github.com/jackc/pgx/v5/stdlib` if an explicit `database/sql` driver adapter is ever required.

### Option 2: `database/sql` with `lib/pq`
- **Architecture:** The classic Go PostgreSQL driver adhering to `database/sql.DB`.
- **Status & Maintenance:** `lib/pq` has been in maintenance mode and deprecated by its maintainers since 2020. The official repository recommends modern drivers (primarily `pgx`).
- **Shortcomings:** Inferior connection pooling ergonomics compared to `puddle`/`pgxpool`, lacks full PostgreSQL v3 wire binary protocol benefits, limited context cancellation responsiveness on network timeouts, and no active feature development.

### Option 3: `database/sql` with `jackc/pgx/v5/stdlib`
- **Architecture:** Standard library `database/sql` using pgx v5 under the hood.
- **Trade-off:** Retains standard library interface familiarity, but imposes an unnecessary reflection and wrapper layer on top of pgx, preventing idiomatic use of `pgx.Batch`, pgx-native listen/notify, and direct binary type optimizations.

### Option 4: Full ORMs (`gorm.io/gorm`, `entgo.io/ent`)
- **Architecture:** High-level object-relational mapping frameworks.
- **Evaluation:** Strictly rejected. Full ORMs obscure raw SQL queries, complicate partial unique indexes and conditional state transitions (`UPDATE ... WHERE state = 'QUEUED'`), introduce heavy reflection overhead, and violate MiniCloud's core philosophy of transparent, engineering-serious distributed systems implementation.

## PostgreSQL Image Tag Decision

### Tag Pinning Policy
- Using `postgres:latest` is strictly prohibited. `latest` introduces non-deterministic builds, unpredictable schema behavior, and unexpected engine upgrades during continuous integration or local development.
- A specific major version tag must be pinned: `postgres:16-alpine`.

### Alternatives Evaluated:
1. `postgres:16-alpine` (Selected):
   - Fast pull times and minimal storage footprint (~130 MB vs ~450 MB for Debian-based images).
   - Fully reproducible, contains standard PostgreSQL 16 server utilities (`psql`, `pg_isready`), and runs unprivileged in container runtimes.
2. `postgres:16-bookworm` / Debian:
   - Larger image footprint (~450 MB) with broader system glibc tools; unnecessary for MiniCloud's dedicated database service.
3. `postgres:15-alpine`:
   - Stable previous generation, but PostgreSQL 16 brings improvements in query parallelism, VACUUM performance, and modern SQL/JSON capabilities.

## Owner Decision & Staged Conditions

The repository owner has **APPROVED** `jackc/pgx/v5` + `pgxpool` and `postgres:16-alpine` subject to the following explicit operational conditions:

1. **Go Toolchain Compatibility (Go <= 1.22):**
   - The development and host environment runs `go1.22.2`.
   - The selected pgx version must declare `go <= 1.22` in its `go.mod`.
   - **Verification:** Version `github.com/jackc/pgx/v5 v5.7.1` declares `go 1.21` in its `go.mod` (as does `v5.6.0`). This strictly satisfies the condition (`1.21 <= 1.22`).
   - **Transitive Dependency Verification:** When Slice 3c adds `pgx`, all transitive dependencies (`pgconn`, `puddle`, `pgpassfile`, `pgservicefile`, `x/crypto`, `x/text`, etc.) must also be explicitly inspected and verified against Go <= 1.22 by checking `go.mod` and `go.sum` immediately after running `GOTOOLCHAIN=local go get github.com/jackc/pgx/v5@v5.7.1`.
2. **Toolchain Auto-Download Guardrail (`GOTOOLCHAIN=local`):**
   - Development and CI environments must enforce `GOTOOLCHAIN=local`.
   - This guarantees that Go will never silently auto-download a newer toolchain or bump the `go` directive in `go.mod`.
3. **Staged Introduction (Slice 3c):**
   - No external dependencies are added to `go.mod` during design or Slices 3a/3b.
   - The dependency is introduced strictly in Slice 3c when the concrete PostgreSQL repository implementation begins.

## Invariants Enforced
- **Zero Third-Party Domain Leakage:** `internal/domain` remains completely decoupled from database drivers and SQL types.
- **Deterministic Toolchain:** Pinned driver major version (`v5`), pinned Go toolchain guardrail (`GOTOOLCHAIN=local`), and pinned database image (`16-alpine`) prevent drift.
- **Race-Clean Execution:** Pure Go driver runs deterministically under `go test -race -count=1`.

## Consequences
- **Positive:**
  - Robust, production-grade connection pooling with health checking and lifecycle hooks.
  - Precise SQLSTATE error code handling for unique constraint violations (`23505`) and serialization conflicts (`40001`).
  - High-performance binary parameter serialization for UUIDs, timestamps, and byte arrays.
- **Negative:**
  - Introduces MiniCloud's first external Go module dependency tree (`github.com/jackc/pgx/v5`) starting in Slice 3c.
