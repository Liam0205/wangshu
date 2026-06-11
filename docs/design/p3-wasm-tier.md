# P3:Wasm 编译层(gibbous/wasm:字节码→Wasm,wazero 执行)

> 状态:**设计阶段,详细设计**(依赖 P1/P2 落地与前置 spike 通过后细化;凡涉 wazero API 细节处标注「待 spike 验证」)。
> 本文是 tier-1 首个发射后端的单一事实源:开工闸门 spike、字节码→Wasm 翻译范式、
> 值世界=linear memory 的两层共见机制、跨层 trampoline 与互调协议、跨层 safepoint
> (收口 [06](./p1-interpreter/06-memory-gc.md) §12 缺口)、层间差分接入、四项税的 wazero 外包。
> 上游契约:`docs/design/roadmap.md` (§4 P3、§2 四项税)、[p2-bridge](./p2-bridge.md)
> (§5 try-compile-fallback 零 deopt、§6 TierState、§7.1 `P3Compiler` 接口——本文实现方)、
> [02-bytecode-isa](./p1-interpreter/02-bytecode-isa.md)(源 ISA)、
> [01-value-object-model](./p1-interpreter/01-value-object-model.md)(值编码,两层同一份)。
> 下游衔接:[p4-method-jit](./p4-method-jit.md)(§0.2 两条进入路径、§6 P3 去留决策矩阵)。

对应 Go 包:`internal/gibbous/wasm`(字节码→Wasm 编译器 + wazero 执行环境)。

---

## 0. 定位:在不用调试机器码的后端上,跑通整套分层机器

P3 = **gibbous / tier-1** 的第一个发射后端([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md):tier-1 同时覆盖 P3 与 P4)。它把 P2 判定「热且可编译」的 Proto 从字节码翻译成 Wasm,交 **wazero**(纯 Go Wasm 编译执行引擎,Apache 2.0)生成并执行机器码。

**战略价值不在倍率,在跑通分层机器**(`docs/design/roadmap.md` (§4)):升层/fallback/trampoline/跨层差分这套「分层骨架」第一次全链路运转,而后端是**不用调试机器码**的 wazero——系统管线(exec-mmap / W^X / icache / 抢占检查点)全部外包(§9)。P4 在此之上**只换发射后端**(Wasm 发射→原生发射,[p4-method-jit](./p4-method-jit.md) §0.2 常规路径)。

P3 继承 P2 的全部决策产物,自己只做「翻译 + 执行 + 跨层」:

| 关注点 | 归属 | P3 的角色 |
|---|---|---|
| 编译哪些 Proto(热度/可编译性) | P2([p2-bridge](./p2-bridge.md) §2/§4/§6) | 只接收,不判定 |
| 类型 feedback | P2 产出(§3.4) | **可选**消费,发更紧的 Wasm(§3) |
| 零 deopt(fallback 而非投机) | P2 决策(§5.1) | 严格遵守:快路径=语义分发,非投机 guard(§3) |
| 字节码→Wasm 翻译 | **本文** §2 | |
| linear memory 共见 | **本文** §4 | |
| trampoline / 互调协议 | **本文** §5 | |
| 跨层 safepoint | **本文** §6(收口 06 §12) | |

**验收**(`docs/design/roadmap.md` (§4)):循环密集脚本**相对 P1 再 ≥2x**;crescent/gibbous 两层差分 fuzz **逐字节一致**。坐标系警告见 §8。

---

## 1. 开工前置 spike:wazero call boundary 实测 <150ns(闸门)

### 1.1 闸门语义

`docs/design/roadmap.md` (§4):**「开工前置 spike:wazero call boundary 实测(目标 <150ns),不达标则跳过本阶段直接做 P4」**。这是 P3 的生死闸门,先于一切翻译工作执行。不达标走 [p4-method-jit](./p4-method-jit.md) §0.2 的「跳跃路径」(P4 自建分层骨架)。

### 1.2 测什么

「call boundary」= **Go ↔ wazero 生成码的一次往返**,样本形状覆盖三档:

| 样本 | 形状 | 对应真实场景 |
|---|---|---|
| S1 空往返 | Go 调一个空 Wasm 函数(无参无返回)返回 | 跨层固定成本下限 |
| S2 带参往返 | 传 2 个 i32(ciIdx/base),Wasm 读写 linear memory 各一次,返回 i32 status | gibbous 函数入口的真实形状(§5.2) |
| S3 反向往返 | Wasm 内 `call` 一个 imported Go 函数(空体)返回 | gibbous 调解释器助手/host 的形状(§5.3) |

工具:Go benchmark(`-benchtime` 足量、固定 CPU 频率),wazero 用**编译模式**(非解释模式)。S2 是主指标;S1/S3 用于定位成本构成。

### 1.3 为什么是 150ns:跨层摊销模型

`docs/design/roadmap.md` (§2) 推论:VM↔宿主边界是**几十~百 ns 固定成本**。gibbous↔crescent 边界的发生频率**高于**宿主边界(列内核下宿主边界每批一次,而跨层边界可能每次「调用未编译函数/慢路径助手」就一次),所以它必须**不贵于**宿主边界,150ns 是「百 ns 档上沿」的工程化阈值。

摊销模型(决定收益能否兑现):

```
设热循环每迭代执行 I 条指令,编译使每指令开销从 c_interp 降到 c_wasm,
每迭代发生 k 次跨层(慢路径助手/未编译被调/分配助手),每次 T_cross:

  每迭代净收益 = I·(c_interp − c_wasm) − k·T_cross

列内核理想形状:整个热闭包编译进同一 Wasm 函数,k ≈ 0(仅分配/IC miss 偶发),
  T_cross=150ns 摊到整批 N 个 item 可忽略 ⇒ 收益完整兑现。
劣化形状:循环体内每迭代都跨层(k ≥ 1),则 k·150ns 与 I·Δc 同量级,
  收益被吃光 ⇒ 这正是 §2「翻译单位覆盖整个热闭包」与 §3「IC 快路径内联避免跨层」的动机。
```

若 S2 实测 ≥150ns:跨层成本与宿主边界同档,「解释↔编译交错执行」形态死亡,只剩「整程序编译」一条路——而那条路 P4 原生后端做得更好(无 Wasm 语义中介),**直接跳 P4**。

### 1.4 三种出路

| spike 结果 | 决策 |
|---|---|
| S2 < 150ns 且 S3 同档 | 开工 P3(本文全部生效) |
| S2 ≥ 150ns | 跳过 P3 直做 P4([p4-method-jit](./p4-method-jit.md) §0.2 跳跃路径;本文 §2-§8 的分层协议设计仍被 P4 继承,只换发射) |
| 边缘值(150±30ns) | 混合策略:仅编译「自包含热闭包」(k≈0 形状,P2 可编译性分析加一条「调用密度」启发),交错形态不升层;跑 §8 验收基准定夺 |

> spike 同时顺带验证 §4 的 memory 共享机制与 §9 的四项税兑现(三件事一次 spike 全过,避免重复搭环境)。

---

## 2. 字节码→Wasm 编译器

### 2.1 翻译单位:一个 Proto → 一个 Wasm 函数;一次升层 → 一个 module(基线)

| 候选 | 优点 | 缺点 | 决策 |
|---|---|---|---|
| **每 Proto 一个 module**(每函数独立编译实例化) | 增量升层自然;失败隔离(单 Proto 编译失败只 fallback 自己) | module 实例化有固定开销;Proto 间直调要经 Go 中转 | **P3 基线** |
| 批量:N 个热 Proto 合一个 module | gibbous→gibbous 可 `call`/`call_indirect` 直调,免 Go 往返 | 升层时机不同步,批次划分引入策略复杂度;一损俱损 | 优化项,spike 后评估(§11) |

基线选「每 Proto 一个 module」:**正确性与隔离优先**(与 [05](./p1-interpreter/05-interpreter-loop.md) §2.2 选基线 switch 同一哲学)。实例化开销发生在升层时刻(一次性),计入 P2 的编译预算([p2-bridge](./p2-bridge.md) §2.5),不在热路径。

实现 [p2-bridge](./p2-bridge.md) §7.1 的接口:

```go
// internal/gibbous/wasm —— P2 §7.1 P3Compiler 的实现方
type Compiler struct { rt wazero.Runtime /* + 共享 imports、memory 适配,§4 */ }

func (c *Compiler) SupportsAllOpcodes(p *bytecode.Proto) bool
    // 后端成熟度闸门(P2 §4.3 F7):初期白名单渐进,目标=全 opcode
    // (被 P2 形状排除的 VARARG 等本就到不了这里)。
func (c *Compiler) Compile(p *bytecode.Proto, fb *bridge.TypeFeedback) (bridge.GibbousCode, error)
    // 产物 GibbousCode = wazero api.Function 句柄 + 元数据(protoID、编译时 IC 固化快照)。
    // error ≠ nil ⇒ P2 §5.3 编译失败 fallback(TierStuck,永久解释)。
```

### 2.2 寄存器映射:基线全 memory-resident,locals 缓存是受纪律的优化

Lua 寄存器 `R(i)` 在解释器里就是 arena 值栈槽([01](./p1-interpreter/01-value-object-model.md) §5.6、[05](./p1-interpreter/05-interpreter-loop.md) §1.3)。Wasm 侧两种放法:

| 方案 | 读写成本 | GC/解释器可见性 | 跨层切换 |
|---|---|---|---|
| **(A)linear memory 栈槽**(寄存器=共见值栈,与解释器同构) | 每次 `i64.load/store`(wazero 编译后是一次内存访问) | **天然共见**:GC 根扫描复用 R5([06](./p1-interpreter/06-memory-gc.md) §5.1),trampoline 零物化 | 零成本 |
| (B)Wasm locals(wazero 编译为机器寄存器/栈槽) | 寄存器级 | GC/解释器**不可见**,边界必须物化写回 | 每边界一次写回 |

**P3 基线选 (A) 全 memory-resident**:语义与解释器逐槽同构,byte-equal 差分零额外面;GC 根枚举不变;trampoline 协议(§5)退化为「传 base 偏移」。**收益来源不靠寄存器提升**,而靠消灭 dispatch 与译码:解释器每条指令付「取指 + switch 间接跳 + 操作数位运算」([05](./p1-interpreter/05-interpreter-loop.md) §2.1),编译后是**直线代码**,操作数是编译期立即数——这正是 [05](./p1-interpreter/05-interpreter-loop.md) §2.2 说「closure-threading 是 P3 翻译的中间形态」的完全体。

**(B) 作为受纪律的优化**(spike 后 A/B,口径同 [12](./p1-interpreter/12-testing-difftest.md) 的 dispatch spike:byte-equal 且不更慢才采纳):只对**循环局部热槽**(FORLOOP 的 idx/limit/step 三槽是首选)做 locals 缓存,纪律是——

> **任何 safepoint、任何跨层调用、任何可能读栈的助手调用之前,缓存的 locals 必须写回 memory 栈槽**(§6.3)。写回点由编译器静态插入,漏写回=GC 误根/解释器读脏,GC 压力 fuzz([12](./p1-interpreter/12-testing-difftest.md))是主防线。

### 2.3 代表 opcode 翻译示例(WAT 风格伪码)

函数签名(§5.2):`(func $proto_N (param $base i32) (result i32))`——`$base` 是本帧 R0 在 linear memory 的**字节偏移**,返回 status(`0=OK / 1=ERR`)。A/B/C 是编译期常量,落为静态 offset 立即数。

**MOVE A B**(一次 load+store,无装箱、无 dispatch):

```wat
(i64.store offset=8*A (local.get $base)
  (i64.load offset=8*B (local.get $base)))
```

**ADD A B C**(双 number 快路径 + NaN 规范化,语义与 [05](./p1-interpreter/05-interpreter-loop.md) §4.1 逐分支同构):

```wat
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vc (i64.load offset=8*C (local.get $base)))
(if (i32.and  ;; IsNumber×2:01 §3.2 的单比较,Wasm 直译
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then
    (local.set $r (f64.add (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc))))
    (if (f64.ne (local.get $r) (local.get $r))   ;; canonicalizeNaN(01 §3.4)
      (then (local.set $r (f64.reinterpret_i64 (i64.const 0x7FF8000000000000)))))
    (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r))))
  (else  ;; 慢路径:imported 助手回 Go(coercion/__add,07)——传编译期 pc 供错误定位
    (br_if $err (call $h_arith (local.get $base) (i32.const PC) (i32.const OP_ADD)))))
```

**FORLOOP A sBx**(热回边;FORPREP 已保证三槽 number,[05](./p1-interpreter/05-interpreter-loop.md) §10.1):

```wat
(local.set $idx (f64.add (f64.load offset=8*A     (local.get $base))
                         (f64.load offset=8*(A+2) (local.get $base))))
;; 方向敏感判界;step 是编译期常量时(常见)特化为单比较
(if (call $continue? ...)
  (then
    (f64.store offset=8*A     (local.get $base) (local.get $idx))
    (f64.store offset=8*(A+3) (local.get $base) (local.get $idx))
    ;; 回边 safepoint:仅检查标志,一次 i32.load + 分支(§6.2)
    (if (i32.load (global.get $gcPending)) (then (call $h_safepoint (local.get $base))))
    (br $L_body)))
```

**GETTABLE A B C**(P2 feedback 单态点:编译期固化 IC 快照内联,失效退助手):

```wat
;; 固化快照 = 编译时 IC slot 的 (tableRef, gen, kind, index)(02 §7、05 §6.2)
(local.set $t (i64.load offset=8*B (local.get $base)))
(if (i32.and (call $is_table (local.get $t))
             (i32.and (i64.eq (call $gcref (local.get $t)) (i64.const SNAP_TABLEREF))
                      (i32.eq (call $gen (local.get $t))   (i32.const SNAP_GEN))))
  (then ;; 同表同代次 ⇒ 直达槽(array/node 按 SNAP_KIND 静态选)
    (i64.store offset=8*A (local.get $base) (call $slot_load ...)))
  (else ;; miss/形状变了 ⇒ 完整查找+元方法,正确性不依赖快照
    (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
```

**CALL A B C**(统一经调度助手,基线;§5.3):

```wat
(local.set $st (call $h_call (local.get $base) (i32.const PC)
                             (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; status=0:返回值已回填 R(A..),直线继续
```

### 2.4 pc 物化:traceback 逐字节一致的前提

直线代码没有运行期 pc。**每个可能出错/调用/safepoint 的点,把编译期已知的 pc 作为立即数传给助手**(上例的 `(i32.const PC)`),助手写回 CallInfo.savedPC。这保证 gibbous 帧的错误位置、traceback([09](./p1-interpreter/09-errors-pcall.md))与解释执行**逐字节一致**——差分口径([12](./p1-interpreter/12-testing-difftest.md))不为 gibbous 开任何豁免。

---

## 3. IC 与 TypeFeedback:非投机消费(与 P2 零 deopt 口径严格一致)

[p2-bridge](./p2-bridge.md) §7.1 已定:P3 是 try-compile,**不依赖 feedback 正确性**。本文落实为两条纪律:

1. **快路径检查 = 语义分发,不是投机 guard**。§2.3 ADD 的 `IsNumber×2`、GETTABLE 的「同表同代次」与解释器快路径([05](./p1-interpreter/05-interpreter-loop.md) §4.1/§6.3)是**同一组判定**——失败走慢路径助手得到正确结果,**不存在 deopt**([p2-bridge](./p2-bridge.md) §5.1 的 fallback ≠ deopt 表)。feedback 只影响「内联哪条快路径、固化哪份 IC 快照」,即**代码形状**,不影响**语义覆盖面**。
2. **IC 快照编译期固化,失效自然降级**。解释器的 IC slot 是运行期可变的(mono IC 重填,[05](./p1-interpreter/05-interpreter-loop.md) §6.3);gibbous 把编译时刻的快照烧进代码。此后表形状变化(gen bump)→ 快照永久 miss → 该点每次走助手 ≈ 解释器无 IC 的水平,**正确但慢**。是否值得「IC 失效计数 → 重编译」留待 P4 一并评估(§11;P4 有同样问题且有 deopt 基建,统一解决)。

---

## 4. 值世界 = linear memory:两层共见的物理兑现

### 4.1 同一块内存、同一套编码、同一套偏移

[value-representation](../../llmdoc/architecture/value-representation.md) 的主线在 P3 落地:P1 的 arena **就是** Wasm linear memory。三层等义:

- **值编码**:NaN-boxed u64([01](./p1-interpreter/01-value-object-model.md) §3)在两层逐位同一——§2.3 ADD 示例的 `0xFFF8…` 比较直译自 `value.IsNumber`。
- **引用**:GCRef = 48-bit 字节偏移([01](./p1-interpreter/01-value-object-model.md) §2),在 Wasm 侧就是 linear memory 地址(offset 寻址),两层互换零翻译。
- **容量**:arena 的 `bump/cap` 是 uint32、单 arena ≤4 GiB([06](./p1-interpreter/06-memory-gc.md) §3)——**恰好匹配 wasm32 的 32-bit 寻址**,这是 P1 当时定 uint32 的隐性红利。

所以 crescent 写一个表槽、gibbous 读同一偏移,**无序列化、无拷贝、无影子副本**——「编译层是纯增量」(roadmap §3)的物理含义。

### 4.2 backing 归属:arena 收养 wazero memory(关键设计,需 06 配合)

[06](./p1-interpreter/06-memory-gc.md) §1.1 的 backing 是 Go 堆 `[]uint64`,§3 的 grow 是 `make+copy`。但 wazero 的 linear memory 由其 Runtime 持有(`api.Memory`),Wasm 侧 `memory.grow` 按页扩。两者只能留一个,否则就是两块内存(违背共见)。

**决策:P3 起,arena 的 backing 改为「收养 wazero Memory 的底层 buffer」**——NewState 时即经 wazero 分配 memory(P1 形态下 wazero 仅作 allocator,无模块运行),arena 的 `words/bytes` 视图从该 buffer 派生;grow 走 `memory.grow`(偏移寻址使 grow 后**所有 GCRef/链表/bump 一字不改**,[06](./p1-interpreter/06-memory-gc.md) §3 的红利原样保留,仅 Go 侧视图 slice 重取)。

> **对 06 的回填请求**:§1.1/§3 的 backing 来源需抽象为可替换(`newBacking(minBytes) ([]uint64)` 一个注入点),P1 实现为 `make`,P3 替换为 wazero memory adapter。**P1 写代码时就按此留口**,否则 P3 要在已固化的分配器里动手术。记入 doc-gaps(§11)。
> wazero 具体 API(import memory vs 宿主读 module memory、grow 的并发约束)**待 spike 验证**(§1.4 顺带项)。

### 4.3 Go 堆侧资产不进 linear memory

`Proto`/指令流/host 注册表住 Go 堆、经整数 ID 引用([01](./p1-interpreter/01-value-object-model.md) §1)——它们**不需要**进 linear memory:gibbous 代码不读 Lua 指令(已翻译),调 host 经 imported 函数(§5.3)。两层共见的范围**精确等于运行期值世界**,与 01 §1 的划分自洽。

---

## 5. 跨层 trampoline 与互调协议

### 5.1 协议总则:CallInfo 是唯一真相,跨层不换 ABI

调用链状态全住 arena 的 CallInfo([05](./p1-interpreter/05-interpreter-loop.md) §1.2),gibbous 帧**同样压 CallInfo**:复用 word2 的 reserved 位(bits [63:50])取 **bit50 = `callStatus_gibbous`**。参数/返回值全经共见值栈([02](./p1-interpreter/02-bytecode-isa.md) §3 的调用约定原样),跨层只传 `base`。**对 [05](./p1-interpreter/05-interpreter-loop.md) §1.2 的回填请求**:bit50 语义登记(只增不改),记入 doc-gaps(§11)。

### 5.2 crescent → gibbous(升层函数入口)

```
crescent 的 doCall(05 §7.1)增加一个分支:
  callee 是 Lua closure 且 protos[id].tierState == TierGibbous:
    1. 形参搬移/adjustVarargs 照旧(P2 形状排除已保证非 vararg)
    2. 压 CallInfo(标 bit50 gibbous)
    3. st := gibbousCode[id].fn.Call(ctx, uint64(baseBytes))   // 进 wazero,一次跨层
    4. st==OK:返回值已在 R(A..)(Wasm 侧 RETURN 助手按 nresults 回填),弹 CallInfo,继续解释
       st==ERR:vm.pendingErr 已置,走 05 §9 错误冒泡
```

被调 Wasm 函数从 `$base` 自取一切(参数在栈槽、常量烧在代码里)——入口协议只有一个 i32,这正是 spike S2 测的形状(§1.2)。

### 5.3 gibbous → crescent / host(imported 调度助手)

§2.3 CALL 示例的 `$h_call` 是 imported Go 函数,按被调者分派:

| 被调者 | 路径 | Go 栈变化 |
|---|---|---|
| gibbous(已编译) | 基线:Go 侧再 `fn.Call`(Wasm→Go→Wasm,两次跨层);优化:同 module 内 `call_indirect` 直调(§2.1 批量编译时,免 Go 往返) | +1 帧(基线) |
| crescent(未编译/TierStuck) | `vm.execute` 跑该帧(05 §7.3 的 fresh reentry),返回后回 Wasm | +1 execute |
| host fn | `callHost`(05 §7.6)原样 | 同解释器 |

**错误传播**:status 链。`$h_call` 返回非 0 ⇒ Wasm 函数清理后自身返回非 0 ⇒ 上层(crescent 或外层 Wasm)继续冒泡——与 [05](./p1-interpreter/05-interpreter-loop.md) §9 的显式错误返回**同构**,protected 边界(pcall)的清理责任不变([05](./p1-interpreter/05-interpreter-loop.md) §9.3)。错误可以任意穿越 gibbous 帧,因为冒泡是**单向放弃**,无需复原 Wasm 帧。

### 5.4 yield 不能穿越 gibbous 帧:线程级 tier 规则

[08](./p1-interpreter/08-coroutines.md) 路线 B 下,yield 信号靠 return 冒泡且**之后要 resume 复原**。错误穿 Wasm 帧可以(单向),yield 不行——core Wasm 无 continuation,**Wasm 帧无法挂起后复原**。这与「yield 不能跨 host(C)边界」是同一物理限制([08](./p1-interpreter/08-coroutines.md))。

若放任,会出现「解释执行能 yield、升层后同代码报错」的语义分裂——差分必炸。**P3 定稿:线程级 tier 规则——只有主线程的执行进入 gibbous;协程线程上调用一律走 crescent**(doCall 的 gibbous 分支多查一个 `th == mainThread`)。

- 代价:协程内代码永不升层。对列内核目标可接受:首个宿主(规则引擎)的批量 kernel 经 `Program.Call` 在主线程跑,协程是边角形态。
- 主线程 yield 本就非法(「attempt to yield from outside a coroutine」,[08](./p1-interpreter/08-coroutines.md)),故主线程上的 gibbous 帧**永远不会**被 yield 穿越——规则自洽。
- 备选:协程改 goroutine 路线([08](./p1-interpreter/08-coroutines.md) 的路线 A 兜底)可整体消解此限制,记入缺口(§11)。
- **对 08/P2 的回填请求**:08 增「gibbous 帧 = 不可穿越边界」一节;P2 §6.2 升层判定增线程上下文输入。记 doc-gaps。

---

## 6. 跨层 safepoint(收口 [06](./p1-interpreter/06-memory-gc.md) §12 缺口)

### 6.1 布点哲学不变:受控位置才允许 runtime 介入

[06](./p1-interpreter/06-memory-gc.md) §7.1 的两类 safepoint(分配点 + 层边界)在 P3 的具体形态:

| 位置 | 机制 |
|---|---|
| **分配点** | gibbous 代码**自身从不分配**——NEWTABLE/CONCAT/CLOSURE/rehash 全经 imported 助手回 Go(§2.3),分配与 GC 都发生在助手内(同 P1 的 Alloc 内同步 collect,[06](./p1-interpreter/06-memory-gc.md) §8.2)。助手返回时 GC 已完成,Wasm 侧无感(memory-resident 寄存器使根天然可见,§6.3) |
| **层边界** | crescent↔gibbous 的 trampoline(§5.2/§5.3)天然是检查点,与 [05](./p1-interpreter/05-interpreter-loop.md) 的调用边界 safepoint 同位 |
| **循环回边** | §2.3 FORLOOP 的 `gcPending` 检查:一次 `i32.load` + 几乎恒不跳的分支。覆盖「循环体内经助手分配置了 pending、但 collect 被推迟」的回收时机(对齐 05 §5.3 的 opcode 末尾检查) |

纯计算不分配的循环不触发 GC(没垃圾),与 [06](./p1-interpreter/06-memory-gc.md) §12 口径一致。**Go 调度器的异步抢占**是另一回事:那是 wazero 生成码自己的回边检查点(roadmap §2 税二,wazero 已验证),与我们的 gcPending 检查互不相干、各管各的。

### 6.2 GC 根:零新增机制

基线 memory-resident(§2.2A)下,gibbous 帧的活跃寄存器**就是** thread 值栈槽——[06](./p1-interpreter/06-memory-gc.md) §5.1 的 R5(running thread 栈 + CallInfo)**原样覆盖**,根枚举代码一行不改。这是基线方案最大的正确性红利。

### 6.3 locals 缓存的写回纪律(若启用 §2.2B 优化)

缓存进 Wasm locals 的值对 GC 不可见 ⇒ **任何可能触发 GC 的点(全部助手调用、回边 safepoint 命中)之前写回栈槽**。编译器静态插写回;遗漏由 GC 压力 fuzz 兜捕([12](./p1-interpreter/12-testing-difftest.md):每分配即 full GC 模式下,漏写回必现为脏值/误回收)。

### 6.4 写屏障与增量 GC:P3 不动

P3 的 GC 仍是 P1 的 STW full GC,写屏障接口维持空实现([06](./p1-interpreter/06-memory-gc.md) §9.4)。增量 GC(届时 gibbous 写表槽需插逻辑屏障)留 P3 之后单独评估——**P3 的范围刻意只换执行引擎,不动内存管理**(每阶段一块硬骨头,roadmap §5 原则 3)。

---

## 7. 层间差分:crescent vs gibbous 逐字节(CI 门禁)

roadmap §5 原则 2 在 P3 第一次有了「层间」含义([architecture](./architecture.md) §4 不变式 2):

- **接入 [12](./p1-interpreter/12-testing-difftest.md) 的 tier 矩阵**:同一 Proto 分别在「纯 crescent」与「强制 gibbous」下执行,可观察输出([12](./p1-interpreter/12-testing-difftest.md) 的口径总表原样适用,不为 gibbous 开新豁免)逐字节比对。
- **强制全量升层模式**:difftest 跑 gibbous 侧时绕过热度阈值(所有 CompCompilable 的 Proto 直接编译),消除「热度时序导致哪些函数被编译」的不确定性,保证可复现。
- **GC 压力 fuzz 同样上 gibbous**:逼出 locals 写回遗漏(§6.3)与助手内根登记缺漏。
- **持续 fuzz + CI 必过**:任何 crescent/gibbous 输出不一致都是阻断级 bug。P3 是 try-compile 非投机,差分验的是**翻译正确性**(比 P4 验投机正确性的面窄,这也是先 P3 后 P4 的风险阶梯)。

---

## 8. 验收与坐标系

| 验收项 | 门槛 | 口径 |
|---|---|---|
| 性能 | 循环密集脚本**相对 P1 再 ≥2x** | 列内核形状基准([12](./p1-interpreter/12-testing-difftest.md) 三档中的 loop 档),同机 A/B |
| 正确性 | 两层差分 fuzz **逐字节一致** | §7,CI 门禁 |

> **坐标系警告**([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)):流水线图的「4-8x」以 gopher-lua 为基线;验收门槛「≥2x」以 **P1 为基线**。两者不可混用(P1 自身 ≥2x over gopher-lua,链乘后量级自洽)。

---

## 9. 四项税的 wazero 外包(P3 选型的本质)

`docs/design/roadmap.md` (§2) 的四项税,P3 **一分自己的活都不干**:

| 税 | wazero 替我们解决的方式 | 望舒侧剩余义务 |
|---|---|---|
| GC 精确栈扫描 | Wasm 执行在 wazero 自管栈,Go GC 不扫生成码帧 | 无(值世界本就在 arena,§4) |
| 异步抢占 | wazero 生成码循环回边已有抢占检查点(roadmap §2「已验证」) | 无(§6.1 的 gcPending 检查是我们自己 GC 的事,另算) |
| 栈移动 | Wasm 栈不在 Go 栈上,morestack 与生成码无关 | 无 |
| 写屏障 | 值世界在 linear memory,生成码无 Go 指针写 | 无(P1 已兑现,§4 延续) |

**P3 选 wazero 的本质 = 把四项税外包给已验证的实现**,自己专注「翻译 + 分层协议」。P4 收回这层外包(原生发射),四项税才需要自己全额兑付([p4-method-jit](./p4-method-jit.md) §4)——届时 wazero 转为采石场(参考实现)而非依赖。P3 的去留(退役 vs 可移植中层)在 P4 验收时用数据定([p4-method-jit](./p4-method-jit.md) §6 决策矩阵,缺省倾向退役)。

---

## 10. 不变式清单(实现与差分须守)

1. **语义分发非投机**:gibbous 快路径判定与解释器同款(IsNumber/同表同代次),失败走助手而非 deopt——零 deopt([p2-bridge](./p2-bridge.md) §5)在代码层的兑现。
2. **值编码/GCRef 两层逐位同一**:Wasm 侧不引入任何私有值表示([01](./p1-interpreter/01-value-object-model.md) §7「值表示一次定死」)。
3. **CallInfo 唯一真相**:gibbous 帧压 CallInfo(bit50),跨层只传 base;traceback/错误定位与解释器逐字节一致(§2.4 pc 物化)。
4. **错误可穿越、yield 不可穿越**:status 链冒泡 vs 线程级 tier 规则(§5.3/§5.4)。
5. **基线 memory-resident**:寄存器=共见栈槽;locals 缓存必须满足写回纪律(§6.3)。
6. **升层单向**:gibbous 代码无运行期退回路径(零 deopt);fallback 都发生在编译前/编译时([p2-bridge](./p2-bridge.md) §5.3)。
7. **解释器永不退役**:任何 Proto 始终保有可解释字节码([architecture](./architecture.md) §4 不变式 1);gibbous 只是可选加速面。

---

## 11. 文档缺口 / 待决(记入 [memory/doc-gaps](../../llmdoc/memory/doc-gaps.md))

- **wazero memory 共享 API 细节**(§4.2):import memory vs 读 module memory、buffer 稳定性、`memory.grow` 后 Go 侧视图重取的精确时序——**待 spike 验证**(§1.4)。
- **对 06 的回填**(§4.2):arena backing 来源抽象为注入点(P1 `make` / P3 wazero adapter),P1 实现期就要留口。
- **对 05 的回填**(§5.1):CallInfo word2 bit50 `callStatus_gibbous` 登记。
- **对 08/P2 的回填**(§5.4):gibbous 帧不可穿越 yield;升层判定加线程上下文;协程 goroutine 化(08 路线 A)作为解除线程级限制的备选。
- **编译批次策略**(§2.1):每 Proto 一 module 的实例化开销实测;批量 + `call_indirect` 直调的收益边界。
- **locals 缓存的槽选择与写回插入算法**(§2.2B/§6.3):FORLOOP 三槽之外是否扩展,待基线数据。
- **IC 快照失效 → 重编译**(§3):失效计数与重编译预算,与 P4 的再训练机制([p4-method-jit](./p4-method-jit.md) §3.4)统一评估。
- **边缘 spike 值的混合策略**(§1.4):「调用密度」启发式进 P2 可编译性分析的精确定义。

---

相关:[p2-bridge](./p2-bridge.md) · [p4-method-jit](./p4-method-jit.md) · [p5-trace-jit](./p5-trace-jit.md) ·
[01-value-object-model](./p1-interpreter/01-value-object-model.md) · [02-bytecode-isa](./p1-interpreter/02-bytecode-isa.md) ·
[05-interpreter-loop](./p1-interpreter/05-interpreter-loop.md) · [06-memory-gc](./p1-interpreter/06-memory-gc.md) ·
[08-coroutines](./p1-interpreter/08-coroutines.md) · [11-embedding-arena-abi](./p1-interpreter/11-embedding-arena-abi.md) ·
[12-testing-difftest](./p1-interpreter/12-testing-difftest.md) · [architecture](./architecture.md) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) ·
[value-representation](../../llmdoc/architecture/value-representation.md) ·
[design-premises](../../llmdoc/must/design-premises.md)
