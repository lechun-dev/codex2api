-- codex2api v3.0.5 (upstream c47c1a66) additive MySQL 5.6 upgrade.
-- Back up the database and select it with USE before running this script.
-- Startup performs these migrations automatically; this script is for manual pre-deployment upgrades.
-- No existing logs/accounts/assets are removed. Safe to rerun.

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

CALL c2a_add_column_if_missing('system_settings', 'codex_turn_state_template_cache_enabled', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'codex_turn_state_account_mode', 'VARCHAR(20) DEFAULT ''auto''');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_enabled', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_models', 'TEXT NULL');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_403_pause_disabled', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_403_probe_interval_minutes', 'INT DEFAULT 1');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_429_cooldown_seconds', 'INT DEFAULT 5');
CALL c2a_add_column_if_missing('system_settings', 'codex_basispoints_cache_creation_as_input', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('system_settings', 'codex_synced_desktop_mac_build', 'TEXT NULL');
CALL c2a_add_column_if_missing('system_settings', 'codex_synced_desktop_windows_build', 'TEXT NULL');
CALL c2a_add_column_if_missing('system_settings', 'codex_synced_vscode_build', 'TEXT NULL');
CALL c2a_add_column_if_missing('system_settings', 'auto_reset_credits_on_exhaustion_enabled', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('usage_logs', 'daybreak_program', 'VARCHAR(32) NOT NULL DEFAULT ''''');
CALL c2a_add_column_if_missing('usage_logs', 'upstream_response_model', 'VARCHAR(200) NULL');
CALL c2a_add_column_if_missing('usage_logs', 'upstream_model_mismatch', 'TINYINT(1) NULL');
CALL c2a_add_column_if_missing('usage_logs', 'turn_state_overridden', 'TINYINT(1) DEFAULT 0');
CALL c2a_add_column_if_missing('usage_logs', 'turn_state_rewrite_note', 'TEXT NULL');
CALL c2a_add_column_if_missing('usage_logs', 'injected_turn_state', 'TEXT NULL');
CALL c2a_add_column_if_missing('usage_logs', 'upstream_turn_state', 'TEXT NULL');
CALL c2a_add_column_if_missing('usage_logs', 'video_seconds', 'INT DEFAULT 0');
CALL c2a_add_column_if_missing('image_generation_jobs', 'queue_owner', 'VARCHAR(255) NOT NULL DEFAULT ''''');
CALL c2a_add_column_if_missing('image_generation_jobs', 'queue_lease_until', 'BIGINT NOT NULL DEFAULT 0');
CALL c2a_add_column_if_missing('image_assets', 'expires_at', 'BIGINT NOT NULL DEFAULT 0');
CALL c2a_add_column_if_missing('image_assets', 'delete_after_read', 'TINYINT(1) NOT NULL DEFAULT 0');

ALTER TABLE system_settings MODIFY COLUMN codex_fingerprint_default_mode VARCHAR(64) DEFAULT 'off';
DROP PROCEDURE IF EXISTS c2a_add_column_if_missing;

CREATE TABLE IF NOT EXISTS daybreak_snapshots (
 account_id BIGINT NOT NULL PRIMARY KEY,
 identity TEXT NOT NULL, observed_at BIGINT NOT NULL, checked_at BIGINT NOT NULL, models_json MEDIUMTEXT NOT NULL,
 FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS channel_monitor_configs (
		account_id BIGINT NOT NULL PRIMARY KEY,
		enabled BOOLEAN NOT NULL DEFAULT FALSE,
		interval_minutes INTEGER NOT NULL DEFAULT 5,
		model VARCHAR(255) NOT NULL DEFAULT '',
		status VARCHAR(24) NOT NULL DEFAULT 'unknown',
		http_status INTEGER NOT NULL DEFAULT 0,
		latency_ms BIGINT NOT NULL DEFAULT 0,
		first_token_ms BIGINT NOT NULL DEFAULT 0,
		message TEXT NOT NULL,
		last_checked_at DATETIME NULL,
		next_check_at DATETIME NULL,
		billing_status VARCHAR(24) NOT NULL DEFAULT 'unknown',
		billing_data MEDIUMTEXT NOT NULL,
		billing_message TEXT NOT NULL,
		billing_http_status INTEGER NOT NULL DEFAULT 0,
		billing_checked_at DATETIME NULL,
		billing_success_at DATETIME NULL,
		billing_next_check_at DATETIME NULL,
		billing_failure_count INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8;

CREATE TABLE IF NOT EXISTS channel_monitor_checks (
		id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
		account_id BIGINT NOT NULL,
		status VARCHAR(24) NOT NULL,
		http_status INTEGER NOT NULL DEFAULT 0,
		model VARCHAR(255) NOT NULL DEFAULT '',
		latency_ms BIGINT NOT NULL DEFAULT 0,
		first_token_ms BIGINT NOT NULL DEFAULT 0,
		message TEXT NOT NULL,
		checked_at DATETIME NOT NULL
	, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8;

DELIMITER $$
DROP PROCEDURE IF EXISTS c2a_add_index_if_missing$$
CREATE PROCEDURE c2a_add_index_if_missing(IN p_table VARCHAR(64), IN p_name VARCHAR(64), IN p_columns VARCHAR(255))
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=p_table AND INDEX_NAME=p_name) THEN
        SET @c2a_sql=CONCAT('CREATE INDEX `',p_name,'` ON `',p_table,'` (',p_columns,')');
        PREPARE c2a_stmt FROM @c2a_sql;
        EXECUTE c2a_stmt;
        DEALLOCATE PREPARE c2a_stmt;
    END IF;
END$$
DELIMITER ;
CALL c2a_add_index_if_missing('channel_monitor_configs','idx_channel_monitor_configs_due','enabled,next_check_at');
CALL c2a_add_index_if_missing('channel_monitor_configs','idx_channel_monitor_configs_billing_due','enabled,billing_next_check_at');
CALL c2a_add_index_if_missing('channel_monitor_checks','idx_channel_monitor_checks_account_time','account_id,checked_at');
CALL c2a_add_index_if_missing('channel_monitor_checks','idx_channel_monitor_checks_time','checked_at');
CALL c2a_add_index_if_missing('image_assets','idx_image_assets_expires_id','expires_at,id');
CALL c2a_add_index_if_missing('image_assets','idx_image_assets_created_id','created_at,id');
CALL c2a_add_index_if_missing('image_generation_jobs','idx_image_jobs_queue_status_id','status,id');
CALL c2a_add_index_if_missing('image_generation_jobs','idx_image_jobs_key_created_id','api_key_id,created_at,id');
DROP PROCEDURE IF EXISTS c2a_add_index_if_missing;
