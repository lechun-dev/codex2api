package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newArchiveE2EHandler(t *testing.T) (*Handler, *database.DB) {
	t.Helper()
	return newArchiveE2EHandlerWithExclude(t, config.ConversationArchiveExclude{})
}

func newArchiveE2EHandlerWithExclude(t *testing.T, exclude config.ConversationArchiveExclude) (*Handler, *database.DB) {
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
	archiver := newConversationArchiver(db, exclude)
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

func newArchiveChatRouter(h *Handler) *gin.Engine {
	router := gin.New()
	router.Use(withCachedBody())
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"收到\"}}]}\n\n")
		c.Status(http.StatusOK)
	})
	return router
}

func sendArchiveChat(t *testing.T, router *gin.Engine, requestID string, decorate func(*http.Request)) {
	t.Helper()
	body := []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"本机调试内容"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", requestID)
	if decorate != nil {
		decorate(req)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// 等异步队列落库，确认「没有写入」时也要给它同样的时间窗。
	time.Sleep(200 * time.Millisecond)
}

func archiveTotal(t *testing.T, db *database.DB) int {
	t.Helper()
	_, total, err := db.ListConversationArchives(context.Background(), database.ConversationArchiveFilter{}, true)
	if err != nil {
		t.Fatalf("ListConversationArchives: %v", err)
	}
	return total
}

// 机器码（Codex installation_id）命中的请求不归档，其它机器照常归档。
func TestConversationArchiveExcludesMachine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const machineID = "92217c0d-827d-4af6-845e-dbe2d49b693e"
	exclude, err := config.ParseConversationArchiveExclude("machine:" + strings.ToUpper(machineID))
	if err != nil {
		t.Fatalf("ParseConversationArchiveExclude: %v", err)
	}
	h, db := newArchiveE2EHandlerWithExclude(t, exclude)
	router := newArchiveChatRouter(h)

	sendArchiveChat(t, router, "req-machine-excluded", func(req *http.Request) {
		req.Header.Set("X-Codex-Turn-Metadata", `{"installation_id":"`+machineID+`","session_id":"s-1"}`)
	})
	if total := archiveTotal(t, db); total != 0 {
		t.Fatalf("命中排除机器码的请求不应归档，实际 %d 条", total)
	}

	sendArchiveChat(t, router, "req-machine-kept", func(req *http.Request) {
		req.Header.Set("X-Codex-Turn-Metadata", `{"installation_id":"11111111-2222-3333-4444-555555555555","session_id":"s-2"}`)
	})
	if total := archiveTotal(t, db); total != 1 {
		t.Fatalf("未命中排除项的机器应照常归档，实际 %d 条", total)
	}
}

// 请求体 client_metadata 里的机器码同样生效。
func TestConversationArchiveExcludesMachineFromBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const machineID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	exclude, err := config.ParseConversationArchiveExclude(machineID)
	if err != nil {
		t.Fatalf("ParseConversationArchiveExclude: %v", err)
	}
	h, db := newArchiveE2EHandlerWithExclude(t, exclude)

	router := gin.New()
	router.Use(withCachedBody())
	router.Use(h.ConversationArchiveMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"收到\"}\n\n")
		c.Status(http.StatusOK)
	})
	body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"私聊内容"}],` +
		`"client_metadata":{"x-codex-installation-id":"` + machineID + `"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", "req-machine-body")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	time.Sleep(200 * time.Millisecond)
	if total := archiveTotal(t, db); total != 0 {
		t.Fatalf("请求体机器码命中排除项时不应归档，实际 %d 条", total)
	}
}

// 来源 IP / 网段排除。
func TestConversationArchiveExcludesClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exclude, err := config.ParseConversationArchiveExclude("ip:192.0.2.0/24")
	if err != nil {
		t.Fatalf("ParseConversationArchiveExclude: %v", err)
	}
	h, db := newArchiveE2EHandlerWithExclude(t, exclude)
	router := newArchiveChatRouter(h)

	sendArchiveChat(t, router, "req-ip-excluded", func(req *http.Request) {
		req.RemoteAddr = "192.0.2.7:51000"
	})
	if total := archiveTotal(t, db); total != 0 {
		t.Fatalf("命中排除网段的请求不应归档，实际 %d 条", total)
	}

	sendArchiveChat(t, router, "req-ip-kept", func(req *http.Request) {
		req.RemoteAddr = "198.51.100.7:51000"
	})
	if total := archiveTotal(t, db); total != 1 {
		t.Fatalf("网段外的请求应照常归档，实际 %d 条", total)
	}
}

// API Key 维度的排除发生在鉴权之后。
func TestConversationArchiveExcludesAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exclude, err := config.ParseConversationArchiveExclude("key:7")
	if err != nil {
		t.Fatalf("ParseConversationArchiveExclude: %v", err)
	}
	h, db := newArchiveE2EHandlerWithExclude(t, exclude)

	newRouter := func(apiKeyID int64) *gin.Engine {
		router := gin.New()
		router.Use(withCachedBody())
		router.Use(func(c *gin.Context) {
			c.Set(contextAPIKeyID, apiKeyID)
			c.Next()
		})
		router.Use(h.ConversationArchiveMiddleware())
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"收到\"}}]}\n\n")
			c.Status(http.StatusOK)
		})
		return router
	}

	sendArchiveChat(t, newRouter(7), "req-key-excluded", nil)
	if total := archiveTotal(t, db); total != 0 {
		t.Fatalf("命中排除 API Key 的请求不应归档，实际 %d 条", total)
	}

	sendArchiveChat(t, newRouter(8), "req-key-kept", nil)
	if total := archiveTotal(t, db); total != 1 {
		t.Fatalf("其它 API Key 应照常归档，实际 %d 条", total)
	}
}
