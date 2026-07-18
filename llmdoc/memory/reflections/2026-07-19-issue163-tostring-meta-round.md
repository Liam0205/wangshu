---
name: 2026-07-19-issue163-tostring-meta-round
description: >
  2026-07-19 nightly-fuzz crasher 巡检轮(分支 fix/163-tostring-nonfunction-meta,
  PR #164)。一个真实 stdlib 语义 bug + 一个不可复现:① issue #163——
  `setmetatable({},{__tostring=0})` 后 print 该表,PUC 的 luaL_callmeta 把
  __tostring 字段推栈后直接 lua_call,非可调用值由调用机制报
  "attempt to call a number value",带 __call 的表则被正常调用;wangshu 的
  valueToStringMeta 用 `Tag(h) == TagFunction` 做类型过滤,非函数一律静默回退
  地址形式,verdict class 分歧。修法一行:任意非 nil 都经 ProtectedCallDirect
  调用,让既有调用机制决定。教训 1(模式):PUC 的元方法语义常是「把字段当可
  调用的东西直接调用,错误留给调用机制报」,类型过滤会同时吞掉应报错路径、
  误杀 __call 表路径。教训 2:写共享 string metatable 的 difftest corner 必须
  探完恢复(`__tostring = nil`),否则污染后续 corner 的 harness 自身 tostring。
  ② issue #162 为不可复现 concat storm 家族第 10 例(本地重放 4.6s 干净,
  worker 12.4M execs 处静默死,GOMEMLIMIT=512MiB 在场仍没接住);per-seed
  wall-clock 检测的优先级在上升,本轮未实施,待用户决定是否立项。
metadata:
  type: reflection
  date: 2026-07-19
---

# 2026-07-19 nightly crasher 巡检轮反思(#163/#162,PR #164)

> 范围:分支 `fix/163-tostring-nonfunction-meta`,PR #164。一个真实 stdlib
> 语义 bug(#163,非函数 __tostring 元字段)+ 一个不可复现 crasher
> (#162,concat storm 家族第 10 例)。

## 任务

处置 2026-07-19 nightly-fuzz 巡检报出的两个 crasher:

- **#163**:`setmetatable({},{__tostring=0})print(t)` 在 oracle diff fuzz
  撞出 verdict class 分歧——PUC 报 "attempt to call a number value",
  wangshu 静默完成并打印地址(真 stdlib 语义 bug);
- **#162**:concat storm 输入本地重放干净、worker 静默死亡(不可复现,
  分诊处置)。

## 期望与实际

### #163(非函数 __tostring 元字段)

- 期望:`tostring`/`print` 对带 __tostring 元字段的值,行为与 PUC 逐字节
  一致——包括元字段不是函数的情况。
- 实际:PUC 的 `luaL_callmeta` 把 __tostring 字段推栈后直接 `lua_call`,
  语义由调用机制决定:非可调用值(number/boolean/string)报
  "attempt to call a X value",带 __call 的表被正常调用。wangshu 的
  `valueToStringMeta` 用 `Tag(h) == value.TagFunction` 做类型过滤,非函数
  一律静默回退地址形式——「应报错的路径」被吞成回退,「带 __call 的表」这条
  非常规但合法的路径也被误杀。
- 修法(commit dc86490,一行语义修复):类型过滤改为「任意非 nil 都经
  `ProtectedCallDirect` 调用」,让既有调用机制决定——它已经会路由 __call,
  也会对非可调用值报正确的错。

### #162(不可复现 concat storm 家族,第 10 例)

- 输入:`function cat(i)return"<大量高位字节>"..i end out=""for i=1,700066 do
  dut=out..cat(i)end`。本地重放 4.6 秒干净;worker 在 12.4M execs 处
  "hung or terminated unexpectedly"(exit status 2),日志无 panic/OOM 栈,
  `GOMEMLIMIT=512MiB` 在场仍静默死。
- 与 #123/#144/#145/#150-#152/#156/#157/#159 同族,按
  [[unreproducible-crasher-triage]] 处置,corpus 不入库。
- 值得记录的观察:这个输入的写法里 `dut=out..cat(i)` 其实每次都从空 `out`
  拼接,单次结果不大——重放快是自然的;worker 死亡更可能来自 4 个 parallel
  worker 的叠加峰值或 mutation 后的变体,而不是这个 minimized 输入本身。
  家族计数已到 10 例,per-seed wall-clock 检测(前两轮反思提出的下一层
  诊断硬化)的优先级在上升,但本轮巡检未实施——需要用户决定是否立项。

## 踩坑与教训

### 教训 1:PUC 元方法语义常是「直接调用、错误留给调用机制」,类型过滤是错误的实现模式

PUC 实现元方法查询时,常见写法是把字段当可调用的东西直接调用
(`luaL_callmeta`:推字段、`lua_call`),错误由调用机制统一报。用「先检查
类型再决定用不用」的类型过滤去实现,会同时产生两类偏差:

- 「应该报错的路径」被静默吞成回退路径(#163 的 verdict class 分歧正是
  这个:oracle 报错,wangshu 正常完成);
- 「非常规但合法的路径」被误杀——带 __call 的表作为 __tostring 元字段在
  PUC 是可用的,类型过滤直接跳过它。

**How to apply**:实现或审查类似 __tostring 的元字段消费点(__index 的
函数分支、__call 链等)时,优先「非 nil 就交给调用机制」而不是「先验
类型」;如再遇到元方法相关的 fuzz 分歧,先怀疑类型过滤。修法也因此极小
——本例只改一行判断,把决定权还给 `ProtectedCallDirect`(它已经正确处理
__call 路由和非可调用值报错)。

### 教训 2:写共享 metatable 的 difftest corner 必须自己恢复现场

第一版 corner 写了 `getmetatable("").__tostring = 42` 探共享 string
metatable 的路径,探完没有恢复——共享 string metatable 被污染,后续
corner 的 harness 自己对 string 调 tostring 时就炸了,在 oracle 一侧以
harness 崩溃的方式暴露(比静默污染好,但仍是本轮唯一一次返工点)。修成
探完 `__tostring = nil` 恢复。

**Why**:string metatable 是整个 State 共享的,corner 之间在同一 State
里顺序执行,任何写入都会泄漏给后续 corner 乃至 harness 自身的输出路径。
`corners_test.go` 里的 strmt 系列早有「探完恢复」的先例,但一直没有明文
规则,这次是靠先例之外的新 corner 重新踩了一遍。

**How to apply**:任何 difftest corner 要写共享 metatable(string
metatable、全局表的元表等),同一段脚本内必须自己清理现场;写新 corner
前先看同文件里同主题系列(如 strmt)的既有写法。

## 流程

修复 → corpus 入库(`testdata/fuzz/FuzzOracleDiff/c3a6ed137f361957`)→
5 个 difftest corners → 三 build 全绿 → 60s FuzzOracleDiff smoke
(73k execs)→ arm64 交叉编译 → PR #164 → CI 绿 → bot 唯一小问题
(corner 命名 `number` 应为 `string`,commit 3a51685)已修 → 增量
APPROVE。本轮顺利,除教训 2 的 corner 恢复外无返工。

## Promotion 候选

- **教训 1**(类型过滤 vs 调用机制):可考虑并入
  [[cross-backend-semantic-fix-sweep]](它已有「PUC 语义由 C 实现定义」
  一节,与 PUC 分歧先 grep `_lua515/` 源码的纪律直接适用本例)或作为独立
  小节。但它属 stdlib 语义类,首次样本可暂留 memory,由 recorder 决定
  是否写入及落点。
- **教训 2**(difftest corner 共享状态清理):首次成文,暂留 memory。
  若后续再有 corner 污染共享状态的实例,可考虑在 difftest 相关文档里立
  明文规则。
- **#162 家族计数到 10 例**:per-seed wall-clock 硬化的优先级数据点,
  属 [[unreproducible-crasher-triage]] 的处置演进记录,暂留 memory;
  是否立项由用户决定。

## 触发场景

- 实现或审查元方法字段的消费点(__tostring/__index 函数分支/__call 链)
  时(教训 1:非 nil 就交给调用机制,别先验类型);
- 元方法相关的 oracle diff fuzz 报 verdict class 分歧时(教训 1:先怀疑
  类型过滤把报错路径吞成了回退);
- 写要改共享 metatable 的 difftest corner 时(教训 2:同段脚本内恢复
  现场,先看 strmt 系列既有写法);
- nightly concat storm 家族再报不可复现 crasher 时(家族已 10 例,
  GOMEMLIMIT 已证接不住,minimized 输入未必是死因本身——per-seed
  wall-clock 是下一层硬化,待用户决定)。

## 关联

[[cross-backend-semantic-fix-sweep]](教训 1 的落点候选,「PUC 语义由 C
实现定义」节的邻接实例)· [[unreproducible-crasher-triage]](#162 分诊
依据 + 家族计数)· [[2026-07-18-issue155-158-nightly-crasher-round]]
(concat storm 家族前序 + GOMEMLIMIT 没接住的第一次观察)· issue #163 ·
issue #162 · PR #164 · commit dc86490(语义修复)· commit 3a51685
(corner 改名)
