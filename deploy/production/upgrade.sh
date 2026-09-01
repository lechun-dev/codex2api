#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${CODEX2API_INSTALL_DIR:-/opt/codex2api}"
VERSION=""
IMAGE=""
while (($#)); do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    -h|--help) echo '用法: sudo ./upgrade.sh --version vX.Y.Z [--image IMAGE] [--dir PATH]'; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
[[ -n "$VERSION" ]] || { echo '必须指定 --version' >&2; exit 1; }
[[ -f "$INSTALL_DIR/shared/.env" ]] || { echo "找不到 $INSTALL_DIR/shared/.env，请先安装" >&2; exit 1; }
command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 || { echo '需要 Docker Compose v2' >&2; exit 1; }
IMAGE="${IMAGE:-ghcr.io/james-6-23/codex2api:${VERSION}}"
umask 077
mkdir -p "$INSTALL_DIR/releases/$VERSION/logs" "$INSTALL_DIR/backups"
cp "$SCRIPT_DIR/docker-compose.yml" "$INSTALL_DIR/releases/$VERSION/docker-compose.yml"
CURRENT_TARGET="$(readlink "$INSTALL_DIR/current" 2>/dev/null || true)"
[[ -n "$CURRENT_TARGET" ]] && ln -sfn "$CURRENT_TARGET" "$INSTALL_DIR/previous"
ln -sfn "releases/$VERSION" "$INSTALL_DIR/current.new"
if grep -q '^CODEX2API_IMAGE=' "$INSTALL_DIR/shared/.env"; then
  sed -i.bak "s#^CODEX2API_IMAGE=.*#CODEX2API_IMAGE=$IMAGE#" "$INSTALL_DIR/shared/.env"
else
  printf '\nCODEX2API_IMAGE=%s\n' "$IMAGE" >> "$INSTALL_DIR/shared/.env"
fi
COMPOSE=(docker compose --project-directory "$INSTALL_DIR/current.new" --env-file "$INSTALL_DIR/shared/.env" -f "$INSTALL_DIR/current.new/docker-compose.yml")
"${COMPOSE[@]}" pull
"${COMPOSE[@]}" up -d
ln -sfn "releases/$VERSION" "$INSTALL_DIR/current"
rm -f "$INSTALL_DIR/current.new" "$INSTALL_DIR/shared/.env.bak"
"$SCRIPT_DIR/smoke-test.sh" --dir "$INSTALL_DIR"
echo "已升级到 $VERSION。失败时运行 ./rollback.sh --dir $INSTALL_DIR"
