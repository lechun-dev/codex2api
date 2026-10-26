package database

import (
	"strings"
	"testing"
)

// MySQL 5.6 的建表/建索引语句必须能被 5.6 解析：不能出现 BIGSERIAL、
// IF NOT EXISTS、部分索引、JSON 函数、RETURNING 等。
func TestIntelligenceCheckMySQLDDLAvoidsPostgresSyntax(t *testing.T) {
	for _, stmt := range intelligenceCheckMySQLTables() {
		assertNoMySQL56IncompatibleSQL(t, stmt)
		if strings.Contains(strings.ToUpper(stmt), "REFERENCES") {
			t.Fatalf("MySQL 建表不应带外键约束（历史库/字符集不一致时会建表失败）: %s", stmt)
		}
	}
	for _, idx := range intelligenceCheckMySQLIndexes() {
		assertNoMySQL56IncompatibleSQL(t, idx.stmt)
		if strings.Contains(strings.ToUpper(idx.stmt), " WHERE ") {
			t.Fatalf("MySQL 5.6 不支持部分索引: %s", idx.stmt)
		}
	}
}

func TestIntelligenceCheckMySQLSchemaCoversJSONBackedColumns(t *testing.T) {
	// 5.6 读不了 JSON，所以这些字段必须以真实列存在。
	required := map[string][]string{
		"intelligence_check_accounts": {"state_action"},
		"intelligence_check_runs": {
			"question_name", "duration_ms", "summary_version",
			"source", "account_name", "channel",
		},
	}
	for _, stmt := range intelligenceCheckMySQLTables() {
		for table, columns := range required {
			if !strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS "+table) {
				continue
			}
			for _, column := range columns {
				if !strings.Contains(stmt, column+" ") {
					t.Fatalf("%s 缺少列 %s", table, column)
				}
			}
			delete(required, table)
		}
	}
	if len(required) != 0 {
		t.Fatalf("建表语句缺少这些表: %v", required)
	}
}

func TestIntelligenceCheckMySQLExpressionsUseColumnsNotJSON(t *testing.T) {
	db := &DB{driver: "mysql"}

	channel := db.intelligenceChannelExpr()
	if !strings.Contains(channel, "REGEXP") {
		t.Fatalf("MySQL 渠道表达式应使用正则匹配 credentials: %s", channel)
	}
	if strings.Contains(channel, "->>") || strings.Contains(strings.ToUpper(channel), "JSON_EXTRACT") {
		t.Fatalf("MySQL 渠道表达式不应包含 JSON 函数: %s", channel)
	}

	eligible := db.intelligenceEligibleSQL()
	if !strings.Contains(eligible, "REGEXP") || !strings.Contains(eligible, "NOT IN") {
		t.Fatalf("MySQL 资格表达式不完整: %s", eligible)
	}
	assertNoMySQL56IncompatibleSQL(t, eligible)

	if got := db.intelligenceStateActionExpr("s"); got != "s.state_action" {
		t.Fatalf("MySQL 的 state 归属动作应读落地列，got %q", got)
	}

	cases := map[string]string{
		"source":        "source",
		"account_name":  "account_name",
		"channel":       "channel",
		"duration_ms":   "duration_ms",
		"question.name": "question_name",
		// 未收录字段返回 SQL 空串字面量，避免把 JSON 表达式漏进 MySQL。
		"unknown": "''",
	}
	for field, want := range cases {
		if got := db.intelligenceJSONField(field); got != want {
			t.Fatalf("intelligenceJSONField(%q) = %q, want %q", field, got, want)
		}
	}
}

func TestIntelligenceCheckPostgresExpressionsUnchanged(t *testing.T) {
	// fork 只在 MySQL 分支改动；PostgreSQL 仍走 JSON 表达式。
	db := &DB{}
	if got := db.intelligenceStateActionExpr("s"); !strings.Contains(got, "->>") {
		t.Fatalf("PostgreSQL 分支应保留 JSON 表达式, got %q", got)
	}
	if got := db.intelligenceJSONField("source"); !strings.Contains(got, "->>") {
		t.Fatalf("PostgreSQL 分支应保留 JSON 表达式, got %q", got)
	}
}
