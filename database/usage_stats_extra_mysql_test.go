package database

// 真 MySQL 上的合并用量统计：UNION ALL 派生表 + $n 占位符要经过 MySQL 5.6 的
// 占位符重写，这里覆盖一次，确认备份表能合并、缺表能跳过。

import (
	"context"
	"testing"
	"time"
)

func TestMySQLUsageStatsIncludingBackupIntegration(t *testing.T) {
	dsn := mysqlTempDatabase(t, mysqlIntelligenceDSN(t))
	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE usage_logs_openclaw LIKE usage_logs`); err != nil {
		t.Fatalf("create usage_logs_openclaw: %v", err)
	}
	day := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	insert := func(table string, tokens int, billed float64, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO `+table+`
			(api_key_id, endpoint, model, status_code, total_tokens, prompt_tokens, completion_tokens,
			 input_tokens, output_tokens, user_billed, created_at)
			VALUES (1, '/responses', 'gpt-6-astra', 200, ?, ?, 0, ?, 0, ?, ?)`,
			tokens, tokens, tokens, billed, at); err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}
	insert("usage_logs", 100, 1.5, day.AddDate(0, 3, 0))
	insert("usage_logs_openclaw", 400, 4.0, day)

	db.SetUsageLogExtraTables([]string{"usage_logs_openclaw"})
	stats, err := db.GetUsageStatsIncludingBackup(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetUsageStatsIncludingBackup: %v", err)
	}
	if stats.TotalRequests != 2 || stats.TotalTokens != 500 || stats.TotalUserBilled != 5.5 {
		t.Fatalf("合并结果 = %d 条 / %d tokens / %v 元", stats.TotalRequests, stats.TotalTokens, stats.TotalUserBilled)
	}
	if len(stats.ModelStats) != 1 || stats.ModelStats[0].Requests != 2 {
		t.Fatalf("按模型合并 = %+v", stats.ModelStats)
	}

	// 时间过滤（只取 9 月之后）不应包含 6 月的备份数据
	stats, err = db.GetUsageStatsIncludingBackup(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Time{})
	if err != nil {
		t.Fatalf("range query: %v", err)
	}
	if stats.TotalRequests != 1 {
		t.Fatalf("9 月以后应有 1 条, got %d", stats.TotalRequests)
	}
}
