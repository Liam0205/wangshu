# P1 脊柱:字节码 ISA

> 状态:**设计阶段,可实现深度**。本文是 codegen(`internal/frontend/compile`)与解释器
> (`internal/crescent`)之间的**指令集契约**,也是 P3 字节码→Wasm 翻译的源 ISA。
> opcode 编号与语义以本文为单一事实源。值表示见 [01-value-object-model](./01-value-object-model.md)。

对应 Go 包:`internal/bytecode`。

---

## 1. 为什么寄存器式(对 roadmap §4「寄存器式字节码」的展开)

roadmap §4 选寄存器式 VM(对解释器友好,日后翻译 Wasm locals 也直白)。设计采用 **Lua 5.1 风格的寄存器机**(与官方 luac 同族),收益:

- 指令数少(寄存器机典型比栈机少 ~50% 指令),dispatch 次数少 ⇒ 直接利好 roadmap §1 的「减少每指令开销」;
- 寄存器 = thread 值栈槽(见 [01](./01-value-object-model.md) §5.6),`R(i)=stack[base+i]`,无独立操作数栈;
- 翻译到 Wasm 时,寄存器可直接落为 Wasm `local` 或 linear-memory 槽,P3 翻译近乎直译(roadmap §4「翻译为 Wasm locals 也直白」)。

**与官方 Lua 5.1 的关系:** 我们**自定义 opcode 编号与编码宽度**(不二进制兼容官方 `.luc`),但**语义对齐 5.1**,以便复用官方 conformance 套并做差分(见 [12-testing-difftest](./12-testing-difftest.md))。

---

## 2. 指令编码

指令固定 **32 bit**(`type Instruction uint32`)。三种格式(对齐官方 iABC/iABx/iAsBx 思路,但字段宽度自定):

```
 31              23              14         6        0
 +-------+--------+--------+--------+--------+
格式 ABC :  |   B:9   |   C:9   |  A:8   | OP:6 |      (31..23=B, 22..14=C, 13..6=A, 5..0=OP)
格式 ABx :  |        Bx:18      |  A:8   | OP:6 |      (31..14=Bx 无符号)
格式 AsBx:  |       sBx:18      |  A:8   | OP:6 |      (sBx = Bx - 131071,有符号)
```

字段范围:
- `OP`:6 bit ⇒ 最多 64 opcode(P1 用 ~40,留余量)。
- `A`:8 bit ⇒ 目标寄存器 0..255(故 `MaxStack ≤ 250`,与 Lua 5.1 一致,留 fixstack 余量)。
- `B`,`C`:各 9 bit。最高位是 **RK 标志**:值 `0..255` 表示寄存器 `R`,值 `256..511`(即高位置 1)表示常量 `K[operand-256]`。记 `RK(B)`。⇒ 寄存器与常量各 ≤256;常量超 256 用 `LOADK`(见 §5)。
- `Bx`:18 bit 无符号 0..262143(常量索引 / proto 索引,容量远超 256 限制)。
- `sBx`:18 bit 有符号 ±131071(跳转偏移,覆盖超大函数)。

辅助记号:`R(x)`=寄存器 x;`K(x)`=常量 x;`RK(x)`=x<256?R(x):K(x-256);`G`=全局表(`_ENV` 在 5.1 即 globals,经 upvalue/registry)。

编解码 helper(`internal/bytecode`):
```go
func Op(i Instruction) OpCode { return OpCode(i & 0x3F) }
func A(i Instruction) int     { return int(i>>6) & 0xFF }
func B(i Instruction) int     { return int(i>>23) & 0x1FF }
func C(i Instruction) int     { return int(i>>14) & 0x1FF }
func Bx(i Instruction) int    { return int(i>>14) & 0x3FFFF }
func SBx(i Instruction) int   { return Bx(i) - 131071 }
func IsK(rk int) bool         { return rk >= 256 }
func KIdx(rk int) int         { return rk - 256 }
```

---

## 3. 栈帧 / 调用约定

```
thread.valueStack:
  ... | [base] R0 R1 ... R(MaxStack-1) | [top] 临时/调用参数区 ...
```
- `base` = 当前函数第 0 寄存器在值栈的绝对索引(存于 CallInfo)。
- `R(0..NumParams-1)` = 形参;vararg 函数的额外实参存于 base 之下的负区(见 §6 `VARARG`)。
- **调用**:`CALL A B C` 约定被调函数在 `R(A)`,参数 `R(A+1..A+B-1)`,返回值回填 `R(A..A+C-2)`。`B=0` 表示参数一直到 `top`(配合前一条产生多值的指令);`C=0` 表示返回值数目可变(传播到 `top`)。
- CallInfo 结构与 base 维护见 [05-interpreter-loop](./05-interpreter-loop.md)。

---

## 4. OpCode 完整表(单一事实源)

编号即实现枚举值(`iota` 顺序)。语义列用 §2 记号。所有「可触发 metamethod」的指令在操作数类型不满足快路径时回退元方法(见 [07](./07-metatables-metamethods.md))。

| # | 助记符 | 格式 | 语义 |
|---|---|---|---|
| 0 | `MOVE` | ABC | `R(A) := R(B)` |
| 1 | `LOADK` | ABx | `R(A) := K(Bx)` |
| 2 | `LOADBOOL` | ABC | `R(A) := bool(B); if C≠0 then pc++`(C 用于条件表达式跳过下一条) |
| 3 | `LOADNIL` | ABC | `R(A), R(A+1), ..., R(B) := nil`(闭区间,B≥A) |
| 4 | `GETUPVAL` | ABC | `R(A) := Upval(B)` |
| 5 | `SETUPVAL` | ABC | `Upval(B) := R(A)` |
| 6 | `GETGLOBAL` | ABx | `R(A) := Gtable[K(Bx)]`(经 globals 表 + 可能 `__index`)** IC** |
| 7 | `SETGLOBAL` | ABx | `Gtable[K(Bx)] := R(A)` ** IC** |
| 8 | `GETTABLE` | ABC | `R(A) := R(B)[RK(C)]`(可触发 `__index`)** IC** |
| 9 | `SETTABLE` | ABC | `R(A)[RK(B)] := RK(C)`(可触发 `__newindex`)** IC** |
| 10 | `NEWTABLE` | ABC | `R(A) := {}`,预分配数组 `B`(浮点编码)、哈希 `C`(浮点编码)槽 |
| 11 | `SELF` | ABC | `R(A+1) := R(B); R(A) := R(B)[RK(C)]`(方法调用 `obj:m()` 优化)** IC** |
| 12 | `ADD` | ABC | `R(A) := RK(B) + RK(C)`(数字快路径,否则 `__add`)** IC** |
| 13 | `SUB` | ABC | `R(A) := RK(B) - RK(C)`(否则 `__sub`) |
| 14 | `MUL` | ABC | `R(A) := RK(B) * RK(C)`(否则 `__mul`) |
| 15 | `DIV` | ABC | `R(A) := RK(B) / RK(C)`(否则 `__div`;浮点除,/0 得 ±Inf) |
| 16 | `MOD` | ABC | `R(A) := RK(B) % RK(C)`(Lua 语义:`a-floor(a/b)*b`,否则 `__mod`) |
| 17 | `POW` | ABC | `R(A) := RK(B) ^ RK(C)`(`math.pow` 语义,否则 `__pow`) |
| 18 | `UNM` | ABC | `R(A) := -R(B)`(否则 `__unm`) |
| 19 | `NOT` | ABC | `R(A) := not R(B)`(真值取反,无 metamethod) |
| 20 | `LEN` | ABC | `R(A) := #R(B)`(string 长度 / table border / `__len`*) |
| 21 | `CONCAT` | ABC | `R(A) := R(B) .. R(B+1) .. ... .. R(C)`(右结合,否则 `__concat`) |
| 22 | `JMP` | AsBx | `pc += sBx`(无条件跳转) |
| 23 | `EQ` | ABC | `if (RK(B)==RK(C)) ≠ bool(A) then pc++`(配合下一条 JMP;`__eq`) |
| 24 | `LT` | ABC | `if (RK(B)<RK(C)) ≠ bool(A) then pc++`(`__lt`) |
| 25 | `LE` | ABC | `if (RK(B)<=RK(C)) ≠ bool(A) then pc++`(`__le`) |
| 26 | `TEST` | ABC | `if bool(R(A)) ≠ bool(C) then pc++`(真值测试,用于 `and`/`or` 短路) |
| 27 | `TESTSET` | ABC | `if bool(R(B))==bool(C) then R(A):=R(B) else pc++` |
| 28 | `CALL` | ABC | 调用 `R(A)(R(A+1..A+B-1))`,返回回填 `R(A..A+C-2)`;B/C=0 见 §3 |
| 29 | `TAILCALL` | ABC | 尾调用 `R(A)(R(A+1..A+B-1))`,复用当前帧(栈不增长) |
| 30 | `RETURN` | ABC | 返回 `R(A..A+B-2)`;`B=0` 返回到 `top` |
| 31 | `FORLOOP` | AsBx | 数值 for 回边:`R(A)+=R(A+2); if R(A)<?=R(A+1) then {pc+=sBx; R(A+3):=R(A)}` ** 热点回边** |
| 32 | `FORPREP` | AsBx | 数值 for 准备:`R(A)-=R(A+2); pc+=sBx`(校验三槽为数字) |
| 33 | `TFORLOOP` | ABC | 泛型 for:调用迭代器 `R(A)`,产出 `R(A+3..A+2+C)`;若首值非 nil 则 `R(A+2):=R(A+3)` 否则 `pc++` |
| 34 | `SETLIST` | ABC | `R(A)[ (C-1)*FPF + i ] := R(A+i)`,i=1..B(表构造批量填数组;B=0 到 top;C=0 取下一指令为大批次号) |
| 35 | `CLOSE` | ABC | 关闭所有 ≥ `R(A)` 的开放 upvalue(作用域退出) |
| 36 | `CLOSURE` | ABx | `R(A) := closure(Proto[Bx])`,随后 `nupvals` 条伪指令(`MOVE`/`GETUPVAL`)描述 upvalue 捕获 |
| 37 | `VARARG` | ABC | `R(A..A+B-2) := ...`(把 vararg 拷入;B=0 到 top) |

记号补充:
- `bool(A)` 在 EQ/LT/LE/TEST 中是比较期望布尔(用于把「比较+条件跳」编码成两指令对 `CMP; JMP`)。
- ** IC**:该指令带 inline cache slot(见 §7)。
- ** 热点回边**:`FORLOOP`(及循环里的 `JMP` 向后)是 P2 热度计数的 back-edge 采样点(见 [../p2-bridge/00-overview](../p2-bridge/00-overview.md))。
- `*` `__len` 在 Lua 5.1 仅对 userdata 生效(table 的 `__len` 是 5.2+,roadmap §6 已排除);本表 `LEN` 对 table 直接取 border。
- `FPF`(fields per flush)= `SETLIST` 每批字段数,定为 **50**(与 Lua 5.1 `LFIELDS_PER_FLUSH` 一致)。

> 编号 38..63 预留:P2/P3 可能新增 `tier guard`、`profile counter` 等伪指令,**不占用 0..37**,保证 P1 字节码向后兼容上层翻译。

---

## 5. 常量池(K)

`Proto.Consts []Value`(见 [01](./01-value-object-model.md) §5.7):
- 数字常量:直接 NaN-boxed double。
- 字符串常量:已 intern 的 arena GCRef(是 GC 根)。
- `nil`/`bool` 一般不入常量池(用 `LOADNIL`/`LOADBOOL`),但允许存在。
- `RK` 槽位 ≤256;超出部分 codegen 必须改用 `LOADK`(Bx 18-bit 容纳大常量表)。

---

## 6. 数值 for 与 vararg 的寄存器约定

**数值 for**(`for v=init,limit,step do`)占 4 个连续寄存器 `R(A..A+3)`:
- `R(A)` = 内部索引(init),`R(A+1)` = limit,`R(A+2)` = step,`R(A+3)` = 外部可见循环变量 `v`。
- `FORPREP` 预减一个 step 并跳到 `FORLOOP`;`FORLOOP` 加 step、判界、回跳并刷新 `v`。三槽必须是 number,否则 `FORPREP` 报错 `'for' initial value must be a number`。

**泛型 for**(`for k,v in iter,state,ctrl`)占 `R(A..A+2)`+循环变量:
- `R(A)`=迭代函数,`R(A+1)`=状态,`R(A+2)`=控制变量;`TFORLOOP` 调用 `R(A)(R(A+1),R(A+2))`,结果落 `R(A+3..)`。

**vararg**:`IsVararg` 函数的多余实参存在 `base` 之下;`VARARG` 指令按需拷回寄存器。`...` 个数 = `actualArgs - NumParams`,由 CallInfo 记录。

---

## 7. Inline Cache slot(为 P1 提速 + 为 P2 供料)

带 ** IC** 的指令(GETGLOBAL/SETGLOBAL/GETTABLE/SETTABLE/SELF/算术 IC)在 Proto 旁附 IC 数组(不占指令位,按 pc 索引):

```go
type ICSlot struct {
    shape    uint32 // 缓存的 table "形状":目标表的 gen 代次(01 §5.2;globals 是其特例)
    index    uint32 // 命中时直达的数组/节点槽索引
    tableRef uint32 // 目标表 arena 偏移低 32 位,身份比对用(非 GC 根;承 05 §6.6 回填)
    kind     uint8  // 0 未初始化 / 1 array hit / 2 node hit / 3 mono-metamethod / 4 megamorphic
}
```
- **算术 IC 的字段挪用**(承 [../p2-bridge/00-overview](../p2-bridge/00-overview.md) §3.6 回填):算术指令的 IC slot 无表可缓存,`shape`/`index`/`tableRef` 三字段闲置,挪用为 `numHits`/`metaHits` 双计数(P1 写不读:快路径 `numHits++`、元方法慢路径 `metaHits++`),为 P2 类型 feedback 供料。同一结构、按 kind 区分字段语义,不增尺寸。
- P1:`GETTABLE` 等命中 IC 时跳过哈希查找,直达槽位 ⇒ 兑现 roadmap §4「全局/表访问 inline cache」。
- P2:IC 命中分布是**类型 feedback**,记录后供编译层做类型投机(roadmap §4 P2「inline cache 反馈记录」)。
- 算术 IC 记录操作数实际类型(都是 number ⇒ P4 可发 f64 快路径 + guard,见 [../p4-method-jit](../p4-method-jit/00-overview.md))。

IC 失效:table 结构变化(rehash / 加 metatable)递增其 shape 版本;全局表写递增全局版本。详见 [05-interpreter-loop](./05-interpreter-loop.md) §IC。

---

## 8. 一个端到端编码示例

源:
```lua
local function f(n)
  local s = 0
  for i=1,n do s = s + i*i end
  return s
end
```
codegen 产出(`f` 的 Proto,寄存器:R0=n 形参,R1=s,R2..R5=for 四槽,R6=临时):
```
LOADK     R1  K0(0)          ; s = 0
LOADK     R2  K1(1)          ; init=1
MOVE      R3  R0             ; limit=n
LOADK     R4  K1(1)          ; step=1
FORPREP   R2  -> L1          ; 跳到 FORLOOP
L0: MUL   R6  R5 R5          ; tmp = i*i   (R5 是循环变量 i = R(A+3))
    ADD   R1  R1 R6          ; s = s + tmp
L1: FORLOOP R2  -> L0        ; i+=1; if i<=n goto L0  (热点回边)
RETURN    R1  2              ; return s (B=2 ⇒ 返回 1 个值)
RETURN    R0  1              ; 隐式 return(B=1 ⇒ 0 个值)
```
这段就是 roadmap §1「Horner 风格计算密集脚本」的同类形状:循环体在 VM 内迭代(列内核形状),`FORLOOP` 回边是热度采样与未来 trace 录制起点。

---

## 9. 不变式 / 实现注意

1. **指令定长 32-bit**:`Code []uint32`,pc 是下标;`JMP/FORLOOP/FORPREP` 的 sBx 以「下一条指令」为基准(执行时 pc 已自增)。
2. **A 永远是寄存器**(0..255),不是 RK;只有 B/C 可能是 K。
3. **比较指令成对**:`EQ/LT/LE/TEST/TESTSET` 后必跟 `JMP`,codegen 必须保证;解释器对它们做「条件 pc++ 跳过 JMP」。
4. **多值边界**:`CALL/RETURN/VARARG/SETLIST` 的 `B=0`/`C=0` 表示「到 top」,是多返回值传播的关键,实现时维护好 `top`。
5. **MaxStack 静态已知**:每个 Proto 编译期算出寄存器水位,解释器进入帧时确保栈容量(见 [05](./05-interpreter-loop.md))。
6. **语义对齐 5.1**:任何语义疑义以 Lua 5.1 参考实现为准,并由差分测试钉死(见 [12](./12-testing-difftest.md))。

---

## 10. 文档缺口 / 待决(记入 memory/doc-gaps)

- **NEWTABLE 的 B/C 浮点编码**(Lua 用 `int2fb`/`fb2int` 把容量近似编码进 9-bit)沿用 5.1 算法,实现时落 `internal/bytecode` helper,本文未展开公式。
- **opcode 是否需要 `GETTABLE_N`/`GETTABLE_S`(按键类型特化)** 这类 P1 提速变体待性能 spike 后决定,当前保持 5.1 最小集。
- IC slot 的 `shape` 版本号生成机制**已在 [05](./05-interpreter-loop.md) §6 定稿**(per-table 单调代次,globals 是特例);§7 的 `tableRef` 字段与算术 IC 双计数挪用即其回填结果。**本缺口已关闭。**

---

相关:[01-value-object-model](./01-value-object-model.md) · [04-frontend-parser-codegen](./04-frontend-parser-codegen.md) ·
[05-interpreter-loop](./05-interpreter-loop.md) · [../p2-bridge/00-overview](../p2-bridge/00-overview.md) · [../p3-wasm-tier](../p3-wasm-tier/00-overview.md)
