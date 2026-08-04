#!/usr/bin/env bash
# Single-instance guard for the wangshu nightly patrol.
#
# A patrol run can legitimately exceed 24h (a hard crasher plus several audit rounds), so the 03:07
# schedule can fire while the previous run is still working. Only one may proceed.
#
# Implemented with a MARKER FILE holding a deadline, not with a PID liveness check: the patrol runs
# inside the assistant's session rather than as a shell process, so the PID that writes the lock is
# gone by the time the next fire checks it -- an earlier PID-based version wrongly reclaimed its own
# live lock for exactly that reason. A deadline is self-healing without needing a live process:
# a crashed run's lock expires on its own.
#
#   lock.sh acquire [hours]  -> 0 if acquired (default lease 36h), 1 if a live lease exists
#   lock.sh renew   [hours]  -> extend the lease while still working
#   lock.sh release          -> drop it
#   lock.sh status           -> print remaining lease
set -uo pipefail
LOCK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCK_FILE="$LOCK_DIR/patrol.lock"
now() { date +%s; }

case "${1:-status}" in
  acquire|renew)
    hours="${2:-36}"
    if [ "$1" = acquire ] && [ -f "$LOCK_FILE" ]; then
      exp="$(head -1 "$LOCK_FILE" 2>/dev/null || echo 0)"
      if [ "$exp" -gt "$(now)" ] 2>/dev/null; then
        echo "HELD, $(( (exp - $(now)) / 3600 ))h of lease left; not starting a second run"
        exit 1
      fi
      echo "expired lease -- reclaiming"
    fi
    printf '%s\n%s\n' "$(( $(now) + hours * 3600 ))" "since $(date -Iseconds)" > "$LOCK_FILE"
    echo "$1 ok, lease ${hours}h"
    ;;
  release) rm -f "$LOCK_FILE"; echo "RELEASED" ;;
  *)
    if [ -f "$LOCK_FILE" ]; then
      exp="$(head -1 "$LOCK_FILE")"
      left=$(( (exp - $(now)) / 3600 ))
      [ "$left" -gt 0 ] && echo "HELD, ${left}h left" || echo "EXPIRED (reclaimable)"
    else
      echo "FREE"
    fi ;;
  esac
