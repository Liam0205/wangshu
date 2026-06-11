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

## P1 总验收结果(roadmap §4 / 12 §10)

- **三档 ≥2x over gopher-lua**:✅ Xeon 6982P-C 实测(IC 落地后):
  simple 275ns vs 874ns = **3.18x**;arith 311ns vs 966ns = **3.10x**;
  loop 15.1µs vs 34.4µs = **2.28x**。分配 5 allocs/op vs gopher 8-124。
- **与官方 Lua 5.1.5 输出逐字节一致**:✅ seed corpus 70 用例 + 随机生成
  200 种子全部 byte-equal。oracle 源码编译供给(`~/.local/bin/lua5.1`)。
- **`make all` 门禁**:✅ gofmt 空、golangci-lint 0 issues、`go test -race` 全绿。

## 与设计文档的对账(实现形态差异,均为接口等价)

| 设计点 | 设计文档形态 | 实现形态 | 对账结论 |
|---|---|---|---|
| 值栈/CallInfo 位置 | 住 arena(05 §1.2),Thread 对象 word 字段 | Go slice(crescent.thread struct) | **接口等价、P3 迁移点已留**:backing 注入点(`arena.Options.NewBacking`,06 §1.1 唯一硬性前瞻义务)已就位;协程"状态冻结"语义已可工作(yield 保留 CallInfo 链)。物理搬迁是 P3 wazero memory 收养时的工作,届时 stack/cis 切 arena 视图不动 opcode 语义 |
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
