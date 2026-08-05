package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIKeyClientCountsDeduplicateAndSeparateActiveFromTotal(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "clients.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "clients", "sk-client-counts-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey error = %v", err)
	}
	for _, clientID := range []string{"client-alpha", "client-beta", "client-alpha"} {
		if !db.RecordAPIKeyClient(keyID, clientID) {
			t.Fatalf("RecordAPIKeyClient(%q) = false", clientID)
		}
	}
	if err := db.FlushAPIKeyClients(ctx); err != nil {
		t.Fatalf("FlushAPIKeyClients error = %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := db.conn.ExecContext(ctx, `UPDATE api_key_clients SET last_seen_at=$1 WHERE api_key_id=$2 AND client_id_hash=$3`,
		sqliteTimeParam(old), keyID, hashAPIKeyClientID(keyID, "client-beta")); err != nil {
		t.Fatalf("age client record error = %v", err)
	}

	counts, err := db.ListAPIKeyClientCounts(ctx, map[int64]time.Duration{keyID: time.Hour})
	if err != nil {
		t.Fatalf("ListAPIKeyClientCounts error = %v", err)
	}
	if got := counts[keyID]; got.Active != 1 || got.Total != 2 {
		t.Fatalf("counts = %#v, want active=1 total=2", got)
	}

	var storedHash string
	if err := db.conn.QueryRowContext(ctx, `SELECT client_id_hash FROM api_key_clients WHERE api_key_id=$1 ORDER BY client_id_hash LIMIT 1`, keyID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored hash error = %v", err)
	}
	if len(storedHash) != 64 || storedHash == "client-alpha" || storedHash == "client-beta" {
		t.Fatalf("stored client identity = %q, want SHA-256 hash only", storedHash)
	}
}

func TestAPIKeyClientHashIsScopedToAPIKey(t *testing.T) {
	first := hashAPIKeyClientID(1, "shared-client")
	second := hashAPIKeyClientID(2, "shared-client")
	if first == second {
		t.Fatal("same client identifier produced a linkable hash across API keys")
	}
	if first != hashAPIKeyClientID(1, "shared-client") {
		t.Fatal("client hash is not stable within one API key")
	}
}

func TestDeleteAPIKeyRemovesClientCounts(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "delete-clients.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "clients", "sk-delete-client-counts-1234567890")
	if err != nil {
		t.Fatalf("InsertAPIKey error = %v", err)
	}
	db.RecordAPIKeyClient(keyID, "client-alpha")
	if err := db.FlushAPIKeyClients(ctx); err != nil {
		t.Fatalf("FlushAPIKeyClients error = %v", err)
	}
	// This observation is still pending when the key is deleted. It must not be
	// written back later and recreate orphaned statistics.
	db.RecordAPIKeyClient(keyID, "client-beta")
	if err := db.DeleteAPIKey(ctx, keyID); err != nil {
		t.Fatalf("DeleteAPIKey error = %v", err)
	}
	if err := db.FlushAPIKeyClients(ctx); err != nil {
		t.Fatalf("FlushAPIKeyClients after deletion error = %v", err)
	}

	var count int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_key_clients WHERE api_key_id=$1`, keyID).Scan(&count); err != nil {
		t.Fatalf("count client rows error = %v", err)
	}
	if count != 0 {
		t.Fatalf("client rows after key deletion = %d, want 0", count)
	}
}

func TestCloseFlushesPendingAPIKeyClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-flush.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatalf("New(sqlite) error = %v", err)
	}

	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "clients", "sk-close-flush-1234567890")
	if err != nil {
		db.Close()
		t.Fatalf("InsertAPIKey error = %v", err)
	}
	if !db.RecordAPIKeyClient(keyID, "client-alpha") {
		db.Close()
		t.Fatal("RecordAPIKeyClient returned false")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	reopened, err := New("sqlite", path)
	if err != nil {
		t.Fatalf("reopen sqlite error = %v", err)
	}
	defer reopened.Close()
	counts, err := reopened.ListAPIKeyClientCounts(ctx, map[int64]time.Duration{keyID: time.Hour})
	if err != nil {
		t.Fatalf("ListAPIKeyClientCounts error = %v", err)
	}
	if got := counts[keyID]; got.Active != 1 || got.Total != 1 {
		t.Fatalf("counts after reopen = %#v, want active=1 total=1", got)
	}
}

func TestAPIKeyClientsMySQLDDLIsMySQL56Compatible(t *testing.T) {
	ddl := apiKeyClientsMySQLDDL()
	for _, fragment := range []string{
		"ENGINE=InnoDB DEFAULT CHARSET=utf8",
		"client_id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
		"PRIMARY KEY (api_key_id, client_id_hash)",
		"KEY idx_api_key_clients_last_seen (api_key_id, last_seen_at)",
	} {
		if !strings.Contains(ddl, fragment) {
			t.Fatalf("MySQL DDL missing %q:\n%s", fragment, ddl)
		}
	}
	for _, unsupported := range []string{"IF NOT EXISTS idx_", "TIMESTAMPTZ", "ON CONFLICT", "JSONB"} {
		if strings.Contains(ddl, unsupported) {
			t.Fatalf("MySQL 5.6 DDL contains unsupported fragment %q:\n%s", unsupported, ddl)
		}
	}
}
