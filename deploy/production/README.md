# Codex2API 生产交付包

这是可直接交给客户的单机生产包：固定版本 Docker 镜像、PostgreSQL、Redis、持久化卷，以及安装、升级、回滚、备份和健康检查脚本。默认只绑定 `127.0.0.1`，可放在 Caddy/Nginx 后面；VPN/WireGuard 安装在客户宿主机时，容器会沿用宿主机出口。

## 首次安装

在已安装 Docker Engine + Compose v2 的 Linux 服务器上执行：

```bash
sudo ./install.sh --version v2.8.2
sudo ./install.sh --bind 10.0.0.10 --port 8080
```

脚本创建 `/opt/codex2api`，随机生成数据库密码和 `ADMIN_SECRET`（只写入权限为 600 的 `shared/.env`），拉取固定镜像并运行健康检查。若使用宿主机代理，增加 `--proxy-url socks5://user:pass@host.docker.internal:1080`；WireGuard/OpenVPN 则在宿主机配置默认路由/策略路由，并放行 Docker 网段。

## 升级与回滚

拿到下一版交付包后，在新包目录执行：

```bash
sudo ./backup.sh
sudo ./upgrade.sh --version v2.8.3
sudo ./rollback.sh
```

程序版本位于 `releases/<version>`，客户配置和数据卷独立保存。升级脚本不会覆盖 `.env`；生产环境禁止把代理凭据、Token 或 API Key 提交到 Git。数据库迁移若不可逆，必须先验证备份并按发布说明执行恢复/向前修复。

## 备份与验收

`backup.sh` 导出 PostgreSQL、图库卷和配置快照到 `backups/<timestamp>`。每次交付至少验收 `/health`、服务器重启自动恢复，以及经 VPN/代理完成一次真实账号刷新和模型请求（后者需由客户提供可用账号，不由脚本自动发送）。

## 批量部署

将本目录作为 Ansible role 的 `files/` 内容，按客户 inventory 设置 `CODEX2API_INSTALL_DIR`、版本和各自的 `.env`/Vault；同一版本先 canary 一台，通过后再并行发布其余主机。镜像仓库受限时把 `CODEX2API_IMAGE` 指向客户私有 registry，或预加载同一 digest 的离线镜像。
