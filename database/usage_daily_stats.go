package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// UsageDailyTokenRow is one calendar day's token usage, split by effective model.
type UsageDailyTokenRow struct {
	Date         string           `json:"date"`
	Requests     int64            `json:"requests"`
	TotalTokens  int64            `json:"total_tokens"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	CachedTokens int64            `json:"cached_tokens"`
	ModelTokens  map[string]int64 `json:"model_tokens"`
}

// UsageDailyTokenTotal is the total for the selected range.
type UsageDailyTokenTotal struct {
	Requests     int64            `json:"requests"`
	TotalTokens  int64            `json:"total_tokens"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	CachedTokens int64            `json:"cached_tokens"`
	ModelTokens  map[string]int64 `json:"model_tokens"`
}

// UsageDailyTokenStats is returned by the daily token usage endpoint.
type UsageDailyTokenStats struct {
	Models []string             `json:"models"`
	Rows   []UsageDailyTokenRow `json:"rows"`
	Total  UsageDailyTokenTotal `json:"total"`
}

// GetDailyTokenUsage aggregates retained usage logs by calendar day and model.
// The query deliberately uses DATE()/GROUP BY only, which is supported by
// SQLite, MySQL 5.6 and PostgreSQL without JSON or window-function features.
func (db *DB) GetDailyTokenUsage(ctx context.Context, rangeStart, rangeEnd time.Time, channel, model string, apiKeyID, accountID *int64) (*UsageDailyTokenStats, error) {
	now := time.Now()
	if rangeStart.IsZero() {
		rangeStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	if rangeEnd.IsZero() {
		rangeEnd = now
	}
	if !rangeEnd.After(rangeStart) {
		return nil, fmt.Errorf("end must be after start")
	}

	timeWhere, args := db.usageStatsTimeWhere("created_at", rangeStart, rangeEnd, channel)
	modelExpr := "COALESCE(NULLIF(effective_model, ''), NULLIF(model, ''), 'unknown')"
	if model = strings.TrimSpace(model); model != "" {
		placeholder := fmt.Sprintf("$%d", len(args)+1)
		timeWhere += fmt.Sprintf(" AND (%s = %s OR model = %s)", modelExpr, placeholder, placeholder)
		args = append(args, model)
	}
	if apiKeyID != nil {
		placeholder := fmt.Sprintf("$%d", len(args)+1)
		timeWhere += " AND api_key_id = " + placeholder
		args = append(args, *apiKeyID)
	}
	if accountID != nil {
		placeholder := fmt.Sprintf("$%d", len(args)+1)
		timeWhere += " AND account_id = " + placeholder
		args = append(args, *accountID)
	}

	dayExpr := "DATE(created_at)"
	if !db.isSQLite() && !db.isMySQL() {
		dayExpr = "TO_CHAR(DATE_TRUNC('day', created_at), 'YYYY-MM-DD')"
	}
	query := `SELECT ` + dayExpr + ` AS usage_day,
		` + modelExpr + ` AS model_name,
		COUNT(*) AS requests,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens
		FROM usage_logs
		WHERE ` + timeWhere + ` AND status_code <> 499
		GROUP BY 1, 2
		ORDER BY 1 ASC, 2 ASC`

	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDate := make(map[string]*UsageDailyTokenRow)
	modelTotals := make(map[string]int64)
	for rows.Next() {
		var day, modelName string
		var row UsageDailyTokenRow
		if err := rows.Scan(&day, &modelName, &row.Requests, &row.TotalTokens, &row.InputTokens, &row.OutputTokens, &row.CachedTokens); err != nil {
			return nil, err
		}
		day = strings.TrimSpace(day)
		if day == "" {
			continue
		}
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			modelName = "unknown"
		}
		if existing := byDate[day]; existing != nil {
			existing.Requests += row.Requests
			existing.TotalTokens += row.TotalTokens
			existing.InputTokens += row.InputTokens
			existing.OutputTokens += row.OutputTokens
			existing.CachedTokens += row.CachedTokens
			existing.ModelTokens[modelName] += row.TotalTokens
		} else {
			row.Date = day
			row.ModelTokens = map[string]int64{modelName: row.TotalTokens}
			byDate[day] = &row
		}
		modelTotals[modelName] += row.TotalTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &UsageDailyTokenStats{
		Models: make([]string, 0, len(modelTotals)),
		Rows:   make([]UsageDailyTokenRow, 0, len(byDate)),
		Total: UsageDailyTokenTotal{
			ModelTokens: make(map[string]int64, len(modelTotals)),
		},
	}
	for modelName, total := range modelTotals {
		result.Models = append(result.Models, modelName)
		result.Total.ModelTokens[modelName] = total
	}
	sort.Slice(result.Models, func(i, j int) bool {
		left, right := result.Total.ModelTokens[result.Models[i]], result.Total.ModelTokens[result.Models[j]]
		if left != right {
			return left > right
		}
		return result.Models[i] < result.Models[j]
	})
	for _, row := range byDate {
		result.Rows = append(result.Rows, *row)
		result.Total.Requests += row.Requests
		result.Total.TotalTokens += row.TotalTokens
		result.Total.InputTokens += row.InputTokens
		result.Total.OutputTokens += row.OutputTokens
		result.Total.CachedTokens += row.CachedTokens
	}
	sort.Slice(result.Rows, func(i, j int) bool { return result.Rows[i].Date < result.Rows[j].Date })
	return result, nil
}
