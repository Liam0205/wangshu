---
name: 2026-07-19-fuzz-worker-forensics-round
description: >
  2026-07-19 fuzz worker 取证设施轮(分支 feat/fuzz-worker-forensics,PR #165,
  用户主动立项:彻底解决 concat storm 家族「查不到死因」的问题)。破局线索:
  家族 10 例的死亡签名全是 "exit status 2"——这是 Go runtime 自身 fatal 的
  退出码(OS OOM killer 的 SIGKILL 会报 "signal: killed"),worker 死时几乎
  一定打印了完整栈迹;而 internal/fuzz 起 worker 时 cmd.Stderr 留 nil,
  os/exec 把它接到 /dev/null——尸检报告每次都写了,每次都被扔掉。问题性质
  从「查不到死因」变成「输出没接住」。交付两个机制:A 尸检(TestMain 检测
  -test.fuzzworker,把 fd 2 dup 到 fuzz-forensics/worker-<pid>-stderr.log,
  合成 fatal 探针验证栈迹确实进了日志)+ B 飞行记录仪(每次 fuzz 回调把
  seq+时间戳+target+输入以单次 WriteAt 覆盖写进定长 8KiB per-PID 记录,恢复
  「真正在飞的输入」)。教训 1(头条):对静默死亡,先分类退出方式(exit
  code vs signal),再查子进程的 stdio 接线——两步只要几分钟,却解开 8 天
  10 例的僵局;此前多轮都在复现矩阵和内存限制上打转,没人查过 exit status 2
  的语义。教训 2:取证设施必须 best-effort(错误全吞,绝不把健康的 fuzz run
  变成失败),且不能反过来给被诊断的问题添加压力(review 抓出每 exec 分配
  8KiB buffer 造成 GC churn,改包级复用)。
metadata:
  type: reflection
  date: 2026-07-19
---

# 2026-07-19 fuzz worker 取证设施轮反思(PR #165)

> 范围:分支 `feat/fuzz-worker-forensics`,PR #165,三个 commit
> `34fdeb5` / `5fe6af6` / `6a189eb`。用户主动立项:彻底解决 concat storm
> 家族(#123/#144/#145/#150-#152/#156/#157/#159/#162,累计 10 例)
> 「查不到死因」的问题。

## 任务

concat storm 家族的 nightly fuzz worker 静默死亡已累计 10 例,横跨 8 天,
每次都是「minimized 输入本地重放干净 + worker 几千万 execs 后无声消失」,
此前各轮按 [[unreproducible-crasher-triage]] 处置(corpus 入库 + GOMEMLIMIT
诊断硬化),但 `GOMEMLIMIT=512MiB` 已被观察到接不住,家族计数持续上升。
本轮目标:不再猜死因,给下一次复发装上足够的诊断设施,让 artifact 里
自带证据。

## 期望与实际

- 期望:立项时的预设方向是上一轮反思提出的「harness 按 seed 记
  wall-clock」,把「进程在哪个 seed 之后消失」变成可读线索。
- 实际:动手前的两步廉价检查直接改变了问题性质,交付了远强于原构想的
  两个机制。

### 破局线索(本轮最重要的认知)

两个此前从没人查过的事实,合起来把「查不到死因」翻译成「输出没接住」:

1. **死亡签名的语义**:家族 10 例的失败消息全是 `fuzzing process hung or
   terminated unexpectedly: exit status 2`。exit status 2 是 Go runtime
   自身 fatal(panic / fatal error)的退出码;若是 OS OOM killer 的
   SIGKILL,coordinator 会报 `signal: killed`。也就是说 worker 死的时候
   几乎一定打印了完整栈迹——死因不是「无声」,是有一份完整的尸检报告。
2. **子进程的 stdio 接线**:查 internal/fuzz 源码,coordinator 用
   `exec.Command` 起 worker 时 `cmd.Stderr` 留 nil,os/exec 把它接到
   /dev/null——**尸检报告每次都写了,每次都被扔掉**。

这两步(exit 方式分类 + stdio 接线检查)各只要几分钟,却解开了 8 天
10 例的僵局。此前多轮都在「七角度复现矩阵」「GOMEMLIMIT 内存限制」上
打转,没有一轮查过 exit status 2 的语义——最便宜的检查被排在了最后,
与 [[unreproducible-crasher-triage]] 「第一步永远是版本核对」教训的
结构一样:先做几分钟量级的分类检查,再撒几十分钟量级的矩阵。

### 机制 A(尸检):接住 worker 的 stderr

根包新增 `fuzz_forensics_test.go` 的 TestMain,检测 `os.Args` 里的
`-test.fuzzworker`;worker 模式下把 fd 2 dup 到
`fuzz-forensics/worker-<pid>-stderr.log`,并加
`debug.SetTraceback("all")`。平台细节:linux 用 `syscall.Dup3`
(linux/arm64 是 nightly CI 架构,Go syscall 包在该平台没有 Dup2),
darwin 用 `Dup2`,其它平台静默降级。

验证方式是 prove-the-path 应用于诊断设施本身:用合成 fatal
(goroutine panic 杀进程,退出码同为 exit status 2)当探针,确认完整
栈迹确实进了 per-PID 日志、coordinator 侧照旧只报不透明错误——即
「设施装上了且真的接得住」不是靠推理,是靠探针证明的。

### 机制 B(飞行记录仪):恢复真正在飞的输入

每次 fuzz 回调开头,把 seq + 时间戳 + target + 输入用单次 `WriteAt`
覆盖写进定长 8KiB 的 per-PID 记录文件。关键动机:不撞崩的 mutation
永远不会写进任何 corpus,而 minimized 输入又屡次被证明不是真凶
(guide 已记录:重放干净更可能因为真实压力来自 parallel worker 叠加
峰值或 minimization 之前的变体)——飞行记录是恢复「进程死亡时刻真正
在跑的输入」的唯一手段。定长覆盖写,没有 I/O 累积。

### 配套

- `scripts/go-fuzz.sh` 每个 target 先清 `fuzz-forensics/`(防跨 target
  误归因);静默死亡失败时,把长出 header 的 worker stderr 日志 + 全部
  飞行记录 dump 进日志流;
- nightly workflow 的失败 artifact 加 `**/fuzz-forensics/**`;
- `.gitignore` 加该目录。

## 踩坑与教训

### 教训 1(头条):静默死亡三步——先分类退出方式,再查子进程 stdio 接线,最后才谈复现矩阵

对「进程静默死亡」类问题,有两个几分钟量级的检查应该永远排在复现矩阵
之前:

1. **退出方式分类**:exit code 还是 signal?exit status 2 = Go runtime
   自身 fatal(有栈迹);`signal: killed` = 外部 kill(真的无声)。
   这一步直接决定「有没有尸检报告可找」;
2. **子进程 stdio 接线**:如果判定有输出,查它被接到了哪里——本例
   internal/fuzz 的 `cmd.Stderr` 是 nil,输出进了 /dev/null。

**Why**:此前多轮的调查资源全部投在了「复现」和「限制资源」上,隐含
假设是「死因没有留下证据,只能重新制造一次」。但退出码语义说明证据
每次都产生了,只是没被接住——问题类别判断错了,后续投资全部错位。

**How to apply**:fuzz / CI / 生产报「进程无声消失」时,第一反应不是
撒复现矩阵,而是:① 死亡消息里的退出方式是什么语义;② 若有输出,
子进程的 stdout/stderr 被父进程接到了哪里。两步做完再决定要不要复现。

### 教训 2:取证设施必须 best-effort,且不能给被诊断的问题添加压力

设计原则:所有错误吞掉,绝不能把健康的 fuzz run 变成失败;非 worker
模式下 TestMain 零开销,`recordFuzzExec` 退化为一次 nil 检查。

review 一轮过,bot 唯一 finding 恰好击中这条原则的另一半:第一版每次
exec 分配一个新的 8KiB buffer,给 fuzz 负载加了持续的 GC churn——而
我们正在调查的正是这个负载的内存压力。诊断设施反过来给被诊断的问题
添加压力,轻则扰动观测,重则自己成为诱因。修法(commit `6a189eb`)
改为包级复用 buffer;安全性依据是 fuzz 回调在单个 worker 进程内串行。

### 展望:装好之后是被动等待,两种结果都是决定性信息

下一次家族复发时,artifact 里就有完整栈迹 + 在飞输入,届时凭证据修
真 bug。若复发时 stderr 日志仍然只有 header(即不是 Go fatal),那
同样是决定性信息:说明死因在 Go runtime 之外(如 coordinator 侧 pipe
断裂),排除法一样前进了一大步。每一层没接住都是新信息,不是浪费。

## 流程

exit status 2 语义确认 → internal/fuzz 源码核对(cmd.Stderr nil)→
机制 A(TestMain + dup fd 2,合成 fatal 探针验证)→ 机制 B(飞行
记录仪)→ 配套(go-fuzz.sh 清理与 dump + nightly artifact +
.gitignore)→ PR #165 → CI 绿 → bot 唯一小问题(8KiB buffer GC
churn)修掉(`6a189eb`)→ 增量 APPROVE。review 一轮过,无返工。

## Promotion 候选

- **教训 1(静默死亡三步)**:建议并入 [[unreproducible-crasher-triage]]
  的处置流程——它直接改变了该 guide「七角度复现矩阵」的优先级排序,
  exit-code 语义检查应该排在最前(与既有「第一步永远是版本核对」同
  量级,都是几分钟排除最便宜解释)。样本虽是首次,但它一步解开了
  10 例僵局,信号强度足够,建议本轮就提升,由 recorder 决定措辞与落点。
- **机制 A / B 的存在**:应写进该 guide 的诊断硬化层级(第三层),
  取代此前「harness 按 seed 记 wall-clock」的构想——飞行记录仪是它的
  超集(不止记「跑到了哪个 seed」,还记完整输入与时间戳)。guide 当前
  「下一层升级方向是按 seed 记 wall-clock」一句已过期,须更新。
- **教训 2(取证设施 best-effort + 不给被诊断问题添压)**:首次成文,
  暂留 memory;若后续再有诊断设施扰动被观测对象的实例,可考虑提升。

## 触发场景

- fuzz / CI / 生产报「进程无声消失 / hung or terminated unexpectedly」
  时(教训 1:先分类退出方式——exit code 语义 vs signal,再查子进程
  stdio 接线,最后才撒复现矩阵);
- 给低频复发问题设计诊断硬化时(机制 B 的动机:不撞崩的输入不进
  corpus,minimized 输入未必是真凶,「在飞输入」只能靠飞行记录恢复;
  定长覆盖写避免 I/O 累积);
- 给 fuzz / 测试基础设施加任何常驻观测代码时(教训 2:错误全吞、
  非目标模式零开销、热路径不引入分配——诊断设施不能成为被诊断问题的
  一部分);
- concat storm 家族下次复发时(先看 artifact 里的
  `fuzz-forensics/worker-<pid>-stderr.log` 与飞行记录;stderr 只有
  header 也是决定性信息,指向 Go runtime 之外的死因)。

## 关联

[[unreproducible-crasher-triage]](本轮是该 guide 诊断硬化层级的直接
升级,教训 1 与机制 A/B 均候选并入)· [[prove-the-path-under-test]]
(合成 fatal 探针 = 对诊断设施本身证明路径可达)·
[[2026-07-19-issue163-tostring-meta-round]](#162 一节,家族第 10 例
与「per-seed wall-clock 优先级上升」的前序记录)·
[[2026-07-18-issue155-158-nightly-crasher-round]](GOMEMLIMIT 没接住
的第一次观察)· PR #165 · commit 34fdeb5(机制 A + B)· commit
5fe6af6(harness dump + CI artifact)· commit 6a189eb(buffer 复用)

## 附记(合入前外部审查轮:取证设施自己丢证据的三种方式)

外部增量审查(`.code-review/from-2fe2c37/`)对 PR #165 提出三条重要建议,
全部属实并已修复(commit 007b1ab):

1. **记录容量小于输入上限**:飞行记录定长 8KiB,但三个 target 的长度
   门在 16KiB——8 到 16KiB 之间的合法 mutation 若是真凶,记录里只剩
   前缀,尾部永久丢失,而这恰是「恢复不进 corpus 的在飞输入」这一核心
   用途的破坏。修复:容量提到 20KiB(最大 gated 输入 + header),
   recordFuzzExec 移到各 target 长度/NUL 门之后(被 skip 的输入不执行
   、不可能是真凶),并加 TestFlightRecordMaxInputRecoverable 断言
   16KiB 任意字节输入逐字节可恢复。
2. **热路径分配污染被观测对象**:fmt + time.Format 路径每次执行分配
   88 B——上一轮 review 已抓过一次 buffer 分配,这轮又在格式化路径
   抓到残余。改用 strconv.Append* / Time.AppendFormat 全程写入复用
   buffer,TestFlightRecordZeroAllocs 用 AllocsPerRun 钉在 0。
3. **跨调用清理删证据**:nightly p1 job 先跑 native fuzz 再跑 oracle
   fuzz(always()),artifact 上传在两者之后;共享目录每次调用无条件
   rm -rf,oracle 调用会把 native 失败的尸检文件先删掉。修复:每个
   target 独立目录 fuzz-forensics/<FuzzTarget>/(经
   WANGSHU_FUZZ_FORENSICS_DIR 传入),只清自己的;顺带修掉 dump 路径
   的 sed/grep 文本过滤(会腐蚀 NUL/非 UTF-8 字节——飞行记录里可能是
   唯一一份真凶输入),改为只打印可打印的 header 行、指向随 artifact
   上传的原始文件。

**教训 H(三条的公共模式)**:取证设施的审查焦点应该是「它会不会自己
丢证据」——容量边界(能装下最大的证据吗)、观测扰动(会不会改变被观
测对象)、生命周期(证据活得过收集流程吗)。三条 finding 全是这三问
的实例;设计取证/诊断类设施时按这三问自查,能在 review 前就消掉这类
缺陷。另外「被 skip 的输入不可能是真凶,所以记录点放在门之后」这个
论证方向值得记住:取证点的正确位置由「什么能杀死进程」决定,不是
「越早越全」。
