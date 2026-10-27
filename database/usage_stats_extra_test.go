package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 备份表（如 usage_logs_openclaw）里的历史数据必须计入内部用量统计的总数与各维度，
// 未配置或表不存在时不能影响原有统计。
func TestGetUsageStatsIncludingBackupMergesExtraTable(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "extra.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// 备份表与 usage_logs 同结构（这里建一份最小同构表用于验证合并逻辑）。
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE usage_logs_openclaw (
		api_key_id INTEGER DEFAULT 0,
		account_id INTEGER DEFAULT 0,
		endpoint TEXT DEFAULT '',
		model TEXT DEFAULT '',
		status_code INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		cached_tokens INTEGER DEFAULT 0,
		input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		account_billed REAL DEFAULT 0,
		user_billed REAL DEFAULT 0,
		api_key_name TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatalf("create backup table: %v", err)
	}

	day := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	insert := func(table string, tokens int, billed float64, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO `+table+`
			(api_key_id, endpoint, model, status_code, total_tokens, prompt_tokens, completion_tokens, input_tokens, output_tokens, user_billed, api_key_name, created_at)
			VALUES (1, '/responses', 'gpt-6-astra', 200, $1, $1, 0, $1, 0, $2, 'legacy-key', $3)`,
			tokens, billed, sqliteTimeParam(at)); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}
	insert("usage_logs", 100, 1.5, day.AddDate(0, 3, 0))          // 9 月：当前表
	insert("usage_logs", 50, 0.5, day.AddDate(0, 3, 1))           // 9 月：当前表
	insert("usage_logs_openclaw", 400, 4.0, day)                  // 6 月：备份表
	insert("usage_logs_openclaw", 100, 1.0, day.AddDate(0, 0, 5)) // 6 月：备份表

	// 1) 未配置备份表：只统计 usage_logs
	stats, err := db.GetUsageStatsIncludingBackup(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("stats without backup: %v", err)
	}
	if stats.TotalRequests != 2 || stats.TotalTokens != 150 {
		t.Fatalf("无备份表时总量 = %d/%d, want 2/150", stats.TotalRequests, stats.TotalTokens)
	}

	// 2) 配置备份表后：两张表合并
	db.SetUsageLogExtraTables([]string{"usage_logs_openclaw", "usage_logs", "bad name;"})
	stats, err = db.GetUsageStatsIncludingBackup(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("stats with backup: %v", err)
	}
	if stats.TotalRequests != 4 || stats.TotalTokens != 650 {
		t.Fatalf("合并后总量 = %d/%d, want 4/650", stats.TotalRequests, stats.TotalTokens)
	}
	if stats.TotalUserBilled != 7.0 {
		t.Fatalf("合并后计费 = %v, want 7", stats.TotalUserBilled)
	}
	if len(stats.ModelStats) != 1 || stats.ModelStats[0].Requests != 4 || stats.ModelStats[0].Tokens != 650 {
		t.Fatalf("按模型合并 = %+v", stats.ModelStats)
	}
	if len(stats.APIKeyStats) != 1 || stats.APIKeyStats[0].Requests != 4 {
		t.Fatalf("按 key 合并 = %+v", stats.APIKeyStats)
	}

	// 3) 时间范围过滤也要覆盖两张表
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	stats, err = db.GetUsageStatsIncludingBackup(ctx, sep, time.Time{})
	if err != nil {
		t.Fatalf("stats from Sep: %v", err)
	}
	if stats.TotalRequests != 2 || stats.TotalTokens != 150 {
		t.Fatalf("9 月以后 = %d/%d, want 2/150", stats.TotalRequests, stats.TotalTokens)
	}

	// 4) 备份表不存在时自动跳过，不影响统计
	db.SetUsageLogExtraTables([]string{"usage_logs_not_exists"})
	stats, err = db.GetUsageStatsIncludingBackup(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("stats missing backup: %v", err)
	}
	if stats.TotalRequests != 2 {
		t.Fatalf("备份表缺失时 = %d, want 2", stats.TotalRequests)
	}
}
