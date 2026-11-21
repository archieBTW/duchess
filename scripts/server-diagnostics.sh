#!/usr/bin/env bash
# duchess:name: Server Diagnostics Snapshot
# duchess:description: Prints hostname, uptime, top CPU/memory processes, listening ports, and failed units.
# duchess:category: diagnostics
set -euo pipefail

echo "=== Host Summary ==="
echo "Host: $(hostname -f 2>/dev/null || hostname)"
echo "Kernel: $(uname -srm)"
echo "Uptime: $(uptime)"
echo ""
echo "=== Top CPU Processes ==="
ps -eo pid,ppid,user,%cpu,%mem,comm --sort=-%cpu | head -n 10
echo ""
echo "=== Listening TCP Ports ==="
ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || echo "ss/netstat unavailable"
echo ""
echo "=== Failed Systemd Units ==="
systemctl --failed --no-pager 2>/dev/null || echo "systemd unavailable"
