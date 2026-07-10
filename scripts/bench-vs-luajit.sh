#!/usr/bin/env bash
# bench-vs-luajit.sh [--outdir <dir>] [--count N] [--luajit path]
#
# 跑 wangshu(P1 / P4-auto / P4-force)与 LuaJIT 的同源脚本对比:四个引擎
# 档位执行同一份 benchmarks/vsluajit/bench.lua(自计时,os.clock),输出
# Markdown 对比表。列内核吞吐口径——对应 roadmap §0 终局目标的度量形式
# (「列内核 10-30x over gopher-lua,逼近 LuaJIT 档」)。
#
# 每档跑 -count 次(默认 3),按 kernel 取 median。checksum 行用于跨引擎
# 结果一致性校验:任何档位 checksum 不一致直接报错退出(防静默错果)。
#
# 共享机纪律(llmdoc feedback_shared_machine_resource_limits):四档串行,
# 一次只占一个核。
#
# 用法:
#   ./scripts/bench-vs-luajit.sh                  # 全跑 + 出表
#   ./scripts/bench-vs-luajit.sh --count 5        # 每档 5 次取 median
#   ./scripts/bench-vs-luajit.sh --luajit /opt/bin/luajit
#
# LuaJIT 不在 PATH 时跳过该列(表格照出,列留 n/a)。
# 兼容性同 run-test-bins.sh:避用 GNU-only 特性,macOS(BSD)可直接跑。
set -uo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
bench_mod="$repo_root/benchmarks"
bench_lua="$bench_mod/vsluajit/bench.lua"

count=3
outdir=""
luajit_bin="${LUAJIT:-luajit}"

while [ $# -gt 0 ]; do
    case "$1" in
        --outdir) outdir="${2:-}"; shift 2 ;;
        --count)  count="${2:-}"; shift 2 ;;
        --luajit) luajit_bin="${2:-}"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)
            echo "✗ unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [ -z "$outdir" ]; then
    outdir="$(mktemp -d "${TMPDIR:-/tmp}/wangshu-vsluajit.XXXXXX")"
fi
mkdir -p "$outdir"
echo "logs -> $outdir" >&2

# 构建两个 benchlua 二进制(P1 默认 build / P4 build)。
echo "→ building benchlua (P1 + P4)" >&2
( cd "$bench_mod" && go build -o "$outdir/benchlua-p1" ./cmd/benchlua ) || exit 1
( cd "$bench_mod" && go build -tags "wangshu_p4 wangshu_profile" \
    -o "$outdir/benchlua-p4" ./cmd/benchlua ) || exit 1

# run_engine <label> <logfile> <cmd...>:跑 count 轮,日志逐轮追加。
run_engine() {
    local label="$1" logf="$2"; shift 2
    : > "$logf"
    local i=1
    while [ "$i" -le "$count" ]; do
        echo "→ $label round $i/$count" >&2
        "$@" "$bench_lua" >> "$logf" || { echo "✗ $label failed" >&2; return 1; }
        i=$((i + 1))
    done
}

run_engine "P1"       "$outdir/p1.log"    "$outdir/benchlua-p1"          || exit 1
run_engine "P4-auto"  "$outdir/p4auto.log" "$outdir/benchlua-p4"         || exit 1
run_engine "P4-force" "$outdir/p4force.log" "$outdir/benchlua-p4" -force || exit 1

have_luajit=0
if command -v "$luajit_bin" >/dev/null 2>&1; then
    have_luajit=1
    "$luajit_bin" -v >&2 || true
    run_engine "LuaJIT" "$outdir/luajit.log" "$luajit_bin" || exit 1
else
    echo "! luajit not found ($luajit_bin) — LuaJIT column will be n/a" >&2
fi

# checksum 跨引擎一致性校验(每档取第一轮的 checksum 行)。
ck_ref="$(grep '^checksum' "$outdir/p1.log" | head -1 | cut -f2)"
for f in p4auto p4force luajit; do
    [ -f "$outdir/$f.log" ] || continue
    ck="$(grep '^checksum' "$outdir/$f.log" | head -1 | cut -f2)"
    if [ "$ck" != "$ck_ref" ]; then
        echo "✗ checksum divergence: p1=$ck_ref $f=$ck" >&2
        exit 1
    fi
done
echo "checksums agree across engines: $ck_ref" >&2

# 表头:平台 / CPU / toolchain(对齐 bench-readme-table.sh 格式)。
goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
gover="$(go version | awk '{print $3}')"
cpu="$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/.*: //')"
[ -z "$cpu" ] && cpu="$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo '?')"
ljver="n/a"
[ "$have_luajit" = 1 ] && ljver="$("$luajit_bin" -v 2>&1 | head -1)"
today="$(date +%F)"
echo ""
echo "<!-- $goos/$goarch, $cpu, $gover, luajit: $ljver, count=$count median, measured $today -->"
echo ""
python3 "$repo_root/scripts/bench_vs_luajit_table.py" "$outdir"
