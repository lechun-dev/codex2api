-- codex2api v2.6.0 schema update for MySQL 5.6+.
-- Run this against the codex2api database before deploying v2.6.0.

ALTER TABLE system_settings
    ADD COLUMN grok_config TEXT NULL,
    ADD COLUMN codex_preflight_sse_passthrough_enabled TINYINT(1) DEFAULT 0;

ALTER TABLE prompt_filter_logs
    MODIFY COLUMN endpoint VARCHAR(256) DEFAULT '',
    ADD COLUMN request_protocol VARCHAR(64) DEFAULT '',
    ADD COLUMN request_provider VARCHAR(64) DEFAULT '',
    ADD COLUMN audit_score INT DEFAULT 0,
    ADD COLUMN policy_profile VARCHAR(32) DEFAULT '',
    ADD COLUMN reason_code VARCHAR(100) DEFAULT '',
    ADD COLUMN primary_origin VARCHAR(50) DEFAULT '',
    ADD COLUMN strike_eligible TINYINT(1) DEFAULT 0,
    ADD COLUMN match_context TEXT NULL;
