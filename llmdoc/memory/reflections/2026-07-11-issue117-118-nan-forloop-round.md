---
name: 2026-07-11-issue117-118-nan-forloop-round
description: >
  issue #117/#118 修复轮(2026-07-11,PR #119,closes #117 #118):nightly go-fuzz 两个 crasher
  同根因——`for A=0,0%0 do end` 常量折叠 0%0 出 NaN limit,PJ3 字节级 FORLOOP spec 模板的退出比较
  `ucomisd idx,limit; ja` 在 unordered 上永假(ucomisd 置 CF=ZF=1),mmap 段死循环;单线程 run 里
  没人置 safepoint 抢占位,spec 模板通道又没有 step 计费点,自救不了。修法三处:amd64 换比较序 +
  `jb`(CF=1 覆盖 limit<idx 与 unordered)、arm64 CondGT→CondHI(HI 在 unordered 下真)、analyzer
  step 门 `step<=0` 改 `!(step>0)` 让 NaN step 也走拒绝。核心教训:① 手写浮点比较 + 条件跳转的第
  二例同族 bug(第一例 #103 inline compare),凡涉及 ucomisd/fcmpe 后接条件跳转都必须显式回答
  「NaN 时跳到哪」,修复手法族「换操作数序,让 unordered 落在跳转触发侧」;② `x<=0` 与 `!(x>0)`
  在 NaN 上不同值,shape gate 必须按「拒绝侧默认」写(比较为假时应落慢路径 / 拒绝);③ 「同语义
  多通道 emit」是 cross-backend-semantic-fix-sweep 的通道内变体(per-op FORLOOP 早修好,spec 模板
  漏网),同类扫描不只跨架构也跨通道;④ prove-the-path 又立功:初版测试载体带非空 body 落到没坏
  过的 per-op 通道,SpecForLoopHits delta 断言当场抓出空测。
metadata:
  type: reflection
  date: 2026-07-11
---

# issue #117/#118 修复轮反思(2026-07-11,PR #119)

> 范围:分支 `fix/issue117-118-nightly-crashers`,commit `45ada6a`。nightly go-fuzz 两个 hang
> crasher(`FuzzAutoPromote` seed `eb8fb93a433d40b2` / `FuzzP4ForceAllPromote` seed
> `5159747aad201f47`)同根因,一次修完。

## 任务

nightly 两个 crasher hang worker / minimizer,最小复现形式一致:`for A=0,0%0 do end`。常量折叠
让 `0%0` 变 NaN,该 NaN 是低于 NaN-box tag 空间的合法数值,顺利通过 PJ3 字节级 FORLOOP spec
模板的 `analyzeForLoopForm` 数值门,进段执行时退出比较永假,mmap 段死循环。

## 期望与实际

- 期望:nightly 两个 seed 应正常退出(PUC 语义 `NaN < 0` / `NaN > 0` 都为 false,for 循环条件不
  成立,零迭代)。
- 实际:mmap 段死循环。修法确认后先写三形状回归测试(限 10s 超时),测试如预期 fail;修完
  amd64 四处 + arm64 四处 + analyzer 三处后测试转绿,两个 nightly corpus 重放通过,三 build
  全量绿。

## 根因与修法

- **根因**:PJ3 spec 模板通道退出比较 `ucomisd idx, limit; ja exit`,ucomisd 在 unordered
  (NaN)条件下置 CF=ZF=PF=1,`ja` 需要 CF=0 && ZF=0——永不跳,段内死循环。单线程 run 里没人
  置 preempt 标志,spec 模板通道也没有 step budget 计费点,两条兜底路径都救不了。arm64 镜像同
  bug:`fcmpe; b.gt` 里 GT 在 unordered 下为 false,同样循环下去。per-op 通道的 emitFORLOOP 早
  已处理 unordered(#103 轮那一批修的),spec 模板漏网。
- **修法**(同一 PR 三部分):
  1. amd64 四处模板(`EmitForLoopEmptyConst` / `RegLimit` / `WithRegKBody` / `WithRegKBody2`):
     `ucomisd idx, limit; ja` → `ucomisd limit, idx; jb`,CF=1 同时覆盖 `limit < idx` 与
     unordered。
  2. arm64 四处模板:`CondGT` → `CondHI`——HI(C=1 && Z=0)在 fcmpe 的 unordered 结果
     (C=1, Z=0)下真,退出成立。
  3. analyzer 三处 step 门:`value.AsNumber(kStep) <= 0` → `!(value.AsNumber(kStep) > 0)`。NaN
     的 `<=` 与 `>` 都为 false,原写法 `NaN <= 0` 为 false 会**放行** NaN step 进模板,新写法
     `!(NaN > 0)` 为 true 会**拒绝**,与「拒绝侧默认」对齐。

## 核心教训

### 教训 1(浮点比较 + 条件跳转的 unordered 分支去向必须显式论证)

这是「手写浮点比较 + 条件跳转,unordered 侧去向未论证」的第二例。第一例是 #103 的 P4 amd64
inline 比较快路径(四种 op/A 组合全反),本轮是 PJ3 spec 模板 FORLOOP 的退出比较。两次修复
手法完全同族:**换操作数序,让「异常侧」(unordered)落在跳转触发侧**——amd64 从
`ucomisd idx,limit; ja` 换成 `ucomisd limit,idx; jb`(CF=1 覆盖两侧);arm64 用能对 unordered
返回 true 的条件码族(HI/LS/MI/PL)代替 GT/LT/GE/LE(unordered 全 false)。

可操作纪律:凡是手写 `ucomisd` / `fcmpe` 后接条件跳转的位置,注释里必须显式回答**「操作数为
NaN 时跳到哪」**——不写等价于「没论证」。这条已经在 [[cross-backend-semantic-fix-sweep]] §「常
见语义风险家族 · unordered 比较」下有条目,本轮是**通道内**又一次实证(不是 arm64/amd64 分家
不对称,而是 per-op / spec 模板两个通道不对称),可以在原表下加一行「通道内变体」。

### 教训 2(shape gate 按「拒绝侧默认」写,不是「接受侧默认」)

`step <= 0` 与 `!(step > 0)` 在正常数值上等价,但 NaN 上不同:`NaN <= 0` 为 false(放行进
接受路径),`!(NaN > 0)` 为 true(落进拒绝路径)。**shape gate 的语义应该是「比较为假时落拒
绝/慢路径,不是接受/快路径」**——即写「必须满足接受条件才允许」,而不是「不满足拒绝条件就
允许」。两种写法在正常输入上等价,在 IEEE 边值上分岔,选择 negated 形式就把 NaN 这一类未论证
输入默认落到安全侧。

这条可以推广到所有 shape gate、IC gate、投机快路径 gate:**用「必须证明可接受」的正向形式,
不用「未证明不可接受」的反向形式**——两者只在异常输入上分岔,而异常输入正是漏网 bug 的常客。
建议随 unordered 条目一起入 guide,或者作为一条独立的短纪律。

### 教训 3(「同语义多通道 emit」是 cross-backend sweep 的通道内变体)

per-op FORLOOP 早就把 unordered 处理对了(#103 轮修的),但 PJ3 spec 模板通道是**另一份**
FORLOOP emit,漏网。这不是跨架构不对称,是**同架构内两个通道不对称**——per-op 通道走
`emit_ops_amd64.go`,spec 模板通道走 `internal/gibbous/jit/amd64/pj3_template.go`,两份代码各
写各的比较+跳转,同类风险不共享修复。

修复时四处 amd64 模板 + 四处 arm64 模板 + 三处 analyzer 门一次扫全,而不是等下一次 fuzz 再撞
其中某处漏网的。cross-backend-semantic-fix-sweep guide 的枚举清单目前列的是「P3 wasm / P4
amd64 / P4 arm64」三个后端,建议扩到「每个后端内所有独立的 emit 通道」——本仓的具体形式是
「per-op 通道」和「PJ3 spec 模板通道」两份,未来若再增(e.g. 一个 tier-3 内联通道)清单同步。

### 教训 4(prove-the-path 再一次:载体形状必须精确命中被测通道)

初版回归测试的载体是 `for ... do s = s + 1 end` 放进带 return 的 kernel,想的是让循环有可见
副作用。但 PJ3 spec 模板只匹配**精确的 6/7-op 空 body 形状**(`for i=K1,K2 do end` + 空
RETURN),带 body 的载体形状不对,静默落到从没坏过的 per-op 通道——测试全绿但什么都没测。
`jit.SpecForLoopHits()` delta 断言当场抓出空测(delta=0 时 `t.Fatal("carrier missed the PJ3
spec template ...")`),载体重写为「kernel 只做空 for、外层 chunk 调两次」。

这是 [[prove-the-path-under-test]] §2(b)「白盒命中计数器」的又一具体实例,和 #112 的「tier 状
态机吸收态载体必须跨过 retry window」是同源纪律的两种表现:两者都是「测试载体的具体形状要
精确匹配被测通道的接受形式,输出等价不等于路径命中」。

## 修复流程与验证

- 先复现:三形状(nan_limit / nan_init / nan_step)各写一个 10s deadline 的 `mustFinish`
  测试,`nan_limit` 与 `nan_init` 先挂在 deadline(死循环),`nan_step` 先挂在
  `SpecForLoopHits` delta ≠ 0(analyzer 门放行了)——三条 fail 全对上根因假设,再动手修。
- 修 amd64 → 修 arm64 → 修 analyzer:每步单独跑目标测试转绿,三部分修完跑 nightly 两个 seed
  的 corpus 重放,均通过。
- 全量:`go test ./...` × 三 build tag(default / wangshu_p4 / wangshu_p4+wangshu_profile)全绿。
- corpus 入 `testdata/fuzz/FuzzAutoPromote/eb8fb93a433d40b2` 与
  `testdata/fuzz/FuzzP4ForceAllPromote/5159747aad201f47`,固化为永久回归。

## Promotion 候选

- 「手写浮点比较 + 条件跳转的 unordered 侧必须显式论证 + 换操作数序把 unordered 落在触发侧」——
  已在 [[cross-backend-semantic-fix-sweep]] §「unordered 比较」有条目,本轮加「通道内变体
  (per-op vs spec 模板)」实证。
- 「shape gate 按拒绝侧默认写(用 `!(x > 0)` 不用 `x <= 0`)」——语义上是通用纪律,不局限于
  FORLOOP;可考虑并入 cross-backend-semantic-fix-sweep 的「NaN 别名」家族下,或单开一条短
  guide「speculation gate: reject-by-default on unmodeled inputs」。
- 「同一架构内多通道独立 emit,修复必须扫全所有通道」——现有 guide 的清单可从「后端」扩到
  「后端 × 通道」。

## 关联

[[2026-07-09-issue103-compare-ieee-round]](第一例 unordered 分支未论证:P4 inline 比较)·
[[2026-07-10-issue112-selftail-loop-round]](同源 prove-the-path 载体错配:tier 状态机 retry
window)· [[cross-backend-semantic-fix-sweep]] · [[prove-the-path-under-test]] · issue #117 ·
issue #118 · PR #119 · `internal/gibbous/jit/amd64/pj3_template.go` ·
`internal/gibbous/jit/arm64/pj3_template.go` · `internal/gibbous/jit/compiler.go`
