#!/usr/bin/env bash
# ==============================================================================
# Duchess Installer
# Builds and installs the `duchess` binary into PATH (~/.local/bin by default)
# and sets up playbooks, scripts, inventory, and config in ~/.duchess/.
#
# Usage:
#   ./install.sh
#   ./install.sh --bin-dir ~/.local/bin --home-dir ~/.duchess
#   ./install.sh --system          # Install binary to /usr/local/bin (uses sudo)
#   ./install.sh --force           # Overwrite existing duchess.yml & inventory
#   ./install.sh --no-path         # Do not modify shell rc files
# ==============================================================================
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CODE_PARENT_DIR="$(cd "$REPO_DIR/.." && pwd)"

BIN_DIR="${DUCHESS_BIN_DIR:-$HOME/.local/bin}"
HOME_DIR="${DUCHESS_HOME:-$HOME/.duchess}"
FORCE=false
MODIFY_PATH=true
SYSTEM_INSTALL=false

GREEN="\033[0;32m"
CYAN="\033[0;36m"
YELLOW="\033[1;33m"
RED="\033[0;31m"
NC="\033[0m"

usage() {
  cat <<EOF
Usage: $0 [options]

Options:
  --bin-dir <dir>    Directory to install the duchess binary (default: ~/.local/bin)
  --home-dir <dir>   Directory for playbooks, scripts, inventory, config (default: ~/.duchess)
  --system           Install binary to /usr/local/bin (may prompt for sudo)
  --force            Overwrite existing duchess.yml and inventory files in home dir
  --no-path          Skip adding bin-dir to shell PATH rc files
  -h, --help         Show this help message
EOF
}

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
      SYSTEM_INSTALL=true
      BIN_DIR="/usr/local/bin"
      shift
      ;;
    --force)
      FORCE=true
      shift
      ;;
    --no-path)
      MODIFY_PATH=false
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo -e "${RED}Unknown option: $1${NC}" >&2
      usage
      exit 1
      ;;
  esac
done

echo -e "${CYAN}======================================================${NC}"
echo -e "${CYAN}  Installing Duchess DevOps Control Plane${NC}"
echo -e "${CYAN}======================================================${NC}"
echo "  Binary target : ${BIN_DIR}/duchess"
echo "  Duchess home  : ${HOME_DIR}"
echo ""

# ------------------------------------------------------------------------------
# 1. Build (or reuse) the duchess binary
# ------------------------------------------------------------------------------
TMP_BIN="$(mktemp)"
trap 'rm -f "$TMP_BIN"' EXIT

if command -v go >/dev/null 2>&1; then
  echo -e "${YELLOW}▶ Building duchess from source...${NC}"
  (cd "$REPO_DIR/cli" && go build -o "$TMP_BIN" .)
  # Also keep cli/bin/duchess up to date
  mkdir -p "$REPO_DIR/cli/bin"
  cp "$TMP_BIN" "$REPO_DIR/cli/bin/duchess"
elif [ -x "$REPO_DIR/cli/bin/duchess" ]; then
  echo -e "${YELLOW}▶ Go not found; using prebuilt cli/bin/duchess...${NC}"
  cp "$REPO_DIR/cli/bin/duchess" "$TMP_BIN"
else
  echo -e "${RED}Error: Go is not installed and no prebuilt cli/bin/duchess binary was found.${NC}" >&2
  exit 1
fi

# Install binary
if [ "$SYSTEM_INSTALL" = true ] && [ ! -w "$BIN_DIR" ]; then
  sudo mkdir -p "$BIN_DIR"
  sudo install -m 0755 "$TMP_BIN" "$BIN_DIR/duchess"
else
  mkdir -p "$BIN_DIR"
  install -m 0755 "$TMP_BIN" "$BIN_DIR/duchess"
fi
echo -e "${GREEN}✓ Installed binary to ${BIN_DIR}/duchess${NC}"

# ------------------------------------------------------------------------------
# 2. Populate ~/.duchess/{playbooks,scripts,inventory,history,duchess.yml}
# ------------------------------------------------------------------------------
echo -e "${YELLOW}▶ Installing playbooks, scripts, inventory, and config to ${HOME_DIR}...${NC}"
mkdir -p "$HOME_DIR/playbooks" "$HOME_DIR/scripts" "$HOME_DIR/inventory" "$HOME_DIR/history"

# Preserve any existing vault.yml secrets in ~/.duchess/playbooks/group_vars/all/vault.yml
VAULT_BACKUP=""
if [ -f "$HOME_DIR/playbooks/group_vars/all/vault.yml" ]; then
  VAULT_BACKUP="$(mktemp)"
  cp "$HOME_DIR/playbooks/group_vars/all/vault.yml" "$VAULT_BACKUP"
fi

# Copy playbooks, roles, group_vars, and ansible.cfg
cp -R "$REPO_DIR/playbooks/." "$HOME_DIR/playbooks/"

if [ -n "$VAULT_BACKUP" ]; then
  mkdir -p "$HOME_DIR/playbooks/group_vars/all"
  cp "$VAULT_BACKUP" "$HOME_DIR/playbooks/group_vars/all/vault.yml"
  rm -f "$VAULT_BACKUP"
fi

# Point installed vars.yml local_code_root fallback to the parent code directory
# so site deploy playbooks still locate sibling projects when run from ~/.duchess
if [ -f "$HOME_DIR/playbooks/group_vars/all/vars.yml" ]; then
  python3 - "$HOME_DIR/playbooks/group_vars/all/vars.yml" "$CODE_PARENT_DIR" <<'PY'
import sys
path, code_root = sys.argv[1], sys.argv[2]
with open(path, "r", encoding="utf-8") as f:
    text = f.read()
old = 'local_code_root: "{{ lookup(\'env\', \'DUCHESS_CODE_ROOT\') | default(playbook_dir + \'/../..\', true) }}"'
new = f'local_code_root: "{{{{ lookup(\'env\', \'DUCHESS_CODE_ROOT\') | default(\'{code_root}\', true) }}}}"'
if old in text:
    with open(path, "w", encoding="utf-8") as f:
        f.write(text.replace(old, new, 1))
PY
fi

# Copy scripts and preserve executable bit
cp -R "$REPO_DIR/scripts/." "$HOME_DIR/scripts/"
chmod +x "$HOME_DIR/scripts/"*.sh 2>/dev/null || true

if [ -f "$HOME_DIR/scripts/build_all_local.sh" ]; then
  python3 - "$HOME_DIR/scripts/build_all_local.sh" "$CODE_PARENT_DIR" <<'PY'
import sys
path, code_root = sys.argv[1], sys.argv[2]
with open(path, "r", encoding="utf-8") as f:
    text = f.read()
old = 'ROOT_CODE_DIR="${DUCHESS_CODE_ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"'
new = f'ROOT_CODE_DIR="${{DUCHESS_CODE_ROOT:-{code_root}}}"'
if old in text:
    with open(path, "w", encoding="utf-8") as f:
        f.write(text.replace(old, new, 1))
PY
fi

# Copy inventory files only if missing (or --force) so user servers aren't overwritten
for inv_file in hosts.ini hosts.yml; do
  if [ -f "$REPO_DIR/inventory/$inv_file" ]; then
    if [ ! -f "$HOME_DIR/inventory/$inv_file" ] || [ "$FORCE" = true ]; then
      cp "$REPO_DIR/inventory/$inv_file" "$HOME_DIR/inventory/$inv_file"
      echo -e "${GREEN}✓ Installed ${HOME_DIR}/inventory/${inv_file}${NC}"
    else
      echo -e "  Preserved existing ${HOME_DIR}/inventory/${inv_file}"
    fi
  fi
done

# Install or create ~/.duchess/duchess.yml (preserving existing config unless --force)
if [ ! -f "$HOME_DIR/duchess.yml" ] || [ "$FORCE" = true ]; then
  if [ -f "$REPO_DIR/duchess.yml" ]; then
    cp "$REPO_DIR/duchess.yml" "$HOME_DIR/duchess.yml"
    echo -e "${GREEN}✓ Copied workspace duchess.yml to ${HOME_DIR}/duchess.yml${NC}"
  else
    cat > "$HOME_DIR/duchess.yml" <<EOF
# Duchess Workspace Configuration
inventory: inventory/hosts.ini
poll_interval_seconds: 10
exec_timeout_seconds: 0
ssh:
  use_ssh_config: true
  strict_host_key: false
alerts:
  disk_warn_pct: 85
  disk_crit_pct: 95
  mem_warn_pct: 90
  cpu_warn_pct: 90
  load_factor: 1.5
  cert_warn_days: 14
EOF
    echo -e "${GREEN}✓ Created starter config at ${HOME_DIR}/duchess.yml${NC}"
  fi
else
  echo -e "  Preserved existing ${HOME_DIR}/duchess.yml"
fi

echo -e "${GREEN}✓ Installed playbooks ($(find "$HOME_DIR/playbooks" -maxdepth 1 -name '*.yml' | wc -l | tr -d ' ')) and scripts ($(find "$HOME_DIR/scripts" -maxdepth 1 -name '*.sh' | wc -l | tr -d ' ')) to ${HOME_DIR}${NC}"

# ------------------------------------------------------------------------------
# 3. Add BIN_DIR to PATH in user's shell rc file(s)
# ------------------------------------------------------------------------------
add_path_posix() {
  local rc_file="$1"
  local export_line="export PATH=\"${BIN_DIR}:\$PATH\""
  if [ -f "$rc_file" ] && grep -Fq "$BIN_DIR" "$rc_file"; then
    echo -e "  PATH already configured in ${rc_file}"
    return 0
  fi
  {
    echo ""
    echo "# Added by Duchess installer"
    echo "$export_line"
  } >> "$rc_file"
  echo -e "${GREEN}✓ Added ${BIN_DIR} to PATH in ${rc_file}${NC}"
}

add_path_fish() {
  local fish_cfg="$HOME/.config/fish/config.fish"
  mkdir -p "$(dirname "$fish_cfg")"
  if [ -f "$fish_cfg" ] && grep -Fq "$BIN_DIR" "$fish_cfg"; then
    echo -e "  PATH already configured in ${fish_cfg}"
    return 0
  fi
  {
    echo ""
    echo "# Added by Duchess installer"
    echo "fish_add_path ${BIN_DIR}"
  } >> "$fish_cfg"
  echo -e "${GREEN}✓ Added ${BIN_DIR} to PATH in ${fish_cfg}${NC}"
}

PATH_UPDATED=false
if [ "$MODIFY_PATH" = true ]; then
  case ":${PATH}:" in
    *":${BIN_DIR}:"*)
      echo -e "${GREEN}✓ ${BIN_DIR} is already in your current PATH${NC}"
      ;;
    *)
      SHELL_NAME="$(basename "${SHELL:-}")"
      case "$SHELL_NAME" in
        zsh)
          add_path_posix "$HOME/.zshrc"
          PATH_UPDATED=true
          ;;
        fish)
          add_path_fish
          PATH_UPDATED=true
          ;;
        bash)
          add_path_posix "$HOME/.bashrc"
          PATH_UPDATED=true
          ;;
        *)
          add_path_posix "$HOME/.profile"
          PATH_UPDATED=true
          ;;
      esac
      ;;
  esac
fi

echo ""
echo -e "${GREEN}======================================================${NC}"
echo -e "${GREEN}  Duchess installed successfully!${NC}"
echo -e "${GREEN}======================================================${NC}"
echo "  Binary    : ${BIN_DIR}/duchess"
echo "  Config    : ${HOME_DIR}/duchess.yml"
echo "  Inventory : ${HOME_DIR}/inventory/hosts.ini"
echo "  Playbooks : ${HOME_DIR}/playbooks/"
echo "  Scripts   : ${HOME_DIR}/scripts/"
echo ""
if [ "$PATH_UPDATED" = true ]; then
  echo -e "${YELLOW}Restart your terminal (or run: export PATH=\"${BIN_DIR}:\$PATH\") to use 'duchess' immediately.${NC}"
else
  echo "Run 'duchess' from any directory to launch the TUI!"
fi
