# Fork 维护手册（lechun-dev/codex2api）

本仓库是 `james-6-23/codex2api` 的 fork，长期承载两件事：

1. **MySQL 5.6 方言兼容**（上游只支持 PostgreSQL / SQLite）
2. **自有部署链路**（Jenkins + systemd + GitHub Actions，不走上游 Docker 主路径）

上游迭代很快（周均 50+ 提交），所以每次同步都要按本文档走一遍。改动本文档时请同步更新
`docs/JENKINS.md` 里对应的参数说明。

---

## 1. 分叉面

用下面命令随时重算：

```bash
git fetch upstream
git diff --stat upstream/main main | tail -1
git diff --name-only upstream/main main
```

当前规模约 **196 个文件、+19594/-1431 行**。真正由 fork 自己维护的文件（冲突高发区）：

| 范围 | 文件 | 说明 |
| --- | --- | --- |
| 方言驱动 | `database/mysql_driver.go`、`database/mysql*.go` | 注册 `mysql56` 驱动，在 `database/sql` 层把 PG 方言重写成 5.6 能跑的形式（`$n`→`?` 并重排参数、去 `::type`、`CAST(x AS TEXT)`→`CHAR`、`ON CONFLICT`→`ON DUPLICATE KEY UPDATE`、`api_keys` 标识符加反引号、DSN 补 `clientFoundRows`） |
| 方言分支 | 30+ 文件、169 处 `db.isMySQL()` | 驱动重写覆盖不到的整段 SQL（JSON 函数、窗口函数、DDL、索引幂等）在这里分叉 |
| 手工 DDL | `docs/sql/mysql56_*.sql` | 上游只维护 PG/SQLite 的表结构变更 |
| 部署 | `Jenkinsfile`、`docs/JENKINS.md`、`deploy/systemd/*`、`deploy/nginx/*`、`.github/workflows/deploy-ssh*.yml` | 自有发布链路与门禁 |
| 兼容测试 | `database/mysql_driver_test.go`、`database/mysql_integration_test.go`、`database/*mysql*_test.go` | 前者是静态断言（禁止 CTE / `->>` / `::` / `RETURNING` 等泄漏），后者需要真 5.6 |
| 其它本地能力 | `auth/continuation_capacity.go`（可回放续链借用容量）、`proxy/model_capabilities.go` 的 DeepSeek Flash 覆盖 | 与上游逻辑交织，合并时按语义保留 |

**判断准则**：合并冲突里，如果是「上游重构 + 本地能力」，保留上游结构、把本地能力按新结构重写（不要整段取 ours）；
如果是「上游新增字段 / 新索引」，两侧都要（不要整段取 theirs）。

---

## 2. 同步上游

```bash
git fetch upstream
git switch main
git merge upstream/main --no-ff -m "Merge upstream through $(git rev-parse --short upstream/main) with MySQL 5.6 compatibility"
# 解冲突（见第 1 节准则）
go build ./...
go vet ./...
go test -count=1 ./...
git push origin main
```

冲突高发文件（历史统计）：`proxy/handler.go`、`proxy/model_capabilities.go`、`database/postgres.go`、
`database/grok_state.go`、`database/image_job_queue.go`、`frontend/src/pages/*.tsx`、`frontend/src/locales/*.json`。

前端 locale 冲突一律「两边的 key 都保留」，不要整文件取一边。

---

## 3. 必过的门禁

| 门禁 | 位置 | 覆盖 |
| --- | --- | --- |
| `go test ./...` | 本地 / Jenkins `Test` / GH `test-race` | 通用回归，**不含真 MySQL** |
| MySQL 5.6 兼容 | Jenkins `MySQL 5.6 兼容` / GH `mysql56-compatibility` | 起真 `mysql:5.6` 跑 `TestMySQL.*Integration` |
| 部署门禁 | `deploy-ssh*.yml` 的 `Test MySQL 5.6 compatibility` step | 部署 job 自带 `mysql:5.6` service，发版前跑真集成测试，失败即中止 |

注意：`database/mysql_integration_test.go` 在没有 `CODEX2API_MYSQL_TEST_DSN` 时直接 `t.Skip`，
所以任何只跑 `go test ./...` 的链路（包括所有旧的发布脚本）都不能证明 5.6 可用。

本地跑一次真 5.6：

```bash
docker run -d --name c2a-mysql56 -e MYSQL_ROOT_PASSWORD=test-only \
  -e MYSQL_DATABASE=codex2api_test -p 13306:3306 mysql:5.6
# 等 mysqladmin ping 通过后
export CODEX2API_MYSQL_TEST_DSN='root:test-only@tcp(127.0.0.1:13306)/codex2api_test?parseTime=true&charset=utf8&loc=UTC'
export CODEX2API_TEST_MYSQL_DSN="$CODEX2API_MYSQL_TEST_DSN"
go test -count=1 ./database -run 'TestMySQL.*Integration|TestAPIKeyAuthRevisionMySQL'
```

---

## 4. 上游功能在 MySQL 上的移植：账号智能检查

上游 v3.1.1 新增的「账号智能检查」（`database/intelligence_check*.go`）最初只有
PostgreSQL / SQLite 实现，用到了 MySQL 5.6 完全没有的能力。fork 的做法是把
「SQL 里读 JSON」换成「写入时落成真实列」，实现集中在
`database/intelligence_check_mysql.go`：

| 上游写法 | MySQL 5.6 替代 |
| --- | --- |
| `state_json::jsonb->>'action'` / `json_extract(state_json,'$.action')` | 写 state 时同步 `state_action VARCHAR(32)` 列 |
| `run_json::jsonb->>'source' / account_name / channel / duration_ms` | 写入时同步同名真实列（`source`、`account_name`、`channel`、`duration_ms`、`question_name`） |
| `COUNT(*) OVER ()` | 冲突总数单独 `COUNT(*)`，名单再取前 200 条 |
| `credentials->>'upstream_type'` | `CAST(credentials AS CHAR) REGEXP '"upstream_type"\s*:\s*"claude"'` |
| `CREATE UNIQUE INDEX ... WHERE status IN (...)` | 普通索引 + 事务内查重（`intelligenceEnqueueTx` 先查活跃 run） |
| `CREATE INDEX IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` | `ensureMySQLIndex` / `ensureMySQLColumn` |
| `INSERT ... RETURNING id` | `Exec` + `LastInsertId()` |
| `AFTER INSERT` 触发器（含 JSON 解析） | 调度器每次 tick 前 `enrollIntelligenceCheckAccountsMySQLTx` 补插缺失账号 |
| `DELETE ... WHERE id NOT IN (SELECT ... LIMIT n)` | 先查出要保留的 id，再按显式占位符删除 |
| `TEXT NOT NULL DEFAULT '{}'` | 5.6 的 TEXT 不能带默认值：列保持 `NOT NULL`，所有插入显式写入 `'{}'` |
| 外键 `REFERENCES accounts(id) ON DELETE CASCADE` | 不建外键；账号彻底删除时在 `PurgeAccount` / `PurgeDeletedAccounts` 显式清理两张表 |

覆盖：`database/mysql_intelligence_integration_test.go`（测试名匹配
`TestMySQL.*Integration`，由 CI 的 `mysql56-compatibility` 与部署门禁执行），
`database/intelligence_check_mysql_test.go` 负责静态断言（DDL/查询里不允许出现
`BIGSERIAL`、`IF NOT EXISTS`、部分索引、`->>`、`json_extract`、`RETURNING`）。

移植时踩过的语义差异，改代码时别再踩：

- SQLite 默认不启用外键，MySQL 侧没建外键，**都要显式清理**子表，否则回收站清空后留孤儿行。
- `state_json` 在 SQLite/PG 上带 `DEFAULT '{}'`，MySQL 没有默认值，任何新增插入路径都必须写值。
- MySQL 的 `utf8` 列按字符数计长度，截断账号名要用 rune 而不是 byte。

## 5. 新增 SQL 的自检

改任何 DB 代码后：

1. `grep` 一下新写的 SQL 有没有 PG 专有语法：`::`、`->>`、`jsonb`、`LATERAL`、`DISTINCT ON`、
   `RETURNING`、`ILIKE`、`WITH RECURSIVE`、`OVER (`、`CREATE INDEX IF NOT EXISTS`。
2. 有的话，要么走驱动重写能覆盖的写法，要么按现有惯例加 `db.isMySQL()` 分支。
3. 加一条测试：静态断言用 `assertNoMySQL56IncompatibleSQL`，行为断言进 `mysql_*_test.go`。
4. 表结构变更额外补一份 `docs/sql/mysql56_*.sql`。

可以跑 `.github/scripts/sql-dialect-audit.sh` 扫出全仓候选点（只做提示，不做门禁）。

---

## 6. 部署布局

```
/deploy/codex2api/
├── releases/<build>/        # 每次发版一个目录（二进制 + 本次运行日志）
├── current -> releases/...  # 当前版本
└── shared/
    ├── .env                 # 唯一配置源，不进仓库
    └── logs/security/       # 审计日志（systemd 已固定到这里）
```

- 保留最近 5 个 release（Jenkins / GitHub Actions 部署步骤里自动清理）
- 审计日志跟随 `shared/`，不随版本目录漂移；单文件 100MB、保留 10 个
- 回滚：`ln -sfn <旧 release> /deploy/codex2api/current && systemctl restart codex2api`

---

## 7. 分支与评审

- `main` 受保护：只接受 PR，必须 `mysql56-compatibility` + `test-race` 绿。
- 上游同步用 `codex/upstream-merge-<YYYYMMDD>` 分支开 PR，不直接推 `main`。
- 功能/加固用 `codex/<topic>` 分支。
- 一次性分支合并后删除；历史 `codex/upstream-merge-mysql56-*` 已全部合并，可清理。
