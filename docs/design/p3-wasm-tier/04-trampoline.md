# P3-04 跨层 trampoline 与互调协议:三向互调 + CallInfo bit50 写入 + status 链错误冒泡

> 状态:**详细设计**(开工前置 spike 通过后落地,凡涉 wazero API 细节处标注「待 spike 验证」)。本文是 [00-overview](./00-overview.md) §0 文档地图所定的「跨层互调」单一事实源——CallInfo bit50 `callStatus_gibbous`(对 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 的回填请求)、crescent→gibbous 升层函数入口协议、gibbous→crescent/host imported 助手三向分派、status 链错误冒泡、参数/返回值经共见值栈、trampoline 实装骨架、升层日志接通。
>
> 上游契约:[00-overview](./00-overview.md)(P3 总览,本文严格遵守其章节番号与风格基线;§3 第 2 项耦合点 CallInfo bit50、§6 决策速查 trampoline 入口签名 / bit50 写入 / 错误传播、§9 不变式 3/4)、../p3-wasm-tier §5(原稿主体,本文按 §5.1→§1 / §5.2→§2 / §5.3→§3+§4 / §5.4→§5 / §10 不变式 3/4/6 → §8 章节映射展开)。
>
> P1 依赖面:[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1.2 CallInfo word2 bit 布局 + §7 CALL/TAILCALL/RETURN 调用约定 + §7.3 reentry 边界 + §7.6 host 调用约定 + §9 错误冒泡)、[../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md)(§3 路线 B yield 冒泡 + §5 yield 不跨 host 边界)。
>
> P2 依赖面:[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(§4.4 installGibbous 协议写 bit50 + §4.5 多 State 锁 + §5.2 后端 panic recover)、[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(§6 GibbousCode 接口形态 + Run/Dispose/GetTrampoline)。
>
> 下游协作:[02-translation](./02-translation.md)(§3.6 CALL/RETURN 翻译形态调本文,§4 pc 物化协议)、[03-memory-model](./03-memory-model.md)(跨层只传 `base i32`,余从共见栈槽自取)、[05-safepoint-gc](./05-safepoint-gc.md)(层边界 safepoint 与 trampoline 同位)、[07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(yield 不可穿越 gibbous 帧的完整论证 + 线程级 tier 规则,本文 §5 链过去)。

对应 Go 包:`internal/gibbous/wasm`(trampoline 主体 `trampoline.go` + 调度助手 `helpers.go`),与 [02-translation](./02-translation.md) §6.1 的包布局共题。

---

## 0. 定位:P3 跨层协议的单一事实源

P3 工作流是「P2 喂料 → P3 翻译 → wazero 执行」三段式([00-overview](./00-overview.md) §2)。[02-translation](./02-translation.md) 承担「翻译」,本文承担**翻译产物如何与解释器世界互调**——升层入口、慢路径出口、错误冒泡通道三条主线。一句话职责:**定义 crescent ↔ gibbous ↔ host 三向互调协议,使 gibbous 帧在调用链中与 crescent 帧无缝衔接,且 traceback / 错误定位与解释器逐字节一致**。

### 0.1 协议总则:CallInfo 是唯一真相,跨层不换 ABI

本文最高指导原则,贯穿全篇,任何实现倾向先过这条:

> **调用链状态全住 arena 的 CallInfo([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2);gibbous 帧同样压 CallInfo;参数/返回值全经共见值栈;跨层只传 `base i32`。**

物理含义三句:

1. **不发明新调用记录**:gibbous 帧不在 Go 栈、不在 wazero 栈上额外维护一份「调用状态」——它压的 CallInfo 与 crescent 帧压的 CallInfo 是同一种结构、同一块 arena。这是 [00-overview](./00-overview.md) §9 不变式 3「CallInfo 唯一真相」的兑现。
2. **不换 ABI**:从 crescent 帧到 gibbous 帧、从 gibbous 帧回 crescent 帧,调用约定不变——还是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7 的「被调 closure 在 `base-1`、参数在 `[base, base+nargs)`、返回值回填 `R(A..)`」那一套。trampoline 只是「换执行引擎」的薄层,不引入新的参数传递机制。
3. **跨层只传一个 i32**:升层入口 `(func $proto_N (param $base i32) (result i32))` 只收 `base`(R0 字节偏移),其余(参数值、常量、IC slot、nresults)全从共见栈槽与编译期烧入的立即数自取。这正是 spike S2 测的形状([01-spike-gate](./01-spike-gate.md) §1.2)——单 i32 跨层是 wazero call boundary 的最廉价形态。

### 0.2 三条主线与本文章节

| 主线 | 含义 | 本文章节 |
|---|---|---|
| **升层入口** | crescent 的 doCall 检测到 callee 已升 gibbous → 进 wazero | §2 |
| **慢路径出口** | gibbous 代码遇到 CALL / 算术慢路径 / IC miss / 分配 → imported 助手回 Go | §3 |
| **错误冒泡通道** | gibbous 内出错 → status 链一路 return 到 protected 边界 | §4 |

外加三个支撑章节:bit50 协议(§1,跨层标识)、yield 不可穿越(§5,链 07)、实装骨架与日志接通(§6/§7)。

### 0.3 本文与现稿 §5 的关系

现稿 ../p3-wasm-tier §5 是 45 行综述;本文是其展开式,覆盖:

| 现稿 §5 章节 | 本文位置 | 展开内容 |
|---|---|---|
| §5.1 协议总则 + CallInfo 唯一真相 | §0.1 + §1 | bit50 设计意图、P3 起的语义、P1 透明性、不改现存帧、P1 实代码现状、回填请求 |
| §5.2 crescent → gibbous 入口 | §2 | 入口签名、doCall gibbous 分支三步走 Go 骨架、status 处理、单 i32 协议 |
| §5.3 gibbous → crescent/host 助手 | §3 | `$h_call` 三向分派表 + Go callback 实装、多种 helper 列举、imported declaration + registration |
| §5.3 错误传播段 | §4 | status 链、与 05 §9 同构、pcall 清理责任、错误可穿越、traceback、CallInfo 链清理 |
| §5.4 yield 不可穿越 | §5(短) | 承诺 + 物理限制摘要,详细论证全链 07 |
| §10 不变式 3/4/6 | §8 | 6 条不变式聚合 |
| §11 缺口前几条 | §9 | gibbous→gibbous 直调、helper 拆 trait、对 05/04 回填 |

### 0.4 与 P4(原生 JIT)的关系

P4 **继承本文的全部跨层协议**,只换发射后端([../p4-method-jit/01-launch-judgment](../p4-method-jit/01-launch-judgment.md) §2 常规路径承诺)。具体地:

- bit50 `callStatus_gibbous` 在 P4 同样标 1(P4 帧也是 gibbous tier,[00-overview](./00-overview.md) §1 坐标系警告:P3/P4 同 tier-1)；
- 升层入口签名 `(base i32) → status i32` 在 P4 是 asm trampoline stub 的形态([../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.3 `jitTrampolineEnter`),语义同一；
- status 链错误冒泡协议 P4 原样继承（P4 多一个 `status=2 DEOPT` 出口，是 P4 独有的投机失败 OSR exit，[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1；**P3 的 trampoline 永远不返回 2**，[00-overview](./00-overview.md) §1）；
- imported 助手三向分派(§3)在 P4 退化为原生码直接 call Go 函数（无 wazero 中介），但分派语义同一。

**所以本文是 P3/P4 共用的跨层协议规范**——把跨层协议一次定稳，P4 阶段不动协议,只换「进入执行引擎」与「从执行引擎调 Go」的两个物理桩。

### 0.5 章节 → P-Wasm 里程碑映射(实现者导航)

本文各协议点落在哪个 P-Wasm 里程碑([00-overview](./00-overview.md) §4),供实现者按 PW 顺序施工时定位:

| 协议点 | 本文章节 | 落地 PW | 验收关联([00-overview](./00-overview.md) §4) |
|---|---|---|---|
| 升层入口签名 + doCall gibbous 分支 + status 处理 | §2.1-§2.4 | **PW2** | 5-op Proto 升层后 byte-equal + 升层日志触发 |
| 算术/IC 慢路径助手(h_arith / h_gettable) | §3.3 | **PW3 / PW5** | 混合类型走助手结果逐字节一致 / 形状变化走助手仍正确 |
| 回边 safepoint 助手(h_safepoint) | §3.3 | **PW4** | 数值 for 编译后 ≥2x + 回边 GC byte-equal |
| CALL/TAILCALL/RETURN + 三向分派 + status 链错误冒泡 | §2.2 / §2.5 / §3.1-§3.2 / §4 | **PW6** | gibbous 内调未编译 Proto 经 trampoline 出去由 crescent 跑;错误穿越冒泡到 pcall 边界 |
| bit50 写入(CallInfo struct 加字段)+ 对 P1 05 回填 | §1 | **PW6** | 与跨层协议同批落地 |
| 线程级 tier 守卫(`th == mainThread`) | §5(链 07) | **PW8** | 协程内即便 hot + Compilable 也保持 TierInterp |
| 升层日志接通 | §7 | **PW2 起** | 升层日志格式断言(promote/stuck/fail 三时点) |
| 跨层差分(traceback 逐字节)+ panic 兜底实测 | §4.5 / §6.5 | **PW9** | V1-V18 全过 + 强制全升模式 + GC 压力 fuzz |

**关键:跨层协议主体在 PW6 落地**(CALL 系列 + 三向分派 + status 链),与 [00-overview](./00-overview.md) §5 人月分解「PW6 是大头(1-2 人月,三路分派 + 错误冒泡 + pcall 边界清理)」一致。PW2 先落最小入口(单 i32 升层 + RETURN),PW6 补全调用与错误冒泡——这与 [02-translation](./02-translation.md) §1.3 的 opcode 渐进白名单同步(CALL/RETURN 在 PW6 才加入 supported 表)。

---

## 1. CallInfo bit50 `callStatus_gibbous`(对 P1 05 的回填请求)

### 1.1 P1 05 §1.2 word2 bit50 的设计意图(P1 当前预留)

[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 已给 CallInfo 的 word2 布局,bit50 是为 P3 预留的标识位:

```
CallInfo[i].word2  (arena 内):
  [31:0]  protoID             (本帧 closure 的 ProtoID;host 帧为哨兵 0xFFFFFFFF)
  [47:32] nresults            (调用者期望的返回值个数;C-1,0xFFFF 表示「可变/到 top」)
  [48]    callStatus_tailcall (本帧是尾调用产生,RETURN 时特殊处理)
  [49]    callStatus_fresh    (本帧是「reentry 边界」,见 05 §7.3)
  [50]    callStatus_gibbous  (P3+:本帧在 gibbous 编译码中执行,承 [../p3-wasm-tier] §5.1;P1 恒 0)
  [63:51] reserved
```

bit50 与 bit48/bit49 同属 `callStatus_*` 位族——它们都是「关于这一帧执行方式的元信息」,挤在 word2 的高位与 protoID/nresults 共用一个字。设计意图:**用一位标识此帧的执行引擎是 gibbous(Wasm)还是 crescent(解释器)**,供 trampoline 在跨层时判流向、供 traceback 在错误回溯时区分帧类型(虽然差分口径不为 gibbous 帧开任何显示豁免,[08-testing-strategy](./08-testing-strategy.md) §2)。

### 1.2 P3 起的语义:gibbous 帧入口 trampoline 写 1

P1 阶段 bit50 恒 0(P1 无 gibbous 层)。**P3 起的语义**:

> gibbous 帧入口 trampoline(§2)在压新 CallInfo 时,把该帧的 bit50 置 1,标识「此帧走 Wasm 路径」。

这一位的读者是 **trampoline 自己**——在跨层调度(§3)与错误冒泡(§4)时,trampoline 据 bit50 判断「当前帧是 gibbous 帧还是 crescent 帧」,从而决定弹帧/清理的具体路径。它也是 P4 trampoline 的依赖(P4 帧同标 bit50,§0.4)。

```
bit50 的「写入者 / 读取者」分工:
  写入者:P3 trampoline(进 gibbous 帧时,§2.2 step2)
  读取者:P3 trampoline 自己(跨层调度判流向 §3 / 错误冒泡判清理 §4)
          也是 P4 trampoline 的依赖
  不读者:P1 解释器主循环(§1.3 透明性)
```

### 1.3 P1 解释器主循环不读 bit50(P1 不感知 gibbous,bit 对它透明)

**关键纪律**:P1 解释器主循环([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §12 完整骨架)**从不读 bit50**。

物理原因:解释器执行一帧时,它本身就是 crescent 执行引擎——它不需要问「这帧是不是 gibbous」,因为它只可能在自己执行的帧上跑(crescent 帧)。一个标了 bit50=1 的 gibbous 帧,根本不会被解释器主循环执行——它在 trampoline 跳进 wazero 后,由 Wasm 代码执行(§2.2 step3)。所以:

- **bit50 对解释器透明**:解释器读 CallInfo 时只读 base/protoID/nresults/savedPC 等它需要的字段,bit50 不在它的读取面。
- **这是「P1 不感知 gibbous」原则的兑现**:P1 是独立完整交付的层,P3 是在其上叠加的可选加速面([00-overview](./00-overview.md) §1)。P1 代码不需要为 P3 的存在改任何执行逻辑——bit50 是 P3/P4 trampoline 之间的私有约定,只是物理上借住在 P1 定义的 CallInfo 结构里。
- **回填的本质是「登记一位的语义」,不是「让 P1 读它」**:对 P1 05 的回填(§1.6)只是把 bit50 的语义从「预留」升级为「P3 trampoline 写,P1 不读」,P1 主循环代码零改动。

### 1.4 不在升层瞬间改现存帧的此字段

**纪律(承 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.4)**:installGibbous 把 Proto 升 gibbous 时,**不改任何现存 CallInfo 的 bit50**——现存 CallInfo 仍跑 crescent,新进帧的新 CallInfo 才标 1。

物理原因:Proto 在升层瞬间可能正被某个 State 解释跑(其 CallInfo bit50=0)。如果升层那一刻强行把这一帧的 bit50 改成 1,会让 P1 解释器在执行中途「突然变成 gibbous 帧」——但解释器并不会因为 bit50 变了就跳去 wazero(它不读 bit50,§1.3),所以这一改只会制造一个「标了 gibbous 却在被解释器跑」的不一致帧。正确做法:

```
升层瞬间的两种合法可观察态(installGibbous 后,[../p2-bridge/04] §4.4):
  ① Proto 还没升:trampoline 表无该 Proto + 新进帧 bit50 不写(走 crescent doCall 默认分支)
  ② Proto 已升:  trampoline 表有该 Proto + 新进帧 bit50 写 1(走 §2.2 gibbous 分支)
  ★ 没有「半升半不升」的中间态——bit50 写入与 trampoline 表注册对 P1 视角原子
    (P2 单 State 内同步调用,无并发竞争,[../p2-bridge/04] §3.5;多 State 经
     compileMu 锁 + 双重检查,[../p2-bridge/04] §4.5)
```

要点:**bit50 的「物化」发生在「下次该 Proto 被作为 Lua 帧进入」时**(由 §2 的 doCall gibbous 分支决定),不是 installGibbous 那一刻立即改所有现存 CallInfo。旧 CallInfo(bit50=0)直到自己 RETURN 自然消亡,期间一直跑 crescent——同一帧的执行体不中途换引擎,这是「同一帧执行体」的语义保证。

### 1.5 P1 实代码现状:callInfo 是普通 Go struct

P1 当前实装(`internal/crescent/state.go` 的 `callInfo` struct)是普通 Go struct,**未预留 bit50 字段**:

```go
// internal/crescent/state.go(P1 现状,M9 简化版)
//
// **gibbous 标识位**(p2-bridge/04 §4.4 word2 bit50 callStatus_gibbous)
// 当前**未在 callInfo 上预留字段**——P3 trampoline 真落地时再加,以免本期
// 引入无人读写的死字段被 lint 抓出。预留语义记在 installGibbous 注释里。
type callInfo struct {
    base     int             // R0 在 stack 的绝对索引
    funcIdx  int             // 被调 closure 槽(funcIdx = base-1)
    top      int             // 本帧逻辑顶
    proto    *bytecode.Proto // 当前 Proto
    cl       arena.GCRef     // 当前 closure
    nresults int             // 调用者期望的返回数;-1 = 可变
    tailcall bool
    fresh    bool            // execute 重入边界
    varargs  []value.Value
    pc       int32
}
```

P3 落地时引入 bit50 有两种实装形态,**P3 落地时定**:

| 形态 | 实装 | 优劣 |
|---|---|---|
| (a) 位打包(设计文档 word2 形态) | callInfo 改成 4-word arena 结构,bit50 是 word2 的真实位 | 与设计文档 §1.2 逐位对齐;arena 化(值栈/CallInfo 迁 arena 是 P3 前置,[03-memory-model](./03-memory-model.md) §1)同批落地 |
| (b) bool 字段简化版 | callInfo 加 `gibbous bool` 字段,与现有 `tailcall/fresh bool` 同款 | 实装最简;与 P1 现状(tailcall/fresh 都是 bool)一致;但与 word2 位布局形式上不对齐(语义等价) |

**P3 落地时定**——(b) 的简化版与 P1 现有 `tailcall/fresh bool` 同款,落地阻力最小;(a) 的位打包要等值栈/CallInfo arena 化([03-memory-model](./03-memory-model.md) §1 收养 wazero memory)一起做。本文不预判,只登记两种形态都满足「§1.2-§1.4 的语义」即可。

### 1.6 对 P1 05 的回填请求(本文不主动改)

**回填请求(承用户裁决「本期只记录不主动改」,[00-overview](./00-overview.md) §7 结论)**:

| 回填项 | 落点 | 内容 | 状态 |
|---|---|---|---|
| bit50 字段语义登记 | [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 | 把 bit50 `callStatus_gibbous` 从「P1 恒 0 预留」升级为「P3 trampoline 写,P1 不读;语义见 P3 04 §1」 | **记录,不主动改** |
| callInfo struct 加 gibbous 标识 | `internal/crescent/state.go` | P3 PW6 落地时按 §1.5 (a)/(b) 之一加字段;现状注释已留语义于 installGibbous | **P3 落地时同批补**(PW6) |

P1 本期已在 `installGibbous` 注释里留语义指针(`internal/bridge/bridge.go`:「**CallInfo bit50 写入**都在 PB6/P3 落地时补上」),与 [00-overview](./00-overview.md) §7 对账表「bit50:P3 落地时同批补」一致。**本文记录此回填请求,不在本轮修改 P1 05 或 crescent 代码**。

---

## 2. crescent → gibbous(升层函数入口)

本节是「升层入口」主线(§0.2)的单一事实源——crescent 的 doCall 检测到被调 Proto 已升 gibbous 时,如何从解释器世界进入 wazero 执行。核心是 §2.2 的三步走(形参搬移 → 压 CallInfo 标 bit50 → 进 wazero)与 §2.1 的单 i32 入口签名(= spike S2 形状)。§2.5 给 TAILCALL 的复用帧特例,§2.6 给一次完整跨层调用的全景 trace。

### 2.1 入口签名:`(func $proto_N (param $base i32) (result i32))`

每个升层的 Proto 翻译产物([02-translation](./02-translation.md) §1.2 每 Proto 一 module)导出一个入口函数:

```wat
;; 每 Proto 一个导出入口(WAT 风格,实际由 wazero 从字节码构造)
(func $proto_N (param $base i32) (result i32)
  ;; $base = 本帧 R0 在 thread.valueStack 的字节偏移(共见 linear memory)
  ;; 返回 i32 status:0 = OK,1 = ERR
  ;; ... 翻译后的指令体(02-translation §3 各 opcode 形态)...
  ;; RETURN opcode 翻译产物:把返回值回填 R(A..) 共见栈槽,push status,return
)
```

两个数字的语义钉死:

- **入参 `$base i32`**:R0 在 `thread.valueStack` 的**字节偏移**(不是字索引,不是绝对指针)。gibbous 代码用 `$base + 8*reg` 寻址第 reg 个寄存器([03-memory-model](./03-memory-model.md) §2,NaN-box u64 = 8 字节,寄存器槽 8 对齐)。**为什么是字节偏移而不是字索引**:linear memory 的 `i64.load` 取字节地址,字节偏移直接做 load 的地址操作数,免一次 `<<3`。
- **返回 `i32 status`**:`0 = OK`(返回值已回填 `R(A..)`,调用方弹 CallInfo 继续解释)、`1 = ERR`(`state.pendingErr` 已置,走 §4 错误冒泡)。**P3 永远不返回 2**(2=DEOPT 是 P4 OSR exit 专用,[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1)——P3 零 deopt,[00-overview](./00-overview.md) §9 不变式 1。

**入口协议只有一个 i32(base)**——参数在栈槽、常量烧在代码里、nresults 从 CallInfo 读、IC slot 编译期固化([06-ic-feedback-consume](./06-ic-feedback-consume.md) §1)。这正是 spike S2 测的形状([01-spike-gate](./01-spike-gate.md) §1.2:单 i32 入参 + i32 返回的跨层成本 < 150ns 是 P3 生死闸门)。**任何「给入口加第二个参数」的实现倾向都直接判否**——它会偏离 spike 实测的形状,使 §1 摊销模型失效。

### 2.2 doCall 的 gibbous 分支:三步走流程 + Go 实代码骨架

crescent 的 `doCall`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.1)在 Lua closure 分支里增加一个 gibbous 子分支:

```
doCall 的分发增量(05 §7.1 的 isLuaClosure 分支细化):
  callee 是 Lua closure:
    proto := protos[protoIDOf(cl)]
    if proto.tierState == TierGibbous && th == mainThread:   ← gibbous 分支(线程级 tier,§5)
       三步走(见下)
    else:
       enterLuaFrame + reentry(05 §7.1 原样,跑 crescent)
```

三步走流程:

```go
// internal/gibbous/wasm/trampoline.go —— crescent → gibbous 升层入口
//
// 调用方:crescent 的 doCall(05 §7.1)在 detect 到 callee 的 tierState ==
//   TierGibbous(且主线程,§5)时调用。
//
// 三步走:
//   step1 形参搬移/adjustVarargs 照旧(P2 F1 已排除 vararg 形态)
//   step2 压 CallInfo 标 bit50 gibbous
//   step3 fn.Call(ctx, base) 进 wazero
func (t *Trampoline) enterGibbous(st *State, f *frame, i Instruction, cl value.GCRef) callResult {
    a := A(i)
    proto := st.protos[protoIDOf(cl)]
    nargs := computeNargs(f, i)        // B>0: B-1;B=0: top-(base+a+1)(05 §7.1 同款)
    nresults := C(i) - 1               // C>0: C-1;C=0: 0xFFFF(到 top,可变)
    newBase := f.base + a + 1          // 新帧 base:R(A) 之后第一个槽

    // ── step1:形参搬移/adjustVarargs 照旧(与 enterLuaFrame 同款,05 §1.4 + §8.5)──
    //   P2 F1 闸门已排除 vararg 形态([../p2-bridge/03] §3.7 F1),所以这里的
    //   adjustVarargs 在 gibbous 路径上恒走「非 vararg」分支(固定 NumParams 形参,
    //   多退少补 nil 到 MaxStack)——但代码与 crescent 共用,不分叉。
    adjustVarargs(f, proto, newBase, nargs)
    ensureStack(st.runningThread, newBase+int(proto.MaxStack))

    // ── step2:压 CallInfo,标 bit50 gibbous ──
    //   与 enterLuaFrame 的压栈同款,唯一差异是 callStatus_gibbous = 1。
    //   bit50 在新 CallInfo 上写 1(§1.2);现存帧的 CallInfo 不动(§1.4)。
    ci := pushCallInfo(st.runningThread)
    ci.base, ci.protoID, ci.nresults = newBase, protoIDOf(cl), nresults
    ci.savedPC = f.pc                  // 调用者断点(返回本帧时恢复)
    ci.top = newBase + int(proto.NumParams)
    ci.cl = cl
    ci.gibbous = true                  // ★ bit50 callStatus_gibbous = 1(§1.5 (b) 形态;
                                       //   (a) 位打包形态写 word2 bit50)

    // ── step3:进 wazero(一次跨层,目标 <150ns,[01-spike-gate] §1)──
    //   base 转字节偏移(R0 在 valueStack 的字节偏移,§2.1)。
    baseBytes := int32(newBase) * 8    // NaN-box u64 = 8 字节,字索引 → 字节偏移
    code := t.codeOf(proto)            // §6.1 trampoline 表查 GibbousCode
    status := code.Run(st, baseBytes)  // [../p2-bridge/05] §6.1 GibbousCode.Run
                                       //   = c.fn.Call(st.ctx, uint64(baseBytes))(§6.2)

    // ── status 处理(§2.3)──
    switch status {
    case statusOK: // 0
        // 返回值已在 R(A..)(Wasm 侧 RETURN 助手按 nresults 回填,§4.7 h_return 或
        //   RETURN opcode 自身快路径);弹 CallInfo,主循环继续解释调用者帧。
        popCallInfo(st.runningThread)
        // 若 caller 期望可变返回(nresults=0xFFFF),caller 的 top 已被 Wasm 侧更新。
        return callReturnedGibbous     // 主循环不重载 code(没切到新 crescent 帧)
    case statusERR: // 1
        // st.pendingErr 已置(Wasm 帧经 h_call 或 h_raise 设置),走 05 §9 错误冒泡。
        popCallInfo(st.runningThread)  // gibbous 帧由 trampoline 弹出(§4.6)
        return vm.throwPending(f)      // 一路 return *LuaError(05 §9.2)
    }
}
```

要点:

- **step1 与 crescent 共用 `adjustVarargs`/`ensureStack`**:不为 gibbous 路径分叉形参搬移逻辑——P2 F1 已保证非 vararg([../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) §3.7),所以 `adjustVarargs` 恒走简单分支,但代码同源,差分时不引入「gibbous 路径形参搬移与 crescent 路径有差异」的 bug 面。
- **step2 唯一差异是 bit50=1**:压 CallInfo 的字段(base/protoID/nresults/savedPC/top/cl)与 `enterLuaFrame`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.4)完全一致,唯一增量是 `ci.gibbous = true`。
- **step3 是「一次跨层」**:`code.Run(st, baseBytes)` 内部就是一次 `fn.Call(ctx, base)`([../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.2),Go→Wasm 一次边界穿越。这一次穿越的成本是 [01-spike-gate](./01-spike-gate.md) §1 的核心闸门(S2 < 150ns)。
- **被调 Wasm 函数从 `$base` 自取一切**:参数在栈槽 `[base, base+nargs)`、常量烧在代码里、IC slot 编译期固化、nresults 从 CallInfo word2 读。

### 2.3 status 处理:OK 弹 CallInfo 继续解释 / ERR 走 P1 09 错误冒泡

step3 返回的 status 决定后续:

| status | 含义 | trampoline 处理 |
|---|---|---|
| **0 = OK** | Wasm 函数正常 RETURN,返回值已按 nresults 回填 `R(A..)`(funcIdx 位起,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.2 moveResults 同款语义) | 弹本帧 CallInfo,`return callReturnedGibbous`——主循环不重载 code(没切到新 crescent 帧,与 callReturnedHost 同位,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.6)。caller 期望可变返回时,caller 的 top 已被 Wasm 侧 RETURN 助手更新 |
| **1 = ERR** | Wasm 函数遇到不可恢复错误(经 `h_call`/`h_raise` 把 `state.pendingErr` 置好后,自身 return 1) | 弹本帧 CallInfo(§4.6),`return vm.throwPending(f)`——把 `state.pendingErr` 作为 `*LuaError` 一路 return 出 execute([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.2 throw),直到撞上 protected 边界(§4.3) |

**OK 路径的返回值回填语义**:Wasm 侧的 RETURN opcode 翻译产物([02-translation](./02-translation.md) §3.6)负责「把 `R(A..A+nret)` 搬到 funcIdx 位起、按调用者 CallInfo 记录的 nresults 多退少补」——与解释器 `doReturn` 的 `moveResults`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.2)逐字节同构。返回值落点是共见栈槽,trampoline 弹 CallInfo 后,主循环看到的返回值已就位,与「调用了一个 crescent 函数返回」无可观察差异。

**ERR 路径的错误传播**:见 §4(status 链)。关键是 trampoline 在 ERR 时也要弹本帧 CallInfo(gibbous 帧由 trampoline 负责弹出,§4.6),然后把 pendingErr 转成 throw 路径。

### 2.4 入口协议只有一个 i32(base):正是 spike S2 测的形状

重申 §2.1 的纪律,因为它是 P3 生死闸门的直接关联:

> **升层入口 `(param $base i32) (result i32)` 只有一个 i32 入参 + 一个 i32 返回——这正是 [01-spike-gate](./01-spike-gate.md) §1.2 spike S2 测的形状。**

为什么钉死单 i32:

- **S2 测的就是这个形状**:[01-spike-gate](./01-spike-gate.md) §1.2 的 S2 样本是「Go 调一个 wazero 函数,传一个 i32 整数,函数读一次 linear memory 后返回一个 i32」。S2 < 150ns 是 P3 开工闸门(PW0,[00-overview](./00-overview.md) §4)。如果实际入口签名偏离 S2(比如传 3 个参数、传指针、传结构),spike 实测值不再代表真实跨层成本,§1 摊销模型失效。
- **一切都能从 base 自取**:[03-memory-model](./03-memory-model.md) §2 证明了「值世界全在共见 linear memory」——参数、常量、IC slot、CallInfo 全可经 `base` 与编译期立即数寻址到,**不需要再传任何东西**。多传参数是冗余,且每多一个参数都增加跨层成本(wazero 的参数 marshalling)。
- **链 01-spike-gate**:本协议是 S2 形状的「正式化」——spike 验证形状可行(< 150ns),本文把它定为协议。若 spike 不达标(S2 ≥ 150ns),P3 整体跳过([01-spike-gate](./01-spike-gate.md) §1.4 直跳 P4),本协议随之作废。

### 2.5 TAILCALL 跨 gibbous:复用帧,CallInfo 数不变

crescent 的 TAILCALL([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.5)是「复用当前帧、CallInfo 数不变」的尾调用。当被尾调用者升 gibbous 时,trampoline 的处理与 §2.2 的 CALL 入口有关键差异——**不压新 CallInfo,而是原地改写当前 CallInfo**:

```go
// internal/gibbous/wasm/trampoline.go —— crescent → gibbous 尾调用入口
//
// 调用方:crescent 的 doTailCall(05 §7.5)在 detect 到被尾调用者升 gibbous 时调用。
// 关键差异 vs enterGibbous(§2.2):TAILCALL 复用当前帧,CallInfo 数不变
//   (栈深度恒定,05 §7.5)。
func (t *Trampoline) tailEnterGibbous(st *State, f *frame, i Instruction, cl value.GCRef) callResult {
    proto := st.protos[protoIDOf(cl)]
    nargs := computeNargs(f, i)
    th := st.runningThread

    // ── step1:关闭当前帧开放 upvalue(≥ base):当前帧即将被覆盖(05 §7.5 step1)──
    closeUpvals(f, f.base)

    // ── step2:callee + args 下移覆盖当前帧(原地替换,base 不变,05 §7.5 step2)──
    moveDownForTailcall(f, A(i), nargs)
    adjustVarargs(f, proto, f.base, nargs)  // F1 已排除 vararg,恒走简单分支
    ensureStack(th, f.base+int(proto.MaxStack))

    // ── step3:改写当前 CallInfo(protoID 换新,base 不变,nresults 继承)──
    //   ★ 不 pushCallInfo —— 复用当前 ci(05 §7.5 step3 的 CallInfo 原地改写)。
    ci := th.curCI()
    ci.protoID = protoIDOf(cl)
    ci.tailcall = true        // bit48 callStatus_tailcall(traceback 显示 (...tail calls...))
    ci.gibbous = true         // ★ bit50 改写为 gibbous(本帧从此走 Wasm 路径)
    ci.cl = cl

    // ── step4:进 wazero(与 §2.2 step3 同款)──
    baseBytes := int32(f.base) * 8
    code := t.codeOf(proto)
    status := code.Run(st, baseBytes)

    // ── status 处理:尾调用透传调用者期望 nresults,弹帧后退到「调用者的调用者」──
    switch status {
    case statusOK:
        popCallInfo(th)          // 弹掉这个复用帧(等价于原帧 RETURN)
        return callReturnedGibbous
    case statusERR:
        popCallInfo(th)
        return vm.throwPending(f)
    }
}
```

要点:

- **不压新 CallInfo,改写当前 CallInfo**:与 §2.2 CALL 的 `pushCallInfo` 不同,TAILCALL 改写 `th.curCI()`——`for i=1,1e9 do return f() end` 式尾递归在 gibbous 路径下同样**栈深度恒定**(CallInfo 数不变,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.5)。
- **bit50 在改写时设(不是新帧)**:TAILCALL 改写当前 CallInfo 的 bit50——本帧从 crescent 帧「原地变成」gibbous 帧。这是 §1.4「不改现存帧 bit50」的**唯一合法例外**:TAILCALL 语义本就是「当前帧被新函数完全替换」(关 upvalue、下移参数、改 protoID),所以改 bit50 是「替换帧的执行引擎」的一部分,与「执行中途换引擎」(§1.4 禁止)不同——TAILCALL 这一帧的旧函数已彻底退出,新函数从 pc=0 开始执行。
- **gibbous → gibbous 的尾调用**:若 gibbous 代码内部发出 TAILCALL([02-translation](./02-translation.md) §3.6),它经 `h_call`(§3.2)分派,被调者升 gibbous 时同样走「改写当前 CallInfo + 再 fn.Call」——但「复用帧」语义在 Wasm 侧由 RETURN 翻译产物处理(尾调用的返回值透传调用者期望 nresults)。TAILCALL 的跨 gibbous 形态细节归 [02-translation](./02-translation.md) §3.6,本文只定「改写当前 CallInfo bit50、不压新帧」这一协议点。
- **host 尾调用**:被尾调用者是 host fn 时,走 `tailCallHost`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.5),其结果作为本帧返回值——与 crescent 同款,gibbous 不特殊处理。

### 2.6 一次完整跨层调用的全景 trace(worked example)

把 §2.2 / §2.3 / §3 / §4 串起来,以「主线程脚本调一个升 gibbous 的 `dot` 函数,`dot` 内部调一个未编译的 `helper`」为例,走一遍完整 trace:

```
脚本:
  function helper(x) return x * 2 end       -- 未升层(冷,跑 crescent)
  function dot(a, b) return helper(a) + b end -- 已升层(热,TierGibbous)
  local r = dot(3, 4)                        -- 主线程顶层调用

trace(Go 栈深度 / wazero 栈 / CallInfo 链 三轴):
┌─ 时刻 T0:crescent 主循环执行顶层 chunk 的 CALL dot ─────────────────┐
│  Go 栈:  execute(顶层)                                              │
│  CallInfo:[chunk(bit50=0)]                                          │
│  doCall 见 dot.tierState==TierGibbous && th==mainThread             │
│    → enterGibbous(§2.2):                                           │
│       step1 形参 a=3,b=4 搬到 newBase                              │
│       step2 压 CallInfo[dot](bit50=1),savedPC=chunk 的 CALL 下条   │
│       step3 dot.fn.Call(ctx, baseBytes)  ← 跨层进 wazero            │
└────────────────────────────────────────────────────────────────────┘
                              │
┌─ 时刻 T1:wazero 执行 dot 的 Wasm 代码 ───────────────────────────────┐
│  Go 栈:  execute(顶层) → dot.fn.Call(Go→Wasm 边界)                 │
│  wazero 栈:[dot 帧]                                                 │
│  CallInfo:[chunk(0), dot(1)]                                       │
│  dot 翻译产物执行到 CALL helper:                                    │
│    call $h_call(helperBase, pc_of_CALL)  ← imported,跨层回 Go       │
└────────────────────────────────────────────────────────────────────┘
                              │
┌─ 时刻 T2:h_call callback(Go)三向分派 ──────────────────────────────┐
│  Go 栈:  execute(顶层) → dot.fn.Call → h_call callback              │
│  h_call 入口:th.curCI().savedPC = pc_of_CALL(pc 物化,§4.5)        │
│  callee = helper,isLuaClosure + helper.tierState==TierInterp        │
│    → default 分支(§3.2 ②):crescent fresh reentry                  │
│       st.executeFrameForGibbous(th, helper, helperBase):           │
│         压 CallInfo[helper](fresh, bit50=0)                        │
│         vm.execute(th)  ← 新 execute 循环(Go 栈 +1)               │
└────────────────────────────────────────────────────────────────────┘
                              │
┌─ 时刻 T3:crescent 跑 helper 帧(解释器)────────────────────────────┐
│  Go 栈:  execute(顶层) → dot.fn.Call → h_call → execute(helper)    │
│  CallInfo:[chunk(0), dot(1), helper(0,fresh)]                      │
│  helper 解释执行 x*2(MUL 快路径),RETURN 6:                        │
│    moveResults 把 6 回填 helperBase-1(funcIdx 位)                  │
│    弹 CallInfo[helper],退到 fresh 帧 → execute(helper)返回 nil 错  │
└────────────────────────────────────────────────────────────────────┘
                              │  返回 status=0(OK)
┌─ 时刻 T4:h_call 返回 Wasm,dot 继续 ─────────────────────────────────┐
│  Go 栈:  execute(顶层) → dot.fn.Call → h_call → (返回)             │
│  h_call return 0 给 Wasm                                            │
│  CallInfo:[chunk(0), dot(1)]                                       │
│  dot 翻译产物:helper 返回值 6 已在共见栈槽,继续 ADD 6+b(=6+4=10)  │
│    RETURN 翻译产物:把 10 回填 dotBase-1,push status=0,return     │
└────────────────────────────────────────────────────────────────────┘
                              │  dot.fn.Call 返回 status=0
┌─ 时刻 T5:enterGibbous 收 status=0,回 crescent 主循环 ────────────────┐
│  Go 栈:  execute(顶层)                                              │
│  CallInfo:[chunk(0)]  ← 弹掉 dot 帧                                 │
│  enterGibbous step3 后 statusOK:popCallInfo(dot),返回             │
│    callReturnedGibbous;主循环继续,r=10 已在栈槽                    │
└────────────────────────────────────────────────────────────────────┘
```

这个 trace 揭示几个协议要点:

- **Go 栈深度 = 「跨层穿越 + host→Lua 重入」的层数,不是 Lua 调用深度**:T3 时 Go 栈有 4 层(execute顶层 / dot.fn.Call / h_call / execute(helper)),但 Lua 调用深度只有 3(chunk→dot→helper)。每次「crescent→gibbous」(dot.fn.Call)和每次「gibbous→crescent」(h_call 起 execute)都加一层 Go 栈——这是基线方案(每 Proto 一 module + Go 往返)的固有代价,优化项 call_indirect 直调(§3.1 + §9 缺口)消除「gibbous→gibbous」那一层 Go 往返,但「gibbous→crescent」必然加 execute(无法避免,因为要跑解释器)。
- **CallInfo 链贯穿所有引擎**:整个 trace 里 CallInfo 链(`[chunk, dot, helper]`)是连续的——无论某一帧是 gibbous(dot,bit50=1)还是 crescent(chunk/helper,bit50=0),它们都在同一条 CallInfo 链上,bit50 只是标识执行引擎。这是 §0.1「CallInfo 唯一真相」的具体兑现:traceback 遍历这条链时,看到的就是逻辑上的 Lua 调用链。
- **pc 物化的时机**:T2 时 h_call 入口写 `dot 帧的 savedPC = pc_of_CALL`——所以若 helper 内部出错,traceback 能正确显示「dot 在第几行调用 helper」(dot 帧的 savedPC 已物化为 CALL helper 那条指令的 pc,§4.5)。
- **返回值全经共见栈槽**:helper 返回 6(T3 回填 helperBase-1)、dot 返回 10(T4 回填 dotBase-1)——全部经共见 linear memory 栈槽,无序列化、无拷贝([03-memory-model](./03-memory-model.md) §2)。dot 的 Wasm 代码在 T4 读 helper 的返回值时,直接 `i64.load` 那个共见栈槽。

---

## 3. gibbous → crescent / host(imported 调度助手)

本节是「慢路径出口」主线(§0.2)的单一事实源——gibbous 代码遇到需要 Go 介入的点(调用、算术慢路径、IC miss、分配、回边 GC)时,如何经 imported 助手回 Go。核心是 §3.1 的 `$h_call` 三向分派(gibbous/crescent/host)与 §3.3 的完整 helper 清单。一切 helper 都遵守「接受 base + pc 立即数,自取一切」——这是 §0.1「跨层只传 base」原则在出口方向的兑现(入口方向是 §2.1)。

### 3.1 imported 助手 `$h_call` 三向分派

gibbous 代码遇到 CALL opcode 时([02-translation](./02-translation.md) §3.6),它**自己不实现调用逻辑**——调用一个 imported Go 函数 `$h_call`,由 Go 侧按被调者的类型三向分派:

| 被调者 | 路径 | Go 栈变化 |
|---|---|---|
| **gibbous(已编译)** | 基线:Go 侧再 `fn.Call`(Wasm→Go→Wasm,两次跨层);优化:同 module 内 `call_indirect` 直调(§9 缺口 + [02-translation](./02-translation.md) §1.2 批量编译时,免 Go 往返) | +1 帧(基线) |
| **crescent(未编译/TierStuck)** | `vm.execute` 跑该帧([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.3 fresh reentry),返回后回 Wasm | +1 execute |
| **host fn** | `callHost`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.6)原样 | 同解释器 |

ASCII 跨层流程图(三向分派全景):

```
                    gibbous 代码(Wasm)执行到 CALL opcode
                              │
                              │  call $h_call(callBase, pc)   ← imported,跨层回 Go
                              ▼
        ┌──────────────────── Go 侧 h_call callback(§3.2)────────────────────┐
        │  callee := stk[callBase-1]      // 共见栈槽自取被调对象            │
        │  写 ci.savedPC = pc             // pc 物化(§4.5,[02] §4)         │
        │  switch 被调者类型:                                                │
        └───────┬──────────────────┬──────────────────────┬──────────────────┘
                │                  │                       │
     ① gibbous │       ② crescent│            ③ host fn  │
       (已编译) │       (未编译)   │                       │
                ▼                  ▼                       ▼
   ┌────────────────┐  ┌──────────────────┐  ┌───────────────────────┐
   │ 基线:           │  │ vm.execute(th)    │  │ callHost(05 §7.6)     │
   │  fn.Call(ctx,    │  │  压 fresh CallInfo │  │  Go 同步调 hostFn      │
   │   calleeBase)    │  │  (05 §7.3)        │  │  (+CallInfo host 哨兵) │
   │  (Wasm→Go→Wasm)  │  │  跑该帧 crescent   │  │                       │
   │ 优化:            │  │  返回后回 Wasm     │  │                       │
   │  call_indirect   │  │                   │  │                       │
   │  同 module 直调   │  │                   │  │                       │
   │  (免 Go 往返,§9)  │  │                   │  │                       │
   └───────┬─────────┘  └────────┬──────────┘  └──────────┬────────────┘
           │                     │                        │
           └─────────────────────┴────────────────────────┘
                              │
                   被调返回 status(0=OK / 1=ERR)
                              │
                              ▼
              h_call 返回 status i32 给 Wasm 侧
                              │
              ┌───────────────┴───────────────┐
            status==0                       status!=0
              │                                │
              ▼                                ▼
      Wasm 继续执行下条指令         Wasm 函数清理后自身 return 非 0
      (返回值已回填 R(A..))         (status 链冒泡,§4.1)
```

**三向分派的统一出口**:无论被调者是哪类,`h_call` 都返回一个 status i32(0=OK / 1=ERR)给 Wasm 侧。Wasm 侧据此决定继续执行(OK,返回值已在共见栈槽)或自身 return 非 0(ERR,status 链冒泡,§4.1)。这让 gibbous 代码对「被调者是什么类型」无感——它只看 `h_call` 的 status 返回值,与解释器 doCall 看 callResult 同构。

CALL opcode 在 Wasm 侧的翻译产物形态(WAT 风格,完整翻译表归 [02-translation](./02-translation.md) §3.6,此处只示协议接口):

```wat
;; 源:CALL A B C(调 R(A),参数 R(A+1..A+B-1),返回回填 R(A..A+C-2))
;; 翻译产物:计算被调帧 base 字节偏移,call $h_call,检 status 决定继续或冒泡
;; (常量 A/B/C 编译期已知,callBase / pc 都是编译期立即数)
  ;; callBase = $base + 8*(A+1)   ── 被调帧 R0 字节偏移(funcIdx = callBase-8)
  (local.set $callBase
    (i32.add (local.get $base) (i32.const <8*(A+1)>)))
  ;; call $h_call(callBase, pc_of_this_CALL):三向分派回 Go(§3.2)
  (local.set $status
    (call $h_call (local.get $callBase) (i32.const <pc_of_this_CALL>)))
  ;; status≠0 ⇒ 自身清理后 return status(status 链冒泡,§4.1)
  (if (i32.ne (local.get $status) (i32.const 0))
    (then
      ;; 基线 memory-resident:无 locals 缓存需写回,清理是空操作(§4.4)
      (return (local.get $status))))
  ;; status==0 ⇒ 返回值已在 R(A..) 共见栈槽,继续执行下条指令
  ;; (无需搬运:h_call 内被调者的 RETURN 已按 nresults 回填 funcIdx 位起)
```

要点:**Wasm 侧的 CALL 翻译产物极薄**——它只算被调帧 base、调 `$h_call`、检 status。所有「被调者是什么、怎么调、返回值怎么回填」的逻辑全在 Go 侧 `h_call`(§3.2)——这是「gibbous 代码自身从不实现调用逻辑」的兑现,使翻译器对 CALL 的翻译与「被调者的 tier 状态」解耦(被调者升没升 gibbous,Wasm 侧的 CALL 翻译产物完全一样,差异全在运行期 `h_call` 分派)。

### 3.2 三向调度的 Go 实代码骨架(`h_call` callback 实装)

```go
// internal/gibbous/wasm/helpers.go —— h_call imported 助手的 Go 实装
//
// 这是 gibbous → {gibbous, crescent, host} 三向分派的中枢。
// 由 wazero 在 module 实例化时注册为 imported 函数(§3.4)。
//
// 入参(经 wazero,从 Wasm 侧传来):
//   - callBase:被调帧 R0 在 valueStack 的字节偏移(被调 closure 在 callBase-8)
//   - pc:发出 CALL 的指令 pc(编译期立即数,pc 物化用,§4.5)
// 返回:status i32(0=OK / 1=ERR)
func (t *Trampoline) hCall(ctx context.Context, st *State, callBase, pc uint32) uint32 {
    th := st.runningThread
    // pc 物化:写回当前 gibbous 帧的 savedPC,供 traceback 定位(§4.5,[02] §4)
    th.curCI().savedPC = int32(pc)

    calleeSlot := int(callBase/8) - 1        // funcIdx = base-1(字索引)
    callee := th.stk[calleeSlot]

    switch {
    case isLuaClosure(callee):
        cl := value.GCRefOf(callee)
        proto := st.protos[protoIDOf(cl)]
        switch {
        case proto.tierState == TierGibbous && th == st.mainThread:
            // ── ① gibbous(已编译,主线程)──
            // 基线:Go 侧再进 wazero(Wasm→Go→Wasm,两次跨层)。
            //   压 CallInfo 标 bit50(与 §2.2 step2 同款,经 enterGibbous 的内部路径)。
            // 优化(§9 缺口):若被调与调用者在同一批量 module,Wasm 侧直接
            //   call_indirect,不回 Go,本 callback 不被调用——故此分支只覆盖
            //   「跨 module 的 gibbous→gibbous」。
            ci := pushCallInfoFor(th, cl, callBase, /*nresults 从 CALL 解码*/)
            ci.gibbous = true
            code := t.codeOf(proto)
            return code.Run(st, int32(callBase))   // 一次再跨层
        default:
            // ── ② crescent(未编译 / TierStuck / 协程线程)──
            // vm.execute 跑该帧(05 §7.3 fresh reentry):压 fresh CallInfo,
            //   起一个新的 execute 循环跑这一帧,返回后控制权回到本 callback,
            //   再 return 回 Wasm。
            //   注意:协程线程上即便 callee 是 TierGibbous,也走这里跑 crescent
            //   (§5 线程级 tier;但实际上协程线程的 doCall 入口就不会进 gibbous,
            //    所以这里的 default 主要覆盖「未编译 + TierStuck」)。
            return st.executeFrameForGibbous(th, cl, callBase)  // 内部压 fresh + execute
        }

    case isHostClosure(callee):
        // ── ③ host fn ──
        // callHost(05 §7.6)原样:压 host 哨兵 CallInfo,Go 同步调 hostFn,
        //   返回值回填 R(A..)。host 内部若回调 Lua 仍走 05 §7.3 重入。
        return st.callHostForGibbous(th, calleeSlot, callBase)

    default:
        // R(A) 非可调用 → __call 元方法(05 doCall 的 callMeta);
        //   有则插入参数前重试,无则构造 *LuaError 设 pendingErr 返 1。
        return st.callMetaForGibbous(th, calleeSlot, callBase)
    }
}
```

要点:

- **三个分支映射 §3.1 的三行表**:gibbous(已编译)→ 再 `fn.Call`;crescent(未编译)→ `execute` fresh reentry;host → `callHost`。第四个 default 分支处理 `__call` 元方法(与解释器 doCall 的 callMeta 分支同构,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.1)。
- **crescent fresh reentry 是关键复用**:gibbous 调一个未编译的 Proto 时,**经 trampoline 出去由 crescent 跑**——这正是 [00-overview](./00-overview.md) §3 列的「反向依赖:P3 必须能把控制权交回解释器跑未编译 Proto」。它用的就是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.3 的 fresh reentry 机制(压 fresh CallInfo + 起新 execute),与「host 回调 Lua」走同一条路。这是 PW6 验收的核心场景:「gibbous 内调未编译 Proto 经 trampoline 出去由 crescent 跑」([00-overview](./00-overview.md) §4 PW6)。
- **pc 物化在 callback 入口统一做**:`th.curCI().savedPC = int32(pc)`——直线代码无运行期 pc,每个调用点把编译期已知 pc 作为立即数传给助手,助手写回 CallInfo.savedPC,使 traceback 逐字节一致([02-translation](./02-translation.md) §4)。
- **statusOK 时返回值已在共见栈槽**:被调返回后,返回值在 `R(A..)`(funcIdx 位起),Wasm 侧继续执行即可读到——无需 callback 额外搬运(共见内存,[03-memory-model](./03-memory-model.md) §2)。

### 3.3 多种 helper 列举

`h_call` 是最核心的助手,但 gibbous 代码的所有「慢路径」与「需要 Go 介入的点」都走 imported 助手。完整清单:

| helper | 触发点 | 职责 | 对应 opcode / 章节 |
|---|---|---|---|
| **`h_call`** | CALL / TAILCALL | 三向分派(§3.1) | [02-translation](./02-translation.md) §3.6 |
| **`h_arith`** | 算术 opcode 慢路径 | 双 number 快路径(Wasm 内直发 f64,[02-translation](./02-translation.md) §3.2)失败 → 走元方法(`__add`/`__sub`/.../字符串强转 number) | [02-translation](./02-translation.md) §3.2 + [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §4 |
| **`h_gettable`** | GETTABLE / SETTABLE / SELF 的 IC miss | IC 快照命中(同表同代次,[06-ic-feedback-consume](./06-ic-feedback-consume.md) §1)直发;miss → 走完整哈希查找 + `__index`/`__newindex` 元方法链 | [02-translation](./02-translation.md) §3.4 + [06-ic-feedback-consume](./06-ic-feedback-consume.md) §3 |
| **`h_safepoint`** | 回边(FORLOOP/JMP 回跳) | gcPending 检查 + 触发 GC([05-safepoint-gc](./05-safepoint-gc.md) §3);几乎恒不跳的分支 | [02-translation](./02-translation.md) §3.5 + [05-safepoint-gc](./05-safepoint-gc.md) §3 |
| **`h_alloc`** | NEWTABLE / CLOSURE / CONCAT / SETLIST(可能 rehash) | 分配对象(table/closure/string)——gibbous 代码自身从不分配([05-safepoint-gc](./05-safepoint-gc.md) §1),分配与 GC 都在助手内同步发生 | [02-translation](./02-translation.md) §3.4/§3.7 + [05-safepoint-gc](./05-safepoint-gc.md) §1 |
| **`h_raise`** | 解释器内在错误点(对 nil 算术、table index is nil/NaN 等翻译产物检出错误) | 构造 `*LuaError` 设 `state.pendingErr`,返回非 0(让 Wasm 帧 status 链冒泡,§4) | [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.4 |
| **`h_concat`** | CONCAT(多值字符串拼接) | 拼接 + `__concat` 元方法;分配新字符串(经 h_alloc 语义) | [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §4 |

**统一形态**:所有 helper 都遵守「接受 base + pc 立即数,自取一切」(§8 不变式 4)——它们从共见栈槽读操作数、从 pc 立即数物化错误位置、返回 status i32。helper **可重入 wazero**(`h_call` 的 gibbous 分支再 `fn.Call`,形成 gibbous→helper→gibbous 链,§8 不变式 5)。

两个代表性 helper 的 Go 骨架(展示「快路径在 Wasm 侧、慢路径才回 Go」的分工):

```go
// internal/gibbous/wasm/helpers.go —— h_arith 算术慢路径助手
//
// 触发:Wasm 侧算术 opcode 的双 number 快路径([02-translation] §3.2)失败时
//   (操作数有一个非 number),才 call $h_arith 回 Go 走元方法链。
// 入参:op(算术种类 ADD/SUB/.../UNM),base(本帧 R0 字节偏移),pc(物化用)。
//   被操作的寄存器号(A/B/C)编译期已知,经 op 高位或额外立即数编入(此处简化为
//   helper 内按 op 解出固定寄存器布局;实际由 [02] §3.2 的翻译形态定)。
func (t *Trampoline) hArith(ctx context.Context, st *State, op, base, pc uint32) uint32 {
    th := st.runningThread
    th.curCI().savedPC = int32(pc)   // pc 物化(§4.5)
    // 解操作数(从共见栈槽自取,寄存器布局编译期已编入 op)
    b, c := decodeArithRegs(op)
    lhs := th.stk[int(base/8)+b]
    rhs := th.stk[int(base/8)+c]
    // 走解释器同款元方法链(05 §4 arith helper):字符串强转 number / __add 等。
    //   注意:这里复用 crescent 的 arith helper,不另写一份——保证逐字节同构。
    res, e := st.arithMeta(opKind(op), lhs, rhs)
    if e != nil {
        st.pendingErr = e            // 元方法也失败 → 构造 *LuaError
        return 1                     // status=ERR,status 链冒泡(§4.1)
    }
    th.stk[int(base/8)+decodeArithDst(op)] = res  // 回填结果到共见栈槽
    return 0                         // status=OK,Wasm 继续
}

// internal/gibbous/wasm/helpers.go —— h_safepoint 回边 GC 检查助手
//
// 触发:FORLOOP/JMP 回跳([02-translation] §3.5)处,周期性回 Go 检查 GC。
//   Wasm 侧先 i32.load gcPending 标志,非 0 才 call $h_safepoint(几乎恒不跳)。
// 入参:pc(物化 + context 取消检查用)。
func (t *Trampoline) hSafepoint(ctx context.Context, st *State, pc uint32) uint32 {
    th := st.runningThread
    th.curCI().savedPC = int32(pc)
    // context 取消检查(§6.2):回边是周期性回 Go 的点,在此响应取消。
    if st.ctxErr != nil {
        if err := st.ctxErr(); err != nil {
            st.pendingErr = &LuaError{Msg: err.Error()}
            return 1
        }
    }
    // GC 触发(05 §5.3 opcode 末尾检查 + 06 §8.2 同步 collect):
    //   locals 缓存写回纪律(若启用 §2 优化)在 Wasm 侧 call 此 helper 前已完成
    //   ([05-safepoint-gc] §4),所以根此刻全在共见栈槽可见。
    if st.gc.Pending() {
        st.gc.Collect()              // 同步 STW full GC(P3 不动 GC 形态,[05] §6.4)
    }
    return 0
}
```

要点(两个骨架共同体现的纪律):

- **快路径在 Wasm 侧,慢路径才回 Go**:`h_arith` 只在双 number 快路径失败时才被调(Wasm 侧先尝试 f64 直算,[02-translation](./02-translation.md) §3.2);`h_safepoint` 只在 gcPending 标志非 0 时才被调(Wasm 侧先 `i32.load` 标志)。这是「helper 是慢路径出口」的兑现——热路径(双 number 算术、无 GC 的回边)根本不跨层。
- **慢路径复用 crescent helper**:`h_arith` 调 `st.arithMeta`(crescent 的 arith 元方法 helper,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §4)——**不另写一份元方法逻辑**,保证 gibbous 慢路径与 crescent 逐字节同构。这是「正确性优先」的工程手法:慢路径不是性能瓶颈(罕见),复用 crescent 实现既省代码又保证一致。
- **`h_safepoint` 是 context 取消 + GC 的双职责点**:回边是 gibbous 代码周期性回 Go 的唯一点,所以 context 取消检查(§6.2)与 GC 触发([05-safepoint-gc](./05-safepoint-gc.md) §3)都在这里顺带做——与解释器主循环的 opcode 末尾 safepoint([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §5)同位。

**helper 数量增长的拆分问题**:当前列了 7 个 helper,随 PW 推进可能增加(如 LEN 的 `h_len`、TEST 系列的辅助、RETURN 可变返回的 `h_return` §4.7)。「per-helper Go 函数 vs 单一 dispatcher」(一个 `h_dispatch(opcode, base, pc)` 统一入口,内部 switch)的取舍留 PW6 实测后定(§9)——单一 dispatcher 减少 imported 函数数量(每个 imported 函数有注册成本),但每次调用多一次 switch;per-helper 直达但 imported 表膨胀。

### 3.4 helper imported declaration 与 Go callback registration

helper 在 Wasm module 里声明为 imported 函数,在 Go 侧注册为 host module 的导出函数:

```wat
;; Wasm module 侧:声明 imported 助手(WAT 风格)
(import "wangshu" "h_call"      (func $h_call      (param i32 i32) (result i32)))  ;; callBase, pc → status
(import "wangshu" "h_arith"     (func $h_arith     (param i32 i32 i32) (result i32)))  ;; op, base, pc → status
(import "wangshu" "h_gettable"  (func $h_gettable  (param i32 i32 i32) (result i32)))  ;; mode, base, pc → status
(import "wangshu" "h_safepoint" (func $h_safepoint (param i32) (result i32)))  ;; pc → status
(import "wangshu" "h_alloc"     (func $h_alloc     (param i32 i32 i32) (result i32)))  ;; kind, base, pc → status
(import "wangshu" "h_raise"     (func $h_raise     (param i32 i32) (result i32)))  ;; errKind, pc → status(恒 1)
(import "wangshu" "h_concat"    (func $h_concat    (param i32 i32 i32) (result i32)))  ;; a, b_count, pc → status
```

```go
// internal/gibbous/wasm/helpers.go —— Go 侧 host module 注册(wazero,待 spike 验证 API)
//
// 每 State 一份 host module(因为 helper callback 闭包捕获了 *State,§6.3 多 State 各一份)。
func (t *Trampoline) buildHostModule(ctx context.Context, rt wazero.Runtime, st *State) (api.Module, error) {
    return rt.NewHostModuleBuilder("wangshu").
        // h_call:三向分派(§3.2)
        NewFunctionBuilder().WithFunc(func(ctx context.Context, callBase, pc uint32) uint32 {
            return t.hCall(ctx, st, callBase, pc)
        }).Export("h_call").
        // h_arith:算术慢路径
        NewFunctionBuilder().WithFunc(func(ctx context.Context, op, base, pc uint32) uint32 {
            return t.hArith(ctx, st, op, base, pc)
        }).Export("h_arith").
        // h_gettable:IC miss 慢路径
        NewFunctionBuilder().WithFunc(func(ctx context.Context, mode, base, pc uint32) uint32 {
            return t.hGettable(ctx, st, mode, base, pc)
        }).Export("h_gettable").
        // h_safepoint:回边 GC 检查
        NewFunctionBuilder().WithFunc(func(ctx context.Context, pc uint32) uint32 {
            return t.hSafepoint(ctx, st, pc)
        }).Export("h_safepoint").
        // h_alloc:对象分配(NEWTABLE/CLOSURE/...)
        NewFunctionBuilder().WithFunc(func(ctx context.Context, kind, base, pc uint32) uint32 {
            return t.hAlloc(ctx, st, kind, base, pc)
        }).Export("h_alloc").
        // h_raise:错误构造(恒返 1)
        NewFunctionBuilder().WithFunc(func(ctx context.Context, errKind, pc uint32) uint32 {
            return t.hRaise(ctx, st, errKind, pc)
        }).Export("h_raise").
        // h_concat:字符串拼接
        NewFunctionBuilder().WithFunc(func(ctx context.Context, a, bCount, pc uint32) uint32 {
            return t.hConcat(ctx, st, a, bCount, pc)
        }).Export("h_concat").
        Instantiate(ctx)
}
```

要点:

- **`*State` 经闭包捕获,不经参数**:每个 helper callback 捕获 `st *State`(以及 `t *Trampoline`),所以 host module 是 per-State 构造的(§6.3)——多 State 各自的 helper callback 闭包捕获各自的 State,无锁、无共享可变状态。
- **callback 签名是「i32 参数 + i32 返回」**:全部 helper 的参数与返回都是 i32(整数立即数:opcode/mode/kind/base/pc,返回 status)——与 §2.1 的入口签名同款最廉价形态,使 helper 跨层成本最小([01-spike-gate](./01-spike-gate.md) §1 的 S3 样本测的就是「Wasm 调 imported Go 函数」的成本)。
- **`context.Context` 经 wazero ctx 第一个参数透传**:每个 callback 的第一参数是 `ctx context.Context`(wazero 调用时传入,§6.2)——这是 P3 接通 `SetCancelHook`([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) 相关,context 取消钩子)的通道(§6.2)。
- **imported 数量与 §3.3 拆分问题**:这里列 7 个 imported 函数;若 PW6 实测发现 imported 注册/调用成本随数量线性增长,改成单一 `h_dispatch` dispatcher(§9 缺口)。

---

## 4. 错误传播:status 链

本节是「错误冒泡通道」主线(§0.2)的单一事实源——gibbous 帧内出错时,错误信号如何单向冒泡到 protected 边界。核心命题:status 链与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9 的「显式错误返回」结构同构(§4.2),错误可任意穿越 gibbous 帧因为冒泡是单向放弃(§4.4),清理责任在 pcall 边界一次性收口(§4.3)。这与 §5 的「yield 不可穿越」构成对偶:错误单向放弃可穿,yield 双向复原不可穿。

### 4.1 `$h_call` 返回非 0 ⇒ Wasm 函数清理后自身返回非 0 ⇒ 上层继续冒泡

错误传播机制是 **status 链**——一个布尔信号(status≠0)沿调用链单向向上传递:

```
status 链冒泡(ASCII,以 gibbous→gibbous→crescent 三层调用链为例):
                                                          
  外层 gibbous 帧 A(Wasm)                                 
    │  调 B:call $h_call → status_B                       
    │                                                      
    ▼  内层 gibbous 帧 B(Wasm)                            
       │  调 C:call $h_call → status_C                     
       │                                                   
       ▼  crescent 帧 C(解释器,未编译 Proto)             
          │  执行中出错:helper 构造 *LuaError 设 pendingErr 
          │  vm.execute 返回 *LuaError(05 §9.2)            
          │                                                
          ▼  h_call(for C)见 execute 返回 err              
             │  设 state.pendingErr,返回 status=1 给帧 B   
             │                                             
       ┌─────┘                                             
       ▼  帧 B 收到 h_call 返回 status_C=1                 
          │  ★ 帧 B 清理本帧(若有需 Wasm 侧清理的局部),
          │    自身 return 1(不继续执行后续指令)          
          │                                                
    ┌─────┘                                                
    ▼  帧 A 收到 h_call 返回 status_B=1                    
       │  ★ 帧 A 同样清理 + 自身 return 1                  
       │                                                   
  ┌────┘                                                   
  ▼  crescent doCall(enterGibbous,§2.2)收到 status=1     
     弹帧 A 的 CallInfo,vm.throwPending(05 §9.2)           
     一路 return *LuaError 直到 protected 边界(§4.3)       
```

机制要点:

- **`$h_call` 返回非 0**:当被调者(任一类型)出错时,`h_call` callback 设 `state.pendingErr` 并返回 status=1 给 Wasm 侧。
- **Wasm 函数清理后自身返回非 0**:Wasm 侧收到 `h_call` 的非 0 返回,**不继续执行后续指令**,而是跳到函数出口、return 自身的 status=1。「清理」在 P3 基线下通常是空操作(基线 memory-resident,无 Wasm locals 缓存需要落回,§4.4);若启用 locals 缓存优化([02-translation](./02-translation.md) §2 + [05-safepoint-gc](./05-safepoint-gc.md) §4),清理 = 把脏的 locals 写回栈槽(但出错路径下值已无意义,清理主要是为根可见性,且错误冒泡是单向放弃,§4.4)。
- **上层继续冒泡**:外层帧(crescent 或外层 Wasm)收到非 0 status,重复同一动作——单向向上,直到 crescent 的 doCall(enterGibbous)收到 status,转 `vm.throwPending` 走 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9 错误冒泡。

### 4.2 与 P1 05 §9 显式错误返回同构

status 链与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9 的「显式错误返回」**结构同构**:

| 维度 | P1 05 §9(crescent) | P3 04 §4(gibbous) |
|---|---|---|
| 错误信号 | helper 返回非 nil `*LuaError`,主循环 `return e` | `h_call`/`h_raise` 返回 status≠0,Wasm 函数 `return status` |
| 传播方式 | 一路 return 出 execute(显式返回,非 panic) | status 链单向向上(显式返回,非 trap) |
| 停靠站 | protected 边界(pcall,标 callStatus_fresh,§7.3) | 同一 protected 边界(§4.3) |
| 中间帧清理 | 出错帧只 `return e`,清理责任在边界(§9.3) | 出错 Wasm 帧只 `return status`,清理责任在边界(§4.3) |
| 为何不用异常机制 | panic/recover 与 reentry 模型冲突 + 慢([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.1) | Wasm trap 同理:trap 一路 unwind wazero 栈,跳过中间 CallInfo 清理 |

**为什么 gibbous 不用 Wasm trap 做错误传播**(与 P1 不用 panic 同理):

- Wasm 有 `unreachable`/trap 机制(类似异常),但**trap 会一路 unwind wazero 的执行栈**——这与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.1 拒绝 panic 的理由完全平行:Lua 调用链不在 wazero 栈上(在 arena 的 CallInfo),trap unwind 只 unwind 到最外层 `fn.Call`,跳过中间所有 gibbous/crescent 帧的 CallInfo 清理。要在 Go 侧 recover trap 后手工重建 CallInfo 状态,比显式 status 返回更复杂。
- **status 返回是「错误路径让步、热路径优先」**:正常脚本极少出错,status≠0 的分支几乎恒不跳,分支预测器轻松命中,接近零成本([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.5)。与解释器同一性能哲学。

**这是 [00-overview](./00-overview.md) §9 不变式 3「CallInfo 唯一真相」与不变式 4「错误可穿越」在错误路径的兑现**:错误信号是布尔 status,但调用链状态(谁该被清理到哪)永远从 CallInfo 读,不从 Wasm/Go 栈读。

### 4.3 protected 边界(pcall)清理责任不变

**纪律(承 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.3)**:protected 边界(pcall / xpcall)的清理责任在 P3 下**完全不变**——错误穿越多少 gibbous 帧都不影响。

```
错误冒泡到 pcall 边界的清理(05 §9.3 原样,gibbous 帧透明):
  pcall host 实现:
    1. 记下当前 ciTop(保护点 savedCiTop)与栈 top。
    2. vm.callLuaFromHost(f, args):压 fresh CallInfo,起 execute 跑被保护函数。
         ★ 被保护函数内部可能调用了升 gibbous 的 Proto(经 §2.2 enterGibbous),
           那些 gibbous 帧也压了 CallInfo(标 bit50)。
    3. execute 返回 *LuaError:
         a. ciTop 回退到 savedCiTop —— 丢弃出错帧及其上所有帧的 CallInfo,
            ★ 包括中间的 gibbous 帧 CallInfo(bit50=1 的帧一并丢弃,无特殊处理)。
         b. 关闭这些被丢弃帧的开放 upvalue(closeUpvals 到保护点 base)。
         c. 恢复栈 top 到保护点。
         d. 返回 (false, errLua.value)。
```

要点:

- **gibbous 帧对 pcall 透明**:pcall 边界回退 ciTop 时,**不区分被丢弃的帧是 crescent 帧还是 gibbous 帧**——它们的 CallInfo 形态相同(同一种结构、同一块 arena,§0.1),pcall 一律按「丢弃 ≥ savedCiTop 的所有 CallInfo」处理。bit50=1 的帧与 bit50=0 的帧在这里没有区别。
- **清理责任在边界,不在每个出错的 gibbous 帧**:与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.3 一致——出错的 gibbous 帧只管 `return status` 冒泡,**省掉每帧的清理逻辑**。这正是 status 链(类比显式返回)相对 trap(类比 panic)的关键简化:trap 要在每个 gibbous 帧 unwind 时清理,status 链让边界一次性清理。
- **upvalue 关闭也在边界统一做**:gibbous 帧若构造了开放 upvalue(CLOSURE,[02-translation](./02-translation.md) §3.7),它们挂在 thread 的开放 upvalue 链上([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §8.3)——pcall 边界 `closeUpvals` 时一并关闭,与 crescent 构造的开放 upvalue 无区别。

### 4.4 错误可任意穿越 gibbous 帧:冒泡是单向放弃,无需复原 Wasm 帧

**核心物理事实([00-overview](./00-overview.md) §9 不变式 4 上半)**:错误可以任意穿越 gibbous 帧,因为冒泡是**单向放弃**——错误一旦发生,这一帧及其上所有帧都被丢弃,**无需复原任何 Wasm 帧的执行状态**。

对比 yield(§5,不可穿越):

| 维度 | 错误冒泡(可穿越) | yield(不可穿越,§5) |
|---|---|---|
| 方向 | **单向放弃**:帧被丢弃,永不回来 | **双向**:挂起后要 resume 复原继续跑 |
| Wasm 帧需求 | 无需复原——丢弃即可 | 需挂起后从中断点复原——core Wasm 做不到 |
| 物理可行性 | ✅ Wasm 函数正常 return(status≠0)即放弃自身 | ❌ core Wasm 无 continuation,无法挂起后复原 |

为什么单向放弃使 Wasm 帧无需复原:

- **错误发生时,这一帧的局部值已无意义**:无论 Wasm locals 缓存了什么、栈槽里是什么中间值,错误冒泡都意味着「这次计算失败、整帧作废」——没有任何后续代码会读这一帧的局部,所以不需要把它们恢复到某个一致状态。Wasm 函数只需 return status≠0,把控制权交给上层,自身的栈帧由 wazero 在 return 时自动销毁。
- **clean-up 是空操作(基线)或仅为根可见性(优化态)**:基线 memory-resident 下,gibbous 帧无 Wasm locals 缓存,清理是纯空操作(直接 return status)。启用 locals 缓存优化时,错误路径下的「写回栈槽」也只是为了 GC 根可见性(若错误冒泡途中触发 GC),但因为这些帧即将被 pcall 边界整体丢弃,写回的值不会被任何后续代码读——实践上错误路径可不写回(值作废),GC 在错误冒泡完成后(pcall 已清理)再触发即可。
- **这与 host 帧错误穿越同理**:host 函数出错时(`vm.raise`),host 的 Go 栈帧也是 return 出去就放弃,不需要复原([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.4)。gibbous 帧与 host 帧在「错误单向穿越」这点上同构。

### 4.5 traceback 经 imported 助手取本帧 pc

错误发生时构造 traceback 需要「每一帧的当前 pc」——但 gibbous 直线代码无运行期 pc 变量([02-translation](./02-translation.md) §4 pc 物化协议)。机制:

```
gibbous 帧的 pc 物化(traceback 用,[02-translation] §4):
  - 直线代码无运行期 pc(Wasm 不维护 Lua pc)。
  - 每个可能出错/调用/safepoint 的点,把编译期已知 pc 作为立即数传给助手。
  - 助手(h_call/h_arith/h_gettable/h_raise/...)入口统一写:
        th.curCI().savedPC = int32(pc)
  - 所以错误发生那一刻,当前 gibbous 帧的 CallInfo.savedPC 已是「出错指令的 pc」。
  - traceback 遍历 CallInfo 链时,gibbous 帧的 savedPC 与 crescent 帧的 savedPC
    取法一致(都从 CallInfo 读)——traceback 逐字节一致([08-testing-strategy] §2)。
```

要点:

- **pc 物化是「调用 helper 时顺带做」**:§3.2 的 `hCall` 入口第一行就是 `th.curCI().savedPC = int32(pc)`——所有 helper 都在入口写 savedPC,把编译期立即数物化进 CallInfo。这意味着「凡是会出错的点必然先调了某个 helper(h_arith/h_gettable/h_call/h_raise)」,而 helper 入口已写好 savedPC,所以出错时 savedPC 必然是正确的出错 pc。
- **traceback 不为 gibbous 帧开豁免**:[08-testing-strategy](./08-testing-strategy.md) §2 的差分口径要求 gibbous 与 crescent 的 traceback 逐字节一致。gibbous 帧的 savedPC 经 pc 物化已正确,traceback 遍历 CallInfo 链时对 gibbous 帧(bit50=1)与 crescent 帧(bit50=0)用同一套「读 savedPC → 映射回源码行」逻辑——**bit50 在 traceback 里不触发任何特殊显示**(虽然 §1.1 提到 bit50 理论上可用于区分帧类型,但差分口径下不这样用,以保证逐字节一致)。
- **链 [02-translation](./02-translation.md) §4**:pc 物化的完整协议(哪些点物化、立即数如何编入翻译产物)归 02;本文只用「helper 入口写 savedPC」这一兑现点。

### 4.6 错误穿越 gibbous 时的 CallInfo 链清理:gibbous 帧由 trampoline 弹出

错误穿越 gibbous 帧时,CallInfo 链的清理分两个层级,**与 doReturn 类似流程**:

```
错误穿越时的 CallInfo 弹出分工:
  ① 最外层 gibbous 帧(被 crescent enterGibbous 直接调的那个):
       由 trampoline 弹出 —— §2.2 step3 的 ERR 分支:
         case statusERR: popCallInfo(st.runningThread); return vm.throwPending(f)
       这一帧的 CallInfo 由 enterGibbous 压、由 enterGibbous 的 ERR 分支弹,对称。
  ② 中间的 gibbous/crescent 帧(被 h_call 链调进去的):
       - gibbous→gibbous(经 h_call 再 fn.Call):内层 gibbous 帧的 CallInfo 由
         h_call 的 ① 分支(§3.2)压、由其 return 路径弹(status≠0 时弹后返回)。
       - gibbous→crescent(经 h_call 起 execute):crescent 帧的 CallInfo 由
         execute 自己管(fresh 帧 + 内部 doReturn/doCall 链);execute 返回 err 时
         其内部帧已按 05 §9 处理(未保护则随 execute return 放弃)。
  ③ 最终到达 pcall 边界:边界 ciTop 回退一次性丢弃所有残余(§4.3)。
```

要点:

- **gibbous 帧由 trampoline 弹出,对称于压入**:谁压的 CallInfo 谁弹——enterGibbous 压的(§2.2 step2),由 enterGibbous 的 status 分支弹(OK 与 ERR 都弹,§2.3);h_call 的 gibbous 分支压的,由 h_call 的 return 路径弹。这与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.2 doReturn 的「正常返回弹 CallInfo」是类似流程——只是错误路径下弹完即继续冒泡(不 moveResults 返回值)。
- **「类似 doReturn」的差异**:doReturn(正常返回)要 `moveResults`(搬返回值)+ `closeUpvals`(关闭本帧 upvalue);错误路径弹 CallInfo 时**不搬返回值**(无返回值,是错误),**upvalue 关闭延到 pcall 边界统一做**(§4.3,因为错误冒泡是单向放弃,中间帧的 upvalue 由边界 closeUpvals 一次性关)。所以错误路径的弹帧比 doReturn 更轻(只 popCallInfo)。
- **三层级的统一收口在 pcall 边界**:无论错误穿越了几个 gibbous 帧、几个 crescent 帧,最终所有残余 CallInfo 由最近的 pcall 边界 ciTop 回退一次性清理(§4.3)——这是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9.3「清理责任在边界」的兑现,gibbous 帧的存在不改变这个收口点。

### 4.7 正常返回:RETURN 翻译产物按 nresults 回填 + 多值契约

与错误路径(status≠0,单向放弃)对偶,正常返回(status=0)的 RETURN 翻译产物负责「把返回值按调用者期望 nresults 回填到 funcIdx 位起」——与解释器 `doReturn` 的 `moveResults`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.2)逐字节同构。RETURN 在 Wasm 侧的翻译产物(WAT 风格,完整归 [02-translation](./02-translation.md) §3.6,此处只示协议接口):

```wat
;; 源:RETURN A B(返回 R(A..A+B-2),B=0 返回到 top)
;; 翻译产物:从 CallInfo 读 nresults,把 R(A..) 搬到 funcIdx 位起,push status=0
;; (A/nret 编译期已知;nresults 是运行期从调用者 CallInfo word2 读,因为同一个
;;  Proto 可能被不同调用点以不同 C 调用)
  ;; 多值返回需要运行期 nresults(调用者期望几个返回值)——直线代码下用 h_return
  ;; 助手完成 moveResults(它读 CallInfo.nresults 做多退少补),或在 nret/nresults
  ;; 都编译期已知的单值快路径直接 i64.store 回填:
  ;;
  ;; 快路径(B 固定 + 调用点 nresults 固定,编译期可知):直接搬
  (i64.store (i32.sub (local.get $base) (i32.const 8))    ;; funcIdx 字节地址 = base-8
             (i64.load (i32.add (local.get $base) (i32.const <8*A>))))  ;; R(A)
  ;; 慢路径(B=0 到 top,或 nresults=0xFFFF 可变):call $h_return 做完整 moveResults
  ;;   (call $h_return (local.get $base) (i32.const <A>) (i32.const <nret>))
  (return (i32.const 0))   ;; status=0 OK
```

多值返回契约的三个层级:

| 形态 | 编译期可知性 | 翻译形态 |
|---|---|---|
| 单值固定返回(`return x`,调用点 `C=2`) | nret=1、nresults=1 都编译期可知 | 直接 `i64.store` 回填一个槽,快路径 |
| 多值固定返回(`return x, y`,调用点 `C=3`) | nret=2、nresults=2 编译期可知 | 直接 `i64.store` 回填多个槽 + 补 nil 到 nresults |
| 可变返回(`return f()` 或 `return ...`,B=0;或调用点 C=0 期望可变) | nret 或 nresults 运行期才定(从 top / CallInfo 读) | `call $h_return` 助手做完整 moveResults(读 CallInfo.nresults 多退少补 + 更新 caller top) |

要点:

- **nresults 从调用者 CallInfo 读,不从被调 Proto 烧入**:同一个升 gibbous 的 Proto 可能被不同调用点以不同 `C`(期望返回数)调用——比如 `local a = dot(...)`(C=2,期望 1 个)与 `local a, b = dot(...)`(C=3,期望 2 个)。所以 RETURN 翻译产物不能把 nresults 烧成立即数,而是运行期从调用者 CallInfo word2 的 nresults 字段读(§1.1 word2 布局)。这与解释器 doReturn 读 `ci.nresults`([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7.2)同源。
- **可变返回链(B=0/C=0)走 `h_return` 助手**:多值传播链(`CALL B=0`/`RETURN B=0`/`SETLIST B=0` 消费前一指令的可变返回,[../p1-interpreter/02](../p1-interpreter/02-bytecode-isa.md) §9-4)在 gibbous 侧由 `h_return` 助手处理——它读运行期 top、做 moveResults、更新 caller top,与解释器 `moveResults` 的 `nresults=0xFFFF` 分支逐字节同构。`h_return` 是 §3.3 helper 清单的隐含成员(归 RETURN 翻译,本文不单列,但它与 h_alloc 同款经共见栈槽 + CallInfo 自取)。
- **返回值回填落点是共见栈槽**:无论快/慢路径,返回值都落到 `funcIdx`(=base-1)位起的共见栈槽——trampoline 弹 CallInfo 后(§2.3 statusOK),主循环看到的返回值已就位,与「调用了 crescent 函数返回」无可观察差异([03-memory-model](./03-memory-model.md) §2)。

---

## 5. yield 不穿越:链 07-coroutine-thread-rule

本节简短——详细论证 + 线程级 tier 规则全部链到 [07-coroutine-thread-rule](./07-coroutine-thread-rule.md)。

### 5.1 本节只承诺:gibbous 帧不可穿越 yield

**承诺**:gibbous 帧不可穿越 yield——这是物理限制 + 线程级 tier 规则共同保证「该情形不会发生」。

物理限制(摘要,完整论证见 [07-coroutine-thread-rule](./07-coroutine-thread-rule.md)):

- [../p1-interpreter/08](../p1-interpreter/08-coroutines.md) 路线 B 下,yield 信号靠 return 冒泡且**之后要 resume 复原**(双向,§4.4 对比表)。
- 错误穿 Wasm 帧可以(单向放弃,§4.4),**yield 不行**——core Wasm 无 continuation,Wasm 帧无法挂起后从中断点复原。
- 这与「yield 不能跨 host(C)边界」是同一物理限制([../p1-interpreter/08](../p1-interpreter/08-coroutines.md) §5)——host 帧是真 Go 栈帧、gibbous 帧是 wazero 栈帧,两者都「不能挂起后复原」。

### 5.2 线程级 tier 规则使该情形不会发生(链 07)

若放任,会出现「解释执行能 yield、升层后同代码报错」的语义分裂——差分必炸。**P3 定稿:线程级 tier 规则**——

> 只有主线程的执行进入 gibbous;协程线程上调用一律走 crescent(doCall 的 gibbous 分支多查一个 `th == mainThread`,见 §2.2 / §3.2 的 `th == st.mainThread` 守卫)。

由此:

- 协程内代码永不升层(在 crescent 跑,可正常 yield)。
- 主线程 yield 本就非法(`attempt to yield from outside a coroutine`,[../p1-interpreter/08](../p1-interpreter/08-coroutines.md))——故主线程上的 gibbous 帧**永远不会**被 yield 穿越。规则自洽。

**完整论证(物理限制详解 + 线程级 tier 规则的代价/备选/回填请求)全部归 [07-coroutine-thread-rule](./07-coroutine-thread-rule.md)**——本文只承诺「gibbous 帧不可穿越 yield,且该情形不会发生」,不展开。本文 §2.2 / §3.2 的 `th == mainThread` 守卫是该规则在 trampoline 代码层的兑现点。

> **本文不展开 yield 不穿越的范围边界**:那是 [07-coroutine-thread-rule](./07-coroutine-thread-rule.md) 的职责(它定线程级 tier 规则、协程升层确认、对 08/P2 的回填请求)。本文链过去,不重复。

---

## 6. trampoline 实装骨架

### 6.1 包布局 `internal/gibbous/wasm/trampoline.go`

trampoline 主体在 `internal/gibbous/wasm/trampoline.go`,与 [02-translation](./02-translation.md) §6.1 的包布局共题:

```
internal/gibbous/wasm/
  compile.go      翻译器主体(02-translation §6.1)
  emit.go         WAT/wasm 字节发射(02)
  opcodes.go      逐 opcode 翻译(02 §3)
  memory.go       arena ↔ wazero memory adapter(03-memory-model §1)
  trampoline.go   ★ 本文主体:Trampoline struct + enter / 三向 dispatch 入口
  helpers.go      ★ 本文:imported 助手 Go 实装(h_call/h_arith/...,§3.2/§3.4)
  code.go         GibbousCode 实现(p3Code,[../p2-bridge/05] §6.2)
```

trampoline.go 的三大职责:

```go
// internal/gibbous/wasm/trampoline.go —— P3 跨层 trampoline
type Trampoline struct {
    // codes: 已注册的 GibbousCode(Proto → p3Code),installGibbous 时注册(§6.4)。
    //   每 State 一份 Trampoline(§6.3),但 codes 表的内容(GibbousCode)是
    //   多 State 共享的只读对象([../p2-bridge/05] §6.4)。
    codes map[*bytecode.Proto]*p3Code

    rt wazero.Runtime  // 本 State 的 wazero Runtime(§6.3 每 State 一份)
    st *State          // 反查 State(helper callback 闭包捕获用)
}

// enter:crescent → gibbous 升层入口(§2.2 enterGibbous)。
func (t *Trampoline) enter(st *State, f *frame, i Instruction, cl value.GCRef) callResult { /* §2.2 */ }

// 三向 dispatch helpers:gibbous → {gibbous,crescent,host}(§3.2)。
func (t *Trampoline) hCall(ctx context.Context, st *State, callBase, pc uint32) uint32 { /* §3.2 */ }
func (t *Trampoline) hArith(ctx context.Context, st *State, op, base, pc uint32) uint32 { /* §3.3 */ }
// ... h_gettable / h_safepoint / h_alloc / h_raise / h_concat ...

// Register:installGibbous 调用,把 GibbousCode 注册进 codes 表(§6.4)。
func (t *Trampoline) Register(proto *bytecode.Proto, code *p3Code) { t.codes[proto] = code }
```

三大职责对应本文三主线:`enter` = 升层入口(§2);`hCall` 等 dispatch helpers = 慢路径出口(§3)+ 错误冒泡(§4);`Register` = 与 P2 installGibbous 接线(§6.4)。

### 6.2 wazero ctx 与 Go context.Context 的关系

context 经 wazero ctx 传入,canceled hook 走 helper:

```go
// p3Code.Run(§2.2 step3 / [../p2-bridge/05] §6.2):context 经 wazero ctx 第一参数传入
func (c *p3Code) Run(st *State, base int32) int32 {
    // st.ctx 是 State 持有的 context.Context(经门面层 SetCancelHook 注入,
    //   [../p2-bridge/04] context 取消钩子);它作为 wazero Call 的第一参数。
    results, err := c.fn.Call(st.ctx, uint64(base))
    if err != nil {
        st.pendingErr = err   // wazero 内部错误(罕见,如 OOM/trap)→ ERR
        return 1
    }
    return int32(results[0])
}
```

机制:

- **context 经 wazero ctx 一路传到 helper callback 的第一参数**:wazero 把 `fn.Call(ctx, ...)` 的 ctx 透传给 module 内所有 imported 函数 callback 的第一参数(§3.4 每个 callback 的 `ctx context.Context`)。所以 gibbous 代码调 helper 时,helper 能拿到当前 context。
- **canceled hook 走 helper**:context 取消检查(`ctx.Err() != nil` 或 `SetCancelHook` 的钩子)在 helper 内做——典型在 `h_safepoint`(回边)里顺带检查 context 是否已取消([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §5 safepoint 同位)。因为回边是 gibbous 代码周期性回 Go 的点,context 取消在那里检查能及时响应(类似 GC pending 检查,[05-safepoint-gc](./05-safepoint-gc.md) §3)。
- **剥离接口签名避免直接依赖标准库 context**:P1 现状(`internal/crescent/state.go` 的 `ctxHolder`)已用「`err func() error`」抽象签名剥离 context 直接依赖,保持 internal 包零标准库非基础包依赖——P3 同款机制可共用(`SetCancelHook(func() error)` 注入,context 在门面层包装,[00-overview](./00-overview.md) 相关纪律 + issue234 反思轮「internal 接口签名避免反向依赖标准库」)。
- **待 spike 验证**:wazero 是否把 ctx 透传给所有 imported callback、ctx 取消是否能中断正在执行的 Wasm 函数——这些 wazero API 行为细节待 [01-spike-gate](./01-spike-gate.md) §4 spike 验证。

### 6.3 多 State 并发:trampoline 每 State 一份

**纪律**:trampoline 每 State 一份(wazero Runtime per State),无锁。

```
多 State 并发的 trampoline 形态:
  State A ── Trampoline A ── wazero Runtime A ── linear memory A(arena A)
  State B ── Trampoline B ── wazero Runtime B ── linear memory B(arena B)
  ★ 两个 State 各自独立的 Runtime / memory / trampoline,无共享可变状态,无锁。
```

物理依据:

- **每 State 一个 wazero Runtime**:[03-memory-model](./03-memory-model.md) §1 已定 NewState 时即经 wazero 分配 memory(每 State 一份 arena = 一份 linear memory)。Runtime 也随之每 State 一份——State A 的 gibbous 代码在 Runtime A 跑、读 memory A;State B 在 Runtime B 跑、读 memory B,物理隔离。
- **helper callback 闭包捕获各自的 State**:§3.4 的 host module 是 per-State 构造的(`buildHostModule(ctx, rt, st)` 捕获 `st`)——State A 的 h_call 捕获 State A,操作 State A 的 thread/arena;无跨 State 数据访问,故无锁。
- **与 P1 多 State 隔离一致**:P1 已是「每 State 私有 arena/thread/globals」(`internal/crescent/state.go` 的 State struct),`-race` 硬门禁通过([00-overview](../p2-bridge/00-overview.md) P2 验收第四档「多 State `-race`」)。P3 的 per-State Runtime/Trampoline 延续这个隔离模型——多 State 并发跑各自的 gibbous 代码,不引入新的共享可变状态。

### 6.4 multi-State 共享 Proto:同一 GibbousCode 多 State 共享

**纪律(承 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.5 + [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.4)**:同一 GibbousCode 多 State 共享(installGibbous 已 CAS 安装),trampoline 各 State 独立调用。

```
multi-State 共享 Proto 的两层(GibbousCode 共享 vs Runtime 独立):
  ┌──────────────────────────────────────────────────────────────┐
  │  Bridge(Program 级)                                          │
  │    gibbousCodes[proto] = GibbousCode  ← 多 State 共享的只读对象 │
  │    (compileMu 锁 + 双重检查保证只编一次,[../p2-bridge/04] §4.5)│
  └──────────────────────────────────────────────────────────────┘
           │                              │
   State A 的 Trampoline           State B 的 Trampoline
     codes[proto] = ↑同一 GibbousCode    codes[proto] = ↑同一 GibbousCode
           │                              │
     在 Runtime A 调 Run            在 Runtime B 调 Run
     (读 memory A)                  (读 memory B)
```

要点:

- **GibbousCode 是只读共享对象**:[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.4 已定「Program 同时被多个 State 并发使用时,GibbousCode 是只读共享对象——任何 State 都可调 Run,Dispose 只在 Program 销毁时调一次」。wazero `CompiledModule` 本身线程安全(可被多个 Runtime 实例化/调用)。
- **installGibbous 的幂等保证**:[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.5 的 compileMu 锁 + 双重检查保证「多 State 同时 considerPromotion 同一 Proto 时,只编译一次,后到的 State 复用已有 GibbousCode」——所以 `gibbousCodes[proto]` 全局只有一份,各 State 的 trampoline `codes` 表指向同一份。
- **trampoline 各 State 独立调用**:虽然 GibbousCode 共享,但「执行」是各 State 在自己的 Runtime 里、读自己的 memory——State A 调 `code.Run(stateA, base)` 在 Runtime A、读 memory A;State B 调 `code.Run(stateB, base)` 在 Runtime B、读 memory B。**Run 是无状态的**(状态全在传入的 State 参数 + base 指向的共见栈槽),所以多 State 并发调同一 GibbousCode 安全。
- **对 P2 04 的回填请求(§9)**:installGibbous 增「multi-State 共享 Proto trampoline 注册」幂等保证的显式登记——当前 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.5 已有锁 + 双重检查,P3 落地时把「trampoline.Register 在多 State 下的幂等性」补进 §4.5(各 State trampoline 注册同一 GibbousCode 是幂等的,因为 GibbousCode 全局唯一)。

### 6.5 panic recover:gibbous 内 panic 兜底转 status=ERR + LogPanic

**纪律**:gibbous 内 panic(理论不应发生,Wasm 是安全的)兜底转 status=ERR + LogPanic。

```go
// internal/gibbous/wasm/trampoline.go —— enter 的 panic 兜底(防御性)
func (t *Trampoline) enter(st *State, f *frame, i Instruction, cl value.GCRef) (res callResult) {
    defer func() {
        if r := recover(); r != nil {
            // gibbous 内 panic 理论不应发生(Wasm 是类型安全的,wazero 类型系统
            //   兜底,§9 缺口);但 helper callback(Go 代码)有 bug 可能 panic,
            //   或 wazero 内部异常。兜底:转 status=ERR + LogPanic,绝不让 panic
            //   穿越到 P1 主循环(与 [../p2-bridge/04] §5.2 编译期 panic recover 同款纪律)。
            st.pendingErr = &LuaError{Msg: fmt.Sprintf("gibbous runtime panic: %v", r)}
            if t.st.logger != nil {
                t.st.logger.LogPanic(st.protos[protoIDOf(cl)], r)  // [../p2-bridge/04] §6.4
            }
            // 弹本帧 CallInfo(若已压),走错误冒泡
            popCallInfoIfPushed(st.runningThread)
            res = vm.throwPending(f)
        }
    }()
    // ... §2.2 enterGibbous 主体 ...
}
```

要点:

- **panic 理论不应发生**:gibbous 代码是 Wasm,类型安全(wazero 类型系统兜底,§9 缺口),不会有 Go 那种 nil deref / 越界 panic。真正可能 panic 的是 **helper callback(Go 代码)**——比如 h_alloc 内分配逻辑 bug、h_gettable 内 IC 处理 bug。
- **兜底转 status=ERR**:与 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §5.2「P3 后端 panic recover」同款防御性纪律——P3 是新代码(P3 落地早期 helper 实装可能有 bug),panic 必须被 recover 转成可恢复错误,**绝不让 panic 穿越到 P1 主循环**(P1 主循环不应因 P3 bug 崩溃,与运行期 GibbousCode.Run 的兜底对偶,[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1 Run 契约)。
- **LogPanic 接通 P2 诊断**:panic 兜底时调 `logger.LogPanic`([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6.4),写独立诊断 channel(不刷主日志)——让开发者识别 P3 runtime helper 的实现 bug。
- **gibbous 帧 panic 是否可达待 PW6 实测(§9 缺口)**:理论不应有(wazero 类型系统兜底),但 helper callback Go 代码的 panic 面真实存在——PW6 落地时实测 helper 在边角形态(F7 漏判类)下是否会 panic,决定这个兜底是否被真实触发。

### 6.6 trampoline 表生命期与 P2 installGibbous 接线

trampoline 的 `codes` 表(§6.1)由 P2 的 installGibbous 注册、由 Program 销毁时清理——本节给完整的生命期接线,串起 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.4/§4.6 与 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.4:

```
GibbousCode + trampoline 表的生命期(承 [../p2-bridge/04] §4.6 + [../p2-bridge/05] §6.4):
  ① 创建:P3.Compile 返回时新建 p3Code(wazero CompiledModule + api.Function,[../p2-bridge/05] §6.2)
  ② 安装:installGibbous([../p2-bridge/04] §4.4)
            ├─ b.gibbousCodes[proto] = code      (Bridge 持引用,防 GC 早释)
            ├─ t.Register(proto, code)            (trampoline codes 表注册,§6.1)
            └─ LogPromoted(proto, pd)             (升层日志,§7.1)
  ③ 使用:crescent doCall 见 tierState==TierGibbous → enterGibbous(§2.2)→ code.Run
            (每 State 在自己的 Runtime 调,多 State 共享同一 code,§6.4)
  ④ 回收:Program 销毁 → Bridge 析构:
            ├─ 遍历 gibbousCodes,每个 code.Dispose()  ([../p2-bridge/05] §6.4,幂等)
            │    = c.module.Close(ctx)  (关 wazero CompiledModule,[../p2-bridge/05] §6.2)
            ├─ 清 trampoline codes 表
            └─ 关每 State 的 wazero Runtime(§6.3)
```

要点:

- **trampoline 的 `Register` 是 installGibbous 的子步骤**:[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.4 的 installGibbous 三件事(注册 trampoline 表 / 挂 GibbousCode 引用防 GC / 写 bit50)中,「注册 trampoline 表」就是调 `t.Register(proto, code)`(§6.1)。当前 P2 实装(`internal/bridge/bridge.go`)的 installGibbous 简化版只挂 `gibbousCodes` map(因 P3 还没真存在),P3 落地时补上 `Register` 调用——这是 §6.4 末尾「对 P2 04 回填」的具体内容之一。
- **Dispose 幂等 + 仅 Program 销毁时调一次**:[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.4 定 Dispose 幂等(用 sync.Once 或 atomic CAS 守)、多 State 共享 Proto 时只在 Program 销毁调一次。trampoline 的 codes 表清理与 Dispose 同批——不存在「卸载 gibbous 回 interp」的运行期事件(单向 + 吸收态,[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §2.4),所以 codes 表只增不减(运行期),只在 Program 析构时整体清。
- **GibbousCode 与 Program 同生命期**:[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.6——一旦升层不再卸载,直到 Program 整体释放。这让 trampoline 的 codes 表在运行期是「只读稳定」的(注册后不变),多 State 并发读它无锁(§6.3 各 State 自己的 trampoline,codes 表内容是注册时一次性写入的共享 GibbousCode)。
- **多 State 下 Runtime 各关各的,GibbousCode 关一次**:回收时,每 State 的 wazero Runtime 各自关闭(§6.3 per-State Runtime);但 GibbousCode(wazero CompiledModule)是 Program 级共享对象([../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.4),只 Dispose 一次。这个「Runtime per-State / CompiledModule per-Program」的二层结构是 wazero 的标准用法(CompiledModule 可被多个 Runtime 实例化)——待 spike 验证具体 API([01-spike-gate](./01-spike-gate.md) §4)。

---

## 7. 升层日志接通

### 7.1 gibbous 帧入口的诊断:链 P2 04 §6 升层日志格式

gibbous 帧入口的诊断**复用 P2 升层日志格式**——本节链 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6,不重定义:

- **升层成功日志在 installGibbous 时打**(不在每次进 gibbous 帧时打):格式 `function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)`([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6.1)。这是「Proto 第一次升 gibbous」的一次性事件,由 P2 的 `installGibbous` → `LogPromoted` 触发([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6.4),**P3 trampoline 不重复打**。
- **日志说「promoted to gibbous」不说「promoted to wasm」**:[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6.1 末尾纪律——日志统一是 gibbous(月相 tier 名,不区分 P3/P4 子档,[00-overview](./00-overview.md) §1 坐标系警告)。P3 是 gibbous 的 Wasm 后端,但日志反映 tier 层(gibbous)而非后端(wasm)。

### 7.2 gibbous 帧 RETURN 时不打日志(只在三个时点打)

**纪律**:gibbous 帧 RETURN 时不打日志——升层日志只在 promote / stuck / fail 三个时点打。

```
升层日志的三个时点([../p2-bridge/04] §6,P3 trampoline 不增):
  ① promote:installGibbous 时一次(LogPromoted,§6.1)——Proto 第一次升 gibbous
  ② stuck:   considerPromotion 判不可编译时一次(LogStuck,§6.2)——F1-F7 拦下
  ③ fail:    Compile 返回 err 时一次(LogCompileFail,§6.3)——F7 漏判/资源/panic
  ★ 不打:gibbous 帧每次进入(enterGibbous)/ 每次 RETURN —— 那是热路径,
    日志会刷屏,且无诊断价值(升层后每次调用都进 gibbous 是预期行为)。
```

物理原因:

- **进/出 gibbous 帧是热路径**:升层后的 Proto 被反复调用数千万次(列内核形状),每次进 gibbous 帧(enterGibbous)/ 每次 RETURN 都在热路径——打日志会刷屏 + 拖累性能。
- **三个时点都是低频事件**:promote(一次性)/ stuck(一次性)/ fail(一次性)都是「Proto 生命周期内最多一次」的状态转移事件([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §2 状态机单向 + 吸收态),频次低,不刷屏。
- **差分一致性**:[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §6.5 的「特定脚本应产生特定升层日志」断言基于这三个时点——若 gibbous 帧 RETURN 也打日志,日志条数随运行期调用次数变化,无法做稳定断言。

---

## 8. 不变式清单

[01-spike-gate](./01-spike-gate.md)~[08-testing-strategy](./08-testing-strategy.md) 各篇分别承担,本节聚合本文相关的 6 条:

1. **CallInfo 是唯一真相,跨层只传 base**:gibbous 帧压 CallInfo(标 bit50),跨层只传 `base i32`,其余从共见栈槽 + 编译期立即数自取(§0.1 + §2.1)。映射 [00-overview](./00-overview.md) §9 不变式 3。
2. **bit50 在新帧入口写,不改现存帧**:gibbous 帧入口 trampoline 在压新 CallInfo 时写 bit50=1;installGibbous 不改任何现存 CallInfo 的 bit50(现存帧仍跑 crescent 直到自然消亡)(§1.2 + §1.4)。
3. **status 链可穿,yield 链不可穿**:错误经 status 链单向冒泡可任意穿越 gibbous 帧(单向放弃,无需复原);yield 不可穿越 gibbous 帧(物理限制 + 线程级 tier 规则使该情形不会发生)(§4.4 + §5)。映射 [00-overview](./00-overview.md) §9 不变式 4。
4. **helper 接受 base + pc 立即数,自取一切**:所有 imported 助手从共见栈槽读操作数、从 pc 立即数物化错误位置(savedPC),返回 status i32,不依赖任何 Go/Wasm 栈上的额外上下文(§3.3 + §4.5)。
5. **helper 可重入 wazero**:`h_call` 的 gibbous 分支再 `fn.Call`,形成 gibbous→helper→gibbous 调用链;helper 内可起新 execute(gibbous→helper→crescent)(§3.2)。
6. **错误冒泡是单向放弃——无需复原 Wasm 帧**:错误发生时这一帧及其上所有帧被丢弃,Wasm 函数只需 return status≠0,栈帧由 wazero 自动销毁,无需恢复到一致状态;清理责任在 pcall 边界一次性收口(§4.3 + §4.4 + §4.6)。映射 [00-overview](./00-overview.md) §9 不变式 4(下半)。

---

## 9. 文档缺口

各项记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md),随实现推进收敛:

| 缺口 | 内容 | 收口时机 |
|---|---|---|
| **gibbous → gibbous 同 module 内 call_indirect 直调** | 批量 module([02-translation](./02-translation.md) §1.2 优化项)下,同 module 内的 gibbous→gibbous 调用用 `call_indirect` 直调,免 Go 往返(§3.1 表第一行的「优化」列)——能否兑现「免 Go 往返」收益取决于实测跨层成本与同 Program 内函数互调密度 | **PW10 spike 闸门先行**(PW9 实测密度论据已坐实:call 核小叶函数每调经 `h_call` 双跨层 ~143ns → gibbous 比 crescent 慢 7x;loop 计算密集核 2.58x 达标。investigator 确认为里程碑级架构改(每 Proto 独立 module + Lua 帧住 Go `th.cis`),生死未知数 = wazero 增量 module 可行性,故 spike 先行验 call_indirect 成本 + 增量重编生命周期,绿则重写、红则退守拒升小叶函数启发式) |
| **helper 数量增长后是否拆 trait** | per-helper Go 函数 vs 单一 dispatcher(`h_dispatch(opcode, base, pc)` 统一入口)——单一 dispatcher 减少 imported 函数数量但每次调用多一次 switch;per-helper 直达但 imported 表膨胀 | 留 PW6 实测后定(§3.3) |
| **对 P1 05 的回填:bit50 字段语义登记** | 把 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 bit50 从「P1 恒 0 预留」升级为「P3 trampoline 写,P1 不读;语义见 P3 04 §1」;crescent callInfo struct 加 gibbous 标识字段(§1.5 (a)/(b) 之一) | **记录,P3 落地时同批补**(PW6,§1.6) |
| **对 P2 04 的回填:installGibbous 增 multi-State 共享 Proto trampoline 注册幂等保证** | [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.5 已有 compileMu 锁 + 双重检查;P3 落地时补「各 State trampoline.Register 同一 GibbousCode 是幂等的(GibbousCode 全局唯一)」的显式登记 | **记录,P3 落地时同批补**(§6.4) |
| **gibbous 帧 panic 是否可达** | 理论不应有(Wasm 类型安全,wazero 类型系统兜底);但 helper callback(Go 代码)的 panic 面真实存在——§6.5 的兜底是否被真实触发待实测 | 留 PW6 实测(§6.5) |

---

相关:
[00-overview](./00-overview.md)(P3 总览,本文遵守其章节番号与风格) ·
[01-spike-gate](./01-spike-gate.md)(开工闸门,§2.1/§2.4 单 i32 协议 = S2 形状) ·
[02-translation](./02-translation.md)(翻译器,§3.6 CALL/RETURN 调本文,§4 pc 物化协议) ·
[03-memory-model](./03-memory-model.md)(共见内存,跨层只传 base 余从共见栈槽自取) ·
[05-safepoint-gc](./05-safepoint-gc.md)(层边界 safepoint 与 trampoline 同位,§3.3 h_safepoint) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(IC 快照固化,§3.3 h_gettable) ·
[07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(yield 不可穿越完整论证 + 线程级 tier 规则,§5 链过去) ·
[08-testing-strategy](./08-testing-strategy.md)(差分口径,traceback 逐字节一致 §4.5) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1.2 CallInfo bit 布局 + §7 调用约定 + §9 错误冒泡) ·
[../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md)(§3 路线 B yield 冒泡 + §5 yield 不跨 host 边界) ·
[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(§4.4 installGibbous 写 bit50 + §4.5 多 State 锁 + §6 升层日志) ·
[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(§6 GibbousCode 接口 Run/Dispose/GetTrampoline) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(P4 继承本文全部跨层协议,只换发射后端,§0.4) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(回填请求 + 缺口跟踪)
