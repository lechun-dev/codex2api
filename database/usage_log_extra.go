package database

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// usageLogExtraTablePattern 限制额外表名只能是普通标识符，避免拼接 SQL 时被注入。
var usageLogExtraTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SetUsageLogExtraTables 记录需要并入用量统计的历史备份表（例如 usage_logs_openclaw）。
// 传空表示只用 usage_logs 一张表。写入永远只写 usage_logs，这些表只参与读取。
func (db *DB) SetUsageLogExtraTables(names []string) {
	if db == nil {
		return
	}
	normalized := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || !usageLogExtraTablePattern.MatchString(name) {
			continue
		}
		if name == "usage_logs" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, name)
	}
	db.usageLogExtraTables = normalized
}

// existingUsageLogExtraTables 只返回数据库里真实存在的备份表。
// 配置了但还没建表（或已清理）的名称会被跳过并打一条日志，避免整个统计接口报错。
func (db *DB) existingUsageLogExtraTables(ctx context.Context) []string {
	if db == nil || len(db.usageLogExtraTables) == 0 {
		return nil
	}
	query := ""
	switch {
	case db.isSQLite():
		query = `SELECT name FROM sqlite_master WHERE type='table' AND name IN (%s)`
	case db.isMySQL():
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN (%s)`
	default:
		query = `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename IN (%s)`
	}
	placeholders := make([]string, len(db.usageLogExtraTables))
	args := make([]any, len(db.usageLogExtraTables))
	for i, name := range db.usageLogExtraTables {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}
	rows, err := db.conn.QueryContext(ctx, fmt.Sprintf(query, strings.Join(placeholders, ",")), args...)
	if err != nil {
		log.Printf("[usage_logs] 检查备份表失败，本次只统计 usage_logs: %v", err)
		return nil
	}
	defer rows.Close()
	found := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		found[strings.ToLower(name)] = struct{}{}
	}
	out := make([]string, 0, len(db.usageLogExtraTables))
	for _, name := range db.usageLogExtraTables {
		if _, ok := found[strings.ToLower(name)]; ok {
			out = append(out, name)
			continue
		}
		log.Printf("[usage_logs] 备份表 %s 不存在，已跳过", name)
	}
	return out
}

// usageLogSourceSQL 返回用量统计的读取来源：
// 没有备份表时就是 usage_logs；有时是它们的 UNION ALL 派生表，列结构与 usage_logs 一致。
// 只有新增的内部用量统计接口使用它，写入和其他查询仍然只走 usage_logs。
func (db *DB) usageLogSourceSQL(ctx context.Context) string {
	extra := db.existingUsageLogExtraTables(ctx)
	if len(extra) == 0 {
		return "usage_logs"
	}
	// 显式列出统计需要的列：备份表即使多几列/少几列，只要这些列在就能合并，
	// 避免 SELECT * 因列数不一致直接报错。
	columns := "created_at, api_key_id, api_key_name, endpoint, model, status_code, total_tokens, " +
		"prompt_tokens, completion_tokens, cached_tokens, input_tokens, output_tokens, account_billed, user_billed"
	parts := make([]string, 0, len(extra)+1)
	parts = append(parts, "SELECT "+columns+" FROM usage_logs")
	for _, name := range extra {
		parts = append(parts, "SELECT "+columns+" FROM "+name)
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ") AS usage_logs"
}
