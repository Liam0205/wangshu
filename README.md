# Wangshu（望舒）

望舒是纯 Go 实现的高性能可嵌入的 Lua 5.1 虚拟机。它不依赖 cgo，因此保持了交叉编译能力。

关于命名：Lua 是葡萄牙语中「月亮」的意思；望舒是中国神话中为月亮驱车的神灵（「前望舒使先驱」——《楚辞·离骚》）。为月亮驱车，即驱动 Lua 引擎。是一信达雅的名字。

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Go Report Card](https://goreportcard.com/badge/github.com/Liam0205/wangshu)](https://goreportcard.com/report/github.com/Liam0205/wangshu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**中文** · [English](README.en.md)

## 目标

- 语言标准：实现 Lua 5.1 的核心语言特性。——与 LuaJIT 一致，不追求语言的绝对完整性。
- 正确性：在圈定的语言特性范围，与 Lua 5.1 官方实现的输出逐字节一致。
- 高性能：将 Go 生态的 Lua 执行性能从 gopher-lua 提升至 LuaJ-luajc（Java）甚至 LuaJIT（C++）级别。
- 挂平台：在 Linux/amd64, Linux/arm64, macOS/arm64 测试通过；保留其他平台扩张支持的能力。

## 架构

望舒使用分层虚拟机架构；其执行层以月相命名：

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT （RC 状态） ──► P5 trace JIT （尚未实现）
(crescent)    (基建)        (gibbous)          (gibbous)                   (fullmoon)
```

架构核心承诺：

* NaN-boxed u64 值表示
* 自管理的 arana 线性内存——各层共用同一块内存
* P1 解释器始终可用——所有编译层的 deopt 着陆点及语义 oracle
* CI 保证层与层之间逐字节一致

## 性能指标

数字取自同一台机（linux/amd64, Intel Xeon Platinum, 24 core, go1.26.2, `-benchtime=2s -count=3`, 取 median, 2026-07-02）。格式为「wall time (倍率 over gopher-lua)」，倍率越大越好；粗体表示倍率 ≥ 1.5×。darwin/arm64 实测见下方小节。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 954 ns | 135 ns (**7.07×**) | 3956 ns (0.24×) | 3954 ns (0.24×) | 147 ns (**6.49×**) | 147 ns (**6.49×**) |
| | Arith (Horner) | 1045 ns | 175 ns (**5.97×**) | 10134 ns (0.10×) | 10150 ns (0.10×) | 184 ns (**5.70×**) | 184 ns (**5.70×**) |
| | Loop (求和循环) | 37.2 µs | 17.0 µs (**2.18×**) | 358 µs (0.10×) | 357 µs (0.10×) | 21.1 µs (**1.77×**) | 21.1 µs (**1.77×**) |
| heavy 内核 [^cat-heavy] | HeavyArith | 158 ms | 80.7 ms (**1.96×**) | 86.1 ms (**1.84×**) | 86.0 ms (**1.84×**) | 13.6 ms (**11.60×**) | 13.2 ms (**11.93×**) |
| | HeavyRecursion | 8.16 ms | 5.15 ms (**1.58×**) | 5.71 ms (1.43×) | 5.71 ms (1.43×) | 5.36 ms (**1.52×**) | 5.34 ms (**1.53×**) |
| | HeavyFloatloop | 285 ms | 147 ms (**1.94×**) | 50.5 ms (**5.64×**) | 50.6 ms (**5.63×**) | 21.9 ms (**13.02×**) | 21.5 ms (**13.26×**) |
| realworld small [^cat-realworld] | fib | 9.44 ms | 10.0 ms (0.94×) | 24.4 ms (0.39×) | 25.1 ms (0.38×) | 10.9 ms (0.86×) | 10.9 ms (0.87×) |
| | binary-trees | 33.7 ms | 36.8 ms (0.92×) | 104.7 ms (0.32×) | 103.1 ms (0.33×) | 39.4 ms (0.86×) | 38.9 ms (0.87×) |
| | spectral-norm | 22.6 ms | 18.6 ms (1.22×) | 39.7 ms (0.57×) | 47.3 ms (0.48×) | 18.1 ms (1.25×) | 17.7 ms (1.28×) |
| | fannkuch | 4.20 ms | 5.65 ms (0.74×) | 5.71 ms (0.74×) | 5.74 ms (0.73×) | 0.60 ms (**7.00×**) | 0.60 ms (**7.04×**) |
| | n-body | 51.9 ms | 45.4 ms (1.14×) | 89.7 ms (0.58×) | 87.0 ms (0.60×) | 44.2 ms (1.17×) | 44.3 ms (1.17×) |
| 边界 mini · Call [^cat-mini] | PureVM | 945 ns | 138 ns (**6.85×**) | — | — | — | — |
| | CallOnly | 85.2 ns | 194 ns (0.44×) | 208 ns (0.41×) | 315 ns (0.27×) | 204 ns (0.42×) | 225 ns (0.38×) |
| | Boundary (+SetGlobal) | 185 ns | 324 ns (0.57×) | 340 ns (0.54×) | 337 ns (0.55×) | 295 ns (0.63×) | 294 ns (0.63×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 945 ns | 138 ns (**6.85×**) | — | — | — | — |
| | CallOnly | 85.2 ns | 79.4 ns (1.07×) | 79.0 ns (1.08×) | 166 ns (0.51×) | 78.6 ns (1.08×) | 112 ns (0.76×) |
| | Boundary (+SetGlobal) | 185 ns | 180 ns (1.03×) | 192 ns (0.97×) | 192 ns (0.96×) | 169 ns (1.09×) | 169 ns (1.09×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 476 µs | 583 µs (0.82×) | 574 µs (0.83×) | 566 µs (0.84×) | 464 µs (1.03×) | 463 µs (1.03×) |
| | Transform (×1000) | 337 µs | 436 µs (0.77×) | 443 µs (0.76×) | 440 µs (0.77×) | 392 µs (0.86×) | 390 µs (0.86×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 476 µs | 407 µs (1.17×) | 420 µs (1.13×) | 420 µs (1.13×) | 324 µs (1.47×) | 321 µs (1.48×) |
| | Transform (×1000) | 337 µs | 287 µs (1.18×) | 292 µs (1.15×) | 289 µs (1.17×) | 261 µs (1.29×) | 259 µs (1.30×) |

P4 vs P3 同口径对比：27 组对照中 25 组 P4 领先 ≥ 2%（多数 +10% ~ +85%）。仅「CallOnly auto」两行例外——该脚本低于升层阈值，P3/P4 build 都在跑同一份 P1 解释器代码，差值为测量噪声（< 2%）。

### darwin/arm64 实测（Apple M5 Pro）

同一套复现命令在 Apple M5 Pro（darwin/arm64, go1.26.4, `-benchtime=2s -count=3`, 取 median, 2026-07-03）的实测。arm64 的 P4 native op-set 已经通过 exit-reason 协议移植完成（issue #37 / #40）：算术 / 比较 / 表 / 全局 / 调用 op 与 amd64 同一套接受面（IC 门 + CALL 密度门），heavy 三本与 realworld 五本 P4 全面不差于 P3——HeavyArith 2.0×、HeavyFloatloop 2.5× over P3，与 amd64 的翻盘幅度同量级。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 | Simple (分支/比较) | 572 ns | 83.2 ns (**6.88×**) | 2.57 µs (0.22×) | 2.57 µs (0.22×) | 82.7 ns (**6.92×**) | 82.7 ns (**6.92×**) |
| | Arith (Horner) | 605 ns | 102 ns (**5.93×**) | 6.42 µs (0.09×) | 6.38 µs (0.09×) | 105 ns (**5.76×**) | 105 ns (**5.76×**) |
| | Loop (求和循环) | 20.0 µs | 9.99 µs (**2.00×**) | 498 µs (0.04×) | 499 µs (0.04×) | 12.4 µs (**1.61×**) | 12.4 µs (**1.61×**) |
| heavy 内核 | HeavyArith | 87.2 ms | 44.3 ms (**1.97×**) | 50.9 ms (**1.71×**) | 51.3 ms (**1.70×**) | 25.6 ms (**3.40×**) | 25.3 ms (**3.45×**) |
| | HeavyRecursion | 5.50 ms | 3.13 ms (**1.76×**) | 3.60 ms (**1.53×**) | 3.70 ms (1.48×) | 3.37 ms (**1.63×**) | 3.38 ms (**1.63×**) |
| | HeavyFloatloop | 153 ms | 83.8 ms (**1.83×**) | 61.5 ms (**2.49×**) | 62.4 ms (**2.46×**) | 25.3 ms (**6.05×**) | 25.3 ms (**6.05×**) |
| realworld small | fib | 5.60 ms | 6.41 ms (0.87×) | 14.3 ms (0.39×) | 14.3 ms (0.39×) | 6.97 ms (0.80×) | 6.94 ms (0.81×) |
| | binary-trees | 19.3 ms | 23.9 ms (0.81×) | 59.9 ms (0.32×) | 59.9 ms (0.32×) | 25.5 ms (0.76×) | 25.2 ms (0.77×) |
| | spectral-norm | 12.9 ms | 12.2 ms (1.06×) | 23.2 ms (0.56×) | 28.3 ms (0.46×) | 12.1 ms (1.07×) | 13.2 ms (0.98×) |
| | fannkuch | 2.46 ms | 3.64 ms (0.68×) | 3.71 ms (0.66×) | 3.72 ms (0.66×) | 3.71 ms (0.66×) | 3.85 ms (0.64×) |
| | n-body | 30.2 ms | 27.5 ms (1.10×) | 49.8 ms (0.61×) | 50.0 ms (0.60×) | 32.9 ms (0.92×) | 32.9 ms (0.92×) |
| 边界 mini · Call | PureVM | 490 ns | 77.5 ns (**6.32×**) | — | — | — | — |
| | CallOnly | 54.0 ns | 104 ns (0.52×) | 105 ns (0.51×) | 165 ns (0.33×) | 105 ns (0.51×) | 106 ns (0.51×) |
| | Boundary (+SetGlobal) | 120 ns | 179 ns (0.67×) | 177 ns (0.68×) | 180 ns (0.67×) | 176 ns (0.68×) | 176 ns (0.68×) |
| 边界 mini · CallInto | PureVM | 490 ns | 77.5 ns (**6.32×**) | — | — | — | — |
| | CallOnly | 54.0 ns | 46.4 ns (1.17×) | 48.7 ns (1.11×) | 103 ns (0.53×) | 48.9 ns (1.11×) | 48.4 ns (1.12×) |
| | Boundary (+SetGlobal) | 120 ns | 120 ns (1.01×) | 120 ns (1.00×) | 121 ns (1.00×) | 120 ns (1.01×) | 122 ns (0.99×) |
| 真实负载 · Call | Predicate (×1000) | 282 µs | 321 µs (0.88×) | 323 µs (0.87×) | 327 µs (0.86×) | 322 µs (0.88×) | 324 µs (0.87×) |
| | Transform (×1000) | 212 µs | 236 µs (0.90×) | 239 µs (0.89×) | 243 µs (0.88×) | 224 µs (0.95×) | 222 µs (0.96×) |
| 真实负载 · CallInto | Predicate (×1000) | 282 µs | 264 µs (1.07×) | 262 µs (1.08×) | 269 µs (1.05×) | 265 µs (1.07×) | 263 µs (1.07×) |
| | Transform (×1000) | 212 µs | 181 µs (1.17×) | 183 µs (1.16×) | 183 µs (1.16×) | 167 µs (**1.27×**) | 167 µs (**1.27×**) |

P4 vs P3 同口径对比（arm64）：heavy 三本 + realworld 五本 P4 全面不差于 P3（HeavyArith 2.03×、HeavyFloatloop 2.47×、fib 2.06×、binary-trees 2.38×、spectral-norm 2.14×、n-body 1.52× over P3；fannkuch 与 HeavyRecursion 打平在噪声内）。

[^cat-baseline]: `benchmarks/baseline`。三个自含脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行无 Go↔Lua 跨界。反映 VM 内核在最小工作量下的 dispatch / 算术 / 循环开销。
[^cat-heavy]: `benchmarks/heavy`。三个扁平数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），故意剔除表 / 字符串 / library CALL 与其他 helper-bound 结构。反映编译档在能真正发挥的形状上的性能上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 五脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），语义单次通过与官方 lua5.1.5 做差分测试（逐字节比对）。反映调用 / 分配 / 浮点 / 表操作混合场景下的常规负载。
[^cat-mini]: `benchmarks/embedded`，mini_bench_test.go。嵌入路径的最小形式：每 iter 一次 SetGlobal + 一次 Call + 一次读结果。反映边界往返成本本身，以及 `Call` 分配路径与 `CallInto` 零分配路径的成本差。
[^cat-embed]: `benchmarks/embedded`，realworld_embedded_bench_test.go。1000 item batch，逐 item set 字段 → Call 谓词 / 特征变换脚本 → 读标量结果，写法贴近 pineapple `transform_by_lua`。反映真实批处理嵌入下的稳态吞吐。

### 列的含义

- **`gopher`** — gopher-lua v1.1.2，基线。表格里的倍率都是 `gopher / X`，越大越好。
- **`P1`** — `go build` 默认档，纯解释器（crescent），没有升层机制，一列就够。
- **`P3 auto` / `P3 force`** — `wangshu_p3 wangshu_profile` build 下 gibbous-wasm 编译层的两种测法（详见下节）。
- **`P4 auto` / `P4 force`** — `wangshu_p4 wangshu_profile` build 下 gibbous-jit method JIT 的两种测法。
- **`Call` / `CallInto`** — 嵌入 API 两种边界调用方式：`st.Call` 每次分配 `[]Value` 返回切片；`st.CallInto` 复用调用方 `dst`，零分配。只在跨界 benchmark 拆两列。

> `—` 表示该场景下不涉及 Call/CallInto 之分（PureVM 无跨界；升不到编译层的 baseline 短脚本没有独立数字）。

### auto 与 force 只对 P3/P4 有意义

P3/P4 编译档不是「装了就一定用」。它们是**基于热度阈值的自动升层机制**：

1. 每个函数（Proto）默认在 P1 crescent 解释器上跑。
2. 每次调用累计一次调用计数；`wangshu_profile` build tag 开启这个采样器（不带此 tag 采样禁用，编译档退化到 P1）。
3. 计数越过 `HotEntryThreshold`（默认 200）后，如果该 Proto 通过 F1-F7 可编译性检查，就升到 P3 或 P4，后续调用走编译层。
4. 升不动的 Proto（协程 / 顶层 vararg / 含 `ReasonUnknownCall` / VARARG 等）保持在 P1，无声降级。

因此 P3/P4 每档在表格里各有两列：

- **`auto`** — 生产模式。State 长期复用，前 ~200 次调用走 P1 解释器，越过阈值后升到编译层。`b.N` 上摊薄下来，warmup tail 通常在噪声之内。
- **`force`** — `SetForceAllPromote(true)` 强制所有可升 Proto 直接升层，预热一轮后测稳态。**非生产模式**，只用于差分测试和 benchmark 上限。

两者稳态数字理论上应当接近；出现明显差异说明升层策略或阈值需要调整。

### 复现命令

三档 build 各跑一次，`-count=3` 取 median，全套约 6-10 分钟：

```bash
DIRS='./benchmarks/baseline/ ./benchmarks/heavy/ ./benchmarks/realworld/ ./benchmarks/embedded/'
FLAGS='-run=^$ -benchtime=2s -count=3'

# P1: crescent 解释器 (默认 build)
go test -bench='_(Wangshu|WangshuCall|WangshuCallInto|Gopher)$' $FLAGS $DIRS

# P3: gibbous-wasm (auto 走 _WangshuKernel/_GibbousAuto*，force 走 _Gibbous*)
go test -tags "wangshu_p3 wangshu_profile" \
    -bench='_(Gibbous|GibbousCall|GibbousCallInto|GibbousAuto|GibbousAutoCall|GibbousAutoCallInto|WangshuKernel)$' \
    $FLAGS $DIRS

# P4: gibbous-jit (auto 走 _GibbousJITAuto*，force 走 _GibbousJIT*)
go test -tags "wangshu_p4 wangshu_profile" \
    -bench='_(GibbousJIT|GibbousJITCall|GibbousJITCallInto|GibbousJITAuto|GibbousJITAutoCall|GibbousJITAutoCallInto)$' \
    $FLAGS $DIRS
```

CI 侧另有 `bench-acceptance` workflow 跨三平台 (linux/amd64, linux/arm64, darwin/arm64) 跑列内核 Horner-1000 + heavy 三本 + boundary Const/Nil 六组：

```bash
gh workflow run bench-acceptance.yml
```

数据落在 workflow artifact 里，Run #28505893556 (2026-07-01) 是 P4 PJ11 验收基准，见 [p4 09-acceptance-checklist §3](docs/design/p4-method-jit/09-acceptance-checklist.md)。

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
st.ArenaCapKB() // arena backing 容量 KB（grow-only；pool 层据此判 fat state 阈值）
st.PromotionCount() // 已升层 proto 数（testing-only 白盒断言）
```

显式驱动 GC：

```go
st.Collect()           // 强制一次 full GC sweep
st.MaybeCollectNow()   // 依 host trigger 阈值判是否 collect（非强制）
st.SetHostTriggeredCollect(true) // opt-in：host 侧跨阈自动 collect（要求 transient GCRef 全 pin）
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

望舒实现的是 Lua 5.1 核心语言（与 LuaJIT 一致的语法层），覆盖 Lua 5.1 参考手册中定义的 38 个字节码 opcode 除 `VARARG` 外的全部（`VARARG` 在 P3/P4 编译层永不接入，走 P1 解释器路径），以及 stdlib 的 base / string / table / math / os / io / coroutine 全部必做面。

正确性验证四层：

1. **官方测试套 byte-equal**：13 个 5.1.5 官方文件（vararg / sort / pm 整文件 + 其余截至豁免线）逐字节一致。
2. **手册逐节 probe**：100 项手册特性 + 12 项边角 + 29 条错误消息（含行号断言）+ 70 条种子用例逐字节一致。
3. **差分随机 fuzz**：nightly-diff-fuzz workflow 每晚 2M 条随机脚本与 Lua 5.1.5 oracle 做差分测试（P1 + P3 + P4 三档并行）。
4. **三方差分**：crescent（P1）vs gibbous（P3/P4）在 P4 build 下每 CI 跑一次 byte-equal，PR #29/#31 tri-platform matrix 全绿。

**豁免清单**（`test/difftest/corners_test.go::exemptions`，共 15 项，`go test -v -run TestExemptions_Documented` 可审计）：

| 类别 | 具体项 | 豁免原因 |
| --- | --- | --- |
| Lua 5.2+ 特性 | `rawlen`、`table.pack` / `table.move` | 与 5.1 手册不符 |
| Lua 5.3+ 特性 | `math.tointeger` / `type` / `maxinteger` / `mininteger` | 整数类型属 5.3 特性 |
| 嵌入式安全 | `os.execute`、`io.popen` / `io.tmpfile`、`os.exit` 真退出、`loadfile` / `dofile` 默认禁用 | 嵌入式 VM 不让脚本跑 shell / 越权文件系统 |
| Debug 接口 | `debug.sethook` / `getlocal` / `setlocal` / `getupvalue` / `setupvalue` / `getregistry` | 需要解释器内部 hook，成本收益不划算 |
| 模块系统 | `require` / `module` / `package` | 嵌入式宿主经 `Compile` 提供脚本，不从文件系统 require |
| 字节码序列化 | `string.dump` | 自定义 ISA 不兼容官方 `.luc` |
| 环境操作 | `getfenv` / `setfenv` | 与 P2 分层桥 F4 形状分析冲突 |
| C 未定义行为 | `tonumber` 负数 `strtoul` 回绕 | 官方经 C `strtoul` 溢出返 `1.844e19`，本实现返 `-255`，取直觉语义 |
| 灾难性回溯 | pattern 灾难回溯 `.*.+%A*x` | 回溯预算 `1<<20` 步硬限，报 `pattern too complex`（嵌入式防挂起） |
| 增量 GC | `collectgarbage("step"/"setstepmul")` | STW GC 无增量调参，占位返回 |

其余「存在但不逐字节比」的项（`collectgarbage("count")` / `gcinfo` / `os.time` / `os.clock` / `os.date("%Y")` / `io.write` / `loadfile` 返错格式）由 `TestApprox_ExistenceOnly` 只断言返回值格式不比数值。

## 文档导航

按角色路径：

- **想用起来**：本 README 「快速开始」→ [pkg.go.dev](https://pkg.go.dev/github.com/Liam0205/wangshu) 的 `Compile` / `Program.Run` / `Program.Call` / `State.CallInto` API 参考。
- **想理解架构**：[docs/design/architecture.md](docs/design/architecture.md)（包布局 / 组件依赖 / tier 映射）→ [docs/design/roadmap.md](docs/design/roadmap.md)（动机 / 校准测量 / 演进路线 / 非目标）。
- **深入某一层**：
  - P1 解释器（13 篇）：[docs/design/p1-interpreter/00-overview.md](docs/design/p1-interpreter/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p1-interpreter/implementation-progress.md)
  - P2 分层桥（7 篇）：[docs/design/p2-bridge/00-overview.md](docs/design/p2-bridge/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p2-bridge/implementation-progress.md)
  - P3 gibbous-wasm（10 篇）：[docs/design/p3-wasm-tier/00-overview.md](docs/design/p3-wasm-tier/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md)
  - P4 gibbous-jit（11 篇 + progress）：[docs/design/p4-method-jit/00-overview.md](docs/design/p4-method-jit/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p4-method-jit/implementation-progress.md) · PJ11 验收 [09-acceptance-checklist](docs/design/p4-method-jit/09-acceptance-checklist.md)
  - P5 trace JIT（未立项，图纸已就位）：[docs/design/p5-trace-jit/](docs/design/p5-trace-jit/00-overview.md)（11 章施工图纸，启动判定见 01）
- **工程规范 / 提交纪律**：[docs/design/engineering.md](docs/design/engineering.md)（Git hooks / CI / Makefile / 发布纪律 / lint 工具链）。
- **AI 协作规范**：[llmdoc/](llmdoc/) 目录记录项目层面对 LLM 协作者的引导——[startup.md](llmdoc/startup.md) 起，含 must（不可违背）/ guides（协作最佳实践）/ memory（历史决议与反思，`reflections/` 有各里程碑教训）。

## 欢迎贡献

Issue 与 PR 都欢迎。基本步骤：

**开发环境**：Go 1.25+，Linux/amd64、Linux/arm64 或 macOS/arm64（其他 GOOS/GOARCH 组合按纯 Go stub 编译过但未真跑测试）。可选依赖：`lua5.1`（官方 oracle，差分测试用；`apt install lua5.1` 或源码编译 5.1.5）、`golangci-lint`（lint）。

**常用 make 目标**：

```bash
make all              # 提交前本地全检：fmt + lint + build-all + test-all + fuzz-all + conformance + difftest-all
make test-p4          # 单独跑 P4 build 全套测试
make test-p3          # 单独跑 P3 build
make difftest         # 三档 × 三平台差分测试
make fuzz-p4          # P4 build 下 fuzz 冒烟
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
