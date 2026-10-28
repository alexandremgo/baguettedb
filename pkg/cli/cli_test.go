package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
)

// TestExitCode checks the exit status each kind of command error maps to, wrapped or not.
func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"no error", nil, exitOK},
		{"failure", errors.New("disk full"), exitFailure},
		{"usage error", usageError{err: errors.New(`unknown command "nope"`)}, exitUsage},
		{"wrapped usage error", fmt.Errorf("parsing: %w", usageError{err: errors.New("bad flag")}), exitUsage},
		{"cancelled", context.Canceled, exitInterrupted},
		{"wrapped cancellation", fmt.Errorf("serving: %w", context.Canceled), exitInterrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestApplyDataDir checks the settings derived from the data directory, and that a setting whose environment
// variable is set to a non-empty value is left as the environment had it.
func TestApplyDataDir(t *testing.T) {
	const dir = "/home/me/.baguettedb"

	t.Run("derives every setting", func(t *testing.T) {
		var cfg config.Config
		applyDataDir(&cfg, dir, fakeEnv(map[string]string{"BAGUETTEDB_DATABASES_DATA_DIR": ""}))

		if cfg.InstanceSQLitePath != "/home/me/.baguettedb/instance.sqlite" ||
			cfg.DatabasesDataDir != "/home/me/.baguettedb/databases" ||
			cfg.DatabasesKEKFile != "/home/me/.baguettedb/kek" ||
			!cfg.DatabasesKEKAutogenerate ||
			cfg.DatabasesBackupEnabled == nil || *cfg.DatabasesBackupEnabled {
			t.Errorf("derived config = %+v, want every setting derived from %s", cfg, dir)
		}
	})

	t.Run("keeps the environment's settings", func(t *testing.T) {
		backups := true
		cfg := config.Config{
			InstanceSQLitePath:     "/srv/instance.sqlite",
			DatabasesDataDir:       "/srv/databases",
			DatabasesKEKFile:       "/run/secrets/kek",
			DatabasesBackupEnabled: &backups,
		}
		applyDataDir(&cfg, dir, fakeEnv(map[string]string{
			"BAGUETTEDB_INSTANCE_SQLITE_PATH":       cfg.InstanceSQLitePath,
			"BAGUETTEDB_DATABASES_DATA_DIR":         cfg.DatabasesDataDir,
			"BAGUETTEDB_DATABASES_KEK_FILE":         cfg.DatabasesKEKFile,
			"BAGUETTEDB_DATABASES_KEK_AUTOGENERATE": "false",
			"BAGUETTEDB_DATABASES_BACKUP_ENABLED":   "true",
		}))

		if cfg.InstanceSQLitePath != "/srv/instance.sqlite" ||
			cfg.DatabasesDataDir != "/srv/databases" ||
			cfg.DatabasesKEKFile != "/run/secrets/kek" ||
			cfg.DatabasesKEKAutogenerate ||
			!*cfg.DatabasesBackupEnabled {
			t.Errorf("config = %+v, want the environment's settings untouched", cfg)
		}
	})
}

// fakeEnv returns a lookup over env alone, standing in for os.LookupEnv.
func fakeEnv(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}
