package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMySQLSettingsSchemaIncludesCodexUserAgentConfig(t *testing.T) {
	ddl := systemSettingsMySQLDDL()
	if !strings.Contains(ddl, "codex_user_agent_config TEXT NULL") {
		t.Fatalf("MySQL system_settings DDL missing codex_user_agent_config: %s", ddl)
	}
	if !strings.Contains(ddl, "smart_pacing_enabled TINYINT(1) DEFAULT 0") {
		t.Fatalf("MySQL system_settings DDL missing smart_pacing_enabled: %s", ddl)
	}
	if !strings.Contains(ddl, "smart_pacing_min_concurrency INT DEFAULT 1") {
		t.Fatalf("MySQL system_settings DDL missing smart_pacing_min_concurrency: %s", ddl)
	}
	if !strings.Contains(ddl, "smart_pacing_windows VARCHAR(16) DEFAULT '5h,7d'") {
		t.Fatalf("MySQL system_settings DDL missing smart_pacing_windows: %s", ddl)
	}
	if strings.Contains(ddl, "codex_user_agent_config TEXT DEFAULT '{}'") {
		t.Fatalf("MySQL 5.6 incompatible TEXT default leaked into DDL: %s", ddl)
	}
	for _, needle := range []string{
		"test_content TEXT NULL",
		"retry_interval_ms INT DEFAULT 0",
		"transport_retry_policy VARCHAR(20) DEFAULT 'rotate'",
		"codex_continue_thinking_enabled TINYINT(1) DEFAULT 0",
		"codex_continue_max_rounds INT DEFAULT 8",
		"codex_synced_cli_version VARCHAR(64) DEFAULT ''",
		"codex_cli_version_sync_enabled TINYINT(1) DEFAULT 1",
		"codex_cli_version_sync_interval_hours INT DEFAULT 12",
		"model_pricing_overrides MEDIUMTEXT NULL",
		"model_pricing_sync_url TEXT NULL",
		"payload_rules MEDIUMTEXT NULL",
		"grok_config TEXT NULL",
		"prompt_filter_strict_terminal_enabled TINYINT(1) DEFAULT 0",
		"prompt_filter_advanced_config MEDIUMTEXT NULL",
		"public_image_studio_page_enabled TINYINT(1) DEFAULT 1",
		"public_account_portal_page_enabled TINYINT(1) DEFAULT 0",
		"ignore_usage_limit_status TINYINT(1) DEFAULT 0",
		"auto_reset_credits_enabled TINYINT(1) DEFAULT 0",
		"auto_reset_credits_before_expiry_min INT DEFAULT 60",
		"codex_ws_size_router_enabled TINYINT(1) DEFAULT 1",
		"codex_ws_busy_acquire_max_wait_sec INT DEFAULT 30",
		"codex_ws_busy_overflow_enabled TINYINT(1) DEFAULT 0",
		"codex_ws_busy_patience_sec INT DEFAULT 2",
		"codex_ws_weak_network_mode TINYINT(1) DEFAULT 0",
		"overflow_auto_compact_enabled TINYINT(1) DEFAULT 0",
		"codex_preflight_sse_passthrough_enabled TINYINT(1) DEFAULT 0",
		"utls_shutdown_timeout_minutes INT DEFAULT 30",
		"response_cache_local_max_bytes BIGINT NOT NULL DEFAULT 67108864",
		"response_cache_local_max_entry_bytes BIGINT NOT NULL DEFAULT 8388608",
		"response_cache_reconstruct_max_bytes BIGINT NOT NULL DEFAULT 67108864",
		"response_cache_config_generation BIGINT NOT NULL DEFAULT 1",
		"session_affinity_spread TINYINT(1) DEFAULT 0",
		"relay_model_cooldown_mode VARCHAR(20) NOT NULL DEFAULT 'off'",
		"relay_model_cooldown_seconds INT NOT NULL DEFAULT 2",
		"relay_model_cooldown_backoff_enabled TINYINT(1) NOT NULL DEFAULT 0",
		"oauth_model_cooldown_mode VARCHAR(20) NOT NULL DEFAULT 'adaptive'",
		"oauth_model_cooldown_seconds INT NOT NULL DEFAULT 300",
		"oauth_model_cooldown_backoff_enabled TINYINT(1) NOT NULL DEFAULT 1",
	} {
		if !strings.Contains(ddl, needle) {
			t.Fatalf("MySQL system_settings DDL missing %q: %s", needle, ddl)
		}
	}
	for _, column := range mysql56SystemSettingsColumns {
		if column.table != "system_settings" || !strings.Contains(ddl, column.name+" "+column.def) {
			t.Fatalf("MySQL system_settings upgrade column is inconsistent with create DDL: %+v", column)
		}
	}
	for _, incompatible := range []string{
		"model_pricing_overrides TEXT DEFAULT",
		"model_pricing_overrides MEDIUMTEXT DEFAULT",
		"model_pricing_sync_url TEXT DEFAULT",
		"payload_rules TEXT DEFAULT",
		"payload_rules MEDIUMTEXT DEFAULT",
		"prompt_filter_advanced_config TEXT DEFAULT",
		"prompt_filter_advanced_config MEDIUMTEXT DEFAULT",
		"grok_config TEXT DEFAULT",
		"note TEXT DEFAULT",
		"client_user_agent TEXT DEFAULT",
		"upstream_user_agent TEXT DEFAULT",
	} {
		if strings.Contains(ddl, incompatible) {
			t.Fatalf("MySQL 5.6 incompatible text default leaked into DDL: %q", incompatible)
		}
	}
}

func TestMySQL56V268MigrationScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_v2.6.8.sql"))
	if err != nil {
		t.Fatalf("read MySQL 5.6 v2.6.8 migration: %v", err)
	}
	script := string(raw)
	for _, definition := range []string{
		"response_cache_local_max_bytes BIGINT NOT NULL DEFAULT 67108864",
		"response_cache_local_max_entry_bytes BIGINT NOT NULL DEFAULT 8388608",
		"response_cache_reconstruct_max_bytes BIGINT NOT NULL DEFAULT 67108864",
		"response_cache_config_generation BIGINT NOT NULL DEFAULT 1",
	} {
		if !strings.Contains(script, "ADD COLUMN "+definition) {
			t.Fatalf("MySQL 5.6 v2.6.8 migration missing %q", definition)
		}
	}
	for _, incompatible := range []string{"ADD COLUMN IF NOT EXISTS", "BOOLEAN", "ON CONFLICT", "TIMESTAMPTZ"} {
		if strings.Contains(strings.ToUpper(script), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q in migration", incompatible)
		}
	}
}

func TestEnsureMySQLColumnDefaultSkipsCurrentDefault(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{"0.144.1"}}
	driverName := fmt.Sprintf("codex2api-mysql-default-current-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	if err := db.ensureMySQLColumnDefault(
		context.Background(),
		"system_settings",
		"codex_min_cli_version",
		"0.144.1",
		"VARCHAR(32) DEFAULT '0.144.1'",
	); err != nil {
		t.Fatalf("ensureMySQLColumnDefault() error = %v", err)
	}
	if capture.execCount != 0 {
		t.Fatalf("ensureMySQLColumnDefault() executed ALTER for current default")
	}
}

func TestEnsureMySQLColumnDefaultUpdatesStaleDefault(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{"0.118.0"}}
	driverName := fmt.Sprintf("codex2api-mysql-default-stale-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	if err := db.ensureMySQLColumnDefault(
		context.Background(),
		"system_settings",
		"codex_min_cli_version",
		"0.144.1",
		"VARCHAR(32) DEFAULT '0.144.1'",
	); err != nil {
		t.Fatalf("ensureMySQLColumnDefault() error = %v", err)
	}
	if capture.execCount != 1 {
		t.Fatalf("ensureMySQLColumnDefault() ALTER count = %d, want 1", capture.execCount)
	}
	want := "ALTER TABLE `system_settings` MODIFY COLUMN `codex_min_cli_version` VARCHAR(32) DEFAULT '0.144.1'"
	if capture.query != want {
		t.Fatalf("ensureMySQLColumnDefault() query = %q, want %q", capture.query, want)
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
}

func TestMySQL56AccountAndUsageAuditColumns(t *testing.T) {
	definitions := map[string]string{
		"accounts.note":                    mysql56AccountNoteDefinition,
		"usage_logs.client_user_agent":     mysql56ClientUserAgentDefinition,
		"usage_logs.upstream_user_agent":   mysql56UpstreamUserAgentDefinition,
		"usage_logs.user_agent_overridden": mysql56UserAgentOverriddenDefinition,
	}
	for column, definition := range definitions {
		if strings.Contains(strings.ToUpper(definition), "TEXT DEFAULT") {
			t.Fatalf("MySQL 5.6 incompatible definition for %s: %s", column, definition)
		}
		if definition == "" {
			t.Fatalf("MySQL 5.6 definition for %s is empty", column)
		}
	}
}

func TestMySQLPromptFilterNewAPIBindingsSchemaIsMySQL56Compatible(t *testing.T) {
	ddl := mysql56PromptFilterNewAPIBindingsDDL
	for _, needle := range []string{
		"api_key_id BIGINT NOT NULL PRIMARY KEY",
		"platform_code VARCHAR(32) NOT NULL UNIQUE",
		"secret TEXT NOT NULL",
		"require_signed_identity TINYINT(1) NOT NULL DEFAULT 0",
		"prompt_filter_scope VARCHAR(16) NOT NULL DEFAULT 'inherit'",
		"previous_secret_expires_at DATETIME NULL",
		"updated_at DATETIME DEFAULT CURRENT_TIMESTAMP",
		"ENGINE=InnoDB",
	} {
		if !strings.Contains(ddl, needle) {
			t.Fatalf("MySQL prompt_filter_newapi_bindings DDL missing %q: %s", needle, ddl)
		}
	}
	for _, incompatible := range []string{"TIMESTAMPTZ", "BOOLEAN", "ON CONFLICT", "TEXT NOT NULL DEFAULT"} {
		if strings.Contains(strings.ToUpper(ddl), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 incompatible syntax leaked into prompt_filter_newapi_bindings DDL: %q", incompatible)
		}
	}
}

func TestMySQLPromptIntelligenceSchemasAreMySQL56Compatible(t *testing.T) {
	ddls := map[string]string{
		"rule candidates":  mysql56PromptRuleCandidatesDDL,
		"rule evidence":    mysql56PromptRuleCandidateEvidenceDDL,
		"policy incidents": mysql56PromptPolicyIncidentsDDL,
	}
	for name, ddl := range ddls {
		for _, incompatible := range []string{
			"BIGSERIAL",
			"TIMESTAMPTZ",
			"BOOLEAN",
			"ON CONFLICT",
			"CREATE INDEX IF NOT EXISTS",
			"ADD COLUMN IF NOT EXISTS",
			"TEXT NOT NULL DEFAULT",
			"MEDIUMTEXT NOT NULL DEFAULT",
		} {
			if strings.Contains(strings.ToUpper(ddl), incompatible) {
				t.Fatalf("MySQL 5.6 incompatible syntax leaked into %s DDL: %q", name, incompatible)
			}
		}
		if !strings.Contains(ddl, "ENGINE=InnoDB DEFAULT CHARSET=utf8") {
			t.Fatalf("MySQL %s DDL is missing its MySQL 5.6 table options", name)
		}
	}
	if !strings.Contains(mysql56PromptRuleCandidatesDDL, "fingerprint VARCHAR(64) CHARACTER SET ascii") {
		t.Fatal("candidate fingerprint must use ASCII to stay inside legacy InnoDB index limits")
	}
	if !strings.Contains(mysql56PromptRuleCandidateEvidenceDDL, "source_ref_hash VARCHAR(64) CHARACTER SET ascii") {
		t.Fatal("evidence hash must use ASCII to stay inside legacy InnoDB index limits")
	}
}

func TestMySQL56UpstreamC5007B6MigrationScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_upstream_c5007b6.sql"))
	if err != nil {
		t.Fatalf("read MySQL 5.6 upstream migration: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS prompt_filter_newapi_bindings",
		"CREATE TABLE IF NOT EXISTS prompt_rule_candidates",
		"CREATE TABLE IF NOT EXISTS prompt_rule_candidate_evidence",
		"CREATE TABLE IF NOT EXISTS prompt_policy_incidents",
		"CREATE TABLE IF NOT EXISTS prompt_risk_events",
		"CREATE TABLE IF NOT EXISTS prompt_risk_event_sources",
		"CREATE TABLE IF NOT EXISTS prompt_risk_identities",
		"CREATE TABLE IF NOT EXISTS prompt_risk_trust_policies",
		"CREATE TABLE IF NOT EXISTS prompt_risk_trust_events",
		"c2a_add_column_if_missing('usage_logs', 'internal_reason'",
		"c2a_add_column_if_missing('usage_logs', 'parent_request_id'",
		"c2a_add_column_if_missing('usage_logs', 'prompt_policy_incident_id'",
		"c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_endpoint', '`endpoint`(191), `created_at`')",
		"c2a_add_index_if_missing('prompt_risk_identities', 'idx_prompt_risk_identities_external', '`platform`(64), `external_user_id`(128)')",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("MySQL 5.6 upstream migration missing %q", required)
		}
	}
	for _, incompatible := range []string{
		"ADD COLUMN IF NOT EXISTS",
		"CREATE INDEX IF NOT EXISTS",
		"BIGSERIAL",
		"TIMESTAMPTZ",
		"BOOLEAN",
		"ON CONFLICT",
		"TEXT NOT NULL DEFAULT",
		"MEDIUMTEXT NOT NULL DEFAULT",
	} {
		if strings.Contains(strings.ToUpper(script), incompatible) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q in upstream migration", incompatible)
		}
	}
}

func TestMySQL56Upstream0173444MigrationScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_upstream_0173444.sql"))
	if err != nil {
		t.Fatalf("read MySQL 5.6 upstream migration: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS prompt_conversation_locks",
		"CREATE TABLE IF NOT EXISTS usage_stats_rollup",
		"CREATE TABLE IF NOT EXISTS usage_stats_rollup_state",
		"id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY",
		"ENGINE=InnoDB DEFAULT CHARSET=utf8",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("MySQL 5.6 upstream migration missing %q", required)
		}
	}
	for _, incompatible := range []string{
		"CREATE INDEX IF NOT EXISTS",
		"ADD COLUMN IF NOT EXISTS",
		"BIGSERIAL",
		"TIMESTAMPTZ",
		"BOOLEAN",
		"ON CONFLICT",
		"RETURNING",
		"TEXT NOT NULL DEFAULT",
		"MEDIUMTEXT NOT NULL DEFAULT",
		"ROW_NUMBER(",
		"AS MATERIALIZED",
	} {
		if strings.Contains(strings.ToUpper(script), incompatible) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q in upstream migration", incompatible)
		}
	}
	for _, destructive := range []string{"ALTER TABLE", "DROP TABLE", "DELETE FROM", "TRUNCATE TABLE"} {
		if strings.Contains(strings.ToUpper(script), destructive) {
			t.Fatalf("unexpected destructive statement %q in create-only migration", destructive)
		}
	}
}

func TestMySQLPromptFilterLogsSchemaIncludesAuditFields(t *testing.T) {
	ddl := promptFilterLogsMySQLDDL()
	for _, needle := range []string{
		"endpoint VARCHAR(256) DEFAULT ''",
		"request_protocol VARCHAR(64) DEFAULT ''",
		"request_provider VARCHAR(64) DEFAULT ''",
		"audit_score INT DEFAULT 0",
		"policy_profile VARCHAR(32) DEFAULT ''",
		"reason_code VARCHAR(100) DEFAULT ''",
		"primary_origin VARCHAR(50) DEFAULT ''",
		"strike_eligible TINYINT(1) DEFAULT 0",
		"match_context TEXT NULL",
	} {
		if !strings.Contains(ddl, needle) {
			t.Fatalf("MySQL prompt_filter_logs DDL missing %q: %s", needle, ddl)
		}
	}
	for _, column := range mysql56PromptFilterLogColumns {
		if column.table != "prompt_filter_logs" || !strings.Contains(ddl, column.name+" "+column.def) {
			t.Fatalf("MySQL prompt_filter_logs upgrade column is inconsistent with create DDL: %+v", column)
		}
	}
	for _, incompatible := range []string{
		"TIMESTAMPTZ",
		"BOOLEAN",
		"TEXT DEFAULT",
		"::jsonb",
	} {
		if strings.Contains(strings.ToUpper(ddl), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 incompatible syntax leaked into prompt_filter_logs DDL: %q", incompatible)
		}
	}
}

func TestWorkspaceIdentityCredentialsUpdateSQLByDriver(t *testing.T) {
	mysqlQuery := (&DB{driver: "mysql"}).workspaceIdentityCredentialsUpdateSQL()
	if strings.Contains(mysqlQuery, "::jsonb") {
		t.Fatalf("PostgreSQL cast leaked into MySQL workspace identity migration: %s", mysqlQuery)
	}
	postgresQuery := (&DB{driver: "postgres"}).workspaceIdentityCredentialsUpdateSQL()
	if !strings.Contains(postgresQuery, "::jsonb") {
		t.Fatalf("PostgreSQL workspace identity migration lost jsonb cast: %s", postgresQuery)
	}
	sqliteQuery := (&DB{driver: "sqlite"}).workspaceIdentityCredentialsUpdateSQL()
	if strings.Contains(sqliteQuery, "::jsonb") {
		t.Fatalf("PostgreSQL cast leaked into SQLite workspace identity migration: %s", sqliteQuery)
	}
}

func TestMySQLAccountGroupSchemaIncludesBaseConcurrencyOverride(t *testing.T) {
	ddl := accountGroupsMySQLDDL()
	if !strings.Contains(ddl, "base_concurrency_override INT NULL") {
		t.Fatalf("MySQL account_groups DDL missing base_concurrency_override: %s", ddl)
	}
	if !strings.Contains(ddl, "proxy_urls TEXT NULL") {
		t.Fatalf("MySQL account_groups DDL missing MySQL 5.6-compatible proxy_urls: %s", ddl)
	}
	if strings.Contains(strings.ToUpper(ddl), "PROXY_URLS TEXT DEFAULT") {
		t.Fatalf("MySQL 5.6-incompatible proxy_urls default leaked into DDL: %s", ddl)
	}
}

func TestMySQL56V271MigrationScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_v2.7.1.sql"))
	if err != nil {
		t.Fatalf("read MySQL 5.6 v2.7.1 migration: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		"c2a_add_column_if_missing('account_groups', 'proxy_urls', 'TEXT NULL')",
		"c2a_add_column_if_missing('system_settings', 'session_affinity_spread', 'TINYINT(1) DEFAULT 0')",
		"c2a_add_column_if_missing('system_settings', 'relay_model_cooldown_mode'",
		"c2a_add_column_if_missing('system_settings', 'oauth_model_cooldown_mode'",
		"c2a_add_column_if_missing('prompt_filter_newapi_bindings', 'prompt_filter_scope'",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("MySQL 5.6 v2.7.1 migration missing %q", required)
		}
	}
	for _, incompatible := range []string{"ADD COLUMN IF NOT EXISTS", "BOOLEAN", "ON CONFLICT", "TIMESTAMPTZ", "TEXT DEFAULT"} {
		if strings.Contains(strings.ToUpper(script), incompatible) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q in v2.7.1 migration", incompatible)
		}
	}
}

func TestMySQLAPIKeyScopeCountersSchemaIsMySQL56Compatible(t *testing.T) {
	ddl := apiKeyScopeCountersMySQLDDL()
	for _, needle := range []string{
		"api_key_id BIGINT NOT NULL",
		"scope_type VARCHAR(16) NOT NULL",
		"scope_id BIGINT NOT NULL",
		"updated_at DATETIME DEFAULT CURRENT_TIMESTAMP",
		"PRIMARY KEY (api_key_id, scope_type, scope_id)",
		"ENGINE=InnoDB",
	} {
		if !strings.Contains(ddl, needle) {
			t.Fatalf("MySQL api_key_scope_counters DDL missing %q: %s", needle, ddl)
		}
	}
	for _, incompatible := range []string{"TIMESTAMPTZ", "BOOLEAN", "ON CONFLICT", "TEXT DEFAULT"} {
		if strings.Contains(strings.ToUpper(ddl), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 incompatible syntax leaked into api_key_scope_counters DDL: %q", incompatible)
		}
	}
}
