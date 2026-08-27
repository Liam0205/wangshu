# 工程化机制:Git hooks / CI / 任务入口 / 发布

> 状态:**设计阶段,可实现深度(跨阶段)**。本文定义仓库级工程化机制:`.githooks/` 三件套、
> GitHub Actions workflow 集、Makefile 任务入口、lint 工具链、官方 oracle 的 CI 供给、发布纪律。
> 与 [12-testing-difftest](./p1-interpreter/12-testing-difftest.md) 的分工:**12 定「测什么、什么必过」
> (门禁逻辑),本篇定「仓库机制怎么搭」(门禁的载体)**。12 §8 的 CI 门禁五步在本篇 §3.1 落为
> 具体 workflow job。
> 主要借鉴 [Liam0205/pineapple](https://github.com/Liam0205/pineapple) 的实践(同作者多语言
> monorepo,hooks/CI/nightly-fuzz 体系成熟),并补强其四处已知缺口(经评审定稿):

**每个 workflow 的每个 job 都有 job 级 `timeout-minutes`**,nightly 那个 job 里**每一个**步骤还额外有 step 级 `timeout-minutes`(含两个 `uses:` 步骤:checkout / setup-go 各 10):装 oracle 12、rolling-seed 170、GC-stress 75、auto-mode 170、native go-fuzz 按层 **195/280/280**、oracle-diff go-fuzz 80、upload 15、triage 10;job 上限 350、`gofuzztime` 35m。native go-fuzz 的上限**按层取值**(p1 195、p3/p4 280):后面的步骤都是 `if: always()`,所以要满足的是「前置 + 本步上限 + 后面每一步的上限 < job 上限」,而 p1 的尾巴含 oracle-diff(105)、p3/p4 只有 25。实测三层分别在 340 / 347 / 347 分钟,都在 350 以内。step 上限是**两侧**约束:既要盖过合法最坏情形(p4 是 6 × 35m 再加两次 #75804 重试 = 280),又要**在 job 上限之前触发**(这一步之前约 40 分钟已消耗,所以必须小于 310)—— 窗口是 280 到 310。

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
.PHONY: all fmt lint test-scripts test bench-test race cover fuzz bench conformance difftest hooks tidy

all: fmt lint test-scripts test fuzz conformance difftest bench-test      ## 默认:提交前本地全检(主模块 + benchmarks 子模块)

fmt:                                                ## 格式化(写回)
	gofmt -w $(shell git ls-files '*.go')

lint:                                               ## 全仓静态检查
	golangci-lint run ./...

test-scripts:                                       ## 工具脚本自测(#179 go-fuzz.sh retry-path;#236-#241 lua tarball 取包)
	bash scripts/test-go-fuzz-retry.sh
	bash scripts/test-fetch-lua-tarball.sh

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
- **`test-scripts` 目标**(2026-07-25,issue #179 引入):tooling 类回归脚本必须至少挂一个门禁,否则一次 downstream 改动就能让它静默失效——issue #179 之前 `scripts/test-go-fuzz-retry.sh` 失效期至少半年没人发现。目标本身秒级,同时挂 `make all`(本地 pre-commit)与 `ci.yml` 独立 job(PR 门禁,失败信号清晰、可 rerun),两个门禁同步对它负责。目前两个脚本:`scripts/test-go-fuzz-retry.sh`(验证 `go-fuzz.sh` 对 golang/go#75804 spurious deadline 的重试逻辑)与 **`scripts/test-fetch-lua-tarball.sh`**(2026-08-09,#236-#241 引入,五个用例钉住 `scripts/fetch-lua-tarball.sh` 的取包行为,见 §4)。后者的五个用例**全部离线**(用 `file://` origin 冒充上游),因为它防的正是上游抖动——一个「防住外部抖动」的测试如果自己依赖上游,它加的是噪声不是防线。其中「curl 调用带限时标志」这一条第一版**grep 整个文件、假绿**:把 `--max-time` 从调用里删掉后照旧通过,因为那个词在脚本顶部的注释里还在;现在只截取那条 curl 调用再检查。判据:**检查代码属性的测试要作用在代码本身上,一个注释就能满足的测试没有在测代码**(方法论见 `llmdoc/guides/prove-the-path-under-test.md` §9.1b)。
- `make hooks` 替代 pineapple 的"README 一行指引"——新人 clone 后 `make hooks` 一步完成,README 与 [00-overview](./p1-interpreter/00-overview.md) 都指向它。
- **`make all` 是「本地提交前全检」**——七件套含 `fuzz / conformance / difftest` 全部跑一遍(耗时 ~2-3 min),目的是把 nightly 才跑的强度拉到本地强制,与 CI 门禁口径对齐。日常小改动若不想每次等三分钟,用 `make test` 跑主模块 race + `make fmt lint` 即可;commit/push 前再过一次 `make all`。

### 1.1 fuzz seed 纪律(`make fuzz` 兜底机制)

`scripts/go-fuzz.sh <fuzztime>` 给每个 fuzz target 一个 `-fuzztime` wall-clock 上限,**Go fuzz 框架内部用 `context.WithTimeout` 实现**。到点后的 deadline 错误正常应被框架抑制(`internal/fuzz` 的 `err == fuzzCtx.Err()` 检查),但 context 取消传播是「先关父 done channel、后 cancel 子 context」,存在一个竞态窗口:coordinator 在窗口内观察到 done 时,抑制检查失败,deadline 逃逸成 `--- FAIL: FuzzX: context deadline exceeded` 假失败(golang/go#75804;本仓 issue #63,机制级复现见该 issue)。**上游修复已随 Go 1.27.0 发布**(2026-08-27 核实,#180):`$GOROOT/src/internal/fuzz/fuzz.go` 的抑制检查现在是 `err == ctx.Err() || err == fuzzCtx.Err() || isInterruptError(err)`(升级前只有后两项),紧邻注释逐字引用 `go.dev/issue/75804`,说明「ctx 的 deadline 到期后存在一个窗口:`ctx.Err()` 已置位而子 context `fuzzCtx` 尚未被 cancel」——与本节推断的竞态窗口一致。对照 upstream 同一文件:`go1.26.2` 里 `issue/75804` 出现 **0** 次、`go1.27.0` 出现 **1** 次,所以这是 1.27 新增的修复,而不是 1.27 里本来就有的代码。`go-fuzz.sh` 对此做条件重试:失败输出含 deadline 字样**且无 `Failing input written to` crasher 落盘**才判为假失败重试一次;真 crasher(必伴随 crasher 落盘行)立即如实失败,重试后再挂也如实失败——不掩盖真反例。

**升级到 1.27.0 之后这个条件重试仍然保留**(#180 只做「升 go.mod + 改本节描述」这一步)。理由:它是目前**唯一**区分「真 crasher」与「工具链竞态」的判据,删掉之后一旦判错,代价是**真 bug 被当成噪声重试掉**;而它的触发频率本来就很低(约 450 个 job 里 9 次),所以**本轮那次 30 秒级 fuzz smoke 报 0 次重试并不构成证据** —— 在原来那个频率下,一次这么短的 smoke 本来也极可能是 0 次,这个观察在「修复生效」与「修复没生效」两个假设下给出的是同一个结果。修复真的生效之后,这个数字应当在 nightly 上自然归零 —— **等实测归零再删,就有数据背书而不是靠推断**,这也是 #180 那条「评估并删除」的正确执行顺序(评估的结论是证据还不够,于是这一轮只做升级与文档同步)。方法论见 `llmdoc/guides/prove-the-path-under-test.md` §4.6a;核实上游是否真的修了那一步见 `llmdoc/guides/design-claims-vs-codebase-physics.md` §5.1。

另一类 wall-clock 相关 false alarm 来自 seed 本身:

**纪律**:fuzz seed 不应包含「靠 `SetStepBudget` 兜底的近无限循环」(如 `while true do end` / `for i = 1, 1e9 do end`)——`SetStepBudget` 的 budget 计费按指令数,跟 fuzz 框架的 wall-clock 不同步。当解释器跑完 budget 的 wall-clock 量级接近 `-fuzztime`(秒级到十秒级)时,CI runner 慢一点就触发 false alarm。

**正解**:循环 seed 把上限改到 budget 兜住的 wall-clock 量级以下。`SetStepBudget(1<<20)` 下,`1e5` 是稳定毫秒级触发,**`1e6` 在 fuzz 引擎变体下仍观察到「0/sec 拖尾」**——保守取 `1e5`。被破除的「测真无限循环不挂宿主」语义其实由 fuzz 引擎随机变体自身覆盖——seed 只是入口,引擎会生成远超 budget 的循环变体,无需手写无限循环。

**首次踩坑**:v0.1.0 时 `fuzz_test.go` 有 `while true do end` + `for i=1,1e9 do end` 两 seed,CI 偶发 fail(`gh run 27414570482` v0.1.0 tag push 的失败);本地与 master push 无复现。修复见对应 commit。

**补记(2026-08-04,#224/#225)**:本节两处 `SetStepBudget(1<<20)` 是**当年**的数值,写在这里是为了保留那次校准的推理。**p4 的两个 fuzz target 现在用 `1<<16`**(`internal/fuzzbudget` 的 `fuzzbudget.Steps`,`fuzz_auto_test.go` 与 `fuzz_p4_test.go` 四处共用),理由与本节同源、只是量的对象不同:本节讲的是**单个 seed** 的循环上限要低于 budget 兜住的 wall-clock 量级,#224/#225 讲的是 **budget 自己**允许的字节工作量投射到 CI 之后要低于 go-fuzz 的 10 秒 per-input 看门狗——`1<<20` 允许约 64 MiB concat、本地每子测试 0.7–1.3 秒、CI 慢约 10 倍 ⟹ 12–13 秒 > 10 秒,六个 concat storm 家族 seed 里两个已超、四个余量不到 1.4 倍。**为什么终值是 `1<<16` 而不是减半的 `1<<19`**:`1<<19` 只修好了那六个 seed,而同一预算下最贵的可达写法(`s:gsub("%a","x")` 紧循环)投射到 41 秒、是看门狗的四倍——`gsub` 每次调用按大约两倍主串计费但实际工作远多于等额计费的 concat,**等额计费不等于等额 wall-clock**,所以上限要按「计费相同时最贵的写法」定;`1<<16` 让最坏那条降到 5.2 秒、余量 1.93 倍。判据与算法见 [p1-interpreter/12-testing-difftest.md](./p1-interpreter/12-testing-difftest.md) §4.9a2;引这一节的 `1<<20` 时注明它是历史数值。

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

  test-scripts:    # 工具脚本自测(2026-07-25,issue #179;<5s;与 lint 并列独立 job 便于 rerun)
    - run: make test-scripts

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
  schedule: [{ cron: '0 */4 * * *' }]   # 每 4 小时一轮(2026-07-10 从每日一轮加密)
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
- **调度频率与种子滚动**(2026-07-10 加密):每 4 小时一轮(6 轮/天),滚动种子从天级 epoch 改为**小时级 epoch**(`date +%s / 3600 * 1e7`)——加频的前提,否则同日各轮重复探索同一种子段。单轮墙钟 ~3h40m,极端慢跑与下一轮短暂重叠无害(并发 run 落在不同小时桶、issue 按标题去重),间隔是预算选择而非防重叠约束。加密动因:2026-07 上旬 10 晚中 4 晚撞出真 bug(#97 家族 / #103 / #106 / #107),种子空间远未探干,且 P3/P4 接受面扩张快(#52/#77/#99/#102),每次扩面都是新的探索维度。
- **自动开 issue 含本地复现步骤**——分歧不靠人盯 Actions 页面;与本仓库已导入的 `patrol`/`bug-analyze` agent skills 衔接(issue 即工单)。
- **infra issue 的去重键按 日期,标题里故意不放 `matrix.variant`**(2026-08-09,#236-#241):divergence 与 infra 是两类性质不同的事件,而原先共用一个含 tier 的标题模板。divergence 失败天然属于**某一个 tier**(tier 是它身份的一部分),而 **infra 失败天然横跨所有 tier**(装不上依赖、上游不可达,与被测的是 p1 还是 p4 无关)。标题里嵌 `${{ matrix.variant }}` 让 p1/p3/p4 生成三个不同标题,下面那段按标题去重的逻辑看不出它们是同一件事:**两次上游抖动 × 三个 tier = 六个 issue**。现在 infra 标题键在 `${{ github.run_id }}`(三个 job 共享同一个 run id),第一个报的 job 开 issue、第二三个改为评论;tier 从标题挪进 body 的「首个报告的 tier」与后续评论里——**去重键与信息量是两件事**。判据:给自动化加去重时先问「这类事件的**天然单位**是什么」,再对每一维问「这一维变化时是同一件事还是两件事」,答「同一件事」的维度不能进键;**多加一维只会让分组更细、永远不会报错**,所以这类错误没有任何自动信号,全部代价落在读 issue 的人身上。
- **依赖步骤失败会让后续步骤 skip,于是「报红但什么都没测」**(2026-08-09,#236-#241,**已知缺口**):oracle 安装步骤失败让后面三个差分 fuzz 步骤全部 `skipped`,那一轮报 failure 而实际什么都没跑,那一晚的探索预算是零。这比真失败更坏,因为红色的默认含义是「跑了并且发现了问题」,而两者在 Actions 页面上是同一个红叉。取包已经加了限时与重试(§4.1),但**「本轮未执行任何差分」这句话已经补上(infra issue 的 body 用 `steps.difffuzz.outcome` 写明本轮差分是否执行)**——infra issue 的 body 说的是失败原因的类别,不是「这一轮的探索预算为零」。记 §7 缺口。判据:给 CI 加依赖步骤时问一句「这一步挂了之后,后面被 skip 的步骤里有没有本来该产生结论的」,有的话失败信息里要写清「未执行」而不只是「失败」。

- **step 级超时覆盖每一步,预算按实测成本重配**(2026-08-19,commit `9b61079`,**收口上一条部分缺口**):上一条限时的是取包脚本里的 curl,但装 oracle 那一步还有 `apt-get`/`make`/`check-oracle.sh` 三条命令留在原地无界——**只框住了报错的那一条命令,不是那一整步**。2026-08-12/08-15/08-18 一周内 job 超时掐掉三次,08-18 那次 p1 腿在这一步卡了 5h50m 被掐、几乎丢了整轮覆盖(rolling-seed 与 auto-mode 被 skip、两个 go-fuzz 步骤根本没启动)。普查后发现无界的步骤是**三个不是一个**(装 oracle、upload logs、triage),现在三步各带 step 级 `timeout-minutes`(12/15/10)——选 step 级而不是逐条包 timeout 是因为它对**以后新加的命令同样生效**,这正是上一轮栽的地方。同轮纠正预算:`gofuzztime` 曾建议连续五轮从 150m 压到 90m(auto-mode 的上限),**实测这个建议省不下任何时间**——auto-mode 只跑 2 分钟,150m 从没接近过;真正的成本中心是 native go-fuzz 步骤,312 分钟的 job 里它占 270 分钟(87%),原因是 workflow input description 写的「per target(4 targets)」是陈旧计数,`go-fuzz.sh` 源码扫描发现的无 tag 目标实际是 **6 个**(6×45m=270,与实测吻合)。`gofuzztime` 45m→35m 把这一步降到约 210 分钟、job 约 250 分钟、余量从约 38 分钟升到约 98 分钟,是真实的约 22% 探索量削减,选它而不是砍差分步骤是因为后者只占 job 的一小部分(rolling-seed 36 分钟、auto-mode 2 分钟)。**两条自我纠正**:① 曾提议把 job `timeout-minutes` 抬到 420,**不可能**——GitHub 托管 runner 单 job 硬上限 360 分钟;② 曾按「step 上限之和必须低于 job 上限」算出某 leg 最坏 548 分钟、判定「只能大幅压缩」,**这个框架本身是错的**——job 超时才是预算,step 上限的作用是防一条卡死的命令悄悄吃掉这个预算,两者不是同一件事,不该按求和去配。判据见 `llmdoc/guides/unreproducible-crasher-triage.md`「给一条报错的命令加超时」与「job 超时与 step 超时」两节 + `llmdoc/guides/design-claims-vs-codebase-physics.md` §3.1/§5;反思 [[2026-08-19-nightly-step-timeouts-and-budget]]。
- **cgo 内嵌 oracle 差分 go-fuzz**(2026-07-12,p1 腿专属步骤):`internal/oracle` 把官方 5.1.5 库源码 vendor 进仓(`_lua515/` 下划线目录,Go 工具链忽略;来源 + sha256 记录在该目录 README)并经 cgo 单编译单元(官方 `etc/all.c` 手法)嵌进测试二进制,build tag `wangshu_oracle_cgo && cgo` 隔离——默认 build 与全部既有 variant 保持零 cgo(与 jitcgo 一样的隔离纪律)。价值:滚动种子腿喂的是 generator 的**规整**脚本,`FuzzOracleDiff` 让 go-fuzz 变异**任意不规则源码**在进程内做差分比对(fork 子进程模式撑不起 go-fuzz 的 exec 频率)。比对协议:两侧先跑同一段 Lua prelude(print/io.write 捕获 + os.time/math.random 等确定性 stub + pairs/next 排序迭代 + 从活 wangshu State 枚举生成的 globals 白名单裁剪 oracle 能力面),资源限制(shim 侧 alloc 帽/指令 hook/输出帽,wangshu 侧 step budget/arena 帽)任一触发即 skip(两侧预算机制不同构),实现常数类护栏(语法深度/栈溢出/复杂度上限)任一触发也 skip(阈值名义同为 200 级但触发点差几个输入),其余要求结局 class 一致 + 捕获输出**经地址归一后逐字节相等**(引擎相关的 `table: 0x…`/`function: 0x…`/`thread: 0x…`/`userdata: 0x…` 归一为 `0xADDR`,类型前缀锚定保脚本自己的 `0x1` 字面输出仍逐字节比对——**锚点最终改为「只匹配地址本体 + Go 侧校验前缀」(`\b` 与「消耗一个非字母」两版都失败)**,`\b` 的 word 字符包含数字,`io.write(0)print(print)` 产生的 `0function: 0x...` 会整段逃过归一化,#232;而**地址的宽度**归一化到不了,`#tostring(t)` 在 PUC 是 21、望舒是 17,长度在归一化之前就分歧,所以宽度另在 oracle 的 prelude 渲染处对齐,#233,判据同下文 NaN 一条:归一化管值、渲染处管宽度);`CompareOutput` 就是「归一化地址后逐字节比较」,**没有任何接受差异的路径**。唯一曾经需要这样一条路径的平台差异是 **NaN 符号渲染**:glibc 的 printf 按符号位打 `-nan`,wangshu 打 `nan`,差一个字节,而 IEEE 754 不赋 NaN 符号位数值语义,两种输出都合规。**现在在产生差异的地方消除它**:oracle 是我们自己 vendor 并用 cgo 编译、只为做差分基准而存在的(且已经为确定性 stub 掉 `os.time`/`math.random`/`pairs` 顺序),所以在 `internal/oracle/lua515.c` 里覆盖 `lua_number2str`(覆盖 `tostring`/`..`/`io.write` 的数字渲染)、并对 `lstrlib.c` 的 include 局部 shadow `sprintf`(`string.format` 的 `%e`/`%f`/`%g` 族直接调 `sprintf` 不走 `lua_number2str`),把 NaN 渲染的符号去掉;vendored 源码保持与记录的 sha256 逐字节一致(配置只在 `lua515.c` 与 CFLAGS 里做,这是仓库既有纪律)。三个实现要点:① 保持**字段宽度**——`sprintf` 已经补过 padding,直接删符号会让字段短一位,而 buffer 本身分不清前导空格是 padding(`%5E`)还是空格 flag 的符号(`% E`),所以把 format spec 传进去、按声明宽度决定;② 符号去掉后 glibc 的 `+`/空格 flag 开始作用于 NaN,产生 `+NAN`/` NAN`,所以剥的是任何符号字符而不只是 `-`;③ Inf 保留符号(那个符号有意义),脚本自己写的 `"-nan"` 字面量不改。**曾经的设计与它为何失败**(#173 引入、#184/#185 延伸,已全部删除):旧设计把这个差异当作「不可比的类」,在 harness 侧识别并豁免——prelude 按每次 NaN 渲染事件记录 `__nan_spans` 区间 FIFO + `__oracle_readout()` 多行 header + `CompareOutput` 的 span-anchored 分类(`knownNaNSignDifference`)+ 一整层入口拦截。这条路不可能收敛:那个符号字节一旦进入字符串就是普通数据,`#`、`==`、`string.sub`、`..`、算术能把它搬到任何地方——`string.len(0/0)` 是 4 对 3,`string.len(0/0)*100` 是 400 对 300,输出里连一个 nan 字节都不剩,没有任何可锚定的东西;任何下游识别规则都必须读一个两侧不相等的量,所以都做不到对称。新设计相对旧设计约少 550 行,而且两个 crasher 与整个「泄漏成数字」的家族现在是普通的 equal 而不是 skip,覆盖面是**增加**的(那些输入以前会连同同一次运行里的真差异一起被丢掉)。**保留的两类豁免性质不同**:地址归一(上面的 `0xADDR`)是两个引擎堆布局本来不同、没有「正确值」可对齐,只能在比较时归一;实现常数类护栏(`stack overflow`、`too many syntax levels`、200 local variables 等)是两侧独立选定的实现限制,改 oracle 的常数去凑 wangshu 会让它不再是独立基准,只能 skip。判据:差异如果来自「同一个抽象值的不同书写方式」,在渲染处消除;如果来自「两侧本来就是不同的东西」,在比较时归一或跳过。错误消息文本不比(errmsg 语料的职责)。PR 门禁 `oracle-smoke` job(linux amd64+arm64,shim 单测 + 60s 冒烟);上线首日 fuzz 长时间运行 + 配套 argsweep 系统性扫描(全 stdlib 函数 × 退化参数形状,fork 子进程 oracle 一次性比对)共抓出 **32 处** P1 语义分歧并全部修复(清单见 README 正确性五层第 5 层;其中 gcstress 还顺带抓出 string 元表未接 GC 根的 use-after-free,代码审查轮又收紧了 go-fuzz.sh 探测吞编译错误与 0x 归一化过宽两处 harness 缺陷)。**上线后持续巡检 + 修复分歧**:PR #172(#170/#171,`string.format` 非有限浮点 verb 渲染对齐 glibc)/ PR #176(#174/#175,`toNumberStr` 走 `crescent.ParseLuaNumber` + `tonumber(number, base)` 强制转 string)/ PR #178(#177,P4 shape-template FORLOOP deopt 补 preempt 与 body 副作用)/ **#192/#193/#194/#196 那一轮**(2026-07-28,四个 issue 修出**六个根因**:C99 `nan(n-char-sequence)` 前缀 / `tonumber(x, 10)` 被错误送进逐字符解析循环——一个结构性原因造出六条分歧,其中五条没有任何 issue 记录 / `%d`/`%i` 精度 0 配值 0 丢符号 / `table.insert` 在 5.1 里本来没有边界检查加 `table.concat` 错误文本 / `string.char` 的 `luaL_checkint` 两步转换 / `strtoul` 的无符号取反与溢出饱和;`strtoul` 那处原有一句注释声称「已登记为 diff 豁免」但**没有任何代码实现它**,分歧一直是活的。另有一处 table 库缺 `, got no value` 从句是本分支自己 45 秒 fuzz 冒烟撞出的。判据见 `docs/design/p1-interpreter/12-testing-difftest.md` §4.9b:先分清那个 C 行为是有定义还是 UB,再决定对齐还是跳过;并且「已豁免」的声明必须能指向执行它的代码)/ **NaN 符号渲染在 oracle 侧归一**(承 #170/#171 的一半差异,#173 与 #184/#185 的 harness 侧分类方案已被替换,见上文);同轮把产品侧两处 glibc 模仿也清掉(`internal/stdlib/stringlib.go`:`string.format` 原本对大写 verb 硬编码 `-NAN`,导致 `%e` 与 `%E` 自相矛盾;小写 NaN 原本按「声明宽度减一」补齐以复现 glibc 保留但不显示的符号列,导致 `%5f` 与 `%5E` 补齐方式不一致)——两处都只为让 oracle 一致而存在,而 arm64 的 glibc 与 x86 还不同,模仿本来就不可移植;现在 NaN 在所有 verb 下都不带符号、都按完整声明宽度补齐,wangshu 自身也一致了。nightly-diff-fuzz 对 NaN 符号差异不再有任何豁免:真出现就是真差异。**后续验证**:nightly 在四个不同日期自动开出的 crasher #187/#188/#189/#190(`string.format("%q",0%0)`、`string.format((0))<string.format((0%0))`、`string.format("%q",(0%0))`、`string.format("+% E",-(0%0))`)全部是这同一个根因的表现,被上面的渲染处消除一起解决,一行代码没改;双向验证确认它们在 redesign 之前的 base 上全部 FAIL、在现在的实现上全部以**真正的逐字节 equal** 通过而不是 skip(区别要紧:skip 会把这些输入连同同一次运行里的真差异一起丢掉)。四条 reproducer 加 10 条延伸 seed 入 `testdata/fuzz/FuzzOracleDiff/`(共 73 个),入库理由是它们触到三个已有 seed 到不了的写法:`%q` 作用于 NaN(引用渲染结果而不做浮点格式化,不走 `%e`/`%f`/`%g` 那条 `sprintf` 通道)、比较两个 `string.format` 的**返回值**(差异变成一个 boolean,输出里根本没有 NaN 文本可锚定)、多个符号 flag 同时出现。另扫了 236 + 140 种同族写法(`%q` × 宽度、比较运算符 × format 与 tostring 结果、flag 组合 × 浮点 verb;7 种产生 NaN 的方式 × 20 种消费其文本的方式)确认整个家族零差异。**渲染处消除这个手法在 2026-08-05 第二次被用上(#233)**:`#tostring(t)` 量的是地址的**宽度**(PUC `%p` 12 位给 21、望舒 `0x%08x` 8 位给 17),长度在归一化之前就分歧——与 `string.len(0/0)` 4 对 3 完全一样的机制,所以 prelude 包一层 `tostring` 把 PUC 自己的地址渲染成望舒的 8 位,望舒侧的宽度成为一份契约由 `fuzz_234_test.go::TestAddressLengthIsComparable` 钉住。同一轮的 #232 是归一化的另一条能力边界(`addrRe` 的 `\b` 锚点漏掉「数字紧贴类型名」),#234 是引擎侧 gsub 替换串的 `%` 转义(`add_s` 的分支有三个出口加一个越界读,望舒只对了两个,其中「非数字原样吐出」那一半是既有缺陷);详见 `docs/design/p1-interpreter/12-testing-difftest.md` §4.3a 与 `10-stdlib.md` §6.5.2。**参照实现自己会崩的那一类(2026-08-11,#244)**:差分 harness 把 PUC 拉进同一个进程,所以失败信号可能来自**参照实现**而不是望舒 —— `A(unpack({},0X80000000))` 让内嵌 oracle 吃 SIGSEGV(栈迹在 `_Cfunc_wangshu_oracle_exec` 里)、真的 `lua5.1` 二进制同样 dumped core,而望舒抬 `too many results to unpack` 行为正确。机制是 `luaB_unpack` 的 `n = e - i + 1` 在 int 上溢、`n <= 0` 只拦到回绕成非正的那一半,巨大正值再经 `lua_checkstack` 的 int `size` 二次上溢后被拿去扩栈。处置与其余 PUC UB range 一致:prelude 抬 sentinel **跳过**(会死的 oracle 不能当参照),望舒侧无事可做。**分诊纪律**:差分 harness 报 crash 时先读栈迹属于哪一侧、并拿真的参照实现二进制跑一次,再决定往哪边查;**区间纪律**:那条 skip 的区间要按机制的条件表达式定,现行守卫只读 `i` 而机制读 `i` 与 `e`,两个方向都不对(太窄漏崩、太宽漏比对),记为已知缺口,口径见 `12-testing-difftest.md` §4.9f 与 `10-stdlib.md` §4.5。

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

### 4.1 源码构建那条腿的取包:`scripts/fetch-lua-tarball.sh`(2026-08-09,#236-#241)

源码构建腿原先在四处 call site 各写一遍**裸 curl**(`ci.yml` 的 test 与 difftest 两个 job 的 macOS cache-miss 分支、`bench-acceptance.yml` 的 macOS 分支、`nightly-diff-fuzz.yml` 的 oracle 安装步骤),没有限时、没有重试、没有校验。2026-08-07 上游一次可达性抖动因此让 nightly 连红两轮,自动开出六个 issue(#236-#241)。四处现在统一走 `scripts/fetch-lua-tarball.sh`,四个性质:

| 性质 | 写法 | 为什么 |
|---|---|---|
| **限时** | `--connect-timeout 15 --max-time 120` | 挂住的取包应该快速失败,而不是把整个步骤耗掉(那两次各耗了约 2 分 15 秒才被 shell 放弃) |
| **重试** | `--retry 4 --retry-delay 5 --retry-all-errors` | 观察到的失败是瞬时的。`--retry` 才是关键 —— 它的默认值是 0,所以旧的裸 curl 根本不重试。`--retry-all-errors` 只是额外放宽,**不是**超时重试的前提:curl 手册写的是「transient error means **either: a timeout**, an FTP 4xx ... 」,所以单靠 `--retry` 就能覆盖 #236–#241 那次失败。此前把它写成前提是错的,记在这里因为那曾是保留这个 flag 的唯一理由。|
| **校验** | 解包**之前**核对官方 SHA-256(`2640fc56…5333`) | 截断或被替换的下载不能被静默编译进差分 oracle——那会让整轮差分结论失效**而且无声** |
| **复用** | 已存在且校验通过的 tarball 直接用 | 于是 actions cache 命中可以完全跳过网络 |

**不做镜像**是明确取舍:`github.com/lua/lua` 没有 5.1.5 tag,任何重新打包的副本都过不了官方 checksum;多一个源降低的是**取包失败率**(重试已经在降它),checksum 防的是「取到了错的东西并且编译了它」,后者的失效是无声的,所以 checksum 这个性质比「多一个源」更值钱。

**这一轮的诊断本身有一条判据值得记**:失败步骤报的是 `exit code 28`,当时被读成 **ENOSPC**(磁盘写满,errno 28)并据此提了「腾磁盘空间 / 缓存构建产物」的建议,而 **28 是 `curl` 的 `CURLE_OPERATION_TIMEDOUT`**——那一步是 curl,shell 报的退出码来自 curl 自己的退出码表、不是 errno 表,两张表恰好在 28 这个数字上都有条目。日志里有两条免费的反证:`apt` 成功结束之后**沉默 2 分 15 秒**才失败(磁盘写满是**立刻**失败的,会先卡的只有等待类失败),以及 `no space left` / `ENOSPC` / `disk full` 在两个 run 的日志里出现次数都是 **0**。判据:**看到一个数字退出码,先定位是哪个命令退出的,再查那个命令自己的退出码表**;并且**分类一个 CI 失败时先把时间戳减一遍**,时间形式往往比错误码更能定类,而且它在日志里是免费的(方法论见 `llmdoc/guides/unreproducible-crasher-triage.md`「静默死亡先做两步分类」)。
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
4. **oracle 版本校验 fail-fast**:lua5.1 非 5.1.5 即失败,绝不静默降级。源码构建腿的取包**必须限时 + 重试 + 校验 SHA-256 之后才解包**(§4.1),四处 call site 共用 `scripts/fetch-lua-tarball.sh`,不允许再出现裸 curl。
5. **release tag 不可移动**;golden/豁免清单改动必须显式过 review(12 §8)。
6. **commit message 强制 `type(scope):`**(commit-msg hook + 既有提交史一致)。
7. **nightly job 的每一个步骤都带 step 级 `timeout-minutes`**(§3.2,2026-08-19,commit `9b61079`):防的是「一条卡死的命令悄悄吃掉整个 job 的预算」,不是「所有 step 上限之和低于 job 上限」——两者是不同的约束,job 超时才是那个总预算。给报错命令加超时时作用域必须是它所在的**整个步骤**而不是那一条命令,否则同一步里的邻居命令留在无界状态,下一次会从邻居那里挂住。

**文档缺口(记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md)):**

- nightly-diff-fuzz 的 `fuzz-triage.sh` 解析协议(FAIL/INFRA 分类的精确判据)待 difftest harness(M14)定稿后完成。
- 非 Ubuntu runner 的 oracle 源码编译 + 缓存方案待实测(取包这一环已收口,见 §4.1)。
- ~~**nightly 报 failure 但差分步骤被 skip 时,没有任何地方写明「本轮未执行任何差分 fuzz」**~~ (2026-08-09 已收口:infra issue 的 body 读 `steps.difffuzz.outcome`,被 skip 时明确写出该句)
- ~~`unpack` 的 int32 skip 区间~~ 守卫经六轮审计收口:区间读 `i` 与 `e`、窄化走 `__ckint0`、助手在脚本前捕获、非表首参不跳;用例见 `internal/oracle/unpack_guard_test.go`(13 skip / 12 compare,两向变异确认)。按机制应判 `i32 <= e32 且 (e32 - i32 + 1) > INT_MAX`;口径见 `p1-interpreter/12-testing-difftest.md` §4.9f。
- bench-gate 的回退阈值(±N%)与基线 artifact 的存储/对比协议待 M14 校准。
- agentic workflows 的接入时机与模板源(§3.5)。
- 覆盖率是否设硬门槛(当前仅 artifact 存档,pineapple 一样的;若 P1 后期需要再议)。

---

相关:[12-testing-difftest](./p1-interpreter/12-testing-difftest.md)(门禁逻辑) ·
[00-overview](./p1-interpreter/00-overview.md)(M0 里程碑) ·
[architecture](./architecture.md)(目录布局) ·
[multi-doc-drafting](../../llmdoc/guides/multi-doc-drafting.md)(工作流 guide)
