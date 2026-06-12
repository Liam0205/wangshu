# 完整性补全轮(特性探测 corpus + 25 缺口修复)

- **日期**:2026-06-12
- **任务类型**:差分测试结构性盲区修补(特性探测 corpus)+ 特性面补全(元方法/base 库)+ codegen 语义修复 + CI 维护

## 任务

用户指出结构性问题:**「官方有而我们没有的功能,diff-fuzz 测不出来——随机生成器
跟着实现走,只会生成已实现的特性;若不修,diff-fuzz 是假的」**。本轮回应,提交序列:
`98b6805`(FuzzPattern 灾难性回溯 → 回溯预算有界失败)→ `590d8b3`(特性探测 corpus
上线 + 元方法面补全 + goIfTrue/goIfFalse 语义修复)→ `c76adeb`(base 库补全)→
`509a2f5`(生成器三期 15→19 类语句)→ `1379319`(actions 升 Node 24 线 + 三 fuzz
目标输入尺寸上限)。

corpus 落点 `test/difftest/probes_test.go`(`featureProbes` / `TestDiff_FeatureProbes`),
按官方 5.1 手册逐节组织(§2.2 值/§2.4 语句/§2.5 表达式/§2.8 元表全 17 事件/§5 各库/
协程/闭包),实测 100 项(`590d8b3` 提交说明口径 93,为撰写时点数字;以文件实测为准),
现全绿常驻对拍。

## 预期 vs 实际

- 预期:probe corpus 是验证性动作,可能扫出少量边角缺口。
- 实际:**在 570+ 随机脚本对拍全绿的状态下,probe 一次扫出 25 个完整性缺口**——
  整个 __call/__eq/__lt/__le/__tostring/__metatable 元方法面、loadstring/load、
  select 负索引、tonumber(s, base)、gsub 锚点语义、table.maxn 全部缺失,外加一个
  codegen 语义 bug(`nil and 2` 错产 false)。「生成器跟实现走」的盲区是真实且大面积的。

## 做对了什么(可复用模式)

1. **差分 fuzz 的两个正交轴(本轮核心认识)**:随机生成器测「已实现行为的正确性」,
   但其文法只覆盖已实现子集,对「缺特性」结构性失明;特性探测 corpus 按**官方手册**
   (而非自家实现)逐节写,测「特性面的完整性」。两轴正交、互不替代——只有生成器的
   diff-fuzz 对缺特性是假防线,本轮 25 缺口全部在生成器全绿状态下被 probe 一次扫出。
   与测试加固轮「fuzz 目标空转是虚假安全感」同族:**防线的参照系必须是外部规范,
   不能是自身实现**。
2. **probe 上线后的护栏闭环**:新特性补完 → probe 转绿 → 生成器三期把新特性编入
   随机文法(`test/difftest/generator.go` 新增 metaOperators/metaCallable/
   loadstringStmt/tostringMeta,15→19 类)→ 滚动种子持续压行为正确性。probe 管
   「有没有」,生成器管「对不对」,缺口修复后立即让两轴都覆盖到新特性。
3. **SetCompileFn 回调注入解依赖**:loadstring/load 需要在 stdlib 里调编译器,但
   crescent 不能反向依赖 frontend——`wangshu.go` 装配层注入编译回调
   (`internal/crescent/state.go` `SetCompileFn`),依赖方向保持单向。
4. **元方法对齐到 5.1 源码语义而非手册文字**:__eq 按 get_compTM 语义(两边 handler
   是**同一函数**才触发,不同 handler 直接 false);__le 缺失时回退 `not (b < a)`
   (5.1 特有);__call 三入口(doCall/doTailCall/callLuaFromHost)一致处理
   args 右移一格。手册不写这些细节,只有照 lvm.c 才能逐字节一致。

## 什么出了问题 / 根因

1. **生成器结构性盲区(本轮动因)**:收尾轮起生成器文法就是「按我们实现了什么写」,
   每期扩文法也只把**已实现**特性编进去——参照系錯了,570+ 脚本全绿只证明
   「已实现子集内正确」,对特性面完整性零覆盖。根因:把「差分 fuzz 全绿」误读为
   「与官方一致」,漏掉了「输入分布本身偏向自家实现」这层。
2. **codegen goIfTrue 把 VNIL 特判「恒跳」丢原值(probe_and_or_values 捕获)**:
   `nil and 2` 错产 false 应为 nil;goIfFalse 对 VK/VKNUM 同样错产 true 应保留原值。
   根因:把「常量真值已知」当成「可走 LOADBOOL 恒跳」,但 and/or 是**取值**运算——
   只有短路值恰为布尔的 VFALSE/VTRUE 可恒跳,其余必须落 TESTSET 保值
   (对齐 luaK_goiftrue/goiffalse;修复见 `internal/frontend/compile/expdesc.go`
   `goIfTrue`/`goIfFalse`)。又一例「lcode.c 同构必须到 helper 层」级别的语义细节。
3. **probe 笔误两例——probe 必须先过 oracle 再当判据**:probe_assert_message 直接
   对拍 assert 错误值,但错误串含 chunkname/行号(两端必不同),须 gsub 归一化;
   probe_string_format_misc 写了 `format("%s", nil)`,5.1 本就报错,不是合法探测。
   教训:**corpus 的每一项先在 oracle 单跑确认是合法且确定性的 5.1 程序,才有资格
   当完整性判据**——否则 probe 红色会被误读成实现缺口。
4. **FuzzPattern 灾难性回溯挂死 CI(`98b6805`)**:`.*.+%A*` 类 pattern 指数回溯,
   CI fuzz-smoke 直接 hang。裁量:纯 Go 实现选择**回溯预算有界失败**
   (`internal/stdlib/pattern.go` `maxMatchSteps` = 2^20,超限报 "pattern too
   complex"),而非 C 官方的「真跑很久」——嵌入式 VM 的 fuzz/宿主不可挂起,偏离
   官方行为是有意裁量,已在源码注释记录(预算横跨全部起点共享,逐起点重试不重置)。
5. **fuzz 超大输入在 fuzztime 截止边缘超时 flake(`1379319`)**:fuzz 引擎变异出的
   超大输入恰在 -fuzztime 截止前开跑 → 单用例超时报失败,非真 bug。修复:lex/parse/
   端到端三目标加输入尺寸上限(超限 t.Skip)。CI fuzz 目标要同时设「时间预算」与
   「单输入尺寸预算」两道闸。

## 缺失的文档或信号

- `docs/design/p1-interpreter/12-testing-difftest.md` §3 只设计了随机生成器轴,
  **没有「特性探测 corpus」机制**——「生成器参照系偏向自家实现」这一盲区在设计期
  未被识别,由用户在交付后指出。值得回填两轴正交模型。
- doc-gaps 原「Node 20→24 迁移期」条目写「2026-09 前升即可」,本轮顺手提前完成
  (checkout v6 / setup-go v6 / upload-artifact v7),待办已关闭。

## Promotion 候选

- **回填设计文档(并入 doc-gaps 合并轮,recorder 执行)**:12 §3 增补「特性探测
  corpus」机制——两轴正交模型(生成器=行为正确性/probe=特性面完整性)、corpus
  按官方手册逐节组织、probe 先过 oracle 纪律、新特性「probe 转绿 + 编入生成器文法」
  闭环。已登记 doc-gaps 回填待办第 8 项。
- **暂留 memory**:「防线参照系必须是外部规范」纪律(与加固轮「防线上线验收 = 抓到
  过 bug」合并观察,P2 接新执行层时若再用一轮,合并升 guides);pattern 有界失败
  裁量(源码注释已记录,涉及官方行为偏离的裁量清单若再增两例,考虑立 reference)。

## 后续行动

- P2 开工前 recorder 执行 doc-gaps「设计文档回填待办」八项合一轮(本轮新增第 8 项)。
- P2+ 每个新执行层接入时,probe corpus 与生成器、GC 双模式对照同批换内核重跑
  (corpus 本身执行层无关,harness 直接复用)。
- 5.1 手册仍有未入 corpus 的小节(io/os 环境相关、依赖宿主的不确定输出),如后续
  发现可确定性对拍的子集,增量补 probe;新增项一律先过 oracle。
