# P4-10 逐 opcode 翻译器(per-op translator)——PJ10 工程方向

> 状态:**详细设计**(PJ10 启动文档,2026-06-30)。本文是 PJ10 工程的单一事实源——把 P4 从「按函数样子识别 + 写死字节级模板」彻底换成「逐 opcode 翻译,任意形态的 Proto 都能升 P4」。
>
> 上游契约:[00-overview](./00-overview.md)(P4 总览;本文是 §0 文档地图新增一行 10)、[01-launch-judgment](./01-launch-judgment.md)(P4 立项判定依然成立,本文是「方向修正」非「立项再议」)、[02-template-direction](./02-template-direction.md)(原方向裁决:JSC Baseline / V8 Sparkplug per-function 模板编译;本文承认其大方向,**对「形态识别 + 字节级模板」的具体落地方式做修正**——§1.3 详)。
>
> P1 依赖面:[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(源 ISA 完整定义,本文输入)、[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(解释器主循环,P4 翻译产物与之 byte-equal)。
>
> P3 对照面:[../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md)(P3 wasm 已经是「逐 opcode 翻译」的范式;本文继承其翻译框架——CFG + relooper + 逐 opcode emit + 渐进白名单——只把发射目标从 wasm 字节码换成 amd64/arm64 原生码)。
>
> P5 下游:[../p5-trace-jit](../p5-trace-jit.md)(trace JIT 的前置假设是「baseline 层能为任意 opcode 发射机器码」,本文交付的就是这层;PJ10 完成后 P5 立项的基础设施就齐了)。
>
> P4 内部:[03-speculation-ic](./03-speculation-ic.md)(投机 + IC;本文承担「逐 op emit」基线,投机模板套在 emit 顶上)、[04-osr-deopt](./04-osr-deopt.md)(OSR exit 协议,emit 函数遇到 guard 时生成 exit stub 经此回栈)、[05-system-pipeline](./05-system-pipeline.md)(四项税 / mmap+W^X / trampoline,本文复用)、[06-backends](./06-backends.md)(双后端寄存器约定;本文新增 per-op emit 函数也按 §3 的 ABI 写)。

对应 Go 包:`internal/gibbous/jit/{amd64,arm64}`(per-arch emit 函数 + 共享翻译器骨架,详 §3)。

---

## 0. 一句话定位

**把每条 Lua opcode 单独翻译成原生机器码,串成函数体——任意形状的 Proto 都能升 P4,不再受「字节级模板」白名单限制。** 这与 P3 wasm 翻译器([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md))同款思路,只是发射目标从 wasm 字节码换成 x86-64 / aarch64 原生码。

---

## 1. 为什么换方向(PJ0-PJ9 的字节级模板路径走到了天花板)

### 1.1 PJ0-PJ9 现状(2026-06-30)

PJ0-PJ9 的设计是 **「按函数样子识别 + 写死字节级模板」**:`analyzeShape(*Proto)` 返一个 `shapeInfo`,扫描函数字节码看「长得像不像」预定义的几种形态:

- 单 BB「值产生 + RETURN A 1/2」(LOADK/LOADBOOL/LOADNIL/MOVE/GETUPVAL 等十几种)
- 二段算术链 + RETURN(`return a + b * c`)
- FORLOOP 空 body / 单条 body / 二段 body 字节级 inline
- 表 IC ArrayHit / NodeHit(getter / setter / SELF method 六模板)
- CALL void / TAILCALL / SELF + CALL spec template(0-7 参 × N=2-15 返值 × 双 receiver)

每种形态对应一段手写汇编模板,长度 ~70-200 字节,Compile 时 mmap 一块可执行内存,把模板字节码拷进去,跳进去执行。

**这套设计在自己适配的形态上很厉害**——V14 luajc 档实测 12.6× / 25.1× / 13.5× over gopher-lua,远超 4.4× 基线([implementation-progress §8](./implementation-progress.md)),完整兑现了 P4 立项动机([01-launch-judgment §4](./01-launch-judgment.md))。

### 1.2 撞墙现象:覆盖率被「形态命中」卡死

但 V15b heavy bench(2026-06-30 加入,见 [09-acceptance-checklist §3.V15b](./09-acceptance-checklist.md))实测显示:

| 脚本 | kernel 形态 | P4 升层数 |
|---|---|---|
| heavy_arith | `for i=1,n do local x = i*1.5; s = s + x*x - x/2 + 0.25 end` | **0** |
| heavy_recursion | `if n == 1 then return s; if n%2==0 then return f(n/2,s+1); return f(3n+1,s+1)` | **0** |
| heavy_floatloop | `while i<256 do ... ; i = i+1 end` 嵌套 FORLOOP | **0** |

三个 kernel 全升 0,落回 P1 解释器。原因:

1. **`for i=1,n do` 内 body 多段算术 + 跨段累加** — 不在 PJ3 FORLOOP「空 body / 单 reg-K body / 二段 reg-K body」白名单内
2. **自递归 + 多 `if-then-end` 分支** — 不在 PJ5 CALL void / TAILCALL / SELF 形态白名单内
3. **`while` 单条件循环 + 嵌套 FORLOOP** — 没有对应字节级模板

字节级模板每加一种,工程量 1-3 天(写 amd64 模板 + arm64 模板 + 字节级单测 + 双轨 byte-equal + difftest 角落语料)。Lua 函数样子组合无穷,**这条路有天花板,而且天花板比想象中低**——heavy 三个脚本是写得很普通的浮点循环,就已经全跌穿。真实宿主代码里更复杂的循环嵌套、多分支、表/字符串混合操作,几乎全跌。

### 1.3 PJ0-PJ9 的物理基础保留,只换发射策略

注意:**PJ10 不是把 PJ0-PJ9 推倒重来**。PJ0-PJ9 交付的物理基础全部保留:

| PJ0-PJ9 交付物 | PJ10 复用方式 |
|---|---|
| `internal/gibbous/jit` 包骨架 + build tag 互斥 + bridge.P3Compiler 注入接口 | 完全保留(P4 还是「一个 P3Compiler 实现」) |
| amd64 mmap / W^X / icache flush / trampoline 四件套([05-system-pipeline](./05-system-pipeline.md))| 完全保留 — per-op 翻译产物还是一段可执行字节码,落进同款 mmap 段 |
| arm64 codepage + trampoline + MAP_JIT([06-backends](./06-backends.md))| 完全保留 |
| `p4Code` / `Run` 接口([../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md))| 完全保留 |
| 14 个 host helper([implementation-progress §7](./implementation-progress.md))| **大部分保留**,逐 op 翻译时直接 call 这些 helper 处理类型多态慢路径 |
| OSR exit 协议 + `p4SpecState[proto]` 子状态机([04-osr-deopt](./04-osr-deopt.md))| 完全保留 — guard 失败时仍走 OSR exit |
| 投机模板 + IC inline([03-speculation-ic](./03-speculation-ic.md))| **保留作 fast path** — 见 §6 |
| 字节级模板(PJ0-PJ9 的全部产物 ~25+ 形态)| **保留作 fast path** — 见 §6 |

**变的只有一件事**:`SupportsAllOpcodes` 不再是 `analyzeShape(proto).ok`,而是 P3 wasm 那种 `supported [numOps]bool` 白名单 — 一旦每个 op 都接,任何 Proto 都能升 P4。

### 1.4 [02-template-direction](./02-template-direction.md) 是否被推翻

不被推翻。02 文档的核心裁决是:

1. **JSC Baseline / V8 Sparkplug 风格 per-function 模板编译** — 这条仍然成立。「模板」在 02 的语境里指「按方法/函数为单位编译」,与「按 trace 编译」(JIT 设计的两大分类)对立。本文承担的「逐 op 翻译」也是按函数为单位的。
2. **不上 IR、不上跨指令 regalloc** — 这条也成立。逐 op 翻译每个 op 单独 emit,寄存器约定固定,不做 IR / 不做活跃性分析 / 不做跨 op 寄存器复用。
3. **F1-F7 闸门继续生效** — 完全成立。本文实际上让 F7 真正发挥作用(原来 PJ0-PJ9 的 F7 是 `analyzeShape.ok`,本文改成「所有 op 都在 supported 集」)。

被修正的是 **02 §4「模板的具体物理形态」**:02 §4 倾向「整函数一段连续机器码,emit 时按形态查表填模板」,本文修正为「emit 时按 opcode 串行翻译,每个 op 独立 emit 一小段」。**这是 02 在 spike 前未亲手验证的细节**,02 §1.2 也明示「具体形态留 spike 后定」,本文是 spike 后的兑现。

---

## 2. 与 P3 wasm 翻译器的对照(直接借鉴)

P3 wasm `internal/gibbous/wasm/translate.go` 已经实装了「逐 op 翻译」的全套基础设施。PJ10 的核心捷径是 **借鉴 P3 的翻译框架,只换发射目标**。对照表:

| 翻译器组件 | P3 wasm 实装 | P4 PJ10 设计 |
|---|---|---|
| **CFG 构造** | `cfg.go::buildCFG` — 扫一遍 leader,切 BB,linkSuccs | **完全复用方法论**;P4 单独实现,但 BB 划分规则与 P3 一致(JMP/FORLOOP/FORPREP/RETURN/EQ/LT/LE/TEST/TESTSET/TFORLOOP/LOADBOOL 切 BB)|
| **可达性 + 可约简性** | `reachableBlocks` + `analyzeRelooper.isReducible` | **复用算法**;P4 同样要求 reducible CFG(不可约简则 SupportsAllOpcodes 返 false 拒升)|
| **结构化控制流** | `relooper.go` — 把 reducible CFG 还原成 if/loop/block 结构,wazero 接 wasm 结构化指令 | **不需要还原结构** — 原生码可以直接发 jmp/jcc(条件跳/无条件跳),无需 if/loop 结构化指令。直接按 BB 顺序 emit,BB 间用 label + jmp 链接,比 P3 简单 |
| **逐 op emit** | `translate.go::translateOp` switch on `bytecode.Op` | **完全复用 switch 结构**;每个 case 调对应 emit 函数 |
| **emit 函数** | `emit_*.go` — 一个 op 一个文件,产 wazero 字节码序列(api.ValueType 操作数 + opcode 字节)| **per-arch 各一份**:`amd64/emit_*.go` 产 x86-64 字节;`arm64/emit_*.go` 产 aarch64 字节 |
| **value stack 抽象** | P3 用 wasm 内嵌的 value stack(`local.get`/`local.set` 读写)| P4 直接用 **`R(A)` 槽内存地址寻址**(寄存器+偏移,不缓存值到 cpu reg),与 P1 解释器同款槽语义 |
| **safepoint 布点** | 回边 + 调用前后([../p3-wasm-tier/05](../p3-wasm-tier/05-safepoint-gc.md))| 同款规则,emit 时按 op 类型插入 safepoint check(已有 EmitSafepointCheck) |
| **跨层互调** | `h_call` / `h_tailcall` helper(已实装)| 仍走 host helper,emit `CALL` 时发一段「准备参数 + call 助手」的字节序列 |
| **IC 快照固化** | `emit_table.go` 经 P2 IC slot 直达槽 | 复用 PJ4 字节级 IC 模板(`emitter_pj4.go`)作 fast path,具体见 §6 |
| **`SupportsAllOpcodes`** | `compiler.go::SupportsAllOpcodes` — 渐进白名单 + 字符串常量拒 + 死代码不数 + reducible 校验 | **完全相同的算法**,只是 supported 数组随 PJ10a..d 扩张节奏不一样 |

**结论**:P3 wasm 已经把「逐 op 翻译器」整套的工程问题都解过了——CFG / relooper / 死代码 / 字符串常量 / safepoint / IC / 跨层互调,都有现成答案。**PJ10 不需要重新发明,只需要在 amd64 / arm64 端写每个 op 的 emit 函数,把对应的字节序列发出来**。

---

## 3. 架构总图

```
┌──────────────────────────────────────────────────────────────────┐
│                  P4 Compiler.Compile(*Proto)                     │
└──────────────────────────────────────────────────────────────────┘
       │
       │ ① analyzeShape(proto) 还在,但变成 fast path 判定:
       │    匹配 PJ0-PJ9 字节级模板 → 走 specialized 路径(性能极致)
       │    不匹配 → 走 ② 通用 per-op 翻译
       │
       ├──── fast path ────────────────────────────┐
       │     PJ0-PJ9 spec templates 保留           │
       │     - PJ2 投机算术 reg-reg/reg-K/chain-KK│
       │     - PJ3 FORLOOP 字节级 inline           │
       │     - PJ4 表 IC 六路径                    │
       │     - PJ5 CALL void / SELF spec templates │
       │                                           │
       └──── general path (PJ10 新增) ─────────────┤
                                                   │
       ② buildCFG(proto) — 切 BB,linkSuccs        │
       ③ reach + reducibility check                │
       ④ 按 BB rPostOrder 顺序 emit:               │
          for each BB:                             │
            emit_bb_label(bb.id)                   │
            for pc in [bb.startPC, bb.endPC):     │
              emit_<op>(proto, pc)                 │
            emit_terminator(bb)                    │
       ⑤ resolveLabels — 补 jmp 偏移               │
                                                   │
       └───────────────────────────────────────────┘
                                                   │
                                                   ▼
                              ┌──────────────────────────────────┐
                              │ wireToCodepage                   │
                              │ - mmap RW                        │
                              │ - 写字节                         │
                              │ - mprotect RX (linux) /          │
                              │   pthread_jit_write_protect_np   │
                              │   (darwin)                       │
                              │ - icache flush                   │
                              │ - 返 p4Code{entry, ...}          │
                              └──────────────────────────────────┘
```

**关键设计点**:

- **双轨**:`analyzeShape` 不删,作 **fast path 入口**。匹配字节级模板 → 走极致优化路径(PJ0-PJ9 全套);不匹配 → 走通用 per-op 翻译(PJ10 新增)。这样 V14 luajc 档(列内核形态命中 spec template)不丢精度,V15b heavy 形态走通用路径也能升 P4。
- **不引入 IR**:每个 op 直接发 amd64/arm64 字节,不经任何中间表示。寄存器约定固定(详 §4),不做活跃性分析、不做跨 op 寄存器复用。
- **per-arch 双份 emit**:amd64 一份、arm64 一份,跟现有 `internal/gibbous/jit/amd64/` 和 `internal/gibbous/jit/arm64/` 结构对齐。共享部分(CFG / label resolver / supported 表)在 `internal/gibbous/jit/` 主包。

---

## 4. 寄存器 ABI(amd64 端)

承 [05-system-pipeline §4](./05-system-pipeline.md) + [06-backends §3](./06-backends.md) 已有的寄存器约定,PJ10 通用 per-op 翻译沿用同款 ABI,无新增:

| 寄存器 | 用途 | 跨 op 稳定性 |
|---|---|---|
| `R14` | `vsBase` — 值栈基地址(`R(0)` 在内存里就是 `[r14+0]`)| 永远稳定,emit 不动 |
| `R15` | `jitCtx` — host helper 跨层指针 | 永远稳定,emit 不动 |
| `RAX` | 临时寄存器 / RETURN 返值 | 单 op 内可任意用,跨 op 不保证 |
| `RCX` / `RDX` / `RSI` / `RDI` | 临时寄存器 / host helper SysV ABI 参数 | 同上 |
| `XMM0-3` | 浮点临时 | 同上 |
| `RBP` / `RSP` | 帧指针 / 栈指针 | trampoline 进入时建立帧,emit 不破坏 |

**重要约束**(承 PJ0-PJ9):

1. **不在寄存器里缓存 `R(A)` 值跨 op**:`R(A)` 永远存值栈槽 `[r14+A*8]`,每个 op 自己 load / store。即「**guard 即栈槽真相**」([04-osr-deopt §3](./04-osr-deopt.md))不变,OSR exit 时栈槽就是真相,不需要物化序列。
2. **safepoint check 不破坏 RAX/RCX**:safepoint check 用「`cmpq $0, gcPending(R15) + jne slow_path`」固定序列,RAX/RCX 在此前后必须一致(若 emit 当前 op 用了 RAX,emit 完该 op 再 emit safepoint check)。
3. **host helper 调用前后 RAX/RCX 视作 clobbered**:emit 一段 `call h_xxx` 后,所有 caller-saved 寄存器(RAX/RCX/RDX/RSI/RDI/R8-R11 + XMM0-15)视为不定,后续 op 重新 load。

arm64 端寄存器映射(承 PJ8):`X26 = vsBase`,`X27 = jitCtx`,`X0-X7` 调用约定参数,其余按 AAPCS64。具体在 [06-backends §5](./06-backends.md) 已字面化,本文不重抄。

---

## 5. opcode 翻译表(完整 0..37)

按 P3 PW2-PW7 渐进白名单结构,PJ10 拆成 4 个里程碑(PJ10a-d),累积扩 supported 集:

### 5.1 PJ10a 直线 opcode(MOVE / LOADK / LOADBOOL / LOADNIL / GETUPVAL / SETUPVAL / JMP / RETURN)

| op | A/B/C 语义 | amd64 emit 形态(伪汇编)|
|---|---|---|
| MOVE | R(A) := R(B) | `mov rax, [r14+B*8]; mov [r14+A*8], rax` |
| LOADK | R(A) := K(Bx) | `mov rax, imm64; mov [r14+A*8], rax`(K 编译期烧 imm64;字符串常量同 P3 拒 — F7 守门)|
| LOADBOOL | R(A) := bool(B);if C: pc++ | `mov rax, IMM_TRUE_OR_FALSE; mov [r14+A*8], rax;` C != 0 时多发一条 jmp 到 pc+2 的 BB 入口 |
| LOADNIL | R(A..B) := nil | 循环 emit `mov [r14+i*8], NIL_BITS`(B-A+1 次) |
| GETUPVAL | R(A) := U(B) | call host.GetUpval(B);RAX → `[r14+A*8]` |
| SETUPVAL | U(B) := R(A) | `mov rdi, [r14+A*8]; call host.SetUpval(B, rdi)` |
| JMP | pc += sBx | 由 BB terminator 处理 — emit `jmp <bb_label_of_target>` |
| RETURN | 出口 | call host.DoReturn(A, B);ret |

**关键**:这一档全是「值在内存里搬来搬去」+ 偶尔调 helper,不涉及类型判断、不涉及 guard。是 PJ10 最简单的一档,先打通 fully end-to-end pipeline。

### 5.2 PJ10b 算术 + 比较(ADD/SUB/MUL/DIV/MOD/POW/UNM/LEN/CONCAT/NOT/EQ/LT/LE/TEST/TESTSET)

每个算术 op 两条路:

- **fast path**:reg-reg / reg-K / chain-KK 形态 → 跑 PJ2 已有的字节级投机模板(IsNumber guard + SSE2 算术 + 失败 OSR exit)
- **slow path**(本档新增):任意形态 → emit 「load R(B), load R(C), call host.Arith(op, b, c), store R(A)」

emit 函数签名(amd64):

```go
func emitADD(buf *codeBuffer, A, B, C uint8)
func emitSUB(buf *codeBuffer, A, B, C uint8)
// ...
```

比较 op(EQ/LT/LE)既要 emit 算术比较又要 emit 条件跳:

```go
func emitLT(buf *codeBuffer, A uint8, B, C uint16, falseTarget BBLabel)
//    emit:
//      mov rdi, [r14+B*8]
//      mov rsi, [r14+C*8]
//      call host.LessThan
//      cmp eax, A     ; A 是 cmpA 字段
//      je <falseTarget>
//      ; fall through to next BB (= jumpTarget of JMP)
```

### 5.3 PJ10c 控制流 + 循环(FORPREP / FORLOOP / TFORLOOP)

- **FORPREP fast path**:全常量 init/limit/step → PJ3 字节级 inline(已有)
- **FORPREP slow path**(本档新增):任意形态 → emit 「load + call host.ForPrep + 条件跳」
- **FORLOOP fast path**:空 body / 单 reg-K body / 二段 reg-K body → PJ3 字节级 inline(已有)
- **FORLOOP slow path**(本档新增):任意 body → emit 「idx += step; cmp limit; jmp back-edge」(idx/limit/step 都是 SSE float;back-edge 前插 safepoint check)
- **TFORLOOP**:emit 「call host.TForLoop;条件跳」

### 5.4 PJ10d 表 + 函数调用(GETTABLE / SETTABLE / GETGLOBAL / SETGLOBAL / SELF / NEWTABLE / SETLIST / CALL / TAILCALL / CLOSURE / CLOSE)

每个表 op / 调用 op 两条路:

- **fast path**:命中 PJ4 字节级 IC 模板 / PJ5 CALL spec template → 走 PJ4/PJ5 spec(已有)
- **slow path**(本档新增):任意形态 → emit 「load + call host.GetTable/SetTable/CallBaseline/...」

CALL 形态特别注意:`B == 0`(参数到 top)/ `C == 0`(返回到 top)的多值窗口形态,emit 「准备 vararg 头 + call host.CallBaseline 处理 nargs/nresults 不定参形态」。

### 5.5 VARARG 永不接(同 P3)

VARARG 经 P2 F1 闸门拒升([../p2-bridge/03 §3.1](../p2-bridge/03-compilability-analysis.md)),本档不实装。supported[VARARG] 永远 false。

---

## 6. fast path 与 slow path 的协作

P4 PJ10 最重要的工程纪律 — **不重写 PJ0-PJ9,只在外面套通用路径**:

```
SupportsAllOpcodes(proto):
    # PJ10 新逻辑(承 P3 wasm 同款):
    cfg = buildCFG(proto)
    if not all_ops_in_supported(cfg, reachable=True): return False
    if not reducible(cfg): return False
    if any LOADK is string-const: return False  # 同 P3
    return True

Compile(proto):
    # ① 试 fast path(PJ0-PJ9 字节级模板,V14 luajc 档由此守住)
    if shape := analyzeShape(proto); shape.ok:
        return emitFastPathFromShape(shape)

    # ② 通用 per-op 翻译(PJ10 新增)
    cfg := buildCFG(proto)
    buf := newCodeBuffer()
    emitProlog(buf)
    for bb in rPostOrder(cfg):
        buf.bindLabel(bbLabel(bb.id))
        for pc := bb.startPC; pc < bb.endPC-1; pc++:
            emitOp(buf, proto.Code[pc])
        emitTerminator(buf, cfg, bb)
    emitEpilog(buf)
    buf.resolveLabels()
    return wireToCodepage(buf)
```

这样:

- **V14 luajc 档**:`for i=1,1000 do end` 形态命中 PJ3 FORLOOP spec template,走 fast path,保持 12-25× over gopher
- **V15b heavy**:`heavy_arith` 等形态 `analyzeShape` 不命中,走通用 per-op 翻译,虽然单 op 性能逊于字节级 spec,但**至少能升 P4 跑起来**,不像现在一样落回 P1

**预期 V15b heavy 上 P4 通用路径性能**:理论上 ≥ P3 wasm(因为 wasm 还要经 wazero 二次编译,P4 直接发原生码;且不经 wasm linear memory 跨层税),比 P1 解释器快 1.5-2.5× 是合理预期。具体数字 PJ10 完成后实测。

---

## 7. label / 跳转的处理(原生码 vs wasm 的关键差异)

P3 wasm 有 `block`/`loop`/`if-else`/`br`/`br_if` 五种结构化控制流指令,wasm 验证器要求嵌套合规 — 所以 P3 要 relooper 把 reducible CFG 还原成嵌套结构。

P4 原生码没有结构化指令,直接发条件跳(`jcc`)和无条件跳(`jmp`)。流程:

1. **第一遍 emit**:遇到跳目标 BB 不在当前位置时,emit `jmp 0x00000000`(占位 32-bit 偏移),记录这个 `jmp` 的 32-bit 偏移位置 + 目标 BB 标号到 `fixupList`
2. **绑定 BB label**:emit BB 的第一条 op 前,记录「BB X 的入口偏移 = 当前 buf 长度」到 `labelMap[X]`
3. **第二遍 resolve**:遍历 `fixupList`,把占位偏移填成 `labelMap[targetBB] - (jmpInstrEnd)`(32-bit 有符号偏移)

这是经典的 **two-pass assembler** 套路,工程量小,1-2 天就能写好框架(amd64 端,arm64 端同款思路用 4-byte 立即数)。

P4 不需要 relooper,因为原生码不要求结构化 — **比 P3 简单**。但仍要求 CFG reducible:reducible 是「跳目标都是 leader」的等价条件,non-reducible 意味着「跳到 BB 中间」,这种情况只发生在 Lua 的 `goto`,而 luac 5.1.5 不生成 `goto`(5.2+ 才支持),所以 wangshu 的 luac 编出的所有 CFG 都 reducible,F7 守门即可。

---

## 8. supported 数组的渐进扩张

承 P3 PW2-PW7 节奏,PJ10 拆成 4 个 sub-PJ:

| Sub-PJ | 新增 supported(累积) | 工程量 | 验收 |
|---|---|---|---|
| **PJ10a** | 直线 opcode(8 个):MOVE / LOADK / LOADBOOL / LOADNIL / GETUPVAL / SETUPVAL / JMP / RETURN | 1-2 周 | end-to-end pipeline:从 `Compile` 到 `wireToCodepage` 到 `Run`,跑通最简 Proto(`return 42`)+ byte-equal P1 |
| **PJ10b** | 算术 + 比较 + UNM/NOT/LEN/CONCAT(15 个):ADD/SUB/MUL/DIV/MOD/POW/UNM/NOT/LEN/CONCAT/EQ/LT/LE/TEST/TESTSET | 3-4 周 | heavy_arith / heavy_floatloop 升 P4 + byte-equal P1 + 比 P1 快 |
| **PJ10c** | 控制流 + 循环(3 个):FORPREP / FORLOOP / TFORLOOP | 2-3 周 | 含 FORLOOP 的脚本全升 P4(realworld fib 等 + heavy 全套)+ byte-equal |
| **PJ10d** | 表 + 函数调用(10 个):GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/NEWTABLE/SETLIST/CALL/TAILCALL/CLOSURE/CLOSE | 4-6 周 | realworld 五脚本全升 P4 + byte-equal,V15a P4 ≥ P3 真兑现 |

**总工程量估算:10-15 周**(约 2.5-4 人月)。比 [02-template-direction §1.4](./02-template-direction.md) 自估「P4 全栈 +1-2 人年」中的「单架构后端」部分 0.5-1 人年范围内,与 P3 PW0-PW10 总耗时(约 4 个月)相当。

PJ10b/c/d 同时启用 fast path(承 PJ0-PJ9 全套),即使本 sub-PJ 通用路径没接到所有 op,fast path 已接的形态仍保留性能。

---

## 9. 与 PJ11 的关系

PJ11 是原 PJ10 的「luajc 档验收 + 性能调优」,**判定门没变**:V14 列内核负载 ≥ 4.4× over gopher-lua。该门 PJ3 阶段早已突破(实测 12-25×),PJ11 只是把 V14 数字定稿、配套调优(locals 寄存器缓存可能展开 / guard 合并窥孔等,见 [00-overview §11](./00-overview.md))。

PJ10 完成后 PJ11 的工作量 **比原计划下降**——通用 per-op 翻译路径让 V15 / V15b 的数字稳健,不再需要为每种新形态写字节级模板。PJ11 的「性能调优」聚焦在两件事:

1. **fast path 命中率**:PJ4 IC inline / PJ5 CALL spec / PJ3 FORLOOP inline 在通用路径同时存在时的真实命中率(实测)
2. **通用路径细优化**:寄存器/栈槽访问的窥孔合并、host helper 调用前后的临时寄存器复用

---

## 10. 与 P5 trace JIT 的接口

[../p5-trace-jit](../p5-trace-jit.md) 是下一站,前置假设是 **「baseline 层能为任意 opcode 发射机器码」**。当前 PJ0-PJ9 的字节级模板路径**不满足这条**——baseline 只接已识别形态,trace 拿到一串 opcode 没法直接喂给 baseline emit。

PJ10 交付后,这条假设第一次成立:

```
trace recording → trace optimization → trace emit:
    for op in trace_ops:
        baseline.emitOp(buf, op)  # ← P4 PJ10 提供
    # 优化阶段:trace JIT 可以在 emitOp 上层做 guard hoisting / dead op 消除 / 跨 op 寄存器复用
```

PJ10 是 P5 立项的关键基础设施 — 没有 PJ10,P5 trace JIT 是空中楼阁。

---

## 11. 风险与缓解

| 风险 | 等级 | 缓解 |
|---|---|---|
| 通用 per-op 翻译性能不及预期(< P3 wasm)| 中 | fast path 兜底已有 V14 数字;通用路径只需 ≥ P1 解释器即可算「升对了」 |
| emit 函数 38 个,工程量超估 | 中 | 直接借鉴 P3 wasm 同名 emit 函数(P3 已实装 30 个,差别只在「发 wasm 字节 → 发原生字节」),逻辑结构可整段照搬 |
| double-emit(fast path 已生效,通用路径仍写一份)增加维护负担 | 低 | 通用路径作为 fallback,fast path 不命中时才用;两条路径单独 byte-equal 测;且通用路径的 emit 函数 PJ10d 完成后就稳定了 |
| arm64 端 per-op emit 与 amd64 完全对齐工作量翻倍 | 中 | 同 P3 PW0-PW10:amd64 先打通,arm64 跟上;两份 emit 函数签名一致,逻辑结构对应,只是字节序列不同 |
| 通用路径暴露新 byte-equal bug(P1 解释器某条角落语义 P4 通用路径没复制对)| 中 | 全套 difftest / conformance / luasuite 已就绪;每个 sub-PJ 完成后跑全套 |

---

## 12. 决议项

| # | 项 | 状态 | 备注 |
|---|---|---|---|
| D10-1 | PJ10 是否启动 | ✅ 已决议 | 2026-06-30 用户确认 |
| D10-2 | fast path 是否保留 | ✅ 保留 | 见 §6 — PJ0-PJ9 所有 spec templates 作 fast path |
| D10-3 | sub-PJ 拆分(PJ10a-d)是否按 §8 节奏 | ⬜ | PJ10a 启动前确认 |
| D10-4 | amd64 / arm64 并行还是串行 | ⬜ | 倾向串行(amd64 先打通 PJ10a-d,arm64 跟上同款节奏),与 P3 PW0-PW10 节奏对位;并行风险是 arm64 物理 runner 还在 PJ8 工程中 |
| D10-5 | PJ11 V14 / V15 验收数字是否在 PJ10 完成时一并刷 | ⬜ | PJ10d 完成时实测 |

---

## 13. 不变式

PJ10 接续 PJ0-PJ9 的所有不变式,新增本档独有:

1. **fast path 不动**:PJ0-PJ9 字节级模板的 byte-equal P1 与 byte 序列**不能因 PJ10 通用路径接入而改变**(回归测试守门)
2. **per-op 通用路径与 P1 解释器 byte-equal**:每个 emit 函数的产物执行结果与 P1 解释器对应 op 完全一致(逐字节,含 NaN bit pattern / 错误消息 / traceback pc)
3. **OSR exit 仍生效**:通用路径里的 guard 失败(若有,主要在 fast path 内嵌投机时)走 OSR exit 协议,与 PJ0-PJ9 同款
4. **safepoint 不漏**:回边、call 前后、长直线段后(每 N 条指令)都插 safepoint check,纪律承 [05-system-pipeline §6](./05-system-pipeline.md)
5. **不引入 IR**:emit 函数直接发字节,不经 SSA / 中间值 / 任何抽象层

---

## 14. 实现进度(2026-06-30 全 opcode 覆盖 Go 端回放)

PJ10 当前实现采取了**比设计文档更保守的物理路径**:不真正发射多 BB amd64 原生码,而是把单 BB Proto 的指令拆成 Go 端「head op + side effect」的回放清单,mmap 段只是占位 stub(`xor eax, eax; ret`),Run 在执行 stub 后用 Go 代码按清单调 host helper / 写寄存器。

这条「Go 端回放」路径覆盖到的 opcode(2026-06-30 session 完):

| 子档 | 覆盖 opcode | 形态举例 |
|---|---|---|
| **PJ10a 直线** | MOVE / LOADK / LOADBOOL(C=0) / LOADNIL(含 multi-slot + scratch fill)/ GETUPVAL / SETUPVAL / RETURN(B=0 multret tail-call + B=1 setter + B>=2 多返回)| `return x, y, 1, 2` / `local outer; function set(v) outer = v end` |
| **PJ10b 算术 + 一元 + 拼接 + NOT** | ADD / SUB / MUL / DIV / MOD / POW / UNM / LEN / NOT / CONCAT | `return a + b, a - b` / `return -x, not y, #z` / `return a .. b` |
| **PJ10c 比较 diamond** | EQ / LT / LE(4-op 折叠形态) | `return a == b` / `return a < b` / `return a ~= b` / `return a > b` |
| **PJ10c 短路 diamond** | TESTSET + JMP + MOVE/LOADK(3-op 折叠) | `return x and y` / `return x or 0` |
| **PJ10c 数值循环** | FORPREP + FORLOOP(Go 端 host.ForPrep + 迭代 dispatch + bodyEffects 回放)| `for i=1,n do s = s + i end` |
| **PJ10c 泛型循环** | TFORLOOP(JMP-skip-body + body + TFORLOOP + JMP-back 形态,host.TForLoop 驱动)| `for k, v in customIter do ... end`(自写迭代器) |
| **PJ10d 表 + 全局** | GETTABLE / SETTABLE / GETGLOBAL / SETGLOBAL / NEWTABLE / SETLIST | `return t.x` / `return {1,2,3}` / `_G.foo = 1` |
| **PJ10d 函数调用** | CALL / TAILCALL / SELF | `local r = f(x)` / `return f(x)` / `obj:method()` |
| **PJ10d 闭包** | CLOSURE(读 SubNUps 跳 pseudo 伪指令)/ CLOSE | `local f = function() return x end` |

**38 个核心 opcode 中 PJ10 接住:** MOVE / LOADK / LOADBOOL / LOADNIL / GETUPVAL / GETGLOBAL / GETTABLE / SETGLOBAL / SETUPVAL / SETTABLE / NEWTABLE / SELF / ADD / SUB / MUL / DIV / MOD / POW / UNM / NOT / LEN / CONCAT / EQ / LT / LE / TESTSET / FORPREP / FORLOOP / TFORLOOP / CALL / TAILCALL / RETURN / CLOSURE / CLOSE / SETLIST 共 **35 个**(VARARG 设计上永不接,JMP / TEST / LOADBOOL C!=0 需 CFG 多 BB)。

未实现(仍需真 CFG 翻译):

- **LOADBOOL C != 0**:跳过下一指令的 BB 分裂语义(非折叠 diamond 形态)
- **任意 JMP / TEST**:`if-then-end` / `while-do-end` 需要 CFG 多 BB 翻译
- **多 FORLOOP/TFORLOOP 嵌套**:当前只接受单循环对
- **VARARG**:设计文档明示永不接(同 P3)

「真 CFG 翻译」的下一步:
- 构造 CFG(承 P3 wasm `cfg.go` 同款 leader pc 切 BB + 后继边连接)
- 多 BB 翻译:每个 BB 单独发一段字节,JMP 经 label resolver 在终末绑定;FORLOOP 回边发 `jne <body_start>` 之类的物理跳转
- 跳出 Go 端回放,真正发射 amd64 native code

物理路径切换的拐点:**FORLOOP 一旦真接入 native code**,单 BB 回放就必须升级到多 BB native emit,否则循环每次迭代都要跨 Go ↔ mmap 边界一次,违反 PJ10b heavy bench 的 P3 ≥ 5× 加速 baseline。

当前 Go 端 FORLOOP/TFORLOOP 已可正确执行,语义与 P1 解释器逐字节一致,但性能优势限于「函数边界 dispatch 省一次」(因为 body 本身经 host.Arith 调用,与解释器形态相同)。后续 native code 才能在循环内消除 host helper 调用。

