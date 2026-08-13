-- codex2api v2.7.7 schema update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- The script is idempotent and adds the upstream credential-generation and
-- Grok state schema. It does not modify or delete existing data.

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

DROP PROCEDURE IF EXISTS c2a_add_index_if_missing$$
CREATE PROCEDURE c2a_add_index_if_missing(
    IN p_table VARCHAR(64),
    IN p_index VARCHAR(64),
    IN p_columns TEXT
)
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = p_table
          AND INDEX_NAME = p_index
    ) THEN
        SET @c2a_sql = CONCAT(
            'CREATE INDEX `', REPLACE(p_index, '`', '``'),
            '` ON `', REPLACE(p_table, '`', '``'), '` (', p_columns, ')'
        );
        PREPARE c2a_stmt FROM @c2a_sql;
        EXECUTE c2a_stmt;
        DEALLOCATE PREPARE c2a_stmt;
    END IF;
END$$

DELIMITER ;

CALL c2a_add_column_if_missing(
    'usage_logs',
    'credential_generation',
    'BIGINT NOT NULL DEFAULT 0'
);
CALL c2a_add_index_if_missing(
    'usage_logs',
    'idx_usage_logs_account_generation_created_at',
    '`account_id`, `credential_generation`, `created_at`'
);

CALL c2a_add_column_if_missing(
    'accounts',
    'credential_generation',
    'BIGINT NOT NULL DEFAULT 1'
);
CALL c2a_add_column_if_missing(
    'accounts',
    'credential_family_id',
    'VARCHAR(255) CHARACTER SET ascii NOT NULL DEFAULT '''''
);

CREATE TABLE IF NOT EXISTS grok_account_fact_snapshots (
    account_id BIGINT NOT NULL,
    fact_kind VARCHAR(64) CHARACTER SET ascii NOT NULL,
    credential_generation BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'unknown',
    http_status INT NOT NULL DEFAULT 0,
    source VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    payload_json MEDIUMTEXT NOT NULL,
    field_presence_json MEDIUMTEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, fact_kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS grok_model_catalog_snapshots (
    account_id BIGINT NOT NULL,
    origin VARCHAR(64) CHARACTER SET ascii NOT NULL,
    credential_generation BIGINT NOT NULL,
    auth_kind VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'unknown',
    http_etag VARCHAR(255) CHARACTER SET ascii NOT NULL DEFAULT '',
    etag_hint VARCHAR(255) CHARACTER SET ascii NOT NULL DEFAULT '',
    etag_hint_observed_at DATETIME NULL,
    observed_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, origin)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS grok_model_catalog_items (
    account_id BIGINT NOT NULL,
    origin VARCHAR(64) CHARACTER SET ascii NOT NULL,
    model_id VARCHAR(191) CHARACTER SET ascii NOT NULL,
    credential_generation BIGINT NOT NULL,
    display_name TEXT NOT NULL,
    description MEDIUMTEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_base_url TEXT NOT NULL,
    api_backend VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    context_window BIGINT NOT NULL DEFAULT 0,
    max_output_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_effort VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    reasoning_efforts_json MEDIUMTEXT NOT NULL,
    supports_reasoning_effort TINYINT(1) NOT NULL DEFAULT 0,
    supports_backend_search TINYINT(1) NOT NULL DEFAULT 0,
    stream_tool_calls TINYINT(1) NOT NULL DEFAULT 0,
    supported_in_api TINYINT(1) NOT NULL DEFAULT 1,
    hidden TINYINT(1) NOT NULL DEFAULT 0,
    extra_headers_json MEDIUMTEXT NOT NULL,
    field_presence_json MEDIUMTEXT NOT NULL,
    first_seen_at DATETIME NOT NULL,
    PRIMARY KEY (account_id, origin, model_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS grok_model_capabilities (
    account_id BIGINT NOT NULL,
    model_id VARCHAR(191) CHARACTER SET ascii NOT NULL,
    origin VARCHAR(64) CHARACTER SET ascii NOT NULL,
    protocol VARCHAR(32) CHARACTER SET ascii NOT NULL,
    credential_generation BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'untested',
    http_status INT NOT NULL DEFAULT 0,
    provider_code VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    source VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    retry_after_seconds BIGINT NOT NULL DEFAULT 0,
    observed_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, model_id, origin, protocol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS grok_credential_identity_claims (
    identity_key VARCHAR(255) CHARACTER SET ascii NOT NULL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS grok_state_migration_progress (
    version VARCHAR(191) CHARACTER SET ascii NOT NULL PRIMARY KEY,
    phase VARCHAR(32) NOT NULL DEFAULT 'families',
    last_account_id BIGINT NOT NULL DEFAULT 0,
    completed_at DATETIME NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CALL c2a_add_index_if_missing(
    'grok_account_fact_snapshots',
    'idx_grok_facts_expires',
    '`expires_at`'
);
CALL c2a_add_index_if_missing(
    'grok_model_catalog_items',
    'idx_grok_catalog_items_model',
    '`model_id`, `account_id`'
);
CALL c2a_add_index_if_missing(
    'grok_model_capabilities',
    'idx_grok_capabilities_expires',
    '`expires_at`'
);
CALL c2a_add_index_if_missing(
    'grok_credential_identity_claims',
    'idx_grok_identity_claims_account',
    '`account_id`'
);

DROP PROCEDURE IF EXISTS c2a_add_index_if_missing;
DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
