package database

import (
	"context"
	"database/sql"
	"strings"
)

// API Key 归属人邮箱。
//
// 存档不复制归属人：查询时 LEFT JOIN api_keys 取 owner_email，所以只要在
// api_keys 里维护好这一列，历史存档立刻就能按人查到，不需要回填或重新采集。
// 鉴权热路径不读这一列，因此不影响 API Key 校验。

// EnsureAPIKeyOwnerEmail 为 api_keys 补 owner_email 列与索引，可重复执行。
func (db *DB) EnsureAPIKeyOwnerEmail(ctx context.Context) error {
	if db == nil || db.conn == nil {
		return nil
	}
	switch {
	case db.isSQLite():
		if err := db.ensureSQLiteColumn(ctx, "api_keys", "owner_email", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		_, err := db.conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_keys_owner_email ON api_keys(owner_email)`)
		return err
	case db.isMySQL():
		if err := db.ensureMySQLColumn(ctx, "api_keys", "owner_email", "VARCHAR(255) NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		return db.ensureMySQLIndex(ctx, "api_keys", "idx_api_keys_owner_email",
			"CREATE INDEX idx_api_keys_owner_email ON api_keys(owner_email)")
	default:
		if _, err := db.conn.ExecContext(ctx,
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS owner_email VARCHAR(255) NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		_, err := db.conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_keys_owner_email ON api_keys(owner_email)`)
		return err
	}
}

// GetAPIKeyOwnerEmail 返回归属人邮箱；api key 不存在时返回 sql.ErrNoRows。
func (db *DB) GetAPIKeyOwnerEmail(ctx context.Context, apiKeyID int64) (string, error) {
	if db == nil || db.conn == nil || apiKeyID <= 0 {
		return "", sql.ErrNoRows
	}
	var email string
	err := db.conn.QueryRowContext(ctx,
		`SELECT COALESCE(owner_email,'') FROM api_keys WHERE id=$1`, apiKeyID).Scan(&email)
	return strings.TrimSpace(email), err
}

// SetAPIKeyOwnerEmail 更新归属人邮箱；空字符串表示清空。
func (db *DB) SetAPIKeyOwnerEmail(ctx context.Context, apiKeyID int64, email string) error {
	if db == nil || db.conn == nil || apiKeyID <= 0 {
		return sql.ErrNoRows
	}
	email = strings.TrimSpace(email)
	res, err := db.conn.ExecContext(ctx, `UPDATE api_keys SET owner_email=$1 WHERE id=$2`, email, apiKeyID)
	if err != nil {
		return err
	}
	if affected, rerr := res.RowsAffected(); rerr == nil && affected == 0 {
		// MySQL 在值未变化时返回 0，需要确认这一行确实存在。
		var exists int
		if checkErr := db.conn.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE id=$1`, apiKeyID).Scan(&exists); checkErr != nil {
			return sql.ErrNoRows
		}
	}
	return nil
}
