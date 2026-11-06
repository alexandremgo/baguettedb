-- The formulas each Data API key of a bdb may run, one row per key and formula. key_id has no foreign key: the keys
-- live in the auth app's database. formula_id is the ID of a formula in the bdb's own database file, which no foreign
-- key can reach either; a row whose formula is gone is left until met, since running a missing formula fails. The
-- rows go with their bdb. The index answers which keys include a formula.
--
-- IF NOT EXISTS lets a concurrent or partial re-run succeed.
CREATE TABLE IF NOT EXISTS data_api_key_formulas (
    key_id     UUID NOT NULL,
    bdb_id     UUID NOT NULL REFERENCES bdbs(id) ON DELETE CASCADE,
    formula_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (key_id, formula_id)
);

CREATE INDEX IF NOT EXISTS idx_data_api_key_formulas_bdb_formula ON data_api_key_formulas (bdb_id, formula_id);
