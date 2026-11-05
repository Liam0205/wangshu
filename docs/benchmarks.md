# 性能基准：完整数据

> 读者：想看三平台完整数字、或者要复现 / 更新这些数字的人。README 只放 linux/amd64 一张表，其余在这里。英文版见 [benchmarks.en.md](benchmarks.en.md)。

数字来自 GitHub Actions hosted runner 上的一轮标准化基准（`bench-readme-table` workflow，`-benchtime=2s -count=3 -cpu=1`，取 median，2026-07-10，[run 29098511106](https://github.com/Liam0205/wangshu/actions/runs/29098511106)），三个平台同一轮、同一份代码。

**怎么读**

- 每格是「wall time (相对 gopher-lua 的倍率)」，倍率越大越好。**粗体**是该行最快的一格，<ins>下划线</ins>表示倍率 ≥ 1.5×。
- **以倍率为主。** hosted runner 是共享虚拟机，绝对 wall time 在不同轮之间会漂 10-20%。倍率的分母是同一轮、同一台 runner 上测的 gopher-lua，分子分母受同样的干扰，所以倍率可信；跨轮、跨平台都不要直接比 wall time。
- 每个数字都能在 run artifact 的原始日志里找到。

## linux/amd64（Intel Xeon Platinum 8573C）

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

## linux/arm64（Azure Cobalt 100，Neoverse-N2 类）

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

## darwin/arm64（Apple M 系，macos-latest）

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

## 列的含义

- **`gopher`**：gopher-lua v1.1.2，作为基线。表里的倍率都是 `gopher / X`，越大越好。
- **`P1`**：默认 build，纯解释器（crescent），没有升层，只有一列。
- **`P3 auto` / `P3 force`**：`wangshu_p3 wangshu_profile` build 下 gibbous-wasm 编译层的两种测法，见下一节。
- **`P4 auto` / `P4 force`**：`wangshu_p4 wangshu_profile` build 下 gibbous-jit method JIT 的两种测法。
- **`Call` / `CallInto`**：嵌入 API 的两种调用方式。`st.Call` 每次分配一个新的 `[]Value` 返回切片，`st.CallInto` 复用调用方传入的 `dst`，不分配。只有跨边界的基准分这两行。
- **`—`**：这一格不适用。PureVM 不跨边界，没有 Call / CallInto 之分；它也不升层，所以编译层的几列没有单独的数字。

## auto 与 force

P3 / P4 不是打开就一定用上。函数要先在解释器上跑热，才会升到编译层：

1. 每个函数（Proto）一开始都在 P1 解释器上跑。
2. 每次调用都计数。这个计数由 `wangshu_profile` build tag 打开，不带这个 tag 就不计数，编译层也就用不上。
3. 计数超过 `HotEntryThreshold`（默认 200），并且函数通过 F1-F7 可编译性检查，就升到 P3 或 P4，之后的调用走编译层。
4. 不能升层的函数（协程、顶层 vararg、含 `ReasonUnknownCall` 或 VARARG 等）一直留在 P1，不报错。

所以 P3 / P4 各有两列：

- **`auto`**：生产环境的用法。State 长期复用，前约 200 次调用在解释器上跑，之后升层。摊到整个基准里，预热的部分通常在噪声范围内。
- **`force`**：`SetForceAllPromote(true)`，所有能升层的函数一开始就升层，预热一轮后测稳态。**不是生产用法**，只用于差分测试和测上限。

两列的稳态数字应该接近；差得多，说明升层策略或阈值需要调整。

## 复现

本文的三张表由 `bench-readme-table` workflow 产出：在 GitHub Actions 上对三个平台各跑一轮同一个脚本，原始日志和表格保存在 run artifact 里。

```bash
gh workflow run bench-readme-table.yml -f os=all -f count=3        # 三平台标准轮
gh workflow run bench-readme-table.yml --ref <branch> -f os=amd64  # 在任意分支上跑单个平台
```

在本机跑同一个脚本，适合做优化前后的对比（同一台机器、同一轮的相对比较比 hosted runner 稳）：

```bash
./scripts/bench-readme-table.sh                         # 全跑，直接输出 Markdown 表格
./scripts/bench-readme-table.sh --count 5               # 每档跑 5 次取 median
./scripts/bench-readme-table.sh --format-only <logdir>  # 只重新排版已有日志，不重跑
```

脚本会自动识别 `goos/goarch`，在哪个平台跑就产出哪个平台的表。
