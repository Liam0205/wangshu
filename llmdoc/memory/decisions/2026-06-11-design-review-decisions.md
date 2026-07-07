# 决策:设计评审决策轮(7 项裁决)

> **背景**:设计文档集全卷齐备后,主助理按「影响 × 不确定度」盘点设计期自行定下来但不确定的 7 项决策,与用户逐项裁决:4 项修订、3 项经确认维持。已全部落入设计文档(提交 d601fab / df32265 / 734c3b2 / a29bab9)。本文只存结论与落点指针,细节回源文档;过程反思见 `../reflections/2026-06-11-design-review-round.md`。

1. **【修订】验收 oracle 改为官方 Lua 5.1.5;gopher-lua 降为同生态参照+性能基准,其偏离官方处登记豁免。** 理由:「与 gopher-lua 逐字节一致」不可全量成立(pairs 序:gopher 是插入序,官方是哈希布局序)。落点:`docs/design/p1-interpreter/12-testing-difftest.md` §2.6/§3.3;roadmap §4 已同步。
2. **【维持】协程路线 B:单 goroutine + yield 信号冒泡**(经评审确认,非默认沿用)。落点:`docs/design/p1-interpreter/08-coroutines.md` §3。
3. **【修订】ColInt64 超界(|v|>2^53)抛运行期错误**,替代静默转 double。理由:静默丢精度对 ID 类数据(雪花 ID/纳秒时间戳)= 静默错果且差分测不出,违背贯穿原则 2 精神。落点:`docs/design/p1-interpreter/11-embedding-arena-abi.md` §3.3.2;12 口径表第 26 条。
4. **【修订】luac 同构降为软承诺**:寄存器分配与官方 luac 同构是追求目标非硬验收,边角难对齐登记「同构差异黑名单」不死磕;运行期输出差分仍硬门禁。落点:`docs/design/p1-interpreter/04-frontend-parser-codegen.md` §1.1。
5. **【维持】pairs 混合口径**:键集确定用例严格逐字节(JSHash/rehash/Brent/遍历序四环对齐 ltable.c),本质未定义用排序豁免(经评审确认)。落点:`docs/design/p1-interpreter/12-testing-difftest.md` §4.1。
6. **【修订·方向性反转】stdlib 默认面 = gopher-lua 的 OpenLibs 提供面**(原为「最小集+安全默认」;require/package 对齐 gopher 层级、os 默认完整含 execute/exit),以兑现 drop-in 宣称。同时新增**三层禁用机制**(用户追加的产品需求):LibsSafe 预设 / Libs 位掩码 / Exclude 函数级——收紧能力是 VM 责任,收紧决策是宿主责任。落点:`docs/design/p1-interpreter/10-stdlib.md` §4.7/§9.3/§12.1;`11-embedding-arena-abi.md` §1.2 Options。
7. **【维持】P3 协程不升层**;P3 开工前向首个宿主确认「列内核是否跑在协程里」。落点:`docs/design/p3-wasm-tier/07-coroutine-thread-rule.md`(§5.4 已扩展为该子文档)。
