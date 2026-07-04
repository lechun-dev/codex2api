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
	if strings.Contains(ddl, "codex_user_agent_config TEXT DEFAULT '{}'") {
		t.Fatalf("MySQL 5.6 incompatible TEXT default leaked into DDL: %s", ddl)
	}
}
