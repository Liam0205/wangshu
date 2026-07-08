---
name: pr83-forprep-stackgrow-fallout-round
description: PR #83(fixes #78/#80/#81,2026-07-08)承 issue #67 auto-mode coverage 轮(PR #75)——FuzzAutoPromote 上线数天内抓到两个真实 P4 native-tier miscompile,加一个 nightly workflow 的 eval 引号 bug。#78 是两 arch 共有的 FORPREP 缺数字守卫(nil limit NaN 后 FORLOOP 静默零迭代,不报 PUC 5.1 的 "'for' limit must be a number" 错);#80 是两个叠加 bug——(a) nativeCode.Run 把入口捕获的 base(arena 字节偏移)当固定 token 传给每次 RefreshJitCtxAddrs,但 growStack 会重定位值栈段留下悬垂偏移(UAF)/(b) seg2seg 段内直调从不进 Go 侧 ensureStack,深递归溢出 64-slot 初始段写进邻居 arena 对象。#81 是 nightly workflow 里 eval 剥掉 -tags 引号的纯 CI 配置 bug。三 fix 各自独立 e2e + revert-proves-necessity 验证。
metadata:
  type: reflection
  date: 2026-07-08
---

# PR #83 FORPREP + native 调用栈溢出连锁修复轮反思(2026-07-08)

> 范围:分支 `fix/issue78-forprep-guard`,三个提交:`0b8f539`(#81 CI)、`04a0326`(#78+#80 native miscompile)、`3ee667a`(#67 遗留文档小修正)。

## 任务

issue #67 auto-mode 覆盖轮(PR #75,详见 [[2026-07-07-issue67-auto-mode-coverage-round]])上线 `FuzzAutoPromote` 后数天内,该 fuzz target 抓到两个此前完全无测试网覆盖的 P4 native-tier 真实 miscompile(#78、#80),另发现 PR #75 自己引入的 nightly workflow bug(#81)。

## 期望与实际

- 期望:auto-mode 覆盖轮补的测试网(尤其 `FuzzAutoPromote`)只是「补齐结构性盲区」,不预期短期内就抓到新 bug。
- 实际:**测试网在上线后数天内就抓到两个真实 miscompile**——这不是巧合,是 [[prove-the-path-under-test]] §5「fuzz 探索空间维度动了就重探」的直接验证:auto-only 决策链(阈值中途越线、`recheckCompilabilityRuntime` 自然路径)此前从未被任何 fuzz 探索过,是一个此前完全空白的探索空间维度,首次开放就命中——与该 guide 已聚合的三个「扩接受面 → 立刻 fuzz → 抓到既有 bug」实例（`opSupported` 扩面、arm64 新硬件、LEN/MOD/POW 扩面）同构,但触发方式不同:那三例是「扩接受面」触发,本例是「开辟全新驱动模式(auto vs force-all)」触发——**触发探索空间维度重置的动作不止「扩接受面」一种,「切换驱动模式」同样有效**。本轮判断为该既有维度的又一次验证,不构成需要新增的家族分支(核心断言早已跨过阈值,不必每次复现都升级 guide)。

## 三个修复

### 1. #78 — FORPREP 缺数字守卫(两 arch 共有的既有 bug)

两 arch 的 inline FORPREP 快路径直接把 `R(A)`/`R(A+1)`/`R(A+2)`(init/limit/step)喂给浮点运算,不做 `IsNumber` 守卫;`nil` limit 的 NaN-box 恰好是一个 NaN double,FORLOOP 的比较判断得到 false,循环**静默零迭代退出**——而不是解释器的 `"'for' limit must be a number"` 错误。修复:三槽全加 `IsNumber` 守卫(与 inline arith 同 idiom:`raw < qNanBoxBase`);守卫未过 → 走新 `HelperForPrep`(exit-reason code 27)→ dispatcher 分派 `host.ForPrep`(PUC 5.1 coercion-then-error + 槽位归一化 + 预减量)→ 从 FORLOOP 块 resume。可强制转换的字符串 limit(`"10"`)现在也能匹配解释器行为;NaN limit 本身是真数字(< qNanBoxBase),继续走快路径、零迭代、逐字节一致。

### 2. #80 — 深度 native 递归破坏值栈(两个叠加 bug)

**(a) 悬垂 `base`**:`nativeCode.Run` 在入口捕获 `base`(arena 绝对字节偏移),把它传给每次 `RefreshJitCtxAddrs`;但一次会 grow 值栈的 host 调用(`enterLuaFrame` → `growStack`)会把栈段在 arena **重定位**并释放旧段——重入段时用的是已释放偏移,静默读写脏内存。修复:`RefreshJitCtxAddrs` 从**活的**线程状态重算 `vsBase`(`(stackBaseW + cur.base)*8`),不再信任入口捕获的 `base` 参数。

**(b) seg2seg 无边界检查**:段内直调(seg2seg dispatch)从不重入 Go,也就没有 `ensureStack`——深递归帧越过 64-slot 初始栈段后静默溢出进邻居 arena 对象。修复:新增 `jitCtx.valueStackEnd` 字段(与其余地址字段同批在每次 Run 入口/dispatcher 重入时刷新)+ 两 arch 的 fast-body 守卫(被调帧 `vsBase + CalleeMaxStack*8` 必须落在 `valueStackEnd` 之下,否则 `skip_seg` 退到会 grow 栈的 host 路径);arm64 侧额外重新加载被守卫破坏的 depth 寄存器。

### 3. #81 — nightly workflow eval 引号剥离(纯 CI 配置 bug,PR #75 自己引入)

nightly auto-mode leg 用 `eval` 包一条内联加引号的 `-tags` 命令;`eval` 会重新解析命令行,剥掉引号——`"wangshu_p3 wangshu_profile"` 被拆成 `-tags` 值 `wangshu_p3` + 一个多余的位置参数 `wangshu_profile`,`go test` 报 `package wangshu_profile is not in std`。修复:去掉 `eval`(该 step 已由 `matrix.tags != ''` 门控,直接内联环境变量赋值即可,与 PR-gate `ci.yml` 调用带 tag `go test` 的写法字节一致)。p1 逃过一劫是因为它的空 tags 从未触达这个 step 的 matrix 守卫。

## 核心教训

### 教训 1(确认既有 guide,非新增分支):测试网上线本身也是一次探索空间维度重置

[[prove-the-path-under-test]] §5 已聚合三个「扩接受面 → fuzz → 抓 bug」实例。本轮补充:**触发维度重置的不只是「扩接受面」,「新增一种此前从未被驱动过的执行模式」(auto-mode vs force-all)同样触发**——两个 P4 native miscompile 都活在 auto-only 决策链上,`FuzzAutoPromote` 一旦开始真的驱动这条链,几天内就抓到。核心断言(「已跑过 N 分钟没抓到 ≠ 安全,维度变了就重置」)完全适用,不需要为「切换驱动模式」单独立一个子分支——本轮作为该断言的第四次验证记录,不改写 guide 正文。

### 教训 2(design-claims-vs-codebase-physics §2 新实例):固定 token 跨调用边界存活,不同子系统各自撞同一颗雷

#80(a)与 P3 PW6 的 `$base` UAF 是**同一条物理事实**在两个独立子系统里各自被撞中的第二个实例:

- **PW6(P3 wasm 层,2026-06-14)**:`04-trampoline.md` 把 `$base`(wazero linear memory 字节偏移)画成 gibbous wasm 函数入口锁定、全程不变;`h_call` 深入更深 Lua 帧触发 `growStack` 重定位值栈段,返回后陈旧 `$base` 变悬垂指针。
- **PR #83(P4 native 层,2026-07-08)**:`nativeCode.Run` 把入口捕获的 `base`(arena 字节偏移)当固定 token 传给每次 `RefreshJitCtxAddrs`;`enterLuaFrame → growStack` 同样重定位值栈段,入口 `base` 同样变悬垂。

两次踩雷的物理基础完全相同(arena 段可被 grow 重定位,任何跨调用边界存活的固定偏移/token 都会失效),但发生在两个不同的加速层实现(wasm trampoline vs native codegen dispatcher)——**guide §2 判据「这个 token 在它存活的窗口内,底层存储会不会被搬动/失效?谁有能力刷新它?」在新子系统里必须重新逐条核对,不能因为 P3 已经踩过、P4 就自动免疫**。两个子系统的解法同构:被调侧/后续刷新点返回或重算一个新值(PW6 是 `h_call` 返回新 base;#80(a) 是 `RefreshJitCtxAddrs` 从活的线程状态重算),而不是信任入口一次性捕获的值。

**判据强化**:任何新增的加速层(下一个可能是 arm64 native tier 的类似入口捕获模式,或 P5 trace JIT)若存在「入口捕获一个 arena/栈偏移当 token 供整个执行期使用」的模式,必须在设计/实现阶段显式对照 §2 判据重核,不能假设「已有子系统修过所以这里没事」。

### 教训 3(首次样本,暂留观察):段内快路径必须显式复刻 host 路径隐式维护的每条不变式

#80(b)的独立教训:host 调用路径(`enterLuaFrame`)隐式维护「值栈总够用」这条不变式(通过 `ensureStack` 按需 `growStack`);seg2seg 段内直调**完全绕过**这条host路径,因此也绕过了它维护的不变式——但没有任何东西提醒实现者「这个不变式是 host 路径免费提供的,你现在走的是另一条通道,它没有」。

这与 [[backend-capability-vs-profitability]] 描述的「host 通道 vs 段内直调通道」二分(`FloorExempter` 那条:固定 Run 成本假设只对 host 通道成立,seg2seg 通道走另一套物理)是**同一个二分轴的另一个面**——那里是「host 通道的固定成本假设不适用于段内通道」(方向:host 假设 → 段内通道不能用),这里是「host 通道隐式提供的安全网(ensureStack)不适用于段内通道」(方向:host 提供的保障 → 段内通道必须自己补上)。两者共同指向一条更普遍的判据:**任何新增的"绕过 host 通道"的快路径(段内直调、inline 快路径、mmap 段内执行……),都必须显式列出 host 通道隐式提供的全部不变式/副作用/安全网,逐条判断段内快路径是否需要自己重新实现它们**——本轮 `valueStackEnd` 守卫正是这条判据的产物。

首次样本暂留观察;若 P4 后续 op 扩面(如 #77 sqrt intrinsic)或 P5 trace JIT 再撞到「段内快路径漏了 host 路径的隐式保障」同族问题,建议并入 [[backend-capability-vs-profitability]] 作为该 guide 的「通道二分」新增一面,或独立立项。

## 验证

- 7 个新 e2e 回归测试(`e2e_forprep_stackgrow_test.go`),每条在对应修复被 revert 时**证明会失败**(prove-the-path 纪律);
- 两个 fuzz crasher(`dc600438529c986c`、`99f21e04d5377b4c`)作为回归种子保留;
- depth sweep 8..70 clean(此前 ≥20 即发散);
- 全量 P4 测试套 + difftest + conformance + bridge + `-race` 全绿;
- 90s `FuzzAutoPromote` + 60s `FuzzP4ForceAllPromote` + 30s p3 `FuzzAutoPromote` smoke 干净;
- realworld auto benchmark 无退化(nbody 13.2ms 不变)。

## promotion 决策

- 教训 1:确认 [[prove-the-path-under-test]] §5 既有断言(第四次验证),**不新增分支**,本反思记录即可。
- 教训 2:[[design-claims-vs-codebase-physics]] §2 第二独立实例(跨子系统复现同一物理雷区),**本轮直接 promote**——已在该 guide 补一条实例引用(见下)。
- 教训 3:首次样本,暂留观察;若再现候选并入 [[backend-capability-vs-profitability]] 或独立立项。

## 触发场景

- 新增/扩展任何加速层(native codegen、trace JIT、新架构 port)时,若存在「入口捕获 arena/栈偏移当固定 token」模式(教训 2:显式对照 §2 判据重核,不假设已有子系统修过就没事)。
- 给任何"绕过 host 通道"的快路径(段内直调、inline 快路径、mmap 段内执行)设计接受面/守卫时(教训 3:先列 host 通道隐式提供的全部不变式,逐条判断段内快路径是否要自己补)。
- 给自然触发路径(auto/natural)新上线一套测试网时(教训 1:测试网自身首次真正驱动该路径,就是一次探索空间维度重置,预期短期内可能抓到既有 bug,不是异常)。

## 关联

[[design-claims-vs-codebase-physics]](§2 新实例)· [[backend-capability-vs-profitability]](通道二分对偶面,教训 3 候选落点)· [[prove-the-path-under-test]](§5 第四次验证)· [[2026-07-07-issue67-auto-mode-coverage-round]](本轮的直接前序:PR #75 auto-mode 覆盖轮)· [[2026-06-14-p3-pw6-crosslayer-call-round]](教训 2 的第一实例来源:P3 `$base` UAF)· issue #78 · issue #80 · issue #81 · `internal/crescent/gibbous_host_p4.go` · `internal/gibbous/jit/jitcontext.go` · `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go` · `internal/gibbous/jit/peroptranslator/translator_native_arm64.go`
