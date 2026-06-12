# Guide:公共 API 增量交付工作流

> 适用:接到「扩公共 API 表面」的 issue / 需求 / 反馈时——尤其是 issue 字面边界看起来很小、或目标是给 drop-in 候选(gopher-lua / PUC 5.1 / 其它实现)补对位面。
> 来源:`memory/reflections/2026-06-12-issue1-api-gap-round.md`(per-item drop-in 子集 + Register/Module + 公共 HostFn + kFunction)与 `memory/reflections/2026-06-12-issue234-api-gap-round-2.md`(Table / HideFileLoaders / Context,同款工作流第二轮),两轮共 8 条纪律全验证可复用。

## 1. 设计承诺源回看——三角验证

接到「看起来很小」的 issue 时,**不要按字面做**。先做三角验证:① issue 字面边界 ② 设计承诺源(`docs/design/p1-interpreter/11-embedding-arena-abi.md` §7.1/§9.1 + [[embedding-contract]])③ issue 描述的真实业务场景。三者不自洽时(常见根因是 issue 提交者只想到了单点 API 没想清整个调用链),用 AskUserQuestion 给出**三档范围**(issue 字面 / drop-in 最小可用集 / 完整 spec)让用户拍板,不擅自扩大也不按字面交付。

反例:issue #1 字面只补 SetGlobal/GetGlobal 标量,但 pineapple 真用法是「GetGlobal(fn) + CallByParam(fn)」,标量四类型解锁不了真闭环(`87031c2`)。

## 2. 公共面 first-class GCRef-bearing value 必须接 GC 根

机制级硬规则,已升为 [[embedding-contract]] 不变式条款。本节作为工作流落地的 checklist 指针:接到「让宿主长期持有某 GCRef 对象」类 issue(function / table / userdata / coroutine 等)时,**回头核对该 reference 条款**——pin 表(`pinnedRefs` + `freePins` + `visitExtraRefs`)是否覆盖该 kind,Release 是否配对,globals 覆盖 + GC 压力模式下能否复读。本 guide 不重复契约细节。

锚点:`87031c2`(kFunction)/ `2b55e11`(kTable 复用同款 pin 表零额外接根)。

## 3. 单域物理隔离手法——按文件抽取,不靠 `git add -p`

遇到「该单域但已经一锅写完」时,**先做物理重组(抽文件)再分次 commit**,比 `git add -p` 选 hunks 可靠;尤其在 `wangshu.go` 这种已堆积多类 API 的公共门面文件里。手法:把新 API 物理抽到独立文件(`register.go` / `table.go` / `sandbox_test.go` / `context_test.go`),然后按文件 `git add` 分次提交,每个 commit 都是「一个或多个完整文件的变更」。`git add -p` 在公共门面文件里会留下「同文件半成品」中间提交,review/revert 都不稳。

锚点:`87031c2 + cb6e1ae + 5d8d2c2`(issue #1 三件套)/ `2b55e11 + 09fdd72 + 27b4f2e + 584db8e`(issue #2/#3/#4 四件套独立提交)。

## 4. 错误消息稳定语义——不带内部信息

公共面错误措辞**不带**:① 里程碑编号(`(M\d+)`)② commit hash ③ 内部模块/包名(除非该名本身是公共 API 一部分)④ 时态承诺(「暂未支持」「敬请期待」「TODO」「FIXME」)。措辞按「行为语义」写,实现演进时不影响消息。grep `\(M\d+\)` / `TODO` / `FIXME` / `暂未` 做一次性清理。

反例:`031ec06` 把 `"(M12)"` 改为 `"host closure cannot be called from Go end; invoke it from Lua side instead"`——既自解释也与实现状态解耦。

## 5. 行为变更显式标注——错误稳定的对偶面

动了公共 API 返回值映射、错误措辞、行为开关默认值的提交,review 前先自问「相对前版是否静默改了行为/语义?」。若改了,godoc 加「**vN.M.x → vN.M.x+1 行为变更**」段 + 迁移提示。

与第 4 条同纪律两面:错误消息稳定 = **错误**不该泄漏内部演进;行为变更显式 = **行为**演进时必须告知用户。

反例:`Run/Call` 返回路径从 `fromInner` → `fromInnerWithPin` 是静默扩面(table/function 此前映射 Nil,本期可读出),评审抓出后 `bb1e9a8` 补 godoc 行为变更段 + Release 调用提示。

## 6. 范围扩张顺手收口——commit message 必须显式标注

落地新 issue 时若发现「上轮裁口的某条恰好阻挡本轮 spec 隐含期望」,**先把裁口收掉再做本轮**——代价为零(本来就要改桥接面)且对齐 spec 隐含期望;留个 trick 跨 issue 处理是 O(n) 特殊路径累积,迟早形成「公共 API 行为方阵 vs 内部桥接路径不一一对应」的反向不变式洞。

**但**:范围扩张必须在 commit message 显式标注「顺手把 issue #N 留的口收了」否则 review 困惑「为什么 issue #2 改了 Run 返回路径」。

反例:`2b55e11` 把 `fromInner → fromInnerWithPin` 升级,顺手收掉 issue #1 留的「Run/Call 返回 table 不可读」口,commit message 显式标注。

## 7. 对位测试断言文本——先 grep oracle,不凭印象

写「对位 gopher-lua / 对位 PUC 5.1 / 对位某官方实现」类测试时,断言文本应**先 grep oracle 实现或跑一次得到实际输出**,不凭印象写。代价对账:先 grep 多一步,但少一轮全量跑测;且对位证据反而是测试断言本身——若印象与实际不符,修测试 = 验证实现已与官方对齐。

反例:`09fdd72` 测试初版断言 `attempt to call a nil value`,实跑发现 PUC 5.1 给的是 `attempt to call global 'X' (a nil value)`(`luaG_typeerror` 走 `varinfo` 分支带变量名前缀),修测试断言反而验证了我们的措辞与官方完全对齐。

## 8. internal 接口签名避免反向依赖标准库——抽象签名留在门面层

internal 包要接受「外部世界」对象(context / io / timer / net 等)时——尤其是当 internal 当前包依赖图不包含该标准库或第三方包——用**抽象签名**(`func() error` / `io.Reader` / `chan struct{}`)而非具体类型。把具体类型依赖留在门面层注入。

收益:① P3+ 新执行层(wazero / JIT)同款机制可直接共用,无需引入额外包;② internal 测试零外部世界对象,直接造 sentinel 即测;③ 未来若引入非同源取消源(信号 / 自定义 done chan)只改门面映射,internal 零改动。

反例:`27b4f2e` 用 `SetCancelHook(fn func() error)` 而非 `SetContext(ctx context.Context)`,`context` 依赖只在 `wangshu.go` 注入(`st.core.SetCancelHook(ctx.Err)`),`internal/crescent` 保持零标准库非基础包依赖。

## 落点文件参考

- 反思原始样本:
  - `llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md`(纪律 1/2/3/4 首次样本)
  - `llmdoc/memory/reflections/2026-06-12-issue234-api-gap-round-2.md`(纪律 5/6/7/8 新增 + 纪律 1-4 复用验证)
- 契约/不变式:[[embedding-contract]] §公共 first-class GCRef-bearing value 接 GC 根
- 锚点 commit:
  - issue #1 三件套:`87031c2` feat / `cb6e1ae` Register / `5d8d2c2` doc / `031ec06` 评审跟进
  - issue #2/#3/#4 四件套:`2b55e11` Table / `09fdd72` HideFileLoaders / `27b4f2e` SetContext / `584db8e` doc / `bb1e9a8` 评审跟进
- 同族 guide:[[perf-optimization-workflow]]——「快路径家族审计」(优化扫同族)与本 guide 「范围扩张顺手收口」(裁口扫同族)是同一纪律在不同维度的两个落地。
