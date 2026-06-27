# P3 §1:开工前置 spike——wazero call boundary 实测 < 150ns(闸门)

> 状态:**设计阶段,详细设计**(本文是 P3 文档集 [00-overview](./00-overview.md) §0 列出的「开工闸门」单一事实源;凡涉 wazero 具体 API 处标注「待 spike 验证」)。
> 本文是 P3 的**生死闸门**:wazero call boundary 实测 < 150ns 才允许 [02-translation](./02-translation.md) 起的 PW1 翻译器开工;不达标走 [../p4-method-jit/01-launch-judgment](../p4-method-jit/01-launch-judgment.md) §2 的「跳跃路径」(P4 自建分层骨架)。
> 上游契约:[../roadmap.md](../roadmap.md) §1(校准测量——LuaJIT 154μs vs luajc 164μs 仅 6% 是 150ns 阈值的论据)、§2(四项税——wazero 已验证项是 spike 顺带验证的项)、§4(P3 阶段定义与前置 spike 措辞);[00-overview](./00-overview.md) §1(P3 边界——spike 验证的物理事实)、§4 PW0(本文承担)、§8(四项税外包是 P3 选 wazero 的本质)。
> P1 依赖面:[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §1.1(arena backing 来源——spike 顺带验证 [03-memory-model](./03-memory-model.md) 的收养机制)。
> P2 依赖面:[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P2 决策机产出——spike 通过即可消费 P2 的 try-compile 请求)。
> 下游衔接:[02-translation](./02-translation.md)(spike 通过后 PW1 起的翻译器主体)、[../p4-method-jit/01-launch-judgment](../p4-method-jit/01-launch-judgment.md) §2(spike 不达标时的跳跃路径接管点)。
>
> **本文定位一句话**:**P3 不是从写翻译器开始,而是从一次小到极致的 spike 实测开始**——三档样本一次跑完,数据决定 P3 是开工、跳过还是走混合路径。

对应 Go 包:`internal/gibbous/wasm/spike`(spike 完成后归档至 `benchmarks/spike/p3-pw0/`,临时目录退役)。

---

## 0. 定位:闸门是单点决策不可绕过

### 0.1 为什么 spike 先于一切翻译工作

[../roadmap.md](../roadmap.md) §4 的 P3 段落唯一一句加粗:**「开工前置 spike:wazero call boundary 实测(目标 <150ns),不达标则跳过本阶段直接做 P4」**。这是项目从立项第一天就定下的硬规则,不是后置补丁。理由有三:

1. **P3 的全部价值挂在「跨层不贵」这一物理前提上**。[00-overview](./00-overview.md) §1 的一句话定位 P3:「在不用调试机器码的后端上,把 P2 决策机产出的 gibbous 代码请求兑现成 Wasm 可执行码,跑通整套分层骨架(升层 / fallback / trampoline / 跨层差分)」——若 trampoline(crescent↔gibbous)的成本与宿主边界([../roadmap.md §2](../roadmap.md) 的几十~百 ns 固定成本)同档,「解释↔编译交错执行」的形态死亡,P3 退化为「整程序编译」一条路,而那条路 P4 原生后端做得更好(无 Wasm 语义中介)。本文 §3 给出 150ns 阈值的精确论证。

2. **6-12 人月的人力投入不能押在未验证的假设上**([00-overview §5](./00-overview.md))。spike 0.5-1 人月的代价,换 P3 后续 PW1-PW9 是否值得投入的硬数据。即便 spike 不达标(走 P4 跳跃路径,[../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md)),这次投入仍**值得**——它把决策建立在实测而非乐观估计上,[../roadmap.md §5](../roadmap.md) 原则 3「每阶段独立交付价值,任何闸门处停下都不亏」的字面体现。

3. **wazero 是 P3 选型的物理前提,而不是细节**([00-overview §8](./00-overview.md) + [../roadmap.md §2](../roadmap.md))。P3 把四项税(GC 精确栈扫描 / 异步抢占 / 栈移动 / 写屏障)整体外包给 wazero。wazero 已验证四项税自身的解决方案,但**没有显式表态「跨边界往返成本」适配分层 VM 的频次**——分层 VM 的边界发生频率(列内核外每个慢路径助手 / 未编译被调一次)显著高于「应用→Wasm 模块」常规嵌入用法的边界频次。spike 就是替我们补这一项数据。

### 0.2 闸门是单点决策不可绕过

承 [../p2-bridge/00-overview §1](../p2-bridge/00-overview.md) 的「决策机不在热路径」哲学,本文这台「闸门决策机」也只在 P3 项目启动一刻运转一次:

| 性质 | 含义 |
|---|---|
| **一次性** | spike 只跑一次产出三档数据(S1/S2/S3),数据进 [implementation-progress](./implementation-progress.md) 永久存档 |
| **单点** | 闸门是 P3 PW0 的唯一产出物,无并行任务、无可绕过路径(任何「等下再说」都是把不确定性带进 PW1) |
| **决策不可逆** | 走开工(走 P3)还是走跳跃(走 P4),进入 PW1 后**无回头**——回头意味着推翻 PW1 投入,违反 [../roadmap.md §5](../roadmap.md) 原则 3 |
| **数据进档** | spike 三档数据 + 决策报告进 [implementation-progress](./implementation-progress.md);P4 验收时若反向决议「P3 退役」,本闸门数据是「P3 是否本就该跳过」的回顾依据 |

### 0.3 战略价值与闸门的双向性

[00-overview §1](./00-overview.md) 引用 [../roadmap.md §4](../roadmap.md) 反复强调 P3「战略价值不在倍率,在跑通分层机器」——但这条战略价值的兑现**前提**仍是分层机器各部件能在合理代价下相互调用。spike 闸门是这条前提的**物理校核**:

- **若闸门通过**:P3 的「不用调试机器码就能跑通分层骨架」战略价值兑现路径打通,PW1-PW9 可启动;P4 接力时只换发射后端([../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md) 常规路径),节省「分层骨架 + 机器码后端」同步啃两块硬骨头的复杂度。
- **若闸门不通过**:跳跃路径下,P3 的战略价值移交 P4 自建([../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md));但本文 §2-§7 设计的分层协议(trampoline 入口签名、status 链错误冒泡、CallInfo bit50、线程级 tier 规则等)**不丢失**——P4 仍消费这套设计,只把发射后端从 wazero 替成原生 codegen。这是 [../roadmap.md §5](../roadmap.md) 原则 3 的另一面体现:**设计资产的复用性独立于执行后端选择**。

**对偶面:P4 立项判定双向性**(2026-06-28,承 [P4 implementation-progress §2 RJ-15](../p4-method-jit/implementation-progress.md) 跨文档回填请求):P3 spike 闸门双向性([../p4-method-jit/01-launch-judgment §0.3](../p4-method-jit/01-launch-judgment.md))与 P4 立项判定双向性同源逻辑——两者都是 P4 阶段的**闸门级单点决策**,但承担不同时点的不同决策(spike 在 P3 开工前 / 立项在 P4 实施前 / 去留在 P4 验收后,详见 [P4 07 §0.3](../p4-method-jit/07-p3-retirement.md) 对偶面表)。

### 0.4 与 P1/P2 落地状态的关系

[00-overview §7](./00-overview.md) 已对账过:P1 全卷已交付(M0-M14)+ P2 PB0-PB7 全过线 + P2 后续优化轮 #1-#4 全过线(2026-06-13)。这意味着:

- **PW0 启动条件已具备**:P1/P2 不阻塞 spike;spike 只依赖 wazero 库本身可用(Apache 2.0,Go module 可直接 import)。
- **spike 不依赖 P3 翻译器骨架**:三档样本均是手写 WAT(或直接编译好的 wasm 字节)+ 手写 Go 调用代码,**不经** [02-translation](./02-translation.md) 的 Compiler 路径。
- **spike 数据可被 [02-translation](./02-translation.md) 引用**:PW1 起的 trampoline 入口签名(`(func $proto_N (param $base i32) (result i32))`,见 [04-trampoline §2](./04-trampoline.md))已是 spike S2 形状的复刻——spike 通过即代表该入口形状的成本可控,PW1 直接采用。

---

## 1. 测什么:三档样本设计

### 1.1 「call boundary」的精确含义

call boundary = **Go 进程内 Go 代码↔ wazero 编译生成码的一次往返**——具体一次包含:

```
Go 侧:  api.Function.Call(ctx, args...) 调用入口
   │
   ▼  (跨层 ①:Go → wazero 生成码)
wazero 侧: trampoline 序言(参数解包 + Wasm 栈帧建立)
   │
   ▼
wazero 生成码: 用户函数体执行(spike 样本里函数体极小,目的是测纯边界成本)
   │
   ▼  (跨层 ②:wazero 生成码 → Go)
wazero 侧: trampoline 尾声(返回值打包 + Wasm 栈帧拆除)
   │
   ▼
Go 侧:  api.Function.Call 返回
```

「一次往返」= 跨层 ① + 跨层 ②(及两端的 trampoline 序言/尾声)。spike 测的是这一对儿的**总成本**,不拆解中间各项。「成本」用 wall-clock ns/op 衡量(Go benchmark `b.N` 循环内多次调用,均摊后单次成本)。

### 1.2 三档样本与各自定位

| 样本 | 形状 | 对应真实场景 | 目标 |
|---|---|---|---|
| **S1 空往返** | Go 调一个空 Wasm 函数(无参无返回)返回 | 跨层固定成本下限——参数零、副作用零 | < 80ns |
| **S2 带参往返** | Go 传 1 个 i32(`base` 字节偏移),Wasm 体内 `i64.load` + `i64.store` 各一次,返回 i32 status | gibbous 函数入口的真实形状([04-trampoline §2](./04-trampoline.md))——**主指标** | **< 150ns** |
| **S3 反向往返** | Wasm 内 `call` 一个 imported Go 函数(空体),imported 函数立即返回 | gibbous→helper 的形状([04-trampoline §3](./04-trampoline.md))——慢路径助手成本 | < 150ns |

**为什么是这三档**:
- **S1 是物理下限**——不带参数即可跑出 wazero 自身边界 trampoline 的纯成本(参数解包/打包不参与)。S1 与 S2 之差 = 「带 1 个 i32 参数 + 一对 memory load/store」的增量,可用于反推 wazero 参数处理是否有显著开销。
- **S2 是主指标**——它精确复刻 [04-trampoline §2](./04-trampoline.md) 的 crescent → gibbous 入口签名(`(func (param $base i32) (result i32))`),且 Wasm 体内一对 memory 读写复刻 [02-translation §2.3](./02-translation.md) MOVE 等基本 opcode 的最小工作量。S2 通过即说明「真正的 gibbous 入口形状」可控。
- **S3 是反向边界**——P3 慢路径助手(arith/gettable/call/safepoint 等,见 [04-trampoline §3](./04-trampoline.md))全部经 imported Go 函数实现。若 S3 ≫ S2,则慢路径助手成本被放大,影响 §2 摊销模型的 `T_cross` 估计。

### 1.3 三档样本的 wazero API 调用骨架(待 spike 验证)

> 以下 API 是 wazero v1.x 公开门面的合理推断;**精确签名 / 模式开关 / 内存操作语义待 spike 验证**(§8 缺口)。

**S1 空往返骨架**:

```go
// 准备阶段(b.N 循环外):
ctx := context.Background()
rt := wazero.NewRuntimeWithConfig(ctx,
    wazero.NewRuntimeConfigCompiler())     // 强制编译模式,非解释器(待 spike 验证)
defer rt.Close(ctx)

// 模块包含一个空函数:(module (func (export "noop")))
mod, _ := rt.Instantiate(ctx, s1Wasm)
fn := mod.ExportedFunction("noop")

// 测量阶段(b.N 循环内):
for i := 0; i < b.N; i++ {
    _, err := fn.Call(ctx)                 // 一次空往返
    if err != nil { b.Fatal(err) }
}
```

**S2 带参往返骨架**:

```go
// 模块:
//   (module
//     (memory (export "mem") 1)
//     (func (export "rw") (param $base i32) (result i32)
//       (i64.store offset=0 (local.get $base)
//         (i64.load offset=8 (local.get $base)))
//       (i32.const 0)))     ;; status=0
mod, _ := rt.Instantiate(ctx, s2Wasm)
fn := mod.ExportedFunction("rw")
mem := mod.ExportedMemory("mem")

// 预填两个 i64 槽(模拟 base[0] / base[1] 处的值)
mem.WriteUint64Le(0, 0xDEADBEEF)
mem.WriteUint64Le(8, 0xC0FFEE)

for i := 0; i < b.N; i++ {
    _, err := fn.Call(ctx, 0)               // base = 0
    if err != nil { b.Fatal(err) }
}
```

**S3 反向往返骨架**:

```go
// 注册 imported Go 函数(空体):
hostMod := rt.NewHostModuleBuilder("env").
    NewFunctionBuilder().
    WithFunc(func() {}).                    // 空 host fn
    Export("h_noop").
    Instantiate(ctx)

// 模块:
//   (module
//     (import "env" "h_noop" (func $h))
//     (func (export "callout") (call $h)))
mod, _ := rt.Instantiate(ctx, s3Wasm)
fn := mod.ExportedFunction("callout")

for i := 0; i < b.N; i++ {
    _, err := fn.Call(ctx)                  // Wasm 体内 call $h_noop 然后返回
    if err != nil { b.Fatal(err) }
}
```

### 1.4 工具与纪律

- **测量框架**:Go testing benchmark(`go test -bench=. -benchtime=10s`)。
- **wazero 模式**:**强制编译模式**(`wazero.NewRuntimeConfigCompiler`)——若仅用解释模式跑出的数据,与 P3 实际生产形态不符,数据无效。模式开关的具体 API 待 spike 验证(§8 缺口)。
- **CPU 频率**:实测前 `cpupower frequency-set --governor performance` 锁频,避免动态频率漂移。
- **样本量**:`-benchtime=10s` 或 `-benchtime=100x` 的 b.N(取较大),确保 ns/op 噪声 < 5%。
- **多次取稳定值**:同一 spike 样本跑 ≥ 5 次,取中位数;离群值(> ±20%)说明环境干扰,需排查后重测。
- **隔离干扰**:关浏览器、关 IDE、关其它后台进程;`taskset -c <pin>` 绑核。
- **wazero 版本固定**:`go.mod` 锁定 wazero version(spike 数据进 [implementation-progress](./implementation-progress.md) 时随版本号一起记录);未来 wazero 升级需重跑 spike(§6.5 提及的「跨版本退化」防御)。

### 1.5 spike 不测什么(范围警告)

为避免 PW0 膨胀变成「半个 PW1」,本 spike 显式排除以下:

- **不测翻译器**:三档 wasm 模块均手写 WAT(或 wat2wasm 编译),不经 [02-translation](./02-translation.md) 的 Compiler 路径——翻译器是 PW1 起的活。
- **不测 P2 决策**:[../p2-bridge](../p2-bridge/00-overview.md) 的 ProfileData / Compilability / TierState / installGibbous 全部不参与;spike 是闭环的小 benchmark。
- **不测真实 Lua 程序**:不跑 fib/binary-trees/spectral-norm 等基准——这是 PW9 的活;PW0 只验「跨层成本是否在档」。
- **不测 GC 联动**:S2 的 memory 共享只验「读写一对 i64 不出错」,不模拟 [05-safepoint-gc](./05-safepoint-gc.md) 的 gcPending 检查、GC 根扫描等——这些走 §4 的「四项税顺带验证」做最小验证即可。
- **不测错误传播**:status 链冒泡(见 [04-trampoline §4](./04-trampoline.md))在 S2 的 return i32 已经预留接口形状,但不验「错误穿越多帧」的复杂场景。

### 1.6 三档样本的物理学:每档具体在压什么

| 档 | 主要在压的 wazero 内部机制 | 与真实 P3 形态的对应 |
|---|---|---|
| **S1** | trampoline 序言/尾声(参数 = 0,函数体 = 0,纯边界开销) | 退化场景——P3 实际入口至少传 `base i32`,S1 用作下沿基线 |
| **S2** | trampoline + 1 个 i32 参数解包 + linear memory load/store | [04-trampoline §2](./04-trampoline.md) crescent → gibbous 入口的最小形态;每个 gibbous 函数被调一次的成本 |
| **S3** | trampoline(Wasm→Go)+ host fn dispatch + trampoline(Go→Wasm) | [04-trampoline §3](./04-trampoline.md) gibbous → host 慢路径助手的成本;一次 imported 函数调用 |

**S1 与 S2 之差**(预期 30-50 ns):
- 1 个 i32 参数解包(从 wazero 内部参数寄存器到 Wasm 函数 local)
- 1 次 `i64.load offset=0` + 1 次 `i64.store offset=8`(linear memory 操作)
- 共 ≈ 2-4 条 Wasm 指令,wazero 编译为 4-8 条机器指令,纳秒级

如果 S2 - S1 显著大于 50 ns(如 > 100 ns),说明 wazero 的参数处理或 memory 访问路径有意外开销,§6.4 失败分析需深查。

**S2 与 S3 之差**(预期 ±30 ns):
- S2 是「Go → Wasm 函数 → return Go」,边界 1 进 1 出
- S3 是「Go → Wasm 函数 → call host fn → return Wasm → return Go」,边界 1 进 1 出 + 1 出 1 进
- 直觉上 S3 应该比 S2 贵约 1-2 倍;若 S3 远高于 2 倍 S2,说明 imported 函数 dispatch 实现有问题

### 1.7 prior art:用 spike 决定方向的先例

「先做最小 spike 验证关键假设,再决定是否投入大块开发」是工程实践常见模式,在分层 VM 领域有 prior art:

| 项目 | spike 决策 | 与本文对照 |
|---|---|---|
| **wazero 自身** | 早期决策选「编译模式」而非「解释模式」时,做过类似 spike 验证「Go 进程内编译机器码可行 + 性能足以替代 cgo Wasm 引擎」 | wazero 的 spike 通过反过来给 P3 提供「四项税外包」的物理基础([00-overview §8](./00-overview.md)) |
| **V8 Sparkplug** | 设计阶段 spike 验证「单遍模板编译能否给到接近 Maglev 50% 的收益」——spike 通过才投入正式开发 | 同样是「先 spike 后开发」模式;Sparkplug spike 主要测「编译时间 + 加速比」,P3 spike 测「跨层成本」 |
| **JSC LLInt** | 设计阶段 spike 验证「在 LLVM IR 之外手写汇编解释器能否比 C 解释器显著快」 | 类似的「先做最小测试论证关键假设」模式 |

P3 spike 的特殊性:**单一闸门指标决定整阶段去留**(`S2 < 150ns` 是单一二元判定),而非 V8/JSC 的「多指标综合判断」——这一简化的代价是若指标本身有误判风险([../roadmap.md §1](../roadmap.md) 校准测量已规避大部分误判风险,见 §3.3)。

---

## 2. 跨层摊销模型:为什么跨层成本决定 P3 收益

### 2.1 模型推导

设热循环每迭代执行 `I` 条字节码指令,编译使每指令开销从解释器档 `c_interp` 降到编译档 `c_wasm`,每迭代发生 `k` 次跨层(慢路径助手 / 未编译被调 / 分配助手 / 强制 safepoint),每次跨层成本 `T_cross`:

```
每迭代净收益(单位:ns)
  = I·(c_interp − c_wasm)            ← 编译消除的解释开销
    − k·T_cross                       ← 跨层成本

正收益条件:I·(c_interp − c_wasm) > k·T_cross
```

`(c_interp − c_wasm)` 是「解释税」——主要是取指 / 译码 / dispatch 跳转 / pc 维护([../p1-interpreter/05-interpreter-loop §2](../p1-interpreter/05-interpreter-loop.md))。基于 P1 落地数据的粗估,`c_interp` ≈ 10-30 ns/指令,`c_wasm` ≈ 1-3 ns/指令(直线代码 + 编译期立即数),`Δc` ≈ 10-25 ns/指令。

### 2.2 列内核形态(k ≈ 0):理想形状

[../roadmap.md §1](../roadmap.md) 与 [00-overview](./00-overview.md) §2 反复强调的「列内核形状」:循环写在 Lua 内,一次调用进一次 VM,整批数据在 VM 内迭代。这种形态下:

- 整个热闭包(N 个 item 的循环体)编译进同一 Wasm 函数,**`k ≈ 0`**(仅分配 / IC miss 偶发,不在每迭代发生)。
- 跨层成本 `T_cross = 150ns` 摊到整批 `N` 个 item:平均每迭代多担 `150ns / N`,`N = 1000` 时仅 0.15 ns/item,**完全可忽略**。
- 收益完整兑现:`每迭代净收益 ≈ I·Δc ≈ I·15ns`。

**典型 `I` 值**(从 P1 benchmark 估算):
- fib(25):递归形,每层 fib(n)/fib(n-1)/fib(n-2) + 比较 + 加法,约 8-12 字节码指令 / 调用,每迭代 ≈ 10 条。
- binary-trees:树构造与遍历混合,递归边 ≈ 15-25 条 / 迭代。
- spectral-norm 循环体:点积 + 平方根,约 20-40 条 / 迭代。
- 列内核典型 Horner 多项式([../roadmap.md §1](../roadmap.md) 校准测量 1):每次 evaluate 5 次乘加,约 15-20 条 / 迭代。

`I ≈ 20`、`Δc ≈ 15ns` 时,每迭代净收益约 300ns,即便 `k = 1`、`T_cross = 150ns`,仍有 150ns 净收益(2x 加速)。**这正是「相对 P1 再 ≥2x」验收门槛([../roadmap.md §4](../roadmap.md))的物理基础**。

### 2.3 劣化形态(k ≥ 1):收益被吃光

若热循环的每次迭代都跨层(典型病态:循环体内每次都调一个未编译被调函数,或每次都触发 metamethod 走助手),**`k ≥ 1`**:

- `k·T_cross = 150ns`(假设 `T_cross = 150ns`,`k = 1`)
- `I·Δc ≈ 20 × 15ns = 300ns`
- 净收益 ≈ 150ns,加速比仅 1.5x——**吃不到 2x 验收门槛**。

更糟的形态:若 `k ≥ 2` 或 `T_cross > 300ns`,净收益归零甚至为负——**编译反而比解释慢**。这正是:
- [02-translation §1.1](./02-translation.md) 「翻译单位覆盖整个热闭包」(每 Proto 一个 module)——避免 Proto 间相互调用经 Go 中转产生不必要的跨层。
- [06-ic-feedback-consume §1](./06-ic-feedback-consume.md) 「IC 快路径内联避免跨层」——单态点直接出表,失败才走助手。

两条设计动机的物理来源。

### 2.4 边界形态(150 ± 30 ns)的混合策略

若 spike 实测 S2 落在 150 ± 30 ns(120-180 ns)的边缘区间,模型预测:
- 列内核理想形态(`k ≈ 0`)仍能兑现 ≥2x。
- 含若干跨层点的「非理想列内核」(`k = 1-2`)收益降到 1.2-1.8x,**部分兑现**。
- 含密集跨层的「劣化形态」(`k ≥ 3`)无收益甚至负收益。

这正是 §5.3 边缘混合策略的物理依据:**只编译「自包含热闭包」**(P2 可编译性分析加一条「调用密度」启发,识别 `k ≈ 0` 形状),交错形态(`k ≥ 1`)不升层。

### 2.5 `T_cross` 在分层 VM 与常规嵌入用法的频次差

[../roadmap.md §2](../roadmap.md) 推论 VM↔宿主边界是「几十~百 ns 固定成本」。常规嵌入用法(应用调用 Wasm 模块)的边界发生在「应用→模块」入口与「模块→应用」出口,对模块体内大量计算而言**每模块一次**,摊销天然。

分层 VM 的边界发生在 `crescent ↔ gibbous ↔ host fn`:

| 边界类型 | 发生频次 | 备注 |
|---|---|---|
| crescent → gibbous(升层入口) | 每次「调用已编译 Proto」一次 | 与 Lua 调用频次同阶,可能很高 |
| gibbous → crescent(调未编译被调) | 每次「已编译 Proto 调未编译 Proto」一次 | 列内核理想形态下罕见;但被调动态变化(如表存的函数)时不可控 |
| gibbous → host fn(调 Go 助手) | 每次「慢路径触发」一次 | 算术 metamethod / arena 分配 / IC miss / 强制 safepoint 等 |
| gibbous → host fn(stdlib) | 每次 stdlib 调用一次 | string.format / table.insert / math.* 等 |

**最坏情况**(per-item 形态):每个 item 的处理都触发 1-3 次跨层,`T_cross` 直接摊到 item 级。这正是 [../roadmap.md §1](../roadmap.md) 校准测量 1「per-item 跨界形态下边界跨越主导成本」的字面体现——**150ns 阈值之所以紧,正是为了让分层 VM 在非理想形态下也保留一定容忍度**。

### 2.6 模型在 P4 跳跃路径下的复用

§5.4 表中已列「跨层摊销模型」是 P4 跳跃路径下复用的设计资产之一,本节给出复用细节:

P4 原生发射(无 Wasm 中介)的跨层成本可能更低,但模型形式不变:

```
P4 形态下:
  c_interp ≈ 同 P3(P1 解释器档)
  c_native < c_wasm(原生码不经 Wasm 语义中介,无 wazero 抽象层)
  T_cross_native ?? T_cross_wasm(原生 ABI 直跳 vs wazero call)

  每迭代净收益 = I·(c_interp − c_native) − k·T_cross_native
```

P4 的跨层成本档位需要 P4 自己的 spike 测——但「与宿主同档则失败」的 §3.1 论证仍适用。**模型的形式独立于发射后端选择**——这是「设计资产复用性」的具体体现([../roadmap.md §5](../roadmap.md) 原则 3)。

---

## 3. 150ns 阈值的来源

### 3.1 上沿:与宿主边界同档则失败

[../roadmap.md §2](../roadmap.md) 的「几十~百 ns 固定成本」是**宿主边界**的成本——应用 Go 代码 ↔ Wasm 模块的一次往返。150ns 是「百 ns 档」的**上沿**;若 wazero call boundary 实测达到甚至超过这一档,意味着分层 VM 的 `crescent ↔ gibbous` 边界**与宿主边界同档**,§2.5 的频次差被放大成成本灾难——分层 VM 的高频边界承担了与低频宿主边界相同的单次成本,加速比被吃光。

形式化:若 `T_cross_gibbous ≥ T_cross_host`,则
- 「整程序在 gibbous 跑」与「整程序在 host 直接调一次 wasm 模块跑」无成本差异;
- 分层(部分 gibbous + 部分 crescent)反而劣于「全 gibbous」——因为多了 crescent ↔ gibbous 边界。
- 唯一出路是「整程序编译」,而那条路 P4 原生后端做得更好(无 Wasm 语义中介、无 wazero 抽象层)。
- **结论**:此时直接跳 P4([../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md))才是合理选择。

### 3.2 下沿:wazero 已验证档

wazero 项目本身的 benchmark(其 README 与 release notes,**待 spike 实测时核对当前版本数据**)显示「应用调用空 Wasm 函数」的边界成本在数十纳秒档(具体数字 wazero 版本相关,**待 spike 验证**)。这是 spike 期望的**下沿**——若 S1 显著高于 wazero 自报数据(如 S1 > 100ns 而 wazero 自报 30ns),说明我们的测量方法或环境有问题,需排查。

下沿存在保证了「150ns 阈值不是工程目标的天花板,而是工程目标的安全余量」——若 wazero 自身已能跑到几十 ns 档,我们 150ns 阈值留有 2-3 倍的余量给参数处理 / memory 操作等增量成本。

### 3.3 校准测量的支撑:154 vs 164μs 的 6%

[../roadmap.md §1](../roadmap.md) 校准测量 1 的关键事实:**真 LuaJIT 只比 luajc 快 6%**(154 vs 164 μs,1000 items per-item 形态)。两者后端实现差距巨大(LuaJIT 是 trace JIT + 寄存器分配 + 类型投机;luajc 是「Lua→JVM bytecode + JVM C2 优化」),但 per-item 形态下两者数据仅差 6%——边界跨越 + 值装箱已是绝对主导成本。

这一事实有两层含义:

1. **边界成本主导,后端再快也有限**——这正是 [../roadmap.md §1](../roadmap.md) 「per-item 跨界形态下边界跨越主导成本」结论的实证。
2. **如果我们的边界成本与 LuaJIT/luajc 同档(几十~百 ns,因为 Java JNI / LuaJIT C-Lua 边界类似),P3 的天花板就锁定在 luajc 档**——这是 [../p4-method-jit/08-testing-strategy](../p4-method-jit/08-testing-strategy.md) §1 验收门槛「列内核负载 ≥ LuaJ-luajc 档」的来源。
3. **如果我们的边界成本显著高于这一档(>150ns),P3 连 luajc 档都摸不到**,那么[../roadmap.md §4](../roadmap.md) 的「P3 验收 ≥2x over P1」就成了空中楼阁——P1 已是 2-4x over gopher-lua,再 2x 等于 4-8x over gopher-lua,已经接近 luajc 档(luajc ≈ 4.4x over gopher-lua),边界成本必须在档才可能。

### 3.4 工程化阈值:为什么是 150 而不是 100 或 200

闸门阈值是工程决策,需要在「过严浪费机会」与「过松带来后续返工」之间平衡:

| 候选阈值 | 含义 | 风险 |
|---|---|---|
| **80ns**(过严) | 仅 wazero 自报档接近 + 参数处理零成本 | 可能误杀「能跑出 ≥2x 收益但跨层稍贵」的实际可行 P3 |
| **150ns**(选定) | 「百 ns 档上沿」工程化阈值 | 列内核形态下 `T_cross/N` 完全可忽略;非理想形态(`k=1`)仍保留 1.5-2x 收益空间 |
| **300ns**(过松) | 与 LuaJIT C-边界同档,与 hostcall 同档 | §2 模型预测 `k=1` 形态下净收益归零;P3 加速比天花板降到 1.5x 以下,值不回 6-12 人月 |

**选定 150ns 的两条具体论证**:

1. **§2 摊销模型的转折点**:`I = 20`、`Δc = 15ns`、`k = 1` 时,`T_cross = 150ns` 恰好是「净收益 = 150ns,加速比 = 2x」的临界点。> 150ns 加速比降到 < 2x,跨过验收门槛红线。
2. **校准测量的反推**:luajc 边界成本 ≈ 几十~百 ns 档(JNI 实测档位),P3 wazero 边界**至少不能比 luajc 同等任务的边界成本贵**——150ns 给了 50-80% 的余量。

阈值不是「精确到 ns 的硬数字」,边缘 150 ± 30 ns(120-180 ns)走混合策略(§5.3),不是粗暴二元判定。

### 3.5 与 wazero 已解决项的对照

[../roadmap.md §2](../roadmap.md) 表格列出 wazero 已解决的四项税方案:

| 税 | wazero 方案 | 与 spike 的关系 |
|---|---|---|
| GC 精确栈扫描 | Wasm 执行在 wazero 自管栈 | 与跨层成本无关,但 §4 顺带验证 |
| 异步抢占 | 生成代码循环回边插抢占检查点 | 在 spike 函数体足够大时可观测,§4 顺带验证 |
| 栈移动 | Wasm 栈不在 Go 栈 | 与跨层成本无关,但 §4 顺带验证 |
| 写屏障 | 值世界放 linear memory | 与 spike 无关,§3 已不变(P3 不动) |

**spike 不验证「四项税本身是否被解决」**(那是 wazero 项目的承诺,我们信任),只在 §4 做最小确认——但**spike 重点是「跨层成本是否落在 150ns 档」**,这是 wazero 没有显式承诺、需我们自己测的项。

### 3.6 阈值不达标的连锁后果(展开 §3.1 论证)

若 `T_cross > 150ns`,P3 各设计点连锁后果:

| 设计点 | 连锁影响 |
|---|---|
| **每 Proto 一个 module**([02-translation §1.1](./02-translation.md)) | 每次「gibbous Proto 调未编译 Proto」都跨 Go 中转,跨层成本叠加,加速比劣化更严重 |
| **IC 快路径内联**([06-ic-feedback-consume §1](./06-ic-feedback-consume.md)) | 失效路径每次走 `$h_gettable` 助手,每次跨层 150ns+——形状变化频繁的程序加速比归零 |
| **算术慢路径**([02-translation §2.3](./02-translation.md) ADD 示例) | 混合类型每次走 `$h_arith` 助手,150ns+ × 算术指令数;数值密集程序若有微量混合类型操作就废 |
| **回边 safepoint**([05-safepoint-gc §3](./05-safepoint-gc.md)) | gcPending 检查命中(GC pending 时)经 `$h_safepoint` 跨层,GC 频次高时跨层频次也高 |
| **CALL/TAILCALL/RETURN**([04-trampoline §3](./04-trampoline.md)) | 每次函数调用至少 1 次跨层,递归密集程序每次都付 |

每一项都是 P3 设计的核心——若跨层成本超档,**P3 的整体加速比从「2-4x over P1」降到「<1.5x」甚至「无收益」**,P3 6-12 人月投入产出失衡,直接跳 P4 是合理选择。这正是 §0.1 论证「闸门先于一切翻译工作」的延伸。

---

## 4. spike 三件事一次过(顺带验证项)

PW0 的 spike 还要顺带验证两项与 P3 后续设计强耦合的物理事实——一次搭环境一次跑完,避免 PW1/PW2/PW3 时再单独验证(每次都要重搭 wazero 环境,浪费):

### 4.1 主项:call boundary < 150ns

如 §1-§3 论证,这是闸门主指标。三档样本一次跑完,产出数据见 §6.6 决策报告模板。

### 4.2 顺带项 A:linear memory 共享(§4 → 验证 [03-memory-model](./03-memory-model.md))

[03-memory-model](./03-memory-model.md) 的核心设计是「arena backing 收养 wazero memory」——同一块物理内存、同一套 NaN-box 编码、同一套偏移寻址。这一设计的物理可行性需要 spike 顺带验证三件事:

| 验证点 | 操作 | 期望结果 |
|---|---|---|
| **内存可见性** | Go 侧通过 `mem.WriteUint64Le(0, V)` 写一个值,Wasm 侧 `(i64.load offset=0)` 读 | 读到同一个 V(逐位一致) |
| **跨边界一致性** | Wasm 侧 `(i64.store offset=8 V')` 写,Go 侧返回后 `mem.ReadUint64Le(8)` 读 | 读到 V'(无副本、无序列化) |
| **`memory.grow` 后视图重取** | Wasm 侧 `(memory.grow 1)` 增 1 页,Go 侧的 `[]byte` view 是否需要重新获取 | 待 spike 验证(`api.Memory.Read/Write` 接口是否自动跟随 grow) |

**为什么这是 spike 顺带项而不是 PW1 单独项**:[03-memory-model §1.2](./03-memory-model.md) 明确指出 wazero 具体 API(import memory vs 宿主读 module memory、buffer 稳定性、`memory.grow` 后 Go 侧视图重取的精确时序)是「待 spike 验证」的——这一项的不确定性与 call boundary 同源(都是 wazero 嵌入用法的工程细节),一次 spike 一并解决。

### 4.3 顺带项 B:四项税兑现(§9 → 验证 [00-overview §8](./00-overview.md))

四项税的解决方案在 [../roadmap.md §2](../roadmap.md) 表中已宣称「wazero 已验证」,P3 选 wazero 的本质([00-overview §8](./00-overview.md))就是把这四项税外包。但「外包是否兑现」在我们具体 spike 形态下需做最小确认:

| 税 | 最小验证操作 | 期望结果 | 失败信号 |
|---|---|---|---|
| **GC 精确栈扫描** | spike 跑长时间(`-benchtime=60s`)后跑 `runtime.GC()`;之前在 Go 侧分配大量临时对象使其需要被 GC | GC 正常完成,无 panic / 数据损坏 | panic 或 Wasm 函数返回错误说明 wazero 自管栈出了问题 |
| **异步抢占** | spike 中混入 long-running Wasm 函数(循环 1ms),期间 `runtime.Gosched()` 或并发触发 GC | Go 调度器能在 1ms 内中断 Wasm 函数让出 CPU | 若调度器无法抢占,wazero 回边检查点未生效 |
| **栈移动** | 同 § 4.3.1 GC 验证 | Go 侧 goroutine 栈被 morestack 拷贝,wazero 生成码不持有指向 Go 栈的指针——实测「跑大量并发 goroutine 调 wazero」无内存损坏 | 数据损坏说明栈移动破坏了生成码 |
| **写屏障** | spike 中 wazero 生成码写 linear memory(S2 已涵盖),Go GC 跑期间无 panic | 无并发 GC 三色不变式破坏 | 极少触发,但若发生则 wazero 内部实现有 bug |

**纪律**:这四项的「最小验证」目的不是穷尽测试 wazero(那是 wazero 项目的责任),而是确认我们具体使用形态下未踩到 wazero 已知的某个边角 bug。任何一项失败需查 wazero issue tracker / 升版本,不是 P3 自己的设计问题。

### 4.3.1 GC 精确栈扫描的最小验证细节

具体测试形态:

```go
func TestSpike_GCDuringWasm(t *testing.T) {
    // 准备:wazero 实例化 S2 形态的模块(读写一对 i64)
    rt, mod, fn := setupS2Module(t)
    defer rt.Close(context.Background())
    
    // 运行循环:多次调 Wasm + 中间触发 Go GC
    for i := 0; i < 10000; i++ {
        // 跑 Wasm
        _, err := fn.Call(context.Background(), 0)
        if err != nil { t.Fatal(err) }
        
        // 每 100 次触发一次 GC,期间跑大量 Go 临时对象分配
        if i % 100 == 0 {
            _ = make([]byte, 1<<20)  // 触发 Go heap pressure
            runtime.GC()
            runtime.GC()  // 双倍保险:让 GC 走完整循环
        }
    }
    
    // 验证 mem 中的值仍正确(没被 GC 误判 / 误回收)
    val := mod.Memory().ReadUint64Le(0)
    if val != expectedVal { t.Fatal(...) }
}
```

期望结果:循环跑完无 panic、无数据损坏。失败信号:Go GC 触发期间 panic(典型为「invalid pointer」或「scanning unallocated memory」),说明 wazero 自管栈在 GC 视角下未隔离干净。

### 4.3.2 异步抢占的最小验证细节

抢占信号路径:Go 运行时通过 SIGURG 信号向 goroutine 发抢占请求,goroutine 在循环回边的检查点处响应。wazero 生成码已在每个循环回边插了抢占检查点([../roadmap.md §2](../roadmap.md) 「已验证」)。

验证形态:

```go
func TestSpike_AsyncPreempt(t *testing.T) {
    // 准备:wazero 实例化一个长循环 Wasm 函数(loop 1ms)
    rt, mod, longFn := setupLongLoopModule(t)
    defer rt.Close(context.Background())
    
    // 启动主 goroutine 跑 longFn,启动旁路 goroutine 触发 GC
    var wg sync.WaitGroup
    wg.Add(2)
    
    start := time.Now()
    go func() {
        defer wg.Done()
        _, err := longFn.Call(context.Background())
        if err != nil { t.Error(err) }
    }()
    
    go func() {
        defer wg.Done()
        time.Sleep(100 * time.Microsecond)  // 让 longFn 先跑起来
        runtime.GC()  // 触发 stop-the-world,要求所有 goroutine 抢占
    }()
    
    wg.Wait()
    elapsed := time.Since(start)
    
    // longFn 1ms,GC 大概 100us 进入,GC 完成应在 1.5ms 内,而非阻塞 1.1ms
    if elapsed > 2*time.Millisecond { t.Fatal("preempt likely failed") }
}
```

期望结果:GC 不被 longFn 长时间阻塞,完成时间在合理范围。失败信号:GC 完成时间显著拉长,说明 wazero 抢占检查点未生效。

### 4.3.3 栈移动的最小验证细节

Go runtime 在 goroutine 栈接近用尽时调用 `morestack` 拷贝栈到更大空间。若 wazero 生成码持有指向 Go 栈的指针,栈移动后指针失效,数据损坏。

验证形态(隐式覆盖,无需独立测试):
- §4.3.1 GC 测试期间,大量 goroutine 分配会触发 morestack
- §4.3.2 异步抢占测试期间,GC 同样触发 morestack
- 若 wazero 持有 Go 栈指针,这两项测试会偶发数据损坏

**结论**:如果 §4.3.1 + §4.3.2 都通过 1000 轮循环无错,栈移动安全可视为顺带验证。

### 4.3.4 写屏障的最小验证细节

Go 并发 GC 三色不变式要求堆指针写经写屏障。若 wazero 生成码绕过 Go 写屏障写堆指针,GC 可能漏标 / 误回收。

但 P3 设计已避免这一情况:[03-memory-model](./03-memory-model.md) 的核心约束是**值世界全部住 linear memory(arena),wazero 生成码不写 Go 堆指针**。S2 spike 形态(`(i64.store offset=8 ...)`)正是写 linear memory,**不**触发 Go 写屏障——这是 [../roadmap.md §2](../roadmap.md) 表中「值世界放自管 arena/linear memory,边界拷贝」的物理体现。

**结论**:写屏障在 P3 整体设计层面已规避,无需独立测试;但 §4.3.1 GC 测试期间若数据损坏,可能间接揭示写屏障被绕过。

---

### 4.4 spike 测试 harness 设计

将上述三件事(主项 + 两项顺带)整合成一个 Go benchmark + 单测套件:

```go
// internal/gibbous/wasm/spike/spike_test.go(临时 spike 目录)
package spike

import (
    "context"
    "testing"
    "github.com/tetratelabs/wazero"
    // ...
)

// ---- 主项:call boundary ----
func BenchmarkSpike_S1_EmptyRoundtrip(b *testing.B) { /* §1.3 S1 */ }
func BenchmarkSpike_S2_RWRoundtrip(b *testing.B)   { /* §1.3 S2 */ }
func BenchmarkSpike_S3_HostFnCallout(b *testing.B) { /* §1.3 S3 */ }

// ---- 顺带项 A:memory 共享 ----
func TestSpike_MemorySharing_BasicRW(t *testing.T)        { /* §4.2 内存可见性 */ }
func TestSpike_MemorySharing_BoundaryConsist(t *testing.T) { /* §4.2 跨边界一致性 */ }
func TestSpike_MemorySharing_AfterGrow(t *testing.T)       { /* §4.2 grow 后视图 */ }

// ---- 顺带项 B:四项税 ----
func TestSpike_GCDuringWasm(t *testing.T)        { /* §4.3 GC 栈扫描 */ }
func TestSpike_AsyncPreempt(t *testing.T)        { /* §4.3 异步抢占 */ }
func TestSpike_StackMoveSafety(t *testing.T)     { /* §4.3 栈移动 */ }
// 写屏障由其它 GC 测试间接覆盖,不单独测
```

**输出**:benchmark 数据 + 测试套全过 = spike 完成,可进 §6 决策报告。

---

## 5. 三种出路决策树

### 5.1 决策表

| spike 结果 | 决策 | 理由 | 后续动作 |
|---|---|---|---|
| **S2 < 150ns 且 S3 同档**(同档 = ±30ns 内) | **开工 P3**(本子文档闸门通过) | 摊销模型预测列内核形态下完整兑现 ≥2x 收益 | 本文 §6.6 写决策报告;启动 PW1([00-overview §4](./00-overview.md))包骨架开发 |
| **S2 ≥ 150ns**(且不在边缘区) | **跳过 P3 直做 P4** | §3.1 论证:跨层成本与宿主边界同档,P3 加速比天花板被锁死 | 转 [../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md) 跳跃路径;本文 §2-§7 设计的分层协议被 P4 继承,只换发射 |
| **S2 在 150 ± 30ns 边缘** | **混合策略** | §2.4 论证:理想列内核形态仍可兑现,非理想形态收益打折 | 启动 PW1 但加 §5.3 的「调用密度」约束,扩 P2 可编译性分析 |
| **S3 比 S2 显著贵**(> 2x) | **PW1 设计调整** | gibbous→host 慢路径成本被放大,影响算术 metamethod / IC miss / 分配等助手 | 不阻断开工,但 §6.4 失败分析需对 wazero imported 函数 dispatch 的实现做调查;PW3 算术助手设计需考虑「合并多个慢路径成一次跨层」 |

### 5.2 决策树流程图

```
              spike 测出三档数据 (S1, S2, S3)
                       │
                       ▼
                  S2 < 120ns ?
                  /         \
                 是          否
                /             \
               ▼               S2 < 180ns ?
        S3 < S2 + 30 ?         /         \
        /          \          是          否
       是           否        /            \
       │            │        ▼             ▼
       │            │   边缘区           S2 ≥ 180ns
       │            │   (混合策略)       (跳 P4)
       │            │       │              │
       │            │       ▼              ▼
       │            │   PW1 加          走跳跃路径
       │            │   「调用密度」      [../p4-method-jit §0.2]
       │            │   启发           
       │            │   §5.3            
       │            │       
       │            ▼
       │      PW1 设计调整
       │      (S3 慢,
       │       助手合并)
       │            │
       ▼            ▼
   完整开工     条件开工
   PW1 起 [02]  PW1 起 [02]
```

### 5.3 边缘混合策略详细设计

若 S2 实测落在 150 ± 30ns(120-180ns),P3 仍可启动,但需把 §2 摊销模型的 `k ≈ 0` 形状识别加进 P2 决策机:

**新增启发:调用密度**(对 [../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) 的回填请求)

```
ProtoLevel 静态分析期间(03 §2-§3)新增一项:
  callDensity := count(CALL/TAILCALL) / count(non-CALL instructions)

阈值定稿待 spike 数据后:
  callDensity > θ_high(粗估 0.1,即每 10 条非 CALL 指令出现 1 个 CALL):
    → 标记「跨层密集」,Compilability 降级 CompNotCompilable 或新增态 CompNotProfitable
  callDensity < θ_low(粗估 0.02):
    → 标记「自包含热闭包」,允许升层(主路径)
  中间区:
    → 视 P2 阈值与运行期 feedback 综合判定
```

**为什么 callDensity 是合理代理**:CALL 指令是 gibbous→crescent / gibbous→host 跨层的最大来源(算术 metamethod / GETTABLE miss 也走跨层但频次相对可控,且经 IC 快照固化后大多数走快路径)。`k ≈ 0` 形态对应「函数体内主要是直线计算 + 循环,CALL 罕见」,即 `callDensity ≪ 1`。

**边缘策略的本文承诺与回填请求边界**:
- 本文 §8 缺口节登记「对 [../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) 的回填请求:加 `callDensity` 启发」。
- **本文不主动改 [../p2-bridge/03](../p2-bridge/03-compilability-analysis.md)**(回填纪律,见 [00-overview §3](./00-overview.md))——主助理收口在 [implementation-progress](./implementation-progress.md)。
- `θ_high / θ_low` 精确阈值需 spike 实际数据后定,本文先记粗估值;PW1 启动前主助理与用户对齐后定稿。

### 5.4 跳跃路径下「设计资产不丢失」的细则

若决策走「跳过 P3 直做 P4」,本文 §2-§7 的设计内容**不报废**——P4 跳跃路径下,以下设计点继承使用:

| 本文设计点 | P3 常规路径下的角色 | P4 跳跃路径下的角色 |
|---|---|---|
| 跨层摊销模型(§2) | 论证 P3 收益边界 | 同样论证 P4 收益边界——P4 边界成本可能更低(原生 ABI),但模型不变 |
| 跨层成本档位(§3) | 150ns 是 P3 闸门 | P4 跳跃路径下成本档位由 P4 自己 spike 测——但「与宿主同档则失败」的论证仍适用 |
| 三档样本(§1.2) | spike 形状 | P4 自建分层骨架时,trampoline 入口同款形状(参数解包 / 返回 status / memory 共享) |
| memory 共享设计([03-memory-model](./03-memory-model.md)) | wazero memory 收养 | P4 跳跃路径下 arena backing 直接是 Go 堆 + mmap 拼成的可执行内存,但「值世界与执行码共享一块内存」的物理形态延续 |
| 跨层协议([04-trampoline](./04-trampoline.md)) | crescent ↔ gibbous(wazero)↔ host | crescent ↔ gibbous(原生)↔ host,只换发射 |
| 错误冒泡 / yield 不穿越([07](./07-coroutine-thread-rule.md)) | 线程级 tier 规则 | 同一规则,P4 自建时直接采纳 |

**结论**:即使闸门否决 P3,本子文档对 P4 仍是有效设计输入——这是 [../roadmap.md §5](../roadmap.md) 原则 3 的另一面。

---

## 6. spike 实施清单(PW0 施工指南)

### 6.1 准备阶段(spike 启动前)

| 步骤 | 操作 | 完成定义 |
|---|---|---|
| **0** | 确认 P1/P2 落地状态(见 [00-overview §7](./00-overview.md)) | 全 PB 过线;`make test` 与 `make bench` 全绿 |
| **1** | 创建临时 spike 目录 `internal/gibbous/wasm/spike/` | 目录与 `go.mod` 模块路径就位;后续清理时一并删除 |
| **2** | `go get github.com/tetratelabs/wazero@<latest-stable>` | wazero version 锁定在 `go.mod`;记入 spike 报告 |
| **3** | 准备物理环境:锁 CPU 频率 / 关后台进程 / 绑核 | `cpupower frequency-info` 显示锁定;`top` 无显著 CPU 占用 |
| **4** | 写 spike harness 代码(§4.4) | `go vet` 与 `go test -run XXX` 编译通过(不跑) |

### 6.2 测量阶段

| 步骤 | 操作 | 数据采集 |
|---|---|---|
| **5** | 运行 `go test -run '^TestSpike_' -count=1` 验证顺带项可跑通 | 全过 = §4.2-§4.3 验证清单全过 |
| **6** | 运行主 benchmark:`go test -bench=BenchmarkSpike -benchtime=10s -count=5` | 三档(S1/S2/S3)各 5 次数据 |
| **7** | 取每档中位数,计算离散度(标准差 / 中位数) | 离散度 < 5% 视为可信;> 5% 排查环境干扰后重测 |
| **8** | 对照下沿:与 wazero 自报 benchmark 比对(版本相关) | S1 与 wazero 自报数据同档(±50%)= 测量方法正确;否则查环境 |

### 6.3 决策阶段

| 步骤 | 操作 | 产出 |
|---|---|---|
| **9** | 把三档数据代入 §5 决策表 | 三选一结果:开工 / 跳跃 / 边缘混合 |
| **10** | 写决策报告(§6.6 模板) | `docs/design/p3-wasm-tier/spike-report.md`(临时,后续合并入 [implementation-progress](./implementation-progress.md)) |
| **11** | 与用户对齐决策 | 用户裁决「按报告决策开工 / 改方向」 |
| **12** | 数据进档:把决策报告内容合并入 [implementation-progress](./implementation-progress.md) | 永久存档,后续 P4 验收时可回查 |
| **13** | 清理临时 spike 目录(若决策开工 P3,部分代码可留为 PW1 起点) | `git mv` 或 `git rm` 完成 |

### 6.4 失败分析:不达标时的责任归位

若 S2 ≥ 150ns,**问题不一定全是 wazero 的**——需逐项排查:

| 责任项 | 排查方法 | 后续动作 |
|---|---|---|
| **wazero call boundary 实现** | 对照 wazero 自报数据;查 wazero issue tracker 看是否近期版本退化 | 若 wazero 当前版本退化,降版本试;若是结构性问题,记 wazero issue 后转 P4 |
| **wazero 编译模式开关** | 确认 `wazero.NewRuntimeConfigCompiler()` 而非 `RuntimeConfigInterpreter()` | 修配置后重测 |
| **Memory grow 锁** | S2 测量前是否触发了 memory.grow;wazero 内部对 grow 是否有锁开销 | 排除 grow 路径(预分配足量内存);重测 |
| **imported 函数 dispatch**(S3 显著贵的情况) | 对比 wazero 不同版本对 host fn 的实现差异;查是否使用了 reflect 路径 | 切换到 wazero 推荐的「pre-compiled host fn」API;重测 |
| **测量噪声 / 环境干扰** | 离散度 > 5% 时的常见原因 | 重测,确保锁频 + 绑核 + 静默环境 |
| **测量方法本身** | 反向校验:跑一个「Go 直接调 Go 函数」的 baseline benchmark,确认 b.N / time 计算正确 | 若 baseline 数据异常,benchmark 框架有问题,排查 |

**关键纪律**:即便 spike 不达标,**也要先排查上述各项再做闸门决策**。「数据不好就直接跳 P4」是错误判断——可能只是配置问题,排查后能进档。但若排查后仍 ≥ 150ns,**真正不达标**,按 §5 决策表走跳跃路径。

### 6.5 跨版本退化的防御

wazero 是活跃维护的开源项目,版本更新可能改变 call boundary 实现。spike 通过的 wazero version `vX.Y.Z` 必须在 `go.mod` 锁定,且 [implementation-progress](./implementation-progress.md) 永久记录这一版本号。

未来场景:
- **PW1-PW9 期间 wazero 升版本**:必须在 `go.mod` 显式 update + 重跑 spike 主项(§6.2 步骤 6),数据进档后才允许合入升版本 PR。
- **wazero 跨版本退化**(spike 数据从 100ns 退化到 200ns):不接受升版本;若旧版本有安全补丁需打,优先 backport。
- **wazero 突破性优化**(spike 数据从 100ns 优化到 50ns):接受升版本,数据进档,PW1 维持现状(P3 设计不依赖具体边界成本数字,只依赖 < 150ns)。

### 6.6 决策报告模板

```markdown
# P3 PW0 spike 决策报告(YYYY-MM-DD)

## 测量环境
- 机器:<CPU 型号 / 内存 / 操作系统 / 内核版本>
- wazero version:vX.Y.Z(go.mod 锁定)
- Go version:goX.Y
- 测量方法:`go test -bench=BenchmarkSpike -benchtime=10s -count=5`,锁频 + 绑核
- 离散度:S1 / S2 / S3 各档标准差 / 中位数 = NN% / NN% / NN%

## 三档数据(中位数)
| 档 | 实测值 (ns/op) | 期望值 (ns/op) | 是否过线 |
|---|---|---|---|
| S1 空往返 | NN | < 80 | ✅/❌ |
| S2 带参往返 | NN | < 150(主) | ✅/❌ |
| S3 反向往返 | NN | < 150 | ✅/❌ |

## 顺带项验证
- §4.2 内存可见性 / 跨边界一致性 / grow 后视图:✅/❌(各项详述)
- §4.3 GC 栈扫描 / 异步抢占 / 栈移动 / 写屏障:✅/❌(各项详述)

## 决策
- 闸门结果:**开工 P3 / 跳过 P3 直做 P4 / 边缘混合策略**
- 理由:对照 §5 决策表,数据落在 XX 区间
- 后续动作:启动 PW1 / 转 P4 跳跃路径 / 启动 PW1 + 加 callDensity 启发

## 失败分析(若不达标)
- 排查项 §6.4 各表项:逐项排查结果
- 真因归位:wazero 实现 / 配置 / 环境 / 测量方法 / 不可解
- 决策依据:即便排查后仍不达标,数据落在 XX 区间

## 附:原始 benchmark 输出
<go test -bench 完整输出粘贴>
```

### 6.7 spike 完成的验收清单

PW0 完成定义([00-overview §4](./00-overview.md)):

- [ ] §4.4 测试 harness 代码全过
- [ ] 三档主指标数据采集完成,离散度 < 5%
- [ ] §4.2 / §4.3 顺带项全过
- [ ] 决策报告(§6.6 模板)完成,数据进 [implementation-progress](./implementation-progress.md)
- [ ] 与用户对齐决策(走开工 / 跳跃 / 边缘混合)
- [ ] 闸门决策不可逆地写入 [implementation-progress](./implementation-progress.md)

---

## 7. 不变式清单(本节守护)

承 [00-overview §9](./00-overview.md) 的全 P3 不变式,本子文档承担以下三条:

1. **闸门是单点决策不可绕过**(§0.2)——PW0 的产出物只有「三档数据 + 决策报告」一项,无并行任务、无可绕过路径。任何「先开工 PW1 再补 spike」都是把不确定性带进 PW1,违反 [../roadmap.md §5](../roadmap.md) 原则 3。
2. **走 P4 跳跃路径不丢失 P3 设计资产**(§5.4)——本文 §2-§7 的设计内容(摊销模型 / 跨层成本档位 / 三档样本 / memory 共享 / 跨层协议 / yield 不穿越)被 P4 跳跃路径继承。设计文档不报废,只换发射后端。
3. **spike 数据进 [implementation-progress](./implementation-progress.md) 永久存档**(§6.3 步骤 12)——版本号 + 三档数据 + 决策报告全部进档;P4 验收时若反向决议「P3 退役」,本档案是「P3 是否本就该跳过」的回顾依据;wazero 升版本时必须重跑 spike(§6.5),数据进档后方可合入。

---

## 8. 文档缺口 / 待决(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md))

### 8.1 「待 spike 验证」标注

本文以下处标注「待 spike 验证」,PW0 跑完后回填精确值/操作:

- **wazero 强制编译模式 API**(§1.3 / §1.4):`wazero.NewRuntimeConfigCompiler()` 是合理推断,精确开关 API 待 spike 验证。
- **wazero 模块实例化 API**(§1.3):`rt.Instantiate(ctx, wasmBytes)` 与 `rt.NewHostModuleBuilder("env")` 接口形态待 spike 验证。
- **`api.Memory.WriteUint64Le / ReadUint64Le` 接口**(§1.3 / §4.2):wazero 当前版本是否提供这一对儿原生接口待 spike 验证。
- **`memory.grow` 后 Go 侧视图重取协议**(§4.2):wazero 的 `api.Memory` 是否在 grow 后自动跟随,还是需要重新 `mod.ExportedMemory("mem")` 取——待 spike 验证([03-memory-model §3](./03-memory-model.md) 同源缺口)。
- **wazero 当前版本 self-benchmark 数据**(§3.2):用于 spike 数据下沿对照——待 spike 时核对当前版本数据。
- **wazero pre-compiled host fn API**(§6.4):S3 显著贵时切换的目标 API 形态待 spike 验证。

### 8.2 边缘混合策略阈值精确化

- **`callDensity` 启发的精确阈值 `θ_high / θ_low`**(§5.3):本文先记粗估 `0.1 / 0.02`,精确数字待 spike 数据 + PW1 早期实测后定;PW1 启动前主助理与用户对齐定稿。

### 8.3 spike 不达标后 P4 跳跃路径的具体启动时机

- **跳跃路径的衔接窗口**(§5.4):若闸门否决 P3,P4 立即接管还是等 P2 后续优化轮再启动——本文未细化此节奏,留 [../p4-method-jit/01-launch-judgment §2](../p4-method-jit/01-launch-judgment.md) 与主助理协调。
- **跳跃路径下设计资产复用清单**(§5.4 表):粗粒度列出,但精确「哪些设计内容由 P4 直接采纳、哪些需要重写」待 P4 详细设计落地时定。

### 8.4 回填请求

本文不主动改 P1/P2 现稿,以下回填请求由主助理收口在 [implementation-progress](./implementation-progress.md):

- **对 [../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) 的回填请求**(§5.3):加 `callDensity` 启发(边缘混合策略的核心);本回填**仅在 spike 数据落在边缘区(§5 决策表)时触发**——若闸门主路径(< 150ns)通过,本回填无需执行。
- **对 [00-overview §10 风险](./00-overview.md) 的回填请求**:本文 §6.5 的「跨版本退化防御」可作为新增风险条目;但属于运维纪律,本子文档已自含。

---

## 9. spike 与 P3 后续 PW 的衔接

PW0 通过后,本子文档与后续 PW 的具体衔接点:

| PW | 本文为其提供 |
|---|---|
| **PW1**([02-translation §1](./02-translation.md) + [03-memory-model §1](./03-memory-model.md)) | spike 顺带项 A(memory 共享)的具体 wazero API 形态;spike 主项数据用于 PW1 包骨架的「保留 < 150ns 边界成本」纪律 |
| **PW2**([02-translation §2-§3](./02-translation.md) + [04-trampoline §2](./04-trampoline.md)) | spike S2 形状直接成为 trampoline 入口签名(`(func $proto_N (param $base i32) (result i32))`)的复刻;spike harness 的 Go benchmark 框架被 PW2 的 byte-equal 测试框架接管 |
| **PW3**([04-trampoline §3](./04-trampoline.md)) | spike S3 形状成为 imported 助手分派的复刻;若 S3 显著贵,PW3 的算术/慢路径助手设计需考虑「合并多个慢路径成一次跨层」 |
| **PW9**([08-testing-strategy](./08-testing-strategy.md)) | spike 数据是 PW9 性能验收的下沿基线——若 PW9 实测加速比未达 ≥2x,可回查 spike 数据看是否当时跨层成本就高于预期 |

---

相关:
[00-overview](./00-overview.md)(P3 总览,§4 PW0 + §8 四项税外包) ·
[02-translation](./02-translation.md)(翻译器,PW1 起的下游) ·
[03-memory-model](./03-memory-model.md)(共见内存,顺带项 A 验证目标) ·
[04-trampoline](./04-trampoline.md)(跨层互调,S2/S3 形状的设计源) ·
[05-safepoint-gc](./05-safepoint-gc.md)(跨层 GC,顺带项 B 验证目标) ·
[implementation-progress](./implementation-progress.md)(进度对账,spike 数据归档点) ·
[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P2 决策机,本文闸门通过后的消费链路起点) ·
[../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md)(可编译性分析,边缘混合策略 callDensity 启发的回填目标) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(§0.2 跳跃路径,本文闸门否决时接管点) ·
[../roadmap.md](../roadmap.md)(§1 校准测量、§2 四项税、§4 P3 阶段定义) ·
[../architecture.md](../architecture.md)(§3 包布局) ·
[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier-1 映射 + P3 验收 ≥2x over P1 坐标系) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(原则 3 每阶段独立交付不亏) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 开工前置确认 / wazero API 待验证项)




