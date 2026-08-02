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
| step budget 按字节工作量记账 | 共享 `doConcat`(`internal/crescent/call.go`,fast path + slow-path plain fold)调 `chargeBulkWork(len)`(`internal/crescent/state.go` 新增),按 `len >> 6`(1 步 / 64 字节)把 CONCAT 拷贝 + intern 的字节工作量折算进 step budget,使预算成为**字节工作量的度量**而非仅指令条数。动机:`preempt()` 原本每指令边界只把 stepUsed 加 1,单条 CONCAT 能做与字符串长度成正比的无界工作 ⟹ `for i=1,N do glob=cat(i) end`(cat 内 `return "<~15KB 字面量>"..i`)每次迭代只扣约 2 步却拷贝约 15KB,1<<20 预算允许约 50 万次迭代、单次 prog.Run 约 2.7s wall-clock,4×run 撞 Go fuzz 10s per-input 看门狗(concat 风暴 crasher 家族 #166/#167 根因)。三层(P1 executeLoop / P3 wasm h_concat / P4 native host.Concat)全部路由同一 doConcat,单点记账覆盖所有 backend,差分对称性不破;<64B 记 0 步、1MiB concat 记约 16K 步(约 1.5% 预算),正常程序不受影响。比率 `>>6` 是按最慢的 CI runner(比本地慢约 10×)实测收紧后的值。**已知下一批候选无界单指令算子**:string.rep / string.format / table.concat(尚未按工作量记账) | concat 风暴家族根因 (#166/#167,PR #168) | `88e724f` + `ab27936` |

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

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md)

> **补记（同轮第二次审计）**：上面那条「兄弟写法都便宜」的结论只对**单次调用**成立。把同样的工作摊到
> 多次调用上——99 次、每次 2^20-6 个元素——仍然完全参与比对，两侧合计约 11 秒，而指令计数钩子拦不住它
> （每次移位都在一次 C 调用内部完成）。所以 prelude 又加了**累计**预算（`__shiftTotal`，上限 2^22），
> 并由 `internal/oracle` 的 `TestInsertShiftCumulativeBudget` 固定；实测把预算去掉或调得过紧都会让它变红。
