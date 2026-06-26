package database

import (
	"context"
	"strings"
	"testing"
)

func TestSanitizeMySQLTextValueReplacesFourByteRunes(t *testing.T) {
	got := sanitizeMySQLTextValue("hello🙂world")
	want := "hello�world"
	if got != want {
		t.Fatalf("sanitizeMySQLTextValue() = %q, want %q", got, want)
	}
}

func TestSanitizeUsageLogEntryForStorageMySQL(t *testing.T) {
	db := &DB{driver: "mysql"}
	entry := &usageLogEntry{
		RequestText:        "before🙂after",
		ErrorMessage:       "err🙂msg",
		ConversationID:     "conv🙂id",
		PreviousResponseID: "resp_123",
	}

	db.sanitizeUsageLogEntryForStorage(entry)

	if entry.RequestText != "before�after" {
		t.Fatalf("RequestText = %q, want sanitized utf8", entry.RequestText)
	}
	if entry.ErrorMessage != "err�msg" {
		t.Fatalf("ErrorMessage = %q, want sanitized utf8", entry.ErrorMessage)
	}
	if entry.ConversationID != "conv�id" {
		t.Fatalf("ConversationID = %q, want sanitized utf8", entry.ConversationID)
	}
	if entry.PreviousResponseID != "resp_123" {
		t.Fatalf("PreviousResponseID = %q, want unchanged ascii", entry.PreviousResponseID)
	}
}

func TestUsageLogPostgresBatchSizeWithinBindLimit(t *testing.T) {
	if maxUsageLogRowsPerBatch*usageLogInsertColumnCount > maxPostgresBindParams {
		t.Fatalf("batch size exceeds postgres bind limit: rows=%d cols=%d limit=%d", maxUsageLogRowsPerBatch, usageLogInsertColumnCount, maxPostgresBindParams)
	}
	if (maxUsageLogRowsPerBatch+1)*usageLogInsertColumnCount <= maxPostgresBindParams {
		t.Fatalf("batch size is not maximal: rows=%d cols=%d limit=%d", maxUsageLogRowsPerBatch, usageLogInsertColumnCount, maxPostgresBindParams)
	}
}

func TestEncryptUsageLogRequestTextUsesVersionedCiphertext(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	identity := "api-key:42"

	first, err := encryptUsageLogRequestText(masterKey, identity, "hello world")
	if err != nil {
		t.Fatalf("encryptUsageLogRequestText() error: %v", err)
	}
	second, err := encryptUsageLogRequestText(masterKey, identity, "hello world")
	if err != nil {
		t.Fatalf("encryptUsageLogRequestText() second error: %v", err)
	}
	if !strings.HasPrefix(first, usageLogRequestTextCipherPrefix) {
		t.Fatalf("ciphertext prefix = %q, want prefix %q", first, usageLogRequestTextCipherPrefix)
	}
	if first == second {
		t.Fatalf("ciphertexts are identical, want random nonce per encryption")
	}
	plain, err := decryptUsageLogRequestText(masterKey, identity, first)
	if err != nil {
		t.Fatalf("decryptUsageLogRequestText() error: %v", err)
	}
	if plain != "hello world" {
		t.Fatalf("decrypted plaintext = %q, want %q", plain, "hello world")
	}
}

func TestDecryptUsageLogRequestTextRejectsWrongIdentity(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	ciphertext, err := encryptUsageLogRequestText(masterKey, "api-key:42", "secret prompt")
	if err != nil {
		t.Fatalf("encryptUsageLogRequestText() error: %v", err)
	}
	if _, err := decryptUsageLogRequestText(masterKey, "api-key:43", ciphertext); err == nil {
		t.Fatal("decryptUsageLogRequestText() with wrong identity succeeded, want failure")
	}
}

func TestInsertUsageLogEncryptsRequestTextWhenMasterKeyConfigured(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	defer db.Close()
	db.SetUsageLogConfig(UsageLogModeFull, 1000, 3600)
	if err := db.SetUsageLogRequestTextMasterKey([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatalf("SetUsageLogRequestTextMasterKey() error: %v", err)
	}

	if err := db.InsertUsageLog(context.Background(), &UsageLogInput{
		AccountID:   7,
		APIKeyID:    42,
		Endpoint:    "/v1/responses",
		RequestText: "user secret prompt",
	}); err != nil {
		t.Fatalf("InsertUsageLog() error: %v", err)
	}
	if len(db.logBuf) != 1 {
		t.Fatalf("len(logBuf) = %d, want 1", len(db.logBuf))
	}
	stored := db.logBuf[0].RequestText
	if stored == "user secret prompt" {
		t.Fatal("request_text stored in plaintext, want ciphertext")
	}
	plain, err := decryptUsageLogRequestText([]byte("0123456789abcdef0123456789abcdef"), "api-key:42", stored)
	if err != nil {
		t.Fatalf("decryptUsageLogRequestText() error: %v", err)
	}
	if plain != "user secret prompt" {
		t.Fatalf("decrypted plaintext = %q, want %q", plain, "user secret prompt")
	}
}
