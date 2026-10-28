package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrBdbNotFound indicates that no bdb was found for the given criteria.
	ErrBdbNotFound = errors.New("bdb not found")

	// ErrBdbAlreadyDeleted indicates an attempt to delete a bdb that is
	// already soft-deleted, or to mutate a bdb currently in its grace period.
	ErrBdbAlreadyDeleted = errors.New("bdb is already deleted")

	// ErrMemberNotFound indicates that no membership was found for the given user and bdb.
	ErrMemberNotFound = errors.New("member not found")

	// ErrAlreadyMember indicates that the user is already a member of the bdb.
	ErrAlreadyMember = errors.New("user is already a member")

	// ErrCannotRemoveOwner indicates that the bdb owner cannot be removed.
	ErrCannotRemoveOwner = errors.New("cannot remove the bdb owner")

	// ErrCannotChangeOwnerRole indicates that the owner's role cannot be changed.
	ErrCannotChangeOwnerRole = errors.New("cannot change the owner's role")

	// ErrNotBdbMember indicates that the user is not a member of the bdb.
	ErrNotBdbMember = errors.New("user is not a member of this bdb")

	// ErrBdbNameEmpty indicates that the bdb name is required.
	ErrBdbNameEmpty = errors.New("bdb name is required")

	// ErrBdbNameTooLong indicates that the bdb name exceeds the maximum length.
	ErrBdbNameTooLong = errors.New("bdb name exceeds maximum length")

	// ErrBdbNameTaken indicates a name its owner already gives another of their bdbs, whatever the case.
	ErrBdbNameTaken = errors.New("a bdb of yours already has that name")

	// ErrInvalidRole indicates that the provided role is not a recognized bdb role.
	ErrInvalidRole = errors.New("invalid bdb role")

	// ErrInvalidBdbOrder indicates that a requested ordering does not name each of the user's bdbs
	// exactly once, so applying it would leave part of their list unordered.
	ErrInvalidBdbOrder = errors.New("ordering must list each of your bdbs exactly once")

	// ErrInviteNotFound indicates that no invite was found for the given criteria.
	ErrInviteNotFound = errors.New("invite not found")

	// ErrInviteExpired indicates that the invite has passed its expiration time.
	ErrInviteExpired = errors.New("invite has expired")

	// ErrInviteAlreadyUsed indicates that the invite has already been accepted.
	ErrInviteAlreadyUsed = errors.New("invite has already been used")

	// ErrInviteRevoked indicates that the invite was revoked by the bdb owner.
	ErrInviteRevoked = errors.New("invite has been revoked")

	// ErrInviteBdbDeleted indicates that the target bdb has been
	// soft-deleted, so the invite can no longer be redeemed.
	ErrInviteBdbDeleted = errors.New("invite target bdb has been deleted")
)

// BdbLimitReachedError indicates that the user has reached their maximum number of bdbs.
type BdbLimitReachedError struct {
	Max int
}

func (e BdbLimitReachedError) Error() string {
	return fmt.Sprintf("bdb limit reached (max %d)", e.Max)
}

// MemberLimitReachedError indicates that the bdb has reached its maximum number of members.
type MemberLimitReachedError struct {
	Max int
}

func (e MemberLimitReachedError) Error() string {
	return fmt.Sprintf("member limit reached (max %d)", e.Max)
}

// CapReached reports whether count has hit a cap; a cap of 0 means unlimited.
func CapReached(count, limit int) bool {
	return limit > 0 && count >= limit
}
