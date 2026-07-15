#!/bin/bash
# check-pr-ci.sh -- block until the current branch's PR CI finishes, then report.
#
# Exit codes:
#   0  All CI checks pass AND no new review activity since the last push AND no
#      unresolved review threads -- terminal state.
#   1  One or more CI checks failed (failing jobs listed on stderr).
#   2  No PR found for the current branch, or gh unavailable (the calling hook
#      treats this as non-fatal; the script reports a distinct code so callers
#      can tell the cases apart).
#   75 CI passed, but there is new review activity / an unresolved review
#      thread since the last push. The instruction block printed before exit
#      explains the next step. Surfaced as a distinct non-zero code so the
#      calling hook fails the OUTER push and tools that only look at the exit
#      status (e.g. an agent loop) do not miss the stderr instructions as
#      silent context.
#
# This script does **not** push. It only inspects the PR state of a branch
# that has already been pushed.
set -uo pipefail

log() { echo "$@" >&2; }

if ! command -v gh >/dev/null 2>&1; then
  log "post-push: gh CLI not found — skipping CI check."
  exit 2
fi

# jq parses the check list. gh has a built-in --jq filter, but there are
# standalone jq parses below; a missing jq would silently drop failures
# (false green), so treat it as fatal.
if ! command -v jq >/dev/null 2>&1; then
  log "post-push: jq not found — cannot reliably parse CI checks. Failing safe."
  exit 1
fi

branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null)"

# Lock the "new activity" cutoff to NOW, **before** spending minutes watching
# CI -- any GitHub comment with createdAt > this instant is strictly newer
# than our latest push. Locking early (not after the CI watch) means
# legitimate review activity arriving during CI still counts as new. UTC
# ('Z') matches GitHub's UTC createdAt field; a local tz offset like
# "+08:00" would break the literal comparison against 'Z' timestamps.
push_cutoff="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Find the PR for the current branch. Nothing to watch without one.
pr_number="$(gh pr view --json number --jq '.number' 2>/dev/null)"
if [ -z "$pr_number" ]; then
  log "post-push: no open PR for branch '$branch' yet — skipping CI check."
  log "post-push: (open a PR, then CI status will be watched on the next push)"
  exit 2
fi

log "post-push: watching CI for PR #${pr_number} (branch '$branch')..."

# Right after a push, GitHub takes a few seconds to register check runs for
# the new head commit. Poll until at least one check appears (or give up),
# otherwise "not created yet" would be misread as "no CI".
# Tunable via env:
#   WANGSHU_PREPUSH_CHECK_POLL_TRIES    (default 24)
#   WANGSHU_PREPUSH_CHECK_POLL_INTERVAL (default 5, seconds)
poll_tries="${WANGSHU_PREPUSH_CHECK_POLL_TRIES:-24}"
poll_interval="${WANGSHU_PREPUSH_CHECK_POLL_INTERVAL:-5}"
for _ in $(seq 1 "$poll_tries"); do
  cnt="$(gh pr checks "$pr_number" --json state --jq 'length' 2>/dev/null)"
  if [ -n "$cnt" ] && [ "$cnt" -gt 0 ]; then
    break
  fi
  sleep "$poll_interval"
done

# Block until all checks finish. --fail-fast returns as soon as any check
# fails. The watch exit code is deliberately ignored; the final verdict is
# recomputed from a --json query so the reporting logic has a single source.
gh pr checks "$pr_number" --watch --fail-fast --interval 15 >/dev/null 2>&1 || true

# Re-fetch a snapshot of all checks.
checks_json="$(gh pr checks "$pr_number" --json name,state,bucket,link 2>/dev/null)"
if [ -z "$checks_json" ]; then
  log "post-push: could not read CI checks for PR #${pr_number}."
  exit 2
fi

# Collect all failing jobs (bucket == fail or cancel) as "name<TAB>link"
# lines. jq is a hard dependency (checked above); keeping dependencies
# minimal avoids silently returning empty when some interpreter is missing.
failures="$(printf '%s' "$checks_json" \
  | jq -r '.[] | select(.bucket=="fail" or .bucket=="cancel") | "\(.name)\t\(.link // "")"')"

review_decision="$(gh pr view "$pr_number" --json reviewDecision --jq '.reviewDecision' 2>/dev/null)"
[ -z "$review_decision" ] && review_decision="(no review yet)"

if [ -n "$failures" ]; then
  log ""
  log "════════════════════════════════════════════════════════════"
  log "  ✗ post-push: CI FAILED for PR #${pr_number}"
  log "  Failing checks:"
  while IFS=$'\t' read -r name link; do
    [ -z "$name" ] && continue
    log "    • ${name}"
    [ -n "$link" ] && log "        ${link}"
  done <<< "$failures"
  log ""
  log "  → Investigate the failing jobs above."
  log "  → Also check the PR review status (currently: ${review_decision})."
  log "════════════════════════════════════════════════════════════"
  exit 1
fi

log ""
log "════════════════════════════════════════════════════════════"
log "  ✓ post-push: all CI checks passed for PR #${pr_number}"
log "  → PR review decision: ${review_decision}"
log "════════════════════════════════════════════════════════════"

# -- instruction block for Claude Code ---------------------------------------
# When this script runs inside a Claude Code background bash task, all the
# stderr above is fed back into the main conversation when the task ends.
# The block below is written for Claude to consume directly: concrete gh
# commands that assemble the next loop iteration (fetch review comments ->
# understand each one -> fix real issues -> push again), skipping the
# "ask a human to address review feedback" round-trip.
#
# Trigger model (deliberately signal-only, no semantics):
#   We do **not** grep the review bot output for the word "APPROVE". The bot
#   is itself an LLM with non-deterministic wording, and even an APPROVE
#   often carries "non-blocking" suggestions we still want to evaluate. So
#   the script only decides "is there anything new since the current HEAD
#   was pushed"; the actual judgment (real issue vs false positive,
#   blocking vs nit) is left to Claude.
#
# The instruction block fires when any of these hold:
#   - new top-level issue comments since the HEAD push
#   - new inline review comments since the HEAD push
#   - new review submissions since the HEAD push (an APPROVE /
#     CHANGES_REQUESTED event itself counts as new activity)
#   - any unresolved review thread (regardless of age -- an old unresolved
#     thread is still debt the loop has not closed)
#
# If none of the above, exit 0 quietly with a one-line success; automated
# loops read that as "done".

owner="$(gh repo view --json owner --jq '.owner.login' 2>/dev/null)"
repo="$(gh repo view --json name --jq '.name' 2>/dev/null)"

# One GraphQL round-trip fetches everything the "new activity since push"
# detection needs. Each column is filtered independently in jq by
# createdAt > $push_cutoff (locked at script start, before the CI watch).
activity_json="$(gh api graphql -f query='
  query($owner:String!, $repo:String!, $pr:Int!) {
    repository(owner:$owner, name:$repo) {
      pullRequest(number:$pr) {
        comments(last:50)        { nodes { createdAt } }
        reviews(last:50)         { nodes { createdAt state } }
        reviewThreads(first:100) {
          nodes {
            isResolved
            comments(last:50) { nodes { createdAt } }
          }
        }
      }
    }
  }' \
  -F owner="$owner" -F repo="$repo" -F pr="$pr_number" 2>/dev/null)"

# Two ways activity can be unreadable:
#   (a) gh produces no stdout at all (network down / broken gh / no auth)
#   (b) gh returns a GraphQL error envelope `{"errors":[...]}` with exit 0 --
#       the usual shape for expired auth / transient 5xx / schema mismatch.
#       The envelope is non-empty, so `[ -z ]` is not enough; without this
#       branch the jq filters below blow up on `.data == null` with "Cannot
#       iterate over null", all four counters end up empty, and the later
#       `[ -eq ]` leaks "integer expression expected".
# Both paths must surface as "work remains" so the next-step block fires and
# the operator inspects the PR manually -- silently treating a malformed
# envelope as a clean terminal would break the autonomous loop.
activity_unreadable=0
if [ -z "$activity_json" ]; then
  activity_unreadable=1
elif ! printf '%s' "$activity_json" | jq empty >/dev/null 2>&1; then
  # gh produced non-empty stdout that is not valid JSON (auth interstitial,
  # captive-portal HTML, transient gateway error page, ...). Without this
  # guard the `jq -e has("errors")` below fails to parse too, the elif is
  # false, we fall into the "readable" branch, and every counter collapses
  # to 0 via `${var:-0}` -- masquerading as a clean terminal "done".
  activity_unreadable=1
elif printf '%s' "$activity_json" \
       | jq -e 'has("errors") or .data == null' >/dev/null 2>&1; then
  activity_unreadable=1
fi

if [ "$activity_unreadable" -eq 1 ]; then
  log "  ! could not query PR activity — assuming work remains, see comments manually."
  # Force the next-step block to fire (total_new > 0) so the operator
  # inspects the PR manually instead of the script exiting 0 silently.
  new_issue_comments=1
  new_review_submissions=0
  new_inline_comments=0
  unresolved_threads=0
else
  # `// []` collapses a missing or null `nodes` to an empty array so jq
  # still yields totals on partial responses; the `?` on the inner
  # inline-comments traversal tolerates thread nodes without comments.
  # `2>/dev/null` + `${var:-0}` keep all four counters numeric under any
  # surprise, so later arithmetic and `[ -eq ]` never see an empty operand.
  new_issue_comments="$(printf '%s' "$activity_json" \
    | jq --arg t "$push_cutoff" \
        '[(.data.repository.pullRequest.comments.nodes // [])[] | select(.createdAt > $t)] | length' 2>/dev/null)"
  new_review_submissions="$(printf '%s' "$activity_json" \
    | jq --arg t "$push_cutoff" \
        '[(.data.repository.pullRequest.reviews.nodes // [])[] | select(.createdAt > $t)] | length' 2>/dev/null)"
  new_inline_comments="$(printf '%s' "$activity_json" \
    | jq --arg t "$push_cutoff" \
        '[(.data.repository.pullRequest.reviewThreads.nodes // [])[].comments.nodes[]? | select(.createdAt > $t)] | length' 2>/dev/null)"
  unresolved_threads="$(printf '%s' "$activity_json" \
    | jq '[(.data.repository.pullRequest.reviewThreads.nodes // [])[] | select(.isResolved==false)] | length' 2>/dev/null)"
  new_issue_comments="${new_issue_comments:-0}"
  new_review_submissions="${new_review_submissions:-0}"
  new_inline_comments="${new_inline_comments:-0}"
  unresolved_threads="${unresolved_threads:-0}"
fi

total_new=$(( new_issue_comments + new_review_submissions + new_inline_comments ))

# Terminal state: nothing new since the push AND no unresolved threads.
# Quiet -- automated loops read this as "done".
if [ "$total_new" -eq 0 ] && [ "$unresolved_threads" -eq 0 ]; then
  log "  ✓ no new review activity since last push, no unresolved threads — done."
  exit 0
fi

log ""
log "════════════════════════════════════════════════════════════"
log "  ▶ Next step for Claude Code (autonomous loop)"
log "════════════════════════════════════════════════════════════"
log ""
log "PR #${pr_number} has activity since our push cut-off"
log "  ${push_cutoff}"
log "  • new top-level comments:   ${new_issue_comments}"
log "  • new review submissions:   ${new_review_submissions}  (each may be APPROVE / REQUEST_CHANGES / COMMENT)"
log "  • new inline thread replies: ${new_inline_comments}"
log "  • unresolved review threads: ${unresolved_threads}  (any age)"
log ""
log "An APPROVE verdict from the review bot does NOT mean there is nothing to"
log "do — bots are LLMs, their wording is non-deterministic, and they often"
log "approve while still flagging real issues as non-blocking. Read every new"
log "comment and every unresolved thread, then decide on the merits."
log ""
log "Step 1. Fetch the current review surface:"
log ""
log "    # Top-level issue-style comments (often where the review bot posts)"
log "    gh pr view ${pr_number} --json comments \\"
log "        --jq '.comments[] | {author: .author.login, createdAt, body}'"
log ""
log "    # Formal Review submissions (APPROVE / REQUEST_CHANGES / COMMENT)"
log "    gh api graphql -f query='"
log "      query(\$owner:String!, \$repo:String!, \$pr:Int!) {"
log "        repository(owner:\$owner, name:\$repo) {"
log "          pullRequest(number:\$pr) {"
log "            reviews(last:20) { nodes { author { login } state body submittedAt } }"
log "          }"
log "        }"
log "      }' \\"
log "      -F owner=\"${owner}\" -F repo=\"${repo}\" -F pr=${pr_number}"
log ""
log "    # Inline review threads with resolved state and per-thread comments"
log "    gh api graphql -f query='"
log "      query(\$owner:String!, \$repo:String!, \$pr:Int!) {"
log "        repository(owner:\$owner, name:\$repo) {"
log "          pullRequest(number:\$pr) {"
log "            reviewThreads(first:100) {"
log "              nodes {"
log "                isResolved"
log "                path"
log "                line"
log "                comments(first:50) { nodes { author { login } body createdAt } }"
log "              }"
log "            }"
log "          }"
log "        }"
log "      }' \\"
log "      -F owner=\"${owner}\" -F repo=\"${repo}\" -F pr=${pr_number}"
log ""
log "Step 2. For each item, judge on the merits — do NOT just chase APPROVE:"
log "  - Open the cited file:line and read the surrounding code yourself."
log "  - Decide: is the concern real? (bots produce false positives — that's"
log "    fine, but the decision must be backed by reading the code, not by"
log "    trusting the verdict label)"
log "  - If real: it's a work item, whether the bot called it blocking or not."
log "    We do not ship technical debt just because a reviewer waved it through."
log "  - If a false positive: leave it; do not fabricate a fix to placate."
log ""
log "Step 3. Fix every real item:"
log "  - Apply the smallest correct change; do NOT bundle drive-by refactors."
log "  - Run the local validations the original reviewer would have run"
log "    (golangci-lint / go test / make all for cross-cutting changes)."
log "  - Commit per the standard isolation rules (高频单域提交,见"
log "    docs/design/engineering.md):one logical fix = one commit。"
log ""
log "Step 4. Push again:"
log ""
log "    git push"
log ""
log "  The push re-enters this hook chain. CI will be re-watched and this"
log "  block will reappear if any further activity appears after the new HEAD."
log ""
log "Termination model:"
log "  This repo has an active agentic-pr-review bot that posts a new comment"
log "  after every push. That comment's createdAt is necessarily > push_cutoff,"
log "  so total_new ≥ 1 on every cycle and the script's"
log "      ✓ no new review activity since last push ... done."
log "  literal terminal is **practically unreachable** while the bot is active."
log ""
log "  Termination is therefore Claude's call, not a script signal:"
log "    1. Read the bot's latest comment (it's the new top-level comment that"
log "       triggered this block)."
log "    2. If every flagged item is addressed OR a false positive, STOP PUSHING"
log "       — the PR is ready. Tell the user."
log "    3. If new real items appear, loop back to Step 2 above."
log ""
log "  Only if the bot is skipped (docs-only / paths-ignore hit) will the"
log "  literal ✓ done line actually fire; do not rely on it as the sole stop"
log "  signal."
log ""
log "Current state: push_cutoff=${push_cutoff}, new_total=${total_new}, unresolved_threads=${unresolved_threads}, reviewDecision=${review_decision}"
log "════════════════════════════════════════════════════════════"
exit 75
