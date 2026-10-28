-- Initial databases schema for the SQLite instance store, mirroring the Postgres one.
--
-- All statements use IF NOT EXISTS (and the control-row seed ON CONFLICT DO NOTHING) so a concurrent or
-- partial re-run succeeds without colliding on already-present objects. Types map from Postgres as: UUID ->
-- TEXT, TIMESTAMPTZ -> TIMESTAMP (written by Go in UTC; the driver reads it back as time.Time), BYTEA -> BLOB,
-- BOOLEAN / SMALLINT / BIGINT -> INTEGER. Timestamps carry no default: Go always supplies them.

-- encryption_key_wrapped: per-bdb DEK wrapped under the KEK (AES-256-GCM, nonce ‖ ciphertext ‖ tag);
-- every row must supply it explicitly. A bdb's database is owned by (pod_color, pod_shard): pod_shard is
-- the owning databases pod's ordinal, pod_color its blue/green fleet (0 = blue, 1 = green); a single-machine
-- store keeps both at 0. read_only, when 1, makes the database reject mutations and DDL while still serving
-- reads. size_bytes mirrors the on-disk size of the database's SQLite files (main file + WAL sidecar),
-- refreshed best-effort; size_updated_at stays NULL until the first refresh, so a never-measured database is
-- distinguishable from an empty one.
CREATE TABLE IF NOT EXISTS databases (
    id                      TEXT NOT NULL PRIMARY KEY,
    bdb_id                  TEXT NOT NULL UNIQUE,
    created_at              TIMESTAMP NOT NULL,
    updated_at              TIMESTAMP NOT NULL,
    platform_schema_version INTEGER NOT NULL DEFAULT 0,
    encryption_key_wrapped  BLOB NOT NULL,
    pod_shard               INTEGER NOT NULL DEFAULT 0,
    read_only               INTEGER NOT NULL DEFAULT 0,
    pod_color               INTEGER NOT NULL DEFAULT 0,
    size_bytes              INTEGER NOT NULL DEFAULT 0,
    size_updated_at         TIMESTAMP
);

-- Single-row control table naming the currently active fleet; the CHECK keeps it a singleton.
CREATE TABLE IF NOT EXISTS databases_control (
    id           INTEGER NOT NULL PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    active_color INTEGER NOT NULL DEFAULT 0
);
INSERT INTO databases_control (id, active_color) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;

-- database_exports: audit trail of user-facing data exports (full-database file downloads and formula CSV
-- exports). One row per attempt, inserted at start and finalized with its outcome; successful rows double as
-- the rolling-window export rate-limit source. Deliberately no FK to databases — audit rows outlive a deleted
-- database. Never stores exported content or key material.
CREATE TABLE IF NOT EXISTS database_exports (
    id          TEXT NOT NULL PRIMARY KEY,
    database_id TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    outcome     TEXT NOT NULL DEFAULT 'started',
    bytes       INTEGER NOT NULL DEFAULT 0,
    started_at  TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);

-- Serves the rate-limit window count: successes per (database, user, kind) newer than the window start.
CREATE INDEX IF NOT EXISTS idx_database_exports_window
    ON database_exports (database_id, user_id, kind, outcome, started_at);
