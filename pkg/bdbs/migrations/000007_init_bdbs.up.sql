-- Consolidated initial bdbs schema, version 7.
--
-- All statements use IF NOT EXISTS so a concurrent or partial re-run succeeds without colliding on
-- already-present objects.

-- deleted_at, starter_key and starter_version trail the original columns: they were historically added by
-- ALTER TABLE, and existing databases keep that column order, so new databases are created with the same one.
CREATE TABLE IF NOT EXISTS bdbs (
    id              UUID PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ NULL,
    starter_key     TEXT    NULL,
    starter_version INTEGER NULL
);

-- Partial index keeps the hourly purge sweep cheap regardless of total bdb count.
CREATE INDEX IF NOT EXISTS idx_bdbs_deleted_at
    ON bdbs (deleted_at)
    WHERE deleted_at IS NOT NULL;

-- display_order trails for the same ALTER-history reason: per-user position of the bdb in the owning
-- user's list (lower sorts first; a new membership takes MIN - 1 so it lands at the top).
CREATE TABLE IF NOT EXISTS bdb_members (
    bdb_id        UUID NOT NULL REFERENCES bdbs(id) ON DELETE CASCADE,
    user_id       UUID NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    display_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bdb_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_bdb_members_user_id ON bdb_members(user_id);

CREATE TABLE IF NOT EXISTS bdb_invites (
    id          UUID PRIMARY KEY,
    bdb_id      UUID NOT NULL REFERENCES bdbs(id) ON DELETE CASCADE,
    token       TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer')),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked')),
    created_by  UUID NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_by UUID,
    accepted_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bdb_invites_token ON bdb_invites(token);
CREATE INDEX IF NOT EXISTS idx_bdb_invites_bdb_id ON bdb_invites(bdb_id);
