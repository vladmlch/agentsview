#!/usr/bin/env bash
# Drive the in-progress rebase past mechanical dataVersion conflicts.
# Stops on anything unexpected: conflicts outside db.go, non-standard
# conflict shapes, empty picks needing --skip decisions, or rebase errors.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 1

for i in $(seq 1 80); do
  if [ ! -d "$(git rev-parse --git-path rebase-merge 2>/dev/null)" ] && \
     [ ! -d "$(git rev-parse --git-path rebase-apply 2>/dev/null)" ]; then
    echo "REBASE DONE"
    exit 0
  fi

  unmerged=$(git diff --name-only --diff-filter=U)
  if [ -n "$unmerged" ]; then
    if [ "$unmerged" != "internal/db/db.go" ]; then
      echo "STOP: unmerged files: $unmerged"
      exit 3
    fi
    out=$(python3 scratch/resolve_dataversion.py)
    rc=$?
    echo "$out"
    if [ $rc -ne 0 ]; then
      echo "STOP: db.go conflict shape not fully handled"
      exit 4
    fi
    # Fix CurrentDataVersion() assertions added by this pick, if any.
    echo "$out" | sed -n 's/^map \([0-9]*\)=\([0-9]*\)$/\1 \2/p' | while read -r old new; do
      if git diff --cached -- internal/db/db_test.go 2>/dev/null | grep -q "CurrentDataVersion(), $old,"; then
        python3 - "$old" "$new" <<'PY'
import re, sys
old, new = sys.argv[1], sys.argv[2]
p = "internal/db/db_test.go"
t = open(p).read()
t2 = t.replace(f"CurrentDataVersion(), {old},", f"CurrentDataVersion(), {new},")
open(p, "w").write(t2)
print(f"db_test.go: remapped assertion {old} -> {new}")
PY
      fi
    done
    git add internal/db/db.go internal/db/db_test.go 2>/dev/null || git add internal/db/db.go
  fi

  cont=$(GIT_EDITOR=true git rebase --continue 2>&1)
  rc=$?
  echo "$cont" | tail -5
  if [ $rc -ne 0 ]; then
    if [ -n "$(git diff --name-only --diff-filter=U)" ]; then
      echo "--- new conflicts; resolving next iteration"
      continue
    fi
    if echo "$cont" | grep -q "previous cherry-pick is now empty\|No changes - did you forget"; then
      echo "EMPTY PICK: skipping"
      git rebase --skip || exit 5
      continue
    fi
    echo "STOP: rebase --continue failed (rc=$rc)"
    exit 6
  fi
done
echo "STOP: iteration limit hit"
exit 7
