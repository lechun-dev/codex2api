#!/usr/bin/env bash
# 扫出 Go 源码里「驱动层重写救不了」的 PostgreSQL 专有 SQL，供 MySQL 5.6 兼容性人工复核。
#
# 驱动 database/mysql_driver.go 只会处理这几类：$n 占位符、::type 强转、
# CAST(x AS TEXT)、BIGINT 强转、简单 ON CONFLICT、api_keys 列名、clientFoundRows。
# 因此这里只找它覆盖不到的语法。命中不代表一定有 bug——可能整段落在
# isMySQL()/isSQLite() 分支里，所以脚本会把附近有方言分支的标出来并跳过。
#
# 用法：
#   .github/scripts/sql-dialect-audit.sh              # 扫全仓
#   .github/scripts/sql-dialect-audit.sh <rev>        # 只看 <rev>..HEAD 新增行（合并上游后用）
#
# 注意：database/postgres.go 是「PG 方言 SQL 的共享实现」，MySQL 也走这里再由驱动重写，
# 所以默认跳过该文件，只看其它文件里未加方言分支的写法。
set -euo pipefail

cd "$(dirname "$0")/../.."

patterns=(
  'RETURNING'
  'LATERAL'
  'jsonb_'
  'OVER ('
  'CREATE INDEX IF NOT EXISTS'
  'ADD COLUMN IF NOT EXISTS'
  'TIMESTAMPTZ'
  'BIGSERIAL'
)

range=()
if [ "$#" -ge 1 ]; then
  range=("$1..HEAD")
fi

detail_file=$(mktemp)
trap 'rm -f "$detail_file"' EXIT

total=0
unguarded=0
for p in "${patterns[@]}"; do
  out=$(git grep -n -F "$p" -- '*.go' ':!database/postgres.go' ':!*_test.go' ${range[@]+"${range[@]}"} 2>/dev/null || true)
  [ -n "$out" ] || continue
  while IFS= read -r line; do
    total=$((total + 1))
    file=${line%%:*}
    lineno=$(printf '%s' "$line" | cut -d: -f2)
    start=$((lineno > 80 ? lineno - 80 : 1))
    end=$((lineno + 40))
    if sed -n "${start},${end}p" "$file" 2>/dev/null | grep -q 'isMySQL()\|isSQLite()'; then
      continue
    fi
    unguarded=$((unguarded + 1))
    printf '%s\n' "$line" >> "$detail_file"
  done <<< "$out"
done

echo "== 未检测到方言分支的 PG 专有 SQL（需人工确认 MySQL 路径会不会执行到）=="
if [ "$unguarded" -eq 0 ]; then
  echo "（无）"
else
  sed 's/^/  /' "$detail_file"
fi
echo
skipped=$((total - unguarded))
echo "candidates: ${total}（其中 ${skipped} 处附近已有 isMySQL/isSQLite 分支，已跳过），unguarded: ${unguarded}"
