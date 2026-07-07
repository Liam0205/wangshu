# Wangshu(望舒):Go-native Lua VM 设计目标与演进路线

## 0. 名字与定位

**Wangshu(望舒)**:中国神话中为月亮驾车的神,首见于《楚辞·离骚》
("前望舒使先驱"),后世直接用作月亮的代称(现代诗人戴望舒的笔名同源)。
Lua 在葡萄牙语中意为"月亮"——**为月亮驾车,即驱动 Lua 的引擎**,这正是
一个 Lua VM 的本职。巧合的加持:系外行星 HD 173416 b 在 IAU 2019 全球
命名活动中被命名为 Wangshu,其宿主恒星名为 Xihe(羲和,太阳御者)——
日月御者成对挂在天上。

命名空间核查(2026-06-11):pkg.go.dev / LuaRocks / PyPI / Homebrew 均
空闲,无同名软件项目;GitHub 个人用户名被占,组织名可用。

项目本体:纯 Go 实现的高性能嵌入式 Lua 虚拟机(不依赖 cgo,保持交叉编译能力):

- 近期目标:把 Go 生态的 Lua 执行性能从 gopher-lua 档提升到 LuaJ-luajc 档以上
- 终局目标:在"列内核"负载形状下逼近 LuaJIT 档(纯 Go 约束下 10-30x over gopher-lua)
- 语言面:Lua 5.1 核心(与 LuaJIT 的语言面选择一致,也是嵌入式宿主生态的最大公约数;
  不追语言完备性,见 §5 原则 4)

## 1. 动机:两个校准测量

Go 生态目前唯一的主流纯 Go Lua 实现是 gopher-lua:树遍历前端 + 字节码解释器,
值表示是 interface 装箱,每条指令一次 switch dispatch。它慢在哪、新 VM 的收益
上限在哪,我们用三个嵌入栈做了同机同日的隔离 A/B(16 核 Intel Xeon 6982P-C,
宿主按 per-item 粒度调用 Lua 函数,测 ns/op):

**测量 1:计算密集脚本(Horner 5 次多项式循环,1000 items)**

| 嵌入栈 | 绝对值 | lua/native 倍率 | 技术 |
|---|---|---|---|
| gopher-lua(Go) | 729μs | 6-17x | 纯解释,interface 装箱,switch dispatch |
| LuaJ-luac(Java) | 259μs | 2-9x | JVM 解释器,解释器本体被 C2 编译热 |
| LuaJ-luajc(Java) | 164μs | 2-4.6x | Lua→JVM bytecode,C2 全套优化 |
| LuaJIT(C++) | 154μs | 3-5x | trace JIT,NaN-boxing |

关键事实一:**真 LuaJIT 只比 luajc 快 6%**(154 vs 164μs)。per-item 跨界形式下,
边界跨越 + 值装箱主导成本,脚本本体再快也被钉死。

**测量 2:编译收益在边界主导负载下的稀释**

在某生产规则引擎宿主上,启用 luajc(Lua→JVM bytecode)后:隔离脚本级 -37%,
但宿主端到端 benchmark 前后对照全部落在 ±5-7% 噪声带内——该宿主的生产脚本
绝大多数是单行判断,边界成本主导,VM 层加速端到端不可见。

→ 两个测量共同决定本项目的前提:**收益兑现要求宿主以"列内核"形状调用 VM**
(循环写在 Lua 内,一次调用进一次 VM,整批数据在 VM 内迭代),而不是
per-item 反复跨界。宿主侧的配套改造不在本项目范围内,本项目负责把嵌入
接口设计成天然鼓励这种形状(§8)。

## 2. 硬约束:Go runtime 四项税

任何在 Go 进程内生成/执行机器码的路线都要过这四关:

| 税 | 问题 | 标准解法(wazero 已验证) |
|---|---|---|
| GC 精确栈扫描 | JIT 帧无 stack map | JIT 代码跑自管非 Go 栈,边界按 syscall 语义 |
| 异步抢占 | 抢占信号可落在任意 PC | 生成代码在循环回边插抢占检查点 |
| 栈移动 | morestack 拷贝 goroutine 栈 | JIT 代码不持有指向 Go 栈的指针 |
| 写屏障 | 裸指针写破坏并发 GC 三色不变式 | 值世界放自管 arena/linear memory,边界拷贝 |

推论:**VM 边界跨越是几十~百 ns 的固定成本**,短脚本会被吃光——再次印证
§1 的负载形状前提。wazero(纯 Go Wasm 编译执行引擎,Apache 2.0)是
"纯 Go 运行时机器码生成可行"的存在性证明,也是 exec-mmap / W^X /
icache-flush / trampoline 等系统管线的采石场。

## 3. 第 1 天的架构承诺:值表示

分层 VM 的成败取决于各执行层能否共享值表示与对象模型。岔路口:

| 方案 | 值住哪 | 后果 |
|---|---|---|
| Go 原生 tagged struct | Go 堆 | 上手快;但日后上编译层等于重写整个对象层 |
| **NaN-boxed u64 + 自管 arena**(选定) | 自管线性内存(`[]uint64`/`[]byte`) | 解释器与未来编译码读写同一块内存,编译层是纯增量 |

选定方案的代价:自写 mark-sweep GC(safepoint 限定在分配点与层边界,
根放 shadow stack)。部分自付:NaN-boxing 数字零分配,本身就显著快于
interface 装箱。

## 4. 演进阶段

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (trace 收益~70%)   (10-30x,开放式)
```

倍率均为 over gopher-lua,列内核负载形状下。

执行层沿用月相命名(与项目名同一意象系,代码与文档统一使用):

| tier | 月相名 | 对应 |
|---|---|---|
| tier-0 | **crescent**(新月) | P1 解释器 |
| tier-1 | **gibbous**(凸月) | P3 Wasm 编译层 / P4 method JIT |
| tier-2 | **fullmoon**(满月) | P5 trace JIT |

(日志与诊断输出形如 `function promoted to gibbous`,比裸 tier 编号自释。)

### P1:现代解释器(6-9 人月)

- lexer / parser / 寄存器式字节码(对解释器友好,日后翻译为 Wasm locals 也直白)
- NaN-boxed 值世界 + arena + 自管 mark-sweep GC
- closure-compilation 或 computed-goto 风格 dispatch(替代大 switch)
- 全局/表访问 inline cache;stdlib 以 host function 形式提供
- Lua 5.1 conformance 测试套
- **验收**:简单/算术/循环三档脚本全部 ≥2x over gopher-lua;与官方 Lua 5.1.5 差分
  fuzz 输出逐字节一致(官方为最终语义 oracle;gopher-lua 为同生态参照与性能基准,
  其自身偏离官方处登记豁免——口径细则见 `p1-interpreter/12-testing-difftest.md`)
- 止步于此也成立:一个"更好的 gopher-lua"

### P2:分层桥(1-2 人月)

- 函数级热度计数(loop back-edge 计数)
- inline cache 反馈记录(类型 feedback,为编译层供料)
- 静态可编译性分析器:varargs / coroutine / debug 等形状标记"不升层",
  永远走解释——try-compile-fallback-interpret(LuaJ luajc 一样的策略),
  换来零 deopt 机器

### P3:Wasm 编译层(6-12 人月)

- 字节码→Wasm 编译器,wazero 执行;值世界 = linear memory
  (P1 的 arena 直接映射,两层共见)
- 跨层 trampoline;解释器↔编译层互调协议
- **开工前置 spike**:wazero call boundary 实测(目标 <150ns),
  不达标则跳过本阶段直接做 P4
- **验收**:循环密集脚本相对 P1 再 ≥2x;两层差分 fuzz 逐字节一致
- 战略价值:在不用调试机器码的后端上,先把分层机器
  (升层/降层/fallback)整体跑通

### P4:带 IC 反馈的投机 method JIT(+1-2 人年)

- **立项前置 = 立项判定**(承 [design/p4-method-jit/01-launch-judgment](./p4-method-jit/01-launch-judgment.md)):
  P4 启动前先做立项判定(P3 实际表现 + 真实宿主负载证据 + 资源到位),三档决议产出
  「常规推进 / 暂缓 / 跳过」三选一——与 P3 的「开工前置 spike」节奏对位
- JSC Baseline 风格:per-function 模板编译,IC 反馈做类型投机
  (f64 快速路径 + guard),deopt 简单(函数级 OSR exit 回解释器)
- 继承 P3 的全部分层结构,只换发射后端(Wasm 发射→原生发射)
- amd64 + arm64 双后端;系统管线参考 wazero
- **验收**:列内核负载 ≥ LuaJ-luajc 档;Wasm 层退役,或留作可移植中层
  (未移植架构、禁 exec-mmap 环境)——**P3 去留决策框架详见
  [design/p4-method-jit/07-p3-retirement](./p4-method-jit/07-p3-retirement.md)**
  (本节给方向,具体决策框架在子文档)

### P5:trace-based JIT(+2-4 人年到可信 v1,开放式)

- trace 录制(从字节码)、IR 优化(CSE / 循环不变量外提 / 分配下沉)、
  寄存器分配、snapshot + deopt 机器——LuaJIT 的真正护城河,无处抄
- 只在 P4 的收益不够时启动
- **验收**:列内核负载 10-30x over gopher-lua

## 5. 贯穿原则

1. **解释器永不退役**——它是所有编译层的 deopt 着陆点和语义 oracle
2. **层间逐字节差分测试**——每个执行层的输出与解释器 byte-equal,
   持续 fuzz;这是防"投机错误静默错果"(JIT 最危险 bug 类别)的主防线
3. **每阶段独立交付价值**——任何检查处停下都不亏
4. **不可编译/不可升层形状走 fallback,不做完备性**——可静态分析的子集
   走快路径即可覆盖绝大多数真实负载(Pallene 是 typed-subset 路线的先例;
   我们审计过的一个 262 脚本生产库中,绝大多数是简单形状)

## 6. 非目标

- **让 Lua 直接访问 Go 堆对象**:需要全套 GC 纪律税(handle 根表、
  `runtime.Pinner`、指针写 host call、宿主内部布局影子描述符),且
  Go 内置容器的内存布局每版漂移。替代方案是**数据搬家**:宿主把热数据
  放进双方共见的 arena(Arrow 模型),VM 零拷贝读。本项目只需定义
  arena 内存布局 ABI(§8)。
- **复刻 Go runtime 内部机制**:绝不 inline 复刻
  `runtime.gcWriteBarrier` / `runtime.mallocgc` 等内部符号
  (`go:linkname`,每个 Go 版本都可能碎)。
- **Lua 5.2+ 特性**(goto / _ENV / 整数子类型 / 位运算符 / utf8 库等):
  5.1 核心是嵌入生态的事实标准,LuaJIT 同样停在这里。

## 7. Prior art

| 项目 | 借鉴点 |
|---|---|
| wazero | 纯 Go 运行时 codegen 存在性证明;exec mmap / W^X / 自管栈 / trampoline 参考实现 |
| LuaJ luajc | "编译到宿主已有 codegen 引擎"的范式(P3 的精神来源);try-compile-fallback 策略 |
| LuaJIT | NaN-boxing、trace JIT 架构、Lua 5.1 语义基准 |
| gopher-lua | 反面教材:interface 装箱 + switch dispatch 的成本上限;P1 的差分基准 |
| goja(纯 Go ES5) | 纯 Go 解释器路线的天花板参照 |
| Pallene | typed-subset 编译 + fallback 的可行性先例 |
| V8 Ignition→TurboFan / JSC Baseline→DFG→FTL | 分层 VM 的标准阶梯;解释器先行 |

## 8. 宿主嵌入契约

嵌入接口刻意设计为鼓励列内核形状:

- `Compile(script) → Program`(一次编译,含可编译性探测与层级决定)
- `Program.Call(arena, args)`(一次调用一次跨界;批量数据经 arena 传递)
- **arena ABI**:类型化扁平列(`[]float64` / `[]int64` / `[]bool`)+
  字符串区 + presence bitmap。宿主直接写,VM(解释器与编译层)零拷贝读。
- 不强制 arena:per-item 风格的简易 API 照常提供(对标 gopher-lua 易用性),
  但文档明确其性能档位。

首个目标宿主是一个多运行时规则引擎(其 Go 运行时现用 gopher-lua),
但接口不绑定任何宿主:P1 解释器即可作为 gopher-lua 的 drop-in 候选。

---

附:校准测量的原始数据与方法(三嵌入栈 A/B、端到端稀释对照)留存于
发起方仓库的工作区,正式立项时可整理为本项目 `benchmarks/baseline/`
的一部分。
