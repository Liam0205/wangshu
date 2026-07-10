---
name: 2026-07-10-tier-admin-luajit-bench-round
description: >
  P4/P3 生产化三件套 + wangshu-vs-LuaJIT 同源基准四件套轮(2026-07-10,PR #115,分支
  feat/luajit-bench-p4-admin)。动机是用户问「P4-auto 放生产的信心」暴露的三个生产化欠账:
  可观测性全是 testing-only 全局 atomic 探针 / 无运行期降级手段 / 无嵌入方视角部署文档;
  LuaJIT 对比服务 roadmap §0「逼近 LuaJIT 档」的度量 + P5 立项判定数据。核心教训:
  ① Stuck 分因计数选「吸收态转移点埋计数器」而非「事后遍历 profileTable 反推」——
  CompileTried 在三条 Stuck 路径都置位,事后无法区分;② 测 Stuck 分类的载体必须过完
  force-all 的 64 次 retry window(considerPromotion 里 `pd.EntryCount < 64` 的显式
  return),载体只调 20 次不吸收;③ kill switch 语义测试三段式(promote→prove native /
  off→NativeRunCount delta==0 + byte-equal / on→resume + Promoted 不变)——最后一条
  额外把「重开不重编译」保住;④ 跨引擎 checksum(每 kernel 结果累进 + 末行输出,任
  一档分歧即退出非零)同时防两件事:静默错果 + 引擎 DCE 掉计时体;⑤ 生产 admin API
  与 testing-only API 的 godoc 标注纪律(SetTierEnabled 明标生产、SetForceAllPromote
  明标 testing-only)。另外踩了一次心算预期值坑(50500 vs 5050,10 次循环最后一次
  赋值不是累加)——build-neutral 测试的预期值要先跑一遍 P1 拿真值,别心算。
metadata:
  type: reflection
  date: 2026-07-10
---

# Tier admin API + LuaJIT 对比基准轮反思(2026-07-10,PR #115)

> 范围:分支 `feat/luajit-bench-p4-admin`。P4/P3 分层执行生产化三件套(运行期开关
> `SetTierEnabled` + State 级观测 `TierStatsSnapshot` + 嵌入方部署文档)+
> wangshu-vs-LuaJIT 同源对比基准四件套(engine-neutral bench.lua + benchlua
> 二进制 + 驱动脚本 + CI workflow)。

## 任务

用户问「P4-auto 放生产的信心」暴露三个生产化欠账,再叠一条 roadmap §0 的度量需求:

1. **观测**:仓库里所有 tier 相关探针都是 testing-only 的全局 atomic 计数器
   (`peroptranslator.NativeRunCount` 等),生产嵌入方没有任何「这个 State 里升了
   多少 proto、卡在解释器的 proto 是哪三类原因」的入口。
2. **降级**:生产上怀疑升层路径有问题时,当时能做的只有重建 State + 重新编译整
   个进程——不重启不能把这个 State 降回纯解释器。
3. **部署文档**:P3/P4 有一堆嵌入方视角的部署问题(seccomp 是否放行 mmap +
   mprotect、SELinux `deny_execmem`、内存开销量级、如何验证升层真的生效),没
   有集中的文档给嵌入方看。
4. **度量输入**:roadmap §0 说终局目标是「列内核 10-30x over gopher-lua,逼近
   LuaJIT 档」,但仓库里没有 wangshu-vs-LuaJIT 的同源对比表——P5 立项判定缺
   这个数据。

## 期望与实际

- 期望:三件套 + 四件套按图施工,SetTierEnabled 只是 Bridge 上加一个 bool + 三
  个入口 short-circuit,TierStats 遍历现有 profileTable 分类计数即可。
- 实际:观测半件套的分类办法一开始想错了(见教训 1);Stuck 分类测试的载体第
  一版调用次数不够(见教训 2);build-neutral 测试的预期值心算错了(见教训 3
  末尾)。核心结构一次通过。

## 教训 1:吸收态转移点分因计数,不要事后遍历反推

`considerPromotion` 里有三条通往 `TierStuck` 的路径:F1-F6 结构性排除(vararg /
coroutine / unknown call ...)、`PromotionGater` 收益门拒收(auto 模式)、
try-compile 抛错。三条路径都会置位 `pd.CompileTried = true`——第一版想「先
搭好数据结构,分类逻辑事后遍历 profileTable 推出来」,但 `CompileTried` 无法
区分这三类,`Compilable` 字段在收益门拒收时又是 `CompCompilable`(能编,只是
不划算),分类事实在转移点之后就丢了。

改法是在 `Bridge` 上加三个 int 计数器 `stuckNotCompilable` /
`stuckDeclined` / `stuckCompileFailed`,在三处 `TierState = TierStuck` 之后
各 bump 一个,`TierStatsSnapshot` 直接读:

```go
// (P2) 不可编译 → 永久解释
pd.TierState = TierStuck
pd.CompileTried = true
b.stuckNotCompilable++
```

这是通用手法:**吸收态转移点分因计数,而不是吸收态事后分类**——事后分类要
求「所有分类信号在到达吸收态之后仍然可读」,做不到时就要么每类给一个独立
字段(空间开销),要么在转移点埋计数器。转移点计数器天然按语义分组,不用
猜哪个字段还能读。

同源手法在 `PromotionCount` 上已经存在(直接 `len(b.gibbousCodes)` 而不是遍
历 profileTable 数 `TierState == TierGibbous`)——同样是「结果字段就在那里,
不用推导」。

## 教训 2:测 Stuck 分类的载体要过完 force-all retry window

`TestTierAdmin_StatsClassifyStuck` 想测 vararg proto(F1 排除)落进
`StuckNotCompilable`。第一版载体只调 vararg 函数 20 次:

```lua
for _ = 1, 20 do t = kernel(100) + vk(1, 2) end
```

测试挂了,`StuckNotCompilable == 0`。原因在 `considerPromotion` 里:

```go
if b.forceAll && pd.EntryCount < 64 {
    return // stay TierInterp; retry on a later entry
}
```

这是 issue #40 留下的 force-all 专属 warm-up retry window——IC-gated 后端
(binary-trees 的 `check` 第三个 GETTABLE 只有 depth-12 子树跑完才第一次执
行,大约 entry 14)需要给几十次机会让 IC 热起来才做升层决策,不能第一次
就把 not-compilable 判死。所以 vararg proto 在 force-all 下调用不到 64 次
根本进不了吸收态,永远停在 `TierInterp`。

改法是把外层循环调到 100 次,注释里把这个约束写清楚:

```lua
-- 100 calls: force-all's warm-up retry window keeps a not-compilable
-- proto in TierInterp until entry 64, so 20 would never absorb.
for _ = 1, 100 do t = kernel(100) + vk(1, 2) end
```

通用判据:**测「什么形状进哪个 Stuck 分类」的载体,调用次数必须过 force-all
的 retry window(当前 64),否则测的是 TierInterp 状态不是 Stuck 状态**。这
是 [[prove-the-path-under-test]] 家族在 tier 状态机维度的又一形式——绿灯
`stuckNotCompilable == 0` 分不清「vararg 真的没被吸收」和「vararg 还没热到
吸收决策点」,后者是本轮的实际情况。

## 教训 3:kill switch 语义测试三段式,NativeRunCount delta 是 prove-the-path 的正确探针

`TestTierAdmin_KillSwitchRoutesToInterpreter` 结构:

1. **promote → prove native**:force-all 一次 warmup + 一次真跑,断言
   `NativeRunCount` 相对 warmup 后有增长(否则载体没走 native,测试 vacuous);
2. **off → prove zero native delta + byte-equal**:`SetTierEnabled(false)`
   之后同一 State 同一 proto 再跑,断言 `NativeRunCount` delta 严格 == 0 且
   返回值 byte-equal 于前一次;
3. **on → resume + Promoted 不变**:`SetTierEnabled(true)` 之后再跑,断言
   native 恢复 delta 增长 **且** `TierStatsSnapshot().Promoted` 相对第 1 段
   末尾未变——这条把「重开不重编译」的语义额外保住(如果实现把 tierOff
   意外拆除了缓存表,Promoted 会掉到 0 后又重新长回来,`==` 断言直接抓)。

`NativeRunCount` delta 是「prove-the-path」的正确探针,不是绝对值:tier off
期间跑一个空脚本 delta 也是 0,单点断言 `== 0` 骗得过去;`before → off →
on` 三点各做一次 delta 断言才能证「off 时真的没跑 native,on 时又真的跑了」。

第三段的「Promoted 不变」是这次比较满意的设计——它把一个不在 issue 描述里
的隐性契约(重开不重编译,`gibbousCodes` map 不清)钉死;这类隐性契约在没
有断言时是「注释里说了但实现没保证」的高风险区。

## 教训 4:跨引擎 checksum 是廉价的双重防线

`bench.lua` 每个 kernel 的返回值都累进到 module-level `checksum`,末行 print
`checksum\t%.6f`。`bench-vs-luajit.sh` 取 P1 的 checksum 做 reference,任一
其他档(P4-auto / P4-force / LuaJIT)不一致就 `exit 1`。

它同时防两件事:

1. **静默错果**:某档结果算错但没抛错(比如某个 IEEE 边值路径挑了错分支,类似
   #103 那种)——checksum 一致性直接抓到,不需要靠对齐 stdout 数字或人肉
   diff 表格。
2. **引擎 DCE 掉计时体**:LuaJIT 的 trace 优化理论上可以把「返回值只累进到本
   地变量、外部不可见」的 kernel 整个消掉;checksum 是 module-level 的读写,
   trace 不敢跨过它做 DCE,累进+末行 print 强制每次 kernel 调用都有可观察副
   作用。

代价是每次 kernel 调用多一次浮点加 + 一次乘 `1e-9`(warmup 段)或直接加
(timed 段外累加),对 timed 段的每 iter cost 影响可以忽略(数量级差三到六
个数量级)。这是廉价高产的默认动作,值得沉淀。

## 教训 5(过程):build-neutral 测试的预期值要先跑一遍 P1 拿真值,别心算

`TestTierAdmin_SwitchIsSafeOnEveryBuild` 第一版预期值心算成 50500:

```lua
local function f(n) local s = 0; for i = 1, n do s = s + i end; return s end
local t = 0
for _ = 1, 10 do t = f(100) end
return t
```

想成「10 次 × sum(1..100) = 10 × 5050 = 50500」——但 `t = f(100)` 是**赋值**
不是累加,10 次循环最后一次的赋值才生效,答案是 5050。测试挂了才看出来。

通用判据:自计时 / 自校验脚本的预期值先跑一遍 P1(或其他任意已知正确的引擎)
拿真值,再回填断言;心算 lua 表达式很容易在 `=` vs `+=` 这类地方翻车,收益
太低,风险太高。

同源的做法在 `benchmarks/vsluajit/bench.lua` 的 checksum 上已经用了——所有引
擎输出对齐 P1 的 checksum,不去心算浮点累进的目标值。

## LuaJIT 对比的方法论(记录以备后查)

- 同一份 `.lua` 四档跑(P1 / P4-auto / P4-force / LuaJIT),没有 build-tag
  切换的 `if` 分支,「哪档」由 build/flag 决定不由 script 决定;
- 每个 kernel 是 non-vararg 函数(vararg 顶层 chunk 永远不升层,`bench.lua`
  §Methodology 注释解释);
- WARMUP 300 次同时覆盖两件事:wangshu 自然热度升层(entry 阈值 200)与 LuaJIT
  的 trace 录制,timed 段之前两个引擎都到稳态;
- 固定 iteration count(不 calibrate),所有引擎测同一份工作量,报告 per-iter
  cost;
- `os.clock` 在 LuaJIT 是 CPU 时间,在 wangshu 是 wall 时间——对单线程 busy
  loop 等价,这个差异需要在文档里显式记录;
- 共享机纪律:四档串行 run,一次只占一个核(feedback
  `shared_machine_resource_limits`)。

## Promotion 候选

- **教训 1(吸收态转移点分因计数 vs 事后分类)**:首次样本,暂留观察。若 P5
  trace JIT / 或其他状态机再撞同样的问题(多路径进同一吸收态、事后无法区分)
  可升 guide,聚合到 `backend-capability-vs-profitability` 或独立立
  「absorbing-state classification 」guide。
- **教训 3(kill switch 三段式 + Promoted 不变作重开不重编译断言)**:首次样
  本,暂留观察。若后续加其他生产 admin 开关(比如「暂停编译但保留已编译产
  物」)可复用同样三段式。
- **教训 5(自校验脚本预期值先跑再回填)**:process-level 小教训,暂留观察,
  第二实例可考虑并入 [[prove-the-path-under-test]] §4 覆盖度节。
- **另议:生产 admin API vs testing-only API 的 godoc 标注纪律**——本轮
  `SetTierEnabled` godoc 明写「production admin API」,`SetForceAllPromote` 沿
  用旧的「testing-only」标注,`SetHotThresholds` 沿用「testing-only」。这个纪
  律目前只在 tier 相关三个入口上,是否值得沉为 guide 由主线程判断,不强求。

## 过程记录

- 三件套 + 四件套 12 commits 单会话完成;
- `TestTierAdmin_StatsClassifyStuck` 第一版挂了两次(20 次不够;然后一度想调
  `SetHotThresholds(1, 1)` 绕过 retry window,但那样测的是 hotEntry 路径不是
  Stuck 路径,回退);
- LuaJIT bench 表格因本机没 luajit 二进制,只跑了 P1 / P4-auto / P4-force
  三档,LuaJIT 列留 n/a;CI workflow `.github/workflows/bench-vs-luajit.yml`
  在有 luajit 的 runner 上会填满;
- `docs/embedding-tiers.md` 是新文件,从零起草;三节结构(选层 / P4 部署要求 /
  运行期开关 / 观测)对齐嵌入方从下决策到线上跑的顺序。
