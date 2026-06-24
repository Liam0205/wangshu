# P4 §1:启动判定与进入路径——P4 是不是该做、何时做、达标即兑现 LuaJIT 档

> 状态:**架构决策深度**(对齐 [../architecture.md](../architecture.md) §2 状态表:P4 是「架构决策」,比 P2/P3 详细设计粗一档——本文定方向、定边界、给立项判据;**不展开机器码模板细节**,那是 [./02-template-direction.md](./02-template-direction.md) 起的事)。本文是 P4 文档集 [./00-overview.md](./00-overview.md) §0 文档地图所定的「立项闸门」单一事实源——P4 该不该做、何时做、达标即兑现什么、与 P3 的承接,与 P3 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) 在「开工前置裁决」位置上平行。
>
> 上游契约:[../roadmap.md](../roadmap.md)(§1 校准测量——LuaJIT 154μs vs luajc 164μs 仅 6% 是 P4 验收锚点的论据、§2 四项税、§4 P4 阶段定义、§5 五条贯穿原则尤其原则 3「每阶段独立交付不亏」)、[../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md)(前提一 6% 校准 / 前提四 NaN-box 第一天承诺)、[../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P3/P4 同 gibbous tier-1)。
>
> P3 承接面:[../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览,共享前端 / CallInfo / trampoline / 状态机 / 差分轨道——P4 直接继承)、[../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)(P3 闸门同款定位的对位文档:开工前置裁决,§5 决策树「不达标走跳跃路径接 P4」即对应本文 §2.2 之背景)、[../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(P3 PW0-PW10 全卷已收口,实测基线见本文 §3.3)。
>
> 下游协作(同子目录):[./02-template-direction.md](./02-template-direction.md)(方向裁决:per-function 模板编译为何选定,本文 §1 / §4 仅落锚不展开)、[./03-speculation-ic.md](./03-speculation-ic.md)(IC 反馈→f64 快路径 + guard,本文 §4 不涉)、[./04-osr-deopt.md](./04-osr-deopt.md)(deopt 状态机)、[./05-system-pipeline.md](./05-system-pipeline.md)(四项税兑现 + trampoline)、[./06-backends.md](./06-backends.md)(双后端)、[./07-p3-retirement.md](./07-p3-retirement.md)(P3 去留决策矩阵)、[./08-testing-strategy.md](./08-testing-strategy.md)(差分接入)、[./implementation-progress.md](./implementation-progress.md)(进度对账,立项数据归档点)。
>
> **本文定位一句话**:**P4 不是从写模板开始,而是从一次立项判定开始**——校准测量 + P3 实测基线 + 真实宿主负载证据共同决定 P4 是开工、推迟还是跳过。

对应 Go 包:`internal/gibbous/jit`(amd64/arm64 双后端、OSR exit;与 `internal/gibbous/wasm` 同 tier 并列,详见 [../architecture.md](../architecture.md) §1)。

---

## 0. 定位:启动判定先于一切机器码工作

### 0.1 为什么启动判定先于设计展开

P4 的人力估算是 **+1-2 人年**——这是项目从人月级(P1/P2/P3)跨入人年级的第一站([../roadmap.md](../roadmap.md) §4)。在这个量级下,「先判定再设计」不是流程洁癖,而是 [../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付价值,任何闸门处停下都不亏」对 P4 的具体兑付:**立项判定本身即是一次产出**——即便判定结果是「不做」或「推迟」,该判定凭据(校准测量 + P3 实测基线 + 宿主负载证据)写入档案,后续阶段(P5 立项、P3 去留决议、宿主侧改造)都可援引。

理由有三:

1. **P4 是期权而非计划**。流水线上 P1/P2/P3 都有近似确定的人月预算与验收门槛;P4 不同,它的「该不该做」依赖于**之前各阶段实际兑现的收益**(尤其 P3 的实测基线是否已逼近近期目标)。把 P4 视作期权意味着:**条件未满足时不行权**——P3 现状 + 宿主真实需求未到位时强行立项,等于把 1-2 人年押在不必要的工程野心上。

2. **决策不可逆,大方向无回头**。一旦立项,P4 的核心实现路径(per-function 模板编译 + IC 投机 + amd64/arm64 双后端 + OSR exit)就锁定;实施过半再「不做了」等于 6+ 人月沉没成本。同款不可逆纪律已在 P3 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.2 立过——P3 PW0 spike 通过即开工,中途回头违反原则 3。本文 §5 把这条不可逆纪律对位到 P4。

3. **立项前先证负载证据,而非工程野心**(承 [../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同款纪律)。P4 的全部价值落在「列内核负载下兑现 luajc 档 / 逼近 LuaJIT 档」一句承诺上。但「列内核负载是否首个目标宿主的真实热路径形态」不是设计稿能凭空拍板的——它必须有来自宿主端的实证。本文 §3.1 把这一条写成立项硬前置,§3.4 把「无证据」明确列为「跳过 P4」的合法决策。

### 0.2 决策不可逆(立项后大方向无回头)

立项后退出 = 推翻已写的代码。P4 的实施路径锁定的是**结构性选择**——这些选择一旦写进 `internal/gibbous/jit`,撤回意味着把同等量级的代码删掉:

- 模板编译 vs 优化编译:per-function 模板编译已锁定([./02-template-direction.md](./02-template-direction.md) §1.3 候选谱系否决论证),实施过半改 IR-based 优化编译器 = 重写。
- 双后端 vs 单后端:amd64 + arm64 双后端共享骨架已锁定([./06-backends.md](./06-backends.md) §1),只做单架构 = 撤一半发射函数。
- OSR exit 函数级 vs 跨指令 snapshot:函数级 OSR exit 已锁定([./04-osr-deopt.md](./04-osr-deopt.md) §3),改 snapshot 机器 = 重写 deopt。

这三项都是 P4 设计核心,任何一项中途翻案都触发实质重写。**立项前的判定权与立项后的实施权是分离的**:本文 §5.2 把这条分工写成纪律。

### 0.3 战略价值与判定的双向性(达标即兑现 / 不达标停在 P3 仍是闸门胜利)

承 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.3「闸门双向性」同源逻辑——P3 spike 不通过本身仍是 [../roadmap.md](../roadmap.md) §5 原则 3 的胜利(数据建立在实测而非乐观估计上),P4 立项判定也是双向的:

- **若立项判定通过**:P4 兑现 [../roadmap.md](../roadmap.md) §0「逼近 LuaJIT 档」近期目标(达标 = 列内核负载达 luajc 档,而 LuaJIT 仅比 luajc 快 6%),也是首天值表示承诺([../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提四 NaN-box 选型)兑现的最大现金流——值表示与 deopt 物化的红利在此变成可量化收益(详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.2)。
- **若立项判定不通过**:停在 P3,P3 PW0-PW10 已交付的子里程碑(包括架构边界文档化的 ④-ii 留口,详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10)继续作为 wangshu 的当前面;P4 立项凭据写入档案,后续条件成熟(宿主负载形态变化、P3 收益不够)时重新评估——这是「期权未行权」而非「项目失败」。

「达标即兑现 / 不达标停在 P3 仍是闸门胜利」是 [../roadmap.md](../roadmap.md) §5 原则 3 在 P4 立项点的字面体现。

### 0.4 与 P1/P2/P3 落地状态的关系

本文写于 P3 PW0-PW10 全卷收口之后(详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0 与本文 §3.3),这一现实强力影响 P4 立项判定的形态:

- **P1 已交付(M0-M14 + 全卷)**:解释器作为 deopt 着陆点(原则 1 「解释器永不退役」)已是物理事实,P4 OSR exit 直接消费 P1 的 CallInfo / 值栈协议(详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1)。
- **P2 已交付(PB0-PB7 + 后续优化轮 #1-#4)**:P4 共享 P2 前端(热度 / TypeFeedback / F1-F7 闸门 / TierState),不重写决策机([./02-template-direction.md](./02-template-direction.md) §0.2)。
- **P3 已交付(PW0-PW10 全卷)**:历史曾把「P3 被 spike 否决 ⇒ 跳跃路径」作为假设语气保留;**本文写实——P3 spike 已通过,跳跃路径不复存在**(§2.3)。这意味着本文 §2 的二路只剩常规一路,P4 立项时只面对「P3 之后」一种形态。

这一点必须明写:旧单文件的「跳跃路径」是 P3 不存在时的备用逻辑,P3 已交付即解除。本文不复述旧单文件已死的备用路径,§2.2 把它降为历史记录。

### 0.5 立项判定的产出物

本文作为「立项闸门」单一事实源,产出物形态(承 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.2 闸门产出物纪律):

| 产出物 | 内容 | 落点 |
|---|---|---|
| **立项判据**(本文 §3) | 三档前置 + §3.2 反向问题 + §3.3 P3 现状 + §3.4 三档策略 | 本文是单一事实源 |
| **决策记录** | 立项时点的具体档位决议(全启 / 部分前置 / 跳过)+ 用户裁决凭据 | [./implementation-progress.md](./implementation-progress.md)(立项时建立) |
| **数据档案** | §3.3 P3 实测基线 + 宿主侧端到端数据 + 第二闸门数据(若立项) + 验收数据(若立项) | [./implementation-progress.md](./implementation-progress.md) |
| **回填请求** | 本文 §9 列出对 P1/P2/P3 现稿的回填请求 | 主助理收口在 [./implementation-progress.md](./implementation-progress.md) |

任何「P4 该不该做」的讨论,必须援引本文具体节号——没有具体节号支撑的立项主张,违反 [../../llmdoc/guides/multi-doc-drafting.md](../../llmdoc/guides/multi-doc-drafting.md) 「自包含契约」纪律。

---

## 1. 立项前提:luajc 档的精确含义

### 1.1 校准测量 1 三档绝对值

[../roadmap.md](../roadmap.md) §1 校准测量 1(Horner 5 次多项式,1000 items,同机同日 A/B,16 核 Intel Xeon 6982P-C,per-item 粒度调用):

| 嵌入栈 | 绝对值 | 相对 gopher-lua | 技术 |
|---|---|---|---|
| gopher-lua(Go) | 729μs | 1x | 纯解释,interface 装箱,switch dispatch |
| LuaJ-luac(Java) | 259μs | ≈2.8x | JVM 解释器,本体被 C2 编译热 |
| **LuaJ-luajc(Java)** | **164μs** | **≈4.4x** | **Lua→JVM bytecode,C2 全套优化** |
| LuaJIT(C++) | 154μs | ≈4.7x | trace JIT,NaN-boxing |

这是 P4 立项判定的**硬数字基底**——三档之间的差距决定了「兑现 luajc 档」是 P4 的工程目标。

### 1.2 P4 验收 = 列内核负载 ≥ 164μs 那一档

[../roadmap.md](../roadmap.md) §4「P4 验收 = 列内核负载 ≥ LuaJ-luajc 档」的精确含义:

> **同等工作量(列内核形状,Horner 多项式量级或同档)基准上,gibbous-jit 的 ns/op 不劣于 luajc 的 ns/op。**

字面上是「不劣于」,工程上等价于「≥4.4x over gopher-lua」(以同基准 gopher-lua 为基线)。坐标系警告(承 [../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md) 速查表):
- **流水线图倍率「trace 收益 ~70%」是另一坐标系**:它说的是 P4 拿到 trace JIT 理论收益的约七成,剩余 ~30% 是 P5 的边际,与 luajc 档不直接对应。
- **正文验收门槛「≥ luajc 档」是 P4 立项判定的真正锚**——本文 §4 展开。

### 1.3 LuaJIT 仅比 luajc 快 6%——「逼近 LuaJIT 档」的近期含义在 P4 兑现

[../roadmap.md](../roadmap.md) §1 关键事实:**真 LuaJIT 只比 luajc 快 6%**(154 vs 164μs)。两者后端架构差距巨大(LuaJIT 是 trace JIT + 寄存器分配 + 类型投机;luajc 是 Lua→JVM bytecode + JVM C2 优化),per-item 形态下两者数据仅差 6%——边界跨越 + 值装箱已是绝对主导成本(承 [../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提一 6% 校准)。

这一事实有两层含义,共同锚定 P4 立项判定:

1. **达标即「逼近 LuaJIT 档」**——P4 兑现 luajc 档 = 兑现 LuaJIT 档的 ~94%,[../roadmap.md](../roadmap.md) §0「项目近期目标」的字面体现。这是 P4 立项的战略价值(§0.3),也是「P4 不必复刻 trace JIT 的护城河」的物理论据。

2. **6% 差距是 P5 的边际,不是 P4 的边界**——这 6% 留给 P5 trace JIT(详 [../p5-trace-jit.md](../p5-trace-jit.md) §0),而不是把 P4 的目标拉到 154μs。把 P4 验收锚在 luajc 档,是「实现成本与收益曲线严重凸性」([./02-template-direction.md](./02-template-direction.md) §1.3)在阶段切分上的对位:**P4 用最少人年拿走最大一块,剩余边际是 P5 的开放式投资**。

### 1.4 与 P5「~30% 剩余空间」的坐标系

P5 启动条件 = 「P4 收益不够」(详 [../p5-trace-jit.md](../p5-trace-jit.md) §0 / [../roadmap.md](../roadmap.md) §4)。坐标系切分:

| 阶段 | 验收锚 | 留给下一阶段的边际 |
|---|---|---|
| **P4** | 列内核负载 ≥ luajc 档(164μs / 4.4x over gopher-lua) | 6% 到 LuaJIT 档 + 流水线图意义上「trace 收益 ~30%」 |
| **P5** | 列内核负载 10-30x over gopher-lua(开放式) | 不存在(P5 是流水线终点) |

这道坐标系说明 P4 验收数据是 P5 立项的输入(详 §6.2):**P5 立项判定的核心输入,就是 P4 兑现的实测数字与剩余空间**。本文承担 P4 立项闸门,不承担 P5 立项判定;P5 立项详 [../p5-trace-jit.md](../p5-trace-jit.md) §1。

### 1.5 校准测量 2 的对偶面:边界主导稀释

承 [../roadmap.md](../roadmap.md) §1 校准测量 2(某生产规则引擎 luajc 启用前后):
- 隔离脚本级 -37%(luajc 显著加速);
- 宿主端到端 benchmark 前后对照全部落在 ±5-7% 噪声带内(端到端不可见)。

这一测量是 P4 立项判定的**反面警示**:**VM 层加速若被宿主侧边界成本吃光,P4 1-2 人年投入即便兑现 luajc 档,业务价值也可能为零**——这是 §3.1 前置 2「真实宿主负载证据」与 §7.3「真实宿主需求待外部确认」风险的物理来源。

校准测量 2 同时是 [../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提一「负载形状必须是列内核」的实证基础——若宿主侧不以列内核形态调用 VM,任何 VM 内本体加速(P3/P4/P5)都被边界稀释看不见。本节把这层逻辑对位到 P4 立项:**若首个目标宿主侧改造为列内核形态尚未完成,P4 立项的 ROI 同样面临稀释风险**。

### 1.6 prior art:模板编译档位的工程实测

[../roadmap.md](../roadmap.md) §7 列名 V8 Sparkplug 与 JSC Baseline JIT 作为 P4 标准参照(详 [./02-template-direction.md](./02-template-direction.md) §1.2 候选谱系对照)。从立项判定层引用这些 prior art:

| 引擎 | 模板编译层 | 对应优化档位 | 与 P4 验收锚的关系 |
|---|---|---|---|
| **JSC** | Baseline JIT | DFG → FTL | Baseline 把 dispatch + IC 投机做到位,DFG/FTL 是优化层(P5 对位) |
| **V8** | Sparkplug | Maglev → TurboFan | Sparkplug 论文式自我定位「a compiler dispensing with the interpreter dispatch」与 P4 同形态 |
| **望舒** | gibbous/jit (P4 本文) | fullmoon (P5) | 同款分工:模板层兑现 luajc 档,优化层留 P5 兑现 LuaJIT 档剩余 6% |

prior art 给 P4 立项判定的两条工程参考:
- **模板编译能跑到的档位是「dispatch 消除 + IC 投机」之和**——JSC Baseline / V8 Sparkplug 实测都在这一档位附近,与 P4 luajc 档锚定吻合。
- **不必复刻优化编译器即可拿到大部分收益**——这是 [./02-template-direction.md](./02-template-direction.md) §1.3 凸性曲线论证的工程实证,也是 P4 「以最少人年拿走最大一块」立项的 prior art 支撑。

立项判定层不展开各引擎技术细节(那是 [./02-template-direction.md](./02-template-direction.md) §1.2 表格的事),只引用其工程档位作为 P4 立项的可信度参考。

---

## 2. 进入路径:常规 / 跳跃二路

### 2.1 常规路径:P3 之后

「常规路径」是 P3 已交付的形态下 P4 的进入方式:**P3 已上线但收益不够 ⇒ 立项 P4 兑现 luajc 档**。

具体形态:

```
[P3 已交付:PW0-PW10 全卷,分层骨架 + wazero 后端 + 全 38 opcode 翻译]
          │
          ▼
[判定收益是否兑现近期目标]
          │
   ┌──────┴──────┐
   ▼              ▼
收益已够          收益不够,且首个目标宿主真实需求待 P4 兑现
(P3 收口)       │
   │              ▼
推迟 P4         立项 P4(P3 升级为「分层骨架已跑通的基线」)
                  │
                  ▼
            P4 = 纯后端替换(wazero 发射 → 原生发射)
            P3 设计资产 100% 继承(详 §2.4)
```

「P4 = 纯后端替换」是常规路径的关键:**分层机器(升降层、trampoline、差分接入、可编译性闸门、TypeFeedback 供料管线、状态机)在 P3 已跑通**,P4 不重做这些骨架,只把热函数的发射后端从 wazero 换成原生 codegen——这是 [../roadmap.md](../roadmap.md) §4「继承 P3 的全部分层结构,只换发射后端」字面承诺的工程体现。

常规路径下 P4 的人年估算 +1-2 人年([../roadmap.md](../roadmap.md) §4)是「只换后端」的预算;若需要重建分层骨架(跳跃路径),预算上浮,见 §2.2。

### 2.2 跳跃路径:P3 被 spike 否决(已不复存在)

「跳跃路径」是历史备用形态——P3 PW0 spike 不达标(wazero call boundary ≥150ns)⇒ 跳过 P3 直接做 P4。这一路径的设计由 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §5 决策树承接,P4 接管「首次跑通分层机器」的全部工作(原 P3 的战略价值——升层 / 降层 / fallback 骨架——移入 P4),人年估算从「仅换后端」上浮到「分层骨架 + 机器码后端同步啃」量级。

**跳跃路径的当前真实形态——已不复存在**:

P3 PW0 spike 早已通过(wazero call boundary 实测 36.7ns,远低于 150ns 阈值,详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0.1 spike 报告),P3 PW1-PW10 全卷已交付。这意味着:

- **跳跃路径在事实层面被消解**——P3 已存在,不存在「跳过 P3 直接做 P4」的现实选项。
- **跳跃路径设计资产仍写在 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §5.4 表中**——若未来 P3 出现根本性退役决议(详 [./07-p3-retirement.md](./07-p3-retirement.md)),那是另一个语境;当前 P4 立项判定不需考虑跳跃路径。
- **本节保留为历史记录**——历史文档曾以「P3 已上线但收益不够」的设计期假设语气描述 P3 状态,本节明确此为已落地现实而非假设。

### 2.3 当前真实形态:常规路径生效

**P4 立项判定面对的唯一进入路径就是 §2.1 的常规路径**——P3 PW0-PW10 已交付,后端的 P4 立项决策在以下三档之间(详 §3.4):

| 档 | 立项形态 | 触发条件 |
|---|---|---|
| **全启** | 立项 P4 (1-2 人年完整投入,纯后端替换) | P3 实测基线远未到 luajc 档 + 首个宿主真实负载待 P4 兑现 + 资源到位 |
| **部分前置** | 仅做 P4 amd64 单架构 + 仅算术投机(本文 §4.3 内部第二闸门提的 minimal P4) | P3 实测基线接近但未到 luajc 档 + 想用最小 P4 兑现概念验证 |
| **跳过(推迟)** | P4 不做,留 P5 直接接(若有需要) | P3 收益已够 + 无明显宿主负载需求 + 资源紧张 |

档与档的边界由 §3.3 P3 现状 + §3.1 硬前置共同确定;具体决策见 §3.4。

### 2.4 路径下「继承 P3」具体清单

常规路径生效条件下,P4 直接继承 P3 设计资产的清单(对应 [../roadmap.md](../roadmap.md) §4「继承 P3 的全部分层结构」的具体落点):

| 继承项 | P3 落点(子文档) | P4 消费方式 |
|---|---|---|
| **共享前端**(P2 决策机) | [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(P2 全卷)、[../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md) §1(P3/P4 同 tier 边界) | P4 复用 P2 的热度 / TypeFeedback / F1-F7 闸门 / TierState,不重写决策机([./02-template-direction.md](./02-template-direction.md) §0.2) |
| **CallInfo 协议** | [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.2、[../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §1 bit50 | P4 跨层 trampoline 同款使用 CallInfo,bit50 标识 gibbous 帧——P4 接管同一标识,trampoline 切到原生码后形态不变([./04-osr-deopt.md](./04-osr-deopt.md) §3.1 OSR 着陆面消费 CallInfo) |
| **trampoline 入口签名** | [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §2(`(func $proto_N (param $base i32) (result i32))`) | P4 原生码入口同款只接 `base` 与 status,值/参数从共见 arena 自取——签名跨后端不变,详 [./05-system-pipeline.md](./05-system-pipeline.md) |
| **TierState 状态机** | [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) §2(P2/P3 单向无环) | P4 在状态机加一条 `gibbous→interp` 边表 deopt,但单向骨架不动,详 [./04-osr-deopt.md](./04-osr-deopt.md) §1 |
| **差分轨道**(crescent vs gibbous byte-equal) | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 / §7、[../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2 | P4 同槽位接入 `WangshuGibbousJIT` runner,逐字节对拍——同款 fuzz / GC 压力 / 强制全升模式,详 [./08-testing-strategy.md](./08-testing-strategy.md) |
| **可编译性闸门(F1-F7)** | [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3 | P4 沿用同一闸门,F7 后端能力面替换为 P4 后端版本——投机叠在子集**之内**(原则 4 不松动,详 [./03-speculation-ic.md](./03-speculation-ic.md) §2.3) |
| **NaN-box 值表示** | [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §3.2 | P4 原生码直接操作同一 u64 编码,deopt 物化 = memmove([./04-osr-deopt.md](./04-osr-deopt.md) §3.2 兑现 [../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提四) |
| **arena 共见** | [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md) §1.1、[../p3-wasm-tier/03-memory-model.md](../p3-wasm-tier/03-memory-model.md) §1 | P4 原生码读写同一 arena(linear memory 等价),无值表示转换——值世界跨 tier 一字不改 |
| **慢路径助手协议** | [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §3 | P4 trampoline 出去调 helper(算术 metamethod / arena 分配 / IC miss / 强制 safepoint)——形态延续,只是被调方从 wazero imported fn 换成本地 ABI([./05-system-pipeline.md](./05-system-pipeline.md)) |
| **线程级 tier 规则**(协程不升层) | [../p3-wasm-tier/07-coroutine-thread-rule.md](../p3-wasm-tier/07-coroutine-thread-rule.md) | P4 同款使用,协程线程一律走 crescent;不另外推一遍论证([../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md) tier-1 边界) |
| **safepoint 三类**(分配点 / 层边界 / 回边) | [../p3-wasm-tier/05-safepoint-gc.md](../p3-wasm-tier/05-safepoint-gc.md) §3 | P4 同款,但回边检查点改为原生码内 inline 序列(不再依赖 wazero 已自插的检查点)——形态延续,实施细节落 [./05-system-pipeline.md](./05-system-pipeline.md) §4.1 |
| **status 链错误冒泡** | [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §4 | P4 同款,错误经 status 字穿越 gibbous 帧到 pcall 边界——形态延续 |

**结论**:常规路径下 P4 不重做这些清单中任何一项;详细形态延续到 [./05-system-pipeline.md](./05-system-pipeline.md) / [./04-osr-deopt.md](./04-osr-deopt.md) / [./08-testing-strategy.md](./08-testing-strategy.md) 等子文档落地。本节只在立项判定层「点到名」——证明常规路径下 P4 是「纯后端替换」而非「全套重做」。

### 2.5 继承的「P3 没解决但 P4 必须解决」清单

承 §2.4 继承清单的对偶——P3 没解决 / 解决得不够好 / 与 P4 后端形态不兼容的项,P4 立项时必须自付:

| 项 | P3 现状 | P4 立项后必须自付 |
|---|---|---|
| **四项税兑现** | P3 全套外包 wazero | P4 自付:exec mmap / W^X / icache flush / trampoline 系统管线(详 [./05-system-pipeline.md](./05-system-pipeline.md)) |
| **类型投机** | P3 不投机(零 deopt 严守) | P4 引入投机(IC 反馈 → f64 快路径 + guard,详 [./03-speculation-ic.md](./03-speculation-ic.md)) |
| **deopt 机器** | P3 不存在 | P4 函数级 OSR exit + 物化序列(详 [./04-osr-deopt.md](./04-osr-deopt.md)) |
| **机器码生成** | P3 wazero 自动生成 | P4 自管 codegen + 双架构发射函数(详 [./06-backends.md](./06-backends.md)) |
| **call 核架构边界破除** | P3 受 F2-b 限制(详 §3.3) | P4 仅边际改进,不破除(详 §7.4)——这是「P4 必须解决但解决不彻底」的项 |

**纪律**:这一清单与 §2.4 继承清单是对偶的——继承的不重做,自付的别假装继承。把两份清单分开列,可避免立项判定时把「P4 自付项」误算到 P3 已铺好的工作量内,造成估算偏差。

---

## 3. 启动判定:何时正式立项

### 3.1 立项的硬前置(三条必备条件)

P4 立项不是「P3 收口后下一周自动启动」,而是有硬前置条件——任何一条不满足,立项就推迟或跳过。

| 前置 | 内容 | 不满足时 |
|---|---|---|
| **前置 1:P3 已交付** | P3 PW0-PW10 全卷已收口(状态见 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md))——分层骨架在常规路径下提供给 P4 | 未满足意味着「P3 还没跑完」,立项 P4 等于跳跃路径(§2.2 已说不复存在);此情形不存在 |
| **前置 2:真实宿主负载证据** | 首个目标宿主([../../llmdoc/overview/project-overview.md](../../llmdoc/overview/project-overview.md) 多运行时规则引擎)的实际热路径以列内核形态出现,且 P3 实测基线证明列内核类负载收益不够 | 没有宿主真实负载证据 = P4 立项凭工程野心,违反 [../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律——推迟立项,等宿主侧实证 |
| **前置 3:资源到位** | 1-2 人年的人力预算到位 + 双架构 CI 物理 runner 到位(详 [./06-backends.md](./06-backends.md) §5.2) | 资源未到位 ⇒ 推迟;勉强半人年开始 = 中途断档,违反 [../roadmap.md](../roadmap.md) §5 原则 3 |

三条前置共同决定立项时机:**只有当 P3 已上线 + 宿主有列内核负载且 P3 兑现不到 luajc 档 + 1-2 人年预算到位 + 双架构 CI 到位,P4 才正式立项**。任何一条不到位都按 §3.4 推迟或跳过。

### 3.2 反向问题:P3 收益是否已够

承 [../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md)「P5 仅在 P4 不够时启动」同源逻辑——这里是 P4 立项的对偶面:**P4 仅在 P3 不够时启动**。

「P3 收益是否已够」的精确判据:

1. **首个目标宿主的端到端 benchmark**(实际负载形态,非 micro-benchmark)启用 P3 后,与原 gopher-lua 基线对比,**端到端可见加速 ≥ 宿主可接受阈值**(由宿主侧定,P4 立项前外部确认)。
2. **若端到端可见加速 < 阈值**,且 profile 揭示瓶颈在 VM 解释/编译层(非边界税、非分配、非宿主侧自身):**P4 立项的反向条件成立**——P3 不够。
3. **若端到端可见加速 ≥ 阈值**,且首个目标宿主已落地:**P4 立项的反向条件不成立**——P3 已够,P4 推迟或跳过(§3.4 第三档)。

这道反向问题是 [../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付不亏」在 P4 立项点的字面体现:**P3 单独已能兑现宿主需求时,P4 不必启动**。本节同时给 §6 P5 边界提供对偶——P5 立项时同样问「P4 是否已够」。

### 3.3 P3 现状:实测基线对照 luajc 档

P3 PW10 收口时的本机实测基线(详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0 / §14.10),硬件 16 核 Intel Xeon 6982P-C,2026-06-16,基准 `bench-all` 2s × 3 count:

| bench 核 | crescent (P1) | gibbous (P3) | gibbous/crescent 倍率 | 评估 |
|---|---|---|---|---|
| **loop**(循环密集,列内核理想形态) | 5.61ms | 1.68ms | **2.95x** | V14 ≥2x 达标;Horner 5 次多项式同档 |
| **table**(表密集) | (基线) | (略劣) | **0.88x** | 跨层 IC miss 助手成本主导 |
| **call**(小叶函数高频调) | (基线) | (慢 2x) | **0.52x** | 「bench kernel 结构性架构边界」(§14.10),body 含 ReasonUnknownCall(F2-b 不可升)使顶层升层 + ④ emitCall fast body 均无显著效果 |
| **mixed**(混合) | (基线) | (相当) | **0.99x** | 几乎不升不降 |

**对 luajc 档的距离评估**:
- **loop 核 2.95x over P1**(P1 自身已 2.45x over gopher-lua,见 evolution-roadmap §P1)≈ **7.2x over gopher-lua**——已显著超过 luajc 档(4.4x over gopher-lua),列内核形态下 P3 已兑现 luajc 档的 1.6x。
- **table 核 0.88x / call 核 0.52x / mixed 核 0.99x**——非循环密集的真实负载形态下,P3 还未到 luajc 档;尤其 call 核 0.52x(慢于 P1 解释器 2x)是「bench kernel 结构性架构边界」(详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10),profile 揭示 52% 在 enterGibbous + 38% 在 wazero CallWithStack。

**这一基线的两层含义**:

1. **列内核理想形态下 P3 已够**——loop 核 2.95x,远超 luajc 档,P4 立项的「兑现近期目标」已部分到手。如果首个宿主的真实负载就是 loop 核形态(纯计算 + 循环),P3 已够,P4 立项需要更强证据。
2. **call 核 / table 核形态下 P3 不够**——call 核 0.52x、table 核 0.88x 远未到 luajc 档。如果首个宿主的真实负载是 call 密集 / table 密集形态,**P4 立项的反向条件成立**——但有重要前提:**call 核 0.52x 是 bench kernel 结构性边界,不是 wazero 后端实施不足**(详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10),P4 原生后端能否破除这一边界,需在立项判定时单独评估(见下条)。

**关键追问:P4 原生后端能否破除 call 核架构边界?**

call 核 0.52x 的根因是 F2-b(静态分析不能确定被调函数不 yield)→ body 不可升,profile 实证 enterGibbous + CallWithStack 主导。这一形态在 P4 下:

- **wazero CallWithStack 替换为原生码 trampoline** ⇒ 跨层成本可能从 ~143ns 降到几十 ns(原生 ABI 直跳无 Wasm 语义中介);但**无法把 0 跨层做到 0 跨层**——bench kernel body 不可升时,enterGibbous + 跨层仍发生。
- **F2-b 静态分析口径**是 P2 决策机的事,与发射后端无关 ⇒ P4 不解决「body 可升」问题。
- **结论**:P4 对 call 核可能有边际改进(估计 0.5x-0.8x 量级,具体由 P4 原生 trampoline 与 P3 wazero trampoline 跨层成本之比决定),**但破不到 ≥1x**——除非 bench kernel 形态调整或 F2-b 分析口径放宽,这两项均超出 P4 范围(承 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10.5 followup 触达条件)。

这道追问写实**:P4 不是 call 核 0.52x 的银弹**,立项判定不能把这一项当 P4 主收益寄托。P4 的主收益仍在 loop 核 / table 核 / 一般列内核形态——把 P3 已到的 7.2x 推到 8-10x over gopher-lua,把 P3 未到的 0.88x 推到 ≥1x。

**关键追问的对偶面:P4 原生后端能否在 loop 核进一步推升?**

loop 核 P3 已 2.95x(7.2x over gopher-lua),距 luajc 档(4.4x over gopher-lua)已超 1.6x。P4 对 loop 核的潜力:

- **dispatch 消除已部分由 wazero 在 P3 兑现**——P3 把字节码翻译成 Wasm 直线代码,wazero 编译为机器码,dispatch 已大幅消除;P4 在此之上的边际是「无 Wasm 语义中介」(LV2/local 操作直接对 arena 寻址,而非经 wazero memory 抽象)。
- **IC 投机**(类型稳定时直接 f64 指令)是 P4 独有,P3 不投机;loop 核数值密集程度决定 P4 能拿到多少投机收益——Horner 多项式(纯 f64 算术 + IC stable)预期收益较大(估计推到 4-6x over P1 ≈ 10-15x over gopher-lua)。
- **NaN-box 物化优势**:P4 OSR exit 物化 = memmove([./04-osr-deopt.md](./04-osr-deopt.md) §3.2),但 loop 核 deopt 频次低,这一优势在 loop 核兑现量小,主要在 table / mixed 核兑现。

**结论**:P4 在 loop 核可期边际推升,但 P3 已超 luajc 档时,P4 立项的紧迫性减弱——若首个宿主真实负载就是 loop 核,P4 立项是「锦上添花」非「雪中送炭」,§3.4「跳过」档可能更合理。

### 3.4 立项的三档策略

承 §2.3 三档形态,本节展开判据:

#### 全启(立项 1-2 人年完整 P4)

触发条件(三条同时满足):
- 前置 1/2/3 全过(§3.1);
- §3.3 实测基线证明 P3 在首个宿主真实负载下未到 luajc 档;
- §3.3 关键追问下 P4 原生后端有可信收益预期(loop 核 / table 核 / 一般列内核形态)。

实施形态:
- per-function 模板编译 + IC 投机 + amd64/arm64 双后端 + OSR exit 全套(详 [./02-template-direction.md](./02-template-direction.md) / [./03-speculation-ic.md](./03-speculation-ic.md) / [./04-osr-deopt.md](./04-osr-deopt.md) / [./05-system-pipeline.md](./05-system-pipeline.md) / [./06-backends.md](./06-backends.md))。
- 验收 = §4 luajc 档(列内核负载)+ V1-V18 同 P3 全套差分(详 [./08-testing-strategy.md](./08-testing-strategy.md))。

#### 部分前置(minimal P4 概念验证)

触发条件:
- 前置 1/2/3 部分到位(典型:资源紧张或宿主负载证据不充分,但 P3 实测基线接近 luajc 档想用最小 P4 推一把);
- 想以最小代价验证「per-function 模板编译 + IC 投机」在本码库 physics 下的真实收益。

实施形态:
- 单架构(amd64)+ 仅算术投机的 minimal P4(本文 §4.3 内部第二闸门)。
- Horner 多项式列内核 benchmark 单档验收。
- 不达预期 ⇒ 停下重评。这一档是「为了立项判定本身收集数据」的形态,与 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) PW0 spike 同源(用最小代价决定大方向)。

#### 跳过(P4 推迟或不做)

触发条件(任一满足):
- 前置 2 不满足:首个宿主真实负载需求不存在或未确认 ⇒ 工程野心驱动立项被否决;
- §3.2 反向问题答「P3 已够」:首个目标宿主端到端可见加速已 ≥ 阈值,P4 立项无业务收益;
- §3.3 关键追问答「P4 对宿主真实负载形态无可信收益预期」:典型场景是宿主负载就是 call 密集形态,P4 不破 0.52x 架构边界,立项不划算;
- 资源紧张到无法启动 1-2 人年项目(前置 3 长期不到位)。

实施形态:
- P4 不做,P3 PW0-PW10 现状保留作为 wangshu 的当前面;
- 立项判定凭据(本文 + implementation-progress)写入档案,后续条件成熟时重新评估;
- 若性能仍不够、且 P5 立项条件成立,**P5 直接接 P3**——这是 [../p5-trace-jit.md](../p5-trace-jit.md) §1 的另一进入形态(P5 不强制经 P4)。

**三档策略的纪律**:由用户(项目主决策者)与主助理共同裁决,数据进 [./implementation-progress.md](./implementation-progress.md) 永久存档(§5.3)。本文不替用户拍板某档,只把判据列清楚——这是 [../../llmdoc/guides/multi-doc-drafting.md](../../llmdoc/guides/multi-doc-drafting.md) 「单点收口 + 用户裁决」工作流在立项闸门处的体现。

### 3.5 立项判定决策树

把 §3.1 + §3.2 + §3.3 + §3.4 整合成单一决策树,作为立项时主助理与用户对齐的判据:

```
                    [启动 P4 立项判定]
                            │
                            ▼
              [前置 1:P3 已交付?]
              ├─ 否(跳跃路径,§2.2 不复存在)
              │   └─ 异常情形,不在本文范围
              └─ 是(继续)
                            │
                            ▼
            [前置 2:首个宿主真实负载证据到位?]
            ├─ 否(无证据 / 待外部确认)
            │   └─ 决策:推迟立项;§7.3 风险记入档案;
            │     等宿主侧补齐数据后重新评估
            └─ 是(继续)
                            │
                            ▼
       [§3.2 反向问题:P3 收益是否已够首个宿主?]
       ├─ 是(端到端可见加速 ≥ 阈值)
       │   └─ 决策:跳过 P4(§3.4 第三档);
       │     P3 收口为当前面;条件成熟时重新评估
       └─ 否(P3 不够,继续)
                            │
                            ▼
   [§3.3 关键追问:P4 原生后端对宿主负载形态有可信收益预期?]
   ├─ 否(典型:宿主负载是 call 密集形态,P4 不破 0.52x 边界)
   │   └─ 决策:跳过 P4(§3.4 第三档);
   │     转 P5 trace JIT 立项判定([../p5-trace-jit.md](../p5-trace-jit.md) §1)
   │     或宿主侧改造为列内核形态
   └─ 是(继续)
                            │
                            ▼
              [前置 3:资源到位?(1-2 人年 + 双 arch CI)]
              ├─ 否(资源紧张)
              │   └─ 决策:推迟立项;监测资源到位
              └─ 是(继续)
                            │
                            ▼
       [档位选择:全启 vs 部分前置 minimal P4?]
       ├─ 全启(§3.4 第一档,资源充足 + 收益预期强)
       │   └─ 立项 P4 完整投入;走 [./02-template-direction.md](./02-template-direction.md) 起的实施路径
       └─ 部分前置(§3.4 第二档,资源紧张 + 想最小代价验证)
           └─ minimal P4(amd64 + 算术投机)+ §4.3 第二闸门
```

**决策树的纪律**:每条边都有显式判据(本文具体节号),不允许凭直觉跳分支——这是 [../../llmdoc/guides/multi-doc-drafting.md](../../llmdoc/guides/multi-doc-drafting.md) 「自包含契约」纪律在立项闸门处的对位。

### 3.6 立项判据与 P3 PW0 spike 的对照

P3 PW0 spike 与本文 P4 立项判定**形态平行但内容不同**(承 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.1 spike 闸门同款定位):

| 维度 | P3 PW0 spike([../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)) | P4 立项判定(本文) |
|---|---|---|
| **闸门形态** | 物理实测(wazero call boundary <150ns) | 综合判定(三档前置 + P3 现状 + 宿主负载证据) |
| **判据数据** | spike 三档样本(S1/S2/S3)实测 ns/op | P3 实测基线(本文 §3.3)+ 宿主侧端到端数据 |
| **判定时点** | P3 PW0 启动前(0.5-1 人月 spike) | P3 PW10 收口后,任意时点(可推迟无成本) |
| **决策不可逆性** | 通过 ⇒ 开工 P3 PW1-PW9;不通过 ⇒ 跳跃路径 | 通过 ⇒ 立项 P4 1-2 人年;不通过 ⇒ 推迟或跳过 |
| **数据归档点** | [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) | [./implementation-progress.md](./implementation-progress.md)(立项时建立) |
| **战略价值** | 即便不通过,数据建立在实测而非乐观估计上 | 即便不通过,立项凭据写档,后续阶段可援引 |

**关键差异**:P3 spike 是单一物理指标(<150ns 二元判定),P4 立项判定是多维综合(三档前置 + 反向问题 + 关键追问 + 档位选择)。这一差异源自两阶段在流水线上的位置——P3 spike 的对象是「某项物理事实是否成立」,P4 立项的对象是「整个项目是否值得 1-2 人年投入」,后者天然更复杂。

形态相同处:**都是闸门级单点决策不可绕过 + 都把数据进档作永久凭据 + 都接受双向结果(通过 / 不通过)作为合理产出**。

---

## 4. 验收锚点

### 4.1 量化口径:列内核负载 ≥ 164μs 一档

承 §1.2,P4 验收的精确量化锚:

> **同等工作量(Horner 5 次多项式量级或同档列内核基准)上,gibbous-jit 的 ns/op 不劣于 luajc 同基准的 ns/op。**

绝对值:**164μs 那一档**(校准测量 1 第三档,详 §1.1)。

相对值:**≥ 4.4x over gopher-lua 同基准**(以 729μs 为分母换算)。

形式化:
```
P4 验收(单一硬指标):
  ns_per_op_gibbous_jit(列内核基准) ≤ 164μs(±噪声带宽)
  即同基准下 P4 不输给 luajc,逼近 LuaJIT 154μs(差距 ≤6%)
```

为什么取 164μs 而不是 154μs(LuaJIT 档):
- 164μs 是 luajc 档,与 P4 同形态(per-function 编译 + 通用后端优化,无 trace 投机);LuaJIT 154μs 是 trace JIT 形态,不是 P4 同形态产出物。
- 154μs 与 164μs 仅差 6%——这 6% 留给 P5 trace JIT,不是 P4 的边界(§1.3 + §6.2)。

**坐标系警告(承 [../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md) 速查表)**:
- 流水线图倍率「trace 收益 ~70%」与本节验收门槛**不在同一坐标系**——前者描述 P4 拿到 trace JIT 理论收益的 ~70%(剩余 ~30% 是 P5 边际),后者是 luajc 档的硬数字,两者不互相换算。

### 4.2 基准约束(列内核形状)

P4 验收基准必须是列内核形状([../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §6.1 硬约束):**一次 Call 进 VM 整批迭代**——per-item 形状下边界成本主导(承 [../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提一两个校准测量),测不出 P4 的本体收益。

具体约束:

| 约束项 | 内容 | 违反后果 |
|---|---|---|
| **一次 Call 进 VM** | 调用 `Program.Call(state, arena, args)` 一次,Lua 内循环跑 N 个 item | per-item 调用形态下边界成本主导,加速比稀释到 ±5-7% 噪声([../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 校准测量 2 实证) |
| **N ≥ 1000** | 单批数据量足以摊销 trampoline 进入成本 | N 太小则单次跨层成本不被摊销,数字反映边界税而非 VM 本体 |
| **基准对齐 Horner 多项式** | 5 次多项式,1000 items,与 [../roadmap.md](../roadmap.md) §1 校准测量 1 同形 | 形态不对则与 luajc 档绝对值不可比,失去 luajc 档的参照价值 |
| **同机同日 A/B** | gibbous-jit vs luajc 同硬件同日同基准 | 跨硬件/跨日数据漂移导致结论失真;承 [../../llmdoc/guides/perf-optimization-workflow.md](../../llmdoc/guides/perf-optimization-workflow.md) §5「跨机器基线对照」纪律 |

详细 V14 / V15 等验收口径列在 [./08-testing-strategy.md](./08-testing-strategy.md)。本节只点验收锚的硬约束,不展开 V 编号。

### 4.3 P4 内部第二闸门:minimal P4 中途校验

「人年级投入的中途校验」风险。本文把该风险升格为 P4 内部第二闸门,在立项后实施过半时启动:

**第一闸门**:本文承担,立项前判定(§3)。
**第二闸门**:P4 实施过半时承担,minimal P4(amd64 单架构 + 仅算术投机)先打通全管线并在 Horner 档位测一次。

第二闸门触发的判据:
- minimal P4 跑通后实测列内核加速比未到 luajc 档(差距 > 30%);
- profile 揭示瓶颈不在 dispatch 消除 / IC 投机本体(说明模板编译 + 类型投机的核心论证不成立)⇒ 立即停下重评。

第二闸门的存在是 [../roadmap.md](../roadmap.md) §5 原则 3「任何闸门停下不亏」在 P4 内部的套用(承 PW9/PW10 同款 spike 闸门 precedent,详 [../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md](../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md))。

minimal P4 的具体形态由 [./02-template-direction.md](./02-template-direction.md) / [./03-speculation-ic.md](./03-speculation-ic.md) / [./06-backends.md](./06-backends.md) 落地——本文只在立项判定层声明该闸门的存在与位置。

### 4.4 luajc 与 LuaJIT 之间 6% 差距是 P5 的边际

P4 验收锚明确不含 154μs(LuaJIT 档),理由承 §1.3:

| 项 | 责任阶段 | 备注 |
|---|---|---|
| 729μs → 164μs(gopher-lua → luajc) | P1 + P2 + P3 + **P4** 共同 | luajc 档是 P4 验收锚 |
| 164μs → 154μs(luajc → LuaJIT,6% 差距) | **P5**(若启动) | 这 6% 留给 trace JIT 的真正护城河——CSE / 循环不变量外提 / 分配下沉 / 寄存器分配 / snapshot |

**P5 不在 P4 验收范围内**——P4 把列内核做到 164μs 即合格,不必再压向 154μs。压向 154μs 等于在 P4 内部做 trace JIT,违反 [./02-template-direction.md](./02-template-direction.md) §1.3 的边界划定(P4 = dispatch 消除器 + IC 投机注入器,不是优化编译器)。

P5 立项判定的输入(§6.2)即「P4 是否兑现 luajc 档,以及距离 LuaJIT 档还有多少」——本节不替 P5 拍板,只把坐标系点清楚。

### 4.5 验收的四个分档(承 P3 PW9/PW10 实测形态)

P4 验收不是「luajc 档过线即全部 pass」二元结果,承 P3 PW10 实测基线把负载分四档(详 §3.3):

| 验收档 | 工作负载形态 | P4 目标(相对 P1) | P4 目标(相对 luajc 同基准) |
|---|---|---|---|
| **loop**(循环密集,列内核理想形态) | Horner 多项式量级 | ≥3.5x(P3 已 2.95x,P4 进一步提升) | ≥1x(luajc 档不输) |
| **table**(表密集) | 表读写密集脚本 | ≥1.5x(P3 0.88x,P4 应破 1x) | ≥0.6x(可接受边际,不必硬撑到 1x) |
| **call**(小叶函数高频调) | 函数式风格脚本 | ≥1x(P3 0.52x,P4 破跨层税) | 不在 P4 主验收范围(F2-b 架构边界) |
| **mixed**(混合) | 真实生产脚本类 | ≥1.5x(P3 0.99x,P4 应有显著提升) | ≥1x(可接受) |

**主验收锚是 loop 档**(§4.1 luajc 档单一硬指标);table / mixed 档是辅助验收;call 档是「不强求,但若兑现则锦上添花」的边际验收(§7.4 风险已写明 P4 不破 0.52x 架构边界)。

具体 V14/V15 等验收口径列在 [./08-testing-strategy.md](./08-testing-strategy.md);本节只点验收分档的形态,不展开口径细节。

---

## 5. 决策不可逆纪律

### 5.1 立项后退出代价

承 §0.2,立项 P4 后退出 = 推翻已写代码:

| 立项后实施时点 | 退出代价 | 是否仍合理 |
|---|---|---|
| **立项 0-3 月**(早期,minimal P4 还在搭骨架) | 沉没成本 ≤ 0.5 人月,可撤 | 仍合理(配合第二闸门,详 §4.3) |
| **立项 3-9 月**(中期,核心模板 + 双后端骨架已成) | 沉没成本 3-9 人月,撤 = 删大半代码 | 仅在第二闸门红灯下合理(§4.3) |
| **立项 9 月后**(后期,IC 投机 + OSR + 测试套已成) | 沉没成本 ≥ 9 人月,撤 = 重大返工 | **不合理**——除非验收阶段证明无收益,否则继续完成兑现立项凭据 |

**纪律**:立项前的判定权(本文 §3)与立项后的实施权(其他子文档)是分离的——立项后大方向锁定,只有第二闸门的红灯能合法终止;否则推进到验收。这与 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.2 P3 闸门的「单点决策不可绕过」同源。

### 5.2 立项前 vs 立项中 vs 立项后的判定权

清晰分工(承 [../../llmdoc/guides/multi-doc-drafting.md](../../llmdoc/guides/multi-doc-drafting.md) 「主助理与用户对齐」纪律):

| 阶段 | 判定权归属 | 决策内容 |
|---|---|---|
| **立项前** | 本文 §3 + 用户裁决 | 全启 / 部分前置 / 跳过——三档由 §3.4 判据 + 用户拍板 |
| **立项中** | 第二闸门(§4.3)+ 用户裁决 | 继续 / 终止——第二闸门红灯触发,用户裁决去留 |
| **立项后实施期** | 主助理 + 各子文档 | 不裁决「P4 该不该做」,只裁决具体实施细节(候选谱系内 vs 内部边界);超出范围(如要不要扩到 trace JIT)按 §0.2 不可逆纪律拒绝,转到 P5 立项 |
| **验收期** | 本文 §4 + 用户裁决 | 验收 pass / fail——pass 进 P3 去留(详 [./07-p3-retirement.md](./07-p3-retirement.md));fail 触发反向决议(回滚或留作中层) |

**反向决议的合法性边界**:fail 不是「P4 设计错了」,是「P4 在本码库 + 当前宿主负载下未兑现承诺」——这两种归因的差别决定后续做什么。详详细分析在 [./implementation-progress.md](./implementation-progress.md) 验收回顾节(待立项后建立)。

### 5.3 数据进档协议

承 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §6.3「数据进档」纪律,P4 立项判定的数据进档同源:

| 数据 | 归档点 | 用途 |
|---|---|---|
| **§3.3 P3 现状实测基线** | [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0(已存档,本文引用) | 立项判定输入 |
| **§3.4 立项档位决议** | [./implementation-progress.md](./implementation-progress.md)(立项时建立) | 立项凭据 + 后续 P5 立项的输入 |
| **§4.3 第二闸门数据**(若立项) | [./implementation-progress.md](./implementation-progress.md)(立项后实施期建立) | 中途校验凭据 |
| **§4.1 验收数据**(若立项) | [./implementation-progress.md](./implementation-progress.md) + [./08-testing-strategy.md](./08-testing-strategy.md) | 验收凭据 + P3 去留决议输入([./07-p3-retirement.md](./07-p3-retirement.md)) |

**永久存档**:P5 立项时回查这些数据;P3 去留决议时援引验收数据;若 P4 反向决议(撤项),立项凭据写明「未达成的硬前置或闸门红灯」作为撤项依据。

### 5.4 立项前的「写在前面」纪律

承 [../../llmdoc/guides/perf-optimization-workflow.md](../../llmdoc/guides/perf-optimization-workflow.md) §5「跨机器基线对照」+ [../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律,P4 立项时必须把以下凭据「写在前面」(立项报告首页):

| 写在前面项 | 内容 | 失败后果 |
|---|---|---|
| **硬件标注** | 立项数据所在硬件型号 + 内核版本 + 测量日期 | 跨机器复现时数据不可比;立项判据失效 |
| **基准参数** | bench 工具版本 + benchtime + count + 锁频 / 绑核状态 | 立项数据噪声不可控;立项判据失效 |
| **立项判定档位** | 全启 / 部分前置 / 跳过——对应 §3.4 三档 | 不写明则后续争议无凭据 |
| **关键风险标注** | §7 各项风险的当时状态(已缓解 / 已识别未缓解 / 残留) | 实施期出现问题时不能溯源 |
| **回填请求清单** | §9 各项回填请求,主助理 / 用户裁决状态 | 立项后 P1/P2/P3 文档不同步,造成知识与实现脱节 |

把这五项「写在前面」是立项报告的最低标准——少一项立项判定都不能视为完整完成。这是 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §6.6 spike 决策报告模板的同款纪律,在 P4 立项点对位。

---

## 6. 与 P5 的边界

### 6.1 P5 启动条件 = 「P4 收益不够」

详 [../p5-trace-jit.md](../p5-trace-jit.md) §0 / §1。P5 立项判定的核心条件:

| 条件 | 内容 |
|---|---|
| **P5 主条件** | P4 已交付且兑现 luajc 档,但距 LuaJIT 档(154μs)的 6% 差距对首个目标宿主有显著价值,且首个宿主真实负载形态适合 trace JIT 优化(循环不变量 / CSE / 分配下沉) |
| **P5 否决条件** | P4 已够 + 宿主真实负载未到 P5 才能解决的形态——即「6% 差距对宿主无可见业务价值」⇒ P5 不立项 |

**重要分支:P5 不强制经 P4**(承 §3.4 跳过档):若 P4 跳过 + P3 收益不够 + 首个宿主有真实需求,P5 可以直接接 P3——这是 [../roadmap.md](../roadmap.md) §4「每阶段独立交付」的另一面体现。但 P5 直接接 P3 时,P5 自己承担「分层骨架的优化层兑现」(详 [../p5-trace-jit.md](../p5-trace-jit.md) §1.2)。

### 6.2 P4 验收数据是 P5 立项的输入

P4 验收(若立项 + 验收 pass)产出的实测数据(详 §4.1 + §5.3),作为 P5 立项判定的硬数字输入:

| P5 立项问 | P4 验收答 |
|---|---|
| P4 收益是否到位? | 列内核负载 ≥ luajc 档 ⇒ 到位 |
| 距 LuaJIT 档还有多少? | 6% 是基线;但 P4 实际数据可能更接近或更远(取决于实施细节) |
| 首个宿主真实负载下 P4 端到端加速够用吗? | 具体数据,P5 立项判定核心输入 |
| P5 主投资什么(IR 优化 / regalloc / snapshot)? | P4 profile 揭示的瓶颈类目 |

**P4 验收数据的 P5 启动节点**:并非 P4 验收 pass 即立刻启动 P5——P5 有自己的硬前置(详 [../p5-trace-jit.md](../p5-trace-jit.md) §1.1)。P4 验收数据进档,P5 在外部条件成熟时再启动判定。

### 6.3 若 P4 不立项,P5 直接接 P3 的边界

承 §6.1「P5 不强制经 P4」,本节展开此分支的边界:

```
[P3 PW0-PW10 已交付]
        │
        ▼
[P4 立项判定]
        │
   ┌────┴────┐
   ▼          ▼
立项 P4      不立项 P4(§3.4 跳过档)
   │          │
   ▼          ▼
[P4 验收]   [P3 现状作为当前面]
   │          │
   ▼          ▼
[P5 立项]   [若有 P5 需求,P5 直接接 P3]
   │          │
   ▼          ▼
[P5 实施]   [P5 实施,但承担「无 P4 中间层」的额外复杂度]
```

**P5 直接接 P3 的代价**:

| 代价项 | 内容 | 应对 |
|---|---|---|
| **OSR 着陆面** | P5 deopt 必须落到 P3 wasm tier 而非 P1 crescent;但 P3 的「整程序编译」形态使 deopt 落点更复杂 | P5 实施时另立 OSR 设计(详 [../p5-trace-jit.md](../p5-trace-jit.md) §3) |
| **trace 录制源** | P5 trace 录制点在 P3 wasm 边界 vs 在 P4 模板编译边界——前者 trace 跨过 wazero 边界,工程难度上一个量级 | P5 设计可选「在 crescent 解释器层录制」,绕过 P3 wasm 复杂度 |
| **优化层叠加** | P3 wasm + P5 trace JIT 是两层不同后端共存,差分矩阵扩大(crescent vs P3 vs P5) | P5 立项判定时单独评估;若过于复杂,可考虑 P5 启动前重新评估 P4 立项 |

**结论**:P5 直接接 P3 是合法但有代价的形态——P4 不立项时,P5 立项判定要把这些代价计入。本节为完整性记录,具体取舍由 [../p5-trace-jit.md](../p5-trace-jit.md) §1 承担。

### 6.4 P5 立项条件与 P4 立项条件的形态对偶

P4 立项判定(本文)与 P5 立项判定([../p5-trace-jit.md](../p5-trace-jit.md) §1)在形态上对偶——两者都是流水线下一阶段的开工闸门,共享同款判定结构:

| 维度 | P4 立项判定(本文) | P5 立项判定([../p5-trace-jit.md](../p5-trace-jit.md) §1) |
|---|---|---|
| **硬前置** | P3 已交付 + 宿主负载证据 + 资源到位(§3.1) | P4 已交付(若走 P3→P4→P5)或 P3 已交付(若走 P3→P5)+ 宿主负载证据 + 资源到位(P5 自己定义) |
| **反向问题** | P3 收益是否已够?(§3.2) | P4 / P3 收益是否已够? |
| **关键追问** | P4 原生后端能否破除 P3 架构边界?(§3.3) | P5 trace JIT 能否破除 P4 / P3 架构边界?(P5 文档定义) |
| **三档策略** | 全启 / 部分前置 / 跳过(§3.4) | 同款形态 + 是否经 P4 中间层(§6.3 分支) |

这道对偶面表明 wangshu 项目流水线后段(P3 之后)统一采用「立项判定先于实施」模式——这是 [../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付不亏」在闸门级的工程化体现。

---

## 7. 风险与开放问题

本节聚焦立项判定层的风险(实施层风险落 [./02-template-direction.md](./02-template-direction.md) / [./04-osr-deopt.md](./04-osr-deopt.md) / [./05-system-pipeline.md](./05-system-pipeline.md) 等)。

### 7.1 人年级投入的中途校验闸门

**风险**:P4 +1-2 人年是 P1+P2+P3 总和级别的投入,中途无校验等于把全部预算押在初期估算上。

**缓解**:§4.3 第二闸门——minimal P4 单架构 + 仅算术投机先打通全管线 + Horner 档单测;不达预期立即停下重评。

**残留**:第二闸门触发时机本身不易把握——「实施过半」是工程判断,具体什么时刻触发由实施期主助理与用户协商([./implementation-progress.md](./implementation-progress.md) 实施期里程碑节定义)。

### 7.2 跳跃路径已不再相关

承 §2.2,旧单文件的跳跃路径(P3 被 spike 否决 ⇒ P4 自建分层骨架)在 P3 已交付现实下不复存在。本风险项保留为历史记录,未来若 P3 出现根本退役决议(详 [./07-p3-retirement.md](./07-p3-retirement.md))——但那是另一个语境,当前 P4 立项判定不需考虑。

### 7.3 真实宿主需求待外部确认

**风险**:本文 §3.1 前置 2「首个目标宿主真实负载证据」需要外部(宿主侧)确认。若立项时宿主侧数据不充分,P4 立项凭工程野心驱动 = 高风险。

**缓解**:严格按 §3.4「跳过」档处理——前置 2 不满足时不立项;立项判定凭据写入档案,等宿主侧实证补齐后重新评估。

**残留**:外部条件成熟的时点不可控——若宿主侧长期不补齐数据,P4 长期推迟,后续阶段(P5)立项判定也受影响。这是项目人力与外部需求耦合度的内生风险,本文层面无可缓解。

### 7.4 call 核架构边界 P4 不破

承 §3.3 关键追问,P4 原生后端不是 call 核 0.52x 的银弹——bench kernel body 含 ReasonUnknownCall 时,P4 仍跑跨层(原生 trampoline 比 wazero CallWithStack 便宜但非零)。这一项写明:

**风险**:若首个宿主真实负载就是 call 密集形态(典型如 函数式风格 + 闭包高频调),P4 立项即便兑现,call 核加速比仍 < 1x,业务价值不显著。

**缓解**:§3.4 跳过档——这种宿主负载形态下 P4 不立项,转到 P5(trace JIT 内联跨函数边界,可能破除该边界)或宿主侧改造(列内核形态)。

**残留**:F2-b 静态分析口径放宽是 P2 决策机的事,不在 P4 范围;若有需求扩 F2-b,需另立项目。

### 7.5 开放问题(记入 [../../llmdoc/memory/doc-gaps.md](../../llmdoc/memory/doc-gaps.md))

| 问题 | 待解时点 |
|---|---|
| **首个目标宿主真实负载形态**(列内核 / call 密集 / 混合) | 外部确认;P4 立项前 |
| **P4 立项的具体档位决议**(§3.4 三档) | 主助理 + 用户裁决;立项时点未定 |
| **第二闸门精确触发时点**(§4.3) | 立项后实施期定 |
| **P3 去留决议的具体时点**(P4 验收时启动) | 详 [./07-p3-retirement.md](./07-p3-retirement.md);若 P4 不立项则该问题保留至 P5 立项时 |
| **P5 立项的输入数据**(P4 验收数据) | P4 立项 + 验收 pass 后产出;若 P4 不立项,P5 立项另寻输入 |

### 7.6 立项判定本身的元风险

立项判定是判断题,不是搜集题——本节列「判定过程本身」的元风险(meta-risk):

| 元风险 | 内容 | 缓解 |
|---|---|---|
| **判据局部化**(只看 P3 数据,不看宿主侧) | 仅凭 §3.3 实测基线就拍板,忽略 §3.1 前置 2 宿主负载证据 | 立项判定流程强制走完决策树(§3.5),不允许跳分支 |
| **乐观估算**(P4 收益高估) | 把 P4 原生后端潜力估算到 LuaJIT 档,但 P4 不是 trace JIT(§4.4 已说) | 估算锚定 luajc 档(§1.2)而非 LuaJIT 档;P4 收益锚明确写「+ luajc 档」非「= LuaJIT 档」 |
| **悲观估算**(P4 收益低估) | 因 §3.3 call 核 0.52x 推论 P4 整体不值得 | call 核 0.52x 是局部架构边界(§3.3 关键追问已写明)非 P4 整体水位;loop / table / mixed 核仍是 P4 主收益面 |
| **资源到位的乐观估算** | 假设 1-2 人年预算容易凑齐,实际可能跨多个项目周期 | §3.1 前置 3 列硬清单,资源不到位推迟 |
| **决策不可逆带来的过度谨慎** | 立项后无回头使主助理倾向「再等等」推迟立项 | §0.3 双向性已写明:即便不立项也是合理产出;不能因不可逆性而长期推迟 |

**纪律**:这五项元风险共同决定立项判定不是孤立判断,而是流程化决策——按 §3.5 决策树走完每条边,数据进档(§5.3),写在前面(§5.4),才算完整完成立项判定。

---

## 8. 不变式清单(本文承担)

承 [./00-overview.md](./00-overview.md) §9 的全 P4 不变式(待 00 写时聚合呈现),本子文档承担以下三条立项闸门级不变式:

### 8.1 「P4 是期权而非计划」

P4 不是流水线上 P3 之后的自动启动项,而是有硬前置的期权(承 §0.1):

- **行权条件**:三条硬前置(§3.1)+ 三档判据(§3.4)同时满足。
- **未行权代价**:零——立项判定凭据写档,不消耗实施预算。
- **不可逆性**:行权后大方向锁定(§5.1),退出代价随实施时点上升。

任何「下一阶段就是 P4」的工程惯性思维,违反本条不变式——P3 收口后下一阶段是「立项判定」而非「立项实施」。

### 8.2 「立项前先证负载证据,而非工程野心」

承 §3.1 前置 2 + §7.3,P4 立项的核心驱动力必须是首个目标宿主的真实负载证据,而非项目工程野心。「我们已实现 P3,自然下一步是 P4」是错误立项叙事——正确立项叙事是「宿主有真实需求 + P3 兑现不到 + P4 后端能解决」。

这条不变式与 [../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源:**主张须证据,不能凭直觉**。本条把这条纪律对位到立项判定层。

### 8.3 「luajc 档 = 兑现近期目标」

承 §1.2 + §4.1,P4 验收锚是 luajc 档(164μs)而非 LuaJIT 档(154μs)。

- 兑现 luajc 档 = 兑现 [../roadmap.md](../roadmap.md) §0「逼近 LuaJIT 档」近期目标(差距 6%)。
- 不兑现 LuaJIT 档(6% 差距)= P5 的边际,不是 P4 的边界。

任何「P4 必须做到 LuaJIT 档」的提案,违反本条不变式——把 P4 边界画到 P5 范围,等于在 P4 做 trace JIT,与 [./02-template-direction.md](./02-template-direction.md) §1.3 候选谱系否决论证矛盾。

### 8.4 「立项判定与实施权分离」

承 §0.2 + §5.2,立项前(本文判定权)与立项中(第二闸门 §4.3)与立项后(实施细节)是三段独立的判定流程,各有独立的判定权归属:

- **立项前**:本文 §3 判据 + 用户裁决——判「该不该做」;
- **立项中**:第二闸门红灯触发 + 用户裁决——判「继续 / 终止」;
- **立项后实施期**:主助理 + 各子文档——判「具体怎么做」,不重新判「该不该做」;
- **验收期**:本文 §4 + 用户裁决——判「pass / fail + 是否触发 P3 去留」。

任何越权判定(立项后实施期主助理擅自判「不做了」、立项前用户绕过判据直接拍板)都违反本条不变式——立项判定作为闸门级决策,流程化纪律不能让步给临时直觉。

### 8.5 「立项判定本身即是产出」

承 §0.1 + §0.3,立项判定本身即一次产出(无论结果是「立项」、「推迟」还是「跳过」):

- 立项凭据写档,后续阶段(P5 立项、P3 去留、宿主侧改造)可援引;
- 立项判定的过程数据(§3.3 P3 实测基线 + 宿主端到端数据)是 P5 立项的输入,即便 P4 不立项;
- 立项判定的判据(§3.5 决策树)沉淀为项目纪律,后续判定可复用。

这条不变式与 [../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付价值,任何闸门处停下都不亏」共同保证 P4 立项判定即便不通过也不亏。任何「不立项 = 浪费时间」的叙事,违反本条不变式——立项判定的过程产出本身有独立价值。

---

## 9. 回填请求(对 P1/P2/P3 现稿)

本文不主动改 P1/P2/P3 现稿,以下回填请求由主助理收口在 [./implementation-progress.md](./implementation-progress.md):

### 9.1 对 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) 的回填请求

- §0.3「闸门双向性」可补一个对位指针指向本文 §0.3——P3 spike 闸门双向性与 P4 立项判定双向性是同源逻辑,互引可强化纪律一致性。
- §5.4「跳跃路径下设计资产复用」表已罗列 P3→P4 设计资产继承,可补一个指针指向本文 §2.4——本文把继承清单按 P3 子文档拆分,§5.4 表可作为高层概要互补。

### 9.2 对 [../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md) 的回填请求

- §1 P3/P4 边界表(P1/P2/P3/P4 谁拥有什么)可补一行指向本文,作为 P4 立项判定的指针——0 总览只列 P4 实施层(同 tier 不同后端),立项闸门层未点到。

### 9.3 对 [../roadmap.md](../roadmap.md) 的回填请求

- §4 P4 段「+1-2 人年」估算可补「立项前置 = 立项判定(本文)」,使 P4 启动节奏与 P3 同款(spike 先于实施)显式化——目前 §4 P3 段有「开工前置 spike」措辞,P4 段无对位措辞,造成「P4 直接启动」的错误读感。

### 9.4 对 [../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md) 的回填请求

- 速查表 P4 行「前置 spike」列空——可补「P4 立项判定(详本文)」,与 P3 行「wazero call boundary <150ns」对位。
- §P4 正文段可补一句「立项判定先于实施(本文承担)」,与 P3 段「开工前置 spike」对位。

### 9.5 对 [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md) 的回填请求(可选)

- §6 跨文档定稿决策速查可加一行「P4 立项判定」,但 P2 是 P3/P4 共享前端,可能不需要——主助理裁决是否落入 P2 总览。

### 9.6 主动回填请求兑现纪律

承 [../../llmdoc/guides/multi-doc-drafting.md](../../llmdoc/guides/multi-doc-drafting.md) 「单点收口」纪律,本文回填请求**不主动兑现**——主助理在写完 P4 文档集后统一收口:

| 兑现时机 | 内容 |
|---|---|
| **本子文档完成时** | 各回填请求列在本节,不主动改 P1/P2/P3 文档 |
| **主助理收尾时** | 统一兑现 §9.1-§9.5 各项;不兑现的项保留在 [./implementation-progress.md](./implementation-progress.md) 的「待回填」节 |
| **立项时** | 立项判定档位决议进档时,顺手再扫一遍回填请求是否仍有效——若 §9.4 evolution-roadmap 速查表已被其它子文档兑现,本节可去除该项 |

这一纪律保证 P1/P2/P3 现稿不被多个 P4 子文档并行修改造成冲突——回填集中在主助理收口点,主助理负责最终一致性。

---

## 相关

- [../roadmap.md](../roadmap.md)(§1 校准测量 / §2 四项税 / §4 P4 阶段定义 / §5 五条贯穿原则)
- [../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md)(前提一 6% 校准 / 前提四 NaN-box 第一天承诺 / 前提三五条贯穿原则)
- [../../llmdoc/architecture/evolution-roadmap.md](../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P3/P4 同 gibbous tier-1 / 速查表 / 坐标系警告)
- [../../llmdoc/overview/project-overview.md](../../llmdoc/overview/project-overview.md)(项目身份 + 首个目标宿主)
- [../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../llmdoc/guides/design-claims-vs-codebase-physics.md)(主张须证据,不能凭直觉——本文 §0.1 + §3.1 + §8.2 同源纪律)
- [../../llmdoc/guides/perf-optimization-workflow.md](../../llmdoc/guides/perf-optimization-workflow.md)(profile 才是合同 / 跨机器基线对照——本文 §4.2 同源)
- [../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md](../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md)(里程碑级架构改动配 spike 闸门——本文 §4.3 第二闸门 precedent)
- [../../llmdoc/memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md](../../llmdoc/memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md)(profile 才是合同——本文 §3.3 关键追问 + §3.4 跳过档同源)
- [../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览,共享前端 / 分层骨架 / 验收口径——P4 继承入口)
- [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)(P3 闸门同款定位的对位文档——本文形态参照)
- [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(P3 PW0-PW10 实施现状 + §14.10 架构边界——本文 §3.3 数据来源)
- [./00-overview.md](./00-overview.md)(P4 文档集总览,本文是其 §0 文档地图所定的「立项闸门」单一事实源)
- [./02-template-direction.md](./02-template-direction.md)(方向裁决,本文 §1 / §4 仅落锚不展开)
- [./03-speculation-ic.md](./03-speculation-ic.md)(IC 反馈→f64 快路径 + guard,本文 §2.4 / §4.4 提对位)
- [./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化与 deopt 状态机,本文 §0.3 / §2.4 提对位)
- [./05-system-pipeline.md](./05-system-pipeline.md)(四项税兑现 + trampoline,本文 §2.4 提对位)
- [./06-backends.md](./06-backends.md)(amd64/arm64 双后端,本文 §3.1 / §4.3 提对位)
- [./07-p3-retirement.md](./07-p3-retirement.md)(P3 去留决策矩阵,本文 §6.1 / §5.2 提对位)
- [./08-testing-strategy.md](./08-testing-strategy.md)(差分接入与验收口径,本文 §4.2 提对位)
- [./implementation-progress.md](./implementation-progress.md)(立项数据 + 第二闸门数据 + 验收数据归档点)
- [../p5-trace-jit.md](../p5-trace-jit.md)(下一站:P4 收益不够时的开放式选项)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§6.1 列内核基准硬约束 / §3.8 Runner 抽象)

