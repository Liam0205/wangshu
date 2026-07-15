#!/usr/bin/env bash
# check-oracle.sh: verify the official Lua 5.1.5 oracle is available
# (12 §2.6 / engineering.md §4). No oracle always fails (same bar locally
# and in CI; difftest itself skips without an oracle, but explicitly running
# this script demands the oracle be present).
set -euo pipefail

if ! command -v lua5.1 >/dev/null 2>&1; then
    echo "✗ lua5.1 未安装(差分 oracle 不可用)" >&2
    echo "  Ubuntu/Debian: sudo apt-get install -y lua5.1" >&2
    exit 1
fi

v=$(lua5.1 -v 2>&1)
case "$v" in
    *"5.1.5"*) echo "oracle ok: $v" ;;
    *)
        echo "✗ expected Lua 5.1.5, got: $v" >&2
        echo "  oracle 版本错会让差分结论失效,不静默降级(engineering.md §4)" >&2
        exit 1
        ;;
esac
