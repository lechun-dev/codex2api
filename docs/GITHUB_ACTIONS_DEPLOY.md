# GitHub Actions 部署

仓库包含两套互相独立的 SSH 部署工作流，不要把两台服务器写进同一个 Action：

| 工作流 | 文件 | 目标服务器 |
| --- | --- | --- |
| `Deploy SSH` | `.github/workflows/deploy-ssh.yml` | 现有国内服务器 |
| `Deploy SSH (codexProxyNew)` | `.github/workflows/deploy-ssh-codexproxynew.yml` | `codexProxyNew`（`47.245.107.32`） |

两套工作流都只支持在 GitHub Actions 里手动运行，不会因为推送到 `main` 自动部署。

## 回滚

在 Actions 中运行对应服务器的工作流（`Deploy SSH` 或 `Deploy SSH (codexProxyNew)`），将 `action` 选择为 `rollback`，并在 `rollback_release` 中填写该服务器上已有的 release 目录名，例如 `42-abc1234`。可在目标服务器执行以下命令查看可回滚版本：

```bash
find /deploy/codex2api/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -V
```

工作流会先确认目标版本包含 `codex2api` 二进制和共享 `.env`，然后切换 `current`、重启 systemd 服务并执行可选健康检查。回滚不重新编译代码，也不会删除当前或历史 release。

## GitHub Secrets

在 GitHub 仓库设置中进入 `Settings -> Secrets and variables -> Actions`，按工作流分别新增。两套 secrets 不要混用。

### Deploy SSH（现有服务器）

| Secret | 说明 |
| --- | --- |
| `DEPLOY_HOST` | 现有部署服务器 IP 或域名 |
| `DEPLOY_USER` | SSH 用户，例如 `deployuser` |
| `DEPLOY_SSH_KEY` | 可登录现有服务器的 SSH 私钥内容 |

### Deploy SSH (codexProxyNew)

| Secret | 说明 |
| --- | --- |
| `CODEXPROXYNEW_DEPLOY_HOST` | `47.245.107.32` |
| `CODEXPROXYNEW_DEPLOY_USER` | `deployuser` |
| `CODEXPROXYNEW_DEPLOY_SSH_KEY` | 可登录 `codexProxyNew` 的 SSH 私钥内容 |

数据库密码、Redis 密码仍放在服务器本地 `.env`，不要放进仓库。

## 服务器目录

示例使用 `/deploy/codex2api`：

```bash
sudo mkdir -p /deploy/codex2api/releases /deploy/codex2api/shared
sudo chown -R deployuser:deployuser /deploy/codex2api
sudo chmod -R 2775 /deploy/codex2api
```

创建 `/deploy/codex2api/shared/.env`：

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

CACHE_DRIVER=redis
REDIS_ADDR=your-redis-host:6379
REDIS_USERNAME=
REDIS_PASSWORD=your-redis-password
REDIS_DB=0
REDIS_TLS=false
REDIS_INSECURE_SKIP_VERIFY=false
CODEX_USAGE_LOG_CAPTURE_REQUEST_CONTENT=false
# CODEX_USAGE_LOG_MASTER_KEY=
CODEX_CONVERSATION_RECORDING_ENABLED=true

CODEX_DOWNLOADS_DIR=/deploy/codex2api/downloads
```

## systemd

`/etc/systemd/system/codex2api.service` 示例：

```ini
[Unit]
Description=Codex2API
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=deployuser
Group=deployuser
WorkingDirectory=/deploy/codex2api/current
EnvironmentFile=/deploy/codex2api/shared/.env
ExecStart=/deploy/codex2api/current/codex2api
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/deploy/codex2api

[Install]
WantedBy=multi-user.target
```

启用服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable codex2api
```

允许部署用户重启服务：

```text
deployuser ALL=(root) NOPASSWD: /bin/systemctl restart codex2api, /bin/systemctl status codex2api, /bin/systemctl --no-pager --full status codex2api
```

用 `sudo visudo -f /etc/sudoers.d/codex2api-github-actions` 写入。

## 触发部署

进入 GitHub 仓库手动触发对应工作流：

```text
Actions -> Deploy SSH -> Run workflow
Actions -> Deploy SSH (codexProxyNew) -> Run workflow
```

`codexProxyNew` 这台机器在海外，启动时连国内数据库会比较慢，对应工作流的服务重启等待时间更长。

参数：

| 参数 | 建议值 |
| --- | --- |
| `goarch` | x86_64 服务器选 `amd64`，ARM 选 `arm64` |
| `deploy_dir` | `/deploy/codex2api` |
| `service_name` | `codex2api` |
| `healthcheck_url` | 可留空；配置反代后可填健康检查 URL |

工作流会执行：

1. 构建前端
2. 前端 typecheck
3. Go 测试
4. 编译 Linux 二进制
5. 上传到 `$deploy_dir/releases/<run-number>-<sha>/codex2api`
6. 切换 `$deploy_dir/current`
7. 重启 systemd 服务
