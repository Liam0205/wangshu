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

## 运行时断言接口的扩面不对称:优化静默关闭型

前面各实例都是「语义修复漏站点 → 产生错值」。issue #155(2026-07-18,PR #160)暴露了同一心理边界问题的更隐蔽变体:**给靠运行时类型断言满足的接口加方法,只补了一个架构的实现**。给 `bridge.NativeSegAddrer` 加 `NativeSeg2SegRetCount` 方法时只改了 amd64 的实现,漏掉 arm64 镜像——该接口靠 `code.(bridge.NativeSegAddrer)` 运行时断言满足,缺一个方法只是让断言返回 false(合法程序行为,编译器与 lint 都不报),于是 arm64 上共用该断言的全部调用点 ok==false,**seg2seg dispatch 整体静默失效**。失败形式与既有实例不同:不产生错值(结果经 exit-reason 回退仍正确),只是优化静默关闭,byte-equal 测试全绿;只有带命中数断言的白盒测试(prove-the-path 家族)在 arm64 CI leg 上以 hits=0 暴露(修复 commit 5f76472)。

**检查项**:动运行时断言的接口时,grep 该接口的所有实现类型(按 build tag 分文件的类型如 `nativeCode` 有 amd64/arm64 两份 struct),每份都要补方法;补齐后以双架构 CI(尤其带白盒命中数断言的测试)全绿为完成判据——本地单架构全绿不构成任何证据。反思实例见 `memory/reflections/2026-07-18-issue155-158-nightly-crasher-round.md` 教训 1。

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

## PUC 语义由 C 实现定义,不由手册定义

与 PUC Lua 5.1.5 做逐字节差分测试时,分歧的权威依据是官方 `_lua515/` 里的 C 源码,不是 5.1 参考手册。手册对边界值经常写得比实现松、或干脆不写:`string.format` 的 flags 数量上限 / width 与 precision 的位数上限 / `%s` 忽略 `'0'` / 无符号 verb 忽略 `' '` 与 `'+'` / `tonumber` 走 C99 `strtod` 加 hex 整数 fallback 的 `strtoul` endptr 约定 / 常量折叠拒 div-by-zero 与 NaN 结果 / 算术 RK 物化顺序先 o2 后 o1——这些细节全在 `lstrlib.c` / `lobject.c` / `llex.c` 里明写,手册要么略过要么写得更宽。C 侧还有一层「宿主 libc 边界也是 PUC 语义的一部分」的隐含依赖:`sprintf` 与 `strtod` 走宿主 libc,`\'` 转义的接受面走底层字符类判断。差分对手是 C 实现本身,不是文档。

工作流:任何「与 PUC byte-equal」的分歧,先在 `internal/oracle/_lua515/` 里 grep 对应实现,把 C 侧的接受面 / 拒绝面 / 边界值 / hard limit 写进 wangshu 侧,再用 `FuzzOracleDiff` 校验;不要在 wangshu 侧凭手册或探针试常数猜边界。反思实例见 `memory/reflections/2026-07-12-cgo-oracle-fuzz-round.md` 教训 2(一轮里 35 处分歧全部经此手法定位)。与本 guide 已有的「跨后端 / 跨通道枚举」纪律同域:跨后端扫要枚举实现,与 PUC 差分要枚举权威源码。

延伸(真值最终落在宿主 libc 时,读 C 源码只是第一步):PUC 语义不只由 C 源码定义,**非有限值(NaN/Inf)的格式化还由宿主 libc(glibc)定义**。`string.format` 的 `%f/%e/%g/%E/%G` 对 NaN/Inf 转发给 C `sprintf`,输出的大小写拼写(小写 verb → `nan`/`inf`,大写 → `NAN`/`INF`)、符号规则、以及 glibc 为 NaN 保留符号列导致的 width−1 quirk(见下),grep `_lua515/` 只能看到「转发给 `sprintf`」,真正的真值在 libc 里。这类分歧必须以 oracle 实测字节为准,不能照 Go `fmt` 或凭直觉。glibc 的确切规律:glibc 总为 NaN 保留 1 个符号列;小写 verb 符号不显示(那一列变空格被 width 吸收 → 有效 width = 声明 width−1),大写 verb 符号是可见的 `-`(已在 core 里占了那一列 → 完整 width);Inf 符号一直在 core 里 → 完整 width;precision 对 NaN/Inf 忽略。方法论要点:**对付「宿主 libc 定义的格式化」这类外部真值,不要从一两个样本外推规则,直接构造覆盖矩阵(verb × 符号 × flag × width)扫 oracle,规律要能解释矩阵里每一格才算定准**——本轮(#170/#171,PR #172)正是从单点「小写 NaN width−1」外推「所有非有限值 width−1」,一步把 Inf 全改错,靠 93 组覆盖矩阵实测才把完整真值表逼出来。实现落点:`internal/stdlib/stringlib.go` 的 `cFormatSpecialFloat` 在 NaN/Inf 时特判、逐字节复刻 glibc;反思实例见 `memory/reflections/2026-07-22-oracle-format-nan-inf-round.md` 教训 1/2。

延伸(面级规则:先测绘整个行为面再实现):分歧涉及「派生逻辑」(名字从哪来、计数怎么减、回退到什么)而不是「输出格式」时,默认背后是 PUC 的一整个子系统,不是一条孤立措辞。先写探针套把完整行为面测绘成对照表,再一次性实现,避免「修一条、fuzz 再打穿一条」的逐点返工。实证:issue #133(2026-07-14,PR #134)——一个 fuzz 种子表面是 `coroutine.create(coroutine.resume)` 错误消息不同,实际是 `luaL_argerror` 的函数名派生规则整体分歧:PUC 的 `bad argument #N to 'name'` 中 name 来自**调用方的调用点**(ldebug.c `getfuncname` → `getobjname` 对 CALL/TAILCALL/TFORLOOP 的 A 操作数做 symbexec),推论包括别名命名(`local r = string.rep; r(nil)` 报 `'r'`)、method 调用 self 不计入 #N 且减到 0 时改报 `calling 'X' on bad self`、TFORLOOP 站点报 `"(for generator)"`、纯 C-to-C 边界保持 `'?'`;~70 条探针先测绘全部分支,然后一次实现(结构化 `NewArgError` + `resolveArgError` 在 Lua 调用边界统一改写,`callLuaFromHost` wrapper 冻结 `'?'`),全部探针逐字节一致。配套模式:错误消息依赖抛出点拿不到的上下文时,用「错误对象携带结构化字段 + 拥有上下文的边界层统一改写」,不要把上下文穿透传给每个抛出点(~84 个 stdlib 站点若改签名代价不可控);「解析权冻结」(wrapper 把结构化字段归零)防止错误穿越多层边界后被外层调用点错误重新命名。反思实例见 `memory/reflections/2026-07-14-issue133-argerror-caller-name-round.md`。

延伸:「改写输入再委托宿主标准库」是不收敛适配路径,第 N 次被打穿时换成手写。同一个 site(比如 `internal/stdlib/stringlib.go` 的 stringFnFormat unsigned 分支)如果曾经通过「改写 spec 后交给 Go `fmt.Sprintf` / `strconv.*` / 宿主 libc 类库」的方式适配 C 语义,而 `FuzzOracleDiff` 又反复在这个 site 上撞出新分歧(Go `fmt` 与 C `printf` 对 `%#X` 零值前缀 / `%#08X` 补零位置 / `%#.0o` 零值 / `%s` 的 `'0'` flag 是否 pad 等等就有多处分歧,ICU / RE2 / 宿主时区表也同样),这条路径就是不收敛的:两个实现各自演进,分歧集合是开放的,补丁式修复只能覆盖「已被撞到的那一处」。判据一旦成立(同一 site 第 2 次以上被打穿,且新分歧仍在同一语法维度上),就把这一段整段换成手写的 C 语义 renderer,把语义收敛为封闭规则(C99 printf 一页写完 / 官方 `_lua515/` 对应的 C 函数几十行写完)。实证:2026-07-13 nightly 巡检轮里 stringFnFormat 的 `%u/%x/%X/%o` 分支第三次被打穿(前两次 `%100X` 宽度与 `% 00X0` 忽略旗标,这次 `%#X` 零值),从 `bytes.ReplaceAll(spec, ...) + fmt.Sprintf` 换成手写 `cUnsignedFormat`,41 个覆盖前缀 / 宽度 / 精度 / 旗标交互的用例逐字节等于 PUC。触发场景:同一个「改写输入 + 委托宿主标准库」site 被 differential fuzz 打穿第 2 次时,不要再补一发 `ReplaceAll` 或 `strings.ReplaceAll`,直接换手写实现;写新 site 前也要看 Go 标准库对该语义有没有已知的多点分歧,有就直接手写。同族反思实例见 `memory/reflections/2026-07-13-nightly-concat-oom-and-format-hash-round.md`。

## 同族 harness 防护不对称

跨后端 / 跨通道扫的心理边界是「同一段语义在系统里的全部实现站点」,同样的原则也适用于测试与防护本身的「同类 harness」。当给一个 fuzz harness / smoke 脚本 / CI 检查加防护(资源上限帽、豁免规则、异常路径断言、artifact upload、种子清单等)时,不能只加在触发本次修复的那一个,要立刻横向问一句「兄弟 harness 有没有同样的暴露面」,一起加。心理边界停在「当前 harness」而不是「全部同类站点」就是欠账,下一次同类问题在没被防护到的兄弟 harness 上炸出来。

实证:2026-07-13 处置的 issue #127(p3)/ #130(p4)两个 nightly crasher 是同根因 quadratic concat 风暴打爆默认 2 GiB arena 触发进程级 kill。上周 PR #128 给 FuzzOracleDiff 上线时明确考虑了资源问题、加了 `MaxArenaBytes: 64 << 20`,但没横向扫 fuzz_test.go / fuzz_auto_test.go / fuzz_p4_test.go 三个更老的 fuzz harness——它们全都没帽。两个 crasher 本质就是这次不对称欠下的债。修法把三个老 harness 一起补上帽,与 FuzzOracleDiff 对齐。触发场景:任何时候给一个 fuzz / smoke / CI 检查加防护时(资源上限、豁免规则、异常路径、artifact upload、种子清单),立刻 grep 同仓所有兄弟 harness,同一轮补齐;新 harness 上线时也要横向扫兄弟 harness 有没有该同步过来的既有防护。同族反思实例见 `memory/reflections/2026-07-13-nightly-concat-oom-and-format-hash-round.md` 教训 2。

## 相关

- [[unreproducible-crasher-triage]]——差分 fuzz 报层间分歧信号(P1-vs-auto / P1-vs-force / 后端 A vs 后端 B),进入本 guide 的修复流程之前,先按该 guide「真 crasher 但失败形式是层间分歧」节的 oracle 归因步骤确认 bug 真的在 tier / 后端侧;若 oracle 与两层都不符,bug 在共享前端 / stdlib / VM 共享语义,不属于本 guide 的修复范围。共享前端 bug 伪装成层间分歧的实例见 [[2026-07-11-issue125-return-freereg-round]](`return f() or (f())` 的 RETURN 操作数计算读预捕获 freereg 拿栈垃圾,两个 tier 各自读到不同历史值让分歧显性化)。
- [[prove-the-path-under-test]]——修复后证明每个后端的修复站点真被测试执行。
- [[design-claims-vs-codebase-physics]]——「不产生新 NaN 所以不需规范化」这类头注主张要对位模式物理
  重新验证(#107 的头注对了一半,结论错)。
- 反思实例:`2026-07-08-issue67-amd64-nodehit-crossrun-round` /
  `2026-07-09-issue103-compare-ieee-round` / `2026-07-10-issue106-107-nightly-crashers-round` /
  `2026-07-11-issue117-118-nan-forloop-round` /
  `2026-07-18-issue155-158-nightly-crasher-round`(运行时断言接口扩面不对称) /
  `2026-07-22-oracle-format-nan-inf-round`(PUC 语义由 libc 定义:`string.format` NaN/Inf 对齐 glibc)。
