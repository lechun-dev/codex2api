#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
VERSION="${1:-v2.8.2}"
OUT_DIR="${2:-$SCRIPT_DIR/../artifacts}"
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
ARCHIVE="$OUT_DIR/codex2api-production-$VERSION.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
PACKAGE_DIR="$TMP/codex2api-production-$VERSION"
mkdir -p "$PACKAGE_DIR"
cp "$SCRIPT_DIR"/{README.md,.env.example,docker-compose.yml,install.sh,upgrade.sh,rollback.sh,backup.sh,smoke-test.sh} "$PACKAGE_DIR/"
cp -R "$SCRIPT_DIR/ansible" "$PACKAGE_DIR/ansible"
chmod +x "$PACKAGE_DIR"/*.sh
printf 'version=%s\nbuilt_at=%s\nimage=ghcr.io/james-6-23/codex2api:%s\n' "$VERSION" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$VERSION" > "$PACKAGE_DIR/release-manifest"
(cd "$TMP" && tar -czf "$ARCHIVE" "$(basename "$PACKAGE_DIR")")
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$ARCHIVE" > "$ARCHIVE.sha256"
else
  shasum -a 256 "$ARCHIVE" > "$ARCHIVE.sha256"
fi
echo "已生成: $ARCHIVE"
echo "校验文件: $ARCHIVE.sha256"
