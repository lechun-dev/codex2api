package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newArchiveE2EHandler(t *testing.T) (*Handler, *database.DB) {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "archive-e2e.db"))
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
	archiver := newConversationArchiver(db)
	t.Cleanup(archiver.Close)
	return &Handler{db: db, convArchiver: archiver}, db
}

func withCachedBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		var body []byte
		if c.Request.Body != nil {
			body, _ = io.ReadAll(c.Request.Body)
		}
		c.Set("raw_body", body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}

// 端到端：请求进中间件 → 归档异步落库 → 能查到正文与用量。
func TestConversationArchiveMiddlewarePersistsTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newArchiveE2EHandler(t)

	router := gin.New()
	router.Use(withCachedBody())
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n")
		_, _ = c.Writer.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5}}\n\n")
		c.Status(http.StatusOK)
	})

	body := []byte(`{"model":"gpt-6-astra","stream":true,"messages":[{"role":"user","content":"库存对不上怎么办"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", "req-archive-e2e")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var (
		records []database.ConversationRecord
		err     error
	)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, _, err = db.ListConversationArchives(context.Background(),
			database.ConversationArchiveFilter{RequestID: "req-archive-e2e"}, true)
		if err == nil && len(records) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(records) != 1 {
		t.Fatalf("未落库，records=%d err=%v", len(records), err)
	}
	record := records[0]
	if record.UserMessage != "库存对不上怎么办" || record.AssistantMessage != "你好" {
		t.Fatalf("正文不匹配: %+v", record)
	}
	if record.InputTokens != 12 || record.OutputTokens != 5 {
		t.Fatalf("token 未解析: %+v", record)
	}
	if !record.Stream {
		t.Fatalf("stream 标记缺失: %+v", record)
	}
	if record.Model != "gpt-6-astra" || record.Endpoint != "/v1/chat/completions" {
		t.Fatalf("元数据不匹配: %+v", record)
	}
	if record.Status != database.ConversationStatusCompleted {
		t.Fatalf("status = %q", record.Status)
	}
}

// 归档关闭时中间件必须直通。
func TestConversationArchiveMiddlewareDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	router := gin.New()
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"x"}]}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// 只归档文本端点：图片请求不应被采集。
func TestConversationArchiveSkipsNonTextEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newArchiveE2EHandler(t)

	router := gin.New()
	router.Use(withCachedBody())
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/images/generations", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		bytes.NewReader([]byte(`{"prompt":"a cat"}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	time.Sleep(100 * time.Millisecond)
	if records, total, _ := db.ListConversationArchives(context.Background(), database.ConversationArchiveFilter{}, true); total != 0 || len(records) != 0 {
		t.Fatalf("图片端点不应归档: total=%d", total)
	}
}

// 鉴权失败（401/403）的请求不应归档：内容没有归属人，也不该把未认证原文写库。
func TestConversationArchiveSkipsUnauthorizedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newArchiveE2EHandler(t)

	router := gin.New()
	router.Use(withCachedBody())
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"未认证内容"}]}`))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if _, total, _ := db.ListConversationArchives(context.Background(), database.ConversationArchiveFilter{}, true); total != 0 {
		t.Fatalf("401 请求不应归档，实际 %d 条", total)
	}
}
