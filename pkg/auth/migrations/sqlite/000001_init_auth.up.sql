-- Initial auth schema for the SQLite instance store, mirroring the Postgres one.
--
-- All statements use IF NOT EXISTS so a concurrent or partial re-run succeeds without colliding on
-- already-present objects. Types map from Postgres as: UUID -> TEXT, TIMESTAMPTZ -> TIMESTAMP (written by Go
-- in UTC; the driver reads it back as time.Time), BOOLEAN -> INTEGER. Timestamps carry no default: Go
-- always supplies them.

-- password_hash is nullable: users created via an external identity provider exist without a local
-- password.
CREATE TABLE IF NOT EXISTS users (
    id                TEXT NOT NULL PRIMARY KEY,
    email             TEXT NOT NULL UNIQUE,
    password_hash     TEXT,
    first_name        TEXT NOT NULL DEFAULT '',
    last_name         TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL,
    updated_at        TIMESTAMP NOT NULL,
    is_active         INTEGER NOT NULL DEFAULT 1,
    email_verified_at TIMESTAMP NULL
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_user_id ON user_roles (user_id);

CREATE TABLE IF NOT EXISTS external_identities (
    id               TEXT NOT NULL PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    provider_subject TEXT NOT NULL,
    email            TEXT,
    created_at       TIMESTAMP NOT NULL,
    UNIQUE (provider, provider_subject)
);

CREATE INDEX IF NOT EXISTS idx_external_identities_user_id ON external_identities (user_id);

CREATE TABLE IF NOT EXISTS oidc_state (
    state           TEXT NOT NULL PRIMARY KEY,
    provider        TEXT NOT NULL,
    login_challenge TEXT NOT NULL,
    nonce           TEXT NOT NULL,
    code_verifier   TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    expires_at      TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oidc_state_expires_at ON oidc_state (expires_at);

-- Access tokens: opaque bearer tokens a user creates for scripts and local tools (kind personal), or that a signed-in
-- browser or app holds (kind session). Only the SHA-256 of a token is stored; token_prefix keeps its first characters
-- after the kind's prefix in plain, so a listing can tell tokens apart. expires_at is null for a token that never
-- expires, and a revoked token keeps its row with revoked_at set.
CREATE TABLE IF NOT EXISTS access_tokens (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('personal', 'session')),
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL,
    expires_at   TIMESTAMP NULL,
    last_used_at TIMESTAMP NULL,
    revoked_at   TIMESTAMP NULL
);

CREATE INDEX IF NOT EXISTS idx_access_tokens_user_id ON access_tokens (user_id);

-- The account owning a local instance, recorded by ID so the owner can change their email without losing their bdbs.
-- At most one row (id = 1); none on an instance with no implicit owner.
CREATE TABLE IF NOT EXISTS instance_owner (
    id      INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
