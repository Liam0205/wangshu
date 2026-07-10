#!/usr/bin/env bash
# bench-readme-table.sh [--format-only <logdir>] [--outdir <logdir>] [--count N] [--benchtime T]
#
# 一键复现 README「性能指标」表格:跑三档 build 的 benchmark、收集原始日志、
# 用 scripts/bench_readme_table.py 整理成可直接贴进 README 的 Markdown 表格。
#
# 三档 build 各跑一次(P1 默认 build / P3 gibbous-wasm / P4 gibbous-jit),
# 选取的 benchmark 与 README「复现命令」小节一致。每档 `-count` 次取 median。
#
# 共享机纪律(见 llmdoc feedback_shared_machine_resource_limits):
#   固定 `-cpu=1` 串行跑,一次只占一个核,不打满机器。
#
# 用法:
#   ./scripts/bench-readme-table.sh                 # 全跑 + 出表(约 20-30 min)
#   ./scripts/bench-readme-table.sh --outdir /tmp/bl  # 日志落在指定目录
#   ./scripts/bench-readme-table.sh --format-only /tmp/bl  # 只重排已有日志
#   ./scripts/bench-readme-table.sh --count 5       # 每档跑 5 次取 median
#   ./scripts/bench-readme-table.sh --benchtime 1s  # 每 benchmark 时长(默认 2s)
#
# 跨平台:goos/goarch 自动探测,arm64 机器上直接跑同一脚本即可补 arm64 表。
#
# 兼容性同 run-test-bins.sh:避用 GNU-only 特性,macOS(BSD)可直接跑。
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

# DIRS/FLAGS 与 README 复现命令小节保持一致。
dirs="./baseline/ ./heavy/ ./realworld/ ./embedded/"

# P1 (default build): crescent interpreter + gopher baseline (GopherKernel
# is the same-shape denominator for the baseline P3 columns, issue #93).
p1_bench='_(Wangshu|WangshuCall|WangshuCallInto|Gopher|GopherKernel)$'
# P3(gibbous-wasm):auto 走 _WangshuKernel/_GibbousAuto*,force 走 _Gibbous*。
p3_bench='_(Gibbous|GibbousCall|GibbousCallInto|GibbousAuto|GibbousAutoCall|GibbousAutoCallInto|WangshuKernel)$'
# P4(gibbous-jit):auto 走 _GibbousJITAuto*,force 走 _GibbousJIT*。
p4_bench='_(GibbousJIT|GibbousJITCall|GibbousJITCallInto|GibbousJITAuto|GibbousJITAutoCall|GibbousJITAutoCallInto)$'
# P4 baseline 补跑:Simple/Arith/Loop 在 P4 build 下无独立 kernel bench,
# 直接用 `_Wangshu`(顶层 chunk 不升层,auto == force,见 formatter 头注释)。
p4_baseline_bench='(Simple|Arith|Loop)_Wangshu$'

run_bench() {  # run_bench <tags> <bench-regex> <logfile> [append]
    local tags="$1" bench="$2" logf="$3" append="${4:-}"
    # tagflag 判空后再展开:macOS 自带 bash 3.2 在 `set -u` 下展开空数组会
    # 报 unbound variable(bash 4.4 才修),P1 档 tags 为空正好命中——按
    # run-test-bins.sh 的约定用 `${#arr[@]}` 判空规避。
    local tagflag=()
    [ -n "$tags" ] && tagflag=(-tags "$tags")
    echo "→ go test ${tags:+-tags \"$tags\" }-bench='$bench' -count=$count -cpu=1 ..." >&2
    local redir='>'
    [ -n "$append" ] && redir='>>'
    # 只保留 Benchmark 行(其余 PASS/ok/编译输出对 formatter 无用)。
    if [ "${#tagflag[@]}" -gt 0 ]; then
        _run_go "$redir" "$logf" "${tagflag[@]}" -bench="$bench"
    else
        _run_go "$redir" "$logf" -bench="$bench"
    fi
}

# _run_go <redir> <logfile> <go-test-args...>:在 bench 子模块里跑 go test,
# 过滤出 Benchmark 行,按 redir(> / >>)写日志。抽出来避免空数组展开。
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

# 表头一行:平台 / CPU / toolchain / 参数(README 表头照抄这行即可)。
goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
gover="$(go version | awk '{print $3}')"
cpu="$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | sed 's/.*: //')"
[ -z "$cpu" ] && cpu="$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo '?')"
today="$(date +%F)"
echo ""
echo "<!-- $goos/$goarch, $cpu, $gover, -benchtime=$benchtime -count=$count -cpu=1, 取 median, measured $today -->"
echo ""
python3 "$formatter" "$outdir"
