# P3 实现进度对账(implementation-progress)

> 状态:**P3 PW0-PW10 全卷已收口(2026-06-16)**。详细设计齐备(子目录 9 文件约 8800 行);P1 全卷(M0-M14)+ P2 PB0-PB7 + 后续优化轮 #1-#4 全过线;P3 翻译器 + 跨层 + 升层门禁 + 端到端总验收全链路就绪——正确性轴(V1-V13 层间 byte-equal + V17 四 build + V18 -race)+ 性能轴 V14(loop 核 2.58x ≥2x)全过。**PW10 收口**:R1(共享 funcref 表)+ R2(CallInfo→linear-memory = 长期延后的 VS0-e)+ R3(`call_indirect` 直调消 `code.Run` 重入)+ R3c-fix(出错点就地标注)+ R3.5(host helper `WithFunc`→`WithGoFunction` 消反射装箱)+ 零跨界 ①(top mirror 字)+ 基建-a(closure slot 缓存)+ 基建-b(proto cache 段)+ ③a(savedTop)+ ③b(emitReturn 守卫快路径,Wasm 内拆帧)+ ④-i(emitCall 守卫骨架 + fastCallHits mirror 字)+ callOnStack 顶层升层(cl 直接走 enterGibbous + TopLevelUplift 探针)全付;emit 原语 i64.add/i64.or 已保留供未来 ④-ii。**本机 Xeon 6982P 2s×3 count 实测基线(2026-06-16)**:loop 2.95x(+10% over R3.5 2.67x,③ RETURN 拆帧真实收益)/ table 0.88x / call 0.52x / mixed 0.99x。**call 0.52x 是 bench kernel 结构性架构边界**——profile `/tmp/call.prof` 实证四 kernel body 含 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)→ body 不可升 → 顶层升层 + ④ emitCall fast body 均对 bench kernel 无显著效果;**④-ii fast body 留 followup**(预估上限 0.57x 仍 <1x,实现复杂度 ~200 行 wasm 字节级 codegen,UAF 高,ROI/UAF 不利)。**PW10 收口为「已完成子里程碑 + 架构边界文档化」**(详 §14.10),旧文档所述「四核全翻面 table 1.26x / mixed 1.14x」系不同硬件/参数快照,以本机实测为现行基线;「剩 R4/R5 待实现」失实——R4 概念被零跨界 ③b 重新分包为「Wasm 内拆帧 + 守卫快路径」+ ④-i 骨架 + ④-ii followup,R5 re-bench 已并入本节实测基线收口。
> 单一事实源:本文是 P3 实现现状与设计文档差异的对账表(对应 P1/P2 [implementation-progress.md](../p1-interpreter/implementation-progress.md) 的角色)。
> 设计文档集:见 [00-overview](./00-overview.md) §0 文档地图。
>
> **术语:`P-Wasm`(PW)= P3 实现里程碑编号**(对应 P1 的 M、P2 的 PB);PW0 是 spike 检查,PW1-PW9 是翻译器到端到端验收。

---

## 0. 当前状态

**P3 实现:PW0 spike 检查通过 + PW1-PW9 + PW4b 全部交付(含 VS0 值栈 arena 化)。** PW2 trampoline 端到端;PW3 算术+比较;PW4/PW4b relooper + FORPREP/FORLOOP/TFORLOOP;PW5 表 IC inline 跳哈希;PW6 CALL/TAILCALL 跨层互调;PW7 CLOSURE/CLOSE(VARARG 白名单拒);PW8 线程级 tier 规则(协程不升层);**PW9 端到端总验收**——正确性轴(V1-V13 层间 byte-equal + V17 四 build + V18 -race)+ 性能轴 V14(loop 核 **2.58x** over crescent ✅)全过。**全 38 opcode 除 VARARG 外全部可翻译。** 设计文档集已齐备(00-08 共约 8800 行)。

**PW9 性能实测的关键发现(§11 详)**:loop 核 gibbous 2.58x 快于 crescent(V14 ≥2x 达标),推翻 PW9 早期「memory-resident 下 dispatch 消除不足 2x」的结论(那次测的是不升层的 vararg 顶层 chunk = 空测)。但**跨层调用密集核退化**(call 核 0.14x / table 核 0.68x / geomean 0.79x):gibbous→gibbous 经 `h_call` 双跨层(~143ns/次)吃光小叶函数收益。**消除跨层调用税立为后续里程碑 PW10(spike 检查先行)。**

**PW10 收口(2026-06-16,§12/§13/§14 详)**:Phase 0 spike(`spike/p3indirect/` S-A/S-B/S-C,§0.2 存档)裁定架构走 **Arch-2「共享 imported funcref 表」**(各 Proto 模块共享 env holder 导出的同一张 funcref 表、active element 段自注册 `run`,gibbous→gibbous = 经共享表 `call_indirect`),**而非 Arch-1 rebuild-all**。**R1/R2/R3/R3c-fix/R3.5** 已交付(R1 共享 funcref 表 + R2 CallInfo→linear-memory 迁移 = VS0-e + R3 `call_indirect` 直调消 `code.Run` 重入 + R3c-fix 出错点就地标注使错误逐字节等纯解释器 + R3.5 host helper 改 `WithGoFunction` 消反射装箱;详 §12/§13)。**零跨界 ①(top mirror 字基建)+ 基建-a(closure slot 缓存)+ 基建-b(proto cache 段)+ ③a(savedTop)+ ③b(emitReturn 守卫快路径,Wasm 内拆帧)+ ④-i(emitCall 守卫骨架 + fastCallHits mirror 字)+ callOnStack 顶层升层(cl 直接走 enterGibbous + TopLevelUplift 探针)全付**(详 §14)。**本机 Xeon 6982P 2s×3 count 实测基线(2026-06-16)**:loop 2.95x(+10% over R3.5 2.67x,③ RETURN 拆帧真实收益)/ table 0.88x / call 0.52x / mixed 0.99x。**call 0.52x 是 bench kernel 结构性架构边界**(详 §14.10):profile `/tmp/call.prof` 实证 call 核 52% 在 enterGibbous + 38% 在 wazero CallWithStack,根因四 kernel 结构均为 `body → fn`,body 调内层触发 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)→ body 不可升 → 顶层升层 + ④ emitCall fast body 对 bench kernel 无显著效果;**④-ii fast body 未交付**(预估上限 0.57x 仍 <1x,实现复杂度 ~200 行 wasm 字节级 codegen,UAF 高,ROI/UAF 不利,emit 原语 i64.add/i64.or 已保留供未来 ④-ii)。**PW10 收口为「已完成子里程碑 + 架构边界文档化」**——旧文档「四核全翻面 table 1.26x / mixed 1.14x」系不同硬件/参数快照(以本机实测为现行基线,历史数字保留作演化对照);「剩 R4/R5 待实现」失实——R4 概念被零跨界 ③b 重新分包为「Wasm 内拆帧 + 守卫快路径」+ ④-i 骨架 + ④-ii followup,R5 re-bench 已并入本节实测基线收口。

**前置条件检查**:
- ✅ P1 全卷已交付(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮)
- ✅ P2 PB0-PB7 + 后续优化轮 #1-#4 全过线(2026-06-13)
- ✅ P3 设计文档完整(00 总览 + 01 spike 检查 + 02 翻译器 + 03 内存模型 + 04 trampoline + 05 safepoint-gc + 06 feedback 消费 + 07 协程线程规则 + 08 测试验收)
- ✅ **P3 PW0(wazero call boundary spike < 150ns)检查通过**——S2 主指标 36.7ns ≪ 150ns(详见 §0.1 spike 数据存档);**P3 开工放行**
- ✅ **PW1 包骨架 + arena 收养 wazero memory**(`6fd9a1a`)
- ✅ **PW2 直线 opcode 翻译器 + trampoline 端到端 + VS0 值栈 arena 化**(`538e717`;见 §6 VS0 表)
- ✅ **PW3 算术 opcode**(`e33a1fd`;ADD/SUB/MUL/DIV/MOD/POW/UNM/NOT/LEN/CONCAT 快路径 f64 + 慢路径助手)
- ✅ **PW4 控制流 + relooper + 比较 opcode**(`c6102f0`;structurize.go relooper 结构化生成,FORPREP/FORLOOP + EQ/LT/LE/TEST/TESTSET;TFORLOOP 留 PW4b)
- ✅ **PW5 表 IC opcode**(`bb3f16f`/`1ae8fa1`/`e9814e7`/本提交;GETGLOBAL/SETGLOBAL/GETTABLE/SETTABLE/SELF inline IC 快照固化跳哈希,NEWTABLE/SETLIST 经助手;gen bump/换表失效降级助手 byte-equal,零 deopt)
- ✅ **PW6 CALL/TAILCALL 跨层互调**(`6546e45`/`5a86294`;h_call 三向分派(crescent/gibbous/host)复用 doCall/executeFrom,h_tailcall 复用 doTailCall proper TCO;**base 刷新解 growStack 段重定位 UAF**;错误经 status 链穿越 gibbous 帧冒泡 pcall byte-equal)
- ✅ **PW7 CLOSURE/CLOSE + PW4b TFORLOOP**(`6f2fd0e`/`5436e22`;CLOSURE/CLOSE 经助手复用 makeClosure/closeUpvals,emitOpcode skip 机制跳过 CLOSURE 后随伪指令;TFORLOOP 调迭代器复用 callLuaFromHost + base 刷新;VARARG 白名单拒)
- ✅ **PW8 线程级 tier 规则**(`d8fbf06`;运行期守卫 `th==st.mainTh`(PW6 起已完成)+ 升层门禁 onMain bool;协程内 hot 函数保持 TierInterp,主线程同 Proto 升层;RW-6/RW-7 回填)
- ✅ **PW9 端到端总验收**(`bb39b06`/`e94a80e`/`f556c19`;PW9-a gcPending inline 回边零跨层 + PW9-b force-all 层间差分套(V1-V13/V17/V18)+ 性能基准:loop 核 **2.58x** V14 达标。跨层调用税(call 核 0.14x)拆 PW10,§11 对账)
- 🔶 **PW10 消除 gibbous→gibbous 跨层调用税(进行中,§12/§13 对账)**:Phase 0 spike(`457559b`/`096da5b`/`01dfc0f`;`spike/p3indirect/` S-A/S-B/S-C 裁定 Arch-2 共享 funcref 表,决策报告 `spike/p3indirect/DECISION.md`,§0.2 存档)✅ + **R1** 共享 funcref 表基建(`4d063e8`/`adab492`/`8bbf510`;memadapter env holder 导出 TableSlots=8192 + 各 module active element 段自注册 `run`)✅ + **R2** 完整 CallInfo→linear-memory 迁移 = 长期延后的 VS0-e(`e7fc9b2`/`04ce9a8`/`cb7f625`/`10bcaa1`/`1a786ee`;4 word/帧,R2a→R2b-1~4「收口→只写影子→翻转→退役」+ R2b-3 UAF-风险隔离)✅ + **R3** gibbous→gibbous `call_indirect` 直调消 `code.Run` 重入(`6f2712e`/`4f1002a`/`2abdf18`;3 路 i64 sentinel + `ciTransferRef` 中转字 + `h_callerr`/`PopErrFrame` 弹孤立帧)+ **R3c-fix** 出错点就地标注使错误逐字节等纯解释器(`86e39c9`)+ **R3.5** host helper `WithFunc`→`WithGoFunction` 消反射装箱(`1bf9d53`;真正付清性能,四核全翻面)✅。剩 **R4** 消 `h_return` 单跨界 + **R5** re-bench ⏳(call 核仍 0.49x 是真实双跨界)
- ⏳ **P3 开工前置确认(待外部)**:向首个宿主确认「列内核是否跑在协程里」,决定线程级 tier 规则是否成立([07 §3.2](./07-coroutine-thread-rule.md))

---

## 0.1 PW0 spike 实测数据存档(承 [01-spike-gate §6](./01-spike-gate.md) 决策报告模板)

> **本节是 P3 是否启动的依据,永久存档不删**(承 §5 维护协议第 1 条)。spike 代码:`spike/p3boundary/`(独立 go module,不污染主库零依赖)。

**测量环境**:Intel Xeon 6982P-C / go1.26.2 / **wazero v1.12.0** / 编译模式(`NewRuntimeConfigCompiler`)/ `-benchtime=10s -count=5` 取中位数(噪声 < 1%)。

**三档 + 变体实测**:

| 样本 | ns/op | 检查 | 判定 |
|---|---|---|---|
| **S1 空往返** | 18.9 | < 80 | ✅ |
| **S2 带参 + memRW(`Call`)——主指标** | **36.7** | **< 150** | ✅ 远超 |
| S2 零分配(`CallWithStack` 复用栈) | **14.8** | < 150 | ✅✅ 极优 |
| S3 反向 imported(单次完整 `fn.Call`) | 201.7 | < 150 | ⚠️ 超 |
| S3-N 摊销(Wasm 内循环调 imported × 1000) | **~143/单次 dispatch** | < 150 | ⚠️ 边缘 |

**决策(01-spike-gate §5 决策树)**:**S2 主指标 36.7ns ≪ 150ns → 检查主路径通过,P3 开工放行(PW1 可启动)**。`CallWithStack` 零分配路径 14.8ns 更优,PW2 trampoline 入口应直接采用(类比 P1 issue #8 的 `CallInto`)。

**spike 揭示的两项重要认知修正**(直接影响 P3 设计文档,记入 §3.4):

1. **慢路径助手(gibbous → host imported)单次 dispatch ≈ 143ns,贴 150ns 边缘**——不是设计文档摊销模型假设的「同 S2 档 ~36ns」。摊销模型 [01-spike-gate §2 / 02-translation 摊销模型] 的 `k·T_cross` 中 **`T_cross`(慢路径)应取 ~143ns**。含义:列内核形式(k≈0,助手罕见)收益完整;但若热循环每迭代调一次 helper(k≥1),143ns 会显著吃收益——**这强化了 [02-translation §1] 「翻译单位覆盖整个热闭包、IC 快路径内联避免跨层」的动机**。

2. **wazero 生成码不被 Go 异步抢占**(与 [00-overview §8] / roadmap §2 原称「回边已有抢占检查点」**相悖**)——wazero RATIONALE.md「Why it's safe to execute runtime-generated machine codes against async Goroutine preemption」明确:生成码被 Go 运行时标为 async-preemption-**unsafe**,运行期间 Go 调度器无法抢占;wazero 靠 **context cancellation(`WithCloseOnContextDone`)** 协作式终止长循环。实测坐实:纯计算长循环阻塞 STW GC 达 144ms;`WithCloseOnContextDone` + 50ms context 超时精确终止(返回 `context deadline exceeded`)。**含义**:① gibbous 跑长循环时其他 goroutine 的 GC 会被阻塞到该循环经 helper / 函数返回让出——[05-safepoint-gc §1.3] 的回边 gcPending 检查恰好是让出点,**但纯计算无 helper 的死循环是已知边角**;② P3 长循环终止靠 `WithCloseOnContextDone` + context(P1 issue #4 已完成 context 取消,P3 复用),**不是异步抢占**——这是对 roadmap §2「异步抢占税 wazero 已验证」表述的精确化(税被解决的**机制**是 context 协作式终止而非回边抢占)。

**arena 收养可行性确认**(顺带项 A,[01-spike-gate §4.2] 三项验证):

| 验证点 | 结果 |
|---|---|
| `Memory.Read` 返回 write-through 零拷贝视图 | ✅ 确认——P3 arena.backing 可 unsafe 别名此视图作底层(03-memory-model §1 物理基础成立) |
| `memory.grow` 后旧视图失效需重取 | ✅ 坐实(旧视图 cap=64KiB,grow 后新视图 cap=128KiB,底层换了)——[03-memory-model §1.7] 「grow 后 Go 视图重取」的「待验证」项**有答案:必须重取** |
| 固定容量避免 grow 重取 | ✅ `WithMemoryCapacityFromMax(true)` + memory min<max(预分配 max)下,grow 不换 buffer,旧视图稳定——P3 可选「预分配 max 避免重取」策略 |

**四项税最小验证**(顺带项 B):

| 税 | 结果 |
|---|---|
| GC 精确栈扫描 | ✅ 1 万次 wasm 调用 + 中间 `runtime.GC()` × 2,memory 无损坏 |
| 异步抢占 | ⚠️ **机制修正**(见上「认知修正 2」)——wazero 不靠异步抢占,靠 context 协作终止;`WithCloseOnContextDone` 实测有效 |
| 栈移动 | ✅ 隐式覆盖(GC 测试期间 morestack 触发,无数据损坏) |
| 写屏障 | ✅ 设计层规避(值世界全在 linear memory,生成码不写 Go 堆指针,S2 形式已涵盖) |

**版本绑定提醒**:数据随 wazero **v1.12.0** 记录;未来 wazero 升级需重跑 spike(`spike/p3boundary/` 保留作回归)。

---

## 0.2 PW10 Phase 0 spike 决策报告存档(承 §0.1 PW0 spike 先例)

> **本节是 PW10 大修是否启动 + 架构选型的依据,永久存档不删**(承 §5 维护协议第 1 条、镜像 §0.1)。spike 代码:`spike/p3indirect/`(独立 go module,镜像 PW0 `spike/p3boundary`,不污染主库零依赖)。**完整三探针数据 + 决策树见 `spike/p3indirect/DECISION.md`**(本节只存指针与裁决摘要,不重复内容)。

**裁决摘要**(详 `spike/p3indirect/DECISION.md` + §12.1):

| 探针 | 实测 | 检查 | 裁决 |
|---|---|---|---|
| **S-A** intra-module `call_indirect` 单次 dispatch | **~2.5ns**(比 ~35ns 裸 host 跨界便宜 14x) | <30ns | ✅ 远超 ⇒ 值得做 |
| **S-B** rebuild-all 生命周期(整 module 重编 + 实例热交换 + 可重入升层) | **≈1.2ms@256fn**,可行且安全 | 可行即过 | ✅ Arch-1 可走但复杂 |
| **S-C** 跨 module 经**共享 imported funcref 表** `call_indirect`(各 module active element 段自注册 `run`) | **≈2.5ns 零跨 module 惩罚** | 可行即采 | ✅ 裁定 **Arch-2 共享表**(远简于 Arch-1,无 rebuild/代际/O(N²)) |

**关键决策**:S-A/S-B 证 Arch-1「rebuild-all」可行但带代际实例 / 跨代守卫 / O(N²) 重编复杂度;**补 S-C 探一个先前一笔带过的 architecture FORK**——各 Proto 模块能否共享同一张 imported funcref 表 ⟹ S-C 通过,**Arch-2「共享表」** 保持现有「一模块一 Proto」编译路径几乎不动,被选为 PW10 架构。**R1/R2 即按 Arch-2 完成**(R1 共享表基建 + R2 CallInfo→linear-memory 使 Wasm 侧可管帧),R3 接 `call_indirect` 直调。

**版本绑定提醒**:数据随 wazero **v1.12.0** 记录;未来 wazero 升级需重跑(`spike/p3indirect/` 保留作回归)。

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PW | 内容 | 文档 | 完成定义 | 状态 |
|---|---|---|---|---|
| PW0 | 开工前置 spike(wazero call boundary S1/S2/S3 实测) | [01](./01-spike-gate.md) | S2 < 150ns 且 S3 同档 ⇒ 开工;否则跳 P4 或边缘混合 | ✅ **通过**(S2=36.7ns;S3=202ns 走边缘修正,见 §0.1) |
| PW1 | `internal/gibbous/wasm` 包骨架 + arena 收养 wazero memory + 零 opcode 翻译器 | [02 §1](./02-translation.md) + [03 §1](./03-memory-model.md) | bridge 注入真 P3 后所有 Proto 仍走 crescent(F7 拦下);arena/wazero memory 共见验证 | ✅ **通过**(`6fd9a1a`;wazero v1.12.0 build-tag 隔离;三 build tag 全套零回归;收养 grow GCRef 不失效) |
| PW2 | 翻译器骨架 + 直线 opcode(MOVE/LOADK/LOADBOOL/LOADNIL/GETUPVAL/SETUPVAL/RETURN)+ trampoline 入口 + **值栈 arena 化(VS0)** | [02 §3.1](./02-translation.md) + [04 §2](./04-trampoline.md) + [03 §1](./03-memory-model.md) | 直线 Proto 升层后 byte-equal;crescent→gibbous trampoline 端到端 | ✅ **通过**(`538e717`;PW2-a~d 见 §6 VS0 表;值栈迁 arena 解锁端到端;`id(x)`/常量返回 e2e byte-equal + 升层确认) |
| PW3 | 算术 + 比较 opcode + NaN 规范化 + 慢路径助手回 Go | [02 §3.2-§3.3](./02-translation.md) + [04 §3](./04-trampoline.md) | 双 number 快路径直发 f64;混合类型走助手且 byte-equal | ✅ **全过线**(算术 `e33a1fd` + 比较 `c6102f0`;ADD/SUB/MUL/DIV/MOD/POW/UNM/NOT/LEN/CONCAT + EQ/LT/LE/TEST/TESTSET 双 number 快路径 f64 + NaN 规范化,混合类型走 h_arith/h_compare/h_eq 等 byte-equal)。比较 opcode 随 PW4 relooper 多 BB 解锁同批完成 |
| PW4 | 控制流(FORPREP/FORLOOP/TFORLOOP)+ 回边 safepoint | [02 §3.5](./02-translation.md) + [05 §1.3](./05-safepoint-gc.md) | 数值 for 编译后 ≥2x 解释器;回边 GC 触发 byte-equal | ✅ **全过线**(`c6102f0` relooper + FORPREP/FORLOOP + 比较;**TFORLOOP `5436e22`(PW4b)**:h_tforloop 调迭代器(复用 callLuaFromHost)+ i64 三态(newbase≥0 继续/-1 ERR/-2 退出)+ base 刷新)。e2e:sum-for/abs/max/nested-for/while + 自定义迭代器/深迭代 全 byte-equal。回边 safepoint gcPending 全局优化 PW9-a 收口(`bb39b06`,§11.1) |
| PW7 | CLOSURE/CLOSE/VARARG + 闭包/upvalue 编译协议 | [02 §3.7](./02-translation.md) | 闭包构造 + 开放/关闭 upvalue byte-equal | ✅ **过线**(`6f2fd0e`+`5436e22`;CLOSURE/CLOSE 经助手复用 makeClosure/closeUpvals;emitOpcode 加 skip 机制跳过 CLOSURE 后随 SubNUps 条伪指令;upvalue 难点已被 VS0-c 形式 Y 解掉;VARARG 白名单拒(F1 排除 vararg + SupportsAllOpcodes 防御)) |
| PW8 | 线程级 tier 规则 + 协程不升层 + **P2 04 considerPromotion 加线程上下文** | [07](./07-coroutine-thread-rule.md) | 协程内即便 hot + Compilable 也保持 TierInterp;主线程同 Proto 正常升层 | ✅ **过线**(本提交;运行期守卫 `th==st.mainTh`(call.go:60 PW6 起已完成)+ 升层门禁 onMain bool(OnEnter/OnBackEdge/considerPromotion 加参数,协程线程 profile 累加但不进升层决策)。bridge 收 bool 不感知 thread 类型,保持解耦。e2e:协程内 hot 函数十万次调用保持 TierInterp,同 Proto 主线程升层) |
| PW9 | 端到端验收 + 测试套(差分 + 强制全升 + GC 压力 fuzz + 性能 ≥2x) | [08](./08-testing-strategy.md) | **P3 总验收**:V1-V18 | ✅ **正确性 + V14 过线**(`bb39b06`/`e94a80e`/`f556c19`;§11 对账)。PW9-a gcPending inline 回边零跨层;PW9-b force-all 三方差分测试(oracle/crescent/gibbous byte-equal,V1-V13)+ 非空保证 + GC stress 层间(V5/V13)+ 并发 -race(V18)+ 四 build 零回归(V17);**性能 V14 loop 2.58x ≥2x 达标**(推翻空测)。**V15 geomean 0.79x 未达**——跨层调用税(call 核 0.14x)拆 PW10 |
| PW10 | 消除 gibbous→gibbous 跨层调用税(共享 funcref 表 + call_indirect 直调 + CallInfo→linear-memory + 零跨界 RETURN 拆帧 + ④ CALL 建帧 + 顶层升层) | [04 §9](./04-trampoline.md) 缺口 + [02 §1.2](./02-translation.md) | Phase 0 spike 检查(call_indirect <30ns + 增量 module 可行)→ call 核 ≥1x + geomean ≥1.5x | ✅ **收口(2026-06-16):已完成子里程碑 + 架构边界文档化(§12/§13/§14 详)**。Phase 0 spike(`457559b`/`096da5b`/`01dfc0f`;S-A call_indirect ~2.5ns ≪30ns / S-B rebuild-all 1.2ms@256fn / **S-C 裁定 Arch-2 共享 imported funcref 表**,§0.2 + `DECISION.md`)→ **R1** 共享 funcref 表基建(`4d063e8`/`adab492`/`8bbf510`)→ **R2** CallInfo→linear-memory = 延后的 VS0-e(`e7fc9b2`/`04ce9a8`/`cb7f625`/`10bcaa1`/`1a786ee`)→ **R3** gibbous→gibbous `call_indirect` 直调消 `code.Run` 重入(`6f2712e`/`4f1002a`/`2abdf18`)+ **R3c-fix** 出错点就地标注(`86e39c9`)+ **R3.5** host helper 零分配 API(`1bf9d53`)→ **零跨界 ① top mirror 字基建**(`a309a4f`)→ **基建-a closure slot 缓存**(`8aa4c02`,word1 高 16 位 [63:48] 存 slot+1,惰性填充 IC)→ **③a savedTop 基建**(`455d1bd`,caller prologue 快照,emitCall OK/done 两臂条件写回)→ **③b emitReturn 守卫快路径**(`1bff7d2`,5 守卫 + Wasm 内拆帧体 + helperReturn 兜底)→ **基建-b proto cache 段 + ④-i emitCall 守卫骨架 + fastCallHits mirror 字**(`8e820fd`+`bff1630`)→ **callOnStack 顶层升层 cl 直接走 enterGibbous + TopLevelUplift 探针**(`6bb9771`)→ 收口 HEAD `9ab0dba`。**④-ii fast body ⏳ 留 followup,架构边界**(预估上限 0.57x 仍 <1x,~200 行 wasm 字节级 codegen,ROI/UAF 不利,emit 原语 i64.add/i64.or 已保留)。本机 Xeon 6982P 2s×3 count 实测基线 2026-06-16:loop 2.95x(+10% over R3.5 2.67x)/ table 0.88x / call 0.52x / mixed 0.99x;call 0.52x 是 bench kernel 结构性架构边界(profile `/tmp/call.prof` 实证,详 §14.10)|

---

## 2. 跨文档回填请求收口表

P3 设计期各子文档对 P1/P2 现有文档发起的回填请求。**承用户裁决「本期只记录不主动改 P1/P2 现稿」** —— 全部标「⏳ P3 PWx 完成时同批补」,不在文档扩展轮兑现。

### 2.1 对 P1 文档的回填请求

| # | 来源 | 内容 | 兑现 PW |
|---|---|---|---|
| RW-1 | [04 §1](./04-trampoline.md) | P1 [05 §1.2](../p1-interpreter/05-interpreter-loop.md) CallInfo word2 bit50 `callStatus_gibbous` 语义登记(从「P1 恒 0 预留」升级为「P3 trampoline 写,P1 不读」)+ crescent callInfo struct 加 gibbous 标识字段 | ✅ **PW6 完成**(`538e717` VS0-b 加 `callInfo.gibbous bool` 形式 (b);enterGibbous/tailEnterGibbous 写 bit50,P1 主循环不读;P1 05 文档登记待 update 轮补) |
| RW-2 | [05 §2.4](./05-safepoint-gc.md) | P1 [06 §12](../p1-interpreter/06-memory-gc.md) 「层边界 safepoint 的具体形式」缺口标关闭(P3 §1 三类布点已收口) | ✅ **PW9-a 收口**(`bb39b06`;FORLOOP 回边从无条件 h_safepoint 改 inline gcPending 检查——collector 镜像「stressMode‖bytesAllocSince≥threshold」到 linear memory 固定字,gibbous i32.load 非 0 才跨层,热循环零跨层。stressMode 下恒 1 保 GC 压力语义不变。P1 06 §12 文档登记待 update 轮补) |
| RW-3 | [07 §5.2](./07-coroutine-thread-rule.md) | P1 [08](../p1-interpreter/08-coroutines.md) 增「gibbous 帧 = 不可穿越 yield 边界」节(P1 08 §6 已留前瞻引用,替换为正文) | 🔶 **PW8 登记**(本提交;线程级 tier 规则代码完成,「gibbous 帧不可穿越 yield」物理论证由几何隔离构造性消解——协程不进 gibbous + 主线程不能 yield。承用户裁决「本期只登记不主动改 P1 现稿」,P1 08 §6 前瞻引用替换为正文留文档轮) |
| RW-4 | [02 §4.2](./02-translation.md) | P1 [05](../p1-interpreter/05-interpreter-loop.md) `CallInfo.savedPC` 物化语义(pc 立即数经助手写回)登记 | ✅ **PW2-d 完成**(`538e717`;HostState.DoReturn/SetSavedPC 写 `ci.pc`,h_return 传 pc 立即数;P1 05 文档登记待 update 轮补) |
| RW-5 | [03 §8.1](./03-memory-model.md) | P1 [06 §1.1/§3](../p1-interpreter/06-memory-gc.md) `arena.Options.NewBacking` 注入点 P3 build 下的 wazero adapter 替换确认(P1 已留口,P3 验证) | ✅ **PW1 完成**(`6fd9a1a`;memadapter 注入 + VS0-c 值栈也走同一 backing) |

### 2.2 对 P2 文档的回填请求

| # | 来源 | 内容 | 兑现 PW |
|---|---|---|---|
| RW-6 | [07 §2.4 / §8.1](./07-coroutine-thread-rule.md) | P2 [04 §3](../p2-bridge/04-try-compile-fallback.md) considerPromotion 入口加线程上下文输入(`th *Thread`);协程内即便 hot + Compilable 也不升层 | ✅ **PW8 完成**(本提交;形式调整为 `onMain bool`(非 `th *Thread`)——bridge 不能 import crescent thread 类型,crescent 在调用点算 `th==st.mainTh` 传 bool;considerPromotion + considerPromotionWithAggregate 加 onMain 守卫。P2 04 文档登记待 update 轮补) |
| RW-7 | [07 §6.3](./07-coroutine-thread-rule.md) | P2 [01 §4](../p2-bridge/01-profiling.md) onBackEdge / onEnter 入口签名连带扩展(加 `th` 参数,与 considerPromotion 同步) | ✅ **PW8 完成**(本提交;OnEnter/OnBackEdge 加 `onMain bool`,crescent frame.go/execute.go 调用点透传 `th==st.mainTh`;profile 累加无条件(诊断),门禁只在升层决策入口。P2 01 文档登记待 update 轮补) |
| RW-8 | [04 §6.4 / §9](./04-trampoline.md) | P2 [04 §4.4](../p2-bridge/04-try-compile-fallback.md) installGibbous 增「multi-State 共享 Proto trampoline 注册幂等保证」 | ✅ **PW9-b 收口**(`e94a80e`;GibbousCode 经 bridge.gibbousCodes map 全局唯一 + compileMu 锁双重检查保幂等;`TestP3_ConcurrentForceAll` 8 goroutine 独立 State 并发 force-all-gibbous 跑同脚本 `-race` 干净 + 结果一致(V18 验收)。P2 04 文档登记待 update 轮补) |
| RW-9 | [01 §5.3 / §8.4](./01-spike-gate.md) | P2 [03](../p2-bridge/03-compilability-analysis.md) 加 `callDensity` 启发(边缘混合策略)**——仅在 spike 数据落边缘区(150±30ns)时触发**;主路径通过则无需 | PW0 后(仅边缘出路) |
| RW-10 | [08 §2.5 / §7.2](./08-testing-strategy.md) | P1 [12 §3.7](../p1-interpreter/12-testing-difftest.md) 差分生成器偏向「产生 Compilable Proto」(避开 F1-F7 排除形状,否则强制全升下差分退化为 crescent vs crescent) | ✅ **PW9-b 收口**(`e94a80e`;层间套核函数包成非 vararg 内层函数反复调(顶层 vararg chunk F1 排除不升层),`TestPW9_ForceAllPromoteReal` 经真实公共路径断言核函数真达 TierGibbous——锁死「退化为 crescent vs crescent」假阳性。差分生成器偏向留后续) |
| RW-11 | [08 §7.2](./08-testing-strategy.md) | P3 新增测试入口(SetForceAllPromote / wazero memory 健康检查 / gibbous panic 注入 / 跨层错误注入),复用 P2 [06 §11.1](../p2-bridge/06-testing-strategy.md) 的 internal-only 暴露纪律 | 🔶 **PW9-b 部分**(`e94a80e`;SetForceAllPromote 完成——bridge.forceAll + recheckCompilabilityForce 对真实后端重判 F7(绕编译期无 P3 注入的 F7 占位、不绕 F1-F6),crescent.State + wangshu.State 转发(testing-only)。wazero memory 健康检查 / panic 注入留后续) |

---

## 3. 设计期决策盘点(影响 × 不确定度)

按 [multi-doc-drafting guide](../../../llmdoc/guides/multi-doc-drafting.md) 「主动盘点不确定决策」纪律。

### 3.1 影响 PW 开工形式(高影响 / 高不确定度)

| 决策 | 定稿 | 出处 | 复核点 |
|---|---|---|---|
| **wazero call boundary < 150ns** | spike 检查;不达标跳 P4 | [01 §5](./01-spike-gate.md) | **PW0 实测,P3 生死攸关** |
| **协程是否跑列内核** | 线程级 tier 规则(协程不升层)成立的前提 | [07 §3.2](./07-coroutine-thread-rule.md) | **开工前向首个宿主确认**;若列内核真在协程里 → 退路线 A goroutine 化兜底 |
| 翻译单位 | 每 Proto 一 module(基线) | [02 §1](./02-translation.md) | PW1 实测实例化开销;批量 module 留优化 |
| 寄存器映射 | 全 memory-resident(基线) | [02 §2](./02-translation.md) | PW9 前 locals 缓存不启用 |
| arena 收养 wazero memory | P3 起替换 backing 来源 | [03 §1](./03-memory-model.md) | PW1 验证 NewBacking 注入点 + grow 跨边界一致 |

### 3.2 依赖外部数据 / wazero API(中影响 / 高不确定度)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| wazero memory 共享 API(import vs 读 module memory) | 倾向 import memory(基线 A) | [01 §4](./01-spike-gate.md) spike 顺带验证 |
| `memory.grow` 后 Go 视图稳定性 | 倾向「grow 后重取 slice」 | [03 §1.7](./03-memory-model.md) spike 验证 |
| `memory.grow` 并发约束 | 待定 | [03 §3](./03-memory-model.md) spike 验证 |
| IC 快照失效 → 重编译 | P3 不做(正确但慢) | 与 P4 deopt 基建统一评估([06 §6](./06-ic-feedback-consume.md)) |
| locals 缓存槽选择 | FORLOOP 三槽起步 | PW9 实测后定标([02 §2.2](./02-translation.md)) |

### 3.3 低风险已记录(低影响 / 已记缺口)

各子文档 §文档缺口节 + [00-overview §10](./00-overview.md) 风险汇总,约 20 余条次要缺口,均不阻塞 PW0 启动(PW0 spike 本身是唯一硬阻塞)。

### 3.4 PW0 spike 推翻/修正的设计文档表述(PW1 启动时同批修正)

spike 实测(§0.1)推翻了设计文档的两处表述,**PW1 启动时同批修正对应子文档**(本期只记录,承「先记录后修正」纪律):

| # | 设计文档原表述 | spike 实测修正 | 待修正落点 |
|---|---|---|---|
| SP-1 | [00-overview §8] / roadmap §2:「wazero 生成码循环回边已有抢占检查点」(异步抢占税 wazero 已验证) | wazero 生成码 **async-preemption-unsafe**,不被 Go 异步抢占;靠 `WithCloseOnContextDone` + context **协作式终止**(非回边抢占)。纯计算长循环阻塞 STW GC 144ms 实测坐实 | [00-overview §8] + [05-safepoint-gc §1.5] 把「异步抢占税解法」措辞从「回边抢占检查点」改为「context 协作终止 + 望舒 gcPending 回边让出」;长循环终止机制明确为 `WithCloseOnContextDone`(复用 P1 issue #4 context) |
| SP-2 | [01-spike-gate §2 / 02-translation 摊销模型]:`T_cross` 隐含同 S2 档(~36ns) | 慢路径助手(gibbous→host imported)单次 dispatch ≈ **143ns**,贴 150ns 边缘;`T_cross`(慢路径)≫ `T_cross`(入口) | [02-translation §1 / 06-ic-feedback-consume] 强化「翻译单位覆盖热闭包 + IC 快路径内联避免跨层」论证;摊销模型用 `T_cross_slow ≈ 143ns` 重算 k≥1 形式的收益边界 |

**正面确认**(spike 验证设计假设成立,无需修正):arena 收养 wazero memory 物理可行([03-memory-model §1] 成立)、grow 后重取协议([03-memory-model §1.7] 待验证项有答案)、GC 精确栈扫描/栈移动/写屏障三项税兑现。

---

## 4. P3 与 P1/P2 implementation-progress 的差异

| 维度 | P1/P2 implementation-progress | 本文(P3) |
|---|---|---|
| 当前状态 | 全卷已交付,持续维护后续轮次对账 | 设计阶段,实现未启动(PW0 检查待跑) |
| 表格主体 | 实际完成的 PR / 提交哈希 / 时间线 | 设计阶段决策对账 + 待实施回填请求 |
| 与设计文档的差异 | 已完成形式与设计文档的差异 | (无差异——尚未实施) |
| 核心阻塞 | 无(已交付) | **PW0 spike 检查 + 外部宿主确认** —— 两项均可能改变 P3 是否启动 / 如何启动 |
| 后续维护 | 每轮里程碑完成后追加对账行 | PW0 spike 跑出数据后,要么追加 PW 进度行,要么记录「跳 P4」决策 |

---

## 5. 后续维护协议

PW0 启动后,本文按以下协议更新:

1. **PW0 spike 数据进档**:三档(S1/S2/S3)实测 ns 值 + 决策(开工 / 跳 P4 / 边缘混合)永久记录在本文,无论结果如何——这是 P3 是否启动的依据,必须可追溯([01 §6 决策报告模板](./01-spike-gate.md));
2. 每个 PW 完成时,把对应行的 `⏳` 改 `✅`,加完成提交哈希;
3. 实际完成与设计文档有差异时,加「实现现状与设计文档差异对账表」;
4. 跨文档回填请求(§2)逐项实施,把对应行从「⏳ P3 PWx 完成时同批补」改「✅ 已完成」+ 提交哈希;
5. PW9 总验收过线后,本文头部状态改「P3 已交付」+ 验收数字汇总(性能 ≥2x over P1 + V1-V18 全过);
6. **若 PW0 spike 不达标**:本文记录「P3 跳过,直做 P4」决策 + spike 数据,P3 设计文档集转为「P4 继承的分层协议参考」(§2-§7 被 P4 继承,只换发射后端)。

---

## 6. VS0 值栈 arena 化(PW2-d 端到端前置,P1 迁移留口兑现)

> 背景:P1 [implementation-progress §对账表](../p1-interpreter/implementation-progress.md) 「值栈/CallInfo 位置」行留口——值栈 P1 期住 Go slice,物理搬迁是 P3 wazero memory 收养时的工作。PW2-d 真端到端要求 gibbous wasm(`i64.load offset=8*reg ($base)`)读写**真实值栈**,而值栈须住 wazero linear memory(= arena backing),故 VS0 是 PW2-d 硬前置。

**分阶段完成(每阶段 P1 全测试 + difftest 70 种子 byte-equal 独立验收门):**

| 阶段 | 内容 | 提交 |
|---|---|---|
| VS0-a | 栈寻址收口:~40 处 `th.stack[i]` 直接下标 → `thread.slot/setSlot/size/copyOut/copyIn/activeSlice` 集中 helper(纯重构零行为变更) | `4f58917` |
| VS0-b | callInfo 去 Go 指针:`proto *Proto` → `protoID uint32`(Go 指针不能进 linear memory,03 §5);`st.protoOf(ci)` 收口;execute 热循环维护 proto 局部 | `fafbafd` |
| VS0-c | 主线程 + 协程栈进 arena(统一经 `st.newThread()` 分配 arena 段);**形式 Y**(`slot/setSlot` 经 `arena.Words()` 偏移寻址,不缓存派生视图,免疫 arena grow);`growStack` 段内 relocate(WordAt 读旧段避免失效别名);upvalue 经 `owner.slot(idx)` 自动重定位 | `771afdc` |
| VS0-d | trampoline 接通(= PW2-d);见 §1 PW2 行 | `538e717` |
| VS0-e | 协程栈/varargs/CallInfo word 位打包全 arena 化(01 §5.6 完整布局)——**全量交付**:① 协程栈随 VS0-c 进 arena;② CallInfo 经 PW10 R2 物理迁入每线程 arena 段(4 word/帧 + word2 位打包);③ **varargs 进栈下区**(2026-06-16,VS0-e 子步 ①~④)——`enterLuaFrame` 重排 `base = funcIdx + 1 + nVarargs`,vararg 落 `stack[base-nVarargs..base)`(对齐官方 lvm.c OP_VARARG 布局,与 [05 §8.5](../p1-interpreter/05-interpreter-loop.md) 设计描述一致);ciWords 4→5 + word4 [15:0]=nVarargs 段镜像(wasm 端 `ciFrameBytes` 32→40 同步);退役 `callInfo.varargs` Go slice + `thread.ciVarargs` 影子 + `setVarargs/clearVarargs/varargsAt` 三 API(GC 扫栈 [0, top) 自然覆盖栈下区,无独立 ciVarargs scan)。`doVararg` 直接 `th.slot(base-nVarargs+k)` 现读 | `e7fc9b2`/`04ce9a8`/`cb7f625`/`10bcaa1`/`1a786ee`(R2a/R2b-1~4)+ `c22798b`/`4e50687`/`966318c`/`ed95020`(varargs 子步 ①~④)|

**关键设计决策(实现期定,补充设计文档):**

- **寻址形式 Y(否决 X 别名视图)**:值栈与对象世界**共享同一 arena backing**,任意对象分配触发 `arena.grow64` 会使 Go 端别名视图失效(P3 InPlaceBacking 下旧 buffer disconnect = UB)。execute reloadFrame 只重取 `code` 不重取栈视图,无法集中刷新别名 → 形式 X 有 `MOVE→NEWTABLE(grow)→MOVE` 的 UAF 雷区。形式 Y 每次经 `arena.Words()` 现算地址(`words` 字段由 setBacking 更新),彻底免疫。
- **base 字节偏移基准修正(对 [04 §2.2](./04-trampoline.md) 回填)**:设计稿 `baseBytes := int32(newBase)*8` 隐含主线程栈段起于 arena offset 0;实际各 thread 栈段经 AllocWords 分配,非零起始。**正确基准 = `(stackBaseW + ci.base) * 8`**(值栈段字偏移 + 帧 base 槽)。`stackBaseW` 是 thread 值栈段在 arena 的字偏移。
- **callInfo 粒度**:VS0-d 只做 proto→protoID + 加 `gibbous bool`(PW2-d 真实所需);word 位打包(01 §5.6 word2-5)与 cis 数组进 arena **延后到 VS0-e,已由 PW10 R2 交付**(§12.2:4 word/帧物理迁入每线程 arena 段)。`openUvs/uvOwner/pendingResume/protos` 仍是 Go 侧资产留 Go 堆(03 §5 划界);**varargs 也已进栈下区**(VS0-e 子步 ①~④,2026-06-16:ciWords 4→5 + word4 nVarargs 镜像 + enterLuaFrame base 重排 + doVararg 读栈下区 + 退役 ciVarargs/ci.varargs Go 影子,对齐官方 5.1 真栈布局 `[func | vararg | R(0)..]`)。

**实现现状与设计文档差异:**

| 维度 | 设计文档 | 实现现状(VS0-c/d) | 收口 |
|---|---|---|---|
| 单 BB 判定 | [02 §3.1](./02-translation.md) 「单 basic block」 | Lua codegen 在 RETURN 后追加兜底 RETURN 死代码,使其成不可达第 2 BB;改为数**可达** BB(`cfg.reachableBlocks` BFS),`translate`/`SupportsAllOpcodes` 只发射/扫描可达入口 BB | `538e717` |
| CallInfo bit50 | [04 §1.5](./04-trampoline.md) 形式 (a) 位打包 / (b) bool | 取 (b):`callInfo.gibbous bool`(与 tailcall/fresh 一样的) | `538e717` |
| 运行期可编译性重分析 | [analyze_on.go] 留「P3 注入后重跑 AnalyzeProto」 | **未实现**:compile 期无 P3 恒标 NotCompilable,运行期自动重分析需 AST 保留(已弃);PW2-d e2e 经 `SetCompilability` 手工模拟「真 P3 下 F7 放行」+ 驱动 OnEnter 触发真升层 | 留后续(AST 保留或 Proto 级重分析) |

---

## 7. PW5 表 IC 实现现状与设计文档差异

> 02 §3.4 给的是「全形式 inline」理想形式;PW5 实现按 byte-equal 可证性分级——
> inline 只覆盖能逐字节同构的形式,其余降级助手(正确性平凡兜底,06 §1 铁律)。

| 维度 | 设计文档(02 §3.4 / 06) | 实现现状(PW5) | 收口 |
|---|---|---|---|
| 控制结构 | §3.4.2 嵌套 if-then + `br $L_done` | GETTABLE/SETTABLE/SELF 用 `block $done{ block $slow{ guard;br_if 0;命中 br 1 } helper }`——**br 深度恒定 0/1**,避开深嵌套 br 计数脆弱 | `1ae8fa1` |
| 键匹配 inline | §3.4.2 `$ic_key_match`(array/node 全 inline) | inline 仅 ① 常量键(同表同 gen ⟹ 缓存 Index 仍映射同键,**省键匹配**)② 寄存器键 ArrayHit(`f64==Index+1`,避开 uint32 截断)。寄存器键 NodeHit(normKey/keyEqual/string-intern inline 脆弱)→ 助手 | `1ae8fa1` |
| MonoMeta(Kind=3) | §3.2.1 SNAP_KIND=3 mono-meta 直达 | PW5 基线路由助手(`__index` 元方法直达 inline 复杂,留后续) | helper 兜底 |
| 表字段 inline 寻址 | §3.4.1 `$gcref_low32`/`$table_gen`/`$ic_slot_load` 抽象助手 | **全 inline Wasm load**(非 imported 助手——跨层 ~143ns 会废掉跳哈希收益):gen=`i64.load[t+40]>>32`,nodeRef=`[t+24]`,array=`[t+16]`,槽 offset 编译期立即数 | `bb3f16f` |
| SETTABLE 值烧不出 | (未提) | RK(C) 字符串常量编译期烧不出 GCRef → 整条降级 h_settable(助手经 rk() 正确取) | `1ae8fa1` |
| SETLIST C=0 / B=0 | §3.4.7 助手内读下一指令为批次号 | C=0(下一字是数据非 opcode,线性发射器会误译)/ B=0(填到 top,gibbous 帧 top 维护 PW7 前未接)→ SupportsAllOpcodes 拒 | 留 PW6/PW7 |
| globals 立即数 | §3.4.4 编译期烧 globals GCRef | `HostState.GlobalsRaw()` 取 `MakeGC(TagTable, st.globals)`(单 State 私有,身份恒定不移动) | `bb3f16f` |
| feedback 消费 | 06 §4.4 fb 决定路径 + ICSlot 填立即数 | PW5 基线**只读 ICSlot**(snapshotICSlot,atomic race-tolerant);fb 双源选取留后续(06 §6.2;PW9 未涉及) | `bb3f16f` |

**RW 回填**:RW 表无 PW5 专项条目(IC feedback 消费是 06 自包含设计,不回填 P1/P2)。
P1 [05 §6](../p1-interpreter/05-interpreter-loop.md) IC 机制本文 inline 与之逐字节同构,差分门(difftest 70 种子)保证。

---

## 8. PW6 CALL/TAILCALL 实现现状与设计文档差异

> 04-trampoline 设计稿假设值栈不重定位、跨层只传 base i32;PW6 实现发现望舒特有的
> **值栈段 arena 重定位**(growStack)使陈旧 base 失效,这是设计稿未覆盖的正确性隐患
> (VS0 形式 Y 雷区在 gibbous 帧的对偶)。

| 维度 | 设计文档(04-trampoline) | 实现现状(PW6) | 收口 |
|---|---|---|---|
| h_call 返回值 | §3.1 返回 status i32(0/1) | **返回 i64 新 base / 负哨兵**——被调帧深递归 growStack 使值栈段在 arena 重定位(stackBaseW 变),本帧陈旧 `$base` 指向已 Free 旧段 = UAF。h_call 返回时按当前 stackBaseW+ci.base 重算新 base,gibbous `local.set $base` 刷新。`$base` 是可写 wasm param,函数中途无法自刷新故必须助手返回时给 | `6546e45` |
| 三向分派实现 | §3.2 hCall 内 switch 被调者类型 | **复用 doCall 统一分派**(已含 host/__call/gibbous 升层/普通 Lua 四向);普通 Lua closure 用 executeFrom 同步驱动到完成(callLuaFromHost 一样的 nCcalls 守卫,非 copyOut——结果留共见栈槽) | `6546e45` |
| TAILCALL 复用帧 | §2.5 改写当前 CallInfo + 再 fn.Call | **复用 doTailCall + executeFrom**:Lua 尾调用链在解释器内 O(1) 栈迭代(proper TCO);status 三态 0=完成/1=ERR/2=host(落尾随 RETURN)。gibbous→gibbous 尾调用降级在解释器跑(byte-equal,gibbous 仅是优化) | `5a86294` |
| 单 BB TAILCALL | (未区分) | TAILCALL 后仅死代码 RETURN 时 reachableBlocks==1 → 单 BB 直线路径,emitOpcode 须含 TAILCALL 分支(否则落 default error)——PW6-c 的 tail-shaped 测试暴露 | 本提交 |
| 多值窗口 | §4.7 B=0/C=0 经 h_return moveResults | CALL B=0/C=0、TAILCALL B=0(参数/返回到 top)依赖 th.top 跨 opcode 维护 → SupportsAllOpcodes 拒(定参定返放行) | 留后续 |
| 错误冒泡 | §4 status 链单向冒泡 | 复用 enterGibbous throwPending + pcall callLuaFromHost ciTop 回退;gibbous 帧 CallInfo 形式同 crescent,pcall 透明丢弃。错误消息 byte-equal(pc 物化每 helper 入口写 ci.pc) | `6546e45`/本提交 |

**RW 回填**:RW-1(bit50 语义,callInfo.gibbous bool 形式 (b) `538e717` 已完成)、
RW-8(multi-State trampoline 幂等,GibbousCode 全局唯一天然成立)——见 §2.1 表。

---

## 9. PW7 + PW4b 实现现状与设计文档差异

> PW7 设计标注 0.5-1 人月(02 §3.7 担心「开放/关闭 upvalue 在 linear memory 形式的存储
> 协议」),实际比 PW5/PW6 小——该难点已被 VS0-c 解掉。唯一非平凡点是 CLOSURE 伪指令翻译跳过。

| 维度 | 设计文档(02 §3.7 / §3.5.3) | 实现现状(PW7 / PW4b) | 收口 |
|---|---|---|---|
| upvalue 编译协议 | §3.7.1 留「开放/关闭 upvalue 在 wazero memory 形式的存储待定」 | **难点已被 VS0-c 解掉**:值栈住 arena,开放 upvalue 经 `owner.slot(idx)` 形式 Y 寻址(stackBaseW 更新自动重定位);CLOSURE/CLOSE 全经助手复用 makeClosure/closeUpvals,无新 upvalue 物理协议 | `6f2fd0e` |
| CLOSURE 伪指令跳过 | §3.7.1 「翻译器跳过后随 nupvals 条伪指令」 | emitOpcode 改返回 `(skip,err)`,CLOSURE 返回 `SubNUps[Bx]`(父 Proto 自带子 upvalue 数);两处发射循环(单 BB 直线 + emitBlockBody 前缀)`pc += 1 + skip`。机械重构先验零回归再加 opcode | `6f2fd0e` |
| VARARG | §3.7.3 双重保险(白名单拒 + Wasm unreachable 防御) | 白名单不含 VARARG → SupportsAllOpcodes 返 false fallback,**无需任何 VARARG emit**;F1 也排除 vararg 函数。补 TestPW7_VarargRejected 坐实 | `6f2fd0e` |
| TFORLOOP base 刷新 | §3.5.3 助手三态 status(0/1/3) | 迭代器调用经 callLuaFromHost 可能 growStack 段重定位 → **改 i64 三态**(newbase≥0 继续 / -1 ERR / -2 退出),emitTForLoopTerm 解三态 + 刷新 base(同 PW6 h_call,见 [[design-claims-vs-codebase-physics]] §2)。复用 compareSuccs(succExec=回边/succSkip=退出) | `5436e22` |
| TFORLOOP 迭代器调用 | §3.5.3 「迭代器若 gibbous 编译过助手内再 trampoline」 | 复用 callLuaFromHost(host→Lua 重入),迭代器升 gibbous 则其内部 doCall 再 enterGibbous——**自动复用 PW6 跨层机制**,TForLoop 无特殊处理 | `5436e22` |

**RW 回填**:RW-3(P1 08 gibbous 帧不可穿越 yield)留 PW8(线程级 tier 规则)。
upvalue/闭包 byte-equal 由差分门(difftest 70 种子)+ e2e(捕获栈局部/父 upvalue/嵌套)保证。

---

## 10. PW8 实现现状与设计文档差异

> 07 §2.2 的运行期守卫(`th==mainThread`)在 PW6 跨层互调时已完成(call.go:60);PW8 只补
> 升层门禁这一半。设计稿 07 §2.4 写 `considerPromotion(proto, pd, th *Thread)`,实现因 bridge
> 不能 import crescent thread 类型而调整为 `onMain bool`。

| 维度 | 设计文档(07) | 实现现状(PW8) | 收口 |
|---|---|---|---|
| 运行期守卫 | §2.2 doCall gibbous 分支查 `th==mainThread` | **PW6 已完成**(call.go:60 `profileEnabled && th==st.mainTh`)——协程线程上即便 Proto 已升 gibbous 也走 crescent | `6546e45`(PW6) |
| 升层门禁签名 | §2.4 `considerPromotion(proto, pd, th *Thread)` | **改 `onMain bool`**——bridge 是 crescent 的依赖方,反向 import `*thread` 成环;crescent 在 OnEnter/OnBackEdge 调用点算 `th==st.mainTh` 传 bool,bridge 收 bool 保持解耦(语义等价 `th!=mainThread` 守卫) | 本提交 |
| profile 累加 | §2.4 选 (A):协程线程也累加,只门禁决策 | OnEnter/OnBackEdge 无条件累加 EntryCount/BackEdge(诊断价值),onMain 守卫只加在「越阈值 → considerPromotion」之间 | 本提交 |
| considerPromotion 两版本 | §2.4 单入口 | considerPromotion + considerPromotionWithAggregate(P2+ #3 双表混合)两入口都加 onMain 守卫 | 本提交 |
| 三层互锁自洽 | §2.3 主线程不能 yield + 协程不进 gibbous ⟹ yield 撞 gibbous 帧概率 0 | 构造性消解,非运行期检测;e2e 坐实协程 hot 不升层 + 主线程同 Proto 升层 | 本提交 |
| 状态机 | §5.1 不引入新 TierState | 无变更——onMain 只在升层判定入口加门禁,不动状态机转移 | 本提交 |

**RW 回填**:RW-6(considerPromotion 加线程上下文,onMain bool 形式)、RW-7(onBackEdge/onEnter
连带扩展)✅ 已完成(见 §2.2 表);RW-3(P1 08 正文)登记留文档轮。

---

## 11. PW9 端到端总验收实现现状与设计文档差异

> 08-testing-strategy 设 V1-V18 三轴硬门(正确性 V1-V13 / 性能 V14-V16 / 工程 V17-V18)。
> PW9 交付**正确性轴 + 性能 V14(loop ≥2x)+ 工程轴全过**;性能 V15(geomean ≥1.5x)
> 未达标——跨层调用税拆 PW10。三提交 `bb39b06`(PW9-a gcPending)/`e94a80e`(PW9-b force-all
> 层间套)/`f556c19`(性能基准)。

### 11.1 PW9-a gcPending inline safepoint(回边零跨层,RW-2 收口)

| 维度 | 设计文档(05 §3 / 02 §3.5.2) | 实现现状(PW9-a) | 收口 |
|---|---|---|---|
| 回边 safepoint | §3「gcPending 全局优化」:回边 inline 检查标志,非 0 才跨层 | collector 维护 gcPending 标志,**transition-only 写**(免每 alloc store)镜像「stressMode‖bytesAllocSince≥threshold」到 arena 一个固定字(linear memory);State init 分配标志字 + SetGCPendingRef 装入;AllocCharge/Collect/SetStressMode 三个状态转移点 updateGCPending | `bb39b06` |
| gibbous 侧发射 | §3.5.2 `if i32.load(gcPending) then h_safepoint` | emitGCPendingSafepoint:`i32.const 0; i32.load offset=GCPendingAddr; if { localGet base; i32.const pc; call h_safepoint }`——热循环无 GC due 时分支恒不跳,零跨层 | `bb39b06` |
| 正确性 | 跳过仅「不必要的跨层」,GC 该触发仍触发 | flag 保守覆盖 GC due 状态,该触发时必为 1;跳过仅在「无分配 due」(h_safepoint MaybeCollect 本就 no-op,等价)。stressMode 下恒 1 → GC 压力测试每迭代仍跨层,V5/V13 语义不变 | `bb39b06` |

### 11.2 PW9-b force-all 强制全升 + 层间差分套(V1-V13/V17/V18)

| 维度 | 设计文档(08 §2.2 / §2.3.1) | 实现现状(PW9-b) | 收口 |
|---|---|---|---|
| 强制全升入口 | §2.2 SetForceAllPromote 绕热度阈值 | bridge.forceAll 标志;OnEnter/OnBackEdge 首次调用即 considerPromotion(绕 HotEntry/HotBackEdge 阈值);crescent.State + wangshu.State 转发(testing-only) | `e94a80e` |
| **不绕可编译性检查** | §2.3.1 F1-F7 排除形状即便 force-all 也走 crescent | **关键差异**:编译期 analyzeCompilability 用临时 Bridge 跑 F7,无 P3 注入恒标 CompNotCompilable(analyze_on.go「P3 注入后重跑 F7」是历史留口)。recheckCompilabilityForce 在 forceAll 下对**真实后端**重判 F7——清编译期 F7 占位(ReasonBackendUnsupp),保留 F1-F6 结构性排除(已烧进 proto.CompReasons,不依赖 AST)。`TestPW9_ForceAllRespectsStructuralGates` 坐实 vararg 即便 force-all 也不升层 | `e94a80e` |
| 非空保证 | (设计未明写;层间差分隐患) | **新增**:`TestPW9_ForceAllPromoteReal` 经真实公共路径(SetForceAllPromote + 反复调)断言核函数真达 TierGibbous——锁死「force-all 没升任何 Proto → crescent==gibbous 退化为 crescent==crescent」假阳性。RW-10 收口 | `e94a80e` |
| 层间差分套 | §V1-V13 各形状 byte-equal | `test/difftest/p3_test.go` 三方差分测试(oracle/crescent/gibbous 全 byte-equal):V1-V13 各形状 23 核 + 71 种子层间套 + GC stress 层间(V5/V13)+ 并发 force-all(V18 -race)。核函数包成非 vararg 内层函数反复调(顶层 vararg chunk F1 不升,首调跑 crescent、二调起 gibbous) | `e94a80e` |
| 工程轴 | V17 四 build / V18 -race | 四 build(default/profile/p3/p3+profile)零回归 + 8 goroutine 并发 force-all `-race` 干净(gibbousCodes map 经 compileMu 守护,RW-8 收口) | `e94a80e` |

**P4 force-all-jit 一样的纪律**(2026-06-28,承 [../p4-method-jit/implementation-progress §2 RJ-24](../p4-method-jit/implementation-progress.md) 跨文档回填请求):本节 force-all 非空保证援引 RW-10 教训,P4 force-all-jit 接续一样的纪律——详见 [../p4-method-jit/08-testing-strategy §3.7 force-all 非空保证](../p4-method-jit/08-testing-strategy.md) + [../p4-method-jit/implementation-progress §12 V12 状态行](../p4-method-jit/implementation-progress.md)。P4 amd64 端已实证:
- TestP4_PromotionTriggered 强断言(承本会话累计 58 difftest p4Corpus 用例)
- TestP4_ConcurrentForceAll(8 goroutine 并发 force-all P4,无 race)
- 26 e2e + 11 difftest 全 SetForceAllPromote(true) 入口 + V18 -race 含 SELF 测试

### 11.3 性能轴实测对账(V14 达标 / V15 未达,拆 PW10)

> **方法论修正**:PW9 早期一次测量给出 gibbous≈crescent(≈1.0x),据此曾判「memory-resident
> 下 dispatch 消除不足 2x、需 locals 缓存」。该测量是**空测**——`for` 循环写在 vararg 顶层
> chunk(F1 排除,永不升层),实测的是 crescent vs crescent。经**已验证非空**的 force-all 路径
> (核包成非 vararg 内层函数反复调,`TestPW9_ForceAllPromoteReal` 断言真达 TierGibbous)重测,
> 数据完全不同(`test/difftest/p3_bench_test.go`,Xeon 6982P-C):

| 核形式 | crescent | gibbous | 比值 | 判定 |
|---|---|---|---|---|
| loop(纯算术 for) | 5.61ms | 2.17ms | **2.58x** | ✅ V14 ≥2x 达标 |
| mixed(算术+表+分支,少调用) | 173.7ms | 108.7ms | **1.60x** | ✅ |
| table(空表增长 SETTABLE) | 75.2ms | 111.2ms | 0.68x | 退化 |
| call(小叶函数高频调) | 16.5ms | 117.4ms | **0.14x** | 退化(慢 7x) |
| **geomean(四核)** | | | **0.79x** | ❌ V15 ≥1.5x 未达 |

**归因**(investigator `a20ad713` + design-claims-vs-codebase-physics §1 边界成本预算):
- **V14 loop ≥2x 真实达标**——消灭 dispatch 的收益在计算密集核(k≈0 跨层)完整兑现,印证 02 §1 翻译单位覆盖热闭包的设计。
- **退化根因是跨层调用税,非 memory-resident**:gibbous→gibbous 经 `h_call` 双跨层(Wasm→Go→Wasm,PW0 实测 ~143ns/次)。call 核 `mid` 每次调 `inner` 两次 × 10 万循环 = 30 万+ 次跨层 ≈ 43ms 纯边界开销,把便宜的小叶调用变成昂贵往返;table 核同理(空表增长 SETTABLE IC-miss 每元素跨层 h_settable)。这是 [[design-premises]] 前提一/前提二边界成本物理的实证(08 §5.1.2 摊销模型 `k·T_cross` 警告的形式)。
- **locals 缓存治不了这个**——它消的是 memory-resident 寄存器访问,与跨层调用税正交。原 PW10「locals 缓存」方向作废,改为**消除跨层调用税**。

**PW10 立项**:单 module + `call_indirect` 直调(gibbous→gibbous 免 Go 往返,04 §9 缺口 + 02 §1.2 批量 module)+ 轻量 CallInfo 进 linear memory(VS0-e 子集,使 Wasm 侧可管帧)。investigator 确认为里程碑级架构改,**生死未知数**是 wazero 能否增量编译 module(向已实例化 module 加函数);故 **PW10 Phase 0 是 spike 检查**(对齐 PW0 先例):S-A 测 intra-module call_indirect 单次成本(目标 <30ns ≪ 143ns)、S-B 测增量重编 + 实例热交换生命周期。绿则重写,红则回退到「升层启发式拒小叶函数」(被拒者跑 crescent=1.0x,消退化)。
> **后续(§0.2 / §12 已对账)**:Phase 0 spike 绿灯,但 S-C 探出比「单 module / rebuild-all」更简的 **Arch-2「共享 imported funcref 表」**——本段「单 module」是 PW9 立项时的初步设想,实际架构以 §12.1 裁决为准;PW10 R1(共享表基建)+ R2(CallInfo→linear-memory = VS0-e)已交付,R3 `call_indirect` 直调待实现。

**RW 回填**:RW-2(gcPending 收口)、RW-8(并发 force-all -race 验收)、RW-10(非空保证)✅ 已完成(见 §2 表);RW-11(force-all 入口)🔶 部分(wazero memory 健康检查 / panic 注入留后续)。**P3 正确性轴交付 + 性能 V14 达标;V15 跨层调用税拆 PW10。**

---

## 12. PW10 R1-R2 实现对账(消除跨层调用税进行中)

> PW10 要消除 §11.3 暴露的 gibbous→gibbous 跨层调用税。**本节对账其第一轮交付:Phase 0(spike 检查)+ R1(共享 funcref 表基建)+ R2(完整 CallInfo→linear-memory 迁移 = 长期延后的 VS0-e)。R3(`call_indirect` 直调)+ R3.5(host helper 零分配)在第二轮交付,对账见 §13;R4 + R5(re-bench)仍待实现——PW10 仍在飞行中。** 本轮 13 提交 `457559b..9d70247`。承 [04-trampoline](./04-trampoline.md) 跨层机制 + [02-translation](./02-translation.md) VS0-e + 反思 `2026-06-15-p3-pw10-r1-r2-callinfo-migration-round`。

### 12.1 Phase 0 spike 裁定 Arch-2「共享 imported funcref 表」(非 rebuild-all)

> 完整数据 + 决策树见 §0.2 + `spike/p3indirect/DECISION.md`(永久存档)。

PW10 真解(gibbous→gibbous 直接分派、不经 host)被两条码库 physics 挡住:(a) 每个 Proto 编译进**自己独立的 wazero module** ⟹ 跨 module 调用**必须**穿 host;(b) Lua 调用帧活在 Go(`th.cis` 切片)。**生死未知数**:wazero 能否增量提升 module。`spike/p3indirect/`(独立 module 镜像 PW0 `spike/p3boundary`)三探针:

- **S-A**(值不值得做):intra-module `call_indirect` ≈**2.5ns**(比 ~35ns 裸 host 跨界便宜 **14x**,远在 <30ns 目标下);
- **S-B**(能不能做 Arch-1):rebuild-all 生命周期可行——整 module 重编 + 实例热交换,≈1.2ms@256fn 可重入升层安全;**但复杂**(代际实例 / 跨代分派守卫 / O(N²) 重编);
- **S-C**(更简分支):**关键决策——没止步于「S-A/S-B 证 Arch-1 可行」**,补 S-C 探一个先前一笔带过的 architecture FORK:各 Proto 模块能否**共享同一张 imported funcref 表**(每个模块经 active element 段把自己的 `run` 自注册进表,gibbous→gibbous = 经共享表 `call_indirect`,**无需 rebuild**)⟹ **S-C 通过**(跨 module 经共享表 `call_indirect` ≈2.5ns 零跨 module 惩罚)。

**裁定 Arch-2「共享表」**:戏剧性地简于 Arch-1——保持现有「一模块一 Proto」编译路径几乎不动,**无 rebuild、无代际实例、无 O(N²)**。R1/R2 即按 Arch-2 完成。

### 12.2 R1 共享 funcref 表基建 + R2 CallInfo→linear-memory 迁移(= VS0-e 交付)

| 轮 | 内容 | 提交 |
|---|---|---|
| **R1** | memadapter env holder 导出**共享 funcref 表**(`TableSlots=8192`);各 gibbous module **import** 它 + 经 active element 段把自己的 `run` 自注册进 Compiler 分配的单调槽(`Compiler.slotOf`/`SlotOf`)。**零行为变更**——尚无 `call_indirect`(R3 才接线),表只是建好待用 | `4d063e8`/`adab492`/`8bbf510` |
| **R2(VS0-e)** | CallInfo 从 Go `[]callInfo` 物理迁入**每线程 arena 段**(**4 word/帧**,layout 承 [04 §1.2](./04-trampoline.md) word2 位打包)。经「**收口→只写影子→翻转→退役**」剧本(R2a/R2b-1~4)完成 | `e7fc9b2`/`04ce9a8`/`cb7f625`/`10bcaa1`/`1a786ee` |

**R2「收口→只写影子→翻转→退役」分步**(高风险物理数据结构迁移剧本,承 VS0-a/b/c/d house style,把唯一 UAF-风险翻转单独隔离一提交):

| 步 | 内容 | 安全性 |
|---|---|---|
| **R2a** | 所有 **COLD** 字段访问收口走 accessor(`base`/`pc` 仍热/直访)——纯收口 | 行为不变 |
| **R2b-1** | 并行 arena 段作**只写影子** + `wangshu_trace` 下 `verifyCISeg` 往返自检(写进去立即读回、断言段与 Go 权威一致,贯穿整套 difftest 层间套)——段**从不被读** | 行为可证不变(只写) |
| **R2b-2** | `growCISeg` 动态增长使段盖满全深度,**仍只写** | 行为不变 |
| **R2b-3** | GC 根扫描**翻转**为从段 READ `cl`(closure)——**唯一最高 UAF-风险步,单独隔离一提交**(夹在 R2b-2 只写安全与 R2b-4 退役之间,故障必然归因到这一步) | ⚠️ 翻转点 |
| **R2b-4** | 退役 Go slice;`currentCI` 返回稳定 `th.cur` scratch 镜像(**地址恒定的固定 struct 字段,永不重定位**);非当前帧经 `ciAt(depth)` 值快照(form-Y 每次从 depth 重算);`truncateCI` 解栈;varargs 留 Go 影子 `th.ciVarargs` | 退役 |

**`th.cur` 稳定地址消解重定位雷区**:旧 `currentCI` 返回 `&th.cis[len-1]`(指进 Go slice,append 搬家 = [[design-claims-vs-codebase-physics]] §2 雷区对偶——那里 arena grow 搬走 `$base`,这里 slice append 搬走 `&th.cis[i]`)。`th.cur` 把当前帧放进地址恒定的固定 struct 字段,雷区被**构造性消除**而非靠纪律绕——是 §2 从「翻转后回传刷新值」(PW6 `$base`/PW7 TFORLOOP 纪律档)到「稳定地址 + 值快照」(结构档)的升级应对。

### 12.3 R1-R2 轮基准快照(R3 未做时,性能收益尚未兑现 —— 已被 §13.3 R3.5 后数据取代)

> 4 个 benchmark 文件均加 gibbous(凸月)列(build-tag 门控 `wangshu_p3 wangshu_profile`)+ 新 `make bench-gibbous`/`make bench-all`。`wangshu_profile` 给 crescent 加 ~28% 采样税,故 bench-all 让 **crescent/gopher 跑默认 tag、gibbous 跑 p3+profile 分开**(统一 tag 会扭曲 crescent 基线)。**注意**:下表是 R1-R2 轮(R3 未做)的历史快照,R3.5 后的最新四核数据见 §13.3。

**R3-未做基线**:gibbous 在**计算/循环密集**核已反超解释器,但**调用密集**核仍亏——因 gibbous→gibbous 仍付 `h_call` 双跨层税(R3 将以 `call_indirect` 直调消除):

| 核形式 | 比值(gibbous/crescent) | 判定 |
|---|---|---|
| loop(循环密集) | **2.45x** | ✅ 已反超 |
| arith-in-loop(算术放循环里) | **1.88x** | ✅ 已反超 |
| spectralnorm(调用密集) | **~0.21x** | 退化——gibbous→gibbous `h_call` 双跨层主导,**正是 R3 要修的** |

> 加 gibbous 列时跳出 baseline loop「慢 20 倍」/ arith「慢 5 倍」均**非真回归**,是基准工作负载错配(apples-to-oranges:wrapped×50 vs bare / call-entry vs in-loop)——加匹配的 `_WangshuKernel` 列 + 隔离探针后数字自洽(详反思教训 5)。

**R3-R5 后续**:R3 接 `call_indirect` 直接分派 + R3.5 host helper 零分配已交付,四核全翻面(§13.3);剩 R4 → R5 re-bench(目标 call 核 ≥1x + geomean ≥1.5x)。

---

## 13. PW10 R3 + R3.5 实现对账(call_indirect 直调 + host helper 零分配)

> 承 §12(R1-R2)。**本轮交付 R3(gibbous→gibbous `call_indirect` 直调,消 `code.Run` 重入)+ R3c-fix(出错点就地标注,错误逐字节等纯解释器)+ R3.5(host helper 改 wazero 零分配 API,消反射装箱——真正付清性能的步骤)。R4(消 `h_return`)+ R5(re-bench)仍待实现——PW10 仍在飞行中。** 6 提交 `6f2712e..1bf9d53`(中间夹 `836a3c9` untrack 瞬态锁文件):R3a(`6f2712e`)→ R3b-1(`4f1002a`)→ R3c(`2abdf18`)→ R3c-fix(`86e39c9`)→ R3.5(`1bf9d53`)。承 [04-trampoline](./04-trampoline.md) 跨层机制 + §12 R1 共享表/R2 CallInfo→linear-memory 两块基建 + 反思 `2026-06-15-p3-pw10-r3-call-indirect-round`。

### 13.1 R3 gibbous→gibbous `call_indirect` 直调(消 `code.Run` 重入)

> 旧路径:每次 gibbous→gibbous CALL 经 `h_call` host 回调做一次完整 `code.Run` **重入**(Wasm→Go→Wasm)。新路径:经 R1 共享 funcref 表 + R2 linear-memory CallInfo 的 `call_indirect` **直接分派**,callee 不再经 Go 重入。

| 维度 | 旧实现(PW6) | R3 实现 | 收口 |
|---|---|---|---|
| `DoCall`(h_call)返回值 | i64 新 base / 负哨兵(PW6 §8) | **3 路 i64 sentinel**:`-1`=错误 / **偶 ≥0** = "已完成,值为刷新后的 caller base"(host/crescent/`__call`/无槽 fallback,同步跑完如旧)/ **奇 `(slot<<1)|1`** = "indirect"(callee 是 gibbous-with-slot:DoCall 做帧 push + 置 gibbous bit + 把 callee base 写进 `ciTransferRef` 中转字,但**不**跑 callee) | `4f1002a`/`2abdf18` |
| Wasm 侧 `emitCall` | 发 `call h_call` | 解 sentinel:奇数 case 发 `call_indirect <typeEntry> table0`(经 R1 共享表直调 callee 的 `run`),偶数 case 走原同步刷新 base 路径 | `2abdf18` |
| 槽号暴露 | (无) | `bridge.GibbousCode.Slot()` 暴露每 module 的共享表槽(供 DoCall 编出 `(slot<<1)|1`);`HostState.CITransferAddr()` 暴露中转字 linear-memory 地址(镜像既有 `gcPendingRef`/`GCPendingAddr` 模式) | `6f2712e` |
| 失败 call_indirect 的孤立帧 | (旧路径无 indirect) | 新 `PopErrFrame` host shim(`h_callerr`)弹掉失败 call_indirect 留下的孤立 gibbous callee 帧——复刻 baseline `enterGibbous` 的逐层错误弹帧 | `4f1002a`/`2abdf18` |
| 成功 indirect 后 base 刷新 | h_call 返回值直接给 | `DoReturn` 弹帧后把刷新的 caller base 写进 `ciTransferRef` 中转字(LIFO-safe:深度回溯各帧依序写读) | `4f1002a` |

**机制要点**:`ciTransferRef` 中转字是 arena 一个固定 word(地址恒定,镜像 gcPending 模式),承载「indirect 调用前 callee base 入 / 调用返回后刷新的 caller base 出」两个方向的单值传递。`DoCall` 在 indirect case **只做帧 push 不跑 callee**——跑 callee 由 Wasm 侧 `call_indirect` 完成,这才是消掉 `code.Run` 重入的关键(callee 直接在当前 Wasm 栈上跑,不再下一层 Go)。

### 13.2 R3c-fix 出错点锚定错误行号(gibbous→gibbous 错误逐字节等纯解释器)

> R3c 引入逐层弹帧后,嵌套 gibbous→gibbous 错误报了**错的源码行 + 截断 traceback**——这是一个由 R3 触发暴露的潜伏失配,R3c-fix 修复且使错误质量**严格优于**旧 PW6c crescent→gibbous baseline。

| 维度 | R3c 后(回归) | R3c-fix | 收口 |
|---|---|---|---|
| 错误标注时机 | 错误**未标注**存进 pending,顶层 `executeFrom` 用 `currentCI` **延迟标注**——但 R3 逐层弹帧使标注时 `currentCI` 已非失败帧 ⟹ 读到错误帧行号 + 截断 traceback | 新 `raiseGibbous(e)` 收口点在**失败点就地标注**(失败帧仍是 `currentCI` 时)+ 物化 traceback + 标记「已标注」;所有 gibbous 错误助手路由经它;顶层据标记跳过重标注 | `86e39c9` |
| `ci.pc` 约定 | arith 族部分 helper 用 `ci.pc=pc`(与解释器「pc 执行中已自增」约定不一致),`errWithName` 的 `ci.pc-1` 指错指令 ⟹ `:0:` 行号 + 丢变量名 | 统一改 `ci.pc=pc+1` 对齐解释器约定 | `86e39c9` |
| 净结果 | (回归:错行号/截断 traceback) | gibbous→gibbous 错误**逐字节等于纯解释器**——**严格优于**旧 PW6c crescent→gibbous baseline(那个有截断 traceback、无变量名)。**是正确性改进,非仅回归修复** | `86e39c9` |

**根因**(详反思教训 3):「延迟标注」依赖隐含不变式「从产生错误到标注的路上当前帧一直是产生错误的帧」——解释器满足(错误路径不弹帧),R3 逐层弹帧打破之。修复本质是把标注从「延迟到高层」改成「就地、在失败帧仍是当前帧时」。

### 13.3 R3.5 host helper 改 wazero 零分配 API(真正付清性能)

> R3 做完且 e2e 逐字节一致后**重新跑基准——纹丝没动**(call 核仍 ~6x 慢、1.4M allocs/op)。memprofile 揭穿主导项:wazero `WithFunc` host-function 注册**用反射**,每次 host 跨界把每个实参装箱成 `reflect.Value`(`callGoFunc → reflect.Value.call → reflect.New`)——spike 当初归给「边界」的成本主导项是 **per-call 反射分配,不是分派延迟**。

| 维度 | R3.5 前(`WithFunc` 反射) | R3.5 后(`WithGoFunction`) | 收口 |
|---|---|---|---|
| host helper 注册 | 全部 25 个 helper 用 `WithFunc`(反射,每跨界 boxing 每个 i32 实参成 `reflect.Value`) | 全部改 `WithGoFunction(api.GoFunc(func(ctx, stack []uint64)))`(零反射,stack-based:实参经 `api.DecodeI32` 从 `stack[i]` 解、结果写 `stack[0]`) | `1bf9d53` |
| allocs/op | call 1.4M / table 1.6M / loop 2602 | **对齐解释器**:call 1.4M→**2** / table 1.6M→**6810** / loop 2602→**2** | `1bf9d53` |

**四核全翻面**(R3.5 后,gibbous/crescent 比值):

| 核形式 | R3.5 前 | R3.5 后 | ns 变化 | 判定 |
|---|---|---|---|---|
| loop(循环密集) | ~2.5x | **2.65x** | — | ✅ |
| table(表增长) | 0.64x | **1.26x** | 99→51ms | ✅ 翻面 |
| mixed(算术+表+分支) | ~0.68x | **1.14x** | — | ✅ 翻面 |
| call(小叶函数高频调) | 0.17x(6x 慢) | **0.49x**(2x 慢) | 115→38ms | 仍 <1x(唯一短板) |

**call 核残留 0.49x 的诚实归因**:不再是反射装箱,而是**真实的 per-call `h_call`(建帧)+`h_return`(拆帧)双跨界**作用在小叶叶函数上——这是 R4(消 `h_return`)/ Option B(inline frame-build 消 `h_call`)的真实靶子。**用户在「先修零分配 helper」与「先做 R4」之间显式选了前者**(R3.5),R4/Option B 留后续。

### 13.4 验证

V1-V13 层间 byte-equal + 四 build + `-race` difftest + lint 全绿。新增 e2e `internal/crescent/gibbous_r3_indirect_test.go`:
- **HappyPath**——经 `indirectCalls` 计数器**实证 `call_indirect` 真被走到**(`prove-the-path` 纪律,区分 indirect 直调 vs 同步 fallback)；
- **ErrorByteEqual**——合成嵌套错误断言行号 + traceback + 变量名**逐字节等于纯解释器**(补上 §12/全成功语料 difftest 的错误路径盲区,详反思教训 4);
- **BaseRefresh**——经 `growStack` 段重定位验 indirect 调用返回后 caller base 经中转字正确刷新。

**R4 在 §14「零跨界 RETURN 拆帧」中拆分为基建-a/③a/③b 三步分批交付**(其中 ③b emitReturn 守卫快路径已消 `h_return` 主流量);**剩 ④ CALL 建帧快路径(消 `h_call`)+ R5 re-bench**——call 核短板由 ④ 兜尾。

---

## 14. PW10 零跨界 RETURN 拆帧对账(承 §13 R3+R3.5)

> 承 §13(R3+R3.5)与反思 `2026-06-15-p3-pw10-zerocross-stage3-round.md`。**本轮交付 PW10 「零跨界」子里程碑的 ① 基建 / 基建-a / ③a / ③b 四步,③b 把 RETURN 拆帧从经 `h_return` host 单跨界改为 Wasm 内拆帧体 + 五守卫;难点是「守卫多但快路径覆盖率高、回退兜底安全」。④ CALL 建帧快路径 + R5 re-bench 仍待实现——PW10 仍在飞行中。** 三提交 `8aa4c02..1bff7d2`(承 Stage 0/1/2 检查 `6b903ea..a309a4f`):基建-a closure slot 缓存(`8aa4c02`)→ ③a savedTop(`455d1bd`)→ ③b emitReturn 守卫快路径(`1bff7d2`)。

### 14.1 Stage 0/1/2 前置检查(本节简记,详 reflection)

| 阶段 | 内容 | 提交 |
|---|---|---|
| **Stage 0 检查** | spike GREEN——in-Wasm 帧建拆 ≈8.5-9.4ns vs 2 跨界 ≈90ns,证明零跨界路径有 10x 余量(守卫不吞收益) | `6b903ea`/`41b78e9` |
| **Stage 1a/b/c** | ciDepth 字 + `syncCurFromSeg` 收口 + GC 读 `liveCIDepth`——为 Wasm 内段读写铺设元数据 | `335e8d5`/`70c2877`/`c2c9987` |
| **Stage 2 前置** | ci-seg-base + open-upvalue + **th.top mirror 字(零跨界 ①)**——Wasm 侧可直读 ci.base / openUvHead / th.top 三个 caller 状态 | `88d64be`/`ec026ba`/`a309a4f` |

**零跨界 ① top mirror 字**(`a309a4f`):caller 帧的 `th.top` 镜像到 arena 一个固定字(地址恒定,镜像 gcPending 模式),Wasm 侧 `i32.load offset=topMirrorAddr` 直读,无需穿 host。

### 14.2 基建-a closure slot 缓存(`8aa4c02`)

> 难点:emitReturn 守卫需要回答「caller 是 gibbous(同 module 经 `call_indirect` 进来)还是 helper(`h_call` 经 host 进来)」——这是 ③b 快路径 G2 守卫判据。要在 Wasm 内零跨界判,得有「closure→slot」的 inline 查询。

| 维度 | 内容 |
|---|---|
| 存储 | **word1 高 16 位 `[63:48]` 存 `slot+1`**(0 = 未填充,惰性填充 IC):Closure 进入共享 funcref 表的槽号缓存进自身 GCRef 的高位,Wasm 端取低 48 位作 GCRef 解 / 高 16 位作 slot+1 IC |
| 填充时机 | **tryIndirectCallee 首次查到 slot 后回写**(惰性 IC):`emitCall` 经 indirect 分派时,DoCall 写 closure word1 高位 → 下次 `emitReturn` 守卫读高位 ≠ 0 时即知 caller 是 gibbous-with-slot |
| 命中判据 | `G2 caller gibbous` = word1 高 16 位 ≠ 0(slot+1 已填充);未命中走 helperReturn 兜底 |
| 对位 | 复用 PW5 IC 「常量编译期烧 / 寄存器运行期惰性填充」分级思路——slot 是运行期才确定,故走运行期惰性 IC,首次 indirect 后所有重复 RETURN 直走快路径 |

**关键性质**:这条 IC 是**写入端单调**(slot+1 一旦填充就不变,closure→slot 是 module 静态绑定)——无需 gen 失效,无 deopt。

### 14.3 ③a savedTop 基建(`455d1bd`)

> emitReturn 快路径要还原 caller `th.top`——RETURN 完成的副作用之一是 `top` 落到调用前快照(C≠0 时 `top = base + C - 1`,C==0 时 `top` 不变)。在 Wasm 内做这一步,需要 caller 进 callee 前已经把 caller top 快照下来。

| 维度 | 内容 |
|---|---|
| 快照点 | **caller prologue 读 top mirror 字快照**——caller 帧 entry 时把当前 `th.top` 存进自己的 arena 段(savedTop 字段),供本帧将来 RETURN 时还原 |
| 写回时机 | **emitCall OK/done 两臂仅在 C≠0 时写回**——RETURN 的 C 是调用方 callee 给定的固定返回个数(C-1 个);C==0 表「callee 返回多值,top 自行决定」⟹ 不写回 savedTop(caller 保留 callee 留下的 top) |
| 对称性 | OK 臂(indirect 直调成功)与 done 臂(同步 fallback)**两条分派路径都要对称写回**——非对称会留下「同步路径恢复 top / indirect 路径不恢复」的解嵌套陷阱(③ 残留的最棘手风险面) |
| 守卫判据 | `G4 nresults 匹配` = C 与 caller 期望的 nresults 一致 + savedTop 已快照 |

### 14.4 ③b emitReturn 守卫快路径(`1bff7d2`,本轮核心)

> 把 RETURN 从经 `h_return` 单跨界改为 **Wasm 内拆帧体 + 5 守卫**,任一守卫失败回退 `helperReturn`(原有的 `h_return` host 路径作兜底)。

| 守卫 | 含义 | 失败回退原因 |
|---|---|---|
| **G5 ciDepth<2** | 段内当前帧深度 < 2(callee 帧只有 caller 一层在上)——单帧 RETURN 才能 in-Wasm 完成 | 多帧穿透(yield/coroutine resume/pcall longjmp)走 helper |
| **G3 openGuard** | 当前帧无 open upvalue 待 close | 走 helper(closeUpvals 留 Go,upvalue 物理协议未 inline) |
| **G2 caller gibbous** | caller 是 gibbous-with-slot(基建-a IC 高位 ≠ 0) | caller 是 helper/crescent/host,无 caller Wasm 帧可返,走 helper |
| **G4 nresults 匹配** | 返回值个数与 caller 期望一致 + savedTop 已快照(③a) | 多值/不匹配走 helper |
| (隐含)build tag | `wangshu_p3` 编译期开启 | 默认 build 不发射快路径 |

**Wasm 内拆帧体**(全过守卫时):
1. `moveResults` 展开——按返回值个数把 callee slot 内容直接 store 到 caller slot(展开是编译期立即数 offset,无循环);
2. **transfer word**——R3 既有的 `ciTransferRef` 中转字写入刷新的 caller base(同 R3 indirect 返回路径,LIFO-safe);
3. 段 `ciDepth--`——arena 段元数据 store(`Stage 1a` 字段);
4. caller `th.top` 从 savedTop 还原(C≠0 时,③a 对称);
5. 返回 caller Wasm 帧——caller 的 `call_indirect` 后续指令直接拿到刷新的 base(经 transfer word)继续跑。

**任一失败回退 `helperReturn`**:R3 既有的 `h_return` host 路径作兜底,语义不变(byte-equal 由 difftest 保);快路径仅是 RETURN 的零跨界子集。

### 14.5 关键正确性修复:gibCI wrapper 解 Option A 风险 #1

> Option A(零跨界子里程碑统称)留下的 #1 风险「陈旧 `&th.cur` 指针」:`currentCI` 从「指 Go slice」改成「读 arena 段镜像」后,若调用点持有的旧 `&th.cur` 在段写后未同步,会读到陈旧帧。

**修复**(本轮 ③b 同批):**36 处 `currentCI`→`gibCI` 重命名收口**,统一经 `syncCurFromSeg` 收口刷新 `th.cur` 镜像 ⟹ 任何持有 `&th.cur` 的调用点都在调用点之前先经 `gibCI` wrapper 同步,陈旧问题被构造性消除(对位 §12.2 R2b-4 `th.cur` 稳定地址一样的手法的二轮延伸)。

### 14.6 Stage 4 实测对账(本机 Xeon 6982P 2s×3 count,2026-06-15)

| 核形式 | R3.5 后(历史) | ③b 后(本机基线) | ns 变化 | 判定 |
|---|---|---|---|---|
| loop(循环密集) | 2.65x | **2.95x** | crescent 5.61→4.96ms / gibbous 2.17→1.68ms | ✅ **+10% ③ 真实收益**(`h_return` 主流量消除) |
| table(表增长) | 1.26x | **0.88x** | (跨机器漂移) | 持平——非 ③ 引起回归(同 commit 对照证) |
| call(小叶函数高频调) | 0.49x | **0.52x** | 略改善 | 仍 <1x,真实 `h_call` 建帧双跨界(④ 的靶子) |
| mixed | 1.14x | **0.99x** | (跨机器漂移) | 持平 |

**关键证据「同 commit 同硬件复测」**:Stage 4 一开始误判 ③ 引起 table/mixed 回归(基线对比读数掉了 30+%),经 git worktree 切到 ③ 前 commit、同硬件 2s×3 count 复测得到的「无 ③ 基线」也同样掉了 30%——差异源自硬件/bench 参数(本机 vs R3.5 当时机器),**不是 ③ 引入的回归**,本机所有四核 ③ 前 / ③ 后**对照内自洽**。教训:跨机器 perf 基线漂移须同 commit 同硬件复测对照,见 §14.7 教训 3。

### 14.7 验证 + 难点教训

**新增 ③b 命中探针**:`TestPW10ZeroCross_ReturnFastHit`——经 `doReturnHits` 计数器 + 「helper→f 不增 = 快路径命中」断言,**实证 ③b 守卫快路径真被走到**(承 PW9/R3 `prove-the-path-under-test` 纪律,本轮第 5 个独立实例)。

**难点教训(承反思)**:
1. **已识别未触发风险须配套触发用例**——计划文件标了「caller==gibbous + ciDepth≥2 触发解嵌套陷阱」风险列表但未配最小触发用例;Stage 4 difftest 全绿只证明守卫**未被触发**,真要测的陷阱处于结构盲区。纪律:计划文件每个风险标记末尾加「触发该风险的最小测试用例 = ___」,空白立刻补。
2. **difftest 快路径命中盲区(第 2 实例,跨过提升阈值)**——V1-V13 force-all 语料**不覆盖 ③b 守卫命中形式**(A/B 实证:禁 gibCI resync 时 difftest 仍全绿、R3 三件套全挂),根因 difftest 只保证「输出 byte-equal」**不保证「快路径走到」**。承 PW9/R3 错误路径盲区同家族,**`prove-the-path-under-test` 第 5 实例**(PW5 inline-proof / PW6 TierStuck / PW9 vararg 空测 / R3 错误路径 / 本轮快路径命中);留 followup 加针对性语料。
3. **跨机器 perf 基线漂移**——Stage 4 一开始误判 ③ 引起 table/mixed 回归,经同 commit 同硬件复测证差异源自硬件而非代码。纪律:任何 perf 数字判定回归/收益前,必须同 commit 同硬件同参数复测;memory/reflection 写 perf 数字时必须标硬件/参数/日期(本轮发现 index/startup/implementation-progress 三处缺标,本节起补)。

### 14.8 ④-i emitCall 守卫骨架 + fastCallHits mirror 字 + 基建-b proto cache 段(`8e820fd`+`bff1630`)

> 承 §14.4 ③b emitReturn 守卫快路径。④ 概念被分包为 **④-i 守卫骨架(本节交付)+ ④-ii fast body(留 followup,§14.10 详)**。
> ④-i 把 `emitCall` 的守卫部分(判断 callee 是否可走零跨界建帧)和性能探针(`fastCallHits` 镜像字)铺好,fast body(经守卫后在 Wasm 内组帧)留待 ④-ii。

| 维度 | 内容 |
|---|---|
| **基建-b proto cache 段** | 每个 Proto 在 arena 一个固定段(地址恒定,镜像 closure slot IC 思路),缓存 callee 的 MaxStack/NumParams/IsVararg/Compilable 等冷查询字段,供 emitCall 守卫 inline 读(否则每次穿 host 读 Go 侧 Proto 结构体 = 边界税回归) |
| **④-i emitCall 守卫骨架** | 5 守卫(callee 同 module / callee compilable / 非 vararg / nargs 匹配 NumParams / arena 容量充足);全过 → 写 fastCallHits 镜像字 +1 → **当前**回退 `h_call`(④-ii 才接 fast body);任一失败 → 走 `h_call`(R3 既有的 host 路径作兜底) |
| **fastCallHits mirror 字** | arena 一个固定 i32 字(地址恒定,镜像 gcPending 模式),emitCall 守卫全过时 inline 自增——既是 ④-ii fast body 上线后命中率监控点,**也是当下 ④-i 已生效的"哪些 call site 命中守卫"探针**(用于 §14.10 架构边界判据:bench kernel call site 命中数是否非零) |
| **对称性** | 守卫覆盖率 + helperCall 兜底语义不变 + difftest byte-equal——零行为变更基线,与 ④-ii fast body 解耦,可独立验证 |

**为什么先 ④-i 后 ④-ii**:守卫骨架是结构性铺底(proto cache 段 + mirror 字 + 守卫判据),可逐字节同构验证;fast body 是 wasm 字节级 codegen(组帧 + 绑 ciTransferRef + IC mirror 写回 + 错误路径展开),复杂度峰值远高于 ③b。把 ④-i 单独交付让骨架先稳,fast body 留待复杂度峰值评估(§14.10)。

### 14.9 callOnStack 顶层升层(`6bb9771`)

> 上层 hot path 升层:caller 顶层 closure 已升 gibbous 时,绕开 `h_call` 经 host 重入 callee,直接走 `enterGibbous`(等价于 callee 已经在 gibbous 栈上、当前调用 = 同栈 `call_indirect`)。

| 维度 | 内容 |
|---|---|
| **callOnStack 触发条件** | caller 顶层 closure 已升 gibbous(`cl.tier==TierGibbous`)+ callee 也满足升层条件(callee 可经共享表 indirect)⟹ 顶层经 `enterGibbous` 直接进 gibbous 栈而非走 crescent.doCall → h_call → gibbous |
| **TopLevelUplift 探针** | bridge 侧 `TopLevelUplift` 计数器(testing-only)记录顶层升层触发次数,**实证路径真被走到**(prove-the-path-under-test 纪律) |
| **架构动机** | R3.5 + ③b 后 call 核仍 0.52x,profile 揭示 52% 在 enterGibbous + 38% 在 wazero CallWithStack;原假设「② 顶层升层 + ④ emitCall fast body 能消掉这两块」⟹ ② 单独交付后看是否对 bench kernel 有效(§14.10 给出否定证据) |
| **覆盖面** | 仅顶层 hot path(caller 已 gibbous + callee gibbous-with-slot);body 内部的内层 call 是 ④ 的靶子 |

### 14.10 ④-ii 架构边界与未交付决策

> **PW10 收口的核心判据节**。承 ④-i 骨架(§14.8) + 顶层升层(§14.9)。本节文档化「为什么 ④-ii fast body 未交付,call 0.52x 是 bench kernel 结构性架构边界而非实现不足」。

#### 14.10.1 profile 实证(`/tmp/call.prof`)

call 核 cpuprofile 主导项分布:
- **52% enterGibbous**——gibbous 帧 entry 开销(arena 段 push + R2/③a savedTop 写 + 镜像字同步);
- **38% wazero CallWithStack**——wazero 跨 Go/Wasm 边界本身(R3.5 已消反射装箱,残留是 stack-based call 不可避免的固定成本)。

#### 14.10.2 bench kernel 不可升根因(F2-b)

四 kernel 结构均为 `body → fn`(body 调内层 fn):

```
function call_bench()
  local function fn() return 1 end       -- 内层叶函数
  local function body()
    for i=1,N do fn() end                 -- body 调 fn
  end
  body()                                  -- 顶层调 body
end
```

- **body 含 `fn()` 调用**——P2 F2-b 静态分析无法静态确定 `fn` 不 yield(unknown call target 形式)⟹ body 标 ReasonUnknownCall → **body 不可升层**(crescent 跑);
- **fn 是叶函数可升层**,但顶层升层(②,§14.9)只升 caller `body`,body 不可升 ⟹ body 走 crescent → body 内调 fn 仍经 crescent.doCall → `h_call` → gibbous(fn)→ 跨界税未消;
- **④ emitCall fast body 即使完成**,需在 body 的 gibbous 翻译里 inline 建帧——但 **body 根本没翻译进 gibbous**(F2-b 不可升),fast body 无处发射 ⟹ 对 bench kernel 无效。

#### 14.10.3 ④-ii 实现复杂度 vs 预估 ROI

| 维度 | 估值 |
|---|---|
| **代码复杂度** | ~200 行 wasm 字节级 codegen(组帧 + 绑 ciTransferRef + IC mirror 写回 + 错误路径展开 + base 刷新对称) |
| **UAF 风险面** | 高——wasm 字节级操作 linear memory 段(ci-seg 写 callee 帧 + open upvalue 链接 + savedTop 对称写)+ 跨守卫失败/成功路径状态一致性(任一守卫失败必须把已部分写的状态退掉) |
| **预估上限** | 即使 ④-ii 完成,call 核约 **0.57x**(仅消掉 0.52x 中 ~10% 的 h_call 建帧延迟,反射装箱已在 R3.5 消掉、剩余 enterGibbous 开销 ④-ii 不动)——**仍 <1x** |
| **触达面** | 仅对「body 可升 + 内层 fn 频繁调」的 kernel 形式有效——bench 四 kernel 全部不满足(F2-b 已剥离 body) |

#### 14.10.4 收口决策

**决策:留 followup,emit 原语 i64.add/i64.or 已保留供未来 ④-ii**。

判据:
- profile 已实证 ④-ii 对 bench kernel 无显著效果(根因 F2-b 不可升,与 ④-ii 实现层无关);
- 0.57x 上限仍 <1x ⟹ 即便实现也不到 PW10 原立项「call 核 ≥1x」数字目标;
- 实现复杂度峰值(~200 行 wasm 字节级 codegen + 高 UAF)远超 ③b(③b 仅 5 守卫 + 拆帧体)⟹ ROI/UAF 不利;
- **call 0.52x 是 bench kernel 结构性架构边界**——不是实现不足,要拉到 ≥1x 须改 bench kernel 形式(让 body 可升)或改 F2-b 静态分析口径(扩大「不 yield 可证明」集合),均超出 PW10 范围。

承 [[2026-06-16-p3-pw10-architectural-ceiling-round]] 反思教训 1「**profile 才是合同(不是立项数字)**」+ 教训 3「**wasm codegen 复杂度峰值需前置编排**」——立项数字目标 `call 0.52x → ≥1x` 是「方向陈述」,profile 实测主导项(enterGibbous 52% + CallWithStack 38%)证明立项数字事实上不可达,**收口为「已完成子里程碑 + 架构边界文档化」**,绝不硬上高 UAF 的 ④-ii fast body 追不可达数字。promotion 完成:[[perf-optimization-workflow]] §7「立项数字目标 vs profile 实测瓶颈」新立节。

#### 14.10.5 followup 触达条件

未来 ④-ii fast body 若要交付,需满足以下一项之一:
- **bench kernel 形式调整**:让 body 可升(去掉 unknown call,或抽出独立非调用层让 body 是纯叶),使 ④-ii 有处发射;
- **F2-b 静态分析口径扩张**:扩大「不 yield 可证明」集合(如局部 closure 不引出 + 不调 yield 系)使 body 可升;
- **emit 原语扩展**:在 i64.add/i64.or 基础上扩展 wasm 内 GC root barrier inline 化(让 ④-ii UAF 面降下来)。

**emit 原语 i64.add/i64.or 已保留**(本轮 ④-i 骨架引入,后续 ④-ii 直接复用),不需重新打通。

### 14.11 验证 + PW10 收口总结

**新增 ③b 命中探针**:`TestPW10ZeroCross_ReturnFastHit`——经 `doReturnHits` 计数器 + 「helper→f 不增 = 快路径命中」断言,**实证 ③b 守卫快路径真被走到**(承 PW9/R3 `prove-the-path-under-test` 纪律,本轮第 5 个独立实例)。

**新增 TopLevelUplift 探针**(§14.9):bridge 侧计数器记录顶层升层触发次数,**实证 callOnStack 路径真被走到**(本轮第 6 个独立实例)。

**fastCallHits mirror 字**(§14.8):④-i 守卫命中实证——bench kernel 上读到 0 命中 = ④ 即使做完 fast body 也对 bench kernel 无效的强信号(与 §14.10.2 F2-b 不可升根因一致)。

**难点教训(承反思 `2026-06-15-p3-pw10-zerocross-stage3-round` 三条 + 本轮 `2026-06-16-p3-pw10-architectural-ceiling-round` 三条)**:

承前轮三条:
1. **已识别未触发风险须配套触发用例**——计划文件标了「caller==gibbous + ciDepth≥2 触发解嵌套陷阱」风险列表但未配最小触发用例;Stage 4 difftest 全绿只证明守卫**未被触发**,真要测的陷阱处于结构盲区。纪律:计划文件每个风险标记末尾加「触发该风险的最小测试用例 = ___」,空白立刻补。
2. **difftest 快路径命中盲区(第 2 实例,跨过提升阈值)**——V1-V13 force-all 语料**不覆盖 ③b 守卫命中形式**(A/B 实证:禁 gibCI resync 时 difftest 仍全绿、R3 三件套全挂),根因 difftest 只保证「输出 byte-equal」**不保证「快路径走到」**。承 PW9/R3 错误路径盲区同家族,**`prove-the-path-under-test` 第 5 实例**(PW5 inline-proof / PW6 TierStuck / PW9 vararg 空测 / R3 错误路径 / 本轮快路径命中);留 followup 加针对性语料。
3. **跨机器 perf 基线漂移**——Stage 4 一开始误判 ③ 引起 table/mixed 回归,经同 commit 同硬件复测证差异源自硬件而非代码。纪律:任何 perf 数字判定回归/收益前,必须同 commit 同硬件同参数复测;memory/reflection 写 perf 数字时必须标硬件/参数/日期。

**本轮新增三条**(承 `2026-06-16-p3-pw10-architectural-ceiling-round`,详 §14.10):
4. **profile 才是合同(不是立项数字)**——立项时数字目标(0.X x → ≥Y x)是「方向陈述」,完成中 profile 揭示「立项假设的瓶颈 ≠ 实测主导项」或「立项数字事实上不可达」时,**先重评目标可达性再决定继续/止损/换路径**,绝不硬上高 UAF 实现追不可达数字。promotion **已完成** [[perf-optimization-workflow]] §7。
5. **stop hook 诚实解读**——/goal stop hook 强制不结束时若 profile 证明数字不可达,收口应是「已完成子里程碑 + 文档化不可达边界」,绝不硬上 UAF 代码追数字。(首次样本,留反思)
6. **wasm codegen 复杂度峰值需前置编排**——④-ii fast body 需在 wasm 字节级直接组帧、绑 ciTransferRef、IC mirror 写回、错误路径展开,复杂度峰值远超 ③b 守卫快路径,**前置编排成本** vs **ROI** 须在立项时就算清。(首次样本,留反思)

**PW10 收口状态**(2026-06-16,HEAD `9ab0dba`):
- ✅ R1/R2/R3/R3.5 + 零跨界 ①/基建-a/基建-b/③a/③b/④-i 骨架 + 顶层升层 callOnStack 全付;
- ✅ 本机 Xeon 6982P 2s×3 count 实测基线:loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x;
- ✅ call 0.52x 架构边界文档化(§14.10 profile 实证 + 决策依据);
- ⏳ ④-ii fast body 留 followup(架构边界,ROI/UAF 不利,emit 原语 i64.add/i64.or 已保留);
- **PW10 收口为「已完成子里程碑 + 架构边界文档化」**,旧文档「四核全翻面」+「剩 R4/R5 待实现」失实须替换。

相关:
- [00-overview](./00-overview.md)(P3 总览,本文是其 §4 PW 表的运行期对账)
- [01-spike-gate](./01-spike-gate.md)~[08-testing-strategy](./08-testing-strategy.md)(各子系统设计文档)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 一样的)
- [../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 一样的,作维护协议参考)
- [../../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策的纪律来源)
- [../../../llmdoc/guides/perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)(§7「立项数字目标 vs profile 实测瓶颈」承本轮教训 1)
- [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 开工前置确认 / P3 迁移留口)
- 反思:`../../../llmdoc/memory/reflections/2026-06-15-p3-pw10-r1-r2-callinfo-migration-round.md`(R1-R2,§12)、`../../../llmdoc/memory/reflections/2026-06-15-p3-pw10-r3-call-indirect-round.md`(R3+R3.5,§13)、`../../../llmdoc/memory/reflections/2026-06-15-p3-pw10-zerocross-stage3-round.md`(零跨界 ③,§14.1-§14.7)、`../../../llmdoc/memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md`(架构边界收口,§14.8-§14.10)
