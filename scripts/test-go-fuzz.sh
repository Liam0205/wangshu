#!/usr/bin/env bash
# test-go-fuzz.sh — end-to-end self-test for scripts/go-fuzz.sh.
#
# Shadows `go` with a stub on PATH and drives go-fuzz.sh against a fake
# single-target tree. Each case asserts both the exit status AND how many
# times the stub was asked to fuzz, so a go-fuzz.sh that silently stops
# discovering or running targets (rc 0, zero fuzz calls) fails here.
# That exact regression slipped through once: a rewrite dropped the
# target-discovery loop, `bash -n` and every other self-test stayed
# green, and only a call count exposed it (#180).
#
#   1. pass     : fuzz PASS            -> rc 0, one fuzz call
#   2. crash    : crasher written      -> rc nonzero, one fuzz call
#   3. deadline : deadline FAIL, no
#                 crasher written      -> rc nonzero, one fuzz call (no
#                                         retry since golang/go#75804 was
#                                         fixed in Go 1.27, #180)
#   4. filter   : target_regex that
#                 matches nothing      -> rc nonzero, zero fuzz calls
#
# Run: ./scripts/test-go-fuzz.sh   (exits 0 iff all cases pass)
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
stub_dir=$(mktemp -d)
trap 'rm -rf "$stub_dir"' EXIT

cat > "$stub_dir/go" <<'STUB'
#!/usr/bin/env bash
# `go test` stub. The -list probe must succeed and print the target so
# go-fuzz.sh accepts it; every -fuzz call is counted in $CALLS_FILE and
# answers according to $MODE. Anything else exits non-zero so a stub bug
# cannot pass silently.
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
echo fuzz >> "$CALLS_FILE"
case "$MODE" in
  pass)
    echo "PASS"; exit 0;;
  crash)
    echo "--- FAIL: FuzzX (2.01s)"
    echo "Failing input written to testdata/fuzz/FuzzX/abc123"
    exit 1;;
  deadline)
    echo "--- FAIL: FuzzX (31.01s)"
    echo "    context deadline exceeded"
    echo "FAIL"
    exit 1;;
esac
echo "stub go: unknown MODE '$MODE'" >&2
exit 2
STUB
chmod +x "$stub_dir/go"

fails=0

# run_case <mode> <want_rc: zero|nonzero> <want_calls> [target_regex]
run_case() {
    local mode="$1" want="$2" want_calls="$3" regex="${4:-}"
    local tree log calls rc
    tree=$(mktemp -d)
    log=$(mktemp)
    calls=$(mktemp)
    mkdir -p "$tree/pkgx"
    cat > "$tree/pkgx/x_test.go" <<'GO'
package pkgx

import "testing"

func FuzzX(f *testing.F) { f.Fuzz(func(t *testing.T, b []byte) {}) }
GO
    (cd "$tree" && MODE="$mode" CALLS_FILE="$calls" PATH="$stub_dir:$PATH" \
        bash "$script_dir/go-fuzz.sh" 1s "" ${regex:+"$regex"} > "$log" 2>&1)
    rc=$?
    local got_calls ok=1
    got_calls=$(wc -l < "$calls" | tr -d ' ')
    if [ "$want" = "zero" ] && [ "$rc" -ne 0 ]; then ok=0; fi
    if [ "$want" = "nonzero" ] && [ "$rc" -eq 0 ]; then ok=0; fi
    if [ "$got_calls" -ne "$want_calls" ]; then ok=0; fi
    if [ "$ok" -eq 1 ]; then
        echo "CASE $mode${regex:+ ($regex)}: OK (rc=$rc, fuzz calls=$got_calls)"
    else
        echo "CASE $mode${regex:+ ($regex)}: FAILED (rc=$rc want $want, fuzz calls=$got_calls want $want_calls)"
        tail -8 "$log"
        fails=$((fails + 1))
    fi
    rm -rf "$tree" "$log" "$calls"
}

run_case pass     zero    1
run_case crash    nonzero 1
run_case deadline nonzero 1
run_case pass     nonzero 0 FuzzNoSuchTarget

if [ "$fails" -ne 0 ]; then
    echo "✗ $fails case(s) failed"
    exit 1
fi
echo "✓ all go-fuzz.sh cases passed"
