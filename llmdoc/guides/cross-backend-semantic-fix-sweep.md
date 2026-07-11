# 跨后端语义修复同步扫描(cross-backend semantic fix sweep)

## 适用场景

在任何一个执行后端(P3 wasm / P4 amd64 / P4 arm64)修掉一个**语义类** bug 时——尤其是 inline 快路径上
绕过 host 语义的站点(NaN 处理、IEEE 边值、tag 别名、guard 条件)。同一个后端内部也可能同时存在多条
独立的 emit 通道(per-op 翻译器 / PJ3 spec 模板 / 未来的 tier-3 内联通道),同名 op 各写一份不共享
修复,同样属于本 guide 的范围。

## 问题模式:同一语义多处独立实现,修复不对称

同一语义风险在多个后端**以及同一后端内的多条 emit 通道**里各有一份**独立实现**的 inline 快路径。
修 bug 时的心理边界停在「当前在改的那一份」,而 bug 的真实边界是「所有绕过 host 语义的独立实现」。
修好一处、漏掉其余的结果是:同一个 bug 在另一处再潜伏数天到数周,直到 fuzz 或用户再撞一次。

四个实证(时间线,前三例是**跨后端**不对称,第四例是**同后端内跨通道**不对称):

| 轮次 | 修了哪份 | 漏了哪份 | 潜伏 |
|---|---|---|---|
| issue #67(2026-07-08) | arm64 NodeHit guard 改良 | 未移植回 amd64 | 跨 Run 身份 guard 全落空 |
| issue #103(2026-07-09) | arm64 unordered 条件码(#37 端口轮修) | 未回查 amd64 裸 jcc | 带病一周+,fuzz 撞 tier divergence |
| issue #107(2026-07-10) | P4 amd64+arm64 emitUNM(#37 端口轮修) | 未查 P3 wasm emitUnm | canonNaN sign-flip 成 Nil,nightly 撞 |
| issue #117/#118(2026-07-11) | P4 amd64+arm64 per-op emitFORLOOP unordered(#103 轮修的) | 未查同架构 PJ3 spec 模板 FORLOOP | 潜伏约一周,nightly 两个 seed 同时撞死循环 |

#107 最尖锐:#37 的修复注释明确写了 "fixed on both arches in the same change"——当时**以为**扫全了,
但「后端」的枚举本身漏了 P3 wasm。#117/#118 再进一步:同一后端内的**另一条独立通道**(PJ3 spec
模板)也算漏网——per-op 通道走 `emit_ops_amd64.go`/`emit_arm64.go`,spec 模板通道走
`amd64/pj3_template.go`/`arm64/pj3_template.go`,两份代码各写各的比较+跳转,同类风险不共享修复。
凭记忆列清单不可靠——不管是列后端还是列通道。

## 纪律

在任一处修语义类 bug 时,**同一个 PR 内**完成:

1. **枚举全部后端 × 通道**——以 bridge 注册的 Compiler 实现和各后端的 emit 通道为准,不凭记忆:
   - P3 wasm:`internal/gibbous/wasm/translate*.go` 的 `emitXxx`;
   - P4 amd64:
     - per-op 翻译器通道:`internal/gibbous/jit/peroptranslator/emit_ops_amd64.go` 的 `emitXXX`;
     - PJ3 spec 模板通道:`internal/gibbous/jit/amd64/pj3_template.go` 里各形状模板
       (`EmitForLoopEmptyConst` / `RegLimit` / `WithRegKBody` / `WithRegKBody2` 等);
   - P4 arm64:
     - per-op 翻译器通道:`internal/gibbous/jit/peroptranslator/translator_native_arm64.go` /
       `emit_arm64.go` 的 `emitXxxArm64`;
     - PJ3 spec 模板通道:`internal/gibbous/jit/arm64/pj3_template.go`;
   - (未来新增后端或新增内联通道时本清单同步扩。)
2. **grep 同名 op 的所有 emit 站点**,逐一确认同类风险是否存在。判断标准不是「代码长得像不像」,而是
   「这个站点是否同样绕过了 host 语义」——绕过方式不同(wasm f64.neg vs amd64 xor sign bit;per-op
   通道 vs spec 模板通道)不代表风险不同。
3. **每个受影响的实现配 prove-the-path 载体**:P3 用升层后重复调用(第 2 次 Run 起才执行 wasm),P4
   per-op 用 force-all + 白盒计数器,PJ3 spec 模板通道用 `jit.SpecXxxHits()` delta 断言精确匹配模板
   形状——载体形状不对会静默落进旁路通道(#117/#118 初版载体带非空 body 落进 per-op 通道,delta=0
   立刻抓出空测)。
4. **端口轮的"顺手修"要双向审计**:把 A 处移植到 B 处时顺手修的每一处,都要问「这真是 B 特有,还是
   A 也有但没人看」(#103 的教训);反过来,在 B 上做的改良要问「要不要移植回 A」(#67 的教训);
   同名 op 在多个通道各有一份 emit 时,还要问「另一通道的 emit 是不是同族风险」(#117/#118 的教训)。

## 常见语义风险家族(已实证的)

- **NaN 别名**:canonNaN(`0x7FF8...`)经 sign-flip(neg)恰好落在 `TagNil`(`0xFFF8...`);任何对
  NaN-box 位模式做位级变换(neg 的 sign flip、abs 的 mask)的 inline 都可能把 canonNaN 移进/移出 tag
  空间。通用解药:**result guard**——变换后重查 tag 边界(`>= qNanBoxBase` → 慢路径,host 端
  `NumberValue` 规范化),P4 #37 与 P3 #107 两次验证。
- **unordered 比较 + 条件跳转的分支去向义务**:UCOMISD/FCMPE 后接条件跳转时,必须显式论证「操作数
  为 NaN 时跳到哪」——不写等价于「没论证」。unordered 结果被裸条件码错误解析的两次实证:#103(P4
  amd64 inline compare 快路径,四种 op/A 组合全反,per-op 通道)+ #117/#118(PJ3 spec 模板通道
  FORLOOP 退出比较,`ja` 在 unordered 上永假 → mmap 段死循环)。修法范式:**换操作数序,让「异常
  侧」(unordered)落在跳转触发侧**。
  - amd64 用 CF=1 家族(`jb`/`jae`)代替 ZF/SF 混合族;`ucomisd` 在 unordered 下置 CF=ZF=PF=1,`jb`
    (CF=1)天然覆盖 `limit<idx` 与 unordered 两个「不该继续循环」的情况,一条指令兜住。
  - arm64 用能对 unordered 返回 true 的条件码族(HI / LS / MI / PL)代替 GT / LT / GE / LE
    (unordered 全 false);fcmpe 的 unordered 结果是 C=1、Z=0,HI(C=1 && Z=0)在此为真。
- **位相等 ≠ 语义相等**:EQ 的位比较漏 NaN==NaN(canonNaN 规范化使两个 NaN 必然位相等)与 ±0(位不等
  但语义相等)(#103)。
- **跨 Run 失效的身份 guard**:烤进段的对象身份(TableRef)跨 Run 重建后必落空(#67,换内容 guard)。

## shape gate 按「拒绝侧默认」写

投机快路径 / IC gate / 各类 shape gate 的比较条件,**必须写成「比较为假时落拒绝/慢路径,不是落
接受/快路径」**——正向形式「必须证明可接受」而不是反向形式「未证明不可接受」。两种写法在正常输入
上等价,在 IEEE 边值(尤其 NaN)上分岔:

- `step <= 0` 与 `!(step > 0)` 在正常数值上等价,NaN 上不同——`NaN <= 0` 为 false 会**放行** NaN
  step 进接受路径,`!(NaN > 0)` 为 true 会**拒绝**并落慢路径。
- 一般化:`x <= 0` / `x >= 0` / `x == 0` 等直接判「是否满足拒绝条件」的写法,在 NaN 上都会误判为
  「不满足拒绝条件」而放行;把条件写成「必须满足接受条件才允许」(negated 形式)可以把未论证的输入
  默认落到安全侧。

实证:issue #117/#118 的 analyzer step 门 `value.AsNumber(kStep) <= 0` → `!(value.AsNumber(kStep)
> 0)` 是这条纪律的直接应用,配合 unordered 修法一起把 NaN limit / init / step 三种形状全部收敛
到拒绝路径。

## 相关

- [[unreproducible-crasher-triage]]——差分 fuzz 报层间分歧信号(P1-vs-auto / P1-vs-force / 后端 A vs 后端 B),进入本 guide 的修复流程之前,先按该 guide「真 crasher 但失败形式是层间分歧」节的 oracle 归因步骤确认 bug 真的在 tier / 后端侧;若 oracle 与两层都不符,bug 在共享前端 / stdlib / VM 共享语义,不属于本 guide 的修复范围。共享前端 bug 伪装成层间分歧的实例见 [[2026-07-11-issue125-return-freereg-round]](`return f() or (f())` 的 RETURN 操作数计算读预捕获 freereg 拿栈垃圾,两个 tier 各自读到不同历史值让分歧显性化)。
- [[prove-the-path-under-test]]——修复后证明每个后端的修复站点真被测试执行。
- [[design-claims-vs-codebase-physics]]——「不产生新 NaN 所以不需规范化」这类头注主张要对位模式物理
  重新验证(#107 的头注对了一半,结论错)。
- 反思实例:`2026-07-08-issue67-amd64-nodehit-crossrun-round` /
  `2026-07-09-issue103-compare-ieee-round` / `2026-07-10-issue106-107-nightly-crashers-round` /
  `2026-07-11-issue117-118-nan-forloop-round`。
