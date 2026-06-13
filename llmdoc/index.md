# Wangshu llmdoc 文档地图

> 项目状态:**P1(crescent 解释器)完整交付 + P2(bridge 分层桥)PB0-PB7 全过线**——P1 全卷(M0-M14 + 收尾轮 + 测试加固轮 + 完整性补全轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮)与 P2 全卷(2026-06-13 单会话 PB0-PB7 冲刺:bridge 包骨架 + 回边/入口采样 + IC 反馈聚合 + F1-F7 可编译性闸门 + TierState 状态机 + Logger + mock P3 + e2e 验收)交付。**P2 后续优化轮**(精确 yield 调用图分析 / 阈值实测校准 / sync.Pool (C) 双表混合 / megamorphic 主动识别;设计文档原称 `P2+`)规划中。验收:simple 9.0x / arith 7.0x / loop 2.45x over gopher-lua;realworld 五项中四项反超(fib 1.31x / binary-trees 1.09x / spectral-norm 1.43x / nbody 1.08x;fannkuch 0.82x 为剩余短板);70 种子 + 随机脚本对拍官方 5.1.5 逐字节一致;P2 通过四档验收(byte-equal、profile 累积、升层日志、多 State `-race`)。**P3+ 未开始**。`docs/design/` 共 26 篇约 2.1 万行设计文档仍是规范源(P1 全卷 00-12 可实现深度、**P2 全卷 00-06 + implementation-progress 可实现深度(7453 行,2026-06-13 扩展;PB0-PB7 全过线)**、P3 详细设计、P4/P5 架构决策);P1 实现现状与 P3 迁移留口见 `docs/design/p1-interpreter/implementation-progress.md` 对账表;P2 实施现状对账表见 `docs/design/p2-bridge/implementation-progress.md`。本文档库是源文档之上的**知识压缩层**,记录意图与路由。
> 启动阅读顺序请看 [[startup]](本文件不重复有序启动清单)。
> **术语:`P2+` = P2 后续优化轮**(P2 主体 PB0-PB7 交付之后的持续优化项),**不是 P3 阶段**——见 design `p2-bridge/` 各篇与 `implementation-progress.md` §0 头注。

## 类别用途

- **`must/`** —— 每次任务都应先读的微型启动文档。只放跨任务、稳定、几乎每次都用得上的知识。
- **`overview/`** —— 项目/大特性的身份、边界与角色。
- **`architecture/`** —— 检索地图、所有权边界、流程与不变式。
- **`guides/`** —— 一篇一个工作流。
- **`reference/`** —— 稳定查阅事实:契约、schema、约定、术语。
- **`memory/`** —— 历史过程记忆。`memory/reflections/` 归 reflector 所有;`memory/decisions/` 与 `memory/doc-gaps.md` 归 recorder 所有。

## 现有文档与路由提示

### must/
- [[design-premises]] — **最重要的 MUST**。四组不可妥协前提:① 列内核负载形状(两个校准测量:LuaJIT 仅比 luajc 快 6%;生产端到端被稀释到 ±5-7% 噪声)② Go runtime 四项税 → 边界几十~百 ns 固定成本 ③ 五条贯穿原则 ④ 第一天 NaN-boxing 值表示承诺。**判断任何提案是否合理前先读这篇。**

### overview/
- [[project-overview]] — 项目身份与边界:望舒/Lua=月亮意象、纯 Go 嵌入式 Lua VM 定位、三层目标(Lua 5.1 核心)、三项非目标(§6)、首个目标宿主(多运行时规则引擎)、当前状态。**想知道「这是什么、不做什么」看这篇。**

### architecture/
- [[evolution-roadmap]] — 分层 VM 五阶段流水线 P1→P5(人力/倍率/验收/前置 spike)、月相 tier 命名映射。**问演进路线、阶段门槛、wazero<150ns spike、crescent/gibbous/fullmoon 看这篇。** 注意流水线图倍率与正文验收门槛不在同一坐标系。
- [[value-representation]] — 值表示与内存模型:NaN-boxing vs Go tagged struct 决策、自管 arena、自写 mark-sweep GC、同一块内存使编译层成增量。**问值/内存/GC/为什么这样选看这篇。**

### reference/
- [[embedding-contract]] — 宿主嵌入契约:`Compile→Program`、`Program.Call(state, arena, args)`(收尾轮已落地,差异标注见该篇)、arena ABI(类型化扁平列 + 字符串区 + presence bitmap,零拷贝读)、per-item 简易 API 子集已落地(含 Table 读写全闭环 + ForEach + globals baseline 状态隔离 + `CallInto` 零分配边界路径)、drop-in 定位。字段级 spec 在 `docs/design/p1-interpreter/11-embedding-arena-abi.md`。**问宿主怎么嵌入、API 形状、边界成本/CallInto 看这篇。**
- [[glossary]] — 术语表 + prior art 借鉴点。**遇到 NaN-boxing/arena/tier/月相/deopt/列内核等术语,或问参照项目看这篇。**

### guides/
- [[multi-doc-drafting]] — 多文档并行起草工作流:回填请求节协议、单点收口、验收口径收口点指定、子代理失败恢复纪律、收尾主动盘点不确定决策、向用户提问自包含契约。**要一次起草多篇互引文档、或大型设计任务收尾时看这篇。**
- [[perf-optimization-workflow]] — 性能优化工作流:profile 先行(预判清单会被推翻)、每项独立 benchmark 验证 + 单域提交、可疑优化 benchmark 否决门 + 快 revert、归因诚实、池化/复用类优化配套清单。**做性能优化前看这篇。**
- [[public-api-incremental-delivery]] — 公共 API 增量交付工作流(9 条纪律):设计承诺源三角验证、GCRef 接根(指向 [[embedding-contract]] 不变式条款)、单域物理隔离手法、错误消息稳定语义、行为变更显式标注、范围扩张顺手收口、对位测试断言先 grep oracle、internal 接口签名避免反向依赖标准库、**对称面检查**(写入/读出闭环)。**接公共面 issue / 扩 ABI 表面 / drop-in 对位补面 / 给 Program 加字段时看这篇。**

### memory/
- `memory/doc-gaps.md` — 已识别的文档缺口与待外部确认事项(随实现推进收敛;含已收口审计记录)。
- `memory/decisions/2026-06-11-design-review-decisions.md` — 设计评审轮 7 项裁决归档(验收 oracle 改官方 5.1.5、stdlib 反转对齐 gopher 提供面+三层禁用、ColInt64 超界报错、luac 同构软承诺等),每项含设计文档落点指针。**查「某决策为什么这样定、落在哪」先看这篇。**
- `memory/reflections/2026-06-11-design-doc-completion.md` — 设计文档集补齐(P1 全卷 + P2-P5)的过程反思:并行起草+单点收口模式、子代理中断恢复教训。
- `memory/reflections/2026-06-11-design-review-round.md` — 设计评审决策轮过程反思:主动盘点不确定决策的收益、裁决后即时 grep 同步、AskUserQuestion 自包含教训。
- `memory/reflections/2026-06-12-p1-implementation-sprint.md` — P1 实现冲刺(M8-M14 单会话收口)过程反思:difftest 后置的代价(5 个单测漏掉的语义 bug)、lcode.c 同构须到 helper 层、ci 指针刷新不变式、「简化实现+接口留口」模式。**做实现冲刺或 P2 接 difftest 前看这篇。**
- `memory/reflections/2026-06-12-p1-closeout-round.md` — P1 收尾轮(「已知简化」9 项全量落地)过程教训:对称机制复用通道(yield 借 error 哨兵)、IC 命中必须验同键(动态 key 指令)、Program 运行期可写字段须 State 私有化规则、生成器类型封闭纪律。**P2 实现 IC、给 Program 加字段、或写受控文法生成器前看这篇。**
- `memory/reflections/2026-06-12-test-hardening-round.md` — 测试加固轮(go-fuzz 目标/生成器两期/GC 压力/错误消息对拍/nightly)过程教训:每类 fuzz 上线当天即抓到 5 个真实 bug——fuzz 目标空转(脚本在跑但无 `func Fuzz*`)是最危险的虚假安全感、「末位多值源 A 处理」同族 bug 三处分布证明同构逻辑须抽 helper、top 恢复纪律(`L->top = ci->top`)是 5.1 调用约定的一部分且症状离根因极远、GC 压力模式让正常模式难触发的时序 bug 必现(弱表链截断实例)。**搭新防线、写调用桥、或评估「防线是否真在防」时看这篇。**
- `memory/reflections/2026-06-12-completeness-gap-round.md` — 完整性补全轮(特性探测 corpus + 25 缺口修复)过程教训:**差分 fuzz 两轴正交模型**——随机生成器文法跟实现走,只测「已实现行为的正确性」,对「缺特性」结构性失明;特性探测 corpus 按官方手册逐节写,测「特性面完整性」;probe 上线在 570+ 随机脚本全绿下一次扫出 25 个缺口。另含:probe 先过 oracle 纪律(两例笔误)、goIfTrue/goIfFalse 恒跳丢值 bug(`nil and 2` 错产 false)、pattern 灾难性回溯有界失败裁量、「probe 转绿 → 进生成器文法」护栏闭环。**设计新防线选参照系、评估 diff-fuzz 覆盖含义、或 P2+ 接新执行层时看这篇。**
- `memory/reflections/2026-06-12-longevity-review-fix-round.md` — 长稳与审查核销轮(freelist 复用 + 12 轮外部审查 22+ 项全核销)过程教训:**三轴防线模型**(fuzz=行为自洽 / probe=特性面完整性 / review=规范同构+形态组合,前两轴全绿不构成第三轴可省略的理由)、**同构须到时序层**(constFold 须在 exp2RK 之后,操作顺序本身就是规范)、**注释承诺审计**(注释声称的防线不存在就是 bug 预告,四实例)、**内存复用类变更的「良性→致命」升级清单纪律**(复用前先列哪些潜伏 bug 会从良性变 UAF,根审计/尺寸单一源/debug 设施同批)、修复轮收尾的历史 bug 保护测试盘点。**做资源复用类变更、引入外部审查、或写 C→Go 移植代码前看这篇。**
- `memory/reflections/2026-06-12-official-suite-perf-round.md` — 官方测试套与性能轮(luasuite 移植扫出 20 项分歧全修 + profile 驱动六项优化 + IC DataOff 实测回退否决)过程教训:三轴升级为**四轴防线模型**(官方套=作者语义断言,独有负断言能力)、stopAt 棘轮机制、profile 先行(最大收益项 closeUpvals 不在预判清单)、benchmark 否决门 + 快 revert、归因诚实(9.0x 主因是 thread 复用消固定开销)、快路径家族审计(cbaae3f 反例)。**做性能优化、接官方测试套、或评估防线覆盖时看这篇。**
- `memory/reflections/2026-06-12-issue1-api-gap-round.md` — issue #1 公共面 API 缺口轮(per-item drop-in 子集 + Register/Module + 公共 HostFn + Value.kFunction + pin 表 GC 根)过程教训:**「设计承诺源回看」纪律**(issue 字面边界与 issue 描述的业务场景不自洽是常见根因,从设计承诺源反推真实最小可用集)、**宿主长持 GCRef 必须接 GC 根**(first-class function value 暴露的对偶面,pin 表/`visitExtraRefs` 接 R8 类常驻根,Release 可选但根必须有)、**单域提交的物理隔离手法**(一锅写完后按文件抽取再 git add 分次,比 git add -p 选 hunks 更可靠)、**错误消息稳定语义纪律**(不带里程碑编号/hash/时态承诺,公共面措辞与实现状态解耦);附带短评:**主库 go.mod 零外部依赖**(benchmark oracle 依赖按用途拆独立子模块,`d1ff096`)。**两条 promotion 候选(增量交付 guide 立项、GCRef 接根升 reference 不变式)已在 issue234 反思轮拍板落地。**
- `memory/reflections/2026-07-14-issue56-api-gap-round-3.md` — issue #5/#6 公共面 API 缺口轮 3(Table.ForEach + globals baseline 状态隔离)过程教训:对称面缺口延迟发现(写入/读出闭环检查)、GC 根两条通道(pin 表 visitExtraRefs vs baseline visitExtraValues)、线性扫描量级显式注明、同款工作流三轮零阻塞验证 guide 已稳定。**promotion 已落地**:[[public-api-incremental-delivery]] 第 9 条对称面检查 + [[embedding-contract]] 不变式段补 baseline GC 根指针。
- `memory/reflections/2026-06-12-issue234-api-gap-round-2.md` — issue #2/#3/#4 公共面 API 缺口轮 2(Table / HideFileLoaders 沙箱 / Context 取消钩子)过程教训:同款工作流第二轮验证「设计承诺源回看 / GCRef 接根 / 单域物理隔离 / 错误消息稳定」四条纪律可复用(尤其 kTable 复用 kFunction 同款 pin 表零额外成本,验证机制级通用),**新增四条**:**行为变更显式标注**(`fromInner → fromInnerWithPin` 静默扩面被评审抓出,`bb1e9a8` godoc 补 v0.1.1→v0.1.2 行为变更段,是错误稳定语义纪律的对偶面)、**范围扩张顺手收口**(发现上轮裁口阻挡本轮 spec 时现场收掉比留 trick 跨 issue 强 100 倍,但 commit message 必须显式标注)、**对位测试断言先 grep oracle**(PUC 5.1 实际错误带变量名前缀,印象写法反而验证了实现已对齐官方)、**internal 接口签名避免反向依赖标准库**(`SetCancelHook(func() error)` 抽象签名留在门面层注入 context,保持 internal 零标准库非基础包依赖,P3+ JIT 同款机制可共用)。**两条 promotion 已落地**:[[public-api-incremental-delivery]] guide 立项聚合 8 条纪律 + [[embedding-contract]] 升级公共 first-class GCRef-bearing value 接 GC 根为契约级不变式。**做公共 API 演进对外披露评审、规划「相对前版静默改了什么」时看这篇。**
- `memory/reflections/2026-06-13-drift-audit-and-fuzz-hardening-round.md` — 累积偏移审计 + fuzz wall-clock + 嵌入式 hardening 轮(`bb1e9a8..701b1a4`)过程教训:**① 累积偏移审计纪律**——同一驱动源接入到第 N 轮后必做横向审计(本期 6 issue 跨 3 轮),与官方套轮「快路径家族审计」([[official-suite-perf-round]] 做对了 1)同「同族审计」纪律家族(前者性能优化扫同族快路径,本条公共 API 演进扫同族累积偏移);**② 审计具体落地手法**——主导判断而非外包子代理(累积偏移是判断题不是搜集题)、维度 AB 划分(违设/别扭)、严重度三档分级(本轮收/P2+ 入档/建项目纪律)、留「没有违设、看似别扭其实是正解」节防审计偏负面;**③ 测试机制依赖底层框架的时间维度假设**——fuzz wall-clock(`-fuzztime`)与 `SetStepBudget` budget 不同步,seed 上限必须远低于 fuzztime 量级(`SetStepBudget(1<<20)` 下保守取 `1e5` 而非 `1e6`),与「指令计数不能替代 wall-clock 事件触发」同原理(量级层不可跨界假设);**④ 真 bug 由 flake 排查顺手抓出**——修 flake 的副作用(改 seed/阈值/采样率)等价于覆盖路径变化 = fuzz 新一轮探索,本期 1e9→1e5 紧边界 mutate 距离更短抓到 `string.rep("k", 1e14)` 真 OOM,机制级而非巧合;**⑤ 嵌入式 hardening 阈值类**——主动偏离对位 backend 的特殊关系(底线优先级「宿主进程不可崩 > 与对位字节一致」),不同于豁免(合法的不比对)、不同于设计差异(语义有意不同),阈值口径分配类 1 GiB / 循环类 1<<24 + 引入条件三条落 `12-testing-difftest.md §4.9`,与长稳轮「内存复用配套清单」同属「承诺优先级冲突的明确裁判机制」家族。附:`make all` 默认扩面(`44dc6f8` 三件套 → 七件套,工程纪律选择)。**触发场景**:做累积偏移审计 / 写 fuzz/budget 类测试 / 设计 hardening 阈值 / 同款 issue 接入多轮后想暂停做横向审计时看这篇。
- `memory/reflections/2026-06-13-issue8-boundary-cost-round.md` — issue #8 边界成本轮(`CallInto` 零分配 + 四档真实世界 benchmark)过程教训:**① 实现浪费 vs 架构成本辨析**——性能 issue 第一分类问题,边界双拷贝(72 B/2 allocs,VM 栈→inner→public)是可消除的实现冗余而非 NaN-boxing/arena 架构成本,前提一能判否优化提案但不能掩护实现浪费(尤其首个目标宿主真实热路径 + 已承诺 drop-in 对标场景);**② 零拷贝复用栈返回值的双重验证**——GC 根可达性(runningThread 复位后 mainTh 仍常驻根,`SetGCStressMode` + string 返回值 500 轮无 UAF)+ 覆写契约(返回值底层复用栈、下次进 VM 前消费完,与旧 `Call` 独立拷贝可长持的根本差异,godoc ⚠️ + 串台/独立性测试都留),与长稳轮「内存复用配套清单」同家族;**③ benchmark 必须覆盖真实交互形态**——「VM 不是自 high 产品」,旧基准全不跨界致 9× 头条是嵌入者够不到的天花板、真实热路径劣化不可见,固化四档(纯VM micro / 真实负载纯VM / 边界 mini / 真实负载 embedded),边界档并列 `Call` vs `CallInto` vs gopher 让问题与修复同屏可验;**④ README 性能数字同机同日整节重测**(本轮 simple 9.0×→5.9× 机器差异,四档自洽)。**promotion 候选**:教训 1（实现浪费 vs 架构成本）+ 教训 3（benchmark 覆盖真实形态）候选进 [[perf-optimization-workflow]],首次样本暂留观察。**触发场景**:收到「某形态比对标对象慢」的性能 issue / 做返回内部缓冲区切片类零分配优化 / 给嵌入式 VM 设计或评估 benchmark / 改 README 性能数字时看这篇。
- `memory/reflections/2026-06-13-p2-doc-expansion-round.md` — P2 单文件设计稿扩展为子目录详细设计的过程教训(`p2-bridge.md` 703 行 → `p2-bridge/` 8 文件 7453 行,multi-doc-drafting 二轮验证):**① multi-doc-drafting 三协议二轮零阻塞**——并行起草 + 单点收口 + 回填请求节 + 唯一验收口径同 P1 19 篇起草轮验证有效,同款工作流第二次跑零阻塞,验证机制级稳定;**② 子代理工具卡死的 Bash heredoc 兜底**(新教训)——子代理 Write/Edit 卡死但 Bash 可用,自救协议「`cat >> file <<'EOF' ... EOF` 续写」是救命招;主助理「停子代理 + 派新子代理续写」是合法救援动作,介于 multi-doc-drafting guide 「同篇连续失败 2-3 次主助理亲写」上限之前的中间档;**③ 拆子目录前必须做章节映射表**(新纪律)——单文件 → 子目录拆分时,带 §X 章节号的引用不能批量指向总览(章节不存在 = 断链),必须按章节映射(§3 IC→02-ic-feedback、§4 可编译性→03-compilability 等);**④ 子代理拍板 + 用户裁决**——子代理保守(YAGNI)+ 用户偏激进(预设接口零成本)是健康协作:本轮 AST 协议子代理选 ① 用户采纳;profile 归属子代理选 (B) 用户改 (B+C) 嵌套预设 (C) 接口避免事后改接口。**promotion 候选**:教训 2(Bash heredoc 兜底)+ 教训 3(章节映射表)候选进 [[multi-doc-drafting]] guide,首次样本暂留观察。**触发场景**:做大型文档起草扩展 / 子代理 Write 卡死 / 单文件 → 子目录拆分 / 多人协作下的不确定决策收口时看这篇。

---

## 设计文档集路由(llmdoc 之外的源文档)

- **入口**:`docs/design/architecture.md` §0 是文档集地图(包布局/组件图/tier 映射);`docs/design/p1-interpreter/00-overview.md` 是 P1 施工计划与**跨文档定稿决策速查**(§4)。
- **战略层**:`docs/design/roadmap.md`(引用时用 `docs/design/roadmap.md` (§N))。
- **验收口径**:`docs/design/p1-interpreter/12-testing-difftest.md` §10 是 26 条验收口径总表(所有「待定口径」的收口点;评审轮新增第 26 条 ColInt64,勿引用旧版 25 条说法)。
- **实现现状**:`docs/design/p1-interpreter/implementation-progress.md`——P1 里程碑进度、验收数字、收尾轮落地表与**设计文档对账表**(原「已知简化」清单已在收尾轮全部落地;值栈/CallInfo arena 化等 P3 迁移留口见对账表)。**问「实现到哪了、实现形态与设计文档的差异」看这篇。**
- **工程化机制**:`docs/design/engineering.md`——Git hooks(三件套含 commit-msg 强制 `type(scope):`)/CI workflows(ci + nightly-diff-fuzz 自动开 issue + nightly-benchmark)/Makefile 唯一任务入口/`-race` 硬门禁/oracle 供给(lua5.1=5.1.5 校验 fail-fast)/M0 工程地基里程碑。与 12 的分工:12 管「测什么」,engineering 管「机制怎么搭」。其中 nightly-diff-fuzz 已落地(`.github/workflows/nightly-diff-fuzz.yml`,triage 内联非独立脚本,差异对账见 doc-gaps);nightly-benchmark 未建。
- llmdoc 不搬运设计文档内容,只做压缩与路由;深入实现细节一律回源文档。
