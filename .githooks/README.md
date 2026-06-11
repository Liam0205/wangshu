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
| `pre-push` | 推送前 | 全仓 golangci-lint + go vet | 十秒级 |

- 所有 hook 在 CI 环境(`$CI`/`$GITHUB_ACTIONS`)自动短路。
- 完整测试/差分/基准**不在 hook 里跑**——那是 CI 的事(engineering.md 原则 2)。
- 紧急跳过:`git commit --no-verify` / `git push --no-verify`(仅紧急情况)。
