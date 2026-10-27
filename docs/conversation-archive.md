# 会话存档（conversation archive）

把用户与 AI 的**纯文本**对话归档到 `conversation_records`，并提供管理端与内部（AI / lechun-tools）
查询接口，用于后续分析。

## 1. 能力范围

| 项 | 说明 |
| --- | --- |
| 采集端点 | `POST /v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1beta/models/*`（Gemini 原生） |
| 不采集 | 图片/视频/实时通道、`/v1/models` 等非推理请求 |
| 协议 | OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Gemini generateContent，流式与非流式都支持（SSE 自动拼接） |
| 只存文本 | 图片、附件、`tool_use`/`function_call`、`thinking`/`reasoning` 内容全部丢弃 |
| 体积上限 | 请求体 1 MiB、响应体 4 MiB、单条文本 64K 字符；超限截断并置 `truncated=true` |
| 写入方式 | 异步单协程队列（容量 1024），队列满直接丢弃并计数；**绝不阻塞请求** |
| token 用量 | 从响应（含 SSE 末尾 usage 帧）解析 `input_tokens`/`output_tokens` |
| 保留策略 | 默认 90 天，每 24h 分批清理；`0` 表示永久保留 |

> 默认**关闭**。开启后落库的是用户原文，属于敏感数据，请同时确认保留天数与访问控制。

## 2. 启用

```dotenv
# 开启归档（默认 false）
CODEX_CONVERSATION_RECORDING_ENABLED=true
# 保留天数，0 = 永久
CODEX_CONVERSATION_RETENTION_DAYS=90
# 给 AI/lechun-tools 的内部接口：与 sub2api 共用同一套签名头与共享密钥
INTERNAL_IDENTITY_ENABLED=true
INTERNAL_IDENTITY_SECRET=请换成与 sub2api 相同的随机长密钥
INTERNAL_IDENTITY_MAX_SKEW_SECONDS=300
```

启动时会自动建表/补列（`conversation_records.stream/truncated`、`api_keys.owner_email`），可重复执行。

## 3. 表结构

复用 `conversation_records`（PostgreSQL / SQLite / MySQL 5.6 三方言）。关键列：

| 列 | 说明 |
| --- | --- |
| `request_id` | 请求唯一标识（同时作为幂等键，重试会更新同一行） |
| `session_id` | 会话标识：请求头 `X-Session-Id` → body `session_id` → 兜底 `X-Request-ID` |
| `api_key_id` / `api_key_name` | 调用方 API Key |
| `owner_email` | **不在存档里存**：查询时 `LEFT JOIN api_keys` 取 `api_keys.owner_email` |
| `client_id` / `client_ip` | 客户端标识与来源 IP |
| `endpoint` / `model` | 端点与模型 |
| `user_message` / `assistant_message` | 归档正文（列表查询默认不返回） |
| `input_tokens` / `output_tokens` | token 用量 |
| `status` / `status_code` / `duration_ms` | 结果与耗时 |
| `stream` / `truncated` | 是否流式、是否被截断 |
| `created_at` / `completed_at` | 时间 |

索引：`(owner_email, created_at)`、`(api_key_id, session_id, created_at)`、`(created_at)` 等。

## 4. 管理端接口（`X-Admin-Key`）

```
GET /api/admin/conversation-archives
GET /api/admin/conversation-archives/:id
PUT /api/admin/keys/:id/owner        # 维护归属人邮箱：{"owner_email":"a@b.c"}
```

列表过滤器（全部可选，AND 组合）：

| 参数 | 说明 |
| --- | --- |
| `api_key_id`、`owner_email` | 归属维度（`owner_email` 大小写不敏感） |
| `client_id`、`session_id`、`request_id` | 会话维度 |
| `model`、`endpoint`、`status`、`status_code` | 请求维度 |
| `stream`、`truncated`（`true`/`false`） | 布尔维度 |
| `keyword` | 同时匹配用户消息与助手回复（大小写不敏感） |
| `from`、`to` | 支持 RFC3339 / `YYYY-MM-DD` / `YYYY-MM-DD HH:MM:SS` / unix 秒 / 毫秒 |
| `order` | `asc` / `desc`（默认 `desc`） |
| `offset`、`limit` | 默认 0 / 50，上限 500 |
| `include_content` | 默认 `false`：列表只返回元数据与 `user_message_len`/`assistant_message_len` |

示例：

```bash
curl -s -H "X-Admin-Key: $ADMIN_SECRET" \
  'http://127.0.0.1:8080/api/admin/conversation-archives?owner_email=a@b.c&keyword=库存&limit=20'

curl -s -H "X-Admin-Key: $ADMIN_SECRET" \
  'http://127.0.0.1:8080/api/admin/conversation-archives/123'
```

读取正文（列表 `include_content=true`、详情、内部接口）都会写安全审计日志：
`ARCHIVE_READ` / `ARCHIVE_LIST` / `INTERNAL_ARCHIVE_ACCESS`。

## 5. 归属人怎么维护

存档**不复制**归属人，查询时 `LEFT JOIN api_keys` 取 `owner_email`，所以：

- 新增/调整某人归属，只需要维护 `api_keys.owner_email`
- **历史存档立刻按新邮箱可查**，不需要回填、重新采集或重建索引

三种维护方式：

1. **管理台**：密钥列表 → 新建/编辑弹窗里的「归属邮箱」字段（列表里也会显示）
2. 管理接口：`POST /api/admin/keys`（`owner_email` 字段）/ `PUT /api/admin/keys/:id`（`owner_email`）/ 专用 `PUT /api/admin/keys/:id/owner`
3. 直接改库：`UPDATE api_keys SET owner_email='alice@lechun.cc' WHERE id=12;`

```bash
# 管理接口
curl -s -X PUT -H "X-Admin-Key: $ADMIN_SECRET" -H 'Content-Type: application/json' \
  -d '{"owner_email":"alice@lechun.cc"}' \
  http://127.0.0.1:8080/api/admin/keys/12/owner

# 2) 直接改库（例如从人力/SSO 导入）
UPDATE api_keys SET owner_email='alice@lechun.cc' WHERE id=12;
```

想按「人名」查时，两种做法都行：把 `owner_email` 存成包含人名的形式（如 `alice@`），或者额外用 `api_key_name` 过滤。`api_keys(owner_email)` 上有索引。

## 6. 内部接口（给 AI / lechun-tools）

```
GET /api/v1/internal/conversation-archives            # 列表
GET /api/v1/internal/conversation-archives/:id        # 详情
GET /api/v1/internal/conversation-archives/stats      # 存档 token 用量聚合
GET /api/v1/internal/usage/stats                      # usage_logs 用量聚合
```

存档统计支持 `group_by=none|user|date|model|protocol|api_key`、`limit`、`order=asc|desc`；
`usage/stats` 的 `group_by` 兼容 tools 的取值（`user`/`api_key` 都映射到 API Key 维度，
`date` 暂不支持，返回空 items + 总计）。

鉴权**完全复用 sub2api 的内部身份断言**，两边配同一个共享密钥即可：

| 项 | 值 |
| --- | --- |
| 头 | `X-Lechun-Email`、`X-Lechun-Timestamp`（unix 秒）、`X-Lechun-Signature` |
| 签名串 | `email + "\n" + timestamp + "\n" + method + "\n" + path` |
| 签名 | `hex(HMAC-SHA256(secret, 签名串))` |
| 时钟窗口 | `INTERNAL_IDENTITY_MAX_SKEW_SECONDS`（默认 300s） |
| 开关 | `INTERNAL_IDENTITY_ENABLED=true` 且密钥非空，否则路由不存在（404） |

与 sub2api 的差异：codex2api 没有用户/角色体系，**这里只验证“调用方持有共享密钥”**，
`X-Lechun-Email` 仅作为调用方声明的身份写审计日志。谁能查全部存档由调用方
（tools）按自己的角色体系判断，codex2api 不重复实现权限模型。

> 建议 codex2api 与 sub2api 使用**两个不同的共享密钥**，任一侧泄露不会波及其它系统。

```bash
EMAIL=alice@lechun.cc
SECRET=...                       # 与 sub2api 相同的密钥
TS=$(date +%s)
PATH_ONLY=/api/v1/internal/conversation-archives
SIG=$(printf '%s\n%s\n%s\n%s' "$EMAIL" "$TS" "GET" "$PATH_ONLY" \
  | openssl dgst -sha256 -hmac "$SECRET" -r | cut -d' ' -f1)
curl -s "http://127.0.0.1:8080${PATH_ONLY}?owner_email=alice@lechun.cc" \
  -H "X-Lechun-Email: ${EMAIL}" \
  -H "X-Lechun-Timestamp: ${TS}" \
  -H "X-Lechun-Signature: ${SIG}"
```

过滤参数与管理端一致（`owner_email`/`api_key_id`/`session_id`/`keyword`/`from`/`to`/`limit` 等），
列表默认不返回正文。

同时兼容 tools 现有的 sub2api 参数契约（无需改 tools 的查询模型）：

| tools 参数 | codex2api 映射 |
| --- | --- |
| `page` / `page_size` | `offset` / `limit` |
| `user_id` | `api_key_id`（codex2api 没有用户体系） |
| `username` | `owner_email` 模糊匹配 |
| `session_key` | `session_id` |
| `protocol` | `openai_chat` → `/v1/chat/completions`、`openai_responses` → `/v1/responses`、`anthropic_messages` → `/v1/messages`、`gemini` → `/v1beta/models/…` |
| `start_time` / `end_time` | `from` / `to` |

## 7. MySQL 5.6 注意

- 补充列/索引走 `ensureMySQLColumn` / `ensureMySQLIndex`（5.6 没有 `ADD COLUMN IF NOT EXISTS`）；
`api_keys(owner_email)` 索引由 `EnsureAPIKeyOwnerEmail` 幂等创建
- 清理用 `DELETE ... ORDER BY id LIMIT n`（5.6 不支持带 `LIMIT` 的 `IN` 子查询）
- 布尔列是 `TINYINT(1)`；正文是 `LONGTEXT`（无默认值）
- 真 5.6 覆盖：`database/mysql_conversation_archive_integration_test.go`（匹配 CI 的 `TestMySQL.*Integration`）

## 8. 与 sub2api 的差异

sub2api 有用户/邮箱体系，接口按用户身份鉴权；codex2api 只有 API Key，因此：

- 归属人是 **API Key 上的邮箱**（`api_keys.owner_email`，查询时 join），不是登录用户
- 内部接口用共享密钥，不再做「邮箱 → 用户 → 是否管理员」这一跳
