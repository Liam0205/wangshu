# P4 §9: PJ10 验收 Checklist

> 状态: **进行中**。本文是 P4 PJ10 最终验收的可勾选清单, 与 [./08-testing-strategy.md](./08-testing-strategy.md) §2 V1-V22 一一对应。每一项在三个平台 (amd64 / linux-arm64 / darwin-arm64) 上独立勾。
>
> 用法: 每个 V 项分三列, 在对应平台上确认通过后填 ✅。一项任意平台没勾完, PJ10 就不能宣告完成。
>
> 创建于 2026-06-30, 跟随 PR #29 (squashed rewrite of PR #27, darwin/arm64 真机 W^X + 三平台 CI 矩阵) 推进。

---

## 0. 当前 CI 平台覆盖

三个平台都在 PR #29 的 `.github/workflows/ci.yml` 矩阵里:

| 平台 | runner | codepage 路径 | trampoline 路径 |
|---|---|---|---|
| **amd64** | `ubuntu-latest` | `codepage_other.go` (不走) → 用 amd64 后端 | `arch_amd64.go` |
| **linux/arm64** | `ubuntu-24.04-arm` (GHA native) | `codepage_linux.go` (mprotect + 手写 i-cache flush) | `trampoline_real.go` + `trampoline_arm64.s` |
| **darwin/arm64** | `macos-latest` (Apple Silicon M1) | `codepage_darwin.go` (MAP_JIT + pthread_jit_write_protect_np + sys_icache_invalidate via jitcgo 子包) | 同 linux/arm64 (共用 Plan 9 ABI0 asm) |

`darwin/arm64` 是唯一走 cgo + MAP_JIT 路径的平台, 必须单独跑一遍。

---

## 1. 验收项总表 (V1-V22)

每行 4 列: `amd64` / `linux/arm64` / `darwin/arm64` / **如何验**。

### 正确性 (V1-V13, 三方逐字节比对)

CI 里对应 `difftest (p4 / <platform>)` 这组 9 个 job。三方 = `crescent` (P1 解释器) vs `gibbous-jit` (P4) vs 官方 Lua 5.1.5 oracle, 逐字节相等。

PR #29 三平台 9 个 P4 相关 job (test / difftest / fuzz-smoke × 三平台) 全 SUCCESS。覆盖证据 (2026-06-30 核对):
- `test/difftest/p4_test.go::p4Corpus` 共 116 个 P4 专属语料,按形态分类 (PJ5 调用族 89 个 / 表 IC 4 个 / 算术 6 个 / 比较 2 个 / FORLOOP 2 个 / 直线 opcode 3 个 / 等)
- `test/difftest/difftest_test.go::seedCorpus` 共享 V1-V13 通用语料 (在 P4 build 下也跑)
- `test/difftest/gcstress_test.go` GC 压力下 byte-equal (V5/V13)
- `test/difftest/corners_test.go` 角落语义 (V11 协程不升层等)
- `test/difftest/errmsg_test.go` 错误消息 byte-equal (V9 traceback 部分)
- ubuntu (amd64+arm64) `apt install lua5.1` 装 oracle, macos-latest 编译 5.1.5 源码 + cache, 三平台都真比对

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V1 | 直线 opcode byte-equal | ✅ | ✅ | ✅ | `difftest (p4 / <platform>)` 全过; p4Corpus 3 个 case (`p4_const_number/p4_move_arg/p4_loadbool`) |
| V2 | 算术快路径 (f64 + IsNumber guard) | ✅ | ✅ | ✅ | 同上; p4Corpus 8 个 case (算术 6 + 比较 2) |
| V3 | 算术慢路径 (走 helper) | ✅ | ✅ | ✅ | 同上; helper 路径混在 force-all 语料里 |
| V4 | 数值 for (FORPREP/FORLOOP) | ✅ | ✅ | ✅ | 同上; p4Corpus 2 个 case (`p4_for_empty/p4_for_accumulate`) |
| V5 | 回边 GC (gcPending inline) | ✅ | ✅ | ✅ | `gcstress_test.go` 在 P4 build 下也跑 |
| V6 | 表 IC 命中 (单态跳哈希) | ✅ | ✅ | ✅ | 同上; p4Corpus 4 个表 IC case |
| V7 | 表 IC 失效 (gen bump 走 helper) | ✅ | ✅ | ✅ | 同上; force-all 触发 gen bump |
| V8 | 跨层 CALL 链 (jit→jit / jit→crescent / jit→host) | ✅ | ✅ | ✅ | 同上; p4Corpus 89 个 PJ5 调用族 case 全覆盖 |
| V9 | gibbous traceback (帧 pc 物化) | ✅ | ✅ | ✅ | `errmsg_test.go` + `TestP4_ConcurrentForceAll_SpecDeopt` |
| V10 | 闭包 upvalue (CLOSURE/CLOSE) | ✅ | ✅ | ✅ | seedCorpus 共享语料覆盖闭包 |
| V11 | 协程不升层 (tier 恒 Interp/Stuck) | ✅ | ✅ | ✅ | `corners_test.go` coroutine 必做列覆盖 + bridge 单测 |
| V12 | 强制全升 byte-equal (force-all-jit) | ✅ | ✅ | ✅ | `TestP4_Tiered/ConcurrentForceAll/PromotionTriggered` 全过 |
| V13 | GC 压力 fuzz 下 byte-equal | ✅ | ✅ | ✅ | `gcstress_test.go::TestGCStress_*` 在 P4 build 下跑 + `fuzz-smoke (p4 / <platform>)` 30s 不出 mismatch |

### 性能 (V14-V16)

CI 里跑 `.github/workflows/bench-acceptance.yml`, 数字记到本文 §3 表格。

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V14 | 列内核负载 ≥ luajc 档 (164μs Horner 1000 items, 即 ≥ 4.4x over gopher-lua) | ✅ 12.6x | ✅ 25.1x | ✅ 13.5x | bench-acceptance Run #28418158850; 详 §3.V14 |
| V15 | realworld 5 脚本 (fib / binary-trees / spectral-norm / nbody / fannkuch) geomean ≥ P3 geomean ≥ 1.5x | ⚠️ | ⚠️ | ⚠️ | P4 ≥ P3 通过 (geomean P4 0.90 vs P3 0.70, 本机 4 路 gopher/P1/P3/P4 对比); 但 「≥1.5x over gopher」 这条 P1+P3+P4 都没达过 (P1 0.95x / P3 0.70x / P4 0.90x) — 是设计预期偏乐观, 非 P4 引入的退化; 详 §3.V15 + §4 |
| V16 | boundary 往返 ≥ P3 wazero 边界 × 0.95 (不慢过 5%) | ⚠️ | ⚠️ | ⚠️ | 当前 bench P3/P4 body 不同, 无法公平比; 详 §3.V16 + §4 |

### 工程 (V17-V18)

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V17 | 四 build tag 全过: default / `wangshu_profile` / `wangshu_p3` / `wangshu_p4` | ⬜ | ⬜ | ⬜ | CI 的 `test (p1/p3/p4 / <platform>)` 全过 (注: V17 spec 是 4 build, 当前 CI 是 3 build, 见 §4) |
| V18 | -race 多 goroutine 并发, `-count=10` | ⬜ | ⬜ | ⬜ | `test (p4 / <platform>)` job 在 `go test -race ./...` |

### P4 增项 (V19-V22)

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V19 | OSR exit 状态等价 (每 guard 强制失败, exit 后续跑 byte-equal) | ⬜ | ⬜ | ⬜ | `conformance/p4_test.go::TestOSRStateEquivalence` (table-driven 每 guard) |
| V20 | deopt 风暴防抖 (反复 deopt → `P4StuckSpeculation` 吸收态, 不再投机但仍 byte-equal) | ⬜ | ⬜ | ⬜ | `conformance/p4_test.go::TestDeoptStormToStuck` |
| V21 | **三方逐字节比对**: amd64 输出 = arm64 输出 = crescent 输出 | ⬜ | ⬜ | ⬜ | `difftest/p4_test.go::TestDualArchByteEqual` (跨架构) |
| V22 | guard 漏判 fuzz (禁用每条 guard 一次, 看比对能不能抓出来) | ⬜ | ⬜ | ⬜ | `difftest/p4_test.go::FuzzGuardOmission` (build tag `wangshu_p4_guardfuzz`) |

---

## 2. 决议项 (用户决定, 不是测试结果)

| # | 项 | 状态 | 备注 |
|---|---|---|---|
| D1 | bit50 OSR exit 后语义 (清 0 vs 保留 1) | ✅ 清 0 | 2026-06-29 用户确认, 见 [./04-osr-deopt.md](./04-osr-deopt.md) §7.2 |
| D2 | P3 wasm 后端去留 (退役 / 保留为兜底) | ⬜ | V1-V22 全过后由用户决定, 见 [./07-p3-retirement.md](./07-p3-retirement.md) |

---

## 3. 性能数字归档 (V14/V15/V16 实测填入)

### V14 列内核 (Horner 1000 items)

来源: `.github/workflows/bench-acceptance.yml` 跑 `BenchmarkGibbousJIT_PJ3For1000` (P4) 和 `BenchmarkPJ3EmptyLoop1000_Gopher` (gopher-lua), bench_time=2s count=2, 取两次平均 (Run #28418158850, 2026-06-30)。

| 平台 (runner / CPU) | gopher-lua (μs/op) | P4 jit (μs/op) | crescent (μs/op) | P4 over gopher | 达到 luajc 档 (≥4.4x)? |
|---|---|---|---|---|---|
| amd64 (ubuntu-latest, AMD EPYC 9V74) | 995.8 | 79.06 | 894.4 | **12.6x** | ✅ |
| linux/arm64 (ubuntu-24.04-arm, Azure aarch64) | 923.3 | 36.77 | 763.8 | **25.1x** | ✅ |
| darwin/arm64 (macos-latest, Apple M1) | 764.6 | 56.57 | 623.3 | **13.5x** | ✅ |

注: macos M1 的 P4 比 amd64 / linux-arm64 慢, 原因可能是 macos-latest 用 M1 而非 M2/M3, 加上 GitHub Actions runner 是虚拟化 (Apple M1 Virtual), 与 ubuntu runner 性能差异不奇怪。三平台均远超 luajc 档 4.4x 基线。

### V15 realworld geomean

来源: 本机 amd64 Xeon Platinum 24 核, count=3 bench_time=2s 取平均 (2026-06-30)。bench-acceptance Run #28418158850 三平台 CI 数据互相印证, P4 在 0.86-0.89x 区间稳定。

**绝对时间 (ms/op, 越小越快)**:

| 脚本 | gopher-lua | P1 (crescent 解释器) | P3 (wasm) | P4 (jit) |
|---|---|---|---|---|
| fib | 9.38 | 10.19 | **24.79** ⚠️ | 11.10 |
| binarytrees | 33.49 | 36.08 | **25.87** ✅ | 38.72 |
| spectralnorm | 22.28 | 18.35 | **47.31** ⚠️ | 20.39 |
| fannkuch | 4.12 | 5.64 | 5.90 | 5.74 |
| nbody | 46.22 | 45.28 | 44.88 | 45.73 |

**加速比 (gopher / X, >1 表示 X 比 gopher 快)**:

| 脚本 | P1 | P3 | P4 |
|---|---|---|---|
| fib | 0.92x | 0.38x | 0.85x |
| binarytrees | 0.93x | 1.30x | 0.87x |
| spectralnorm | 1.22x | 0.47x | 1.09x |
| fannkuch | 0.73x | 0.70x | 0.72x |
| nbody | 1.02x | 1.03x | 1.01x |
| **geomean** | **0.95x** | **0.70x** | **0.90x** |

**P4 内部对比**:

| 脚本 | P4 vs P1 | P4 vs P3 |
|---|---|---|
| fib | 0.92x (略慢) | **2.23x** (大幅快) |
| binarytrees | 0.93x (略慢) | 0.67x (慢 33%) |
| spectralnorm | 0.90x (略慢) | **2.32x** (大幅快) |
| fannkuch | 0.98x (持平) | 1.03x (略快) |
| nbody | 0.99x (持平) | 0.98x (持平) |

**关键发现**:

1. **P3 极不稳定**: fib 慢到 0.38x (比 gopher 慢 2.6x), spectralnorm 0.47x (慢 2x+), 但 binarytrees 反而最快 (1.30x)。这与 P3 wasm 跨层开销有关 — 小函数频繁调用的脚本 (fib) 受跨层税重挫, 内存压力型 (binarytrees) 反而是 P3 优势区。
2. **P3 整体 geomean 0.70x 比 P1 还慢** (0.95x), 这跟 P3 PW9 历史 0.79x 同一档 (略差是 P3 这段时间又微跌了点)。
3. **P4 geomean 0.90x ≈ P1 (0.95x), 比 P3 (0.70x) 涨**: P4 维度承诺 「P4 ≥ P3」 ✅; 但「≥1.5x over gopher」对当前五脚本工作量结构上不成立 — 这五个脚本是计算密集 + 小函数频繁调用形态, P4 的字节级 inline 加速点 (FORLOOP) 在这些脚本里占比低, gopher-lua 的 register VM 在这种工作量上效率本身就高。
4. **P4 在 fib / spectralnorm 上比 P3 快 2x+**, 但 binarytrees 上反而比 P3 慢 33% — 单脚本逐项检查 P4 在小函数调用密集场景已有大幅改善, 但内存压力型 P3 wasm 反而更省事。

**两条独立断言**:

1. **P4 geomean ≥ P3 geomean**: ✅ 本机 + 三平台 CI 都过 (P4 0.86-0.90x vs P3 0.68-0.77x)
2. **P4 geomean ≥ 1.5x over gopher-lua**: ❌ 都不过 (P4 0.86-0.90x, 距 1.5x 差很远)

**结论**: 设计文档 [./08-testing-strategy.md](./08-testing-strategy.md) §2.2 V15 「P4 geomean ≥ P3 geomean ≥ 1.5x over gopher-lua」 这条预期对当前 wangshu 实现能力 (P3 + P4 都) 偏乐观, 在 realworld 5 脚本工作量上从来没真正达成 (P3 历史就 0.79x)。**P4 维度的承诺 「P4 ≥ P3」 已实现**; 「P4 ≥ 1.5x over gopher」 是设计立项时的预期目标, 实际未兑现。**登记到 §4 不一致的地方**, 待用户在 PJ10 决议时决定: (a) 接受现状, 改 V15 描述为 「P4 ≥ P3」 (b) 视作未达, P4 不收单 (c) 等 P5 trace JIT 兜底。

### V16 boundary 往返

来源: 同上 Run #28418158850。**注意**: 当前 P4 bench (`GibbousJIT_Const/Nil/Bool`) 与 P3 bench (`Simple_Gibbous`) 跑的 body 不一致 (P4 = `return 42` 单条 LOADK+RETURN, P3 = `local a,b=1,2; if a<b ... return r` 五条指令), 直接对比不公平。

| 平台 | P3 边界 (ns/op, simpleBody) | P4 边界 (ns/op, constBody) | P4/P3 | 备注 |
|---|---|---|---|---|
| amd64 | 6402 | 7180 | 1.12x | ⚠️ body 不同, 无法直接比 |
| linux/arm64 | 5871 | 6982 | 1.19x | ⚠️ body 不同 |
| darwin/arm64 | 4007 | 4628 | 1.15x | ⚠️ body 不同 |

**V16 不能直接判断**: 需要 P3 和 P4 跑同一最简 body (`return 42` 类) 才能公平比较, **登记到 §4 不一致的地方**, 待补 P3 const-body bench 或重新定义 V16 测量法。

---

## 4. 和设计文档不一致的地方

(在核对过程中如果发现验收项不能直接打勾, 在此登记原因 + 后续动作)

- **V17 是「四 build」还是「三 build」?** 设计文档 [./08-testing-strategy.md](./08-testing-strategy.md) §2.3 写「四套 build: default + `wangshu_profile` + `wangshu_p3` + `wangshu_p4`」。当前 CI 的 `test` 矩阵只跑 3 个 variant (p1 / p3 / p4); 其中 p1 = default 无 tag, 实际上是覆盖了 default。但「`wangshu_profile` 单独无 p3/p4」这一行没跑 — 待核对时确认是否需要补。

- **V15 P4 realworld geomean 不到 1.5x, 但 P4 ≥ P3 这部分过了**: 设计文档 [./08-testing-strategy.md](./08-testing-strategy.md) §2.2 V15 「P4 geomean ≥ P3 geomean ≥ 1.5x over gopher-lua」 实际上是两个断言:
  - **P4 ≥ P3**: ✅ 本机 + 三平台 CI 都过 (P4 0.86-0.90x vs P3 0.68-0.77x)
  - **≥1.5x over gopher**: ❌ 全都不过 — 实际上**连 P1 都不过** (P1 0.95x, P3 0.70x, P4 0.90x)

  **关键发现**: 这不是 P4 引入的退化, 是设计预期偏乐观。本机 4 路 (gopher / P1 / P3 / P4) 完整对比显示:
  - **gopher-lua 在 realworld 5 脚本上整体最快**, 谁都没真正反超
  - **P1 (crescent 解释器) geomean 0.95x**, 几乎追上 gopher 但没超
  - **P3 geomean 0.70x** 比 P1 还慢 — wasm 跨层开销在 fib (0.38x) / spectralnorm (0.47x) 上重挫, 但 binarytrees (1.30x) 反而是 P3 强项
  - **P4 geomean 0.90x**, 在 P3 ↓ 极差的两个脚本 (fib / spectralnorm) 上把 P3 拉回 P1 水平甚至超 P1, 但 binarytrees 反而比 P3 慢 33% (内存压力型 P3 wasm 反而更省事)

  **realworld 5 脚本工作量本身对 wangshu 不利**: 是计算密集 + 小函数频繁调用工作量, P4 的字节级 inline 加速点 (FORLOOP) 在这些脚本里占比低, gopher-lua 的 register VM + closure dispatch 效率本身就高。设计文档 V14 (Horner 列内核) 测的是单纯 FORLOOP, P4 完胜 (≥12x over gopher), 但 V15 五脚本测的不是 P4 的强项。

  **待用户决议** (PJ10 决议时): (a) 接受现状, 把 V15 描述改成 「P4 ≥ P3」, 1.5x 那条标 「未实现的预期目标」; (b) 视 V15 未达, P4 不收单; (c) 把 1.5x 留 P5 trace JIT 兜底, 当前阶段视 V15 部分达标。

- **V16 boundary 往返 bench 不公平**: 当前 P3 用 `simpleBody` (5 条指令 body), P4 用 `constBody` (1 条 LOADK+RETURN), 工作量差 5 倍, 直接比 ns/op 没有意义。需要补一个 P3 跑 `constBody` 的同款 bench 才能公平比。**下一步**: 在 `benchmarks/baseline/baseline_gibbous_test.go` 加 P3 const-body 对应物, 或重新定义 V16 测量法。

---

## 5. PJ10 完成条件

以下全部勾完才算 PJ10 完成:

- [ ] §1 表格三平台 × V1-V22 全部 ✅
- [ ] §2 D2 P3 退役决议已拍板 (无论退役还是保留)
- [ ] §3 性能数字三平台全部填写完毕
- [ ] §4 不一致的地方 全部澄清 (或登记到 [./implementation-progress.md](./implementation-progress.md) followup)
- [ ] [./implementation-progress.md](./implementation-progress.md) 状态从「在做」改成「P4 PJ10 完成」

---

## 6. 相关引用

- 验收口径正文: [./08-testing-strategy.md](./08-testing-strategy.md) §2 V1-V22 总表
- PJ 里程碑映射: [./06-backends.md](./06-backends.md) §6.1
- 实施进度: [./implementation-progress.md](./implementation-progress.md)
- bit50 决议: [./04-osr-deopt.md](./04-osr-deopt.md) §7.2
- P3 退役框架: [./07-p3-retirement.md](./07-p3-retirement.md)
