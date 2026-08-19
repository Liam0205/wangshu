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
| context cancellation 钩子 | `State.SetContext(ctx)` / `RemoveContext`:VM 在 chargeStep 同一抢占点(回边 + 函数进帧 + TFORLOOP)检查 `ctx.Err()`,事件触发(wall-clock timeout / 上游 Cancel)中止 Run/Call 返回 Go error(pcall 可捕获);跨 goroutine 由 atomic.Pointer 保护;chargeStep 三处调用点合一(stepBudget 外层 if 拿掉,内部短路),零额外抢占点 | 11 §10 / 与 SetStepBudget 并存 (issue #4) | `27b4f2e` |
| Table.ForEach 任意 key 迭代 | `func (t *Table) ForEach(fn func(key, val Value) bool) error`:转发 internal `RawNext` 循环(raw 迭代,与 stdlib next/pairs 同源,迭代序确定性);fn 返 false 提前终止;key/val 走 `fromInnerWithPin` 自动登记 pin 槽。issue #2 SetIndex 写入的对称读出能力,完整读写闭环 | 11 §4.5 (issue #5) | `4f855d2` |
| globals baseline 状态隔离 | `State.MarkGlobalsBaseline` 拍当前 _G 字符串 key 快照、`ResetGlobalsToBaseline` 非 baseline key 清空 + baseline key 复原;baseline 复合值经 `visitExtraValues` 入 GC 根(与 pin 表是 GCRef-bearing value 契约级不变式两面:pin 管「公共 API 暴露的长持 GCRef」、baseline 管「内部状态恢复需要的长持 GCRef」);对位 gopher-lua statePool snapshotBaselineValues + resetToBaseline 模式 | 10 §12.1 hardening (issue #6) | `3d34839` |
| CallInto 零分配边界路径 | `State.CallInto(dst []Value, fn, args...) (n int, err)`:返回值写进调用方拥有的 `dst`,标量(bool/number)整条 round-trip 0 alloc。消除旧 `Call` 的双拷贝地板成本(VM 栈→inner slice→public slice,72 B / 2 allocs/call,与脚本复杂度无关)——内部 `callOnStack` 零拷贝切 `th.stack` 活动区(runningThread 复位后 mainTh 仍是常驻根 → GC 下可达),门面层复用 `innerArgsBuf` + 写调用方 dst。`Call` 保留为独立拷贝便捷形(返回值跨下次 Call 仍可读),内部走 callOnStack 后 append 一次。⚠️ 契约:CallInto 返回值底层是复用栈,下次进入 VM 前消费完;string 仍拷 arena 字节、复合值仍经 pin 表 | boundary-dominated 嵌入优化 (issue #8) | `CallInto` |
| step budget 按字节工作量记账 | 共享 `doConcat`(`internal/crescent/call.go`,fast path + slow-path plain fold)调 `chargeBulkWork(len)`(`internal/crescent/state.go` 新增),按 `len >> 6`(1 步 / 64 字节)把 CONCAT 拷贝 + intern 的字节工作量折算进 step budget,使预算成为**字节工作量的度量**而非仅指令条数。动机:`preempt()` 原本每指令边界只把 stepUsed 加 1,单条 CONCAT 能做与字符串长度成正比的无界工作 ⟹ `for i=1,N do glob=cat(i) end`(cat 内 `return "<~15KB 字面量>"..i`)每次迭代只扣约 2 步却拷贝约 15KB,1<<20 预算允许约 50 万次迭代、单次 prog.Run 约 2.7s wall-clock,4×run 撞 Go fuzz 10s per-input 看门狗(concat 风暴 crasher 家族 #166/#167 根因)。三层(P1 executeLoop / P3 wasm h_concat / P4 native host.Concat)全部路由同一 doConcat,单点记账覆盖所有 backend,差分对称性不破;<64B 记 0 步、1MiB concat 记约 16K 步(约 1.5% 预算),正常程序不受影响。比率 `>>6` 是按最慢的 CI runner(比本地慢约 10×)实测收紧后的值。**当时点名的下一批候选无界单指令算子(string.rep / string.format / table.concat)已于 2026-08-03 结算**,见下一行 | concat 风暴家族根因 (#166/#167,PR #168) | `88e724f` + `ab27936` |
| 三个批量字符串构造函数按字节记账 | `internal/crescent/state.go` 把 `chargeBulkWork` 导出成 `ChargeBulkWork`,`string.rep`(`stdlib.go::stringFnRep`,`len(s)*n`)/ `string.format`(`stringlib.go::stringFnFormat`,`len(out)`)/ `table.concat`(`tablelib.go::tableFnConcat`,分隔符字节 + 各元素字节之和)各自按**产出字节数**走**同一个**计量器,于是「批量工作」在 step budget 里只有一个定义、而不是三个会互相漂移的阈值。动机:上一行点名的三个候选确实是同类风险——实测在 1<<20 step budget 内、且**完全没有触发预算**的情况下,三者各自的紧循环分别跑 **21 秒 / 20 秒 / 53 秒**,单次 `prog.Run` 就已超过 Go fuzz 的 10 秒 per-input 看门狗,而 `FuzzAutoPromote` 每个输入要跑**四次** Run(concat 风暴家族的一样的机制)。记账后 21/20/53 秒变 **46/90/70 毫秒**且预算正确触发;八种普通写法(含 1 MiB 的 `string.rep`、10000 元素的 `table.concat`)与 lua5.1 逐字节一致——1 步 / 64 字节的比率下 1 MiB 产出约记 16K 步、占 1<<20 预算约 1.5%。**`table.concat` 必须按字节而不是元素个数记账**:它的遍历本来就被表的长度界住,所以按个数看永远便宜——256 个元素听起来微不足道,而每个元素 2 KiB 时实际要 53 秒。这与 §4.9 那一类 hardening 上限(`string.rep` 的 1 GiB、`string.format` 的 width/precision)是**两件事**:那些是「宿主进程不可崩」的 fail-fast 上限,本行是预算记账,一次调用可以既在上限之内又把预算耗尽 | concat 风暴家族的下一批 (#222,commit `7119391`) | `7119391` |

## P1 总验收结果(roadmap §4 / 12 §10)

- **三档 ≥2x over gopher-lua**:✅ Xeon 6982P-C 实测(IC 完成后):
  simple 275ns vs 874ns = **3.18x**;arith 311ns vs 966ns = **3.10x**;
  loop 15.1µs vs 34.4µs = **2.28x**。分配 5 allocs/op vs gopher 8-124。
- **benchmark-game 真实负载**(benchmarks/realworld,P1 性能轮后):
  fib 1.31x、binary-trees 1.09x、spectral-norm 1.43x、fannkuch 0.82x、
  nbody 1.08x over gopher-lua——五项中四项反超(性能轮前 0.77x-1.17x,
  binary-trees/fannkuch/spectral-norm 曾落后)。剩余短板 fannkuch(表
  索引/交换密集):IC 命中仍付 accessor 间接层,直达偏移方案(DataOff)
  实测因校验复杂度反噬被否决,记 P2 IC 演进输入。五脚本返回值与官方
  lua5.1 逐字节一致(TestRealWorld_OracleParity)。
- **P1 性能轮**(同 commit 区间):closeUpvals maxOpenIdx 快路径
  (binary-trees -30%)、GC pacing 补附属块统计、根扫描免 map 分配、
  callHost 实参池(spectral-norm -36%)、State.Call 跨 Run 复用主 thread
  (simple 275→98ns)、表槽初始化批量化。
- **与官方 Lua 5.1.5 输出逐字节一致**:✅ seed corpus 70 用例 + 随机生成
  500 种子全部 byte-equal。oracle 源码编译供给(`~/.local/bin/lua5.1`)。
- **官方测试套移植**(test/luasuite):lua-5.1-tests 13 文件原样运行,
  vararg/sort/pm 整文件通过,其余 10 个截断到豁免线(setfenv/debug/
  io 对象/setlocale/string.dump/require,均对应豁免注册表),前缀全过;
  stopAt 表强制登记、豁免线只许前移。
- **长稳承诺**:freelist 循环复用(22000 轮分配密集脚本 arena 稳定
  17.4KB);深递归 `stack overflow` 可恢复(LUAI_MAXCALLS=20000 等价);
  pcall 自递归 `C stack overflow`(LUAI_MAXCCALLS=200 等价)先于 Go 栈
  fatal;`-race` 下 Program 跨 16 goroutine 共享验证。
- **`make all` 门禁**:✅ gofmt 空、golangci-lint 0 issues、`go test -race` 全绿;
  三平台交叉编译冒烟(386/windows-amd64/darwin-arm64)进 CI。

## 审查核销轮(外部逐提交审查 → 集中修复)

外部代码审查逐函数对照官方源码,12 轮报告共发现 22+ 项真实问题,
集中修复轮全量核销(每项独立提交,`95b51a3..`)。重点:

- **DoS 级**:constFold 丢弃带跳转链的 eKNum(`(true and 7 or -1)+1` 一行
  Go panic 崩宿主,潜伏自 M8)→ isnumeral 同构 + Program.call recover 兜底;
  SETLIST 批号超 9-bit 截断挂死 → 官方 C=0+裸批号路径;深嵌套/无限循环 →
  parse 深度护栏 + 回边指令预算。
- **静默错果**:lexer 数字非贪心(`return 1or 2` 被接受执行)→ 官方
  read_numeral 贪心重写;`(a)=5` 被接受 → ParenExpr 全包;table.remove
  越界删末元素;math.max 首参吞错;Fb2Int 缺 &31 掩码。
- **内存/资源**:arena 尺寸入口 uint32 回绕(4GiB 请求"成功"切 8 字节)→
  uint64 域检查 fail-fast;对象尺寸公式四处手写 → object.SizeOf 单源;
  hostFn 注册表无界增长(gmatch/mountArena)→ 引用计数槽回收。
- **官方测试套驱动**(test/luasuite 移植扫出):break/repeat 漏发 CLOSE
  (闭包捕获循环变量后 break 读脏值)、return 短路链快路径挂死、pattern
  %z/未闭合捕获 panic/%q 格式、gsub/sort 走元方法、gmatch 空匹配推进、
  near 原文(txtToken)、luaO_chunkid 同构、错误措辞 luaL_checknumber
  格式、5.0 兼容别名(math.mod/foreach/gfind)。

## 与设计文档的对账(实现形式差异,均为接口等价)

| 设计点 | 设计文档形式 | 实现形式 | 对账结论 |
|---|---|---|---|
| 值栈/CallInfo 位置 | 住 arena(05 §1.2),Thread 对象 word 字段 | Go slice(crescent.thread struct) | **接口等价、P3 迁移点已留**:backing 注入点(`arena.Options.NewBacking`,06 §1.1 唯一硬性前瞻义务)已就位;协程"状态冻结"语义已可工作(yield 保留 CallInfo 链)。物理搬迁是 P3 wazero memory 收养时的工作,届时 stack/cis 切 arena 视图不动 opcode 语义 |
| per-item API 栈机风格 | `PushNumber/ToNumber/Top/Pop/GetGlobalFn/CallFn` 等(11 §7.1 草图,gopher-lua 栈机) | `State.SetGlobal/GetGlobal/Call(fn,args...)` + `Register/RegisterModule`(列表风格) | **形式裁剪、能力等价**:pineapple 一类「fn 一次取出 + 循环 per-item Call」用法由 GetGlobal+Call 覆盖;Push/Pop 栈机风格未做(若未来 gopher-lua 迁移负载明确需要再补)。HostFn 收 args 中 table/function/userdata 仍映射 Nil(本期 fromInner 收紧)、host closure 从 Go 端直接 Call 仍未开 |
| host closure 从 Go 端 Call | 任意 closure 一视同仁可被 `state.Call` 调起(11 §1.5) | internal `State.Call` 见 host closure 直接报错(`call.go:hostCheck`) | **裁口、不影响主线**:`Register` 注册的 host fn 仍由 Lua 内调用闭环工作;Go 端「state.Call(hostFn,…)」用法未开,等真有需求时补 callHost 入口的脚手架(临时栈帧) |
| 开放 upvalue 链 | 按 stackIdx 降序单链(05 §8.3) | Go map(stackIdx → uvRef)+ uvOwner(uv → thread) | 共享语义等价(同槽同 uv);降序链是值栈 arena 化的配套,一并留 P3 |
| executeSignal 三态 | sigReturn/sigYield/sigError 枚举(08 §3.3) | 显式 *LuaError 返回 + errYieldSentinel 哨兵 | 同一冒泡通道,哨兵区分;08 §3.4 "yield↔error 对称"的最小实现 |
| 协程对象 | Thread 对象住 arena(01 §5.6) | lightuserdata 句柄 + Go 注册表 | type() 返回 "thread" 语义一致;арena Thread 对象随值栈 arena 化一并做 |
| xpcall handler 时机 | 栈展开前调用(09) | 捕获后调用(栈已回滚) | **已知微差**:P1 不支持 handler 内 inspect 出错栈帧;traceback 仍可经 Traceback() 取 |
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
  四个 issue 报四处,实际修了六处,另有一处是本分支自己 45 秒 fuzz 冒烟撞出的。

  | 根因 | 落点 | 修法要点 |
  |---|---|---|
  | C99 `nan(n-char-sequence)` 未被消费(#192) | `internal/crescent/number.go` | 只在 `)` 真的存在时才消费整组;未闭合的 `nan(` 保留裸词照旧被拒;`inf(...)` 无此形式仍拒 |
  | `tonumber(x, 10)` 整条路由错(**无 issue,扫描发现**) | `internal/stdlib/stdlib.go` | PUC 在校验范围之前、读 arg 1 之前就判 `base == 10` 并走标准转换。一个原因造出六条分歧(`"1.5"`/`"0x10"`/`"1e3"`/`"inf"`/`"nan"`/number 实参),五条没有任何 issue 记录;现在无 base 与 base 10 共用一个 helper |
  | `%d`/`%i` 精度 0 配值 0 丢符号(#196) | `internal/stdlib/stringlib.go` | C 转换出零个数字但仍输出 `+`/空格 flag 的符号,Go 连符号一起丢。新增 `cSignedFormat` 只在这个角落手写,与隔壁 `cUnsignedFormat` 同一手法(10 §5.2.1b) |
  | `table.insert` 的 5.1 语义本来没有边界检查(#194) | `internal/stdlib/tablelib.go` | `position out of bounds` 属 5.2+;5.1 的 `tinsert` 无检查,`e = #t+1`、`pos > e` 时抬 e(10 §7.2 真值表) |
  | `table.concat` 错误文本(#194) | `internal/stdlib/tablelib.go` | PUC 的 `addfield` 是 `"invalid value (%s) at index %d ..."`,`%s` 是元素的 `luaL_typename`;原先括号括错范围且丢了类型名 |
  | `string.char` 的两步转换(#193) | `internal/stdlib/stringlib.go` + `internal/oracle/prelude.go` | `luaL_checkint` 是 `(int)luaL_checkinteger`;这是 C UB 且跨 arch 不一致,产品侧钉 x86-64、差分侧跳该区间(12 §4.9b,10 §5.4b) |
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
  `tostring(nil)`、`io.write` 的副作用落到 stdout)。过程反思见
  `llmdoc/memory/reflections/2026-07-28-issue197-199-stdlib-semantics.md`。

- **nightly crasher + 预存缺口对账(2026-07-28,#201/#202/#203 一轮)**:三个 issue 性质不同——#201 与 #203
  是 nightly 自动开的 go-fuzz crasher,#202 是上一轮修 #197-#199 时**顺手记下的一组**预存缺口。

  | 项 | 落点 | 结论与修法要点 |
  |---|---|---|
  | `unpack` 的上限是 `LUAI_MAXCSTACK` **减参数个数**(#201) | `internal/stdlib/tablelib.go` | 原先按固定 8000 比较,接受了 PUC 拒绝的 7998..8000 那一段。PUC 的 `luaB_unpack` 调 `lua_checkstack(L, n)`,拒绝条件含 `(L->top - L->base + size) > LUAI_MAXCSTACK`,对 C 函数 `L->top - L->base` 就是参数个数。**结论是量出来的**:阈值随参数个数变化正是排除「硬编码 7997」的证据(10 §4.5) |
  | `error` 自己是被调 host 函数时位置前缀完全丢失(#202) | `internal/crescent/meta.go` + `errors.go` + `state.go` | host 抛错经 `callHost` 直接返回、不进解释器循环,`annotateError` 从来看不到它(#197 那套按帧计数在这条路径上一次都没被调用)。改在边界标注,另外两件要对:**level 1 是 host raiser 的调用者**(不占 Lua 帧,所以 level 1 该裸、level 2 该带前缀;第一版从 `Level-1` 消费使每个 level 偏一格)+ **只对显式 `level >= 2` 生效**(PUC 的库错误经 pcall 抛出时不带位置,`TestTableConcat_ErrorTextMatchesPUC` 立刻抓到)(09 §3.2.2) |
  | `os.time` 忽略 `isdst` 字段(#202) | `internal/stdlib/tablelib.go` | PUC 把它填进 `struct tm` 的 `tm_isdst` 交给 `mktime`,用来确定 DST 相关本地时间的解释;只在与该日期在该时区的自然状态**不一致**时才有影响。要区分「字段不存在」与「显式 false」(新增 `getBoolField` 返回 `(value, present)`)。`TZ=Europe/London` 下双向实测(10 §9.1.1) |
  | 移位区间的 skip 与产品上限拆成两个数(#203) | `internal/oracle/prelude.go` | 同一写法的**第三个** nightly crasher(约 100M 的移位跨度、刚好在 2^27 之下,三条都正确且对称、只是耗数秒,而 coordinator 并行重放整个 corpus)。第三次说明该改的是被接受的区间,不是再挪一个 seed:产品侧留 2^27(**正确性**——lua5.1 会做这个移位),harness skip 降到 2^20(**资源**——什么输入能待在并行重放里)(12 §4.9d) |
  | **不成立的三项**(#202) | — | `coroutine.wrap` 缺前缀 / `%#g` 指数交界 / `math.deg` 差 1 ulp,**实测都与 `lua5.1` 一致**。那个 issue 是自己开的,七项里三项不成立 |
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
  | 三个标准流做成真 file-handle userdata(#205) | `internal/crescent/alloc.go` + `internal/stdlib/tablelib.go` | 上一轮撤回的**根因只有一行**:原型直接调 `object.AllocUserdata`、**跳过了 collector 的 `LinkSweep`**,对象 header 里既没有颜色也没有 sweep 链,收集器根本看不见它;创建句柄后一次 `collectgarbage("collect")` 就以 arena 索引越界 panic 而句柄仍从 `io` 表可达。`alloc.go` 里每个分配器都是 `AllocX` + `LinkSweep` + `AllocCharge` **三件一起**,现在 `State.NewUserdata` 把它固定成唯一入口(06 §2.1.1,10 §10.2.1)。必须是真 userdata 而不是表,因为 PUC 报的是 `userdata`、`type()` 会露馅 |
  | 两处 VM 缺口 + `getmetatable`(#205) | `internal/crescent/meta.go` + `internal/stdlib/stdlib.go` | `metaFieldOfValue` 不认 userdata(所以 userdata 的 `__index` 从来没被查过)、`indexWithMeta` 压根没有 userdata 分支(userdata 没有裸字段,索引直接走 `__index`,无 metatable 时报 `attempt to index a userdata value`);`getmetatable` 对 userdata 原先无条件返回 nil,现在还会遵守 `__metatable`(07 §1.3) |
  | `io.read` / `io.lines` / file 方法(#205) | `internal/stdlib/tablelib.go` | `readFormats` 一份实现同时供 `io.read` 与 `file:read`;`stdinReader` 是**一个共享的** `bufio.Reader`(标准输入的位置是全局状态,两个 reader 各自持缓冲预读会静默丢字节);`"*a"` 在 EOF 返回 `""` 而不是 nil。`io.lines(filename)` **抬错**而不是静默返回空迭代器(10 §10.3) |
  | `debug` 表注册 traceback + getinfo(#205) | `internal/stdlib/tablelib.go` + `internal/crescent/errors.go` | `getinfo` 只填 P1 能诚实回答的字段(`currentline`/`source`/`short_src`/`what`/`func`),`nups`/`activelines`/`namewhat` **宁缺不假造**;`FrameInfo` 有一个 off-by-one——`getinfo` 是 host 函数、host 帧不进 `cis`,所以 level 1 是最内层 cis 帧,直接减 level 会让最常见的 `getinfo(1)` 返回 nil(09 §13.4) |
  | `string.byte` 缺 `lua_checkstack` 上限,而且消息被包了一层(#206) | `internal/stdlib/stringlib.go` | 原先**完全没有上限**,`string.byte(string.rep("a",9000),1,8000)` 返回 8000 个值而 PUC 抬错;上限与 `unpack` 一样是 `8000 - nargs`,但 `luaL_checkstack` 把调用者的文本套成 `stack overflow (%s)`,所以 PUC 输出的是 `stack overflow (string slice too long)`。第一版上限对了、文本照抄了裸串,65 个 oracle 用例里仍有 12 个分歧(10 §5.4c) |
  | 捕获累加器接上 file-handle 的 `:write` | `internal/oracle/prelude.go` | 加了 `io.stdout` 之后脚本可以经一条 harness **没有捕获**的路径输出,那段文本在**两侧**捕获里都不存在——两侧仍然一致、不报分歧,而比较已经不覆盖这条路径写出的任何东西。**这是「因为错误的原因而变绿」**(12 §3.1.1) |
  | **#208 一行代码没改** | — | 它的 fuzz run 跑在 `cbd0512` 上,**早于**上一轮把 harness skip 降到 2^20 的 `c07ba58`;现在这个 seed 0.00 秒就跳过,作为回归防线留在 corpus 里。上一轮「该改的是被接受的区间,不是再挪一个 seed」的正向结算 |

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
  | `TestInsertShiftThresholdsStayDistinct` | 同上 | **按行为断言而不是比对字面量**:两个常数一个在 `internal/stdlib` 一个在 `internal/oracle/prelude.go`、都不导出也不同包,写死 `1<<20`/`1<<27` 只会与任一侧各自漂移。断的是区间里一个输入的行为——跨度约 2M 落在两个阈值**之间**,①必须由产品执行(`Run` 不返错)②必须便宜(耗时上界)。**两个方向都要断**:只断①时合到 2^27 仍然全绿,只断②时合到 2^20 也全绿 |
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
  | 调用的行号取了被调用表达式那一行(#214) | `internal/frontend/parse/expr.go` | `ast.CallExpr{Line: e.Pos()}` 用被调用表达式的起始行,而 PUC 记的是**参数列表**开始那一行:`(0\n)()` lua5.1 报第 2 行、望舒报第 1 行。改成在 `parseArgs` 之前取 `p.tok.Line`。**只有跨行的被调用表达式才有差别**,单行时两者相同,所以只能靠 fuzz 撞出来;第一版把 `Line` 整个挪到参数列表那一行,而它同时喂 callee 物化 / 参数物化 / CALL 三个点,于是 callee 的 GETTABLE 也被挪走(`t.x\n{1}` 把索引 nil 报到第 4 行)——审计发现。改成两个行号:`Line` 给物化、`ArgsLine` 只给 CALL。`MethodCallExpr` **同样有这个问题且早于本分支**(SELF 与 CALL 共用一行,`t:nope\n{}` 报第 2 行而非第 3 行),一并修好(09 §3.5.1) |
  | `string.gsub` 的 repl 类型是惰性校验的(#216) | `internal/stdlib/stringlib.go` | 不是少了一个检查,是**检查放错了位置**:类型判断写在替换循环**里面**,而第 4 个参数把循环次数压成 0,于是循环一次都没跑、非法参数从来没被看到,`gsub("", "", nil, .0)` 成功返回而 lua5.1 抬 `bad argument #3`。PUC 是在循环**之前**用 `luaL_argcheck(tr)` 校验的。**是第 4 个参数让这条路径可达的**(10 §6.5.1) |
  | `math.mod` 不是产品缺陷,是 harness 的**别名漏网**(#217/#219) | `internal/oracle/prelude.go` | `mathFn2` 报第一个缺失参数是**刻意决定**:两个官方构建互相不一致(x86-64 报 #2、arm64 报 #1,C 不规定实参求值顺序),没有可对齐的对象。但 `__wrapArgOrder` 只包了 `math.fmod`,没包它的 `LUA_COMPAT_MOD` 别名 `math.mod`(同一个 C 函数),于是同一写法被开成**两个** issue;`math.atan2` 也从来没被包过。现在按「math 表里所有取两个数的入口」全部包上(12 §4.9e,10 §8.6) |
  | #218 不是缺陷,是**重 workload 进了 corpus** | `test/regression/p4_hot_loop_promote_test.go` | 一亿次迭代、既不崩也不分歧,只是 p4 corpus 里最重的一个 seed(1.8 秒,其余都在 0.3 秒以内,约 6 倍),而 coordinator 并行重放整个 corpus。按 triage guide 走显式回归测试。**第一版做错了**:只抄了脚本、没抄 harness 的 `SetStepBudget(1 << 20)`,跑到循环结束耗 **87 秒**(harness 的 50 倍)——那 1.8 秒不是一亿次迭代的代价,是一百万步预算的代价。镜像 budget 与 arena cap 之后是 1.82 秒。按 guide 规定没有自建 in-test deadline(失败模式是「永不返回」,交给包级 `go test -timeout`) |

  **验证规模**:全套测试 + `-race`、`test/`、p3 / p4 两个 build tag 全部 0 失败;oracle 单测与 corpus 全量
  重放全绿;45 秒引导式 fuzz 干净;**五个 seed 全部从 FAIL 变 PASS**。过程反思见
  `llmdoc/memory/reflections/2026-08-02-issue212-219-fuzz-crasher-batch.md`(四条教训:版本核对的三档是
  成本档不是缺陷档 / 豁免判据必须覆盖别名 / 参照实现在循环之前做的校验不能挪进循环 / 把 seed 转成回归
  测试时要连 harness 的限制一起抄)。

- **concat 风暴家族点名的三个候选算子结算(2026-08-03,#221 / #222 一轮)**:#221 **过期**——seed 是
  `print(pcall(math.mod))`,正是上一轮给 `__wrapArgOrder` 补上 `math.mod` 别名之后修掉的那个,它的 run
  跑在 `947fbda` 上、早于 `ac21b91`;但仍按上一轮的教训在当前 HEAD 上实际重放确认 PASS,一行代码没改。
  #222 是真的,而且是这个家族(#123–#167)的下一批:seed 是一个 777777776 次迭代的拼接循环,target
  `FuzzAutoPromote`,run 跑在上一轮的合并提交 `093f7d1` 上。

  | 项 | 落点 | 结论与要点 |
  |---|---|---|
  | 死因不是「seed 太重」也不是内存 | — | 两条都被实测否掉:seed 本地重放只要 **0.77 秒**、在自己 corpus 里只是**第三重**的(1.22s / 1.21s / 0.77s),两个更重的早该先死;单 seed 峰值 RSS 只有 **106 MB**,而 CI 用的是 `GOMEMLIMIT=512MiB`(纯软限制),整个 corpus 并行重放峰值 525 MB。**定性靠的是读 `llmdoc/guides/unreproducible-crasher-triage.md` 里这个家族自己的结论**:#166 那轮已经查清死因是 **CPU wall-clock 撞 Go fuzz 的 10 秒 per-input 看门狗**、不是内存,而那一节还把剩下的候选按名字列了出来 |
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
  | 真正的问题是 harness 的**余量** | `internal/fuzzbudget` / `fuzz_auto_test.go` / `fuzz_p4_test.go` | `1<<20` 在 1 步 / 64 字节下允许约 **64 MiB** 的 concat,本地每个 fuzz 子测试 **0.7–1.3 秒**;而 **CI 运行器比本地慢约 10 倍**——这个倍率 `internal/crescent/state.go` 的 `chargeBulkWork` 注释里早就写着(`>>6` 那个比率本身就是按最慢的 CI runner 收紧出来的)。最慢的家族 seed 投射到 CI 是 **12–13 秒**对 **10 秒**看门狗:六个 seed **两个已经超过**、四个余量不到 **1.4 倍**。机制是日志印证的而非推断:nightly 日志里就是 `panic: deadlocked` |
  | 修法:四处 `SetStepBudget` 共用一个常量,终值 `1<<16` | `internal/fuzzbudget.Steps` | build tag 与消费方一致(`(wangshu_p3 || wangshu_p4) && wangshu_profile`)——第一版没加 tag 被 golangci-lint 判 unused,因为消费方都在 tag 之后、默认构建看不见。corpus 全量重放 5.5 秒 → **0.85 秒** |
  | **第一版的 `1<<19` 被审计推翻:上限要按「计费相同时最贵的写法」定** | `internal/fuzzbudget.Steps` | 减半到 `1<<19` 只修好了 corpus 里那六个 seed(它们确实都拿到 ≥1.8 倍余量),而**邻域仍在看门狗之上**——按四次 Run × 慢 10 倍投射,`1<<19` 下 `out=out.."x"` 循环 **14 秒**(余量 0.70 倍)、`t[tostring(i)]=i` 循环 **18 秒**(0.56 倍)、`s:gsub("%a","x")` 循环 **41 秒**(0.24 倍,是看门狗的四倍、比被修的 seed 还糟)。根因:`gsub` 每次调用按大约两倍主串计费,实际工作远多于等额计费的 concat——**等额计费不等于等额 wall-clock**。`1<<16` 让最坏那条降到 **5.2 秒**、余量 **1.93 倍**,是第一个满足数量级判据的值 |
  | 覆盖不损失这次是**实测**的,不是断言 | — | harness 自己的 seed corpus 在 `1<<20` / `1<<19` / `1<<16` 三个预算下 `PromotionCount` **完全相同**——那些写法在几次调用之后就升层,从不接近任何一个上限。**注入一个真实的 P1-vs-P4 分歧**(把升层侧的返回值截断)确认 `1<<16` 下 harness 仍然 FAIL、撤掉注入后通过;90 秒引导式 fuzz 干净 |
  | 据实记下变窄的一处(收窄而不是空洞) | `test/regression/issue144_regression_test.go` | `1<<20` 时 corpus 里有两个 seed 会把 arena 推到上限,`1<<16` 时没有,所以那条 arena-cap 错误分支在这里覆盖到的写法变少了。issue144 的回归直接覆盖 arena cap;而且实测两个预算下 step budget 都**先于** arena cap 触发,所以这条路径本来就不是靠 step budget 到达的 |
  | 回归测试原先并不防它声称防的东西 | `test/regression/issue224_watchdog_margin_test.go` | 它在 harness 自己的预算设定下量代价(不是量 corpus 的耗时),但审计实测把预算调回 `1<<20`(正是那个回归)时它**照旧通过**:上界写了 1 秒而它自己的注释里推导出的是 **250 毫秒**(`10 秒 / 10 倍 / 4 次 Run`),而且它拿「四次 Run 的投射」比「一次 Run 的测量」;只用被开成 issue 的那两个 seed 也分辨不出 `1<<16` 与 `1<<19`。现在上界改成推导出的 250 毫秒、用例表加进上面那三个真正约束预算的写法,能同时抓住 `1<<19` 与 `1<<20`,**已用变异实测确认** |

  **验证规模**:两个 seed 入 `testdata/fuzz/FuzzAutoPromote/6ed94d7f9fe6248a` 与 `841bbefecf338b0d`;
  五种构建组合 vet 干净;3 个 commit(`4c89799` 第一版减半、`f844bf1` 文档、`d3928f5` 审计之后改按最贵
  写法定值)。判据落点 [12](./12-testing-difftest.md) §4.9a2(一个预算「界住」还不够,它允许的量必须与
  外部看门狗差一个数量级;「可达的最坏」要按同一计费额度下最贵的写法量)与
  [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §3.4(P4 fuzz
  harness 的 step budget 设定)。过程反思见
  `llmdoc/memory/reflections/2026-08-04-issue224-225-watchdog-margin.md`(六条教训:版本核对之后还要把
  seed 拿到那个旧 commit 上跑一遍以验证归因 / 一个上限只要界住还不够,它允许的量必须与外部看门狗差一个
  数量级 / **上限要由「计费相同时最贵的写法」定,不能由「恰好被开成 issue 的那个写法」定** /
  **一个「防住某个数值」的回归测试必须用变异实测确认它真的会因为那个数值变化而变红** / 降低 fuzz 预算
  不等于降低覆盖,但要用注入缺陷加白盒计数器证明 / 常量要与它的消费方共享 build tag)。

- **两个真缺陷、互不相关(2026-08-04,#228 / #229 一轮,`a11334b`)**:两个 nightly crasher 分别来自
  p1 腿的 `FuzzOracleDiff`(#228)与 p4 腿的 `FuzzP4ForceAllPromote`(#229),run 都在 `efa9aa6` 上,
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
  | 脚本**测量**地址的长度,归一化到不了(#233) | `internal/oracle/prelude.go` | `t={0}print(#tostring(t))`:PUC 21(`%p` 在本平台给 12 位)、望舒 17(`0x%08x` 给 8 位),**长度在归一化之前就分歧**,`NormalizeOutput` 拿到的字节流里只有 `21` 和 `17`——与 `string.len(0/0)` 4 对 3 完全一样的机制。修在**渲染处**:prelude 包一层 `tostring` 把 PUC 自己的地址渲染成望舒的 8 位,与 NaN 符号位是同一个选择;望舒侧的宽度成为契约。**归一化管值、渲染处管宽度**(12 §4.3a) |
  | gsub 替换串里的 `%` 转义(#234) | `internal/stdlib/stringlib.go::st2gsubRepl` | PUC `add_s` 的 `%` 分支有三个出口加一个边界情况,望舒只对了两个:① **末尾的裸 `%`** 让它 `i++` 然后越过长度读 `news[i]`,读到 `lua_tolstring` 保证的 NUL、每次匹配吐一个 NUL 字节(`gsub("aaa","a","x%")` 是 `x\0x\0x\0`,hexdump 实测),望舒要求 `i+1 < len(rb)` 把它当成了字面量;② `%` 后**任何非数字**原样吐出,`gsub("a","a","%z")` 是 `"z"` 而不是报错——**这一半是既有缺陷**,在 base 上同样分歧、只是没有 issue 记它(10 §6.5.2) |
  | 「照抄一个越界读」的判据 | — | 这不是 C 的 UB(`lua_tolstring` 保证那里有 NUL,读它有定义)也不是参照实现自相矛盾,而是**有定义但可疑**;决定照抄的是**口径**——oracle 逐字节比较,不照抄的话凡是以 `%` 结尾的替换串就永远不可比,而且那个差的字节还会流进长度、比较、表键。注释里写清了这是怪癖不是规则 |

  **#233 为什么看起来是间歇的**:#232 那种「原始形式」是否分歧,取决于真实地址恰好需要几位十六进制
  (正好 8 位时两侧碰巧一样长);而 #233 那种「测量长度」是稳定分歧的。**同一个根因、两种可见度**——
  分组时不要按症状把它们分成两件事。

  **验证规模**:三处修复各自用「把该修复还原掉」实测确认会变红——
  `internal/oracle/normalize_addr_test.go::TestNormalizeAddrPrefixValidation`(六条必须归一 + 四条必须
  原样,**两侧都要有**:只写前半时「把锚点整个删掉」也会通过)、
  `fuzz_234_test.go::TestGsubReplacementEscapeMatchesPUC`(四条改了行为 + 三条必须不变:`%%`、`%1`、越界
  下标仍抬错)、`fuzz_234_test.go::TestAddressLengthIsComparable`(望舒侧的宽度契约:`#tostring({})` 是
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
  | 真根因 | `.github/workflows/nightly-diff-fuzz.yml` 等四处 | 裸的 `curl -sLO https://www.lua.org/ftp/lua-5.1.5.tar.gz`,**没有 `--max-time`、没有 `--connect-timeout`、没有 retry**,上游一次可达性抖动就挂到 shell 放弃 |
  | **后果比「一次红」更糟** | 同上 | 这一步失败让后面三个差分 fuzz 步骤被 **skip**(step 5/7/9 是 `skipped`),那一轮报 failure 而**实际什么都没测**、探索预算为零——而红色的默认含义是「跑了并且发现了问题」,两者在 Actions 页面上是同一个红叉(12 §8.1) |
  | 一次抖动开了六个 issue | 同上,triage 段 | infra issue 标题嵌了 `${{ matrix.variant }}`,而 **infra 失败天然横跨所有 tier**(divergence 失败才天然属于某个 tier),p1/p3/p4 三个标题让按标题去重看不出它们是同一件事:**两次抖动 × 三个 tier = 六个**。改成按 `${{ github.run_id }}`(三个 job 共享)去重,第二三个 job 改为评论,tier 挪进正文——**去重键与信息量是两件事**([engineering](../engineering.md) §3.2) |
  | 取包收口 | `scripts/fetch-lua-tarball.sh`(新增) | 四处 call site(`ci.yml` ×2、`bench-acceptance.yml` ×1、`nightly-diff-fuzz.yml` ×1)统一改用它。四个性质:限时 / 重试(`--retry` 才是关键 —— 它的默认值是 0,所以旧的裸 curl 根本不重试。`--retry-all-errors` 只是额外放宽,**不是**超时重试的前提:curl 手册写的是「transient error means **either: a timeout**, an FTP 4xx ... 」,所以单靠 `--retry` 就能覆盖 #236–#241 那次失败。此前把它写成前提是错的,记在这里因为那曾是保留这个 flag 的唯一理由。|
  | 自测本身不能是新的抖动源 | `scripts/test-fetch-lua-tarball.sh`(新增) | 挂进 `make test-scripts`(#179 定下的门禁纪律),五个用例**全部离线**(用 `file://` origin 冒充上游)——一个「防住外部抖动」的测试如果自己依赖上游,它加的是噪声不是防线。每个用例都用变异实测过 |
  | 那个自测第一版假绿 | 同上 | 「curl 调用带限时标志」这一条**第一版 grep 整个文件**:把 `--max-time` 从调用里删掉后照旧通过,因为那个词在脚本顶部的注释里还在(注释正好在解释「bounded: `--connect-timeout` and `--max-time`」)。现在只截取那条 curl 调用再检查。判据:**检查代码属性的测试要作用在代码本身上,一个注释就能满足的测试没有在测代码** |

  **已知缺口(如实记)**:取包这一环收口了,去重也修好了,但**「本轮未执行任何差分」这句话仍然没有
  出现在任何地方** —— infra issue 的 body 说的是失败原因的类别,不是「这一轮的探索预算为零」。
  记入 [engineering](../engineering.md) §7 文档缺口。

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
  | 望舒侧的行为契约 | `fuzz_244_test.go::TestUnpackAtInt32BoundaryDoesNotCrash` | 边界两侧 + `INT_MAX` 起点 + 小负起点 + 显式区间 + 整表 unpack:该抬干净错的抬、该给正确答案的给,从不崩(10 §4.5) |
  | **守卫经六轮审计收口:区间读 `i` 与 `e`、窄化走 `__ckint0`、助手在脚本前捕获、非表首参不跳;用例见 `internal/oracle/unpack_guard_test.go`(13 skip / 12 compare,两向变异确认)。**补上 `e` 后规则完全可推导:**`i32 <= e32 且 (e32 - i32 + 1) > INT_MAX`**,10 组「先预测再实测」全部吻合。**太窄**:`A(unpack({1,2,3},-2147483646))` 经真实 `FuzzOracleDiff` 实测让**整个测试二进制 SIGSEGV**(家族还活着);**太宽**:`unpack({1,2,3},4294967297)` 窄化成 `i32 = 1`、两侧都返回 3,却拿到 sentinel 被 **SKIP**。口径见 12 **§4.9f**,待修 |

  **2026-08-11 那一轮本身是文档轮 + 复核轮,没有改任何代码或测试**;上面「已知缺口」那一行是复核的产出。
  判据落点 [12](./12-testing-difftest.md) §4.9b 第三格(UB 且参照实现直接崩)与 §4.9f(一条 skip 的
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
  | **问题一:作用域错**(头条 A) | 装 oracle 步骤 | #236–#241 那轮给取包脚本的 curl 加了限时,但同一步里 `apt-get`/`make`/`check-oracle.sh` 三条命令仍然无界——**只框住了当时报错的那一条命令,不是那一整步**。08-18 那轮 p1 腿在这一步卡了 **5 小时 50 分**被 job 超时掐掉,rolling-seed 与 auto-mode 被 skip、两个 go-fuzz 步骤根本没启动。普查后发现无界的步骤是**三个不是一个**(装 oracle、upload logs、triage) |
  | 修法 | 三个步骤各加 step 级 `timeout-minutes`(12/15/10) | 选 step 级而不是逐条包 timeout,是因为它对**以后新加的命令同样生效**——这正是这次栽的地方 |
  | **问题二:量错了成本中心**(头条 B) | native go-fuzz 步骤 | 连续五轮建议把 auto-mode 从 150m 压到 90m,**实测这个建议省不下任何时间**——auto-mode 只跑 2 分钟,150m 是从没接近过的挂死上限。真正的成本中心是 native go-fuzz 步骤,**312 分钟的 job 里它占 270 分钟(87%)**,而这一步从没被量过 |
  | 270 分钟的原因 | `scripts/go-fuzz.sh` + workflow input description | `go-fuzz.sh` 按**源码扫描**发现目标、每个目标跑满 fuzztime;无 tag 可见目标是 **6 个**不是 description 里写着的「4 targets」——root 包 `FuzzCompileRun`/`FuzzAutoPromote`/`FuzzP4ForceAllPromote` + `internal/frontend/lex`/`internal/frontend/parse`/`internal/stdlib` 各一个(`FuzzOracleDiff` 被 `wangshu_oracle_cgo` gate、在自己的步骤里跑,不算在内)。`6 × 45m = 270(审计实测补正:各层实际跑到的目标数并不相同 —— p1 4 个、p3 5 个、p4 6 个 target-run,所以 45m 下三条腿分别约 180 / 225 / 270 分钟,p4 恰好 6 × 45;35m 之后约 140 / 175 / 210 分钟。另外 `go-fuzz.sh` 对 golang/go#75804 的假性 deadline 会把该目标重跑一次,于是最坏情形要再加一个 fuzztime —— 审计抓到一次 45m 下真实跑到 315 分钟的 run,所以 go-fuzz 那一步的 step 超时按「最坏腿 + 一次重试 + 余量」取 280 分钟,而不是按中位数取。)`,与实测吻合——这个过时的「4」正是这一步成本被长期低估的原因 |

审计第二轮补正两点:(1)#75804 的重试是**按目标**而不是按腿的 —— `go-fuzz.sh` 对每个发现的目标各调一次 `run_target`、各有两次尝试,跨目标没有任何上限,所以 p4 那条腿可以有两个不同目标各重试一次,得 210 + 35 + 35 = 280;而先前把 go-fuzz 那步的上限正好取成 280,等于在一次**健康**运行上就把余量吃光 ——「按一次重试取」与「按中位数取」是同一个错误往外挪了一格。现取 **320**。(2)另外三步(rolling-seed、GC-stress、auto-mode)先前只靠内层 `go test -timeout`,而 `-timeout` 只看着 `go test` 那个进程,**以后往这一步新加的命令仍然无界** —— 那正是装 oracle 那步坐了 5 小时 50 分的成因。现在每个带 `run:` 的步骤都有 step 级上限,实测总时长约 215 / 217 / 252 分钟(p1/p3/p4),对 350 的 job 上限余量 98~135 分钟。
  | 修法 | `gofuzztime` 45m→35m | `6 × 35m = 210` 分钟,job 约 250 分钟,余量从约 38 分钟升到约 98 分钟。**真实减少约 22% 的每轮探索量**,选它而不是砍差分步骤是因为差分步骤实际便宜(rolling-seed 36 分钟、auto-mode 2 分钟)而这一步占 87%;description 里的「4 targets」改成「6 untagged targets」 |
  | 自我纠正 ①:抬到 420 不可能 | — | 连提四轮把 job `timeout-minutes` 抬到 420,而 GitHub 托管 runner 单 job 硬上限是 **360 分钟**,推荐了四轮却从没查过这个平台限制 |
  | 自我纠正 ②:求和框架本身是错的 | — | 曾按「所有 step 上限之和必须低于 job 上限」算出某 leg 最坏 548 分钟、判「只能大幅压缩」;**这个框架本身是错的**——job 超时才是总预算,step 上限只是防一条卡死的命令悄悄吃掉这个预算,两者不是同一件事,不该按求和去配 |

  五条判据落 `llmdoc/guides/unreproducible-crasher-triage.md`「给一条报错的命令加超时」与「job 超时
  与 step 超时」两节、`llmdoc/guides/design-claims-vs-codebase-physics.md` §3.1(优化建议先量成本
  分布)与 §5(陈旧计数);过程反思见
  `llmdoc/memory/reflections/2026-08-19-nightly-step-timeouts-and-budget.md`。

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md)

> **补记（同轮第二次审计）**：上面那条「兄弟写法都便宜」的结论只对**单次调用**成立。把同样的工作摊到
> 多次调用上——99 次、每次 2^20-6 个元素——仍然完全参与比对，两侧合计约 11 秒，而指令计数钩子拦不住它
> （每次移位都在一次 C 调用内部完成）。所以 prelude 又加了**累计**预算（`__shiftTotal`，上限 2^22），
> 并由 `internal/oracle` 的 `TestInsertShiftCumulativeBudget` 固定；实测把预算去掉或调得过紧都会让它变红。
