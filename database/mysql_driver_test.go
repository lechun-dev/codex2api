package database

import (
	"strings"
	"testing"
)

func TestRewriteSQLForMySQLPlaceholdersAndCasts(t *testing.T) {
	got := rewriteSQLForMySQL(`SELECT LOWER(COALESCE(CAST(a.credentials AS TEXT), '')) FROM accounts WHERE id = $1`)
	want := `SELECT LOWER(COALESCE(CAST(a.credentials AS CHAR), '')) FROM accounts WHERE id = ?`
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLJSONCasts(t *testing.T) {
	got := rewriteSQLForMySQL(`UPDATE accounts SET credentials = $1::jsonb WHERE id = $2`)
	want := `UPDATE accounts SET credentials = ? WHERE id = ?`
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLUpsert(t *testing.T) {
	got := rewriteSQLForMySQL(`
		INSERT INTO model_registry (id, enabled)
		VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled
	`)
	if !containsAll(got, "ON DUPLICATE KEY UPDATE", "enabled = VALUES(enabled)", "VALUES (?, ?)") {
		t.Fatalf("unexpected MySQL upsert rewrite: %s", got)
	}
}

func TestRewriteSQLForMySQLDoNothing(t *testing.T) {
	got := rewriteSQLForMySQL(`INSERT INTO proxies (url, label) VALUES ($1, $2)
ON CONFLICT(url) DO NOTHING RETURNING id`)
	want := `INSERT IGNORE INTO proxies (url, label) VALUES (?, ?)`
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLAPIKeyIdentifier(t *testing.T) {
	got := rewriteSQLForMySQL(`SELECT id, name, key, created_at FROM api_keys WHERE key = $1`)
	want := "SELECT id, name, `key`, created_at FROM api_keys WHERE `key` = ?"
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLAPIKeyInsertIdentifier(t *testing.T) {
	got := rewriteSQLForMySQL(`INSERT INTO api_keys (name, key, quota_limit) VALUES ($1, $2, $3)`)
	want := "INSERT INTO api_keys (name, `key`, quota_limit) VALUES (?, ?, ?)"
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLAPIKeyDDLDoesNotRewritePrimaryKey(t *testing.T) {
	got := rewriteSQLForMySQL("CREATE TABLE api_keys (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, `key` VARCHAR(255) NOT NULL UNIQUE)")
	want := "CREATE TABLE api_keys (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, `key` VARCHAR(255) NOT NULL UNIQUE)"
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func TestRewriteSQLForMySQLAPIKeyStringLiteral(t *testing.T) {
	got := rewriteSQLForMySQL(`SELECT id FROM api_keys WHERE name = 'key' AND key = $1`)
	want := "SELECT id FROM api_keys WHERE name = 'key' AND `key` = ?"
	if got != want {
		t.Fatalf("rewriteSQLForMySQL() = %q, want %q", got, want)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
