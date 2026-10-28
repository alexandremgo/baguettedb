package domain

import "testing"

// TestDatabase_OwnedBy checks the ownership predicate matches on BOTH the fleet color and the shard ordinal: a
// database is owned only by the pod whose (color, ordinal) equal the database's.
func TestDatabase_OwnedBy(t *testing.T) {
	db := &Database{PodColor: 1, PodShard: 2}

	if !db.OwnedBy(1, 2) {
		t.Error("same color and ordinal should be owned")
	}
	if db.OwnedBy(0, 2) {
		t.Error("different color (same ordinal) must NOT be owned")
	}
	if db.OwnedBy(1, 3) {
		t.Error("different ordinal (same color) must NOT be owned")
	}
}
