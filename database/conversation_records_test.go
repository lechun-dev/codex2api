package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConversationRecordsTableIsOptional(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "conversation-disabled.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.conn.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='conversation_records'
	`).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master error: %v", err)
	}
	if count != 0 {
		t.Fatalf("conversation_records exists before EnsureConversationRecords: count=%d", count)
	}
}

func TestConversationRecordsSQLiteSaveUpdateAndLookup(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	defer db.Close()
	if err := db.EnsureConversationRecords(context.Background()); err != nil {
		t.Fatalf("EnsureConversationRecords error: %v", err)
	}

	ctx := context.Background()
	startedAt := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	if err := db.SaveConversationRecord(ctx, ConversationRecordInput{
		RequestID:    "request-1",
		SessionID:    "session-1",
		APIKeyID:     7,
		APIKeyName:   "production",
		ClientID:     "client-1",
		ClientIP:     "127.0.0.1",
		ResponseID:   "resp-1",
		Endpoint:     "/v1/responses",
		Model:        "gpt-5.4",
		UserMessage:  "hello",
		Status:       ConversationStatusPartial,
		StatusCode:   200,
		InputTokens:  10,
		OutputTokens: 2,
		DurationMs:   100,
		CreatedAt:    startedAt,
		UpdatedAt:    startedAt,
	}); err != nil {
		t.Fatalf("initial SaveConversationRecord error: %v", err)
	}

	completedAt := startedAt.Add(time.Second)
	if err := db.SaveConversationRecord(ctx, ConversationRecordInput{
		RequestID:          "request-1",
		SessionID:          "session-1",
		APIKeyID:           7,
		APIKeyName:         "production",
		ClientID:           "client-1",
		ClientIP:           "127.0.0.1",
		ResponseID:         "resp-2",
		PreviousResponseID: "resp-1",
		Endpoint:           "/v1/responses",
		Model:              "gpt-5.4",
		UserMessage:        "hello",
		AssistantMessage:   "world",
		Status:             ConversationStatusCompleted,
		StatusCode:         200,
		InputTokens:        20,
		OutputTokens:       8,
		DurationMs:         250,
		CreatedAt:          startedAt,
		UpdatedAt:          completedAt,
		CompletedAt:        &completedAt,
	}); err != nil {
		t.Fatalf("updated SaveConversationRecord error: %v", err)
	}

	for _, responseID := range []string{"resp-1", "resp-2"} {
		record, err := db.FindConversationByResponseID(ctx, 7, responseID)
		if err != nil {
			t.Fatalf("FindConversationByResponseID(%s) error: %v", responseID, err)
		}
		if record == nil || record.RequestID != "request-1" || record.SessionID != "session-1" {
			t.Fatalf("FindConversationByResponseID(%s) = %#v", responseID, record)
		}
		if record.UserMessage != "hello" || record.AssistantMessage != "world" || record.CompletedAt == nil {
			t.Fatalf("stored conversation = %#v", record)
		}
	}

	if err := db.SaveConversationRecord(ctx, ConversationRecordInput{
		RequestID:   "request-conflicting-response",
		SessionID:   "session-conflicting-response",
		APIKeyID:    7,
		ResponseID:  "resp-2",
		UserMessage: "must not overwrite another request",
		Status:      ConversationStatusCompleted,
		CreatedAt:   startedAt,
		UpdatedAt:   startedAt,
	}); err == nil {
		t.Fatal("SaveConversationRecord must return a response_id uniqueness error")
	}

	if err := db.SaveConversationRecord(ctx, ConversationRecordInput{
		RequestID:   "request-partial",
		SessionID:   "session-partial",
		APIKeyID:    7,
		ClientID:    "client-1",
		ResponseID:  "resp-partial",
		UserMessage: "run a tool",
		Status:      ConversationStatusPartial,
		CreatedAt:   completedAt,
		UpdatedAt:   completedAt,
	}); err != nil {
		t.Fatalf("partial SaveConversationRecord error: %v", err)
	}
	partial, err := db.FindLatestPendingConversation(ctx, 7, "session-partial", "client-1")
	if err != nil || partial == nil || partial.RequestID != "request-partial" {
		t.Fatalf("FindLatestPendingConversation = %#v, err=%v", partial, err)
	}

	if err := db.SaveConversationRecord(ctx, ConversationRecordInput{
		RequestID:   "request-completed-empty",
		SessionID:   "session-completed-empty",
		APIKeyID:    7,
		ClientID:    "client-1",
		ResponseID:  "resp-completed-empty",
		UserMessage: "run another tool",
		Status:      ConversationStatusCompleted,
		StatusCode:  200,
		CreatedAt:   completedAt,
		UpdatedAt:   completedAt,
	}); err != nil {
		t.Fatalf("completed empty SaveConversationRecord error: %v", err)
	}
	pending, err := db.FindLatestPendingConversation(ctx, 7, "session-completed-empty", "client-1")
	if err != nil || pending == nil || pending.RequestID != "request-completed-empty" {
		t.Fatalf("completed empty pending conversation = %#v, err=%v", pending, err)
	}
}

func TestConversationMySQLDDLIsMySQL56Compatible(t *testing.T) {
	ddl := strings.ToUpper(conversationRecordsMySQLDDL)
	if strings.Count(ddl, "LONGTEXT NULL") != 2 {
		t.Fatalf("MySQL DDL must use LONGTEXT NULL for both message columns:\n%s", conversationRecordsMySQLDDL)
	}
	for _, forbidden := range []string{
		" JSON",
		"ADD COLUMN IF NOT EXISTS",
		"WITH RECURSIVE",
		"ROW_NUMBER(",
		"LONGTEXT DEFAULT",
	} {
		if strings.Contains(ddl, forbidden) {
			t.Fatalf("MySQL 5.6 incompatible fragment %q in DDL", forbidden)
		}
	}
	for _, required := range []string{
		"ENGINE=INNODB",
		"CHARACTER SET ASCII",
		"KEY IDX_CONVERSATION_SESSION_CREATED",
		"UNIQUE KEY UK_CONVERSATION_RESPONSE",
	} {
		if !strings.Contains(ddl, required) {
			t.Fatalf("MySQL DDL missing %q", required)
		}
	}
}

func TestConversationMySQLDDLMatchesStandaloneScript(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_conversation_records.sql"))
	if err != nil {
		t.Fatalf("read standalone MySQL DDL: %v", err)
	}
	normalize := func(value string) string {
		return strings.Join(strings.Fields(value), " ")
	}
	if !strings.Contains(normalize(string(script)), normalize(strings.TrimSpace(conversationRecordsMySQLDDL))) {
		t.Fatal("standalone MySQL DDL differs from the embedded conversation_records DDL")
	}
}

func TestSaveConversationRecordUsesMySQL56PlaceholderRewrite(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-conversation-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	now := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	if err := db.SaveConversationRecord(context.Background(), ConversationRecordInput{
		RequestID:        "request-mysql56",
		SessionID:        "session-mysql56",
		APIKeyID:         8,
		APIKeyName:       "production",
		ClientID:         "client-mysql56",
		ClientIP:         "127.0.0.1",
		ResponseID:       "resp-mysql56",
		Endpoint:         "/v1/responses",
		Model:            "gpt-5.4",
		UserMessage:      "user message",
		AssistantMessage: "assistant message",
		Status:           ConversationStatusCompleted,
		StatusCode:       200,
		CreatedAt:        now,
		UpdatedAt:        now,
		CompletedAt:      &now,
	}); err != nil {
		t.Fatalf("SaveConversationRecord() error = %v", err)
	}

	if strings.Contains(capture.query, "$1") || strings.Count(capture.query, "?") != 20 {
		t.Fatalf("unexpected rewritten MySQL query: %s", capture.query)
	}
	if len(capture.args) != 20 {
		t.Fatalf("rewritten MySQL argument count = %d, want 20", len(capture.args))
	}
	if capture.args[0].Value != "session-mysql56" || capture.args[19].Value != "request-mysql56" {
		t.Fatalf("rewritten MySQL argument order is wrong: first=%#v last=%#v", capture.args[0].Value, capture.args[19].Value)
	}
}
