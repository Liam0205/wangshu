#!/usr/bin/env bash
# Self-test for scripts/fetch-lua-tarball.sh (#236-#241).
#
# The behaviours worth pinning are the ones whose absence caused the incident: a fetch that cannot hang
# unboundedly, a checksum that refuses a bad tarball, and a cached tarball that is reused rather than
# re-downloaded. All three run offline against a local file:// origin, so this test never touches the
# network and cannot itself become a flaky CI step -- which is the whole point of the change.
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fetch-lua-tarball.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0
ok()   { printf 'CASE %-28s OK\n' "$1"; }
bad()  { printf 'CASE %-28s FAILED: %s\n' "$1" "$2"; fail=1; }

# A stand-in "upstream": a real gzip tarball, plus the script pointed at it via file://.
mkfake() {
  local dir="$1" content="${2:-real}"
  mkdir -p "$dir/lua-5.1.5/src"
  printf '%s\n' "$content" > "$dir/lua-5.1.5/src/lua.c"
  ( cd "$dir" && tar czf lua-5.1.5.tar.gz lua-5.1.5 && rm -rf lua-5.1.5 )
}

# Rewrite the URL and the expected checksum so the script can be exercised offline.
variant() {
  local out="$1" url="$2" sum="$3"
  sed -e "s|https://www.lua.org/ftp/\${TARBALL}|${url}|" \
      -e "s|^SHA256=.*|SHA256=${sum}|" "$SCRIPT" > "$out"
  chmod +x "$out"
}

# 1. checksum mismatch must refuse, remove the file, and exit non-zero.
# The origin MUST be a separate path from the download destination: an earlier version pointed both at
# lua-5.1.5.tar.gz, so the script's own `rm -f "$TARBALL"` deleted the origin, curl failed with (37),
# and the run took the download-failed branch -- never reaching the checksum check this case exists to
# test. Deleting the whole verify block left all four cases green, which is how that was found.
d="$WORK/c1"; mkdir -p "$d"; mkfake "$d"
mv "$d/lua-5.1.5.tar.gz" "$d/origin.tar.gz"
variant "$d/f.sh" "file://$d/origin.tar.gz" "$(printf '0%.0s' {1..64})"
rc=0
( cd "$d" && rm -rf lua-5.1.5 && ./f.sh >/dev/null 2>&1 ) || rc=$?
if [ "$rc" -eq 0 ]; then bad checksum-mismatch "exited 0 on a bad checksum"
elif [ -d "$d/lua-5.1.5" ]; then bad checksum-mismatch "unpacked despite the mismatch"
else ok checksum-mismatch; fi

# 2. a good checksum must unpack.
d="$WORK/c2"; mkdir -p "$d"; mkfake "$d"
sum=$(sha256sum "$d/lua-5.1.5.tar.gz" | cut -d' ' -f1)
variant "$d/f.sh" "file://$d/lua-5.1.5.tar.gz" "$sum"
cp "$d/lua-5.1.5.tar.gz" "$d/upstream.tar.gz"
if ( cd "$d" && rm -f lua-5.1.5.tar.gz && rm -rf lua-5.1.5 \
     && sed -i "s|file://$d/lua-5.1.5.tar.gz|file://$d/upstream.tar.gz|" f.sh \
     && ./f.sh >/dev/null 2>&1 ) && [ -f "$d/lua-5.1.5/src/lua.c" ]; then
  ok good-checksum
else
  bad good-checksum "a valid tarball did not unpack"
fi

# 3. an already-present verified tarball must be reused, not re-fetched. Point the URL at a
#    non-existent file: if the script still succeeds, it used the cache.
d="$WORK/c3"; mkdir -p "$d"; mkfake "$d"
sum=$(sha256sum "$d/lua-5.1.5.tar.gz" | cut -d' ' -f1)
variant "$d/f.sh" "file://$d/does-not-exist.tar.gz" "$sum"
if ( cd "$d" && rm -rf lua-5.1.5 && ./f.sh >/dev/null 2>&1 ) && [ -d "$d/lua-5.1.5" ]; then
  ok cached-reuse
else
  bad cached-reuse "did not reuse a verified cached tarball"
fi

# 4. the real script must carry a transfer bound. Its absence is what turned one blip into six issues.
# Inspect the curl INVOCATION, not the whole file: a first version of this check grepped the file and
# passed even with the flag deleted, because the word survived in a comment. A test that a comment can
# satisfy is not testing the code.
curl_cmd=$(sed -n '/curl -fsSL/,/"https:/p' "$SCRIPT" | tr -d '\\\n')
missing=""
for flag in --connect-timeout --max-time --retry; do
  case "$curl_cmd" in *"$flag"*) ;; *) missing="$missing $flag";; esac
done
if [ -z "$missing" ]; then
  ok bounded-and-retried
else
  bad bounded-and-retried "curl invocation lacks:$missing"
fi

if [ "$fail" -eq 0 ]; then
  echo "✓ all fetch-lua cases passed"
else
  echo "✗ failures above"
  exit 1
fi
