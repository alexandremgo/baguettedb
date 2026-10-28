-- Access tokens: opaque bearer tokens a user creates for scripts and local tools (kind personal), or that a signed-in
-- browser or app holds (kind session). Only the SHA-256 of a token is stored; token_prefix keeps its first characters
-- after the kind's prefix in plain, so a listing can tell tokens apart. expires_at is null for a token that never
-- expires, and a revoked token keeps its row with revoked_at set.
--
-- IF NOT EXISTS lets a concurrent or partial re-run succeed.
CREATE TABLE IF NOT EXISTS access_tokens (
    id           UUID PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('personal', 'session')),
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NULL,
    last_used_at TIMESTAMPTZ NULL,
    revoked_at   TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_access_tokens_user_id ON access_tokens (user_id);

-- The account owning a local instance, recorded by ID so the owner can change their email without losing their bdbs.
-- At most one row (id = 1); none on an instance with no implicit owner.
CREATE TABLE IF NOT EXISTS instance_owner (
    id      SMALLINT PRIMARY KEY CHECK (id = 1),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
