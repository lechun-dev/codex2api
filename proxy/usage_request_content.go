package proxy

import (
	"strings"
	"unicode/utf8"

	"github.com/codex2api/database"
	"github.com/codex2api/security/promptfilter"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	usageLogSessionFieldMaxLen = 255
	usageLogRequestTextMaxLen  = 128 * 1024
)

func populateRequestContentUsageMeta(c *gin.Context, input *database.UsageLogInput) {
	if c == nil || input == nil {
		return
	}

	body, _ := rawRequestBodyFromContext(c)

	if input.SessionID == "" {
		input.SessionID = truncateUsageLogText(ResolveSessionID(c.Request.Header, body), usageLogSessionFieldMaxLen)
	}
	if input.ConversationID == "" {
		input.ConversationID = truncateUsageLogText(resolveUsageLogConversationID(c, body), usageLogSessionFieldMaxLen)
	}
	if input.PreviousResponseID == "" {
		input.PreviousResponseID = truncateUsageLogText(gjson.GetBytes(body, "previous_response_id").String(), usageLogSessionFieldMaxLen)
	}
	if input.RequestText == "" {
		endpoint := strings.TrimSpace(input.InboundEndpoint)
		if endpoint == "" {
			endpoint = strings.TrimSpace(input.Endpoint)
		}
		if endpoint == "" {
			endpoint = strings.TrimSpace(c.FullPath())
		}
		input.RequestText = strings.TrimSpace(promptfilter.ExtractText(body, endpoint, usageLogRequestTextMaxLen))
	}
}

func resolveUsageLogConversationID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}
	if value := strings.TrimSpace(c.GetHeader("Conversation_id")); value != "" {
		return value
	}
	if value := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); value != "" {
		return value
	}
	return ""
}

func truncateUsageLogText(raw string, maxLen int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || maxLen <= 0 {
		return raw
	}
	raw = strings.ToValidUTF8(raw, "")
	if len(raw) <= maxLen {
		return raw
	}
	end := 0
	for i := 0; i < len(raw); {
		_, size := utf8.DecodeRuneInString(raw[i:])
		if i+size > maxLen {
			break
		}
		end = i + size
		i += size
	}
	return raw[:end]
}
