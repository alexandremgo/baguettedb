---
name: ports-and-adapters
description: Guides interface and adapter discipline in baguettedb. Use when defining or consuming service ports, adding cross-app dependencies, creating new adapters (HTTP, intraprocess), sharing cross-app transfer types, or wiring apps together (pkg/instance, cmd/*).
user-invocable: false
---

# Ports & Adapters in BaguetteDB

## Architecture Overview

BaguetteDB is a modular monolith with ports & adapters (hexagonal architecture). Each app (`auth`, `bdbs`, `databases`, `starters`) is self-contained with its own domain, services, adapters, and API layer.

Cross-app communication goes through **ports** (interfaces defined by the consumer) and **adapters** (implementations organized by transport type). Interfaces are satisfied structurally — an adapter never names the port it implements (it may import the port's package for the types in the port's signatures); only consumers, constructors, and wiring name interface types.

A driving adapter (`api/`, `mcp/`, another app's intraprocess adapter) calls the app's services, never its repositories, even for a plain read: the one-line pass-through service method (`UserService.GetProfile`) is the accepted cost of keeping storage reachable only through the services.

## Directory Structure

```
pkg/<app>/
├── domain/           # Domain types, errors, business rules — no serialization tags
├── service/          # Business logic + interfaces
│   ├── port.go       # MULTI-STAKEHOLDER contracts only (see rules below)
│   └── *.go          # Services; per-consumer interface slices live here, next to their consumer
├── appconv/          # domain → apptypes converters (only in apps that export vocabulary shapes)
├── appinput/         # request DTOs → service inputs, shared by the entry points that decode them
├── adapters/
│   ├── postgres/     # Persistence on a Postgres instance store
│   ├── sqlite/       # Persistence on a SQLite instance store
│   ├── intraprocess/ # In-process adapters for cross-app calls (monolith mode)
│   └── <app>http/    # HTTP adapters for cross-app calls (split-service mode)
├── api/              # HTTP handlers + router (+ internal_handler.go for service-to-service)
├── mcp/              # MCP tools, in apps that expose some
├── store.go          # Per-backend repositories and transaction compositions
└── setup.go          # Wiring: creates adapters, services, handlers
```

`pkg/common/apptypes/` holds the cross-app transfer vocabulary (see below). An app with no multi-stakeholder contracts has **no `port.go` at all** (`pkg/mcpgateway` is the precedent — don't grow layers or files an app doesn't need).

## Interface Discipline (the rules)

1. **The owner takes the full port; minority consumers slice.** A service that uses essentially all of an interface consumes it whole. Any consumer using a minority of it declares its own small, unexported interface **directly above its struct, in its own file** — not in `port.go`. Rule of thumb: uses less than ~half → slice.
2. **Slices redeclare methods, never embed** the wider interface — embedding re-imports the width.
3. **`port.go` holds only multi-stakeholder contracts**: cross-app `*App` ports, extension-point types named by `SetupOptions`/`ExtDeps`, and interfaces named by multiple declarations (e.g. returned by another exported type). Everything else lives next to its single consumer.
4. **Unexported by default.** Wiring works across packages with unexported interfaces (a constructor's unexported param type is callable from `setup.go`). Export only what an external package must *name* — and write a one-line justification comment at the declaration.
5. **Slice interfaces of behavior; collapse interfaces of data.** A dependency that only resolves configuration/policy values is a func type returning a value struct (e.g. `quota.Resolver func(roles []authz.Role) quota.Limits`), not an N-getter interface.
6. **Multi-repository transactions go through a per-consumer struct, not a unit of work.** A service that writes through several repositories in one transaction declares an exported struct of the narrow, unexported interfaces it uses (`UserTx{Users userWriter; Roles roleAssigner; …}`) and takes a `RunInTx[T]` func. `pkg/common/database` offers a generic `PostgresInTx[R]` / `SQLiteInTx[R]`, each backend adapter offers repository constructors over the transaction, and the app's `store.go` composes one func per consumer. A change to one aggregate starts that transaction by loading it with a lock (`FindByIDForUpdate`: `SELECT … FOR UPDATE` on Postgres, a plain read under SQLite's immediate transaction), runs the domain's checks on that fresh state, and writes only what changed (e.g. `domain.DiffMembers` of before and after): concurrent changes then run one after the other and none overwrites another. A rule spanning aggregates (e.g. `CreateOwned(ctx, bdb, maxOwned int)` enforcing a per-owner count under a per-owner lock: an advisory lock on Postgres, the immediate transaction on SQLite) stays a single coarse adapter method. There are no getter-style `UnitOfWork` interfaces. See "Transactions across repositories" below.
7. **Litmus test: no implementation may panic-stub a method to satisfy a port.** If one does, the port is too wide — split it.
8. **Test doubles implement the slice, not the full port.**

### Doc comments on ports

Every port and slice gets a doc comment stating its contract (inputs, outputs, sentinel errors, invariants). Never name implementers ("Satisfied by X") or callers — describe what the interface needs, not who fulfills or uses it. The one sanctioned meta-comment is the export justification (rule 4).

## Cross-App Transfer Vocabulary (`apptypes` + `appconv`)

Shapes that cross an app boundary through a `*App` port live once, in **`pkg/common/apptypes`** — tagged structs, no interfaces, no behavior.

- **Admission test**: a type belongs in `apptypes` only if it crosses an app boundary through a `*App` port. No request-side DTOs, no single-app types, no manifest/authoring schemas.
- `json` tags are correct *here* — this package is the wire vocabulary: the intraprocess adapter passes these structs by value, the HTTP adapter decodes internal-endpoint responses straight into them, and api/mcp layers may reuse them for responses that are shape-identical to the contract (the provider's, or a consumer's that answers with exactly what the provider hands it).
- Evolve additively (new fields with `omitempty` stay wire-compatible). A change here is a cross-app API change — review it as one.
- **Conversion is provider-owned**: an app that contributes shapes to `apptypes` owns a matching `pkg/<app>/appconv` package (imports its own types + `common/apptypes`; behavior-free field mappings). Consumers never convert — they receive `apptypes` values ready-made.
- **One file per providing app**: `apptypes/databases.go` holds what `pkg/databases` provides (`auth.go`, `bdbs.go` likewise); a new providing app gets its own file, paired 1:1 with its `appconv`. Files partition ownership, not names — the package namespace is flat, so a shape name must be unique across providers.

## Naming Conventions

### Ports and slices

- **Cross-app ports**: exported, named `{ExternalApp}App` (`BdbsApp`, `AuthApp`, `DatabasesApp`), in the consuming app's `service/port.go`, doc comment `// {Name} defines the subset of the {app} app needed by {this app}.` plus the export justification.
- **Aggregate/repository ports**: unexported, named after the aggregate (`bdbRepository`). Single stakeholder → declared next to the owner service; multiple stakeholders → `port.go`. Exported only when setup must name it to hold either storage backend's adapter (rule 4).
- **Consumer slices**: small role names describing the capability (`bdbFinder`, `snapshotStore`), unexported, next to their consumer.

Example of a cross-app port:

```go
// BdbsApp defines the subset of the bdbs app needed by databases.
// Exported so that setup.go can accept it as a parameter.
type BdbsApp interface {
    // CheckMemberPermission checks whether the user has the given database permission
    // on the bdb's database, based on their bdb role.
    CheckMemberPermission(
        ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
    ) (bool, error)

    // GetBdbOwner returns the bdb owner's user ID and global roles.
    GetBdbOwner(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, []authz.Role, error)

    // GetOwnedBdbIDs returns the IDs of every bdb the given user owns, including soft-deleted bdbs
    // still awaiting purge (their storage remains allocated until the purge removes it).
    GetOwnedBdbIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}
```

### Adapters

Adapters are organized by **transport type**, not by external app:

- `adapters/intraprocess/` — direct in-process calls (monolith mode). An app may instead keep one package per target app (`adapters/intraprocessbdbs/`, `adapters/intraprocessdatabases/` in starters, constructor `New`), so a binary compiles only the adapters it wires
- `adapters/<app>http/` (`bdbshttp`, `databaseshttp`, `startershttp`) — HTTP calls to other services (split-service
  mode); named after the app that owns them (the consumer), so the package neither shadows `net/http` nor needs an
  import alias
- `adapters/postgres/`, `adapters/sqlite/` — instance-store persistence
- `adapters/sqlitelocal/` — local SQLCipher-encrypted SQLite client

**File naming**: `<external_app>app.go` (e.g. `bdbsapp.go`, `authapp.go`). **Struct naming**: `{ExternalApp}App`.

### Intraprocess Adapter Pattern

Write an intraprocess adapter **only when it does real work**: aggregating several provider services behind one port, or converting provider domain values to `apptypes`. If the provider's service already satisfies the consumer's port structurally, **pass it directly in the wiring — no wrapper**. An adapter whose methods are pure one-line delegations with a redeclared interface is dead weight; delete it.

```go
package intraprocess

// AuthApp adapts the auth app's role and user services to the bdbs AuthApp port,
// converting user profiles to apptypes through auth's appconv.
type AuthApp struct {
    roleSvc *authservice.RoleService
    userSvc *authservice.UserService
}
```

### HTTP Adapter Pattern

The HTTP adapter calls the external app's **internal endpoint**, presenting the shared internal API secret, and decodes responses into `apptypes` structs (for vocabulary shapes) or locally-defined payload types (for app-specific results).

```go
package databaseshttp

// BdbsApp implements the databases BdbsApp port by calling the bdbs service's internal endpoints over HTTP.
type BdbsApp struct {
    baseURL           string
    internalAPISecret string
    client            *http.Client
}
```

### Internal Endpoints

Each app that provides a cross-app port exposes internal endpoints for service-to-service communication:

- Registered in `api/router.go` under `/internal/api/<app>/v1/...`
- Handled by `api/internal_handler.go`
- No JWT — guarded by the shared internal-secret header, and never publicly reachable: the monolith's route mounting skips `/internal/*`, and a split deployment must keep them off the public network
- POST with JSON body for all operations

```go
// Internal service-to-service routes — no JWT, but callers must present the shared internal API secret.
r.Group(func(r chi.Router) {
    r.Use(authhttp.RequireInternalSecret(internalAPISecret))
    r.Post("/internal/api/bdbs/v1/check-permission", internalHandler.CheckPermission)
    r.Post("/internal/api/bdbs/v1/bdb-owner", internalHandler.GetBdbOwner)
    r.Post("/internal/api/bdbs/v1/owned-bdb-ids", internalHandler.GetOwnedBdbIDs)
})
```

## Transactions across repositories

The consumer names what it writes through; the backend only knows how to open a transaction; `store.go` joins the two. Adapters never name the struct or its interfaces.

```go
// pkg/auth/service/user.go — the consumer
type UserTx struct {
    Users    userWriter     // unexported, 3 methods
    Roles    roleAssigner   // unexported, 1 method
    Sessions sessionRevoker // unexported, 1 method
}

func NewUserService(repo UserRepository, inTx RunInTx[UserTx], logger zerolog.Logger) *UserService

return s.inTx(ctx, func(tx UserTx) error {
    if err := tx.Users.Create(ctx, user); err != nil {
        return err
    }
    return tx.Roles.AssignRole(ctx, user.ID, authz.RoleUser)
})

// pkg/auth/service/port.go — the app's transaction func type (each app that needs one declares its own)
type RunInTx[T any] func(ctx context.Context, fn func(T) error) error

// pkg/common/database — one generic helper per storage engine, shared by every app
func PostgresInTx[R any](pool *pgxpool.Pool, build func(pgx.Tx) R) func(context.Context, func(R) error) error

// pkg/auth/store.go — one composition per consumer per backend
userTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.UserTx {
    return service.UserTx{
        Users:    postgres.NewUserRepo(tx),
        Roles:    postgres.NewRoleRepo(tx),
        Sessions: postgres.NewAccessTokenRepo(tx),
    }
}),
```

A struct field of interface type accepts the concrete repository, so the adapter's constructors return concrete types and Go's lack of covariant returns never forces an adapter to name a port. Test doubles hand a struct of fakes to a `RunInTx` that calls `fn` directly.

Two repository shapes follow from rule 6, and the rule says which to use:

- **Single-statement repositories take a `database.DBTX`** on Postgres or a `database.SQLiteDBTX` on SQLite (pool or transaction) and are reused as-is inside a transaction: auth's `NewUserRepo(tx)` above.
- **Repositories that open their own transactions stay pool-bound** (bdbs's `BdbRepo`, whose `CreateOwned` / `Update` guard rules spanning aggregates). The operations that only make sense inside a caller's transaction then live on a separate transaction-bound type: bdbs's `MembershipTxRepo` (`FindByIDForUpdate`, `ApplyMemberChanges`, `MarkInviteAccepted`).

## Wiring

Each app's `setup.go` wires that app alone, and its `Setup` accepts cross-app dependencies as the port interface type:

```go
// databases/setup.go
func Setup(ctx context.Context, cfg config.Config, logger zerolog.Logger, bdbsApp service.BdbsApp, opts SetupOptions) (*SetupResult, error)
```

When two apps depend on each other, one side receives its port after `Setup` instead (bdbs's `SetupResult.SetDatabasesApp`).

The **caller** decides the transport: `pkg/instance` (`instance.New`, shared by `cmd/monolith` and the CLI) wires the apps in-process, and each `cmd/<service>` wires the others over HTTP:

```go
// pkg/instance — in-process (direct service, or an intraprocess adapter when mapping is needed)
inst.Databases, err = dbmodule.Setup(ctx, cfg, logger, inst.Bdbs.AuthChecker, opts.Databases)

// cmd/databases — split-service mode, over HTTP
bdbsHTTP := databaseshttp.NewBdbsApp(cfg.BdbsURL, cfg.InternalAPISecret)
dbResult, err := dbmodule.Setup(ctx, cfg, logger, bdbsHTTP, dbmodule.SetupOptions{})
```

## Checklist: Adding a New Cross-App Dependency

1. **Define the port** in the consuming app's `service/port.go` — exported `{ExternalApp}App`, sized to what the consumer actually needs (it's a slice of the provider, not a mirror of it)
2. **Shared shapes?** If a struct crosses the boundary, add it to `pkg/common/apptypes` (admission test above) and converters to the provider's `appconv`
3. **Add the internal endpoint** in the provider's `api/internal_handler.go` + register under `/internal/api/<app>/v1/...`, inside the internal-secret group
4. **Intraprocess path**: pass the provider's service directly if it satisfies the port; write an `adapters/intraprocess/<app>app.go` adapter only for aggregation/conversion
5. **Create the HTTP adapter** in `adapters/<consumer>http/<app>app.go` — calls the internal endpoint with the internal API secret, decodes into `apptypes`
6. **Accept the port in setup.go** as a parameter
7. **Wire in `pkg/instance`** (in-process) and **cmd/<service>** (HTTP, `cfg.<App>URL` and `cfg.InternalAPISecret`); add the config field if a new service URL is needed
8. **Twin tests**: if MCP tools or `NewServer` wiring changed, mirror in `pkg/mcpgateway/integrationtest/`
9. **Pin the wire**: golden test of the provider's internal endpoints in `pkg/<app>/integrationtest/internal_wire_test.go` + `testdata/internal_wire/<endpoint>.json` (`pkg/databases` is the precedent). Regenerate with `-args -update` only for an intentional wire change, and review the JSON diff as a cross-app API change
