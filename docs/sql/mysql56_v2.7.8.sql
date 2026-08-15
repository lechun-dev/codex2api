-- codex2api v2.7.8 schema update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- The script is idempotent and only adds system settings used by v2.7.8.

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

CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_ws_stateless_slots',
    'INT DEFAULT 8'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'github_token',
    'TEXT NULL'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'github_proxy_url',
    'TEXT NULL'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_overload_pause_enabled',
    'TINYINT(1) DEFAULT 0'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_overload_threshold_percent',
    'INT DEFAULT 20'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_overload_pause_minutes',
    'INT DEFAULT 30'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_overload_window_minutes',
    'INT DEFAULT 5'
);

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
