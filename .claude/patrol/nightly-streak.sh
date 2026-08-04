#!/usr/bin/env bash
# Consecutive successful nightly-diff-fuzz runs, most recent first.
#
# Counts only CONCLUDED runs: an in-progress run has a null conclusion and must neither break the
# streak nor count toward it.
set -uo pipefail
gh run list --workflow=nightly-diff-fuzz.yml --limit 60 \
  --json conclusion --jq '.[].conclusion' 2>/dev/null \
| awk '
    /^$/      { next }                 # still running: skip
    $0=="success" { n++; next }
    { exit }                           # first concluded non-success ends the streak
    END { print n+0 }'
