---
name: 2026-07-24-p4-template-forprep-deopt-round
description: >
  nightly-fuzz 巡检轮，PR #178，修 P4 FuzzAutoPromote crasher #177。触发脚本
  `function sum(n) for A=0,n do end end sum "7"`：auto run 2 报「'for' initial
  value must be a number」，P1 解释器正确 coerce `"7"` 为 7 通过。分诊路径曲折
  ——先按 traceback 怀疑 PJ10 native emit 里 FORPREP 漏了字符串 coerce；加打印
  到 host.ForPrep 看到 R(A)/R(A+1)/R(A+2) 全是 Nil；加 `NativeRunCount` 探针
  发现 auto 两个 run 都是 0，一步排除 PJ10 native 方向；追到 p4Code
  shape-template（compiler.go）的 MOVE-limit 快路径把 init/step 烧成 imm64、
  limit 只进 XMM 从不写回 slot，deopt 路径直接调 host.ForPrep 于是读到 Nil。
  修复：p4Code 增 `forLoopInitK` / `forLoopStepK` 两个 uint64 字段（从
  shapeInfo.forInitK / forStepK 传入 NaN-box），deopt 前 `SetReg(A, initK)` /
  `SetReg(A+1, GetReg(limitReg))` / `SetReg(A+2, stepK)` 恢复 interpreter-shape
  再调 host.ForPrep，helper 就能正确 coerce 字符串 limit，或对真非 number 报
  正确的「limit」错误信息（byte-equal 于 P1）。
metadata:
  type: reflection
  date: 2026-07-24
---

# nightly-fuzz P4 template FORPREP deopt slot 未恢复轮（2026-07-24，PR #178，#177）

> 范围：nightly-fuzz 巡检轮，PR #178。修 1 个 FuzzAutoPromote crasher #177。
> 改动集中在 `internal/gibbous/jit/code.go`（p4Code 结构 + deopt 路径 SetReg
> 恢复）与 `internal/gibbous/jit/compiler.go`（shapeInfo.forInitK / forStepK
> 传入）；`test/regression/issue177_regression_test.go` 钉住 crasher 与真非
> number limit 的错误信息 byte-equal；#177 corpus 入
> `testdata/fuzz/FuzzAutoPromote/8305a8ceb22b8f41` 常驻。

## 任务

nightly 长时间运行的 FuzzAutoPromote（P4 tier 促升差分测试）撞出稳定可复现
的 crasher #177：

```lua
function sum(n) for A=0,n do end end sum "7"
```

auto run 2 报「'for' initial value must be a number」；P1 解释器正确把 `"7"`
coerce 为 7 后循环通过。要定位 P4 路径上 for-loop 的 init/limit/step 处理
错在哪、报错位（init vs limit）错在哪，并修复到与 P1 byte-equal。

## 本轮做了什么

1. **分诊起点误判**：按 auto run 2 的 traceback 直觉先怀疑 P4 PJ10 native
   emit 里 FORPREP opcode 漏了字符串 coerce（PJ10 是 2026-07-01 起做的 CFG +
   label resolver + 35 op × amd64/arm64 native emit）。
2. **加打印到 host.ForPrep**：R(A)/R(A+1)/R(A+2) 三个 slot 读出来都是 Nil
   （NaN-box tag=65528），三个 slot 从未被写。说明 helper 报「initial value
   must be a number」不是 helper 有 bug，而是 slot 全为 Nil，第一个失败的
   Nil-non-number 检查落在 init 上，报错位随之落在 init。
3. **加打印到 `RefreshJitCtxAddrs`**：vsBase 计算正确（caller 传 9416 ==
   `th.stackBaseW+ci.base`），排除 base 刷新错位。
4. **加 `NativeRunCount` 探针**：auto 两个 run 都是 0——决定性证据，sum 根本
   没走 PJ10 nativeCode.Run，而是走的老 shape-template `p4Code`（compiler.go
   路径）。一步把定位面从「PJ10 native emit / PerOpCode spec-template /
   p4Code shape-template」三大类代码收窄到 1 类。
5. **追 `analyzeForLoopForm` 的 MOVE-limit shape**：template fast-path 把
   init/step 烧成 imm64（编译期常量直塞机器码），limit 只读进 XMM 参与比较
   从不写回 slot。正常快路径无 bug；一旦触发 deopt（本轮触发条件：limit 是
   字符串，非 number 快路径拒），代码直接 `host.ForPrep(base, pc, forLoopA)`
   ——helper 期望 slot 是 interpreter-shape 的 init/limit/step，就读到全 Nil。
6. **确认 shapeInfo 字段编码**：改深层数据流前先读 compiler.go 里
   `shapeInfo.forInitK` / `forStepK` 的定义 + template emit 里的消费点，确认
   它们就是 `uint64(kInit)` 直传的 NaN-box u64，与 `SetReg(idx, u64)` 的入参
   编码完全对上，可以直接透传。
7. **修复**：`p4Code` 结构加 `forLoopInitK` / `forLoopStepK` 两个 uint64 字段；
   compiler.go 构造 p4Code 处从 shapeInfo 填这两个字段；code.go 的 deopt 路径
   在 `host.ForPrep` 前 `SetReg(A, forLoopInitK)` /
   `SetReg(A+1, GetReg(limitReg))` / `SetReg(A+2, forLoopStepK)`，把三个 slot
   显式恢复到 interpreter-shape，再调 helper。helper 于是能正确 coerce 字符串
   limit（`"7"` → 7 通过），也能对真非 number limit 报正确的「'for' limit
   must be a number」错误（byte-equal 于 P1）。
8. **测试**：`test/regression/issue177_regression_test.go` 两个场景钉住
   ——crasher 本体（字符串 limit 通过）与真非 number limit（table）的错误
   信息 byte-equal。#177 corpus 入 `testdata/fuzz/FuzzAutoPromote/` 常驻。
   全套件（default/oracle/p3/p4）绿。
9. **合入前外部审查 BLOCKER 附记**（commit b00cb00）：步骤 7 修复只把
   slot 恢复好后调 `host.ForPrep`，随即 `DoReturn`——空 body FORLOOP 的迭代
   **根本没跑**。P1 解释器每轮 FORLOOP 都会 `preempt()`（step budget 计费 +
   cancel context 探测），初版让 P4 deopt 路径瞬间返回：`sum "1000000"` +
   小 budget 下 P1 报「instruction budget exceeded」而 P4 直接返回、
   `SetContext` 超时同样被跳过；宿主任何依赖 preempt 的资源限制都失效。
   修法追加 `host.ForLoop(base, pc, a)` helper 复刻 execute.go FORLOOP 语义
   （`idx += step` / 比较 limit / preempt / 写 `R(A)` 与 `R(A+3)` / 循环），
   code.go deopt 分支改为 `ForPrep → ForLoop → DoReturn`，任一 helper raise
   在 DoReturn 前 bubble。回归测试新增 budget-exceeded + context-canceled
   两个场景（`sum "1000000"` + 4096 步 budget、5ms ctx 超时），两个都以
   `PromotionCount` + `SpecForLoopDeoptHits` 双探针防止静默替身路径——
   促升门 / shape 匹配 / 超时先后关系变化让 auto 静默留在 P1 时同样能拿到
   期望错误信息通过。

## 期望与实际

- 期望：按 auto run 2 traceback 一头扎进 PJ10 native emit 的 FORPREP，改一处
  字符串 coerce。
- 实际：`NativeRunCount=0` 一步证明 PJ10 native 不是当前 code kind，真相是
  老 p4Code shape-template 快路径 + deopt 路径的 slot-shape 错配——快路径为
  性能省了 init/limit/step 三个 slot 的 spill，deopt 路径直接调 helper，
  helper 就读到 Nil。「force-all 通过」是因为促升条件不同，sum 促升到别的
  code 或 fast-path 走通没进 deopt，与本 bug 所在通道无关。

## 教训

### 教训 1（P4 有多种 code kind，分诊必须先用探针确认走的哪条）

P4 build 里可能是 PerOpCode（spec-template）、nativeCode（PJ10）、或
p4Code（compiler.go legacy shape-template）——各自有独立的 fast-path 假设与
deopt 路径。

**Why**：看到 P4 auto crasher 直接读一种 code 的 emit 是猜。本轮
`NativeRunCount` 探针一次读数（auto 两个 run 都是 0）就把定位面从 3 大类
代码收窄到 1 类，省下猜 PJ10 native emit 的时间。

**How to apply**：碰上 P4 tier bug，先加或运行现有探针（`NativeRunCount` /
`DispatchHelperCount` / PerOpCode debug 打印）分类「走哪条 code」，再深挖
对应文件的 emit 代码。承 [[prove-the-path-under-test]] §4「白盒探针分类」
纪律。

### 教训 2（优化后的 fast-path template 里，deopt 路径必须显式恢复省掉的 slot 到 interpreter-shape 再调 host helper）

fast-path template 为了性能把 init/step 烧成 imm64，把 limit 只留 XMM
——正常无 bug，但一旦 deopt 到 host helper，helper 期望 interpreter-shape
slot 有值，就会读到 Nil。这是「optimizer 假设的 shape」与「helper 假设的
shape」的错配。

**Why**：host helper 是共享层实现，它的入参约定就是 interpreter-shape slot。
快路径省 spill 是本地优化，deopt 是通往共享层的出口，出口处必须把状态还回
共享层约定的形式。碰到 helper 报意料之外的错误信息（如本轮 host.ForPrep 该
报「limit」却报「initial value」），红旗是 slot 被读到默认值（Nil / 0）而
不是真值，不是 helper 有 bug。

**How to apply**：任何 fast-path template 里省略 spill 的 slot，deopt 路径
都要显式 SetReg 恢复到 interpreter-shape 再调 host helper。审 fast-path 时
把「哪些 slot 在快路径里不写、哪些 slot 是 helper 期望有值的」列出来，两个
集合的交集就是 deopt 前必须恢复的 slot 集合。承
[[cross-backend-semantic-fix-sweep]]「快路径不能绕过 helper 语义」。

### 教训 3（「A 场景通过 + B 场景失败」只定位触发前提，不能直接推「bug 在哪条代码路径」）

一开始「force-all 通过 vs auto 失败」让我以为是 PJ10 native emit 有 bug（
以为 force-all 走 native = OK、auto 也走 native 但通道略不同）。真相是 auto
和 force-all 促升条件不同，sum 促升到的 code kind 也可能不同——auto 下促升
到 p4Code shape-template + limit 是字符串所以进 deopt；force-all 下可能促升
到别的 code 或 fast-path 走通没进 deopt。

**Why**：tier 分诊里两个场景的通过/失败结果由三个变量共同决定——促升条件、
促升到的 code kind、该 code kind 的 fast-path 是否走通。单看「A 通过 B 失败」
只锁定了「触发前提」（限定 A 与 B 的输入差异），不锁定「哪条代码路径」。

**How to apply**：tier 分诊里「哪个场景通过、哪个失败」只能定位 bug 的触发
前提，不能推断代码路径。触发前提确定后，仍要用探针（`NativeRunCount` 之类）
证走哪条实际代码。承 [[prove-the-path-under-test]]「先证在测路径」纪律。

### 教训 4（改深层 JIT 数据流前，先读所选字段的定义 + 全部消费点，确认语义/编码与新用途一致）

修复要用 `shapeInfo.forInitK` / `forStepK`。我最初以为这是「burn 出来的
imm 位模式」，但需要确认它是不是 NaN-box（直接 SetReg 用）还是别的编码。
读 compiler.go 里字段定义 + template emit 里的消费点后确认它就是
`uint64(kInit)` 直传的 NaN-box u64，与 `SetReg(idx, u64)` 的入参编码完全对
上，可以直接透传。

**Why**：JIT 数据流里字段编码不一致（NaN-box vs 原始 f64 位模式 vs
mov-immediate 位模式）非常容易撞车，猜错了引入沉默的 tag 错乱远比编译错误
难查。花几分钟读定义 + 全部消费点，是把「猜」换成「读」的低成本手段。

**How to apply**：改深层 JIT 数据流时，先花几分钟读所选字段的定义 + 全部
消费点，确认它的语义/编码与新用途一致，再改；如果消费点用法不止一种，需要
写出所有使用形式的对齐关系。承 [[2026-07-11-issue125-return-freereg-round]]
「共享层 codegen 假设与 emit 状态错配」的旧例子。

### 教训 5（deopt 分支替原字节码 return 前，先列被替代字节码的可观察副作用清单）

初版修复只做了「让 host.ForPrep 看到正确的 slot」，随即 DoReturn 结束函数
——但被替代的字节码是「FORPREP + FORLOOP + RETURN」三条，不只是 FORPREP
一条。FORLOOP 的可观察副作用不止是空 body 本身（body 空所以无副作用），
还包括每轮迭代的 `preempt()`：step budget 记账 + cancel context 探测。空 body
让「结果 byte-equal」这一个 assertion 抓不住 iteration 全被跳过——`sum "7"`
只跑七次迭代，结果差在人眼看不出的字节里；把 limit 换成 `"1000000"` + 小
budget，divergence 才浮出来（P1 raise，P4 返回）。

**Why**：deopt 路径本质是「用 helper 补齐 fast-path 没做完的字节码语义」；
只补 slot 而不补 iteration 循环，等于把 FORLOOP 的语义丢了。P1 与 P4 的
byte-equal 契约不只是 result byte-equal，还包括 side-effect byte-equal——
preempt / cancel ctx / body write / stack shape 都在契约里。审查侧一句
「iteration 观测不到就无法证明 iteration 真的跑了」直接把这条盲区照出来。

**How to apply**：deopt 分支写 DoReturn 前，逐条列出「被替代的字节码序列
里，每条指令的可观察副作用是哪些」，逐条对应到 helper 调用：

- 副作用集合 = { 写 register / 写 upval / 写 global / preempt (budget+ctx) /
  GC safepoint / IC 命中记账 / body 有 Lua call 时的 frame 变化 / traceback
  的 pc 状态 / ... }
- helper 覆盖矩阵 = 上面每条哪些 helper 已经做了、哪些没有

回归测试至少覆盖 preempt 两条通用副作用（step budget + cancel context），
它们不需要脚本副作用可观察，只要 loop 足够长就能触发——这两个 case 天生对
「iteration 被跳过」类 bug 敏感，是 deopt-fold-a-loop 家族的通用守门。

**测试用双探针防静默替身**：`PromotionCount` + `SpecForLoopDeoptHits`。前者
证明 auto 真进了 P4，后者证明 P4 里真走了对应 deopt 分支——没这两个探针，
促升门 / shape 匹配 / ctx timeout 先后关系变化让 auto 静默留在 P1 时，同样
的错误信息（interpreter 自己 raise 的 budget/ctx 错）会让 test 假绿。承
[[prove-the-path-under-test]] §7.1 复现侧纪律。

## 承接与链接

- [[prove-the-path-under-test]] §4 白盒探针分类：本轮 `NativeRunCount`
  探针一步收窄定位面，与该 guide「先证在测路径」纪律一致；§7.1 复现侧
  纪律，教训 5 的双探针防静默替身与之直接对应。
- [[cross-backend-semantic-fix-sweep]]：快路径与 helper 语义对齐，本轮
  fast-path 省 spill + deopt 直调 helper 是同一族错配；BLOCKER 附记补的
  「语义完整性契约」是同族第二例（不只 slot shape，还有被替代字节码
  副作用清单）。
- [[2026-07-11-issue125-return-freereg-round]]：共享层 codegen 假设与 emit
  状态错配的老实例，本轮 deopt 路径读到 Nil slot 属于同族「优化侧假设 vs
  helper 约定」错配。

## 提升候选

- 教训 1（P4 code kind 分诊探针清单）与教训 2（fast-path template deopt
  slot 恢复约定）如未在 [[prove-the-path-under-test]] 与
  [[cross-backend-semantic-fix-sweep]] 里显式登记，可考虑升到 guides 里补一
  条「P4 template deopt 前 slot 恢复清单」子节。
- 教训 5（deopt 分支替字节码 return 前列可观察副作用清单）是 [[cross-backend-semantic-fix-sweep]]
  「fast-path template 与 host helper 契约」的第二层：本轮附记已在 guide
  写「slot 契约」，教训 5 加的「副作用完整性契约」值得升到该 guide 一起
  作为「优化后 fast-path template 与 deopt 直调 helper 的两条契约」并列
  段落。回归测试的 budget + ctx 双 case 也可以在 [[prove-the-path-under-test]]
  里作 deopt-fold-a-loop 家族的通用守门推荐。
- 教训 3、教训 4 目前留在本反思即可，属于本轮具体经验。

## 后续动作

- 审计 p4Code 里所有其他 shape-template 的 deopt 路径，检查是否也有类似
  「快路径省 spill + deopt 直调 helper」的错配、或「deopt 直接 DoReturn 跳过
  原来还有的字节码语义」（本轮同时踩到两条，只修 FORPREP + FORLOOP 一对，
  其他 op 未系统检查）。
- 追 P4 PJ10 native emit 侧的 FORPREP 路径，确认它的 deopt 是否也用 host
  helper、slot 恢复约定是否已就位（本轮已排除它不是 #177 的通道，但语义
  一致性值得单独核对）。
