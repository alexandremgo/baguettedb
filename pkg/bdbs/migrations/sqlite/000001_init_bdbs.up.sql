-- Initial bdbs schema for the SQLite instance store, mirroring the Postgres one.
--
-- All statements use IF NOT EXISTS so a concurrent or partial re-run succeeds without colliding on
-- already-present objects. Types map from Postgres as: UUID -> TEXT, TIMESTAMPTZ -> TIMESTAMP (written by Go
-- in UTC; the driver reads it back as time.Time). Timestamps carry no default: Go always supplies them.
-- bdb_members.user_id and the invite user columns reference the auth app's users without a foreign key, as on
-- Postgres where the two apps live in separate databases.

CREATE TABLE IF NOT EXISTS bdbs (
    id              TEXT NOT NULL PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    deleted_at      TIMESTAMP NULL,
    starter_key     TEXT NULL,
    starter_version INTEGER NULL
);

-- Partial index keeps the hourly purge sweep cheap regardless of total bdb count.
CREATE INDEX IF NOT EXISTS idx_bdbs_deleted_at
    ON bdbs (deleted_at)
    WHERE deleted_at IS NOT NULL;

-- display_order: per-user position of the bdb in that user's list (lower sorts first; a new membership takes
-- MIN - 1 so it lands at the top).
CREATE TABLE IF NOT EXISTS bdb_members (
    bdb_id        TEXT NOT NULL REFERENCES bdbs(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    display_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bdb_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_bdb_members_user_id ON bdb_members(user_id);

CREATE TABLE IF NOT EXISTS bdb_invites (
    id          TEXT NOT NULL PRIMARY KEY,
    bdb_id      TEXT NOT NULL REFERENCES bdbs(id) ON DELETE CASCADE,
    token       TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer')),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked')),
    created_by  TEXT NOT NULL,
    expires_at  TIMESTAMP NOT NULL,
    accepted_by TEXT,
    accepted_at TIMESTAMP,
    created_at  TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bdb_invites_token ON bdb_invites(token);
CREATE INDEX IF NOT EXISTS idx_bdb_invites_bdb_id ON bdb_invites(bdb_id);
