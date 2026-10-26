# Jenkins 部署

本文档说明如何用仓库根目录的 `Jenkinsfile` 构建并部署 Codex2API。

## 前提

Jenkins Agent 需要安装：

- Go 1.26.3 或可自动下载 Go toolchain 的 Go 版本
- Node.js 20+ 和 npm
- `ssh`, `scp`, `curl`, `file`

服务器需要：

- Linux + systemd
- 一个可通过 SSH 登录的部署用户
- 已安装 `deploy/systemd/codex2api.service`
- 服务器本地存在部署目录下的 `shared/.env`

数据库密码只放在服务器 `.env` 或 Jenkins Credentials 中，不要提交到仓库。

## 服务器初始化

以下命令以 root 或具备 sudo 权限的用户执行。

```bash
APP_USER=appuser
DEPLOY_DIR=/opt/codex2api

sudo useradd --system --home "$DEPLOY_DIR" --shell /usr/sbin/nologin "$APP_USER" || true
sudo mkdir -p "$DEPLOY_DIR/releases" "$DEPLOY_DIR/shared"
sudo chown -R "$APP_USER:$APP_USER" "$DEPLOY_DIR"
sudo chmod -R 2775 "$DEPLOY_DIR"
```

如果 Jenkins 使用同一个用户 SSH 登录，确认它拥有部署目录权限：

```bash
sudo chown -R "$APP_USER:$APP_USER" "$DEPLOY_DIR"
```

创建 `$DEPLOY_DIR/shared/.env`：

```env
PORT=8080
BIND_HOST=127.0.0.1

DATABASE_DRIVER=mysql
DATABASE_HOST=your-mysql-host
DATABASE_PORT=3306
DATABASE_NAME=your-database
DATABASE_USER=your-user
DATABASE_PASSWORD=your-password
DATABASE_CHARSET=utf8

CACHE_DRIVER=memory
CODEX_USAGE_LOG_CAPTURE_REQUEST_CONTENT=false
# CODEX_USAGE_LOG_MASTER_KEY=
CODEX_CONVERSATION_RECORDING_ENABLED=true

CODEX_DOWNLOADS_DIR=/deploy/codex2api/downloads
```

安装 systemd 服务：

```bash
sudo cp deploy/systemd/codex2api.service /etc/systemd/system/codex2api.service
sudo systemctl daemon-reload
sudo systemctl enable codex2api
```

如果 Jenkins 的 SSH 用户不是 root，需要允许它重启服务。示例：

```bash
sudo visudo -f /etc/sudoers.d/codex2api-jenkins
```

写入：

```text
appuser ALL=(root) NOPASSWD: /bin/systemctl restart codex2api, /bin/systemctl status codex2api, /bin/systemctl --no-pager --full status codex2api
```

如果实际 SSH 用户不是 `appuser`，把这一行里的 `appuser` 替换成实际 SSH 用户。

## Jenkins 配置

创建一个 Pipeline Job，使用仓库内 `Jenkinsfile`。

需要配置一个 SSH Credentials：

- 类型：SSH Username with private key
- ID：`codex2api-prod-ssh`
- Username：服务器 SSH 用户

构建参数：

| 参数 | 说明 |
| --- | --- |
| `GOARCH` | 服务器架构，x86_64 选 `amd64`，ARM 选 `arm64` |
| `SSH_CREDENTIALS_ID` | Jenkins SSH 凭据 ID |
| `DEPLOY_HOST` | 服务器地址 |
| `DEPLOY_USER` | SSH 用户，默认 `appuser`；生产可覆盖为实际用户 |
| `DEPLOY_DIR` | 部署目录，默认 `/opt/codex2api`；生产可覆盖为实际目录 |
| `SERVICE_NAME` | systemd 服务名，默认 `codex2api` |
| `HEALTHCHECK_URL` | 可选，重启后的健康检查 URL |
| `RUN_MYSQL_COMPAT` | 是否在构建前跑 MySQL 5.6 兼容门禁，默认 `true`。**不要在生产分支关掉** |
| `MYSQL_COMPAT_IMAGE` | 门禁用的镜像，默认 `mysql:5.6` |
| `RELEASES_TO_KEEP` | 服务器上保留的历史 release 目录数，默认 `5` |

## 流水线行为

Jenkins 会执行：

1. 拉取代码
2. `frontend/npm ci`
3. `frontend/npm run build`
4. `frontend/npm run typecheck`
5. `go test ./...`（不含真 MySQL：没传 `CODEX2API_MYSQL_TEST_DSN` 时集成测试会 Skip）
6. **MySQL 5.6 兼容门禁**：起 `mysql:5.6` 容器，跑
   `go test ./database -run 'TestMySQL.*Integration|TestAPIKeyAuthRevisionMySQL'`
7. `GOOS=linux GOARCH=<GOARCH> go build`
8. 上传二进制到 `$DEPLOY_DIR/releases/<BUILD_NUMBER>/codex2api`
9. 切换 `$DEPLOY_DIR/current`
10. 重启 `codex2api` systemd 服务
11. 清理旧 release，只保留最近 `RELEASES_TO_KEEP` 个
12. 可选执行健康检查

### MySQL 5.6 门禁

上游只保证 PostgreSQL / SQLite，MySQL 5.6 完全靠本仓库的
`database/mysql_driver.go`（驱动层 SQL 重写）+ 各处的 `db.isMySQL()` 分支维持。
任何只跑 `go test ./...` 的链路都无法证明 5.6 可用，所以门禁是**发布前置**：

- Agent 需要 `docker`；容器端口固定映射到 `127.0.0.1:13306`
- 没有 docker 的 agent：把 `CODEX2API_MYSQL_TEST_DSN` / `CODEX2API_TEST_MYSQL_DSN`
  指向专用 5.6 实例（独立库，测试会建表），并在 job 里注入环境变量
- MySQL 官方 `mysql:5.6` 镜像是 amd64；agent 是 arm 时改用自建镜像或上面的专用实例方案

### GitHub Actions 部署门禁

生产发布实际走 GitHub Actions，而且是从 tag 上手动 dispatch 的：

```
Release / Build Docker Image  <- push tag v*
Deploy SSH / Deploy SSH (codexProxyNew)  <- workflow_dispatch（选 tag + action=deploy）
```

因为发布发生在 tag 上，靠"查某个 commit 的 CI check"并不可靠（tag 可能指向没跑过 CI 的
commit）。所以两个部署 workflow 的 `deploy` job 自己起了 `mysql:5.6` service 容器，
在部署前跑一遍真集成测试，失败就不发版：

```yaml
services:
  mysql:
    image: mysql:5.6
steps:
  - name: Test MySQL 5.6 compatibility   # go test ./database -run 'TestMySQL.*Integration|...'
```

`rollback` job 不跑这个门禁。

### 审计日志与磁盘

审计日志默认目录是相对工作目录的 `logs/security`，而 `WorkingDirectory` 指向
`$DEPLOY_DIR/current`（每次发版都换），不固定的话日志会随 release 目录散落、旧目录各占满
100MB×10 的轮转配额。`deploy/systemd/codex2api.service` 已经把
`SECURITY_LOG_DIR` 固定到 `$DEPLOY_DIR/shared/logs/security`；如果 `.env` 里另设了值，
以 `.env` 为准（`EnvironmentFile` 在 `Environment` 之后加载）。

## 回滚

查看历史版本：

```bash
ls -la /opt/codex2api/releases
```

回滚到指定构建：

```bash
sudo ln -sfn /opt/codex2api/releases/<BUILD_NUMBER> /opt/codex2api/current
sudo systemctl restart codex2api
```

GitHub Actions 部署同样支持 `action=rollback` + `rollback_release` 参数走这条路径。
部署步骤会自动清理 `RELEASES_TO_KEEP` 之外的历史 release（`current` 指向的永远最新，不会被删）。
