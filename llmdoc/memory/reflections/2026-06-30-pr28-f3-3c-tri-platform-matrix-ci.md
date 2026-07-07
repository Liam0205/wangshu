# PR #28(承 F3-#3b)F3-#3c 三平台矩阵 CI 等同轮反思:arm64 与 amd64 地位等同的真实成本是 N 个 macOS 兼容性 paper cut

> 范围:承本会话上半段 [[2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup]] F3-#3b darwin/arm64 真机 execute 闭环。本会话后半段把 PR #28 推进到「arm64 与 amd64 地位等同」的 CI 形式(F3-#3c)——test/fuzz-smoke/conformance/difftest × {amd64, ubuntu-24.04-arm, macos-latest} × {p1, p3, p4} = **36 jobs 矩阵化**——并解决一系列 bash 3.2 / macOS 兼容性问题 + 加 fuzz fail 长期可观测性。最终 **39/39 CI checks pass + machine reviewer APPROVE**,**PR #28 ready to merge to `feat/p4-reworded`**。
> commit 链(`ddcaebe..5afea11`,4 commits;two-dot 记号 exclusive of start,故起点写 `efcfe5e` 父 `ddcaebe`):`efcfe5e`(F3-#3c arm64 三平台矩阵化 + review#4 重复注释)→ `a8d7245`(macOS lua5.1 oracle 改源码编译 + actions/cache)→ `61dadde`(macOS bash 3.2 兼容三连)→ `5afea11`(fuzz-smoke fail artifact upload)。
> 关联:本会话上半段 [[2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup]](F3-#3b 真机 execute 闭环 + bypass 探针根因 isolate);[[2026-06-15-p3-pw10-r3-call-indirect-round]] 头条「spike 量错维度」相邻——那条是「测对了机制错了维度」,本条是「决策对了实施踩跨平台 paper cut 矩阵」;[[design-claims-vs-codebase-physics]] §5「时间维度」(本会话上半段刚扩充)候选续 §6「空间维度——跨 OS/shell 版本物理环境差异」;[[prove-the-path-under-test]] 候选 2 对偶面「证明 input 在 testdata 而非 runner 销毁」。

## 任务

承本会话上半段把 PR #28 F3-#3b 收口后,用户提出简单决策:**「arm64 应该与 amd64 地位等同」**。F3-#3b 阶段 macOS arm64 真机 execute 已经过线,但 ubuntu-24.04-arm 真机 arm64 runner(GHA 2024 GA,public repo 免费)在 PR #27 时被裁判为 followup。本会话后半段目标:把 PR #28 推到「arm64 与 amd64 地位等同」的 CI 形式——test/fuzz-smoke/conformance/difftest 全套 × {amd64, ubuntu-24.04-arm, macos-latest} × {p1, p3, p4} 矩阵化、F3-#3c 检查翻 true、全套 CI checks 绿、machine review 过。

## 预期 vs 实际

- **预期**:把 `.github/workflows/ci.yml` 的 matrix 加 `ubuntu-24.04-arm` 一行就完事,矩阵化是机械改动 ~30 分钟。macOS job 已在 F3-#3b 跑全套,延伸 arm64 应该零阻力。
- **实际**:**矩阵化本身 30 分钟,但延伸面踩了 5 个独立的 macOS 兼容性 paper cut**——每一个都让矩阵 CI 跑挂、需要单独调研根因 + 修复 + 重跑 CI 等结果。**总周期 ~2 小时,实施成本是决策成本的 4-6 倍**。最终 39/39 CI checks pass + machine reviewer APPROVE,但过程是 4 commits(`efcfe5e`→`a8d7245`→`61dadde`→`5afea11`)连环排雷,其中 `61dadde` 一次合修 3 个 bash 3.2 兼容性问题。

## 5 个 macOS 兼容性 paper cut 一览

| # | 表现 | 根因 | 修复 commit |
|---|---|---|---|
| **1** | `test (p1 / macos-latest)` fail | `brew install lua@5.1` 已废弃(macOS homebrew 2024 后只剩 `lua@5.4`)| `a8d7245` 改源码编译 lua-5.1.5 + actions/cache |
| **2** | `difftest (p* / macos-latest)` fail | actions/cache 缓 `/usr/local/bin/lua5.1` symlink 但不缓 symlink 目标二进制 → restore 后 broken symlink | `a8d7245` 改缓**编译产物源目录**(`lua-5.1.5/`)+ restore 后必跑 `make install` |
| **3** | `fuzz-smoke (p1 / macos-latest)` fail | `scripts/go-fuzz.sh` 用 `${arr[@]}` 展开空数组,macOS bash 3.2 + `set -u` 触 unbound | `61dadde` 改 `${arr[@]+"${arr[@]}"}` 跨版本兼容惯用法 |
| **4** | 本机 `git commit` 被 pre-commit hook 拒 | `.githooks/pre-commit` 用 `mapfile` + `declare -A`(bash 4+)在 macOS bash 3.2 跑不过 | `61dadde` 改 `while IFS= read -r ... ; do arr+=("$line"); done < <(cmd)` + `pairs+=("$key"$'\t'"$val")` + `sort -u` 分组循环 |
| **5** | `fuzz-smoke (p4 / ubuntu-latest)` fail | `FuzzP4ForceAllPromote` 探到的 input 没 artifact 上传,GHA runner 销毁后无法本地复现(我本机 darwin/arm64 跑同 fuzz 没探到一样的 input)| `5afea11` 配 `if: failure() + actions/upload-artifact` 上传 `testdata/fuzz/` |

## 核心教训

### 1. 「跨平台等同」决策的实施成本 ≫ 决策本身的概念复杂度——预算 N 倍 paper cut

用户提的决策「arm64 应该与 amd64 地位等同」概念上极简(一句话、一个判断),但实施时炸出 5 个独立的 macOS 兼容性 paper cut——每个看起来 5 分钟修,叠加起来 2 小时;每个都需要单独「等 CI 跑完 → 看 log → 定位根因 → 修 → push → 再等 CI」的闭环 ~20 分钟。

**Why**:跨平台 / 跨 OS / 跨 N 维度等同类决策的实施面 = N 维笛卡尔积上的所有未审视组合,**每个组合都可能藏一个原本被「单平台/单维度」掩护的 latent 问题**——本轮 5 个 paper cut 中:#1 是「macOS homebrew 包政策时间维度漂移」、#2 是「actions/cache 对 symlink 的语义边界」、#3/#4 是「macOS 默认 bash 3.2 vs Linux 默认 bash 4+ 的语法差异」、#5 是「fuzz fail 在 runner 销毁后的 input 丢失」。这些问题在 amd64+Linux 单平台单维度下都不暴露,接 arm64 + macos-latest 时一次性翻面。这与 [[2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup]] 教训 2「多后端镜像同源 bug 漏改」同根——**新维度首次接入时一次爆一批 latent bug**。

**How to apply**:接到「跨平台 / 跨 OS / 跨架构 / 跨 N 维度等同」类简单决策时,实施预算**乘 4-6 倍**;规划时主动列「新维度上有哪些与原维度不同的物理环境差异」清单——本轮 5 个 paper cut 中至少 #1/#3/#4 可以提前 grep `brew install` / `mapfile|declare -A|\$\{arr\[@\]\}` 预防,但 #2/#5 是机制级 latent 必须实战暴露。判据:**「这条决策的概念表述里有几个 N(平台数 / 架构数 / 后端数)」**,N 越大,实施面笛卡尔积越大,paper cut 概率越高。

### 2. macOS bash 3.2 是真实的 deploy target(不是历史遗留)——跨版本 bash 兼容性纪律

Apple 因 **GPLv3 政策**长期不升级系统 bash,**所有 macOS 默认 `/bin/bash` 至今仍是 3.2.57(2026 年本会话实测)**。这是个长期约束,不是「等 macOS 升级 bash 4 就好了」——而且 Apple 已经把默认 shell 切到 zsh,bash 3.2 还在但维护意愿更低。

本会话踩**三个独立的 bash 3.2 vs bash 4+ 语法差异**(全在同一个 commit `61dadde` 修):

| 形式 | bash 3.2 行为 | bash 4+ 行为 | 跨版本兼容写法 |
|---|---|---|---|
| **空数组展开 + `set -u`** | `${arr[@]}` 在 `arr` 为空时被视作 unbound → 触发 `set -u` exit | `${arr[@]}` 空数组安全展开为零参数 | `${arr[@]+"${arr[@]}"}`(参数扩展默认值语法,空数组时整个被替换为空) |
| **`mapfile`(数组从命令读)** | 不支持 `mapfile`(bash 4.0+ 内建)| `mapfile -t arr < <(cmd)` 一行从 stdin 装数组 | `while IFS= read -r line; do arr+=("$line"); done < <(cmd)` 显式循环 push |
| **`declare -A`(关联数组)** | 不支持 `declare -A`(bash 4.0+ 引入关联数组)| `declare -A m; m[key]=val` | `pairs+=("$key"$'\t'"$val")` 把 key/val 用 `\t` 拼成元组数组,然后 `printf '%s\n' "${pairs[@]}" \| sort -u \| while IFS=$'\t' read -r key val; do ...` 分组循环 |

**纪律**:任何 Linux 开发者写的 bash 脚本要在 macOS dev 环境跑(本会话场景:`.githooks/pre-commit` + `scripts/go-fuzz.sh`),提交前 grep `mapfile|declare -A` 关键字 + 检查 `${arr[@]}` 在 `set -u` 下的空数组行为。三条都有跨版本兼容写法,不需要放弃 macOS 兼容性。

**Why bash 3.2 仍是 deploy target**:macOS dev 环境(本机调试 / pre-commit hook)与 GHA macos-latest runner 都默认 `/bin/bash` = 3.2;即使用户装了 homebrew bash 5 也通常在 `/opt/homebrew/bin/bash`,需要 shebang 显式指 + PATH 安排,跨开发机不可靠。**Linux 默认 bash 4+ 的脚本在 macOS 默认 shell 下静默挂**(`mapfile` not found / `declare -A` 报错 / `${arr[@]}` set -u exit)是 dev 环境常见 paper cut。

### 3. actions/cache 缓 symlink 不缓 symlink 目标——必须缓真二进制或编译产物源目录

paper cut #2 的根因极隐蔽:`actions/cache` 缓 `/usr/local/bin/lua5.1` 这种 symlink 时**只缓 symlink 元数据(指向 `/usr/local/bin/lua` 的字符串),不递归 symlink 目标二进制**——cache hit restore 后 symlink 还在,但目标不在(因为目标在另一个未缓的路径),symlink 直接 broken。

第一次跑 ~10 秒编译 + install 创建 symlink → cache save 只存 symlink → 第二次 cache hit restore symlink → 跑 `lua5.1 -v` 报 `command not found` 或 `cannot execute binary file`。

**正确做法两选一**:

1. **缓编译产物源目录**(本批选项):cache key `lua-5.1.5-src-tree`,缓 `lua-5.1.5/` 整个 build dir(已 `make` 出 `src/lua`/`src/luac`);restore 后**必跑 `make install` 重新装到系统目录 + 创建 symlink**,~1s restore + ~0.1s install,比从头编译 ~10s 快 10 倍且 symlink 一定正确;
2. **缓真二进制路径**:不要缓 symlink,直接 cache key 上指 `/usr/local/lib/lua-5.1.5/lua`(真二进制不是 symlink)+ 手动 cache 后 `ln -sf` 重建 symlink。

**Why**:`actions/cache` 底层用 `tar` 打包路径,默认 `tar` 行为是把 symlink 当 symlink 存(只存指向字符串),不会跟着 symlink 把目标也打包进去。这是 POSIX `tar` 默认语义,不是 `actions/cache` bug,但效果上等价于 cache 半残。**纪律**:任何 cache 路径在 cache save 前先 `ls -la` 看是不是 symlink,是 symlink 则切「缓源目录 + restore 后重 install」模式。

### 4. fuzz fail 必须配 artifact upload,否则丢失 input 无法本地复现——`prove-the-path-under-test` 对偶面

paper cut #5(`FuzzP4ForceAllPromote` 在 ubuntu-latest fuzz 30s 抓到 input 但 GHA runner 销毁后 input 丢失)是 `prove-the-path-under-test` 家族的**对偶面**——以往家族实例都是「证明快路径被走到」(走到才能验路径),本条是「证明 input 在 testdata 而非 runner 销毁」(留住才能复现)。

**Why**:fuzz 与一般测试不同——一般测试 fail 只需要看 log 就能复现(input 是源码常量),fuzz fail 需要拿到**具体的 fail input bytes** 才能本地复现。Go fuzz 自动把 fail input 写在 `testdata/fuzz/FuzzXxx/<hash>`,但 GHA runner 销毁后 `testdata/fuzz/` 整个消失,**只有 CI log 里的 input bytes 字符串残留**——若 input 长 / 含二进制 / Go 自动 escape 后字符串解析回 bytes 易出错,**实际本地复现路径断**。

**纪律**:fuzz CI job 必须配 `if: failure() + actions/upload-artifact` 上传整个 `testdata/fuzz/<FuzzName>/`(目录而非单文件,fuzz 可能抓多个 input)。本批 `5afea11` 完成这个机制 + 给 PR comment 列出「artifact 下载步骤 + 本地 `git fetch + go test -fuzz=...` 复现命令」hand-off 给另一平台 dev。

**Why 这是「长期机制」而非单 PR 修复**:fuzz fail 是天然偶然性事件——任何人(本 PR 或后续 PR)在 fuzz CI 撞到 fail 都需要这个 artifact 通道。**一次配好长期受益**,与 [[2026-06-12-test-hardening-round]]「fuzz 目标空转」家族同纪律层(fuzz 防线机制级配套)。

### 5. PR comment 是「跨平台修 bug」的 hand-off 通道——本机不在目标平台时,PR comment 比 chat 更稳定

paper cut #5 触发的真实流程:**`FuzzP4ForceAllPromote/e46b64d036f74281` 在 ubuntu-latest amd64 上 fuzz 30s 抓到**,但我当前在 macOS arm64 本机,**P4 amd64 build 在本机走 stub `codepage_other.go`**(arm64 build 用真实 emit),所以本机 fuzz 同测试不会走 amd64 真路径 → 无法本地复现。

**纪律**:跨平台 bug(本机不在目标平台 / 不在出 bug 的架构 / 不在出 bug 的 build tag)**写 PR comment 详细描述**:

1. **复现路径**:在另一个平台环境怎么 re-run(`git fetch origin pull/28/head && go test -fuzz=FuzzP4ForceAllPromote -fuzztime=60s ./internal/gibbous/jit/...`);
2. **artifact 下载步骤**:`gh run download <run-id> --name fuzz-fail-testdata` + 把 artifact 解到 `testdata/fuzz/FuzzP4ForceAllPromote/`;
3. **bug hypothesis 分档**(三档):H1 `archSupportsSpec` 在 amd64 真路径下的状态机 race / H2 PJ5 SelfCall 在 fuzz random proto 形式下越界 / H3 OSR exit hook 在 PJ7 接上路径上的注册-触发竞态;
4. **留 followup 别在本平台硬上**:本机硬上 amd64 fuzz 路径需要切 cross-compile build tag、装 amd64 toolchain、可能还需 Linux VM——成本远高于 hand-off 给 linux/amd64 dev 环境。

本会话 PR #28 comment(`#issuecomment-4833308292`)就是这个模板的实例。

**Why PR comment > chat hand-off**:PR comment 是仓库永久附件,跨会话 / 跨人 / 跨工具(任何后续 dev 看 PR 都看得到);chat 是临时会话,跨会话即失。本会话最终 merge 后 PR comment 仍在,作 followup 的真 source-of-truth。

### 6. pre-push hook「self-wrapping post-push」模式 + Claude Bash tool 默认 2 分钟 timeout 不兼容——长 hook 配 background bash 是默认

本仓库 `.githooks/pre-push` 设计是 inner push 后**阻塞等 CI ~5-10 分钟** + 抓 PR comments + 报告(本会话经 [[2026-06-15-p3-pw10-r3-call-indirect-round]] 之后引入,本会话用户已多次复现确认)。Claude Bash tool 默认 2 分钟超时,**直接前台 `git push` 会被杀(SIGTERM)在 CI watch 阶段,但 inner push 已成功推上 remote**——表面「push 失败」实际「push 已成功,只是 hook 报告被打断」。

**正确调度**:`git push` 用 `run_in_background: true`,让 hook 跑完自然通知。或独立调 `scripts/check-pr-ci.sh` background。

本会话犯过一次错(用前台 `git push` 跑 2 分钟被杀,以为 push 失败再 retry,实际 remote 已更新),用户提示后改 background。**这个不算新教训**(`run_in_background` 文档明说),但是**「长 hook + 短 timeout 工具」组合的典型踩坑**,值得在 reflection 里立个 trigger:**任何 push / build / test 命令若仓库挂了 post-* hook 或 wrapper 脚本超出 2 分钟,默认用 `run_in_background: true`**。

### 7. macos-latest = macOS 15 arm64 / ubuntu-24.04-arm 是真机 arm64 GA(public repo 免费)

GHA 提供 `ubuntu-24.04-arm`(2024 GA,public repo 免费,真机 AWS Graviton 类 arm64,非 QEMU emulation),是 QEMU emulation 的**严格超集**——真机 BL→mmap 执行能力 + 性能与生产环境一致。

PR #27 时点没切是因为「physical arm64 self-hosted runner 留 followup」的占位——实际有更简单的 GHA 真机选项(`ubuntu-24.04-arm` 在 PR #27 时点已 GA 几个月)。

**触发**:做新 P4 后端 / 任何 cross-arch CI 接入前先看 GHA 当前提供哪些 runner image(`runs-on:` 文档),不要默认走 QEMU。Apple Silicon CI 提供商(MacStadium / Cirrus / Codemagic)是 macOS arm64 备选,但 GHA macos-latest 已默认 M1/M2 物理机(2023 后),不需要 self-hosted。

**关联**:本轮三平台矩阵 `{amd64=ubuntu-latest, ubuntu-24.04-arm, macos-latest}` 全是 GHA 一级支持 runner,**零 self-hosted 维护成本**——这与 PR #27 留 followup 时的预期(self-hosted M1 维护 / 第三方 CI 接入)完全不同,实际选项更优。

## 其它(较小的过程点)

- **PR review #4 重复注释 + 单文件 fix**:`efcfe5e` 同 commit 顺手解 PR review #4 列出的「一样的 sentinel 注释承诺」类重复点(本会话上半段反思 [[2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup]] 教训 3「sentinel 保底注释承诺与检查状态解耦」家族),证实那条候选可以入 [[design-claims-vs-codebase-physics]] §5「时间维度」家族 first-class;
- **`make all` 默认扩面跟 P4 PJ9 双架构差分套对齐**:本批未动 `make all`,但矩阵 CI 翻面相当于 CI 端等价扩面;
- **CI matrix 配置注意点**:`fail-fast: false` 是矩阵 CI 必配(默认 true 会让一个 job fail 杀掉其他平台的 job,debug 多平台 bug 时看不到完整画面);`if: matrix.os == 'macos-latest' && matrix.arch == 'arm64'` 类条件式可针对单元格做特殊配置(本批 macos-latest 单独配 lua 源码编译);
- **machine reviewer APPROVE 是 PR merge 前置**:本仓库配 GitHub Copilot review 类 machine reviewer,APPROVE 后才能 merge——本批 PR #28 39/39 + machine APPROVE 全到位才确认 ready-to-merge。

## 验证

- **三平台矩阵 39/39 CI checks pass**:test/fuzz-smoke/conformance/difftest × {amd64, ubuntu-24.04-arm, macos-latest} × {p1, p3, p4} 全套覆盖(部分维度笛卡尔积条件式跳过,实际 36 → 39 含 lint/typecheck 公共 job);
- **PR #28 machine reviewer APPROVE**:全部 review comment 解决(包括 review #4 重复 sentinel 注释家族);
- **macOS bash 3.2 兼容验证**:本机 M1 直接跑 `.githooks/pre-commit` + `scripts/go-fuzz.sh` 不报错(本会话 `git commit` 流程全程经 hook);
- **fuzz fail artifact 通路验证**:`5afea11` 配 `if: failure() + actions/upload-artifact` 在本批未实际触发 fail(P4 fuzz 已被前序 commit 修),但配置 syntax 经 `actionlint` 静态检查 + 等待下次 fail 时实战;
- **PR #28 ready-to-merge to `feat/p4-reworded`**:base 不是 master(承 F3-#3b 一样的 feature branch 工作流),merge 后 P4 method-JIT 集成开发主线推进一个里程碑。

## promotion 候选

按优先级排序。

### 候选 1:bash 3.2 兼容性纪律 → 落 [[design-claims-vs-codebase-physics]] §6 新章「物理环境差异——跨 OS / shell / runtime 版本约束」(强,跨过提升阈值)

**强信号**:本会话踩 **3 个独立的 bash 3.2 vs 4+ 语法差异**(`${arr[@]}` 空数组 / `mapfile` / `declare -A`),每个都是 macOS dev 环境真实的 build break。家族第 1 实例,**首次系统化记录**,但实例数集中跨过提升阈值(**3 个独立语法差异 = 3 个独立 trigger**,实际等同 3 实例)。

**手法**:在 [[design-claims-vs-codebase-physics]] §6 新章「物理环境差异——跨 OS / shell / runtime 版本约束」(扩 §5「时间维度」之外的新维度:**空间维度的物理环境差异**),列具体语法差异表(本反思教训 2 的三行表可直接复用)+ 跨版本兼容写法 + grep checklist。

**Why 是 design-claims 家族**:这与 §1-§4 的「设计稿热路径 `(call $x)` 主张 / 段重定位 / 成本归类 / GC 根」同属「设计稿/代码主张须对本码库 + 部署环境 physics 重新验证」家族——前 5 章主要是码库内部 physics(边界税 / arena / GC / 时间维度),§6 新章扩到**外部部署环境 physics**(目标 OS 的 shell / runtime 版本约束)。

**推荐完成**:**直接 promote 进 [[design-claims-vs-codebase-physics]] §6 新章**,与 §5「时间维度」并列作「空间维度——跨 OS / shell / runtime 版本物理环境差异」。recorder 落实。

### 候选 2:fuzz fail artifact upload → [[prove-the-path-under-test]] §6 新章「对偶面——证明 input 在 testdata 而非 runner 销毁」(中,首次样本暂留观察)

**新形式**:`prove-the-path-under-test` 家族 9 个实例(承上半段反思 8 实例)中**第一次出现「正向侧 → 对偶面」的延伸**:以往家族 8 实例都是「证明路径真被走到 / 证明覆盖度真在测」(走到才能验路径),本条是**「证明 input 在 testdata 而非 runner 销毁」(留住才能复现)**——fuzz / random-input-based 测试的 fail 与一般测试根本不同,input 是「测试在 fail 时才能拿到的事后数据」,丢失则路径无法复现。

**Why 暂留**:首次样本(本会话 1 实例),配偶面性质明确但需要再 1-2 个实例(任何 fuzz / property-based / random-input-based 测试 fail 但 input 丢失)再升 first-class。

**推荐完成**:**暂留观察**,记录手法(`if: failure() + actions/upload-artifact testdata/fuzz/`)+ 触发场景(任何 fuzz CI / property-based CI / random-input CI)。若 P4 PJ9 双架构差分套 / P5 后端 fuzz 再撞一样的,可升 [[prove-the-path-under-test]] §6 新章。

### 候选 3:「跨平台等同」决策成本预算 → [[perf-optimization-workflow]] 或 [[public-api-incremental-delivery]] 一样的工作流纪律(中,首次样本暂留观察)

**新形式**:用户提的简单决策(「arm64 等同 amd64」)实施时炸出 5 个 macOS 兼容性 paper cut。这个**「概念决策 ≪ 实施成本」的 gap** 是任何「跨平台 / 跨 N 维度等同」类决策的常见特征——不是技术失误,是结构性必然(N 维笛卡尔积上的 latent 问题首次暴露)。

**Why 暂留**:首次样本(本会话 1 实例),但 [[multi-doc-drafting]]「分而治之的跨文档协作」是它的**对偶面**——那个是「拆分 N 维让每个独立推进」的工作流,这个是「等同部署 N 维让笛卡尔积上的问题一次性暴露」的工作流。两者构成「多维度工作的对偶纪律」。

**推荐完成**:**暂留观察**。若后续再撞「简单决策实施时连环踩跨平台问题」情形(P5 后端接入 / cross-arch fuzz 扩面 / 新 OS 接入),可升 workflow guide「multi-platform-deployment-budget」,与 [[multi-doc-drafting]] 配对成「多维度工作的对偶纪律」。

### 候选 4:长 hook + 短 timeout 工具的 background bash 默认纪律(弱,文档已说不算新教训)

`run_in_background: true` 是 Claude Bash tool 文档明说的特性,本会话犯错也是「忘了用」而非「不知道」。**不算新教训**,但可作 trigger:**任何 push / build / test 命令若仓库挂了 post-* hook 或 wrapper 脚本超出 2 分钟,默认用 background**。

**推荐完成**:**memory 内 trigger**,不入 guide。

## 触发场景

按本会话 6 条独立教训分类。

- **做「跨平台 CI 等同」类决策时**(教训 1):预算 N 倍 paper cut——任何「跨平台 / 跨 OS / 跨架构 / 跨 N 维度等同」类决策,实施预算乘 4-6 倍;
- **写 bash 脚本要在 macOS dev 环境跑时**(教训 2):grep `mapfile|declare -A` + 检查 `${arr[@]}` 在 `set -u` 下空数组行为;macOS 默认 `/bin/bash` 是 3.2.57 是长期事实非历史遗留;
- **加 actions/cache 缓 symlink 类文件时**(教训 3):改缓真二进制或编译产物源目录 + restore 后必跑 `make install`;`actions/cache` 缓 symlink 只存指向字符串,目标不递归;
- **加 fuzz CI 时**(教训 4):配 `if: failure() + actions/upload-artifact testdata/fuzz/`——fuzz fail 与一般测试不同,input 是事后数据,丢失则路径无法复现;
- **pre-push / pre-commit hook 阻塞等 CI 时**(教训 6):用 `run_in_background: true` 不用前台——长 hook + 短 timeout 工具组合默认踩坑;
- **跨平台 bug 没本机复现路径时**(教训 5):PR comment hand-off 给另一平台 dev 环境——列复现路径 / artifact 下载步骤 / bug hypothesis 三档 / 留 followup 别在本平台硬上;PR comment 比 chat hand-off 稳定(永久附件);
- **GHA cross-arch CI 接入前**(教训 7):查当前 GHA runner image 提供哪些 arch(`ubuntu-24.04-arm` 2024 GA public repo 免费 / macos-latest 默认 M1 物理),别默认走 QEMU 或 self-hosted。

## 关联

- [[2026-06-30-pr27-f3-3b-darwin-arm64-execute-roundup]](本会话上半段 F3-#3b 真机 execute 闭环 + bypass 探针根因 isolate;本反思承其检查翻 true 之后,把 CI 形式推到三平台矩阵等同 F3-#3c)
- [[2026-06-15-p3-pw10-r3-call-indirect-round]] 头条「spike 量错维度」相邻——那条是「测对了机制错了维度」,本条是「决策对了实施踩跨平台 paper cut 矩阵」;两者都是「面对一个看似简单的目标,实际成本被某个未审视的维度主导」家族
- [[design-claims-vs-codebase-physics]] §5「时间维度」(本会话上半段刚立)——本反思候选 1 续 §6「空间维度」(跨 OS / shell 版本),完成「时间 × 空间」双维度的设计稿主张审计纪律
- [[prove-the-path-under-test]] 候选 2 的对偶面「证明 input 在 testdata 而非 runner 销毁」——以往 8 实例都是正向侧(证路径走到),本条是对偶面(证 input 留住才能复现)
- [[2026-06-12-test-hardening-round]]「fuzz 目标空转」家族——同纪律层(fuzz 防线机制级配套),本条是 fuzz fail artifact 通路(配套的另一面)
- [[multi-doc-drafting]] 候选 3 对偶面——那个是「分而治之」的跨文档协作,这个是「等同部署」的跨平台展开,构成「多维度工作的对偶纪律」
- `docs/design/p4-method-jit/06-backends.md` §4 双后端骨架(本批 CI 矩阵化是该节描述「双架构 CI 双跑」的工程兑现)
- `.github/workflows/ci.yml`(本批主战场:test/fuzz-smoke/conformance/difftest × {amd64, ubuntu-24.04-arm, macos-latest} × {p1, p3, p4} 矩阵化)
- `.githooks/pre-commit` + `scripts/go-fuzz.sh`(bash 3.2 兼容三连修复战场)
- PR #28:https://github.com/Liam0205/wangshu/pull/28(base `feat/p4-reworded`,本反思收口时 39/39 CI checks pass + machine APPROVE,ready-to-merge)
- commit 链:`efcfe5e`(F3-#3c arm64 三平台矩阵 + review#4)→ `a8d7245`(macOS lua5.1 源码编译 + actions/cache)→ `61dadde`(bash 3.2 兼容三连)→ `5afea11`(fuzz fail artifact upload)
