package database

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// 自然日限额(issue #460)的聚合边界:since 之前的不算、499 不算、OldestAt 取窗口内最早一笔。
func TestGetAPIKeyUsageSinceAggregatesFromBoundary(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "daily.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "daily", "sk-daily-usage-a-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey 返回错误: %v", err)
	}

	insert := func(statusCode, totalTokens int, userBilled float64, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `
			INSERT INTO usage_logs (api_key_id, account_id, endpoint, model, status_code, total_tokens, user_billed, created_at)
			VALUES ($1, 1, '/v1/responses', 'gpt-5.4', $2, $3, $4, $5)
		`, keyID, statusCode, totalTokens, userBilled, sqliteTimeParam(at)); err != nil {
			t.Fatalf("insert usage log: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-2 * time.Hour)
	oldest := now.Add(-1 * time.Hour)
	insert(200, 700, 7.00, now.Add(-3*time.Hour)) // since 之前,不计入
	insert(200, 100, 0.10, oldest)
	insert(200, 50, 0.05, now)
	insert(499, 900, 9.99, now) // 客户端取消不计入

	usage, err := db.GetAPIKeyUsageSince(ctx, keyID, since)
	if err != nil {
		t.Fatalf("GetAPIKeyUsageSince 返回错误: %v", err)
	}
	if usage.Requests != 2 || usage.Tokens != 150 || math.Abs(usage.UserBilled-0.15) > 1e-9 {
		t.Fatalf("usage = %+v, want 2 requests / 150 tokens / $0.15", usage)
	}
	if usage.OldestAt == nil || usage.OldestAt.Unix() != oldest.Unix() {
		t.Fatalf("OldestAt = %v, want %v", usage.OldestAt, oldest)
	}
}

// 2026-08-30 coder(lq): 验证 Key 最近使用时间查询只取最新非 499 记录,且无有效记录的 Key 不进入结果。
func TestListAPIKeyLastUsedAtUsesLatestNonCancelledRequest(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "last-used.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	keyWithHistory, err := db.InsertAPIKey(ctx, "history", "sk-last-used-history-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey(history) 返回错误: %v", err)
	}
	keyOnlyCancelled, err := db.InsertAPIKey(ctx, "cancelled", "sk-last-used-cancelled-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey(cancelled) 返回错误: %v", err)
	}
	if _, err := db.InsertAPIKey(ctx, "unused", "sk-last-used-unused-1234567890"); err != nil {
		t.Fatalf("InsertAPIKey(unused) 返回错误: %v", err)
	}

	oldest := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	latest := oldest.Add(2 * time.Hour)
	insert := func(apiKeyID int64, statusCode int, at time.Time) {
		t.Helper()
		if _, err := db.conn.ExecContext(ctx, `
			INSERT INTO usage_logs (api_key_id, account_id, endpoint, model, status_code, created_at)
			VALUES ($1, 1, '/v1/responses', 'gpt-5.4', $2, $3)
		`, apiKeyID, statusCode, sqliteTimeParam(at)); err != nil {
			t.Fatalf("insert usage log: %v", err)
		}
	}
	insert(keyWithHistory, 200, oldest)
	insert(keyWithHistory, 499, latest.Add(time.Minute))
	insert(keyWithHistory, 201, latest)
	insert(keyOnlyCancelled, 499, latest.Add(time.Hour))

	got, err := db.ListAPIKeyLastUsedAt(ctx)
	if err != nil {
		t.Fatalf("ListAPIKeyLastUsedAt 返回错误: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAPIKeyLastUsedAt 返回 %d 个 Key, want 1: %#v", len(got), got)
	}
	if got[keyWithHistory].Unix() != latest.Unix() {
		t.Fatalf("last used = %v, want %v", got[keyWithHistory], latest)
	}
	if _, ok := got[keyOnlyCancelled]; ok {
		t.Fatalf("仅有 499 记录的 Key 不应出现在结果中: %#v", got)
	}
}

// 自助报表的窗口语义:today 是 fixed 且 reset_at=次日零点;滑动窗口带 decay_at=OldestAt+窗口长度。
func TestGetAPIKeySelfUsageReportWindowMetadata(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "daily-report.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "daily-report", "sk-daily-usage-b-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey 返回错误: %v", err)
	}
	now := time.Now()
	if _, err := db.conn.ExecContext(ctx, `
		INSERT INTO usage_logs (api_key_id, account_id, endpoint, model, status_code, total_tokens, user_billed, created_at)
		VALUES ($1, 1, '/v1/responses', 'gpt-5.4', 200, 100, 0.10, $2)
	`, keyID, sqliteTimeParam(now)); err != nil {
		t.Fatalf("insert usage log: %v", err)
	}

	report, err := db.GetAPIKeySelfUsageReport(ctx, keyID, time.Time{}, now, 1, 10)
	if err != nil {
		t.Fatalf("GetAPIKeySelfUsageReport 返回错误: %v", err)
	}

	today := report.Windows.Today
	if today.WindowKind != usageWindowKindFixed || today.ResetAt == nil || today.DecayAt != nil {
		t.Fatalf("today window = kind %q reset %v decay %v, want fixed / non-nil / nil", today.WindowKind, today.ResetAt, today.DecayAt)
	}
	wantReset := StartOfDay(time.Now()).AddDate(0, 0, 1)
	if !today.ResetAt.Equal(wantReset) {
		t.Fatalf("today.ResetAt = %v, want %v", today.ResetAt, wantReset)
	}
	if today.Requests != 1 {
		t.Fatalf("today.Requests = %d, want 1", today.Requests)
	}

	last5h := report.Windows.Last5h
	if last5h.WindowKind != usageWindowKindSliding || last5h.ResetAt != nil {
		t.Fatalf("last5h window = kind %q reset %v, want sliding / nil", last5h.WindowKind, last5h.ResetAt)
	}
	if last5h.OldestAt == nil || last5h.DecayAt == nil {
		t.Fatalf("last5h = oldest %v decay %v, want both non-nil", last5h.OldestAt, last5h.DecayAt)
	}
	if want := last5h.OldestAt.Add(5 * time.Hour); !last5h.DecayAt.Equal(want) {
		t.Fatalf("last5h.DecayAt = %v, want %v", last5h.DecayAt, want)
	}
}
