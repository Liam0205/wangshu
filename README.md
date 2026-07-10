# Wangshu（望舒）

望舒是纯 Go 实现的高性能可嵌入的 Lua 5.1 虚拟机。它不依赖 cgo，因此保持了交叉编译能力。

关于命名：Lua 是葡萄牙语中「月亮」的意思；望舒是中国神话中为月亮驱车的神灵（「前望舒使先驱」——《楚辞·离骚》）。为月亮驱车，即驱动 Lua 引擎。是一信达雅的名字。

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Tag](https://img.shields.io/github/v/tag/Liam0205/wangshu?include_prereleases&sort=semver&label=release)](https://github.com/Liam0205/wangshu/tags)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Liam0205/wangshu)](go.mod)
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

数字来自 GitHub Actions hosted runner 上的标准化基准轮（`bench-readme-table` workflow，`-benchtime=2s -count=3 -cpu=1`，取 median，2026-07-10，[run 29079804565](https://github.com/Liam0205/wangshu/actions/runs/29079804565)），三平台同一轮、同一份代码。格式为「wall time (倍率 over gopher-lua)」，倍率越大越好；**粗体**表示该行最快，<ins>下划线</ins>表示倍率 ≥ 1.5×。

> **怎么读**：hosted runner 是共享虚拟机，绝对 wall time 轮间可漂 10-20%——**请以倍率为主**，wall time 只作同轮内的量级参考。倍率的分母是 gopher-lua 在**同一轮同一台 runner** 上的实测值（分子分母同受干扰，倍率自洽），跨轮、跨平台都不要直接比 wall time。任何数字都可回溯到 run artifact 里的原始日志。

### linux/amd64（Intel Xeon Platinum 8370C）

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 992 ns | **<ins>196 ns (5.07×)</ins>** | <ins>5966 ns (1.70×)</ins> [^p3-kernel] | 12155 ns (0.83×) [^p3-kernel] | <ins>229 ns (4.34×)</ins> | <ins>229 ns (4.34×)</ins> |
|  | Arith (Horner) | 1203 ns | **<ins>260 ns (4.63×)</ins>** | <ins>8513 ns (2.28×)</ins> [^p3-kernel] | 13896 ns (1.39×) [^p3-kernel] | <ins>288 ns (4.18×)</ins> | <ins>288 ns (4.18×)</ins> |
|  | Loop (求和循环) | 77.8 µs | **<ins>22.9 µs (3.39×)</ins>** | <ins>486 µs (7.69×)</ins> [^p3-kernel] | <ins>485 µs (7.71×)</ins> [^p3-kernel] | <ins>29.5 µs (2.64×)</ins> | <ins>29.5 µs (2.64×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 314 ms | <ins>99.3 ms (3.16×)</ins> | <ins>117 ms (2.69×)</ins> | <ins>117 ms (2.69×)</ins> | <ins>17.6 ms (17.8×)</ins> | **<ins>16.8 ms (18.6×)</ins>** |
|  | HeavyRecursion | 11.5 ms | <ins>7.08 ms (1.62×)</ins> | 7.79 ms (1.47×) | 8.28 ms (1.39×) | <ins>2.33 ms (4.91×)</ins> | **<ins>2.33 ms (4.92×)</ins>** [^selftail] |
|  | HeavyFloatloop | 559 ms | <ins>195 ms (2.86×)</ins> | <ins>73.3 ms (7.63×)</ins> | <ins>73.6 ms (7.60×)</ins> | <ins>30.9 ms (18.1×)</ins> | **<ins>30.8 ms (18.2×)</ins>** |
| realworld small [^cat-realworld] | fib | 12.7 ms | 14.3 ms (0.89×) | 15.8 ms (0.80×) [^p3-gate] | 33.8 ms (0.38×) | **<ins>1.37 ms (9.25×)</ins>** [^seg2seg] | <ins>1.38 ms (9.20×)</ins> [^seg2seg] |
|  | binary-trees | 70.6 ms | 53.5 ms (1.32×) | 56.6 ms (1.25×) [^p3-gate] | 139 ms (0.51×) | <ins>39.1 ms (1.80×)</ins> | **<ins>38.9 ms (1.82×)</ins>** [^seg2seg] |
|  | spectral-norm | 48.1 ms | <ins>26.3 ms (1.83×)</ins> | <ins>30.8 ms (1.56×)</ins> [^p3-gate] | 62.3 ms (0.77×) | <ins>2.99 ms (16.1×)</ins> | **<ins>2.98 ms (16.1×)</ins>** [^seg2seg] |
|  | fannkuch | 5.80 ms | 8.11 ms (0.71×) | 8.66 ms (0.67×) | 8.65 ms (0.67×) | <ins>0.74 ms (7.84×)</ins> | **<ins>0.73 ms (7.92×)</ins>** [^seg2seg] |
|  | n-body | 70.0 ms | 63.3 ms (1.11×) | 66.3 ms (1.06×) [^p3-gate] | 118 ms (0.59×) | **<ins>5.68 ms (12.3×)</ins>** [^math-intrinsic] | <ins>5.68 ms (12.3×)</ins> [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 987 ns | **<ins>194 ns (5.09×)</ins>** | — | — | — | — |
|  | CallOnly | **119 ns** | 289 ns (0.41×) | 309 ns (0.38×) | 442 ns (0.27×) | 339 ns (0.35×) | 336 ns (0.35×) |
|  | Boundary (+SetGlobal) | **257 ns** | 483 ns (0.53×) | 499 ns (0.52×) | 976 ns (0.26×) | 450 ns (0.57×) | 445 ns (0.58×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 987 ns | **<ins>194 ns (5.09×)</ins>** | — | — | — | — |
|  | CallOnly | 119 ns | **98.9 ns (1.20×)** | 106 ns (1.12×) | 226 ns (0.53×) | 139 ns (0.86×) | 139 ns (0.86×) |
|  | Boundary (+SetGlobal) | 257 ns | 282 ns (0.91×) | 302 ns (0.85×) | 742 ns (0.35×) | **248 ns (1.04×)** | 250 ns (1.03×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | **652 µs** | 784 µs (0.83×) | 811 µs (0.80×) | 1487 µs (0.44×) | 679 µs (0.96×) | 680 µs (0.96×) |
|  | Transform (×1000) | **528 µs** | 607 µs (0.87×) | 637 µs (0.83×) | 950 µs (0.56×) | 585 µs (0.90×) | 586 µs (0.90×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 652 µs | 583 µs (1.12×) | 603 µs (1.08×) | 1237 µs (0.53×) | **473 µs (1.38×)** | 476 µs (1.37×) |
|  | Transform (×1000) | 528 µs | 406 µs (1.30×) | 428 µs (1.24×) | 717 µs (0.74×) | **382 µs (1.38×)** | 383 µs (1.38×) |

### linux/arm64（Azure Cobalt 100，Neoverse-N2 类）

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 965 ns | **<ins>202 ns (4.77×)</ins>** | <ins>5951 ns (1.68×)</ins> [^p3-kernel] | 10202 ns (0.98×) [^p3-kernel] | <ins>212 ns (4.56×)</ins> | <ins>212 ns (4.56×)</ins> |
|  | Arith (Horner) | 1141 ns | **<ins>247 ns (4.63×)</ins>** | <ins>8344 ns (2.19×)</ins> [^p3-kernel] | <ins>12179 ns (1.50×)</ins> [^p3-kernel] | <ins>259 ns (4.41×)</ins> | <ins>259 ns (4.41×)</ins> |
|  | Loop (求和循环) | 75.9 µs | **<ins>23.1 µs (3.28×)</ins>** | <ins>594 µs (6.10×)</ins> [^p3-kernel] | <ins>594 µs (6.09×)</ins> [^p3-kernel] | <ins>28.9 µs (2.63×)</ins> | <ins>28.9 µs (2.63×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 303 ms | <ins>96.4 ms (3.14×)</ins> | <ins>118 ms (2.57×)</ins> | <ins>118 ms (2.57×)</ins> | <ins>23.1 ms (13.1×)</ins> | **<ins>22.0 ms (13.8×)</ins>** |
|  | HeavyRecursion | 9.66 ms | 6.58 ms (1.47×) | 7.48 ms (1.29×) | 8.12 ms (1.19×) | **<ins>2.43 ms (3.98×)</ins>** | <ins>2.43 ms (3.98×)</ins> [^selftail] |
|  | HeavyFloatloop | 542 ms | <ins>189 ms (2.87×)</ins> | <ins>84.9 ms (6.38×)</ins> | <ins>85.0 ms (6.38×)</ins> | **<ins>37.0 ms (14.7×)</ins>** | <ins>37.0 ms (14.6×)</ins> |
| realworld small [^cat-realworld] | fib | 12.5 ms | 15.8 ms (0.79×) | 16.3 ms (0.77×) [^p3-gate] | 29.8 ms (0.42×) | **<ins>1.46 ms (8.57×)</ins>** [^seg2seg] | <ins>1.46 ms (8.57×)</ins> [^seg2seg] |
|  | binary-trees | 66.2 ms | 52.1 ms (1.27×) | 54.9 ms (1.21×) [^p3-gate] | 120 ms (0.55×) | <ins>37.0 ms (1.79×)</ins> | **<ins>37.0 ms (1.79×)</ins>** [^seg2seg] |
|  | spectral-norm | 46.6 ms | <ins>27.3 ms (1.70×)</ins> | 31.7 ms (1.47×) [^p3-gate] | 54.9 ms (0.85×) | <ins>5.62 ms (8.29×)</ins> | **<ins>5.62 ms (8.29×)</ins>** [^seg2seg] |
|  | fannkuch | 5.63 ms | 7.67 ms (0.73×) | 7.50 ms (0.75×) | 7.50 ms (0.75×) | <ins>0.84 ms (6.72×)</ins> | **<ins>0.83 ms (6.75×)</ins>** [^seg2seg] |
|  | n-body | 70.9 ms | 57.4 ms (1.23×) | 59.2 ms (1.20×) [^p3-gate] | 106 ms (0.67×) | **<ins>8.86 ms (8.00×)</ins>** [^math-intrinsic] | <ins>8.88 ms (7.98×)</ins> [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 972 ns | **<ins>204 ns (4.76×)</ins>** | — | — | — | — |
|  | CallOnly | **132 ns** | 285 ns (0.46×) | 306 ns (0.43×) | 428 ns (0.31×) | 375 ns (0.35×) | 379 ns (0.35×) |
|  | Boundary (+SetGlobal) | **278 ns** | 468 ns (0.59×) | 494 ns (0.56×) | 897 ns (0.31×) | 491 ns (0.57×) | 490 ns (0.57×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 972 ns | **<ins>204 ns (4.76×)</ins>** | — | — | — | — |
|  | CallOnly | 132 ns | **132 ns (1.00×)** | 146 ns (0.91×) | 215 ns (0.62×) | 200 ns (0.66×) | 200 ns (0.66×) |
|  | Boundary (+SetGlobal) | **278 ns** | 312 ns (0.89×) | 324 ns (0.86×) | 672 ns (0.41×) | 317 ns (0.88×) | 316 ns (0.88×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | **657 µs** | 796 µs (0.83×) | 828 µs (0.79×) | 1383 µs (0.47×) | 757 µs (0.87×) | 756 µs (0.87×) |
|  | Transform (×1000) | **570 µs** | 639 µs (0.89×) | 666 µs (0.86×) | 934 µs (0.61×) | 676 µs (0.84×) | 674 µs (0.85×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 657 µs | 621 µs (1.06×) | 630 µs (1.04×) | 1156 µs (0.57×) | **558 µs (1.18×)** | 563 µs (1.17×) |
|  | Transform (×1000) | 570 µs | **469 µs (1.21×)** | 498 µs (1.14×) | 709 µs (0.80×) | 485 µs (1.17×) | 475 µs (1.20×) |

### darwin/arm64（Apple M 系，macos-latest）

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 767 ns | <ins>149 ns (5.16×)</ins> | <ins>4143 ns (2.72×)</ins> [^p3-kernel] | 7896 ns (1.43×) [^p3-kernel] | **<ins>132 ns (5.83×)</ins>** | **<ins>132 ns (5.83×)</ins>** |
|  | Arith (Horner) | 876 ns | <ins>188 ns (4.65×)</ins> | <ins>6233 ns (3.07×)</ins> [^p3-kernel] | <ins>10743 ns (1.78×)</ins> [^p3-kernel] | **<ins>165 ns (5.32×)</ins>** | **<ins>165 ns (5.32×)</ins>** |
|  | Loop (求和循环) | 77.8 µs | **<ins>18.4 µs (4.23×)</ins>** | <ins>914 µs (3.83×)</ins> [^p3-kernel] | <ins>837 µs (4.18×)</ins> [^p3-kernel] | <ins>21.0 µs (3.71×)</ins> | <ins>21.0 µs (3.71×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 292 ms | <ins>109 ms (2.67×)</ins> | <ins>92.2 ms (3.17×)</ins> | <ins>94.6 ms (3.09×)</ins> | <ins>40.0 ms (7.30×)</ins> | **<ins>37.3 ms (7.84×)</ins>** |
|  | HeavyRecursion | 12.0 ms | <ins>6.50 ms (1.84×)</ins> | <ins>6.40 ms (1.87×)</ins> | <ins>6.61 ms (1.81×)</ins> | <ins>2.04 ms (5.87×)</ins> | **<ins>1.91 ms (6.25×)</ins>** [^selftail] |
|  | HeavyFloatloop | 481 ms | <ins>149 ms (3.22×)</ins> | <ins>112 ms (4.29×)</ins> | <ins>111 ms (4.32×)</ins> | <ins>45.5 ms (10.6×)</ins> | **<ins>40.3 ms (11.9×)</ins>** |
| realworld small [^cat-realworld] | fib | 13.2 ms | 15.0 ms (0.88×) | 13.1 ms (1.01×) [^p3-gate] | 22.2 ms (0.60×) | **<ins>1.08 ms (12.2×)</ins>** [^seg2seg] | <ins>1.14 ms (11.6×)</ins> [^seg2seg] |
|  | binary-trees | 54.5 ms | 41.0 ms (1.33×) | 41.2 ms (1.32×) [^p3-gate] | 94.8 ms (0.57×) | <ins>27.9 ms (1.95×)</ins> | **<ins>27.5 ms (1.98×)</ins>** [^seg2seg] |
|  | spectral-norm | 33.3 ms | <ins>20.4 ms (1.63×)</ins> | <ins>21.2 ms (1.57×)</ins> [^p3-gate] | 45.8 ms (0.73×) | **<ins>4.92 ms (6.78×)</ins>** | <ins>4.93 ms (6.77×)</ins> [^seg2seg] |
|  | fannkuch | 4.41 ms | 6.21 ms (0.71×) | 6.44 ms (0.68×) | 6.40 ms (0.69×) | <ins>0.74 ms (5.98×)</ins> | **<ins>0.72 ms (6.11×)</ins>** [^seg2seg] |
|  | n-body | 65.4 ms | 44.7 ms (1.46×) | 46.2 ms (1.42×) [^p3-gate] | 79.5 ms (0.82×) | **<ins>7.35 ms (8.89×)</ins>** [^math-intrinsic] | <ins>7.40 ms (8.83×)</ins> [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 846 ns | **<ins>144 ns (5.88×)</ins>** | — | — | — | — |
|  | CallOnly | **97.5 ns** | 179 ns (0.54×) | 191 ns (0.51×) | 280 ns (0.35×) | 218 ns (0.45×) | 246 ns (0.40×) |
|  | Boundary (+SetGlobal) | **197 ns** | 333 ns (0.59×) | 311 ns (0.64×) | 641 ns (0.31×) | 291 ns (0.68×) | 286 ns (0.69×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 846 ns | **<ins>144 ns (5.88×)</ins>** | — | — | — | — |
|  | CallOnly | 97.5 ns | **71.5 ns (1.36×)** | 82.2 ns (1.19×) | 167 ns (0.58×) | 110 ns (0.88×) | 117 ns (0.84×) |
|  | Boundary (+SetGlobal) | 197 ns | 193 ns (1.02×) | 198 ns (1.00×) | 528 ns (0.37×) | **179 ns (1.10×)** | 179 ns (1.10×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 548 µs | 528 µs (1.04×) | 577 µs (0.95×) | 1055 µs (0.52×) | **465 µs (1.18×)** | 469 µs (1.17×) |
|  | Transform (×1000) | 459 µs | 506 µs (0.91×) | 440 µs (1.04×) | 637 µs (0.72×) | **412 µs (1.12×)** | 421 µs (1.09×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 548 µs | 494 µs (1.11×) | 446 µs (1.23×) | 905 µs (0.61×) | **<ins>348 µs (1.57×)</ins>** | <ins>360 µs (1.52×)</ins> |
|  | Transform (×1000) | 459 µs | 359 µs (1.28×) | 309 µs (1.49×) | 521 µs (0.88×) | **<ins>295 µs (1.56×)</ins>** | <ins>295 µs (1.56×)</ins> |

[^cat-baseline]: `benchmarks/baseline`。三个独立的纯 Lua 脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行无 Go↔Lua 跨界。反映 VM 内核在最小工作量下的 dispatch / 算术 / 循环开销。
[^selftail]: P4 mono 自尾调用段内循环（issue #112 / PR #113，2026-07-10，amd64 + arm64 已交付）：`return f(...)` 且被调就是当前 closure 时，段内直接参数搬移 + 跳回入口（PUC 尾调用帧复用语义下与重进本段位级等价），不再每层付一次段退出 + Go 重入。HeavyRecursion（collatz，递归调用全是 TAILCALL）此前是全表唯一「升层比 P1 解释器还慢」的负载（amd64 1.15× vs P1 1.58×；Cobalt arm64 0.98× 直接输 gopher），本轮翻到 amd64 **4.92×** / arm64 **3.98×** / macOS **6.25×**，三平台 ~4× 改善，fib / HeavyArith 同轮逐字持平无回归。
[^cat-heavy]: `benchmarks/heavy`。三个扁平数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），故意剔除表 / 字符串 / library CALL 与其他 helper-bound 结构。反映编译档在能真正发挥的形状上的性能上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 五脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），语义单次通过与官方 lua5.1.5 做差分测试（逐字节比对）。反映调用 / 分配 / 浮点 / 表操作混合场景下的常规负载。
[^p3-gate]: P3 auto 模式带 helper 密度收益门（issue #39，2026-07-03）：热 proto 的 op 组合里 helper 往返占比过高（wasm→Go 边界成本吞掉升层收益）时拒绝升层、留在解释器。带此标注的行升层被拒，数字即解释器执行（与 P1 列的差异是采样钩子开销）。P3 force 列不受影响（force-all 绕过收益门，保差分覆盖）。
[^p3-kernel]: baseline P3 列的工作负载与其它列不同（issue #93）：顶层 chunk 是 vararg 永不升层，P3 必须测「包进内层 kernel 调 50 次」的形状；其它列跑裸顶层 ×1。因此 P3 列的倍率分母是**同形状**的 gopher 基准（`_GopherKernel`，gopher 跑一样的 kernel×50），wall time 与同行其它列不可直接比（工作量 ≈50 倍）。此前表格误拿顶层 ×1 的 gopher 当分母，把 P3 低估约 50 倍（旧表 0.06×-0.25× 实为 1.3×-3.2×）。各平台表均按修正后口径产出。
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

上面三张表由 `bench-readme-table` workflow 产出——在 GitHub Actions 上对三平台（linux/amd64、linux/arm64、darwin/arm64）各跑一轮同一份脚本，原始日志与表格落在 run artifact 里：

```bash
gh workflow run bench-readme-table.yml -f os=all -f count=3   # 三平台标准轮
gh workflow run bench-readme-table.yml --ref <branch> -f os=amd64  # 在任意分支上单平台跑
```

本地开发机跑同一个脚本（用于优化前后的 A/B 对照——同机同轮的相对比较比 hosted runner 更稳）：

```bash
./scripts/bench-readme-table.sh              # 全跑 + 直接输出 Markdown 表格
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
