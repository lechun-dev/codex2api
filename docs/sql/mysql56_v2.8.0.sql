-- codex2api v2.8.0 settings update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- The script is idempotent and preserves customized or enabled review settings.

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
    'prompt_filter_review_enabled',
    'TINYINT(1) DEFAULT 0'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'prompt_filter_review_api_key',
    'TEXT NULL'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'prompt_filter_review_base_url',
    'TEXT NULL'
);
CALL c2a_add_column_if_missing(
    'system_settings',
    'prompt_filter_review_model',
    'VARCHAR(100) DEFAULT ''deepseek-v4-flash'''
);

ALTER TABLE system_settings
    MODIFY COLUMN prompt_filter_review_model VARCHAR(100) DEFAULT 'deepseek-v4-flash';

UPDATE system_settings
SET prompt_filter_review_base_url = 'https://api.deepseek.com',
    prompt_filter_review_model = 'deepseek-v4-flash'
WHERE COALESCE(prompt_filter_review_api_key, '') = ''
  AND COALESCE(prompt_filter_review_enabled, 0) = 0
  AND COALESCE(prompt_filter_review_base_url, '') = 'https://api.openai.com'
  AND COALESCE(prompt_filter_review_model, '') = 'omni-moderation-latest';

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
