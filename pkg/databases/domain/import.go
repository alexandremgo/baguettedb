package domain

import "errors"

// Errors an import of an exported database file is refused with.
var (
	// ErrImportPassphraseNeeded indicates a file without the SQLite header imported without a passphrase: an
	// encrypted export, or no database at all, which look alike until a passphrase is tried.
	ErrImportPassphraseNeeded = errors.New(
		"the file isn't a SQLite database; if it's an encrypted bdb export, give the passphrase it was exported with")

	// ErrImportUnreadable indicates a file that doesn't open: a wrong passphrase, or no SQLite database at all.
	ErrImportUnreadable = errors.New("the file can't be opened: the passphrase is wrong, or it isn't a bdb export")

	// ErrImportNotExport indicates a file that is no bdb export: no database at all, or a SQLite database without the
	// platform tables an export carries.
	ErrImportNotExport = errors.New("the file is not a bdb export")

	// ErrImportDamaged indicates a file that fails SQLite's integrity check.
	ErrImportDamaged = errors.New("the file is damaged: it fails SQLite's integrity check")

	// ErrImportNewer indicates a file exported by a newer version, whose platform schema this one can't read.
	ErrImportNewer = errors.New("the file was exported by a newer version of BaguetteDB: update this one to import it")

	// ErrImportTooLarge indicates a file larger than the bdb's database may grow.
	ErrImportTooLarge = errors.New("the file is larger than a bdb's database may be")

	// ErrImportTargetNotEmpty indicates an import into a bdb that already holds tables, formulas or skills, which the
	// import would replace.
	ErrImportTargetNotEmpty = errors.New(
		"only an empty bdb can be imported into: this one has tables, formulas or skills")
)

// ImportFile describes an exported database file as an import inspects it, before installing it.
type ImportFile struct {
	// PlatformSchemaVersion is the platform schema version the file was exported at: its PRAGMA user_version.
	PlatformSchemaVersion int

	// HasPlatformTables reports whether the file holds tables named as the platform tables every bdb export carries.
	// Their columns are not checked: a file whose tables only take those names fails the platform migrations once
	// installed.
	HasPlatformTables bool

	// Intact reports whether the file passes SQLite's quick integrity check.
	Intact bool
}
