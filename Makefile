.PHONY: docs-reference notices build build-ui build-cli build-cli-ui release-cli webui-sync build-auth build-bdbs build-databases build-mcp-gateway run run-auth run-bdbs run-databases run-mcp-gateway test test-fresh test-integration test-integration-fresh test-all-postgres test-all-sqlite test-all test-all-fresh lint dev-up dev-down dev-clean web-setup web-dev web-pwa-dev web-build

build:
	go build -o bin/baguettedb ./cmd/monolith

# Version stamped into the CLI: the nearest tag, else the commit; "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Builds bdb, the command line of a local instance (the same code also builds as `baguettedb` from ./cmd/baguettedb).
build-cli:
	go build -ldflags="-X github.com/alexandremgo/baguettedb/pkg/cli.version=$(VERSION)" -o bin/bdb ./cmd/bdb

# Builds the monolith with the web frontend embedded (see webui-sync).
build-ui: web-build webui-sync build

# Builds bdb with the web frontend embedded, so `bdb serve` serves the web app and its sign-in links open it.
build-cli-ui: web-build webui-sync build-cli

# Builds the command line as a release ships it, into dist/bdb: the web app embedded, and no shared library beyond the
# system's (scripts/build-cli-release.sh; on Linux it builds in a container, fully static).
release-cli: web-build webui-sync
	scripts/build-cli-release.sh $(VERSION)

# Copies the built frontend into the directory the monolith and bdb embed. Without it `go build` still succeeds and
# the binary serves the API only. Vite's build metadata (.vite/) stays out: THIRD_PARTY_LICENSES already carries the
# license texts it lists.
webui-sync:
	find pkg/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. pkg/webui/dist/
	rm -rf pkg/webui/dist/.vite

build-auth:
	go build -o bin/baguettedb-auth ./cmd/auth

build-bdbs:
	go build -o bin/baguettedb-bdbs ./cmd/bdbs

build-databases:
	go build -o bin/baguettedb-databases ./cmd/databases

build-mcp-gateway:
	go build -o bin/baguettedb-mcp-gateway ./cmd/mcp-gateway

run:
	go run ./cmd/monolith

run-auth:
	go run ./cmd/auth

run-bdbs:
	go run ./cmd/bdbs

run-databases:
	go run ./cmd/databases

run-mcp-gateway:
	go run ./cmd/mcp-gateway

test:
	go test ./...

test-fresh:
	go test -count=1 ./...

# The targets running the integration tests first embed the web app in the code under test, as a release does: part of
# bdb serve's behavior only exists with it.
test-integration: web-build webui-sync
	go test -tags=integration ./...

test-integration-fresh: web-build webui-sync
	go test -tags=integration -count=1 ./...

# Unit and integration tests on the Postgres instance store, bypassing the cache.
test-all-postgres: web-build webui-sync
	BAGUETTEDB_TEST_STORE=postgres go test -tags=integration -count=1 ./...

# Unit and integration tests on the SQLite instance store, bypassing the cache.
test-all-sqlite: web-build webui-sync
	BAGUETTEDB_TEST_STORE=sqlite go test -tags=integration -count=1 ./...

test-all: web-build webui-sync
	go test -tags=integration ./...

# Unit and integration tests on both instance stores, bypassing the cache.
test-all-fresh: test-all-postgres test-all-sqlite

# Formatting, vet and golangci-lint over the whole codebase.
lint:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: the following files are not formatted (run 'gofmt -w .'):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...
	golangci-lint run ./...

# Regenerates docs/reference/ from the MCP tool registrations, the embedded manuals and the gateway instructions.
docs-reference:
	go run ./cmd/mcp-reference -out docs/reference

# Regenerates THIRD_PARTY_LICENSES from the Go modules bdb links and the web app's bundle. Run after any dependency
# change; CI fails when the committed file differs.
notices: web-build
	go run ./cmd/third-party-notices

dev-up:
	docker compose up --build

dev-down:
	docker compose down

# Tears down the dev stack and wipes all state: the postgres volume and the
# local data dir (per-bdb SQLCipher files plus the KEK generated on first
# start). Useful when resetting after schema changes or stale local databases.
dev-clean:
	docker compose down -v
	rm -rf ./local-data

web-setup:
	cd web && npm install

web-dev:
	cd web && npm run dev

# web-pwa-dev runs the dev server with the service worker enabled (PWA_DEV) for PWA testing.
web-pwa-dev:
	cd web && PWA_DEV=true npm run dev

web-build:
	cd web && npm run build
