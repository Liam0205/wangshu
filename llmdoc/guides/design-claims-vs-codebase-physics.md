# Guide:设计稿主张须对本码库 physics 重新验证

> 适用:把设计稿热路径上的抽象记号(`(call $x)`)、固定 token(base/指针/句柄/视图)、或成本主张照搬到实现之前——尤其每指令必经的快路径、跨层/跨调用存活的值。P3 翻译全程复发,P2 编译层同理。
> 来源:`memory/reflections/2026-06-13-issue8-boundary-cost-round.md`(成本归类)+ `memory/reflections/2026-06-14-p3-pw5-table-ic-round.md`(边界成本预算)+ `memory/reflections/2026-06-14-p3-pw6-crosslayer-call-round.md`(段重定位 UAF)——三轮独立实例聚合为一个判断框架。

设计稿表达的是**语义意图**,用抽象记号写在纸上;它**不携带本码库的物理不变式**。三次了:把设计稿热路径上的一条主张/记号忠实誊写到加速层,本会产出一个 bug 或一处死优化——因为设计稿对某条 wangshu 专属的物理事实是盲的(边界成本、arena 段重定位、GC 根可达性……)。**热路径上的抽象记号在实现前,必须逐条对照本码库 physics 重新推导,而不是照抄伪码。** 这条横跨**性能**(誊写出死优化)与**正确性**(誊写出 UAF)两面,故单独成 guide,不并入 [[perf-optimization-workflow]]。

## 1. 边界成本预算——热路径上的 `(call $x)` 能否塌成 inline

设计稿 WAT 伪码里写成 `(call $helper ...)` 的快路径节点,是**记法不是承诺**:`$helper` 只表达「这里要做 X 语义」,不等于「这里要发一次真实跨层调用」。**PW0 spike 实测一次 gibbous→host imported 调用约 ~143ns**——若快路径上(尤其每指令必经的 IC 快路径)真调一次助手,「跳过哈希」省下的几个 ns 当场被边界成本吞光,整个 inline 加速立项归零。

**实例(PW5)**:`02-translation.md` §3.4 把 `$ic_slot_load`/`$ic_key_match` 写成助手调用形态。正解是全 inline `i64.load`——table 活在 arena = wazero linear memory,`value.GCRefOf(v)`(低 48 位)**就是**字节偏移,`SNAP_INDEX` 是编译期立即数 ⟹ 所有槽 offset 都是常量,助手退化成几条 load。

**判据**:任何标在热路径的助手记号,实现前拿 ~143ns/次的边界成本预算重过一遍——它能不能塌成几条 inline load?能,就**必须** inline,否则该 opcode 的整个加速失去意义。

## 2. arena 段重定位——跨层/跨调用存活的 base/指针/视图,底层段会不会被 grow 搬走、谁能刷新

本码库的物理事实:**值栈与对象世界共享自管 arena,任意分配触发的 grow 会把段在 arena 里重定位**(改写底层基址)。设计稿把一个热路径值跨层画成「入口一次性收到、全程不变」的固定 token 时,这条不变式不在它视野里,却让那个「固定 token」在落地时变成悬垂指针。

**实例(PW6)**:`04-trampoline.md` §2.2 把 `$base`(linear memory 字节偏移)画成 gibbous wasm 函数入口锁定、全程不变。但 gibbous 帧经 `h_call` 调更深 Lua 帧时,嵌套 `growStack`(`internal/crescent/state.go`)把值栈段在 arena 重定位、改写 `th.stackBaseW`,返回后陈旧 `$base` 指向已 Free 旧段 = **UAF**。解法:`h_call`/`h_tailcall` 返回**重算后的新 base**(i64,负哨兵表错),CALL 翻译 `local.tee`→负则 `return 1` 冒泡/否则 `local.set $base` 刷新。

**判据**:这个 token 在它存活的窗口内,底层存储会不会被搬动/失效?谁有能力刷新它、在什么时机?「入口锁定、中途不能自刷新」的载体(wazero local)碰到任何「跨层调用后恢复」的点,都是潜在失效点,必须由被调侧返回时回传刷新后的值。⚠️ **解释器恰好因「每次访问经 `th.slot(i)` 现算地址」碰巧免疫,反而掩盖危险——照搬解释器「能跑」不等于加速层「能跑」**(见下「如何用」)。这是 `feedback_arena_view_aliasing`「形态 Y 别名」雷区的 gibbous 帧对偶。

## 3. 成本归类:架构成本 vs 实现浪费——援引前提判否优化前先分类

[[design-premises]] 前提一明写「per-item 跨界被边界成本吃光收益」是设计预期。最危险的反应是直接援引前提把一个性能 issue 判为「已知限制、无需修」。**前提能判否一个优化提案,但不能拿来掩护实现浪费。** 先 profile 定位成本来源,再把成本拆成两类:

- **架构成本**:NaN-boxing/arena/边界拷贝纪律带来的、前提二四项税决定的固定开销——这是设计选择,不该为它违背前提去优化;
- **实现浪费**:与架构无关的纯实现冗余——无论形态是否被前提判为非主推,都该消除。

**实例(issue8)**:`Call` 的 72 B / 2 allocs 表面像「边界成本=架构给定」,实为返回值**双拷贝**(VM 栈→inner slice→public slice,与 nret/脚本复杂度无关)的可消除冗余;`CallInto` 直切活动区零拷贝即消除,不触碰任何前提。

**判据**:收到「在 X 形态下比对标对象慢」时,先问「这笔成本是架构选择的必然,还是可消除的实现冗余?」。只有架构成本才适用「这是已知设计权衡」的回应——尤其当该形态是**首个目标宿主的真实热路径**且对标场景是已承诺的 drop-in 卖点时。

## 4. GC 根可达性——复用栈/共享 arena 返回的值,根还在不在

把内部缓冲区切片直接返回省分配、或让加速层持有指向共享 arena 的值时,**根可达性不能靠推理下结论,必须 stress 实测**。本码库的物理事实:值切片指向复用栈/arena,持有者复位后,只要存在一条常驻根链(如 `Call` 返回后 `runningThread` 复位 nil 但 `mainTh` 仍是同级常驻根),槽位值在 GC 下仍可达。

**实例(issue8 教训 2)**:`CallInto` 零分配直切 `th.stack[:nret]`,用 `SetGCStressMode(true)`(每分配点触发 GC)+ 复用 dst 循环 + string 返回值(经 arena)500 轮读出仍正确 = 无 UAF;配套覆写契约(返回值下次进 VM 前被覆写,godoc ⚠️ 标注)。

**判据**:任何「返回内部缓冲区切片以省分配」或「加速层持有共享 arena 值」的优化,GC stress 实测 + 覆写契约测试是上线前置,不是可选。与 `feedback_arena_view_aliasing` 同物理基础。

## 如何用

- **设计稿记号是语义契约,不是物理承诺**:`(call $x)`、`$base`、「成本=架构给定」都只声明「这里要什么语义」,从不声明本码库 physics(边界成本/段重定位/根可达性)。实现前逐条过 §1-§4。
- **解释器「能跑」≠ 加速层「能跑」**:二者刷新地址/管理生命周期的能力不同——解释器每访问经 `th.slot()` 现算,gibbous 的 `$base` 入口锁定中途无法自刷新。设计稿往往因解释器碰巧免疫而对危险盲视,加速层照搬就触雷。先问「谁有能力刷新、在什么时机」。
- **逐条对照,而非整体信任**:边界成本预算(§1)、段重定位(§2)、成本归类(§3)、根可达性(§4)是四个独立维度;一条热路径主张可能同时踩多个。

## 关联

[[issue8-boundary-cost-round]](家族奠基:实现浪费 vs 架构成本 + 零拷贝根可达性)· [[p3-pw5-table-ic-round]](边界成本维度:`$helper` 须按 ~143ns 预算重判)· [[p3-pw6-crosslayer-call-round]](内存物理维度:`$base` 须按 arena 段重定位重核)· `feedback_arena_view_aliasing`(arena=linear memory 段可重定位 / 偏移现算寻址,§2/§4 物理基础)· [[design-premises]](前提一边界成本论证 / 前提二四项税,§1/§3 成本根据)· `docs/design/p3-wasm-tier/02-translation.md` §3.4 · `04-trampoline.md` §2-§4
