---
name: p3-pw7-pw4b-closure-tforloop-round
description: P3 PW7(CLOSURE/CLOSE/VARARG)+ PW4b(TFORLOOP 泛型 for)翻译轮过程教训:设计稿标注的「难点」可能已被前序里程碑(VS0-c)解掉、动手前先重核它是否仍是难点、emitOpcode (skip,err) 重构让发射循环跳过 opcode 后随的伪指令数据字、TFORLOOP base 刷新是 design-claims-vs-codebase-physics §2 第 4 实例且这次在 planning 阶段就预判到了(guide 正面确认)、新控制流 opcode 复用 PW6 跨层机制几乎零增量
metadata:
  type: reflection
  date: 2026-06-14
---

# P3 PW7(CLOSURE/CLOSE/VARARG)+ PW4b(TFORLOOP)翻译轮反思

> 范围:PW7 把 CLOSURE(闭包构造)/CLOSE(作用域 upvalue 关闭)/VARARG(vararg 防御)翻译为 Wasm;PW4b 补上 PW4 deferred 的 TFORLOOP(泛型 for)。CLOSURE 复用 crescent `makeClosure`、CLOSE 复用 `closeUpvals`、TFORLOOP 的迭代器调用复用 `callLuaFromHost`(从而自动继承 PW6 的 `doCall`→enterGibbous 跨层再入)。VARARG 不在翻译白名单 → `SupportsAllOpcodes` 返回 false → 整 proto 回退解释器(承 P2 F1 闸门对 vararg 函数的排除)。本轮后**全 38 个 Lua opcode 除 VARARG 外全部可翻译**。两提交:`6f2fd0e`(PW7-a CLOSURE/CLOSE + `emitOpcode` skip 机制)→ `aec94c6`(PW7-b VARARG 防御 + PW4b TFORLOOP i64 base 刷新)。承 `02-translation.md` §3.7/§3.5.3。

## 核心教训

### 1. 设计稿标注的「难点」可能已被前序里程碑解掉——动手前先核实它是否仍是难点

设计稿(`02-translation.md` §3.7)给 PW7 估了 0.5-1 人月,并把「开放/关闭 upvalue 在 linear memory 形态下的存储协议」标为**核心难点**。但这个难点**早已被 VS0-c(值栈 arena 迁移)解掉了**:open upvalue 经 `owner.slot(idx)` 形态 Y 寻址(`stackBaseW` 变化时自动重定位),gibbous 侧的 upvalue 访问从 PW2 起就一直走 helper。所以 CLOSURE/CLOSE 只是路由到 helper 复用 `makeClosure`/`closeUpvals`——**没有任何新的 upvalue 协议要设计**。真正的非平凡点反而是设计稿当脚注处理的东西:在发射循环里跳过 CLOSURE 后随的伪指令(见教训 2)。

**Why**:设计稿的人力估算与「核心难点」标签是在**前序里程碑落地之前**写的;一个基础性变更(VS0 形态 Y 寻址)落地后,可以**追溯性地溶解掉后续里程碑被标注的难点**。难点的位置因此发生了**时间维度的漂移**:它不在设计稿指的地方了,而设计稿不会自动更新这个判断。照着设计稿的难度地图分配注意力,会把精力投到已经不存在的难点上,反而对真正的非平凡点(被当脚注的那个)缺乏戒备。

**How to apply**:开始任何标注「难点」的里程碑前,先把设计稿的「核心难点」**对照已经建好的东西重新验证一遍**——真正的挑战可能已经移位。具体做法:把设计稿点名的难点逐条问「这个难点所依赖的前提,有没有被某个已落地的前序里程碑改掉了?」(这里:upvalue 存储协议依赖「值栈不可寻址/会搬家」,而 VS0-c 已让它经 `slot()` 现算可寻址)。难点已消解的,把省下的注意力转投到设计稿轻描淡写的环节——那里往往藏着实际的非平凡点。

### 2. `emitOpcode` 的 `(skip int, err error)` 重构:opcode 后随「伪指令数据」时,发射循环须能跳过非 opcode 字

CLOSURE A Bx 后面紧跟 `SubNUps[Bx]` 条**伪指令**(MOVE/GETUPVAL,描述每个 upvalue 怎么捕获),它们是**数据**不是可执行 opcode。两条发射循环(单 BB 直线路径 + `emitBlockBody` 前缀)都按 pc 迭代,会把这些数据字**误译成真的寄存器拷贝**。解法:`emitOpcode` 改签名返回 `(skip, err)`;CLOSURE 返回 `SubNUps[Bx]`;两条循环都做 `pc += 1 + skip`。跳过条数取自 `Proto.SubNUps[Bx]`(父 Proto 缓存的子 proto upvalue 数,注释正是「symbexec 精确跳过用」——天生为此设计)。**我先把签名改造当作纯机械重构落地**(在 PW1-PW6 上验证零回归),**再**加 CLOSURE 这一分支。

**Why**:字节码流里**可执行 opcode 与 opcode 私有的数据字是交织的**;朴素的 `pc++` 循环会把数据当指令读。跳过条数必须来自一个 **Proto 级字段**而不是临场重新解码(重新解码 = 把 symbexec 的逻辑在发射器里抄第二遍,易错且耦合)。先做机械签名重构再让行为改动搭车,是因为「改签名」与「加新语义」混在一个 diff 里时,一旦回归无法区分是签名改错了还是新分支错了;拆开后机械重构那步的零回归可独立证明。

**How to apply**:当一个 opcode **拥有后随数据字**时,发射/扫描循环需要一个**显式的 skip 计数**,且应取自 Proto 级字段(`SubNUps` 这类),不要靠重新解码现算。先落地纯机械的签名重构并验证零回归,再让依赖它的行为改动搭车。可对照先例:SETLIST C=0「下一字是批量计数数据」是 PW5 里**同类「下一字是数据」**的情形(那里选了保守回退),CLOSURE 的伪指令是**跳过**——同一物理现象(opcode 拥有数据字)的两种应对。Edit 机械坑同族见「其它」。

### 3. TFORLOOP base 刷新是 [[design-claims-vs-codebase-physics]] §2 的第 4 个实例——这次「实现前就预判到了」

TFORLOOP 的迭代器调用走 `callLuaFromHost`,可能 `growStack`(把值栈段在 arena 重定位),所以调用返回后的 `$base` 是陈旧的 = UAF——正是 PW6 `h_call` 的同一个雷区。但这一次,因为 guide [[design-claims-vs-codebase-physics]] §2 **已经存在**,我在**规划阶段**就预判到了它(而非调试阶段才撞上):把 `h_tforloop` 的返回从设计稿的 3 值状态(0/1/3)改成一个 **i64,同时承载刷新后的 base 和 3 个状态**(newbase≥0 继续 / -1 ERR / -2 退出)。

**Why**:guide 把一个**已经学过两次**的雷区(PW6 段重定位 UAF + 本轮 TFORLOOP)变成了**规划阶段的检查项**——这条教训的成本现在**在规划时一次性付清**,而不是再次经由 GC 压力失败去重新发现。这正是把 memory 提升为 guide 想要的效果:防线从「事后复盘抓到」前移到「动手前预判到」。

**How to apply**:**这是对 guide 提升(PW6 轮拍板)的正面确认**——当一个热路径值要穿进一个**可能 growStack 的 helper** 时,在写 e2e **之前**就把刷新后的 base 编码进返回值。guide 正在按预期工作:把它记作一次**有效性确认**,而不是一条新 promotion。后续遇到同形态(热路径值穿过可重定位段)直接套 §2,不必重新推导。

### 4. TFORLOOP/CLOSURE 复用解释器跨层机制,是 PW6「慢路径复用解释器」红利的延续——新 opcode 几乎零增量

TForLoop 复用 `callLuaFromHost`(所以一个**本身被升层到 gibbous** 的迭代器会自动经 PW6 的 `doCall`→enterGibbous 再入);Closure 复用 `makeClosure`;Close 复用 `closeUpvals`。PW6 建好的跨层机械使 PW4b 的迭代器调用**几乎免费**。

**Why**:一旦解释器复用基底(PW6)存在,后续任何「要调用点什么」的控制流 opcode 都**继承**它。复杂度不需要在每个新 opcode 里重新解决,而是被 PW6 一次性留在了正确的地方。

**How to apply**:这印证了 [[p3-pw6-crosslayer-call-round]] 教训 2(把控制流重的语义路由回解释器)是**会复利的**——每个里程碑的复用基础设施都在**降低下一个里程碑的成本**。接 PW8/PW9 时,先盘点「这个新 opcode 要做的事,PW6/PW7 是不是已经留了一条可复用通道」,而非默认在 wasm 里从头实现。

## 其它(较小)

- **VARARG 不需要任何发射代码**:它不在白名单 → `SupportsAllOpcodes` 返回 false → 整 proto 回退。设计稿写的「Wasm unreachable 防御翻译」是多余的,因为白名单**已经在 emit 之前**就挡住了到达。加了一个测试(`TestPW7_VarargRejected`)把这条锁死。
- **TFORLOOP e2e 不能用 ipairs/pairs**:crescent 测试里没有 stdlib(用了会造成 import cycle),改用**自定义 Lua 迭代器**(基于闭包的 range、无状态 iter+state+control 三元组),它们走**完全相同的 TFORLOOP 路径**(`R(A)(R(A+1),R(A+2))`)——同一物理,无 stdlib 依赖。
- **同款 TierStuck-on-deep-baseline 陷阱**(承 PW6-b):深迭代测试用**独立 State** + **深跑前先 promote**。
- **i64Const 负哨兵**:`uint64(int64(-1))` 是 Go 编译错误(常量溢出);用 `^uint64(0)` 和 `^uint64(0)-1` 表示 -1/-2 的位模式。
- **两个机械 Edit 失手**:old_string 里残留 heredoc `EOF` 痕迹、替换函数体时留了个游离 `}`——都被 build 抓到。(轻微,PW5「Edit 吞掉相邻声明」同族。)

## 验证

4 build 组合 + `-race` + difftest 70 种子 + GC 压力;CLOSURE 捕获(MOVE/GETUPVAL 伪指令 skip 经字节码 dump 验 `SubNUps=1`)、TFORLOOP 深迭代 2000 轮(growStack 下 base 刷新)、VARARG 拒绝。

## 促成的稳定文档更新

- `docs/design/p3-wasm-tier/implementation-progress.md`:PW4/PW7 行 ✅、§9 PW7/PW4b 对账(upvalue 难点被 VS0-c 解掉 / CLOSURE skip 机制 / TFORLOOP i64 base 刷新 / VARARG 白名单防御)、状态行「全 38 opcode 除 VARARG 外全可翻译」。

## promotion 候选

- **教训 1**(设计稿「难点」可能已被前序里程碑解掉,动手前重核)——P3 翻译 PW8/PW9 还会遇到(PW8 线程级 tier 规则的「难点」可能也已被现有 `th==mainTh` 守卫部分解掉)。**首次样本,暂留观察**;若 PW8 再现,可考虑并入 [[design-claims-vs-codebase-physics]] guide 作为「**时间维度**」补充——physics 不仅是空间不变式(边界成本、段重定位、根可达性),也包括「**前序里程碑已经改变的事实**」这一时间维度。
- **教训 3** 是 [[design-claims-vs-codebase-physics]] 的**正面确认**(guide 在 planning 阶段拦截了 hazard),**不是新 promotion**,而是 guide 有效性的证据——在 promotion 节记一句即可。

## 触发场景

接 P3 PW8/PW9、开始任何标注「难点」的里程碑前(先核实难点是否仍在)、给 opcode 加后随数据字的翻译(发射循环跳过)、热路径值穿过可能 growStack 的助手时、写 tier 升层 e2e(深 baseline TierStuck 陷阱)时,看这篇。

## 关联

[[p3-pw6-crosslayer-call-round]](base 刷新家族 / 慢路径复用解释器 / TierStuck 陷阱同源)· [[design-claims-vs-codebase-physics]](教训 3 正面确认 §2 / 教训 1 候选「时间维度」补充)· [[p3-pw5-table-ic-round]](SETLIST「下一字是数据」是 CLOSURE 伪指令 skip 的前一实例 / Edit 机械坑同族)· `docs/design/p3-wasm-tier/02-translation.md` §3.7/§3.5.3 · `implementation-progress.md` §9
