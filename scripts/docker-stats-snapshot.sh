#!/usr/bin/env bash
# duchess:name: Docker Container Stats Snapshot
# duchess:description: Displays running containers with live CPU, memory, and network usage.
# duchess:category: containers
set -euo pipefail

if command -v docker >/dev/null 2>&1; then
  echo "=== Running Docker Containers ==="
  docker ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
  echo ""
  echo "=== Resource Snapshot ==="
  docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.NetIO}}"
elif command -v podman >/dev/null 2>&1; then
  echo "=== Running Podman Containers ==="
  podman ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
  echo ""
  podman stats --no-stream
else
  echo "Neither docker nor podman is installed on $(hostname)."
fi
