package domain

import "errors"

// Starter is a starter in the catalog a bdb can be created from, as the command line sees it.
type Starter struct {
	Key         string
	Name        string
	Description string
	Tags        []string
}

// ErrStarterNotFound reports a starter key that is not in the catalog.
var ErrStarterNotFound = errors.New("no starter with that key")
