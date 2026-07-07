---
name: p3-pw6-crosslayer-call-round
description: P3 PW6 CALL/TAILCALL/RETURN 跨层互调轮(P3 最大里程碑)过程教训:设计稿热路径固定 token(此处 $base)实现前须按本码库不变式(自管 arena 段可重定位)重核否则藏 codebase 专属 UAF、慢路径复用解释器换来逐字节一致+proper TCO+pcall 透明三重免费红利、opcode 既能当 BB 终结又能当单 BB 直线末指令须双路径处理、tier 升层 e2e 里深循环 baseline 会自升进 TierStuck 吸收态
metadata:
  type: reflection
  date: 2026-06-14
---

# P3 PW6 CALL/TAILCALL/RETURN 跨层互调轮反思(P3 最大里程碑)

> 范围:PW6 实现 gibbous→{crescent,gibbous,host} 三向调用分派。gibbous(wasm)帧命中 CALL → 调 imported 助手 `h_call` → Go 侧按被调 tier 分派;错误经 status 链冒泡到 pcall 边界;实参/结果活在共享值栈(arena = wazero linear memory),只有 `base` 跨层。关键复用:crescent 侧 `DoCall` 复用既有 `doCall`(统一 host/`__call`/gibbous 升层/普通 Lua 分派)+ `executeFrom`(嵌套解释器循环)+ `callLuaFromHost` 的 nCcalls 深度计费;TAILCALL 复用 `doTailCall` + `executeFrom` 拿 proper-tail-call O(1) 栈。三提交:`6546e45`(PW6-a CALL 三向分派 + base 刷新 + status 冒泡)→ `5a86294`(PW6-b TAILCALL 帧复用)→ `eaab36d`(PW6-c 错误穿越硬化 + 单 BB TAILCALL 修复)。承 `04-trampoline.md` §2-§4。

## 核心教训

### 1. 设计稿热路径里跨层传递的固定 token(此处 `$base`),实现前必须拿本码库不变式重核——抽象协议会藏 codebase 专属 UAF

`04-trampoline.md` §2.2 把 `$base`(linear memory 里的字节偏移)画成 gibbous wasm 函数入口**一次性收到、全程不变**的 token。但我**编码前先推理**时发现:gibbous 帧经 `h_call` 调一个更深的 Lua 帧时,嵌套的 `execute → enterLuaFrame → ensureStack` 可能触发 `growStack`(`internal/crescent/state.go`),它**把值栈段在 arena 里重定位到别处、改写 `th.stackBaseW`**。调用返回后 gibbous 代码带着**陈旧 `$base`** 恢复执行,指向已 Free 的旧段 → UAF。

解释器对此免疫,因为它每次访问都经 `th.slot(i)`(形式 Y)现算地址;但 gibbous 的 `$base` 是个 **wazero local,函数中途无法自刷新**——这正是 VS0「形式 Y 别名」发现的 gibbous 帧维度对偶(见 `feedback_arena_view_aliasing` / `implementation-progress` §VS0-c)。设计稿假设值栈不重定位,从未触及这点。

**解法**:`h_call`/`h_tailcall` 返回**当前重算的新 base**(i64;负哨兵表错误)而非 status;CALL 翻译做 `local.tee` → 若负则 `return 1`(错误冒泡)→ 否则 `local.set $base` 刷新。用插桩测试证实雷区真实:`helper(100)` 深递归把 stackCap 从 64 撑到 256(段确实搬了家),GC 压力 + 深递归 e2e 会暴露任何漏刷新。

**Why**:抽象调用协议(「传 base、回 status」)是**语义契约**,不携带**物理不变式**。本码库的物理事实是「值栈与对象世界共享自管 arena,任意分配触发的 grow 会搬动段」——这条不变式不在设计稿的视野里,却让设计稿画的「固定 token」在完成时变成悬垂指针。解释器恰好因「每次现算」碰巧免疫,反而掩盖了危险:照搬解释器「能跑」不等于 gibbous「能跑」,因为二者刷新地址的能力不同(一个每访问现算,一个入口锁定)。

**How to apply**:当设计稿把一个**热路径值跨层画成固定 token**(base/指针/句柄/视图)时,实现前别照搬——先把它对照**本码库的内存物理**(这里:自管 arena + 可重定位段)逐条过:这个 token 在它存活的窗口内,底层存储会不会被搬动/失效?谁有能力刷新它、在什么时机?gibbous 这类「入口锁定、中途不能自刷新」的载体,任何「跨层调用后恢复」的点都是潜在失效点,必须由被调侧返回时回传刷新后的值。与 [[p3-pw5-table-ic-round]] 教训 1、[[issue8-boundary-cost-round]] 教训 1 同家族(详见末尾 promotion 节)。

### 2. 慢/通路复用解释器,一举换来「逐字节一致 + proper TCO + pcall 透明」三重免费红利,且让 gibbous 保持纯优化层

PW6 没有在 wasm 里重写调用分派 / 尾调用帧复用 / proper-TCO,而是把它们路由回既有 crescent 的 `doCall`/`doTailCall`/`executeFrom`。gibbous→gibbous 调用也降级为被调方**走解释器**跑(gibbous 再入只是优化层)。这一个决策同时拿到三样东西:

- **(a) 逐字节一致按构造成立**——跟纯解释一模一样的代码路径,无需额外差分测试论证;
- **(b) proper tail call 拿 O(1) 栈是免费的**——`executeFrom` 在解释器里迭代尾调用链(`f(1e5,0)` 不溢出,已验);
- **(c) 错误冒泡 + pcall 清理透明**——gibbous 帧的 CallInfo 形式与 crescent 完全相同,pcall 的 `ciTop` 回退**零特判**就把它们丢弃。

**Why**:这是 wangshu「**解释器永不退役**」原则(`must/design-premises` 贯穿原则)作为**正确性基底**而非仅作 fallback 的兑现。把「难在 wasm 里逐字节复刻的语义」(调用约定、TCO 链、pcall 栈回退)统统当作「已经有一份权威实现」来复用,gibbous 只负责加速**能稳妥加速的直线段**,边界一旦进入复杂控制流就交还解释器。复杂度没有被搬进 wasm,而是被**留在它本就正确的地方**。

**How to apply**:在加速层(JIT/wasm/特化)里遇到「调用约定 / 帧生命周期 / 异常栈回退」这类语义重、逐字节一致脆弱的机制时,默认**路由回基线解释器**,而非在加速层重写。判据:该机制是否已有一份「权威、被差分测试覆盖」的实现?有 → 复用它,把一致性/TCO/异常清理当红利白拿;只在「纯数据流直线段」上做加速。这与 [[p3-pw5-table-ic-round]] 教训 2「凡不可证正确 → 退助手」是同一条纪律的里程碑级放大:那里退的是单个 opcode 的脆弱情形,这里退的是整类控制流机制。

### 3. 一个 opcode 既能当 BB 终结指令(多 BB)又能当单 BB 直线末指令时,两条发射路径都要处理——RETURN 早有的双处理就是模板

我把 TAILCALL 发射接进了多 BB 终结分派(`emitBlockBody`),理由是 `TAILCALL; RETURN` 恒为 2 个 BB(TAILCALL 把 pc+1 标成 leader)。但 `reachableBlocks` 只找到 **1 个**可达 BB——因为 TAILCALL 之后那条 RETURN 是**死代码**。于是该 proto 走**单 BB 直线路径**(`translate.go`),它迭代 `emitOpcode`,而 `emitOpcode` 当时**没有 TAILCALL 分支** → 落 default「未实现」error → Compile 失败 → 静默回退解释器。PW6-c 的测试 `return helper(a)`(一个 tail-shaped 函数)是第一个触到这条路径的,以一句「f 不支持」skip 暴露了缺口。

**Why**:CFG 形状决定走哪条发射路径,而**同一个 opcode 在不同 CFG 形状下落在不同路径**:作为多 BB 的终结子,它走终结分派;作为「整个函数只有一个可达 BB、且它是末指令」的直线尾,它走 `emitOpcode`。我的盲点是把「TAILCALL 后必有 RETURN ⟹ 必 2 BB」当成了真,忽略了「死代码不可达 ⟹ 单 BB」。而 RETURN **早就**两处都处理了(`emitOpcode` + 终结分派),它本该是我接 TAILCALL 时的现成模板——我没有照着既有双处理的 opcode 抄全。

**How to apply**:接一个**兼具「BB 终结子」和「可作单 BB 末指令」双重身份**的 opcode 时,清单化检查两条发射路径(终结分派 + `emitOpcode` 直线路径)是否都覆盖;先看**同类已实现 opcode**(这里 RETURN)是不是双处理的,有就照抄其覆盖面。专门为 **tail-shaped 函数**(`return f(...)`)和 **trivial-leaf 函数** 各写一个测试——它们是与「多 BB 普通函数」不同的 CFG 形状,普通 e2e 测不到。与 [[official-suite-perf-round]]「快路径家族审计」同源:一个机制有几条并行实现路径时,改动/新增必须扫全所有路径。

### 4. tier 升层 e2e 里,深循环/深递归的 baseline 跑会把 proto 自升进 `TierStuck` 吸收态——后续手工 promote 变 no-op,测试静默 skip

P3 不在编译期注入,故 `AnalyzeProto` 把每个 proto 标 `NotCompilable`。e2e 测试用 `promoteProto`(`SetCompilability` + `OnEnter`×阈值)手工强制升层。但一次带深循环/深递归的 baseline 解释器跑(如尾递归 n=1000)会在 baseline 期间**自然触发升层阈值** → `considerPromotion` 走 `NotCompilable` → `TierStuck` 路径 → **永久卡死(吸收态)** → 之后那次手工 `promoteProto` 成 no-op(返回 false → 测试静默 skip)。

**修复**:gibbous 变体跑在**独立的全新 State** 上,且在任何深跑**之前**调 `promoteProto`;解释器 oracle 用**另一个** State。

**Why**:`TierStuck` 是设计上的**吸收态**(一个 proto 一旦判定不可编译就永不再试,避免反复白判)。但测试脚手架「手工 promote」与运行期「自然 promote」共用同一个 `considerPromotion` 计数器:深 baseline 先把计数器推过阈值、把 proto 锁进 Stuck,手工 promote 再来已无力回天。危险在于失败是**静默 skip 而非红**——你以为在测 gibbous 路径,实际跑的全是解释器,绿色给的是虚假安全感(与 [[test-hardening-round]]「fuzz 目标空转」「绿色≠在测你以为在测的」完全同源)。

**How to apply**:写 tier 升层类 e2e 时,**隔离 oracle 与 gibbous 两个 State**,且**在任何深执行之前完成 promote**——因为深循环 baseline 会自升进吸收态、让后续手工 promote 失效。任何带「一次性吸收态 + 计数器触发」的状态机,测试里手工驱动状态前要先确保没有别的路径已把它推进吸收态。配一个「promote 真的生效了」的正向断言(而非依赖「没 skip」),否则静默 skip 看不出来。

## 其它(较小)

- **命名冲突再现**:HostState 的 `Call`/`GetGlobal`/`SetGlobal`/`Globals` 全撞既有 `State` 公共 API,改名 `DoCall`/`DoGetGlobal`/`DoSetGlobal`/`GlobalsRaw`。这已是**复发模式**(PW5 一样的)——HostState 接口里**镜像 opcode 名**的方法天然会撞嵌入 API(`State` 的公共面也按这些动词命名)。接口方法若与宿主 opcode 同名,默认预期会撞,起手就用 `Do*`/`*Raw` 前缀。
- **多值窗口保守回退**:CALL B=0/C=0、TAILCALL B=0(到 top 的多值窗口)需要跨 opcode 维护 `th.top`,而 gibbous 直线代码不做 top 维护 → 在 `SupportsAllOpcodes` 被拒(暂缓),与 PW5 的 SETLIST C=0/B=0 一样的保守回退纪律。
- **`h_call` 返回 i64**(非 i32+status):单个值同时承载「刷新后的 base」与「错误负哨兵」,需要新建 wasm 模块类型(i32×5→i64)+ 用 `i64.lt_s` 判负哨兵。返回值兼载数据与错误信号是「base 必须刷新」这条物理约束逼出来的编码,不是风格选择。

## 验证(每子里程碑)

4 build 组合(default / wangshu_profile / wangshu_p3 / both)全测 + `-race` + difftest 70 种子逐字节一致 + 分配型 GC 压力;另加 base 刷新专项深递归测试(证段重定位下 `$base` 被正确刷新)+ `f(1e5,0)` 尾递归非溢出测试(证 proper TCO O(1) 栈)。

## 促成的稳定文档更新

- `docs/design/p3-wasm-tier/implementation-progress.md`:PW6 行 ✅ + §8 PW6 对账(base 刷新解 growStack 段重定位 UAF,记 `$base` 是可写 wasm param 中途无法自刷新、`h_call` 返回 i64 新 base/负哨兵的论证)+ RW-1/RW-8 回填 + VS0-c 形式 Y 雷区 gibbous 帧对偶条目。

## promotion 候选

- **教训 1**(设计稿热路径固定 token 须拿本码库内存物理重核,否则藏 codebase 专属 UAF)——**这已是该家族第二个实例**:PW5 教训 1 是「设计稿热路径 `$helper` 伪码须按边界成本预算重判」(边界成本维度),本轮是「设计稿热路径 `$base` 固定 token 须按 arena 段重定位不变式重判」(内存物理维度)。两者抽象为同一条:**设计稿在热路径上画的抽象记号/固定 token,都只表达语义,不携带本码库的物理不变式(边界成本、段重定位、根可达性……),实现前必须逐条拿本码库 physics 重核**。PW5 已把它标为「首次样本暂留观察」,**本轮使其从首次样本升为真实模式**(PW5 边界成本 + PW6 段重定位 = 两个独立维度的实例)。建议:可考虑立一篇翻译/性能类 guide「设计稿主张须对本码库 physics 重新验证」(workname: `design-claims-vs-codebase-physics`),把 [[issue8-boundary-cost-round]]「实现浪费 vs 架构成本」、[[p3-pw5-table-ic-round]] 教训 1、本轮教训 1 聚合为一个判断框架。**已提升** → [[design-claims-vs-codebase-physics]] §2 arena 段重定位维度(连同 PW5 §1 边界成本、issue8 §3 成本归类 / §4 根可达性聚合成 4 维判断框架)。
- **教训 4**(tier 升层 e2e 深 baseline 自升进 TierStuck 吸收态 → 后续 promote no-op → 静默 skip)——这是 P3 投机/升层 e2e 全程通用的测试基建陷阱,后续 PW7-PW9 e2e 还会反复遇到。候选进测试类 guide,或并入 [[test-hardening-round]]「绿色≠在测你以为在测的」框架的 tier 升层维度。本轮首次样本,暂留 memory 观察。
- 教训 2(慢路径复用解释器换三重红利)与教训 3(双发射路径)更偏 P3 翻译实现的具体纪律,暂留 memory,不单独提升。

## 触发场景

接 P3 后续翻译里程碑(PW7-PW9)、照抄设计稿把任何热路径值画成跨层固定 token(base/指针/句柄/视图)前、决定「在加速层重写 vs 路由回解释器」一类控制流机制时、接一个兼具 BB 终结子与单 BB 末指令双重身份的 opcode 时、写 tier 升层 e2e(尤其含深循环/深递归 baseline)时、或给 HostState 加镜像 opcode 名的接口方法时,看这篇。

## 关联

[[p3-pw5-table-ic-round]](设计稿热路径记法须按预算重判,教训 1 同家族前一实例;凡不可证 → 退助手,教训 2 单 opcode 形式;命名冲突 / 多值窗口保守回退一样的)· [[issue8-boundary-cost-round]](实现浪费 vs 架构成本,教训 1 家族奠基)· [[test-hardening-round]](绿色≠在测你以为在测的,教训 4 同源)· [[official-suite-perf-round]](快路径家族审计,教训 3 同源)· `feedback_arena_view_aliasing`(arena=linear memory 段可重定位,教训 1 物理基础)· `must/design-premises`(解释器永不退役,教训 2 兑现)· `docs/design/p3-wasm-tier/04-trampoline.md` §2-§4 · `implementation-progress.md` §VS0-c / §8 PW6 对账
