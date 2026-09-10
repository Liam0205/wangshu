#!/usr/bin/env bash
# cover.sh <profile.out> [go test flags...]: run the whole module's tests
# with coverage and write ONE merged profile.
#
# Two invocations, not one, because -coverpkg is all-or-nothing per
# `go test` call and the two halves of the module need different
# instrumentation:
#
#   - internal/...: plain -cover, i.e. each package instrumented for its
#     own unit tests. Exactly the pre-migration behaviour for those
#     packages.
#   - the root package and test/...: the behavioural suites under test/
#     are mostly external test packages with no statements of their own.
#     Plain -cover credits them with "[no statements]" and DISCARDS every
#     root-package statement they execute (test/regression ran 12s of
#     interpreter work and reported nothing). -coverpkg=<root,...>
#     attributes those runs to the root package, whose behavioural suites
#     live under test/ since the root-test migration. The root package
#     itself rides along so any unit test it grows later is instrumented
#     the same way. test/ packages that DO ship non-test Go files
#     (test/difftest's generator.go today) are added to -coverpkg as
#     well, so their own statements keep the coverage they had under a
#     plain `go test -cover ./...`.
#
# Why not -coverpkg=./... in a single call: that would also instrument
# internal/crescent (the interpreter hot loop) for the heavy behavioural
# suites. Measured locally: test/regression 12s -> 400s (one test 3.9s ->
# 195s, 33x) before -race, so the CI job's 45-minute budget cannot hold it.
#
# The build tags MUST reach `go list` as well as `go test`: packages whose
# every file carries a build constraint (internal/gibbous/wasm under
# wangshu_p3, internal/gibbous/jit/{amd64,arm64,peroptranslator} under
# wangshu_p4) are invisible to an untagged `go list ./...`, and `go test`
# would then silently never run them (review finding).
#
# Profiles in the legacy text format are a mode header plus per-block
# lines. The first half instruments only internal/... and the second half
# only the root package plus test/ packages with their own Go files, so
# no block position appears in both files; within the second half every
# test package writes its own copy of the -coverpkg blocks, which
# `go tool cover` merges by summing counts (the normal semantics of a
# multi-package -coverpkg profile).
set -euo pipefail

out=${1:?usage: cover.sh <profile.out> [go test flags...]}
shift
root=$(go list -m)

# Mirror -tags / -tags=... (and the equivalent --tags forms Go's flag
# parser accepts) from the go test flags into go list.
tags=""
prev=""
for arg in "$@"; do
    case "$arg" in
        -tags=* | --tags=*) tags=${arg#*=} ;;
        *) if [ "$prev" = "-tags" ] || [ "$prev" = "--tags" ]; then tags=$arg; fi ;;
    esac
    prev=$arg
done
list_args=()
if [ -n "$tags" ]; then
    list_args=(-tags "$tags")
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

unit_pkgs=$(go list "${list_args[@]+"${list_args[@]}"}" ./... | grep -v -e "^$root/test/" -e "^$root\$")
# test/ packages with non-test Go files of their own join the -coverpkg
# set, so a plain -cover run and this script credit them identically.
behav_pkgs=$(go list "${list_args[@]+"${list_args[@]}"}" -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./test/... | paste -sd, -)
coverpkg=$root${behav_pkgs:+,$behav_pkgs}

# shellcheck disable=SC2086 # unit_pkgs is one import path per line, no spaces
go test "$@" -covermode=atomic -coverprofile="$tmp/unit.out" $unit_pkgs
go test "$@" -covermode=atomic -coverpkg="$coverpkg" -coverprofile="$tmp/behav.out" \
    . ./test/...

mode=$(head -n 1 "$tmp/unit.out")
if [ "$mode" != "$(head -n 1 "$tmp/behav.out")" ]; then
    echo "cover.sh: profile mode mismatch: '$mode' vs '$(head -n 1 "$tmp/behav.out")'" >&2
    exit 1
fi
{
    printf '%s\n' "$mode"
    tail -n +2 "$tmp/unit.out"
    tail -n +2 "$tmp/behav.out"
} > "$out"
