#!/usr/bin/env bash
# Build the official Lua 5.1.5 interpreter from the upstream tarball and install it as lua5.1.
#
# Used by the Linux CI steps that need the reference interpreter built from source (the nightly
# differential fuzz always, PR CI when apt fails or hands back an unusable binary). macOS keeps its own
# `make macosx` steps.
#
# What this adds over `fetch-lua-tarball.sh && make posix`:
#
#   On x86-64 the build passes -ffp-contract=off. Lua 5.1 computes a % b as a - floor(a/b)*b
#   (luaconf.h luai_nummod). A compiler that may contract the expression and targets a CPU with FMA
#   computes a - q*b in one fused instruction, and the result differs from the rounded-product reading
#   whenever q*b is not exactly representable (84572069678023671808 % 89: 0 against -2944). On
#   2026-11-08 the nightly runner image moved to ubuntu-26.04 and the oracle it built started giving
#   the fused answers; every round then reported dozens of divergences that no other machine reproduced
#   (#309 .. #320). The reference answer must not depend on which image compiled it, so contraction is
#   switched off explicitly instead of being left to the toolchain default.
#
#   Only on x86-64. On arm64 the default build is fused, and so is wangshu's Go code there, so the two
#   agree; pinning one side alone would manufacture a divergence. Making '%' unfused everywhere is a
#   product change tracked in #323.
#
# The compiler facts that decide whether the default would have fused are logged. That log is how the
# cause was found: the ubuntu-26.04 runner image installs Ubuntu's amd64v3 packages, whose gcc has the
# same version string as the plain amd64 one but defaults to -march=x86-64-v3, a target with FMA. A
# stock ubuntu:26.04 is amd64 and does not fuse, which is why this never reproduced locally.
#
# Environment:
#   LUA_ORACLE_CC    compiler command (default: gcc). The contraction flag is appended to it.
#   LUA_ORACLE_DEST  install path (default: /usr/local/bin/lua5.1). sudo is used only if needed.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CC_CMD="${LUA_ORACLE_CC:-gcc}"
DEST="${LUA_ORACLE_DEST:-/usr/local/bin/lua5.1}"

log() { printf '[build-lua-oracle] %s\n' "$*" >&2; }

"$HERE/fetch-lua-tarball.sh"

arch="$(uname -m)"
# shellcheck disable=SC2086  # CC_CMD is a command line, word splitting is intended
{
  log "arch: $arch"
  log "compiler: $($CC_CMD --version 2>/dev/null | head -n 1 || echo unknown)"
  log "default -march: $($CC_CMD -Q --help=target 2>/dev/null | awk '$1 == "-march=" { print $2 }' || true)"
  if echo | $CC_CMD -dM -E -x c - 2>/dev/null | grep -q '__FMA__'; then
    log "the compiler's default target has FMA: an unpinned build would fuse a - q*b"
  else
    log "the compiler's default target has no FMA"
  fi
}

case "$arch" in
  x86_64|amd64)
    CC_CMD="$CC_CMD -ffp-contract=off"
    log "x86-64: building with -ffp-contract=off"
    ;;
  *)
    log "$arch: building with the toolchain default (see the header for why)"
    ;;
esac

# `posix` rather than `linux`: it needs no libreadline, so the step has no apt dependency at all.
make -C lua-5.1.5 posix -j2 CC="$CC_CMD"

if [ -w "$(dirname "$DEST")" ]; then
  cp lua-5.1.5/src/lua "$DEST"
else
  sudo cp lua-5.1.5/src/lua "$DEST"
fi
log "installed $DEST"
