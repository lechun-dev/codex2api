package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationSmoke(t *testing.T) {
	dsn := os.Getenv("CODEX2API_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CODEX2API_MYSQL_TEST_DSN to run MySQL integration smoke test")
	}

	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql) failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	smokeKey := "sk-mysql-smoke-" + suffix
	groupName := "mysql-smoke-group-" + suffix
	proxyURL := "http://127.0.0.1:18080/" + suffix
	modelID := "mysql-smoke-model-" + suffix
	templateName := "mysql-smoke-template-" + suffix
	t.Cleanup(func() {
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM image_prompt_templates WHERE name = ?", templateName)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM model_registry WHERE id = ?", modelID)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM proxies WHERE url = ?", proxyURL)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM account_groups WHERE name = ?", groupName)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM api_keys WHERE `key` = ?", smokeKey)
	})

	keyID, err := db.InsertAPIKeyWithOptions(ctx, APIKeyInput{
		Name:            "mysql smoke key",
		Key:             smokeKey,
		QuotaLimit:      123.5,
		AllowedGroupIDs: []int64{2, 1, 2},
		Limits: APIKeyLimits{
			ModelAllow: []string{"gpt-5.4"},
			RPM:        10,
		},
	})
	if err != nil {
		t.Fatalf("InsertAPIKeyWithOptions failed: %v", err)
	}
	row, err := db.GetAPIKeyByValue(ctx, smokeKey)
	if err != nil {
		t.Fatalf("GetAPIKeyByValue failed: %v", err)
	}
	if row.ID != keyID || row.Key != smokeKey || len(row.AllowedGroupIDs) != 3 || row.Limits.RPM != 10 {
		t.Fatalf("unexpected API key row: %#v", row)
	}

	groupID, err := db.CreateAccountGroup(ctx, groupName, "integration", "#123456")
	if err != nil {
		t.Fatalf("CreateAccountGroup failed: %v", err)
	}
	if groupID <= 0 {
		t.Fatalf("groupID = %d, want positive", groupID)
	}

	if inserted, err := db.InsertProxies(ctx, []string{proxyURL, proxyURL}, "smoke"); err != nil {
		t.Fatalf("InsertProxies failed: %v", err)
	} else if inserted != 1 {
		t.Fatalf("InsertProxies inserted = %d, want 1", inserted)
	}

	if err := db.UpsertModelRegistryRows(ctx, []ModelRegistryRow{{
		ID:                  modelID,
		Enabled:             true,
		Category:            "codex",
		Source:              "integration",
		ProOnly:             false,
		APIKeyAuthAvailable: true,
		LastSeenAt:          sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}}); err != nil {
		t.Fatalf("UpsertModelRegistryRows insert failed: %v", err)
	}
	if err := db.UpsertModelRegistryRows(ctx, []ModelRegistryRow{{
		ID:                  modelID,
		Enabled:             false,
		Category:            "codex",
		Source:              "integration-update",
		ProOnly:             true,
		APIKeyAuthAvailable: false,
		LastSeenAt:          sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}}); err != nil {
		t.Fatalf("UpsertModelRegistryRows update failed: %v", err)
	}

	templateID, err := db.InsertImagePromptTemplate(ctx, ImagePromptTemplateInput{
		Name:   templateName,
		Prompt: "draw a production smoke test",
		Tags:   []string{"smoke", "mysql"},
	})
	if err != nil {
		t.Fatalf("InsertImagePromptTemplate failed: %v", err)
	}
	if templateID <= 0 {
		t.Fatalf("templateID = %d, want positive", templateID)
	}

	if snapshot, err := db.GetTrafficSnapshot(ctx); err != nil {
		t.Fatalf("GetTrafficSnapshot failed: %v", err)
	} else if snapshot == nil {
		t.Fatalf("GetTrafficSnapshot returned nil")
	}
}
