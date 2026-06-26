package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestAPIKeyClientLimitEnforceRejectsNewClientOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID:  7,
		Key: "sk-test-client-limit-0007",
		Limits: database.APIKeyLimits{
			MaxClients:          1,
			ClientWindowMinutes: 60,
			ClientLimitMode:     database.APIKeyClientLimitModeEnforce,
		},
	}

	first := testAPIKeyLimitContext(row, "client-one")
	if status, msg := handler.enforceAPIKeyLimits(first, ""); status != 0 || msg != "" {
		t.Fatalf("first client status=%d msg=%q, want allow", status, msg)
	}
	repeated := testAPIKeyLimitContext(row, "client-one")
	if status, msg := handler.enforceAPIKeyLimits(repeated, ""); status != 0 || msg != "" {
		t.Fatalf("same client status=%d msg=%q, want allow", status, msg)
	}
	second := testAPIKeyLimitContext(row, "client-two")
	status, msg := handler.enforceAPIKeyLimits(second, "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("second client status=%d msg=%q, want 429", status, msg)
	}
	if !strings.Contains(msg, "client limit exceeded") {
		t.Fatalf("second client msg=%q, want client limit error", msg)
	}
}

func TestAPIKeyClientLimitEnforceRejectsInvalidExplicitClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID:  8,
		Key: "sk-test-client-limit-0008",
		Limits: database.APIKeyLimits{
			MaxClients:      1,
			ClientLimitMode: database.APIKeyClientLimitModeEnforce,
		},
	}

	ctx := testAPIKeyLimitContext(row, "bad id with spaces")
	status, msg := handler.enforceAPIKeyLimits(ctx, "")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid client status=%d msg=%q, want 400", status, msg)
	}
	if !strings.Contains(msg, "X-Client-Id") {
		t.Fatalf("invalid client msg=%q, want X-Client-Id error", msg)
	}
}

func TestAPIKeyClientLimitObserveDoesNotReject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID:  9,
		Key: "sk-test-client-limit-0009",
		Limits: database.APIKeyLimits{
			MaxClients:          1,
			ClientWindowMinutes: 60,
			ClientLimitMode:     database.APIKeyClientLimitModeObserve,
		},
	}

	for _, clientID := range []string{"", "bad id with spaces", "client-one", "client-two"} {
		ctx := testAPIKeyLimitContext(row, clientID)
		if status, msg := handler.enforceAPIKeyLimits(ctx, ""); status != 0 || msg != "" {
			t.Fatalf("observe client %q status=%d msg=%q, want allow", clientID, status, msg)
		}
	}
}

func TestAPIKeyClientLimitOffDoesNotRequireClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID:  10,
		Key: "sk-test-client-limit-0010",
		Limits: database.APIKeyLimits{
			MaxClients: 1,
		},
	}

	ctx := testAPIKeyLimitContext(row, "")
	if status, msg := handler.enforceAPIKeyLimits(ctx, ""); status != 0 || msg != "" {
		t.Fatalf("off mode status=%d msg=%q, want allow", status, msg)
	}
}

func TestAPIKeyClientLimitEnforceUsesDerivedClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID:  11,
		Key: "sk-test-client-limit-0011",
		Limits: database.APIKeyLimits{
			MaxClients:          1,
			ClientWindowMinutes: 60,
			ClientLimitMode:     database.APIKeyClientLimitModeEnforce,
		},
	}

	first := testAPIKeyLimitContextWithFingerprint(row, "", "198.51.100.11:1234", "codex_cli_rs/0.136.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464", "MacOS", "arm64", "codex_cli_rs")
	if status, msg := handler.enforceAPIKeyLimits(first, ""); status != 0 || msg != "" {
		t.Fatalf("first derived client status=%d msg=%q, want allow", status, msg)
	}
	repeated := testAPIKeyLimitContextWithFingerprint(row, "", "198.51.100.11:2222", "codex_cli_rs/0.137.0 (Mac OS 15.6.0; arm64) Apple_Terminal/500", "MacOS", "arm64", "codex_cli_rs")
	if status, msg := handler.enforceAPIKeyLimits(repeated, ""); status != 0 || msg != "" {
		t.Fatalf("repeat derived client status=%d msg=%q, want allow", status, msg)
	}
	second := testAPIKeyLimitContextWithFingerprint(row, "", "203.0.113.9:3333", "codex_cli_rs/0.136.0 (Windows 10.0.26120; x86_64) WindowsTerminal", "Windows", "x64", "codex_cli_rs")
	status, msg := handler.enforceAPIKeyLimits(second, "")
	if status != http.StatusTooManyRequests {
		t.Fatalf("second derived client status=%d msg=%q, want 429", status, msg)
	}
	if !strings.Contains(msg, "client limit exceeded") {
		t.Fatalf("second derived client msg=%q, want client limit error", msg)
	}
}

func TestDeriveAPIKeyClientIDUsesAPIKeyScopeAndNormalizedUA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rowA := &database.APIKeyRow{ID: 12, Key: "sk-test-client-limit-0012"}
	rowB := &database.APIKeyRow{ID: 13, Key: "sk-test-client-limit-0013"}

	first := testAPIKeyLimitContextWithFingerprint(rowA, "", "198.51.100.20:4444", "codex_cli_rs/0.136.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464", "MacOS", "arm64", "codex_cli_rs")
	second := testAPIKeyLimitContextWithFingerprint(rowA, "", "198.51.100.20:5555", "codex_cli_rs/0.137.1 (Mac OS 15.6.0; arm64) Apple_Terminal/500", "MacOS", "arm64", "codex_cli_rs")
	third := testAPIKeyLimitContextWithFingerprint(rowB, "", "203.0.113.99:6666", "codex_cli_rs/0.137.1 (Mac OS 15.6.0; arm64) Apple_Terminal/500", "MacOS", "arm64", "codex_cli_rs")

	id1 := deriveAPIKeyClientID(first, rowA)
	id2 := deriveAPIKeyClientID(second, rowA)
	id3 := deriveAPIKeyClientID(third, rowB)
	if id1 == "" || id2 == "" || id3 == "" {
		t.Fatalf("derived ids should not be empty: %q %q %q", id1, id2, id3)
	}
	if id1 != id2 {
		t.Fatalf("same machine fingerprint should stay stable across version changes: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("different api keys should produce different derived ids: %q", id1)
	}
	if !strings.HasPrefix(id1, derivedAPIKeyClientIDPrefix) {
		t.Fatalf("derived id = %q, want %q prefix", id1, derivedAPIKeyClientIDPrefix)
	}
	if !validAPIKeyClientID(id1) {
		t.Fatalf("derived id = %q, want valid client id", id1)
	}
}

func TestDeriveAPIKeyClientIDUsesNetworkHintToReduceCollisions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	row := &database.APIKeyRow{ID: 15, Key: "sk-test-client-limit-0015"}

	first := testAPIKeyLimitContextWithFingerprint(row, "", "198.51.100.40:1111", "codex_cli_rs/0.136.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464", "MacOS", "arm64", "codex_cli_rs")
	second := testAPIKeyLimitContextWithFingerprint(row, "", "203.0.113.40:1111", "codex_cli_rs/0.136.9 (Mac OS 15.5.9; arm64) Apple_Terminal/464", "MacOS", "arm64", "codex_cli_rs")

	id1 := deriveAPIKeyClientID(first, row)
	id2 := deriveAPIKeyClientID(second, row)
	if id1 == "" || id2 == "" {
		t.Fatalf("derived ids should not be empty: %q %q", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("different network hints should produce different derived ids: %q", id1)
	}
}

func TestDeriveAPIKeyClientIDIgnoresSessionHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	row := &database.APIKeyRow{ID: 14, Key: "sk-test-client-limit-0014"}

	first := testAPIKeyLimitContextWithFingerprint(row, "", "198.51.100.31:1111", "codex_cli_rs/0.136.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464", "", "", "")
	first.Request.Header.Set("Session_id", "session-alpha")
	first.Request.Header.Set("Conversation_id", "conversation-alpha")
	second := testAPIKeyLimitContextWithFingerprint(row, "", "198.51.100.31:2222", "codex_cli_rs/0.136.0 (Mac OS 15.5.0; arm64) Apple_Terminal/464", "", "", "")
	second.Request.Header.Set("Session_id", "session-beta")
	second.Request.Header.Set("Conversation_id", "conversation-beta")

	id1 := deriveAPIKeyClientID(first, row)
	id2 := deriveAPIKeyClientID(second, row)
	if id1 == "" || id2 == "" {
		t.Fatalf("derived ids should not be empty: %q %q", id1, id2)
	}
	if id1 != id2 {
		t.Fatalf("session headers should not split the same machine into new clients: %q vs %q", id1, id2)
	}
}

func testAPIKeyLimitContext(row *database.APIKeyRow, clientID string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.RemoteAddr = "192.0.2.10:1234"
	if clientID != "" {
		ctx.Request.Header.Set("X-Client-Id", clientID)
	}
	ctx.Set(contextAPIKeyRow, row)
	return ctx
}

func testAPIKeyLimitContextWithFingerprint(row *database.APIKeyRow, clientID, remoteAddr, userAgent, stainlessOS, stainlessArch, originator string) *gin.Context {
	ctx := testAPIKeyLimitContext(row, clientID)
	ctx.Request.RemoteAddr = remoteAddr
	if userAgent != "" {
		ctx.Request.Header.Set("User-Agent", userAgent)
	}
	if stainlessOS != "" {
		ctx.Request.Header.Set("X-Stainless-Os", stainlessOS)
	}
	if stainlessArch != "" {
		ctx.Request.Header.Set("X-Stainless-Arch", stainlessArch)
	}
	if originator != "" {
		ctx.Request.Header.Set("Originator", originator)
	}
	return ctx
}
