# 跨后端语义修复同步扫描(cross-backend semantic fix sweep)

## 适用场景

在任何一个执行后端(P3 wasm / P4 amd64 / P4 arm64)修掉一个**语义类** bug 时——尤其是 inline 快路径上
绕过 host 语义的站点(NaN 处理、IEEE 边值、tag 别名、guard 条件)。同一个后端内部也可能同时存在多条
独立的 emit 通道(per-op 翻译器 / PJ3 spec 模板 / 未来的 tier-3 内联通道),同名 op 各写一份不共享
修复,同样属于本 guide 的范围。

范围之后又扩了三次:与 PUC 逐字节差分时「枚举权威 C 源码」是同一条纪律换了对象(见「PUC 语义由 C 实现
定义」节的六个刻度);**共享层**里同一条调用约定在多条返回 / 退出路径上各做一次时,漏掉其中一条也属于
本 guide 的不对称家族(见「共享层恢复动作的兄弟路径不对称」节);而**一个症状由同一子系统内两处独立
错误共同造成**(不是「一处该传播的没传播」,是从一开始就两处各自错、方向可能相反)也是本 guide 的
不对称家族之一,只是方向相反(见「一个症状可能是同一子系统内两处独立错误叠加」节)。

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

## 共享层恢复动作的兄弟路径不对称:第 N+1 处漏做就是缺陷

不对称家族的第三种。前两种(多后端 emit 站点 / 运行时断言的多架构实现)都是**横向**的同一语义多份
实现;本条是**纵向**的同一条调用约定在多条返回 / 退出路径上各做一次,而其中一条漏做。

**实证(2026-08-04,#229)**:`doReturn` 的**终止分支**没有把 top 恢复成 caller 的逻辑帧顶,而同一条
纪律(PUC `lvm.c` `OP_RETURN` 的 `if (b) L->top = L->ci->top`)在本仓另外**三处**兄弟路径上早就在做
——`doReturn` 自己的非终止分支、gibbous 的 `DoReturn`、`callHost`。漏做的后果是 caller 的活寄存器留在
top 之上,GC 的栈根扫描(`visitThreadValues`)把 `[top, size)` 当陈旧残留清成 nil,于是一个 NEWTABLE
的结果被清掉、紧随的 SETLIST 报 `SETLIST: not a table`。`callHost` 那一处的注释里还写着它当年的症状
(多值 CALL 留下低 top → `callLuaFromHost` 脚手架覆写 TFORLOOP 三槽 → `pairs` 收到 number),来自
2026-06-12 测试加固轮——也就是说这条纪律在本仓已经实证过一次,本轮是**第四处**。

**为什么容易漏**:每一处看起来都是「这条路径的局部细节」,而它们在实现同一条调用约定。新增一条返回
路径时作者关心的是「返回值搬对了吗」,而 top 的恢复不影响返回值、只在 GC 跑起来之后才暴露,所以漏做
不会立刻有症状。这一处之所以被漏,还因为它的条件读起来像「已经没有 caller 了」:
`th.ciDepth <= entryDepth` 只说明离开了**这一层 `executeFrom` 的 entry 帧**,而 gibbous 的
`TailCall` / `DoCall` / `CallBaseline` / `ExecutePlainCallInlineFrame` 都会以 `entryDepth = ciDepth-1`
开一层**嵌套** `executeFrom` 来驱动一次普通的 Lua→Lua 调用,下面还压着活着的 caller 帧。

**检查项**:改共享层(调用 / 返回 / 错误冒泡)时,grep 同一个恢复 / 清理动作的所有出现处,数一数是不是
所有该做的路径都做了——**数量不齐就是信号**。本轮的特征表达式是
`th.setTop(... .base + int(st.protoOf(...).MaxStack))`,把命中站点与「所有会退出一层 Lua 帧的路径」
两张清单对照即可。这是 [[design-claims-vs-codebase-physics]] §4.1「新对象类型的分配路径必须照抄同族
分配器的每一步」的**恢复侧对偶**:那条讲同族分配器里出现三次的动作(`AllocX` + `LinkSweep` +
`AllocCharge`)是契约,本条讲同族返回路径里出现三次的恢复动作也是契约;两者共享同一条元纪律——**同族
里重复出现的动作是契约,不是那几个函数各自的选择**,而且两者的症状都离原因很远(那边 panic 在 GC 里、
错误在分配处;这边报错在 SETLIST、错误在 RETURN)。反思
[[2026-08-04-issue228-229-lazy-capture-and-nested-tailcall-top]] 教训 5。

## 一个症状可能是同一子系统内两处独立错误叠加,少修一处仍然错

不对称家族的第四种,而且方向与前三种相反。前三种都是「**一处**改动没有传播到它全部该到的地方」(多
后端 emit 站点 / 多架构断言实现 / 共享调用约定的兄弟路径);本条是**一个症状从一开始就由两处互相独立
的错误共同产生**,不存在「先改对了一处、忘了传播」这件事,两处从写下的那一刻起就都是错的,而且方向
可能相反,少修任一处,症状仍然不对。

**实证(2026-08-27,#248)**:索引表达式跨行(`A\n.x`)时报错行号不对,`internal/frontend/compile` 里
是两处独立错误共同造成的——`exprIndex`(`codegen.go`)把对象交给 `exp2AnyReg` 时传了运算符的行(该
传对象自己的行),延迟加载的 GETGLOBAL 因此被盖上运算符的行;`eIndexed` 这个 `expDesc`
(`expdesc.go`)不带行号,它的 GETTABLE 用「之后由谁 discharge 就取谁的行」,而不是运算符自己的行。
修前 dump 出的 `LineInfo` 显示 `pc0 GETGLOBAL line=2、pc1 GETTABLE line=1`——两条指令的行号**都**
不对,而且方向刚好相反(该是 1 的那条被写成 2,该是 2 的那条被写成 1)。两处任一单独修复,整张表仍然
是错的:只修第一处,GETGLOBAL 对了但 GETTABLE 仍然跟着 discharge 点走;只修第二处,GETTABLE 对了但
GETGLOBAL 仍然被运算符的行覆盖。

**为什么容易漏**:「消息报错了」这个单一症状天然地暗示「有一个 bug」,而不是「有两个 bug 凑在一起」。
修完一处之后如果症状看起来「变了但还是不对」,容易被误判成「刚做的修复方向错了」而回头怀疑或撤销它,
而真正缺的是**另一处独立的修复**,不是撤销已经做对的那一处。

**检查项**:改完一处怀疑的独立错误后,**先把完整的中间数据结构(本例是整张 `LineInfo` 表)逐条 dump
出来核对,而不是只看最终报出来的那一条错误消息变没变**。错误消息只反映报错点那一条指令的行号,而报错
点往往不是唯一被写错的那条。这与「枚举全部后端 × 通道」是同一条纪律的**子系统内部**版本:那条讲同一
语义风险要在多个后端/多条 emit 通道里逐一核对;本条讲**同一段共享代码路径**里,一个最终症状要拆成
「这条路径上每一条指令/每一个中间状态是不是都对了」逐一核对,不能只验证症状消失。反思
[[2026-08-27-issue248-index-line-across-newline]] 教训 2。

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

工作流:任何「与 PUC byte-equal」的分歧,先在 `internal/oracle/_lua515/` 里 grep 对应实现,把 C 侧的接受面 / 拒绝面 / 边界值 / hard limit 写进 wangshu 侧,再用 `FuzzOracleDiff` 校验;不要在 wangshu 侧凭手册或探针试常数猜边界。

**更细的一个刻度:读到源码还不够,源码里的隐式转换也要展开**。C 的多步转换链里每一步都可能改变结果,跳过中间一步得到的结论可以与真值完全相反。实证(2026-07-28,#193):`string.char(0/0)` wangshu 报错、PUC 得 byte 0。这**不是**一个范围检查——`luaL_checkint` 是 `(int)luaL_checkinteger`,所以 double 先变成 `lua_Integer`(`ptrdiff_t`,64 位)**再**窄化成 int;x86-64 的 `cvttsd2si` 把 NaN 与一切超出 int64 范围的 double 映射成 `INT64_MIN`,其低 32 位恰好是 0,于是 `c == 0`、`uchar(c) == c` 成立、PUC 接受。第一次探测时直接把 double 转成 int(漏了 `lua_Integer` 那一步)得出「PUC 应该拒绝」的**相反**结论。可复用判据:分歧涉及 C 语义时,把参照实现那条链上的**每一次类型转换**都写出来再判断,包括 `luaL_checkint` / `luaL_checkinteger` 这类宏背后的隐式两步;别按「这个参数应该是什么类型」推。反思实例见 `memory/reflections/2026-07-28-four-diff-divergence-issues.md` 教训 3。

**再一个刻度:那个条件读的量可能不止「请求值」本身**。C 侧的检查常常同时读**调用当时的栈状态**,
而 wangshu 侧只看到请求值,于是照抄出来的上限是个常数。实证(2026-07-28,#201):`unpack` 的上限
不是固定的 `LUAI_MAXCSTACK`(8000),而是 8000 **减去参数个数**——PUC 的 `luaB_unpack` 调
`lua_checkstack(L, n)`,它的拒绝条件是
`size > LUAI_MAXCSTACK || (L->top - L->base + size) > LUAI_MAXCSTACK`,而对一个 C 函数来说
`L->top - L->base` 就是参数个数,所以 `unpack({0,"",1},1,7998)` 在 PUC 抬
`too many results to unpack` 而 wangshu 接受。可复用判据:抄一个 C 侧的上限时,把那个条件表达式
里的**每一项**都问一遍「它在 wangshu 这边对应什么」,别只抄阈值常数;定数值的验证手法见
[[prove-the-path-under-test]] §4.5(让决定它的那个量变化一格再测——阈值随参数个数变化才排除了
「硬编码 7997」)。同轮另一处同类:`os.time` 的 `isdst` 字段 PUC 传给 `mktime` 的 `tm_isdst`,
用来确定一个 DST 相关本地时间的解释,只在它与该日期在该时区的自然状态**不一致**时才有影响
(原实现整个字段忽略,于是 `os.time{...,isdst=true}` 与不带它的同一组字段返回同一个瞬间)。反思
实例 [[2026-07-28-issue201-203-unpack-skip-thresholds]]。

**第四个刻度:错误消息本身也可能被包了一层**。前面几个刻度分别讲**值**要顺着 C 的转换链推
(`luaL_checkint` 的隐式两步)、**上限**的条件项可能读调用当时的栈状态(`unpack` 减参数个数),
本条讲**消息文本**也有一条包装链。C 侧抬错有两类入口:`luaL_error` 把给它的文本**直出**,而
`luaL_checkstack` / `luaL_argerror` / `luaL_typerror` 这类会**再套一层格式**;源码里那一行看起来
都只是「一个字符串字面量」,差别在被谁消费。实证(2026-07-29,#206):`string.byte` 缺
`lua_checkstack` 上限,`string.byte(string.rep("a",9000),1,8000)` 返回 8000 个值而 PUC 抬错;
上限与 `unpack` 一样是 `8000 - nargs`(PUC 的 `str_byte` 调
`luaL_checkstack(L, n, "string slice too long")`),但 `luaL_checkstack` 把那段文本包成
`"stack overflow (%s)"`,所以 PUC 输出的是 `stack overflow (string slice too long)`。第一版
上限判断完全正确、照抄了裸文本,65 个 oracle 用例里仍有 12 个分歧。可复用判据:**抄一条 PUC
错误消息时,先看它是经哪个 `luaL_*` 抬出来的,把那一层的格式串一起抄**;同族函数的公式一样
不代表消息一样(`unpack` 与 `string.byte` 的上限公式相同,一个直出、一个包一层)。推广一步:
修一个与已修函数「公式相同」的兄弟函数时,公式可以照抄,消息、默认值、参数校验各自独立核对
一遍。反思实例 [[2026-07-29-issue205-206-208-io-userdata-debug]] 教训 5。

**第五个刻度:校验在控制流里的位置也是要照抄的东西**。前四个刻度分别讲**值**要顺着 C 的转换链推、
**上限**的条件项可能读调用当时的栈状态、**消息**可能被 `luaL_*` 包一层——都是「读到源码那一行之后
还要看一层」;本条讲**那一行在哪**。一个校验位于函数入口 / 循环之前 / 循环之内 / 分支之内,是语义的
一部分,不只是实现细节。实证(2026-08-02,#216):`string.gsub` 的 repl 类型检查被写在替换循环
**里面**,而 PUC 的 `str_gsub` 是在循环**之前**用 `luaL_argcheck(tr)` 校验的;后果是
`gsub("", "", nil, .0)` 在望舒里**成功返回**而 lua5.1 抬
`bad argument #3 (string/function/table expected)`——第 4 个参数把循环次数压成 0,循环一次都没跑,
那个非法参数从来没有被看到。**是那第 4 个参数让这条路径可达的**:没有它,`gsub("", "", nil)` 会走进
循环、在第一次替换时报错,行为碰巧正确。

可复用判据:「进入循环前校验一次」与「每次迭代校验」在参数合法时等价、在参数非法且循环体执行 ≥ 1 次时
也等价,两者**只在参数非法且循环零次这一种输入上分岔**——而这正是 fuzzer 擅长构造的,它只要找到另一个
参数能把次数压到 0。所以:搬参照实现的校验时记下它在 C 源码里的控制流位置并保持一致;对每一个懒校验
问一句「有没有一个输入能让这段循环 / 分支执行零次」——`n = 0`、空串、空表、空区间、提前 return 都是
候选,有的话就为那个输入写一个用例。反思实例
[[2026-08-02-issue212-219-fuzz-crasher-batch]] 教训 3。

**第六个刻度:错误从哪个函数抬出来也是要照抄的东西,而「懒抬」意味着每个读取点各要一次检查**。
第五个刻度讲一个校验在控制流里的**位置**(循环之前还是循环之内),本条讲一个错误的**抬出点**在调用链
里的位置——同一个条件,在生产它的函数里抬、还是在消费它的函数里抬,是语义的一部分。实证(2026-08-04,
#228):`string.gsub("","(",0)` 在 lua5.1 返回 `0 1`,望舒抬 `unfinished capture`。PUC 是从
`push_onecapture` 抬这个错的,也就是**一个捕获真的被读出来的时候**;而 `str_gsub` 的 `add_value` 只有
三条路径会走到那里——**表**替换(`push_onecapture` 之后 `lua_gettable`)、**函数**替换
(`push_captures` 之后 `lua_call`)、以及 `%n` 展开;纯字符串或数字替换走 `add_s`,只展开 `%n`、
从不读捕获。所以同一个 pattern、同一次匹配,**抬不抬错取决于消费者要不要读那个捕获**:
`gsub("abc","(","r")` 正常返回 `"rarbrcr", 4`,而 `match` / `find` / `gmatch` 抬错。望舒原先在
`collectCaptures`(生产侧)就抬了,于是所有消费者都变成早抬。

**判据一:把一个错误从「早抬」改成「懒抬」时,要在每个 materialize 点各加一次检查,而不是在恰好被
报告的那条路径上加一次。** 这个改动的心智模型容易误认成「把 `return err` 往后挪」,而它实际上是
「把一个抬出点变成一个集合」,集合的大小由**消费者**决定而不是由被改的那个函数决定。**参照实现的位置
本身就是清单**:PUC 把错误放在 `push_onecapture` 里,那么「谁调 `push_onecapture`」就是清单,grep 一遍
`_lua515/` 比凭记忆枚举可靠。本轮第一版修法就栽在这里——只在 `%n` 展开那条路径加了检查,而表替换会
**无条件**读捕获 1,于是 `gsub("alo","(.",{})` 本该抬错却成功了。

**判据二:改动一个语义边界之后必须跑官方测试套,因为 fuzz seed 是单向的、官方套是双向的。** 差分 fuzz
报的是「望舒抬了而 lua5.1 没抬」(或反之)的某**一个**具体输入,而一次边界移动同时改变边界两侧的行为;
官方套同时断言哪些必须成功、哪些必须失败,因为它就是为「这个边界在哪」写的。抓到上面那个错误的正是
`test/luasuite/testdata/pm.lua:193`(`assert(not pcall(string.gsub, "alo", "(.", {}))`),oracle 的 seed
只覆盖「不该抬错」那一侧。自查办法:写下「我把这个边界从 X 挪到了 Y」,再问「Y 那一侧的用例来自哪里」
——如果全部来自 fuzz seed,那就只测了一侧。与 [[prove-the-path-under-test]] §4「覆盖度先 grep 既有
oracle 再决定是否补语料」是同一件事的另一个入口:那条讲补语料前先看仓里有没有,本条讲改边界后必须去跑
那个已有的。反思 [[2026-08-04-issue228-229-lazy-capture-and-nested-tailcall-top]] 教训 1/2。

**判据三:懒抬检查还要保持参照实现的粒度与求值顺序。** 找对 materialize 点仍不够:参照函数一次读取
单个元素,望舒就不能先检查整个集合;参照循环从左到右在首个失败处立即返回,望舒就不能把错误暂存到循环
结束后再抬。#228 的后续审计先发现整列表检查让未被读取的 unfinished capture 也报错,再发现
`gsub("ab", "(", "%1%2")` 被延迟检查错误地报成后一个 `%2` 的 `invalid capture index`,而 PUC 在
读 `%1` 时已经报 `unfinished capture`;反转模板顺序后又应以 index error 为先,所以规则不是错误优先级,
而是**首个被求值的失败优先**。自查时把参照函数的参数粒度、循环方向、每个 early return 一起抄成
执行顺序表,并用能同时触发两种错误的输入验证谁先抬。

**第七个刻度:一个分支的**所有出口**都要对一遍,不只对 seed 走到的那个。** 前六个刻度讲读到源码那一行
之后还要看什么(转换链、条件项、消息包装、有定义 / UB、校验在控制流的位置、错误从哪个函数抬出);本条讲
读对了那一行之后**横向**还差什么。定位到参照实现的某一行时,注意力会停在「这一行怎么让 seed 走到这个
结果」,而那一行往往是一个**分支的入口**,分支有多个出口;fuzz 报的是其中一个,其余出口的错误只是还没被
采样到。

**实证(2026-08-05,#234)**:seed 是 `print(string.gsub("aaa","a","%",0000000001))`,报的是末尾 `%` 的
行为。PUC 的 `add_s` 里那个 `%` 分支有**三个出口加一个边界情况**:`%%`、`%0`–`%9`、其余非数字、以及
`i++` 之后越过长度读 `news[i]`。望舒只对了前两个:末尾的 `%` 被当成字面量(要求了 `i+1 < len(rb)`),
而**任何非数字**被抬成 `invalid use of '%' in replacement string`,所以 `gsub("a","a","%z")` 在 lua5.1
是 `"z"` 而望舒报错——**这一半在 base 上同样分歧、是既有缺陷**,只是没有 issue 记它。只修被报的那一半会
留一个已知的洞。

**判据**:定位到参照实现的某个分支之后,把**那个分支的每一个出口**写成一行「这个出口在望舒是什么行为」
逐行核对,而不是只对 seed 走到的那个出口。这是 [[prove-the-path-under-test]] §4.1「一个 reported case
是接受面的一个采样」在**参照实现的控制流**上的形式:那条枚举「同一段实现有几条到达通道」(输入侧),本条
枚举「参照实现的这个分支有几个出口」(实现侧)。反思
[[2026-08-05-issue232-234-address-width-and-gsub-escape]] 教训 4。

**第八个刻度:「实测得来的边界」仍然需要机制来确定它的形状,否则实测只覆盖到采样点所在的那一格
(2026-08-11,#244)。** 前七个刻度讲读到源码那一行之后还要看什么;本条讲**不读源码、直接实测**这条路
自己的陷阱。参照实现落进 UB 时「以二进制实测为准」是对的(那正是上面「有定义 vs UB」表的前提),但这条
纪律有一个容易被跳过的前置问题——**实测要沿着哪些维度取样**,而回答它只能靠机制。

**实证(#244)**:`luaB_unpack` 在 `i` 很负时让 `n = e - i + 1` 上溢、绕过 `n <= 0` 检查、把一个巨大正值
交给 `lua_checkstack` 从而段错误。第一版守卫按实测把窗口定成「只有 `-2147483648` 与 `-2147483647` 会崩」,
并在注释里明确写「按溢出算术推导出的结论与实测不一致,以二进制为准」。**那些测量本身都是真的**,但它们
全部取自 `unpack({}, i)` 这一种写法,而 `{}` 让 `e = #t = 0`——机制里带着 `e`,所以窗口是一条**随 `e`
平移的界线**,不是一对固定值:

| 写法 | `e` | 崩溃的最大 `i` | 相邻不崩的 `i` |
|---|---|---|---|
| `unpack({}, i)` | 0 | `-2147483647` | `-2147483646` |
| `unpack({1,2,3}, i)` | 3 | `-2147483644` | `-2147483643` |
| `unpack({}, i, 2147483647)` | 2147483647 | `0` | `1` |

把 `e` 放进模型后规则完全可推导:`crash ⟺ i32 <= e32 且 (e32 - i32 + 1) > INT_MAX`(窄化之后的 int32
值),**10 组「先写下预测再实测」全部吻合**——推导与实测其实一致,当时不一致只是因为模型漏了一项。后果是
那个守卫**两个方向都错**:`e > 0` 时窗口整段挪出守卫范围(`unpack({1,2,3},-2147483646)` 经真实
`FuzzOracleDiff` 实测让整个测试二进制 SIGSEGV),而 `i >= 2147483648` 被一律跳过(`4294967297` 窄化成 1、
两侧都返回 3,却被 skip)。

**为什么容易错**:在 `e = 0` 上逐值扫得再密,也永远发现不了缺了一个维度,而「我逐值实测过边界两侧各一格」
这句话读起来已经很扎实——这个错误会**自我确认**。

**判据**:给一个 UB / 溢出类边界定位置时,先写出决定它的**表达式**,再让表达式里的**每一项**各变化一格
实测;注释与 commit message 里写清那个**规则**,只写测量值会让下一个读者把一格当成全部。自查办法:写下
「窗口就是 X」之前,先说出「X 是哪个表达式的解,那个表达式里还有哪些量」——说不出表达式,就说明手上只有
一批采样点、没有边界。这与上面第二个刻度(`unpack` 的上限不是常数 8000 而是减去参数个数)是**同一个函数
上的第二次**:那条讲抄上限时把条件表达式的每一项都问一遍,本条讲实测边界时把每一项都变化一格;定数值的
验证手法见 [[prove-the-path-under-test]] §4.5。反思
[[2026-08-11-issue244-oracle-segv-unpack-int32]] 教训 2。

这类审计不能只比较成功 / 失败。官方套可能覆盖边界两侧,却不比较同一次调用会抬哪一种错误;应补一张
`输入形状 × 消费方式 / 替换模板` 的 oracle 矩阵,逐字节比较返回值或错误类别。矩阵还应顺手扫同一错误的
所有兄弟站点:#228 的 14 种 pattern × 10 种 replacement 扫描发现三个 `invalid capture index` 站点中
两个多带了 `%n` 后缀,代码库内部已经不对称。**同族实现有一个站点与 PUC 一致时,它是核对其余站点的
便宜样板,不是只修当前 reproducer 的理由。**

反思实例见 `memory/reflections/2026-07-12-cgo-oracle-fuzz-round.md` 教训 2(一轮里 35 处分歧全部经此手法定位)。与本 guide 已有的「跨后端 / 跨通道枚举」纪律同域:跨后端扫要枚举实现,与 PUC 差分要枚举权威源码。

延伸(真值最终落在宿主 libc 时,读 C 源码只是第一步):PUC 语义不只由 C 源码定义,**非有限值(NaN/Inf)的格式化还由宿主 libc(glibc)定义**。`string.format` 的 `%f/%e/%g/%E/%G` 对 NaN/Inf 转发给 C `sprintf`,输出的大小写拼写(小写 verb → `nan`/`inf`,大写 → `NAN`/`INF`)、符号规则、以及 glibc 为 NaN 保留符号列导致的 width−1 quirk(见下),grep `_lua515/` 只能看到「转发给 `sprintf`」,真正的真值在 libc 里。这类分歧必须以 oracle 实测字节为准,不能照 Go `fmt` 或凭直觉。glibc 的确切规律:glibc 总为 NaN 保留 1 个符号列;小写 verb 符号不显示(那一列变空格被 width 吸收 → 有效 width = 声明 width−1),大写 verb 符号是可见的 `-`(已在 core 里占了那一列 → 完整 width);Inf 符号一直在 core 里 → 完整 width;precision 对 NaN/Inf 忽略。方法论要点:**对付「宿主 libc 定义的格式化」这类外部真值,不要从一两个样本外推规则,直接构造覆盖矩阵(verb × 符号 × flag × width)扫 oracle,规律要能解释矩阵里每一格才算定准**——本轮(#170/#171,PR #172)正是从单点「小写 NaN width−1」外推「所有非有限值 width−1」,一步把 Inf 全改错,靠 93 组覆盖矩阵实测才把完整真值表逼出来。实现落点:`internal/stdlib/stringlib.go` 的 `cFormatSpecialFloat` 在 NaN/Inf 时特判;反思实例见 `memory/reflections/2026-07-22-oracle-format-nan-inf-round.md` 教训 1/2。**2026-07-26 修订:模仿 glibc 的那两处已经撤掉**——`cFormatSpecialFloat` 现在让 NaN 在所有 verb 下都不带符号、都按完整声明宽度补齐(大写 verb 不再硬编码 `-NAN`,小写 NaN 不再按声明宽度减一补齐),Inf 的符号与宽度规则不变。原因:那两处只为让差分 oracle 一致而存在,却让望舒自身的 `%e` 与 `%E`、`%5f` 与 `%5E` 自相矛盾,而 arm64 的 glibc 与 x86 还不同,模仿本来就不可移植;NaN 符号差异现在在 oracle 渲染处消除(`internal/oracle/lua515.c`,详见 `docs/design/p1-interpreter/12-testing-difftest.md` §4.2)。方法论那条(外部真值面要建覆盖矩阵、不从单点外推)仍然成立;附加一条:**在产品代码里逐字节模仿一个宿主 libc 之前,先问这个模仿是为谁服务的**——如果只为让测试基准一致,那它同时会把不可移植性写进产品行为,应该改在基准侧消除差异。

延伸(面级规则:先测绘整个行为面再实现):分歧涉及「派生逻辑」(名字从哪来、计数怎么减、回退到什么)而不是「输出格式」时,默认背后是 PUC 的一整个子系统,不是一条孤立措辞。先写探针套把完整行为面测绘成对照表,再一次性实现,避免「修一条、fuzz 再打穿一条」的逐点返工。实证:issue #133(2026-07-14,PR #134)——一个 fuzz 种子表面是 `coroutine.create(coroutine.resume)` 错误消息不同,实际是 `luaL_argerror` 的函数名派生规则整体分歧:PUC 的 `bad argument #N to 'name'` 中 name 来自**调用方的调用点**(ldebug.c `getfuncname` → `getobjname` 对 CALL/TAILCALL/TFORLOOP 的 A 操作数做 symbexec),推论包括别名命名(`local r = string.rep; r(nil)` 报 `'r'`)、method 调用 self 不计入 #N 且减到 0 时改报 `calling 'X' on bad self`、TFORLOOP 站点报 `"(for generator)"`、纯 C-to-C 边界保持 `'?'`;~70 条探针先测绘全部分支,然后一次实现(结构化 `NewArgError` + `resolveArgError` 在 Lua 调用边界统一改写,`callLuaFromHost` wrapper 冻结 `'?'`),全部探针逐字节一致。配套模式:错误消息依赖抛出点拿不到的上下文时,用「错误对象携带结构化字段 + 拥有上下文的边界层统一改写」,不要把上下文穿透传给每个抛出点(~84 个 stdlib 站点若改签名代价不可控);「解析权冻结」(wrapper 把结构化字段归零)防止错误穿越多层边界后被外层调用点错误重新命名。反思实例见 `memory/reflections/2026-07-14-issue133-argerror-caller-name-round.md`。

延伸:「改写输入再委托宿主标准库」是不收敛适配路径,第 N 次被打穿时换成手写。同一个 site(比如 `internal/stdlib/stringlib.go` 的 stringFnFormat unsigned 分支)如果曾经通过「改写 spec 后交给 Go `fmt.Sprintf` / `strconv.*` / 宿主 libc 类库」的方式适配 C 语义,而 `FuzzOracleDiff` 又反复在这个 site 上撞出新分歧(Go `fmt` 与 C `printf` 对 `%#X` 零值前缀 / `%#08X` 补零位置 / `%#.0o` 零值 / `%s` 的 `'0'` flag 是否 pad 等等就有多处分歧,ICU / RE2 / 宿主时区表也同样),这条路径就是不收敛的:两个实现各自演进,分歧集合是开放的,补丁式修复只能覆盖「已被撞到的那一处」。判据一旦成立(同一 site 第 2 次以上被打穿,且新分歧仍在同一语法维度上),就把这一段整段换成手写的 C 语义 renderer,把语义收敛为封闭规则(C99 printf 一页写完 / 官方 `_lua515/` 对应的 C 函数几十行写完)。实证:2026-07-13 nightly 巡检轮里 stringFnFormat 的 `%u/%x/%X/%o` 分支第三次被打穿(前两次 `%100X` 宽度与 `% 00X0` 忽略旗标,这次 `%#X` 零值),从 `bytes.ReplaceAll(spec, ...) + fmt.Sprintf` 换成手写 `cUnsignedFormat`,41 个覆盖前缀 / 宽度 / 精度 / 旗标交互的用例逐字节等于 PUC。触发场景:同一个「改写输入 + 委托宿主标准库」site 被 differential fuzz 打穿第 2 次时,不要再补一发 `ReplaceAll` 或 `strings.ReplaceAll`,直接换手写实现;写新 site 前也要看 Go 标准库对该语义有没有已知的多点分歧,有就直接手写。同族反思实例见 `memory/reflections/2026-07-13-nightly-concat-oom-and-format-hash-round.md`。

延伸(stdlib 的跨 number/string 边界强制转换,复用 VM 侧权威实现别自写标准库简化版):stdlib 里凡是「字符串→数字」「数字→字符串」这类跨 number/string 边界的强制转换,必须复用 VM 侧对齐 PUC 的权威实现(`crescent.ParseLuaNumber` / `crescent.FormatLuaNumber`),不要用 Go 标准库的简化版。Go `strconv.ParseFloat` 不认 Lua 十六进制整数(`"0X0"` 类,它只接受 C99 hex float),接受面与 PUC `luaO_str2d` 不一致;两份实现并存会让 stdlib 侧的强制转换宽松度系统性低于 VM 侧,而且这个差异不会立刻暴露——要等某个恰好落在差异区的输入被 fuzz 撞出来。这与本节「跨后端扫要枚举实现」是同一原则在「同一进程内同一能力两份实现」维度的延伸:能力已有权威实现时经统一入口复用,别在别处另写一份。实证:issue #174(2026-07-23,PR #176)——`string.rep("...", "0X0")` 的次数参数是 Lua 十六进制整数字符串,PUC 用 `luaL_checknumber` 强制转成 0,wangshu 的 `toNumberStr`(`internal/stdlib/stdlib.go`,被 string/table/math 各库约 28 处调用)用裸 `strconv.ParseFloat` 不认 hex 整数而报错;仓库其实早有对齐 PUC `luaO_str2d` 的 `crescent.ParseLuaNumber` 却没被复用。修法把 `toNumberStr` 改走 `crescent.ParseLuaNumber`,一处对齐所有调用点。同轮 issue #175 是 `tonumber(x, base)` 的第一参数按 PUC `luaL_checkstring` 接受 number 强制转 string。触发场景:给某个「X → Y」语义敏感的基础转换加实现或改行为时,先 grep 全仓(尤其 crescent / VM 侧)看有没有已存在的权威实现可复用,别另写标准库简化版。反思实例见 `memory/reflections/2026-07-23-oracle-arg-coercion-round.md` 教训 1。

## 「对齐 PUC」之前先分清那个行为是有定义的还是 UB

「与 PUC byte-equal」这个目标默认假设 PUC 有唯一确定的行为。落进 C 的未定义行为时这个假设不成立——此时「对齐 PUC」这句话本身没有指称对象,两个官方 build 自己就不一致,硬对齐等于把某台机器的偶然结果写成规范。反过来,落在**有定义**区时跳过比对是白白丢掉覆盖面。所以处理一处 C 语义分歧之前,先查那个操作在 C 标准里是**有定义 / 未指定 / 未定义**,再决定对齐还是跳过。

| 分类 | 处理 | 实证 |
|---|---|---|
| **有定义的 C** | **对齐**(把 C 的规则写进 wangshu) | `strtoul` 的无符号取反与溢出饱和:`tonumber("-7",8)` 得 2^64-7、`("-ff",16)` 得 2^64-255、20 个 `f` 配 base 16 饱和到 2^64-1(2026-07-28) |
| **UB 且跨 arch 不一致** | 产品侧**钉参照平台**(x86-64)+ harness 侧**跳过那个区间** | `%u/%x/%o` 的 `(unsigned long long)(double)`(#158,`cUnsignedCast`);`string.char` 的 `luaL_checkint` 越界 double→int(#193,`cCharCast`)——x86-64 `cvttsd2si` 给 `INT64_MIN`(低 32 位 0,PUC 接受),arm64 `FCVTZS` 把 `+inf` 饱和到 `INT64_MAX`(低 32 位 -1,PUC 报错) |
| **C 未指定(unspecified)** | **两侧都不对齐**——取可辩护的行为,harness 只跳歧义写法 | 函数实参求值顺序:PUC 写成 `f(luaL_checknumber(L,1), luaL_checknumber(L,2))`,gcc 在 x86-64 上从右往左、在 arm64 上从左往右,于是两个官方 build 对同一个调用报**不同的参数编号**。改成报第一个缺失 / 出错的参数,harness 只跳「多于一个坏参数」那种编号取决于顺序的写法;单个坏参数两边编号一致,照旧比对(2026-07-28,`f9425ab`) |
| **参照实现自相矛盾** | 查**语言规范**怎么说,按规范选,把偏离记进注释 | `print` 对内嵌 NUL 截断而 `io.write` 不截断:两者都在 PUC 里、处理的是同一种字符串,`luaB_print` 用 `fputs`(停在第一个 NUL)而 `g_write` 用带长度的 `fwrite`(不截断)。5.1 手册明确字符串是 8-bit clean 可含 NUL,所以那个截断是 C 调用的产物不是语义,对齐它等于故意丢用户数据;wangshu 选**不截断**,理由记在 `internal/stdlib/stdlib.go::baseFnPrint`(2026-07-28,#199) |

**第三、四格与前两格的判断层次相同,证据来源不同**:前两格看 C 标准怎么规定,第三格看标准明确「不规定」,第四格看**参照实现自己是否自洽**——同族的两个函数对同一件事给出相反答案时,「对齐 PUC」同样没有唯一指称对象,照抄哪一个都是把某条 C 调用的副作用写成语义。第四格的判据可操作化:问「这两个函数处理的是不是同一种值、做的是不是同一件事」,是就去查规范;偏离规范的那一侧就是产物。选定之后注释里要写清三件事——哪个是产物、为什么不对齐、以及在差分侧为什么不表现为分歧(`print` 那处:差分 harness 用自己的累积器捕获输出,不经 C `FILE*`)。

**只跳 UB 区间,不要顺手把周边一起跳掉**:`string.char(2^53)` 的输入大,但 int64 可表示,截断在 C 里有定义,所以照旧逐字节比对;跳过的只是 NaN 与超出 int64 范围那一段。同理 in-range 的小数、负数、`[0,255]` 边界全部保持比对。

**第五格:有定义但可疑——判据不在「有没有定义」那一维,而在「我们比什么」那一维(2026-08-05,#234)**。
上表四格分的是**参照实现那个行为的性质**;还有一类行为落不进任何一格:它**有定义、跨平台一致、可以照抄**,
只是它的确定性来自一个实现细节而不是一条语义规则。看到这种行为时第一反应是「这不该照抄」,而这个反应
跳过了一个前提问题——**我们的验证口径是什么**。

**实证(#234)**:PUC 的 `add_s` 对末尾的 `%` 做 `i++` 然后**越过长度读 `news[i]`**,读到的是
`lua_tolstring` 保证的 NUL 终止符,于是**每次匹配吐一个 NUL 字节**(`gsub("aaa","a","x%")` 是
`x\0x\0x\0`,hexdump 实测)。这**不是** C 的 UB(那个位置确实有一个 NUL,读它有定义、结果确定),也**不是**
「参照实现自相矛盾」(PUC 内部一致)。但它显然是怪癖而非规则。判断照抄不照抄的那个量是**口径**:oracle
**逐字节**比较,不照抄的话凡是以 `%` 结尾的替换串就永远不可比——而且那个差的字节还会流进长度、比较、
表键(与 [[prove-the-path-under-test]] §9.0 那条「符号字节一进字符串就是普通数据」同一机制)。

**判据**:遇到参照实现的可疑行为,先问「**我们的验证口径是什么**」——逐字节比较就必须照抄,只比语义就
可以不抄;照抄时在注释里写清这是**怪癖而非规则**,否则下一个读它的人会把它当成一条可以推广的规则。
自查办法:照抄之前写下一句「不照抄的后果是什么」;答案是「某类输入永远不可比」就照抄,答案是「某个边界
值上不一样」就先按上面四格分类。反思 [[2026-08-05-issue232-234-address-width-and-gsub-escape]] 教训 5。

**顺序上还有一步在这之前**:UB 区间的跳过属于 [[prove-the-path-under-test]] §9.0 的「两侧本来就是不同的东西」那一类(两个官方 build 各给一个结果,没有「正确值」可对齐),所以在比较侧跳过是对的;而「同一个抽象值的不同书写方式」(NaN 符号位)必须在渲染处消除,不能设计判据。判位置在前,判有定义 / UB 在后。

**执行体纪律**:决定跳过之后,那个跳过必须有代码实现,并且注释要指向它——本仓的两个执行体是 `internal/oracle/prelude.go` 的 sentinel(差分 harness 侧)与 `test/difftest/corners_test.go::exemptions`(conformance 侧)。写「已登记为豁免」却没有执行体的注释见 [[prove-the-path-under-test]] §4.2(`strtoul` 那处假豁免让分歧活了很久)。

**执行体的覆盖面必须含被豁免函数的全部别名与兼容名(2026-08-02,#217/#219)**。上一段讲豁免要**有**
执行体,本段讲那个执行体**盖到哪里**。`__wrapArgOrder`(第三格「C 未指定的实参求值顺序」那条豁免的
执行体)只包了 `math.fmod`,而 `math.mod` 是 `LUA_COMPAT_MOD` 下**同一个 C 函数**的 5.0 别名——那条
性质(两个官方构建对求值顺序不一致)对它逐字成立,而按名字写的判据看不见它。于是完全相同的写法仍然
可报,被 fuzzer 开成**两个** issue(#217、#219,同一个 seed hash);`math.atan2` 也从来没被包过。

**判据**:豁免的正确边界是「**具备被豁免那条性质的全部入口**」,而人写清单时枚举的是「我记得的那几个
名字」。所以加豁免 / skip / 包装时:① 先查有没有别名或兼容名——Lua 5.1 里就是 `LUA_COMPAT_*` 那一族
(`math.mod` = `fmod`、`string.gfind` = `gmatch`、`table.getn` / `setn`);② 用一句**可以拿去枚举的
性质**来写清单,而不是往清单里再补一行(本轮的写法是「math 表里所有取两个数经 `luaL_checknumber` 的
入口」,现在覆盖 `fmod` / `mod` / `pow` / `ldexp` / `atan2`)。自查办法:同一个豁免被 fuzzer 报了第二次
而产品并没有缺陷时,先怀疑判据的**覆盖面**,不要怀疑判据本身。这与本 guide 开篇「枚举全部后端 × 通道,
不凭记忆」是同一条纪律换了对象——那条枚举**实现站点**,本条枚举**入口名**。反思
[[2026-08-02-issue212-219-fuzz-crasher-batch]] 教训 2。

反思实例见 `memory/reflections/2026-07-28-four-diff-divergence-issues.md` 教训 4(有定义 vs UB)、`memory/reflections/2026-07-28-issue197-199-stdlib-semantics.md` 教训 4(参照实现自相矛盾)与 `memory/reflections/2026-07-18-issue155-158-nightly-crasher-round.md` 教训 3。

## fast-path template 与 deopt helper 的两条契约

### 契约一：恢复 host helper 所需的 slot shape

优化后的 fast-path template(为了性能省略 spill、把值烧成 imm64 或只留寄存器)必须显式**在 deopt 路径上恢复省掉的 slot 到 interpreter-shape**,再调 host helper。host helper 是共享层实现,入参约定就是 interpreter-shape slot 有值——不是它去嗅探 XMM 或 imm 位模式。快路径省 spill 是本地优化,deopt 是通往共享层的出口,出口处必须把状态还回共享层约定的形式。

**心理边界**:与本 guide 已有条目「同一语义在多个后端 / 多条 emit 通道里各写一份 inline 快路径,修复要枚举所有站点」是同一条纪律的**跨层对偶**——那里是「同一语义在多个 emit 站点各绕过一次 host,修复要全枚举」,本条是「优化侧假设(哪些 slot 快路径不写)与 helper 侧约定(哪些 slot helper 期望有值)错配,deopt 路径必须显式恢复」。两者都是「快路径独立绕过共享层语义」的家族。

**红旗信号**:碰到 host helper 报意料之外的错误信息(如 host.ForPrep 该报「limit must be a number」却报「initial value must be a number」),红旗指向 slot 被读到默认值(Nil / 0)而不是真值,不是 helper 有 bug。差分测试全绿的 fast-path template 不构成 deopt 路径正确性证据——差分覆盖 happy path,deopt 走一次要求 fast-path 拒收(如输入触发 non-number 类型错误)。

**检查项**:审 fast-path template 时把「哪些 slot 在快路径里不写(imm 烧入 / 只进寄存器 / 编译期常量塞入)」与「哪些 slot 是对应 host helper 期望有值的」两个集合列出来,**交集就是 deopt 前必须显式 SetReg 恢复的 slot 集合**。fast-path template 里在 p4Code / nativeCode 结构上挂 imm 快照字段(如 `forLoopInitK` / `forLoopStepK` uint64 NaN-box),deopt 前透传给 host.SetReg。

**实证**(issue #177,2026-07-24,PR #178):p4Code shape-template 的 MOVE-limit FORLOOP fast-path 把 init/step 烧成 imm64、limit 只进 XMM 从不写回 slot,R(A)/R(A+1)/R(A+2) 从未被写。deopt 路径直接调 `host.ForPrep(base, pc, forLoopA)`,helper 读到全 Nil,第一个失败的 Nil-non-number 检查落在 init 上,报错位落在 init 而不该报的 limit(触发形状:`function sum(n) for A=0,n do end end sum "7"` —— string limit 触发 deopt,P1 解释器正确 coerce `"7"` 通过)。修法:p4Code 增 `forLoopInitK` / `forLoopStepK` 两个 uint64 NaN-box 字段(compiler.go 构造时从 `shapeInfo.forInitK` / `forStepK` 传入),deopt 前 `SetReg(A, forLoopInitK)` / `SetReg(A+1, GetReg(limitReg))` / `SetReg(A+2, forLoopStepK)`,helper 于是能正确 coerce string limit 或对真非 number limit 报正确的「limit」错误(byte-equal 于 P1)。反思 [[2026-07-24-p4-template-forprep-deopt-round]] 教训 2、教训 4(改深层 JIT 数据流前先读字段定义与全部消费点,确认编码/语义与新用途一致——本轮确认 `shapeInfo.forInitK` 就是 `uint64(kInit)` 直传的 NaN-box u64,与 `SetReg(idx, u64)` 入参编码对上,可直接透传)。

**扩面动作**:审 p4Code 其他 shape-template 的 deopt 路径,同规则扫一遍——快路径为性能省略 spill 的 slot,deopt 路径都得显式 SetReg 补上;PJ10 native emit 侧 FORPREP / 其他 op 走 host helper 的 deopt 也按同一契约核对(本轮修 FORPREP 一条,其他 op 未系统检查,列入后续动作)。

### 契约二：补齐被替代字节码的全部可观察副作用

deopt helper 不能只修复触发失败的那一步；如果 deopt 分支随后直接 return，必须逐条核对它替代的整段字节码，并保留每条指令的可观察副作用。检查集合至少包括 register / upvalue / global 写入、`preempt()` 的 step budget 与 cancel context 探测、GC safepoint、IC 记账、Lua call 的 frame 变化、stack shape 和 traceback PC。helper 覆盖矩阵有缺口时，结果值 byte-equal 仍可能掩盖资源限制或控制流语义已经被跳过。

**实证**(issue #177 合入前审查,2026-07-24):初版 slot-shape 修复在 `host.ForPrep` 成功后直接 `DoReturn`，省掉了原序列中的 FORLOOP。即使 Lua loop body 为空，每轮 FORLOOP 仍会执行 `preempt()`；因此 `sum "1000000"` 在 P1 会触发 instruction budget 或 context cancel，P4 却立即返回。最终新增 `host.ForLoop`，按解释器顺序执行 idx 更新、limit 比较、preempt、`R(A)` / `R(A+3)` 写回，再由 deopt 分支按 `ForPrep → ForLoop → DoReturn` 收尾。

**检查与测试**:deopt 分支直接 return 前，先列「被替代字节码 → 可观察副作用 → 对应 helper」三列表。折叠 loop 时至少用长循环分别钉住 step budget 与 cancel context；同时断言 `PromotionCount` 与该 deopt 分支的专用 hit counter 都有增量，防止测试静默留在解释器、由解释器产生同样错误而假绿。详见 [[prove-the-path-under-test]] §7.1 与 [[2026-07-24-p4-template-forprep-deopt-round]] 教训 5。

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
  `2026-07-22-oracle-format-nan-inf-round`(PUC 语义由 libc 定义:`string.format` NaN/Inf 对齐 glibc) /
  `2026-07-23-oracle-arg-coercion-round`(stdlib 强制转换复用 VM 侧权威实现:`toNumberStr` 走 `crescent.ParseLuaNumber`,#174/#175) /
  `2026-07-24-p4-template-forprep-deopt-round`(fast-path template deopt 路径必须显式 SetReg 恢复省略 spill 的 slot 到 interpreter-shape 再调 host helper,#177) /
  `2026-07-28-four-diff-divergence-issues`(「PUC 语义由 C 实现定义」的更细刻度:`luaL_checkint` 的隐式两步转换 + 「有定义 vs UB:对齐还是跳过」新节,#192/#193/#194/#196 一轮修六个根因) /
  `2026-07-28-issue197-199-stdlib-semantics`(该节第三、四格:C 未指定的实参求值顺序两侧都不对齐 + 参照实现自相矛盾时按语言规范选,`print` 截断 NUL 而 `io.write` 不截断,#197/#198/#199) /
  `2026-07-28-issue201-203-unpack-skip-thresholds`(「PUC 语义由 C 实现定义」的又一刻度:C 侧的上限条件可能同时读调用当时的栈状态,`unpack` 的真实上限是 `LUAI_MAXCSTACK` 减参数个数而不是常数 8000;`os.time` 的 `isdst` 转发给 `mktime` 的 `tm_isdst`,#201/#202) /
  `2026-07-29-issue205-206-208-io-userdata-debug`(该节第四刻度:错误消息也有一条包装链——`luaL_checkstack` 把调用者的文本套成 `stack overflow (%s)`,`string.byte` 的上限公式与 `unpack` 一样但消息不一样,#206) /
  `2026-08-02-issue212-219-fuzz-crasher-batch`(该节第五刻度:校验在控制流里的位置也要照抄——`string.gsub` 的 repl 类型检查从循环之前挪进循环内部,一个把次数压成 0 的参数就让它整个失效,#216;另有「豁免执行体的覆盖面必须含别名」——`__wrapArgOrder` 包了 `math.fmod` 没包它的 `LUA_COMPAT_MOD` 别名 `math.mod`,同一写法被开成两个 issue,#217/#219) /
  `2026-08-05-issue232-234-address-width-and-gsub-escape`(该节第七刻度:一个分支的**所有出口**都要对
  ——`add_s` 的 `%` 分支有三个出口加一个越界读,望舒只对了两个,`gsub("a","a","%z")` 那一半在 base 上就
  是错的;以及「有定义 vs UB」表的**第五格「有定义但可疑」**:这类行为该不该照抄,由「我们比字节还是
  比语义」决定,而不是由那个行为好不好决定,#234) /
  `2026-08-11-issue244-oracle-segv-unpack-int32`(该节**第八个刻度**:「实测得来的边界」仍然需要机制来
  确定它的**形状**——`luaB_unpack` 的崩溃窗口被按 `unpack({}, i)` 实测成一对固定值,而崩溃条件
  `(e - i + 1) > INT_MAX` 里带着 `e`,窗口随 `e` 平移;补上 `e` 之后 10 组预测全部吻合实测,推导与实测
  本来一致、当时不一致只是模型漏了一项。这也是本篇第二个刻度在**同一个函数**上的第二次命中,#244)。

