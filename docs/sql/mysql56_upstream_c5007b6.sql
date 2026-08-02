-- codex2api upstream c5007b6 schema update for MySQL 5.6+.
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

CREATE TABLE IF NOT EXISTS prompt_filter_newapi_bindings (
    api_key_id BIGINT NOT NULL PRIMARY KEY,
    platform_code VARCHAR(32) NOT NULL UNIQUE,
    platform_name VARCHAR(255) NOT NULL DEFAULT '',
    secret TEXT NOT NULL,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    require_signed_identity TINYINT(1) NOT NULL DEFAULT 0,
    policy_mode VARCHAR(16) NOT NULL DEFAULT 'inherit',
    policy_profile VARCHAR(16) NOT NULL DEFAULT 'inherit',
    previous_secret TEXT NOT NULL,
    previous_secret_expires_at DATETIME NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_rule_candidates (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    fingerprint VARCHAR(64) CHARACTER SET ascii NOT NULL UNIQUE,
    kind VARCHAR(16) NOT NULL DEFAULT 'pattern',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    last_source VARCHAR(64) NOT NULL DEFAULT '',
    name VARCHAR(255) NOT NULL DEFAULT '',
    category VARCHAR(100) NOT NULL DEFAULT '',
    rule_json MEDIUMTEXT NOT NULL,
    rationale TEXT NOT NULL,
    source_url TEXT NOT NULL,
    evidence_count INT NOT NULL DEFAULT 0,
    sample_preview TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    published_at DATETIME NULL,
    dismissed_at DATETIME NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_rule_candidate_evidence (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    candidate_id BIGINT NOT NULL,
    source_kind VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    source_ref TEXT NOT NULL,
    source_ref_hash VARCHAR(64) CHARACTER SET ascii NOT NULL,
    sample_preview TEXT NOT NULL,
    metadata_json MEDIUMTEXT NOT NULL,
    request_protocol VARCHAR(64) NOT NULL DEFAULT '',
    request_provider VARCHAR(64) NOT NULL DEFAULT '',
    model VARCHAR(100) NOT NULL DEFAULT '',
    api_key_id BIGINT NOT NULL DEFAULT 0,
    api_key_name VARCHAR(255) NOT NULL DEFAULT '',
    prompt_policy_incident_id VARCHAR(64) NULL,
    observed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_prompt_rule_evidence (candidate_id, source_kind, source_ref_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_policy_incidents (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    incident_id VARCHAR(64) NOT NULL UNIQUE,
    request_correlation_id VARCHAR(64) DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    attempt_index INT DEFAULT 0,
    transport VARCHAR(32) DEFAULT '',
    endpoint VARCHAR(256) DEFAULT '',
    request_protocol VARCHAR(64) DEFAULT '',
    request_provider VARCHAR(64) DEFAULT '',
    model VARCHAR(100) DEFAULT '',
    status_code INT DEFAULT 0,
    account_id BIGINT DEFAULT 0,
    account_name VARCHAR(255) DEFAULT '',
    account_platform VARCHAR(100) DEFAULT '',
    account_group_ids TEXT NULL,
    account_group_names TEXT NULL,
    api_key_id BIGINT DEFAULT 0,
    api_key_name VARCHAR(255) DEFAULT '',
    api_key_masked VARCHAR(64) DEFAULT '',
    api_key_allowed_group_ids TEXT NULL,
    api_key_allowed_group_names TEXT NULL,
    platform VARCHAR(100) DEFAULT '',
    newapi_policy_status VARCHAR(32) DEFAULT '',
    newapi_platform VARCHAR(100) DEFAULT '',
    newapi_user_id VARCHAR(255) DEFAULT '',
    newapi_request_id VARCHAR(255) DEFAULT '',
    session_hash VARCHAR(64) DEFAULT '',
    client_ip_hash VARCHAR(64) DEFAULT '',
    source_ref TEXT NULL,
    upstream_error_code VARCHAR(100) DEFAULT '',
    upstream_error TEXT NULL,
    local_evaluation_state VARCHAR(32) DEFAULT '',
    local_outcome VARCHAR(32) DEFAULT '',
    local_action VARCHAR(32) DEFAULT '',
    local_score INT NULL,
    local_raw_score INT NULL,
    local_audit_score INT NULL,
    local_audit_raw_score INT NULL,
    local_threshold INT DEFAULT 0,
    local_mode VARCHAR(32) DEFAULT '',
    local_policy_profile VARCHAR(32) DEFAULT '',
    local_reason_code VARCHAR(100) DEFAULT '',
    local_reason TEXT NULL,
    local_primary_origin VARCHAR(64) DEFAULT '',
    local_strike_eligible TINYINT(1) DEFAULT 0,
    local_review_model VARCHAR(100) DEFAULT '',
    local_review_flagged TINYINT(1) DEFAULT 0,
    local_review_error TEXT NULL,
    local_matched_patterns MEDIUMTEXT NULL,
    prompt_fingerprint VARCHAR(64) DEFAULT '',
    prompt_preview TEXT NULL,
    prompt_text MEDIUMTEXT NULL,
    prompt_available TINYINT(1) DEFAULT 0,
    local_comparison VARCHAR(32) DEFAULT '',
    candidate_id BIGINT DEFAULT 0,
    candidate_evidence_id BIGINT DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_risk_events (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    source_type VARCHAR(32) CHARACTER SET ascii NOT NULL,
    source_id VARCHAR(96) CHARACTER SET ascii NOT NULL,
    incident_id VARCHAR(64) CHARACTER SET ascii DEFAULT '',
    prompt_filter_log_id BIGINT DEFAULT 0,
    request_correlation_id VARCHAR(64) CHARACTER SET ascii DEFAULT '',
    subject_type VARCHAR(32) CHARACTER SET ascii NOT NULL,
    subject_key VARCHAR(160) CHARACTER SET ascii NOT NULL,
    subject_display VARCHAR(255) DEFAULT '',
    platform VARCHAR(100) DEFAULT '',
    is_person TINYINT(1) DEFAULT 0,
    identity_confidence INT DEFAULT 0,
    event_kind VARCHAR(64) CHARACTER SET ascii NOT NULL,
    request_risk_score INT DEFAULT 0,
    evidence_confidence INT DEFAULT 0,
    reason_code VARCHAR(100) DEFAULT '',
    action VARCHAR(32) DEFAULT '',
    local_outcome VARCHAR(32) DEFAULT '',
    local_comparison VARCHAR(32) DEFAULT '',
    endpoint VARCHAR(256) DEFAULT '',
    model VARCHAR(100) DEFAULT '',
    prompt_fingerprint VARCHAR(64) CHARACTER SET ascii DEFAULT '',
    prompt_preview TEXT NULL,
    api_key_id BIGINT DEFAULT 0,
    api_key_name VARCHAR(255) DEFAULT '',
    api_key_masked VARCHAR(64) DEFAULT '',
    account_id BIGINT DEFAULT 0,
    account_name VARCHAR(255) DEFAULT '',
    UNIQUE KEY uq_prompt_risk_event_source_subject (source_type, source_id, subject_type, subject_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_risk_event_sources (
    source_type VARCHAR(32) CHARACTER SET ascii NOT NULL,
    source_id VARCHAR(96) CHARACTER SET ascii NOT NULL,
    processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_type, source_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_risk_identities (
    subject_type VARCHAR(32) CHARACTER SET ascii NOT NULL,
    subject_key VARCHAR(160) CHARACTER SET ascii NOT NULL,
    platform VARCHAR(100) DEFAULT '',
    external_user_id VARCHAR(255) DEFAULT '',
    user_name VARCHAR(128) DEFAULT '',
    user_email VARCHAR(320) DEFAULT '',
    user_group VARCHAR(100) DEFAULT '',
    source VARCHAR(32) DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (subject_type, subject_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_risk_trust_policies (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    subject_type VARCHAR(40) NOT NULL,
    subject_key VARCHAR(128) NOT NULL UNIQUE,
    status VARCHAR(24) NOT NULL DEFAULT 'active',
    source VARCHAR(24) NOT NULL DEFAULT 'manual',
    reason TEXT NOT NULL,
    risk_threshold INT NOT NULL DEFAULT 35,
    valid_until DATETIME NOT NULL,
    last_evaluated_at DATETIME NULL,
    last_risk_score INT NOT NULL DEFAULT 0,
    last_risk_level VARCHAR(24) NOT NULL DEFAULT 'low',
    bypass_count BIGINT NOT NULL DEFAULT 0,
    last_bypass_at DATETIME NULL,
    model_review_count BIGINT NOT NULL DEFAULT 0,
    last_model_review_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS prompt_risk_trust_events (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    policy_id BIGINT NOT NULL,
    subject_type VARCHAR(40) NOT NULL,
    subject_key VARCHAR(128) NOT NULL,
    event_type VARCHAR(40) NOT NULL,
    reason TEXT NOT NULL,
    risk_score INT NOT NULL DEFAULT 0,
    risk_level VARCHAR(24) NOT NULL DEFAULT '',
    request_id_hash VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CALL c2a_add_column_if_missing('usage_logs', 'internal_reason', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('usage_logs', 'parent_request_id', CONCAT('VARCHAR(128) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('usage_logs', 'prompt_policy_incident_id', 'VARCHAR(64) NULL');
CALL c2a_add_column_if_missing('prompt_rule_candidate_evidence', 'prompt_policy_incident_id', 'VARCHAR(64) NULL');
CALL c2a_add_column_if_missing('prompt_filter_logs', 'request_correlation_id', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'newapi_policy_status', CONCAT('VARCHAR(32) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'newapi_platform', CONCAT('VARCHAR(100) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'newapi_user_id', CONCAT('VARCHAR(255) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'newapi_request_id', CONCAT('VARCHAR(255) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'newapi_decision_id', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_filter_logs', 'session_hash', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'local_reason', 'TEXT NULL');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'account_name', CONCAT('VARCHAR(255) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'account_platform', CONCAT('VARCHAR(100) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'account_group_ids', 'TEXT NULL');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'account_group_names', 'TEXT NULL');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'api_key_allowed_group_ids', 'TEXT NULL');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'api_key_allowed_group_names', 'TEXT NULL');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'prompt_available', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'local_comparison', CONCAT('VARCHAR(32) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'newapi_policy_status', CONCAT('VARCHAR(32) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'newapi_platform', CONCAT('VARCHAR(100) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'newapi_user_id', CONCAT('VARCHAR(255) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'newapi_request_id', CONCAT('VARCHAR(255) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'session_hash', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_policy_incidents', 'client_ip_hash', CONCAT('VARCHAR(64) DEFAULT ', CHAR(39), CHAR(39)));
CALL c2a_add_column_if_missing('prompt_risk_trust_policies', 'source', CONCAT('VARCHAR(24) NOT NULL DEFAULT ', CHAR(39), 'manual', CHAR(39)));
CALL c2a_add_column_if_missing('prompt_risk_trust_policies', 'model_review_count', 'BIGINT NOT NULL DEFAULT 0');
CALL c2a_add_column_if_missing('prompt_risk_trust_policies', 'last_model_review_at', 'DATETIME NULL');

CALL c2a_add_index_if_missing('prompt_rule_candidates', 'idx_prompt_rule_candidates_status_updated', '`status`, `updated_at`');
CALL c2a_add_index_if_missing('prompt_rule_candidates', 'idx_prompt_rule_candidates_source_last_seen', '`last_source`, `last_seen_at`');
CALL c2a_add_index_if_missing('prompt_rule_candidate_evidence', 'idx_prompt_rule_candidate_evidence_candidate', '`candidate_id`, `observed_at`');
CALL c2a_add_index_if_missing('prompt_rule_candidate_evidence', 'idx_prompt_rule_evidence_incident', '`prompt_policy_incident_id`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_request', '`request_correlation_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_created', '`created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_api_key', '`api_key_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_account', '`account_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_endpoint', '`endpoint`(191), `created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_outcome', '`local_outcome`, `created_at`');
CALL c2a_add_index_if_missing('prompt_policy_incidents', 'idx_prompt_policy_incidents_comparison', '`local_comparison`, `created_at`');
CALL c2a_add_index_if_missing('usage_logs', 'idx_usage_logs_policy_incident', '`prompt_policy_incident_id`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_subject', '`subject_type`, `subject_key`, `created_at`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_created', '`created_at`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_kind', '`event_kind`, `created_at`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_incident', '`incident_id`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_api_key', '`api_key_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_risk_events', 'idx_prompt_risk_events_account', '`account_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_risk_event_sources', 'idx_prompt_risk_sources_processed', '`processed_at`');
CALL c2a_add_index_if_missing('prompt_risk_identities', 'idx_prompt_risk_identities_external', '`platform`(64), `external_user_id`(128)');
CALL c2a_add_index_if_missing('prompt_risk_identities', 'idx_prompt_risk_identities_updated', '`updated_at`');
CALL c2a_add_index_if_missing('prompt_risk_trust_policies', 'idx_prompt_risk_trust_status_until', '`status`, `valid_until`');
CALL c2a_add_index_if_missing('prompt_risk_trust_events', 'idx_prompt_risk_trust_events_policy', '`policy_id`, `created_at`');
CALL c2a_add_index_if_missing('prompt_risk_trust_events', 'idx_prompt_risk_trust_events_subject', '`subject_type`, `subject_key`, `created_at`');

DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;
DROP PROCEDURE IF EXISTS c2a_add_index_if_missing;
