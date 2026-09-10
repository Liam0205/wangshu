#!/usr/bin/env bash
# cover.sh <profile.out> [go test flags...]: run the whole module's tests
# with coverage and write ONE merged profile.
#
# Two invocations, not one, because -coverpkg is all-or-nothing per
# `go test` call and the two halves of the module need different
# instrumentation:
#
#   - internal/... and the root package: plain -cover, i.e. each package
#     instrumented for its own unit tests. Exactly the pre-migration
#     behaviour for those packages.
#   - test/...: external test packages with no statements of their own.
#     Plain -cover credits them with "[no statements]" and DISCARDS every
#     root-package statement they execute (test/regression ran 12s of
#     interpreter work and reported nothing). -coverpkg=<root> attributes
#     those runs to the root package, whose behavioural suites live under
#     test/ since the root-test migration.
#
# Why not -coverpkg=./... in a single call: that would also instrument
# internal/crescent (the interpreter hot loop) for the heavy behavioural
# suites. Measured locally: test/regression 12s -> 400s (one test 3.9s ->
# 195s, 33x) before -race, so the CI job's 45-minute budget cannot hold it.
#
# Profiles in the legacy text format are a mode header plus per-block
# lines; the two halves instrument disjoint package sets, so concatenating
# the bodies under one header is a valid merge (no double-counted blocks).
set -euo pipefail

out=${1:?usage: cover.sh <profile.out> [go test flags...]}
shift
root=$(go list -m)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# shellcheck disable=SC2046 # go list output is one import path per line, no spaces
go test "$@" -covermode=atomic -coverprofile="$tmp/unit.out" \
    $(go list ./... | grep -v "^$root/test/")
go test "$@" -covermode=atomic -coverpkg="$root" -coverprofile="$tmp/behav.out" \
    ./test/...

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
