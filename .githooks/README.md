# .githooks

仓库 Git hooks(设计:`docs/design/engineering.md` §2)。

## 安装(一次性)

```bash
make hooks    # 即 git config core.hooksPath .githooks
```

## 各 hook 职责

| hook | 时机 | 做什么 | 量级 |
|---|---|---|---|
| `pre-commit` | 提交前 | staged Go 文件 gofmt 检查 | 秒级 |
| `commit-msg` | 提交时 | 强制 `type(scope): subject` 格式 | 毫秒级 |
| `pre-push` | 推送前 + 推送后 | ① 全仓 golangci-lint + go vet;② self-wrapping inner push;③ 阻塞等 CI + 抓 PR 新评论活动 | 推送前十秒级 + CI 等待 |

- 所有 hook 在 CI 环境(`$CI`/`$GITHUB_ACTIONS`)自动短路。
- 完整测试/差分/基准**不在 hook 里跑**——那是 CI 的事(engineering.md 原则 2)。
- 紧急跳过:`git commit --no-verify` / `git push --no-verify`(仅紧急情况)。

## `pre-push` self-wrapper 详解

git 无原生 post-push hook。`pre-push` 模拟办法:

1. **OUTER push 进入 hook**:跑 lint → lint 过后从 stdin 读 refspec → 用 `GIT_POSTPUSH=1` 标记跑一次 **INNER push**(逐字镜像 refspec,force/tag/delete/multi-ref 都正确)
2. **INNER push 重入 hook**:看到 `GIT_POSTPUSH` 标记从顶部直接 `exit 0` 放行
3. **控制流回 OUTER**:调用 `scripts/check-pr-ci.sh` 阻塞等 CI 结果 + 抓 PR 评论活动
4. **OUTER push 紧接着会失败**(原子 ref 保护 / 连接被掐),这是**预期且无害**的——push 已经经 INNER 成功。真正的 verdict 看 `pre-push` 的 stderr ✓/✗ 报告

**为什么这么设计**:让 `git push` 单条命令在 background bash(Claude Code agent loop)里完成「lint → push → CI watch → 抓 review 评论」全链路,不用上层拆步。`check-pr-ci.sh` 在 stderr 打的 instruction block 直接喂回主对话,Claude 可以 autonomous 跑下一轮 review-fix 循环。

`check-pr-ci.sh` exit codes:
- `0` —— CI 全过 + push 后无新 review 活动 + 无未解决 thread(terminal)
- `1` —— CI 一项及以上失败
- `2` —— 没找到 PR / gh 不可用(`pre-push` 当非致命放行)
- `75` —— CI 过,但有新 review 活动或未解决 thread(`pre-push` 接到后 exit 1,迫使上层 agent 看 stderr 而非静默 0 通过)
