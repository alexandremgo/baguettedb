//go:build integration

package integrationtest

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/cli"
)

// Exit statuses the command line documents.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// runCLI runs the command line args with an empty stdin and returns the exit status and what was written to stdout
// and stderr.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCLIWithStdin(t, "", args...)
}

// runCLIWithStdin runs the command line args with stdin as its standard input and returns the exit status and what
// was written to stdout and stderr.
func runCLIWithStdin(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = cli.Run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// clearDataDirEnv unsets, for the duration of the test, every environment variable that would override a setting
// derived from the data directory, so a developer's .env cannot leak into the instance under test.
func clearDataDirEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BAGUETTEDB_DATA_DIR", "BAGUETTEDB_INSTANCE_SQLITE_PATH", "BAGUETTEDB_DATABASES_DATA_DIR",
		"BAGUETTEDB_DATABASES_KEK_FILE", "BAGUETTEDB_DATABASES_KEK_AUTOGENERATE", "BAGUETTEDB_DATABASES_BACKUP_ENABLED",
	} {
		t.Setenv(key, "")
	}
}

// assertMode fails the test unless path exists with exactly mode (type bits and permissions).
func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("stat %s: %v", path, err)
		return
	}
	if got := info.Mode() & (os.ModeType | os.ModePerm); got != mode {
		t.Errorf("%s has mode %v, want %v", path, got, mode)
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and concurrent readers, for the stderr of a run the test reads
// while it may still be writing.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the buffer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what was written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
