package database

import (
	"strings"
	"testing"
)

func TestDataMigrationsTableDDLMySQLCompatible(t *testing.T) {
	db := &DB{driver: "mysql"}

	ddl := db.dataMigrationsTableDDL()
	if !strings.Contains(ddl, "ENGINE=InnoDB DEFAULT CHARSET=utf8") {
		t.Fatalf("MySQL DDL should declare engine/charset: %s", ddl)
	}
	if !strings.Contains(ddl, "applied_at DATETIME DEFAULT CURRENT_TIMESTAMP") {
		t.Fatalf("MySQL DDL should use DATETIME for applied_at: %s", ddl)
	}
	if strings.Contains(strings.ToUpper(ddl), "TIMESTAMPTZ") {
		t.Fatalf("MySQL DDL should not use TIMESTAMPTZ: %s", ddl)
	}
}

func TestDataMigrationInsertSQLMySQLCompatible(t *testing.T) {
	db := &DB{driver: "mysql"}

	query := db.dataMigrationInsertSQL()
	if !strings.Contains(query, "INSERT IGNORE INTO data_migrations") {
		t.Fatalf("MySQL migration insert should use INSERT IGNORE: %s", query)
	}
	if strings.Contains(strings.ToUpper(query), "ON CONFLICT") {
		t.Fatalf("MySQL migration insert should not use ON CONFLICT: %s", query)
	}
}

func TestDataMigrationInsertSQLPostgresStyleDefault(t *testing.T) {
	db := &DB{driver: "postgres"}

	query := db.dataMigrationInsertSQL()
	if !strings.Contains(query, "ON CONFLICT(version) DO NOTHING") {
		t.Fatalf("default migration insert should preserve ON CONFLICT semantics: %s", query)
	}
	if !strings.Contains(query, "$1") {
		t.Fatalf("default migration insert should preserve positional placeholder: %s", query)
	}
}

func TestAccountGroupUpstreamTypeExpressionMySQL56Compatible(t *testing.T) {
	db := &DB{driver: "mysql"}
	query := accountGroupUpstreamTypeExpression(db)

	if !strings.Contains(query, "CAST(a.credentials AS CHAR)") {
		t.Fatalf("MySQL group expression should read MEDIUMTEXT credentials: %s", query)
	}
	if !strings.Contains(query, "THEN 'grok'") || !strings.Contains(query, "ELSE ''") {
		t.Fatalf("MySQL group expression should return a provider string: %s", query)
	}
	for _, incompatible := range []string{"->>", "JSON_EXTRACT", "ON CONFLICT", "RETURNING"} {
		if strings.Contains(strings.ToUpper(query), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 group expression contains incompatible syntax %q: %s", incompatible, query)
		}
	}
}

func TestClaudeBackfillProviderExpressionMySQL56Compatible(t *testing.T) {
	db := &DB{driver: "mysql"}
	query := `SELECT id FROM accounts a WHERE ` + claudeBackfillProviderExpression(db) + ` = 'claude'`

	if !strings.Contains(query, `CAST(a.credentials AS CHAR)`) || !strings.Contains(query, `THEN 'claude'`) {
		t.Fatalf("MySQL Claude expression should inspect text credentials and return claude: %s", query)
	}
	for _, incompatible := range []string{"->>", "JSON_EXTRACT", "ON CONFLICT", "RETURNING"} {
		if strings.Contains(strings.ToUpper(query), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 Claude expression contains incompatible syntax %q: %s", incompatible, query)
		}
	}
}
