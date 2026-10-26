package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newConversationArchiveTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.EnsureConversationRecords(ctx); err != nil {
		t.Fatalf("EnsureConversationRecords: %v", err)
	}
	if err := db.EnsureConversationArchiveSchema(ctx); err != nil {
		t.Fatalf("EnsureConversationArchiveSchema: %v", err)
	}
	if err := db.EnsureAPIKeyOwnerEmail(ctx); err != nil {
		t.Fatalf("EnsureAPIKeyOwnerEmail: %v", err)
	}
	return db
}

func saveArchiveRow(t *testing.T, db *DB, input ConversationRecordInput) {
	t.Helper()
	if err := db.SaveConversationRecord(context.Background(), input); err != nil {
		t.Fatalf("SaveConversationRecord(%s): %v", input.RequestID, err)
	}
}

func TestConversationArchiveFiltersAndRetention(t *testing.T) {
	db := newConversationArchiveTestDB(t)
	now := time.Now().UTC()

	keyID, err := db.InsertAPIKey(context.Background(), "team-a", "sk-archive-owner")
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	// 先写存档、后维护邮箱：验证历史存档能按人查到（不需要重新采集）。
	email, err := db.GetAPIKeyOwnerEmail(context.Background(), keyID)
	if err != nil || email != "" {
		t.Fatalf("初始 owner_email 应为空: %q %v", email, err)
	}

	base := ConversationRecordInput{
		APIKeyID:   keyID,
		APIKeyName: "team-a",
		ClientID:   "client-1",
		ClientIP:   "127.0.0.1",
		Endpoint:   "/v1/chat/completions",
		Model:      "gpt-6-astra",
		Status:     ConversationStatusCompleted,
		StatusCode: 200,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	old := base
	old.RequestID, old.SessionID = "req-old", "sess-old"
	old.CreatedAt = now.AddDate(0, 0, -120)
	old.UpdatedAt = old.CreatedAt
	old.UserMessage = "很久以前的问题"
	old.AssistantMessage = "很久以前的回答"
	old.Truncated = true
	streamTrue := true
	old.Stream = streamTrue
	saveArchiveRow(t, db, old)

	recent := base
	recent.RequestID, recent.SessionID = "req-new", "sess-new"
	recent.UserMessage = "上次库存对不上"
	recent.AssistantMessage = "已核对，差异 3 件"
	saveArchiveRow(t, db, recent)

	ctx := context.Background()
	all, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{}, false)
	if err != nil || total != 2 || len(all) != 2 {
		t.Fatalf("List all: total=%d len=%d err=%v", total, len(all), err)
	}
	if all[0].UserMessage != "" || all[0].UserMessageLen == 0 {
		t.Fatalf("列表默认不应返回正文，但要有长度: %+v", all[0])
	}

	// 现在才维护邮箱：两条历史存档都应立刻能按邮箱查到。
	if err := db.SetAPIKeyOwnerEmail(ctx, keyID, "alice@lechun.cc"); err != nil {
		t.Fatalf("SetAPIKeyOwnerEmail: %v", err)
	}
	byOwner, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{OwnerEmail: "ALICE@lechun.cc"}, false)
	if err != nil || total != 2 {
		t.Fatalf("按邮箱过滤: total=%d err=%v", total, err)
	}
	if byOwner[0].OwnerEmail != "alice@lechun.cc" {
		t.Fatalf("归属人应来自 join api_keys: %+v", byOwner[0])
	}
	if _, total, err = db.ListConversationArchives(ctx, ConversationArchiveFilter{OwnerEmail: "nobody@lechun.cc"}, false); err != nil || total != 0 {
		t.Fatalf("无匹配邮箱应为 0: total=%d err=%v", total, err)
	}

	keyword, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{Keyword: "库存"}, false)
	if err != nil || total != 1 || keyword[0].RequestID != "req-new" {
		t.Fatalf("关键词过滤: total=%d items=%+v err=%v", total, keyword, err)
	}

	truncated, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{Truncated: &streamTrue}, false)
	if err != nil || total != 1 || truncated[0].RequestID != "req-old" {
		t.Fatalf("truncated 过滤: total=%d err=%v", total, err)
	}

	window, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{
		From: now.AddDate(0, 0, -1), To: now.AddDate(0, 0, 1),
	}, false)
	if err != nil || total != 1 || window[0].RequestID != "req-new" {
		t.Fatalf("时间窗口过滤: total=%d err=%v", total, err)
	}

	page, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{Offset: 1, Limit: 1}, false)
	if err != nil || total != 2 || len(page) != 1 {
		t.Fatalf("分页: total=%d len=%d err=%v", total, len(page), err)
	}

	full, err := db.GetConversationArchive(ctx, window[0].ID)
	if err != nil || full.UserMessage != "上次库存对不上" || full.AssistantMessage == "" {
		t.Fatalf("GetConversationArchive: %+v err=%v", full, err)
	}

	removed, err := db.PurgeConversationArchivesBefore(ctx, now.AddDate(0, 0, -90), 100)
	if err != nil || removed != 1 {
		t.Fatalf("保留策略: removed=%d err=%v", removed, err)
	}
	if _, total, err = db.ListConversationArchives(ctx, ConversationArchiveFilter{}, false); err != nil || total != 1 {
		t.Fatalf("清理后剩余: total=%d err=%v", total, err)
	}
}

func TestConversationArchiveSchemaIsIdempotent(t *testing.T) {
	db := newConversationArchiveTestDB(t)
	ctx := context.Background()
	if err := db.EnsureConversationArchiveSchema(ctx); err != nil {
		t.Fatalf("second EnsureConversationArchiveSchema: %v", err)
	}
	if err := db.EnsureAPIKeyOwnerEmail(ctx); err != nil {
		t.Fatalf("second EnsureAPIKeyOwnerEmail: %v", err)
	}
}
