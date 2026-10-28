package service

import (
	"fmt"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// ConflictError wraps a structured *domain.ChangeConflict so that public
// service methods can keep their (*QueryResult, error) signatures while
// still letting callers recover the full conflict payload via
// errors.AsType[*ConflictError]. The wrapped error chain includes
// domain.ErrChangeConflict, so errors.Is(err, domain.ErrChangeConflict)
// keeps working for simple presence checks.
type ConflictError struct {
	Conflict *domain.ChangeConflict
}

// Error renders a short human-readable string. The structured payload
// lives in Conflict — callers should extract it via errors.AsType.
func (e *ConflictError) Error() string {
	if e == nil || e.Conflict == nil {
		return domain.ErrChangeConflict.Error()
	}
	return fmt.Sprintf("%s: %s", domain.ErrChangeConflict.Error(), e.Conflict.Reason)
}

// Unwrap returns the sentinel so errors.Is(err, domain.ErrChangeConflict)
// matches.
func (e *ConflictError) Unwrap() error { return domain.ErrChangeConflict }

// newConflictError wraps a domain.ChangeConflict into a service-layer
// error. Returns nil when conflict is nil so the typical
// `if err := …; err != nil { return … }` pattern handles the no-conflict
// case without a separate check.
func newConflictError(conflict *domain.ChangeConflict) error {
	if conflict == nil {
		return nil
	}
	return &ConflictError{Conflict: conflict}
}
