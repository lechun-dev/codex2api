package database

import (
	"database/sql/driver"
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

func TestRewriteSQLForMySQLRepeatedPlaceholders(t *testing.T) {
	query, order := rewriteSQLForMySQLWithParamOrder(`SELECT $1, $2, $2 FROM usage_logs WHERE created_at >= $1`)
	wantQuery := `SELECT ?, ?, ? FROM usage_logs WHERE created_at >= ?`
	if query != wantQuery {
		t.Fatalf("query = %q, want %q", query, wantQuery)
	}
	wantOrder := []int{1, 2, 2, 1}
	if len(order) != len(wantOrder) {
		t.Fatalf("order len = %d, want %d: %v", len(order), len(wantOrder), order)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}

	args, err := rewriteMySQLArgs([]driver.NamedValue{
		{Ordinal: 1, Value: "start"},
		{Ordinal: 2, Value: "minute"},
	}, order)
	if err != nil {
		t.Fatalf("rewriteMySQLArgs() error = %v", err)
	}
	gotValues := []interface{}{args[0].Value, args[1].Value, args[2].Value, args[3].Value}
	wantValues := []interface{}{"start", "minute", "minute", "start"}
	for i := range wantValues {
		if gotValues[i] != wantValues[i] {
			t.Fatalf("args values = %v, want %v", gotValues, wantValues)
		}
		if args[i].Ordinal != i+1 {
			t.Fatalf("arg %d ordinal = %d, want %d", i, args[i].Ordinal, i+1)
		}
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
