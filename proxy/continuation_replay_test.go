package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestCanReplayTextContinuation(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"role":"user","content":"hello"}]}`,
		`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},{"role":"user","content":"hello"}]}`,
	} {
		if !canReplayTextContinuation([]byte(body)) {
			t.Errorf("rejected text: %s", body)
		}
	}
	for _, body := range []string{
		`{"input":"continue"}`, `{"input":[]}`, `null`,
		`{"previous_response_id":"resp_1","input":[{"role":"user","content":"hi"}]}`,
		`{"conversation":null,"input":[{"role":"user","content":"hi"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"c1","output":"hi"}]}`,
		`{"input":[{"type":"compaction","encrypted_content":"secret"}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]}`,
		`{"input":[{"role":"user","id":"msg_1","content":"hi"}]}`,
	} {
		if canReplayTextContinuation([]byte(body)) {
			t.Errorf("unsafe replay: %s", body)
		}
	}
}

func TestResponsesTextContinuationBorrowsCapacityWithoutTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get(codexTurnStateHeader) != "" || gjson.GetBytes(body, "client_metadata.x-codex-turn-state").Exists() {
			t.Error("borrowed account received old turn state")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(codexTurnStateHeader, "borrowed-state")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_borrow\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	defer store.Stop()
	bound := &auth.Account{DBID: 1, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "bound", Models: []string{"gpt-5.4"}, PlanType: "api"}
	fallback := &auth.Account{DBID: 2, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "fallback", Models: []string{"gpt-5.4"}, PlanType: "api"}
	store.AddAccount(bound)
	store.AddAccount(fallback)
	atomic.StoreInt64(&bound.ActiveRequests, 1)
	atomic.StoreInt64(&bound.OccupiedRequests, 1)
	store.BindSessionAffinity("borrow-turn", bound, "")
	h := NewHandler(store, nil, nil, nil)
	recorder := invokeResponsesHandlerWithContext(t, func(c *gin.Context) {
		c.Request.Header.Set("Session-Id", "borrow-turn")
		c.Request.Header.Set(codexTurnStateHeader, "old-state")
	}, h.Responses, []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hello"}],"client_metadata":{"x-codex-turn-state":"old-state"},"stream":true}`))
	if recorder.Code != 200 || hits.Load() != 1 {
		t.Fatalf("status=%d hits=%d body=%s", recorder.Code, hits.Load(), recorder.Body.String())
	}
	if id, ok := store.SessionAffinityAccountID("borrow-turn"); !ok || id != bound.ID() {
		t.Fatalf("binding changed: %d %v", id, ok)
	}
	if recorder.Header().Get(codexTurnStateHeader) != "" {
		t.Fatal("borrowed state was paired with original binding")
	}
	if atomic.LoadInt64(&fallback.ActiveRequests) != 0 || atomic.LoadInt64(&fallback.OccupiedRequests) != 0 {
		t.Fatal("borrowed slot leaked")
	}
}
