package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newAPIKeyOwnerTestHandler(t *testing.T) (*Handler, *database.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Handler{db: db}, db
}

func TestCreateAPIKeyPersistsOwnerEmail(t *testing.T) {
	handler, db := newAPIKeyOwnerTestHandler(t)
	body := `{"name":"team-a","key":"sk-owner-email-create-123456","owner_email":"alice@lechun.cc"}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/keys", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.CreateAPIKey(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	keys, err := db.ListAPIKeys(ctx.Request.Context())
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListAPIKeys: %d %v", len(keys), err)
	}
	if keys[0].OwnerEmail != "alice@lechun.cc" {
		t.Fatalf("owner_email = %q, want alice@lechun.cc", keys[0].OwnerEmail)
	}

	// 列表接口必须回显，否则前端看不到谁在用。
	listRecorder := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRecorder)
	listCtx.Request = httptest.NewRequest(http.MethodGet, "/api/admin/keys", nil)
	handler.ListAPIKeys(listCtx)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var payload apiKeysResponse
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].OwnerEmail != "alice@lechun.cc" {
		t.Fatalf("列表未回显 owner_email: %+v", payload.Keys)
	}
}

func TestCreateAPIKeyRejectsInvalidOwnerEmail(t *testing.T) {
	handler, db := newAPIKeyOwnerTestHandler(t)
	for _, body := range []string{
		`{"name":"bad","owner_email":"not-an-email"}`,
		`{"name":"bad","owner_email":"a@@b.cc"}`,
		`{"name":"bad","owner_email":"a b@c.cc"}`,
		`{"name":"bad","owner_email":"a@b"}`,
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/keys", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		handler.CreateAPIKey(ctx)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s => status %d, want 400", body, recorder.Code)
		}
	}
	if keys, err := db.ListAPIKeys(t.Context()); err != nil || len(keys) != 0 {
		t.Fatalf("非法邮箱不应落库: %d %v", len(keys), err)
	}
}

func TestUpdateAPIKeyOwnerEmail(t *testing.T) {
	handler, db := newAPIKeyOwnerTestHandler(t)
	ctx := t.Context()
	id, err := db.InsertAPIKeyWithOptions(ctx, database.APIKeyInput{
		Name: "team-b", Key: "sk-owner-email-update-123456", OwnerEmail: "old@lechun.cc",
	})
	if err != nil {
		t.Fatalf("InsertAPIKeyWithOptions: %v", err)
	}

	patch := func(body string) int {
		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = httptest.NewRequest(http.MethodPatch, "/api/admin/keys/1", strings.NewReader(body))
		ginCtx.Request.Header.Set("Content-Type", "application/json")
		ginCtx.Params = gin.Params{{Key: "id", Value: "1"}}
		handler.UpdateAPIKey(ginCtx)
		return recorder.Code
	}

	if code := patch(`{"owner_email":"new@lechun.cc"}`); code != http.StatusOK {
		t.Fatalf("PATCH 修改邮箱 status = %d", code)
	}
	keys, err := db.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].OwnerEmail != "new@lechun.cc" {
		t.Fatalf("修改后邮箱 = %+v err=%v", keys, err)
	}

	// 空字符串表示清空归属人。
	if code := patch(`{"owner_email":""}`); code != http.StatusOK {
		t.Fatalf("PATCH 清空邮箱 status = %d", code)
	}
	keys, err = db.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].OwnerEmail != "" {
		t.Fatalf("清空后邮箱 = %+v err=%v", keys, err)
	}

	// 非法邮箱必须拒绝，且不覆盖已有值。
	if code := patch(`{"owner_email":"broken"}`); code != http.StatusBadRequest {
		t.Fatalf("非法邮箱 status = %d, want 400", code)
	}
	if keys, err = db.ListAPIKeys(ctx); err != nil || keys[0].ID != id {
		t.Fatalf("列表异常: %+v %v", keys, err)
	}
}
