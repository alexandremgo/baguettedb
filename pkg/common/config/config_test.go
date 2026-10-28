package config

import (
	"os"
	"strings"
	"testing"
)

// TestLoad_PeerURLTemplate checks that the peer-URL template is required, with both substitution tokens, only when a
// pod name marks the process as part of a sharded deployment.
func TestLoad_PeerURLTemplate(t *testing.T) {
	tests := []struct {
		name     string
		podName  string
		template string
		wantErr  string
	}{
		{
			name:     "monolith without pod name skips the template check",
			podName:  "",
			template: "",
		},
		{
			name:     "sharded pod with a complete template",
			podName:  "db-blue-1",
			template: "http://db-{color}-{ordinal}.db-{color}:8002",
		},
		{
			name:    "sharded pod without a template",
			podName: "db-blue-1",
			wantErr: "BAGUETTEDB_DATABASES_PEER_URL_TEMPLATE is required when BAGUETTEDB_DATABASES_POD_NAME is set",
		},
		{
			name:     "sharded pod with a template missing the color token",
			podName:  "db-blue-1",
			template: "http://db-{ordinal}:8002",
			wantErr:  "must contain the {color} token",
		},
		{
			name:     "sharded pod with a template missing the ordinal token",
			podName:  "db-blue-1",
			template: "http://db-{color}:8002",
			wantErr:  "must contain the {ordinal} token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BAGUETTEDB_DATABASES_POD_NAME", tt.podName)
			t.Setenv("BAGUETTEDB_DATABASES_PEER_URL_TEMPLATE", tt.template)

			cfg, err := Load()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() unexpected error: %v", err)
				}
				if cfg.DatabasesPeerURLTemplate != tt.template {
					t.Fatalf("DatabasesPeerURLTemplate = %q, want %q", cfg.DatabasesPeerURLTemplate, tt.template)
				}

				return
			}
			if err == nil {
				t.Fatalf("Load() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// unsetenv removes key for the duration of the test and restores its original state afterwards, so a case can
// observe the "variable absent" path even when the developer's shell exports it.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetting %s: %v", key, err)
	}
}

// TestLoad_UsageCapsDefaultToUnlimited pins that the per-user usage caps have no built-in fallback: an instance that
// sets nothing runs unlimited, while the safety caps keep their defaults.
func TestLoad_UsageCapsDefaultToUnlimited(t *testing.T) {
	for _, key := range []string{
		"BAGUETTEDB_QUOTA_MAX_BDBS", "BAGUETTEDB_QUOTA_MAX_DB_SIZE_BYTES", "BAGUETTEDB_QUOTA_MAX_MEMBERS_PER_BDB",
	} {
		unsetenv(t, key)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.QuotaMaxBdbs != 0 || cfg.QuotaMaxDBSizeBytes != 0 || cfg.QuotaMaxMembersPerBdb != 0 {
		t.Fatalf("usage caps = (%d, %d, %d), want all 0 (unlimited)",
			cfg.QuotaMaxBdbs, cfg.QuotaMaxDBSizeBytes, cfg.QuotaMaxMembersPerBdb)
	}
	if cfg.QuotaUndoStackMaxSize != 50 {
		t.Fatalf("QuotaUndoStackMaxSize = %d, want the 50 safety default", cfg.QuotaUndoStackMaxSize)
	}
}

// TestLoad_SecureCookiesFollowsPublicURLScheme checks that the cookie Secure flag derives from the public URL scheme
// when unset and that an explicit value always wins.
func TestLoad_SecureCookiesFollowsPublicURLScheme(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		explicit  string
		want      bool
	}{
		{name: "https public URL", publicURL: "https://bdb.example.com", want: true},
		{name: "http public URL", publicURL: "http://192.168.1.10:8080", want: false},
		{name: "default public URL is localhost over http", publicURL: "", want: false},
		{name: "explicit true over http", publicURL: "http://192.168.1.10:8080", explicit: "true", want: true},
		{name: "explicit false over https", publicURL: "https://bdb.example.com", explicit: "false", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BAGUETTEDB_PUBLIC_URL", tt.publicURL)
			if tt.explicit == "" {
				unsetenv(t, "BAGUETTEDB_SECURE_COOKIES")
			} else {
				t.Setenv("BAGUETTEDB_SECURE_COOKIES", tt.explicit)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.SecureCookies == nil || *cfg.SecureCookies != tt.want {
				t.Fatalf("SecureCookies = %v, want %v", cfg.SecureCookies, tt.want)
			}
		})
	}
}

// TestLoad_KEKAutogenerateAndCSPExtras checks that the KEK autogenerate flag and the web UI CSP extras load from the
// environment.
func TestLoad_KEKAutogenerateAndCSPExtras(t *testing.T) {
	t.Setenv("BAGUETTEDB_DATABASES_KEK_AUTOGENERATE", "true")
	t.Setenv("BAGUETTEDB_WEBUI_CSP_SCRIPT_SRC_EXTRA", "https://scripts.example.com")
	t.Setenv("BAGUETTEDB_WEBUI_CSP_CONNECT_SRC_EXTRA", "https://api.example.com wss://api.example.com")
	t.Setenv("BAGUETTEDB_WEBUI_CSP_IMG_SRC_EXTRA", "https://img.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if !cfg.DatabasesKEKAutogenerate {
		t.Fatal("DatabasesKEKAutogenerate = false, want true")
	}
	if cfg.WebUICSPScriptSrcExtra != "https://scripts.example.com" ||
		cfg.WebUICSPConnectSrcExtra != "https://api.example.com wss://api.example.com" ||
		cfg.WebUICSPImgSrcExtra != "https://img.example.com" {
		t.Fatalf("CSP extras = (%q, %q, %q), want the env values",
			cfg.WebUICSPScriptSrcExtra, cfg.WebUICSPConnectSrcExtra, cfg.WebUICSPImgSrcExtra)
	}
}
