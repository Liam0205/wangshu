# 官方测试套与性能轮(lua-5.1-tests 移植 + realworld 基准 + P1 性能轮)

- **日期**:2026-06-12
- **任务类型**:官方测试套移植(第四轴防线)+ 真实负载基准 + 审查遗留核销 + profile 驱动性能优化

## 任务

继长稳与审查核销轮后,提交区间 `3f42b1a..0302f7d` 共 27 个提交,三块:
① **官方 Lua 5.1.5 测试套移植**(`1a7ea7b..2cb8770`,后续 `b0336f9` 补 gc.lua)——
`test/luasuite/` 新包,lua.org 官方 lua-5.1-tests 14 个文件入库(testdata 与官方
逐字节一致),vararg/sort/pm 整文件过,其余截断到「豁免线」(stopAt 表:setfenv/
debug/io 对象/setlocale/string.dump/require,均对应既有豁免注册表条目);移植过程
扫出 **20 项真实分歧**并全部修复。② **realworld 基准**(`d220b64`)——
`benchmarks/realworld/` benchmark-game 五脚本(fib/binarytrees/spectralnorm/
fannkuch/nbody),双用途:返回值对拍官方 lua5.1(TestRealWorld_OracleParity)+
vs gopher-lua 基准。③ **审查 13-15 轮遗留 5 项核销 + P1 性能轮**(`f19bcc6..0302f7d`)
——profile 驱动六项优化落地、一项(IC DataOff)实测回退被否决 revert。落地明细见
implementation-progress.md,本文只记过程教训。

## 预期 vs 实际

- 预期 1:三轴防线(fuzz/probe/review)全绿后,官方测试套应是确认性动作。
  实际:**20 项真实分歧**,含 2 个 codegen 级缺陷(break 漏发 CLOSE、stmtReturn
  短路挂死)与 1 处架构性重写(describeReg 倒序回看 → 正向 symbexec)——官方套
  是与前三轴正交的**第四轴**。
- 预期 2:性能轮预判清单是 GC pacing / args 分配 / thread 复用。实际:profile 的
  第一发现是**完全不在预判清单的** closeUpvals——RETURN 每帧 map 全量迭代占
  20% CPU(`a1a1ad7` 加 maxOpenIdx 快路径,binary-trees -30%、fib -16%);而预判
  合理的 IC DataOff 直达偏移(理论上 IC 命中 4 次内存读砍到 2 次)实测 binary-trees
  28.2→31.8ms(+12% 回退,3 轮稳定),整体 revert。
- 预期 3:微基准倍率(2.3-3.2x)可外推到真实负载。实际:realworld 首轮五项中三项
  落后(binary-trees 0.77x、fannkuch 0.83x);性能轮后四项反超(fib 1.31x、
  binary-trees 1.09x、spectral-norm 1.43x、nbody 1.08x,fannkuch 0.82x 剩余短板),
  微基准升到 simple 9.0x / arith 7.0x / loop 2.45x。

## 做对了什么(可复用模式)

1. **四轴防线模型(本轮核心认识,升级长稳轮的三轴)**:fuzz(已实现行为自洽)→
   probe corpus(特性面完整性)→ 外部审查(规范同构+形态组合)→ **官方测试套
   (作者写的语义断言,含负断言与跨特性组合)**。每加一轴都掏出前几轴全绿下的新
   bug(本轮 20 项)。第四轴的独有能力是**负断言**:errors.lua:75 断言错误消息
   **不得**含 'aaa'(`(aaa or aaa)()` 经 TESTSET 合流,官方放弃命名)——前三轴的
   正向对拍只能验证「该给的给了」,结构性测不出「不该给的没给」。
2. **棘轮机制**:未登记 stopAt 的文件 fatal、豁免线只许前移、testdata 与官方逐
   字节一致(不许为通过而改测试)——官方套不是一次性验收,是单向收紧的常驻防线。
   豁免线逐项对应既有豁免注册表条目,可审计。
3. **profile 先行 + benchmark 否决门**:六项优化全部由 profile 数据立项(closeUpvals
   20% CPU、args 占 nbody Go 堆分配 91%、GC pacing 附属块口径不对称导致常驻大表
   低估几个数量级);预判合理的 IC DataOff 也必须过 benchmark 门,实测回退即弃
   (3 轮确认,归因:同键校验复杂度反噬 + gen 全局序列维护成本),**revert 决策快,
   沉没成本不影响判断**。「理论上少两次读」不等于实测更快。
4. **归因诚实**:simple 3.2x→9.0x 的主因是 State.Call 跨 Run 复用主 thread
   (`b745bc5`,短脚本 newThread/栈分配占比极高,275→98ns),不是解释器本身快了
   3 倍——README 措辞如实;realworld 首轮三项落后也诚实入库(「微基准不外推」)。
   基准数字的叙事失真比数字本身错误更难纠正。
5. **池化配套契约(上轮「内存复用配套清单」纪律的再执行)**:callHost 实参池
   (`6b7c870`)配套三件事同批落地——HostFn 契约(不得越过调用保留 args)写入
   **公共类型文档**(`e5b72ab`)、coroutine xfer 改拷贝、select 返回子切片的归还
   时序(defer 到结果拷贝之后);State.Call 复用主 thread 配套复位路径处理错误退出
   残留 openUvs;死协程清 xfer(`0302f7d`)与池归还同一卫生标准。

## 什么出了问题 / 根因(教训)

1. **stmtReturn 短路挂死(`cbaae3f`)排查曲折,根因是「快路径家族」漏审计**:
   `return (not cond) and msg` 的 eNonReloc 快路径漏 hasJumps() 检查,悬空 JMP
   自旋挂死。排查难点在**非确定性触发**——同一 errors.lua 时过时挂(取决于寄存器
   形态),前后用了 head 二分、SIGQUIT dump、指令预算定位三种手段才锁定 errors.lua:7
   的 doit。事后发现 exp2AnyReg **早有正确写法**——这是「eNonReloc 快路径漏
   hasJumps」家族的最后一处。教训:**同一形态的快路径在代码库里有多处,修一处时
   应 grep 全家族**;家族审计当时做了,这类 bug 不会潜伏到官方套才暴露。
2. **describeReg 启发式近似被负断言现形**:旧实现「倒序回看 8 条指令」跨过 JMP
   误归因,正向对拍多轮全绿,直到 errors.lua 负断言抓出。重写为正向 symbexec
   (`70349b1`,官方 ldebug.c 同构)。教训:**调试辅助路径同样须同构**,启发式
   近似在「不该输出却输出」的断言下必然现形——与长稳轮「同构到时序层」同族,
   这次是「同构到调试器层」。
3. **形态猜测 vs 精确事实源(symbexec CLOSURE 伪指令数,`9bda05e`)**:旧实现按
   形态猜测 CLOSURE 后的伪指令数,会吞掉 0-upvalue CLOSURE 后的真实指令;修复是
   给 Proto 加 SubNUps 字段——让事实有单一来源,不靠猜。与长稳轮「对象尺寸单一
   事实源」同构。
4. **过程纪律三项(均为用户纠正,务必记住)**:
   - **commit message 不得引用审查报告编号/位置**——`.code-review` 不入 git,
     「说事实就行,不然以后根本看不懂」。文档与提交信息只引用仓库内可追溯的事实。
   - **单域提交、提频**(同一轮被提醒两次)——大改动按域拆分即时提交,不积攒。
   - **一次误提交**:把用户正在编辑的 reflection 草稿带进了 commit(用户:「别带它,
     同时在写文档呢」),git rm --cached + amend 摘除。提交前 git status 审视
     untracked/暂存列表,**正在协作编辑的文件不带**。
5. **临时探针 runner 覆写事故**:/tmp/dr/main.go 同时被用作多种探针 runner,反复
   覆写导致旧二进制误导判断(gc.lua 修复后跑出旧结果,险些误判修复无效)。临时
   探针应分目录隔离,或重建后立刻验证二进制时效。

## 缺失的文档或信号

- doc-gaps 回填待办第 8 项的防线综述措辞停留在三轴,缺第四轴(官方测试套)的
  定位陈述与负断言价值。
- `04-frontend-parser-codegen.md` 同构纪律已有「helper 层」「时序层」两维度
  (doc-gaps 第 1、9 项),缺「快路径家族审计」——修 hasJumps 类检查时 grep
  同形态全家族,`cbaae3f` 是现成反例(exp2AnyReg 对、stmtReturn 漏)。
- 性能优化工作流无文档:profile 先行、预判清单会被 profile 推翻、可疑优化
  benchmark 否决、归因诚实——本轮六项落地 + 一项否决,样本已够。
- commit 卫生(可追溯引用、单域提频、协作文件不带)无处挂靠——engineering.md
  有 hooks 机制但无提交信息内容纪律。

## Promotion 候选

- **回填设计文档(并入 doc-gaps 既有清单,recorder 执行)**:
  ① 第 8 项防线综述措辞由三轴升级为**四轴**(官方测试套=作者语义断言,独有
  负断言能力 + 棘轮机制:stopAt 只许前移、未登记 fatal、testdata 逐字节一致);
  ② 04 同构纪律追加「**快路径家族审计**」维度(引 `cbaae3f` 反例),与 helper 层、
  时序层并列;
  ③ 第 10 项「内存复用配套清单」追加池化条目:**资源池化的 API 契约必须出现在
  公共类型文档**(HostFn args 生命期,`e5b72ab`),不能只在实现注释——与「注释
  承诺审计」呼应:契约位置决定使用者能否看见。
- **`guides/` 候选**:「性能优化工作流」(profile 先行 / 预判会被推翻 / benchmark
  否决门 + 快 revert / 归因诚实 / 池化配套契约)——本轮样本充分,且 P2 编译层、
  P3 wazero 都会再走同一流程,建议立项;若 recorder 评估暂缓,P2 性能轮再用一遍
  后合并升级。
- **暂留 memory**:commit 卫生三教训(行为纪律,一句话级,先在 memory 观察是否
  复发);/tmp 探针覆写事故(一句话教训,已录本篇);非确定性挂死的三段排查手段
  (head 二分 / SIGQUIT dump / 指令预算定位,下次再用时考虑入 guides)。

## 后续行动

- recorder 更新 doc-gaps:第 8 项三轴→四轴措辞、04 条目追加快路径家族审计、
  第 10 项追加池化契约位置条款;评估「性能优化工作流」guide 立项。
- fannkuch 0.82x 剩余短板留待后续性能轮(已诚实入库,不催熟)。
- P2 新执行层接入时,官方套棘轮与 probe/生成器/GC 双模式同批换内核重跑;豁免线
  只许前移,新执行层不得新增豁免。
- 后续所有提交:信息只引用仓库内可追溯事实;按域拆分即时提交;提交前审视暂存
  区不带协作中文件。
