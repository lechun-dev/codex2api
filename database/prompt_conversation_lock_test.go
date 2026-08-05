package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromptConversationLockLifecycleAndDecisionReplay(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	input := PromptConversationLockInput{
		LockKey:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Platform: "newapi", NewAPIUserID: "42",
		SessionFingerprint: "0123456789abcdef0123456789abcdef",
		SessionHash:        "session-hash", IncidentID: "incident-1", DecisionID: "decision-1",
		RequestID: "request-1", ReasonCode: "upstream_cyber_policy",
		Endpoint: "/v1/responses", Model: "gpt-5.6-sol", LockedAt: time.Now().UTC(),
	}

	locked, changed, err := db.LockPromptConversation(ctx, input)
	if err != nil || !changed || locked.Status != PromptConversationLockStatusActive || locked.TriggerCount != 1 {
		t.Fatalf("first lock = %#v changed=%t err=%v", locked, changed, err)
	}
	replay, changed, err := db.LockPromptConversation(ctx, input)
	if err != nil || changed || replay.TriggerCount != 1 {
		t.Fatalf("decision replay = %#v changed=%t err=%v", replay, changed, err)
	}

	unlocked, err := db.UnlockPromptConversation(ctx, input.LockKey, "confirmed false positive")
	if err != nil || unlocked.Status != PromptConversationLockStatusUnlocked || unlocked.UnlockCount != 1 || unlocked.UnlockedAt == nil {
		t.Fatalf("unlock = %#v err=%v", unlocked, err)
	}
	if _, err := db.GetActivePromptConversationLock(ctx, input.LockKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("active lookup after unlock err=%v, want sql.ErrNoRows", err)
	}

	// Replaying the original decision after a manual unlock must stay unlocked.
	replay, changed, err = db.LockPromptConversation(ctx, input)
	if err != nil || changed || replay.Status != PromptConversationLockStatusUnlocked {
		t.Fatalf("post-unlock replay = %#v changed=%t err=%v", replay, changed, err)
	}
	input.DecisionID = "decision-2"
	input.IncidentID = "incident-2"
	relocked, changed, err := db.LockPromptConversation(ctx, input)
	if err != nil || !changed || relocked.Status != PromptConversationLockStatusActive || relocked.TriggerCount != 2 {
		t.Fatalf("new CYB relock = %#v changed=%t err=%v", relocked, changed, err)
	}
}

func TestEnsurePromptConversationLocksTableUsesMySQL56DDL(t *testing.T) {
	capture := &mysqlCaptureDriver{}
	driverName := fmt.Sprintf("codex2api-mysql-lock-schema-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})
	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	if err := db.ensurePromptConversationLocksTable(context.Background()); err != nil {
		t.Fatalf("ensurePromptConversationLocksTable() error = %v", err)
	}
	for _, fragment := range []string{"AUTO_INCREMENT", "DATETIME", "ENGINE=InnoDB", "KEY idx_prompt_conversation_locks_status"} {
		if !strings.Contains(capture.query, fragment) {
			t.Fatalf("MySQL lock DDL missing %q: %s", fragment, capture.query)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
}
