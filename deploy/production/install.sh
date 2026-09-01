#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${CODEX2API_INSTALL_DIR:-/opt/codex2api}"
VERSION="${CODEX2API_VERSION:-v2.8.2}"
IMAGE="${CODEX2API_IMAGE:-ghcr.io/james-6-23/codex2api:${VERSION}}"
PORT="${CODEX_PORT:-8080}"
BIND="${BIND_HOST:-127.0.0.1}"
ADMIN_SECRET="${ADMIN_SECRET:-}"
DATABASE_PASSWORD="${DATABASE_PASSWORD:-}"
PROXY_URL="${CODEX_PROXY_URL:-}"
DRY_RUN=0

usage() {
  cat <<'EOF'
用法: sudo ./install.sh [选项]
  --dir PATH             安装目录 (默认 /opt/codex2api)
  --version VERSION      发布版本，例如 v2.8.2
  --image IMAGE          完整镜像引用（优先于 --version）
  --port PORT            服务端口（默认 8080）
  --bind ADDRESS         监听地址（默认 127.0.0.1）
  --proxy-url URL        可选 HTTP(S)/SOCKS5 代理
  --admin-secret SECRET  管理后台密钥（省略则随机生成）
  --dry-run              只检查和打印，不写入或启动服务
EOF
}
die() { echo "错误: $*" >&2; exit 1; }
random_secret() { (command -v openssl >/dev/null && openssl rand -hex 24) || head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

while (($#)); do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    --version) VERSION="$2"; IMAGE="ghcr.io/james-6-23/codex2api:$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --bind) BIND="$2"; shift 2 ;;
    --proxy-url) PROXY_URL="$2"; shift 2 ;;
    --admin-secret) ADMIN_SECRET="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数: $1" ;;
  esac
done

[[ "$PORT" =~ ^[0-9]+$ ]] && ((PORT >= 1 && PORT <= 65535)) || die "端口无效: $PORT"
command -v docker >/dev/null 2>&1 || die "未找到 Docker，请先安装 Docker Engine"
docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2"
[[ -n "$ADMIN_SECRET" ]] || ADMIN_SECRET="$(random_secret)"
[[ -n "$DATABASE_PASSWORD" ]] || DATABASE_PASSWORD="$(random_secret)"
echo "Codex2API $VERSION → $INSTALL_DIR"
echo "镜像: $IMAGE | 绑定: $BIND:$PORT"
((DRY_RUN)) && exit 0

umask 077
mkdir -p "$INSTALL_DIR/shared" "$INSTALL_DIR/backups" "$INSTALL_DIR/releases/$VERSION/logs"
cp "$SCRIPT_DIR/docker-compose.yml" "$INSTALL_DIR/releases/$VERSION/docker-compose.yml"
ln -sfn "releases/$VERSION" "$INSTALL_DIR/current"
ln -sfn "$INSTALL_DIR/shared/.env" "$INSTALL_DIR/releases/$VERSION/.env"

if [[ ! -e "$INSTALL_DIR/shared/.env" ]]; then
  {
    printf 'CODEX2API_IMAGE=%s\nCODEX_PORT=%s\nBIND_HOST=%s\nTZ=Asia/Shanghai\n' "$IMAGE" "$PORT" "$BIND"
    printf 'DATABASE_DRIVER=postgres\nDATABASE_HOST=postgres\nDATABASE_PORT=5432\nDATABASE_USER=codex2api\nDATABASE_PASSWORD=%s\nDATABASE_NAME=codex2api\nDATABASE_SSLMODE=disable\n' "$DATABASE_PASSWORD"
    printf 'CACHE_DRIVER=redis\nREDIS_ADDR=redis:6379\nREDIS_DB=0\nREDIS_TLS=false\nREDIS_INSECURE_SKIP_VERIFY=false\nADMIN_SECRET=%s\n' "$ADMIN_SECRET"
    printf 'CODEX_USAGE_LOG_CAPTURE_REQUEST_CONTENT=false\nCODEX_CONVERSATION_RECORDING_ENABLED=true\nCODEX_DOWNLOADS_DIR=/data/downloads\n'
    [[ -n "$PROXY_URL" ]] && printf 'CODEX_PROXY_URL=%s\n' "$PROXY_URL"
  } > "$INSTALL_DIR/shared/.env"
else
  echo "保留已有配置: $INSTALL_DIR/shared/.env"
fi

COMPOSE=(docker compose --project-directory "$INSTALL_DIR/current" --env-file "$INSTALL_DIR/shared/.env" -f "$INSTALL_DIR/current/docker-compose.yml")
"${COMPOSE[@]}" pull
"${COMPOSE[@]}" up -d
"$SCRIPT_DIR/smoke-test.sh" --dir "$INSTALL_DIR"
echo "安装完成。配置: $INSTALL_DIR/shared/.env（权限 600）"
