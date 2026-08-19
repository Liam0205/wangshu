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

数字来自 GitHub Actions hosted runner 上的标准化基准轮（`bench-readme-table` workflow，`-benchtime=2s -count=3 -cpu=1`，取 median，2026-07-10，[run 29098511106](https://github.com/Liam0205/wangshu/actions/runs/29098511106)），三平台同一轮、同一份代码。格式为「wall time (倍率 over gopher-lua)」，倍率越大越好；**粗体**表示该行最快，<ins>下划线</ins>表示倍率 ≥ 1.5×。

> **怎么读**：hosted runner 是共享虚拟机，绝对 wall time 轮间可漂 10-20%——**请以倍率为主**，wall time 只作同轮内的量级参考。倍率的分母是 gopher-lua 在**同一轮同一台 runner** 上的实测值（分子分母同受干扰，倍率自洽），跨轮、跨平台都不要直接比 wall time。任何数字都可回溯到 run artifact 里的原始日志。

### linux/amd64（Intel Xeon Platinum 8573C）

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

### linux/arm64（Azure Cobalt 100，Neoverse-N2 类）

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 987 ns | **<ins>196 ns (5.03×)</ins>** | <ins>6016 ns (1.66×)</ins> [^p3-kernel] | 10223 ns (0.97×) [^p3-kernel] | <ins>206 ns (4.80×)</ins> | <ins>206 ns (4.80×)</ins> |
|  | Arith (Horner) | 1162 ns | **<ins>236 ns (4.92×)</ins>** | <ins>8277 ns (2.19×)</ins> [^p3-kernel] | 12312 ns (1.47×) [^p3-kernel] | <ins>252 ns (4.62×)</ins> | <ins>252 ns (4.62×)</ins> |
|  | Loop (求和循环) | 73.4 µs | **<ins>23.1 µs (3.18×)</ins>** | <ins>594 µs (6.04×)</ins> [^p3-kernel] | <ins>594 µs (6.04×)</ins> [^p3-kernel] | <ins>29.0 µs (2.53×)</ins> | <ins>29.0 µs (2.53×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 300 ms | <ins>96.5 ms (3.11×)</ins> | <ins>119 ms (2.52×)</ins> | <ins>119 ms (2.53×)</ins> | <ins>23.1 ms (13.0×)</ins> | **<ins>22.0 ms (13.7×)</ins>** |
|  | HeavyRecursion | 9.53 ms | 6.59 ms (1.45×) | 7.59 ms (1.26×) | 8.07 ms (1.18×) | **<ins>2.42 ms (3.93×)</ins>** | <ins>2.42 ms (3.93×)</ins> [^selftail] |
|  | HeavyFloatloop | 524 ms | <ins>190 ms (2.76×)</ins> | <ins>84.9 ms (6.17×)</ins> | <ins>85.0 ms (6.17×)</ins> | **<ins>37.0 ms (14.2×)</ins>** | <ins>37.1 ms (14.1×)</ins> |
| realworld small [^cat-realworld] | fib | 12.5 ms | 14.4 ms (0.87×) | 16.1 ms (0.78×) [^p3-gate] | 29.9 ms (0.42×) | <ins>1.46 ms (8.56×)</ins> [^seg2seg] | **<ins>1.46 ms (8.57×)</ins>** [^seg2seg] |
|  | binary-trees | 63.9 ms | 52.0 ms (1.23×) | 54.9 ms (1.16×) [^p3-gate] | 120 ms (0.53×) | **<ins>37.1 ms (1.72×)</ins>** | <ins>37.1 ms (1.72×)</ins> [^seg2seg] |
|  | spectral-norm | 45.5 ms | <ins>27.3 ms (1.67×)</ins> | 31.8 ms (1.43×) [^p3-gate] | 55.4 ms (0.82×) | <ins>5.62 ms (8.10×)</ins> | **<ins>5.62 ms (8.10×)</ins>** [^seg2seg] |
|  | fannkuch | 5.76 ms | 7.21 ms (0.80×) | 7.46 ms (0.77×) | 7.46 ms (0.77×) | <ins>0.83 ms (6.90×)</ins> | **<ins>0.83 ms (6.92×)</ins>** [^seg2seg] |
|  | n-body | 77.7 ms | 57.4 ms (1.35×) | 59.5 ms (1.30×) [^p3-gate] | 106 ms (0.73×) | <ins>8.86 ms (8.77×)</ins> [^math-intrinsic] | **<ins>8.86 ms (8.77×)</ins>** [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 1000 ns | **<ins>198 ns (5.05×)</ins>** | — | — | — | — |
|  | CallOnly | **132 ns** | 279 ns (0.48×) | 301 ns (0.44×) | 429 ns (0.31×) | 368 ns (0.36×) | 364 ns (0.36×) |
|  | Boundary (+SetGlobal) | **279 ns** | 460 ns (0.61×) | 488 ns (0.57×) | 902 ns (0.31×) | 479 ns (0.58×) | 482 ns (0.58×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 1000 ns | **<ins>198 ns (5.05×)</ins>** | — | — | — | — |
|  | CallOnly | 132 ns | **132 ns (1.00×)** | 147 ns (0.90×) | 216 ns (0.61×) | 202 ns (0.65×) | 204 ns (0.65×) |
|  | Boundary (+SetGlobal) | **279 ns** | 312 ns (0.89×) | 325 ns (0.86×) | 689 ns (0.41×) | 319 ns (0.87×) | 319 ns (0.87×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | **665 µs** | 812 µs (0.82×) | 810 µs (0.82×) | 1388 µs (0.48×) | 746 µs (0.89×) | 751 µs (0.88×) |
|  | Transform (×1000) | **546 µs** | 634 µs (0.86×) | 660 µs (0.83×) | 935 µs (0.58×) | 670 µs (0.81×) | 666 µs (0.82×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 665 µs | 645 µs (1.03×) | 632 µs (1.05×) | 1190 µs (0.56×) | **556 µs (1.20×)** | 563 µs (1.18×) |
|  | Transform (×1000) | 546 µs | **471 µs (1.16×)** | 505 µs (1.08×) | 727 µs (0.75×) | 489 µs (1.12×) | 481 µs (1.13×) |

### darwin/arm64（Apple M 系，macos-latest）

| 类别 | 脚本 | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 纯 VM 微基准 [^cat-baseline] | Simple (分支/比较) | 792 ns | <ins>141 ns (5.63×)</ins> | <ins>5002 ns (1.62×)</ins> [^p3-kernel] | 8899 ns (0.91×) [^p3-kernel] | **<ins>134 ns (5.93×)</ins>** | **<ins>134 ns (5.93×)</ins>** |
|  | Arith (Horner) | 909 ns | <ins>188 ns (4.82×)</ins> | <ins>6509 ns (2.31×)</ins> [^p3-kernel] | 11250 ns (1.34×) [^p3-kernel] | **<ins>169 ns (5.37×)</ins>** | **<ins>169 ns (5.37×)</ins>** |
|  | Loop (求和循环) | 56.5 µs | **<ins>18.0 µs (3.14×)</ins>** | <ins>850 µs (3.24×)</ins> [^p3-kernel] | <ins>820 µs (3.36×)</ins> [^p3-kernel] | <ins>21.1 µs (2.68×)</ins> | <ins>21.1 µs (2.68×)</ins> |
| heavy 内核 [^cat-heavy] | HeavyArith | 214 ms | <ins>95.5 ms (2.24×)</ins> | <ins>97.0 ms (2.21×)</ins> | <ins>97.4 ms (2.20×)</ins> | **<ins>34.2 ms (6.26×)</ins>** | <ins>35.2 ms (6.08×)</ins> |
|  | HeavyRecursion | 10.9 ms | <ins>5.24 ms (2.08×)</ins> | <ins>6.38 ms (1.71×)</ins> | <ins>6.84 ms (1.59×)</ins> | <ins>1.76 ms (6.19×)</ins> | **<ins>1.75 ms (6.23×)</ins>** [^selftail] |
|  | HeavyFloatloop | 423 ms | <ins>144 ms (2.95×)</ins> | <ins>118 ms (3.59×)</ins> | <ins>119 ms (3.56×)</ins> | **<ins>37.3 ms (11.3×)</ins>** | <ins>37.6 ms (11.3×)</ins> |
| realworld small [^cat-realworld] | fib | 10.4 ms | 12.2 ms (0.85×) | 13.0 ms (0.80×) [^p3-gate] | 25.2 ms (0.41×) | **<ins>0.99 ms (10.5×)</ins>** [^seg2seg] | <ins>1.00 ms (10.4×)</ins> [^seg2seg] |
|  | binary-trees | 59.8 ms | 47.0 ms (1.27×) | 41.6 ms (1.44×) [^p3-gate] | 94.1 ms (0.64×) | **<ins>26.0 ms (2.30×)</ins>** | <ins>26.0 ms (2.30×)</ins> [^seg2seg] |
|  | spectral-norm | 36.1 ms | <ins>21.8 ms (1.65×)</ins> | <ins>22.6 ms (1.59×)</ins> [^p3-gate] | 45.6 ms (0.79×) | **<ins>4.51 ms (8.01×)</ins>** | <ins>4.63 ms (7.80×)</ins> [^seg2seg] |
|  | fannkuch | 4.84 ms | 6.75 ms (0.72×) | 6.18 ms (0.78×) | 6.20 ms (0.78×) | **<ins>0.69 ms (6.99×)</ins>** | <ins>0.71 ms (6.82×)</ins> [^seg2seg] |
|  | n-body | 65.5 ms | 45.3 ms (1.45×) | 44.3 ms (1.48×) [^p3-gate] | 79.4 ms (0.83×) | **<ins>6.88 ms (9.52×)</ins>** [^math-intrinsic] | <ins>6.91 ms (9.48×)</ins> [^math-intrinsic] |
| 边界 mini · Call [^cat-mini] | PureVM | 908 ns | **<ins>145 ns (6.28×)</ins>** | — | — | — | — |
|  | CallOnly | **97.3 ns** | 190 ns (0.51×) | 178 ns (0.55×) | 307 ns (0.32×) | 223 ns (0.44×) | 224 ns (0.43×) |
|  | Boundary (+SetGlobal) | **212 ns** | 323 ns (0.66×) | 297 ns (0.72×) | 719 ns (0.30×) | 308 ns (0.69×) | 298 ns (0.71×) |
| 边界 mini · CallInto [^cat-mini] | PureVM | 908 ns | **<ins>145 ns (6.28×)</ins>** | — | — | — | — |
|  | CallOnly | 97.3 ns | **75.4 ns (1.29×)** | 79.6 ns (1.22×) | 170 ns (0.57×) | 112 ns (0.87×) | 113 ns (0.86×) |
|  | Boundary (+SetGlobal) | 212 ns | 196 ns (1.08×) | 188 ns (1.13×) | 516 ns (0.41×) | **177 ns (1.20×)** | 178 ns (1.20×) |
| 真实负载 · Call [^cat-embed] | Predicate (×1000) | 566 µs | 581 µs (0.97×) | 522 µs (1.08×) | 967 µs (0.59×) | **472 µs (1.20×)** | 480 µs (1.18×) |
|  | Transform (×1000) | 423 µs | **406 µs (1.04×)** | 407 µs (1.04×) | 610 µs (0.69×) | 418 µs (1.01×) | 415 µs (1.02×) |
| 真实负载 · CallInto [^cat-embed] | Predicate (×1000) | 566 µs | 442 µs (1.28×) | 432 µs (1.31×) | 856 µs (0.66×) | **<ins>348 µs (1.63×)</ins>** | <ins>350 µs (1.62×)</ins> |
|  | Transform (×1000) | 423 µs | 330 µs (1.28×) | 301 µs (1.40×) | 498 µs (0.85×) | **293 µs (1.44×)** | 294 µs (1.44×) |

[^cat-baseline]: `benchmarks/baseline`。三个独立的纯 Lua 脚本（Simple 分支比较、Arith 六阶 Horner 多项式、Loop 求和 1..N），单次执行无 Go↔Lua 跨界。反映 VM 内核在最小工作量下的 dispatch / 算术 / 循环开销。
[^selftail]: P4 mono 自尾调用段内循环（issue #112 / PR #113，2026-07-10，amd64 + arm64 已交付）：`return f(...)` 且被调就是当前 closure 时，段内直接参数搬移 + 跳回入口（PUC 尾调用帧复用语义下与重进本段位级等价），不再每层付一次段退出 + Go 重入。HeavyRecursion（collatz，递归调用全是 TAILCALL）此前是全表唯一「升层比 P1 解释器还慢」的负载（amd64 1.15× vs P1 1.58×；Cobalt arm64 0.98× 直接输 gopher），本轮翻到 amd64 **4.93×** / arm64 **3.93×** / macOS **6.23×**，三平台 ~4× 改善，fib / HeavyArith 同轮逐字持平无回归。
[^cat-heavy]: `benchmarks/heavy`。三个扁平数值内核（HeavyArith 纯算术、HeavyRecursion 自递归、HeavyFloatloop 嵌套浮点循环），故意剔除表 / 字符串 / library CALL 与其他 helper-bound 结构。反映编译档在能真正发挥的形状上的性能上限。
[^cat-realworld]: `benchmarks/realworld`。benchmark-game 五脚本（fib / binary-trees / spectral-norm / fannkuch / n-body），语义单次通过与官方 lua5.1.5 做差分测试（逐字节比对）。反映调用 / 分配 / 浮点 / 表操作混合场景下的常规负载。
[^p3-gate]: P3 auto 模式带 helper 密度收益门（issue #39，2026-07-03）：热 proto 的 op 组合里 helper 往返占比过高（wasm→Go 边界成本吞掉升层收益）时拒绝升层、留在解释器。带此标注的行升层被拒，数字即解释器执行（与 P1 列的差异是采样钩子开销）。P3 force 列不受影响（force-all 绕过收益门，保差分覆盖）。
[^p3-kernel]: baseline P3 列的工作负载与其它列不同（issue #93）：顶层 chunk 是 vararg 永不升层，P3 必须测「包进内层 kernel 调 50 次」的形状；其它列跑裸顶层 ×1。因此 P3 列的倍率分母是**同形状**的 gopher 基准（`_GopherKernel`，gopher 跑一样的 kernel×50），wall time 与同行其它列不可直接比（工作量 ≈50 倍）。此前表格误拿顶层 ×1 的 gopher 当分母，把 P3 低估约 50 倍（旧表 0.06×-0.25× 实为 1.3×-3.2×）。各平台表均按修正后口径产出。
[^seg2seg]: P4 段到段 CALL 直跳（issue #50，2026-07-04，amd64 + arm64 已交付）：自递归 / arith-callee（fib 形状）之前每次调用付一次跨界往返税（mmap RET → Go dispatch → host.CallBaseline → mmap 重入），现在 caller 段直接 `call` 进 callee 段、callee 段内组拆帧 + native 递归、全程不出 mmap。fib / spectral-norm / fannkuch 等自递归与 arith-callee 负载因此翻到两位数倍率；binary-trees 的 `check`（自递归 + GETTABLE ArrayHit 读表）随 ArrayHit 站点纳入段到段资格而解锁,剩余瓶颈是 bottomup 的分配。arm64 同批交付,双架构收益形状一致（当前各平台数字见上方三表）。
[^math-intrinsic]: P4 math.* intrinsic emission（issue #77 / PR #87，2026-07-08，amd64 + arm64 已交付）：CALL 站点 IC 观察到被调是已知纯数值 host closure（sqrt / floor / ceil / abs / max / min）时，段内直接发射硬件指令（amd64 SQRTSD / ROUNDSD 等）而不再 exit-reason 往返到 Go host closure。n-body 的稳态几乎全是 `sqrt(dist2)` 调用，之前既因每次 sqrt 付一次跨界往返、又因 CALL 密度门把带 sqrt 的热函数误判成「调用太密、升层不划算」而拒绝升层，两头卡住（P4 ≈ P1，1.41×）；#77 一并修好后（intrinsic CALL 不计入密度门 + sqrt 内联发射），n-body 从 ~P1 水平翻到两位数倍率，双架构一致（arm64 走 FSQRT / FRINTM 等对应指令），结果与解释器逐字节一致（含 NaN / Inf / ±0）。当前各平台数字见上方三表。
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

脚本会自动探测 `goos/goarch`，同一条命令在任何平台复现对应表。

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

望舒实现的是 Lua 5.1 核心语言（与 LuaJIT 一致的语法层），覆盖 Lua 5.1 参考手册中定义的 38 个字节码 opcode 除 `VARARG` 外的全部（`VARARG` 在 P3/P4 编译层永不接入，走 P1 解释器路径），以及 stdlib 的 base / string / table / math / os / coroutine 全部必做面。**io 库提供 `io.write` / `io.read` / `io.lines` 与三个标准流**（#205，2026-07-29）：`io.stdout` / `io.stdin` / `io.stderr` 是**真 file-handle userdata**，`type()` 报 `userdata` 与官方一致，共享一张 metatable 提供 `:write` / `:close` / `:read` / `:lines` / `:flush`；仍缺的是需要真实文件的部分——`io.open` / `io.popen` / `io.tmpfile` / `f:seek`，所以 `io.lines(filename)` 抬错而不是静默返回一个空迭代器。`debug` 库提供 `traceback` 与 `getinfo`，其余（`sethook` / `getlocal` / `setlocal` / `getupvalue` / `setupvalue` / `getregistry`）仍不提供，它们需要解释器没有暴露的内省钩子；`getinfo` 只填能诚实回答的字段（`currentline` / `source` / `short_src` / `what` / `func`），`nups` / `activelines` / `namewhat` 宁缺不假造。缺口说明见 [10](docs/design/p1-interpreter/10-stdlib.md) §10.1.1。

正确性验证五层：

1. **官方测试套 byte-equal**：13 个 5.1.5 官方文件（vararg / sort / pm 整文件 + 其余截至豁免线）逐字节一致。
2. **手册逐节 probe**：100 项手册特性 + 12 项边角 + 29 条错误消息（含行号断言）+ 71 条种子用例逐字节一致。
3. **差分随机 fuzz**：nightly-diff-fuzz workflow 每晚 2M 条随机脚本与 Lua 5.1.5 oracle 做差分测试（P1 + P3 + P4 三档并行）。读它的失败时先确认那几个 fuzz 步骤**真的执行过**——准备类步骤（装 oracle、取包）失败会让它们被 skip，那一轮报红而实际什么都没测，与「跑了并且发现分歧」在 Actions 页面上是同一个红叉（#236–#241，见下）。
4. **三方差分**：crescent（P1）vs gibbous（P3/P4）在 P4 build 下每 CI 跑一次 byte-equal，PR #29/#31 tri-platform matrix 全绿。
5. **cgo 内嵌 oracle 差分 fuzz**：`internal/oracle` 把官方 5.1.5 源码经 cgo 嵌进测试二进制（build tag `wangshu_oracle_cgo`，默认 build 保持零 cgo），`FuzzOracleDiff` 用 go-fuzz 变异的**任意不规则源码**（不限于 generator 的规整脚本）在进程内做差分比对：两侧跑同一段 prelude（输出捕获 + 确定性 stub + 排序迭代 + 白名单裁剪）后**比输出经地址归一后 byte-equal**，**没有任何接受差异的路径**（地址归一与实现常数类护栏跳过属于另外两类，见下）。PR 门禁 60s 冒烟（oracle-smoke job），nightly p1 腿连续跑 45 分钟。上线首日（长时间 fuzz + 配套的 stdlib 全函数 × 退化参数系统性扫描）抓出 **32 处** P1 与官方的语义分歧并全部修复——覆盖 string 库 number 自动转串、`__tostring` 原样透传、协程错误消息 type 名、未知转义字面放行、长括号嵌套 deprecation、`upper`/`lower` 按字节、`string.format` verb 集/无符号转型/`%c` NUL 截断/scanformat 硬限、编译期常量折叠 ±0/NaN 规则与 RK 物化序、`tonumber` 的 C99 strtod 接受面（hex float / inf / nan）、表构造器源码序覆盖语义、真实共享 string 元表（`getmetatable("")` 可改且全部元方法全局生效）、`load` 只收 reader 函数、`os.time` 表协议等；上线后持续巡检又修 #170/#171（`string.format` NaN/Inf 渲染对齐 glibc，PR #172）/ #174/#175（`toNumberStr` 走 `crescent.ParseLuaNumber`，PR #176）/ #177（P4 shape-template FORLOOP deopt 补 preempt 与 body 副作用，PR #178）/ **#192/#193/#194/#196 那一轮**（2026-07-28，四个 issue 修出**六个根因**：C99 `nan(n-char-sequence)` 前缀、`tonumber(x, 10)` 整条路由错（一个原因造出六条分歧，五条没有 issue）、`%d`/`%i` 精度 0 配值 0 丢符号、`table.insert` 的 5.1 无边界检查语义 + `table.concat` 错误文本、`string.char` 的两步转换、`strtoul` 的无符号取反与溢出饱和（原先那条「已登记豁免」没有任何代码实现它）；另有 45 秒 fuzz 冒烟撞出 table 库缺 `, got no value` 从句）/ **#197/#198/#199 那一轮**（2026-07-28：`error(msg, level)` 的位置前缀真的按 level 选帧——host 边界算**一个** level 而不是终点，且一个边界可能代表多个叠起来的 host 帧（`pcall(pcall,f)` 在 PUC 是两个 C 帧），所以每帧的 host 帧计数存进了 `callInfo` 的打包位；`%g` 补上 C 的默认精度 6（显式精度本来就对，`%e`/`%f` 的默认也本来就对，只有 `%g` 的默认不同）；gmatch 的前导 `^` 是普通字符不是锚（PUC 的 `gmatch_aux` 没有 anchor 处理）；`assert` 第二参数走 `luaL_optstring` 只收 string/number；`math.modf` 对无穷、`math.frexp` 在 DBL_MAX 附近、`math.rad` 溢出三处 Go math 包与 C 的差；`io.write` 返回布尔成功标志（issue 里写的「返回文件句柄」是错的，那是 5.2+）；`os.date` 从六个指令扩到完整指令循环 + `*t` 与 `!` 前缀）/ **#232/#233/#234 那一轮**（2026-08-05，定时巡检的第一次实际处理轮，三个 crasher 全部真复现，而 **#232 与 #233 是同一个根因的两种可见度**——都是引用值地址：#232 是地址归一的锚点用 `\b` 而 word 字符包含数字，`io.write(0)print(print)` 产生的 `0function: 0x...` 整段逃过归一化；#233 是脚本**测量**地址的长度（`#tostring(t)` PUC 21 对望舒 17），长度在归一化之前就分歧、`NormalizeOutput` 物理上到不了，所以在 oracle 的 prelude 渲染处对齐——**归一化管值、渲染处管宽度**；#234 是引擎侧 gsub 替换串的 `%` 转义，`add_s` 的分支有三个出口加一个越界读，望舒只对了两个，「`%` 后非数字原样吐出」那一半是既有缺陷）/ **#244 那一轮**（2026-08-11，**崩的是 oracle 不是望舒**：`A(unpack({},0X80000000))` 让内嵌 oracle 吃 SIGSEGV（栈迹在 cgo 里）、真的 `lua5.1` 二进制同样 dumped core，而望舒对整段抬 `too many results to unpack`、行为正确且从不崩——PUC 的 `luaB_unpack` 把 `n = e - i + 1` 算在 int 上并用 `n <= 0` 检查,而**这个减法本身是有符号溢出 UB**——gcc -O2 把该检查当不可达整段删掉,`lua_checkstack` 随后收到负的 `size` 并照单接受(`lapi.c` 两个比较都为假),于是崩掉;同一份源码在 -O0 下干净抬错,**所以这个崩溃依赖优化等级**。(此前这里写「回绕成正的巨大值绕过检查」是错的:`i <= e` 时 int32 回绕恒 `<= 0`,那个检查本该拦住所有情形——错的机制不影响守卫条件,但会让读者以为与优化无关。)处置与其余 PUC UB range 一致：差分侧跳过，**会死的 oracle 不能当参照**。分诊纪律：差分 harness 报 crash 时先读栈迹属于哪一侧、并拿真的参照实现二进制跑一次。**守卫经四轮审计收口**:区间读 `i` 与 `e` 两个量(`i32 <= e32 且 (e32-i32+1) > INT_MAX`,`e` 默认 `#t`)、窄化走 `luaL_checkint` 自己那条链(`__ckint0`,而不是手搓一份)、`math.floor`/`tonumber` 在脚本运行前捕获成 local(读活全局时一个 `math.floor=function() return 0 end` 的输入就能把守卫算成 0、让 oracle 段错误 —— **守卫可以被它所守卫的输入绕开**)、非表首参不再被跳(`luaL_checktype` 先抬,两侧一致)。用例见 `internal/oracle/unpack_guard_test.go`(13 条必须 skip 含 5 条绕过尝试、12 条必须仍比较,两个方向都用变异确认),见 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9f。

**NaN 符号渲染差异在 oracle 侧消除，不在比较侧豁免**：`0/0` 转文本时 glibc 的 printf 按符号位打 `-nan`，wangshu 打 `nan`，差一个字节；IEEE 754 不赋 NaN 符号位数值语义，两种输出都合规。oracle 是仓库自己 vendor 并用 cgo 编译、只为做差分基准而存在的（且已经为确定性 stub 掉 `os.time`/`math.random`/`pairs` 顺序），所以在 `internal/oracle/lua515.c` 里覆盖 `lua_number2str`、并对 `lstrlib.c` 的 include 局部 shadow `sprintf`，把 NaN 渲染的符号去掉（保持字段宽度：`sprintf` 已经补过 padding，且 buffer 分不清前导空格是 padding 还是空格 flag 的符号，所以按 format spec 里的声明宽度重新补齐；符号去掉后 glibc 的 `+`/空格 flag 开始作用于 NaN，所以剥的是任何符号字符；Inf 保留符号，脚本自己写的 `"-nan"` 字面量不改）；vendored 源码保持与记录的 sha256 逐字节一致。同轮清掉产品侧两处 glibc 模仿（`internal/stdlib/stringlib.go`：`string.format` 原本对大写 verb 硬编码 `-NAN`，让 `%e` 与 `%E` 自相矛盾；小写 NaN 原本按「声明宽度减一」补齐以复现 glibc 保留但不显示的符号列，让 `%5f` 与 `%5E` 补齐方式不一致）——两处都只为让 oracle 一致而存在，而 arm64 的 glibc 与 x86 还不同，模仿本来就不可移植；现在 NaN 在所有 verb 下都不带符号、都按完整声明宽度补齐，wangshu 自身也一致了。**曾经的设计与它为何失败**（#173 引入、#184/#185 延伸，已全部删除）：旧设计把这个差异当作「不可比的类」，在 harness 侧识别并豁免（`__nan_spans` 区间 FIFO + 多行 readout header + `CompareOutput` 的 span-anchored 分类 + 一整层入口拦截）。这条路不可能收敛：那个符号字节一旦进入字符串就是普通数据，`#`、`==`、`string.sub`、`..`、算术能把它搬到任何地方——`string.len(0/0)` 是 4 对 3，`string.len(0/0)*100` 是 400 对 300，输出里连一个 nan 字节都不剩，没有任何可锚定的东西；任何下游识别规则都必须读一个两侧不相等的量，所以都做不到对称。新设计约少 550 行，且两个 crasher 与整个「泄漏成数字」的家族现在是普通的 equal 而不是 skip，覆盖面是增加的（那些输入以前会连同同一次运行里的真差异一起被丢掉）。**保留的两类豁免性质不同**：地址归一（`table: 0x...`）是两个引擎堆布局本来不同、没有「正确值」可对齐，只能在比较时归一；实现常数类护栏（`stack overflow`、`too many syntax levels`、200 local variables 等）是两侧独立选定的实现限制，改 oracle 的常数去凑 wangshu 会让它不再是独立基准，只能 skip。判据：差异如果来自「同一个抽象值的不同书写方式」，在渲染处消除；如果来自「两侧本来就是不同的东西」，在比较时归一或跳过。**后续验证**：nightly 在四个不同日期自动开出的 crasher #187 / #188 / #189 / #190（`string.format("%q",0%0)`、`string.format((0))<string.format((0%0))`、`string.format("%q",(0%0))`、`string.format("+% E",-(0%0))`）全部是这同一个根因的表现，被上面的渲染处消除一起解决，一行代码没改；双向验证确认它们在 redesign 之前的 base 上全部 FAIL、在现在的实现上全部以真正的逐字节 equal 通过而不是 skip。四条 reproducer 加 10 条延伸 seed 入 `testdata/fuzz/FuzzOracleDiff/`（共 73 个），理由是它们触到三个已有 seed 到不了的写法：`%q` 作用于 NaN（引用渲染结果而不做浮点格式化）、比较两个 `string.format` 的返回值（差异变成一个 boolean，输出里根本没有 NaN 文本）、多个符号 flag 同时出现。另扫了 236 + 140 种同族写法（`%q` × 宽度、比较运算符 × format 与 tostring 结果、flag 组合 × 浮点 verb；产生 NaN 的方式 × 消费其文本的方式）确认整个家族零差异。**「在渲染处消除」在 2026-08-05 第二次被用上（#233）**：`#tostring(t)` 量的是地址的**宽度**（PUC `%p` 12 位给 21、望舒 `0x%08x` 8 位给 17），长度在归一化之前就分歧——与 `string.len(0/0)` 4 对 3 完全一样的机制，所以 prelude 包一层 `tostring` 把 PUC 自己的地址渲染成望舒的 8 位，望舒侧的宽度成为一份契约由 `fuzz_234_test.go::TestAddressLengthIsComparable` 钉住。**所以那条判据要读细一格**：判去「比较时归一」的那一类里仍可能有一个子量必须在渲染侧对齐——地址的**值**只能归一，地址的**宽度**是渲染选择，会被 `#` 测量成普通数字。

**豁免清单**（权威来源是 `test/difftest/corners_test.go::exemptions`，`go test -v -run TestExemptions_Documented` 可审计；下表按类别归并，行数与该清单的条目数不是一对一，所以这里不写条目总数——先前写的「共 15 项」与两边都对不上。另有 `string.char` 的 C UB 一项，执行体在差分 harness 侧 `internal/oracle/prelude.go` 的 sentinel 而不在这张表里）：

| 类别 | 具体项 | 豁免原因 |
| --- | --- | --- |
| Lua 5.2+ 特性 | `rawlen`、`table.pack` / `table.move` | 与 5.1 手册不符 |
| Lua 5.3+ 特性 | `math.tointeger` / `type` / `maxinteger` / `mininteger` | 整数类型属 5.3 特性 |
| 嵌入式安全 | `os.execute`、`io.popen` / `io.tmpfile`、`os.exit` 真退出、`loadfile` / `dofile` 默认禁用 | 嵌入式 VM 不让脚本跑 shell / 越权文件系统 |
| 真实文件 IO | `io.open` / `f:seek`（三个标准流与 `io.read` / `io.lines` 已提供） | 需要文件系统访问控制 + `__gc` 关文件那一环，P1 不做 |
| Debug 接口 | `debug.sethook` / `getlocal` / `setlocal` / `getupvalue` / `setupvalue` / `getregistry` | 需要解释器内部 hook，成本收益不划算 |
| 模块系统 | `require` / `module` / `package` | 嵌入式宿主经 `Compile` 提供脚本，不从文件系统 require |
| 字节码序列化 | `string.dump` | 自定义 ISA 不兼容官方 `.luc` |
| 环境操作 | `getfenv` / `setfenv` | 与 P2 分层桥 F4 形状分析冲突 |
| C 未定义行为 | `string.char` 的越界 double→int 转换 | `luaL_checkint` 是 `(int)luaL_checkinteger`：double 先变 `lua_Integer` 再窄化成 int，越界与 NaN 落进 C UB，两个官方 build 互不一致（x86-64 `cvttsd2si` → `INT64_MIN`，低 32 位 0，PUC 接受得 byte 0；arm64 `FCVTZS` 把 `+inf` 饱和到 `INT64_MAX`，低 32 位 -1，PUC 报错）。产品侧钉 x86-64 结果（与 `%u`/`%x`/`%o` 的 `cUnsignedCast` 同一手法），差分侧跳过这段区间；in-range 的值（含 `2^53`）照旧比对 |
| 灾难性回溯 | pattern 灾难回溯 `.*.+%A*x` | 回溯预算 `1<<20` 步硬限，报 `pattern too complex`（嵌入式防挂起） |
| 增量 GC | `collectgarbage("step"/"setstepmul")` | STW GC 无增量调参，占位返回 |

其余「存在但不逐字节比」的项（`collectgarbage("count")` / `gcinfo` / `os.time` / `os.clock` / `os.date("%Y")` / `io.write` / `loadfile` 返错格式）由 `TestApprox_ExistenceOnly` 只断言返回值格式不比数值——这一类不是「不可比」而是「值随运行环境变」（时间、内存、文件系统），所以 `os.date` 的**指令输出**并不在这一类里，它已经逐字节核对过（2026-07-28）。

**2026-07-28 变动（`tonumber` 负数回绕不再是豁免）**：`tonumber('-ff',16)` 原先被列为「C 未定义行为」豁免、有意取直觉语义返 `-255`。这个分类是错的：C `strtoul` 的无符号取反与溢出饱和是**有定义的** C，不是 UB，所以应该对齐而不是豁免——现在 `tonumber("-7",8)` 得 2^64-7、`("-ff",16)` 得 2^64-255、20 个 `f` 配 base 16 饱和到 2^64-1，与官方一致。更要紧的是那条豁免**从来没有任何代码实现它**（只是 `internal/stdlib/stdlib.go` 里的一句注释），所以分歧一直是活的，nightly 随时可能把它开成 crasher。`test/difftest/corners_test.go::exemptions` 里对应的那条已作废条目同轮摘掉了。判据（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9b）：**先分清那个 C 行为是有定义还是 UB，再决定对齐还是跳过**；并且「已豁免」的声明必须能指向执行它的代码或测试。

**2026-07-28 变动（`error` level 的 skip 撤掉 + 刻意不对齐 `print` 的 NUL 截断）**：① 差分 harness 里为 #197（`error(msg, level)` 选帧错误）加的那条 skip 覆盖**任何提到 error 第二参数的输入**，#197 修好后同轮撤掉——24 种 error level 写法现在**零 skip** 参与比对，只剩「非有限 level」跳过（那是 `luaL_checkint` 窄化的真 UB）。纪律：**为某个 bug 加的 skip 必须随那个 bug 一起撤掉**，留着的 skip 会把一整类输入静默挡在比对之外，而且读起来像已经处理过了（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9c）。② `print` 对内嵌 NUL **不截断**，这是刻意偏离：PUC 的 `luaB_print` 用 `fputs` 停在第一个 NUL，而 PUC **自己的** `io.write` 用带长度的 `fwrite` 不截断——参照实现在同一件事上内部不一致，而 5.1 手册明确字符串是 8-bit clean 可含 NUL，所以那个截断是 C 调用的产物，对齐它等于故意丢用户数据（差分侧不表现为分歧：harness 用自己的累积器捕获输出，不经 C `FILE*`）。判据：**参照实现自相矛盾时按语言规范选，并把偏离记进代码注释**；这与「参数求值顺序在 C 里是未指定、两侧都不对齐」同属一类（四格判据见 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9b）。

**2026-07-28 变动（产品上限与 harness skip 拆成两个数）**：`table.insert` 的移位跨度上限此前产品侧与差分 harness 侧读**同一个数**（2^27）。这个写法被 nightly 开成**三个** crasher issue（#203 是第三个）：位置窄化成约 100M 的移位跨度、刚好落在 2^27 之下，三条都**正确且对称**——两侧引擎都做这个移位、结果一致——只是耗数秒，而 fuzz coordinator 启动时**并行重放整个 seed corpus**，这样的 seed 会把 worker 弄死。前两个都按「重 workload 走 `test/regression/`」挪走了，每一次单看都对；第三次说明该修的不是再挪一个 seed，而是**被接受的区间宽到 fuzzer 会持续探索它**。现在两个阈值故意不同，因为它们回答不同的问题：产品侧 `tableInsertShiftCap` 留在 2^27，那是**正确性**边界（在它之下望舒必须做这个移位，因为 lua5.1 会做；数值本身是实测参照实现代价定下来的）；harness 侧的 skip 降到 2^20，纯粹是「什么样的输入能待在并行 corpus 重放里」的资源问题。判据：**一个阈值的正确数值由它防的东西决定，两处读同一个常数而防的东西不同就该是两个常数**——与「skip 与产品规则必须逐字对应」不冲突，那条管判据的**形状**、这条管**数值**，形状一致、数值分开（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9d）。`test/regression` 仍然串行跑一次真实的 100M 元素移位，所以「这个工作确实被完成而不是被上限拒绝」没有丢覆盖。同轮另修两处 stdlib 语义（#201/#202）：`unpack` 的上限是 `LUAI_MAXCSTACK` **减去参数个数**而不是固定 8000（PUC 的 `lua_checkstack` 拒绝条件含 `L->top - L->base + size`，对 C 函数那就是参数个数；阈值随参数个数变化正是排除「硬编码 7997」的那条证据），以及 `error` 自己作为被调 host 函数抛错时位置前缀完全丢失（它经 `callHost` 直接返回、不进解释器循环，所以 #197 那套按帧计数在这条路径上从未被调用；改在边界标注，level 1 是 host raiser 的**调用者**，且只对显式 `level >= 2` 生效——给所有 host 错误标注会给 PUC 留裸的库错误加上前缀）。

**2026-07-29 变动（file-handle userdata + `debug` 子集 + `string.byte` 的上限）**：① **#205 交付三个标准流与 `io.read`**。上一轮撤回的根因只有一行：原型直接调 `object.AllocUserdata`、**跳过了 collector 的 `LinkSweep`**，对象 header 里既没有颜色也没有 sweep 链，收集器根本看不见它，创建句柄后一次 `collectgarbage("collect")` 就以 arena 索引越界 panic 而句柄仍从 `io` 表可达——`internal/crescent/alloc.go` 里每个分配器都是 `AllocX` + `LinkSweep` + `AllocCharge` **三件一起**，现在 `State.NewUserdata` 把它固定成唯一入口（记在 [06](docs/design/p1-interpreter/06-memory-gc.md) §2.1.1 与 [10](docs/design/p1-interpreter/10-stdlib.md) §10.2.1）。必须是真 userdata 而不是一张表，因为 PUC 报的是 `userdata`、`type()` 会露馅；为此补了两处 VM 缺口（`metaFieldOfValue` 不认 userdata，所以 userdata 的 `__index` 从来没被查过；`indexWithMeta` 压根没有 userdata 分支），`getmetatable` 对 userdata 也开始遵守 `__metatable`。56 个探针与系统 `lua5.1` 比对抓到四条细节：非句柄 receiver 报 `FILE* expected, got X` 而不是通用消息、读一个只写句柄返回 PUC 的 errno 三元组 `(nil, "Bad file descriptor", 9)` 而不是单个 nil、`debug.traceback` 传**显式 nil** 返回 nil（显式 nil 是一个值、不是「参数缺失」）、`getinfo` 无参时报 `function or level expected`（这个词序）；54/56 一致，剩下 2 个是故意不提供的 `debug.sethook` / `getlocal`。`io.read` 的验证只能用真实二进制——`go test` 不把 stdin 转发给测试进程，在 `go test` 里写的探针全部返回 nil，那量到的是 harness 而不是代码。② **差分 harness 的捕获累加器现在也覆盖 file-handle 的 `:write`**：加了 `io.stdout` 之后 `io.stdout:write("A")` 会经一条 harness **没有捕获**的路径输出，那段文本在**两侧**捕获里都不存在——两侧仍然一致、不报分歧，而比较已经不覆盖这条路径写出的任何东西，这是「因为错误的原因而变绿」，与一条掩盖整类输入的 skip 是同一种失效方式（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §3.1.1）。③ **#206 `string.byte` 补上 `lua_checkstack` 上限**：原先完全没有上限，`string.byte(string.rep("a",9000),1,8000)` 返回 8000 个值而 PUC 抬错；上限与 `unpack` 一样是 `8000 - nargs`，但**消息被包了一层**——`luaL_checkstack` 把调用者给的文本套成 `stack overflow (%s)`，所以 PUC 输出的是 `stack overflow (string slice too long)` 而不是裸串（`unpack` 那边不暴露这件事，因为 `luaB_unpack` 用的是直出的 `luaL_error`）。判据：抄一条 PUC 错误消息时先看它是经哪个 `luaL_*` 抬出来的（[10](docs/design/p1-interpreter/10-stdlib.md) §5.4c）。④ **#208 一行代码没改**：它的 fuzz run 跑在上一轮把 harness skip 降到 2^20 **之前**的 commit 上，现在这个 seed 0.00 秒就跳过，作为回归防线留在 corpus 里——上一轮「该改的是被接受的区间，不是再挪一个 seed」的正向结算。

**2026-07-29 变动（把那个阈值拆分固定下来，零产品代码改动）**：#209 是 insert 移位那一写法的**第三个** nightly crasher，它本身也是过期的（fuzz run 跑在把 harness skip 降到 2^20 之前的 commit 上，现在 0.00 秒就跳过，seed 入 corpus 作回归防线）。但同一写法在三个不同夜晚被开成 issue 这件事指向的不是产品缺陷——根因上一轮已经修对，**缺的是一个把那个决定固定下来的测试**：入 corpus 的 seed 只能表达「这个输入不崩」，它表达不了「这两个阈值还是两个数」，两个数被合回一个之后现有 seed 全都照旧通过。现在 `test/regression/insert_shift_cost_test.go::TestInsertShiftThresholdsStayDistinct` 断这件事，而且**按行为断言而不是比对字面量**（两个常数一个在 `internal/stdlib` 一个在 `internal/oracle/prelude.go`，都不导出、也不同包，写死 `1<<20`/`1<<27` 只会与任一侧各自漂移）：落在两个阈值**之间**的跨度必须由产品**执行**（不被上限拒绝）**且必须便宜**（skip 的取值就建立在这一段便宜这个假设上），两个方向都断（只断前者时两个数合到 2^27 仍然全绿）。判据：**修完一个反复出现的问题之后，要问「有什么东西固定住这个修法吗」**——自查办法是假设本次改动被整段 revert，现有测试会不会红（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9d）。同轮另确认昂贵的那一段只在「远低于索引 1」这一侧（大的正位置、`table.remove` 两个方向、中等跨度实测都是 1-2 ms 且两侧对称），并且没有按文件名模式清理 corpus——四个看起来同类的 `table.insert(t,4...)` 里 `4294967298` 窄化成 **+2**，是一个普通的正位置插入、根本不走 skip，按模式合并会删掉唯一的用例。

**2026-08-02 变动（八个 nightly crasher、六个 seed、三个真缺陷）**：#212–#219 八个 issue 的 fuzz run 全跑在同一个旧 commit 上，与上一轮（#209）字面事实几乎一样，但**结论相反**——实际重放之后五个如实复现。判据：**版本核对回答的是「要不要在这个 commit 上复现一遍」这个成本问题，它不回答「有没有缺陷」**，nightly 每晚跑的就是当时的 master，所以「全落在一个旧 commit 上」是常态而不是线索。另外八个 issue 只有**六个不同的 seed**（#212/#215 与 #217/#219 各是同一个 hash，同一个输入在两个夜晚各被最小化一次就会开出两个 issue），先算 hash 再分组比逐条查根因便宜得多。三个真缺陷：① **`error()` 的 level 参数没做类型检查**——原先写成「转换成功才用，失败静默保留默认值 1」，而 PUC 的 `luaL_optint` 对**显式传了一个转不动的值**是抬错的，于是 `error("", 0>0)` 报空消息而 lua5.1 报 `bad argument #2 to 'error' (number expected, got boolean)`；`luaL_opt*` 是**两条**规则，缺省或显式 nil 取默认值、显式非法值抬错，而数字字符串（`"2"`）仍然强制转换（[09](docs/design/p1-interpreter/09-errors-pcall.md) §3.1a）。② **调用的行号取的是被调用表达式那一行，不是参数列表那一行**——`(0` 换行 `)()` 在 lua5.1 里报第 2 行（`()` 所在的行）而望舒报第 1 行，因为 `CallExpr.Line` 用了被调用表达式的起始行；**只有跨行的被调用表达式才有差别**，单行时两者相同，所以这个偏差只能靠 fuzz 撞出来（[09](docs/design/p1-interpreter/09-errors-pcall.md) §3.5.1）。③ **`string.gsub` 的 repl 类型是惰性校验的**——不是少了一个检查，是**检查放错了位置**：类型判断写在替换循环里面，而第 4 个参数把循环次数压成 0，于是循环一次都没跑、那个非法参数从来没被看到，`gsub("", "", nil, .0)` 成功返回而 lua5.1 抬 `bad argument #3`；PUC 是在循环**之前**用 `luaL_argcheck` 校验的。判据：**一个校验在控制流里的位置也是要照抄的东西**，「循环之前校验一次」与「每次迭代校验」只在「参数非法 + 循环执行零次」这一种输入上分岔，而这正是 fuzzer 擅长构造的（[10](docs/design/p1-interpreter/10-stdlib.md) §6.5.1）。两个非缺陷：④ **`math.mod` 是差分 harness 的别名漏网**——`mathFn2` 报第一个缺失参数是刻意的决定（两个官方构建互相不一致，x86-64 报 #2、arm64 报 #1，因为 C 不规定实参求值顺序，没有可对齐的对象），但那条豁免的执行体只包了 `math.fmod`、没包它的 `LUA_COMPAT_MOD` 别名 `math.mod`（同一个 C 函数），于是完全相同的写法仍然可报、被开成两个 issue，`math.atan2` 也从来没被包过。判据：**豁免的边界是「具备被豁免那条性质的全部入口」，不是「我记得的那几个名字」**，加豁免前先查有没有别名或 `LUA_COMPAT_*` 兼容名（[12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9e）。⑤ **#218 是重 workload 进了 corpus**——一亿次迭代、既不崩也不分歧，只是 p4 corpus 里最重的一个 seed（1.8 秒，其余都在 0.3 秒以内），按「重 workload 走 `test/regression/`」挪走；这里第一版做错了：只抄了脚本、没抄 harness 的 `SetStepBudget(1 << 20)`，跑到循环结束耗 **87 秒**，是它要替代的那个 seed 的 50 倍。判据：**把一个 fuzz seed 转成显式回归测试时，要把那个 fuzz target 设置的每一个限制一起抄过来**——seed 的代价由「脚本 + 全部限制」共同决定，而 seed 文件里只有前一半；写完量一次耗时与 harness 对一下，量级不同就说明漏抄了。

**2026-08-03 变动（三个批量字符串构造函数开始按字节计入 step budget）**：`string.rep` / `string.format` / `table.concat` 现在都把**产出的字节数**记进 `SetStepBudget`，与 CONCAT 共用同一个计量器（1 单位 / 64 字节）。此前它们只在 preempt 点被计常数步，所以一个紧循环每次迭代只扣几步、却能搬运任意多字节：在 `SetStepBudget(1 << 20)` 之下、**并且完全没有触发预算**的情况下，三者各自的紧循环分别跑 **21 秒 / 20 秒 / 53 秒**（记账后是 46/90/70 毫秒，且预算正确触发）。这是 concat 风暴 crasher 家族（#123–#167）的一样的机制——单次 `Program.Run` 就超过 Go fuzz 的 10 秒 per-input 看门狗，而 `FuzzAutoPromote` 每个输入要跑四次 Run（#222）。**普通程序不受影响**：1 MiB 的产出约记 16K 单位、占 1<<20 预算约 1.5%，八种常规写法（含 1 MiB 的 `string.rep`、10000 元素的 `table.concat`）与 lua5.1 逐字节一致。两条判据：① **记账的计量单位要与真实成本同量纲**——`table.concat` 必须按**字节**而不是元素个数，它的遍历本来就被表的长度界住，所以「256 个元素」按个数看微不足道、每个元素 2 KiB 时实际要 53 秒；② **同一类资源只该有一个计量器**——三者走同一个 `ChargeBulkWork` 而不是各自定阈值，否则几个数字会互相漂移、哪个先触发变得难以预测，而预算本身是可加的量（记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9a 与 [10](docs/design/p1-interpreter/10-stdlib.md) §3.1a；分层执行下的语义见 [docs/embedding-tiers.md](docs/embedding-tiers.md) §5）。注意这与 `string.rep` 的 1 GiB hardening 上限是**两件事**：那个上限答「单次调用会不会把宿主进程搞死」，记账答「一个循环能不能在预算内搬无界字节」，一次调用可以既在上限之内又把预算耗尽。同轮 #221 一行代码没改（它的 seed 正是上一轮补上 `math.mod` 别名之后修掉的那个，fuzz run 跑在那个修复之前的 commit 上；仍按纪律实际重放确认过）。

**2026-08-04 变动（p4 fuzz harness 的 step budget 从 `1<<20` 降到 `1<<16`，产品代码零改动）**：#224/#225 是 concat 风暴家族的第七、第八个 nightly crasher，两个 seed **既不崩也不分歧**，字节记账正常把它们界住。真正的问题是 harness 的**余量**：`1<<20` 的 step budget 允许约 64 MiB 的 concat，本地每个 fuzz 子测试 0.7–1.3 秒，而 CI 运行器比本地慢约 10 倍（这个倍率 `internal/crescent/state.go` 的 `chargeBulkWork` 注释里早就写着——`>>6` 那个比率本身就是按最慢的 CI runner 收紧出来的），于是最慢的家族 seed 投射到 CI 是 **12–13 秒**，撞上 go-fuzz 的 **10 秒 per-input 看门狗**：六个家族 seed 里**两个已经超过**、另外四个余量不到 **1.4 倍**；nightly 日志印证的正是那个看门狗（`panic: deadlocked`）。四处 `SetStepBudget` 现在共用一个常量 `fuzzbudget.Steps`（`internal/fuzzbudget`，终值 `1<<16`），corpus 全量重放 5.5 秒 → **0.85 秒**。**这个值第一版取错过，错法本身是判据**：我先按被开成 issue 的那六个 seed 把预算**减半到 `1<<19`**，那六个 seed 确实都拿到 ≥1.8 倍余量，而一次独立审计量出**它们的邻域仍然在看门狗之上**——按四次 Run × 慢 10 倍投射，`1<<19` 下 `out=out.."x"` 循环 **14 秒**、`t[tostring(i)]=i` 循环 **18 秒**、`s:gsub("%a","x")` 循环 **41 秒**（是看门狗的四倍、比被修的 seed 还糟）。根因是 `gsub` 每次调用按大约两倍主串计费，但实际工作远多于等额计费的 concat——**等额计费不等于等额 wall-clock**。`1<<16` 让最坏那条降到 **5.2 秒**、余量 **1.93 倍**，是第一个满足数量级判据的值。**覆盖不损失这次是量出来的**：harness 自己的 seed corpus 在 `1<<20` / `1<<19` / `1<<16` 三个预算下 `PromotionCount` 完全相同（那些写法几次调用后就升层、从不接近任何一个上限）；另外注入一个真实的 P1-vs-P4 分歧确认 `1<<16` 下 harness 仍然 FAIL，90 秒引导式 fuzz 干净。据实记下变窄的一处：`1<<20` 时有两个 seed 会把 arena 推到上限、`1<<16` 时没有，所以那条 arena-cap 错误分支在这里覆盖到的写法变少了——这是收窄而不是空洞，`test/regression/issue144_regression_test.go` 直接覆盖 arena cap，而且两个预算下 step budget 都先于 arena cap 触发。四条判据：① **一个上限只要「界住」还不够，它允许的量必须与外部看门狗差一个数量级**——算法是「上限允许的量 × 单位量的本地耗时 × 目标机器的慢速倍率」，再与那台机器上每一个外部超时比余量，小于一个数量级就等于没有余量；余量不够时输入从「无界」变成「只是太慢」，而看门狗分不出这两者。② **上限要由「计费相同时最贵的写法」定，不能由「恰好被开成 issue 的那个写法」定**——定值之前把同一计费额度下最贵的几种写法都量一遍，沿着计费口（本仓是 `ChargeBulkWork`）枚举候选（①②记在 [12](docs/design/p1-interpreter/12-testing-difftest.md) §4.9a2 与 [P4 08](docs/design/p4-method-jit/08-testing-strategy.md) §3.4）。③ **一个「防住某个数值」的回归测试必须用变异实测确认它真的会因为那个数值变化而变红**——`test/regression/issue224_watchdog_margin_test.go` 第一版在预算调回 `1<<20`（正是那个回归）时照旧通过，因为它的上界写了 1 秒而它自己的注释里推导出的是 250 毫秒，而且它拿「四次 Run 的投射」比「一次 Run 的测量」；现在上界是推导出的 250 毫秒、用例表加进上面那三个真正约束预算的写法，能同时抓住 `1<<19` 与 `1<<20`。④ **判定一个 issue「已被上一轮修好」时要把 seed 拿到那个旧 commit 上也跑一遍**——这一轮两个 run 都在 #222 合并之前的 commit 上，按惯常流程就该判「过期」，而它们在**那个 commit 上也已经被界住**，所以 #222 那一轮不是修好它们的原因，「过期」这个结论不成立；run 时间早于合并是真的，但与这两个 issue 为什么被开出来无关。

**2026-08-04 变动（未闭合捕获改成读取时才抬错；嵌套尾调用返回后恢复调用方的 top）**：#228 与 #229 是同一晚拿到的两个 nightly crasher，**两个都在当前 master 上如实复现、都是真缺陷，而且互不相关**。① **#228 是抬错的时机错了，不是少了一个检查**：`"("` 这类未闭合的捕获，PUC 在 `push_onecapture` 里报错，也就是一个捕获**真的被读出来**的时候；而 `str_gsub` 的 `add_value` 只有表替换、函数替换、`%n` 展开三条路径会走到那里——纯字符串或数字替换走 `add_s`，只展开 `%n`、从不读捕获。所以同一个 pattern、同一次匹配，抬不抬错取决于消费者要不要读那个捕获：lua5.1 里 `string.gsub("abc","(","r")` 正常返回 `"rarbrcr", 4`，而 `string.match` / `string.find` 抬错。望舒原先在收集阶段就抬，于是所有消费者都变成早抬（fuzz 从 `gsub("","(",0)` 撞出）；现在 `capResult` 带一个标记、由 `capsToValues`（对应 PUC `push_onecapture` 的那个函数）在读取时抬。**第一版修法漏了表替换那条路径**——它在 `lua_gettable` 之前无条件读捕获 1，于是 `gsub("alo","(.",{})` 本该抬错却成功了，**抓到它的是官方测试套 `pm.lua:193`，不是 oracle seed**（那个 seed 只覆盖「不该抬错」这一侧）。两条判据：**把一个错误从早抬改成懒抬时，要在每个物化点各加一次检查**（参照实现里「谁调 `push_onecapture`」就是那份清单），以及**改动语义边界之后必须跑官方测试套**——fuzz seed 是单向的、官方套是双向的（记在 [10](docs/design/p1-interpreter/10-stdlib.md) §6.4.1）。② **#229 是共享调用层的既有缺陷，抬错的地方不是缺陷的地方**：P4 抬 `SETLIST: not a table` 而 P1 成功，而根因不在 JIT 里——gibbous 的 `TailCall` helper 为了同步跑完 Lua 尾调用链，以 `entryDepth = ciDepth-1` 开了一层**嵌套** `executeFrom`，于是「离开这一层的入口帧」**不等于**「离开最后一个 Lua 帧」，下面还压着一个活着的 caller 帧；而 `doReturn` 在终止分支仍然把 top 收窄到 `dst + wantedN`，caller 的活寄存器留在 top 之上，GC 的栈根扫描把 `[top, size)` 当陈旧残留清成 nil（这一步本身对齐官方 `lgc.c`），于是 `A={0}` 的 NEWTABLE 结果被清掉、紧随的 SETLIST 报错。修法对照 PUC `lvm.c` 的 `if (b) L->top = L->ci->top`——PUC 从不为 Lua callee 重进一层 `luaV_execute`，所以这笔恢复天然归 RETURN 自己做；**这是同一条纪律的第四处**，`doReturn` 自己的非终止分支、gibbous 的 `DoReturn`、`callHost` 三处早就在做（记在 [05](docs/design/p1-interpreter/05-interpreter-loop.md) §7.2.1）。分诊过程本身有两条判据：**「有一段既有注释正好描述了这个症状」是最容易走错的线索**（peroptranslator 里那段注释逐字写着 `NEWTABLE head + SETLIST → "SETLIST: not a table"`，而探针证明那条路径根本没走到），以及**抬错的位置不是缺陷的位置**——栈迹里**没有**任何 JIT 帧这件事本身就是关键信息，它把搜索范围从「JIT 怎么编译 SETLIST」翻转成「谁在 SETLIST 之前动了那个寄存器」；坏值 `tag=65528` 就是 `value.TagNil`，而 nil 是清理动作的值、不是任何指令的自然产物。两个回归测试都用「把修复还原掉」实测确认过会变红。

**2026-08-09 变动（oracle 取包限时 + 校验校验和；nightly 的 infra issue 按日期去重）**：#236–#241 是六个 nightly 自动开的 issue（标签 `ci` 不是 `bug`），**一个根因**：源码构建 oracle 那一步是裸的 `curl -sLO https://www.lua.org/ftp/lua-5.1.5.tar.gz`，**没有 `--max-time`、没有 `--connect-timeout`、没有 retry**，2026-08-07 上游一次可达性抖动就让它挂到 shell 放弃。**这一轮的诊断先判错了，而判错的方式本身是判据**：失败步骤报 `exit code 28`，被读成 **ENOSPC**（磁盘写满，errno 28）并据此提了「腾磁盘空间」的建议，而 **28 是 `curl` 的 `CURLE_OPERATION_TIMEDOUT`**——那一步是 curl，shell 报的退出码来自 curl 自己的表、不是 errno 表，两张表恰好在 28 这个数字上都有条目且都在讲一种资源类失败。日志里有两条免费的反证：`apt` 成功结束后**沉默 2 分 15 秒**才失败（磁盘写满是**立刻**失败的，会先卡的只有等待类失败），以及 `no space left` / `ENOSPC` / `disk full` 在两个 run 里出现次数都是 **0**。两条判据：**看到一个数字退出码，先定位是哪个命令退出的，再查那个命令自己的退出码表**；**分类一个 CI 失败时先把时间戳减一遍**，时间形式往往比错误码更能定类而且免费。**后果比一次红更糟**：这一步失败让后面三个差分 fuzz 步骤被 **skip**，那一轮报 failure 而实际什么都没测，那一晚的探索预算是零——而红色的默认含义是「跑了并且发现了问题」。**修法**：四处 call site（`ci.yml` ×2、`bench-acceptance.yml` ×1、`nightly-diff-fuzz.yml` ×1）统一改用 `scripts/fetch-lua-tarball.sh`——限时（`--connect-timeout 15 --max-time 120`）、重试（`--retry 4 --retry-delay 5 --retry-all-errors`，**不加 `--retry-all-errors` 的话 curl 不重试超时**，而这次的失败方式恰好是裸 `--retry` 救不了的那一种）、**解包之前校验官方 SHA-256**（截断或被替换的下载不能被静默编译进差分 oracle，那种失效是无声的）、复用已校验的 tarball（cache 命中完全跳过网络）；**不做镜像**是明确取舍——`github.com/lua/lua` 没有 5.1.5 tag，重新打包的副本过不了官方 checksum，而多一个源降低的是取包失败率（重试已经在降它）、checksum 防的是「取到了错的东西并且编译了它」。**另一半是去重键**：infra issue 标题里嵌了 `${{ matrix.variant }}`，而 infra 失败天然横跨所有 tier（divergence 失败才天然属于某个 tier），p1/p3/p4 三个标题让按标题去重看不出它们是同一件事，**两次抖动 × 三个 tier = 六个 issue**；现在按 `${{ github.run_id }}`（三个 job 共享）去重，第二三个 job 改为评论，tier 挪进正文。判据：**给自动化加去重时先问「这类事件的天然单位是什么」**，对每一维问「这一维变化时是同一件事还是两件事」，答「同一件事」的不进键；多加一维只会让分组更细、永远不会报错，所以这类错误没有任何自动信号。新增 `scripts/test-fetch-lua-tarball.sh` 挂进 `make test-scripts`，五个用例**全部离线**（用 `file://` origin）——一个「防住外部抖动」的测试如果自己依赖上游，它加的是噪声不是防线；每个用例都用变异实测过，其中「curl 调用带限时标志」这一条第一版 **grep 整个文件、假绿**（把 `--max-time` 从调用里删掉后照旧通过，因为那个词在注释里还在），现在只截取那条 curl 调用再检查。判据：**检查代码属性的测试要作用在代码本身上，一个注释就能满足的测试没有在测代码**。产品代码零改动；机制细节见 [engineering](docs/design/engineering.md) §4.1 与 §3.2、口径见 [12](docs/design/p1-interpreter/12-testing-difftest.md) §8.1。**已知缺口**：「本轮未执行任何差分」这句话已经补上(infra issue 的 body 用 `steps.difffuzz.outcome` 写明本轮差分是否执行)（infra issue 的 body 只说失败原因的类别）。

**2026-08-19 变动（nightly job 每个步骤都带 step 级超时；`gofuzztime` 预算按实测成本重配，产品代码零改动）**：nightly fuzz job 一周内被 job 超时（350 分钟）掐掉三次（2026-08-12 p4、08-15 p4、08-18 p1），其中两次真丢了覆盖。**两个独立问题，而两个都先诊断错了，是量出来才纠正的**。**问题一（作用域错）**：#236–#241 那轮给取包脚本的 curl 加了限时，但装 oracle 那一步里 `apt-get`/`make`/`check-oracle.sh` 三条命令仍然无界——只框住了当时报错的那一条命令，不是那一整步；08-18 那轮 p1 腿就在这一步卡了 **5 小时 50 分**，rolling-seed 与 auto-mode 被 skip、两个 go-fuzz 步骤根本没启动。普查后发现无界的步骤是**三个不是一个**（装 oracle、upload logs、triage），现在三步都带 step 级 `timeout-minutes`（12/15/10）——选 step 级机制是因为它对**以后新加的命令同样生效**，命令级超时只覆盖被点名的那一条。**问题二（量错了成本中心）**：连续五轮建议把 auto-mode 从 150m 压到 90m，实测这个建议**省不下任何时间**——auto-mode 只跑 2 分钟，150m 是从没接近过的挂死上限；真正的成本中心是 native go-fuzz 步骤，**312 分钟的 job 里它占 270 分钟（87%）**。原因是 workflow input description 长期写「per target（4 targets）」，而 `go-fuzz.sh` 按源码扫描发现的无 tag 目标实际是 **6 个**（`FuzzOracleDiff` 被 `wangshu_oracle_cgo` gate、在自己的步骤里跑，不算在内），6×45m=270 分钟，与实测吻合——这个过时的数字正是这一步成本被长期低估的原因。修法：`gofuzztime` 45m→35m，把这一步降到约 210 分钟、job 约 250 分钟，余量从约 38 分钟升到约 98 分钟，是真实的约 22% 探索量削减，选它而不是砍差分步骤是因为差分步骤实际很便宜（rolling-seed 36 分钟、auto-mode 2 分钟）而这一步占 87%；description 里的「4 targets」改成「6 untagged targets」。**另外两条自我纠正**：①连提四轮把 job `timeout-minutes` 抬到 420 不可能——GitHub 托管 runner 单 job 硬上限 360 分钟，推荐了四轮却从没查过平台限制；②「step 上限之和必须低于 job 上限」这个框架本身是错的——job 超时才是总预算，step 上限的作用是防一条卡死的命令悄悄吃掉这个预算，两者不是同一件事，按「求和」去配曾算出某 leg 最坏 548 分钟、逼出「只能大幅压缩」的错误结论。**五条判据**：给报错命令加超时时作用域要是它所在的整个步骤，先列出同一执行单元里所有命令；推荐调大任何配额之前先查平台硬上限；提「压缩 X」之前先量整个预算里每一项的实测耗时，占比小的项压缩不了实际时长；「per X」类配置描述里的 X 要核实际数量，这类数字不会自己报错；给一批步骤定超时时先问防的是总量超支还是单点卡死，是后者时数值按该步实测耗时的 1.5~2 倍取，不要求和。方法论见 `llmdoc/guides/unreproducible-crasher-triage.md`「给一条报错的命令加超时」与「job 超时与 step 超时」两节、`llmdoc/guides/design-claims-vs-codebase-physics.md` §3.1/§5；口径见 [12](docs/design/p1-interpreter/12-testing-difftest.md) §8.2、[engineering](docs/design/engineering.md) §3.2/§7。

每个带 `run:` 的步骤都有 step 级 `timeout-minutes`:装 oracle 12、rolling-seed 170、GC-stress 75、auto-mode 170、native go-fuzz **300**、oracle-diff go-fuzz 120、upload 15、triage 10;job 上限 350、`gofuzztime` 35m。step 上限是**两侧**约束:既要盖过合法最坏情形(p4 是 6 × 35m 再加两次 #75804 重试 = 280),又要**在 job 上限之前触发**(这一步之前约 40 分钟已消耗,所以必须小于 310)—— 窗口是 280 到 310。


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
