#!/usr/bin/env bash
# Emit the three concatenated diffs (committed-vs-base, staged, unstaged)
# with clear separators so the orchestrator can split them if needed.
set -euo pipefail

BASE="${1:?usage: capture-diff.sh <base-ref>}"

echo "=== diff vs ${BASE} (committed) ==="
git diff "${BASE}...HEAD"
echo ""
echo "=== diff staged ==="
git diff --cached
echo ""
echo "=== diff unstaged ==="
git diff