package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 内部用量统计读 usage_log_hourly：验证历史覆盖、按维度分组、时间范围过滤。
func TestGetUsageStatsFromRollup(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "rollupq.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	keyID, err := db.InsertAPIKey(ctx, "rollup-key", "sk-rollup-query-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	insert := func(model, endpoint string, tokens int, billed float64, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs
			(api_key_id, api_key_name, endpoint, model, status_code, prompt_tokens, completion_tokens,
			 total_tokens, input_tokens, output_tokens, user_billed, account_billed, created_at)
			VALUES ($1, 'rollup-key', $2, $3, 200, $4, 0, $4, $4, 0, $5, $5, $6)`,
			keyID, endpoint, model, tokens, billed, sqliteTimeParam(at)); err != nil {
			t.Fatalf("insert usage log: %v", err)
		}
	}
	june := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sep := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	insert("gpt-6-astra", "/responses", 100, 1.0, june)
	insert("gpt-6-astra", "/responses", 200, 2.0, june.Add(time.Hour))
	insert("grok-4.6", "/chat/completions", 300, 3.0, sep)

	// 汇总表由明细重建（生产上合并历史后也是这样触发一次）。
	if err := db.RebuildUsageHourlyRollup(ctx); err != nil {
		t.Fatalf("RebuildUsageHourlyRollup: %v", err)
	}

	all, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "none", 10, "desc")
	if err != nil {
		t.Fatalf("stats all: %v", err)
	}
	if all.TotalRequests != 3 || all.TotalTokens != 600 || all.UserBilled != 6.0 {
		t.Fatalf("全量 = %d 条 / %d tokens / %v 元", all.TotalRequests, all.TotalTokens, all.UserBilled)
	}

	byModel, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "model", 10, "desc")
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	if len(byModel.Items) != 2 || byModel.Items[0].Key != "gpt-6-astra" || byModel.Items[0].Requests != 2 {
		t.Fatalf("按模型 = %+v", byModel.Items)
	}

	byKey, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "api_key", 10, "desc")
	if err != nil || len(byKey.Items) != 1 || byKey.Items[0].Label != "rollup-key" {
		t.Fatalf("按 key = %+v err=%v", byKey.Items, err)
	}

	byDate, err := db.GetUsageStatsFromRollup(ctx, time.Time{}, time.Time{}, "date", 10, "asc")
	if err != nil || len(byDate.Items) != 2 {
		t.Fatalf("按日期 = %+v err=%v", byDate.Items, err)
	}
	if byDate.Items[0].Key != "2026-06-10" || byDate.Items[0].Requests != 2 {
		t.Fatalf("日期序列 = %+v", byDate.Items)
	}

	// 只查 9 月：不应包含 6 月的数据
	sepOnly, err := db.GetUsageStatsFromRollup(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Time{}, "none", 10, "desc")
	if err != nil || sepOnly.TotalRequests != 1 || sepOnly.TotalTokens != 300 {
		t.Fatalf("9 月后 = %+v err=%v", sepOnly, err)
	}
}
