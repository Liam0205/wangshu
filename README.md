# Wangshu(望舒)

纯 Go 实现的高性能嵌入式 Lua 5.1 虚拟机(不依赖 cgo,保持交叉编译能力)。

**望舒**:中国神话中为月亮驾车的神(《楚辞·离骚》"前望舒使先驱")。Lua 在葡萄牙语中意为"月亮"——为月亮驾车,即驱动 Lua 的引擎。

## 目标

- 近期:把 Go 生态的 Lua 执行性能从 gopher-lua 档提升到 LuaJ-luajc 档以上
- 终局:在"列内核"负载形状下逼近 LuaJIT 档(纯 Go 约束下 10-30x over gopher-lua)
- 语言面:Lua 5.1 核心(与 LuaJIT 一致,不追语言完备性)

## 架构

分层 VM,执行层以月相命名:

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(crescent)    (基建)        (gibbous)          (gibbous)         (fullmoon)
```

核心承诺:NaN-boxed u64 值表示 + 自管 arena 线性内存,各执行层共见同一块内存,上编译层是纯增量;解释器永不退役(所有编译层的 deopt 着陆点与语义 oracle);层间逐字节差分测试是 CI 必过门禁。

## 当前状态

**P1(crescent 解释器)完整交付 + P2(bridge 分层桥)PB0-PB7 + P2 后续优化轮 #1-#4 全过线**:P1 全里程碑 M0-M14 + 收尾轮(协程/pattern matcher/IC/arena ABI 等)+ 长稳轮(freelist 内存复用/调用深度上限/并发验证)+ 审查核销轮(22+ 项逐函数对照官方源码的发现全量修复)落地;**P2 全卷 PB0-PB7 + 后续优化轮 #1-#4**(2026-06-13 单会话冲刺:bridge 包骨架 + 回边/入口采样钩 + 算术 IC 双计数 + IC 反馈聚合 + F1-F7 可编译性闸门 + TierState 状态机 + 升层日志 + mock P3 + e2e 验收 + stdlib 白名单 + 阈值校准占位 + sync.Pool (C) 双表混合 + megamorphic 主动识别)落地。

P1 总验收通过:

- 性能四档实测见下「性能基准」节——纯 VM 微基准 5-6x over gopher-lua,真实负载纯 VM 五项中四项反超,边界密集嵌入经零分配 `CallInto` 反超;
- 与官方 Lua 5.1.5 差分对拍逐字节一致:**官方测试套 13 文件**(vararg/sort/pm 整文件,其余截至豁免线)+ 100 项手册逐节特性探测 + 12 项边角探测 + 29 条错误消息(含行号断言)+ 70 种子用例 + 500 随机脚本(nightly 每晚 200 万滚动)+ benchmark-game 五脚本返回值;
- 特性面三列全落地:必做列 probe 全绿、简化列存在性验证、缺口列 15 项显式豁免(`TestExemptions_Documented` 可审计);
- 长稳承诺:freelist 循环复用(22000 轮分配密集脚本 arena 稳定 17.4KB)、深递归报 `stack overflow` 可恢复(LUAI_MAXCALLS=20000 等价)、`-race` 下 Program 跨 goroutine 共享验证;
- conformance 套全绿;GC 双模式透明性对照全绿;三平台交叉编译冒烟(386/windows/darwin-arm64);`make all`(gofmt + lint + race)全绿。

### 快速开始

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

列内核形状(批量数据一次跨界,循环全在 VM 内):

```go
ar := wangshu.NewArena(nrows)
ar.AddFloatColumn("price", prices, nil)
ar.AddInt64Column("qty", qtys, nil)
prog, _ := wangshu.Compile([]byte(`
    local price, qty = arena.price, arena.qty
    local total = 0
    for i = 1, arena.rows do total = total + price[i] * qty[i] end
    return total
`), "kernel")
results, err := prog.Call(st, ar) // 一次调用一次跨界
```

`Program` 不可变、可跨 State 复用;`State` 每 goroutine 一个。

### P1 能力面

NaN-boxed 值表示、arena + mark-sweep GC(弱表/finalizer/size-class freelist 内存复用)、完整前端(寄存器分配与 luac 同构)、大 switch 解释器(reentry,Lua 调用不增 Go 栈;调用深度上限可恢复)、inline cache(表/全局访问直达槽)、元表全套、协程(create/resume/yield/wrap)、pcall/xpcall/error(位置前缀+traceback+luaO_chunkid 同构)、Lua 5.1 pattern matcher(含 %f frontier/%z)、stdlib(base/string/table/math/os/io/coroutine + 5.0 兼容别名)、回边指令预算(不可信脚本配额)、arena ABI 列数据零拷贝读、公共嵌入 API、四套测试机制(conformance/官方测试套/difftest 随机生成 + 官方对拍/benchmark)。

P3 迁移留口(对账记录见 [implementation-progress](docs/design/p1-interpreter/implementation-progress.md)):值栈/CallInfo 的 arena 物理搬迁(backing 注入点已就位)、upvalue 降序链。

### 性能基准

> 同机同日实测(Xeon 6982P-C,go1.26.2,`GOMAXPROCS=1` 串行,`-count` 多次取稳定值)。绝对 ns 依机器而异,关注的是同机比值与分配数。`make bench` 复现。对比对象 gopher-lua v1.1.2。
>
> wangshu 的定位是**与真实世界交互的嵌入式 VM**,不是只跑自含脚本的微基准产品——因此基准分四档,从「纯 VM 自含」一路覆盖到「边界密集的真实嵌入负载」,诚实呈现每一档的表现。

**① 纯 VM micro-bench**(`benchmarks/baseline`)——自含脚本,无数据跨界,衡量 VM 内核:

| 脚本 | wangshu | gopher-lua | 倍率 |
|------|---------|-----------|------|
| simple(分支/比较) | 127 ns · 72 B | 745 ns · 2760 B | **5.9×** |
| arith(Horner 多项式) | 171 ns · 72 B | 877 ns · 2831 B | **5.1×** |
| loop(求和循环) | 15.5 µs · 72 B | 56.0 µs · 32.6 KB | **3.6×** |

**② 真实负载纯 VM small-bench**(`benchmarks/realworld`)——benchmark-game 经典脚本,调用/分配/浮点/表操作混合,官方 lua5.1 对拍正确性:

| 脚本 | wangshu | gopher-lua | 倍率 |
|------|---------|-----------|------|
| fib | 7.57 ms | 9.55 ms | **1.26×** |
| binary-trees | 29.7 ms | 51.7 ms | **1.74×** |
| spectral-norm | 15.5 ms | 33.2 ms | **2.13×** |
| n-body | 42.5 ms | 62.4 ms | **1.47×** |
| fannkuch | 5.15 ms | 4.20 ms | 0.81×(表索引密集,剩余短板) |

**③ 边界 mini-bench**(`benchmarks/embedded`,issue #8)——嵌入者真实路径:每次跨界 set 输入、call、读返回值。这是 VM 内核优势被边界成本主导的一档:

| 档位 | wangshu `Call` | wangshu `CallInto` | gopher-lua |
|------|----------------|--------------------|-----------|
| PureVM(无跨界) | 128 ns · 72 B · 2 allocs | — | 750 ns · 2760 B · 8 allocs |
| CallOnly(纯 call+读) | 183 ns · 72 B · 2 allocs | **69 ns · 0 B · 0 allocs** | 85 ns · 0 B · 0 allocs |
| Boundary(+1×SetGlobal) | 309 ns · 72 B · 2 allocs | **164 ns · 0 B · 0 allocs** | 188 ns · 0 B · 0 allocs |

旧 `Call` 每次 round-trip 固定 72 B / 2 allocs(返回值双拷贝),边界密集时被这个地板成本主导——CallOnly/Boundary 两档反而**慢于** gopher-lua。零分配的 `CallInto`(调用方复用 `dst`)消除双拷贝,两档均**反超** gopher-lua。

**④ 真实负载 embedded bench**(`benchmarks/embedded`)——贴近 pineapple `transform_by_lua` 形态:每批 1000 item,逐 item set 字段 → call 谓词/特征变换脚本 → 读标量结果:

| 形态 | wangshu `Call` | wangshu `CallInto` | gopher-lua |
|------|----------------|--------------------|-----------|
| Predicate(布尔谓词,读 4 字段) | 535 µs · 72 KB · 2000 allocs | **389 µs · 0 B · 0 allocs** | 504 µs · 31.9 KB · 2990 allocs |
| Transform(数值特征变换) | 411 µs · 72 KB · 2000 allocs | **253 µs · 0 B · 0 allocs** | 378 µs · 47.1 KB · 3080 allocs |

旧 `Call` 路径下 Predicate 慢于 gopher-lua(535 vs 504 µs);改用 `CallInto` 后两形态均反超(**1.30× / 1.50×**)且全程零分配。boundary-dominated 嵌入(短脚本、高调用频率)用 `CallInto` 是兑现 VM 内核优势的关键。

> 形态选择:列内核(批量数据一次跨界、循环全在 VM 内,见上「快速开始」arena 示例)能完全摊薄边界成本,是高吞吐首选;无法列内核化的 per-item 形态请用 `CallInto` 复用 `dst` 走零分配路径。


- 战略层:[docs/design/roadmap.md](docs/design/roadmap.md)(动机/校准测量/演进路线/非目标)
- 总览:[docs/design/architecture.md](docs/design/architecture.md)(包布局/组件依赖/tier 映射)
- P1 解释器详细设计(13 篇):[docs/design/p1-interpreter/](docs/design/p1-interpreter/),从 [00-overview](docs/design/p1-interpreter/00-overview.md) 进入;实现进度:[implementation-progress](docs/design/p1-interpreter/implementation-progress.md)
- P2 分层桥详细设计(7 篇):[docs/design/p2-bridge/](docs/design/p2-bridge/),从 [00-overview](docs/design/p2-bridge/00-overview.md) 进入;实现进度:[implementation-progress](docs/design/p2-bridge/implementation-progress.md)(PB0-PB7 全 ✅,P2 后续优化轮规划中)
- P3-P5 阶段设计:[p3-wasm-tier](docs/design/p3-wasm-tier.md) · [p4-method-jit](docs/design/p4-method-jit.md) · [p5-trace-jit](docs/design/p5-trace-jit.md)
- 工程化机制(hooks/CI/Makefile/发布):[engineering](docs/design/engineering.md)
