-- codex2api upstream 0173444 schema update for MySQL 5.6+.
-- Select the codex2api database before running this script.
-- This script is idempotent and does not alter or delete existing business data.

CREATE TABLE IF NOT EXISTS prompt_conversation_locks (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    lock_key VARCHAR(64) CHARACTER SET ascii NOT NULL UNIQUE,
    status VARCHAR(24) NOT NULL DEFAULT 'active',
    platform VARCHAR(100) NOT NULL DEFAULT '',
    newapi_user_id VARCHAR(255) NOT NULL DEFAULT '',
    session_fingerprint VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
    session_hash VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    incident_id VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
    decision_id VARCHAR(128) CHARACTER SET ascii NOT NULL DEFAULT '',
    request_id VARCHAR(255) NOT NULL DEFAULT '',
    reason_code VARCHAR(100) NOT NULL DEFAULT '',
    endpoint VARCHAR(255) NOT NULL DEFAULT '',
    model VARCHAR(128) NOT NULL DEFAULT '',
    trigger_count BIGINT NOT NULL DEFAULT 1,
    unlock_count BIGINT NOT NULL DEFAULT 0,
    locked_at DATETIME NOT NULL,
    unlocked_at DATETIME NULL,
    unlock_reason VARCHAR(1000) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    KEY idx_prompt_conversation_locks_status (status, updated_at),
    KEY idx_prompt_conversation_locks_session (session_hash, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS usage_stats_rollup (
    channel VARCHAR(32) NOT NULL PRIMARY KEY,
    total_requests BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    cache_hit_requests BIGINT NOT NULL DEFAULT 0,
    first_token_ms_sum DOUBLE NOT NULL DEFAULT 0,
    first_token_samples BIGINT NOT NULL DEFAULT 0,
    account_billed DOUBLE NOT NULL DEFAULT 0,
    user_billed DOUBLE NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS usage_stats_rollup_state (
    id INT NOT NULL PRIMARY KEY,
    initialized INT NOT NULL DEFAULT 0,
    last_log_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8;
