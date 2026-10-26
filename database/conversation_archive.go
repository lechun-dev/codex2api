package database

// 会话存档的查询与保留策略。
//
// 存档复用 conversation_records 表：上游已经删掉了这套实现（含 DDL），fork 保留了
// 三方言建表语句。归属人不在存档里做快照，而是在查询时 LEFT JOIN api_keys 取
// owner_email —— 这样先在 api_keys 里维护好邮箱，历史存档立刻就能按人查到，
// 不需要回填或重新采集。
//
// 只保存文本：图片、附件、工具调用与思考内容不入库；超长内容截断并标记 truncated。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ConversationArchiveFilter 是存档列表查询条件。零值表示不过滤。
type ConversationArchiveFilter struct {
	APIKeyID   int64
	OwnerEmail string
	ClientID   string
	SessionID  string
	RequestID  string
	Model      string
	Endpoint   string
	Status     string
	StatusCode int
	Stream     *bool
	Truncated  *bool
	Keyword    string
	From       time.Time
	To         time.Time
	Order      string // "asc" | "desc"，默认 desc
	Offset     int
	Limit      int
}

func (f ConversationArchiveFilter) normalized() ConversationArchiveFilter {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if strings.ToLower(strings.TrimSpace(f.Order)) == "asc" {
		f.Order = "asc"
	} else {
		f.Order = "desc"
	}
	f.OwnerEmail = strings.TrimSpace(f.OwnerEmail)
	f.ClientID = strings.TrimSpace(f.ClientID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.RequestID = strings.TrimSpace(f.RequestID)
	f.Model = strings.TrimSpace(f.Model)
	f.Endpoint = strings.TrimSpace(f.Endpoint)
	f.Status = strings.TrimSpace(f.Status)
	f.Keyword = strings.TrimSpace(f.Keyword)
	return f
}

// EnsureConversationArchiveSchema 为存量库补列/补索引，可重复执行。
// 只补采集侧新增的 stream/truncated；归属人来自 api_keys.owner_email。
func (db *DB) EnsureConversationArchiveSchema(ctx context.Context) error {
	if db == nil || db.conn == nil {
		return nil
	}
	switch {
	case db.isSQLite():
		if err := db.ensureSQLiteColumn(ctx, "conversation_records", "stream", "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		return db.ensureSQLiteColumn(ctx, "conversation_records", "truncated", "INTEGER NOT NULL DEFAULT 0")
	case db.isMySQL():
		if err := db.ensureMySQLColumn(ctx, "conversation_records", "stream", "TINYINT(1) NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		return db.ensureMySQLColumn(ctx, "conversation_records", "truncated", "TINYINT(1) NOT NULL DEFAULT 0")
	default:
		for _, col := range []struct{ name, def string }{
			{"stream", "BOOLEAN NOT NULL DEFAULT FALSE"},
			{"truncated", "BOOLEAN NOT NULL DEFAULT FALSE"},
		} {
			if _, err := db.conn.ExecContext(ctx, fmt.Sprintf(
				"ALTER TABLE conversation_records ADD COLUMN IF NOT EXISTS %s %s", col.name, col.def)); err != nil {
				return err
			}
		}
		return nil
	}
}

// conversationArchiveWhere 按方言拼装过滤条件（r=conversation_records，k=api_keys），
// 使用 $n 占位符；MySQL 由 driver 层重写成 ?。
func (db *DB) conversationArchiveWhere(f ConversationArchiveFilter) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	add := func(expr string, value interface{}) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expr, "$"+fmt.Sprint(len(args))))
	}
	if f.APIKeyID > 0 {
		add("r.api_key_id=%s", f.APIKeyID)
	}
	if f.OwnerEmail != "" {
		add("LOWER(COALESCE(k.owner_email,''))=LOWER(%s)", f.OwnerEmail)
	}
	if f.ClientID != "" {
		add("r.client_id=%s", f.ClientID)
	}
	if f.SessionID != "" {
		add("r.session_id=%s", f.SessionID)
	}
	if f.RequestID != "" {
		add("r.request_id=%s", f.RequestID)
	}
	if f.Model != "" {
		add("LOWER(r.model)=LOWER(%s)", f.Model)
	}
	if f.Endpoint != "" {
		add("r.endpoint=%s", f.Endpoint)
	}
	if f.Status != "" {
		add("r.status=%s", f.Status)
	}
	if f.StatusCode > 0 {
		add("r.status_code=%s", f.StatusCode)
	}
	if f.Stream != nil {
		add("r.stream=%s", *f.Stream)
	}
	if f.Truncated != nil {
		add("r.truncated=%s", *f.Truncated)
	}
	if !f.From.IsZero() {
		add("r.created_at>=%s", db.timeArg(f.From))
	}
	if !f.To.IsZero() {
		add("r.created_at<=%s", db.timeArg(f.To))
	}
	if f.Keyword != "" {
		args = append(args, "%"+f.Keyword+"%")
		placeholder := "$" + fmt.Sprint(len(args))
		clauses = append(clauses, fmt.Sprintf(
			"(LOWER(COALESCE(r.user_message,'')) LIKE LOWER(%s) OR LOWER(COALESCE(r.assistant_message,'')) LIKE LOWER(%s))",
			placeholder, placeholder))
	}
	return strings.Join(clauses, " AND "), args
}

const conversationArchiveFrom = ` FROM conversation_records r LEFT JOIN api_keys k ON k.id=r.api_key_id `

// ListConversationArchives 返回存档列表与总数；includeContent=false 时不返回正文。
func (db *DB) ListConversationArchives(ctx context.Context, f ConversationArchiveFilter, includeContent bool) ([]ConversationRecord, int, error) {
	if db == nil || db.conn == nil {
		return nil, 0, nil
	}
	f = f.normalized()
	where, args := db.conversationArchiveWhere(f)
	var total int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*)`+conversationArchiveFrom+` WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT ` + conversationArchiveColumns(includeContent) + conversationArchiveFrom + ` WHERE ` + where +
		` ORDER BY r.id ` + strings.ToUpper(f.Order) +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := db.conn.QueryContext(ctx, query, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ConversationRecord{}
	for rows.Next() {
		record, scanErr := scanConversationArchive(rows, includeContent)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		out = append(out, *record)
	}
	return out, total, rows.Err()
}

// GetConversationArchive 按 id 返回完整存档（含正文）。
func (db *DB) GetConversationArchive(ctx context.Context, id int64) (*ConversationRecord, error) {
	if db == nil || db.conn == nil || id <= 0 {
		return nil, sql.ErrNoRows
	}
	row := db.conn.QueryRowContext(ctx,
		`SELECT `+conversationArchiveColumns(true)+conversationArchiveFrom+` WHERE r.id=$1`, id)
	return scanConversationArchive(row, true)
}

// PurgeConversationArchivesBefore 删除 cutoff 之前的存档，返回删除行数。
// 分批删除，避免一次锁住大表。
func (db *DB) PurgeConversationArchivesBefore(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	if db == nil || db.conn == nil || cutoff.IsZero() {
		return 0, nil
	}
	if batch <= 0 {
		batch = 2000
	}
	var removed int64
	for {
		var res sql.Result
		var err error
		if db.isMySQL() {
			// MySQL 5.6 不支持「IN 子查询里带 LIMIT」，用单表 LIMIT。
			res, err = db.conn.ExecContext(ctx,
				`DELETE FROM conversation_records WHERE created_at < $1 ORDER BY id LIMIT $2`, db.timeArg(cutoff), batch)
		} else {
			res, err = db.conn.ExecContext(ctx, `
				DELETE FROM conversation_records WHERE id IN (
					SELECT id FROM conversation_records WHERE created_at < $1 ORDER BY id LIMIT $2
				)`, db.timeArg(cutoff), batch)
		}
		if err != nil {
			return removed, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += n
		if n < int64(batch) {
			return removed, nil
		}
	}
}

func conversationArchiveColumns(includeContent bool) string {
	owner := `COALESCE(k.owner_email,'') AS owner_email`
	if includeContent {
		return `r.id,r.request_id,r.session_id,r.api_key_id,r.api_key_name,r.client_id,r.client_ip,
			r.response_id,r.previous_response_id,r.endpoint,r.model,r.user_message,r.assistant_message,
			r.status,r.status_code,r.input_tokens,r.output_tokens,r.duration_ms,r.created_at,r.updated_at,
			r.completed_at,r.stream,r.truncated,` + owner
	}
	return `r.id,r.request_id,r.session_id,r.api_key_id,r.api_key_name,r.client_id,r.client_ip,
		r.response_id,r.previous_response_id,r.endpoint,r.model,r.status,r.status_code,
		r.input_tokens,r.output_tokens,r.duration_ms,r.created_at,r.updated_at,r.completed_at,
		r.stream,r.truncated,
		LENGTH(COALESCE(r.user_message,'')) AS user_message_len,
		LENGTH(COALESCE(r.assistant_message,'')) AS assistant_message_len,` + owner
}

func scanConversationArchive(row interface{ Scan(...interface{}) error }, includeContent bool) (*ConversationRecord, error) {
	record := &ConversationRecord{}
	var updated time.Time
	var completed sql.NullTime
	var responseID, previousResponseID sql.NullString
	if includeContent {
		if err := row.Scan(
			&record.ID, &record.RequestID, &record.SessionID, &record.APIKeyID, &record.APIKeyName,
			&record.ClientID, &record.ClientIP, &responseID, &previousResponseID, &record.Endpoint,
			&record.Model, &record.UserMessage, &record.AssistantMessage, &record.Status,
			&record.StatusCode, &record.InputTokens, &record.OutputTokens, &record.DurationMs,
			&record.CreatedAt, &updated, &completed, &record.Stream, &record.Truncated, &record.OwnerEmail,
		); err != nil {
			return nil, err
		}
	} else {
		var userLen, assistantLen sql.NullInt64
		if err := row.Scan(
			&record.ID, &record.RequestID, &record.SessionID, &record.APIKeyID, &record.APIKeyName,
			&record.ClientID, &record.ClientIP, &responseID, &previousResponseID, &record.Endpoint,
			&record.Model, &record.Status, &record.StatusCode, &record.InputTokens, &record.OutputTokens,
			&record.DurationMs, &record.CreatedAt, &updated, &completed, &record.Stream, &record.Truncated,
			&userLen, &assistantLen, &record.OwnerEmail,
		); err != nil {
			return nil, err
		}
		record.UserMessageLen = int(userLen.Int64)
		record.AssistantMessageLen = int(assistantLen.Int64)
	}
	record.ResponseID = responseID.String
	record.PreviousResponseID = previousResponseID.String
	record.UpdatedAt = updated
	if completed.Valid {
		record.CompletedAt = &completed.Time
	}
	return record, nil
}
