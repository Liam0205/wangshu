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

**设计阶段,设计文档集已齐备**,尚无代码实现。

- 战略层:[docs/design/roadmap.md](docs/design/roadmap.md)(动机/校准测量/演进路线/非目标)
- 总览:[docs/design/architecture.md](docs/design/architecture.md)(包布局/组件依赖/tier 映射)
- P1 解释器详细设计(可实现深度,13 篇):[docs/design/p1-interpreter/](docs/design/p1-interpreter/),从 [00-overview](docs/design/p1-interpreter/00-overview.md) 进入
- P2-P5 阶段设计:[p2-bridge](docs/design/p2-bridge.md) · [p3-wasm-tier](docs/design/p3-wasm-tier.md) · [p4-method-jit](docs/design/p4-method-jit.md) · [p5-trace-jit](docs/design/p5-trace-jit.md)
