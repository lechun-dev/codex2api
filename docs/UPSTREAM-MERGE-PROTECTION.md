# 上游合并保护：会话存档排除功能（禁止被合并覆盖）

> **这是本地自定义功能，上游没有这段代码。**
> 从上游同步分支时，冲突解决或「checkout 上游版本」都会把这一块删掉。
> 合并完成后**必须**运行：
>
> ```bash
> bash .github/scripts/check-conversation-archive.sh
> ```

## 功能背景

`CODEX_CONVERSATION_EXCLUDE`：命中的请求**完全不采集**会话正文——不解析文本、
不旁路复制响应、不写 `conversation_records`。用量日志（`usage_logs`）照常记录。

四种排除维度（可混用）：

| 前缀 | 含义 |
|---|---|
| `machine:<installation_id>` | Codex 客户端机器码（turn metadata / 请求头 / client_metadata） |
| `client:<client_id>` | 显式 `X-Client-Id` 或鉴权派生的 client_id |
| `ip:<ip 或 cidr>` | 来源 IP / 网段 |
| `key:<api_key_id>` | API Key ID |

典型用途：把自己工作站的 Codex 请求从会话存档里排除。
本仓库引入提交：`f95d7aee`（tag `v3.1.8`）。
生产实例（codexapi.lechun.cc）已配置 `machine:92217c0d-827d-4af6-845e-dbe2d49b693e`。

## 合并上游时不能丢的文件与符号

| 文件 | 必须保留的内容 |
|---|---|
| `config/conversation_archive_exclude.go` | 整个文件：`ConversationArchiveExclude`、`ParseConversationArchiveExclude` |
| `proxy/conversation_archive_exclude.go` | 整个文件：`archiveExclusion`、`newArchiveExclusion`、`archiveRequestMachineID`、`logHit` |
| `config/config.go` | 字段 `ConversationArchiveExclude ConversationArchiveExclude`；`ParseConversationArchiveExclude(os.Getenv("CODEX_CONVERSATION_EXCLUDE"))` 的加载逻辑（解析失败要 fail startup） |
| `proxy/conversation_archive.go` | 结构体字段 `exclude *archiveExclusion`；初始化 `exclude: newArchiveExclusion(exclude)`；两处 `archiver.exclude.match(archiveIdentity{...})` 命中即跳过采集 |
| `.env.example` | `CODEX_CONVERSATION_EXCLUDE=` 条目 |
| `docs/conversation-archive.md` | 「排除项（CODEX_CONVERSATION_EXCLUDE）」一节 |
| `README.md` | 环境变量表中的 `CODEX_CONVERSATION_EXCLUDE` 行 |
| `proxy/conversation_archive_e2e_test.go` | 排除相关 e2e 测试 |
| `config/config_test.go` | 排除配置解析测试 |

## 冲突处理原则

1. `conversation_archive.go` 中凡是包含 `exclude` / `match` / `archiveIdentity`
   字样的冲突块，**以本地版本为准**，再把上游对该文件其他部分的改动并入。
2. 如果上游重构了 `ConversationArchiver` 的结构或中间件链路，需要把
   `exclude` 字段和两处 `match` 调用**移植**到新结构里，而不是删除。
3. 不确定时先看 `git log --follow proxy/conversation_archive_exclude.go`，
   确认文件历史还在，再看本文档核对符号。

## 合并后验证

```bash
bash .github/scripts/check-conversation-archive.sh  # 符号完整性（CI 也会跑）
go test -count=1 ./config/ ./proxy/ -run 'Archive|Exclude'   # 相关单测
# 冒烟：启动后日志应出现
#   [conversation-archive] 排除项已启用：机器码 N 个 / 客户端 N 个 / IP 网段 N 个 / API Key N 个
#   命中请求时：[conversation-archive] 命中排除项（machine），本次请求不归档
```
