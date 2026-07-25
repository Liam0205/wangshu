#!/usr/bin/env bash
# test-go-fuzz-retry.sh — regression test for go-fuzz.sh's run_target
# discrimination logic (issue #63 / golang/go#75804 conditional retry).
#
# Shadows `go` with a stub on PATH and drives go-fuzz.sh against a fake
# single-target tree, asserting the three retry-path branches:
#
#   1. spurious-then-pass : deadline FAIL without a crasher, then PASS
#                           -> retried once, overall rc 0
#   2. real-crash         : FAIL with "Failing input written to ..."
#                           -> NO retry, immediate nonzero rc
#   3. always-spurious    : deadline FAIL twice -> nonzero rc (a second
#                           failure is reported as-is)
#
# Run: ./scripts/test-go-fuzz-retry.sh   (exits 0 iff all cases pass)
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
stub_dir=$(mktemp -d)
state_file="$stub_dir/state"
trap 'rm -rf "$stub_dir"' EXIT

cat > "$stub_dir/go" <<'STUB'
#!/usr/bin/env bash
# `go test` stub used by test-go-fuzz-retry.sh. Two invocation kinds
# must be distinguished, mirroring what scripts/go-fuzz.sh actually
# issues:
#
#   1. probe:  `go test [-tags ...] ./pkg -run=^$ -list ^FuzzX$`
#              -> must succeed and print the target name so go-fuzz.sh
#                 accepts the target as buildable and moves on to the
#                 real fuzz call. Independent of $STATE_FILE.
#   2. fuzz :  `go test [-tags ...] ./pkg -run=^$ -fuzz=^FuzzX$ ...`
#              -> $STATE_FILE dictates the outcome (deadline / crash
#                 / pass), driving go-fuzz.sh's #75804 retry logic.
#
# Discriminator: `-list` is a probe-only flag; `-fuzz` is a fuzz-only
# flag. Everything else (compile errors, unknown flags, ...) exits
# non-zero so a stub bug can't sneak past the harness.
for arg in "$@"; do
  case "$arg" in
    -list|-list=*) echo "FuzzX"; exit 0;;
  esac
done
saw_fuzz=0
for arg in "$@"; do
  case "$arg" in
    -fuzz|-fuzz=*) saw_fuzz=1; break;;
  esac
done
if [ "$saw_fuzz" -eq 0 ]; then
  echo "stub go: unexpected invocation $*" >&2
  exit 2
fi
case "$(cat "$STATE_FILE")" in
  spurious-then-pass)
    echo pass > "$STATE_FILE"
    echo "--- FAIL: FuzzX (31.01s)"
    echo "    context deadline exceeded"
    echo "FAIL"
    exit 1;;
  pass)
    echo "PASS"; exit 0;;
  real-crash)
    echo "--- FAIL: FuzzX (2.01s)"
    echo "    context deadline exceeded"  # can co-occur with a real crash
    echo "Failing input written to testdata/fuzz/FuzzX/abc123"
    exit 1;;
  always-spurious)
    echo "--- FAIL: FuzzX (31.01s)"
    echo "    context deadline exceeded"
    echo "FAIL"
    exit 1;;
esac
STUB
chmod +x "$stub_dir/go"
export STATE_FILE="$state_file"

fails=0

run_case() {
    local mode="$1" want="$2" extra_check="${3:-}"
    echo "$mode" > "$state_file"
    local tree log rc
    tree=$(mktemp -d)
    log=$(mktemp)
    mkdir -p "$tree/pkgx"
    cat > "$tree/pkgx/x_test.go" <<'GO'
package pkgx

import "testing"

func FuzzX(f *testing.F) { f.Fuzz(func(t *testing.T, b []byte) {}) }
GO
    (cd "$tree" && PATH="$stub_dir:$PATH" bash "$script_dir/go-fuzz.sh" 1s > "$log" 2>&1)
    rc=$?
    local ok=1
    if [ "$want" = "zero" ] && [ "$rc" -ne 0 ]; then ok=0; fi
    if [ "$want" = "nonzero" ] && [ "$rc" -eq 0 ]; then ok=0; fi
    if [ -n "$extra_check" ] && ! eval "$extra_check"; then ok=0; fi
    if [ "$ok" -eq 1 ]; then
        echo "CASE $mode: OK (rc=$rc)"
    else
        echo "CASE $mode: FAILED (rc=$rc, want $want)"
        tail -8 "$log"
        fails=$((fails + 1))
    fi
    rm -rf "$tree" "$log"
}

# Each case's extra_check does prove-the-path: it asserts a byte that
# ONLY appears when run_target really executed the intended branch.
# "retrying once" is emitted from within run_target's spurious-deadline
# adjudication; "Failing input written to" is the fuzz stub's own crash
# signature and only surfaces when run_target actually ran the target
# (not when the probe short-circuited). Without these positive markers
# a bug in the probe path could turn the whole harness green while
# skipping the retry logic entirely (issue #179).
#
# 1. Spurious deadline once, pass on retry -> success, retry emitted.
run_case spurious-then-pass zero 'grep -q "retrying once" "$log"'
# 2. Real crasher -> immediate failure, NO retry, crash line reached.
run_case real-crash nonzero '! grep -q "retrying once" "$log" && grep -q "Failing input written to" "$log"'
# 3. Spurious twice -> failure (second failure reported as-is), exactly
#    one retry.
run_case always-spurious nonzero '[ "$(grep -c "retrying once" "$log")" -eq 1 ]'

if [ "$fails" -ne 0 ]; then
    echo "✗ $fails case(s) failed"
    exit 1
fi
echo "✓ all retry-path cases passed"
