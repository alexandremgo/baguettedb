# Vendored SQLCipher amalgamation

This directory holds the C amalgamation that backs the `sqlitelocal` adapter
through its CGo binding (`../binding.go`).

## What's here

- `sqlite3.c` — single-file C amalgamation produced by upstream's `make sqlite3.c`.
- `sqlite3.h` — public header generated alongside `sqlite3.c`.
- `SQLCIPHER_VERSION` — version, upstream commit SHA, source tarball URL, and SHA-256.

## Why vendored

- **Reproducible builds** — the binding compiles bit-for-bit identical bytes regardless of the build host.
- **No system SQLite collision** — distros ship their own `libsqlite3` without SQLCipher; linking the amalgamation directly avoids accidental symbol overlap.
- **Pinned crypto behavior** — SQLCipher's wire format and KDF iteration count are tied to the source version. A managed dependency could shift them silently across upgrades; the amalgamation cannot.

## License

SQLCipher Community Edition is under a BSD license: the two BSD clauses plus a
no-endorsement clause, i.e. BSD-3-Clause. Its text, as the header of the vendored
`sqlite3.c` carries it, is committed in `third-party/sqlcipher/LICENSE` at the
repo root, which `make notices` reproduces in `THIRD_PARTY_LICENSES`.

## How to compile

The CGo binding (one directory up) sets the required preprocessor flags:

```
-DSQLITE_HAS_CODEC
-DSQLCIPHER_CRYPTO_CC          (macOS)
-DSQLCIPHER_CRYPTO_OPENSSL     (every other system)
-DSQLITE_EXTRA_INIT=sqlcipher_extra_init
-DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown
-DSQLITE_TEMP_STORE=2
-DSQLITE_THREADSAFE=1
```

On macOS the crypto provider is CommonCrypto, linked from the system's Security
and CoreFoundation frameworks, so a macOS build needs no OpenSSL and its binary
starts on any Mac. Elsewhere it is OpenSSL, linked via `pkg-config: libcrypto`
(statically in release builds, see `scripts/build-cli-release.sh`). The two
providers implement the same algorithms, so a file written under one opens under
the other. The first two flags
enable SQLCipher itself; the next two are mandated by `sqlite3.c` (a `#error`
directive enforces them — they wire SQLCipher's per-process init hooks into
SQLite's startup path). `SQLITE_TEMP_STORE=2` matches the upstream
amalgamation's configure default (in-RAM temp tables); `SQLITE_THREADSAFE=1`
is required for Go's database/sql connection pool.

### Build prerequisites

On macOS, a C toolchain alone (the Xcode Command Line Tools). Elsewhere:

- A C toolchain.
- `pkg-config` (the original GNOME tool, ≥ 0.29) or `pkgconf` (its modern
  fork, ≥ 1.5) — either binary on `PATH` works, since both expose a
  `pkg-config` executable. Verified locally with `pkgconf 2.5.1`; any
  version emitting standard `--cflags` / `--libs` output is fine.
- OpenSSL 3 development headers and `.pc` files.

#### Linux

```bash
apt-get install pkg-config libssl-dev
```

`libssl-dev` installs the `.pc` files in standard locations, so no
`PKG_CONFIG_PATH` tweaking is needed.

## How to regenerate

When bumping SQLCipher, work from a fresh download of the upstream release
tarball — never from this directory, and not from a long-lived clone of the
SQLCipher repo (artefacts from prior builds can leak into the amalgamation).

Build prerequisites — macOS: `brew install openssl@3 tcl-tk`. Linux:
`apt-get install libssl-dev tcl`. Both need a working C toolchain.

```bash
# 1. Pick the new version and download its release tarball.
VERSION=4.16.0  # whatever you're bumping to
cd "$(mktemp -d)"
curl -sSLO "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${VERSION}.tar.gz"

# 2. Verify the tarball checksum against the value published on the GitHub
#    release page. Record the new SHA-256 — it goes into SQLCIPHER_VERSION.
shasum -a 256 "v${VERSION}.tar.gz"

# 3. Extract and enter the source tree.
tar xzf "v${VERSION}.tar.gz"
cd "sqlcipher-${VERSION}"

# 4. Capture the upstream commit SHA — also goes into SQLCIPHER_VERSION.
cat manifest.uuid

# 5. Generate the amalgamation.
./configure \
  --with-tempstore=yes \
  CFLAGS="-DSQLITE_HAS_CODEC -DSQLCIPHER_CRYPTO_OPENSSL -I$(brew --prefix openssl@3)/include" \
  LDFLAGS="-L$(brew --prefix openssl@3)/lib -lcrypto"
make sqlite3.c
```

After `make sqlite3.c` succeeds, the source tree contains the freshly
generated `sqlite3.c` and `sqlite3.h`. Copy both into this directory,
overwriting the previous version, then update `SQLCIPHER_VERSION` with the
new release tag, the contents of `manifest.uuid`, the new tarball URL, and
its SHA-256.

Then refresh the license text: replace `third-party/sqlcipher/LICENSE` with the
license comment of the new `sqlite3.c` (the block from `Copyright (c) 2008-…,
ZETETIC LLC` to `EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.`, without
the leading `** `), and run `make notices` so `THIRD_PARTY_LICENSES` carries the
new version and text.

On Linux build hosts, replace `$(brew --prefix openssl@3)` with the path to
`libssl-dev`'s headers (typically the system default, so the `-I` / `-L`
flags can be dropped entirely).

## Why this version

SQLCipher 4.15.0 (SQLite 3.53.0) is pinned because:

- 4.14.0 carries the upstream WAL-reset corruption fix from SQLite 3.51.3 — we run WAL.
- 4.15.0 hardens `sqlcipher_export` (used for re-keying / export workflows).
