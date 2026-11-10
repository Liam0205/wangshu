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

# '%' must not be FMA-contracted on x86-64 (#309).
#
# Lua 5.1 computes a % b as a - floor(a/b)*b. A build whose compiler fused the multiply and the subtract
# gives a different answer whenever the product is not exactly representable, and wangshu on amd64 is
# compared against the rounded-product reading. The right version string says nothing about this: on
# 2026-11-08 the nightly runner image started producing a fused 5.1.5 and every round reported dozens
# of divergences across all three tiers, filed as product bugs. Checking here turns that into one
# environment failure that names its cause.
#
# Three probes, one per place the reference implementation evaluates '%': constant folding (lcode.c),
# OP_MOD on run-time numbers, and the string-coercing slow path (both lvm.c).
#
# Not enforced off x86-64: arm64 builds fuse by default and so does wangshu there (#323), so the value
# is only reported.
probe='local a, b = 84572069678023671808, 89
io.write(string.format("%.17g %.17g %.17g", 84572069678023671808 % 89, a % b, ("84572069678023671808") % b))'
want="0 0 0"
got=$(lua5.1 -e "$probe" 2>&1) || true
arch=$(uname -m)
case "$arch" in
    x86_64|amd64)
        if [ "$got" != "$want" ]; then
            echo "✗ lua5.1 的 % 结果不对：期望 \"$want\"，实际 \"$got\"" >&2
            echo "  84572069678023671808 % 89 应为 0。得到 -2944，说明这个 lua5.1 编译时把 a - floor(a/b)*b" >&2
            echo "  里的乘法和减法合成了一条 FMA 指令。用 scripts/build-lua-oracle.sh 重新构建" >&2
            echo "  （x86-64 上带 -ffp-contract=off）。" >&2
            exit 1
        fi
        echo "oracle % ok: product rounded (not FMA-contracted)"
        ;;
    *)
        echo "oracle % probe on $arch (not enforced, see #323): $got"
        ;;
esac
