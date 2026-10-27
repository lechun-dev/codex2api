package database

import (
	"context"
	"time"
)

// GetUsageStatsIncludingBackup 计算用量统计，读取范围是 usage_logs 加上配置的历史备份表
// （如 usage_logs_openclaw）。只有内部 AI 用量统计接口使用它；其他查询仍然只读 usage_logs。
//
// 与 getUsageStats 的区别：不走 usage_hourly_rollup 累计表（rollup 只覆盖 usage_logs），
// 直接在合并后的数据上聚合，保证备份表里的历史数据能计入总数与各维度。
func (db *DB) GetUsageStatsIncludingBackup(ctx context.Context, from, to time.Time) (*UsageStats, error) {
	stats := &UsageStats{
		ModelStats:    []UsageModelStat{},
		EndpointStats: []UsageEndpointStat{},
		APIKeyStats:   []UsageAPIKeyStat{},
	}
	if db == nil || db.conn == nil {
		return stats, nil
	}
	source := db.usageLogSourceSQL(ctx)
	var errorCount int64
	where := "1=1"
	args := []any{}
	if !from.IsZero() {
		args = append(args, db.timeArg(from))
		where += " AND created_at >= $1"
	}
	if !to.IsZero() {
		args = append(args, db.timeArg(to))
		where += " AND created_at <= $2"
	}

	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(total_tokens),0), COALESCE(SUM(prompt_tokens),0),
		COALESCE(SUM(completion_tokens),0), COALESCE(SUM(cached_tokens),0),
		COALESCE(SUM(account_billed),0), COALESCE(SUM(user_billed),0),
		COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0)
		FROM `+source+` WHERE `+where, args...).Scan(
		&stats.TotalRequests, &stats.TotalTokens, &stats.TotalPrompt, &stats.TotalCompletion,
		&stats.TotalCachedTokens, &stats.TotalAccountBilled, &stats.TotalUserBilled, &errorCount,
	); err != nil {
		return nil, err
	}

	// 错误率沿用现有口径：状态码 >= 400 的请求占比。
	if stats.TotalRequests > 0 {
		stats.ErrorRate = float64(errorCount) / float64(stats.TotalRequests) * 100
	}
	if err := db.fillBackupModelStats(ctx, source, where, args, stats); err != nil {
		return nil, err
	}
	if err := db.fillBackupEndpointStats(ctx, source, where, args, stats); err != nil {
		return nil, err
	}
	if err := db.fillBackupAPIKeyStats(ctx, source, where, args, stats); err != nil {
		return nil, err
	}
	return stats, nil
}

func (db *DB) fillBackupModelStats(ctx context.Context, source, where string, args []any, stats *UsageStats) error {
	rows, err := db.conn.QueryContext(ctx, `SELECT COALESCE(model,''), COUNT(*),
		COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cached_tokens),0), COALESCE(SUM(account_billed),0), COALESCE(SUM(user_billed),0),
		COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0)
		FROM `+source+` WHERE `+where+` GROUP BY COALESCE(model,'') ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageModelStat
		if err := rows.Scan(&item.Model, &item.Requests, &item.Tokens, &item.InputTokens, &item.OutputTokens,
			&item.CachedTokens, &item.AccountBilled, &item.UserBilled, &item.ErrorCount); err != nil {
			return err
		}
		stats.ModelStats = append(stats.ModelStats, item)
	}
	return rows.Err()
}

func (db *DB) fillBackupEndpointStats(ctx context.Context, source, where string, args []any, stats *UsageStats) error {
	rows, err := db.conn.QueryContext(ctx, `SELECT COALESCE(endpoint,''), COUNT(*), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0), COALESCE(SUM(user_billed),0)
		FROM `+source+` WHERE `+where+` GROUP BY COALESCE(endpoint,'') ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageEndpointStat
		if err := rows.Scan(&item.Endpoint, &item.Requests, &item.Tokens, &item.ErrorCount, &item.UserBilled); err != nil {
			return err
		}
		stats.EndpointStats = append(stats.EndpointStats, item)
	}
	return rows.Err()
}

func (db *DB) fillBackupAPIKeyStats(ctx context.Context, source, where string, args []any, stats *UsageStats) error {
	rows, err := db.conn.QueryContext(ctx, `SELECT COALESCE(api_key_id,0), COALESCE(MAX(api_key_name),''), COUNT(*),
		COALESCE(SUM(total_tokens),0), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(user_billed),0)
		FROM `+source+` WHERE `+where+` GROUP BY COALESCE(api_key_id,0) ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageAPIKeyStat
		if err := rows.Scan(&item.APIKeyID, &item.Label, &item.Requests, &item.Tokens, &item.ErrorCount, &item.UserBilled); err != nil {
			return err
		}
		stats.APIKeyStats = append(stats.APIKeyStats, item)
	}
	return rows.Err()
}
