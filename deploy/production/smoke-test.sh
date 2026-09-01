#!/usr/bin/env bash
set -Eeuo pipefail
INSTALL_DIR="${CODEX2API_INSTALL_DIR:-/opt/codex2api}"
while (($#)); do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    -h|--help) echo '用法: ./smoke-test.sh [--dir PATH]'; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
[[ -f "$INSTALL_DIR/shared/.env" ]] || { echo '找不到部署配置' >&2; exit 1; }
PORT="$(sed -n 's/^CODEX_PORT=//p' "$INSTALL_DIR/shared/.env" | head -n1)"; PORT="${PORT:-8080}"
BIND="$(sed -n 's/^BIND_HOST=//p' "$INSTALL_DIR/shared/.env" | head -n1)"; BIND="${BIND:-127.0.0.1}"
curl --fail --silent --show-error --max-time 10 "http://$BIND:$PORT/health" >/dev/null
docker compose --project-directory "$INSTALL_DIR/current" --env-file "$INSTALL_DIR/shared/.env" -f "$INSTALL_DIR/current/docker-compose.yml" ps
echo "健康检查通过: http://$BIND:$PORT/health"
