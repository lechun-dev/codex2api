package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// ListAccountListProjection returns only non-secret fields needed to build the
// short-lived admin list snapshot. PostgreSQL and SQLite project fields in SQL;
// MySQL 5.6 has no usable JSON functions, so that branch reads the credentials
// text temporarily and applies the same secret-stripping projection in Go.
func (db *DB) ListAccountListProjection(ctx context.Context, channel string) ([]*AccountRow, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	where := `status <> 'deleted' AND COALESCE(error_message, '') <> 'deleted'`
	if db.isMySQL() {
		// 2026-09-02 coder(lq): MySQL 5.6 has no JSON functions. Read MEDIUMTEXT
		// only for this short-lived projection and discard secret fields below.
		var query string
		switch channel {
		case UpstreamChannelGrok:
			where += ` AND ` + db.accountUpstreamTypePredicate(UpstreamChannelGrok)
		case UpstreamChannelAntigravity:
			where += ` AND ` + db.accountUpstreamTypePredicate(UpstreamChannelAntigravity)
		case UpstreamChannelClaude:
			where += ` AND ` + db.accountUpstreamTypePredicate(UpstreamChannelClaude)
		case UpstreamChannelCodex:
			where += ` AND NOT (` + db.accountUpstreamTypePredicate(UpstreamChannelGrok) +
				` OR ` + db.accountUpstreamTypePredicate(UpstreamChannelAntigravity) +
				` OR ` + db.accountUpstreamTypePredicate(UpstreamChannelClaude) + `)`
		}
		query = `SELECT id, name, type, proxy_url, status, cooldown_reason, cooldown_until,
			COALESCE(error_message, ''), COALESCE(enabled, true), COALESCE(locked, false),
			score_bias_override, base_concurrency_override, COALESCE(tags, '[]'), created_at, updated_at,
			COALESCE(credential_generation, 1), COALESCE(credential_family_id, ''), credentials
			FROM accounts WHERE ` + where + ` ORDER BY id`
		return db.listAccountListProjectionMySQL(ctx, query)
	}
	upstreamExpr := `LOWER(COALESCE(credentials->>'upstream_type', ''))`
	fromClause := `FROM accounts
		CROSS JOIN LATERAL jsonb_to_record(accounts.credentials) AS account_public(
			upstream_type text, email text, base_url text, plan_type text,
			models jsonb, api_key text, refresh_token text, scheduler_priority text,
			avatar_url text, verified_email boolean, project_id text,
			antigravity_sync_error text, antigravity_sync_warning text,
			antigravity_permissions text, antigravity_entitlements text, antigravity_quota text,
			claude_usage_probe_at text, claude_usage_probe_error text,
			claude_auth_kind text
		)`
	credentialColumns := `
		COALESCE(account_public.upstream_type, ''),
		COALESCE(account_public.email, ''),
		COALESCE(account_public.base_url, ''),
		COALESCE(account_public.plan_type, ''),
		COALESCE(account_public.models, '[]'::jsonb)::text,
		COALESCE(account_public.api_key, '') <> '',
		COALESCE(account_public.refresh_token, '') <> '',
		COALESCE(account_public.scheduler_priority, ''),
		COALESCE(account_public.avatar_url, ''),
		COALESCE(account_public.verified_email, false),
		COALESCE(account_public.project_id, ''),
		COALESCE(account_public.antigravity_sync_error, ''),
		COALESCE(account_public.antigravity_sync_warning, ''),
		COALESCE(NULLIF(account_public.antigravity_permissions, ''), account_public.antigravity_entitlements, ''),
		COALESCE(account_public.antigravity_quota, ''),
		COALESCE(account_public.claude_usage_probe_at, ''),
		COALESCE(account_public.claude_usage_probe_error, ''),
		COALESCE(account_public.claude_auth_kind, '')`
	if db.isSQLite() {
		upstreamExpr = `LOWER(COALESCE(json_extract(credentials, '$.upstream_type'), ''))`
		fromClause = `FROM accounts`
		credentialColumns = `
			COALESCE(json_extract(credentials, '$.upstream_type'), ''),
			COALESCE(json_extract(credentials, '$.email'), ''),
			COALESCE(json_extract(credentials, '$.base_url'), ''),
			COALESCE(json_extract(credentials, '$.plan_type'), ''),
			COALESCE(json_extract(credentials, '$.models'), '[]'),
			CASE WHEN COALESCE(json_extract(credentials, '$.api_key'), '') <> '' THEN 1 ELSE 0 END,
			CASE WHEN COALESCE(json_extract(credentials, '$.refresh_token'), '') <> '' THEN 1 ELSE 0 END,
			COALESCE(CAST(json_extract(credentials, '$.scheduler_priority') AS TEXT), ''),
			COALESCE(json_extract(credentials, '$.avatar_url'), ''),
			CASE WHEN COALESCE(json_extract(credentials, '$.verified_email'), 0) <> 0 THEN 1 ELSE 0 END,
			COALESCE(json_extract(credentials, '$.project_id'), ''),
			COALESCE(json_extract(credentials, '$.antigravity_sync_error'), ''),
			COALESCE(json_extract(credentials, '$.antigravity_sync_warning'), ''),
			COALESCE(NULLIF(json_extract(credentials, '$.antigravity_permissions'), ''), json_extract(credentials, '$.antigravity_entitlements'), '{}'),
			COALESCE(json_extract(credentials, '$.antigravity_quota'), '{}'),
			COALESCE(json_extract(credentials, '$.claude_usage_probe_at'), ''),
			COALESCE(json_extract(credentials, '$.claude_usage_probe_error'), ''),
			COALESCE(json_extract(credentials, '$.claude_auth_kind'), '')`
	}
	switch channel {
	case UpstreamChannelGrok:
		where += ` AND ` + upstreamExpr + ` = 'grok'`
	case UpstreamChannelAntigravity:
		where += ` AND ` + upstreamExpr + ` = 'antigravity'`
	case UpstreamChannelClaude:
		where += ` AND ` + upstreamExpr + ` = 'claude'`
	case UpstreamChannelCodex:
		where += ` AND ` + upstreamExpr + ` NOT IN ('grok', 'antigravity', 'claude')`
	}
	query := `SELECT id, name, type, proxy_url, status, cooldown_reason, cooldown_until,
		COALESCE(error_message, ''), COALESCE(enabled, true), COALESCE(locked, false),
		score_bias_override, base_concurrency_override, COALESCE(tags, '[]'), created_at, updated_at,
		COALESCE(credential_generation, 1), COALESCE(credential_family_id, ''),` + credentialColumns + `
		` + fromClause + ` WHERE ` + where + ` ORDER BY id`
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询账号列表投影失败: %w", err)
	}
	defer rows.Close()
	result := make([]*AccountRow, 0)
	for rows.Next() {
		row, err := scanAccountListProjection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) listAccountListProjectionMySQL(ctx context.Context, query string) ([]*AccountRow, error) {
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询账号列表投影失败: %w", err)
	}
	defer rows.Close()
	result := make([]*AccountRow, 0)
	for rows.Next() {
		row := &AccountRow{}
		var credentialsRaw, cooldownRaw, tagsRaw, createdRaw, updatedRaw interface{}
		if err := rows.Scan(
			&row.ID, &row.Name, &row.Type, &row.ProxyURL, &row.Status, &row.CooldownReason, &cooldownRaw,
			&row.ErrorMessage, &row.Enabled, &row.Locked, &row.ScoreBiasOverride, &row.BaseConcurrencyOverride,
			&tagsRaw, &createdRaw, &updatedRaw, &row.CredentialGeneration, &row.CredentialFamilyID,
			&credentialsRaw,
		); err != nil {
			return nil, fmt.Errorf("扫描账号列表投影失败: %w", err)
		}
		row.Tags = decodeTagsValue(tagsRaw)
		if err := populateAccountProjectionTimes(row, cooldownRaw, createdRaw, updatedRaw); err != nil {
			return nil, err
		}
		row.Credentials = projectAccountCredentials(decodeCredentials(credentialsRaw))
		result = append(result, row)
	}
	return result, rows.Err()
}

type accountProjectionScanner interface {
	Scan(dest ...interface{}) error
}

func scanAccountListProjection(scanner accountProjectionScanner) (*AccountRow, error) {
	row := &AccountRow{}
	var cooldownRaw, tagsRaw, createdRaw, updatedRaw interface{}
	var upstreamType, email, baseURL, planType, schedulerPriority string
	var avatarURL, projectID string
	var antigravitySyncError, antigravitySyncWarning, antigravityPermissions, antigravityQuota string
	var claudeUsageProbeAt, claudeUsageProbeError, claudeAuthKind string
	var modelsRaw interface{}
	var hasAPIKey, hasRefreshToken, verifiedEmail bool
	if err := scanner.Scan(
		&row.ID, &row.Name, &row.Type, &row.ProxyURL, &row.Status, &row.CooldownReason, &cooldownRaw,
		&row.ErrorMessage, &row.Enabled, &row.Locked, &row.ScoreBiasOverride, &row.BaseConcurrencyOverride,
		&tagsRaw, &createdRaw, &updatedRaw, &row.CredentialGeneration, &row.CredentialFamilyID,
		&upstreamType, &email, &baseURL, &planType, &modelsRaw,
		&hasAPIKey, &hasRefreshToken, &schedulerPriority,
		&avatarURL, &verifiedEmail, &projectID,
		&antigravitySyncError, &antigravitySyncWarning, &antigravityPermissions, &antigravityQuota,
		&claudeUsageProbeAt, &claudeUsageProbeError, &claudeAuthKind,
	); err != nil {
		return nil, fmt.Errorf("扫描账号列表投影失败: %w", err)
	}
	row.Tags = decodeTagsValue(tagsRaw)
	if err := populateAccountProjectionTimes(row, cooldownRaw, createdRaw, updatedRaw); err != nil {
		return nil, err
	}
	row.Credentials = projectAccountCredentials(map[string]interface{}{
		"upstream_type":            upstreamType,
		"email":                    email,
		"base_url":                 baseURL,
		"plan_type":                planType,
		"models":                   modelsRaw,
		"api_key":                  hasAPIKey,
		"refresh_token":            hasRefreshToken,
		"scheduler_priority":       schedulerPriority,
		"avatar_url":               avatarURL,
		"verified_email":           verifiedEmail,
		"project_id":               projectID,
		"antigravity_sync_error":   antigravitySyncError,
		"antigravity_sync_warning": antigravitySyncWarning,
		"antigravity_permissions":  antigravityPermissions,
		"antigravity_quota":        antigravityQuota,
		"claude_usage_probe_at":    claudeUsageProbeAt,
		"claude_usage_probe_error": claudeUsageProbeError,
		"claude_auth_kind":         claudeAuthKind,
	})
	return row, nil
}

func populateAccountProjectionTimes(row *AccountRow, cooldownRaw, createdRaw, updatedRaw interface{}) error {
	var err error
	row.CooldownUntil, err = parseDBNullTimeValue(cooldownRaw)
	if err != nil {
		return fmt.Errorf("解析 cooldown_until 失败: %w", err)
	}
	row.CreatedAt, err = parseDBTimeValue(createdRaw)
	if err != nil {
		return fmt.Errorf("解析 created_at 失败: %w", err)
	}
	row.UpdatedAt, err = parseDBTimeValue(updatedRaw)
	if err != nil {
		return fmt.Errorf("解析 updated_at 失败: %w", err)
	}
	return nil
}

func projectAccountCredentials(credentials map[string]interface{}) map[string]interface{} {
	projected := map[string]interface{}{
		"upstream_type": projectionStringValue(credentials["upstream_type"]),
		"email":         projectionStringValue(credentials["email"]),
		"base_url":      projectionStringValue(credentials["base_url"]),
		"plan_type":     projectionStringValue(credentials["plan_type"]),
	}
	if models := decodeProjectionStringSlice(credentials["models"]); len(models) > 0 {
		projected["models"] = models
	}
	if projectionCredentialConfigured(credentials["api_key"]) {
		projected["api_key"] = "configured"
	}
	if projectionCredentialConfigured(credentials["refresh_token"]) {
		projected["refresh_token"] = "configured"
	}
	if schedulerPriority := projectionStringValue(credentials["scheduler_priority"]); strings.TrimSpace(schedulerPriority) != "" {
		projected["scheduler_priority"] = schedulerPriority
	}
	for _, key := range []string{
		"avatar_url", "project_id", "antigravity_sync_error", "antigravity_sync_warning",
		"antigravity_permissions", "antigravity_quota", "claude_usage_probe_at", "claude_usage_probe_error",
		"claude_auth_kind",
	} {
		if value := credentials[key]; value != nil && strings.TrimSpace(projectionStringValue(value)) != "" {
			projected[key] = value
		}
	}
	if _, ok := projected["antigravity_permissions"]; !ok {
		if value := credentials["antigravity_entitlements"]; value != nil && strings.TrimSpace(projectionStringValue(value)) != "" {
			projected["antigravity_permissions"] = value
		}
	}
	if verified, ok := credentials["verified_email"].(bool); ok && verified {
		projected["verified_email"] = true
	}
	return projected
}

func projectionStringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprint(value)
}

func projectionCredentialConfigured(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return value != nil
	}
}

func decodeProjectionStringSlice(value interface{}) []string {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	case nil:
		return nil
	default:
		raw, _ = json.Marshal(typed)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

// ListActiveByIDs fetches the complete base rows for one selected page in a
// single query. It is intentionally capped by the caller's page size (<=500).
func (db *DB) ListActiveByIDs(ctx context.Context, ids []int64) ([]*AccountRow, error) {
	ids = positiveUniqueIDs(ids)
	if len(ids) == 0 {
		return []*AccountRow{}, nil
	}
	args := make([]interface{}, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	query := `SELECT id, name, platform, type, credentials, proxy_url, status, cooldown_reason,
		cooldown_until, error_message, COALESCE(enabled, true), COALESCE(locked, false),
		COALESCE(credit_enabled, false), COALESCE(credit_skip_usage_window, false),
		COALESCE(skip_warm_tier, false), score_bias_override, base_concurrency_override,
		COALESCE(tags, '[]'), COALESCE(note, ''), created_at, updated_at,
		COALESCE(credential_generation, 1), COALESCE(credential_family_id, '')
		FROM accounts WHERE status <> 'deleted' AND COALESCE(error_message, '') <> 'deleted'
		AND id IN (` + strings.Join(placeholders, ",") + `) ORDER BY id`
	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询账号失败: %w", err)
	}
	defer rows.Close()
	result := make([]*AccountRow, 0, len(ids))
	for rows.Next() {
		row := &AccountRow{}
		var credentialsRaw, cooldownRaw, tagsRaw, createdRaw, updatedRaw interface{}
		if err := rows.Scan(
			&row.ID, &row.Name, &row.Platform, &row.Type, &credentialsRaw, &row.ProxyURL, &row.Status,
			&row.CooldownReason, &cooldownRaw, &row.ErrorMessage, &row.Enabled, &row.Locked,
			&row.CreditEnabled, &row.CreditSkipUsageWindow, &row.SkipWarmTier, &row.ScoreBiasOverride,
			&row.BaseConcurrencyOverride, &tagsRaw, &row.Note, &createdRaw, &updatedRaw,
			&row.CredentialGeneration, &row.CredentialFamilyID,
		); err != nil {
			return nil, fmt.Errorf("扫描账号行失败: %w", err)
		}
		row.Credentials = decodeCredentials(credentialsRaw)
		row.Tags = decodeTagsValue(tagsRaw)
		row.CooldownUntil, err = parseDBNullTimeValue(cooldownRaw)
		if err != nil {
			return nil, err
		}
		row.CreatedAt, err = parseDBTimeValue(createdRaw)
		if err != nil {
			return nil, err
		}
		row.UpdatedAt, err = parseDBTimeValue(updatedRaw)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var _ accountProjectionScanner = (*sql.Row)(nil)
