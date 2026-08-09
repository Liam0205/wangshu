#!/usr/bin/env bash
# Fetch and unpack the PUC Lua 5.1.5 source tarball, resiliently.
#
# Four workflow steps ran a bare `curl` on www.lua.org with no timeout and no retry. That is how
# #236-#241 happened: on 2026-08-07 two nightly runs sat ~2m15s on the fetch and then exited 28 --
# curl's CURLE_OPERATION_TIMEDOUT, not a disk error -- which failed the oracle install step and left the
# three differential fuzz steps SKIPPED, so each run reported failure while testing nothing. Six issues
# were filed for one upstream reachability blip.
#
# Behaviour:
#   - bounded: --connect-timeout and --max-time, so a hung fetch fails fast instead of consuming the step
#   - retried: several attempts with backoff, since the observed failure was transient
#   - verified: SHA-256 checked before unpacking, so a truncated or substituted download cannot be
#     silently compiled into the differential oracle
#   - cacheable: an existing verified tarball is reused, so actions/cache can skip the network entirely
#
# No mirror: github.com/lua/lua carries no 5.1.5 tag, and any re-packed copy would fail the official
# checksum, which is the property worth more than a second source.
set -euo pipefail

VERSION=5.1.5
TARBALL="lua-${VERSION}.tar.gz"
# Official SHA-256 of lua-5.1.5.tar.gz from www.lua.org/ftp/ (verified against the live file).
SHA256=2640fc56a795f29d28ef15e13c34a47e223960b0240e8cb0a82d9b0738695333

log() { printf '[fetch-lua] %s\n' "$*" >&2; }

# Portable digest: macOS ships `shasum -a 256`, Linux ships `sha256sum`. Three of the four call sites
# are macOS, and an earlier version of this script called sha256sum unconditionally -- which on macOS
# exits 127 (command not found), reads as a checksum failure, and DELETES a perfectly good tarball
# before exiting 1. The sites it replaced used `curl | tar xz` and worked, so that was a regression.
#
# A missing digest tool is fatal rather than skipped: the checksum is the property that keeps a
# truncated or substituted download from being compiled into the differential oracle, so silently
# proceeding without it would defeat the point.
digest() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    log "neither sha256sum nor shasum is available; cannot verify the download"
    exit 1
  fi
}

verify() { [ -f "$TARBALL" ] && [ "$(digest "$TARBALL")" = "$SHA256" ]; }

if [ -f "$TARBALL" ] && verify; then
  log "reusing the cached tarball (checksum ok)"
else
  rm -f "$TARBALL"
  log "downloading lua ${VERSION}"
  # --retry-all-errors so a timeout retries too; without it curl retries only "transient" HTTP codes.
  if ! curl -fsSL --connect-timeout 15 --max-time 120 \
            --retry 4 --retry-delay 5 --retry-all-errors \
            -o "$TARBALL" "https://www.lua.org/ftp/${TARBALL}"; then
    log "download failed after retries (upstream unreachable)"
    rm -f "$TARBALL"
    exit 1
  fi
  if ! verify; then
    log "checksum mismatch; refusing to build the oracle from it"
    rm -f "$TARBALL"
    exit 1
  fi
fi

rm -rf "lua-${VERSION}"
tar xzf "$TARBALL"
log "ok: lua-${VERSION}/ ready"
