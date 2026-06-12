# issue #2 / #3 / #4 公共面 API 缺口轮 2(Table / HideFileLoaders / Context)

- **日期**:2026-06-12
- **任务类型**:issue 驱动的公共面 API 增量交付(pineapple wangshu backend 接入触发,
  同款工作流第二轮)

## 任务

issue #2/#3/#4 来自 pineapple wangshu backend 接入,三件 gap:
- **#2** 公共面 `Value` sum-type 缺 Table kind → 无法以宿主一次跨界投喂 mixed-type
  `[]any` list 进 VM(pineapple common-mode 列内核形态);
- **#3** `Options{AllowFileLoad:false}` 默认 loadfile 失败返回 `(nil, errmsg)`(PUC
  5.1.5 对位),与 gopher-lua 嵌入式 sandbox 传统(从 globals 刮除致脚本调用 fatal)
  不一致;
- **#4** 客户端取消(HTTP timeout / ctx.Cancel)无法穿透 VM,`SetStepBudget` 按指令
  计数不能替代 wall-clock 事件触发。

提交区间 `v0.1.1..HEAD`(5 个 commit):`2b55e11` Table / `09fdd72` HideFileLoaders /
`27b4f2e` SetContext / `584db8e` doc 对账 / `bb1e9a8` 评审跟进(godoc「三件套」→
「四件套」+ Run/Call 返回值生命期段)。

## 预期 vs 实际

- 预期:同款工作流第二轮——issue #1 已经把「公共 API 增量交付」纪律走了一遍
  (设计承诺源回看 / GC 根机制 / 单域物理隔离 / 错误消息稳定语义),复用同一套
  模式落地三件即可。
- 实际:四条核心纪律在本期重复触发并验证可复用(尤其 GC 根机制——issue #2 kTable
  与 issue #1 kFunction 用同款 pin 表,零额外接根工作,验证「公共 first-class
  GCRef-bearing value 必须接根」是稳定不变式而非一次性手法),**额外**长出四条
  新维度教训(下文展开):评审反馈跨域复用 / issue 范围扩张顺手收口 / 对位测试
  断言先 grep oracle / internal 接口签名避免反向依赖标准库。

## 教训(每条首句为「下次什么场景会触发」)

### 1. 评审反馈的跨域复用——「稳定语义」纪律的另一面是「行为变更显式」

**触发场景**:任何动了公共 API 返回值映射、错误措辞、行为开关 default 的提交,
review/合入前先想「这是否相对前一版静默改了行为/语义」。

issue #1 评审抓的是「报错带 `(M12)` 里程碑编号是内部信息渗漏」(教训 4 的稳定
语义);本期评审又被抓「`Run/Call` 返回路径升级到 `fromInnerWithPin` 是相对
v0.1.0 的静默行为变更,godoc 应给迁移提示」——两次反馈是**同一纪律的两侧**:
- 错误消息稳定 = **错误**不该泄漏内部演进(issue #1 教训 4,负面/不该说的说了);
- 行为变更显式 = **行为**演进时必须显式告知用户(本期评审,正面/该说的没说)。

底层原理一致:**公共 API 演进时,文档与代码必须同步显式标注 N→N+1 的差异**。
处理动作:godoc 加「**v0.1.1 → v0.1.2 行为变更**」段说明返回值映射扩面、给
迁移提示(`Release()` 调用);`GetGlobal` godoc 把「table 不暴露」更新为「自
v0.1.2 经 `AsTable` 暴露」;`Program.Call/State.Call` godoc 指针指回 Run 段统一
信源。两条合起来覆盖「公共 API 演进对外披露纪律」的两面,均应纳入未来公共 API
增量交付 guide。

### 2. issue 范围扩张顺手收口——一类积极的范围越界

**触发场景**:落地新 issue 时发现「上轮裁口的某条恰好阻挡本轮 spec 隐含期望」。

issue #2 用户字面只要求「公共 Table API」,但 `fromInner` 对 table/function 返回值
映射 Nil 是 issue #1 时刻的范围裁口(那时只允许 function 经 `GetGlobal` 取出)。
本期把 `fromInner → fromInnerWithPin` 升级时,**顺手把 issue #1 留的「Run/Call
返回 table 不可读」口也收了**——同步公共面复合值暴露逻辑,代价为零(本来 kTable
桥接要写)且对齐 issue #2 spec 隐含期望(`State.Call` 返回 table 应该工作)。

**关键认识**:issue 落地时若发现「上轮裁口的某条恰好阻挡本轮承诺」,**先把裁口
收掉再做本轮,比留个 trick 跨 issue 强 100 倍**。把口收掉是 O(1) 桥接改面,
跨 issue 留 trick 是 O(n) 特殊路径累积,后者迟早形成「公共 API 行为方阵 vs 内部
桥接 path 不一一对应」的反向不变式洞。

**风险与处理**:范围扩张必须在 commit message 显式标注,否则 review 会困惑
「为什么 issue #2 改了 Run 返回路径」。本期 `2b55e11` commit message 显式标了
「Program.Run/Call 与 State.Call 返回路径从 fromInner 升级到 fromInnerWithPin」,
配合评审跟进 commit `bb1e9a8` godoc 加「行为变更段」,两层信号让用户与未来
review 都能定位变化。**与官方套轮「快路径家族审计」同族**——一处变更必扫同族路径
(优化要扫快路径全家族;裁口要扫同族裁口全家族)。

### 3. 对位测试断言文本先 grep oracle——一行最小代价规避一轮跑测

**触发场景**:写「对位 gopher-lua / 对位 PUC 5.1 / 对位某官方实现」类测试时。

issue #3 测试初版断言 `attempt to call a nil value`(我以为这是 PUC 5.1 标准
nil-call 文本),实跑发现实际是 `attempt to call global 'X' (a nil value)`——
5.1 给的 global 调用路径会带变量名前缀(`luaG_typeerror` 走 `varinfo` 分支拿出
全局名)。修改测试断言反而验证了「我们的错误措辞确实跟官方完全对齐」——测试
反馈即是行为对照证据。代价对账:先 grep oracle 多一步,但少一轮全量跑测。

**教训**:对位类测试(声称「与 X 字节一致」)的断言文本应**先 grep oracle 实现
或跑一次得到实际输出**,不要凭印象写。本期最终留下的注释 `// 脚本调用 →
"attempt to call a nil value"`(`sandbox_test.go:20`)是初稿历史,实际断言已修正
为带 global 前缀的完整文本——对位证据反而是测试本身。**与官方套轮「归因诚实」
同族**——本条是该纪律在「初版编写阶段」的前置化:与其等跑红后归因,不如编写
阶段就拿 oracle 实际文本写断言。

### 4. internal 接口签名避免反向依赖标准库——`func() error` 而非 `context.Context`

**触发场景**:internal 包要接受一个「外部世界」对象(context / io / timer / net
等)时——尤其是当 internal 当前包依赖图不包含该标准库或第三方包时。

issue #4 ctx 检查搭进 `chargeStep` 时,`internal/crescent` 包当前不依赖 `context`
标准库(全靠门面注入,内部抢占只看 step budget 与 step counter)。我刻意把
internal 接口设计成 **`SetCancelHook(fn func() error)`** 而不是 **`SetContext
(ctx context.Context)`**,把 `context` 依赖留在门面层(`st.core.SetCancelHook
(ctx.Err)`),internal 只看 `func() error` 抽象签名(返回 nil = 未取消,返回 err
= 取消并传 err 到错误消息)。

**关键认识**:internal 包的依赖图是 P3+ 架构演进的重要约束面——
- 门面层吃下与外部世界的接口形状(context / io / net / time / 用户自定义类型);
- internal 只看抽象签名(`func() error` / `io.Reader` / `chan struct{}` 等)。

收益:① P3+ JIT 同款抢占机制可以**直接共用此 hook 无需引入 context 包**;
② internal 测试不需要造 ctx,直接 `SetCancelHook(func() error { return errSentinel })`
即测;③ 未来若引入非 context 的取消源(信号、自定义 done chan),门面层加一种
映射即可,internal 零改动。**反例**:直接给 internal 写 `SetContext(ctx
context.Context)` 则被绑死在 `context` 包语义,P3+ 引入新执行层时还要为每层重复
造 ctx 注入。

**与既有教训的关系**:与「单域物理隔离」(issue #1 教训 3)同属「物理分层」
纪律族,但触点是**包依赖分层**而非文件分层——前者是 commit 级单域,后者是
架构级单域,底层动作相同:中间用抽象签名解耦。

## 缺失的文档或信号

- 公共 API 增量交付**仍然没有 guide**——issue #1 反思已建议立项,本期同款工作流
  走完第二轮验证可复用,样本充分(详见 Promotion 报告)。
- `reference/embedding-contract.md` 已同步 per-item drop-in 子集状态,「宿主长持
  GCRef 必须接 GC 根」契约在 issue #1 反思建议回填(doc-gaps 既有清单),本期
  kTable 再次验证该不变式适用范围超出 kFunction,**应升级为契约级条款**(详见
  Promotion 报告)。
- internal 包依赖分层纪律(教训 4)目前只有 `SetCancelHook` 一处样本,可观察
  P2+(wazero / IC 重构 / arena 化值栈)是否复现后再决定是否升 reference。
- `bb1e9a8` 评审跟进的「行为变更显式段」是 godoc 内出现的首次「v0.x → v0.y
  行为变更」标注,可作为后续公共 API 版本迁移 godoc 模板参考——但目前还不是
  独立文档形态。

## Promotion 报告

### Q1:是否建议立项「公共 API 增量交付工作流」guide?

**答:强烈建议立项**。本期是同款工作流走完第二轮,样本充分。聚合教训清单
(commit / reflection 锚点):

| 维度 | commit / reflection 锚点 |
|---|---|
| 设计承诺源回看 | `87031c2` / `2026-06-12-issue1-api-gap-round.md` §教训 1 |
| 公共面 first-class GCRef 接 GC 根(契约级,见 Q2)| `87031c2`(kFunction)+ `2b55e11`(kTable 复用 pin 表)/ issue #1 反思 §教训 2 + 本反思 §做对了什么 1 |
| 单域物理隔离手法 | `87031c2 + cb6e1ae + 5d8d2c2` / issue #1 反思 §教训 3 |
| 错误消息稳定语义(内部信息不渗漏)| `031ec06`(`"(M12)"` → 行为语义)/ issue #1 反思 §教训 4 |
| 公共 API 行为变更必须显式标注 | `bb1e9a8`(godoc「v0.1.1 → v0.1.2」段)/ 本反思 §教训 1 |
| 上轮裁口与本轮 spec 冲突时现场收掉 | `2b55e11`(`fromInner → fromInnerWithPin` 顺手收 issue #1 口)/ 本反思 §教训 2 |
| 对位测试断言先 grep oracle | `09fdd72`(`sandbox_test.go` 断言修正)/ 本反思 §教训 3 |
| internal 包依赖分层 | `27b4f2e`(`SetCancelHook` 中性签名)/ 本反思 §教训 4 |

**判断依据**:
- **样本充分**:8 条围绕「公共 API 演进」的纪律分布在 issue #1 + issue #2/#3/#4
  两批共 10 个 commit、两次评审跟进(`031ec06` + `bb1e9a8`)、两轮反思,覆盖
  「issue 接到 → 设计承诺三角验证 → 物理隔离 → GC 根配套 → 错误消息 → 行为变更
  披露 → 跨 issue 范围管理 → 对位测试 → 包依赖分层」全流程节点。
- **可复用性高**:P2+ 接 wazero、扩展 ABI、补 stdlib、IC 重构、给 Program 加字段
  时都会反复走同款流程。
- **与既有 guide 平行**:与官方套轮「性能优化工作流」guide 候选同级(一篇覆盖
  一个工作流),可同批立项。聚合形态明确,不是发散贴士集合,围绕「公共 API
  从 issue 到合入」一条主线。

### Q2:是否建议把「公共 first-class GCRef-bearing value 必须接 GC 根」做成 `reference/embedding-contract.md` 的不变式条款?

**答:建议升级为 reference 契约条款**。

**判断依据**:
- **两次样本验证**:issue #1 kFunction 与 issue #2 kTable 均用同款 `pinnedRefs +
  freePins + visitExtraRefs` 通道接根,**机制完全复用零额外成本**——证明这不是
  「kFunction 特殊路径」也不是「kTable 特殊路径」,而是**「公共面任何 first-class
  GCRef-bearing Value kind」的通用不变式**。
- **契约级硬规则适合 reference**:guide 体例适合工作流纪律(可有例外、有判断、
  有场景),reference 适合契约级硬规则(无例外、机制级保证)。本条是「任何
  公共面 GCRef value kind 必须接根」的不变式,属于硬规则。
- **未来扩展面前置约束**:P2+ 若新增公共 first-class kind(userdata / coroutine
  / cdata),此契约条款是开发起点的硬约束;留在 guide 里容易被新场景作者绕过,
  放 reference 是机制级把关。
- **issue #1 反思已建议并纳入 doc-gaps**;本期 kTable 复用 pin 表验证机制可复用,
  **正是把「建议条款」升级为「契约条款」的时机**。

**与 guide 不重叠**:guide 讲「接 issue 时怎么判断该接根、怎么验证 pin 表落到
visitExtraRefs」(工作流维度);reference 讲「公共 Value sum-type 任何 GCRef-bearing
kind 必须可被 GC visitor 触达」(契约维度)。

**落点提议**:`reference/embedding-contract.md` 新增一节(或并入 §6 句柄表现有
节),引 `pinnedRefs / freePins / visitExtraRefs` 实现为参照,与「内存复用配套
清单」(长稳轮)的对偶面并列——后者:VM 内部 freelist 复用要求根全审计;本条:
公共 API 暴露的长持 GCRef 要求根机制。

## 后续行动

- recorder 立项「公共 API 增量交付工作流」guide(教训 8 条聚合,参 Q1 锚点表)。
- recorder 把「公共 first-class GCRef-bearing value 必须接 GC 根」从「doc-gaps
  回填待办」升级为 `reference/embedding-contract.md` 契约级条款(参 Q2 落点提议)。
- P2+ 任何新增公共 first-class GCRef kind(userdata / coroutine / cdata 等)
  实现前,先核对 pin 表机制是否覆盖该 kind;若新增执行层(JIT / wazero),
  cancel hook 注入面复用 `SetCancelHook(func() error)` 中性签名。
- 公共 API 后续提交评审检查清单加一条「相对前版有无静默行为变更?godoc 是否
  显式标注 N→N+1 差异?」——与既有「错误消息不带内部信息」并列。
