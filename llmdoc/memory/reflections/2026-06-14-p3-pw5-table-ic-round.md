---
name: p3-pw5-table-ic-round
description: P3 PW5 表 IC opcode 翻译轮(翻译复杂度峰值)过程教训:设计稿 WAT 里的 $helper 伪码须按边界成本预算重判该不该 inline、inline 覆盖按「逐字节可证性」分级而非「能不能做」、两块控制结构恒定 br 深度、inline 路径须用毒化哨兵助手证明跳过了哈希
metadata:
  type: reflection
  date: 2026-06-14
---

# P3 PW5 表 IC opcode 翻译轮反思(翻译复杂度峰值)

> 范围:PW5 把表访问 opcode(GETGLOBAL/SETGLOBAL/GETTABLE/SETTABLE/SELF/NEWTABLE/SETLIST)翻译为 Wasm,核心是 **inline IC 快照固化**——快路径在 Wasm 里直接读 Lua table 字段(gen 校验 → 直 load array/node 槽)真正**跳过哈希查找**;失效(rehash→gen bump / 换表 / nil 槽)降级到 imported 助手复用解释器路径(逐字节一致)。零 deopt(P3 是 try-compile,非投机)。四提交:`bb3f16f`(PW5-a GETGLOBAL/SETGLOBAL + 公共基建)→ `1ae8fa1`(PW5-b GETTABLE/SETTABLE 动态键匹配)→ `e9814e7`(PW5-c SELF)→ `5c181b1`(PW5-d NEWTABLE/SETLIST 纯助手)。承 `02-translation.md` §3.4 + `06-ic-feedback-consume.md`。

## 核心教训

### 1. 设计稿 WAT 里写成 `(call $helper ...)` 的快路径,实现前必须按边界成本预算重判该不该真做成跨层调用

`02-translation.md` §3.4 的 WAT 伪码把 `$is_table`/`$table_gen`/`$ic_slot_load`/`$ic_key_match` 写成了助手调用形态。但 PW0 spike 实测一次 gibbous→host imported 调用约 **143ns**——若快路径上调一次助手,「跳过哈希」省下的几个 ns 当场被边界成本吞光,整个 IC inline 的收益归零。

正确做法是**全 inline Wasm load**:让这些「助手」退化成几条 `i64.load`。物理基础与 VS0 值栈「形态 Y」同一条 arena=linear memory 对偶——table 活在 arena = wazero linear memory,`value.GCRefOf(v)`(低 48 位)**就是**字节偏移,所以 table 字段直接 `i64.load offset=...` 可寻址:gen 在 offset 40 高 32 位、nodeRef 在 24、arrayRef 在 16、node stride 24B,而 `SNAP_INDEX` 是编译期立即数 → 所有槽 offset 都是常量。这与 VS0 的「arena backing 即 linear memory,偏移现算寻址」是同一条物理洞察的复用(见 `feedback_arena_view_aliasing` / `implementation-progress` §VS0-c)。

**How to apply**:设计稿热路径上的 `(call $xxx ...)` WAT 伪码是**记法**不是**承诺**——`$helper` 这种抽象记号只表达「这里要做 X 语义」,不等于「这里要发一次真实跨层调用」。任何标在热路径(尤其每指令必经的 IC 快路径)的助手记号,实现前都要拿 PW0 的边界成本预算(~143ns/次)重新过一遍:它能不能塌成几条 inline load?能,就必须 inline,否则该 opcode 的整个加速立项失去意义。与 [[issue8-boundary-cost-round]] 教训 1「实现浪费 vs 架构成本」同家族——这里是「设计记法的边界成本未被预算」,同样要在实现侧主动算账,不能照抄伪码。

### 2. inline 覆盖按「逐字节可证性」分级,而非「能不能做」

我没有把设计展示的每种情形都 inline。只 inline 了**可证与解释器逐字节一致**的形态:

- **常量键访问**(同表 + 同代次 ⟹ 缓存的 `Index` 仍映射同一个键,故键匹配可**整段跳过**)——这优雅地顺手解决了「字符串常量键的值烧不进 Wasm」的难题:键根本不需要被比较;
- **寄存器键 ArrayHit**(数值匹配走 `f64(key) == Index+1`,绕开了朴素 `i32.wrap` 会引入的 uint32 截断陷阱)。

我**刻意路由到助手**的情形:寄存器键 NodeHit(inline `normKey`/`keyEqual`/字符串 intern 语义对逐字节一致太脆)、MonoMeta(`__index` 元方法)、带字符串常量值的 SETTABLE(GCRef 烧不进)。安全原则与 PW4 relooper 同:**凡不可证正确 → 退助手**,助手里逐字节一致天然成立。

「不打折扣」(no-compromise)目标的真实含义是**对主导情形交付验收口径**(单态访问跳哈希),而**不是**强行 inline 每一种情形——把脆弱情形硬 inline 反而违背逐字节一致这条更高优先级的底线。

**How to apply**:做 IC/快路径 inline 时,先对每种情形问「我能在 Wasm 里逐字节复刻解释器语义并证明吗?」而非「我能不能在 Wasm 里把它做出来?」。能证 → inline;不能证 → 退助手(那里复用解释器路径,一致性免费)。验收口径只要求主导情形被加速,不要求穷尽。

### 3. 守卫检查多的路径,用「两块结构 + 恒定 br 深度」胜过深嵌 if 的 br 深度算术

GETTABLE/SETTABLE/SELF 用了:
```
block $done {
  block $slow {
    <检查;每个失败:cond; i32.eqz; br_if 0 → $slow>
    <命中:store; br 1 → $done>
  }
  <helper>
}
```
无论守卫检查有多少个,br 深度恒为常量(0=$slow,1=$done),避开了深嵌 `if/then` 需要手算 br 深度的脆弱算术。对照:GETGLOBAL/SETGLOBAL 检查少,用嵌套 if + `br 2` 反而更简单。

**How to apply**:守卫数量会增长、或多个失败点都要跳到同一慢路径时,优先「两块包夹 + 失败 br_if 到内块、命中 br 到外块」的结构;br 深度与检查数解耦,加减守卫不必重算深度。检查极少时嵌套 if 仍可取。

### 4. 证明「跳过了哈希」的 inline-proof 测试,需要毒化/哨兵助手

普通 e2e(调用 → 得到正确值)**无法区分** inline 快路径跑了还是助手回退跑了——两者都正确。所以我加了 translate 层单测(`TestPW5_GetGlobalInlineHit` / `GetTableInlineHit` / `SelfInlineHit`):手工在共享内存里摆好一张 table,要么**毒化助手**(返回 error),要么利用 mock 助手不写 `R(A)` 的事实:若 Run 成功且得到正确值、而助手被调用零次(或 `R(A)` 不是哨兵值),则 inline 路径可证执行了。这是验证**验收口径本身**(「跳过哈希」)的唯一手段,区别于只验**正确性**。

**How to apply**:当验收口径是「某条快路径被走到」(而非只是结果正确)时,普通 e2e 给的是虚假安全感(回退也对)。必须用「毒化对照助手」或「哨兵不写目标寄存器」让两条路径在断言上可分辨,否则测试绿了也证明不了立项目标达成。与 [[test-hardening-round]]「fuzz 目标空转是最危险的虚假安全感」同源——绿色不等于在测你以为在测的东西。

### 5. 机械编辑反复踩坑:Edit 的 old_string 吞掉了相邻函数声明

两次在已有 `func TestXxx(t *testing.T) {` 之前插入新测试函数时,我的 Edit 替换掉了那行签名,留下一段无宿主的孤立函数体(`cases := ...` 无外层 func),产生 `expected declaration, found cases`。两次的修复都是补回被吞的 `func` 声明。

**How to apply**:在已有声明**正前方**插入代码时,old_string/new_string 都要包含**完整的已有声明**(别让锚点骑在声明边界上),或者直接追加到文件末尾。

## 其它(较小)

- **SETLIST C=0**(下一条指令字是批量计数 DATA 而非 opcode)与 **B=0**(填到 top,需 gibbous 帧 top 维护,PW7 才接)在 `SupportsAllOpcodes` 被拒,保守回退。
- **命名冲突**:HostState 的 `GetGlobal`/`SetGlobal`/`Globals` 撞了既有 State 公共 API,改名 `DoGetGlobal`/`DoSetGlobal`/`GlobalsRaw`。
- **pre-commit lint 容忍 gibbous 包未用符号**:整包被 build-tag(`wangshu_p3`)排除在默认 lint 之外,命中 hook 已知的「build constraints exclude all」误报放行路径(承 `c33f599`),故 PW5-a 期预埋的 PW5-b 机件不挡提交。

## 验证(每子里程碑)

4 build 组合(default / wangshu_profile / wangshu_p3 / both)全测 + `-race` + difftest 70 种子逐字节一致 + 对 NEWTABLE/SETLIST 分配型 e2e 跑 GC 压力。

## 促成的稳定文档更新

- `docs/design/p3-wasm-tier/implementation-progress.md` PW5 行 + 「表字段 inline 寻址(全 inline Wasm load,非 imported 助手)」对账条目(已落地,记 offset 布局与 ~143ns 论证)。

## promotion 候选(首次样本,暂留观察)

- **教训 1**(设计稿热路径 `$helper` WAT 伪码须按边界成本预算重判)——这是 P3 翻译全程都会复发的判断(PW6 LEN/CONCAT、PW7 CALL/RETURN 的伪码里还有更多 `(call $...)`),若再遇一两例可考虑提进性能/翻译类 guide,或并入 [[issue8-boundary-cost-round]] 「实现浪费 vs 架构成本」框架的对偶面(「设计记法的边界成本未预算」)。本轮首次样本,暂留 memory。
- **教训 4**(inline-proof 须用毒化/哨兵助手证明走了哪条路径)——「验收口径是路径被走到,而非结果正确」是 P3 投机/分级 inline 全程通用的测试技术,候选进测试类 guide。本轮首次样本,暂留 memory 观察。

## 触发场景

接 P3 后续翻译里程碑(PW6/PW7)、照抄设计稿 WAT 伪码前、做任何「快路径 inline vs 退助手」的分级裁量、写「证明某条快路径被走到」的测试、或在已有函数声明正前方用 Edit 插代码时,看这篇。

## 关联

[[issue8-boundary-cost-round]](边界成本预算 / 实现浪费辨析,教训 1 对偶)· [[test-hardening-round]](绿色≠在测你以为在测的,教训 4 同源)· [[p1-closeout-round]](IC 命中必须验同键——本轮常量键「同表同代次跳键匹配」是其 P3 形态)· `feedback_arena_view_aliasing`(arena=linear memory 偏移寻址,inline load 的物理基础)· `docs/design/p3-wasm-tier/02-translation.md` §3.4 · `06-ic-feedback-consume.md`(失效降级运行期机制)
