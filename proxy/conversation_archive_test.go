package proxy

import (
	"strings"
	"testing"
)

func TestArchiveProtocolDetection(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions": "chat",
		"/chat/completions":    "chat",
		"/v1/responses":        "responses",
		"/responses":           "responses",
		"/v1/messages":         "anthropic",
		"/messages":            "anthropic",
		"/v1beta/models/gemini-4-argon:generateContent": "gemini",
		"/v1/images/generations":                        "",
		"/v1/videos/generations":                        "",
		"/api/admin/accounts":                           "",
	}
	for path, want := range cases {
		if got := archiveProtocol(path); got != want {
			t.Fatalf("archiveProtocol(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestArchiveRequestTextAcrossProtocols(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
		want     string
	}{
		{
			name:     "chat last user message",
			protocol: "chat",
			body:     `{"model":"gpt","messages":[{"role":"user","content":"第一轮"},{"role":"assistant","content":"答"},{"role":"user","content":"第二轮"}]}`,
			want:     "第二轮",
		},
		{
			name:     "chat multimodal drops images",
			protocol: "chat",
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`,
			want:     "看图",
		},
		{
			name:     "responses input string",
			protocol: "responses",
			body:     `{"input":"直接问一句"}`,
			want:     "直接问一句",
		},
		{
			name:     "responses input array",
			protocol: "responses",
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"结构化输入"}]}]}`,
			want:     "结构化输入",
		},
		{
			name:     "anthropic text blocks",
			protocol: "anthropic",
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"anthropic 问题"},{"type":"image","source":{}}]}]}`,
			want:     "anthropic 问题",
		},
		{
			name:     "gemini contents",
			protocol: "gemini",
			body:     `{"contents":[{"role":"user","parts":[{"text":"旧问题"}]},{"role":"model","parts":[{"text":"答"}]},{"role":"user","parts":[{"text":"新问题"},{"inlineData":{"mimeType":"image/png","data":"x"}}]}]}`,
			want:     "新问题",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := archiveRequestText([]byte(tc.body), tc.protocol)
			if got != tc.want || truncated {
				t.Fatalf("got %q truncated=%v, want %q", got, truncated, tc.want)
			}
		})
	}
}

func TestArchiveResponseJSONAcrossProtocols(t *testing.T) {
	cases := []struct {
		name       string
		protocol   string
		body       string
		wantText   string
		wantInput  int
		wantOutput int
	}{
		{
			name:       "chat",
			protocol:   "chat",
			body:       `{"choices":[{"message":{"role":"assistant","content":"聊天回复"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
			wantText:   "聊天回复",
			wantInput:  11,
			wantOutput: 7,
		},
		{
			name:       "responses",
			protocol:   "responses",
			body:       `{"output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"响应文本"}]}],"usage":{"input_tokens":21,"output_tokens":9}}`,
			wantText:   "响应文本",
			wantInput:  21,
			wantOutput: 9,
		},
		{
			name:       "anthropic",
			protocol:   "anthropic",
			body:       `{"content":[{"type":"thinking","thinking":"不要归档"},{"type":"text","text":"克劳德回复"}],"usage":{"input_tokens":31,"output_tokens":12}}`,
			wantText:   "克劳德回复",
			wantInput:  31,
			wantOutput: 12,
		},
		{
			name:       "gemini",
			protocol:   "gemini",
			body:       `{"candidates":[{"content":{"parts":[{"text":"双子回复"},{"functionCall":{"name":"x"}}]}}],"usageMetadata":{"promptTokenCount":41,"candidatesTokenCount":13}}`,
			wantText:   "双子回复",
			wantInput:  41,
			wantOutput: 13,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := archiveResponseText([]byte(tc.body), "application/json", tc.protocol, false)
			if got.text != tc.wantText || got.inputTokens != tc.wantInput || got.outputTokens != tc.wantOutput || got.stream {
				t.Fatalf("got %+v, want text=%q in=%d out=%d", got, tc.wantText, tc.wantInput, tc.wantOutput)
			}
		})
	}
}

func TestArchiveResponseSSEAcrossProtocols(t *testing.T) {
	cases := []struct {
		name       string
		protocol   string
		body       string
		wantText   string
		wantInput  int
		wantOutput int
	}{
		{
			name:     "chat",
			protocol: "chat",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n" +
				"data: [DONE]\n\n",
			wantText:   "你好，世界",
			wantInput:  5,
			wantOutput: 3,
		},
		{
			name:     "responses",
			protocol: "responses",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"流式\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"响应\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":4}}}\n\n",
			wantText:   "流式响应",
			wantInput:  8,
			wantOutput: 4,
		},
		{
			name:     "anthropic",
			protocol: "anthropic",
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":15}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"克劳德\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":6}}\n\n",
			wantText:   "克劳德",
			wantInput:  15,
			wantOutput: 6,
		},
		{
			name:     "gemini",
			protocol: "gemini",
			body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"双子\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"流\"}]}}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":2}}\n\n",
			wantText:   "双子流",
			wantInput:  9,
			wantOutput: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := archiveResponseText([]byte(tc.body), "text/event-stream", tc.protocol, false)
			if got.text != tc.wantText || got.inputTokens != tc.wantInput || got.outputTokens != tc.wantOutput || !got.stream {
				t.Fatalf("got %+v, want text=%q in=%d out=%d stream=true", got, tc.wantText, tc.wantInput, tc.wantOutput)
			}
		})
	}
}

func TestArchiveTextTruncation(t *testing.T) {
	long := strings.Repeat("字", archiveMaxTextRunes+10)
	clamped, truncated := clampArchiveText(long)
	if !truncated || len([]rune(clamped)) != archiveMaxTextRunes {
		t.Fatalf("clamp: truncated=%v runes=%d", truncated, len([]rune(clamped)))
	}

	writer := &archiveResponseWriter{limit: 8}
	writer.capture([]byte("12345"))
	writer.capture([]byte("67890"))
	if !writer.truncated || writer.buf.String() != "12345678" {
		t.Fatalf("writer capture: truncated=%v buf=%q", writer.truncated, writer.buf.String())
	}
}

func TestArchiveStatusMapping(t *testing.T) {
	cases := map[int]string{
		200: "completed",
		400: "failed",
		499: "canceled",
		500: "failed",
		0:   "incomplete",
	}
	for code, want := range cases {
		if got := archiveStatus(code); got != want {
			t.Fatalf("archiveStatus(%d) = %q, want %q", code, got, want)
		}
	}
}
