package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestPopulateRequestContentUsageMetaExtractsSessionConversationAndText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Session_id", "session-123")
	c.Request.Header.Set("Conversation_id", "conversation-456")
	setRawRequestBody(c, []byte(`{
		"previous_response_id":"resp_789",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"message","role":"assistant","content":"world"}
		]
	}`))

	input := &database.UsageLogInput{Endpoint: "/v1/responses"}
	populateRequestContentUsageMeta(c, input)

	if input.SessionID != "session-123" {
		t.Fatalf("SessionID = %q, want session-123", input.SessionID)
	}
	if input.ConversationID != "conversation-456" {
		t.Fatalf("ConversationID = %q, want conversation-456", input.ConversationID)
	}
	if input.PreviousResponseID != "resp_789" {
		t.Fatalf("PreviousResponseID = %q, want resp_789", input.PreviousResponseID)
	}
	if !strings.Contains(input.RequestText, "hello") || !strings.Contains(input.RequestText, "world") {
		t.Fatalf("RequestText = %q, want extracted conversation text", input.RequestText)
	}
}

func TestPopulateRequestContentUsageMetaUsesResolvedSessionID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Authorization", "Bearer sk-test-usage-content")
	setRawRequestBody(c, []byte(`{
		"prompt_cache_key":"prompt-cache-123",
		"input":[{"type":"message","role":"user","content":"hello"}]
	}`))

	input := &database.UsageLogInput{Endpoint: "/v1/responses"}
	populateRequestContentUsageMeta(c, input)

	if input.SessionID != "prompt-cache-123" {
		t.Fatalf("SessionID = %q, want prompt-cache-123", input.SessionID)
	}
	if input.ConversationID != "prompt-cache-123" {
		t.Fatalf("ConversationID = %q, want prompt-cache-123", input.ConversationID)
	}
}

func TestTruncateUsageLogTextPreservesUTF8Boundary(t *testing.T) {
	got := truncateUsageLogText("ab🙂cd", 5)
	if got != "ab" {
		t.Fatalf("truncateUsageLogText() = %q, want %q", got, "ab")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateUsageLogText() returned invalid utf8: %q", got)
	}
}
