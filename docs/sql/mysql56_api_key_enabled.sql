-- MySQL 5.6: add the API key enabled state without deleting existing keys.
-- Existing rows remain enabled. This script is safe to run more than once.

SET @codex2api_schema = DATABASE();
SET @codex2api_enabled_exists = (
    SELECT COUNT(*)
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = @codex2api_schema
      AND TABLE_NAME = 'api_keys'
      AND COLUMN_NAME = 'enabled'
);
SET @codex2api_sql = IF(
    @codex2api_enabled_exists = 0,
    'ALTER TABLE api_keys ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 1',
    'SELECT ''api_keys.enabled already exists'''
);

PREPARE codex2api_stmt FROM @codex2api_sql;
EXECUTE codex2api_stmt;
DEALLOCATE PREPARE codex2api_stmt;
