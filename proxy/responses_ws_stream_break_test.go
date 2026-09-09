package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestResponsesWebSocketStreamBreakReturnsFailedTerminalAndKeepsConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	previousSettings := CurrentRuntimeSettings()
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
		ApplyRuntimeSettings(previousSettings)
	})
	nextSettings := previousSettings
	nextSettings.CodexWSSilentRetry = false
	nextSettings.CodexWSSilentRetries = 0
	nextSettings.CodexWSHideErrors = true
	nextSettings.ContinuousRetryPolicy = database.ContinuousRetryPolicy{}
	ApplyRuntimeSettings(nextSettings)

	var calls atomic.Int64
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header, poolRouteKey string) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`data: {"type":"response.created","response":{"id":"resp_broken"}}` + "\n\n" +
						`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n",
				)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp_ok","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n",
			)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:      2,
		MaxRetries:          0,
		MaxRateLimitRetries: 0,
		TestConcurrency:     1,
		TestModel:           "gpt-5.5",
	})
	t.Cleanup(store.Stop)
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at-1", PlanType: "pro", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "at-2", PlanType: "pro", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		if response != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, response.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","input":"first"}`)); err != nil {
		t.Fatalf("write first request: %v", err)
	}
	failed := readResponsesWSTerminalEvent(t, conn)
	if eventType := gjson.GetBytes(failed, "type").String(); eventType != "response.failed" {
		t.Fatalf("first terminal type = %q, want response.failed; body=%s", eventType, failed)
	}
	if code := gjson.GetBytes(failed, "response.error.code").String(); code != ErrorCodeUpstreamStreamBreak {
		t.Fatalf("first terminal error code = %q, want %q; body=%s", code, ErrorCodeUpstreamStreamBreak, failed)
	}

	// 2026-09-08 coder(lq): A failed turn must not force Codex to reconnect the
	// transport; the next response.create should complete on the same connection.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","input":"second"}`)); err != nil {
		t.Fatalf("write second request on reused connection: %v", err)
	}
	completed := readResponsesWSTerminalEvent(t, conn)
	if eventType := gjson.GetBytes(completed, "type").String(); eventType != "response.completed" {
		t.Fatalf("second terminal type = %q, want response.completed; body=%s", eventType, completed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}
