package database

// 真 MySQL 5.6 上的会话存档测试：建表补列、按人过滤、保留策略（MySQL 用
// DELETE ... ORDER BY ... LIMIT，5.6 不支持带 LIMIT 的 IN 子查询）都在这里跑。
// 测试名匹配 CI 的 TestMySQL.*Integration 门禁。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMySQLConversationArchiveIntegration(t *testing.T) {
	dsn := mysqlTempDatabase(t, mysqlIntelligenceDSN(t))
	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.EnsureConversationRecords(ctx); err != nil {
		t.Fatalf("EnsureConversationRecords: %v", err)
	}
	if err := db.EnsureConversationArchiveSchema(ctx); err != nil {
		t.Fatalf("EnsureConversationArchiveSchema: %v", err)
	}
	if err := db.EnsureAPIKeyOwnerEmail(ctx); err != nil {
		t.Fatalf("EnsureAPIKeyOwnerEmail: %v", err)
	}
	// 可重复执行
	if err := db.EnsureConversationArchiveSchema(ctx); err != nil {
		t.Fatalf("second EnsureConversationArchiveSchema: %v", err)
	}

	keyID, err := db.InsertAPIKey(ctx, "mysql-archive-key", "sk-mysql-archive-owner")
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.conn.ExecContext(context.Background(), `DELETE FROM conversation_records WHERE api_key_id=?`, keyID)
		_, _ = db.conn.ExecContext(context.Background(), `DELETE FROM api_keys WHERE id=?`, keyID)
	})
	if err := db.SetAPIKeyOwnerEmail(ctx, keyID, "bob@lechun.cc"); err != nil {
		t.Fatalf("SetAPIKeyOwnerEmail: %v", err)
	}
	if email, err := db.GetAPIKeyOwnerEmail(ctx, keyID); err != nil || email != "bob@lechun.cc" {
		t.Fatalf("GetAPIKeyOwnerEmail = (%q,%v)", email, err)
	}

	now := time.Now().UTC()
	base := ConversationRecordInput{
		APIKeyID: keyID, APIKeyName: "mysql-archive-key", ClientID: "client-mysql",
		ClientIP: "10.0.0.1", Endpoint: "/v1/responses", Model: "gpt-6-astra",
		Status: ConversationStatusCompleted, StatusCode: 200,
		CreatedAt: now, UpdatedAt: now,
	}
	fresh := base
	fresh.RequestID, fresh.SessionID = "mysql-req-new", "mysql-sess-new"
	fresh.UserMessage, fresh.AssistantMessage = "今天发多少单", "共 128 单"
	if err := db.SaveConversationRecord(ctx, fresh); err != nil {
		t.Fatalf("SaveConversationRecord(fresh): %v", err)
	}
	oldRow := base
	oldRow.RequestID, oldRow.SessionID = "mysql-req-old", "mysql-sess-old"
	oldRow.UserMessage, oldRow.AssistantMessage = "过期会话", "过期回答"
	oldRow.CreatedAt = now.AddDate(0, 0, -120)
	oldRow.UpdatedAt = oldRow.CreatedAt
	if err := db.SaveConversationRecord(ctx, oldRow); err != nil {
		t.Fatalf("SaveConversationRecord(old): %v", err)
	}

	if _, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{APIKeyID: keyID}, false); err != nil || total != 2 {
		t.Fatalf("按 API Key 列表: total=%d err=%v", total, err)
	}
	if _, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{OwnerEmail: "BOB@lechun.cc"}, false); err != nil || total != 2 {
		t.Fatalf("按邮箱过滤: total=%d err=%v", total, err)
	}
	keyword, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{Keyword: "128"}, false)
	if err != nil || total != 1 || !strings.Contains(keyword[0].RequestID, "mysql-req-new") {
		t.Fatalf("关键词过滤: total=%d err=%v", total, err)
	}
	full, err := db.GetConversationArchive(ctx, keyword[0].ID)
	if err != nil || full.UserMessage != "今天发多少单" {
		t.Fatalf("详情: %+v err=%v", full, err)
	}

	removed, err := db.PurgeConversationArchivesBefore(ctx, now.AddDate(0, 0, -90), 100)
	if err != nil || removed != 1 {
		t.Fatalf("保留策略: removed=%d err=%v", removed, err)
	}
	if _, total, err := db.ListConversationArchives(ctx, ConversationArchiveFilter{APIKeyID: keyID}, false); err != nil || total != 1 {
		t.Fatalf("清理后剩余: total=%d err=%v", total, err)
	}
}
