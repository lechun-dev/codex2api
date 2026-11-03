#!/usr/bin/env bash
# 会话存档排除功能（CODEX_CONVERSATION_EXCLUDE）完整性检查。
#
# 这是本地自定义功能，上游代码里没有。从上游合并分支时，如果冲突解决
# 不当，这一块的代码可能被上游版本覆盖掉。合并完成后运行本脚本：
# 任何一项缺失都会以非零码退出，提示恢复相关代码。
#
# 详细说明见 docs/UPSTREAM-MERGE-PROTECTION.md。
set -euo pipefail

# 无论从哪个目录调用，都以仓库根（含 .git 的目录）为基准。
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
while [ "$REPO_ROOT" != "/" ] && [ ! -d "$REPO_ROOT/.git" ]; do
  REPO_ROOT="$(dirname "$REPO_ROOT")"
done
cd "$REPO_ROOT"

fail=0

check_file() {
  local f="$1"
  if [ -f "$f" ]; then
    echo "ok   file  $f"
  else
    echo "MISS file  $f"
    fail=1
  fi
}

check_sym() {
  local sym="$1" f="$2"
  if grep -qF -- "$sym" "$f" 2>/dev/null; then
    echo "ok   sym   [$sym] @ $f"
  else
    echo "MISS sym   [$sym] @ $f"
    fail=1
  fi
}

echo "== 会话存档排除功能完整性检查（codex2api）=="

# 核心实现
check_file config/conversation_archive_exclude.go
check_file proxy/conversation_archive_exclude.go
check_sym 'func ParseConversationArchiveExclude' config/conversation_archive_exclude.go
check_sym 'ConversationArchiveExclude ConversationArchiveExclude' config/config.go
check_sym 'ParseConversationArchiveExclude(os.Getenv("CODEX_CONVERSATION_EXCLUDE"))' config/config.go
check_sym 'exclude: newArchiveExclusion(exclude)' proxy/conversation_archive.go
check_sym 'archiver.exclude.match(archiveIdentity{' proxy/conversation_archive.go
check_sym 'func archiveRequestMachineID' proxy/conversation_archive_exclude.go

# 文档与示例配置
check_sym 'CODEX_CONVERSATION_EXCLUDE' .env.example
check_sym 'CODEX_CONVERSATION_EXCLUDE' docs/conversation-archive.md

# 回归测试
check_file proxy/conversation_archive_e2e_test.go
check_file config/config_test.go

if [ "$fail" -ne 0 ]; then
  echo
  echo "!! 会话存档排除功能缺失。这是本地自定义功能，上游没有这段代码。"
  echo "!! 请对照 docs/UPSTREAM-MERGE-PROTECTION.md 恢复相关代码，"
  echo "!! 不要在合并冲突中直接采用上游版本。"
  exit 1
fi

echo "== 全部通过 =="
