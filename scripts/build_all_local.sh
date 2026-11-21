#!/usr/bin/env bash
# ==============================================================================
# Build Local Web & Backend Projects for Production Deployment
# Builds Flutter Web apps, Vite/Node apps, and Go backend on the Mac control node
#
# Usage:
#   ./scripts/build_all_local.sh [all | <site> ...]
#
# Examples:
#   ./scripts/build_all_local.sh              # Build all sites
#   ./scripts/build_all_local.sh bybl         # Build bybl (Frontend + Backend)
#   ./scripts/build_all_local.sh bybl_web     # Build only bybl Frontend
#   ./scripts/build_all_local.sh bybl_backend # Build only bybl Go Backend
#   ./scripts/build_all_local.sh archbtw      # Build only archbtw.sh
#   ./scripts/build_all_local.sh bybl,folja   # Build multiple specific sites
# ==============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_CODE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ANSI Colors
GREEN="\033[0;32m"
CYAN="\033[0;36m"
YELLOW="\033[1;33m"
RED="\033[0;31m"
NC="\033[0m"

usage() {
  echo -e "${CYAN}Usage:${NC} $0 [all | <site> ...]"
  echo ""
  echo "Available site targets:"
  echo "  all              - Build all static sites and bybl backend (default)"
  echo "  archbtw          - archbtw.sh (Flutter Web)"
  echo "  bybl             - bybl.dev Frontend (Flutter) + Backend (Go)"
  echo "  bybl_web         - bybl.dev Frontend only (Flutter Web)"
  echo "  bybl_backend     - api.bybl.dev / gofyr.bybl.dev Backend only (Go)"
  echo "  archerwoods      - archerwoods.dev / fyrWeb (Flutter Web)"
  echo "  folja            - folja.archerwoods.dev / folja.app (Node/Vite)"
  echo "  libretrac        - libretrac.site (Flutter Web)"
  echo "  theolivebranch   - theolivebranch.press (Flutter Web)"
  echo "  welledumacated   - welledumacated.dev (Flutter Web)"
  echo "  fyr              - fyr.software / fyrDE/fyrwebsite (Flutter Web)"
  echo "  carlyleauto      - carlyleauto.online / bcd_landing (Flutter Web)"
  echo "  beats            - beats.archerwoods.dev / beats-by-arch (Flutter Web)"
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

FLUTTER_BIN=""
ensure_flutter() {
  if [ -n "$FLUTTER_BIN" ]; then
    return 0
  fi
  if command -v flutter >/dev/null 2>&1; then
    FLUTTER_BIN="flutter"
  elif [ -x "/Users/torrey/Flutter/flutter/bin/flutter" ]; then
    FLUTTER_BIN="/Users/torrey/Flutter/flutter/bin/flutter"
  elif [ -x "$HOME/development/flutter/bin/flutter" ]; then
    FLUTTER_BIN="$HOME/development/flutter/bin/flutter"
  else
    echo -e "${RED}❌ Error: Flutter executable not found in PATH!${NC}"
    exit 1
  fi
  FLUTTER_INFO="$("$FLUTTER_BIN" --version 2>/dev/null)" || true
  FLUTTER_VER="$(echo "$FLUTTER_INFO" | head -n 1)"
  echo -e "Using Flutter: ${GREEN}${FLUTTER_VER}${NC}"
}

ensure_npm() {
  if ! command -v npm >/dev/null 2>&1; then
    echo -e "${RED}❌ Error: npm is not installed or not in PATH!${NC}"
    exit 1
  fi
}

build_flutter_project() {
  local name="$1"
  local rel_path="$2"
  local target_dir="$ROOT_CODE_DIR/$rel_path"

  ensure_flutter
  echo -e "\n${YELLOW}▶ Building Flutter Web: ${name} (${rel_path})${NC}"
  if [ ! -d "$target_dir" ]; then
    echo -e "${RED}❌ Error: Directory '$target_dir' does not exist!${NC}"
    return 1
  fi

  pushd "$target_dir" >/dev/null
  "$FLUTTER_BIN" build web --release
  if [ "$rel_path" = "beats-by-arch" ] && [ -d "build/web" ]; then
    echo "Applying beats cache-busting assets and service worker purge..."
    local build_ts
    build_ts="$(date +%s)"
    cp build/web/main.dart.js build/web/beats_app.js 2>/dev/null || true
    if [ -f "build/web/assets/fonts/MaterialIcons-Regular.otf" ]; then
      cp build/web/assets/fonts/MaterialIcons-Regular.otf build/web/assets/fonts/MaterialIcons-Beats.otf 2>/dev/null || true
    fi
    python3 -c "
import re
ts = '$build_ts'
try:
    with open('build/web/flutter_bootstrap.js', 'r') as f:
        c = f.read()
    c = c.replace('\"mainJsPath\":\"main.dart.js\"', f'\"mainJsPath\":\"beats_app.js?v={ts}\"')
    c = re.sub(r'serviceWorkerSettings:\s*\{[^}]*\}', 'serviceWorkerSettings: null', c)
    with open('build/web/flutter_bootstrap.js', 'w') as f:
        f.write(c)
except Exception as e:
    print(f'Error updating flutter_bootstrap.js: {e}')

try:
    with open('build/web/assets/FontManifest.json', 'r') as f:
        c = f.read()
    c = c.replace('fonts/MaterialIcons-Regular.otf', f'fonts/MaterialIcons-Beats.otf?v={ts}')
    with open('build/web/assets/FontManifest.json', 'w') as f:
        f.write(c)
except Exception as e:
    print(f'Error updating FontManifest.json: {e}')

try:
    with open('build/web/index.html', 'r') as f:
        c = f.read()
    c = re.sub(r'src=\"flutter_bootstrap\.js[^\"]*\"', f'src=\"flutter_bootstrap.js?v={ts}\"', c)
    with open('build/web/index.html', 'w') as f:
        f.write(c)
except Exception as e:
    print(f'Error updating index.html: {e}')
"
  fi
  popd >/dev/null
  echo -e "${GREEN}✓ Successfully built ${name}!${NC}"
}

build_npm_project() {
  local name="$1"
  local rel_path="$2"
  local target_dir="$ROOT_CODE_DIR/$rel_path"

  ensure_npm
  echo -e "\n${YELLOW}▶ Building Node/Vite Web: ${name} (${rel_path})${NC}"
  if [ ! -d "$target_dir" ]; then
    echo -e "${RED}❌ Error: Directory '$target_dir' does not exist!${NC}"
    return 1
  fi

  pushd "$target_dir" >/dev/null
  if [ ! -d "node_modules" ]; then
    echo "Installing npm dependencies..."
    npm install
  fi
  npm run build
  popd >/dev/null
  echo -e "${GREEN}✓ Successfully built ${name}!${NC}"
}

build_go_backend() {
  local name="$1"
  local rel_path="$2"
  local target_dir="$ROOT_CODE_DIR/$rel_path"

  echo -e "\n${YELLOW}▶ Compiling Go Linux/amd64 Binary: ${name} (${rel_path})${NC}"
  if [ ! -d "$target_dir" ]; then
    echo -e "${RED}❌ Error: Directory '$target_dir' does not exist!${NC}"
    return 1
  fi

  pushd "$target_dir" >/dev/null
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o main .
  popd >/dev/null
  echo -e "${GREEN}✓ Successfully compiled ${name} for Linux/amd64!${NC}"
}

build_single_target() {
  local target
  target="$(echo "$1" | tr '[:upper:]' '[:lower:]')"

  case "$target" in
    all)
      build_flutter_project "archbtw.sh" "archbtw.sh"
      build_flutter_project "bybl (Frontend)" "bybl/Frontend"
      build_go_backend "bybl (Backend)" "bybl/Backend"
      build_flutter_project "fyrWeb (archerwoods.dev)" "fyrWeb"
      build_npm_project "folja.app (folja.archerwoods.dev)" "folja.app"
      build_flutter_project "libretrac.site" "libretrac"
      build_flutter_project "theolivebranch.press" "theolivebranch"
      build_flutter_project "welledumacated.dev" "welledumacated.dev"
      build_flutter_project "fyrDE/fyrwebsite (fyr.software)" "fyrDE/fyrwebsite"
      build_flutter_project "bcd_landing (carlyleauto.online)" "bcd_landing"
      build_flutter_project "beats-by-arch (beats.archerwoods.dev)" "beats-by-arch"
      ;;
    archbtw|archbtw.sh)
      build_flutter_project "archbtw.sh" "archbtw.sh"
      ;;
    bybl)
      build_flutter_project "bybl (Frontend)" "bybl/Frontend"
      build_go_backend "bybl (Backend)" "bybl/Backend"
      ;;
    bybl_web|bybl_frontend|bybl.dev)
      build_flutter_project "bybl (Frontend)" "bybl/Frontend"
      ;;
    bybl_backend|bybl_api|bybl_gofyr|api.bybl.dev|gofyr.bybl.dev)
      build_go_backend "bybl (Backend)" "bybl/Backend"
      ;;
    fyrweb|archerwoods|archerwoods.dev)
      build_flutter_project "fyrWeb (archerwoods.dev)" "fyrWeb"
      ;;
    folja|folja.app|folja.archerwoods.dev)
      build_npm_project "folja.app (folja.archerwoods.dev)" "folja.app"
      ;;
    libretrac|libretrac.site)
      build_flutter_project "libretrac.site" "libretrac"
      ;;
    theolivebranch|theolivebranch.press)
      build_flutter_project "theolivebranch.press" "theolivebranch"
      ;;
    welledumacated|welledumacated.dev)
      build_flutter_project "welledumacated.dev" "welledumacated.dev"
      ;;
    fyrwebsite|fyr|fyr_software|fyr.software)
      build_flutter_project "fyrDE/fyrwebsite (fyr.software)" "fyrDE/fyrwebsite"
      ;;
    bcd_landing|carlyle|carlyleauto|carlyleauto.online)
      build_flutter_project "bcd_landing (carlyleauto.online)" "bcd_landing"
      ;;
    beats|beats-by-arch|beats_by_arch|beats.archerwoods.dev)
      build_flutter_project "beats-by-arch (beats.archerwoods.dev)" "beats-by-arch"
      ;;
    *)
      echo -e "${RED}❌ Error: Unknown site target '${target}'${NC}"
      echo ""
      usage
      exit 1
      ;;
  esac
}

# Collect targets (default to "all"), splitting comma-separated arguments
TARGETS=()
if [ $# -eq 0 ]; then
  TARGETS=("all")
else
  for arg in "$@"; do
    IFS=',' read -ra SPLIT_ARGS <<< "$arg"
    for item in "${SPLIT_ARGS[@]}"; do
      item="$(echo "$item" | xargs)"
      if [ -n "$item" ]; then
        TARGETS+=("$item")
      fi
    done
  done
fi

echo -e "${CYAN}======================================================${NC}"
echo -e "${CYAN}🚀 Building Projects for Production: ${TARGETS[*]}${NC}"
echo -e "${CYAN}======================================================${NC}"

for t in "${TARGETS[@]}"; do
  build_single_target "$t"
done

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}🎉 Requested builds complete (${TARGETS[*]})!${NC}"
echo -e "${GREEN}Assets are ready for Ansible deployment.${NC}"
echo -e "${GREEN}======================================================${NC}"
