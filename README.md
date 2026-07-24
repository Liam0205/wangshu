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

望舒实现的是 Lua 5.1 核心语言（与 LuaJIT 一致的语法层），覆盖 Lua 5.1 参考手册中定义的 38 个字节码 opcode 除 `VARARG` 外的全部（`VARARG` 在 P3/P4 编译层永不接入，走 P1 解释器路径），以及 stdlib 的 base / string / table / math / os / io / coroutine 全部必做面。

正确性验证五层：

1. **官方测试套 byte-equal**：13 个 5.1.5 官方文件（vararg / sort / pm 整文件 + 其余截至豁免线）逐字节一致。
2. **手册逐节 probe**：100 项手册特性 + 12 项边角 + 29 条错误消息（含行号断言）+ 70 条种子用例逐字节一致。
3. **差分随机 fuzz**：nightly-diff-fuzz workflow 每晚 2M 条随机脚本与 Lua 5.1.5 oracle 做差分测试（P1 + P3 + P4 三档并行）。
4. **三方差分**：crescent（P1）vs gibbous（P3/P4）在 P4 build 下每 CI 跑一次 byte-equal，PR #29/#31 tri-platform matrix 全绿。
5. **cgo 内嵌 oracle 差分 fuzz**：`internal/oracle` 把官方 5.1.5 源码经 cgo 嵌进测试二进制（build tag `wangshu_oracle_cgo`，默认 build 保持零 cgo），`FuzzOracleDiff` 用 go-fuzz 变异的**任意不规则源码**（不限于 generator 的规整脚本）在进程内对拍：两侧跑同一段 prelude（输出捕获 + 确定性 stub + 排序迭代 + 白名单裁剪）后**比输出经地址归一后 byte-equal**，唯一豁免的窄口径是 **NaN 符号 spelling**（#173，IEEE 754 不赋 NaN 符号数值语义，PUC/x86/glibc 与 wangshu 的显示因 NaN 位模式与 verb 大小写双向分歧；机制化处理路径见下）。PR 门禁 60s 冒烟（oracle-smoke job），nightly p1 腿 45m 长跑。上线首日（fuzz 长跑 + 配套的 stdlib 全函数 × 退化参数系统性扫描）抓出 **32 处** P1 与官方的语义分歧并全部修复——覆盖 string 库 number 自动转串、`__tostring` 原样透传、协程错误消息 type 名、未知转义字面放行、长括号嵌套 deprecation、`upper`/`lower` 按字节、`string.format` verb 集/无符号转型/`%c` NUL 截断/scanformat 硬限、编译期常量折叠 ±0/NaN 规则与 RK 物化序、`tonumber` 的 C99 strtod 接受面（hex float / inf / nan）、表构造器源码序覆盖语义、真实共享 string 元表（`getmetatable("")` 可改且全部元方法全局生效）、`load` 只收 reader 函数、`os.time` 表协议等；上线后持续巡检又修 #170/#171（`string.format` NaN/Inf 渲染对齐 glibc，PR #172）/ #174/#175（`toNumberStr` 走 `crescent.ParseLuaNumber`，PR #176）/ #177（P4 shape-template FORLOOP deopt 补 preempt 与 body 副作用，PR #178），并把 **#173 定性为已知平台差异**（`internal/stdlib/stringlib.go` 硬编码最常见负号路径，剩余 spelling 差异走 harness 侧机制化跳过——prelude 加 `__nan_output` 证据位来自渲染出口、`internal/oracle` `CompareOutput` 三态归类仅在两端都有 NaN 证据时对单一 token spelling 精细匹配含 printf 宽度一列偿还，字面量/case/alignment/其他字节差异一律回到正常失败；nightly-diff-fuzz 对此类窄口径 NaN 符号差异不再报 bug，其他输出差异仍正常失败）。

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
