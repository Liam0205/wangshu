# 工程化机制:Git hooks / CI / 任务入口 / 发布

> 状态:**设计阶段,可实现深度(跨阶段)**。本文定义仓库级工程化机制:`.githooks/` 三件套、
> GitHub Actions workflow 集、Makefile 任务入口、lint 工具链、官方 oracle 的 CI 供给、发布纪律。
> 与 [12-testing-difftest](./p1-interpreter/12-testing-difftest.md) 的分工:**12 定「测什么、什么必过」
> (门禁逻辑),本篇定「仓库机制怎么搭」(门禁的载体)**。12 §8 的 CI 门禁五步在本篇 §3.1 落为
> 具体 workflow job。
> 主要借鉴 [Liam0205/pineapple](https://github.com/Liam0205/pineapple) 的实践(同作者多语言
> monorepo,hooks/CI/nightly-fuzz 体系成熟),并补强其四处已知缺口(经评审定稿):
> **`-race` 进硬门禁、commit-msg 强制校验、Makefile 任务入口、nightly fuzz 自动开 issue**。
> 完成时机:**M0(工程地基,先于 M1 arena)**——见 [00-overview](./p1-interpreter/00-overview.md) §2。

对应仓库路径:`.githooks/`、`.github/workflows/`、`Makefile`、`.golangci.yml`、`scripts/`。

---

## 0. 设计原则

1. **门禁逻辑与机制载体分离**:12 号文档回答「差分 fuzz 必须零未豁免差异」;本篇回答「这条门禁跑在哪个 workflow 的哪个 job、PR 与 nightly 怎么分工」。改门禁逻辑改 12,改机制改本篇。
2. **本地 hooks 快、CI 全**:pre-commit 只查 staged 文件(秒级),pre-push 全仓 lint(十秒级),完整测试/差分/基准全部交给 CI——开发者本地循环不被长任务阻塞。
3. **hook 在 CI 环境短路**:所有 hook 开头检测 `CI`/`GITHUB_ACTIONS` 环境变量即退出,避免 CI 里重复执行(CI 有自己的 job)。
4. **失败信息可执行**:hook 拦截时打印精确的修复命令(`fix: gofmt -w <files>`),而非只报"failed"。
5. **单一任务入口**:一切任务经 `make <target>`,CI 与本地跑同一套 target——消除"本地过了 CI 挂"的环境分叉(pineapple 用散装 `scripts/*.sh`,发现性差,是其已知缺口)。

---

## 1. Makefile 任务入口

```makefile
# Makefile(仓库根)——唯一任务入口,CI 与本地共用。
.PHONY: all fmt lint test bench-test race cover fuzz bench conformance difftest hooks tidy

all: fmt lint test fuzz conformance difftest bench-test      ## 默认:提交前本地全检(主模块 + benchmarks 子模块)

fmt:                                                ## 格式化(写回)
	gofmt -w $(shell git ls-files '*.go')

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

test:                                               ## 全部单测(含 race,见 §3.1)
	go test -race ./...

cover:                                              ## 覆盖率(输出 coverage.out + 终端摘要)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

fuzz:                                               ## 全部 fuzz 目标各跑一轮冒烟(自动发现 func Fuzz*)
	./scripts/go-fuzz.sh 30s

conformance:                                        ## conformance 套(12 §2)
	go test ./test/conformance/...

difftest:                                           ## 一轮固定时长差分 fuzz(12 §3;需本机有 lua5.1,见 §4)
	go test ./test/difftest/ -run TestDifferential -fuzztime=60s

bench:                                              ## 三档基准(12 §6)
	go test -bench=. -benchmem -count=1 -run='^$$' ./benchmarks/...

hooks:                                              ## 安装 git hooks(一次性)
	git config core.hooksPath .githooks
	@echo "hooks installed: $$(git config core.hooksPath)"

tidy:
	go mod tidy && git diff --exit-code go.mod go.sum
```

- 借鉴 pineapple 的 `go-fuzz.sh`(grep 自动发现 `^func Fuzz` 目标逐个跑),但入口统一挂 Makefile。
- `make hooks` 替代 pineapple 的"README 一行指引"——新人 clone 后 `make hooks` 一步完成,README 与 [00-overview](./p1-interpreter/00-overview.md) 都指向它。
- **`make all` 是「本地提交前全检」**——七件套含 `fuzz / conformance / difftest` 全部跑一遍(耗时 ~2-3 min),目的是把 nightly 才跑的强度拉到本地强制,与 CI 门禁口径对齐。日常小改动若不想每次等三分钟,用 `make test` 跑主模块 race + `make fmt lint` 即可;commit/push 前再过一次 `make all`。

### 1.1 fuzz seed 纪律(`make fuzz` 兜底机制)

`scripts/go-fuzz.sh <fuzztime>` 给每个 fuzz target 一个 `-fuzztime` wall-clock 上限,**Go fuzz 框架内部用 `context.WithTimeout` 实现**。到点后的 deadline 错误正常应被框架抑制(`internal/fuzz` 的 `err == fuzzCtx.Err()` 检查),但 context 取消传播是「先关父 done channel、后 cancel 子 context」,存在一个竞态窗口:coordinator 在窗口内观察到 done 时,抑制检查失败,deadline 逃逸成 `--- FAIL: FuzzX: context deadline exceeded` 假失败(golang/go#75804,上游修复未合入;本仓 issue #63,机制级复现见该 issue)。`go-fuzz.sh` 对此做条件重试:失败输出含 deadline 字样**且无 `Failing input written to` crasher 落盘**才判为假失败重试一次;真 crasher(必伴随 crasher 落盘行)立即如实失败,重试后再挂也如实失败——不掩盖真反例。

另一类 wall-clock 相关 false alarm 来自 seed 本身:

**纪律**:fuzz seed 不应包含「靠 `SetStepBudget` 兜底的近无限循环」(如 `while true do end` / `for i = 1, 1e9 do end`)——`SetStepBudget` 的 budget 计费按指令数,跟 fuzz 框架的 wall-clock 不同步。当解释器跑完 budget 的 wall-clock 量级接近 `-fuzztime`(秒级到十秒级)时,CI runner 慢一点就触发 false alarm。

**正解**:循环 seed 把上限改到 budget 兜住的 wall-clock 量级以下。`SetStepBudget(1<<20)` 下,`1e5` 是稳定毫秒级触发,**`1e6` 在 fuzz 引擎变体下仍观察到「0/sec 拖尾」**——保守取 `1e5`。被破除的「测真无限循环不挂宿主」语义其实由 fuzz 引擎随机变体自身覆盖——seed 只是入口,引擎会生成远超 budget 的循环变体,无需手写无限循环。

**首次踩坑**:v0.1.0 时 `fuzz_test.go` 有 `while true do end` + `for i=1,1e9 do end` 两 seed,CI 偶发 fail(`gh run 27414570482` v0.1.0 tag push 的失败);本地与 master push 无复现。修复见对应 commit。

---

## 2. Git hooks(`.githooks/`)

安装:`make hooks`(即 `git config core.hooksPath .githooks`)。目录含 `README.md` 说明 + 三个 hook。所有 hook 开头:

```bash
# CI 环境短路(原则 3)
if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ]; then exit 0; fi
```

### 2.1 pre-commit:staged-only 格式检查(秒级)

```bash
#!/usr/bin/env bash
set -euo pipefail
mapfile -t staged < <(git diff --cached --name-only --diff-filter=ACM -- '*.go')
[ ${#staged[@]} -eq 0 ] && exit 0
unformatted=$(gofmt -l "${staged[@]}")
if [ -n "$unformatted" ]; then
    echo "✗ gofmt 未通过:"
    echo "$unformatted"
    echo ""
    echo "fix: gofmt -w $unformatted && git add $unformatted"
    exit 1
fi
```

- **只查本次暂存的 Go 文件**(快);工具缺失静默跳过(不绑架无 Go 环境的文档提交)。
- 失败打印**可直接复制执行**的修复命令(原则 4,pineapple 一样的)。

### 2.2 commit-msg:`type(scope):` 强制校验 + 标题 ASCII-only(经评审纳入;pineapple 缺口补强)

```bash
#!/usr/bin/env bash
# 校验 conventional commits:type(scope): subject 或 type: subject
# type 枚举与本仓库既有提交史一致(git log 全部为此格式)。
# 同时校验标题为纯 ASCII(英语策略,2026-06-29 起本项目 commit subject
# 英语化;见用户记忆 feedback_code_language_english)。
msg=$(head -1 "$1")
pattern='^(feat|fix|doc|docs|test|chore|perf|refactor|ci|bench|build|revert)(\([a-z0-9/_.-]+\))?: .+'
# merge/revert/fixup 的自动消息放行
case "$msg" in Merge\ *|Revert\ *|fixup!\ *|squash!\ *) exit 0;; esac
if ! [[ "$msg" =~ $pattern ]]; then
    echo "✗ commit message 不符合 'type(scope): subject' 格式:"
    echo "    $msg"
    echo "允许的 type:feat fix doc docs test chore perf refactor ci bench build revert"
    echo "示例:doc(p1): clarify IC invalidation rules"
    exit 1
fi
# ASCII-only 校验(tr -d 删 ASCII 字节,剩余即非 ASCII,POSIX 可移植 BSD+GNU)
non_ascii=$(LC_ALL=C printf '%s' "$msg" | LC_ALL=C tr -d '\000-\177')
if [ -n "$non_ascii" ]; then
    echo "✗ commit subject 含非 ASCII 字符(英语策略):"
    echo "    $msg"
    echo "Non-ASCII bytes (raw): $non_ascii"
    exit 1
fi
```

- pineapple 纯靠习惯(其提交史 `type(scope):` 高度一致但无强制);望舒**强制校验**——对 agent 提交工作流尤其友好(机器生成的 message 偶发跑偏,hook 立刻拦)。
- scope 建议(非强制枚举):`p1`..`p5`、包名(`arena`/`crescent`/...)、`llmdoc`、`ci`。
- **ASCII-only 标题**:2026-06-29 起本项目 commit subject 英语化(用户记忆 `feedback_code_language_english`);hook 立刻拦截 CJK / em-dash / `§` 等非 ASCII 字符。body 不限制(可含 URL / 日志片段 / 等)。

### 2.3 pre-push:全仓 lint(十秒级)

```bash
#!/usr/bin/env bash
set -euo pipefail
echo "pre-push: golangci-lint run ./..."
if ! golangci-lint run ./...; then
    echo "✗ lint 未通过;fix 后重新 push(跳过:git push --no-verify,仅紧急情况)"
    exit 1
fi
go vet ./...
```

- **不在 pre-push 跑完整测试/差分**(原则 2):那是 CI 的事,push 应在十秒级完成。
- **不采纳** pineapple 的 self-wrapping post-push CI watch(hook 内部代理真实 push 再阻塞等 CI):它使外层 `git push` 退出码不可信,移植成本与心智负担高;望舒用 `gh run watch` / PR 页面替代,需要时由 agent 工作流显式调用。

---

## 3. CI workflows(`.github/workflows/`)

### 3.1 `ci.yml`:每 PR / push master 的门禁(12 §8 的机制载体)

```yaml
name: ci
on:
  push: { branches: [master], tags: ['v*'] }
  pull_request:
paths-ignore: ['docs/**', 'llmdoc/**', '.claude/**', '**.md']
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: ${{ github.ref != 'refs/heads/master' }}

jobs:
  lint:            # golangci-lint(与 pre-push 同配置,双保险)
    - uses: golangci/golangci-lint-action@v9

  test:            # 单测 + race + 覆盖率(12 §8 步骤 1)
    - run: go test -race -coverprofile=coverage.out -covermode=atomic ./...
    - uses: actions/upload-artifact   # coverage.out,retention 30 天
    # -race 是硬门禁(经评审定稿;pineapple 缺口补强):
    #   P1 解释器单 goroutine,race 主要拦「宿主并发误用 State」与测试自身的并发 bug;
    #   P3+ 跨层后价值更大。CI 时间翻倍可接受(P1 单测规模小)。

  conformance:     # 12 §8 步骤 2:差分测试 golden
    - run: make conformance

  difftest:        # 12 §8 步骤 3:固定时长差分 fuzz(三方比对 + GC 压力)
    - run: sudo apt-get install -y lua5.1 && ./scripts/check-oracle.sh   # §4 oracle 供给
    - run: make difftest
    - if: failure()
      uses: actions/upload-artifact   # 最小化复现(12 §3.6)

  fuzz-smoke:      # Go 原生 fuzz 目标各 30s 冒烟(词法/解析等包内 Fuzz*)
    needs: test
    - run: make fuzz

  bench:           # 12 §8 步骤 4:基准回归(三档 ≥2x + 不回退)
    - run: make bench | tee bench.txt
    - run: ./scripts/bench-gate.sh bench.txt    # ≥2x 判据 + 对照基线阈值
    - run: cat bench.txt >> "$GITHUB_STEP_SUMMARY"

  golden-diff-guard:   # 12 §8:golden / 豁免清单改动高亮
    if: github.event_name == 'pull_request'
    - run: |
        git diff --name-only origin/${{ github.base_ref }} -- \
          'test/conformance/golden/**' '**/exemptions*' | tee changed-golden.txt
        # 非空时在 PR 评论/STEP_SUMMARY 高亮,要求 reviewer 显式确认(防改 golden 掩盖真 bug)
```

要点:

- **job 与 12 §8 五步一一对应**(单元/conformance/差分/基准/golden 高亮);dispatch A/B(12 §8 步骤 5)只在改 dispatch 的 PR 手动触发,不进默认矩阵。
- `paths-ignore` 让纯文档 PR 不烧 CI 时长(本仓库文档提交占比高)。
- `concurrency` 非主干 cancel-in-progress(pineapple 一样的)。
- Go 版本经 `go-version-file: go.mod` 锁定,缓存 `go.sum`。

### 3.2 `nightly-diff-fuzz.yml`:长跑差分 + 自动开 issue(经评审纳入,pineapple 最有价值的可借鉴项)

```yaml
on:
  schedule: [{ cron: '0 21 * * *' }]    # 北京时间 05:00
  workflow_dispatch:
    inputs: { rounds: { default: '10000' } }

jobs:
  diff-fuzz:
    timeout-minutes: 350
    steps:
      - run: ./scripts/check-oracle.sh
      - run: go test ./test/difftest/ -run TestDifferential -fuzztime=6h | tee fuzz.log
      - name: triage
        if: always()
        run: |
          # 解析 fuzz.log:统计 FAIL(真分歧)/ INFRA(oracle 不可用等环境失败),分流处理
          ./scripts/fuzz-triage.sh fuzz.log
      - name: file divergence issue
        if: env.DIVERGENCE == 'true'
        run: |
          # 打包分歧脚本 + 三方输出 + 最小化复现(12 §3.6)为 artifact,
          # gh issue create --title "difftest divergence: <摘要>" \
          #   --body "<最小化脚本 + 望舒/官方/gopher 三方输出 + 本地复现命令(make difftest FUZZ_SEED=...)>"
      - name: file infra issue        # 环境失败与真分歧分流,不混淆信号
        if: env.INFRA_FAIL == 'true'
        run: gh issue create --title "difftest infra failure" --label ci
```

- **PR 门禁防回归,nightly 长跑拓新**(12 §8 已定分工);nightly 撞出的分歧经最小化回流为 conformance 用例(12 §3.6),回归不依赖再次随机撞中。
- **自动开 issue 含本地复现步骤**——分歧不靠人盯 Actions 页面;与本仓库已导入的 `patrol`/`bug-analyze` agent skills 衔接(issue 即工单)。

### 3.3 `nightly-benchmark.yml`:基准漂移监控

```yaml
on: { schedule: [{ cron: '30 21 * * *' }], workflow_dispatch: }
# 跑三档基准 → gh run download 上次成功 run 的 bench artifact → delta 对比脚本 →
# 漂移超阈值(如 ±5%)写 STEP_SUMMARY 并开 issue;跑前停无关服务降噪(pineapple 一样的)。
```

PR 的 bench job 防"单次回退",nightly 防"温水煮蛙式缓慢劣化"(每次 PR 都在阈值内、累计漂移超标)。

### 3.4 `release.yml`:发布(轻量)

望舒是 Go 库(+ 可选 CLI `cmd/wangshu`),Go 库经 module proxy 自动分发,**无需发布产物**;release 流程只做:

1. tag 触发(`v*`)→ CI 全绿是前置(`workflow_run`)→ `gh release create` 附 changelog。
2. **tag 纪律**(借鉴 pineapple `tag-release.sh`):release tag **不可静默移动**——脚本先 `git ls-remote` 检查远端同名 tag,存在即拒绝;push 后再次 `ls-remote` 验证落点(不信任 push 退出码)。
3. 版本号若出现在源码常量(如 `_VERSION` 之外的 `wangshu.Version`),tag 前校验一致。

### 3.5 agentic workflows(预留)

pineapple 有 PR 自动 review 与 llmdoc 每日更新的 reusable workflow;本仓库已导入对应 skills(`.claude/skills/`:`pr-review`/`update-llmdoc`/`patrol` 等)。workflow 文件在 M0 仅留占位注释,接入时机与模板源另定(记 §7 缺口)。

---

## 4. 官方 oracle 的 CI 供给(12 §2.6 锁版本的机制兑现)

12 §2.6 锁定官方 **Lua 5.1.5** 为最终语义 oracle。CI 获取方式:

```bash
# scripts/check-oracle.sh —— difftest 前置校验
# ubuntu-latest: apt 的 lua5.1 包即 5.1.5(多年冻结);校验版本串,不符则 fail-fast。
v=$(lua5.1 -v 2>&1)
case "$v" in *"5.1.5"*) echo "oracle ok: $v" ;;
             *) echo "✗ expected Lua 5.1.5, got: $v" >&2; exit 1 ;; esac
```

- 优先 `apt-get install lua5.1`(快、缓存友好);版本串校验防发行版漂移,不符则 fail(**不静默降级**——oracle 版本错会让差分结论失效)。
- 兜底(apt 不可用/非 Ubuntu runner):源码编译 5.1.5 并以 actions cache 缓存产物,记 §7 缺口待 M14 完成时实测。
- gopher-lua 是 `go.mod` 依赖,版本天然锁定(12 §2.6"锁 commit/tag")。
- 本地 `make difftest` 同样经 `check-oracle.sh`,无 lua5.1 时给出安装指引并跳过(本地可跳,CI 必过)。

---

## 5. lint 工具链

```yaml
# .golangci.yml —— v2 极简起步(pineapple 一样的思路:默认 linter 集 + 噪音排除)
version: "2"
linters:
  exclusions:
    presets: [comments, common-false-positives, legacy, std-error-handling]
```

- **起步极简**(默认 linter 集),P1 实现期按真实噪音增量调整;不预配置大而全规则(规则先于代码是空转)。
- 格式化用 `gofmt`(不引入 gofumpt/goimports 强制——与 pineapple 一致,降低工具链门槛;若实现期 import 分组成为真实痛点再升级,记 §7)。
- 注意:解释器核心(`internal/crescent` 的大 switch、位运算)易触发圈复杂度/魔法数类 linter 误报,豁免**按文件/包**配置而非全局关闭。

---

## 6. M0:工程地基里程碑(对 [00-overview](./p1-interpreter/00-overview.md) §2 的增补)

本篇全部机制在 **M0** 完成(先于 M1 arena),完成定义:

| 项 | 验收 |
|---|---|
| `go.mod` + 目录骨架 | `go build ./...` 过(空包占位) |
| Makefile | `make all`/`make hooks` 可跑 |
| `.githooks/` 三件套 | 本地提交/推送被正确拦截与放行;commit-msg 校验生效 |
| `ci.yml` | lint + test(-race)+ fuzz-smoke 三 job 绿(conformance/difftest/bench job 先建占位,随 M5/M9/M14 启用) |
| `.golangci.yml` | `make lint` 过 |
| `scripts/check-oracle.sh` | 本地与 CI 均可校验 lua5.1 |

> M0 估算 ≤0.25 人月,不改变 [00-overview](./p1-interpreter/00-overview.md) §3 的总人月区间(其 M14 含"CI 门禁就绪"已部分覆盖;M0 把骨架前置,M14 只剩启用全部门禁)。

---

## 7. 不变式与文档缺口

**不变式:**

1. **CI 与本地同入口**:所有 CI job 的核心命令是 `make <target>`,本地可完整复现。
2. **hook 快、CI 全**:pre-commit 秒级(staged-only)、pre-push 十秒级(lint)、测试/差分/基准只在 CI。
3. **`-race` 恒开**(test/cover target 与 CI test job)。
4. **oracle 版本校验 fail-fast**:lua5.1 非 5.1.5 即失败,绝不静默降级。
5. **release tag 不可移动**;golden/豁免清单改动必须显式过 review(12 §8)。
6. **commit message 强制 `type(scope):`**(commit-msg hook + 既有提交史一致)。

**文档缺口(记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md)):**

- nightly-diff-fuzz 的 `fuzz-triage.sh` 解析协议(FAIL/INFRA 分类的精确判据)待 difftest harness(M14)定稿后完成。
- 非 Ubuntu runner 的 oracle 源码编译 + 缓存方案待实测。
- bench-gate 的回退阈值(±N%)与基线 artifact 的存储/对比协议待 M14 校准。
- agentic workflows 的接入时机与模板源(§3.5)。
- 覆盖率是否设硬门槛(当前仅 artifact 存档,pineapple 一样的;若 P1 后期需要再议)。

---

相关:[12-testing-difftest](./p1-interpreter/12-testing-difftest.md)(门禁逻辑) ·
[00-overview](./p1-interpreter/00-overview.md)(M0 里程碑) ·
[architecture](./architecture.md)(目录布局) ·
[multi-doc-drafting](../../llmdoc/guides/multi-doc-drafting.md)(工作流 guide)
