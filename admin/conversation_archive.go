package admin

// 会话存档查询 API：
//   - 管理端 GET /api/admin/conversation-archives[/:id]（X-Admin-Key）
//   - 内部端 GET /api/internal/conversation-archives[/:id]（HMAC 共享密钥，供 lechun-tools/AI 调用）
//   - PUT  /api/admin/keys/:id/owner 维护 API Key 归属人，存档按人查询依赖它
//
// 列表默认只返回元数据与正文长度；详情才返回正文，并写入安全审计日志。

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

const (
	// 与 sub2api 内部身份断言完全一致的请求头与签名串，两边共用同一个共享密钥。
	internalIdentityEmailHeader     = "X-Lechun-Email"
	internalIdentityTimestampHeader = "X-Lechun-Timestamp"
	internalIdentitySignatureHeader = "X-Lechun-Signature"

	internalIdentityDefaultMaxSkew = 300 * time.Second
)

// SetInternalIdentity 配置内部签名身份断言（与 sub2api 的 internal_identity 对齐）。
// enabled=false 或 secret 为空时不注册路由，对外表现为 404。
func (h *Handler) SetInternalIdentity(enabled bool, secret string, maxSkew time.Duration) {
	if h == nil {
		return
	}
	h.internalIdentityEnabled = enabled
	h.internalIdentitySecret = strings.TrimSpace(secret)
	if maxSkew <= 0 {
		maxSkew = internalIdentityDefaultMaxSkew
	}
	h.internalIdentityMaxSkew = maxSkew
}

func (h *Handler) registerConversationArchiveRoutes(api *gin.RouterGroup) {
	api.GET("/conversation-archives", h.ListConversationArchives)
	api.GET("/conversation-archives/:id", h.GetConversationArchive)
	api.PUT("/keys/:id/owner", h.SetAPIKeyOwner)
}

func (h *Handler) registerInternalConversationArchiveRoutes(r *gin.Engine) {
	if h == nil || !h.internalIdentityEnabled || h.internalIdentitySecret == "" {
		return
	}
	internal := r.Group("/api/v1/internal/conversation-archives")
	internal.Use(h.internalIdentityAuth())
	internal.GET("", h.ListConversationArchives)
	internal.GET("/:id", h.GetConversationArchive)
}

// internalIdentityAuth 校验内部系统的签名身份断言：
//
//	canonical = email + "\n" + timestamp + "\n" + method + "\n" + path
//	X-Lechun-Signature = hex(HMAC-SHA256(secret, canonical))
//
// 头名、签名串、时钟窗口与 sub2api 的 internal_identity 完全一致，两边共用一个
// 共享密钥即可。codex2api 没有用户表，所以邮箱只作为“调用方声明的身份”落审计，
// 不再做“按邮箱查用户 + 管理员校验”这一跳。
func (h *Handler) internalIdentityAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.internalIdentityEnabled || h.internalIdentitySecret == "" {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}
		email := strings.TrimSpace(c.GetHeader(internalIdentityEmailHeader))
		timestamp := strings.TrimSpace(c.GetHeader(internalIdentityTimestampHeader))
		signature := strings.ToLower(strings.TrimSpace(c.GetHeader(internalIdentitySignatureHeader)))
		if email == "" || timestamp == "" || signature == "" {
			security.SecurityAuditLog("INTERNAL_IDENTITY_REJECTED", "reason=missing_header path="+c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid internal identity signature"})
			return
		}
		unix, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid internal identity signature"})
			return
		}
		skew := int64(h.internalIdentityMaxSkew / time.Second)
		now := time.Now().Unix()
		if unix < now-skew || unix > now+skew {
			security.SecurityAuditLog("INTERNAL_IDENTITY_REJECTED", "reason=expired email="+email+" path="+c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Internal identity signature expired"})
			return
		}
		canonical := email + "\n" + timestamp + "\n" + c.Request.Method + "\n" + c.Request.URL.Path
		mac := hmac.New(sha256.New, []byte(h.internalIdentitySecret))
		_, _ = mac.Write([]byte(canonical))
		expected := hex.EncodeToString(mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
			security.SecurityAuditLog("INTERNAL_IDENTITY_REJECTED", "reason=bad_signature email="+email+" path="+c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid internal identity signature"})
			return
		}
		security.SecurityAuditLog("INTERNAL_IDENTITY_ACCESS", "email="+email+" path="+c.Request.URL.Path+" query="+c.Request.URL.RawQuery)
		c.Set("internal_identity_email", email)
		c.Next()
	}
}

func parseConversationArchiveFilter(c *gin.Context) database.ConversationArchiveFilter {
	query := c.Request.URL.Query()
	filter := database.ConversationArchiveFilter{
		OwnerEmail: query.Get("owner_email"),
		ClientID:   query.Get("client_id"),
		SessionID:  query.Get("session_id"),
		RequestID:  query.Get("request_id"),
		Model:      query.Get("model"),
		Endpoint:   query.Get("endpoint"),
		Status:     query.Get("status"),
		Keyword:    query.Get("keyword"),
		Order:      query.Get("order"),
	}
	filter.APIKeyID, _ = strconv.ParseInt(query.Get("api_key_id"), 10, 64)
	filter.StatusCode, _ = strconv.Atoi(query.Get("status_code"))
	filter.Offset, _ = strconv.Atoi(query.Get("offset"))
	filter.Limit, _ = strconv.Atoi(query.Get("limit"))
	if value, ok := parseOptionalBool(query.Get("stream")); ok {
		filter.Stream = &value
	}
	if value, ok := parseOptionalBool(query.Get("truncated")); ok {
		filter.Truncated = &value
	}
	filter.From = parseArchiveTime(query.Get("from"))
	filter.To = parseArchiveTime(query.Get("to"))
	return filter
}

func parseOptionalBool(raw string) (bool, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func parseArchiveTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if ts, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ts > 1e12 { // 毫秒
			return time.UnixMilli(ts).UTC()
		}
		return time.Unix(ts, 0).UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

// ListConversationArchives 返回存档列表；include_content=true 时附带正文。
func (h *Handler) ListConversationArchives(c *gin.Context) {
	filter := parseConversationArchiveFilter(c)
	includeContent, _ := parseOptionalBool(c.Query("include_content"))
	records, total, err := h.db.ListConversationArchives(c.Request.Context(), filter, includeContent)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	security.SecurityAuditLog("ARCHIVE_LIST", "path="+c.Request.URL.Path+" total="+strconv.Itoa(total))
	c.JSON(http.StatusOK, gin.H{
		"items":           records,
		"total":           total,
		"limit":           filter.Limit,
		"offset":          filter.Offset,
		"include_content": includeContent,
	})
}

// GetConversationArchive 返回单条存档（含正文）。
func (h *Handler) GetConversationArchive(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的存档 ID"})
		return
	}
	record, getErr := h.db.GetConversationArchive(c.Request.Context(), id)
	if getErr != nil {
		writeInternalError(c, getErr)
		return
	}
	security.SecurityAuditLog("ARCHIVE_READ", "path="+c.Request.URL.Path+" id="+strconv.FormatInt(id, 10))
	c.JSON(http.StatusOK, record)
}

// SetAPIKeyOwner 维护 API Key 的归属人邮箱；存档按人查询时 join 这一列。
func (h *Handler) SetAPIKeyOwner(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 API Key ID"})
		return
	}
	var payload struct {
		OwnerEmail string `json:"owner_email"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	email := strings.TrimSpace(payload.OwnerEmail)
	if email != "" && !strings.Contains(email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "owner_email 格式无效"})
		return
	}
	if err := h.db.SetAPIKeyOwnerEmail(c.Request.Context(), id, email); err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key_id": id, "owner_email": email})
}
