#!/usr/bin/env bash
# build-test-bins.sh <variant>
#
# 把所有含 test 的包编译为 `.test` binary,落到 test-bin/<variant>/。
# 配合 run-test-bins.sh 把 build 与 run 分阶段——`go test -c` 编一次,
# 后续 `make test` / `make bench` 跑同一份 binary,避免每次重编。
#
# variant:
#   p1  默认 build(新月解释器,P3/P4 完全 dead-code)
#   p3  wangshu_p3 + wangshu_profile build(P1 解释器 + P3 凸月 wasm 编译层)
#   p4  wangshu_p4 build(P1 解释器 + P4 凸月 jit 编译层;PJ0 阶段:supported 全 false ⇒ 行为等价 P1)
#   future: p5 同款接入。
#
# **P3+P4 互斥 build tag**(用户裁决,docs/design/p4-method-jit/06-backends.md §1):
# wangshu_p3 与 wangshu_p4 不允许同时启用——本脚本拒此组合。
#
# 主模块 + benchmarks 子模块都编(子模块独立 go.mod)。包名经
# `path-to-name` 规范化:`github.com/Liam0205/wangshu/internal/arena` →
# `internal-arena.test`,root 包(`github.com/Liam0205/wangshu`)→ `root.test`,
# 子模块包前缀化为 `bench-`。
set -uo pipefail

variant="${1:-}"
case "$variant" in
    p1)
        tags=""
        ;;
    p3)
        tags="wangshu_p3 wangshu_profile"
        ;;
    p4)
        tags="wangshu_p4 wangshu_profile"
        ;;
    "")
        echo "usage: $0 <variant>  (variant: p1 | p3 | p4)" >&2
        exit 2
        ;;
    *)
        echo "✗ unknown variant: $variant (allowed: p1 | p3 | p4)" >&2
        exit 2
        ;;
esac

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
outdir="$repo_root/test-bin/$variant"
mkdir -p "$outdir"

# pkg_to_name <full import path>
# 主模块 root(`github.com/Liam0205/wangshu`)→ `root`
# 主模块子包(`...wangshu/internal/arena`)→ `internal-arena`
# benchmarks 子模块(`...wangshu/benchmarks/baseline`)→ `bench-baseline`
# 前缀化是隔离手段——避免未来主模块新增顶层包与子模块包名冲突,
# `.test` 在 test-bin/<variant>/ 平铺时互相覆盖(issue #15 review)。
pkg_to_name() {
    local p=$1
    p=${p#github.com/Liam0205/wangshu}
    p=${p#/}
    [ -z "$p" ] && { echo "root"; return; }
    # benchmarks/baseline → bench-baseline(显式前缀,兑现注释承诺)
    if [[ "$p" == benchmarks/* ]]; then
        p=${p#benchmarks/}
        echo "bench-${p//\//-}"
        return
    fi
    echo "${p//\//-}"
}

# build_pkg <full import path> <module_root_dir>
# 写入 binary + 把它的源码 dir 记到 manifest.txt(供 run 阶段 cd 进去,
# 因为预编译 binary 跑时 cwd 是调用方,不是 `go test` 默认那样自动 cd 到
# 包目录——`testdata/` 等相对路径需要在跑前手工 cd 兜住)。
build_pkg() {
    local pkg=$1
    local mod_root=$2
    local name
    name=$(pkg_to_name "$pkg")
    local out="$outdir/$name.test"

    local -a args=(-race -c -o "$out")
    if [ -n "$tags" ]; then
        args+=(-tags "$tags")
    fi

    # `go test -c` 在没 test 的包上会写空 binary 也不报错;为避免后续 run 阶段
    # 误以为是有效 binary 跑出 "no tests to run" 噪音,这里先按 .TestGoFiles +
    # .XTestGoFiles 过滤。调用方保证只传含 test 的包。
    if ! go test "${args[@]}" "$pkg"; then
        echo "✗ build failed: $pkg ($variant)" >&2
        return 1
    fi

    # 把包源码 dir(绝对路径)记到 manifest;run-test-bins.sh 据此 cd。
    # **传 -tags 给 go list**:build tag-only 包(如 internal/gibbous/wasm 在
    # wangshu_p3 build / internal/gibbous/jit 在 wangshu_p4 build)未传
    # tag 时 `go list` 会跳过解析,失败时报「no Go files」类错。
    local src_dir
    if [ -n "$tags" ]; then
        src_dir=$(go list -tags "$tags" -f '{{.Dir}}' "$pkg" 2>/dev/null)
    else
        src_dir=$(go list -f '{{.Dir}}' "$pkg" 2>/dev/null)
    fi
    if [ -z "$src_dir" ]; then
        echo "✗ go list -f {{.Dir}} failed for $pkg" >&2
        return 1
    fi
    echo "$name.test $src_dir" >> "$outdir/manifest.txt"

    echo "  $name.test  ←  $pkg"
}

# 第一阶段:主模块
echo "===== build $variant test binaries → test-bin/$variant/ ====="
rm -f "$outdir/manifest.txt"
echo "[1/2] main module"
# 替代 bash 4 `mapfile`:while read 配 process substitution(bash 3.2 兼容)
# **传 -tags 给 go list**:build tag-only 包(如 internal/gibbous/wasm 在
# wangshu_p3 build 下、internal/gibbous/jit 在 wangshu_p4 build 下)经此被
# 识别;否则 go list 默认 build 不会列出它们(测试漏跑)。
main_pkgs=()
list_args=(-f '{{if or (len .TestGoFiles) (len .XTestGoFiles)}}{{.ImportPath}}{{end}}')
if [ -n "$tags" ]; then
    list_args+=(-tags "$tags")
fi
while IFS= read -r pkg; do
    [ -z "$pkg" ] && continue
    main_pkgs+=("$pkg")
done < <(cd "$repo_root" && \
    go list "${list_args[@]}" ./... 2>/dev/null)
for pkg in "${main_pkgs[@]}"; do
    (cd "$repo_root" && build_pkg "$pkg" "$repo_root") || exit 1
done

# 第二阶段:benchmarks 子模块(独立 go.mod)
echo "[2/2] benchmarks submodule"
bench_pkgs=()
while IFS= read -r pkg; do
    [ -z "$pkg" ] && continue
    bench_pkgs+=("$pkg")
done < <(cd "$repo_root/benchmarks" && \
    go list "${list_args[@]}" ./... 2>/dev/null)
for pkg in "${bench_pkgs[@]}"; do
    (cd "$repo_root/benchmarks" && build_pkg "$pkg" "$repo_root/benchmarks") || exit 1
done

count=$(find "$outdir" -name '*.test' -type f | wc -l)
echo "===== done: $count binaries in $outdir ====="
