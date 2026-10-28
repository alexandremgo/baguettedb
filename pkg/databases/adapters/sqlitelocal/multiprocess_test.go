//go:build cgo && integration

package sqlitelocal

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// Environment TestMultiProcess_ConcurrentWritesOnOneBdb hands each child process.
const (
	helperEnvMarker   = "BAGUETTEDB_SQLITELOCAL_MULTIPROCESS_HELPER"
	helperEnvDataDir  = "BAGUETTEDB_SQLITELOCAL_DATA_DIR"
	helperEnvKey      = "BAGUETTEDB_SQLITELOCAL_KEY"
	helperEnvWriter   = "BAGUETTEDB_SQLITELOCAL_WRITER"
	multiProcessDBID  = "multiprocess"
	multiProcessStart = "start"
)

const (
	multiProcessChildren   = 4
	multiProcessWritesEach = 100
)

// TestMultiProcess_ConcurrentWritesOnOneBdb runs several processes writing one bdb file at once, each through its own
// Client, and every write is a transaction that reads before it writes: the shape that fails with SQLITE_BUSY_SNAPSHOT
// when a deferred transaction upgrades to a writer after another process committed. It checks that no process
// surfaces an error and that every row lands.
func TestMultiProcess_ConcurrentWritesOnOneBdb(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	key := newKey(t)

	parent := newMultiProcessClient(t, dataDir, key)
	if err := parent.CreateDatabase(ctx, multiProcessDBID, key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := parent.Execute(ctx, multiProcessDBID,
		"CREATE TABLE items (id INTEGER PRIMARY KEY, writer TEXT NOT NULL, seen INTEGER NOT NULL)", nil); err != nil {
		t.Fatalf("creating the table: %v", err)
	}

	children := make([]*exec.Cmd, multiProcessChildren)
	outputs := make([]*bytes.Buffer, multiProcessChildren)
	for i := range children {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMultiProcessHelper$", "-test.v")
		cmd.Env = append(os.Environ(),
			helperEnvMarker+"=1",
			helperEnvDataDir+"="+dataDir,
			helperEnvKey+"="+hex.EncodeToString(key),
			fmt.Sprintf("%s=child-%d", helperEnvWriter, i),
		)
		outputs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = outputs[i], outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting child %d: %v", i, err)
		}
		children[i] = cmd
	}

	// Releases every process at once, so the writes overlap instead of following each process's startup.
	if err := os.WriteFile(filepath.Join(dataDir, multiProcessStart), nil, 0o600); err != nil {
		t.Fatalf("writing the start file: %v", err)
	}
	parentErr := writeRows(ctx, parent, "parent")
	for i, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, outputs[i])
		}
	}
	if parentErr != nil {
		t.Fatalf("parent: %v", parentErr)
	}

	res, err := parent.Execute(ctx, multiProcessDBID, "SELECT writer, COUNT(*) FROM items GROUP BY writer", nil)
	if err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if len(res.Rows) != multiProcessChildren+1 {
		t.Fatalf("got rows from %d writers, want %d: %v", len(res.Rows), multiProcessChildren+1, res.Rows)
	}
	for _, row := range res.Rows {
		if n := row[1].Value; n != int64(multiProcessWritesEach) {
			t.Errorf("writer %v wrote %v rows, want %d", row[0].Value, n, multiProcessWritesEach)
		}
	}
}

// TestMultiProcessHelper is the child process of TestMultiProcess_ConcurrentWritesOnOneBdb: it opens the data dir the
// parent names through a Client of its own, waits for the start file and writes its share of rows.
func TestMultiProcessHelper(t *testing.T) {
	if os.Getenv(helperEnvMarker) != "1" {
		t.Skip("runs only as a child process of TestMultiProcess_ConcurrentWritesOnOneBdb")
	}
	key, err := hex.DecodeString(os.Getenv(helperEnvKey))
	if err != nil {
		t.Fatalf("decoding the key: %v", err)
	}
	dataDir := os.Getenv(helperEnvDataDir)
	client := newMultiProcessClient(t, dataDir, key)

	if err := waitForFile(filepath.Join(dataDir, multiProcessStart), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := writeRows(context.Background(), client, os.Getenv(helperEnvWriter)); err != nil {
		t.Fatal(err)
	}
}

// newMultiProcessClient opens a Client on dataDir whose DEK resolver returns key, with the production busy timeout.
func newMultiProcessClient(t *testing.T, dataDir string, key []byte) *Client {
	t.Helper()
	c, err := NewClient(Config{
		DataDir:         dataDir,
		CacheTTL:        time.Hour,
		CacheMaxEntries: 4,
		CacheSweepEvery: time.Hour,
		BusyTimeout:     5 * time.Second,
		DEKResolver: func(context.Context, string) ([]byte, error) {
			return key, nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

// writeRows inserts multiProcessWritesEach rows tagged writer, each in its own transaction that counts the table
// before inserting, so every write reads before it writes.
func writeRows(ctx context.Context, c *Client, writer string) error {
	for i := range multiProcessWritesEach {
		err := c.WithTx(ctx, multiProcessDBID, false, func(ctx context.Context, tx service.Tx) error {
			count, err := tx.Exec(ctx, "SELECT COUNT(*) FROM items", nil)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "INSERT INTO items (writer, seen) VALUES (?, ?)",
				[]domain.TypedValue{{Type: domain.SQLText, Value: writer}, count.Rows[0][0]})
			return err
		})
		if err != nil {
			return fmt.Errorf("%s, write %d: %w", writer, i, err)
		}
	}
	return nil
}

// waitForFile polls until path exists or timeout passes.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("waiting for %s: %w", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("%s did not appear within %s", path, timeout)
}

// Processes TestOpen_FreshConnectionsWhileOthersWrite starts, and the fresh connections each opens in turn.
const (
	freshOpeners   = 20
	freshOpensEach = 20
)

// helperEnvFreshOpener marks a child process of TestOpen_FreshConnectionsWhileOthersWrite.
const helperEnvFreshOpener = "BAGUETTEDB_SQLITELOCAL_FRESH_OPENER_HELPER"

// TestOpen_FreshConnectionsWhileOthersWrite runs several processes on one bdb file at once, each opening fresh
// connections in turn that write a row and close, as concurrent one-shot commands do. A fresh connection reads the
// file before any other statement, while another process may be writing, recovering the WAL or checkpointing it on
// close: it checks that no open or write fails with the file locked, and that every row lands.
func TestOpen_FreshConnectionsWhileOthersWrite(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	key := newKey(t)

	setup := newMultiProcessClient(t, dataDir, key)
	if err := setup.CreateDatabase(ctx, multiProcessDBID, key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := setup.Execute(ctx, multiProcessDBID,
		"CREATE TABLE items (id INTEGER PRIMARY KEY, writer TEXT NOT NULL)", nil); err != nil {
		t.Fatalf("creating the table: %v", err)
	}
	// Closes the setup connection, so the children open and close the file's last connections themselves.
	if err := setup.EvictDatabase(ctx, multiProcessDBID); err != nil {
		t.Fatalf("EvictDatabase: %v", err)
	}

	children := make([]*exec.Cmd, freshOpeners)
	outputs := make([]*bytes.Buffer, freshOpeners)
	for i := range children {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFreshOpenerHelper$", "-test.v")
		cmd.Env = append(os.Environ(),
			helperEnvFreshOpener+"=1",
			helperEnvDataDir+"="+dataDir,
			helperEnvKey+"="+hex.EncodeToString(key),
			fmt.Sprintf("%s=child-%d", helperEnvWriter, i),
		)
		outputs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = outputs[i], outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting child %d: %v", i, err)
		}
		children[i] = cmd
	}
	if err := os.WriteFile(filepath.Join(dataDir, multiProcessStart), nil, 0o600); err != nil {
		t.Fatalf("writing the start file: %v", err)
	}
	for i, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, outputs[i])
		}
	}

	res, err := setup.Execute(ctx, multiProcessDBID, "SELECT COUNT(*) FROM items", nil)
	if err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if n := res.Rows[0][0].Value; n != int64(freshOpeners*freshOpensEach) {
		t.Errorf("got %v rows, want %d", n, freshOpeners*freshOpensEach)
	}
}

// TestFreshOpenerHelper is the child process of TestOpen_FreshConnectionsWhileOthersWrite: it waits for the start
// file in the data dir the parent names, then opens freshOpensEach connections in turn, each writing one row.
func TestFreshOpenerHelper(t *testing.T) {
	if os.Getenv(helperEnvFreshOpener) != "1" {
		t.Skip("runs only as a child process of TestOpen_FreshConnectionsWhileOthersWrite")
	}
	key, err := hex.DecodeString(os.Getenv(helperEnvKey))
	if err != nil {
		t.Fatalf("decoding the key: %v", err)
	}
	dataDir := os.Getenv(helperEnvDataDir)
	if err := waitForFile(filepath.Join(dataDir, multiProcessStart), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, multiProcessDBID+".db")
	for i := range freshOpensEach {
		if err := openWriteClose(context.Background(), path, key, os.Getenv(helperEnvWriter)); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
}

// openWriteClose opens path with key on a connection of its own, with the production busy timeout, inserts one row
// tagged writer and closes it.
func openWriteClose(ctx context.Context, path string, key []byte, writer string) error {
	conn, err := Open(path, key, 5*time.Second)
	if err != nil {
		return fmt.Errorf("opening: %w", err)
	}
	_, err = conn.Exec(ctx, "INSERT INTO items (writer) VALUES (?)", writer)
	if cerr := conn.Close(); cerr != nil {
		return errors.Join(err, fmt.Errorf("closing: %w", cerr))
	}
	return err
}
