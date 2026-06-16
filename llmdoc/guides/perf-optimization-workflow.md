# Guide:性能优化工作流

> 适用:对解释器/VM 热路径做性能优化轮时(P1 已走通一遍;P2 编译层、P3 wazero 会再走同一流程)。
> 来源:`memory/reflections/2026-06-12-official-suite-perf-round.md`(六项落地 + 一项实测否决的实战样本)。

## 1. profile 先行——预判清单会被推翻

先跑 `-cpuprofile`/`-memprofile` + `pprof -top/-list`,再决定做什么。**不要按预判清单直接开工**:P1 性能轮的预判清单(GC pacing / args 分配 / thread 复用)没有包含最大收益项——closeUpvals 对 openUvs map 的 RETURN 每帧全量迭代占 20% CPU,是 pprof 直接暴露的意外发现(加 maxOpenIdx 快路径后 binary-trees -30%、fib -16%)。预判只用于准备 benchmark 场景,立项一律以 profile 数据为准。

## 2. 每项优化独立 benchmark 验证、独立提交

- 改一项测一项:`-benchtime 2s`,关键判断用 `-count=3` 看稳定性,不凭单轮数字下结论。
- 单域提交,commit message 带实测数字(P1 性能轮六个 perf 提交均如此)——数字进 git 史,回归时可逐项二分归因。
- 提交信息只引用仓库内可追溯的事实,不引用外部审查报告编号等仓库外位置。

## 3. 可疑优化的 benchmark 否决门

「理论更快」不等于实测更快——校验复杂度、分支预测、缓存行为会反噬。实例:IC DataOff 直达偏移理论上把 IC 命中的 4 次内存读砍到 2 次,实测 binary-trees 28.2→31.8ms(+12% 回退,3 轮稳定),整体 revert(归因:同键校验复杂度反噬 + gen 全局序列维护成本)。纪律:

- 任何优化(包括「显然合理」的)必须过 benchmark 门;
- 回退确认后 **revert 决策要快**,沉没成本不进判断;
- 否决结论与归因记入提交/反思,防止同一想法被反复重提。

## 4. 归因诚实

对外验收数字必须可归因。实例:simple 档 3.2x→9.0x 的主因是 State.Call 跨 Run 复用主 thread 消去了短脚本的固定开销(newThread/栈分配,275→98ns),**不是解释器本身快了 3 倍**——README 措辞如实标注。微基准倍率不外推到真实负载(realworld 首轮三项落后也诚实入库)。基准叙事失真比数字本身错误更难纠正。

## 5. 跨机器/跨参数 perf 基线对照

> 来源:`memory/reflections/2026-06-15-p3-pw10-zerocross-stage3-round.md` 教训 3。承 §1「profile 先行」/ §3「benchmark 否决门」配成 perf 判定纪律三件套。

任何性能数字判定**回归 / 收益** 前,必须**同 commit 同硬件同参数复测对照**——绝不拿 memory/reflection/implementation-progress 里的历史数字直接下结论。手法:

- 用 `git worktree add` 切到对比 commit、**同一机器同一 bench 参数(`-count=N -benchtime=Ts`)** 跑,3 分钟得到对照数据;
- 对照数据**写进本轮提交 / reflection**(同 commit 旧 ns / 新 ns 并列),不依赖外部「历史读数」。

**实例**(PW10 零跨界 ③ Stage 4):一开始误判 ③b 引起 table/mixed 回归(基线对比读数掉了 30+%),经 worktree 切 ③ 前 commit、同硬件 2s×3 count 复测,「无 ③ 基线」也同样掉了 30%——差异源自硬件 / bench 参数(本机 Xeon 6982P vs R3.5 当时机器),非 ③ 引入的回归;**对照内自洽** ⟹ ③ 真实收益是 loop +10%(crescent 5.61→4.96ms / gibbous 2.17→1.68ms,2.65x→2.95x)。

**次纪律——memory/reflection 写 perf 数字时必须标硬件/参数/日期**:本轮发现 `llmdoc/index.md` / `llmdoc/startup.md` / `docs/design/p3-wasm-tier/implementation-progress.md` 三处均缺标 ⟹ 历史读数被当成「现行基线」直接下结论。纪律:任何对外验收 / 对内对账数字三件套必标:① 硬件型号(CPU 系列)② bench 参数(`-count` / `-benchtime`)③ 日期(commit 哈希更佳)。

## 6. 池化/复用类优化的配套清单

衔接 `06-memory-gc.md` 回填条款(doc-gaps 第 10 项)与长稳轮「良性→致命」升级清单纪律,池化/复用类优化每项落地时同批完成:

1. **API 生命期契约入公共类型文档**(不能只在实现注释)——callHost 实参池的「args 仅本次调用有效」契约写入 `HostFn` 类型文档;
2. **debug 构建毒值防护**——wangshu_trace 构建下归还时填毒值,违约保留立即显形;
3. **相关传值区改拷贝**——coroutine xfer 不得持有池切片引用;
4. **归还时序**——defer 到消费完成之后(select 返回 args 子切片的场景);
5. 复用对象的**复位路径覆盖异常退出**(State.Call 复用主 thread 须清错误退出残留的 openUvs;死协程清 xfer 同一卫生标准)。

## 7. 立项数字目标 vs profile 实测瓶颈:profile 才是合同

> 来源:`memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md` 教训 1。承 §1「profile 先行」(立项侧)+ §3「benchmark 否决门」(单优化裁量侧)+ §5「跨机器基线对照」(时效性)+ 本节(落地中止损纪律)成 **perf 判定纪律四件套**。

立项时给出「数字目标 0.X x → ≥Y x」类指标(如 PW10 「call 核 0.52x → ≥1x」)是**方向陈述**,**不是合同**。落地中跑 cpuprofile / -benchmem 揭示「真实瓶颈所在 + 各块占比」才是合同。

**触发场景**:
- perf 里程碑落地中 profile 揭示「立项时假设的瓶颈块 ≠ 实测主导项」时;
- profile 揭示「立项时数字目标在事实上不可达」(已实测的不可优化项 + 仍可优化项的上限和已确认 <立项目标)时;
- /goal stop hook 强制不结束但 profile 证明数字不可达时(收口已落地子里程碑 + 文档化不可达边界,绝不硬上 UAF 代码追数字)。

**判据**:做下一步优化前问「**这步打 profile 里哪块?预估能消多少?**」若答「不打主导块,即使做完数字也不达标」⟹ **止损 / 落档 / 换路径**,别为追原数字硬上高 UAF 实现。

**纪律**:
- 立项数字目标在 profile 揭示其事实上不可达时,**先重评目标可达性,再决定继续/止损/换路径**;
- 不可达边界须**文档化为已知架构边界**(根因 + profile 证据 + 预估上限 + 触达条件),非作「未交付的失败」掩埋;
- 「已落地子里程碑 + 架构边界文档化」是合法的里程碑收口形态,绝不为追立项数字硬上高 UAF 实现。

**实例**(PW10 ④-ii):立项目标 `call 0.52x → ≥1x`。落地中 profile `/tmp/call.prof` 揭示:
- call 核 52% 在 enterGibbous + 38% 在 wazero CallWithStack(R3.5 已消反射装箱后的残留固定开销);
- 四 kernel 结构均为 `body → fn`,body 含 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)→ body 不可升 → 顶层升层(② callOnStack)+ ④ emitCall fast body **均对 bench kernel 无显著效果**(④ fast body 在不可升的 body 里无处发射);
- ④-ii fast body 即使完成,预估上限 0.57x(仅消 0.52x 中 ~10% 的 h_call 建帧延迟)**仍 <1x**;
- 实现复杂度 ~200 行 wasm 字节级 codegen,UAF 高(组帧 + 绑 ciTransferRef + IC mirror 写回 + 错误路径展开 + 跨守卫状态一致性)。

**决策**:**止损 / 文档化为「call 0.52x 是 bench kernel 结构性架构边界」+ 留 followup**,而非硬上 200 行 wasm 字节级 codegen 追不可达数字。emit 原语 i64.add/i64.or 已保留供未来 ④-ii(若 bench kernel 形态调整或 F2-b 口径扩张),不需重新打通。

**与四件套其他节的关系**:
- **§1「profile 先行」**(立项侧)管「立项时拿什么数据立项」;
- **§3「benchmark 否决门」**(单优化裁量侧)管「单项优化做完后实测不符就 revert」;
- **§5「跨机器基线对照」**(时效性)管「数字判定回归/收益前同 commit 同硬件复测」;
- **§7「立项数字目标 vs profile 实测瓶颈」**(落地中止损纪律)管「立项数字事实上不可达时的诚实收口」。

四节互锁:§1 防立项盲打 → §3 防单步过度乐观 → §5 防跨时空对照失真 → §7 防为追不可达数字硬上 UAF。

## 落点文件参考

- `internal/crescent/table.go` (`closeUpvals`) — maxOpenIdx 快路径。
- `internal/crescent/host.go` (`callHost` / `HostFn`) — 实参池与契约文档。
- `internal/crescent/state.go` (`mainTh`) — 主 thread 跨 Run 复用。
- `internal/gc/sweep.go` (`objectBytes`) — pacing 统计含附属块。
- `benchmarks/realworld/` — benchmark-game 五脚本(对拍 + vs gopher-lua);`benchmarks/baseline/` 三档微基准。
