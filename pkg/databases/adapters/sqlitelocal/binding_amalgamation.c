/*
 * Compiles the vendored SQLCipher amalgamation into the sqlitelocal package.
 *
 * All preprocessor flags (SQLITE_HAS_CODEC, SQLCIPHER_CRYPTO_OPENSSL,
 * SQLITE_EXTRA_INIT, SQLITE_EXTRA_SHUTDOWN, SQLITE_TEMP_STORE,
 * SQLITE_THREADSAFE, and the -I path to csrc/) are set in binding.go's
 * #cgo CFLAGS, so this file is just a tiny shim.
 *
 * Why the shim instead of compiling csrc/sqlite3.c directly: Go's CGo build
 * only auto-compiles .c files in the package directory, not subdirectories.
 * Including the amalgamation from a top-level shim keeps the vendored source
 * cleanly isolated under csrc/ while still letting Go pick it up.
 */

#include "sqlite3.c"
