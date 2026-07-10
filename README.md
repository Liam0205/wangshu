# Wangshu（望舒）

望舒是纯 Go 实现的高性能可嵌入的 Lua 5.1 虚拟机。它不依赖 cgo，因此保持了交叉编译能力。

关于命名：Lua 是葡萄牙语中「月亮」的意思；望舒是中国神话中为月亮驱车的神灵（「前望舒使先驱」——《楚辞·离骚》）。为月亮驱车，即驱动 Lua 引擎。是一信达雅的名字。

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**中文** · [English](README.en.md)

## 目标

- 语言标准：实现 Lua 5.1 的核心语言特性。——与 LuaJIT 一致，不追求语言的绝对完整性。
- 正确性：在圈定的语言特性范围，与 Lua 5.1 官方实现的输出逐字节一致。
- 高性能：将 Go 生态的 Lua 执行性能从 gopher-lua 提升至 LuaJ-luajc（Java）甚至 LuaJIT（C++）级别。
- 跨平台：在 Linux/amd64, Linux/arm64, macOS/arm64 测试通过；保留其他平台扩张支持的能力。
- 工业级：望舒从立项开始就是为了公司业务服务的，并且已经为在我所在公司线上运行。从测试、到 CI、再到各类 nightly-fuzz，都是朝着工业级的项目要求去的。望舒从来都不会，也不可能是一个个人的练习项目。我们希望望舒最终成为在 Go 语言项目中嵌入 Lua 的事实标准。

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

数字取自同一台机（linux/amd64, Intel Xeon Platinum, 24 core, go1.26.2, `-benchtime=2s -count=3 -cpu=1`, 取 median，2026-07-09 实测）。格式为「wall time (倍率 over gopher-lua)」，倍率越大越好；**粗体**表示该行最快，<ins>下划线</ins>表示倍率 ≥ 1.5×。整张表由 `scripts/bench-readme-table.sh` 一键复现（见[下方小节](#复现命令)）。darwin/arm64 实测见下方小节。

> 说明：倍率的分母是 gopher-lua 在**当轮**同机实测值，机器状态（co-tenant / 温度 / turbo）会让 gopher 基线在不同轮次间浮动，所以倍率只在同一轮内横向可比；跨轮对比请看 wall time 而非倍率。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 845 ns | **<ins>149 ns (5.67×)</ins>** | <ins>4426 ns (1.85×)</ins> [^p3-kernel] | 9675 ns (0.85×) [^p3-kernel] | <ins>162 ns (5.22×)</ins> | <ins>162 ns (5.22×)</ins> |
|  | Arith (Horner) | 1003 ns | **<ins>196 ns (5.11×)</ins>** | <ins>6577 ns (2.23×)</ins> [^p3-kernel] | 11152 ns (1.32×) [^p3-kernel] | <ins>199 ns (5.04×)</ins> | <ins>199 ns (5.04×)</ins> |
|  | Loop (求和循环) | 60.0 µs | **<ins>18.2 µs (3.30×)</ins>** | <ins>396 µs (7.39×)</ins> [^p3-kernel] | <ins>393 µs (7.45×)</ins> [^p3-kernel] | <ins>23.0 µs (2.61×)</ins> | <ins>23.0 µs (2.61×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 249 ms | <ins>79.5 ms (3.13×)</ins> | <ins>95.3 ms (2.61×)</ins> | <ins>95.1 ms (2.61×)</ins> | <ins>15.6 ms (15.9×)</ins> | **<ins>15.2 ms (16.4×)</ins>** |
|  | HeavyRecursion | 8.94 ms | **<ins>5.49 ms (1.63×)</ins>** | <ins>5.87 ms (1.52×)</ins> | 6.18 ms (1.45×) | 6.48 ms (1.38×) | 6.48 ms (1.38×) |
|  | HeavyFloatloop | 445 ms | <ins>157 ms (2.83×)</ins> | <ins>55.2 ms (8.06×)</ins> | <ins>55.1 ms (8.07×)</ins> | **<ins>26.6 ms (16.7×)</ins>** | <ins>26.8 ms (16.6×)</ins> |
| realworld small [^cat-realworld] | fib | 10.2 ms | 10.9 ms (0.93×) | 11.9 ms (0.86×) [^p3-gate] | 27.1 ms (0.38×) | <ins>1.04 ms (9.81×)</ins> [^seg2seg] | **<ins>1.04 ms (9.83×)</ins>** [^seg2seg] |
|  | binary-trees | 55.1 ms | 38.9 ms (1.42×) | 41.7 ms (1.32×) [^p3-gate] | 113 ms (0.49×) | **<ins>28.3 ms (1.95×)</ins>** | <ins>28.3 ms (1.95×)</ins> [^seg2seg] |
|  | spectral-norm | 36.7 ms | <ins>19.5 ms (1.89×)</ins> | <ins>22.5 ms (1.63×)</ins> [^p3-gate] | 51.0 ms (0.72×) | <ins>2.34 ms (15.7×)</ins> | **<ins>2.33 ms (15.7×)</ins>** [^seg2seg] |
|  | fannkuch | 4.47 ms | 5.91 ms (0.76×) | 6.20 ms (0.72×) | 6.20 ms (0.72×) | <ins>0.67 ms (6.70×)</ins> | **<ins>0.66 ms (6.76×)</ins>** [^seg2seg] |
|  | n-body | 66.9 ms | 45.7 ms (1.46×) | 48.3 ms (1.39×) [^p3-gate] | 94.0 ms (0.71×) | <ins>4.48 ms (15.0×)</ins> [^math-intrinsic] | **<ins>4.48 ms (15.0×)</ins>** [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 845 ns | **<ins>150 ns (5.62×)</ins>** | — | — | — | — |
|  | CallOnly | **91.1 ns** | 210 ns (0.43×) | 223 ns (0.41×) | 340 ns (0.27×) | 241 ns (0.38×) | 241 ns (0.38×) |
|  | Boundary (+SetGlobal) | **198 ns** | 348 ns (0.57×) | 362 ns (0.55×) | 744 ns (0.27×) | 324 ns (0.61×) | 320 ns (0.62×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 845 ns | **<ins>150 ns (5.62×)</ins>** | — | — | — | — |
|  | CallOnly | 91.1 ns | **84.3 ns (1.08×)** | 85.9 ns (1.06×) | 181 ns (0.50×) | 110 ns (0.83×) | 110 ns (0.83×) |
|  | Boundary (+SetGlobal) | 198 ns | 194 ns (1.02×) | 206 ns (0.96×) | 588 ns (0.34×) | **172 ns (1.15×)** | 172 ns (1.15×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 542 µs | 591 µs (0.92×) | 611 µs (0.89×) | 1138 µs (0.48×) | **500 µs (1.08×)** | 502 µs (1.08×) |
|  | Transform (×1000) | **388 µs** | 454 µs (0.85×) | 471 µs (0.82×) | 716 µs (0.54×) | 430 µs (0.90×) | 425 µs (0.91×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 542 µs | 427 µs (1.27×) | 452 µs (1.20×) | 965 µs (0.56×) | <ins>348 µs (1.56×)</ins> | **<ins>348 µs (1.56×)</ins>** |
|  | Transform (×1000) | 388 µs | 291 µs (1.33×) | 312 µs (1.24×) | 553 µs (0.70×) | **276 µs (1.40×)** | 278 µs (1.40×) |

### darwin/arm64 实测（Apple M5 Pro）

同一套复现命令在 Apple M5 Pro（darwin/arm64, go1.26.4, `-benchtime=2s -count=3 -cpu=1`, 取 median，2026-07-09 全表实测，与上方 amd64 表同一代码，含 #91-#94 修复）。gopher 基线与倍率只在本表内横向可比。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 | Simple (分支/比较) | 494 ns | **<ins>83.8 ns (5.89×)</ins>** | <ins>2606 ns (1.77×)</ins> [^p3-kernel] | 5155 ns (0.89×) [^p3-kernel] | <ins>84.9 ns (5.81×)</ins> | <ins>84.9 ns (5.81×)</ins> |
|  | Arith (Horner) | 509 ns | **<ins>103 ns (4.95×)</ins>** | <ins>3659 ns (2.26×)</ins> [^p3-kernel] | 6483 ns (1.27×) [^p3-kernel] | <ins>109 ns (4.69×)</ins> | <ins>109 ns (4.69×)</ins> |
|  | Loop (求和循环) | 30.3 µs | **<ins>9.54 µs (3.18×)</ins>** | <ins>483 µs (3.07×)</ins> [^p3-kernel] | <ins>483 µs (3.07×)</ins> [^p3-kernel] | <ins>12.4 µs (2.44×)</ins> | <ins>12.4 µs (2.44×)</ins> |
| heavy 内核 | HeavyArith | 127 ms | <ins>44.9 ms (2.82×)</ins> | <ins>50.8 ms (2.50×)</ins> | <ins>50.8 ms (2.50×)</ins> | **<ins>24.5 ms (5.19×)</ins>** | <ins>24.6 ms (5.15×)</ins> |
|  | HeavyRecursion | 6.07 ms | **<ins>3.07 ms (1.98×)</ins>** | <ins>3.46 ms (1.75×)</ins> | <ins>3.58 ms (1.69×)</ins> | 4.49 ms (1.35×) | 4.50 ms (1.35×) |
|  | HeavyFloatloop | 221 ms | <ins>84.9 ms (2.61×)</ins> | <ins>59.8 ms (3.70×)</ins> | <ins>58.8 ms (3.76×)</ins> | **<ins>24.9 ms (8.90×)</ins>** | <ins>25.2 ms (8.79×)</ins> |
| realworld small | fib | 5.48 ms | 6.24 ms (0.88×) | 7.03 ms (0.78×) [^p3-gate] | 14.1 ms (0.39×) | <ins>0.64 ms (8.57×)</ins> [^seg2seg] | **<ins>0.63 ms (8.71×)</ins>** [^seg2seg] |
|  | binary-trees | 30.0 ms | 23.6 ms (1.27×) | 25.1 ms (1.20×) [^p3-gate] | 59.6 ms (0.50×) | <ins>16.6 ms (1.81×)</ins> | **<ins>16.4 ms (1.83×)</ins>** [^seg2seg] |
|  | spectral-norm | 19.5 ms | <ins>12.0 ms (1.62×)</ins> | 13.3 ms (1.46×) [^p3-gate] | 27.8 ms (0.70×) | <ins>2.21 ms (8.82×)</ins> | **<ins>2.20 ms (8.87×)</ins>** [^seg2seg] |
|  | fannkuch | 2.48 ms | 3.61 ms (0.69×) | 3.71 ms (0.67×) | 3.70 ms (0.67×) | **<ins>0.37 ms (6.75×)</ins>** | <ins>0.37 ms (6.73×)</ins> [^seg2seg] |
|  | n-body | 37.8 ms | 31.1 ms (1.22×) | 27.8 ms (1.36×) [^p3-gate] | 49.3 ms (0.77×) | <ins>3.83 ms (9.88×)</ins> [^math-intrinsic] | **<ins>3.82 ms (9.91×)</ins>** [^math-intrinsic] |
| 边界 mini · Call | PureVM | 452 ns | **<ins>83.4 ns (5.41×)</ins>** | — | — | — | — |
|  | CallOnly | **53.9 ns** | 107 ns (0.51×) | 110 ns (0.49×) | 168 ns (0.32×) | 143 ns (0.38×) | 141 ns (0.38×) |
|  | Boundary (+SetGlobal) | **118 ns** | 174 ns (0.68×) | 180 ns (0.66×) | 384 ns (0.31×) | 187 ns (0.63×) | 178 ns (0.66×) |
| 边界 mini · CallInto | PureVM | 452 ns | **<ins>83.4 ns (5.41×)</ins>** | — | — | — | — |
|  | CallOnly | 53.9 ns | **46.4 ns (1.16×)** | 50.0 ns (1.08×) | 103 ns (0.52×) | 72.1 ns (0.75×) | 71.9 ns (0.75×) |
|  | Boundary (+SetGlobal) | 118 ns | 117 ns (1.01×) | 120 ns (0.99×) | 317 ns (0.37×) | 116 ns (1.02×) | **116 ns (1.02×)** |
| 真实负载 · Call | Predicate (×1000) | 302 µs | 321 µs (0.94×) | 325 µs (0.93×) | 611 µs (0.49×) | 299 µs (1.01×) | **296 µs (1.02×)** |
|  | Transform (×1000) | 252 µs | **237 µs (1.06×)** | 245 µs (1.03×) | 372 µs (0.68×) | 249 µs (1.01×) | 251 µs (1.00×) |
| 真实负载 · CallInto | Predicate (×1000) | 302 µs | 258 µs (1.17×) | 259 µs (1.17×) | 539 µs (0.56×) | 226 µs (1.33×) | **223 µs (1.36×)** |
|  | Transform (×1000) | 252 µs | **176 µs (1.43×)** | 181 µs (1.40×) | 305 µs (0.83×) | 177 µs (1.42×) | 178 µs (1.42×) |

[^cat-baseline]: `benchmarks/baseline`。三个独立的纯 Lua 脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行无 Go↔Lua 跨界。反映 VM 内核在最小工作量下的 dispatch / 算术 / 循环开销。
[^cat-heavy]: `benchmarks/heavy`。三个扁平数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），故意剔除表 / 字符串 / library CALL 与其他 helper-bound 结构。反映编译档在能真正发挥的形状上的性能上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 五脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），语义单次通过与官方 lua5.1.5 做差分测试（逐字节比对）。反映调用 / 分配 / 浮点 / 表操作混合场景下的常规负载。
[^p3-gate]: P3 auto 模式带 helper 密度收益门（issue #39，2026-07-03）：热 proto 的 op 组合里 helper 往返占比过高（wasm→Go 边界成本吞掉升层收益）时拒绝升层、留在解释器。带此标注的行升层被拒，数字即解释器执行（与 P1 列的差异是采样钩子开销）。P3 force 列不受影响（force-all 绕过收益门，保差分覆盖）。
[^p3-kernel]: baseline P3 列的工作负载与其它列不同（issue #93）：顶层 chunk 是 vararg 永不升层，P3 必须测「包进内层 kernel 调 50 次」的形状；其它列跑裸顶层 ×1。因此 P3 列的倍率分母是**同形状**的 gopher 基准（`_GopherKernel`，gopher 跑一样的 kernel×50），wall time 与同行其它列不可直接比（工作量 ≈50 倍）。此前表格误拿顶层 ×1 的 gopher 当分母，把 P3 低估约 50 倍（旧表 0.06×-0.25× 实为 1.3×-3.2×）。amd64 与 arm64 行均已按修正后口径同机重测（2026-07-09）。
[^seg2seg]: P4 段到段 CALL 直跳（issue #50，2026-07-04，amd64 + arm64 已交付）：自递归 / arith-callee（fib 形状）之前每次调用付一次跨界往返税（mmap RET → Go dispatch → host.CallBaseline → mmap 重入），现在 caller 段直接 `call` 进 callee 段、callee 段内组拆帧 + native 递归、全程不出 mmap。amd64 同轮实测（2026-07-08，`scripts/bench-readme-table.sh`，`-benchtime=2s -count=3 -cpu=1` median，over gopher-lua）：fib **10.2×**、spectral-norm auto 与 force 都到约 **16×**（内层 A/Av/Atv 走段到段；#77 的密度门修复后 auto 也能吃满收益，不再只有 force 受益）、fannkuch **7.13×**、binary-trees **2.00×**（`check` 自递归 + GETTABLE ArrayHit 读表，随 ArrayHit 站点纳入段到段资格 + forceAll 重试窗口放宽而解锁，剩余瓶颈是 bottomup 的分配）。darwin/arm64 M5 Pro 同日复测（见下表）：fib **8.67×**、spectral-norm **8.75×**（auto 同到 8.73×，与 amd64 一样吃到 #77 密度门修复）、binary-trees **1.82×**，双架构收益形状一致。
[^math-intrinsic]: P4 math.* intrinsic emission（issue #77 / PR #87，2026-07-08，amd64 + arm64 已交付）：CALL 站点 IC 观察到被调是已知纯数值 host closure（sqrt / floor / ceil / abs / max / min）时，段内直接发射硬件指令（amd64 SQRTSD / ROUNDSD 等）而不再 exit-reason 往返到 Go host closure。n-body 的稳态几乎全是 `sqrt(dist2)` 调用，之前既因每次 sqrt 付一次跨界往返、又因 CALL 密度门把带 sqrt 的热函数误判成「调用太密、升层不划算」而拒绝升层，两头卡住（P4 ≈ P1，1.41×）；#77 一并修好后（intrinsic CALL 不计入密度门 + sqrt 内联发射），n-body 从 1.40× 翻到 **14.3×**（60.5 ms → 4.23 ms）。结果与解释器逐字节一致（含 NaN / Inf / ±0）。darwin/arm64 M5 Pro 同日复测同样生效（FSQRT / FRINTM 等 arm64 对应指令）：n-body 从 0.98× 翻到 **9.65×**（30.9 ms → 3.81 ms），auto 与 force 一致。
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

整张表由一个脚本一键复现——跑三档 build、收集原始日志、整理成上面这张 Markdown 表格（`-cpu=1` 串行，共享机友好；全套约 20-30 分钟）：

```bash
./scripts/bench-readme-table.sh              # 全跑 + 直接输出可贴进 README 的表格
./scripts/bench-readme-table.sh --count 5    # 每档跑 5 次取 median
./scripts/bench-readme-table.sh --format-only <logdir>  # 只重排已有日志,不重跑
```

脚本会自动探测 `goos/goarch`，在 arm64 机器上跑同一条命令即可复现 arm64 表。它选取的 benchmark 与下面手动命令一致（benchmark 在 `benchmarks/` 子模块里，手动跑要先 `cd benchmarks`）：

```bash
cd benchmarks
DIRS='./baseline/ ./heavy/ ./realworld/ ./embedded/'
FLAGS='-run=^$ -benchtime=2s -count=3 -cpu=1'

# P1: crescent 解释器 (默认 build；GopherKernel 是 baseline P3 列的同形状分母)
go test -bench='_(Wangshu|WangshuCall|WangshuCallInto|Gopher|GopherKernel)$' $FLAGS $DIRS

# P3: gibbous-wasm (auto 走 _WangshuKernel/_GibbousAuto*，force 走 _Gibbous*)
go test -tags "wangshu_p3 wangshu_profile" \
    -bench='_(Gibbous|GibbousCall|GibbousCallInto|GibbousAuto|GibbousAutoCall|GibbousAutoCallInto|WangshuKernel)$' \
    $FLAGS $DIRS

# P4: gibbous-jit (auto 走 _GibbousJITAuto*，force 走 _GibbousJIT*)
go test -tags "wangshu_p4 wangshu_profile" \
    -bench='_(GibbousJIT|GibbousJITCall|GibbousJITCallInto|GibbousJITAuto|GibbousJITAutoCall|GibbousJITAutoCallInto)$' \
    $FLAGS $DIRS
# P4 baseline (Simple/Arith/Loop 在 P4 build 下无独立 kernel bench,用 _Wangshu)
go test -tags "wangshu_p4 wangshu_profile" -bench='(Simple|Arith|Loop)_Wangshu$' $FLAGS ./baseline/
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
