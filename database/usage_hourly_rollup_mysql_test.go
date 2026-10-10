package database

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

func TestMySQL308HourlySchemaAndSQL(t *testing.T) {
	for _, q := range mysqlUsageHourlyDDL(nil) {
		assertNoMySQL56IncompatibleSQL(t, q)
		for _, forbidden := range []string{"TEXT NOT NULL DEFAULT", "TIMESTAMPTZ", "CREATE INDEX IF NOT EXISTS", "utf8mb4_0900", "PRIMARY KEY (bucket, api_key_id"} {
			if strings.Contains(q, forbidden) {
				t.Fatalf("MySQL 5.6 incompatible %q: %s", forbidden, q)
			}
		}
		if !strings.Contains(q, "ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_general_ci") {
			t.Fatalf("missing explicit table options: %s", q)
		}
	}
	ddl := mysqlUsageHourlyDDL(map[string]string{"api_key_name": "TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL"})[0]
	if !strings.Contains(ddl, "api_key_name TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL") || !strings.Contains(ddl, "PRIMARY KEY (bucket, dimension_hash)") {
		t.Fatalf("source collation or short primary key missing: %s", ddl)
	}
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(0)}, queryRows: [][]driver.Value{{"api_key_name", "utf8mb4", "utf8mb4_unicode_ci"}}}
	db := newMySQLCaptureDB(t, capture)
	if err := db.ensureMySQLUsageHourlySchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, q)
		if strings.Contains(q, "ADD COLUMN IF NOT EXISTS") {
			t.Fatalf("non-idempotent MySQL migration: %s", q)
		}
	}
	q := rewriteSQLForMySQL(db.usageHourlyUpsertSQL("u.id > $1 AND u.id <= $2"))
	for _, fragment := range []string{"DATE_FORMAT(u.created_at", "UNHEX(SHA2(CONCAT(", "ON DUPLICATE KEY UPDATE", "VALUES(requests)", "FROM (SELECT", "CAST(hourly_rows.api_key_name AS BINARY)"} {
		if !strings.Contains(q, fragment) {
			t.Fatalf("missing %q: %s", fragment, q)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, q)
	if strings.Contains(q, "date_trunc") || strings.Contains(q, "excluded.") {
		t.Fatalf("Postgres syntax leaked into MySQL: %s", q)
	}
}

func TestMySQL308BigintCastRewriting(t *testing.T) {
	q := `SELECT CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN requests ELSE 0 END), 0) AS BIGINT),
		'AS BIGINT)', "AS BIGINT)", ` + "`AS BIGINT)`" + ` /* AS BIGINT) */ -- AS BIGINT)
		FROM usage_log_hourly`
	got := rewriteSQLForMySQL(q)
	if strings.Count(got, "AS SIGNED") != 1 || strings.Count(got, "AS BIGINT)") != 5 {
		t.Fatalf("cast rewriting changed quoted/comment text: %s", got)
	}
}

func TestMySQL308SettingsUpgradeAndRetention(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx, `INSERT IGNORE INTO system_settings (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	settings, err := db.GetSystemSettings(ctx)
	if err != nil || settings == nil || settings.CodexWSDownstreamKeepaliveSlots != 8 {
		t.Fatalf("default retention: %+v, %v", settings, err)
	}
	for _, slots := range []int{0, 17, 32} {
		settings.CodexWSDownstreamKeepaliveSlots = slots
		settings.PreservePromptFilterCustomPatterns = true
		settings.PreservePromptFilterReviewAPIKey = true
		if err := db.UpdateSystemSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		read, err := db.GetSystemSettings(ctx)
		if err != nil || read.CodexWSDownstreamKeepaliveSlots != slots {
			t.Fatalf("roundtrip retention=%d: %+v, %v", slots, read, err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `ALTER TABLE system_settings DROP COLUMN codex_ws_downstream_keepalive_slots`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateMySQL(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err = db.GetSystemSettings(ctx)
	if err != nil || settings == nil || settings.CodexWSDownstreamKeepaliveSlots != 8 {
		t.Fatalf("upgraded retention: %+v, %v", settings, err)
	}
	if err := db.initializeRuntimeCacheScope(ctx); err != nil || db.RuntimeCacheScope() == "" {
		t.Fatalf("runtime cache scope: %q, %v", db.RuntimeCacheScope(), err)
	}
}

func TestUsageHourlyRollupMatchesRawLogsMySQL(t *testing.T) {
	runUsageHourlyEquivalence(t, newMySQLPromptReviewIntegrationDB(t))
}

func TestUsageHourlyRollupMaintainedByLogFlushMySQL(t *testing.T) {
	runUsageHourlyFlushMaintenance(t, newMySQLPromptReviewIntegrationDB(t))
}

func TestMySQL308HourlyBackfillRepairAndLongDimensions(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	logs := generateHourlyTestLogs(now, []int64{1, 2}, 2077)
	// 2026-10-10 coder(lq): Two Chinese names sharing a >191-char prefix must
	// remain distinct; retain literal separators and case-sensitive byte values.
	for _, name := range []string{strings.Repeat("中", 220) + "甲:1", strings.Repeat("中", 220) + "乙:1", "", "Key:A", "key:a"} {
		logs = append(logs, hourlyTestLog{at: now.Add(-2 * time.Hour), keyName: name, apiKeyID: 1, status: 200, prompt: 13, userBilled: 0.125})
	}
	if _, err := db.conn.ExecContext(ctx, `ALTER TABLE usage_logs MODIFY api_key_name TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL`); err != nil {
		t.Fatal(err)
	}
	// Recreate the empty hourly table to exercise source-column collation copy.
	if _, err := db.conn.ExecContext(ctx, `DROP TABLE usage_log_hourly`); err != nil {
		t.Fatal(err)
	}
	if err := db.ensureMySQLUsageHourlySchema(ctx); err != nil {
		t.Fatal(err)
	}
	insertHourlyTestLogs(t, db, logs)
	// 2026-10-10 coder(lq): Distinct 4-byte labels must not hash to the same
	// connection-charset replacement character when DATABASE_CHARSET=utf8.
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs (api_key_id, api_key_name, status_code, created_at)
		VALUES (1, CONVERT(0xF09F9880 USING utf8mb4), 200, $1),
		       (1, CONVERT(0xF09F9881 USING utf8mb4), 200, $1)`, db.timeArg(now.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	// 2026-10-10 coder(lq): Simulate a pre-upgrade database with history but
	// no hourly state, then run the same schema/init sequence used at startup.
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM usage_log_hourly_state`); err != nil {
		t.Fatal(err)
	}
	if err := db.ensureMySQLUsageHourlySchema(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := db.initUsageHourlyRollup(ctx)
	if err != nil || !pending || db.usageHourlyRollupUsable(ctx) {
		t.Fatalf("backfill readiness: %v, %v", pending, err)
	}
	steps := 0
	for {
		done, err := db.usageHourlyBackfillStep(ctx)
		if err != nil {
			t.Fatal(err)
		}
		steps++
		if done {
			break
		}
	}
	if steps != 2 {
		t.Fatalf("backfill took %d steps, want 2 bounded chunks", steps)
	}
	assertUsageHourlyMatchesLogs(t, db)
	var names int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT api_key_name) FROM usage_log_hourly WHERE LENGTH(api_key_name) > 600`).Scan(&names); err != nil || names != 2 {
		t.Fatalf("long names were merged/truncated: %d, %v", names, err)
	}
	var emojiHashes int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT dimension_hash) FROM usage_log_hourly
		WHERE bucket = $1 AND LENGTH(api_key_name) = 4`, db.timeArg(now.Add(-2*time.Hour).Truncate(time.Hour))).Scan(&emojiHashes); err != nil || emojiHashes != 2 {
		t.Fatalf("4-byte labels lost their distinct hashes: %d, %v", emojiHashes, err)
	}
	insertHourlyTestLogs(t, db, []hourlyTestLog{{at: now.Add(-time.Hour), apiKeyID: 1, status: 500, userBilled: 0.25}})
	// Inspect freshness without triggering the background repair notification.
	db.repairUsageHourly(ctx)
	if !db.usageHourlyRollupUsable(ctx) {
		t.Fatal("out-of-band insert was not repaired")
	}
	assertUsageHourlyMatchesLogs(t, db)
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM usage_logs WHERE id = (SELECT id FROM (SELECT MAX(id) AS id FROM usage_logs) last_row)`); err != nil {
		t.Fatal(err)
	}
	db.repairUsageHourly(ctx)
	assertUsageHourlyMatchesLogs(t, db)
	if !db.usageHourlyRollupUsable(ctx) {
		t.Fatal("deleted-row repair did not finish")
	}
}

func TestMySQL308HourlyWriterWaitsForRebuildLock(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := db.lockUsageHourlyForRebuildTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		done <- db.insertUsageLogBatch(ctx, []usageLogEntry{{StoreUsageLog: true, StatusCode: 200, TotalTokens: 17}})
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("writer bypassed rebuild lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer remained blocked after rebuild committed")
	}
	assertUsageHourlyMatchesLogs(t, db)
}
