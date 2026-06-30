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

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V1 | 直线 opcode byte-equal | ⬜ | ⬜ | ⬜ | `difftest (p4 / <platform>)` 全过 |
| V2 | 算术快路径 (f64 + IsNumber guard) | ⬜ | ⬜ | ⬜ | 同上 |
| V3 | 算术慢路径 (走 helper) | ⬜ | ⬜ | ⬜ | 同上 |
| V4 | 数值 for (FORPREP/FORLOOP) | ⬜ | ⬜ | ⬜ | 同上 |
| V5 | 回边 GC (gcPending inline) | ⬜ | ⬜ | ⬜ | 同上 |
| V6 | 表 IC 命中 (单态跳哈希) | ⬜ | ⬜ | ⬜ | 同上 |
| V7 | 表 IC 失效 (gen bump 走 helper) | ⬜ | ⬜ | ⬜ | 同上 |
| V8 | 跨层 CALL 链 (jit→jit / jit→crescent / jit→host) | ⬜ | ⬜ | ⬜ | 同上 |
| V9 | gibbous traceback (帧 pc 物化) | ⬜ | ⬜ | ⬜ | 同上 |
| V10 | 闭包 upvalue (CLOSURE/CLOSE) | ⬜ | ⬜ | ⬜ | 同上 |
| V11 | 协程不升层 (tier 恒 Interp/Stuck) | ⬜ | ⬜ | ⬜ | 同上 + `test (p4 / <platform>)` 里 bridge 单测 |
| V12 | 强制全升 byte-equal (force-all-jit) | ⬜ | ⬜ | ⬜ | `test (p4 / <platform>)` 含 force-all 路径 |
| V13 | GC 压力 fuzz 下 byte-equal | ⬜ | ⬜ | ⬜ | `fuzz-smoke (p4 / <platform>)` 30s 不出 mismatch |

### 性能 (V14-V16)

CI 里跑 `make bench-p4` 系列, 数字记到本文末尾的 §3 表格。

| # | 描述 | amd64 | linux/arm64 | darwin/arm64 | 如何验 |
|---|---|---|---|---|---|
| V14 | 列内核负载 ≥ luajc 档 (164μs Horner 1000 items, 即 ≥ 4.4x over gopher-lua) | ⬜ | ⬜ | ⬜ | `make bench-p4`, 看 Horner kernel ns/op |
| V15 | realworld 5 脚本 (fib / binary-trees / spectral-norm / nbody / fannkuch) geomean ≥ P3 geomean ≥ 1.5x | ⬜ | ⬜ | ⬜ | `cd benchmarks && go test -bench=. -tags=wangshu_p4` 五脚本几何平均 |
| V16 | boundary 往返 ≥ P3 wazero 边界 × 0.95 (不慢过 5%) | ⬜ | ⬜ | ⬜ | boundary bench P4 vs P3 ns/op 对比 |

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

| 平台 | P1 crescent (μs/op) | P3 wasm (μs/op) | P4 jit (μs/op) | P4 over gopher-lua | 达到 luajc 档 (164μs)? |
|---|---|---|---|---|---|
| amd64 | TBD | TBD | TBD | TBD | ⬜ |
| linux/arm64 | TBD | TBD | TBD | TBD | ⬜ |
| darwin/arm64 | TBD | TBD | TBD | TBD | ⬜ |

### V15 realworld geomean

| 平台 | P3 geomean over gopher-lua | P4 geomean over gopher-lua | P4 ≥ P3 ≥ 1.5x? |
|---|---|---|---|
| amd64 | TBD | TBD | ⬜ |
| linux/arm64 | TBD | TBD | ⬜ |
| darwin/arm64 | TBD | TBD | ⬜ |

### V16 boundary 往返

| 平台 | P3 boundary (ns/op) | P4 boundary (ns/op) | P4 / P3 ≥ 0.95 (不慢过 5%)? |
|---|---|---|---|
| amd64 | TBD | TBD | ⬜ |
| linux/arm64 | TBD | TBD | ⬜ |
| darwin/arm64 | TBD | TBD | ⬜ |

---

## 4. 和设计文档不一致的地方

(在核对过程中如果发现验收项不能直接打勾, 在此登记原因 + 后续动作)

- **V17 是「四 build」还是「三 build」?** 设计文档 [./08-testing-strategy.md](./08-testing-strategy.md) §2.3 写「四套 build: default + `wangshu_profile` + `wangshu_p3` + `wangshu_p4`」。当前 CI 的 `test` 矩阵只跑 3 个 variant (p1 / p3 / p4); 其中 p1 = default 无 tag, 实际上是覆盖了 default。但「`wangshu_profile` 单独无 p3/p4」这一行没跑 — 待核对时确认是否需要补。

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
