# 跨后端语义修复同步扫描(cross-backend semantic fix sweep)

## 适用场景

在任何一个执行后端(P3 wasm / P4 amd64 / P4 arm64)修掉一个**语义类** bug 时——尤其是 inline 快路径上
绕过 host 语义的站点(NaN 处理、IEEE 边值、tag 别名、guard 条件)。

## 问题模式:双后端修复不对称

同一语义风险在多个后端各有一份**独立实现**的 inline 快路径。修 bug 时的心理边界停在「当前在改的后端」,
而 bug 的真实边界是「所有绕过 host 语义的 inline 站点」。修好一个后端、漏掉其余的结果是:同一个 bug 在
另一个后端再潜伏数天到数周,直到 fuzz 或用户再撞一次。

三个实证(时间线):

| 轮次 | 修了哪边 | 漏了哪边 | 潜伏 |
|---|---|---|---|
| issue #67(2026-07-08) | arm64 NodeHit guard 改良 | 未移植回 amd64 | 跨 Run 身份 guard 全落空 |
| issue #103(2026-07-09) | arm64 unordered 条件码(#37 端口轮修) | 未回查 amd64 裸 jcc | 带病一周+,fuzz 撞 tier divergence |
| issue #107(2026-07-10) | P4 amd64+arm64 emitUNM(#37 端口轮修) | 未查 P3 wasm emitUnm | canonNaN sign-flip 成 Nil,nightly 撞 |

#107 最尖锐:#37 的修复注释明确写了 "fixed on both arches in the same change"——当时**以为**扫全了,
但「后端」的枚举本身漏了 P3 wasm。凭记忆列后端清单不可靠。

## 纪律

在任一后端修语义类 bug 时,**同一个 PR 内**完成:

1. **枚举全部后端**——以 bridge 注册的 Compiler 实现为准,不凭记忆:
   - P3 wasm:`internal/gibbous/wasm/translate*.go` 的 `emitXxx`;
   - P4 amd64:`internal/gibbous/jit/peroptranslator/emit_ops_amd64.go` 的 `emitXXX`;
   - P4 arm64:`internal/gibbous/jit/peroptranslator/translator_native_arm64.go` / `emit_arm64.go` 的
     `emitXxxArm64`;
   - (未来新增后端时本清单同步扩。)
2. **grep 同名 op 的所有 emit 站点**,逐一确认同类风险是否存在。判断标准不是「代码长得像不像」,而是
   「这个站点是否同样绕过了 host 语义」——绕过方式不同(wasm f64.neg vs amd64 xor sign bit)不代表风险
   不同。
3. **每个受影响后端配 prove-the-path 载体**:P3 用升层后重复调用(第 2 次 Run 起才执行 wasm),P4 用
   force-all + 白盒计数器;修复站点与测试载体一一对应。
4. **端口轮的"顺手修"要双向审计**:把 A 后端移植到 B 后端时顺手修的每一处,都要问「这真是 B 特有,
   还是 A 也有但没人看」(#103 的教训);反过来,在 B 上做的改良要问「要不要移植回 A」(#67 的教训)。

## 常见语义风险家族(已实证的)

- **NaN 别名**:canonNaN(`0x7FF8...`)经 sign-flip(neg)恰好落在 `TagNil`(`0xFFF8...`);任何对
  NaN-box 位模式做位级变换(neg 的 sign flip、abs 的 mask)的 inline 都可能把 canonNaN 移进/移出 tag
  空间。通用解药:**result guard**——变换后重查 tag 边界(`>= qNanBoxBase` → 慢路径,host 端
  `NumberValue` 规范化),P4 #37 与 P3 #107 两次验证。
- **unordered 比较**:UCOMISD/FCMPE 的 unordered 结果被裸条件码错误解析(#103,amd64 jae/ja vs arm64
  MI/PL/LS/HI)。
- **位相等 ≠ 语义相等**:EQ 的位比较漏 NaN==NaN(canonNaN 规范化使两个 NaN 必然位相等)与 ±0(位不等
  但语义相等)(#103)。
- **跨 Run 失效的身份 guard**:烤进段的对象身份(TableRef)跨 Run 重建后必落空(#67,换内容 guard)。

## 相关

- [[prove-the-path-under-test]]——修复后证明每个后端的修复站点真被测试执行。
- [[design-claims-vs-codebase-physics]]——「不产生新 NaN 所以不需规范化」这类头注主张要对位模式物理
  重新验证(#107 的头注对了一半,结论错)。
- 反思实例:`2026-07-08-issue67-amd64-nodehit-crossrun-round` /
  `2026-07-09-issue103-compare-ieee-round` / `2026-07-10-issue106-107-nightly-crashers-round`。
