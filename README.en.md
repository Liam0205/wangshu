# Wangshu

Wangshu is a high-performance, embeddable Lua 5.1 virtual machine written in pure Go. It has no cgo dependency, preserving cross-compilation.

Naming: *Lua* means "moon" in Portuguese; *Wangshu* (望舒) is the Chinese mythological deity that drives the moon's chariot ("前望舒使先驱", from *Chu Ci · Li Sao*). Driving the moon — driving the Lua engine.

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Tag](https://img.shields.io/github/v/tag/Liam0205/wangshu?include_prereleases&sort=semver&label=release)](https://github.com/Liam0205/wangshu/tags)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Liam0205/wangshu)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[中文](README.md) · **English**

## Goals

- Language: cover Lua 5.1 core, matching LuaJIT's scope. No pursuit of language completeness.
- Correctness: byte-equal output against the official Lua 5.1 implementation within the covered feature set.
- Performance: lift Lua execution in the Go ecosystem from gopher-lua up to LuaJ-luajc (Java), aiming toward LuaJIT (C++).
- Platform: verified on Linux/amd64, Linux/arm64, macOS/arm64; other platforms remain reachable.
- Industrial-grade: Wangshu was built to serve production business needs from day one, and already runs in production at the company I work for. From testing to CI to the various nightly fuzz jobs, everything is driven toward industrial-grade project requirements. Wangshu has never been, and can never be, a personal practice project. Our goal is for Wangshu to become the de facto standard for embedding Lua in Go projects.

## Architecture

Wangshu uses a layered VM architecture; execution tiers are named after lunar phases:

```
P1 interpreter ──► P2 tiering bridge ──► P3 Wasm compilation ──► P4 method JIT (RC) ──► P5 trace JIT (not yet)
(crescent)         (bridge)              (gibbous)                (gibbous)              (fullmoon)
```

Architectural invariants:

* NaN-boxed u64 value representation.
* Self-managed arena as linear memory — all tiers share the same block.
* P1 interpreter is always available — it is the deopt landing site and the semantic oracle for every compilation tier.
* Byte-equal output across tiers is a CI gate.

## Performance

Numbers come from a standardized benchmark round on GitHub Actions hosted runners (the `bench-readme-table` workflow, `-benchtime=2s -count=3 -cpu=1`, median, 2026-07-10, [run 29079804565](https://github.com/Liam0205/wangshu/actions/runs/29079804565)) — all three platforms in one round on the same code. Format is "wall time (ratio over gopher-lua)"; larger is better; **bold** marks the fastest cell in a row, <ins>underline</ins> marks ratios ≥ 1.5×.

> **How to read**: hosted runners are shared VMs — absolute wall times can swing 10-20% between rounds. **Read the ratios**; wall times are only an order-of-magnitude reference within one round. The denominator is gopher-lua measured on the SAME runner in the SAME round (numerator and denominator share the interference, so ratios stay self-consistent). Do not compare wall times across rounds or platforms. Every number traces back to the raw logs in the run artifact.

### linux/amd64 (Intel Xeon Platinum 8370C)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 992 ns | **<ins>196 ns (5.07×)</ins>** | <ins>5966 ns (1.70×)</ins> [^p3-kernel] | 12155 ns (0.83×) [^p3-kernel] | <ins>229 ns (4.34×)</ins> | <ins>229 ns (4.34×)</ins> |
|  | Arith (Horner) | 1203 ns | **<ins>260 ns (4.63×)</ins>** | <ins>8513 ns (2.28×)</ins> [^p3-kernel] | 13896 ns (1.39×) [^p3-kernel] | <ins>288 ns (4.18×)</ins> | <ins>288 ns (4.18×)</ins> |
|  | Loop (summing loop) | 77.8 µs | **<ins>22.9 µs (3.39×)</ins>** | <ins>486 µs (7.69×)</ins> [^p3-kernel] | <ins>485 µs (7.71×)</ins> [^p3-kernel] | <ins>29.5 µs (2.64×)</ins> | <ins>29.5 µs (2.64×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 314 ms | <ins>99.3 ms (3.16×)</ins> | <ins>117 ms (2.69×)</ins> | <ins>117 ms (2.69×)</ins> | <ins>17.6 ms (17.8×)</ins> | **<ins>16.8 ms (18.6×)</ins>** |
|  | HeavyRecursion | 11.5 ms | <ins>7.08 ms (1.62×)</ins> | 7.79 ms (1.47×) | 8.28 ms (1.39×) | <ins>2.33 ms (4.91×)</ins> | **<ins>2.33 ms (4.92×)</ins>** [^selftail] |
|  | HeavyFloatloop | 559 ms | <ins>195 ms (2.86×)</ins> | <ins>73.3 ms (7.63×)</ins> | <ins>73.6 ms (7.60×)</ins> | <ins>30.9 ms (18.1×)</ins> | **<ins>30.8 ms (18.2×)</ins>** |
| realworld small [^cat-realworld] | fib | 12.7 ms | 14.3 ms (0.89×) | 15.8 ms (0.80×) [^p3-gate] | 33.8 ms (0.38×) | **<ins>1.37 ms (9.25×)</ins>** [^seg2seg] | <ins>1.38 ms (9.20×)</ins> [^seg2seg] |
|  | binary-trees | 70.6 ms | 53.5 ms (1.32×) | 56.6 ms (1.25×) [^p3-gate] | 139 ms (0.51×) | <ins>39.1 ms (1.80×)</ins> | **<ins>38.9 ms (1.82×)</ins>** [^seg2seg] |
|  | spectral-norm | 48.1 ms | <ins>26.3 ms (1.83×)</ins> | <ins>30.8 ms (1.56×)</ins> [^p3-gate] | 62.3 ms (0.77×) | <ins>2.99 ms (16.1×)</ins> | **<ins>2.98 ms (16.1×)</ins>** [^seg2seg] |
|  | fannkuch | 5.80 ms | 8.11 ms (0.71×) | 8.66 ms (0.67×) | 8.65 ms (0.67×) | <ins>0.74 ms (7.84×)</ins> | **<ins>0.73 ms (7.92×)</ins>** [^seg2seg] |
|  | n-body | 70.0 ms | 63.3 ms (1.11×) | 66.3 ms (1.06×) [^p3-gate] | 118 ms (0.59×) | **<ins>5.68 ms (12.3×)</ins>** [^math-intrinsic] | <ins>5.68 ms (12.3×)</ins> [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 987 ns | **<ins>194 ns (5.09×)</ins>** | — | — | — | — |
|  | CallOnly | **119 ns** | 289 ns (0.41×) | 309 ns (0.38×) | 442 ns (0.27×) | 339 ns (0.35×) | 336 ns (0.35×) |
|  | Boundary (+SetGlobal) | **257 ns** | 483 ns (0.53×) | 499 ns (0.52×) | 976 ns (0.26×) | 450 ns (0.57×) | 445 ns (0.58×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 987 ns | **<ins>194 ns (5.09×)</ins>** | — | — | — | — |
|  | CallOnly | 119 ns | **98.9 ns (1.20×)** | 106 ns (1.12×) | 226 ns (0.53×) | 139 ns (0.86×) | 139 ns (0.86×) |
|  | Boundary (+SetGlobal) | 257 ns | 282 ns (0.91×) | 302 ns (0.85×) | 742 ns (0.35×) | **248 ns (1.04×)** | 250 ns (1.03×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | **652 µs** | 784 µs (0.83×) | 811 µs (0.80×) | 1487 µs (0.44×) | 679 µs (0.96×) | 680 µs (0.96×) |
|  | Transform (×1000) | **528 µs** | 607 µs (0.87×) | 637 µs (0.83×) | 950 µs (0.56×) | 585 µs (0.90×) | 586 µs (0.90×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 652 µs | 583 µs (1.12×) | 603 µs (1.08×) | 1237 µs (0.53×) | **473 µs (1.38×)** | 476 µs (1.37×) |
|  | Transform (×1000) | 528 µs | 406 µs (1.30×) | 428 µs (1.24×) | 717 µs (0.74×) | **382 µs (1.38×)** | 383 µs (1.38×) |

### linux/arm64 (Azure Cobalt 100, Neoverse-N2 class)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 965 ns | **<ins>202 ns (4.77×)</ins>** | <ins>5951 ns (1.68×)</ins> [^p3-kernel] | 10202 ns (0.98×) [^p3-kernel] | <ins>212 ns (4.56×)</ins> | <ins>212 ns (4.56×)</ins> |
|  | Arith (Horner) | 1141 ns | **<ins>247 ns (4.63×)</ins>** | <ins>8344 ns (2.19×)</ins> [^p3-kernel] | <ins>12179 ns (1.50×)</ins> [^p3-kernel] | <ins>259 ns (4.41×)</ins> | <ins>259 ns (4.41×)</ins> |
|  | Loop (summing loop) | 75.9 µs | **<ins>23.1 µs (3.28×)</ins>** | <ins>594 µs (6.10×)</ins> [^p3-kernel] | <ins>594 µs (6.09×)</ins> [^p3-kernel] | <ins>28.9 µs (2.63×)</ins> | <ins>28.9 µs (2.63×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 303 ms | <ins>96.4 ms (3.14×)</ins> | <ins>118 ms (2.57×)</ins> | <ins>118 ms (2.57×)</ins> | <ins>23.1 ms (13.1×)</ins> | **<ins>22.0 ms (13.8×)</ins>** |
|  | HeavyRecursion | 9.66 ms | 6.58 ms (1.47×) | 7.48 ms (1.29×) | 8.12 ms (1.19×) | **<ins>2.43 ms (3.98×)</ins>** | <ins>2.43 ms (3.98×)</ins> [^selftail] |
|  | HeavyFloatloop | 542 ms | <ins>189 ms (2.87×)</ins> | <ins>84.9 ms (6.38×)</ins> | <ins>85.0 ms (6.38×)</ins> | **<ins>37.0 ms (14.7×)</ins>** | <ins>37.0 ms (14.6×)</ins> |
| realworld small [^cat-realworld] | fib | 12.5 ms | 15.8 ms (0.79×) | 16.3 ms (0.77×) [^p3-gate] | 29.8 ms (0.42×) | **<ins>1.46 ms (8.57×)</ins>** [^seg2seg] | <ins>1.46 ms (8.57×)</ins> [^seg2seg] |
|  | binary-trees | 66.2 ms | 52.1 ms (1.27×) | 54.9 ms (1.21×) [^p3-gate] | 120 ms (0.55×) | <ins>37.0 ms (1.79×)</ins> | **<ins>37.0 ms (1.79×)</ins>** [^seg2seg] |
|  | spectral-norm | 46.6 ms | <ins>27.3 ms (1.70×)</ins> | 31.7 ms (1.47×) [^p3-gate] | 54.9 ms (0.85×) | <ins>5.62 ms (8.29×)</ins> | **<ins>5.62 ms (8.29×)</ins>** [^seg2seg] |
|  | fannkuch | 5.63 ms | 7.67 ms (0.73×) | 7.50 ms (0.75×) | 7.50 ms (0.75×) | <ins>0.84 ms (6.72×)</ins> | **<ins>0.83 ms (6.75×)</ins>** [^seg2seg] |
|  | n-body | 70.9 ms | 57.4 ms (1.23×) | 59.2 ms (1.20×) [^p3-gate] | 106 ms (0.67×) | **<ins>8.86 ms (8.00×)</ins>** [^math-intrinsic] | <ins>8.88 ms (7.98×)</ins> [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 972 ns | **<ins>204 ns (4.76×)</ins>** | — | — | — | — |
|  | CallOnly | **132 ns** | 285 ns (0.46×) | 306 ns (0.43×) | 428 ns (0.31×) | 375 ns (0.35×) | 379 ns (0.35×) |
|  | Boundary (+SetGlobal) | **278 ns** | 468 ns (0.59×) | 494 ns (0.56×) | 897 ns (0.31×) | 491 ns (0.57×) | 490 ns (0.57×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 972 ns | **<ins>204 ns (4.76×)</ins>** | — | — | — | — |
|  | CallOnly | 132 ns | **132 ns (1.00×)** | 146 ns (0.91×) | 215 ns (0.62×) | 200 ns (0.66×) | 200 ns (0.66×) |
|  | Boundary (+SetGlobal) | **278 ns** | 312 ns (0.89×) | 324 ns (0.86×) | 672 ns (0.41×) | 317 ns (0.88×) | 316 ns (0.88×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | **657 µs** | 796 µs (0.83×) | 828 µs (0.79×) | 1383 µs (0.47×) | 757 µs (0.87×) | 756 µs (0.87×) |
|  | Transform (×1000) | **570 µs** | 639 µs (0.89×) | 666 µs (0.86×) | 934 µs (0.61×) | 676 µs (0.84×) | 674 µs (0.85×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 657 µs | 621 µs (1.06×) | 630 µs (1.04×) | 1156 µs (0.57×) | **558 µs (1.18×)** | 563 µs (1.17×) |
|  | Transform (×1000) | 570 µs | **469 µs (1.21×)** | 498 µs (1.14×) | 709 µs (0.80×) | 485 µs (1.17×) | 475 µs (1.20×) |

### darwin/arm64 (Apple M-series, macos-latest)

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 767 ns | <ins>149 ns (5.16×)</ins> | <ins>4143 ns (2.72×)</ins> [^p3-kernel] | 7896 ns (1.43×) [^p3-kernel] | **<ins>132 ns (5.83×)</ins>** | **<ins>132 ns (5.83×)</ins>** |
|  | Arith (Horner) | 876 ns | <ins>188 ns (4.65×)</ins> | <ins>6233 ns (3.07×)</ins> [^p3-kernel] | <ins>10743 ns (1.78×)</ins> [^p3-kernel] | **<ins>165 ns (5.32×)</ins>** | **<ins>165 ns (5.32×)</ins>** |
|  | Loop (summing loop) | 77.8 µs | **<ins>18.4 µs (4.23×)</ins>** | <ins>914 µs (3.83×)</ins> [^p3-kernel] | <ins>837 µs (4.18×)</ins> [^p3-kernel] | <ins>21.0 µs (3.71×)</ins> | <ins>21.0 µs (3.71×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 292 ms | <ins>109 ms (2.67×)</ins> | <ins>92.2 ms (3.17×)</ins> | <ins>94.6 ms (3.09×)</ins> | <ins>40.0 ms (7.30×)</ins> | **<ins>37.3 ms (7.84×)</ins>** |
|  | HeavyRecursion | 12.0 ms | <ins>6.50 ms (1.84×)</ins> | <ins>6.40 ms (1.87×)</ins> | <ins>6.61 ms (1.81×)</ins> | <ins>2.04 ms (5.87×)</ins> | **<ins>1.91 ms (6.25×)</ins>** [^selftail] |
|  | HeavyFloatloop | 481 ms | <ins>149 ms (3.22×)</ins> | <ins>112 ms (4.29×)</ins> | <ins>111 ms (4.32×)</ins> | <ins>45.5 ms (10.6×)</ins> | **<ins>40.3 ms (11.9×)</ins>** |
| realworld small [^cat-realworld] | fib | 13.2 ms | 15.0 ms (0.88×) | 13.1 ms (1.01×) [^p3-gate] | 22.2 ms (0.60×) | **<ins>1.08 ms (12.2×)</ins>** [^seg2seg] | <ins>1.14 ms (11.6×)</ins> [^seg2seg] |
|  | binary-trees | 54.5 ms | 41.0 ms (1.33×) | 41.2 ms (1.32×) [^p3-gate] | 94.8 ms (0.57×) | <ins>27.9 ms (1.95×)</ins> | **<ins>27.5 ms (1.98×)</ins>** [^seg2seg] |
|  | spectral-norm | 33.3 ms | <ins>20.4 ms (1.63×)</ins> | <ins>21.2 ms (1.57×)</ins> [^p3-gate] | 45.8 ms (0.73×) | **<ins>4.92 ms (6.78×)</ins>** | <ins>4.93 ms (6.77×)</ins> [^seg2seg] |
|  | fannkuch | 4.41 ms | 6.21 ms (0.71×) | 6.44 ms (0.68×) | 6.40 ms (0.69×) | <ins>0.74 ms (5.98×)</ins> | **<ins>0.72 ms (6.11×)</ins>** [^seg2seg] |
|  | n-body | 65.4 ms | 44.7 ms (1.46×) | 46.2 ms (1.42×) [^p3-gate] | 79.5 ms (0.82×) | **<ins>7.35 ms (8.89×)</ins>** [^math-intrinsic] | <ins>7.40 ms (8.83×)</ins> [^math-intrinsic] |
| Boundary mini · Call [^cat-mini] | PureVM | 846 ns | **<ins>144 ns (5.88×)</ins>** | — | — | — | — |
|  | CallOnly | **97.5 ns** | 179 ns (0.54×) | 191 ns (0.51×) | 280 ns (0.35×) | 218 ns (0.45×) | 246 ns (0.40×) |
|  | Boundary (+SetGlobal) | **197 ns** | 333 ns (0.59×) | 311 ns (0.64×) | 641 ns (0.31×) | 291 ns (0.68×) | 286 ns (0.69×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 846 ns | **<ins>144 ns (5.88×)</ins>** | — | — | — | — |
|  | CallOnly | 97.5 ns | **71.5 ns (1.36×)** | 82.2 ns (1.19×) | 167 ns (0.58×) | 110 ns (0.88×) | 117 ns (0.84×) |
|  | Boundary (+SetGlobal) | 197 ns | 193 ns (1.02×) | 198 ns (1.00×) | 528 ns (0.37×) | **179 ns (1.10×)** | 179 ns (1.10×) |
| Real workload · Call [^cat-embed] | Predicate (×1000) | 548 µs | 528 µs (1.04×) | 577 µs (0.95×) | 1055 µs (0.52×) | **465 µs (1.18×)** | 469 µs (1.17×) |
|  | Transform (×1000) | 459 µs | 506 µs (0.91×) | 440 µs (1.04×) | 637 µs (0.72×) | **412 µs (1.12×)** | 421 µs (1.09×) |
| Real workload · CallInto [^cat-embed] | Predicate (×1000) | 548 µs | 494 µs (1.11×) | 446 µs (1.23×) | 905 µs (0.61×) | **<ins>348 µs (1.57×)</ins>** | <ins>360 µs (1.52×)</ins> |
|  | Transform (×1000) | 459 µs | 359 µs (1.28×) | 309 µs (1.49×) | 521 µs (0.88×) | **<ins>295 µs (1.56×)</ins>** | <ins>295 µs (1.56×)</ins> |

[^cat-baseline]: `benchmarks/baseline`. Three self-contained scripts (Simple branch-compare, Arith six-order Horner polynomial, Loop sum 1..N), no Go↔Lua boundary crossing. Shows VM-core dispatch / arithmetic / loop cost under minimum workload.
[^selftail]: P4 mono self-tail-call in-segment loop (issue #112 / PR #113, 2026-07-10, amd64 + arm64 shipped): when `return f(...)` calls the running closure itself, the segment moves the arguments and jumps back to its entry (bit-identical to re-entering under PUC tail-call frame-reuse semantics) instead of paying a per-level segment exit + Go re-entry. HeavyRecursion (collatz — every recursive call is a TAILCALL) was previously the only workload in the table where promotion LOST to the P1 interpreter (amd64 1.15× vs P1 1.58×; Cobalt arm64 0.98×, losing to gopher outright); this round it lands at amd64 **4.92×** / arm64 **3.98×** / macOS **6.25×** — a ~4× improvement on all three platforms, with fib / HeavyArith unchanged in the same round.
[^cat-heavy]: `benchmarks/heavy`. Three flat numeric kernels (HeavyArith pure arithmetic, HeavyRecursion self-recursion, HeavyFloatloop nested float loop); intentionally excludes tables, strings, library CALL and other helper-bound structures. Shows the compilation tier's performance ceiling on shapes that actually let it work.
[^cat-realworld]: `benchmarks/realworld`. Five benchmark-game scripts (fib / binary-trees / spectral-norm / fannkuch / n-body); a single-pass semantics run is differential-tested against the official lua5.1.5 (byte-equal). Shows conventional load under a mix of calls / allocations / floats / table ops.
[^p3-gate]: P3 auto carries a helper-density profitability gate (issue #39, 2026-07-03): when a hot proto's op mix is dominated by helper round trips (the wasm→Go boundary cost eats the promotion win), promotion is declined and the proto stays on the interpreter. Rows with this marker declined promotion; the number IS interpreter execution (the delta vs the P1 column is sampling-hook overhead). The P3 force column is unaffected (force-all bypasses the gate to preserve differential coverage).
[^p3-kernel]: The baseline P3 columns run a different workload from the other columns (issue #93): a top-level chunk is vararg and never promotes, so P3 must measure the body wrapped in an inner kernel called 50 times, while the other columns run the bare top-level ×1. The P3 ratios therefore use the SAME-shape gopher baseline (`_GopherKernel`, gopher running the identical kernel×50) as denominator, and the wall times are not directly comparable with the rest of the row (~50× the work). The table previously divided by the top-level ×1 gopher number, understating P3 by ~50× (the old 0.06×-0.25× cells were really 1.3×-3.2×). All platform tables are produced under the corrected basis.
[^seg2seg]: P4 segment-to-segment CALL dispatch (issue #50, 2026-07-04, delivered on both amd64 and arm64): self-recursive / arith-callee shapes (the fib pattern) used to pay a cross-boundary round trip per call (mmap RET → Go dispatch → host.CallBaseline → mmap re-entry); the caller segment now `call`s directly into the callee segment, which builds/tears its frame in-segment and recurses natively without ever leaving mmap. fib / spectral-norm / fannkuch and other self-recursive or arith-callee loads flipped to double-digit ratios as a result; binary-trees' `check` (self-recursion + GETTABLE ArrayHit table reads) unlocked once ArrayHit sites became seg2seg-eligible, with bottomup's allocation as the remaining bottleneck. Both arches share the win; see the three tables above for current per-platform numbers.
[^math-intrinsic]: P4 math.* intrinsic emission (issue #77 / PR #87, 2026-07-08, amd64 + arm64 shipped): when a CALL site's IC observes the callee is a known pure-numeric host closure (sqrt / floor / ceil / abs / max / min), the segment emits the hardware instruction directly (amd64 SQRTSD / ROUNDSD etc., arm64 FSQRT / FRINTM etc.) instead of an exit-reason round trip to the Go host closure. n-body's steady state is almost entirely `sqrt(dist2)` calls; it was previously stuck both ways — each sqrt paid a boundary round trip AND the CALL density gate misjudged the hot function as "too call-dense to promote". With #77 (intrinsic CALLs excluded from the density gate + inline sqrt emission) it flipped from ~P1-level to double-digit ratios on both arches, byte-equal with the interpreter (including NaN / Inf / ±0).
[^cat-mini]: `benchmarks/embedded`, mini_bench_test.go. The minimal shape of the embed path: one SetGlobal + one Call + one result read per iter. Shows raw boundary-crossing cost, plus the delta between the allocating `Call` path and the zero-alloc `CallInto` path.
[^cat-embed]: `benchmarks/embedded`, realworld_embedded_bench_test.go. A batch of 1000 items — per item set fields → Call predicate / feature-transform script → read scalar result, shaped after pineapple's `transform_by_lua`. Shows steady-state throughput of a real batch-processing embed.

### What each column means

- **`gopher`** — gopher-lua v1.1.2, the baseline. Every ratio in the table is `gopher / X`; larger is better.
- **`P1`** — default `go build`, pure interpreter (crescent), no promotion, single column.
- **`P3 auto` / `P3 force`** — two measurements of the gibbous-wasm compilation tier under `wangshu_p3 wangshu_profile` build (see the next section).
- **`P4 auto` / `P4 force`** — two measurements of the gibbous-jit method JIT under `wangshu_p4 wangshu_profile` build.
- **`Call` / `CallInto`** — two boundary call styles: `st.Call` allocates a fresh `[]Value` slice per call; `st.CallInto` reuses the caller's `dst`, zero-alloc. Split only in cross-boundary benchmarks.

> `—` marks scenarios where the Call / CallInto split does not apply (PureVM has no boundary; the baseline short scripts on the compilation tier have no separate numbers because they never promote).

### auto vs force applies only to P3/P4

The compilation tiers (P3/P4) are not "install and it runs". They rely on a **heat-threshold automatic promotion** mechanism:

1. Every function (Proto) starts on the P1 crescent interpreter.
2. Each call bumps a counter; the `wangshu_profile` build tag turns this sampler on (without it, sampling is disabled and the compilation tier collapses back to P1).
3. Once the counter crosses `HotEntryThreshold` (default 200), if the Proto passes the F1-F7 compilability checks, it is promoted to P3 or P4, and subsequent calls run on the compiled tier.
4. Protos that cannot be promoted (coroutines / top-level vararg / `ReasonUnknownCall` / VARARG etc.) stay on P1 as a silent fallback.

So each of P3/P4 has two columns:

- **`auto`** — production mode. The State is long-lived; the first ~200 calls run on P1, then the promoted tier takes over. Over `b.N`, the warmup tail is usually within noise.
- **`force`** — `SetForceAllPromote(true)` forces all promotable Protos up immediately, followed by one warmup run to measure steady state. **Not a production mode**; used only for differential testing and benchmark upper bounds.

The two steady-state numbers should be close in theory; any large gap indicates the promotion policy or threshold needs tuning.

### Reproduction commands

The three tables above are produced by the `bench-readme-table` workflow — one round of the same script on all three platforms (linux/amd64, linux/arm64, darwin/arm64) on GitHub Actions, raw logs and tables landing in the run artifact:

```bash
gh workflow run bench-readme-table.yml -f os=all -f count=3   # standard three-platform round
gh workflow run bench-readme-table.yml --ref <branch> -f os=amd64  # single platform on any branch
```

The same script runs on a local dev machine (for before/after A/B comparisons of an optimization — same-machine same-round relative comparison is steadier than hosted runners):

```bash
./scripts/bench-readme-table.sh              # full run + Markdown table on stdout
./scripts/bench-readme-table.sh --count 5    # 5 repetitions per tier, median
./scripts/bench-readme-table.sh --format-only <logdir>  # reformat existing logs without rerunning
```

The script auto-detects `goos/goarch`; the same command reproduces the matching table on any platform.

## Quick start

### Minimal example

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

`Program` is immutable and reusable across States. Each goroutine needs its own `State`.

### Columnar-kernel shape: cross the boundary once, keep the loop in the VM

For batch data processing, prefer the arena column container: the host Go side attaches `[]float64` / `[]int64` / `[]bool` / `[]string` into the arena; the script side sees an ordinary-looking table `arena.price`, and `price[i]` directly reads a NaN-boxed value with no per-item boundary crossing.

```go
ar := wangshu.NewArena(nrows)
ar.AddFloatColumn("price", prices, nil) // present=nil means all present
ar.AddInt64Column("qty",   qtys,   nil)

prog, _ := wangshu.Compile([]byte(`
    local price, qty = arena.price, arena.qty
    local total = 0
    for i = 1, arena.rows do total = total + price[i] * qty[i] end
    return total
`), "kernel")

results, err := prog.Call(st, ar) // single boundary crossing, the loop stays in the VM
```

### Switching between the four execution tiers

All four tiers are selected via build tag, no source-side changes. The default build is P1; P3/P4 require the explicit tag. Within the same build, `auto` is the default (production heat threshold + F1-F7 compilability checks); `SetForceAllPromote(true)` switches to `force` (bypasses heat threshold, non-production, used for differential testing and benchmarks).

```bash
# P1 crescent interpreter (default build, always available)
go build ./...

# P3 gibbous-wasm compilation tier (depends on wazero)
go build -tags "wangshu_p3 wangshu_profile" ./...

# P4 gibbous-jit method JIT (self-managed native codegen, amd64 + arm64)
go build -tags "wangshu_p4 wangshu_profile" ./...
```

`wangshu_profile` is the promotion prerequisite: without this tag, heat sampling is disabled and the promotion path is unreachable. `wangshu_p3` and `wangshu_p4` are mutually exclusive — only one at a time.

```go
st := wangshu.NewState(wangshu.Options{})

// auto: default. Waits for hot functions to promote naturally (via HotEntryThreshold).
_, _ = prog.Run(st)

// force: **testing-only**, bypasses the threshold and promotes everything. Do not use in production.
st.SetForceAllPromote(true)
_, _ = prog.Run(st)

// Observe whether promotion actually happened
n := st.PromotionCount() // >0 means promotion has occurred
```

`SetForceAllPromote` only bypasses the heat threshold; it does **not** bypass the F1-F7 compilability checks (coroutines, top-level vararg, Protos with `ReasonUnknownCall` or VARARG opcodes still do not promote). Non-promotable Protos silently fall back to P1; cross-tier byte-equal output is preserved.

### Production runtime switch and observability

The production admin API for tiered execution (unlike the testing-only force switch above):

```go
// One-flip fallback to the interpreter: new promotions stop, and
// already-promoted functions run on P1 again; compiled code is kept,
// so re-enabling resumes without recompiling.
st.SetTierEnabled(false)
st.SetTierEnabled(true)

// Per-State tier distribution snapshot
stats := st.TierStatsSnapshot()
// stats.Promoted            number of promoted protos
// stats.StuckCompileFailed  real compile failures — nonzero is worth investigating
// stats.TierEnabled         switch state
```

Deployment requirements (P4's exec-mmap environment constraints), rollout advice, and step-budget semantics under tiered execution are covered in [docs/embedding-tiers.md](docs/embedding-tiers.md).

### Managing and reusing arena

`Options` exposes the initial and upper-bound sizes of the arena:

```go
st := wangshu.NewState(wangshu.Options{
    InitialArenaBytes: 64 * 1024,        // initial 64 KiB
    MaxArenaBytes:     16 * 1024 * 1024, // upper bound 16 MiB, fail-fast on overrun
})
```

Statistics:

```go
st.GCCountKB()  // currently used KB (live bytes; drops after Collect)
st.ArenaCapKB() // arena backing capacity in KB (grow-only; the pool layer uses this as its fat-state threshold)
st.PromotionCount() // number of promoted Protos (testing-only white-box assertion)
```

Explicit GC control:

```go
st.Collect()           // force a full GC sweep
st.MaybeCollectNow()   // collect only if the host-trigger threshold is met (non-forced)
st.SetHostTriggeredCollect(true) // opt-in: host-side threshold auto-triggers collect (requires all transient GCRefs to be pinned)
```

For short-script high-frequency call scenarios, prefer `CallInto` to reuse the return-value slice and stay on the zero-alloc path:

```go
dst := make([]wangshu.Value, 0, 4)
for i := 0; i < 1000; i++ {
    n, err := st.CallInto(dst[:], fn, wangshu.String("item"))
    _ = n; _ = err
    // dst is reused, no per-call allocation
}
```

For long-lived State scenarios (rule-engine hot reload / data-flow transformation), pair `SetHostTriggeredCollect` with a `Collect` cadence to bring GC pressure to near zero.

## Language coverage

Wangshu implements the Lua 5.1 core language (same surface as LuaJIT), covering 37 of the 38 bytecode opcodes defined by the Lua 5.1 reference manual (`VARARG` never enters the P3/P4 compilation tier and stays on the P1 path), plus the mandatory surface of the base / string / table / math / os / io / coroutine stdlib.

Correctness is verified at four layers:

1. **Official test suite byte-equal**: 13 files from Lua 5.1.5 (vararg / sort / pm as full files, others up to the exemption line) byte-equal.
2. **Reference-manual probes**: 100 manual features + 12 corner cases + 29 error messages (with line-number assertions) + 70 seed cases, all byte-equal.
3. **Differential random fuzz**: the nightly-diff-fuzz workflow runs 2M random scripts per night against the Lua 5.1.5 oracle (P1 + P3 + P4 in parallel).
4. **Three-way diff**: crescent (P1) vs gibbous (P3/P4) under the P4 build runs a byte-equal check every CI, PRs #29/#31 green across the tri-platform matrix.

**Exemption list** (`test/difftest/corners_test.go::exemptions`, 15 items, auditable via `go test -v -run TestExemptions_Documented`):

| Category | Items | Reason |
| --- | --- | --- |
| Lua 5.2+ features | `rawlen`, `table.pack` / `table.move` | Not in the 5.1 manual |
| Lua 5.3+ features | `math.tointeger` / `type` / `maxinteger` / `mininteger` | Integer type is a 5.3 feature |
| Embed safety | `os.execute`, `io.popen` / `io.tmpfile`, `os.exit` real-exit, `loadfile` / `dofile` disabled by default | Embedded VMs do not let scripts run shells / access the filesystem beyond their scope |
| Debug interface | `debug.sethook` / `getlocal` / `setlocal` / `getupvalue` / `setupvalue` / `getregistry` | Requires interpreter-internal hooks, cost/benefit is not worth it |
| Module system | `require` / `module` / `package` | Embedders provide scripts via `Compile`; no filesystem-driven require |
| Bytecode serialisation | `string.dump` | Custom ISA is not compatible with the official `.luc` |
| Environment ops | `getfenv` / `setfenv` | Conflicts with P2 tiering-bridge F4 shape analysis |
| C undefined behaviour | `tonumber` negative `strtoul` wraparound | The official implementation returns `1.844e19` via C `strtoul` overflow; we return `-255` — intuitive semantics preferred |
| Catastrophic backtracking | pattern catastrophic backtracking `.*.+%A*x` | Backtracking budget is capped at `1<<20`; reports `pattern too complex` (embedded anti-hang) |
| Incremental GC | `collectgarbage("step" / "setstepmul")` | STW GC has no incremental parameters, placeholder returns |

Everything else "exists but is not byte-compared" (`collectgarbage("count")` / `gcinfo` / `os.time` / `os.clock` / `os.date("%Y")` / `io.write` / `loadfile` error format) is asserted only for return-value shape by `TestApprox_ExistenceOnly`, not for numeric equality.

## Doc navigation

By role:

- **Just want to use it**: this README "Quick start" section → the [pkg.go.dev](https://pkg.go.dev/github.com/Liam0205/wangshu) API reference for `Compile` / `Program.Run` / `Program.Call` / `State.CallInto`.
- **Going to production with P3/P4 tiered execution**: [docs/embedding-tiers.md](docs/embedding-tiers.md) — deployment requirements (exec-mmap environment constraints), runtime switch, TierStats observability, step-budget semantics, launch checklist.
- **Understanding the architecture**: [docs/design/architecture.md](docs/design/architecture.md) (package layout / component dependencies / tier mapping) → [docs/design/roadmap.md](docs/design/roadmap.md) (motivation / calibration measurement / evolution path / non-goals).
- **Deep dive into a tier**:
  - P1 interpreter (13 docs): start from [docs/design/p1-interpreter/00-overview.md](docs/design/p1-interpreter/00-overview.md); progress at [implementation-progress](docs/design/p1-interpreter/implementation-progress.md).
  - P2 tiering bridge (7 docs): start from [docs/design/p2-bridge/00-overview.md](docs/design/p2-bridge/00-overview.md); progress at [implementation-progress](docs/design/p2-bridge/implementation-progress.md).
  - P3 gibbous-wasm (10 docs): start from [docs/design/p3-wasm-tier/00-overview.md](docs/design/p3-wasm-tier/00-overview.md); progress at [implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md).
  - P4 gibbous-jit (11 docs + progress): start from [docs/design/p4-method-jit/00-overview.md](docs/design/p4-method-jit/00-overview.md); progress at [implementation-progress](docs/design/p4-method-jit/implementation-progress.md); PJ11 acceptance at [09-acceptance-checklist](docs/design/p4-method-jit/09-acceptance-checklist.md).
  - P5 trace JIT (not implemented): [docs/design/p5-trace-jit.md](docs/design/p5-trace-jit.md) (outline design).
- **Engineering conventions / commit discipline**: [docs/design/engineering.md](docs/design/engineering.md) (Git hooks / CI / Makefile / release discipline / lint toolchain).
- **AI collaboration conventions**: [llmdoc/](llmdoc/) records project-level guidance for LLM collaborators — start from [startup.md](llmdoc/startup.md); contains `must/` (non-negotiable), `guides/` (best practices), `memory/` (historical decisions and reflections; `reflections/` has milestone lessons).

## Contributing

Issues and PRs are welcome. Basic steps:

**Dev environment**: Go 1.25+, Linux/amd64, Linux/arm64, or macOS/arm64 (other GOOS/GOARCH combinations compile via pure-Go stubs but are not exercised). Optional deps: `lua5.1` (the official oracle, used by differential tests; `apt install lua5.1` or build 5.1.5 from source), `golangci-lint` (lint).

**Common make targets**:

```bash
make all              # full local pre-submit check: fmt + lint + build-all + test-all + fuzz-all + conformance + difftest-all
make test-p4          # run the P4 build test suite only
make test-p3          # run the P3 build test suite only
make difftest         # differential tests across three builds x three platforms
make fuzz-p4          # fuzz smoke under the P4 build
make bench            # baseline micro-benchmarks
make release TAG=vX.Y.Z MESSAGE_FILE=notes.txt  # cut an annotated tag (local only, does not push)
```

**Submission flow**:

1. Fork and create a feature branch (do not push to master directly).
2. `make all` must pass locally.
3. Commit messages in English; subject a single line ≤ 72 ASCII characters; body may be Chinese, must explain why and how.
4. PR description must include change scope, testing status, and whether external dependencies were introduced (main-library zero-cgo / zero external deps are hard promises).
5. PR triggers CI (three-platform × three-build × test/fuzz-smoke/conformance/difftest, all green) plus the agentic-pr-review bot review; REQUEST_CHANGES from the bot must be addressed, APPROVE-then-maintainer-merge.
6. Discuss direction in an issue first for large changes.

**Bug reports** should include a minimal reproducer script, Go version, GOOS/GOARCH, and `make all` output. For output divergences from the official 5.1.5, attach the corresponding `lua5.1 -e ...` stdout.

## License

Apache License 2.0, see [LICENSE](LICENSE) (in the absence of the file, the `go.mod` declaration is authoritative).

Plain-language summary for users:

- Free to use, modify, distribute, and use commercially, including embedding into closed-source products.
- Keep the LICENSE and copyright notice intact.
- If your distribution contains modifications to this project, note them briefly.
- Provided `AS IS`, no warranty from the project.
