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

Numbers come from a standardized benchmark round on GitHub Actions hosted runners (the `bench-readme-table` workflow, `-benchtime=2s -count=3 -cpu=1`, median, 2026-07-10, [run 29098511106](https://github.com/Liam0205/wangshu/actions/runs/29098511106)) — all three platforms in one round on the same code. Format is "wall time (ratio over gopher-lua)"; larger is better; **bold** marks the fastest cell in a row, <ins>underline</ins> marks ratios ≥ 1.5×.

> **How to read**: hosted runners are shared VMs — absolute wall times can swing 10-20% between rounds. **Read the ratios**; wall times are only an order-of-magnitude reference within one round. The denominator is gopher-lua measured on the SAME runner in the SAME round (numerator and denominator share the interference, so ratios stay self-consistent). Do not compare wall times across rounds or platforms. Every number traces back to the raw logs in the run artifact.

### linux/amd64 (Intel Xeon Platinum 8573C)

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

### linux/arm64 (Azure Cobalt 100, Neoverse-N2 class)

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

### darwin/arm64 (Apple M-series, macos-latest)

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

[^cat-baseline]: `benchmarks/baseline`. Three self-contained scripts (Simple branch-compare, Arith six-order Horner polynomial, Loop sum 1..N), no Go↔Lua boundary crossing. Shows VM-core dispatch / arithmetic / loop cost under minimum workload.
[^selftail]: P4 mono self-tail-call in-segment loop (issue #112 / PR #113, 2026-07-10, amd64 + arm64 shipped): when `return f(...)` calls the running closure itself, the segment moves the arguments and jumps back to its entry (bit-identical to re-entering under PUC tail-call frame-reuse semantics) instead of paying a per-level segment exit + Go re-entry. HeavyRecursion (collatz — every recursive call is a TAILCALL) was previously the only workload in the table where promotion LOST to the P1 interpreter (amd64 1.15× vs P1 1.58×; Cobalt arm64 0.98×, losing to gopher outright); this round it lands at amd64 **4.93×** / arm64 **3.93×** / macOS **6.23×** — a ~4× improvement on all three platforms, with fib / HeavyArith unchanged in the same round.
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
