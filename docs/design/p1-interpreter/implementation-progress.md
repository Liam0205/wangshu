# P1 实现进度

> 状态:**P1 全里程碑 M0-M14 已落地,总验收通过**(2026-06-12)。下文记录每个完成里程碑的
> 产出、单测覆盖、验收结果,以及 P1 范围内的已知简化(后续推进项)。
>
> 设计文档参考路径见 [00-overview](./00-overview.md);每个里程碑列对应文档 §。

## 已完成里程碑

| M | 内容 | 主要文件 | 单测 | 提交 |
|---|---|---|---|---|
| M0 | 工程地基:go.mod/Makefile/.githooks/ci.yml/.golangci.yml/oracle 校验脚本 | `Makefile`, `.githooks/`, `.github/workflows/ci.yml`, `scripts/check-oracle.sh`, `scripts/go-fuzz.sh` | hook 拦截手测、`make all` 通过 | `eaf944c` |
| M1 | arena 分配器:bump + grow + 双视图 backing + null GCRef 保留 + 注入点 | `internal/arena/arena.go` | 11 用例 | `a34c680` |
| M2 | NaN-boxed Value:8 个 tag、IsNumber/IsCollectable/Truthy、NaN 规范化 | `internal/value/value.go` | 9 用例 | `e209dba` |
| M3 | object 布局:GCHeader 位字段 + 六类对象读写 helper | `internal/object/` | 8 用例 | `0a62aab` |
| M4 | bytecode ISA:38 opcode + 编解码 + Proto + ICSlot + int2fb | `internal/bytecode/` | 9 用例 | `e8f24a0` |
| M5 | mark-sweep GC:STW + 双白 + gray stack + R1..R9 根 + shadow stack + JSHash intern | `internal/gc/` | 9 用例 | `336e98e` |
| M6 | lexer:21 关键字 + 全 token + 数字/字符串/注释 + 四种换行 | `internal/frontend/{token,lex}/` | 11 用例 | `c726e33` |
| M7 | parser:递归下降 + 优先级爬升 + AST(后补 ParenExpr) | `internal/frontend/{ast,parse}/` | 全文法覆盖 | `4ff15cc` |
| M8 | codegen:expdesc/freereg 水位线 + 跳转回填 + 常量去重 + 黄金字节码 | `internal/frontend/compile/` | 9 黄金测试(02 §8 逐字节)| `6f63bce` |
| M9 | 解释器最小循环:大 switch + reentry(Lua 调用不增 Go 栈)+ 算术/循环/调用 | `internal/crescent/` | 7 端到端 | `f6df2ab` |
| M10 | GC 接入:根注入(ExtraValues/ExtraRefs)+ 分配点 safepoint + LinkSweep/计费 | `internal/crescent/alloc.go`, `internal/gc/` | 4 GC 压力 | `10b3e87` |
| M11 | 元表 + pcall:__index/__newindex 链、算术元方法、callLuaFromHost 重入边界 | `internal/crescent/meta.go` | 9 端到端 | `f7812b1` |
| M12 | stdlib + host fn:base/math/string 最小集、HostFn 注册与同步调用 | `internal/stdlib/`, `internal/crescent/host.go` | 9 端到端 | `96a9b7f` |
| M13 | 公共 API:Compile/Program/NewState/Value、vararg 完整化 | `wangshu.go` | 7 公共 API | `0d1c211` |
| M14 | 三套测试:conformance(28)+ difftest(33 对拍 5.1.5)+ 三档基准 | `test/`, `benchmarks/baseline/` | 全绿 | `5cc5a6a` |

## P1 总验收结果(roadmap §4 / 12 §10)

- **三档 ≥2x over gopher-lua**:✅ Xeon 6982P-C 实测 simple 2.28x(402ns vs 915ns)、
  arith 2.40x(421ns vs 1011ns)、loop 2.30x(15.0µs vs 34.5µs)。
- **与官方 Lua 5.1.5 输出逐字节一致**:✅ difftest seed corpus 33 用例(含 NaN/Inf/-0
  格式、Lua mod 语义、字典序比较)。oracle 源码编译供给(`~/.local/bin/lua5.1`)。
- **`make all` 门禁**:✅ gofmt 空、golangci-lint 0 issues、`go test -race ./...` 全绿。

## P1 范围内的已知简化(后续推进,不阻塞 P2)

| 项 | 现状 | 设计文档落点 |
|---|---|---|
| IC 命中路径 | ICSlot 结构/字段就位(Proto.IC),解释器尚未读 IC 做直达槽访问 | 05 §6 |
| table 存储 | 旁路 Go map(GCRef → map[uint64]Value);arena 原生 array/node 段未接 | 01 §5.2 / 05 §6.3 |
| 协程 | 未实现(路线 B:单 goroutine + executeSignal) | 08 |
| string pattern | string.match/find/gsub 未实现(P1 裁剪表的大头) | 10 §7 |
| arena ABI | 列数据接口(ColumnDesc/presence bitmap)未实现 | 11 §3-§5 |
| 值栈位置 | Go slice(非 arena 视图);CallInfo 同 | 05 §1.2/§1.3 |
| 弱表/finalizer | GC 侧 stub 已留,元表 __mode 未接 | 06 §8.4 / 07 §13 |
| xpcall/traceback | pcall 已落地;xpcall handler 与 traceback 格式未实现 | 09 |
| 差分 fuzz 生成器 | seed corpus 固定脚本;随机脚本生成器未接 | 12 §3.2 |

这些简化均为"接口形状已定、内部实现可替换"的形态(如 table 旁路换 arena 哈希不动
调用方),符合 roadmap §5 原则 3「每阶段独立交付价值」。

## 重要实现决策与差分修偏记录

- **字符串字面量惰性 intern**(Proto.StringLits/StringLitIdx):Program 跨 State 共享时
  每 State 私有 intern(11 §1.3 定稿的并发细化)。
- **错误传播**:显式 `*LuaError` 返回贯穿主循环;host→Lua 重入边界(callLuaFromHost)
  负责 CallInfo 回滚(05 §9 定稿)。
- **差分修偏实例**(12 §0 机制起效的证据):rawEqual NaN bits、%.14g 的 inf/nan 措辞、
  and/or 的 VCALL 单值收敛(luaK_posfix 同构)、VARARG 落点回填、ParenExpr 强制单值。
  全部由 conformance/difftest 捕获后当步修复。

## 相关

[00-overview](./00-overview.md) · [../engineering](../engineering.md) ·
[12-testing-difftest](./12-testing-difftest.md)
