// Package domain holds the databases model and its rules: the metadata of a bdb's encrypted database file, statement
// classification and query results, the change log and the SQL that captures and reverts its entries, skills, formulas
// with their parameter and display schemas, and the errors the app reports. It imports no other app, and its types
// carry no serialization tags.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Database represents a bdb-owned per-bdb SQLite database (one encrypted file per bdb).
type Database struct {
	ID    uuid.UUID
	BdbID uuid.UUID

	// PlatformSchemaVersion mirrors the SQLite PRAGMA user_version of this
	// database's SQLite file. The source of truth lives in SQLite;
	// this column is updated best-effort after each successful platform
	// migration and serves as a fast check to skip unnecessary migration
	// attempts on subsequent requests.
	PlatformSchemaVersion int

	// EncryptionKeyWrapped is the per-bdb DEK wrapped under the KEK
	// (AES-256-GCM, nonce ‖ ciphertext ‖ tag). Set at creation time and
	// never rotated implicitly. The plaintext DEK is never persisted.
	EncryptionKeyWrapped []byte

	// PodShard is the ordinal of the databases pod that owns this bdb's
	// file. 0 in single-pod deployments; the source of truth for routing
	// once multi-pod sharding is active.
	PodShard int16

	// PodColor is the blue/green fleet that owns this bdb's file (0 = blue, 1 = green). It is the deploy axis,
	// orthogonal to PodShard's load axis; together (PodColor, PodShard) identify the owning pod. 0 in single-fleet
	// deployments.
	PodColor int16

	// ReadOnly, when true, makes the database reject mutations and DDL while
	// still serving reads. Set during a rebalance move's drain window and
	// cleared once the file is adopted on its new pod.
	ReadOnly bool

	// SizeBytes mirrors the on-disk size of the database's SQLite files (main file + WAL sidecar), refreshed
	// best-effort after successful writes. Near-real-time, not byte-exact: refreshes are coalesced, so the
	// value can lag the live file by a few seconds.
	SizeBytes int64

	// SizeUpdatedAt is when SizeBytes was last refreshed. Nil until the first refresh, so a never-measured
	// database is distinguishable from an empty one.
	SizeUpdatedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// OwnedBy reports whether the pod identified by (color, ordinal) owns this database — i.e. its blue/green fleet
// color and its shard ordinal both match.
func (d *Database) OwnedBy(color, ordinal int16) bool {
	return d.PodColor == color && d.PodShard == ordinal
}

// DatabaseStats holds usage statistics for a database.
type DatabaseStats struct {
	StorageBytesUsed uint64

	// LastModifiedAt is the newest modification time across the database's on-disk files (main file and WAL
	// sidecar). Zero when no file exists on disk. Drives the backup dirty-check.
	LastModifiedAt time.Time
}

// SnapshotInfo describes one stored database snapshot object.
type SnapshotInfo struct {
	// Key is the full object key in the snapshot store.
	Key string

	// DatabaseID is the database the snapshot belongs to.
	DatabaseID string

	// TakenAt is the instant the snapshot was taken (UTC).
	TakenAt time.Time

	// SizeBytes is the snapshot object's size.
	SizeBytes int64
}
