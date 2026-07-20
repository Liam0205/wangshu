# 不可复现 crasher 的处置模式(unreproducible crasher triage)

## 适用场景

fuzz / nightly / CI 报了一个 crasher,给出了一个落盘 input,但本地精确重放该 input 无论多少次都不
复现。典型征象:

- `go test -fuzz` 在 nightly / 长时间运行里 worker 死亡,报 `fuzzing process hung or terminated
  unexpectedly: exit status 2`,artifact 里挂了一个 `testdata/fuzz/FuzzXxx/<hash>`;
- 拿到该 input 精确重放,单次 / N 次都 PASS,harness 镜像跑 hammer 也 PASS;
- 几千万到上亿 execs 之后才死一次,复现窗口远长于一次典型 fuzz smoke。

本 guide 管的是**这种「不可复现」问题怎么止损**;它不管「可复现问题怎么修对」——那属于
[[prove-the-path-under-test]](修复后证在测的路径真被走到)和 [[cross-backend-semantic-fix-sweep]]
(修同一语义时枚举全部后端 × 通道)的范围。三者构成:

- 可复现 crasher(input 决定的 VM bug)→ prove-the-path + cross-backend sweep;
- 不可复现 crasher(进程级资源耗尽 / 工具链 flake / 已修复代码 remnant)→ **本 guide**。

## 第一步永远是版本核对(不是复现矩阵)

看到「落盘 input 无法复现」这个信号后,**第一件事是核对失败 run 使用的代码版本相对当前 master 是否
已含相关修复**,不是撒复现矩阵。

**判据**:「撞的是已修复代码,当时的 headSha 还没含修复」是所有解释里**最便宜**的一条,一条命令就能
排除,复现矩阵每条要几分钟到几十分钟。把最便宜的解释放到最后,是「先撒大网再省钱」的反模式。

**手法**:

```
gh run view <run-id> --json headSha,event,createdAt
git log --oneline <headSha>..HEAD -- <suspected-path>
git merge-base --is-ancestor <fix-commit> <headSha>; echo $?
```

分三档:

- 失败 headSha **早于**最近相关修复 → 判「已在 master 修好」,corpus 入库常驻回归,复现矩阵不撒;
- 失败 headSha **已含**相关修复 → 便宜解释已排除,再走下面的分诊 + 复现矩阵;
- 失败 headSha 与相关修复无明确因果 → 走分诊,顺带记「versions checked, no direct match」进 issue。

**教训来源**:#123 轮走反了顺序——先精确重放 → 镜像 hammer → 恢复路径 → mmap 探针 → 定向 fuzz →
GC 压力 → 最后才核 headSha,前六条都跑完再核版本。等排除掉「已修复代码」这条最平凡的解释后回头看,
前六条至少有一半可以不撒。反思 [[2026-07-11-issue123-unreproducible-crasher-round]] 教训 1。

## 真假 crasher 分界(承 2026-07-03 分诊纪律)

go-fuzz 的失败模型是「worker 挂掉时把当前 input 落盘」,但 worker 挂掉的原因可以是 input 触发
panic(input 决定),也可以是 worker 进程被 OS 因资源耗尽 kill(与 input 无关,input 只是刚好在跑
而已)。两种原因需要完全不同的处置路径,**「落盘 input 能否精确重放」是区分二者的最直接判据**。

三种典型征象与对应判定:

| 征象 | 判定 | 处置方向 |
|---|---|---|
| `context deadline exceeded` + **无** failing input 文件 | fuzz 引擎 30s 窗口收尾时的工具链 flake(golang/go#75804 一类) | 单独复跑即过,不入 corpus,不开 issue(反复出现再入 doc-gaps) |
| `Failing input written to testdata/...` + input **可精确重放** | 真 crasher,input 决定的 VM bug | 走常规调查,定位 VM 侧根因 |
| `Failing input written to testdata/...` + input 精确重放**多次干净** + 几千万 execs 后才死一次 | 进程级资源耗尽嫌疑(内存 / mmap 数 / OS OOM killer / **CPU wall-clock 撞 fuzz 10 秒 per-input 看门狗**) | **本 guide 剩余各节** |

> **补记(2026-07-20)**:concat storm 家族此前落在这一行、被具体判为「内存」资源耗尽,后经取证栈迹
> 证伪——真因是 CPU wall-clock 撞 Go fuzz 的 10 秒 per-input 看门狗(见下文「concat storm 家族已
> 根因定性并修复」一节)。教训:这一行的「资源耗尽」是一类候选而非单指内存;拿到取证栈迹后,看
> panic 首行区分——`panic: deadlocked!` = CPU 撞 10 秒看门狗(单输入 CPU 时间),`signal: killed`
> 才是 OS 层内存 / OOM。

**Why**:分诊纪律的价值就是把「进程级资源耗尽」和「input 决定的 VM bug」两类原因显式分开,让「无法
复现」这个信号有明确的下一步(不是继续挖根因,而是转向进程级观测手段)。若没有这条纪律,自然反应是
把 input 当作 VM bug 触发点去追,可能会围绕这个 corpus 硬编一个「防御性修复」——但那个修复其实什么
也没修,因为 VM 侧根本没 bug。

**教训来源**:2026-07-03 issue #40 分诊沉淀 + #123 轮直接消费。反思
[[2026-07-11-issue123-unreproducible-crasher-round]] 教训 2。

### 真 crasher 但失败形式是层间分歧:先问 oracle 拿第三方真值

判据表把「真 crasher(落盘 input 能复现)」判到「走常规调查定位 VM 侧根因」,但**真 crasher 的失败形式若是层间分歧**(P1-vs-auto 结果不一致 / P1-vs-force 不一致 / 后端 A vs 后端 B 不一致),归因的第一步不是查升层路径 / 后端路径,而是**问 PUC luac oracle 拿第三方真值**。

- 若 oracle 与 P1 一致、只与被测 tier 不一致 → 归因到被测 tier(升层 / 后端 / IC / 快路径);
- 若 oracle 与**两层都不一致** → bug 在共享层(前端 codegen / stdlib / VM 共享语义),**不在升层路径**——差分 fuzz 的 P1 参照系本身可能是错的,只是两层错法不同才让分歧显性化,若两层巧合错到同一份历史残留,这个 bug 会完全静默通过差分。

判据成立的物理前提:差分 fuzz 的参照系是「另一层」不是「已知正确的实现」,「差分 = 参照系正确 + 被测有 bug」的隐含前提天然被忽视,归因会偏向「被测层」放过共享层。PUC luac 是最便宜的 oracle(同 corpus 直接跑 + `luac -l` 出参考字节码,操作数级 diff 直接指出发射分支)。

**#125 实证**:corpus `function sum() for A=0,0 do end end return sum() or (sum())` P1 返 `0`、auto 返 `nil`、oracle 说 `nil`——表象是「层间分歧指向升层路径」,真相是共享前端 `stmtReturn` 的 codegen bug 让两个 tier 各自读到不同的栈垃圾:`base := fs.freereg` 在 `exp2NextReg` 之前捕获,而 `exp2NextReg` 内部先 `freeExp`(freereg 回落一格)再把值物化到低一格,`RETURN A = base` 因此指向值的后一格,读到栈上的未定义数据。反思
[[2026-07-11-issue125-return-freereg-round]] 教训 1(层间分歧归因先问 oracle) + 教训 2(前端 codegen freereg capture-use 间距审计判据:`base := fs.freereg` 与 use 之间夹了任何会移动 freereg 的调用如 `freeExp` / `exp2NextReg` 就红旗,改用 `e.info` 消费物化后的权威位置或后移 capture)。

## 静默死亡先做两步分类(排在复现矩阵之前)

worker「无声消失 / hung or terminated unexpectedly」类失败,在撒七角度复现矩阵之前,先做两个
几分钟量级的分类检查——与「版本核对先行」同一量级,都是先用最便宜的检查排除最便宜的解释:

1. **先分类退出方式**:失败消息里是 exit code 还是 signal?`exit status 2` 是 Go runtime 自身
   fatal(panic / fatal error)的退出码,worker 死时几乎必然打印了完整栈迹——死因不是「无声」,
   是有一份完整的尸检报告没被看到;`signal: killed` 才是 OS 层的 SIGKILL(如 OOM killer),
   那才是真的没有输出。这一步直接决定「有没有栈迹可找」。
2. **再查子进程的 stdio 接线**:若第 1 步判定有输出,查它被父进程接到了哪里。internal/fuzz 的
   coordinator 用 `exec.Command` 起 worker 时 `cmd.Stderr` 留 nil,os/exec 把它接到
   /dev/null——尸检报告每次都写了,每次都被系统性丢弃。

**教训来源**:concat storm 家族 10 例横跨 8 天,此前各轮全部在复现矩阵与内存限制上打转,没有
一轮查过 `exit status 2` 的语义;这两步各花几分钟,合起来把问题性质从「查不到死因」翻译成
「输出没接住」,一步解开僵局。反思
`memory/reflections/2026-07-19-fuzz-worker-forensics-round.md` 教训 1。

## 复现矩阵检查单(七角度)

退出方式与 stdio 接线分类完毕、版本核对已排除便宜解释、精确重放确认干净后,若还要继续调查,
以下七角度是「crasher 落盘 input 无法复现时,还能穷举什么」的实用检查单。逐条对照,不用现场想角度。

1. **精确重放**:corpus 原样跑 N 次(N ≥ 10),观察是否真的 100% 干净;
2. **harness 镜像 hammer**:阈值 / budget 参数与真实 fuzz worker 对齐后 hammer(N ≥ 300);形状对
   不上会静默落进别的路径,把 harness 里所有与 worker 一样的调参列清楚再跑;
3. **错误恢复路径**:`bare` / `pcall` / `coroutine` 三种包裹分别试,验证异常路径可正确恢复(不是
   死循环、不是 State 污染无法复用);
4. **资源泄漏探针**:重复触发升层 / 分配 / mmap 的操作跑 N 轮(N ≥ 300),观察 `/proc/self/maps`
   段数 + RSS 曲线是否单调增(mmap 段数不回收是常见嫌疑);
5. **定向 fuzz**:以 corpus 为种子跑短时(6~10min)定向 fuzz,让 mutation 探索附近形状;若邻域内
   有真 crasher 应短时抓到;
6. **GC 压力**:`GOGC=1` + `SetGCStressMode`(或等价),让 GC 时序变化暴露顺序依赖 bug;
7. **版本核对**:见上一节——**实际做的时候排最前**,写在这里是为了做检查单时不漏。

**七角度全部干净 + 几千万 execs 后无声死 + input 精确重放干净**这一组合的画像与「input 决定的 VM
bug」不匹配,更像 fuzz worker 进程级资源耗尽(内存 / mmap 数 / OS OOM killer)。此时**转处置模式**,
不继续无限挖根因。

**#123 轮实例**:七角度全干净,同 headSha `d6e05bd` 的 p1 腿 45 分钟 / 9100 万 execs 也没崩,特征
指向 fuzz worker 进程级资源耗尽,处置转向 corpus 入库 + 诊断硬化。

## 处置模式(判定为不可复现后)

不硬编修复,不无限挖根因。做两件事:

### 1. corpus 入库常驻回归——但要挑对入库位置

即使这个 input 是**合法通过用例**(#123 那个 `326b508ea720a654` 是无界非尾递归 + 每层 60 次全局写
循环,行为正确),也保留常驻回归。理由:

- 入库无成本;
- 站岗有价值——若这段代码将来因某处改动真的变成 VM bug 触发点,这个种子会立刻抓到;
- 常驻回归是「已经付过一次调查代价」的最便宜产出。

**入库位置的取舍**(#123 轮踩过一次):默认把 corpus 放进 `testdata/fuzz/FuzzXxx/<hash>`,但要先
判断 workload 本身的资源密度。fuzz coordinator 启动时在 `-parallel=N` 下**并行重放全部 seed
corpus** 作为 baseline coverage sweep;若 corpus 触发的 workload 本身很重(深递归 / 长循环 / 高
分配),并行重放瞬间放大资源压力,恰恰命中「不可复现 crasher」判定为进程级资源耗尽时怀疑的根因。
#123 轮的实测:corpus 入 `testdata/fuzz/` 后 fuzz-smoke 三条腿(mac / p3 / p4 ubuntu)在 30s 内
连挂——不是 corpus 有语义 bug,是 fuzz coordinator 的并发放大导致 worker 死。

判据:input 单独跑消耗几百 ms 以上 CPU、或触发深递归 / 大分配的,**改走 Go 回归测试**——写一个
显式测试(串行、单进程、逐 seed 跑一遍)覆盖同一形状,而不是入 `testdata/fuzz/`。功能等价,不搅
动 fuzz coordinator。#123 轮就是这样处理的:两个 corpus(`326b508e` / `8c132ff5`)从
`testdata/fuzz/FuzzAutoPromote/` 撤回,改成 `issue123_regression_test.go` 里的显式测试。

**判据的首次正向消费**:#125 轮的 corpus `function sum() for A=0,0 do end end return sum() or (sum())` 是简单的 `function` + `for` 循环 + `return`,budget 天然有界,直接入 `testdata/fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf` 常驻(fuzz worker 会把它当种子 mutation 探索周边形状),与 #123 的重 workload 走 `test/regression/` 形成对照。判据在这一轮首次被正向使用,证明可执行。反思 [[2026-07-11-issue125-return-freereg-round]] 教训 4。

**重 workload regression 测试的裁判机制:交给 `go test -timeout` 不要自建 per-run deadline**。走 `test/regression/` 路线的显式测试(如 `issue123_regression_test.go`)针对的失败模式是**永不返回**(#123 类段内无限循环),不是「合法路径慢过某阈值」。此时**不要**在测试内部用 `time.AfterFunc` / channel 类手法自建 per-run wall-clock deadline——那是量纲错配:测试想抓的是「非终止」,任何有限秒数都在跟共享 runner 的可变速度对赌,5s→30s→120s 的常量演进史本身就证明这类失败对常量修改免疫。正解是让测试直接跑裸的 `ProgramCall`,「永不返回」的判定交给 harness 自带的包级 `go test -timeout`(默认 10min,CI 未覆写)——它触发时严格更优:整个 test binary 被拿下 + 全部 goroutine 栈 dump,信息量比一行 `t.Fatalf("did not terminate within Ns")` 高一个数量级,同时 10min 相对合法 run(`-race` build 约 21s)的误报余量约 28×,比 in-test deadline 做得到的量级高得多。

自建 in-test deadline 只在这两种情况才有正当理由:(a) 测试**需要在超时后继续执行**(跑清理 / 对比结果 / 累积数据),或者 (b) 失败模式明确是「慢过某个具体阈值」而非「永不返回」。i123 两条都不属于,`issue123_regression_test.go` 因此在 PR #129(commit 706ba26)整段删掉了自建的 `runWithDeadlineErr`。同理:全仓其它已存在的 `mustFinish` / `runWithDeadline` 类模式(例如 `forloop_nan_limit_test.go` 的 10s deadline)按每处的误报余量分档处置——余量 < 10× 立刻换成包级 timeout,余量 10×~100× 作观察项,余量 > 100× 暂不动。反思 [[2026-07-12-i123-deadline-to-package-timeout-round]] 教训 1 + 教训 2。

### 2. 诊断硬化 —— 让下次复发自带诊断

无声外部 kill 之所以难查,是因为它没留下任何 in-process 证据——Go runtime 什么都来不及打,artifact
里只有一句 `exit status 2`。正确的投资方向是**把外部 kill 转成 in-process fatal**,或者**在被外部
kill 前 dump 系统状态快照**。#123 轮的两条硬化:

- `GOMEMLIMIT=6GiB`(在 `scripts/go-fuzz.sh` 或等价脚本里):压低 heap 峰值、让 GC 更早更积极地
  归还内存,降低撞上 OS OOM killer 的概率。**注意它是纯软限制**:`runtime/debug.SetMemoryLimit`
  文档明确「the application may still make progress」,Go runtime 在任何情况下都不会因它主动
  fatal——#123 轮曾误以为它能把无声 kill 转成带栈的 Go OOM,这个理解是错的(2026-07-18 复核);
- worker 无声死时 dump `free -m` + `vm.max_map_count`(以及等价的 OS 状态量)进上传 artifact,
  下次复发时 artifact 里就有当时的系统状态快照,不用对着 CI 日志一句话硬猜。

**Why**:低频罕见事件的调查成本大头不是「修」,是「等下一次复发」。等的时候免费,但复发时若信息不够
又要再等,循环下去。投资应该投在「让复发时的信息一次性够用」,不是投在「这次尽力挖」。

**硬化层级的最新状态(2026-07-19)**:`GOMEMLIMIT` 软限制已被观察到接不住这族死亡——2026-07-18
轮的 #156/#157/#159 三个 run 都已带上 PR #154 的 `GOMEMLIMIT=512MiB`,p4 worker 仍在约 4150 万
execs 处无声消失;2026-07-19 轮的 #162(concat storm 家族第 10 例)同样在 `GOMEMLIMIT=512MiB`
在场时于约 1240 万 execs 处静默死,本地重放 4.6 秒干净。软限制只影响 GC 节奏,既不会主动
fatal,也防不住分配速率超过 GC 回收速度时
RSS 冲过限制被 SIGKILL,更防不住非内存死因。

**第三层:worker 取证设施(PR #165,2026-07-19)**。原构想「harness 按 seed 记 wall-clock」
已被超集机制取代,交付两个机制(`fuzz_forensics_test.go`):

- **机制 A(尸检)**:TestMain 检测到 `-test.fuzzworker` 时把 fd 2 dup 到
  `fuzz-forensics/worker-<pid>-stderr.log` 并加 `debug.SetTraceback("all")`,接住此前被
  /dev/null 丢弃的 Go fatal 完整栈迹(合成 fatal 探针已验证栈迹确实进日志);
- **机制 B(飞行记录仪)**:每次 fuzz 回调(在各 target 的长度/NUL 检查**之后**——被 skip
  的输入不执行、不可能是真凶)把 seq / 时间戳 / target / 输入以单次 `WriteAt` 覆盖写进定长
  20KiB 的 per-PID 记录文件(容量覆盖最大 gated 输入 16KiB + header,逐字节可恢复,有单元
  测试钉住;热路径 0 alloc,`AllocsPerRun` 断言)。动机:不撞崩的 mutation 不会进任何
  corpus,而 minimized 输入又屡次被证明不是真凶——飞行记录是恢复「进程死亡时刻真正在跑的
  输入」的唯一手段;定长覆盖写,无 I/O 累积。

配套:`scripts/go-fuzz.sh` 按 target 隔离目录 `fuzz-forensics/<FuzzTarget>/`(经
`WANGSHU_FUZZ_FORENSICS_DIR` 传入,只清自己的目录——p1 job 先跑 native fuzz 再跑 oracle
fuzz,共享目录会让后者删掉前者的证据)、静默死亡失败时把栈迹日志 dump 进日志流并指向随
artifact 上传的原始飞行记录文件(不做文本过滤,防腐蚀 NUL/非 UTF-8 字节);nightly 失败
artifact 上传 `**/fuzz-forensics/**`。下一次家族复发时
artifact 里即有完整栈迹与在飞输入;若 stderr 日志仍只有 header(不是 Go fatal),则死因在
Go runtime 之外(如 coordinator 侧 pipe 断裂),同样是决定性的排除信息。
每一层没接住都是新信息,不是浪费。反思实例见
`memory/reflections/2026-07-18-issue155-158-nightly-crasher-round.md` 教训 4、
`memory/reflections/2026-07-19-issue163-tostring-meta-round.md`(#162 一节)与
`memory/reflections/2026-07-19-fuzz-worker-forensics-round.md`(机制 A/B 交付轮)。

**观察:minimized 输入本身往往不是死因(2026-07-19,#162)**。concat storm 家族累计 10 例,
本地精确重放全部干净——落盘的 minimized 输入更像「进程死亡时刻恰好在跑的那个」,真实压力更
可能来自 4 个 parallel worker 的叠加峰值,或 minimization 之前某个更重的 mutation 变体。这
也解释了为什么重放总是干净:重放只复现「单个 worker 跑单个 minimized 输入」这种最轻的情况。

## concat storm 家族已根因定性并修复(2026-07-20,PR #168)——真因是 CPU 看门狗,不是内存

上文各节把 concat storm 家族(#123-#167)当作「不可复现 / 疑似进程级资源耗尽(内存)」处理,
历轮处置是 corpus 入库 + 诊断硬化。**这段历史叙事本身有价值**(它建立了正确的止损流程、并催生
了下面结算的取证设施投资),保留;但它对死因性质的具体判断**后续被证伪**——真因是 CPU
wall-clock,不是内存。

**取证栈迹给出的定性(2026-07-20)**:#166/#167 两个 nightly p3 crasher 的 headSha 都是 PR #165
取证设施上线之后的 master HEAD——取证设施上线后家族**第一次复发**。两个 run 各有恰好一个长出
header 的 worker stderr 日志(机制 A 尸检接住了此前被 /dev/null 丢弃的 fd 2),抓到完整栈迹:
`panic: deadlocked!` + 一个 runnable goroutine 卡在
`gc.Collector.stringMatches → Intern → crescent.doConcat → executeLoop`。关键定性:
`panic: deadlocked!` 来自 Go fuzz 的 **per-input 看门狗**——`internal/fuzz/worker.go` 里
`time.AfterFunc(10*time.Second, panic)`,即单个 fuzz 输入跑过 10 秒就被打死。**死因是 CPU
wall-clock 撞 10 秒看门狗,不是内存 OOM**;历轮 `GOMEMLIMIT` / 降 arena cap 从来不奏效、最小化
corpus 本地重放永远干净,都因为死因根本不在内存这一维度。

**根因**:`preempt()`(state.go)在每个指令边界只把 stepUsed 加 1,**不计 CONCAT / Intern 的字节
工作量**。`for i=1,N do glob=cat(i) end`、cat 内 `return "<~15KB 字面量>"..i`,每次迭代只扣约 2 步
却拷贝 + intern 约 15KB;1<<20 步预算允许约 50 万次迭代,单次 prog.Run 约 2.7 秒字节工作,
`FuzzAutoPromote` harness 每输入跑 4 次 Run(2 State × 2 轮),4×2.7s≈11s > 10 秒看门狗 → worker
panic → CI 报 `exit status 2` → 自动开 crasher issue。落盘的是最小化后的**轻**输入(单次 Run < 10
秒),所以本地重放永远干净。

**修复(PR #168)**:共享 `doConcat`(`internal/crescent/call.go`)调 `chargeBulkWork(len)`
(`internal/crescent/state.go` 新增),按 `len >> 6`(1 步 / 64 字节)把 CONCAT 字节工作量折算进
step budget,使预算成为字节工作量的度量;三层(P1 executeLoop / P3 wasm h_concat / P4 native
host.Concat)全部路由同一 doConcat,单点记账覆盖所有 backend。回归 `issue166_concat_storm_test.go`
+ `issue166_concat_storm_tiered_test.go`,#166/#167 corpus 入 `testdata/fuzz/FuzzAutoPromote/`。
VM 行为侧的对账见 `docs/design/p1-interpreter/implementation-progress.md`。

**范围**:这解决的是这一族**已知形状**(byte-heavy concat 循环:单指令做与字节数成正比的无界工作、
只扣常数步数)。它**不**代表所有不可复现 crasher 都已解决——本 guide 的止损流程、静默死亡两步
分类、诊断硬化三层仍是遇到新的不可复现 crasher 时的默认路径;已知的下一批无界单指令算子
(string.rep / string.format / table.concat)尚未按工作量记账,是同类风险的候选。

**这印证了取证设施投资的价值**。PR #165 的赌注是「让复发时信息一次性够用」——取证设施上线到家族
复发之间隔了不到两天,而家族此前空转了数周。上文诊断硬化第三层的「每一层没接住都是新信息,不是
浪费」这条纪律在本轮正面结算:机制 A 的 worker stderr 栈迹 + 机制 B 的飞行记录合起来一步定性,把
横跨数周的「查不出死因」变成一行日志的「根因清晰」。反思
`memory/reflections/2026-07-20-concat-storm-root-cause-round.md`(取证兑现 + 静默死亡两步分类直接
给答案 + 家族级误分类可持续数周,真值来自第三方证据而非表象反推 + 指令预算须度量工作量而非条数)。

**How to apply**:遇到「本地无法复现 + 生产 / CI 出现」类问题,除了调查根因之外,并行考虑:能不能加
一个便宜的诊断改动,让下次复发时留下更多信息?能就先做诊断硬化,再看要不要继续挖这次。

**教训来源**:反思 [[2026-07-11-issue123-unreproducible-crasher-round]] 教训 3。

## 与其他 guide 的关系

- 与 [[prove-the-path-under-test]] 互补:那篇管**可复现问题的修复怎么修对**(证在测的路径真被
  走到、证被归因的路径真的存在、证收益来自稳态生效);本篇管**不可复现问题怎么止损**(判定为
  不可复现后不硬编修复,corpus 入库 + 诊断硬化让下次复发自带信息)。
- 与 [[cross-backend-semantic-fix-sweep]] 互补:那篇管**修同一语义类 bug 时枚举全部后端 × 通道**;
  本篇的判定前提是「input 决定的 VM bug 与进程级资源耗尽已经分开」,分开之后属于 VM bug 的那类才
  可能进入 cross-backend sweep 的范围。
- 反思实例:`2026-07-11-issue123-unreproducible-crasher-round`(七角度复现矩阵 + 分诊 + corpus
  入库 + `GOMEMLIMIT` 诊断硬化) · `2026-07-03-issue40-arm64-stopbleed-round` §「其它(较小)」
  fuzz 失败形式分诊纪律(deadline vs failing-input 判据的来源)。
