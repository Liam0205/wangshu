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

**P1(crescent 解释器)+ P2(bridge 分层桥)+ P3(gibbous/Wasm 编译层)PW0-PW10 全卷 + VS0-e 全量收口**:P1 全里程碑 M0-M14 + 收尾轮 / 长稳轮 / 审查核销轮(22+ 项逐函数对照官方源码的发现全量修复)落地;**P2 全卷 PB0-PB7 + 后续优化轮 #1-#4**(2026-06-13 单会话冲刺:bridge 包骨架 + 回边/入口采样 + IC 反馈聚合 + F1-F7 可编译性闸门 + TierState 状态机 + sync.Pool (C) 双表混合 + megamorphic 主动识别)落地;**P3 PW0-PW10**(wazero call boundary spike 闸门 36.7ns<150ns → 全 38 opcode 除 VARARG 外翻译成 Wasm(算术/比较/控制流 relooper/表 IC inline 跳哈希/CALL·TAILCALL 跨层互调/CLOSURE·CLOSE/数值·泛型 for)→ 线程级 tier 规则(协程不升层)→ gcPending inline 回边零跨层 → V1-V18 端到端总验收 + **PW10 消除 gibbous→gibbous 跨层调用税**(共享 funcref 表 + `call_indirect` 直调 + CallInfo arena 化 + 零跨界 RETURN 拆帧 + 顶层升层)→ **VS0-e 全量收口**(2026-06-16:varargs 进栈下区,对齐官方 Lua 5.1 真栈布局 `[func | vararg | R(0)..]`,统一栈模型 + 简化 GC 路径))落地。

性能基线(Xeon 6982P-C,2026-06-16):新月对位 gopher-lua 嵌入式真实形态 1.03–7.06× + realworld 五项中两项反超(spectral 1.20×、nbody 1.05×);P3 凸月可选层在循环密集 hot function 形态 ~2.96× over 新月(详「性能基准」节末「P3 凸月编译层」)。详细对账见 [p3 implementation-progress](docs/design/p3-wasm-tier/implementation-progress.md)。

**embedding admin API**(2026-06-16,issue #9 / #10 / #11 三件套全交付):
- `Table.Preallocate(n)` + `State.NewArrayTable(vals)`(issue #10):一次性 array 段预分配,绕过反复 SetIndex 的 O(N²) rehash 风暴(N=1000 实测 25-60× 加速,详「性能基准」)。
- `Options.InitialArenaBytes` / `MaxArenaBytes`(issue #11):接通 arena 容量定制 + fail-fast 上限。
- `State.ArenaCapKB()` / `State.GCCountKB()`(issue #11):pool 层据 cap 判 fat state 阈值。
- `State.Collect()` / `State.MaybeCollectNow()`(issue #9):显式驱动 GC cadence,避免 boundary-dominated 短脚本下 VM safepoint starvation。
- `State.SetHostTriggeredCollect(on)`(issue #9 experimental,opt-in):host alloc 跨阈直接触发 collect,需调用方保证 transient GCRef 全 pin。
- arena LARGE freelist 改 multi-bucket size-class(底层 root fix):naive `NewTable + SetIndex(1..N)` 形态从 O(N²) 回到 O(N) 摊销(N=1000 ns/elem 824 → 59,**25× 加速**)。
- arena Compact 在 Collect 末尾缩 backing slab 到 max(bump, 64 KiB):缓解 grow doubling 高水位 latched 现象,Go runtime 回收旧大 slab(默认 build 生效;P3 收养 wazero linear memory 模式 no-op)。

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

P3 迁移留口**已全部收口**(对账记录见 [implementation-progress](docs/design/p1-interpreter/implementation-progress.md)):主线程值栈 arena 化(VS0-a/b/c)+ 协程栈独立段(VS0-c 顺手交付)+ CallInfo 进每线程 arena 段(PW10 R2,4 word/帧 + word2 位打包)+ **varargs 进栈下区**(VS0-e 子步 ①~④,2026-06-16:`enterLuaFrame` 重排 `base = funcIdx + 1 + nVarargs`,vararg 落 `stack[base-nVarargs..base)`,对齐官方 Lua 5.1 真栈布局,统一栈模型 + GC 扫栈 `[0, top)` 自然覆盖)。

### 性能基准

> 同机同日实测(Xeon 6982P-C,go1.26.2,`-count=3 -benchtime=2s` 取均值,2026-06-16)。绝对 ns 依机器而异,关注的是同机比值与分配数。`make bench` 复现。对比对象 gopher-lua v1.1.2。
>
> wangshu 的定位是**与真实世界交互的嵌入式 VM**,不是只跑自含脚本的微基准产品——因此基准分四档,从「纯 VM 自含」一路覆盖到「边界密集的真实嵌入负载」,诚实呈现每一档的表现。**默认 build 跑新月解释器(P1);P3 凸月可选编译层数据见末段**。

**① 纯 VM micro-bench**(`benchmarks/baseline`)——自含脚本,无数据跨界,衡量 VM 内核:

| 脚本 | wangshu | gopher-lua | 倍率 |
|------|---------|-----------|------|
| simple(分支/比较) | 127 ns · 72 B | 879 ns · 2760 B | **6.95×** |
| arith(Horner 多项式) | 175 ns · 72 B | 968 ns · 2832 B | **5.52×** |
| loop(求和循环) | 17.3 µs · 72 B | 34.8 µs · 32.6 KB | **2.01×** |

**② 真实负载纯 VM small-bench**(`benchmarks/realworld`)——benchmark-game 经典脚本,调用/分配/浮点/表操作混合,官方 lua5.1 对拍正确性:

| 脚本 | wangshu | gopher-lua | 倍率 |
|------|---------|-----------|------|
| fib | 9.91 ms · 87 B | 9.13 ms · 7.7 KB | 0.92× |
| binary-trees | 34.82 ms · 62 KB · 34 allocs | 31.33 ms · 17.4 MB · 272k allocs | 0.90× |
| spectral-norm | 17.52 ms · 5.2 KB | 21.05 ms · 6.8 MB · 26.6k allocs | **1.20×** |
| n-body | 41.80 ms · 402 KB · 50k allocs | 44.05 ms · 11.3 MB · 92.7k allocs | **1.05×** |
| fannkuch | 5.28 ms · 348 B | 3.97 ms · 34.6 KB · 132 allocs | 0.75×(表索引密集,剩余短板) |

> wangshu 在「纯 ns/op 倍率」上对 gopher 微输 fib/binary-trees,但 binary-trees / spectral-norm / n-body 的**分配数差 3-5 个数量级**(34 vs 272k / 39 vs 26.6k / 50k vs 92.7k allocs)——arena + freelist 内存模型对长寿命 State 嵌入(规则引擎 hot reload / 数据流转换)的 GC pressure 几乎为零。

**③ 边界 mini-bench**(`benchmarks/embedded`,issue #8)——嵌入者真实路径:每次跨界 set 输入、call、读返回值。这是 VM 内核优势被边界成本主导的一档:

| 档位 | wangshu `Call` | wangshu `CallInto` | gopher-lua |
|------|----------------|--------------------|-----------|
| PureVM(无跨界) | 121 ns · 72 B · 2 allocs | — | 853 ns · 2760 B · 8 allocs |
| CallOnly(纯 call+读) | 183 ns · 72 B · 2 allocs | **75.7 ns · 0 B · 0 allocs** | 82.0 ns · 0 B · 0 allocs |
| Boundary(+1×SetGlobal) | 306 ns · 72 B · 2 allocs | **176 ns · 0 B · 0 allocs** | 180 ns · 0 B · 0 allocs |

旧 `Call` 每次 round-trip 固定 72 B / 2 allocs(返回值双拷贝),边界密集时被这个地板成本主导——CallOnly/Boundary 两档反而**慢于** gopher-lua。零分配的 `CallInto`(调用方复用 `dst`)消除双拷贝,两档均**反超** gopher-lua(CallOnly 1.08× / Boundary 1.03×)。

**④ 真实负载 embedded bench**(`benchmarks/embedded`)——贴近 pineapple `transform_by_lua` 形态:每批 1000 item,逐 item set 字段 → call 谓词/特征变换脚本 → 读标量结果:

| 形态 | wangshu `Call` | wangshu `CallInto` | gopher-lua |
|------|----------------|--------------------|-----------|
| Predicate(布尔谓词,读 4 字段) | 527 µs · 72 KB · 2000 allocs | **396 µs · 0 B · 0 allocs** | 442 µs · 31.9 KB · 2990 allocs |
| Transform(数值特征变换) | 408 µs · 72 KB · 2000 allocs | **273 µs · 0 B · 0 allocs** | 312 µs · 47.1 KB · 3080 allocs |

旧 `Call` 路径下 Predicate 慢于 gopher-lua;改用 `CallInto` 后两形态均反超(**1.12× / 1.15×**)且全程零分配。boundary-dominated 嵌入(短脚本、高调用频率)用 `CallInto` 是兑现 VM 内核优势的关键。

> 形态选择:列内核(批量数据一次跨界、循环全在 VM 内,见上「快速开始」arena 示例)能完全摊薄边界成本,是高吞吐首选;无法列内核化的 per-item 形态请用 `CallInto` 复用 `dst` 走零分配路径。

#### P3 凸月编译层(可选)

> 启用方式:`go build -tags "wangshu_p3 wangshu_profile"`(两个 tag 都要)。同 build 下解释器自动为 hot function 升层到 Wasm,**输出 byte-equal 不变**;升不动的 proto 无声降级回新月(F1-F7 闸门)。**默认 build 不打包 wazero**。

凸月在循环密集 hot function 形态收益显著(`test/difftest/p3_bench_test.go` 的 P3_Kernels,kernel 包内层 function;2026-06-16 force-all 实测):

| 核 | 新月解释器 | 凸月 wasm | 倍率 |
|---|---|---|---|
| loop(循环密集内层函数) | 5.51 ms | 1.86 ms | **2.96×** ✅ |
| table(表查找密集) | 86.9 ms | 95.4 ms | 0.91× |
| call(内层函数互调) | 21.8 ms | 44.4 ms | 0.49×* |
| mixed(混合) | 191.8 ms | 195.6 ms | 0.98× |

> *call 0.49× 是 bench kernel 结构性架构边界:`body` 含 `ReasonUnknownCall`(F2-b 静态分析不能确认被调不 yield),force-all 强制升 body 后跨界税(~150 ns/调)无法摊销。**生产环境 F1-F7 闸门自动挡这类 proto,实际跑新月,无回归**。详 [p3 implementation-progress §14.10](docs/design/p3-wasm-tier/implementation-progress.md)。

**凸月适用形态**:长寿命 State + 循环密集 hot function(规则引擎 predicate、数据流转换 kernel)→ 升层后净赚。
**凸月不适用形态**:短脚本一次性跑 / 配置文件解析 / 协程密集 → `wangshu_profile` 给解释器加 ~28% 采样税无法摊销,关凸月(默认 build)更优。

约束(无声降级回新月,行为透明):
- 协程不升层(F1 / PW8 线程级 tier 规则:gibbous 帧不可穿越 yield/resume)
- 顶层 vararg main chunk 不升层(F1)
- 含 ReasonUnknownCall 的 body 不升层(F2-b)
- 含 VARARG opcode 的 proto 不升层(F3)


- 战略层:[docs/design/roadmap.md](docs/design/roadmap.md)(动机/校准测量/演进路线/非目标)
- 总览:[docs/design/architecture.md](docs/design/architecture.md)(包布局/组件依赖/tier 映射)
- P1 解释器详细设计(13 篇):[docs/design/p1-interpreter/](docs/design/p1-interpreter/),从 [00-overview](docs/design/p1-interpreter/00-overview.md) 进入;实现进度:[implementation-progress](docs/design/p1-interpreter/implementation-progress.md)
- P2 分层桥详细设计(7 篇):[docs/design/p2-bridge/](docs/design/p2-bridge/),从 [00-overview](docs/design/p2-bridge/00-overview.md) 进入;实现进度:[implementation-progress](docs/design/p2-bridge/implementation-progress.md)(PB0-PB7 全 ✅,P2 后续优化轮规划中)
- P3-P5 阶段设计:[p3-wasm-tier](docs/design/p3-wasm-tier/00-overview.md) · [p4-method-jit](docs/design/p4-method-jit.md) · [p5-trace-jit](docs/design/p5-trace-jit.md)
- 工程化机制(hooks/CI/Makefile/发布):[engineering](docs/design/engineering.md)
