-- MiniCloud node and capability model
-- Migration: 000007_node_capability_model.sql (ADR-0022 Sections 3 and 12, task V2-1b)
-- Forward-only (ADR-0010). Adds nodes (identity only), node_hardware (versioned inventory),
-- node_telemetry (latest sample only) and the nullable workers.node_id. Every measurement column is
-- nullable: NULL means unknown, never zero. No node_tools table (V4-3) and no reservation columns
-- (V2-3). The worker_capacity view does not select node_id, so it is not recreated.
-- Rollback is code-only (ADR-0022 Section 12): the tables and the column stay.

-- 1. nodes: identity only.
CREATE TABLE nodes (
    id            TEXT PRIMARY KEY,
    display_name  VARCHAR(64) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_nodes_id_uuid CHECK (id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CONSTRAINT chk_nodes_display_name CHECK (octet_length(display_name) <= 64 AND display_name !~ '[[:cntrl:]]')
);

-- 2. node_hardware: versioned static inventory. The current row is the highest version.
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
    CONSTRAINT chk_node_hardware_version CHECK (version > 0),
    CONSTRAINT chk_node_hardware_agent_version CHECK (octet_length(agent_version) <= 64),
    CONSTRAINT chk_node_hardware_os_arch CHECK (os ~ '^[a-z0-9_]{1,32}$' AND arch ~ '^[a-z0-9_]{1,32}$'),
    CONSTRAINT chk_node_hardware_models CHECK (
        (cpu_model IS NULL OR cpu_model ~ '^[ -~]{1,128}$')
        AND (gpu_model IS NULL OR gpu_model ~ '^[ -~]{1,128}$')),
    CONSTRAINT chk_node_hardware_counts CHECK (
        (cpu_logical_cores IS NULL OR cpu_logical_cores BETWEEN 1 AND 2147483647)
        AND (cpu_physical_cores IS NULL OR cpu_physical_cores BETWEEN 1 AND 2147483647)
        AND (gpu_count IS NULL OR gpu_count BETWEEN 0 AND 2147483647)
        AND (cpu_logical_cores IS NULL OR cpu_physical_cores IS NULL OR cpu_logical_cores >= cpu_physical_cores)),
    CONSTRAINT chk_node_hardware_bytes CHECK (
        (memory_total_bytes IS NULL OR memory_total_bytes BETWEEN 0 AND 9007199254740991)
        AND (disk_total_bytes IS NULL OR disk_total_bytes BETWEEN 0 AND 9007199254740991)
        AND (gpu_vram_total_bytes IS NULL OR gpu_vram_total_bytes BETWEEN 0 AND 9007199254740991))
);

-- 3. node_telemetry: latest sample only, one row per node, written by a conditional upsert.
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
    CONSTRAINT chk_node_telemetry_seq CHECK (seq BETWEEN 1 AND 9007199254740991),
    CONSTRAINT chk_node_telemetry_load CHECK (cpu_load_1m_milli IS NULL OR cpu_load_1m_milli >= 0),
    CONSTRAINT chk_node_telemetry_bytes CHECK (
        (memory_available_bytes IS NULL OR memory_available_bytes BETWEEN 0 AND 9007199254740991)
        AND (disk_free_bytes IS NULL OR disk_free_bytes BETWEEN 0 AND 9007199254740991))
);

-- 4. workers.node_id: nullable, no default (no table rewrite). Existing rows read NULL (legacy).
ALTER TABLE workers ADD COLUMN node_id TEXT;

-- 5. The foreign key is added NOT VALID and then validated (as 000003 to 000005 do for constraints).
-- Every existing row is NULL, so validation passes.
ALTER TABLE workers ADD CONSTRAINT fk_workers_node
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE workers VALIDATE CONSTRAINT fk_workers_node;

-- 6. Partial index for node_id lookups.
CREATE INDEX idx_workers_node ON workers (node_id) WHERE node_id IS NOT NULL;
