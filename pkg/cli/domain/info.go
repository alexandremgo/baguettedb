package domain

// Where a command's data directory came from.
const (
	DataDirFromFlag    = "flag"
	DataDirFromEnv     = "env"
	DataDirFromDefault = "default"
)

// Info is what bdb info reports: the binary, the data directory a command would use and how it was chosen, the
// instance it holds, and the server running on it.
type Info struct {
	Binary  BinaryInfo
	DataDir DataDirInfo
	// Instance is nil when the data directory has no instance store yet.
	Instance *InstanceSummary
	// As is the account --as names, empty without the flag.
	As string
	// Server is the bdb serve running on the data directory, nil when none is.
	Server *ServedInstance
	// MCPName is the name bdb mcp config registers the instance under, as the account As or the owner.
	MCPName string
}

// BinaryInfo describes the running binary.
type BinaryInfo struct {
	Name    string
	Version string
	OS      string
	Arch    string
	// CipherVersion and CipherProvider are SQLCipher's version and the crypto library its encryption uses.
	CipherVersion  string
	CipherProvider string
	WebApp         bool
}

// DataDirInfo describes a data directory: where it is, how it was chosen, and its files.
type DataDirInfo struct {
	Path   string
	Source string
	Exists bool
	// Key, InstanceStore and Databases are where the settings put the encryption key, the instance store and the
	// bdb files, the data directory's own or another an environment variable names.
	Key           DataFile
	InstanceStore DataFile
	Databases     DataFile
	// KeyOwnerOnly reports whether no one but its owner can read the key file.
	KeyOwnerOnly bool
	// InstanceStoreBytes counts the instance store with its journal files; BdbFiles and BdbBytes the bdb files.
	InstanceStoreBytes int64
	BdbFiles           int
	BdbBytes           int64
}

// DataFile is a file or directory the settings place: its path, whether it exists, and the environment variable that
// placed it elsewhere than the data directory, if any.
type DataFile struct {
	Path     string
	Exists   bool
	Override string
}

// InstanceSummary is what an instance store holds, read without changing it.
type InstanceSummary struct {
	// Owner is the instance's owner, nil when none is recorded yet.
	Owner *User
	// AsFound reports whether an account has the email --as names; false without the flag.
	AsFound  bool
	Bdbs     int
	Accounts int
	Schemas  []AppSchema
	// PlatformSchema is the platform schema version this binary keeps bdbs at.
	PlatformSchema int
}

// AppSchema is the schema version of one app's tables in the instance store, against the version this binary
// migrates them to.
type AppSchema struct {
	App     string
	Version uint
	Dirty   bool
	Latest  uint
}

// Newer reports whether a newer bdb migrated the app's tables past what this binary knows.
func (s AppSchema) Newer() bool {
	return s.Version > s.Latest
}
