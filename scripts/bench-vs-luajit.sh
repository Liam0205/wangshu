#!/usr/bin/env bash
# bench-vs-luajit.sh [--outdir <dir>] [--count N] [--luajit path]
#
# Same-source comparison of wangshu (P1 / P4-auto / P4-force) against
# LuaJIT: four engine tiers execute the same benchmarks/vsluajit/bench.lua
# (self-timed via os.clock) and a Markdown comparison table is emitted.
# Columnar-kernel throughput basis -- the measurement form of the roadmap §0
# end goal ("columnar kernels 10-30x over gopher-lua, approaching the LuaJIT
# tier").
#
# Each tier runs -count times (default 3); the median is taken per kernel.
# The checksum lines cross-validate results across engines: any tier with a
# mismatched checksum aborts with an error (guards against silently wrong
# results).
#
# Shared-machine discipline (llmdoc feedback_shared_machine_resource_limits):
# the four tiers run serially, one core at a time.
#
# Usage:
#   ./scripts/bench-vs-luajit.sh                  # full run + table
#   ./scripts/bench-vs-luajit.sh --count 5        # 5 rounds per tier, median
#   ./scripts/bench-vs-luajit.sh --luajit /opt/bin/luajit
#
# When LuaJIT is not on PATH its column is skipped (the table still prints,
# column shows n/a).
# Compatibility matches run-test-bins.sh: avoid GNU-only features so stock
# macOS (BSD tools) runs it directly.
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
# Resolve to an absolute path: the go build below runs inside a
# `cd "$bench_mod"` subshell, so a relative --outdir would drop the
# binaries under benchmarks/<outdir>/ while the run_engine calls
# resolve it from the caller's cwd (bit the first CI run).
outdir="$(cd "$outdir" && pwd)"
echo "logs -> $outdir" >&2

# Build the two benchlua binaries (P1 default build / P4 build).
echo "→ building benchlua (P1 + P4)" >&2
( cd "$bench_mod" && go build -o "$outdir/benchlua-p1" ./cmd/benchlua ) || exit 1
( cd "$bench_mod" && go build -tags "wangshu_p4 wangshu_profile" \
    -o "$outdir/benchlua-p4" ./cmd/benchlua ) || exit 1

# run_engine <label> <logfile> <cmd...>: run count rounds, appending to the log each round.
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

# Cross-engine checksum consistency check (first round's checksum line per tier).
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

# Header: platform / CPU / toolchain (matches the bench-readme-table.sh format).
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
