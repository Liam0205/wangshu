# P1 脊柱:解释器主循环 / dispatch / inline cache 执行

> 状态:**设计阶段,可实现深度**。本文是 tier-0 解释器(`internal/crescent`)执行引擎的**单一事实源**:
> dispatch 策略、执行帧/CallInfo 维护、逐 opcode 执行侧实现、inline cache 命中/失效机制、
> Lua-call-Lua reentrant loop、safepoint 布点、错误传播机制。
> 上游契约:[01-value-object-model](./01-value-object-model.md)(值/对象/Thread §5.6)、
> [02-bytecode-isa](./02-bytecode-isa.md)(opcode 表/调用约定/IC slot)。战略动机见 `roadmap.md` (§1/§4/§5)。
>
> **本文定稿三件被其它文档引用的机制**(02/01 把它们留给本文):
> ① IC shape 版本机制(per-table 单调代次 + 全局表版本号,§6);
> ② 错误传播机制(显式错误返回 + protected call 边界,**不用** panic/recover 跨循环,§9);
> ③ upvalue 关闭流程(open upvalue 降序链 + CLOSE/RETURN 关闭,§8.3)。

对应 Go 包:`internal/crescent`。

---

## 0. 本文在 P1 中的位置与目标

`crescent` 把前端产出的 `Proto`(见 [02](./02-bytecode-isa.md) §5)在 [01](./01-value-object-model.md) 的值世界上**跑起来**。它是 roadmap §5 原则 1「解释器永不退役」的物理载体——既是 P1 的唯一执行层,也是 P3/P4/P5 所有编译层的 **deopt 着陆点与语义 oracle**;`test/difftest` 把它当 byte-equal 基准(见 [12-testing-difftest](./12-testing-difftest.md))。

设计的全部张力来自一句话:**用纯 Go 把每条指令的开销压到能 ≥2x over gopher-lua**(roadmap §4 验收)。gopher-lua 慢在两处——interface 装箱(每个值一次堆分配 + 类型断言)与 switch dispatch 的分支预测失败。我们已经在 [01](./01-value-object-model.md) 用 NaN-box 干掉了装箱;本文负责 dispatch 与执行侧的每一处常数因子。**≥2x 的可达性论证见 §3.4**。

本文不重抄 [02](./02-bytecode-isa.md) 的 opcode 语义表,只给**执行侧关键实现**:快路径/慢路径如何分叉、IC 如何命中、帧如何切换、错误如何冒泡。

---

## 1. 执行帧(Frame)与 CallInfo

### 1.1 两层概念:Frame 是「活跃寄存器视图」,CallInfo 是「持久化调用记录」

Lua-call-Lua 不吃 Go 栈(§7)是本文的核心不变式,它要求**调用链的状态全部住 arena**(扣合 roadmap §2 栈移动税:「JIT 代码不持有指向 Go 栈的指针」——解释器同样不让调用链住 Go 栈,否则协程切换、未来 OSR 都要拷 Go 栈)。因此区分两个东西:

- **CallInfo**:每个活跃 Lua 调用一条记录,**住 Thread 的 CallInfo 数组**(arena 内,见 [01](./01-value-object-model.md) §5.6 `callInfoRef`)。它是调用链的持久状态,协程挂起后仍然存在。
- **Frame**:解释器主循环**当前正在执行**的那一帧的**热字段缓存**,是一个 Go 栈上的局部结构(或干脆是一组 Go 局部变量)。它把「当前 CallInfo + 当前 Proto + base 指针 + pc」缓存进 Go 寄存器/局部,避免每条指令都去 arena 读 CallInfo。

> 类比 Lua 5.1 `lvm.c` 的 `luaV_execute`:`L->ci`、`base`、`pc`、`k`(常量表)被提到循环顶部的局部变量,帧切换时重载——本文照搬这个「局部缓存 + 切换重载」模式,只是 CallInfo 物理上住 arena。

### 1.2 CallInfo 布局(住 arena,Thread.callInfoRef 指向的数组元素)

[01](./01-value-object-model.md) §5.6 给了 Thread 持有 `callInfoRef` + `ciTop/ciCap`,但把单条 CallInfo 的字段留给本文。定稿如下(每条 CallInfo = 4 字 = 32 字节,全部是普通整数/小字段,**不含 GCRef 指向 Go 栈**):

```
CallInfo[i]  (arena 内,32 字节):
  word0: [31:0] base    (本帧 R0 在 thread.valueStack 的绝对索引)
       | [63:32] funcIdx (被调用 closure 在栈上的绝对索引,= base-1 的约定见下)
  word1: [31:0] savedPC (返回本帧时恢复的 pc,即调用者发出 CALL 的下一条)
       | [63:32] top     (本帧逻辑栈顶:多值指令用,= base + 当前活跃寄存器数)
  word2: [31:0] protoID  (本帧 closure 的 ProtoID;host 帧为哨兵 0xFFFFFFFF)
       | [47:32] nresults (调用者期望的返回值个数;C-1,0xFFFF 表示「可变/到top」)
       | [48]    callStatus_tailcall (本帧是尾调用产生,RETURN 时特殊处理)
       | [49]    callStatus_fresh    (本帧是「reentry 边界」,见 §7.3)
       | [50]    callStatus_gibbous  (P3+:本帧在 gibbous 编译码中执行,承 [../p3-wasm-tier](../p3-wasm-tier/04-trampoline.md) §1;P1 恒 0)
       | [63:51] reserved
  word3: errfuncBase / 保护点字段(pcall 设置的消息处理器栈位,见 §9.3;0=无)
```

约定:**被调用 closure 自身存在 `valueStack[base-1]`**(即 `funcIdx = base-1`)。这与 [02](./02-bytecode-isa.md) §3「`CALL A B C`,被调函数在 `R(A)`」衔接——发起调用时 `R(A)` 在调用者帧里,搬移后它成为新帧的 `base-1`(§7.1 详述)。`base` 之下、`funcIdx` 之上是 vararg 溢出区(见 §8.5)。

### 1.3 Frame(主循环局部缓存,Go 侧,不入 arena)

```go
// Frame 不是 GC 对象,是主循环顶部的局部缓存;切帧时整体重载。
// 用值类型(非指针),让 Go 编译器尽量分配到寄存器。
type frame struct {
    pc     int32         // 当前指令下标(thread.code 内)
    base   int32         // 本帧 R0 的绝对栈索引(= 当前 CallInfo.base)
    ci     int32         // 当前 CallInfo 在 ci 数组的下标(ciTop-1)
    proto  *bytecode.Proto // 当前 Proto(Go 堆,缓存避免每指令解引用)
    cl     value.GCRef   // 当前 Lua closure 的 GCRef(取 upvalue 用)
    // 下面三个是「值栈 backing 的本地副本指针」,栈扩容(§1.5)后必须重取:
    stk    []uint64      // = arena 上 thread.valueStack 的 Go slice 别名(words 视图切片)
    k      []value.Value // = proto.Consts(常量表本地缓存)
    ic     []bytecode.ICSlot // = proto 的 IC 数组(按 pc 索引)
}
```

`stk` 是对 arena 中 Thread 值栈那段 `words` 的 Go slice 别名(通过 `unsafe` 在 arena 层构造,见 [06-memory-gc](./06-memory-gc.md) 的 arena 视图)。**寄存器访问因此是 `f.stk[f.base+a]` 一次切片索引**,无边界外解引用、无装箱——这是相对 gopher-lua 的核心常数因子优势。

> **关键纪律**:任何可能触发**栈扩容或 GC 搬迁**的操作(分配、CALL、CONCAT、可能 rehash 的 SETTABLE)之后,`f.stk` 必须从 Thread 重新取(因为 arena 整体搬迁会让旧 slice 失效)。本文在每个相关 opcode 标注「**重载 stk**」。实现上把「读 CallInfo.top/base 重建 frame」封装成 `reloadFrame(f)`;其对称操作 `saveFrame(f)`(把 frame 热字段 pc/top 写回当前 CallInfo)在协程挂起时使用(承 [08](./08-coroutines.md) §9.3 回填:yield 时 frame 缓存必须落回 arena,使挂起的 Thread 状态自包含)。

### 1.4 进入/退出帧 与 MaxStack 容量检查

进入一个 Lua 帧(被 §7.1 的 CALL 路径调用):

```
enterLuaFrame(callerTop, cl, base, nresults):
  proto := protos[cl.protoID]
  // 1. 栈容量检查:本帧最高水位 = base + proto.MaxStack
  ensureStack(thread, base + int(proto.MaxStack))   // 不足则扩容(§1.5)
  // 2. 形参已由调用方搬到 [base, base+NumParams)(§7.1);补 nil 到 MaxStack
  for r := numActualArgs; r < int(proto.MaxStack); r++ { stk[base+r] = value.Nil }
  // 3. 压 CallInfo
  ci := pushCallInfo(thread)
  ci.base, ci.protoID, ci.nresults = base, cl.protoID, nresults
  ci.savedPC = caller.pc            // 调用者断点
  ci.top = base + int(proto.NumParams) // 初始 top(多值指令再调)
  // 4. 重载 frame 局部缓存,pc 归零
  f.base, f.proto, f.cl, f.pc = base, proto, clRef, 0
  reloadFrame(f)
```

`ensureStack` 守 `MaxStack ≤ 250`([02](./02-bytecode-isa.md) §2 的 A=8bit 约束)与全局栈上限(防递归爆栈,Lua `LUAI_MAXCCALLS` 等价物,见 §7.4)。容量来源是 `Proto.MaxStack`——codegen 编译期算出的水位([02](./02-bytecode-isa.md) §9 不变式 5),解释器只需一次比较,**不在每条指令检查栈**。

退出帧见 §7.2(RETURN)。

### 1.5 栈扩容与开放 upvalue 修正

`ensureStack` 不足时,arena 重新分配更大的 `Value[]` 栈,memmove 旧内容,更新 `Thread.valueStackRef/stackCap`。**因为 valueStack 在 arena 内**(roadmap §2 栈移动税的兑现:解释器不持 Go 栈指针),搬迁是 arena 内 memmove,Go GC 不参与。搬迁后必须:

1. 重取所有 frame 的 `stk` slice(当前 frame 与——若未来支持——挂起 frame,但挂起帧靠 reload 自然恢复,只当前 frame 需立即重取)。
2. **开放 upvalue 用 `(threadRef, stackIdx)` 定位**(见 [01](./01-value-object-model.md) §5.4),`stackIdx` 是逻辑索引不是物理指针,**搬迁后无需修正**——这正是 [01](./01-value-object-model.md) §5.4 选「逻辑索引而非裸指针」的理由。若改用裸指针实现就必须在此遍历 openupval 链修正,逻辑索引省掉了这一步。

> 设计权衡:栈用「整段连续 + 扩容搬迁」而非「分段栈」,换取 `R(i)` 单次索引的最快路径;扩容是摊还罕见事件(MaxStack 已知,通常一次到位)。

---

## 2. dispatch 策略:纯 Go 下的务实选择

roadmap §4 点名「closure-compilation 或 computed-goto 风格 dispatch(替代大 switch)」。但 **Go 语言层面既没有 computed goto,也没有尾调用保证**(`gc` 编译器不做 TCO),这两个是 C/汇编解释器加速 dispatch 的标准手段,在 Go 里都不可用。必须务实重选。

### 2.1 三个候选与它们在 Go 上的真实代价

**(a) 大 `switch` on opcode(基线)**

```go
for {
    i := f.stk_code[f.pc]; f.pc++
    switch bytecode.Op(i) {
    case bytecode.MOVE: ...
    case bytecode.ADD:  ...
    // ... 38 个 case
    }
}
```

- Go 编译器对**稠密、从 0 起的整数 case** 生成**跳转表**(jump table),不是线性 if 链——实测 `go tool compile -S` 可见 `JMP (AX)(CX*8)` 形式。我们的 opcode 是 `iota` 连续 0..37([02](./02-bytecode-isa.md) §4),正好命中跳转表条件。
- 代价:每次循环一次间接跳转,CPU 分支目标预测器(BTB)对解释器这种「跳转目标几乎随机」的模式**预测命中率低**——这是 gopher-lua 也付的税。但 Go 的跳转表已经避免了「线性 if 链」的更坏情况。
- 优点:**最简单、最易保证正确**;调试器友好;一个函数内所有指令逻辑可见,寄存器化的 `pc/base` 等局部不跨函数边界。
- **这是 P1 的落地基线**。

**(b) closure-threading / direct-threading:预解码成 `[]func(*frame)`**

把每条指令在「装载 Proto 时」预解码成一个闭包,执行时遍历调用:

```go
type instrFn func(vm *VM, f *frame) (next int32) // 返回下一 pc
// 装载期:proto.Code → []instrFn,操作数预提取进闭包捕获变量
decoded := make([]instrFn, len(code))
// 执行期:
for {
    f.pc = decoded[f.pc](vm, f)
}
```

这就是 roadmap 说的「closure-compilation」雏形——把「取指 + 译码」从热循环移到装载期,热循环只剩「调用 + 取下一 pc」。

- **真实代价(Go 特有)**:每条指令是一次**Go 函数间接调用**(`CALL (reg)`)。Go 的函数调用不是零成本——有调用帧建立、参数/返回值传递(虽然可内联部分,但闭包数组里的 `instrFn` 是**间接调用,Go 编译器无法内联**)。所以它用「一次间接 call」换掉「一次取指 + 一次 switch 间接跳转 + 操作数解码」。
- **是否更快不确定**:在 C 里 direct-threading 赢在「每条指令末尾直接 `goto *next`」省掉 switch 的范围检查与单一回边的预测瓶颈;但 Go 里它退化成「间接 call」,而间接 call 与 switch 的间接 jmp **在现代 CPU 上预测代价相近**,却多了 call 的栈/ABI 开销。**净收益主要来自「译码外提」而非「dispatch 本身」**。
- **额外收益(为什么仍要保留它)**:① 译码外提对**操作数复杂的指令**(IC 指令、CALL)收益明显——闭包捕获里可以预存 `A/B/C` 解码结果、IC slot 指针,省掉每次执行的位运算;② **它是 P3 字节码→Wasm 翻译的中间形态**——把指令变成「可独立调用的单元」正是翻译的前一步,P1 做了等于给 P3 探了路(roadmap §4「翻译为 Wasm locals 也直白」的工程铺垫)。
- 代价补充:闭包数组本身要内存(每 proto 一份 `[]instrFn`),且闭包捕获变量可能逃逸到堆——要小心设计成**捕获索引而非捕获指针**,减少 GC 压力。

**(c) 预解码 + 循环内 switch 混合**

预解码出一个**操作数已拆好的结构数组** `[]decodedInstr{op, a, b, c, ic*}`,但 dispatch 仍用 switch:

```go
type decodedInstr struct { op uint8; a uint8; b, c uint16; ic *bytecode.ICSlot }
for {
    d := &decoded[f.pc]; f.pc++
    switch d.op { case MOVE: ...; }   // 操作数已拆,直接用 d.a/d.b/d.c
}
```

- 拿掉了「每指令位运算解码」(a/b/c/IsK 的移位掩码),保留 switch 的简单性。
- 代价:`decodedInstr` 比 `uint32` 大(8 字节 vs 4 字节),cache 占用翻倍——对 cache 敏感的紧循环可能反而变慢。
- 是 (a)→(b) 之间的折中,**风险最低的提速尝试**。

### 2.2 P1 选定方案

**P1 基线选 (a) 大 switch**,理由:正确性优先(它是语义 oracle 与差分基准,bug 代价最高),且 Go 跳转表已避免最坏情况。**(b) closure-threading 作为 P1 内的提速 spike**,在基线 byte-equal 通过后再做 A/B,因为:① 它是 roadmap 点名的方向;② 它同时是 P3 翻译的中间形态,投入不浪费;③ 若 spike 证明 (b) 不够,(c) 是兜底。

落地纪律(三方案共享、不被 dispatch 选择绑死):

- **执行逻辑写成可复用的 helper**(如 `doArith`、`doGetTable`),switch case 与未来的 closure 都调它——换 dispatch 不改语义实现,保住 oracle 唯一性。
- **IC slot、常量表在装帧时缓存进 frame**(§1.3 的 `ic/k`),三方案都受益。
- spike 的成败口径写进 [12-testing-difftest](./12-testing-difftest.md) 的基准:**(b)/(c) 必须 byte-equal 于 (a),且在三档脚本(简单/算术/循环)上不慢于 (a)** 才采纳。

> doc-gap:closure-threading 的具体闭包签名、捕获策略、与 IC slot 指针的绑定方式,留到 P1 提速 spike 阶段定稿(§10)。本文先锁定基线 (a) 可实现。

### 2.3 主循环骨架(取指→译码→执行→safepoint)

基线 (a) 的骨架(伪 Go,省略错误处理细节见 §9):

> **签名注**:本节及 §12 以 `(err *LuaError)` 返回为基线展示。协程接入后([08](./08-coroutines.md) §3.3 回填),返回值**泛化为三态 `executeSignal`**(sigReturn / sigYield / sigError)——错误返回是其中 sigError 分支,机制不变;`callHost` 相应增加 `callYield` 信号分支(与 §9.4 检查 `pendingErr` 对称)。本文余下部分按 `*LuaError` 行文,读作 sigError 即可。

```go
func (vm *VM) execute(th *Thread) (err *LuaError) {
    f := vm.loadTopFrame(th)        // 从 ciTop-1 重建 frame 局部缓存
    code := f.proto.Code
    for {
        i := code[f.pc]
        f.pc++                       // pc 先自增:JMP/FORLOOP 的 sBx 以「下一条」为基准([02] §9-1)
        switch bytecode.Op(i) {

        case bytecode.MOVE:          // R(A) := R(B)
            a, b := bytecode.A(i), bytecode.B(i)
            f.stk[f.base+a] = f.stk[f.base+b]

        case bytecode.ADD:           // 算术快路径,见 §4.1
            if e := vm.doArith(f, i, opAdd); e != nil { return vm.throw(f, e) }

        case bytecode.CALL:          // 见 §7.1;可能切帧 → 重载 code/f
            switch vm.doCall(f, i) {
            case callEnteredLua:     // 进入了一个新 Lua 帧
                code = f.proto.Code  // 重载指令流(reentry,不递归 Go 栈)
            case callReturnedHost:   // host 函数已同步执行完,返回值已回填
            case callError:
                return vm.throw(f, vm.pendingErr)
            }

        case bytecode.RETURN:        // 见 §7.2;可能退到调用者帧或彻底返回
            if vm.doReturn(f, i) == returnToCaller {
                code = f.proto.Code  // 退帧后重载调用者指令流
            } else {                 // 顶层帧返回 → 整个 execute 结束
                return nil
            }

        // ... 其余 opcode

        }

        // safepoint:仅在「本轮指令可能触发分配」时检查(§5)。
        // 实现上把该检查内联进会分配的 opcode 末尾,而非每轮都查(见 §5.2)。
    }
}
```

注意 **`code` 是 Go 局部变量,切帧时显式重载**——这就是「Lua-call-Lua 在同一个 Go 栈帧里 reentry」的机制(§7):没有递归调用 `vm.execute`,只是改了 `f` 和 `code` 继续循环。

---

## 3. dispatch 与 ≥2x over gopher-lua 验收的关系

roadmap §4 P1 验收:**简单/算术/循环三档脚本全部 ≥2x over gopher-lua**。我们靠两个独立来源叠出 2x,**dispatch 只是其中之一且不是主力**:

### 3.1 主力:NaN-box 去装箱(已由 [01](./01-value-object-model.md) 兑现)

gopher-lua 的 `LValue` 是 Go interface,每个数字值是 `LNumber(float64)` 装箱——**堆分配 + 接口类型断言**。一个 `s = s + i*i` 在 gopher-lua 里:`MUL` 产生一个新 `LNumber`(可能逃逸分配)、`ADD` 再产生一个、类型断言两次。我们这边:

- 值是 `uint64`,在值栈(arena)里**原地读写,零分配**;
- 类型判定是**单次 `v < 0xFFF8…` 比较**([01](./01-value-object-model.md) §3.2),不是接口断言。

这一项在算术/循环档就能拿到大头——roadmap §3 明说「NaN-boxing 数字零分配,本身就显著快于 interface 装箱」,§1 把 gopher-lua 的慢归因到「interface 装箱,switch dispatch」**两者并列**。

### 3.2 辅力:更好的 dispatch + IC

- **IC**(§6)让 `GETGLOBAL`/`GETTABLE` 命中时从「哈希查找」降为「一次版本比较 + 直达槽位索引」——gopher-lua 每次表访问都走完整哈希查找无缓存。算术 IC + 全局/表 IC 在「循环里反复读同一全局函数/同一表字段」的形状(列内核典型)收益显著。
- **dispatch**:即便 P1 只用基线 (a),Go 跳转表 ≈ gopher-lua 的 switch,**不亏**;(b)/(c) spike 若成功是**净赚**。换言之 dispatch 选择对 2x 的贡献是「不拖后腿 + spike 上行空间」,**主力是 §3.1**。

### 3.3 frame 局部缓存 + 寄存器栈

- `pc/base/proto/k/ic` 提到 Go 局部(§1.3),热循环里是寄存器访问;
- `R(i)` 是 `f.stk[base+i]` 单次切片索引,对比 gopher-lua 的 `L.reg.array[...]`(也类似)但我们的元素是裸 `uint64` 不是 interface——**取一个寄存器值不触发任何接口拆箱**。

### 3.4 可达性结论

三档:**简单**(MOVE/LOADK/比较/跳转为主)主要吃「去装箱 + 跳转表 dispatch」;**算术**额外吃「f64 直算零分配 + 算术 IC」;**循环**额外吃「FORLOOP 回边零开销 + 表/全局 IC 在循环内复用」。**去装箱单独大概率已接近或越过 2x,IC 与 dispatch 把循环/列内核档进一步拉开**。这与 roadmap §4「止步于此也成立:一个更好的 gopher-lua」自洽。若实测某档不达 2x,提速顺序是:先上 (c) 预解码省位运算 → 再上 (b) closure-threading,而**不是**改值表示(值表示是第 1 天不可逆承诺,[01](./01-value-object-model.md))。

---

## 4. 算术与一元/比较/CONCAT 执行侧

### 4.1 算术快路径(ADD/SUB/MUL/DIV/MOD/POW)

模式统一:**两操作数都是 number → 直接 f64 运算 + canonicalizeNaN;否则元方法路径**。

```go
func (vm *VM) doArith(f *frame, i Instruction, op arithOp) *LuaError {
    b := f.rk(i, B(i))   // RK(B):B<256 取 f.stk[base+B],否则取 f.k[B-256]
    c := f.rk(i, C(i))   // RK(C) 同理
    if value.IsNumber(b) && value.IsNumber(c) {     // 快路径:单两次 < 比较
        x, y := value.AsNumber(b), value.AsNumber(c)
        var r float64
        switch op {
        case opAdd: r = x + y
        case opSub: r = x - y
        case opMul: r = x * y
        case opDiv: r = x / y                        // /0 得 ±Inf(Lua 浮点语义,[02] §4-15)
        case opMod: r = luaMod(x, y)                 // a - floor(a/b)*b,见下
        case opPow: r = math.Pow(x, y)
        }
        f.stk[f.base+A(i)] = value.NumberValue(r)    // NumberValue 内含 canonicalizeNaN([01] §3.4)
        // 算术 IC:记录「本 slot 两操作数都是 number」供 P2/P4 类型 feedback([02] §7)
        f.ic[f.pc-1].recordArithNumber()
        return nil
    }
    return vm.arithMeta(f, i, op, b, c)              // 慢路径:__add/__sub/...,见 §4.2
}
```

要点:

- **`canonicalizeNaN` 收敛在 `value.NumberValue`**([01](./01-value-object-model.md) §3.4):任何算术产生 NaN(如 `0/0`、`Inf-Inf`)都规范成 `0x7FF8…`,绝不让负 NaN 渗回值世界(否则会被误判成 boxed tag,破坏 [01](./01-value-object-model.md) §3.4 不变式)。x86/arm64 默认产正 qNaN,`NumberValue` 的 `f!=f` 兜底覆盖罕见来源。
- **`MOD` 语义**:`luaMod(a,b) = a - math.Floor(a/b)*b`([02](./02-bytecode-isa.md) §4-16,Lua 5.1 语义,**不是** Go 的 `math.Mod` 截断取余)。`b==0` 时 `a/b` 为 ±Inf,`floor(±Inf)` 为 ±Inf,结果 NaN——与 Lua 5.1 一致,由差分测试钉死。
- **`POW`**:`math.Pow` 语义([02](./02-bytecode-isa.md) §4-17),`2^0.5` 等。
- **算术 IC 不影响快路径取值**,只记录类型分布(命中即「都是 number」),是给上层供料的旁路写,P1 自身不读它做分支(P1 的快路径判定就是现场 `IsNumber`,无需 IC 加速——算术 IC 纯为 P2/P4),见 §6.4。

> **字符串自动转数字**:Lua 5.1 算术对「能转数字的字符串」(如 `"10"+5`)做隐式转换。这走慢路径 `arithMeta` 的**前置 coercion**:若操作数是 string 且 `tonumber` 成功,则取转换后的数字重算快路径;否则才查元方法。详细 coercion 规则与 `__add` 查找顺序见 [07-metatables-metamethods](./07-metatables-metamethods.md);本文只定「快路径 = 双 number,其余下放 §4.2」。

### 4.2 算术慢路径(元方法)

```
arithMeta(f, i, op, b, c):
  1. 尝试字符串→数字 coercion(见上);成功则回快路径算。
  2. 否则按 Lua 5.1 顺序找元方法:先 b 的 metatable[__add],再 c 的;
     找到 → 以 (b, c) 为参数调用该函数(可能是 Lua closure → §7.1 reentry,
     可能是 host → §7.5),结果落 R(A)。
  3. 都没有 → 抛错 "attempt to perform arithmetic on a <type> value"(§9)。
```

元方法查找/调用的完整定义在 [07-metatables-metamethods](./07-metatables-metamethods.md);**本文负责的是「慢路径会发起一次函数调用,因此可能 reentry 或调 host,可能触发栈扩容/GC」** —— 调用返回后必须 **重载 stk**(§1.3 纪律)。

### 4.3 一元:UNM / NOT / LEN

- **UNM**(`R(A):=-R(B)`):`IsNumber(R(B))` → `NumberValue(-AsNumber(b))`(注意 `-0.0`);否则 `__unm`。
- **NOT**(`R(A):=not R(B)`):**无元方法**,纯真值取反:`BoolValue(!Truthy(R(B)))`([01](./01-value-object-model.md) §6 真值:仅 nil/false 为假)。
- **LEN**(`R(A):=#R(B)`):
  - string → 长度(从 String 对象 word1 的 len 字段读,[01](./01-value-object-model.md) §5.1);
  - table → border(`#` 二分,[01](./01-value-object-model.md) §5.2 的 `#` 语义);
  - userdata → `__len`([02](./02-bytecode-isa.md) §4-20 注:`__len` 在 5.1 仅 userdata 生效;table 直接取 border,**不查 `__len`**——这是 5.1 与 5.2+ 的关键差异,roadmap §6 锁定 5.1);
  - 其它类型 → 错 "attempt to get length of a <type> value"。

### 4.4 比较指令 EQ / LT / LE(与条件跳配对)

[02](./02-bytecode-isa.md) §4 把比较编码成「比较 + 紧跟 JMP」两指令对(§9-3 不变式)。执行侧:

```go
case bytecode.LT:    // if (RK(B) < RK(C)) ≠ bool(A) then pc++  (跳过下一条 JMP)
    b, c := f.rk(i, B(i)), f.rk(i, C(i))
    res, e := vm.lessThan(b, c)        // 快路径见下;可能 __lt(慢)
    if e != nil { return vm.throw(f, e) }
    if res != (A(i) != 0) {
        f.pc++                          // 条件不满足:跳过紧随的 JMP
    }
    // 否则正常落到下一条 JMP,由 JMP 执行 pc += sBx
```

- **EQ**:先 `rawequal`([01](./01-value-object-model.md) §6:number 走 `AsNumber` 浮点比较以正确处理 `+0/-0`/`NaN`;string 走 GCRef 相等;其它 boxed 走 bits 相等)。**仅当两操作数同为 table 或同为 userdata 且 rawequal 为假**时才查 `__eq`(Lua 5.1 要求两者同类型且各自有 `__eq`,[01](./01-value-object-model.md) §6),见 [07](./07-metatables-metamethods.md)。
- **LT/LE 快路径**:两 number → 直接 `<` / `<=`;两 string → 字典序(逐字节,[01](./01-value-object-model.md) §5.1 内容)。**注意 `NaN` 的比较**:`NaN < x`、`NaN <= x`、`x < NaN` 全为 false(IEEE 语义,Go 的 `<` 天然满足),无需特判。
- **混合类型 LT/LE**:number vs string **不自动转**(与算术不同!Lua 5.1 比较不做 string↔number coercion),直接走 `__lt`/`__le`,没有则错 "attempt to compare number with string"。这条易错,差分测试重点覆盖。
- **`bool(A)`** 是 codegen 编码进 A 的「期望布尔」([02](./02-bytecode-isa.md) §4 记号),用于把 `a < b`(期望 true 才不跳)与 `a >= b`(把 `<` 取反编码)统一成同一条 LT。解释器只做 `比较结果 ≠ bool(A) ⇒ pc++`,不关心它是哪种源码比较。

### 4.5 TEST / TESTSET(and/or 短路)

- **TEST**(`if Truthy(R(A)) ≠ bool(C) then pc++`):纯真值测试,无取值无元方法。
- **TESTSET**(`if Truthy(R(B))==bool(C) then R(A):=R(B) else pc++`):真值匹配则传值,否则跳过下一条 JMP。两者都只用 [01](./01-value-object-model.md) §6 `Truthy`。

### 4.6 CONCAT(右折叠)

`R(A) := R(B) .. R(B+1) .. … .. R(C)`([02](./02-bytecode-isa.md) §4-21),**右结合**。执行侧关键:

```
doConcat(f, i):  // B..C 是一段连续寄存器
  // 1. 快路径:若 [R(B)..R(C)] 全是 string 或 number,
  //    先算总长,一次性分配结果 String(arena),从右向左拷贝拼接 → intern。
  // 2. 数字转字符串用 Lua 5.1 的 "%.14g" 格式(LUAI_NUMFMT 等价),
  //    保证与差分基准逐字节一致。
  // 3. 慢路径:遇到非 string/number 操作数,从右向左两两折叠,
  //    对每对触发 __concat 元方法(右结合:先合并最右两个),见 [07]。
  // 4. 结果 intern 后落 R(A)。
```

要点:

- **右结合**体现在元方法触发顺序:`a..b..c` 在有元方法时等价 `a..(b..c)`,先合 `(b..c)`。但**全 string/number 的快路径**可以一次线性拼接(无可观察的结合性差异,纯优化)。
- CONCAT **会分配新 String** → 是 **safepoint**(§5)且 **结果 intern**([01](./01-value-object-model.md) §5.1 全 intern);拼接后 **重载 stk**(分配可能搬迁 arena)。
- 数字→字符串格式 `%.14g` 必须与 gopher-lua/官方一致,否则 `tostring(0.1)` 之类差分失败,见 [12](./12-testing-difftest.md)。

---

## 5. safepoint:GC 在哪些 opcode 边界检查

### 5.1 布点原则(扣合 roadmap §3 与 [design-premises](../../../llmdoc/must/design-premises.md))

roadmap §3 / 前提四锁定:**safepoint 限定在分配点与层边界,根放 shadow stack**。落到解释器主循环,GC 只可能在**两类边界**介入:

1. **分配指令**:执行后可能触发一次堆分配(arena bump 越过阈值)→ 该 opcode 末尾是 safepoint。
2. **调用边界**:CALL/TAILCALL/RETURN 进出帧——天然是「层边界」,也是 GC 安全检查的自然位置。

**为什么不能每条指令都设 safepoint**:① 多数指令(MOVE/算术/比较/跳转)不分配,设了是纯开销;② safepoint 要求把活跃寄存器作为 GC 根可见——若每指令都暴露根,代价极高。限定在分配点意味着「只有可能制造垃圾的地方才需要可能回收」,这是精确且省的。

### 5.2 哪些 opcode 是 safepoint(分配点清单)

| opcode | 为何分配 | safepoint 处理 |
|---|---|---|
| `NEWTABLE` | 新建 Table(array+hash 区) | 分配后检查 GC;**重载 stk** |
| `CONCAT` | 新建结果 String | 同上 |
| `CLOSURE` | 新建 Closure(+ 可能新建 Upvalue) | 同上 |
| `SETLIST` | 可能触发表 rehash(array 扩容) | 同上 |
| `SETTABLE`/`SETGLOBAL` | 插入新键可能 rehash | 同上 |
| `GETTABLE`/算术/比较/CONCAT 的**元方法慢路径** | 元方法可能分配/调用 | 走 §7 调用边界 safepoint |
| `CALL`/`TAILCALL`/`RETURN` | 帧切换 = 层边界 | 调用边界 safepoint(§7) |
| `VARARG` | 拷贝不分配,但 `B=0` 到 top 可能 ensureStack 扩容 | 扩容点检查 |

非分配指令(MOVE/LOADK/LOADBOOL/LOADNIL/GETUPVAL/SETUPVAL/UNM/NOT/LEN-on-existing/JMP/比较/TEST/FORLOOP/FORPREP 的快路径)**不设 safepoint**。

### 5.3 safepoint 的实现机制

```go
// 在分配 helper(arena.alloc)内部维护:分配越过 GC 触发阈值时置 vm.gcPending。
// opcode 末尾(仅分配 opcode)检查:
if vm.gcPending {
    vm.pushShadowRoots(f)   // 把当前 frame 的活跃寄存器 [base, base+proto.MaxStack) 登记为根
    vm.gc.Collect()          // STW mark-sweep(P1);未来可增量
    vm.popShadowRoots()
    reloadFrame(f)           // GC 可能搬迁 arena → 重取 stk/k
}
```

- **shadow stack 根**:GC 的根集合是 Thread 的值栈活跃区 + 所有 CallInfo + 全局表 + Proto 注册表常量(见 [01](./01-value-object-model.md) §1「Proto 常量 GCRef 是 GC 根」)+ 当前在途的 Go 局部里持有的 GCRef(如 CONCAT 半成品)。详尽根枚举与 mark/sweep 算法见 [06-memory-gc](./06-memory-gc.md);**本文只声明「解释器在分配 opcode 末尾与调用边界把 frame 暴露为根并允许 Collect」**。
- **P1 是 STW**:主循环在 safepoint 同步 Collect,无并发,无写屏障复杂度(写屏障接口预留给增量 GC,P1 不启用)。这把 GC 正确性的难度压到最低,符合 roadmap §5 原则 3「每阶段独立交付」。

> 与编译层的对照:roadmap §2 要求「JIT 代码在循环回边插抢占检查点」。解释器**没有异步抢占问题**(它就是 Go 代码,Go 调度器在函数调用处可抢占),所以 P1 不需要回边抢占点;但**它的 safepoint 布点哲学(分配点 + 层边界)与未来 JIT 的回边检查点同源**,都是「受控位置才允许 runtime 介入」。FORLOOP 回边在 P1 不是 safepoint(循环体若不分配就一路跑),但它是 P2 热度计数采样点([02](./02-bytecode-isa.md) §4「热点回边」)——两件事不要混。

---

## 6. Inline Cache 执行机制(定稿)

[02](./02-bytecode-isa.md) §7 给了 `ICSlot` 结构与「失效靠 shape 版本号」的方向,但**把具体版本机制留给本文定稿**([02](./02-bytecode-isa.md) §10、[01](./01-value-object-model.md) 均指向这里)。本节定稿,**其它文档以此为准**。

### 6.1 定稿:per-table 单调代次 + 全局表版本号

两套版本号,各管一类失效:

- **per-table 代次(generation)`gen`**:每个 Table 对象**自带一个单调递增的 `uint32` 代次**,存在 Table 对象里。**任何改变表"形状"的操作递增它**:rehash(array/hash 重排,[01](./01-value-object-model.md) §5.2)、增删 metatable、键的数组↔哈希迁移。普通的「已存在键改值」**不**递增(形状没变,槽位还在原处)。
  - 存储位置:复用 [01](./01-value-object-model.md) §5.2 Table 的 `word5`(原 `lastfree` 字段)旁,或在 `word1` 的高位预留——**定稿:Table 增加一个代次字段**。这要求回填 [01](./01-value-object-model.md) §5.2 布局(见 §6.6 对 01 的回填请求 / doc-gap)。
- **全局表版本号 `globalsVer`**:globals 表([02](./02-bytecode-isa.md) §2 记号 `G`)是一张特殊 Table,**复用它自己的 per-table 代次**即可——GETGLOBAL/SETGLOBAL 的 IC 用 globals 表的 `gen` 当 shape。所以「全局表版本号」不是独立机制,是 per-table 代次在 globals 上的特例。**这统一了两类 IC,定稿为单一机制:per-table 单调代次,globals 是其特例**。

> 为什么选 per-table 代次而非「全局单调代次」:全局单调代次(任何表变化都 bump 一个进程级计数器)会让**所有** IC 一起失效(false sharing),循环里改 A 表会废掉读 B 表的 IC。per-table 代次精确到表,只失效真正变了形状的表的相关 IC。代价是每表多 4 字节——可接受。

### 6.2 ICSlot 语义(对 [02](./02-bytecode-isa.md) §7 字段的执行侧定稿)

```go
type ICSlot struct {
    shape uint32 // 缓存时记录的「目标 table 的 gen 代次」(globals 即 globals 表的 gen)
    index uint32 // 命中时直达槽位:array hit=数组下标;node hit=Node 数组下标
    kind  uint8  // 0 未初始化 / 1 array hit / 2 node hit / 3 mono-metamethod / 4 megamorphic
}
```

- `kind` 还需隐含**缓存的目标表身份**——但 IC slot 不存 GCRef(避免 IC 成为 GC 根 + 表被回收后悬垂)。**定稿:IC 命中校验「同表 + 同代次」**。「同表」怎么判而不存 GCRef?→ 在校验时拿**本次访问的实际表 GCRef** 与 **slot 缓存的代次** 比对:slot 额外存一个 `tableRef uint32`(arena 偏移低 32 位,arena <4GB 足够)用于「同表」判定,但**它不作为 GC 根**(只读比对,表若被回收则代次比对也会失败,自然 miss)。这要求扩展 ICSlot 加 `tableRef` 字段(见 §6.6 对 [02](./02-bytecode-isa.md) §7 的回填)。

### 6.3 命中流程(以 GETTABLE 为例)

```go
func (vm *VM) doGetTable(f *frame, i Instruction) *LuaError {
    tbl := f.stk[f.base+B(i)]      // R(B)
    key := f.rk(i, C(i))          // RK(C)
    slot := &f.ic[f.pc-1]
    if value.Tag(tbl) == value.TagTable {
        t := object.TableAt(vm.arena, value.GCRefOf(tbl))
        // —— IC 命中校验:同表 + 同代次 ——
        if slot.kind != 0 &&
           slot.tableRef == uint32(value.GCRefOf(tbl)) &&
           slot.shape == t.Gen() {
            switch slot.kind {
            case icArrayHit:
                f.stk[f.base+A(i)] = t.ArrayAt(slot.index)   // 直达数组槽,跳过哈希
                return nil
            case icNodeHit:
                f.stk[f.base+A(i)] = t.NodeValAt(slot.index) // 直达节点槽
                return nil
            // icMonoMeta / icMega 见下
            }
        }
        // —— 未命中:慢查找 + 更新 slot ——
        v, where, idx := t.RawGetWithLoc(key)   // 完整查找,返回命中位置(array/node/none)
        if where != locNone {
            slot.kind = icKindOf(where)          // array/node hit
            slot.index = idx
            slot.shape = t.Gen()
            slot.tableRef = uint32(value.GCRefOf(tbl))
            f.stk[f.base+A(i)] = v
            return nil
        }
        // 表里没有该键 → 走 __index(可能 raw nil 或元方法),见 [07]
        return vm.indexMeta(f, A(i), tbl, key, slot)
    }
    // R(B) 非 table → __index 元方法(string 的 __index = string 库等),见 [07]
    return vm.indexMeta(f, A(i), tbl, key, slot)
}
```

关键性质:

- **命中 = 一次身份比对 + 一次代次比对 + 一次数组/节点索引**,完全跳过哈希计算与冲突链遍历。这是相对 gopher-lua「每次都 hash」的核心加速(roadmap §4「全局/表访问 inline cache」)。
- **未命中 = 完整查找 + 把命中位置写回 slot**,下次同表同形状即命中。
- **polymorphic 处理(P1 简化)**:P1 用 **mono IC**——只缓存最近一次命中的表。若下次换了一张表(`tableRef` 不同),直接当 miss 重填(不升级成 N 路缓存)。频繁换表的访问点会退化成「每次慢查找 + 重填」≈ 无 IC,但**正确性不受影响**,且这种点本就不该有 IC 收益。`kind=4 megamorphic` 预留给 P2 标记「此点多态、别再投机」(供编译层用,P1 可不实现降级,见 §6.4)。

### 6.4 各 IC 指令的差异

- **GETGLOBAL/SETGLOBAL**(ABx,key 是 `K(Bx)` 字符串常量):目标表恒为 globals,`tableRef` 恒等,只需代次比对。命中直达槽位写/读。SETGLOBAL 插入**新**全局键 → globals rehash → globals 代次自增 → 自动失效旧 IC(正确)。
- **GETTABLE/SETTABLE**:目标表是 `R(B)`/`R(A)`,运行期变,需「同表 + 同代次」双重校验。
- **SELF**(`R(A+1):=R(B); R(A):=R(B)[RK(C)]`,[02](./02-bytecode-isa.md) §4-11):IC 缓存的是 `R(B)[method]` 的查找(方法通常在 metatable 的 `__index` 表里,常驻不变 → IC 命中率极高,这正是 `obj:m()` 优化的意义)。先做 `R(A+1):=R(B)`(self 传递),再走与 GETTABLE 同构的 IC 取方法。
- **算术 IC**(ADD 等,[02](./02-bytecode-isa.md) §4-12 仅 ADD 标 IC,但同族适用):**不参与取值快路径**(§4.1 已说明,P1 快路径靠现场 `IsNumber`)。算术 IC slot 记录操作数实际类型分布——承 [../p2-bridge/00-overview](../p2-bridge/00-overview.md) §3.6 回填,定为 **`numHits`/`metaHits` 双计数**(快路径 `numHits++`、元方法慢路径 `metaHits++`,挪用算术 IC 闲置的 shape/index/tableRef 字段,[02](./02-bytecode-isa.md) §7 已登记)——纯为 P2 类型 feedback 与 P4 f64 投机供料([../p4-method-jit](../p4-method-jit/00-overview.md))。P1 写它、不读它分支。

### 6.5 失效机制(写侧职责)

谁改表形状,谁递增代次:

| 操作 | 递增哪个表的 gen | 触发文档 |
|---|---|---|
| Table rehash(array/hash 重排) | 该表 | [01](./01-value-object-model.md) §5.2 / [06](./06-memory-gc.md) |
| `setmetatable` / 清 metatable | 该表 | [07](./07-metatables-metamethods.md) |
| SETTABLE 插入触发 rehash | 该表 | 本文 §7 之外的写路径 |
| SETGLOBAL 插入新键触发 rehash | globals 表 | 本文 |
| **`insertNewKey` Brent-style 重定位**(新键落主位、把占用者迁到 free 槽)| **该表**(承 `internal/crescent/rawtable.go`)| 本文 §6.5.1 gen 契约强度 |
| 已存在键改值(无 rehash) | **不递增** | 本文 |

**纪律**:rehash 是唯一会让「array/node 槽位下标失效」的操作,所以代次必须且只须在「槽位下标可能变」时递增。「改值不动槽」不递增是性能关键(循环里反复 `t[k]=v` 改同一键不该废 IC)。

#### 6.5.1 gen 契约强度:由最严 consumer 定义

**契约强度定义**:表的 gen invariant 的**强度**由所有 consumer 中**最严格**的那个定义——任何改变 key → slot 映射的写路径都必须 BumpGen,即便解释器自己的读路径「碰巧不敏感」。

**consumer 谱系**(按对 gen 的依赖强度排列):

| Consumer | 命中路径 | 对 key→slot 稳定性的依赖 | 强度 |
|---|---|---|---|
| P1 解释器 `icGetTable` / `icGetNodeVal` | ICSlot cache 命中路径 | **每次访问都复验 NodeKey**(比对 IC 记录的 key 与该 slot 当前的 key)——若 key 已被 Brent 挪走,复验失败降级慢查找 | **弱**(自愈) |
| P3 wasm `emitGetGlobal` NodeHit inline | 全局表 IC 直达 | **只查 gen 是否 match**,不复验 key——**node 索引编译期烧入 wasm 字节码**;若 gen 未 bump 而 key 已挪走,读到相邻新占用者 = **静默错果** | **严** |
| P4 native `GETGLOBAL` NodeHit inline | 全局表 IC 直达(P4 exit-reason 协议) | **同上**——node 索引编译期烧入机器码立即数,只查 gen;缺 bump = 静默错果 | **严** |

**发现现场**(承 memory `project_pj10_native_longtask.md`「PJ10 must-beat-P3 op 集扩面」条目 fuzz seed `4b3d10ff17c418d4`):
`insertNewKey` 的 Brent 重定位分支 (`internal/crescent/rawtable.go:180-206`) 会**改 slot** 但历史上**没有 BumpGen**——P1 解释器的每次访问复验 NodeKey 掩盖了这条漏洞多年,直到 P3 wasm 与 P4 native 的 gen-only inline 快路径接入,才让静默错果浮现。修复:在 Brent 重定位后无条件 `object.BumpGen(st.arena, t)`(见 rawtable.go:204 附近注释)。

**推论**:任何未来引入的表变换,若可能改变 key → slot 映射(rehash、Brent 挪位、shrink、compact 等),**必须 BumpGen**——即便当时的 consumer 都是「自愈」型,也不能省略,因为**下一代 consumer 可能是「严」型**;省略等于给未来埋 UAF。这是**「invariant 的强度由最严 consumer 定义」**原则在 gen 上的兑现,与 llmdoc `memory/doc-gaps.md` 登记的相关缺口对应(具体登记项由 recorder 维护)。

### 6.6 对上游文档的回填请求(本文定稿带来的字段增补)——**已兑现**

本节定稿要求的两处上游布局增字段**均已回填落地**:

1. **[01](./01-value-object-model.md) §5.2 Table 布局**:`gen uint32` 代次字段已落入 word5 高 32 位(与 lastfree 同字),`object.Table` 暴露 `Gen()` / `bumpGen()`。
2. **[02](./02-bytecode-isa.md) §7 ICSlot**:`tableRef uint32` 已增补(目标表 arena 偏移低 32 位,仅作身份比对,非 GC 根);算术 IC 的字段挪用(双计数,P2 回填)也已一并登记。

这两处不改语义、只增字段,与 [01](./01-value-object-model.md)/[02](./02-bytecode-isa.md) 的 ABI 承诺兼容(只增不改)。

---

## 7. 调用:CALL / TAILCALL / RETURN —— Lua-call-Lua 不吃 Go 栈

这是本文最核心的不变式(扣合 roadmap §2 栈移动税「不持有指向 Go 栈的指针」与 [08-coroutines](./08-coroutines.md) 的协程切换)。

### 7.1 CALL:Lua 调 Lua = reentry(不递归 Go 栈)

`CALL A B C`:调用 `R(A)`,参数 `R(A+1..A+B-1)`,返回回填 `R(A..A+C-2)`([02](./02-bytecode-isa.md) §3/§4-28)。`B=0` 参数到 `top`,`C=0` 返回到 `top`。

```go
func (vm *VM) doCall(f *frame, i Instruction) callResult {
    a := A(i)
    callee := f.stk[f.base+a]            // R(A):被调对象
    nargs := computeNargs(f, i)          // B>0: B-1;B=0: top-(base+a+1)(到 top)
    nresults := C(i) - 1                 // C>0: C-1;C=0: 0xFFFF(到 top,可变)

    switch {
    case isLuaClosure(callee):
        cl := value.GCRefOf(callee)
        proto := vm.protos[protoIDOf(cl)]
        newBase := f.base + a + 1        // 新帧 base:R(A) 之后第一个槽
        // 形参搬移:实参已在 [newBase, newBase+nargs);多退少补 nil 到 NumParams
        adjustVarargs(f, proto, newBase, nargs)   // vararg 处理见 §8.5
        vm.enterLuaFrame(/*callerTop*/, cl, newBase, nresults) // §1.4:压 CallInfo + 重载 frame
        return callEnteredLua            // 主循环重载 code,继续在同一 Go 栈帧循环(reentry!)

    case isHostClosure(callee):
        return vm.callHost(f, a, nargs, nresults)  // §7.5:进 Go 调用栈

    default:
        // R(A) 非可调用 → 查 __call 元方法([07]);有则把 callee 插入参数前重试;无则错
        return vm.callMeta(f, i, callee)
    }
}
```

**reentry 的本质**:`callEnteredLua` 返回后,主循环(§2.3)只是重载 `code = f.proto.Code` 并 `continue`——**没有 Go 层面的递归调用**。被调函数的执行就是同一个 `for` 循环的后续迭代,只不过 `f.base/f.pc/f.proto` 都换成了新帧的。这意味着:

- **Lua 调用深度不消耗 Go 栈**:1000 层 Lua 递归 = 1000 条 CallInfo(arena),Go 栈深度恒为 1(就在 `execute` 那一帧)。这正面兑现 roadmap §2「不持有指向 Go 栈的指针」——调用链状态全在 arena,Go 栈不增长,morestack 拷栈与解释器调用链解耦。
- **协程切换可行**([08](./08-coroutines.md)):挂起 = 保存当前 frame 回 CallInfo + 切到另一 Thread 的 CallInfo 链;因为状态全在 arena,切协程不需要拷 Go 栈。

**P4 阶段新增 `callDeoptResume` doCall 出口**(2026-06-28,承 [../p4-method-jit/implementation-progress §2 RJ-3](../p4-method-jit/implementation-progress.md) 跨文档回填请求):本节 callResult 枚举(P1 阶段)未列 `callDeoptResume`,因 P1/P2/P3 不需要——P3 永不返回 status=2(承 [../p2-bridge/05-p3-p4-interface §6.1](../p2-bridge/05-p3-p4-interface.md))。**P4 阶段引入**:GibbousCode.Run 返回 status=2 (DEOPT) 时 doCall 出口走 `callDeoptResume`(reloadFrame + 续跑同帧),具体协议详见 [../p4-method-jit/04-osr-deopt §5 OSR exit 流程](../p4-method-jit/04-osr-deopt.md) + [../p4-method-jit/05-system-pipeline](../p4-method-jit/05-system-pipeline.md)。**P1 视角影响**:enterLuaFrame / 主循环重载 frame 逻辑不变,callResult 枚举在 P4 build 下增 callDeoptResume 一个变体。

### 7.2 RETURN:退帧或终止

`RETURN A B`:返回 `R(A..A+B-2)`,`B=0` 返回到 `top`([02](./02-bytecode-isa.md) §4-30)。

```go
func (vm *VM) doReturn(f *frame, i Instruction) returnResult {
    a, nret := A(i), retCount(f, i)      // B>0: B-1;B=0: top-(base+a)
    ci := vm.ci(f.ci)
    // 1. 关闭本帧所有开放 upvalue(≥ base),见 §8.3
    vm.closeUpvals(f, f.base)
    // 2. 把返回值从 [base+a, base+a+nret) 搬到调用者期望的位置 [funcIdx, ...)
    //    funcIdx = base-1(被调 closure 原先所在槽,[01] §5.6 约定)
    dst := f.base - 1
    moveResults(f, dst, f.base+a, nret, ci.nresults)  // 按 caller 期望 nresults 多退少补
    // 3. 弹 CallInfo,恢复调用者 frame
    vm.popCallInfo()
    if vm.ciTop == vm.entryCi {          // 退到了 execute 的入口帧之下
        return returnTerminate           // 整个 execute 结束(§7.3)
    }
    vm.loadTopFrame_into(f)              // 重载为调用者帧:base/pc(=savedPC)/proto/cl
    // 若 caller 期望可变返回(nresults=0xFFFF),更新 caller 的 top = dst+nret
    return returnToCaller                 // 主循环重载 code 继续
}
```

要点:

- **多返回值多退少补**:`moveResults` 按调用者 CallInfo 记录的 `nresults`(C-1)裁剪/补 nil;`nresults=0xFFFF`(C=0)则把**全部** nret 个值落下并更新调用者 `top`,供后续 `CALL B=0`/`RETURN B=0`/`SETLIST B=0` 消费(多值传播链,[02](./02-bytecode-isa.md) §9-4)。
- **upvalue 必须在搬返回值前关闭**(§8.3):因为返回后这些栈槽会被复用,开放 upvalue 若还指着它们就会读到垃圾。
- **退到入口帧即终止**:`execute` 被调用时记下 `entryCi`(§7.3);RETURN 退到它之下就 return 出 Go 函数。

### 7.3 reentry 边界:execute 的入口帧与 host→Lua 重入

`vm.execute(th)` 可能被多个源头调用:① 顶层 `Program.Call`;② host function 内部回调 Lua(如 `table.sort` 的比较器、`pcall` 的被保护函数);③ 协程 resume。每次进入 `execute` 记一个 **`entryCi`**(进入时的 ciTop),`RETURN` 退到 `entryCi` 即返回 Go——**这一帧的 CallInfo 标 `callStatus_fresh`(§1.2 word2 bit49)**,表示「它是一个 Go→Lua 的 reentry 边界,RETURN 到此要交还 Go 控制权,而非继续 reentry 循环」。

```
host function 调 Lua(如 pcall(f)):
  host 代码 → vm.callLuaFromHost(fRef, args)
            → 压一个 fresh CallInfo(标 callStatus_fresh)
            → vm.execute(th)   // 新的 Go 栈帧!这里确实递归了 Go 栈
            → execute 内 Lua 调 Lua 仍是 reentry(不再加 Go 栈)
            → f 返回时 RETURN 退到 fresh 帧 → execute 返回 → 回到 host 代码
```

**纪律**:Lua→Lua 永远 reentry(不加 Go 栈);**只有 host→Lua 才新起一个 `execute`(加一层 Go 栈)**。所以 Go 栈深度 = host→Lua 重入的层数,而非 Lua 调用深度。`pcall`/`coroutine` 这些必然 host→Lua 重入的点,Go 栈会增长,但它们的层数远小于 Lua 递归深度,可控(且 §7.4 有上限)。

### 7.4 调用深度上限(防爆栈)

两个上限:

- **Lua 调用深度**(CallInfo 数 / 值栈深度):由 `ciCap` 与栈上限守。超限抛 `"stack overflow"`(Lua 语义)。这是 arena 内的逻辑上限,不是 Go 栈。
- **host→Lua 重入深度**(真 Go 栈消耗):维护一个 `nCcalls` 计数(Lua `LUAI_MAXCCALLS=200` 等价物),每次 `callLuaFromHost` +1,返回 -1;超限抛 `"C stack overflow"`。这防止「Lua 调 host 调 Lua 调 host …」无限交替真把 Go 栈打爆(Go 栈虽可增长但有 `maxstacksize` 上限 ~1GB,撞上会 fatal 不可恢复——必须在它之前用我们的可恢复错误拦下)。

### 7.5 TAILCALL:复用帧,栈不增长

`TAILCALL A B C`:尾调用 `R(A)(R(A+1..A+B-1))`,**复用当前帧**([02](./02-bytecode-isa.md) §4-29)。语义:当前函数的 `return f(args)` 不应增加调用深度(Lua 5.1 保证 proper tail call)。

```go
func (vm *VM) doTailCall(f *frame, i Instruction) callResult {
    callee := f.stk[f.base+A(i)]
    nargs := computeNargs(f, i)
    switch {
    case isLuaClosure(callee):
        // 1. 关闭当前帧开放 upvalue(≥ base):当前帧即将被覆盖
        vm.closeUpvals(f, f.base)
        // 2. 把 callee + args 下移覆盖当前帧:callee → base-1(funcIdx 位),args → base..
        //    即「原地替换」当前帧的函数与参数,base 不变,CallInfo 不新增。
        moveDownForTailcall(f, A(i), nargs)
        cl := value.GCRefOf(callee)
        proto := vm.protos[protoIDOf(cl)]
        // 3. 改写当前 CallInfo:protoID 换新,base 不变,nresults 继承(尾调用透传调用者期望)
        ci := vm.ci(f.ci)
        ci.protoID = protoIDOf(cl); ci.tailcall = true
        adjustVarargs(f, proto, f.base, nargs)
        ensureStack(th, f.base+int(proto.MaxStack))
        f.proto, f.cl, f.pc = proto, cl, 0
        reloadFrame(f)
        return callEnteredLua          // 仍 reentry,但 CallInfo 数不变 → 栈不增长
    case isHostClosure(callee):
        // host 尾调用:正常调 host(§7.5),其结果作为本帧返回值(等价 return host(...))
        return vm.tailCallHost(f, A(i), nargs)
    default:
        return vm.callMeta(f, i, callee)  // __call
    }
}
```

**关键差异 vs CALL**:CALL 压**新** CallInfo(深度 +1);TAILCALL **原地改写当前** CallInfo(深度不变),所以 `for i=1,1e9 do return f() end` 式尾递归**栈深度恒定**。`ci.tailcall=true` 标志影响错误回溯([09](./09-errors-pcall.md):尾调用帧在 traceback 里显示为 `(...tail calls...)`)。

### 7.6 host function 调用约定(本文给接口,细节见 [10](./10-stdlib.md))

host function(stdlib 与宿主注册)是 Go 函数,**进 Go 调用栈**(与 Lua-call-Lua 的 reentry 相反)。本文定接口形状,完整约定见 [10-stdlib](./10-stdlib.md):

```go
// host function 签名:从 thread 取参数,push 返回值,返回返回值个数。
type HostFn func(vm *VM, th *Thread) (nret int)

// 调用侧(doCall 的 host 分支):
func (vm *VM) callHost(f *frame, a, nargs, nresults int) callResult {
    // 1. 设置 host 帧:参数在 [base+a+1, base+a+1+nargs);
    //    host 用 vm/th 的 API(类似 lua_gettop/lua_pushX)读参写返回值。
    // 2. host 帧也压一条 CallInfo(protoID=哨兵 host 标记),供错误回溯定位。
    // 3. 直接 Go 调用 hostFn(vm, th)——这一步进 Go 栈(同步执行)。
    nret := vm.hostFns[hostFnIDOf(...)](vm, th)
    // 4. host 返回值在栈顶 [top-nret, top);搬到 R(A..) 按 nresults 裁剪。
    moveResults(...)
    // 5. 弹 host CallInfo。host 不分配 frame、不进解释循环。
    return callReturnedHost            // 主循环不重载 code(没切 Lua 帧)
}
```

要点:

- **host 调用是同步 Go 调用**,执行完返回值已就位,主循环**不切 code、不 reentry**(`callReturnedHost`)。
- host **内部若回调 Lua**(`vm.callLuaFromHost`),才触发 §7.3 的 Go 栈重入。
- host 可能**抛 Lua 错误**(`vm.raise`):它走 §9 的错误返回路径,被最近 protected 边界捕获。host 不该 Go `panic`(§9.4)。
- host 帧也有 CallInfo(带 host 哨兵 protoID),让 `error`/traceback 能显示 `[C]: in function 'xxx'`([09](./09-errors-pcall.md))。

---

## 8. upvalue、闭包构造、作用域关闭(定稿关闭流程)

### 8.1 GETUPVAL / SETUPVAL

`Upval(B)` 是当前 closure 的第 B 个 upvalue(Closure 对象 word2+ 的 `upvalRef[B]`,[01](./01-value-object-model.md) §5.3)。

```go
case bytecode.GETUPVAL:  // R(A) := Upval(B)
    uv := closureUpval(f.cl, B(i))          // GCRef → Upvalue 对象
    f.stk[f.base+A(i)] = upvalGet(vm, uv)   // 开放:读 thread.stk[idx];关闭:读 word2

case bytecode.SETUPVAL:  // Upval(B) := R(A)
    uv := closureUpval(f.cl, B(i))
    upvalSet(vm, uv, f.stk[f.base+A(i)])    // 开放:写回栈槽;关闭:写 word2
```

`upvalGet/upvalSet` 按 Upvalue 的开放/关闭态([01](./01-value-object-model.md) §5.4 `flags bit0`)分派:

- **开放**:upvalue 逻辑上指向 `thread.valueStack[stackIdx]`([01](./01-value-object-model.md) §5.4 用 `(threadRef, stackIdx)` 定位),读写直接落那个栈槽。
- **关闭**:读写 Upvalue 对象自己的 `word2`(自持值)。

### 8.2 CLOSURE:构造闭包 + 后随伪指令捕获 upvalue

`CLOSURE A Bx`:`R(A) := closure(Proto[Bx])`,**随后 `nupvals` 条伪指令**描述每个 upvalue 的来源([02](./02-bytecode-isa.md) §4-36)。

```go
case bytecode.CLOSURE:
    proto := vm.protos[f.proto.Protos[Bx(i)]]   // 嵌套 proto
    cl := vm.newLuaClosure(proto.protoID, proto.nupvals)  // 分配 Closure(arena)→ safepoint!
    for j := 0; j < proto.nupvals; j++ {
        pseudo := f.proto.Code[f.pc]; f.pc++     // 读后随伪指令
        switch bytecode.Op(pseudo) {
        case bytecode.MOVE:      // upvalue 捕获「父帧的局部寄存器 R(B)」
            // → 在父帧栈槽 base+B 上 findOrCreate 一个开放 upvalue,挂到本 closure
            uv := vm.findOrCreateUpval(th, f.base+B(pseudo))
            setClosureUpval(cl, j, uv)
        case bytecode.GETUPVAL:  // upvalue 捕获「父帧自己的 upvalue B」
            // → 直接共享父 closure 的第 B 个 upvalue
            setClosureUpval(cl, j, closureUpval(f.cl, B(pseudo)))
        }
    }
    f.stk[f.base+A(i)] = value.MakeGC(value.TagFunction, cl)
    reloadFrame(f)   // 分配过 → 重载 stk
```

- **后随伪指令**:codegen 在 CLOSURE 后紧跟 `nupvals` 条 `MOVE`(捕获父局部)或 `GETUPVAL`(捕获父 upvalue)。它们**不是可执行指令**,是 CLOSURE 的「操作数延续」——解释器读它们但不当指令 dispatch(读完 pc 已跳过)。这是 Lua 5.1 的经典编码,差分基准会验证伪指令序列。
- **findOrCreateUpval**:见 §8.3 的开放 upvalue 链——同一栈槽只能有一个开放 upvalue(多个闭包捕获同一变量要共享),所以是「查链找已有,没有才新建并按序插入」。
- CLOSURE **分配 Closure 对象**(可能还分配 Upvalue)→ safepoint,**重载 stk**。

### 8.3 定稿:open upvalue 链与关闭流程

[01](./01-value-object-model.md) §5.4 / §5.6 给了「Thread 持 `openUpvalRef` 链头」「CLOSE/作用域退出关闭」的方向,**把具体流程留给本文**([01](./01-value-object-model.md) §5.4 末句指向这里)。定稿:

**数据结构**:每个 Thread 维护一条**按 `stackIdx` 降序排列**的开放 upvalue 单链表(链头 = `Thread.openUpvalRef`,[01](./01-value-object-model.md) §5.6 word6)。降序的理由:关闭操作总是「关闭 ≥ 某 level 的所有 upvalue」(作用域退出从栈顶往下收),降序链让我们从链头开始顺序摘除直到 `stackIdx < level`,O(被关闭数)。

链节点复用 Upvalue 对象自身(开放态),用一个 `nextOpen` 字段串联(在 [01](./01-value-object-model.md) §5.4 Upvalue 的开放态 word1 里编入 next 偏移,或加一字——见 §8.6 回填请求)。

**findOrCreateUpval(th, stackIdx)**(CLOSURE 捕获父局部时用):

```
沿 openUpvalRef 链(降序)找 stackIdx:
  - 链中已有指向该 stackIdx 的开放 upvalue → 返回它(共享!多闭包捕获同一变量)
  - 链中没有(到达 stackIdx 更小的节点或链尾)→ 新建开放 Upvalue(threadRef, stackIdx),
    按降序插入链中正确位置,返回它
```

**closeUpvals(th, level)**(CLOSE 指令 / RETURN / 块退出时用):

```
从 openUpvalRef 链头开始(stackIdx 最大):
  while 链头 != 0 且 链头.stackIdx >= level:
    uv := 链头
    链头 = uv.nextOpen           // 摘下
    val := thread.valueStack[uv.stackIdx]   // 读出当前栈值
    uv.word2 = val               // 拷入 Upvalue 自持
    uv.flags.bit0 = 1            // 置「关闭」态([01] §5.4)
    uv.nextOpen = 0              // 脱链
  Thread.openUpvalRef = 链头
```

关闭后该 upvalue 与栈槽脱钩(自持值),栈槽可安全复用。**这是 open→closed 转换的定稿**([01](./01-value-object-model.md) §5.4 所指)。

### 8.4 CLOSE 指令与隐式关闭点

`CLOSE A`:关闭所有 `≥ R(A)` 的开放 upvalue([02](./02-bytecode-isa.md) §4-35)。

```go
case bytecode.CLOSE:
    vm.closeUpvals(th, f.base+A(i))   // level = base+A
```

**何时关闭**(codegen 发 CLOSE 或解释器隐式做):

- **显式 CLOSE**:codegen 在「捕获了局部变量的块」退出时发 CLOSE(如 `do local x; foo=function() return x end end` 块尾)。
- **RETURN/TAILCALL 隐式关闭**:§7.2/§7.5 在退帧前 `closeUpvals(f, f.base)` 关闭本帧全部(level=base)——因为整帧栈槽都要释放。
- **循环回边**:`for`/`while` 体内若有闭包捕获循环变量,每次迭代的变量是新实例(Lua 5.1 语义),codegen 在循环体尾发 CLOSE 关闭该次迭代捕获的 upvalue。这保证 `for i=1,3 do t[i]=function() return i end end` 里三个闭包捕获三个不同的 `i`(经差分验证)。

> **正确性纪律**:任何「栈槽即将被复用或失效」的点(退帧、块退出、循环迭代收尾),凡其上可能有开放 upvalue,就必须先 closeUpvals。漏关 = 闭包读到被覆盖的栈值(经典 use-after-scope bug)。codegen 与解释器对此有明确分工:codegen 发 CLOSE 标块边界,解释器在退帧时兜底全关。

### 8.5 vararg 与 VARARG 指令

[02](./02-bytecode-isa.md) §6:vararg 函数的多余实参存在 `base` 之下。`adjustVarargs`(§7.1 调用):

```
进入 vararg 函数时:
  实参 nargs 个在 [newBase, newBase+nargs)。
  固定形参 NumParams 个保留在 [newBase, newBase+NumParams)。
  多余的 (nargs - NumParams) 个 vararg 搬到 newBase 之下(funcIdx 与 base 之间的负区),
    CallInfo 记录 vararg 起始与个数。
  base 实际指向「固定形参之后」——即 base 上移 NumParams,vararg 落在 [oldBase, base)。
```

`VARARG A B`(`R(A..A+B-2) := ...`,B=0 到 top,[02](./02-bytecode-isa.md) §4-37):从 CallInfo 记录的 vararg 区拷回 `R(A..)`。`B=0` 时拷全部 vararg 并更新 top(供 `f(...)` 透传)。VARARG **可能扩容栈**(B=0 且 vararg 很多)→ ensureStack → 可能重载 stk。

### 8.6 对上游文档的回填请求(upvalue 链字段)——**已兑现**

§8.3 的降序链需要 Upvalue 对象在**开放态**有一个 `nextOpen`(48-bit GCRef 或栈索引)字段。定稿:把 `nextOpen` 编入开放态的 word2(开放时 word2 不存值、正好空闲;关闭时 word2 改存自持值,此时 nextOpen 已无意义因已脱链)。**[01](./01-value-object-model.md) §5.4 已按此更新布局**(开放态 word1 = stackIdx+threadRef,word2 = nextOpen;关闭态 word2 = 自持值)。

---

## 9. 错误传播(定稿:显式错误返回 + protected call 边界)

### 9.1 定稿:不用 panic/recover 跨主循环,用显式错误返回

Lua C 实现用 `setjmp/longjmp` 做错误传播(`error` 跳回最近 `pcall`)。Go 的等价物有两条路:

| 方案 | 机制 | 评估 |
|---|---|---|
| **panic/recover** | `error` 时 `panic(luaErr)`,`pcall` 处 `recover()` | ❌ 跨 reentrant loop 复杂且慢 |
| **显式错误返回**(**选定**) | helper 返回 `*LuaError`,主循环逐层冒泡到 protected 边界 | ✅ 与 reentry 模型契合 |

**为什么不用 panic/recover**(定稿理由,[09](./09-errors-pcall.md) 以此为准):

1. **与 reentry 模型冲突**:§7 的 Lua-call-Lua 是「同一 Go 栈帧里 reentry」,不是 Go 递归。panic 会**一路 unwind Go 栈**——但 Lua 调用链根本不在 Go 栈上(在 arena 的 CallInfo)!panic 只会 unwind 到最近的 `execute` 入口(§7.3 的 fresh 帧),**跳过中间所有 Lua 帧的 CallInfo 清理**(开放 upvalue 关闭、栈 top 恢复)。要在 recover 里手工重建 CallInfo 状态,比显式返回更复杂。
2. **性能**:Go 的 panic/recover 有不可忽略的固定成本(分配 + 栈遍历),而 Lua 里 `pcall` 是常用控制流(不只异常),热路径上不能用 panic。
3. **可控性**:显式 `*LuaError` 返回让「错误在哪一层产生、经过哪些帧」完全显式,便于构造 traceback([09](./09-errors-pcall.md))。

**代价**:每个可能出错的 helper 都返回 `*LuaError`,主循环每个 case 检查并 `return vm.throw(...)`。这是「显式错误处理啰嗦」的常规 Go 代价,但换来与 reentry/协程模型的一致性。

### 9.2 错误对象与冒泡路径

```go
type LuaError struct {
    value     value.Value   // error 的值(通常 string,也可是任意 Lua 值,Lua 语义)
    traceback string        // 可选:产生时捕获的调用栈([09])
    level     int           // error(msg, level) 的 level
}

// 主循环每个可能出错的 case:
//   if e := vm.doXxx(...); e != nil { return vm.throw(f, e) }
// throw 不立即 panic,而是:
func (vm *VM) throw(f *frame, e *LuaError) *LuaError {
    // 把 e 一路 return 出 execute;execute 的调用者(顶层 Call 或 callLuaFromHost)
    // 检查返回的 *LuaError:
    //   - 若当前 execute 是某个 pcall 的 fresh 边界(§7.3 + §9.3)→ pcall 捕获,转成 (false, errval)
    //   - 否则继续 return 给上层(可能上层是另一个 pcall,或顶层 → 传给宿主)
    return e
}
```

**冒泡机制**:`execute` 返回 `*LuaError`(§2.3 签名 `(err *LuaError)`)。错误产生时:

1. 出错 helper 返回非 nil `*LuaError`;
2. 主循环 case `return e`——**直接 return 出 `execute` 这个 Go 函数**;
3. 但 `execute` 可能管着多层 Lua 帧(CallInfo)。**关键**:return 出 execute **之前**,要不要清理中间 Lua 帧?→ 见 §9.3:**protected 边界负责清理到自己为止的 CallInfo**,未保护的错误一路 return 到顶层。

### 9.3 protected call 边界(pcall / 元方法保护点)

`pcall(f, ...)`(host function)是 protected 边界。机制:

```
pcall host 实现:
  1. 记下当前 ciTop（保护点 savedCiTop）与栈 top。
  2. vm.callLuaFromHost(f, args):
       - 压 fresh CallInfo(§7.3),标 errfuncBase（word3,§1.2）。
       - vm.execute(th) 跑被保护函数(新 Go 栈帧)。
  3. execute 返回:
       - 返回 nil(无错)→ pcall 返回 (true, results...)。
       - 返回 *LuaError → pcall 捕获:
           a. 把 ciTop 回退到 savedCiTop（丢弃出错帧及其上所有 Lua 帧的 CallInfo）。
           b. 关闭这些被丢弃帧的开放 upvalue(closeUpvals 到保护点 base)。
           c. 恢复栈 top 到保护点。
           d. 返回 (false, errLua.value) 给调用者。
```

要点:

- **错误冒泡 = execute 一路 return *LuaError**,直到撞上一个「在 host 里调了 callLuaFromHost 且检查返回值」的 protected 边界(pcall),由它把 `*LuaError` 转成 `(false, errval)` 返回值并**清理 CallInfo + 关 upvalue + 恢复 top**。
- **没有 pcall 保护时**:错误一路 return 到顶层 `Program.Call`,后者把 `*LuaError` 转成 Go 的 `error` 返回给宿主([11-embedding-arena-abi](./11-embedding-arena-abi.md))。中间所有 Lua 帧的 CallInfo 随 execute return 被一并放弃(因为整个 execute 失败,Thread 状态标 dead 或重置)。
- **CallInfo 清理责任在 protected 边界**,不在每个出错帧——出错帧只管 `return e` 冒泡,**省掉了每帧的 defer/cleanup**(这正是显式返回 vs panic 的关键简化:panic 要在每帧 defer 关 upvalue,显式返回让边界一次性清理)。
- **元方法/错误处理器**(`error` 的 message handler、`xpcall` 的 handler):在边界捕获后、返回前调用 handler(可能再 reentry execute);细节 [09](./09-errors-pcall.md)。

### 9.4 错误来源与 host 协作

- **解释器内在错误**:类型错误(对 nil 算术)、`table index is nil/NaN`、stack overflow、除零外的运行期错——helper 构造 `*LuaError` 返回。
- **`error(v)` 内建**:host function `error` 调 `vm.raise(v, level)`,它**不 Go panic**,而是构造 `*LuaError` 并通过「host 返回特殊信号 + callHost 检查」让 execute 开始冒泡(host 不能直接 return 出 execute,需经 callHost 把错误转成 execute 的 return 路径)。
- **host function 出错**:host 调 `vm.raise` 同上;host **绝不该 Go panic**(Go panic 会绕过我们的 CallInfo 清理)。若 host 代码有 bug 真 panic 了,顶层 `Program.Call` 用一个**最外层 recover 兜底**转成 Lua 错误并标记 Thread 损坏——但这是**安全网,不是正常路径**([09](./09-errors-pcall.md) 定此兜底)。
- **`assert`/`tonumber` 失败**等:走各自 host 逻辑,失败时 `vm.raise`。

> 这与 §7.3 衔接:错误冒泡的「停靠站」就是 §7.3 标 `callStatus_fresh` 的 reentry 边界——每个 host→Lua 重入点都是潜在的错误捕获点(若该 host 是 pcall),否则错误穿过它继续向 Go 上层 return。

### 9.5 错误传播与 ≥2x 验收的关系

错误路径**不在热路径**(正常脚本极少出错),所以「显式返回啰嗦」不损 §3 的性能——快路径里 `if e != nil` 的 `e` 几乎总是 nil,分支预测器轻松命中,接近零成本。相反若用 panic,即便不出错也要为可能的 panic 准备 defer/recover 框架,反而拖累热路径。**显式返回是「错误路径让步、热路径优先」的正确选择**,与 roadmap §1「减少每指令开销」一致。

### 9.6 P4 OSR exit 路径与错误冒泡互斥(2026-06-28 新增)

承 [../p4-method-jit/implementation-progress §2 RJ-4](../p4-method-jit/implementation-progress.md) 跨文档回填请求 + [../p4-method-jit/04-osr-deopt §7.4](../p4-method-jit/04-osr-deopt.md):**OSR exit 路径不应设置 `state.pendingErr`**——exit 是「投机失误」非「语义错误」,与本节 9.2-9.4 描述的错误冒泡纪律**不互斥**:

- **错误冒泡**(本节 9.2-9.4):语义错误(attempt to index nil 等),`pendingErr` 置位 + execute return *LuaError → 一路 return 到 pcall / Go 上层
- **OSR exit**(P4 新增):投机失败(IC NodeHit guard 不成立 / IsNumber 失败),GibbousCode.Run 返回 status=2 → doCall 走 `callDeoptResume`(§7.1 + RJ-3),**不动 pendingErr**
- **同发情况**:若投机段中真发生语义错误(如 SELF method 调用 method=nil),**错误优先**:pendingErr 置位 + GibbousCode.Run 返回 status=1(ERR),不返 status=2

**P4 验证形态**(承本会话实证):
- `TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_NilRecv`(语义错误冒泡正确)
- `TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_BadMethod`(method=number 时 attempt to call 冒泡)
- `TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt`(投机失误纯 deopt,不动 pendingErr,byte-equal P1)

---

## 10. FORPREP / FORLOOP / TFORLOOP 执行(数值 for 与泛型 for)

### 10.1 数值 for:FORPREP + FORLOOP

[02](./02-bytecode-isa.md) §6:数值 for 占 4 连续寄存器 `R(A..A+3)`:`R(A)`=内部索引(init)、`R(A+1)`=limit、`R(A+2)`=step、`R(A+3)`=外部循环变量 `v`。

**FORPREP A sBx**(准备,[02](./02-bytecode-isa.md) §4-32):

```go
case bytecode.FORPREP:
    a := A(i)
    // 三槽必须是 number(或可转 number 的字符串,Lua 5.1 对 for 也做 coercion)
    init, ok1 := forNum(f.stk[f.base+a])      // tonumber,失败报错
    limit, ok2 := forNum(f.stk[f.base+a+1])
    step, ok3 := forNum(f.stk[f.base+a+2])
    if !ok1 { return vm.throw(f, errForNotNumber("initial")) }
    if !ok2 { return vm.throw(f, errForNotNumber("limit")) }
    if !ok3 { return vm.throw(f, errForNotNumber("step")) }
    f.stk[f.base+a]   = value.NumberValue(init - step)  // 预减一个 step([02] §6)
    f.stk[f.base+a+1] = value.NumberValue(limit)         // 回填规范化后的 limit/step
    f.stk[f.base+a+2] = value.NumberValue(step)
    f.pc += SBx(i)                                        // 跳到 FORLOOP
```

- **三槽校验**:init/limit/step 都必须能转 number,否则报 `"'for' initial/limit/step value must be a number"`([02](./02-bytecode-isa.md) §6)。校验通过后**回填规范化的数值**(把字符串 "1" 转成数字 1 存回),这样 FORLOOP 不必每次再转。
- **预减一个 step**:FORPREP 先 `init -= step`,这样第一次 FORLOOP 加回 step 后正好是 init——把「进入循环体前的判界」与「回边判界」统一成 FORLOOP 一处逻辑。

**FORLOOP A sBx**(回边,[02](./02-bytecode-isa.md) §4-31,**热点回边**):

```go
case bytecode.FORLOOP:
    a := A(i)
    idx  := value.AsNumber(f.stk[f.base+a])     // 当前内部索引(已是 number,FORPREP 保证)
    step := value.AsNumber(f.stk[f.base+a+2])
    limit:= value.AsNumber(f.stk[f.base+a+1])
    idx += step                                  // 加 step
    // 判界:step>0 用 idx<=limit;step<0 用 idx>=limit(方向敏感)
    cont := false
    if step >= 0 { cont = idx <= limit } else { cont = idx >= limit }
    if cont {
        f.stk[f.base+a]   = value.NumberValue(idx)   // 更新内部索引
        f.stk[f.base+a+3] = value.NumberValue(idx)   // 刷新外部循环变量 v = R(A+3)
        f.pc += SBx(i)                                // 回跳循环体
    }
    // 否则不跳,自然落到循环后续指令(退出循环)
```

要点:

- **方向敏感判界**:step≥0 用 `idx <= limit`,step<0 用 `idx >= limit`。这与 Lua 5.1 一致(`for i=10,1,-1`)。`step==0` 是 Lua 5.1 未定义/死循环,我们按 `step>=0` 分支处理(`idx<=limit` 恒可能真 → 死循环,与官方一致,不特判报错——除非选择更友好,见 doc-gap)。
- **回边零额外开销**:三个 `AsNumber` 是 `math.Float64frombits`(无分支),加法、一次比较、两次写栈、一次 pc 加——**没有分配、没有类型断言、没有元方法检查**。这是 roadmap §1「Horner 循环」类负载的最热路径,直接决定循环档 ≥2x([02](./02-bytecode-isa.md) §8 示例就是这形状)。对比 gopher-lua:它的 for 循环变量是 interface,每次迭代要拆箱/装箱。
- **FORLOOP 不是 safepoint**(§5.2):它不分配。循环体若不分配,整个循环一路跑不进 GC——这是好事(热循环不被 GC 打断)。
- **NaN limit**:若 limit 是 NaN,`idx <= NaN` 恒 false → 循环一次不执行就退出(IEEE 语义,与 Lua 一致)。

> **整数 vs 浮点 for**:Lua 5.1 没有整数子类型(roadmap §6 锁定),for 索引就是 double。大整数循环(`for i=1,2^53`)在 double 精度内精确,超过 2^53 会丢精度——与 Lua 5.1 行为一致,不特殊处理。P4 可在「检测到索引始终是小整数」时投机整数循环(见 [../p4-method-jit](../p4-method-jit/00-overview.md)),P1 一律 double。

### 10.2 泛型 for:TFORLOOP

[02](./02-bytecode-isa.md) §4-33 / §6:泛型 for `for k,v in iter,state,ctrl` 占 `R(A..A+2)`(迭代函数/状态/控制变量)+ 循环变量。`TFORLOOP A C`:调用 `R(A)(R(A+1), R(A+2))`,产出 `R(A+3..A+2+C)`;若首产出值非 nil 则 `R(A+2) := R(A+3)`(更新控制变量),否则 `pc++`(跳过紧随的回边 JMP,退出循环)。

```go
case bytecode.TFORLOOP:
    a, c := A(i), C(i)
    // 1. 把迭代器 + 两个参数拷到调用区(R(A+3) 起,作为临时调用帧)
    callBase := f.base + a + 3
    f.stk[callBase]   = f.stk[f.base+a]      // iter 函数
    f.stk[callBase+1] = f.stk[f.base+a+1]    // state
    f.stk[callBase+2] = f.stk[f.base+a+2]    // control
    // 2. 调用 iter(state, control),期望 C 个返回值,落 R(A+3..A+2+C)
    //    —— 这是一次完整 CALL:iter 可能是 Lua closure(reentry)或 host(如 next/ipairs 迭代器)
    if e := vm.callFixed(f, callBase, /*nargs*/2, /*nresults*/c); e != nil {
        return vm.throw(f, e)
    }
    // 3. 检查首产出 R(A+3)
    if f.stk[f.base+a+3] != value.Nil {
        f.stk[f.base+a+2] = f.stk[f.base+a+3]  // 控制变量 = 首返回值(供下次迭代)
        // 落到紧随的 JMP(回跳循环体)
    } else {
        f.pc++                                  // 首值 nil:跳过 JMP,退出循环
    }
```

要点:

- **TFORLOOP 内含一次函数调用**(调迭代器),所以**可能 reentry(迭代器是 Lua 函数)或调 host(`next`/`pairs`/`ipairs` 的迭代器是 host function)**。调用返回后 **重载 stk**(可能分配/扩容)。
- **控制变量传递**:首返回值非 nil 即作为下次迭代的 control(`R(A+2)`),这是泛型 for 的状态机(如 `next(t, k)` 返回下一个 k)。
- **TFORLOOP 后必跟 JMP**(codegen 保证,类似比较指令对):首值非 nil 落到 JMP 回跳;首值 nil 则 `pc++` 跳过 JMP 退出。
- **`pairs`/`ipairs` 的迭代器**是 host function(`next` / 数组步进),由 [10](./10-stdlib.md) 提供;它们的调用走 §7.5 host 路径,同步执行。

---

## 11. SETLIST / NEWTABLE / GETGLOBAL 等表构造与全局访问

### 11.1 NEWTABLE

`NEWTABLE A B C`:`R(A) := {}`,预分配数组 `B`、哈希 `C` 槽([02](./02-bytecode-isa.md) §4-10,B/C 是 `int2fb` 浮点编码)。

```go
case bytecode.NEWTABLE:
    asize := fb2int(B(i))     // 解码近似容量([02] §10 待落 helper)
    hsize := fb2int(C(i))
    tref := vm.newTable(asize, hsize)   // 分配 Table + array/hash 区(arena)→ safepoint!
    f.stk[f.base+A(i)] = value.MakeGC(value.TagTable, tref)
    reloadFrame(f)
```

分配 → safepoint,重载 stk。新表 `gen=0`(§6.1 初始代次)。

### 11.2 SETLIST

`SETLIST A B C`:`R(A)[(C-1)*FPF + i] := R(A+i)`,i=1..B(批量填数组,[02](./02-bytecode-isa.md) §4-34)。`B=0` 到 top;`C=0` 取下一指令为大批次号;`FPF=50`([02](./02-bytecode-isa.md) §4)。

```go
case bytecode.SETLIST:
    a := A(i)
    n := B(i); if n == 0 { n = int(ci.top) - (f.base + a) - 1 }  // B=0:到 top
    batch := C(i); if batch == 0 { batch = int(f.proto.Code[f.pc]); f.pc++ } // C=0:下条是大批次号
    base0 := (batch - 1) * FPF
    t := object.TableAt(vm.arena, value.GCRefOf(f.stk[f.base+a]))
    for j := 1; j <= n; j++ {
        t.SetInt(base0+j, f.stk[f.base+a+j])   // 直接填数组部分(可能触发 array 扩容 → rehash → bump gen)
    }
    reloadFrame(f)   // 可能 rehash 分配
```

- **批量填数组**:表构造 `{1,2,3,...}` 用 SETLIST 一次填一批(每批 FPF=50),避免逐个 SETTABLE。
- **`B=0` 到 top**:配合前面产生多值的指令(如 `{f()}` 把 f 的所有返回值入表),用 `top` 界定个数。
- **`C=0` 大批次**:超过 9-bit 能编码的批次号时,下一条指令整体当批次号(罕见,大表)。
- 填数组可能触发 array 扩容/rehash → **bump 该表 gen**(§6.5,失效相关 IC),safepoint,重载 stk。

### 11.3 GETGLOBAL / SETGLOBAL(IC,见 §6.4)

`GETGLOBAL A Bx`:`R(A) := Gtable[K(Bx)]`,`K(Bx)` 是字符串常量键。走 §6.3 的 IC 流程,目标表恒为 globals。globals 在 Lua 5.1 是当前环境表(经 closure 的 `_ENV` upvalue / registry,[02](./02-bytecode-isa.md) §2 记号 `G`)。命中 = globals 代次比对 + 直达槽。`SETGLOBAL` 对称,插入新全局键触发 globals rehash → bump globals gen → 失效旧全局 IC(§6.4)。

---

## 12. 主循环完整骨架(汇总)

把 §2.3 / §5 / §7 / §9 合并的取指→译码→执行→safepoint 骨架(基线 (a),伪 Go):

```go
func (vm *VM) execute(th *Thread) (err *LuaError) {
    entryCi := th.ciTop          // §7.3:reentry 边界
    f := vm.loadTopFrame(th)
    code := f.proto.Code

    for {
        i := code[f.pc]
        f.pc++                    // pc 先增:JMP/FOR* 的 sBx 以下一条为基准([02] §9-1)

        switch bytecode.Op(i) {

        // —— 无分配、无切帧:直接执行,不设 safepoint ——
        case bytecode.MOVE:     f.stk[f.base+A(i)] = f.stk[f.base+B(i)]
        case bytecode.LOADK:    f.stk[f.base+A(i)] = f.k[Bx(i)]
        case bytecode.LOADBOOL:
            f.stk[f.base+A(i)] = value.BoolValue(B(i) != 0)
            if C(i) != 0 { f.pc++ }                 // 条件表达式跳过下一条([02] §4-2)
        case bytecode.LOADNIL:
            for r := A(i); r <= B(i); r++ { f.stk[f.base+r] = value.Nil }  // 闭区间
        case bytecode.JMP:      f.pc += SBx(i)
        case bytecode.GETUPVAL: /* §8.1 */
        case bytecode.SETUPVAL: /* §8.1 */
        case bytecode.NOT, bytecode.UNM, bytecode.LEN: /* §4.3,LEN-on-table/string 不分配 */
        case bytecode.EQ, bytecode.LT, bytecode.LE:    /* §4.4,快路径不分配 */
        case bytecode.TEST, bytecode.TESTSET:          /* §4.5 */
        case bytecode.FORLOOP, bytecode.FORPREP:       /* §10.1,快路径不分配 */

        // —— 算术:快路径不分配;慢路径(元方法)经调用边界 safepoint ——
        case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
            if e := vm.doArith(f, i, arithOpOf(i)); e != nil { return vm.throw(f, e) }
            // doArith 快路径无分配;慢路径内部走 §7 调用(自带 safepoint)+ 重载 stk

        // —— 可能分配 → 末尾 safepoint + 重载 stk ——
        case bytecode.NEWTABLE: /* §11.1 */ ; vm.safepoint(f)
        case bytecode.CONCAT:   /* §4.6  */ ; vm.safepoint(f)
        case bytecode.CLOSURE:  /* §8.2  */ ; vm.safepoint(f)
        case bytecode.SETLIST:  /* §11.2 */ ; vm.safepoint(f)

        // —— 表/全局访问:IC;插入可能 rehash(safepoint)——
        case bytecode.GETTABLE: if e := vm.doGetTable(f, i); e != nil { return vm.throw(f, e) }
        case bytecode.SETTABLE: if e := vm.doSetTable(f, i); e != nil { return vm.throw(f, e) }; vm.safepoint(f)
        case bytecode.GETGLOBAL:if e := vm.doGetGlobal(f, i); e != nil { return vm.throw(f, e) }
        case bytecode.SETGLOBAL:if e := vm.doSetGlobal(f, i); e != nil { return vm.throw(f, e) }; vm.safepoint(f)
        case bytecode.SELF:     if e := vm.doSelf(f, i); e != nil { return vm.throw(f, e) }

        // —— 调用边界:reentry / host / 退帧;天然层边界 safepoint ——
        case bytecode.CALL:
            switch vm.doCall(f, i) {
            case callEnteredLua:  code = f.proto.Code   // reentry,重载指令流
            case callReturnedHost: /* host 已同步执行完 */
            case callError:       return vm.throw(f, vm.pendingErr)
            }
            vm.safepoint(f)
        case bytecode.TAILCALL:
            switch vm.doTailCall(f, i) {
            case callEnteredLua:  code = f.proto.Code
            case callReturnedHost: /* host 尾调用结果即本帧返回 → 退帧 */
            case callError:       return vm.throw(f, vm.pendingErr)
            }
        case bytecode.RETURN:
            switch vm.doReturn(f, i) {
            case returnToCaller:  code = f.proto.Code   // 退到调用者帧,重载指令流
            case returnTerminate: return nil            // 退到 entryCi 之下 → execute 结束
            }
        case bytecode.TFORLOOP:
            if e := vm.doTForLoop(f, i); e != nil { return vm.throw(f, e) }
            vm.safepoint(f)

        case bytecode.VARARG:   /* §8.5 */ ; vm.safepoint(f) // B=0 可能扩容
        }
    }
}

// safepoint helper(§5.3):仅在分配 opcode 末尾调用。
func (vm *VM) safepoint(f *frame) {
    if vm.gcPending {
        vm.pushShadowRoots(f); vm.gc.Collect(); vm.popShadowRoots()
        reloadFrame(f)
    }
}
```

> **dispatch 与执行解耦**:所有 `vm.doXxx` 是可复用 helper(§2.2 纪律),换 closure-threading dispatch 时它们不变——保住语义 oracle 唯一性。`code` 局部变量 + `callEnteredLua`/`returnToCaller` 重载是 reentry 的全部魔法(§7)。

---

## 13. 文档缺口 / 待决(记入 memory/doc-gaps)

- **dispatch spike 定稿**:closure-threading 的具体闭包签名、操作数捕获策略、与 IC slot 指针绑定方式,留到 P1 提速 spike 阶段(§2.2)。基线 (a) 已可实现;(b)/(c) 的 A/B 口径见 [12](./12-testing-difftest.md)。
- ~~对 [01](./01-value-object-model.md) / [02](./02-bytecode-isa.md) 的回填~~:**已兑现**(§6.6/§8.6)——01 §5.2 gen、01 §5.4 nextOpen、02 §7 tableRef 均已落入对应布局。
- **`step==0` 数值 for**:当前按 Lua 5.1「未定义/可能死循环」处理(§10.1),是否改为友好报错待定(影响差分一致性——官方不报错,改了会与官方差分不一致,**倾向保持不报错**)。
- **字符串→数字 coercion 的精确边界**:已由 [07](./07-metatables-metamethods.md) §5.2 收口(算术/数值 for/tonumber 共用一套 `parseLuaNumber`),精确边界(十六进制、空白)由差分基准钉死。
- **`%.14g` 数字格式**:CONCAT/tostring 的数字→字符串格式(§4.6)需与 gopher-lua/官方逐字节核对,口径已锁定在 [12](./12-testing-difftest.md) 验收口径总表。
- **mono IC 是否够用**:P1 用 mono IC(§6.3),频繁多态访问点退化为无 IC。是否需要 P1 就上 2-4 路 polymorphic IC,待性能 spike(多数列内核负载是 mono,**倾向 P1 只做 mono**,polymorphic 留 P2 反馈驱动)。
- **host panic 兜底语义**:§9.4 的顶层 recover 兜底(host 真 panic 时转 Lua 错误 + 标 Thread 损坏)的精确语义与可恢复性,已由 [09](./09-errors-pcall.md) 定稿。
- **协程侧扩展**(承 [08](./08-coroutines.md) 回填,已在 §1.3/§2.3 登记):`executeSignal` 三态泛化、`saveFrame` 对称操作、`callHost` 的 callYield 分支;`resumeNCcallsBaseline` 放 VM 运行期变量(08 倾向),不改 Thread 布局。

---

相关:[01-value-object-model](./01-value-object-model.md) · [02-bytecode-isa](./02-bytecode-isa.md) ·
[06-memory-gc](./06-memory-gc.md) · [07-metatables-metamethods](./07-metatables-metamethods.md) ·
[08-coroutines](./08-coroutines.md) · [09-errors-pcall](./09-errors-pcall.md) ·
[10-stdlib](./10-stdlib.md) · [11-embedding-arena-abi](./11-embedding-arena-abi.md) ·
[12-testing-difftest](./12-testing-difftest.md) · [../p2-bridge/00-overview](../p2-bridge/00-overview.md) ·
[../p4-method-jit](../p4-method-jit/00-overview.md) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
[value-representation](../../../llmdoc/architecture/value-representation.md)
