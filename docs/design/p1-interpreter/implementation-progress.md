# P1 实现进度

> 状态:**P1 全里程碑 M0-M14 + 收尾轮(原"已知简化"清单)已完成**(2026-06-12)。
> 本文记录每个完成里程碑的产出、验收结果,以及与设计文档的对账。
>
> 设计文档参考路径见 [00-overview](./00-overview.md);每个里程碑列对应文档 §。

## 已完成里程碑(M0-M14)

| M | 内容 | 主要文件 | 单测 | 提交 |
|---|---|---|---|---|
| M0 | 工程地基:go.mod/Makefile/.githooks/ci.yml/.golangci.yml/oracle 校验脚本 | `Makefile`, `.githooks/`, `.github/workflows/ci.yml`, `scripts/` | hook 拦截手测、`make all` 通过 | `eaf944c` |
| M1 | arena 分配器:bump + grow + 双视图 backing + null GCRef 保留 + 注入点 | `internal/arena/arena.go` | 11 用例 | `a34c680` |
| M2 | NaN-boxed Value:8 个 tag、IsNumber/IsCollectable/Truthy、NaN 规范化 | `internal/value/value.go` | 9 用例 | `e209dba` |
| M3 | object 布局:GCHeader 位字段 + 六类对象读写 helper | `internal/object/` | 8 用例 | `0a62aab` |
| M4 | bytecode ISA:38 opcode + 编解码 + Proto + ICSlot + int2fb | `internal/bytecode/` | 9 用例 | `e8f24a0` |
| M5 | mark-sweep GC:STW + 双白 + gray stack + R1..R9 根 + shadow stack + JSHash intern | `internal/gc/` | 9 用例 | `336e98e` |
| M6 | lexer:21 关键字 + 全 token + 数字/字符串/注释 + 四种换行 | `internal/frontend/{token,lex}/` | 11 用例 | `c726e33` |
| M7 | parser:递归下降 + 优先级爬升 + AST(后补 ParenExpr) | `internal/frontend/{ast,parse}/` | 全文法覆盖 | `4ff15cc` |
| M8 | codegen:expdesc/freereg 水位线 + 跳转回填 + 常量去重 + 黄金字节码 | `internal/frontend/compile/` | 9 黄金测试(02 §8 逐字节)| `6f63bce` |
| M9 | 解释器最小循环:大 switch + reentry(Lua 调用不增 Go 栈) | `internal/crescent/` | 7 端到端 | `f6df2ab` |
| M10 | GC 接入:根注入 + 分配点 safepoint + LinkSweep/计费 | `internal/crescent/alloc.go` | 4 GC 压力 | `10b3e87` |
| M11 | 元表 + pcall:__index/__newindex 链、算术元方法、callLuaFromHost | `internal/crescent/meta.go` | 9 端到端 | `f7812b1` |
| M12 | stdlib + host fn:base/math/string 最小集、HostFn 注册与同步调用 | `internal/stdlib/` | 9 端到端 | `96a9b7f` |
| M13 | 公共 API:Compile/Program/NewState/Value、vararg 完整化 | `wangshu.go` | 7 公共 API | `0d1c211` |
| M14 | 三套测试:conformance + difftest(差分测试 5.1.5)+ 三档基准 | `test/`, `benchmarks/baseline/` | 全绿 | `5cc5a6a` |

## 收尾轮(原"已知简化"清单,全部完成)

| 项 | 完成内容 | 设计文档 | 提交 |
|---|---|---|---|
| table 存储 | arena 原生 array+hash(主位置+冲突链、迁位插入、rehash 选最优 asize、border 二分、rawNext 迭代);旁路 Go map 移除 | 01 §5.2 | `1ab4beb` |
| generic for | TFORLOOP 完成 + next/pairs/ipairs | 05 §10.2 | `1ab4beb` |
| IC 命中路径 | icGetTable/icSetTable 五类指令(同表+同代次+同键校验,mono IC,array/node 直达);LoadProgram 改 State 私有浅拷贝(IC/常量不跨 State 串台) | 05 §6 | `c04010e` |
| string pattern | 完整 lstrlib 引擎(字符类/集合/量词/锚点/捕获/%b/%1-%9)+ find/match/gmatch/gsub/format/byte/char;string 库表挂 per-type __index(`("x"):upper()`) | 10 §7 / 07 §1.2 | `c167ad6` |
| stdlib 必做列 | table(insert/remove/concat/sort/getn/unpack)、math 补全(fmod/modf/random/三角/deg/rad + pi/huge)、os(time/clock/date/getenv)、io.write、unpack/xpcall;**io 三个标准流 + io.read/io.lines 与 debug 的 traceback/getinfo 在 2026-07-29(#205)补上**(见下方对账条目) | 10 裁剪表 / 10 §10.1.1 / 09 §13.4 | `72a09d5` + #205 一轮 |
| 协程 | 路线 B:yield 哨兵经显式错误通道冒泡(08 §3.4 对称机制),pendingResume 记录恢复点,resume 参数写回 yield CALL 结果寄存器;coroutine.create/resume/yield/status/wrap/running;跨 thread upvalue 经 uvOwner | 08 §3 | `c0f2ba9` |
| 错误目录 | chunkname:line: 位置前缀(error level 语义,level=0 不加)+ traceback(顶层未捕获自动附)| 09 | `473e4dd` |
| 弱表/finalizer | __mode 解析缓存进 GCHeader flags(setmetatable 唯一写入口),GC clear 弱分支激活;SetFinalizerRunner + __gc 创建逆序执行 | 06 §8.4/§10、07 §13 | `7078fcf` |
| arena ABI | wangshu.Arena 四类型列(float64/int64/bool/string)+ presence bitmap + 字符串池去重;Program.Call(state, arena, args);脚本侧 arena.col[i] 零拷贝即时装箱、只读、ColInt64 2^53 护栏 | 11 §3-§5 | `5122ae8` |
| difftest 生成器 | 受控文法随机脚本(类型化局部池),200 确定性种子差分测试官方 5.1.5 全部逐字节一致 | 12 §3.2 | `e1ddf2f` |
| per-item drop-in 子集 | `State.SetGlobal/GetGlobal/Call(fn,args...)` + `Register/RegisterModule` + 公共 `HostFn` 类型;`Value` 加 `kFunction` kind(外部不可构造,只能 GetGlobal 取);State pin 表 + GC 根接入(`PinRef/UnpinRef/visitExtraRefs`),globals 覆盖与 freelist 复用下旧 fn Value 仍安全可调;`Value.Release()` 显式释放 pin 槽 | 11 §7.1 / §9.1 (issue #1) | `87031c2` + `cb6e1ae` |
| 公共 Table API | `State.NewTable` + `Value.AsTable` + `Table.Set/SetIndex/Get/GetIndex/Len`;`Value` 加 `kTable` kind(同 kFunction 经 pin 表挂 GC 根);`fromInner` 升级为 `fromInnerWithPin`,Program.Run/Call 与 State.Call 返回路径能携带 table/function 引用;支持嵌套 table 与 mixed-type list 作 Lua 表 round-trip | 11 §4.5 (issue #2) | `2b55e11` |
| 严格沙箱模式 | `Options.HideFileLoaders bool`:从 globals 刮除 `loadfile`/`dofile`/`loadstring`/`load` 四件套(置 Nil);脚本调用 fatal `attempt to call global 'X' (a nil value)`,对位 gopher-lua 嵌入式沙箱传统;与 `AllowFileLoad=true` 同设 NewState panic fail-fast。默认行为不变(PUC 5.1.5 oracle 差分测试不退化) | 10 §12.1 LibsSafe 思路最小完成 (issue #3) | `09fdd72` |
| context cancellation 钩子 | `State.SetContext(ctx)` / `RemoveContext`:VM 在 chargeStep 同一抢占点(循环回跳(back edge) + 函数进帧 + TFORLOOP)检查 `ctx.Err()`,事件触发(wall-clock timeout / 上游 Cancel)中止 Run/Call 返回 Go error(pcall 可捕获);跨 goroutine 由 atomic.Pointer 保护;chargeStep 三处调用点合一(stepBudget 外层 if 拿掉,内部短路),零额外抢占点 | 11 §10 / 与 SetStepBudget 并存 (issue #4) | `27b4f2e` |
| Table.ForEach 任意 key 迭代 | `func (t *Table) ForEach(fn func(key, val Value) bool) error`:转发 internal `RawNext` 循环(raw 迭代,与 stdlib next/pairs 同源,迭代序确定性);fn 返 false 提前终止;key/val 走 `fromInnerWithPin` 自动登记 pin 槽。issue #2 SetIndex 写入的对称读出能力,完整读写闭环 | 11 §4.5 (issue #5) | `4f855d2` |
| globals baseline 状态隔离 | `State.MarkGlobalsBaseline` 拍当前 _G 字符串 key 快照、`ResetGlobalsToBaseline` 非 baseline key 清空 + baseline key 复原;baseline 复合值经 `visitExtraValues` 入 GC 根(与 pin 表是 GCRef-bearing value 必须遵守的不变式的两面:pin 管「公共 API 暴露的长持 GCRef」、baseline 管「内部状态恢复需要的长持 GCRef」);对位 gopher-lua statePool snapshotBaselineValues + resetToBaseline 模式 | 10 §12.1 hardening (issue #6) | `3d34839` |
| CallInto 零分配边界路径 | `State.CallInto(dst []Value, fn, args...) (n int, err)`:返回值写进调用方拥有的 `dst`,标量(bool/number)整条 round-trip 0 alloc。消除旧 `Call` 的双拷贝地板成本(VM 栈→inner slice→public slice,72 B / 2 allocs/call,与脚本复杂度无关)——内部 `callOnStack` 零拷贝切 `th.stack` 活动区(返回值只在下次进入 VM 或宿主触发收集(`Collect()` / `MaybeCollectNow()`)之前有效:runningThread 复位后主线程的栈不是 GC 根，#292 一轮订正了原先「mainTh 仍是常驻根」的说法),门面层复用 `innerArgsBuf` + 写调用方 dst。`Call` 保留为独立拷贝的便捷写法(返回值跨下次 Call 仍可读),内部走 callOnStack 后 append 一次。⚠️ 约定:CallInto 返回值底层是复用栈,下次进入 VM 前消费完;string 仍拷 arena 字节、复合值仍经 pin 表 | boundary-dominated 嵌入优化 (issue #8) | `CallInto` |
| step budget 按字节工作量记账 | 共享 `doConcat`(`internal/crescent/call.go`,fast path + slow-path plain fold)调 `chargeBulkWork(len)`(`internal/crescent/state.go` 新增),按 `len >> 6`(1 步 / 64 字节)把 CONCAT 拷贝 + intern 的字节工作量折算进 step budget,使预算成为**字节工作量的度量**而非仅指令条数。动机:`preempt()` 原本每指令边界只把 stepUsed 加 1,单条 CONCAT 能做与字符串长度成正比的无界工作 ⟹ `for i=1,N do glob=cat(i) end`(cat 内 `return "<~15KB 字面量>"..i`)每次迭代只扣约 2 步却拷贝约 15KB,1<<20 预算允许约 50 万次迭代、单次 prog.Run 约 2.7s wall-clock,4×run 触发 Go fuzz 10s per-input 看门狗(concat 风暴 crasher 家族 #166/#167 根因)。三层(P1 executeLoop / P3 wasm h_concat / P4 native host.Concat)全部路由同一 doConcat,单点记账覆盖所有 backend,差分对称性不破;<64B 记 0 步、1MiB concat 记约 16K 步(约 1.5% 预算),正常程序不受影响。比率 `>>6` 是按最慢的 CI runner(比本地慢约 10×)实测收紧后的值。**当时点名的下一批候选无界单指令算子(string.rep / string.format / table.concat)已于 2026-08-03 解决**,见下一行 | concat 风暴家族根因 (#166/#167,PR #168) | `88e724f` + `ab27936` |
| 三个批量字符串构造函数按字节记账 | `internal/crescent/state.go` 把 `chargeBulkWork` 导出成 `ChargeBulkWork`,`string.rep`(`stdlib.go::stringFnRep`,`len(s)*n`)/ `string.format`(`stringlib.go::stringFnFormat`,`len(out)`)/ `table.concat`(`tablelib.go::tableFnConcat`,分隔符字节 + 各元素字节之和)各自按**产出字节数**走**同一个**计量器,于是「批量工作」在 step budget 里只有一个定义、而不是三个会互相漂移的阈值。动机:上一行点名的三个候选确实是同类风险——实测在 1<<20 step budget 内、且**完全没有触发预算**的情况下,三者各自的紧循环分别跑 **21 秒 / 20 秒 / 53 秒**,单次 `prog.Run` 就已超过 Go fuzz 的 10 秒 per-input 看门狗,而 `FuzzAutoPromote` 每个输入要跑**四次** Run(与 concat 风暴家族相同的机制)。记账后 21/20/53 秒变 **46/90/70 毫秒**且预算正确触发;八种普通写法(含 1 MiB 的 `string.rep`、10000 元素的 `table.concat`)与 lua5.1 逐字节一致——1 步 / 64 字节的比率下 1 MiB 产出约记 16K 步、占 1<<20 预算约 1.5%。**`table.concat` 必须按字节而不是元素个数记账**:它的遍历本来就被表的长度界住,所以按个数看永远便宜——256 个元素听起来微不足道,而每个元素 2 KiB 时实际要 53 秒。这与 §4.9 那一类 hardening 上限(`string.rep` 的 1 GiB、`string.format` 的 width/precision)是**两件事**:那些是「宿主进程不可崩」的 fail-fast 上限,本行是预算记账,一次调用可以既在上限之内又把预算耗尽 | concat 风暴家族的下一批 (#222,commit `7119391`) | `7119391` |

## P1 总验收结果(roadmap §4 / 12 §10)

- **三档 ≥2x over gopher-lua**:✅ Xeon 6982P-C 实测(IC 完成后):
  simple 275ns vs 874ns = **3.18x**;arith 311ns vs 966ns = **3.10x**;
  loop 15.1µs vs 34.4µs = **2.28x**。分配 5 allocs/op vs gopher 8-124。
- **benchmark-game 真实负载**(benchmarks/realworld,P1 性能轮后):
  fib 1.31x、binary-trees 1.09x、spectral-norm 1.43x、fannkuch 0.82x、
  nbody 1.08x over gopher-lua——五项中四项反超(性能轮前 0.77x-1.17x,
  binary-trees/fannkuch/spectral-norm 曾落后)。剩余短板 fannkuch(表
  索引/交换密集):IC 命中仍付 accessor 间接层,直达偏移方案(DataOff)
  实测因校验复杂度得不偿失被否决,记 P2 IC 演进输入。五脚本返回值与官方
  lua5.1 逐字节一致(TestRealWorld_OracleParity)。
- **P1 性能轮**(同 commit 区间):closeUpvals maxOpenIdx 快路径
  (binary-trees -30%)、GC pacing 补附属块统计、根扫描免 map 分配、
  callHost 实参池(spectral-norm -36%)、State.Call 跨 Run 复用主 thread
  (simple 275→98ns)、表槽初始化批量化。
- **与官方 Lua 5.1.5 输出逐字节一致**:✅ seed corpus 70 用例 + 随机生成
  500 种子全部 byte-equal。oracle 源码编译供给(`~/.local/bin/lua5.1`)。
- **官方测试套移植**(test/luasuite):lua-5.1-tests 的一个子集原样运行(与上游**逐字节相同**),
  vararg/sort/pm 整文件通过,其余截断到豁免线(setfenv/debug/
  io 对象/setlocale/string.dump/require,均对应豁免注册表),前缀全过;
  stopAt 表强制登记、豁免线只许前移。
  ⚠️ **覆盖率口径纠正(2026-08-29)**:这一条历来只写「文件数 + 前缀全过」,不带覆盖率,
  读起来像全套跑过了。实测:套件文件数**少于上游**,按行号算只跑约一半,
  只有少数文件从头跑到尾(`events.lua` 只跑 3 行)。更细的一格是**「在套件里」不等于「在跑」** ——
  有文件开头就是 `if T == nil then ... return end`(官方 testC 调试库,本仓不提供),整文件零断言;
  所以按行号算的占比与已跑行数都不能当执行证据,要用执行侧的量(实际执行的断言次数)核。
  准确数字读 `test/luasuite/luasuite_test.go` 的 `stopAt` 表与 `resumeAt` 表(**文件数与占比不在本文写死**,
  这类量写进文档就会变成陈旧计数);口径与判据见
  [12](./12-testing-difftest.md) §2.1a,反思 [[2026-08-29-conformance-coverage-and-tiered-oracle-diff]]。
- **长时间稳定性承诺**:freelist 循环复用(22000 轮分配密集脚本 arena 稳定
  17.4KB);深递归 `stack overflow` 可恢复(LUAI_MAXCALLS=20000 等价);
  pcall 自递归 `C stack overflow`(LUAI_MAXCCALLS=200 等价)先于 Go 栈
  fatal;`-race` 下 Program 跨 16 goroutine 共享验证。
- **`make all` 检查**:✅ gofmt 空、golangci-lint 0 issues、`go test -race` 全绿;
  三平台交叉编译冒烟(386/windows-amd64/darwin-arm64)进 CI。

## 审查核销轮(外部逐提交审查 → 集中修复)

外部代码审查逐函数对照官方源码,12 轮报告共发现 22+ 项真实问题,
集中修复轮全量核销(每项独立提交,`95b51a3..`)。重点:

- **DoS 级**:constFold 丢弃带跳转链的 eKNum(`(true and 7 or -1)+1` 一行
  Go panic 崩宿主,潜伏自 M8)→ isnumeral 同构 + Program.call recover 兜底;
  SETLIST 批号超 9-bit 截断卡死 → 官方 C=0+裸批号路径;深嵌套/无限循环 →
  parse 深度护栏 + back edge 指令预算。
- **静默给出错误结果**:lexer 数字非贪心(`return 1or 2` 被接受执行)→ 官方
  read_numeral 贪心重写;`(a)=5` 被接受 → ParenExpr 全包;table.remove
  越界删末元素;math.max 首参吞错;Fb2Int 缺 &31 掩码。
- **内存/资源**:arena 尺寸入口 uint32 回绕(4GiB 请求"成功"切 8 字节)→
  uint64 域检查 fail-fast;对象尺寸公式四处手写 → object.SizeOf 单源;
  hostFn 注册表无界增长(gmatch/mountArena)→ 引用计数槽回收。
- **官方测试套驱动**(test/luasuite 移植扫出):break/repeat 漏发 CLOSE
  (闭包捕获循环变量后 break 读脏值)、return 短路链快路径卡死、pattern
  %z/未闭合捕获 panic/%q 格式、gsub/sort 走元方法、gmatch 空匹配推进、
  near 原文(txtToken)、luaO_chunkid 同构、错误措辞 luaL_checknumber
  格式、5.0 兼容别名(math.mod/foreach/gfind)。

## 与设计文档的对账(实现形式差异,均为接口等价)

| 设计点 | 设计文档形式 | 实现形式 | 对账结论 |
|---|---|---|---|
| 值栈/CallInfo 位置 | 住 arena(05 §1.2),Thread 对象 word 字段 | Go slice(crescent.thread struct) | **接口等价、P3 迁移点已留**:backing 注入点(`arena.Options.NewBacking`,06 §1.1 唯一硬性前瞻义务)已就位;协程"状态冻结"语义已可工作(yield 保留 CallInfo 链)。物理搬迁是 P3 wazero memory 接管时的工作,届时 stack/cis 切 arena 视图不动 opcode 语义 |
| per-item API 栈机风格 | `PushNumber/ToNumber/Top/Pop/GetGlobalFn/CallFn` 等(11 §7.1 草图,gopher-lua 栈机) | `State.SetGlobal/GetGlobal/Call(fn,args...)` + `Register/RegisterModule`(列表风格) | **形式裁剪、能力等价**:pineapple 一类「fn 一次取出 + 循环 per-item Call」用法由 GetGlobal+Call 覆盖;Push/Pop 栈机风格未做(若未来 gopher-lua 迁移负载明确需要再补)。HostFn 收 args 中 table/function/userdata 仍映射 Nil(本期 fromInner 收紧)、host closure 从 Go 端直接 Call 仍未开 |
| host closure 从 Go 端 Call | 任意 closure 一视同仁可被 `state.Call` 调起(11 §1.5) | internal `State.Call` 见 host closure 直接报错(`call.go:hostCheck`) | **有意裁剪、不影响主线**:`Register` 注册的 host fn 仍可由 Lua 内调用正常工作;Go 端「state.Call(hostFn,…)」用法未开,等真有需求时补 callHost 入口的脚手架(临时栈帧) |
| 开放 upvalue 链 | 按 stackIdx 降序单链(05 §8.3) | Go map(stackIdx → uvRef)+ uvOwner(uv → thread) | 共享语义等价(同槽同 uv);降序链是值栈 arena 化的配套,一并留 P3 |
| executeSignal 三态 | sigReturn/sigYield/sigError 枚举(08 §3.3) | 显式 *LuaError 返回 + errYieldSentinel 哨兵 | 同一冒泡通道,哨兵区分;08 §3.4 "yield↔error 对称"的最小实现 |
| 协程对象 | Thread 对象住 arena(01 §5.6) | arena 里只有头的 Thread 对象(word7 = 注册表下标)+ Go 注册表记录(值栈 / CallInfo 是 runtime 自己的 arena 段) | **回收语义已对齐**(#291,2026-10-04):原先是 lightuserdata 句柄，所有非 dead 协程都是 GC 根;现在没有引用的协程被回收，弱表照常清，见 08 §6.1。栈仍不放进 Thread 对象自己的字段 |
| xpcall handler 时机 | 栈展开前调用(09) | 出错点调用(2026-10-02 起) | **已对齐**(#279 本地审查补修):原先在捕获后调用、栈已回滚,`xpcall(f, debug.traceback)` 只看到调用 xpcall 的那一帧;现在按 5.1 的 `luaG_errormsg` 在出错点调用，见 09 §6.2 |
| C 调用深度的起点 | `LUAI_MAXCCALLS` = 200;宿主运行 chunk 的 `lua_pcall` 本身是一层 `luaD_call`,lua5.1 独立解释器另外还有 `lua_cpcall(pmain)` 一层 | 宿主运行 chunk(`callOnStack`)计一层，与嵌入式 PUC 一致 | **已知限制，只剩 lua.c 独有的 1 层**(2026-10-02 #276-#279 本地审查):原先主 chunk 从深度 0 开始，比嵌入式 PUC 多走 1 层;第六轮审查用内嵌 oracle 查出后改为计入宿主那一层，`test/fuzz/cdepth_oracle_test.go::TestCDepthMatchesEmbeddedPUC` 在六种写法上与 oracle 逐字节一致。与 lua5.1 独立解释器相比仍浅 1 层(`__index` 自递归 197 对 196),那是 lua.c 自己的 `lua_cpcall(pmain)`,嵌入式 VM 没有，不模仿。差分侧按 `SkipClassError` 的「实现常数类护栏」跳过 |
| 语法层数的起点 | `lparser.c` 的 `enterlevel` 记在 `nCcalls` 上，同一个 200 | `parse.maxParseDepth` = 200,减去当前 C 调用深度(`ParseAtCDepth`) | **已知限制，同上一行**:计层位置一致(块与子表达式各一层),随调用深度的变化一致，与嵌入式 PUC 逐字节一致;与 lua5.1 独立解释器相比多 1 层(顶层 197 对 196),来源就是上一行 lua.c 的那 1 层 |
| ephemeron | 键活则值无条件活(07 §13.5 P1 简化,自带) | 同设计 | 一致(设计本身即简化) |

## 重要实现决策与差分修偏记录

- **字符串字面量惰性 intern**(Proto.StringLits/StringLitIdx):Program 跨 State 共享,
  每 State 私有 intern + 私有 IC(11 §1.3 定稿的并发细化)。
- **错误传播**:显式 `*LuaError` 返回贯穿主循环;host→Lua 重入边界(callLuaFromHost)
  负责 CallInfo 回滚(05 §9 定稿)。yield 复用同一通道(哨兵)。
- **差分修偏实例**(12 §0 机制起效的证据):rawEqual NaN bits、%.14g 的 inf/nan 措辞、
  and/or 的 VCALL 单值收敛、VARARG 落点回填、ParenExpr 强制单值、return-vararg
  多值、break 双层块、多值 return 末位 eCall 的 A 覆盖。全部由 conformance/
  difftest 捕获后当步修复。
- **oracle 差分巡检的 stdlib 语义修偏(2026-07-28,#192/#193/#194/#196 一轮六个根因)**:
  四个 issue 报四处,实际修了六处,另有一处是本分支自己 45 秒 fuzz 冒烟跑出来的。

  | 根因 | 落点 | 修法要点 |
  |---|---|---|
  | C99 `nan(n-char-sequence)` 未被消费(#192) | `internal/crescent/number.go` | 只在 `)` 真的存在时才消费整组;未闭合的 `nan(` 保留裸词照旧被拒;`inf(...)` 无此形式仍拒 |
  | `tonumber(x, 10)` 整条路由错(**无 issue,扫描发现**) | `internal/stdlib/stdlib.go` | PUC 在校验范围之前、读 arg 1 之前就判 `base == 10` 并走标准转换。一个原因造出六条分歧(`"1.5"`/`"0x10"`/`"1e3"`/`"inf"`/`"nan"`/number 实参),五条没有任何 issue 记录;现在无 base 与 base 10 共用一个 helper |
  | `%d`/`%i` 精度 0 配值 0 丢符号(#196) | `internal/stdlib/stringlib.go` | C 转换出零个数字但仍输出 `+`/空格 flag 的符号,Go 连符号一起丢。新增 `cSignedFormat` 只在这个角落手写,与隔壁 `cUnsignedFormat` 同一手法(10 §5.2.1b) |
  | `table.insert` 的 5.1 语义本来没有边界检查(#194) | `internal/stdlib/tablelib.go` | `position out of bounds` 属 5.2+;5.1 的 `tinsert` 无检查,`e = #t+1`、`pos > e` 时抬 e(10 §7.2 真值表) |
  | `table.concat` 错误文本(#194) | `internal/stdlib/tablelib.go` | PUC 的 `addfield` 是 `"invalid value (%s) at index %d ..."`,`%s` 是元素的 `luaL_typename`;原先括号括错范围且丢了类型名 |
  | `string.char` 的两步转换(#193) | `internal/stdlib/stringlib.go` + `internal/oracle/prelude.go` | `luaL_checkint` 是 `(int)luaL_checkinteger`;这是 C UB 且跨 arch 不一致,产品侧固定按 x86-64、差分侧跳该区间(12 §4.9b,10 §5.4b) |
  | `strtoul` 的无符号取反与溢出饱和(**无 issue,扫描发现**) | `internal/stdlib/stdlib.go` | 有定义的 C,所以对齐而非跳过。原有注释声称「已登记为 diff 豁免」但**没有任何代码实现它**,分歧一直是活的 |
  | table 库缺 `, got no value` 从句(**无 issue,fuzz 冒烟发现**) | `internal/stdlib/tablelib.go` | `luaL_typerror` 是 `"%s expected, got %s"`,`lua_typename` 把 `LUA_TNONE` 映射成 `"no value"`;显式 `nil` 与「没传」是两种情况(10 §2.4) |

  扫描规模:8640 种带符号 verb 写法(`d`/`i` × 12 flag 组合 × 6 宽度 × 6 精度 × 10 值)、
  164 种 `tonumber`/`char`/`insert`/`concat` 组合、45 种负数 × base 组合,全部零差异;
  60 秒 fuzz 无 crash;23 条 seed 入 `testdata/fuzz/FuzzOracleDiff/`,**故意包含负例**
  (`nan(` 仍拒、`char(-1)` 仍报错、`inf(0)` 仍拒)——只钉住「修好的方向」的 seed 分不出
  「正确的修复」和「什么都接受的修复」。过程反思见
  `llmdoc/memory/reflections/2026-07-28-four-diff-divergence-issues.md`。

- **oracle 差分巡检的 stdlib 语义修偏(2026-07-28,#197/#198/#199 一轮)**:承上一条同日那轮
  (#199 那组分歧本身就是上一轮第六次审计在别的子系统找到、开成 issue 的)。

  | 根因 | 落点 | 修法要点 |
  |---|---|---|
  | `error(msg, level)` 解析了 level 却不用它选帧(#197) | `internal/crescent/errors.go` + `state.go` + `frame.go` + `meta.go` | 每个 level ≥ 2 都拿到最内层位置。两层认识:**host 边界是一个 level 不是终点**(PUC 对 `pcall(f)` 的栈是 `[f, pcall(C), caller]`,level 2 落 C 帧空前缀、level 3 到 caller 有前缀)+ **一个边界可代表多个叠起来的 host 帧**(`pcall(pcall,f)` 在 PUC 是两个 C 帧,而 `nCcalls` 是运行总数区分不了),所以**每帧的 host 帧计数进 `callInfo` word2 bit 51-54**(四位饱和,`pendingHostFrames` 递增 / 下次压帧消费,`verifyCISeg` 覆盖),常数偏移在 `pcall(pcall,f)` 上必然错(09 §3.2.1,05 §1.2 布局) |
  | `%g` 用 Go 的最短往返而不是 C 的默认精度 6(#198) | `internal/stdlib/stringlib.go` | 显式精度本来就对,`%e`/`%f` 的默认也本来就对(Go 与 C 都是 6),**只有 `%g` 的默认不同**;没有显式精度时补 `.6`(10 §5.2.1a) |
  | `gmatch` 的前导 `^` 被当成锚(#199) | `internal/stdlib/pattern.go` + `stringlib.go` | PUC 的 `gmatch_aux` 直接调 `match()`、没有 anchor 处理(不像 `str_find_aux`),所以 `^` 在 gmatch 里是**普通字符**;给顶层驱动加 allowAnchor 选项而不是写第二份 matcher,find/match/gsub 保持 anchor(10 §6.3) |
  | `assert` 第二参数什么都收(#199) | `internal/stdlib/stdlib.go` | PUC 是 `luaL_error(L, "%s", luaL_optstring(L, 2, ...))`,`luaL_optstring` 只收 string 或 number,table/boolean 是**参数错误**而不是被 stringify;原来用 `valueToString` 并把 table 本身当错误值(09 §4.1) |
  | `math.modf` / `frexp` / `rad` 三处 Go math 包与 C 的差(#199) | `internal/stdlib/mathx.go` | 无穷的小数部分 C 给**带符号的 0**、Go 给 NaN;`floor(log2)+1` 推指数在 DBL_MAX 处 off-by-one(改用 `math.Frexp`);`x*π/180` 的乘法先溢出(改乘单个常数 `π/180`,PUC 的 `RADIANS_PER_DEGREE`)(10 §8.5) |
  | `io.write` 一个值都不返回(#199) | `internal/stdlib/tablelib.go` | 5.1 的 `g_write` 压的是**布尔**成功标志,写失败压 false 不抬错;**issue 里写的「返回文件句柄」是错的**,那是 5.2+。原先 `type(io.write(""))` 报 `value expected`(10 §10.3) |
  | `os.date` 只认六个指令、`*t` 与 `!` 前缀压根不认(#199) | `internal/stdlib/tablelib.go` | `strings.Replacer` 写法让其余指令原样透出(`os.date("%j")` 返回 `"%j"`);改成指令循环覆盖 `Y y m d e H M S I p j a A b B c x X Z w n t %%`,未定义指令原样输出(glibc 就是这样),`*t`/`!*t` 返回九字段表(10 §9.2) |
  | `print` 对内嵌 NUL 截断——**刻意不改** | `internal/stdlib/stdlib.go`(注释) | PUC 的 `luaB_print` 用 `fputs` 停在第一个 NUL,而 PUC 自己的 `io.write` 用带长度的 `fwrite` 不截断,**参照实现内部不一致**;5.1 手册明确字符串 8-bit clean,那个截断是 C 调用的产物,对齐它等于故意丢用户数据(10 §4.2,12 §4.9b 第四格) |

  **harness 侧同轮撤 skip**:为 #197 加的那条 skip 覆盖任何提到 error 第二参数的输入,修好后
  撤掉——24 种 error level 写法现在**零 skip** 参与比对,只剩非有限 level 跳过(`luaL_checkint`
  窄化的真 UB),函数改名 `errorLevelUBRange`(12 §4.9c)。
  **验证与扫描规模**:与系统 `lua5.1` 比对 error level 30 种写法(五种嵌套 × level 0-5,含
  `pcall(pcall,f)`)、`%g` 13 种、gmatch/find/gsub 若干、assert 7 种、math 12 种、`os.date`
  九种;另主动生成 296 个探针直接与 `lua5.1` 比对(`%g`/浮点 verb × 12 值 × 13 spec、
  gmatch/find/gsub × 9 pattern、assert × 7 消息、math × 9 值 × 7 函数、os.date × 16 格式、
  io.write),**289 个可比对项零真实分歧**(4 个差异是探针自身产物:地址文本、表里的
  `tostring(nil)`、`io.write` 的副作用写到 stdout)。过程反思见
  `llmdoc/memory/reflections/2026-07-28-issue197-199-stdlib-semantics.md`。

- **nightly crasher + 预存缺口对账(2026-07-28,#201/#202/#203 一轮)**:三个 issue 性质不同——#201 与 #203
  是 nightly 自动开的 go-fuzz crasher,#202 是上一轮修 #197-#199 时**顺手记下的一组**预存缺口。

  | 项 | 落点 | 结论与修法要点 |
  |---|---|---|
  | `unpack` 的上限是 `LUAI_MAXCSTACK` **减参数个数**(#201) | `internal/stdlib/tablelib.go` | 原先按固定 8000 比较,接受了 PUC 拒绝的 7998..8000 那一段。PUC 的 `luaB_unpack` 调 `lua_checkstack(L, n)`,拒绝条件含 `(L->top - L->base + size) > LUAI_MAXCSTACK`,对 C 函数 `L->top - L->base` 就是参数个数。**结论是量出来的**:阈值随参数个数变化正是排除「硬编码 7997」的证据(10 §4.5) |
  | `error` 自己是被调 host 函数时位置前缀完全丢失(#202) | `internal/crescent/meta.go` + `errors.go` + `state.go` | host 抛错经 `callHost` 直接返回、不进解释器循环,`annotateError` 从来看不到它(#197 那套按帧计数在这条路径上一次都没被调用)。改在边界标注,另外两件要对:**level 1 是 host raiser 的调用者**(不占 Lua 帧,所以 level 1 该裸、level 2 该带前缀;第一版从 `Level-1` 消费使每个 level 偏一格)+ **只对显式 `level >= 2` 生效**(PUC 的库错误经 pcall 抛出时不带位置,`TestTableConcat_ErrorTextMatchesPUC` 立刻抓到)(09 §3.2.2) |
  | `os.time` 忽略 `isdst` 字段(#202) | `internal/stdlib/tablelib.go` | PUC 把它填进 `struct tm` 的 `tm_isdst` 交给 `mktime`,用来确定 DST 相关本地时间的解释;只在与该日期在该时区的自然状态**不一致**时才有影响。要区分「字段不存在」与「显式 false」(新增 `getBoolField` 返回 `(value, present)`)。`TZ=Europe/London` 下双向实测(10 §9.1.1) |
  | 移位区间的 skip 与产品上限拆成两个数(#203) | `internal/oracle/prelude.go` | 同一写法的**第三个** nightly crasher(约 100M 的移位跨度、刚好在 2^27 之下,三条都正确且对称、只是耗数秒,而 coordinator 并行重放整个 corpus)。第三次说明该改的是被接受的区间,不是再挪一个 seed:产品侧留 2^27(**正确性**——lua5.1 会做这个移位),harness skip 降到 2^20(**资源**——什么输入能待在并行重放里)(12 §4.9d) |
  | **不成立的三项**(#202) | — | `coroutine.wrap` 缺前缀 / `%#g` 指数交界 / `math.deg` 差 1 ulp,**实测都与 `lua5.1` 一致**。那个 issue 是自己开的,七项里三项不成立。(2026-10-02 订正:`coroutine.wrap` 一项当时只测了经 `pcall` 调用的写法，那里调用方是 C 函数、两边都只有一层位置;在 Lua 代码里直接调用时 lua5.1 会多补一层调用方位置，这一项其实成立，见 #276) |
  | **挪去 #205 的两项**(#202) | — | `io.stdout`/`io.read` 缺失(需要 file-handle userdata 基础设施,10 §10.1.1)、`debug.traceback` 缺 `[C]` 帧(`debug` 库整个不存在,在 `debug` 表出现之前无从谈起) |

  **`io.stdout` 撤回的完整理由(10 §10.1.1)**:已经做出来了——三个标准流做成 file-handle userdata + 共享
  metatable(`write`/`close`),为此补了三处 VM 缺口(`metaFieldOfValue` 不认 userdata、`indexWithMeta` 没有
  userdata 分支、`getmetatable` 对 userdata 无条件返回 nil),补完之后 `io.stdout:write()`、`:close()`、
  `getmetatable` 都对了。**但随后 `TestGCStress_RandomScripts` 失败,创建句柄之后一个
  `collectgarbage("collect")` 就以 arena 索引越界 panic**:句柄在仍从 `io` 表可达的情况下被清扫,说明构造
  方式没有正确进入 GC 的根 / 追踪路径(`internal/gc/mark.go` 确实会把 `OBJ_USERDATA` 追到它的 meta 与
  env ref,所以问题在构造侧不在收集器)。这是运行时里**第一个在 `__gc` finalizer 之外创建的 userdata**,
  分配与 rooting 的约定得先搞清楚,所以整段撤回、开 #205,而不是带着一个会破坏 arena 的改动继续。

  **验证与扫描规模**:84 种 unpack 写法与 oracle 一致(覆盖 7995-8001 那一段、1e9 量级范围、空范围与倒置
  范围、非有限边界);66 种 error level 写法与 `lua5.1` 一致(含 `pcall(error,...)` 与
  `pcall(pcall,error,...)` across level 0-6);另主动生成 85 个探针直接与系统 `lua5.1` 比对(unpack 边界
  × 12 值 × 4 写法、error level × 5 × 5 写法含 host raiser、`os.time` 的 isdst 六种组合、六种「host callee
  抛的库错误必须保持裸」),**81 个可比对项零差异**(4 个是 lua 侧也抬错、在那种探针写法里无法捕获的,已由
  84 种 oracle 比对覆盖);两条 seed 入 `testdata/fuzz/FuzzOracleDiff/`。过程反思见
  `llmdoc/memory/reflections/2026-07-28-issue201-203-unpack-skip-thresholds.md`。

- **file-handle userdata + `debug` 子集 + `string.byte` 的上限(2026-07-29,#205/#206/#208 一轮)**:
  上一轮撤回的那一块交付,同时补一处 stdlib 上限、一处 harness 捕获缺口。

  | 项 | 落点 | 结论与修法要点 |
  |---|---|---|
  | 三个标准流做成真 file-handle userdata(#205) | `internal/crescent/alloc.go` + `internal/stdlib/tablelib.go` | 上一轮撤回的**根因只有一行**:原型直接调 `object.AllocUserdata`、**跳过了 collector 的 `LinkSweep`**,对象 header 里既没有颜色也没有 sweep 链,收集器根本看不见它;创建句柄后一次 `collectgarbage("collect")` 就以 arena 索引越界 panic 而句柄仍从 `io` 表可达。`alloc.go` 里每个分配器都是 `AllocX` + `LinkSweep` + `AllocCharge` **三件一起**,现在 `State.NewUserdata` 把它固定成唯一入口(06 §2.1.1,10 §10.2.1)。必须是真 userdata 而不是表,因为 PUC 报的是 `userdata`、用表的话 `type()` 会暴露差异 |
  | 两处 VM 缺口 + `getmetatable`(#205) | `internal/crescent/meta.go` + `internal/stdlib/stdlib.go` | `metaFieldOfValue` 不认 userdata(所以 userdata 的 `__index` 从来没被查过)、`indexWithMeta` 压根没有 userdata 分支(userdata 没有裸字段,索引直接走 `__index`,无 metatable 时报 `attempt to index a userdata value`);`getmetatable` 对 userdata 原先无条件返回 nil,现在还会遵守 `__metatable`(07 §1.3) |
  | `io.read` / `io.lines` / file 方法(#205) | `internal/stdlib/tablelib.go` | `readFormats` 一份实现同时供 `io.read` 与 `file:read`;`stdinReader` 是**一个共享的** `bufio.Reader`(标准输入的位置是全局状态,两个 reader 各自持缓冲预读会静默丢字节);`"*a"` 在 EOF 返回 `""` 而不是 nil。`io.lines(filename)` **抬错**而不是静默返回空迭代器(10 §10.3) |
  | `debug` 表注册 traceback + getinfo(#205) | `internal/stdlib/tablelib.go` + `internal/crescent/errors.go` | `getinfo` 只填 P1 能诚实回答的字段(`currentline`/`source`/`short_src`/`what`/`func`),`nups`/`activelines`/`namewhat` **宁缺不假造**;`FrameInfo` 有一个 off-by-one——`getinfo` 是 host 函数、host 帧不进 `cis`,所以 level 1 是最内层 cis 帧,直接减 level 会让最常见的 `getinfo(1)` 返回 nil(09 §13.4) |
  | `string.byte` 缺 `lua_checkstack` 上限,而且消息被包了一层(#206) | `internal/stdlib/stringlib.go` | 原先**完全没有上限**,`string.byte(string.rep("a",9000),1,8000)` 返回 8000 个值而 PUC 抬错;上限与 `unpack` 一样是 `8000 - nargs`,但 `luaL_checkstack` 把调用者的文本套成 `stack overflow (%s)`,所以 PUC 输出的是 `stack overflow (string slice too long)`。第一版上限对了、文本照抄了裸串,65 个 oracle 用例里仍有 12 个分歧(10 §5.4c) |
  | 捕获累加器接上 file-handle 的 `:write` | `internal/oracle/prelude.go` | 加了 `io.stdout` 之后脚本可以经一条 harness **没有捕获**的路径输出,那段文本在**两侧**捕获里都不存在——两侧仍然一致、不报分歧,而比较已经不覆盖这条路径写出的任何东西。**这是「因为错误的原因而变绿」**(12 §3.1.1) |
  | **#208 一行代码没改** | — | 它的 fuzz run 跑在 `cbd0512` 上,**早于**上一轮把 harness skip 降到 2^20 的 `c07ba58`;现在这个 seed 0.00 秒就跳过,作为回归防线留在 corpus 里。上一轮「该改的是被接受的区间,不是再挪一个 seed」的正面验证 |

  **仍然缺的**:`io.open` / `io.popen` / `io.tmpfile` / `f:seek` / `io.input` / `io.output` / `io.type`
  (需要真实文件,`__gc` 关文件那一环要跟 `io.open` 一起做);`debug` 的
  `sethook`/`getlocal`/`setlocal`/`getupvalue`/`setupvalue`/`getregistry`(需要解释器没有暴露的内省钩子);
  遍历全部 5.1 函数名的**存在性差分用例**本身仍然没写(10 §12.3)。`test/difftest/corners_test.go::exemptions`
  里那三条已同轮改写成如实描述现在缺什么。

  **验证与扫描规模**:56 个探针直接与系统 `lua5.1` 比对(句柄 type / 相等性 / `:write` 返回类型 /
  `:close` 的 `(nil,msg)` / 非句柄 receiver 的措辞 / 只写句柄读出的 errno 三元组 / `debug.traceback`
  的四种 message 类型 / `getinfo` 的 level 与无参错误),**54/56 一致**(剩下 2 个是故意不提供的
  `debug.sethook`/`getlocal`);`io.read` 八种写法(`*l`/`*n`/`*a`/字节数/EOF/默认/`io.stdin:read`/
  `io.lines`)用**真实二进制** + 真实 stdin 与 `lua5.1` 一致——`go test` 不转发 stdin,在 `go test` 里
  写的探针全部返回 nil、量到的是 harness 而不是代码;65 个 `string.byte` 上限用例与 oracle 一致;
  `io_handles_test.go` 三个测试(`TestIOHandles_SurviveGC` / `TestIOHandles_MatchPUC` /
  `TestDebugLibrary_MatchPUC`)。过程反思见
  `llmdoc/memory/reflections/2026-07-29-issue205-206-208-io-userdata-debug.md`。

- **把两个阈值的拆分固定下来(2026-07-29,#209 一轮,零产品代码改动)**:#209 是 insert 移位那一写法的
  **第三个** nightly crasher(#203 / #208 / #209 三个不同夜晚),而它本身也是过期的——fuzz run 跑在
  `cbd0512` 上、**早于**把 harness skip 降到 2^20 的 `c07ba58`,现在这个 seed 0.00 秒就跳过,seed 入
  `testdata/fuzz/FuzzOracleDiff/` 作回归防线(第一档命中的第二个实例,第一个是 #208)。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 拆分本身缺一个执行体 | `test/regression/insert_shift_cost_test.go` | 上一轮把产品上限(2^27,正确性)与 harness skip(2^20,资源)拆成两个数是对的,但**修完之后没有任何东西固定住「这两个阈值是两个数」**:入 corpus 的 seed 只能表达「这个输入不崩」,表达不了「那个决定还在」——合到 2^20 则产品开始拒绝一段 lua5.1 能完成的移位而昂贵区 seed 只是被 skip,合到 2^27 则昂贵那一段重新进并行重放而现有 seed 恰好都在跳过区(12 §4.9d) |
  | `TestInsertShiftThresholdsStayDistinct` | 同上 | **按行为断言而不是比对字面量**:两个常数一个在 `internal/stdlib` 一个在 `internal/oracle/prelude.go`、都不导出也不同包,写死 `1<<20`/`1<<27` 只会与任一侧各自漂移。断言的是区间里一个输入的行为——跨度约 2M 落在两个阈值**之间**,①必须由产品执行(`Run` 不返错)②必须便宜(耗时上界)。**两个方向都要断言**:只断言①时合到 2^27 仍然全绿,只断言②时合到 2^20 也全绿 |
  | 昂贵的那一段只在「远低于索引 1」这一侧 | — | 五个 seed 同一写法,没有只确认 #209 那一个:大的**正**位置、`table.remove` 位置远低于 1、`table.remove` 位置远高于 `#t`、中等跨度实测都是 1-2 ms 且两侧对称,所以 skip 覆盖的就是**单次调用**里真实的那一类;摊到多次调用上的同一份工作是第二次审计补的缺口(见文末补记) |
  | corpus 没有按文件名模式清理 | `testdata/fuzz/FuzzOracleDiff/` | 四个 `table.insert(t,4...` 看起来同类,逐个算窄化值才发现 `4294967298 = 2^32 + 2` 模 2^32 之后是 **+2**——普通的正位置插入、根本不走 skip,其余三个都是约 -100M。按模式合并会删掉唯一的用例,最后全部保留(全量重放 0.62 秒) |

  **验证规模**:120 秒引导式 fuzz(17.7 万次执行)干净;corpus 全量重放 **0.62 秒**(此前单个这类 seed
  就要数秒,正是压垮 worker 的原因);全套测试、`test/`、oracle 单测、corpus 全绿。另记:两次跑在修复
  之后的 nightly run(`921d3ec` 与 `5383aec`)当时**仍在 in_progress**,所以「修法已在 nightly 里被
  验证过」这句话当时不能说,支撑结论的是本地那 120 秒 fuzz 与 corpus 重放。过程反思见
  `llmdoc/memory/reflections/2026-07-29-issue209-stale-crasher-threshold-pin.md`。

- **八个 nightly crasher、六个 seed、三个真缺陷(2026-08-02,#212–#219 一轮)**:八个 run 的 headSha 全是
  `5383aec`,与 #209 那一轮字面事实几乎一样,但**结论相反**——实际重放之后五个如实复现,三个是真产品缺陷。
  八个 issue 只有**六个不同的 seed**(#212/#215 是同一个 hash `1d42242f157c9754`,#217/#219 是同一个
  `0150a245c776b8ed`)。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | `error()` 的 level 参数没做类型检查(#212/#213/#215) | `internal/stdlib/stdlib.go` | 原先写成「转换成功才用,失败静默保留默认值 1」,而 PUC 的 `luaL_optint` 对**显式传了一个转不动的值**是**抬错**的:`error("", 0>0)` 报空消息而 lua5.1 报 `bad argument #2 to 'error' (number expected, got boolean)`。`luaL_opt*` 是**两条**规则——缺省 / 显式 nil 取默认值,显式非法值抬错,而数字字符串仍然强制转换。差分细节:同一个错误在 Lua 函数**内部**抬出时带位置前缀,经 `pcall(error, ...)` 直接调用时不带、函数名退化成 `'?'`(09 §3.1a) |
  | 调用的行号取了被调用表达式那一行(#214) | `internal/frontend/parse/expr.go` | `ast.CallExpr{Line: e.Pos()}` 用被调用表达式的起始行,而 PUC 记的是**参数列表**开始那一行:`(0\n)()` lua5.1 报第 2 行、望舒报第 1 行。改成在 `parseArgs` 之前取 `p.tok.Line`。**只有跨行的被调用表达式才有差别**,单行时两者相同,所以只能靠 fuzz 发现;第一版把 `Line` 整个挪到参数列表那一行,而它同时喂 callee 物化 / 参数物化 / CALL 三个点,于是 callee 的 GETTABLE 也被挪走(`t.x\n{1}` 把索引 nil 报到第 4 行)——审计发现。改成两个行号:`Line` 给物化、`ArgsLine` 只给 CALL。`MethodCallExpr` **同样有这个问题且早于本分支**(SELF 与 CALL 共用一行,`t:nope\n{}` 报第 2 行而非第 3 行),一并修好(09 §3.5.1) |
  | `string.gsub` 的 repl 类型是惰性校验的(#216) | `internal/stdlib/stringlib.go` | 不是少了一个检查,是**检查放错了位置**:类型判断写在替换循环**里面**,而第 4 个参数把循环次数压成 0,于是循环一次都没跑、非法参数从来没被看到,`gsub("", "", nil, .0)` 成功返回而 lua5.1 抬 `bad argument #3`。PUC 是在循环**之前**用 `luaL_argcheck(tr)` 校验的。**是第 4 个参数让这条路径可达的**(10 §6.5.1) |
  | `math.mod` 不是产品缺陷,是 harness 的**别名漏网**(#217/#219) | `internal/oracle/prelude.go` | `mathFn2` 报第一个缺失参数是**刻意决定**:两个官方构建互相不一致(x86-64 报 #2、arm64 报 #1,C 不规定实参求值顺序),没有可对齐的对象。但 `__wrapArgOrder` 只包了 `math.fmod`,没包它的 `LUA_COMPAT_MOD` 别名 `math.mod`(同一个 C 函数),于是同一写法被开成**两个** issue;`math.atan2` 也从来没被包过。现在按「math 表里所有取两个数的入口」全部包上(12 §4.9e,10 §8.6) |
  | #218 不是缺陷,是**重 workload 进了 corpus** | `test/regression/p4_hot_loop_promote_test.go` | 一亿次迭代、既不崩也不分歧,只是 p4 corpus 里最重的一个 seed(1.8 秒,其余都在 0.3 秒以内,约 6 倍),而 coordinator 并行重放整个 corpus。按 triage guide 走显式回归测试。**第一版做错了**:只抄了脚本、没抄 harness 的 `SetStepBudget(1 << 20)`,跑到循环结束耗 **87 秒**(harness 的 50 倍)——那 1.8 秒不是一亿次迭代的代价,是一百万步预算的代价。镜像 budget 与 arena cap 之后是 1.82 秒。按 guide 规定没有自建 in-test deadline(失败模式是「永不返回」,交给包级 `go test -timeout`) |

  **验证规模**:全套测试 + `-race`、`test/`、p3 / p4 两个 build tag 全部 0 失败;oracle 单测与 corpus 全量
  重放全绿;45 秒引导式 fuzz 干净;**五个 seed 全部从 FAIL 变 PASS**。过程反思见
  `llmdoc/memory/reflections/2026-08-02-issue212-219-fuzz-crasher-batch.md`(四条教训:版本核对的三档是
  成本档不是缺陷档 / 豁免判据必须覆盖别名 / 参照实现在循环之前做的校验不能挪进循环 / 把 seed 转成回归
  测试时要连 harness 的限制一起抄)。

- **concat 风暴家族点名的三个候选算子处理完(2026-08-03,#221 / #222 一轮)**:#221 **过期**——seed 是
  `print(pcall(math.mod))`,正是上一轮给 `__wrapArgOrder` 补上 `math.mod` 别名之后修掉的那个,它的 run
  跑在 `947fbda` 上、早于 `ac21b91`;但仍按上一轮的教训在当前 HEAD 上实际重放确认 PASS,一行代码没改。
  #222 是真的,而且是这个家族(#123–#167)的下一批:seed 是一个 777777776 次迭代的拼接循环,target
  `FuzzAutoPromote`,run 跑在上一轮的合并提交 `093f7d1` 上。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 死因不是「seed 太重」也不是内存 | — | 两条都被实测否掉:seed 本地重放只要 **0.77 秒**、在自己 corpus 里只是**第三重**的(1.22s / 1.21s / 0.77s),两个更重的早该先死;单 seed 峰值 RSS 只有 **106 MB**,而 CI 用的是 `GOMEMLIMIT=512MiB`(纯软限制),整个 corpus 并行重放峰值 525 MB。**定性靠的是读 `llmdoc/guides/unreproducible-crasher-triage.md` 里这个家族自己的结论**:#166 那轮已经查清死因是 **CPU wall-clock 触发 Go fuzz 的 10 秒 per-input 看门狗**、不是内存,而那一节还把剩下的候选按名字列了出来 |
  | 三个候选算子确实是同类风险 | `internal/stdlib/{stdlib,stringlib,tablelib}.go` | 在 1<<20 step budget 内、**并且完全没有触发预算**的情况下,`string.rep` / `string.format` / `table.concat` 各自的紧循环分别跑 **21 秒 / 20 秒 / 53 秒**——单次 `prog.Run` 就已超过看门狗,而 `FuzzAutoPromote` 每个输入要跑**四次** Run |
  | 三者走**同一个**计量器 | `internal/crescent/state.go::ChargeBulkWork` | `chargeBulkWork` 导出后三个函数各自按**产出字节数**记账,于是「批量工作」在预算里只有一个定义;各自定一个阈值的话它们迟早互相漂移,而**哪个先触发**会变得难以预测,并且预算本身是可加的量、多个阈值表达不了「几种批量操作叠加起来超了」。21/20/53 秒变 **46/90/70 毫秒**且预算正确触发 |
  | `table.concat` 按**字节**而不是元素个数 | `tablelib.go::tableFnConcat` | 它的遍历本来就被表的长度界住,所以按元素个数看永远便宜——**256 个元素**听起来微不足道,而每个元素 2 KiB 时实际要 **53 秒**。代价是拼出来的字节数,记账就要读那个量 |
  | 普通写法不受影响 | `test/regression/issue222_bulk_builder_test.go` | 八种常规写法(`string.rep("ab",3)`、**1 MiB 的 `string.rep`**、`string.format("%s-%d",…)`、`table.concat({1,2,3},",")`、1000 与 **10000** 元素的 `table.concat` 等)与 lua5.1 逐字节一致;1 步 / 64 字节的比率下 1 MiB 产出约记 16K 步、占 1<<20 预算约 1.5%。按 triage guide 的规定**没有自建 in-test deadline** |

  **验证规模**:两个回归测试全绿(0.20 秒 / 0.00 秒);#222 的 seed 入
  `testdata/fuzz/FuzzAutoPromote/ad96f441153507ff`。过程反思见
  `llmdoc/memory/reflections/2026-08-03-issue221-222-bulk-builder-budget.md`(四条教训:一个家族的第 N
  次复发先读那个家族自己的结论而不是从现象重新推 / 记账的计量单位要与真实成本同量纲 / 新增的资源判据
  要复用既有的计量器不要自建第二个阈值 / 「本地重放干净」对这个家族天然无效,落盘的必然是最小化后的
  轻输入)。

- **同一族的下半句:预算界住了,但余量不够(2026-08-04,#224 / #225 一轮,产品代码零改动)**:这一族的
  第七、第八个 nightly crasher,两个 seed 都是「约 90 字节的字面量在 `for i=1,777777776` 循环里拼接」
  且赋值目标被 fuzzer 写错(`qut` 而不是 `out`,所以累加器根本不增长——最小化后的 seed 天然是轻的)。
  两个都**既不崩也不分歧**,字节记账正常把它们界住,本地重放各约 1 秒就正确抬「instruction budget
  exceeded」。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 「已被上一轮修好」这个归因是错的 | — | 两个 run 的 headSha 都是 `093f7d1`、早于 #222 那一轮的合并,按前几轮的流程就该判第一档结案。这一轮多做了一步:**把 seed 也拿到 `093f7d1` 上跑**,结果它们在**那里也已经被界住**——所以 #222 那一轮不是修好它们的原因,「过期、已修复」这个结论不成立;run 时间确实早于合并,但那个事实与这两个 issue 为什么被开出来无关 |
  | 真正的问题是 harness 的**余量** | `internal/fuzzbudget` / `fuzz_auto_test.go` / `fuzz_p4_test.go` | `1<<20` 在 1 步 / 64 字节下允许约 **64 MiB** 的 concat,本地每个 fuzz 子测试 **0.7–1.3 秒**;而 **CI 运行器比本地慢约 10 倍**——这个倍率 `internal/crescent/state.go` 的 `chargeBulkWork` 注释里早就写着(`>>6` 那个比率本身就是按最慢的 CI runner 收紧出来的)。最慢的家族 seed 推算到 CI 是 **12–13 秒**对 **10 秒**看门狗:六个 seed **两个已经超过**、四个余量不到 **1.4 倍**。机制是日志印证的而非推断:nightly 日志里就是 `panic: deadlocked` |
  | 修法:四处 `SetStepBudget` 共用一个常量,终值 `1<<16` | `internal/fuzzbudget.Steps` | build tag 与消费方一致(`(wangshu_p3 || wangshu_p4) && wangshu_profile`)——第一版没加 tag 被 golangci-lint 判 unused,因为消费方都在 tag 之后、默认构建看不见。corpus 全量重放 5.5 秒 → **0.85 秒** |
  | **第一版的 `1<<19` 被审计推翻:上限要按「计费相同时最贵的写法」定** | `internal/fuzzbudget.Steps` | 减半到 `1<<19` 只修好了 corpus 里那六个 seed(它们确实都拿到 ≥1.8 倍余量),而**邻域仍在看门狗之上**——按四次 Run × 慢 10 倍推算,`1<<19` 下 `out=out.."x"` 循环 **14 秒**(余量 0.70 倍)、`t[tostring(i)]=i` 循环 **18 秒**(0.56 倍)、`s:gsub("%a","x")` 循环 **41 秒**(0.24 倍,是看门狗的四倍、比被修的 seed 还糟)。根因:`gsub` 每次调用按大约两倍主串计费,实际工作远多于等额计费的 concat——**等额计费不等于等额 wall-clock**。`1<<16` 让最坏那条降到 **5.2 秒**、余量 **1.93 倍**,是第一个满足数量级判据的值 |
  | 覆盖不损失这次是**实测**的,不是断言 | — | harness 自己的 seed corpus 在 `1<<20` / `1<<19` / `1<<16` 三个预算下 `PromotionCount` **完全相同**——那些写法在几次调用之后就升层,从不接近任何一个上限。**注入一个真实的 P1-vs-P4 分歧**(把升层侧的返回值截断)确认 `1<<16` 下 harness 仍然 FAIL、撤掉注入后通过;90 秒引导式 fuzz 干净 |
  | 据实记下变窄的一处(收窄而不是空洞) | `test/regression/issue144_regression_test.go` | `1<<20` 时 corpus 里有两个 seed 会把 arena 推到上限,`1<<16` 时没有,所以那条 arena-cap 错误分支在这里覆盖到的写法变少了。issue144 的回归直接覆盖 arena cap;而且实测两个预算下 step budget 都**先于** arena cap 触发,所以这条路径本来就不是靠 step budget 到达的 |
  | 回归测试原先并不防它声称防的东西 | `test/regression/issue224_watchdog_margin_test.go` | 它在 harness 自己的预算设定下量代价(不是量 corpus 的耗时),但审计实测把预算调回 `1<<20`(正是那个回归)时它**照旧通过**:上界写了 1 秒而它自己的注释里推导出的是 **250 毫秒**(`10 秒 / 10 倍 / 4 次 Run`),而且它拿「四次 Run 的推算」比「一次 Run 的测量」;只用被开成 issue 的那两个 seed 也分辨不出 `1<<16` 与 `1<<19`。现在上界改成推导出的 250 毫秒、用例表加进上面那三个真正约束预算的写法,能同时抓住 `1<<19` 与 `1<<20`,**已用变异实测确认** |

  **验证规模**:两个 seed 入 `testdata/fuzz/FuzzAutoPromote/6ed94d7f9fe6248a` 与 `841bbefecf338b0d`;
  五种构建组合 vet 干净;3 个 commit(`4c89799` 第一版减半、`f844bf1` 文档、`d3928f5` 审计之后改按最贵
  写法定值)。判据写在 [12](./12-testing-difftest.md) §4.9a2(一个预算「界住」还不够,它允许的量必须与
  外部看门狗差一个数量级;「可达的最坏」要按同一计费额度下最贵的写法量)与
  [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §3.4(P4 fuzz
  harness 的 step budget 设定)。过程反思见
  `llmdoc/memory/reflections/2026-08-04-issue224-225-watchdog-margin.md`(六条教训:版本核对之后还要把
  seed 拿到那个旧 commit 上跑一遍以验证归因 / 一个上限只要界住还不够,它允许的量必须与外部看门狗差一个
  数量级 / **上限要由「计费相同时最贵的写法」定,不能由「恰好被开成 issue 的那个写法」定** /
  **一个「防住某个数值」的回归测试必须用变异实测确认它真的会因为那个数值变化而变红** / 降低 fuzz 预算
  不等于降低覆盖,但要用注入缺陷加白盒计数器证明 / 常量要与它的消费方共享 build tag)。

- **两个真缺陷、互不相关(2026-08-04,#228 / #229 一轮,`a11334b`)**:两个 nightly crasher 分别来自
  p1 job 的 `FuzzOracleDiff`(#228)与 p4 job 的 `FuzzP4ForceAllPromote`(#229),run 都在 `efa9aa6` 上,
  但版本核对这一轮不是关键——**两个都在当前 master 上如实复现、都是真缺陷,而且互不相关**(seed hash
  不同、子系统不同,「会不会被同一个改动一起解决」这一格给出的是否)。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | `unfinished capture` 抬得太早(#228) | `internal/stdlib/pattern.go` + `stringlib.go` | 不是少了检查,是**抬错的位置**错了:PUC 从 `push_onecapture` 抬,也就是一个捕获**真的被读出来**的时候,而 `add_value` 只有表替换 / 函数替换 / `%n` 展开三条路径走到那里——纯字符串或数字替换走 `add_s`,只展开 `%n`、从不读捕获。所以 lua5.1 里 `gsub("abc","(","r")` 返回 `"rarbrcr", 4` 而 `match`/`find` 抬错。望舒在 `collectCaptures`(生产侧)就抬了,于是所有消费者都早抬。修法:`capResult` 加 `unfinished` 标记,`capsToValues`(本文件对应 `push_onecapture` 的函数)在**读取时**抬(10 §6.4.1) |
  | 第一版修法漏了表替换那条路径 | `internal/stdlib/stringlib.go` | 我只在 `%n` 展开那条路径加了检查,而**表替换会无条件读捕获 1**(PUC 的 `add_value` 在 `lua_gettable` 之前就调 `push_onecapture`),于是 `gsub("alo","(.",{})` 本该抬错却成功。**抓到它的是官方测试套 `test/luasuite/testdata/pm.lua:193`,不是 oracle seed**——那个 seed 只覆盖「不该抬错」这一侧。判据:懒抬错要在**每个**物化点各加一次检查;改语义边界之后必须跑官方套(seed 单向、官方套双向) |
  | 嵌套 `executeFrom` 返回后没恢复 caller 的 top(#229) | `internal/crescent/call.go::doReturn` | gibbous 的 `TailCall` helper 以 `entryDepth = ciDepth-1` 开一层**嵌套** `executeFrom`,于是「离开这一层的 entry 帧」**不等于**「离开最后一个 Lua 帧」;`doReturn` 在终止分支仍按 `dst + wantedN` 收窄 top,caller 的活寄存器留在 top 之上,GC 的 `visitThreadValues` 把 `[top, size)` 清成 nil,`A={0}` 的 NEWTABLE 结果被清掉、SETLIST 报「not a table」。**是既有缺陷**(把 #228 的改动 stash 掉同样复现,从 `origin/master` 就在)。对照 `lvm.c` `OP_RETURN` 的 `if (b) L->top = L->ci->top`;**同一条纪律的第四处**,前三处(`doReturn` 非终止分支 / gibbous `DoReturn` / `callHost`)早就在做(05 §7.2.1,P4 [implementation-progress](../p4-method-jit/implementation-progress.md) §27) |
  | 分诊三次走错方向 | — | ① peroptranslator 里那段注释逐字写着 `NEWTABLE head + SETLIST → "SETLIST: not a table"`,**探针一行没打出来**,那条路径根本没走到;② stale base,每个副作用后都 `RefreshJitCtxAddrs`,不解决;③ 在 `doSetList` 抬错点打栈迹拿到 `executeLoop -> doSetList`、**没有任何 JIT 帧**,才定位到破坏在更早的已升层调用里。坏值 `tag=65528` 就是 `value.TagNil`,而 nil 是**清理动作**的值、不是任何指令的自然产物 |

  **验证规模**:两个回归测试(`fuzz_228_test.go` 十条用例 —— 四条不该抬错 + 六条必须抬错;
  `fuzz_229_test.go` 比 P1 与 P4 forceAll 两路结果)**都用「把修复还原掉」实测确认会变红**;
  官方测试套含 `pm.lua` 整文件全绿;两个 seed 入 `testdata/fuzz/FuzzOracleDiff/b38e375dae54ee4b` 与
  `testdata/fuzz/FuzzP4ForceAllPromote/6e4264d640c40292`。过程反思见
  `llmdoc/memory/reflections/2026-08-04-issue228-229-lazy-capture-and-nested-tailcall-top.md`
  (五条教训:懒抬错要在每个物化点各加检查、两侧都要有用例 / fuzz seed 单向、改语义边界后必须跑官方套 /
  「有一段既有注释正好描述了这个症状」是最容易走错的线索,先用探针证明那条路径真的被执行 / 抬错的位置
  不是缺陷的位置,栈迹里缺少哪一层本身就是信息 / 一个共享层的恢复动作已有 N 处兄弟路径在做时,第 N+1 处
  漏做就是缺陷)。

- **三个 crasher、两个根因(2026-08-05,#232 / #233 / #234 一轮,`2b1aeb4`)**:**定时巡检任务(每天
  03:07,前一天刚建,辅助脚本见 `.claude/patrol/`)的第一次实际处理轮**。三个 `FuzzOracleDiff` crasher
  **全部在当前 HEAD 上如实复现**,而 **#232 与 #233 是同一个根因家族**(引用值地址的宽度),所以实际是
  两个修复加一个既有兄弟缺陷。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 地址逃过了归一化(#232) | `internal/oracle/compare.go` | `addrRe` 用 `\b` 锚定类型名,而 `\b` 的 word 字符**包含数字和下划线**:`io.write` 不带换行,于是 `io.write(0)print(print)` 的输出是 `0function: 0x...`,`0` 与 `function` 之间没有 `\b`,地址整段逃过归一化——**凡是这种写法都必然分歧**。锚点本身有正当理由(防止把脚本自己的 hex 归一掉),坏在写法。改成「不是字母」,替换随之改成只重写匹配内部的 `0x...` 再把前导字符放回去(12 §4.3a) |
  | 脚本**测量**地址的长度,归一化到不了(#233) | `internal/oracle/prelude.go` | `t={0}print(#tostring(t))`:PUC 21(`%p` 在本平台给 12 位)、望舒 17(`0x%08x` 给 8 位),**长度在归一化之前就分歧**,`NormalizeOutput` 拿到的字节流里只有 `21` 和 `17`——与 `string.len(0/0)` 4 对 3 完全一样的机制。修在**渲染处**:prelude 包一层 `tostring` 把 PUC 自己的地址渲染成望舒的 8 位,与 NaN 符号位是同一个选择;望舒侧的宽度成为约定。**归一化管值、渲染处管宽度**(12 §4.3a) |
  | gsub 替换串里的 `%` 转义(#234) | `internal/stdlib/stringlib.go::st2gsubRepl` | PUC `add_s` 的 `%` 分支有三个出口加一个边界情况,望舒只对了两个:① **末尾的裸 `%`** 让它 `i++` 然后越过长度读 `news[i]`,读到 `lua_tolstring` 保证的 NUL、每次匹配吐一个 NUL 字节(`gsub("aaa","a","x%")` 是 `x\0x\0x\0`,hexdump 实测),望舒要求 `i+1 < len(rb)` 把它当成了字面量;② `%` 后**任何非数字**原样吐出,`gsub("a","a","%z")` 是 `"z"` 而不是报错——**这一半是既有缺陷**,在 base 上同样分歧、只是没有 issue 记它(10 §6.5.2) |
  | 「照抄一个越界读」的判据 | — | 这不是 C 的 UB(`lua_tolstring` 保证那里有 NUL,读它有定义)也不是参照实现自相矛盾,而是**有定义但可疑**;决定照抄的是**口径**——oracle 逐字节比较,不照抄的话凡是以 `%` 结尾的替换串就永远不可比,而且那个差的字节还会流进长度、比较、表键。注释里写清了这是怪癖不是规则 |

  **#233 为什么看起来是间歇的**:#232 那种「原始形式」是否分歧,取决于真实地址恰好需要几位十六进制
  (正好 8 位时两侧碰巧一样长);而 #233 那种「测量长度」是稳定分歧的。**同一个根因、两种可见度**——
  分组时不要按症状把它们分成两件事。

  **验证规模**:三处修复各自用「把该修复还原掉」实测确认会变红——
  `internal/oracle/normalize_addr_test.go::TestNormalizeAddrPrefixValidation`(六条必须归一 + 四条必须
  原样,**两侧都要有**:只写前半时「把锚点整个删掉」也会通过)、
  `fuzz_234_test.go::TestGsubReplacementEscapeMatchesPUC`(四条改了行为 + 三条必须不变:`%%`、`%1`、越界
  下标仍抬错)、`fuzz_234_test.go::TestAddressLengthIsComparable`(望舒侧的宽度约定:`#tostring({})` 是
  17、`#tostring(print)` 是 20)。三个 seed 入 `testdata/fuzz/FuzzOracleDiff/`。过程反思见
  `llmdoc/memory/reflections/2026-08-05-issue232-234-address-width-and-gsub-escape.md`(五条教训:一批
  crasher 里同一个根因可能以不同的**可见度**出现,不要按症状分成两件事 / 归一化只能救「被打印出来」的
  差异,救不了「被测量」的差异 / 一个正则锚点的**反例集合**要用它自己的失败输入去检验,不能靠读它的
  注释 / 修参照实现的语义时「只修被报的那一半」会留下已知的洞,一个分支的**所有出口**都要对 / 照抄
  参照实现的越界读怪癖是可以的,前提是判据来自「**比较的是什么**」)。

- **六个 issue、一个根因、而且第一次判错了(2026-08-09,#236–#241 一轮,`81acb57`,产品代码与测试零
  改动)**:六个 nightly 自动开的 issue,标签 `ci` **不是 `bug`** —— 这一轮不涉及任何语义,全部落在
  工程侧,记在这里是因为它改变了**怎么读 nightly 的失败**这一条口径(12 §8.1)。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | **诊断先判错了**(头条) | — | 失败步骤报 `exit code 28`,当天早上的巡检把它读成 **ENOSPC**(磁盘写满,errno 28)并据此提了「在 oracle 源码构建前腾空间或缓存构建产物」的建议。**28 是 `curl` 的 `CURLE_OPERATION_TIMEDOUT`**:那一步是 curl,shell 报的退出码来自 **curl 自己的退出码表**、不是 errno 表,两张表恰好在 28 这个数字上都有条目而且都在讲一种资源类失败(空间 / 时间),所以错的解释读起来完全自然。判据:**看到一个数字退出码,先定位是哪个命令退出的,再查那个命令自己的表** |
  | 两条免费的反证一开始就在日志里 | — | ① `apt` 在 16:55:03 成功结束,然后**沉默 2 分 15 秒**才报 exit 28——磁盘写满是**立刻**失败的,会先卡的只有等待类失败;② `no space left` / `ENOSPC` / `disk full` 在两个 run 的日志里出现次数都是 **0**。判据:**分类一个 CI 失败时先把时间戳减一遍**,时间形式往往比错误码更能定类,而且它在日志里免费 |
  | 真根因 | `.github/workflows/nightly-diff-fuzz.yml` 等四处 | 裸的 `curl -sLO https://www.lua.org/ftp/lua-5.1.5.tar.gz`,**没有 `--max-time`、没有 `--connect-timeout`、没有 retry**,上游一次可达性抖动就一直卡到 shell 放弃 |
  | **后果比「一次红」更糟** | 同上 | 这一步失败让后面三个差分 fuzz 步骤被 **skip**(step 5/7/9 是 `skipped`),那一轮报 failure 而**实际什么都没测**、探索预算为零——而红色的默认含义是「跑了并且发现了问题」,两者在 Actions 页面上是同一个红叉(12 §8.1) |
  | 一次抖动开了六个 issue | 同上,triage 段 | infra issue 标题嵌了 `${{ matrix.variant }}`,而 **infra 失败天然横跨所有 tier**(divergence 失败才天然属于某个 tier),p1/p3/p4 三个标题让按标题去重看不出它们是同一件事:**两次抖动 × 三个 tier = 六个**。起初改成按 `${{ github.run_id }}`(三个 job 共享)去重,后来又改成**按日期**去重(一天六轮,按 run id 仍会一次抖动开多个),同一天之后报的 job 改为评论,tier 挪进正文——**去重键与信息量是两件事**([engineering](../engineering.md) §3.2) |
  | 取包统一处理 | `scripts/fetch-lua-tarball.sh`(新增) | 四处 call site(`ci.yml` ×2、`bench-acceptance.yml` ×1、`nightly-diff-fuzz.yml` ×1)统一改用它。四个性质:限时 / 重试(`--retry` 才是关键 —— 它的默认值是 0,所以旧的裸 curl 根本不重试。`--retry-all-errors` 只是额外放宽,**不是**超时重试的前提:curl 手册写的是「transient error means **either: a timeout**, an FTP 4xx ... 」,所以单靠 `--retry` 就能覆盖 #236–#241 那次失败。此前把它写成前提是错的,记在这里因为那曾是保留这个 flag 的唯一理由。|
  | 自测本身不能是新的抖动源 | `scripts/test-fetch-lua-tarball.sh`(新增) | 挂进 `make test-scripts`(#179 定下的检查纪律),五个用例**全部离线**(用 `file://` origin 冒充上游)——一个「防住外部抖动」的测试如果自己依赖上游,它加的是噪声不是防线。每个用例都用变异实测过 |
  | 那个自测第一版假绿 | 同上 | 「curl 调用带限时标志」这一条**第一版 grep 整个文件**:把 `--max-time` 从调用里删掉后照旧通过,因为那个词在脚本顶部的注释里还在(注释正好在解释「bounded: `--connect-timeout` and `--max-time`」)。现在只截取那条 curl 调用再检查。判据:**检查代码属性的测试要作用在代码本身上,一个注释就能满足的测试没有在测代码** |

  **当时的已知缺口(已补上)**:这一轮结束时,取包和去重都修好了,但「本轮未执行任何差分」这句话还没有
  出现在任何地方,infra issue 的 body 只说失败原因的类别。后来 infra issue 的 body 改为读
  `steps.difffuzz.outcome`,写明本轮差分是否执行,[engineering](../engineering.md) §7 对应条目已划掉。

  过程反思见
  `llmdoc/memory/reflections/2026-08-09-issue236-241-curl-timeout-misread-as-enospc.md`
  (五条教训:同一个数字在不同的退出码表里含义不同、先确认这个退出码是**谁的** / 时间形式是区分故障
  类别的**免费**证据 / 一个失败步骤让后续步骤 skip 会造出「报红但什么都没测」 / 自动开 issue 的
  去重键必须与「一次事件」的粒度对齐 / 检查「某个标志存在」的测试不能 grep 整个文件)。

- **崩的是参照实现,望舒零改动(2026-08-11,#244 一轮,`a22de53`)**:定时巡检发现的 p1
  `FuzzOracleDiff` crasher,seed 是 `A(unpack({},0X80000000))`。**版本核对这次很干净**——失败 run 的
  headSha 正好**就是当时的 master**(`e0e7b34`),不可能是过期 seed;在当前 HEAD 上重放确实复现。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | **崩的是 oracle,不是望舒**(头条) | — | 重放拿到 **SIGSEGV 且栈迹在 cgo 里**(`_Cfunc_wangshu_oracle_exec`),拿真的 `lua5.1` 二进制直接试同样 **dumped core**,而望舒对同一输入抬 `too many results to unpack`、行为正确、从不崩。判据:差分 harness 报 crash 时先读**栈迹属于哪一侧**再决定往哪边查——「fuzz 报了 crash」不等于「被测实现有 bug」,参照实现也会崩而且更容易被误判成我们的问题(拿真参照实现二进制跑一次最便宜、单独就能定性) |
    | 机制 | `internal/oracle/_lua515/src/lbaselib.c`(`luaB_unpack`)+ `lapi.c`(`lua_checkstack`) | `i`/`e` 经 `luaL_optint`/`luaL_checkint` 窄化成 **int**,`n = e - i + 1` 的减法是**有符号溢出 UB**;gcc `-O2` 把 `n <= 0` 检查当不可达删掉,`lua_checkstack` 收到负 `size` 而拒绝条件对负值两个都为假 → 段错误。**-O0 下同一份源码干净抬错,崩溃依赖优化等级**(此前记的「回绕成正的巨大值绕过检查」是错的:`i <= e` 时回绕恒 `<= 0`) |
  | 修法:差分侧跳过 | `internal/oracle/prelude.go` | prelude 包一层 `unpack`,索引落在崩溃窗口时抬 `LimitSentinel`,与其余 PUC UB range 一致(**会死的 oracle 不能当参照**,12 §4.9b 第三格)。望舒侧**零改动**——它本来就正确 |
  | 望舒侧的行为约定 | `fuzz_244_test.go::TestUnpackAtInt32BoundaryDoesNotCrash` | 边界两侧 + `INT_MAX` 起点 + 小负起点 + 显式区间 + 整表 unpack:该抬干净错的抬、该给正确答案的给,从不崩(10 §4.5) |
  | **守卫经六轮审计后完成:区间读 `i` 与 `e`、窄化走 `__ckint0`、助手在脚本前捕获、非表首参不跳;用例见 `internal/oracle/unpack_guard_test.go`(13 skip / 12 compare,两向变异确认)。**补上 `e` 后规则完全可推导:**`i32 <= e32 且 (e32 - i32 + 1) > INT_MAX`**,10 组「先预测再实测」全部吻合。**太窄**:`A(unpack({1,2,3},-2147483646))` 经真实 `FuzzOracleDiff` 实测让**整个测试二进制 SIGSEGV**(这个问题家族仍未消失);**太宽**:`unpack({1,2,3},4294967297)` 窄化成 `i32 = 1`、两侧都返回 3,却拿到 sentinel 被 **SKIP**。口径见 12 **§4.9f**,待修 |

  **2026-08-11 那一轮本身是文档轮 + 复核轮,没有改任何代码或测试**;上面「已知缺口」那一行是复核的产出。
  判据写在 [12](./12-testing-difftest.md) §4.9b 第三格(UB 且参照实现直接崩)与 §4.9f(一条 skip 的
  **区间**要按机制定,不能按被报的那个写法实测出来)、[10](./10-stdlib.md) §4.5(`unpack` 的索引语义)、
  [engineering](../engineering.md) §3.2 与 §7。过程反思见
  `llmdoc/memory/reflections/2026-08-11-issue244-oracle-segv-unpack-int32.md`(三条教训:差分 harness 报的
  crash 第一个要问「谁崩了」 / 「实测」与「推导」不是互斥的——一个被称为「实测得来」的边界仍然需要机制来
  确定它的**形状**,否则实测只覆盖到采样点所在的那一格,而且这个错误会自我确认 / 限制类守卫的两侧用例要
  沿**机制**的每个维度取,不能只沿被报的那个写法取,两种失效反馈还不对称——太宽表现为测试通过、太窄表现
  为下一次 nightly 再开一个 issue)。

- **两个问题都先诊断错了(2026-08-19,commit `9b61079`,产品代码零改动)**:nightly job 一周内被 job
  超时(350 分钟)掐掉三次(08-12 p4、08-15 p4、08-18 p1),其中两次真丢了覆盖。这一轮不涉及任何语义,
  只改 `.github/workflows/nightly-diff-fuzz.yml`,记在这里是因为它纠正了**步骤级超时的作用域**与
  **fuzz 预算的分配依据**这两条口径(12 §8.2)。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | **问题一:作用域错**(头条 A) | 装 oracle 步骤 | #236–#241 那轮给取包脚本的 curl 加了限时,但同一步里 `apt-get`/`make`/`check-oracle.sh` 三条命令仍然无界——**只框住了当时报错的那一条命令,不是那一整步**。08-18 那轮 p1 job 在这一步卡了 **5 小时 50 分**被 job 超时掐掉,rolling-seed 与 auto-mode 被 skip、两个 go-fuzz 步骤根本没启动。普查后发现无界的步骤是**三个不是一个**(装 oracle、upload logs、triage) |
  | 修法 | 三个步骤各加 step 级 `timeout-minutes`(12/15/10) | 选 step 级而不是逐条包 timeout,是因为它对**以后新加的命令同样生效**——这正是这次出问题的地方 |
  | **问题二:量错了成本中心**(头条 B) | native go-fuzz 步骤 | 连续五轮建议把 auto-mode 从 150m 压到 90m,**实测这个建议省不下任何时间**——auto-mode 只跑 2 分钟,150m 是从没接近过的防卡死超时上限。真正的成本中心是 native go-fuzz 步骤,**312 分钟的 job 里它占 270 分钟(87%)**,而这一步从没被量过 |
  | 270 分钟的原因 | `scripts/go-fuzz.sh` + workflow input description | `go-fuzz.sh` 按**源码扫描**发现目标、每个目标跑满 fuzztime;无 tag 可见目标是 **6 个**不是 description 里写着的「4 targets」——root 包 `FuzzCompileRun`/`FuzzAutoPromote`/`FuzzP4ForceAllPromote` + `internal/frontend/lex`/`internal/frontend/parse`/`internal/stdlib` 各一个(`FuzzOracleDiff` 被 `wangshu_oracle_cgo` gate、在自己的步骤里跑,不算在内)。`6 × 45m = 270(审计实测补正:各层实际跑到的目标数并不相同 —— p1 4 个、p3 5 个、p4 6 个 target-run,所以 45m 下三个 job 分别约 180 / 225 / 270 分钟,p4 恰好 6 × 45;35m 之后约 140 / 175 / 210 分钟。另外 `go-fuzz.sh` 对 golang/go#75804 的假性 deadline 会把该目标重跑一次,于是最坏情形要再加一个 fuzztime —— 审计抓到一次 45m 下真实跑到 315 分钟的 run,所以 go-fuzz 那一步的 step 超时按「最慢的那个 job + 一次重试 + 余量」取 300 分钟,而不是按中位数取。)`,与实测吻合——这个过时的「4」正是这一步成本被长期低估的原因 |


**审计逐轮补正**(三轮各修一处定值错误,保留过程):

- 审计第二轮补正两点:(1)#75804 的重试是**按目标**而不是按 job 的 —— `go-fuzz.sh` 对每个发现的目标各调一次 `run_target`、各有两次尝试,跨目标没有任何上限,所以 p4 那个 job 可以有两个不同目标各重试一次,得 210 + 35 + 35 = 280;而先前把 go-fuzz 那步的上限正好取成 280,等于在一次**健康**运行上就把余量吃光 ——「按一次重试取」与「按中位数取」是同一个错误往外挪了一格。现取 **320**。(2)另外三步(rolling-seed、GC-stress、auto-mode)先前只靠内层 `go test -timeout`,而 `-timeout` 只看着 `go test` 那个进程,**以后往这一步新加的命令仍然无界** —— 那正是装 oracle 那步卡了 5 小时 50 分的成因。现在每个带 `run:` 的步骤都有 step 级上限,实测总时长约 215 / 217 / 252 分钟(p1/p3/p4),对 350 的 job 上限余量 98~135 分钟。

- 审计第三轮补正:step 上限是**两侧**约束,而我前两轮都只看了一侧 —— 下界要盖过合法最坏情形(p4 两次重试 280),上界要**在 job 上限之前触发**,即小于 350 减去这一步之前已消耗的时间(约 40 分钟)。取 320 时会在约 360 分钟触发,已在 350 之后,于是这个上限**永远不会生效**,真卡死时依旧退化成「cancelled、triage 被 skip、静默丢覆盖」—— 正是这次改动要消除的结果。窗口是 280 < bound < 310,现取 **300**(实测三层分别在 338 / 340 / 340 分钟触发)。oracle-diff 那步同理:合法最坏 70 分钟、窗口上界 172,取 120。

- 还有一处值得记的自查失误:我写脚本核验「每个 step 上限都能在 job 上限之前触发」时,用子串匹配去查每步的实测耗时,而 `auto-mode rolling-seed diff` 这个名字**同时**含有 `auto-mode` 与`rolling-seed`,匹配先命中后者,于是给它记了 36 分钟而不是 2 分钟 —— 前置耗时被算成 74 而不是 40,脚本因此报「300 也不可达」。改成按前缀精确匹配后三层都可达。判据:用名字做键去查表时,先确认**没有一个名字能匹配多个键**;这类错误不会报错,只会给出一个看似严谨的错数字。  | 修法 | `gofuzztime` 45m→35m | `6 × 35m = 210` 分钟,job 约 250 分钟,余量从约 38 分钟升到约 98 分钟。**真实减少约 22% 的每轮探索量**,选它而不是砍差分步骤是因为差分步骤实际便宜(rolling-seed 36 分钟、auto-mode 2 分钟)而这一步占 87%;description 里的「4 targets」改成「6 untagged targets」 |  | 自我纠正 ①:抬到 420 不可能 | — | 连提四轮把 job `timeout-minutes` 抬到 420,而 GitHub 托管 runner 单 job 硬上限是 **360 分钟**,推荐了四轮却从没查过这个平台限制 |  | 自我纠正 ②:求和框架本身是错的 | — | 曾按「所有 step 上限之和必须低于 job 上限」算出某个 job 最坏 548 分钟、判「只能大幅压缩」;**这个框架本身是错的**——job 超时才是总预算,step 上限只是防一条卡死的命令悄悄吃掉这个预算,两者不是同一件事,不该按求和去配 |

- 五条判据写进 `llmdoc/guides/unreproducible-crasher-triage.md`「给一条报错的命令加超时」与「job 超时  与 step 超时」两节、`llmdoc/guides/design-claims-vs-codebase-physics.md` §3.1(优化建议先量成本  分布)与 §5(陈旧计数);过程反思见  `llmdoc/memory/reflections/2026-08-19-nightly-step-timeouts-and-budget.md`。

- **索引表达式跨行时报错行号标错,两处独立错误各修一处(2026-08-27,#248 一轮,`1693a69`)**:nightly
  开出的 p1 `FuzzOracleDiff` crasher,seed 带协程但**最小化之后协程是噪声**——剥掉协程与 `pcall` 之后
  `print(pcall(function() return A\n.A end))` 一样分歧,关键写法是**索引运算符与对象/键之间隔着换行**
  (`A\n.x`)。版本核对干净,失败 run 的 headSha 正好就是当时的 master,当前 HEAD 重放确实复现。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 分歧是行号,不是消息 | — | oracle 报 `:2:`、望舒报 `:1:`,消息本体一致(`attempt to index global 'A' (a nil value)`);真 `lua5.1` 二进制同样报 `:2:` |
  | 根因一:对象被盖上运算符的行 | `internal/frontend/compile/codegen.go::exprIndex` | 把对象交给 `exp2AnyReg(e.Line, &obj)`,而 `e.Line` 是索引运算符的行,延迟加载的对象(GETGLOBAL/GETTABLE)因此被盖上运算符的行。改用 `e.Obj.Pos()`(键用 `e.Key.Pos()`) |
  | 根因二:GETTABLE 没有自己的行 | `internal/frontend/compile/expdesc.go` | `EIndexed` 这个 `expDesc` 不带行号,它的 GETTABLE 在后续任意一次 `dischargeVars` 才真正发射,取的是「discharge 那一刻调用方传入的行」——对 `local v = A\n.x` 就是 `LocalStmt` 所在行(行 1)而不是运算符自己的行(行 2)。加 `opLine int32` 字段,GETTABLE 优先取它 |
  | 两处任一单独修复都不够 | — | 修前 dump 出的 `LineInfo` 是 `pc0 GETGLOBAL line=2、pc1 GETTABLE line=1`——两条指令行号都不对且方向相反;只修一处会让另一条指令继续错 |
  | 既有测试为什么长期假绿 | `internal/frontend/parse/parser_test.go::TestIndexExprLineIsOperatorLine` | 用局部变量做对象——局部变量已经在寄存器里,`exprIndex` 不发射任何指令、GETTABLE 立即 discharge,两处错误在这条路径上都不可能出现。新测试 `issue248_index_line_test.go::TestIndexLinesAcrossNewline` 改用全局(触发延迟加载)并核对整张 `LineInfo` 表 |

  三条判据:①**位置/行号类测试的载体必须触发延迟加载**——判据是「这个写法会不会让编译器发射我关心
  的那条指令」,不发射就测不到([[prove-the-path-under-test]] §2.1);②**一处最终症状可能由两处独立
  错误叠加**——改完先核对整张中间数据结构(本例是 `LineInfo`),不能只看错误消息变没变
  ([[cross-backend-semantic-fix-sweep]]「一个症状可能是同一子系统内两处独立错误叠加」节);③**最小化
  要先剥掉「看起来相关」的外壳**——seed 里的协程是噪声,逐个删外层构造、每删一次重跑,别对着 seed 的
  原始形式找因果([[unreproducible-crasher-triage]])。写进 [09](./09-errors-pcall.md) §3.5.2、
  [04](./04-frontend-parser-codegen.md) §5.2.1。过程反思见
  `llmdoc/memory/reflections/2026-08-27-issue248-index-line-across-newline.md`。

- **issue #252 —— 上面那条「根因二」的口径被推翻:GETTABLE 的行是 discharge 点的 lastline(2026-09-03,
  分支 `fix/252-paren-discharge-line`,PR #253,7 commits)**。nightly 又开出一个同族 `FuzzOracleDiff`
  issue,seed `co=coroutine.create(function()(A.A\n)()end)print(coroutine.resume(co))`(标题写「crash」但
  不是崩溃,是错误消息里的行号差分:PUC `:2:` / 望舒 `:1:`)。**版本核对干净**,失败 run 的 headSha 就是
  当前 HEAD。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | #248 的「运算符行」口径是近似 | `internal/oracle/_lua515/src/lcode.c` | `luaK_codeABC`/`luaK_codeABx` **不接收行号参数**,一律用 `fs->ls->lastline`;GETTABLE 记的是**它被 discharge 那一刻**的行。判别输入 `local v = A.x\n\n+1` **完全没有括号**、运算符在行 1,luac 记行 3(`+` 才是 discharge 点)——这排除了「括号特例」与「运算符行」两种模型 |
  | 修法 | `expdesc.go` / `codegen.go` / `stmt.go` / `parse/expr.go` / `parse/stmt.go` / `ast/ast.go` | 删 `expDesc.opLine`,改由调用方传 lastline 语义的行;`parseExprListEnds` 按元素返回物化行(表达式列表每个元素在**它后面那个分隔符**被消费后 discharge:`f(A.A\n,1\n)` 给出 2 和 3),末元素由调用方按闭合 token 填。新增 `ParenExpr.EndLine` / `{Local,Assign,Return,GenFor}Stmt.ExprEndLines` / `{Call,MethodCall}Expr.ArgEndLines` / `calleeEndLine` |
  | 同族缺陷共四格 | — | ① 括号与调用参数(主 case)② 赋值右侧 + `return` + 泛型 for(`ends` 参数接线时传了 `nil`,靠自查清单查出;三处**全都**偏且偏在会 raise 的 GETTABLE 上)③ 单目标赋值快速路径(`storeVar` 用**同一个** `line` 参数同时干「物化 RHS」与「发射 store」两件事,靠 PR #253 远端评审查出,已拆成 `rhsLine` + `line`) |
  | 泛型 for 的时机陷阱 | `parse/stmt.go` | 末元素的行必须在 `check_match(DO)` **之前**取:`GenForStmt` 节点是循环体解析完才构造的,那时 `p.lastLine` 已指向 `end`,直接用会把迭代器的 GETTABLE 推到循环之外。`for k in A.x\n do end`(GETTABLE 归 1)与 `for k in A.x\n, 1 do end`(归 2)这一对用例锁定它 |
  | 第一版修法把 #248 自己的语料改红 | `testdata/fuzz/FuzzOracleDiff/8dff36b8bd115962` | `test-all`/`conformance-all`/`difftest-all` 全绿,只有 `make fuzz-oracle`(重放常驻语料)逮到——`difftest` **不重放** `testdata/fuzz/`,而同族回归最可能正落在那里 |
  | 仍未对齐的残留(不可见) | — | `storeVar` 的 store 行(`x = A\n.x` 的 SETGLOBAL 我们 1 / luac 2,已用能 raise 的 `__newindex` store 验证不产生用户可见差分)、`f{...}`/`f"..."` 糖式调用的单参数物化行(`t.x\n{1}` 的 NEWTABLE 我们 2 / luac 1)、数值 for 的 FORPREP/LOADK 行。已登记 `llmdoc/memory/doc-gaps.md`。**2026-09-18 订正(#262)**:这份清单已作废,其中 `storeVar` 的 store(表目标上是 SETTABLE)和数值 for 的 FORPREP 其实都会 raise;#262 把它们连同其余同族位置全部对齐,21019 个形状逐指令行号 0 差异,见 [04](04-frontend-parser-codegen.md) §5.2.2 订正块与 `internal/frontend/compile/issue262_operator_line_test.go` |

  四条判据:①**自己从参照实现行为归纳出的口径,与别人对源码的陈述一样需要回到源码验证**——能读源码就
  去读(`luac5.1` 五行给出答案),否则构造让两个候选模型给出不同答案的输入
  ([[design-claims-vs-codebase-physics]] §5.1a);②**用例要能区分两个候选模型**——为单目标赋值那一格
  写的 `x = A.x\n` 在两个模型下期望值相同、零区分力,难点不在知道要构造判别输入而在**每加一格都重新
  问一次**([[prove-the-path-under-test]] §2.1a);③**给共用 helper 加「缺失时回退」的可选参数时,每个
  仍传缺省值的调用点都要实测**——回退默认值让漏接线与不需要接线在测试结果上完全同形(同上 §4.7a);
  ④**同族旧语料要和新语料一起当验收检查**(同上 §9.6b)。写进
  [04](./04-frontend-parser-codegen.md) §5.2.2、[09](./09-errors-pcall.md) §3.5.2 订正块。过程反思见
  `llmdoc/memory/reflections/2026-09-03-issue252-discharge-line-is-lastline.md`。

- **issue #271 —— `table.sort` 改为逐步移植 `auxsort`,顺带修正 `<` / `<=` 的元方法规则(2026-10-01,
  分支 `fix/271-table-sort-auxsort`)**。nightly 开出的 p1 `FuzzOracleDiff` 分歧(标题写「crash」,其实是输出不同):
  seed `t={0,0,0%0,0X0}table.sort(t)print(unpack(t))`,oracle 输出 `0 nan 0 0`,望舒输出 `0 0 nan 0`。
  失败 run 的 headSha 就是当前 master,本地重放立刻复现。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 根因:排序算法不同 | `internal/stdlib/tablelib.go::tableFnSort` | 设计稿 10 §7.3 早就写了「移植 5.1 `auxsort`」,实现却是把数组拷出来跑 `sort.SliceStable` 再写回。两种算法只在严格弱序下给出相同结果;NaN 与任何数比较都为假,不是严格弱序,于是元素落在不同位置 |
  | 拷贝还藏住了三类可观察行为 | 同上 | 比较器看不到排序中途的表;比较器报错时表保持原样(5.1 留下排到一半的顺序);永远不会报 `invalid order function for sorting`。改为 `tableSorter` 直接读写表,读写顺序、比较哪两个元素、操作数先后都与 `ltablib.c` 一致 |
  | 共用的 `LessThan` 错误措辞也错 | `internal/crescent/meta.go::LessThan` | 不论类型是否相同一律报 `two <type> values`,`table.sort({1, "a"})` 报 `two string values`,5.1 是 `string with number` |
  | 顺带查出 VM 的 `<` / `<=` 规则偏差 | `internal/crescent/execute.go::doCompare` | 按 `lvm.c`:类型不同**先报错、不查元方法**;只取左操作数的处理函数,且右操作数的必须与之 `rawequal`(`call_orderTM`);处理函数不要求是 function。原实现「先查左、没有再查右」且不比类型,一侧有 `__lt` 时 `A < {}`、`A < 1` 都返回处理函数的结果。P3/P4 的比较慢路径都经 `doCompare`,一处修好三层 |
  | 错误措辞的第三个字母规则 | `execute.go::orderError` | `luaG_ordererror` 只比较类型名的第三个字母,`"string"` 与 `"thread"` 相同,所以 `"s" < co` 报 `two string values`。照抄,不按类型名相等判断 |
  | 计费补一道后备 | `tableFnSort` / `tableSorter.less` | 旧的稳定归并排序在任何输入上都是 O(n log n);快排在 McIlroy 对抗排列上是平方级(3000 个元素约 225 万次比较,正常排列约 3.5 万次),只预付 `n*log2(n)` 会让这类输入几乎不计费。比较次数超过预付额四倍后每次比较再计一个字节当量 |
  | 平方级排序还要能被取消 | `tableSorter.less` / `crescent.State.CheckCancel` | 排数字时不运行 Lua 代码,没有指令边界查 `SetContext`;独立审查实测 20000 个元素的对抗排列在 500ms 超时下跑满 1.55s。每 4096 次比较查一次取消状态 |
  | 跨比较持有的元素没有 GC 根(存量) | `tableSorter.hold` | 独立审查发现:比较器清空表、把参数置 nil 再 `collectgarbage()`,Go 局部持有的 pivot 等元素被回收,下一次比较读到已释放的对象,VM 内部 panic(master 上同样复现)。5.1 把它们放在 `sort` 自己的栈上。改为逐个 `PinRef`;设计稿 10 §7.4「比较时元素都在表槽里,无需 Pin」的定稿一并订正 |
  | 默认比较器报错多了位置前缀(存量) | `tableSorter.less` | `lua_lessthan` 在 C 函数里抛错,5.1 不带位置;望舒的宿主调用边界补上了 Lua 调用方的行号。对这类错误 `MarkAnnotated` |
  | 全局 yield 哨兵被并发写(存量 + 本轮一处) | `crescent.LuaError.MarkAnnotated` / `meta.go::callLuaFromHost*` | 第二轮复核发现:`__lt = coroutine.yield` 时处理函数返回包级共享的 `errYieldSentinel`,上一行加的 `MarkAnnotated` 会写它;master 上元方法调用边界的 `e.argNarg = 0` 也写它,普通 `A < B` 就能让两个 State 在 `-race` 下报竞争。两处都跳过哨兵。同一处的语义问题(协程越过元方法边界挂起,lua5.1 报错)开了 #272 |

  验证:新增 `test/regression/issue271_sort_order_test.go`(排序 18 条 + 比较规则 14 条,期望值逐条用 `lua5.1` 跑出;
  另一条对抗排列测试同时断言步数预算与取消都会触发)与 `issue271_sentinel_race_test.go`(两个 State 并发,`-race` 下
  去掉修复即报竞争);三组在修复前全部失败,后补的 GC、位置前缀、取消三处
  各自去掉修复后也会失败。改完先由独立子代理逐行对照 `ltablib.c` / `lvm.c` 审查并做差分测试,上面最后三行来自这次审查。
  比较器里 yield 的位置前缀、`__lt` 处理函数里 `error(m, 2)` 的层级、`next({}, {})` 的位置前缀、traceback 少一行
  `[C]: in function 'sort'` 几处差异在 master 上就有,与本轮无关,登记在 `llmdoc/memory/doc-gaps.md`。#271 语料入 `test/fuzz/testdata/fuzz/FuzzOracleDiff/`。
  `make test-all` / `conformance-all` / `difftest-all` 与 oracle 语料重放全绿。代价按元素类型不同,主要原因是每次访问都走
  `RawGet`/`RawSet`,不再在 Go 切片上排序。另有一项是自己引入后又收回的:第一版 `lessThan` 先对两个操作数取类型名、
  再判断是不是数字,默认比较器每次比较都多两次 `typeNameOf`;最终全范围审查量出这占数字排序变慢的约三分之一,
  于是在开头补上「两个数字 / 两个字符串」快路径(同类型操作数恰好是类型检查会通过的情况,检查顺序不可观察)。
  P1 构建,本机 base 与 HEAD 交替各跑三轮取均值(每轮三次排序):30 万个数字默认 `<` 0.45s → 0.56s(慢约 25%;
  补快路径前是 0.61s),15 万个字符串 0.50s → 0.55s(慢约 10%),10 万个 table 加 Lua 比较器两次测量方向相反
  (本机快约 9%,独立审查用另一组数据规模量到慢约 25%),只能说在噪声范围内,没有可靠结论。设计稿 07 §9.2-§9.4、10 §7.3 已按实际规则改写。

- **issue #272 —— yield 越界检查补到 `State.Yield`,并修好被尾调用的 yield(2026-10-01,
  分支 `fix/272-273-yield-boundary-and-p4-tailcall`)**。#271 一轮的独立审查发现:元方法处理函数直接是宿主函数
  `coroutine.yield` 时,协程会在比较中途挂起,lua5.1 报 `attempt to yield across metamethod/C-call boundary`。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 根因:基线检查从没实现 | `internal/crescent/coroutine.go::Yield` | 设计稿 08 §5.2 写了 nCcalls 基线比对,实现只在 `callLuaFromHost` 里把「从 **Lua** 函数冒出来的哨兵」改成越界错误。宿主函数被直接当元方法 / 比较器 / gsub 替换函数 / for-in 迭代器调用时,哨兵经 `callMetaHandler` / `ProtectedCall` 原样返回,绕过了这道检查 |
  | 修法 | `coroutine.baseCcalls` + `State.Yield` | 照 `ldo.c`:`lua_resume` 里 `L->baseCcalls = ++L->nCcalls`,`lua_yield` 在 `nCcalls > baseCcalls` 时报错。resume 的 `nCcalls++` 之后记录基线,`Yield` 先比较,越界就报错、不产生哨兵 |
  | 主线程措辞 | 同上 | 5.1 只有一种措辞:主线程的 `nCcalls` 也大于 `baseCcalls`,同样报 `attempt to yield across metamethod/C-call boundary`。望舒原先报 5.2+ 的 `attempt to yield from outside a coroutine` |
  | 错误不带位置 | 同上 | 错误由 C 函数 `yield` 内部抛出,5.1 不加位置前缀;`MarkAnnotated` 防止宿主调用边界补上调用方行号 |
  | 顺带发现:被尾调用的 yield | `internal/crescent/call.go::doTailCall` | `return coroutine.yield(x)` 是宿主尾调用,`doTailCall` 没有像 `doCall` 那样记录 resume 点,下一次 resume 报 `cannot resume: no pending yield point`。官方套件 closure.lua 的「yields in tail calls」正好测这个,但它在 setfenv 豁免行之后,从没跑到。补上与 `doCall` 相同的 `pendingResume`(nresults 为 multret,由紧随的 `RETURN A 0` 从 top 返回) |

  验证:`test/regression/issue272_yield_boundary_test.go`(越界 12 条 + 尾调用 yield 3 条,期望值逐条用 `lua5.1`
  跑出;两半修复各自去掉后都会失败)。#271 的 `issue271_sentinel_race_test.go` 在 `-race` 下照常通过:那段脚本
  现在在 `Yield` 处就被拒绝,不再产生哨兵,哨兵上的两处跳过写入保留作纵深防御。设计稿 08 §5.2、§8.2、§1 / §5 /
  §11 的措辞与 P3 设计稿 04 / 07 里的同一句已同步。官方套件 closure.lua 截断点之后的协程一段(第 178–407 行)
  经 `luasuite_test.go` 新增的 `resumeAt` 单独运行(子测试 `closure.lua:178-407`),P1 / P3 / P4 都通过;去掉尾调用
  yield 的修复后它在 `closure.lua:228` 报 `cannot resume: no pending yield point`。

- **宿主调用边界上的错误位置、层级与 traceback(2026-10-02,#276 / #277 / #278 / #279)**:#271、#272 两轮审查登记在
  doc-gaps 里的存量差异，转成 issue 后一起修。四个问题都出在「宿主函数参与的调用链」上,修之前先用探针把同类写法
  扫了一遍，范围都比 issue 原文大。

  | 问题 | 位置 | 根因与修法 |
  |---|---|---|
  | `coroutine.wrap` 少一层调用方位置(#276) | `internal/stdlib/coroutinelib.go::wrapError` | 照 `auxwrap`:字符串或数字错误重新包成 level 1 的错误，由解释器的 CALL 点补上调用方位置;调用方是宿主函数(pcall、比较器)时由该边界冻结为不带位置。非字符串错误值原样传出 |
  | 宿主函数触发的元方法处理函数里 `error(m, level)` 差一层(#277) | `internal/crescent/meta.go::LessThan` / `IndexWithMeta` | `callMetaHandler` 按「VM 派发元方法不插 C 帧」不加层，这对 VM 自己的派发是对的;但 `table.sort` 的默认比较器、`gsub` 的表替换值经这两个导出入口进来时，处理函数的调用方是 C 函数 `sort` / `gsub`,这一层没人计。入口处给 `pendingHostFrames` 加一 |
  | 从 C 函数内部抛出的错误多了位置前缀(#278) | `meta.go::RawSet` / `RawNext` / `IndexWithMeta`、`callLuaFromHost` | `luaG_runerror` 只在当前帧是 Lua 函数时加位置:`next` 的 `invalid key to 'next'`、`rawset` 的 `table index is nil/NaN` 在 lua5.1 里不带位置;宿主函数被另一个宿主函数调用时(`gsub("a", "a", error)`、`sort` 的比较器是 `rawset`、`foreach` 的回调是 `error`),`luaL_where(L, 1)` 指向那个 C 函数，也不带位置。原先这些错误穿出宿主函数后被解释器 CALL 点补上了 Lua 行号。修法是在这几个只有宿主函数会调用的入口把错误冻结(`MarkAnnotated`);TFORLOOP 走的 `callLuaFromHostNamed` 调用方是 Lua 帧，不冻结 |
  | traceback 格式与生成时机(#279) | `internal/crescent/errors.go` | 见 09 §7.1 / §7.3 的订正：函数名从调用方指令推断、匿名函数 `<file:line>`、`[C]: in function 'x'` / `[C]: ?`、`(tail call): ?`、主线程栈底 `[C]: ?`、`LEVELS1/LEVELS2` 截断;未捕获错误的 traceback 改在出错点、没有捕获边界时生成(`protectDepth`) |
  | 顺带：P3/P4 调用 helper 的 pc 约定 | `internal/crescent/gibbous_host.go::DoCall` / `CallBaseline` / `TailCall` | 这三个 helper 把 `ci.pc` 存成 CALL 本身的 pc,而解释器与其余 helper(R3c-fix)都存「下一条」。差一条指令时 traceback 读到的是 CALL 之前的指令，给不出函数名;改成 `pc + 1` |
  | 顺带：协程栈底的幻影 C 帧 | `internal/crescent/coroutine.go::Resume` | `pcall(coroutine.resume, co)` 留在 resumer 上的 `pendingHostFrames` 被协程的第一帧吸收，协程内的 traceback 栈底多一行 `[C]: ?`、`getinfo(2)` 返回 C 帧。Resume 期间清零并在返回时恢复 |
  | 本地审查补修:`__newindex` 遇到 nil / NaN 键(#278 同类) | `internal/crescent/meta.go::setIndexWithMeta` | `luaV_settable` 总是先做 `luaH_set`,nil / NaN 键在赋值所在的 Lua 帧报错，根本走不到 `__newindex`。望舒先找 `__newindex`:`__newindex = rawset` 时错误来自 rawset(冻结后成了裸消息),Lua 处理函数里报处理函数的行，什么都不做的处理函数则静默成功。键不存在时先做同样的检查(`rawtable.go::checkKey`) |
  | 本地审查补修:`print` 少了 `tostring` 这一层(#277 / #279 同类) | `internal/stdlib/stdlib.go::baseFnPrint` | `luaB_print` 取全局 `tostring` 逐个 `lua_call`,所以重定义的 `tostring` 对 print 生效,`__tostring` 处理函数在两层 C 帧之上(`tostring`、`print`)。原先直接转换，少一层：处理函数里 `error(m, 3/4)` 差一层,traceback 少一行 `[C]: ?`。改为照做;全局 `tostring` 为 nil 时报 `attempt to call a nil value` |
  | 本地审查补修：非常量键的变量名后缀 | `internal/crescent/objname.go::describeRegDepth` | `getobjname` 对 GETTABLE / SELF 总是给 `field` / `method`,键不是字符串常量时名字是 `'?'`:`t[i]()` 报 `attempt to call field '?' (a nil value)`。原先这种情况不给后缀，只有 traceback 用了 `kname` |
  | 本地审查补修:`xpcall` 的 handler 时机 | `internal/crescent/errors.go::atRaisePoint` / `runErrFunc`、`meta.go::ProtectedCallWithHandler` | 见 09 §6.2 实现现状与 §6.5 订正:handler 在出错点调用，看得到出错的帧;只取一个结果;handler 不是函数时、一直失败时给 `error in error handling`,只失败一次时用再次调用的结果。原先在 `ProtectedCall` 返回后调用(本文件开头对账表的「xpcall handler 时机」一行已随之改写) |
  | 本地审查补修:`debug.traceback(co)` 与数字字符串 level | `internal/crescent/coroutine.go::CoTraceback`、`internal/stdlib/tablelib.go::debugFnTraceback` | 见 09 §13.1。原先把协程句柄当成非字符串 message 原样返回;level 只认数字 |
  | 本地审查补修：非字符串错误值不附 traceback | `internal/crescent/errors.go::wantsTraceback` | lua.c 的处理函数对非字符串错误值原样返回，未捕获的 `error({})` 不带 traceback |
  | 本地审查第二轮:handler 的 C 调用深度余量 | `internal/crescent/frame.go::cCallCheck`、`errors.go::runErrFunc` | handler 改在出错点调用后，`C stack overflow` 发生时深度已到上限，调 handler 的入口检查立刻失败，xpcall 一律得到 `error in error handling`。照 `luaD_call`:到上限报错，越过上限八分之一才是 `LUA_ERRERR`,中间只有 handler 能进入;为 C 深度错误调 handler 时深度加一(5.1 报错的那次调用已经加过) |
  | 本地审查第二轮:load 的 reader | `internal/stdlib/stdlib.go` 的 load、`meta.go::ProtectedCallKeepingHandler` / `RaiseCaughtInHost` | reader 原先经 `ProtectedCallDirect` 调用、在 Go 侧吞掉错误，出错点照样当作「协程将死」存了栈，协程正常结束后 `debug.traceback(co)` 显示 reader 出错时的栈。现在 reader 走不改 handler 的保护调用(`State.catchDepth` 计入),handler 的结果成为 load 返回的 msg;reader 返回非字符串时的错误补上调用方位置并经过出错点处理;数字片段照 `lua_isstring` 接受。见 10 §4.7 的补记 |
  | 本地审查第三轮:resume 与 `LUA_ERRERR` | `internal/crescent/coroutine.go::Resume`、`frame.go::cCallCheck` | 第二轮把 `Resume` 也接到了 `cCallCheck`,但 `lua_resume` 的检查是不留余量的 `nCcalls >= LUAI_MAXCCALLS`:handler 越过上限运行时，协程不该被恢复。改回原来的写法;没启动过的协程在这里被拒时照 `resume_error` 变成 dead。`lparser.c` 的 `enterlevel` 也把语法层数记在同一个深度上，`loadstring` 改为把当前深度传给 `parse.ParseAtCDepth`(第四轮补)。`LUA_ERRERR` 的错误值是 `luaD_seterrorobj` 的字面量，不带位置，构造时冻结。另补「调 handler 本身撞上上限」分支的两条用例 |
  | 本地审查第五轮:`cCallCheck` 的边界 | `internal/crescent/frame.go::cCallCheck` | `luaD_call` 先把深度加一再比较，望舒原先先比较再加一，所以 Lua 代码最深跑在 200 层(5.1 是 199)。第四轮让语法层数也算在这个深度上之后，最深一层连 `loadstring("")` 都编译不了，也不能 resume。改为先加一再比较 |
  | 本地审查第六轮：主 chunk 的起点 | `internal/crescent/state.go::callOnStack` | 宿主运行 chunk 的那次调用在 5.1 里本身就是一层(`lua_pcall` → `luaD_call`),望舒原先不计，主 chunk 从 0 开始，比嵌入式 PUC 多走 1 层。改为计入(解释器与 `enterGibbous` 顶层入口都在这一层之内),并用内嵌 oracle 钉住 |
  | 本地审查第七轮:P3/P4 编译帧调 Lua 被调方的重入 | `internal/crescent/gibbous_host.go`(`DoCall` / `CallBaseline` / `TailCall` 的回退路径与两个 inline-frame helper)、`frame.go::reentryCheck` | 编译帧要执行一个 Lua 被调方时会重新进入 Go(`executeFrom` 或 `enterGibbous`),原先把这一次记在 `nCcalls` 上。PUC 的 Lua 调 Lua 在同一个 `luaV_execute` 里完成，不算 C 层，所以 P3 force-all 下经一个 Lua 包装函数递归 `table.sort` 只到 149 层(lua5.1 197,master 上是 151 对 200)。改为记在单独的 `State.luaReentry` 上：脚本看不到它，但它仍然限制 Go 栈深度，gibbous 的水位线按 `nCcalls + luaReentry` 判断 |
  | 本地审查第七轮：深度测试接进 CI | `.github/workflows/ci.yml` oracle-smoke、`Makefile` fuzz-oracle | `TestCDepth*` 是普通测试，而 oracle-smoke 只跑 fuzz 目标(`-run '^$'`)和点名的测试，原先根本不执行。现在两处都按 P1/P3/P4 三套 oracle 标签点名运行;测试本身每种写法加一遍 force-all,并新增从 Go 直接调用已升层函数的用例，覆盖 `enterGibbous` 顶层入口 |
  | 本地审查第八、九轮:force-all 那一遍是否真的用到编译层 | `test/fuzz/cdepth_oracle_test.go` | force-all 只是放宽升层门槛，并不保证递归的那个函数被编译：用到 `pcall`、`loadstring`、协程的函数过不了可编译性检查。第八轮先给每种写法标上「要求升层」并断言 `runTieredSide` 的 promoted,第九轮审查指出这个值来自 `PromotionCount`,prelude 自带的辅助函数升层也会算进去(P4 上只要 `pcall` 接住过错误就为真),断言防不住「递归的函数没被编译」。现在去掉这条断言，注释写明 force-all 那一遍只是在升层开关打开时再与 oracle 比一次；编译帧调 Lua 是否多算 C 层，由下一行的两个测试按入口逐个检查。`TestCDepthOfAPromotedFunctionCalledFromGo` 的升层计数检查同样只是粗略的保护，注释里写明了这一点和变异的结果 |
  | 本地审查第八轮：五个重入入口各有测试 | `test/regression/issue276_279_reentry_depth_test.go`、`internal/crescent/reentry_inline_frame_p4_test.go` | 第七轮改动的五个入口里，原先只有 P3 的 `DoCall` 和 P4 的 `CallBaseline` 改回 `nCcalls` 时会有测试失败。新测试从 Go 调用五种编译过的调用方，在最深处用 `table.sort` 的 `__lt` 递归量出 C 调用深度，要求与直接调用一致。写法上要注意三点：调用方只能用局部函数和白名单库函数(`pcall` 也会让函数不升层);被调方用 vararg 让两个编译层都不编译它，否则编译码直接调用，不经过这些入口;前几次调用先让被调方正常返回,P4 要等调用点成功过一次才改走 `ExecutePlainCallInlineFrame`。`ExecuteCalleeFromInlineFrame` 没能找到经公共 API 走到它的写法，放在 `internal/crescent` 里按 SELF 专用模板的写法单独测。逐个把五个入口改回 `nCcalls`,每个都有测试失败 |
  | 全范围终审:wrap 传出非字符串错误 | `internal/stdlib/coroutinelib.go::wrapError` | 字符串和数字错误会重新包装，其他值原样返回协程里那个错误对象。对象上记的是协程那条栈上的出错点信息(出错点之上有几个宿主帧、协程的帧是否已保存),到了调用方线程会让 xpcall handler 拿到的 traceback 多一行或少一行 `[C]: ?`,外层协程因此死亡时 `debug.traceback(outer)` 只剩标题。改为用同一个错误值新建一个不加位置的错误对象，三种写法的期望值用 `lua5.1` 跑出 |
  | 第二次全范围终审:P4 inline-frame helper 不写调用方 pc | `internal/crescent/gibbous_host.go`(`ExecutePlainCallInlineFrame` / `ExecuteCalleeFromInlineFrame`)、`internal/gibbous/jit` 两处调用点 | `DoCall` / `CallBaseline` / `TailCall` 都把调用方 `ci.pc` 写成 CALL 的 pc+1,traceback 靠它取调用方行号和被调方名字;这两个兄弟 helper 的参数里没有 pc,P4 的调用点成功过一次、改走 `ExecutePlainCallInlineFrame` 之后，被调方出错时调用方那一行变成 `x: in function 'mid'`、被调方没有名字。两个 helper 改为接收 CALL 的 pc(exit-reason 里本来就带着，SELF 专用模板用 `retPC-1`),进入被调方前写回 |
  | 语法错误里块名的截断长度(用户决定在本分支修) | `internal/bytecode/chunkid.go`(`ChunkIDN`)、`internal/frontend/{lex,parse,compile}` 三种错误类型 | `luaX_lexerror` 用 `MAXSRC`(80)调 `luaO_chunkid`,运行期错误用 `LUA_IDSIZE`(60)。望舒原来所有块名都用同一个上限，语法错误里较长的块名被提前截断。现在 `ChunkIDN` 带缓冲区长度，前端错误用 80;运行期、traceback、`short_src` 用 60。同时修好了 `@file` 的长度计算:原来按 `59-3=56` 保留，而 `luaO_chunkid` 减的是 `sizeof(" '...' ")`(8),60 时保留 52,所以运行期错误前缀、traceback 和 `short_src` 里较长的文件名原来就多显示 4 个字节。master 上就有这两处差异 |
  | `print` 逐个写出参数(用户决定在本分支修) | `internal/stdlib/stdlib.go::baseFnPrint`、`internal/oracle/prelude.go` | `luaB_print` 转换完一个参数就写出，望舒原来全部转换完再写：后面的参数转换失败时什么都不写，`__tostring` 自己的输出也跑到整行前面。现在先写进缓冲，在每次可能运行 Lua 代码的转换之前(全局 `tostring` 被替换，或参数带 `__tostring`)、报错返回之前和结尾写出：写出顺序与 5.1 相同，普通的 print 仍是一次写操作(逐段直接写出的第一版在管道上慢了约 2.8 倍，本地审查第十六轮查出)。不调用内置 `tostring` 的参数仍做那次调用会做的 C 调用深度检查和字节记账(第十七轮查出：跳过调用后，在 C 调用深度用尽的那一层 print 不再报 `C stack overflow`)。差分测试 harness 里的 `print` 原先先写制表符再转换，与 `luaB_print` 的顺序不同，一起改了。master 上就有这个差异 |
  | 第四次全范围终审:xpcall handler 的结果一直被当作 GC 根 | `internal/crescent/meta.go::ProtectedCallWithHandler`、`state.go`(`errFuncResult`) | handler 在出错点运行，结果先存在 `LuaError` 里，错误传回 xpcall 的途中靠 `State.errFuncResult` 作根;这个字段原来从不清空，最后一次 handler 的结果(比如一张大表)会一直留到下一次 handler 运行或 State 销毁。现在 xpcall 返回时把它恢复成进入时的值。回归 `issue279_xpcall_handler_result_test.go` 与 `lua5.1` 一样输出 `false false` |
  | 第五次全范围终审:`dofile` 加载失败的错误多了调用方位置(#278 同类，用户决定在本分支修) | `internal/stdlib/baseenv.go::baseFnDofile` | `luaB_dofile` 在 `luaL_loadfile` 失败后直接 `lua_error`,消息本身已经带着文件名和行号，不再加调用方位置;望舒用 level 1 的错误抛出，Lua 调用方的行号被加在前面(`x:3: bad.lx:1: ...`)。现在把错误冻结为不带位置。只在 `Options.AllowFileLoad` 打开时出现，master 上就有。打不开文件时 lua5.1 的消息后面还有 `: <strerror>`,望舒没有，这是另一处措辞差异，当时没有改，后来在 #288 修好 |

  验证：四个 regression 文件(`issue276_*` / `issue277_*` / `issue278_*` / `issue279_*`)期望值逐条用 `lua5.1` 跑出;
  #279 的用例在 P1、P3、P4 下各跑一遍 force-all 与不升层，但这些用例调用 `debug.traceback` 等不在白名单里的函数，
  force-all 下实际没有帧升层(本地审查用 `PromotionCount` 查出),只说明解释器路径正确;编译层由
  `issue276_279_compiled_callers_test.go` 覆盖，它的调用方只调用已知局部函数和白名单 stdlib,force-all 与阈值 1
  的 auto 各跑一遍并断言 `PromotionCount` 非零(P4 在 arm64 上不断言：那里的翻译器只有原生路径，原生路径不接受的函数没有
  head-op replay 可退，这些调用方大多留在解释器里，PR CI 的 arm64 / macOS P4 job 因此失败过一次;用例仍与 lua5.1 比输出),去掉 gibbous
  调用 helper 的 `pc + 1` 修复后它会失败。它原先的预热调用每次都报错，P4 的调用点从没成功过，走不到 `ExecutePlainCallInlineFrame`;第二次全范围终审查出这条路径不写调用方 pc,补了「调用点先成功、后出错」的两个用例(未捕获与 `xpcall(f, debug.traceback)`),`ExecuteCalleeFromInlineFrame` 的同一检查在 `internal/crescent` 的 `TestInlineFrameCalleeAddsNoCLevel` 里;
  `crescent` 内部测试 `TestTraceback_NotBuiltForCaughtErrors`
  钉住「被 pcall / resume 捕获的错误不生成 traceback」。各修复去掉后对应用例都会失败。`test/api` 里原先断言
  `[C]: in ?` 的用例改为断言 lua5.1 的完整输出。

- **#276-#279 本地审查登记的八处存量差异(2026-10-03,#281-#288)**:都是 master 上就有的差异，转成 issue 后一起修。
  每一处都先用探针和 lua5.1 比对，再把同类写法扫一遍;#282 的范围用户决定「全部对齐」,比 issue 原文大很多。

  | 问题 | 位置 | 根因与修法 |
  |---|---|---|
  | resume 正在运行或 normal 的协程用了 5.2 措辞(#281) | `internal/crescent/coroutine.go::Resume` | `luaB_coresume` / `auxwrap` 先查 `costatus`,按 `statnames` 报 `cannot resume running coroutine` / `cannot resume normal coroutine`;`non-suspended` 只有 C API 能走到。08 §2.3 的错误说法一起订正 |
  | 空块名显示成 `?`(#284) | `internal/bytecode/chunkid.go` | `luaO_chunkid` 对不以 `=`、`@` 开头的块名一律走 `[string "..."]` 分支，空串也一样 |
  | load 把 reader 的错误值转成了字符串(#283) | `internal/stdlib/stdlib.go::readerErrorValue` | `luaD_protectedparser` 留在栈上的就是错误值本身:`load(error)` 返回 `nil, nil`,`error({})` 返回那张表。顶层未捕获时 lua.c 的 traceback 处理函数只会给字符串和数字加 traceback,照做 |
  | `table.foreach` / `foreachi` 参数错误少 `got <type>`(#286) | `internal/stdlib/tablelib.go` | `luaL_checktype` 走 `tag_error` |
  | `__index` / `__newindex` 成环用了 5.2 措辞(#287) | `internal/crescent/meta.go` | `MAXTAGLOOP` 之后是 `loop in gettable` / `loop in settable`;次数上限本来就一致 |
  | loadfile / dofile 打不开文件时少 strerror(#288) | `internal/stdlib/baseenv.go::fileErrorMessage` | `errfile`:`cannot open|read <name>: <strerror>`,读目录这类打开成功但读失败的情况是 `read`。同轮还发现 loadfile 不跳过首行 `#`(用户决定本分支修):`luaL_loadfile` 跳到第一个换行，行号不变。终审又查出文件名里的 NUL:`fopen` 和 `errfile` 都在第一个 NUL 处停，望舒原先把整段交给 `os.ReadFile`,得到 `Invalid argument`;现在先截断 |
  | GC 把调用之上的旧寄存器当根(#285) | `internal/crescent/host.go::callHost` | `luaD_precall` 对 C 函数设 `L->top = func + 1 + nargs`,`traversestack` 只标记到 top。宿主函数运行期间把 top 降到最后一个参数之后，已返回 callee 留下的大表可被回收。降 top 之后 luasuite `closure.lua:95` 失败，查出一个潜伏缺陷:pcall 的错误展开和协程因错误死亡都没有关闭开放 upvalue,出错前建的闭包指着会被复用或被 GC 清掉的栈槽。`callLuaFromHostNamed` 与 `Resume` 现在先 `closeUpvals`,对应 `luaD_pcall` 的 `luaF_close` |
  | 词法 / 语法错误的措辞、`near` 和行号(#282) | `internal/frontend/lex` / `parse` / `token` | 按 `llex.c` / `lparser.c` 的报错点逐条对齐，见 [03](./03-frontend-lexer.md) §11.2 与 [04](./04-frontend-parser-codegen.md) §4.4、§4.4.1。要点：不认识的字符是单字符 token(`token.CHAR`),由语法分析报 `unexpected symbol`;词法错误都带 `near`,内容是扫描缓冲区(解码后的字符串);`exprstat` 按表达式形状定语句种类;名字检查带引号;`check_match` 报开记号和它的行;错误行号是扫描器所在行;局部变量 / upvalue / 赋值目标的上限改在解析时按 `errorlimit` 的写法报;`local function` 和函数表达式的 linedefined 改为 `(` 所在行 |
  | 长字符串里的 CR LF 存成两个字节(#282 同类，值的差异) | `internal/frontend/lex/lexer.go::readLongString` | `read_long_string` 把每个换行序列存成一个 `\n`;`[[a<CR><LF>b]]` 长 3,原先是 4 |
  | 块名里的 NUL(#282 同类) | `internal/crescent/state.go::CompileAndLoad` | `lua_load` 把块名当 C 字符串，第一个 NUL 之后都看不到 |
  | 寄存器上限多了一个(#282 同类) | `internal/frontend/compile/funcstate.go::checkStack` | `luaK_checkstack` 是 `newstack >= MAXSTACK`,一个函数最多 249 个寄存器 |

  **已知限制(用户决定不处理)**:`function or expression too complex` / `control structure too long` 不带 `near`、行号
  取 codegen 手里的行;常量超过 262143 个或局部变量调试表超过 32767 条时，lua5.1 按运行期错误抛出，望舒按编译错误
  处理。详见 [04](./04-frontend-parser-codegen.md) §9。

  **另开的 issue**:#290(P4 构建下 `go vet` 的 `unsafe.Pointer` 警告)、#291(没有引用的挂起协程永远不被回收)、#292(顶层 Run 出错后、下一次 Run 之前调用 `Collect()`,逃逸闭包读到已回收的对象)。三个都已在下一条修好。

  验证:#284 的用例加在已有的 `syntax_error_chunkname_test.go`,其余每个 issue 一个 regression 文件(`issue281_*` 到 `issue288_*`,#282 有词法、语法、块名、寄存器上限四个),
  期望值逐条用 `lua5.1` 跑出;赋值目标上限三条依赖 C 调用深度，取自内嵌的 5.1.5(`internal/oracle`),理由见
  `issue282_parser_errors_test.go` 头注。探针在 P1、P3、P4 的不升层 / force-all / auto 七种配置下都与 lua5.1 一致。
  #285 的三处改动和 #282 的词法改动各自去掉后，对应用例都会失败。

- **#281-#288 一轮另开的三个 issue(2026-10-04,#290 / #291 / #292)**:

  | 问题 | 位置 | 根因与修法 |
  |---|---|---|
  | P4 构建下 `go vet` 报 `possible misuse of unsafe.Pointer`(#290) | `internal/gibbous/jit/peroptranslator/emit_shim_amd64.go` | 报警的是没有调用方的 `funcEntryPC` / `uintptrToPtr`,直接删掉。默认、P3、P4 三种构建的 `go vet` 都干净 |
  | 顶层 Run 出错后、下一次 Run 之前 `Collect()`,逃逸闭包读到已回收的对象(#292) | `internal/crescent/state.go::callOnStack` | 出错展开的帧没有执行 RETURN,upvalue 一直开着，等到下一次 Run 才关;两次 Run 之间主线程的栈不是根，于是闭包引用的值被回收。改为出错返回前、`runningThread` 还指着主线程时 `closeUpvals(th, 0)`,与 `luaD_pcall` 的 `luaF_close` 一样。`Run`、`Call`、`CallInto` 都经过这里，见 [05](./05-interpreter-loop.md) §9.3 |
  | 没有引用的挂起协程永远不被回收(#291) | `internal/crescent/coroutine.go`、`internal/gc` | 协程句柄是 lightuserdata,所有非 dead 协程都是根。现在协程的 Lua 值是 arena 里只有头的 Thread 对象，收集器经 `Roots.ScanThread` 扫它的栈;根里只放 running 和 normal 的协程;开放 upvalue 标记它所指的栈槽值(同 `reallymarkobject`),没被标记的协程在清扫前关闭 upvalue、归还栈段，注册表下标复用;协程栈段计入 GC 分配量和活跃字节，创建前先检查一次 GC,否则只建协程的循环不会自动收集。`coroutine.wrap` 返回的函数把协程放在宿主闭包的 upvalue 里，与 `auxwrap` 一样让函数持有协程。`type` / `tostring` / 报错里的类型名不再查注册表，直接按 `TagThread` 判断。见 [08](./08-coroutines.md) §6.1、[06](./06-memory-gc.md) §5.2 |

  验证:#291、#292 各一个 regression 文件(`issue291_unreferenced_coroutines_test.go`、`issue292_collect_after_failed_run_test.go`),
  期望值用 `lua5.1` 跑出;#291 的第二组用例在 GC 压力模式下也跑一遍，第三组不调用 `collectgarbage()`,断言内存峰值有界(修前 53 MB / 115 MB,修后约 1 MB,lua5.1 是 1.3 MB / 0.6 MB)。探针在 P1、P3、P4 的不升层 / force-all / auto
  七种配置下都与 lua5.1 一致，打开 GC 压力模式也一致。#291 的用例去掉对应修复后会失败。#292 的 regression 用例
  只在 #292 的关闭和 #291 的开放 upvalue 标记都去掉时才失败(后者单独也能让闭包的值活下来),所以另加
  `internal/crescent/callonstack_error_upvals_test.go` 直接断言出错返回后主线程上没有开放 upvalue,只去掉 #292
  的关闭它就失败。

  **另开的 issue**:#296(arena 大块分配的空闲块不合并，容量只增不减;协程在 #291 之后会被自动回收，深协程循环因此
  在每次收集后缩容、再扩栈时扩回，变慢)、#297(只靠宿主函数分配的脚本循环碰不到 safepoint,GC 从不触发)。两处在
  #294 之前的 `082a2cd` 上就能复现。

- **arena 大块碎片与只靠宿主函数分配的循环(2026-10-05,#296 / #297)**:#290-#292 一轮审查登记的两处存量差异。
  两者互相牵连：只给 #297 加 safepoint 时，收集一频繁，每轮收集末尾的 `Compact` 把 slab 缩到 bump,下一个变长的大
  字符串又让 `grow64` 翻倍扩回，`string.rep` 递增长度的循环照样撞上 2 GiB 上限，所以先修 #296。

  | 问题 | 位置 | 根因与修法 |
  |---|---|---|
  | 大块尺寸逐渐变大的循环让 arena 只增不减(#296) | `internal/arena/freelist.go`、`arena.go`、`internal/gc/collector.go` | LARGE 空闲块从不合并：反复拼接变长的字符串、协程值栈逐次翻倍时，没有一块空闲块够大，每块都从 bump 新切;每轮收集又把 slab 缩到 bump,下一轮再翻倍，整块来回拷贝。现在空闲 LARGE 块按起止偏移建索引，归还时与前后相邻的空闲 LARGE 块合并，合并后正好结束在 bump 的块让 bump 退回(清零后);`Compact` 改为按 GC 水位留余量:`target = (live + 下次阈值) × 1.25`,容量超过 `max(target, bump, 64 KiB)` 两倍时才缩到这个大小。小对象桶不变。见 [06](./06-memory-gc.md) §2.4、§3 |
  | 只在宿主函数里分配的循环从不收集(#297) | `internal/crescent/host.go::callHost` | 宿主函数分配只记账、不收集，自动检查点只有会分配的 opcode。宿主函数返回、结果写进栈槽之后调一次 `MaybeCollect`,此时宿主函数产生的值都在栈上，参数回到调用方的寄存器,Go 侧不持有临时值;lua5.1 在会分配的 API 里调 `luaC_checkGC`。见 [06](./06-memory-gc.md) §7.1 |

  数据(默认构建，括号里是 lua5.1):拼接探针 `collectgarbage("count")` 峰值 2530 KB → 160 KB(212 KB),arena 容量
  407 MB → 1.3 MB,耗时 92 s → 4 s(1.5 s);深协程探针 11.3 s → 0.9 s(0.24 s),arena 227 MB → 3.4 MB;递增 `string.rep`
  探针从撞上 2 GiB 上限变为峰值 8.8 MB(9.2 MB),固定长度 `string.rep` 探针 4993 KB → 211 KB(389 KB)。剩下的耗时差距
  在拼接本身的拷贝和解释器调用开销上，不在 arena。P3 下 `Compact` 不缩，同样几个探针的容量在 16 MB 以内。

  验证:`internal/arena/coalesce_test.go` 覆盖向前、向后、双向合并，从桶中间摘块,bump 回退及其清零，以及随机分配 /
  释放序列下的模型检查(活块与空闲块不重叠、相邻空闲块都已合并、`freeBytes` 与链上一致),打开 `debugFreelist` 也通过;
  去掉清零或去掉向前合并，对应用例失败。依赖「释放后块留在 freelist」的旧 freelist 用例在块后垫一个小块。
  `internal/gc` 加一条收集后容量等于 `(live × 3) × 1.25` 的用例。regression 两个文件
  (`issue296_arena_fragmentation_test.go`、`issue297_host_allocation_gc_test.go`)在修复前都失败。探针在 P1、P3、P4 的
  不升层 / force-all / auto 七种配置下输出相同，打开 GC 压力模式也一样;宿主函数回调宿主函数的场景(`gsub` 带函数 /
  表替换、`sort` 带比较函数、`xpcall` 带宿主处理函数、`__index` 为宿主表等)在这些配置和压力模式下与 lua5.1 一致。
  宿主调用密集的循环(500 万次 `math.floor`)耗时不变。

  **同一轮另修的 `SetHostTriggeredCollect` 误回收(用户决定在本分支修)**:这个默认关闭的开关原来让 `AllocCharge`
  越过阈值就立刻收集，而多数分配助手先挂 sweep 链、再记账、之后才接根，新对象会在自己那次记账里被回收：开关打开后
  连建两万张表，有 6 个句柄指向别的表;开关打开后跑官方测试套，本分支修复前 gc / errors / literals / nextvar / pm /
  strings 六个文件失败,master 上除 gc 外的五个失败。现在 `AllocCharge` 只记账，会分配的公开 API 在返回前调 `Collector.HostSafepoint`(此时产生的对象都已 pin 或
  写进表里),开关打开时越过阈值才收集。`test/api/host_triggered_collect_test.go` 在 master 上失败、修复后通过;
  `TestOfficialSuiteHostTriggered` 在开关打开时跑整套官方测试。

## 2026-10-14：os.time 字段读取经过 __index（#299）

`internal/stdlib/tablelib.go` 的数值字段读取和 `getBoolField` 改用 `IndexWithMeta`，
传播处理函数错误，保留默认值、整数转换与时区算法。`test/language/ostime_index_test.go`
增加 17 个子用例；PUC Lua 5.1.5 逐项核对，修复前 16 个失败、已有字段对照通过。
机制见 [10 §9.1.1](./10-stdlib.md)，
过程记录见 [#299 复盘](../../../llmdoc/memory/reflections/2026-10-14-issue299-os-time-index.md)。

## 2026-11-10：参照实现的 `%` 在 x86-64 上固定为不融合，tarball 进 cache（#309）

nightly 从 2026-11-08 起每轮报几十到九十多条分歧（#309 到 #322，其中 #315 是分层 go-fuzz 报的）。原因在参照实现：
runner 镜像换成 ubuntu-26.04 后，在它上面编出来的官方 Lua 把 `a - floor(a/b)*b` 里的乘法和减法合成了一条 FMA 指令，
`%` 对一部分操作数的结果变了。望舒的代码没有变，这一轮也没有改它。同期另有五轮因为 `www.lua.org` 连接超时什么都没测
（#306、#307、#308、#312、#321）。

| 改动 | 位置 | 内容 |
|---|---|---|
| `lua5.1` 二进制固定编译方式 | `scripts/build-lua-oracle.sh`（新增） | 取包、`make posix`、安装；x86-64 上加 `-ffp-contract=off`；日志里记编译器版本、默认 `-march`、默认目标有没有 FMA |
| 内嵌参照实现固定 `%` | `internal/oracle/lua515.c` | x86-64 上重定义 `luai_nummod`，乘积先存进 `volatile double`；`_lua515/` 不动 |
| 参照实现不对时在安装步骤失败 | `scripts/check-oracle.sh` | x86-64 上验算三次 `84572069678023671808 % 89`，有一处不是 `0` 就失败；其他架构只打印 |
| 测试 | `internal/oracle/nummod_test.go`、`scripts/test-check-oracle.sh`（均新增） | 前者六个用例，`oracle-smoke` 和 `make fuzz-oracle` 用 `CGO_CFLAGS="-O2 -mfma"` 再跑一遍；后者六个用例，离线，挂进 `make test-scripts` |
| workflow | `nightly-diff-fuzz.yml`、`ci.yml`、`bench-acceptance.yml` | nightly 用新脚本装参照实现，tarball 存进 Actions cache（nightly 写，其余只读）；`ci.yml` 里 apt 装的包要过 `check-oracle.sh`，不过就用源码构建 |

验证（本机 x86-64，gcc 15.2）：

- 用 `-march=x86-64-v3` 编参照实现：经 `build-lua-oracle.sh` 编的通过 `check-oracle.sh`；不加 `-ffp-contract=off`
  编的得到 `-2944 -2944 -2944`，被拒绝。
- 11-09 两轮 nightly 日志里的 187 个分歧种子：对着融合的参照实现 187 个都分歧，对着固定后的 187 个都通过。
  #309、#313、#316、#318、#322 标题里的五个种子单独重放，结果相同。
- #315 的输入经 `FuzzOracleDiffTiered`（p4，`CGO_CFLAGS="-O2 -mfma"`）重放：有重定义时通过，去掉后失败。
- `nummod_test.go` 在 `-mfma` 下去掉重定义，六个用例全部失败。

**同一个 PR 里另修的一个测试期望值**：PR CI 的 `ubuntu-latest` 也已经是 26.04，`test (p1/p3/p4 / ubuntu-latest)`
三个 job 都在 `TestOSTimeIsDST_DefaultHourFallback` 的 `Africa/Windhoek` 用例上失败，master 上同样失败（上一次 master
的 CI 是 11-05，当时镜像还是 24.04）。原因是 Ubuntu 26.04 的 tzdata 包换了打包形式，这个时区哪一段算夏令时反过来了，
glibc 的结果也跟着变。望舒在两种 tzdata 上都与 glibc `mktime` 一致（各 14976 例，差异为 0），只是测试把其中一种的
结果写成了固定期望值。用例换成 `Asia/Amman`，产品代码没有改。细节见 [10 §9.1](./10-stdlib.md) 末尾的 2026-11-10 补记。

真实 runner 上的结果（PR #324 的 CI，ubuntu-26.04）：`build-lua-oracle.sh` 的日志显示镜像里 gcc 的默认 `-march` 是
`x86-64-v3`（镜像用的是 Ubuntu 的 `amd64v3` 包），这就是它默认融合的原因；apt 装的 `lua5.1` 在 `check-oracle.sh` 上得到
`-2944 -2944 -2944` 被拒绝，改用源码构建后通过；arm64 的 runner 上只打印结果（`-2944 -2944 -2944`），不判定。

只在 x86-64 上固定的原因、`-ffp-contract=off` 对参照实现其他代码的影响、镜像为什么默认融合，
见 [12 §4.9g](./12-testing-difftest.md)。arm64 上望舒自己的 `%` 是融合的，与 amd64 结果不同，记在
[#323](https://github.com/Liam0205/wangshu/issues/323)。过程记录见
[#309 复盘](../../../llmdoc/memory/reflections/2026-11-10-issue309-oracle-fp-contraction.md)。

## 2026-11-25：标准库删掉两个 5.1 没有的名字，`pairs` / `ipairs` 私有持有迭代器（#329）

nightly 的 go-fuzz（p1，`FuzzOracleDiff`）找到 `t={}table.unpack(t)`：官方 Lua 5.1 报
`attempt to call field 'unpack' (a nil value)`，望舒照常执行。在当前 master 上精确重放可以复现，是产品缺陷。

把望舒和 `lua5.1` 的全部全局名字、库字段各导出一份比对，望舒多出来的名字一共两个：

| 多出的名字 | 来历 | 连带的行为差异 |
|---|---|---|
| `table.unpack` | `OpenAll` 从 2026-06-11 起多注册的别名，与 [10 §4.5](./10-stdlib.md) 写的「不提供」相反 | 探测 `table.unpack` 的脚本走了 5.2 的分支 |
| `__ipairs_iter` | `ipairs` 的内部迭代器被放进全局表，`ipairs` 每次调用去全局表里查它 | 清空它 `ipairs` 就报错 |

`pairs` 用同样的办法查全局 `next`，所以还有两处差异：`next = nil` 之后 `pairs` 报错（官方照常遍历）；
`pairs({}) == next` 是 `true`（官方是 `false`）。

| 改动 | 位置 | 内容 |
|---|---|---|
| 删 `table.unpack` | `internal/stdlib/stdlib.go` `OpenAll` | 不再注册 |
| 迭代器私有化 | 同文件 `registerIterFactory` | `pairs` / `ipairs` 各自在注册时建好迭代器的宿主闭包，由工厂函数的 Go 闭包捕获，同时挂在工厂函数的 upvalue 上让 GC 保活；不再有 `__ipairs_iter`，`pairs` 不再读全局 `next` |
| fuzz 的 prelude | `internal/oracle/prelude.go` | 原来靠包装全局 `__ipairs_iter` 来拦 `luaL_checkint` 的未定义行为区间，改成包装 `ipairs`、发放同一个带检查的迭代器 |
| 行为测试 | `test/language/stdlib_names_test.go`（新增） | 27 个用例，期望值逐个在 lua5.1 5.1.5 上实际执行核对过 |
| 分层测试 | `test/difftest/tiered_stdlib_names_test.go`（新增） | 十段脚本在 P3 / P4 的升层代码里执行，与解释器、与 `lua5.1` 比对（带文件名的 `io.lines` 一段是已登记的差异，与记录的文本比对）；断言 `GibbousEntries() > 0`（arm64 的 P4 上有一段例外，见 10 §12.3） |
| 白盒探针 | `State.GibbousEntries()`（新增，testing-only） | 执行进入升层代码的次数；`PromotionCount()` 只说明编译过 |
| 名字集合比对 | `test/difftest/corners_test.go` `TestDiff_StdlibNames`（新增） | 对着真实的 `lua5.1` 双向比对；望舒多出的名字一律失败，缺少的逐个登记在 `stdlibAbsent`（合入时 37 条）；清单里还包含哪些名字是同一个函数对象 |
| 语料 | `test/fuzz/testdata/fuzz/FuzzOracleDiff/` | 加入 #329 的输入和七条回归语料；原来写着 `__ipairs_iter` 的两条改成经 `ipairs({})` 取迭代器 |

**对使用者的影响**（六项都是改成与官方 5.1 一致）：

- 调用 `table.unpack(...)` 的脚本会开始报错，改成 `unpack(...)` 即可（Lua 5.1 本来就只有后者）。
- 全局 `__ipairs_iter` 没有了。读过它的脚本或宿主代码改成 `ipairs({})` 的第一个返回值。
- 改写或清空全局 `next` 不再影响 `pairs`。以前靠替换 `next` 来改变 `pairs` 行为的脚本不再生效，要改就替换 `pairs` 本身。
  相应地，`pairs({}) == next` 现在是 `false`（`pairs` 返回的是它自己持有的迭代器）。
- `ipairs` 的迭代器接受数字字符串作控制值，以前这种调用会报错。
- `io.lines(nil)` 现在报 `FILE* expected, got nil`，以前返回默认输入的迭代器；不带参数的 `io.lines()` 不变。
  传入布尔值、表等既不是字符串也不是数字的值时，报错文字从 `io.lines with a filename is not supported` 变成
  `string expected, got …`；传入数字和传入字符串一样，仍然报 `io.lines with a filename is not supported`。
- `math.mod == math.fmod` 现在是 `true`，以前是 `false`（两个名字各有一个函数对象）。调用结果没有变化。

### 独立审查之后补的内容

合入前的独立审查没有发现阻塞问题，提出两条重要问题、三条小问题和两处存量差异，都在同一个 PR 里处理了。

| 审查意见 | 处理 |
|---|---|
| 行为测试里标着「热函数」的用例其实没有升层：直接调用全局 `ipairs` / `pairs` 的函数不会升到 P3 / P4，所以「三种构建一致」没有任何测试在执行 | 新增 `test/difftest/tiered_stdlib_names_test.go` 的 `TestTiered_StdlibNames`：三段确实会升层的脚本，分别在只用解释器、强制升层、自然升温三种方式下执行，要求彼此一致并与 `lua5.1` 一致，并断言 `PromotionCount() > 0`。原用例改了写法和注释，不再声称覆盖升层路径。（这个断言后来被第四轮审查指出不够：升了层不等于执行过，见下面第四轮的表） |
| 文档对 os / io 缺少的函数前后矛盾：`stdlibAbsent` 登记它们没有注册，而 README 写「os 全部提供」「`os.exit` 不真的退出进程」，设计文档多处写「做简化版」 | README 两个语言版本、[10](./10-stdlib.md) 的 §9.1 / §9.3 / §9.4 / §10.1 / §12.1 / §14 都按现状改了，新增 §9.5 说明 os 库只注册了五个函数；`exemptions` 里与 `stdlibAbsent` 矛盾的条目改掉。新增 `TestStdlibAbsentIsInREADME` 和 `TestStdlibAbsentReasonsResolve`，以后 README 或登记理由与实际不符会失败。这些函数要不要补记在 [#330](https://github.com/Liam0205/wangshu/issues/330) |
| 设计文档还有两处写 `pairs` 返回 `next` | 改成「返回的迭代器与 `next` 行为相同，但不是同一个函数对象」 |
| 一条新语料里有两个表达式在 fuzz 的 prelude 之下恒成立；prelude 里拦截 `ipairs` 迭代器非有限控制值的检查没有任何测试 | 语料删掉那两个表达式；新增 `internal/oracle/ipairs_guard_test.go` 的 `TestIpairsIteratorGuard` 和两条语料。写测试时还发现这个检查用固定形参接收参数，会把「没有传控制值」变成「传了 nil」，改成原样转发 |
| 新增的中文段落用了半角标点 | 改成全角 |
| 存量差异：`ipairs` 的迭代器不接受数字字符串作控制值（官方用 `luaL_checkint`，`ipairs({})(t, "1")` 从下标 1 继续，望舒报 `number expected, got string`） | 在这个 PR 里修了。另用 58 段脚本把所有接收数字参数的库函数逐个传入数字字符串与 `lua5.1` 比对，没有发现第二处 |
| 存量差异：`debug.traceback` 在 `FuzzOracleDiff` 里两侧输出不同（望舒在主块之下多一层 C 帧，与独立的 `lua5.1` 相同，与内嵌用法不同） | 需要先定以哪种用法为准，记在 [#331](https://github.com/Liam0205/wangshu/issues/331)，这一轮没有改 |

第二轮独立审查（全范围，另一位 reviewer）确认代码和测试没有问题，并用它自己的变异确认 `TestTiered_StdlibNames`
验证的确实是升层之后的路径（只改升层路径的 `TForLoop` 时，它在 P3、P4 下失败，而解释器上的行为测试照常通过；
这个结论只对走 `TForLoop` 的那两段成立，直接放在 `pcall` 下面的一段没有进入升层代码，见下面第四轮）。
它提出一条重要问题、四条小问题和三处存量差异：

| 审查意见 | 处理 |
|---|---|
| 同一组说法在别处的副本没有改完：[10](./10-stdlib.md) §12.1 新写的说明漏了 `Options.HideFileLoaders`；[12](./12-testing-difftest.md) 还写「`os.exit` 默认不真退」；`llmdoc/reference/embedding-contract.md` 还写「默认面对齐 gopher-lua」；`llmdoc/memory/doc-gaps.md` 里同一个缺口有三条互相矛盾的登记；[11](./11-embedding-arena-abi.md) 把 `Options.Libs` / `Exclude` 写成现有字段；10 §14 的 os 行写「os.execute 默认禁用」 | 六处都按现状改了；doc-gaps 的三条合并成一条。顺着同一个说法还改了 10 §4.7（package 系统没有注册）、§10.4、§15.2，并在 2026-06-11 评审决定的归档里补记「第 6 项没有照此实现」 |
| `TestStdlibAbsentIsInREADME` 对 `debug.debug` 不起作用：库名和函数名相同，行里本来就有 `debug` 这个词 | 函数与库同名时要求写出全名；README 相应写成 `debug.debug`。从 README 删掉它，测试现在会失败 |
| 新写的文字把范围总表叫成「§11」，实际是 §14（§11 是 coroutine 库） | 新写的三处改了；`corners_test.go` 和 `tablelib.go` 里原有的「10 §11」也一起改成 §14 |
| 「对使用者的影响」只写了 `table.unpack` 一项 | 补全为五项 |
| 改写的一条里全角、半角标点混用 | 统一成全角 |
| 存量差异：`io.lines(nil)` 官方报 `FILE* expected, got nil`，望舒返回迭代器 | 在这个 PR 里修了：`io.lines` 现在区分「没有参数」和「显式 nil」，既不是字符串也不是数字的参数先报 `string expected`，与 `liolib.c` 的 `io_lines` 一致。测试在 `test/api/io_handles_test.go` |
| 存量差异：10 §14 总表不完整（base 行缺 `getfenv` / `setfenv` / `newproxy`，debug 行只列了 12 个缺少函数里的 6 个） | 补全 |

第三轮独立审查（全范围，又一位 reviewer）没有找到完成条件 1、2 的反例，提出一条重要问题、三条小问题和四处存量差异：

| 审查意见 | 处理 |
|---|---|
| `io.lines` 的修复只有解释器上的测试，期望值是手写的；没有升层路径上的测试，也没有差分语料 | `TestTiered_StdlibNames` 加第四段脚本（把 `io.lines` 当泛型 for 的生成器，循环设了上限，因为旧行为返回迭代器会一直循环）；加一条 `FuzzOracleDiff` 语料。换回旧的 `io.lines` 两者都失败 |
| `TestStdlibAbsentIsInREADME` 有 4 个名字实际没有被检查：英文 README 原因栏里的普通单词 input、output 被当成了 `io.input` / `io.output`；debug 那一行为 `debug.getfenv` 写的缩写被当成了全局 `getfenv` / `setfenv` | 匹配规则改成只读「具体项」一栏、名字必须带反引号、库那一行的缩写不能代表同名的全局函数；规则本身由 `TestReadmeListsMatcher` 固定。对登记表里 35 个名字逐个从两个 README 里删掉再跑测试，每一个都会失败 |
| `f:setvbuf` 的说法不一致：README 把它归在「真实文件 IO」，登记表的理由是「标准流不带缓冲」；10 §10.1 和 §14 总表里没有它 | README 单列一行，10 号文档两处补上。官方对标准流调用它会成功，这一点也写明了 |
| `llmdoc/reference/embedding-contract.md` 句尾还写三层开关「留待宿主接入前完成」，与 doc-gaps 的「要不要做没有决定」相反 | 改成一致 |
| 存量差异：`math.mod == math.fmod` 官方是 `true`，望舒是 `false` | 在这个 PR 里修了：别名改成指向同一个函数对象，与 `string.gfind` 的做法相同。名字比对测试原来只比名字和类型，看不到这一点，现在清单里加了「哪些名字是同一个对象」 |
| 存量差异：10 号文档对 `table.getn` / `setn` 有三种说法，都与代码不符（文档说没有，实际有）；总表没有列 `table.foreach` / `foreachi`、`math.mod`、`string.gfind` | 都按现状改了。用一段临时脚本把 10 号文档的逐函数表格与实际注册的名字机械核对了一遍，改完之后不一致为 0。这些表格本身没有常驻检查，记在 [#332](https://github.com/Liam0205/wangshu/issues/332) |
| 存量差异：#331（主块之下的 C 帧）、`io.lines(文件名)` 的报错文本（属于 #330） | 不在这一轮修。reviewer 认为可以接受，并提醒 #331 的触发输入只有二十几个字节，nightly 随时可能撞到 |

第四轮独立审查（全范围，又一位 reviewer）仍然没有找到产品行为上的问题，提出两条重要问题和四条小问题，都与测试是否
有效有关：

| 审查意见 | 处理 |
|---|---|
| `TestTiered_StdlibNames` 四段脚本里有两段升了层却从来没有进入升层代码：内核直接放在 `pcall` 下面，而被宿主函数调用的 Lua 函数固定由解释器执行。`PromotionCount() > 0` 数的是被编译的 Proto 个数，拦不住这种情况。只改升层路径 `TForLoop` 的出错分支，测试照常通过。`math.mod` 一项没有分层用例 | 新增白盒探针 `State.GibbousEntries()`（Go 把控制权交给升层代码的次数），规则由 `test/tiering/gibbous_entries_test.go` 固定。分层测试改成：需要出错的内核经一个 Lua 包装函数调用（不能是尾调用），断言强制升层一格 `GibbousEntries() > 0`（自然升温一格只对 P3、P4 在自然升温下都会进入升层代码的脚本断言）、只用解释器的一格等于 0。补了两段脚本（`ipairs` 迭代器自己的参数错误、`math.mod` 与 `fmod` 的身份），现在共六段。只改升层路径的 `TForLoop` 或 `CALL` 报错，测试在 P3、P4 下失败，而解释器上的测试照常通过 |
| 上一轮新加的语料 `r329f_io_lines_args` 离一处已知差异只有一步变异：把 `{}` 换成 `00`，`io.lines` 就收到一个文件名，官方去打开文件，望舒报「不支持」。官方一侧还会真的打开宿主文件 | 差分 fuzz 的 prelude 给 `io.lines` 加了一层：参数是字符串或数字时两侧报同一条 harness 错误；没有参数、显式 `nil`、其他类型照常比对。`internal/oracle/io_lines_guard_test.go` 固定两个方向，另加一条语料。reviewer 点名的几种一步变异输入现在两侧一致 |
| 10 号文档还有两处写「危险函数受配置」「`os.getenv` 嵌入场景可禁用」 | 按现状改 |
| `tablelib.go` 里 `table.setn` 的注释引用了设计文档里已经不存在的说法 | 改掉 |
| README 说完整清单在 `exemptions`，实际逐个名字的清单是 `stdlibAbsent` | 两个语言版本都改了 |
| 文档把 `f:setvbuf` 和 `io.lines(文件名)` 的报错文本指到 #330，而 #330 里没有提到它们 | 在 #330 补了一条评论 |

reviewer 在跑 P4 全量测试时还撞到一次 `TestIssue177_StringLimitContextCanceled` 失败，重跑通过。在 master 的基线提交上
对比过：这个测试依赖一个 5 毫秒的定时器，默认并行度下 400 次里失败 5 次，`GOMAXPROCS=1` 时每次都失败，分支上的表现相同。
与这次改动无关，记在 [#334](https://github.com/Liam0205/wangshu/issues/334)，测试本身没有动。

用新探针扫了一遍现有的分层语料：`p4Corpus` 里有 12 个出错路径用例是同一种情况（升了层、从未进入原生码），`p3Corpus`
没有。把这 12 个临时改写成经包装函数、循环调用之后，内核确实在原生码里出错，结果与解释器、与 `lua5.1` 一致，所以没有
发现缺陷；语料本身的修改不在这一轮的范围里，记在 [#333](https://github.com/Liam0205/wangshu/issues/333)。

第五轮独立审查（全范围，又一位 reviewer）对五条完成条件都没有找到反例，没有阻塞问题和重要问题，提出三条小问题和几处
修改前后都存在的文档问题：

| 审查意见 | 处理 |
|---|---|
| 10 号文档 `file:setvbuf` 一行写「读 stdin 经过 State 自己的 `bufio.Reader`」，实际是进程内共享的一个（`internal/crescent/state.go` 的 `stdinShared`），同一篇文档的 §10.3 也是这么写的 | 改成「进程内共享」；`tablelib.go` 里 `readFormats` 的注释说的是同一件事，一并按代码改了 |
| 分层测试的注释说「尾调用不进入升层代码」，这只对 P3 成立，P4 的尾调用会进入 | 注释和 10 §12.3 都写明是 P3 |
| 代码注释和设计文档正文里有十几处在讲「以前是怎么错的」，与复盘和本文件重复 | 这些位置只留现在的行为和约束，经过只记在本文件和复盘里 |
| `debug.getinfo` 的字段说明不完整：文档写只填 `currentline` / `source` / `short_src` / `what` / `func`，实际还有 `linedefined`，参数是 Lua 函数时有 `lastlinedefined`，带选项 `L` 时有 `activelines`（修改前后都是这样） | README 两个语言版本、09 §13、10 §14、`exemptions` 的理由和 `tablelib.go` 的注释都按实测结果改了 |
| `file:flush()` 一行和 `fileFnFlush` 的注释还写「标准流无缓冲」，与上一轮改过的 `setvbuf` 一行不一致（读 stdin 是经过缓冲的）；`internal/stdlib` 的包注释还写 io / os / coroutine 留到以后做 | 都改成现状 |
| 差分 fuzz 的 prelude 只裁剪全局名字和库表字段，不裁剪文件句柄的元表，所以 `io.stdout:setvbuf(...)`、`io.stdout.seek`、句柄元表的 `__gc` 在两侧不同（修改前后都是这样，现有语料里没有 `io.stdout`） | 在 #330 补了一条评论，与 #331 属于同一类「nightly 撞到会报成缺陷」的存量差异 |

第六轮独立审查（全范围，又一位 reviewer）同样没有阻塞问题和重要问题，提出四条小问题，都在文字上：

| 审查意见 | 处理 |
|---|---|
| `GibbousEntries` 的说明把元方法一概说成由解释器执行，而 `__call` 的处理函数经 `CALL` 指令调用，会进入升层代码 | godoc 两处和指南 §2.2c 改成「`__index`、算术这类由宿主代码回调的元方法」，并写明 `__call` 是例外 |
| 上一轮改的 `debug.getinfo` 字段说明还不准：`lastlinedefined` 和 `activelines` 只在第一个参数是 Lua 函数时返回，按栈层级查询时没有；「像官方一样逐个字母校验」也不对，望舒接受 `>` 而官方报错 | 七处文档按实测结果改；行为上的差异（接受 `>`、按栈层级查询缺两个字段）修改前就有，记在 [#335](https://github.com/Liam0205/wangshu/issues/335) |
| 09 §13.3 的 debug 范围表把 `debug.setmetatable` / `getmetatable` 标成「必做」、`getupvalue` / `setupvalue` 标成「可选」，实际都没有注册；三处缺少函数的清单只列了 12 个里的 6 个 | 表改成「未注册」并补上 `getfenv` / `setfenv` / `debug.debug` 一行，三处清单补全。07 号文档里把 `debug.setmetatable` 当成入口的说法没有动，在 #332 补了评论 |
| 注释里还有五处在讲「以前是怎么错的」 | 删掉，另把本范围新增的注释和文档行逐行读了一遍 |

这一轮还指出两处修改前就有、与 #329 无关的行为：`table` 和 `math` 两个库表的 `pairs` 遍历顺序与官方不同
（[#336](https://github.com/Liam0205/wangshu/issues/336)），`ResetGlobalsToBaseline` 不恢复库表内部被脚本改过的字段
（[#337](https://github.com/Liam0205/wangshu/issues/337)）。

第七轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，三条小问题都在代码注释上：

| 审查意见 | 处理 |
|---|---|
| 分层测试里 `math.mod` 一段的注释说「P4 不升层按字段名调用 `math.mod` 和 `math.fmod` 的函数」，实测这样的函数在 P4 上会升层，不升层的只是其中一种带算术填充的写法 | 注释只留测试自己断言的事实：这种写法两层在强制升层下都升层；自然升温下 P4 在升层代码里执行，P3 不升层 |
| `debugFnGetInfo` 函数体里一行注释还写「`"u"` nups」，与同一个函数的 godoc（`nups` 不返回）矛盾 | 改成「`n` 和 `u` 被接受但不产生字段」 |
| `internal/stdlib` 的包注释说「os、io、debug 不完整」，漏了 base 和 string 里缺的名字 | 改成指向 `stdlibAbsent`，那里逐个列出缺少的名字 |

第八轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，只有一条小问题：`GibbousEntries` 的三处说明列举了
哪些调用不进入升层代码，读起来像完整的清单，却没有提协程（进入升层代码的入口都要求在主线程上）。三处都补上了主线程和
重入深度两个条件，并写明列出的情况只是例子，写测试时直接断言 `GibbousEntries()`。同一轮还指出 README 的「不提供」表
没有列「带文件名的 `io.lines`」（修改前就没有列），两个语言版本都补在「真实文件 IO」一行里；补完重做了 README 的
逐名字删除检查，35 个名字删掉任何一个测试都会失败。

第九轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都在文字上：10 §12.3 和 12 号文档说分层测试
断言「进入过升层代码」，没有写明自然升温那一格只对六段里的四段断言（已写明是哪两段、为什么）；`ioFnLines` 的函数头注释
还只说「不带文件名的 `io.lines`」（已改）。

第十轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都在文字上：

| 审查意见 | 处理 |
|---|---|
| `GibbousEntries` 的三处说明把「Lua 迭代器不进入升层代码」限定成「由解释器的 `TFORLOOP` 调用」，实际泛型 for 所在的函数升层之后也一样（升层代码的 `TForLoop` 同样经宿主回调的路径调用迭代器） | 三处改成不论循环由谁执行。实测：循环函数调用 10 次、进入升层代码 9 次，迭代器调用 40 次、进入 0 次 |
| `debug` 库 12 个未注册函数的原因，六处统一写成「需要解释器内部的钩子」，而 09 §13.3 的表对其中五个给的是别的原因 | 六处不再各写一遍原因，改成指向 09 §13.3 |

这一轮还指出一处修改前就有、与 #329 无关的缺陷：遍历过程中把当前字段赋成 `nil`（`for k in pairs(t) do t[k] = nil end`），
望舒报 `invalid key to 'next'`，官方正常遍历完。原因是删除时把哈希节点的键也清掉了。这是产品缺陷，单独记在
[#338](https://github.com/Liam0205/wangshu/issues/338)，不在这个分支里改。

第十一轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都出在 `debug` 库的文档上：

| 审查意见 | 处理 |
|---|---|
| 文档把 `func` 写成 `debug.getinfo` 总会返回的字段，按栈层级查到 C 帧（层级 0、`pcall` 这类宿主函数的帧、主块之下的 C 帧）时实际不返回 | `debug.getinfo` 的字段说明原来在七处各写一遍，每处都只写了一部分，连续三轮被指出不准。这次把各种查询形式逐个实测，做成一张按查询形式分的表放在 09 §13.4，其余六处只留不随形式变化的部分并指向这张表 |
| 六处说「各个函数的原因见 09 §13.3」，而那张表对 `getupvalue` / `setupvalue`、`getmetatable` / `setmetatable` 没有给出原因 | 这四个函数没有实现，也没有记录过「不做」的决定，表里照实写；六处的说法改成「说明见 09 §13.3」 |

实测时还发现两处 `debug.getinfo` 与官方不同、此前没有记录的行为（负的栈层级、第一个参数是协程），和「按栈层级查到 C 帧时没有
`func`」一起补进了 [#335](https://github.com/Liam0205/wangshu/issues/335)。

第十二轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，只有一条小问题：上一轮在 09 §13.4 新写的一段和
那张表用了半角标点。整段新写的中文改成全角；在旧行上原地改的仍然跟随所在行的写法。这一轮还指出
`debug.getinfo(1, ">S")` 会让官方实现段错误，在差分 fuzz 里表现为整个测试进程崩溃，补进了
[#335](https://github.com/Liam0205/wangshu/issues/335)。

第十三轮独立审查（全范围，又一位 reviewer）有一条阻塞问题和三条小问题：

| 审查意见 | 处理 |
|---|---|
| 阻塞：`TestTiered_StdlibNames` 的 `missing_table_unpack_error` 在 arm64 的 P4 上会失败，报「强制升层没有进入升层代码」。内核里有一条读不存在字段的 `GETTABLE`，P4 的原生翻译不接受；amd64 上它经 head-op replay 仍然升层，arm64 没有这条路径。CI 的两个 arm64 P4 任务会变红，#329 的输入在 arm64 的 P4 上也没有升层路径上的验证 | 这一段是否进入升层代码改成按构建区分的常量（`missingFieldCallRunsCompiled`，arm64 的 P4 为 false，那里只比对结果并断言进入次数为 0）。另加一段 `missing_table_unpack_upvalue_error`：同一个调用经 upvalue 取函数，P3 和两种架构的 P4 在强制升层下都进入升层代码（39 次）。现在共七段 |
| 10 号文档一行之内全角、半角标点混用 | 这一行在半角段落里，整行改回半角 |
| 10 号文档 `io.lines` 一行写「其他非字符串报 `string expected`」，数字其实和字符串一样当作文件名 | 改成「既不是字符串也不是数字的参数」。同样的说法在 `test/api/io_handles_test.go` 和分层测试的注释里各有一处，一起改了 |
| 10 号文档 `os.getenv` 一行写「现在宿主关不掉它」，说得过头（宿主可以在加载脚本之前把它置空，没有的是 `Options` 里的开关） | 改成「没有对应的开关」 |

前十二轮审查和每一轮的自查都只在 amd64 上执行，所以这条阻塞问题一直没有被发现。这一轮起用 `qemu-aarch64`（用户态模拟）
运行交叉编译成 linux/arm64 的测试程序：修改之前这一段确实失败（进入升层代码 0 次），修改之后 `test/difftest`、
`test/tiering`、`test/language`、`test/api` 四个包在 P3、P4 两种构建下通过。darwin/arm64 没有执行过，以 CI 为准。

核对这条时还发现一处修改前就有、与 #329 无关的 P4 缺陷：调用结果赋给两个以上局部变量的函数升层之后，报错里被调用者的
名字不对（`attempt to call upvalue 'u'` 变成 `attempt to call local 'a'`）。它是产品缺陷，单独记在
[#339](https://github.com/Liam0205/wangshu/issues/339)。新加的那一段内核带算术填充，在 P4 上走原生翻译，不经过出问题的路径。

第十四轮独立审查（全范围，又一位 reviewer，这一轮起 reviewer 也用 `qemu-aarch64` 执行 linux/arm64 的测试）没有阻塞问题和
重要问题，只有一条小问题：分层测试比对报错文本时把整个位置前缀都去掉了，行号没有参与比对，而注释给的理由（两侧 chunk 名
不同）只需要去掉 chunk 名。改成只去掉 chunk 名；出错的四段内核原来都写在一行里，行号恒为同一个值，一并拆成多行。
只改升层路径的两种变异（`raiseGibbous` 取下一条指令的行号、`TForLoop` 的出错分支取前面指令的行号）现在各让两段失败，
修改之前七段全部通过；解释器上的测试两种变异下都照常通过。这一轮还确认 #339 的报错行号也不对（`stdin:3:` 变成
`stdin:5:`），补进了那个 issue。

第十四轮的 reviewer 逐项复核第十三轮的发现时，指出 `io.lines` 那一条改得不完整：把数字算进「非字符串」的说法还剩两处
（差分 fuzz 的 prelude 里 `io.lines` 包装的注释，和上面「对使用者的影响」一段）。上一轮全仓查找时用的关键词太窄，
目录也没有包含 `internal/oracle`。这次把分支新增行里所有按类型描述 `io.lines` 参数的句子逐条看了一遍，两处都改了。
它还指出带 P4 标签跑 lint 得到的 72 条是 golangci-lint 默认上限截断后的数，关掉上限是 105 条，linux/arm64 上另有 74 条，
[#340](https://github.com/Liam0205/wangshu/issues/340) 的正文已更正。

第十五轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，有三条小问题：

| 审查意见 | 处理 |
|---|---|
| `TestStdlibAbsentIsInREADME` 读的两个 README 正好在 CI 的忽略路径里（`ci.yml` 的 `paths-ignore` 含 `**.md`），只改 README 的提交不会触发任何检查，不一致要到下一个改代码的提交才暴露 | 新增 `readme-check.yml`，只在两个 README 变化时触发，执行 `make check-readme`（不需要 `lua5.1`，几秒）。这个目标在测试失败时失败，在测试没有执行时也失败。把 README 里的一个名字改掉、把目标里的测试名改成不存在的名字，两种情况目标都失败 |
| #340 在仓库文档里没有登记，其余十个 issue 在 doc-gaps 里各有一条 | doc-gaps 补上 #340 |
| 10 号文档 `io.type(obj)` 一行全角、半角标点混用 | 这一行的后两格是整格新写的，相邻三行整行是全角，这一行第二格的半角括号改成全角。同一张表和 §12.3 里另有三处把报错文本、登记理由的字面写法放在「」里，其中的半角符号是字面内容，改成用反引号标出 |

第十六轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，只有一条小问题：上一轮在 `engineering.md` 新加的
「`check-readme` 目标」一条是整条新写的，却跟着相邻条目用了半角标点，改成全角。这一轮的变异结果里有一处值得补：把
`ipairs` 迭代器改成先检查表、再检查控制值，默认构建下没有测试失败，只有 P3 / P4 构建的分层测试能发现。行为测试原有的
两条用例各只有一个参数不对，分不出检查的先后。补了一条两个参数都不对的用例（官方报第 2 个参数），同样的变异现在在
默认构建下失败。这一轮的 reviewer 还用 Ubuntu 24.04 打包的 `lua5.1`（CI 的 arm64 任务安装的版本）重跑了名字比对，
结果与本机源码构建的相同。

第十七轮独立审查（全范围，又一位 reviewer）有一条重要问题和一条小问题：

| 审查意见 | 处理 |
|---|---|
| 重要：`io.lines` 带文件名的那个分支没有任何测试。把「数字也算文件名」的判断去掉，或者让带文件名的调用直接返回读标准输入的迭代器，全部测试照常通过。差分 fuzz 的 prelude 把这类调用在两侧都换成同一条 harness 错误，所以相关语料和 `TestIoLinesFilenameGuard` 验证的是 prelude，不是望舒的这个分支 | `test/api/io_handles_test.go` 加两个用例（字符串、数字）；`TestTiered_StdlibNames` 加一段 `io_lines_filename_error`，在升层的泛型 for 里走同一个分支。它的结果与官方不同（已登记的差异），所以不与 `lua5.1` 比对，改为三次执行互相比对并与记录的文本比对 |
| `exemptions` 里两条整条新写的理由用了半角标点 | 改成全角 |

这条重要问题说明前面各轮的变异都是按「想得到的错误」挑的，没有按分支逐个做。这一轮把分支对产品代码的全部改动列出来
（`ioFnLines` 的五个分支、工厂函数的两个检查和保活、迭代器的四个检查、`math.mod` 的发布、两个计数点），逐个分支做变异，
共 19 种，各在默认、P3、P4 三种构建下跑 `test/language`、`test/api`、`test/difftest`、`test/tiering`。除了审查指出的两种，
还有两种没有任何测试失败：工厂函数不检查「没有参数」，和 P4 尾调用入口不计数。另有四种在 P3 / P4 构建下靠的是不分构建的
测试，分层测试本身不失败。补的测试：

- 行为测试加 `pcall(pairs)`、`pcall(ipairs)` 两个用例（官方报 `table expected, got no value`）。
- `TestGibbousEntries` 的尾调用一格原来只记录、不断言，改成按构建断言（P3 不进入，P4 进入），常量 `tailCallEntersCompiled`。
- `TestTiered_StdlibNames` 加一段 `factories_called_from_compiled_code`，在升层函数里调用两个工厂函数本身。

补完之后 19 种变异在三种构建下都有测试失败，只有两处是按设计不失败的：没有升层的构建上计数恒为 0，P3 没有尾调用入口。

第十八轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都在 README 检查的覆盖范围上：

| 审查意见 | 处理 |
|---|---|
| `make check-readme` 只在 README 变化时执行。一个只改代码的提交把那个测试改名、或者改了这个目标，要到下一个只改 README 的提交才失败，而且算在与原因无关的提交头上；`readme-check.yml` 自己以后的修改也没有一次执行来验证 | 这个目标另外挂到 `make all` 和 `ci.yml` 的 `test-scripts` 任务上；`readme-check.yml` 的触发路径加上它自己 |
| README 的检查只有一个方向：以后补上一个现在缺少的函数，登记表里的条目会被要求删掉，README 里「不提供」的说法却没有测试约束 | 新增 `TestReadmeListsOnlyAbsentNames`：「不提供」表里列出的名字，望舒必须确实没有。因为行为不同而列出的七个已提供名字登记在 `readmeListsProvided`，登记过期也失败。在工作区的副本里做了八种变异（给 io 库加上 `type` 而 README 不改、README 多列一个已提供的函数、README 去掉登记过的名字、去掉一个缺少的名字、两个测试各自改名、包编译不过），`make check-readme` 每次都以非零退出 |

这一轮还发现一项与 #329 无关、此前没有登记的存量问题：`make test-trace`（trace 构建的全仓测试）在 master 上不能通过，
两个包共 16 个测试失败。CI 和 `make all` 都不执行这个目标。记在 [#341](https://github.com/Liam0205/wangshu/issues/341)。

第十八轮的 reviewer 复核第十七轮那次逐分支变异时，指出划分漏了一处：`ipairs` 迭代器里「给了第二个参数才去读控制值」
这个条件。给了表而控制值缺失或者是 nil 时，官方报 `number expected, got no value` / `got nil`；把这两种情况当成 0 的
变异在全部测试下通过。补了三处：

- 行为测试加三个用例：控制值缺失、是 nil、是布尔值。
- `TestTiered_StdlibNames` 的 `ipairs_iterator_argument_errors` 加两种：泛型 for 不写控制值（这时传给迭代器的是 nil）、
  控制值是布尔值。
- 差分 fuzz 的语料加一条 `r329h_ipairs_iter_no_control`。

手工列分支已经漏了两次，所以改用脚本对本分支改写的三个函数（`registerIterFactory`、`ipairsIter`、`ioFnLines`）做机械变异：
每个 `if` 强制为真、强制为假，`&&` / `||` 的每个操作数单独去掉，比较运算符取反，参数下标加一，报错文字改动，返回值个数改动，
共 54 种，每一种在默认、P3、P4 三种构建下各跑 `test/language`、`test/api`、`test/difftest`、`test/tiering`。53 种在三种构建下
都有测试失败。
剩下的一种是等价变异：`ipairsIter` 里 `st.RawGet` 之后的 `if e != nil`，`RawGet` 不会返回错误，这个分支执行不到，
这一行不是本分支写的，没有改。

第十九轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都是文字：

| 审查意见 | 处理 |
|---|---|
| `engineering.md` 有一处还写「读 Markdown 的测试目前只有一个」，同一份文档的另一处已经列了两个 | 改成指向 `make check-readme` 执行的那几个测试，不再写个数 |
| `table.setn` 的三处说明写成官方「就是一句 `luaL_error`」「直接报 `'setn' is obsolete`」。官方 `ltablib.c` 先检查第一个参数是表，再报这个错。实现一直是对的 | 函数头注释和 10 号文档的四处一起改成「先检查第一个参数是表，然后报错」 |

第二十轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，一条小问题：`ci.yml` 里说明 `check-readme` 那一步的注释
写的是 `readme-check.yml`「只在 README 变化时触发」，而那个工作流在自己变化时也触发。注释改了，`engineering.md` 里同一件事的
说法一并补全。

这一轮还发现两项与 #329 无关、此前没有登记的存量差异，都在 master 上复现过，差分 fuzz 都能直接看到：

- 表构造式以多值展开结尾（`{...}`、`{f()}`、`{1, f()}`），展开出来的值里有 nil 时，`#t` 和 `unpack(t)` 给出的个数
  可能与官方不同，`local args = {...}` 之后 `f(unpack(args))` 传出去的参数个数因此可能不对。记在
  [#342](https://github.com/Liam0205/wangshu/issues/342)。
- `io.stdout.close()` 不传句柄时报 `got no value`，官方报 `got nil`。记在
  [#343](https://github.com/Liam0205/wangshu/issues/343)。

第二十轮的 reviewer 复核第十九轮的 `table.setn` 一条时，把本分支新增的、提到官方 C 函数的注释和文档逐条对着源码读了一遍，
又找到一处同类的错误：`ipairs` 迭代器的两处注释写 `luaL_checkint` 经过 `lua_tonumber`，官方经过的是 `lua_tointeger`
（`luaL_checkinteger` 调用它，结果为 0 时再调 `lua_isnumber`）。结论「数字字符串是合法的控制值」没有错，函数名改了。
我随后把这些注释和文档句子带着文件名和行号列出来（Go 文件 40 多行，Markdown 十余行），对着 `lbaselib.c`、`liolib.c`、
`ltablib.c`、`lmathlib.c`、`lstrlib.c`、`lauxlib.c`、`lapi.c` 的对应函数逐条核对，没有找到第三处。

第二十一轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，一条小问题：分层测试里
`factories_called_from_compiled_code` 一段有 `viaIpairs`、`viaPairs` 两个内核，断言只要求进入升层代码的次数大于 0。
把其中一个内核改成不升层的写法，进入次数从 78 降到 39，这一段照常通过，那一半的覆盖就没有了。这一段拆成
`ipairs_called_from_compiled_code` 和 `pairs_called_from_compiled_code` 两段，各一个内核；同样的变异现在让对应的一段在
P3、P4 下失败。拆开后的十段在 amd64 和 linux/arm64（qemu）的 P3、P4 上通过。其余八段本来就各只有一个会进入升层代码的函数
（出错的五段里包装函数 `call` 也被编译了，但它只被 `pcall` 调用，不进入升层代码）。

第二十二轮独立审查（全范围，又一位 reviewer）在当时的提交上没有任何问题。审查期间我按第二十一轮 reviewer 的复核意见
改写了 #342 的说明，把这个提交并进第二十二轮的范围之后，reviewer 指出改写的文字有两处不对：

| 审查意见 | 处理 |
|---|---|
| 「最后一个值也是 nil 时两侧相同」不成立：传入 `1, 2, nil, 4, nil` 官方得 2、望舒得 4。望舒的结果有时比官方大；最后一个值不是 nil 时两侧也不一定不同 | 我写这句话的依据只有 `1, nil, nil` 一组输入。改成先量再写：把长度 1 到 8、每个位置是值或 nil 的 510 种序列全部经 `{...}` 和 `{0, ...}` 构造一遍，在官方和望舒三种构建上比对。`{...}` 的结果：含 nil 的 502 种里 281 种两侧不同，望舒偏小 273 种、偏大 8 种；最后一个值不是 nil 的 247 种，官方都等于总个数，其中 188 种两侧不同；最后一个值是 nil 的 255 种里 93 种不同；不含 nil 的 8 种都相同。`{0, ...}` 有 244 种不同。doc-gaps、这份记录里对应的一句和 #342 的正文都按实测重写 |
| 原因一句写成「中间的 nil 落进哈希段」。nil 不会存进表里，落进哈希段的是 nil 之后的值 | 改正 |

「写死个数的字面量不受影响」这一句原来也只看了两个字面量，这次用 16 个带 nil 的字面量（最长 120 个元素，跨过
`SETLIST` 每 50 个一批的边界）量过，两侧相同。

reviewer 同时说明，doc-gaps 里其余几个 issue 的说明它只读过、没有执行。我把本分支在 doc-gaps 里新增的十四条里能执行的
说法写成脚本，在官方和望舒上各执行了一遍，都与文字相符：#338（哈希键、小数键报错，数组部分的键不报错；官方测试套件里
对应的两段在截断行之后）、#339（两种情况都按 issue 里的脚本在 P4 强制升层下复现）、#343（`close` 不同，`flush` /
`write` / `read` / `lines` 相同）、#330（列出的名字都没有注册；`setvbuf` / `seek` / `__gc` 官方有、望舒没有；语料里没有
`io.stdout`）、#334（`GOMAXPROCS=1` 五次都失败）、#335（`>` 的两种形式、负层级、协程参数、各字段；`>S` 让官方实现
段错误）、#336（键集合相同，顺序不同）、#337（重置之后 `math.floor` 仍是 nil）、#331（独立解释器一侧的栈回溯末行）。
这一次没有重新执行的有四项，依据是前几轮的结果：#341 的 trace 构建（输出约 10 GB）、#340 的 lint 条数、
#333 的 12 个用例、#331 里参照实现内嵌用法一侧的输出。

第二十三轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题都是文字：

| 审查意见 | 处理 |
|---|---|
| 上面第二十一轮一段写「其余八段本来就各只有一个会升层的函数」。出错的五段里包装函数 `call` 也被编译了，强制升层下这五段的 `PromotionCount()` 是 2 | 我用临时测试打印了十段的升层数和进入次数，与 reviewer 的结果相同。那一句改成「各只有一个会进入升层代码的函数」并写明包装函数只被 `pcall` 调用；指南 §2.2c 里对应的判据也按这个说法写 |
| `engineering.md` §3.1 的 YAML 片段里整行新写的一条注释用了半角标点 | 改成全角 |

这一轮还指出一项修改前就有的文档问题：`engineering.md` §1 的 Makefile 片段与真实的 Makefile 不一致（`all:` 的依赖、目标清单、
「七件套」的个数）。本分支只在两边的 `all:` 里各加了 `check-readme`。记在
[#344](https://github.com/Liam0205/wangshu/issues/344)。

第二十四轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，三条小问题是同一种情况：本分支改了一处，
说同一件事的另一处没有跟着改。

| 审查意见 | 处理 |
|---|---|
| 10 §8.6 写「`mathExtraFns` 里 `fmod` 与 `mod` 也指向同一个实现」。`mod` 已经不在那张表里，改由 `OpenAll` 设成 `fmod` 的闭包 | 改成指向 `OpenAll`。本分支从注册表里删掉的条目（`pairs`、`ipairs`、`mod`）和删掉的函数（`baseFnPairs`、`baseFnIpairs`）在文档和注释里的引用都查了一遍，没有别的残留 |
| `internal/stdlib/baseenv.go` 的文件头注释还把范围总表写成「10 §11」。同一种引用本分支在另外两个文件里改成了 §14，这里漏了 | 改成 §14。接着把 Go 文件里所有「10 §N」的引用用脚本对着 10 号文档的章节标题核对了一遍，又找到三个文件里四处指错了节（模式匹配写成 §7，应为 §6；string 库写成 §7-§8，应为 §5-§6；宿主函数调用约定写成 §3，应为 §1）。10 号文档的章节编号从第一版起没有变过，这几处是写下时就错的，一并改了。本分支新增的 Markdown 行里 71 处章节引用也核对过，都存在 |
| 两个 README 列举 `make all` 的内容时没有 `check-readme` | 补上，同时补上修改前就漏列的 `test-scripts`，现在与 Makefile 的 `all:` 逐项相同 |

reviewer 另外列了三处修改前就过期、本分支没有碰的文字：`test/difftest/doc.go` 还写着「Currently a placeholder」；
`docs/design/architecture.md` 的目录树列的工作流（`nightly-benchmark`、`release`）与 `.github/workflows/` 里实际的文件不符；
`docs/design/p3-wasm-tier/08-testing-strategy.md` 引用的 `all:` 一行是旧的。它们与 #344 是同一类问题，补在那个 issue 的评论里。

第二十五轮独立审查（全范围，又一位 reviewer）没有阻塞问题和重要问题，两条小问题：

| 审查意见 | 处理 |
|---|---|
| README 反方向的检查看不到 `debug` 这个名字。`debug.debug` 在登记表里，全局名 `debug` 因此被当成可能是库函数的缩写；而含有带反引号的 `debug` 的格子一定被算作「提到了 debug 库」，于是每个格子都被跳过。在「不提供」表里加一行把整个 debug 库写成不提供，三个测试都通过。望舒提供 114 个名字，reviewer 把其中 106 个逐个加进表里（没有试的 8 个是登记过、本来就允许出现的 7 个，和 README 里没有对应写法的字符串元表一项），只有这一个不失败 | 匹配规则加一条：库自己的名字不受这个限制（带反引号的 `debug` 不会是 `debug.debug` 的缩写，那条规则本来就有）。两个 README 的 debug 一行原来给库名加了反引号，按新规则它就成了「列出了全局 `debug`」，所以去掉那一处反引号。`TestReadmeListsMatcher` 加两个用例。我重做了两个方向的逐名字检查：114 个已提供的名字逐个加进表里，都能被看到；35 个缺少的名字，在列出它的那一行里去掉对应的词，都判为未列出。实际在两个 README 里加一行「整个 debug 库不提供」，`make check-readme` 失败 |
| 指南新增 §2.2c 之后，同一份指南里另外几处仍把 `PromotionCount` 当作「路径被执行过」的证据，速查一节也没有条目指向 §2.2c | 指南里提到 `PromotionCount` 的位置逐个读了一遍，五处对齐到 §2.2c 的结论（它是编译侧的量），速查一节加一条。指南之外提到它的文档也查了（README 两处、另外三份指南、12 号文档、P4 的测试策略文档和进度记录）：它们或者只把它说成「已升层的 Proto 数」，或者把它与执行侧的计数器（`NativeRunCount`、各分支的命中计数）一起断言，没有哪一处单独拿它当「升层代码执行过」的证据，没有改 |

这一轮还发现一项与 #329 无关、此前没有登记的存量差异：`collectgarbage("setpause")` 和 `("setstepmul")` 固定返回 0，官方返回设置之前的
值，而设计文档把 `setpause` 列为完整提供。记在 [#345](https://github.com/Liam0205/wangshu/issues/345)。

第二十五轮的 reviewer 复核第二十四轮改节号的那一条时，顺带指出 `internal/crescent/host.go` 文件头注释里写的 `HostFn` 签名带一个
`*thread` 参数，同文件里实际的类型没有。这一行是修改前就不对的；本分支改过同一段注释里的节号，所以把签名也改成与类型一致。

验证：`make all` 与 `make fuzz-oracle` 通过。把 `internal/stdlib` 的 `stdlib.go` 和 `tablelib.go` 一起换回修复前的版本：
行为测试 10 个用例失败，`test/api` 的 `io.lines` 用例 4 个失败，`TestTiered_StdlibNames` 在 P3、P4 两种构建下十段脚本里
六段失败（剩下的四段修复前后结果相同：迭代器对坏参数的报错、带文件名的 `io.lines`、从升层代码里调用两个工厂函数各一段，它们的作用是
在升层路径上执行这几条路径），名字比对报出两个多余
名字和一组缺少的别名，README 反方向的检查失败（README 写着不提供的 `table.unpack` 又有了），#329 的输入和四条回归语料失败。名字比对测试另做了两种变异（删一条登记、加一条过期登记），都会
失败。去掉 prelude 里的检查，`TestIpairsIteratorGuard` 报 8 处、`TestIoLinesFilenameGuard` 报 5 处；`exemptions` 改一个
条目名，`TestStdlibAbsentReasonsResolve` 失败；README 的逐名字删除见上面第三轮的表。

机制见 [10 §4.4 / §4.5 / §9.5 / §12.3](./10-stdlib.md)，过程记录见
[#329 复盘](../../../llmdoc/memory/reflections/2026-11-25-issue329-stdlib-extra-names.md)。

## 2026-11-27：文件句柄的 `close` 不带参数时报 `got nil`（#343）

`io.stdout.close()` 官方报 `bad argument #1 to 'close' (FILE* expected, got nil)`，望舒报 `got no value`。#329 的独立审查发现的，
修改前三种构建相同，只影响报错文字。

官方的句柄方法 `close` 就是 `liolib.c` 的 `io_close`：没有参数时先 `lua_rawgeti(L, LUA_ENVIRONINDEX, IO_OUTPUT)`，再检查第一个参数是
不是文件。句柄方法是在 `luaopen_io` 换上私有环境之前注册的（`createmeta` 在 `lua_replace(L, LUA_ENVIRONINDEX)` 之前），环境是全局表
（官方里实测：`rawset(_G, 2, io.open("/dev/null"))` 之后 `io.stdout.close()` 返回 true，那个文件变成 `closed file`；`getfenv` 对任何 C 函数
都返回全局表，不能用来判断），`IO_OUTPUT` 是 2，
所以压进去的是全局表的原始字段 `[2]`：通常是 nil，报 `got nil`；脚本往 `_G[2]` 放过值时按那个值检查（数字报 `got number`，标准流则返回
`(nil, "cannot close standard file")`）。`fileFnClose` 在没有参数时读全局表的 `[2]`（不经过元方法）再检查。
`flush`、`read`、`write`、`lines` 在官方实现里没有这一步，不带参数时两侧都报 `got no value`，没有改。

把三个标准流的七个方法在「不带参数、nil、数字、表」四种情况下的报错与 `lua5.1` 逐行比过，除 `close` 这一处以外，不同的只有望舒
没有提供的 `seek`、`setvbuf` 和元表的 `__gc`（都是已经登记的豁免）。

测试：`test/api` 的 `TestIOHandles_MatchPUC` 加 16 个用例（三个标准流、按名字调用、`_G[2]` 是数字 / 表 / 标准流 / 只能经 `__index` 取到、
显式 nil、数字、多余参数，以及另外四个方法不带参数时的对照）；差分 fuzz 语料 `r343_file_close_no_handle` 和 `r343_file_close_reads_globals_2`；`test/difftest` 的探测
`corner_io_close_without_handle_reads_globals_2`；`TestTiered_StdlibNames` 加一段 `file_close_without_handle`，在 P3 / P4 的升层代码里
调用。只撤掉 `fileFnClose` 的改动，这四处都失败。

## 2026-11-27：`collectgarbage("setpause")` / `("setstepmul")` 返回设置之前的值（#345）

两个选项固定返回 0，官方返回设置之前的值（默认都是 200）。[10 §4.6](./10-stdlib.md) 一直把 `setpause` 写成「设 GCPAUSE，返回旧值」、
把 `setstepmul` 写成「存值但不生效，返回旧值」，实现没有按它做。#329 的独立审查发现的。

| 改动 | 位置 | 说明 |
|---|---|---|
| `setpause` 设置回收器的 pause 比例并返回旧值 | `baseenv.go`、`gc/collector.go::SetPause` | 新值在下一次回收结束、计算下次触发阈值时生效，与官方的 `setthreshold` 时机相同 |
| `setstepmul` 存值并返回旧值 | `gc/collector.go::SetStepMul` | 回收器不是增量的，这个值不参与任何计算 |
| 第二个参数按 `luaL_optint(L, 2, 0)` 转成 int | `baseenv.go` | 之前只检查它是不是数字，没有用它的值 |
| 阈值的计算改用饱和算术 | `gc/collector.go::pausedBytes` | 比例现在来自脚本：极大的比例不能让乘法回绕成一个很小的阈值；负数按官方的效果处理成「不再自动回收」 |

**比例的含义与官方不同，登记为差异**：望舒拿 `live × 比例 / 100` 与上次回收以来新分配的字节数比较，官方拿它与总字节数比较，望舒的
比例 `p` 大约相当于官方的 `p + 100`。这是回收器一直以来的做法，06 §8.3 原来写的「共 2*live 触发」与代码
不符，这次改成了代码的实际行为。`setpause` 在 10 §4.6 里标成 △，登记进 README 的差异表和 `exemptions`，`TestDiff_GCPauseMeaning` 固定
两边各自的结果，`corner_gc_setpause_ratios_in_between` 在两边结果相同的中间取值上比较。要不要改成官方的口径、池化复用时要不要恢复
回收器的设置，记在 [#353](https://github.com/Liam0205/wangshu/issues/353)。

选项和第二个参数都不合法时先报选项（`collectgarbage("bogus", "x")` 报 `invalid option 'bogus'`），与官方 `luaL_checkoption` 在前、
`luaL_optint` 在后的顺序相同；之前先报第二个参数。第二个参数超出 64 位整数范围时官方的结果是 C 的未定义行为、随平台变化，望舒固定
采用 x86-64 的结果，登记在 README 的「C 未定义行为」一行和 10 §4.6。

`step` 没有改：仍然做一次完整回收并返回 `true`，官方返回的是这一步有没有结束一轮回收。这是登记过的差异（README「增量 GC」一行、
`exemptions`），两处的文字从「只返回占位值」改成了现在的实际行为。

测试：`test/difftest` 的 `cornerProbes` 加 11 条 `corner_gc_*`，直接与 `lua5.1` 比较（差分 fuzz 的 prelude 把 `collectgarbage` 换成了桩，
看不到它）：返回旧值（两条）、不带值、各种数值转换（两条，其中一条是超出 int32、仍在 int64 之内的数）、参数不是数字时的报错、选项和第二个参数都不合法时先报哪一个、设置的比例在回收之后
仍然读得到、其余选项仍返回 0，一条验证比例确实生效的（比例为 0 时只被弱表引用的对象在分配过程中被回收，比例为最大值时没有被回收），
以及一条在两边结果相同的中间取值上比较的。`TestDiff_GCPauseMeaning` 固定两边在比例含义上的不同。`internal/gc` 的 `TestSetPauseDrivesTheThreshold` 直接断言阈值，`TestCollect_CompactFollowsThePauseRatio` 断言回收之后保留的
arena 大小跟着比例变。`TestTiered_StdlibNames` 加一段
`collectgarbage_set_returns_previous`，在 P3 / P4 的升层代码里调用。

## 2026-11-27：删除的键留在哈希节点里，遍历过程中可以清空字段（#338）

`for k in pairs(t) do t[k] = nil end` 报 `invalid key to 'next'`，官方正常遍历完；只有落在数组部分的键不受影响。#329 的独立审查
发现的，在 master 上三种构建相同。手册对 `next` 的说明明确允许遍历过程中清空已有字段。

原因是 `rawSet` 删除时把哈希节点的键也清成了 Nil，`rawNext` 按键找不到它所在的节点。官方 `luaH_set` 只把值置成 nil，键留在节点里。

| 改动 | 位置 | 说明 |
|---|---|---|
| 删除只清值，键和链接留在节点里 | `rawtable.go::rawSet` | 节点有三种状态：空闲、有值、已删除，见 [01 §5.2](./01-value-object-model.md) |
| 同一个键再写入时回到原节点 | `rawSet` | 查找时按键命中已删除的节点，值写回原处 |
| 主位置上已删除的节点可以被新键接管，保留链接 | `rawtable.go::insertNewKey` | 对应官方 `newkey` 的 `ttisnil(gval(mp))`；节点可能在别的冲突链中间，所以 `next` 不动 |
| 找空闲节点时跳过已删除的节点 | `rawtable.go::findFreeNode` | 判断条件没有变（键为 Nil 才算空闲），变的是已删除的节点现在有键 |
| 已删除的节点没有 IC 位置 | `ic.go::rawGetWithLoc` | 返回 `locNone`，IC 不会回填到没有值的节点上 |
| 已删除节点里的键不让对象存活，字符串除外 | `gc/mark.go::scanTable` | 对应官方 `traversetable` 的 `removeentry` |
| 对象已死的键在 sweep 之前换成 `object.DeadKey` | `gc/sweep.go::clearDeadKeys`、`clearWeakTables` | 强表登记在 `deadKeyList`；节点仍然是已删除状态，见 [06 §8.4](./06-memory-gc.md) |
| 弱表清项只清值，键保留 | `gc/sweep.go::clearWeakTables` | 遍历弱表时回收清掉了当前条目，遍历可以继续 |
| `table.foreach` 和公开 API 的 `Table.ForEach` 在回调期间固定当前的键 | `stdlib/tablelib.go`、`table.go` | 回调清掉条目、丢掉键再回收时，下一步仍然要从这个键继续；官方的 `lua_next` 把键留在 C 栈上 |

**gen 约定的变化**（完整的表在 [05 §6.5](./05-interpreter-loop.md) 和
[design-claims-vs-codebase-physics](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) §2.1）：

- 删除仍然 `BumpGen`，每次「有值 → 已删除」一次；对已删除的键再赋 nil 不 bump。
- 新键接管已删除的节点时 `BumpGen`。P3 的 `emitSetGlobal` 内联写 nil，不经过 `rawSet`，所以节点换键的这一刻是读方快照唯一的失效点。
- 回收器替换键、清弱表条目时 `BumpGen`，每张表一次。

**脚本能观察到的行为变化**：

1. 遍历过程中把字段赋成 nil 不再报错（哈希部分的键；`pairs`、`next`、`table.foreach` 都是）。
2. `next(t, k)` 接受一个已经从表里删掉、但节点还在的键，从它的位置继续；从来不在表里的键仍然报 `invalid key to 'next'`。
   键是字符串时有一种情况与官方不同，见下面「与官方不同、有意保留」。
3. 删除之后再插入新键的遍历顺序变了：已删除的节点不再被任意新键占用。顺序本身与官方仍然不同（[#349](https://github.com/Liam0205/wangshu/issues/349)）。
4. 遍历弱表时发生回收、当前条目被清掉，遍历可以继续。
5. 作为键被删除的字符串会多存活到所在的表下一次 rehash；原地清空之后闲置的表不释放它放过的字符串键（见下面「与官方不同、有意保留」）。
6. 删除一个键之后又有键写入，`next(t, 已删除的键)` 报不报错取决于那个节点有没有被接管或者被 rehash 丢掉。两边可能一个报错、一个继续，
   两个方向都有，原因有三个：哈希部分的布局与官方不同（[#349](https://github.com/Liam0205/wangshu/issues/349)）；官方每次回收都把
   已删除的键标成 dead key，之后同一个键写回去按新键插入、可能引起 rehash，望舒总是写回原节点；官方给不存在的键赋 nil、或者赋值被
   `__newindex` 接走时也会给键建一个节点（占掉空闲节点、可能引起 rehash），望舒不建。修改前望舒在这种情况下一律报错。
   第三个原因本身修改前就有（`t.x = nil` 之后 `next(t, "x")`，官方成功、望舒报错）；修改前它引起 rehash 的那一半看不出来，因为那时
   望舒对已删除的键总是报错，现在能看出来了。

**测试**：

- `internal/crescent`：`TestRawTable_DeleteKeepsKeyAndNode`、`TestRawTable_DeadNodeTakeoverBumpsGen`、
  `TestRawTable_DeadNodeIsNotAFreeNode`、`TestRawTable_TakeoverKeepsChain`、`TestRawTable_RehashDropsDeadNodes`、
  `TestRawNext_ContinuesFromDeletedKey`，回收器一侧的 `TestDeadKey_*` 和 `TestWeak_*`，以及随机模型比对
  `TestRawTable_ModelCheck`（40 个种子，每个 3000 步，每一步与 Go map 比较并检查哈希链的结构）。
- `test/difftest`：`TestDiff_ClearWhileTraversing`，16 段结果与遍历顺序无关的脚本，不经过 prelude，直接与 `lua5.1` 比较，在普通回收
  和每个安全点都回收两种方式下各执行一遍；`nextvar.lua` 的「erasing values」一段原样收在里面。`TestTiered_ClearWhileTraversing`，6 段
  脚本在 P3 / P4 下按「解释器预热、再强制升层」执行，断言进入了升层代码。`TestDiff_NextFromCollectedStringKey` 固定上面第 2 条里与官方
  不同的那一种情况（见下）。
- `test/api`：`TestTableForEach_ClearingEntries`。

变异验证：24 处改动逐个撤掉或改坏，每一处都有测试失败。其中「接管时不 bump」由单元测试和 P3 下的
`deleted_global_node_taken_over` 发现（P4 下没有分层用例能看到它：P4 的内联路径都不写入 nil，删除总是经过表操作、在删除时 bump）；
「删除时不 bump」只由单元测试发现（现在没有哪个内联路径的正确性依赖这一次 bump，见下面 P4 模板一段）；P4 模板「不内联写入 nil」
这一项只由模板的字节布局测试发现（去掉它之后槽位变成 nil，下一次赋值照样退回宿主，行为上没有差别）。把同一份 `TestDiff_ClearWhileTraversing` 放到修改前的 master 上，
16 段里 13 段失败（剩下的 3 段修改前后结果相同：数组部分的键、被删除的键不让对象存活、删除之后隔着回收再写回）。

**与官方不同、有意保留**：

- 已删除的节点让字符串键存活。`t[k] = nil`、回收、再用重新拼出来的同内容字符串调用 `next(t, k)`，望舒从原位置继续；官方不让这样的键
  存活，回收之后只按对象地址认它，新字符串没有正好分配到原地址上就报 `invalid key to 'next'`。没有发生回收时两边相同。官方的结果
  取决于回收有没有在中间发生，望舒的不取决于。代价是内存：被删除的字符串键要到下一次 rehash 才能回收，原地清空之后闲置的表会一直
  占着它们（10 万个约 105 字节的键，清空并回收之后官方剩 5402KB，望舒仍是 15584KB）。这个取舍要不要保留记在
  [#352](https://github.com/Liam0205/wangshu/issues/352)。`TestDiff_NextFromCollectedStringKey` 固定望舒一侧的结果。
- 上面第 6 条。`TestDiff_NextOnRemovedKeyAfterStores` 固定七段脚本两边各自的结果：前三段的原因是布局，#349 解决之后应当变成两边
  相同；第四段的原因是回收，它与上一条是同一个取舍（表的状态不随回收时机变化）；后三段的原因是官方给「不留下值的赋值」也建节点，
  要不要照做记在 #349（[01 §5.2](./01-value-object-model.md)）。

两条都登记在 `exemptions`、README 的差异表里，第一条另见 [06 §8.4](./06-memory-gc.md)。

**P4 单条 SETTABLE 的模板一并修了（[#347](https://github.com/Liam0205/wangshu/issues/347)）**。函数体只有一条常量键赋值时 P4 用专用
模板编译（`EmitSetTableNodeHit` / `EmitSetTableArrayHit`，arm64 同），模板原来只检查「是表、gen 相同、节点里的键相同」，不看槽位现在
有没有值，也不比对是不是原来那张表。删除保留键之后，这两点合在一起会出错：用同一个构造函数建出来的另一张表 gen 相同，它的这个
字段被删除之后键还在节点里，模板的三项检查全部通过，值被直接写进已删除的节点，`__newindex` 没有被调用。#347 原来登记的「模板自己把
字段赋成 nil、再赋值」是同一个缺口在同一张表上的表现。现在四个模板都检查「槽位现有的值不是 nil」和「要写入的值不是 nil」，不满足
就退回宿主的慢路径；模板内联完成的只剩「非 nil 的值覆盖非 nil 的值」这一种赋值，它本来就不查 `__newindex`。
`TestTiered_SingleStoreTemplate`（5 段，哈希部分和数组部分各有「自己删了再写」「换一张已删除的表」两种）在 P4 的 amd64 和 arm64 上
都进入了模板；撤掉模板的改动，前 4 段失败。
这两项检查读的是固化下标对应的槽位，前提是下标在这张表之内。模板不检查这一点（[#350](https://github.com/Liam0205/wangshu/issues/350)，
修改前就有）：换一张 gen 相同但数组部分或哈希部分更小的表，读到的是表外的内存，跳过 `__newindex` 的现象在这样的表上仍然会出现，要等
#350 修掉。

**这一轮没有修、另外登记的存量问题**：弱表会清掉字符串（[#348](https://github.com/Liam0205/wangshu/issues/348)）；
哈希部分的遍历顺序与官方不同（[#349](https://github.com/Liam0205/wangshu/issues/349)）；P4 单条表访问的模板不检查下标是否在表内
（[#350](https://github.com/Liam0205/wangshu/issues/350)）；P4 arm64 的同一组模板在数组下标 4096 以上、哈希部分节点下标正好是 1365
时访问到错误的槽位（[#351](https://github.com/Liam0205/wangshu/issues/351)）。

过程记录见 [#338 复盘](../../../llmdoc/memory/reflections/2026-11-27-issue338-deleted-key-stays-in-node.md)。

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md)

> **补记（同轮第二次审计）**：上面那条「兄弟写法都便宜」的结论只对**单次调用**成立。把同样的工作摊到
> 多次调用上——99 次、每次 2^20-6 个元素——仍然完全参与比对，两侧合计约 11 秒，而指令计数钩子拦不住它
> （每次移位都在一次 C 调用内部完成）。所以 prelude 又加了**累计**预算（`__shiftTotal`，上限 2^22），
> 并由 `internal/oracle` 的 `TestInsertShiftCumulativeBudget` 固定；实测把预算去掉或调得过紧都会让它变红。
