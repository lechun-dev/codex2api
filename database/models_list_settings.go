package database

import (
	"context"
	"errors"
	"fmt"
)

const (
	DefaultModelsListReadMaxBytes = int64(8 << 20)
	MinModelsListReadMaxBytes     = int64(1 << 20)
	MaxModelsListReadMaxBytes     = int64(256 << 20)
)

var ErrInvalidModelsListReadMaxBytes = errors.New("invalid models list read max bytes")

// 2026-09-02 coder(lq): 统一限制模型清单读取上限，避免后台保存和代理运行时出现不同边界。
func NormalizeModelsListReadMaxBytes(value int64) int64 {
	if value <= 0 {
		return DefaultModelsListReadMaxBytes
	}
	if value < MinModelsListReadMaxBytes {
		return MinModelsListReadMaxBytes
	}
	if value > MaxModelsListReadMaxBytes {
		return MaxModelsListReadMaxBytes
	}
	return value
}

func ValidateModelsListReadMaxBytes(value int64) error {
	if value < MinModelsListReadMaxBytes || value > MaxModelsListReadMaxBytes {
		return fmt.Errorf(
			"%w: models_list_read_max_bytes must be between %d and %d",
			ErrInvalidModelsListReadMaxBytes,
			MinModelsListReadMaxBytes,
			MaxModelsListReadMaxBytes,
		)
	}
	return nil
}

// UpdateModelsListReadMaxBytes only touches the model-list limit so a settings
// save cannot overwrite unrelated values from a stale admin snapshot.
func (db *DB) UpdateModelsListReadMaxBytes(ctx context.Context, value int64) error {
	value = NormalizeModelsListReadMaxBytes(value)
	if err := ValidateModelsListReadMaxBytes(value); err != nil {
		return err
	}

	return db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		insertQuery := `
			INSERT INTO system_settings (id) VALUES (1)
			ON CONFLICT (id) DO NOTHING
		`
		if db.isMySQL() {
			insertQuery = `INSERT IGNORE INTO system_settings (id) VALUES (1)`
		}
		if _, err := tx.ExecContext(ctx, insertQuery); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE system_settings
			SET models_list_read_max_bytes = $1
			WHERE id = 1
		`, value); err != nil {
			return err
		}
		return tx.Commit()
	})
}
