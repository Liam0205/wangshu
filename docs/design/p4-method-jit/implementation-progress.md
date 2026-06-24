# P4 实现进度对账(implementation-progress)

> 状态:**设计阶段,实现未启动**(P4 立项判定通过后启动 PJ0)。详细设计齐备(子目录 10 文件约 7600 行,2026-06-24 单文件 360 行 → 子目录 10 文件扩展轮)。
> P1 全卷(M0-M14)+ P2 PB0-PB7 + 后续优化轮 #1-#4 + P3 PW0-PW10 + VS0-e 全卷已交付(2026-06-16),P4 启动前置就绪;**唯一阻塞**是 P4 立项判定本身(承 [01-launch-judgment §3](./01-launch-judgment.md))。
> 单一事实源:本文是 P4 实现现状与设计文档差异的对账表(对应 [P3 implementation-progress](../p3-wasm-tier/implementation-progress.md) 的角色,但 P4 是设计阶段未实施,本文重在「设计期决策盘点 + 跨文档回填请求收口表 + 实施前置确认 + 后续维护协议」)。
> 设计文档集:见 [00-overview §0](./00-overview.md) 文档地图。
>
> **术语:`P-JIT`(PJ)= P4 实现里程碑编号**(对应 P1 的 M、P2 的 PB、P3 的 PW);PJ0 = 立项判定 + 包骨架,PJ1-PJ7 = amd64 全栈,PJ8-PJ9 = arm64 全栈,PJ10 = luajc 档总验收。

---

## 0. 当前状态

**P4 实现:未启动**(等立项判定)。**设计文档集已齐备**(00-08 + implementation-progress 共 10 文件约 7600 行):

- [00-overview](./00-overview.md)(318 行):文档地图 + PJ 里程碑 + 人月分解 + 跨文档定稿决策速查
- [01-launch-judgment](./01-launch-judgment.md)(810 行):启动闸门 + luajc 档锚点 + 立项决策树
- [02-template-direction](./02-template-direction.md)(710 行):方向裁决 = JSC Baseline 风格 per-function 模板编译
- [03-speculation-ic](./03-speculation-ic.md)(1099 行):IC 反馈消费 + f64 快路径 + guard + 状态机加 deopt 边
- [04-osr-deopt](./04-osr-deopt.md)(1096 行):OSR exit 协议 + 物化 + 再训练
- [05-system-pipeline](./05-system-pipeline.md)(1092 行):四项税自付 + W^X + icache + trampoline + arena base 重载
- [06-backends](./06-backends.md)(908 行):双后端共享骨架 + per-arch + 双架构 CI
- [07-p3-retirement](./07-p3-retirement.md)(837 行):P3 去留决策框架(P4 验收时定)
- [08-testing-strategy](./08-testing-strategy.md)(1074 行):luajc 档 + V1-V22 + deopt 注入 + 双架构差分套

**前置条件检查**:

- ✅ **P1 全卷已交付**(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮 + issue #1-#18 公共面缺口轮系列)
- ✅ **P2 PB0-PB7 + 后续优化轮 #1-#4 全过线**(2026-06-13)
- ✅ **P3 PW0-PW10 + VS0-e 全卷已交付**(2026-06-16,本机 Xeon 6982P 2s×3 count 实测基线:loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x;call 0.52x 是 bench kernel 结构性架构边界)
- ✅ **P4 设计文档完整**(00-08 + implementation-progress 共 10 文件)
- ⏳ **P4 立项判定**:当前需要外部输入(承 [01-launch-judgment §3](./01-launch-judgment.md))——三条硬前置:① 真实宿主负载需求(首个目标宿主规则引擎的列内核确认触及 luajc 档需求)② 资源到位(+1-2 人年人力承诺)③ 设计文档齐备(已就绪)。**P3 现状是 P4 立项的双向信号**:loop 2.95x 显著但仍 ≪ luajc 档 4.4x,call 0.52x 结构性架构边界 ⇒ **若真实宿主负载主要是 loop/mixed 形态可暂不立项,若需 ≥luajc 档列内核能力则立项**(详 [01 §3.2 反向问题 / §3.3 P3 现状对照](./01-launch-judgment.md))。

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PJ | 内容 | 文档 | 完成定义 | 状态 |
|---|---|---|---|---|
| PJ0 | 立项判定 + 包骨架 + build tag 隔离 | [01](./01-launch-judgment.md) + [06 §6.1](./06-backends.md) | 立项判定通过 + `internal/gibbous/jit/{amd64,arm64}` 骨架 + bridge 注入 P4Compiler 后 SupportsAllOpcodes 全 false | ⏳ **立项前** |
| PJ1 | amd64 trampoline + 直线模板(6 opcode) | [05](./05-system-pipeline.md) + [06 §3.1](./06-backends.md) | 直线 Proto 升层后 byte-equal;exec mmap + W^X 翻面工作 | ⏳ |
| PJ2 | amd64 算术 + 比较 + IsNumber×2 guard | [03](./03-speculation-ic.md) + [06 §3.2](./06-backends.md) | 双 number 快路径直发 `mulsd` 等;guard 失败 OSR exit 回解释 | ⏳ |
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | [05 §6.3](./05-system-pipeline.md) + [06 §3.3](./06-backends.md) | 数值 for 编译后 ≥luajc 档单档(**P4 价值首次实证**)| ⏳ |
| PJ4 | amd64 表 IC 模板 + stableShape/Index 直达槽投机 | [03 §6](./03-speculation-ic.md) + [06 §3.4](./06-backends.md) | 单态表 guard + 直达槽跳哈希;形状变化 deopt + 再训练 | ⏳ |
| PJ5 | amd64 CALL/TAILCALL + 跨层互调 + OSR exit 实装 | [04](./04-osr-deopt.md) + [05 §4.3](./05-system-pipeline.md) + [06 §3.5](./06-backends.md) | gibbous-jit 三向分派 + OSR exit 状态等价(V19)| ⏳ |
| PJ6 | amd64 CLOSURE/CLOSE + upvalue | [06 §3.6](./06-backends.md) | 闭包 byte-equal(复用 makeClosure/closeUpvals)| ⏳ |
| PJ7 | amd64 端到端验收 + 性能基准 | [08](./08-testing-strategy.md) | 单架构 V1-V22 全过 + V14 luajc 档 | ⏳ |
| PJ8 | arm64 后端启动 + 渐进交付 | [06](./06-backends.md) | arm64 各 opcode 模板按族落地;`MAP_JIT` + icache flush | ⏳ |
| PJ9 | arm64 端到端验收 + 双架构差分套 | [06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md) | 双架构 V1-V22 全过;Go 1.22/1.24/tip 矩阵 CI 绿 | ⏳ |
| PJ10 | luajc 档验收 + 性能调优 | [01](./01-launch-judgment.md) + [08 §8](./08-testing-strategy.md) | **P4 总验收**:列内核负载 ≥luajc 档(≥164μs 水位 over gopher-lua)| ⏳ |

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

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-5 | [03 §11.1 RB-1](./03-speculation-ic.md) | [P2 05 §5.5 P4 deopt 兜底与重训练](../p2-bridge/05-p3-p4-interface.md):与 [P4 03 §7.3 / §7.4](./03-speculation-ic.md) 字面同源化;明确 P2 接受 RequestRefresh 后 CAS 装新 feedback,P3 旧指针仍可读 | ⏳ PJ4 |
| RJ-6 | [03 §11.1 RB-2](./03-speculation-ic.md) | [P2 02 §9.2 LT/LE 子分流缺口](../p2-bridge/02-ic-feedback.md):[P4 03 §10.4](./03-speculation-ic.md) 是其 P4 视角的对偶兑现,引用本文作 P4 端实证 | ⏳ PJ4 |
| RJ-7 | [03 §11.1 RB-3](./03-speculation-ic.md) | [P2 05 §5.6 P4 不依赖 P2 状态机硬纪律](../p2-bridge/05-p3-p4-interface.md):[P4 03 §8](./03-speculation-ic.md) 直接复刻该节,P2 文档侧加引用「本节具体形态见 P4 §3 §8」| ⏳ PJ4 |
| RJ-8 | [04 §12](./04-osr-deopt.md) | [P2 04 §2.1 TierState 枚举](../p2-bridge/04-try-compile-fallback.md):新增 `TierGibbousJIT` / `TierStuckSpeculation` 两个 tierState 值;与 PB0 现有 `TierInterp/TierGibbous/TierStuck` 兼容(承 [P4 04 §5.2](./04-osr-deopt.md) 状态机)| ⏳ PJ4 / PJ5 |
| RJ-9 | [04 §12](./04-osr-deopt.md) | [P2 01 §2.2 ProfileData 字段](../p2-bridge/01-profiling.md):新增 `ProfileData.deoptCount uint32`——本 Proto 在当前 P4 编译产物上的累计 deopt 次数;每次重编译时 reset | ⏳ PJ4 |
| RJ-10 | [04 §12](./04-osr-deopt.md) | [P2 01 §2.2](../p2-bridge/01-profiling.md):新增 `ProfileData.recompileCount uint8`——本 Proto 在 P4 上的重编译次数;累计不 reset;达 `MaxRecompileTries` 后吸收(承 [P4 04 §5.3](./04-osr-deopt.md))| ⏳ PJ4 |
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

### 2.4 对 ../roadmap.md / ../../llmdoc/architecture/evolution-roadmap.md 的回填请求

| # | 来源 | 内容 | 兑现 PJ |
|---|---|---|---|
| RJ-25 | [01 §9.3](./01-launch-judgment.md) | [../roadmap §4 P4 段](../roadmap.md):「+1-2 人年」估算补「立项前置 = 立项判定([P4 01](./01-launch-judgment.md))」,使 P4 启动节奏与 P3 同款(spike 先于实施)显式化——目前 §4 P3 段有「开工前置 spike」措辞,P4 段无对位措辞 | ⏳ PJ0 |
| RJ-26 | [07 §11.5](./07-p3-retirement.md) | [../roadmap §4 P4 段](../roadmap.md):「Wasm 层退役,或留作可移植中层」措辞补指针指向 [P4 07](./07-p3-retirement.md),使该决策框架的单一事实源显式化 | ⏳ PJ10 |
| RJ-27 | [01 §9.4](./01-launch-judgment.md) | [../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 速查表 P4 行:「前置 spike」列空,补「P4 立项判定(详 [P4 01](./01-launch-judgment.md))」,与 P3 行「wazero call boundary <150ns」对位 | ⏳ PJ0 |
| RJ-28 | [01 §9.4](./01-launch-judgment.md) | [../../llmdoc/architecture/evolution-roadmap §P4 正文段](../../../llmdoc/architecture/evolution-roadmap.md):补「立项判定先于实施(本文承担)」,与 P3 段「开工前置 spike」对位 | ⏳ PJ0 |
| RJ-29 | [01 §9.5(可选)](./01-launch-judgment.md) | [../p2-bridge/00-overview §6 跨文档定稿决策速查](../p2-bridge/00-overview.md):可加一行「P4 立项判定」,但 P2 是 P3/P4 共享前端可能不需要——**主助理裁决是否落入 P2 总览** | ⏳ PJ0(主助理决议) |

### 2.5 P4 子目录内部回填(本期收口)

子文档间互相补章节引用——主助理收尾轮统一兑现,这一节列出已识别的双向引用需求:

| # | 来源 | 内容 | 状态 |
|---|---|---|---|
| RJ-30 | [03 §11.3 RB-6](./03-speculation-ic.md) | [04 §5](./04-osr-deopt.md)(deopt 计数 + TierStuck-speculation)+ [03 §7.2](./03-speculation-ic.md) 给 P4 视角,04 §5 给具体物化协议;两文协同覆盖完整闭环 | ✅ 已对接(双向引用已落) |
| RJ-31 | [03 §11.3 RB-7](./03-speculation-ic.md) | [08](./08-testing-strategy.md)(差分接入「投机错果」最危险 bug 类)+ [03 §3.5 / §9.1](./03-speculation-ic.md) 提名差分主防线 | ✅ 已对接 |
| RJ-32 | [03 §11.3 RB-8](./03-speculation-ic.md) | [06](./06-backends.md)(per-arch 发射函数)+ [03 §2 / §5](./03-speculation-ic.md) 给伪汇编示意,06 引用 03 作 amd64 端母版 | ✅ 已对接 |
| RJ-33 | [03 §11.3 RB-9](./03-speculation-ic.md) | [02 §2.4 / §4.1 / §4.4](./02-template-direction.md)「子集内投机」承诺 + [03 §4.4](./03-speculation-ic.md) 落具体形态 | ✅ 已对接 |
| RJ-34 | [07 §11.4](./07-p3-retirement.md) | [08](./08-testing-strategy.md):验收基准分档加宿主真实负载形态([07 §10.1](./07-p3-retirement.md) 风险缓解);[07 §10.1](./07-p3-retirement.md) 已识别该风险,具体口径展开在 08 | ⏳ PJ10 |
| RJ-35 | [08 §13.4 RB-8](./08-testing-strategy.md) | [03 §3.5](./03-speculation-ic.md)「guard 多判 vs 漏判」语义边界 + [08 §3.4 / §11.1](./08-testing-strategy.md) 字面化,加双向引用 | ✅ 已对接 |
| RJ-36 | [08 §13.4 RB-9](./08-testing-strategy.md) | [04 §5 deopt 风暴 / §6 exit stub](./04-osr-deopt.md):[04 §5.5](./04-osr-deopt.md) 给 deopt 风暴物理学,[08 §5.6 V20](./08-testing-strategy.md) 把它翻成具体测试构造,加双向引用 | ✅ 已对接 |
| RJ-37 | [08 §13.4 RB-10](./08-testing-strategy.md) | [06 §5 双架构测试纪律 / §6 PJ 里程碑](./06-backends.md):[06 §5.2 / §6.2](./06-backends.md) 已立 V-J 编号,[08 §2.5](./08-testing-strategy.md) 落实 V1-V22 与 PJ 的具体映射 | ✅ 已对接 |

**回填请求总数**:**37 项**,分布如下:

| 目标 | 数量 | 兑现节奏 |
|---|---|---|
| P1 现稿(P1 12 / P1 05)| **4 项**(RJ-1~4)| PJ5 / PJ7 |
| P2 现稿(P2 01 / 02 / 04 / 05 / 06)| **10 项**(RJ-5~14)| PJ4 / PJ5 / PJ7 |
| P3 现稿(P3 00 / 01 / 04 / 06 / 08 / implementation-progress)| **10 项**(RJ-15~24)| PJ0 / PJ4 / PJ5 / PJ7 / PJ10 |
| roadmap + evolution-roadmap + P2 00 总览(可选)| **5 项**(RJ-25~29)| PJ0 / PJ10 |
| P4 子目录内部 | **8 项**(RJ-30~37,其中 6 项已落地双向引用)| 已对接 6 项,余 2 项(RJ-34 ⏳ PJ10 / 内部已完整) |

**核心纪律**:承用户裁决「本期只记录不主动改 P1/P2/P3 现稿」,所有非「已对接」项标 ⏳。立项后按 PJ 落地节奏分批兑现。RJ-11 / RJ-29 / RJ-30~33 / RJ-35~37 共 8 项已对接(无需 PJ 落地兑现)。

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
| **编译执行的线程模型** | 倾向同步编译(模板编译微秒级)| [01 §0.4](./01-launch-judgment.md) + 旧 P4 §8 | PJ0 实测;若 cold-start 长尾再考虑后台 goroutine 编译 + 安装屏障 |

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

各子文档 §风险节 + [00-overview §10 风险与未决缺口汇总](./00-overview.md) + [../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 次要缺口,约 10 余条;此处简列指针,均不阻塞 PJ0 启动(立项判定本身是唯一硬阻塞)。

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

## 5. 后续维护协议

PJ0 启动后,本文按以下协议更新(承 [P3 implementation-progress §5](../p3-wasm-tier/implementation-progress.md) 范本):

1. **PJ0 立项判定数据进档**:立项报告(三档决议 + 真实宿主需求确认 + 资源到位证据 + P3 现状数据复核)永久记录在本文,无论结果如何——这是 P4 是否启动的依据,必须可追溯(承 [01 §5.3 数据进档协议](./01-launch-judgment.md));
2. 每个 PJ 完成时,把对应行 ⏳ 改 ✅,加完成提交哈希;
3. 实际落地与设计文档有差异时,加「实现现状与设计文档差异对账表」(P3 同款 §6 / §7 / §8 / ... 节);
4. 跨文档回填请求(§2)逐项实施,把对应行从「⏳ P4 PJx 落地时同批补」改「✅ 已落地」+ 提交哈希;
5. PJ10 总验收过线后,本文头部状态改「P4 已交付」+ 验收数字汇总(luajc 档 + V1-V22 全过);
6. **若 PJ0 立项判定否决**:本文记录「P4 跳过」决策 + 判定数据;P4 设计文档集转为「未来再启动时的参考资产」(子目录 10 文件 7600 行作未来重启的设计基线,与 P3 spike 不达标后「跳跃路径」的资产复用形态同源);
7. **若 PJ10 验收为「P3 留中层」**:RJ-12 触发(承 §2.2),P2 04 considerPromotion 接口扩展加平台维度;否则 RJ-12 自动消解(决策为退役时不触发);
8. **若 PJ3 / PJ7 内部第二闸门未达标**:承 [01 §4.3](./01-launch-judgment.md) 中途校验纪律,记录「P4 止损」决策 + 数据,可能改 P5 路径或退守 P3 永久基线。

---

相关:
- [00-overview](./00-overview.md)(P4 总览,本文是其 §4 PJ 表的运行期对账 + §6 跨文档定稿决策收口)
- [01-launch-judgment](./01-launch-judgment.md)~[08-testing-strategy](./08-testing-strategy.md)(各子系统设计文档,本文 §2 聚合其 §回填请求节)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款,作维护协议参考)
- [../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 同款,作维护协议参考)
- [../p3-wasm-tier/implementation-progress](../p3-wasm-tier/implementation-progress.md)(P3 同款,作维护协议范本)
- [../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策 + 单点收口的纪律来源)
- [../../llmdoc/guides/prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md)(投机/OSR/deopt 路径白盒命中纪律——P4 落地时全程生效)
- [../../llmdoc/guides/perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)(§7 profile 才是合同——P4 PJ10 调优纪律)
- [../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P4 启动前置确认 / P4 落地时回填项的长期登记点)
