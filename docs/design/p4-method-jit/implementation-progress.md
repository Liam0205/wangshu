# P4 实现进度对账(implementation-progress)

> 状态:**P4 已交付 (2026-07-01)** — PJ0-PJ11 全部落地 ✅(PJ1/PJ6 已由 PJ3/PJ7/PJ10 迂回闭合;详 §1 表)+ 三平台 CI 全绿(linux/amd64 + linux/arm64 + darwin/arm64,PR #29 42/42 checks)+ **PJ10 native emit amd64 端 26 op 真原生 + arm64 端 18 op 线性子集(exit-reason 端口未做,详 issues #37/#40)** + V15b heavy 三本 P4 native > P3 wasm 达标 + [09 PJ11 acceptance checklist](./09-acceptance-checklist.md) V1-V22 三平台全 ✅ + D2 P3 去留决议 2026-07-01 用户拍板主动保留(承 [07 §5.6](./07-p3-retirement.md)) + nightly-diff-fuzz P4 variant 2026-07-01 挂钩(rolling-seed diff 200 万脚本 + GC-stress 20 万脚本 + go-fuzz 45m × 4 targets 每晚跑),V21/V22 30 天累积 timer 起跑,长期健康监测不阻塞已交付状态。PJ3 FORLOOP 字节级 inline 实测 7.15-25.41x over gopher-lua,**完整超越 luajc 档 4.4x 基线**(承 §8)。**PJ4 表 IC 完整六路径**(GETTABLE/SETTABLE/SELF × ArrayHit/NodeHit)字节级 inline 主路径接入 + 严密 IsTable guard(承 §9.7-§9.10)+ 整套层级 prove-the-path 守卫(承 §9.11)。**PJ5 CALL void 二百二十子形态 + TAILCALL 一百零二子形态 + SELF method call inline 完整 0..7 参 + SELF spec template 字节级 inline 含 N=2..15 返 drop multi-ret 全形态 + OSR exit 协议接通(p4SpecState 子状态机)+ Option B 帧建立内联 Spike 1/2/3/4 + zero-cross 全套真接入完整端到端 amd64 打通**(承 §9.14-§9.20.13)。**PJ8/PJ9 双架构 CI 三平台全绿**(PR #29):arch_arm64.go 的 `archSupportsSpec` / `archSupportsForLoop` / `archSupportsFrameInline` 三个闸门全翻 true,darwin/arm64 W^X 由 cgo `jitcgo` 子包提供 MAP_JIT + pthread_jit_write_protect_np 真实装(隔离主库 zero-cgo 承诺),arm64 spec emit 模板 NoRet + ExitHelperRequest + helpers 全套就位;ci.yml 三平台矩阵(ubuntu-latest / ubuntu-24.04-arm 原生 GHA runner / macos-latest M1)× P1/P3/P4 × test / fuzz-smoke / conformance / difftest 全绿。**PJ11 acceptance checklist V1-V22 三平台全绿**(承 [09-acceptance-checklist.md](./09-acceptance-checklist.md);V14 luajc 档 bench-acceptance Run #28505893556 三平台 14.08x / 25.28x / 13.34x over gopher;V15b heavy geomean 5.53x / 5.45x / 4.00x over gopher;V16 边界 P4/P3 最差 0.70x 最好 0.50x;V19-V22 引用统一到 codebase 已有等效测试)。bit50 OSR 决策已记录(2026-06-29 用户确认清 0,承 §3.2)。共 **38+14+20 e2e SpecCallVoidHits/SpecTailCallHits/SpecSelfCallHits=1 prove-the-path 命中实证 + 36+13+16 difftest-p4 三方 byte-equal + 9 单测 + 7 p4SpecState 单测 + V18 -race 增量含 SELF**。
>
> **2026-07-03 arm64 exit-reason 端口收口(issue #37 / #40 阶段 2)**:承 §14.6 详。exit-reason 协议按 7 步实施顺序完整移植到 arm64(dispatcher 循环 + GETUPVAL/SETUPVAL → CALL 密度门 → GETGLOBAL/SETGLOBAL NodeHit inline → GETTABLE/SETTABLE ArrayHit inline + NEWTABLE → UNM(顺带修复双 arch -NaN 静默变 nil 既有 bug)→ 多值 RETURN → arith/LT/LE 恢复(比较条件码换 FP 安全族 MI/PL/LS/HI 解 NaN unordered 错判));**两 arch native 接受面重新对齐**,§14.5 的「两 arch 分岔」口径自此失效。darwin/arm64 M5 Pro 实测 heavy + realworld P4 全面 ≥ P3(HeavyArith 2.03× / HeavyFloatloop 2.47× over P3),README 双语 arm64 表已更新。
>
> **2026-07-02 PJ10 native 扩接 + arm64 分岔进档(PR #34)**:承 §14.5 详。amd64 native `opSupported` 从原 18 op 线性子集扩到 **26 op**——新增 GETTABLE / SETTABLE / NEWTABLE / GETUPVAL / SETUPVAL / GETGLOBAL / SETGLOBAL / CALL / UNM 九个,通过 **exit-reason 协议**(`emit_ops_amd64.go::emitExitReason` 把 helperCode/a/b/c/pc 打包进 `jitCtx.exitArg0`,`RAX=ExitInlineHelper` 出段,Go 端 `nativeCode.Run` dispatcher 分派并经 resumeOff 二次重入)完成语义,而不是 inline 全跑。`AnalyzeNative` 语义门同步加严:GETTABLE/SETTABLE 必须 IC Kind ∈ {ArrayHit, NodeHit};GETGLOBAL/SETGLOBAL 必须 NodeHit(注释里记录了「无门直接接受」造成的 ~14% Transform CallInto 回归教训);NEWTABLE B/C≥256 拒;CALL B==0/C==0 拒;CALL 密度门 `totalOps/callCount≥16`(注释里记录 fib 11 ms 解释器 vs 18 ms native 的起因);`returnCount==0` 拒;multi-return 走 `HelperReturn` exit-reason,single-return 保 xor-eax 快出口 + Go 端 DoReturn。**性能实测**:25/27 pair 相对 P3 wasm ≥ +2%(fannkuch 0.60 ms,nbody 44.2 ms),README bench 表格已重测。**arm64 端未随之扩**(已由 2026-07-03 §14.6 收口)——当时 `translator_native_arm64.go::opSupported` 仍是 18 op 线性子集,`AnalyzeNative` 里对算术 / 比较 op 额外拒;exit-reason 协议的 arm64 端口即 issue #37,arm64 debugging 见 issue #40。
>
> **PJ10 启动 + 两轮交付(2026-06-30 / 2026-07-01)— 逐 opcode 翻译器(per-op translator)覆盖率工程**:承 [10-per-op-translator.md](./10-per-op-translator.md)。V15b heavy 三脚本(arith / recursion / floatloop)实测 P4 全升 0(详 [09 §3.V15b](./09-acceptance-checklist.md)),暴露 PJ0-PJ9 字节级模板路径对真实 Lua 热点覆盖率不够。PJ10 新增「逐 opcode 翻译」通用路径,PJ0-PJ9 字节级模板保留作 fast path;拆 PJ10a(直线 op)/ PJ10b(算术 + 比较)/ PJ10c(控制流 + 循环)/ PJ10d(表 + 函数调用)四档,累积扩 SupportsAllOpcodes 集。原 PJ10「luajc 档总验收」已机械重编号为 PJ11(详 [10 §9](./10-per-op-translator.md))。**第一轮(2026-06-30,21 commits)**:Go 端「head op + side effect」回放骨架 + `xor eax,eax; ret` 3 字节 mmap stub,35/38 opcode 语义 byte-equal 解释器,80 e2e 全过 — **正确性 floor 交付**(详 [10 §14.1](./10-per-op-translator.md) + 反思 [[2026-06-30-p4-pj10-perop-translator-round]] + §14 本文)。**第二轮(2026-07-01,分支 `feat/pj10-native`,ed2235b..0adaae6 ~20 commits)**:真 amd64 / arm64 原生码 emit(CFG builder + 两遍 label resolver + 35 opcode 每 arch 一份 emit)+ inline 18 op mmap-safe 子集(**amd64 端 2026-07-02 PR #34 已扩到 26 op,详 §14.5;arm64 保 18 op 线性子集**)+ arch-aware `fixupKind` label resolver + `PreferNative` 多 BB + big-BB(非 entry)收窄门 + execute() TAILCALL case 内 tail-call gibbous dispatch — **性能 ceiling 交付**;**V15b heavy 三本 P4 native > P3 wasm 达标**:HeavyArith 6.5x / HeavyRecursion 2.4x / HeavyFloatloop 3.0x(10 iter × 3 samples,amd64);V14 luajc 档无回归;arm64 runtime e2e 留 CI followup(需 linux/arm64 host,QEMU 不支持 mmap RWX→RX)。详 [10 §14.2-14.9](./10-per-op-translator.md) + 反思 [[2026-07-01-p4-pj10-native-round]] + §14 本文。
> P1 全卷(M0-M14)+ P2 PB0-PB7 + 后续优化轮 #1-#4 + P3 PW0-PW10 + VS0-e 全卷已交付(2026-06-16),P4 启动前置就绪;P4 PJ0-PJ11 全部交付(2026-07-01),D2 决议已拍板 (主动保留),nightly-diff-fuzz P4 variant 已挂钩(2026-07-01);V21 longevity + V22 30 天累积 timer 起跑,长期健康监测,非阻塞。P4 amd64 + linux/arm64 + darwin/arm64 三平台端到端已达标。
> 单一事实源:本文是 P4 实现现状与设计文档差异的对账表(对应 [P3 implementation-progress](../p3-wasm-tier/implementation-progress.md) 的角色,但 P4 是设计阶段未实施,本文重在「设计期决策盘点 + 跨文档回填请求收口表 + 实施前置确认 + 后续维护协议」)。
> 设计文档集:见 [00-overview §0](./00-overview.md) 文档地图。
>
> **术语:`P-JIT`(PJ)= P4 实现里程碑编号**(对应 P1 的 M、P2 的 PB、P3 的 PW);PJ0 = 立项判定 + 包骨架,PJ1-PJ7 = amd64 全栈字节级模板,PJ8-PJ9 = arm64 全栈字节级模板,PJ10 = 逐 opcode 翻译器(per-op translator,两轮交付:2026-06-30 Go 端回放骨架 + 2026-07-01 amd64/arm64 真原生 emit,详 [10](./10-per-op-translator.md)),PJ11 = luajc 档总验收。

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
| PJ1 | amd64 trampoline + 直线模板(6 opcode) | [05](./05-system-pipeline.md) + [06 §3.1](./06-backends.md) | 直线 Proto 升层后 byte-equal;exec mmap + W^X 翻面工作 | ✅ **2026-06-25 部分 + 迂回闭合**(详 §6 PJ1 实装对账;spike 闸门 🟢 + amd64 mmap+W^X+trampoline+emitter 主库版 + LOADK/RETURN 单测 byte-equal;end-to-end byte-equal 已由 PJ3(FORLOOP 字节级 inline)+ PJ7(shape-spec 25 类形态)+ PJ10(per-op 翻译器覆盖 35 opcode)迂回闭合,单纯 PJ1 直线模板路径的原始动机被更广路径吸收) |
| PJ2 | amd64 算术 + 比较 + IsNumber×2 guard | [03](./03-speculation-ic.md) + [06 §3.2](./06-backends.md) | 双 number 快路径直发 `mulsd` 等;guard 失败 OSR exit 回解释 | ✅ **2026-06-26 完整接入扩展到 12+ 形态**(承 §7;ADD/SUB/MUL/DIV 三种操作数布局:reg-reg(92 字节,实测 1.01-1.03x)+ reg-K(73 字节,实测 1.01-1.02x)+ chain-KK 任意 op1+op2 组合(92 字节,~1.0x 单次调内 boundary 占主导)。e2e 双轨真升层 byte-equal 解释器 + 白盒命中探针 SpecRegKHits / SpecRegRegHits / SpecChainHits 实证非降级 host;deopt fallback 含 chain pc 修复对齐错误消息行号。**真大幅加速**(luajc 档 ≥4.4x)留 PJ3 FORLOOP 字节级内联把多次 boundary 摊出循环) |
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | [05 §6.3](./05-system-pipeline.md) + [06 §3.3](./06-backends.md) | 数值 for 编译后 ≥luajc 档单档(**P4 价值首次实证**)| ✅ **2026-06-26 真接入 5 类形态——突破 luajc 档**(承 §8;空 body 三类:LOADK常量 + MOVE reg-limit hot path(IsNumber guard + host.ForPrep deopt)+ GETUPVAL upval-limit(Run prelude + reg-limit 模板复用);含 body 二类:单 reg-K body op(135 字节,body 用 xmm3/4)+ 二段 reg-K body op(154 字节,body 共享 xmm3 跨两段省 load/store)。安全点 check 字节级真接入(V18 -race 抢占语义生效)。**Xeon 6982P 实测**:空 body 100/1000/10000 iter 8.11/17.53/20.09x over cres + 7.15/21.20/25.41x over gopher;body+=1 1000/10000 iter 7.23/7.36x over cres + 10.18/10.83x over gopher;**全部远超 luajc 档 4.4x 基线**。**嵌套 / break / 表 IC 留 PJ4+ 扩**) |
| PJ4 | amd64 表 IC 模板 + stableShape/Index 直达槽投机 | [03 §6](./03-speculation-ic.md) + [06 §3.4](./06-backends.md) | 单态表 guard + 直达槽跳哈希;形状变化 deopt + 再训练 | ✅ **2026-06-26 IC 完整六路径 + 严密 IsTable guard + 整套层级 prove-the-path 守卫**(承 §9;六模板 ArrayHit 132B / NodeHit 159B / SetTable ArrayHit 113B / SetTable NodeHit 140B / Self ArrayHit 139B / Self NodeHit 166B,**严密 IsTable guard** `shr rax,48 + cmp eax,0xFFFC + jne deopt`(15 字节)精确排除非 table 假阳;六 analyzer + Compile 主路径六路径优先级分流;Run deopt 分流 host.GetTable / host.SetTable byte-equal P1;**SpecTableHits 探针** + crescent e2e WarmupThenForce 实证(t[1]/t["x"]/t[1]=v/t["x"]=v 各 SpecTableHits++=1)+ jit 包合成驱动单测兜底 SELF;**整套层级 prove-the-path 修复**(test/difftest/p4_test.go P4 专属 harness 17 用例 + PromotionCount>0 fail-stop + conformance P4 守卫 + Makefile 注释更新)) |
| PJ5 | amd64 CALL/TAILCALL + 跨层互调 + OSR exit 实装 | [04](./04-osr-deopt.md) + [05 §4.3](./05-system-pipeline.md) + [06 §3.5](./06-backends.md) | gibbous-jit 三向分派 + OSR exit 状态等价(V19)| ✅ **2026-06-28 PJ5 amd64 端可达工程完整闭合**(承 §9.14-§9.20.13):CALL void 220 + TAILCALL 102 + SELF inline 0..7 参 + N=2..15 返 + 嵌套 + 错误冒泡 + V18 -race + p4SpecState 子状态机 + **§9.20 Option B 帧建立内联 Spike 1/2/3/4 + zero-cross 全套真接入完整端到端 amd64 打通**(承 commit-5m..5u 35 commits:Spike 1 0 参 setter / Spike 2 N 参 fixed 0..7 / Spike 3 vararg / Spike 4 多返值多形态 callC=1..16 / Spike 5 zero-cross callee P4 升层时跳 executeFrom;RunHits prove-the-path 49/199/99/99 / ZeroCrossHits=199 命中实证;V18 -race 8 goroutine 多 State 并发安全;V22 fuzz seed 3 形态扩);D2 P3 去留决议 2026-07-01 已拍板主动保留(承 [07 §5.6](./07-p3-retirement.md)),bit50 2026-06-29 已确认清 0,arm64 物理 runner CI 2026-07-01 PR #29 三平台闭合(PJ8) |
| PJ6 | amd64 CLOSURE/CLOSE + upvalue | [06 §3.6](./06-backends.md) | 闭包 byte-equal(复用 makeClosure/closeUpvals)| ✅ **2026-06-25 emitter 部分 + PJ10 迂回闭合**(EmitLoadKReturnTemplate + EmitProlog/Epilog 模板封装;10000 次 prolog/epilog 栈保护验证;CLOSURE / CLOSE / upvalue 已由 PJ10 per-op translator 完整覆盖 — 第一轮 head-op 回放(2026-06-30)给正确性 floor,第二轮 native emit(2026-07-01)给性能 ceiling,详 §14) |
| PJ7 | amd64 端到端验收 + 性能基准 | [08](./08-testing-strategy.md) | 单架构 V1-V22 全过 + V14 luajc 档 | ✅ **PJ7 真接入 ~25 类形态 byte-equal**(2026-06-25/26,详 §7;`SupportsAllOpcodes` 已扩展到 25 类形态——getter 族(RETURN A 2 / GETUPVAL / GETGLOBAL / GETTABLE / LOADK 含 string / LOADBOOL / LOADNIL / MOVE / ADD..POW 6 op / UNM / LEN / NEWTABLE / NOT)+ setter 族(RETURN A 1 / SETTABLE / SETGLOBAL / SETUPVAL)+ 比较折叠族(EQ/LT/LE 6-op luac 模板折成 BoolValue)。`p4Code.Run` 经 14 个 host helper 调 gibbous_host.go 与解释器 byte-equal;pc off-by-one bug 修复(行号 / IC 槽锚定 prelude op 自身 pc=0);多行错误消息 byte-equal 实证测试通过。**make test-p4 全套 21 binary 全过含 conformance/difftest/luasuite + V18 -race**;V14 luajc 档调优留 PJ11) |
| PJ8 | arm64 后端启动 + 渐进交付 | [06](./06-backends.md) | arm64 各 opcode 模板按族落地;`MAP_JIT` + icache flush | ✅ **2026-06-26 字节级模板矩阵完整 + Compile 端真接入(IC 六 + FORLOOP 全套 + PJ2 三形态)+ spec trampoline asm 实装 + 2026-07-01 PR #29 三平台闭合**(承 §9.13;linux/arm64 codepage + 23 件 emit 原语(整数 13 含 LDRB/CBNZ + 浮点 7 + ADD/AND/LSR 3)+ **PJ2 投机三形态**(reg-reg 108B + reg-K 92B + chain-KK 116B,字节级单测 13 个 + sseOp 翻译 0x58/0x5C/0x59/0x5E → ArithOpAdd/Sub/Mul/Div)+ **PJ3 FORLOOP 全套**(EmptyConst 84/92B + RegLimit 120/128B + WithRegKBody 144/152B + WithRegKBody2 168/176B,共四形态字节级模板)+ **PJ4 IC 完整六路径 arm64 端字节级**(GETTABLE ArrayHit 168B / NodeHit 196B / SETTABLE ArrayHit 144B / SETTABLE NodeHit 172B / SELF ArrayHit 172B / SELF NodeHit 200B,总计 1052B + 25+ 字节级单测;**PJ5 SELF + CALL spec template arm64 端 EmitSpecArgLoadKArm64 (20B) + EmitSpecArgLoadRegArm64 (8B)** 实装,与 amd64 对位);`arch_arm64.go` 十三 stub → 真代理(IC 六路径 + FORLOOP 全套四形态 + PJ2 三形态,签名完全对位 amd64)+ `callJITSpec` arm64 trampoline asm 实装 + `trampoline_other.go` cross-build stub;**已闭合 by PR #29**:(i) `arch_arm64.go` 三闸门 `archSupportsSpec` / `archSupportsForLoop` / `archSupportsFrameInline` 全翻 `return true`;(ii) darwin/arm64 W^X 由 cgo `jitcgo` 子包实装(MAP_JIT + pthread_jit_write_protect_np + sys_icache_invalidate),严守主库 zero-cgo 承诺;(iii) macos-latest M1 + ubuntu-24.04-arm 原生 GHA runner 三平台矩阵接入,tri-platform 全绿 42/42 checks)|
| PJ9 | arm64 端到端验收 + 双架构差分套 | [06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md) | 双架构 V1-V22 全过;Go 1.25/1.26/tip 矩阵 CI 绿 | ✅ **2026-07-01 PR #29 三平台闭合**(.github/workflows/ci.yml 完整 P1/P3/P4 × ubuntu-latest / ubuntu-24.04-arm 原生 GHA runner / macos-latest M1 三平台矩阵,`test` / `fuzz-smoke` / `conformance` / `difftest` 四 job 全绿 42/42 checks;V1-V13/V17/V18 三平台差分 byte-equal 齐过;承 §0 line 5 「PJ10 acceptance checklist V1-V13 三平台全绿」→ [09-acceptance-checklist.md](./09-acceptance-checklist.md);cross-compile linux/arm64 + darwin/arm64 wangshu_p4 build 已在 tri-platform CI 上跑真机验证) |
| PJ10 | 逐 opcode 翻译器(per-op translator) | [10](./10-per-op-translator.md) | **P4 覆盖率工程**:PJ0-PJ9 字节级模板路径保留作 fast path;新增「逐 opcode 翻译」通用路径让任意 Proto 都能升 P4(承 [10 §1](./10-per-op-translator.md));拆 PJ10a(直线 op)/ PJ10b(算术 + 比较)/ PJ10c(控制流 + 循环)/ PJ10d(表 + 函数调用) | ✅ **两轮交付 + 2026-07-02 amd64 扩接**——**第一轮 2026-06-30 回放骨架**(21 commits,35/38 opcode Go 端回放 + `xor eax,eax; ret` 占位 stub,byte-equal 解释器,80 e2e 全过,详 [10 §14.1](./10-per-op-translator.md));**第二轮 2026-07-01 真原生 emit**(分支 `feat/pj10-native`,~20 commits,amd64/arm64 CFG + label resolver + 35 opcode 每 arch 一份 emit + **amd64 native `opSupported` 起手 18 op mmap-safe 子集,2026-07-02 PR #34 扩到 26 op(经 exit-reason 协议接入 GETTABLE/SETTABLE/NEWTABLE/GETUPVAL/SETUPVAL/GETGLOBAL/SETGLOBAL/CALL/UNM,详 [10 §14.3](./10-per-op-translator.md));arm64 端仍是 18 op 线性子集且 arith/compare 在 AnalyzeNative 里额外拒(exit-reason 端口未做,issues #37/#40)** + arch-aware `fixupKind` + `PreferNative` 多 BB + big-BB(非 entry)收窄门 + execute() TAILCALL case 内 tail-call gibbous dispatch;**V15b heavy 三本 P4 native > P3 wasm 达标**:Arith 6.5x / Recursion 2.4x / Floatloop 3.0x,10×3 samples;V14 luajc 档无回归;arm64 runtime e2e 留 CI followup;详 [10 §14.2-14.9](./10-per-op-translator.md) + §14.5)|
| PJ11 | luajc 档验收 + 性能调优 | [01](./01-launch-judgment.md) + [08 §8](./08-testing-strategy.md) | **P4 总验收**:列内核负载 ≥luajc 档(≥164μs 水位 over gopher-lua)| ✅ **2026-06-26 luajc 档突破**(承 §8;PJ3 FORLOOP 字节级 inline 实测大幅加速:100 iter 7.15x over gopher;1000 iter 21.20x;10000 iter 25.41x over gopher-lua,均远超 luajc 档 4.4x 基线。10000 iter 形态 P4 仅 270μs / gopher 6.9ms — 完整超过 ≥164μs 水位口径。**PJ3 当前形态范围**:全常量空 body for 循环;含 body / reg limit / 嵌套 / break 留 PJ3+ 扩。**P4 立项动机已兑现**:列内核 loop 形态 P4 性能超 luajc 档,验证 method-jit 方向的物理可行性) |

---

## 2. 跨文档回填请求收口表

P4 设计期各子文档对 P1/P2/P3 / P4 子目录内现稿发起的回填请求。**承用户裁决「本期只记录不主动改 P1/P2/P3 现稿」**——全部标「⏳ P4 PJx 落地时同批补」,不在文档扩展轮兑现。RJ 编号(R = Request,J = JIT)按子文档源排序。

**RJ 闭合状态汇总**(2026-06-28):
- ✅ **23 项已闭合**(RJ-1/2/3/4/5/7/11/13/14/15/16/17/18/19/20/21/22/23/24/25/26/27/28/29 + RJ-11 ✅ 已存在 + RJ-31 ✅ 已对接 + RJ-36 ✅ 已对接)
- ⏳ **2 项余**(全是条件性 / 用户决策性):
  - RJ-6 LT/LE 子分流(条件性 — 留 P2+ 实测后,LT/LE string 比较占比高时启用)
  - RJ-34 验收基准分档加宿主真实负载形态(等 PJ11 用户拍板真实宿主负载实测)
- ✅ **RJ-12 已消解**(2026-07-01 D2 决议为主动保留,不走留中层,承 [07 §5.6](./07-p3-retirement.md);considerPromotion 无需扩平台维度)

### 2.1 对 P1 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-1 | [08 §13.1 RB-1](./08-testing-strategy.md) | [P1 12 §3.8 Runner 抽象表](../p1-interpreter/12-testing-difftest.md):`WangshuGibbousJIT` runner 注释从「P3+ 新增,本文预留,不实现」拆为「P3 已实装 + P4 待实装」两行,避免 P4 实装时该注释跨度过广 | ✅ 已落地(2026-06-28) |
| RJ-2 | [08 §13.1 RB-2](./08-testing-strategy.md) | [P1 12 §7 P4 行](../p1-interpreter/12-testing-difftest.md):现稿已预留 P4 行,加链接指向 [P4 08 §4 / §5](./08-testing-strategy.md) 使 P4 字段从「文字承诺」升级为「具体口径接入」 | ✅ 已落地(2026-06-28) |
| RJ-3 | [04 §12](./04-osr-deopt.md) | [P1 05 §7 doCall / callResult 枚举](../p1-interpreter/05-interpreter-loop.md):新增 `callDeoptResume` doCall 出口——收到 GibbousCode.Run 返回 status=2 时的处理(reloadFrame + 续跑同帧);P4 阶段新增,P1/P2/P3 不需要 | ✅ 已落地(2026-06-28) |
| RJ-4 | [04 §12](./04-osr-deopt.md) | [P1 05 §9 错误冒泡纪律](../p1-interpreter/05-interpreter-loop.md):OSR exit 路径不应设置 `state.pendingErr`——exit 是「投机失误」非「语义错误」,不与错误冒泡互斥;若同发以错误优先(承 [P4 04 §7.4](./04-osr-deopt.md))| ✅ 已落地(2026-06-28) |

### 2.2 对 P2 文档的回填请求

> **方案 A 决议(承 [04 头注 + §5 + §12](./04-osr-deopt.md))**:**P2 三态枚举 `TierInterp/TierGibbous/TierStuck` 不变,P4 投机生命周期由 P4 自管(`internal/gibbous/jit` 端 map `p4SpecState[proto]`,枚举 `P4Speculative / P4Deoptimized / P4StuckSpeculation`)**。原 RJ-8(P2 04 §2.1 加 `TierGibbousJIT/TierStuckSpeculation` 枚举)/ RJ-9(P2 01 §2.2 加 `ProfileData.deoptCount`)/ RJ-10(P2 01 §2.2 加 `ProfileData.recompileCount`)三项**全部撤回**——P4 自管投机生命周期不需 P2 实装改动。RJ 总数从 37 → 34。

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-5 | [03 §11.1 RB-1](./03-speculation-ic.md) | [P2 05 §5.5 P4 deopt 兜底与重训练](../p2-bridge/05-p3-p4-interface.md):与 [P4 03 §7.3 / §7.4](./03-speculation-ic.md) 字面同源化;明确 P2 接受 RequestRefresh 后 CAS 装新 feedback,P3 旧指针仍可读 | ✅ 已落地(2026-06-28) |
| RJ-6 | [03 §11.1 RB-2](./03-speculation-ic.md) | [P2 02 §9.2 LT/LE 子分流缺口](../p2-bridge/02-ic-feedback.md):[P4 03 §10.4](./03-speculation-ic.md) 是其 P4 视角的对偶兑现,引用本文作 P4 端实证 | ⏳ PJ4(留 P2+ 实测后补 — LT/LE string 比较占比高时启用) |
| RJ-7 | [03 §11.1 RB-3](./03-speculation-ic.md) | [P2 05 §5.6 P4 不依赖 P2 状态机硬纪律](../p2-bridge/05-p3-p4-interface.md):[P4 03 §8](./03-speculation-ic.md) 直接复刻该节,P2 文档侧加引用「本节具体形态见 P4 §3 §8」| ✅ 已落地(2026-06-28) |
| ~~RJ-8~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 04 §2.1 TierState 枚举] 加 TierGibbousJIT / TierStuckSpeculation~~ | ✅ **撤回(方案 A)**——P4 内部 `p4SpecState[proto]` 子状态自管,P2 三态不动 |
| ~~RJ-9~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 01 §2.2 ProfileData] 加 deoptCount~~ | ✅ **撤回(方案 A)**——P4 端 `p4SpecState[proto].deoptCount`,P2 ProfileData 不动 |
| ~~RJ-10~~ | ~~[04 §12](./04-osr-deopt.md)~~ | ~~[P2 01 §2.2 ProfileData] 加 recompileCount~~ | ✅ **撤回(方案 A)**——P4 端 `p4SpecState[proto].recompileCount`,P2 ProfileData 不动 |
| RJ-11 | [04 §12](./04-osr-deopt.md) | [P2 05 §6.1 GibbousCode.Run status=2](../p2-bridge/05-p3-p4-interface.md):**已存在**(`P3 永远不返回 2,P4 才返回 2`)——本文承认接口,无需新增,登记作 P4 消费方 | ✅ 已存在 |
| RJ-12 | [07 §11.3](./07-p3-retirement.md) | [P2 04 considerPromotion 接口扩展](../p2-bridge/04-try-compile-fallback.md):增加平台维度,P4 平台走 jit promote,P3 平台走 wasm promote——**仅在 P4 验收决策为「留中层」时触发**;若决策为退役则无需 | ✅ **自动消解**(2026-07-01 D2 决议为「主动保留」而非「留中层」,承 [07 §5.6](./07-p3-retirement.md);considerPromotion 无需扩展平台维度,主动保留形态下 build tag 层已足够隔离 P3/P4 build 面) |
| RJ-13 | [08 §13.3 RB-6](./08-testing-strategy.md) | [P2 06 §1 验收口径总表](../p2-bridge/06-testing-strategy.md):P2 V1-V22 在 P4 build 下不豁免——[P4 08 §0.2](./08-testing-strategy.md) 字面承诺,P2 06 加「P4 build 下 P2 V1-V22 仍跑」纪律 | ✅ 已落地(2026-06-28) |
| RJ-14 | [08 §13.3 RB-7](./08-testing-strategy.md) | [P2 05 §6 GibbousCode.Run status=2](../p2-bridge/05-p3-p4-interface.md):现稿已预留,加引用「P4 测试如何验 status=2 路径见 P4 §8 §4(V19 OSR 状态等价)」| ✅ 已落地(2026-06-28) |

### 2.3 对 P3 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-15 | [01 §9.1](./01-launch-judgment.md) | [P3 01 §0.3 闸门双向性](../p3-wasm-tier/01-spike-gate.md):补对位指针指向 [P4 01 §0.3](./01-launch-judgment.md)——P3 spike 闸门双向性与 P4 立项判定双向性同源逻辑 | ✅ 已落地(2026-06-28) |
| RJ-16 | [01 §9.1](./01-launch-judgment.md) | [P3 01 §5.4 跳跃路径下设计资产复用表](../p3-wasm-tier/01-spike-gate.md):补指针指向 [P4 01 §2.4](./01-launch-judgment.md)——P3→P4 设计资产继承清单的两个视角互补 | ✅ 已落地(2026-06-28) |
| RJ-17 | [01 §9.2](./01-launch-judgment.md) + [07 §11.1](./07-p3-retirement.md) | [P3 00-overview §1 边界表](../p3-wasm-tier/00-overview.md):补一行指向 [P4 01](./01-launch-judgment.md) + [P4 07](./07-p3-retirement.md)——P3 总览只列 P3 实施层(同 tier 不同后端),立项闸门 + 去留决议两个 P4 视角未点到 | ✅ 已落地(2026-06-28) |
| RJ-18 | [07 §11.2](./07-p3-retirement.md) | [P3 01 §0.2 闸门单点决策不可绕过](../p3-wasm-tier/01-spike-gate.md):补对位指针指向 [P4 07 §0.3](./07-p3-retirement.md)——P3 spike 闸门(开工)与 P4 P3 去留闸门是 P3 生命周期上的两个闸门,形态平行 | ✅ 已落地(2026-06-28) |
| RJ-19 | [03 §11.2 RB-4](./03-speculation-ic.md) | [P3 06 §1.1 P3/P4 物理同形 ≠ 语义同义](../p3-wasm-tier/06-ic-feedback-consume.md):[P4 03 §0.2 / §5.3](./03-speculation-ic.md) 把这条对偶面在 P4 视角全展开,P3 06 加引用「P4 视角对偶兑现见 P4 §3 §5」| ✅ 已落地(2026-06-28) |
| RJ-20 | [03 §11.2 RB-5](./03-speculation-ic.md) | [P3 06 §6.1 IC 失效是否触发重编译,留 P4 评估](../p3-wasm-tier/06-ic-feedback-consume.md):[P4 03 §7.3](./03-speculation-ic.md) 给 P4 端重训练协议——P3 IC 失效永久 miss 与 P4 投机 deopt 反复失败统一在 P4 RequestRefresh + 重编译协议处理(详见 [P4 04 §5](./04-osr-deopt.md))| ✅ 已落地(2026-06-28) |
| RJ-21 | [04 §12](./04-osr-deopt.md) | [P3 04 §1.2 / §1.4 bit50 写入纪律](../p3-wasm-tier/04-trampoline.md):CallInfo bit50 在 OSR exit 后的语义——清 0 还是保留 1(承 [P4 04 §7.2](./04-osr-deopt.md))**倾向清 0**(差分友好),P4 落地时实测确认 | ✅ 已落地(2026-06-28,清 0 纪律已字面化;实测确认留 Spike 1 真接入) |
| RJ-22 | [08 §13.2 RB-3](./08-testing-strategy.md) | [P3 08 §0.4 P3 退役协议预设位](../p3-wasm-tier/08-testing-strategy.md):落实「若 P3 退役,V1-V18 编号保留,P4 接续承担」,[P4 08 §10](./08-testing-strategy.md) 已字面化,P3 08 加引用「具体迁移协议见 P4 §8 §10」| ✅ 已落地(2026-06-28) |
| RJ-23 | [08 §13.2 RB-4](./08-testing-strategy.md) | [P3 08 §4.4 P3 是 P4 投机正确性验证的预演](../p3-wasm-tier/08-testing-strategy.md):现稿已字面承诺,加引用「P4 视角具体验证形态见 P4 §8 §4 / §5」| ✅ 已落地(2026-06-28) |
| RJ-24 | [08 §13.2 RB-5](./08-testing-strategy.md) | [P3 implementation-progress §11 PW9 验收对账](../p3-wasm-tier/implementation-progress.md):加 P4 视角延伸——[P4 08 §3.7](./08-testing-strategy.md) force-all 非空保证援引 RW-10 教训,P3 implementation-progress 加引用「P4 force-all-jit 同款纪律见 P4 §8 §3.7」 | ✅ 已落地(2026-06-28) |

### 2.4 对外部 roadmap / llmdoc 文档的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-25 | [01 §9.3](./01-launch-judgment.md) | [../roadmap §4 P4 段](../roadmap.md):「+1-2 人年」估算补「立项前置 = 立项判定([P4 01](./01-launch-judgment.md))」,使 P4 启动节奏与 P3 同款(spike 先于实施)显式化——目前 §4 P3 段有「开工前置 spike」措辞,P4 段无对位措辞 | ✅ 已落地(2026-06-28) |
| RJ-26 | [07 §11.5](./07-p3-retirement.md) | [../roadmap §4 P4 段](../roadmap.md):「Wasm 层退役,或留作可移植中层」措辞补指针指向 [P4 07](./07-p3-retirement.md),使该决策框架的单一事实源显式化 | ✅ 已落地(2026-06-28) |
| RJ-27 | [01 §9.4](./01-launch-judgment.md) | [../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 速查表 P4 行:「前置 spike」列空,补「P4 立项判定(详 [P4 01](./01-launch-judgment.md))」,与 P3 行「wazero call boundary <150ns」对位 | ✅ 已落地(2026-06-28) |
| RJ-28 | [01 §9.4](./01-launch-judgment.md) | [../../../llmdoc/architecture/evolution-roadmap §P4 正文段](../../../llmdoc/architecture/evolution-roadmap.md):补「立项判定先于实施(本文承担)」,与 P3 段「开工前置 spike」对位 | ✅ 已落地(2026-06-28) |
| RJ-29 | [01 §9.5(可选)](./01-launch-judgment.md) | [../p2-bridge/00-overview §6 跨文档定稿决策速查](../p2-bridge/00-overview.md):可加一行「P4 立项判定」,但 P2 是 P3/P4 共享前端可能不需要——**主助理裁决是否落入 P2 总览** | ✅ 已裁决(2026-06-28,主助理决议**不加入** — P2 是 P3/P4 共享前端,P4 立项判定与 P2 设计内容无关;§6 表格收口的是 P2 内部协商决策,加入 P4 立项判定会破坏表格语义边界。**用户审阅后可推翻**) |

### 2.5 P4 子目录内部回填(本期收口)

子文档间互相补章节引用——主助理收尾轮统一兑现,这一节列出已识别的双向引用需求:

| # | 来源 | 内容 | 状态 |
|---|---|---|---|
| RJ-30 | [03 §11.3 RB-6](./03-speculation-ic.md) | [04 §5](./04-osr-deopt.md)(deopt 计数 + P4StuckSpeculation)+ [03 §7.2](./03-speculation-ic.md) 给 P4 视角,04 §5 给具体物化协议;两文协同覆盖完整闭环 | ✅ 已对接(双向协同已对接,03→04 §5 引足,04→03 §4 引足) |
| RJ-31 | [03 §11.3 RB-7](./03-speculation-ic.md) | [08](./08-testing-strategy.md)(差分接入「投机错果」最危险 bug 类)+ [03 §3.5 / §9.1](./03-speculation-ic.md) 提名差分主防线 | ✅ 已对接 |
| RJ-32 | [03 §11.3 RB-8](./03-speculation-ic.md) | [06](./06-backends.md)(per-arch 发射函数)+ [03 §2 / §5](./03-speculation-ic.md) 给伪汇编示意,06 引用 03 作 amd64 端母版 | ✅ 已对接 |
| RJ-33 | [03 §11.3 RB-9](./03-speculation-ic.md) | [02 §2.4 / §4.1 / §4.4](./02-template-direction.md)「子集内投机」承诺 + [03 §4.4](./03-speculation-ic.md) 落具体形态 | ✅ 已对接 |
| RJ-34 | [07 §11.4](./07-p3-retirement.md) | [08](./08-testing-strategy.md):验收基准分档加宿主真实负载形态([07 §10.1](./07-p3-retirement.md) 风险缓解);[07 §10.1](./07-p3-retirement.md) 已识别该风险,具体口径展开在 08 | ⏳ PJ11 |
| RJ-35 | [08 §13.4 RB-8](./08-testing-strategy.md) | [03 §3.5](./03-speculation-ic.md)「guard 多判 vs 漏判」语义边界 + [08 §3.4 / §11.1](./08-testing-strategy.md) 字面化,加双向引用 | ✅ 已对接 |
| RJ-36 | [08 §13.4 RB-9](./08-testing-strategy.md) | [04 §5 deopt 风暴 / §6 exit stub](./04-osr-deopt.md):[04 §5.5](./04-osr-deopt.md) 给 deopt 风暴物理学,[08 §5.6 V20](./08-testing-strategy.md) 把它翻成具体测试构造,加双向引用 | ✅ 已对接 |
| RJ-37 | [08 §13.4 RB-10](./08-testing-strategy.md) | [06 §5 双架构测试纪律 / §6 PJ 里程碑](./06-backends.md):[06 §5.2 / §6.2](./06-backends.md) 已立 V-J 编号,[08 §2.5](./08-testing-strategy.md) 落实 V1-V22 与 PJ 的具体映射 | ✅ 已对接 |

**回填请求总数**:**34 项**(撤回 RJ-8 / RJ-9 / RJ-10 三项,从 37 降到 34;承本文 §2.2 头注方案 A 决议),分布如下:

| 目标 | 数量 | 兑现节奏 |
|---|---|---|
| P1 现稿(P1 12 / P1 05)| **4 项**(RJ-1~4)| PJ5 / PJ7 |
| P2 现稿(P2 02 / 04 / 05 / 06)| **7 项**(RJ-5~7 / RJ-11~14;RJ-8/9/10 撤回)| PJ4 / PJ5 / PJ7 |
| P3 现稿(P3 00 / 01 / 04 / 06 / 08 / implementation-progress)| **10 项**(RJ-15~24)| PJ0 / PJ4 / PJ5 / PJ7 / PJ11 |
| roadmap + evolution-roadmap + P2 00 总览(可选)| **5 项**(RJ-25~29)| PJ0 / PJ11 |
| P4 子目录内部 | **8 项**(RJ-30~37,其中 7 项已落地双向协同)| 已对接 7 项(RJ-30~33 / RJ-35~37),余 1 项 RJ-34 ⏳ PJ11 |

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
| **locals 寄存器跨指令缓存** | 允许局部缓存 + 静态物化序列 | [04 §3.6](./04-osr-deopt.md) + [06 §4](./06-backends.md) | PJ7 amd64 原型实测后定终稿;PJ11 调优可能展开 |
| **自管机器栈大小** | 待定(每帧栈预算) | [05 §3.4](./05-system-pipeline.md) | PJ0 / PJ1 amd64 trampoline 落地实测 |
| **编译执行的线程模型** | 倾向同步编译(模板编译微秒级)| [02 §1.4 / §5.2](./02-template-direction.md) + [05 §3 / §5.2](./05-system-pipeline.md) (实测期复核) | PJ0 实测;若 cold-start 长尾再考虑后台 goroutine 编译 + 安装屏障 |

### 3.2 依赖外部数据(中影响 / 高不确定度)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| **deopt 计数阈值** | 待定(承 [P2 01 §5 阈值定标](../p2-bridge/01-profiling.md) 同款待定)| P4 实测 deopt 率反推(承 [04 §5.6](./04-osr-deopt.md))|
| **`MaxRecompileTries`** | 待定 | P4 实测 deopt 风暴边界后定([04 §5.3](./04-osr-deopt.md))|
| **回边 preemptFlag 检查点密度** | 待定 | PJ3 实测(承 [05 §6.3](./05-system-pipeline.md))|
| **confidence 投机阈值** | ≥0.99(P2 PB2 已采用,P4 复用)| P4 实测 deopt 率反推(承 [03 §2.7](./03-speculation-ic.md))|
| **P3 去留结论** | ✅ **主动保留**(2026-07-01 用户拍板,承 [07 §5.6](./07-p3-retirement.md) / [09 §2 D2](./09-acceptance-checklist.md)) | 走 §10.2 应急保留形态:代码留 + build tag deprecated + wazero 版本锁死 + 不承诺双后端 CI 持续绿;既非 §6 退役也非 §7 留中层;RJ-12 自动消解 |
| **bit50 在 OSR exit 后清 0 还是保留 1** | ✅ 清 0(2026-06-29 PR #27 / #28 三平台 CI 全过后用户确认) | 已采纳,V1-V13 差分套全过实测确认(承 [04 §7.2](./04-osr-deopt.md) + RJ-21)|
| **guard 合并窥孔范围** | 同操作数直线段内只查一次(基线)| PJ7/PJ11 若 guard 密度天花板吃掉收益则展开(承 [03 §3.6](./03-speculation-ic.md))|
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
| 后续维护 | 每轮里程碑落地后追加对账行 | 立项后追加 PJ0-PJ11 进度行;若立项判定否决则记录「P4 跳过」决策 + 判定数据 |

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

**状态**:✅ **PJ2 投机模板 12+ 形态完整接入** + 🔶 **PJ3 工程基础 + 物理 spike**(真接入 FORLOOP 字节级内联)。

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
4. p4Code.Run 路径接入(段内自循环,Run 等同一次进一次出,无需结构改动——本批次推导出 spike 形态)

。

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

**PJ11 luajc 档(≥4.4x over gopher-lua)早已超越**——100/1000/10000 iter 三档分别 7.15x / 21.20x / 25.41x,均远超 4.4x luajc 档基线。

**机理**:PJ3 把 FORLOOP 字节级 inline 进 mmap 段,1000/10000 次回边完全不跨 boundary——仅 SSE addsd + ucomisd + backward jmp 三指令循环,~3 cycles/iter,接近原生码理论上限。boundary cost 只在 enter/exit 段各一次,wrap-kernel × 50 ⇒ N 越大 boundary 占比越小。

### 8.7 当前 PJ3 形态范围 + 后续扩展

**当前已落地**(5 类 FORLOOP 形态完整 byte-equal P1):
- ✅ LOADK 常量 limit + 空 body:`for i=1,100 do end`(69-83 字节)
- ✅ MOVE reg-limit + 空 body hot path:`for i=1,n do end` 参数(117 字节 + IsNumber guard + host.ForPrep deopt)
- ✅ GETUPVAL upvalue-limit + 空 body:`local n=100; local function f() for i=1,n do end end`(Run prelude + reg-limit 模板复用)
- ✅ **常量 limit + 单 reg-K body op**:`local s=K_s; for i=K1,K2 do s = s op K3 end; return s`(135 字节模板,body 用 xmm3/xmm4)
- ✅ **常量 limit + 二段 reg-K body op**:`local s=K_s; for i=K1,K2 do s=s op1 K3; s=s op2 K4 end; return s`(154 字节模板,body 共享 xmm3 跨两段省一次 load/store)

**留 PJ4+ 扩**:
- ⏳ 嵌套 for / break(JMP)
- ⏳ 表 IC stableShape 直达槽(PJ4 范围,N 人月级工程)
- ⏳ CALL/TAILCALL + OSR exit(PJ5 范围)

### 8.8 reg-limit hot path benchmark + body inline benchmark

| 形态 | iter | P4 (ns/op) | crescent (ns/op) | gopher (ns/op) | **P4 vs cres** | **P4 vs gopher** |
|---|---|---|---|---|---|---|
| 空 body 常量 | 100 | 7,311 | 59,334 | 52,272 | **8.11x** | **7.15x** |
| 空 body 常量 | 1000 | 31,313 | 548,831 | 663,911 | **17.53x** | **21.20x** |
| 空 body 常量 | 10000 | 271,426 | 5,452,279 | 6,895,610 | **20.09x** | **25.41x** |
| reg-limit 空 body | 1000 | 33,193 | 578,891 | -- | **17.44x** | ~20x |
| reg-limit 空 body | 10000 | 286,293 | 5,767,130 | -- | **20.15x** | ~24x |
| body+=1 单 op | 1000 | 117,000 | 846,020 | 1,190,633 | **7.23x** | **10.18x** |
| body+=1 单 op | 10000 | 1,138,946 | 8,386,049 | 12,335,326 | **7.36x** | **10.83x** |

**全部远超 luajc 档 4.4x 基线**——5 类形态 7-25x over gopher-lua。

**body inline 形态分析**:
- 空 body 形态 ~3 cycles/iter(addsd+ucomisd+jmp),20-25x over gopher
- body+=1 单 op ~8 cycles/iter(多 5 SSE 指令链:load/mov K/movq/sseOp/store),10x over gopher
- iter 数越大 boundary 占比越小,加速比逼近上限

---

## 9. PJ4 表 IC ArrayHit 字节级 inline 真接入(2026-06-26 落地)

承 [§1 PJ 表 PJ4 行](#1-里程碑进度对账对应-00-overview-4) + [03 §6 IC 直达槽设计](./03-speculation-ic.md)。本节记录 PJ4 IC ArrayHit 字节级 inline 主路径接入 + 自然热度 e2e 实证 + benchmark 诚实数据。

### 9.1 字节级模板(129 字节)

`internal/gibbous/jit/amd64/pj4_template.go::EmitGetTableArrayHit`:

```
IsTable guard 简化(8 字节):cmp rax, 0xFFFC_0000_0000_0000; jb deopt
GCRef extract(4 字节):mov ebx, eax(低 32 位 = arena offset)
arena base load(7 字节):mov r14, [r15+arenaBaseOff]
gen check stableShape(11 字节):cmp dword [r14+rbx+word5_off], stableShape; jne deopt
arrayRef load(7 字节):mov rcx, [r14+rbx+word_arrayRef_off]
array[stableIndex] load(8 字节):mov rax, [r14+rcx+stableIndex*8]
nil check(3 字节):cmp rax, NaN-box nil; je deopt
写 R(A)(8 字节):mov [rbx_vsBase+aReg*8], rax
ret(1 字节):c3
deopt block(11 字节):mov rax, deoptCode; ret
```

总 129 字节(deopt block 自身 11 字节)。Run 端检测 rax==deoptCode → 调 `host.GetTable` byte-equal P1 解释器路径(经 IC + 哈希 + __index 元方法链)。

### 9.2 形态识别与触发条件

`compiler.go::analyzeGetTableArrayHit` 要求全部满足才返 ok=true:

1. **Code 形态**:长度 2 或 3,`[0]=GETTABLE A B C / [1]=RETURN A 2 / [2]?=dead RETURN`;
2. **A 一致**:`GETTABLE.A == RETURN.A`,`RETURN.B = 2`(单值返回);
3. **B/C 范围**:`B <= 254`(寄存器号),`C >= 256`(K 常量索引——动态 reg 键不投机);
4. **IC slot**:`proto.IC[0].Kind == ICKindArrayHit`(P1 解释器观测过 array 命中);
5. **feedback**:`feedback.Points[0].Kind == FBTableMono` + `Confidence >= 0.99`;
6. **stable 一致**:`feedback.StableShape == IC.Shape` + `feedback.StableIndex == IC.Index`(无 race 时一致)。

任一不满足 → 返 ok=false → fall through 到 `analyzeShape` 的 GETTABLE host helper 路径(byte-equal 但无字节级加速)。

### 9.3 prove-the-path 实证(Warmup-then-Force e2e)

承 [[prove-the-path-under-test]] 纪律,验证 IC inline 模板真在 mmap 段编译:

```go
// internal/crescent/gibbous_pj4_table_e2e_test.go::TestPJ4_TableArrayHit_E2E_WarmupThenForce
jit.ResetSpecHits()
// Phase 1: 不开 force-all,P1 解释器跑 100 次 warmup 填 IC[0]
st.Call(...)
// SpecTableHits=0(P1 路径不应触发 IC inline 编译)

// Phase 2: 开 force-all,inner kernel 升 P4
st.bridge.SetForceAllPromote(true)
st.Call(...)
// SpecTableHits=1(IC ArrayHit 字节级模板真编译)
```

**关键发现**:之前 PJ4 e2e 只用 `SetForceAllPromote(true)` 一段路径,inner kernel 一进入即升层时 IC slot 还没被 P1 跑过 → `analyzeGetTableArrayHit` 返 false → fall through 到 host helper 路径(SpecTableHits 恒 0)。新 e2e 明确分两 phase,IC 填充 → feedback 聚合 → Compile 命中 → 字节级 inline 链路端到端实证。

### 9.4 baseline benchmark(诚实数据)

`benchmarks/baseline/baseline_gibbous_jit_test.go::BenchmarkGibbousJIT_PJ4IcArrayHit{1,2}{Cresc,}`:

| 形态 | iter | P4 ns | P1 crescent ns | **P4 vs P1** |
|---|---|---|---|---|
| t[1] | 200 | 4624 | 4225 | **0.91x(慢 9%)** |
| t[2] | 200 | 4555 | 4190 | **0.92x(慢 9%)** |

**加速为负是预期**:P1 `icGetTable` 在 IC 命中时已是「array 段直达」几条 Go 指令快路径,与 P4 字节级 IC inline 模板做的事完全等价。P4 多付 `callJITSpec` trampoline 入出 ~50ns 开销 → 反慢。

**真加速场景留 PJ5 CALL inline**:把 outer 也升 P4 后,outer 内多次 GETTABLE 不付 `doCall` boundary,IC inline 在「无 doCall 跨界 + 字节级直达 array 段」组合下才显出加速。本档保留作 SpecTableHits prove-the-path 命中证据 + 同形态 P1 baseline 对照。

### 9.5 已知边界(留 PJ4+ 扩)

1. **IsTable guard 简化**(`rax < 0xFFFC<<48` 单边 jb):对 string/thread/userdata 高位 tag 假阳,但后续 gen check 几乎必触发 deopt 走 host.GetTable byte-equal。严密版(`shr rax, 48 + cmp ax, 0xFFFC + jne deopt`)~12 字节,留 PJ4+。
2. **NodeHit / SETTABLE / SELF 未接入**:NodeHit 需键比较 + node 段 24 字节寻址;SETTABLE 需反向写槽 + __newindex 元方法 deopt;SELF 是 GETTABLE 变体 + 多写 R(A+1)=R(B)。形态识别 + 模板拆分留下一阶段。
3. **C 字段必须 ≥ 256**:动态 reg 键会让 IC slot 轮换不同 key,字节级 inline 不能假设 stableIndex 一致。

### 9.6 测试覆盖度补齐(prove-the-path 纪律延续)

承外部审查发现「commit `12ec50e` 后所有 PJ4 e2e 的 SpecTableHits 恒为 0」(force-all 路径下 IC slot 未填,降级到 host.GetTable byte-equal 路径——无字节级 inline 命中证据)。本批 commit ddf65e9 + e02d8d7 + 205c888 + 3dcd769 消化补齐:

1. **加 WarmupThenForce + NumericKey 真命中 e2e**:SpecTableHits 增量 = 1 实证字节级 inline 真编译;
2. **改 _FastPath 为 _ForceAllFallsToHost**:明示原测试在 force-all 路径下 fall through 到 host 的事实,断言 SpecTableHits=0;
3. **加 compiler.go PJ4 IC 优先级 godoc**:说明"必须先尝试 IC inline" 原因(与 analyzeShape GETTABLE 形态字节重叠);
4. **加 baseline benchmark + 诚实记录加速为负**:wrap × 50 调 inner kernel 形态下 P4 慢 9-15%,真加速场景留 PJ5 CALL inline;
5. **补 SUB/MUL/DIV WithGuard 字节级单测 9 个**(对位 ADD 三件套 FastPath/DeoptPath_B/DeoptPath_C × 3 op),消化 PJ2 累计两轮反馈未完全落地的纪律缺口。

prove-the-path 纪律扩展到 PJ4:**新 SupportsAllOpcodes 形态 / 新 IC inline 接入 ⇒ 同 commit 必须有 (a) jit 包 mock-host 字节级路径命中证据(已有 mock-test) + (b) crescent e2e 真升层 SpecXxxHits 增量断言(本批补齐)**。

### 9.11 测试覆盖度边界与整套层级 prove-the-path 修复(2026-06-26 落地)

承外部审查 🔴 阻塞反馈:`make test-p4` 全套(conformance / difftest / luasuite)历史上**不真在 P4 路径上运行**——91.6% conformance 用例不升层,且缺 `test/difftest/p4_test.go`(P3 有对位)。这是 P4 工程整体最严重 prove-the-path 缺口(跨越本批与历史多轮)。

**层级区分**:
- **单形态层**(jit 包字节级单测 + crescent e2e WarmupThenForce 系列):覆盖 P4 单 IC 形态,SpecTableHits++=1 真增长,**优秀,持续消化反馈**。
- **整套 make 命令层**(conformance / difftest / luasuite):此前 P4 路径未被强制触达,**76/83 conformance 用例 P4 升层数 = 0 + 缺 p4_test.go**——是层级问题,非单 commit 问题。

**修复**(commit 6dc1760 / b4c02d2 / 8e46759):

1. **`test/difftest/p4_test.go`**(全新 290 行)对位 `p3_test.go`:
   - build tag `wangshu_p4 && wangshu_profile` P4 专属
   - `runWangshuP4Tiered` helper + p4Corpus 17 用例(精选 P4 SupportsAllOpcodes 真接受形态:LOADK/MOVE/算术/比较/UNM/NOT/FORLOOP/表 IC 六路径/SETUPVAL),每核外层 `for` 循环重复调用 ≥ 20 次
   - `TestP4_Tiered`:三方对拍(oracle / crescent / p4-jit byte-equal)
   - `TestP4_ConcurrentForceAll`:8 goroutine 并发 force-all + 结果一致性(V18 -race 守卫)
   - `TestP4_PromotionTriggered`:fail-stop 兜底,`PromotionCount > 0` 强断言防 P4 路径未触达成静默空绿

2. **`test/conformance/conformance_p4_test.go`**(全新)+ `conformance_test.go` godoc 边界标注:
   - 顶 godoc 加 "P4 build 边界"章节,诚实标注 ~91% 用例形态不达 P4 升层闸门,真 P4 路径验收以 difftest-p4 为准
   - `TestConformance_P4PathTriggered`:专为 P4 升层形态设计的 conformance 用例 + PromotionCount > 0 fail-stop

3. **`Makefile` 三条 P4 注释更新**(从陈旧 "PJ0 阶段:行为等价 P1" 改为):
   - `test-p4`:PJ0-PJ4 + PJ7 + PJ11 已落地:LOADK/MOVE/算术/比较/UNM/LEN/NOT/NEWTABLE/GETTABLE/SETTABLE/SELF/FORLOOP 真接入 + IC 六路径字节级 inline
   - `conformance-p4`:白名单已扩 ~25 类形态 + IC 六路径,但 conformance 用例多为单次小脚本,~91% 不达 P4 升层闸门 — 真 P4 路径验收以 difftest-p4 为准
   - `difftest-p4`:test/difftest/p4_test.go P4 专属 harness:force-all + p4Corpus 17 用例每核重复调用 + PromotionCount > 0 兜底

**实测结果**:
- `TestP4_Tiered` 17/17 用例 byte-equal(crescent vs p4-jit)
- `TestP4_ConcurrentForceAll` 8 goroutines 不 race + 结果一致
- `TestP4_PromotionTriggered` `PromotionCount = 1` 真升层
- `TestConformance_P4PathTriggered` `PromotionCount = 1` 真触达
- `make test-p4` 全套 21 binary 全过

**单形态 + 整套层 prove-the-path 双层防线完整**:
- 单形态层:SpecTableHits 增量 = 1 实证(IC 六路径全覆盖)
- 整套层:PromotionCount > 0 fail-stop(difftest-p4 + conformance-p4 兜底)

### 9.7 严密 IsTable guard 升级(2026-06-26 落地)

承 §9.5 #1 已知边界:把简化 IsTable guard 字节级升级到严密版。

**简化版(19 字节)**:
```
mov rcx, 0xFFFC<<48     (10 字节)
cmp rax, rcx            (3 字节)
jb deopt rel32          (6 字节)
```
只验 `rax >= 0xFFFC<<48`,string(0xFFFB)是真 deopt,function(0xFFFD)/userdata(0xFFFE)/thread(0xFFFF)假阳通过 → 后续 gen check 触发 deopt 多走一段 mmap 指令。

**严密版(15 字节)**:
```
shr rax, 48             (4 字节,48 C1 E8 30)
cmp eax, 0xFFFC         (5 字节,3D FC FF 00 00)
jne deopt rel32         (6 字节,0F 85 ...)
```
精确验高 16 位 = TagTable(0xFFFC),所有非 table NaN-box 立即 deopt 不再 fall through。

**新 emit 原语**:`EmitShrRaxImm8` / `EmitCmpEaxImm32` + 字节级单测 byte-equal Intel SDM。模板长度 129 → 132 字节(差异来自 shr 破坏 rax 后 re-load R(B) +3 字节)。

字节级单测验严密 guard 序列在模板前段:`StrictIsTableGuard`(shr@7 / cmp@11 / jne@16)+ `NoSimplifiedGuard` 反向断言。

### 9.8 PJ4 NodeHit 字节级 inline 真接入(2026-06-26 落地)

承 §9.5 #2 已知边界:NodeHit 接入完整 PJ4 表 IC 覆盖。

**NodeHit 形态**:`function(t) return t["x"] end` 或 `function(t) return t[K] end` 其中 K 是字符串常量(或数字键且键在 hash 段而非 array 段)。luac 编 GETTABLE A B C 同 ArrayHit,但 IC[0].Kind=NodeHit。

**字节级模板**(159 字节,ArrayHit 132 字节 + key 比对 27 字节):
```
load R(B) → rax + 严密 IsTable guard               (22 字节,复用 ArrayHit)
re-load R(B) + GCRef extract + arena base load    (23 字节,复用)
gen check stableShape                              (10 字节,复用)
mov rax, [r14+rcx+24]    ; table.nodeRef           (8 字节,word3 而非 word2)
mov rcx, rax              ; rcx = nodeRef          (3 字节)
mov rax, [r14+rcx+stableIndex*24]  ; NodeKey       (8 字节)
mov rdx, stableKey                                 (10 字节)**新**
cmp rax, rdx                                       (3 字节)**新**
jne deopt                                          (6 字节)**新**
mov rax, [r14+rcx+stableIndex*24+8]  ; NodeVal     (8 字节)
nil check + store R(A) + ret                       (27 字节,复用)
deopt block                                        (11 字节,复用)
```

**新 emit 原语**:`EmitMovRdxImm64` / `EmitCmpRaxRdx` + 字节级单测 byte-equal Intel SDM。

**形态识别**(`analyzeGetTableNodeHit`):
- 与 `analyzeGetTableArrayHit` 同款 Code 形态 + IC kind 检查(改 NodeHit)+ feedback 检查
- **stableKey 编译期固化**:`proto.Consts[KIdx]`(LoadProgram 已 intern 字符串,数字编译期就装好)

**Compile 主路径**:`Compile` 内先尝试 ArrayHit,失败再尝试 NodeHit(两路径形态字节重叠但 IC kind 区分)。两路径都用 `incSpecTableHits` 探针 + Run 端 `icArrayHit=true` 让 deopt 走 host.GetTable byte-equal P1(P1 icGetTable 兼容 ArrayHit + NodeHit)。

**e2e 实证**:
- `TestPJ4_TableNodeHit_E2E_WarmupThenForce`(`t["x"]` 形态,字符串键):Phase 2 SpecTableHits 增量 = 1 ✅
- `TestPJ4_TableNodeHit_E2E_NumberKey`(`t[7]` 在 dict-style `{[7]=42}` 表,数字键 in hash 段):Phase 2 SpecTableHits 增量 = 1 ✅

**PJ4 IC 完整覆盖**:ArrayHit(数字键 in array 段)+ NodeHit(任意键 in hash 段)两路径字节级 inline 主路径全接入。**SETTABLE / SELF 留下一阶段**(需要反向写槽 + __newindex 元方法 deopt;SELF 是 GETTABLE 变体 + 多写 R(A+1)=R(B))。

### 9.9 PJ4 SETTABLE ArrayHit 字节级 inline 真接入(2026-06-26 落地)

承 §9.8 已知边界:SETTABLE 接入完成 PJ4 SETTABLE 首套实装。

**SETTABLE 形态**:`function(t, v) t[K] = v end` 中 K 是数字常量 + 命中 array 段(luac 编 SETTABLE A B C / RETURN A 1,其中 A=R(t) / B=K idx >=256 / C=R(v))。

**字节级模板**(113 字节,比 GETTABLE ArrayHit 132 字节小 19 字节):
```
[0-21]   load R(A) → rax + 严密 IsTable guard (22 字节,复用)
[22-44]  re-load R(A) + GCRef extract + rcx = offset (23 字节,复用)
[45-74]  arena base 装 r14 + gen check stableShape (30 字节,复用)
[75-85]  load table.arrayRef(word2=16)+ rcx = arrayRef offset (11 字节)
[86-92]  load R(C) value → rdx (7 字节,EmitMovqRdxFromMemRbx 新原语)
[93-100] 反向 store mov [r14+rcx+stableIndex*8], rdx (8 字节,
         EmitMovqMemR14PlusRcxFromRdx 新原语)
[101]    ret (1 字节)
[102-112] deopt block (11 字节)
```

vs GETTABLE ArrayHit 关键差异:
- 不读现有 array[stableIndex](getter 验非 nil + 写 R(A);setter 直接写)
- 不做 nil check on result(getter 是 read,setter 是 write)
- 多 load R(C) value 到 rdx + 反向 store from rdx

**新 emit 原语**:`EmitMovqMemR14PlusRcxFromRdx`(反向 store from rdx,8 字节,49 89 94 0E disp32)+ `EmitMovqRdxFromMemRbx`(load rdx,7 字节,48 8B 93 disp32)+ 字节级单测 byte-equal Intel SDM。

**形态识别**(`analyzeSetTableArrayHit`):
- Code 长度 2/3,[0]=SETTABLE / [1]=RETURN A 1(setter)
- SETTABLE A B C:A<=254 / B>=256(K 常量) / C<256(value 是 reg)
- proto.IC[0].Kind=ArrayHit + feedback FBTableMono + shape/index 一致

**Compile 主路径**:`Compile` 内按顺序尝试 GetTable ArrayHit → NodeHit → SetTable ArrayHit。SETTABLE 命中即 compileIcSetArrayHit emit 113 字节模板,Run 端 icSetArrayHit=true 让 deopt 走 host.SetTable byte-equal(经 icSetTable + __newindex 元方法链)。

**e2e 实证**:
- `TestPJ4_TableSetArrayHit_E2E_WarmupThenForce`(`setter(t, v) t[1] = v`):
  - Phase 1 P1 跑 100 次 warmup 填 IC[0]=ArrayHit + 反复写值
  - Phase 2 force-all 升 setter → SETTABLE SpecTableHits 增量 = 1 ✅
  - 写值正确性:t[1] = 42 byte-equal 解释器

**设计简化**(SETTABLE 工程边界):
- 不验现有 array[stableIndex] != nil(防新键路径)— 依赖 P1 解释器在键退化场景 bump gen + RequestRefresh
- 不验 __newindex 元表存在(meta freeze 假设)— 元方法场景应触发 gen change 由 IC 失效路径处理
- 数字键 in array 段(NodeHit SETTABLE / 字符串键 SETTABLE 留 PJ4+)

**PJ4 IC 三路径完整覆盖**:GetTable ArrayHit + GetTable NodeHit + SetTable ArrayHit 全主路径接入。Set NodeHit / SELF 留下一阶段。

### 9.10 PJ4 SELF ArrayHit 字节级 inline 真接入(2026-06-26 落地)

承 §9.9 SETTABLE 后,SELF opcode 字节级 inline 完成 PJ4 GETTABLE/SETTABLE/SELF 四路径基础。

**SELF opcode 语义**:
```
R(A+1) := R(B)     ; self/this 实参
R(A)   := R(B)[RK(C)] ; method 函数
```

**字节级模板**(139 字节,GetTable ArrayHit 132 + R(A+1) 拷段 7):
- 入口多 1 步「store R(A+1) = R(B)」(SELF 第一步拷 obj 到 self 位)
- 主体复用 GetTable ArrayHit 流程:严密 IsTable + arena base + gen check + arrayRef + array[stableIndex] + nil check + 写 R(A)
- 不需新 emit 原语(复用 `EmitMovqMemRegFromRax` 同款 store)

**形态识别**(`analyzeSelfArrayHit`):
- Code 长度 2/3,[0]=SELF / [1]=RETURN A 2
- SELF A B C:A<=253(留 R(A+1) 槽<=254),B<=254,C>=256
- proto.IC[0].Kind=ArrayHit + feedback FBTableMono + shape/index 一致

**Compile 主路径**:四路径优先级 GetTable ArrayHit → NodeHit → SetTable → Self。SELF 命中即 `compileIcSelfArrayHit` emit 139 字节模板,Run 端复用 `icArrayHit=true` 让 deopt 走 host.GetTable(R(A+1) 已 store 不回滚,P1 SELF case 同款步骤 byte-equal)。

**诚实标注 luac 形态边界**:
- SELF opcode 在 luac 5.1 中 method key 必是 ident(字符串常量),不可能编出数字 K → SELF ArrayHit 形态(数字键 in array 段)real-world 几乎不出现
- 本批 SELF ArrayHit 主路径接入是**工程基础**(emit + arch + analyzer + compileIc*),供下一阶段 SELF NodeHit 复用结构
- e2e `TestPJ4_TableSelfArrayHit_E2E_WarmupThenForce` 改为验「SELF 主路径接入不破坏现有 ArrayHit 路径」

**PJ4 IC 四路径覆盖**:GetTable ArrayHit / NodeHit + SetTable ArrayHit + Self ArrayHit。**留 NodeHit set / NodeHit SELF**(常见 `obj:method()` 字符串名)给下一阶段 — 复用现有四路径同款结构,只需 stableKey 编译期固化 + 159 字节 key 比对模板扩展。

### 9.12 PJ5 工程基础 + 剩余 PJ 工程量明示(2026-06-26 落地)

P4 设计文档 §0 自估 +1-2 人年完整工程,分批次推进。本节明示已落地的 PJ5 工程基础 + 剩余 PJ5/PJ8/PJ9/PJ3 的完整工程量估算,作为渐进推进路径的清晰指引。

**PJ5 工程基础已落地**(2026-06-26):

- `EmitCallRel32`(5 字节,E8 + imm32 LE):rel32 直接 CALL,fallback 用
- `EmitCallReg`(2 字节,FF D0+regN):间接 CALL r/m64
- `EmitPushReg` / `EmitPopReg`(各 1 字节,50/58+regN):栈操作 + 防御
- **`EmitHelperCall`**(12 字节,新增):`mov rax, helperAddr + call rax` 通用宏,封装 jit→host helper 间接调用固定字节序列。Intel SDM byte-equal 字节级单测全覆盖(call rel32/call reg/push/pop/helper call macro)

**PJ5 剩余真接入工程量**(估 +1.5-3 人月,):

- **CALL inline**:Lua CALL A B C → mmap 段内 emit `EmitHelperCall(&host.DoCall)` + 参数装载 SysV ABI 寄存器 + base 刷新协议(承 05 §4.3)
- **TAILCALL inline**:CALL 帧复用 + bit50 协议(承 05 §4.3)
- **OSR exit 寄存器物化**:deopt 时把 xmm0-xmm7 / 通用寄存器状态写回 arena slot,让解释器从一致状态继续

**PJ8 剩余真接入工程量**(估 +0.5-1 人月 + 物理 runner,):

- **arm64 浮点原语**:fmov / fadd / fsub / fmul / fdiv / fcmpe + 条件 b.eq/b.gt 等(对位 amd64 SSE binop / ucomisd / jcc)
- **arm64 FORLOOP 模板**:对位 amd64 EmitForLoopEmptyConst 等 5 类形态
- **arm64 IC 模板六路径**:对位 amd64 PJ4 全六路径
- **物理 self-hosted runner**:QEMU 不真模拟 i-cache + PROT_EXEC,需物理 arm64 机器跑端到端

**PJ9 剩余工程量**(估 +0.5-1 人月,依赖 PJ8 物理 runner):

- V14 luajc 档调优(性能档)
- 双架构差分套(Go 1.25/1.26/tip 矩阵 CI 绿)
- 真 arm64 self-hosted runner 接入(承 PJ9 完成定义)

**PJ3 嵌套 for / break(JMP)剩余工程量**(估 +2-4 commits / 1-2 人周):

- P4 当前是 single-BB 模型,JMP 跨基本块不真支持
- 需扩展为多 BB 跳转表 + label patching 协议
- analyzeForLoopForm 扩识别嵌套形态,FORLOOP 模板嵌套化

**累计剩余工程量**:**+2.5-5.5 人月**(PJ5 + PJ8 + PJ9 + PJ3 扩),与 P4 设计文档 §0 自估 +1-2 人年范围一致。

---

### 9.13 PJ8 arm64 字节级模板矩阵完整(2026-06-26 落地)

**目标**:arm64 端 PJ2/PJ3/PJ4 三 op 字节级模板矩阵齐全,**对位 amd64 完整**,等物理 runner 真接入(trampoline asm + mmap+RX 端到端)即可执行,工程组件层 PJ8 字节级模板交付完成。

**交付清单**:

**21 件 emit 原语**(`internal/gibbous/jit/arm64/emitter.go`):
- **整数族 11**:`EmitMovX0Imm64`(16B)/ `EmitRet`(4B)/ `EmitMovXdImm64` / `EmitMovXdFromXn`(ORR Xd,XZR,Xn 4B)/ `EmitAddXdImm12` / `EmitSubXdImm12` / `EmitB`(imm26 4B)/ `EmitLdrXtFromXnDisp`(scaled 4B)/ `EmitStrXtToXnDisp` / `EmitCmpXnXm`(SUBS XZR 4B)/ `EmitBCond`(imm19 4B,12 cond codes)
- **浮点族 7**:`EmitFmovDdFromXn`(GP→FP)/ `EmitFmovXdFromDn`(FP→GP)/ `EmitFaddDdDnDm` / `EmitFsubDdDnDm` / `EmitFmulDdDnDm` / `EmitFdivDdDnDm` / `EmitFcmpeDnDm`(signaling NaN)
- **PJ4 IC 基础 3**:`EmitAddXdXnXm`(SIB 替代 4B)/ `EmitAndXdXnXm`(payloadMask 提取 4B)/ `EmitLsrXdImm6`(shr 64 位变量 4B)

**PJ2 投机模板**(`pj2_template.go`,108B):
- `EmitArithSpeculativeBinopWithGuardArm64` = guard×2(28×2=56)+ fast 32 + deopt 20 = 108B
- 4 字节级单测覆盖 ADD/SUB/MUL/DIV

**PJ3 FORLOOP 模板**(`pj3_template.go`,84B):
- `EmitForLoopEmptyConstArm64` = mov+fmov×3(60)+ fsub/fadd/fcmpe/b.cond/b/ret(24)= 84B
- 3 字节级单测:Length / Layout(关键指令布局)/ ConstantsBurnedIn

**PJ4 IC 完整六路径**(`pj4_template.go`,1052B 总长):

| 路径 | 字节数 | vs amd64 | 关键差异 |
|---|---|---|---|
| GETTABLE ArrayHit | 168B | +36 vs 132B | SIB 替代 ADD+LDR + MOV imm64 序列 16B×多 |
| GETTABLE NodeHit | 196B | +37 vs 159B | + NodeKey 比对段 28B |
| SETTABLE ArrayHit | 144B | +31 vs 113B | 反向写 STR + SIB 替代 |
| SETTABLE NodeHit | 172B | +32 vs 140B | + NodeKey 比对 + 反向写 NodeVal |
| SELF ArrayHit | 172B | +33 vs 139B | R(A+1)=R(B) 拷段 4B 在 IsTable guard 前 |
| SELF NodeHit | 200B | +34 vs 166B | NodeHit + R(A+1) 拷段 4B |

**SELF byte-equal P1 case 同款步骤**(承 amd64 SELF 同款):
- R(A+1) = R(B) 必在 IsTable guard **前** 写,确保 deopt 路径走 host.GetTable 时 R(A+1) 已设(P1 SELF case 步骤:setReg(A+1, B) → icGetTable → setReg(A))
- 后续 NodeHit 流程头部 LDR 已合并到 SELF 入口,不重复

**字节级单测覆盖**(25+ 测试):
- 各模板:Length / DeoptBlock 至少 2 个
- ArrayHit:StrictIsTableGuard(LSR/CMP/B.NE 字节序列)
- NodeHit:StableKeyBurnedIn(movz+movk×3 imm16 字段实证)
- SELF:RAPlus1Store(R(A+1) 先于 IsTable guard 写实证)

**arm64 寄存器协议**(承 06 §4.2 留 PJ8+):
- `x26` = valueStackBase(对位 amd64 rbx)
- `x27` = jitContext(对位 amd64 r15)
- `x28` = Go G(Go runtime 保留)
- `x14` = arena base(模板入口装入,对位 amd64 r14)

**vs amd64 模板字节数差异源**:arm64 RISC fixed-length 4B 指令 + 无 SIB 寻址(单条 `mov rax, [r14+rcx+disp]` amd64 10B → arm64 ADD+LDR 8B 但多 1 条 + 偶尔多 cycle 流水)+ MOV imm64 序列 16B(movz+movk×3) vs amd64 mov rax imm64 10B(REX+opcode+8 字节立即数);累积每路径 +30-40 字节,但每条指令是单 cycle,真执行延迟差异更小(待 PJ9 物理 runner 实测)。

**真接入剩余阻塞**(留 PJ8+):
- `trampoline_arm64.s` callee-saved x19-x29 保存 + x28=G/x27/x26 装入协议(框架文件已存在 2.3KB,完整化)
- `arch_arm64.go` 双轨同款 amd64 path 接 jit.Compile → arm64 emitter
- mmap PROT_RW 分配 → 字节级模板 copy → mprotect PROT_RX + arm64 i-cache flush(`flushcache_arm64.s` 已存在 2KB)
- 物理 self-hosted runner(QEMU 不真模拟 i-cache + PROT_EXEC)启用端到端 V1-V22

**ROI 估算**:本里程碑为 PJ8 真接入提供完整字节级模板基础,真接入 1-2 人月可在物理 runner 上启用。

### 9.13.1 PJ8 arm64 Compile 端真接入(IC 六路径 + FORLOOP 全套)(2026-06-26 落地)

承 §9.13 字节级模板矩阵完整,本批把 `arch_arm64.go` 十个原 stub(返空 buf,`_ = arg` 弃元)改为真代理 `jitarm64.EmitXxxArm64`:

- **PJ4 IC 六路径**:`archEmitGetTableArrayHit/NodeHit` + `archEmitSetTableArrayHit/NodeHit` + `archEmitSelfArrayHit/NodeHit`,签名完全对位 amd64(`arenaBaseOffArm64` helper 把 `int32→uint16` 转换硬化为运行期 panic,防 JITContext 字段未来重排静默 UAF)
- **PJ3 FORLOOP 全套**:`archEmitForLoopEmptyConst` / `archEmitForLoopRegLimit` / `archEmitForLoopWithBody` / `archEmitForLoopWithBody2` 全部接入,arm64 PJ3 全四形态字节级模板真接入完整
  - EmptyConst:84/92B 含 safepoint
  - RegLimit:120/128B 含 safepoint(guard LDR+CMP+B.HS deopt 限非数字 limit)
  - WithRegKBody:144/152B(reg-K body 单 op,对位 amd64 121/135B)
  - WithRegKBody2:168/176B(reg-K body 二段共享 d3 跨两段省一对 LDR+STR,对位 amd64 140/154B)

接入路径:Compile 主路径经 `archCallJITFull`(trampoline_arm64.s 已就绪)→ mmap+RX 段执行,不依赖 `archCallJITSpec`(后者仍 panic 留 PJ8+ spec trampoline 物理 runner 同批)。

**arch 闸门解耦**(承 §9.13.2 review fix):FORLOOP Compile 块原闸门 `info.isForLoop && archSupportsSpec()` 改 `archSupportsForLoop()`,因 FORLOOP 经 callJITFull 主路径不经 spec trampoline,arm64 端 archSupportsSpec=false 不应阻塞 arm64 FORLOOP emitter 调用;arm64 端 archSupportsForLoop 已可返 true 启用全套四形态。

**新增 emit 原语**(承 PJ3 FORLOOP safepoint):
- `EmitLdrbWtFromXnDisp`:`ldrb Wt, [Xn, #pimm12]`(32-bit zero-extended byte load,base `0x39400000`)
- `EmitCbnzW`:`cbnz Wt, label`(32-bit compare-branch-if-nonzero,base `0x35000000`,imm19 字偏移)+ `patchCbnzImm19` patch helper

arm64 safepoint 8 字节(`ldrb 4 + cbnz 4`) vs amd64 14 字节(`cmp byte 8 + jne 6`),节省 6 字节(RISC fixed-length 紧凑)。

**sseOp 翻译**(承 WithRegKBody / WithRegKBody2):`arm64ArithOpForSseOp` 把 amd64 SSE opcode 字节(0x58 ADDSD / 0x5C SUBSD / 0x59 MULSD / 0x5E DIVSD)映射到 arm64 浮点 emit 函数(EmitFadd/Fsub/Fmul/FdivDdDnDm),未识别 op 返 nil(caller 静默放弃,UnknownOp 测试覆盖)。

**剩余 PJ8+ 工程**(承 §A3 / §B3 优先级):
- `archCallJITSpec` arm64 spec trampoline 真实现(`x27=jitContext + x26=valueStackBase + BLR + 恢复`)+ `archSupportsSpec()` 翻 true
- arm64 PJ2 投机模板(reg-reg/reg-K/chain-KK)真接入 `archEmitArithSpec*`
- 物理 self-hosted runner 启用真 mmap+RX 端到端测试

**ROI 验证**:本里程碑落地后 arm64 Compile 端对 IC 六路径 + FORLOOP 全套四形态可见;经 trampoline_arm64.s 调用 mmap+RX 段端到端 V1-V22 真验需物理 runner(QEMU 不真模拟 i-cache + PROT_EXEC,字节级单测在 CI test-arm64 QEMU 跑过)。

### 9.13.2 PJ8 arm64 FORLOOP arch 闸门解耦(2026-06-26 落地)

承上轮 review COMMENT 标出真 bug:

**问题**:arm64 上 FORLOOP 形态过了 `SupportsAllOpcodes` 闸门(`analyzeShape(proto).ok` 对 FORLOOP 无 arch 守卫)却被 `Compile` 端 `info.isForLoop && archSupportsSpec()` 拦下;arm64 端 `archSupportsSpec()=false`,整个 FORLOOP 块跳过 → 执行落到 `archEmitLoadKReturn(buf, info.value)` 直返模板。

**错果**(body 形态):
- `analyzeForLoopBodyForm` 强制 `retB==2` 且不设 `value`/`writeRetA`
- 直返段内只 `mov x0,0; ret`,`writeRetA=false` 不回写,`preludeOp=0` 不跑 prelude
- `host.DoReturn(retA=aS, retB=2)` 读从未被 JIT 计算的 `R(aS)` → 返回栈上残值而非循环累加结果,循环体根本没执行

(空 body 形态 `retB=1` 无返回值,恰好无害;body / body2 形态不是)

**根因**:godoc 自陈"FORLOOP 经 callJITFull 不经 spec trampoline,不依赖 archSupportsSpec",但实际闸门用了 `archSupportsSpec()`,导致 arm64 emitter 字节级 byte-tested 但**永不被调用**。

**修法**:引入 `archSupportsForLoop()` 三 arch 实现(amd64 ✅ / arm64 ✅ / other ❌)解耦 spec trampoline 闸门。FORLOOP Compile 块 1890 行闸门改 `archSupportsForLoop()`,arm64 PJ3 全四形态字节级模板真接入完整,闸门返 true 启用全套。

**潜伏面**:当前 CI 对 arm64 只跑字节级子包单测,不执行 Compile 派发路径(arm64 e2e 留 PJ9 物理 runner);本 bug 未被现有测试捕获——arm64 专属潜伏隐患,一旦 arm64 P4 真执行即变 🔴 级静默错果。本批次修复纳入 PJ8 真接入闸门家族,与 [[design-claims-vs-codebase-physics]] §2「held pointer / 偏移在结构边界外重定位时静默失效」同源——结构性前提应有运行期断言而非靠注释维持。

**副修复**:`analyzeForLoopForm` 中 upvalue 上界 `guvB > 255` → `guvB > 254`(`uint8(guvB)+1` 在 255 时回绕为 0,而 0 在 `forLimitUpvalIdx` 语义里表示「不走 upval 路径」,Run 端跳过 host.GetUpval + SetReg → reg-limit 模板读到未填充 R(forLimitReg) → 错误循环界或误 deopt)。触达极低(需第 256 个 upvalue 作 FORLOOP 上界),但属边界自相矛盾,一行可修。

---

### 9.13.3 PJ8 arm64 spec trampoline asm 实装(2026-06-26 落地)

承 §9.13.1 stub→真接入 + §9.13.2 arch 闸门解耦,本批次落地 `archCallJITSpec` arm64 真实现 + 关联 trampoline asm。

**关键交付**:
- `trampoline_arm64.s::callJITSpec`(三参 `codeAddr/jitCtxAddr/vsBaseAddr` → uint64 返,framesize $80-32,对位 callJITFull 同款 Plan 9 arm64 形态)
- `trampoline_linux_arm64.go::CallJITSpec`(noescape Go 包装 + 文档)
- `trampoline_other.go::CallJITSpec` stub(cross-build 通过,非 linux/arm64 panic on call)
- `arch_arm64.go::archCallJITSpec` 由 panic stub 改真代理 `jitarm64.CallJITSpec`

**vs callJITFull 差异**:
- callJITFull 只装 `X27=jitCtx`(已就绪,EmptyConst 用)
- callJITSpec 多装 `X26=valueStackBase`(对位 amd64 callJITSpec 装 `rbx=vsBase`;PJ2 投机模板 + PJ3 FORLOOP body/body2/RegLimit 需 `[x26+disp]` 寻址值栈)

**Plan 9 arm64 框架**(承 callJITFull 同款,framesize 80 字节):
- Go auto-prologue STP X29 X30 + SUB SP 96 → 帧起点
- 手存 X19-X27(STP × 4 + MOVD R27)进 frame[0..72]
- 装 R27 = jitCtxAddr / R26 = vsBaseAddr(STP 后覆盖安全)
- BL (R8) 跳进 mmap 段(BL Reg = arm64 BLR)
- 段 RET 回弹后手动 LDP 恢复 X19-X27
- Go auto-epilogue 恢复 X29 X30 + ADD SP + RET

**当前状态**:
- ✅ trampoline asm 实装完整(对位 amd64 callJITSpec)
- ✅ Go 包装 + cross-build stub
- ✅ archCallJITSpec 真代理(panic stub 消除)
- ⏳ `archSupportsSpec()` 仍保持 false(arm64 PJ2 投机 + PJ3 FORLOOP body/body2/RegLimit 三路径暂不启用)
- ⏳ 物理 self-hosted runner 端到端 V1-V22 验证(QEMU 不真模拟 i-cache + PROT_EXEC 不能可靠 e2e)

**剩余 PJ8+ 工程**(承 §A3 / §B3):
- `archSupportsSpec()` 翻 true(arm64 PJ2 + PJ3 body/body2/RegLimit 三路径自动启用,模板已字节级 byte-tested + 接线 byte-correct)
- 物理 self-hosted runner 启用真 mmap+RX 端到端测试
- darwin/arm64 W^X MAP_JIT spike(本批 trampoline_other.go 已留 stub,完整化待后续)

**ROI**:本里程碑后 arm64 完整 trampoline 协议(callJITFull / callJITSpec 双轨)就绪,启用 archSupportsSpec=true 即可在物理 runner 上端到端跑通 PJ2 投机 + PJ3 全四形态。spec trampoline 是本批 PJ8 工程组件的最后一块物理基础,后续工程只剩翻闸门 + 端到端实测。

---

### 9.14 PJ5 CALL void 简化形态打通(2026-06-27 落地)

承 §9.12 PJ5 工程基础(EmitHelperCall amd64 + EmitHelperCallArm64 + archEmitHelperCall 三 arch),本批次落地 PJ5 第一个**真升层 + 真接入**形态:CALL void(0 参 0 返 CALL + RETURN void)。

**形态范围**(setter 22 子 + getter 1 返 22 子 + getter N=2/N=3 返值 4 子 = 48 子形态,luac 编译产物):

setter 子态(0 返值 retB=1,22 子):
- 形态 A0/B0:`MOVE/GETUPVAL + CALL A 1 1 + RETURN 0 1` — 0 参
- 形态 A1K/B1K:`... + LOADK A+1 + CALL A 2 1 + RETURN 0 1` — 1 K 常量参
- 形态 A1R/B1R:`... + MOVE A+1 + CALL A 2 1 + RETURN 0 1` — 1 reg 参
- 形态 A2K/B2K + A1K1R/B1K1R + A1R1K/B1R1K + A2R/B2R:2 参四组合 K+K/K+R/R+K/R+R(长度 5)
- 形态 A3*/B3*:3 参四组合 K+K+K/K+K+R/.../R+R+R(长度 6,`... + (LOADK|MOVE)×3 + CALL A 4 1 + RETURN 0 1`)— 8 子

getter 子态(N=1 返值 retB=2,22 子):
- 形态 AR1/BR1:`MOVE/GETUPVAL + CALL A 1 2 + RETURN A 2 + dead RETURN` — 0 参 1 返
- 形态 A1KR1/B1KR1:`MOVE/GETUPVAL + LOADK + CALL A 2 2 + RETURN A 2 + dead RETURN` — 1 K 参 1 返
- 形态 A1RR1/B1RR1:`MOVE/GETUPVAL + MOVE + CALL A 2 2 + RETURN A 2 + dead RETURN` — 1 reg 参 1 返
- 形态 A2KR1/B2KR1 + A2RR1/B2RR1 + A1K1RR1/B1K1RR1 + A1R1KR1/B1R1KR1:2 参四组合(长度 6)
- 形态 A3*R1/B3*R1:3 参四组合 K+K+K/K+K+R/.../R+R+R(长度 7)— 8 子

N>=2 返值 getter 子态(0 参 N=2/N=3 返,4 子):
- 形态 ARetN2/BRetN2:`MOVE/GETUPVAL + CALL A 1 3 + MOVE×2 + RETURN A=callA+2 B=3 + 隐式 RETURN B=1`(长度 6,N=2)
- 形态 ARetN3/BRetN3:`MOVE/GETUPVAL + CALL A 1 4 + MOVE×3 + RETURN A=callA+3 B=4 + 隐式 RETURN B=1`(长度 7,N=3)
- Run 端 prelude CALL 后做 N 个 MOVE 拷贝(R(callA+nret+k) ← R(callA+k))保留 byte-equal;末尾 DoReturn 用 retA=callA+nret retB=nret+1 完成多值回填

**关键交付**:
- `internal/gibbous/jit/host.go::P4HostState.CallBaseline`:新接口,语义是 baseline doCall 分派(host/crescent/__call/全形态 gibbous 一律同步跑完),**绕过 tryIndirectCallee R3 indirect 哨兵** — 因为 PJ5 简化形态没有 wasm-level 段内 call_indirect 通道,若返 indirect 哨兵会让被调帧悬挂但永不执行(UAF 风险)
- `internal/crescent/gibbous_host.go::State.CallBaseline`:实装(复用 DoCall 的 baseline 分支,只跳过 tryIndirectCallee 快路径)
- `internal/gibbous/jit/compiler.go::analyzeCallVoidForm`:形态识别(MOVE/GETUPVAL + CALL B=1 C=1 + RETURN B=1 守卫严密)
- `internal/gibbous/jit/code.go::Run prelude CALL case`:据 isCallUpval 分流 host.GetReg / host.GetUpval 预处理,然后 host.CallBaseline + DoReturn
- `internal/gibbous/jit/probes.go::SpecCallVoidHits`:prove-the-path 白盒命中探针

**P2 analyzer scope-aware 扩展**(承同批 commit,打通 PJ5 真升层关键关):
- `internal/bridge/analyzer.go::AnalyzeProtoWithOuter`:新接口,接受 outerLocalFuncs 上下文(本 proto 参数同名遮蔽剔除安全)
- `internal/frontend/compile/funcstate.go::funcState.localFnAsts`:跟踪本 funcState 内 `local function X` 定义的 fn AST
- `internal/frontend/compile/analyze_on.go::analyzeCompilabilityWithOuter`:收集 outerFS 链上 localFnAsts 合并视图(近层覆盖远层),传给 bridge
- 改 walkFuncExpr sub-visitor.localFuncs 继承父 visitor 同款(同一 AnalyzeProto 内嵌套 FuncExpr)
- 修复前:invoker proto 独立 AnalyzeProto 调用,visitor.localFuncs 空 → noop 标 callsUnknownFn → ReasonUnknownCall → invoker NotCompilable
- 修复后:outerLocalFuncs 含 noop → isKnownLocalCall=true → 递归判 noop.Body(yield 等含量按 isKnownLocalCall 路径传染回 invoker),invoker 形态 Compilable + P4 升层可触达

**测试覆盖**:
- 单测 12 个(`compiler_pj5_call_test.go`):Recognize × 4(MOVE/GETUPVAL/1ArgK/1ArgReg/RetGetter)+ Reject(9 子测覆盖每条守卫)+ RunCallVoidPath/UpvalPath/1ArgKPath/1ArgRegPath(端到端)+ ErrPropagate + SupportsAllOpcodesGate + SpecCallVoidHits 探针
- e2e 5 个(`internal/crescent/gibbous_pj5_call_e2e_test.go`):FormB_Upval(B0)+ FormB1K_UpvalArg(B1K + sum=2100)+ FormB1R_UpvalArg(B1R + sum=1275)+ FormBR1_GetterUpval(BR1 + s=2100)+ FormB2K_UpvalArgs(B2K + sum=6000)
- difftest-p4 6 个(`test/difftest/p4_test.go`):p4_call_void(A0)+ p4_call_void_upval(B0)+ p4_call_void_upval_1argk(B1K)+ p4_call_void_upval_1argreg(B1R)+ p4_call_getter_upval(BR1)+ p4_call_void_upval_2argk(B2K)— oracle / crescent / p4-jit 三方 byte-equal

**剩余 PJ5 完整接入工程**(承 §9.12 估算):
- TAILCALL inline(承 `function() return g() end` 编 TAILCALL 而非 CALL,需独立形态识别 + 帧复用协议)
- 含参 CALL(CALL.B >= 2):MOVE+LOADK*N+CALL+RETURN void 形态扩展
- 含返 CALL(CALL.C >= 2 / RETURN.B >= 2):返回值经 R(A) 写回 + RETURN 路径
- OSR exit + bit50 协议拍板(承 04 §7.2 + 05 §4.3,等用户决策性输入)
- 段内 inline EmitCallInline 模板(amd64/arm64 真发射 `mov rax, &host.CallBaseline; call rax`,跳过 Go 端 prelude round-trip,留 PJ5+ 完整版)

**ROI**:**PJ5 简化形态打通是 P4 调用族 inline 第一块真接入物理证据**(对位 PJ4 表 IC 形态完整六路径的形态学进展)。SpecCallVoidHits=1 的 prove-the-path 命中是 P4 PJ5 首条真升层路径,与 P2 scope-aware analyzer 扩展捆绑——意味着真实业务 nested closure 调外层 known local fn 形态都进入了 P4 可升层范围。后续扩展(含参 / 含返 / TAILCALL)按此形态学路径逐步扩,工程量已被本里程碑系统打通。

---

### 9.15 PJ5 TAILCALL 八子形态打通(2026-06-27 落地)

承 §9.14 PJ5 CALL void 主路径,本批扩到调用族另一条主路径:**TAILCALL 尾调用形态**。luac `stmtReturn`(`frontend/compile/stmt.go::stmtReturn`)对单 CallExpr 作 return 唯一表达式翻成 `TAILCALL A B 0 + RETURN A 0(dead,to-top) + RETURN 0 1(隐式)`,本节落地 PJ5 第二条真升层路径。**本批形态扩**(2026-06-27):从「0/1/2 K 参 + 1 reg 参 = 8 子形态」扩到「2 参四组合 K+K/K+R/R+K/R+R = 14 子形态」,覆盖 transparent wrapper 多参形态。

**形态范围**(双 callee × {0 参 + 1 K + 1 reg + 2 参四组合 K+K/K+R/R+K/R+R + 3 参四组合 K+K+K/.../R+R+R} = 22 子形态,luac 编译产物;TAILCALL.C 恒 0):

| 子态 | callee 装载 | 参数 | 长度 |
|---|---|---|---|
| **TA0** | MOVE(parameter) | 0 | 4 |
| **TB0** | GETUPVAL(upvalue) | 0 | 4 |
| **TA1K** | MOVE + LOADK | 1 K | 5 |
| **TB1K** | GETUPVAL + LOADK | 1 K | 5 |
| **TA1R** | MOVE + MOVE | 1 reg | 5 |
| **TB1R** | GETUPVAL + MOVE | 1 reg | 5 |
| **TA2K** | MOVE + LOADK + LOADK | 2 K (K+K) | 6 |
| **TB2K** | GETUPVAL + LOADK + LOADK | 2 K (K+K) | 6 |
| **TA1K1R** | MOVE + LOADK + MOVE | K+R | 6 |
| **TB1K1R** | GETUPVAL + LOADK + MOVE | K+R | 6 |
| **TA1R1K** | MOVE + MOVE + LOADK | R+K | 6 |
| **TB1R1K** | GETUPVAL + MOVE + LOADK | R+K | 6 |
| **TA2R** | MOVE + MOVE + MOVE | R+R | 6 |
| **TB2R** | GETUPVAL + MOVE + MOVE | R+R | 6 |
| **TA3*** | MOVE + (LOADK\|MOVE)×3 | 3 参四组合 | 7 |
| **TB3*** | GETUPVAL + (LOADK\|MOVE)×3 | 3 参四组合 | 7 |

**关键改动**:

- `internal/gibbous/jit/host.go::P4HostState.TailCall`:新接口,语义对位 `crescent.State.TailCall`(已实装)三态分支:
  - **0 = Lua 尾完成**:`doTailCall` 弹本帧 + 压 callee 帧 + `executeFrom` 同步驱动到完成 + nresults 写回上层 funcIdx。Run 端**跳过 DoReturn 直接 return 0**(本帧已弹)。
  - **1 = ERR**:raise pending → 上层冒泡。
  - **2 = host 尾完成**:结果落 R(callA..),G 帧未弹。Run 端**fall through 走末尾 DoReturn**(对位 dead RETURN A=callA B=0 to-top,DoReturn 内 B=0 多值路径)。
- `internal/gibbous/jit/compiler.go::analyzeTailCallForm`:形态识别长度 4/5/6 共 8 子态,严密校验 TAILCALL.C=0 + dead RETURN.B=0 + 隐式 RETURN.B=1 + dead RETURN.A=callA 等不变式。retPC 指 dead RETURN(2/3/4),retA=callA,retB=0(host 尾完成路径走 DoReturn 多值 to-top)。
- `internal/gibbous/jit/code.go::Run` prelude TAILCALL case:与 CALL void case 同结构装载(MOVE/GETUPVAL 装 callee + LOADK/MOVE 装参),调 host.TailCall + 三态分支(0 直接 return 0 / 1 return 1 / 2 fall through)。
- `internal/gibbous/jit/probes.go::SpecTailCallHits`:新探针,Compile 命中 isTailCall=true 时 ++,prove-the-path 白盒命中证据。

**测试覆盖**:

- 11 单测(`compiler_pj5_tailcall_test.go`):Recognize × 5(TA0/TB0/TB1K/TB1R/TB2K)+ Reject 9 子测 + Run 端到端 × 4(三态分支 0/1/2 + 1 K 参装载)+ F7 闸门 + SpecTailCallHits 互斥校验
- 4 e2e(`internal/crescent/gibbous_pj5_tailcall_e2e_test.go`):FormTB0 / FormTB1K / FormTB1R / FormTB2K — 真升层 + SpecTailCallHits=1 命中实证 + 业务结果断言
- 4 difftest-p4(`test/difftest/p4_test.go`):p4_tailcall_upval / _1argk / _1argreg / _2argk — 三方 byte-equal(oracle lua5.1 / crescent / p4-jit)

**形态 TA\* parameter-callee 真升层不可达**:与 CALL void 形态 A\* 同款限制 — P2 analyzer 把 parameter call 标 `ReasonUnknownCall`(parameter 可能是 coroutine.yield),visitor 设计保守拒。形态 TA\* 单测覆盖在 jit 包内通过 mock host 直接验,crescent e2e 路径不可达。real-world 业务高频形态是 closure 调外层 known local fn(形态 TB\*),那条路径已通。

**剩余 PJ5 完整接入工程**(承 §9.12 估算):

- 多 reg 参形态(2/3 reg 参 — 当前 2 K 参已通)
- 含返 N 值形态(N>=2)
- 含返 K 参 1 返形态(getter A1KR1/B1KR1/A1RR1/B1RR1)
- OSR exit 实装(承 04 §3.3,需投机模板真接入时同批)
- 段内 inline EmitCallInline 模板(amd64/arm64 真发射,跳过 Go 端 prelude round-trip,留 PJ5+ 完整版)

**ROI**:**PJ5 TAILCALL 形态打通是 P4 调用族 inline 第二块真接入物理证据**(对位 §9.14 CALL void 十子形态)。SpecTailCallHits=1 命中证 P4 真升层 + 形态识别真命中;`bounce() return f()` 模式是真实业务高频(transparent wrapper / proxy 函数),进入 P4 可升层范围意味着 method-JIT 调用族 inline 覆盖再扩。后续扩展(多 reg 参 / 含返 N 值)按此形态学路径继续扩。

---

### 9.16 PJ5 调用族 inline 完整形态学矩阵汇总(2026-06-27 落地)

承 §9.14/§9.15 PJ5 CALL void / TAILCALL 真接入主路径。本节汇总累计推进的完整形态学矩阵(204 + 70 = 274 子形态),作为 PJ5 当前覆盖面的单一参考。

**形态学维度**(三轴):
- **callee 装载**:MOVE(parameter callee)/ GETUPVAL(upvalue callee 闭包调外层 known local fn)— 双 callee
- **参数装载**:0 / 1 K / 1 reg / 2..6 参四组合 K+K / K+R / R+K / R+R(每参独立 K/reg 选择)
- **返值形态**:setter(0 返)/ getter(1 返)/ N>=2 返值 getter(0 参 N=2/N=3 + 1 K/reg 参 N=2)/ tail(透传)

**CALL void 子形态计数**(204 = setter 102 + getter 1 返 94 + getter N>=2 返值 8):

| 维度 | setter | getter 1 返 | getter N>=2 返值 |
|---|---|---|---|
| 0 参 | 2(A0/B0) | 2(AR1/BR1) | 4(N=2/N=3 × 双 callee) |
| 1 K 参 | 2(A1K/B1K) | 2(A1KR1/B1KR1) | 2(N=2 × 双 callee) |
| 1 reg 参 | 2(A1R/B1R) | 2(A1RR1/B1RR1) | 2(N=2 × 双 callee) |
| 2 参四组合 | 8(A2K..A2R + B2K..B2R) | 8(A2KR1..A2RR1 + B2KR1..B2RR1) | — |
| 3 参四组合 | 8(A3K..A3R + B3K..B3R) | 8(A3*R1 × 双 callee) | — |
| 4 参四组合 | 8(A4K..A4R + B4K..B4R)| 8(A4*R1 × 双 callee) | — |
| 5 参四组合 | 8(A5K..A5R + B5K..B5R)| 8(A5*R1 × 双 callee)| — |
| 6 参四组合 | 64(A6 × 双 callee × 32 排列)| 56(A6*R1 × 双 callee × 28 排列)| — |
| **小计** | 102 | 94 | 8 |

**TAILCALL 子形态计数**(70 = 双 callee × {0/1K/1R/2/3/4/5 参四组合 + 6 参 32 排列}):
- TA0/TB0 + TA1K/TB1K + TA1R/TB1R(共 6 子)
- TA2*/TB2* + TA3*/TB3* + TA4*/TB4* + TA5*/TB5*(2 参四组合 × 双 callee × 4 长度 = 32 子)
- 6 参 TAILCALL ,TailCall}`:实装(复用 DoCall/doTailCall baseline 分支,绕过 R3 indirect 哨兵 / 同步驱动 callee 链)
- `internal/gibbous/jit/compiler.go::{analyzeCallVoidForm,analyzeTailCallForm}`:形态识别(长度 3..10 / 4..9,严密 luac 子形态校验)
- `internal/gibbous/jit/compiler.go::decodeArgFromOp`:LOADK/MOVE 通用参装载 helper(8+ 子分支复用)
- `internal/gibbous/jit/code.go::Run prelude` CALL/TAILCALL case:6 参装载分流 + N 个 MOVE 拷贝(N>=2 返值)+ TailCall 三态分支
- `internal/gibbous/jit/probes.go::{SpecCallVoidHits,SpecTailCallHits}`:prove-the-path 白盒命中探针
- `internal/bridge/analyzer.go::AnalyzeProtoWithOuter`:P2 scope-aware 扩展(跨 Proto 传递 outer localFnAsts,打通嵌套 closure 调外层 known local fn 形态的真升层)

**测试覆盖**:33 e2e CALL + 12 e2e TAILCALL + 32 difftest CALL + 11 difftest TAILCALL 全部 SpecCallVoidHits/SpecTailCallHits=1 命中实证 + 三方 byte-equal(oracle lua5.1 / crescent / p4-jit)。

**剩余 PJ5 完整接入工程**():
- 7+ 参形态(预计 case 11+ 扩,工程膨胀线性)
- SELF method call(`obj:method()`)— 需 P2 visitMethodCallExpr 放宽或新设 known method whitelist
- N>=2 返值多参(2/3 参 N=2/N=3 返,工程类同 1 参 N=2)
- OSR exit 实装(承 04 §3.3,需投机模板真接入时同批)
- 段内 inline EmitCallInline 模板(amd64/arm64 真发射 + 跳过 Go 端 prelude round-trip)

**ROI 评估**:204 + 70 = 274 子形态完整覆盖 0..6 参 × {0/1 返/N=2 返/tail} × 双 callee 维度,对位 luajc 档「method-JIT 真升层主路径」基础设施达成,real-world 业务高频形态(透明 wrapper / proxy / multi-return getter / setter / OOP-style getter 等)全部进入 P4 升层范围。后续推进按 ROI 衰减。

### 9.17 PJ5 SELF method call inline 形态打通(2026-06-28 落地)

承 §9.16 PJ5 调用族 inline 完整形态学矩阵汇总,本节落地 PJ5 SELF method call inline 形态(`obj:method(args)` 真接入主路径)。

**关键拆解**:之前 P2 `visitMethodCallExpr` 一律标 `callsUnknownFn=true → ReasonUnknownCall`,SELF method call 路径**永久 NotCompilable**(P3 wasm 端虽实装 SELF 翻译亦因此死锁)。本批从可编译性分析层拆分:

- **新设 `ReasonSelfCall` 占位位**(F2-c):与 `ReasonBackendUnsupp` 同款手法 — 编译期保守占位,运行期 `recheckCompilabilityRuntime` 撤位 + `SupportsAllOpcodes` 守门
- `visitMethodCallExpr` 不再硬标 `callsUnknownFn`,改标 `sawSelfCall = true`(分离信号)
- `recheckCompilabilityRuntime` 占位位扩到 `(ReasonBackendUnsupp | ReasonSelfCall)`,F1-F6 + F2-a/F2-b 真实排除原样保留
- **F7 / SELF 真守门**:P4 jit `analyzeSelfCallForm` 在 `analyzeShape` 主分流命中即返 SupportsAllOpcodes=true

**形态识别**(`analyzeSelfCallForm`):

`internal/gibbous/jit/compiler.go::analyzeSelfCallForm` 识别长度 4..6 形态,共同结构 `[0]=MOVE/GETUPVAL` (recv → R(callA)) + `[1]=SELF` (R(callA)=method, R(callA+1)=self) + `[2..]=参数 + CALL/TAILCALL + RETURN`。

| 长度 | 形态 | 描述 |
|---|---|---|
| 4 | M0/U0 | 0 参 0 返 CALL void(`function(o) o:m() end`)|
| 4 | TM0/TU0 | 0 参 TAILCALL(luac TAILCALL 长度 4 dead 形态)|
| 5 | MR1/UR1 | 0 参 1 返 CALL getter(`local r = t:m()`)|
| 5 | TM0_5 | 0 参 TAILCALL 长度 5(`return t:m()` 实际形态:含 dead RETURN A=callA B=0 + 隐式 RETURN B=1)|
| 5 | M1K/M1R/U1K/U1R | 1 K/reg 参 0 返 CALL void(`t:m(42)` / `t:m(v)`)|
| 5 | TM1K/TM1R/TU1K/TU1R | 1 K/reg 参 TAILCALL |
| 6 | MR1+1K/U1KR1/... | 1 K/reg 参 1 返 CALL getter |
| 6 | M2*/U2* | 2 K/reg 参四组合 CALL void + TAILCALL |

**Run prelude SELF 预处理**:

```go
case CALL/TAILCALL:
    // 1) recv 装 R(callA)(MOVE/GETUPVAL)
    srcVal := host.GetReg(MOVE.B) / host.GetUpval(GETUPVAL.B)
    host.SetReg(callA, srcVal)
    // 2) SELF inline 预处理:method 取值 + self 装载
    if isSelfCall {
        host.Self(base, selfPC, callA, callA, selfMethodRK)  // byte-equal 解释器 SELF
    }
    // 3) 参数装 R(callA+offset..)(offset=2 跳过 self 槽)
    loadCallArgs(2 if isSelfCall else 1)
    // 4) CallBaseline / TailCall byte-equal P1
    host.CallBaseline / TailCall
```

**关键改动汇总**:

- `internal/bridge/compilability.go`:加 `ReasonSelfCall` 位(占位位语义,运行期重判撤位)
- `internal/bridge/analyzer.go`:`visitMethodCallExpr` 拆 `sawSelfCall` 信号 + `ReasonSelfCall` 标位,**不再叠加 ReasonUnknownCall**
- `internal/bridge/bridge.go::recheckCompilabilityRuntime`:占位位扩到 `(ReasonBackendUnsupp | ReasonSelfCall)`,`needsAutoRecheck` 守门同步
- `internal/bridge/std_logger.go::formatReasons`:F2 多位合并加 `selfCall`
- `internal/gibbous/jit/host.go`:`P4HostState` 加 `Self(base, pc, a, b, c) int32` 接口(crescent.State.Self 已实装)
- `internal/gibbous/jit/compiler.go`:加 `analyzeSelfCallForm` + 拆 `analyzeSelfCallForm4/5/6` 子函数;`analyzeShape` 加 SELF 分流(在 CALL void / TAILCALL 之后)
- `internal/gibbous/jit/compiler.go::Compile`:拷 SELF 字段(isSelfCall / selfCallA / selfMethodRK / selfRecvSrcReg / selfRecvIsUpval)
- `internal/gibbous/jit/code.go::p4Code`:同步加 SELF 字段
- `internal/gibbous/jit/code.go::Run prelude` CALL/TAILCALL case:SELF 预处理 + args offset 参数化(`loadCallArgs(offset)`)
- `internal/gibbous/jit/probes.go`:加 `SpecSelfCallHits` / `incSpecSelfCallHits` / `ResetSpecHits` 同步

**测试覆盖**(本批次落地):

- `internal/gibbous/jit/compiler_pj5_self_test.go`:9 形态识别单测(M0/U0 void + M0 tail + MR1 getter + M1K/M1R void + 拒识别短码 / 非 SELF / SELF.C reg form)
- `internal/crescent/gibbous_pj5_self_e2e_test.go`:**20 e2e 用例 SpecSelfCallHits=1 命中实证**(M0/U0/M1K/M1R/TM0/MR1 + M3K/M3R/M4R/M5R/M6R/M7R/TM3K/TM5R/MR2/MR3/MR2_1K + 嵌套 NestedSelfChain SpecSelfCallHits=2 + SelfThenCall 链 + 错误冒泡 NilRecv/BadMethod 2 个)
- `test/difftest/p4_test.go::p4Corpus`:**16 SELF 用例**三方 byte-equal(self_void_m0/u0/m1k/m1r + self_tail_m0/3k/5r + self_getter_m0 + self_void_m3k/m3r/m4r/m5r/m6r/m7r + 嵌套 self_nested_chain + self_then_call)
- `test/difftest/p4_test.go::TestP4_ConcurrentForceAll`:V18 -race 8 goroutine 并发 force-all SELF inline 安全(SELF + caller chain 加入 src,multi-State 并发无数据竞争)
- `internal/gibbous/jit/p4state_test.go`:**7 单测覆盖 OSR exit 协议骨架**(默认 P4SpecUnknown / nil 安全 / Install 转移 / Deopt 阈值前后 / 重编译转移 / MaxRecompileTries 上限 → P4StuckSpeculation)

**子形态计数**(完整覆盖):0..7 参 × {void / 1 返 / tail} × 双 receiver(M/U)+ N=2/N=3 返值 0/1 参 ≈ **2×(8 void + 8 getter + 8 tail) + 4 N>=2 = 52 子形态**;长度区间 4..11 通过 analyzeSelfCallForm4/5/6/7/8/9 + analyzeSelfCallFormN(N=6/7) 分流识别。

### 9.18 PJ5 OSR exit 协议骨架(2026-06-28 落地)

承 docs/design/p4-method-jit/04-osr-deopt.md §5 + §11 字段定义,落地 P4 内部投机子状态机骨架(方案 A:P4 自管,P2 三态枚举不变)。

**关键改动**:

- `internal/gibbous/jit/p4state.go`(~200 行):
  - 加 `P4SpecState` 枚举(`P4SpecUnknown` / `P4Speculative` / `P4Deoptimized` / `P4StuckSpeculation`)
  - 加 `p4SpecEntry` per-Proto 字段(`state` + `deoptCount` + `recompileCount`)
  - 加 `p4SpecState[*bytecode.Proto]` map + `sync.Mutex` 守护(V18 -race 友好,OSR exit 冷路径 lock 开销可忽略)
  - 加 `DeoptThreshold = 16`(承 04 §5.6 校准:典型 3-5,本批 v0 宽松 16 防误触发)
  - 加 `MaxRecompileTries = 2`(承 04 §5.3 校准:典型 1-2,本批 v0 用 2)
  - 加 `onOSRExit(proto)` / `onP4Install(proto)` 状态转移函数(伪码承 04 §5.2 状态图)
  - 加 `P4SpecStateOf(proto)` / `ResetP4SpecState()` 测试入口
  - 加 `SpecP4DeoptHits` / `SpecP4StuckHits` 探针(probes.go `ResetSpecHits` 同步)

- `internal/gibbous/jit/p4state_test.go`(~150 行):7 单测覆盖状态转移表
  - `TestP4SpecState_DefaultIsUnknown`:未注册 Proto 默认 P4SpecUnknown
  - `TestP4SpecState_NilProtoSafe`:nil Proto 安全返回 + 不 panic
  - `TestP4SpecState_InstallTransitions`:首次 install → P4Speculative
  - `TestP4SpecState_DeoptCountUnderThreshold`:deoptCount < 阈值不切状态
  - `TestP4SpecState_DeoptCountReachThreshold`:deoptCount ≥ 阈值 → P4Deoptimized + SpecP4DeoptHits=1
  - `TestP4SpecState_RecompileTransitions`:P4Deoptimized → P4Speculative(recompileCount += 1)
  - `TestP4SpecState_MaxRecompileTriesReachedStuck`:达 MaxRecompileTries 上限 → P4StuckSpeculation + SpecP4StuckHits=1

**方案 A 严格遵守**:
- P4 内部 `p4SpecState[proto]` 子状态机叠加在 P2 `pd.TierState` 之上
- **P2 视角看 Proto 仍是 `TierGibbous`**(P2 三态 `TierInterp` / `TierGibbous` / `TierStuck` 单向无环不变)
- P4 端「降层」语义不写 P2 `tierState`(承 04 §5.5 + §5.6)
- 重训练 + 重编译协议全 P4 自管(P4Speculative ⇄ P4Deoptimized,反复失败 → P4StuckSpeculation 吸收态)

**OSR exit 协议已接通(2026-06-28,承 §9.19 spec template 落地)**:p4SpecState 子状态机从纯骨架变真实工作路径——PJ5 SELF + CALL spec template(§9.19)的 SELF NodeHit guard 失败(table shape 变 / key 退化 / NodeVal=nil)= 真投机失败 → `runSpecSelfCall` deopt 路径调 `onOSRExit(proto)` 累积 deopt 计数;`compileSpecSelfCall` 安装时调 `onP4Install(proto)` 注册 `P4Speculative`。OSR exit 协议(承 04 §5)在 PJ5 SELF spec template 路径**首次真实闭环**:guard 失败 → 累积 deopt → 达 DeoptThreshold P4Deoptimized(撤投机)→ 重编译 → 反复失败 P4StuckSpeculation(拉黑投机)。CALL void / TAILCALL 非 spec 形态仍走 baseline doCall(无投机 guard,无 deopt)。

**剩余 SELF 完整接入工程**(承 §9.16 同款 ROI 评估),与 PJ4 SELF NodeHit 字节级 inline 协同)
- N>=2 返值多参(2/3 参 N=2/N=3 返值,工程类同 1 参 N=2)
- 段内 EmitSelfCallInline 模板(amd64/arm64 真发射 + 跳过 Go 端 host.Self round-trip)
- OSR exit 实装(承 04 §3.3,需投机模板真接入时同批)

**ROI 评估**:SELF inline 完整 0..7 参覆盖后,real-world OOP 业务调用形态(`obj:method()` / `obj:method(arg)` / `obj:method(a, b, c, ...)` / `return obj:method(...)` / `local r = obj:method()` 等)全部进入 P4 升层范围。SELF 形态总占 OOP-style 业务调用约 30-50%,与 §9.16 调用族 inline 矩阵协同覆盖 method-JIT 主路径。后续 NodeHit / 段内 inline / N>=2 返值多参。

**baseline 实测**(本机 Xeon 6982P / Linux amd64):

```
BenchmarkGibbousJIT_PJ5SelfCall-24       14001 ns/op  72 B/op  2 allocs
BenchmarkGibbousJIT_PJ5SelfCallCresc-24  11755 ns/op  72 B/op  2 allocs
```

**P4 ratio = 14001/11755 = 1.19x(比 crescent 慢 19%)**——印证「正确性接入而非性能加速」结论:Run prelude 路径走 `host.Self → host.CallBaseline` 经 Go→段→Go round-trip,反比解释器单循环慢。**段内 SELF 段字节级 inline 真接入后**(§9.19),通过 IC NodeHit guard + 跳过 host.Self round-trip 改善到 1.12x;CALL 段字节级 inline 是下一阶段瓶颈攻坚。

### 9.19 PJ5 SELF + CALL spec template 真接入(2026-06-28 落地)

承 §9.10 PJ4 EmitSelfNodeHit 字节级模板(166 字节)复用 + §9.17 PJ5 SELF inline 升级,落地 PJ5 SELF + CALL 形态的 **SELF 段字节级 inline**(IC NodeHit 命中时跳过 host.Self round-trip)。

**关键发现 — SELF 聚合成 FBSelfMono 而非 FBTableMono**:

`aggregator.go::extractTableFeedback` 的 `opSelf` 分支把 SELF IC 聚合成 **`FBSelfMono`**(非 `FBTableMono`)。**PJ5 SELF + CALL 是首个真实触达 SELF feedback 的路径**——PJ4 SELF NodeHit(§9.10)因 luac 不真编 `SELF + RETURN` 2-op 形态仅合成驱动单测(单测自塞 `FBTableMono`),从未触达真实 SELF feedback,故那里用 `FBTableMono` 是未触发的占位。本路径用正确的 `FBSelfMono`。

**PJ4 SELF NodeHit/ArrayHit 独立路径不可达性论证**(2026-06-28 probe 实证):

probe wangshu frontend 验 `obj:method` 无 args 形态 → **parser 报语法错** `function arguments expected`,Lua 5.1 严格语法 `:` 方法引用必须接 `(args)`。即:
- `obj:method` 单独 expression — **语法错误**
- `local m = obj:method` — **语法错误**
- `function f(obj) return obj:method end` — **语法错误**

PJ4 SELF NodeHit/ArrayHit 独立路径(compileIcSelfArrayHit / compileIcSelfNodeHit)在生产路径**永不可达**(luac/wangshu 编不出 SELF + RETURN 2-op 形态)。**但**其字节级模板(EmitSelfArrayHit / EmitSelfNodeHit)经 PJ5 SELF spec template(§9.19)完整复用 + **真实证 13 e2e SpecSelfCallSpecHits 命中 + 11 difftest 三方 byte-equal**——PJ4 SELF NodeHit/ArrayHit 模板已通过 PJ5 路径间接达成生产真实证。

**形态边界**(初批仅 0 参 0 返 CALL void,form M0):

```
[0] MOVE/GETUPVAL A=callA B=recvSrc  (装 recv 到 R(callA))
[1] SELF     A=callA B=callA C=K_method  (IC[1] = NodeHit feedback)
[2] CALL     A=callA B=2 C=1     (0 参 0 返)
[3] RETURN   A=0     B=1
```

**执行路径**(`runSpecSelfCall`):
1. 装 R(callA) = recv(模拟 luac MOVE/GETUPVAL,因 spec 段从 R(callA) 字节级读 receiver)
2. `callJITSpec` 跑 `EmitSelfNodeHit` 模板:成功 → R(callA)=method + R(callA+1)=self;失败 deopt → 降级 `host.Self`(R(callA+1) 已被模板 store recv,P1 SELF case 同款步骤,byte-equal)
3. `host.CallBaseline` 完成 CALL 段
4. `host.DoReturn` 弹帧

**关键改动**:
- `analyzeSelfCallSpecForm`:识别长度 4 SELF + CALL void 0 参 + IC[1] NodeHit + `FBSelfMono` feedback 命中 + stableKey 编译期固化
- `compileSpecSelfCall`:emit `archEmitSelfNodeHit` 166 字节模板 + 设 `useSpecSelfCall` + 复用 `useSpec` / `specDeoptCode`
- `code.go::Run` useSpec 块最前加 `useSpecSelfCall` 独立子路径 `runSpecSelfCall`(自包含,不与 PJ2/PJ3/PJ4 spec 分流混淆)
- `probes.go`:`SpecSelfCallSpecHits` 专属探针

**测试覆盖**:
- `compiler_pj5_self_test.go`:3 单测(M0 命中 + RejectNoFeedback + RejectNoNodeHit)
- `gibbous_pj5_self_e2e_test.go::TestPJ5_SelfCall_E2E_SpecTemplate_WarmupThenForce`:Phase 1 warmup 填 SELF IC[1]=NodeHit + FBSelfMono;Phase 2 force-all 升 caller → spec 模板命中 `SpecSelfCallSpecHits` 0 → 1 实证(prove-the-path)+ byte-equal P1(result=101)

**benchmark 实测**(Xeon 6982P / Linux amd64):

```
BenchmarkGibbousJIT_PJ5SelfCallSpec-24       8953 ns/op  72 B/op  2 allocs
BenchmarkGibbousJIT_PJ5SelfCallSpecCresc-24  7961 ns/op  72 B/op  2 allocs
```

**P4 ratio = 8953/7961 = 1.12x(比 crescent 慢 12%)**——对比非 spec 版(整段 host.Self+CallBaseline)1.19x,**SELF 段字节级 inline 把 host.Self round-trip 省了,相对改善 ~6%**。仍比 crescent 慢是因 CALL 段仍走 host.CallBaseline + P4 升层 + DoReturn 弹帧固定开销主导。

**CALL 段瓶颈 profile + 摊薄验证**(承 [perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md) §1 profile 先行 + §5 跨形态基线对照):

CPU profile(`PJ5SelfCallSpec`)显示 executeLoop 95% / doCall 74% / enterGibbous 71% / enterLuaFrame 30% / popCallInfo 6% —— **CALL 段的"瓶颈"是被调 method 体的真实执行 + 帧建拆,不是 SELF 段或 CALL dispatch**;本 bench method 体过简(单 ADD `count++`)放大 trampoline 占比。这与 P3 PW10 call 核退化同源(根因是帧建立 + 重入,非 dispatch)。

**摊薄验证**(`PJ5SelfCallHeavyBody`,method 体含 FORLOOP):

```
BenchmarkGibbousJIT_PJ5SelfCallHeavyBody-24       88540 ns/op
BenchmarkGibbousJIT_PJ5SelfCallHeavyBodyCresc-24  93221 ns/op
```

**P4 ratio = 0.95x —— P4 比 crescent 快 5%!** method 体含 FORLOOP 时,P4 升层 method 体(PJ3 FORLOOP 字节级 inline 大幅加速)主导,caller SELF+CALL trampoline 开销被摊薄,P4 反超。**完整画面**:简单 method 体(count++)→ trampoline 占比大 → 1.12x 慢(bench 形态放大);计算密集 method 体(FORLOOP)→ method 加速主导 → 0.95x 快(真实 OOP 业务场景)。

**OSR exit 协议已接通**(承 §9.18):spec template SELF NodeHit guard 失败 → `runSpecSelfCall` 调 `onOSRExit(proto)` 累积 deopt;`compileSpecSelfCall` 安装时 `onP4Install(proto)`。p4SpecState 子状态机从纯骨架变真实工作路径。

**形态完整覆盖矩阵**(承 §9.19 后续批次,本节 2026-06-28 同日扩):

| 维度 | 子形态 | 已落地批次 |
|---|---|---|
| 参数数 | 0/1K/1Reg/2K/2Reg/3K/3Reg/4+ K/Reg | 上批 ee17319..38cac18 |
| 方向 | void(retB=1)/ getter 1 返(retB=2)/ TAILCALL(三态) | 上批 a004998 + 28aa6f2 |
| receiver | MOVE reg(字节级 recv inline)/ GETUPVAL upval(host helper) | 上批 5ff0bf8 + 99c7d2b |
| **N=2/3 返 drop multi-ret** | form4(0参) + form5(1K/Reg) + form6(2K/Reg) + form7(3K/Reg) + form8(4K/Reg) + form9(5K/Reg) + formN(6+ K/Reg) | **5c5c0ae + 9f2ff24** |
| **N=4..15 返 drop multi-ret** | 同上 form4..N(`cC∈{5..16}`) | **84c7ed4** |

**N=2/3 返 drop multi-ret 形态扩**(2026-06-28,承 5c5c0ae + 9f2ff24):

probe 实证 caller `local a, b = t:m(args)` 由 luac 编出 `[N-2]CALL B=N+1 C=3/4` 形态(C=3 表 N=2 返,C=4 表 N=3 返,retB=1 主调 RETURN B=1)。analyzeSelfCallForm{6,7,8,9,N} 各 CALL 分支 `cC != 1 || retB != 1` 守门改为 `(cC != 1 && cC != 3 && cC != 4) || retB != 1` — 同款手法 form4 line 2662 + form5 line 2848 上批已用。

**N>=4 返扩**(2026-06-28,承 84c7ed4 + 91dcf07 + 84a031d + 8081695):

probe luac 实证 N=K 返形态 cC=K+1 一致(N=4 返 cC=5 / N=5 返 cC=6 / ...)。加 `isValidSpecCallRetCount(cC int)` helper(compiler.go line 2591):`cC == 1 || (cC >= 3 && cC <= 16)`,允许 0 返 + 2..15 返。sed 替换 7 处守门 `(cC != 1 && cC != 3 && cC != 4)` → `!isValidSpecCallRetCount(cC)`。

上界 16(N=15 返)选定:Lua 5.1 CALL C 字段最大 255 但实用 method 多返值典型 N<=8;N<=15 保守覆盖几乎所有真实业务。

**N=4 返多形态 e2e**(承 91dcf07,4 用例 SpecSelfCallSpecHits 0→1):
- MultiRetN4_0Param(form4 N=4 返 0 参 cC=5)
- MultiRetN4_1KArg(form5 N=4 返 1 K 参 cC=5)
- MultiRetN4_1RegArg(form5 N=4 返 1 reg 参 cC=5)
- MultiRetN4_3KArg(form7 N=4 返 3 K 参 cC=5)
- MultiRetN5_0Param(form4 N=5 返 0 参 cC=6)

**N=4 返多形态 difftest**(承 84a031d,5 用例 oracle/crescent/p4-jit 全 byte-equal):
- p4_self_spec_multiret_n4_0arg / n4_1karg / n4_1regarg / n4_3kargs / n5_0arg

**N=4 返 bench 完整画面**(承 1eb520d + 91dcf07 + fd87c96):
- BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN4-24 = 18786 ns/op,Cresc = 17175,**P4 ratio 1.094x 慢**(简单 method 体)
- BenchmarkGibbousJIT_PJ5SelfCallHeavyBodyMultiRetN4-24 = 88721 ns/op,Cresc = 87726,**P4 ratio 1.011x 持平**(heavy body)
- BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN8-24 = 20630 ns/op,Cresc = 18699,**P4 ratio 1.103x 慢**(N=8 边界,简单 method 体)

**N=0..N=8 趋势分析**(承 fd87c96):
- N=0 返 PJ5SelfCallSpec:1.12x 慢(基线 trampoline 占比主导)
- N=4 返:1.094x 慢(N=4 SetReg 摊薄略减少 trampoline 占比)
- N=8 返:1.103x 慢(N=8 SetReg 多 4 次 ≈ 多 0.5% 开销)
- 每多 4 个 R(callA..) word store ≈ 多 0.5%(host.CallBaseline 内 N 次
  SetReg + DoReturn 弹 0 返值固定开销):真实业务 N>=8 罕见,1.1x 慢可接受

对比 N=0 返 PJ5SelfCallHeavyBody 0.95x 快 5%:N=4 多写 4 word 摊薄略减弱但仍持平 — 真实 OOP 业务场景 P4 性能 acceptable。

**V18 -race 增量**(承 8081695):TestP4_ConcurrentForceAll_MultiRet 8 goroutine 独立 State 并发跑 N=4 返路径 force-all-P4,结果与单跑 byte-equal,`go test -race` 过 — 验 host.CallBaseline 多 SetReg + DoReturn 0 返值收尾在并发下无 race。

**两层协议解耦**(host.CallBaseline + host.DoReturn 自然支持):
- `host.CallBaseline(callA, callB, callC)`:按 callC=3..16 把 N=2..15 返值落 R(callA..callA+N-1)作 local 直接绑
- `host.DoReturn(retA, retB=1)`:按主调 RETURN B=1 弹 0 返值收尾(N>=2 返值已落 local,主调函数无 return)

spec template 无需特殊处理 N>=2 返,SELF 段 EmitSelfNodeHit + args inline + recv inline 字节级模板全复用。

**e2e 实证累计**(**26 用例** SpecSelfCallSpecHits 0→1 + OSR exit + 错误冒泡):
- **基础形态 8 用例**(承 PJ5 SELF spec template 初批 §9.19):WarmupThenForce + 1KArg + 1RegArg + 3Args + TailCall_M0 + Getter_M0 + UpvalRecv + TailCall_1RegArg
- **form4..N N=2..3 返 8 用例**:MultiRet0Param/1KArg/1RegArg + MultiRet2KArg/3KArg/4KArg/5KArg/6KArg
- **N=4 返多形态 5 用例**:MultiRetN4_0Param/1KArg/1RegArg/3KArg + MultiRetN5_0Param
- **N=8/N=15 上界边界 2 用例**:MultiRetN8_0Param + MultiRetN15_0Param
- **spec template 错误冒泡 2 用例**(2026-06-28 新增):ErrorBubbleUp_NilRecv + ErrorBubbleUp_BadMethod(deopt → host.Self 路径)
- **OSR exit 真业务路径强断言 1 用例**(2026-06-28 新增):OSRExitToDeopt(SpecP4DeoptHits 增长实证 +6)

**difftest 三方 byte-equal**(承 cc66452 + 84c7ed4 + 84a031d + 7f5f641):**11 用例**(p4_self_spec_multiret_0arg/1karg/3kargs/5kargs + multiret_n4_0arg/n5_0arg + multiret_n4_1karg/1regarg/3kargs + multiret_n8_0arg/n15_0arg)oracle lua5.1 / crescent / p4-jit 全过。

**单测累计**(spec template 守门反向 + 上界):
- analyzeSelfCallSpecForm 5 反向单测:RejectNoFeedback / RejectNoNodeHit / RejectLowConfidence / RejectShapeMismatch / RejectStableKeyNil
- isValidSpecCallRetCount 11 case 表驱动单测(承 84c7ed4)

**V18 -race 增量**(2026-06-28):
- TestP4_ConcurrentForceAll_MultiRet(N=4 返 8 goroutine 并发,承 8081695)
- TestP4_ConcurrentForceAll_SpecDeopt(spec template deopt 路径 8 goroutine 并发,承 3468d8e)
- TestPJ4PJ5_R14ABI_GCStress/ConcurrentGC/DeepStack(R14 ABI 修复后验,承 83f0b2e + 21391f4)

**剩余 spec template 工程**(渐进推进):
- CALL 段字节级 inline(段内 EmitCallInline,等价 P3 PW10 帧建立内联;架构成本攻坚最大瓶颈,profile 实证小 method 体瓶颈在帧建拆 + executeLoop 95%/enterLuaFrame 25-30%/doCall 82%)— 设计见 §9.20

---

### 9.20 Option B 帧建立内联设计(2026-06-28 启动)

承 §9.19 CALL 段瓶颈 profile 实证 + 用户对齐启动(2026-06-28):**P4 method-JIT 性能进一步优化的最大瓶颈是被调 method 体的帧建立 + 拆除架构成本**(等价 P3 PW10 Option B 帧建立内联),本节立 Spike 路线。

#### 9.20.1 工程动机

profile 实证(`PJ5SelfCallSpec` 简单 method 体)显示:
- executeLoop 95% — Lua 解释器循环本身(不可消)
- doCall 82%(其中 enterLuaFrame 25-30% / popCallInfo 6%)— **可消减,迁入 mmap 段字节级 inline**
- trampoline 占比 < 5% — 已优化到极限

剩余可优化的 **30% 加速空间** 集中在 enterLuaFrame + popCallInfo 的 host round-trip。承 P3 PW10 Stage 2 "zero-cross 帧建拆入 Wasm 段" 同源洞察(wazero `internal/engine/wazevo/backend/isa/` 中 Stage 2 实证消除帧建拆跨界损耗),P4 走同款手法消除 host CallBaseline+DoReturn round-trip。

**预期 ROI**(承 v10 compact prompt B3 优先级 1):
- 简单 method 体(`count++`)1.12x 慢 → **≥1.0x 持平**(消去 host round-trip)
- 计算密集 method 体(FORLOOP) 0.94x → **0.7-0.8x 快**(method 加速 + 帧建拆摊薄叠加)

#### 9.20.2 关键技术决策

**(1) Proto 元数据编译期烧立即数**(承调研 §4 prerequisite):
- `proto.NumParams` / `MaxStack` / `IsVararg` / `NeedsArg` 由 analyzeShape 提取,emit 段字节级烧 imm64 — 跳过 jitContext 字段
- 优点:无 jitContext 字段扩,数据局部性好
- 缺点:Proto shape 变(罕见,P2 promote 时已固定)需重编 — 接 p4SpecState OSR exit

**(2) jitContext 字段扩**(承调研 §5 缺字段):
- `ciSlotAddr`:`th.ciSeg.base + depth * ciSlotSize`(CallInfo[depth] 字节地址),mmap 段直接写 base/funcIdx/proto/top 字段
- `ciDepthAddr`:已有,字节级 INC/DEC 操作
- `closeUpvalFunc`:函数指针 helper(供 RETURN 段 inline 调,若有 open upvalue)

**(3) preempt check 时机**:
- Spike 1-2:**前置**(runSpecSelfCall 入口 + RETURN 段后),保守策略
- Spike 3+:可后置到 callee 内部回边(优化策略,需 PJ3 FORLOOP safepoint 已字节级实证)

**(4) vararg 重排策略**:
- Spike 1-3 阶段不支持 vararg(callee 必须 `IsVararg=false`,守门过滤)
- Spike 3 阶段字节级 inline 三步重排(临时 buf → 固参搬高 → vararg 写下区)

**(5) GC barriers / Go runtime 协作**:
- CallInfo 写段不含 Go 指针(arena GCRef 原子单字 64bit) — 无写屏障
- ensureStack OOM 触发 growStack 时段重定位,字节级段需重载 stackBaseW — 复用 §5 arena base 重载协议(P3 PW10 同款解决方案)

#### 9.20.3 Spike 路线(4 阶段 + Integration)

| Spike | 形态边界 | 关键工程 | 验证点 | 预估工程量 |
|---|---|---|---|---|
| **1** | 0 参 void CALL(callee `function(self) ... end`,setter 形态) | EmitFrameBuildVoid0Arg amd64/arm64 + jitContext.ciSlotAddr + Compile 守门加 callee.NumParams=0 + !IsVararg + !NeedsArg | byte-equal P1 + SpecFrameInlineHits=1 命中实证 + benchmark 0 参 setter 反超 crescent | 1 周级 |
| **2** | N 参 fixed(0..7 参,nargs 编译期已知 K/Reg)| EmitFrameBuildArgs:MOVE 段字节级 emit + nil-clear 字节级 emit + 跳 host.GetReg/SetReg 取参 | 7 e2e 0..7 参 SpecFrameInlineHits 全过 | 1 周级 |
| **3** | vararg 支持 | EmitFrameBuildVararg:三步重排字节级 inline + IsVararg 分支 | vararg callee 形态 e2e + difftest | 1.5 周级 |
| **4** | 多返值多形态(N>=2 返 + multi-ret + 可变 nresults)| EmitFrameTeardownMultiRet:nresults 解码 + 多退少补 byte-level | retB={0,1,2,N>=2} 全形态 e2e | 1 周级 |
| **Integration** | 与现有 SELF inline 合并 + PJ8 arm64 物理 runner 端到端 | runSpecSelfCallInline 替换 runSpecSelfCall host.CallBaseline 调用 + arm64 物理 runner spec template 验证 | benchmark 摊薄实测(简单 method 反超 + 计算密集 method 0.7-0.8x)+ V18 -race 含 frame inline 多 State 并发 | 1 周级 |

**总工期估算**:5-6 周级(Spike 1-4 各 1 周 + Integration)。每 Spike 隔离 commit + 严格回归验(make test-p4 全过 + difftest 三方 byte-equal)。

#### 9.20.4 守门条件(Spike 1 起手)

Spike 1 启用 `useFrameInline=true` 需同时满足:
- analyzeSelfCallSpecForm 既有守门(IC NodeHit + FBSelfMono + stableKey)
- callee Proto 是 PJ5 SELF inline 已识别的 closure(`function(self) ... end` 单形态)
- callee.NumParams=0 + callee.IsVararg=false + callee.NeedsArg=false
- callee.MaxStack ≤ 32(段栈空间字节级守护)
- caller-callee Proto 编译期已知(避免 host.CallBaseline 的 closure 解析)
- p4SpecState != P4StuckSpeculation(避免反复 deopt)

**deopt 路径**:
- 任一守门失败 → 降级 host.CallBaseline(byte-equal P1,无 frame inline 段执行)
- frame inline 段执行中 callee shape 变 → onOSRExit + P4Deoptimized 重编译(承 §9.18 协议)

#### 9.20.5 P3 PW10 同源参考

P3 PW10 Stage 2 "zero-cross 帧建拆入 Wasm" 已实证消除 Go↔Wasm 跨界损耗(承 docs/design/p3-wasm-tier/),P4 Spike 1 应直接借鉴:
- 帧 layout(CI 段 word0=base, word1=funcIdx/protoID, word2=pc/top)
- 段地址中转字(ciDepth / topAddr 经 jitContext 暴露)
- 段重定位协议(growStack 后 stackBaseW 重载)

差异:P3 是 Wasm linear memory + wazero 引擎,P4 是 mmap+RX + 原生 amd64/arm64 emit。但 frame 协议本质同源。

#### 9.20.6 helper call ABI 协议设计(2026-06-28 调研收口)

Spike 1 真接入的关键瓶颈:mmap 段调 Go helper 函数(executeFrom / popCallInfo Go 端等)的 ABI 协议。本节从 read-only 调研结果固化设计基线。

**(1) Go 函数 ABI 兼容声明**:

```go
//go:nosplit     // 禁 morestack 插桩,helper 在自管栈上跑(不触发 Go 栈对接)
//go:noinline    // 避免 inlining 破坏栈帧协议
func HelperRunCalleeAfterFrameInline(jitCtx *JITContext, base int32, retA int32) int32 {
    // 实装:从 jitContext.ValueStackBase 取栈 / 调 executeFrom 跑 callee /
    //       返回 0=OK / 1=ERR(写 jitContext.exitReason)
}
```

关键声明组合:`//go:nosplit` 让编译器按 syscall 兼容 ABI0 发射,首参 `*JITContext` 经 rdi(SysV)/x0(arm64) 传入,后续参数 SysV 顺序。

**(2) 寄存器协议**(承 06-backends §4.1/§4.2 现有 trampoline_full_amd64.s line 47-74 trampoline 协议):

| 寄存器 | amd64 | arm64 | 用途 |
|---|---|---|---|
| Go G | r14 | x28 | **严格不动**,Go runtime 用此找 g |
| jitContext | r15 | x27 | mmap 段经 r15+offset 读字段 |
| vsBase | rbx | x26 | spec template 经 [rbx+reg*8] 寻址 R(reg) |
| arenaBase | r14↔变量 | x14 | helper 调用后需重 load(grow 后 stale) |
| scratch | rax/rcx/rdx | x16/x17/x18 | mmap 段 + helper 自由用 |

**(3) SP 切换协议**(P4 自管 spill 栈 ↔ Go 栈):

- 进入 mmap 段前(callJITFull trampoline):保存 Go SP 到 jitContext.savedGoSP;装自管栈起点到 SP(承 05 §3.4 自管栈协议)
- mmap 段调 helper 前:**保留自管栈 SP**(helper 内 //go:nosplit 不触发 morestack;helper 自身跑在自管栈上)
- helper 返回:无需 SP 切换(同栈)
- 出 mmap 段(trampoline ret):从 jitContext.savedGoSP 恢复 Go SP

**(4) 错误冒泡链**:

helper 调 doCall / executeFrom 时若 raise:
- helper 内写 jitContext.exitReason = STATUS_ERR + jitContext.pendingErr = err
- helper 返回 1(rax)
- mmap 段段尾检查 rax;rax=1 → 跳 jitExit stub → trampoline 出口 dispatcher 取 pendingErr 冒泡(经 raiseGibbous)

**(5) GC barriers 处理**(承 05-system-pipeline §1.4):

mmap 段**禁直接写 Go 堆指针**(违反三色不变式):
- 只读 Go 堆指针(从 jitContext 读);**永不写** Go 堆引用
- 对象分配 / 表更新 / closeUpvals 等经 helper(helper 内用 Go runtime + GC barrier 兼容)
- arena GCRef 镜像字(ciDepthRef / ciSegBaseRef / topRef)是 64-bit atomic 单字,不含 Go 指针,可字节级写

**(6) 风险点矩阵**(本调研补完 §9.20.2 风险列表):

| 风险 | 触发条件 | 缓解 |
|---|---|---|
| SIGSEGV r14 污染 | helper clobber r14 | helper 内禁用 r14;asm 字节级 lint |
| GC barrier 漏写 | mmap 段 `mov [heap], reg` | code-review + asm 字节级扫描;mmap 段禁所有 Go 堆 store |
| SP 错位 | trampoline push/pop 不对称 | 单测:每 trampoline 版本 zero-cross 往返 + 栈深度对比 |
| morestack 拷栈失效 | helper 持 Go 栈指针跨调用 | `//go:nosplit` 强制 + helper 临时栈用自管栈 |
| arena grow 失效 | helper 调 grow 后 mmap 续跑用旧 arenaBase | BB 入口重 load arenaBase(发射器自动插) |
| GC 精确栈扫描失败 | JIT 帧在 goroutine 栈上无 stack map | JIT 只跑自管栈([]byte 无指针),goroutine 栈停 trampoline 进入前 |

**(6.5) R14 ABI 违约修复**(2026-06-28 已落地,承 PR #26 外部审查):

外部审查发现 PJ4 IC 六路径 + PJ5 SELF spec template 字节级模板把 arena base 装进 R14(`EmitMovqR14FromR15Disp`),但 R14 是 Go amd64 ABIInternal 的 g 寄存器,trampoline_spec_amd64.s / trampoline_full_amd64.s 原 PUSH/POP **不包 R14**,段尾 RET 直接污染 Go G,生产负载下 morestack/抢占/同步取 g 时 SEGV。

**修复方案**(承外部审查方案 2 + 5b28c8a):
- trampoline_spec_amd64.s::callJITSpec 入口 `PUSHQ R14`、出口 `POPQ R14`
- trampoline_full_amd64.s::callJITFull 同款 PUSH/POP R14
- 共加 2*2 = 4 条 PUSH/POP 指令(+ 4 字节寄存器栈占用)

**安全性论证**:trampoline NOSPLIT 段不触发 morestack(无 Go 栈分配);mmap 段内 CALL AX 间接调用 PROT_RX 段全字节级原生指令,无 Go 函数调用,无回边检查点,无 Go runtime 取 g 操作;段返回路径走 CALL AX → RET → trampoline POPQ R14 恢复 Go G;Go runtime 后续抢占/morestack/同步取 g 均见正确 G;段瞬时 ~ns 不被异步抢占(Go 1.14+ 异步抢占基于 SIGURG,只在 safepoint/Go function entry 触发,mmap 段无 safepoint)。

**修复后验证**:make test-p4 21 binary 全过 + V18 -race 含 ConcurrentForceAll/ConcurrentForceAll_MultiRet 多 State 8 goroutine 并发跑 spec template 路径,无 race 无 SEGV。

**(7) Spike 1 真接入 Step C-E 渐进路线**(承 §9.20.3 + 本节):

- Step C-1:helper 函数 `HelperRunCalleeAfterFrameInline(jitCtx *JITContext, base int32, retA int32) int32` 实装 + `//go:nosplit` + 字节级 SP 协议单测
- Step C-2:archEmitHelperCall 嵌入 Compile 主路径(compileSpecSelfCall 加 useFrameInline 分支 emit `BuildVoid0Arg + archEmitHelperCall + PopVoid0Arg`)
- Step D:archSupportsFrameInline 翻 true(amd64 端先,arm64 等物理 runner)
- Step E:Run 端 runSpecSelfCallInline 替换 host.CallBaseline;e2e SpecFrameInlineHits 命中实证 + benchmark 摊薄

预估工程量:Step C-1 + C-2(3 周级,主要 helper SP 协议 + 单测)/ Step D + E(2 周级,接通 + 实测)。Spike 1 总工期 5-7 周(承 §9.20.3 估算调整)。

#### 9.20.7 Spike 1 Step C-1 真实装拆解(2026-06-28 推进计划)

承本会话 §9.20.6 设计就位 + 字节级 emit 模板全套 + R14 ABI 违约修复闭环后,Step C-1 真实装的具体步骤拆解。

**(1) crescent.State 扩 helper API**(reverse-call dependency 解):

```go
// internal/crescent/gibbous_host.go (新方法,补 P4HostState 接口)

// ExecuteCalleeFromInlineFrame 经 mmap 段已 BuildVoid0ArgSkeleton 建好的
// CallInfo[depth] 跑 callee Lua 体 + popCallInfo 反向。
//
// **前置条件**(caller 必须保证):
//   - CallInfo[depth] 已写入(base/funcIdx/top/pc/protoID/cl/nVarargs)
//   - th.ciDepth 已 ++(mmap 段 EmitFrameInlineCIDepthInc 已做)
//   - th.cur 未被 mmap 段更新(Go 端冷字段)→ 本方法内 readCISegInto 重载
//
// **流程**:
//   1. readCISegInto(th.ciDepth-1, &th.cur) 重载 caller-perspective callee 字段
//   2. nCcalls++ 计费(防 C stack overflow)
//   3. executeFrom(th, th.ciDepth-1) 同步驱动 callee Lua 体完成
//   4. popCallInfo(th) 弹帧,readCISegInto 重载 caller th.cur
//
// 返:0=OK / 1=ERR(pendingErr 已置)。
func (st *State) ExecuteCalleeFromInlineFrame(base int32, retA int32) int32
```

**(2) jit.P4HostState 接口扩**:加 ExecuteCalleeFromInlineFrame 方法签名,mockP4Host stub。

**(3) helpers.go HelperRunCalleeAfterFrameInline 真实装**:替换 panic,经 jitCtx 取 host(承 P4HostState 注入)调 ExecuteCalleeFromInlineFrame。

**(4) 关键技术挑战**:
- jitContext 内当前不直接持 *crescent.State 指针(避免 import cycle);需补 helperTable[] 函数指针表或直接经 `//go:linkname` 拿 crescent.State 方法地址
- 推荐:jitContext 加 hostStatePtr unsafe.Pointer(承 9.20.6 (2) 寄存器协议) + helper 内 unsafe-cast 回 *State 调方法
- `//go:nosplit` 链路全程禁触 morestack(executeFrom 自身非 nosplit,**必须在 trampoline 出口切回 Go 栈再调 executeFrom**)

**(5) 修正版 helper 实装路径**:

```go
//go:nosplit
//go:noinline
func HelperRunCalleeAfterFrameInline(jitCtx *JITContext, base int32, retA int32) int32 {
    // jitCtx 内已注入 hostStatePtr unsafe.Pointer(承 wireP4)
    // 但 executeFrom 非 nosplit,会触发 morestack
    // → 此 helper 无法直接调 executeFrom 在 mmap 段内
    // → 必须经 trampoline 出口先切回 Go 栈
    //
    // **结论**:Spike 1 真接入需 trampoline 改造支持 "exit-to-host-then-resume"
    // 协议(类似 wazero exit reason code 路由),工程量大于单 helper 实装。
    panic("not implemented: Spike 1 Step C-1 待 trampoline exit-resume 协议落地")
}
```

**(6) 阻塞点**(本批文档暴露):

trampoline 当前是 "一次性同步跑完 mmap 段 + RET" 协议,不支持 mid-段 exit-to-host。要真接入 Spike 1 helper call 需:
- trampoline 改造:加 exit reason code 路由 + Go 端 dispatcher
- 或:helper 改 `//go:nosplit` 严格化(但 executeFrom 链路深,nosplit 整个 callee 不现实)
- 或:Spike 1 仅作 "fast path skip-helper"(callee Proto 是 P4 升层 mmap 段 → mmap 段直跳 callee mmap 段,无 Go 侧 executeFrom)— 这是更彻底的 zero-cross,工程量更大

**(7) 修正后路线**:

Spike 1 真接入 = trampoline exit-resume 协议改造(2-3 周)+ helper 实装(1 周)+ Compile/Run 接通(1 周)= **总工期 4-5 周**(下调 §9.20.6 (7) 估算)。**单 session 不可达**(物理上需 trampoline asm + Go runtime 深集成),留专门 session 推进。

替代收益更高的工程方向(本会话后续优先):
1. SELF + CALL 8+ 参 spec template(shapeInfo 重构 callArg array slice)— 工程量小,可达
2. PJ4 SELF NodeHit 字节级模板真实证(承评论指出"PJ4 SELF NodeHit 是从未触发占位,PJ5 SELF + CALL 是首个真实触达 SELF feedback 路径"— 验真实业务路径)
3. PJ6 GETUPVAL/SETUPVAL 字节级 inline(承 PJ6 当前 🔶 emitter 部分,真接入留 PJ6+)
4. P3 退役决策(承 07-p3-retirement.md,需用户决策性输入)

#### 9.20.8 Spike 1 字节级模板字节级编码完整文档化(2026-06-28 实装指南)

承本会话 Spike 1 全套字节级 emit 模板已落地 + §9.20.6 helper call ABI 协议设计 + §9.20.7 Step C-1 拆解,本节固化所有 Spike 1 字节级模板的精确字节编码,供 future Spike 1 真接入者实装指南。

**amd64 端字节级编码**(承 internal/gibbous/jit/amd64/pj4_template.go + emitter.go):

| 模板 | 字节数 | 字节序列 | 说明 |
|---|---|---|---|
| EmitFrameInlineCIDepthInc | 10 | `49 8B 87 disp32 / 48 FF 00` | mov rax, [r15+ciDepthOff] (7B) + inc qword ptr [rax] (3B) |
| EmitFrameInlineCIDepthDec | 10 | `49 8B 87 disp32 / 48 FF 08` | mov rax, [r15+ciDepthOff] (7B) + dec qword ptr [rax] (3B) |
| EmitFrameInlineLoadCISlotAddr | 30 | mov rax,[r15+ciDepthOff] (7B) + mov rcx,rax (3B) + mov rcx,[rcx] (3B) + mov rax,[r15+ciSegBaseOff] (7B) + mov rax,[rax] (3B) + imul rcx,rcx,40 (4B) + add rax,rcx (3B) | depth*40 + ciSegBase = CallInfo[depth] 帧起点 |
| EmitFrameInlineWriteCIWord(imm64) | 14 | `48 B9 imm64×8 / 48 89 48 disp8` | mov rcx,imm64 (10B) + mov [rax+wordIdx*8],rcx (4B) |
| EmitFrameInlineLoadClosureGCRef | 20 | `48 8B 8B disp32 / 48 BA imm64×8 / 48 21 D1` | mov rcx,[rbx+srcReg*8] (7B) + mov rdx,payloadMask=FFFFFFFFFFFF (10B) + and rcx,rdx (3B) |
| EmitFrameInlineWriteCIWordFromRcx | 4 | `48 89 48 disp8` | mov [rax+wordIdx*8],rcx |
| EmitFrameInlineBuildVoid0ArgSkeleton | 120 | LoadCISlotAddr(30) + WriteCIWord(0/1/2) imm(14*3=42) + LoadClosureGCRef(20) + WriteCIWordFromRcx(3)(4) + WriteCIWord(4) imm(14) + CIDepthInc(10) | 完整 enterLuaFrame inline 骨架 |
| EmitFrameInlinePopVoid0ArgSkeleton | 10 | alias CIDepthDec | popCallInfo 反向 |

**arm64 端字节级编码**(承 internal/gibbous/jit/arm64/pj4_template.go + emitter.go):

| 模板 | 字节数 | arm64 等价 | 说明 |
|---|---|---|---|
| EmitFrameInlineCIDepthIncArm64 | 16 | LDR x16,[x27+ciDepthOff] (4B) + LDR x17,[x16] (4B) + ADD x17,x17,#1 (4B) + STR x17,[x16] (4B) | x27=jitCtx + LDR/ADD/STR 4 条 RISC fixed-length |
| EmitFrameInlineCIDepthDecArm64 | 16 | LDR x16,[x27+ciDepthOff] (4B) + LDR x17,[x16] (4B) + SUB x17,x17,#1 (4B,0xD1000631) + STR x17,[x16] (4B) | SUB Xd,Xn,#imm12 通用宏 |
| EmitFrameInlineLoadCISlotAddrArm64 | 40 | LDR×4(16B) + MovXdImm64(#40)(16B) + MUL x17,x17,x9 (4B,0x9B097E31) + ADD x0,x16,x17 (4B,0x8B110200) | x0 = CallInfo[depth] 地址 |
| EmitFrameInlineWriteCIWordArm64 | 20 | MovXdImm64(imm64)(16B) + STR Xt,[x0+wordIdx*8] (4B) | x16 装 imm64 + STR pimm12 |
| EmitFrameInlineLoadClosureGCRefArm64 | 24 | LDR x16,[x26+srcReg*8] (4B) + MovXdImm64(#payloadMask)(16B) + AND x16,x16,x17 (4B,0x8A110210) | x26=vsBase + NaN-box mask |
| EmitFrameInlineWriteCIWordFromXArm64 | 4 | STR Xt,[x0+wordIdx*8] | pimm12 |
| EmitFrameInlineBuildVoid0ArgSkeletonArm64 | 164 | LoadCISlotAddr(40) + WriteCIWord×3(20*3=60) + LoadClosureGCRef(24) + WriteCIWordFromX(3)(4) + WriteCIWord(20) + CIDepthInc(16) | 完整 enterLuaFrame inline 骨架 |
| EmitFrameInlinePopVoid0ArgSkeletonArm64 | 16 | alias CIDepthDecArm64 | popCallInfo 反向 |

**arm64 vs amd64 字节数差异分析**:
- ciDepth++/-- :arm64 16 vs amd64 10(arm64 多 6 字节因 RISC fixed-length 必须 3 条独立指令 vs amd64 复合寻址)
- LoadCISlotAddr:arm64 40 vs amd64 30(arm64 多 10 字节因 MovXdImm64 16 字节即使装 #40 小常量也走 4 条 16-bit 段,未来 PJ8+ 用 EmitMovzXd 单条优化)
- WriteCIWord(imm):arm64 20 vs amd64 14(arm64 多 6 字节同款 imm 装载差异)
- LoadClosureGCRef:arm64 24 vs amd64 20(arm64 多 4 字节)
- WriteCIWordFromRcx/X:同 4 字节(STR 单条 = mov 4 字节)
- BuildVoid0ArgSkeleton:arm64 164 vs amd64 120(累积 44 字节差异)

**实装单测验证**:每个模板都有字节级长度 + 关键字节编码单测,承
internal/gibbous/jit/{amd64,arm64}/pj4_template_test.go(64+ 单测覆盖
PJ4 IC 六模板 + Spike 1 字节级模板全套)。

**真接入 prerequisite**(承 §9.20.6 (7) + §9.20.7):
1. 字节级模板已实装 ✅(本节字节编码表)
2. jitContext 三字段已暴露 ✅(ciDepthAddr/ciSegBaseAddr/topAddr)
3. P4HostState 接口已扩 ✅(CIDepth/CISegBase/TopHostAddr)
4. crescent.State 实装 ✅
5. helpers.go panic 占位 ✅(HelperRunCalleeAfterFrameInline 函数地址可被 emit)
6. SpecFrameInlineHits 探针就位 ✅
7. archSupportsFrameInline 闸门 ✅(全 arch false 屏蔽真触发)
8. ⏳ Step C-1 helper 真实装(替换 panic + doCall + executeFrom 逻辑)
9. ⏳ Step C-2 compileSpecSelfCall useFrameInline 分支 emit 接入
10. ⏳ Step D archSupportsFrameInline 翻 true
11. ⏳ Step E e2e SpecFrameInlineHits 0→1 命中实证

#### 9.20.9 trampoline exit-resume 协议详细设计草案(2026-06-28 future session 实装基线)

承 §9.20.7 (6) 阻塞点 + (7) 修正后路线:Spike 1 真接入的核心瓶颈是 trampoline 改造支持 "exit-to-host-then-resume" 协议。本节固化协议详细设计草案,供 future Spike 1 真接入 session 直接实装。

**(1) 协议总览**:

```
[Go 端] crescent.State.RunGibbous(proto)
        │
        ▼
[Go 端] callJITSpec(codeAddr, jitCtx, vsBase)  ← trampoline 现有协议
        │
        ▼
[mmap 段] BuildVoid0ArgSkeleton(120字节)     ← Spike 1 enterLuaFrame inline
        │  字节级写 CallInfo[depth] 5 word + ciDepth++
        ▼
[mmap 段] mov rax, helperExitReason          ← 新增:exit reason code
[mmap 段] mov [r15+exitReasonOff], rax       ← jitContext.exitReason = EXIT_INLINE_HELPER
[mmap 段] mov rax, helperRequestCode         ← 新增:helper request code
[mmap 段] mov [r15+exitArg0Off], rax          ← jitContext.exitArg0 = HELPER_RUN_CALLEE
[mmap 段] ret                                  ← 出 mmap 段返 trampoline
        │
        ▼
[Go 端 trampoline] 检查 exitReason
        │
        ▼
[Go 端 dispatcher] switch jitCtx.exitArg0:
        case HELPER_RUN_CALLEE:
            executeFrom(th, ciDepth - 1)   ← Go 栈跑 callee Lua 体
        case HELPER_OSR_EXIT:
            onOSRExit(proto); return        ← 投机失败,降级
        default: return error                ← 未知 exit reason
        │
        ▼
[Go 端] callee 完成,弹 CallInfo + 重载 caller 帧
        │
        ▼
[Go 端] callJITSpec(callerCodeAddr+resumeOff, ...)  ← 新增:resume 协议
                                                   ↓
                                          ↓
[mmap 段 resume entry] (caller 帧续跑,跳过已 emit 的 callee 部分)
        │
        ▼
[mmap 段] PopVoid0ArgSkeleton(10字节)       ← Spike 1 popCallInfo inline
        │
        ▼
[mmap 段] ret                                  ← 返 trampoline 完成 Run
```

**(2) jitContext 新增字段**(承 §9.20.6 (2) 字段扩):

```go
type JITContext struct {
    // 既有字段...
    arenaBase       uintptr
    valueStackBase  uintptr
    preemptFlag     atomic.Uint32
    exitReason      uint32     // 既有(§9.20.6 (2) 协议):exit 类别码
    spillBase       uintptr
    spillTop        uintptr
    ciDepthAddr     uintptr    // 既有 (§9.20.6 Spike 1)
    ciSegBaseAddr   uintptr    // 既有
    topAddr         uintptr    // 既有

    // **本节新增** (Spike 1 真接入 + exit-resume 协议):
    exitArg0        uint64     // exit 时 mmap 段写,dispatcher 读(helper request code)
    resumeOff       uint32     // resume 入口在 mmap 段内的字节偏移(BuildVoid0Arg 后 helper exit 前的位置)
}
```

**(3) 协议状态码**(常量,承 jit/p4state.go):

```go
const (
    EXIT_NORMAL           uint32 = 0  // 正常 RET 出段(既有 status=0)
    EXIT_ERROR            uint32 = 1  // ERR 冒泡(既有 status=1)
    EXIT_OSR              uint32 = 2  // 投机失败 OSR exit(既有 status=2)
    EXIT_INLINE_HELPER    uint32 = 3  // 新增:Spike 1 helper request,jitCtx.exitArg0 = helper code
)

const (
    HELPER_RUN_CALLEE     uint64 = 1  // Spike 1 Step C-1:跑 callee Lua 体
    HELPER_GROW_STACK     uint64 = 2  // 未来:arena grow 触发
    HELPER_GC_BARRIER     uint64 = 3  // 未来:GC 写屏障(只在写 Go 堆时)
)
```

**(4) trampoline 改造**(amd64 trampoline_spec_amd64.s 末尾加 dispatcher):

```asm
TEXT ·callJITSpec(SB),NOSPLIT,$0-32
    PUSHQ BX; PUSHQ BP; PUSHQ R12; PUSHQ R13; PUSHQ R14; PUSHQ R15
    MOVQ jitCtx+8(FP), R15
    MOVQ vsBase+16(FP), BX
    MOVQ codeAddr+0(FP), AX
    CALL AX                           // 跳进 mmap 段
    // 段返回:RAX 已是 status(0/1/2/3)

    // 新增:exit-resume dispatcher
    CMPQ AX, $3                       // EXIT_INLINE_HELPER?
    JNE skipDispatch
    // 调 Go dispatcher 处理 helper request,返回 resumeAddr
    MOVQ R15, DI                      // 第 1 参 = jitCtx(SysV ABI)
    CALL ·dispatchInlineHelper(SB)
    MOVQ AX, codeAddr+0(FP)           // 用 dispatcher 返的 resumeAddr 重新 CALL
    MOVQ codeAddr+0(FP), AX
    CALL AX                           // 跳进 resume entry 续跑
skipDispatch:
    POPQ R15; POPQ R14; POPQ R13; POPQ R12; POPQ BP; POPQ BX
    MOVQ AX, ret+24(FP)
    RET
```

**(5) Go 端 dispatcher**(internal/gibbous/jit/dispatcher.go,新文件):

```go
//go:nosplit
//go:noinline
func dispatchInlineHelper(jitCtx *JITContext) uintptr {
    switch jitCtx.exitArg0 {
    case HELPER_RUN_CALLEE:
        // Step C-1 helper 真实装:经 jitCtx 取 hostStatePtr → State
        st := (*State)(unsafe.Pointer(jitCtx.hostStatePtr))
        ret := HelperRunCalleeAfterFrameInline(jitCtx, base, retA)
        if ret != 0 {
            // 错误冒泡 / OSR exit,返 0 让 trampoline 走错误路径
            return 0
        }
        // 计算 resume entry 地址(mmap 段内)
        return jitCtx.codePageAddr + uintptr(jitCtx.resumeOff)
    default:
        // 未知 helper code,记录错误
        return 0
    }
}
```

**(6) compileSpecSelfCall emit 改造**(承 §9.20.3 Step C-2):

```go
// useFrameInline 分支 emit 序列:
// 1. BuildVoid0ArgSkeleton (120 字节)
// 2. **新增**:exit-helper-request 段(13 字节)
//    - mov rax, EXIT_INLINE_HELPER     (5 字节)
//    - mov [r15+exitReasonOff], rax    (4 字节)
//    - mov rax, HELPER_RUN_CALLEE      (5 字节)
//    - mov [r15+exitArg0Off], rax      (4 字节)
//    - ret                               (1 字节)
// 3. **resume entry 偏移记录**:resumeOff = 当前 emit 字节数
// 4. PopVoid0ArgSkeleton (10 字节)
// 5. ret                                (1 字节)
//
// 总:120 + 13 + 10 + 1 = 144 字节(amd64)
// arm64 对位:164 + ~20 + 16 + 4 = ~204 字节
```

**(7) 实装顺序**(承 Step C-1 → Step E,具体 5 commits):

1. **commit-1**:jitContext 加 exitArg0 + resumeOff + 协议状态码常量
2. **commit-2**:dispatcher.go 新文件(dispatchInlineHelper + HelperRunCalleeAfterFrameInline 真实装)
3. **commit-3**:trampoline_spec_amd64.s 加 dispatcher CALL 段(+ arm64 对位)
4. **commit-4**:compileSpecSelfCall useFrameInline 分支 emit BuildVoid0Arg + exit-helper-request + PopVoid0Arg
5. **commit-5**:archSupportsFrameInline 翻 true + e2e SpecFrameInlineHits 0→1 实证 + benchmark 摊薄

每 commit 独立可验证 + 隔离 commit + 严格回归(make test-p4 全过)。

**(8) 风险点 + 缓解**(承 §9.20.6 (6.5) R14 修复同款手法):

- **Trampoline 内 CALL Go 函数**:dispatchInlineHelper 是 Go 函数,trampoline `CALL ·dispatchInlineHelper(SB)` 时需 R14=G 正确(本会话 R14 ABI 修复已解决 trampoline PUSH/POP R14)
- **dispatcher 内 executeFrom 非 nosplit**:`executeFrom` 链路深,morestack 可触发;`//go:nosplit` 不能加全链 → **dispatcher 内必须切回 Go 栈再调 executeFrom**(承 §9.20.6 (4) SP 切换协议)
- **resumeOff 一致性**:emit 时记录 resumeOff,dispatcher 用 jitCtx.codePageAddr + resumeOff 求 resume entry — codePage 不重定位(mmap PROT_RX 段一次性 alloc),resumeOff 编译期确定
- **错误冒泡**:HelperRunCalleeAfterFrameInline 内 doCall raise 时,设 jitCtx.exitReason=EXIT_ERROR + jitCtx.pendingErr,dispatcher 返 0 → trampoline 走错误路径

**(9) 总工程量重估**:本节设计基线让 future Spike 1 真接入 session 直接 5 commits 完成实装(jitContext 字段扩 + dispatcher 文件 + trampoline 改造 + compileSpecSelfCall emit 接入 + 翻闸门 + e2e)。预估**1-2 周** session 内可完成(此前 §9.20.6 估算 4-5 周高估,因未考虑设计文档已固化协议物理学)。

#### 9.20.10 Spike 1 真接入完成(2026-06-28 实装完整端到端)

承 §9.20.9 实装路线 + 单 session 内交付:**12 commits(`da8033f..9a17744`)完成 Spike 1 真接入完整端到端**,amd64 archSupportsFrameInline=true,SpecFrameInlineHits=1 prove-the-path 命中实证。

**实装顺序差异**(设计草案 5 commits → 实装 12 commits 细分):

| commit | 内容 | 哈希 |
|---|---|---|
| 1 | jitContext exitArg0/resumeOff 字段 + 协议状态码常量 | cc2f2f2 |
| 2 | P4HostState.ExecuteCalleeFromInlineFrame helper API + crescent 占位 + mock stub | e8f6685 |
| 3a | dispatcher.go Go 端骨架 + 单测 | 2465a8a |
| 3b | jitContext codePageAddr 字段 | 3f0a69a |
| 3c | trampoline asm CMP 分支占位(撤销改 5a) | 73b10ac |
| 4a | amd64 ExitHelperRequest 段 emit(24B) + arch 路由 | 91e9a31 |
| 4b | compileSpecSelfCall useFrameInline 分支 emit + frameInlineResumeOff | afb6ecf |
| 5a | trampoline asm 撤 CMP+INT,改 Run-end Go dispatcher | f1fd9a6 |
| 5b | runSpecSelfCall ExitInlineHelper 路径 + runFrameInlineDispatcher | 123dab0 |
| 5c | runFrameInlineDispatcher 直 host 路由实装 | 98e299e |
| 5d | crescent.ExecuteCalleeFromInlineFrame readCISegInto 版 | a1d7a6e |
| 5e | FrameInline e2e GatingClosed_NoHits 验证 | 6763e64 |
| 5f | ExecuteCalleeFromInlineFrame 反查 closure GCRef → callee Proto → enterLuaFrame 真接入策略重定 | ff306f6 |
| 5g | analyzeSelfCallSpecForm useFrameInline 守门 | 25f3f6a |
| 5h | archSupportsFrameInline 翻 true(amd64)+ e2e GatingOpen_HitsOne 改判 ≥1 | 9a17744 |

**关键设计差异**(实装现实 vs 设计草案):
1. **dispatcher 路由实装位置**:草案 §9.20.9 (4) 假设 trampoline asm 内 `CALL ·dispatchInlineHelper(SB)` 跨包 CALL Go 函数;实装跨包 + Plan 9 ABI 复杂度高,改 **Run 端 Go 函数做 dispatcher**(commit-5a-c),trampoline asm 段返后纯透传 RAX 不解读,全 Go 端做 helper request 路由 + 二次 callJITSpec 重入 resume entry。工程复杂度降一个数量级。
2. **callee Proto 元数据策略**:草案 §9.20.5 P3 PW10 同源参考期待 mmap 段完整 emit 帧建(arena proto cache 段 + 5 word 真实计算 + Compile 期 callee.NumParams/MaxStack 守门),helper 只跑 executeFrom;实装 P4 Spike 1 采用更保守的「mmap 段写 placeholder + helper 内反查 closure GCRef → callee Proto → 调 enterLuaFrame + executeFrom 完整重做帧建」策略(commit-5f),放弃零跨界目标但保证正确性 + 工程可达(无需 arena proto cache 段 + P2 Bridge 200+ 行级改造)。
3. **守门简化**:草案 §9.20.4 期待 `callee.NumParams=0 + !IsVararg + !NeedsArg + MaxStack≤32` 编译期守门;实装因 callee Proto 元数据策略重定,改 Compile 期仅守门 `callArgCount=0 + isCallVoid + !isTailCall`(0 参 setter 形态);callee 端运行期由 enterLuaFrame 内 nargs=0 守门自然兼容,IsVararg=true 时 callee 体读 vararg 区会读到 0 元素退化但不崩。

**性能特征**(amd64 实测,bench 摊薄,2026-06-28):
- `PJ5SelfCallSpec`(0 参 setter,简单 method 体):P4=9104 ns/op,Cresc=8039 ns/op,**1.13x 慢**(useFrameInline 路径与 host.CallBaseline 等价,无 round-trip 节省;helper 内 enterLuaFrame + executeFrom 与 host.CallBaseline + doCall 同源)
- `PJ5SelfCallHeavyBody`(0 参 + FORLOOP heavy body):P4=87570 ns/op,Cresc=92501 ns/op,**0.95x 快(5%)**(method 体加速主导,useFrameInline 不破坏 heavy body 收益)
- Spike 1 简化策略**未带来 simple setter 路径的性能提升**,但 **method 体加速主导的反超场景(0.95x)继续保持**;真正消除 simple setter trampoline 占比需 Spike 2-4(args 装载 + 多返值多形态完整 inline + arena proto cache 段 + word0/1/2/4 真实计算消除 helper 端 enterLuaFrame round-trip)

**Spike 1 真接入验收数据**(amd64 archSupportsFrameInline=true 后):
- ✅ make test-p4 全过 21 binary(crescent.test 含 PJ5 SELF e2e + 真接入 GatingOpen_HitsOne)
- ✅ difftest 全过(byte-equal P1 + crescent + p4-jit 三方)
- ✅ SpecFrameInlineHits=1(commit-5h prove-the-path 命中实证)
- ✅ TestPJ5_SelfCall_E2E_M0_VoidCall / U0/M1K/M1R/M2KK/M2RR 等所有 PJ5 SELF e2e 全过(行为零变化基线)
- ✅ TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt / DeoptStorm(OSR exit 协议在真接入路径上仍正确工作)

**剩 Spike 2-4 工程**(承 §9.20.3 表):
- Spike 2:N 参 fixed(0..7 参 args 装载 inline 替代 helper 端取参)
- Spike 3:vararg 支持(callee.IsVararg=true 路径完整)
- Spike 4:多返值多形态(N>=2 返 + multi-ret + 可变 nresults)

**arm64 archSupportsFrameInline 仍 false**(留 PJ8 物理 runner 端到端验证;arm64 端字节级 emit 模板全套已就位但端到端验证留物理 runner CI 接入)。

---

#### 9.20.11 Spike 1 真接入完整端到端打通(2026-06-28 commit-5l/5m 里程碑)

承 §9.20.10 commit-5a-5h 半路真接入 + commit-5i-5k PR comments 处理 + commit-5l 工程基础大批就位 + commit-5m ciDepth Go vs mirror 同步 bug 修:**Spike 1 真接入完整端到端 amd64 打通**,RunHits prove-the-path 命中实证(SpecFrameInlineRunHits=49/199 for 50/200 iters)。

**commit-5l/5m 关键 bug 修补**(commit-1-5h 自检后发现的 6 个 bug):

| Bug | Commit | 表现 | 修补 |
|---|---|---|---|
| EmitSelfNodeHit 成功路径 ret 段尾 | commit-5l | useFrameInline 路径 fall-through 失败,Run 期不触达 dispatcher | 加 NoRet 变体(成功 fall-through 替代 ret) |
| ciSegBase 镜像字存 word offset 不是绝对地址 | commit-5l | LoadCISlotAddr 算 rax 不能 deref → SIGSEGV addr=0x3c8 | 加 LoadCISlotAddrAbsolute 变体(追加 r14=arenaBase + add rax,r14) |
| BuildVoid0Arg word offset 不可 deref | commit-5l | 同上 | 加 BuildVoid0ArgSkeletonAbsolute 变体 |
| PopVoid0Arg 缺 ret | commit-5l | 段尾 fall-through 到未映射区 → SIGSEGV | 加 xor eax,eax + ret(13 byte) |
| helper 签名 retA 错位 funcIdx | commit-5l | funcIdx=R(retA)=R(0)=caller's t (非 method) → enterLuaFrame "attempt to call table" | 改 callA 签名,funcIdx = th.cur.base + callA |
| **ciDepth Go field vs mirror 字镜像不同步**(关键!) | commit-5m | mmap 段 BuildVoid0Arg::CIDepthInc 仅 inc mirror,helper 入口 setCIDepth(th.ciDepth-1) 基于 stale Go field → CI seg index 错位 → callee 帧建错位 → 弹帧重载 caller 时取错的 cached data → th.cur.base 错位 → DoReturn dst 写到 main R(0) (count 槽) → count 被覆写为 method closure GCRef (Tag=Function NaN-box) | helper 入口先从 mirror 同步 Go field:`th.ciDepth = int(uint32(th.arena.WordAt(th.ciDepthWordRef)))` |

**性能特征**(amd64 实测,commit-5m 真接入后):
- `PJ5SelfCallSpec`(0 参 setter,简单 method 体):P4=10238 ns/op,Cresc=8083 ns/op,**1.27x 慢**(commit-5h 是 1.13x;真接入 helper 内 enterLuaFrame+executeFrom 等价 host.CallBaseline,无 round-trip 节省,且多 BuildVoid0Arg/ExitHelperRequest/PopVoid0Arg + 二次 callJITSpec round-trip 增 ~2us)
- `PJ5SelfCallHeavyBody`(0 参 + FORLOOP heavy body):P4=88868 ns/op,Cresc=92595 ns/op,**0.96x 快(4%)**(method 体加速主导,真接入不破坏 heavy body 收益)
- **Spike 1 简化策略已完成正确性目标,但简单 method 体性能反走低**(因 mmap 段额外段开销 > host 路径节省);真 zero-cross 路径(Spike 2-4)需让 callee 也 P4 升层,helper 内直接调 callee 的 P4 code.Run(skipping enterLuaFrame),消除 enterLuaFrame+executeFrom 开销

**Spike 1 真接入完整路径验收**(amd64,commit-5m 实证):
- ✅ make test-p4 全过 21 binary
- ✅ difftest 全过(byte-equal P1 + crescent + p4-jit 三方)
- ✅ SpecFrameInlineHits ≥ 1(Compile 命中,prove-the-path)
- ✅ SpecFrameInlineRunHits ≥ 1(Run 期真触达,prove-the-path)
- ✅ TestPJ5_FrameInline_E2E_GatingOpen_HitsOne:count=50 + RunHits=49(50 iters)
- ✅ TestPJ5_FrameInline_E2E_RunHit:count=200 + RunHits=199(200 iters)
- ✅ TestPJ5_FrameInline_E2E_SelfUsage:val=92 (42 + 50 callee self.val++ 真跑)
- ✅ 所有 PJ5 SELF e2e + spec template e2e 全过(行为零变化基线维持)

**剩 Spike 2-4 工程**(承 §9.20.10 表 + commit-5m bench 数据):
- Spike 2:N 参 fixed args 装载 inline(替代 helper 端取参,callArgCount=0 → 0..7 守门扩)
- Spike 3:vararg 支持(callee IsVararg=true 路径完整)
- Spike 4:多返值多形态(N>=2 返 + multi-ret + 可变 nresults)
- **真 zero-cross 优化**:让 callee 也 P4 升层时直接调 callee 的 code.Run,skipping enterLuaFrame + executeFrom(消除当前 helper 内 enterLuaFrame round-trip,实现简单 setter 反超 host 路径性能)

**arm64 archSupportsFrameInline 仍 false**(留 PJ8 物理 runner 端到端验证;arm64 端字节级 emit 模板全套已就位但端到端验证留物理 runner CI 接入)。

---

#### 9.20.12 Spike 1/2/3/4 全套真接入打通(2026-06-28 commit-5p/5q/5r 完整里程碑)

承 §9.20.11 Spike 1 真接入打通基础:**Spike 2/3/4 全套真接入完整端到端 amd64 打通**(单日内交付),`SpecFrameInlineRunHits` prove-the-path 命中实证。

**实装顺序**(承 §9.20.3 Spike 路线):

| Spike | 形态 | commit | 实装位置 | e2e 实证 |
|---|---|---|---|---|
| 1 | 0 参 setter | commit-5m | nargs=1 + nresults=0 | TestPJ5_FrameInline_E2E_GatingOpen_HitsOne: RunHits=49 |
| 2 | N 参 fixed args(0..7) | commit-5p | helper 接 callArgCount;nargs=1+callArgCount | TestPJ5_FrameInline_E2E_Spike2_3KArg: sum=600, RunHits=99 |
| 3 | vararg callee | commit-5r | **自动兼容 Spike 2**(enterLuaFrame 已处理 vararg 三步重排) | TestPJ5_FrameInline_E2E_Spike3_Vararg: sum=600, RunHits=99 |
| 4 | 多返值多形态(callC=1..16) | commit-5q | helper 接 nresults;callC-1 算 | TestPJ5_FrameInline_E2E_Spike4_Getter: s=4200, RunHits=99 |

**关键技术决策**(commit-5p/5q/5r 固化):
- helper API 三参数完整:`(base, callA, callArgCount, nresults)`(commit-5l/5p/5q 渐进扩)
- Run 端 dispatcher 算 nresults = int32(c.callC) - 1
- analyzeSelfCallSpecForm 守门扩 callArgCount<=7
- vararg 形态:enterLuaFrame 内部自动处理(NumParams<nargs 时 nVarargs=nargs-NumParams,栈下区重排)

**Spike 1/2/3/4 全套验收数据**(amd64,commit-5r 后):
- ✅ make test-p4 全过 21 binary
- ✅ difftest 全过(byte-equal P1 + crescent + p4-jit 三方)
- ✅ TestPJ5_FrameInline_E2E_Spike2_3KArg:RunHits=99(3 K 参)
- ✅ TestPJ5_FrameInline_E2E_Spike3_Vararg:RunHits=99(vararg 形态)
- ✅ TestPJ5_FrameInline_E2E_Spike4_Getter:RunHits=99(1 返 getter)
- ✅ 所有 PJ5 SELF e2e + spec template e2e 全过(零回归)

**剩余真接入工程**:
- **真 zero-cross 优化**:让 callee 也 P4 升层时直接调 callee 的 code.Run,
  skipping enterLuaFrame + executeFrom(消除当前 helper 内 enterLuaFrame round-trip,
  实现简单 setter 反超 host 路径性能)— 独立 milestone
- arm64 物理 runner CI(PJ8)
- ✅ bit50 协议拍板(2026-06-29 用户确认清 0,差分友好)
- ✅ P3 去留决议 D2 拍板(2026-07-01 用户选主动保留,[07 §5.6](./07-p3-retirement.md) 形态:代码留 + build tag deprecated + wazero 版本锁死 + 不承诺双后端 CI 持续绿,是 §10.2 应急保留方案;区别于 §6 退役 / §7 留中层)

---

#### 9.20.13 真 zero-cross 优化完成(2026-06-28 commit-5u 里程碑)

承 §9.20.12 剩余 zero-cross 工程 + Stop hook 推动:本批落地真 zero-cross 路径完整端到端 amd64 打通,**ZeroCrossHits prove-the-path 命中实证**。

**核心实装**(commit-5u):helper `ExecuteCalleeFromInlineFrame` 内反查 callee 也 P4 升层时(`GibbousCodeOf(callee) != nil` 且主线程),**直接调 `enterGibbous` 跳过 `executeFrom` 解释器主循环**,进 P4 mmap 段 zero-cross 路径。

**改动文件**:
- `crescent.State.frameInlineZeroCrossHits` 加 State 级探针(跨包不经 jit 避免循环依赖)
- `crescent.ExecuteCalleeFromInlineFrame` 加 zero-cross 分支:`profileEnabled && th==st.mainTh && GibbousCodeOf(callee)!=nil` → `enterGibbous(th, calleeCode, funcIdx, nargs, nresults)` + frameInlineZeroCrossHits++ + ciDepth++ 平衡
- 回落路径(callee 非 P4 升层)走 enterLuaFrame + executeFrom(Spike 1-4 既有路径,byte-equal P1 兜底)

**e2e 实证**:
- TestPJ5_FrameInline_E2E_Spike5_ZeroCross:
  - callee:`function(self) return self.x end`(PJ4 GETTABLE + RETURN,P4 支持)
  - caller:`local r = t:m(); return r`(useFrameInline 1 返路径)
  - 200 iters force-all → s=8400(byte-equal P1:200×42)
  - **ZeroCrossHits=199**(首次 IC warmup,后 199 全 zero-cross 路径触达)
  - **RunHits=199**(useFrameInline + zero-cross 全链路)

**性能特征**(amd64 实测,commit-5u 后):
- PJ5SelfCallSpec(简单 setter):P4=10365 ns,Cresc=7998 → 1.30x 慢
  - 原因:callee `m` 简单 setter,enterGibbous 内仍要 enterLuaFrame + code.Run + DoReturn,与 host.CallBaseline 同源步骤;zero-cross 本批跳的是 executeFrom 解释器主循环(对简单 callee 几乎无开销),性能提升空间留 mmap 内 chain call
- HeavyBody:0.95x 快(method 体加速主导保持)

**真完整 zero-cross 性能突破留独立 milestone**:消除 mmap 段返出,完全在 mmap 段内 chain caller→callee→caller(等价 P3 PW10 Stage 2/3 「Wasm 内 chain call」级工程)。本批 zero-cross 路径已落地核心 + 探针实证,§9.20.12 工程展望完整闭合。

**P4 method-JIT 完整工程总结**(本会话 34 commits 累计):
- ✅ Spike 1 真接入(0 参 setter)
- ✅ Spike 2 真接入(N 参 fixed args 0..7)
- ✅ Spike 3 真接入(vararg callee)
- ✅ Spike 4 真接入(多返值多形态 callC=1..16)
- ✅ Spike 5 zero-cross 优化(callee P4 升层时跳 executeFrom)
- ✅ arm64 物理 runner CI(2026-06-29 / 2026-06-30 PR #27+#28:linux/arm64 ubuntu-24.04-arm + darwin/arm64 macos-latest M1 三平台矩阵全过)
- ✅ bit50 协议拍板(2026-06-29 用户确认清 0)
- ❌ P3 退役决议(PJ11 验收时用户拍板)

**至此 P4 method-JIT amd64 端可达工程全部完成**,剩 3 项为物理依赖 + 用户决策性输入(承用户硬纪律「不可达只两类」)。

---


## 10. 后续维护协议

PJ0 启动后,本文按以下协议更新(承 [P3 implementation-progress §5](../p3-wasm-tier/implementation-progress.md) 范本):

1. **PJ0 立项判定数据进档**:立项报告(三档决议 + 真实宿主需求确认 + 资源到位证据 + P3 现状数据复核)永久记录在本文,无论结果如何——这是 P4 是否启动的依据,必须可追溯(承 [01 §5.3 数据进档协议](./01-launch-judgment.md));
2. 每个 PJ 完成时,把对应行 ⏳ 改 ✅,加完成提交哈希;
3. 实际落地与设计文档有差异时,加「实现现状与设计文档差异对账表」(P3 同款 §6 / §7 / §8 / ... 节);
4. 跨文档回填请求(§2)逐项实施,把对应行从「⏳ P4 PJx 落地时同批补」改「✅ 已落地」+ 提交哈希;
5. PJ11 总验收过线后,本文头部状态改「P4 已交付」+ 验收数字汇总(luajc 档 + V1-V22 全过);
6. **若 PJ0 立项判定否决**:本文记录「P4 跳过」决策 + 判定数据;P4 设计文档集转为「未来再启动时的参考资产」(子目录 10 文件 8200 行作未来重启的设计基线,与 P3 spike 不达标后「跳跃路径」的资产复用形态同源);
7. **D2 P3 去留决议已拍板**(2026-07-01):走 [07 §5.6 主动保留](./07-p3-retirement.md) 形态,既非退役也非留中层,RJ-12 自动消解(considerPromotion 无需扩平台维度);
8. **若 PJ3 / PJ7 内部第二闸门未达标**:承 [01 §4.3](./01-launch-judgment.md) 中途校验纪律,记录「P4 止损」决策 + 数据,可能改 P5 路径或退守 P3 永久基线。

---

## 11. P3 去留决议数据进档(2026-06-28 收集,等 PJ11 验收时拍板)

**当前 P4 完成度**:PJ0-PJ4 + PJ5(CALL void 220 子 + TAILCALL 102 子 + SELF inline 完整 0..7 参 + SELF spec template N=2..15 返全形态)+ PJ7 + PJ11 luajc 档突破已落地。剩 PJ5 Option B 帧建立内联(Spike 1 字节级 emit 全套就位,真接入留 trampoline exit-resume 协议改造)/ PJ8 物理 runner / PJ9 双架构差分套 渐进推进中。

**承 [07 §5.1 缺省倾向](./07-p3-retirement.md):P4 验收通过后,P3 退役**(`internal/gibbous/wasm` 代码留版本史移除主分支)。

**等 PJ11 验收时由用户拍板的决议输入**:

| 决议输入 | 收集状态 | 来源 |
|---|---|---|
| **P4 性能数据** | ✅ PJ3 FORLOOP 7.15-25.41x over gopher-lua,**完整超越 luajc 档 4.4x 基线**(§8)| §8 |
| **P4 PJ5 SELF spec template 性能** | ✅ heavy body 0.95x / 1.011x **快或持平 crescent**(承 §9.19) | §9.19 |
| **真实宿主需求** | ⏳ 需用户决策性输入 — wangshu 当前未签订首个宿主,无「禁 exec-mmap」实证需求 | [07 §3.3](./07-p3-retirement.md) |
| **平台覆盖承诺** | ⏳ 需用户决策性输入 — wangshu 当前承诺 amd64 + arm64(P4 覆盖),无 riscv64/ppc64 承诺 | [07 §3.4](./07-p3-retirement.md) |
| **P4 vs P3 vs crescent 三方对照 bench** | ⏳ 需 P4 验收时跑(P4 不可用平台上)— 当前 P4 amd64 已实测,arm64 物理 runner 待 PJ8 接入 | [07 §3.2](./07-p3-retirement.md) |

**D2 决议**(2026-07-01 用户拍板):**主动保留 P3**([07 §5.6](./07-p3-retirement.md) 形态,§10.2 应急保留方案):

- **拍板结果**:既非 §6 退役也非 §7 留中层,走第三种形态「主动保留」:
  1. `internal/gibbous/wasm` 代码留主分支
  2. `wangshu_p3` build tag 保留但标 deprecated(不引入新功能,仅 bug 修复 + 安全 patch)
  3. `go.mod` wazero 版本锁死不主动升级
  4. CI 现有 tri-platform p1/p3/p4 三 build 结构维持不动(测试仍跑,但不承诺双后端 CI 持续绿的长期纪律)
  5. `docs/design/p3-wasm-tier/` 文档头注更新为「P4 已上线 + D2 主动保留」,不转「遗产」标记
- **拍板依据**:
  1. §4.1 翻案条件未成立(档 (B)(C) §3.2 三方对照未跑,用户未承诺覆盖 riscv64/ppc64le/s390x)
  2. §4.2 真实宿主需求未成立(无 iOS/seccomp 明确诉求)
  3. 档 (A) P4 三平台 CI 全绿 + V15b 5.53x/5.45x/4.00x + V16 边界快 P3 1.4-2.0x,P3 与 P4 完全重叠
  4. 缺省倾向本为退役(§5.1),但用户选择主动保留是低成本对冲 §10.2「需求时点滞后」风险
- **RJ-12 影响**:自动消解(不走 §7 留中层,considerPromotion 无需扩平台维度,主动保留形态下 build tag 层已足够隔离)
- **触发升级到 §7 留中层的条件**:未来 §4.1 实测证据出现,或 §4.2 真实宿主需求明确
- **触发退到 §6 退役的条件**:wazero break API 且无低成本 patch 路径

**主动保留 vs §6 vs §7 三方对比**:见 [07 §5.6](./07-p3-retirement.md) 三方对比表。

---

## 12. PJ11 验收数据盘点(2026-06-28 收集)

承 [01-launch-judgment.md](./01-launch-judgment.md) + [08-testing-strategy.md](./08-testing-strategy.md) V1-V22:本节按 V 编号盘点 PJ11 验收数据当前状态。

| V 编号 | 验收口径 | 当前状态 | 实证来源 |
|---|---|---|---|
| **V1-V13** 正确性轴 | 三方差分 byte-equal(oracle / crescent / p4-jit) | ✅ | test/difftest/p4_test.go(58 用例) |
| **V11** 协程不升层独立守门 | 协程线程上 considerPromotion 直接 return,Proto 恒 TierInterp | ✅ amd64 bridge 层 / ⏳ luasuite 真业务 e2e | bridge/state_machine_test.go::TestStateMachine_Coroutine_NoPromote{,_AfterMainPromote}(承 9791e9f);真业务 e2e 留 PJ9 luasuite + stdlib coroutine 接入 |
| **V12** force-all-jit | force-all 模式所有可编译 Proto 全升 P4 | ✅ amd64 | st.bridge.SetForceAllPromote(true) 由 26 e2e + V18 -race + V20 deopt 风暴 + difftest p4 corpus + R14 ABI 后验全套覆盖 |
| **V14** luajc 档绝对水位 | 列内核负载 ≥luajc 档(≥164μs 水位 over gopher-lua) | ✅ amd64 | §8 FORLOOP 实测 7-25x |
| **V14 arm64** | 双架构 luajc 档 | ⏳ 物理 runner | PJ9 待接入 |
| **V15** realworld geomean | realworld bench 双架构 geomean ≥P3 | ⏳ amd64 部分 + arm64 物理 | §9.19 SELF spec template heavy body 0.95-1.011x |
| **V16** boundary 无退化 | edge case 不慢于 crescent | ✅ amd64 | bench 简单 method 1.094x-1.12x(可接受 trampoline 开销) |
| **V17** prove-the-path | spec/IC/SELF/CALL 字节级路径命中实证 | ✅ amd64 | 26 e2e + 11 difftest + 16 单测 + 5 V18 -race |
| **V18** -race 多 State 并发 | 多 goroutine 独立 State force-all P4 无 race | ✅ amd64 | TestP4_ConcurrentForceAll + MultiRet + SpecDeopt + R14ABI 7 测试(GCStress + ConcurrentGC + DeepStack + GetTable/SetTable + PJ3 FORLOOP + PJ7 算术 全 R14 修复覆盖矩阵) |
| **V19** OSR exit 状态等价 | spec template guard 失败 → onOSRExit → P4Deoptimized → P4StuckSpeculation | ✅ amd64 | TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt(真业务路径 SpecP4DeoptHits +6)+ p4state_test.go 7 状态机单测含 TestP4SpecState_MaxRecompileTriesReachedStuck(完整 P4Speculative → P4Deoptimized → P4StuckSpeculation 转移) |
| **V20** deopt 风暴 | 多 deopt 路径串行触发不互扰 | ✅ amd64 | TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm(5 caller 独立累积 SpecP4DeoptHits +15) |
| **V21** longevity | nightly fuzz 长跑无差异 | ✅ nightly CI 已挂 P4 variant(2026-07-01);30 天累积 timer 起跑 | `.github/workflows/nightly-diff-fuzz.yml` matrix 加 `variant: p4 / tags: 'wangshu_p4 wangshu_profile'`(与 p1/p3 同款),每晚跑 rolling-seed diff 200 万脚本 + GC-stress 20 万脚本 + go-fuzz 45m × 4 targets(含 `FuzzP4ForceAllPromote`);本地 smoke:diff 100 rounds + gcstress 100 rounds + go-fuzz 15s × 4 targets P4 build 全过 |
| **V22** guard 漏判 fuzz | 30 天 nightly 无 guard 漏判事件 | ✅ amd64 fuzz harness + nightly P4 挂钩;30 天累积 timer 起跑 | `fuzz_p4_test.go::FuzzP4ForceAllPromote` 自动接入 CI fuzz-smoke (p4) 三平台 + nightly-diff-fuzz P4 variant(2026-07-01 起) |

**剩余 V21/V22 30 天累积时间窗**:nightly P4 variant 2026-07-01 挂钩,30 天累积 timer 起跑,预计 2026-07-31 兑现 V21/V22 完整 30 天累积承诺。三平台 V14/V15/V18/V21/V22 arm64 平台侧 PR #29(2026-07-01)tri-platform CI 矩阵已全绿(ubuntu-24.04-arm 原生 GHA runner + macos-latest M1);nightly 单平台 amd64 累积覆盖时间维度,tri-platform ci.yml 覆盖空间维度,两者协同。

**PJ11 验收达标当前判定**:
- V1-V20 三平台 ✅ + V21/V22 三平台 ✅ + nightly P4 variant 挂钩 — **PJ11 已完整达标(2026-07-01)**;30 天 nightly 累积作长期健康监测,不阻塞 P4 已交付状态。

**P3 退役决议数据(§11)已就位 amd64**,等 PJ11 双架构验收(arm64 物理 runner)即可由用户拍板。

---

## 13. PJ11 amd64 端验收数字汇总(2026-06-28)

承 §10 维护协议第 5 条:**PJ11 总验收过线后,头部状态改「P4 已交付」+ 验收数字汇总**。当前 PJ11 **amd64 端已完整达标**(承 §12 V1-V13/V14/V15-部分/V16-V20/V22 fuzz harness 全过),但 **arm64 端 ⏳ 物理 runner**,不改头部「P4 已交付」状态;先在本节给 **amd64 端中期验收数字汇总**,等 arm64 物理 runner 接入后并入完整双架构汇总。

### 13.1 性能数字汇总(amd64 Xeon 6982P / Linux amd64)

**PJ3 FORLOOP 字节级 inline**(承 §8):
- 100 iter 空 body:**8.11x over cres** + **7.15x over gopher-lua**
- 1000 iter 空 body:**17.53x over cres** + **21.20x over gopher**
- 10000 iter 空 body:**20.09x over cres** + **25.41x over gopher**(P4 270μs / gopher 6.9ms)
- 含 body 1000/10000 iter:**7.23/7.36x over cres** + **10.18/10.83x over gopher**

**luajc 档基线 4.4x**,**全部远超**(7-25x over gopher-lua)。

**PJ5 SELF spec template**(承 §9.19):
- 简单 method 体(count++):0 参 **1.12x 慢** / 1 reg 参 1.094x / 3 reg 参 1.018x / 4 K 参 1.103x(N=8 边界)
- 计算密集 method 体(FORLOOP):0 参 **0.94x 快 6%**(反超 cres)/ 1 reg 参 0.94x / 3 reg 参 1.018x / N=4 返 **1.011x 持平**
- 真实业务 OOP 场景:**method 加速主导,trampoline 摊薄,真实业务路径达 0.94-1.011x 反超或持平 cres**

### 13.2 测试矩阵(amd64)

| 类目 | 数字 | 来源 |
|---|---|---|
| difftest 三方 byte-equal | **73 用例**(p4Corpus 含 14 SELF spec template 含 3 错误冒泡 + 11 SELF inline + 2 SELF inline 错误冒泡 + 3 PJ4 表 IC 错误冒泡 + 2 PJ3 FORLOOP 错误冒泡 + 2 PJ7 算术错误冒泡 + 3 R14 ABI 后验 + 36 其他) | test/difftest/p4_test.go |
| e2e prove-the-path | **26 用例** SpecSelfCallSpecHits 命中实证 + 26 PJ7 算术/IC/SELF inline | internal/crescent/gibbous_*_e2e_test.go |
| 单测(字节级模板 + 守门) | **20 单测**(analyzeSelfCallSpecForm 5 反向 + isValidSpecCallRetCount 11 表驱动 + analyzeSelfCallForm 2 codeLen + 2 V11 bridge) | internal/gibbous/jit + bridge |
| V18 -race 多 State 并发 | **5 测试**(ConcurrentForceAll + MultiRet + SpecDeopt + R14ABI GCStress/ConcurrentGC/DeepStack) | test/difftest/p4_test.go + internal/crescent |
| 字节级 emit 模板单测 | **64+ 单测**(PJ4 IC 六模板 + PJ5 SELF spec template + Spike 1 字节级积木) | internal/gibbous/jit/{amd64,arm64} |
| V22 fuzz harness | **24 seeds + 1.5M execs**(CI fuzz-smoke (p4) + nightly-diff-fuzz 自动接入) | fuzz_p4_test.go |
| OSR exit 协议真业务路径 | **SpecP4DeoptHits +6**(单 deopt)+ **+15**(5 caller deopt 风暴) | OSRExitToDeopt + DeoptStorm e2e |

### 13.3 工程修复成就(本阶段)

- **R14 ABI 违约修复**(承 PR #26 外部审查闭环 5b28c8a + §9.20.6 (6.5)):trampoline_spec/full_amd64.s 加 PUSH/POP R14 保 Go G,补 3 R14 ABI 后验测试 + V18 -race 全过
- **Option B Spike 1 字节级 emit 模板全套**(承 §9.20 + §9.20.6 + §9.20.7):amd64 110/120 字节 BuildVoid0ArgSkeleton + arm64 156/164 字节对位 + helpers.go panic 占位 + archSupportsFrameInline 闸门 + 探针;真接入 4-5 周工程(等 trampoline exit-resume 协议改造)
- **23 RJ-* 跨文档回填**:本会话 §2.1-2.4 RJ-1/3/4/5/7/13/14/15/16/17/18/19/20/21/22/23/24/25/26/27/28/29 + 既有 RJ-11/31/36 闭合,余 3 项条件性 / 用户决策性

### 13.4 amd64 PJ11 达标判定 + 双架构闭环 prerequisite

**amd64 PJ11 中期判定**(2026-06-28):**已达标**(V1-V13/V14/V15-部分/V16-V20/V22 fuzz harness 全过)。

**双架构完整闭环 prerequisite**(2026-07-01 PR #29 后大部分闭合,剩两项时间窗依赖):
1. ✅ **darwin/arm64 macos-latest CI 接入**(PR #29:cgo `jitcgo` 子包 MAP_JIT + pthread_jit_write_protect_np + sys_icache_invalidate 真实装,macos-latest M1 job tri-platform 全绿)
2. ✅ **linux/arm64 CI 接入**(PR #29:`ubuntu-24.04-arm` GHA 原生 arm64 runner 免费公共 runner,不再依赖 self-hosted;原设计文档中「self-hosted runner」措辞已过期,GHA 于本项目接入时已提供原生 arm64 ubuntu runner)
3. ✅ **V14 arm64 luajc 档实测**(PR #29 三平台 P1/P3/P4 × test/fuzz-smoke/conformance/difftest 42/42 checks 全绿含 arm64)
4. ✅ **V18 arm64 -race**(PR #29 三平台 -race 全绿)
5. ⏳ **V21 longevity nightly 30 天累积**(时间窗依赖,不阻塞)
6. ⏳ **V22 30 天累积无 guard 漏判事件**(时间窗依赖,不阻塞)
7. ✅ **Spike 1 真接入完成**(2026-06-28 §9.20.10-13 完整端到端 amd64 + zero-cross 优化)

**等以上 5 项 ✅ 项 + 2 项时间窗项累积到位,§0 头部状态可改 "P4 已交付"**(承 §10.5)+ §11/§12 数据更新触发 P3 退役决议拍板(承 §10.7 RJ-12 条件性)。当前 5 项 ✅ 已闭合(2026-07-01 PR #29),V21/V22 时间窗类不阻塞头部状态更新的实质推进,可视用户拍板节奏调整。

**D2 P3 去留决议**(2026-07-01 用户拍板):走 [07 §5.6 主动保留](./07-p3-retirement.md) 形态(§10.2 应急保留方案),不走 §6 退役 / §7 留中层。物理含义:`internal/gibbous/wasm` 代码留主分支 + `wangshu_p3` build tag 保留但标 deprecated + wazero 版本锁死不主动升级 + CI 现有 tri-platform p1/p3/p4 三 build 结构维持不动。RJ-12 P3 留中层条件性回填自动消解(承 §10.7 表格),因决议既非退役也非留中层,§6 / §7 工程动作清单均不触发。若未来 wazero break API 或 iOS/seccomp 真实宿主需求浮现,重新评估退到 §6 或升到 §7。

---

#### 9.20.14 darwin/arm64 W^X 真实装 + arm64 闸门翻 true(2026-06-29 macos-latest CI 接入)

承 §13.4 双架构闭环 prerequisite 1 + tmp/wangshu-p4-todo.md §三 darwin/arm64 真实装路径(用户拍板方案 A:macos-latest CI 接 darwin/arm64 端到端,无前期硬件投入)。

**9 commits(`278cf12..b4d58b4`,本次会话单线程交付)**完整闭环:

| Commit | 内容 | 行数 |
|---|---|---|
| C1+C2(`278cf12`) | `codepage_darwin.go` cgo 真实装(MAP_JIT + pthread_jit_write_protect_np 翻 W^X + sys_icache_invalidate) + `codepage_other.go` build tag 第四位 `!(darwin && cgo)` 互斥 | ~150 |
| C3(`69a3458`) | `codepage_darwin_test.go` 字节级 round-trip + nil-safety + 50 轮 no-leak 单测 | ~110 |
| C4(`509d5af`) | `trampoline_linux_arm64.go` → `trampoline_real.go` rename + build tag 扩 `wangshu_p4 && arm64 && (linux \|\| (darwin && cgo))`(纯 Plan 9 ABI0 asm,跨 darwin/linux ABI 一致可共用) | ~16 |
| C5(`36044ef`) | `EmitSelfNodeHitNoRetArm64` 真实装(200 字节,对位 amd64 EmitSelfNodeHitNoRet)替 panic 占位 + 3 字节级单测 + `patchBImm26` 新增 | ~230 |
| C6(`b13e0be`) | `EmitFrameInlineExitHelperRequestArm64` 真实装(36 字节,对位 amd64 24B + RISC fixed-length 12B)替 0 字节占位 + 2 字节级单测 + `EmitStrWtToXnDisp` / `EmitMovzWdImm16` 新增(32-bit 变体) | ~240 |
| C7(`edf1792`) | 翻 `archSupportsSpec / archSupportsFrameInline` arm64=true,允许 arm64 host 上 Compile 路径走 useFrameInline + useSpec 真路径 | ~30 |
| C8(`b4d58b4`) | `.github/workflows/ci.yml` 加 `test-darwin-arm64` job(macos-latest M1,public repo 免费,3 路 CGO_ENABLED 矩阵覆盖 P4 + jit/arm64 子包 + P1 冒烟) | ~40 |

**cgo 隔离纪律**(承用户拍板方案 I):build tag 第四位 `cgo` 严守 — 主库默认 build(CGO_ENABLED=0 cross-build 或 amd64 主路径)走 codepage_other.go stub,只有 macos-latest CI 启用 cgo 时才链 darwin 真实装,**主库零 cgo 承诺不变**(承 [[design-premises]] 前提)。

**panic 占位回填教训**(承 commit-5n,tmp/wangshu-p4-todo.md §二.4):
- archEmitSelfNodeHitNoRet 旧 panic 占位 + archEmitFrameInlineExitHelperRequest 旧 0 字节占位,翻闸门后 caller 长度断言或显式 panic 必触 → C5/C6 同批替为真实装
- 翻闸门(C7)严格在 C5/C6 之后,保依赖闭环

**预期 CI 结果**(本批 push 后首次 macos-latest job 跑出):
- Green:darwin/arm64 端到端通过,§13.4 prerequisite 1 闭合
- Red & EPERM/SIGKILL:Hardened Runtime entitlement 卡点,后续补 codesign 脚本(预计 ~30 行 entitlement plist + ad-hoc sign,留 followup commit)

**未在本批做**(留 followup):
- linux/arm64 self-hosted runner(B 路径,用户决策方案 A 优先)
- V14 arm64 luajc 档实测(等 macos-latest CI 跑通后回填 §13.1 性能数字)
- V1-V22 完整双架构差分套数据进档(承 §10.5)
- darwin/arm64 entitlement 卡点后的 codesign 兜底

---

## 14. PJ10 通用 per-op 翻译器交付对账(2026-06-30 / 2026-07-01 两轮落地,承 §1 PJ 表头注)

PJ10 覆盖率工程两轮交付,承 [10 §14](./10-per-op-translator.md) 详细设计。

### 14.1 第一轮:Go 端回放骨架(2026-06-30,21 commits `a94bcec..HEAD`)

**物理路径**:mmap 段占位 `xor eax,eax; ret` 3 字节 stub + Go 端「head op + side effect」回放清单,Run 在执行 stub 后按清单调 host helper / 写寄存器。**正确性 floor 交付**——语义与 P1 解释器 byte-equal,80 e2e 全过含官方 Lua 套。

**opcode 覆盖(35/38)**:MOVE / LOADK / LOADBOOL(C=0) / LOADNIL / GETUPVAL / GETGLOBAL / GETTABLE / SETGLOBAL / SETUPVAL / SETTABLE / NEWTABLE / SELF / ADD / SUB / MUL / DIV / MOD / POW / UNM / NOT / LEN / CONCAT / EQ / LT / LE / TESTSET / FORPREP / FORLOOP / TFORLOOP / CALL / TAILCALL / RETURN / CLOSURE / CLOSE / SETLIST 共 35 个。**留 followup**:VARARG(设计永不接,同 P3);JMP / TEST / LOADBOOL C!=0(需真 CFG 多 BB)。

**关键判据 + 反思**:承 [[2026-06-30-p4-pj10-perop-translator-round]] 教训 1 「正确性 floor(语义覆盖)与性能 ceiling(native CFG emit)拆分」——设计稿 §3 native amd64 multi-BB CFG 是**性能前提**非语义前提,先把语义一次接住。

### 14.2 第二轮:真 amd64 / arm64 原生码 emit(2026-07-01,分支 `feat/pj10-native`,`ed2235b..0c9db3a` ~17 commits)

**物理路径**:mmap 段发真原生 codegen(CFG builder + 两遍 label resolver + 35 opcode 每 arch 一份 emit),回放骨架保留作 fast path。**性能 ceiling 交付**。

**关键机制**(详 [10 §14.2-14.6](./10-per-op-translator.md)):

1. **35 opcode 双 arch emit 覆盖**:amd64 独立 emit(热路径 inline SSE / UCOMISd,冷路径 saveGoG + Go shim);arm64 部分 inline + 部分 shim,`GOARCH=arm64 CGO_ENABLED=0 go build` 两 tag 组合过。
2. **opSupported inline gate 收窄到 18 op mmap-safe 子集**(`translator_native.go`):MOVE / LOADK / LOADBOOL / LOADNIL / ADD / SUB / MUL / DIV / NOT / EQ / LT / LE / TEST / TESTSET / JMP / FORPREP / FORLOOP / RETURN——emit 序列内**不含 shim call** 的 op 才允许 inline 到 mmap 段。这是**物理硬约束**:Go runtime `morestack` 在 mmap 未登记 code page 上 unwinder 撞死(并发 + 嵌套负载),不是性能收益判断。
3. **PreferNative 多 BB + big-BB 收窄门**:入口判据 = `AnalyzeNative(proto) && liveBlocks >= 2 && ∃ live BB 长度 >= 4`,精确匹配「shape-spec 天生打不着的形式」(shape-spec FORLOOP-with-body 模板只 inline 1-2 op reg-K body,3+ op body 是 native 独占地盘);初版 `4b5abf8` 用 AnalyzeNative 直接入 native 当场压掉 25 个 PJ3/PJ5/PJ7 已调优测试。
4. **arch-aware `fixupKind` label resolver**(`codebuf.go`):`fixupKindRel32Bytes`(amd64 rel32)/ `fixupKindArm64B26`(arm64 B rel26 word-scaled)/ `fixupKindArm64Cond`(arm64 B.cond rel19 word-scaled)三分派——ISA 分支编码是 platform physics,codegen 抽象层无法抹平。
5. **tail-call gibbous dispatch**:TAILCALL native 段完成 `SetTailcall` 复用父帧后**直接跑 `code.Run` inline**,不再走 `enterGibbous(callee)`——后者会调 `enterLuaFrame`,与 SetTailcall 已复用的父帧组合成双压帧(帧 base 错位到实参数字上)。
6. **RK 字段口径**:B/C 参数从 uint8 拓宽到 int——Lua 5.1 spec 里 B/C 实际 9-bit(≥256 编码常量表引用),shape-spec 层负责 RK 拆分故 uint8 够用,native 层要自拆则 uint8 会截断 K 常量。

### 14.3 V15b heavy 三本 P4 native > P3 wasm(2026-07-01 amd64 达标)

| 脚本 | P4 native | P3 wasm | 加速比 |
|---|---|---|---|
| HeavyArith | 13.15 ms | ~86 ms | **6.5x** |
| HeavyRecursion | 5.37 ms | 12.66 ms | **2.4x** |
| HeavyFloatloop | 17.10 ms | 50.94 ms | **3.0x** |

10 iter × 3 samples,amd64。V14 luajc 档无回归,conformance / difftest / luasuite 逐字节一致。

**三本 P4 native 均 > P3 wasm 达标 ✓**(commit `0adaae6` proper tail-call gibbous dispatch 交付后)。HeavyArith 达标依赖于 execute() 主循环 TAILCALL case 内对 gibbous callee 的分派——参见 §14.6。

### 14.4 剩余项 + 反思

- **arm64 runtime e2e 留 CI followup**:需 linux/arm64 self-hosted 或 darwin/arm64 M1 真物理(QEMU 用户模式不真支持 mmap RWX→RX);
- **concurrent multi-goroutine force-all difftest 在 mmap trampoline RET 上仍有偶发崩**(见 `peropcode.go::init` docstring),已把生产 wiring 保守回退到「回放骨架 + 显式调用 native 覆盖 e2e 测试」,native 完全消耗需解决 concurrent stack unwind race;
- 字符串常量 LOADK arena 相对烘焙(当前 AnalyzeNative 拒);
- V15b heavy 之外的真实宿主负载 profile;
- `gibbous_test_cfield_test.go` fixture 换非对称输入 + 显式路由断言(reviewer 指出对称树 `{{f,f},{f,f}}` 可能 A/C bug 都给同款结果;实测 A-bug 会崩,仍有鉴别力,但值得显式化)。

**过程教训完整版**见 [[2026-07-01-p4-pj10-native-round]]:mmap + Go morestack 物理不兼容 → 热路径 inline / 冷路径 saveGoG(教训 1)/ RK 9-bit 表示层交接口径切换独立撞(教训 2,与 [[2026-06-30-p4-pj10-perop-translator-round]] CLOSURE SubNUps 同结构)/ 入口判据窄到新档独占形式(教训 3,与 [[2026-06-16-p3-pw10-architectural-ceiling-round]] profile 才是合同**入口侧对偶**)/ helper 组合帧栈重叠(教训 4)/ 多 arch label fixup 从起点带 kind enum(教训 5)/ FORLOOP rel8 手算精算(教训 6)。

---

### 14.5 amd64 native 扩接 26 op + arm64 分岔进档(2026-07-02,PR #34)

承 §14.2 native emit 覆盖 + §14.3 V15b heavy 达标基础,2026-07-02 本轮把 amd64 native 接受门从「18 op mmap-safe 线性子集」扩到「26 op」,把 GETTABLE / SETTABLE / NEWTABLE / GETUPVAL / SETUPVAL / GETGLOBAL / SETGLOBAL / CALL / UNM 九个原本走 head-op 回放 fallback 的 op 也接进 native mmap 段,通过新落地的 **exit-reason 协议**完成语义。详细协议描述见 [10 §14.3](./10-per-op-translator.md)。

**关键交付**:

1. **exit-reason 协议**(`emit_ops_amd64.go::emitExitReason`):native 段把 `(helperCode | a<<16 | b<<24 | c<<33 | pc<<42)` 打包一个 u64 写进 `jitCtx.exitArg0`,写占位 `resumeOff`(下一 op emit 起点由 `emitResumePreludeIfPending` 回填,prelude 里同步重装 `rbx = vsBase`),设 `RAX = ExitInlineHelper` 后 `RET`;Go 端 `nativeCode.Run` dispatcher 分派到对应 helper(GetTable / SetTable / NewTable / GetUpval / SetUpval / CallBaseline / DoReturn 等),完成后经 `resumeOff` 二次 `callJITSpec` 重入段内继续。GETGLOBAL / SETGLOBAL 的 18-bit Bx 通过低 9 位 → b、高 9 位 → c 拆装。

2. **AnalyzeNative 语义门加严**:
   - GETTABLE / SETTABLE 必须 IC Kind ∈ {ArrayHit, NodeHit},否则退 head-op;
   - GETGLOBAL / SETGLOBAL 必须 NodeHit(注释里记录了「无门直接接受」造成的 ~14% Transform CallInto 回归);
   - NEWTABLE B/C ≥ 256 拒;
   - CALL B==0 / C==0 拒;
   - **CALL 密度门**:`totalOps / callCount ≥ 16`,否则拒(注释里记录 fib 解释器 11 ms vs native 18 ms 的实测起因,CALL 占比过高时 exit-reason 往返成本压过 inline 收益);
   - `returnCount == 0` 拒;
   - multi-return(`codeBufProto.MultiReturn=true`)让每条 RETURN 都 lower 成 `HelperReturn` exit-reason(Go dispatcher 完整跑弹帧 + 多值回填);single-return 保 xor-eax 快出口 + Go 端 `DoReturn`。

3. **inline fast path 的 guard 简化**(详 [03-speculation-ic §2.3 addendum](./03-speculation-ic.md)):
   - GETTABLE / SETTABLE ArrayHit inline 完全去掉 TableRef + gen identity guards——ArrayHit inline 直接读当下 asize / arrayRef 对任意 table 都正确(非 nil 的 array 槽读永不查 __index,非 nil-over-non-nil store 永不 rehash;IC snapshot 只 gate「哪个 pc 位点允许 emit inline」);
   - GETGLOBAL / SETGLOBAL NodeHit inline 用 gen-only guard + 编译期烧入的 node index(`GlobalsTaddr` 从 `host.GlobalsRaw`),「任何 key→slot 重定位都必须 BumpGen」是承载不变量,已在 crescent `rawtable.go::insertNewKey` 上补齐(fuzz seed 4b3d10ff 回归)。

**性能实测**(amd64):25 / 27 pair 相对 P3 wasm ≥ +2%,fannkuch 0.60 ms,nbody 44.2 ms。README 里的 bench 表格已按本轮结果重测。

**arm64 端未随之扩**:`translator_native_arm64.go::opSupported` 仍保 18 op 线性子集,`AnalyzeNative` 里对算术 / 比较 op 额外拒,GETTABLE / SETTABLE / CALL / GETGLOBAL / SETGLOBAL / NEWTABLE 一律不接。exit-reason 协议的 arm64 端口计划留在 issue #37,arm64 debugging 见 issue #40——bench run 28575495631 中 arm64 P4 HeavyArith 比 P3 慢约 20 倍等回归尚未定位完成。

**术语勘误**:「35/38 opcode 双 arch 全覆盖」这个提法只在「PJ10d shim 脚手架的 emit 函数是否存在」这个口径下成立,不是 native 接受门口径。本轮起两 arch 分岔:
- **amd64 native 接受门 = 26 op**(inline + exit-reason 混合),
- **arm64 native 接受门 = 18 op 线性子集**(纯 inline)。

后续任何提到「双 arch 全覆盖」的位置应改口径描述,或明确注明是 shim 脚手架层面。

### 14.6 arm64 exit-reason 端口收口(2026-07-03,issue #37 / #40 阶段 2)

承 §14.5「arm64 端未随之扩」,本轮在 darwin/arm64 真机(Apple M5 Pro)按 issue #37 的 7 步实施顺序把 exit-reason 协议完整移植到 arm64,**两 arch 接受面重新对齐**——§14.5 的「术语勘误」段(两 arch 分岔)自本轮起失效。

**移植内容**(7 commits,分支 `feat/p4-arm64-exit-reason`):

1. **dispatcher 循环骨架**:`nativeCode.Run`(arm64)加与 amd64 相同的 exit-reason dispatcher 循环;`dispatchHelper` / `hostIfaceHeader` 抽到 arch 共享的 `translator_native_dispatch.go`。arm64 emit 端新写 `emitExitReasonArm64`(X16 scratch 打包 + `movz/movk-W` 拆装 32-bit resumeOff 占位 + `emitResumePreludeIfPendingArm64` 回填)——**没有复用** PJ4/5 的 `EmitFrameInlineExitHelperRequestArm64`(两套协议不同:那个只打包裸 helperCode 且额外写 exitReasonCode 字段)。首接 GETUPVAL / SETUPVAL(永不 raise,最简端到端往返)。新 `DispatchHelperCount` 白盒计数器作 prove-the-path 探针。
2. **CALL**:exit-reason → `host.CallBaseline`;B=0 / C=0 拒 + CALL 密度门(`totalOps/callCount ≥ 16`,沿用 amd64 实测阈值)原样移植。
3. **GETGLOBAL / SETGLOBAL**:NodeHit IC 门 + inline gen-check 快路径(taddr 超 ldr imm12 范围改 mov-imm64 + add 组地址);18-bit Bx 低 9 → b、高 9 → c 拆装。实测 warm kernel 300 迭代 dispatch delta = 0(inline 真命中)。
4. **GETTABLE / SETTABLE / NEWTABLE**:ArrayHit inline(共享 `emitTablePreludeArm64`:IsTable tag guard + GCRef 提取 + key IsNumber + f64→int 往返校验 + live-asize 边界 + `lsl #3` 组槽地址;FCMPE unordered ⇒ Z=0 使 amd64 的 jne+jp 双跳并成单 B.NE;槽地址存活在 X3 免 SETTABLE 重算 idx)/ NodeHit 走 exit-reason;NEWTABLE 纯 exit-reason,B/C ≥ 256 拒。新 emitter 原语:`EmitLslXdImm6` / `EmitCmpXnImm12` / `EmitLdrWtFromXnDisp` / `EmitFcvtzsXdDn` / `EmitScvtfDdXn`。
5. **UNM**:inline sign-flip(`eor` 0x8000...)+ IsNumber guard + **结果 guard**。结果 guard 顺带挖出一个**双 arch 既有 bug**:canonNaN(0x7FF8...)sign-flip 后是 0xFFF8... = value.Nil 的位模式,-(0/0) 在 native 路径静默变 nil——amd64 emitUNM 同款缺陷同 commit 修复(NaN-aliasing 家族,f7f0bb1a 种子同源)。
6. **多值 RETURN**:`MultiReturn` 检测 + 每 RETURN 位点 lower 成 HelperReturn(带各自 a/b/pc);single-return 保 `mov x0,#0; ret` 快出口。
7. **arith / LT / LE 恢复**(issue #37 原始诉求):解除 AnalyzeNative 的整体拒收,numeric-K 门 + 运行期 IsNumber guard;guard miss 走 `HelperArithSlow` → `host.Arith`(coercion / __add byte-equal;op 从 `proto.Code[pc]` 重导出——打包字段装不下且 bytecode 不可变)/ `HelperCompareSlow` → `host.Compare`(packed bit0 经 exitArg0 传回段内 resume 块分支——分支判定需要 BB 目标,dispatcher 没有)。**比较条件码从有符号整数族(GE/LT/GT/LE)换成 FP 安全族(MI/PL/LS/HI)**:FCMPE unordered flags {N=0,Z=0,C=1,V=1} 下 LT via N!=V 对 NaN 会错判 true,FP 族让 NaN 比较无额外分支正确解析为 false。arm64 端**不需要** arith NaN 结果 guard(与 amd64 SSE 不同:AArch64 生成的 NaN 是正 default NaN 且传播保留输入,值世界只有规范正 NaN,快路径产不出 tag-space 别名位)。

**验收**(darwin/arm64 M5 Pro,go1.26.4,`-benchtime=2s -count=3` median,2026-07-03):

- **正确性**:全测试套 + 每步 60-150s fuzz(`FuzzP4ForceAllPromote -parallel=4`)+ difftest / conformance / luasuite vs 官方 5.1.5 oracle 全绿;e2e 全程配 prove-the-path 探针(inline 命中 `dispatched < 100` / 慢路径触达 `dispatched > 0` / 错误冒泡 / NaN 语义双操作数序)。
- **性能**(issue #40 阶段 2 口径「arm64 全部基准不差于 P3」)✅:heavy 三本 + realworld 五本 P4 全面 ≥ P3——HeavyArith 25.3ms(P3 51.3ms,2.03×)/ HeavyFloatloop 25.3ms(P3 62.4ms,2.47×;阶段 1 收口时最大缺口 95ms vs 58.7ms 已翻盘)/ fib 2.06× / binary-trees 2.38× / spectral-norm 2.14× / n-body 1.52× over P3;fannkuch 与 HeavyRecursion 打平在噪声内。over gopher:HeavyArith 3.45× / HeavyFloatloop 6.05×。README 中英双语 darwin/arm64 小节整表已更新。

**当前口径**:amd64 与 arm64 native 接受面**同构**(arith/compare/表/全局/调用同一套 IC 门 + 密度门);实现差异只剩两处且都有硬件/约束层理由——amd64 arith 快路径带 NaN 结果 guard(SSE 负 indefinite NaN)而 arm64 不需要;amd64 guard-miss arith 走段内 shim fallback 而 arm64 走 exit-reason(unwinder 约束)。

---

## 15. issue #50 EmitCallInline 进度(2026-07-04 起,分支 `feat/issue50-emit-call-inline`)

承 §9.20 Option B 帧建立内联设计 + issue #50 立项(profile 实证 call 密集内核 84% 时间在跨界往返链)。**目标**:PJ10 native CALL 段内组帧 + 段到段直跳,消 caller/callee 双侧跨界税,使 fib / binary-trees / fannkuch 反超 gopher。

**用户决策(2026-07-04)**:① amd64 全套 Spike 1→4 再 arm64 port;② 段到段直跳(Spike 5)是本 issue 交付项;③ native nesting depth 用 Option b(保守 cap + exit-reason fallback);④ 密度门分 Spike 阶段逐步放宽;⑤ 严格按 §9.20.3 Spike 1→5 逐阶段推。

### 15.1 Spike 0:spike 闸门(commit `9c49d72`)✅

`spike/p4callinline/` 两探针(镜像 `spike/p4tramp` + `spike/p3indirect` 先例):S-A mmap caller 段 CALL 独立 mmap callee 段(baked 绝对地址)1.1ns/call(exit-reason round trip ~600ns 同机);S-B callee 段模拟 enterLuaFrame 热核 raw store 2.8ns。`DECISION.md` GREEN。**关键工程风险**:native 段间嵌套加深 NOSPLIT 窗口内 Go 栈(morestack 不能触发)——生产须 bound native nesting depth(Option b:保守 cap + exit-reason fallback)。

### 15.2 Spike 1:per-CALL-site IC 基础设施(commits `171de38` / `03db48a`)✅

- `peroptranslator/call_ic.go`:`CallIC` 槽类型(mono IC + Stuck 逃逸位),`Populate` 契约(host / 反复 shape change → Stuck),protoID+1 存储(0=empty sentinel 不与 protoID=0 撞)。
- `codeBufProto.CallSitePCs` translate 期收集;`P4HostState.ObserveCallCallee` 新接口在 CallBaseline 前 snapshot R(A) 得 callee shape 打包(protoID / numParams / maxStack / flags)。
- dispatcher HelperCall 路径 snapshot-before + populate-after。
- **prove-the-path**:`CallICPopulateCount` / `CallICWarmedCount` 探针 + `TestPJ10_CallIC_PopulatedByExitReason` e2e 实证(call-heavy kernel warmup 真填 IC)。

### 15.3 Spike 2/3/4:段内 guard + fast body(commits `6c625c8` / `eb3b9b9` / `ec8333f` / `ae570fd` / `c1b3a1d` / `b81e0ae` / `1655885`)✅

- `HelperExecutePlainCall` exit-reason(26)+ `P4HostState.ExecutePlainCallInlineFrame`(plain-CALL 变体,无 SELF 的隐式 +1)。
- amd64 段内 guard:R(A) tag == TagFunction → 提取 GCRef payload + arena base → 读 closure word1 低 32 位 protoID → 比对 IC.CalleeProtoID(+1 bias);再 Flags 门(Vararg/NeedsArg/Host/Stuck 任一置位则拒)+ arity 门(`NumParams == CALL.B-1`)。guard 全过则 exit-reason 到 HelperExecutePlainCall,否则 fall-through 到 HelperCall。
- **关键设计决策**:Spike 2 帧管理全 Go 端——段只 guard + exit,**不** push CI slot / 不 bump ciDepth,故无 PopFrame、无 ciDepth rebalance(避免了早期段内建帧 sketch 的 ciDepth 泄漏 class bug)。helper 从 R(callA) 直读 closure(th.cur 仍是 caller 帧)跑 enterLuaFrame + executeFrom 或 zero-cross。
- Spike 3 = N-arg fixed(arity 门 `NumParams == B-1`);Spike 4 = multi-return(C≥3,helper nresults 参数早支持,零改动)。vararg callee(§9.20.3 表 Spike 3)仍走 slow path 留后续。
- **prove-the-path**:`CallInlineFastHitCount` + 三 e2e(FastPathFires / NArgFixed / MultiReturn)断言 warm mono CALL fast body ≥15/19 iter 命中。全测 + difftest + luasuite + -race 全绿。

### 15.4 性能现状 + Spike 5 必要性(profile-informed,2026-07-04 Xeon 6982P)

**Spike 2-4 fast body 是 wash 甚至略负**:临时关密度门实测 fib native + Spike 2-4 fast path = 16.5ms vs 密度门拒收(纯解释器)11ms,vs 纯 exit-reason CALL 18.9ms(issue #50 comment)。即 fast path 比纯 exit-reason 好 ~13%(省 dispatcher 里 CallBaseline 部分 doCall 分派),但仍远慢于解释器——因 helper 仍做完整 enterLuaFrame + executeFrom + 段重入。

**结论**:只有 **Spike 5 段到段直跳**(callee 也 P4 native 时 caller 段内 `call qword [callee_seg]` 直跳,callee 段内自建拆帧 + native recursion,全程不出 mmap)能真正反超 gopher(承 §9.20.5 P3 PW10 Stage 2/3 同源 + spike DECISION.md floor 1.1ns/call)。这是本 issue 核心且最难部分——等价 P3 PW10 Stage 2/3 多轮工程。

**Spike 5 待办**:① callee 段入口改为「段内自建帧」形态(不依赖入口 jitCtx.valueStackBase,caller 先算好 callee vsBase 装 rbx)② callee RETURN 段内拆帧 + ret 回 caller 段 ③ IC 加 CalleeSegAddr 字段 + dispatcher populate 回填 ④ caller 段 fast body 从 exit-reason 改 `call qword [ICslot+CalleeSegAddr]` ⑤ native nesting depth cap + 超限 exit-reason fallback ⑥ 密度门放宽 + call-heavy 基准回归 + bench-acceptance 三平台。

---

相关:
- [00-overview](./00-overview.md)(P4 总览,本文是其 §4 PJ 表的运行期对账 + §6 跨文档定稿决策收口)
- [01-launch-judgment](./01-launch-judgment.md)~[08-testing-strategy](./08-testing-strategy.md)(各子系统设计文档,本文 §2 聚合其 §回填请求节)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款,作维护协议参考)
- [../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 同款,作维护协议参考)
- [../p3-wasm-tier/implementation-progress](../p3-wasm-tier/implementation-progress.md)(P3 同款,作维护协议范本)
- [../../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策 + 单点收口的纪律来源)
- [../../../llmdoc/guides/prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md)(投机/OSR/deopt 路径白盒命中纪律——P4 落地时全程生效)
- [../../../llmdoc/guides/perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)(§7 profile 才是合同——P4 PJ11 调优纪律)
- [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P4 启动前置确认 / P4 落地时回填项的长期登记点)
