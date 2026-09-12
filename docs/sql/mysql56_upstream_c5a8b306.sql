-- codex2api upstream c5a8b306 schema update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- The script is idempotent and may be run again after an interrupted deployment.
-- Covers:
--   1) Codex telemetry / OAuth keepalive settings columns
--   2) codex_oauth_refresh_attempts refresh protection table

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
    'codex_telemetry_enabled',
    'TINYINT(1) DEFAULT 0'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_oauth_keepalive_enabled',
    'TINYINT(1) DEFAULT 0'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'codex_telemetry_timing_debug',
    'TINYINT(1) DEFAULT 0'
);

CREATE TABLE IF NOT EXISTS codex_oauth_refresh_attempts (
  rt_fingerprint VARCHAR(64) NOT NULL,
  attempt_id VARCHAR(64) NOT NULL,
  started_at BIGINT NOT NULL,
  PRIMARY KEY (rt_fingerprint)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
