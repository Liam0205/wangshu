#!/bin/bash
# check-pr-ci.sh — 阻塞等当前分支的 PR CI 跑完,然后报告。
#
# Exit codes:
#   0  所有 CI checks 过 AND 自上次 push 起无新 review 活动 AND 无未解决 review
#      thread —— terminal state。
#   1  一项或多项 CI checks 失败(失败 job 在 stderr 列出)。
#   2  当前分支没找到 PR,或 gh 不可用(调用方 hook 当非致命处理,本脚本独立
#      报这个码以便区分)。
#   75 CI 过了,但自上次 push 起有新 review 活动 / 未解决 review thread。退出
#      前的 instruction block 说明下一步该干什么。这里 surface 成非零独立码,
#      让调用方 hook 让 OUTER push 失败,让只看 exit status 的工具(比如 agent
#      loop)不会把 stderr 指令当 silent context 漏掉。
#
# 本脚本**不 push**。它只查已经 push 过的分支对应 PR 的状态。
set -uo pipefail

log() { echo "$@" >&2; }

if ! command -v gh >/dev/null 2>&1; then
  log "post-push: gh CLI not found — skipping CI check."
  exit 2
fi

# jq 用来 parse check 列表。gh 自带 --jq filter,但下面还有独立 jq parse;
# 缺 jq 会静默漏掉失败(false green),所以缺则致命退出。
if ! command -v jq >/dev/null 2>&1; then
  log "post-push: jq not found — cannot reliably parse CI checks. Failing safe."
  exit 1
fi

branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null)"

# Lock 「新活动」的时间截止点 NOW,在花数分钟看 CI **之前** —— 任何 createdAt >
# 此刻的 GitHub 评论严格晚于我们最近一次 push。提前锁(不放 CI watch 之后)
# 这样 CI 期间到达的合法 review 活动仍算新。UTC('Z')与 GitHub UTC createdAt
# 字段对齐;本地 tz offset 如 "+08:00" 会对 'Z' 字面比较错乱。
push_cutoff="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 找当前分支对应的 PR。没就没什么可看。
pr_number="$(gh pr view --json number --jq '.number' 2>/dev/null)"
if [ -z "$pr_number" ]; then
  log "post-push: no open PR for branch '$branch' yet — skipping CI check."
  log "post-push: (open a PR, then CI status will be watched on the next push)"
  exit 2
fi

log "post-push: watching CI for PR #${pr_number} (branch '$branch')..."

# 刚 push 完,GitHub 需要几秒才会为新 head commit 注册 check run。先 poll 直到
# 出现至少一项 check(或放弃),否则我们会把「还没创建」误判成「没 CI」。
# Env 可调:
#   WANGSHU_PREPUSH_CHECK_POLL_TRIES    (默认 24)
#   WANGSHU_PREPUSH_CHECK_POLL_INTERVAL (默认 5,秒)
poll_tries="${WANGSHU_PREPUSH_CHECK_POLL_TRIES:-24}"
poll_interval="${WANGSHU_PREPUSH_CHECK_POLL_INTERVAL:-5}"
for _ in $(seq 1 "$poll_tries"); do
  cnt="$(gh pr checks "$pr_number" --json state --jq 'length' 2>/dev/null)"
  if [ -n "$cnt" ] && [ "$cnt" -gt 0 ]; then
    break
  fi
  sleep "$poll_interval"
done

# 阻塞等所有 checks 跑完。--fail-fast 任一失败立即返回。这里故意忽略 watch
# exit code,最终 verdict 重新由 --json query 算 —— 报告逻辑只有一份源。
gh pr checks "$pr_number" --watch --fail-fast --interval 15 >/dev/null 2>&1 || true

# 重新拉一次所有 checks 的快照。
checks_json="$(gh pr checks "$pr_number" --json name,state,bucket,link 2>/dev/null)"
if [ -z "$checks_json" ]; then
  log "post-push: could not read CI checks for PR #${pr_number}."
  exit 2
fi

# 收集所有失败 job(bucket == fail 或 cancel)作 "name<TAB>link" 行列表。jq
# 强制依赖(上面已检),保持依赖最小化,避免某解释器缺失时静默返回空。
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

# ── instruction block for Claude Code ──────────────────────────────────────
# 当本脚本在 Claude Code background bash 任务里跑时,以上 stderr 全部会在任务
# 结束时回灌到主对话。下面这段是写给 Claude 直接消费的:用具体的 gh 命令拼出
# 下一步循环(拉 review 评论 → 逐条理解 → 改真问题 → 再 push),省掉「让人去
# address review feedback」的回合。
#
# 触发模型(故意只看信号,不看语义):
#   我们**不去 grep** review bot 输出里有没有 "APPROVE" 字样。bot 自己也是 LLM,
#   措辞非确定性,即便 APPROVE 也常带「非阻塞」建议,我们仍要评估。所以脚本
#   只判定「自当前 HEAD push 之后是否有任何新东西」,实际判断(真问题 vs 假阳性、
#   blocking vs nit)交给 Claude。
#
# 出指令 block 的条件(任一为真):
#   - 自 HEAD push 时刻起的新顶层 issue 评论
#   - 自 HEAD push 时刻起的新 inline review 评论
#   - 自 HEAD push 时刻起的新 review 提交(APPROVE / CHANGES_REQUESTED 事件
#     本身就算新活动)
#   - 任何未解决 review thread(不限年龄 —— 老的未解决 thread 仍是 loop 没
#     合上的债务)
#
# 以上都没有就静默 exit 0 + 单行成功,自动循环读作「done」。

owner="$(gh repo view --json owner --jq '.owner.login' 2>/dev/null)"
repo="$(gh repo view --json name --jq '.name' 2>/dev/null)"

# 单次 GraphQL round-trip 拉齐「自 push 后新活动」检测要用的全部数据。每列
# 独立按 createdAt > $push_cutoff(脚本开始时锁好的,CI watch 之前)在 jq 过滤。
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

# 两种「读不到活动」情况:
#   (a) gh 完全没 stdout(网络断 / gh 坏 / 无 auth)
#   (b) gh 返回 GraphQL 错误信封 `{"errors":[...]}` + exit 0 —— auth 过期 /
#       transient 5xx / schema mismatch 常见形态。信封非空,所以 `[ -z ]` 不
#       够;不加这个分支下面的 jq 过滤会在 `.data == null` 上炸 "Cannot iterate
#       over null",四个 counter 全空,后面 `[ -eq ]` 漏出 "integer expression
#       expected"。
# 两个 path 都要 surface 成「work remains」,让 next-step block 触发、运营方手
# 工看 PR —— 静默把畸形信封当 clean terminal 会把自治 loop 毁掉。
activity_unreadable=0
if [ -z "$activity_json" ]; then
  activity_unreadable=1
elif ! printf '%s' "$activity_json" | jq empty >/dev/null 2>&1; then
  # gh 非空 stdout 但不是合法 JSON(auth interstitial、captive-portal HTML、
  # transient gateway error page 等)。没这个 guard 下面 `jq -e has("errors")`
  # 自己也 parse 失败,elif 为 false,会落到「可读」分支,每个 counter 经
  # `${var:-0}` 缩为 0 —— 伪装成 clean terminal "done"。
  activity_unreadable=1
elif printf '%s' "$activity_json" \
       | jq -e 'has("errors") or .data == null' >/dev/null 2>&1; then
  activity_unreadable=1
fi

if [ "$activity_unreadable" -eq 1 ]; then
  log "  ! could not query PR activity — assuming work remains, see comments manually."
  # 强制让 next-step block 触发(total_new > 0),让运营方手工看 PR,而不是
  # 脚本静默 exit 0。
  new_issue_comments=1
  new_review_submissions=0
  new_inline_comments=0
  unresolved_threads=0
else
  # `// []` 把缺失或 null 的 `nodes` 缩成空数组,jq 在部分响应下仍保 total;
  # 内层 inline-comments traversal 上的 `?` 容忍 thread node 没 comments。
  # `2>/dev/null` + `${var:-0}` 让四个 counter 在任何意外下保持数值,后面算
  # 术与 `[ -eq ]` 不会落到空 operand。
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

# Terminal state:push 之后没有新东西 AND 没有未解决 thread。静默 —— 自动 loop
# 读作「done」。
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
log "Termination: when this script next prints"
log "    ✓ no new review activity since last push, no unresolved threads — done."
log "the loop is done. Stop pushing and tell the user the PR is ready."
log ""
log "Current state: push_cutoff=${push_cutoff}, new_total=${total_new}, unresolved_threads=${unresolved_threads}, reviewDecision=${review_decision}"
log "════════════════════════════════════════════════════════════"
exit 75
