#!/usr/bin/env bash
set -Eeuo pipefail

# Runs on the operator workstation. Installation and service changes happen on
# the customer host over SSH.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
HOST=""; USER="root"; SSH_PORT="22"; IDENTITY=""; PACKAGE=""
INSTALL_DIR="/opt/codex2api"; VERSION="v2.8.2"; IMAGE=""
APP_PORT="8080"; BIND_HOST="127.0.0.1"; PROXY_URL=""
BOOTSTRAP_DOCKER=0; KEEP_UPLOAD=0

usage() {
  cat <<'EOF'
用法: sudo ./deploy-remote.sh --host HOST [选项]

在客户服务器完成 SSH 预检、生产包上传、远程安装和 /health 验收。
客户机需要 Docker Engine + Compose v2；VPN/WireGuard 请先在客户机配置。

  --host HOST             客户服务器主机名或 IP（必填）
  --user USER             SSH 用户（默认 root；需能无交互 sudo）
  --ssh-port PORT         SSH 端口（默认 22）
  --identity FILE         SSH 私钥文件
  --package FILE          生产包 tar.gz（默认自动构建当前版本）
  --version VERSION       版本（默认 v2.8.2）
  --image IMAGE           覆盖镜像引用（私有 registry/digest）
  --dir PATH              客户机安装目录（默认 /opt/codex2api）
  --port PORT             应用端口（默认 8080）
  --bind ADDRESS          绑定地址（默认 127.0.0.1）
  --proxy-url URL         客户机代理地址（不写入本地日志）
  --bootstrap-docker      尝试用 apt/dnf/yum 安装 Docker（需客户确认）
  --keep-upload           保留客户机 /tmp 中的上传包用于排障
EOF
}
die() { echo "错误: $*" >&2; exit 1; }
quote() { printf '%q' "$1"; }

while (($#)); do
  case "$1" in
    --host) HOST="${2:?--host 需要参数}"; shift 2 ;;
    --user) USER="${2:?--user 需要参数}"; shift 2 ;;
    --ssh-port) SSH_PORT="${2:?--ssh-port 需要参数}"; shift 2 ;;
    --identity) IDENTITY="${2:?--identity 需要参数}"; shift 2 ;;
    --package) PACKAGE="${2:?--package 需要参数}"; shift 2 ;;
    --version) VERSION="${2:?--version 需要参数}"; shift 2 ;;
    --image) IMAGE="${2:?--image 需要参数}"; shift 2 ;;
    --dir) INSTALL_DIR="${2:?--dir 需要参数}"; shift 2 ;;
    --port) APP_PORT="${2:?--port 需要参数}"; shift 2 ;;
    --bind) BIND_HOST="${2:?--bind 需要参数}"; shift 2 ;;
    --proxy-url) PROXY_URL="${2:?--proxy-url 需要参数}"; shift 2 ;;
    --bootstrap-docker) BOOTSTRAP_DOCKER=1; shift ;;
    --keep-upload) KEEP_UPLOAD=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数: $1" ;;
  esac
done

[[ -n "$HOST" ]] || die "必须指定 --host"
[[ "$SSH_PORT" =~ ^[0-9]+$ ]] && ((SSH_PORT >= 1 && SSH_PORT <= 65535)) || die "SSH 端口无效"
[[ "$APP_PORT" =~ ^[0-9]+$ ]] && ((APP_PORT >= 1 && APP_PORT <= 65535)) || die "应用端口无效"
[[ "$INSTALL_DIR" = /* && "$INSTALL_DIR" != *..* ]] || die "安装目录必须是绝对路径且不能包含 .."
[[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || die "版本格式无效"
command -v ssh >/dev/null 2>&1 || die "未找到 ssh"
command -v scp >/dev/null 2>&1 || die "未找到 scp"

if [[ -z "$PACKAGE" ]]; then
  PACKAGE="$SCRIPT_DIR/../artifacts/codex2api-production-$VERSION.tar.gz"
  [[ -f "$PACKAGE" ]] || "$SCRIPT_DIR/build-package.sh" "$VERSION" >/dev/null
fi
[[ -f "$PACKAGE" ]] || die "找不到生产包: $PACKAGE"
PACKAGE="$(cd "$(dirname "$PACKAGE")" && pwd)/$(basename "$PACKAGE")"
if [[ -f "$PACKAGE.sha256" ]]; then
  (cd "$(dirname "$PACKAGE")" && shasum -a 256 -c "$(basename "$PACKAGE").sha256" >/dev/null 2>&1 || sha256sum -c "$(basename "$PACKAGE").sha256" >/dev/null) \
    || die "生产包 SHA256 校验失败"
fi

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=3)
[[ -n "$IDENTITY" ]] && SSH_OPTS+=(-i "$IDENTITY")
REMOTE="$USER@$HOST"
UPLOAD="/tmp/codex2api-deploy-$$.tar.gz"

echo "连接客户服务器 $REMOTE ..."
ssh "${SSH_OPTS[@]}" -p "$SSH_PORT" "$REMOTE" 'set -eu; command -v tar >/dev/null; if [ "$(id -u)" -ne 0 ]; then command -v sudo >/dev/null; sudo -n true; fi; if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then echo docker-compose-ok; else echo docker-compose-missing; fi'

if ((BOOTSTRAP_DOCKER)); then
  ssh "${SSH_OPTS[@]}" -p "$SSH_PORT" "$REMOTE" 'set -eu
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then exit 0; fi
    SUDO=""; [ "$(id -u)" -eq 0 ] || SUDO="sudo -n"
    if command -v apt-get >/dev/null 2>&1; then $SUDO apt-get update && $SUDO apt-get install -y docker.io docker-compose-plugin
    elif command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y docker docker-compose-plugin && $SUDO systemctl enable --now docker
    elif command -v yum >/dev/null 2>&1; then $SUDO yum install -y docker docker-compose-plugin && $SUDO systemctl enable --now docker
    else echo "不支持自动安装 Docker，请人工安装 Docker Engine + Compose v2" >&2; exit 1; fi'
fi

echo "上传并执行远程安装 ..."
scp "${SSH_OPTS[@]}" -P "$SSH_PORT" "$PACKAGE" "$REMOTE:$UPLOAD"
REMOTE_ARGS=("--dir" "$INSTALL_DIR" "--version" "$VERSION" "--port" "$APP_PORT" "--bind" "$BIND_HOST")
[[ -n "$IMAGE" ]] && REMOTE_ARGS+=("--image" "$IMAGE")
[[ -n "$PROXY_URL" ]] && REMOTE_ARGS+=("--proxy-url" "$PROXY_URL")
REMOTE_CMD="set -eu; tmp=\$(mktemp -d /tmp/codex2api-package.XXXXXX); trap 'rm -rf \"\$tmp\"' EXIT; tar -xzf $(quote "$UPLOAD") -C \"\$tmp\" --strip-components=1; cd \"\$tmp\"; if [ \"\$(id -u)\" -eq 0 ]; then runner=; else runner='sudo -n'; fi; \$runner bash ./install.sh"
for arg in "${REMOTE_ARGS[@]}"; do REMOTE_CMD+=" $(quote "$arg")"; done
((KEEP_UPLOAD)) || REMOTE_CMD+="; rm -f $(quote "$UPLOAD")"
ssh "${SSH_OPTS[@]}" -p "$SSH_PORT" "$REMOTE" "$REMOTE_CMD"
echo "客户服务器部署完成：$REMOTE:$INSTALL_DIR（已执行 /health 验收）"
