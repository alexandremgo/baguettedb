// Package config holds the configuration of a BaguetteDB process, read from the BAGUETTEDB_-prefixed environment
// variables into a single Config: the instance store, each bdb's encrypted database file and its snapshots, the
// authorization server, telemetry and the quota caps. Load fills unset settings with defaults and rejects invalid ones.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
	"github.com/rs/zerolog"
)

// envPrefix is the prefix of the environment variables the configuration is read from.
const envPrefix = "BAGUETTEDB_"

// Default per-request SQL caps, applied by Load when the matching BAGUETTEDB_QUOTA_* variable is unset or zero.
const (
	DefaultQuotaMaxStatementsPerRequest = 100
	DefaultQuotaMaxRowsPerRequest       = 1000
	DefaultQuotaMaxQueryResultRows      = 1000
	DefaultQuotaMaxConcurrentQueries    = 4
)

// Config holds the application configuration.
type Config struct {
	Port int `koanf:"port"`

	// Database DSNs — set directly via BAGUETTEDB_AUTH_DATABASE_DSN, or built
	// from individual components (BAGUETTEDB_DB_HOST, BAGUETTEDB_DB_PORT, and
	// per-service BAGUETTEDB_AUTH_DB_NAME, BAGUETTEDB_AUTH_DB_USER, BAGUETTEDB_AUTH_DB_PASSWORD).
	AuthDatabaseDSN      string `koanf:"auth_database_dsn"`
	BdbsDatabaseDSN      string `koanf:"bdbs_database_dsn"`
	DatabasesDatabaseDSN string `koanf:"databases_database_dsn"`

	// InstanceSQLitePath is the SQLite file the auth, bdbs and databases apps keep their records in, in place
	// of the three Postgres databases. Empty (the default) keeps Postgres.
	InstanceSQLitePath string `koanf:"instance_sqlite_path"`

	// Database components — used to build DSNs when the DSN fields are empty.
	DBHost              string `koanf:"db_host"`
	DBPort              string `koanf:"db_port"`
	AuthDBName          string `koanf:"auth_db_name"`
	AuthDBUser          string `koanf:"auth_db_user"`
	AuthDBPassword      string `koanf:"auth_db_password"`
	BdbsDBName          string `koanf:"bdbs_db_name"`
	BdbsDBUser          string `koanf:"bdbs_db_user"`
	BdbsDBPassword      string `koanf:"bdbs_db_password"`
	DatabasesDBName     string `koanf:"databases_db_name"`
	DatabasesDBUser     string `koanf:"databases_db_user"`
	DatabasesDBPassword string `koanf:"databases_db_password"`

	// DatabasesKEKFile is the path to a file holding the 32-byte raw
	// key-encryption key used by the databases module to wrap/unwrap
	// per-bdb DEKs. Required when the databases module wires a KEK-backed
	// adapter; Setup fails fast if the path is empty or the file is missing.
	DatabasesKEKFile string `koanf:"databases_kek_file"`

	// DatabasesKEKAutogenerate generates a fresh 32-byte KEK at DatabasesKEKFile on startup when no file exists
	// there. Off by default, so a deployment that forgot to mount its key fails loudly instead of minting one nobody
	// backed up.
	DatabasesKEKAutogenerate bool `koanf:"databases_kek_autogenerate"`

	// DatabasesDataDir is the on-disk directory holding per-bdb SQLCipher
	// database files. Required: Setup fails fast if empty (mirrors the
	// DatabasesKEKFile bar — operators must make a deliberate choice about
	// persistent storage location).
	DatabasesDataDir string `koanf:"databases_data_dir"`

	// DatabasesSQLiteCacheTTL is the idle duration after which a cached
	// SQLCipher connector is closed by the sweeper. Defaults to 30 minutes
	// when unset.
	DatabasesSQLiteCacheTTL time.Duration `koanf:"databases_sqlite_cache_ttl"`

	// DatabasesSQLiteCacheMaxEntries caps the number of simultaneously open
	// SQLCipher connectors in the connector cache. LRU eviction kicks in
	// above this count. Defaults to 500 when unset.
	DatabasesSQLiteCacheMaxEntries int `koanf:"databases_sqlite_cache_max_entries"`

	// DatabasesSQLiteBusyTimeout is how long a statement on a bdb file waits for a lock another connection or process
	// holds on it before failing. Defaults to 5 seconds when unset.
	DatabasesSQLiteBusyTimeout time.Duration `koanf:"databases_sqlite_busy_timeout"`

	// DatabasesMaxQueryDuration caps every Execute / ExecuteTransaction call
	// in the databases module via a service-layer timeout decorator. Defense
	// in depth: the adapter binding already honours upstream ctx cancellation,
	// this protects callers that pass context.Background(). Defaults to 30s
	// when unset.
	DatabasesMaxQueryDuration time.Duration `koanf:"databases_max_query_duration"`

	// DatabasesFormulaRunTimeout bounds a single formula run from the app, separately from (and
	// typically shorter than) DatabasesMaxQueryDuration: app-initiated runs should feel snappy, and
	// exceeding this surfaces a distinct "too big to run in the app" error so the UI can point the
	// user at their AI assistant instead. Defaults to 5s when unset.
	DatabasesFormulaRunTimeout time.Duration `koanf:"databases_formula_run_timeout"`

	// DatabasesS3Endpoint is the S3-compatible endpoint URL for database snapshot storage. Empty means snapshot
	// storage is not configured and backup features stay disabled.
	DatabasesS3Endpoint string `koanf:"databases_s3_endpoint"`

	// DatabasesS3Region is the S3 region name. Self-hosted endpoints generally ignore it; defaults to
	// "gra" in the adapter when empty.
	DatabasesS3Region string `koanf:"databases_s3_region"`

	// DatabasesS3Bucket is the bucket holding snapshot objects. Required when DatabasesS3Endpoint is set;
	// the bucket itself is provisioned by the deployment, never created by the application.
	DatabasesS3Bucket string `koanf:"databases_s3_bucket"`

	// DatabasesS3Prefix is an optional key prefix prepended to every snapshot object key (e.g. an environment
	// name), isolating environments sharing one bucket.
	DatabasesS3Prefix string `koanf:"databases_s3_prefix"`

	// DatabasesS3AccessKeyID and DatabasesS3SecretAccessKey are the static credentials for the S3 endpoint.
	DatabasesS3AccessKeyID     string `koanf:"databases_s3_access_key_id"`
	DatabasesS3SecretAccessKey string `koanf:"databases_s3_secret_access_key"`

	// DatabasesS3UsePathStyle switches the S3 client to path-style addressing (`endpoint/bucket/key`).
	// Required by most self-hosted S3 implementations.
	DatabasesS3UsePathStyle bool `koanf:"databases_s3_use_path_style"`

	// DatabasesBackupInterval is how often the backup runner sweeps for databases changed since their last
	// snapshot. It is the backup RPO: at most this much data is lost when a volume dies. Defaults to 15m.
	DatabasesBackupInterval time.Duration `koanf:"databases_backup_interval"`

	// DatabasesBackupEnabled toggles the periodic backup runner. Defaults to true. The internal on-demand
	// snapshot endpoints stay available regardless — this is the rollout kill switch for when the snapshot
	// path itself blocks an urgent deploy.
	DatabasesBackupEnabled *bool `koanf:"databases_backup_enabled"`

	// DatabasesBackupMaxSnapshotAge is the freshness floor: an unchanged database is still re-snapshotted when
	// its newest stored snapshot is older than this. Must stay well under the bucket's lifecycle expiry so an
	// idle database always keeps at least one live snapshot object. Defaults to 84h (half the 7-day expiry).
	DatabasesBackupMaxSnapshotAge time.Duration `koanf:"databases_backup_max_snapshot_age"`

	// DatabasesReconcileMode controls the boot reconcile pass that restores missing database files from their
	// latest snapshots: "strict" (default) fails startup when a restore fails, so a half-recovered instance
	// never serves traffic; "warn" logs loudly and starts anyway — the force-deploy escape hatch when the
	// snapshot store itself is down.
	DatabasesReconcileMode string `koanf:"databases_reconcile_mode"`

	// DatabasesPodName is this instance's name in a sharded deployment, as the orchestrator assigns it. The shard
	// ordinal is parsed from its numeric suffix (e.g. "db-2" → 2). Empty in single-pod and monolith deployments,
	// which means ordinal 0 and no shard routing.
	DatabasesPodName string `koanf:"databases_pod_name"`

	// DatabasesShardCount is the number of databases pods across which bdbs are sharded. Defaults to 1
	// (single-pod): shard routing is disabled entirely and every request is served locally.
	DatabasesShardCount int `koanf:"databases_shard_count"`

	// DatabasesPeerURLTemplate builds the base URL of a peer pod from its fleet and shard ordinal by substituting
	// the {color} (blue/green) and {ordinal} tokens, e.g. "http://db-{color}-{ordinal}.example.internal:8002" with
	// per-pod DNS. Required, with both tokens present, whenever DatabasesPodName is set; unused otherwise.
	DatabasesPeerURLTemplate string `koanf:"databases_peer_url_template"`

	// DatabasesPodColor is this pod's blue/green fleet (0 = blue, 1 = green), set per fleet in a two-fleet
	// deployment. 0 in single-fleet and monolith deployments. Together with the pod's shard ordinal it identifies
	// which databases this pod owns.
	DatabasesPodColor int `koanf:"databases_pod_color"`

	HydraPublicURL  string `koanf:"hydra_public_url"`
	HydraAdminURL   string `koanf:"hydra_admin_url"`
	HydraIssuerURL  string `koanf:"hydra_issuer_url"`
	AdminEmail      string `koanf:"admin_email"`
	AdminPassword   string `koanf:"admin_password"`
	PublicURL       string `koanf:"public_url"`
	AuthURL         string `koanf:"auth_url"`
	BdbsURL         string `koanf:"bdbs_url"`
	DatabasesURL    string `koanf:"databases_url"`
	OtelEnabled     bool   `koanf:"otel_enabled"`
	OtelEndpoint    string `koanf:"otel_endpoint"`
	OtelHeaders     string `koanf:"otel_headers"`
	OtelInsecure    bool   `koanf:"otel_insecure"`
	OtelEnvironment string `koanf:"otel_environment"`

	// Quota limits. Zero disables a cap. The usage caps (bdbs per user, database size, members per bdb) have no
	// built-in default, so a deployment that sets nothing runs unlimited.
	QuotaMaxBdbs                 int           `koanf:"quota_max_bdbs"`
	QuotaMaxDBSizeBytes          int64         `koanf:"quota_max_db_size_bytes"`
	QuotaMaxTotalDBSizeBytes     int64         `koanf:"quota_max_total_db_size_bytes"`
	QuotaMaxMembersPerBdb        int           `koanf:"quota_max_members_per_bdb"`
	QuotaUndoStackMaxSize        int           `koanf:"quota_undo_stack_max_size"`
	QuotaAffectedRowsMax         int           `koanf:"quota_affected_rows_max"`
	QuotaMaxStatementsPerRequest int           `koanf:"quota_max_statements_per_request"`
	QuotaMaxRowsPerRequest       int           `koanf:"quota_max_rows_per_request"`
	QuotaMaxQueryResultRows      int           `koanf:"quota_max_query_result_rows"`
	QuotaMaxConcurrentQueries    int           `koanf:"quota_max_concurrent_queries"`
	QuotaSnapshotMaxRows         int           `koanf:"quota_snapshot_max_rows"`
	QuotaMaxDBExportsPerWindow   int           `koanf:"quota_max_db_exports_per_window"`
	QuotaDBExportWindow          time.Duration `koanf:"quota_db_export_window"`
	QuotaMaxFormulaExportRows    int           `koanf:"quota_max_formula_export_rows"`

	// WebClientID is the OAuth2 client ID of the web frontend, sent by the cookie token handler on refresh requests.
	// Empty means the auth module provisions a first-party public client named baguettedb-web in Hydra at startup
	// and uses that.
	WebClientID string `koanf:"web_client_id"`

	// SecureCookies sets the Secure flag on the httpOnly auth cookies. When unset it follows the PublicURL scheme
	// (https → true), so an instance served over plain HTTP keeps its cookies. Set explicitly to override.
	SecureCookies *bool `koanf:"secure_cookies"`

	// AppBaseURL is the public base URL of the web frontend (http://localhost:5173 by default).
	// Used to construct invite links that users share.
	AppBaseURL string `koanf:"app_base_url"`

	// WebUICSPScriptSrcExtra, WebUICSPConnectSrcExtra and WebUICSPImgSrcExtra are extra Content-Security-Policy
	// sources (space-separated) appended to the script-src, connect-src and img-src directives of the embedded web
	// UI's document policy.
	WebUICSPScriptSrcExtra  string `koanf:"webui_csp_script_src_extra"`
	WebUICSPConnectSrcExtra string `koanf:"webui_csp_connect_src_extra"`
	WebUICSPImgSrcExtra     string `koanf:"webui_csp_img_src_extra"`

	// BdbsPurgeInterval is how often the bdbs service sweeps for
	// soft-deleted bdbs whose 7-day grace period has expired and hard-deletes
	// them. Defaults to 1h; can be shortened for local dev or extended for ops
	// tuning. Has no effect on the user-facing grace-period duration.
	BdbsPurgeInterval time.Duration `koanf:"bdbs_purge_interval"`

	// MCPDescribeBdbSkillBodyBudgetBytes caps the total bytes of skill bodies inlined in one describe_bdb response.
	// At or under the cap every skill body (bdb-level and table-targeted) is inlined; over it, table-targeted
	// bodies degrade to summaries and the response's `omitted` note says so. Defaults to 16 KiB — generous enough
	// that every builtin starter inlines fully.
	MCPDescribeBdbSkillBodyBudgetBytes int `koanf:"mcp_describe_bdb_skill_body_budget_bytes"`

	// MCPDescribeBdbSchemaTableLimit caps how many tables a bdb may have before describe_bdb stops inlining
	// column-level schemas. At or under the limit every table entry carries its columns and foreign keys; over it,
	// all entries degrade to name+type and the response's `omitted` note says so. Defaults to 20.
	MCPDescribeBdbSchemaTableLimit int `koanf:"mcp_describe_bdb_schema_table_limit"`

	// ShutdownTimeout caps how long each HTTP server is given to drain in-flight requests after a SIGINT/SIGTERM before
	// the process exits. Should not exceed the grace period the process manager allows before killing the process.
	// Defaults to 30s.
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`

	// MCPGatewayBackendInitTimeout caps how long the MCP gateway retries connecting to each backend MCP server
	// (bdbs, databases) at startup before giving up, so a briefly-unreachable backend (a rolling restart or a
	// blue/green cutover) doesn't crash-loop the gateway. Defaults to 60s.
	MCPGatewayBackendInitTimeout time.Duration `koanf:"mcp_gateway_backend_init_timeout"`

	// MaxRequestBodySize caps the HTTP request body size in bytes.
	MaxRequestBodySize int64 `koanf:"max_request_body_size"`

	// InternalAPISecret is the shared secret required on internal service-to-service endpoints (/internal/*):
	// callers send it in the X-Internal-Api-Secret header and requests missing or mismatching it are rejected.
	// Required by the split-service binaries.
	InternalAPISecret string `koanf:"internal_api_secret"`
}

// LogValues logs all non-secret configuration values at info level.
func (c Config) LogValues(logger zerolog.Logger) {
	logger.Info().
		Int("port", c.Port).
		Str("public_url", c.PublicURL).
		Str("auth_url", c.AuthURL).
		Str("bdbs_url", c.BdbsURL).
		Str("databases_url", c.DatabasesURL).
		Str("hydra_public_url", c.HydraPublicURL).
		Str("hydra_admin_url", c.HydraAdminURL).
		Str("hydra_issuer_url", c.HydraIssuerURL).
		Str("app_base_url", c.AppBaseURL).
		Str("web_client_id", c.WebClientID).
		Str("webui_csp_script_src_extra", c.WebUICSPScriptSrcExtra).
		Str("webui_csp_connect_src_extra", c.WebUICSPConnectSrcExtra).
		Str("webui_csp_img_src_extra", c.WebUICSPImgSrcExtra).
		Bool("secure_cookies", c.SecureCookies != nil && *c.SecureCookies).
		Int64("max_request_body_size", c.MaxRequestBodySize).
		Dur("bdbs_purge_interval", c.BdbsPurgeInterval).
		Int("mcp_describe_bdb_skill_body_budget_bytes", c.MCPDescribeBdbSkillBodyBudgetBytes).
		Int("mcp_describe_bdb_schema_table_limit", c.MCPDescribeBdbSchemaTableLimit).
		Dur("shutdown_timeout", c.ShutdownTimeout).
		Dur("mcp_gateway_backend_init_timeout", c.MCPGatewayBackendInitTimeout).
		Int("quota_max_bdbs", c.QuotaMaxBdbs).
		Int64("quota_max_db_size_bytes", c.QuotaMaxDBSizeBytes).
		Int64("quota_max_total_db_size_bytes", c.QuotaMaxTotalDBSizeBytes).
		Int("quota_max_members_per_bdb", c.QuotaMaxMembersPerBdb).
		Int("quota_undo_stack_max_size", c.QuotaUndoStackMaxSize).
		Int("quota_affected_rows_max", c.QuotaAffectedRowsMax).
		Int("quota_max_statements_per_request", c.QuotaMaxStatementsPerRequest).
		Int("quota_max_rows_per_request", c.QuotaMaxRowsPerRequest).
		Int("quota_max_query_result_rows", c.QuotaMaxQueryResultRows).
		Int("quota_max_concurrent_queries", c.QuotaMaxConcurrentQueries).
		Int("quota_snapshot_max_rows", c.QuotaSnapshotMaxRows).
		Int("quota_max_db_exports_per_window", c.QuotaMaxDBExportsPerWindow).
		Dur("quota_db_export_window", c.QuotaDBExportWindow).
		Int("quota_max_formula_export_rows", c.QuotaMaxFormulaExportRows).
		Dur("databases_max_query_duration", c.DatabasesMaxQueryDuration).
		Dur("databases_formula_run_timeout", c.DatabasesFormulaRunTimeout).
		Str("instance_sqlite_path", c.InstanceSQLitePath).
		Str("databases_data_dir", c.DatabasesDataDir).
		Bool("databases_kek_autogenerate", c.DatabasesKEKAutogenerate).
		Dur("databases_sqlite_cache_ttl", c.DatabasesSQLiteCacheTTL).
		Int("databases_sqlite_cache_max_entries", c.DatabasesSQLiteCacheMaxEntries).
		Dur("databases_sqlite_busy_timeout", c.DatabasesSQLiteBusyTimeout).
		Str("databases_s3_endpoint", c.DatabasesS3Endpoint).
		Str("databases_s3_region", c.DatabasesS3Region).
		Str("databases_s3_bucket", c.DatabasesS3Bucket).
		Str("databases_s3_prefix", c.DatabasesS3Prefix).
		Bool("databases_s3_use_path_style", c.DatabasesS3UsePathStyle).
		Dur("databases_backup_interval", c.DatabasesBackupInterval).
		Bool("databases_backup_enabled", c.DatabasesBackupEnabled != nil && *c.DatabasesBackupEnabled).
		Dur("databases_backup_max_snapshot_age", c.DatabasesBackupMaxSnapshotAge).
		Str("databases_reconcile_mode", c.DatabasesReconcileMode).
		Str("databases_pod_name", c.DatabasesPodName).
		Int("databases_shard_count", c.DatabasesShardCount).
		Str("databases_peer_url_template", c.DatabasesPeerURLTemplate).
		Int("databases_pod_color", c.DatabasesPodColor).
		Bool("otel_enabled", c.OtelEnabled).
		Str("otel_endpoint", c.OtelEndpoint).
		Bool("otel_insecure", c.OtelInsecure).
		Str("otel_environment", c.OtelEnvironment).
		Msg("loaded configuration")
}

// BdbsMCPURL returns the base URL of the bdbs MCP server (same as BdbsURL).
func (c Config) BdbsMCPURL() string { return c.BdbsURL }

// DatabasesMCPURL returns the base URL of the databases MCP server (same as DatabasesURL).
func (c Config) DatabasesMCPURL() string { return c.DatabasesURL }

// Load reads configuration from environment variables prefixed with BAGUETTEDB_.
func Load() (Config, error) {
	k := koanf.New(".")

	err := k.Load(env.Provider(envPrefix, ".", func(s string) string {
		return strings.ToLower(strings.TrimPrefix(s, envPrefix))
	}), nil)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, err
	}

	if cfg.Port == 0 {
		cfg.Port = 8080
	}
	if cfg.HydraPublicURL == "" {
		cfg.HydraPublicURL = "http://localhost:4444"
	}
	if cfg.HydraAdminURL == "" {
		cfg.HydraAdminURL = "http://localhost:4445"
	}
	if cfg.HydraIssuerURL == "" {
		cfg.HydraIssuerURL = cfg.HydraPublicURL
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = "http://localhost:8000"
	}
	if cfg.BdbsURL == "" {
		cfg.BdbsURL = "http://localhost:8001"
	}
	if cfg.DatabasesURL == "" {
		cfg.DatabasesURL = "http://localhost:8002"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = fmt.Sprintf("http://localhost:%d", cfg.Port)
	}
	if cfg.OtelEndpoint == "" {
		cfg.OtelEndpoint = "localhost:4317"
	}
	if cfg.OtelEnvironment == "" {
		cfg.OtelEnvironment = "dev"
	}

	if cfg.BdbsPurgeInterval <= 0 {
		cfg.BdbsPurgeInterval = time.Hour
	}
	if cfg.MCPDescribeBdbSkillBodyBudgetBytes <= 0 {
		cfg.MCPDescribeBdbSkillBodyBudgetBytes = 16 * 1024 // keep in sync with the bdbs MCP default
	}
	if cfg.MCPDescribeBdbSchemaTableLimit <= 0 {
		cfg.MCPDescribeBdbSchemaTableLimit = 20 // keep in sync with the bdbs MCP default
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}
	if cfg.MCPGatewayBackendInitTimeout <= 0 {
		cfg.MCPGatewayBackendInitTimeout = 60 * time.Second
	}

	// Quota defaults. The usage caps (max bdbs, max database size, members per bdb) deliberately have none.
	if cfg.QuotaUndoStackMaxSize == 0 {
		cfg.QuotaUndoStackMaxSize = 50
	}
	if cfg.QuotaAffectedRowsMax == 0 {
		cfg.QuotaAffectedRowsMax = 100
	}
	if cfg.QuotaMaxStatementsPerRequest == 0 {
		cfg.QuotaMaxStatementsPerRequest = DefaultQuotaMaxStatementsPerRequest
	}
	if cfg.QuotaMaxRowsPerRequest == 0 {
		cfg.QuotaMaxRowsPerRequest = DefaultQuotaMaxRowsPerRequest
	}
	if cfg.QuotaMaxQueryResultRows == 0 {
		cfg.QuotaMaxQueryResultRows = DefaultQuotaMaxQueryResultRows
	}
	if cfg.QuotaMaxConcurrentQueries == 0 {
		cfg.QuotaMaxConcurrentQueries = DefaultQuotaMaxConcurrentQueries
	}
	if cfg.QuotaSnapshotMaxRows == 0 {
		cfg.QuotaSnapshotMaxRows = 10000
	}
	if cfg.QuotaMaxDBExportsPerWindow == 0 {
		cfg.QuotaMaxDBExportsPerWindow = 1
	}
	if cfg.QuotaDBExportWindow <= 0 {
		cfg.QuotaDBExportWindow = 24 * time.Hour
	}
	if cfg.QuotaMaxFormulaExportRows == 0 {
		cfg.QuotaMaxFormulaExportRows = 100000
	}

	if cfg.DatabasesMaxQueryDuration <= 0 {
		cfg.DatabasesMaxQueryDuration = 30 * time.Second
	}

	if cfg.DatabasesFormulaRunTimeout <= 0 {
		cfg.DatabasesFormulaRunTimeout = 5 * time.Second
	}

	if cfg.DatabasesSQLiteCacheTTL <= 0 {
		cfg.DatabasesSQLiteCacheTTL = 30 * time.Minute
	}
	if cfg.DatabasesSQLiteBusyTimeout <= 0 {
		cfg.DatabasesSQLiteBusyTimeout = 5 * time.Second
	}
	if cfg.DatabasesBackupInterval <= 0 {
		cfg.DatabasesBackupInterval = 15 * time.Minute
	}
	if cfg.DatabasesBackupEnabled == nil {
		t := true
		cfg.DatabasesBackupEnabled = &t
	}
	if cfg.DatabasesBackupMaxSnapshotAge <= 0 {
		cfg.DatabasesBackupMaxSnapshotAge = 84 * time.Hour
	}
	switch cfg.DatabasesReconcileMode {
	case "":
		cfg.DatabasesReconcileMode = "strict"
	case "strict", "warn":
	default:
		return Config{}, fmt.Errorf(
			"BAGUETTEDB_DATABASES_RECONCILE_MODE must be \"strict\" or \"warn\", got %q", cfg.DatabasesReconcileMode)
	}

	if cfg.DatabasesShardCount <= 0 {
		cfg.DatabasesShardCount = 1
	}
	if cfg.DatabasesPodName != "" {
		if err := validatePeerURLTemplate(cfg.DatabasesPeerURLTemplate); err != nil {
			return Config{}, err
		}
	}
	if cfg.DatabasesSQLiteCacheMaxEntries <= 0 {
		cfg.DatabasesSQLiteCacheMaxEntries = 500
	}

	if cfg.SecureCookies == nil {
		secure := strings.HasPrefix(cfg.PublicURL, "https://")
		cfg.SecureCookies = &secure
	}

	if cfg.AppBaseURL == "" {
		cfg.AppBaseURL = "http://localhost:5173"
	}

	if cfg.MaxRequestBodySize == 0 {
		cfg.MaxRequestBodySize = 1024 * 1024 // 1 MB
	}

	// Builds DSNs from individual components when the DSN fields are empty.
	if cfg.DBHost == "" {
		cfg.DBHost = "localhost"
	}
	if cfg.DBPort == "" {
		cfg.DBPort = "5432"
	}
	if cfg.AuthDatabaseDSN == "" && cfg.AuthDBName != "" {
		cfg.AuthDatabaseDSN = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			cfg.AuthDBUser, cfg.AuthDBPassword, cfg.DBHost, cfg.DBPort, cfg.AuthDBName)
	}
	if cfg.BdbsDatabaseDSN == "" && cfg.BdbsDBName != "" {
		cfg.BdbsDatabaseDSN = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			cfg.BdbsDBUser, cfg.BdbsDBPassword, cfg.DBHost, cfg.DBPort, cfg.BdbsDBName)
	}
	if cfg.DatabasesDatabaseDSN == "" && cfg.DatabasesDBName != "" {
		cfg.DatabasesDatabaseDSN = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			cfg.DatabasesDBUser, cfg.DatabasesDBPassword, cfg.DBHost, cfg.DBPort, cfg.DatabasesDBName)
	}

	return cfg, nil
}

// validatePeerURLTemplate checks that a sharded deployment's peer-URL template is set and carries both the {color}
// and {ordinal} substitution tokens, so every peer pod resolves to a distinct base URL.
func validatePeerURLTemplate(template string) error {
	if template == "" {
		return errors.New(
			"BAGUETTEDB_DATABASES_PEER_URL_TEMPLATE is required when BAGUETTEDB_DATABASES_POD_NAME is set " +
				"and must contain the {color} and {ordinal} tokens")
	}
	for _, token := range []string{"{color}", "{ordinal}"} {
		if !strings.Contains(template, token) {
			return fmt.Errorf("BAGUETTEDB_DATABASES_PEER_URL_TEMPLATE must contain the %s token, got %q",
				token, template)
		}
	}

	return nil
}
