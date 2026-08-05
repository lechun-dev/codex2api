-- API Key 客户端数量统计（MySQL 5.6+）
-- 可在发布新版本前单独执行；应用启动时也会幂等创建此表。

CREATE TABLE IF NOT EXISTS api_key_clients (
    api_key_id BIGINT NOT NULL,
    client_id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    first_seen_at DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL,
    PRIMARY KEY (api_key_id, client_id_hash),
    KEY idx_api_key_clients_last_seen (api_key_id, last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;
