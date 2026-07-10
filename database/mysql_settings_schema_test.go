package database

import (
	"strings"
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
	} {
		if !strings.Contains(ddl, needle) {
			t.Fatalf("MySQL system_settings DDL missing %q: %s", needle, ddl)
		}
	}
}
