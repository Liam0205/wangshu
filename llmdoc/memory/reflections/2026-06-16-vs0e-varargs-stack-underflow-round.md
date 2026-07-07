# VS0-e 收口轮:varargs 进栈下区(M14 留口完成,P3 PW10 完整收尾)

承 [[p3-pw10-architectural-ceiling-round]] PW10 收口轮后,task list 唯一剩 task #6 VS0-e 待办——延后 7 个月的「协程栈 + varargs + CallInfo word 位打包 全 arena 化」对称投资。本轮单会话四子步完成,把最后剩的 varargs 进栈下区做完。

**调研发现的关键事实(动手前重核)**:VS0-e 任务描述三项里**两项已隐性交付**——协程栈早随 VS0-c(主线程 + 协程统一经 `st.newThread()` 进 arena)完成,callInfo word 位打包早随 PW10 R2(4 word/帧 + packCIWord2)完成。**真剩的只有 varargs 这一项**,而它的设计描述早在 `docs/design/p1-interpreter/05-interpreter-loop.md §8.5` 就明确「多余的 (nargs - NumParams) 个 vararg 搬到 newBase 之下(funcIdx 与 base 之间的负区)」——**官方 Lua 5.1 栈下区真布局**,M14 简化成 Go `ciVarargs` 影子,VS0-e 才是设计文档的真正完成。

本轮 7 子步(②~⑥ 实际合并为 4 提交)+ ⑦ 文档同步:① callInfo 加 nVarargs 字段(零行为变更基建,`c22798b`)→ ② ciWords 4→5 + word4 nVarargs 镜像 + wasm 端 ciFrameBytes 32→40 同步(`4e50687`,最危险的同步翻转,通过 verifyCISeg + V1-V13 双轴)→ ③ enterLuaFrame base 重排 + 固参/vararg 三步搬移(`966318c`,base 重排雷区 = VS0-c R5/R6 同档,通过 luasuite + V1-V13)→ ④ doVararg 读栈下区 + 退役 ci.varargs/ciVarargs/三 API(`ed95020`,GC 扫栈 [0, top) 自然覆盖 vararg ⊆ [funcIdx+1, base) ⊆ [0, top))→ ⑤ GC 扫栈区间收口(并入 ④)→ ⑥ e2e difftest vararg 全形式 + 协程对称验收(luasuite/closure.lua 已含 NeedsArg + vararg + 协程多值 yield/resume 最复杂组合,**无需再加冗余语料**)→ ⑦ 文档同步。

**教训**:

**① 调研先于实现:task 描述失实是常事,7 个月延后的任务最该先重核**。task #6 描述「协程栈 + varargs + CallInfo word 位打包 全 arena 化(延后)」写于 P2 时代,**两项已隐性被 VS0-c + PW10 R2 顺手收掉**,但 task 描述没更新。如果直接照 task 描述开干,会重复实现协程栈 arena 化 + 重新设计 word 位打包,2-3 小时全是无效工作。**纪律**:接延后 ≥6 个月的 task 前,**先对 task 描述每一项做现状重核**(grep 字段/调用方/已交付里程碑的设计文档),把实际剩余范围圈出来再开工。本轮 1 小时调研把范围从「2-3 小时高 UAF」缩到「2 小时低 UAF」。承 [[p3-pw7-pw4b-closure-tforloop-round]] 教训 1「设计稿『难点』可能已被前序里程碑解掉」的对偶面:**task 列表的延后项也可能已被前序里程碑解掉,不只是难点会过期,工作量本身也会**。

**② 设计文档比 task 描述更权威——设计稿是规范源,task 是实施单**。task #6 的「varargs 迁 arena 栈下区」措辞含糊(arena 段?栈下区?),但 P1 设计文档 `05-interpreter-loop.md §8.5` 早就明确「vararg 搬到 newBase 之下(funcIdx 与 base 之间的负区)+ CallInfo 记录 vararg 起始与个数」——这是官方 Lua 5.1 lua_State 布局,与 task 措辞一致。**实施时按设计文档而非 task 描述**:从 `enterLuaFrame` 重排 base + 栈下区 + 退役 Go ciVarargs,不引入「varargs 单独 arena 段」(选项 B,task 措辞的另一种解读)。承 [[design-claims-vs-codebase-physics]] 「设计稿主张须对本码库 physics 重新验证」的延伸:**设计稿主张被前序里程碑实现细节验证后,task 描述若与设计稿冲突,以设计稿 + 已完成实现为准**——M14 时 ciVarargs 是 Go slice(简化版),VS0-e 时设计文档 + 官方 5.1 真布局是规范,两者一致。

**③ ciWords 4→5 段布局扩张 = PW10 ③ 收口前曾撞过的 fork,有解就别绕**。memory `[[project_pw10_zerocross_milestone]] §"fork 已解(2026-06-15)"` 记载:PW10 ③ 收口前曾因 caller MaxStack 扩 word4 而要扩张段布局,后用「caller 自恢复 top」绕开。VS0-e 时 nVarargs 必须进段(callee 在 popCallInfo 时 caller 帧 ci 从段重建,nVarargs 必须从段读),**没有一样的绕开法**。但 PW10 已经把段步长改动的 wasm 端单点(`helpers_index.go ciFrameBytes`)定位清楚,本次扩张 4→5 只需同步两处常量(`state.go ciWords` + `helpers_index.go ciFrameBytes`)+ verifyCISeg 加 nVarargs 比对,**实际改动面极小**(15 行)且通过 V1-V13 byte-equal 一次通过——因为 wasm 端 ③b emitReturnFast 段地址寻址用的是 `i32Const(ciFrameBytes)`,**常量更新自动传播**,这是 PW10 时刻意预留的可扩张点。**教训**:**前序里程碑的 fork 决策如果是「绕开」,要顺手把绕开后剩余的扩张点定位清楚**,未来真要扩张时改动面小一个数量级。承 [[p3-pw10-r1-r2-callinfo-migration-round]] 教训 2「物理数据结构迁移收口→只写影子→翻转→退役」同源:本次 4 子步 = 缩小版剧本(子步 ① 零行为变更基建 + 子步 ② 只写镜像 + 子步 ③ 翻转 + 子步 ④ 退役)。

**④ 既有官方 5.1 luasuite 覆盖度比手写语料强 N 倍——子步 ⑥ 直接验证 luasuite 通过即收口**。子步 ⑥ task 描述列了 11 条 vararg 形式语料(空 vararg / select / NeedsArg / pcall / coroutine.yield 多值 / ...),写下来要 50+ 行 Go 测试 + Lua 脚本。但 `test/luasuite/testdata/vararg.lua` 是 PUC 5.1 官方 vararg 全套移植,`closure.lua` 已含 `-- tests for multiple yield/resume arguments` 节(`{coroutine.yield(unpack(arg[i]))}` 是 NeedsArg + vararg + 协程多值 yield/resume + 解包 _G.x 最复杂组合）。luasuite 全 14 文件全 PASS = vararg 行为与官方 5.1 字节级一致,**这比手写 11 条语料权威 N 倍**(因为是 5.1 作者本人写的 vararg 测试)。**纪律**:加 e2e 语料前先 grep 既有 oracle 测试套是否已覆盖,若已覆盖直接验证它过即可,**不写冗余语料**——`prove-the-path-under-test` 家族的「覆盖度先验证再补」对偶面。

**触发场景**:接延后 ≥6 个月的 task 前(先重核每一项现状,描述可能失实)、task 描述与设计文档冲突时(以设计文档 + 已完成实现为准)、需要扩段布局/word 时(grep 既有 ciFrameBytes 类常量定位单点,改一处自动传播)、加 e2e 语料前(先 grep 既有官方 5.1/PUC 测试套是否已覆盖,避免冗余)。

**commit 链**(VS0-e 子步 ①~④):`c22798b`(① nVarargs 字段基建)→ `4e50687`(② ciWords 4→5 + word4 + ciFrameBytes)→ `966318c`(③ enterLuaFrame base 重排)→ `ed95020`(④ doVararg 读栈下区 + 退役 ci.varargs/ciVarargs)。
