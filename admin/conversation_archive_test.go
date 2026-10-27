package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func signInternalIdentity(secret, email, timestamp, method, path string) string {
	canonical := email + "\n" + timestamp + "\n" + method + "\n" + path
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestInternalIdentityAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "s3cr3t-shared-key"
	const path = "/api/v1/internal/conversation-archives"
	h := &Handler{}
	h.SetInternalIdentity(true, secret, time.Minute)

	router := gin.New()
	router.GET(path, h.internalIdentityAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })

	now := strconv.FormatInt(time.Now().Unix(), 10)
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)

	cases := []struct {
		name       string
		email      string
		timestamp  string
		signature  string
		wantStatus int
	}{
		{"valid", "alice@lechun.cc", now, signInternalIdentity(secret, "alice@lechun.cc", now, http.MethodGet, path), http.StatusOK},
		{"missing email", "", now, signInternalIdentity(secret, "", now, http.MethodGet, path), http.StatusUnauthorized},
		{"bad signature", "alice@lechun.cc", now, "deadbeef", http.StatusUnauthorized},
		{"signature for another path", "alice@lechun.cc", now, signInternalIdentity(secret, "alice@lechun.cc", now, http.MethodGet, "/api/v1/internal/conversation-archives/1"), http.StatusUnauthorized},
		{"expired", "alice@lechun.cc", stale, signInternalIdentity(secret, "alice@lechun.cc", stale, http.MethodGet, path), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.email != "" {
				req.Header.Set("X-Lechun-Email", tc.email)
			}
			if tc.timestamp != "" {
				req.Header.Set("X-Lechun-Timestamp", tc.timestamp)
			}
			if tc.signature != "" {
				req.Header.Set("X-Lechun-Signature", tc.signature)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestInternalRoutesNotRegisteredWithoutSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, setup := range []func(*Handler){
		func(h *Handler) {},
		func(h *Handler) { h.SetInternalIdentity(false, "secret", time.Minute) },
		func(h *Handler) { h.SetInternalIdentity(true, "", time.Minute) },
	} {
		h := &Handler{}
		setup(h)
		router := gin.New()
		h.registerInternalConversationArchiveRoutes(router)
		if len(router.Routes()) != 0 {
			t.Fatalf("未启用时不应注册内部路由: %v", router.Routes())
		}
	}
}

func TestParseConversationArchiveFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var got filterProbe
	router.GET("/probe", func(c *gin.Context) {
		f := parseConversationArchiveFilter(c)
		got.stream = f.Stream
		got.truncated = f.Truncated
		got.from = f.From
		got.to = f.To
		got.limit = f.Limit
		got.offset = f.Offset
		got.ownerEmail = f.OwnerEmail
		got.keyword = f.Keyword
		got.statusCode = f.StatusCode
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet,
		"/probe?owner_email=a@b.c&keyword=%E5%BA%93%E5%AD%98&stream=true&truncated=0&status_code=500&limit=20&offset=40&from=2026-10-01&to=1793000000", nil)
	router.ServeHTTP(httptest.NewRecorder(), req)

	if got.ownerEmail != "a@b.c" || got.keyword != "库存" || got.statusCode != 500 || got.limit != 20 || got.offset != 40 {
		t.Fatalf("filter parse: %+v", got)
	}
	if got.stream == nil || !*got.stream {
		t.Fatalf("stream 应为 true")
	}
	if got.truncated == nil || *got.truncated {
		t.Fatalf("truncated 应为 false")
	}
	if got.from.IsZero() || got.to.IsZero() {
		t.Fatalf("时间解析失败: from=%v to=%v", got.from, got.to)
	}
}

type filterProbe struct {
	stream     *bool
	truncated  *bool
	from, to   time.Time
	limit      int
	offset     int
	statusCode int
	ownerEmail string
	keyword    string
}

func TestParseArchiveFilterAcceptsToolsContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var got database.ConversationArchiveFilter
	router.GET("/probe", func(c *gin.Context) {
		got = parseConversationArchiveFilter(c)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet,
		"/probe?page=3&page_size=20&user_id=7&username=alice&session_key=sess-1&protocol=openai_chat&start_time=2026-10-01&end_time=2026-10-31", nil)
	router.ServeHTTP(httptest.NewRecorder(), req)

	if got.APIKeyID != 7 {
		t.Fatalf("user_id 应映射到 api_key_id, got %d", got.APIKeyID)
	}
	if got.OwnerEmailLike != "alice" {
		t.Fatalf("username 应映射到归属邮箱模糊匹配, got %q", got.OwnerEmailLike)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("session_key 应映射到 session_id, got %q", got.SessionID)
	}
	if got.Limit != 20 || got.Offset != 40 {
		t.Fatalf("page/page_size 映射错误: limit=%d offset=%d", got.Limit, got.Offset)
	}
	if len(got.EndpointIn) != 2 || got.EndpointIn[0] != "/v1/chat/completions" {
		t.Fatalf("protocol 映射错误: %+v", got.EndpointIn)
	}
	if got.From.IsZero() || got.To.IsZero() {
		t.Fatalf("start_time/end_time 未解析: %v %v", got.From, got.To)
	}
}

func TestConversationArchiveStatsHandler(t *testing.T) {
	handler, db := newAPIKeyOwnerTestHandler(t)
	ctx := context.Background()
	if err := db.EnsureConversationRecords(ctx); err != nil {
		t.Fatalf("EnsureConversationRecords: %v", err)
	}
	if err := db.EnsureConversationArchiveSchema(ctx); err != nil {
		t.Fatalf("EnsureConversationArchiveSchema: %v", err)
	}
	now := time.Now().UTC()
	for i, model := range []string{"gpt-6-astra", "gpt-6-astra", "claude-sonnet-4-6"} {
		if err := db.SaveConversationRecord(ctx, database.ConversationRecordInput{
			RequestID: "stats-" + strconv.Itoa(i), SessionID: "stats-sess", Model: model,
			Endpoint: "/v1/chat/completions", Status: database.ConversationStatusCompleted, StatusCode: 200,
			InputTokens: 10, OutputTokens: 4, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("SaveConversationRecord: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/internal/conversation-archives/stats?group_by=model&limit=10", nil)
	handler.ConversationArchiveStats(ginCtx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		GroupBy       string `json:"group_by"`
		TotalRequests int64  `json:"total_requests"`
		Items         []struct {
			Key      string `json:"key"`
			Requests int64  `json:"requests"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.GroupBy != "model" || payload.TotalRequests != 3 || len(payload.Items) != 2 {
		t.Fatalf("stats payload = %+v", payload)
	}
	if payload.Items[0].Key != "gpt-6-astra" || payload.Items[0].Requests != 2 {
		t.Fatalf("top group = %+v", payload.Items[0])
	}
}
