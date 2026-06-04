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
- 服务器本地存在 `/deploy/shared/.env`

数据库密码只放在服务器 `.env` 或 Jenkins Credentials 中，不要提交到仓库。

## 服务器初始化

以下命令以 root 或具备 sudo 权限的用户执行。

```bash
sudo useradd --system --home /deploy --shell /usr/sbin/nologin deployuser || true
sudo mkdir -p /deploy/releases /deploy/shared
sudo chown -R deployuser:deployuser /deploy
sudo chmod -R 2775 /deploy
```

如果 `deployuser` 已经存在，并且 Jenkins 使用这个用户 SSH 登录，确认它拥有 `/deploy` 目录权限：

```bash
sudo chown -R deployuser:deployuser /deploy
```

创建 `/deploy/shared/.env`：

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
deployuser ALL=(root) NOPASSWD: /bin/systemctl restart codex2api, /bin/systemctl status codex2api, /bin/systemctl --no-pager --full status codex2api
```

如果实际 SSH 用户不是 `deployuser`，把这一行里的 `deployuser` 替换成实际 SSH 用户。

## Jenkins 配置

创建一个 Pipeline Job，使用仓库内 `Jenkinsfile`。

需要配置一个 SSH Credentials：

- 类型：SSH Username with private key
- ID：`codex2api-prod-ssh`
- Username：`deployuser`

构建参数：

| 参数 | 说明 |
| --- | --- |
| `GOARCH` | 服务器架构，x86_64 选 `amd64`，ARM 选 `arm64` |
| `SSH_CREDENTIALS_ID` | Jenkins SSH 凭据 ID |
| `DEPLOY_HOST` | 服务器地址 |
| `DEPLOY_USER` | SSH 用户，默认 `deployuser` |
| `DEPLOY_DIR` | 部署目录，默认 `/deploy` |
| `SERVICE_NAME` | systemd 服务名，默认 `codex2api` |
| `HEALTHCHECK_URL` | 可选，重启后的健康检查 URL |

## 流水线行为

Jenkins 会执行：

1. 拉取代码
2. `frontend/npm ci`
3. `frontend/npm run build`
4. `frontend/npm run typecheck`
5. `go test ./...`
6. `GOOS=linux GOARCH=<GOARCH> go build`
7. 上传二进制到 `/deploy/releases/<BUILD_NUMBER>/codex2api`
8. 切换 `/deploy/current`
9. 重启 `codex2api` systemd 服务
10. 可选执行健康检查

## 回滚

查看历史版本：

```bash
ls -la /deploy/releases
```

回滚到指定构建：

```bash
sudo ln -sfn /deploy/releases/<BUILD_NUMBER> /deploy/current
sudo systemctl restart codex2api
```
