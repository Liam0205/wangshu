---
name: 2026-07-25-issue179-test-go-fuzz-retry-revive-round
description: >
  issue #179：仓库自带的 `scripts/test-go-fuzz-retry.sh` 已失效 — `scripts/go-fuzz.sh`
  后来加了 `go test -list` 的 fuzz 目标存在性 probe，但 stub `go` 只按状态字返回
  deadline-fail 输出，probe 阶段就退出，spurious-then-pass / always-spurious 两个
  case 从来没走到重试逻辑；third case real-crash 表面 OK 其实也是 probe 阶段假绿
  （probe 失败 rc=1 + 无「retrying once」字样，凑巧匹配「rc nonzero + 无重试」的判据）。
  且该脚本没接入 Makefile / CI 门禁，失效一年半没人发现。本轮修 stub 加 `-list` 分支
  返回 `FuzzX` 并 exit 0；顺手给 real-crash 加 `grep -q "Failing input written to"` 的
  positive prove-the-path 断言把假绿治死；`test-scripts` 目标接入 Makefile `all` 与
  `ci.yml` 独立 job。四教训中最强的一条：**回归测试判据只用 rc + 反向 grep 会假绿**，
  必须配 positive marker（本轮 `Failing input written to`）证明被测通道真正被执行。
metadata:
  type: reflection
  date: 2026-07-25
---

# issue #179：回归脚本失效 & prove-the-path 到 test-harness 侧（2026-07-25）

> 范围：分支 `fix/179-test-go-fuzz-retry-revive`，改 `scripts/test-go-fuzz-retry.sh`
> stub（加 `-list` probe 分支 + real-crash 加 positive marker 断言）+ `Makefile` 加
> `test-scripts` 目标并挂到 `all` + `.github/workflows/ci.yml` 加独立 `test-scripts`
> job；反思本文。不触及产品代码。

## 任务

issue #179 描述得清楚：`scripts/go-fuzz.sh` 后来加了一段 `go test -list` 用来
探测目标是否可编（避免 tag-gated 目标白跑一个 fuzztime slot），但配套的自测脚本
`scripts/test-go-fuzz-retry.sh` 里的 `go` stub 没跟上 —— 一进 probe 就照
state file 返回 deadline-fail 输出，被 go-fuzz.sh 判为「probe FAILED（编译错？）」
提前退出，两个失败 case 从没走到重试逻辑；而且该脚本没进 Makefile / CI，
失效期极长。

任务清单：

- [ ] stub 加 `-list` probe 分支返回 `FuzzX` 并 exit 0
- [ ] `spurious-then-pass` / `real-crash` / `always-spurious` 三分支都真进入重试逻辑
- [ ] 接入合适的 Makefile 目标与 CI 门禁
- [ ] `bash scripts/test-go-fuzz-retry.sh` 通过

## 本轮做了什么

### 1. stub 里区分 probe 与实际 fuzz 调用

`scripts/test-go-fuzz-retry.sh` 的 stub 以前只按 `$STATE_FILE` 决定输出，
不看命令行参数。改成两步过滤：

- 参数里出现 `-list` 或 `-list=*` → 直接 `echo FuzzX; exit 0`（probe 短路）；
- 参数里未出现 `-fuzz` → 未预期调用，`exit 2` 让 stub bug 一定浮出（防未来
  go-fuzz.sh 再加个 probe 之类的调用把 stub 又打瞎）；
- 只有见到 `-fuzz` 才进入原来的状态字派发（deadline / crash / pass）。

Fail-closed 的第二条判据是关键 — 只加正向的 `-list` 分支还行，把「意外
`go` 调用」标为 error 才是真正防未来回归的手法（**PR review 侧兜底**）。

### 2. real-crash case 加 positive prove-the-path 断言

原 real-crash 判据是 `rc nonzero` + `! grep -q "retrying once" "$log"`。
但脚本失效期，real-crash 也走的是 probe 阶段假绿路径 —— probe fail 出
rc=1 + 无 retry 字样，凑巧同时满足两条判据。三 case 里唯一表面 OK 的
其实全靠这个巧合。

加一条 positive marker：`grep -q "Failing input written to" "$log"`。
这行字节串只由 stub 在 real-crash state 里输出，只有 run_target 真的
执行到 fuzz 阶段才会出现在 log 里；probe 阶段短路则完全不打这行。修
stub 后再加这条断言 — 三 case 全绿。

### 3. 反向验证：故意去掉 stub 的 `-list` 分支，三 case 全 fail

用 `sed -i '/-list|-list=\*) echo "FuzzX"; exit 0;;/d'` 临时把新加的分支
删掉重跑，三 case 全部 FAILED（**包括** real-crash，因为 positive marker
断言现在正确地区分「probe 阶段短路」和「run_target 真的跑了」）；恢复
补丁后三 case 全绿。反向验证证明 prove-the-path 断言真的在把守。

### 4. Makefile 与 CI 接入

- Makefile 加 `test-scripts` 目标，`bash scripts/test-go-fuzz-retry.sh`
  一行调用；`.PHONY` 与 `all` 依赖同步加上（挂在 `lint` 之后 `build-all`
  之前，与 `lint` 同层的 fast static check）。
- `.github/workflows/ci.yml` 加独立 `test-scripts` job，runs-on
  `ubuntu-latest`，只跑 `make test-scripts`。用独立 job 是因为失败信号
  清晰 + 可以单独 rerun，且开销 <5s；不跟 `lint` 合并是因为二者出问题
  时排查目标完全不同。

`make test-scripts` 本地耗时 <2s，`make all` 里增加的墙钟可忽略。

## 教训

### 教训 1（头条）：回归测试判据只用 rc + 反向 grep 会假绿，必须配 positive marker

three-case harness 里 real-crash 是唯一「表面 OK」的 —— 它的判据是
`rc nonzero AND ! grep "retrying once"`。probe 阶段短路的失败恰好同时
满足两条：rc=1（probe fail）+ 无 retry 字样（根本没走到 run_target 里
那句 `echo "... retrying once" >&2`）。三分支里唯一没被察觉是**因为负
断言/rc 断言的判据集恰好被 probe-fail 的输出形式满足了**。

解药：**每个 case 的判据里必须至少含一个 positive marker，证明目标
路径的字节串真的出现在了 log 里**。real-crash 加 `Failing input
written to`，spurious-then-pass 与 always-spurious 的 `retrying once`
正是这个形式 —— 只是 real-crash 那条本来是负断言（`! grep`），当时看
不出问题；反过来"没某个字节串"是**弱证据**，随便一条与 happy path
无关的短路都能满足它。

家族归属：与 [[prove-the-path-under-test]] guide § 2/§3 反向侧解药
「毒化助手 / 命中计数器 / 错误路径用例」同源，但**载体是 shell 集成
测试**而非 Go 单元测试；本轮实证「rc + 反向 grep 判据在有 probe 短路
时假绿」是该家族**第 16 个独立实例**，跨过既有阈值（guide 已聚合 15
实例）；建议下一次 promotion 时把 shell 集成测试维度加进正文 —— 现有
条目基本围绕 Go 测试 harness / 白盒计数器 / 探针，shell 层的 stub +
grep 判据是尚未显式覆盖的载体。**首次样本暂留观察**，若下一次 shell
层再撞类似问题即升 guide。

### 教训 2：stub 内含双通道时，未匹配的调用必须 fail-closed

`scripts/go-fuzz.sh` 现在实际做两种 `go` 调用（probe + fuzz），未来还可
能加第三种（比如 `go env`、`go tool` 之类的 sanity check）。stub 只
按状态字 dispatch 意味着**每加一种新 go 调用，stub 就要跟着 patch**，
不 patch 就悄悄退化 —— 这次的直接教训。

解药：**stub 里加一条 unmatched-case fail-closed 分支**（本轮 `exit 2`
+ stderr 提示），让未来 stub 与 go-fuzz.sh 不同步时**第一时间**出现红
灯而不是假绿。这是 [[cross-backend-semantic-fix-sweep]] 的「新通道加
入现有 harness 时同步防护」纪律在 stub 侧的对偶 —— 那里说改一个 emit
通道要横向扫兄弟通道，本轮说加一个 caller 调用形式要扫兄弟 stub 分
支；首次样本暂留观察。

### 教训 3：tooling 脚本不进 Makefile / CI 会静默失效

`scripts/test-go-fuzz-retry.sh` 是 [[cgo-oracle-fuzz-round]] 之前引入的
（issue #63 golang/go#75804 retry 修复的回归 harness），从没接进
Makefile 或 CI —— 失效期至少半年（`go-fuzz.sh` 加 `-list` probe 的
commit 之后就崩，但没人跑过它）。

解药：**tooling 类回归脚本必须接入至少一个门禁**（Makefile `all` 目标 /
CI job / pre-push hook），否则一次 downstream 改动就能让它静默失效，
且失效期正比于「没人手工跑」的时间。本轮把它挂到 `Makefile all` +
`ci.yml` 独立 job 上，两个门禁同时对它负责：本地 `make all` 抓一次，
PR CI 再抓一次。

家族归属：与既有 [[cgo-oracle-fuzz-round]] 教训 4「测试 harness 静默
skip / 静默 pipe 关闭是持续雷区」是**同族对偶** —— 那里说 shell 探针
在 `| grep -q` / 循环内不加 `</dev/null` 会静默 skip；本轮说
tooling harness 不接门禁会静默失效。**首次以「不接门禁」形式出现，
暂留观察**；如果再撞一次 tooling 脚本静默失效，可以把两条合并成小 guide
「tooling 脚本失效模式与配套门禁纪律」。

### 教训 4（过程）：issue body 里的清单是判据但不总完整

issue #179 body 列的四条完成条件，前三条判据都能 mechanical 判定；
但**第 4 条「`bash scripts/test-go-fuzz-retry.sh` 通过」是判据里最弱
的**（本身 issue 记录时就是弱证据 — 通过不代表判据 1-3 真的到位）。
本轮如果只按第 4 条判定完成，会漏 real-crash 的假绿问题。

解药：**issue clist 里的「测试通过」条目要用作** *充分* **前提证明**其他
判据到位，不能反过来把「通过」当成其他判据的证据。跟 [[prove-the-path-under-test]]
的核心命题一致（测试通过 ≠ 在测的路径被走到），但本轮不同的是**清
单编写者本身**也可能落入这个陷阱 —— 需要在编写 issue clist 时把
「通过」拆成对应各判据的具体 positive assertion，而非笼统「通过」。
首次样本暂留观察。

## Promotion 候选

- **教训 1**（rc + 反向 grep 判据在有 probe 短路时假绿；positive marker
  必要）：跨过 [[prove-the-path-under-test]] guide 的实例阈值但**载体
  是新的**（shell 集成测试 + stub），建议下次 shell 层再撞时把这个
  维度加进 guide 正文。本轮暂留观察。
- **教训 2**（stub fail-closed for unmatched invocation）：与
  [[cross-backend-semantic-fix-sweep]] 的「新通道加入现有 harness 时
  同步防护」对偶，暂留观察；若下次是「上游 caller 新形式打瞎下游
  stub」再现即升 guide。
- **教训 3**（tooling 脚本不接门禁会静默失效）：与 [[cgo-oracle-fuzz-round]]
  教训 4 是同族，暂留观察；下一实例出现时合并成小 guide。
- **教训 4**（issue clist「测试通过」是弱证据）：暂留观察。

## 触发场景

- 写 shell / 集成测试 harness 且用 `rc` + `grep -q` / `! grep -q` 组合
  做判据时（教训 1：至少一个 positive marker 证明目标路径的字节串真出
  现在 log 里）；
- 写测试 stub / mock 时（教训 2：unmatched-case fail-closed，别按状态
  字兜底通配）；
- 引入 tooling 类回归脚本 / 一次性验证脚本时（教训 3：接门禁，Makefile
  `all` / CI job 至少挂一个）；
- 写 issue completion checklist 时（教训 4：「测试通过」是弱证据，
  clist 项应对齐具体断言而非笼统脚本 exit code）。

## 关联

- [[prove-the-path-under-test]]（教训 1 在其反向侧解药家族里补 shell
  载体维度，第 16 独立实例）
- [[cross-backend-semantic-fix-sweep]]（教训 2 「stub fail-closed」是
  其「新通道加入现有 harness」纪律的对偶）
- [[cgo-oracle-fuzz-round]]（教训 3 与其教训 4「shell 探针静默 skip」
  同族）
- issue #63（golang/go#75804 retry 修复所引入的 `test-go-fuzz-retry.sh`）
- issue #179 · commit（分支 `fix/179-test-go-fuzz-retry-revive`）·
  `scripts/test-go-fuzz-retry.sh` · `scripts/go-fuzz.sh` · `Makefile`
  `test-scripts` target · `.github/workflows/ci.yml` `test-scripts` job
