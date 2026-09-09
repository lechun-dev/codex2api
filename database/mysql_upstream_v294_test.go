package database

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

func TestMySQL294ProxyRiskDDLAndInsertIDs(t *testing.T) {
	capture := &mysqlCaptureDriver{lastInsertID: 42}
	db := newMySQLCaptureDB(t, capture)
	ctx := context.Background()
	if err := db.ensureProxyRiskScoringTables(ctx); err != nil {
		t.Fatal(err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected two table statements: %v", capture.queries)
	}
	for _, query := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, query)
		for _, bad := range []string{"SERIAL", "TEXT NOT NULL DEFAULT", "TEXT DEFAULT", "TIMESTAMPTZ", " DESC"} {
			if strings.Contains(query, bad) {
				t.Fatalf("incompatible DDL %q: %s", bad, query)
			}
		}
		if !strings.Contains(query, "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci") {
			t.Fatalf("missing explicit compatible table options: %s", query)
		}
	}
	profile := &ProxyRiskScoringProfile{Name: "test"}
	if id, err := db.CreateProxyRiskScoringProfile(ctx, profile); err != nil || id != 42 || profile.ID != 42 {
		t.Fatalf("create profile id=%d profile=%+v err=%v", id, profile, err)
	}
	snapshot := &ProxyRiskScoreSnapshot{ProxyID: 1, ProfileID: 42}
	if err := db.InsertProxyRiskScoreSnapshot(ctx, snapshot); err != nil || snapshot.ID != 42 {
		t.Fatalf("insert snapshot id=%d err=%v", snapshot.ID, err)
	}
	for _, query := range capture.queries {
		if strings.Contains(query, "RETURNING") {
			t.Fatalf("MySQL insert must use LastInsertId: %s", query)
		}
	}
}

func TestMySQL294ProxyRiskHistoryPagination(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRows: [][]driver.Value{{int64(0)}, {}}}
	db := newMySQLCaptureDB(t, capture)
	if _, _, err := db.ListProxyRiskScoreHistory(context.Background(), 8, 9, 3, 20); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capture.query, "LIMIT ? OFFSET ?") {
		t.Fatalf("invalid MySQL pagination: %s", capture.query)
	}
	if len(capture.args) != 4 || capture.args[2].Value != int64(20) || capture.args[3].Value != int64(40) {
		t.Fatalf("wrong limit/offset arguments: %+v", capture.args)
	}
}

func TestMySQL294DailyBreakdownMigratesAndPreservesCounts(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(0)}}
	db := newMySQLCaptureDB(t, capture)
	if err := db.UpsertAccountDailyBreakdown(context.Background(), AccountDailyBreakdownInput{AccountID: 7, Day: "2026-09-09", Percent: 1}); err != nil {
		t.Fatal(err)
	}
	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{"breakdown_percent", "breakdown_json", "surfaces_json", "MEDIUMTEXT NULL", "clients_json, models_json", "'[]','[]'", "ON DUPLICATE KEY UPDATE"} {
		if !strings.Contains(queries, want) {
			t.Fatalf("missing %q: %s", want, queries)
		}
	}
	for _, query := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, query)
	}
	update := strings.Split(capture.query, "ON DUPLICATE KEY UPDATE")[1]
	if strings.Contains(update, "clients_json") || strings.Contains(update, "models_json") || strings.Contains(update, "total_tokens") {
		t.Fatalf("breakdown update must preserve counts: %s", update)
	}
}

func TestMySQL294PromptRetentionUsesMaterializedBatches(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(1)}, execRowsAffectedSet: true}
	db := newMySQLCaptureDB(t, capture)
	ctx := context.Background()
	if err := db.ensurePromptLogRetentionConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurgeExpiredPromptLogs(ctx, time.Now().UTC(), 10, 0); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, query := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, query)
		if strings.Contains(query, "DELETE FROM") {
			deletes++
			if !strings.Contains(query, "FROM (SELECT") || !strings.Contains(query, "AS purge_candidates") {
				t.Fatalf("missing derived-table deletion guard: %s", query)
			}
		}
	}
	if deletes != 3 {
		t.Fatalf("expected 3 bounded deletion queries, got %d", deletes)
	}
}
