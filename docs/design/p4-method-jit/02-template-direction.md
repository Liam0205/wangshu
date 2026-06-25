# P4 §2:方向裁决——JSC Baseline 风格 per-function 模板编译

> 状态:**架构决策深度**(对齐 [../architecture.md](../architecture.md) §2 状态表:P4 是「架构决策」,比 P2/P3 详细设计粗一档——本文定方向、定边界、给候选谱系否决论证;**不逐 opcode 展开模板形态**,逐 opcode 模板留 [./03-speculation-ic.md](./03-speculation-ic.md) / [./06-backends.md](./06-backends.md) 落地)。本文是 P4 文档集 [./00-overview.md](./00-overview.md) §0 文档地图所定的「方向裁决」单一事实源——P4 为何是模板编译、为何不是优化编译器、为何不直接做 trace JIT、做什么 / 不做什么的边界、prior art 阶梯对照。
>
> 上游契约:[../roadmap.md](../roadmap.md)(§4 P4 定义、§7 prior art:V8 Sparkplug / JSC Baseline JIT、§5 五条贯穿原则)、[../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提三五原则 / 前提四值表示承诺)、[../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P4=gibbous tier-1,与 P3 同层)。
>
> P1 依赖面:[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md)(源 ISA 38 个 opcode + §7 IC slot——P4 模板的输入面)、[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(§1.3 arena 值栈槽寻址 / §2 主循环与 dispatch 形态 / §7 调用协议——P4 模板编译要消除的解释器恒定税与要保留的 CallInfo 协议)、[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §3.2(NaN-box 位布局——P4 生成码直接操作的同一编码)。
>
> P2 依赖面:[../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3(F1-F7 闸门——P4 仍只编译可编译子集,F7 由 P4 后端替换 P3 后端实装)、[../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md)(P2/P3 零 deopt 状态机基线——P4 引入 deopt 边的对照参照)。
>
> P3 依赖面(同 tier 对位):[../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md)(P3 翻译器主体——P4 与之共享 P2 前端,只换发射后端)。
>
> 下游协作(同子目录):[./03-speculation-ic.md](./03-speculation-ic.md)(IC 反馈→f64 快路径 + guard,本文 §2.4 / §4.1 提对位 + §4.4 的「子集内投机」由其落具体)、[./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化与 deopt 状态机,本文 §6 第 2 条「栈槽真相」由其落具体形态)、[./05-system-pipeline.md](./05-system-pipeline.md)(四项税兑现与 trampoline)、[./06-backends.md](./06-backends.md)(amd64/arm64 双后端发射函数与寄存器约定)、[../p5-trace-jit.md](../p5-trace-jit.md) §1.2(P5 = P4 收益不够时的下一站,本文 §4.3 落对位)。

对应 Go 包:`internal/gibbous/jit`(`internal/gibbous/wasm` 的兄弟包,P4 后端发射函数主包,详见 [../architecture.md](../architecture.md) §1)。

---

## 0. 定位

### 0.1 一句话决定式

> **P4 = dispatch 消除器 + IC 投机注入器,不是优化编译器。**

模板编译消除的是**解释器的恒定税**——取指、译码、dispatch 跳转、pc 维护、操作数位运算([../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §2.1 主循环每条指令都付的开销),把它变成直线机器码 + 编译期立即数操作数。**它不消除**栈槽内存往返(每条字节码边界处活值仍住 arena 值栈,§1.6)——那是 P5 寄存器分配的活,不是 P4 的边界。这一道分工是「P4 简单 vs P5 复杂」的结构分界线,也是 §3 否决方法级优化编译器、§4 划定边界表的核心论据。

「dispatch 消除器」与「IC 投机注入器」是同一道工序的两面:模板贴出的同时,IC feedback 决定贴的是「f64 快路径 + guard」还是「通用语义模板」(详 [./03-speculation-ic.md](./03-speculation-ic.md) §2)。若只消 dispatch 不投机,P4 退化为「直线化的解释器」,只能拿到 dispatch 税那一段;两者叠加,才把 [../roadmap.md](../roadmap.md) 流水线图标注的「trace 收益的约 70%」拿到手。

### 0.2 与 P3 翻译器的关系

P3 与 P4 **同属 gibbous(tier-1)**——同一个 tier、同一套分层结构(P2 的热度 / TypeFeedback / 可编译性闸门 + 升层状态机 + 跨层调用协议),区别只在「热函数被编译成什么」:P3 发 Wasm 交 wazero 执行,P4 **直接发原生机器码**([../roadmap.md](../roadmap.md) §4「继承 P3 的全部分层结构,只换发射后端」)。包布局据此:`internal/gibbous/wasm` 与 `internal/gibbous/jit` 并列([../architecture.md](../architecture.md) §1)。

| 维度 | P3 (`gibbous/wasm`) | P4 (`gibbous/jit`,本文裁决形态) |
|---|---|---|
| 共享前端 | P2(热度 / TypeFeedback / F1-F7 闸门 / TierState 状态机)** | 同 P2,完全复用 |
| 字节码源 | `Proto.Code`(P1 ISA 38 opcode) | 同 P1 ISA |
| 翻译形态 | 字节码 → Wasm 函数(WAT 形态,详 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3) | 字节码 → 原生机器码(per-opcode 模板贴出,§1.1) |
| 执行后端 | wazero(Apache 2.0 纯 Go Wasm 引擎) | 自管 codegen + amd64/arm64 双后端 |
| 四项税应对 | 全套外包给 wazero([../roadmap.md](../roadmap.md) §2 标准解法的现成实装) | 自付,wazero 是采石场参考(详 [./05-system-pipeline.md](./05-system-pipeline.md)) |
| 投机 | 否(P3 是非投机翻译,逐字节同构,零 deopt) | **是**(P4 是首层投机:f64 快路径 + guard,详 [./03-speculation-ic.md](./03-speculation-ic.md)) |
| 升层目的地 | 「热且可编译且无投机风险」 | 「热且可编译且有稳定 feedback」 |
| Deopt 边 | 不存在(状态机单向无环,详 [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) §2) | 存在(状态机加一条 `gibbous→interp` 边,详 [./04-osr-deopt.md](./04-osr-deopt.md)) |

**这道并列关系的实施红利**:P3 已铺好的所有分层骨架(升层时机、F1-F7 闸门、TypeFeedback 供料管线、跨层 trampoline 协议、`P3Compiler` 接口签名)P4 全部继承,只把发射后端从 wazero 换成本地 codegen——这是 [../roadmap.md](../roadmap.md) §4 「P4 = P3 的全部分层结构,只换发射后端」字面承诺的物理形态,也是 §3 拒绝「跳过 P3 直接做 P4」的部分理由。

### 0.3 与 P5 trace JIT 的关系

P5(fullmoon tier-2)是「P4 收益不够时」的下一站([../p5-trace-jit.md](../p5-trace-jit.md) §0 / [../roadmap.md](../roadmap.md) §4 启动条件)。P4 与 P5 在分层阶梯上是**相邻两级**,两者的分工由「边际收益曲线 × 实现成本曲线」共同决定:

```
        加速比(over crescent)
          ▲
    P5 ── │           ╭──────  ← P5 拿走剩余 ~30%(跨函数内联 + IR 优化 + 寄存器分配)
          │       ╭───╯
          │   ╭───╯
    P4 ── │ ──╯              ← P4 拿走 ~70%(dispatch 消除 + IC 投机 + 栈槽直存)
          │
    P3 ── │ ──┐              ← P3(gibbous/wasm)≈ P4 一档(同 tier),非投机翻译
          │
    P1 ── │  1x              ← crescent(基线解释)
          └──────────────────────────────► 实现复杂度(人月→人年→数人年)
            6-9月  人月  人年  数人年
            P1     P2/P3  P4    P5
```

(纵轴是示意,具体数字见 §2.1 阶梯表与 [../roadmap.md](../roadmap.md) §1 校准测量。)

P4 与 P5 的分工依据 [../p5-trace-jit.md](../p5-trace-jit.md) §1.2 的「method JIT 结构性吃不下的负载类别」表:

| 负载类别 | P4 是否吃得下 | 落点 |
|---|---|---|
| 标量算术内核(列内核循环体纯计算) | **是**(dispatch 消除 + f64 快路径主场) | P4 验收的目标负载 |
| 跨函数热循环(循环体每轮调小函数) | **否**(函数边界 = 编译单元边界,调用税付不掉) | P5 trace 内联的主场 |
| 循环携带的冗余(LICM / CSE 候选) | **否**(模板编译无 IR,看不见跨指令数据流) | P5 IR 优化 |
| 分配密集循环 | **否**(P4 不做逃逸分析) | P5 分配下沉 |
| megamorphic 的稳定子集 | **否**(P2 标 `FBTableMega` 即放弃投机) | P5 按实际路径特化 |

**P4 的合法猎物只是第一行,但第一行就是 [../roadmap.md](../roadmap.md) §0 近期目标(「列内核负载 ≥ luajc 档」)的承载者**——前提一([../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md))校准测量 1 显示真 LuaJIT 仅比 luajc 快 6%,P4 达标即「逼近 LuaJIT 档」,项目近期目标在此兑现,是否启动 P5 由验收后实测裁决,本文不做预设。

### 0.4 章节 → 后续详细设计映射

本文承担「方向裁决」单一事实源,不展开任何机制的具体形态;具体形态由本目录后续子文档接力。映射表(每节点出本文章节 → 下游详细设计):

| 本文节 | 主题 | 下游落具体的位置 |
|---|---|---|
| §1.1-1.3 | per-opcode 模板形态 / 虚拟寄存器 = 栈槽 / 控制流直译 | [./03-speculation-ic.md](./03-speculation-ic.md) §1(模板与 IC 的接缝) + [./06-backends.md](./06-backends.md) §3(per-arch 发射函数实装) |
| §1.4 编译时间线性 | 微秒级编译 → 同步编译可行 | [./05-system-pipeline.md](./05-system-pipeline.md) §3(编译执行线程模型) |
| §1.5 消除什么 | dispatch / 译码 / pc 维护的具体消除点 | [./03-speculation-ic.md](./03-speculation-ic.md) §1.4(模板内部短寿寄存器约定) |
| §1.6 不消除什么 | 栈槽内存往返保留 → 「栈槽真相」不变式 | [./04-osr-deopt.md](./04-osr-deopt.md) §3.3(由它落具体不变式形态与 exit 物化) |
| §2.4 IC 投机的对位 | per-opcode 模板 + 内联 IC 形态 | [./03-speculation-ic.md](./03-speculation-ic.md) §2-§4(feedback 种类 → 投机模板 → guard 形态) |
| §3 候选谱系否决 | 否决理由的「简单性向下传导」 | [./04-osr-deopt.md](./04-osr-deopt.md) §3(deopt 不需要 snapshot 的具体落点) |
| §4.1 做(P4 边界内)| 沿用 P2 F1-F7 闸门 | [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3(F7 由 P4 后端实装替换 P3) |
| §4.2 不做(留 P5)| 跨函数内联 / snapshot / LICM/CSE / 寄存器分配 | [../p5-trace-jit.md](../p5-trace-jit.md) §1.2 / §3 |
| §5.1 模板总量 | 38 opcode + guard/exit/trampoline 胶水 | [./06-backends.md](./06-backends.md) §3 / §5(每架构数十段发射函数) |

---

## 1. 模板编译是什么

### 1.1 per-function 一遍线性扫描

模板编译的工序极简单——单遍扫一遍一个 Proto 的字节码,每条指令贴上一段预制的机器码模板,顺序拼接成该函数的原生代码:

```
线性扫 Proto.Code:
  for pc, instr := range proto.Code:
    1. 查模板表:opcode → 模板发射函数(§5.3 分级)
    2. 按 IC feedback 决定贴「投机模板」或「通用模板」(详 03-speculation-ic §2)
    3. 实例化模板的操作数槽位(A/B/C 落为编译期立即数)
    4. 实例化跳转目标(JMP 等记 pc → 机器地址映射,前向跳转回填)
    5. 发射到代码缓冲(自管 mmap 代码页,详 05-system-pipeline §2)
  收尾:回填所有前向跳转;exec mmap 翻面;icache flush(arm64);装载到 Proto.gibbousJITCode
```

**没有 IR,没有跨指令的寄存器分配,没有指令调度**——这三个「没有」是 §3 否决方法级优化编译器的结构性来源,也是 §1.4 编译时间线性的物理基础。

JSC Baseline / V8 Sparkplug 的同款工序见 §2,本节先把工序与解释器的对位关系说清:

```
解释器主循环                                  P4 模板编译产出
([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §2.1)
─────────────────────────────────────────    ─────────────────────────────────────────
for {                                        ;; 函数序言(trampoline 入口,详 05 §2.4):
  instr := code[pc]                          ;;   切自管栈、装 jitContext、刷新 base
  pc++                                       ;;   每条字节码编译期已知 pc,无运行期 pc 维护
  switch instr.OpCode {                      
  case ADD:                                  ;; opcode = ADD 的模板贴出(详 03 §2):
    a, b, c := decode_ABC(instr)             ;;   ① 取 R(B):mov rax, qword [r_base + 8*B]   ← B 是编译期立即数
    vb, vc := stk[base+b], stk[base+c]       ;;   ② 取 R(C):mov rdx, qword [r_base + 8*C]
    if isnum(vb) && isnum(vc):               ;;   ③ guard:test rax/rdx 是否 IsNumber(NaN-box 比较,详 03 §2.3)
      stk[base+a] = vb_f64 + vc_f64          ;;   ④ 快路径:movq xmm0, rax; addsd xmm0, xmm0(rdx);movq qword [r_base + 8*A], xmm0
    else:                                    ;;   ⑤ guard 失败 → call 慢路径助手(经 trampoline 出去,详 04 §3.1)
      doArithSlow(...)                       ;;   慢路径返回后回到下条字节码模板
  case MOV: ...                              ;; opcode = MOVE 的模板贴出:
  ...                                        ;;   ① 取 R(B);② 写 R(A);(无 guard,纯 i64 字节复制)
  }                                          ;; 各 opcode 模板按 pc 顺序拼接,中间无 dispatch、无译码
}
```

(per-opcode 模板的具体 NASM 形态由 [./03-speculation-ic.md](./03-speculation-ic.md) §2 / [./06-backends.md](./06-backends.md) §3 落地;本节伪码仅说明形态,具体寄存器约定与指令选择不在本文范围。)

模板编译的收益就在右栏与左栏的差:右栏每条字节码不付左栏的 `for / decode_ABC / switch` 开销;guard 是 NaN-box 单次 u64 比较加条件跳的恒定 2-3 条机器指令(详 [./03-speculation-ic.md](./03-speculation-ic.md) §2.2);快路径直接走 SSE/NEON 浮点指令——这些都是 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §3.4 列举的「翻译 vs 解释」常数因子优势的兑现。

### 1.2 虚拟寄存器 = arena 值栈槽

P4 模板的「虚拟寄存器」与解释器**完全一致**:`R(i)` = `valueStack[base+i]`(详 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.3 / [../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §3 栈帧约定)。模板做的是:

```
取操作数:从栈槽 load 到机器寄存器(暂存)
计算:    在机器寄存器之间运算
回写:    store 回栈槽
```

机器寄存器**只在单条模板内部短暂存活**——出了模板边界(= 一条字节码完成),所有 Lua 活值物化在栈槽里。这条结构性质是 §1.6「不消除栈槽往返」与 §6 第 2 条「栈槽真相」不变式的物理基础。

这条选择与 P3 翻译器 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §2.2「全 memory-resident」是同一道基线选择(同一句话:把寄存器藏到不可见地方就破坏了 GC 根集合的简洁性,也增加了与解释器 byte-equal 的差分面),区别只是 P3 的「内存」是 wazero linear memory(由 wazero 维护),P4 的「内存」是 arena 值栈段(由望舒自管 GC 维护)——两者**物理上是同一块字节**,因为 P3 落地后 arena 已收养 wazero memory([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §7.2 / `03-memory-model`),P4 接手只是把读写指令从 Wasm `i64.load/store` 换成机器 `mov`,寻址公式不变。

**红利**:P3 在 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §2.2 列举的四项 memory-resident 红利(GC 根天然共见 / byte-equal 差分零额外面 / trampoline 协议退化为「传 base 偏移」/ 收益不靠寄存器提升),P4 全部继承。这是 §0.2 表格「P3/P4 同前端、同字节码源」的微观体现。

### 1.3 控制流直译 + pc→机器地址映射

控制流的处理同样直白:

| 字节码控制流 | 机器码翻译 |
|---|---|
| `JMP sBx`(无条件跳) | 机器 `jmp imm32`(立即数偏移),目标 = 模板表内 `pc+1+sBx` 对应模板起始地址 |
| `EQ`/`LT`/`LE`/`TEST`/`TESTSET` 后随 `JMP`(条件跳) | 比较结果 → 机器 `jz/jnz/je/jne` 等条件跳,目标同上 |
| `FORLOOP`/`FORPREP` 回边 | 算术 + 比较 + 条件跳到 FORLOOP 模板起始(回边处插 safepoint 检查,详 §1.5 与 [./05-system-pipeline.md](./05-system-pipeline.md) §3) |
| `CALL`/`TAILCALL`/`RETURN` | 经 trampoline 出 JIT 世界(若被调方非 JIT)或同 JIT 世界内 call(若被调方有 JIT 码),详 [./05-system-pipeline.md](./05-system-pipeline.md) §4 |

关键工程细节是**前向跳转的回填**:线性扫描时若 `JMP sBx` 的目标 pc 尚未发射,该 jmp 的目标偏移记不到,先发射占位 `jmp imm32(=0)` 并把回填请求登记到 fixup 表;扫到目标 pc 时把对应模板起始机器地址写回 fixup 表里所有等待者。这是 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3.5 wat 形态 `relooper` 算法的机器码同构——只是 wat 那边是结构化控制流(`block`/`loop`/`br`),P4 这边是一遍扫 + fixup 的传统模板编译做法,两种做法各自匹配各自后端的语义。

| 对照维度 | P3(wasm 翻译) | P4(机器码模板) |
|---|---|---|
| 控制流结构 | 结构化(Wasm 强制 `block`/`loop`/`br_table`) | 线性(机器码任意 jmp,无结构化约束) |
| 回边表达 | `loop` + `br` 回到块起 | `jcc`/`jmp imm32` 到回边模板地址 |
| 前向跳转 | wazero 自处理 | 自前向 fixup 表回填 |
| safepoint 插入点 | 回边由翻译器自插([../p3-wasm-tier/05-safepoint-gc.md](../p3-wasm-tier/05-safepoint-gc.md)) | 同样回边自插(详 [./05-system-pipeline.md](./05-system-pipeline.md) §3) |

(具体 jmp 编码、fixup 表数据结构、回边 safepoint 形态由 [./06-backends.md](./06-backends.md) §3 与 [./05-system-pipeline.md](./05-system-pipeline.md) §3 落地。)

### 1.4 编译时间线性 = 升层停顿可忽略

per-opcode 模板贴出 + 单遍扫描 ⇒ **编译时间与字节码长度严格 O(N)**(N = `len(Proto.Code)`),常数极小(每 opcode 数十条机器指令的 memcpy + 立即数填充 + 偶发 fixup 登记)。

| 函数规模(opcode 数) | 估计编译时间 | 比对参照 |
|---|---|---|
| 典型小函数(< 50 opcode) | 数微秒 | 典型 Lua 函数,P2 F5 大函数闸门通常远大于此(详 [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3.5) |
| 中等函数(50-300 opcode) | 几十微秒 | 列内核典型循环体加调用助手 |
| 上限(F5 大函数闸门内的最大值) | 数百微秒 | F5 闸门保证不会有更大 |

(具体常数待 P4 原型 spike 后定标,本表是「同款工序的同款数量级」推断,基于 wazero 编译模式的同款经验数字与 V8 Sparkplug 论文报告的「单次编译微秒级」。)

伪码:

```
compileProto(proto, feedback):           // 总成本 O(N),N = len(proto.Code)
  buf := newCodeBuffer()                 // O(1) 准备
  for pc, instr := range proto.Code:     // O(N) 主循环
    template := selectTemplate(instr, feedback)   // O(1) 表查
    template.emit(buf, pc, instr.A, instr.B, instr.C)  // O(1) per-opcode
    if isJump(instr): registerFixup(...) // O(1)
  resolveFixups(buf)                     // O(F),F = 跳转数 ≤ N
  finalize(buf):                         // O(1)
    mprotect(buf.page, R|X)              // exec mmap 翻面(05 §2.2)
    icacheFlush(buf.page)                // arm64 必需(05 §2.3)
  return buf.entry                       // 函数入口机器地址
```

per-opcode 模板形态(NASM 风格,amd64 ADD 投机模板示意):

```nasm
; ADD A B C  (R(A) := R(B) + R(C),feedback=FBArithStableNumber)
;   r_base = 编译期约定的「base 字节偏移」固定寄存器(详 06-backends §3.2)
;   8*B / 8*C / 8*A 是编译期立即数

  mov   rax, qword [r_base + 8*B]   ; 取 R(B) NaN-box u64
  mov   rdx, qword [r_base + 8*C]   ; 取 R(C) NaN-box u64

  ; guard:两操作数 IsNumber(NaN-box 单次比较,详 03-speculation-ic §2.3)
  ; (具体比较序列由 03 落,本节示意:guard 失败 → 跳到本模板的 osr_exit_pc_<pc> stub)
  <guard rax IsNumber>
  jne   osr_exit_pc_<pc>
  <guard rdx IsNumber>
  jne   osr_exit_pc_<pc>

  ; 快路径:f64 加法
  movq  xmm0, rax
  movq  xmm1, rdx
  addsd xmm0, xmm1
  ; (NaN 规范化序列,详 03 §2.1 落具体)
  movq  qword [r_base + 8*A], xmm0  ; 写回 R(A)

  ; 落到下条字节码模板(直线,无 dispatch)
```

(具体寄存器约定、`r_base` 选什么、guard 序列、NaN 规范化由 [./06-backends.md](./06-backends.md) §3 + [./03-speculation-ic.md](./03-speculation-ic.md) §2 落地;本节伪码仅说明形态。同款形态在 arm64 上对应 `ldr`/`str`/`fmov`/`fadd`,接近 1:1 翻译。)

**红利:升层停顿可忽略,无需后台编译流水线**——升层时刻直接同步编译该 Proto,几十微秒级开销摊到列内核数千万次调用上不可见。这与 V8 Maglev/TurboFan、JSC DFG/FTL 等优化编译层「必须后台编译 + 安装屏障」形成鲜明对比([../roadmap.md](../roadmap.md) §7 prior art 阶梯,§2.1 详)——后者编译耗时数百毫秒到秒级,同步编译会卡主线程。

工程含义:编译执行的线程模型继承 P3 ([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1)同款决策——升层时机由 P2 决策机驱动,同步编译装载到 `Proto.gibbousJITCode`,不引入异步编译屏障。具体决策落点与 P3 同处,由 [./05-system-pipeline.md](./05-system-pipeline.md) §3 守(若 P3 后置实测推翻,P4 同步翻案)。

### 1.5 模板编译消除什么

精确清单——每条字节码,解释器付而 P4 模板**不付**的开销:

| 解释器开销项 | 解释器位置 | P4 不付的原因 |
|---|---|---|
| **取指**:`instr := code[pc]; pc++` | [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §2.1 主循环每轮 | 编译期已知 pc → 模板按 pc 顺序拼接,运行期无 pc 寄存器 |
| **译码**:`opcode, a, b, c := decode(instr)` | 同上 | A/B/C 在编译期烧成立即数;opcode 编译期已识别为模板 |
| **dispatch 跳转**:`switch opcode { ... }` 间接跳 | 同上(closure-threading 也只缩这一项) | 模板按 opcode 选定 → 直线代码,无间接跳 |
| **类型分支**:`if isnum(vb) && isnum(vc) { ... } else { slow }`(算术族每条) | [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §4 | 投机模板下退化为「guard + 快路径」(2-3 条指令的 NaN-box 比较)+ 失败 OSR exit;通用模板按解释器同构(无消除) |
| **IC 命中路径的结构往返**:slot 索引、代次比对、null 槽等 | [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §6 | 命中分支烧成「比较代次 + 直达槽 load/store」机器码(详 [./03-speculation-ic.md](./03-speculation-ic.md) §3) |
| **跳转 pc 维护**:`pc = pc + 1 + sBx`(JMP 类) | [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §2 | 替换为机器 `jmp imm32`,无 pc 算术 |

这些项加起来即「dispatch 税」—— [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §3.4 列举的解释器主循环每条指令付的恒定开销,P4 全部消掉。

### 1.6 模板编译不消除什么

精确清单——每条字节码边界,P4 模板**保留**的开销:

| 保留的开销项 | 保留原因 | P5 怎么消(参照) |
|---|---|---|
| **栈槽内存往返**:每条 opcode 把操作数从栈槽 load、计算、store 回栈槽 | 模板内寄存器只单条短寿,不跨字节码边界(§1.2) | P5 寄存器分配让 IR 值跨指令驻留机器寄存器,栈槽往返消失([../p5-trace-jit.md](../p5-trace-jit.md) §2 ③) |
| **跨指令的冗余计算**:`R(B)` 在多条指令重复 load、相同表达式重算、循环不变量每轮重做 | 无 IR 看不见跨指令数据流 | P5 IR 优化:CSE / LICM / 死代码消除([../p5-trace-jit.md](../p5-trace-jit.md) §2 ②) |
| **跨函数边界开销**:CALL/TAILCALL/RETURN 走通用 CallInfo 协议(详 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §7) | 编译单元是函数,看不见调用图 | P5 trace 跨调用边界录制,被调函数体内联进 trace([../p5-trace-jit.md](../p5-trace-jit.md) §1.2 第 1 类) |
| **分配点的真实分配**:每轮迭代构造的临时 table/string | 无逃逸分析 | P5 分配下沉,不逃出 trace 的分配彻底消除([../p5-trace-jit.md](../p5-trace-jit.md) §1.2 第 3 类) |
| **每模板独立的 guard**:同一操作数在直线段内被多次 guard | 无 IR 看不见 guard 之间的覆盖关系 | P5 沿 trace guard 去重([../p5-trace-jit.md](../p5-trace-jit.md) §2 ②);P4 自身的窥孔级 guard 合并是不引入 IR 前提下可做的局部优化(§7) |

P4 vs P5 的边界由这条「保留 vs 消除」清单划定:**P4 在「单条字节码边界内」做完全部优化(消 dispatch + IC 投机),不跨边界**;P5 在边界之外做(IR + regalloc + trace 内联)。这条结构分界线是 §3 否决「方法级优化编译器」、§4.2 划「不做」清单的核心论据,也是 §6 第 2 条「栈槽真相」不变式的来源。

---

## 2. prior art 对照:JSC Baseline / V8 Sparkplug

### 2.1 三引擎分层阶梯表

[../roadmap.md](../roadmap.md) §7 点名 V8 与 JSC 的分层阶梯为 P4 选型的标准参照。三引擎按 tier 阶梯对位:

| 引擎 | 解释器(基线) | **第一层编译(P4 的对标)** | 第二层编译(优化) | 第三层编译(顶档优化) |
|---|---|---|---|---|
| **V8** | Ignition(寄存器机字节码 + 解释器) | **Sparkplug**(2021 加入,单遍无 IR 直接从字节码发射) | Maglev(2023 加入,中等优化 SSA) | TurboFan(全套 SSA + regalloc + 调度) |
| **JSC** | LLInt(Low-Level 解释器,汇编级) | **Baseline JIT**(per-opcode 模板 + 内联 IC) | DFG(Data Flow Graph,中等优化 SSA) | FTL(Faster Than Light,LLVM 后端的全套优化) |
| **望舒** | crescent(P1,寄存器机字节码 + 解释器) | **gibbous/jit(P4,本文)**或 P3 gibbous/wasm(同 tier) | (无;原则 4「不做完备性」,跳过中等优化层) | fullmoon(P5 trace JIT,不同范式) |

**重要观察一**:V8 与 JSC 都采用了**四层阶梯**(解释 + 模板 + 中等 + 顶档),望舒选了**三层阶梯**(解释 + 模板 + trace)——跳过了「中等优化层」(Maglev / DFG 的对应层)。这是 §3 否决方法级优化编译器的体现:把投入放在「dispatch 消除 + IC 投机」与「trace 投机」两端,中间的「方法级 SSA + regalloc」一档因边际收益过低被跳过。

**重要观察二**:V8 Sparkplug 与 JSC Baseline 都明确把自己定位成**「dispatch 消除器 + IC 注入器」**而非「优化编译器」——两个引擎在公开材料中强调的口径与 P4 的 §0.1 决定式同款:

> "Sparkplug is a non-optimizing JIT compiler. ... It does **just one pass** over the function ... and **emits Sparkplug code** ... a stripped-down version of the bytecode handler, **dispensing with the interpreter dispatch**." —— V8 Sparkplug 公开介绍的精神

> JSC Baseline JIT 的设计文档把自己定位为「per-opcode 模板拼接 + 内联 IC」,**不做寄存器分配、不做指令调度、不做跨基本块优化**,只把解释器的 dispatch 拆掉、把 IC 命中路径内联成机器码。

**P4 的 §0.1 决定式与上述两条公开定位完全对位**——这不是巧合,是「同样的 Lua/JS 语义 + 同样的『先解释、后优化』分层 VM 哲学 + 同样的『中等档收益不够 vs 实现成本太高』权衡」共同得出的结论。

### 2.2 Sparkplug 自我定位 = P4 自我定位

V8 Sparkplug 的设计文档与博客明确(V8 团队公开材料):

1. **「非优化编译器」**——单遍扫一遍字节码,逐 opcode 把对应解释器 handler 的「除掉 dispatch 之后」的部分发射成机器码,字面意义的「dispatch 消除」。
2. **「每条字节码边界处保持解释器栈布局」**——这是为了让 Sparkplug 与 Ignition(V8 解释器)在任意字节码边界可互相切换:Sparkplug 可在任意时刻 OSR exit 到 Ignition、Ignition 可在任意时刻 OSR enter 到 Sparkplug,**因为两者读写同一份栈、同一种值布局**。
3. **「编译时间数微秒级」**——同步编译,不需要后台流水线。

**P4 三条对应**:

| Sparkplug 设计点 | P4 §X 对应 |
|---|---|
| 非优化编译器、单遍扫 | §1.1 per-function 一遍线性扫描 + §0.1 决定式 |
| 字节码边界保持解释器栈布局 | §1.2 虚拟寄存器 = arena 值栈槽 + §1.6 不消除栈槽往返 + §6 第 2 条「栈槽真相」 |
| 编译时间数微秒级、同步编译 | §1.4 编译时间线性 = 升层停顿可忽略 |

**关键差异(也是 P4 的特殊性,§2.5 详)**:Sparkplug 的「每条字节码边界保持解释器栈布局」红利在 V8 是**精心设计**(为了支持两个 tier 之间的双向 OSR);**P4 这条性质是「值表示一次定死」的免费派生**——P1 第一天就承诺值住 arena、NaN-box 编码([../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提四),P4 直接读写同一块内存、同一种编码,根本没有「机器表示 vs 解释器表示」之分,deopt 物化退化为 memmove(详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.2)。

### 2.3 V8 Sparkplug 的具体形态

Sparkplug 的工序更具体的形态(公开材料 + V8 源码可观察):

```
对每个字节码 instr:
  1. 调用 BaselineCompiler::VisitXxx(instr) ——  各 opcode 对应一个 VisitXxx 方法
  2. 方法体内部用 macro-assembler 直接发射机器码:
     a. 取操作数(从字节码寄存器栈位置 load 到机器寄存器)
     b. 调用「内置」(builtin):builtin 是预编译好的机器码助手,封装了原解释器
        handler 的「核心计算部分」(去掉 dispatch);Sparkplug 直接 jmp/call builtin,
        而不是把 handler 拷一份到本函数代码体里
     c. 把结果写回字节码寄存器栈位置
  3. 跳转(JmpIfTrue 等)用机器条件跳到下条字节码模板地址
```

**P4 与之的差别**:V8 Sparkplug 大量使用 **builtin 复用**(builtin 是预编译的机器码助手,Sparkplug 函数体里 jmp/call builtin 而非把代码拷过来),代码体积小;望舒 P4 在「调用 Go 慢路径助手」上用同款思路(慢路径助手是 Go 函数,经 trampoline 出 JIT 世界 → 调用 → 回来,详 [./05-system-pipeline.md](./05-system-pipeline.md) §4),但**快路径**(算术 f64 计算、IC 命中槽 load/store)展开为 inline 机器码而非 jmp builtin——这是 P3 在 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3.2 / §3.4 同款的 inline 决定的对应:把热路径 inline 到本函数体里,避免 jmp builtin 的间接跳转开销。两者都对,选 inline 是因为 P4 的「热路径密度高、guard 多」形态下 inline 收益更稳。

**为什么 P4 不学 V8 builtin 复用**:V8 的 builtin 是 V8 团队多年优化的预编译机器码、跨 Sparkplug 与其它层共享(Maglev/TurboFan 也调同套 builtin),投入与摊销基础完全不同。望舒在 +1-2 人年内不具备 builtin 体系的投入预算,选 inline 是单层最简形态。

### 2.4 JSC Baseline 的 per-opcode 模板 + 内联 IC

JSC Baseline JIT 的形态(WebKit 设计文档):

1. **per-opcode 模板**:每个 opcode 对应一段 macro-assembler 写的模板,Baseline JIT 主循环按 opcode 选模板贴出。
2. **内联 IC**:对 `GetById`/`PutById` 等表/属性访问 opcode,Baseline 在模板里**直接 inline 一个 IC 槽**——首次执行命中时把「Cell 形状判断 + 直达槽偏移」写进 IC 槽,下次同形状命中直接读槽,失效则降级到 IC 重新填或退助手。这是「内联缓存」字面意义的物理形态:cache 的物理位置就在生成码里,不是数据结构。
3. **JSC Baseline 的 OSR 着陆点**:guard 失败时 exit 到 LLInt(JSC 解释器)对应的字节码 pc 续跑;这条 deopt 形态比 DFG/FTL 的 snapshot 简单一个量级。

**P4 与 P3 的对位**(三引擎共有的 IC 形态):

| 形态 | JSC Baseline | V8 Sparkplug | 望舒 P3 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3.4 | 望舒 P4(本目录 03 落) |
|---|---|---|---|---|
| 表访问 IC 命中路径 | inline 「形状比较 + 直达槽偏移」机器码 | inline 「shape 比较 + 直达槽 load」机器码 | inline 「同表 + 同代次 + 直达槽 load」 wasm | inline「同表 + 同代次 + 直达槽 load」机器码 |
| 失效降级 | inline IC 不命中走 IC stub 重新填 | 同上 | inline 不命中走 helper(降级稳定后永久走 helper,详 06) | 同 P3:不命中走 helper,失效阈值后该点降级永久走通用模板(详 03 §3 / §4) |
| 投机 guard 失败 | OSR exit 到 LLInt(解释器) | OSR exit 到 Ignition(解释器) | (P3 不投机,无此项) | OSR exit 到 crescent(解释器),详 04 |

P4 的 IC 形态与 JSC Baseline 同构——**这是「per-opcode 模板 + 内联 IC」字面意义的同名形态**,具体投机模板的形状与 guard 设计由 [./03-speculation-ic.md](./03-speculation-ic.md) §2-§4 落地。

### 2.5 望舒 P4 的特殊性

与 V8 Sparkplug / JSC Baseline 同形态,但望舒 P4 在分层阶梯上的特殊性来自两条码库 physics:

#### 2.5.1 纯 Go 约束

V8/JSC 是 C++ 引擎,不受 Go runtime 四项税([../roadmap.md](../roadmap.md) §2 / [../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提二)约束,可大量使用硬件信号陷阱实现「零成本 guard」——非法访问 SIGSEGV、信号处理器接管恢复。望舒在纯 Go 下走不通这条路:

- **Go runtime 拥有信号处理**:落在非 Go PC 上的 fault 无法恢复,直接 fatal——这是 [../roadmap.md](../roadmap.md) §2 四项税同族的「runtime 所有权」约束(wazero 同样全程显式边界检查,从不依赖陷阱)。
- **结论**:P4 所有 guard 都是显式「比较 + 条件跳」(每 guard 2-3 条指令的恒定成本),具体形态由 [./03-speculation-ic.md](./03-speculation-ic.md) §2.2 落地。这压低了生成码密度天花板,是「纯 Go 逼近而非追平 LuaJIT」的微观注脚之一——量化上已被前提一的 6% 校准吸收(LuaJIT 仅比 luajc 快 6%)。

#### 2.5.2 第一天 NaN-box 承诺让 deopt 简单

P1 第一天的值表示承诺([../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提四「值即 8 字节,跨 tier 拷贝是 memmove」+ [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §7 不变式 1)让 P4 的 deopt 物化退化为「把暂存寄存器里的 NaN-box u64 写回栈槽」——零格式转换、零类型重建、零分配:

| 引擎 | 解释器值表示 | 编译层值表示 | deopt 物化复杂度 |
|---|---|---|---|
| V8 | tagged pointer(SMI / heap) | 同(共用) | 中(寄存器 → 栈槽,需 SMI/HeapObject 区分) |
| JSC | NaN-boxed JSValue | 同 | 中(同上,NaN-box 编码同源) |
| **望舒 P4** | NaN-boxed u64 in arena 值栈 | **同(共用 arena 字节)** | **微(memmove,零格式转换,详 04 §3.2)** |

这是 §3.3 拒绝方法级优化编译器的「简单性向下传导」论据的微观来源——望舒 deopt 简单,**不是因为放弃功能(没有 deopt),是因为承诺替我们消化了复杂度**。

#### 2.5.3 与 P3 同 tier:复用分层骨架

V8 Sparkplug 是 V8 第一个引入分层骨架的层(此前 Ignition/TurboFan 二层就够);望舒 P4 是**第二个**进入 gibbous tier 的后端(P3 wasm 后端已先行)——这条「先 P3 后 P4」的顺序让 P4 直接继承 P3 已铺好的:

- P2 决策机驱动的逐 Proto 升层时机([../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) §3 `considerPromotion`)
- F1-F7 可编译性闸门([../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3,F7 由 P4 后端实装替换 P3)
- TypeFeedback 供料管线([../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md))
- 跨层 trampoline 协议(crescent ↔ gibbous,详 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md))
- 单 Proto 失败原子性纪律([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1.4)

**这道复用是 §3.4 拒绝「跳过 P3 直接做 P4」的部分理由**——若 P3 不存在,P4 同时啃「分层骨架 + 机器码后端」两块硬骨头,人年估算上浮。详细对位见 [./00-overview.md](./00-overview.md) §0.2(「常规路径 vs 跳跃路径」)。

---

## 3. 为什么不是优化编译器(成本/收益曲线)

### 3.1 候选谱系表

把 P4 的方向放在三档候选谱系内对照——「模板编译 vs 方法级优化编译器 vs 直接做 trace JIT」——每档的人力、预期收益、否决理由:

| 候选 | 人力(over P3 完工) | 在望舒约束下的预期收益(over crescent) | 实现成本来源 | 判定 |
|---|---|---|---|---|
| **(a) 模板编译 + IC 投机(选定,P4 本文)** | +1-2 人年(模板 ×2 架构 + 系统管线 + OSR exit + 双架构 CI) | 消除 dispatch/译码 + 热点 f64 直算 + IC 内联 → 拿到「trace 收益的 ~70%」(流水线图) | 后端工程为主:per-arch 发射函数、自管栈、exec mmap、icache flush、trampoline | **选定**(§3.2 论证) |
| (b) 方法级优化编译器(SSA IR + regalloc + 调度,DFG/Maglev/TurboFan 档) | 数人年起(IR 设计 + regalloc + 调度 + 与 deopt 耦合 + 双架构) | 在 (a) 之上的边际:跨指令栈槽→寄存器、跨基本块优化、cross-opcode CSE | 双重投入:全套 SSA 工具链 + 与 deopt 机器(snapshot)的耦合(§3.3) | **否决**(§3.3 论证) |
| (c) 直接做 trace JIT(跳过 P4) | +2-4 人年开放式(P5) | 上限最高(剩余 ~30% 全拿,前提是真存在 P5 的负载) | trace 录制 + IR 优化 + 寄存器分配 + snapshot + side trace(无处抄,LuaJIT 真护城河,详 [../p5-trace-jit.md](../p5-trace-jit.md)) | **否决**(§3.4 论证) |

核心论证:**实现成本与收益在这条曲线上严重凸性**——(a) 用最少的人年拿走最大的一块(dispatch 税 + 类型投机),且其简单性**向下传导**(§3.3 详)。这是 §0.1「P4 = dispatch 消除器 + IC 投机注入器」决定式的成本侧支撑。

### 3.2 方法级优化编译器的边际:列内核校准

[../roadmap.md](../roadmap.md) §1 / [../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一的两个校准测量是 (b) 否决的关键论据:

**校准测量 1**:Horner 5 次多项式 1000 items 同机同日:

| 档位 | 绝对值 | 相对 gopher-lua | 技术 |
|---|---|---|---|
| gopher-lua | 729μs | 1x(基线) | 解释 + interface 装箱 |
| LuaJ luajc | 164μs | ≈4.4x | Lua → JVM bytecode → C2 全套优化(= **方法级优化编译器档**) |
| LuaJIT | 154μs | ≈4.7x | trace JIT |

**真 LuaJIT 仅比 luajc 快 6%**(154 vs 164μs)——这一条数字直接否决 (b):

> 假设望舒 P4 模板编译已达 luajc 档(§7 验收锚点,详 [./00-overview.md](./00-overview.md) 验收节);**(b) 在 P4 之上的全部边际收益就只有「luajc 与 LuaJIT 之间的 6%」乘以「(b) 实际能拿到 trace 的几成」**——而 (b) 没有 trace 的关键武器(跨函数内联、控制流投机、分配下沉),拿不到 6% 全额,实际边际收益严重小于 6%。

**校准测量 2**:某生产规则引擎宿主开 luajc 后:隔离脚本级 -37%,但宿主端到端 ±5-7% 噪声带——边界主导形态下方法级优化收益被稀释到不可见。这是 [../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一的反面用法:即便 (b) 做出来,也会被生产形态的边界稀释。

**(b) 的人力成本**:数人年起。SSA IR 设计 + regalloc(线性扫描或图着色)+ 指令调度 + 与 deopt 机器(snapshot)的耦合(§3.3)+ 双架构(每架构都要重做 regalloc 调优)——任一环节工业级实现都是人月级投入。

**收益严重凸性**:**(b) 的人力 ÷ 模板编译 ≈ 数倍**;**(b) 的边际收益 ÷ 模板编译边际 < 6%**(因为模板编译已拿走主体)。曲线在 (a) 这一点之后严重凸,投入产出比快速跌落。

(论证沿用旧 §1.3 的核心思路,本节按候选/校准/收益曲线/成本结构四档逐项展开。)

### 3.3 拒绝方法级优化的核心论证:简单性向下传导

(b) 否决的最深论据不是「人力贵」(虽然贵),而是 (b) 的简单性破坏会**向下传导**到 deopt 机器,使整套机器复杂度上升一个量级:

```
模板编译(P4 选定):                             方法级优化编译器(否决):
                                                
单条字节码内取操作数 → 计算 → 写回                 跨多条字节码做寄存器分配
机器寄存器仅模板内短寿,不跨字节码边界               IR 值跨指令驻留机器寄存器,跨基本块流动
                                                
↓                                              ↓
                                                
栈槽边界处 = 解释器活值真相                          字节码边界处寄存器内有「IR 值」,栈槽内是脏的
(每条字节码模板写回栈槽才结束,详 §1.6 / §6 第 2 条)  (优化编译器的全部价值就在「不去写栈槽」)
                                                
↓                                              ↓
                                                
guard 布在模板开头,失败瞬间栈槽即完整状态            guard 失败时必须从「IR 值 → 栈槽」物化:
                                                  - 哪些 IR 值还活、住哪个寄存器、对应哪个栈槽
                                                  - 多帧内联的情况下还要重建调用链栈帧
                                                  - 编译期记录「IR 值 ↔ 栈槽」映射 = snapshot
↓                                              
                                                ↓
OSR exit = 写回 pc + 退出 JIT 世界                 OSR exit = 按 snapshot 把 IR 值还原到栈槽 +
(详 04 §3.3 「osrExit」骨架)                       重建多帧 + 写回 pc + 退出
零格式转换、零类型重建、零 snapshot 数据结构          每 guard 一份 snapshot 元数据(空间成本)
                                                  snapshot 解码器(代码成本)
                                                  snapshot 与 IR 优化的耦合(优化设计成本)
```

**「简单性向下传导」的精确含义**:模板编译用「不优化跨指令」换掉了整台 snapshot 机器——这不是「P4 偷工减料没做 deopt」,是 **P4 的结构本身让 deopt 不需要 snapshot**。代价是 P4 生成码保留栈槽内存往返(§1.6),但收益与代价的兑换率由 §7 验收锚点(luajc 档)裁决——前提一校准测量已显示真 LuaJIT 仅比 luajc 快 6%,P4 达到 luajc 档项目近期目标即兑现。

| 维度 | P4 模板编译 | (b) 方法级优化 | P5 trace JIT |
|---|---|---|---|
| 编译时间 | 微秒级,同步可行(§1.4) | 毫秒到秒级,后台编译 | 录制 + 优化 + 发射,同 (b) 量级 |
| Deopt 数据结构 | 无 snapshot(§3.3) | 每 guard 一份 snapshot | 每 guard 一份 snapshot,且更精细(详 [../p5-trace-jit.md](../p5-trace-jit.md) §4) |
| Deopt 物化复杂度 | memmove(§2.5.2 / 04 §3.2) | IR 值 → 栈槽,寄存器 → 栈槽,可能多帧 | 同 (b) + 跨 trace 边界 |
| 与上游优化的耦合 | 无(模板独立选型) | 强(每条优化都要算 snapshot 影响) | 强 + trace 黑名单 |

**这条「简单性向下传导」的对偶面**:[./04-osr-deopt.md](./04-osr-deopt.md) §3.3 把它落为 P4 第二条不变式「栈槽真相」,由其守。

### 3.4 拒绝跳过 P4 直接做 trace 的理由

(c) 是另一面候选——直接跳到 P5 trace JIT,把 +1-2 人年的 P4 投入直接转入 +2-4 人年的 P5。**否决依据有三**:

**第一,违反原则 3「每阶段独立交付」**([../roadmap.md](../roadmap.md) §5)。原则 3 字面:「每阶段独立交付价值,任何闸门处停下都不亏」。(c) 把 P4 与 P5 合并成一档「+3-6 人年到可信 v1」的开放式投入,违反原则 3——若 P5 中途因技术风险或人力转移停滞,望舒退到 P3 档,跳过 P4 = 永远没有 luajc 档的兑现路径。原则 3 的反面就是 (c)。

**第二,P5 启动条件「P4 不够时」自反约束**([../roadmap.md](../roadmap.md) §4 / [../p5-trace-jit.md](../p5-trace-jit.md) §0)。P5 文档自身明确:「**P5 不是计划,是期权**」「只在 P4 的收益不够时启动」——这条启动条件在 (c) 下变成自反:既然没有 P4,「P4 不够」永远不可观测,P5 是否值得做永远没有数据决定。

**第三,P4 的全部代码资产(模板表、guard、OSR exit、双架构发射函数)是 P5 的基建**(详 [../p5-trace-jit.md](../p5-trace-jit.md) §3 「与 P4 的关系:基建全复用,新增四件套」)——P5 复用 P4 的 exec mmap/W^X/icache/trampoline/自管栈/helper 表全套,只新增 trace 录制 + IR 优化 + 寄存器分配 + snapshot 四件套。即使 P5 终将启动,跳过 P4 ≠ 节省 P4 那 1-2 人年,而是把 P4 的工作并入 P5 第一阶段。**(c) 不省时间,只增加风险**。

**否决结论**:P4 是 P5 的前置基建 + 数据采集器 + 风险中位机。「直接跳到 P5」表面省一档,实际把所有不确定性堆在最大档投入上,违反原则 3 并自损 P5 启动判定的前提。

---

## 4. 边界表:P4 做什么 / 不做什么

本节扩展边界简表为四节:做的项展开论据 + 不做的项展开论据 + 与 P5 的对位 + 「投机叠在子集之内」的正交关系。

### 4.1 做(P4 边界内,展开)

| 项 | 形态 | 落具体的子文档 |
|---|---|---|
| **per-opcode 模板发射** | 单遍扫 + 模板贴出(§1.1);函数为编译单元;每架构数十段发射函数(§5.1) | [./06-backends.md](./06-backends.md) §3(发射函数实装) |
| **IC 反馈类型投机** | 消费 P2 `TypeFeedback`(详 [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md));feedback 高置信度 → 发投机模板(f64 快路径 + guard);低置信度 / mega → 发通用模板(等价解释器语义) | [./03-speculation-ic.md](./03-speculation-ic.md) §2-§4(feedback 种类 → 投机模板 → guard 形态) |
| **OSR exit 回解释器** | guard 失败 / 抢占检查 / 错误冒泡时,经 trampoline 退出 JIT 世界,从 exit 对应字节码 pc 起由 crescent 续跑;函数级粒度,不跨帧 | [./04-osr-deopt.md](./04-osr-deopt.md) §3 |
| **栈槽直存直取** | 虚拟寄存器 = arena 值栈槽(§1.2);机器寄存器只在单条模板内部短寿;字节码边界处全部活值物化在栈槽 | [./04-osr-deopt.md](./04-osr-deopt.md) §3.3「栈槽真相」不变式 |
| **amd64 + arm64 双后端** | 共享骨架(线性扫、IC feedback 决策、OSR exit 逻辑、`jitContext` 布局)+ per-arch 发射函数;wazero 同款组织 | [./06-backends.md](./06-backends.md) §1-§4(后端抽象与切分) |
| **沿用 P2 F1-F7 闸门** | P4 仍只编译静态可编译子集;F7 闸门`SupportsAllOpcodes`由 P4 后端实装替换 P3,渐进白名单同款保守缺省 | [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3 + [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1.3(P-Wasm 渐进表的 P-JIT 对位由 [./00-overview.md](./00-overview.md) 接力,本文不展开) |

每项的详细形态由各引用子文档落地;本节只列「P4 边界内做」清单。

### 4.2 不做(留 P5 或永不做)

| 项 | 不做原因 | P5 的对应武器或永不做理由 |
|---|---|---|
| **跨函数内联** | 模板编译单元是函数;调用走通用 CallInfo 协议(详 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §7),编译期看不见调用图 | P5 trace 跨调用边界录制,被调函数体内联进 trace([../p5-trace-jit.md](../p5-trace-jit.md) §1.2 第 1 类) |
| **投机失败的细粒度恢复(snapshot)** | 模板编译让「字节码边界 = 栈槽真相」(§1.6 / §6 第 2 条),OSR exit 退化为 memmove,无 snapshot 必要 | P5 必须 snapshot,因为 P5 在 IR 层把值留在寄存器、把分配下沉、把多帧内联,exit 必须靠 snapshot 重建([../p5-trace-jit.md](../p5-trace-jit.md) §4) |
| **循环不变量外提(LICM)/ CSE / 死代码消除** | 无 IR 看不见跨指令数据流(§1.6) | P5 IR 优化([../p5-trace-jit.md](../p5-trace-jit.md) §2 ②) |
| **跨指令寄存器分配** | 模板内寄存器只单条短寿(§1.2);regalloc 是 (b) 否决(§3.2)的核心成本项 | P5 IR 上做线性扫描寄存器分配([../p5-trace-jit.md](../p5-trace-jit.md) §2 ③);P4 内的窥孔级局部缓存(如 FORLOOP idx/limit/step 短暂寄存器驻留)是允许的实现自由度,但 exit 物化序列必须编译期静态生成,详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3 注 |
| **分配下沉 / 逃逸分析** | P4 不做逃逸分析;每轮迭代真实分配 | P5 分配下沉([../p5-trace-jit.md](../p5-trace-jit.md) §1.2 第 3 类) |
| **其它架构(riscv64/ppc64/s390x)** | 双后端已是双倍维护成本;再加一架构是工程线性成本 | P3(wasm 层)在 P4 验收后可保留为「可移植中层」覆盖未支持架构(详 [./07-p3-retirement.md](./07-p3-retirement.md) 决策框架);永不做新架构原生 codegen |
| **为 vararg / coroutine / debug 补编译路径** | 原则 4(不做完备性):F1-F7 闸门已把这些形状标「不可编译」走 fallback([../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3) | 永不做(原则 4) |
| **大函数编译** | F5 大函数闸门在 P2 已设([../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3.5);P4 不需要为巨函数特化模板(§7 风险 4 涉及 icache 反噬) | 沿用 P2 F5 闸门(永不做巨函数模板) |

### 4.3 与 P5 §1.2 的对位

[../p5-trace-jit.md](../p5-trace-jit.md) §1.2 列出的「method JIT 结构性吃不下的负载类别」表是 P4 不做清单的负载侧表达——本表的「不做项」对应该表的「P5 的猎物范围」:

| §4.2 「不做」项 | [../p5-trace-jit.md](../p5-trace-jit.md) §1.2 类别 | 对应负载形状 |
|---|---|---|
| 跨函数内联 | 第 1 类「跨函数热循环」 | 列内核循环体里每轮调小函数(比较器、per-row 回调、`obj:method()` 链) |
| LICM / CSE / 跨指令优化 | 第 2 类「循环携带的冗余」 | 循环体内不变的表查找、重复的 guard、跨迭代可复用的子表达式 |
| 分配下沉 / 逃逸分析 | 第 3 类「分配密集循环」 | 每轮迭代构造临时 table/字符串(中间结果打包、闭包逃逸) |
| (无对应,P4 也不做)| 第 4 类「megamorphic 调用点的稳定子集」 | P4 整点放弃投机(`FBTableMega` → 通用模板),P5 按实际路径特化 |

**P4 验收「列内核 ≥ luajc 档」**([../roadmap.md](../roadmap.md) §4)——验收负载是「循环体纯标量算术」,正是 [../p5-trace-jit.md](../p5-trace-jit.md) §1.2 表外的第 0 类(P4 已吃下)。**项目近期目标在 P4 兑现**;若验收后真实宿主负载主要落在 P5 §1.2 表内(尤其第 1 类),则 P5 立项评审开启,否则维持 P4 终态(详 [../p5-trace-jit.md](../p5-trace-jit.md) §1.3 量化口径预登记)。

### 4.4 P4 仍编译可编译子集 + 投机叠在之内

「不可编译形状走 fallback」与「可编译形状内做类型投机」是**正交的两层**(原则 4 不因 P4 而松动):

```
P2 F1-F7 闸门:静态分析 Proto,标 CompCompilable=true/false
  ┌─────────────────────────────────────────────────┐
  │ 可编译子集(Proto.CompCompilable=true):                
  │   ┌───────────────────────────────────────────┐
  │   │ 投机层(P4 内,叠加):                        
  │   │   IC feedback 高置信 → 投机模板(f64 快路径 + guard)
  │   │   IC feedback 低置信 / mega → 通用模板(等价解释器语义)
  │   │   guard 失败 → OSR exit 回解释(零错果,详 04)
  │   └───────────────────────────────────────────┘
  └─────────────────────────────────────────────────┘
  不可编译子集(Proto.CompCompilable=false):
    永远走 crescent 解释,P4 不接(F1 vararg / F4 coroutine / F6 debug.* 等)
```

P4 与 P3 的差别只在「有无投机层」一档([./00-overview.md](./00-overview.md) §0.2 表):

- **P3**:可编译子集内**不投机**,翻译输出与解释器逐字节同构,零 deopt(详 [../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §8 不变式 1)。
- **P4**:可编译子集内**投机**,引入 deopt 边(详 [./04-osr-deopt.md](./04-osr-deopt.md) §2)。

「投机叠在子集之内」的物理含义:即使某 Proto 在投机维度反复失败被 P4 端标 `P4StuckSpeculation`(P4 内部子状态,永久只发通用模板;P2 tierState 仍 `TierGibbous`,承方案 A,详 [./04-osr-deopt.md](./04-osr-deopt.md) §5.3),仍比解释快(dispatch 税照省)。**P4 的最坏场景 ≈ P3 的常规场景**——这是「投机叠加」非「投机替代」的工程红利。

具体形态由 [./03-speculation-ic.md](./03-speculation-ic.md) §1 落地(投机模板 vs 通用模板的接缝、降级路径)。

---

## 5. 模板编译落地的可行性论证

本节回答「+1-2 人年是否真的够」——不是说服读者代价低,而是说明**模板编译的工序在望舒约束下确实是有限工程**,不会失控。

### 5.1 模板总量评估

P4 模板总量的来源:

| 模板类别 | 数量 | 备注 |
|---|---|---|
| **opcode 模板**(per-arch) | **38**(P1 ISA 全集,详 [../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §4) | 减掉 F1-F7 不接的 opcode(主要是 VARARG)≈ **37 模板/架构** |
| **投机模板的 feedback 分支** | 算术族每条 ×2(投机 + 通用),表族每条 ×2-3(IC mono / IC mega / 通用),全局/方法访问族每条 ×2 | 合计 ~10-15 个 feedback-driven 分支,详 [./03-speculation-ic.md](./03-speculation-ic.md) §2 |
| **guard 与 OSR exit 胶水** | guard 比较序列(NaN-box / 同表代次 / 同 globals 代次) + exit stub(写回 pc + trampoline 出去) | ~5-10 段共享胶水/架构 |
| **trampoline 与系统管线** | 入口 trampoline + 慢路径 helper 调用桩 + 抢占检查点 + safepoint 序列 | ~5 段汇编/架构(详 [./05-system-pipeline.md](./05-system-pipeline.md)) |

**每架构数十段发射函数**——上限粗估:opcode 模板 37 + 投机分支 10-15 + 胶水 5-10 + 系统管线 5 ≈ **60-70 段**。两架构 = ~120-140 段。这是「人年级」工程量但**不是失控量级**——每段发射函数的复杂度是「写汇编 + 写测试」,人月级可承担。

对比 P3:[../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §3 的 opcode 翻译表加各分支与助手胶水也在同档量级,P3 PW1-PW9 在 6-12 人月内交付,P4 双架构同款工序 +1-2 人年估算合理。

### 5.2 编译时间预算

(承 §1.4 的展开,本节细化与升层时机的关系。)

| 维度 | 估计值 | 来源 |
|---|---|---|
| 单 Proto 编译时间 | 数微秒到数百微秒 | §1.4 表(线性扫描 + 模板 memcpy + 立即数填充) |
| 升层频率 | 升层是逐 Proto 一次性事件(P2 决策机驱动) | [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) §3 |
| 列内核形状下编译时间占比 | 远小于 1%(数千万次调用 × 10ns vs 一次升层 × 100μs) | 列内核形状的固定结构 |
| 同步编译可行性 | 可行(无后台编译 / 安装屏障 / 代际实例需求) | §1.4 红利 |

**升层时刻的几十到几百微秒同步编译,在列内核形状下完全可吸收**。[../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一的「列内核负载形状」前提与本节的「升层停顿可忽略」结论自洽——若负载不是列内核形状(per-item 跨界),升层频率与脚本本体执行时间不可分,编译时间预算需重新评估。但前提一的负载形状假设是项目所有 P3-P5 阶段共有的前提,P4 不引入新的形状要求。

### 5.3 模板设计的「分级表达」

37 个 opcode 模板按工序复杂度分四族,工程上按族开发以摊匀难度:

| 族 | opcode 集 | 模板特点 | 工程难度 |
|---|---|---|---|
| **算术族** | ADD / SUB / MUL / DIV / MOD / POW / UNM / NOT / EQ / LT / LE / TEST / TESTSET | 双 number 投机快路径(SSE/NEON 浮点)+ NaN 规范化 + guard;失败走慢路径助手 | 中(NaN 规范化是 byte-equal 关键,详 [./03-speculation-ic.md](./03-speculation-ic.md) §2.1) |
| **表 IC 族** | GETTABLE / SETTABLE / GETGLOBAL / SETGLOBAL / SELF / NEWTABLE / SETLIST | IC 命中:同表 + 同代次 + 直达槽 inline(JSC Baseline 同款,§2.4);失效降级走助手 | 高(翻译复杂度峰值,详 [./03-speculation-ic.md](./03-speculation-ic.md) §3) |
| **控制流族** | JMP / FORPREP / FORLOOP / TFORLOOP / LOADBOOL / LOADNIL / MOVE / LOADK / GETUPVAL / SETUPVAL / LEN / CONCAT / CLOSURE / CLOSE | 直线翻译 + 跳转回填 + 回边 safepoint | 低-中 |
| **调用族** | CALL / TAILCALL / RETURN | 跨层互调 + status 链错误冒泡(经 trampoline,详 [./05-system-pipeline.md](./05-system-pipeline.md) §4) | 高(协议复杂,P3 PW6 同款工程难点) |

「分级表达」的工程价值:**P-JIT 渐进里程碑可按族铺**(详 [./00-overview.md](./00-overview.md) 里程碑节展开,与 P3 PW1-PW7 的渐进白名单同款)——每个里程碑落一族,每族落地后跑全套差分套验证 byte-equal。失败原子性([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1.4)同款保留:F7 闸门(`SupportsAllOpcodes`)由 P4 后端实装替换 P3,在白名单未覆盖某 opcode 时返 false,该 Proto 不进入 P4 升层路径(可降级到 P3 wasm 层或解释器,具体降级路径由 [./00-overview.md](./00-overview.md) 落)。

### 5.4 与 P3 翻译器的相互独立性

P3 翻译表与 P4 模板各管各——**两套不共享代码**,只共享 P2 前端(决策机、TypeFeedback、F1-F7 闸门、TierState 状态机)。原因:

| 维度 | P3 翻译输出 | P4 模板发射 |
|---|---|---|
| 目标语义 | Wasm IR(WAT 形态) | 原生机器码(amd64/arm64) |
| 控制流表达 | 结构化(Wasm `block`/`loop`/`br_table`,relooper 算法) | 线性(机器 jmp + fixup 表) |
| 投机维度 | 不投机(零 deopt) | 投机(f64 快路径 + guard) |
| GC 根扫描 | wazero 维护 + arena 共见 | 自管(自管栈 + jitContext) |
| 寄存器约定 | wazero 自管 | 自管(per-arch 约定) |

**两套独立的工程红利**:P3 已落地的代码不阻塞 P4 设计;P4 设计不回头改 P3 实装;两套并行开发(若资源支持)。**两套独立的工程代价**:38 个 opcode 翻译两套各写一份——但这是「两套各自简单」换「不引入共享 IR」的合理权衡(同 §3.2 / §3.3 否决方法级优化编译器的同源论据:不引入 IR)。

P3 与 P4 共享的部分仍清晰:

| 共享项 | 物理形态 |
|---|---|
| P2 决策机 | 同 [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md) 的全套机制 |
| P1 字节码源 | 同 `Proto.Code` |
| 值表示 | 同 NaN-box u64 + arena 值栈段(§2.5.2) |
| F1-F6 闸门 | 同 [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3.1-§3.6(F7 由各自后端实装) |
| TypeFeedback 数据结构 | 同 [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md)(P3 不消费 / P4 消费) |
| 跨层 trampoline 协议 | 同 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(P4 trampoline 是其同构再实现,详 [./05-system-pipeline.md](./05-system-pipeline.md)) |

**这正是 §0.2 表「P3/P4 同 tier、共享前端、不同后端」的微观体现**——P4 设计可以专注后端发射形态,前端假设全部已被 P3 验证。

---

## 6. 不变式清单(本文承担)

本节是本文的事实主张总册。每条不变式由本文承担,具体落点要么本文章节要么下游子文档落实物——不变式的「形式化」由各落点守。

### 6.1 P4 = dispatch 消除器 + IC 投机注入器,不是优化编译器

**形式化**:P4 的全部价值挂在「消 dispatch + 注 IC 投机」二项上;**P4 不做**任何跨字节码边界的优化(IR、LICM、CSE、寄存器分配、调度、内联),也不引入 snapshot 数据结构。

**承本节**:§0.1 决定式 + §1.5 消除清单 + §1.6 不消除清单 + §2.2 Sparkplug 自我定位对位 + §3.1 候选谱系判定 + §4 边界表。

**违反检测**:P4 实装中若引入 IR 数据结构、跨基本块寄存器分配、跨函数内联展开、snapshot 元数据,即违反此不变式——要么是规范扩展(应升级为 (b) 路线,需评审推翻本不变式),要么是范围外的工程错位(应撤回)。

### 6.2 跨字节码边界 = 栈槽真相

**形式化**:每条字节码边界处,全部 Lua 活值都已物化在 arena 值栈槽中;模板把结果 store 回栈槽才结束;机器寄存器只在单条模板内部短寿。

**承本节**:§1.2 虚拟寄存器 = 栈槽 + §1.6 不消除栈槽往返。

**落具体**:[./04-osr-deopt.md](./04-osr-deopt.md) §3.3 由其守(具体形态、exit 物化序列、局部缓存自由度的边界条件)。本文不在此重复落点的具体形式;本文只声明「这条不变式由 P4 承担」。

**违反检测**:P4 模板中若有任何跨字节码边界的「寄存器内活值」(除 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3 注允许的 FORLOOP idx/limit/step 等局部缓存,且 exit 物化序列必须编译期静态生成),即违反此不变式。

### 6.3 P4 编译子集 = F1-F7 闸门子集

**形式化**:P4 仍只编译静态可编译子集;F1-F6 闸门完全沿用 P2 ([../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) §3.1-§3.6),F7 由 P4 后端实装替换 P3 但**保守缺省纪律不变**(白名单外一律返 false,渐进扩充);投机叠在该子集之内,不放宽闸门。

**承本节**:§4.4 「P4 仍编译可编译子集 + 投机叠在之内」+ §4.1 「沿用 P2 F1-F7 闸门」。

**违反检测**:若 P4 实装引入「P3 不接但 P4 接」的形状(如 vararg / coroutine 编译路径),即违反原则 4「不可编译/不可升层形状走 fallback,不做完备性」。

### 6.4 线性扫描 + 模板贴出 = O(N) 编译

**形式化**:`P4Compile(proto)` 的时间复杂度严格 O(N),N = `len(proto.Code)`;常数因子是「per-opcode 模板 emit + 立即数填充 + 偶发 fixup 登记」,无任何二次扫描或全图分析。

**承本节**:§1.1 工序伪码 + §1.4 编译时间线性 + §5.2 编译时间预算。

**违反检测**:若 P4 实装引入二次扫描(全 Proto 数据流分析、回填后再扫一次做窥孔优化等)使复杂度超过 O(N),即违反此不变式——窥孔级 guard 合并(§7 风险 2 缓解)只在线性扫期间做,不引入二次扫。

---

## 7. 风险与开放问题

本节列出 P4 方向裁决落地中已识别的工程不确定度;具体缓解或决议由后续子文档与实装期负责。

**风险:**

1. **模板设计反复(算术 NaN 规范化、guard 合并)的工程不确定度**——算术族模板的 NaN 规范化保证(IEEE 754 与解释器 byte-equal,详 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §4.1)在机器码层落地的指令序列首版可能反复;guard 合并(同一操作数在直线段内只查一次)是不引入 IR 前提下可做的窥孔级优化,但合并范围与边界需实测调。具体落地由 [./03-speculation-ic.md](./03-speculation-ic.md) §2 / §5 守,本文留口。

2. **双架构维护矩阵**——双后端(amd64/arm64) × 双架构 CI(物理 runner)是长期固定成本。若资源紧张,**arm64 滞后交付不阻塞 P4 验收**(验收平台定 amd64),但发布口径须如实标注「arm64 维护中」。详 [./06-backends.md](./06-backends.md) §5.2 落 arm64 滞后的应急方案。

3. **locals 寄存器跨指令缓存的开放**——§1.2 / §6 第 2 条「栈槽真相」允许 FORLOOP idx/limit/step 等循环局部热槽的短暂寄存器驻留(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3 注),但具体允许范围(只 FORLOOP 三槽 vs 扩展到其它循环局部热槽)与 exit 物化序列的具体形态留 [./06-backends.md](./06-backends.md) 后再评估。**P3 同款决议**([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §2.3 / §9 文档缺口):loop 核 memory-resident 已 2.58x over 解释器达标,真实瓶颈是跨层调用税而非寄存器访问;P4 同款保留「全栈槽直存直取 + 局部缓存预留扩展」,实测后再评估。

**开放问题(由后续子文档收口):**

- **F7 闸门的 P4 实装**:`SupportsAllOpcodes` 的 P-JIT 渐进白名单(对位 P3 PW1-PW7)由 [./00-overview.md](./00-overview.md) 给里程碑表,本文不展开。
- **deopt 计数阈值与去投机重编策略**:由 [./04-osr-deopt.md](./04-osr-deopt.md) §3 / §4 落具体。
- **同步编译 vs 后台编译的最终决议**:继承 P3 ([../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md) §1) 同款决策(基线同步),由 [./05-system-pipeline.md](./05-system-pipeline.md) §3 守,实装期可推翻。
- **多 State 并发下 P4 代码与 profile 的共享语义**:承 [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md) §9 同款并发缺口,P4 不引入新约束。

## 8. 回填请求(若有)

本文为方向裁决,无回填请求穿越本文向上游传递——上游契约(roadmap §4 / §7、design-premises 前提一/二/三/四、evolution-roadmap tier 映射、P1 02/05、P2 03/04、P3 02、P5 §1)已稳定,本文是其在 P4 子目录的展开,不要求修改。

---

相关:
[./00-overview.md](./00-overview.md)(P4 总览,本文遵守其章节番号与风格基线) ·
[./03-speculation-ic.md](./03-speculation-ic.md)(IC 反馈→f64 快路径 + guard,本文 §2.4 / §4.1 / §5.3 对位) ·
[./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化与 deopt 状态机,本文 §6 第 2 条「栈槽真相」由其落具体) ·
[./05-system-pipeline.md](./05-system-pipeline.md)(四项税兑现 + trampoline + 编译执行线程模型) ·
[./06-backends.md](./06-backends.md)(amd64/arm64 双后端发射函数与寄存器约定,本文 §5.1 / §7 风险 2/3 落具体) ·
[../p3-wasm-tier/02-translation.md](../p3-wasm-tier/02-translation.md)(P3 翻译器主体,本文 §0.2 / §2.4 / §5.4 对位) ·
[../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)(P3 闸门,本文 §0.2 跳跃路径触发参照) ·
[../p5-trace-jit.md](../p5-trace-jit.md)(P5 trace JIT,本文 §0.3 / §3.4 / §4.3 对位) ·
[../roadmap.md](../roadmap.md)(§4 P4 定义 / §7 prior art / §5 五原则,本文上游契约) ·
[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md)(源 ISA 38 opcode + IC slot,本文 §5.1 / §5.3 对位) ·
[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(主循环 + dispatch 形态 + arena 值栈槽寻址 + 调用协议,本文 §1 / §2.5 对位) ·
[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box 编码,本文 §2.5.2 对位) ·
[../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md)(F1-F7 闸门,本文 §4.1 / §4.4 / §6.3 对位) ·
[../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md)(P2/P3 零 deopt 状态机基线,本文 §4.4 对位) ·
[../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P4=gibbous tier-1 与 P3 同层) ·
[../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提二四项税 / 前提三五原则 / 前提四值表示承诺,本文上游)
