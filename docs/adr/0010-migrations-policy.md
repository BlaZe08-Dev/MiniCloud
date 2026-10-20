# ADR 0010: Database Migrations Policy and Runner Architecture

## Status
PROPOSED (Awaiting Owner Approval)

## Context
As MiniCloud introduces durable PostgreSQL storage in Phase 3, the database schema must evolve predictably across development, testing, and multi-version production deployments.

Uncontrolled schema changes, ad-hoc `ALTER TABLE` statements, or poorly designed migration tooling cause severe distributed systems failures:
- Split-brain schema divergence across environments.
- Deployment deadlocks and table-level locking spikes.
- Irreversible data loss from automatic "down" migration scripts.
- Race conditions when multiple coordinator instances boot simultaneously.

We must define:
1. The migration evolution policy (forward-only vs up/down, expand/contract rules).
2. The migration runner architecture (embedded in-tree runner vs external third-party tools like `golang-migrate` or `goose`).

## Migration Evolution Policy

### 1. Forward-Only, Data-Preserving Migrations
- **No Down Migrations:** "Down" rollbacks in distributed systems are inherently unsafe. If a migration adds a column or splits a table, rolling back drops newly written live production data. All schema evolution in MiniCloud is strictly **forward-only**.
- **Fix Forward:** If a migration contains a defect or performance regression, the resolution is to author a new forward migration that corrects the schema or index.
- **Transactional DDL:** Every migration script runs inside an explicit database transaction (`BEGIN ... COMMIT`). PostgreSQL natively supports transactional DDL (creating tables, adding columns, creating indexes); if any statement in a migration fails, the entire migration rolls back cleanly without leaving the database in a partially applied state.

### 2. The Expand / Contract Pattern for Destructive Changes
Destructive changes (renaming columns, dropping columns, changing types, splitting tables) **must never occur in a single release step**. Any destructive evolution must span multiple releases following the **Expand/Contract** pattern:
1. **Expand (Release N):** Add the new column, table, or constraint as nullable or with defaults. Application code is deployed to read from the old or new location and write to both (dual-write) or write to the new location with fallback.
2. **Backfill / Migrate (Release N or Background):** Backfill historical rows from the old schema representation to the new representation asynchronously.
3. **Contract (Release N+1):** Once all running coordinator instances are upgraded and no operational code references the old column/table, author a subsequent forward migration to drop the old column/table or add `NOT NULL` constraints.

## Migration Runner Options Evaluated

### Option 1: Embedded SQL Files with Minimal In-Tree Runner (Recommended)
- **Architecture:** Migration files stored as raw, numbered SQL scripts (`migrations/000001_initial_schema.sql`, `migrations/000002_add_index.sql`) embedded directly into the Go binary using Go 1.16+ `//go:embed migrations/*.sql`.
- **Mechanism:**
  - A dedicated schema tracking table:
    ```sql
    CREATE TABLE IF NOT EXISTS schema_migrations (
        version BIGINT PRIMARY KEY,
        name TEXT NOT NULL,
        checksum TEXT NOT NULL,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
    );
    ```
  - **Concurrency Safety via Advisory Locks:** Before running migrations, the runner acquires a PostgreSQL transaction-level advisory lock:
    `SELECT pg_advisory_lock(hashtext('minicloud_migrations'));`
    This ensures that when multiple coordinator replicas start concurrently, exactly one instance executes pending migrations while others wait.
  - **Checksum Verification:** Each migration file's SHA-256 hash is recorded upon application. On startup, the runner verifies that previously applied migrations match their recorded checksums. If a previously applied migration file was modified in source code, the runner refuses to proceed, preventing silent schema drift.
  - **Zero Third-Party Dependency:** Built purely using `pgxpool` and standard library `embed`, `crypto/sha256`, and `sort`.

### Option 2: `golang-migrate/migrate`
- **Architecture:** Established Go migration library and CLI tool.
- **Trade-offs:** Brings a large dependency tree (often pulls dozens of cloud storage SDKs, database drivers, and CLI frameworks). Strongly encourages paired `.up.sql` and `.down.sql` files, conflicting with MiniCloud's forward-only invariant. Running it requires installing an external binary or maintaining separate container images in CI.

### Option 3: `pressly/goose`
- **Architecture:** Lightweight migration tool supporting SQL and Go-based migrations.
- **Trade-offs:** Adds an external module dependency. Go-based migration files encourage executing arbitrary application logic inside schema migrations, violating the boundary between structural SQL definitions and application business logic.

## Decision Proposal

1. **Adopt Forward-Only Embedded SQL Migrations** with a minimal in-tree migration runner in `internal/storage/postgres/migrations.go`.
2. **Schema Tracking & Safety:**
   - Sequential, zero-padded integer numbering (`000001_name.sql`, `000002_name.sql`).
   - Dedicated `schema_migrations` table tracking version, name, SHA-256 checksum, and execution timestamp.
   - Mandatory PostgreSQL advisory lock (`pg_advisory_lock`) during migration runs.
   - Startup verification that applied migrations match filesystem checksums exactly.
3. **Enforce Expand/Contract:** Destructive schema alterations must follow the three-phase expand/contract lifecycle across separate pull requests and release boundaries.

## Invariants Enforced
- **Atomic Migrations:** Each migration script executes within an explicit single transaction (`BEGIN ... COMMIT`).
- **Cluster Bootstrapping Safety:** PostgreSQL advisory locking prevents concurrent migration races during coordinator cluster startups.
- **Immutable Migration History:** SHA-256 checksum enforcement guarantees that applied migrations cannot be tampered with or edited post-facto.

## Consequences
- **Positive:**
  - Coordinator binary is 100% self-contained: single executable runs migrations on startup or via an operational flag (`minicloud-coordinator --migrate-only`).
  - Zero external toolchains or CLI binaries required in CI, Docker, or development machines.
  - Guarantees data preservation under all upgrade scenarios.
- **Negative:**
  - Requires maintaining a small (~150 LOC) migration runner in `internal/storage/postgres`.
