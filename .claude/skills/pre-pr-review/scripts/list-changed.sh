#!/usr/bin/env bash
# Emit the three name-status lists matching capture-diff.sh.
set -euo pipefail

BASE="${1:?usage: list-changed.sh <base-ref>}"

echo "=== name-status vs ${BASE} (committed) ==="
git diff --name-status "${BASE}"
echo ""
echo "=== name-status staged ==="
git diff --cached --name-status
echo ""
echo "=== name-status unstaged ==="
git diff --name-status