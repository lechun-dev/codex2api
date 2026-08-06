-- codex2api v2.7.1 schema update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- The script is idempotent and may be run again after an interrupted deployment.

DELIMITER $$

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing$$
CREATE PROCEDURE c2a_add_column_if_missing(
    IN p_table VARCHAR(64),
    IN p_column VARCHAR(64),
    IN p_definition TEXT
)
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = p_table
          AND COLUMN_NAME = p_column
    ) THEN
        SET @c2a_sql = CONCAT(
            'ALTER TABLE `', REPLACE(p_table, '`', '``'),
            '` ADD COLUMN `', REPLACE(p_column, '`', '``'),
            '` ', p_definition
        );
        PREPARE c2a_stmt FROM @c2a_sql;
        EXECUTE c2a_stmt;
        DEALLOCATE PREPARE c2a_stmt;
    END IF;
END$$

DELIMITER ;

CALL c2a_add_column_if_missing('account_groups', 'proxy_urls', 'TEXT NULL');
CALL c2a_add_column_if_missing('system_settings', 'session_affinity_spread', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'relay_model_cooldown_mode', CONCAT('VARCHAR(20) NOT NULL DEFAULT ', CHAR(39), 'off', CHAR(39)));
CALL c2a_add_column_if_missing('system_settings', 'relay_model_cooldown_seconds', 'INT NOT NULL DEFAULT 2');
CALL c2a_add_column_if_missing('system_settings', 'relay_model_cooldown_backoff_enabled', 'TINYINT(1) NOT NULL DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'oauth_model_cooldown_mode', CONCAT('VARCHAR(20) NOT NULL DEFAULT ', CHAR(39), 'adaptive', CHAR(39)));
CALL c2a_add_column_if_missing('system_settings', 'oauth_model_cooldown_seconds', 'INT NOT NULL DEFAULT 300');
CALL c2a_add_column_if_missing('system_settings', 'oauth_model_cooldown_backoff_enabled', 'TINYINT(1) NOT NULL DEFAULT 1');
CALL c2a_add_column_if_missing('prompt_filter_newapi_bindings', 'prompt_filter_scope', CONCAT('VARCHAR(16) NOT NULL DEFAULT ', CHAR(39), 'inherit', CHAR(39)));

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
