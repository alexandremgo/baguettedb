-- Data API keys: access tokens of kind data_api_key, each bound to one bdb (bdb_id), that let an app of the user's run the
-- formulas picked for it. bdb_id is set for Data API keys and for no other kind. It has no foreign key: bdbs live in
-- another app's database in a split deployment. Data API keys are listed and revoked by bdb, hence the partial index.
--
-- Each constraint is dropped before it is added, and the column and index use IF NOT EXISTS, so a concurrent or
-- partial re-run succeeds.
ALTER TABLE access_tokens DROP CONSTRAINT IF EXISTS access_tokens_kind_check;
ALTER TABLE access_tokens
    ADD CONSTRAINT access_tokens_kind_check CHECK (kind IN ('personal', 'session', 'data_api_key'));

ALTER TABLE access_tokens ADD COLUMN IF NOT EXISTS bdb_id UUID NULL;

ALTER TABLE access_tokens DROP CONSTRAINT IF EXISTS access_tokens_bdb_id_check;
ALTER TABLE access_tokens
    ADD CONSTRAINT access_tokens_bdb_id_check CHECK ((bdb_id IS NOT NULL) = (kind = 'data_api_key'));

CREATE INDEX IF NOT EXISTS idx_access_tokens_bdb_id ON access_tokens (bdb_id) WHERE bdb_id IS NOT NULL;
