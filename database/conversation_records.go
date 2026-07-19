package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	ConversationStatusCompleted  = "completed"
	ConversationStatusPartial    = "partial"
	ConversationStatusFailed     = "failed"
	ConversationStatusCanceled   = "canceled"
	ConversationStatusIncomplete = "incomplete"
)

// ConversationRecordInput is a complete snapshot of one user interaction.
// Tool-continuation requests reuse RequestID and replace the stored snapshot.
type ConversationRecordInput struct {
	RequestID          string
	SessionID          string
	APIKeyID           int64
	APIKeyName         string
	ClientID           string
	ClientIP           string
	ResponseID         string
	PreviousResponseID string
	Endpoint           string
	Model              string
	UserMessage        string
	AssistantMessage   string
	Status             string
	StatusCode         int
	InputTokens        int
	OutputTokens       int
	DurationMs         int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type ConversationRecord struct {
	ID                 int64      `json:"id"`
	RequestID          string     `json:"request_id"`
	SessionID          string     `json:"session_id"`
	APIKeyID           int64      `json:"api_key_id"`
	APIKeyName         string     `json:"api_key_name"`
	ClientID           string     `json:"client_id"`
	ClientIP           string     `json:"client_ip"`
	ResponseID         string     `json:"response_id"`
	PreviousResponseID string     `json:"previous_response_id"`
	Endpoint           string     `json:"endpoint"`
	Model              string     `json:"model"`
	UserMessage        string     `json:"user_message"`
	AssistantMessage   string     `json:"assistant_message"`
	Status             string     `json:"status"`
	StatusCode         int        `json:"status_code"`
	InputTokens        int        `json:"input_tokens"`
	OutputTokens       int        `json:"output_tokens"`
	DurationMs         int        `json:"duration_ms"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

const conversationRecordsPostgresDDL = `
CREATE TABLE IF NOT EXISTS conversation_records (
	id                   BIGSERIAL PRIMARY KEY,
	request_id           VARCHAR(64) NOT NULL UNIQUE,
	session_id           VARCHAR(255) NOT NULL,
	api_key_id           BIGINT NOT NULL DEFAULT 0,
	api_key_name         VARCHAR(255) NOT NULL DEFAULT '',
	client_id            VARCHAR(128) NOT NULL DEFAULT '',
	client_ip            VARCHAR(64) NOT NULL DEFAULT '',
	response_id          VARCHAR(255),
	previous_response_id VARCHAR(255),
	endpoint             VARCHAR(100) NOT NULL DEFAULT '',
	model                VARCHAR(100) NOT NULL DEFAULT '',
	user_message         TEXT,
	assistant_message    TEXT,
	status               VARCHAR(16) NOT NULL DEFAULT 'completed',
	status_code          INT NOT NULL DEFAULT 0,
	input_tokens         INT NOT NULL DEFAULT 0,
	output_tokens        INT NOT NULL DEFAULT 0,
	duration_ms          INT NOT NULL DEFAULT 0,
	created_at           TIMESTAMP NOT NULL,
	updated_at           TIMESTAMP NOT NULL,
	completed_at         TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_conversation_session_created
	ON conversation_records(api_key_id, session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_user_created
	ON conversation_records(api_key_id, client_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_created_at
	ON conversation_records(created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_previous_response
	ON conversation_records(api_key_id, previous_response_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_conversation_response
	ON conversation_records(api_key_id, response_id);
`

const conversationRecordsSQLiteDDL = `
CREATE TABLE IF NOT EXISTS conversation_records (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id           TEXT NOT NULL UNIQUE,
	session_id           TEXT NOT NULL,
	api_key_id           INTEGER NOT NULL DEFAULT 0,
	api_key_name         TEXT NOT NULL DEFAULT '',
	client_id            TEXT NOT NULL DEFAULT '',
	client_ip            TEXT NOT NULL DEFAULT '',
	response_id          TEXT,
	previous_response_id TEXT,
	endpoint             TEXT NOT NULL DEFAULT '',
	model                TEXT NOT NULL DEFAULT '',
	user_message         TEXT,
	assistant_message    TEXT,
	status               TEXT NOT NULL DEFAULT 'completed',
	status_code          INTEGER NOT NULL DEFAULT 0,
	input_tokens         INTEGER NOT NULL DEFAULT 0,
	output_tokens        INTEGER NOT NULL DEFAULT 0,
	duration_ms          INTEGER NOT NULL DEFAULT 0,
	created_at           DATETIME NOT NULL,
	updated_at           DATETIME NOT NULL,
	completed_at         DATETIME
);
CREATE INDEX IF NOT EXISTS idx_conversation_session_created
	ON conversation_records(api_key_id, session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_user_created
	ON conversation_records(api_key_id, client_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_created_at
	ON conversation_records(created_at);
CREATE INDEX IF NOT EXISTS idx_conversation_previous_response
	ON conversation_records(api_key_id, previous_response_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_conversation_response
	ON conversation_records(api_key_id, response_id);
`

// MySQL 5.6 has no JSON type requirement here and LONGTEXT columns deliberately
// have no DEFAULT clause. Indexed identifiers use ASCII to stay below old
// InnoDB index-length limits even when the table default charset changes.
const conversationRecordsMySQLDDL = `
CREATE TABLE IF NOT EXISTS conversation_records (
	id                   BIGINT NOT NULL AUTO_INCREMENT,
	request_id           VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
	session_id           VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
	api_key_id           BIGINT NOT NULL DEFAULT 0,
	api_key_name         VARCHAR(255) NOT NULL DEFAULT '',
	client_id            VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
	client_ip            VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
	response_id          VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
	previous_response_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
	endpoint             VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
	model                VARCHAR(100) NOT NULL DEFAULT '',
	user_message         LONGTEXT NULL,
	assistant_message    LONGTEXT NULL,
	status               VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'completed',
	status_code          INT NOT NULL DEFAULT 0,
	input_tokens         INT NOT NULL DEFAULT 0,
	output_tokens        INT NOT NULL DEFAULT 0,
	duration_ms          INT NOT NULL DEFAULT 0,
	created_at           DATETIME NOT NULL,
	updated_at           DATETIME NOT NULL,
	completed_at         DATETIME NULL,
	PRIMARY KEY (id),
	UNIQUE KEY uk_conversation_request (request_id),
	UNIQUE KEY uk_conversation_response (api_key_id, response_id),
	KEY idx_conversation_session_created (api_key_id, session_id, created_at),
	KEY idx_conversation_user_created (api_key_id, client_id, created_at),
	KEY idx_conversation_created_at (created_at),
	KEY idx_conversation_previous_response (api_key_id, previous_response_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;
`

func (db *DB) migrateConversationRecords(ctx context.Context) error {
	if db == nil {
		return nil
	}
	ddl := conversationRecordsPostgresDDL
	if db.isSQLite() {
		ddl = conversationRecordsSQLiteDDL
	} else if db.isMySQL() {
		ddl = conversationRecordsMySQLDDL
	}
	_, err := db.conn.ExecContext(ctx, ddl)
	return err
}

// EnsureConversationRecords creates the optional conversation-recording table.
// Callers invoke it only when plaintext conversation recording is enabled.
func (db *DB) EnsureConversationRecords(ctx context.Context) error {
	return db.migrateConversationRecords(ctx)
}

func normalizeConversationStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ConversationStatusCompleted:
		return ConversationStatusCompleted
	case ConversationStatusPartial:
		return ConversationStatusPartial
	case ConversationStatusFailed:
		return ConversationStatusFailed
	case ConversationStatusCanceled:
		return ConversationStatusCanceled
	case ConversationStatusIncomplete:
		return ConversationStatusIncomplete
	default:
		return ConversationStatusCompleted
	}
}

func (db *DB) normalizeConversationRecordInput(input ConversationRecordInput) ConversationRecordInput {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.APIKeyName = strings.TrimSpace(input.APIKeyName)
	input.ClientID = strings.TrimSpace(input.ClientID)
	input.ClientIP = strings.TrimSpace(input.ClientIP)
	input.ResponseID = strings.TrimSpace(input.ResponseID)
	input.PreviousResponseID = strings.TrimSpace(input.PreviousResponseID)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	input.Model = strings.TrimSpace(input.Model)
	input.Status = normalizeConversationStatus(input.Status)
	now := time.Now().UTC()
	if input.CreatedAt.IsZero() {
		input.CreatedAt = now
	}
	if input.UpdatedAt.IsZero() {
		input.UpdatedAt = now
	}
	if db != nil && db.isMySQL() {
		input.RequestID = sanitizeMySQLTextValue(input.RequestID)
		input.SessionID = sanitizeMySQLTextValue(input.SessionID)
		input.APIKeyName = sanitizeMySQLTextValue(input.APIKeyName)
		input.ClientID = sanitizeMySQLTextValue(input.ClientID)
		input.ClientIP = sanitizeMySQLTextValue(input.ClientIP)
		input.ResponseID = sanitizeMySQLTextValue(input.ResponseID)
		input.PreviousResponseID = sanitizeMySQLTextValue(input.PreviousResponseID)
		input.Endpoint = sanitizeMySQLTextValue(input.Endpoint)
		input.Model = sanitizeMySQLTextValue(input.Model)
		input.UserMessage = sanitizeMySQLTextValue(input.UserMessage)
		input.AssistantMessage = sanitizeMySQLTextValue(input.AssistantMessage)
	}
	return input
}

func nullableConversationString(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// SaveConversationRecord stores a full interaction snapshot. Reusing request_id
// updates the same row, which is how tool-continuation requests are folded into
// their originating user interaction.
func (db *DB) SaveConversationRecord(ctx context.Context, input ConversationRecordInput) error {
	if db == nil {
		return nil
	}
	input = db.normalizeConversationRecordInput(input)
	if input.RequestID == "" || input.SessionID == "" {
		return fmt.Errorf("request_id and session_id are required")
	}

	args := []interface{}{
		input.SessionID,
		input.APIKeyID,
		input.APIKeyName,
		input.ClientID,
		input.ClientIP,
		nullableConversationString(input.ResponseID),
		nullableConversationString(input.PreviousResponseID),
		input.Endpoint,
		input.Model,
		input.UserMessage,
		input.AssistantMessage,
		input.Status,
		input.StatusCode,
		input.InputTokens,
		input.OutputTokens,
		input.DurationMs,
		db.timeArg(input.CreatedAt),
		db.timeArg(input.UpdatedAt),
		nil,
		input.RequestID,
	}
	if input.CompletedAt != nil && !input.CompletedAt.IsZero() {
		args[18] = db.timeArg(*input.CompletedAt)
	}

	result, err := db.conn.ExecContext(ctx, `
		UPDATE conversation_records SET
			session_id=$1,
			api_key_id=$2,
			api_key_name=$3,
			client_id=$4,
			client_ip=$5,
			response_id=$6,
			previous_response_id=$7,
			endpoint=$8,
			model=$9,
			user_message=$10,
			assistant_message=$11,
			status=$12,
			status_code=$13,
			input_tokens=$14,
			output_tokens=$15,
			duration_ms=$16,
			created_at=$17,
			updated_at=$18,
			completed_at=$19
		WHERE request_id=$20
	`, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected > 0 {
		return nil
	}

	_, err = db.conn.ExecContext(ctx, `
		INSERT INTO conversation_records (
			request_id, session_id, api_key_id, api_key_name, client_id, client_ip,
			response_id, previous_response_id, endpoint, model,
			user_message, assistant_message, status, status_code,
			input_tokens, output_tokens, duration_ms,
			created_at, updated_at, completed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14,
			$15, $16, $17,
			$18, $19, $20
		)
	`,
		input.RequestID,
		input.SessionID,
		input.APIKeyID,
		input.APIKeyName,
		input.ClientID,
		input.ClientIP,
		nullableConversationString(input.ResponseID),
		nullableConversationString(input.PreviousResponseID),
		input.Endpoint,
		input.Model,
		input.UserMessage,
		input.AssistantMessage,
		input.Status,
		input.StatusCode,
		input.InputTokens,
		input.OutputTokens,
		input.DurationMs,
		db.timeArg(input.CreatedAt),
		db.timeArg(input.UpdatedAt),
		args[18],
	)
	if err == nil {
		return nil
	}

	// A second process may have inserted the same request_id between UPDATE and
	// INSERT. Retry the UPDATE once instead of surfacing a harmless duplicate.
	retryResult, retryErr := db.conn.ExecContext(ctx, `
			UPDATE conversation_records SET
				session_id=$1, api_key_id=$2, api_key_name=$3, client_id=$4, client_ip=$5,
				response_id=$6, previous_response_id=$7, endpoint=$8, model=$9,
			user_message=$10, assistant_message=$11, status=$12, status_code=$13,
			input_tokens=$14, output_tokens=$15, duration_ms=$16,
			created_at=$17, updated_at=$18, completed_at=$19
		WHERE request_id=$20
	`, args...)
	if retryErr != nil {
		return err
	}
	retryAffected, retryAffectedErr := retryResult.RowsAffected()
	if retryAffectedErr == nil && retryAffected > 0 {
		return nil
	}

	// MySQL reports zero affected rows when an existing row already contains the
	// same values. Confirm that request_id really exists before treating the
	// failed INSERT as a harmless concurrent/idempotent write.
	var existing int
	existsErr := db.conn.QueryRowContext(ctx,
		`SELECT 1 FROM conversation_records WHERE request_id=$1 LIMIT 1`,
		input.RequestID,
	).Scan(&existing)
	if existsErr == nil {
		return nil
	}
	return err
}

func scanConversationRecord(scanner interface {
	Scan(dest ...interface{}) error
}) (*ConversationRecord, error) {
	record := &ConversationRecord{}
	var responseID sql.NullString
	var previousResponseID sql.NullString
	var createdRaw interface{}
	var updatedRaw interface{}
	var completedRaw interface{}
	if err := scanner.Scan(
		&record.ID,
		&record.RequestID,
		&record.SessionID,
		&record.APIKeyID,
		&record.APIKeyName,
		&record.ClientID,
		&record.ClientIP,
		&responseID,
		&previousResponseID,
		&record.Endpoint,
		&record.Model,
		&record.UserMessage,
		&record.AssistantMessage,
		&record.Status,
		&record.StatusCode,
		&record.InputTokens,
		&record.OutputTokens,
		&record.DurationMs,
		&createdRaw,
		&updatedRaw,
		&completedRaw,
	); err != nil {
		return nil, err
	}
	record.ResponseID = responseID.String
	record.PreviousResponseID = previousResponseID.String
	var err error
	record.CreatedAt, err = parseDBTimeValue(createdRaw)
	if err != nil {
		return nil, err
	}
	record.UpdatedAt, err = parseDBTimeValue(updatedRaw)
	if err != nil {
		return nil, err
	}
	completedAt, err := parseDBNullTimeValue(completedRaw)
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		t := completedAt.Time
		record.CompletedAt = &t
	}
	return record, nil
}

const conversationRecordSelect = `
	SELECT
		id, request_id, session_id, api_key_id, api_key_name, client_id, client_ip,
		response_id, previous_response_id, endpoint, model,
		COALESCE(user_message, ''), COALESCE(assistant_message, ''),
		status, status_code, input_tokens, output_tokens, duration_ms,
		created_at, updated_at, completed_at
	FROM conversation_records
`

func (db *DB) FindConversationByResponseID(ctx context.Context, apiKeyID int64, responseID string) (*ConversationRecord, error) {
	responseID = strings.TrimSpace(responseID)
	if db == nil || responseID == "" {
		return nil, nil
	}
	record, err := scanConversationRecord(db.conn.QueryRowContext(ctx,
		conversationRecordSelect+`
		WHERE api_key_id=$1 AND (response_id=$2 OR previous_response_id=$2)
		ORDER BY id DESC LIMIT 1
	`, apiKeyID, responseID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}

// FindLatestPendingConversation finds the interaction that should receive a
// tool continuation. A completed row without assistant text is included
// because some Responses variants omit enough tool metadata to mark the
// intermediate turn partial.
func (db *DB) FindLatestPendingConversation(ctx context.Context, apiKeyID int64, sessionID, clientID string) (*ConversationRecord, error) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	query := conversationRecordSelect + `
		WHERE api_key_id=$1 AND session_id=$2
		  AND (status=$3 OR (status=$4 AND COALESCE(assistant_message, '')=''))
	`
	args := []interface{}{apiKeyID, sessionID, ConversationStatusPartial, ConversationStatusCompleted}
	if strings.TrimSpace(clientID) != "" {
		query += ` AND client_id=$5`
		args = append(args, clientID)
	}
	query += ` ORDER BY id DESC LIMIT 1`
	record, err := scanConversationRecord(db.conn.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}
