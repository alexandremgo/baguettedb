package domain

import "errors"

// Errors importing a file is refused with that the command line answers by asking for a passphrase.
var (
	// ErrPassphraseNeeded reports a file imported without a passphrase that is either an encrypted export or no SQLite
	// database at all, which look alike until a passphrase is tried.
	ErrPassphraseNeeded = errors.New("the file isn't a SQLite database; if it's an encrypted bdb export, give its " +
		"passphrase with --passphrase-file")

	// ErrImportUnreadable reports a file that doesn't open: a wrong passphrase, or not a bdb export at all.
	ErrImportUnreadable = errors.New("the file can't be opened: the passphrase is wrong, or it isn't a bdb export")
)
