package database

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// UsageRollupItem 是从小时汇总表聚合出来的一行（按 group_by 分组）。
type UsageRollupItem struct {
	Key          string  `json:"key"`
	Label        string  `json:"label,omitempty"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	UserBilled   float64 `json:"user_billed"`
}

// UsageRollupStats 是内部 AI 用量统计的返回结构。
type UsageRollupStats struct {
	GroupBy       string            `json:"group_by"`
	TotalRequests int64             `json:"total_requests"`
	TotalTokens   int64             `json:"total_tokens"`
	InputTokens   int64             `json:"input_tokens"`
	OutputTokens  int64             `json:"output_tokens"`
	UserBilled    float64           `json:"user_billed"`
	AccountBilled float64           `json:"account_billed"`
	Items         []UsageRollupItem `json:"items"`
}

// usageRollupGroupExpr 把 group_by 映射成 usage_log_hourly 的分组表达式。
// 返回空串表示不过滤分组（只返回总计）。
func (db *DB) usageRollupGroupExpr(groupBy string) (expr, labelExpr string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "model":
		return "COALESCE(model,'')", "", true
	case "protocol", "endpoint", "channel":
		return "COALESCE(endpoint,'')", "", true
	case "user", "api_key", "group":
		return "CAST(api_key_id AS CHAR)", "COALESCE(MAX(api_key_name),'')", true
	case "date", "day":
		// bucket 是小时粒度，按天输出
		switch {
		case db.isSQLite():
			return "strftime('%Y-%m-%d', bucket)", "", true
		case db.isMySQL():
			return "DATE_FORMAT(bucket,'%Y-%m-%d')", "", true
		default:
			return "TO_CHAR(bucket,'YYYY-MM-DD')", "", true
		}
	default:
		return "", "", false
	}
}

// GetUsageStatsFromRollup 从 usage_log_hourly 聚合用量统计。
//
// 为什么用它：小时汇总表按 (bucket, dimension_hash) 存了 model / endpoint / api_key
// 等维度，覆盖全部历史，行数量级远小于明细（当前约 7.5 万行 vs 260 万行明细），
// 因此既能返回历史全量分组，又能在毫秒级完成，不需要缓存或后台任务。
func (db *DB) GetUsageStatsFromRollup(ctx context.Context, from, to time.Time, groupBy string, limit int, order string) (*UsageRollupStats, error) {
	out := &UsageRollupStats{GroupBy: strings.ToLower(strings.TrimSpace(groupBy)), Items: []UsageRollupItem{}}
	if db == nil || db.conn == nil {
		return out, nil
	}
	where := "1=1"
	args := []any{}
	if !from.IsZero() {
		args = append(args, db.timeArg(from))
		where += " AND bucket >= $1"
	}
	if !to.IsZero() {
		args = append(args, db.timeArg(to))
		where += " AND bucket <= $2"
	}

	if err := db.conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(user_billed),0), COALESCE(SUM(account_billed),0)
		FROM usage_log_hourly WHERE `+where, args...).Scan(
		&out.TotalRequests, &out.TotalTokens, &out.InputTokens, &out.OutputTokens,
		&out.UserBilled, &out.AccountBilled,
	); err != nil {
		return nil, err
	}

	expr, labelExpr, ok := db.usageRollupGroupExpr(groupBy)
	if !ok {
		if out.GroupBy != "none" && out.GroupBy != "" {
			out.GroupBy = "none"
		}
		return out, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	direction := "DESC"
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		direction = "ASC"
	}
	cols := expr + ` AS k, COALESCE(SUM(requests),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(user_billed),0)`
	if labelExpr != "" {
		cols += ", " + labelExpr
	}
	// 日期序列按时间排序，其它维度按请求数排序。
	orderExpr := "SUM(requests)"
	if strings.EqualFold(out.GroupBy, "date") || strings.EqualFold(out.GroupBy, "day") {
		orderExpr = "k"
	}
	query := `SELECT ` + cols + ` FROM usage_log_hourly WHERE ` + where +
		` GROUP BY ` + expr + ` ORDER BY ` + orderExpr + ` ` + direction + ` LIMIT $` + strconv.Itoa(len(args)+1)
	rows, err := db.conn.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageRollupItem
		var lasterr error
		if labelExpr != "" {
			lasterr = rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.InputTokens, &item.OutputTokens, &item.UserBilled, &item.Label)
		} else {
			lasterr = rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.InputTokens, &item.OutputTokens, &item.UserBilled)
		}
		if lasterr != nil {
			return nil, lasterr
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
