# Wangshu（望舒）

望舒是纯 Go 实现的高性能可嵌入的 Lua 5.1 虚拟机。它不依赖 cgo，因此保持了交叉编译能力。

关于命名：Lua 是葡萄牙语中「月亮」的意思；望舒是中国神话中为月亮驱车的神灵（「前望舒使先驱」——《楚辞·离骚》）。为月亮驱车，即驱动 Lua 引擎。是一信达雅的名字。

[![CI](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/ci.yml)
[![Nightly](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml/badge.svg?branch=master)](https://github.com/Liam0205/wangshu/actions/workflows/nightly-diff-fuzz.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Liam0205/wangshu.svg)](https://pkg.go.dev/github.com/Liam0205/wangshu)
[![Go Report Card](https://goreportcard.com/badge/github.com/Liam0205/wangshu)](https://goreportcard.com/report/github.com/Liam0205/wangshu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

## 目标

- 语言标准：实现 Lua 5.1 的核心语言特性。——与 LuaJIT 一致，不追求语言的绝对完整性。
- 正确性：在圈定的语言特性范围，与 Lua 5.1 官方实现的输出逐字节一致。
- 高性能：将 Go 生态的 Lua 执行性能从 gopher-lua 提升至 LuaJ-luajc（Java）甚至 LuaJIT（C++）级别。
- 挂平台：在 Linux/amd64, Linux/arm64, macOS/arm64 测试通过；保留其他平台扩张支持的能力。

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

下表汇总各典型脚本在四档执行模式下的实测数据。每列格式为「wall time · 倍率 (over gopher-lua)」。**auto** 表示走生产热度阈值 + F1-F7 可编译性闸门；**force** 表示 `SetForceAllPromote(true)`，绕过热度阈值强制升层（不绕闸门），差分测试专用，非生产模式。

| 脚本类别 | 脚本 | gopher-lua | P1 (crescent) | P3 auto | P3 force | P4 auto | P4 force |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **纯 VM 微基准** | simple（分支/比较） | 879 ns | 127 ns · **6.95×** | ≈ P1 | ≈ P1 | ≈ P1 | ≈ P1 |
| （benchmarks/baseline） | arith（Horner 多项式） | 968 ns | 175 ns · **5.52×** | ≈ P1 | ≈ P1 | ≈ P1 | ≈ P1 |
|  | loop（求和循环） | 34.8 µs | 17.3 µs · **2.01×** | ≈ P1 | 12 µs · 2.90× | ≈ P1 | 见下 luajc 档 |
| **列内核 luajc 档** | Horner 1000 items（linux/amd64） | 972 µs | 894 µs · 1.09× | — | — | — | 69.0 µs · **14.08×** |
| （bench-acceptance） | Horner 1000 items（linux/arm64） | 928 µs | 764 µs · 1.21× | — | — | — | 36.7 µs · **25.28×** |
|  | Horner 1000 items（darwin/arm64） | 946 µs | 623 µs · 1.52× | — | — | — | 70.9 µs · **13.34×** |
| **heavy 计算内核** | heavy_arith（linux/amd64） | 1.00× (基线) | ≈ 1.6× | — | 1.65× | — | **9.38×** |
| （benchmarks/heavy） | heavy_recursion（linux/amd64） | 1.00× | ≈ 1.4× | — | 1.11× | — | **1.28×** |
|  | heavy_floatloop（linux/amd64） | 1.00× | ≈ 1.9× | — | 4.97× | — | **14.11×** |
|  | heavy geomean（linux/amd64） | 1.00× | — | — | 2.32× | — | **5.53×** |
|  | heavy geomean（linux/arm64） | 1.00× | — | — | — | — | **5.45×** |
|  | heavy geomean（darwin/arm64） | 1.00× | — | — | — | — | **4.00×** |
| **realworld small** | fib | 9.13 ms | 9.91 ms · 0.92× | 24.8 ms · 0.38× | 24.8 ms · 0.38× | 11.1 ms · 0.85× | 18.1 ms · 0.50× |
| （benchmarks/realworld） | binary-trees | 31.3 ms | 34.8 ms · 0.90× | 25.9 ms · 1.30× | 25.9 ms · 1.30× | 38.7 ms · 0.87× | 58.2 ms · 0.54× |
|  | spectral-norm | 21.1 ms | 17.5 ms · **1.20×** | 47.3 ms · 0.47× | 47.3 ms · 0.47× | 20.4 ms · 1.09× | 20.4 ms · 1.09× |
|  | n-body | 44.1 ms | 41.8 ms · **1.05×** | 44.9 ms · 1.03× | 44.9 ms · 1.03× | 45.7 ms · 1.01× | 65.6 ms · 0.67× |
|  | fannkuch | 3.97 ms | 5.28 ms · 0.75× | 5.90 ms · 0.70× | 5.90 ms · 0.70× | 5.74 ms · 0.72× | 8.60 ms · 0.46× |
| **边界往返** | Boundary（+1× SetGlobal） | 180 ns | `CallInto` 176 ns · **1.03×** | 224 ns · 0.80× | 224 ns · 0.80× | 149 ns · **1.21×** | 149 ns · **1.21×** |
| （benchmarks/embedded） | Predicate（per-item × 1000） | 442 µs | `CallInto` 396 µs · **1.12×** | ≈ P1 | ≈ P1 | ≈ P1 | ≈ P1 |
|  | Transform（per-item × 1000） | 312 µs | `CallInto` 273 µs · **1.15×** | ≈ P1 | ≈ P1 | ≈ P1 | ≈ P1 |

数字来源：

- **P1 / P3 auto/force / realworld / 边界往返**：Xeon 6982P-C, go1.26.2, `-count=3 -benchtime=2s`, 2026-06-16 (详 [p3 implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md))。
- **列内核 luajc 档 / heavy 计算内核（三平台 P4）**：GitHub Actions bench-acceptance workflow Run #28505893556 (2026-07-01)，ubuntu-latest (linux/amd64, AMD EPYC 7763) + ubuntu-24.04-arm (linux/arm64, Azure Ampere) + macos-latest (darwin/arm64, Apple M1 Virtual)。

关键读数：

- **luajc 档兑现**：P4 在 Horner-1000 列内核形状上三平台稳定超过 luajc 档 4.4× 基线（14×/25×/13×），达成 [roadmap](docs/design/roadmap.md) §1 「≥ 164 µs 水位」量化承诺。
- **heavy 内核数量级提升**：P4 在裸算术 / 递归 / 浮点循环三档 geomean 4-5.5× over gopher-lua，且每档均 P4 ≥ P3；这是 PJ10 native emit 的直接收益。
- **realworld helper-bound 是结构性等价**：五脚本 P4 geomean ≈ P3 ≈ 0.83× over gopher-lua，非退化，而是表 / 字符串 / library CALL 的宿主侧开销占主导，升层无法摊薄。三档间大致齐平即视为符合预期。
- **边界往返 P4 反超 P3 wazero**：P4 自管 trampoline 边界比 P3 wazero 边界快 1.4-2.0×，符合物理预期。
- **微基准 auto/force 一致**：micro-bench 脚本一次跑完，热度阈值未触达，auto 数字 ≡ P1；force 用来跨过阈值观察升层后行为。

**⚠️ P3 在部分 realworld 脚本上出现负向**（fib 0.38×、spectralnorm 0.47×）：wasm 跨界税对小函数密集调用形态不可摊销，属于结构性负担；`wangshu_p3` build 加了 `wangshu_profile` 后即使自然不升层也会带 ~28% 采样税。生产环境若确认负载是短脚本 / 高频调用形态，用默认 build（P1）更优。

`make bench` 复现主模块 baseline；`make -C benchmarks/heavy bench` 复现 heavy 内核；bench-acceptance workflow 可手动触发跨三平台跑（`gh workflow run bench-acceptance.yml`）。

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

四档均通过 build tag 选择，源码零改动。默认 build 就是 P1；启用 P3/P4 需要显式带 tag，同 build 里默认走 auto（生产热度阈值 + F1-F7 可编译性闸门），用 `SetForceAllPromote(true)` 切到 force（绕开热度阈值，非生产模式，用来跑差分测试与 benchmark）。

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

`SetForceAllPromote` 只绕过热度阈值，**不**绕过 F1-F7 可编译性闸门（协程、顶层 vararg、含 `ReasonUnknownCall`、含 VARARG opcode 的 proto 依然不升层）。升不动的 proto 无声降级回 P1 解释器，输出层间 byte-equal 不变。

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

短脚本高频调用形态推荐用 `CallInto` 复用返回值切片，走零分配路径：

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
3. **差分随机 fuzz**：nightly-diff-fuzz workflow 每晚 2M 条随机脚本 vs Lua 5.1.5 oracle 对拍（P1 + P3 + P4 三档并行）。
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

其余「存在但不逐字节比」的项（`collectgarbage("count")` / `gcinfo` / `os.time` / `os.clock` / `os.date("%Y")` / `io.write` / `loadfile` 返错形态）由 `TestApprox_ExistenceOnly` 只断言形态不比值。

## 文档导航

按角色路径：

- **想用起来**：本 README 「快速开始」→ [pkg.go.dev](https://pkg.go.dev/github.com/Liam0205/wangshu) 的 `Compile` / `Program.Run` / `Program.Call` / `State.CallInto` API 参考。
- **想理解架构**：[docs/design/architecture.md](docs/design/architecture.md)（包布局 / 组件依赖 / tier 映射）→ [docs/design/roadmap.md](docs/design/roadmap.md)（动机 / 校准测量 / 演进路线 / 非目标）。
- **深入某一层**：
  - P1 解释器（13 篇）：[docs/design/p1-interpreter/00-overview.md](docs/design/p1-interpreter/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p1-interpreter/implementation-progress.md)
  - P2 分层桥（7 篇）：[docs/design/p2-bridge/00-overview.md](docs/design/p2-bridge/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p2-bridge/implementation-progress.md)
  - P3 gibbous-wasm（10 篇）：[docs/design/p3-wasm-tier/00-overview.md](docs/design/p3-wasm-tier/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md)
  - P4 gibbous-jit（11 篇 + progress）：[docs/design/p4-method-jit/00-overview.md](docs/design/p4-method-jit/00-overview.md) 起 · 进度对账 [implementation-progress](docs/design/p4-method-jit/implementation-progress.md) · PJ11 验收 [09-acceptance-checklist](docs/design/p4-method-jit/09-acceptance-checklist.md)
  - P5 trace JIT（尚未实现）：[docs/design/p5-trace-jit.md](docs/design/p5-trace-jit.md)（轮廓设计）
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
