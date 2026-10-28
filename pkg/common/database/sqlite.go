package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"modernc.org/sqlite" // also registers the "sqlite" driver for database/sql
	sqlite3 "modernc.org/sqlite/lib"
)

// sqliteParams configures every connection to an instance store's SQLite file:
//
//   - _txlock=immediate makes every transaction BEGIN IMMEDIATE, so it takes the write lock up front and a
//     concurrent writer waits on the busy timeout instead of failing with SQLITE_BUSY when it upgrades a read.
//   - _time_format=sqlite writes time.Time as "2006-01-02 15:04:05.999999999-07:00", which the driver parses back
//     into time.Time for columns declared TIMESTAMP.
//   - _timezone=UTC converts every time.Time to UTC before writing it, so all stored times share one offset and
//     compare and sort as text in time order.
//   - busy_timeout lets a connection wait up to 5 s for another process's write lock.
//   - journal_mode(WAL) lets readers proceed while a write is in flight.
//   - foreign_keys(ON) enforces the REFERENCES clauses, off by default in SQLite.
const sqliteParams = "_txlock=immediate&_time_format=sqlite&_timezone=UTC" +
	"&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"

// SQLiteDBTX is the common query interface satisfied by both *sql.DB and *sql.Tx.
type SQLiteDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// OpenSQLite opens the SQLite file at path, creating it when missing, and checks that it is usable.
// Several processes may hold the same file open at once: writes are serialized by SQLite's lock and wait on it.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?"+sqliteParams)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		if cerr := db.Close(); cerr != nil {
			err = fmt.Errorf("%w (closing: %w)", err, cerr)
		}
		return nil, fmt.Errorf("opening sqlite %s: %w", path, err)
	}
	return db, nil
}

// IsSQLiteUniqueViolation reports whether err comes from an SQLite UNIQUE or PRIMARY KEY constraint failure.
func IsSQLiteUniqueViolation(err error) bool {
	sqliteErr, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// InPlaceholders returns the placeholder list of an expanded SQLite IN clause for n values: "?, ?, ?" for n = 3,
// and "" for n = 0.
func InPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// likeEscaper prefixes the LIKE wildcards and the escape character with a backslash.
var likeEscaper = strings.NewReplacer(`%`, `\%`, `_`, `\_`, `\`, `\\`)

// EscapeLike escapes s for use inside a LIKE pattern declared with ESCAPE '\': the wildcards % and _, and the
// escape character itself, are each prefixed with a backslash so they match literally.
func EscapeLike(s string) string {
	return likeEscaper.Replace(s)
}
