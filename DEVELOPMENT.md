# Developing BaguetteDB

How to build, run and test the code in this repository. Running a released `bdb` is covered by the
[README](README.md).

## Repo layout

- `cmd/` — entrypoints: `monolith` (everything in one process, on Postgres and Hydra), the split services `auth`,
  `bdbs`, `databases`, `mcp-gateway`, and `bdb` (also built as `baguettedb`), the command line of a local instance
- `pkg/cli` — the command line: its commands, and its use cases over the modules wired in-process
- `pkg/` — the modules, each with `domain/`, `service/`, `adapters/`, `api/` and `mcp/` layers, plus `pkg/common`
  (config, auth middleware, shared helpers) and `pkg/webui` (the embedded web app)
- `web/` — the React + Vite web app
- `docs/` — README assets and the generated reference (see [Reference docs](#reference-docs))
- `third-party/` — upstream license texts of the native libraries the binaries contain (see
  [Third-party notices](#third-party-notices))
- `Makefile` — build, run and test targets (see below)

## Prerequisites

Linux or macOS. Windows is not supported yet, natively: SQLCipher is built through CGo, and the SQLite instance
store takes Unix `flock` locks.

- Go (version pinned in `go.mod`)
- Node.js 24 and npm, for the web app
- A C toolchain (`cc`, `make`)
- On Linux, `pkg-config` (or `pkgconf`) and the OpenSSL 3 development headers
- Docker with Compose, for the development stack and the integration tests

The C toolchain is needed because `pkg/databases/adapters/sqlitelocal/` embeds SQLCipher via CGo. Its crypto comes
from the system on macOS (CommonCrypto) and from OpenSSL on Linux. See
`pkg/databases/adapters/sqlitelocal/csrc/README.md` for the vendored-source rationale and bump procedure.

### macOS

```bash
xcode-select --install   # the C toolchain, if not installed yet
```

### Linux

```bash
apt-get install build-essential pkg-config libssl-dev
```

## Development stack

The root `compose.yaml` runs Postgres, Hydra and the monolith, the latter rebuilt on every file change by Air
(`Dockerfile.dev`). Configuration comes from a `.env` next to it: copy `.env.example`, every value has a
working default for local development.

```bash
cp .env.example .env
make dev-up        # builds and starts; the API and the embedded web app answer on http://localhost:8080
make dev-down      # stops, keeps the Postgres volume and ./local-data
make dev-clean     # stops and wipes everything: Postgres volume, ./local-data (bdb files and key)
```

The stack listens on `http://localhost:8080`. To complete a login from another device, for instance through an
HTTPS tunnel, set `BAGUETTEDB_DEV_ORIGIN` to the tunnel origin in `.env`: the OAuth issuer and the login redirects
follow it.

### Web app

```bash
make web-setup     # npm install in web/
make web-dev       # Vite dev server on http://localhost:5173, proxying the API to :8080
make web-pwa-dev   # same with the service worker enabled, for PWA testing
make web-build     # production bundle in web/dist
```

Sign in at `http://localhost:5173`. The OAuth client the web app uses is provisioned by the auth module at
startup, with the redirect URI derived from `BAGUETTEDB_APP_BASE_URL` (which defaults to the Vite origin), so
nothing needs registering by hand.

## Tests

```bash
make test                       # Go unit tests, cached
make test-fresh                 # same, bypassing the cache
make test-all                   # unit + integration tests (need Docker: they start Postgres), cached
make test-all-fresh             # unit + integration tests on both instance stores, bypassing the cache
make test-all-postgres          # unit + integration tests on the Postgres instance store only
make test-all-sqlite            # unit + integration tests on the SQLite instance store only
go test -v ./pkg/databases/adapters/sqlitelocal/...   # one package, verbose
```

Integration suites run on the instance store `BAGUETTEDB_TEST_STORE` names: `postgres` (the default, a Postgres
container) or `sqlite` (a fresh SQLite file). A suite opens its store with `testutil.OpenInstanceDB` and builds
its repositories for whichever one it got, so every integration suite runs on both.

Integration tests carry the `integration` build tag. For gopls to see them:

```bash
GOFLAGS="-tags=integration" gopls check <file>
```

The MCP gateway keeps twin copies of the module MCP tests under `pkg/mcpgateway/integrationtest/`. A change to a
tool's behavior or registration in `pkg/bdbs/mcp` or `pkg/databases/mcp` must be mirrored there.

## Reference docs

`docs/reference/` is generated from the code: the MCP tool reference from the tool registrations of every module,
the two manuals `get_docs` serves, and the gateway's agent instructions. Regenerate after touching a tool
description or input schema, an embedded manual or the instructions; CI fails when the committed files are stale.

```bash
make docs-reference
```

## Third-party notices

`THIRD_PARTY_LICENSES` ships next to `bdb` in every release archive and on the web app's Licenses page. It is
generated by `cmd/third-party-notices` from what the build ships: the Go modules `bdb` links on each release platform,
the Go standard library, the packages Vite bundles into the web app (`web/dist/.vite/license.json`), the Workbox
runtime of the service worker, and the native libraries whose license texts live under `third-party/`. Regenerate
after any dependency change; CI fails when the committed file is stale. On a Dependabot pull request, the GitHub
Actions workflow "Dependabot: regenerate notices" does it and commits the result onto the branch.

```bash
make notices       # builds the web app, then regenerates THIRD_PARTY_LICENSES
```

Every license is checked against an allowlist of permissive licenses (`allowedLicenses` in
`cmd/third-party-notices/policy.go`), and the run writes nothing when one of these turns up:

- **A license outside the allowlist** (GPL, LGPL, MPL, source-available terms, CC0…): decide by hand whether the
  dependency stays. Widen the list only for a license whose every obligation the notices file meets by itself.
- **A module or package without a license file**: no license means no permission to redistribute it.
- **A license file the classifier only partly recognizes**: read it, then add an entry to `overrides` in `policy.go`
  with the license it was reviewed as and the SHA-256 the error prints. An override pins that exact text, so the run
  fails again when upstream changes it.

A native library, one no package manager knows (vendored C, or a library the release build links statically), goes
in by committing its upstream license text verbatim under `third-party/<name>/` and adding it to `natives` in
`cmd/third-party-notices/native.go`.

## Lint

```bash
make lint          # gofmt check, go vet ./..., golangci-lint run ./... (the whole codebase)
cd web && npm run lint
```

CI pins golangci-lint (v2.15.0, in `.github/workflows/ci.yml`); install the same version locally
(`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`), and bump both together.

Lines wrap at 120 characters, code and comments alike (`lll` in `.golangci.yml`).

Imports come in three groups: the standard library, third-party modules, then this module's own packages (`gci`
in `.golangci.yml`); `golangci-lint fmt ./...` puts them in order.

The import boundaries between apps and layers are `depguard` rules in `.golangci.yml`: an app's domain, service,
api and mcp import no other app, a domain no infrastructure, and a service none of its app's adapters or transports.

## Building

```bash
make build         # bin/baguettedb: the API only, no web app
make build-ui      # web-build, then copies web/dist into pkg/webui/dist, then build: the API and the web app
```

`pkg/webui` embeds whatever is in `pkg/webui/dist` at compile time (`go:embed`). Only a `.gitkeep` placeholder
is tracked, so a plain `make build` yields a binary that serves the API and `/config.json` but answers 404 for
the app shell; `make build-ui` fills the directory first. A release `bdb` (`make release-cli`) always contains the
web app.

Two rules follow from the embedding:

- A new top-level route in the web app must not start with a backend prefix (`/api/`, `/oauth2/`, `/login`,
  `/consent`, `/.well-known/`, `/authorize`, `/token`, `/register`, `/userinfo`, `/mcp`, `/healthz`, `/static/`).
  The fallback in `pkg/webui` refuses to serve the app shell under those, and `web/pwaConfig.ts` keeps the
  matching service-worker denylist.
- The web app reads its runtime configuration from `/config.json`, served by the backend: the OAuth client id and
  redirect URI, and the instance's MCP URL. Nothing is baked in at build time.

### The command line

```bash
make build-cli     # bin/bdb: the command line, without the web app (bdb serve answers the API and MCP only)
make build-cli-ui  # web-build, webui-sync, then build-cli: bdb serve serves the web app too
make release-cli   # dist/bdb as a release ships it (scripts/build-cli-release.sh)
```

A release binary needs no shared library beyond the system's: on macOS SQLCipher's crypto comes from the Security
framework, and on Linux `scripts/build-cli-release.sh` builds in an Alpine container and links everything
statically. The release workflow builds one per platform and attaches them to the GitHub release.

Try a change against a throwaway data directory rather than your own `~/.baguettedb`:

```bash
go run ./cmd/bdb --data-dir /tmp/bdb-try bdbs create --starter kanban
go run ./cmd/bdb --data-dir /tmp/bdb-try serve
```

`bdb info` reports which data directory is in use, the binary's crypto provider and whether it embeds the web app,
and the instance store's schema versions against the binary's: it reads the store without opening the instance, so
it never migrates or creates anything.

The command line's integration tests (`pkg/cli/integrationtest/`) run it in-process through `cli.Run`, on its own
SQLite instance store.

### Releasing

A release goes out in three steps, so the archives people download are the ones tested:

1. Build: run the Release workflow on `main` with the version, `v1.2.3`, or `v1.2.3-rc1` for a pre-release. It builds
   and smoke-tests one archive per platform and attaches them, with `checksums.txt`, to a draft release that only
   maintainers see and that has no tag yet. Running it again for the same version rebuilds the draft from `main`.
2. Test: `gh release download v1.2.3` fetches the draft's archives as they will be published.
3. Publish: `gh release edit v1.2.3 --draft=false`, or Publish on the draft's page. GitHub then creates the `v1.2.3`
   tag on the commit the archives were built from, and the Homebrew workflow writes the new formula to
   `alexandremgo/homebrew-tap` (`scripts/homebrew-formula.sh`; a pre-release leaves it alone).

```bash
gh workflow run release.yml -f version=v1.2.3
```

Never push a `v*` tag by hand: the workflow refuses a version whose tag exists, and once a tag is public the Go module
proxy keeps that version for good, so a mistake costs the version number.

## MCP server against the development stack

`make dev-up` serves the MCP endpoint at `http://localhost:8080/mcp` (Streamable HTTP, protected by the local
Hydra via OAuth). Point Claude Code at it with a `.mcp.json` at the repo root, which is gitignored:

```json
{
  "mcpServers": {
    "baguettedb": {
      "type": "http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Run `/mcp` from the repo, pick `baguettedb`, and choose Authenticate: Claude Code registers itself as an OAuth
client and opens a browser to log in. `claude mcp login baguettedb` does the same from a shell.

`make dev-clean` drops the Postgres volume, and with it Hydra's registered OAuth clients. Claude Code keeps its
now-orphaned registration and the browser lands on an error page, with `The requested OAuth 2.0 Client does not
exist` in the Hydra logs. Clear the stale credentials, then authenticate again:

```bash
claude mcp logout baguettedb
```

## The encryption key in development

Each bdb is encrypted at rest with SQLCipher under a key of its own. That key is wrapped (AES-256-GCM) with a
single 32-byte key encryption key, the **KEK**, which the `databases` module reads at startup from the path in
`BAGUETTEDB_DATABASES_KEK_FILE`.

The development stack sets `BAGUETTEDB_DATABASES_KEK_AUTOGENERATE=true`, so the module generates 32 random bytes
at `./local-data/kek` on the first start and leaves the file alone afterwards; the per-bdb SQLite files live next
to it under `./local-data/databases/`, and the directory is gitignored. The flag is off by default: a deployment
that mounts the key from a secret store fails fast when the file is missing, rather than minting a key nobody
backed up.

Tests do not read this file: each test that needs a KEK generates an ephemeral one inside `t.TempDir()`.
