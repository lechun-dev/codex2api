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
		ID: 7,
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

func TestAPIKeyClientLimitEnforceRequiresClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID: 8,
		Limits: database.APIKeyLimits{
			MaxClients:      1,
			ClientLimitMode: database.APIKeyClientLimitModeEnforce,
		},
	}

	ctx := testAPIKeyLimitContext(row, "")
	status, msg := handler.enforceAPIKeyLimits(ctx, "")
	if status != http.StatusBadRequest {
		t.Fatalf("missing client status=%d msg=%q, want 400", status, msg)
	}
	if !strings.Contains(msg, "X-Client-Id") {
		t.Fatalf("missing client msg=%q, want X-Client-Id error", msg)
	}
}

func TestAPIKeyClientLimitObserveDoesNotReject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetRuntimeCache(cache.NewMemory(1))
	row := &database.APIKeyRow{
		ID: 9,
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
		ID: 10,
		Limits: database.APIKeyLimits{
			MaxClients: 1,
		},
	}

	ctx := testAPIKeyLimitContext(row, "")
	if status, msg := handler.enforceAPIKeyLimits(ctx, ""); status != 0 || msg != "" {
		t.Fatalf("off mode status=%d msg=%q, want allow", status, msg)
	}
}

func testAPIKeyLimitContext(row *database.APIKeyRow, clientID string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if clientID != "" {
		ctx.Request.Header.Set("X-Client-Id", clientID)
	}
	ctx.Set(contextAPIKeyRow, row)
	return ctx
}
