-- codex2api v2.6.8 schema update for MySQL 5.6+.
-- Run this against the codex2api database before deploying v2.6.8.

ALTER TABLE system_settings
    ADD COLUMN response_cache_local_max_bytes BIGINT NOT NULL DEFAULT 67108864,
    ADD COLUMN response_cache_local_max_entry_bytes BIGINT NOT NULL DEFAULT 8388608,
    ADD COLUMN response_cache_reconstruct_max_bytes BIGINT NOT NULL DEFAULT 67108864,
    ADD COLUMN response_cache_config_generation BIGINT NOT NULL DEFAULT 1;
