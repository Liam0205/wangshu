#!/usr/bin/env bash
# check-oracle.sh:校验官方 Lua 5.1.5 oracle 可用(12 §2.6 / engineering.md §4)。
# 无 oracle 一律 fail(本地与 CI 同标准;difftest 自身对无 oracle 会 skip,
# 但显式跑本脚本即要求 oracle 在位)。
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
