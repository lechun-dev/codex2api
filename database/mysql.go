package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) migrateMySQL(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) DEFAULT '',
			platform VARCHAR(50) DEFAULT 'openai',
			type VARCHAR(50) DEFAULT 'oauth',
			credentials MEDIUMTEXT NOT NULL,
			proxy_url VARCHAR(500) DEFAULT '',
			status VARCHAR(50) DEFAULT 'active',
			cooldown_reason VARCHAR(50) DEFAULT '',
			cooldown_until DATETIME NULL,
			score_bias_override INT NULL,
			base_concurrency_override INT NULL,
			skip_warm_tier TINYINT(1) DEFAULT 0,
			error_message VARCHAR(2048) DEFAULT '',
			deleted_at DATETIME NULL,
			tags TEXT NULL,
			enabled TINYINT(1) DEFAULT 1,
			locked TINYINT(1) DEFAULT 0,
			credit_enabled TINYINT(1) DEFAULT 0,
			credit_skip_usage_window TINYINT(1) DEFAULT 0,
			image_quota_remaining INT NULL,
			image_quota_total INT NULL,
			today_used_count INT DEFAULT 0,
			image_quota_reset_at DATETIME NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS usage_logs (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			account_id BIGINT DEFAULT 0,
			client_ip VARCHAR(64) DEFAULT '',
			session_id VARCHAR(255) DEFAULT '',
			conversation_id VARCHAR(255) DEFAULT '',
			previous_response_id VARCHAR(255) DEFAULT '',
			request_text MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL,
			endpoint VARCHAR(100) DEFAULT '',
			model VARCHAR(100) DEFAULT '',
			prompt_tokens INT DEFAULT 0,
			completion_tokens INT DEFAULT 0,
			total_tokens INT DEFAULT 0,
			status_code INT DEFAULT 0,
			duration_ms INT DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			input_tokens INT DEFAULT 0,
			output_tokens INT DEFAULT 0,
			reasoning_tokens INT DEFAULT 0,
			first_token_ms INT DEFAULT 0,
			reasoning_effort VARCHAR(20) DEFAULT '',
			effective_model VARCHAR(100) DEFAULT '',
			inbound_endpoint VARCHAR(100) DEFAULT '',
			upstream_endpoint VARCHAR(100) DEFAULT '',
			stream TINYINT(1) DEFAULT 0,
			compact TINYINT(1) DEFAULT 0,
			via_websocket TINYINT(1) DEFAULT 0,
			cached_tokens INT DEFAULT 0,
			service_tier VARCHAR(20) DEFAULT '',
			requested_service_tier VARCHAR(20) DEFAULT '',
			actual_service_tier VARCHAR(20) DEFAULT '',
			billing_service_tier VARCHAR(20) DEFAULT '',
			api_key_id BIGINT DEFAULT 0,
			api_key_name VARCHAR(255) DEFAULT '',
			api_key_masked VARCHAR(64) DEFAULT '',
			image_count INT DEFAULT 0,
			image_width INT DEFAULT 0,
			image_height INT DEFAULT 0,
			image_bytes INT DEFAULT 0,
			image_format VARCHAR(20) DEFAULT '',
			image_size VARCHAR(32) DEFAULT '',
			account_billed DOUBLE DEFAULT 0,
			user_billed DOUBLE DEFAULT 0,
			is_retry_attempt TINYINT(1) DEFAULT 0,
			attempt_index INT DEFAULT 0,
			upstream_error_kind VARCHAR(64) DEFAULT '',
			error_message VARCHAR(2048) DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) DEFAULT '',
			` + "`key`" + ` VARCHAR(255) NOT NULL UNIQUE,
			quota_limit DOUBLE DEFAULT 0,
			quota_used DOUBLE DEFAULT 0,
			total_used DOUBLE DEFAULT 0,
			reset_count INT DEFAULT 0,
			last_reset_at DATETIME NULL,
			allowed_group_ids TEXT NULL,
			limits TEXT NULL,
			expires_at DATETIME NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS account_groups (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(80) NOT NULL UNIQUE,
			description VARCHAR(1024) DEFAULT '',
			color VARCHAR(20) DEFAULT '',
			sort_order INT DEFAULT 0,
			auto_pause_5h_threshold DOUBLE DEFAULT 0,
			auto_pause_7d_threshold DOUBLE DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS account_group_members (
			account_id BIGINT NOT NULL,
			group_id BIGINT NOT NULL,
			PRIMARY KEY (account_id, group_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS account_model_cooldowns (
			account_id BIGINT NOT NULL,
			model VARCHAR(100) NOT NULL,
			reason VARCHAR(64) DEFAULT '',
			reset_at DATETIME NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (account_id, model)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS system_settings (
			id INT NOT NULL PRIMARY KEY,
			site_name VARCHAR(255) DEFAULT 'CodexProxy',
			site_logo VARCHAR(1024) DEFAULT '',
			background_config TEXT NULL,
			max_concurrency INT DEFAULT 2,
			global_rpm INT DEFAULT 0,
			test_model VARCHAR(100) DEFAULT 'gpt-5.4',
			test_concurrency INT DEFAULT 50,
			proxy_url VARCHAR(500) DEFAULT '',
			pg_max_conns INT DEFAULT 50,
			redis_pool_size INT DEFAULT 30,
			auto_clean_unauthorized TINYINT(1) DEFAULT 0,
			auto_clean_rate_limited TINYINT(1) DEFAULT 0,
				background_refresh_interval_minutes INT DEFAULT 2,
				usage_probe_max_age_minutes INT DEFAULT 10,
				usage_probe_concurrency INT DEFAULT 16,
				usage_probe_responses_fallback_enabled TINYINT(1) DEFAULT 1,
				recovery_probe_interval_minutes INT DEFAULT 30,
			admin_secret VARCHAR(255) DEFAULT '',
			auto_clean_full_usage TINYINT(1) DEFAULT 0,
			auto_clean_error TINYINT(1) DEFAULT 0,
			auto_clean_expired TINYINT(1) DEFAULT 0,
			lazy_mode TINYINT(1) DEFAULT 0,
			proxy_pool_enabled TINYINT(1) DEFAULT 0,
			fast_scheduler_enabled TINYINT(1) DEFAULT 0,
			max_retries INT DEFAULT 2,
			max_rate_limit_retries INT DEFAULT 1,
			reasoning_effort_models TEXT NULL,
			allow_remote_migration TINYINT(1) DEFAULT 0,
			model_mapping TEXT NULL,
			codex_model_mapping TEXT NULL,
			resin_url TEXT NULL,
			resin_platform_name VARCHAR(255) DEFAULT '',
			prompt_filter_enabled TINYINT(1) DEFAULT 0,
			prompt_filter_mode VARCHAR(20) DEFAULT 'monitor',
			prompt_filter_threshold INT DEFAULT 50,
			prompt_filter_strict_threshold INT DEFAULT 90,
			prompt_filter_log_matches TINYINT(1) DEFAULT 1,
			prompt_filter_max_text_length INT DEFAULT 81920,
			prompt_filter_sensitive_words TEXT NULL,
			prompt_filter_custom_patterns TEXT NULL,
			prompt_filter_disabled_patterns TEXT NULL,
			prompt_filter_review_enabled TINYINT(1) DEFAULT 0,
			prompt_filter_review_api_key TEXT NULL,
			prompt_filter_review_base_url TEXT NULL,
			prompt_filter_review_model VARCHAR(100) DEFAULT 'omni-moderation-latest',
			prompt_filter_review_timeout_seconds INT DEFAULT 10,
			prompt_filter_review_fail_closed TINYINT(1) DEFAULT 1,
			client_compat_mode VARCHAR(20) DEFAULT 'preserve',
			codex_min_cli_version VARCHAR(32) DEFAULT '0.118.0',
			usage_log_mode VARCHAR(20) DEFAULT 'full',
			usage_log_batch_size INT DEFAULT 200,
			usage_log_flush_interval_seconds INT DEFAULT 5,
			stream_flush_policy VARCHAR(20) DEFAULT 'immediate',
			stream_flush_interval_ms INT DEFAULT 20,
			first_token_mode VARCHAR(20) DEFAULT 'strict',
			first_token_timeout_seconds INT DEFAULT 0,
			billing_tier_policy VARCHAR(20) DEFAULT 'actual',
			image_storage_config TEXT NULL,
			show_full_usage_numbers TINYINT(1) DEFAULT 0,
			public_key_usage_page_enabled TINYINT(1) DEFAULT 1,
			auto_pause_5h_threshold DOUBLE DEFAULT 0,
			auto_pause_7d_threshold DOUBLE DEFAULT 0,
			auto_pause_5h_guard_band_percent DOUBLE DEFAULT 5,
			auto_pause_5h_guard_concurrency INT DEFAULT 1,
			scheduler_mode VARCHAR(20) DEFAULT 'round_robin',
			affinity_mode VARCHAR(16) DEFAULT 'bounded',
				codex_force_websocket TINYINT(1) DEFAULT 0,
				codex_ws_keepalive_enabled TINYINT(1) DEFAULT 0,
				codex_ws_keepalive_interval_sec INT DEFAULT 60,
				codex_ws_hide_upstream_errors TINYINT(1) DEFAULT 1,
				codex_ws_silent_retry_enabled TINYINT(1) DEFAULT 1,
				codex_ws_silent_max_retries INT DEFAULT 2
			) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS model_registry (
			id VARCHAR(100) NOT NULL PRIMARY KEY,
			enabled TINYINT(1) DEFAULT 1,
			category VARCHAR(50) DEFAULT 'codex',
			source VARCHAR(50) DEFAULT 'manual',
			pro_only TINYINT(1) DEFAULT 0,
			api_key_auth_available TINYINT(1) DEFAULT 1,
			last_seen_at DATETIME NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS model_registry_sync (
			id INT NOT NULL PRIMARY KEY,
			source_url TEXT NULL,
			last_synced_at DATETIME NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS proxies (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			url VARCHAR(255) NOT NULL,
			label VARCHAR(255) DEFAULT '',
			enabled TINYINT(1) DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			test_ip VARCHAR(100) DEFAULT '',
			test_location VARCHAR(255) DEFAULT '',
			test_latency_ms INT DEFAULT 0,
			UNIQUE KEY uniq_proxies_url (url)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS account_events (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			account_id BIGINT NOT NULL DEFAULT 0,
			event_type VARCHAR(20) NOT NULL,
			source VARCHAR(30) DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS image_prompt_templates (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL DEFAULT '',
			prompt MEDIUMTEXT NOT NULL,
			model VARCHAR(100) DEFAULT '',
			size VARCHAR(32) DEFAULT '',
			quality VARCHAR(32) DEFAULT '',
			output_format VARCHAR(32) DEFAULT '',
			background VARCHAR(32) DEFAULT '',
			style VARCHAR(64) DEFAULT '',
			tags TEXT NULL,
			favorite TINYINT(1) DEFAULT 0,
			usage_count INT DEFAULT 0,
			last_used_at DATETIME NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS image_generation_jobs (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			status VARCHAR(32) NOT NULL DEFAULT 'queued',
			prompt MEDIUMTEXT NOT NULL,
			params_json TEXT NULL,
			api_key_id BIGINT DEFAULT 0,
			api_key_name VARCHAR(255) DEFAULT '',
			api_key_masked VARCHAR(64) DEFAULT '',
			error_message TEXT NULL,
			duration_ms INT DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME NULL,
			completed_at DATETIME NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS image_assets (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			job_id BIGINT NOT NULL DEFAULT 0,
			template_id BIGINT DEFAULT 0,
			filename VARCHAR(255) NOT NULL DEFAULT '',
			storage_path TEXT NULL,
			mime_type VARCHAR(100) NOT NULL DEFAULT '',
			bytes INT DEFAULT 0,
			width INT DEFAULT 0,
			height INT DEFAULT 0,
			model VARCHAR(100) DEFAULT '',
			requested_size VARCHAR(32) DEFAULT '',
			actual_size VARCHAR(32) DEFAULT '',
			quality VARCHAR(32) DEFAULT '',
			output_format VARCHAR(32) DEFAULT '',
			revised_prompt MEDIUMTEXT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
		`CREATE TABLE IF NOT EXISTS prompt_filter_logs (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			source VARCHAR(50) DEFAULT '',
			endpoint VARCHAR(100) DEFAULT '',
			model VARCHAR(100) DEFAULT '',
			action VARCHAR(20) DEFAULT '',
			mode VARCHAR(20) DEFAULT '',
			score INT DEFAULT 0,
			threshold_value INT DEFAULT 0,
			matched_patterns TEXT NULL,
			text_preview TEXT NULL,
			api_key_id BIGINT DEFAULT 0,
			api_key_name VARCHAR(255) DEFAULT '',
			api_key_masked VARCHAR(64) DEFAULT '',
			client_ip VARCHAR(64) DEFAULT '',
			error_code VARCHAR(100) DEFAULT '',
			review_model VARCHAR(100) DEFAULT '',
			review_flagged TINYINT(1) DEFAULT 0,
			review_error TEXT NULL,
			full_text MEDIUMTEXT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8`,
	}
	for _, stmt := range statements {
		if _, err := db.conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	columns := []struct {
		table string
		name  string
		def   string
	}{
		{"accounts", "cooldown_reason", "VARCHAR(50) DEFAULT ''"},
		{"accounts", "cooldown_until", "DATETIME NULL"},
		{"accounts", "score_bias_override", "INT NULL"},
		{"accounts", "base_concurrency_override", "INT NULL"},
		{"accounts", "tags", "TEXT NULL"},
		{"accounts", "deleted_at", "DATETIME NULL"},
		{"accounts", "enabled", "TINYINT(1) DEFAULT 1"},
		{"accounts", "locked", "TINYINT(1) DEFAULT 0"},
		{"accounts", "credit_enabled", "TINYINT(1) DEFAULT 0"},
		{"accounts", "credit_skip_usage_window", "TINYINT(1) DEFAULT 0"},
		{"accounts", "skip_warm_tier", "TINYINT(1) DEFAULT 0"},
		{"accounts", "image_quota_remaining", "INT NULL"},
		{"accounts", "image_quota_total", "INT NULL"},
		{"accounts", "today_used_count", "INT DEFAULT 0"},
		{"accounts", "image_quota_reset_at", "DATETIME NULL"},
		{"usage_logs", "client_ip", "VARCHAR(64) DEFAULT ''"},
		{"usage_logs", "input_tokens", "INT DEFAULT 0"},
		{"usage_logs", "output_tokens", "INT DEFAULT 0"},
		{"usage_logs", "reasoning_tokens", "INT DEFAULT 0"},
		{"usage_logs", "first_token_ms", "INT DEFAULT 0"},
		{"usage_logs", "reasoning_effort", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "effective_model", "VARCHAR(100) DEFAULT ''"},
		{"usage_logs", "inbound_endpoint", "VARCHAR(100) DEFAULT ''"},
		{"usage_logs", "upstream_endpoint", "VARCHAR(100) DEFAULT ''"},
		{"usage_logs", "stream", "TINYINT(1) DEFAULT 0"},
		{"usage_logs", "compact", "TINYINT(1) DEFAULT 0"},
		{"usage_logs", "via_websocket", "TINYINT(1) DEFAULT 0"},
		{"usage_logs", "cached_tokens", "INT DEFAULT 0"},
		{"usage_logs", "service_tier", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "requested_service_tier", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "actual_service_tier", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "billing_service_tier", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "api_key_id", "BIGINT DEFAULT 0"},
		{"usage_logs", "api_key_name", "VARCHAR(255) DEFAULT ''"},
		{"usage_logs", "api_key_masked", "VARCHAR(64) DEFAULT ''"},
		{"usage_logs", "image_count", "INT DEFAULT 0"},
		{"usage_logs", "session_id", "VARCHAR(255) DEFAULT ''"},
		{"usage_logs", "conversation_id", "VARCHAR(255) DEFAULT ''"},
		{"usage_logs", "previous_response_id", "VARCHAR(255) DEFAULT ''"},
		{"usage_logs", "request_text", "MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL"},
		{"usage_logs", "image_width", "INT DEFAULT 0"},
		{"usage_logs", "image_height", "INT DEFAULT 0"},
		{"usage_logs", "image_bytes", "INT DEFAULT 0"},
		{"usage_logs", "image_format", "VARCHAR(20) DEFAULT ''"},
		{"usage_logs", "image_size", "VARCHAR(32) DEFAULT ''"},
		{"usage_logs", "account_billed", "DOUBLE DEFAULT 0"},
		{"usage_logs", "user_billed", "DOUBLE DEFAULT 0"},
		{"usage_logs", "is_retry_attempt", "TINYINT(1) DEFAULT 0"},
		{"usage_logs", "attempt_index", "INT DEFAULT 0"},
		{"usage_logs", "upstream_error_kind", "VARCHAR(64) DEFAULT ''"},
		{"usage_logs", "error_message", "VARCHAR(2048) DEFAULT ''"},
		{"api_keys", "total_used", "DOUBLE DEFAULT 0"},
		{"api_keys", "reset_count", "INT DEFAULT 0"},
		{"api_keys", "last_reset_at", "DATETIME NULL"},
		{"api_keys", "allowed_group_ids", "TEXT NULL"},
		{"api_keys", "limits", "TEXT NULL"},
		{"api_keys", "expires_at", "DATETIME NULL"},
		{"account_groups", "description", "VARCHAR(1024) DEFAULT ''"},
		{"account_groups", "color", "VARCHAR(20) DEFAULT ''"},
		{"account_groups", "sort_order", "INT DEFAULT 0"},
		{"account_groups", "created_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"account_groups", "updated_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"account_groups", "auto_pause_5h_threshold", "DOUBLE DEFAULT 0"},
		{"account_groups", "auto_pause_7d_threshold", "DOUBLE DEFAULT 0"},
		{"system_settings", "site_name", "VARCHAR(255) DEFAULT 'CodexProxy'"},
		{"system_settings", "site_logo", "VARCHAR(1024) DEFAULT ''"},
		{"system_settings", "reasoning_effort_models", "TEXT NULL"},
		{"system_settings", "background_config", "TEXT NULL"},
		{"system_settings", "pg_max_conns", "INT DEFAULT 50"},
		{"system_settings", "redis_pool_size", "INT DEFAULT 30"},
		{"system_settings", "auto_clean_unauthorized", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "auto_clean_rate_limited", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "background_refresh_interval_minutes", "INT DEFAULT 2"},
		{"system_settings", "usage_probe_max_age_minutes", "INT DEFAULT 10"},
		{"system_settings", "usage_probe_concurrency", "INT DEFAULT 16"},
		{"system_settings", "usage_probe_responses_fallback_enabled", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "recovery_probe_interval_minutes", "INT DEFAULT 30"},
		{"system_settings", "admin_secret", "VARCHAR(255) DEFAULT ''"},
		{"system_settings", "auto_clean_full_usage", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "auto_clean_error", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "auto_clean_expired", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "lazy_mode", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "proxy_pool_enabled", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "fast_scheduler_enabled", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "max_retries", "INT DEFAULT 2"},
		{"system_settings", "max_rate_limit_retries", "INT DEFAULT 1"},
		{"system_settings", "allow_remote_migration", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "model_mapping", "TEXT NULL"},
		{"system_settings", "codex_model_mapping", "TEXT NULL"},
		{"system_settings", "resin_url", "TEXT NULL"},
		{"system_settings", "resin_platform_name", "VARCHAR(255) DEFAULT ''"},
		{"system_settings", "prompt_filter_enabled", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "prompt_filter_mode", "VARCHAR(20) DEFAULT 'monitor'"},
		{"system_settings", "prompt_filter_threshold", "INT DEFAULT 50"},
		{"system_settings", "prompt_filter_strict_threshold", "INT DEFAULT 90"},
		{"system_settings", "prompt_filter_log_matches", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "prompt_filter_max_text_length", "INT DEFAULT 81920"},
		{"system_settings", "prompt_filter_sensitive_words", "TEXT NULL"},
		{"system_settings", "prompt_filter_custom_patterns", "TEXT NULL"},
		{"system_settings", "prompt_filter_disabled_patterns", "TEXT NULL"},
		{"system_settings", "prompt_filter_review_enabled", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "prompt_filter_review_api_key", "TEXT NULL"},
		{"system_settings", "prompt_filter_review_base_url", "TEXT NULL"},
		{"system_settings", "prompt_filter_review_model", "VARCHAR(100) DEFAULT 'omni-moderation-latest'"},
		{"system_settings", "prompt_filter_review_timeout_seconds", "INT DEFAULT 10"},
		{"system_settings", "prompt_filter_review_fail_closed", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "image_storage_config", "TEXT NULL"},
		{"system_settings", "billing_tier_policy", "VARCHAR(20) DEFAULT 'actual'"},
		{"system_settings", "show_full_usage_numbers", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "public_key_usage_page_enabled", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "scheduler_mode", "VARCHAR(20) DEFAULT 'round_robin'"},
		{"system_settings", "affinity_mode", "VARCHAR(16) DEFAULT 'bounded'"},
		{"system_settings", "client_compat_mode", "VARCHAR(20) DEFAULT 'preserve'"},
		{"system_settings", "codex_min_cli_version", "VARCHAR(32) DEFAULT '0.118.0'"},
		{"system_settings", "usage_log_mode", "VARCHAR(20) DEFAULT 'full'"},
		{"system_settings", "usage_log_batch_size", "INT DEFAULT 200"},
		{"system_settings", "usage_log_flush_interval_seconds", "INT DEFAULT 5"},
		{"system_settings", "stream_flush_policy", "VARCHAR(20) DEFAULT 'immediate'"},
		{"system_settings", "stream_flush_interval_ms", "INT DEFAULT 20"},
		{"system_settings", "first_token_mode", "VARCHAR(20) DEFAULT 'strict'"},
		{"system_settings", "first_token_timeout_seconds", "INT DEFAULT 0"},
		{"system_settings", "auto_pause_5h_threshold", "DOUBLE DEFAULT 0"},
		{"system_settings", "auto_pause_7d_threshold", "DOUBLE DEFAULT 0"},
		{"system_settings", "auto_pause_5h_guard_band_percent", "DOUBLE DEFAULT 5"},
		{"system_settings", "auto_pause_5h_guard_concurrency", "INT DEFAULT 1"},
		{"prompt_filter_logs", "review_model", "VARCHAR(100) DEFAULT ''"},
		{"prompt_filter_logs", "review_flagged", "TINYINT(1) DEFAULT 0"},
		{"prompt_filter_logs", "review_error", "TEXT NULL"},
		{"prompt_filter_logs", "full_text", "MEDIUMTEXT NULL"},
		{"system_settings", "codex_force_websocket", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "codex_ws_keepalive_enabled", "TINYINT(1) DEFAULT 0"},
		{"system_settings", "codex_ws_keepalive_interval_sec", "INT DEFAULT 60"},
		{"system_settings", "codex_ws_hide_upstream_errors", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "codex_ws_silent_retry_enabled", "TINYINT(1) DEFAULT 1"},
		{"system_settings", "codex_ws_silent_max_retries", "INT DEFAULT 2"},
		{"proxies", "test_ip", "VARCHAR(100) DEFAULT ''"},
		{"proxies", "test_location", "VARCHAR(255) DEFAULT ''"},
		{"proxies", "test_latency_ms", "INT DEFAULT 0"},
	}
	for _, column := range columns {
		if err := db.ensureMySQLColumn(ctx, column.table, column.name, column.def); err != nil {
			return err
		}
	}

	indexes := []struct {
		table string
		name  string
		stmt  string
	}{
		{"accounts", "idx_accounts_status", "CREATE INDEX idx_accounts_status ON accounts(status)"},
		{"accounts", "idx_accounts_platform", "CREATE INDEX idx_accounts_platform ON accounts(platform)"},
		{"accounts", "idx_accounts_cooldown_until", "CREATE INDEX idx_accounts_cooldown_until ON accounts(cooldown_until)"},
		{"usage_logs", "idx_usage_logs_created_at", "CREATE INDEX idx_usage_logs_created_at ON usage_logs(created_at)"},
		{"usage_logs", "idx_usage_logs_account_id", "CREATE INDEX idx_usage_logs_account_id ON usage_logs(account_id)"},
		{"usage_logs", "idx_usage_logs_created_status", "CREATE INDEX idx_usage_logs_created_status ON usage_logs(created_at, status_code)"},
		{"usage_logs", "idx_usage_logs_account_status", "CREATE INDEX idx_usage_logs_account_status ON usage_logs(account_id, status_code)"},
		{"usage_logs", "idx_usage_logs_api_key_created_at", "CREATE INDEX idx_usage_logs_api_key_created_at ON usage_logs(api_key_id, created_at)"},
		{"api_keys", "idx_api_keys_expires_at", "CREATE INDEX idx_api_keys_expires_at ON api_keys(expires_at)"},
		{"account_group_members", "idx_account_group_members_group", "CREATE INDEX idx_account_group_members_group ON account_group_members(group_id)"},
		{"account_group_members", "idx_account_group_members_account", "CREATE INDEX idx_account_group_members_account ON account_group_members(account_id)"},
		{"account_model_cooldowns", "idx_account_model_cooldowns_reset_at", "CREATE INDEX idx_account_model_cooldowns_reset_at ON account_model_cooldowns(reset_at)"},
		{"account_events", "idx_account_events_created", "CREATE INDEX idx_account_events_created ON account_events(created_at)"},
		{"account_events", "idx_account_events_type_created", "CREATE INDEX idx_account_events_type_created ON account_events(event_type, created_at)"},
		{"image_prompt_templates", "idx_image_prompt_templates_updated", "CREATE INDEX idx_image_prompt_templates_updated ON image_prompt_templates(updated_at)"},
		{"image_prompt_templates", "idx_image_prompt_templates_favorite", "CREATE INDEX idx_image_prompt_templates_favorite ON image_prompt_templates(favorite, updated_at)"},
		{"image_generation_jobs", "idx_image_generation_jobs_created", "CREATE INDEX idx_image_generation_jobs_created ON image_generation_jobs(created_at)"},
		{"image_generation_jobs", "idx_image_generation_jobs_status", "CREATE INDEX idx_image_generation_jobs_status ON image_generation_jobs(status, created_at)"},
		{"image_assets", "idx_image_assets_created", "CREATE INDEX idx_image_assets_created ON image_assets(created_at)"},
		{"image_assets", "idx_image_assets_job_id", "CREATE INDEX idx_image_assets_job_id ON image_assets(job_id)"},
		{"prompt_filter_logs", "idx_prompt_filter_logs_created_at", "CREATE INDEX idx_prompt_filter_logs_created_at ON prompt_filter_logs(created_at)"},
		{"prompt_filter_logs", "idx_prompt_filter_logs_action_created_at", "CREATE INDEX idx_prompt_filter_logs_action_created_at ON prompt_filter_logs(action, created_at)"},
	}
	for _, idx := range indexes {
		if err := db.ensureMySQLIndex(ctx, idx.table, idx.name, idx.stmt); err != nil {
			return err
		}
	}

	_, err := db.conn.ExecContext(ctx, `
		UPDATE accounts
		SET status = 'deleted',
			error_message = '',
			cooldown_reason = '',
			cooldown_until = NULL,
			deleted_at = COALESCE(deleted_at, updated_at, CURRENT_TIMESTAMP),
			updated_at = CURRENT_TIMESTAMP
		WHERE status <> 'deleted' AND COALESCE(error_message, '') = 'deleted'
	`)
	return err
}

func (db *DB) ensureMySQLColumn(ctx context.Context, table, name, columnDef string) error {
	var count int
	if err := db.conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = ?
		  AND COLUMN_NAME = ?
	`, table, name).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s", table, name, columnDef))
	return err
}

func (db *DB) ensureMySQLIndex(ctx context.Context, table, name, stmt string) error {
	var count int
	if err := db.conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = ?
		  AND INDEX_NAME = ?
	`, table, name).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.conn.ExecContext(ctx, stmt)
	return err
}

func (db *DB) resetMySQLAutoIncrement(ctx context.Context, table string) error {
	_, err := db.conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE `%s` AUTO_INCREMENT = 1", table))
	if err == nil || err == sql.ErrNoRows {
		return nil
	}
	return err
}

func (db *DB) getTrafficSnapshotMySQL(ctx context.Context) (*TrafficSnapshot, error) {
	snapshot := &TrafficSnapshot{}
	if err := db.conn.QueryRowContext(ctx, `
		SELECT
			COUNT(*) / 10.0 AS qps,
			COALESCE(SUM(total_tokens), 0) / 10.0 AS tps
		FROM usage_logs
		WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 10 SECOND
	`).Scan(&snapshot.QPS, &snapshot.TPS); err != nil {
		return nil, err
	}

	if err := db.conn.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(req_count), 0), COALESCE(MAX(token_count), 0)
		FROM (
			SELECT
				COUNT(*) AS req_count,
				COALESCE(SUM(total_tokens), 0) AS token_count
			FROM usage_logs
			WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 5 MINUTE
			GROUP BY UNIX_TIMESTAMP(created_at)
		) per_second
	`).Scan(&snapshot.QPSPeak, &snapshot.TPSPeak); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (db *DB) getChartAggregationMySQL(ctx context.Context, start, end time.Time, bucketMinutes int) (*ChartAggregation, error) {
	if bucketMinutes < 1 {
		bucketMinutes = 5
	}
	result := &ChartAggregation{}
	startArg, endArg := db.timeRangeArgs(start, end)

	timelineQuery := `
	SELECT
		CONCAT(DATE_FORMAT(FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(created_at) / (? * 60)) * (? * 60)), '%Y-%m-%dT%H:%i:%s'), 'Z') AS bucket,
		COUNT(*)                              AS requests,
		COALESCE(AVG(duration_ms), 0)         AS avg_latency,
		COALESCE(SUM(input_tokens), 0)        AS input_tokens,
		COALESCE(SUM(output_tokens), 0)       AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0)    AS reasoning_tokens,
		COALESCE(SUM(cached_tokens), 0)       AS cached_tokens,
		COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS errors_4xx,
		COALESCE(SUM(CASE WHEN status_code >= 500 AND status_code < 600 THEN 1 ELSE 0 END), 0) AS errors_5xx
	FROM usage_logs
	WHERE created_at >= ? AND created_at <= ?
	  AND status_code <> 499
	GROUP BY bucket
	ORDER BY bucket`
	rows, err := db.conn.QueryContext(ctx, timelineQuery, bucketMinutes, bucketMinutes, startArg, endArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ChartTimelinePoint
		if err := rows.Scan(&p.Bucket, &p.Requests, &p.AvgLatency, &p.InputTokens, &p.OutputTokens, &p.ReasoningTokens, &p.CachedTokens, &p.Errors4xx, &p.Errors5xx); err != nil {
			return nil, err
		}
		result.Timeline = append(result.Timeline, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result.Timeline == nil {
		result.Timeline = []ChartTimelinePoint{}
	}

	modelRows, err := db.conn.QueryContext(ctx, `
		SELECT COALESCE(model, 'unknown'), COUNT(*) AS requests
		FROM usage_logs
		WHERE created_at >= ? AND created_at <= ?
		  AND status_code <> 499
		GROUP BY model
		ORDER BY requests DESC
		LIMIT 10
	`, startArg, endArg)
	if err != nil {
		return nil, err
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var p ChartModelPoint
		if err := modelRows.Scan(&p.Model, &p.Requests); err != nil {
			return nil, err
		}
		result.Models = append(result.Models, p)
	}
	if err := modelRows.Err(); err != nil {
		return nil, err
	}
	if result.Models == nil {
		result.Models = []ChartModelPoint{}
	}
	return result, nil
}

func (db *DB) getAccountEventTrendMySQL(ctx context.Context, start, end time.Time, bucketMinutes int) ([]AccountEventPoint, error) {
	if bucketMinutes < 1 {
		bucketMinutes = 60
	}
	startArg, endArg := db.timeRangeArgs(start, end)
	rows, err := db.conn.QueryContext(ctx, `
		SELECT
			DATE_FORMAT(FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(created_at) / (? * 60)) * (? * 60)), '%Y-%m-%dT%H:%i:%s') AS bucket,
			COALESCE(SUM(CASE WHEN event_type = 'added' THEN 1 ELSE 0 END), 0) AS added,
			COALESCE(SUM(CASE WHEN event_type = 'deleted' AND source = 'manual' THEN 1 ELSE 0 END), 0) AS deleted
		FROM account_events
		WHERE created_at >= ? AND created_at <= ?
		  AND (event_type = 'added' OR (event_type = 'deleted' AND source = 'manual'))
		GROUP BY bucket
		ORDER BY bucket
	`, bucketMinutes, bucketMinutes, startArg, endArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AccountEventPoint
	for rows.Next() {
		var p AccountEventPoint
		if err := rows.Scan(&p.Bucket, &p.Added, &p.Deleted); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []AccountEventPoint{}
	}
	return result, nil
}

func (db *DB) getAccountsBilledSinceChunkMySQL(ctx context.Context, ids []int64, windows map[int64]time.Time, result map[int64]float64) error {
	clauses := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids)*2)
	for _, accountID := range ids {
		clauses = append(clauses, "(account_id = ? AND created_at >= ?)")
		args = append(args, accountID, db.timeArg(windows[accountID]))
	}
	query := fmt.Sprintf(`
		SELECT account_id, COALESCE(SUM(account_billed), 0) AS account_billed
		FROM usage_logs
		WHERE status_code <> 499
		  AND (%s)
		GROUP BY account_id
	`, strings.Join(clauses, " OR "))

	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID int64
		var billed float64
		if err := rows.Scan(&accountID, &billed); err != nil {
			return err
		}
		result[accountID] = billed
	}
	return rows.Err()
}
