# P4 实现进度对账(implementation-progress)

> 状态:**PJ0-PJ3 + PJ10 luajc 档突破已落地**(2026-06-26)。`function() for i=K1, K2 do end end` 全常量空 body FORLOOP 字节级 inline 真接入主路径,Xeon 6982P 实测 7.15-25.41x over gopher-lua,**完整超越 luajc 档 4.4x 基线**(承 §8 / [01](./01-launch-judgment.md))。详细设计齐备(子目录 10 文件约 8200 行,2026-06-24 单文件 360 行 → 子目录 10 文件扩展轮 → 审查收口轮)。
> P1 全卷(M0-M14)+ P2 PB0-PB7 + 后续优化轮 #1-#4 + P3 PW0-PW10 + VS0-e 全卷已交付(2026-06-16),P4 启动前置就绪;**唯一阻塞**是 P4 立项判定本身(承 [01-launch-judgment §3](./01-launch-judgment.md))。
> 单一事实源:本文是 P4 实现现状与设计文档差异的对账表(对应 [P3 implementation-progress](../p3-wasm-tier/implementation-progress.md) 的角色,但 P4 是设计阶段未实施,本文重在「设计期决策盘点 + 跨文档回填请求收口表 + 实施前置确认 + 后续维护协议」)。
> 设计文档集:见 [00-overview §0](./00-overview.md) 文档地图。
>
> **术语:`P-JIT`(PJ)= P4 实现里程碑编号**(对应 P1 的 M、P2 的 PB、P3 的 PW);PJ0 = 立项判定 + 包骨架,PJ1-PJ7 = amd64 全栈,PJ8-PJ9 = arm64 全栈,PJ10 = luajc 档总验收。

---

## 0. 当前状态

**P4 实现:PJ0 包骨架已落地 + PJ1 spike 闸门 🟢 + amd64 工程组件已落地**(2026-06-25,详 §5/§6)。**设计文档集已齐备**(00-08 + implementation-progress 共 10 文件约 8200 行,审查后口径):

- [00-overview](./00-overview.md)(319 行):文档地图 + PJ 里程碑 + 人月分解 + 跨文档定稿决策速查
- [01-launch-judgment](./01-launch-judgment.md)(810 行):启动闸门 + luajc 档锚点 + 立项决策树
- [02-template-direction](./02-template-direction.md)(710 行):方向裁决 = JSC Baseline 风格 per-function 模板编译
- [03-speculation-ic](./03-speculation-ic.md)(1104 行):IC 反馈消费 + f64 快路径 + guard + P4 内部 p4SpecState 子状态机叠加
- [04-osr-deopt](./04-osr-deopt.md)(1117 行):OSR exit 协议 + 物化 + 再训练
- [05-system-pipeline](./05-system-pipeline.md)(1099 行):四项税自付 + W^X + icache + trampoline + arena base 重载
- [06-backends](./06-backends.md)(919 行):双后端共享骨架 + per-arch + 双架构 CI
- [07-p3-retirement](./07-p3-retirement.md)(837 行):P3 去留决策框架(P4 验收时定)
- [08-testing-strategy](./08-testing-strategy.md)(1077 行):luajc 档 + V1-V22 + deopt 注入 + 双架构差分套

**前置条件检查**:

- ✅ **P1 全卷已交付**(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮 + issue #1-#18 公共面缺口轮系列)
- ✅ **P2 PB0-PB7 + 后续优化轮 #1-#4 全过线**(2026-06-13)
- ✅ **P3 PW0-PW10 + VS0-e 全卷已交付**(2026-06-16,本机 Xeon 6982P 2s×3 count 实测基线:loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x;call 0.52x 是 bench kernel 结构性架构边界)
- ✅ **P4 设计文档完整**(00-08 + implementation-progress 共 10 文件)
- ✅ **P4 PJ0 包骨架落地**(2026-06-25):`internal/gibbous/jit/{,amd64,arm64}` 子包 + bridge 注入 + Makefile/build-test-bins.sh `-p4` 系列接入 + V1-V13/V17/V18 在 P4 build 下不豁免(`make test-p4` 全过);PJ0 阶段 SupportsAllOpcodes 全 false ⇒ 行为等价 P1-only(详 §5)
- ✅ **P4 PJ0-PJ7 真接入扩展全过线**(2026-06-25/26):
  - PJ0 包骨架 + bridge 注入 + V1-V13/V17/V18 在 P4 build 下不豁免
  - PJ1 spike 闸门 🟢 + amd64 工程组件(codepage/trampoline/emitter)
  - PJ2 jitContext + 完整 trampoline + LOADK/RETURN 真接入
  - PJ3-PJ6 emitter 原语扩展(控制流 / 比较 / 调用 / 闭包族)
  - **PJ7 真接入 ~25 类形态 byte-equal**(getter 族 + setter 族 + 比较折叠族)
  - 14 个 host helper 全实装,P4HostState 接口完整
  - pc off-by-one 修复 + 多行错误消息 byte-equal 实证
- ⏳ **P4 立项判定 PJ2+ 启动**:当前需要外部输入(承 [01-launch-judgment §3](./01-launch-judgment.md))——三条硬前置:① 真实宿主负载需求(首个目标宿主规则引擎的列内核确认触及 luajc 档需求)② 资源到位(+1-2 人年人力承诺)③ 设计文档齐备(已就绪)。**P3 现状是 P4 立项的双向信号**:**loop 2.95x over P1(= 7.2x over gopher-lua,已超 luajc 档 4.4x over gopher-lua)**——列内核 loop 形态 P3 已超 luajc 档;但 **table 0.88x / call 0.52x / mixed 0.99x 仍 ≪ luajc 档列内核形态**——P4 立项动机**不在 loop 而在非 loop 形态**(call 0.52x 是 bench kernel 结构性架构边界);⇒ **若真实宿主负载主要是 loop 形态可暂不立项,若需 ≥luajc 档列内核能力(尤其 table/call/mixed 路径)则立项**(详 [01 §3.2 反向问题 / §3.3 P3 现状对照](./01-launch-judgment.md))。

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PJ | 内容 | 文档 | 完成定义 | 状态 |
|---|---|---|---|---|
| PJ0 | 立项判定 + 包骨架 + build tag 隔离 | [01](./01-launch-judgment.md) + [06 §6.1](./06-backends.md) | 立项判定通过 + `internal/gibbous/jit/{amd64,arm64}` 骨架 + bridge 注入 P4Compiler 后 SupportsAllOpcodes 全 false | ✅ **2026-06-25 落地**(详 §6 PJ0 实装对账) |
| PJ1 | amd64 trampoline + 直线模板(6 opcode) | [05](./05-system-pipeline.md) + [06 §3.1](./06-backends.md) | 直线 Proto 升层后 byte-equal;exec mmap + W^X 翻面工作 | 🔶 **2026-06-25 部分**(详 §6 PJ1 实装对账;spike 闸门 🟢 + amd64 mmap+W^X+trampoline+emitter 主库版 + LOADK/RETURN 单测 byte-equal;**未接入 GibbousCode.Run end-to-end byte-equal**——SupportsAllOpcodes 仍全 false,完整接入留 PJ3+) |
| PJ2 | amd64 算术 + 比较 + IsNumber×2 guard | [03](./03-speculation-ic.md) + [06 §3.2](./06-backends.md) | 双 number 快路径直发 `mulsd` 等;guard 失败 OSR exit 回解释 | ✅ **2026-06-26 完整接入扩展到 12+ 形态**(承 §7;ADD/SUB/MUL/DIV 三种操作数布局:reg-reg(92 字节,实测 1.01-1.03x)+ reg-K(73 字节,实测 1.01-1.02x)+ chain-KK 任意 op1+op2 组合(92 字节,~1.0x 单次调内 boundary 占主导)。e2e 双轨真升层 byte-equal 解释器 + 白盒命中探针 SpecRegKHits / SpecRegRegHits / SpecChainHits 实证非降级 host;deopt fallback 含 chain pc 修复对齐错误消息行号。**真大幅加速**(luajc 档 ≥4.4x)留 PJ3 FORLOOP 字节级内联把多次 boundary 摊出循环) |
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | [05 §6.3](./05-system-pipeline.md) + [06 §3.3](./06-backends.md) | 数值 for 编译后 ≥luajc 档单档(**P4 价值首次实证**)| ✅ **2026-06-26 真接入 3 类形态——突破 luajc 档**(承 §8;`function() for i=K1, K2 do end end` 全常量空 body 形态(69-83 字节模板)+ `for i=1, n do end` reg-limit hot path(117 字节,IsNumber guard + host.ForPrep deopt byte-equal P1)+ closure capture upvalue-limit(Run prelude host.GetUpval + reg-limit 模板复用)。安全点 check 字节级真接入(cmp byte [r15+preemptFlagOff], 0 + jne after_loop;V18 -race 抢占语义生效,spike 实证 preemptFlag 真切换执行路径)。**Xeon 6982P 实测加速比**:空 body 100/1000/10000 iter 8.11/17.53/20.09x over cres;reg-limit hot path 1000/10000 iter 17.44/20.15x;**全部 over gopher-lua 7-25x,远超 luajc 档 4.4x 基线**。**含 body / 嵌套 / break 留 PJ3+ 扩**) |
| PJ4 | amd64 表 IC 模板 + stableShape/Index 直达槽投机 | [03 §6](./03-speculation-ic.md) + [06 §3.4](./06-backends.md) | 单态表 guard + 直达槽跳哈希;形状变化 deopt + 再训练 | 🔶 **2026-06-25 emitter 部分**(EmitCmpRaxImm32 + EmitJaeRel32 + EmitJmpRel32;mmap 段验证 cmp+jcc/jmp 真按 flag 跳——IsNumber guard 物理基础;真接入留 PJ4+) |
| PJ5 | amd64 CALL/TAILCALL + 跨层互调 + OSR exit 实装 | [04](./04-osr-deopt.md) + [05 §4.3](./05-system-pipeline.md) + [06 §3.5](./06-backends.md) | gibbous-jit 三向分派 + OSR exit 状态等价(V19)| 🔶 **2026-06-25 emitter 部分**(EmitCallRel32/CallReg/PushReg/PopReg;push/pop round-trip 验证;helper call 真接入留 PJ5+) |
| PJ6 | amd64 CLOSURE/CLOSE + upvalue | [06 §3.6](./06-backends.md) | 闭包 byte-equal(复用 makeClosure/closeUpvals)| 🔶 **2026-06-25 emitter 部分**(EmitLoadKReturnTemplate + EmitProlog/Epilog 模板封装;10000 次 prolog/epilog 栈保护验证;upvalue 真接入留 PJ6+) |
| PJ7 | amd64 端到端验收 + 性能基准 | [08](./08-testing-strategy.md) | 单架构 V1-V22 全过 + V14 luajc 档 | ✅ **PJ7 真接入 ~25 类形态 byte-equal**(2026-06-25/26,详 §7;`SupportsAllOpcodes` 已扩展到 25 类形态——getter 族(RETURN A 2 / GETUPVAL / GETGLOBAL / GETTABLE / LOADK 含 string / LOADBOOL / LOADNIL / MOVE / ADD..POW 6 op / UNM / LEN / NEWTABLE / NOT)+ setter 族(RETURN A 1 / SETTABLE / SETGLOBAL / SETUPVAL)+ 比较折叠族(EQ/LT/LE 6-op luac 模板折成 BoolValue)。`p4Code.Run` 经 14 个 host helper 调 gibbous_host.go 与解释器 byte-equal;pc off-by-one bug 修复(行号 / IC 槽锚定 prelude op 自身 pc=0);多行错误消息 byte-equal 实证测试通过。**make test-p4 全套 21 binary 全过含 conformance/difftest/luasuite + V18 -race**;V14 luajc 档调优留 PJ10) |
| PJ8 | arm64 后端启动 + 渐进交付 | [06](./06-backends.md) | arm64 各 opcode 模板按族落地;`MAP_JIT` + icache flush | 🔶 **2026-06-25 工程组件部分**(linux/arm64 codepage + arm64 emitter movz/movk/ret 真发指令编码字节级验证;darwin/arm64 W^X MAP_JIT spike 留 PJ8+;真执行端到端留 PJ8+ trampoline asm) |
| PJ9 | arm64 端到端验收 + 双架构差分套 | [06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md) | 双架构 V1-V22 全过;Go 1.25/1.26/tip 矩阵 CI 绿 | 🔶 **2026-06-25 CI 矩阵部分**(.github/workflows/ci.yml 加 P4 variant 到 test/fuzz/conformance/difftest 4 job;cross-compile linux/arm64 + darwin/arm64 wangshu_p4 build 验证;真 arm64 self-hosted runner 留 PJ9+ 基础设施) |
| PJ10 | luajc 档验收 + 性能调优 | [01](./01-launch-judgment.md) + [08 §8](./08-testing-strategy.md) | **P4 总验收**:列内核负载 ≥luajc 档(≥164μs 水位 over gopher-lua)| ✅ **2026-06-26 luajc 档突破**(承 §8;PJ3 FORLOOP 字节级 inline 实测大幅加速:100 iter 7.15x over gopher;1000 iter 21.20x;10000 iter 25.41x over gopher-lua,均远超 luajc 档 4.4x 基线。10000 iter 形态 P4 仅 270μs / gopher 6.9ms — 完整超过 ≥164μs 水位口径。**PJ3 当前形态范围**:全常量空 body for 循环;含 body / reg limit / 嵌套 / break 留 PJ3+ 扩。**P4 立项动机已兑现**:列内核 loop 形态 P4 性能超 luajc 档,验证 method-jit 方向的物理可行性) |

---

## 2. 跨文档回填请求收口表

P4 设计期各子文档对 P1/P2/P3 / P4 子目录内现稿发起的回填请求。**承用户裁决「本期只记录不主动改 P1/P2/P3 现稿」**——全部标「⏳ P4 PJx 落地时同批补」,不在文档扩展轮兑现。RJ 编号(R = Request,J = JIT)按子文档源排序。

### 2.1 对 P1 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-1 | [08 §13.1 RB-1](./08-testing-strategy.md) | [P1 12 §3.8 Runner 抽象表](../p1-interpreter/12-testing-difftest.md):`WangshuGibbousJIT` runner 注释从「P3+ 新增,本文预留,不实现」拆为「P3 已实装 + P4 待实装」两行,避免 P4 实装时该注释跨度过广 | ⏳ PJ7 |
| RJ-2 | [08 §13.1 RB-2](./08-testing-strategy.md) | [P1 12 §7 P4 行](../p1-interpreter/12-testing-difftest.md):现稿已预留 P4 行,加链接指向 [P4 08 §4 / §5](./08-testing-strategy.md) 使 P4 字段从「文字承诺」升级为「具体口径接入」 | ⏳ PJ7 |
| RJ-3 | [04 §12](./04-osr-deopt.md) | [P1 05 §7 doCall / callResult 枚举](../p1-interpreter/05-interpreter-loop.md):新增 `callDeoptResume` doCall 出口——收到 GibbousCode.Run 返回 status=2 时的处理(reloadFrame + 续跑同帧);P4 阶段新增,P1/P2/P3 不需要 | ⏳ PJ5 |
| RJ-4 | [04 §12](./04-osr-deopt.md) | [P1 05 §9 错误冒泡纪律](../p1-interpreter/05-interpreter-loop.md):OSR exit 路径不应设置 `state.pendingErr`——exit 是「投机失误」非「语义错误」,不与错误冒泡互斥;若同发以错误优先(承 [P4 04 §7.4](./04-osr-deopt.md))| ⏳ PJ5 |

### 2.2 对 P2 文档的回填请求

> **方案 A 决议(承 [04 头注 + §5 + §12](./04-osr-deopt.md))**:**P2 三态枚举 `TierInterp/TierGibbous/TierStuck` 不变,P4 投机生命周期由 P4 自管(`internal/gibbous/jit` 端 map `p4SpecState[proto]`,枚举 `P4Speculative / P4Deoptimized / P4StuckSpeculation`)**。原 RJ-8(P2 04 §2.1 加 `TierGibbousJIT/TierStuckSpeculation` 枚举)/ RJ-9(P2 01 §2.2 加 `ProfileData.deoptCount`)/ RJ-10(P2 01 §2.2 加 `ProfileData.recompileCount`)三项**全部撤回**——P4 自管投机生命周期不需 P2 实装改动。RJ 总数从 37 → 34。

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-5 | [03 §11.1 RB-1](./03-speculation-ic.md) | [P2 05 §5.5 P4 deopt 兜底与重训练](../p2-bridge/05-p3-p4-interface.md):与 [P4 03 §7.3 / §7.4](./03-speculation-ic.md) 字面同源化;明确 P2 接受 RequestRefresh 后 CAS 装新 feedback,P3 旧指针仍可读 | ⏳ PJ4 |
| RJ-6 | [03 §11.1 RB-2](./03-speculation-ic.md) | [P2 02 §9.2 LT/LE 子分流缺口](../p2-bridge/02-ic-feedback.md):[P4 03 §10.4](./03-speculation-ic.md) 是其 P4 视角的对偶兑现,引用本文作 P4 端实证 | ⏳ PJ4 |
| RJ-7 | [03 §11.1 RB-3](./03-speculation-ic.md) | [P2 05 §5.6 P4 不依赖 P2 状态机硬纪律](../p2-bridge/05-p3-p4-interface.md):[P4 03 §8](./03-speculation-ic.md) 直接复刻该节,P2 文档侧加引用「本节具体形态见 P4 §3 §8」| ⏳ PJ4 |
| ~~RJ-8~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 04 §2.1 TierState 枚举] 加 TierGibbousJIT / TierStuckSpeculation~~ | ✅ **撤回(方案 A)**——P4 内部 `p4SpecState[proto]` 子状态自管,P2 三态不动 |
| ~~RJ-9~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 01 §2.2 ProfileData] 加 deoptCount~~ | ✅ **撤回(方案 A)**——P4 端 `p4SpecState[proto].deoptCount`,P2 ProfileData 不动 |
| ~~RJ-10~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 01 §2.2 ProfileData] 加 recompileCount~~ | ✅ **撤回(方案 A)**——P4 端 `p4SpecState[proto].recompileCount`,P2 ProfileData 不动 |
| RJ-11 | [04 §12](./04-osr-deopt.md) | [P2 05 §6.1 GibbousCode.Run status=2](../p2-bridge/05-p3-p4-interface.md):**已存在**(`P3 永远不返回 2,P4 才返回 2`)——本文承认接口,无需新增,登记作 P4 消费方 | ✅ 已存在 |
| RJ-12 | [07 §11.3](./07-p3-retirement.md) | [P2 04 considerPromotion 接口扩展](../p2-bridge/04-try-compile-fallback.md):增加平台维度,P4 平台走 jit promote,P3 平台走 wasm promote——**仅在 P4 验收决策为「留中层」时触发**;若决策为退役则无需 | ⏳ **条件性**(PJ10 决策为留中层时触发) |
| RJ-13 | [08 §13.3 RB-6](./08-testing-strategy.md) | [P2 06 §1 验收口径总表](../p2-bridge/06-testing-strategy.md):P2 V1-V22 在 P4 build 下不豁免——[P4 08 §0.2](./08-testing-strategy.md) 字面承诺,P2 06 加「P4 build 下 P2 V1-V22 仍跑」纪律 | ⏳ PJ7 |
| RJ-14 | [08 §13.3 RB-7](./08-testing-strategy.md) | [P2 05 §6 GibbousCode.Run status=2](../p2-bridge/05-p3-p4-interface.md):现稿已预留,加引用「P4 测试如何验 status=2 路径见 P4 §8 §4(V19 OSR 状态等价)」| ⏳ PJ7 |

### 2.3 对 P3 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-15 | [01 §9.1](./01-launch-judgment.md) | [P3 01 §0.3 闸门双向性](../p3-wasm-tier/01-spike-gate.md):补对位指针指向 [P4 01 §0.3](./01-launch-judgment.md)——P3 spike 闸门双向性与 P4 立项判定双向性同源逻辑 | ⏳ PJ0 |
| RJ-16 | [01 §9.1](./01-launch-judgment.md) | [P3 01 §5.4 跳跃路径下设计资产复用表](../p3-wasm-tier/01-spike-gate.md):补指针指向 [P4 01 §2.4](./01-launch-judgment.md)——P3→P4 设计资产继承清单的两个视角互补 | ⏳ PJ0 |
| RJ-17 | [01 §9.2](./01-launch-judgment.md) + [07 §11.1](./07-p3-retirement.md) | [P3 00-overview §1 边界表](../p3-wasm-tier/00-overview.md):补一行指向 [P4 01](./01-launch-judgment.md) + [P4 07](./07-p3-retirement.md)——P3 总览只列 P3 实施层(同 tier 不同后端),立项闸门 + 去留决议两个 P4 视角未点到 | ⏳ PJ0 |
| RJ-18 | [07 §11.2](./07-p3-retirement.md) | [P3 01 §0.2 闸门单点决策不可绕过](../p3-wasm-tier/01-spike-gate.md):补对位指针指向 [P4 07 §0.3](./07-p3-retirement.md)——P3 spike 闸门(开工)与 P4 P3 去留闸门是 P3 生命周期上的两个闸门,形态平行 | ⏳ PJ10 |
| RJ-19 | [03 §11.2 RB-4](./03-speculation-ic.md) | [P3 06 §1.1 P3/P4 物理同形 ≠ 语义同义](../p3-wasm-tier/06-ic-feedback-consume.md):[P4 03 §0.2 / §5.3](./03-speculation-ic.md) 把这条对偶面在 P4 视角全展开,P3 06 加引用「P4 视角对偶兑现见 P4 §3 §5」| ⏳ PJ4 |
| RJ-20 | [03 §11.2 RB-5](./03-speculation-ic.md) | [P3 06 §6.1 IC 失效是否触发重编译,留 P4 评估](../p3-wasm-tier/06-ic-feedback-consume.md):[P4 03 §7.3](./03-speculation-ic.md) 给 P4 端重训练协议——P3 IC 失效永久 miss 与 P4 投机 deopt 反复失败统一在 P4 RequestRefresh + 重编译协议处理(详见 [P4 04 §5](./04-osr-deopt.md))| ⏳ PJ4 |
| RJ-21 | [04 §12](./04-osr-deopt.md) | [P3 04 §1.2 / §1.4 bit50 写入纪律](../p3-wasm-tier/04-trampoline.md):CallInfo bit50 在 OSR exit 后的语义——清 0 还是保留 1(承 [P4 04 §7.2](./04-osr-deopt.md))**倾向清 0**(差分友好),P4 落地时实测确认 | ⏳ PJ5 |
| RJ-22 | [08 §13.2 RB-3](./08-testing-strategy.md) | [P3 08 §0.4 P3 退役协议预设位](../p3-wasm-tier/08-testing-strategy.md):落实「若 P3 退役,V1-V18 编号保留,P4 接续承担」,[P4 08 §10](./08-testing-strategy.md) 已字面化,P3 08 加引用「具体迁移协议见 P4 §8 §10」| ⏳ PJ7 / PJ10 |
| RJ-23 | [08 §13.2 RB-4](./08-testing-strategy.md) | [P3 08 §4.4 P3 是 P4 投机正确性验证的预演](../p3-wasm-tier/08-testing-strategy.md):现稿已字面承诺,加引用「P4 视角具体验证形态见 P4 §8 §4 / §5」| ⏳ PJ7 |
| RJ-24 | [08 §13.2 RB-5](./08-testing-strategy.md) | [P3 implementation-progress §11 PW9 验收对账](../p3-wasm-tier/implementation-progress.md):加 P4 视角延伸——[P4 08 §3.7](./08-testing-strategy.md) force-all 非空保证援引 RW-10 教训,P3 implementation-progress 加引用「P4 force-all-jit 同款纪律见 P4 §8 §3.7」| ⏳ PJ7 |

### 2.4 对外部 roadmap / llmdoc 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-25 | [01 §9.3](./01-launch-judgment.md) | [../roadmap §4 P4 段](../roadmap.md):「+1-2 人年」估算补「立项前置 = 立项判定([P4 01](./01-launch-judgment.md))」,使 P4 启动节奏与 P3 同款(spike 先于实施)显式化——目前 §4 P3 段有「开工前置 spike」措辞,P4 段无对位措辞 | ⏳ PJ0 |
| RJ-26 | [07 §11.5](./07-p3-retirement.md) | [../roadmap §4 P4 段](../roadmap.md):「Wasm 层退役,或留作可移植中层」措辞补指针指向 [P4 07](./07-p3-retirement.md),使该决策框架的单一事实源显式化 | ⏳ PJ10 |
| RJ-27 | [01 §9.4](./01-launch-judgment.md) | [../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 速查表 P4 行:「前置 spike」列空,补「P4 立项判定(详 [P4 01](./01-launch-judgment.md))」,与 P3 行「wazero call boundary <150ns」对位 | ⏳ PJ0 |
| RJ-28 | [01 §9.4](./01-launch-judgment.md) | [../../../llmdoc/architecture/evolution-roadmap §P4 正文段](../../../llmdoc/architecture/evolution-roadmap.md):补「立项判定先于实施(本文承担)」,与 P3 段「开工前置 spike」对位 | ⏳ PJ0 |
| RJ-29 | [01 §9.5(可选)](./01-launch-judgment.md) | [../p2-bridge/00-overview §6 跨文档定稿决策速查](../p2-bridge/00-overview.md):可加一行「P4 立项判定」,但 P2 是 P3/P4 共享前端可能不需要——**主助理裁决是否落入 P2 总览** | ⏳ PJ0(主助理决议) |

### 2.5 P4 子目录内部回填(本期收口)

子文档间互相补章节引用——主助理收尾轮统一兑现,这一节列出已识别的双向引用需求:

| # | 来源 | 内容 | 状态 |
|---|---|---|---|
| RJ-30 | [03 §11.3 RB-6](./03-speculation-ic.md) | [04 §5](./04-osr-deopt.md)(deopt 计数 + P4StuckSpeculation)+ [03 §7.2](./03-speculation-ic.md) 给 P4 视角,04 §5 给具体物化协议;两文协同覆盖完整闭环 | ✅ 已对接(双向协同已对接,03→04 §5 引足,04→03 §4 引足) |
| RJ-31 | [03 §11.3 RB-7](./03-speculation-ic.md) | [08](./08-testing-strategy.md)(差分接入「投机错果」最危险 bug 类)+ [03 §3.5 / §9.1](./03-speculation-ic.md) 提名差分主防线 | ✅ 已对接 |
| RJ-32 | [03 §11.3 RB-8](./03-speculation-ic.md) | [06](./06-backends.md)(per-arch 发射函数)+ [03 §2 / §5](./03-speculation-ic.md) 给伪汇编示意,06 引用 03 作 amd64 端母版 | ✅ 已对接 |
| RJ-33 | [03 §11.3 RB-9](./03-speculation-ic.md) | [02 §2.4 / §4.1 / §4.4](./02-template-direction.md)「子集内投机」承诺 + [03 §4.4](./03-speculation-ic.md) 落具体形态 | ✅ 已对接 |
| RJ-34 | [07 §11.4](./07-p3-retirement.md) | [08](./08-testing-strategy.md):验收基准分档加宿主真实负载形态([07 §10.1](./07-p3-retirement.md) 风险缓解);[07 §10.1](./07-p3-retirement.md) 已识别该风险,具体口径展开在 08 | ⏳ PJ10 |
| RJ-35 | [08 §13.4 RB-8](./08-testing-strategy.md) | [03 §3.5](./03-speculation-ic.md)「guard 多判 vs 漏判」语义边界 + [08 §3.4 / §11.1](./08-testing-strategy.md) 字面化,加双向引用 | ✅ 已对接 |
| RJ-36 | [08 §13.4 RB-9](./08-testing-strategy.md) | [04 §5 deopt 风暴 / §6 exit stub](./04-osr-deopt.md):[04 §5.5](./04-osr-deopt.md) 给 deopt 风暴物理学,[08 §5.6 V20](./08-testing-strategy.md) 把它翻成具体测试构造,加双向引用 | ✅ 已对接 |
| RJ-37 | [08 §13.4 RB-10](./08-testing-strategy.md) | [06 §5 双架构测试纪律 / §6 PJ 里程碑](./06-backends.md):[06 §5.2 / §6.2](./06-backends.md) 已立 V-J 编号,[08 §2.5](./08-testing-strategy.md) 落实 V1-V22 与 PJ 的具体映射 | ✅ 已对接 |

**回填请求总数**:**34 项**(撤回 RJ-8 / RJ-9 / RJ-10 三项,从 37 降到 34;承本文 §2.2 头注方案 A 决议),分布如下:

| 目标 | 数量 | 兑现节奏 |
|---|---|---|
| P1 现稿(P1 12 / P1 05)| **4 项**(RJ-1~4)| PJ5 / PJ7 |
| P2 现稿(P2 02 / 04 / 05 / 06)| **7 项**(RJ-5~7 / RJ-11~14;RJ-8/9/10 撤回)| PJ4 / PJ5 / PJ7 |
| P3 现稿(P3 00 / 01 / 04 / 06 / 08 / implementation-progress)| **10 项**(RJ-15~24)| PJ0 / PJ4 / PJ5 / PJ7 / PJ10 |
| roadmap + evolution-roadmap + P2 00 总览(可选)| **5 项**(RJ-25~29)| PJ0 / PJ10 |
| P4 子目录内部 | **8 项**(RJ-30~37,其中 7 项已落地双向协同)| 已对接 7 项(RJ-30~33 / RJ-35~37),余 1 项 RJ-34 ⏳ PJ10 |

**核心纪律**:承用户裁决「本期只记录不主动改 P1/P2/P3 现稿」,所有非「已对接」项标 ⏳。立项后按 PJ 落地节奏分批兑现。RJ-11 / RJ-30~33 / RJ-35~37 共 8 项已对接(无需 PJ 落地兑现;RJ-29 实状态是 ⏳ PJ0(主助理决议)不计入「已对接」)。

---

## 3. 设计期决策盘点(影响 × 不确定度)

按 [multi-doc-drafting guide](../../../llmdoc/guides/multi-doc-drafting.md)「主动盘点不确定决策」纪律。

### 3.1 影响 PJ 启动形态(高影响 / 高不确定度)

| 决策 | 定稿 | 出处 | 复核点 |
|---|---|---|---|
| **立项判定** | 立项前置闸门(三档:全启 / 部分前置 / 跳过)| [01 §3](./01-launch-judgment.md) | **外部输入**:① 真实宿主负载需求 ② 资源到位 ③ P3 现状是否够;**P4 生死攸关** |
| **双架构选择** | amd64 + arm64 双后端,其余架构留 P3 兜底或不支持 | [02 §4](./02-template-direction.md) + [06 §1](./06-backends.md) | 一致(P4 范围内确定)|
| **不写宏汇编** | 共享骨架 + per-arch 发射器(否决架构中立宏层) | [06 §1](./06-backends.md) | 一致(架构层选型确定)|
| **OSR 着陆粒度** | 函数级 + 静态 exit stub(允许局部缓存 + 静态物化序列) | [04 §1 / §3.7](./04-osr-deopt.md) | PJ7 amd64 原型实测后定终稿(纯函数级 vs 允许局部缓存)|
| **locals 寄存器跨指令缓存** | 允许局部缓存 + 静态物化序列 | [04 §3.6](./04-osr-deopt.md) + [06 §4](./06-backends.md) | PJ7 amd64 原型实测后定终稿;PJ10 调优可能展开 |
| **自管机器栈大小** | 待定(每帧栈预算) | [05 §3.4](./05-system-pipeline.md) | PJ0 / PJ1 amd64 trampoline 落地实测 |
| **编译执行的线程模型** | 倾向同步编译(模板编译微秒级)| [02 §1.4 / §5.2](./02-template-direction.md) + [05 §3 / §5.2](./05-system-pipeline.md) (实测期复核) | PJ0 实测;若 cold-start 长尾再考虑后台 goroutine 编译 + 安装屏障 |

### 3.2 依赖外部数据(中影响 / 高不确定度)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| **deopt 计数阈值** | 待定(承 [P2 01 §5 阈值定标](../p2-bridge/01-profiling.md) 同款待定)| P4 实测 deopt 率反推(承 [04 §5.6](./04-osr-deopt.md))|
| **`MaxRecompileTries`** | 待定 | P4 实测 deopt 风暴边界后定([04 §5.3](./04-osr-deopt.md))|
| **回边 preemptFlag 检查点密度** | 待定 | PJ3 实测(承 [05 §6.3](./05-system-pipeline.md))|
| **confidence 投机阈值** | ≥0.99(P2 PB2 已采用,P4 复用)| P4 实测 deopt 率反推(承 [03 §2.7](./03-speculation-ic.md))|
| **P3 去留结论** | 缺省退役 | **P4 验收时数据定**(承 [07 §5](./07-p3-retirement.md));翻案条件:真实宿主 iOS / 解释模式实测翻盘 |
| **bit50 在 OSR exit 后清 0 还是保留 1** | 倾向清 0(差分友好)| PJ5 实测确认(承 [04 §7.2](./04-osr-deopt.md) + RJ-21)|
| **guard 合并窥孔范围** | 同操作数直线段内只查一次(基线)| PJ7/PJ10 若 guard 密度天花板吃掉收益则展开(承 [03 §3.6](./03-speculation-ic.md))|
| **多 State 并发下 JIT 代码与 profile 的共享语义** | 待定 | PJ7 验收期落地(承 [../p2-bridge/00-overview §9](../p2-bridge/00-overview.md) 同款缺口)|

### 3.3 低风险已记录(低影响 / 已记缺口)

各子文档 §风险节 + [00-overview §10 风险与未决缺口汇总](./00-overview.md) + [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 次要缺口,约 10 余条;此处简列指针,均不阻塞 PJ0 启动(立项判定本身是唯一硬阻塞)。

---

## 4. P4 与 P1/P2/P3 implementation-progress 的差异

| 维度 | P1/P2/P3 implementation-progress | 本文(P4)|
|---|---|---|
| 当前状态 | 全卷已交付,持续维护后续轮次对账 | 设计阶段,实现未启动(等立项判定)|
| 表格主体 | 实际落地的 PR / 提交哈希 / 时间线 | 设计期决策盘点 + 待实施回填请求 |
| 与设计文档的差异 | 已落地形态与设计文档的差异 |(无差异——尚未实施)|
| 核心阻塞 | 无(已交付)| **P4 立项判定 + 真实宿主需求确认**——两项均可能改变 P4 是否启动 / 如何启动 |
| 后续维护 | 每轮里程碑落地后追加对账行 | 立项后追加 PJ0-PJ10 进度行;若立项判定否决则记录「P4 跳过」决策 + 判定数据 |

---

## 5. PJ0 实装对账(2026-06-25 落地,承 §1 PJ 表头注)

**状态**:✅ **PJ0 包骨架 + bridge 注入 + Makefile/build-test-bins.sh -p4 系列接入 + V1-V13/V17/V18 在 P4 build 下不豁免** 全过线。

### 5.1 立项判定数据进档(承 [01 §5.3](./01-launch-judgment.md))

| 硬前置 | 状态 | 说明 |
|---|---|---|
| ① P3 全卷已交付 | ✅ | PW0-PW10 全收口(2026-06-16),实测基线 loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x |
| ② 真实宿主负载需求 | ⏳ **未到位** | 首个目标宿主(规则引擎)未明确给出「列内核形态触及 luajc 档需求」的具体证据;**P4 价值兑现仍需此证据,但包骨架阶段 PJ0 不阻塞**(下文 §6.2 解释为何) |
| ③ 资源到位 | ⏳ **未承诺** | +1-2 人年人力承诺未到位;**包骨架阶段 PJ0 不阻塞** |
| ④ 设计文档齐备 | ✅ | 子目录 10 文件 ~8200 行(2026-06-24 扩展轮 + 2 轮审查闭环) |

**PJ0 立项决策**:**「最小可行包骨架」档**(承 01 §3.4 三档策略中的「部分前置」)——立项判定本身的产出(立项报告)即 PJ0 第一交付物,不论后续 PJ1+ 是否启动,本档已永久存档作未来重启的设计基线(承 §5 维护协议第 6 条)。

**为何 PJ0 不阻塞②③**:PJ0 的本质是「包骨架 + build tag 隔离 + bridge 注入」,**不引入任何机器码生成、原生码段管理、或 P4 投机面**——SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent,P4 build 行为与 P1-only 等价。这层骨架只是「让 P3+P4 共存的 build tag 协议成立」+「为 PJ1+ 立项后启动时不需返工 build 系统」预先铺好——是「闸门停下不亏」(roadmap §5 原则 3)在 P4 内部的兑现:即便 PJ1+ 永不启动,PJ0 的产出已让 P3 退役/P4 立项的决策面在工程层面就绪。

### 5.2 包骨架交付清单

| 件 | 落地 | 文件 |
|---|---|---|
| `internal/gibbous/jit/` 主包 | ✅ | `doc.go` / `compiler.go`(wangshu_p4)/ `compiler_off.go`(默认 build)/ `code.go` / `compiler_test.go` |
| `internal/gibbous/jit/amd64/` 子包 | ✅ | `doc.go`(wangshu_p4 && amd64,PJ0 占位) |
| `internal/gibbous/jit/arm64/` 子包 | ✅ | `doc.go`(wangshu_p4 && arm64,PJ0 占位) |
| bridge 注入(wireP4) | ✅ | `internal/crescent/arena_p4.go`(wangshu_p4 build);`arena_default.go` / `arena_p3.go` 加 wireP4 no-op stub |
| build tag 互斥 | ✅ | `wangshu_p3 && !wangshu_p4` / `wangshu_p4 && !wangshu_p3`,`!wangshu_p3 && !wangshu_p4` 默认;`build-test-bins.sh` 拒共存组合 |
| `state.go` 调用 wireP4 | ✅ | `wireP3()` 后追加 `wireP4()`(默认/p3 build no-op,p4 build 注入) |

### 5.3 工程化机制接入

| 件 | 落地 | 说明 |
|---|---|---|
| Makefile -p4 系列 | ✅ | `build-p4` / `test-p4` / `bench-p4` / `fuzz-p4` / `difftest-p4` / `conformance-p4`,与 `-p1` / `-p3` 平行;`-all` 顺手聚合 |
| `build-test-bins.sh p4` | ✅ | tags="wangshu_p4";拒未知 variant;同步修 `go list -tags` 漏传 bug(影响 P3 wasm 包识别) |
| 头注 + 注释更新 | ✅ | Makefile 命名约定从「P1/P3/未来 P4」更新为「P1/P3/P4/未来 P5」;build-test-bins.sh 同款 |

### 5.4 验收口径(00 §4 PJ0 + §5.1 立项判定):全过

| 验收项 | 实测 | 命令 |
|---|---|---|
| 三 build vet 通过 | ✅ | `go vet ./...` / `go vet -tags "wangshu_p3 wangshu_profile" ./...` / `go vet -tags wangshu_p4 ./...` |
| lint 0 issues | ✅ | `golangci-lint run ./...` |
| `bridge.P3Compiler` 接口编译期断言 | ✅ | `code.go` 末:`_ bridge.P3Compiler = (*Compiler)(nil)` / `_ bridge.GibbousCode = (*p4Code)(nil)` |
| SupportsAllOpcodes 全 false | ✅ | `TestPJ0_SupportsAllOpcodesAlwaysFalse`(5 档 opcode 形态全断言 false) |
| Compile 返 ErrCompileNotImplemented | ✅ | `TestPJ0_CompileReturnsNotImplemented`(防御性兜底——bridge 不应在 PJ0 调到这里) |
| 接口契约容忍 nil feedback | ✅ | `TestPJ0_CompileToleratesNilFeedback`(承 P3Compiler 接口契约) |
| **P4 build 全套测试套与 P1-only 等价** | ✅ | `make test-p4` 跑 20 个 `.test` binary 全过(含 conformance / difftest / luasuite);**关键防线**——「行为等价 P1」是 PJ0 验收口径,与 V1-V13/V17/V18 不豁免对齐 |

### 5.5 主助理裁决项落地

| # | 裁决项 | 决议 |
|---|---|---|
| 1 | PJ1 是否落地 JMP | ✅ 含 JMP(00 §4 PJ1 6 op,与始话对齐;forwardJump fixup 表在 PJ1 同批落地) |
| 2 | P3+P4 共存 build tag 协议 | ✅ **互斥 build tag**(`wangshu_p3` 与 `wangshu_p4` 不允许同时启用);默认 build = P1-only;build-test-bins.sh 拒共存 |
| 3 | darwin/arm64 W^X 翻面方式 | ⏳ **PJ1 同步 spike**(MAP_JIT + pthread_jit_write_protect_np vs RW→RX seal 二折一);PJ0 阶段不涉及实装 |
| 4 | JMP forwardJump fixup 表 | ⏳ PJ1 实装时设计(接口已留 `[]forwardJump` 占位) |
| 5 | RJ-29 主助理决议 | ⏳ 留 PJ0 立项判定数据进档时同批决议(本期暂不落入 P2 总览) |

### 5.6 后续 PJ 路标

PJ1 启动条件:**真实宿主负载需求 + 资源到位**(②③ 前置)。PJ0 已铺好 bridge 注入接口、Makefile 工程化、测试套不豁免基线;PJ1+ 启动时无需返工 build 系统,直接补 amd64 trampoline + 直线模板 + W^X spike 即可。

承 [01 §3.4 三档策略](./01-launch-judgment.md):若 ②③ 一直未到位,P4 最终决议「跳过」时本节作未来重启的「PJ0 已交付,PJ1+ 待启」存档。

---

## 6. PJ1 实装对账(2026-06-25 部分,承 §1 PJ 表头注)

**状态**:🔶 **PJ1 部分**——spike 闸门 🟢 + amd64 mmap+W^X+trampoline+emitter 主库版 + LOADK/RETURN 单测 byte-equal **已落地**;**未接入 GibbousCode.Run end-to-end byte-equal**——SupportsAllOpcodes 仍全 false,完整端到端接入留 PJ2+(承下文 §6.4 范围裁决)。

### 6.1 spike 闸门 🟢(承 06 §1.7)

`spike/p4tramp/`(独立 module,镜像 spike/p3boundary / spike/p3indirect):

| 闸门 | 内容 | 实测 |
|---|---|---|
| ① | exec mmap + W^X 翻面工作 | unix.Mmap PROT_RW → 写 9 字节 mov+ret → unix.Mprotect PROT_RX ✅ |
| ② | trampoline 进出对称 | S2 同段 10000 次 + S3 8 段 100 轮交叉 全过 ✅ |
| ③ | 单条直线模板可发射可执行 | S1 5 档 imm64(0/1/0xdeadbeef/0xcafebabedeadbeef/^uint64(0)) ✅ |
| ④ | 单 CALL ~1.95 ns/op | 比 P3 wazero S1 18.9ns 快 ~10x;P4 自管 codegen 物理收益首次实证 ✅ |

决策报告归档:`spike/p4tramp/DECISION.md`(对位 spike/p3indirect/DECISION.md)。

### 6.2 主库 amd64 后端落地(`internal/gibbous/jit/amd64/`)

| 件 | 落地 | 文件 |
|---|---|---|
| codepage(mmap + W^X 翻面 + munmap) | ✅ linux/amd64 | `codepage_linux.go`(主)+ `codepage_other.go`(其它平台占位 stub) |
| trampoline asm(Plan 9,NOSPLIT|NOFRAME) | ✅ linux/amd64 | `trampoline_amd64.s`(go:noescape)+ `trampoline_linux_amd64.go`(Go 端 callJIT 声明)+ `trampoline_other.go`(其它平台 panic stub) |
| emitter(EmitMovRaxImm64 + EmitRet) | ✅ amd64 | `emitter.go`——LOADK + RETURN 直线模板原语;PJ2-PJ7 渐进扩(MOVE / 算术 / 表 IC / 控制流) |
| 单测(端到端 mmap → 执行 → 返回) | ✅ | `emitter_test.go`:TestPJ1_Emitter_MovRaxRet(5 档 imm)/ TestPJ1_RepeatedCalls(1000 次)/ BenchmarkPJ1_CallJIT(实测 1.96ns,与 spike 1.95ns 一致) |

### 6.3 与 spike 形态的关系

主库 amd64 后端 = spike/p4tramp 同款形态 + per-Proto 段释放策略(Munmap + Length API)。**PJ1 简化形态**(承 spike DECISION.md「极简形态的限制」):
- 不切自管栈、不装 jitContext / r14=arena base / rbx=值栈 base;
- 不保存 callee-saved(`r12-r15/rbp` Go ABI0)——因模板只跑 mov+ret 不动它们;
- 不带 GC 安全点纪律(段瞬时执行,Go runtime 异步抢占落 mmap PC 不可恢复——这正是 PJ2+ 完整版要解的);
- 不引入 codeAddr 校验(nil ptr / 越界检查留 PJ2+ 完整版)。

**PJ1 简化形态适用范围**:LOADK / RETURN 类「无 helper / 无栈帧 / 无外部状态」直线模板。这些 opcode 落地能让 trampoline 单一 CALL+RET 即可,不需要完整 jitContext。PJ2 算术 / PJ4 表 IC 引入 helper 调用时,trampoline 升级到完整版。

### 6.4 范围裁决:为何 SupportsAllOpcodes 仍全 false

PJ1 设计原意「直线 Proto 升层后 byte-equal」需要:
1. ✅ amd64 mmap+W^X+trampoline+emitter 工程组件(本节 §6.2)
2. ❌ **GibbousCode.Run 接入 crescent 值栈写回** —— 让 LOADK 烧入的 imm 真的写到 R(A) 槽位,RETURN 把帧弹出
3. ❌ **完整 jitContext + 切 SP** —— 让模板能从 r14/rbx 读 arena base / 值栈 base

第 2/3 项的复杂度峰值在 trampoline 切 SP 与 jitContext 装载,这块不是 PJ1 简化形态的能做(spike 极简形态明示不验)。**真正的 PJ1 「直线 Proto byte-equal」需要 PJ2 完整 trampoline 同批落地**——单纯 PJ1 范围内做完工程组件而 SupportsAllOpcodes 提前开放(让 LOADK/RETURN 走 P4 路径)会导致 GibbousCode.Run 写不回值栈、产生静默错果(非 byte-equal)。

**PJ1 范围裁决**:本期交付「**spike 闸门 🟢 + amd64 工程组件 + LOADK/RETURN 单测**」三件套——这是 PJ2+ 启动的物理基础;**SupportsAllOpcodes 保持全 false**,等 PJ2+ 完整 trampoline + jitContext 同批落地后开 LOADK/RETURN 白名单。这与「闸门停下不亏」纪律对齐:即便 PJ2+ 永不启动,本期的工程组件已实证 P4 物理可行性。

### 6.5 验收口径(00 §4 PJ1 + §6.1 spike + §6.2 主库):全过

| 验收项 | 实测 | 命令 |
|---|---|---|
| spike 四档闸门 | ✅ | `cd spike/p4tramp && go test -v ./... && go test -bench=. -benchtime=2s -count=3 ./...` |
| 主库 amd64 后端编译 | ✅ | `go build -tags wangshu_p4 ./...`(三 build vet + lint 全过) |
| 主库 amd64 单测 | ✅ | `go test -tags wangshu_p4 ./internal/gibbous/jit/amd64/...`(TestPJ1_Emitter_MovRaxRet 5 档 + TestPJ1_RepeatedCalls 1000 次) |
| 主库 amd64 性能基线 | ✅ | `go test -tags wangshu_p4 -bench=BenchmarkPJ1 ./internal/gibbous/jit/amd64/...`:1.96 ns/op(与 spike 1.95ns 一致) |
| **P4 build 全套测试套与 P1-only 等价** | ✅ | `make test-p4` 跑 21 个 .test binary 全过(新增 internal-gibbous-jit-amd64.test);**PJ0 防线延续**——SupportsAllOpcodes 仍全 false ⇒ 行为等价 P1 |

### 6.6 后续 PJ 路标(承 §6.4 范围裁决)

PJ2+ 启动条件:**真实宿主负载需求 + 资源到位**(承 §5.1 立项判定 ② ③ 前置)。PJ1 已铺好:
- spike 闸门 🟢 实证 P4 物理可行性(4 闸门 + 性能基线 1.95ns);
- amd64 工程组件(codepage + trampoline + emitter)已落地;
- 单测路径(端到端 mmap→执行→返回)已验证;

PJ2 启动时直接补:
1. `internal/gibbous/jit/jitcontext.go`(jitContext struct,承 05 §3.3)
2. `trampoline_amd64.s` 升级到完整版(切 SP + 装 jitContext + 保存 callee-saved)
3. `emitter.go` 扩 ADD/SUB 投机模板 + IsNumber×2 guard(承 03 §2 + 06 §3.2)
4. SupportsAllOpcodes 开 LOADK/RETURN/MOVE/LOADBOOL/LOADNIL/JMP 白名单
5. GibbousCode.Run 接入 crescent 值栈(end-to-end byte-equal)

承 [01 §3.4 三档策略](./01-launch-judgment.md):若 ② ③ 一直未到位,P4 最终决议「跳过」时本期产出作未来重启的「PJ1 工程组件已落地,PJ2+ 待启」存档。

---

## 7. PJ2 / PJ3 工程基础对账(2026-06-26 落地,承 §1 PJ 表头注)

**状态**:✅ **PJ2 投机模板 12+ 形态完整接入** + 🔶 **PJ3 工程基础 + 物理 spike**(真接入 FORLOOP 字节级内联留下一会话)。

### 7.1 PJ2 投机模板完整接入(2026-06-26)

| 形态 | 模板字节数 | 实测加速比 | 命中探针 |
|---|---|---|---|
| reg-reg ADD/SUB/MUL/DIV | 92 字节(guard×2 + 快路径 + deopt block) | 1.01-1.03x | SpecRegRegHits |
| reg-K   ADD/SUB/MUL/DIV | 73 字节(单 guard + K imm64 烧入 + 快路径 + deopt) | 1.01-1.02x | SpecRegKHits |
| chain-KK 任意 op1+op2  | 92 字节(单 guard + K1/K2 imm64 + 双 SSE binop 复用 xmm0) | ~1.0x(单调内 boundary 主导) | SpecChainHits |

**字节级单测**:每形态 mmap+RX round-trip + 双轨 fast/deopt + 字节级 byte-equal Intel SDM 编码。

**e2e 双轨真升层**:每形态 fast-path(双 number byte-equal 解释器)+ deopt-path(non-number 触发 IsNumber guard 失败 → host.Arith × N → raise byte-equal 解释器报错)。

**关键 bug 修复**:chain 形态 pc 实参 retPC-2 锚定 op1 真实位置(retPC-1 错位到 op2)——既存慢路径与新增 spec deopt 路径双向修正,对齐错误消息行号 byte-equal。

**白盒命中探针**:`jit.SpecRegRegHits / SpecRegKHits / SpecChainHits + ResetSpecHits`——e2e 测试断言投机模板真编译被走到(非降级 host helper 假绿)。

### 7.2 PJ3 工程基础 + 物理 spike(2026-06-26)

| 工程组件 | 字节数 | 落地状态 |
|---|---|---|
| EmitCmpByteR15DispImm8(safepoint check primitive) | 8 字节(41 80 BF disp32 imm8)| ✅ + 字节级单测 |
| EmitIncReg64 / EmitDecReg64(整数计数器累加)| 3 字节(48 FF C0/C8+rd)| ✅ + 5 档单测 |
| EmitMovReg64Imm32SignExt(短 imm32 装 r64)| 7 字节(48 C7 C0+rd imm32)| ✅ + 4 档单测 |
| PatchRel32(forward jmp fixup tool)| - | ✅ + 单测(0x12345678 / -1 / 复合 jne+body 模式) |
| JITContextOffsets(字段偏移常量)| 4 个 unsafe.Offsetof | ✅ |

**spike 字节级物理证据**(`emitter_pj3_loop_spike_amd64_test.go`):

mmap+RX 段内 emit:
```
mov rax, 0; mov rcx, N
loop_start:
  inc rax
  cmp byte [r15+preemptFlagOff], 0
  jne after_loop                       ; forward fixup (emit-then-patch)
  dec rcx
  jne loop_start                       ; backward jmp (negative rel32)
after_loop: ret
```

**实测验证**:
- Normal 路径(preemptFlag=0):N=100 全跑完,rax=100 ⇒ backward jmp 真在 mmap+RX 跑通 99 次
- EarlyExit 路径(preemptFlag=1):第一次 cmp 即触发 jne after_loop,rax=1 ⇒ safepoint check + r15 装载 + byte cmp **真生效**

**prove-the-path 硬证据**:`spikeCtxInstance.preemptFlag` 0 → 1 真改变执行路径(rax 100 → 1),非降级路径,emit-then-patch 模式实证成功。

### 7.3 PJ3 真接入 FORLOOP 字节级内联剩余工程

工程基础已 ~90% 齐备,剩余:
1. `analyzeForLoopForm` CFG 识别:FORPREP-body-FORLOOP 闭环 + body ⊆ SupportsAllOpcodes
2. emit FORLOOP 浮点 idx+step / ucomisd limit / 回边 backward jcc / safepoint check(emit 原语全齐)
3. exit stub:deopt 时写当前 R(A) idx 槽 + 跳回 host helper
4. p4Code.Run 路径接入(段内自循环,Run 等同一次进一次出,无需结构改动——本会话推导出 spike 形态)

留下一会话推进真接入主路径。

---

## 8. PJ3 FORLOOP 字节级 inline 真接入 + luajc 档突破(2026-06-26 落地)

### 8.1 最简形态

`function() for i=K1, K2 do end end`(全常量 init/limit/step + 空 body FORLOOP)。承 §7.2 spike 物理证据 → §7.3 路标 → 本节真接入。

### 8.2 字节级模板(amd64,69 字节)

```
[ 0] mov rax, K_init imm64; movq xmm0, rax   ; xmm0 = init
[15] mov rax, K_limit imm64; movq xmm1, rax  ; xmm1 = limit
[30] mov rax, K_step imm64; movq xmm2, rax   ; xmm2 = step
[45] subsd xmm0, xmm2                        ; FORPREP 预减 idx
[49] ; loop_start
[49] addsd xmm0, xmm2                        ; FORLOOP idx += step
[53] ucomisd xmm0, xmm1                      ; cmp idx, limit
[57] ja  after_loop                          ; idx > limit → exit
[63] jmp loop_start                          ; backward jmp
[68] ; after_loop
[68] ret
```

### 8.3 形态识别(analyzeForLoopForm)

约束:proto.Code 长度 6/7 + 三 LOADK + FORPREP sBx=0 + FORLOOP sBx=-1 + RETURN A=0 B=1 + 三 K 都是 number + step > 0。

### 8.4 主路径接入

- `compiler.go::Compile`:isForLoop=true 走 archEmitForLoopEmptyConst,p4Code 设 writeRetA=false / useSpec=false(段内自循环,无 host helper)
- `arch_amd64/arm64/other.go`:archEmitForLoopEmptyConst 路由(amd64 真实装,arm64/other stub)
- `probes.go`:SpecForLoopHits / incSpecForLoopHits / ResetSpecHits 加 forLoop 字段

### 8.5 验证

| 层 | 测试 | 结果 |
|---|---|---|
| 字节级单测 | `pj3_template_amd64_test.go` 7 档 mmap+RX round-trip(1..100 / 1..1000 / 1..10000 / 1..10 step 2 / 1..100 step 0.5 / 单次迭代边界 0..0 / 1..1)| ✅ 全过 |
| e2e 真升层 | `gibbous_pj3_forloop_e2e_test.go` 3 档(100 / 1 / 1000 iter)+ SpecForLoopHits>0 实证模板真编译 | ✅ 全过 |
| make test-p4 全套 | 含 conformance/difftest/luasuite/-race + 3 PJ3 e2e | ✅ 全过 |

### 8.6 luajc 档实测加速比(Xeon 6982P,2s × 2 count,wangshu_p4 wangshu_profile)

形态对位 wrap-kernel × 50(避免 apples-to-oranges 工作负载错配):

| iter 次数 | P4 (ns/op) | crescent (ns/op) | gopher-lua (ns/op) | **P4 vs cres** | **P4 vs gopher** |
|---|---|---|---|---|---|
| 100 | 7,311 | 59,334 | 52,272 | **8.11x** | **7.15x** |
| 1000 | 31,313 | 548,831 | 663,911 | **17.53x** | **21.20x** |
| 10000 | 271,426 | 5,452,279 | 6,895,610 | **20.09x** | **25.41x** |

**PJ10 luajc 档(≥4.4x over gopher-lua)早已超越**——100/1000/10000 iter 三档分别 7.15x / 21.20x / 25.41x,均远超 4.4x luajc 档基线。

**机理**:PJ3 把 FORLOOP 字节级 inline 进 mmap 段,1000/10000 次回边完全不跨 boundary——仅 SSE addsd + ucomisd + backward jmp 三指令循环,~3 cycles/iter,接近原生码理论上限。boundary cost 只在 enter/exit 段各一次,wrap-kernel × 50 ⇒ N 越大 boundary 占比越小。

### 8.7 当前 PJ3 形态范围 + 后续扩展

**当前已落地**(三类 FORLOOP 形态完整 byte-equal P1):
- ✅ LOADK 常量 limit:`for i=1,100 do end`(69-83 字节)
- ✅ MOVE reg-limit hot path:`for i=1,n do end` 参数(117 字节 + IsNumber guard + host.ForPrep deopt)
- ✅ GETUPVAL upvalue-limit:`local n=100; local function f() for i=1,n do end end`(Run prelude + reg-limit 模板复用)

**留 PJ3+ 扩**:
- ⏳ body 含 reg-K spec op(reg-K 模板已就绪,inline 到 loop body 内需寄存器分配 + R(A) 写槽 emit)
- ⏳ 嵌套 / break(JMP)

### 8.8 reg-limit hot path benchmark(2026-06-26 实测)

形态:`function(n) for i=1, n do end end`(wrap × 50 调 kernel(N))

| iter | P4 (ns/op) | crescent (ns/op) | **P4 vs cres** | **P4 vs gopher** |
|---|---|---|---|---|
| 1000 | 33,193 | 578,891 | **17.44x** | **20.00x** |
| 10000 | 286,293 | 5,767,130 | **20.15x** | **24.09x** |

reg-limit hot path 形态 P4 真接入与全常量空 body 形态加速比相当(20-24x),只多一次 IsNumber guard 微小开销 ~50ns 一次性。**真实生产 hot path 形态(参数传 limit)完整突破 luajc 档**。

---

## 9. 后续维护协议

PJ0 启动后,本文按以下协议更新(承 [P3 implementation-progress §5](../p3-wasm-tier/implementation-progress.md) 范本):

1. **PJ0 立项判定数据进档**:立项报告(三档决议 + 真实宿主需求确认 + 资源到位证据 + P3 现状数据复核)永久记录在本文,无论结果如何——这是 P4 是否启动的依据,必须可追溯(承 [01 §5.3 数据进档协议](./01-launch-judgment.md));
2. 每个 PJ 完成时,把对应行 ⏳ 改 ✅,加完成提交哈希;
3. 实际落地与设计文档有差异时,加「实现现状与设计文档差异对账表」(P3 同款 §6 / §7 / §8 / ... 节);
4. 跨文档回填请求(§2)逐项实施,把对应行从「⏳ P4 PJx 落地时同批补」改「✅ 已落地」+ 提交哈希;
5. PJ10 总验收过线后,本文头部状态改「P4 已交付」+ 验收数字汇总(luajc 档 + V1-V22 全过);
6. **若 PJ0 立项判定否决**:本文记录「P4 跳过」决策 + 判定数据;P4 设计文档集转为「未来再启动时的参考资产」(子目录 10 文件 8200 行作未来重启的设计基线,与 P3 spike 不达标后「跳跃路径」的资产复用形态同源);
7. **若 PJ10 验收为「P3 留中层」**:RJ-12 触发(承 §2.2),P2 04 considerPromotion 接口扩展加平台维度;否则 RJ-12 自动消解(决策为退役时不触发);
8. **若 PJ3 / PJ7 内部第二闸门未达标**:承 [01 §4.3](./01-launch-judgment.md) 中途校验纪律,记录「P4 止损」决策 + 数据,可能改 P5 路径或退守 P3 永久基线。

---

相关:
- [00-overview](./00-overview.md)(P4 总览,本文是其 §4 PJ 表的运行期对账 + §6 跨文档定稿决策收口)
- [01-launch-judgment](./01-launch-judgment.md)~[08-testing-strategy](./08-testing-strategy.md)(各子系统设计文档,本文 §2 聚合其 §回填请求节)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款,作维护协议参考)
- [../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 同款,作维护协议参考)
- [../p3-wasm-tier/implementation-progress](../p3-wasm-tier/implementation-progress.md)(P3 同款,作维护协议范本)
- [../../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策 + 单点收口的纪律来源)
- [../../../llmdoc/guides/prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md)(投机/OSR/deopt 路径白盒命中纪律——P4 落地时全程生效)
- [../../../llmdoc/guides/perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)(§7 profile 才是合同——P4 PJ10 调优纪律)
- [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P4 启动前置确认 / P4 落地时回填项的长期登记点)
