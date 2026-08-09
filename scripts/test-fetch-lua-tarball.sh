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
# The test needs a digest too, and an earlier version called sha256sum directly -- repeating the exact
# portability defect it exists to guard. CI's test-scripts job is ubuntu so CI could not see it, but
# `make all` includes test-scripts and macOS is a documented dev environment.
if command -v sha256sum >/dev/null 2>&1; then
  sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "neither sha256sum nor shasum available; cannot run this test" >&2
  exit 1
fi

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
sum=$(sha256_of "$d/lua-5.1.5.tar.gz")
variant "$d/f.sh" "file://$d/lua-5.1.5.tar.gz" "$sum"
# The origin is passed to variant() directly. An earlier version generated the variant against one URL
# and then rewrote it with `sed -i`, which is not portable: BSD sed (macOS) requires an explicit suffix
# argument for -i, so the chain broke and this case failed there. Third time a change in this file
# repeated the portability class it exists to guard, so: no in-place edits, pass the value once.
mv "$d/lua-5.1.5.tar.gz" "$d/upstream.tar.gz"
variant "$d/f.sh" "file://$d/upstream.tar.gz" "$sum"
if ( cd "$d" && rm -rf lua-5.1.5 && ./f.sh >/dev/null 2>&1 ) && [ -f "$d/lua-5.1.5/src/lua.c" ]; then
  ok good-checksum
else
  bad good-checksum "a valid tarball did not unpack"
fi

# 3. an already-present verified tarball must be reused, not re-fetched. Point the URL at a
#    non-existent file: if the script still succeeds, it used the cache.
d="$WORK/c3"; mkdir -p "$d"; mkfake "$d"
sum=$(sha256_of "$d/lua-5.1.5.tar.gz")
variant "$d/f.sh" "file://$d/does-not-exist.tar.gz" "$sum"
if ( cd "$d" && rm -rf lua-5.1.5 && ./f.sh >/dev/null 2>&1 ) && [ -d "$d/lua-5.1.5" ]; then
  ok cached-reuse
else
  bad cached-reuse "did not reuse a verified cached tarball"
fi

# 4. a cached tarball must be VERIFIED, not merely reused. Plant a corrupt tarball and point the URL at
#    a missing file: if the script trusts the cache blindly it "succeeds" with garbage.
# A VALID gzip tarball whose checksum is wrong -- not garbage bytes. Garbage cannot distinguish the
# cases, because tar rejects it anyway and the script fails for that reason instead: a first version
# planted "not a tarball" and the blind-trust mutation still failed, so the case proved nothing.
d="$WORK/c3b"; mkdir -p "$d"; mkfake "$d" "wrong-content"
variant "$d/f.sh" "file://$d/absent.tar.gz" "$(printf '0%.0s' {1..64})"
rc=0
( cd "$d" && rm -rf lua-5.1.5 && ./f.sh >/dev/null 2>&1 ) || rc=$?
if [ "$rc" -eq 0 ] || [ -d "$d/lua-5.1.5" ]; then
  bad cached-is-verified "trusted a corrupt cached tarball"
else
  ok cached-is-verified
fi

# 5. the real script must carry a transfer bound. Its absence is what turned one blip into six issues.
# Inspect the curl INVOCATION, not the whole file: a first version of this check grepped the file and
# passed even with the flag deleted, because the word survived in a comment. A test that a comment can
# satisfy is not testing the code.
# Extract from the code, with comment lines stripped FIRST. The anchor itself was satisfiable by a
# comment: writing a line mentioning `curl -fsSL ... "https://` in the header and deleting all three real
# flags left this green. Stripping comments before matching removes that whole avenue -- the third
# distinct way this one check has been vacuous, which is why it is now belt and braces (comments removed,
# invocation isolated, values required positive).
curl_cmd=$(grep -v '^[[:space:]]*#' "$SCRIPT" | sed -n '/curl -fsSL/,/"https:/p' | tr -d '\\\n')
if [ -z "$curl_cmd" ]; then
  bad bounded-and-retried "could not locate the curl invocation in the script"
  curl_cmd="(missing)"
fi
missing=""
# Check the VALUES, not just presence: --max-time 0 means "no limit" and --retry 0 is curl's default,
# i.e. exactly the pre-#236 state, and a presence-only test passed with all three set to 0. Require a
# positive integer after each.
for flag in --connect-timeout --max-time; do
  ok_val=0
  for n in $(printf '%s\n' "$curl_cmd" | grep -o -- "$flag [0-9][0-9]*" | grep -o '[0-9][0-9]*$' || true); do
    [ "$n" -gt 0 ] && ok_val=1
  done
  [ "$ok_val" -eq 1 ] || missing="$missing $flag<positive>"
done
# --retry needs its own pattern: a bare substring test is satisfied by --retry-delay and
# --retry-all-errors, so deleting the real `--retry 4` left this green. curl's default retry count is 0
# and --retry-all-errors does nothing without --retry, so that deletion removed retrying entirely --
# the half of the fix this case exists for. Require --retry followed by a digit.
# `|| true`: grep exits 1 when the flag is absent, and under `set -e` that aborted the whole test before
# the verdict printed -- a mutation was correctly DETECTED but silently, which reads as a pass.
retry_n=$(printf '%s\n' "$curl_cmd" | grep -o -- '--retry [0-9][0-9]*' | grep -o '[0-9][0-9]*$' | head -1 || true)
if [ -z "$retry_n" ] || [ "$retry_n" -lt 1 ]; then
  missing="$missing --retry<positive>"
fi
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
