package database

import (
	"context"
	"encoding/json"
	"strings"
)

// ListScheduledConnectionTestConfigs projects only non-secret plan data; token
// blobs and the runtime scheduler are not exposed to the maintenance worker.
type ScheduledConnectionTestConfig struct {
	AccountID int64
	Config    string
}

func (db *DB) ListScheduledConnectionTestConfigs(ctx context.Context) ([]ScheduledConnectionTestConfig, error) {
	if db.isMySQL() {
		return db.listScheduledConnectionTestConfigsMySQL(ctx)
	}
	expr := "credentials->>'scheduled_connection_tests'"
	upstream := "LOWER(COALESCE(credentials->>'upstream_type',''))"
	if db.isSQLite() {
		expr = "json_extract(credentials,'$.scheduled_connection_tests')"
		upstream = "LOWER(COALESCE(json_extract(credentials,'$.upstream_type'),''))"
	}
	rows, err := db.conn.QueryContext(ctx, "SELECT id,"+expr+" FROM accounts WHERE status<>'deleted' AND COALESCE(error_message,'')<>'deleted' AND COALESCE(enabled,true) AND "+upstream+" IN ('','codex','openai_responses') AND COALESCE("+expr+",'')<>'' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ScheduledConnectionTestConfig{}
	for rows.Next() {
		var row ScheduledConnectionTestConfig
		if err := rows.Scan(&row.AccountID, &row.Config); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) ListOneShotMaintenanceJobs(ctx context.Context, id int64, prefix string, limit int) ([]MaintenanceJob, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT entity_id,job_kind,due_at,lease_owner,lease_until,attempts,last_error FROM maintenance_jobs WHERE entity_id=$1 AND substr(job_kind,1,length($2))=$2 ORDER BY due_at DESC LIMIT $3`, id, prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []MaintenanceJob{}
	for rows.Next() {
		job, err := scanMaintenanceJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

// MySQL 5.6 stores credentials as text and has no JSON extraction functions.
// The SQL predicate only narrows candidates; parse the top-level fields in Go
// and return just the non-secret plan, never the surrounding credential blob.
func (db *DB) listScheduledConnectionTestConfigsMySQL(ctx context.Context) ([]ScheduledConnectionTestConfig, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT id, COALESCE(credentials,'') FROM accounts
  WHERE status<>'deleted' AND COALESCE(error_message,'')<>'deleted'
    AND COALESCE(enabled,1)<>0 AND credentials LIKE '%scheduled_connection_tests%'
  ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ScheduledConnectionTestConfig{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var fields struct {
			Upstream string          `json:"upstream_type"`
			Plan     json.RawMessage `json:"scheduled_connection_tests"`
		}
		if json.Unmarshal([]byte(raw), &fields) != nil {
			continue
		}
		switch strings.ToLower(fields.Upstream) {
		case "", "codex", "openai_responses":
		default:
			continue
		}
		plan := strings.TrimSpace(string(fields.Plan))
		if plan == "" || plan == "null" {
			continue
		}
		// Existing account APIs store the plan as a JSON-encoded string; also
		// support object-valued plans, matching the PostgreSQL/SQLite projections.
		if strings.HasPrefix(plan, "\"") {
			if json.Unmarshal(fields.Plan, &plan) != nil || plan == "" {
				continue
			}
		}
		result = append(result, ScheduledConnectionTestConfig{AccountID: id, Config: plan})
	}
	return result, rows.Err()
}
