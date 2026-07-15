#!/usr/bin/env bash
# run-test-bins.sh <mode> [variant...]
#
# Run the precompiled binaries under test-bin/<variant>/*.test, in one of
# two modes:
#   test   run all test functions (-test.v on by default, no -test.bench)
#   bench  run only benchmark functions (-test.run=^$ -test.bench=.)
#
# variant defaults to every directory under test-bin/; when given, only the
# listed ones run (e.g. `p1`). Default enumeration is directory-lexicographic
# (p1 -> p3 -> p4), stable and readable.
#
# If a binary is missing, the script suggests `make build-all` first.
#
# Compatibility (issue #15 review): the script avoids GNU `find -printf` /
# bash 4 `mapfile` / `declare -A`, so it runs directly on macOS (BSD find +
# bash 3.2).
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

# Collect the variants to run
variants=()
if [ $# -gt 0 ]; then
    variants=("$@")
else
    # Replacement for `find -printf '%f\n'`: glob + basename. With zero
    # matching directories, `nullglob` keeps the `for` from iterating;
    # empty variants are caught by the check below.
    shopt -s nullglob
    for d in "$bindir"/*/; do
        variants+=("$(basename "$d")")
    done
    shopt -u nullglob
    # Stable lexicographic sort: p1 -> p3 -> p4
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

# manifest_lookup <binary basename>: look up the source dir in the loaded
# names[]/dirs[] parallel arrays; empty when not found. Replacement for
# bash 4 `declare -A` (default macOS bash 3.2 has no associative arrays,
# issue #15 review).
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

    # Replacement for `mapfile -t bins < <(find ... | sort)`: read loop + glob
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

    # Read the manifest: one `<basename>.test <absolute source dir>` entry
    # per line. A precompiled binary runs with the caller's cwd rather than
    # the package dir `go test` auto-cds into, so relative paths like
    # `testdata/*.lua` need a cd. Parallel arrays _man_names[] /
    # _man_dirs[] (bash 3.2 has no associative arrays)
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
                # -test.v is noisy but human-friendly; -test.timeout guards against fuzz/long hangs
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
                # benchmark only; non-bench binaries (no Benchmark*) exit instantly
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
