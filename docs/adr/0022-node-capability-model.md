# ADR 0022: Node and Capability Model (V2-1)

## Status

**Proposed** (2026-11-20, task V2-1-ADR, branch `agent/claude/V2-1-ADR`). The owner accepts or changes it later. Revised once after the Gemini audit (`reports/gemini/V2-1-ADR-audit.md`, fix round 1); the orchestrator decisions of that round are labelled Proposed where they appear. Design only: no Go code, no migration file and no test exists for anything in this ADR yet. Every test named below is a planned test.

Owner answers used here: OD-08, OD-09 and OD-19, recorded verbatim in [`docs/agents/handoffs/V2-1-orchestrator-decisions.md`](../agents/handoffs/V2-1-orchestrator-decisions.md). Still open and not answered here: OD-20, OD-21, OD-22, OD-23 (values), OD-24, OD-25, the coordinator availability model and the target scale.

Builds on [ADR 0018](0018-ai-assisted-workflow-dispatch.md) (owner decision D3, device model), [ADR 0020](0020-round-robin-real-slots.md) (slots per worker, the reservation cap), [ADR 0004](0004-version-boundaries-and-compatibility.md) and [ADR 0010](0010-migrations-policy.md) (additive `/v1`, forward-only migrations). Supporting design: [`docs/post-v1/NODE_CAPABILITIES.md`](../post-v1/NODE_CAPABILITIES.md).

### Labels

Each statement carries one label.
- **[Decided: source]**: an accepted ADR, shipped code (confirmed by code reading on `origin/main` at 588fcfd), or an owner answer (OD-08, OD-09, OD-19 of 2026-11-20).
- **[Proposed]**: this ADR's design; binding only after the owner accepts the ADR.
- **[Owner question]**: a choice that belongs to the owner; listed in Section 16 and not chosen here.

No number in this ADR is a measurement. Limits are either existing limits (cited) or derived from a stated rule; no timing value is set (Section 16, OD-23).

## 1. Context

| # | Fact | Label |
|---|---|---|
| C1 | A node is one physical machine. `nodes` is identity only; hardware, telemetry and tools are three separate tables, never one wide row. A worker belongs to at most one node through nullable `workers.node_id`. | [Decided: ADR-0018, D3] |
| C2 | Registering a node and registering a worker are separate protocol steps; the V1 worker registration is unchanged. A worker with `node_id` NULL is a legacy worker and stays valid and eligible. | [Decided: ADR-0018, D3] |
| C3 | Resources (CPU, RAM, disk, GPU) belong to the node and are shared by its workers; slots belong to the worker. | [Decided: ADR-0018 D3; OD-09] |
| C4 | `node_tools` is designed in this ADR and created in V4-3, so no empty table ships in V2. | [Decided: `docs/post-v1/ROADMAP.md` V2-1 and V4-3 rows] |
| C5 | V1 derives worker capacity from active attempts, never from a stored counter (`worker_capacity` view). | [Decided: `docs/schema.md` section 1 principle 4 and section 3.2] |
| C6 | Row-lock order in PostgreSQL is jobs, workers, attempts, results, job_events. | [Decided: `internal/storage/postgres/repository.go:24-33`] |
| C7 | All endpoints are unauthenticated; the coordinator binds `127.0.0.1` by default. | [Decided: SEC-004, accepted V1 risk] |
| C8 | The register body is decoded straight into `domain.Worker`; unknown fields are ignored. The worker ID JSON key is `id` (`internal/domain/worker.go:9`, sent by `internal/worker/client.go:354`). | [Decided: `internal/api/handlers.go:289-293`] |
| C9 | Register validation: worker ID 1..128 bytes of `[A-Za-z0-9_-]` (`internal/api/handlers.go:52-66`); `total_slots` in [1, 1024], `version` at most 64 bytes, at most 16 task names (`internal/coordinator/service.go:193-197`, `:326-348`); CHECK constraints in migration 000003. | [Decided: code; SEC-008, SEC-015, SEC-016, SEC-017] |
| C10 | The next free migration number is 000007. | [Decided: `docs/agents/TASKS.md` reserved touch points; `docs/schema.md` section 6] |
| C11 | No V1 code filters by OS, architecture, tool or RAM. | [Decided: `docs/post-v1/NODE_CAPABILITIES.md` "What already exists"] |

**Documentation conflict found (confirmed by code reading).** `docs/protocol.md` section 4.1 shows the register key as `"worker_id"`, but the code decodes and sends `"id"` (C8). Smallest correction, [Proposed]: slice V2-1c fixes `docs/protocol.md` to `"id"` when it adds `node_id` there. This ADR does not edit `docs/protocol.md`.

## 2. Decision summary

1. **[Decided: OD-19]** Node identity is a random UUID that the node generates on first start and keeps in a file with mode 0600 in a directory it owns; the same UUID is reused on every restart. The coordinator validates the format. Node registration is idempotent. `display_name` is a label: not unique, not a key, no scheduling meaning.
2. **[Decided: OD-08]** Linux is the first implementation, not the only target. The capability contract is versioned and OS-independent; OS-specific code sits behind a platform adapter. V2-1 implements and promises only Linux; on other platforms a field reports unknown.
3. **[Decided: OD-09]** Reservations are made by the scheduler at node level for resources and at worker level for slots. Telemetry never guarantees capacity, and stale telemetry is never a hard guarantee. Reservations are atomic and are released or reconciled on completion, failure, cancellation and lease expiry, and stay correct across a coordinator restart.
4. **[Proposed]** V2-1 builds the data model, the node protocol, the platform adapter and worker-to-node reporting. It does **not** build reservations or placement: those are V2-3, because the per-job resource demand is OD-24 (open). Section 7 fixes the reservation design now so that V2-1's tables do not need to change for it.

## 3. Data model

All tables below are [Proposed] unless a row says otherwise. Every measurement column is nullable: NULL means unknown (Section 10).

### 3.1 `nodes` (identity only)

```sql
CREATE TABLE nodes (
    id            TEXT PRIMARY KEY,
    display_name  VARCHAR(64) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_nodes_id_uuid CHECK (id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CONSTRAINT chk_nodes_display_name CHECK (display_name !~ '[[:cntrl:]]')
);
```

- `id` is TEXT with a CHECK, not the PostgreSQL `UUID` type, so it joins `workers.node_id` (TEXT) without casts and matches the existing TEXT IDs. A canonical lowercase UUID is 36 bytes of `[0-9a-f-]`, so it also passes the existing route identifier check (C9) unchanged. [Proposed]
- `last_seen_at` is written by node registration only, not by telemetry (Section 8.3 explains why). [Proposed]
- **V3-2 extension point.** V3-2 adds credential columns (for example an enrolled key fingerprint and an enrolment time) to `nodes` additively. Nothing in V2-1 reads or depends on their absence, so V2-1 does not change for it. [Decided: OD-19 says V3-2 adds a credential column and V2-1 must not need to change; the column shape is Proposed and belongs to V3-2]

### 3.2 `node_hardware` (versioned static inventory)

```sql
CREATE TABLE node_hardware (
    node_id               TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    version               INTEGER NOT NULL,
    contract_version      SMALLINT NOT NULL,
    agent_version         VARCHAR(64) NOT NULL DEFAULT '',
    os                    VARCHAR(32) NOT NULL,
    arch                  VARCHAR(32) NOT NULL,
    cpu_model             VARCHAR(128),
    cpu_logical_cores     INTEGER,
    cpu_physical_cores    INTEGER,
    memory_total_bytes    BIGINT,
    disk_total_bytes      BIGINT,
    gpu_count             INTEGER,
    gpu_model             VARCHAR(128),
    gpu_vram_total_bytes  BIGINT,
    content_hash          CHAR(64) NOT NULL,
    reported_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (node_id, version),
    CONSTRAINT chk_node_hardware_version CHECK (version > 0)
    -- plus the range CHECKs of Section 9
);
```

- The current inventory is the row with the highest `version` for the node. A registration whose canonical hardware content hashes to the current row's `content_hash` inserts nothing; a different hash inserts `version + 1`. [Decided: "a new version row when it changes", `docs/post-v1/NODE_CAPABILITIES.md` device model table; the hash mechanism is Proposed]
- `os` and `arch` are `runtime.GOOS` and `runtime.GOARCH` of the reporting binary, which are known on every platform, so they are NOT NULL. Every other measurement is nullable. [Proposed]
- `disk_total_bytes` is the size of the volume that holds the node state directory until V4-1 defines workspace volumes. [Proposed]
- Old versions are history. Their number must be bounded (threat T3, Section 13); the retention bound is [Owner question OQ-1]. V2-1b must not ship without it.

### 3.3 `node_telemetry` (latest sample only)

```sql
CREATE TABLE node_telemetry (
    node_id                 TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE RESTRICT,
    contract_version        SMALLINT NOT NULL,
    received_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    sampled_at              TIMESTAMPTZ NOT NULL,
    cpu_load_1m_milli       INTEGER,
    memory_available_bytes  BIGINT,
    disk_free_bytes         BIGINT,
    CONSTRAINT chk_node_telemetry_sample_order CHECK (sampled_at <= received_at)
    -- plus the range CHECKs of Section 9
);
```

- One row per node, overwritten by each report (upsert). Telemetry never writes `node_hardware`. [Decided: "latest dynamic sample", "telemetry updates never rewrite the hardware row", ADR-0018 D3 and `docs/post-v1/ROADMAP.md` V2-1 row; upsert is Proposed]
- **Clock.** `received_at` is database time. The node sends `sample_age_ms`, the age of the sample on its own monotonic clock when it sends the report, and the coordinator stores `sampled_at = received_at - sample_age_ms`. No node wall-clock time is stored, which follows the database-clock rule of `docs/schema.md` section 1 principle 3. [Proposed]
- `cpu_load_1m_milli` is the one-minute load average times 1000, stored as an integer to avoid floating point in SQL. [Proposed]
- GPU utilisation is not in V2-1 (Section 6.3). [Proposed]

### 3.4 `workers.node_id`

```sql
ALTER TABLE workers ADD COLUMN node_id TEXT;
ALTER TABLE workers ADD CONSTRAINT fk_workers_node
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE workers VALIDATE CONSTRAINT fk_workers_node;
CREATE INDEX idx_workers_node ON workers (node_id) WHERE node_id IS NOT NULL;
```

- Nullable, no default: existing rows read NULL (legacy, C2) and PostgreSQL 16 adds the column without a table rewrite. The FK uses `NOT VALID` then `VALIDATE`, as migrations 000003 to 000005 do for constraints. [Proposed; the NOT VALID pattern is Decided: `docs/schema.md` section 5 "Zero Lock Contention"]
- The `worker_capacity` view is unchanged. [Proposed]

### 3.5 `node_tools` (designed here, created in V4-3)

```sql
-- NOT in migration 000007. Created by the V4-3 migration.
CREATE TABLE node_tools (
    node_id           TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    adapter_id        VARCHAR(64) NOT NULL,      -- canonical adapter ID, ^[a-z0-9_]{1,64}$
    cli_version       VARCHAR(64),               -- NULL = unknown
    readiness         VARCHAR(16) NOT NULL,      -- READY, NEEDS_LOGIN, EXPIRED, UNKNOWN, UNAVAILABLE
    probe_result      VARCHAR(32),
    last_probed_at    TIMESTAMPTZ NOT NULL,      -- database time, as for telemetry
    contract_version  SMALLINT NOT NULL,
    PRIMARY KEY (node_id, adapter_id)
);
```

- Per node, never credentials: no token, key, credential file content or environment dump has a column. [Decided: ADR-0018 core invariants; readiness enum from `docs/post-v1/NODE_CAPABILITIES.md` "Tool readiness"]
- A per-node row cap (like the 16 task names of SEC-017) is set in V4-3. [Proposed]

### 3.6 Entity relations

```
nodes 1 ── 0..n node_hardware   (versions)
nodes 1 ── 0..1 node_telemetry  (latest)
nodes 1 ── 0..n workers         (workers.node_id, nullable)
nodes 1 ── 0..n node_tools      (V4-3)
```

## 4. Protocol (additive `/v1` only)

All of Section 4 is [Proposed]; the additive rule itself is [Decided: ADR-0004, `docs/protocol.md` section 5.1].

### 4.1 `POST /v1/nodes/register` (new)

```json
{
  "node_id": "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23",
  "display_name": "desk-linux",
  "contract_version": 1,
  "agent_version": "v1.2.0",
  "hardware": {
    "os": "linux", "arch": "amd64",
    "cpu_model": "...", "cpu_logical_cores": 8, "cpu_physical_cores": 4,
    "memory_total_bytes": 17179869184,
    "disk_total_bytes": null,
    "gpu_count": null, "gpu_model": null, "gpu_vram_total_bytes": null
  }
}
```

The example values are illustrative input, not measurements.

Response 200: `{"status": "REGISTERED", "node_id": "...", "hardware_version": 3}`.

- **Idempotent.** The same `node_id` registering again updates `display_name` and `last_seen_at` on the existing row and never creates a second one (`INSERT ... ON CONFLICT (id) DO UPDATE`). It inserts a new hardware version only if the content hash changed (Section 3.2). [Decided: OD-19 idempotency; SQL shape Proposed]
- An absent or `null` measurement is stored as NULL (unknown); `0` is stored as zero (Section 10).
- Errors: 400 `MALFORMED_JSON` (existing code) for undecodable JSON; 400 `INVALID_REQUEST` (existing code) for a bad UUID, an out-of-range value or a bad string; 400 `UNSUPPORTED_CONTRACT_VERSION` (new code, new endpoint) for a `contract_version` the coordinator does not support. Unknown JSON fields inside a supported version are ignored, as everywhere in `/v1`; an unknown required version fails closed. [Decided: fail closed on unknown required versions, `docs/post-v1/NODE_CAPABILITIES.md` "Registration and heartbeat"; codes Proposed]

### 4.2 `POST /v1/nodes/{node_id}/telemetry` (new)

```json
{
  "contract_version": 1,
  "sample_age_ms": 120,
  "cpu_load_1m_milli": 850,
  "memory_available_bytes": 9126805504,
  "disk_free_bytes": null
}
```

Response 200 `{"status": "ACK"}`. 404 `NODE_NOT_FOUND` (new code, new endpoint) when the node has not registered; the sender then registers again. Telemetry is a separate call from worker heartbeats; the worker heartbeat (`docs/protocol.md` section 4.2) is unchanged.

### 4.3 How a worker reports its node: optional `node_id` on `POST /v1/workers/register`

```json
{ "id": "worker-1", "version": "...", "total_slots": 4, "supported_tasks": ["echo"],
  "executes_concurrently": true, "node_id": "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23" }
```

- Absent or `null`: `workers.node_id` becomes NULL and the worker is legacy; the request and response are exactly as today. This keeps the v1.0.0, V2-0 and V2-0b workers unchanged. [Decided: C2; mechanism Proposed]
- Present: must be a canonical UUID of a node that is already registered. A malformed value or an unregistered node returns 400 `INVALID_REQUEST` (an existing code on this endpoint, so no new status semantics). The worker registers its node first, then itself. [Proposed]
- Every registration rewrites `node_id` (as `UpsertWorker` already rewrites every field, `internal/storage/postgres/repository.go:383-392`; in memory `internal/storage/inmemory.go:284-302`), so the latest registration under a worker ID wins. Registration never rejects a change or a clearing of `node_id`, because a restart may legitimately move a worker to another node. [Proposed; orchestrator decision of fix round 1]
- `workers.node_id` is therefore **mutable and not an accounting key**. Running attempts and live reservations keep the node they were placed on: V2-3 snapshots the node ID onto the attempt at claim time (`attempts.node_id`) and onto the job at reservation time (`jobs.assigned_node_id`), and `reserved(node)` sums by those snapshot columns, never by joining `workers` (Section 7.2). A re-registration, honest or forged, changes where the worker's **future** placements go and nothing already placed. [Proposed; orchestrator decision of fix round 1]
- Because unknown fields are ignored today (C8), a new worker that sends `node_id` to an old coordinator registers as a legacy worker; it may also get 404 from the new node routes, which it logs once and then works as a legacy worker. [Proposed]

### 4.4 Read views

- `GET /v1/workers` gains `node_id` (string or `null`). [Proposed]
- `GET /v1/nodes` and `GET /v1/nodes/{node_id}`: identity, current hardware with `version`, latest telemetry with `received_at` and `sampled_at`, and the worker IDs on the node. Unknown values are `null`, never `0`. Whether these views should exist before V3-2, given that they publish node IDs (threat T1), is [Owner question OQ-6]. [Proposed]

### 4.5 Who sends node reports before V3-3

Before the Node Agent (V3-3) exists, the worker process sends the node reports: on start it loads or creates the node ID (Section 5), calls 4.1, then registers itself with `node_id` (4.3), and sends 4.2 on a timer. Several worker processes on one machine that share the same state directory share one node ID; this ADR does not decide how many workers a node runs (OD-20 stays open). [Proposed; OD-20 open]

Node reporting is opt-in through a worker setting that names the state directory; with the setting unset the worker behaves exactly as today. Whether it should instead be on by default, and the default directory, is [Owner question OQ-2]. [Proposed]

## 5. Identity (OD-19 as decided)

| Rule | Label |
|---|---|
| The node generates a random UUID on first start: 16 bytes from `crypto/rand`, formatted as an RFC 9562 version 4 UUID in lowercase. No new dependency is needed. | [Decided: OD-19 random UUID; formatting Proposed] |
| The UUID is kept in a file `node_id` in a state directory the node owns. Directory mode 0700, file mode 0600. | [Decided: OD-19 file mode 0600, owned directory; 0700 for the directory is Proposed] |
| First creation is race-safe: write a temporary file in the same directory, `fsync`, then create the final name with an exclusive link or rename that fails if it exists; on failure read the existing file. Two workers starting together on one machine end with the same UUID. | [Proposed] |
| The same UUID is reused on every restart. | [Decided: OD-19] |
| A file that exists but does not hold a valid UUID, or whose mode lets group or others read or write it (Linux adapter), makes the node refuse to start with a clear error. It never silently creates a new identity, which would orphan the old node row. | [Proposed] |
| The coordinator validates the format (CHECK `chk_nodes_id_uuid` and the same check in the handler). | [Decided: OD-19 format validation; regex Proposed] |
| Registration is idempotent; the same ID updates the existing row. | [Decided: OD-19] |
| `display_name` is an operator label: not unique, not a key, no scheduling meaning. No index, no uniqueness constraint, and no scheduler query reads it. | [Decided: OD-19] |
| V3-2 binds the UUID to an authenticated enrolment by adding a credential column; V2-1 does not change for it. | [Decided: OD-19] |
| **Known limit.** Until V3-2, any caller who knows a node ID can impersonate that node: register hardware for it, send its telemetry, and attach workers to it. Node IDs are not secret (they appear in read views). This is not solved by V2-1. | [Decided: OD-19, consistent with SEC-004] |
| A UUID is an identifier, not a credential. Mode 0600 limits casual reading and accidental copying on the host; it is not access control against the network. | [Proposed] |
| Rejected: a configured-only ID (collisions, typos) and a hardware-derived ID such as `/etc/machine-id` (breaks on cloned VMs, leaks a machine identifier). | [Decided: OD-19] |
| A cloned VM or disk image taken **after** first start also copies `node_id`, so two machines would share one identity. Operators must remove `node_id` from images; V2-1 logs a warning when one node ID reports alternating hardware hashes (threat T7). | [Proposed] |

## 6. Platform scope (OD-08 as decided)

### 6.1 Contract

- The capability contract is the JSON of Section 4 plus the domain types that validate it, with an integer `contract_version` (V2-1 ships version 1). It names no OS-specific concept: no `/proc` path, no registry key, no `sysctl` name. [Decided: OD-08 versioned OS-independent contract; content Proposed]
- Adding an optional field is a compatible change within a version. Changing a field's meaning or unit needs a new `contract_version`; the coordinator then accepts both for a migration window. [Proposed]

### 6.2 Adapter boundary

```
internal/domain      (contract types, validation, contract_version; no OS code)
        ^
        | imports
internal/platform    (Probe interface + per-OS implementations; returns contract types)
        ^
        | imports
internal/worker      (calls Probe, sends reports; later the Node Agent, V3-3)
```

- One interface, [Proposed]:

  ```go
  // Probe reports what this machine can tell; a field it cannot measure is nil (unknown).
  type Probe interface {
      Hardware(ctx context.Context) (domain.NodeHardware, error)
      Telemetry(ctx context.Context) (domain.NodeTelemetry, error)
  }
  ```

- Linux implementation in a file with build constraint `linux`; a fallback in a file with build constraint `!linux` that fills only `os` and `arch` from `runtime` and leaves every other field nil. [Decided: OD-08 Linux only in V2-1, unknown elsewhere; file split Proposed]
- **Adding Windows or macOS without changing the contract** [Proposed]: add a file with build constraint `windows` (or `darwin`) that implements `Probe`, narrow the fallback's constraint to `!linux && !windows`, and add that platform's adapter tests. The domain types, the JSON, the coordinator, the schema and the scheduler do not change, because they only see contract fields and nil. Promising that platform as supported still needs its own build, tests and an approved task (OD-25, open).
- The coordinator, storage and scheduler never import `internal/platform`; `internal/domain` never imports it either. An architecture test enforces both (Section 14). [Proposed]
- Process launching and process-tree handling for later slices also belong behind this boundary, not in the domain. [Decided: `docs/post-v1/NODE_CAPABILITIES.md` "Platform support" design rule; it is a post-V1 design document, not an accepted ADR]

### 6.3 Linux adapter, V2-1 sources (stdlib only, no new dependency)

| Field | Source | Label |
|---|---|---|
| `cpu_model`, `cpu_logical_cores`, `cpu_physical_cores` | `/proc/cpuinfo`; physical cores from distinct (`physical id`, `core id`) pairs; nil when the keys are missing (some ARM kernels omit them) | [Proposed] |
| `memory_total_bytes` | `MemTotal` in `/proc/meminfo`, lowered to cgroup v2 `memory.max` when that file is readable and not `max`, so a worker inside a container does not report host memory | [Proposed] |
| `disk_total_bytes`, `disk_free_bytes` | `syscall.Statfs` on the state directory | [Proposed] |
| `cpu_load_1m_milli` | first field of `/proc/loadavg` | [Proposed] |
| `memory_available_bytes` | `MemAvailable` in `/proc/meminfo` (nil on kernels without it), lowered by the cgroup v2 headroom (`memory.max - memory.current`) when readable | [Proposed] |
| GPU fields | nil (unknown) in V2-1. Discovery needs either vendor tools (an executed binary) or vendor-specific `sysfs` parsing; whether V2-1 includes any is [Owner question OQ-4] | [Proposed] |

## 7. Resource model (OD-09 as decided)

Implemented in V2-3, designed here. Everything in this section is [Proposed] unless labelled.

### 7.1 Ownership

- Slots belong to the worker and keep the ADR-0020 rule (active attempts plus live reservations below the assignable slots, at most 8 live reservations per worker). [Decided: ADR-0020 sections 2.1 and 2.5]
- CPU, RAM, disk and GPU belong to the node and are shared by its workers. [Decided: OD-09]

### 7.2 Capacity, demand and reserved

- **Capacity** of a node on a dimension is the total from its current `node_hardware` row (static inventory). Unknown total means the node has unknown capacity on that dimension. Whether a fixed headroom for the OS is subtracted is [Owner question OQ-5].
- **Demand** of a job is its resource request vector. Its field names and syntax are OD-24 (open), so V2-1 adds no job field. A job with no request has zero demand on every dimension and needs no node reservation.
- **Reserved** on a node is derived, never stored as a counter:

  ```
  reserved(node, dim) =
      sum of the demand snapshot of every attempt in PENDING or RUNNING
          with attempts.node_id = node
    + sum of the demand of every job with a live round-robin reservation
          (assignment_expires_at > database now) with jobs.assigned_node_id = node
  ```

  **The node ID is a snapshot, not a join.** `reserved(node)` never reads `workers.node_id`, because that column is rewritten by every worker registration (Section 4.3) and a join would let a re-registration move demand between nodes or erase it. [Proposed; orchestrator decision of fix round 1]
  - `attempts.node_id` (nullable TEXT) is written once, at claim time, in the transaction that inserts the attempt, from the node the claim locked and admitted against (Section 7.4). It is never updated afterwards.
  - `jobs.assigned_node_id` (nullable TEXT) is written at reservation time, in the same statement that sets `assigned_worker_id`, from the node the reservation was admitted against, and is cleared by every statement that clears `assigned_worker_id`.
  - Both columns, the demand snapshot columns and the OD-24 job fields are added by the **V2-3** migration. V2-1's tables need no change for them.
  - An attempt or job whose node ID is NULL (a legacy worker, or a row from before V2-3) holds no node demand. Such a row has zero demand by construction: a legacy worker only takes zero-demand jobs (Section 7.3), and rows created before V2-3 have no demand fields.

  This follows the V1 rule that capacity is derived from attempt rows (C5) and the ADR-0020 rule that live reservations count (A + R). The demand is snapshotted onto the attempt at claim time, so a later change of a job cannot change what a running attempt holds. The claim that consumes a reservation clears `assigned_worker_id` in the same transaction that inserts the attempt (`internal/storage/postgres/assignment.go:701` and `:719-721`), so demand moves from the second sum to the first without being counted twice or dropped. The claim copies the node it locked into `attempts.node_id`; a reservation whose `assigned_node_id` differs from the claiming worker's current `workers.node_id` is not honoured (Section 7.4).

### 7.3 Admission rule

A job with demand `d` may be reserved to, or claimed by, a worker on node `n` only if, for every dimension where `d > 0`:

1. the capacity of `n` is known, and
2. `reserved(n) + d <= capacity(n)`, and
3. if telemetry for `n` exists and is fresh (OD-23), it does not show less free resource than `d`.

- Telemetry can only **refuse** a placement, never allow one that rule 2 refuses. Telemetry alone never guarantees availability. [Decided: OD-09]
- Stale or absent telemetry is never used as a guarantee. Whether stale telemetry also blocks placement (fail closed) or is ignored by rule 3 is tied to the TTL and the score (OD-21, OD-23) and is decided in V2-3; this ADR only fixes that it never satisfies a constraint. [Decided: OD-09 for "never a guarantee"; the rest Proposed]
- Unknown never passes a hard constraint. [Proposed; matches the proposed placement text in `docs/post-v1/NODE_CAPABILITIES.md`]
- A legacy worker (no node) cannot take a job with non-zero demand (its node capacity is unknown). Jobs with zero demand keep exactly today's eligibility under `round_robin` and `fifo_pull`. [Proposed]

### 7.4 Atomic reservation

[Decided: OD-09 requires atomicity; mechanism Proposed]

- **PostgreSQL.** Every transaction that creates node demand (the assignment pass reserving a job, and every claim arm) first reads the worker's `node_id` without a lock, then locks the node row with `SELECT ... FROM nodes WHERE id = $1 FOR NO KEY UPDATE` after the job row and before the worker row, then locks the worker row and **re-reads** `workers.node_id`. If it no longer equals the locked node, the transaction rolls back and refuses or retries, because the worker re-registered between the unlocked read and the lock. It then computes `reserved(n)` with a new statement, checks the rule, and inserts the attempt (with `attempts.node_id = n`) or sets the reservation (with `jobs.assigned_node_id = n`) in the same transaction. A claim that consumes a reservation whose `jobs.assigned_node_id` differs from the worker's current node does not honour it: the reservation is cleared as if lapsed and the job is placed again. [Proposed; orchestrator decision of fix round 1] Two transactions for two workers of the same node serialise on that node row. Under READ COMMITTED each statement takes a new snapshot, so the second transaction's `reserved(n)` query, run after it gets the lock, sees the first one's committed attempt. No two transactions can both reserve the same capacity.
- **New lock order:** jobs, nodes, workers, attempts, results, job_events. Paths that do not touch node demand (sweeper, commit, cancel, renew) keep their current order and never lock `nodes`.
- **Why `FOR NO KEY UPDATE`.** Inserting or updating a worker with a `node_id` makes PostgreSQL take `FOR KEY SHARE` on the node row through the FK while the worker row is already locked, which is the order workers then nodes. `FOR KEY SHARE` does not conflict with `FOR NO KEY UPDATE`, so this does not deadlock with a claim. It would conflict with `FOR UPDATE`, a key update or a delete, so node rows are never locked `FOR UPDATE`, `nodes.id` is never updated, and node rows are never deleted in V2 (`ON DELETE RESTRICT`). A planned deadlock test covers this (Section 14).
- **In-memory repository.** `PollWork` holds the service mutex for the whole claim (`internal/coordinator/service.go:416-418`), and the assignment pass runs under the same mutex (ADR-0020 F6); the in-memory check runs inside that critical section. A conformance test runs against both repositories.

### 7.5 Release and reconciliation paths

Because `reserved(n)` is derived from attempt and job rows, a reservation is released in the same transaction that moves its attempt out of PENDING and RUNNING or clears its job reservation. No separate release step exists that could be skipped. [Proposed; the state changes cited are Decided by code]

| Path | What happens to the node demand | Code that already changes the state (confirmed by code reading) |
|---|---|---|
| Completion | Commit success sets the attempt `COMPLETED`; demand is released in that transaction. | `internal/storage/postgres/repository.go:1236-1252` (`UPDATE attempts`) and `:1257-1264` (`UPDATE jobs`) |
| Failure | Commit failure sets the attempt `FAILED` (job `RETRYING`, `CANCELLED` or `FAILED`); demand released. A late commit on an already terminal job closes a still `RUNNING` attempt as `LOST` or `CANCELLED`; released. A retry is a new attempt that must pass admission again. | `internal/storage/postgres/repository.go:1236-1252` (`UPDATE attempts`), `:1257-1264` (`UPDATE jobs`); late terminal commit: `:1347-1352` (`UPDATE attempts`, executed at `:1352`) |
| Cancellation | A `QUEUED` or `RETRYING` job: cancel clears its round-robin reservation, so its job demand is released. A `RUNNING` job: cancel only sets `CANCEL_REQUESTED`; the attempt stays `RUNNING` and **keeps** its demand until the worker commits or the lease is swept, because the worker may still be running it (invariant 6). | `internal/storage/postgres/repository.go:1456-1459` (`UPDATE jobs` that clears `assigned_worker_id` for a queued or retrying job) and `:1465-1466` (`UPDATE jobs` that sets `cancel_requested_at` for a running job) |
| Lease expiry | The sweeper sets the attempt `LOST` or `CANCELLED`; demand released. A lapsed or revoked round-robin reservation is cleared by the pass; released. A worker marked `UNHEALTHY` by the reaper does **not** release demand; its attempts keep it until their leases are swept. | `internal/storage/postgres/leases.go:302-317` (`closeAttempt`, `UPDATE attempts` at `:310`) and the switch arms `:354-414` that call it (calls at `:361`, `:371`, `:392`, `:412`); `internal/storage/postgres/assignment.go:216` and `:231`; `internal/storage/postgres/repository.go:503-510` |
| Coordinator restart | Nothing to rebuild: demand is a query over durable rows, so it is exactly what the database holds after restart. Attempts that were running keep their demand; the startup grace period (`internal/coordinator/sweeper.go:19`) delays sweeping, so surviving workers can still commit, and the sweeper releases the rest after grace. The in-memory backend keeps no state across a restart at all, so its jobs, attempts and demand all disappear together. The snapshot columns are durable rows, so a worker that re-registers with another node after the restart does not move the demand that survived it. | `docs/schema.md` "Startup and Outage Grace Mechanisms" |
| Worker re-registers with NULL or another node while it holds attempts or live reservations | Nothing. `reserved(node)` sums `attempts.node_id` and `jobs.assigned_node_id`, so no node's total changes (Section 7.2). The re-registration only changes where the worker's future placements go, and the claim re-check of Section 7.4 refuses a placement that raced with it. Registration does not reject the change (Section 4.3). | `internal/storage/postgres/repository.go:383-392` (`UpsertWorker` rewrites `node_id`); `internal/storage/inmemory.go:284-302`; the snapshot columns are new in V2-3 |
| Hardware change (reconciliation) | A new hardware version with a lower total does not cancel or preempt running attempts. If `reserved(n) > capacity(n)`, the node accepts no new demand on that dimension until enough attempts finish. A total that becomes unknown blocks new demand on that dimension. Capacity is read from `node_hardware` by the snapshot node ID, never through `workers`, so a worker re-registration cannot change it. | [Proposed] |

**In-memory repository equivalents** (`origin/main` at 588fcfd, confirmed by code reading). The V2-3 fields are set at the same points as in PostgreSQL, under the repository mutex, and `reserved` is computed by the same rule over `r.attempts` and `r.jobs`.

| Path | PostgreSQL | In-memory |
|---|---|---|
| Completion, failure | `repository.go:1236-1252`, `:1257-1264` | `internal/storage/inmemory.go:720` (`TransitionTo`), `:724` (`att.State = attState`) |
| Late terminal commit | `repository.go:1347-1352` | `inmemory.go:784-788` (`rejectTerminalCommitLocked`) |
| Cancellation | `repository.go:1456-1459`, `:1465-1466` | `inmemory.go:857-858` (running: `CancelRequestedAt`), `:859-861` (queued or retrying: `ClearAssignment`) |
| Lease expiry | `leases.go:302-317`, `:354-414` | `inmemory.go:1026` (`markLost`), `:1047` and `:1061` (`AttemptCancelled`) |
| Revocation, lapse | `assignment.go:216`, `:231` | `internal/storage/inmemory_assignment.go:136`, `:150` (`ClearAssignment`) |
| Claim consumes reservation | `assignment.go:701`, `:719-721` | `inmemory_assignment.go:307` (`ClearAssignment`), `:338` (`r.attempts[attemptID] = stored`) |
| Worker marked `UNHEALTHY` (demand kept) | `repository.go:503-510` | `inmemory.go:350-364` |
| Worker registration | `repository.go:383-392` | `inmemory.go:284-302` |

### 7.6 Tests that prove each path

Listed in Section 14 under "Resource model (V2-3)". All are planned tests.

## 8. Concurrency and write paths of V2-1

[Proposed]

### 8.1 Node registration

One transaction: upsert `nodes` (takes the row lock implicitly); read the current hardware hash; insert `node_hardware` version + 1 only on a change; prune history beyond the retention bound (OQ-1). Two concurrent registrations of the same ID serialise on the `nodes` row, so versions stay gap-free and unique (the primary key would reject a duplicate anyway).

### 8.2 Worker registration with `node_id`

The existing upsert gains `node_id = EXCLUDED.node_id`. The FK check takes `FOR KEY SHARE` on the node row (Section 7.4). No other lock is added, and the registration does not look at the worker's attempts: it rewrites `node_id` freely, and `reserved(node)` does not depend on it (Section 4.3).

### 8.3 Telemetry

One upsert on `node_telemetry` only. It does not write `nodes.last_seen_at`, so a telemetry stream never contends with the node row lock that V2-3 claims take. Node liveness for operators is `node_telemetry.received_at`.

## 9. Field limits and validation

Limits follow the SEC-015 and SEC-016 style: checked at the API boundary with `400 INVALID_REQUEST`, and again by a named CHECK constraint. String caps reuse existing caps (64 bytes like `version`, SEC-016; 128 bytes like identifiers, SEC-008). Integer caps are derived, not invented: byte and count fields are bounded by 2^53 - 1, the largest integer a JSON number carries exactly, which also keeps any sum of up to 1024 workers' values inside BIGINT. All rows are [Proposed].

| Field | Type | Bounds | Charset / rule | CHECK name |
|---|---|---|---|---|
| `node_id` | TEXT | 36 bytes | lowercase RFC 9562 v4 UUID | `chk_nodes_id_uuid` |
| `display_name` | VARCHAR(64) | 0..64 bytes | valid UTF-8, no control characters, no NUL | `chk_nodes_display_name` |
| `contract_version` | SMALLINT | supported set (V2-1: {1}) | integer | handler only |
| `agent_version` | VARCHAR(64) | 0..64 bytes | as `workers.version` (SEC-016), no NUL | `chk_node_hardware_agent_version` |
| `os`, `arch` | VARCHAR(32) | 1..32 bytes | `^[a-z0-9_]{1,32}$` (Go `GOOS`/`GOARCH` values) | `chk_node_hardware_os_arch` |
| `cpu_model`, `gpu_model` | VARCHAR(128) | NULL or 1..128 bytes | printable ASCII after the adapter replaces other bytes; rejected otherwise | `chk_node_hardware_models` |
| `cpu_logical_cores`, `cpu_physical_cores`, `gpu_count` | INTEGER | NULL or 0..2^31 - 1; logical >= physical when both known; `cpu_*` >= 1 when known | integer | `chk_node_hardware_counts` |
| `memory_total_bytes`, `disk_total_bytes`, `gpu_vram_total_bytes` | BIGINT | NULL or 0..2^53 - 1 | integer | `chk_node_hardware_bytes` |
| `memory_available_bytes`, `disk_free_bytes` | BIGINT | NULL or 0..2^53 - 1 | integer; not checked against the hardware total (they come from different moments) | `chk_node_telemetry_bytes` |
| `cpu_load_1m_milli` | INTEGER | NULL or 0..2^31 - 1 | integer | `chk_node_telemetry_load` |
| `sample_age_ms` | request only | 0..2^31 - 1 | integer; a value that would put `sampled_at` before the node's `created_at` is rejected | handler only |
| `content_hash` | CHAR(64) | 64 | SHA-256 hex, computed by the coordinator, never client input | none (coordinator-written) |
| request body | | 1 MiB | the existing global limit | `docs/protocol.md` section 2 |

- A negative number, a non-integer, a string where a number is expected, or a value above its cap returns 400; JSON `null` and an absent key both mean unknown. [Proposed]
- `mc` and logs print `display_name`, `cpu_model` and `gpu_model` through the existing escaping used for worker-supplied text (SEC-048). [Proposed]
- The new rows are added to `docs/security/field-limits.md` by slice V2-1c. [Proposed]

## 10. Failure and unknown-data semantics

| Rule | Label |
|---|---|
| Absent data means unknown, never zero and never unlimited. | [Decided: `docs/post-v1/NODE_CAPABILITIES.md` rule; ADR-0018 D3 consequences] |
| `0` is a real measured value (for example `gpu_count: 0` means "no GPU found by a probe that can find GPUs"). An adapter that cannot measure a field returns nil, not 0. | [Proposed] |
| Unknown is stored as SQL NULL, shown as JSON `null`, and fails every hard constraint that needs the value. | [Proposed] |
| A node with no `node_hardware` row has unknown capacity on every dimension. | [Proposed] |
| A node with no `node_telemetry` row, or an expired one (OD-23), has unknown telemetry. | [Proposed] |
| A probe error for one field makes that field nil; it does not fail the whole report. A failure to read the node ID file fails worker start (Section 5). | [Proposed] |
| If node registration or telemetry fails (coordinator down, 5xx), the worker keeps its normal V1 loop and retries the node call with the existing capped jittered backoff (`internal/worker/client.go` func `ComputeBackoffWithJitter`). Job execution never waits on telemetry. | [Proposed] |
| If the worker cannot register its node at start, it registers itself without `node_id` (legacy) and retries the node registration; on success it registers itself again with `node_id`. | [Proposed] |

## 11. Legacy workers

- A worker that never sends `node_id` keeps `workers.node_id` NULL, registers, heartbeats, polls, renews and commits exactly as today, and stays eligible under `round_robin` and `fifo_pull`. [Decided: ADR-0018 D3]
- Existing rows after migration 000007 read NULL. [Proposed]
- Under V2-3, a legacy worker gets only jobs with zero demand (Section 7.3). Jobs without a resource request, which is every job submitted with the current API, are unaffected. [Proposed]
- Existing tests that must pass unchanged after V2-1: `TestConformance_Assign_ReservationCapPerWorker`, `TestConformance_Claim_RespectsWorkerCapacity`, `TestConformance_Worker_ExecutesConcurrentlyRoundTrip`, `TestAPI_RegisterWorker_ExecutesConcurrentlyOptional`, `TestAPI_RegisterWorker_SlotsUpperLimit`, `TestAPI_RegisterWorker_VersionLengthLimit`, `TestAPI_IdentifierValidation_NegativeCases`, `TestPostgresWiring_CoordinatorRestartRecovery`. [Proposed list; the tests exist]

## 12. Migration 000007 plan and rollback

[Proposed; forward-only is Decided: ADR-0010]

**File:** migration 000007 in `internal/storage/postgres/migrations/`, name proposed as 000007_node_capability_model.sql.

**Order of statements:**
1. `CREATE TABLE nodes` with its CHECKs.
2. `CREATE TABLE node_hardware` with its CHECKs.
3. `CREATE TABLE node_telemetry` with its CHECKs.
4. `ALTER TABLE workers ADD COLUMN node_id TEXT` (nullable, no default, no rewrite).
5. `ADD CONSTRAINT fk_workers_node ... NOT VALID`, then `VALIDATE CONSTRAINT` (every existing row is NULL, so validation passes).
6. `CREATE INDEX idx_workers_node`.
7. No `node_tools` table (V4-3) and no reservation columns (V2-3).

**Compatibility of the running system:** steps 1 to 3 create new objects; steps 4 to 6 take short locks on `workers` only. The `worker_capacity` view does not select `node_id`, so it does not need to be recreated.

**Rollback (code rollback after 000007 is applied):**
- The tables and the column stay (no down migrations). The previous coordinator neither reads nor writes them, and its `UpsertWorker` does not touch `node_id`.
- Not fully inert, the same pattern as 000006 (`docs/schema.md` section 6): a worker that registered with `node_id` keeps that stored value while the old coordinator runs. After a later roll-forward the value is rewritten by the next registration of that worker ID, because the new upsert always writes `node_id`. Operator step before rolling forward again, if stale links matter: `UPDATE workers SET node_id = NULL;` (running workers send it again on their next registration), or restart all workers.
- Policy-level rollback stays `MINICLOUD_DISPATCH_POLICY=fifo_pull` (ADR-0017); V2-1 itself changes no dispatch behaviour.

**Migration tests:** listed in Section 14.

## 13. Threat model

All endpoints stay unauthenticated until V3-2 (SEC-004); the `127.0.0.1` bind default is the only network control and must not be widened. [Decided: SEC-004, `AGENTS.md` section 2]

| # | Threat | Effect | Bound or mitigation | Label |
|---|---|---|---|---|
| T1 | **Impersonation by known ID.** A caller who knows a node ID registers hardware for it, sends telemetry for it, or attaches its own worker to it. IDs are visible in read views, so "knows" is easy. | Wrong inventory (for example inflated `memory_total_bytes`), so V2-3 could place more demand on the real machine than it holds; fake telemetry hides load or blocks placement; a rogue worker on a node consumes that node's resource budget. | **Not solved before V3-2.** Bounded by the bind default, the field caps (Section 9), and the fact that V2-1 itself does not place by resources. V3-2 binds the ID to a credential. | [Decided: OD-19 known limit; bounds Proposed] |
| T2 | Sybil nodes: many node IDs registered. | Row growth in `nodes`, `node_hardware`, `node_telemetry`. | Same class as many worker IDs today; no per-caller limit exists in V1. Closed with authentication. | [Proposed] |
| T3 | Hardware churn: one ID alternates two hardware contents. | Unbounded `node_hardware` history. | A retention bound per node (OQ-1), pruned in the registration transaction. V2-1b blocks on OQ-1. | [Proposed] |
| T4 | Hostile strings in `display_name`, `cpu_model`, `gpu_model`. | Terminal escape injection in `mc`, log forging. | Charset and length checks (Section 9); escaping as for SEC-048. | [Proposed] |
| T5 | Wrong node clock. | Wrong freshness. | No node wall-clock value is stored; `sampled_at` is database time minus a bounded relative age (Section 3.3). A node can still lie about the age within the cap; that is T1. | [Proposed] |
| T6 | Leak of the `node_id` file. | Same as T1 (the ID is not secret anyway). | Mode 0600 limits local exposure. | [Decided: OD-19 mode 0600] |
| T7 | Cloned image with `node_id`. | Two machines report as one node. | Operator guidance; warning on alternating hardware hashes for one ID. Detection is a heuristic, not a guarantee. | [Proposed] |
| T8 | Probe reads host values inside a container. | Inflated capacity on an honest node. | cgroup v2 limits applied in the Linux adapter (Section 6.3). | [Proposed] |
| T9 | Reservation race between two workers of one node. | Over-commitment. | Node row lock (Section 7.4); planned race test. | [Proposed] |
| T10 | **Re-registration of a worker ID with NULL or another `node_id` while it holds attempts or reservations** (an honest restart that moves, a misconfiguration, or a forged registration, which is T1 applied to workers: registration is unauthenticated). | If `reserved(node)` joined `workers.node_id`, the worker's running demand would vanish from its node (over-commitment, possible out-of-memory on the real machine) or appear on another node (phantom reservations). | `reserved(node)` sums the snapshot columns `attempts.node_id` and `jobs.assigned_node_id`, never `workers.node_id` (Section 7.2); the claim re-reads `workers.node_id` after locking the worker (Section 7.4). Registration still accepts the change (Section 4.3). It does **not** stop a forged registration from steering that worker's future placements, which stays T1 until V3-2. Planned tests and mutant R9, R10 (Section 14). | [Proposed; orchestrator decision of fix round 1] |

**The SEC-004 limit.** Nothing in this ADR authenticates a node, a worker or a caller. Every row above that says "closed with authentication" depends on V3-2. [Decided: SEC-004]

**New finding (warranted, not registered by this task).** T1 is a new attack surface (node-scoped writes that later feed placement) inside SEC-004, in the same way SEC-068 was for reservations. Proposed register entry: next free number (SEC-070 at 588fcfd, where the register ends at SEC-069), severity Low, status "Open (within SEC-004 until V3-2; designed, not executed)", summary "any caller who knows a node ID can register hardware, send telemetry and attach workers for that node; node IDs are visible in read views". This task does not own `docs/security/findings.md`; whether it becomes a new number or an update to SEC-004, and which task writes it, is [Owner question OQ-7]. The number is provisional; the register is the authority (`docs/agents/TASKS.md` reserved touch points).

## 14. Test plan

Every test in this section is a planned test; none exists yet. Database tests run with `make test-db` on the builder's own database (`AGENTS.md` section 9).

### 14.1 Domain and platform (V2-1a)

| Test | Proves |
|---|---|
| `TestNodeID_Validate_Table` (planned test) | Accepts lowercase v4 UUIDs; rejects uppercase, braces, wrong version nibble, wrong variant, 35 and 37 bytes, empty. |
| `TestNodeHardware_Validate_Table` (planned test) | Every cap of Section 9 at the edge and one past it; nil passes; negative fails; logical < physical fails. |
| `TestNodeTelemetry_Validate_Table` (planned test) | Same for telemetry fields and `sample_age_ms`. |
| `TestNodeIDFile_CreateThenReuse` (planned test) | First start creates a 0600 file in a 0700 directory; second start returns the same UUID. |
| `TestNodeIDFile_ConcurrentFirstStart_SameID` (planned test) | Several goroutines start together on an empty directory; all return one UUID; one file exists. |
| `TestNodeIDFile_CorruptFile_Refuses` (planned test) | Invalid content refuses start and does not overwrite the file. |
| `TestNodeIDFile_WideMode_Refuses` (planned test) | Mode 0644 refuses start (Linux). |
| `TestProbeLinux_ParsesFixtures` (planned test) | Parses recorded `/proc/cpuinfo`, `/proc/meminfo`, `/proc/loadavg` fixtures, including files that lack keys (nil, not 0). |
| `TestProbeLinux_CgroupLowersMemory` (planned test) | A fixture `memory.max` below `MemTotal` lowers the total; `max` leaves it unchanged. |
| `TestProbeFallback_OnlyOSArch` (planned test) | The non-Linux fallback, compiled on Linux through an internal constructor, fills `os` and `arch` and leaves every other field nil. |
| `TestArchitecture_PlatformIsolation` (planned test) | `internal/domain`, `internal/coordinator`, `internal/storage` and `internal/api` never import `internal/platform`. |

### 14.2 Storage and migration (V2-1b)

| Test | Proves |
|---|---|
| `TestMigrator_Migration000007_FreshSchema` (planned test) | The three tables, the column, the FK and the index exist with the named CHECKs; 000007 is the last migration. |
| `TestMigrator_Migration000007_UpgradeFrom000006_WithExistingRows` (planned test) | Existing worker rows read `node_id` NULL and keep every other value. |
| `TestSchemaInvariants_NodeChecks` (planned test) | Each CHECK of Section 9 rejects an out-of-range row by SQL. |
| `TestConformance_Node_RegisterIdempotent` (planned test) | Same ID twice: one `nodes` row; `display_name` updated. Both repositories. |
| `TestConformance_Node_HardwareVersionOnChangeOnly` (planned test) | Same content: no new version; changed content: version + 1. Both repositories. |
| `TestConformance_Node_HardwareRetentionBound` (planned test) | History never exceeds the OQ-1 bound under alternating contents. |
| `TestConformance_Node_TelemetryDoesNotTouchHardware` (planned test) | A telemetry upsert leaves the `node_hardware` rows and `nodes.last_seen_at` byte-identical. |
| `TestConformance_Node_UnknownStaysNull` (planned test) | Absent fields read back nil, not 0; a sent 0 reads back 0. |
| `TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced` (planned test) | `DELETE FROM nodes` fails with a foreign-key violation while a `workers` row or a `node_hardware` row references the node, and succeeds once none does (the test deletes the referencing rows first). Also covers `node_telemetry`. |
| `TestConformance_Worker_NodeIDRoundTrip` (planned test) | `node_id` stored, read back, and reset to NULL by a registration without it. |
| `TestPostgresRepository_Node_ConcurrentRegister_GapFreeVersions` (planned test) | Concurrent registrations with different contents yield unique consecutive versions. |

### 14.3 API, worker and abuse (V2-1c, V2-1d)

| Test | Proves |
|---|---|
| `TestAPI_RegisterNode_Validation` (planned test) | 400 for each bad field; 400 `UNSUPPORTED_CONTRACT_VERSION`; 200 and idempotent for a good body. |
| `TestAPI_NodeTelemetry_UnknownNode404` (planned test) | 404 `NODE_NOT_FOUND` for an unregistered ID. |
| `TestAPI_RegisterWorker_NodeIDOptional` (planned test) | Absent stores NULL with the response unchanged; a registered node stores it; malformed or unregistered returns 400 `INVALID_REQUEST`. |
| `TestAPI_ListWorkers_NodeIDNullForLegacy` (planned test) | `node_id` is `null`, not `""`, for a legacy worker. |
| `TestAPI_Node_SampledAtUsesDatabaseClock` (planned test) | `sampled_at` equals database receive time minus `sample_age_ms`, independent of any client time. |
| `TestWorker_RegistersNodeThenSelf` (planned test) | With the state directory set: node register, then worker register with `node_id`, then telemetry. |
| `TestWorker_NoStateDir_LegacyRegistration` (planned test) | Without the setting, the register body is byte-identical to today's. |
| `TestWorker_NodeRegisterFails_FallsBackToLegacy` (planned test) | Coordinator 5xx on node register: the worker still registers and runs jobs, then attaches after a retry succeeds. |
| `TestAbuse_Node_ImpersonationByKnownID` (planned test) | Documents T1: a second caller with a known ID can change that node's hardware version. Expected to **pass** (the attack works) until V3-2; it pins the documented limit, not a fix. |
| `TestAbuse_Node_HostileStrings` (planned test) | Control characters and ANSI escapes in `display_name` and `cpu_model` are rejected with 400. |
| `TestAbuse_Node_HardwareChurnBounded` (planned test) | T3 over HTTP: history stays within the OQ-1 bound. |

### 14.4 Resource model (V2-3; designed here)

| Path | Test |
|---|---|
| Atomicity | `TestConformance_NodeReserve_TwoWorkersOneNode_NoOvercommit` (planned test): two workers on one node, capacity for one job, two concurrent claims; exactly one attempt; both repositories. |
| Atomicity (PostgreSQL) | `TestPostgresRepository_NodeReserve_RecheckUnderNodeLock` (planned test): a second connection holds the node row and commits an attempt while the claim waits; the claim must re-read and refuse. |
| Lock order | `TestPostgresRepository_NodeLock_NoDeadlockWithWorkerRegister` (planned test): concurrent worker registrations with `node_id` and claims on the same node finish without deadlock under `-race -count=50`. |
| Completion | `TestConformance_NodeReserve_ReleasedOnCommitSuccess` (planned test) |
| Failure | `TestConformance_NodeReserve_ReleasedOnCommitFailure` (planned test) and `TestConformance_NodeReserve_ReleasedOnLateTerminalCommit` (planned test) |
| Cancellation | `TestConformance_NodeReserve_QueuedCancelReleasesJobDemand` (planned test) and `TestConformance_NodeReserve_RunningCancelKeepsUntilCommitOrSweep` (planned test) |
| Lease expiry | `TestConformance_NodeReserve_ReleasedOnLeaseSweep` (planned test), `TestConformance_NodeReserve_LapsedReservationReleased` (planned test), `TestConformance_NodeReserve_UnhealthyWorkerKeepsDemand` (planned test) |
| Coordinator restart | `TestPostgresWiring_NodeReserve_SurvivesCoordinatorRestart` (planned test): restart with running attempts; reserved equals the pre-restart value; after commit or sweep it drops to zero. |
| Lock order (hierarchy) | `TestPostgresRepository_NodeLock_HierarchyEnforced` (planned test): one connection holds the node row `FOR NO KEY UPDATE`, a claim starts and blocks on it; a third connection must still take the claiming worker's row `SELECT ... FOR UPDATE NOWAIT` successfully, which proves the claim has not locked `workers` before `nodes`. |
| Re-registration (attempts) | `TestConformance_Worker_ReRegisterWithActiveAttemptsKeepsNodeDemand` (planned test): a worker on node A with a `RUNNING` attempt re-registers with `node_id` NULL, then with node B, then back; `reserved(A)` and `reserved(B)` are unchanged at every step; both repositories. |
| Re-registration (reservation) | `TestConformance_Worker_ReRegisterWithLiveReservationKeepsNodeDemand` (planned test): the same for a live round-robin reservation, using `jobs.assigned_node_id`; the claim after the move does not honour the reservation (Section 7.4). |
| Re-registration race | `TestPostgresRepository_NodeReserve_ClaimRechecksWorkerNodeAfterLock` (planned test): a second connection changes `workers.node_id` after the claim's unlocked read and before it locks the worker row; the claim must roll back and not insert an attempt on the old node. |
| Live reservation blocks placement | `TestConformance_NodeReserve_LiveReservationBlocksPlacement` (planned test): a live, unclaimed reservation for node A fills its capacity; a second job that needs the same capacity is refused for any worker of A until the reservation is claimed or lapses. Both repositories. |
| Claim moves demand | `TestConformance_NodeReserve_ClaimMovesDemandWithoutDoubleCount` (planned test) |
| Telemetry never guarantees | `TestConformance_NodeReserve_FreshTelemetryCannotExceedCapacity` (planned test) and `TestConformance_NodeReserve_StaleTelemetryNeverSatisfies` (planned test) |
| Unknown | `TestConformance_NodeReserve_UnknownCapacityRefusesDemand` (planned test) and `TestConformance_NodeReserve_LegacyWorkerZeroDemandUnchanged` (planned test) |
| Hardware shrink | `TestConformance_NodeReserve_ShrinkDoesNotPreempt` (planned test) |

### 14.5 Mutants to kill

Each mutant is applied in a throwaway worktree under `~/mc-mut-claude`, must make at least one named test fail, and the test must pass after restore (`AGENTS.md` section 6). All are planned.

| # | Mutation | Must fail (all planned tests) |
|---|---|---|
| N1 | Registration inserts a new `nodes` row instead of upserting | `TestConformance_Node_RegisterIdempotent` (planned) |
| N2 | Insert a hardware version on every registration | `TestConformance_Node_HardwareVersionOnChangeOnly` (planned) |
| N3 | Telemetry upsert also writes `node_hardware` or `nodes.last_seen_at` | `TestConformance_Node_TelemetryDoesNotTouchHardware` (planned) |
| N4 | Map JSON `null` or absent to 0 | `TestConformance_Node_UnknownStaysNull` (planned), `TestAPI_ListWorkers_NodeIDNullForLegacy` (planned) |
| N5 | Accept uppercase or non-v4 UUIDs | `TestNodeID_Validate_Table` (planned) |
| N6 | Regenerate the ID when the file is corrupt | `TestNodeIDFile_CorruptFile_Refuses` (planned) |
| N7 | Create the ID file with mode 0644 | `TestNodeIDFile_CreateThenReuse` (planned) |
| N8 | `UpsertWorker` does not overwrite `node_id` on conflict | `TestConformance_Worker_NodeIDRoundTrip` (planned) |
| N9 | Use the client's clock for `sampled_at` | `TestAPI_Node_SampledAtUsesDatabaseClock` (planned) |
| N10 | Drop the retention prune | `TestConformance_Node_HardwareRetentionBound` (planned) |
| N11 | Ignore cgroup `memory.max` | `TestProbeLinux_CgroupLowersMemory` (planned) |
| R1 | Claim without the node row lock | `TestConformance_NodeReserve_TwoWorkersOneNode_NoOvercommit` (planned), `TestPostgresRepository_NodeReserve_RecheckUnderNodeLock` (planned) |
| R2 | `reserved` ignores live round-robin reservations | `TestConformance_NodeReserve_LiveReservationBlocksPlacement` (planned), `TestConformance_NodeReserve_ClaimMovesDemandWithoutDoubleCount` (planned) |
| R3 | `reserved` counts terminal attempts | `TestConformance_NodeReserve_ReleasedOnCommitSuccess` (planned) |
| R4 | Telemetry free memory used instead of capacity minus reserved | `TestConformance_NodeReserve_FreshTelemetryCannotExceedCapacity` (planned) |
| R5 | Unknown capacity treated as unlimited | `TestConformance_NodeReserve_UnknownCapacityRefusesDemand` (planned) |
| R6 | Lock the node row `FOR UPDATE` | `TestPostgresRepository_NodeLock_NoDeadlockWithWorkerRegister` (planned) |
| R7 | `<` instead of `<=` in the admission rule | `TestConformance_NodeReserve_TwoWorkersOneNode_NoOvercommit` (planned) with an exact-fit case |
| R8 | A claim locks the worker row before the node row | `TestPostgresRepository_NodeLock_HierarchyEnforced` (planned) |
| R9 | `reserved` joins `workers.node_id` instead of summing `attempts.node_id` and `jobs.assigned_node_id`, so a re-registration changes `reserved(node)` | `TestConformance_Worker_ReRegisterWithActiveAttemptsKeepsNodeDemand` (planned), `TestConformance_Worker_ReRegisterWithLiveReservationKeepsNodeDemand` (planned) |
| R10 | The claim does not re-read `workers.node_id` after locking the worker | `TestPostgresRepository_NodeReserve_ClaimRechecksWorkerNodeAfterLock` (planned) |
| N12 | A foreign key to `nodes` uses `ON DELETE CASCADE` (or no action) instead of `RESTRICT` | `TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced` (planned) |

## 15. Implementation slices and owned files

Each slice needs an owner-approved task entry before code (`docs/post-v1/ROADMAP.md` sequencing rule). Paths of files that do not exist yet are written without code formatting. All rows [Proposed].

| Slice | Scope | Owned files |
|---|---|---|
| V2-1a | Contract types and validation; platform adapter (Linux and fallback); node ID file; architecture test | internal/domain/node.go and node_test.go (new); internal/platform/ probe.go, probe_linux.go, probe_other.go, nodeid.go and their tests (new); `tests/architecture/boundaries_test.go` |
| V2-1b | Migration 000007; PostgreSQL and in-memory node storage; conformance suite | internal/storage/postgres/migrations/000007_node_capability_model.sql (new); internal/storage/postgres/nodes.go and migration_000007_test.go (new); `internal/storage/postgres/repository.go` (`UpsertWorker`, `GetWorker`, `ListWorkers`); `internal/storage/postgres/migrator_test.go` (last-migration pin); internal/storage/inmemory_nodes.go (new); `internal/storage/inmemory.go`; `internal/storage/repository.go`; internal/storage/storagetest/nodes.go (new); reserved touch points `internal/domain`, `internal/storage/storagetest`, migration number 000007 |
| V2-1c | API routes and coordinator methods; docs | internal/api/nodes.go (new); `internal/api/handlers.go`; `internal/api/server.go`; internal/coordinator/nodes.go (new); `internal/coordinator/service.go`; `docs/protocol.md`; `docs/schema.md`; `docs/security/field-limits.md` |
| V2-1d | Worker reports its node | internal/worker/node.go (new); `internal/worker/client.go`; `internal/config/config.go`; `.env.example` |
| V2-1e | Security register and abuse tests | `docs/security/findings.md` (reserved touch point); tests/abuse/nodes_test.go (new) |
| V2-3 | Reservations and placement (Section 7). Its migration adds `attempts.node_id`, `jobs.assigned_node_id` and the demand snapshot columns (with the OD-24 job fields), so V2-1's tables need no change | its own ADR and task; not part of V2-1 |
| V4-3 | `node_tools` | its own task |

Builder: Claude Code for V2-1b and V2-1c (migration, repository, locking); V2-1a, V2-1d and V2-1e may go to either agent under `AGENTS.md` section 7. [Proposed]

## 16. Open items and owner questions

### Still open, not answered here

- **OD-20** workers per node and the agent-process model (before V3-3). The design works for any number of workers per node.
- **OD-21** score function, **OD-22** smoothing and hysteresis, **OD-24** job constraint fields (they define demand, Section 7.2), **OD-25** first non-Linux target.
- Coordinator availability model and target scale.
- **OD-23** time-to-live values. This ADR sets no TTL and no report interval. How they will be derived [Proposed]: (1) V2-1d makes the telemetry report interval a worker setting and records `received_at` for each report; (2) a measurement run, with the V2-2 harness or a scenario added to it, records the distribution of gaps between consecutive `received_at` values per node and the reported `sample_age_ms`, on the recorded environment, with real numbers; (3) the owner sets the telemetry TTL from those measured values (for example from a high percentile of the gap plus the measured sample age) and records it in an ADR amendment; (4) until then V2-3 cannot use telemetry as a constraint, which is safe because telemetry never guarantees capacity (Section 7.3). The hardware record TTL is derived the same way from the measured node re-registration interval.

### Owner questions (not chosen here)

- **OQ-1** Retention bound for `node_hardware` history per node (a count). Blocks V2-1b.
- **OQ-2** Node reporting opt-in (state directory setting unset means legacy) or on by default; the default state directory and the setting name.
- **OQ-3** Default telemetry report interval for V2-1d, until OD-23 measurements exist.
- **OQ-4** GPU discovery in the V2-1 Linux adapter: none (unknown, as proposed), `sysfs` only, or a vendor tool run as a registered executable.
- **OQ-5** A fixed OS headroom subtracted from node capacity in V2-3, and its size.
- **OQ-6** Whether `GET /v1/nodes` and the `node_id` field in `GET /v1/workers` ship before V3-2, given that they publish node IDs (T1). Hiding them would not close T1 (IDs can be guessed from other channels or observed locally), but it removes the easiest path.
- **OQ-7** Register T1 as a new finding (provisional SEC-070) or as an update to SEC-004, and which task writes `docs/security/findings.md`.

## 17. Consequences

- V2 gains a node model without changing any V1 request, response, status code or dispatch decision. [Proposed]
- The reservation design reuses the V1 rule of derived capacity, so release has no separate step that a crash could skip. [Proposed]
- Before V3-2, node data is only as trustworthy as any caller on the bind address. V2-3 placement built on it inherits that limit. [Decided: OD-19, SEC-004]
- Non-Linux machines report `os` and `arch` and nothing else until an adapter for them is built and tested. [Decided: OD-08]
