---
name: 2026-07-14-issue133-argerror-caller-name-round
description: >
  2026-07-14 定时巡检补跑轮(分支 fix/argerror-caller-name,PR #134,CI 全绿)。
  处理 nightly 差分 fuzz 报的 issue #133(corpus e8534c580042ec44:
  `co=coroutine.create(coroutine.resume)print(coroutine.resume(co))`),真实分歧
  有两个独立根因:① luaL_argerror 的函数名派生规则——PUC 5.1.5 的 "bad argument
  #N to 'name'" 中 name 来自【调用方的调用点】(ldebug.c getfuncname → getobjname
  对 CALL/TAILCALL/TFORLOOP 的 A 操作数做 symbexec),不是被调函数身份;wangshu
  之前在每个 stdlib 站点硬编码被调函数名。修法是结构化 NewArgError(narg, extra)
  + State.resolveArgError 在 Lua 调用边界统一改写(doCall/doTailCall/TFORLOOP,
  解释器与 gibbous 对称),callLuaFromHost wrapper 在宿主调用边界冻结 '?'。
  ② coroutine.create 必须拒绝 C 函数("Lua function expected",luaB_cocreate
  的 lua_iscfunction 检查)。顺手同步约 84 个 stdlib 站点的检查顺序与措辞。核心
  可复用教训:① 修「面级规则」类分歧前先用探针套把 PUC 完整行为面测绘成对照表再
  一次性实现;② 错误消息依赖抛出点拿不到的上下文时,用「结构化错误字段 + 拥有
  上下文的边界层统一改写」而不是把上下文穿透传给每个抛出点;③ 跨行结构的机械
  迁移不要用单发多行正则,先收窄到单行可判定子集。
metadata:
  type: reflection
  date: 2026-07-14
---

# issue #133:luaL_argerror 函数名按调用点派生(2026-07-14,PR #134)

> 范围:分支 `fix/argerror-caller-name`,PR #134(CI 全绿)。2026-07-14 定时巡检
> 补跑轮,处理 nightly 差分 fuzz 报的 issue #133,corpus
> `testdata/fuzz/FuzzOracleDiff/e8534c580042ec44`。

## 任务

nightly-diff-fuzz 报 p1 腿 crasher,种子是
`co=coroutine.create(coroutine.resume)print(coroutine.resume(co))`。本地重放确认
是当前 master 上的真实分歧(不是已修复代码上的旧撞点),牵出两个独立根因,一次
修复 + 语料入库 + 回归测试。

## 根因与修法

### 根因 1:luaL_argerror 的函数名派生规则

PUC 5.1.5 的 `bad argument #N to 'name'` 中,`name` 来自**调用方的调用点**而不是
被调函数身份:ldebug.c `getfuncname` → `getobjname` 对调用帧的 CALL/TAILCALL/
TFORLOOP 指令的 A 操作数做 symbexec。推论(全部经探针实证):

- `local r = string.rep; r(nil)` 报 `'r'` 不报 `'rep'`(别名命名);
- method 调用 self 不计入 #N(namewhat=="method" 时 narg--),narg 减到 0 时消息
  整体改成 `calling 'X' on bad self (extra)`;
- TFORLOOP 调用点是命名站点,典型报 `"(for generator)"`;
- 只跨 C-to-C 边界的错误(pcall 直调、table.sort 比较器、元方法 handler)保持
  `'?'`——getfuncname 要求调用帧是 Lua 帧。

wangshu 之前在每个 stdlib 站点硬编码被调函数名,以上四类写法全部分歧。

修法(结构化错误 + 边界解析):

- stdlib 站点改抛 `NewArgError(narg, extra)`(`internal/crescent/state.go`,
  LuaError 增 argNarg/argExtra 字段,消息先以 `'?'` 占位);
- `State.resolveArgError`(`internal/crescent/objname.go`)在 Lua 调用边界改写
  消息:doCall/doTailCall(`internal/crescent/call.go`)与 TFORLOOP(解释器
  `execute.go` + gibbous `gibbous_host.go` 对称)经 `callSiteFuncName`
  (getobjname 镜像)取名并处理 method 减一与 bad-self 分支;
- `callLuaFromHost`(`meta.go`)拆成 wrapper:在宿主调用边界把 argNarg 归零
  (冻结 `'?'`),TFORLOOP 边界改走 `callLuaFromHostNamed` 保留解析权。
- pc 口径注意:解释器主循环已 `ci.pc++`,解析时用 `ci.pc-1`;gibbous helper 收到
  的是原始 pc,直接用。两处口径不同,是历史文档已记录过的差异,本轮再次命中。

### 根因 2:coroutine.create 必须拒绝 C 函数

PUC `luaB_cocreate` 检查 `lua_isfunction && !lua_iscfunction`,C 函数报
`Lua function expected`。wangshu 之前接受 host closure,到 resume 时才分歧。修在
`internal/crescent/coroutine.go` NewCoroutine(增 `object.IsHostClosure` 检查)。

### 顺手同步的每站点差异

sweep 中同步的检查顺序/措辞:ipairs aux 先查 int 再查 table;rawget/rawset/
rawequal 按参数报 `value expected`;select 报 `number expected, got no value`;
裸 `number expected` 站点补 `, got <type>`。合计约 84 个 stdlib 站点迁移到
NewArgError。

## 验证

- ~70 条探针(别名/method/bad self/for generator/元方法/C 调用方/退化参数,覆盖
  string/table/math/os/coroutine 库)与 PUC 5.1.5 逐字节一致;gibbous force-all
  与解释器对称;
- 三个 build 变体(default / p3+profile / p4+profile)测试全绿;lint 0 issue;
- FuzzOracleDiff 90s + FuzzCompileRun 60s + FuzzAutoPromote 60s 冒烟干净;
- difftest errmsg 语料新增 7 条 `argerr_*` 用例(`test/difftest/errmsg_test.go`);
- crasher corpus 入库 `testdata/fuzz/FuzzOracleDiff/e8534c580042ec44` 常驻回归。

## 教训

### 教训 1:面级规则的分歧,先测绘完整行为面再一次性实现

一个 fuzz 种子表面上只是一条错误消息不同,实际牵出的是 PUC 一整个命名派生子系统
(别名命名 / `'?'` 回退 / method 计数 / bad self / for generator / C 函数拒绝)。
动手改实现前先写 ~70 条探针把整个行为面测绘成对照表,再一次性实现,避免了「修一
条、fuzz 再打穿一条」的逐点返工。这与 [[cross-backend-semantic-fix-sweep]] 的
「PUC 语义由 C 实现定义,分歧先 grep `_lua515/` 源码」一节同源,但本轮是**面级
规则**不是点级分歧——判断信号是:分歧涉及「派生逻辑」(名字从哪来、计数怎么减)
而不是「输出格式」时,默认背后是一个子系统,值得先探针测绘再动手。
**promotion 候选**:作为该节的升级实例(第二个面级实例,第一个是 `%#X` 手写
renderer)。

### 教训 2:错误消息依赖抛出点拿不到的上下文时,用结构化字段 + 边界层改写

函数名只有调用方的帧才知道,抛出点(stdlib 函数内部)拿不到。正解不是把调用点
信息穿透传给每个抛出点(~84 个站点全要改签名),而是错误对象携带结构化字段
(argNarg/argExtra),在**拥有上下文的边界层**(doCall/doTailCall/TFORLOOP)统一
改写消息。`'?'` 冻结是这个模式的关键细节:wrapper 在宿主调用边界把 argNarg 归零,
防止错误穿越多层边界后被**外层**调用点错误地重新命名——解析权只属于最内层的
Lua 调用边界。首次样本,暂留观察;若 P5 或 traceback 类需求再现同结构可升 guide。

### 教训 3:跨行结构的机械迁移不要用单发多行正则

用 Python 多行正则批量改写 stringlib.go 时把跨行 `fmt.Sprintf(...)` 弄坏(产出
`NewArgError(argn+1))` 之类的语法错误),整体 `git checkout` revert 后改成两段式
才干净:先跑**只匹配单行字面量**的逐行正则(可判定、误伤面为零),剩下的多行
站点逐个手工替换。教训:机械迁移的正则先收窄到单行可判定子集,跨行结构宁可手工。
首次样本,暂留观察。

### 教训 4(过程):CI 基础设施噪声再确认

CI 首轮 p4/macos-latest 失败是 `upload-artifact` FinalizeArtifact ETIMEDOUT
(测试本身全绿,失败在 coverage artifact 上传收尾),`gh run rerun --failed` 后
全绿。与 [[2026-07-09-issue52-close-round]] 记录的「GHA hosted runner 基础设施
噪声识别」同类,又一实例:识别特征是失败步骤不在测试/编译,而在 actions 官方
action 的网络收尾。

## promotion 决策

- 教训 1 + 教训 2 → 候选进 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C
  实现定义」节作新实例(caller-site 命名规则测绘 + 结构化错误边界解析);
- 教训 3 首次样本,暂留观察。

## 触发场景

- 与 PUC 的分歧涉及「派生逻辑」(名字、计数、回退)而非「输出格式」时(教训 1:
  默认是面级子系统,先探针测绘再实现);
- 错误消息/诊断信息需要抛出点拿不到的上下文时(教训 2:结构化字段 + 边界层改写
  + 解析权冻结);
- 用脚本机械迁移几十个调用站点时(教训 3:正则收窄到单行子集);
- CI 失败步骤在 actions 官方 action 网络收尾而非测试时(教训 4:rerun 即可)。
