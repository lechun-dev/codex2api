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
