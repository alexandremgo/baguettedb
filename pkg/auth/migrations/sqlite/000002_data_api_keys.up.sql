-- Data API keys: access tokens of kind data_api_key, each bound to one bdb (bdb_id), that let an app of the user's run the
-- formulas picked for it. bdb_id is set for Data API keys and for no other kind. It has no foreign key, as on the Postgres
-- instance store, where bdbs live in another app's database. Data API keys are listed and revoked by bdb, hence the
-- partial index.
--
-- SQLite cannot change a table's CHECK constraints, so the table is rebuilt: created anew, filled from the old one,
-- which is then dropped, and renamed into place. No table references access_tokens, so dropping it touches no other
-- row. The migration runs in one transaction. Replayed by a second process that raced the first, it rebuilds the
-- table again from the rebuilt one; a Data API key created in between fails the copy's CHECK, and the replay rolls back
-- with nothing lost.
CREATE TABLE IF NOT EXISTS access_tokens_rebuilt (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('personal', 'session', 'data_api_key')),
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL,
    expires_at   TIMESTAMP NULL,
    last_used_at TIMESTAMP NULL,
    revoked_at   TIMESTAMP NULL,
    bdb_id       TEXT NULL,
    CHECK ((bdb_id IS NOT NULL) = (kind = 'data_api_key'))
);

INSERT INTO access_tokens_rebuilt
    (id, user_id, kind, name, token_prefix, token_hash, created_at, expires_at, last_used_at, revoked_at)
SELECT id, user_id, kind, name, token_prefix, token_hash, created_at, expires_at, last_used_at, revoked_at
FROM access_tokens;

DROP TABLE access_tokens;

ALTER TABLE access_tokens_rebuilt RENAME TO access_tokens;

CREATE INDEX IF NOT EXISTS idx_access_tokens_user_id ON access_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_access_tokens_bdb_id ON access_tokens (bdb_id) WHERE bdb_id IS NOT NULL;
