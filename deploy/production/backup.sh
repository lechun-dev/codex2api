#!/usr/bin/env bash
set -Eeuo pipefail
INSTALL_DIR="${CODEX2API_INSTALL_DIR:-/opt/codex2api}"
while (($#)); do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    -h|--help) echo '用法: sudo ./backup.sh [--dir PATH]'; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
[[ -f "$INSTALL_DIR/shared/.env" ]] || { echo '找不到部署配置' >&2; exit 1; }
STAMP="$(date +%Y%m%d-%H%M%S)"
DEST="$INSTALL_DIR/backups/$STAMP"
mkdir -p "$DEST"
env_value() { sed -n "s/^$1=//p" "$INSTALL_DIR/shared/.env" | head -n1; }
DATABASE_USER="$(env_value DATABASE_USER)"; DATABASE_USER="${DATABASE_USER:-codex2api}"
DATABASE_NAME="$(env_value DATABASE_NAME)"; DATABASE_NAME="${DATABASE_NAME:-codex2api}"
COMPOSE=(docker compose --project-directory "$INSTALL_DIR/current" --env-file "$INSTALL_DIR/shared/.env" -f "$INSTALL_DIR/current/docker-compose.yml")
"${COMPOSE[@]}" exec -T postgres pg_dump -U "$DATABASE_USER" "$DATABASE_NAME" > "$DEST/postgres.sql"
docker run --rm -v codex2api_image-assets:/data -v "$DEST":/backup alpine:3.19 tar czf /backup/image-assets.tgz -C /data .
cp "$INSTALL_DIR/shared/.env" "$DEST/env.snapshot"
chmod 600 "$DEST"/*
echo "备份完成: $DEST"
