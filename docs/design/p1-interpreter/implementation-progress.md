# P1 实现进度

> 状态:**P1 全里程碑 M0-M14 + 收尾轮(原"已知简化"清单)已落地**(2026-06-12)。
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
| M14 | 三套测试:conformance + difftest(对拍 5.1.5)+ 三档基准 | `test/`, `benchmarks/baseline/` | 全绿 | `5cc5a6a` |

## 收尾轮(原"已知简化"清单,全部落地)

| 项 | 落地内容 | 设计文档 | 提交 |
|---|---|---|---|
| table 存储 | arena 原生 array+hash(主位置+冲突链、迁位插入、rehash 选最优 asize、border 二分、rawNext 迭代);旁路 Go map 移除 | 01 §5.2 | `1ab4beb` |
| generic for | TFORLOOP 落地 + next/pairs/ipairs | 05 §10.2 | `1ab4beb` |
| IC 命中路径 | icGetTable/icSetTable 五类指令(同表+同代次+同键校验,mono IC,array/node 直达);LoadProgram 改 State 私有浅拷贝(IC/常量不跨 State 串台) | 05 §6 | `c04010e` |
| string pattern | 完整 lstrlib 引擎(字符类/集合/量词/锚点/捕获/%b/%1-%9)+ find/match/gmatch/gsub/format/byte/char;string 库表挂 per-type __index(`("x"):upper()`) | 10 §7 / 07 §1.2 | `c167ad6` |
| stdlib 必做列 | table(insert/remove/concat/sort/getn/unpack)、math 补全(fmod/modf/random/三角/deg/rad + pi/huge)、os(time/clock/date/getenv)、io.write、unpack/xpcall | 10 裁剪表 | `72a09d5` |
| 协程 | 路线 B:yield 哨兵经显式错误通道冒泡(08 §3.4 对称机制),pendingResume 记录恢复点,resume 参数写回 yield CALL 结果寄存器;coroutine.create/resume/yield/status/wrap/running;跨 thread upvalue 经 uvOwner | 08 §3 | `c0f2ba9` |
| 错误目录 | chunkname:line: 位置前缀(error level 语义,level=0 不加)+ traceback(顶层未捕获自动附)| 09 | `473e4dd` |
| 弱表/finalizer | __mode 解析缓存进 GCHeader flags(setmetatable 唯一写入口),GC clear 弱分支激活;SetFinalizerRunner + __gc 创建逆序执行 | 06 §8.4/§10、07 §13 | `7078fcf` |
| arena ABI | wangshu.Arena 四类型列(float64/int64/bool/string)+ presence bitmap + 字符串池去重;Program.Call(state, arena, args);脚本侧 arena.col[i] 零拷贝即时装箱、只读、ColInt64 2^53 护栏 | 11 §3-§5 | `5122ae8` |
| difftest 生成器 | 受控文法随机脚本(类型化局部池),200 确定性种子对拍官方 5.1.5 全部逐字节一致 | 12 §3.2 | `e1ddf2f` |
| per-item drop-in 子集 | `State.SetGlobal/GetGlobal/Call(fn,args...)` + `Register/RegisterModule` + 公共 `HostFn` 类型;`Value` 加 `kFunction` kind(外部不可构造,只能 GetGlobal 取);State pin 表 + GC 根接入(`PinRef/UnpinRef/visitExtraRefs`),globals 覆盖与 freelist 复用下旧 fn Value 仍安全可调;`Value.Release()` 显式释放 pin 槽 | 11 §7.1 / §9.1 (issue #1) | `87031c2` + `cb6e1ae` |
| 公共 Table API | `State.NewTable` + `Value.AsTable` + `Table.Set/SetIndex/Get/GetIndex/Len`;`Value` 加 `kTable` kind(同 kFunction 经 pin 表挂 GC 根);`fromInner` 升级为 `fromInnerWithPin`,Program.Run/Call 与 State.Call 返回路径能携带 table/function 引用;支持嵌套 table 与 mixed-type list 作 Lua 表 round-trip | 11 §4.5 (issue #2) | `2b55e11` |
| 严格沙箱模式 | `Options.HideFileLoaders bool`:从 globals 刮除 `loadfile`/`dofile`/`loadstring`/`load` 四件套(置 Nil);脚本调用 fatal `attempt to call global 'X' (a nil value)`,对位 gopher-lua 嵌入式沙箱传统;与 `AllowFileLoad=true` 同设 NewState panic fail-fast。默认行为不变(PUC 5.1.5 oracle 对拍不退化) | 10 §12.1 LibsSafe 思路最小落地 (issue #3) | `09fdd72` |
| context cancellation 钩子 | `State.SetContext(ctx)` / `RemoveContext`:VM 在 chargeStep 同一抢占点(回边 + 函数进帧 + TFORLOOP)检查 `ctx.Err()`,事件触发(wall-clock timeout / 上游 Cancel)中止 Run/Call 返回 Go error(pcall 可捕获);跨 goroutine 由 atomic.Pointer 保护;chargeStep 三处调用点合一(stepBudget 外层 if 拿掉,内部短路),零额外抢占点 | 11 §10 / 与 SetStepBudget 并存 (issue #4) | `27b4f2e` |
| Table.ForEach 任意 key 迭代 | `func (t *Table) ForEach(fn func(key, val Value) bool) error`:转发 internal `RawNext` 循环(raw 迭代,与 stdlib next/pairs 同源,迭代序确定性);fn 返 false 提前终止;key/val 走 `fromInnerWithPin` 自动登记 pin 槽。issue #2 SetIndex 写入的对称读出能力,完整读写闭环 | 11 §4.5 (issue #5) | `4f855d2` |
| globals baseline 状态隔离 | `State.MarkGlobalsBaseline` 拍当前 _G 字符串 key 快照、`ResetGlobalsToBaseline` 非 baseline key 清空 + baseline key 复原;baseline 复合值经 `visitExtraValues` 入 GC 根(与 pin 表是 GCRef-bearing value 契约级不变式两面:pin 管「公共 API 暴露的长持 GCRef」、baseline 管「内部状态恢复需要的长持 GCRef」);对位 gopher-lua statePool snapshotBaselineValues + resetToBaseline 模式 | 10 §12.1 hardening (issue #6) | `3d34839` |
| CallInto 零分配边界路径 | `State.CallInto(dst []Value, fn, args...) (n int, err)`:返回值写进调用方拥有的 `dst`,标量(bool/number)整条 round-trip 0 alloc。消除旧 `Call` 的双拷贝地板成本(VM 栈→inner slice→public slice,72 B / 2 allocs/call,与脚本复杂度无关)——内部 `callOnStack` 零拷贝切 `th.stack` 活动区(runningThread 复位后 mainTh 仍是常驻根 → GC 下可达),门面层复用 `innerArgsBuf` + 写调用方 dst。`Call` 保留为独立拷贝便捷形(返回值跨下次 Call 仍可读),内部走 callOnStack 后 append 一次。⚠️ 契约:CallInto 返回值底层是复用栈,下次进入 VM 前消费完;string 仍拷 arena 字节、复合值仍经 pin 表 | boundary-dominated 嵌入优化 (issue #8) | `CallInto` |

## P1 总验收结果(roadmap §4 / 12 §10)

- **三档 ≥2x over gopher-lua**:✅ Xeon 6982P-C 实测(IC 落地后):
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

## 与设计文档的对账(实现形态差异,均为接口等价)

| 设计点 | 设计文档形态 | 实现形态 | 对账结论 |
|---|---|---|---|
| 值栈/CallInfo 位置 | 住 arena(05 §1.2),Thread 对象 word 字段 | Go slice(crescent.thread struct) | **接口等价、P3 迁移点已留**:backing 注入点(`arena.Options.NewBacking`,06 §1.1 唯一硬性前瞻义务)已就位;协程"状态冻结"语义已可工作(yield 保留 CallInfo 链)。物理搬迁是 P3 wazero memory 收养时的工作,届时 stack/cis 切 arena 视图不动 opcode 语义 |
| per-item API 栈机风格 | `PushNumber/ToNumber/Top/Pop/GetGlobalFn/CallFn` 等(11 §7.1 草图,gopher-lua 栈机) | `State.SetGlobal/GetGlobal/Call(fn,args...)` + `Register/RegisterModule`(列表风格) | **形态裁剪、能力等价**:pineapple 一类「fn 一次取出 + 循环 per-item Call」用法由 GetGlobal+Call 覆盖;Push/Pop 栈机风格未做(若未来 gopher-lua 迁移负载明确需要再补)。HostFn 收 args 中 table/function/userdata 仍映射 Nil(本期 fromInner 收紧)、host closure 从 Go 端直接 Call 仍未开 |
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

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md)
