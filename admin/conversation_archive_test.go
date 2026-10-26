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
