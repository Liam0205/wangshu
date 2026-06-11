#!/usr/bin/env bash
# check-oracle.sh:校验官方 Lua 5.1.5 oracle 可用(12 §2.6 / engineering.md §4)。
# CI 必过;本地无 lua5.1 给安装指引(本地可跳,CI 必过)。
set -euo pipefail

if ! command -v lua5.1 >/dev/null 2>&1; then
    echo "✗ lua5.1 未安装(差分 oracle 不可用)" >&2
    echo "  Ubuntu/Debian: sudo apt-get install -y lua5.1" >&2
    if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ]; then
        exit 1   # CI 必须有 oracle
    fi
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
