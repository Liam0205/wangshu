# 累积偏移审计 + fuzz wall-clock 与嵌入式 hardening 轮

- **日期**:2026-06-13
- **任务类型**:增量交付到第 N 轮后的累积偏移审计 + CI flake 排查顺手抓真 bug

## 任务

提交区间 `bb1e9a8..701b1a4`,三块:

**A 群** issue #5/#6 公共面缺口轮 3(`4f855d2..59606d7`)——一样的工作流第三轮,过程
教训独立沉淀在 [[issue56-api-gap-round-3]](本反思**不重复**,仅作横向锚点引用)。

**B 群:累积偏移审计与收口**(`e2f4340 + d5acf6d`)。v0.1.0 → HEAD 区间 7 项偏移
审计:**4 项立刻收口**(A1 per-item godoc 加性能档位指针 / B1 `chargeStep → preempt`
改名 / B2 baseline 接根升 reference 不变式 / B5 `HideFileLoaders` 名实对账)、**4 项
P2+ 入档**(A2 drop-in 绑定漂移监控 / B3 host closure 公共面对称缺口 / B4 baseline
非字符串 key 静默跳过 / B7 State 字段并发模型分级,入 doc-gaps)。**审计判断:无硬
违设**(三条非目标铁线均未触线),软偏移集中在 per-item 公共面比例膨胀但 godoc 缺
性能档位指针、wangshu 越来越像 gopher-lua-clone 风险。

**C 群:fuzz wall-clock false alarm + stdlib hardening**(`66dd011 + 701b1a4`)。
`gh run 27414570482` 失败根因 `FuzzCompileRun` seed 含 `while true do end` / `for
i=1,1e9 do end` 靠 `SetStepBudget(1<<20)` 兜底,但 fuzz 框架 `-fuzztime 30s` 内部
`context.WithTimeout`,wall-clock 与 budget 量级近,CI runner 慢一点撞 deadline。
seed 1e9→1e5 + `engineering.md §1.1` 立纪律(`66dd011`)。**关键转折**:1e6→1e5
重跑时 fuzz 引擎抓到新 corpus `2abea9243c4e4b41`(`string.rep("k", 1e14)`)——真
bug,触发 Go runtime fatal "out of memory",`defer recover` 兜不住,违反「嵌入式
VM 宿主进程不可崩」承诺([09-errors-pcall](../../../docs/design/p1-interpreter/09-errors-pcall.md)
§11)。stdlib 同源 OOM 点统一加 1 GiB / 1<<24 fail-fast,`12-testing-difftest.md §4.9`
立「嵌入式 hardening 阈值」纪律(`701b1a4`)。附:`make all` 默认扩面(`44dc6f8`)
工程纪律选择,文末短评。

## 预期 vs 实际

- 预期 1:三轮接入后做累积偏移审计是直觉行为,结果应是「无硬违设、几项软偏移」。
  实际:结果与预期一致,**但审计本身长出方法论维度教训**——尺子是「设计承诺源」
  而不是直觉,主导判断比子代理汇总更准(累积偏移是判断题不是搜集题)。
- 预期 2:CI flake 修一改完就 commit。实际:**flake 修复行为 = 覆盖路径调整 = fuzz
  新一轮探索**,1e5 紧边界 mutate 距离更短,引擎抓到真 OOM bug——「真 bug 由 flake
  排查顺手抓出」不是巧合而是机制。

## 教训(每条首句为「下次什么场景会触发」)

### 1. 「累积偏移审计」纪律——单一驱动源接入到第 N 轮后必做

**触发场景**:同一外部驱动源(本期 pineapple)接入若干轮后(本期 6 个 issue 跨 3
轮),每轮孤立看的「合理」叠在一起可能滑离最初设计承诺,应主动做累积偏移审计。

**工作流五步**:① 取出**设计承诺源**——[[design-premises]] 四组前提 + [[project
-overview]] §6 非目标三条 + [[evolution-roadmap]] drop-in 定位段;② 把每轮 commit
对应的公共 API 改动列清单(本期 7 个改动);③ **维度 A 检查是否违设**——per-item
公共面比例膨胀 / 是否踩三条非目标 / drop-in 绑定漂移;④ **维度 B 检查实现别扭**
——命名不贴语义 / 机制重复 / 公共面对称破 / 名实不符;⑤ 每项按严重度分:本轮
立刻收 / P2+ 入档 / 可接受。

**关键认识**:审计判断的尺子是**设计承诺源**而不是直觉。本期 7 项偏移**无硬违设**
——但软偏移(per-item godoc 缺性能档位指针、`chargeStep` 命名不贴 preempt 语义、
`HideFileLoaders` 名实不符)若不审计会一直累积。

**横向锚点**:与官方套轮「快路径家族审计」([[official-suite-perf-round]] 做对了 1)
同**「同族审计」纪律家族**——一处变更必扫同族:性能优化扫同族快路径(同一形式多处
分布);**本条是「公共 API 演进扫同族」**(同一驱动源累积接入)。两者都是「孤立看
合理、叠在一起滑」类问题的防线。

### 2. 累积偏移审计的具体「完成手法」

**触发场景**:打算做累积偏移审计但不知道从哪下手。

**具体动作**:① **让自己写审计而不是子代理**——本期 Explore agent 临时不可用倒逼
出来,但发现主导判断比汇总更准(累积偏移是判断题,需拿设计前提逐项裁,不是搜集题);
② 直接 grep 关键函数 + Read 关键文件 + 看历史 commit message,不依赖 agent 系统
调研;③ **报告分维度** A(违设)/ B(别扭),每条带文件/行/commit 锚点 + 严重度
(重/中/轻);④ **单独留一节「没有违设、看似别扭其实是正解」**——防审计偏负面
(如 baseline 单字符串 key 是「stdlib 与宿主自定义全局都是 string」事实下的合理
裁口,不是 bug)。

**落点三档**:**本轮立刻收**(便宜立竿见影:godoc 增补、改名、文档对齐)/ **P2+
入档**(需触达对应代码才顺手收:公共面对称、并发模型分级,P2 接 wazero 时大概率
触达 §1/§10/§8 三段设计文档)/ **建项目纪律**(本期新提出「单一驱动源接入轮数
上限」候选 A2,P2+ 接入第二宿主时验证)。

### 3. 测试机制依赖底层框架的「时间维度」假设——fuzz wall-clock vs budget

**触发场景**:用底层兜底机制(`SetStepBudget` / 内存上限 / 步进配额)写测试时。

`SetStepBudget(1<<20)` 是**指令数兜底**,fuzz 框架 `-fuzztime 30s` 是 **wall-clock
兜底**——两套机制不同步。当 budget 兜住的 wall-clock 量级接近 fuzztime
(100ms-1s vs 30s),CI runner 慢一点就 false alarm(fuzz 框架 cancel ctx →
`"context deadline exceeded"`)。Go fuzz 框架不区分「无限循环 seed 撞 deadline」
与「真 hang」。

**正解**:seed 上限改到 budget 兜住的 wall-clock 量级**远低于** fuzztime(`1e5` 在
`SetStepBudget(1<<20)` 下稳定毫秒级触发;**`1e6` 在 fuzz 引擎变体下仍观察到「0/sec
拖尾」**,保守取 `1e5`)。被破除的「测真无限循环不挂宿主」语义由 fuzz 引擎随机
变体自身覆盖——seed 是入口,引擎会生成远超 budget 的循环变体。

**教训扩展**:**测试触发条件与底层兜底机制的量级关系是测试设计的关键维度**,不
仅是 fuzz——任何「靠超时/配额兜底」的测试都要审计上下游量级。与「指令计数不能
替代 wall-clock 事件触发」(issue #4 `SetContext` 的根本动机)对偶,同一原理:
**量级层不能跨界假设**。

### 4. 「真 bug 由 flake 排查顺手抓出」——保留诊断动作的副作用价值

**触发场景**:排查 flake 时若顺手做了「让覆盖路径变化」的动作(改 seed / 改阈值
/ 改采样率),flake 修了但**新覆盖路径可能抓到真 bug**——别立刻 commit,先观察
新覆盖。

本期实例:为修 fuzz false alarm 把 seed 改 1e5(更紧的边界),fuzz 引擎在新 seed
周围探索时抓到 `string.rep("k", 1e14)` 真 OOM 漏洞(corpus `2abea9243c4e4b41`)——
这个 bug 在 1e9 seed 下 fuzz 也潜在能抓到,但 1e5 边界让 mutate 距离更短更易命中。

**关键认识**:这种「顺手抓 bug」不是巧合而是机制——**fuzz 引擎围绕 seed 做覆盖
驱动 mutate,seed 变化 = 探索方向变化**。修 flake 时应预期可能抓到新 bug,准备好
对应工作流(不阻塞当下 flake 修复 commit,新 bug 独立追)。本期处理:flake 修单
提 `66dd011`,真 bug 修单提 `701b1a4` + corpus 入库 + 设计文档立纪律,两个 commit
时间戳间隔 ~21 秒但独立单域——符合 [[public-api-incremental-delivery]] 第 3 条
「单域物理隔离」纪律。

### 5. 「嵌入式 hardening 阈值」类——与对位差异的特殊关系

**触发场景**:开发对位某个 backend(本期 PUC 5.1.5 / gopher-lua)的功能时,对位
backend 自身的「不防御」行为与项目「嵌入式 VM 宿主进程不可崩」承诺([09-errors
-pcall](../../../docs/design/p1-interpreter/09-errors-pcall.md) §11)冲突。

**底线优先级**:**宿主进程不可崩 > 与对位 backend 字节一致**。当对位自身不防御
某类输入会触发 **Go runtime fatal**(`out of memory` / stack overflow,`defer
recover` 都兜不住),我们必须主动 fail-fast 偏离对位行为。这**不是豁免**(豁免是
「合法的不比对」),而是**「嵌入式 hardening 阈值」类主动偏离**——commit message
与 godoc 必须写明背景。

**完成三件套**(`docs/design/p1-interpreter/12-testing-difftest.md §4.9`):
① **阈值口径**——分配类 1 GiB(`string.rep len*n > 1<<30`、`string.format
width/precision > 1<<30`)、循环类 1<<24(`table.concat j-i > 1<<24`);② **引入
条件三条**——不可恢复 runtime 崩溃 / fail-fast 返 Lua 错误(可被 pcall 兜住)/
commit message 与 godoc 写明背景;③ **仅性能/效率差异不触发 hardening**(避免
「我觉得太慢」类无原则偏离对位)。

**与既有教训关系**:与长稳轮「内存复用配套清单」纪律([[longevity-review-fix-round]]
做对了 4)同属「**承诺优先级冲突时的明确裁判机制**」家族——前者「内存复用便利性
vs 根管理一致性」(后者优先);本条「与对位字节一致 vs 嵌入式宿主不可崩」(后者
优先)。共同模式:**底线优先级写入设计文档 + 配套清单 + 触发条件**。

## 附带短评:`make all` 默认扩面(`44dc6f8`)

用户手动扩 `make all`:三件套 → 七件套,代价从秒级到 ~2-3 分钟级。本地一行命令更
充分检测、与 CI 门禁口径对齐,但日常小改动也吃这个时间——工程纪律选择,不算反思
教训。**对账缺口**:`engineering.md §1` Makefile 代码块仍写「all: fmt lint test」
三件套,与现状不符。

## 缺失的文档或信号

- 「累积偏移审计」工作流无 guide——本期首次完整执行,五步模式清晰,样本数 1;
- 「fuzz seed 量级与 fuzztime 关系」纪律已落 `engineering.md §1.1`,但「测试机制
  依赖底层框架的时间维度假设」更普适认识只在反思里,未提炼;
- 「真 bug 由 flake 排查顺手抓出」机制认识只在本反思,无独立落点;
- 「嵌入式 hardening 阈值」类已落 §4.9,但**与豁免注册表的边界关系**(hardening=
  主动偏离对位、豁免=合法不比对)在 [[glossary]] 无对照条目,新读者可能混淆。

## Promotion 评估

### Q1:教训 1+2 是否立项 guide「累积偏移审计工作流」?

**答:暂留 memory 观察,样本数 1**。理由:① 样本不足——[[public-api-incremental
-delivery]] guide 立项是两轮样本后,本期首次完整执行;② 触发频率低——单一驱动源到
「触发审计」轮数门槛(候选 N=4)很高,P2+ 接入第二宿主是再次机会;③ 横向锚点可
补偿——可在 [[official-suite-perf-round]] 「快路径家族审计」做对了节添加交叉
指引。**待复现信号**:P2+ 第二宿主接入若再走完五步,立项 `guides/cumulative-drift
-audit.md`,与 [[public-api-incremental-delivery]] 并列(前者横切多轮,后者纵切单轮)。

### Q2:教训 3+4 是否在 engineering.md 立纪律?

**答:教训 3 已落 §1.1,更普适表述与教训 4 暂留 memory**。教训 3 fuzz seed 纪律
已落 `engineering.md §1.1`(`66dd011`)无须再升;**更普适表述「测试触发条件与底层
兜底机制量级关系」**观察 P2+ 是否复现(如配额类 / 时间窗口类测试再次踩一样的坑),
复现后可升 `engineering.md §1` 总则或 `12-testing-difftest.md` 测试设计纪律节。
教训 4「真 bug 由 flake 排查顺手抓出」本质是 fuzz 引擎工作机制的工程含义,不属于
「门禁机制」范畴,更接近测试设计哲学([[official-suite-perf-round]] 做对了 3
「profile 先行 + benchmark 否决门」同族);留 memory,P2+ JIT trace 录制再次出现
类似事件再提炼为「诊断动作副作用价值」guide。

### Q3:教训 5 是否升 reference 不变式?

**答:已落 `12-testing-difftest.md §4.9` 设计文档,不重复升 llmdoc reference**。
§4.9 已写阈值口径、引入条件三条、首次踩坑入档,与 §10 验收口径总表并列;
[[embedding-contract]] 不变式条款应聚焦「宿主嵌入面」,「嵌入式 hardening 阈值」
面向 stdlib 实现者而非宿主使用者,落点选择正确。**可在 [[glossary]] 加一条术语
对照**:区分「嵌入式 hardening 阈值」(主动偏离对位)vs「豁免」(合法不比对)vs
「设计差异」(语义有意不同)三类对位差异,防新读者混淆——下一轮 recorder 小动作。

## 后续行动

- recorder 在 [[glossary]] 增补「嵌入式 hardening 阈值 vs 豁免 vs 设计差异」对位
  差异三类术语对照(Q3);
- recorder 修 `docs/design/engineering.md §1` Makefile 代码块 `all: fmt lint test`
  → `all: fmt lint test fuzz conformance difftest bench-test`,与现状(`44dc6f8`)
  对齐(对账缺口);
- 累积偏移审计五步暂留本反思,P2+ 第二宿主接入若复现可立项 guide(Q1);「测试触
  发条件与底层兜底机制量级关系」更普适表述待 P2+ 复现样本(Q2 教训 3 扩展);
- P2+ 新增 stdlib 函数若涉及「`len * n`」「`width/precision`」「循环计数由脚本控
  制」三类形式,前置约束:套用 §4.9 阈值口径(分配类 1 GiB / 循环类 1<<24)。
