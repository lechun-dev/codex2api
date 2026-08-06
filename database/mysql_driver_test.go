package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestRewriteModelCooldownInsertForMySQL56(t *testing.T) {
	got := rewriteSQLForMySQL(`INSERT INTO system_settings (id) VALUES (1) ON CONFLICT(id) DO NOTHING`)
	if !strings.Contains(got, "INSERT IGNORE INTO system_settings") {
		t.Fatalf("model cooldown insert was not rewritten for MySQL: %s", got)
	}
	assertNoMySQL56IncompatibleSQL(t, got)
}

func TestAccountChannelPredicateUsesMySQL56CompatibleSQL(t *testing.T) {
	db := &DB{driver: "mysql"}
	got := db.accountUpstreamTypeIsGrokPredicate()

	for _, fragment := range []string{"CAST(credentials AS CHAR)", "REGEXP", "[[:space:]]"} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("MySQL account channel predicate missing %q: %s", fragment, got)
		}
	}
	for _, incompatible := range []string{"JSON_EXTRACT", "->>"} {
		if strings.Contains(strings.ToUpper(got), strings.ToUpper(incompatible)) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q leaked into predicate: %s", incompatible, got)
		}
	}
}

func TestListActiveByChannelGeneratesMySQL56CompatibleSQL(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	if _, err := db.ListActiveByChannel(context.Background(), UpstreamChannelGrok); err != nil {
		t.Fatalf("ListActiveByChannel() error = %v", err)
	}

	for _, fragment := range []string{"CAST(credentials AS CHAR)", "REGEXP", "[[:space:]]"} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten channel query missing %q: %s", fragment, capture.query)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
}

func TestListAPIKeyAccountStatsGeneratesMySQL56CompatibleSQL(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	if _, err := db.ListAPIKeyAccountStats(context.Background(), 7, start, end); err != nil {
		t.Fatalf("ListAPIKeyAccountStats() error = %v", err)
	}

	for _, fragment := range []string{"CAST(a.credentials AS CHAR)", "api_key_id = ?", "created_at >= ?", "created_at < ?", "FROM ("} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten API key account query missing %q: %s", fragment, capture.query)
		}
	}
	if strings.Contains(strings.ToUpper(capture.query), "WITH AGGREGATED") {
		t.Fatalf("MySQL 5.6 query still uses a CTE: %s", capture.query)
	}
	if got := strings.Count(capture.query, "?"); got != 3 {
		t.Fatalf("rewritten API key account placeholder count = %d, want 3: %s", got, capture.query)
	}
	if len(capture.args) != 3 || capture.args[0].Value != int64(7) {
		t.Fatalf("rewritten API key account args = %#v", capture.args)
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
}

func TestAttachAPIKeyAccountGroupsGeneratesMySQL56CompatibleSQL(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	items := []APIKeyAccountStat{{AccountID: 11}, {AccountID: 22}, {AccountID: 11}}
	if err := db.attachAPIKeyAccountGroups(context.Background(), items); err != nil {
		t.Fatalf("attachAPIKeyAccountGroups() error = %v", err)
	}

	if !strings.Contains(capture.query, "m.account_id IN (?,?)") {
		t.Fatalf("rewritten account group query has unexpected placeholders: %s", capture.query)
	}
	if len(capture.args) != 2 || capture.args[0].Value != int64(11) || capture.args[1].Value != int64(22) {
		t.Fatalf("rewritten account group args = %#v", capture.args)
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
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

func TestResponseCacheSettingsSQLUsesMySQL56Syntax(t *testing.T) {
	insert := rewriteSQLForMySQL(`
		INSERT INTO system_settings (id) VALUES (1)
		ON CONFLICT (id) DO NOTHING
	`)
	if !strings.Contains(insert, "INSERT IGNORE INTO system_settings") {
		t.Fatalf("response cache settings insert was not rewritten: %s", insert)
	}

	selectForUpdate := rewriteSQLForMySQL(responseCacheSettingsSelectQuery(true))
	for _, fragment := range []string{
		"response_cache_local_max_bytes",
		"response_cache_local_max_entry_bytes",
		"response_cache_reconstruct_max_bytes",
		"response_cache_config_generation",
		"FOR UPDATE",
	} {
		if !strings.Contains(selectForUpdate, fragment) {
			t.Fatalf("response cache settings query missing %q: %s", fragment, selectForUpdate)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, insert)
	assertNoMySQL56IncompatibleSQL(t, selectForUpdate)
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

func TestRewriteSQLForMySQLAPIKeyRegeneration(t *testing.T) {
	selectQuery := rewriteSQLForMySQL(`SELECT key, name FROM api_keys WHERE id = $1 FOR UPDATE`)
	if selectQuery != "SELECT `key`, name FROM api_keys WHERE id = ? FOR UPDATE" {
		t.Fatalf("rewritten select = %q", selectQuery)
	}
	updateQuery := rewriteSQLForMySQL(`UPDATE api_keys SET key = $1 WHERE id = $2`)
	if updateQuery != "UPDATE api_keys SET `key` = ? WHERE id = ?" {
		t.Fatalf("rewritten update = %q", updateQuery)
	}
	assertNoMySQL56IncompatibleSQL(t, selectQuery)
	assertNoMySQL56IncompatibleSQL(t, updateQuery)
}

func TestRewriteSQLForMySQLAPIKeyEnabledQueries(t *testing.T) {
	selectQuery := rewriteSQLForMySQL(`SELECT ` + apiKeySelectColumns + ` FROM api_keys WHERE key = $1 AND enabled = TRUE`)
	if !strings.Contains(selectQuery, "WHERE `key` = ? AND enabled = TRUE") {
		t.Fatalf("rewritten select = %q", selectQuery)
	}
	updateQuery := rewriteSQLForMySQL(`UPDATE api_keys SET enabled = $1 WHERE id = $2`)
	if updateQuery != "UPDATE api_keys SET enabled = ? WHERE id = ?" {
		t.Fatalf("rewritten update = %q", updateQuery)
	}
	assertNoMySQL56IncompatibleSQL(t, selectQuery)
	assertNoMySQL56IncompatibleSQL(t, updateQuery)
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
		PayloadRules:                        `{"override":[{"path":"service_tier","value":"priority"}]}`,
		PromptFilterStrictTerminalEnabled:   true,
		PromptFilterAdvancedConfig:          `{"normalization":{"enabled":true}}`,
		PublicImageStudioPageEnabled:        true,
		PublicAccountPortalPageEnabled:      true,
		ModelPricingOverrides:               `{"gpt-5.4":{"input":2.5,"source":"custom"}}`,
		ModelPricingSyncURL:                 "https://example.test/pricing.json",
		IgnoreUsageLimitStatus:              true,
		AutoResetCreditsEnabled:             true,
		AutoResetCreditsBeforeExpiryMin:     75,
		CodexWSSizeRouterEnabled:            true,
		CodexWSBusyAcquireMaxWaitSec:        45,
		CodexWSBusyOverflowEnabled:          true,
		CodexWSBusyPatienceSec:              3,
		CodexWSWeakNetworkMode:              true,
		OverflowAutoCompactEnabled:          true,
		FirstTokenExcludesWsAcquire:         true,
		CodexPreflightSSEPassthroughEnabled: true,
		UTLSShutdownTimeoutMinutes:          45,
	}
	if err := db.UpdateSystemSettings(context.Background(), settings); err != nil {
		t.Fatalf("UpdateSystemSettings() error = %v", err)
	}

	if strings.Contains(strings.ToUpper(capture.query), "ON CONFLICT") {
		t.Fatalf("PostgreSQL ON CONFLICT leaked into MySQL query: %s", capture.query)
	}
	for _, fragment := range []string{
		"ON DUPLICATE KEY UPDATE",
		"payload_rules = VALUES(payload_rules)",
		"prompt_filter_strict_terminal_enabled = VALUES(prompt_filter_strict_terminal_enabled)",
		"prompt_filter_advanced_config = VALUES(prompt_filter_advanced_config)",
		"public_image_studio_page_enabled = VALUES(public_image_studio_page_enabled)",
		"public_account_portal_page_enabled = VALUES(public_account_portal_page_enabled)",
		"ignore_usage_limit_status = VALUES(ignore_usage_limit_status)",
		"auto_reset_credits_enabled = VALUES(auto_reset_credits_enabled)",
		"auto_reset_credits_before_expiry_min = VALUES(auto_reset_credits_before_expiry_min)",
		"codex_ws_size_router_enabled = VALUES(codex_ws_size_router_enabled)",
		"codex_ws_busy_acquire_max_wait_sec = VALUES(codex_ws_busy_acquire_max_wait_sec)",
		"codex_ws_busy_overflow_enabled = VALUES(codex_ws_busy_overflow_enabled)",
		"codex_ws_busy_patience_sec = VALUES(codex_ws_busy_patience_sec)",
		"codex_ws_weak_network_mode = VALUES(codex_ws_weak_network_mode)",
		"overflow_auto_compact_enabled = VALUES(overflow_auto_compact_enabled)",
		"first_token_excludes_ws_acquire = VALUES(first_token_excludes_ws_acquire)",
		"grok_config = VALUES(grok_config)",
		"codex_preflight_sse_passthrough_enabled = VALUES(codex_preflight_sse_passthrough_enabled)",
		"utls_shutdown_timeout_minutes = VALUES(utls_shutdown_timeout_minutes)",
		"session_affinity_spread = VALUES(session_affinity_spread)",
	} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten settings query missing %q: %s", fragment, capture.query)
		}
	}
	if got := strings.Count(capture.query, "?"); got != 106 {
		t.Fatalf("rewritten settings placeholder count = %d, want 106", got)
	}
	if len(capture.args) != 106 {
		t.Fatalf("rewritten settings argument count = %d, want 106", len(capture.args))
	}
	wantTail := []interface{}{
		settings.ModelPricingOverrides,
		settings.ModelPricingSyncURL,
		settings.IgnoreUsageLimitStatus,
		settings.AutoResetCreditsEnabled,
		int64(settings.AutoResetCreditsBeforeExpiryMin),
		settings.PromptFilterStrictTerminalEnabled,
		settings.PromptFilterAdvancedConfig,
		settings.PayloadRules,
		settings.PublicAccountPortalPageEnabled,
		settings.CodexWSSizeRouterEnabled,
		int64(settings.CodexWSBusyAcquireMaxWaitSec),
		settings.CodexWSBusyOverflowEnabled,
		int64(settings.CodexWSBusyPatienceSec),
		settings.OverflowAutoCompactEnabled,
		settings.FirstTokenExcludesWsAcquire,
		settings.CodexPreflightSSEPassthroughEnabled,
		int64(settings.UTLSShutdownTimeoutMinutes),
		settings.CodexWSWeakNetworkMode,
		settings.PreservePromptFilterCustomPatterns,
	}
	for i, want := range wantTail {
		got := capture.args[len(capture.args)-len(wantTail)+i].Value
		if got != want {
			t.Fatalf("settings tail argument %d = %#v, want %#v", i, got, want)
		}
	}
}

func TestAPIKeyScopeCounterSQLUsesMySQL56Upserts(t *testing.T) {
	db := &DB{driver: "mysql"}
	queries := []string{db.resetAPIKeyScopeCounterSQL()}
	accountQuery, groupQuery := db.apiKeyScopeCounterUpsertSQL()
	queries = append(queries, accountQuery, groupQuery)

	for _, query := range queries {
		rewritten, order := rewriteSQLForMySQLWithParamOrder(query)
		assertNoMySQL56IncompatibleSQL(t, rewritten)
		if !strings.Contains(rewritten, "ON DUPLICATE KEY UPDATE") {
			t.Fatalf("MySQL scope counter query is not an upsert: %s", rewritten)
		}
		if len(order) == 0 {
			t.Fatalf("MySQL scope counter query lost placeholder order: %s", rewritten)
		}
	}
	for _, fragment := range []string{
		"VALUES(used_cost)",
		"VALUES(used_tokens)",
		"VALUES(used_requests)",
	} {
		if !strings.Contains(rewriteSQLForMySQL(accountQuery), fragment) {
			t.Fatalf("MySQL account scope upsert missing %q: %s", fragment, accountQuery)
		}
		if !strings.Contains(rewriteSQLForMySQL(groupQuery), fragment) {
			t.Fatalf("MySQL group scope upsert missing %q: %s", fragment, groupQuery)
		}
	}
}

func TestCreatePromptFilterNewAPIBindingRewritesForMySQL56(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	const secret = "01234567890123456789012345678901"
	if err := db.CreatePromptFilterNewAPIBinding(context.Background(), &PromptFilterNewAPIBinding{
		APIKeyID:              17,
		PlatformCode:          "newapi-test",
		PlatformName:          "NewAPI Test",
		Secret:                secret,
		Enabled:               true,
		RequireSignedIdentity: true,
	}); err != nil {
		t.Fatalf("CreatePromptFilterNewAPIBinding() error = %v", err)
	}

	for _, fragment := range []string{
		"INSERT INTO prompt_filter_newapi_bindings",
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, CURRENT_TIMESTAMP)",
	} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten prompt-filter binding query missing %q: %s", fragment, capture.query)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
	if len(capture.args) != 9 || capture.args[0].Value != int64(17) || capture.args[3].Value != secret {
		t.Fatalf("rewritten prompt-filter binding args = %#v", capture.args)
	}
}

func TestUsageLogBatchInsertRewritesAuditFieldsForMySQL56(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	entry := usageLogEntry{
		AccountID:              7,
		ClientIP:               "127.0.0.1",
		SessionID:              "session-1",
		ConversationID:         "conversation-1",
		PreviousResponseID:     "response-1",
		RequestText:            "hello",
		HasCompactionHistory:   true,
		WsAcquireMs:            1234,
		ClientUserAgent:        "Codex Desktop/0.144.2",
		UpstreamUserAgent:      "codex_cli_rs/0.144.2",
		UserAgentOverridden:    true,
		InternalReason:         "policy_intercept",
		ParentRequestID:        "parent-request-1",
		PromptPolicyIncidentID: "incident-1",
	}
	if err := db.batchInsertLogsChunk(context.Background(), conn, []usageLogEntry{entry}); err != nil {
		t.Fatalf("batchInsertLogsChunk() error = %v", err)
	}

	for _, fragment := range []string{
		"session_id",
		"request_text",
		"ws_acquire_ms",
		"has_compaction_history",
		"client_user_agent",
		"upstream_user_agent",
		"user_agent_overridden",
		"internal_reason",
		"parent_request_id",
		"prompt_policy_incident_id",
	} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("rewritten usage-log insert missing %q: %s", fragment, capture.query)
		}
	}
	if got := strings.Count(capture.query, "?"); got != usageLogInsertColumnCount {
		t.Fatalf("rewritten usage-log placeholder count = %d, want %d", got, usageLogInsertColumnCount)
	}
	if len(capture.args) != usageLogInsertColumnCount {
		t.Fatalf("rewritten usage-log argument count = %d, want %d", len(capture.args), usageLogInsertColumnCount)
	}
	wantTail := []interface{}{
		entry.ClientUserAgent,
		entry.UpstreamUserAgent,
		entry.UserAgentOverridden,
		entry.InternalReason,
		entry.ParentRequestID,
		entry.PromptPolicyIncidentID,
	}
	for i, want := range wantTail {
		got := capture.args[len(capture.args)-len(wantTail)+i].Value
		if got != want {
			t.Fatalf("usage-log tail argument %d = %#v, want %#v", i, got, want)
		}
	}
}

func TestCreateAccountGroupUsesMySQL56InsertPath(t *testing.T) {
	capture := &mysqlCaptureDriver{lastInsertID: 42}
	driverName := fmt.Sprintf("codex2api-mysql-capture-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	id, err := db.CreateAccountGroup(context.Background(), "mysql56", "", "", 0, 0, sql.NullInt64{Int64: 6, Valid: true})
	if err != nil {
		t.Fatalf("CreateAccountGroup() error = %v", err)
	}
	if id != 42 {
		t.Fatalf("CreateAccountGroup() id = %d, want 42", id)
	}
	if strings.Contains(strings.ToUpper(capture.query), "RETURNING") {
		t.Fatalf("PostgreSQL RETURNING leaked into MySQL query: %s", capture.query)
	}
	if !strings.Contains(capture.query, "base_concurrency_override") || strings.Count(capture.query, "?") != 7 {
		t.Fatalf("unexpected MySQL account group insert: %s", capture.query)
	}
	if len(capture.args) != 7 || capture.args[6].Value != int64(6) {
		t.Fatalf("unexpected MySQL account group args: %#v", capture.args)
	}
}

type mysqlCaptureDriver struct {
	query        string
	queries      []string
	args         []driver.NamedValue
	lastInsertID int64
	queryRow     []driver.Value
	execCount    int
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
	c.capture.queries = append(c.capture.queries, query)
	c.capture.args = append([]driver.NamedValue(nil), args...)
	c.capture.execCount++
	return mysqlCaptureResult{lastInsertID: c.capture.lastInsertID, rowsAffected: 1}, nil
}

func (c *mysqlCaptureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.capture.query = query
	c.capture.queries = append(c.capture.queries, query)
	c.capture.args = append([]driver.NamedValue(nil), args...)
	return &mysqlCaptureRows{values: c.capture.queryRow}, nil
}

type mysqlCaptureRows struct {
	values  []driver.Value
	emitted bool
}

func (r *mysqlCaptureRows) Columns() []string {
	columns := make([]string, len(r.values))
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%d", i)
	}
	return columns
}

func (*mysqlCaptureRows) Close() error { return nil }

func (r *mysqlCaptureRows) Next(dest []driver.Value) error {
	if r.emitted || len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values)
	r.emitted = true
	return nil
}

type mysqlCaptureResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (r mysqlCaptureResult) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r mysqlCaptureResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

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

func assertNoMySQL56IncompatibleSQL(t *testing.T, query string) {
	t.Helper()
	for _, incompatible := range []string{
		"JSON_EXTRACT",
		"->>",
		" AS TEXT)",
		"::",
		"ON CONFLICT",
		"RETURNING",
		"TIMESTAMPTZ",
		"AS MATERIALIZED",
		"ROW_NUMBER(",
		"CREATE INDEX IF NOT EXISTS",
		"BIGSERIAL",
	} {
		if strings.Contains(strings.ToUpper(query), incompatible) {
			t.Fatalf("MySQL 5.6 incompatible syntax %q leaked into query: %s", incompatible, query)
		}
	}
	for i := 0; i+1 < len(query); i++ {
		if query[i] == '$' && query[i+1] >= '0' && query[i+1] <= '9' {
			t.Fatalf("PostgreSQL placeholders leaked into MySQL query: %s", query)
		}
	}
}
