#!/usr/bin/env bash
# Detect the most likely parent (source) branch of the current branch.
# Outputs JSON: {current, parent, base, candidates: [{branch, depth}]}
set -euo pipefail

CURRENT=$(git branch --show-current)
if [ -z "$CURRENT" ]; then
  echo '{"error": "detached HEAD or not on a branch"}'
  exit 1
fi

HEAD_SHA=$(git rev-parse HEAD)

CANDIDATES=$(git for-each-ref --format='%(refname:short)' refs/heads/ \
  | grep -vx "$CURRENT" \
  | while read -r b; do
      mb=$(git merge-base HEAD "$b" 2>/dev/null) || continue
      [ "$mb" = "$HEAD_SHA" ] && continue   # b is a descendant of HEAD
      depth=$(git rev-list --count "$mb..HEAD")
      printf '%s %s\n' "$depth" "$b"
    done \
  | sort -n)

PARENT=$(echo "$CANDIDATES" | head -1 | awk '{print $2}')
[ -z "$PARENT" ] && PARENT=main

BASE=$(git merge-base HEAD "$PARENT")

CAND_JSON=$(echo "$CANDIDATES" \
  | awk 'NF {printf "%s{\"branch\":\"%s\",\"depth\":%s}", (NR>1?",":""), $2, $1}')

cat <<EOF
{"current":"$CURRENT","parent":"$PARENT","base":"$BASE","candidates":[$CAND_JSON]}
EOF