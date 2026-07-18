-- codex2api conversation records
-- Compatible with MySQL 5.6 and later.
-- Safe to run repeatedly: the table is created only when it does not exist.

CREATE TABLE IF NOT EXISTS conversation_records (
    id                   BIGINT NOT NULL AUTO_INCREMENT,
    request_id           VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    session_id           VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    api_key_id           BIGINT NOT NULL DEFAULT 0,
    api_key_name         VARCHAR(255) NOT NULL DEFAULT '',
    client_id            VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    client_ip            VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    response_id          VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
    previous_response_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
    endpoint             VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    model                VARCHAR(100) NOT NULL DEFAULT '',
    user_message         LONGTEXT NULL,
    assistant_message    LONGTEXT NULL,
    status               VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'completed',
    status_code          INT NOT NULL DEFAULT 0,
    input_tokens         INT NOT NULL DEFAULT 0,
    output_tokens        INT NOT NULL DEFAULT 0,
    duration_ms          INT NOT NULL DEFAULT 0,
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL,
    completed_at         DATETIME NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_conversation_request (request_id),
    UNIQUE KEY uk_conversation_response (api_key_id, response_id),
    KEY idx_conversation_session_created (api_key_id, session_id, created_at),
    KEY idx_conversation_user_created (api_key_id, client_id, created_at),
    KEY idx_conversation_created_at (created_at),
    KEY idx_conversation_previous_response (api_key_id, previous_response_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;
