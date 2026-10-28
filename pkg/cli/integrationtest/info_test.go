//go:build integration

package integrationtest

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// infoReport is the part of info --json the tests read.
type infoReport struct {
	Binary struct {
		Name           string `json:"name"`
		CipherProvider string `json:"cipher_provider"`
	} `json:"binary"`
	DataDir struct {
		Path          string `json:"path"`
		Source        string `json:"source"`
		Exists        bool   `json:"exists"`
		KeyOwnerOnly  bool   `json:"key_owner_only"`
		BdbFiles      int    `json:"bdb_files"`
		InstanceStore struct {
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"instance_store"`
	} `json:"data_dir"`
	Instance *struct {
		Owner *struct {
			Email string `json:"email"`
		} `json:"owner"`
		AsFound  *bool `json:"as_found"`
		Bdbs     int   `json:"bdbs"`
		Accounts int   `json:"accounts"`
		Schemas  []struct {
			App     string `json:"app"`
			Version uint   `json:"version"`
			Latest  uint   `json:"latest"`
		} `json:"schemas"`
	} `json:"instance"`
	Server *struct {
		URL string `json:"url"`
	} `json:"server"`
	MCPName string `json:"mcp_name"`
}

// runInfo runs info --json with args before it and decodes the report, failing the test unless it exits 0.
func runInfo(t *testing.T, args ...string) infoReport {
	t.Helper()
	code, stdout, stderr := runCLI(t, append(append([]string{"bdb"}, args...), "info", "--json")...)
	if code != exitOK {
		t.Fatalf("info %v = %d, stderr %q", args, code, stderr)
	}
	var report infoReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("info --json is not the report: %v\n%s", err, stdout)
	}
	return report
}

// TestRun_InfoMissingDataDir checks that info on a data directory that doesn't exist says so, in text with how to
// create it and in JSON, and creates nothing.
func TestRun_InfoMissingDataDir(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), "nowhere")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "info")
	want := dataDir + " (from --data-dir): not created yet; `bdb init` creates it"
	if code != exitOK || !strings.Contains(stdout, want) {
		t.Errorf("info = %d %q, stderr %q; want the directory reported as not created yet", code, stdout, stderr)
	}
	if report := runInfo(t, "--data-dir", dataDir); report.DataDir.Exists || report.Instance != nil {
		t.Errorf("info --json = %+v, want a missing data directory and no instance", report)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after info, stat %s = %v; want nothing created", dataDir, err)
	}
}

// TestRun_Info checks info on a data directory in use: chosen by --data-dir or by the environment, the owner-only key,
// the owner, the account --as acts as (or an unknown one), the counts, the schemas at this binary's versions, the bdb
// file, the crypto provider, the server once bdb serve runs, the MCP name; and that it changes nothing in the store.
func TestRun_Info(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) {
		t.Helper()
		code, _, stderr := runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
		if code != exitOK {
			t.Fatalf("%v = %d, stderr %q", args, code, stderr)
		}
	}
	bdb("owner@example.com\nOwner Person\nowner-pass-1\nowner-pass-1\n", "init")
	bdb("", "bdbs", "create", "Notes")
	bdb("bob-password-1\n", "users", "create", "bob@example.com", "--password-stdin")

	store := filepath.Join(dataDir, "instance.sqlite")
	before, err := os.Stat(store)
	if err != nil {
		t.Fatal(err)
	}
	report := runInfo(t, "--data-dir", dataDir, "--as", "bob@example.com")
	in := report.Instance
	switch {
	case report.Binary.Name != "bdb" || report.Binary.CipherProvider == "":
		t.Errorf("binary = %+v, want bdb and a crypto provider", report.Binary)
	case report.DataDir.Path != dataDir || report.DataDir.Source != "flag" || !report.DataDir.Exists ||
		!report.DataDir.KeyOwnerOnly || report.DataDir.BdbFiles != 1 || report.DataDir.InstanceStore.Path != store:
		t.Errorf("data dir = %+v, want %s from the flag, an owner-only key, one bdb file, the store", report.DataDir,
			dataDir)
	case in == nil || in.Owner == nil || in.Owner.Email != "owner@example.com" || in.AsFound == nil || !*in.AsFound ||
		in.Bdbs != 1 || in.Accounts != 2:
		t.Errorf("instance = %+v, want the owner, bob found, 1 bdb and 2 accounts", in)
	case report.Server != nil || report.MCPName != "bdb-local-bob":
		t.Errorf("server %+v, MCP name %q; want none and bdb-local-bob", report.Server, report.MCPName)
	}
	if in != nil {
		for _, s := range in.Schemas {
			if s.Version == 0 || s.Version != s.Latest {
				t.Errorf("schema %+v, want the app at this binary's latest version", s)
			}
		}
	}
	after, err := os.Stat(store)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("info changed the instance store: %v → %v (%v)", before.ModTime(), after.ModTime(), err)
	}

	code, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "--as", "nobody@example.com", "info")
	if code != exitOK || !strings.Contains(stdout, "nobody@example.com (--as), no such account") {
		t.Errorf("info --as an unknown account = %d %q, want it reported as unknown", code, stdout)
	}

	t.Setenv("BAGUETTEDB_DATA_DIR", dataDir)
	if report := runInfo(t); report.DataDir.Source != "env" || report.DataDir.Path != dataDir {
		t.Errorf("info with $BAGUETTEDB_DATA_DIR = %+v, want %s from the environment", report.DataDir, dataDir)
	}

	baseURL, _, stop := startServe(t, dataDir)
	defer stop()
	// The server records itself once it serves; its start lines come first.
	deadline := time.Now().Add(5 * time.Second)
	for report = runInfo(t); report.Server == nil && time.Now().Before(deadline); report = runInfo(t) {
		time.Sleep(50 * time.Millisecond)
	}
	if report.Server == nil || report.Server.URL != baseURL {
		t.Errorf("server while bdb serve runs = %+v, want %s", report.Server, baseURL)
	}
}
