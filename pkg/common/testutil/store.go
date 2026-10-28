package testutil

import (
	"fmt"
	"os"
)

// Instance stores the integration suites can run on.
const (
	StorePostgres = "postgres"
	StoreSQLite   = "sqlite"
)

// InstanceStore returns the instance store the integration suites run on, read from BAGUETTEDB_TEST_STORE:
// StorePostgres when unset, StoreSQLite, or an error for any other value.
func InstanceStore() (string, error) {
	switch store := os.Getenv("BAGUETTEDB_TEST_STORE"); store {
	case "", StorePostgres:
		return StorePostgres, nil
	case StoreSQLite:
		return StoreSQLite, nil
	default:
		return "", fmt.Errorf("BAGUETTEDB_TEST_STORE=%q: want %q or %q", store, StorePostgres, StoreSQLite)
	}
}
