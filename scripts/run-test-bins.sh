#!/usr/bin/env bash
# run-test-bins.sh <mode> [variant...]
#
# 跑 test-bin/<variant>/*.test 里的预编译 binary,按 mode 切两种用法:
#   test   跑所有测试函数(-test.v 默认开,不开 -test.bench)
#   bench  只跑 benchmark 函数(-test.run=^$ -test.bench=.)
#
# variant 缺省 = test-bin/ 下所有目录;指定时只跑给出的几个(如 `p1`)。
# 默认枚举顺序按目录字典序(p1 → p3 → p4),稳定可读。
#
# 跑前若 binary 不存在,提示先 `make build-all`。
#
# 兼容性(issue #15 review):脚本避用 GNU `find -printf` / bash 4
# `mapfile` / `declare -A`,可在 macOS(BSD find + bash 3.2)直接跑。
set -uo pipefail

mode="${1:-}"
shift || true

case "$mode" in
    test|bench) ;;
    "")
        echo "usage: $0 <test|bench> [variant...]" >&2
        exit 2 ;;
    *)
        echo "✗ unknown mode: $mode (allowed: test | bench)" >&2
        exit 2 ;;
esac

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
bindir="$repo_root/test-bin"

if [ ! -d "$bindir" ]; then
    echo "✗ no test-bin/ — run \`make build-all\` first" >&2
    exit 1
fi

# 收集要跑的 variants
variants=()
if [ $# -gt 0 ]; then
    variants=("$@")
else
    # 替代 `find -printf '%f\n'`:用 glob + basename。glob 命中目录为 0 时
    # `nullglob` 让 `for` 不进入循环,空 variants 由下面的 check 兜住。
    shopt -s nullglob
    for d in "$bindir"/*/; do
        variants+=("$(basename "$d")")
    done
    shopt -u nullglob
    # 按字典序稳定排:p1 → p3 → p4
    if [ "${#variants[@]}" -gt 0 ]; then
        IFS=$'\n' variants=($(printf '%s\n' "${variants[@]}" | sort))
        unset IFS
    fi
fi

if [ "${#variants[@]}" -eq 0 ]; then
    echo "✗ no variants under test-bin/ — run \`make build-all\` first" >&2
    exit 1
fi

overall_rc=0

# manifest_lookup <binary basename>:从已加载的 names[]/dirs[] 平行数组里
# 找对应源码 dir,找不到回空。替代 bash 4 `declare -A`(macOS 默认 bash 3.2
# 不支持 associative array,issue #15 review)。
manifest_lookup() {
    local needle=$1
    local i
    for i in "${!_man_names[@]}"; do
        if [ "${_man_names[$i]}" = "$needle" ]; then
            echo "${_man_dirs[$i]}"
            return
        fi
    done
}

for v in "${variants[@]}"; do
    vdir="$bindir/$v"
    if [ ! -d "$vdir" ]; then
        echo "✗ variant '$v' not built: $vdir 不存在" >&2
        overall_rc=1
        continue
    fi

    # 替代 `mapfile -t bins < <(find ... | sort)`:循环读 + glob
    bins=()
    shopt -s nullglob
    for f in "$vdir"/*.test; do
        [ -f "$f" ] && bins+=("$f")
    done
    shopt -u nullglob
    if [ "${#bins[@]}" -gt 0 ]; then
        IFS=$'\n' bins=($(printf '%s\n' "${bins[@]}" | sort))
        unset IFS
    fi

    if [ "${#bins[@]}" -eq 0 ]; then
        echo "✗ variant '$v' 下没有 .test binary" >&2
        overall_rc=1
        continue
    fi

    # 读 manifest:`<basename>.test <绝对源码目录>` 一行一项。
    # 预编译 binary 跑时 cwd 是调用方,而非 `go test` 自动 cd 到的包目录,
    # `testdata/*.lua` 类相对路径需要 cd 兜住。
    # 用平行数组 _man_names[] / _man_dirs[](bash 3.2 无 associative array)
    _man_names=()
    _man_dirs=()
    if [ -f "$vdir/manifest.txt" ]; then
        while IFS=' ' read -r bname bdir; do
            [ -z "$bname" ] && continue
            _man_names+=("$bname")
            _man_dirs+=("$bdir")
        done < "$vdir/manifest.txt"
    fi

    echo ""
    echo "════════════════════════════════════════════════════════════"
    echo "  $mode :: variant=$v  (${#bins[@]} binaries)"
    echo "════════════════════════════════════════════════════════════"

    for bin in "${bins[@]}"; do
        name=$(basename "$bin")
        sdir=$(manifest_lookup "$name")
        echo ""
        echo "→ $v/$name${sdir:+  (cwd=$sdir)}"
        case "$mode" in
            test)
                # -test.v 给 noisy 但便于人看;-test.timeout 防 fuzz/long 卡死
                if [ -n "$sdir" ]; then
                    if ! (cd "$sdir" && "$bin" -test.v -test.timeout=600s); then
                        echo "✗ $v/$name failed" >&2
                        overall_rc=1
                    fi
                else
                    if ! "$bin" -test.v -test.timeout=600s; then
                        echo "✗ $v/$name failed" >&2
                        overall_rc=1
                    fi
                fi
                ;;
            bench)
                # benchmark only;非 bench binary(无 Benchmark*)会瞬时退出
                if [ -n "$sdir" ]; then
                    if ! (cd "$sdir" && "$bin" -test.run='^$' -test.bench='.' -test.benchmem -test.count=1); then
                        echo "✗ $v/$name failed" >&2
                        overall_rc=1
                    fi
                else
                    if ! "$bin" -test.run='^$' -test.bench='.' -test.benchmem -test.count=1; then
                        echo "✗ $v/$name failed" >&2
                        overall_rc=1
                    fi
                fi
                ;;
        esac
    done
done

echo ""
if [ "$overall_rc" -ne 0 ]; then
    echo "✗ run-test-bins: 某些 binary 失败,见上"
else
    echo "✓ run-test-bins: all $mode runs passed"
fi
exit "$overall_rc"
