#!/usr/bin/env bash
set -Eeuo pipefail
INSTALL_DIR="${CODEX2API_INSTALL_DIR:-/opt/codex2api}"
while (($#)); do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    -h|--help) echo '用法: sudo ./rollback.sh [--dir PATH]'; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
[[ -L "$INSTALL_DIR/previous" ]] || { echo '没有可回滚的版本（previous 链接不存在）' >&2; exit 1; }
PREVIOUS="$(readlink "$INSTALL_DIR/previous")"
ln -sfn "$PREVIOUS" "$INSTALL_DIR/current"
COMPOSE=(docker compose --project-directory "$INSTALL_DIR/current" --env-file "$INSTALL_DIR/shared/.env" -f "$INSTALL_DIR/current/docker-compose.yml")
"${COMPOSE[@]}" up -d
echo "已切回 $PREVIOUS"
