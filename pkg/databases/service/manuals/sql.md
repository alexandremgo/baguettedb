# SQL capabilities and engine rules

Each bdb is a SQLite database. Check the exact engine version with `SELECT sqlite_version()`; it is
guaranteed to be at least 3.45, so the JSON1 function set, JSONB, and FTS5 are all built in.

## Identifier rules

Every created or renamed-to name (tables, columns, indexes, views, triggers) must start with an ASCII
letter and contain only ASCII letters, digits, and underscores, at most 128 bytes. SQL keywords (e.g.
`order`) are allowed — generated SQL always quotes identifiers — but best avoided, since every later
hand-written query has to double-quote them.

## Reserved `_` prefix (platform tables)

A leading underscore marks a platform-reserved name — the identifier rules exclude it, so user schema
can never take one. Platform tables (e.g. `_change_log`, the per-user undo/redo stacks, skills storage)
live in the same database: **reads on them are always allowed** (audit visibility — `SELECT` away), but
writes, DDL, and creating new `_`-prefixed objects are denied. SQLite's own `sqlite_*` internals are
likewise off-limits; `sqlite_master` remains readable for schema introspection. A trigger body is checked
when the trigger fires, so a trigger that writes a platform table fails at the statement that fires it,
not at creation. Two more statement shapes are refused: `CREATE TEMP ...` (temporary objects would live
on the shared connection, not in the bdb — use a CTE for intermediate results or a regular table for
scratch data) and `REINDEX` (index maintenance is the platform's job).

## JSON support

JSON values are stored as TEXT — SQLite has no JSON column type. Declare JSON-holding columns TEXT and
validate with a CHECK constraint:

```sql
CREATE TABLE items (data TEXT CHECK (json_valid(data)))
```

- The full JSON1 function set is available: `json`, `json_extract`, `json_object`, `json_array`,
  `json_set` / `json_insert` / `json_replace` / `json_remove` / `json_patch`, `json_each`, `json_tree`,
  `json_valid`, `json_array_length`, and the `->` / `->>` arrow operators.
- JSONB (`jsonb_*` variants) is available for compact binary storage; `json(...)` converts back to text.
- Expression indexes on extracted paths are supported and used by the query planner:

```sql
CREATE INDEX idx_items_field ON items (json_extract(data, '$.field'))
```

- Formula group params and multi-select lookup params bind as ONE JSON TEXT value — consume them with
  `json_each`, e.g. `WHERE m.id IN (SELECT je.value FROM json_each(:share_among) je)`.

## Full-text search

FTS5 is compiled in: `CREATE VIRTUAL TABLE docs_fts USING fts5(title, body)` works, including BM25
ranking. Caveat: virtual tables are captured only at the DDL level in the change log — direct row
writes into them bypass it, and undoing a DROP restores an empty vtable to be rebuilt from its source.

## Change log semantics

Every write — `{{.ToolExecuteData}}`, `{{.ToolExecuteSchema}}`, and formula actions — is captured in `_change_log`,
grouped by `batch_id`: one request (or one formula action run) is one atomic batch and one undo entry.
Entries carry a kind (`DML` or `DDL`). Undo/redo stacks are per-user; use `{{.ToolListChanges}}`,
`{{.ToolUndoLastChange}}` / `{{.ToolRedoLastChange}}`, and `{{.ToolRevertChange}}` (any user's entry, by `change_log_id` or
`batch_id`) rather than editing platform tables — which is denied anyway. Owner-only
`{{.ToolUndoLastChangeAnyone}}` / `{{.ToolRedoLastChangeAnyone}}` operate across users.

BLOB values cannot be stored: the change log records each row's values as JSON, which has no binary type, so any
write carrying a non-NULL BLOB — into a column of any declared type — is refused with a clear error. Keep binary in
a TEXT column as base64 or hex. A BLOB produced by an expression in `{{.ToolExecuteQuery}}` (`x'00ff'`,
`zeroblob()`) is returned as base64.

Reversals are recorded like writes: an undo, redo, or revert produces entries carrying the statement that ran, so
`{{.ToolListChanges}}` shows a reversal by its effect (undoing `CREATE TABLE t` lists `DROP TABLE "t"`). A column
brought back by undoing a `DROP COLUMN` is rebuilt behind the scenes and recorded as the equivalent
`ALTER TABLE … ADD COLUMN`, constraints included. Every write needs free space under the bdb's size limit, a reversal
that re-adds data (a restored column or table) included: once the bdb is full the write is refused with a clear
error — delete rows or drop tables first.

## Statement limits

The values below are the limits that apply to you on this instance.

- `{{.ToolExecuteQuery}}`: a single read-only SELECT; multi-statement input is not supported.
{{- if .MaxQueryResultRows}} At most
  {{.MaxQueryResultRows}} result rows — a larger result is refused, so add a LIMIT or aggregate; the same cap
  applies to `{{.ToolRunFormula}}` results, where a view paginates with `:cursor`/`:limit` or exports as CSV
  instead.
{{- else}} Results are not capped on
  this instance; on a large table, still add a LIMIT or aggregate, and page a `{{.ToolRunFormula}}` view with
  `:cursor`/`:limit` or export it as CSV.
{{- end}}
- `{{.ToolExecuteData}}`: {{if .MaxStatementsPerRequest}}up to {{.MaxStatementsPerRequest}}{{else}}any number of{{end}} DML statements per request, {{if .MaxRowsPerRequest}}at most
  {{.MaxRowsPerRequest}} affected rows total{{else}}no cap on
  affected rows{{end}}, atomic (one transaction, one batch, one undo entry).
{{if .MaxConcurrentQueries -}}
- At most {{.MaxConcurrentQueries}} of your requests (queries, writes, schema changes, formula runs) run at the same
  time on an instance; one more is refused with a retry hint — wait for the running ones rather than firing them
  in parallel.
{{else -}}
- Your requests (queries, writes, schema changes, formula runs) have no concurrency cap on this instance.
{{end -}}
- `{{.ToolExecuteSchema}}`: {{if .MaxStatementsPerRequest}}up to {{.MaxStatementsPerRequest}}{{else}}any number of{{end}} DDL statements per request, atomic. `ALTER TABLE RENAME TO` is
  single-statement only, and a statement referencing an object created by an earlier statement in the
  same request (e.g. `CREATE TABLE foo; CREATE INDEX ... ON foo`) must be split into separate requests.
