package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	conversationWorkQueueSize     = 4096
	conversationWorkQueueMaxBytes = 64 * 1024 * 1024
	conversationLinkTTL           = 24 * time.Hour
	conversationLinkMaxEntries    = 50000
	conversationLookupTimeout     = 3 * time.Second
	conversationWriteTimeout      = 15 * time.Second
	conversationCloseDrainTimeout = 15 * time.Second
)

type conversationRecordStore interface {
	SaveConversationRecord(context.Context, database.ConversationRecordInput) error
	FindConversationByResponseID(context.Context, int64, string) (*database.ConversationRecord, error)
	FindLatestPendingConversation(context.Context, int64, string, string) (*database.ConversationRecord, error)
}

// conversationRecorder keeps all database work off the request path. Request
// handlers only append already-parsed text and enqueue turn snapshots.
type conversationRecorder struct {
	store conversationRecordStore

	workCh chan conversationTurnResult
	stopCh chan struct{}
	close  sync.Mutex
	closed bool
	wg     sync.WaitGroup

	byResponse     map[string]*conversationCachedLink
	queueByteLimit int64
	queuedBytes    atomic.Int64
	dropLogAt      atomic.Int64
}

type conversationCachedLink struct {
	sessionID string
	expiresAt time.Time
}

type conversationInteractionState struct {
	record database.ConversationRecordInput
}

type conversationTurn struct {
	recorder *conversationRecorder

	requestID          string
	startedAt          time.Time
	apiKeyID           int64
	apiKeyName         string
	clientID           string
	clientIP           string
	endpoint           string
	model              string
	explicitSessionID  string
	previousResponseID string
	userMessage        string

	assistant          strings.Builder
	fallbackAssistant  string
	finalAssistant     string
	nonFinalAssistant  bool
	responseID         string
	terminal           bool
	failed             bool
	incomplete         bool
	awaitingToolOutput bool
	finished           bool
}

type conversationTurnResult struct {
	RequestID          string
	StartedAt          time.Time
	APIKeyID           int64
	APIKeyName         string
	ClientID           string
	ClientIP           string
	Endpoint           string
	Model              string
	ExplicitSessionID  string
	PreviousResponseID string
	UserMessage        string
	AssistantMessage   string
	ResponseID         string
	Terminal           bool
	Failed             bool
	Incomplete         bool
	AwaitingToolOutput bool
	StatusCode         int
	InputTokens        int
	OutputTokens       int
	DurationMs         int
	RequestErr         error
	queuedBytes        int64
}

func newConversationRecorder(store conversationRecordStore) *conversationRecorder {
	if store == nil {
		return nil
	}
	r := &conversationRecorder{
		store:          store,
		workCh:         make(chan conversationTurnResult, conversationWorkQueueSize),
		stopCh:         make(chan struct{}),
		byResponse:     make(map[string]*conversationCachedLink),
		queueByteLimit: conversationWorkQueueMaxBytes,
	}
	r.wg.Add(1)
	go r.run()
	return r
}

func (r *conversationRecorder) Close() {
	if r == nil {
		return
	}
	r.close.Lock()
	if !r.closed {
		r.closed = true
		close(r.stopCh)
	}
	r.close.Unlock()
	r.wg.Wait()
}

func (r *conversationRecorder) run() {
	defer r.wg.Done()
	for {
		// Prioritize shutdown before taking another queued item so a full queue
		// cannot postpone the bounded drain phase indefinitely.
		select {
		case <-r.stopCh:
			r.drainPending()
			return
		default:
		}
		select {
		case result := <-r.workCh:
			r.processTurn(context.Background(), result)
		case <-r.stopCh:
			r.drainPending()
			return
		}
	}
}

func (r *conversationRecorder) drainPending() {
	drainCtx, cancel := context.WithTimeout(context.Background(), conversationCloseDrainTimeout)
	defer cancel()
	for {
		if drainCtx.Err() != nil {
			r.discardPending()
			return
		}
		select {
		case result := <-r.workCh:
			r.processTurn(drainCtx, result)
		default:
			return
		}
	}
}

func (r *conversationRecorder) processTurn(ctx context.Context, result conversationTurnResult) {
	defer r.queuedBytes.Add(-result.queuedBytes)
	r.persistTurn(ctx, result)
}

func (r *conversationRecorder) discardPending() {
	dropped := 0
	for {
		select {
		case result := <-r.workCh:
			r.queuedBytes.Add(-result.queuedBytes)
			dropped++
		default:
			if dropped > 0 {
				log.Printf("[会话记录] 关闭排空超时，已跳过 %d 条待写记录", dropped)
			}
			return
		}
	}
}

func (r *conversationRecorder) enqueue(result conversationTurnResult) bool {
	if r == nil {
		return false
	}
	result.queuedBytes = conversationResultPayloadBytes(result)
	if !r.reserveQueueBytes(result.queuedBytes) {
		r.logQueueDrop()
		return false
	}
	r.close.Lock()
	defer r.close.Unlock()
	if r.closed {
		r.queuedBytes.Add(-result.queuedBytes)
		return false
	}
	select {
	case r.workCh <- result:
		return true
	default:
		r.queuedBytes.Add(-result.queuedBytes)
		r.logQueueDrop()
		return false
	}
}

func (r *conversationRecorder) reserveQueueBytes(size int64) bool {
	if size <= 0 {
		size = 1
	}
	limit := r.queueByteLimit
	if limit <= 0 {
		limit = conversationWorkQueueMaxBytes
	}
	for {
		current := r.queuedBytes.Load()
		if size > limit-current {
			return false
		}
		if r.queuedBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func conversationResultPayloadBytes(result conversationTurnResult) int64 {
	size := len(result.UserMessage) + len(result.AssistantMessage)
	if size == 0 {
		return 1
	}
	return int64(size)
}

func (r *conversationRecorder) logQueueDrop() {
	now := time.Now().Unix()
	for {
		previous := r.dropLogAt.Load()
		if now-previous < 60 {
			return
		}
		if r.dropLogAt.CompareAndSwap(previous, now) {
			log.Printf("[会话记录] 异步队列已满，本次记录已跳过，不影响 API 请求")
			return
		}
	}
}

func (h *Handler) beginConversationTurn(c *gin.Context, requestBody []byte) *conversationTurn {
	if h == nil || h.convRecorder == nil || c == nil || c.Request == nil {
		return nil
	}
	row := apiKeyRowFromContext(c)
	clientID, _ := resolveAPIKeyClientID(c, row)
	apiKeyName := ""
	if row != nil {
		apiKeyName = strings.TrimSpace(row.Name)
	}
	turn := &conversationTurn{
		recorder:           h.convRecorder,
		startedAt:          time.Now().UTC(),
		apiKeyID:           requestAPIKeyID(c),
		apiKeyName:         apiKeyName,
		clientID:           normalizeConversationIdentifier(clientID, 128, "client"),
		clientIP:           normalizeConversationIdentifier(c.ClientIP(), 64, "ip"),
		endpoint:           strings.TrimSpace(c.Request.URL.Path),
		model:              strings.TrimSpace(gjson.GetBytes(requestBody, "model").String()),
		explicitSessionID:  resolveConversationSessionIDFromHeaders(c.Request.Header, requestBody),
		previousResponseID: normalizeConversationIdentifier(gjson.GetBytes(requestBody, "previous_response_id").String(), 255, "resp"),
		userMessage:        extractCurrentConversationUserMessage(requestBody),
	}
	if turn.userMessage != "" {
		turn.requestID = uuid.NewString()
		turn.recorder.enqueue(conversationTurnResult{
			RequestID:          turn.requestID,
			StartedAt:          turn.startedAt,
			APIKeyID:           turn.apiKeyID,
			APIKeyName:         turn.apiKeyName,
			ClientID:           turn.clientID,
			ClientIP:           turn.clientIP,
			Endpoint:           turn.endpoint,
			Model:              turn.model,
			ExplicitSessionID:  turn.explicitSessionID,
			PreviousResponseID: turn.previousResponseID,
			UserMessage:        turn.userMessage,
		})
	}
	return turn
}

// observeResponsesEvent reuses the gjson value already parsed by the proxy's
// streaming loop. It does not parse or copy the complete SSE frame again.
func (t *conversationTurn) observeResponsesEvent(eventType string, parsed gjson.Result) {
	if t == nil {
		return
	}
	switch eventType {
	case "response.created", "response.in_progress":
		t.setResponseID(parsed.Get("response.id").String())
	case "response.output_text.delta":
		t.assistant.WriteString(parsed.Get("delta").String())
	case "response.output_text.done":
		if t.assistant.Len() == 0 {
			t.fallbackAssistant = appendConversationText(t.fallbackAssistant, parsed.Get("text").String())
		}
	case "response.output_item.done":
		item := parsed.Get("item")
		if isConversationToolCallType(item.Get("type").String()) {
			t.awaitingToolOutput = true
		}
		if isConversationNonFinalAssistantItem(item) {
			t.nonFinalAssistant = true
		}
		if text := visibleResponseItemText(item); text != "" {
			t.finalAssistant = appendConversationText(t.finalAssistant, text)
		}
		if t.assistant.Len() == 0 {
			t.fallbackAssistant = appendConversationText(t.fallbackAssistant, visibleResponseItemText(item))
		}
	case "response.completed":
		response := parsed.Get("response")
		t.terminal = true
		t.failed = false
		t.incomplete = strings.EqualFold(response.Get("status").String(), "incomplete")
		t.setResponseID(response.Get("id").String())
		if text := visibleResponseOutputText(response.Get("output")); text != "" {
			t.finalAssistant = text
		}
		t.nonFinalAssistant = t.nonFinalAssistant || responseOutputHasNonFinalAssistant(response.Get("output"))
		t.awaitingToolOutput = responseOutputAwaitsToolOutput(response.Get("output"))
	case "response.incomplete":
		response := parsed.Get("response")
		t.terminal = true
		t.incomplete = true
		t.setResponseID(response.Get("id").String())
		if text := visibleResponseOutputText(response.Get("output")); text != "" {
			t.finalAssistant = text
		}
		t.nonFinalAssistant = t.nonFinalAssistant || responseOutputHasNonFinalAssistant(response.Get("output"))
	case "response.failed", "error":
		t.terminal = true
		t.failed = true
		t.setResponseID(parsed.Get("response.id").String())
	}
}

func (t *conversationTurn) observeResponsesObject(response gjson.Result) {
	if t == nil || !response.Exists() {
		return
	}
	t.terminal = true
	t.failed = strings.EqualFold(response.Get("status").String(), "failed")
	t.incomplete = strings.EqualFold(response.Get("status").String(), "incomplete")
	t.setResponseID(response.Get("id").String())
	if text := visibleResponseOutputText(response.Get("output")); text != "" {
		t.finalAssistant = text
	}
	t.nonFinalAssistant = t.nonFinalAssistant || responseOutputHasNonFinalAssistant(response.Get("output"))
	t.awaitingToolOutput = responseOutputAwaitsToolOutput(response.Get("output"))
}

func (t *conversationTurn) setResponseID(responseID string) {
	if responseID = normalizeConversationIdentifier(responseID, 255, "resp"); responseID != "" {
		t.responseID = responseID
	}
}

// finish only enqueues data. Session linking and all SQL run in the recorder's
// background worker, so conversation recording cannot add database latency to
// the API response path.
func (t *conversationTurn) finish(input *database.UsageLogInput, requestErr error) {
	if t == nil || t.recorder == nil || input == nil || t.finished {
		return
	}
	t.finished = true
	assistant := t.finalAssistant
	if strings.TrimSpace(assistant) == "" && !t.nonFinalAssistant {
		assistant = t.assistant.String()
	}
	if strings.TrimSpace(assistant) == "" && !t.nonFinalAssistant {
		assistant = t.fallbackAssistant
	}
	if !t.terminal || t.failed || t.incomplete || t.awaitingToolOutput {
		assistant = ""
	}
	if t.userMessage == "" && assistant == "" && t.previousResponseID == "" && t.responseID == "" {
		return
	}
	durationMs := input.DurationMs
	if durationMs <= 0 {
		durationMs = int(time.Since(t.startedAt).Milliseconds())
	}
	endpoint := strings.TrimSpace(input.InboundEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(input.Endpoint)
	}
	if endpoint == "" {
		endpoint = t.endpoint
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = t.model
	}
	t.recorder.enqueue(conversationTurnResult{
		RequestID:          t.requestID,
		StartedAt:          t.startedAt,
		APIKeyID:           t.apiKeyID,
		APIKeyName:         t.apiKeyName,
		ClientID:           t.clientID,
		ClientIP:           t.clientIP,
		Endpoint:           endpoint,
		Model:              model,
		ExplicitSessionID:  t.explicitSessionID,
		PreviousResponseID: t.previousResponseID,
		UserMessage:        t.userMessage,
		AssistantMessage:   assistant,
		ResponseID:         t.responseID,
		Terminal:           t.terminal,
		Failed:             t.failed,
		Incomplete:         t.incomplete,
		AwaitingToolOutput: t.awaitingToolOutput,
		StatusCode:         input.StatusCode,
		InputTokens:        input.InputTokens,
		OutputTokens:       input.OutputTokens,
		DurationMs:         durationMs,
		RequestErr:         requestErr,
	})
}

// finishFallback records valid requests that leave the handler before the
// normal usage-log completion path, such as account exhaustion or a transport
// failure before the first upstream event. A normal finish sets t.finished and
// makes this method a no-op.
func (t *conversationTurn) finishFallback(c *gin.Context) {
	if t == nil || t.finished || c == nil {
		return
	}
	statusCode := c.Writer.Status()
	if statusCode <= 0 {
		statusCode = http.StatusInternalServerError
	}
	var requestErr error
	if c.Request != nil {
		requestErr = c.Request.Context().Err()
	}
	t.finish(&database.UsageLogInput{
		Endpoint:        t.endpoint,
		InboundEndpoint: t.endpoint,
		Model:           t.model,
		StatusCode:      statusCode,
		DurationMs:      int(time.Since(t.startedAt).Milliseconds()),
	}, requestErr)
}

func (r *conversationRecorder) persistTurn(parent context.Context, result conversationTurnResult) {
	if parent == nil {
		parent = context.Background()
	}
	result.PreviousResponseID = normalizeConversationIdentifier(result.PreviousResponseID, 255, "resp")
	result.ResponseID = normalizeConversationIdentifier(result.ResponseID, 255, "resp")
	if !result.Terminal || result.Failed || result.Incomplete || result.AwaitingToolOutput {
		result.AssistantMessage = ""
	}

	cachedLink := r.cachedByResponse(result.APIKeyID, result.PreviousResponseID)
	var linkedState *conversationInteractionState
	if result.PreviousResponseID != "" && (cachedLink == nil || result.UserMessage == "") {
		ctx, cancel := context.WithTimeout(parent, conversationLookupTimeout)
		record, err := r.store.FindConversationByResponseID(ctx, result.APIKeyID, result.PreviousResponseID)
		cancel()
		if err != nil {
			log.Printf("[会话记录] 查询响应链失败: %v", err)
		} else if record != nil {
			cachedLink = &conversationCachedLink{
				sessionID: record.SessionID,
				expiresAt: time.Now().Add(conversationLinkTTL),
			}
			if result.UserMessage == "" && record.Status == database.ConversationStatusPartial {
				linkedState = conversationStateFromRecord(record)
			}
		}
	}

	sessionID := result.ExplicitSessionID
	sessionKnown := sessionID != ""
	if cachedLink != nil {
		sessionID = cachedLink.sessionID
		sessionKnown = true
	}
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	sessionID = normalizeConversationIdentifier(sessionID, 255, "session")

	if linkedState == nil && result.UserMessage == "" && sessionKnown {
		ctx, cancel := context.WithTimeout(parent, conversationLookupTimeout)
		record, err := r.store.FindLatestPendingConversation(ctx, result.APIKeyID, sessionID, result.ClientID)
		cancel()
		if err != nil {
			log.Printf("[会话记录] 查询待续会话失败: %v", err)
		} else if record != nil {
			linkedState = conversationStateFromRecord(record)
		}
	}

	if result.UserMessage == "" && linkedState == nil {
		return
	}

	var state *conversationInteractionState
	if result.UserMessage != "" || linkedState == nil {
		requestID := normalizeConversationIdentifier(result.RequestID, 64, "request")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		state = &conversationInteractionState{record: database.ConversationRecordInput{
			RequestID:   requestID,
			SessionID:   sessionID,
			APIKeyID:    result.APIKeyID,
			APIKeyName:  result.APIKeyName,
			ClientID:    result.ClientID,
			ClientIP:    result.ClientIP,
			Endpoint:    result.Endpoint,
			Model:       result.Model,
			UserMessage: result.UserMessage,
			CreatedAt:   result.StartedAt,
		}}
	} else {
		state = linkedState
	}

	now := time.Now().UTC()
	record := &state.record
	if record.APIKeyName == "" {
		record.APIKeyName = result.APIKeyName
	}
	if record.ClientID == "" {
		record.ClientID = result.ClientID
	}
	if record.ClientIP == "" {
		record.ClientIP = result.ClientIP
	}
	if record.Endpoint == "" {
		record.Endpoint = result.Endpoint
	}
	if result.Model != "" {
		record.Model = result.Model
	}
	if result.UserMessage != "" {
		record.UserMessage = result.UserMessage
	}
	record.AssistantMessage = appendConversationText(record.AssistantMessage, result.AssistantMessage)
	record.PreviousResponseID = result.PreviousResponseID
	if result.ResponseID != "" {
		record.ResponseID = result.ResponseID
	}
	record.InputTokens += result.InputTokens
	record.OutputTokens += result.OutputTokens
	record.DurationMs += result.DurationMs
	record.StatusCode = result.StatusCode
	record.Status = conversationTurnStatus(result)
	record.UpdatedAt = now
	if record.Status == database.ConversationStatusPartial {
		record.CompletedAt = nil
	} else {
		completedAt := now
		record.CompletedAt = &completedAt
	}

	r.saveRecord(parent, *record)
	r.cacheState(state, result.PreviousResponseID, result.ResponseID)
}

func (r *conversationRecorder) saveRecord(parent context.Context, input database.ConversationRecordInput) bool {
	ctx, cancel := context.WithTimeout(parent, conversationWriteTimeout)
	defer cancel()
	var err error
	attempts := 0
retryLoop:
	for attempt := 0; attempt < 3; attempt++ {
		attempts++
		err = r.store.SaveConversationRecord(ctx, input)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			err = ctx.Err()
			break retryLoop
		case <-timer.C:
		}
	}
	log.Printf("[会话记录] 写入失败（已尝试%d次）: request_id=%s err=%v", attempts, input.RequestID, err)
	return false
}

func conversationTurnStatus(result conversationTurnResult) string {
	if result.RequestErr != nil {
		if result.AssistantMessage != "" {
			return database.ConversationStatusPartial
		}
		if errors.Is(result.RequestErr, context.Canceled) {
			return database.ConversationStatusCanceled
		}
		if errors.Is(result.RequestErr, context.DeadlineExceeded) {
			return database.ConversationStatusIncomplete
		}
		return database.ConversationStatusFailed
	}
	if result.Failed || result.StatusCode >= http.StatusBadRequest {
		if result.AssistantMessage != "" {
			return database.ConversationStatusPartial
		}
		return database.ConversationStatusFailed
	}
	if result.Incomplete {
		return database.ConversationStatusIncomplete
	}
	if result.AwaitingToolOutput || !result.Terminal || strings.TrimSpace(result.AssistantMessage) == "" {
		return database.ConversationStatusPartial
	}
	return database.ConversationStatusCompleted
}

func conversationStateFromRecord(record *database.ConversationRecord) *conversationInteractionState {
	if record == nil {
		return nil
	}
	return &conversationInteractionState{record: database.ConversationRecordInput{
		RequestID:          record.RequestID,
		SessionID:          record.SessionID,
		APIKeyID:           record.APIKeyID,
		APIKeyName:         record.APIKeyName,
		ClientID:           record.ClientID,
		ClientIP:           record.ClientIP,
		ResponseID:         record.ResponseID,
		PreviousResponseID: record.PreviousResponseID,
		Endpoint:           record.Endpoint,
		Model:              record.Model,
		UserMessage:        record.UserMessage,
		AssistantMessage:   record.AssistantMessage,
		Status:             record.Status,
		StatusCode:         record.StatusCode,
		InputTokens:        record.InputTokens,
		OutputTokens:       record.OutputTokens,
		DurationMs:         record.DurationMs,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		CompletedAt:        record.CompletedAt,
	}}
}

func (r *conversationRecorder) cachedByResponse(apiKeyID int64, responseID string) *conversationCachedLink {
	if responseID == "" {
		return nil
	}
	key := conversationResponseCacheKey(apiKeyID, responseID)
	link, ok := r.byResponse[key]
	if !ok || link == nil || time.Now().After(link.expiresAt) {
		delete(r.byResponse, key)
		return nil
	}
	return link
}

func (r *conversationRecorder) cacheState(state *conversationInteractionState, responseIDs ...string) {
	expiresAt := time.Now().Add(conversationLinkTTL)
	link := &conversationCachedLink{
		sessionID: state.record.SessionID,
		expiresAt: expiresAt,
	}
	for _, responseID := range responseIDs {
		if responseID = strings.TrimSpace(responseID); responseID != "" {
			r.byResponse[conversationResponseCacheKey(state.record.APIKeyID, responseID)] = link
		}
	}
	r.pruneCache()
}

func (r *conversationRecorder) pruneCache() {
	if len(r.byResponse) <= conversationLinkMaxEntries {
		return
	}
	now := time.Now()
	for key, link := range r.byResponse {
		if now.After(link.expiresAt) {
			delete(r.byResponse, key)
		}
	}
	for len(r.byResponse) > conversationLinkMaxEntries {
		for key := range r.byResponse {
			delete(r.byResponse, key)
			break
		}
	}
}

func conversationResponseCacheKey(apiKeyID int64, responseID string) string {
	return strings.Join([]string{strconv.FormatInt(apiKeyID, 10), responseID}, "\x00")
}

func normalizeConversationIdentifier(value string, maxLen int, prefix string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	ascii := len(value) <= maxLen
	for i := 0; ascii && i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			ascii = false
		}
	}
	if ascii {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func resolveConversationSessionID(body []byte) string {
	for _, value := range []string{
		gjson.GetBytes(body, "session_id").String(),
		gjson.GetBytes(body, "conversation_id").String(),
		gjson.GetBytes(body, "conversation.id").String(),
		gjson.GetBytes(body, "conversation").String(),
		gjson.GetBytes(body, "prompt_cache_key").String(),
	} {
		if normalized := normalizeConversationIdentifier(value, 255, "session"); normalized != "" {
			return normalized
		}
	}
	return ""
}

func resolveConversationSessionIDFromHeaders(headers http.Header, body []byte) string {
	if headers != nil {
		for _, name := range []string{"Session_id", "Conversation_id"} {
			if normalized := normalizeConversationIdentifier(headers.Get(name), 255, "session"); normalized != "" {
				return normalized
			}
		}
	}
	if explicit := resolveConversationSessionID(body); explicit != "" {
		return explicit
	}
	if contentSeed := normalizeConversationIdentifier(deriveContentSessionSeed(body), 255, "session"); contentSeed != "" {
		return contentSeed
	}
	if headers != nil {
		if affinity := resolveDownstreamAffinityID(headers); affinity != "" {
			return normalizeConversationIdentifier(affinity, 255, "session")
		}
		return normalizeConversationIdentifier(headers.Get("Idempotency-Key"), 255, "session")
	}
	return ""
}

func extractCurrentConversationUserMessage(body []byte) string {
	if !gjson.ValidBytes(body) {
		return ""
	}
	root := gjson.ParseBytes(body)
	if messages := root.Get("messages"); messages.IsArray() {
		return latestCurrentUserMessage(messages.Array())
	}
	input := root.Get("input")
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String())
	}
	if input.IsArray() {
		return latestCurrentUserMessage(input.Array())
	}
	if input.IsObject() && strings.EqualFold(input.Get("role").String(), "user") {
		return conversationContentText(input.Get("content"))
	}
	return strings.TrimSpace(root.Get("prompt").String())
}

func latestCurrentUserMessage(items []gjson.Result) string {
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if role == "assistant" || role == "tool" || isToolContinuationType(itemType) {
			return ""
		}
		if role == "user" {
			return conversationContentText(item.Get("content"))
		}
		if itemType == "input_text" {
			return strings.TrimSpace(item.Get("text").String())
		}
	}
	return ""
}

func isToolContinuationType(itemType string) bool {
	return strings.HasSuffix(itemType, "_call_output") ||
		itemType == "function_call_output" ||
		itemType == "computer_call_output" ||
		itemType == "local_shell_call_output" ||
		itemType == "mcp_tool_call_output"
}

func conversationContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return strings.TrimSpace(content.Get("text").String())
	}
	parts := make([]string, 0, len(content.Array()))
	for _, part := range content.Array() {
		if part.Type == gjson.String {
			if text := strings.TrimSpace(part.String()); text != "" {
				parts = append(parts, text)
			}
			continue
		}
		partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
		if partType != "" && partType != "input_text" && partType != "text" {
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func appendConversationText(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return existing
	}
	return existing + "\n\n" + next
}

func isConversationToolCallType(itemType string) bool {
	itemType = strings.ToLower(strings.TrimSpace(itemType))
	return strings.Contains(itemType, "call") && !strings.HasSuffix(itemType, "_output")
}

func responseOutputAwaitsToolOutput(output gjson.Result) bool {
	items := output.Array()
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if itemType == "" || itemType == "reasoning" || isConversationCommentaryItem(item) {
			continue
		}
		if isConversationToolCallType(itemType) {
			return true
		}
		if itemType == "message" || strings.EqualFold(item.Get("role").String(), "assistant") {
			return false
		}
	}
	return false
}

func visibleResponseOutputText(output gjson.Result) string {
	parts := make([]string, 0, 2)
	for _, item := range output.Array() {
		if text := visibleResponseItemText(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func visibleResponseItemText(item gjson.Result) string {
	if item.Get("type").String() != "message" && item.Get("role").String() != "assistant" {
		return ""
	}
	phase := strings.ToLower(strings.TrimSpace(item.Get("phase").String()))
	if phase != "" && phase != "final_answer" && phase != "final" {
		return ""
	}
	parts := make([]string, 0, 2)
	for _, content := range item.Get("content").Array() {
		contentType := strings.ToLower(content.Get("type").String())
		if contentType != "output_text" && contentType != "text" {
			continue
		}
		if text := strings.TrimSpace(content.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func responseOutputHasNonFinalAssistant(output gjson.Result) bool {
	for _, item := range output.Array() {
		if isConversationNonFinalAssistantItem(item) {
			return true
		}
	}
	return false
}

func isConversationNonFinalAssistantItem(item gjson.Result) bool {
	if item.Get("type").String() != "message" && !strings.EqualFold(item.Get("role").String(), "assistant") {
		return false
	}
	phase := strings.ToLower(strings.TrimSpace(item.Get("phase").String()))
	return phase != "" && phase != "final_answer" && phase != "final"
}

func isConversationCommentaryItem(item gjson.Result) bool {
	phase := strings.ToLower(strings.TrimSpace(item.Get("phase").String()))
	return phase == "commentary" || phase == "analysis" || phase == "reasoning"
}
