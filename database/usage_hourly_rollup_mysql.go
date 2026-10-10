package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 2026-10-10 coder(lq): Bound backfill work while live MySQL writers wait on
// the shared state-row mutex; release it between small historical chunks.
const usageHourlyBackfillChunkMySQL = 2000

var usageHourlyTextDimensions = []string{
	"api_key_name", "api_key_masked", "channel", "model", "effective_model", "inbound_endpoint", "endpoint",
}

// 2026-10-10 coder(lq): A SHA-256 dimension key avoids MySQL 5.6's 767-byte
// index limit without truncating labels or merging distinct long/prefix values.
// Text remains unindexed and inherits each source column's charset/collation;
// reporting still groups the original values, including case/accent equivalents.
func mysqlUsageHourlyDDL(textDefinitions map[string]string) []string {
	var dimensions, aggregates strings.Builder
	for _, name := range usageHourlyTextDimensions {
		definition := textDefinitions[name]
		if definition == "" {
			definition = "TEXT CHARACTER SET utf8 COLLATE utf8_general_ci NOT NULL"
		}
		fmt.Fprintf(&dimensions, "%s %s,\n", name, definition)
	}
	for _, agg := range usageHourlyAggregates {
		typeName := "BIGINT"
		if agg.column == "account_billed" || agg.column == "user_billed" {
			typeName = "DOUBLE"
		}
		fmt.Fprintf(&aggregates, "%s %s NOT NULL DEFAULT 0,\n", agg.column, typeName)
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_log_hourly (
		bucket DATETIME NOT NULL,
		dimension_hash BINARY(32) NOT NULL,
		api_key_id BIGINT NOT NULL DEFAULT 0,
` + dimensions.String() + `
		status_code INT NOT NULL DEFAULT 0,
		stream TINYINT(1) NOT NULL DEFAULT 0,
		internal INT NOT NULL DEFAULT 0,
` + aggregates.String() + `
		min_created_at DATETIME NULL,
		PRIMARY KEY (bucket, dimension_hash),
		KEY idx_usage_log_hourly_api_key_bucket (api_key_id, bucket)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_general_ci`,
		`CREATE TABLE IF NOT EXISTS usage_log_hourly_state (
		id INT NOT NULL PRIMARY KEY,
		version INT NOT NULL DEFAULT 0,
		ready INT NOT NULL DEFAULT 0,
		backfill_to_id BIGINT NOT NULL DEFAULT 0,
		backfill_cursor BIGINT NOT NULL DEFAULT 0,
		last_log_id BIGINT NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_general_ci`,
	}
}

func (db *DB) ensureMySQLUsageHourlySchema(ctx context.Context) error {
	rows, err := db.conn.QueryContext(ctx, `SELECT COLUMN_NAME, CHARACTER_SET_NAME, COLLATION_NAME
		FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_logs'
		AND COLUMN_NAME IN ('api_key_name', 'api_key_masked', 'channel', 'model', 'effective_model', 'inbound_endpoint', 'endpoint')`)
	if err != nil {
		return err
	}
	definitions := make(map[string]string)
	for rows.Next() {
		var name, charset, collation string
		if err := rows.Scan(&name, &charset, &collation); err != nil {
			rows.Close()
			return err
		}
		if !mysqlHourlyMetadataIdentifier(charset) || !mysqlHourlyMetadataIdentifier(collation) {
			rows.Close()
			return fmt.Errorf("invalid usage dimension charset/collation for %s", name)
		}
		definitions[name] = "TEXT CHARACTER SET " + charset + " COLLATE " + collation + " NOT NULL"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, statement := range mysqlUsageHourlyDDL(definitions) {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, agg := range usageHourlyAggregates {
		typeName := "BIGINT"
		if agg.column == "account_billed" || agg.column == "user_billed" {
			typeName = "DOUBLE"
		}
		if err := db.ensureMySQLColumn(ctx, "usage_log_hourly", agg.column, typeName+" NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if err := db.ensureMySQLIndex(ctx, "usage_log_hourly", "idx_usage_log_hourly_api_key_bucket",
		`CREATE INDEX idx_usage_log_hourly_api_key_bucket ON usage_log_hourly (api_key_id, bucket)`); err != nil {
		return err
	}
	// 2026-10-10 coder(lq): Seed the shared mutex row before any transaction
	// starts; version=0 forces the normal initial reset/backfill, not a ready flag.
	_, err = db.conn.ExecContext(ctx, `INSERT IGNORE INTO usage_log_hourly_state (id) VALUES (1)`)
	return err
}

func mysqlHourlyMetadataIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// 2026-10-10 coder(lq): InnoDB has no transactional LOCK TABLE equivalent.
// Acquire this row BEFORE usage_logs or rollup rows in every writer, backfill,
// rebuild and clear transaction, preventing double counts and lock inversion.
func (db *DB) lockMySQLUsageHourlyTx(ctx context.Context, tx *sql.Tx) error {
	var id int
	return tx.QueryRowContext(ctx, `SELECT id FROM usage_log_hourly_state WHERE id = 1 FOR UPDATE`).Scan(&id)
}

func mysqlUsageHourlyUpsertSQL(where string, columns, selects, updates []string, groupBy string) string {
	allColumns := strings.Split(strings.Join(columns, ","), ",")
	aliased := make([]string, len(allColumns))
	for i, name := range allColumns {
		allColumns[i] = strings.TrimSpace(name)
		aliased[i] = selects[i] + " AS " + allColumns[i]
	}
	// 2026-10-10 coder(lq): Length-prefixed fields prevent delimiter ambiguity
	// (including Chinese names). Hash the grouped projection, not ungrouped
	// source fields, so ONLY_FULL_GROUP_BY remains valid on MySQL 5.6 and 8.0.
	// Binary casts preserve utf8mb4 bytes even when the connection uses utf8,
	// and avoid illegal mixes of source-column collations inside CONCAT.
	parts := make([]string, 0, 33)
	for _, name := range allColumns[1:12] { // bucket is already part of the primary key
		value := "CAST(hourly_rows." + name + " AS BINARY)"
		parts = append(parts, "LENGTH("+value+")", "':'", value)
	}
	return `INSERT INTO usage_log_hourly (` + strings.Join(allColumns, ", ") + `, dimension_hash)
		SELECT hourly_rows.*, UNHEX(SHA2(CONCAT(` + strings.Join(parts, ", ") + `), 256))
		FROM (SELECT ` + strings.Join(aliased, ",\n") + ` FROM usage_logs u WHERE ` + where + `
		GROUP BY ` + groupBy + `) AS hourly_rows
		ORDER BY ` + groupBy + `
		ON DUPLICATE KEY UPDATE ` + rewriteMySQLExcludedValues(strings.Join(updates, ",\n"))
}
