package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

var mysqlCaptureDriverSequence uint64

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

func TestUpdateSystemSettingsRewritesNewFieldsForMySQL56(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	settings := &SystemSettings{
		ModelPricingOverrides:  `{"gpt-5.4":{"input":2.5,"source":"custom"}}`,
		ModelPricingSyncURL:    "https://example.test/pricing.json",
		IgnoreUsageLimitStatus: true,
	}
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("UpdateSystemSettings() error = %v", err)
	}

	if strings.Contains(strings.ToUpper(capture.query), "ON CONFLICT") {
		t.Fatalf("PostgreSQL ON CONFLICT leaked into MySQL query: %s", capture.query)
	}
	for _, fragment := range []string{
		"ON DUPLICATE KEY UPDATE",
		"model_pricing_overrides = VALUES(model_pricing_overrides)",
		"model_pricing_sync_url = VALUES(model_pricing_sync_url)",
		"ignore_usage_limit_status = VALUES(ignore_usage_limit_status)",
	} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten settings query missing %q: %s", fragment, capture.query)
		}
	}
	if got := strings.Count(capture.query, "?"); got != 87 {
		t.Fatalf("rewritten settings placeholder count = %d, want 87", got)
	}
	if len(capture.args) != 87 {
		t.Fatalf("rewritten settings argument count = %d, want 87", len(capture.args))
	}
	wantTail := []interface{}{
		settings.ModelPricingOverrides,
		settings.ModelPricingSyncURL,
		settings.IgnoreUsageLimitStatus,
	}
	for i, want := range wantTail {
		got := capture.args[len(capture.args)-len(wantTail)+i].Value
		if got != want {
			t.Fatalf("settings tail argument %d = %#v, want %#v", i, got, want)
		}
	}
}

type mysqlCaptureDriver struct {
	query string
	args  []driver.NamedValue
}

func (d *mysqlCaptureDriver) Open(string) (driver.Conn, error) {
	return &mysqlCaptureConn{capture: d}, nil
}

type mysqlCaptureConn struct {
	capture *mysqlCaptureDriver
}

func (c *mysqlCaptureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by capture driver")
}

func (c *mysqlCaptureConn) Close() error { return nil }

func (c *mysqlCaptureConn) Begin() (driver.Tx, error) {
	return mysqlCaptureTx{}, nil
}

func (c *mysqlCaptureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.capture.query = query
	c.capture.args = append([]driver.NamedValue(nil), args...)
	return driver.RowsAffected(1), nil
}

type mysqlCaptureTx struct{}

func (mysqlCaptureTx) Commit() error   { return nil }
func (mysqlCaptureTx) Rollback() error { return nil }

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
