# Benchmarks: full data

> For readers who want all three platforms, or who need to reproduce or refresh these numbers. The README shows only the linux/amd64 table. Chinese version: [benchmarks.md](benchmarks.md).

The numbers come from one standardized benchmark round on GitHub Actions hosted runners (the `bench-readme-table` workflow, `-benchtime=2s -count=3 -cpu=1`, median, 2026-07-10, [run 29098511106](https://github.com/Liam0205/wangshu/actions/runs/29098511106)): all three platforms, one round, the same code.

**How to read**

- Each cell is "wall time (ratio over gopher-lua)"; larger is better. **Bold** is the fastest cell in a row; <ins>underline</ins> marks ratios ≥ 1.5×.
- **Read the ratios.** Hosted runners are shared VMs, so absolute wall times drift 10-20% between rounds. The denominator is gopher-lua measured on the same runner in the same round, so both sides see the same interference and the ratio holds. Do not compare wall times across rounds or platforms.
- Every number traces back to the raw logs in the run artifact.

## linux/amd64 (Intel Xeon Platinum 8573C)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 826 ns | **<ins>149 ns (5.54×)</ins>** | <ins>4246 ns (2.00×)</ins> [^p3-kernel] | 9613 ns (0.88×) [^p3-kernel] | <ins>165 ns (5.02×)</ins> | <ins>165 ns (5.02×)</ins> |
|  | Arith (Horner) | 994 ns | <ins>209 ns (4.75×)</ins> | <ins>6512 ns (2.35×)</ins> [^p3-kernel] | 11423 ns (1.34×) [^p3-kernel] | **<ins>207 ns (4.81×)</ins>** | **<ins>207 ns (4.81×)</ins>** |
|  | Loop (summing loop) | 60.6 µs | **<ins>20.1 µs (3.01×)</ins>** | <ins>419 µs (7.25×)</ins> [^p3-kernel] | <ins>405 µs (7.49×)</ins> [^p3-kernel] | <ins>22.8 µs (2.66×)</ins> | <ins>22.8 µs (2.66×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 292 ms | <ins>84.3 ms (3.46×)</ins> | <ins>97.6 ms (2.99×)</ins> | <ins>97.2 ms (3.00×)</ins> | <ins>16.2 ms (18.0×)</ins> | **<ins>15.7 ms (18.5×)</ins>** |
|  | HeavyRecursion | 9.34 ms | <ins>5.51 ms (1.69×)</ins> | <ins>5.93 ms (1.58×)</ins> | 6.44 ms (1.45×) | <ins>1.94 ms (4.80×)</ins> | **<ins>1.89 ms (4.93×)</ins>** [^selftail] |
|  | HeavyFloatloop | 464 ms | <ins>166 ms (2.80×)</ins> | <ins>57.7 ms (8.03×)</ins> | <ins>60.0 ms (7.73×)</ins> | <ins>26.2 ms (17.7×)</ins> | **<ins>25.7 ms (18.0×)</ins>** |
| realworld small [^cat-realworld] | fib | 10.2 ms | 11.8 ms (0.87×) | 12.8 ms (0.80×) [^p3-gate] | 27.7 ms (0.37×) | **<ins>1.09 ms (9.35×)</ins>** [^seg2seg] | <ins>1.12 ms (9.10×)</ins> [^seg2seg] |
|  | binary-trees | 56.0 ms | 41.4 ms (1.35×) | 45.1 ms (1.24×) [^p3-gate] | 118 ms (0.48×) | **<ins>29.7 ms (1.89×)</ins>** | <ins>31.5 ms (1.78×)</ins> [^seg2seg] |
|  | spectral-norm | 37.0 ms | <ins>21.1 ms (1.75×)</ins> | 25.3 ms (1.46×) [^p3-gate] | 53.4 ms (0.69×) | <ins>2.46 ms (15.0×)</ins> | **<ins>2.40 ms (15.4×)</ins>** [^seg2seg] |
|  | fannkuch | 4.87 ms | 6.32 ms (0.77×) | 7.00 ms (0.70×) | 7.12 ms (0.68×) | <ins>0.65 ms (7.51×)</ins> | **<ins>0.62 ms (7.87×)</ins>** [^seg2seg] |
|  | n-body | 68.4 ms | 53.7 ms (1.27×) | 56.0 ms (1.22×) [^p3-gate] | 108 ms (0.63×) | <ins>4.70 ms (14.5×)</ins> [^math-intrinsic] | **<ins>4.64 ms (14.7×)</ins>** [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 844 ns | **<ins>151 ns (5.58×)</ins>** | — | — | — | — |
|  | CallOnly | **104 ns** | 222 ns (0.47×) | 235 ns (0.44×) | 365 ns (0.28×) | 251 ns (0.41×) | 251 ns (0.41×) |
|  | Boundary (+SetGlobal) | **220 ns** | 392 ns (0.56×) | 400 ns (0.55×) | 830 ns (0.27×) | 351 ns (0.63×) | 342 ns (0.64×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 844 ns | **<ins>151 ns (5.58×)</ins>** | — | — | — | — |
|  | CallOnly | 104 ns | **84.9 ns (1.22×)** | 85.6 ns (1.21×) | 196 ns (0.53×) | 114 ns (0.90×) | 115 ns (0.90×) |
|  | Boundary (+SetGlobal) | 220 ns | 230 ns (0.96×) | 244 ns (0.90×) | 618 ns (0.36×) | **191 ns (1.15×)** | 198 ns (1.11×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | 568 µs | 660 µs (0.86×) | 697 µs (0.82×) | 1241 µs (0.46×) | 571 µs (0.99×) | **554 µs (1.03×)** |
|  | Transform (×1000) | **466 µs** | 493 µs (0.95×) | 532 µs (0.88×) | 801 µs (0.58×) | 488 µs (0.95×) | 475 µs (0.98×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 568 µs | 489 µs (1.16×) | 507 µs (1.12×) | 1043 µs (0.54×) | **382 µs (1.49×)** | 394 µs (1.44×) |
|  | Transform (×1000) | 466 µs | 355 µs (1.31×) | 348 µs (1.34×) | 589 µs (0.79×) | <ins>310 µs (1.51×)</ins> | **<ins>306 µs (1.52×)</ins>** |

## linux/arm64 (Azure Cobalt 100, Neoverse-N2 class)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 987 ns | **<ins>196 ns (5.03×)</ins>** | <ins>6016 ns (1.66×)</ins> [^p3-kernel] | 10223 ns (0.97×) [^p3-kernel] | <ins>206 ns (4.80×)</ins> | <ins>206 ns (4.80×)</ins> |
|  | Arith (Horner) | 1162 ns | **<ins>236 ns (4.92×)</ins>** | <ins>8277 ns (2.19×)</ins> [^p3-kernel] | 12312 ns (1.47×) [^p3-kernel] | <ins>252 ns (4.62×)</ins> | <ins>252 ns (4.62×)</ins> |
|  | Loop (summing loop) | 73.4 µs | **<ins>23.1 µs (3.18×)</ins>** | <ins>594 µs (6.04×)</ins> [^p3-kernel] | <ins>594 µs (6.04×)</ins> [^p3-kernel] | <ins>29.0 µs (2.53×)</ins> | <ins>29.0 µs (2.53×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 300 ms | <ins>96.5 ms (3.11×)</ins> | <ins>119 ms (2.52×)</ins> | <ins>119 ms (2.53×)</ins> | <ins>23.1 ms (13.0×)</ins> | **<ins>22.0 ms (13.7×)</ins>** |
|  | HeavyRecursion | 9.53 ms | 6.59 ms (1.45×) | 7.59 ms (1.26×) | 8.07 ms (1.18×) | **<ins>2.42 ms (3.93×)</ins>** | <ins>2.42 ms (3.93×)</ins> [^selftail] |
|  | HeavyFloatloop | 524 ms | <ins>190 ms (2.76×)</ins> | <ins>84.9 ms (6.17×)</ins> | <ins>85.0 ms (6.17×)</ins> | **<ins>37.0 ms (14.2×)</ins>** | <ins>37.1 ms (14.1×)</ins> |
| realworld small [^cat-realworld] | fib | 12.5 ms | 14.4 ms (0.87×) | 16.1 ms (0.78×) [^p3-gate] | 29.9 ms (0.42×) | <ins>1.46 ms (8.56×)</ins> [^seg2seg] | **<ins>1.46 ms (8.57×)</ins>** [^seg2seg] |
|  | binary-trees | 63.9 ms | 52.0 ms (1.23×) | 54.9 ms (1.16×) [^p3-gate] | 120 ms (0.53×) | **<ins>37.1 ms (1.72×)</ins>** | <ins>37.1 ms (1.72×)</ins> [^seg2seg] |
|  | spectral-norm | 45.5 ms | <ins>27.3 ms (1.67×)</ins> | 31.8 ms (1.43×) [^p3-gate] | 55.4 ms (0.82×) | <ins>5.62 ms (8.10×)</ins> | **<ins>5.62 ms (8.10×)</ins>** [^seg2seg] |
|  | fannkuch | 5.76 ms | 7.21 ms (0.80×) | 7.46 ms (0.77×) | 7.46 ms (0.77×) | <ins>0.83 ms (6.90×)</ins> | **<ins>0.83 ms (6.92×)</ins>** [^seg2seg] |
|  | n-body | 77.7 ms | 57.4 ms (1.35×) | 59.5 ms (1.30×) [^p3-gate] | 106 ms (0.73×) | <ins>8.86 ms (8.77×)</ins> [^math-intrinsic] | **<ins>8.86 ms (8.77×)</ins>** [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 1000 ns | **<ins>198 ns (5.05×)</ins>** | — | — | — | — |
|  | CallOnly | **132 ns** | 279 ns (0.48×) | 301 ns (0.44×) | 429 ns (0.31×) | 368 ns (0.36×) | 364 ns (0.36×) |
|  | Boundary (+SetGlobal) | **279 ns** | 460 ns (0.61×) | 488 ns (0.57×) | 902 ns (0.31×) | 479 ns (0.58×) | 482 ns (0.58×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 1000 ns | **<ins>198 ns (5.05×)</ins>** | — | — | — | — |
|  | CallOnly | 132 ns | **132 ns (1.00×)** | 147 ns (0.90×) | 216 ns (0.61×) | 202 ns (0.65×) | 204 ns (0.65×) |
|  | Boundary (+SetGlobal) | **279 ns** | 312 ns (0.89×) | 325 ns (0.86×) | 689 ns (0.41×) | 319 ns (0.87×) | 319 ns (0.87×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | **665 µs** | 812 µs (0.82×) | 810 µs (0.82×) | 1388 µs (0.48×) | 746 µs (0.89×) | 751 µs (0.88×) |
|  | Transform (×1000) | **546 µs** | 634 µs (0.86×) | 660 µs (0.83×) | 935 µs (0.58×) | 670 µs (0.81×) | 666 µs (0.82×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 665 µs | 645 µs (1.03×) | 632 µs (1.05×) | 1190 µs (0.56×) | **556 µs (1.20×)** | 563 µs (1.18×) |
|  | Transform (×1000) | 546 µs | **471 µs (1.16×)** | 505 µs (1.08×) | 727 µs (0.75×) | 489 µs (1.12×) | 481 µs (1.13×) |

## darwin/arm64 (Apple M-series, macos-latest)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 792 ns | <ins>141 ns (5.63×)</ins> | <ins>5002 ns (1.62×)</ins> [^p3-kernel] | 8899 ns (0.91×) [^p3-kernel] | **<ins>134 ns (5.93×)</ins>** | **<ins>134 ns (5.93×)</ins>** |
|  | Arith (Horner) | 909 ns | <ins>188 ns (4.82×)</ins> | <ins>6509 ns (2.31×)</ins> [^p3-kernel] | 11250 ns (1.34×) [^p3-kernel] | **<ins>169 ns (5.37×)</ins>** | **<ins>169 ns (5.37×)</ins>** |
|  | Loop (summing loop) | 56.5 µs | **<ins>18.0 µs (3.14×)</ins>** | <ins>850 µs (3.24×)</ins> [^p3-kernel] | <ins>820 µs (3.36×)</ins> [^p3-kernel] | <ins>21.1 µs (2.68×)</ins> | <ins>21.1 µs (2.68×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 214 ms | <ins>95.5 ms (2.24×)</ins> | <ins>97.0 ms (2.21×)</ins> | <ins>97.4 ms (2.20×)</ins> | **<ins>34.2 ms (6.26×)</ins>** | <ins>35.2 ms (6.08×)</ins> |
|  | HeavyRecursion | 10.9 ms | <ins>5.24 ms (2.08×)</ins> | <ins>6.38 ms (1.71×)</ins> | <ins>6.84 ms (1.59×)</ins> | <ins>1.76 ms (6.19×)</ins> | **<ins>1.75 ms (6.23×)</ins>** [^selftail] |
|  | HeavyFloatloop | 423 ms | <ins>144 ms (2.95×)</ins> | <ins>118 ms (3.59×)</ins> | <ins>119 ms (3.56×)</ins> | **<ins>37.3 ms (11.3×)</ins>** | <ins>37.6 ms (11.3×)</ins> |
| realworld small [^cat-realworld] | fib | 10.4 ms | 12.2 ms (0.85×) | 13.0 ms (0.80×) [^p3-gate] | 25.2 ms (0.41×) | **<ins>0.99 ms (10.5×)</ins>** [^seg2seg] | <ins>1.00 ms (10.4×)</ins> [^seg2seg] |
|  | binary-trees | 59.8 ms | 47.0 ms (1.27×) | 41.6 ms (1.44×) [^p3-gate] | 94.1 ms (0.64×) | **<ins>26.0 ms (2.30×)</ins>** | <ins>26.0 ms (2.30×)</ins> [^seg2seg] |
|  | spectral-norm | 36.1 ms | <ins>21.8 ms (1.65×)</ins> | <ins>22.6 ms (1.59×)</ins> [^p3-gate] | 45.6 ms (0.79×) | **<ins>4.51 ms (8.01×)</ins>** | <ins>4.63 ms (7.80×)</ins> [^seg2seg] |
|  | fannkuch | 4.84 ms | 6.75 ms (0.72×) | 6.18 ms (0.78×) | 6.20 ms (0.78×) | **<ins>0.69 ms (6.99×)</ins>** | <ins>0.71 ms (6.82×)</ins> [^seg2seg] |
|  | n-body | 65.5 ms | 45.3 ms (1.45×) | 44.3 ms (1.48×) [^p3-gate] | 79.4 ms (0.83×) | **<ins>6.88 ms (9.52×)</ins>** [^math-intrinsic] | <ins>6.91 ms (9.48×)</ins> [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 908 ns | **<ins>145 ns (6.28×)</ins>** | — | — | — | — |
|  | CallOnly | **97.3 ns** | 190 ns (0.51×) | 178 ns (0.55×) | 307 ns (0.32×) | 223 ns (0.44×) | 224 ns (0.43×) |
|  | Boundary (+SetGlobal) | **212 ns** | 323 ns (0.66×) | 297 ns (0.72×) | 719 ns (0.30×) | 308 ns (0.69×) | 298 ns (0.71×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 908 ns | **<ins>145 ns (6.28×)</ins>** | — | — | — | — |
|  | CallOnly | 97.3 ns | **75.4 ns (1.29×)** | 79.6 ns (1.22×) | 170 ns (0.57×) | 112 ns (0.87×) | 113 ns (0.86×) |
|  | Boundary (+SetGlobal) | 212 ns | 196 ns (1.08×) | 188 ns (1.13×) | 516 ns (0.41×) | **177 ns (1.20×)** | 178 ns (1.20×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | 566 µs | 581 µs (0.97×) | 522 µs (1.08×) | 967 µs (0.59×) | **472 µs (1.20×)** | 480 µs (1.18×) |
|  | Transform (×1000) | 423 µs | **406 µs (1.04×)** | 407 µs (1.04×) | 610 µs (0.69×) | 418 µs (1.01×) | 415 µs (1.02×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 566 µs | 442 µs (1.28×) | 432 µs (1.31×) | 856 µs (0.66×) | **<ins>348 µs (1.63×)</ins>** | <ins>350 µs (1.62×)</ins> |
|  | Transform (×1000) | 423 µs | 330 µs (1.28×) | 301 µs (1.40×) | 498 µs (0.85×) | **293 µs (1.44×)** | 294 µs (1.44×) |

[^cat-baseline]: `benchmarks/baseline`. Three self-contained Lua scripts (Simple branch-compare, Arith six-order Horner polynomial, Loop sum 1..N), run once, never crossing the Go↔Lua boundary. Shows the VM core's dispatch, arithmetic and loop cost under minimal work.
[^cat-heavy]: `benchmarks/heavy`. Three flat numeric kernels (HeavyArith pure arithmetic, HeavyRecursion self-recursion, HeavyFloatloop nested float loop) that deliberately avoid tables, strings and library calls. Shows the compiled tiers' ceiling on the code they handle best.
[^cat-realworld]: `benchmarks/realworld`. Five benchmark-game scripts (fib / binary-trees / spectral-norm / fannkuch / n-body), each one's output byte-compared against the official lua5.1.5. Shows a typical mix of calls, allocation, floating point and table operations.
[^cat-mini]: `benchmarks/embedded`, `mini_bench_test.go`. The smallest embedding round trip: one SetGlobal, one Call, one result read per iteration. Shows the cost of crossing the boundary itself, and the gap between the allocating `Call` and the zero-alloc `CallInto`.
[^cat-embed]: `benchmarks/embedded`, `realworld_embedded_bench_test.go`. Batches of 1000 items: per item set fields, call a predicate or feature-transform script, read back a scalar. Shows steady-state throughput of a real batch-processing embed.
[^p3-kernel]: These cells measure different work from the rest of the row. A top-level chunk is vararg and never promotes, so the P3 columns measure the script body wrapped in an inner function called 50 times, and divide by a gopher run of the same shape (`_GopherKernel`). The ratio is comparable; the wall time is about 50× the rest of the row and is not.
[^p3-gate]: P3 auto has a profitability check: when too many of a hot function's operations have to call back into Go, the boundary cost would cancel the gain, so the function stays on the interpreter. Cells with this mark did not promote, and the number is interpreter time (the gap to the P1 column is the sampling overhead). P3 force bypasses the check and is unaffected.
[^seg2seg]: P4 segment-to-segment calls: for self-recursion or calls into a pure-arithmetic function (the fib pattern), the caller's machine code jumps straight into the callee's, and the whole recursion never returns to Go. Implemented on amd64 and arm64.
[^selftail]: P4 self-tail-call loop: when `return f(...)` calls the running function itself, the machine code moves the arguments and jumps back to the entry instead of exiting and re-entering from Go at every level. HeavyRecursion (collatz) recurses only through tail calls, so it gains the most.
[^math-intrinsic]: P4 math intrinsics: when a call site sees a known pure-numeric host function (`sqrt` / `floor` / `ceil` / `abs` / `max` / `min`), it emits the hardware instruction directly (SQRTSD / ROUNDSD on amd64, FSQRT / FRINTM etc. on arm64) instead of calling back into Go. Results are byte-equal with the interpreter, including NaN, Inf and ±0. n-body's hot loop is almost all `sqrt`, so it gains the most.

## What each column means

- **`gopher`**: gopher-lua v1.1.2, the baseline. Every ratio in the tables is `gopher / X`; larger is better.
- **`P1`**: the default build, a pure interpreter (crescent) with no promotion; one column.
- **`P3 auto` / `P3 force`**: two measurements of the gibbous-wasm tier under the `wangshu_p3 wangshu_profile` build; see the next section.
- **`P4 auto` / `P4 force`**: two measurements of the gibbous-jit method JIT under the `wangshu_p4 wangshu_profile` build.
- **`Call` / `CallInto`**: the two embedding call styles. `st.Call` allocates a fresh `[]Value` result slice per call; `st.CallInto` reuses the caller's `dst` and does not allocate. Only the boundary benchmarks split into these two rows.
- **`—`**: not applicable. PureVM does not cross the boundary, so there is no Call / CallInto split; it also never promotes, so the compiled-tier columns have no separate number.

## auto and force

P3 / P4 do not take over just by being built in. A function has to get hot on the interpreter first:

1. Every function (Proto) starts on the P1 interpreter.
2. Every call is counted. The `wangshu_profile` build tag turns the counting on; without it nothing is counted and the compiled tier is never reached.
3. Once the count passes `HotEntryThreshold` (default 200) and the function passes the F1-F7 compilability checks, it is promoted to P3 or P4, and later calls run on the compiled tier.
4. Functions that cannot be promoted (coroutines, top-level vararg, `ReasonUnknownCall`, VARARG and so on) stay on P1 without an error.

So P3 / P4 each have two columns:

- **`auto`**: the production setup. The State is reused; the first ~200 calls run on the interpreter, then the function is promoted. Spread over the whole benchmark, the warm-up is usually within noise.
- **`force`**: `SetForceAllPromote(true)` promotes every promotable function up front, then one warm-up run before measuring steady state. **Not for production**; used for differential testing and measuring the ceiling.

The two steady-state numbers should be close; a large gap means the promotion policy or threshold needs tuning.

## Reproducing

The three tables here are produced by the `bench-readme-table` workflow: the same script runs once on each of the three platforms on GitHub Actions, and the raw logs and tables land in the run artifact.

```bash
gh workflow run bench-readme-table.yml -f os=all -f count=3        # standard three-platform round
gh workflow run bench-readme-table.yml --ref <branch> -f os=amd64  # one platform on any branch
```

The same script runs locally, which suits before/after comparisons of an optimization (a relative comparison on one machine in one round is steadier than hosted runners):

```bash
./scripts/bench-readme-table.sh                         # full run, Markdown table on stdout
./scripts/bench-readme-table.sh --count 5               # 5 runs per tier, median
./scripts/bench-readme-table.sh --format-only <logdir>  # re-render existing logs without rerunning
```

The script detects `goos/goarch` itself and produces the table for whichever platform it runs on.
