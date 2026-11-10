#!/usr/bin/env bash
# Self-test for scripts/check-oracle.sh (#309).
#
# The property worth pinning is the one whose absence let the incident through: a lua5.1 that reports
# the right version but computes '%' with a fused multiply-subtract must be REJECTED on x86-64, and must
# not be rejected on arm64, where that is the expected build (#323).
#
# Runs against stand-in `lua5.1` and `uname` commands on an isolated PATH, so it needs neither a real
# interpreter nor a particular host architecture.
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-oracle.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

ok()   { printf 'CASE %-34s OK\n' "$1"; }
bad()  { printf 'CASE %-34s FAILED: %s\n' "$1" "$2"; fail=1; }

BASH_BIN="$(command -v bash)"

# mkbin <dir> <arch> [<version line> <probe output>]: an isolated PATH directory. Without the last two
# arguments no lua5.1 is installed in it.
mkbin() {
  local dir="$1" arch="$2"
  mkdir -p "$dir"
  ln -s "$BASH_BIN" "$dir/bash"
  printf '#!%s\necho %s\n' "$BASH_BIN" "$arch" > "$dir/uname"
  chmod +x "$dir/uname"
  if [ $# -ge 4 ]; then
    # The real interpreter prints its version on stderr; check-oracle.sh reads both streams.
    cat > "$dir/lua5.1" <<EOF
#!$BASH_BIN
if [ "\$1" = "-v" ]; then echo '$3' >&2; exit 0; fi
printf '%s' '$4'
EOF
    chmod +x "$dir/lua5.1"
  fi
}

# run <dir>: run the script with only <dir> on PATH; sets rc and out.
run() {
  set +e
  out="$(PATH="$1" "$BASH_BIN" "$SCRIPT" 2>&1)"
  rc=$?
  set -e
}

V515='Lua 5.1.5  Copyright (C) 1994-2012 Lua.org, PUC-Rio'

mkbin "$WORK/good" x86_64 "$V515" '0 0 0'
run "$WORK/good"
if [ "$rc" -eq 0 ] && grep -q 'oracle % ok' <<<"$out"; then ok "x86-64 rounded product"
else bad "x86-64 rounded product" "rc=$rc out=$out"; fi

mkbin "$WORK/fused" x86_64 "$V515" '-2944 -2944 -2944'
run "$WORK/fused"
if [ "$rc" -ne 0 ] && grep -q 'FMA' <<<"$out"; then ok "x86-64 fused is rejected"
else bad "x86-64 fused is rejected" "rc=$rc out=$out"; fi

# One fused evaluation site is enough to reject: the three probes cover different code in the
# reference implementation, and a build can differ between them.
mkbin "$WORK/partial" x86_64 "$V515" '0 -2944 0'
run "$WORK/partial"
if [ "$rc" -ne 0 ]; then ok "x86-64 one fused site is rejected"
else bad "x86-64 one fused site is rejected" "rc=$rc out=$out"; fi

mkbin "$WORK/arm" aarch64 "$V515" '-2944 -2944 -2944'
run "$WORK/arm"
if [ "$rc" -eq 0 ] && grep -q -- '-2944' <<<"$out"; then ok "arm64 fused is reported, not failed"
else bad "arm64 fused is reported, not failed" "rc=$rc out=$out"; fi

mkbin "$WORK/oldver" x86_64 'Lua 5.1.4  Copyright (C) 1994-2008 Lua.org, PUC-Rio' '0 0 0'
run "$WORK/oldver"
if [ "$rc" -ne 0 ] && grep -q '5.1.4' <<<"$out"; then ok "wrong version is rejected"
else bad "wrong version is rejected" "rc=$rc out=$out"; fi

mkbin "$WORK/none" x86_64
run "$WORK/none"
if [ "$rc" -ne 0 ]; then ok "missing lua5.1 is rejected"
else bad "missing lua5.1 is rejected" "rc=$rc out=$out"; fi

if [ "$fail" -ne 0 ]; then
  echo "test-check-oracle: FAILED" >&2
  exit 1
fi
echo "test-check-oracle: all cases passed"
