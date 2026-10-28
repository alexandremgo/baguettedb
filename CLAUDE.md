# BaguetteDB - Claude Code Instructions

## Security

- **Never** add API keys, secrets, passwords, tokens, or any sensitive information directly in the code base.
- Always use environment variables for sensitive values.
- If a new secret or credential is needed, add it as a field of `Config` in `pkg/common/config/config.go` (loaded from a `BAGUETTEDB_*` variable) and document it in `.env.example` with a placeholder value.

## Configuration & Environment Variables

- Always consider whether a hardcoded value (port, URL, timeout, feature flag, threshold, etc.) should be configurable via an environment variable.
- When adding a new environment variable:
  1. Add a descriptive comment explaining its purpose.
  2. Add it to `.env.example` with a sensible default or placeholder value.

## Comments

- Follow Go's standard godoc style: doc comments start with the identifier name, then a 3rd person singular verb.
  - Example: `// HashPassword hashes a plaintext password using bcrypt.`
- Comments should be descriptive and add value; do not add them if variable/function/class names are already self-explanatory.
- Don't reference documentation files from source comments. State the rationale inline — doc paths rot under refactor.
- Every package has exactly one package comment (`// Package x holds …`): what the package is, its layer role, what
  belongs in it. It sits above `package x` in the package's central file (its main type or entry point). A `doc.go`
  holds it only when it runs long, or when the package has no non-test file (an `integrationtest` package: Go ignores
  package comments in `_test.go` files, so its `doc.go` carries just the build tag, the comment and the package clause).
  A `cmd/*` main package's comment reads `// Command x …`.

## Go Best Practices

- Follow standard Go project layout and conventions.
- Format with `gofmt`; `golangci-lint fmt ./...` orders imports in three groups (standard library, third-party, this
  module).
- Handle errors explicitly. Discard one only on purpose, written `_ =` (a deferred `Rollback` or `Close`, a response
  write), never by ignoring the return value.
- Prefer returning errors over panicking.
- Use meaningful, idiomatic names (`camelCase` for unexported, `PascalCase` for exported).
- Keep functions short and focused on a single responsibility.
- Write table-driven tests where applicable.
- Use `context.Context` for cancellation and deadlines in I/O operations.
- Avoid global mutable state; prefer dependency injection.

## Interfaces

- The owner service takes the full port; any consumer using a minority of an interface declares its own small,
  unexported slice **next to the consumer struct** (not in `port.go`). Slices redeclare methods, never embed.
- `pkg/<app>/service/port.go` holds only multi-stakeholder contracts: cross-app `*App` ports, extension-point types
  named by `SetupOptions`/`ExtDeps`, and interfaces named by multiple declarations. An app with none has no `port.go`.
- Interfaces are unexported by default. Export only what an external package must *name*, with a one-line
  justification comment at the declaration.
- Slice interfaces of **behavior**; collapse interfaces of **data** — a config/policy lookup is a func type returning
  a value struct, not an N-getter interface.
- A service writing through several repositories in one transaction declares an exported struct of the narrow,
  unexported interfaces it uses (`UserTx`) and takes a `RunInTx[T]` func; `pkg/common/database` offers a generic
  `PostgresInTx` / `SQLiteInTx`, each backend adapter offers repository constructors over the transaction, and the
  app's `store.go` composes them per consumer. A change to one aggregate starts that transaction by loading it with a
  lock (`FindByIDForUpdate`), decides in the domain, and writes only the delta. A rule spanning aggregates
  (`CreateOwned(ctx, bdb, maxOwned)`, under a per-owner lock) stays one coarse adapter method. No `UnitOfWork`.
- No implementation may panic-stub a method to satisfy a port — if one must, the port is too wide: split it.
- Test doubles implement the slice a test needs, not the full port.
- Cross-app transfer shapes live once in `pkg/common/apptypes` (tagged structs, no behavior; only shapes that cross
  an app boundary through a `*App` port); the providing app owns the domain→apptypes converters in `pkg/<app>/appconv`.
- See `.claude/skills/ports-and-adapters/SKILL.md` for the full conventions, patterns, and checklist.

## Domain vs DTO Separation

- **Domain types** (`pkg/*/domain/`) must not have `json`, `xml`, or other serialization tags. They represent internal state and business rules.
- **DTOs** (Data Transfer Objects) live in the API layer (`pkg/*/api/dto.go`) and handle serialization. They have `json` tags and conversion functions from/to domain types.
- When adding a new domain type that will be exposed via REST or MCP, create a corresponding DTO in `api/dto.go` with a `to*Response` conversion function.
- The MCP layer (`pkg/*/mcp/`) should also convert domain types to DTOs (or reuse the REST DTOs) before marshaling to JSON.
- Shapes that cross an app boundary through an `*App` port live once in `pkg/common/apptypes` (tagged, behavior-free); the
  providing app converts to them in its `appconv` package, and api/mcp layers (the provider's, or a consumer's that
  answers with exactly what the provider hands it) may reuse them for responses that are shape-identical to the
  contract instead of redeclaring a mirror.

## Platform Table Migrations

When modifying platform tables (`_change_log`, `_change_snapshots`, `_undo_stack`, `_redo_stack`, `_skills`, `_skill_targets`, `_formulas`):

1. Generate the migration with `migrate create -ext sql -dir pkg/databases/migrations/platform -seq <name>` (do not hand-create the files), then delete the generated `.down.sql`: migrations are forward-only
2. **Increment `CurrentPlatformSchemaVersion`** in `pkg/databases/service/database.go` to the new file's number (the loader reads each version from its file name)
3. Update triggers in `pkg/databases/domain/trigger_generator.go` if `_change_log` schema changed — existing user tables need a post-migration hook (`postMigrationHook` in `pkg/databases/service/platform_migrate_hooks.go`, and its test) to regenerate their per-table triggers
4. Migrations must be idempotent (`IF NOT EXISTS` / `IF EXISTS`), so a re-run on a database left half-migrated succeeds

## Running gopls on Integration Tests

Files with `//go:build integration` require the build tag to be recognized by gopls:

```sh
GOFLAGS="-tags=integration" gopls check <file>
```

## Web Frontend Embedding

- `pkg/webui` embeds `pkg/webui/dist` (`go:embed all:dist`), a copy of `web/dist` made by `make webui-sync`. Only
  the `.gitkeep` placeholder is tracked, so on a fresh checkout `make build` yields an API-only binary (it embeds
  whatever an earlier sync left there) and `make build-ui` (`web-build` + `webui-sync` + `build`) one that serves the
  SPA too. `make build-cli` / `build-cli-ui` do the same for the `bdb` CLI, and `make release-cli` always builds the
  full CLI.
- New SPA top-level routes must not collide with a backend prefix (`/api/`, `/oauth2/`, `/login`, `/consent`,
  `/.well-known/`, `/authorize`, `/token`, `/register`, `/userinfo`, `/mcp`, `/healthz`, `/static/`, `/internal/`):
  the fallback in `pkg/webui` refuses the app shell under those, by plain prefix match (so `/tokens` is refused too),
  and the service-worker denylist in `web/pwaConfig.ts` must cover them as well.

## Third-Party Notices

- `THIRD_PARTY_LICENSES` is generated: never edit it by hand. After any dependency change (`go.mod`,
  `web/package.json`, a vendored or statically linked native library), run `make notices` and commit the result; CI
  fails when it is stale. A vendored or statically linked native library also needs its license under
  `third-party/<name>/` and an entry in `natives` (`cmd/third-party-notices/native.go`). A license outside the
  allowlist or a partly recognized license file fails the run: see DEVELOPMENT.md, "Third-party notices", before
  touching `allowedLicenses` or `overrides` (`cmd/third-party-notices/policy.go`).

## Line Length

Wrap lines (code and comments) at **120 characters**, enforced over the whole codebase by the `lll` linter in
`.golangci.yml`. Use the full 120 — don't wrap at 80. Wrap only when a line would exceed 120. A string literal too
long for one line splits into two string literals joined with `+` (the value stays the same), preferably before an SQL
keyword or after punctuation.

Files excluded from `lll` (large prompt-string literals kept single-line —
hand-wrapping risks altering the prompt at a seam):
`pkg/bdbs/mcp/bdb.go`, `pkg/mcpgateway/gateway.go`.

## Pre-Commit Checks

Before considering a task complete, always run:

1. `gofmt -l .` — fix any formatting issues with `gofmt -w .`
2. `golangci-lint run ./...` — fix all linter errors (or run `make lint`)

Do not commit code that fails either check.
