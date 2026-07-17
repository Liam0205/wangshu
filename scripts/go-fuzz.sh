#!/usr/bin/env bash
# go-fuzz.sh <fuzztime> [build_tags] [target_regex]: auto-discover every
# func Fuzz* target and run one smoke round each. engineering.md §1.
#
# build_tags (optional, default empty = default build): passed to go test -tags.
# target_regex (optional, default all): only run targets whose function name
# matches this ERE. Build tags are additive -- e.g. under the
# wangshu_oracle_cgo build every default-build target also compiles, and an
# unfiltered sweep re-runs the whole set (the nightly p1 leg once blew past
# the 350m job timeout at 5x45m and got silently cancelled). Typical usage:
#   ./scripts/go-fuzz.sh 30s                          # default build (crescent interpreter)
#   ./scripts/go-fuzz.sh 30s "wangshu_p3 wangshu_profile"  # gibbous build (force-all promoted paths)
#   ./scripts/go-fuzz.sh 45m wangshu_oracle_cgo FuzzOracleDiff  # oracle diff only
set -euo pipefail

fuzztime="${1:-30s}"
tags="${2:-}"
target_regex="${3:-}"
found=0

tags_arg=()
if [ -n "$tags" ]; then
    tags_arg=(-tags "$tags")
fi

# run_target <pkg> <func>: run one fuzz target, swallowing a known Go
# toolchain spurious failure.
#
# golang/go#75804 (wangshu issue #63): when -fuzztime expires, the
# internal/fuzz coordinator suppresses the deadline error via
# `err == fuzzCtx.Err()`, but context cancellation propagates as "close the
# parent done channel first, cancel the child context later", leaving a
# window where ctx.Err() is set while fuzzCtx.Err() is still nil; the
# suppression misses and the deadline escapes as a test failure of the form
# `--- FAIL: FuzzX ... context deadline exceeded`. No crash, no new
# counterexample corpus -- a pure toolchain race (upstream fix CL 774140
# not yet merged).
#
# Adjudication discipline (never mask real failures):
#   - a real crasher always comes with "Failing input written to
#     testdata/fuzz/..." (testing/fuzz.go's fixed output for
#     fuzzCrashError) -- if present, it is a real failure, report at once;
#   - only "failure output mentions deadline AND no crasher was written" is
#     judged a #75804 spurious failure and retried once; if the retry fails
#     again (for any reason), fail honestly.
run_target() {
    local pkg="$1" func="$2" log rc
    log=$(mktemp)
    for attempt in 1 2; do
        set +e
        # GOMEMLIMIT (issue #123 diagnostic hardening, tightened after
        # issues #144/#145/#150/#151/#152): the fuzz coordinator starts
        # -parallel=N worker child processes that each inherit this env
        # var as a per-worker Go heap soft limit. When a worker's Go
        # heap hits the limit, the Go runtime raises an explicit OOM
        # fatal with a full goroutine stack instead of being killed
        # silently by the OS OOM killer.
        #
        # The concat-storm family of unreproducible crashers (#144-#152)
        # showed that 6GiB was too generous: a single seed doing
        # quadratic string concatenation (out = out .. cat(i)) can grow
        # Go-heap temporary strings to several GiB before the step
        # budget catches it — and under 4 parallel workers, four such
        # seeds blow past any reasonable runner memory. 512MiB is tight
        # enough to catch concat storms early (the arena is already
        # capped at 64MiB inside each harness; 512MiB leaves ~448MiB
        # for Go heap overhead, interpreter state, and temporary
        # strings — ample for any budget-bounded fuzz input) yet loose
        # enough to never false-positive on legitimate scripts.
        GOMEMLIMIT="${GOMEMLIMIT:-512MiB}" \
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
    if [ -n "$target_regex" ] && ! grep -qE "^${target_regex}\$" <<<"$func"; then
        continue
    fi
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
    #
    # A probe FAILURE (test package does not compile under these tags)
    # is fatal, not a skip: swallowing it lets a broken build tag or a
    # tag-specific compile error silently drop the target while other
    # targets keep the run green (PR review finding). "Compiles fine
    # but target absent" (rc=0, name missing) is the only skip case.
    probe_rc=0
    listing=$(go test "${tags_arg[@]+"${tags_arg[@]}"}" "./$pkg" -run='^$' \
        -list "^${func}\$" </dev/null 2>&1) || probe_rc=$?
    if [ "$probe_rc" -ne 0 ]; then
        if grep -q "matched no packages\|build constraints exclude all Go files" <<<"$listing"; then
            # The whole package vanishes under these tags (all files
            # tag-gated) — genuine not-buildable-here, skip.
            echo "fuzz: $pkg :: $func not buildable under tags=${tags:-default},skip"
            continue
        fi
        echo "fuzz: probe for $pkg :: $func FAILED under tags=${tags:-default} (compile error?):" >&2
        echo "$listing" >&2
        exit "$probe_rc"
    fi
    if ! grep -q "^${func}\$" <<<"$listing"; then
        echo "fuzz: $pkg :: $func not buildable under tags=${tags:-default},skip"
        continue
    fi
    found=1
    echo "fuzz: $pkg :: $func ($fuzztime) tags=${tags:-default}"
    # `"${arr[@]+"${arr[@]}"}"` is the empty-array-safe expansion idiom
    # under set -u -- macOS bash 3.2 (Apple does not upgrade GPLv3
    # packages) treats expanding an empty array `${arr[@]}` under set -u
    # as a fatal unbound-variable error; `${arr[@]+...}` expands only when
    # the array is set and skips when empty.
    run_target "$pkg" "$func"
    # --exclude-dir exclusions:
    #   - benchmarks: independent submodule; its own fuzz targets are
    #     covered via the separate benchmarks/ path
    #   - benchmarks/pineapple/.pineapple: scratch clone of the pineapple
    #     repo, a different module; go test there reports "main module
    #     does not contain package"
done < <(grep -rn --include='*_test.go' \
    --exclude-dir=benchmarks \
    --exclude-dir=.pineapple \
    -E '^func Fuzz[A-Za-z0-9_]*\(f \*testing\.F\)' . 2>/dev/null || true)

if [ "$found" -eq 0 ]; then
    echo "no fuzz targets found (func Fuzz*${target_regex:+ matching \"$target_regex\"}),skip"
    if [ -n "$target_regex" ]; then
        # An explicit target filter matching nothing is a caller error
        # (typo'd name / target renamed), not a benign empty sweep.
        exit 1
    fi
fi
