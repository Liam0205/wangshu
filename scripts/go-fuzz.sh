#!/usr/bin/env bash
# go-fuzz.sh <fuzztime> [build_tags]:自动发现全部 func Fuzz* 目标,各跑一轮冒烟。
# engineering.md §1。
#
# build_tags(可选,默认空 = 默认 build):传给 go test -tags。典型用法:
#   ./scripts/go-fuzz.sh 30s                          # 默认 build(新月解释器)
#   ./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"  # 凸月 build(force-all 升层后路径)
set -euo pipefail

fuzztime="${1:-30s}"
tags="${2:-}"
found=0

tags_arg=()
if [ -n "$tags" ]; then
    tags_arg=(-tags "$tags")
fi

# run_target <pkg> <func>:跑一个 fuzz 目标,吞掉已知的 Go 工具链假失败。
#
# golang/go#75804(wangshu issue #63):-fuzztime 到点时,internal/fuzz 的
# coordinator 用 `err == fuzzCtx.Err()` 抑制 deadline 错误,但 context 取消
# 传播是「先关父 done channel、后 cancel 子 context」,存在一个窗口:
# ctx.Err() 已置而 fuzzCtx.Err() 仍是 nil,抑制失败,deadline 以
# `--- FAIL: FuzzX ... context deadline exceeded` 的形式逃逸成测试失败。
# 无 crash、无新反例语料,纯工具链竞态(上游修复 CL 774140 未合入)。
#
# 判定纪律(不掩盖真失败):
#   - 真 crasher 一定伴随 "Failing input written to testdata/fuzz/..."
#     (testing/fuzz.go 对 fuzzCrashError 的固定输出)——出现即真失败,立即报;
#   - 只有「失败输出含 deadline 字样且无 crasher 落盘」才判为 #75804 假失败,
#     重试一次;重试再挂(无论什么原因)都如实失败。
run_target() {
    local pkg="$1" func="$2" log rc
    log=$(mktemp)
    for attempt in 1 2; do
        set +e
        # GOMEMLIMIT (issue #123 diagnostic hardening): a nightly p4
        # worker died silently ("terminated unexpectedly: exit status 2",
        # no panic output) after 35min / 51M execs, and the written-out
        # input replays clean from every angle — the signature of
        # process-level resource exhaustion, not an input-determined bug.
        # A soft memory limit makes a memory-exhaustion death loud: the
        # Go runtime fails with an explicit out-of-memory fatal + stack
        # instead of being killed from outside. NOTE: the env var is
        # inherited by each of the -parallel=4 worker processes, so
        # 6GiB is a PER-WORKER cap, not a machine-wide one — effective
        # for the single-worker-blowup case (the likely one), best-
        # effort if all four grow evenly on a 7GB runner.
        GOMEMLIMIT="${GOMEMLIMIT:-6GiB}" \
        go test "${tags_arg[@]+"${tags_arg[@]}"}" "./$pkg" -run='^$' \
            -fuzz="^${func}\$" -fuzztime="$fuzztime" -timeout=120s -parallel=4 \
            2>&1 | tee "$log"
        rc=${PIPESTATUS[0]}
        set -e
        if [ "$rc" -eq 0 ]; then
            rm -f "$log"
            return 0
        fi
        # Diagnostic breadcrumbs for silent worker deaths (issue #123):
        # capture memory/map-count state at failure time into the log
        # stream so the uploaded artifact carries it.
        if grep -q 'hung or terminated unexpectedly' "$log"; then
            echo "── silent-worker-death diagnostics (issue #123) ──" >&2
            free -m >&2 2>/dev/null || true
            echo "vm.max_map_count: $(cat /proc/sys/vm/max_map_count 2>/dev/null || echo n/a)" >&2
        fi
        if [ "$attempt" -eq 1 ] \
           && grep -q 'context deadline exceeded' "$log" \
           && ! grep -q 'Failing input written to' "$log"; then
            echo "fuzz: $pkg :: $func hit golang/go#75804 (spurious deadline at fuzztime wrap-up, no crasher written) — retrying once" >&2
            continue
        fi
        rm -f "$log"
        return "$rc"
    done
}

while IFS=: read -r file line decl; do
    func=$(echo "$decl" | sed -E 's/^func (Fuzz[A-Za-z0-9_]*).*/\1/')
    pkg=$(dirname "$file")
    # Skip targets that are not buildable under the current tags:
    # tag-gated fuzz files (e.g. FuzzOracleDiff needs wangshu_oracle_cgo
    # + CGO_ENABLED=1) are invisible to `go test -list` in other
    # builds, and running -fuzz against an absent target burns a full
    # fuzztime slot on "no fuzz tests to fuzz". `-list` compiles the
    # test binary but runs nothing, so the probe is cheap.
    # Capture-then-grep, NOT `go test | grep -q`: under pipefail,
    # grep -q exits on first match and go test dies of SIGPIPE writing
    # the trailing "ok ..." line, failing the pipeline despite the
    # match. </dev/null keeps the probe from consuming the while-read
    # target list on stdin.
    listing=$(go test "${tags_arg[@]+"${tags_arg[@]}"}" "./$pkg" -run='^$' \
        -list "^${func}\$" </dev/null 2>/dev/null || true)
    if ! grep -q "^${func}\$" <<<"$listing"; then
        echo "fuzz: $pkg :: $func not buildable under tags=${tags:-default},skip"
        continue
    fi
    found=1
    echo "fuzz: $pkg :: $func ($fuzztime) tags=${tags:-default}"
    # `"${arr[@]+"${arr[@]}"}"` 是 set -u 下「空数组安全展开」惯用法——macOS
    # bash 3.2(Apple 不升级 GPLv3 包)在 set -u 下展开空数组 `${arr[@]}` 触发
    # unbound variable 致命错;`${arr[@]+...}` 仅在数组已设时展开,空时跳过。
    run_target "$pkg" "$func"
    # --exclude-dir 排除:
    #   - benchmarks:独立子模块,自家 fuzz 目标已通过 benchmarks/ 单独路径覆盖
    #   - benchmarks/pineapple/.pineapple:pineapple 仓临时 clone 落点,属另一 module,
    #     go test 跑会报 "main module does not contain package"
done < <(grep -rn --include='*_test.go' \
    --exclude-dir=benchmarks \
    --exclude-dir=.pineapple \
    -E '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . 2>/dev/null || true)

if [ "$found" -eq 0 ]; then
    echo "no fuzz targets found (func Fuzz*),skip"
fi
