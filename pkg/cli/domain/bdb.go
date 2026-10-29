// Package domain holds the command line's own model, whichever instance serves it: the shapes the commands work with
// and the rules over them that need no I/O.
package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// Bdb is a bdb as the command line sees it: what identifies it, what it is for, and the caller's role in it.
type Bdb struct {
	ID          uuid.UUID
	Name        string
	Description string
	Role        string
}

// shortIDLen is the length of a short ID.
const shortIDLen = 8

// ShortID returns the first characters of the bdb's ID, enough to tell it apart in a listing and to name it as an
// ID prefix.
func (b Bdb) ShortID() string {
	return b.ID.String()[:shortIDLen]
}

// BdbSummary is a bdb in a listing, with its tables. TablesErr is set, and Tables empty, when the bdb's tables could
// not be read; the rest of the listing stands.
type BdbSummary struct {
	Bdb
	Tables    []apptypes.TableInfo
	TablesErr error
}

// CreatedBdb is a bdb just created, with the starter it was seeded from ("" for a blank bdb) and what it holds.
// ContentsErr is set, and Tables and Formulas empty, when its contents could not be read back; the bdb exists
// regardless.
type CreatedBdb struct {
	Bdb
	StarterKey  string
	Tables      []apptypes.TableInfo
	Formulas    []apptypes.Formula
	ContentsErr error
}

// ErrBdbNotFound reports a bdb reference that matches no bdb.
var ErrBdbNotFound = errors.New("no bdb")

// ErrBdbNameTaken reports a name another bdb of the same owner has, whatever the case.
var ErrBdbNameTaken = errors.New("you already have a bdb with that name, whatever the case")

// ErrNotBdbOwner reports a change only the bdb's owner may make, asked by another member.
var ErrNotBdbOwner = errors.New("only the owner can rename a bdb or change its description")

// AmbiguousBdbError reports a bdb reference that matches several bdbs at the same level.
type AmbiguousBdbError struct {
	Ref     string
	Matches []Bdb
}

// Error lists the matching bdbs by name and short ID.
func (e AmbiguousBdbError) Error() string {
	names := make([]string, len(e.Matches))
	for i, b := range e.Matches {
		names[i] = fmt.Sprintf("%s (%s)", b.Name, b.ShortID())
	}
	return fmt.Sprintf("%q matches several bdbs: %s; name one by a longer ID prefix", e.Ref, strings.Join(names, ", "))
}

// ResolveBdb returns the bdb among bdbs that ref names, trying in turn its exact name, its name ignoring case, and a
// prefix of its ID. The first level with a match decides: one match is the answer, several are an
// AmbiguousBdbError. A ref matching nothing, or empty, wraps ErrBdbNotFound.
func ResolveBdb(ref string, bdbs []Bdb) (Bdb, error) {
	levels := []func(Bdb) bool{
		func(b Bdb) bool { return b.Name == ref },
		func(b Bdb) bool { return strings.EqualFold(b.Name, ref) },
		func(b Bdb) bool { return strings.HasPrefix(b.ID.String(), strings.ToLower(ref)) },
	}
	if ref != "" {
		for _, matches := range levels {
			var found []Bdb
			for _, b := range bdbs {
				if matches(b) {
					found = append(found, b)
				}
			}
			switch len(found) {
			case 0:
				continue
			case 1:
				return found[0], nil
			default:
				return Bdb{}, AmbiguousBdbError{Ref: ref, Matches: found}
			}
		}
	}
	return Bdb{}, fmt.Errorf("%w named %q or with an ID starting with it", ErrBdbNotFound, ref)
}
