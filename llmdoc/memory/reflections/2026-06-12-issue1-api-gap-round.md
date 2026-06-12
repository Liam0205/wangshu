# issue #1 公共面 API 缺口轮(per-item drop-in 闭环 + Register/Module + 公共 HostFn)

- **日期**:2026-06-12
- **任务类型**:issue 驱动的公共面 API 增量交付(pineapple `transform_by_lua` 真实接入触发)

## 任务

issue #1 字面只提补 `SetGlobal`/`GetGlobal` 标量四类型(Nil/Bool/Number/String),
并显式声明「表/函数/userdata 不暴露」。实际驱动场景是 pineapple `transform_by_lua`
算子,真实用法为「`GetGlobal("f")` 取出函数 → 循环 `SetGlobal(field, ...)` 注入字段
→ `CallByParam(fn)` 调用」。提交区间 `ac68470..031ec06` 共 5 个提交:
① `d1ff096` build——benchmarks 拆独立子模块、主库零外部依赖(前序工程纪律,本轮
附带短评);② `87031c2` feat——`SetGlobal/GetGlobal/Call` + `Value` 增 `kFunction` +
pin 表把宿主长持的 GCRef 接入 GC 根;③ `cb6e1ae` feat——`Register/RegisterModule` +
公共 `HostFn`(列表风格,非栈机);④ `5d8d2c2` doc——per-item drop-in 子集落地对账;
⑤ `031ec06` chore——评审跟进:host closure 报错稳定化 + Go 端 Call 负测试。
本文只记过程教训,落地明细见 implementation-progress.md。

## 预期 vs 实际

- 预期:issue 字面已框定「不暴露表/函数/userdata」,补四类型标量即可关闭。
- 实际:**字面承诺无法关闭 issue 描述的真实场景**——pineapple 用法需要
  `GetGlobal` 取出 function 与 `Call(fn)`,标量四类型 + 不暴露函数 = drop-in 闭环断。
  从 issue 字面回溯到 `docs/design/p1-interpreter/11-embedding-arena-abi.md` §7.1/§9.1
  + `embedding-contract.md` 设计承诺源,发现完整 per-item 子集 + drop-in 形态对标
  早已承诺,issue 边界与设计承诺、与 issue 自己描述的业务场景三者**互相不自洽**。
  AskUserQuestion 给出三档(issue 字面 / drop-in 最小可用集 / 完整 §7.1 栈机),
  最终落地中间档。

## 做对了什么(可复用模式)

1. **「设计承诺源回看」纪律**(见教训 1):接到表面小的 issue 时,不直接按字面做,
   而是回到设计承诺源验证 issue 自己的边界与「issue 描述的业务场景」是否自洽。
2. **GC 根机制随公共 first-class value 同批落地**(见教训 2):`Value.kFunction` 加
   pin 表(`pinnedRefs` + `freePins` 复用空闲槽,经 `visitExtraRefs` 接根),
   不留 UAF 余地——内存复用类纪律(长稳轮)的对偶面:**句柄持有期由宿主决定时,
   必须有显式 GC 根**。
3. **物理隔离实现「单域提交」**(见教训 3):一锅写完后按文件抽取
   (`register.go` / `global_test.go` / `register_test.go`)再分次 commit,
   比 `git add -p` 选 hunks 更可靠;尤其在 `wangshu.go` 这种已堆积多类 API 的公共
   门面文件里,文件级隔离让 review/revert 都更稳。
4. **稳定语义错误消息**(见教训 4):公共面错误从「M12 不支持」改为
   「host closure cannot be called from Go end; invoke it from Lua side instead」,
   既自解释也与实现状态解耦。

## 教训(每条首句为「下次什么场景会触发」)

### 1. issue 字面 vs 真实场景的差距——「设计承诺源回看」纪律

**触发场景**:接到一个「看起来很小」的 issue,issue 自己框定了某些「不做」的边界
(尤其是「为节制范围」型 issue:补几个标量、不暴露复杂类型、只补单点 API)时。

issue #1 字面补 SetGlobal/GetGlobal 解锁不了 pineapple 真用法:
`L.GetGlobal("f")` 拿不到 function → `L.CallByParam(fn)` 无等价物 → 整个 drop-in
闭环断。issue 提交者**只是没想清整个调用链**——这是 issue 字面边界与 issue 自己
描述的业务场景不自洽的常见根因。

处理动作三步:① 从 issue 字面 → 回到设计承诺源(本例 §7.1/§9.1 与
`embedding-contract` 早就承诺了完整 per-item 子集);② 回到「真实驱动场景」
(pineapple 用法)反推「最小可用集」;③ AskUserQuestion 给出**三档范围**
(issue 字面 / drop-in 最小可用集 / 完整 §7.1 栈机)让用户拍板,而不是擅自扩大或
按字面交付。

**与既有教训的关系**:这是「面向真实驱动场景反推最小可用集」的纪律,与设计评审
轮的「主动盘点不确定决策」同族,但触点不同——评审轮是设计期主动,本条是 issue
执行期被动验证。

### 2. 宿主 Go 长持 GCRef 必须当 GC 根——pin 表机制

**触发场景**:任何让宿主 Go 端**长期持有** GCRef(table / function / userdata 等
垃圾回收对象)的公共 API——即使是 first-class function value 在 `Value` 里持有。

当公共面暴露 first-class function value 给宿主 Go 端持有时,GCRef 必须当 GC 根
遍历,否则 UAF 链条:用户 `GetGlobal("f")` 取出 fn → SetGlobal("f", nil) 覆盖
globals → 下一次 GC 回收槽位 → freelist 复用槽位 → 用户用旧 fn Value 调用 = UAF
或串台。

解法:State 加 `pinnedRefs []GCRef` + `freePins []uint32`(复用空闲槽,与
`hostFnRegistry` 同形态),经 `visitExtraRefs` 接 GC 根(已有 `ExtraRefs` 扩展点,
**无需改 collector 接口**);公共 Value 加 `kFunction` kind 持 `*State + pinIdx`,
`Release()` 显式释放(可选——不释放仅累积小量,致命的是「没接根」)。这是
11 §6 句柄表设计的极简落地。

**关键认识**:即使 shadow stack(LIFO)看似可用,公共 API 的持有期是任意的不是
LIFO——这是长稳轮「内存复用配套清单」的对偶面:**长稳轮谈的是 freelist 内部复用
要求根全审计,本条谈的是公共 API 暴露给宿主的长持引用要求根机制**,两面同根:
任何让某对象在「VM 自己的生命周期管理之外」被持有,都必须显式接根。

### 3. 单域物理隔离比 `git add -p` 可靠——「单域提交」纪律的落地手法

**触发场景**:遇到「该单域但已经一锅写完」的状态——尤其改动落在公共门面文件
(`wangshu.go` 这类已堆积多类 API 的文件)里时。

本期一锅写完全部代码后才想起单域纪律(官方套轮反思已两次预告)。落地手法:
**先做物理重组(抽文件)再分次 commit**——把 `wangshu.go` 中 Register/RegisterModule
物理抽到独立文件 `register.go`,测试拆 `global_test.go` / `register_test.go`,然后
按文件 `git add` 分次提交(SetGlobal/GetGlobal/Call 一次,Register 一次,文档一次)。

**为什么不用 `git add -p`**:hunk 级拆分在公共门面文件里会留下「同文件半成品」
的中间提交(同一文件里某些 hunks 在 commit A 提了、某些等到 commit B,语义上
难以独立 review);文件级隔离让每个 commit 都是「一个或多个完整文件的变更」,
review/revert/cherry-pick 都更稳。

**与官方套轮的关系**:官方套轮反思已记录「单域提交、提频」教训(同轮被提醒两次),
本条是该纪律在「已经一锅写完」状态下的**回收手法**——不要试图用 `git add -p`
拼凑,直接做物理重组。

### 4. 报错信息泄漏内部里程碑编号——稳定语义纪律

**触发场景**:写公共面错误消息(即使是经过 internal 层透传上来的)时,或评审/
review 看到 `(M\d+)` / commit hash / 内部模块名等内部信息出现在用户可见错误里时。

评审发现 `State.Call` 拒绝 host closure 时报 `"(M12)"` 里程碑编号——这是**内部
信息渗漏**,且未来真做了支持还得改一次错误措辞(消息与实现状态耦合)。改为
稳定语义:「host closure cannot be called from Go end; invoke it from Lua side
instead」——既自解释也跟实现状态解耦,实现演进时不影响消息。一并修了 `frame.go`
不可达路径的同款措辞。

**规律**:公共面错误消息用「行为语义」措辞,不带:① 内部里程碑编号(M\d+);
② 内部 commit hash;③ 内部模块/包名(除非该名本身是公共 API 一部分);
④ 「暂未支持」「TODO」「FIXME」类时态承诺(实现状态变化要追改一次消息)。

**与官方套轮 errors.lua 负断言的关系**:官方套轮反思讲的是「错误消息**该有什么
不该有什么**由官方测试套用负断言锁定」,本条讲的是「错误消息**不该带什么内部
信息**」——两者都是「消息内容质量」维度,但前者由测试套门禁,后者目前只能靠
评审/review 抓。

## 附带短评:公共主库零外部依赖纪律(`d1ff096`)

**触发场景**:嵌入式 VM / 库类项目主 `go.mod` 引入了「test-only 或 benchmark-only」
的外部依赖时。

`gopher-lua` 仅作 benchmark 对照用,但放在主 `go.mod` 里会让所有下游模块图被动
带上(模块裁剪不下载源码,但安全扫描器/审计可见,作为公共依赖面是不正确的)。
拆 `benchmarks/` 为独立 Go 子模块(独立 `go.mod`,`replace` 回父目录)。

**关键认识**:嵌入式 VM 这类**被动依赖**特别敏感(被嵌入意味着进入大量下游),
公共主库的 `go.mod` 应做**零外部 require 承诺**;Test-only / benchmark 依赖按
用途拆子模块。这与「主库公共面零依赖」承诺同维度,与本期主线四条教训独立,
但同属「公共面纪律」族。

## 缺失的文档或信号

- 公共 API 增量交付无工作流文档:① issue 字面 vs 设计承诺源 vs 真实场景三角验证;
  ② 公共面暴露 first-class GCRef 必备 GC 根机制;③ 单域提交的物理重组手法;
  ④ 错误消息稳定语义纪律——四条同属「公共 API 增量交付」一个工作流,样本已够。
- `reference/embedding-contract.md` 已同步 per-item drop-in 子集状态,但「宿主长持
  GCRef 必须接根」的设计契约(11 §6 句柄表)未在该篇显式陈述,只在实现里有 pin 表;
  使用者(尤其后续 P2+ 扩展 API 表面者)难以从契约文档看到这条不变式。
- engineering.md 工程纪律未涵盖「公共主库零外部 require」承诺——benchmarks 子
  模块拆分是现成实例,但承诺本身无挂靠点。

## Promotion 候选

- **`guides/` 候选(强烈建议立项)**:「公共 API 增量交付工作流」,聚合教训
  1+3+4——issue 字面 vs 设计承诺源回看(教训 1)/ 单域物理隔离手法(教训 3)/
  错误消息稳定语义(教训 4)。本期样本充分,且 P2+ 接 wazero、扩展宿主 ABI 面、
  补 stdlib 时都会反复走同一流程,立项收益高。可与官方套轮 Promotion 候选的
  「性能优化工作流」guide 平行立项。
- **回填设计文档(并入 doc-gaps 既有清单,recorder 执行)**:`embedding-contract`
  增补「宿主长持 GCRef 接根契约」条款(教训 2),与既有「内存复用配套清单」
  (长稳轮)的对偶面,引 pin 表实现为反例参照——这条是 API 表面扩展时的不变式,
  须文档显式承诺,不应留在实现注释里。
- **`reference/` 候选(暂留 memory 观察)**:公共面错误消息的「稳定语义」措辞规范
  (不带 M 编号 / commit hash / 模块名 / 时态承诺)——一句话级,先在 memory 观察
  是否复发后再决定升 reference。
- **engineering.md 增补(可选)**:公共主库「零外部 require」承诺与 benchmark/
  test-only 依赖拆子模块手法——一句一例,与既有「-race 硬门禁」「oracle 供给」等
  工程承诺并列。

## 后续行动

- 评估「公共 API 增量交付工作流」guide 立项(教训 1+3+4 聚合),与官方套轮的
  「性能优化工作流」guide 同批考虑。
- recorder 把「宿主长持 GCRef 接根契约」登记进 doc-gaps 回填待办,落入
  `embedding-contract.md` 或 `11-embedding-arena-abi.md` §6 增补。
- 后续 P2+ 任何扩展宿主 ABI 表面 / 新增公共 API 的提交,先过教训 1 的三角验证
  (issue 字面 / 设计承诺源 / 真实驱动场景),三者不自洽时 AskUserQuestion 拍板;
  暴露 first-class GCRef 时核对 pin 表机制是否到位。
- 公共面错误消息评审:grep `\(M\d+\)` / `TODO` / `FIXME` / `暂未` 类措辞做一次性
  清理,后续提交评审中常规检查。
