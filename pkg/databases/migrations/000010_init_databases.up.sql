-- Consolidated initial databases schema, version 10.
--
-- All statements use IF NOT EXISTS (and the control-row seed ON CONFLICT DO NOTHING) so a concurrent or
-- partial re-run succeeds without colliding on already-present objects.

-- The columns after updated_at trail in their historical ALTER TABLE order, so new databases are created
-- with the same column order existing databases have.
--
-- encryption_key_wrapped: per-bdb DEK wrapped under the KEK (AES-256-GCM, nonce ‖ ciphertext ‖ tag);
-- every row must supply it explicitly. A bdb's database is owned by (pod_color, pod_shard): pod_shard is
-- the owning databases pod's ordinal, pod_color its blue/green fleet (0 = blue, 1 = green). read_only,
-- when true, makes the database reject mutations and DDL while still serving reads (set during a
-- rebalance move's drain window, cleared once the file is adopted on its new pod). size_bytes mirrors the
-- on-disk size of the database's SQLite files (main file + WAL sidecar), refreshed best-effort;
-- size_updated_at stays NULL until the first refresh, so a never-measured database is distinguishable
-- from an empty one.
CREATE TABLE IF NOT EXISTS databases (
    id                      UUID PRIMARY KEY,
    bdb_id                  UUID NOT NULL UNIQUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    platform_schema_version INTEGER NOT NULL DEFAULT 0,
    encryption_key_wrapped  BYTEA NOT NULL,
    pod_shard               SMALLINT NOT NULL DEFAULT 0,
    read_only               BOOLEAN NOT NULL DEFAULT false,
    pod_color               SMALLINT NOT NULL DEFAULT 0,
    size_bytes              BIGINT NOT NULL DEFAULT 0,
    size_updated_at         TIMESTAMPTZ
);

-- Single-row control table naming the currently active fleet. The deploy orchestrator flips active_color
-- at cutover; the one-row CHECK keeps it a singleton.
CREATE TABLE IF NOT EXISTS databases_control (
    id           BOOLEAN PRIMARY KEY DEFAULT true,
    active_color SMALLINT NOT NULL DEFAULT 0,
    CONSTRAINT databases_control_single_row CHECK (id)
);
INSERT INTO databases_control (id, active_color) VALUES (true, 0) ON CONFLICT (id) DO NOTHING;

-- database_exports: audit trail of user-facing data exports (full-database file downloads and formula CSV
-- exports). One row per attempt, inserted at start and finalized with its outcome; successful rows double as
-- the rolling-window export rate-limit source. Deliberately no FK to databases — audit rows outlive a deleted
-- database. Never stores exported content or key material.
CREATE TABLE IF NOT EXISTS database_exports (
    id          UUID PRIMARY KEY,
    database_id UUID NOT NULL,
    user_id     UUID NOT NULL,
    kind        TEXT NOT NULL,
    outcome     TEXT NOT NULL DEFAULT 'started',
    bytes       BIGINT NOT NULL DEFAULT 0,
    started_at  TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);

-- Serves the rate-limit window count: successes per (database, user, kind) newer than the window start.
CREATE INDEX IF NOT EXISTS idx_database_exports_window
    ON database_exports (database_id, user_id, kind, outcome, started_at);
