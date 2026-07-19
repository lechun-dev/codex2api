package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type fakeConversationRecordStore struct {
	mu      sync.Mutex
	records map[string]database.ConversationRecordInput
	saved   []database.ConversationRecordInput
}

func newFakeConversationRecordStore() *fakeConversationRecordStore {
	return &fakeConversationRecordStore{records: make(map[string]database.ConversationRecordInput)}
}

func (s *fakeConversationRecordStore) SaveConversationRecord(_ context.Context, input database.ConversationRecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[input.RequestID] = input
	s.saved = append(s.saved, input)
	return nil
}

func (s *fakeConversationRecordStore) FindConversationByResponseID(_ context.Context, apiKeyID int64, responseID string) (*database.ConversationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, input := range s.records {
		if input.APIKeyID == apiKeyID && (input.ResponseID == responseID || input.PreviousResponseID == responseID) {
			return conversationTestRecord(input), nil
		}
	}
	return nil, nil
}

func (s *fakeConversationRecordStore) FindLatestPendingConversation(_ context.Context, apiKeyID int64, sessionID, clientID string) (*database.ConversationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.saved) - 1; index >= 0; index-- {
		input := s.saved[index]
		if input.APIKeyID == apiKeyID && input.SessionID == sessionID &&
			input.ClientID == clientID &&
			(input.Status == database.ConversationStatusPartial ||
				(input.Status == database.ConversationStatusCompleted && input.AssistantMessage == "")) {
			return conversationTestRecord(input), nil
		}
	}
	return nil, nil
}

func conversationTestRecord(input database.ConversationRecordInput) *database.ConversationRecord {
	return &database.ConversationRecord{
		RequestID:          input.RequestID,
		SessionID:          input.SessionID,
		APIKeyID:           input.APIKeyID,
		APIKeyName:         input.APIKeyName,
		ClientID:           input.ClientID,
		ClientIP:           input.ClientIP,
		ResponseID:         input.ResponseID,
		PreviousResponseID: input.PreviousResponseID,
		Endpoint:           input.Endpoint,
		Model:              input.Model,
		UserMessage:        input.UserMessage,
		AssistantMessage:   input.AssistantMessage,
		Status:             input.Status,
		StatusCode:         input.StatusCode,
		InputTokens:        input.InputTokens,
		OutputTokens:       input.OutputTokens,
		DurationMs:         input.DurationMs,
		CreatedAt:          input.CreatedAt,
		UpdatedAt:          input.UpdatedAt,
		CompletedAt:        input.CompletedAt,
	}
}

func (s *fakeConversationRecordStore) snapshots() []database.ConversationRecordInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.ConversationRecordInput(nil), s.saved...)
}

func (s *fakeConversationRecordStore) recordCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func TestExtractCurrentConversationUserMessage(t *testing.T) {
	t.Run("latest user text", func(t *testing.T) {
		body := []byte(`{"input":[
			{"role":"user","content":[{"type":"input_text","text":"old"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"role":"user","content":[{"type":"input_text","text":"new question"}]}
		]}`)
		if got := extractCurrentConversationUserMessage(body); got != "new question" {
			t.Fatalf("message = %q", got)
		}
	})

	t.Run("tool continuation has no new user", func(t *testing.T) {
		body := []byte(`{"previous_response_id":"resp-1","input":[
			{"role":"user","content":"question"},
			{"type":"function_call_output","call_id":"call-1","output":"tool result"}
		]}`)
		if got := extractCurrentConversationUserMessage(body); got != "" {
			t.Fatalf("message = %q, want empty", got)
		}
	})

	t.Run("anthropic tool result is not a new user message", func(t *testing.T) {
		body := []byte(`{"messages":[
			{"role":"user","content":"question"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool-1"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"result"}]}
		]}`)
		if got := extractCurrentConversationUserMessage(body); got != "" {
			t.Fatalf("message = %q, want empty", got)
		}
	})
}

func TestConversationTurnReusesParsedResponsesEvents(t *testing.T) {
	turn := &conversationTurn{}
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp-1"}}`,
		`{"type":"response.output_text.delta","delta":"hello "}`,
		`{"type":"response.output_text.delta","delta":"world"}`,
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello world"}]}]}}`,
	} {
		parsed := gjson.Parse(payload)
		turn.observeResponsesEvent(parsed.Get("type").String(), parsed)
	}
	if got := turn.assistant.String(); got != "hello world" {
		t.Fatalf("assistant = %q", got)
	}
	if turn.responseID != "resp-1" || !turn.terminal || turn.failed || turn.awaitingToolOutput {
		t.Fatalf("turn metadata = %#v", turn)
	}
}

func TestConversationTurnKeepsOnlyFinalAnswerPhase(t *testing.T) {
	turn := &conversationTurn{}
	for _, payload := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"I will inspect the code."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will inspect the code."}]}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"The deployment is healthy."}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The deployment is healthy."}]}}`,
		`{"type":"response.completed","response":{"id":"resp-final","status":"completed","output":[
			{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will inspect the code."}]},
			{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The deployment is healthy."}]}
		]}}`,
	} {
		parsed := gjson.Parse(payload)
		turn.observeResponsesEvent(parsed.Get("type").String(), parsed)
	}

	if turn.finalAssistant != "The deployment is healthy." {
		t.Fatalf("final assistant = %q", turn.finalAssistant)
	}
	if !turn.nonFinalAssistant || turn.awaitingToolOutput || !turn.terminal {
		t.Fatalf("turn metadata = %#v", turn)
	}
}

func TestConversationTurnKeepsCommentaryOnlyResponseAsPartial(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	turn := &conversationTurn{
		recorder:          recorder,
		startedAt:         time.Now().UTC(),
		apiKeyID:          9,
		clientID:          "client",
		explicitSessionID: "session",
		userMessage:       "inspect the deployment",
	}
	for _, payload := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"I will inspect the deployment."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will inspect the deployment."}]}}`,
		`{"type":"response.completed","response":{"id":"resp-commentary","status":"completed","output":[
			{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will inspect the deployment."}]}
		]}}`,
	} {
		parsed := gjson.Parse(payload)
		turn.observeResponsesEvent(parsed.Get("type").String(), parsed)
	}
	turn.finish(&database.UsageLogInput{
		InboundEndpoint: "/v1/responses",
		Model:           "gpt-5.6-sol",
		StatusCode:      http.StatusOK,
	}, nil)
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 1 || snapshots[0].AssistantMessage != "" ||
		snapshots[0].Status != database.ConversationStatusPartial {
		t.Fatalf("commentary-only response = %#v", snapshots)
	}
}

func TestBeginConversationTurnWritesPartialThenUpdatesSameRecord(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Session_id", "session-start")
	c.Set(contextAPIKeyID, int64(9))

	handler := &Handler{convRecorder: recorder}
	turn := handler.beginConversationTurn(c, []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"role":"user","content":"keep this request"}]
	}`))
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp-start"}}`,
		`{"type":"response.completed","response":{"id":"resp-start","status":"completed","output":[
			{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"kept final answer"}]}
		]}}`,
	} {
		parsed := gjson.Parse(payload)
		turn.observeResponsesEvent(parsed.Get("type").String(), parsed)
	}
	turn.finish(&database.UsageLogInput{
		InboundEndpoint: "/v1/responses",
		Model:           "gpt-5.6-sol",
		StatusCode:      http.StatusOK,
	}, nil)
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("saved snapshots = %d, want initial and final", len(snapshots))
	}
	initial, final := snapshots[0], snapshots[1]
	if initial.RequestID == "" || initial.RequestID != final.RequestID {
		t.Fatalf("request IDs = %q / %q", initial.RequestID, final.RequestID)
	}
	if initial.UserMessage != "keep this request" || initial.AssistantMessage != "" ||
		initial.Status != database.ConversationStatusPartial || initial.CompletedAt != nil {
		t.Fatalf("initial snapshot = %#v", initial)
	}
	if final.UserMessage != "keep this request" || final.AssistantMessage != "kept final answer" ||
		final.Status != database.ConversationStatusCompleted || final.CompletedAt == nil {
		t.Fatalf("final snapshot = %#v", final)
	}
	if got := store.recordCount(); got != 1 {
		t.Fatalf("stored row count = %d, want one", got)
	}
}

func TestResponseOutputAwaitsToolAfterCommentary(t *testing.T) {
	output := gjson.Parse(`[
		{"type":"function_call","call_id":"call-1","name":"shell"},
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Running the command."}]}
	]`)
	if !responseOutputAwaitsToolOutput(output) {
		t.Fatal("commentary after a tool call must still leave the turn awaiting tool output")
	}
}

func TestConversationTurnFallbackKeepsEarlyFailureWithoutAnswer(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no account"})

	turn := &conversationTurn{
		recorder:    recorder,
		startedAt:   time.Now().UTC(),
		apiKeyID:    4,
		endpoint:    "/v1/responses",
		model:       "gpt-5.4",
		userMessage: "please answer",
	}
	turn.finishFallback(c)
	turn.finishFallback(c)
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 1 || snapshots[0].UserMessage != "please answer" ||
		snapshots[0].AssistantMessage != "" || snapshots[0].Status != database.ConversationStatusFailed ||
		snapshots[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("early failure = %#v", snapshots)
	}
}

func TestConversationRecorderKeepsIncompleteAndInterruptedTurnsWithoutAnswer(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	start := time.Now().UTC()

	for _, result := range []conversationTurnResult{
		{
			StartedAt: start, APIKeyID: 9, ClientID: "client",
			ExplicitSessionID: "session-incomplete", UserMessage: "too much context",
			ResponseID: "resp-incomplete", Terminal: true, Incomplete: true,
			StatusCode: http.StatusOK,
		},
		{
			StartedAt: start.Add(time.Second), APIKeyID: 9, ClientID: "client",
			ExplicitSessionID: "session-interrupted", UserMessage: "interrupted request",
			ResponseID: "resp-interrupted", Terminal: false,
			StatusCode: http.StatusOK,
		},
	} {
		if !recorder.enqueue(result) {
			t.Fatal("enqueue returned false")
		}
	}
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("saved snapshots = %d, want two", len(snapshots))
	}
	if snapshots[0].Status != database.ConversationStatusIncomplete || snapshots[0].AssistantMessage != "" {
		t.Fatalf("incomplete snapshot = %#v", snapshots[0])
	}
	if snapshots[1].Status != database.ConversationStatusPartial || snapshots[1].AssistantMessage != "" {
		t.Fatalf("interrupted snapshot = %#v", snapshots[1])
	}
}

func TestConversationTurnStatusRequiresFinalAnswerForCompleted(t *testing.T) {
	if got := conversationTurnStatus(conversationTurnResult{Terminal: true, StatusCode: http.StatusOK}); got != database.ConversationStatusPartial {
		t.Fatalf("terminal response without answer status = %q", got)
	}
	if got := conversationTurnStatus(conversationTurnResult{
		Terminal: true, StatusCode: http.StatusOK, AssistantMessage: "final",
	}); got != database.ConversationStatusCompleted {
		t.Fatalf("terminal response with answer status = %q", got)
	}
	if got := conversationTurnStatus(conversationTurnResult{RequestErr: context.DeadlineExceeded}); got != database.ConversationStatusIncomplete {
		t.Fatalf("deadline status = %q", got)
	}
}

func TestConversationRecorderKeepsPartialInteractionWhenChainEndsWithoutAnswer(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	start := time.Now().UTC()

	if !recorder.enqueue(conversationTurnResult{
		StartedAt: start, APIKeyID: 9, ClientID: "client",
		ExplicitSessionID: "session", UserMessage: "run the tool", ResponseID: "resp-1",
		Terminal: true, AwaitingToolOutput: true, StatusCode: http.StatusOK,
	}) {
		t.Fatal("enqueue partial interaction returned false")
	}
	if !recorder.enqueue(conversationTurnResult{
		StartedAt: start.Add(time.Second), APIKeyID: 9, ClientID: "client",
		ExplicitSessionID: "session", PreviousResponseID: "resp-1", ResponseID: "resp-2",
		Terminal: true, Failed: true, StatusCode: http.StatusBadGateway,
	}) {
		t.Fatal("enqueue failed continuation returned false")
	}
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 2 || snapshots[0].RequestID != snapshots[1].RequestID {
		t.Fatalf("tool chain snapshots = %#v", snapshots)
	}
	final := snapshots[1]
	if final.UserMessage != "run the tool" || final.AssistantMessage != "" ||
		final.Status != database.ConversationStatusFailed {
		t.Fatalf("failed tool chain = %#v", final)
	}
}

func TestConversationRecorderFoldsToolContinuationsAndDrainsOnClose(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	start := time.Now().UTC()

	results := []conversationTurnResult{
		{
			StartedAt: start, APIKeyID: 9, APIKeyName: "key", ClientID: "client",
			ExplicitSessionID: "session", UserMessage: "run the tests", ResponseID: "resp-1",
			AssistantMessage: "I will run the tests.", Terminal: true,
			AwaitingToolOutput: true, StatusCode: 200, DurationMs: 100,
		},
		{
			StartedAt: start.Add(time.Second), APIKeyID: 9, ClientID: "client",
			ExplicitSessionID: "session", ResponseID: "resp-2",
			AssistantMessage: "The test command is still running.", Terminal: true,
			AwaitingToolOutput: true, StatusCode: 200, DurationMs: 50,
		},
		{
			StartedAt: start.Add(2 * time.Second), APIKeyID: 9, ClientID: "client",
			ExplicitSessionID: "session", AssistantMessage: "all tests passed", ResponseID: "resp-3",
			Terminal: true, StatusCode: 200, OutputTokens: 4, DurationMs: 75,
		},
		{
			StartedAt: start.Add(3 * time.Second), APIKeyID: 9, ClientID: "client",
			PreviousResponseID: "resp-3", UserMessage: "next question", AssistantMessage: "next answer",
			ResponseID: "resp-4", Terminal: true, StatusCode: 200,
		},
	}
	for _, result := range results {
		if !recorder.enqueue(result) {
			t.Fatal("enqueue returned false")
		}
	}
	recorder.Close()
	recorder.Close()

	snapshots := store.snapshots()
	if len(snapshots) != 4 {
		t.Fatalf("snapshot count = %d, want four serialized writes", len(snapshots))
	}
	firstRequestID := snapshots[0].RequestID
	for index := 1; index <= 2; index++ {
		if snapshots[index].RequestID != firstRequestID {
			t.Fatalf("tool continuation %d created a new row", index)
		}
	}
	final := snapshots[2]
	if final.SessionID != "session" || final.UserMessage != "run the tests" || final.AssistantMessage != "all tests passed" {
		t.Fatalf("final folded interaction = %#v", final)
	}
	if final.Status != database.ConversationStatusCompleted || final.DurationMs != 225 {
		t.Fatalf("final status/duration = %q/%d", final.Status, final.DurationMs)
	}
	if snapshots[3].RequestID == firstRequestID || snapshots[3].SessionID != "session" {
		t.Fatalf("next user turn was not a new row in the same session: %#v", snapshots[3])
	}
	if got := store.recordCount(); got != 2 {
		t.Fatalf("stored row count = %d, want one row per user interaction", got)
	}
}

func TestConversationRecorderSkipsOrphanInternalTurns(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	for index := range 10 {
		assistant := ""
		if index%2 != 0 {
			assistant = "orphan assistant output"
		}
		if !recorder.enqueue(conversationTurnResult{
			StartedAt:         time.Now().UTC(),
			APIKeyID:          9,
			ClientID:          "client",
			ExplicitSessionID: "internal-session",
			ResponseID:        "empty-response",
			AssistantMessage:  assistant,
			Terminal:          true,
			StatusCode:        http.StatusOK,
		}) {
			t.Fatal("enqueue returned false")
		}
	}
	recorder.Close()

	if got := store.recordCount(); got != 0 {
		t.Fatalf("orphan internal turns created %d rows", got)
	}
}

func TestConversationRecorderQueueFullDoesNotBlock(t *testing.T) {
	recorder := &conversationRecorder{workCh: make(chan conversationTurnResult, 1), queueByteLimit: 1024}
	if !recorder.enqueue(conversationTurnResult{}) {
		t.Fatal("first enqueue returned false")
	}
	start := time.Now()
	if recorder.enqueue(conversationTurnResult{}) {
		t.Fatal("second enqueue should be dropped when the queue is full")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("full queue blocked for %s", elapsed)
	}
}

func TestConversationRecorderQueuePayloadLimitDoesNotBlock(t *testing.T) {
	recorder := &conversationRecorder{
		workCh:         make(chan conversationTurnResult, 2),
		queueByteLimit: 10,
	}
	if !recorder.enqueue(conversationTurnResult{UserMessage: "12345"}) {
		t.Fatal("first enqueue returned false")
	}
	start := time.Now()
	if recorder.enqueue(conversationTurnResult{AssistantMessage: "123456"}) {
		t.Fatal("enqueue should be dropped when queued payload bytes exceed the limit")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("payload-limited queue blocked for %s", elapsed)
	}
	if got := recorder.queuedBytes.Load(); got != 5 {
		t.Fatalf("queued bytes = %d, want 5", got)
	}
}

func TestConversationCacheStoresOnlyLightweightSessionLink(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	if !recorder.enqueue(conversationTurnResult{
		StartedAt:         time.Now().UTC(),
		APIKeyID:          3,
		ExplicitSessionID: "session-lightweight",
		UserMessage:       "sensitive user content",
		AssistantMessage:  "large assistant content",
		ResponseID:        "resp-lightweight",
		Terminal:          true,
		StatusCode:        200,
	}) {
		t.Fatal("enqueue returned false")
	}
	recorder.Close()

	link := recorder.byResponse[conversationResponseCacheKey(3, "resp-lightweight")]
	if link == nil || link.sessionID != "session-lightweight" {
		t.Fatalf("cached link = %#v", link)
	}
}

func TestResolveConversationSessionIDUsesStableRequestIdentity(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Session_id", "explicit-session")
	if got := resolveConversationSessionIDFromHeaders(headers, []byte(`{"input":"hello"}`)); got != "explicit-session" {
		t.Fatalf("explicit session = %q", got)
	}

	headers = make(http.Header)
	headers.Set("Idempotency-Key", "request-session")
	if got := resolveConversationSessionIDFromHeaders(headers, []byte(`{}`)); got != "request-session" {
		t.Fatalf("idempotency fallback session = %q", got)
	}

	firstHeaders := make(http.Header)
	firstHeaders.Set("Idempotency-Key", "internal-request-1")
	secondHeaders := make(http.Header)
	secondHeaders.Set("Idempotency-Key", "internal-request-2")
	stableBody := []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"inspect upstream"}]}`)
	if firstID, secondID := resolveConversationSessionIDFromHeaders(firstHeaders, stableBody),
		resolveConversationSessionIDFromHeaders(secondHeaders, stableBody); firstID == "" || firstID != secondID {
		t.Fatalf("rotating idempotency keys split one content session: %q / %q", firstID, secondID)
	}

	first := []byte(`{"model":"gpt-5.4","messages":[
		{"role":"user","content":"first question"},
		{"role":"assistant","content":"first answer"},
		{"role":"user","content":"follow up one"}
	]}`)
	second := []byte(`{"model":"gpt-5.4","messages":[
		{"role":"user","content":"first question"},
		{"role":"assistant","content":"first answer"},
		{"role":"user","content":"follow up two"}
	]}`)
	firstID := resolveConversationSessionIDFromHeaders(nil, first)
	secondID := resolveConversationSessionIDFromHeaders(nil, second)
	if firstID == "" || firstID != secondID {
		t.Fatalf("derived session IDs = %q / %q, want one stable non-empty ID", firstID, secondID)
	}
}

func TestConversationRecorderPersistsEndToEndWithSQLite(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "conversation-e2e.db"))
	if err != nil {
		t.Fatalf("database.New error: %v", err)
	}
	defer db.Close()
	if err := db.EnsureConversationRecords(context.Background()); err != nil {
		t.Fatalf("EnsureConversationRecords error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Session_id", "session-e2e")
	c.Set(contextAPIKeyID, int64(12))
	c.Set(contextAPIKeyName, "e2e-key")

	handler := &Handler{convRecorder: newConversationRecorder(db)}
	turn := handler.beginConversationTurn(c, []byte(`{
		"model":"gpt-5.4",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hello sqlite"}]}]
	}`))
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp-e2e"}}`,
		`{"type":"response.output_text.delta","delta":"hello "}`,
		`{"type":"response.output_text.delta","delta":"database"}`,
		`{"type":"response.completed","response":{"id":"resp-e2e","status":"completed","output":[]}}`,
	} {
		parsed := gjson.Parse(payload)
		turn.observeResponsesEvent(parsed.Get("type").String(), parsed)
	}
	turn.finish(&database.UsageLogInput{
		Endpoint:        "/v1/responses",
		Model:           "gpt-5.4",
		StatusCode:      200,
		InputTokens:     11,
		OutputTokens:    2,
		DurationMs:      25,
		InboundEndpoint: "/v1/responses",
	}, nil)
	handler.Close()

	record, err := db.FindConversationByResponseID(context.Background(), 12, "resp-e2e")
	if err != nil {
		t.Fatalf("FindConversationByResponseID error: %v", err)
	}
	if record == nil {
		t.Fatal("conversation record was not persisted")
	}
	if record.SessionID != "session-e2e" || record.UserMessage != "hello sqlite" || record.AssistantMessage != "hello database" {
		t.Fatalf("persisted conversation = %#v", record)
	}
	if record.InputTokens != 11 || record.OutputTokens != 2 || record.Status != database.ConversationStatusCompleted {
		t.Fatalf("persisted metadata = %#v", record)
	}
}

func BenchmarkConversationTurnObserveParsedDelta(b *testing.B) {
	parsed := gjson.Parse(`{"type":"response.output_text.delta","delta":"hello"}`)
	var turn conversationTurn
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		turn.observeResponsesEvent("response.output_text.delta", parsed)
		if turn.assistant.Len() >= 4096 {
			turn.assistant.Reset()
		}
	}
}
