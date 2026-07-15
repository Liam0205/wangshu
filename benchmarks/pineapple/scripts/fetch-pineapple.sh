#!/usr/bin/env bash
# fetch-pineapple.sh -- git clone (or git fetch + reset) pineapple into this
# repo's benchmarks/pineapple/.pineapple/, tracking master HEAD.
#
# Design principles:
#   - .pineapple/ is in benchmarks/pineapple/.gitignore, never tracked by wangshu
#   - no pinned commit hash; clone master HEAD and let each developer fetch
#     the latest locally
#   - numbers drift with pineapple master -- intentionally the "real
#     downstream shape": when pineapple ships a new adapter optimization the
#     wangshu bench numbers follow, and vice versa
#   - CI fetches in a workflow step; local developers fetch before each bench run
#   - idempotent: an existing .pineapple/ gets fetch + reset, no re-clone
#
# Risk record (accepted by the user): pineapple master may introduce a
# breaking change that stops the wangshu bench from compiling -- then look at
# what pineapple changed and sync locally; wangshu's own functional tests
# (make all) are unaffected because .pineapple/ is a dependency only of the
# independent benchmarks/pineapple/ submodule.
set -euo pipefail

PINEAPPLE_REPO="${PINEAPPLE_REPO:-https://github.com/Liam0205/pineapple.git}"
PINEAPPLE_BRANCH="${PINEAPPLE_BRANCH:-master}"

# The script's parent directory = benchmarks/pineapple/; clone target is .pineapple/
script_dir="$(cd "$(dirname "$0")" && pwd)"
mod_dir="$(cd "$script_dir/.." && pwd)"
target="$mod_dir/.pineapple"

if [ -d "$target/.git" ]; then
    echo "[fetch-pineapple] $target 已存在,更新 $PINEAPPLE_BRANCH..."
    git -C "$target" fetch --depth=1 origin "$PINEAPPLE_BRANCH"
    git -C "$target" reset --hard "origin/$PINEAPPLE_BRANCH"
else
    echo "[fetch-pineapple] 首次 clone $PINEAPPLE_REPO 到 $target..."
    git clone --depth=1 --branch "$PINEAPPLE_BRANCH" "$PINEAPPLE_REPO" "$target"
fi

# Report the currently pinned commit (helps debug number drift)
head_sha=$(git -C "$target" rev-parse HEAD)
head_msg=$(git -C "$target" log -1 --format='%s')
echo "[fetch-pineapple] HEAD = $head_sha"
echo "[fetch-pineapple] msg  = $head_msg"
