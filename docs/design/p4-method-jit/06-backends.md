# P4 §6:双后端架构——共享骨架 + per-arch 发射器 + 双架构测试纪律

> 状态:**详细设计**(P3 PW0-PW10 全卷收口后启动 P4 时落地)。本文是 P4 文档集的「双后端架构」单一事实源——为什么不用宏汇编、骨架/per-arch 切分、模板的分级表达、双架构 CI 矩阵、per-arch 寄存器约定。
>
> 上游契约:[../roadmap.md](../roadmap.md) §0「纯 Go / 禁 cgo」(不得依赖 cgo 汇编器)、[../roadmap.md](../roadmap.md) §7 prior art(wazero 共享骨架 + per-arch compiler 是组织模板)、[../architecture.md](../architecture.md) §1 包布局(`internal/gibbous/jit` 与 `internal/gibbous/wasm` 并列)、§2 月相 tier 映射(P3/P4 同属 gibbous tier-1)。
>
> P1 依赖面:[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md)(38 个 opcode 完整表,本文 §3 按族归类的源)、[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §3(NaN-box 编码)、[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1(CallInfo / 值栈布局)。
>
> P2 依赖面:[../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md)(`TypeFeedback`,§3.9 投机/通用二选)、[../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md)(F1-F7 闸门,§3.8 渐进白名单)。
>
> 同集协作:[./04-osr-deopt.md](./04-osr-deopt.md) §3.3 / §6(OSR exit 物化序列与 exit stub 物理形态——本文 §3.x guard 行链至 04,§4 寄存器约定给 exit stub 着陆面)、[./05-system-pipeline.md](./05-system-pipeline.md) §2.2 / §3.3(W^X / 代码页 / jitContext / helper 表 / trampoline 协议骨架)、[./08-testing-strategy.md](./08-testing-strategy.md)(差分双架构双跑——本文 §5 给纪律,08 给口径)、[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §8(CI 门禁)。

对应 Go 包:`internal/gibbous/jit`(主包),子包 `internal/gibbous/jit/amd64` 与 `internal/gibbous/jit/arm64`。

---

## 0. 定位

### 0.1 一句话

**P4 = 共享编译骨架 + per-arch 发射器,落地 amd64/arm64 双架构**——编译驱动、guard 语义、OSR 物化逻辑、jitContext 布局、helper 表、调用协议**只写一次**(架构无关);每 opcode 的发射函数、guard 比较/条件跳指令、寄存器约定、trampoline 汇编、指令编码器、icache flush(arm64)**写两份**(per-arch 各一)。

这与 wazero wazevo backend 的组织同款([../roadmap.md](../roadmap.md) §7)——共享骨架 + amd64/arm64 各自 backend——是「纯 Go 多架构机器码生成」的可行参照。

> **wazero v1.x 已切到 wazevo**:本文以及后续所有 wazero 采石场指针指的是当前一代 wazevo 引擎(SSA + per-ISA backend,位于 `internal/engine/wazevo/backend/isa/{amd64,arm64}/`),不是早期 `internal/engine/compiler` 子包(已不存在)。

### 0.2 包布局

```
internal/gibbous/jit/                  ← 主包(架构无关骨架)
  compile.go        编译驱动:线性扫字节码、标签/回填、模板选型
  emit.go           per-arch emitter trait + 通用调度
  osr.go            OSR exit 物化逻辑(承 04 §3.3)
  codepage.go       exec mmap 代码页池(承 05 §2.2)
  jitcontext.go     jitContext 布局 + helper 表(承 05 §3.3)
  trampoline.go     调用协议骨架(承 05 §3.3 / §3.4)
  amd64/            per-arch 发射器(amd64)
    emitter.go        实现 emit.go 的 trait
    encoder.go        x86-64 指令编码器
    regs.go           amd64 寄存器约定(§4.1)
    trampoline_amd64.s 汇编 stub:进入/退出/exit/helper 跳板
    opcodes_*.go      各 opcode 族模板(arith/table/control/call/closure/lineart)
  arm64/            per-arch 发射器(arm64)
    emitter.go / encoder.go / regs.go(§4.2)
    trampoline_arm64.s 汇编 stub + icache flush(§4.2.8)
    opcodes_*.go
```

设计要点:

- **执行层包名沿用月相**(`gibbous/jit` 与 `gibbous/wasm` 并列,[../architecture.md](../architecture.md) §1):P3 与 P4 同属 gibbous tier-1,只换发射后端(P3 发 Wasm 交 wazero,P4 直接发原生码)。
- **per-arch 子包从主包看是 emitter trait 的实现**(§2.4):主包不 import per-arch 子包,经 build tag 在编译期选定 emitter 注入。这与 wazero `internal/engine/wazevo/backend/isa/{amd64,arm64}/` 子包按 ISA 分流的形态同形。
- **公共 API 不跨 per-arch 子包暴露**:外部嵌入只见 `wangshu.Program.Call`,经 [bridge](../p2-bridge/05-p3-p4-interface.md) 升层后由主包路由到当前架构 emitter。

### 0.3 章节路标

| § | 内容 |
|---|---|
| §1 | 后端抽象的两个候选(宏汇编 vs 共享骨架 + per-arch),决定性理由 |
| §2 | 共享骨架 vs per-arch 切分(架构无关写一次 / per-arch 各一份)+ emitter trait |
| §3 | 模板的分级表达——38 opcode 按族归类 + 投机/通用二选 |
| §4 | 寄存器约定(per-arch 核心决策)——amd64 / arm64 分配方案 + 典型 ADD 投机模板伪汇编 |
| §5 | 双架构测试纪律——CI 双跑、交叉编译不替代真机、先 amd64 后 arm64 |
| §6 | 实装顺序与里程碑——PJ0..PJ10 渐进交付 + 验收口径预设 |
| §7 | 不变式清单 |
| §8 | 风险与开放问题 |
| §9 | 回填请求 |

---

## 1. 后端抽象的两个候选

P4 立项决策表展开:

### 1.1 候选 (a):架构中立宏汇编器

**形态**:模板用统一「虚拟指令」写一遍,中立宏汇编器在编译期翻译到目标架构。典型实现是定义抽象 IR(虚拟寄存器、虚拟指令 `vMov`/`vAdd`/`vCmpJump`),per-arch 提供 lowering(虚拟→真实)。LLVM SelectionDAG、QEMU TCG、Cranelift 早期 backend 抽象都是这一族。

**否决理由**(决定性):

1. **泄漏寻址模式差异**:amd64 复杂寻址(`[base+index*scale+disp]`)与 arm64 简单寻址(`[base, #imm]` / `[base, Xn, LSL #2]`)在中立层要么各自暴露(等于没抽象),要么压成最低公分母(amd64 浪费,arm64 凑活)。寻址模式恰恰是模板编译密度的关键。
2. **泄漏标志位差异**:amd64 算术更新 EFLAGS,跳转读标志(`add` 后接 `jno`);arm64 显式标志(`adds` 才更新 NZCV)+ 条件指令(`b.cs`)。投机模板的 guard(§3.x「IsNumber 单比较 + 条件跳」)在两架构是不同指令组合,统一会多一条无用指令或走最复杂通路。
3. **泄漏寄存器约定差异**:amd64 SystemV / Windows、arm64 AAPCS、Go ABI0 三套约定的被调方保存集 / 参数寄存器都不同(§4.3);中立层「N 个通用寄存器自由分配」与真实 ABI 合约割裂——trampoline 难看、helper 调用前后多余 spill。
4. **生成码质量与可调试性都受损**:disassembly 的产物与心智模型隔层映射,debug 投机错误要还原一层翻译。这与 [./02-template-direction.md](./02-template-direction.md) §1.3「P4 全部价值在生成码质量与系统管线正确性」直接冲突。
5. **「写一次」是假命题**:即便有中立层,per-arch 仍需 lowering / 寄存器分配 / icache / trampoline——只是把工作量从「opcode 模板」推到「lowering 表」,总量不减只移位,且增加 IR 调试代价。

### 1.2 候选 (b):共享骨架 + per-arch 发射器(选定)

**形态**:编译器骨架(线性扫描、pc→机器地址映射、guard 语义、OSR 接线、feedback 决策、helper 表组装)架构无关;每 opcode 的发射函数按架构各实现一份,后接小型指令编码器。

**结构**:

```
[架构无关层] compile.go::CompileProto(proto, fb) {
  emitter := pickEmitter()              // amd64.NewEmitter() 或 arm64.NewEmitter()
  for pc, instr := range proto.Code {
    emitter.EmitOpcode(pc, instr, fb)   // per-arch 发射函数,接受架构无关参数
  }
  emitter.EmitOSRExitTable()            // per-arch 物化序列(承 04 §6)
  return emitter.Finalize()             // 返回 jitCode(代码页 + 入口偏移 + exit 表)
}
```

每 opcode 的发射函数按架构各实现一份,具体指令选择(amd64 `mulsd` vs arm64 `fmul`,寻址模式、立即数编码、跳转回填)写得地道。**根本区别**:候选 (b) 把抽象边界放在「opcode → 机器码段」层级——一段一段独立写,不强行统一寻址/标志/寄存器约定;骨架只承载与架构无关的**逻辑**(模板选型、guard 决策、exit 物化映射表),不承载**指令形态**。

### 1.3 决定性理由

| 维度 | (a) 中立宏 | (b) 共享骨架 + per-arch |
|---|---|---|
| 生成码质量 | 受最低公分母约束,寻址 / 标志位 / 寄存器分配三处泄漏 | 每架构地道,投机模板 guard 紧凑(2-3 条) |
| 系统管线正确性 | trampoline / Go ABI0 / icache / W^X 仍要 per-arch | per-arch 直接对接平台具体约定 |
| 调试可达性 | disassembly 与心智模型隔层映射 | disassembly 即模板源码所写 |
| 实现总成本 | opcode ×1 + lowering ×2 + IR 调试 | opcode ×2 + 编码器 ×2(更线性) |
| 与 wazero 对照 | 无对应物 | 同款组织(§1.4) |

P4 全部价值在生成码质量与系统管线正确性,中立宏层在两点上都是负资产。

### 1.4 与 wazero 同款组织验证

wazero wazevo backend([../roadmap.md](../roadmap.md) §7 prior art)是「纯 Go 运行时机器码生成可行」的存在性证明,内部组织正是「共享骨架 + amd64/arm64 各自 backend」:`internal/engine/wazevo/`(SSA IR、CFG、寄存器分配抽象、guard 语义)+ `internal/engine/wazevo/backend/isa/amd64/`(per-arch 发射函数 + 寄存器分配 + 寻址模式 + 立即数编码)+ `internal/engine/wazevo/backend/isa/arm64/`(同款 arm64 一份)+ 各自的指令字节流编码器。

P4 直接对照这套组织,差异只在「P4 输入是 Lua 字节码,wazero 是 Wasm」——两者输出形式(原生机器码段 + 入口偏移 + 跨层 trampoline)与系统管线诉求(exec mmap / W^X / icache)完全同构。这不是抄设计,是同一约束下的同一最优解。

### 1.5 模板总量可控

[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §4 完整 ISA 共 38 个 opcode,加 guard 胶水(IsNumber 双查 / 同表同代次 / 单态 metatable)、exit stub 胶水、trampoline 胶水,**每架构数十段发射函数**,在 +1-2 人年预算内([./00-overview.md](./00-overview.md) §0.1)。

| 项目 | per-arch 发射函数 / IR lowering 数量级 |
|---|---|
| wazero compiler | ~150-200 op(Wasm 全集 + 内部 IR) |
| LuaJIT IR lowering | ~80-100 IR op |
| **P4** | **~38 opcode 模板 + ~10 胶水 ≈ 50 段 / 架构** |

每段平均 20-50 行 Go(发射函数)+ 编码器调用,总规模 amd64 子包 ~2000-3000 行 + arm64 同档,与 P3 翻译器([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) ~1500 行)在同一量级。

### 1.6 per-arch 是「分到本架构」非「重写一份」

骨架(§2.2)与 per-arch(§2.3)在工作量上**各占一半**:骨架的「线性扫描 + 标签回填 + 模板选型」是字节码遍历框架(直接复用 P3 PW 的 `compile.go` 同款驱动模式,只换发射目标);per-arch 是「每 opcode 选指令 + 编码 + 寄存器 + trampoline」。

| 阶段 | 骨架(架构无关) | per-arch ×2 | amd64 / arm64 |
|---|---|---|---|
| 编译驱动 / 标签 / 选型 | 30% | — | — |
| OSR 物化 / jitContext / helper 表 | 15% | — | — |
| 代码页 / W^X / 调用协议骨架 | 5% | — | — |
| per-arch 发射函数 ×38 opcode | — | 30% | 15% / 15% |
| per-arch 寄存器 + trampoline + icache | — | 15% | 7% / 8% |
| per-arch 指令编码器 | — | 5% | 2.5% / 2.5% |
| 合计 | **50%** | **50%** | **24.5% / 25.5%** |

推论:

1. **arm64 滞后交付不阻塞 P4 验收**——验收平台定 amd64,arm64 单独按 PJ8/PJ9 交付。发布口径如实标注「P4 验收=amd64,arm64 后随」(本文 §7 风险节)。
2. **arm64 工作量 ≈ amd64 ~104%**——但骨架已写完后 arm64 边际成本只有 ~25%,不重写一半。
3. **骨架在第一架构落地时就按 §2 切分好接口**(§5.5 反复强调),avoid「amd64 写完再抽象」返工。

### 1.7 裁决纪律:候选 (b) 选定基于定性理由,emitter trait 签名落地前须经 spike 闸门

**承 [`2026-06-15-p3-pw10-r1-r2-callinfo-migration-round`](../../../llmdoc/memory/reflections/2026-06-15-p3-pw10-r1-r2-callinfo-migration-round.md) 教训 1「探未审视的 architecture FORK」纪律**——「里程碑级架构改动配 spike 闸门」对应到 P4 双后端这一层时,具体落实是:

- **§1.2 候选 (b) 选定基于定性理由(寻址 / 标志 / ABI 三处泄漏 + wazero 同款组织验证),无 spike 数据**——但「共享骨架 + per-arch」抽象内部仍存在更细的 architecture FORK,设计期未显式审视:**emitter trait 粒度选 (i) 函数级 / (ii) opcode 族级 / (iii) 字节级编码层** 三个候选不等价,有泄漏方向不同。
- **§2.4 emitter trait 签名落地前须经 PJ0 一次单 opcode 双架构原型(spike 闸门)**:**ADD 投机模板 amd64 + arm64 各一份**(per-arch 全栈对位:guard 形态 + f64 快路径 + exit stub + ABI 寄存器 + 编码器调用),validate trait 不漏抽象 + 不污染骨架——若 arm64 落地时发现 trait 需变签名(典型:emit trait 把 amd64 寻址模式硬编码进抽象,arm64 没有 RIP-relative),**回炉重做 trait + 改 amd64 实装,不可绕过(不在 arm64 里 workaround)**。
- **承 §5.5 / §5.6 同款纪律**:「先 amd64 后 arm64 + 骨架先行 + arm64 校验」是平衡——ADD 投机模板双架构原型是这条纪律的 spike 兑现,**spike 不通过(trait 需大改)就不能进 PJ1**。

这条纪律守住的是 P4 抽象层 emitter trait 在 amd64 实装时不留架构假设 / arm64 启动时不返工的工程边界——任何「先 amd64 写完 38 opcode 再抽 trait」的提案直接判否。

---

## 2. 共享骨架 vs per-arch 切分

### 2.1 切分表

承本文 §1 候选 (b),切分表展开到子项粒度:

| 类别 | 架构无关(写一次) | per-arch(各一份) |
|---|---|---|
| **编译驱动** | 线性扫字节码 / pc→机器地址映射 / 前向跳转回填 / IC feedback → 模板选型 / 守卫合并 | — |
| **guard 与 OSR** | guard 语义(IsNumber / 同表同代次 / 单态 metatable) / OSR exit 物化序列**逻辑**(§3.3 承 04 §3.3) / exit 表(机器地址→字节码 pc + 寄存器→栈槽写回脚本) | guard 比较 + 条件跳的**指令** / exit stub 物理序列(承 04 §6) |
| **jitContext / helper 表 / 调用协议** | jitContext 布局(承 05 §3.3) / helper 表入口枚举 / 调用协议(jit→jit / jit→interp / jit→host)逻辑分派(承 05 §3.4) | jitContext 固定寄存器(amd64 r15 / arm64 x28) / helper 调用前 spill 列表 / trampoline 汇编 |
| **代码页管理** | exec mmap 池化策略 / per-Proto 段大小预估 / 释放回收 / W^X 翻面纪律(承 05 §2.2) | 平台 syscall 适配(linux mprotect / darwin MAP_JIT + pthread_jit_write_protect_np / windows VirtualProtect) |
| **指令编码** | — | per-arch 编码器:opcode 字节序、ModRM/REX(amd64) / 32-bit 定长(arm64) |
| **icache 维护** | — | arm64:写码后 `IC IVAU` / `DC CVAU` / `DSB ISH` / `ISB` 序列(§4.2.8) / amd64:无操作 |
| **常量池** | 常量发射策略(嵌指令流 vs 末尾池) | amd64:RIP-relative 立即数 / arm64:`adrp+ldr` 远立即数 |

### 2.2 架构无关层(写一次)

#### 2.2.1 编译驱动(`compile.go`)

```go
func CompileProto(proto *bytecode.Proto, fb *bridge.TypeFeedback, e Emitter) (*JITCode, error) {
    e.PrologueAndCheckStack(proto.MaxStack)            // per-arch:帧建立 + 栈深检查
    pcMap := make(map[int]int32, len(proto.Code))      // bytecode pc → machine offset
    fixups := []forwardJump{}                          // 待回填的前向跳转
    for pc, instr := range proto.Code {
        pcMap[pc] = e.Position()
        op := bytecode.Op(instr)
        if jitFamily(op) == fNotImpl {                 // 不在白名单(§3.8)
            return nil, &CompileError{Kind: NotInWhitelist, PC: pc}
        }
        tmpl := chooseTemplate(op, instr, fb, pc)      // 模板选型:架构无关
        newFixups, err := e.EmitOpcode(pc, instr, tmpl)
        if err != nil { return nil, err }
        fixups = append(fixups, newFixups...)
    }
    for _, fj := range fixups { e.PatchJump(fj.machineOffset, pcMap[fj.targetPC]) }
    e.EmitOSRExitTable(buildExitTable(proto, pcMap))   // 承 04 §6
    return e.Finalize()
}
```

要点:**pc→机器地址映射**架构无关(每条 opcode 发射器返回字节数,骨架累计);**前向跳转回填**架构无关(emitter 留占位偏移并返 `forwardJump`,骨架统一回填,per-arch 提供 `PatchJump` 改写立即数);**模板选型**架构无关(按 [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md) `TypeFeedback` 决定投机/通用,emitter 不直接看 feedback 只看 `tmpl` 枚举)。

#### 2.2.2 guard 语义与 OSR exit 物化逻辑

guard 语义(IsNumber / 同表同代次 / 单态 metatable / globals 代次 / 方法点单态)在 `internal/gibbous/jit/guard.go` 定义为枚举:

```go
type GuardKind uint8
const (
    GuardIsNumber       GuardKind = iota // NaN-box u64 与 NumTag 单比较
    GuardTableSameGen                    // 同表 + 同代次
    GuardMetatableMono                   // 单态 metatable
    GuardGlobalsGen                      // globals 代次
    GuardSelfMono                        // 方法点单态
)
```

per-arch emitter 把 `kind` 翻译成具体指令(amd64 cmp+jne / arm64 cmp+b.ne)。OSR exit 物化的**逻辑**(承 04 §3.3 寄存器→栈槽写回序列编译期生成)架构无关——骨架按编译期已知的「该 guard 失败时哪些寄存器持值、写回哪个栈槽」生成 `ExitWriteback{regID, slotOffset}` 列表,per-arch emitter 翻译成具体 `mov [base+offset], reg` 序列。

#### 2.2.3 jitContext / helper 表 / 调用协议

承 [./05-system-pipeline.md](./05-system-pipeline.md) §3.3,jitContext 布局架构无关:

```go
type jitContext struct {
    arenaBase   uintptr        // arena[0] 字节地址(safepoint 间稳定)
    valueStack  uintptr        // 当前 thread 值栈 R0 字节地址
    preemptFlag uint32         // 抢占检查点读这个
    helperTable [numHelpers]uintptr  // helper 函数指针表
    exitReason  uint32         // exit 原因码
    exitPC      int32          // exit 时的字节码 pc
    // ... 承 05 §3.3 完整字段
}
```

helper 表入口枚举架构无关(`HelperArithSlow` / `HelperTableGrow` / `HelperRaiseError` / `HelperHostCall` / 等,承 05 §3.4)。per-arch:helper 调用前后 spill / restore 序列、ABI 寄存器装参。

#### 2.2.4 代码页管理 / W^X 策略

承 [./05-system-pipeline.md](./05-system-pipeline.md) §2.2:代码页池化、W^X 翻面纪律(任何时刻不持 RWX 页)是架构无关。per-arch 部分仅平台 syscall 适配:

| 平台 | 写入阶段 | 翻面 |
|---|---|---|
| linux amd64/arm64 | `mmap(PROT_READ\|WRITE)` | `mprotect(PROT_READ\|EXEC)` |
| darwin arm64 | `mmap(MAP_JIT \| RWX)` | `pthread_jit_write_protect_np(true)` |
| darwin amd64 | `mmap(RW)` | `mprotect(REX)` |
| windows | `VirtualAlloc(PAGE_READWRITE)` | `VirtualProtect(PAGE_EXECUTE_READ)` |

骨架定义抽象 `CodePage{Write([]byte); Seal(); Free()}`,per-平台子包实现具体 syscall。

### 2.3 per-arch(各一份)

#### 2.3.1 每 opcode 发射函数

每 opcode 一段(38 段),按族组织在 `opcodes_*.go`(§3 族划分)。形态:

```go
// internal/gibbous/jit/amd64/opcodes_arith.go —— ADD 投机模板(amd64)
func (e *amd64Emitter) emitAddSpec(pc int, instr bytecode.Instruction, tmpl Template) {
    a, b, c := bytecode.A(instr), bytecode.B(instr), bytecode.C(instr)
    e.movsdLoad(xmm0, valueStackBase, 8*b)             // movsd xmm0, [rbx + 8*b]
    e.movsdLoad(xmm1, valueStackBase, 8*c)
    e.emitGuardIsNumber(rbx, 8*b, exitPC(pc))          // cmp + jne exit_stub
    e.emitGuardIsNumber(rbx, 8*c, exitPC(pc))
    e.addsd(xmm0, xmm1)
    e.movsdStore(valueStackBase, 8*a, xmm0)
}
```

per-arch 编码差异(同款 ADD 模板的两架构对位见 §4.1.6 / §4.2.6)。

#### 2.3.2 guard 比较 / 条件跳的指令

| guard 种类 | amd64 | arm64 |
|---|---|---|
| `IsNumber` | `mov rax, [base+off]; mov rcx, NumTagMask; and rax, rcx; cmp rax, NumTag; jne exit` | `ldr x0, [base, #off]; and x0, x0, #NumTagMask; cmp x0, #NumTag; b.ne exit` |
| `TableSameGen` | `mov rax, [tableRef]; cmp rax, [tableGenSnap]; jne exit` | `ldr x0, [tableRef]; ldr x1, [tableGenSnap]; cmp x0, x1; b.ne exit` |
| `MetatableMono` | `cmp [metaPtr], expectedMeta; jne exit` | `ldr x0, [metaPtr]; ldr x1, [expectedMeta]; cmp x0, x1; b.ne exit` |

amd64 `cmp` 可直接接内存操作数,arm64 必须先 `ldr`——这就是 §1 候选 (a) 中立宏抽象不掉的「寻址模式差异」实例。

#### 2.3.3 寄存器约定 + 暂存分配 / 2.3.4 trampoline 汇编 / 2.3.5 icache flush(arm64)/ 2.3.6 指令编码器

§4 详。trampoline 是手写汇编(`trampoline_amd64.s` / `trampoline_arm64.s`)不能用 emitter 生成——它是「代码段边界」,在 Go 链接器视角是普通汇编符号,不是动态生成的代码。

### 2.4 per-arch emitter trait(Go 风格 interface)

骨架定义 `Emitter` interface,per-arch 实装:

```go
// internal/gibbous/jit/emit.go —— 架构无关 emitter trait
type Emitter interface {
    Position() int32                                          // 当前发射偏移
    PrologueAndCheckStack(maxStack int)                       // 帧建立
    Finalize() (*JITCode, error)                              // 收尾

    // opcode 发射(返 forward jump 列表给骨架统一回填)
    EmitOpcode(pc int, instr bytecode.Instruction, tmpl Template) ([]forwardJump, error)

    // guard / exit 物化
    EmitGuard(kind GuardKind, op GuardOperand, exitPC int)    // §2.2.2
    EmitOSRExitTable(table []ExitEntry)                       // 承 04 §6

    PatchJump(machineOffset int32, targetMachineOffset int32) // 跳转回填
    EmitHelperCall(helperID int, args []HelperArg)            // 慢路径 helper
}
```

骨架经 build tag 选定(类比 wazero 的 ISA 分流——`internal/engine/wazevo/backend/isa/{amd64,arm64}/`):

```go
//go:build amd64
package jit
func newDefaultEmitter() Emitter { return amd64.NewEmitter() }

//go:build arm64
package jit
func newDefaultEmitter() Emitter { return arm64.NewEmitter() }
```

`compile.go` 不 import per-arch 子包,只经 `newDefaultEmitter` 注入。

---

## 3. 模板的分级表达(对应 38 个 opcode)

承 [../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §4 OpCode 完整表:38 个 opcode 按语义族归类为 7 族,每族给「典型 opcode + 投机模板 vs 通用模板 + 与 P3 翻译表对位」。

族划分总览:

| 族 | opcode | 数量 | 投机来源 | P3 翻译对位 |
|---|---|---|---|---|
| §3.1 算术 | ADD/SUB/MUL/DIV/MOD/POW/UNM/NOT/LEN/CONCAT | 10 | `FBArithStableNumber` | [P3 §3.2](../p3-wasm-tier/02-translation.md) |
| §3.2 比较 | EQ/LT/LE/TEST/TESTSET | 5 | `FBArithStableNumber`(EQ/LT/LE) | [P3 §3.3](../p3-wasm-tier/02-translation.md) |
| §3.3 表 IC | GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/NEWTABLE/SETLIST | 7 | `FBTableMono` / `FBGlobalStable` / `FBSelfMono` | [P3 §3.4](../p3-wasm-tier/02-translation.md) |
| §3.4 控制流 | JMP/FORLOOP/FORPREP/TFORLOOP | 4 | 无投机(回边 safepoint 是义务) | [P3 §3.5](../p3-wasm-tier/02-translation.md) |
| §3.5 调用 | CALL/TAILCALL/RETURN | 3 | jit→jit 直跳(同代次 fastCall) | [P3 §3.6](../p3-wasm-tier/02-translation.md) |
| §3.6 闭包 | CLOSURE/CLOSE/GETUPVAL/SETUPVAL/VARARG | 5 | 无投机(VARARG F1 排除) | [P3 §3.7](../p3-wasm-tier/02-translation.md) |
| §3.7 直线 | MOVE/LOADK/LOADBOOL/LOADNIL | 4 | 无投机(语义直翻) | [P3 §3.1](../p3-wasm-tier/02-translation.md) |
| 合计 | | **38** | | |

### 3.1 算术族(ADD/SUB/MUL/DIV/MOD/POW/UNM/NOT/LEN/CONCAT,编号 12-21)

**投机模板**(适用 `fb[pc].Kind == FBArithStableNumber`,典型 ADD 见 §4.1.6 / §4.2.6 完整伪汇编):load 两操作数 → IsNumber × 2 guard → f64 直算(`addsd` / `fadd`)→ store 回栈槽。**通用模板**(`FBUnstable` / 缺失):走 `HelperArithSlow`(P3 同名 helper 复用,承 P3 §7.3 通用慢路径),不发 guard——直接调慢路径 helper。

各 opcode 投机/通用裁决:

| opcode | 决策 | 理由 |
|---|---|---|
| ADD/SUB/MUL/DIV | 投机优先 | f64 直算高收益 |
| MOD | 投机 | `a-floor(a/b)*b` 仍 f64 |
| POW | 通用 | `math.Pow` 调用本身就重,投机收益不抵 guard 成本 |
| UNM | 投机 | 单 f64 取负 |
| NOT | 通用 | 无元方法,纯真值取反,投机无意义 |
| LEN | 通用 | string/table border 无 f64 路径 |
| CONCAT | 通用 | string concat 是分配,右结合多元 + `__concat` |

**与 P3 翻译表对位**:P3([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3.2)同样分快慢:快路径 f64 直算 + NaN 规范化,慢路径 `h_arith` helper。**P4 与 P3 根本差异**:P3 是「快路径=语义分发,失败走慢路径」(承 P3 §8 不变式 1:零 deopt);P4 是「快路径=投机假设,失败 OSR exit 回解释器」(承 [./03-speculation-ic.md](./03-speculation-ic.md) §2 投机引入 deopt 边)。两者的 guard 都是显式比较 + 条件跳,只是失败后语义不同(P3 走 helper / P4 走 OSR)。

### 3.2 比较族(EQ/LT/LE/TEST/TESTSET,编号 23-27)

**投机模板**(EQ/LT/LE 适用 `FBArithStableNumber`):IsNumber × 2 guard → `ucomisd` / `fcmp` → 条件跳;`EQ` / `LT` / `LE` 都是「比较+条件跳」对(承 [../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §9 不变式 3:必跟 JMP),投机模板可把比较与下条 JMP 的条件跳**融合**——这是 P4 相对 P3 的额外密度收益(P3 受 Wasm 表达力约束,无法把比较直接接 JMP)。

**通用模板**:`TEST` / `TESTSET` 是真值测试(无 metamethod),不需投机——任何值的 truthy/falsy 是常数时间。直接发通用模板:NaN-box u64 与 nil/false 比对 + 条件跳。`EQ` 通用模板要处理 `__eq`(同类型对象的 metamethod)→ helper。

**与 P3 翻译表对位**:P3 §3.3 把 EQ 投机为 `f64.eq`,但**不能融合下条 JMP**(Wasm 没「比较+跳」单指令,要 `f64.eq` 出 i32 再 `br_if`)。P4 融合是 native code 密度优势。

### 3.3 表 IC 族(GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/NEWTABLE/SETLIST,编号 6-11、34)

**投机模板**(典型 GETTABLE,FBTableMono 命中):

```
;; GETTABLE 投机模板(amd64,FBTableMono 直达槽)
mov  rax, [rbx + 8*B]          ; load R(B) = 表 GCRef
mov  rcx, [icSlotBase + 0]     ; 编译期烧入的 IC 快照 tableRef
cmp  eax, ecx                  ; guard 1:tableRef 身份比对(同表)
jne  exit_pc_N
mov  edx, [tableHeader + GenOffset]
cmp  edx, [icSlotBase + 4]     ; guard 2:同代次
jne  exit_pc_N
mov  rax, [tableArrayBase + 8*SNAP_INDEX]  ; 命中:直达槽 load
mov  [rbx + 8*A], rax
```

`SETTABLE` 同款,但需多一步「写屏障」逻辑——望舒值世界全在 arena,无 Go GC 写屏障义务(承 [./05-system-pipeline.md](./05-system-pipeline.md) §1「白赚」),但**自管 GC 的 mark-sweep**仍可能要标记表为「脏」(承 [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md) §7)。

**通用模板**:`FBTableMega` / IC 失效 → `h_gettable` helper(P3 同款慢路径)。`NEWTABLE` 走 helper(分配是慢路径);`SETLIST` 多值边界(`B=0`/`C=0`)需跨 opcode 维护 top,直接走 helper。承 P3 PW5 同款保守裁决。

**与 P3 翻译表对位**:P3 PW5([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3.4)是 P3 翻译复杂度峰值——表 IC inline 固化跳哈希、失效降级走助手。P4 把 P3 的 `i64.load offset=8*SNAP_INDEX` 换成 `mov rax, [tableArrayBase + 8*SNAP_INDEX]`,本质同构。P4 额外能力:`MetatableMono` 投机(承 §3.9)、多代次 polymorphic IC(2-4 个 shape)。

### 3.4 控制流族(JMP/FORLOOP/FORPREP/TFORLOOP,编号 22、31-33)

**JMP**:翻译为机器跳。前向跳留 fixup(§2.2.1),反向跳直接发——反向跳是循环回边,**必须插回边 safepoint**。

**FORPREP / FORLOOP**(数值 for):FORPREP 三槽校验 + 减一个 step 跳到 FORLOOP;FORLOOP 加 step、判界、回跳并刷新外部循环变量 R(A+3)——是热点回边。模板形态(投机:三槽 number 假设已由 FBArithStableNumber 暗示):

```
;; FORLOOP 投机模板(amd64)
movsd xmm0, [rbx + 8*A]        ; idx
addsd xmm0, [rbx + 8*(A+2)]    ; idx += step
ucomisd xmm0, [rbx + 8*(A+1)]  ; cmp idx, limit
ja   loop_exit                 ; idx > limit → 退出循环
;; 回边 safepoint(§3.4 末):检查 preemptFlag
test [r15 + PreemptFlagOff], 1
jnz  safepoint_exit_stub
movsd [rbx + 8*A], xmm0        ; 写回 R(A) := idx, R(A+3) := idx
movsd [rbx + 8*(A+3)], xmm0
jmp  loop_body_start
loop_exit:
```

**TFORLOOP**(泛型 for):`R(A)(R(A+1), R(A+2))` 调迭代器,本质是调用族(§3.5)固定形态 → `HelperTForCall`(承 P3 同款 h_tforloop)。迭代器调用可能 growStack 重定位值栈段——helper 返回新 valueStack base(承 P3 PW4b base 刷新机制,P4 同构)。

**回边 safepoint**(承 [./05-system-pipeline.md](./05-system-pipeline.md) §3.5):**所有反向跳都必须插 safepoint**(2-3 条:load preemptFlag + test + branch to exit_stub)。这是 [../roadmap.md](../roadmap.md) §2 异步抢占税兑现——直线段长度有界 ⇒ 不可抢占窗口有界。短直线段不插(P2 F5 的大函数闸门已天然限制单函数尺寸)。

**与 P3 翻译表对位**:P3 PW4 用 wazero relooper 结构化生成(if/loop/br),P4 直接发机器跳——P3 的「结构化」约束在 native code 不存在(任意跳转都合法),P4 模板更直白。

### 3.5 调用族(CALL/TAILCALL/RETURN,编号 28-30)

**CALL 三向分派**(承 P3 §3.6):同 tier(jit)Lua 函数 / 解释 tier(crescent)Lua 函数 / host 函数。jit→jit 直跳(同 module 内 `call` 指令,~5ns 量级),失败 → OSR exit:

```
;; CALL A B C 模板(amd64)
mov  rax, [rbx + 8*A]                ; load 被调函数 R(A)
;; emitGuardClosureKind(rax, fastCallExpected) → fb 显示该点单态调用 jit 函数
mov  rcx, fastCallTargetEntry        ; 编译期烧入目标 codeEntry
;; emit Frame Setup(更新 CallInfo、装参)
call rcx                             ; jit→jit 直跳
;; emit Return Handling(从 CallInfo 拿 base、写回 R(A..A+C-2))
```

**TAILCALL**:复用解释器同款 helper(承 P3 PW6)——TAILCALL 在 BB 终结子位置(后随死代码 RETURN)→ jit 内不优化为直接跳目标(那要重写帧),走 `HelperTailCall` 把帧改造交解释器。收益:proper TCO 拿 O(1) 栈免费(承 P3 PW6 教训 2 红利)。

**RETURN**:把 R(A..A+B-2) 写到调用方期望位置(NaN-box memmove,承 04 §3.2)→ 跳回 trampoline 出口。

**与 P3 翻译表对位**:P3 PW6 `h_call` 是「Wasm→Go→Wasm」双跨层,产生 ~143ns 边界税(P3 PW10 R3 优化目标)。P4 jit→jit 直跳是同 module 内 `call` 指令,~5ns 量级——这是 P4 相对 P3 的核心收益区(承 [./02-template-direction.md](./02-template-direction.md) §1.3 dispatch 消除)。

### 3.6 闭包族(CLOSURE/CLOSE/GETUPVAL/SETUPVAL/VARARG,编号 36、35、4、5、37)

**CLOSURE / CLOSE**:复用解释器 helper(承 P3 PW7 决策)。`CLOSURE A Bx` → `HelperMakeClosure(Proto[Bx])` → 写 R(A);后随 nupvals 条伪指令(MOVE/GETUPVAL)由 emitOpcode 跳过(skip,err 协议,承 P3 PW7 教训 2)。`CLOSE A` → `HelperCloseUpvals(threshold=A)`。

**GETUPVAL / SETUPVAL**:直接 inline(upvalue cell 在闭包对象,经 closure 指针偏移寻址):

```
;; GETUPVAL R(A) := Upval(B) (amd64)
mov  rax, [rbx + 8*A_closureSlot]    ; 当前函数闭包 GCRef
mov  rcx, [rax + ClosureUpvalsOff + 8*B]  ; upvalue cell 指针
mov  rdx, [rcx + UpvalueValueOff]    ; 间接 load 值
mov  [rbx + 8*A], rdx
```

**VARARG**:**永不在白名单**(承 [./02-template-direction.md](./02-template-direction.md) §4 + [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) F1 排除)。F1 闸门把 vararg 函数标 `CompCompilable=false`,Proto 永不升层。emit default 分支防御性 panic(`unreachable: VARARG`)。

**与 P3 翻译表对位**:P3 PW7 同款机制——闭包是 GC 对象、open upvalue 指栈槽、close upvalue 指 cell。P4 inline GETUPVAL 比 P3 紧凑(P3 走 helper,P4 直接寻址)。

### 3.7 直线族(MOVE/LOADK/LOADBOOL/LOADNIL,编号 0-3)

无投机,纯语义直翻:

```
;; MOVE R(A) := R(B) (amd64):mov rax, [rbx + 8*B]; mov [rbx + 8*A], rax
;; LOADK R(A) := K(Bx):mov rax, ConstK_Bx_RawU64; mov [rbx + 8*A], rax  (NaN-box u64 64-bit 立即数)
;; LOADBOOL R(A) := bool(B); if C≠0 then pc++:mov [rbx + 8*A], BoolNanBox; (if C≠0) jmp pc+2
;; LOADNIL R(A..R(B)) := nil:范围小展开 mov 序列;大区间走 helper
```

**与 P3 翻译表对位**:P3 §3.1 同款。差异:LOADK 把 NaN-box u64 直接烧成 64-bit 立即数(P3 用 `i64.const`)。

### 3.8 渐进白名单(F7 闸门 + per-arch 进度对账)

`Emitter.SupportsAllOpcodes(proto)` 是 [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3.7 F7 闸门的 P4 实装——P4 后端开发期渐进扩充,**保守缺省**(不在白名单一律返 false)。类比 P3 PW1-PW9 的 `supported [numOps]bool` 数组([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1.3 / §5.2),P4 按 PJ(§6)渐进:

| PJ | 新增 opcode 集合(累积) |
|---|---|
| PJ0 | ∅ |
| PJ1 | + MOVE / LOADK / LOADBOOL / LOADNIL / RETURN |
| PJ2 | + 算术 + 比较(全 §3.1 / §3.2) |
| PJ3 | + JMP / FORLOOP / FORPREP / TFORLOOP |
| PJ4 | + GETTABLE / SETTABLE / GETGLOBAL / SETGLOBAL / SELF / NEWTABLE / LEN / CONCAT / SETLIST |
| PJ5 | + CALL / TAILCALL |
| PJ6 | + CLOSURE / CLOSE + GETUPVAL / SETUPVAL |
| PJ7-PJ9 | (全集 0..37 已支持,只剩 VARARG「不应到达」断言 + 双架构验收) |

**保守缺省的实装表达**(同 P3 §5.2):`Emitter.supported [numOps]bool` 数组初值全 false,每 PJ 在初始化里把对应 opcode 标 true。`SupportsAllOpcodes` 单遍扫 `proto.Code`,任一 OpCode 不在 supported 即返 false。**未识别 opcode 编号(38..63 预留区)统一返 false**。

per-arch 进度对账(amd64 / arm64 各自一份白名单):amd64 先行(§5.4 / §6.1 PJ1-PJ7),arm64 启动后(PJ8)逐 opcode 同步——任意时刻 amd64 / arm64 supported 集合可能不同步(arm64 滞后),这是「先 amd64 后 arm64」的代价(§5.4)。

### 3.9 各族的「投机模板 vs 通用模板」二选

承 [./03-speculation-ic.md](./03-speculation-ic.md) §2(待写)+ [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md) `TypeFeedback`:

| feedback Kind | 投机模板 | 通用模板 |
|---|---|---|
| `FBArithStableNumber` | f64 直算 + IsNumber × 2 guard | helper 调 doArithSlow |
| `FBTableMono` | 直达槽 + 同表同代次 guard | helper 调 doGetTable |
| `FBGlobalStable` | 常量化 / 直达 globals 槽 + globals 代次 guard | helper 调 doGetGlobal |
| `FBSelfMono` | inline 方法查找 + metatable 代次 guard | helper 调 doSelf |
| `FBUnstable` / `FBTableMega` | **不投机** | helper(等价解释器语义) |
| feedback 缺失 | **不投机** | helper |

裁决规则:① **confidence 阈值**:`fb[pc].Hits >= MinHits` 且 `fb[pc].MetaHits / fb[pc].Hits < MetaRatio` ⇒ 投机([../p2-bridge/01-profiling.md](../p2-bridge/01-profiling.md) §5 同款待定);② **去投机重编译**:若 Proto deopt 计数超阈值(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.4),重编译时所有投机点降级为通用模板;③ **per-arch 一致**:投机/通用裁决在架构无关骨架完成(§2.2.1 `chooseTemplate`),per-arch emitter 只看 `tmpl` 枚举发对应模板——避免 amd64 / arm64 投机决策不同步导致差分破裂。

---

## 4. 寄存器约定(per-arch 的核心决策)

寄存器约定是 per-arch 核心决策——决定模板内 load/store 形态、helper 调用 spill 量、trampoline 装载列表、与 Go runtime ABI 兼容性。本节给两架构具体方案 + Go ABI0 兼容位 + 典型 ADD 投机模板完整伪汇编对位。

### 4.1 amd64 寄存器分配方案

#### 4.1.1 寄存器分类总表(amd64)

| 分类 | 寄存器 | 用途 | Go ABI0 角色 |
|---|---|---|---|
| **jitContext 固定** | `r15` | 指向 jitContext(整个 P4 生命周期不动) | callee-saved(进 trampoline 时保存) |
| **arena base** | `r14` | arena[0] 字节地址(safepoint 间稳定) | callee-saved |
| **值栈 base** | `rbx` | 当前 thread valueStack[0] 字节地址 | callee-saved |
| **暂存通用** | `rax` `rcx` `rdx` `rsi` `rdi` `r8` `r9` | 模板内短暂持值,helper 调用前 spill | caller-saved |
| **暂存额外** | `r10` `r11` | 同暂存通用,优先级低 | caller-saved |
| **f64 快路径** | `xmm0..xmm3` | 算术/比较族 f64 直算 | caller-saved |
| **栈指针** | `rsp` | 自管机器栈 SP(trampoline 切换后) | trampoline 管 |
| **保留** | `rbp` `r12` `r13` | trampoline 内部 / 未来扩展 | callee-saved |

#### 4.1.2 jitContext / arena base / 值栈 base 三固定寄存器

`r15`(jitContext)整个 P4 生命周期不动,trampoline 入口装入。`r14`(arena base)在 safepoint(分配点 / 跨层调用)后可能刷新——分配慢路径 helper 返回后,trampoline 出口重 load `r14 := jitContext.arenaBase`。`rbx`(值栈 base)在 growStack 后可能刷新——`HelperGrowStack` 返回后重 load `rbx := jitContext.valueStack`。这是 P3 PW6 base 刷新机制的 P4 同构(承 P3 PW6 教训 1 / [`design-claims-vs-codebase-physics`](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) §2):跨层调用后值栈段可能搬家,陈旧 `rbx` 是 UAF 雷区。

模板内栈槽访问:`mov rax, [rbx + 8*B]` / `mov [rbx + 8*A], rax`(B/A 是编译期立即数);safepoint 检查:`test BYTE PTR [r15 + PreemptFlagOff], 1; jnz safepoint_exit_stub`;helper 调用经 jitContext 装参:`mov rdi, r15; mov rsi, [rbx + 8*A]; call [r15 + HelperTableOff + 8*HelperArithSlow]`。

#### 4.1.3 暂存寄存器池

每条模板内部短暂使用,**不跨字节码边界**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1 栈槽真相不变式 + [./02-template-direction.md](./02-template-direction.md) §1.5)。暂存分配策略:简单模板用 `rax` / `rcx`;复杂模板按固定顺序 `rax → rcx → rdx → rsi → rdi`,模板内不冲突就够;helper 调用前 spill——caller-saved 寄存器在 helper 调用后值丢失,若模板需跨 helper 保留某值,经 jitContext 临时槽传递(`jitContext.scratch[]`)。

不引入跨字节码边界的寄存器分配——这是模板编译相对优化编译器的根本简化(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1 推论)。

#### 4.1.4 amd64 ADD 投机模板(完整伪汇编)

```asm
;; ADD R(A), RK(B), RK(C) —— amd64 投机模板(FBArithStableNumber 命中)
;; 入口:rbx = valueStack base, r15 = jitContext base

    ;; --- guard:IsNumber × 2(NaN-box u64 单比较) ---
    mov   rax, [rbx + 8*B]              ; load RK(B) NaN-box u64
    mov   rcx, NumTagMask
    and   rax, rcx
    cmp   rax, NumTag
    jne   exit_pc_N                     ; guard 失败 → OSR exit(承 04 §6)

    mov   rax, [rbx + 8*C]              ; 同样查 RK(C)
    and   rax, rcx
    cmp   rax, NumTag
    jne   exit_pc_N

    ;; --- 算:f64 加 ---
    movsd xmm0, [rbx + 8*B]             ; 直接 load f64(NaN-box 通过 guard 后位重解释)
    movsd xmm1, [rbx + 8*C]
    addsd xmm0, xmm1

    ;; --- store 回 R(A) ---
    movsd [rbx + 8*A], xmm0
;; ~10 条指令
```

OSR exit `exit_pc_N` 的 stub(承 04 §6):

```asm
exit_pc_N:
    mov   DWORD PTR [r15 + ExitReasonOff], EXIT_GUARD_NUMBER
    mov   DWORD PTR [r15 + ExitPCOff], pc_N
    jmp   trampoline_exit               ; 退到 trampoline 出口,解释器接管
```

#### 4.1.5 与 Go ABI0 兼容性

P4 trampoline 入口(`enterJIT`)是 Go 调用栈上的普通汇编函数。Go ABI0 要求被调方保存:`rbx` `rbp` `r12` `r13` `r14` `r15` 和 SP。P4 把 `rbx` `r14` `r15` 都用于关键角色——必须在 trampoline 入口保存(push 到自管栈)、出口恢复:

```asm
;; trampoline_amd64.s —— 进入 stub
TEXT ·enterJIT(SB), NOSPLIT, $0-24
    PUSHQ rbx; PUSHQ rbp; PUSHQ r12; PUSHQ r13; PUSHQ r14; PUSHQ r15  ; 保存 callee-saved
    MOVQ  jitCtx+0(FP), r15
    MOVQ  ValueStackOff(r15), rbx
    MOVQ  ArenaBaseOff(r15), r14
    MOVQ  SelfStackTopOff(r15), rsp     ; 切到自管机器栈(承 05 §3.3)
    MOVQ  codeEntry+8(FP), rax
    JMP   rax                           ; 跳入代码入口
```

helper 调用进出 Go 世界时,trampoline 反向:把当前 SP / 寄存器状态保存到 jitContext 临时区 → 切回 Go SP → 调 Go 函数 → 切回自管 SP → 恢复寄存器。

### 4.2 arm64 寄存器分配方案

#### 4.2.1 寄存器分类总表(arm64)

| 分类 | 寄存器 | 用途 | Go ABI0 角色 |
|---|---|---|---|
| **jitContext 固定** | `x28` | 指向 jitContext | callee-saved(Go 上 G 寄存器,P4 内部用,trampoline 协议交换) |
| **arena base** | `x27` | arena[0] 字节地址 | callee-saved |
| **值栈 base** | `x26` | 当前 thread valueStack[0] 字节地址 | callee-saved |
| **暂存通用** | `x0..x9` | 模板内短暂持值 | caller-saved(AAPCS scratch + 参数寄存器) |
| **暂存额外** | `x10..x15` | 同暂存通用,优先级低 | caller-saved |
| **f64 快路径** | `v0..v3` | 算术/比较族 f64 直算 | caller-saved |
| **链接 / 帧** | `lr (x30)` `fp (x29)` | 函数调用链 / 栈帧 | trampoline 管 |
| **栈指针** | `sp` | 自管机器栈 SP | trampoline 管 |
| **保留** | `x16-x18` `x19-x25` | 平台保留 / 未来扩展 | x16/x17 IP / x18 平台寄存器 / x19-x25 callee-saved |

> **x28 = Go G 寄存器的特殊性**:Go arm64 ABI `x28` 是当前 G 指针。P4 自管世界期间 `x28` 改为 jitContext——trampoline 进入时保存原 G 指针到 jitContext 槽,退出时恢复。helper 调用回 Go 世界前必须切回 G(由 trampoline 出口处理)。这是 arm64 相对 amd64 多出的复杂度。

#### 4.2.2 jitContext / arena base / 值栈 base 三固定寄存器

同 amd64 三角色,arm64 偏移立即数范围:`ldr (immediate)` unsigned offset `0..32760`(8 字节倍数);P1 `MaxStack <= 250` 远小于 4096,不需大偏移。模板内栈槽访问:`ldr x0, [x26, #8*B]; str x0, [x26, #8*A]`。safepoint:`ldrb w0, [x28, #PreemptFlagOff]; cbnz w0, safepoint_exit_stub`。helper 装参:`mov x0, x28; ldr x1, [x26, #8*A]; ldr x2, [x28, #HelperTableOff + 8*HelperArithSlow]; blr x2`。

#### 4.2.3 暂存寄存器池

策略同 amd64:简单模板用 `x0`/`x1`,复杂模板按固定顺序 `x0 → x1 → x2 → ...`。`x0`-`x7` 同时是 AAPCS 参数寄存器——helper 调用前装参时这些寄存器自动用上,模板内不能跨 helper 保值。

#### 4.2.4 arm64 ADD 投机模板(完整伪汇编)

```asm
;; ADD R(A), RK(B), RK(C) —— arm64 投机模板(FBArithStableNumber 命中)
;; 入口:x26 = valueStack base, x28 = jitContext base

    ;; --- guard:IsNumber × 2(NaN-box u64 单比较) ---
    ldr   x0, [x26, #8*B]               ; load RK(B) NaN-box u64
    mov   x1, #NumTagMask               ; (实际是 movz/movk 序列)
    and   x0, x0, x1
    mov   x1, #NumTag
    cmp   x0, x1
    b.ne  exit_pc_N                     ; guard 失败 → OSR exit

    ldr   x0, [x26, #8*C]
    mov   x1, #NumTagMask
    and   x0, x0, x1
    mov   x1, #NumTag
    cmp   x0, x1
    b.ne  exit_pc_N

    ;; --- 算:f64 加 ---
    ldr   d0, [x26, #8*B]               ; 直接 load f64
    ldr   d1, [x26, #8*C]
    fadd  d0, d0, d1

    ;; --- store 回 R(A) ---
    str   d0, [x26, #8*A]
;; ~14 条指令(arm64 立即数装载多 movz/movk 步,密度比 amd64 略低)
```

OSR exit stub:

```asm
exit_pc_N:
    mov   w0, #EXIT_GUARD_NUMBER
    str   w0, [x28, #ExitReasonOff]
    mov   w0, #pc_N
    str   w0, [x28, #ExitPCOff]
    b     trampoline_exit
```

> **优化机会**:`NumTagMask` / `NumTag` 立即数若编译期已知,可经 literal pool(`adrp+ldr`)一次 load 到固定寄存器复用。但增加寄存器占用;实测后定。

#### 4.2.5 与 Go ABI0 兼容性

Go arm64 ABI 要求 callee-saved:`x19-x28` `x29(fp)` 和 `sp`。P4 用 `x26` `x27` `x28`——必须在 trampoline 保存:

```asm
;; trampoline_arm64.s —— 进入 stub(简化版)
TEXT ·enterJIT(SB), NOSPLIT, $0-24
    stp   x19, x20, [sp, #-16]!         ; 成对 push callee-saved
    stp   x21, x22, [sp, #-16]!
    stp   x23, x24, [sp, #-16]!
    stp   x25, x26, [sp, #-16]!
    stp   x27, x28, [sp, #-16]!
    stp   x29, x30, [sp, #-16]!
    ldr   x28, jitCtx+0(FP)
    ldr   x26, [x28, #ValueStackOff]
    ldr   x27, [x28, #ArenaBaseOff]
    ldr   x9,  [x28, #SelfStackTopOff]
    mov   sp,  x9                       ; 切到自管机器栈
    ldr   x9,  codeEntry+8(FP)
    br    x9
```

#### 4.2.6 icache flush(arm64 独有)

代码页 Seal(W^X 翻面前)调用:

```asm
;; arm64 icache flush 序列(每 64 字节缓存行执行一次)
flush_icache:                           ; x0 = 起始, x1 = 长度
    add   x2, x0, x1                    ; 结束地址
    mrs   x3, CTR_EL0
    ubfm  x3, x3, #16, #19              ; 缓存行字节数
    mov   x4, #4
    lsl   x3, x4, x3
1:  dc    cvau, x0                      ; 清 D-cache 到 PoU
    add   x0, x0, x3
    cmp   x0, x2
    b.lo  1b
    dsb   ish                           ; 数据屏障
    sub   x0, x2, x1
2:  ic    ivau, x0                      ; 失效 I-cache
    add   x0, x0, x3
    cmp   x0, x2
    b.lo  2b
    dsb   ish
    isb                                 ; 指令屏障
    ret
```

amd64 无此序列(硬件 SMC 检测保证 I-cache 一致性)。

### 4.3 共同约定

#### 4.3.1 edges / stack-spill 形态

无论 amd64 还是 arm64,跨字节码边界的「值的归宿」都是 arena 值栈槽——这是「栈槽真相不变式」(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1)。模板内寄存器只在单条模板内部短暂持值,模板结束前必经 store 写回栈槽。

#### 4.3.2 调用约定保留位

每架构的 Go ABI0 callee-saved 集合,P4 trampoline 在进入时全部保存到自管栈,退出时恢复:

| 架构 | callee-saved | 保存方式 |
|---|---|---|
| amd64 | `rbx` `rbp` `r12` `r13` `r14` `r15` | trampoline 入口 PUSH,出口 POP |
| arm64 | `x19-x28` `x29(fp)` `x30(lr)` | trampoline 入口 stp 成对 push,出口 ldp 成对 pop |

> Go 1.18+ arm64 引入 R28(G 寄存器)纪律:Go runtime 期望 G 在 R28——P4 trampoline 进入时把 G 备份到 jitContext.savedG,退出时恢复。这是 arm64 平台特有的「切世界」开销(amd64 无此约束)。

#### 4.3.3 helper 调用 ABI

helper 是 Go 函数(`internal/gibbous/jit/helpers.go`),P4 经 helper 表入口跳板调用——跳板是 per-arch 汇编 stub,负责:① 切回 Go SP(自管 SP 暂存到 jitContext.savedSP);② 装参到 ABI 寄存器(amd64 SystemV / arm64 AAPCS):`rdi/x0` = jitContext,`rsi/x1`..`r9/x7` = helper 参数(超过经栈传);③ 调 Go 函数;④ 切回自管 SP;⑤ 检查 helper 返回 status:非零 → 写 jitContext.exitReason 跳 trampoline 出口,零 → 返回模板继续。完整骨架在 [./05-system-pipeline.md](./05-system-pipeline.md) §3.4。

### 4.4 模板内寄存器存活范围

承 [./02-template-direction.md](./02-template-direction.md) §1.1 + [./04-osr-deopt.md](./04-osr-deopt.md) §3.1 不变式:**每条字节码边界处全部 Lua 活值已物化在 arena 值栈槽中**;机器寄存器只在单条模板内部短暂持值。推论:① 暂存寄存器在每条模板的开头视为「无值」;② 暂存寄存器在每条模板的结尾视为「丢弃」;③ 跨模板的状态承载者只有栈槽 / jitContext 字段 / 三个固定寄存器(但它们也是从 jitContext 派生)。

**例外**:某些 opcode 为性能在边界间短暂缓存值(如 FORLOOP 的循环变量驻留 xmm0 / d0)——承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1 注「实现自由度」:允许此类局部缓存,但相应 guard 的 exit 必须补一段「寄存器→栈槽」写回序列(每 exit 点编译期生成)。**架构决策:允许此类局部缓存,但 exit 物化序列必须编译期静态生成**(承 04 §3.3),杜绝运行期映射查询。

amd64 / arm64 在「locals 寄存器跨指令缓存」上的差异:amd64 xmm0-xmm7 + r10/r11 都可缓存;arm64 v0-v3 + x0-x9 + 寄存器更多但 G 寄存器协议占 x28——P4 内部缓存预算不受 G 约束(自管世界 x28 = jitContext)。具体哪些循环变量值得缓存,留实测决定(开放问题)。

---

## 5. 双架构测试纪律

本节展开双后端 + 双架构的测试纪律。

### 5.1 差分门禁双跑

P4 差分主防线是「同 Proto crescent vs gibbous-jit byte-equal」(承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 Runner 抽象)——P4 启动时新增 `WangshuGibbousJIT` runner,与既有 `WangshuCrescent` 在同输入下输出 byte-equal。

**CI 门禁要求**(承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §8 + [../architecture.md](../architecture.md) §4 不变式 2):① 每次 PR 必跑 difftest 全套——同 Proto 经 crescent / gibbous-wasm / gibbous-jit 三 tier 各跑一遍,逐字节比对;② **amd64 与 arm64 物理 runner 各跑全套**(§5.3);③ nightly fuzz(承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §5)叠加 P4 runner——P4 是望舒第一个会说谎的层(投机错误静默产错果),fuzz 是覆盖 deopt 路径的主要手段(承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.2)。

### 5.2 同 Proto crescent vs gibbous-jit byte-equal

差分口径(承 [./08-testing-strategy.md](./08-testing-strategy.md) 待写):**输入相同**(同 Proto.Code、同输入参数、同初始 globals 表);**输出 byte-equal**(返回值 / globals 修改 / 错误消息含位置 / traceback);**不开豁免**(承 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §0 同款)。

amd64 与 arm64 互为对照——两架构在同输入下输出仍 byte-equal:

```
crescent (Go)  ←─ byte-equal ─→  gibbous-jit/amd64 (native)
                                        ↕ byte-equal
crescent (Go)  ←─ byte-equal ─→  gibbous-jit/arm64 (native)
```

任何架构 jit 输出与解释器有差异,直接 fail。这把「投机模板正确性」与「per-arch 编码正确性」两层 bug 都暴露:投机模板 bug(architecture-independent)→ 两架构同时 fail;per-arch 编码 bug(架构特异)→ 一架构 fail 立即定位到具体 emitter。

### 5.3 交叉编译只能保证能构建,不能代跑差分

**纪律**:CI 必须有真 arm64 物理 runner——交叉编译(`GOARCH=arm64 go build`)只验证「代码能编」。理由:① arm64 icache flush 序列(§4.2.6)、内存模型差异(LDR/STR 弱有序、需 dmb 屏障)、原子操作语义,在 amd64 上模拟不了——只有真硬件能跑出 race condition;② wazero 项目同款 CI 矩阵(linux/amd64 + linux/arm64 + darwin/amd64 + darwin/arm64 全跑差分);③ **CI 不跑 = 实质未测**(承 [`prove-the-path-under-test`](../../../llmdoc/guides/prove-the-path-under-test.md) §1)。

**配套口径**(承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §8 CI 门禁):

| 平台 | 验收要求 |
|---|---|
| linux amd64 | 全 difftest + nightly fuzz + benchmark |
| linux arm64 | 全 difftest + nightly fuzz |
| darwin amd64 | 至少 difftest 主套(GitHub Actions 提供) |
| darwin arm64 | 至少 difftest 主套(MAP_JIT 平台特异性) |
| windows amd64 | 至少 build + 基本测试(VirtualProtect 平台特异性) |

### 5.4 先 amd64 后 arm64 的实施顺序

**先 amd64 后 arm64**:① **PJ0-PJ7**:amd64 单架构走通全管线(直线 → 算术 → 控制流 → 表 IC → 调用 → 闭包 → 端到端),每 PJ amd64 实装通过差分验收后才开下一档;② **PJ8**:arm64 后端启动——按已稳定骨架接口实装 emitter,逐 opcode 同步白名单;③ **PJ9**:arm64 端到端验收 + 双架构差分套接入 CI;④ **PJ10**:luajc 档验收。

amd64 先行的理由:① **生态成熟度**(大部分 Go 开发机是 amd64,debug 工具链完善);② **wazero 参照**(amd64 backend 历史更长、代码更稳定);③ **校准测量基线**([../roadmap.md](../roadmap.md) §1 在 Xeon amd64 上做)。

### 5.5 但骨架先行

**第一架构落地时按 §2 切分好接口,避免「amd64 写完再抽象」返工**。落地:① **PJ0 启动时就定 §2.4 emitter trait**:`Emitter` interface 完整签名,amd64.NewEmitter 是第一个实装,arm64.NewEmitter 是空骨架(panic stub);② **PJ1-PJ7 每加一个 opcode 模板,同步在 arm64.go 留 stub**——不实装,但保留方法签名,给 arm64 启动一个完整的「待填表」;③ **避免反模式**:把 amd64 特异性(如 RIP-relative 寻址)硬编码进 emitter trait —— arm64 没有 RIP-relative,得另搞接口。trait 签名要架构无关(用「LoadConstant」而非「mov reg, [rip+disp]」)。

### 5.6 arm64 验证抽象是否真架构无关

arm64 启动(PJ8)时,既有 amd64 实装是「镜子」——所有泄漏成本立即暴露:① 若 arm64 emitter 实装某 opcode 时发现 emitter trait 不够用(必须改签名),说明 amd64 实装时藏了架构假设——立刻回去修 trait + amd64,不在 arm64 里 workaround;② 若 arm64 实装某 opcode 比 amd64 少 / 多关键信息(如 NaN 规范化在 arm64 是 `fadd` 自动 / amd64 需手工)——这是 per-arch 模板该处理的,trait 不动;③ 若骨架的某机制(如 OSR exit 物化序列)在 arm64 性能不通(指令多 / 寄存器不够),回去改骨架的「物化逻辑」。

这是「先 amd64 后 arm64」相对「同时双架构」的关键收益——双架构同时写容易因 amd64 进度快被 amd64 假设污染骨架,**先单后双 + 骨架先行 + arm64 校验**是平衡。

### 5.7 arm64 滞后交付不阻塞 P4 验收

**arm64 滞后交付**:若资源紧张,arm64 滞后交付不阻塞 P4 验收。具体:**验收平台定 amd64**([../roadmap.md](../roadmap.md) §4 P4 验收在 amd64 跑过即算 P4 落地);**发布口径标注**(`gibbous/jit` 发布时如实标「P4 amd64 GA / arm64 beta(滞后)」,Linux/macOS arm64 用户回落到 P3 wasm tier 或 crescent 解释器);**不退化 difftest 矩阵**(arm64 build 仍要过,只是「每 PJ 的 arm64 验收」推后);**正式 GA 条件**:arm64 全 PJ 通过 + 双架构 nightly fuzz 跑 ≥30 天无差异。

---

## 6. 实装顺序与里程碑(per-arch 渐进交付)

### 6.1 PJ 编号(P-JIT)预设

类比 P3 PW0-PW10、P2 PB、P1 M——本文为 P4 预设 **PJ0-PJ10**(P-JIT):

| PJ | 名称 | 目标 | 验收 |
|---|---|---|---|
| **PJ0** | 架构选定 + 包骨架 | `internal/gibbous/jit/{,amd64,arm64}` 包骨架 + emitter trait + 空 Compile(SupportsAllOpcodes 恒返 false) | P4 build tag 下编译通过;P3-only 等价 |
| **PJ1** | amd64 trampoline + 直线模板 | trampoline 进出 stub + MOVE/LOADK/LOADBOOL/LOADNIL/RETURN | crescent vs gibbous-jit byte-equal(直线脚本) |
| **PJ2** | amd64 算术 + 比较模板 | 全 §3.1 / §3.2 投机模板 + IsNumber × 2 guard + 通用模板 + helper 接线 | 算术/比较脚本差分;FBArithStableNumber 投机命中 + guard 失败 OSR exit 走通 |
| **PJ3** | amd64 控制流 + FORLOOP + 回边 safepoint | JMP / FORLOOP / FORPREP / TFORLOOP + 反向跳 safepoint + 跳转回填 | 循环脚本差分;preemptFlag 触发 GC/调度 |
| **PJ4** | amd64 表 IC 模板(投机版) | FBTableMono 直达槽 + 同表同代次 guard + 通用模板 helper | 表 IC 脚本差分;IC 失效 → OSR exit;IC 命中证明走 fastpath |
| **PJ5** | amd64 CALL/TAILCALL + 跨层互调 + OSR exit | jit→jit 直跳 + jit→crescent 经 trampoline + jit→host helper + 错误冒泡 | 跨层调用脚本差分(承 P3 PW6 同款语料);OSR exit 物化无损 |
| **PJ6** | amd64 CLOSURE/CLOSE + upvalue | CLOSURE / CLOSE / GETUPVAL / SETUPVAL inline + skip 协议(承 P3 PW7) | 闭包脚本差分 |
| **PJ7** | amd64 端到端验收 + 性能基准 | 全 §3 模板 + V1-V13 差分语料 + V14-V18 性能/race 验收 | luasuite 全绿;Horner 校准 ≥ luajc 档 |
| **PJ8** | arm64 后端启动 + 同框架渐进 | arm64.NewEmitter + arm64 trampoline + icache flush + 全 opcode arm64 对位 + 双架构 build 通过 | arm64 单架构差分通过(逐 PJ 对位 amd64) |
| **PJ9** | arm64 端到端验收 + 双架构差分套 | 双架构 difftest 矩阵接 CI + nightly fuzz 双跑 + crescent vs gibbous-jit/amd64 vs gibbous-jit/arm64 三方 byte-equal | 双架构 nightly fuzz ≥30 天无差异 |
| **PJ10** | luajc 档验收 | 列内核负载 ≥ luajc 档(承 [./01-launch-judgment.md](./01-launch-judgment.md) §4 待写) | Horner 校准达标 + 真实生产负载抽样验证 |

### 6.2 各 PJ 验收口径预设(承 [./08-testing-strategy.md](./08-testing-strategy.md))

每 PJ 验收口径(承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §10 风格 + P3 PW9 V1-V18 模板):**正确性轴**(每 PJ 必跑):V-J1..V-J7 累积的 opcode 子集脚本差分;V-J8 OSR exit 物化等价;V-J9 deopt 注入模式(每 guard 强制失败一次);V-J10 GC 压力 fuzz 叠加 JIT。**性能轴**(PJ7 / PJ10):V-J11 Horner 校准 ≥ luajc 档(164μs 水位);V-J12 列内核 benchmark + 与 P3 / crescent 对照。**多 build / race 轴**(PJ7 / PJ9):V-J13 `-race` 全套通过;V-J14 四 build 矩阵各自完整。**双架构轴**(PJ8 / PJ9):V-J15 amd64 vs arm64 差分;V-J16 双架构 nightly fuzz ≥30 天无差异。具体口径在 [./08-testing-strategy.md](./08-testing-strategy.md) 待写,本节给 hint。

### 6.3 人月分解预设(承 [./01-launch-judgment.md](./01-launch-judgment.md) §3)

总预算 **+1-2 人年**(承 [./00-overview.md](./00-overview.md) §0.1)。预算分配预设(待 PJ0 spike 后修正):

| 阶段 | PJ | 人月预算 | 主要工作 |
|---|---|---|---|
| 启动 | PJ0 | 1 | 包骨架 + emitter trait 设计 + 编译驱动 + 代码页池 + jitContext + helper 表骨架 |
| amd64 实装 | PJ1-PJ6 | 6-8 | 38 opcode 模板 + 投机决策 + OSR exit + 跨层互调 + 闭包 |
| amd64 端到端 | PJ7 | 2-3 | 性能调优(投机阈值 / guard 合并 / 模板内寄存器分配优化)+ 差分加固 |
| arm64 实装 | PJ8 | 4-6 | arm64 emitter 实装 + 寄存器约定 + trampoline + icache + 全 opcode 对位 |
| arm64 端到端 | PJ9 | 1-2 | 双架构 CI + nightly fuzz + 文档 |
| luajc 档验收 | PJ10 | 1-2 | 真实生产负载抽样 + 性能调优收尾 |
| **合计** | | **15-22 人月** | (区间下限对应「无返工 + 骨架第一次就对」,上限对应「投机机制有重设计」) |

时间分布:**前 6 个月** PJ0-PJ4 amd64 直跑通(端到端基本路径绿),用 Horner 校准做中途闸门(承 [./01-launch-judgment.md](./01-launch-judgment.md) §4.3);**6-12 月** PJ5-PJ7 amd64 收尾 + 性能调优,通过 amd64 验收;**12-18 月** PJ8-PJ10 arm64 接入 + 双架构验收 + luajc 档收尾。

中途闸门(承 [./01-launch-judgment.md](./01-launch-judgment.md) §4.3):**单架构(amd64)+ 仅算术投机的最小 P4 先打通全管线并测 Horner 档位,若距 luajc 档仍远,立即停下重评**——这是「每阶段独立交付价值,任何闸门停下不亏」原则在 P4 内部的套用。

---

## 7. 不变式清单

承 P3 §8 / P4 主稿 §3.3 风格,P4 双后端不变式:

1. **「per-arch 是隔离实装,不漏抽象」**(承 §1.4 + §2.4):骨架不 import per-arch 子包;架构特异性(寻址模式、寄存器约定、icache、ABI)只在 per-arch 子包出现;trait 签名架构无关;骨架的所有逻辑(模板选型、guard 决策、exit 物化映射、代码页管理)架构无关。任何架构特异性出现在骨架中,是抽象漏洞,立即修。
2. **「双架构 CI 双跑」**(承 §5.1 / §5.3):amd64 与 arm64 物理 runner 各跑全套差分;交叉编译只验证「能编」不替代真机;arm64 滞后交付不阻塞 P4 验收(§5.7),但发布口径如实标注。
3. **「每族 opcode 的投机/通用二选靠 P2 feedback」**(承 §3.9):投机/通用裁决在架构无关骨架完成(`chooseTemplate`),per-arch emitter 只看 `tmpl` 枚举发对应模板——避免 amd64 / arm64 投机决策不同步导致差分破裂。feedback 缺失时保守发通用模板。
4. **「保守缺省的渐进白名单」**(承 §3.8):不在白名单的 opcode 一律返 false,VARARG 永不在白名单,未识别 opcode 编号(38..63 预留区)返 false;每 PJ 扩充一档,白名单按 PJ 进度对账。
5. **「栈槽真相」**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1 + [./05-system-pipeline.md](./05-system-pipeline.md) §3.6.1):每条字节码边界处全部 Lua 活值已物化在 arena 值栈槽中;模板内寄存器只在单条模板内部短暂持值;局部缓存(如 FORLOOP 循环变量)允许,但 exit 物化序列编译期静态生成。
6. **「GC 根天然共见,P4 不增机制」**(承 [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md) §5 R5 + [./05-system-pipeline.md](./05-system-pipeline.md) §1):gibbous-jit 帧的活跃寄存器**就是** thread 值栈槽,根枚举代码不需要额外感知 P4 的存在;jit→helper 跨边界时活值已在栈槽,GC 安全。
7. **「自管世界与 Go 世界严格隔离」**(承 [./05-system-pipeline.md](./05-system-pipeline.md) §3.3 + §4.1.5 / §4.2.5):JIT 码内不调用任何普通 Go 函数(经 trampoline);自管机器栈与 Go goroutine 栈分离;Go runtime 的 morestack / 抢占 / GC 不进入 JIT 世界。

---

## 8. 风险与开放问题

本文双后端架构相关风险:

### 8.1 风险

1. **arm64 维护矩阵长期固定成本**:双后端 + 双架构 CI 是长期固定成本——每 opcode 模板要双写、每性能优化要双跑、每 bug 修复要双 verify。缓解:严格执行 §5.5 骨架先行 + §5.6 arm64 校验,把「双写」收窄到 per-arch 模板内部。
2. **暂存寄存器池策略实测不利**(承 §4.1.3 / §4.2.3):若按固定顺序分配的暂存在复杂模板里频繁冲突(如表 IC 投机 5 个暂存 + helper 调用前 spill),性能可能不抵预期。缓解:单模板内允许「人工分配」(模板作者按需指定暂存寄存器),不引入跨字节码边界的寄存器分配器(那是 P5 的活)。
3. **locals 寄存器跨指令缓存的两架构差异**(承 §4.4):amd64 / arm64 寄存器数量与 ABI 占用不同,locals 缓存策略可能两架构最优解不同。缓解:per-arch 各自决定缓存策略,只要 exit 物化序列对位即可(承 04 §3.3 注 + [./02-template-direction.md](./02-template-direction.md) §3.3 待写)。
4. **arm64 平台子矩阵复杂**(承 §4.2.5 G 寄存器协议 + §4.2.6 icache + darwin MAP_JIT):arm64 有 linux / darwin / windows 三平台,每平台 syscall 适配 + W^X 翻面 + G 寄存器协议都不同。缓解:platform-specific 适配在 codepage_*.go 单独抽,emitter 不知道平台。
5. **指令编码器 bug 长尾**(承 §2.3 末):编码器是「写汇编」最低层,bug 难发现(产物是字节流,需 disassemble 才能验)。缓解:编码器单测「字节级对拍」(用 `golang.org/x/arch/x86/x86asm` 与 `arm64asm` 反汇编对拍真实指令字节)。

### 8.2 开放问题(记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **暂存寄存器池策略实测**(§4.1.3 / §4.2.3):固定顺序 vs 人工分配 vs 简化版图染色——PJ4 表 IC 模板压力测试后定。
- **locals 寄存器跨指令缓存的具体清单**(§4.4):FORLOOP 三槽是首选,其它循环局部热槽是否扩展、扩展到哪些——PJ7 性能调优期实测后定。
- **literal pool 策略**(§2.1 末 / §4.2.4 注):arm64 大立即数(NumTagMask 等)经 `adrp+ldr` 还是 `movz+movk` 序列——实测后定。
- **per-arch 模板的人工调优窗口**(§4.4 注):是否允许 per-arch 模板在保持差分一致下做架构特异性优化(如 amd64 用 LEA 复合寻址 / arm64 用 csel 条件选择)——PJ7 / PJ8 后评估。
- **Go 版本演进的影响**:Go ABI0 / G 寄存器协议 / 抢占机制随版本演进,callee-saved 集合可能变——CI 跑 Go 1.25 + 1.26 + tip(N-1 + N + tip,承本仓 `go.mod` 已 `go 1.26.2`,Go 官方支持窗口仅最新两个 minor;Go 版本演进时矩阵同步顺移),变化时立即修。

---

## 9. 回填请求

本文几乎全是 P4 自身机制(双后端架构、模板族、寄存器约定、PJ 里程碑、双架构 CI 纪律),与 P1 / P2 / P3 现稿无回填需要。

可能的下游回填(P4 文档集内):

- **[./04-osr-deopt.md](./04-osr-deopt.md) §6 待写**:本文 §3.x 多次援引 04 §6「exit stub 物理形态」——04 §6 应给 amd64 / arm64 各自的 exit stub 完整伪汇编(与本文 §4.1.4 / §4.2.4 ADD 投机模板的 exit stub 形态对位)。
- **[./05-system-pipeline.md](./05-system-pipeline.md) §3.3 / §3.4 待写**:本文 §4.1.5 / §4.2.5 援引 05 §3.3 / §3.4 trampoline 协议骨架——05 应给「helper 调用 ABI / 进/出/exit 三种出口」的架构无关骨架。
- **[./08-testing-strategy.md](./08-testing-strategy.md) 待写**:本文 §5 / §6.2 援引 08「差分双架构双跑 + V-J1..V-J16 验收口径」——08 应给完整 V 编号表与具体测试组织。
- **[./03-speculation-ic.md](./03-speculation-ic.md) §2 待写**:本文 §3.9 援引 03 §2「投机模板 vs 通用模板」二选总论——03 应给 feedback Kind 与投机决策的完整对位表。
- **[./02-template-direction.md](./02-template-direction.md) §1.5 / §3.3 待写**:本文 §4.4 援引 02 §1.5「模板内寄存器存活范围」与 §3.3「locals 寄存器跨指令缓存」——02 应给模板编译的总体方向。

PJ7 / PJ8 / PJ10 验收期发现具体收益/瓶颈数据,回填到 §6 PJ 验收口径与 §8.2 开放问题。

---

相关:
[./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化序列与 exit stub 物理形态——本文 §3.x guard 行链至此) ·
[./05-system-pipeline.md](./05-system-pipeline.md)(trampoline 协议骨架 / W^X / 代码页 / jitContext / helper 表——本文 §4 给两架构具体寄存器分配) ·
[./08-testing-strategy.md](./08-testing-strategy.md)(差分双架构双跑 + V-J 验收口径——本文 §5 给纪律,08 给口径) ·
[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md)(38 opcode 完整表,本文 §3 按族归类的源) ·
[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box 编码 / GC 根 R5——P4 寄存器持值的形式) ·
[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(CallInfo / 值栈布局——per-arch 寄存器映射的源) ·
[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象 / §8 CI 门禁 / §10 验收口径——双架构双跑接入点) ·
[../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md)(TypeFeedback shape——§3.9 投机/通用二选的源) ·
[../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md)(F1-F7 闸门——§3.8 渐进白名单的对位) ·
[../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md)(P3 翻译器主体——本文 §3 各族「与 P3 翻译表对位」的对位源) ·
[../roadmap.md](../roadmap.md)(§0 纯 Go / 禁 cgo / §2 四项税 / §7 prior art) ·
[../architecture.md](../architecture.md)(§1 包布局 / §2 月相 tier 映射 / §4 三不变式)