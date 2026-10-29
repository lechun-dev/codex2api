package database

// 真 MySQL 上验证内部用量统计的小时汇总查询：$n 占位符重写、DATE_FORMAT 分桶、
// 按 model/api_key 分组都要能跑通。

import (
	"context"
	"testing"
	"time"
)

func TestMySQLUsageStatsFromRollupIntegration(t *testing.T) {
	dsn := mysqlTempDatabase(t, mysqlIntelligenceDSN(t))
	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	keyID, err := db.InsertAPIKey(ctx, "rollup-mysql", "sk-rollup-mysql-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	insert := func(model, endpoint string, tokens int, billed float64, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs
			(api_key_id, api_key_name, endpoint, model, status_code, prompt_tokens, completion_tokens,
			 total_tokens, input_tokens, output_tokens, user_billed, account_billed, created_at)
			VALUES (?, 'rollup-mysql', ?, ?, 200, ?, 0, ?, ?, 0, ?, ?, ?)`,
			keyID, endpoint, model, tokens, tokens, tokens, billed, billed, at); err != nil {
			t.Fatalf("insert usage log: %v", err)
		}
	}
	june := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sep := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	insert("gpt-6-astra", "/responses", 100, 1.0, june)
	insert("gpt-6-astra", "/responses", 200, 2.0, june.Add(time.Hour))
	insert("grok-4.6", "/chat/completions", 300, 3.0, sep)

	if err := db.RebuildUsageHourlyRollup(ctx); err != nil {
		t.Fatalf("RebuildUsageHourlyRollup: %v", err)
	}

	all, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "none", 10, "desc")
	if err != nil {
		t.Fatalf("stats all: %v", err)
	}
	if all.TotalRequests != 3 || all.TotalTokens != 600 || all.UserBilled != 6.0 {
		t.Fatalf("全量 = %d / %d / %v", all.TotalRequests, all.TotalTokens, all.UserBilled)
	}

	byModel, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "model", 10, "desc")
	if err != nil || len(byModel.Items) != 2 || byModel.Items[0].Key != "gpt-6-astra" || byModel.Items[0].Requests != 2 {
		t.Fatalf("按模型 = %+v err=%v", byModel.Items, err)
	}

	byDate, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "date", 10, "asc")
	if err != nil || len(byDate.Items) != 2 || byDate.Items[0].Key != "2026-06-10" {
		t.Fatalf("按日期 = %+v err=%v", byDate.Items, err)
	}

	byKey, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "api_key", 10, "desc")
	if err != nil || len(byKey.Items) != 1 || byKey.Items[0].Label != "rollup-mysql" {
		t.Fatalf("按 key = %+v err=%v", byKey.Items, err)
	}
}
