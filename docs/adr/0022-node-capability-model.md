# ADR 0022: Node and Capability Model (V2-1)

## Status

**Proposed** (2026-11-20, task V2-1-ADR, branch `agent/claude/V2-1-ADR`). The owner accepts or changes it later. Revised after the Gemini audit (`reports/gemini/V2-1-ADR-audit.md`, fix round 1; the orchestrator decisions of that round are labelled Proposed where they appear) and again to record the owner's answers OQ-1 to OQ-7 (fix round 2, 2026-11-20). Design only: no Go code, no migration file and no test exists for anything in this ADR yet. Every test named below is a planned test.

Owner answers used here: OD-08, OD-09 and OD-19, recorded verbatim in [`docs/agents/handoffs/V2-1-orchestrator-decisions.md`](../agents/handoffs/V2-1-orchestrator-decisions.md); and Q1 to Q7 (the former OQ-1 to OQ-7), recorded verbatim in [`docs/agents/handoffs/V2-1-owner-answers.md`](../agents/handoffs/V2-1-owner-answers.md). Still open and not answered here: OD-20, OD-21, OD-22, OD-23 (values), OD-24, OD-25, the coordinator availability model and the target scale, OQ-8 (new), and the V2-3 worker-declines design item (Section 16).

Builds on [ADR 0018](0018-ai-assisted-workflow-dispatch.md) (owner decision D3, device model), [ADR 0020](0020-round-robin-real-slots.md) (slots per worker, the reservation cap), [ADR 0004](0004-version-boundaries-and-compatibility.md) and [ADR 0010](0010-migrations-policy.md) (additive `/v1`, forward-only migrations). Supporting design: [`docs/post-v1/NODE_CAPABILITIES.md`](../post-v1/NODE_CAPABILITIES.md).

### Labels

Each statement carries one label.
- **[Decided: source]**: an accepted ADR, shipped code (confirmed by code reading on `origin/main` at 588fcfd), or an owner answer (OD-08, OD-09, OD-19 of 2026-11-20).
- **[Decided: owner answer Q<n>, 2026-11-20]**: the owner's answer to question Q<n> (Q1 to Q7, `docs/agents/handoffs/V2-1-owner-answers.md`). Where this ADR adds a mechanism to carry such an answer out, the mechanism is labelled [Proposed] next to it.
- **[Proposed]**: this ADR's design; binding only after the owner accepts the ADR.
- **[Owner question]**: a choice that belongs to the owner; listed in Section 16 and not chosen here.

No number in this ADR is a measurement. Limits are either existing limits (cited), numbers the owner gave (the 10 hardware records of Q1), or derived from a stated rule; no timing value is set except one placeholder that is labelled "placeholder, not measured" (OQ-8, Section 16). The owner's example headroom values (Section 7.7) are examples, not defaults.

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
5. **[Decided: owner principle, 2026-11-20]** **Identity, Observation and Authority are three separate concepts and must never be treated as interchangeable.** Identity says which machine this is. Observation says what that machine reports. Authority says whether it is authenticated and trusted to participate. A UUID identifies, telemetry describes, authentication establishes trust. In this ADR: the node UUID is identity only (Section 5); hardware and telemetry are observations and never confer identity or trust (Sections 3, 7); no authority exists before V3-2, so every node-derived input is an unauthenticated observation attached to a claimed identity (Sections 7.0, 13).
6. **[Decided: owner answers Q1 to Q7, 2026-11-20]** Summary of the answers, each carried out in the section named: hardware history keeps the latest 10 records per node, with validation and rate limiting of hardware updates (3.2, 8.1); node reporting is explicit opt-in (4.5, 5); the telemetry interval is configurable with no default until OD-23 is measured, with a sequence number and rejection of out-of-order reports (3.3, 4.2); GPU detection is deferred and reported as unknown (6.3, 10); resource headroom is dynamic and OS-aware, defined here and built in V2-3 (7.7); node endpoints are served only in a local-only configuration (4.4); node impersonation has its own finding SEC-070, linked to SEC-004 (13).

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
- **Retention.** The latest 10 `node_hardware` rows per node are kept; older versions are deleted in the registration transaction (Section 8.1). [Decided: owner answer Q1, 2026-11-20] The 10 is a code constant (`MaxHardwareHistory`), not a setting, because the owner fixed it. [Proposed: constant, not setting] Version numbers are never reused, so after pruning the surviving versions are the newest 10 and the oldest ones are gone; the current row (highest version) is never pruned.
- **No record when nothing changed, validated and rate limited.** A registration whose content hash equals the current row's inserts nothing (above). A registration that would insert a new version is validated first (Section 9) and then limited to one new version per node per minimum interval, measured on the database clock from the newest row's `reported_at` (Sections 4.1 and 8.1). [Decided: owner answer Q1 for "validate and rate-limit updates to prevent unnecessary database writes"; mechanism Proposed]

### 3.3 `node_telemetry` (latest sample only)

```sql
CREATE TABLE node_telemetry (
    node_id                 TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE RESTRICT,
    contract_version        SMALLINT NOT NULL,
    session_id              TEXT NOT NULL,
    seq                     BIGINT NOT NULL,
    received_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    sampled_at              TIMESTAMPTZ NOT NULL,
    cpu_load_1m_milli       INTEGER,
    memory_available_bytes  BIGINT,
    disk_free_bytes         BIGINT,
    CONSTRAINT chk_node_telemetry_sample_order CHECK (sampled_at <= received_at),
    CONSTRAINT chk_node_telemetry_session CHECK (session_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CONSTRAINT chk_node_telemetry_seq CHECK (seq BETWEEN 1 AND 9007199254740991)
    -- plus the range CHECKs of Section 9
);
```

- One row per node, overwritten by each accepted report (conditional upsert, below). Telemetry never writes `node_hardware`. [Decided: "latest dynamic sample", "telemetry updates never rewrite the hardware row", ADR-0018 D3 and `docs/post-v1/ROADMAP.md` V2-1 row; upsert is Proposed]
- **Clock.** `received_at` is database time. The node sends `sample_age_ms`, the age of the sample on its own monotonic clock when it sends the report, and the coordinator stores `sampled_at = received_at - sample_age_ms`. No node wall-clock time is stored, which follows the database-clock rule of `docs/schema.md` section 1 principle 3. [Proposed]
- **Why an age and not a node timestamp.** The owner asked that every report carry a timestamp and a sequence number (Q3); this ADR meets the timestamp requirement with `sample_age_ms` plus the database receive time, because a node wall-clock value is one the coordinator cannot trust (wrong or forged clock, T5) and would add a second clock to reason about. [Decided: owner answer Q3 for the timestamp requirement; the age encoding is Proposed, a deviation from the literal wording that the owner may reverse]
- **Sequence and session.** Every report carries `session_id`, a random UUID the worker draws once per process start (`crypto/rand`, same format and validator as a node ID), and `seq`, an integer starting at 1 that the worker increases by one for every report it sends in that session. [Decided: owner answer Q3 for the sequence number and for rejecting out-of-order reports; session mechanism Proposed]
  - **Rule.** A report is accepted if the node has no stored sample, or the stored `session_id` differs from the report's, or the report's `seq` is greater than the stored `seq`. Otherwise (same session and `seq` less than or equal to the stored one) it is rejected and the stored sample is not touched. [Proposed]
  - **Why a session.** A bare increasing counter locks the node out after a worker restart: the new process starts again at 1, below the stored value, and every report would be rejected. A random session ID per process start lets a restart begin again at 1 and be accepted at once, while ordering stays strict inside a session. The alternative, persisting the counter in the state directory, would need a write on every report and breaks when the directory is lost or copied. [Proposed]
  - **Conditional and atomic.** The accept test and the write are one statement. Illustrative shape, designed and not executed against PostgreSQL:

    ```sql
    WITH c(ts) AS (SELECT clock_timestamp())
    INSERT INTO node_telemetry AS t
        (node_id, contract_version, session_id, seq, received_at, sampled_at,
         cpu_load_1m_milli, memory_available_bytes, disk_free_bytes)
    SELECT $1, $2, $3, $4, c.ts, c.ts - ($5 * interval '1 millisecond'), $6, $7, $8 FROM c
    ON CONFLICT (node_id) DO UPDATE SET
        contract_version = EXCLUDED.contract_version, session_id = EXCLUDED.session_id,
        seq = EXCLUDED.seq, received_at = EXCLUDED.received_at, sampled_at = EXCLUDED.sampled_at,
        cpu_load_1m_milli = EXCLUDED.cpu_load_1m_milli,
        memory_available_bytes = EXCLUDED.memory_available_bytes,
        disk_free_bytes = EXCLUDED.disk_free_bytes
    WHERE t.session_id <> EXCLUDED.session_id OR t.seq < EXCLUDED.seq
    RETURNING seq;
    ```

    `ON CONFLICT DO UPDATE` locks the existing row and evaluates the `WHERE` against that locked, latest version, so two concurrent reports for one node serialise and exactly the rule above decides each. Zero returned rows means the report was rejected and nothing changed (not even `received_at`). The single `c.ts` serves both `received_at` and `sampled_at`, so `sampled_at <= received_at` holds. The coordinator checks that the node exists before this statement (404 `NODE_NOT_FOUND`, Section 4.2); node rows are never deleted in V2 (`ON DELETE RESTRICT`), so that check cannot go stale. [Proposed]
  - **Stale reads as unknown.** Telemetry older than the time-to-live reads as unknown, not as the last value. The TTL is OD-23 and is not set here; until it is, the read views show the sample age computed from the database clock and claim no freshness, and V2-3 cannot use telemetry as a constraint (Section 16). [Decided: owner answer Q3 for stale telemetry as stale or unknown; TTL value open]
  - **Never a reservation.** Telemetry is an estimate and never counts as a reservation (Section 7.3). [Decided: owner answer Q3 and OD-09]
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
- **Hardware update limit** [Decided: owner answer Q1 for validating and rate-limiting updates; rule Proposed]:
  - *What is limited:* only a registration that would insert a **new hardware version** (its content hash differs from the current row's). One new version per node per `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` (coordinator setting, a duration, validated positive), measured on the database clock from the newest row's `reported_at`. The limit value is a setting. If a default is needed it is the placeholder `60s`, **placeholder, not measured**, and is owner question OQ-8.
  - *Per node:* keyed by `node_id`, not by caller, because there is no caller identity before V3-2. Rotating node IDs gets around it; that is T2 (sybil), not a new gap.
  - *Not limited:* a registration with an unchanged hash. It inserts no hardware row. It writes the `nodes` row (`display_name`, `last_seen_at`) only when `display_name` changed or `last_seen_at` is older than the same interval; otherwise it writes nothing. A registration that fails validation writes nothing and does not count against the limit.
  - *What the caller gets:* 429 with the new code `RATE_LIMITED`, a `Retry-After` header in whole seconds (the rest of the interval, rounded up, from database time) and an opaque error body that does not echo the stored hardware. Nothing is written, not even `display_name`. A 429 only happens when the node already has a hardware row, so it also tells the sender the node exists.
  - *Why a new code:* no existing code fits. The only 429-like rejection today would be 503 `SERVICE_UNAVAILABLE`, which `docs/protocol.md` defines as a database or coordinator outage and which the worker answers with outage backoff; 400 `INVALID_REQUEST` says the body is wrong, which it is not; 409 is used for stale-attempt rejections and carries no retry hint. 429 is new for a new endpoint, so it is additive.
- Errors: 400 `MALFORMED_JSON` (existing code) for undecodable JSON; 400 `INVALID_REQUEST` (existing code) for a bad UUID, an out-of-range value or a bad string; 400 `UNSUPPORTED_CONTRACT_VERSION` (new code, new endpoint) for a `contract_version` the coordinator does not support; 429 `RATE_LIMITED` (new code, new endpoint) as above; 404 when the node routes are not served (Section 4.4). Unknown JSON fields inside a supported version are ignored, as everywhere in `/v1`; an unknown required version fails closed. [Decided: fail closed on unknown required versions, `docs/post-v1/NODE_CAPABILITIES.md` "Registration and heartbeat"; codes Proposed]

### 4.2 `POST /v1/nodes/{node_id}/telemetry` (new)

```json
{
  "contract_version": 1,
  "session_id": "b6d1e0c4-2a57-4f0e-8d3b-6c9a41f2e7a0",
  "seq": 17,
  "sample_age_ms": 120,
  "cpu_load_1m_milli": 850,
  "memory_available_bytes": 9126805504,
  "disk_free_bytes": null
}
```

The example values are illustrative input, not measurements.

Response 200 `{"status": "ACK"}`. 404 `NODE_NOT_FOUND` (new code, new endpoint) when the node has not registered; the sender then registers again. Telemetry is a separate call from worker heartbeats; the worker heartbeat (`docs/protocol.md` section 4.2) is unchanged. `session_id` and `seq` are required; a missing or invalid one is 400 `INVALID_REQUEST`.

- **Out of order or replayed report.** Same session and `seq` less than or equal to the stored one: **409 `TELEMETRY_OUT_OF_ORDER`** (new code, new endpoint). The stored sample is not touched (Section 3.3). 409 is the existing status for "this request refers to something already superseded" (`STALE_ATTEMPT_REJECTED`, `docs/protocol.md` section 6.3); the code is new. The body is opaque and does not say what `seq` is stored. [Proposed]
- **Worker behaviour.** The worker sends reports one at a time and uses a new `seq` for every attempt, including a retry after a timeout, so an honest sender is never rejected for reusing one. On 409 it logs one line, draws a new `session_id`, restarts at `seq` 1 and carries on with the next report, so a rejected report costs one report and not the node. It never retries the rejected body. [Proposed]
- **Interval.** The worker sends telemetry every `MINICLOUD_NODE_TELEMETRY_INTERVAL`, a duration validated positive. **No default is chosen**: the value comes from the OD-23 benchmark (Section 16). With node reporting on and this setting unset, the worker registers its node and sends hardware but sends no telemetry and logs one info line saying so. Its lower and upper bounds are also OD-23 values and are not set here. [Decided: owner answer Q3 for configurable, no default before the benchmark; the unset behaviour is Proposed]

### 4.3 How a worker reports its node: optional `node_id` on `POST /v1/workers/register`

```json
{ "id": "worker-1", "version": "...", "total_slots": 4, "supported_tasks": ["echo"],
  "executes_concurrently": true, "node_id": "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23" }
```

- Absent or `null`: `workers.node_id` becomes NULL and the worker is legacy; the request and response are exactly as today. This keeps the v1.0.0, V2-0 and V2-0b workers unchanged. [Decided: C2; mechanism Proposed]
- Present: must be a canonical UUID of a node that is already registered. A malformed value or an unregistered node returns 400 `INVALID_REQUEST` (an existing code on this endpoint, so no new status semantics). The worker registers its node first, then itself. [Proposed]
- Every registration rewrites `node_id` (as `UpsertWorker` already rewrites every field, `internal/storage/postgres/repository.go:383-392`; in memory `internal/storage/inmemory.go:284-302`), so the latest registration under a worker ID wins. Registration never rejects a change or a clearing of `node_id`, because a restart may legitimately move a worker to another node. [Proposed; orchestrator decision of fix round 1]
- `workers.node_id` is therefore **mutable and not an accounting key**. Running attempts and live reservations keep the node they were placed on: V2-3 snapshots the node ID onto the attempt at claim time (`attempts.node_id`) and onto the job at reservation time (`jobs.assigned_node_id`), and `reserved(node)` sums by those snapshot columns, never by joining `workers` (Section 7.2). A re-registration, honest or forged, changes where the worker's **future** placements go and nothing already placed. [Proposed; orchestrator decision of fix round 1]
- Because unknown fields are ignored today (C8), a new worker that sends `node_id` to an old coordinator registers as a legacy worker; it may also get 404 from the new node routes, which it logs once and then works as a legacy worker. The same happens against a new coordinator that does not serve the node routes (Section 4.4). [Proposed]

### 4.4 Read views and the local-only rule

[Decided: owner answer Q6, 2026-11-20]: node endpoints are available only in a local-only configuration and are not exposed unauthenticated to network clients; sensitive hardware detail is restricted; no override flag. The mechanism below is [Proposed].

- **Which endpoints.** `GET /v1/nodes`, `GET /v1/nodes/{node_id}`, the `node_id` field of `GET /v1/workers`, and, because they are unauthenticated node-management writes, `POST /v1/nodes/register` and `POST /v1/nodes/{node_id}/telemetry`.
- **How "local-only" is decided.** Once, at coordinator startup, from the configured `MINICLOUD_HTTP_ADDR` (the existing setting, default `127.0.0.1:8080`): the address is local-only if and only if its host part is a literal loopback IP address (`127.0.0.0/8` or `::1`, as Go's `net.IP.IsLoopback` decides). Anything else is not local-only and fails closed: an empty host (`:8080`), `0.0.0.0`, `[::]`, any other IP, and any host **name**, including `localhost`, because a name is not resolved at startup and depends on the resolver. The result is one boolean kept for the process lifetime and logged once at startup. It is never recomputed per request and never read from a header (`Host`, `X-Forwarded-For` and similar are client-controlled). There is **no override flag** and no setting that forces the routes on for a non-loopback address. [Proposed]
- **When the address is local-only:** the routes are served as described below.
- **When it is not:** the node routes are **not served** (404, as for any unknown route), and `GET /v1/workers` omits the `node_id` key entirely (the V1 response shape, not `null`, so a client cannot read "absent" as "legacy"). `POST /v1/workers/register` ignores a `node_id` in the body and stores NULL, exactly as an old coordinator would (Section 4.3). So node registration and telemetry are unavailable on such a coordinator, and an opted-in worker runs as a legacy worker (Section 10): it logs once on the 404 and does not retry (Section 10). This fits the legacy fallback of Section 11 and changes no V1 behaviour. Node reporting across machines therefore waits for V3-2: authentication and authorization must exist before remote node-management endpoints are enabled, and V3-2 replaces this check with an authenticated one. [Proposed; the V3-2 requirement is Decided: owner answer Q6]
- **Local-only responses.** `GET /v1/workers` gains `node_id` (string or `null`). `GET /v1/nodes` is a **summary**: `node_id`, `display_name`, `os`, `arch`, `hardware_version`, the number of workers and the telemetry age in milliseconds. `GET /v1/nodes/{node_id}` is the full view: identity, current hardware with `version`, latest telemetry with `received_at` and `sampled_at`, and the worker IDs on the node. Unknown values are `null`, never `0`. [Proposed]
- **Sensitive hardware detail** [Proposed list]: `cpu_model`, `gpu_model`, `cpu_logical_cores`, `cpu_physical_cores`, `gpu_count`, `memory_total_bytes`, `disk_total_bytes`, `gpu_vram_total_bytes`, `agent_version`, and every telemetry value (`cpu_load_1m_milli`, `memory_available_bytes`, `disk_free_bytes`, which also show what the machine is doing). They appear **only** in `GET /v1/nodes/{node_id}` (inbound in the two POST bodies and at rest in the tables). They are not in the `GET /v1/nodes` summary, not in `GET /v1/workers` (which carries `node_id` only), not in job events, and not in log lines: logs carry the node ID and the failure class, never a hardware or telemetry value, and any string from a node is escaped (Section 9). `os`, `arch` and `hardware_version` are not treated as sensitive. `display_name` is an operator label and appears in both node views.
- **Honest limits.** A loopback address is a **network restriction, not authentication**. (1) Anything that reaches the loopback address passes: every local user and process, a container with host networking, an SSH or other port forward. (2) A **reverse proxy on the same machine defeats the check**: the coordinator listens on loopback and cannot see that a remote client is behind the proxy, so the node routes are then reachable from the network without authentication. A per-request peer-address check would not help, because the proxy's own address is loopback. (3) The check reads the configured address, not the sockets actually opened (for example a listener handed in by a supervisor). (4) A UUID is not proof of identity, so a caller who can reach the routes can still impersonate a node (T1, SEC-070). This ADR does not close these; V3-2 does. [Decided: owner answer Q6 "localhost binding is a network restriction, not an authentication mechanism" and "never treat UUIDs as proof of node identity"; the proxy and listener cases are Proposed analysis]

### 4.5 Who sends node reports before V3-3, and consent

Before the Node Agent (V3-3) exists, the worker process sends the node reports: on start it loads or creates the node ID (Section 5), calls 4.1, then registers itself with `node_id` (4.3), and sends 4.2 on a timer. Several worker processes on one machine that share the same state directory share one node ID; this ADR does not decide how many workers a node runs (OD-20 stays open). [Proposed; OD-20 open]

**Node reporting is opt-in.** [Decided: owner answer Q2, 2026-11-20: explicit opt-in, an explicit `node_reporting_enabled` configuration, a configurable state directory with `~/.minicloud` as the default when reporting is enabled, and the existence of the state-directory setting is not consent.] The mechanism follows the naming and parsing style of `internal/config` (all `MINICLOUD_` environment variables, strict errors that name the key and the value) [Proposed]:

| Setting | Type | Rule |
|---|---|---|
| `MINICLOUD_NODE_REPORTING_ENABLED` | boolean | Unset or empty means `false`. Accepted values are `true` and `false`, case-insensitive; anything else (`1`, `yes`, a typo) is a startup error that names the key and the value, in the style of the strict integer parsers. There is no other way to turn reporting on. |
| `MINICLOUD_NODE_STATE_DIR` | path | Optional. Must be an absolute path when set. Read only when reporting is enabled. |
| `MINICLOUD_NODE_TELEMETRY_INTERVAL` | duration | Section 4.2: positive, no default. |

- **Reporting off (the default).** The worker behaves exactly as today: no state directory is read, created or stat-ed, no node ID file is touched, no probe runs, and no node, hardware or telemetry request is sent; the worker register body is byte-identical to today's. **A state directory alone is not consent:** if `MINICLOUD_NODE_STATE_DIR` is set while reporting is off, the worker does nothing with it and logs exactly one warning line at startup saying that the setting is ignored because `MINICLOUD_NODE_REPORTING_ENABLED` is not `true`. The line names the two settings and carries no path and no other value.
- **Reporting on.** The state directory is `MINICLOUD_NODE_STATE_DIR` if set, otherwise `~/.minicloud`, resolved with `os.UserHomeDir`. The directory and the ID file follow Section 5.
- **Home directory cannot be resolved and no directory is set.** The worker **refuses to enable reporting and stops with a clear startup error** that names both settings (and does not run as a legacy worker). Justification: the owner switched reporting on explicitly, so quietly running without it would hide a misconfiguration; the same fail-closed behaviour already applies to a corrupt ID file (Section 5) and to every invalid setting in `internal/config`, and a stopped worker is visible at once while a silent legacy worker would only show later as a node that never appears. This is a startup configuration error. It is different from a coordinator failure at run time, which keeps the worker running as a legacy worker (Section 10). [Proposed]
- **Consent covers sending as well as reading.** Reporting sends the hardware inventory to the coordinator; that is the data the owner did not want collected without intent (Q2).

## 5. Identity (OD-19 as decided)

| Rule | Label |
|---|---|
| The node generates a random UUID on first start: 16 bytes from `crypto/rand`, formatted as an RFC 9562 version 4 UUID in lowercase. No new dependency is needed. | [Decided: OD-19 random UUID; formatting Proposed] |
| The UUID is kept in a file `node_id` in a state directory the node owns. Directory mode 0700, file mode 0600. | [Decided: OD-19 file mode 0600, owned directory; 0700 for the directory is Proposed] |
| **Consent comes first.** The state directory is read and the ID file is created only when `MINICLOUD_NODE_REPORTING_ENABLED` is `true` (Section 4.5). With reporting off, or with only `MINICLOUD_NODE_STATE_DIR` set, nothing is read, created or sent. With reporting on, the directory is `MINICLOUD_NODE_STATE_DIR` or, by default, `~/.minicloud`. If the home directory cannot be resolved and no directory is set, the worker stops with a startup error. | [Decided: owner answer Q2 for opt-in, the `~/.minicloud` default and "a directory setting is not consent"; the error behaviour is Proposed] |
| First creation is race-safe: write a temporary file in the same directory, `fsync`, then create the final name with an exclusive link or rename that fails if it exists; on failure read the existing file. Two workers starting together on one machine end with the same UUID. | [Proposed] |
| The same UUID is reused on every restart. | [Decided: OD-19] |
| A file that exists but does not hold a valid UUID, or whose mode lets group or others read or write it (Linux adapter), makes the node refuse to start with a clear error. It never silently creates a new identity, which would orphan the old node row. | [Proposed] |
| The coordinator validates the format (CHECK `chk_nodes_id_uuid` and the same check in the handler). | [Decided: OD-19 format validation; regex Proposed] |
| Registration is idempotent; the same ID updates the existing row. | [Decided: OD-19] |
| `display_name` is an operator label: not unique, not a key, no scheduling meaning. No index, no uniqueness constraint, and no scheduler query reads it. | [Decided: OD-19] |
| V3-2 binds the UUID to an authenticated enrolment by adding a credential column; V2-1 does not change for it. | [Decided: OD-19] |
| **Known limit.** Until V3-2, any caller who can reach the node routes and knows a node ID can impersonate that node: register hardware for it, send its telemetry, and attach workers to it. Node IDs are not secret (they appear in the local-only read views). This is not solved by V2-1; it is tracked as SEC-070 (Section 13). | [Decided: OD-19, consistent with SEC-004; owner answers Q6 and Q7] |
| A UUID is an identifier, not a credential and not proof of identity: knowing a node ID does not show that a machine owns it. Mode 0600 limits casual reading and accidental copying on the host; it is not access control against the network. This is the Identity half of the Identity / Observation / Authority split (Section 2, item 5). | [Decided: owner answer Q6 and the owner principle; the 0600 sentence is Proposed] |
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
| GPU fields (`gpu_count`, `gpu_model`, `gpu_vram_total_bytes`) | **Always nil (unknown) in V2-1.** GPU detection is deferred to a later milestone: it needs vendor tools (an executed binary) or vendor-specific `sysfs` parsing, and NVIDIA, AMD and Intel differ. The V2-1 Linux probe never reports `gpu_count: 0`: 0 means "a probe that can detect GPUs confirmed there is none", and V2-1 has no such probe (Section 10). The schema stays extensible: the columns are nullable and a later probe fills them within `contract_version` 1 or a later version. | [Decided: owner answer Q4, 2026-11-20] |

## 7. Resource model (OD-09 as decided)

Implemented in V2-3, designed here (the capacity model and its configuration are defined in Section 7.7). Everything in this section is [Proposed] unless labelled.

### 7.0 Identity, Observation, Authority in the resource model

[Decided: owner principle, Section 2 item 5.] The three concepts stay separate in every rule below:

- **Identity** is the node ID. It selects a node row and is snapshotted onto attempts and reservations (7.2). It says nothing about what the machine has or whether it may take work.
- **Observation** is hardware (the capacity total) and telemetry (the free-resource estimate). It is what a caller said, attached to an identity. Hardware sets the capacity ceiling; telemetry can only lower it. Neither proves identity or grants trust.
- **Authority** is authentication. It does not exist before V3-2, so every Observation in this section is unauthenticated and inherits the T1 limit (Section 13). Nothing in V2-1 or V2-3 may use "this node ID reported it" as evidence that a machine is trusted, and no code path may let a UUID, a hardware value or a telemetry value stand in for a credential.

### 7.1 Ownership

- Slots belong to the worker and keep the ADR-0020 rule (active attempts plus live reservations below the assignable slots, at most 8 live reservations per worker). [Decided: ADR-0020 sections 2.1 and 2.5]
- CPU, RAM, disk and GPU belong to the node and are shared by its workers. [Decided: OD-09]

### 7.2 Capacity, demand and reserved

- **Capacity** of a node on a dimension is the total from its current `node_hardware` row (static inventory). Unknown total means the node has unknown capacity on that dimension. The OS is not given a fixed reserve: headroom is dynamic and OS-aware and is subtracted from the total as Section 7.7 defines (the **effective capacity**). [Decided: owner answer Q5, 2026-11-20]
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
2. `reserved(n) + d <= effective_capacity(n)`, where `effective_capacity(n) = capacity(n) - headroom(n)` and the headroom is the configured part of Section 7.7, and
3. if telemetry for `n` exists and is fresh (OD-23), it does not show less free resource than `d` plus the headroom of Section 7.7.

- Headroom and telemetry can only **refuse** a placement, never allow one that rule 2 refuses and never raise capacity. Telemetry alone never guarantees availability. [Decided: OD-09 and owner answers Q3 and Q5]
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

### 7.7 Capacity model and headroom configuration (defined in V2-1, built in V2-3)

**Direction** [Decided: owner answer Q5, 2026-11-20]: no fixed reserve such as 1 GB for every machine. MiniCloud uses OS-aware dynamic headroom: the memory the OS and other programs use changes while jobs run, so capacity for jobs is what the OS reports as available, not the hardware total. **Milestone boundary** [Decided: owner answer Q5]: V2-1 defines the resource-capacity model and its configuration (this section); V2-3 implements reservations, headroom and enforcement. V2-1 writes no headroom code and reads none of the settings below (parsing settings that nothing uses would be dead surface); the settings are fixed here so V2-3 does not redesign them. [Proposed: V2-1 does not parse them]

**Model** [Proposed, following the owner's wording]:

```
effective_capacity(node, dim) = max(0, hardware_total(node, dim) - headroom(node, dim))
available_for_new_demand(node, dim) = effective_capacity(node, dim) - reserved(node, dim)
admit a demand d only if d <= available_for_new_demand   (and rule 3 of Section 7.3)
```

That is: effective capacity is the hardware total minus headroom, minus MiniCloud's own reservations (`reserved`, Section 7.2).

- **Memory headroom.** Two parts. (a) A configured reserve, `reserve = max(min_free_reserve, ceil(hardware_total x reserve_percent / 100))`, both being settings; the larger of the two applies. (b) An OS-reported part: the Linux `MemAvailable` value that the V2-1 probe already sends as `memory_available_bytes` (Section 6.3). When telemetry is fresh (OD-23), a placement also needs `memory_available_bytes - reserve >= d`. The reserve is needed in (b) so that the OS keeps its own share even when the node is idle. [Proposed]
- **CPU headroom.** Inputs are the logical core count (hardware) and `cpu_load_1m_milli` (telemetry). From these the coordinator derives two estimates, with no new telemetry field: `idle_cores = max(0, logical_cores - load_1m)` and `utilization_percent = 100 x load_1m / logical_cores`. A placement of CPU demand `d` needs `utilization_percent <= max_utilization_percent` and `idle_cores - d >= min_idle_cores`. These are load-average estimates, not measured utilization: the one-minute load average is smoothed and counts runnable and uninterruptible tasks. They are coarse pressure signals that can refuse a placement, not a capacity measurement. Per-core utilization needs `/proc/stat` deltas, which are not in the V2-1 contract; whether they are added is an OD-22 (smoothing) question. [Proposed]
- **Headroom and telemetry only refuse.** Headroom is subtracted and telemetry is a further bound; neither adds capacity, and unknown or stale input never satisfies a bound. OD-09 stays intact: capacity is never raised by an observation. [Decided: OD-09; owner answer Q3]
- **Account for existing reservations.** `reserved` is the derived sum of Section 7.2. [Decided: owner answer Q5 "account for existing MiniCloud reservations"; derivation Proposed]
- **Runtime monitoring.** The same headroom test runs at every admission, from the latest accepted telemetry, so changing pressure changes later decisions. It does not preempt a running attempt (Section 7.5, hardware change row, applies by analogy). [Proposed]
- **Platforms.** Linux only in V2-1 (`MemAvailable`). Windows `GlobalMemoryStatusEx` or an equivalent is a future platform adapter behind the `Probe` interface (Section 6.2, OD-08, OD-25), not part of V2-1. [Decided: owner answer Q5 and OD-08]

**Settings** [Proposed: names, types and validation; the settings are read by the coordinator in V2-3. Style as in `internal/config`: environment variables, a strict error that names the key and the value]:

| Variable | Type | Validation |
|---|---|---|
| `MINICLOUD_RESOURCE_MEMORY_STRATEGY` | string | Accepted: `dynamic`. Other values are a startup error. |
| `MINICLOUD_RESOURCE_MEMORY_MIN_FREE_RESERVE_MB` | integer (MiB) | 0 up to floor((2^53 - 1) / 1048576); 0 means no minimum reserve. |
| `MINICLOUD_RESOURCE_MEMORY_RESERVE_PERCENT` | integer | 0 to 99 (100 would reserve everything). Whole percent. |
| `MINICLOUD_RESOURCE_CPU_STRATEGY` | string | Accepted: `pressure_aware`. Other values are a startup error. |
| `MINICLOUD_RESOURCE_CPU_MAX_UTILIZATION_PERCENT` | integer | 1 to 100. |
| `MINICLOUD_RESOURCE_CPU_MIN_IDLE_CORES` | integer | 0 or more. |

- **No defaults.** The owner's example configuration (memory strategy `dynamic`, `min_free_reserve_mb: 2048`, `reserve_percent: 15`; CPU strategy `pressure_aware`, `max_utilization_percent: 80`, `min_idle_cores: 1`) was in YAML. This repository is stdlib only and configures by environment variables, so the settings above carry the same names as variables. **The owner's values 2048, 15, 80 and 1 are examples, not defaults;** they must be benchmarked and tuned before any default is chosen. When V2-3 resource placement is enabled the settings must be set explicitly; unset means the headroom model is not configured and a job with non-zero demand is refused (unknown never passes, Section 10). [Decided: owner answer Q5 "example starting values, not universal defaults"; the env mapping and the no-default rule are Proposed]

**The worker makes the final decision before starting a job** [Decided: owner answer Q5]: the coordinator's telemetry is an estimate, and the worker, which sees its own machine, decides whether to start. The coordinator's admission is therefore a first, advisory-grade refusal, not the last word. The **mechanism is not designed here.** It is a new protocol and lifecycle question listed as an open V2-3 design item (Section 16, item D-1): what happens to a claimed attempt that the worker declines; whether a decline consumes a retry (ADR-0015); how a decline releases the node demand (Section 7.5: demand is derived from the attempt row, so the attempt must leave `PENDING` or `RUNNING` in the same transaction); and how it interacts with ADR-0019 (worker slots and concurrency) and ADR-0020 (reservations, lapses and the fifo fallback).

## 8. Concurrency and write paths of V2-1

[Proposed]

### 8.1 Node registration

One transaction, in this order (validation, Section 9, has already passed in the handler) [Proposed]:

1. `INSERT INTO nodes ... ON CONFLICT (id) DO NOTHING` (creates a new node; writes nothing for an existing one).
2. `SELECT ... FROM nodes WHERE id = $1 FOR NO KEY UPDATE`: serialises concurrent registrations of the same ID without writing, and uses the lock mode Section 7.4 allows (never `FOR UPDATE`).
3. Read the newest `node_hardware` row (highest version) and compute the content hash of the request.
4. Decide, on database time:
   - **No hardware row yet:** insert version 1 (nothing to limit).
   - **Same hash:** insert no hardware row. Update `nodes.display_name` and `last_seen_at` only if the name changed or `last_seen_at` is older than `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`; otherwise write nothing. Return 200.
   - **Different hash and the newest row's `reported_at` is within the interval:** roll back and return 429 `RATE_LIMITED` with `Retry-After` (Section 4.1). Nothing is written.
   - **Different hash and the interval has passed:** update `nodes`, insert version + 1, then delete the rows with `version <= newest - 10`, which leaves the newest 10 (`MaxHardwareHistory`, Section 3.2).

Concurrent registrations of the same ID serialise on step 2, so versions are unique and increase by one at insertion; after pruning, the surviving versions are the newest 10. The same rules run under the repository mutex in the in-memory backend, and a conformance test runs against both. [Decided: owner answer Q1 for the 10 records, no record on an unchanged hash and rate limiting; steps Proposed]

### 8.2 Worker registration with `node_id`

The existing upsert gains `node_id = EXCLUDED.node_id`. The FK check takes `FOR KEY SHARE` on the node row (Section 7.4). No other lock is added, and the registration does not look at the worker's attempts: it rewrites `node_id` freely, and `reserved(node)` does not depend on it (Section 4.3).

### 8.3 Telemetry

One conditional upsert on `node_telemetry` only (Section 3.3). It does not write `nodes.last_seen_at`, so a telemetry stream never contends with the node row lock that V2-3 claims take (the foreign key takes `FOR KEY SHARE` on the node row, which does not conflict with `FOR NO KEY UPDATE`; inferred, not executed). A rejected report (409) changes nothing. Node liveness for operators is `node_telemetry.received_at`.

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
| `session_id` | TEXT | 36 bytes | lowercase RFC 9562 v4 UUID | `chk_node_telemetry_session` |
| `seq` | BIGINT | 1..2^53 - 1 | integer; must exceed the stored `seq` within the same session (Section 3.3), else 409 `TELEMETRY_OUT_OF_ORDER` | `chk_node_telemetry_seq` |
| `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` | duration (coordinator setting) | positive; upper bound is part of OQ-8 | strict parse naming the key | startup validation |
| `MINICLOUD_NODE_TELEMETRY_INTERVAL` | duration (worker setting) | positive; bounds are OD-23 values, not set here | strict parse naming the key | startup validation |
| hardware history | rows per node | exactly the newest 10 kept | `MaxHardwareHistory` constant (Q1) | pruning in the registration transaction |
| `content_hash` | CHAR(64) | 64 | SHA-256 hex, computed by the coordinator, never client input | none (coordinator-written) |
| request body | | 1 MiB | the existing global limit | `docs/protocol.md` section 2 |

- A negative number, a non-integer, a string where a number is expected, or a value above its cap returns 400; JSON `null` and an absent key both mean unknown. [Proposed]
- `mc` and logs print `display_name`, `cpu_model` and `gpu_model` through the existing escaping used for worker-supplied text (SEC-048). [Proposed]
- The new rows are added to `docs/security/field-limits.md` by slice V2-1c. [Proposed]

## 10. Failure and unknown-data semantics

| Rule | Label |
|---|---|
| Absent data means unknown, never zero and never unlimited. | [Decided: `docs/post-v1/NODE_CAPABILITIES.md` rule; ADR-0018 D3 consequences] |
| `0` is a real measured value (for example `gpu_count: 0` means "no GPU found by a probe that can find GPUs"). An adapter that cannot measure a field returns nil, not 0. The V2-1 Linux probe cannot detect GPUs, so it never reports `gpu_count: 0`; its GPU fields are always nil. A later GPU probe may report 0 only after it confirmed that no supported GPU is present. | [Decided: owner answer Q4 for GPU unknown and "0 only when confirmed"; the general 0-versus-nil rule is Proposed] |
| Unknown is stored as SQL NULL, shown as JSON `null`, and fails every hard constraint that needs the value. | [Proposed] |
| A node with no `node_hardware` row has unknown capacity on every dimension. | [Proposed] |
| A node with no `node_telemetry` row, or an expired one (OD-23), has unknown telemetry. | [Decided: owner answer Q3 for stale telemetry as stale or unknown; TTL value open (OD-23)] |
| A probe error for one field makes that field nil; it does not fail the whole report. A failure to read the node ID file fails worker start (Section 5). | [Proposed] |
| If node registration or telemetry fails (coordinator down, 5xx), the worker keeps its normal V1 loop and retries the node call with the existing capped jittered backoff (`internal/worker/client.go` func `ComputeBackoffWithJitter`). Job execution never waits on telemetry. | [Proposed] |
| If the worker cannot register its node at start (reporting is on), it registers itself without `node_id` (legacy) and retries the node registration; on success it registers itself again with `node_id`. | [Proposed] |
| A 404 on `POST /v1/nodes/register` means the coordinator does not serve node routes (old coordinator, or not local-only, Section 4.4). The worker logs one line, runs as a legacy worker and does **not** retry for the life of the process. A 429 `RATE_LIMITED` means the node already exists: the worker registers itself with `node_id` and retries the hardware registration after `Retry-After`. A 409 `TELEMETRY_OUT_OF_ORDER` starts a new telemetry session (Section 4.2). A startup configuration error (reporting on, no resolvable state directory, an invalid setting) stops the worker (Section 4.5). | [Proposed] |

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
3. `CREATE TABLE node_telemetry` with its CHECKs, including the `session_id` and `seq` columns and their CHECKs (Section 3.3). The hardware retention of 10 and the rate limit need no schema object: pruning is in the registration transaction and the limit reads `node_hardware.reported_at`.
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

**Identity, Observation, Authority.** [Decided: owner principle, Section 2 item 5] The threats below follow from one fact: before V3-2 there is Identity (a node UUID) and Observation (hardware and telemetry), but no Authority. A caller who presents a node ID is believed on identity alone, and what it reports is believed as an observation of that identity. T1 is exactly that gap; T11 is the same gap seen through the telemetry sequence number. Nothing in this ADR treats a UUID, a hardware value or a telemetry value as proof of trust.

| # | Threat | Effect | Bound or mitigation | Label |
|---|---|---|---|---|
| T1 | **Impersonation by known ID.** A caller who can reach the node routes and knows a node ID registers hardware for it, sends telemetry for it, or attaches its own worker to it. IDs are visible in the local-only read views, so "knows" is easy for anything that can reach the coordinator. | Wrong inventory (for example inflated `memory_total_bytes`), so V2-3 could place more demand on the real machine than it holds; fake telemetry hides load or blocks placement; a rogue worker on a node consumes that node's resource budget. | **Not solved before V3-2.** Bounded by: the node routes are served only when `MINICLOUD_HTTP_ADDR` is a loopback address, so unauthenticated **network** clients cannot reach them (Section 4.4), with the limits stated there (a local process, a container with host networking or a reverse proxy on loopback still can); the hardware rate limit (Section 4.1); the field caps (Section 9); and the fact that V2-1 itself does not place by resources. V3-2 binds the ID to a credential. Tracked as SEC-070 (below). | [Decided: OD-19 known limit; owner answers Q6 and Q7; bounds Proposed] |
| T2 | Sybil nodes: many node IDs registered. | Row growth in `nodes`, `node_hardware`, `node_telemetry`. | Same class as many worker IDs today; no per-caller limit exists in V1. Closed with authentication. | [Proposed] |
| T3 | Hardware churn: one ID alternates two hardware contents. | Unbounded `node_hardware` history. | The newest 10 rows per node are kept, pruned in the registration transaction (Section 3.2), and a new version needs the rate-limit interval to have passed (Section 4.1), so one node ID costs at most 10 rows and at most one write per interval. | [Decided: owner answer Q1; mechanism Proposed] |
| T4 | Hostile strings in `display_name`, `cpu_model`, `gpu_model`. | Terminal escape injection in `mc`, log forging. | Charset and length checks (Section 9); escaping as for SEC-048. | [Proposed] |
| T5 | Wrong node clock. | Wrong freshness. | No node wall-clock value is stored; `sampled_at` is database time minus a bounded relative age (Section 3.3). A node can still lie about the age within the cap; that is T1. | [Proposed] |
| T6 | Leak of the `node_id` file. | Same as T1 (the ID is not secret anyway). | Mode 0600 limits local exposure. | [Decided: OD-19 mode 0600] |
| T7 | Cloned image with `node_id`. | Two machines report as one node. | Operator guidance; warning on alternating hardware hashes for one ID. Detection is a heuristic, not a guarantee. | [Proposed] |
| T8 | Probe reads host values inside a container. | Inflated capacity on an honest node. | cgroup v2 limits applied in the Linux adapter (Section 6.3). | [Proposed] |
| T9 | Reservation race between two workers of one node. | Over-commitment. | Node row lock (Section 7.4); planned race test. | [Proposed] |
| T10 | **Re-registration of a worker ID with NULL or another `node_id` while it holds attempts or reservations** (an honest restart that moves, a misconfiguration, or a forged registration, which is T1 applied to workers: registration is unauthenticated). | If `reserved(node)` joined `workers.node_id`, the worker's running demand would vanish from its node (over-commitment, possible out-of-memory on the real machine) or appear on another node (phantom reservations). | `reserved(node)` sums the snapshot columns `attempts.node_id` and `jobs.assigned_node_id`, never `workers.node_id` (Section 7.2); the claim re-reads `workers.node_id` after locking the worker (Section 7.4). Registration still accepts the change (Section 4.3). It does **not** stop a forged registration from steering that worker's future placements, which stays T1 until V3-2. Planned tests and mutant R9, R10 (Section 14). | [Proposed; orchestrator decision of fix round 1] |
| T11 | **Forged sequence number or session.** A caller who can reach the telemetry route and knows a node ID sends reports with chosen `session_id` and `seq` values. | (a) With a **new random `session_id`** the report is always accepted and overwrites the stored sample: the sequence number gives **no protection against an attacker**; it only orders honest reports. (b) With the honest worker's current `session_id` and a huge `seq` (up to 2^53 - 1) the attacker makes every later honest report of that session fail with 409 until the worker starts a new session, which it does on the first 409 (Section 4.2) or on restart. In the meantime the node's stored sample is the attacker's and later reads as stale, then unknown. (c) Neither can raise capacity (Section 7.3): at worst a forged sample refuses placements on that node or hides load, which is T1. | The session design bounds (b) to one rejected report, not a permanent lock-out: a restart always recovers because a new session is accepted (Section 3.3). A bare counter would have been locked out for good by one forged huge value. Replay of an old report from an earlier session is accepted as a new session; stopping that needs authentication (V3-2). Planned abuse test pins this (Section 14.3). | [Proposed; owner answer Q3 for the sequence number] |
| T12 | **Local-only check bypass.** A reverse proxy, port forward or host-network container on the coordinator's machine forwards remote requests to the loopback address. | The node routes are reachable from the network without authentication (T1 from a remote caller). | **Not closed**; stated in Section 4.4. No override flag exists and none is added. The decision is made at startup from `MINICLOUD_HTTP_ADDR`, never from a request header, so a header cannot flip it (planned test). Closed by V3-2 authentication. | [Decided: owner answer Q6 for the limit; check Proposed] |

**The SEC-004 limit.** Nothing in this ADR authenticates a node, a worker or a caller. Every row above that says "closed with authentication" depends on V3-2. [Decided: SEC-004]

**Finding SEC-070 (registered).** T1 is a new attack surface (node-scoped writes that later feed placement) inside SEC-004, in the same way SEC-068 was for reservations. It is registered as **SEC-070** in `docs/security/findings.md`, linked to SEC-004: severity Low, status "Open (within SEC-004 until V3-2; designed, not executed)", summary "node identity impersonation by known ID". Owner severity note: Low while node reporting is controlled and the coordinator is local-only; reassess if remote unauthenticated node registration becomes possible. SEC-004 concerns missing API authentication; SEC-070 concerns node identity impersonation; they stay separate so that remediation and tracking are clearer. [Decided: owner answer Q7, 2026-11-20] The entry was written by task V2-1-ADR-FIX2, so slice V2-1e no longer writes the register (Section 15).

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
| `TestProbeLinux_GPUFieldsAlwaysNil` (planned test) | The V2-1 Linux probe returns nil for `gpu_count`, `gpu_model` and `gpu_vram_total_bytes` on every fixture, including one with a fake GPU entry under `sysfs`; it never returns 0 (Q4). |
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
| `TestConformance_Node_HardwareHistoryKeepsLatest10` (planned test) | After 25 accepted changes (time moved past the interval each time) exactly the newest 10 versions remain, the current one is the highest, and an 11th row never exists. Both repositories. |
| `TestConformance_Node_HardwareRateLimit_RejectsChangeInsideInterval` (planned test) | A changed hardware content inside the interval is rejected with the rate-limit error and writes nothing: no new version, `display_name` and `last_seen_at` unchanged. Both repositories. |
| `TestConformance_Node_HardwareRateLimit_AllowsAfterInterval` (planned test) | The same change is accepted once the interval has passed (database time moved, not a sleep). |
| `TestConformance_Node_HardwareRateLimit_UnchangedNeverLimited` (planned test) | Repeated registrations with an unchanged hash always succeed, insert no hardware row, and write the `nodes` row only when the name changed or `last_seen_at` is older than the interval. |
| `TestConformance_Node_Telemetry_OutOfOrderRejected` (planned test) | Same session, `seq` lower than the stored one: rejected, and the stored row (all columns, `received_at` included) is byte-identical. Both repositories. |
| `TestConformance_Node_Telemetry_ReplaySameSeqRejected` (planned test) | Same session, equal `seq`: rejected, stored row unchanged. |
| `TestConformance_Node_Telemetry_NewSessionAcceptedAfterRestart` (planned test) | A new `session_id` with `seq` 1 is accepted when the stored `seq` is far higher, and replaces the sample. |
| `TestPostgresRepository_Node_TelemetrySeq_ConcurrentSameSession_OneWins` (planned test) | Concurrent reports with the same session and the same `seq`: exactly one is accepted, under `-race -count=50`. |
| `TestConformance_Node_TelemetryDoesNotTouchHardware` (planned test) | A telemetry upsert leaves the `node_hardware` rows and `nodes.last_seen_at` byte-identical. |
| `TestConformance_Node_UnknownStaysNull` (planned test) | Absent fields read back nil, not 0; a sent 0 reads back 0. |
| `TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced` (planned test) | `DELETE FROM nodes` fails with a foreign-key violation while a `workers` row or a `node_hardware` row references the node, and succeeds once none does (the test deletes the referencing rows first). Also covers `node_telemetry`. |
| `TestConformance_Worker_NodeIDRoundTrip` (planned test) | `node_id` stored, read back, and reset to NULL by a registration without it. |
| `TestPostgresRepository_Node_ConcurrentRegister_GapFreeVersions` (planned test) | Concurrent registrations with different contents (time moved past the interval between them) yield unique versions that rise by one at insertion; after pruning the newest 10 remain. |

### 14.3 API, worker and abuse (V2-1c, V2-1d)

| Test | Proves |
|---|---|
| `TestAPI_RegisterNode_Validation` (planned test) | 400 for each bad field; 400 `UNSUPPORTED_CONTRACT_VERSION`; 200 and idempotent for a good body. |
| `TestAPI_NodeTelemetry_UnknownNode404` (planned test) | 404 `NODE_NOT_FOUND` for an unregistered ID. |
| `TestAPI_RegisterWorker_NodeIDOptional` (planned test) | Absent stores NULL with the response unchanged; a registered node stores it; malformed or unregistered returns 400 `INVALID_REQUEST`. |
| `TestAPI_ListWorkers_NodeIDNullForLegacy` (planned test) | `node_id` is `null`, not `""`, for a legacy worker. |
| `TestAPI_Node_SampledAtUsesDatabaseClock` (planned test) | `sampled_at` equals database receive time minus `sample_age_ms`, independent of any client time. |
| `TestWorker_RegistersNodeThenSelf` (planned test) | With `MINICLOUD_NODE_REPORTING_ENABLED=true` (and the interval set): node register, then worker register with `node_id`, then telemetry. |
| `TestWorker_ReportingOff_LegacyRegistration` (planned test) | With reporting not enabled, the register body is byte-identical to today's, no node request is sent and no probe runs. |
| `TestWorker_StateDirSetButReportingOff_NothingReadCreatedOrSent` (planned test) | `MINICLOUD_NODE_STATE_DIR` set (an existing and a non-existing directory) with reporting off: the directory is not read, created or stat-ed, no ID file exists afterwards, no node request is sent, and exactly one warning line is logged that carries no path. |
| `TestWorker_ReportingOn_DefaultStateDirIsHomeMinicloud` (planned test) | Reporting on, no directory set, a fake home directory: the ID file is created under `<home>/.minicloud` with modes 0700 and 0600. |
| `TestWorker_ReportingOn_NoHomeNoDir_StartupError` (planned test) | Reporting on, no directory set, home unresolvable: startup fails with an error naming both settings; no registration is sent (the worker does not fall back to legacy). |
| `TestWorker_ReportingOn_NoTelemetryIntervalSendsNoTelemetry` (planned test) | Reporting on, interval unset: node and worker registration happen, no telemetry request is sent, one info line is logged. |
| `TestWorker_TelemetrySeqIncreasesAndRetryUsesNewSeq` (planned test) | `seq` starts at 1 and rises by one per report; a retry after a timeout uses a new `seq`; a 409 starts a new session at `seq` 1 and the rejected body is not resent. |
| `TestWorker_Node404_LegacyNoRetry` (planned test) | 404 on node register: one log line, worker registers without `node_id`, no further node request for the life of the process. |
| `TestWorker_Node429_RegistersWithNodeIDAndRetriesAfter` (planned test) | 429 with `Retry-After`: the worker registers itself with `node_id` and retries the hardware registration only after the delay. |
| `TestConfig_NodeReportingEnabled_StrictParse` (planned test) | Unset and empty are false; `true`, `TRUE`, `false` accepted; `1`, `yes`, `on`, ` true`, `ture` each return an error naming the key and the value. |
| `TestConfig_NodeStateDir_MustBeAbsolute` (planned test) | A relative path is a startup error when reporting is on; a set but invalid path is not examined when reporting is off. |
| `TestConfig_NodeIntervals_Validation` (planned test) | `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL` and `MINICLOUD_NODE_TELEMETRY_INTERVAL`: zero, negative, a number without a unit and garbage are startup errors naming the key. |
| `TestConfig_NodeLocalOnly_Table` (planned test) | Local-only for `127.0.0.1:8080`, `127.0.0.2:1` and `[::1]:8080`; not local-only for `0.0.0.0:8080`, `:8080`, `[::]:8080`, `localhost:8080`, `192.168.1.5:8080` and `example.com:8080`. |
| `TestAPI_NodeRoutes_NotServedWhenNotLocalOnly` (planned test) | With a non-loopback configured address, all four node routes return 404 and `GET /v1/workers` has no `node_id` key (absent, not null). |
| `TestAPI_RegisterWorker_NodeIDIgnoredWhenNotLocalOnly` (planned test) | A `node_id` in the worker register body is ignored (stored NULL, response unchanged) when not local-only, even for a registered node. |
| `TestAPI_NodeRoutes_IgnoreForwardedHeaders` (planned test) | `X-Forwarded-For`, `X-Real-IP` and `Host` values cannot make a non-local-only coordinator serve the routes or a local-only one stop serving them. |
| `TestAPI_ListNodes_SummaryOmitsSensitiveFields` (planned test) | `GET /v1/nodes` carries none of the sensitive fields of Section 4.4; `GET /v1/nodes/{id}` carries them; `GET /v1/workers` and job events never do. |
| `TestAPI_RegisterNode_RateLimited_429RetryAfter` (planned test) | A changed hardware body inside the interval returns 429 `RATE_LIMITED` with an integer `Retry-After` and an opaque body; the same body after the interval returns 200. |
| `TestAPI_NodeTelemetry_OutOfOrder409` (planned test) | Same session and a lower or equal `seq`: 409 `TELEMETRY_OUT_OF_ORDER`, the stored sample unchanged; a missing or malformed `session_id` or `seq`: 400 `INVALID_REQUEST`. |
| `TestAbuse_Node_ForgedSequence` (planned test) | Documents T11: a huge `seq` in the honest session rejects later honest reports; a new session recovers; a new session also overwrites any stored sample. Expected to **pass** (the attack works) until V3-2; it pins the documented limit. |
| `TestWorker_NodeRegisterFails_FallsBackToLegacy` (planned test) | Coordinator 5xx on node register: the worker still registers and runs jobs, then attaches after a retry succeeds. |
| `TestAbuse_Node_ImpersonationByKnownID` (planned test) | Documents T1 and SEC-070: on a local-only configuration a second caller with a known ID can change that node's hardware version (after the interval). Expected to **pass** (the attack works) until V3-2; it pins the documented limit, not a fix. |
| `TestAbuse_Node_HostileStrings` (planned test) | Control characters and ANSI escapes in `display_name` and `cpu_model` are rejected with 400. |
| `TestAbuse_Node_HardwareChurnBounded` (planned test) | T3 over HTTP: alternating contents never leave more than 10 rows, and the rate limit rejects the alternation inside the interval. |

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
| Headroom (memory) | `TestConformance_NodeReserve_MemoryHeadroomMaxOfMinAndPercent` (planned test): the larger of the minimum and the percentage reserve applies; fresh telemetry with `memory_available_bytes - reserve < d` refuses; exact fit admits. |
| Headroom (CPU) | `TestConformance_NodeReserve_CPUPressureRefuses` (planned test): load-average estimates above `max_utilization_percent`, or fewer idle cores than `d + min_idle_cores`, refuse. |
| Headroom never raises | `TestConformance_NodeReserve_HeadroomAndTelemetryNeverRaiseCapacity` (planned test): no combination of telemetry values admits a demand that `reserved + d <= capacity - headroom` refuses. |
| Settings | `TestConfig_ResourceHeadroom_Validation` (planned test): each variable of Section 7.7 at its edge and one past it, a wrong strategy string, and "unset means not configured, so non-zero demand is refused". |

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
| N10 | Drop the retention prune, or keep 11 rows (off by one) | `TestConformance_Node_HardwareHistoryKeepsLatest10` (planned) |
| N11 | Ignore cgroup `memory.max` | `TestProbeLinux_CgroupLowersMemory` (planned) |
| N13 | Drop the hardware rate limit | `TestConformance_Node_HardwareRateLimit_RejectsChangeInsideInterval` (planned), `TestAPI_RegisterNode_RateLimited_429RetryAfter` (planned) |
| N14 | The rate limit also rejects an unchanged-hash registration | `TestConformance_Node_HardwareRateLimit_UnchangedNeverLimited` (planned) |
| N15 | A rejected (429) registration still writes `display_name` or `last_seen_at` | `TestConformance_Node_HardwareRateLimit_RejectsChangeInsideInterval` (planned) |
| N16 | A state directory alone counts as consent (reporting starts when only `MINICLOUD_NODE_STATE_DIR` is set) | `TestWorker_StateDirSetButReportingOff_NothingReadCreatedOrSent` (planned) |
| N17 | Reporting is on by default when the variable is unset | `TestWorker_ReportingOff_LegacyRegistration` (planned), `TestConfig_NodeReportingEnabled_StrictParse` (planned) |
| N18 | The worker reads or creates the state directory before checking the enabled flag | `TestWorker_StateDirSetButReportingOff_NothingReadCreatedOrSent` (planned) |
| N19 | No home directory and no state dir silently falls back to legacy instead of an error | `TestWorker_ReportingOn_NoHomeNoDir_StartupError` (planned) |
| N20 | The telemetry upsert is unconditional (the `WHERE` of Section 3.3 is dropped) | `TestConformance_Node_Telemetry_OutOfOrderRejected` (planned) |
| N21 | `<` becomes `<=` in the accept test, so an equal `seq` is accepted | `TestConformance_Node_Telemetry_ReplaySameSeqRejected` (planned) |
| N22 | A different `session_id` is rejected, or the session check is removed (a restart locks the node out) | `TestConformance_Node_Telemetry_NewSessionAcceptedAfterRestart` (planned) |
| N23 | A rejected report still updates `received_at` or any stored column | `TestConformance_Node_Telemetry_OutOfOrderRejected` (planned) |
| N24 | The sequence check is a read in Go followed by a separate write (not atomic) | `TestPostgresRepository_Node_TelemetrySeq_ConcurrentSameSession_OneWins` (planned) |
| N25 | The Linux probe returns `gpu_count: 0` | `TestProbeLinux_GPUFieldsAlwaysNil` (planned) |
| N26 | The local-only decision is taken per request from a header | `TestAPI_NodeRoutes_IgnoreForwardedHeaders` (planned) |
| N27 | `0.0.0.0`, an empty host or a host name counts as loopback | `TestConfig_NodeLocalOnly_Table` (planned) |
| N28 | `node_id` is still served in `GET /v1/workers`, or the node routes are still served, when not local-only | `TestAPI_NodeRoutes_NotServedWhenNotLocalOnly` (planned) |
| N29 | `GET /v1/nodes` includes a sensitive field | `TestAPI_ListNodes_SummaryOmitsSensitiveFields` (planned) |
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
| R11 | Headroom is added to capacity instead of subtracted, or fresh telemetry free memory is used to exceed the capacity bound | `TestConformance_NodeReserve_HeadroomAndTelemetryNeverRaiseCapacity` (planned) |
| R12 | The reserve uses the minimum only, or the percentage only, instead of the larger | `TestConformance_NodeReserve_MemoryHeadroomMaxOfMinAndPercent` (planned) |
| R13 | An unset headroom setting is treated as zero headroom | `TestConfig_ResourceHeadroom_Validation` (planned) |
| N12 | A foreign key to `nodes` uses `ON DELETE CASCADE` (or no action) instead of `RESTRICT` | `TestSchemaInvariants_NodeDeleteRestrictedWhileReferenced` (planned) |

## 15. Implementation slices and owned files

Each slice needs an owner-approved task entry before code (`docs/post-v1/ROADMAP.md` sequencing rule). Paths of files that do not exist yet are written without code formatting. All rows [Proposed].

| Slice | Scope | Owned files |
|---|---|---|
| V2-1a | Contract types and validation; platform adapter (Linux and fallback); node ID file; architecture test | internal/domain/node.go and node_test.go (new); internal/platform/ probe.go, probe_linux.go, probe_other.go, nodeid.go and their tests (new); `tests/architecture/boundaries_test.go` |
| V2-1b | Migration 000007; PostgreSQL and in-memory node storage; conformance suite | internal/storage/postgres/migrations/000007_node_capability_model.sql (new); internal/storage/postgres/nodes.go and migration_000007_test.go (new); `internal/storage/postgres/repository.go` (`UpsertWorker`, `GetWorker`, `ListWorkers`); `internal/storage/postgres/migrator_test.go` (last-migration pin); internal/storage/inmemory_nodes.go (new); `internal/storage/inmemory.go`; `internal/storage/repository.go`; internal/storage/storagetest/nodes.go (new); reserved touch points `internal/domain`, `internal/storage/storagetest`, migration number 000007 |
| V2-1c | API routes and coordinator methods (local-only decision at startup, rate limit, 429 and 409 codes); docs | internal/api/nodes.go (new); `internal/api/handlers.go`; `internal/api/server.go`; internal/coordinator/nodes.go (new); `internal/coordinator/service.go`; `internal/config/config.go` (`MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`, the derived local-only flag); `docs/protocol.md`; `docs/schema.md`; `docs/security/field-limits.md` |
| V2-1d | Worker reports its node (opt-in flag, state directory default, telemetry session and sequence, interval) | internal/worker/node.go (new); `internal/worker/client.go`; `internal/config/config.go` (`MINICLOUD_NODE_REPORTING_ENABLED`, `MINICLOUD_NODE_STATE_DIR`, `MINICLOUD_NODE_TELEMETRY_INTERVAL`); `.env.example` |
| V2-1e | Abuse tests only. SEC-070 is already registered (Section 13), so this slice no longer writes `docs/security/findings.md` | tests/abuse/nodes_test.go (new) |
| V2-3 | Reservations, headroom and placement (Sections 7 and 7.7; reads the `MINICLOUD_RESOURCE_*` settings; owns the worker-declines design, Section 16 item D-1). Its migration adds `attempts.node_id`, `jobs.assigned_node_id` and the demand snapshot columns (with the OD-24 job fields), so V2-1's tables need no change | its own ADR and task; not part of V2-1 |
| V4-3 | `node_tools` | its own task |

Builder: Claude Code for V2-1b and V2-1c (migration, repository, locking); V2-1a, V2-1d and V2-1e may go to either agent under `AGENTS.md` section 7. [Proposed]

## 16. Open items and owner questions

### Answered by the owner (2026-11-20, `docs/agents/handoffs/V2-1-owner-answers.md`)

| Was | Owner decision | Where carried out |
|---|---|---|
| OQ-1 | **Answered.** Keep the latest 10 hardware records per node; no new record when the hardware is unchanged; validate and rate-limit updates. [Decided: owner answer Q1, 2026-11-20] | 3.2, 4.1, 8.1, T3 |
| OQ-2 | **Answered.** Node reporting is explicit opt-in through `MINICLOUD_NODE_REPORTING_ENABLED`; a state directory setting alone is not consent; default directory `~/.minicloud` when enabled. [Decided: owner answer Q2, 2026-11-20] | 4.5, 5 |
| OQ-3 | **Answered.** The telemetry interval is configurable and has no default until the OD-23 benchmark; each report has a sequence number; out-of-order reports are rejected; old telemetry reads as unknown; telemetry is never a reservation. [Decided: owner answer Q3, 2026-11-20] | 3.3, 4.2, 9, T11 |
| OQ-4 | **Answered.** GPU detection is deferred; the V2-1 probe reports GPU fields as unknown and never 0. [Decided: owner answer Q4, 2026-11-20] | 6.3, 10 |
| OQ-5 | **Answered.** Dynamic, OS-aware headroom instead of a fixed reserve; V2-1 defines the model and its configuration, V2-3 builds it; the worker makes the final decision before starting a job. [Decided: owner answer Q5, 2026-11-20] | 7.7 |
| OQ-6 | **Answered.** Node endpoints are available only in a local-only configuration; no unauthenticated exposure to network clients; sensitive hardware detail restricted. [Decided: owner answer Q6, 2026-11-20] | 4.4, T1, T12 |
| OQ-7 | **Answered.** SEC-070, linked to SEC-004, registered by task V2-1-ADR-FIX2. [Decided: owner answer Q7, 2026-11-20] | 13, 15 |

### Still open, not answered here

- **OD-20** workers per node and the agent-process model (before V3-3). The design works for any number of workers per node.
- **OD-21** score function, **OD-22** smoothing and hysteresis, **OD-24** job constraint fields (they define demand, Section 7.2), **OD-25** first non-Linux target.
- **OD-23** time-to-live and interval values. This ADR sets no telemetry TTL, no telemetry interval default and no interval bounds. How they will be derived [Proposed]: (1) V2-1d makes the telemetry report interval a worker setting and records `received_at` for each report; (2) a measurement run, with the V2-2 harness or a scenario added to it, records the distribution of gaps between consecutive `received_at` values per node and the reported `sample_age_ms`, on the recorded environment, with real numbers; (3) the owner sets the telemetry TTL, the default interval and its bounds from those measured values (for example from a high percentile of the gap plus the measured sample age) and records them in an ADR amendment; (4) until then V2-3 cannot use telemetry as a constraint, which is safe because telemetry never guarantees capacity (Section 7.3). The hardware record TTL is derived the same way from the measured node re-registration interval. The headroom example values of Section 7.7 are benchmarked in the same way before any default exists.
- **Coordinator availability model and target scale.**
- **OQ-8** (new): the value and bounds of `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`. The limit value is a setting. This ADR proposes the placeholder default `60s`, **placeholder, not measured**; the owner should confirm it, replace it, or choose "no default, must be set". Also open: its upper bound.
- **D-1 (V2-3 design item): the worker makes the final decision before starting a job.** The direction is Decided (owner answer Q5); the mechanism is a new protocol and lifecycle design that V2-3 owns and this ADR does not design. Questions to answer: (1) what state does a claimed attempt reach when the worker declines, and which endpoint or field reports the decline; (2) does a decline consume a retry (`max_attempts`, ADR-0015) or not, and how is a worker that declines every job kept from starving a job; (3) how does a decline release node demand, given that demand is derived from the attempt row (Section 7.5), and which event does it write; (4) how does it interact with ADR-0019 (slots and concurrency: the slot is freed) and ADR-0020 (does a decline count as a lapse toward `MINICLOUD_ASSIGNMENT_MAX_LAPSES` and the fifo fallback, and how is the job kept from being re-offered to the same worker at once); (5) which input and which settings the worker uses for its own check, since the coordinator's telemetry is only an estimate; (6) how the coordinator's headroom test and the worker's final test avoid counting the same running job twice (`MemAvailable` already reflects running jobs, `reserved` also counts them).

## 17. Consequences

- V2 gains a node model without changing any V1 request, response, status code or dispatch decision. [Proposed]
- The reservation design reuses the V1 rule of derived capacity, so release has no separate step that a crash could skip. [Proposed]
- Before V3-2, node data is only as trustworthy as any caller that can reach the loopback address. V2-3 placement built on it inherits that limit. [Decided: OD-19, SEC-004]
- Node reporting is opt-in and only works against a coordinator whose `MINICLOUD_HTTP_ADDR` is a loopback address; on any other address the node features are off and every worker is a legacy worker until V3-2 adds authentication. [Decided: owner answers Q2 and Q6]
- Identity, Observation and Authority stay separate: a UUID identifies, telemetry describes, authentication (V3-2) establishes trust. [Decided: owner principle]
- Non-Linux machines report `os` and `arch` and nothing else until an adapter for them is built and tested. [Decided: OD-08]
