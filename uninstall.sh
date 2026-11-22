#!/usr/bin/env bash
# ==============================================================================
# Duchess Uninstaller
# Removes the installed `duchess` binary. Keeps ~/.duchess data by default
# unless --purge is passed.
# ==============================================================================
set -euo pipefail

BIN_DIR="${DUCHESS_BIN_DIR:-$HOME/.local/bin}"
HOME_DIR="${DUCHESS_HOME:-$HOME/.duchess}"
PURGE=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin-dir)
      BIN_DIR="${2:?--bin-dir requires a directory}"
      shift 2
      ;;
    --home-dir)
      HOME_DIR="${2:?--home-dir requires a directory}"
      shift 2
      ;;
    --system)
      BIN_DIR="/usr/local/bin"
      shift
      ;;
    --purge)
      PURGE=true
      shift
      ;;
    -h|--help)
      echo "Usage: $0 [--bin-dir <dir>] [--home-dir <dir>] [--system] [--purge]"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

TARGET_BIN="$BIN_DIR/duchess"
if [ -f "$TARGET_BIN" ]; then
  if [ -w "$BIN_DIR" ]; then
    rm -f "$TARGET_BIN"
  else
    sudo rm -f "$TARGET_BIN"
  fi
  echo "Removed $TARGET_BIN"
else
  echo "Binary not found at $TARGET_BIN"
fi

if [ "$PURGE" = true ]; then
  if [ -d "$HOME_DIR" ]; then
    rm -rf "$HOME_DIR"
    echo "Purged $HOME_DIR"
  fi
else
  echo "Preserved user workspace at $HOME_DIR (pass --purge to remove it)"
fi
