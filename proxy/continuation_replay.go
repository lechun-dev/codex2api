package proxy

import "encoding/json"

// 2026-09-08 coder(lq): Fail closed for references, tools, encrypted reasoning,
// and unknown input shapes. Only explicit text messages can borrow an account.
func canReplayTextContinuation(body []byte) bool {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if _, exists := request[key]; exists {
			return false
		}
	}
	if raw, exists := request["tools"]; exists {
		var tools []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &tools) != nil {
			return false
		}
		for _, tool := range tools {
			if tool.Type != "function" {
				return false
			}
		}
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(request["input"], &messages) != nil || len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		for key := range message {
			if key != "type" && key != "role" && key != "content" {
				return false
			}
		}
		var kind, role string
		if raw, exists := message["type"]; exists && (json.Unmarshal(raw, &kind) != nil || kind != "message") {
			return false
		}
		if json.Unmarshal(message["role"], &role) != nil || (role != "user" && role != "assistant" && role != "system" && role != "developer") {
			return false
		}
		var text string
		if json.Unmarshal(message["content"], &text) == nil && text != "" {
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &parts) != nil || len(parts) == 0 {
			return false
		}
		for _, part := range parts {
			if len(part) != 2 || json.Unmarshal(part["type"], &kind) != nil || (kind != "input_text" && kind != "output_text") || json.Unmarshal(part["text"], &text) != nil || text == "" {
				return false
			}
		}
	}
	return true
}
