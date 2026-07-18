package proxy

import (
	"context"
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

func (s *fakeConversationRecordStore) FindLatestPartialConversation(_ context.Context, apiKeyID int64, sessionID, clientID string) (*database.ConversationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.saved) - 1; index >= 0; index-- {
		input := s.saved[index]
		if input.APIKeyID == apiKeyID && input.SessionID == sessionID &&
			input.ClientID == clientID && input.Status == database.ConversationStatusPartial {
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

func TestConversationRecorderFoldsToolContinuationsAndDrainsOnClose(t *testing.T) {
	store := newFakeConversationRecordStore()
	recorder := newConversationRecorder(store)
	start := time.Now().UTC()

	results := []conversationTurnResult{
		{
			StartedAt: start, APIKeyID: 9, APIKeyName: "key", ClientID: "client",
			ExplicitSessionID: "session", UserMessage: "run the tests", ResponseID: "resp-1",
			Terminal: true, AwaitingToolOutput: true, StatusCode: 200, DurationMs: 100,
		},
		{
			StartedAt: start.Add(time.Second), APIKeyID: 9, ClientID: "client",
			PreviousResponseID: "resp-1", ResponseID: "resp-2", Terminal: true,
			AwaitingToolOutput: true, StatusCode: 200, DurationMs: 50,
		},
		{
			StartedAt: start.Add(2 * time.Second), APIKeyID: 9, ClientID: "client",
			PreviousResponseID: "resp-2", AssistantMessage: "all tests passed", ResponseID: "resp-3",
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
}

func TestConversationRecorderQueueFullDoesNotBlock(t *testing.T) {
	recorder := &conversationRecorder{workCh: make(chan conversationTurnResult, 1)}
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
