#!/usr/bin/env bash
# duchess:name: Clear Application Cache
# duchess:description: Safely clears application temp/cache directories or flushes local Redis cache for self-service support.
# duchess:category: maintenance
# duchess:vars: CACHE_DIR=/var/cache/nginx
set -euo pipefail

CACHE_DIR="${CACHE_DIR:-/var/cache/nginx}"
echo "Checking cache path: ${CACHE_DIR}"

if [ -d "${CACHE_DIR}" ]; then
  BEFORE=$(du -sh "${CACHE_DIR}" 2>/dev/null | awk '{print $1}')
  find "${CACHE_DIR}" -mindepth 1 -maxdepth 2 -type f -delete 2>/dev/null || true
  AFTER=$(du -sh "${CACHE_DIR}" 2>/dev/null | awk '{print $1}')
  echo "Cleared files in ${CACHE_DIR} (before: ${BEFORE}, after: ${AFTER})"
else
  echo "Cache directory ${CACHE_DIR} does not exist on $(hostname); no changes made."
fi
