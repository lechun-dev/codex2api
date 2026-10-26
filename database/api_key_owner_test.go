package database

import (
	"context"
	"path/filepath"
	"testing"
)

// 归属邮箱必须能过 API Key 的读写路径与鉴权查询路径，否则存档 join 拿到的是空值。
func TestAPIKeyOwnerEmailRoundTrip(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	id, err := db.InsertAPIKeyWithOptions(ctx, APIKeyInput{
		Name: "team-a", Key: "sk-owner-roundtrip-123456", OwnerEmail: "alice@lechun.cc",
	})
	if err != nil {
		t.Fatalf("InsertAPIKeyWithOptions: %v", err)
	}

	keys, err := db.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].OwnerEmail != "alice@lechun.cc" {
		t.Fatalf("ListAPIKeys owner = %+v err=%v", keys, err)
	}
	// 鉴权热路径用的是 GetAPIKeyByValue，也要能带出归属人。
	byValue, err := db.GetAPIKeyByValue(ctx, "sk-owner-roundtrip-123456")
	if err != nil || byValue.OwnerEmail != "alice@lechun.cc" {
		t.Fatalf("GetAPIKeyByValue owner = %+v err=%v", byValue, err)
	}
	if email, err := db.GetAPIKeyOwnerEmail(ctx, id); err != nil || email != "alice@lechun.cc" {
		t.Fatalf("GetAPIKeyOwnerEmail = %q err=%v", email, err)
	}

	if err := db.UpdateAPIKey(ctx, id, APIKeyUpdate{OwnerEmail: "bob@lechun.cc", OwnerEmailSet: true}); err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}
	if email, _ := db.GetAPIKeyOwnerEmail(ctx, id); email != "bob@lechun.cc" {
		t.Fatalf("更新后邮箱 = %q", email)
	}

	// 只改邮箱不应影响其它字段。
	if err := db.UpdateAPIKey(ctx, id, APIKeyUpdate{OwnerEmailSet: true}); err != nil {
		t.Fatalf("清空邮箱: %v", err)
	}
	keys, err = db.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].OwnerEmail != "" || keys[0].Name != "team-a" {
		t.Fatalf("清空后 = %+v err=%v", keys, err)
	}
	if err := db.EnsureAPIKeyOwnerEmail(ctx); err != nil {
		t.Fatalf("EnsureAPIKeyOwnerEmail 非幂等: %v", err)
	}
}
