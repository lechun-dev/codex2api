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
// allowedEmails 非空时只允许这些邮箱调用（codex2api 没有用户表，用它替代 sub2api 的
// “按邮箱查用户并校验管理员”；为空表示不限制，能签名即可）。
func (h *Handler) SetInternalIdentity(enabled bool, secret string, maxSkew time.Duration, allowedEmails []string) {
	if h == nil {
		return
	}
	h.internalIdentityEnabled = enabled
	h.internalIdentitySecret = strings.TrimSpace(secret)
	if maxSkew <= 0 {
		maxSkew = internalIdentityDefaultMaxSkew
	}
	h.internalIdentityMaxSkew = maxSkew
	if len(allowedEmails) == 0 {
		h.internalIdentityAllowedEmail = nil
		return
	}
	allowed := make(map[string]struct{}, len(allowedEmails))
	for _, email := range allowedEmails {
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			allowed[email] = struct{}{}
		}
	}
	h.internalIdentityAllowedEmail = allowed
}

func (h *Handler) registerConversationArchiveRoutes(api *gin.RouterGroup) {
	api.GET("/conversation-archives", h.ListConversationArchives)
	api.GET("/conversation-archives/stats", h.ConversationArchiveStats)
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
	internal.GET("/stats", h.ConversationArchiveStats)
	internal.GET("/:id", h.GetConversationArchive)

	// tools 的 token 用量统计走的另一个前缀。
	usage := r.Group("/api/v1/internal/usage")
	usage.Use(h.internalIdentityAuth())
	usage.GET("/stats", h.InternalUsageStats)
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
		if len(h.internalIdentityAllowedEmail) > 0 {
			if _, ok := h.internalIdentityAllowedEmail[strings.ToLower(email)]; !ok {
				security.SecurityAuditLog("INTERNAL_IDENTITY_REJECTED", "reason=email_not_allowed email="+email+" path="+c.Request.URL.Path)
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Internal identity is not allowed"})
				return
			}
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
	filter.From = parseArchiveTime(firstNonEmpty(query.Get("from"), query.Get("start_time")))
	filter.To = parseArchiveTime(firstNonEmpty(query.Get("to"), query.Get("end_time")))

	// 兼容 tools 的 sub2api 查询契约（AIC_SUB2API_* 那套参数名）。
	// codex2api 没有用户体系：user_id → api_key_id，username → 归属邮箱模糊匹配。
	if value := strings.TrimSpace(query.Get("user_id")); value != "" {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			filter.APIKeyID = id
		}
	}
	if value := strings.TrimSpace(query.Get("username")); value != "" && filter.OwnerEmail == "" {
		filter.OwnerEmailLike = value
	}
	if value := strings.TrimSpace(query.Get("session_key")); value != "" && filter.SessionID == "" {
		filter.SessionID = value
	}
	if value := strings.TrimSpace(query.Get("api_key_id")); value != "" {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			filter.APIKeyID = id
		}
	}
	switch strings.ToLower(strings.TrimSpace(query.Get("protocol"))) {
	case "openai_chat", "chat":
		filter.EndpointIn = []string{"/v1/chat/completions", "/chat/completions"}
	case "openai_responses", "responses":
		filter.EndpointIn = []string{"/v1/responses", "/responses"}
	case "anthropic_messages", "anthropic":
		filter.EndpointIn = []string{"/v1/messages", "/messages"}
	case "gemini":
		filter.EndpointLike = "/v1beta/models/"
	}
	// page/page_size 与 offset/limit 的对应关系。
	if filter.Limit == 0 {
		if size, err := strconv.Atoi(strings.TrimSpace(query.Get("page_size"))); err == nil && size > 0 {
			filter.Limit = size
		}
	}
	if filter.Offset == 0 {
		if page, err := strconv.Atoi(strings.TrimSpace(query.Get("page"))); err == nil && page > 1 && filter.Limit > 0 {
			filter.Offset = (page - 1) * filter.Limit
		}
	}
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

// ConversationArchiveStats 按维度聚合存档的 token 用量。
// 支持 group_by=none|user|date|model|protocol|api_key。
func (h *Handler) ConversationArchiveStats(c *gin.Context) {
	filter := parseConversationArchiveFilter(c)
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	stats, err := h.db.ConversationArchiveStats(c.Request.Context(), filter,
		firstNonEmpty(c.Query("group_by"), "none"), limit, c.Query("order"))
	if err != nil {
		writeInternalError(c, err)
		return
	}
	security.SecurityAuditLog("ARCHIVE_STATS", "path="+c.Request.URL.Path+" group_by="+stats.GroupBy)
	c.JSON(http.StatusOK, stats)
}

// InternalUsageStats 返回 usage_logs 的用量汇总，供 tools 的 token 统计卡片使用。
// group_by 复用 tools 的取值：none|user|date|model|protocol|group|api_key。
// codex2api 没有用户/分组维度：user 与 api_key 都映射到 API Key 维度，date 暂不支持。
func (h *Handler) InternalUsageStats(c *gin.Context) {
	from := parseArchiveTime(firstNonEmpty(c.Query("from"), c.Query("start_time")))
	to := parseArchiveTime(firstNonEmpty(c.Query("to"), c.Query("end_time")))
	stats, err := h.db.GetUsageStatsFiltered(c.Request.Context(), from, to, "", database.UsageLogFilter{}, true)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	groupBy := strings.ToLower(strings.TrimSpace(firstNonEmpty(c.Query("group_by"), "none")))
	type item struct {
		Key          string  `json:"key"`
		Label        string  `json:"label,omitempty"`
		Requests     int64   `json:"requests"`
		Tokens       int64   `json:"tokens"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		UserBilled   float64 `json:"user_billed"`
	}
	items := []item{}
	switch groupBy {
	case "model":
		for _, row := range stats.ModelStats {
			items = append(items, item{Key: row.Model, Requests: row.Requests, Tokens: row.Tokens,
				InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, UserBilled: row.UserBilled})
		}
	case "protocol", "endpoint":
		for _, row := range stats.EndpointStats {
			items = append(items, item{Key: row.Endpoint, Requests: row.Requests, Tokens: row.Tokens, UserBilled: row.UserBilled})
		}
	case "user", "api_key", "group":
		for _, row := range stats.APIKeyStats {
			items = append(items, item{Key: strconv.FormatInt(row.APIKeyID, 10), Label: row.Label,
				Requests: row.Requests, Tokens: row.Tokens, UserBilled: row.UserBilled})
		}
	case "date":
		// 直方图维度暂未实现，返回空 items，调用方回退到总计。
	default:
		groupBy = "none"
	}
	security.SecurityAuditLog("INTERNAL_USAGE_STATS", "path="+c.Request.URL.Path+" group_by="+groupBy)
	c.JSON(http.StatusOK, gin.H{
		"group_by":       groupBy,
		"total_requests": stats.TotalRequests,
		"total_tokens":   stats.TotalTokens,
		"input_tokens":   stats.TotalPrompt,
		"output_tokens":  stats.TotalCompletion,
		"user_billed":    stats.TotalUserBilled,
		"account_billed": stats.TotalAccountBilled,
		"items":          items,
	})
}
