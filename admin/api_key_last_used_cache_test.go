package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

// 2026-11-02 coder(lq): Key display caching never affects persisted quotas and reports freshness explicitly.
func TestListAPIKeysCachesLastUseDisplay(t *testing.T) {
	db := newTestAdminDB(t)
	ctx := context.Background()
	id, err := db.InsertAPIKey(ctx, "last-use-cache", "sk-last-use-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{db: db}
	read := func() apiKeysResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/api-keys", nil)
		handler.ListAPIKeys(c)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var result apiKeysResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := read(); len(got.Keys) != 1 || got.LastUsedStatsState != "ready" || (got.LastUsedStatsAt == nil || got.LastUsedStatsAt.IsZero()) {
		t.Fatalf("first response: %+v", got)
	}
	if err := db.InsertUsageLog(ctx, &database.UsageLogInput{APIKeyID: id, Model: "test", StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	db.FlushUsageLogs()
	if got := read(); got.Keys[0].LastUsedAt != nil {
		t.Fatal("cache bypassed on repeated list refresh")
	}
	handler.displayStats.clear()
	if got := read(); got.Keys[0].LastUsedAt == nil {
		t.Fatal("clear did not refresh last use")
	}
	handler.displayStats.mu.Lock()
	entry := handler.displayStats.entries["api-key-last-used"]
	entry.updatedAt = time.Now().Add(-time.Minute)
	handler.displayStats.mu.Unlock()
	if got := read(); got.LastUsedStatsState != "stale" || got.Keys[0].LastUsedAt == nil {
		t.Fatal("stale display was not retained/labeled")
	}
	// Wait for the detached refresh before the disposable DB is closed.
	handler.displayStats.mu.Lock()
	done := entry.done
	handler.displayStats.mu.Unlock()
	if done != nil {
		<-done
	}
}
