package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func signInternalRequest(secret, method, path, query, timestamp, body string) string {
	sum := sha256.Sum256([]byte(body))
	canonical := method + "\n" + path + "\n" + query + "\n" + timestamp + "\n" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestInternalConversationAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "s3cr3t-shared-key"
	h := &Handler{}
	h.SetInternalConversationArchive(secret, time.Minute)

	router := gin.New()
	router.GET("/api/internal/conversation-archives", h.internalConversationAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	now := strconv.FormatInt(time.Now().Unix(), 10)
	valid := signInternalRequest(secret, http.MethodGet, "/api/internal/conversation-archives", "limit=5", now, "")
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)

	cases := []struct {
		name       string
		timestamp  string
		signature  string
		wantStatus int
	}{
		{"valid", now, valid, http.StatusOK},
		{"bad signature", now, "deadbeef", http.StatusUnauthorized},
		{"missing signature", now, "", http.StatusUnauthorized},
		{"skewed timestamp", stale, signInternalRequest(secret, http.MethodGet, "/api/internal/conversation-archives", "limit=5", stale, ""), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/internal/conversation-archives?limit=5", nil)
			if tc.timestamp != "" {
				req.Header.Set("X-Internal-Timestamp", tc.timestamp)
			}
			if tc.signature != "" {
				req.Header.Set("X-Internal-Signature", tc.signature)
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
	h := &Handler{}
	router := gin.New()
	h.registerInternalConversationArchiveRoutes(router)
	if len(router.Routes()) != 0 {
		t.Fatalf("未配置密钥时不应注册内部路由: %v", router.Routes())
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
