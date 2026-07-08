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

数字取自同一台机（linux/amd64, Intel Xeon Platinum, 24 core, go1.26.2, `-benchtime=2s -count=3 -cpu=1`, 取 median，2026-07-08 实测）。格式为「wall time (倍率 over gopher-lua)」，倍率越大越好；**粗体**表示该行最快，<ins>下划线</ins>表示倍率 ≥ 1.5×。整张表由 `scripts/bench-readme-table.sh` 一键复现（见[下方小节](#复现命令)）。darwin/arm64 实测见下方小节。

> 说明：倍率的分母是 gopher-lua 在**当轮**同机实测值，机器状态（co-tenant / 温度 / turbo）会让 gopher 基线在不同轮次间浮动，所以倍率只在同一轮内横向可比；跨轮对比请看 wall time 而非倍率。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 812 ns | **<ins>141 ns (5.77×)</ins>** | 4122 ns (0.20×) | 4105 ns (0.20×) | <ins>160 ns (5.08×)</ins> | <ins>160 ns (5.08×)</ins> |
|  | Arith (Horner) | 945 ns | **<ins>186 ns (5.08×)</ins>** | 10557 ns (0.09×) | 10412 ns (0.09×) | <ins>195 ns (4.85×)</ins> | <ins>195 ns (4.85×)</ins> |
|  | Loop (求和循环) | 56.2 µs | **<ins>17.2 µs (3.27×)</ins>** | 365 µs (0.15×) | 365 µs (0.15×) | <ins>22.0 µs (2.56×)</ins> | <ins>22.0 µs (2.56×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 248 ms | <ins>76.4 ms (3.24×)</ins> | <ins>91.2 ms (2.71×)</ins> | <ins>91.4 ms (2.71×)</ins> | <ins>14.6 ms (16.9×)</ins> | **<ins>14.2 ms (17.5×)</ins>** |
|  | HeavyRecursion | 8.82 ms | **<ins>5.22 ms (1.69×)</ins>** | 5.89 ms (1.50×) | <ins>5.86 ms (1.50×)</ins> | <ins>5.54 ms (1.59×)</ins> | <ins>5.56 ms (1.59×)</ins> |
|  | HeavyFloatloop | 420 ms | <ins>148 ms (2.83×)</ins> | <ins>52.6 ms (7.98×)</ins> | <ins>52.7 ms (7.97×)</ins> | **<ins>25.3 ms (16.6×)</ins>** | <ins>25.3 ms (16.6×)</ins> |
| realworld small [^cat-realworld] | fib | 9.80 ms | 10.3 ms (0.95×) | 11.3 ms (0.87×) [^p3-gate] | 25.0 ms (0.39×) | <ins>0.96 ms (10.2×)</ins> [^seg2seg] | **<ins>0.96 ms (10.2×)</ins>** [^seg2seg] |
|  | binary-trees | 53.2 ms | 36.7 ms (1.45×) | 41.3 ms (1.29×) [^p3-gate] | 106 ms (0.50×) | <ins>26.6 ms (2.00×)</ins> | **<ins>26.6 ms (2.00×)</ins>** [^seg2seg] |
|  | spectral-norm | 34.8 ms | <ins>18.5 ms (1.88×)</ins> | <ins>22.0 ms (1.58×)</ins> [^p3-gate] | 48.0 ms (0.72×) | <ins>2.18 ms (15.9×)</ins> | **<ins>2.17 ms (16.0×)</ins>** [^seg2seg] |
|  | fannkuch | 4.44 ms | 5.57 ms (0.80×) | 5.98 ms (0.74×) | 6.00 ms (0.74×) | <ins>0.63 ms (7.10×)</ins> | **<ins>0.62 ms (7.13×)</ins>** [^seg2seg] |
|  | n-body | 60.5 ms | 43.1 ms (1.40×) | 45.3 ms (1.34×) [^p3-gate] | 88.9 ms (0.68×) | <ins>4.25 ms (14.2×)</ins> [^math-intrinsic] | **<ins>4.23 ms (14.3×)</ins>** [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 791 ns | **<ins>144 ns (5.47×)</ins>** | — | — | — | — |
|  | CallOnly | **86.3 ns** | 203 ns (0.43×) | 213 ns (0.41×) | 316 ns (0.27×) | 232 ns (0.37×) | 231 ns (0.37×) |
|  | Boundary (+SetGlobal) | **187 ns** | 330 ns (0.57×) | 345 ns (0.54×) | 343 ns (0.54×) | 304 ns (0.61×) | 306 ns (0.61×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 791 ns | **<ins>144 ns (5.47×)</ins>** | — | — | — | — |
|  | CallOnly | 86.3 ns | **80.1 ns (1.08×)** | 81.0 ns (1.07×) | 174 ns (0.50×) | 105 ns (0.82×) | 105 ns (0.83×) |
|  | Boundary (+SetGlobal) | 187 ns | 182 ns (1.03×) | 197 ns (0.95×) | 197 ns (0.95×) | **164 ns (1.14×)** | 166 ns (1.13×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 502 µs | 559 µs (0.90×) | 570 µs (0.88×) | 571 µs (0.88×) | **475 µs (1.06×)** | 480 µs (1.05×) |
|  | Transform (×1000) | **359 µs** | 424 µs (0.85×) | 446 µs (0.80×) | 445 µs (0.81×) | 415 µs (0.86×) | 402 µs (0.89×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 502 µs | 402 µs (1.25×) | 422 µs (1.19×) | 422 µs (1.19×) | **<ins>320 µs (1.57×)</ins>** | <ins>326 µs (1.54×)</ins> |
|  | Transform (×1000) | 359 µs | 272 µs (1.32×) | 299 µs (1.20×) | 294 µs (1.22×) | 259 µs (1.39×) | **256 µs (1.40×)** |

### darwin/arm64 实测（Apple M5 Pro）

同一套复现命令在 Apple M5 Pro（darwin/arm64, go1.26.4, `-benchtime=2s -count=3 -cpu=1`, 取 median）的实测。

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 | Simple (分支/比较) | 572 ns | <ins>83.2 ns (6.88×)</ins> | 2.57 µs (0.22×) | 2.57 µs (0.22×) | **<ins>82.7 ns (6.92×)</ins>** | **<ins>82.7 ns (6.92×)</ins>** |
|  | Arith (Horner) | 605 ns | **<ins>102 ns (5.93×)</ins>** | 6.42 µs (0.09×) | 6.38 µs (0.09×) | <ins>105 ns (5.76×)</ins> | <ins>105 ns (5.76×)</ins> |
|  | Loop (求和循环) | 20.0 µs | **<ins>9.99 µs (2.00×)</ins>** | 498 µs (0.04×) | 499 µs (0.04×) | <ins>12.4 µs (1.61×)</ins> | <ins>12.4 µs (1.61×)</ins> |
| heavy 内核 | HeavyArith | 87.2 ms | <ins>44.3 ms (1.97×)</ins> | <ins>50.9 ms (1.71×)</ins> | <ins>51.3 ms (1.70×)</ins> | <ins>24.8 ms (3.52×)</ins> | **<ins>24.5 ms (3.56×)</ins>** |
|  | HeavyRecursion | 5.50 ms | **<ins>3.13 ms (1.76×)</ins>** | <ins>3.60 ms (1.53×)</ins> | 3.70 ms (1.48×) | <ins>3.38 ms (1.63×)</ins> | <ins>3.40 ms (1.62×)</ins> |
|  | HeavyFloatloop | 153 ms | <ins>83.8 ms (1.83×)</ins> | <ins>61.5 ms (2.49×)</ins> | <ins>62.4 ms (2.46×)</ins> | <ins>25.0 ms (6.13×)</ins> | **<ins>24.9 ms (6.14×)</ins>** |
| realworld small | fib | 5.60 ms | 6.41 ms (0.87×) | 7.33 ms (0.76×) [^p3-gate] | 14.3 ms (0.39×) | **<ins>0.60 ms (9.3×)</ins>** [^seg2seg] | <ins>0.61 ms (9.1×)</ins> [^seg2seg] |
|  | binary-trees | 19.3 ms | 23.9 ms (0.81×) | 26.4 ms (0.73×) [^p3-gate] | 59.9 ms (0.32×) | 16.7 ms (1.16×) [^seg2seg] | **16.6 ms (1.16×)** [^seg2seg] |
|  | spectral-norm | 12.9 ms | 12.2 ms (1.06×) | 13.5 ms (0.96×) [^p3-gate] | 28.3 ms (0.46×) | 10.2 ms (1.26×) | **<ins>2.25 ms (5.74×)</ins>** [^seg2seg] |
|  | fannkuch | 2.46 ms | 3.64 ms (0.68×) | 3.76 ms (0.65×) | 3.72 ms (0.66×) | **<ins>0.34 ms (7.25×)</ins>** | **<ins>0.34 ms (7.27×)</ins>** |
|  | n-body | 30.2 ms | **27.5 ms (1.10×)** | 28.9 ms (1.04×) [^p3-gate] | 50.0 ms (0.60×) | 31.0 ms (0.98×) | 30.9 ms (0.98×) |
| 边界 mini · Call | PureVM | 490 ns | **<ins>77.5 ns (6.32×)</ins>** | — | — | — | — |
|  | CallOnly | **54.0 ns** | 104 ns (0.52×) | 105 ns (0.51×) | 165 ns (0.33×) | 105 ns (0.51×) | 106 ns (0.51×) |
|  | Boundary (+SetGlobal) | **120 ns** | 179 ns (0.67×) | 177 ns (0.68×) | 180 ns (0.67×) | 176 ns (0.68×) | 176 ns (0.68×) |
| 边界 mini · CallInto | PureVM | 490 ns | **<ins>77.5 ns (6.32×)</ins>** | — | — | — | — |
|  | CallOnly | 54.0 ns | **46.4 ns (1.17×)** | 48.7 ns (1.11×) | 103 ns (0.53×) | 48.9 ns (1.11×) | 48.4 ns (1.12×) |
|  | Boundary (+SetGlobal) | **120 ns** | **120 ns (1.01×)** | **120 ns (1.00×)** | 121 ns (1.00×) | **120 ns (1.01×)** | 122 ns (0.99×) |
| 真实负载 · Call | Predicate (×1000) | **282 µs** | 321 µs (0.88×) | 323 µs (0.87×) | 327 µs (0.86×) | 322 µs (0.88×) | 324 µs (0.87×) |
|  | Transform (×1000) | **212 µs** | 236 µs (0.90×) | 239 µs (0.89×) | 243 µs (0.88×) | 224 µs (0.95×) | 222 µs (0.96×) |
| 真实负载 · CallInto | Predicate (×1000) | 282 µs | 264 µs (1.07×) | **262 µs (1.08×)** | 269 µs (1.05×) | 265 µs (1.07×) | 263 µs (1.07×) |
|  | Transform (×1000) | 212 µs | 181 µs (1.17×) | 183 µs (1.16×) | 183 µs (1.16×) | **167 µs (1.27×)** | **167 µs (1.27×)** |

[^cat-baseline]: `benchmarks/baseline`。三个独立的纯 Lua 脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行无 Go↔Lua 跨界。反映 VM 内核在最小工作量下的 dispatch / 算术 / 循环开销。
[^cat-heavy]: `benchmarks/heavy`。三个扁平数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），故意剔除表 / 字符串 / library CALL 与其他 helper-bound 结构。反映编译档在能真正发挥的形状上的性能上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 五脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），语义单次通过与官方 lua5.1.5 做差分测试（逐字节比对）。反映调用 / 分配 / 浮点 / 表操作混合场景下的常规负载。
[^p3-gate]: P3 auto 模式带 helper 密度收益门（issue #39，2026-07-03）：热 proto 的 op 组合里 helper 往返占比过高（wasm→Go 边界成本吞掉升层收益）时拒绝升层、留在解释器。带此标注的行升层被拒，数字即解释器执行（与 P1 列的差异是采样钩子开销）。P3 force 列不受影响（force-all 绕过收益门，保差分覆盖）。
[^seg2seg]: P4 段到段 CALL 直跳（issue #50，2026-07-04，amd64 + arm64 已交付）：自递归 / arith-callee（fib 形状）之前每次调用付一次跨界往返税（mmap RET → Go dispatch → host.CallBaseline → mmap 重入），现在 caller 段直接 `call` 进 callee 段、callee 段内组拆帧 + native 递归、全程不出 mmap。amd64 同轮实测（2026-07-08，`scripts/bench-readme-table.sh`，`-benchtime=2s -count=3 -cpu=1` median，over gopher-lua）：fib **10.2×**、spectral-norm auto 与 force 都到约 **16×**（内层 A/Av/Atv 走段到段；#77 的密度门修复后 auto 也能吃满收益，不再只有 force 受益）、fannkuch **7.13×**、binary-trees **2.00×**（`check` 自递归 + GETTABLE ArrayHit 读表，随 ArrayHit 站点纳入段到段资格 + forceAll 重试窗口放宽而解锁，剩余瓶颈是 bottomup 的分配）。arm64 端镜像实现同分支交付，darwin/arm64 M5 Pro 真机数字仍是上一轮（2026-07-07，见下表，本轮尚未重跑）：fib **9.1×**、spectral-norm **5.74×**，追踪于 issue #61。
[^math-intrinsic]: P4 math.* intrinsic emission（issue #77 / PR #87，2026-07-08，amd64 + arm64 已交付）：CALL 站点 IC 观察到被调是已知纯数值 host closure（sqrt / floor / ceil / abs / max / min）时，段内直接发射硬件指令（amd64 SQRTSD / ROUNDSD 等）而不再 exit-reason 往返到 Go host closure。n-body 的稳态几乎全是 `sqrt(dist2)` 调用，之前既因每次 sqrt 付一次跨界往返、又因 CALL 密度门把带 sqrt 的热函数误判成「调用太密、升层不划算」而拒绝升层，两头卡住（P4 ≈ P1，1.41×）；#77 一并修好后（intrinsic CALL 不计入密度门 + sqrt 内联发射），n-body 从 1.40× 翻到 **14.3×**（60.5 ms → 4.23 ms）。结果与解释器逐字节一致（含 NaN / Inf / ±0）。arm64 正确性由 CI 保证，效率待 arm64 机器复测。
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
