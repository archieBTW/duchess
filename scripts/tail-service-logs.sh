#!/usr/bin/env bash
# duchess:name: Tail Service Logs
# duchess:description: Fetches the most recent log entries for a specified systemd service (default: nginx).
# duchess:category: diagnostics
# duchess:vars: SERVICE_NAME=nginx,LINES=50
set -euo pipefail

SERVICE_NAME="${SERVICE_NAME:-nginx}"
LINES="${LINES:-50}"

echo "=== Last ${LINES} log lines for ${SERVICE_NAME} ==="
if command -v journalctl >/dev/null 2>&1; then
  journalctl -u "${SERVICE_NAME}" -n "${LINES}" --no-pager || true
else
  echo "journalctl not available; checking /var/log..."
  tail -n "${LINES}" "/var/log/${SERVICE_NAME}/error.log" 2>/dev/null || echo "No logs found for ${SERVICE_NAME}"
fi
