#!/bin/bash
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "$0")" && pwd)"
REPAIR_SCRIPT="$TOOL_DIR/repair-codex-history-provider.mjs"
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
NODE_BIN="${NODE_BIN:-}"

pause() {
  echo
  read -r -p "Press Enter to close..."
}

if [ ! -f "$REPAIR_SCRIPT" ]; then
  echo "Repair script not found:"
  echo "  $REPAIR_SCRIPT"
  pause
  exit 1
fi

if [ -z "$NODE_BIN" ]; then
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
fi

if [ ! -d "$CODEX_HOME" ]; then
  echo "Codex home not found:"
  echo "  $CODEX_HOME"
  pause
  exit 1
fi

echo "Codex history repair"
echo "Codex home: $CODEX_HOME"
echo "Node: $NODE_BIN"
echo
echo "Step 1: dry-run. No files will be changed."
echo
"$NODE_BIN" "$REPAIR_SCRIPT" --codex-home "$CODEX_HOME"

echo
echo "Close Codex Desktop before continuing."
echo "Type REPAIR to apply the changes shown above."
read -r -p "> " CONFIRM
if [ "$CONFIRM" != "REPAIR" ]; then
  echo "Canceled."
  pause
  exit 0
fi

echo
echo "Step 2: applying repair."
echo
"$NODE_BIN" "$REPAIR_SCRIPT" --codex-home "$CODEX_HOME" --apply --yes

echo
echo "History repair finished. Restart Codex Desktop."
pause
