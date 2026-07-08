# Wangshu

Wangshu is a high-performance, embeddable Lua 5.1 virtual machine written in pure Go. It has no cgo dependency, preserving cross-compilation.

Naming: *Lua* means "moon" in Portuguese; *Wangshu* (望舒) is the Chinese mythological deity that drives the moon's chariot ("前望舒使先驱", from *Chu Ci · Li Sao*). Driving the moon — driving the Lua engine.

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Go Report Card](https://goreportcard.com/badge/github.com/Liam0205/wangshu)](https://goreportcard.com/report/github.com/Liam0205/wangshu)
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

Numbers taken on one machine (linux/amd64, Intel Xeon Platinum, 24 core, go1.26.2, `-benchtime=2s -count=3 -cpu=1`, median). Format is "wall time (ratio over gopher-lua)"; larger is better; **bold** marks the fastest cell in a row, <ins>underline</ins> marks ratios ≥ 1.5×. Reproduction commands are in the [section below](#reproduction-commands). darwin/arm64 measurements are in the subsection below.

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro [^cat-baseline] | Simple (branch/compare) | 954 ns | **<ins>135 ns (7.07×)</ins>** | 4062 ns (0.23×) | 4116 ns (0.23×) | <ins>145 ns (6.58×)</ins> | <ins>145 ns (6.58×)</ins> |
|  | Arith (Horner) | 1045 ns | **<ins>175 ns (5.97×)</ins>** | 10135 ns (0.10×) | 10321 ns (0.10×) | <ins>183 ns (5.71×)</ins> | <ins>183 ns (5.71×)</ins> |
|  | Loop (sum) | 37.2 µs | **<ins>17.0 µs (2.18×)</ins>** | 364 µs (0.10×) | 365 µs (0.10×) | <ins>21.4 µs (1.74×)</ins> | <ins>21.4 µs (1.74×)</ins> |
| Heavy kernels [^cat-heavy] | HeavyArith | 240 ms | <ins>78.3 ms (3.06×)</ins> | <ins>86.4 ms (2.77×)</ins> | <ins>86.4 ms (2.77×)</ins> | <ins>14.2 ms (16.8×)</ins> | **<ins>13.7 ms (17.5×)</ins>** |
|  | HeavyRecursion | 8.99 ms | **<ins>5.07 ms (1.76×)</ins>** | <ins>5.72 ms (1.57×)</ins> | <ins>5.71 ms (1.58×)</ins> | <ins>5.36 ms (1.68×)</ins> | <ins>5.42 ms (1.66×)</ins> |
|  | HeavyFloatloop | 410 ms | <ins>146 ms (2.81×)</ins> | <ins>51.1 ms (8.04×)</ins> | <ins>51.2 ms (8.01×)</ins> | **<ins>24.0 ms (17.1×)</ins>** | **<ins>24.0 ms (17.1×)</ins>** |
| Realworld small [^cat-realworld] | fib | 9.32 ms | 10.0 ms (0.93×) | 11.1 ms (0.84×) [^p3-gate] | 25.0 ms (0.37×) | **<ins>0.90 ms (10.3×)</ins>** [^seg2seg] | <ins>0.91 ms (10.3×)</ins> [^seg2seg] |
|  | binary-trees | 51.5 ms | 35.8 ms (1.44×) | 38.6 ms (1.34×) [^p3-gate] | 103.6 ms (0.50×) | <ins>26.0 ms (1.98×)</ins> | **<ins>26.1 ms (1.98×)</ins>** [^seg2seg] |
|  | spectral-norm | 33.3 ms | <ins>18.3 ms (1.82×)</ins> | <ins>20.6 ms (1.62×)</ins> [^p3-gate] | 46.3 ms (0.72×) | <ins>15.6 ms (2.14×)</ins> | **<ins>2.11 ms (15.8×)</ins>** [^seg2seg] |
|  | fannkuch | 4.15 ms | 5.60 ms (0.74×) | 5.74 ms (0.72×) | 5.74 ms (0.73×) | **<ins>0.60 ms (6.9×)</ins>** | **<ins>0.60 ms (6.9×)</ins>** [^seg2seg] |
|  | n-body | 59.9 ms | 44.6 ms (1.34×) | 43.3 ms (1.38×) [^p3-gate] | 86.0 ms (0.70×) | **42.5 ms (1.41×)** | **42.5 ms (1.41×)** |
| Boundary mini · Call [^cat-mini] | PureVM | 945 ns | **<ins>138 ns (6.85×)</ins>** | — | — | — | — |
|  | CallOnly | **85.2 ns** | 194 ns (0.44×) | 200 ns (0.43×) | 314 ns (0.27×) | 197 ns (0.43×) | 200 ns (0.43×) |
|  | Boundary (+SetGlobal) | **185 ns** | 324 ns (0.57×) | 328 ns (0.56×) | 343 ns (0.54×) | 329 ns (0.56×) | 335 ns (0.55×) |
| Boundary mini · CallInto [^cat-mini] | PureVM | 945 ns | **<ins>138 ns (6.85×)</ins>** | — | — | — | — |
|  | CallOnly | 85.2 ns | 79.4 ns (1.07×) | 79.2 ns (1.08×) | 181 ns (0.47×) | 79.5 ns (1.07×) | **79.0 ns (1.08×)** |
|  | Boundary (+SetGlobal) | 185 ns | **180 ns (1.03×)** | 192.7 ns (0.96×) | 195 ns (0.95×) | 189.3 ns (0.98×) | 191.2 ns (0.97×) |
| Realworld embedded · Call [^cat-embed] | Predicate (×1000) | **476 µs** | 583 µs (0.82×) | 569 µs (0.84×) | 589 µs (0.81×) | 561 µs (0.85×) | 560 µs (0.85×) |
|  | Transform (×1000) | **337 µs** | 436 µs (0.77×) | 436 µs (0.77×) | 438 µs (0.77×) | 431 µs (0.78×) | 435 µs (0.77×) |
| Realworld embedded · CallInto [^cat-embed] | Predicate (×1000) | 476 µs | 407 µs (1.17×) | 421 µs (1.13×) | 425 µs (1.12×) | **404 µs (1.18×)** | 409 µs (1.16×) |
|  | Transform (×1000) | 337 µs | **287 µs (1.18×)** | 290 µs (1.16×) | 290 µs (1.16×) | 288 µs (1.17×) | 291 µs (1.16×) |

### darwin/arm64 measurements (Apple M5 Pro)

The same reproduction commands measured on an Apple M5 Pro (darwin/arm64, go1.26.4, `-benchtime=2s -count=3 -cpu=1`, median).

| Category | Script | gopher | P1 | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Pure-VM micro | Simple (branch/compare) | 572 ns | <ins>83.2 ns (6.88×)</ins> | 2.57 µs (0.22×) | 2.57 µs (0.22×) | **<ins>82.7 ns (6.92×)</ins>** | **<ins>82.7 ns (6.92×)</ins>** |
|  | Arith (Horner) | 605 ns | **<ins>102 ns (5.93×)</ins>** | 6.42 µs (0.09×) | 6.38 µs (0.09×) | <ins>105 ns (5.76×)</ins> | <ins>105 ns (5.76×)</ins> |
|  | Loop (sum) | 20.0 µs | **<ins>9.99 µs (2.00×)</ins>** | 498 µs (0.04×) | 499 µs (0.04×) | <ins>12.4 µs (1.61×)</ins> | <ins>12.4 µs (1.61×)</ins> |
| Heavy kernels | HeavyArith | 87.2 ms | <ins>44.3 ms (1.97×)</ins> | <ins>50.9 ms (1.71×)</ins> | <ins>51.3 ms (1.70×)</ins> | <ins>24.8 ms (3.52×)</ins> | **<ins>24.5 ms (3.56×)</ins>** |
|  | HeavyRecursion | 5.50 ms | **<ins>3.13 ms (1.76×)</ins>** | <ins>3.60 ms (1.53×)</ins> | 3.70 ms (1.48×) | <ins>3.38 ms (1.63×)</ins> | <ins>3.40 ms (1.62×)</ins> |
|  | HeavyFloatloop | 153 ms | <ins>83.8 ms (1.83×)</ins> | <ins>61.5 ms (2.49×)</ins> | <ins>62.4 ms (2.46×)</ins> | <ins>25.0 ms (6.13×)</ins> | **<ins>24.9 ms (6.14×)</ins>** |
| Realworld small | fib | 5.60 ms | 6.41 ms (0.87×) | 7.33 ms (0.76×) [^p3-gate] | 14.3 ms (0.39×) | **<ins>0.60 ms (9.3×)</ins>** [^seg2seg] | <ins>0.61 ms (9.1×)</ins> [^seg2seg] |
|  | binary-trees | 19.3 ms | 23.9 ms (0.81×) | 26.4 ms (0.73×) [^p3-gate] | 59.9 ms (0.32×) | 16.7 ms (1.16×) [^seg2seg] | **16.6 ms (1.16×)** [^seg2seg] |
|  | spectral-norm | 12.9 ms | 12.2 ms (1.06×) | 13.5 ms (0.96×) [^p3-gate] | 28.3 ms (0.46×) | 10.2 ms (1.26×) | **<ins>2.25 ms (5.74×)</ins>** [^seg2seg] |
|  | fannkuch | 2.46 ms | 3.64 ms (0.68×) | 3.76 ms (0.65×) | 3.72 ms (0.66×) | **<ins>0.34 ms (7.25×)</ins>** | **<ins>0.34 ms (7.27×)</ins>** |
|  | n-body | 30.2 ms | **27.5 ms (1.10×)** | 28.9 ms (1.04×) [^p3-gate] | 50.0 ms (0.60×) | 31.0 ms (0.98×) | 30.9 ms (0.98×) |
| Boundary mini · Call | PureVM | 490 ns | **<ins>77.5 ns (6.32×)</ins>** | — | — | — | — |
|  | CallOnly | **54.0 ns** | 104 ns (0.52×) | 105 ns (0.51×) | 165 ns (0.33×) | 105 ns (0.51×) | 106 ns (0.51×) |
|  | Boundary (+SetGlobal) | **120 ns** | 179 ns (0.67×) | 177 ns (0.68×) | 180 ns (0.67×) | 176 ns (0.68×) | 176 ns (0.68×) |
| Boundary mini · CallInto | PureVM | 490 ns | **<ins>77.5 ns (6.32×)</ins>** | — | — | — | — |
|  | CallOnly | 54.0 ns | **46.4 ns (1.17×)** | 48.7 ns (1.11×) | 103 ns (0.53×) | 48.9 ns (1.11×) | 48.4 ns (1.12×) |
|  | Boundary (+SetGlobal) | **120 ns** | **120 ns (1.01×)** | **120 ns (1.00×)** | 121 ns (1.00×) | **120 ns (1.01×)** | 122 ns (0.99×) |
| Realworld embedded · Call | Predicate (×1000) | **282 µs** | 321 µs (0.88×) | 323 µs (0.87×) | 327 µs (0.86×) | 322 µs (0.88×) | 324 µs (0.87×) |
|  | Transform (×1000) | **212 µs** | 236 µs (0.90×) | 239 µs (0.89×) | 243 µs (0.88×) | 224 µs (0.95×) | 222 µs (0.96×) |
| Realworld embedded · CallInto | Predicate (×1000) | 282 µs | 264 µs (1.07×) | **262 µs (1.08×)** | 269 µs (1.05×) | 265 µs (1.07×) | 263 µs (1.07×) |
|  | Transform (×1000) | 212 µs | 181 µs (1.17×) | 183 µs (1.16×) | 183 µs (1.16×) | **167 µs (1.27×)** | **167 µs (1.27×)** |

[^cat-baseline]: `benchmarks/baseline`. Three self-contained scripts (Simple branch-compare, Arith six-order Horner polynomial, Loop sum 1..N), no Go↔Lua boundary crossing. Shows VM-core dispatch / arithmetic / loop cost under minimum workload.
[^cat-heavy]: `benchmarks/heavy`. Three flat numeric kernels (HeavyArith pure arithmetic, HeavyRecursion self-recursion, HeavyFloatloop nested float loop); intentionally excludes tables, strings, library CALL and other helper-bound structures. Shows the compilation tier's performance ceiling on shapes that actually let it work.
[^cat-realworld]: `benchmarks/realworld`. Five benchmark-game scripts (fib / binary-trees / spectral-norm / fannkuch / n-body); a single-pass semantics run is differential-tested against the official lua5.1.5 (byte-equal). Shows conventional load under a mix of calls / allocations / floats / table ops.
[^p3-gate]: P3 auto carries a helper-density profitability gate (issue #39, 2026-07-03): when a hot proto's op mix is dominated by helper round trips (the wasm→Go boundary cost eats the promotion win), promotion is declined and the proto stays on the interpreter. Rows with this marker declined promotion; the number IS interpreter execution (the delta vs the P1 column is sampling-hook overhead). The P3 force column is unaffected (force-all bypasses the gate to preserve differential coverage).
[^seg2seg]: P4 segment-to-segment CALL dispatch (issue #50, 2026-07-04, delivered on both amd64 and arm64): self-recursive / arith-callee shapes (the fib pattern) used to pay a cross-boundary round trip per call (mmap RET → Go dispatch → host.CallBaseline → mmap re-entry); the caller segment now `call`s directly into the callee segment, which builds/tears its frame in-segment and recurses natively without ever leaving mmap. Same-machine same-batch measurements (2026-07-07, `-benchtime=2s -count=3 -cpu=1` median, over gopher-lua): fib flipped from 0.87× to **10.3×**, spectral-norm from 1.28× to **15.8×** (the inner A/Av/Atv go segment-to-segment; note P4 auto only reaches 2.14× — full promotion under force is needed to capture the whole win), fannkuch **6.9×**. n-body (1.41×) gains little: allocation/GC-bound, its recursive callees carry table ops that stay off the segment-to-segment path. binary-trees' `check` (self-recursion + GETTABLE ArrayHit table reads) unlocked once GETTABLE ArrayHit sites became seg2seg-eligible and the forceAll retry window widened (a recursive proto's deep-pc ICs only warm after a subtree returns) — amd64 went from 1.35× to **1.98×**, arm64 from 0.77× to 1.16× (the remaining bottleneck is bottomup's allocation). The arm64 mirror shipped on the same branch; darwin/arm64 M5 Pro re-measurements (2026-07-07, table below): fib flipped from 0.81× to **9.1×**, spectral-norm from 0.98× to **5.74×**; tracked in issue #61.
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

Three builds, `-count=3` for median, whole set finishes in ~6-10 minutes:

```bash
DIRS='./benchmarks/baseline/ ./benchmarks/heavy/ ./benchmarks/realworld/ ./benchmarks/embedded/'
FLAGS='-run=^$ -benchtime=2s -count=3'

# P1: crescent interpreter (default build)
go test -bench='_(Wangshu|WangshuCall|WangshuCallInto|Gopher)$' $FLAGS $DIRS

# P3: gibbous-wasm (auto goes through _WangshuKernel/_GibbousAuto*; force through _Gibbous*)
go test -tags "wangshu_p3 wangshu_profile" \
    -bench='_(Gibbous|GibbousCall|GibbousCallInto|GibbousAuto|GibbousAutoCall|GibbousAutoCallInto|WangshuKernel)$' \
    $FLAGS $DIRS

# P4: gibbous-jit (auto goes through _GibbousJITAuto*; force through _GibbousJIT*)
go test -tags "wangshu_p4 wangshu_profile" \
    -bench='_(GibbousJIT|GibbousJITCall|GibbousJITCallInto|GibbousJITAuto|GibbousJITAutoCall|GibbousJITAutoCallInto)$' \
    $FLAGS $DIRS
```

CI also carries a `bench-acceptance` workflow that runs Horner-1000 + heavy triplet + boundary Const/Nil six-way across three platforms (linux/amd64, linux/arm64, darwin/arm64):

```bash
gh workflow run bench-acceptance.yml
```

Numbers land as workflow artifacts. Run #28505893556 (2026-07-01) is the P4 PJ11 acceptance baseline, see [p4 09-acceptance-checklist §3](docs/design/p4-method-jit/09-acceptance-checklist.md).

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
