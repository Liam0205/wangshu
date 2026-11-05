# Wangshu（望舒）

望舒是纯 Go 实现的高性能可嵌入的 Lua 5.1 虚拟机。它不依赖 cgo，因此保持了交叉编译能力。

关于命名：Lua 是葡萄牙语中「月亮」的意思；望舒是中国神话中为月亮驱车的神灵（「前望舒使先驱」——《楚辞·离骚》）。为月亮驱车，即驱动 Lua 引擎。是一个信达雅的名字。

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Tag](https://img.shields.io/github/v/tag/Liam0205/wangshu?include_prereleases&sort=date&label=release)](https://github.com/Liam0205/wangshu/tags)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Liam0205/wangshu)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**中文** · [English](README.en.md)

## 目标

- 语言标准：实现 Lua 5.1 的核心语言特性。——与 LuaJIT 一致，不追求语言的绝对完整性。
- 正确性：在圈定的语言特性范围，与 Lua 5.1 官方实现的输出逐字节一致。
- 高性能：将 Go 生态的 Lua 执行性能从 gopher-lua 提升至 LuaJ-luajc（Java）甚至 LuaJIT（C++）级别。
- 跨平台：在 Linux/amd64, Linux/arm64, macOS/arm64 测试通过；保留其他平台扩张支持的能力。
- 工业级：望舒从立项开始就是为了公司业务服务的，并且已经在我所在的公司线上运行。从测试、到 CI、再到各类 nightly-fuzz，都是朝着工业级的项目要求去的。望舒从来都不会，也不可能是一个个人的练习项目。我们希望望舒最终成为在 Go 语言项目中嵌入 Lua 的事实标准。

## 快速开始

### 最小示例

```go
import "github.com/Liam0205/wangshu"

prog, err := wangshu.Compile([]byte(`
    local s = 0
    for i = 1, 100 do s = s + i * i end
    return s
`), "demo")
st := wangshu.NewState(wangshu.Options{})
results, err := prog.Run(st)
// results[0].Number() == 338350
```

`Program` 不可变，可跨 State 复用；`State` 每个 goroutine 独立一个。

### 列内核形状：一次跨界，循环全在 VM 内

批量数据处理场景推荐用 arena 列容器：宿主 Go 侧把 `[]float64` / `[]int64` / `[]bool` / `[]string` 挂进 arena，脚本侧看到 `arena.price` 这样的普通表，`price[i]` 直接读到 NaN-boxed 值，无需 per-item 跨界。

```go
ar := wangshu.NewArena(nrows)
ar.AddFloatColumn("price", prices, nil) // present=nil 表示全部 present
ar.AddInt64Column("qty",   qtys,   nil)

prog, _ := wangshu.Compile([]byte(`
    local price, qty = arena.price, arena.qty
    local total = 0
    for i = 1, arena.rows do total = total + price[i] * qty[i] end
    return total
`), "kernel")

results, err := prog.Call(st, ar) // 单次跨界，循环全部在 VM 内
```

### 在四档执行模式之间切换

四档均通过 build tag 选择，源码零改动。默认 build 就是 P1；启用 P3/P4 需要显式带 tag，同 build 里默认走 auto（生产热度阈值 + F1-F7 可编译性检查），用 `SetForceAllPromote(true)` 切到 force（绕开热度阈值，非生产模式，用来跑差分测试与 benchmark）。

```bash
# P1 crescent 解释器（默认 build，永远可用）
go build ./...

# P3 gibbous-wasm 编译层（依赖 wazero）
go build -tags "wangshu_p3 wangshu_profile" ./...

# P4 gibbous-jit method JIT（自管原生码 codegen，amd64 + arm64）
go build -tags "wangshu_p4 wangshu_profile" ./...
```

`wangshu_profile` 是升层前置：不带此 tag 时热度采样禁用，无法进入升层路径。`wangshu_p3` 与 `wangshu_p4` 互斥，一次只能启用一档。

```go
st := wangshu.NewState(wangshu.Options{})

// auto 模式：默认。等待 hot function 自然升层（依 HotEntryThreshold）。
_, _ = prog.Run(st)

// force 模式：**testing-only**，绕过阈值全升。生产不要开。
st.SetForceAllPromote(true)
_, _ = prog.Run(st)

// 观测升层是否真的发生了
n := st.PromotionCount() // >0 表示已经升层
```

`SetForceAllPromote` 只绕过热度阈值，**不**绕过 F1-F7 可编译性检查（协程、顶层 vararg、含 `ReasonUnknownCall`、含 VARARG opcode 的 proto 依然不升层）。升不动的 proto 无声降级回 P1 解释器，输出层间 byte-equal 不变。

### 生产环境的运行期开关与观测

分层执行的生产 admin API（与上面 testing-only 的 force 开关不同）：

```go
// 一键退回解释器：新升层停止，已升层的函数也回 P1 执行；
// 编译产物保留，重新打开即恢复，不需要重新编译。
st.SetTierEnabled(false)
st.SetTierEnabled(true)

// State 级分层执行分布快照
stats := st.TierStatsSnapshot()
// stats.Promoted            已升层 proto 数
// stats.StuckCompileFailed  真编译失败数——非零值得排查
// stats.TierEnabled         开关状态
```

部署要求（P4 的 exec-mmap 环境约束）、灰度建议与 step budget 在分层执行下的语义，详见 [docs/embedding-tiers.md](docs/embedding-tiers.md)。

### 管理与复用 arena

`Options` 提供 arena 容量的初始值 / 上限：

```go
st := wangshu.NewState(wangshu.Options{
    InitialArenaBytes: 64 * 1024,        // 初始 64 KiB
    MaxArenaBytes:     16 * 1024 * 1024, // 上限 16 MiB，超阈 fail-fast
})
```

统计指标：

```go
st.GCCountKB()  // 当前已用 KB（live bytes；随 Collect 回落）
st.ArenaCapKB() // arena backing 容量 KB（不够时翻倍；收集后超过预计用量两倍才缩，wangshu_p3 构建不缩；pool 层据此判 fat state 阈值）
st.PromotionCount() // 已升层 proto 数（testing-only 白盒断言）
```

显式驱动 GC：

```go
st.Collect()           // 强制一次 full GC sweep
st.MaybeCollectNow()   // 依 GC 阈值判是否 collect（非强制）
st.SetHostTriggeredCollect(true) // opt-in：公开 API 返回前检查阈值，越过就 collect（默认关闭）
```

短脚本高频调用场景推荐用 `CallInto` 复用返回值切片，走零分配路径：

```go
dst := make([]wangshu.Value, 0, 4)
for i := 0; i < 1000; i++ {
    n, err := st.CallInto(dst[:], fn, wangshu.String("item"))
    _ = n; _ = err
    // dst 复用，无 per-call 分配
}
```

长寿命 State 场景（规则引擎 hot reload / 数据流转换）搭配 arena 的 `SetHostTriggeredCollect` + `Collect` cadence，可以把 GC 压力压到近乎为零。

## 语言支持

望舒实现 Lua 5.1 的核心语言，语法范围与 LuaJIT 一致：

- **语言**：Lua 5.1 参考手册的全部语法；38 个字节码 opcode 全部实现。其中 `VARARG` 只在 P1 解释器上执行，用到它的函数不会升到 P3/P4。
- **标准库**：base、string、table、math、os、coroutine 全部提供。
- **io**：提供 `io.write` / `io.read` / `io.lines` 和三个标准流 `io.stdout` / `io.stdin` / `io.stderr`。标准流是真正的文件句柄 userdata，支持 `:write` / `:read` / `:lines` / `:flush` / `:close`。需要打开真实文件的部分没有提供，见下表。
- **debug**：提供 `debug.traceback` 和 `debug.getinfo`。`getinfo` 只返回能如实给出的字段（`currentline` / `source` / `short_src` / `what` / `func`），`nups` / `activelines` / `namewhat` 不返回。

### 不提供或与官方不同的部分

| 类别 | 具体项 | 原因 |
| --- | --- | --- |
| Lua 5.2+ 特性 | `rawlen`、`table.pack` / `table.move` | 不属于 Lua 5.1 |
| Lua 5.3+ 特性 | `math.tointeger` / `type` / `maxinteger` / `mininteger` | 整数类型是 5.3 的特性 |
| 嵌入安全 | `os.execute`、`io.popen`、`os.exit` 不真的退出进程、`loadfile` / `dofile` 默认关闭 | 嵌入式 VM 不让脚本执行 shell 或越权读文件系统；`loadfile` / `dofile` 可以用 `Options.AllowFileLoad` 打开 |
| 真实文件 IO | `io.open` / `io.tmpfile` / `f:seek` | 需要文件系统访问控制和关闭文件的 `__gc`，目前不做 |
| debug 接口 | `debug.sethook` / `getlocal` / `setlocal` / `getupvalue` / `setupvalue` / `getregistry` | 需要解释器内部的钩子 |
| 模块系统 | `require` / `module` / `package` | 宿主通过 `Compile` 提供脚本，不从文件系统加载模块 |
| 字节码序列化 | `string.dump` | 望舒的字节码格式与官方 `.luc` 不兼容 |
| 环境操作 | `getfenv` / `setfenv` | 与分层执行的形状分析冲突 |
| 增量 GC | `collectgarbage("step")` / `collectgarbage("setstepmul")` | GC 是 stop-the-world，没有增量参数，调用只返回占位值 |
| 防挂起 | 模式匹配的灾难性回溯（如 `.*.+%A*x`） | 回溯超过 `1<<20` 步时报 `pattern too complex`，官方会一直算下去 |
| C 未定义行为 | `string.char` 收到超出 int 范围的数（含 NaN、±inf） | 官方行为由 C 未定义行为决定，x86-64 与 arm64 结果不同；望舒固定采用 x86-64 的结果 |
| 时区 | `os.time{isdst=...}` 贴着夏令时切换点的少数输入 | 所有平台都按 glibc 的规则解释，所以各平台结果一致；在 macOS / BSD 上可能与本机编译的官方 Lua 不同 |

完整清单和每一条的理由在 `test/difftest/corners_test.go` 的 `exemptions`，可以用 `go test -v -run TestExemptions_Documented ./test/difftest/` 列出。各函数的细节见 [10-stdlib](docs/design/p1-interpreter/10-stdlib.md)。

## 正确性验证

参照物是官方 Lua 5.1.5。验证分六层，每一层比对的对象不同：

| 层 | 比对什么 | 在哪里跑 |
| --- | --- | --- |
| 1. 官方测试套件 | 跑上游 `lua5.1-tests` 里的文件，输出逐字节一致 | 每次 CI |
| 2. 手册逐项用例 | 按参考手册逐节写的特性、边角和错误消息用例，与官方 `lua5.1` 逐字节比对 | 每次 CI |
| 3. 随机脚本差分 | 生成器产出的随机脚本，P1 / P3 / P4 三档分别与官方 `lua5.1` 比对 | nightly，每轮 200 万条 |
| 4. 层间差分 | 同一份字节码在解释器与编译层上跑，输出逐字节一致 | 每次 CI |
| 5. 内嵌 oracle fuzz（P1） | go-fuzz 变异出的任意源码，在同一进程里与经 cgo 嵌入的官方 5.1.5 比对 | PR 上跑 60 秒冒烟，nightly 每档跑约半小时 |
| 6. 内嵌 oracle fuzz（P3 / P4） | 同上，但强制升层后再比，直接比编译层与官方，不靠第 4 层的传递 | PR 上跑 60 秒冒烟，nightly 每档跑约半小时 |

几点说明：

- **官方测试套件不是全部跑完。** 套件里的文件与上游逐字节相同，但文件数比上游少，多数文件在用到不支持的功能（`setfenv`、`debug` 高级接口、`string.dump` 等）的那一行之前截断，只有少数文件从头跑到尾。每个文件跑到哪一行、为什么截断，见 `test/luasuite/luasuite_test.go` 的 `stopAt` 表。
- **比对没有「接受差异」的路径。** 对象地址（`table: 0x...`）在比较前归一化，因为两边的堆布局本来就不同。另有几类输入整个跳过不比：触发实现上限的（栈溢出深度、语法嵌套层数等两边各自定的常数）、碰到 C 未定义行为的、以及会让官方实现自己崩溃的。除此之外，输出有任何差异都算失败。
- 内嵌的 oracle 只在 `wangshu_oracle_cgo` build tag 下编译，默认 build 仍然不依赖 cgo。

每一层的口径、跳过规则和历次发现的问题，见 [12-testing-difftest](docs/design/p1-interpreter/12-testing-difftest.md)。

## 性能

linux/amd64（Intel Xeon Platinum 8573C）上的结果。每格是「wall time (相对 gopher-lua 的倍率)」，倍率越大越好；**粗体**是该行最快的一格，<ins>下划线</ins>表示倍率 ≥ 1.5×。数字来自 GitHub Actions hosted runner 上的一轮标准化基准（2026-07-10，[run 29098511106](https://github.com/Liam0205/wangshu/actions/runs/29098511106)），请以倍率为主，wall time 在不同轮之间会漂 10-20%。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 826 ns | **<ins>149 ns (5.54×)</ins>** | <ins>4246 ns (2.00×)</ins> [^p3-kernel] | 9613 ns (0.88×) [^p3-kernel] | <ins>165 ns (5.02×)</ins> | <ins>165 ns (5.02×)</ins> |
|  | Arith (Horner) | 994 ns | <ins>209 ns (4.75×)</ins> | <ins>6512 ns (2.35×)</ins> [^p3-kernel] | 11423 ns (1.34×) [^p3-kernel] | **<ins>207 ns (4.81×)</ins>** | **<ins>207 ns (4.81×)</ins>** |
|  | Loop (求和循环) | 60.6 µs | **<ins>20.1 µs (3.01×)</ins>** | <ins>419 µs (7.25×)</ins> [^p3-kernel] | <ins>405 µs (7.49×)</ins> [^p3-kernel] | <ins>22.8 µs (2.66×)</ins> | <ins>22.8 µs (2.66×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 292 ms | <ins>84.3 ms (3.46×)</ins> | <ins>97.6 ms (2.99×)</ins> | <ins>97.2 ms (3.00×)</ins> | <ins>16.2 ms (18.0×)</ins> | **<ins>15.7 ms (18.5×)</ins>** |
|  | HeavyRecursion | 9.34 ms | <ins>5.51 ms (1.69×)</ins> | <ins>5.93 ms (1.58×)</ins> | 6.44 ms (1.45×) | <ins>1.94 ms (4.80×)</ins> | **<ins>1.89 ms (4.93×)</ins>** [^selftail] |
|  | HeavyFloatloop | 464 ms | <ins>166 ms (2.80×)</ins> | <ins>57.7 ms (8.03×)</ins> | <ins>60.0 ms (7.73×)</ins> | <ins>26.2 ms (17.7×)</ins> | **<ins>25.7 ms (18.0×)</ins>** |
| realworld small [^cat-realworld] | fib | 10.2 ms | 11.8 ms (0.87×) | 12.8 ms (0.80×) [^p3-gate] | 27.7 ms (0.37×) | **<ins>1.09 ms (9.35×)</ins>** [^seg2seg] | <ins>1.12 ms (9.10×)</ins> [^seg2seg] |
|  | binary-trees | 56.0 ms | 41.4 ms (1.35×) | 45.1 ms (1.24×) [^p3-gate] | 118 ms (0.48×) | **<ins>29.7 ms (1.89×)</ins>** | <ins>31.5 ms (1.78×)</ins> [^seg2seg] |
|  | spectral-norm | 37.0 ms | <ins>21.1 ms (1.75×)</ins> | 25.3 ms (1.46×) [^p3-gate] | 53.4 ms (0.69×) | <ins>2.46 ms (15.0×)</ins> | **<ins>2.40 ms (15.4×)</ins>** [^seg2seg] |
|  | fannkuch | 4.87 ms | 6.32 ms (0.77×) | 7.00 ms (0.70×) | 7.12 ms (0.68×) | <ins>0.65 ms (7.51×)</ins> | **<ins>0.62 ms (7.87×)</ins>** [^seg2seg] |
|  | n-body | 68.4 ms | 53.7 ms (1.27×) | 56.0 ms (1.22×) [^p3-gate] | 108 ms (0.63×) | <ins>4.70 ms (14.5×)</ins> [^math-intrinsic] | **<ins>4.64 ms (14.7×)</ins>** [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 844 ns | **<ins>151 ns (5.58×)</ins>** | — | — | — | — |
|  | CallOnly | **104 ns** | 222 ns (0.47×) | 235 ns (0.44×) | 365 ns (0.28×) | 251 ns (0.41×) | 251 ns (0.41×) |
|  | Boundary (+SetGlobal) | **220 ns** | 392 ns (0.56×) | 400 ns (0.55×) | 830 ns (0.27×) | 351 ns (0.63×) | 342 ns (0.64×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 844 ns | **<ins>151 ns (5.58×)</ins>** | — | — | — | — |
|  | CallOnly | 104 ns | **84.9 ns (1.22×)** | 85.6 ns (1.21×) | 196 ns (0.53×) | 114 ns (0.90×) | 115 ns (0.90×) |
|  | Boundary (+SetGlobal) | 220 ns | 230 ns (0.96×) | 244 ns (0.90×) | 618 ns (0.36×) | **191 ns (1.15×)** | 198 ns (1.11×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 568 µs | 660 µs (0.86×) | 697 µs (0.82×) | 1241 µs (0.46×) | 571 µs (0.99×) | **554 µs (1.03×)** |
|  | Transform (×1000) | **466 µs** | 493 µs (0.95×) | 532 µs (0.88×) | 801 µs (0.58×) | 488 µs (0.95×) | 475 µs (0.98×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 568 µs | 489 µs (1.16×) | 507 µs (1.12×) | 1043 µs (0.54×) | **382 µs (1.49×)** | 394 µs (1.44×) |
|  | Transform (×1000) | 466 µs | 355 µs (1.31×) | 348 µs (1.34×) | 589 µs (0.79×) | <ins>310 µs (1.51×)</ins> | **<ins>306 µs (1.52×)</ins>** |

各列的含义：`gopher` 是 gopher-lua v1.1.2；`P1` 是默认 build 的解释器；`P3` / `P4` 是两个编译层，`auto` 是生产环境的自动升层，`force` 是测试用的强制升层；`Call` / `CallInto` 是两种嵌入调用方式，`CallInto` 不分配。

linux/arm64、darwin/arm64 的结果，各列和脚注的详细说明，以及复现方法，见 [docs/benchmarks.md](docs/benchmarks.md)。

[^cat-baseline]: `benchmarks/baseline`。三个独立的纯 Lua 脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行，不跨 Go↔Lua 边界。反映 VM 内核在最小工作量下的分派、算术和循环开销。
[^cat-heavy]: `benchmarks/heavy`。三个扁平的数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），刻意不含表、字符串和库函数调用。反映编译层在它最擅长的写法上的上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 的五个脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），每个脚本的输出都和官方 lua5.1.5 逐字节比对过。反映调用、分配、浮点和表操作混合的常规负载。
[^cat-mini]: `benchmarks/embedded` 的 `mini_bench_test.go`。嵌入调用的最小形式：每次迭代一次 SetGlobal、一次 Call、读一次结果。反映跨边界本身的成本，以及会分配的 `Call` 与零分配的 `CallInto` 之间的差距。
[^cat-embed]: `benchmarks/embedded` 的 `realworld_embedded_bench_test.go`。1000 条数据一批，每条设置字段、调用谓词或特征变换脚本、读回一个标量。反映真实批处理嵌入场景下的稳态吞吐。
[^p3-kernel]: 这几格的工作量和同行其他列不同。顶层 chunk 是 vararg，永远不会升层，所以 P3 列测的是「把脚本体包进一个内层函数、调用 50 次」；倍率的分母也换成同样写法的 gopher 基准（`_GopherKernel`），所以倍率可比，但 wall time 约是同行其他列的 50 倍，不能直接比。
[^p3-gate]: P3 auto 有一道收益检查：热函数里需要回到 Go 的操作太多、跨边界的成本会吃掉升层收益时，就不升层，留在解释器里跑。带这个标记的格子没有升层，数字就是解释器的执行时间（和 P1 列的差别是采样开销）。P3 force 绕过这道检查，所以不受影响。
[^seg2seg]: P4 的段到段直接调用：自递归、或调用纯算术函数（fib 这类写法）时，调用方的机器码直接跳进被调方的机器码，整个递归过程不回到 Go。amd64 与 arm64 都已实现。
[^selftail]: P4 的自尾调用段内循环：`return f(...)` 调用的就是当前函数时，在机器码内搬移参数并跳回入口，不再每层退出一次再从 Go 重新进入。HeavyRecursion（collatz）的递归调用全是尾调用，所以受益最大。
[^math-intrinsic]: P4 的 math 函数内联：调用点观察到被调的是已知的纯数值宿主函数（`sqrt` / `floor` / `ceil` / `abs` / `max` / `min`）时，直接生成对应的硬件指令（amd64 的 SQRTSD / ROUNDSD，arm64 的 FSQRT / FRINTM 等），不再跨边界回到 Go。结果与解释器逐字节一致，包括 NaN、Inf 和 ±0。n-body 的热循环几乎都是 `sqrt`，受益最大。

## 架构

望舒使用分层虚拟机架构；其执行层以月相命名：

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT （RC 状态） ──► P5 trace JIT （尚未实现）
(crescent)    (基建)        (gibbous)          (gibbous)                   (fullmoon)
```

架构核心承诺：

* NaN-boxed u64 值表示
* 自管理的 arena 线性内存——各层共用同一块内存
* P1 解释器始终可用——所有编译层的 deopt 着陆点及语义 oracle
* CI 保证层与层之间逐字节一致

## 文档导航

按角色路径：

- **想用起来**：本 README 「快速开始」→ [pkg.go.dev](https://pkg.go.dev/github.com/Liam0205/wangshu) 的 `Compile` / `Program.Run` / `Program.Call` / `State.CallInto` API 参考。
- **想上生产（P3/P4 分层执行）**：[docs/embedding-tiers.md](docs/embedding-tiers.md)——部署要求（exec-mmap 环境约束）、运行期开关、TierStats 观测、step budget 语义、上线检查清单。
- **想理解架构**：[docs/design/architecture.md](docs/design/architecture.md)（包布局 / 组件依赖 / tier 映射）→ [docs/design/roadmap.md](docs/design/roadmap.md)（动机 / 校准测量 / 演进路线 / 非目标）。
- **深入某一层**：
  - P1 解释器（13 篇）：[docs/design/p1-interpreter/00-overview.md](docs/design/p1-interpreter/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p1-interpreter/implementation-progress.md)
  - P2 分层桥（7 篇）：[docs/design/p2-bridge/00-overview.md](docs/design/p2-bridge/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p2-bridge/implementation-progress.md)
  - P3 gibbous-wasm（10 篇）：[docs/design/p3-wasm-tier/00-overview.md](docs/design/p3-wasm-tier/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md)
  - P4 gibbous-jit（11 篇 + progress）：[docs/design/p4-method-jit/00-overview.md](docs/design/p4-method-jit/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p4-method-jit/implementation-progress.md) · PJ11 验收 [09-acceptance-checklist](docs/design/p4-method-jit/09-acceptance-checklist.md)
  - P5 trace JIT（未立项，图纸已就位）：[docs/design/p5-trace-jit/](docs/design/p5-trace-jit/00-overview.md)（11 章施工图纸，启动判定见 01）
- **工程规范 / 提交纪律**：[docs/design/engineering.md](docs/design/engineering.md)（Git hooks / CI / Makefile / 发布纪律 / lint 工具链）。
- **AI 协作规范**：[llmdoc/](llmdoc/) 目录记录项目层面对 LLM 协作者的引导——[startup.md](llmdoc/startup.md) 起，含 must（不可违背）/ guides（协作最佳实践）/ memory（历史决议与反思，`reflections/` 有各里程碑教训）。
- **性能完整数据**：[docs/benchmarks.md](docs/benchmarks.md)（三平台的表、各列说明、复现方法）。
- **修复记录 / 行为变更**：各阶段 `implementation-progress.md`（上面「深入某一层」里的进度对账）按日期记每轮修了什么、改了哪些行为；每轮的根因和教训在 [llmdoc/memory/reflections/](llmdoc/memory/reflections/)。

## 欢迎贡献

Issue 与 PR 都欢迎。基本步骤：

**开发环境**：Go 1.27+（`go.mod` 的 `go` 指令是 `1.27.0`，更早的工具链会直接拒绝构建；升到 1.27.0 的原因见 [engineering.md §1.1](docs/design/engineering.md)），Linux/amd64、Linux/arm64 或 macOS/arm64（其他 GOOS/GOARCH 组合按纯 Go stub 编译过但未实际运行测试）。可选依赖：`lua5.1`（官方 oracle，差分测试用；`apt install lua5.1` 或源码编译 5.1.5）、`golangci-lint`（lint）。

**常用 make 目标**：

```bash
make all              # 提交前本地全检：fmt + lint + build-all + test-all + fuzz-all + conformance + difftest-all
make test-p4          # 单独跑 P4 build 全套测试
make test-p3          # 单独跑 P3 build
make difftest         # 三档 × 三平台差分测试
make fuzz-p4          # P4 build 下 fuzz 冒烟
make fuzz-oracle      # cgo 内嵌官方 5.1.5 进程内差分 fuzz（需本机 gcc）
make bench            # baseline 微基准
make release TAG=vX.Y.Z MESSAGE_FILE=notes.txt  # 打 annotated tag（本地不 push）
```

**提交流程**：

1. Fork + 建 feature 分支（不要直接 push master）。
2. 本地 `make all` 通过。
3. Commit message 用英文，subject 单行 ≤ 72 字符 ASCII，body 中文可以，说清 why 与 how。
4. PR 描述必须包含变更范围、测试情况、是否引入外部依赖（zero-cgo / 主库 zero 外部依赖是硬承诺）。
5. PR 触发 CI（三平台 × 三 build × test/fuzz-smoke/conformance/difftest 全绿）+ agentic-pr-review bot 自动审阅；bot 提 REQUEST_CHANGES 须响应，APPROVE 后 maintainer 审 merge。
6. 大改动之前建议先开 issue 讨论方向。

**Bug report** 请附最小复现脚本、Go 版本、GOOS/GOARCH、`make all` 输出。若涉及输出与官方 5.1.5 不一致，一起附上 `lua5.1 -e ...` 的 stdout 对比。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)（若文件缺失以 `go.mod` 声明为准）。

用人话总结：

- 可自由地用、改、分发、商用，包括嵌入闭源产品；
- 保留 LICENSE 与版权声明；
- 若你的分发物包含本项目的改动，简要标注改动点即可；
- 项目方无担保义务，`AS IS`。
