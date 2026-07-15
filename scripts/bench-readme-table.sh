#!/usr/bin/env bash
# bench-readme-table.sh [--format-only <logdir>] [--outdir <logdir>] [--count N] [--benchtime T]
#
# One-shot reproduction of the README performance table: run the benchmarks
# under all three builds, collect the raw logs, and format them with
# scripts/bench_readme_table.py into a Markdown table ready to paste into
# the README.
#
# Each of the three builds runs once (P1 default build / P3 gibbous-wasm /
# P4 gibbous-jit); the selected benchmarks match the README "reproduction
# commands" section. Each build runs `-count` times and takes the median.
#
# Shared-machine discipline (see llmdoc feedback_shared_machine_resource_limits):
#   fixed `-cpu=1`, serial runs, one core at a time, never saturate the box.
#
# Usage:
#   ./scripts/bench-readme-table.sh                 # full run + table (~20-30 min)
#   ./scripts/bench-readme-table.sh --outdir /tmp/bl  # logs into a given dir
#   ./scripts/bench-readme-table.sh --format-only /tmp/bl  # only reformat existing logs
#   ./scripts/bench-readme-table.sh --count 5       # 5 runs per build, median
#   ./scripts/bench-readme-table.sh --benchtime 1s  # per-benchmark time (default 2s)
#
# Cross-platform: goos/goarch auto-detected; run the same script on an arm64
# machine to fill in the arm64 table.
#
# Compatibility matches run-test-bins.sh: avoid GNU-only features so stock
# macOS (BSD tools) runs it directly.
set -uo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
bench_mod="$repo_root/benchmarks"
formatter="$repo_root/scripts/bench_readme_table.py"

count=3
benchtime=2s
outdir=""
format_only=""

while [ $# -gt 0 ]; do
    case "$1" in
        --format-only) format_only="${2:-}"; outdir="${2:-}"; shift 2 ;;
        --outdir)      outdir="${2:-}"; shift 2 ;;
        --count)       count="${2:-}"; shift 2 ;;
        --benchtime)   benchtime="${2:-}"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)
            echo "✗ unknown arg: $1" >&2; exit 2 ;;
    esac
done

# DIRS/FLAGS stay in sync with the README reproduction-commands section.
dirs="./baseline/ ./heavy/ ./realworld/ ./embedded/"

# P1 (default build): crescent interpreter + gopher baseline (GopherKernel
# is the same-shape denominator for the baseline P3 columns, issue #93).
p1_bench='_(Wangshu|WangshuCall|WangshuCallInto|Gopher|GopherKernel)$'
# P3 (gibbous-wasm): auto uses _WangshuKernel/_GibbousAuto*, force uses _Gibbous*.
p3_bench='_(Gibbous|GibbousCall|GibbousCallInto|GibbousAuto|GibbousAutoCall|GibbousAutoCallInto|WangshuKernel)$'
# P4 (gibbous-jit): auto uses _GibbousJITAuto*, force uses _GibbousJIT*.
p4_bench='_(GibbousJIT|GibbousJITCall|GibbousJITCallInto|GibbousJITAuto|GibbousJITAutoCall|GibbousJITAutoCallInto)$'
# P4 baseline extra pass: Simple/Arith/Loop have no separate kernel bench
# under the P4 build; use `_Wangshu` directly (the top-level chunk never
# promotes, so auto == force; see the formatter header comment).
p4_baseline_bench='(Simple|Arith|Loop)_Wangshu$'

run_bench() {  # run_bench <tags> <bench-regex> <logfile> [append]
    local tags="$1" bench="$2" logf="$3" append="${4:-}"
    # Check tagflag for emptiness before expanding: stock macOS bash 3.2
    # reports unbound variable when expanding an empty array under `set -u`
    # (fixed only in bash 4.4), and the P1 build's empty tags hits exactly
    # that -- follow run-test-bins.sh's convention of guarding with
    # `${#arr[@]}`.
    local tagflag=()
    [ -n "$tags" ] && tagflag=(-tags "$tags")
    echo "→ go test ${tags:+-tags \"$tags\" }-bench='$bench' -count=$count -cpu=1 ..." >&2
    local redir='>'
    [ -n "$append" ] && redir='>>'
    # Keep only Benchmark lines (PASS/ok/compile output is useless to the formatter).
    if [ "${#tagflag[@]}" -gt 0 ]; then
        _run_go "$redir" "$logf" "${tagflag[@]}" -bench="$bench"
    else
        _run_go "$redir" "$logf" -bench="$bench"
    fi
}

# _run_go <redir> <logfile> <go-test-args...>: run go test in the bench
# submodule, filter Benchmark lines, write the log per redir (> / >>).
# Factored out to avoid empty-array expansion.
_run_go() {
    local redir="$1" logf="$2"; shift 2
    if [ "$redir" = '>>' ]; then
        ( cd "$bench_mod" && go test "$@" \
            -run='^$' -benchtime="$benchtime" -count="$count" -cpu=1 $dirs ) \
            2>&1 | grep -E '^Benchmark' >> "$logf"
    else
        ( cd "$bench_mod" && go test "$@" \
            -run='^$' -benchtime="$benchtime" -count="$count" -cpu=1 $dirs ) \
            2>&1 | grep -E '^Benchmark' > "$logf"
    fi
}

if [ -z "$format_only" ]; then
    if [ -z "$outdir" ]; then
        outdir="$(mktemp -d "${TMPDIR:-/tmp}/wangshu-bench.XXXXXX")"
    fi
    mkdir -p "$outdir"
    echo "logs -> $outdir" >&2
    run_bench ""                            "$p1_bench"          "$outdir/p1.log"
    run_bench "wangshu_p3 wangshu_profile"  "$p3_bench"          "$outdir/p3.log"
    run_bench "wangshu_p4 wangshu_profile"  "$p4_bench"          "$outdir/p4.log"
    run_bench "wangshu_p4 wangshu_profile"  "$p4_baseline_bench" "$outdir/p4.log" append
fi

if [ ! -f "$outdir/p1.log" ]; then
    echo "✗ no logs under $outdir — run without --format-only first" >&2
    exit 1
fi

# Header line: platform / CPU / toolchain / parameters (the README header
# can copy this line verbatim).
goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
gover="$(go version | awk '{print $3}')"
cpu="$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/.*: //')"
[ -z "$cpu" ] && cpu="$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo '?')"
today="$(date +%F)"
echo ""
echo "<!-- $goos/$goarch, $cpu, $gover, -benchtime=$benchtime -count=$count -cpu=1, 取 median, measured $today -->"
echo ""
python3 "$formatter" "$outdir"
