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
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

const internalConversationMaxBodyBytes = 1 << 20

// SetInternalConversationArchive 配置内部 HMAC 通道；secret 为空表示不开启（路由不注册）。
func (h *Handler) SetInternalConversationArchive(secret string, maxSkew time.Duration) {
	if h == nil {
		return
	}
	h.internalConversationSecret = strings.TrimSpace(secret)
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	h.internalConversationMaxSkew = maxSkew
}

func (h *Handler) registerConversationArchiveRoutes(api *gin.RouterGroup) {
	api.GET("/conversation-archives", h.ListConversationArchives)
	api.GET("/conversation-archives/:id", h.GetConversationArchive)
	api.PUT("/keys/:id/owner", h.SetAPIKeyOwner)
}

func (h *Handler) registerInternalConversationArchiveRoutes(r *gin.Engine) {
	if h == nil || h.internalConversationSecret == "" {
		return
	}
	internal := r.Group("/api/internal/conversation-archives")
	internal.Use(h.internalConversationAuth())
	internal.GET("", h.ListConversationArchives)
	internal.GET("/:id", h.GetConversationArchive)
}

// internalConversationAuth 校验 HMAC-SHA256 签名：
//
//	canonical = METHOD \n PATH \n QUERY \n TIMESTAMP \n SHA256(body)
//	X-Internal-Signature = hex(HMAC-SHA256(secret, canonical))
//
// 时间戳超出 maxSkew 直接拒绝，防止重放。
func (h *Handler) internalConversationAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		timestamp := strings.TrimSpace(c.GetHeader("X-Internal-Timestamp"))
		signature := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Internal-Signature")))
		if timestamp == "" || signature == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少内部签名头"})
			return
		}
		ts, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "时间戳无效"})
			return
		}
		if skew := time.Since(time.Unix(ts, 0)); skew > h.internalConversationMaxSkew || skew < -h.internalConversationMaxSkew {
			security.SecurityAuditLog("INTERNAL_ARCHIVE_REJECTED", "reason=timestamp_skew path="+c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "时间戳超出允许范围"})
			return
		}
		var body []byte
		if c.Request.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(c.Request.Body, internalConversationMaxBodyBytes))
			c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		sum := sha256.Sum256(body)
		canonical := strings.Join([]string{
			c.Request.Method,
			c.Request.URL.Path,
			c.Request.URL.RawQuery,
			timestamp,
			hex.EncodeToString(sum[:]),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(h.internalConversationSecret))
		mac.Write([]byte(canonical))
		expected := hex.EncodeToString(mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
			security.SecurityAuditLog("INTERNAL_ARCHIVE_REJECTED", "reason=bad_signature path="+c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "签名校验失败"})
			return
		}
		security.SecurityAuditLog("INTERNAL_ARCHIVE_ACCESS", "path="+c.Request.URL.Path+" query="+c.Request.URL.RawQuery)
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
