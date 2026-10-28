-- Consolidated initial platform schema, version 8.
--
-- All statements use IF NOT EXISTS so a re-run on a database left in partial state (migration aborted
-- mid-apply on an earlier pass) succeeds without colliding on the already-present objects.
--
-- Per-table DML triggers against _change_log are not created here: they are generated in Go per user
-- table, and a fresh database has no user tables yet.

CREATE TABLE IF NOT EXISTS _change_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL DEFAULT '__pending__',
    kind TEXT NOT NULL CHECK(kind IN ('DML', 'DDL')),
    operation TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT 'change'
        CHECK(origin IN ('change', 'undo', 'redo', 'revert')),
    changed_by TEXT NOT NULL DEFAULT '__pending__',
    changed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    table_name TEXT,
    row_id TEXT,
    old_values JSON,
    new_values JSON,
    object_type TEXT,
    object_name TEXT,
    ddl_subtype TEXT,
    ddl_statement TEXT,
    ddl_metadata JSON
);
CREATE INDEX IF NOT EXISTS idx_change_log_kind_changed_at ON _change_log (kind, changed_at);
CREATE INDEX IF NOT EXISTS idx_change_log_table_changed_at ON _change_log (table_name, changed_at);
CREATE INDEX IF NOT EXISTS idx_change_log_changed_at ON _change_log (changed_at);
CREATE INDEX IF NOT EXISTS idx_change_log_batch_id ON _change_log (batch_id);

CREATE TABLE IF NOT EXISTS _change_snapshots (
    change_log_id INTEGER PRIMARY KEY REFERENCES _change_log(id) ON DELETE CASCADE,
    column_metadata JSON,
    dependent_objects JSON,
    row_payload JSON,
    row_count_total INTEGER,
    truncated INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS _undo_stack (
    position INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    user_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS _redo_stack (
    position INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    user_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_undo_stack_user_position ON _undo_stack (user_id, position);
CREATE INDEX IF NOT EXISTS idx_redo_stack_user_position ON _redo_stack (user_id, position);
CREATE INDEX IF NOT EXISTS idx_undo_stack_batch_id ON _undo_stack (batch_id);
CREATE INDEX IF NOT EXISTS idx_redo_stack_batch_id ON _redo_stack (batch_id);

CREATE TABLE IF NOT EXISTS _skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_skills_name ON _skills (name);

CREATE TABLE IF NOT EXISTS _skill_targets (
    skill_id TEXT NOT NULL,
    table_name TEXT NOT NULL,
    PRIMARY KEY (skill_id, table_name),
    FOREIGN KEY (skill_id) REFERENCES _skills(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_skill_targets_table ON _skill_targets (table_name);

-- display, hidden and label trail the timestamps: they were historically added by ALTER TABLE, and
-- existing databases keep that column order, so new databases are created with the same one.
CREATE TABLE IF NOT EXISTS _formulas (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    sql TEXT NOT NULL,
    kind TEXT NOT NULL,
    statement_verb TEXT NOT NULL,
    params TEXT NOT NULL DEFAULT '[]',
    attached_to_formula_id TEXT REFERENCES _formulas(id) ON DELETE CASCADE,
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    display TEXT,
    hidden INTEGER NOT NULL DEFAULT 0,
    label TEXT
);
CREATE INDEX IF NOT EXISTS idx_formulas_attached ON _formulas (attached_to_formula_id);
