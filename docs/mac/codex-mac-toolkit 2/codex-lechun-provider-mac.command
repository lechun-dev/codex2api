#!/bin/bash
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIGURE_SCRIPT="$TOOL_DIR/configure-lechun-provider.mjs"
REPAIR_SCRIPT="$TOOL_DIR/repair-codex-history-provider.mjs"
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
NODE_BIN="${NODE_BIN:-}"

pause() {
  echo
  read -r -p "Press Enter to close..."
}

find_node() {
  if [ -n "$NODE_BIN" ]; then
    return
  fi
  if command -v node >/dev/null 2>&1; then
    NODE_BIN="$(command -v node)"
  elif [ -x "/Applications/Codex.app/Contents/Resources/cua_node/bin/node" ]; then
    NODE_BIN="/Applications/Codex.app/Contents/Resources/cua_node/bin/node"
  else
    echo "Node.js was not found."
    echo "Install Node.js, or set NODE_BIN to a node executable, then run this tool again."
    pause
    exit 1
  fi
}

for required in "$CONFIGURE_SCRIPT" "$REPAIR_SCRIPT"; do
  if [ ! -f "$required" ]; then
    echo "Required script not found:"
    echo "  $required"
    pause
    exit 1
  fi
done

if [ ! -d "$CODEX_HOME" ]; then
  echo "Codex home not found:"
  echo "  $CODEX_HOME"
  pause
  exit 1
fi

find_node

echo "Codex Lechun provider setup"
echo "Codex home: $CODEX_HOME"
echo "Node: $NODE_BIN"
echo
echo "Close Codex Desktop before continuing."
echo

printf "Enter Lechun API key: "
stty -echo
IFS= read -r LECHUN_API_KEY
stty echo
printf "\n"

if [ -z "${LECHUN_API_KEY// }" ]; then
  echo "API key cannot be empty."
  pause
  exit 1
fi
export LECHUN_API_KEY

echo
echo "Step 1: provider config dry-run. No files will be changed."
echo
"$NODE_BIN" "$CONFIGURE_SCRIPT" --codex-home "$CODEX_HOME"

echo
echo "Step 2: history repair dry-run. No files will be changed."
echo
"$NODE_BIN" "$REPAIR_SCRIPT" --codex-home "$CODEX_HOME" --provider lechun

echo
echo "Type CONFIGURE to apply provider config and history repair."
read -r -p "> " CONFIRM
if [ "$CONFIRM" != "CONFIGURE" ]; then
  echo "Canceled."
  pause
  exit 0
fi

echo
echo "Applying provider config..."
echo "Full snapshot backup may take several minutes. Progress lines start with [snapshot]."
"$NODE_BIN" "$CONFIGURE_SCRIPT" --codex-home "$CODEX_HOME" --apply --yes

echo
echo "Applying history repair..."
"$NODE_BIN" "$REPAIR_SCRIPT" --codex-home "$CODEX_HOME" --provider lechun --apply --yes

echo
echo "Done. Restart Codex Desktop."
echo "Do not share config.toml because it now contains your API key."
pause
