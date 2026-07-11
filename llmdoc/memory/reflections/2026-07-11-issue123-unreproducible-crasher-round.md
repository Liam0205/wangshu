---
name: 2026-07-11-issue123-unreproducible-crasher-round
description: >
  issue #123 nightly p4 go-fuzz worker 无声死亡调查轮(2026-07-11,PR #124,closes #123):
  worker 跑 35 分钟 / 5100 万 execs 后 `fuzzing process hung or terminated unexpectedly:
  exit status 2`,无 panic 输出,落盘 corpus 是合法通过用例(无界非尾递归 + 每层 60 次全局
  写循环)。按 2026-07-03 沉淀的分诊纪律(真 crasher 判据 = 落盘 input 可复现)判定这不是
  input 决定的 VM bug——七角度复现矩阵全干净(精确重放 ×10 / 阈值镜像 hammer ×300 /
  bare+pcall+coroutine 三种恢复路径 / mmap 泄漏探针 300 层升层 / 6 分钟定向 fuzz 1600 万
  execs / GOGC=1 + SetGCStressMode / 版本核对失败 run headSha=d6e05bd 已含 #117/#118
  修复,同代码 p1 腿 45 分钟 / 9100 万 execs 干净),特征(几千万 execs 后死 + 无声 + input
  干净)指向 fuzz worker 进程级资源耗尽。处置不硬编修复也不无限挖:corpus 入库常驻回归
  (它本身是合法通过用例)+ scripts/go-fuzz.sh 诊断硬化(GOMEMLIMIT=6GiB 把无声外部 kill
  转成带栈的 runtime OOM fatal;worker 无声死时 dump `free -m` 与 `vm.max_map_count`
  进上传 artifact)。核心教训:① 用户提示「先看 run 开始时的代码版本」——版本核对该是无法
  复现时的第一步,不是第七步,先排除「撞的是已修复代码」这个最便宜的解释再撒复现矩阵;
  ② 分诊纪律真的把资源耗尽和 VM bug 分开来了,避免了硬修一个不存在的 VM bug;③ 「让下次
  复发自带诊断」是不可复现问题的正确投资方向——GOMEMLIMIT 把外部 kill 转成带栈的 fatal,
  等于把复现成本转嫁给下一次自然发生;④ 七角度复现矩阵(重放 / 镜像 / 错误恢复 / 资源
  泄漏 / 定向 fuzz / GC 压力 / 版本核对)本身值得留档,是「crasher 不复现时还能穷举什么」
  的检查单。
metadata:
  type: reflection
  date: 2026-07-11
---

# issue #123 无声 crasher 调查轮反思(2026-07-11,PR #124)

> 范围:分支 `fix/issue123-nightly-crasher`,PR #124。nightly p4 go-fuzz worker 无声
> 死亡的调查与处置——判定不是 input 决定的 VM bug,做 corpus 入库 + go-fuzz.sh 诊断
> 硬化两件事,不硬编修复不存在的 VM bug。

## 任务

nightly p4 `FuzzAutoPromote` worker 跑到 35 分钟 / 5100 万 execs 后死亡,报
`fuzzing process hung or terminated unexpectedly: exit status 2`,无任何 panic /
stack trace 输出。落盘 corpus `326b508ea720a654` 是一段合法的无界非尾递归 + 每层 60 次
全局写循环。任务是判定是否为真 crasher、能否复现、如何处置。

## 期望与实际

- 期望:按分诊纪律(2026-07-03 沉淀),落盘 input 若真是 crasher 触发点,精确重放应
  立即复现,进而定位 VM 侧根因。
- 实际:精确重放 + 六种旁路探测(共七角度)全部干净,同 headSha 的 p1 腿 45 分钟 / 9100
  万 execs 也没崩。特征(几千万 execs 后死 + 无声 + input 干净)与「input 决定的 VM
  bug」画像不匹配,更像 fuzz worker 进程级资源耗尽(内存 / mmap 数 / OS OOM killer)。
  处置转向 corpus 入库常驻回归 + go-fuzz.sh 诊断硬化,不硬编修复。

## 七角度复现矩阵(全干净)

1. corpus 精确重放 ×10;
2. harness 镜像(阈值 2/4 + budget 1<<20 两个 run)hammer ×300;
3. 提高 budget 到真栈溢出 + `bare` / `pcall` / `coroutine` 三种包裹的恢复路径(全部正确
   报 stack overflow 且 State 可复用);
4. mmap 泄漏探针(300 个升层 State,`/proc/self/maps` 稳定 44 条);
5. 以 corpus 为种子的 6 分钟定向 fuzz(1600 万 execs);
6. `GOGC=1` + `SetGCStressMode` hammer;
7. 版本核对:失败 run 的 `headSha=d6e05bd` 已含 #117/#118 修复,同代码 p1 腿 45 分钟 /
   9100 万 execs 干净。

## 处置

- corpus `testdata/fuzz/FuzzAutoPromote/326b508ea720a654` 入库常驻回归(它本身就是合法
  通过用例,入库无成本但站岗有价值);
- `scripts/go-fuzz.sh` 诊断硬化两条:
  - 加 `GOMEMLIMIT=6GiB`,把「内存悄悄涨到系统 OOM killer 边界然后被无声 kill」转成
    带完整 goroutine 栈的 runtime OOM fatal;
  - worker 无声死时 dump `free -m` 与 `vm.max_map_count` 进上传 artifact,下次复发直接
    看得到当时的系统状态。

意图明确:不复现则 corpus 站岗,复发则自带诊断。

## 核心教训

### 教训 1(无法复现 crasher 时,版本核对该是第一步不是第七步)

用户的关键提示:「看看 run 开始时的代码版本」。我这轮走的顺序是先撒精确重放 → 镜像
hammer → 恢复路径 → 资源泄漏探针 → 定向 fuzz → GC 压力 → 最后才去核 headSha。等把前六
条都跑完再核版本,顺序是反的。

正确顺序:**看到「落盘 input 无法复现」这个信号后,第一件事是核对失败 run 使用的 SHA
是否已经包含了近期修复**。这是最便宜的解释——「撞的是已修复代码,当时的 head 还没含
修复」——一条 `gh api` 或者翻 CI 日志就能排除。若失败 SHA 早于最近相关修复,直接判「已
在 master 修好,corpus 入库常驻回归」即可,复现矩阵都不用撒。若失败 SHA 已含修复(本
轮情况),再走复现矩阵才有意义,而且此时「没复现」也已经排除掉了最平凡的一种原因,
后续假设空间更干净。

**Why**:复现矩阵每条都要花几分钟到几十分钟,版本核对只要一条命令。把最便宜的解释放
到最后,是「先撒大网再省钱」的反模式——正确顺序永远是**便宜的解释先排除,再上贵的调查
动作**。

**How to apply**:任何「fuzz / nightly 报了个 crasher 但本地无法复现」的场景,第一件
事跑 `gh run view` 或翻 CI 日志确认失败时的 head SHA,和当前 master 比对,看有没有相关
修复已在两者之间进去。已修复就 corpus 入库结案;没修复才开复现矩阵。

### 教训 2(分诊纪律真的把资源耗尽和 VM bug 分开来了)

2026-07-03 反思沉淀的「真 crasher 判据 = 落盘 input 能否复现」这一条,这轮直接消费。
若没有这条纪律,面对「fuzz worker 死了 + 落盘一个 input」的组合,自然反应是把 input
当作 VM bug 触发点去追,可能会围绕这个 corpus 硬编一个「防御性修复」——但这个修复其实
什么也没修,因为 VM 侧根本没 bug。分诊纪律的价值就是把「进程级资源耗尽」和「input
决定的 VM bug」两类原因显式分开来,让「无法复现」这个信号有明确的下一步(不是继续挖
根因,而是转向进程级观测手段)。

**Why**:go-fuzz 的失败模型是「worker 挂掉时把当前 input 落盘」,但 worker 挂掉的原因
可以是 input 触发 panic(input 决定),也可以是 worker 进程被 OS 因资源耗尽 kill(与
input 无关,input 只是刚好在跑而已)。两种原因需要完全不同的处置路径,而「落盘 input
能否精确重放」是区分二者的最直接判据。

**How to apply**:go-fuzz worker 报 crasher 时,先跑一次精确重放。能复现 → input 决定
的 bug,按常规调查根因。多次重放都干净 → 转向进程级观测(内存 / mmap 数 / OS 日志),
不再纠缠 input 本身。

### 教训 3(不可复现问题的正确投资方向:让下次复发自带诊断)

无声外部 kill 之所以难查,是因为它没留下任何 in-process 证据——Go runtime 什么都来不
及打,artifact 里只有一句 `exit status 2`。这轮的处置思路是**把外部 kill 转成 in-process
fatal**:`GOMEMLIMIT=6GiB` 让内存耗尽在到达 OS OOM killer 边界之前先触发 Go runtime
自己的 OOM fatal,那会带完整 goroutine 栈和 heap 摘要;`free -m` + `vm.max_map_count`
dump 让即便还是被外部 kill,artifact 里也有当时的系统状态快照。

这是对「不可复现的低频罕见事件」的正确投资方向:不硬追这一次,而是把下一次复发的**信息
密度**拉高。复发是免费的采样机会,只要下次复发时有栈或有 dump,就不用再靠对着 CI 日志
一句话硬猜。

**Why**:低频罕见事件的调查成本大头不是「修」,是「等下一次复发」。等的时候免费,但
复发时若信息不够又要再等,循环下去。投资应该投在「让复发时的信息一次性够用」,不是投
在「这次尽力挖」。

**How to apply**:遇到「本地无法复现 + 生产 / CI 出现」类问题,除了调查根因之外,并
行考虑:能不能加一个便宜的诊断改动,让下次复发时留下更多信息?能就先做诊断硬化,把下
次复发的 information yield 拉高,再看要不要继续挖这次。

### 教训 4(复现矩阵的七个角度值得留档)

这轮的七角度矩阵是「crasher 落盘 input 无法复现时,还能穷举什么」的一份实用检查单:

1. **精确重放**:corpus 原样跑多次;
2. **harness 镜像**:阈值 / budget 参数与真实 fuzz worker 对齐后 hammer;
3. **错误恢复路径**:bare / pcall / coroutine 三种包裹分别试,验证异常路径可正确恢复;
4. **资源泄漏探针**:重复升层 / 分配的操作跑 N 次,观察 mmap 段数 / RSS 曲线;
5. **定向 fuzz**:以 corpus 为种子跑短时定向 fuzz,让 mutation 探索附近形状;
6. **GC 压力**:`GOGC=1` + `SetGCStressMode`,让 GC 时序变化暴露顺序依赖;
7. **版本核对**:失败 run 的 head SHA 相对当前 master 是否已含相关修复(见教训 1,
   实际应该排最前)。

留档意图:未来遇到同类「fuzz 报 crasher 但落盘 input 无法复现」时,可以拿这份检查单
逐条对照,不用现场想角度。

## Promotion 候选

- **教训 1 + 教训 2 + 教训 3 + 教训 4 合起来**:构成一份「不可复现 crasher 的处置模式」
  完整流程——版本核对先行 → 复现矩阵七角度 → 分诊(input 决定 vs 进程级) → corpus 入库
  + 诊断硬化。目前的形式已经足够沉一篇 guide(暂名 `unreproducible-crasher-triage`),
  或者并入 [[2026-07-03-issue40-arm64-stopbleed-round]] 提到的分诊纪律所在文档
  (那份分诊纪律目前只在「其它(较小)」小节里一段话,升格与本轮教训合起来足够独立成
  篇)。是否升格与升格形式由 recorder 判断;两个方向我倾向新开一篇,因为本轮除了分诊
  判据之外还沉了「版本核对先行」「诊断硬化投资方向」「七角度矩阵检查单」三条 07-03
  轮没有的内容,合进原文可能盖过原纪律的主线。
- 教训 3「不可复现问题投资诊断硬化而不是硬挖这次」这一条,和 [[perf-optimization-workflow]]
  §1「profile 先行」是同族的「先建立可观测性,再判断动作」思路,但方向不同(那条是性能
  侧的 profile,本条是可靠性侧的复发诊断),暂不合并,可在关联节提及。

## 触发场景

- go-fuzz / nightly worker 报 crasher,落盘 input 精确重放跑不出来时(教训 1:先核失败
  run 的 head SHA 相对当前 master 是否已含相关修复,再决定要不要开复现矩阵)。
- 判断「fuzz worker 死掉了」到底是 input 决定的 VM bug 还是进程级资源耗尽时(教训 2:
  精确重放能否复现是最直接的分界,不能复现优先考虑进程级)。
- 遇到「本地无法复现 + 生产 / CI 出现」类低频事件时(教训 3:并行做诊断硬化,把下次
  复发的信息密度拉高,不要只投入「这次尽力挖」)。
- 需要在 crasher 落盘 input 无法复现时穷举调查角度时(教训 4:七角度矩阵作检查单)。

## 验证

- corpus `testdata/fuzz/FuzzAutoPromote/326b508ea720a654` 入库,作为合法通过用例站岗;
- `scripts/go-fuzz.sh` 加 `GOMEMLIMIT=6GiB` + worker 无声死时 dump `free -m` /
  `vm.max_map_count`,PR #124 closes #123。

## 关联

[[2026-07-03-issue40-arm64-stopbleed-round]](本轮消费该篇「fuzz 失败形式分诊纪律」条目
——`context deadline exceeded` + 无 failing input 是 flake,`Failing input written to
testdata/...` + `hung or terminated unexpectedly` 是真 crasher,本轮扩到「真 crasher
落盘 input 但无法精确重放 → 转向进程级资源耗尽假设」)· [[2026-07-11-issue117-118-nan-forloop-round]]
(本轮版本核对时确认失败 SHA `d6e05bd` 已含 #117/#118 修复,排除了「撞的是同一批 NaN
死循环」这条便宜的假设)· [[perf-optimization-workflow]] §1「profile 先行」(教训 3
的可靠性侧邻接:先建立可观测性,再判断动作)· issue #123 · PR #124 ·
`scripts/go-fuzz.sh` · `testdata/fuzz/FuzzAutoPromote/326b508ea720a654`
