#!/usr/bin/env bash
# build-test-bins.sh <variant>
#
# Compile every package containing tests into a `.test` binary under
# test-bin/<variant>/. Together with run-test-bins.sh this splits build from
# run -- `go test -c` compiles once, and subsequent `make test` / `make
# bench` invocations run the same binaries instead of recompiling each time.
#
# variant:
#   p1  default build (crescent interpreter, P3/P4 fully dead-code)
#   p3  wangshu_p3 + wangshu_profile build (P1 interpreter + P3 gibbous wasm tier)
#   p4  wangshu_p4 build (P1 interpreter + P4 gibbous jit tier; at the PJ0
#       stage supported is all-false => behaviorally equivalent to P1)
#   future: p5 plugs in the same way.
#
# **P3+P4 build tags are mutually exclusive** (user decision,
# docs/design/p4-method-jit/06-backends.md §1): wangshu_p3 and wangshu_p4
# must not be enabled together -- this script rejects that combination.
#
# Both the main module and the benchmarks submodule are compiled (the
# submodule has its own go.mod). Package names are normalized via
# `path-to-name`: `github.com/Liam0205/wangshu/internal/arena` ->
# `internal-arena.test`, the root package (`github.com/Liam0205/wangshu`) ->
# `root.test`, submodule packages get a `bench-` prefix.
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
# main module root (`github.com/Liam0205/wangshu`) -> `root`
# main module subpackage (`...wangshu/internal/arena`) -> `internal-arena`
# benchmarks submodule (`...wangshu/benchmarks/baseline`) -> `bench-baseline`
# The prefix is an isolation measure -- it prevents a future top-level
# package in the main module from colliding with a submodule package name
# and overwriting its `.test` in the flat test-bin/<variant>/ layout
# (issue #15 review).
pkg_to_name() {
    local p=$1
    p=${p#github.com/Liam0205/wangshu}
    p=${p#/}
    [ -z "$p" ] && { echo "root"; return; }
    # benchmarks/baseline -> bench-baseline (explicit prefix, honoring the comment above)
    if [[ "$p" == benchmarks/* ]]; then
        p=${p#benchmarks/}
        echo "bench-${p//\//-}"
        return
    fi
    echo "${p//\//-}"
}

# build_pkg <full import path> <module_root_dir>
# Write the binary + record its source dir in manifest.txt (the run stage
# cds into it: a precompiled binary runs with the caller's cwd, unlike
# `go test` which auto-cds into the package directory -- relative paths
# like `testdata/` need a manual cd before running).
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

    # `go test -c` on a test-less package writes an empty binary without
    # complaint; to keep the run stage from treating it as valid and
    # emitting "no tests to run" noise, packages are pre-filtered by
    # .TestGoFiles + .XTestGoFiles. Callers guarantee only test-bearing
    # packages are passed.
    if ! go test "${args[@]}" "$pkg"; then
        echo "✗ build failed: $pkg ($variant)" >&2
        return 1
    fi

    # Record the package source dir (absolute path) in the manifest;
    # run-test-bins.sh cds based on it.
    # **Pass -tags to go list**: for tag-only packages (e.g.
    # internal/gibbous/wasm under the wangshu_p3 build,
    # internal/gibbous/jit under wangshu_p4), `go list` without the tag
    # skips parsing and fails with "no Go files"-style errors.
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

# Stage 1: main module
echo "===== build $variant test binaries → test-bin/$variant/ ====="
rm -f "$outdir/manifest.txt"
echo "[1/2] main module"
# Replacement for bash 4 `mapfile`: while read with process substitution
# (bash 3.2 compatible)
# **Pass -tags to go list**: tag-only packages (e.g. internal/gibbous/wasm
# under the wangshu_p3 build, internal/gibbous/jit under wangshu_p4) are
# recognized this way; without the tags the default build's go list would
# omit them (tests silently skipped).
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

# Stage 2: benchmarks submodule (own go.mod)
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
