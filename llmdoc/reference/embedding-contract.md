# 参考:宿主嵌入契约

> 状态:字段级 spec 定稿于 `docs/design/p1-interpreter/11-embedding-arena-abi.md`;**收尾轮后 `Program.Call(state, arena, args)` 与 arena 列接口已完成,剩余差异见下「P1 实际完成差异」**。概念源:`docs/design/roadmap.md` (§8),量化背景见 (§1)。本文只保留契约形状,字段细节查 11。
> 这套接口**刻意设计为鼓励「列内核」形状**——为什么必须如此,见 [[design-premises]]。

## 设计意图:逼着宿主走列内核形状

列内核形状 = 循环写在 Lua 内,**一次调用进一次 VM,整批数据在 VM 内迭代**,而不是 per-item 反复跨界。两个校准测量(§1)证明 per-item 跨界会被边界成本吃光收益,因此嵌入接口被设计成天然鼓励列内核;宿主侧配套改造不在本项目范围。

## 核心 API

| 接口 | 语义 |
|---|---|
| `Compile(script) → Program` | **一次编译**,含**可编译性探测与层级决定** |
| `Program.Call(state, arena, args)` | **一次调用一次跨界**;批量数据经 arena 传递 |

- `Compile` 在编译期就完成可编译性探测与升层决策(对应 [[evolution-roadmap]] P2 的静态可编译性分析)。
- `Program.Call` 的设计要点是把「跨界」压缩到每批一次——这是列内核形状在 API 层面的完成。

> **P1 实际完成差异**(长稳/审查修复轮后):`Program.Call(state *State, arena *Arena, args ...Value)` **已按 11 §1.5 签名完成**(`wangshu.go`),arena 列数据接口可用——`NewArena` + 四类型列(`AddFloatColumn`/`AddInt64Column`/`AddBoolColumn`/`AddStringColumn`,见 `arena_abi.go`)+ presence bitmap + VM 内只读访问;另有 `Program.Run(state, args...)`(无 arena 便捷形)与 `NewState(Options)`。增量更新:① **公共 Value API 更名**——`String_()` → `Str()`、`GoString()` → `Display()`;② **`Options.AllowFileLoad` 安全门控**——`loadfile`/`dofile` 默认禁用,须显式开启(豁免注册表已登记);③ **`State.SetStepBudget`**——执行工作量预算(非纯指令计数),超限抛可恢复 "instruction budget exceeded",宿主脚本配额的种子机制;每个 preempt 点(循环回跳/调用/TFORLOOP)计 1 单位,此外 CONCAT 按结果字节数额外计 `len>>6`(见 `internal/crescent` `chargeBulkWork`,issue #166/#167 concat 风暴根因),即使没有循环回跳单次大 CONCAT 也可能触发,分档粒度见 `docs/embedding-tiers.md` §5;④ **同一 Program 多 State 多 goroutine 并发已验证**(`test/.../concurrency_test.go`,`-race` 通过);⑤ hostFn 注册表槽回收(引用计数 + GC 回调,长驻 State 不再泄漏);⑥ **per-item drop-in 子集已完成**(issue #1):`State.SetGlobal/GetGlobal/Call(fn, args...)` + `State.Register/RegisterModule` + 公共 `HostFn = func(*State,[]Value)([]Value,error)`;`Value` 加 `kFunction` kind(外部不可构造,只由 `GetGlobal` 取出,经 State pin 表登记为 GC 根;`v.Release()` 显式释放);⑦ **公共 Table API 已完成**(issue #2):`State.NewTable` + `Value.AsTable` + `Table.Set/SetIndex/Get/GetIndex/Len`;`Value` 加 `kTable` kind(同 kFunction 经 pin 表挂 GC 根);`Program.Run/Call` 与 `State.Call` 返回路径已升级到 `fromInnerWithPin` → 脚本返回 table/function 可在 Go 端读出;⑧ **`Options.HideFileLoaders` 严格沙箱**(issue #3):置 true 时从 globals 刮除 `loadfile`/`dofile`/`loadstring`/`load` 四件套(置 Nil),脚本调用 fatal `attempt to call global 'X' (a nil value)`,对位 gopher-lua;与 `AllowFileLoad=true` 同设 panic fail-fast;⑨ **`State.SetContext/RemoveContext` 取消钩子**(issue #4):context.Context 注入,VM 在 preempt 同一抢占点检查 `ctx.Err()`,事件触发(wall-clock timeout / 上游 Cancel)中止 Run/Call 返回包装 ctx.Err 的 Go error,pcall 可捕获,跨 goroutine 由 atomic.Pointer 保护;⑩ **`Table.ForEach` 任意 key 迭代**(issue #5):`func (t *Table) ForEach(fn func(key, val Value) bool) error`,转发 internal RawNext 循环(raw 迭代,与 stdlib next/pairs 同源,迭代序确定性),fn 返 false 提前终止;脚本返 map 的对称读出能力,与 issue #2 SetIndex 写入构成完整读写闭环;⑪ **`State.MarkGlobalsBaseline` / `ResetGlobalsToBaseline` 脚本级状态隔离**(issue #6):pineapple sync.Pool 复用 State 形式下,Mark 拍当前 _G 字符串 key 快照为基线、Reset 把非 baseline key 清空 + baseline key 复原。对位 gopher-lua statePool snapshotBaselineValues + resetToBaseline 模式;baseline 复合值经 visitExtraValues 入 GC 根(与 pin 表是契约级不变式的两面:pin 管「公共 API 暴露的长持 GCRef」、baseline 管「内部状态恢复需要的长持 GCRef」)。`§7.1` 草图里的 Push/Pop/CallFn 栈机风格未做(pineapple 实际用法不依赖,见 [memory/reflections/2026-06-12-official-suite-perf-round](../memory/reflections/2026-06-12-official-suite-perf-round.md));host closure 的 Go 端直接 Call(`state.Call(hostFn,…)`)仍未开(internal `State.Call` 拒);HostFn 收到的 args 中 table/function/userdata 仍映射为 Nil。可编译性探测/升层决策属 P2。实现形式与 11 的字段级差异见 `docs/design/p1-interpreter/implementation-progress.md` 对账表。

## 公共 first-class GCRef-bearing value:必须接 GC 根(契约级不变式)

> 状态:**契约级硬规则**——任何让宿主 Go 端长期持有 VM 内部 `GCRef` 的公共 API,该 `GCRef` 必须经 `State` pin 表(`pinnedRefs` + `freePins` + `visitExtraRefs`)登记为 GC 根。两次样本(issue #1 kFunction / issue #2 kTable)用一样的机制零额外接根工作,验证为通用不变式而非一次性手法。

**覆盖面**:本期已完成 `kFunction`(issue #1)/ `kTable`(issue #2);未来若新增 `kUserdata`/`kThread`/`kCoroutine` 或其它公共 first-class kind,前置约束相同——实现前先核对 pin 表是否覆盖。

**为什么是契约级而非工作流级**:

- shadow stack 是 LIFO,公共 API 的持有期是任意的,LIFO 假设不适用;
- `globals` 覆盖同名 + freelist 复用会把潜伏的根管理 bug 从良性(死对象躺 arena)升级为致命(UAF 或串台执行);
- 两次样本用一样的 `pinnedRefs / freePins / visitExtraRefs` 通道零额外接根工作——是机制级保证,不是 kind 特殊路径。

**如何识别违约**:Go 端取出 first-class 复合 Value 后,`globals` 覆盖同名 + GC 压力模式(`SetGCStressMode(true)`)→ 重新读 Value/调用 → 若访问 panic 或返回错值,即接根缺失。

**释放纪律**:`Value.Release()` 显式释放可选——不释放仅累积少量 pin 槽,致命的是没接根。长驻 State 高吞吐场景应配对调用以防 pin 表无界增长。

**与对偶面**:与长稳轮「内存复用类变更配套清单」对偶——后者管 VM 内部 freelist 内的根全审计,本条款管公共 API 暴露的长持 GCRef。两面同根:**任何让某对象在「VM 自己的生命周期管理之外」被持有,都必须显式接根**。

**baseline 维度**:globals baseline(issue #6)是同一不变式在「内部状态恢复」维度的完成——baseline 快照持有的复合值(table/function)同样是 GC 可达对象,经 `visitExtraValues`(而非 pin 表的 `visitExtraRefs`)入 GC 根。pin 表管「公共 API 暴露的长持 GCRef」,baseline 管「内部状态恢复需要的长持 GCRef」,两者分通道但同一不变式:长持 GCRef 必须接根。

**锚点**:`87031c2`(kFunction 立项)/ `2b55e11`(kTable 验证机制通用);源反思 `memory/reflections/2026-06-12-issue1-api-gap-round.md` 教训 2 + `2026-06-12-issue234-api-gap-round-2.md` Q2 评估。工作流维度的完成纪律(怎么判断该接根、怎么验证)见 [[public-api-incremental-delivery]] §2。

## arena ABI

宿主直接写,**VM(解释器与编译层)零拷贝读**。布局:

- **类型化扁平列**:`[]float64` / `[]int64` / `[]bool`;
- **字符串区**;
- **presence bitmap**(标记每个槽位是否有值)。

> ABI 字段级细节已定稿于 `docs/design/p1-interpreter/11-embedding-arena-abi.md`:类型化扁平列编码(§3.3)、字符串区 offset 表+字节池(§3.3.4)、presence bitmap 位序(§3.4)、`args` 与 arena 的精确关系(§4.3)、句柄表(§6)、per-item 简易 API(§7)。本文不搬运字段细节,查 11。

这块 arena 与 [[value-representation]] 的自管线性内存是同一份内存的不同视角——「零拷贝读」之所以成立,正因为 VM 各层值世界本就住在这块共见内存里(Arrow 数据搬家模型,替代「让 Lua 直访 Go 堆」,见 [[project-overview]] 非目标)。

## 不强制 arena 的简易 API

- **per-item 风格的简易 API 子集已完成**(对标 gopher-lua 易用性):`State.SetGlobal/GetGlobal/Call(fn, args...)` + `Register/RegisterModule`(见上差异标注 ⑥);
- **Table 读写全闭环已完成**:`NewTable` / `Set/SetIndex/Get/GetIndex/Len` + `ForEach` 任意 key 迭代(见差异标注 ⑦ ⑩)——写入与迭代读出对称,宿主端完整操作 Lua table 无需 Push/Pop;
- **globals baseline 状态隔离已完成**:`MarkGlobalsBaseline/ResetGlobalsToBaseline`(见差异标注 ⑪)——sync.Pool 复用 State 形式下宿主端一行调用恢复干净 _G,drop-in 配套能力;
- **`CallInto` 零分配边界路径已完成**(issue #8):`State.CallInto(dst []Value, fn, args...) (n int, err)` 让调用方拥有返回值 buffer,标量(bool/number)round-trip 0 alloc。旧 `Call` 每次固定 72 B / 2 allocs(VM 栈→inner→public 双拷贝,与脚本复杂度无关),boundary-dominated 嵌入(per-item 短调用)被这地板成本主导、在对标场景反被 gopher-lua 超过;`CallInto` 消除双拷贝(内部零拷贝切复用栈 `th.stack`,门面复用 `innerArgsBuf` + 写调用方 dst),两档边界基准均反超 gopher-lua。⚠️ 契约:返回值底层是复用栈,下次进入 VM 前消费完;string 仍拷 arena 字节、复合值仍经 pin 表。**这是「实现浪费」的消除,非架构成本——不违背列内核前提(前提一仍成立:列内核完全摊薄边界成本是高吞吐首选),而是补上无法列内核化的 per-item 形式的零分配通道**;
- **boundary-dominated 嵌入快路径已完成**(issue #13,parity-friendly,不破跨引擎 `lua_script` 字节对等):
  - **类型化 array table 族**:`State.NewFloatArrayTable / NewInt64ArrayTable / NewBoolArrayTable / NewStringArrayTable` —— 从 typed slice 一次性 NaN-box 进 arena 数组段,跳过 `[]Value` 中转。脚本侧看到普通 array table(`xs[i]` / `#xs` / `pairs`),**不是** arena 列轨的 `__index` 代理 —— pineapple 一类 common-mode 灌列形式(`SetGlobal(field, []any) → makeArrayTable`)的下层中转可消,无需脚本改动;Int64 承袭 `Arena.AddInt64Column` 的 |v|>2^53 报错规则。
  - **`GlobalsSlot` 预解析句柄**:`State.GlobalsSlot(name) → slot` + `SetBySlot(slot, v) / GetBySlot(slot) / slot.Release()` —— 把 `gc.Intern([]byte(name))` 摊销到 Init 期一次性,热循环里 SetGlobal 跳过 `[]byte` 分配 + intern 哈希查找。仅消除宿主端 intern 成本;globals rawtable 本身查找仍 per-key irreducible。跨 State 误用 panic fail-fast(同 `State.Call` 跨 State 函数实参风格)。
- **批量 array table 构造已完成**(issue #10,绕 rehash 风暴):`State.NewArrayTable(vals []Value) Value`(一次性分配 array 段 = `len(vals)` 写入,返回 pin 表登记的 table-kind Value)+ `Table.Preallocate(n uint32) error`(预扩 array 段,仅扩不缩、原数据保留)。`naive NewTable + SetIndex(1..N)` 的反复 rehash 是 O(N²);两者把它压回 O(N) 摊销(N=1000 实测 25-60× 加速)。已知最终大小直接 `NewArrayTable`;分次填充但已知大小用 `NewTable + Preallocate(N) + SetIndex`。`vals` 含复合值时同经 pin 表接根。
- 未完成的 Push/Pop 栈机风格按 `§7.1` 草图保留承诺;
- 但文档**明确标注其性能档位**——`Call` 走 per-item 跨界形式,落在被边界成本主导的那一档(见 [[design-premises]] 前提一);高频热路径优先列内核(arena 轨),无法列内核化时用 `CallInto` 走零分配。

## State 生命周期:arena 容量与 GC 节奏管理(admin API)

> 状态:**已完成**(issue #9 / #11,2026-06-16)。面向 long-running State pool 嵌入(规则引擎 hot reload / 数据流转换):boundary-dominated 工作负载下 host 反复构造大表 + 短脚本时,VM opcode safepoint 触发频率不足以让 GC 跟上 host-driven allocation 节奏 → arena 账面单调上涨,需 host 侧显式管理。

**arena 容量定制 + 观测**(issue #11):

- `Options.InitialArenaBytes` / `Options.MaxArenaBytes`:`NewState` 时定制 arena 初始容量与 fail-fast 上限;
- `State.GCCountKB() float64`:返回 arena 当前 **live bytes**(bump 指针,含 freelist 待复用块),随 Collect 回落;
- `State.ArenaCapKB() float64`:返回 arena **backing slab 容量**,反映真实 Go 堆驻留——grow-only 模式下单调上涨、不被 Collect 缩。pool 层据此判 fat state 阈值(比 GCCountKB 准,后者被 sweep 隐藏了 latched high-water);底层 arena Compact 在 Collect 末尾缩 backing slab 到 `max(bump, 64 KiB)` 缓解高水位 latched(默认 build 生效;P3 收养 wazero linear memory 模式 no-op)。

**GC 节奏显式驱动**(issue #9):

- `State.Collect()`:强制一次 full sweep(对应 `collectgarbage("collect")`),免走脚本调用迂回。典型在 pool 归还点 / 批次完成点周期调用,保持 GCCountKB 有界。代价 ~微秒到毫秒级(取决 live 规模),不缩 backing 容量;
- `State.MaybeCollectNow()`:按阈值条件触发(命中才 collect,否则 no-op),等价让 host 触发一次 safepoint 检查。比 Collect 廉价但不保证 sweep,强约束需 sweep 直接调 Collect;
- `State.SetHostTriggeredCollect(on bool)`:**experimental opt-in,默认 off**。开启后任何 host alloc 跨 GC 阈直接 sweep。⚠️ **安全契约**:调用方须保证所有 transient GCRef 都 reachable from GC root——current stdlib + string intern 的 mid-construction transient GCRef 未全经 pin/shadow stack 登记,**未审计前生产开启有 UAF 风险**(已知 break:luasuite gc/literals/nextvar/pm/strings)。**推荐替代**:用 `Collect()` / `MaybeCollectNow()` 显式 cadence 控制(production-safe)。

## 分层执行运行期管理:kill switch 与观测(admin API)

> 状态:**已完成**(PR #115,2026-07-10)。面向生产灰度 P3/P4 分层执行:线上怀疑升层路径(wasm 段 / 原生段)有问题时,不必重建 State 或重新编译进程即可降级到纯解释器,配合观测快照定位。非 `wangshu_p3` / `wangshu_p4` build 下解释器是唯一执行层,这组 API 均 no-op(观测返回零分布 + `TierEnabled=true`)。

**运行期总开关**:

- `State.SetTierEnabled(enabled bool)`:分层执行的运行期 kill switch(默认开启)。关闭后不再发生新升层(入口/回边采样短路、不再累积热度)、**已升层的 Proto 也回到解释器执行**(下一次分派决策起生效,正在段内执行的一次调用正常跑完);已编译产物保留在缓存,重新开启即恢复分层、**不重新编译**;
- `State.TierEnabled() bool`:返回开关当前状态。

**State 级观测**:

- `State.TierStatsSnapshot() TierStats`:返回本 State 的分层执行分布快照(诊断路径,开销很小但不建议逐帧轮询)。`TierStats` 字段:`Promoted`(已装 wasm/原生产物的 Proto 数)/ `StuckNotCompilable`(可编译性检查排除形状,预期内)/ `StuckDeclined`(profitability gate 判不划算,预期内)/ `StuckCompileFailed`(**非零值得排查**)/ `Profiled`(进过采样钩点的 Proto 数)/ `TierEnabled`(镜像开关状态)。

> `SetTierEnabled` godoc 明标「生产 admin API」,与 testing-only 的 `SetForceAllPromote` 措辞区分。三向 kill switch 语义的正确测法(promote→off→on 三段式,只断言「结果一致」等价于没测)见 [[prove-the-path-under-test]] §2(d)。嵌入方选层 / P4 部署要求 / kill switch 用法的完整部署指南见 `docs/embedding-tiers.md`;过程背景见 `memory/reflections/2026-07-10-tier-admin-luajit-bench-round.md`。

## 宿主绑定与 drop-in

- **首个目标宿主**:一个**多运行时规则引擎**(其 Go 运行时现用 gopher-lua);但接口**不绑定任何宿主**。
- **P1 解释器即可作为 gopher-lua 的 drop-in 候选**(见 [[evolution-roadmap]] P1)。
- **stdlib 默认面对齐 gopher-lua 的 OpenLibs 提供面**(兑现 drop-in 宣称);宿主收紧机制的设计承诺是三层:**LibsSafe 预设 / Libs 位掩码 / Exclude 函数级**——收紧能力是 VM 责任,收紧决策是宿主责任。**当前实际完成是双门控**:`Options.AllowFileLoad`(loadfile/dofile 默认禁用)+ `Options.HideFileLoaders`(issue #3,严格沙箱:从 globals 刮除 loadfile/dofile/loadstring/load 四件套,见差异标注 ⑧),完整三层机制留待宿主接入前完成(见 doc-gaps)。
- **sync.Pool 复用 State 配套**:`MarkGlobalsBaseline/ResetGlobalsToBaseline`(issue #6)提供 pool 取出→跑脚本→重置→放回的状态隔离闭环,对位 gopher-lua statePool snapshotBaselineValues + resetToBaseline 模式(见差异标注 ⑪)。
- 细节见 `docs/design/p1-interpreter/10-stdlib.md` §12.1、`11-embedding-arena-abi.md` §1.2;决策背景见 `memory/decisions/2026-06-11-design-review-decisions.md` 第 6 项。

---

相关:[[design-premises]] · [[value-representation]] · [[evolution-roadmap]] · [[project-overview]] · [[glossary]]
