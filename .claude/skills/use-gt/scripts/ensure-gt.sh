#!/bin/sh
# Ensure the `gt` CLI is installed locally.
# Installs from the official release script if `gt` is not on PATH.
set -eu

if command -v gt >/dev/null 2>&1; then
  echo "gt already installed at $(command -v gt)"
  exit 0
fi

echo "gt not found on PATH, installing..."
curl -fsSL https://raw.githubusercontent.com/pedromvgomes/gt/main/install.sh | sh

if ! command -v gt >/dev/null 2>&1; then
  cat >&2 <<'EOF'
gt installation completed but the binary is not on PATH.
The installer likely placed it at $HOME/.local/bin/gt.
Add that directory to PATH, e.g.:
  export PATH="$HOME/.local/bin:$PATH"
EOF
  exit 1
fi

echo "gt installed: $(gt --version 2>/dev/null || command -v gt)"
