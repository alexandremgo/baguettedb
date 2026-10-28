-- Consolidated initial auth schema, version 6.
--
-- All statements use IF NOT EXISTS so a concurrent or partial re-run succeeds without colliding on
-- already-present objects.

-- password_hash is nullable: users created via an external identity provider exist without a local
-- password. email_verified_at trails the original columns: it was historically added by ALTER TABLE, and
-- existing databases keep that column order, so new databases are created with the same one.
CREATE TABLE IF NOT EXISTS users (
    id                UUID PRIMARY KEY,
    email             TEXT NOT NULL UNIQUE,
    password_hash     TEXT,
    first_name        TEXT NOT NULL DEFAULT '',
    last_name         TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active         BOOLEAN NOT NULL DEFAULT TRUE,
    email_verified_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_user_id ON user_roles (user_id);

CREATE TABLE IF NOT EXISTS external_identities (
    id               UUID PRIMARY KEY,
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    provider_subject TEXT NOT NULL,
    email            TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, provider_subject)
);

CREATE INDEX IF NOT EXISTS idx_external_identities_user_id ON external_identities (user_id);

CREATE TABLE IF NOT EXISTS oidc_state (
    state           TEXT PRIMARY KEY,
    provider        TEXT NOT NULL,
    login_challenge TEXT NOT NULL,
    nonce           TEXT NOT NULL,
    code_verifier   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oidc_state_expires_at ON oidc_state (expires_at);
