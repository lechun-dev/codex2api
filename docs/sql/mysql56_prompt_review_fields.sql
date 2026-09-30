-- Repair the seven missing prompt_filter_logs review columns (MySQL 5.6+).
-- Back up the database first. Select the application database in your client,
-- e.g. USE `lechun-codex2api`; then execute this entire file in one connection.
-- Run only one copy, with no concurrent application schema migration.
-- DDL commits implicitly and may wait for metadata locks; it cannot be rolled back.
-- Only absent columns are added, in one ALTER. Existing rows/columns are preserved.
-- No stored procedure, DELIMITER, or newer ADD COLUMN syntax is required.

SELECT GROUP_CONCAT(
    CONCAT('ADD COLUMN `', required.column_name, '` ', required.column_definition)
    ORDER BY required.ordinal SEPARATOR ', '
) INTO @c2a_prompt_review_additions
FROM (
    SELECT 1 AS ordinal, 'reviewed' AS column_name, 'TINYINT(1) DEFAULT 0' AS column_definition
    UNION ALL SELECT 2, 'review_confidence', 'DOUBLE NULL'
    UNION ALL SELECT 3, 'review_threshold', 'DOUBLE NULL'
    UNION ALL SELECT 4, 'review_reason', 'TEXT NULL'
    UNION ALL SELECT 5, 'review_endpoint', 'VARCHAR(512) DEFAULT '''''
    UNION ALL SELECT 6, 'review_request_mode', 'VARCHAR(32) DEFAULT '''''
    UNION ALL SELECT 7, 'review_latency_ms', 'BIGINT NULL'
) AS required
WHERE NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS AS existing
    WHERE existing.TABLE_SCHEMA = DATABASE()
      AND existing.TABLE_NAME = 'prompt_filter_logs'
      AND existing.COLUMN_NAME = required.column_name
);

SET @c2a_prompt_review_sql = IF(
    @c2a_prompt_review_additions IS NULL,
    'SELECT ''All seven prompt review columns already exist'' AS migration_status',
    CONCAT('ALTER TABLE `prompt_filter_logs` ', @c2a_prompt_review_additions)
);
PREPARE c2a_prompt_review_stmt FROM @c2a_prompt_review_sql;
EXECUTE c2a_prompt_review_stmt;
DEALLOCATE PREPARE c2a_prompt_review_stmt;

-- Must return seven rows. Existing column definitions are not rewritten.
SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'prompt_filter_logs'
  AND COLUMN_NAME IN (
      'reviewed', 'review_confidence', 'review_threshold', 'review_reason',
      'review_endpoint', 'review_request_mode', 'review_latency_ms'
  )
ORDER BY ORDINAL_POSITION;

-- Verify that the review fields can be queried without reading any log contents.
SELECT COALESCE(reviewed, FALSE), review_confidence, review_threshold,
       COALESCE(review_reason, ''), COALESCE(review_endpoint, ''),
       COALESCE(review_request_mode, ''), review_latency_ms
FROM prompt_filter_logs LIMIT 0;
