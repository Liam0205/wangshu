---
name: p4-pj10-native-round
description: P4 PJ10 native emit 轮过程教训:PJ10 head-op 回放骨架换成 amd64/arm64 真原生 codegen 后 V15b heavy 三本 P4>P3 达 2.3x-3.0x;mmap 段内 shim call 与 Go morestack 不兼容 → 热路径必须 inline、冷路径 Go 端 dispatch;RK 字段 uint8 截断 bug 隐藏在从 shape-spec 迁到 native 的口径切换里;Go ABIInternal(RAX/RBX/RCX/RDI/RSI/R8...)不是 SysV,helper call 后 RBX 必刷、R14=G 须复位;prefer-native 拦截过宽当场压掉 25 个 PJ3/5/7 已调优测试 → 判据必须"精确匹配 shape-spec 触达不到的形式"(多 BB + ≥4 op live BB);tail-call gibbous dispatch 首版误重入 enterGibbous 双压帧;arm64 vs amd64 分支编码差异要求 arch-aware fixup 表;FORLOOP inline 前向 rel8 必须按字节精算或用符号 fixup。本会话 15 commits ed2235b..0c9db3a,分支 feat/pj10-native
metadata:
  type: reflection
  date: 2026-07-01
---

# P4 PJ10 native emit 轮反思(2026-07-01,head-op 回放骨架 → 真 amd64/arm64 原生 codegen)

> 范围:承 [[p4-pj10-perop-translator-round]] 的 Go 端回放骨架 + `xor eax,eax; ret` 3 字节 mmap stub,一次性做到位换成真原生 codegen——CFG builder + 两遍 label resolver + 35 opcode 每 arch 一份 emit(amd64 / arm64)。本会话 15 commits `ed2235b..0c9db3a`,分支 `feat/pj10-native`,V15b heavy 三本 P4 native > P3 wasm 达 HeavyArith 2.3x / HeavyRecursion 2.3x / HeavyFloatloop 3.0x。

## 核心教训(按强度排序)

### 1. mmap 段内不能调 Go helper(shim) —— morestack 走不过去 → 热路径必须 inline,冷路径退 Go 端 dispatch

初版 `ecf3896` 铺了 shim call 基础设施,想以「mmap 里嵌 Go helper call」把 35 opcode 一次性接住,写起来最省事。上机才发现在并发 + 嵌套负载下,Go stack unwinder 走到 mmap'd 段就崩——`morestack` 遇到没登记的 PC 无法回溯栈帧。这条**物理约束**决定了整个 opSupported gate 的形状:热路径(算术 / 比较 / MOVE / LOADK / FORPREP / FORLOOP / EQ / TEST / TESTSET)**只能 inline emit**,冷路径(RETURN / CALL / TAILCALL / CLOSURE / TFORLOOP 首次)通过 saveGoG 协议把 R14/RBX 交回 Go 端 dispatch 完成。

**Why**:Go 1.17+ ABIInternal 用 R14 存 G 指针,`morestack` prologue 检查 SP vs g.stackguard;进入 mmap 段后 PC 不在任何 `_func` 表里,unwinder 撞死。这不是 emit 顺序问题,是 Go runtime 层面对 unregistered code page 的物理限制——**逃不掉**,只能在 emit 侧 fork 出「安全可 inline」子集。

**How to apply**:任何在 mmap / unregistered code page 里做的 codegen,先列「哪些 op 频次高(必须 inline)/ 哪些低(可 shim + saveGoG)」,再定 opSupported 白名单;先假设「shim 可用」写完 35 op,再改成 inline,是双倍 emit 工作量。

### 2. RK 字段是 9-bit(0-511),从 shape-spec 迁到 native 时 uint8 截断 K 引用低 8 位

`f778ab7` 修的 bug:Lua 5.1 字节码里 B/C 字段实际 9 位,值 ≥256 编码常量表引用(K constant),<256 是寄存器。PJ10 head-op 回放阶段是 shape-spec 层负责 RK 拆分,native 层直接吃 uint8 B/C 就够;换到真 emit 后 native 端要自己算 `if C >= 256 then K[C-256] else R[C]`,而 emit 函数签名沿用了 uint8 B/C → C=256 被截成 0、C=257 截成 1,把 K 常量误引成寄存器。修法:算术 / 比较 / 表 op 的 B/C 参数从 uint8 拓宽到 int。

**Why**:表示口径切换(shape-spec 层 RK 已拆好 → native 层需自拆)在函数签名边界失守。前一轮回放骨架从没让这条位宽暴露出来,是「上游把粗活干了」的假象——一旦下游改成自己拆,原本够用的 uint8 就露馅。

**How to apply**:形式表示层交接边界(spec → codegen / IR → asm 等)迁移时,把每个 opcode 字段的**bit 宽度**在新签名里显式核对一遍,别沿用旧层的 Go 数据类型;9-bit RK 是 Lua 5.1 spec 硬性,新增翻译器每一遍都得独立撞一次(与 [[p4-pj10-perop-translator-round]] 教训 3「CLOSURE SubNUps idiom 跨翻译器复发」同款结构——**同一条 bytecode 物理事实,每个翻译器独立撞**)。

### 3. prefer-native 拦截过宽当场压掉 25 个已调优测试 —— 判据必须精确匹配 shape-spec 触达不到的形式

初版 `4b5abf8` 的「prefer native」在 Compile 顶端对任何 `AnalyzeNative` 接住的 Proto 一律走 native,结果 25 个 PJ3/PJ5/PJ7 shape-spec 测试红——它们断言某个 spec 快路径被走到,现在被 native 抢了。修法(`0c9db3a`):把 PreferNative 收窄到「多 BB **且**至少一个 live BB 有 ≥4 opcode」,精确匹配 shape-spec 天生打不着的形式(单 BB / 少 op 小函数走 spec,多 BB / 大 BB 走 native)。

**Why**:多路径优化系统里,新路径的入口判据是「**新路径独占的形式**」不是「新路径能干」。native 能干 shape-spec 一半以上,但那一半 shape-spec 干得更快(spec 直接 head-op 回放不用 CFG / label resolve / codegen 开销)——把它抢过来是负收益。

**How to apply**:引入 tier / 快路径 / 优化器新档时,入口判据用「**这个档独占能做的形式集**」而非「这个档能做的最大集」。判据落地形式:先跑一遍全测试看新档拦截了多少既有快路径 → 那些是「本不该拦」的信号,把判据窄到只吃它们剩下的部分。这条与 [[p3-pw10-architectural-ceiling-round]] 教训 1「profile 才是合同」是**入口侧对偶**——那条管「实测证明目标不可达就止损」,本条管「实测证明新入口抢了别人的地盘就窄化」,共享判据基础「实测再决定形式」。

### 4. Tail-call gibbous dispatch 首版误重入 enterGibbous 双压帧

`4b5abf8` 的 `doTailCall` 想用 `enterGibbous(callee)` 复用父帧,结果 `enterGibbous` 里自己会调一次 `enterLuaFrame` —— 加上 `SetTailcall` 前已经压好的帧,一共两层,PJ5 TailCall 测试报 `attempt to call ... (a number value)`(base 错位到实参数字上)。修法:tail-call 帧上直接跑 `code.Run` inline,不再走 `enterGibbous`(它有自己独立的 `enterLuaFrame` 语义,是给「首次进入 gibbous 帧」用的)。

**Why**:`enterGibbous` / `enterLuaFrame` / `SetTailcall` 三者的帧栈生命周期契约各自独立,组合时 SetTailcall 已经完成的「复用父帧」被 enterGibbous 的「新建帧」覆盖了一次——这是**helper 组合语义的重叠**,不是逻辑错。要修得靠拆:tail-call 复用父帧就绕开 enterGibbous,直接 Run。

**How to apply**:helper 组合(A + B)出现帧栈 / 状态双写时,先看两者各自的 lifecycle 契约有没有重叠字段;若有,组合处不能都调,得选一条 canonical 路径把重叠字段的所有权定下来。

### 5. arm64 vs amd64 分支编码差异要求 arch-aware label fixup

amd64 分支是签名 32-bit 字节位移(rel32),arm64 分为 `B` rel26 word-scaled 与 `B.cond` / `CBNZ` rel19,位掩码不同。共享 `codeBuf` 就得 arch-aware `resolveLabels`——`65cb85f` 加了 `fixupKind` enum + `addFixupKind`,arm64 site 用 `fixupKindArm64B26` / `fixupKindArm64B19` 分别打补丁。

**Why**:ISA 分支编码是 platform physics,没法共 codegen 抽象层直接抹平——每种编码宽度 / scale / bit layout 都得走独立 fixup。移植到新 arch 时低估这条,会写到一半再回头改 fixup 表结构。

**How to apply**:多 arch label fixup 从起点就带 `fixupKind` enum;别一开始只支持 amd64 rel32,后来 arm64 再补——补一次就要改 codeBuf 里每个 label emit 点。

### 6. FORLOOP inline 前向 rel8 按字节精算 —— `movsd disp32` 是 8 字节不是 9

`000b6bf` FORLOOP inline 里手算 jae / jmp 的 rel8 位移,`movsd xmm0, [rbx+disp32]` 一开始按 9 字节算(错),condTrue 块实际 13 字节,jae 落错跳到中间——heavy_floatloop 直接死循环。修法:把每条 SSE / 分支的字节长度都在代码注释里写死,再算相对偏移。

**Why**:x86 变长指令 + SSE prefix 组合下每条字节数得查表,记错 1 字节整个前向分支散架。inline 短距分支能省 emit 复杂度,代价是**手算字节数不容错**。

**How to apply**:能用符号 fixup 就用,不用 rel8 手算;必须 inline 短距(如 FORLOOP hot loop 想省 rel32)时,把每条指令字节数写在注释里 + 每个前向分支的目标块字节数独立列,再算 delta,别边写边算。

## 其它(较小)

- **Go ABIInternal ≠ SysV**:amd64 int 参 RAX/RBX/RCX/RDI/RSI/R8/R9/R10/R11,不是 SysV 的 RDI/RSI。RBX 是 caller-saved arg1,每次 helper call 后必须重载;R14=G / R15=jitCtx 也得在 helper call 后复位。写 shim 交接时容易按 SysV 直觉写。
- **cross-compile 早验**:`GOARCH=arm64 CGO_ENABLED=0 go build` 对 `wangshu_p4` / `wangshu_p4 wangshu_profile` 两 tag 组合都跑一次,能提前抓 arm64 端 emit 函数签名 / 未导出符号 / 平台 stub 缺件。linux/arm64 host 上真跑 e2e 是 CI followup。
- **bench 噪声 vs 信号**:P3 wasm heavy 三本首次跑 `10x count 1` 拿到 30-90ms 摆动(首次分配 + 预热),换 `-count=3 -benchtime=10x` 才稳。V15b 类 heavy 验收前默认 3 样本以上,别看首帧数字下结论(与 [[p3-pw9-acceptance-perf-round]] 教训 1「先证被测路径真被走到」邻接——本条是**度量口径**版:先证 bench 数字稳,再谈 tier 对比)。
- **PJ10 native 版本对 opSupported 的严格化**:回放骨架 35/38 op 全支持,native 版本 opSupported 现只放热路径 inline + 冷路径可 saveGoG 那部分。前一轮 [[p4-pj10-perop-translator-round]] 教训 1「正确性 floor vs 性能 ceiling 拆分」的 floor 已完成;本轮做 ceiling 时反而要**收窄 opSupported** —— floor 与 ceiling 的 op 支持面**不一定相等**,ceiling 有 physical constraint(mmap+morestack)会砍支持面。这是那条教训的续集:floor 覆盖不代表 ceiling 一定能全接住,ceiling 落地要再做一次可行性 gate。

## 验证

- 35 opcode / arch × 2 arch = 每 arch 一份 emit 全就位(amd64 独立 35,arm64 部分经 shim);
- V15b heavy 三本(10 iter × 3 sample):HeavyArith P4=13.15ms vs P3=30.34ms(**2.3x**)/ HeavyRecursion P4=5.48ms vs P3=12.66ms(**2.3x**)/ HeavyFloatloop P4=17.24ms vs P3=50.94ms(**3.0x**);
- V14 luajc 无回归;
- conformance / difftest / luasuite 逐字节一致;
- `GOARCH=arm64 CGO_ENABLED=0 go build` 两 tag 组合过,arm64 runtime e2e 留 CI(需 linux/arm64 host)。

## promotion 候选

- **教训 1「mmap 段内 shim + Go morestack 不兼容」**——首次样本,unique perspective(Go runtime unwinder 对 unregistered code page 的物理限制)。若后续 P5 trace JIT / 其它 mmap-based codegen 再撞,可升 guide「mmap 段内 helper call 的可行性 gate」。**暂留观察**。
- **教训 2「表示层交接的 bit-width 独立核对」**——与 [[p4-pj10-perop-translator-round]] 教训 3「CLOSURE SubNUps 跨翻译器复发」同款结构(同一条 bytecode 物理事实每个翻译器独立撞),两者构成「Lua 5.1 spec 物理事实 · 翻译器边界独立撞」家族第 2 实例;若第 3 实例出现,可升 guide「新翻译器接入 Lua spec 时的 bit-width / pseudo 数据字独立核对清单」。
- **教训 3「入口判据窄到新档独占形式」**——与 [[p3-pw10-architectural-ceiling-round]] 教训 1「profile 才是合同」是**入口侧对偶**(那管止损,本管窄化);首次样本,可暂留观察或作 [[perf-optimization-workflow]] 邻接补充「新档入口判据 = 独占能做的形式集,不是最大能做集」。
- **教训 4「helper 组合帧栈重叠」**——首次样本,更偏 P4 特定 helper 契约,暂留 memory。
- **教训 5「多 arch label fixup 从起点带 fixupKind」** + **教训 6「rel8 手算精算」**——多 arch codegen 具体机制,首次样本,暂留;若 P4 backends 后续 arch 再撞,可合并成小 guide「多 arch label resolver 通用结构」。

## 触发场景

- mmap / unregistered code page 里想调 Go helper 时(教训 1:先做「热路径 inline / 冷路径 saveGoG」拆分,别一遍写 shim 再回头 inline);
- 从上游 IR / spec 层往下游 codegen 迁,签名要吃更细的字段时(教训 2:bit-width 独立核对,Lua 5.1 RK 9-bit / CLOSURE SubNUps pseudo 数据字 pc 步进都是同款);
- 引入新 tier / 快路径 / 优化档,想改 Compile 入口时(教训 3:入口判据 = 新档独占形式集;先跑全测试看抢了哪些既有快路径,再窄化);
- helper 组合(enterLuaFrame + SetTailcall + enterGibbous 类)出现帧栈 / 状态双写时(教训 4:先看 lifecycle 契约的重叠字段,再定 canonical 路径);
- 多 arch codegen 起点(教训 5:label fixup 带 fixupKind enum,别单 arch 写完再补);
- 短距前向分支想省 rel32 用 rel8 时(教训 6:能用符号 fixup 就用,必须 inline 时每条字节数注释 + 独立算 delta);
- 看 heavy bench 首次数字剧烈摆动时(其它·bench 噪声:`-count=3 -benchtime=10x` 起底,别看首帧下结论)。

## 关联

[[p4-pj10-perop-translator-round]](**直接前序**:Go 端回放骨架 + `xor eax,eax; ret` 占位 stub 是本轮的 floor;本轮换真 native codegen 是 ceiling 交付;教训 2「RK 位宽」与那篇教训 3「SubNUps 跨翻译器」同结构;本轮教训 1 是那篇教训 1 floor/ceiling 拆分的续集——floor 全 op 覆盖不代表 ceiling opSupported 一样宽,ceiling 有 mmap+morestack 物理砍位)· [[project-pj10-native-longtask]](本轮开工前的立项 memory,「一步做到位」策略与本轮 15 commits 交付对齐)· [[p3-pw10-architectural-ceiling-round]] 教训 1(profile 才是合同——本轮教训 3「入口判据窄到独占形式」与之是**入口侧对偶**,共享判据基础「实测再决定形式」)· [[p3-pw9-acceptance-perf-round]] 教训 1(先证被测路径真被走到——本轮「bench 噪声 vs 信号」是**度量口径**版邻接:先证数字稳,再谈 tier 对比)· [[perf-optimization-workflow]]([[p3-pw10-architectural-ceiling-round]] 提名的 §7「profile 才是合同」,本轮教训 3 若升为新档入口判据可作邻接补充)· `internal/gibbous/jit/peroptranslator/translator_native_amd64.go` / `translator_native_arm64.go` / `register_amd64.go` / `register_arm64.go` / `emit_ops_amd64.go` / `cfg.go` / `label_resolver.go`(本轮主要交付面)· 本会话 commits `ed2235b..0c9db3a`(15 commits,分支 `feat/pj10-native`,V15b heavy P4>P3 2.3x-3.0x)
