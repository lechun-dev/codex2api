package database

// MySQL 5.6 版本的「账号智能检查」存储实现。
//
// 上游这份功能是按 PostgreSQL / SQLite 写的：SQL 里直接解析 JSON
// （`state_json::jsonb->>'action'`、`json_extract(...)`）、用窗口函数
// （`COUNT(*) OVER ()`）、部分索引（`... WHERE status IN (...)`）、
// `ADD COLUMN IF NOT EXISTS`、`RETURNING id` 以及 PL/pgSQL 触发器。
// MySQL 5.6 一样都不支持：没有 JSON 函数、没有窗口函数、没有部分索引，
// TEXT 列不能带 DEFAULT，也没有 RETURNING 和 PG 方言的触发器。
//
// 所以这里的策略是「把 JSON 里的判断字段落成真实列」：
//   - intelligence_check_accounts.state_action  ← state_json 里的 action
//   - intelligence_check_runs.source / account_name / channel ← run_json 同名对象
//   - intelligence_check_runs.question_name / duration_ms     ← 上游已有摘要列
// 写入时在 Go 里同步这些列，查询时 MySQL 分支改用列而不是 JSON 表达式。
// PostgreSQL / SQLite 仍然走原来的 JSON 表达式，行为不变。
//
// 新账号自动入组在 MySQL 上不用触发器（触发器里没法解析 JSON），改由调度器
// 每次 tick 前补插缺失行，等价于上游触发器「新账号按设置自动 opt-in」的语义。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	ic "github.com/codex2api/internal/intelligencecheck"
)

// intelligenceCheckMySQLTables 是 MySQL 5.6 的建表语句（供测试做语法断言）。
func intelligenceCheckMySQLTables() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS intelligence_check_settings (
			id INT NOT NULL PRIMARY KEY,
			revision BIGINT NOT NULL,
			config_json MEDIUMTEXT NOT NULL,
			new_accounts_enabled TINYINT(1) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		// state_json 用 MEDIUMTEXT 且允许 NULL：MySQL 5.6 不允许 TEXT 带 DEFAULT。
		`CREATE TABLE IF NOT EXISTS intelligence_check_accounts (
			account_id BIGINT NOT NULL PRIMARY KEY,
			enabled TINYINT(1) NOT NULL DEFAULT 0,
			quality_paused TINYINT(1) NOT NULL DEFAULT 0,
			manual_revision BIGINT NOT NULL DEFAULT 0,
			state_json MEDIUMTEXT NOT NULL,
			state_action VARCHAR(32) NOT NULL DEFAULT '',
			next_due_ms BIGINT NOT NULL DEFAULT 0,
			question_cursor INT NOT NULL DEFAULT 0,
			status VARCHAR(32) NOT NULL DEFAULT 'unknown'
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS intelligence_check_runs (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			account_id BIGINT NOT NULL,
			status VARCHAR(32) NOT NULL,
			run_json MEDIUMTEXT NOT NULL,
			created_ms BIGINT NOT NULL,
			ready_ms BIGINT NOT NULL,
			completed_ms BIGINT NOT NULL DEFAULT 0,
			lease_owner VARCHAR(255) NOT NULL DEFAULT '',
			lease_until_ms BIGINT NOT NULL DEFAULT 0,
			question_name VARCHAR(255) NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			summary_version INT NOT NULL DEFAULT 0,
			source VARCHAR(16) NOT NULL DEFAULT '',
			account_name VARCHAR(255) NOT NULL DEFAULT '',
			channel VARCHAR(32) NOT NULL DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
	}
}

// intelligenceCheckMySQLIndexes 是幂等建索引语句（MySQL 5.6 没有部分索引）。
func intelligenceCheckMySQLIndexes() []struct {
	table, name string
	stmt        string
} {
	return []struct {
		table, name string
		stmt        string
	}{
		{"intelligence_check_accounts", "idx_intelligence_due", "CREATE INDEX idx_intelligence_due ON intelligence_check_accounts(enabled,next_due_ms,account_id)"},
		{"intelligence_check_runs", "idx_intelligence_queue", "CREATE INDEX idx_intelligence_queue ON intelligence_check_runs(status,ready_ms,id)"},
		{"intelligence_check_runs", "idx_intelligence_history", "CREATE INDEX idx_intelligence_history ON intelligence_check_runs(account_id,id)"},
		// 上游用部分唯一索引保证「每个账号最多一条活跃 run」；5.6 没有部分索引，
		// 这里退回普通索引，唯一性由 intelligenceEnqueueTx 的事务内查重保证。
		{"intelligence_check_runs", "idx_intelligence_one_active", "CREATE INDEX idx_intelligence_one_active ON intelligence_check_runs(account_id,status)"},
		{"intelligence_check_runs", "idx_intelligence_bars", "CREATE INDEX idx_intelligence_bars ON intelligence_check_runs(account_id,id,status)"},
	}
}

// ensureIntelligenceCheckSchemaMySQL 建表、补列、建索引，全部幂等。
func (db *DB) ensureIntelligenceCheckSchemaMySQL(ctx context.Context) error {
	for _, q := range intelligenceCheckMySQLTables() {
		if _, err := db.conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// 升级路径：MySQL 5.6 没有 ADD COLUMN IF NOT EXISTS，按列名判断后补。
	columns := []struct{ table, name, def string }{
		{"intelligence_check_accounts", "state_action", "VARCHAR(32) NOT NULL DEFAULT ''"},
		{"intelligence_check_runs", "question_name", "VARCHAR(255) NOT NULL DEFAULT ''"},
		{"intelligence_check_runs", "duration_ms", "BIGINT NOT NULL DEFAULT 0"},
		{"intelligence_check_runs", "summary_version", "INT NOT NULL DEFAULT 0"},
		{"intelligence_check_runs", "source", "VARCHAR(16) NOT NULL DEFAULT ''"},
		{"intelligence_check_runs", "account_name", "VARCHAR(255) NOT NULL DEFAULT ''"},
		{"intelligence_check_runs", "channel", "VARCHAR(32) NOT NULL DEFAULT ''"},
	}
	for _, col := range columns {
		if err := db.ensureMySQLColumn(ctx, col.table, col.name, col.def); err != nil {
			return err
		}
	}
	for _, idx := range intelligenceCheckMySQLIndexes() {
		if err := db.ensureMySQLIndex(ctx, idx.table, idx.name, idx.stmt); err != nil {
			return err
		}
	}
	// 幂等写入默认设置行：MySQL 用 INSERT IGNORE 代替 ON CONFLICT DO NOTHING。
	defaults := ic.DefaultSettings()
	raw, _ := json.Marshal(defaults)
	if _, err := db.conn.ExecContext(ctx,
		`INSERT IGNORE INTO intelligence_check_settings(id,revision,config_json,new_accounts_enabled) VALUES(1,?,?,?)`,
		defaults.Revision, string(raw), defaults.NewAccountsEnabled,
	); err != nil {
		return err
	}
	// 存量账号一律先按 opt-out 建档，保留上游「老账号默认不参与」的语义。
	if _, err := db.conn.ExecContext(ctx,
		`INSERT IGNORE INTO intelligence_check_accounts(account_id,enabled,state_json) SELECT id,0,'{}' FROM accounts`,
	); err != nil {
		return err
	}
	return db.backfillIntelligenceRunSummariesMySQL(ctx)
}

// backfillIntelligenceRunSummariesMySQL 用 Go 侧解析 run_json 回填摘要列，
// 替代上游在 SQL 里用 json_extract / ->> 的那条 UPDATE。
func (db *DB) backfillIntelligenceRunSummariesMySQL(ctx context.Context) error {
	rows, err := db.conn.QueryContext(ctx,
		`SELECT id,COALESCE(run_json,'') FROM intelligence_check_runs WHERE summary_version=0 AND status NOT IN ('queued','running') ORDER BY id LIMIT 2000`)
	if err != nil {
		return err
	}
	type pending struct {
		id        int64
		name      string
		duration  int64
		runJSON   string
		account   int64
		channel   string
		source    string
		accountID int64
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.runJSON); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(items) == 0 {
		return err
	}
	for _, p := range items {
		var r IntelligenceCheckRun
		if p.runJSON != "" {
			_ = json.Unmarshal([]byte(p.runJSON), &r)
		}
		if _, err := db.conn.ExecContext(ctx,
			`UPDATE intelligence_check_runs SET question_name=?,duration_ms=?,summary_version=1,source=?,account_name=?,channel=? WHERE id=?`,
			r.Question.Name, r.DurationMS, r.Source, r.AccountName, r.Channel, p.id,
		); err != nil {
			return err
		}
	}
	return nil
}

// intelligenceUpstreamExprMySQL 用正则从 credentials 文本里取 upstream_type：
// 5.6 没有 JSON 函数，沿用本仓库 accountUpstreamTypePredicate 的既有做法。
func (db *DB) intelligenceUpstreamExprMySQL(upstreamType string) string {
	// 生成：... REGEXP '"upstream_type"[[:space:]]*:[[:space:]]*"claude"'
	return `LOWER(COALESCE(CAST(a.credentials AS CHAR),'')) REGEXP '"upstream_type"[[:space:]]*:[[:space:]]*"` + upstreamType + `"'`
}

func (db *DB) intelligenceChannelExprMySQL() string {
	return `CASE WHEN (` + db.intelligenceUpstreamExprMySQL(UpstreamChannelClaude) + `) OR LOWER(COALESCE(a.platform,'')) IN ('anthropic','claude') THEN 'claude' ELSE 'codex' END`
}

func (db *DB) intelligenceEligibleSQLMySQL() string {
	return `a.status<>'deleted' AND COALESCE(a.error_message,'')<>'deleted' AND NOT (` +
		db.intelligenceUpstreamExprMySQL(UpstreamChannelGrok) + ` OR ` + db.intelligenceUpstreamExprMySQL(UpstreamChannelAntigravity) +
		`) AND LOWER(COALESCE(a.platform,'openai')) NOT IN ('xai','grok','antigravity','google')`
}

// enrollIntelligenceCheckAccountsMySQLTx 等价于上游的 AFTER INSERT 触发器：
// 把还没有档案、且符合设置的账号补进来。调度器每次 tick 调一次即可。
func (db *DB) enrollIntelligenceCheckAccountsMySQLTx(ctx context.Context, tx *sql.Tx, s ic.Settings, targetChannel string) error {
	if !s.NewAccountsEnabled {
		return nil
	}
	where := db.intelligenceEligibleSQLMySQL() + ` AND st.account_id IS NULL`
	if s.Action == "switch_group" {
		if strings.TrimSpace(targetChannel) == "" {
			return nil
		}
		where += ` AND ` + db.intelligenceChannelExprMySQL() + `='` + strings.ReplaceAll(targetChannel, "'", "''") + `'`
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id FROM accounts a LEFT JOIN intelligence_check_accounts st ON st.account_id=a.id WHERE `+where+` ORDER BY a.id LIMIT 500`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT IGNORE INTO intelligence_check_accounts(account_id,enabled,state_json) VALUES(?,1,'{}')`, id); err != nil {
			return err
		}
	}
	return nil
}

// intelligenceRunJSONFieldMySQL 把 run_json 里的字段映射到真实列。
// 未收录的字段返回空串，调用方应改走 Go 侧解析。
func intelligenceRunJSONFieldMySQL(field string) string {
	switch field {
	case "source":
		return "source"
	case "account_name":
		return "account_name"
	case "channel":
		return "channel"
	case "duration_ms":
		return "duration_ms"
	case "question.name":
		return "question_name"
	default:
		return ""
	}
}

// intelligenceStateActionValue 从 state 里取需要落成 state_action 列的归属动作。
func intelligenceStateActionValue(state ic.State) string {
	return strings.TrimSpace(state.OwnedAction)
}

// intelligenceTruncateForColumn 把可能很长的文本裁到 MySQL 列长度内（按 rune，
// 避免把多字节字符截断后写入 utf8 列失败）。
func intelligenceTruncateForColumn(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
