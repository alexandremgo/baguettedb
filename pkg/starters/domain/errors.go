package domain

import (
	"errors"
	"fmt"
)

// ErrManifestInvalid is returned when a manifest fails structural validation.
var ErrManifestInvalid = errors.New("starter manifest invalid")

// ErrStarterNotFound is returned when no source exposes the requested starter key.
var ErrStarterNotFound = errors.New("starter not found")

// ErrStarterApplyFailed is returned when seeding a freshly created bdb from a manifest fails. The bdb is
// deleted best-effort, so the whole creation can be retried.
var ErrStarterApplyFailed = errors.New("starter apply failed")

// ErrBdbNameEmpty is returned when a new bdb would have an empty name.
var ErrBdbNameEmpty = errors.New("bdb name is required")

// ErrBdbNameTooLong is returned when a new bdb's name exceeds the maximum length.
var ErrBdbNameTooLong = errors.New("bdb name exceeds maximum length")

// ErrBdbNameTaken is returned when the user already gives another of their bdbs the new bdb's name, whatever the
// case.
var ErrBdbNameTaken = errors.New("a bdb of yours already has that name")

// BdbLimitReachedError is returned when the user already owns as many bdbs as their roles allow.
type BdbLimitReachedError struct {
	Max int
}

func (e BdbLimitReachedError) Error() string {
	return fmt.Sprintf("bdb limit reached (max %d)", e.Max)
}
