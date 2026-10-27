package proxy

// 会话存档采集：在网关层把用户消息与 AI 回复的纯文本归档到 conversation_records。
//
// 设计要点（与 sub2api 的 conversation_archives 对齐）：
//   - 只在 POST 的文本推理端点生效，图片/视频/实时通道不采
//   - 请求体来自 BodyCacheMiddleware 的缓存，不额外读流
//   - 响应体用 tee writer 旁路复制，仍然逐块写给客户端；超过上限只保留头部并标记 truncated
//   - 只保存文本：图片、附件、工具调用、思考内容全部丢弃
//   - 异步落库，队列满直接丢弃（绝不阻塞请求），丢弃计数写日志
//   - 不写归属人：按人查询由 api_keys.owner_email 在读取时 join，历史数据无需回填

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/codex2api/api"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	archiveMaxRequestBytes  = 1 << 20  // 1 MiB
	archiveMaxResponseBytes = 4 << 20  // 4 MiB
	archiveMaxTextRunes     = 64 << 10 // 64K 字符
	archiveQueueSize        = 1024
	archivePersistTimeout   = 15 * time.Second
)

// conversationArchiver 负责异步落库。
type conversationArchiver struct {
	db      *database.DB
	jobs    chan database.ConversationRecordInput
	done    chan struct{}
	once    sync.Once
	dropped atomic.Int64
}

func newConversationArchiver(db *database.DB) *conversationArchiver {
	if db == nil {
		return nil
	}
	a := &conversationArchiver{db: db, jobs: make(chan database.ConversationRecordInput, archiveQueueSize), done: make(chan struct{})}
	go a.run()
	return a
}

func (a *conversationArchiver) Close() {
	if a == nil {
		return
	}
	a.once.Do(func() {
		close(a.jobs)
		<-a.done
	})
}

func (a *conversationArchiver) run() {
	defer close(a.done)
	for job := range a.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), archivePersistTimeout)
		if err := a.db.SaveConversationRecord(ctx, job); err != nil {
			log.Printf("[conversation-archive] 保存失败 request_id=%s: %v", job.RequestID, err)
		}
		cancel()
	}
}

func (a *conversationArchiver) enqueue(job database.ConversationRecordInput) {
	select {
	case a.jobs <- job:
	default:
		if n := a.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Printf("[conversation-archive] 队列已满，累计丢弃 %d 条", n)
		}
	}
}

// archiveResponseWriter 把写给客户端的字节旁路复制一份（有上限）。
type archiveResponseWriter struct {
	gin.ResponseWriter
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *archiveResponseWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *archiveResponseWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *archiveResponseWriter) capture(data []byte) {
	if w.truncated {
		return
	}
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return
	}
	if len(data) > remaining {
		w.buf.Write(data[:remaining])
		w.truncated = true
		return
	}
	w.buf.Write(data)
}

// ConversationArchiveMiddleware 归档 /v1 文本端点的请求与回复。
// 未启用（archiver 为 nil）时是零开销的直通中间件。
func (h *Handler) ConversationArchiveMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		archiver := h.convArchiver
		if archiver == nil || c.Request == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		protocol := archiveProtocol(path)
		if protocol == "" {
			c.Next()
			return
		}
		body := api.GetRawBody(c)
		if len(body) == 0 {
			c.Next()
			return
		}
		requestText, requestTruncated := archiveRequestText(body, protocol)
		if len(body) > archiveMaxRequestBytes {
			requestTruncated = true
		}
		started := time.Now().UTC()
		writer := &archiveResponseWriter{ResponseWriter: c.Writer, limit: archiveMaxResponseBytes}
		c.Writer = writer

		c.Next()

		// 鉴权失败的请求不归档：内容没有归属人，存下来既没有分析价值，
		// 也会把未认证的原文写进库。
		if c.Writer.Status() == http.StatusUnauthorized || c.Writer.Status() == http.StatusForbidden {
			return
		}

		if writer.buf.Len() == 0 && requestText == "" {
			return
		}
		contentType := ""
		if c.Writer != nil && c.Writer.Header() != nil {
			contentType = c.Writer.Header().Get("Content-Type")
		}
		response := archiveResponseText(writer.buf.Bytes(), contentType, protocol, writer.truncated)

		row := apiKeyRowFromContext(c)
		apiKeyID := requestAPIKeyID(c)
		apiKeyName := ""
		if row != nil {
			apiKeyName = strings.TrimSpace(row.Name)
		}
		if requestText == "" && response.text == "" {
			return
		}
		sessionID := strings.TrimSpace(resolveConversationSessionIDFromHeaders(c.Request.Header, body))
		if sessionID == "" {
			sessionID = strings.TrimSpace(c.GetHeader("X-Request-ID"))
		}
		if sessionID == "" {
			sessionID = requestIDFromContext(c)
		}
		requestID := requestIDFromContext(c)
		if requestID == "" {
			requestID = sessionID
		}
		clientID, _ := resolveAPIKeyClientID(c, row)
		completedAt := time.Now().UTC()
		archiver.enqueue(database.ConversationRecordInput{
			RequestID:        requestID,
			SessionID:        sessionID,
			APIKeyID:         apiKeyID,
			APIKeyName:       apiKeyName,
			ClientID:         normalizeConversationIdentifier(clientID, 128, "client"),
			ClientIP:         normalizeConversationIdentifier(c.ClientIP(), 64, "ip"),
			Endpoint:         truncateArchiveText(path, 100),
			Model:            archiveRequestModel(body, path, protocol),
			UserMessage:      requestText,
			AssistantMessage: response.text,
			Status:           archiveStatus(c.Writer.Status()),
			StatusCode:       c.Writer.Status(),
			InputTokens:      response.inputTokens,
			OutputTokens:     response.outputTokens,
			DurationMs:       int(completedAt.Sub(started) / time.Millisecond),
			Stream:           response.stream,
			Truncated:        requestTruncated || response.truncated,
			CreatedAt:        started,
			UpdatedAt:        completedAt,
			CompletedAt:      &completedAt,
		})
	}
}

func archiveProtocol(path string) string {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return "chat"
	case strings.HasSuffix(path, "/responses"):
		return "responses"
	case strings.HasSuffix(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, "/v1beta/models/"):
		return "gemini"
	default:
		return ""
	}
}

func archiveStatus(code int) string {
	switch {
	case code == 0:
		return database.ConversationStatusIncomplete
	case code == http.StatusRequestTimeout || code == 499:
		return database.ConversationStatusCanceled
	case code >= 500:
		return database.ConversationStatusFailed
	case code >= 400:
		return database.ConversationStatusFailed
	default:
		return database.ConversationStatusCompleted
	}
}

func requestIDFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value := strings.TrimSpace(c.GetHeader("X-Request-ID")); value != "" {
		return value
	}
	return ""
}

func archiveRequestModel(body []byte, path, protocol string) string {
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		return truncateArchiveText(model, 100)
	}
	if protocol == "gemini" {
		if idx := strings.Index(path, "/v1beta/models/"); idx >= 0 {
			rest := path[idx+len("/v1beta/models/"):]
			if cut := strings.Index(rest, ":"); cut > 0 {
				return truncateArchiveText(rest[:cut], 100)
			}
			return truncateArchiveText(rest, 100)
		}
	}
	return ""
}

// archiveRequestText 只取「当前这一轮用户输入」的文本，跳过历史轮次。
func archiveRequestText(body []byte, protocol string) (string, bool) {
	if !gjson.ValidBytes(body) {
		return "", false
	}
	root := gjson.ParseBytes(body)
	var text string
	switch protocol {
	case "gemini":
		text = lastGeminiUserText(root.Get("contents"))
	default:
		text = extractCurrentConversationUserMessage(body)
	}
	text = strings.TrimSpace(text)
	clamped, truncated := clampArchiveText(text)
	return clamped, truncated
}

func lastGeminiUserText(contents gjson.Result) string {
	if !contents.IsArray() {
		return ""
	}
	items := contents.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role != "" && role != "user" {
			continue
		}
		if text := geminiPartsText(item.Get("parts")); text != "" {
			return text
		}
	}
	return ""
}

func geminiPartsText(parts gjson.Result) string {
	if !parts.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, part := range parts.Array() {
		// 只取纯文本；inlineData（图片/附件）与 functionCall 直接丢弃。
		if text := part.Get("text"); text.Type == gjson.String {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text.String())
		}
	}
	return b.String()
}

type archiveResponse struct {
	text         string
	inputTokens  int
	outputTokens int
	truncated    bool
	stream       bool
}

func archiveResponseText(raw []byte, contentType, protocol string, bodyTruncated bool) archiveResponse {
	out := archiveResponse{truncated: bodyTruncated}
	if len(raw) == 0 {
		return out
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || looksLikeSSE(raw) {
		out.stream = true
		out.text, out.inputTokens, out.outputTokens = parseArchiveSSE(raw, protocol)
	} else {
		out.text, out.inputTokens, out.outputTokens = parseArchiveJSON(raw, protocol)
	}
	out.text = strings.TrimSpace(out.text)
	clamped, truncated := clampArchiveText(out.text)
	out.text = clamped
	out.truncated = out.truncated || truncated
	return out
}

func looksLikeSSE(raw []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(raw), []byte("data:"))
}

func parseArchiveJSON(raw []byte, protocol string) (text string, inputTokens, outputTokens int) {
	if !gjson.ValidBytes(raw) {
		return "", 0, 0
	}
	root := gjson.ParseBytes(raw)
	switch protocol {
	case "chat":
		text = chatMessageText(root.Get("choices.0.message"))
		inputTokens = int(root.Get("usage.prompt_tokens").Int())
		outputTokens = int(root.Get("usage.completion_tokens").Int())
	case "responses":
		text = responsesOutputText(root.Get("output"))
		inputTokens = int(root.Get("usage.input_tokens").Int())
		outputTokens = int(root.Get("usage.output_tokens").Int())
	case "anthropic":
		text = anthropicContentText(root.Get("content"))
		inputTokens = int(root.Get("usage.input_tokens").Int())
		outputTokens = int(root.Get("usage.output_tokens").Int())
	case "gemini":
		text = geminiCandidateText(root.Get("candidates.0.content"))
		inputTokens = int(root.Get("usageMetadata.promptTokenCount").Int())
		outputTokens = int(root.Get("usageMetadata.candidatesTokenCount").Int())
	}
	return text, inputTokens, outputTokens
}

func parseArchiveSSE(raw []byte, protocol string) (text string, inputTokens, outputTokens int) {
	var b strings.Builder
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
			continue
		}
		event := gjson.ParseBytes(payload)
		switch protocol {
		case "chat":
			if delta := chatMessageText(event.Get("choices.0.delta")); delta != "" {
				b.WriteString(delta)
			}
			if usage := event.Get("usage"); usage.Exists() {
				inputTokens = int(usage.Get("prompt_tokens").Int())
				outputTokens = int(usage.Get("completion_tokens").Int())
			}
		case "responses":
			switch event.Get("type").String() {
			case "response.output_text.delta":
				b.WriteString(event.Get("delta").String())
			case "response.completed", "response.incomplete":
				if usage := event.Get("response.usage"); usage.Exists() {
					inputTokens = int(usage.Get("input_tokens").Int())
					outputTokens = int(usage.Get("output_tokens").Int())
				}
			}
		case "anthropic":
			switch event.Get("type").String() {
			case "content_block_delta":
				b.WriteString(event.Get("delta.text").String())
			case "message_start":
				inputTokens = int(event.Get("message.usage.input_tokens").Int())
			case "message_delta":
				if usage := event.Get("usage"); usage.Exists() {
					outputTokens = int(usage.Get("output_tokens").Int())
					if v := usage.Get("input_tokens"); v.Exists() {
						inputTokens = int(v.Int())
					}
				}
			}
		case "gemini":
			if chunk := geminiCandidateText(event.Get("candidates.0.content")); chunk != "" {
				b.WriteString(chunk)
			}
			if usage := event.Get("usageMetadata"); usage.Exists() {
				inputTokens = int(usage.Get("promptTokenCount").Int())
				outputTokens = int(usage.Get("candidatesTokenCount").Int())
			}
		}
	}
	return b.String(), inputTokens, outputTokens
}

func chatMessageText(node gjson.Result) string {
	if !node.Exists() {
		return ""
	}
	content := node.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var b strings.Builder
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				b.WriteString(part.Get("text").String())
			}
		}
		return b.String()
	}
	return ""
}

func responsesOutputText(output gjson.Result) string {
	if !output.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, item := range output.Array() {
		// 只要 assistant message 的文本；reasoning / function_call 不归档。
		if item.Get("type").String() != "message" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" {
				b.WriteString(part.Get("text").String())
			}
		}
	}
	return b.String()
}

func anthropicContentText(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, block := range content.Array() {
		// text 以外的块（tool_use / thinking / image）一律丢弃。
		if block.Get("type").String() == "text" {
			b.WriteString(block.Get("text").String())
		}
	}
	return b.String()
}

func geminiCandidateText(candidate gjson.Result) string {
	return geminiPartsText(candidate.Get("parts"))
}

func clampArchiveText(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if utf8.RuneCountInString(text) <= archiveMaxTextRunes {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:archiveMaxTextRunes]), true
}

func truncateArchiveText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max]
}
