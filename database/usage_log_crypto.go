package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const (
	usageLogRequestTextCipherVersion = "v1"
	usageLogRequestTextCipherPrefix  = usageLogRequestTextCipherVersion + ":"
	usageLogRequestTextNonceSize     = 12
	usageLogRequestTextKeySize       = 32
)

var usageLogRequestTextAAD = []byte("codex2api:usage-log:request-text:v1")

func (db *DB) SetUsageLogRequestTextMasterKey(key []byte) error {
	if db == nil {
		return nil
	}
	if len(key) == 0 {
		db.usageLogRequestTextMasterKey.Store([]byte(nil))
		return nil
	}
	if len(key) != usageLogRequestTextKeySize {
		return fmt.Errorf("usage log request_text master key must be %d bytes, got %d", usageLogRequestTextKeySize, len(key))
	}
	cloned := append([]byte(nil), key...)
	db.usageLogRequestTextMasterKey.Store(cloned)
	return nil
}

func (db *DB) usageLogRequestTextMasterKeyBytes() []byte {
	if db == nil {
		return nil
	}
	raw := db.usageLogRequestTextMasterKey.Load()
	if raw == nil {
		return nil
	}
	key, _ := raw.([]byte)
	if len(key) == 0 {
		return nil
	}
	return append([]byte(nil), key...)
}

func (db *DB) protectUsageLogEntryForStorage(entry *usageLogEntry) error {
	if db == nil || entry == nil || strings.TrimSpace(entry.RequestText) == "" {
		return nil
	}
	encrypted, err := encryptUsageLogRequestText(
		db.usageLogRequestTextMasterKeyBytes(),
		usageLogRequestTextIdentity(entry),
		entry.RequestText,
	)
	if err != nil {
		return err
	}
	entry.RequestText = encrypted
	return nil
}

func usageLogRequestTextIdentity(entry *usageLogEntry) string {
	if entry == nil {
		return "anonymous"
	}
	switch {
	case entry.APIKeyID > 0:
		return fmt.Sprintf("api-key:%d", entry.APIKeyID)
	case entry.AccountID > 0:
		return fmt.Sprintf("account:%d", entry.AccountID)
	default:
		return "anonymous"
	}
}

func encryptUsageLogRequestText(masterKey []byte, identity string, plaintext string) (string, error) {
	if strings.TrimSpace(plaintext) == "" || len(masterKey) == 0 {
		return plaintext, nil
	}
	if len(masterKey) != usageLogRequestTextKeySize {
		return "", fmt.Errorf("invalid usage log master key length: %d", len(masterKey))
	}
	if identity == "" {
		identity = "anonymous"
	}
	key, err := deriveUsageLogRequestTextKey(masterKey, identity)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create aes-gcm cipher: %w", err)
	}
	nonce := make([]byte, usageLogRequestTextNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), usageLogRequestTextAAD)
	payload := append(nonce, ciphertext...)
	return usageLogRequestTextCipherPrefix + base64.StdEncoding.EncodeToString(payload), nil
}

func decryptUsageLogRequestText(masterKey []byte, identity string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(masterKey) == 0 {
		return value, nil
	}
	if len(masterKey) != usageLogRequestTextKeySize {
		return "", fmt.Errorf("invalid usage log master key length: %d", len(masterKey))
	}
	if !strings.HasPrefix(value, usageLogRequestTextCipherPrefix) {
		return value, nil
	}
	if identity == "" {
		identity = "anonymous"
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, usageLogRequestTextCipherPrefix))
	if err != nil {
		return "", fmt.Errorf("decode request_text payload: %w", err)
	}
	key, err := deriveUsageLogRequestTextKey(masterKey, identity)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create aes-gcm cipher: %w", err)
	}
	if len(payload) < usageLogRequestTextNonceSize {
		return "", errors.New("request_text payload too short")
	}
	nonce := payload[:usageLogRequestTextNonceSize]
	ciphertext := payload[usageLogRequestTextNonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, usageLogRequestTextAAD)
	if err != nil {
		return "", fmt.Errorf("decrypt request_text payload: %w", err)
	}
	return string(plaintext), nil
}

func deriveUsageLogRequestTextKey(masterKey []byte, identity string) ([]byte, error) {
	info := []byte("codex2api:usage-log:request-text:" + identity)
	reader := hkdf.New(sha256.New, masterKey, nil, info)
	key := make([]byte, usageLogRequestTextKeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive usage log key: %w", err)
	}
	return key, nil
}
