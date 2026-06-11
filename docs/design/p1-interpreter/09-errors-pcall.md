# P1 脊柱:错误模型 / pcall / error / 栈回溯

> 状态:**设计阶段,可实现深度**。本文是 Lua 5.1 **错误传播 + 保护调用 + 错误对象 + 栈回溯**的单一事实源:
> `LuaError` 对象细化、错误从产生到捕获的全景路径、`error`/`assert`/`pcall`/`xpcall` 语义、
> `traceback` 生成、函数名符号推断、运行期错误信息目录、stack overflow 两类、host panic 兜底、
> 协程错误边界、debug 库供料接口。
> 上游契约:[05-interpreter-loop](./05-interpreter-loop.md) §9 已定稿**错误传播机制**(显式 `*LuaError` 返回,
> **不用** panic/recover 跨主循环)——本文不推翻 05 的决策,而是**展开它留给本文的实现细节**:
> §9.2 `LuaError` struct、§9.3 pcall 骨架、§9.4 错误来源与 host 协作、host panic 兜底;
> 还承接 §7.3(reentry/fresh 边界 = 错误停靠站)、§7.4(stack overflow / C stack overflow 上限)、
> §1.2(CallInfo word3 `errfuncBase`/保护点字段)、§7.2(`ci.tailcall` → traceback)、§7.5(host 帧 CallInfo → `[C]` 帧)。
> 值/对象侧:[01](./01-value-object-model.md) §5.6 Thread `word7 errorJmp`(pcall 保护点链)、§5.7 Proto `LineInfo`/`Source`(错误位置来源)。
> 错误措辞与 [07](./07-metatables-metamethods.md) §14.4 共定:07 给「类型名层」措辞,**变量名后缀(`(global 'f')` 等)由本文定稿**。
> 语言面锁 Lua 5.1(`docs/design/roadmap.md` (§6),5.2+ 的 `error` level 行为、`xpcall` 传 args 等显式以 5.1 为准)。

对应 Go 包:错误逻辑(`LuaError` 构造、`throw`、traceback 生成、函数名推断、错误措辞)在
`internal/crescent`;`error`/`assert`/`pcall`/`xpcall`/`debug.traceback`/`debug.getinfo` 是
host functions,在 `internal/stdlib`(与 [10-stdlib](./10-stdlib.md) 协作);位置来源 `Proto.LineInfo`/`Source`
在 `internal/bytecode`(见 [01](./01-value-object-model.md) §5.7)。

---

## 0. 本文在 P1 中的位置与设计张力

错误处理是 Lua「机制开放」的另一半(与元方法并列):`error`/`pcall` 把异常控制流暴露成**普通可调用值**,
脚本可以把「失败」当数据传递、当控制流使用(`pcall` 远不止异常,常被当「尝试执行」用)。这一点直接决定了
05 §9.1 的关键选择——**用显式 `*LuaError` 返回而非 panic/recover**:因为 `pcall` 是热路径控制流,不能
为它支付 Go panic 的固定成本(05 §9.5)。

本文的全部张力来自三条约束的夹击:

1. **不推翻 05,只展开**。05 §9 已经把「错误对象长什么样、怎么冒泡、谁清理 CallInfo」定死了。本文不重新
   论证「为什么不用 panic」(指针:[05](./05-interpreter-loop.md) §9.1 的方案对比表),而是把 05 留白的
   **traceback 怎么生成、函数名怎么推断、level 怎么算、xpcall handler 何时调、错误措辞精确到什么字**补全到
   可实现。函数/字段名与 05 **严格一致**(`LuaError`、`throw`、`callLuaFromHost`、`nCcalls`、`ci.tailcall`、`errfuncBase`)。

2. **必须严格 Lua 5.1**(`roadmap.md` (§6))。错误是 5.1 与 5.2+ 差异密集区之一:`error(msg, level)` 的 level
   语义跨版本稳定但 `xpcall` 的签名 **5.1 不传额外 args 给 f**(5.2+ 才支持)、`assert` 抛的是**裸 message 不加位置**、
   traceback 的具体行格式逐版本有微调。**每一处 5.2+ 行为都显式标注以 5.1 为准**,否则差分基准([12](./12-testing-difftest.md))
   会因「行为偏移一个版本」而与官方 5.1 分叉(roadmap §5 原则 2:不仅 tier 间逐字节一致,**与官方 5.1 也要一致**)。

3. **错误路径不在热路径,但 traceback 质量决定可用性**。错误传播本身(`if e != nil { return e }`)接近零成本
   (05 §9.5),所以本文**不为性能让步**;但 traceback 的「函数名推断」要做字节码符号执行(逆推被调函数名),
   这是 Lua 5.1 调试信息里最复杂的一块,**P1 做简化版**(§8 标范围),完整推断记缺口。

> 一句话定位:本文是 05 §9 的**实现展开 + 调试信息层**。05 定「错误怎么流动」,本文定「错误对象携带什么、
> 报错说什么话、栈回溯长什么样、保护调用的四个内建(error/assert/pcall/xpcall)如何落地」。

---

## 1. 错误对象模型(细化 05 §9.2 的 `LuaError`)

### 1.1 `LuaError` struct(承 05 §9.2,补字段语义)

05 §9.2 给了 `LuaError` 的三个字段,本文细化每个字段的**语义、何时填、谁读**:

```go
// internal/crescent —— 错误传播的载体。它是 Go 侧对象(不入 arena),
// 在 helper 间 return 冒泡(05 §9.1);只在 execute 的调用链里短暂存活,被 protected 边界消费后丢弃。
type LuaError struct {
    value     value.Value   // ① error 的 Lua 值。通常是 string,但可是【任意 Lua 类型】(见 §1.2)
    traceback string        // ② 可选:产生时捕获的调用栈快照(§7)。仅在需要时填(见 §1.3)
    level     int           // ③ error(msg, level) 传入的 level,仅在【构造时加位置前缀】用过一次后即无意义
}
```

**字段语义逐条**:

- **`value`(必填)**:错误的「真身」。它是一个 NaN-boxed `value.Value`([01](./01-value-object-model.md) §3),
  被 `pcall` 捕获后作为第二返回值 `(false, value)` 交还脚本。**绝大多数是 string**(解释器内在错误、`error("msg")`),
  但 Lua 语义允许任意类型(`error({code=42})` 抛 table)——见 §1.2。
- **`traceback`(可选)**:产生错误时的栈回溯字符串(§7 格式)。**不是每个 `LuaError` 都填**:解释器内在错误与
  `error()` 在**抛出点不立即生成 traceback**(那会在错误路径分配字符串,且多数错误被 `pcall` 静默捕获后丢弃,
  白做)。traceback 只在两个时机生成:① `xpcall` 的 message handler 被调用时(handler 通常是 `debug.traceback`,
  §6/§7 在**栈展开前**调它);② 顶层错误传到 `Program.Call` 且宿主请求了 traceback(§11)。所以 `LuaError.traceback`
  字段是「若已生成则缓存于此」的槽,默认空串。
- **`level`(构造期用)**:仅 `error(msg, level)` 内建用它决定「位置前缀加在哪一层的位置」(§3)。**位置前缀在
  `error` 构造 `LuaError` 时就拼进 `value` 了**(若 `value` 是 string 且 level≠0),拼完 `level` 字段对后续冒泡
  无意义——它不参与捕获,也不被 `pcall` 读取。保留它只为调试/对称,实际可省(标 doc-gap)。

### 1.2 错误值可为任意 Lua 类型;位置前缀只对 string 加

Lua 5.1 的 `error(v)` 接受**任意值** `v`,该值原样成为错误对象。这要求 `LuaError.value` 是 `value.Value`
而非 Go `string`:

| `error(v)` 的 `v` | `LuaError.value` | 位置前缀 | `pcall` 捕获后脚本看到 |
|---|---|---|---|
| string `"boom"` | `"foo.lua:12: boom"`(level≠0 时) | **加**(§3) | 带位置的字符串 |
| string + level=0 | `"boom"`(原样) | **不加** | 原样字符串 |
| number `42` | `42`(boxed number) | **不加** | `42` |
| table `{code=42}` | table 的 GCRef | **不加** | 同一个 table 对象 |
| nil / boolean | 原样 boxed | **不加** | 原样 |

**关键 5.1 口径**:**位置前缀 `"<source>:<line>: "` 只在「错误值是 string 且 level≠0」时添加**(§3.2)。
若 `v` 不是 string(table/number/...),`error` **不动它**,直接抛(`level` 被忽略)。理由:位置前缀是字符串
拼接,对非字符串无意义,且会破坏脚本「用 table 传结构化错误」的用法。解释器**内在错误**(类型错误等,§9)
产生的 `value` 恒为 string,所以它们总带位置前缀(在错误措辞前拼 `"<source>:<line>: "`,§9.1)。

> 这与 07 §14.4 衔接:07 的 `indexError`/`arithError` 等给「类型名层措辞」(如 `attempt to index a nil value`),
> 本文在构造时**前缀位置 + 后缀变量名**,得到完整的 `foo.lua:12: attempt to index a nil value (global 'x')`(§9.1)。

### 1.3 `LuaError` 的生命周期与不入 arena

`LuaError` **住 Go 堆,不入 arena**——它不是 Lua 值,是「Go 侧的错误传播信封」。但它**包着**一个 `value.Value`
(`value` 字段),该 Value 若是可回收类型(string/table)则指向 arena。**关键 GC 纪律**:错误冒泡期间,
`LuaError.value` 指向的 arena 对象必须存活,否则 traceback 还没生成、`pcall` 还没读到,对象就被回收了。

落地:错误冒泡是**同步的、无分配的 return 链**(05 §9.1),从抛出点到 protected 边界之间**不触发 GC**
(冒泡路径上的 `return e` 不分配、不调用)。所以 `LuaError.value` 在冒泡途中天然安全。**唯一例外**:
若 message handler(§6)在错误点被调用并分配(它会——`debug.traceback` 拼字符串),此时 `LuaError.value`
必须作为 **GC 根**临时登记(把它压 shadow stack,05 §5.3)。详见 §6.3 的 handler 调用纪律。

---

## 2. 错误传播全景(引用 05 §9.1 决策,给完整路径图)

### 2.1 决策回顾(不重复论证,给指针)

05 §9.1 已定稿:**显式 `*LuaError` 返回,不用 panic/recover 跨主循环**。三条理由(与 reentry 模型冲突、
panic 性能、可控性)详见 [05](./05-interpreter-loop.md) §9.1 的方案对比表,本文不复述。本节只把这条决策**落成
一张端到端路径图**,标清每一跳谁负责什么。

### 2.2 错误从产生到捕获的完整路径图

```
                          ┌──────────────────────────────────────────────┐
   ① 错误产生(三类来源,§9 / §5.4)                                       │
   ┌─────────────────────────────────────────┐                          │
   │ a. 解释器内在错误:doArith/doGetTable/... │                          │
   │    检测到非法类型 → 构造 *LuaError        │                          │
   │ b. error(v)/assert 内建:host 调 raise    │   每个 helper 返回        │
   │    → 构造 *LuaError                       │   非 nil *LuaError        │
   │ c. host function 出错:host 调 raise       │                          │
   └─────────────────────────────────────────┘                          │
                    │ return *LuaError                                    │
                    ▼                                                     │
   ② 主循环冒泡(05 §2.3 / §12)                                          │
   ┌─────────────────────────────────────────┐                          │
   │ case ADD: if e:=doArith(); e!=nil {       │                          │
   │             return vm.throw(f, e)  ───────┼──┐                       │
   │           }                                │  │ throw 不 panic,      │
   │ ... 每个可能出错的 case 同构              │  │ 直接 return 出 execute │
   └─────────────────────────────────────────┘  │                       │
                                                  ▼                       │
   ③ execute 返回 *LuaError(05 §9.2)                                     │
      execute 可能管着多层 Lua 帧(CallInfo 在 arena)。                  │
      错误 return 出 execute 这个 Go 函数,**不逐帧清理**——             │
      CallInfo 清理责任在 ④ 的边界(05 §9.3)。                          │
                    │                                                     │
                    ▼                                                     │
   ④ protected 边界捕获(§5 pcall / §6 xpcall)                          │
   ┌─────────────────────────────────────────┐                          │
   │ pcall host:                               │                          │
   │   if e := callLuaFromHost(f,args);        │  这是错误冒泡的         │
   │      e != nil {                            │  「停靠站」(05 §7.3:   │
   │     回退 ciTop 到保护点(丢弃出错帧)     │   标 callStatus_fresh   │
   │     closeUpvals 到保护点 base             │   的 reentry 边界)      │
   │     恢复栈 top                             │                          │
   │     return (false, e.value)  ─────────────┼──── 转成 Lua 返回值      │
   │   }                                        │                          │
   └─────────────────────────────────────────┘                          │
                    │ 若【无】pcall 保护                                  │
                    ▼                                                     │
   ⑤ 一路 return 到顶层 Program.Call(§11)                               │
      最外层把 *LuaError 转 Go error 交还宿主;Thread 标 dead/重置。      │
                                                                          │
   (协程边界 = 另一类停靠站,§12:错误穿过 resume → coroutine.resume     │
    返回 (false, err),协程变 dead)                                      ─┘
```

**每一跳的责任归属(定稿)**:

| 跳 | 谁 | 责任 | 不负责 |
|---|---|---|---|
| ① 产生 | helper(`doArith` 等)/ host(`raise`) | 构造 `*LuaError`(填 `value`,加位置前缀+变量名) | 不生成 traceback(除非 xpcall handler 要,§6);不清理栈 |
| ② 冒泡 | 主循环 case | `return vm.throw(f, e)` | 不清理 CallInfo(交边界) |
| ③ 退出 execute | `execute` | `return e` 出 Go 函数 | 不逐帧清理(显式返回 vs panic 的关键简化,05 §9.3) |
| ④ 捕获 | protected 边界(pcall/xpcall host) | 回退 ciTop + closeUpvals + 恢复 top + 转返回值 | —— |
| ⑤ 顶层 | `Program.Call` | 转 Go error + 标 Thread 状态 | —— |

**澄清「CallInfo 清理责任在边界」(05 §9.3 的核心简化)**:出错帧与中间所有 Lua 帧**不做任何 cleanup**,
只管 `return e` 冒泡。这正是显式返回相对 panic 的关键收益——panic 要在**每帧** defer 关 upvalue、恢复 top,
而显式返回让 protected 边界**一次性**把 `ciTop` 回退到保护点(丢弃一摞 CallInfo)、一次 `closeUpvals` 关掉
保护点之上所有开放 upvalue(05 §8.3)、一次恢复 `top`。**O(被丢弃帧数) 的批量清理,而非 O(帧数) 的逐帧 defer**。

---

## 3. `error(message, level)` —— 位置前缀的生成

### 3.1 Lua 5.1 `error` 语义

`error(message [, level])` 是 base 库 host function([10](./10-stdlib.md) 提供,语义本文定稿)。它**终止当前函数
并抛出 message 作为错误**:

```
error(message, level):           -- level 默认 1
  if message 是 string and level != 0:
    pos := 位置前缀(从 CallInfo 链回溯 level 层,取该层 pc → line)   -- §3.2
    message := pos .. message      -- 前缀拼接(仅 string 且 level≠0)
  -- message 非 string,或 level==0:原样,不加前缀
  raise(message)                   -- 构造 *LuaError{value: message},开始冒泡(§3.3)
```

**level 语义(逐值,Lua 5.1)**:

| level | 位置前缀指向 | 典型用途 |
|---|---|---|
| `0` | **不加位置前缀** | 错误信息已自带位置,或抛非字符串 |
| `1`(默认) | **调用 `error` 的那个函数**的当前位置 | 最常见:`error("bad arg")` 报在 error 被调用处 |
| `2` | 调用「调用 error 的函数」的**调用者**的位置 | 库函数把错误归咎于**调用者**(`luaL_argerror` 风格):`assert` 之外的参数检查 |
| `n` | 沿调用链再上溯 `n-1` 层 | 罕见;深层库封装 |

> **5.1 vs 5.4 口径**:`error` 的 level 语义在 5.1→5.4 **稳定不变**(都是「level 层向上取位置」)。5.4 对
> `error` 无破坏性改动。所以本节无需版本分叉标注——这是错误模型里少数跨版本一致的部分。**唯一要锁 5.1 的是
> 位置前缀格式**(`"<source>:<line>: "`,见 §3.2)与「非 string 不加前缀」,这两条 5.1/5.4 也一致,放心实现。

### 3.2 位置前缀格式与从 CallInfo 链回溯 level 层

**位置前缀格式(Lua 5.1,定稿)**:

```
"<source>:<line>: "          -- 注意冒号后有一个空格;source 与 line 间一个冒号
例:"foo.lua:12: "  /  "@/path/script.lua:3: "  /  "[string \"a=1\"]:1: "
```

- **`<source>`**:出错帧 Proto 的 `Source`([01](./01-value-object-model.md) §5.7 `Source GCRef`)。Lua 5.1 的
  source 带前缀符号:`@filename`(文件,显示时**去掉 `@`**)、`=name`(自定义名,去掉 `=`)、`[string "..."]`
  (字符串 chunk,显示为截断的源码片段)。这套「source → 短名」的规约叫 `luaO_chunkid`,**P1 需实现**(§3.4)。
- **`<line>`**:由该帧**当前 pc** 经 `Proto.LineInfo[pc]` 查出(02 §5:LineInfo 每指令一个源行号)。
- **C 帧无位置前缀**:若 level 指向的是 host(C)帧,Lua 5.1 不加 `<source>:<line>:`(C 函数无行号)。
  此时前缀为空,message 原样(等价 level=0 的效果)。

**回溯 level 层的伪码**(`luaL_where` 等价物):

```go
// 从「调用 error 的函数」起,向上数 level 层,取该帧的 source:line 前缀。
// level=1 即「调用 error 的函数」本身;level=2 是它的调用者;以此类推。
func (vm *VM) where(th *Thread, level int) string {
    // 当前 ci 链:[..., callerOfError, errorHostFrame(顶)]
    // error 本身是 host 帧,占 ciTop-1;level=1 指向 ciTop-2(调用 error 的 Lua 帧)。
    ci := th.ciAt(th.ciTop - 1 - level)        // 回溯 level 层(error 帧不计,故 -1-level)
    if ci == nil {
        return ""                               // level 超出栈深 → 无前缀(Lua 5.1 行为)
    }
    if ci.isHostFrame() {                       // protoID == 哨兵(05 §1.2 word2 host 标记)
        return ""                               // C 帧无 source:line
    }
    proto := vm.protos[ci.protoID]
    line := proto.LineInfo[ci.currentPC()]      // pc → line(§3.5:取该帧「当前正在执行的指令」pc)
    src := chunkID(vm.sourceShort(proto.Source)) // source → 短名(§3.4)
    return fmt.Sprintf("%s:%d: ", src, line)
}
```

**「当前 pc」的取法(关键细节,§3.5 详述)**:对**非栈顶帧**(level≥2 指向的调用者帧),其「当前 pc」是
它**发出调用时保存的 savedPC 的前一条**——因为 savedPC 指向「调用返回后要执行的下一条」(05 §1.2 word1),
报错位置应是「发出调用的那条 CALL」本身,故取 `savedPC - 1`。对栈顶活跃帧(若 level 能指到它),用 frame
的当前 `pc - 1`(pc 已自增,05 §2.3)。这套「savedPC-1 / pc-1」的偏移是 traceback 行号正确的命脉(§7.4)。

### 3.3 `raise` —— 从 host 把错误转入 execute 的冒泡路径

`error` 是 host function,它不能直接 `return` 出 `execute`(host 在 Go 栈上,execute 在它下面)。所以
**host 抛错要经 `raise` 把 `*LuaError` 转成「execute 开始冒泡」的信号**(承 05 §9.4):

```go
// host function(error/assert/解释器内在错误的 host 侧)抛 Lua 错误的统一入口。
// 它【不 Go panic】(05 §9.4:panic 绕过 CallInfo 清理),而是设置 pending 错误并让
// callHost 把控制权以 *LuaError 形式交还 execute 的冒泡。
func (vm *VM) raise(errval value.Value) callResult {
    vm.pendingErr = &LuaError{value: errval}   // 暂存;callHost 检查它
    return callError                            // callHost 返回此 → 主循环 return vm.throw(...)
}

// 便捷形态:解释器内在错误用 raisef 拼措辞 + 位置 + 变量名(§9.1)。
func (vm *VM) raisef(f *frame, format string, args ...any) *LuaError {
    msg := vm.where(f.thread(), 1) + fmt.Sprintf(format, args...)  // 前缀位置(level=1 = 出错帧自身)
    return &LuaError{value: vm.internString(msg)}
}
```

**机制衔接**(05 §7.6 host 调用约定 + §9.4):`callHost` 同步调用 host Go 函数后,检查 `vm.pendingErr`:
若非 nil(host 调过 `raise`),`callHost` 返回 `callError`,主循环 `case CALL` 的 `callError` 分支
`return vm.throw(f, vm.pendingErr)`(05 §12)——错误就此进入 ③ 的冒泡。**host 永不直接 return 出 execute,
也永不 Go panic**;它经 `raise` → `pendingErr` → `callHost` 返回 `callError` → 主循环 throw 这条规整路径。

### 3.4 `chunkID`:source → 显示短名(`luaO_chunkid` 等价)

Lua 5.1 把 `Proto.Source`(原始 chunk 名)转成 traceback/错误里显示的短名,规则(P1 需实现):

| source 首字符 | 含义 | 显示形态 | 例 |
|---|---|---|---|
| `@` | 文件名 | 去 `@`,过长则前缀 `...`(总长上限 `LUA_IDSIZE`≈60) | `@/a/b/foo.lua` → `foo.lua` 或 `...b/foo.lua` |
| `=` | 自定义名 | 去 `=`,截断到上限 | `=stdin` → `stdin` |
| 其它 | 字符串 chunk(`load`/`loadstring` 的源码) | `[string "首行截断..."]` | `[string "local a=1..."]` |

- **字符串 chunk 的截断**:取源码首行,若过长截断并加 `...`,包进 `[string "..."]`。换行符在首行处截断。
  这套规则细节(`LUA_IDSIZE`=60、截断位置)**待 12 差分核对**与官方逐字节对齐——traceback 里的 `[string ...]`
  形态是高频差分点。
- **P1 实现位置**:`internal/crescent` 的 `chunkID(source []byte, isFromSource bool) string`,或落 `bytecode`
  侧供 traceback 与 error 共用。Source 内容从 arena String 读([01](./01-value-object-model.md) §5.1)。

### 3.5 pc → line 与「当前 pc」的精确取值

**`Proto.LineInfo[pc]` 给指令 pc 的源行号**(02 §5)。但「报错该用哪个 pc」分三种帧:

| 帧类型 | 当前 pc 取法 | 理由 |
|---|---|---|
| 栈顶活跃帧(正在执行) | `frame.pc - 1` | pc 已自增(05 §2.3),出错的是「刚取出执行的那条」,故 -1 |
| 非栈顶帧(已发出调用,在等返回) | `ci.savedPC - 1` | savedPC 指向「返回后下一条」(05 §1.2),报错位置是发出调用的 CALL 本身,故 -1 |
| host(C)帧 | 无行号 | C 函数无 LineInfo;显示 `[C]` 不带行号 |

> **这个 -1 偏移是 traceback 与 error 位置正确性的命脉**。漏掉它,所有非顶层帧的行号会偏到「下一条指令的行」
> (常常是错误的行,甚至下一个语句)。Lua 5.1 `lua_getinfo`/`currentline` 内部就是 `pc - 1`(`savedpc - 1`)。
> **由 [12](./12-testing-difftest.md) 用多行脚本的 traceback 钉死行号逐字节一致**。

---

## 4. `assert(v, message, ...)` —— 裸 message,不加位置

### 4.1 Lua 5.1 `assert` 语义

`assert(v [, message, ...])` 是 base 库 host function:

```
assert(v, message, ...):
  if truthy(v):                       -- [01] §6:仅 nil/false 为假
    return v, message, ...            -- 真值:【原样返回所有参数】(含 v 自身与后续)
  -- v 为假:报错
  if message == nil:
    raise("assertion failed!")        -- 默认信息(注意感叹号,无位置前缀!)
  else:
    raise(message)                    -- 用用户的 message【原样】,【不加位置前缀】
```

**关键 5.1 口径(易错)**:

- **真值时返回**所有参数(`assert(io.open(f))` 惯用法:成功则透传 file handle,失败则抛第二个返回值作 message)。
  返回**全部**实参,不只 `v`(`local a, b = assert(f())` 能拿到 f 的多返回值)。
- **`assert` 抛的是裸 message,不加 `<source>:<line>:` 前缀**。这是 5.1 行为:`assert` **不**经 `error` 的 level
  机制——它直接 `raise(message)`。所以 `assert(false, "boom")` 抛的错误值是 `"boom"`(不带位置),而
  `error("boom")` 抛 `"foo.lua:N: boom"`(带位置)。**这个差异必须实现对**:`assert` 与 `error` 走不同路径,
  `assert` 跳过 `where()` 前缀。
- **默认 message 是 `"assertion failed!"`**(含感叹号,无位置前缀)。**待 12 差分核对**精确标点(感叹号、无尾随空格)。
- **message 可为任意类型**:`assert(false, {code=1})` 抛 table(同 `error` 的非 string 处理,不加前缀本就因为非 string)。

### 4.2 `assert` 实现要点

```go
// base 库 host function(签名见 05 §7.6 HostFn / [10] 调用约定)。
func hostAssert(vm *VM, th *Thread) int {
    v := th.arg(1)
    if value.Truthy(v) {
        return th.returnAllArgs()          // 真值:返回全部参数(nargs 个),不裁剪
    }
    msg := th.arg(2)
    if msg == value.Nil {
        msg = vm.internString("assertion failed!")  // 默认,无位置前缀
    }
    // 直接 raise(msg)【裸值,不经 where()】—— 这是 assert 与 error 的关键分野
    vm.raise(msg)                          // §3.3:转入 execute 冒泡
    return 0                               // 不可达(raise 后 callHost 走 callError 路径)
}
```

- **不加位置**:`hostAssert` **不调 `where()`**。即使 message 是 string,也原样 `raise`。这与 `error(msg, 1)`
  形成对比(后者加前缀)。差分测试覆盖「`assert` vs `error` 的位置前缀差异」。
- **返回全部参数**:`returnAllArgs` 把 `[base+1, top)` 全部作为返回值(不按某固定 nresults 裁剪;由调用点的
  CALL 的 C 决定实际接收几个,05 §7.2 `moveResults`)。

---

## 5. `pcall(f, args...)` —— 保护调用边界(展开 05 §9.3 骨架)

### 5.1 Lua 5.1 `pcall` 语义

`pcall(f, arg1, ...)` **在保护模式下调用 `f`**,捕获其中任何错误:

```
pcall(f, args...):
  成功(f 正常返回):  return true, (f 的所有返回值)
  出错(f 内任意层 error / 内在错误):  return false, errval
```

- 第一返回值是**布尔**:`true`=无错,`false`=有错。
- 成功时后随 `f` 的**全部**返回值;出错时后随**错误对象**(`LuaError.value`,通常 string)。
- **`pcall` 捕获一切**:`f` 内任意深度的 `error`、解释器内在错误、被调函数的错误,都被这层 `pcall` 拦下
  (除非更内层有另一个 `pcall` 先拦)。

### 5.2 `pcall` 实现:展开 05 §9.3 骨架到可实现

05 §9.3 给了 pcall 的伪码骨架,本节展开每一步的栈/CallInfo 操作:

```go
// base 库 host function。pcall 是 protected 边界(05 §7.3 fresh 帧 + §9.3 捕获)。
func hostPcall(vm *VM, th *Thread) int {
    f := th.arg(1)                              // 被保护函数
    nargs := th.nargs() - 1                     // 其余参数 args...(arg(2..))

    // —— ① 记录保护点(用于出错时回退)——
    savedCiTop := th.ciTop                       // CallInfo 保护点
    savedTop   := th.top                         // 值栈 top 保护点
    savedNCcalls := vm.nCcalls                   // C 栈深度保护点(05 §7.4)

    // —— ② 在保护模式下调用 f ——
    //   callLuaFromHost 压一个 fresh CallInfo(05 §7.3,标 callStatus_fresh),
    //   并在 word3 记 errfuncBase=0(pcall 无 message handler,05 §1.2);execute 跑 f。
    results, lerr := vm.callLuaFromHostProtected(th, f, th.argsFrom(2), nargs)

    if lerr == nil {
        // —— ③a 成功:返回 true + f 的所有返回值 ——
        th.pushBool(true)                        // 第一返回值
        th.pushResults(results)                  // 后随 f 的返回值(已在栈上,调整位置)
        return 1 + len(results)
    }

    // —— ③b 出错:捕获 *LuaError,清理到保护点 ——
    vm.recoverToProtectionPoint(th, savedCiTop, savedTop, savedNCcalls)  // §5.3
    th.pushBool(false)                           // 第一返回值
    th.push(lerr.value)                          // 第二返回值 = 错误对象(LuaError.value)
    return 2
}
```

### 5.3 `recoverToProtectionPoint` —— 边界的清理职责(05 §9.3 的核心)

错误冒泡时**出错帧不清理**(§2.2),清理全压在 protected 边界。`recoverToProtectionPoint` 是这个一次性清理:

```go
// 把 Thread 从「错误冒泡后的脏状态」恢复到保护点的干净状态。承 05 §9.3。
func (vm *VM) recoverToProtectionPoint(th *Thread, ciTop, top int32, nCcalls int) {
    // ① 关闭保护点之上所有开放 upvalue(被丢弃帧可能有捕获局部的闭包,05 §8.3)
    //    level = 保护点帧的 base(ciTop 对应帧之上的栈都将废弃)
    vm.closeUpvals(th, th.ciAt(ciTop).base)
    // ② 回退 CallInfo 栈:丢弃出错帧及其上所有 Lua 帧(批量,O(丢弃数))
    th.ciTop = ciTop
    // ③ 恢复值栈 top(被丢弃帧的临时值作废)
    th.top = top
    // ④ 恢复 C 栈深度计数(05 §7.4:出错路径上可能 callLuaFromHost 过几层未配平)
    vm.nCcalls = nCcalls
    // ⑤ 清 pending 错误(已被本边界消费)
    vm.pendingErr = nil
}
```

**为什么这是显式返回相对 panic 的关键简化**(再强调,05 §9.3):清理是**批量的、声明式的**——「把 ciTop/top
拨回保护点」一步丢弃一摞帧,而非 panic 模型里每帧 defer 一个 cleanup。**O(被丢弃帧数) 的指针拨动,而非
O(帧数) 的 defer 链展开**。upvalue 关闭也是一次 `closeUpvals(level)` 扫降序链(05 §8.3),不逐帧。

### 5.4 `pcall` 边界与 §7.3 fresh 帧的关系

`pcall` 必然 host→Lua 重入(host 的 pcall 回调 Lua 的 `f`),所以它压一个 **`callStatus_fresh` 的 CallInfo**
(05 §7.3),并起一个**新的 `execute` Go 栈帧**(05 §7.3:只有 host→Lua 才加 Go 栈)。这个 fresh 帧就是
**错误冒泡的停靠站**:`f` 内的错误一路 `return` 出 `f` 的 `execute`,该 `execute` 返回到 `callLuaFromHostProtected`,
后者把 `*LuaError` 交给 `hostPcall` 的 `lerr` 检查。**所以「错误在哪停」= 「哪个 host 起了 execute 并检查返回值」**
(05 §9.4)。`pcall` 是这种 host 的典范;任何 host 若调 `callLuaFromHost` 且检查返回值,都能成为保护点。

> **C 栈深度也是 pcall 必须存恢复的**(05 §7.4 `nCcalls`):`f` 内若有 host↔Lua 交替重入,出错时这些层的
> `nCcalls` 加减可能未配平(错误跳过了正常 return 的 -1)。`recoverToProtectionPoint` 第 ④ 步把 `nCcalls`
> 拨回保护点值,否则连续 `pcall` 失败会让 `nCcalls` 虚高、误报 `"C stack overflow"`。

---

## 6. `xpcall(f, handler, args...)` —— 栈展开前调 handler

### 6.1 Lua 5.1 签名与口径(显式标注 5.1 vs 5.2+)

`xpcall(f, handler)` 与 `pcall` 类似,但**出错时先用 `handler`(message handler)处理错误对象**,再返回。

```
xpcall(f, handler):              -- Lua 5.1 签名
  成功:  return true, (f 的返回值)
  出错:  return false, handler(errval)   -- handler 在【栈展开前】被调用,见 §6.2
```

**关键 5.1 口径(版本差异,必须标注)**:

| 维度 | Lua 5.1 | Lua 5.2+ | P1 采用 |
|---|---|---|---|
| 是否传 args 给 f | **不传**。5.1 `xpcall(f, handler)` **只接 f 与 handler 两参**,**不能**给 f 传额外参数 | 5.2+ 起 `xpcall(f, handler, arg1, ...)` 支持给 f 传参 | **5.1:不传 args 给 f** |
| handler 调用时机 | 错误点(栈展开前) | 同 | 同 |
| handler 返回值 | 作为 `xpcall` 的第二返回值 | 同 | 同 |

> **P1 锁 5.1:`xpcall` 第三参起的 args 不传给 f**。若脚本写 `xpcall(f, h, 1, 2)`,5.1 里 `1, 2` 被**忽略**
> (f 收不到)。本文按 5.1 实现(忽略额外参数);若宿主生态需要 5.2 行为,记 doc-gap(§14)但**默认 5.1**。
> 任务口径明确:**以 5.1 为准并记口径**。这是与 `pcall`(任意版本都传 args)的关键差异。

### 6.2 机制:handler 在错误点的栈上下文调用(展开前!)

**这是 xpcall 的精髓,也是它与 pcall 实现上最大的不同**:

- `pcall` 在**栈展开后**(清理到保护点后)才有错误对象可处理——但它不调任何 handler,直接返回错误。
- `xpcall` 的 handler(通常是 `debug.traceback`)**需要访问出错现场的完整调用栈**来生成 traceback。如果先
  展开栈(回退 ciTop)再调 handler,**出错帧的 CallInfo 已经没了**,traceback 就拿不到完整栈了。所以
  **handler 必须在「捕获错误后、清理 CallInfo 前」被调用**——此时出错点的整条 CallInfo 链还完整挂在 Thread 上。

```
xpcall 出错时的精确时序(定稿):
  1. f 内某层抛错 → *LuaError 一路 return 到 xpcall 起的 execute → 返回 lerr。
     ★ 此刻 CallInfo 链【尚未清理】:出错帧、中间帧全部还在(ciTop 仍指向出错点)。
  2. 在【未清理】的栈上调用 handler(lerr.value):
       - handler 是 Lua closure → callLuaFromHost reentry 子循环(在出错栈之上压新帧);
       - handler 是 host(如 debug.traceback)→ 同步 Go 调用。
       handler 此时能通过 debug.traceback / debug.getinfo 遍历【完整的出错 CallInfo 链】(§7)。
  3. handler 返回值 hres(通常是带 traceback 的字符串)。
  4. 【现在】才清理:recoverToProtectionPoint(回退 ciTop / top / nCcalls + closeUpvals)。
  5. return false, hres。
```

**05 §9.3 行 880 把这个时序的定稿留给本文**:「元方法/错误处理器(error 的 message handler、xpcall 的
handler):在边界捕获后、返回前调用 handler(可能再 reentry execute)」——本文定稿为「**捕获后、清理前**,
在未展开的出错栈上调用」,这是 traceback 能拿到完整栈的**充要条件**。

### 6.3 `xpcall` 实现

```go
func hostXpcall(vm *VM, th *Thread) int {
    f := th.arg(1)
    handler := th.arg(2)                         // message handler
    // 5.1:不取 arg(3..) 作 f 的参数(忽略;见 §6.1 口径)

    savedCiTop := th.ciTop
    savedTop   := th.top
    savedNCcalls := vm.nCcalls

    // —— ① 把 handler 记到保护点的 errfuncBase(05 §1.2 CallInfo word3)——
    //   这样若 f 内错误经过本 fresh 帧,边界知道「有 message handler 要调」。
    //   callLuaFromHostProtected 把 handler 的栈位写入 fresh CallInfo.word3(errfuncBase)。
    results, lerr := vm.callLuaFromHostWithHandler(th, f, /*noArgs*/, handler)

    if lerr == nil {
        th.pushBool(true)
        th.pushResults(results)
        return 1 + len(results)
    }

    // —— ② 出错:【在未清理的出错栈上】调 handler ——(§6.2 时序第 2 步)
    //   注意:此时【还没】recoverToProtectionPoint!CallInfo 链完整。
    hres, herr := vm.callHandlerOnErrorStack(th, handler, lerr.value)   // §6.4
    var second value.Value
    if herr != nil {
        // handler 自己又出错了(§6.5):5.1 行为 —— 用一个特殊信息,不再递归调 handler
        second = vm.internString("error in error handling")  // 待 12 核对措辞
    } else {
        second = hres                            // handler 的返回值(通常带 traceback)
    }

    // —— ③ 现在才清理 ——(§6.2 时序第 4 步)
    vm.recoverToProtectionPoint(th, savedCiTop, savedTop, savedNCcalls)
    th.pushBool(false)
    th.push(second)
    return 2
}
```

### 6.4 `callHandlerOnErrorStack` —— 在未展开栈上调 handler 的纪律

```go
// 在【出错 CallInfo 链尚未清理】的状态下调用 message handler。
// handler 收到错误对象,返回处理后的值(通常 debug.traceback 拼好的字符串)。
func (vm *VM) callHandlerOnErrorStack(th *Thread, handler, errval value.Value) (value.Value, *LuaError) {
    // ① errval 作为 GC 根临时登记(§1.3:handler 会分配 → 可能 GC,errval 不能被回收)
    vm.pushShadowRoot(errval)
    defer vm.popShadowRoot()
    // ② handler 在出错栈【之上】压临时帧调用(不动出错帧,它们还要供 handler 的 traceback 遍历)
    //    handler 是 debug.traceback → 它遍历 th 的【完整 CallInfo 链】(含出错帧)生成回溯。
    return vm.callValueReturning1(th, handler, []value.Value{errval})
}
```

**纪律要点**:

- **不动出错帧**:handler 在出错栈**之上**(当前 top 之上)压临时帧执行;出错的 CallInfo 链原封不动挂着,
  供 `debug.traceback`/`debug.getinfo` 遍历(§7/§13)。这正是「展开前调」的物理实现。
- **errval 登记 GC 根**:handler 必然分配(拼字符串),触发 GC;`errval` 此刻只被 `LuaError.value` 这个 Go
  指针引用(不在任何 Lua 栈槽里),GC 看不到 → 必须显式压 shadow root(§1.3)。
- **handler 可能 reentry**:handler 是 Lua closure 时走 reentry 子循环(05 §7.1),在出错栈之上跑。这层
  reentry 也加 `nCcalls`(05 §7.4),受 C stack overflow 上限保护(防 handler 无限递归)。

### 6.5 handler 自身出错:`"error in error handling"`

若 message handler **自己也抛错**(罕见但可能:handler 有 bug),Lua 5.1 **不再递归调用 handler**(否则可能
无限),而是用一个固定错误信息 `"error in error handling"` 作为 `xpcall` 的第二返回值(`herr` 分支)。
**待 12 差分核对**精确措辞(5.1 的 `luaD_throw(LUA_ERRERR)` 对应信息)。这是「错误处理的错误」的兜底,
保证 xpcall 永远能返回(不会因 handler 出错而把错误继续冒泡出 xpcall)。

---

## 7. 栈回溯 traceback

### 7.1 traceback 格式(Lua 5.1 `luaL_traceback` 等价)

traceback 是一个**多行字符串**,描述错误发生时的调用栈。格式(P1 定稿,**待 12 差分核对**精确空白/标点):

```
stack traceback:
	<location>: in <what>
	<location>: in <what>
	...
	(...tail calls...)        ← 尾调用帧(05 §7.2 ci.tailcall)
	<location>: in <what>
```

逐部分定义:

- **首行**:固定 `"stack traceback:"`(无缩进)。
- **每帧一行**:`"\t" + <location> + ": in " + <what>`(**前导一个 tab**)。
- **`<location>`**(帧位置):
  - Lua 帧:`<source>:<line>`(source 经 `chunkID` §3.4,line 经 pc→line §3.5 含 -1 偏移)。例 `foo.lua:12`。
  - host(C)帧:`[C]`(无行号)。
- **`<what>`**(帧是什么函数):
  | what 形态 | 含义 | 何时 |
  |---|---|---|
  | `function '<name>'` | 有推断出名字的函数 | 函数名推断成功(§8),如 `function 'print'` |
  | `function <source:line>` | 匿名/无名函数,用定义位置标识 | 推断不出名字,用 Proto 定义处 |
  | `main chunk` | 主 chunk(顶层) | 该帧是脚本顶层 |
  | `?` | 完全无法标识(罕见) | 兜底 |
- **尾调用标记**:若某帧是尾调用产生(`ci.tailcall==true`,05 §7.2/§7.5),在该帧位置**追加一行**
  `"\t(...tail calls...)"`(表示这里省略了被尾调用复用掉的帧——尾调用复用帧使中间帧不在栈上)。

### 7.2 traceback 生成伪码(遍历 CallInfo 链)

```go
// 生成 th 当前调用栈的 traceback 字符串。从栈顶帧向栈底遍历 CallInfo 链。
// level1/levelN:Lua 5.1 traceback 可指定起止层(debug.traceback(msg, level));P1 默认全栈。
func (vm *VM) traceback(th *Thread, msg string, startLevel int) string {
    var b strings.Builder
    if msg != "" {
        b.WriteString(msg)        // debug.traceback(msg) 把 msg 放在 traceback 前(§13)
        b.WriteByte('\n')
    }
    b.WriteString("stack traceback:")
    for ci := th.ciTop - 1 - startLevel; ci >= th.ciBase; ci-- {
        c := th.ciAt(ci)
        b.WriteString("\n\t")
        // —— location ——
        if c.isHostFrame() {
            b.WriteString("[C]")
        } else {
            proto := vm.protos[c.protoID]
            line := proto.LineInfo[currentPCOf(c)]    // §3.5:顶帧 pc-1 / 非顶帧 savedPC-1
            b.WriteString(fmt.Sprintf("%s:%d", chunkID(vm.sourceShort(proto.Source)), line))
        }
        b.WriteString(": in ")
        // —— what:函数名推断 ——
        b.WriteString(vm.funcWhat(th, ci))            // §8:推断 function 'name' / main chunk / ...
        // —— 尾调用标记 ——
        if c.tailcall {                               // 05 §7.2 word2 bit48
            b.WriteString("\n\t(...tail calls...)")
        }
    }
    return b.String()
}
```

### 7.3 traceback 何时生成(性能纪律)

**traceback 不在抛出点生成**(§1.2),只在三处:

1. **`xpcall` 的 handler = `debug.traceback`**:最常见。handler 在出错栈上(展开前,§6.2)调
   `debug.traceback`,后者调本节 `traceback(th, ...)` 遍历完整出错栈。这是 traceback 拿到完整栈的正路。
2. **顶层未捕获错误**:错误传到 `Program.Call`(§11),若宿主配置「附带 traceback」,在转 Go error 前生成。
3. **`debug.traceback(msg, level)` 被显式调用**(§13):脚本主动要当前栈回溯(不一定在错误时)。

**为什么不在抛出点生成**:① 多数错误被 `pcall` 静默捕获后丢弃(不看 traceback),抛出点生成是白做功;
② 生成 traceback 要分配字符串、遍历栈,在错误路径上(虽冷)也无谓;③ `pcall`(无 handler)根本不需要
traceback,只要错误值。**所以 traceback 是「按需生成」**——谁要谁调,默认不生成(`LuaError.traceback` 默认空)。

### 7.4 行号正确性:-1 偏移贯穿 traceback

traceback 每帧的 `<line>` 都经 §3.5 的「当前 pc」取值:**栈顶帧 `pc-1`,非栈顶帧 `savedPC-1`**。这是
traceback 行号与官方逐字节一致的命脉(§3.5 已强调)。**特别注意**:traceback 遍历的帧大多是**非栈顶帧**
(它们都已发出调用在等返回),所以**几乎所有帧都用 `savedPC-1`**。漏掉 -1 会让整个 traceback 的行号系统性
偏移。由 [12](./12-testing-difftest.md) 用「深调用链 + 错误」的脚本钉死每一行行号。

---

## 8. 函数名推断(getfuncname / getobjname)

### 8.1 机制:从调用点指令逆推被调函数名

traceback 的 `function '<name>'` 里的 `<name>` **不是函数自带的属性**(Lua 函数是匿名值,没有「名字」)。
Lua 5.1 用一个巧妙机制推断:**看调用者帧在「发出调用的那条指令」处,逆推被调函数的 `R(A)` 是从哪来的**
(`getfuncname` → `getobjname` → 字节码符号执行)。

```
推断帧 ci 的函数名(getfuncname):
  callerCi := ci 的调用者帧(ci 在 callerCi 里被调用)
  if callerCi 是 host 帧:  return 无名(host 调 Lua,无字节码可逆推)
  callPC := callerCi.savedPC - 1        -- 调用者「发出调用的那条指令」(§3.5)
  inst := callerCi.proto.Code[callPC]
  switch Op(inst):
    case CALL, TAILCALL:                 -- 普通调用:逆推 R(A) 的来源
        return getobjname(callerCi.proto, callPC, A(inst))
    case TFORLOOP:                       -- 泛型 for 的迭代器
        return "for iterator", kindForIterator
    case SELF:                           -- 方法调用 obj:m()
        return getobjname(callerCi.proto, callPC, A(inst)), kindMethod
    -- 元方法触发的调用(算术/index/...)无显式 CALL 指令:
    default:                             -- 经元方法进入(__index/__add/...)
        return 元方法事件名(如 "index"/"add"), kindMetamethod
```

**`getobjname(proto, pc, reg)`** 是核心:它**逆向符号执行**——从 `pc` 往前找「最后一条写 `reg` 的指令」,
按那条指令的种类判定 `reg` 里的值「叫什么名字」:

| 写 reg 的指令 | reg 的来源 | 推断出的名字 + 种类 | 错误后缀形态 |
|---|---|---|---|
| `GETGLOBAL R(A) K(Bx)` | 全局变量 | global,名 = `K(Bx)` 字符串常量 | `(global 'foo')` |
| `GETTABLE`/`GETFIELD` R(A) ... RK(C) | 表字段(C 是字符串常量键) | field,名 = `K(C)` | `(field 'bar')` |
| `GETUPVAL R(A) B` | upvalue | upvalue,名 = `Proto.UpvalNames[B]`(★需回填,§8.4) | `(upvalue 'z')` |
| `SELF R(A) ... RK(C)` | 方法 | method,名 = `K(C)` | `(method 'm')` |
| `MOVE R(A) R(B)` | 局部变量(从另一寄存器) | local,名 = 查局部变量表 `LocVars`(★需回填) | `(local 'x')` |
| 是某局部变量寄存器 | 局部变量 | local,名 = `LocVars` 按 pc 活跃区间查 | `(local 'x')` |
| `LOADK`/算术结果/... | 临时值,无名 | `?` 或无后缀 | (无变量名后缀) |

### 8.2 这给出错误信息里的变量名后缀(承 07 §14.4)

07 §14.4 把「变量名增强」明确下放给本文。**这是同一套 `getobjname` 机制的另一个出口**:不仅 traceback 的
`function 'name'` 用它,**运行期错误信息的变量名后缀也用它**。例:

```
attempt to call a nil value (global 'foo')        ← foo() 中 foo 是未定义全局
attempt to index a nil value (field 'bar')         ← t.bar.x 中 t.bar 是 nil
attempt to perform arithmetic on a nil value (local 'n')  ← n+1 中 n 是 nil 局部
attempt to call a nil value (method 'm')           ← obj:m() 中 m 不存在
```

**机制**:错误在某 opcode(如 CALL/GETTABLE/ADD)处产生时,**当前帧的当前 pc** 就是「肇事指令」。对它的
**肇事操作数寄存器**(CALL 的 R(A)、GETTABLE 的 R(B)、ADD 的非数字那个 RK)调 `getobjname(proto, pc, reg)`,
得到 `(global 'x')` / `(field 'y')` / `(local 'z')` / `(method 'm')` / `(upvalue 'w')` 后缀,**拼到 07 给的
类型名措辞之后**:

```go
// 07 的 *Error helper(arithError/indexError/callError/...)在本文增强为带变量名。
// 07 给「类型名层」:vm.arithError(f, bad) → "attempt to perform arithmetic on a <type> value"
// 本文增强:在构造时附加位置前缀(§3)+ 变量名后缀(本节)。
func (vm *VM) describeOperand(f *frame, reg int) string {
    name, kind := vm.getobjname(f.proto, f.pc-1, reg)   // 肇事指令 pc-1,肇事寄存器 reg
    if name == "" {
        return ""                                        // 推断不出 → 无后缀
    }
    switch kind {
    case kindGlobal:    return fmt.Sprintf(" (global '%s')", name)
    case kindLocal:     return fmt.Sprintf(" (local '%s')", name)
    case kindField:     return fmt.Sprintf(" (field '%s')", name)
    case kindMethod:    return fmt.Sprintf(" (method '%s')", name)
    case kindUpvalue:   return fmt.Sprintf(" (upvalue '%s')", name)
    default:            return ""
    }
}
```

**与 07 的分工定稿**:07 负责「`attempt to X a <type> value`」的**类型名层**(§9.1 目录);本文负责
**位置前缀(`<source>:<line>: ` 前)+ 变量名后缀(`(global 'x')` 后)**。完整错误信息 =
`<source>:<line>: attempt to X a <type> value (<kind> '<name>')`。

### 8.3 P1 简化范围(完整符号推断复杂,标缺口)

**完整 `getobjname` 是 Lua 5.1 调试信息里最复杂的一块**(`ldebug.c` 的 `symbexec` 做完整字节码符号执行,
处理跳转、寄存器复用、活跃区间等)。**P1 做简化版**:

| 推断种类 | P1 支持? | 依赖 | 说明 |
|---|---|---|---|
| `global` | **✅ 必做** | `GETGLOBAL` 的 K(Bx) | 最常见(`foo()` 报 `(global 'foo')`),逆推一条指令即可,无需符号执行 |
| `field` | **✅ 必做** | `GETTABLE`/`SELF` 的 RK(C) 是字符串常量 | 常见(`t.x()`),逆推一条指令 |
| `method` | **✅ 必做** | `SELF` 的 RK(C) | `obj:m()`,逆推一条指令 |
| `local` | **✅ 应做** | `LocVars` 局部变量名表(★需回填,§8.4) | 需局部变量调试表;逆推「最后写该 reg 的指令」 |
| `upvalue` | **△ 可选** | `UpvalNames` upvalue 名表(★需回填) | 需 upvalue 名表;P1 可缺(显示无后缀) |
| `for iterator` | **△ 可选** | TFORLOOP 识别 | traceback 的 `for iterator` what;P1 可简化为 `?` |
| 元方法名(`metamethod`) | **△ 可选** | 识别经元方法进入的帧 | traceback 里 `metamethod 'index'`;P1 可简化 |
| 完整 `symbexec`(跨跳转的寄存器追踪) | **❌ 不做** | —— | 完整符号执行处理控制流合并;P1 只做「线性逆推最近写指令」,遇跳转放弃(返回无名) |

**P1 落地策略**:`getobjname` **只逆推「肇事 pc 往前最近一条无条件写该寄存器的指令」**(不跨基本块、不处理
跳转合并)。这覆盖绝大多数真实错误场景(`foo()`、`t.x`、`n+1` 都是肇事指令的直接前驱写)。复杂场景(寄存器
被多路径写、跳转后)P1 返回无名(错误信息无变量名后缀,但**类型名层仍完整**——退化优雅,不影响正确性,只
少一个括号提示)。**与官方 5.1 的差分**:P1 简化可能在「复杂控制流后的错误」上少变量名后缀,**记为已知差异**
(差分基准对「变量名后缀」做宽松匹配,或标注豁免,见 §14)。

### 8.4 对 01 §5.7 的回填请求(强化 04 §13)——LocVars / UpvalNames

**`local` 与 `upvalue` 名字推断需要 Proto 持久化局部变量名表与 upvalue 名表,但 [01](./01-value-object-model.md)
§5.7 当前 Proto struct 只有 `LineInfo`,没有 `LocVars`/`UpvalNames`**:

```go
// [01] §5.7 当前 Proto(节选):
type Proto struct {
    // ...
    LineInfo   []int32       // ✅ 有:每指令源行(pc→line,§3.5)
    Source     GCRef         // ✅ 有:源名(chunkID,§3.4)
    UpvalDescs []UpvalDesc   // ✅ 有:upvalue 来源(但 04 §8.3 的 name 字段未必持久化到此)
    // ❌ 缺:LocVars  []LocalVar  —— 局部变量名 + 活跃区间(getobjname 的 local 推断需要)
    // ❌ 缺:UpvalNames []GCRef   —— upvalue 名(getobjname 的 upvalue 推断需要;或复用 UpvalDescs.name)
}
```

**回填请求(本文强化,04 §13 已提同一缺口)——已兑现**:[01](./01-value-object-model.md) §5.7 已增补:

1. **`LocVars []LocalVar`**:✅ 已落入 01 §5.7(`{Name string; StartPC, EndPC int32}`,Go 堆调试数据;Name 为 Go string 而非 GCRef,不入 arena 不参与 GC)。来源即 04 §5.9 的 `funcState.locvars`,codegen 期持久化。`getobjname` 的 local 分支按「pc 落在哪个 LocVar 的 `[StartPC,EndPC)` 且对应该寄存器」查名。
2. **upvalue 名**:✅ 01 §5.7 定为**复用 `UpvalDescs.name`**(04 §8.3 的 `upvalDesc.name` 持久化到 Proto.UpvalDescs),不单列 `UpvalNames`。

> 回填后 §8.3 的 `local`/`upvalue` 推断**可实现**;`global`/`field`/`method` 本就不依赖它们(从常量池 K 取名)。

---

## 9. 运行期错误信息目录(解释器内在错误措辞)

### 9.1 内在错误措辞总表(单一事实源,与 05/07 对齐)

下表是**解释器内在错误**(非 `error()`/`assert()` 抛的,而是 VM 执行非法操作时构造的)的**完整措辞目录**。
所有这些错误的 `value` 是 string,**带位置前缀**(§3.2:`<source>:<line>: ` 在前)**与变量名后缀**(§8.2:
`(<kind> '<name>')` 在后,若推断得出)。措辞主体与 [07](./07-metatables-metamethods.md) §14.4 / [05](./05-interpreter-loop.md)
对齐:

| # | 触发场景 | 措辞主体(类型名层) | 来源 opcode/操作 | 定义文档 | 变量名后缀 |
|---|---|---|---|---|---|
| 1 | 索引非 table 且无 `__index` | `attempt to index a <type> value` | GETTABLE/GETGLOBAL/SELF | 07 §3 `indexError` | ✅ `(global/field/local/upvalue 'x')` |
| 2 | 写索引非 table 且无 `__newindex` | `attempt to index a <type> value` | SETTABLE/SETGLOBAL | 07 §4 | ✅ 同上 |
| 3 | 调用不可调用值且无 `__call` | `attempt to call a <type> value` | CALL/TAILCALL | 07 §10 `callError` | ✅ `(global/field/method/local/upvalue 'f')` |
| 4 | 算术于非数字(且不可 coerce)且无算术元方法 | `attempt to perform arithmetic on a <type> value` | ADD/SUB/MUL/DIV/MOD/POW/UNM | 07 §5/§6 `arithError` | ✅ |
| 5 | 连接非 string/number 且无 `__concat` | `attempt to concatenate a <type> value` | CONCAT | 07 §8 `concatError` | ✅ |
| 6 | 取长度于非法类型且无 `__len` | `attempt to get length of a <type> value` | LEN | 07 §7 `lenError` | ✅ |
| 7 | 比较两个同类型值(无元方法) | `attempt to compare two <type> values` | EQ/LT/LE | 07 §9 `compareError` | ✗(一般无) |
| 8 | 比较不同类型值(无元方法) | `attempt to compare <type-a> with <type-b>` | LT/LE | 07 §9 | ✗ |
| 9 | table 索引为 nil(写) | `table index is nil` | SETTABLE/SETLIST/rawset | 07 §4.3 / 01 §5.2 | ✗ |
| 10 | table 索引为 NaN(写) | `table index is NaN` | 同上 | 07 §4.3 / 01 §5.2 | ✗ |
| 11 | 数值 for 初值非数字 | `'for' initial value must be a number` | FORPREP | 05 §10.1 | ✗ |
| 12 | 数值 for 限值非数字 | `'for' limit must be a number` | FORPREP | 05 §10.1 | ✗ |
| 13 | 数值 for 步长非数字 | `'for' step must be a number` | FORPREP | 05 §10.1 | ✗ |
| 14 | Lua 调用深度超限 | `stack overflow` | enterLuaFrame / CALL | 05 §7.4 §1.4 | ✗ |
| 15 | host↔Lua 重入超限 | `C stack overflow` | callLuaFromHost | 05 §7.4 | ✗ |
| 16 | `__index`/`__newindex`/`__call` 链过长 | `'__index' chain too long; possible loop` 等 | 元方法链 | 07 §3.3(MAXTAGLOOP=100) | ✗ |
| 17 | `string.format`/`unpack` 等参数越界 | `'n' too large` / `bad argument #n to '<fn>' (...)` | stdlib host | 10 | ✗(host 自构) |
| 18 | `__tostring` 返回非 string | `'__tostring' must return a string` | tostring | 07 §11 | ✗ |

### 9.2 类型名(`<type>`)取值

`<type>` 是 Lua `type()` 的返回值([01](./01-value-object-model.md) §3.3 表):`"nil"`/`"boolean"`/`"number"`/
`"string"`/`"table"`/`"function"`/`"userdata"`/`"thread"`。**注意 lightuserdata 与 full userdata 都报
`"userdata"`**(01 §3.3)。错误里的类型名取**肇事操作数**的类型(07 §5.1:取第一个非数字操作数等规则)。

### 9.3 冠词与精确标点(待 12 差分核对)

**错误措辞里的冠词/复数/标点必须与 Lua 5.1 参考实现逐字节一致,本文给骨架不编造**:

- **冠词 `a`/`an`**:Lua 5.1 一律用 `a`(`attempt to index a nil value`,**不是** `an nil`——5.1 不做元音判断)。
  **待 12 核对**:5.1 是否对 `userdata` 等用 `a`(应是)。
- **复数**:`attempt to compare two table values`(`values` 复数,`two` 后)。
- **标点**:措辞内无尾随句号;位置前缀 `: `(冒号空格);变量名后缀 ` (kind 'name')`(前导空格 + 括号 + 单引号)。
- **`bad argument` 系列**(stdlib host,#17):`bad argument #<n> to '<fnname>' (<reason>)`,如
  `bad argument #1 to 'setmetatable' (table expected, got number)`(07 §1.1 已用此格式)。`<reason>` 形态多样,
  由各 host 构造([10](./10-stdlib.md))。

> **所有精确措辞标 `待 12 差分核对`**:本目录给「说什么」(语义)与「骨架措辞」,精确到字符的标点/冠词/
> 单复数由 [12](./12-testing-difftest.md) 拿官方 Lua 5.1 当 oracle 钉死。**不编造**(roadmap §5 原则 2:与官方
> 5.1 逐字节一致)。措辞主体(`attempt to index a <type> value` 等)是 Lua 5.1 稳定文案,可信;边角标点存疑标注。

---

## 10. stack overflow vs C stack overflow(承 05 §7.4)

两类「栈溢出」错误**语义不同、触发点不同、措辞不同**,必须分清(05 §7.4 定上限,本文定措辞与语义):

| 维度 | `stack overflow` | `C stack overflow` |
|---|---|---|
| 含义 | **Lua 调用深度**超限(CallInfo 数 / 值栈深度) | **host↔Lua 重入深度**超限(真 Go 栈消耗) |
| 触发点 | `enterLuaFrame` / `ensureStack`(05 §1.4) 超 `ciCap`/栈上限 | `callLuaFromHost` 时 `nCcalls` 超 `LUAI_MAXCCALLS`(=200,05 §7.4) |
| 物理资源 | arena 内的 CallInfo 数组 / 值栈(**不是 Go 栈**) | 真 Go 栈(host→Lua 每层加一个 execute Go 帧,05 §7.3) |
| 典型触发 | `local function f() return 1+f() end f()`(深 Lua 递归,非尾) | `pcall` 套 `pcall` 套...无限,或元方法无限互调经 host | 
| 可恢复? | ✅ 可被 `pcall` 捕获(它是普通 Lua 错误) | ✅ 可被 `pcall` 捕获,但**保护边界自身也在消耗 C 栈**(见下) |
| 措辞 | `stack overflow` | `C stack overflow` |

**关键区别(为什么要两个上限)**:

- **Lua 调用深度不吃 Go 栈**(05 §7.1:Lua-call-Lua 是 reentry,Go 栈深恒为 1)。所以 1000 层 Lua 递归
  只是 1000 条 CallInfo(arena),不会爆 Go 栈——它由 `stack overflow`(arena 逻辑上限)拦截。
- **host↔Lua 重入吃 Go 栈**(05 §7.3:只有 host→Lua 才加 execute Go 帧)。`pcall`/`coroutine`/元方法是
  host 的反复 Lua 重入会真涨 Go 栈。Go 栈虽可增长但有 `maxstacksize`(~1GB)硬上限,**撞上是 fatal 不可恢复**。
  所以必须在它之前用 `nCcalls`(=200)把 `C stack overflow` 作为**可恢复**错误拦下(05 §7.4)。

> **`C stack overflow` 的微妙处**:它触发时,`pcall` 想捕获——但 `pcall` 捕获也要起一层 execute(消耗 C 栈)。
> Lua 5.1 给 `pcall`/错误处理**保留一小段 C 栈余量**(`LUAI_MAXCCALLS` 之上还有约 200 的 buffer),让错误能被
> 处理而不立即再次溢出。P1 应同样保留余量(`nCcalls` 超 200 报错,但允许错误处理路径短暂超到 ~220)。
> **待 12 核对**精确余量值。记 doc-gap(§14)。

---

## 11. host panic 兜底(定稿 05 §9.4 的安全网)

### 11.1 语义:顶层 recover,转 Lua 错误 + 标 Thread 损坏

05 §9.4 定下:**host function 绝不该 Go panic**(panic 绕过 CallInfo 清理,破坏 §2.2 的边界清理契约)。
host 抛错走 `raise`(§3.3)。但**若 host 代码有 bug 真的 Go panic 了**(数组越界、nil 解引用等 Go 运行期
panic),需要一个**最外层兜底**防止整个 Go 进程崩溃——这是**安全网,不是正常路径**(05 §9.4 明确)。本文定稿:

```go
// 顶层 Program.Call 的最外层 recover 兜底(05 §9.4)。只防【意外 Go panic】,不是正常错误路径。
func (p *Program) Call(arena *Arena, args ...Value) (results []Value, err error) {
    th := p.mainThread
    defer func() {
        if r := recover(); r != nil {
            // 意外 Go panic(host bug / VM bug)落到这里。
            // ① 把它转成 Lua 错误对象(字符串,含 panic 信息)
            msg := fmt.Sprintf("internal error (Go panic): %v", r)
            err = &HostPanicError{Recovered: r, Message: msg}   // Go error 类型(§11.2)
            // ② 标记 Thread 损坏:状态机进入不可恢复态(05 §5.6 word1 status)
            th.markCorrupted()              // status = dead + corrupted 标志;后续任何操作拒绝
            // ③ 不尝试清理 CallInfo(panic 已破坏栈不变式,清理可能二次 panic)
            results = nil
        }
    }()
    return p.callProtected(th, args)        // 正常执行(内部错误走 §2 的 *LuaError 路径,不到这里)
}
```

### 11.2 兜底的语义边界(可恢复性)

| 维度 | 定稿 |
|---|---|
| 何时触发 | **仅** host/VM 代码意外 Go panic(bug)。正常 Lua 错误走 `*LuaError` 显式返回(§2),**永不到 recover** |
| 转成什么 | Go 侧 `error`(`HostPanicError`,含原始 `recover()` 值 + 信息);**不是** Lua 错误对象交还脚本 |
| 能被 `pcall` 捕获吗 | **不能**。这是顶层兜底,在 `Program.Call` 最外层,已穿过所有 Lua 的 `pcall`。脚本无法捕获 Go panic |
| Thread 后续可用吗 | **否**。Thread 标 `corrupted`(§11.1 ②),后续 `Program.Call`/`resume` 直接拒绝(返回「Thread 损坏」错误) |
| 与正常错误的区别 | 正常错误 = 可控、可 pcall、Thread 仍健康;panic 兜底 = 不可控、不可 pcall、Thread 报废 |

**为什么标 Thread 损坏而非继续用**:Go panic 发生在任意 PC,此刻 `arena`/CallInfo/值栈/upvalue 链可能处于
**半更新的不一致态**(panic 打断了某个本应原子的操作)。继续用这个 Thread 会读到损坏状态、行为未定义。所以
**报废它**是唯一安全选择。宿主收到 `HostPanicError` 应丢弃该 State/Thread,重建。

> **这是「安全网」而非「正常路径」的全部含义**(05 §9.4):正常的 Lua 错误(`error`、类型错误、stack overflow)
> 都不经这条 recover——它们是 `*LuaError` 显式 return(§2),Thread 健康、可 pcall、可继续。recover 只兜
> **不该发生的 Go panic**(代码 bug),并把 Thread 判死。P1 的 host functions([10](./10-stdlib.md))应严格
> 用 `raise` 抛错、不 panic,使这条兜底永不触发(它存在只为「万一」)。

---

## 12. 与协程的关系(协程边界 = 另一个错误停靠站)

### 12.1 错误跨 resume 边界:协程内未捕获错误使 resume 返回 (false, err)

**协程的 resume 边界是 §2.2 路径图里「另一类停靠站」**(与 pcall 并列)。08(协程)未写,本节给 09 侧的定稿,
**08 引用本文**:

```
coroutine.resume(co, args...) 内:
  在 co 的 Thread 上跑 execute(从 co 上次 yield 点或起点恢复)。
  - co 正常 yield:  resume 返回 (true, yield 的值...)。
  - co 正常结束(主函数 return):  resume 返回 (true, 返回值...)。
  - co 内【未被协程内 pcall 捕获】的错误:
      错误一路 return 出 co 的 execute(冒泡到 resume 这个 host 边界);
      ★ resume 是一个 protected 边界(类似 pcall)——它【捕获】*LuaError;
      → resume 返回 (false, errval);
      → co 的 Thread 状态变 dead(05 §5.6 word1 status = dead);
      → co 此后不可再 resume(再 resume 报 "cannot resume dead coroutine")。
```

**关键定稿**:

- **`resume` 是 protected 边界**:它和 `pcall` 一样,在 host 里调 `callLuaFromHost`(实为切到 co 的 CallInfo
  链跑 execute)并**检查返回的 `*LuaError`**(05 §9.4:每个 host→Lua 重入点都是潜在捕获点)。所以协程内的
  错误**不会**冒泡穿过 resume 打到 resume 的调用者——它被 resume 转成 `(false, err)` 返回值。
- **协程出错即 dead**:与「协程正常结束变 dead」一样,**出错也使协程变 dead**(不可重入)。这是 5.1 语义:
  出错的协程不能恢复(它的栈状态已随错误冒泡作废)。
- **错误对象传递**:`errval` 就是 `LuaError.value`(可任意类型,§1.2)。`resume` 把它作为第二返回值。
  **位置前缀/变量名后缀已在 co 内构造错误时拼好**(§3/§8),resume 原样传递。

### 12.2 协程边界 vs pcall 边界的异同

| 维度 | pcall 边界(§5) | resume 边界(本节) |
|---|---|---|
| 是否捕获错误 | ✅ | ✅ |
| 捕获后 | 返回 `(false, errval)` 给**同一 Thread** 的调用者 | 返回 `(false, errval)` 给 resume 的**调用者 Thread** |
| 出错对象 | `LuaError.value` | `LuaError.value` |
| 清理 | `recoverToProtectionPoint`(回退 ciTop 等,§5.3) | co 的 Thread 整体标 dead(不需回退到保护点——整个 co 作废) |
| 跨 Thread? | 否(同 Thread) | **是**(co Thread → resumer Thread) |
| 错误后还能用吗 | pcall 后 Thread 健康,可继续 | co 变 dead,不可再 resume;resumer 健康 |

> **traceback 跨协程**:co 内的错误,其 traceback(若 xpcall handler 生成)只含 **co 自己的 CallInfo 链**
> (co 的栈),**不含** resumer 的栈(两个 Thread 的 CallInfo 链独立,05 §1.2 每 Thread 一条链)。这与 5.1
> 一致:协程有独立栈,traceback 不跨 resume 缝合(除非用 `debug.traceback(co)` 显式传 co,§13)。

### 12.3 `coroutine.wrap` 的错误处理差异

`coroutine.wrap(f)` 返回一个函数,调用它 = resume 对应协程,但**错误处理不同于 `resume`**:

- **`resume` 捕获错误**返回 `(false, err)`(本节 §12.1)。
- **`wrap` 的函数不捕获**:协程内错误**直接传播**(re-raise)到调用 wrap 函数的地方(等价 `wrap` 内部
  `resume` 后若 `false` 就 `error(err)` 重抛)。所以 `wrap` 的函数出错会让**调用者**的 pcall 捕获(若有)。
- **08 定稿 wrap 细节**,本文只点明这个错误传播差异(09↔08 协作)。

---

## 13. debug 库接口(traceback / getinfo —— P1 简化范围记缺口)

`debug` 库为错误处理与诊断供料。**P1 实现最小子集**(够 `xpcall` 用 + 基本 introspection),完整 debug 库记缺口。
10 引用本节范围。

### 13.1 `debug.traceback([thread,] [message [, level]])`

**最重要的 debug 接口**——它是 `xpcall` 的标准 message handler。语义:

```
debug.traceback(message, level):
  -- message 非 string 且非 nil:【原样返回 message】(5.1:不是 string 就不处理,直接返回)
  if message != nil and type(message) != "string":
    return message
  -- 否则:生成当前(或指定 thread)栈的 traceback,message 作前缀
  return (message and message.."\n" or "") .. traceback(thread, level)   -- §7.2
```

- **作 xpcall handler 时**:`xpcall(f, debug.traceback)`——出错时 `debug.traceback(errval)` 被调(§6.2,
  在出错栈上),把 `errval`(错误信息)作前缀 + 当前出错栈的回溯,返回带完整 traceback 的字符串。这是
  「为什么 xpcall 比 pcall 有用」的核心:`pcall` 只给错误信息,`xpcall(f, debug.traceback)` 给错误信息 + 栈回溯。
- **`message` 非 string 原样返回**(5.1 行为):若传入的「错误」是 table(`error({})`),`debug.traceback`
  **不**给它加栈回溯(无法拼字符串),直接返回该 table。**待 12 核对**此 5.1 行为。
- **`level` 参数**:从第几层开始回溯(跳过最内层若干帧,如跳过 traceback 自身)。P1 支持(§7.2 `startLevel`)。
- **`thread` 参数**:可对**另一个协程** co 生成 traceback(`debug.traceback(co)`)——跨 Thread 读 co 的
  CallInfo 链(§12.2)。P1 可简化(只支持当前 thread,记缺口)。

### 13.2 `debug.getinfo([thread,] f_or_level [, what])`

返回一个 table,描述函数或栈帧的信息(给 traceback 供料 / 脚本 introspection)。Lua 5.1 字段:

| 字段 | 含义 | 来源 | P1 |
|---|---|---|---|
| `source` | chunk 源名(`@file`/`=name`/`=[string]`) | `Proto.Source`([01] §5.7) | ✅ |
| `short_src` | 短名(`chunkID` 后,§3.4) | 同上经 chunkID | ✅ |
| `currentline` | 当前行(栈帧时) | pc→line(§3.5,含 -1) | ✅ |
| `what` | `"Lua"`/`"C"`/`"main"` | 帧类型(host 哨兵 protoID / 主 chunk) | ✅ |
| `name` | 推断的函数名 | `getfuncname`(§8) | △(依赖 §8 简化) |
| `namewhat` | 名字种类(`"global"`/`"local"`/`"method"`/...) | `getobjname` kind(§8) | △ |
| `linedefined` / `lastlinedefined` | 函数定义起止行 | Proto 调试信息(需回填?) | △ |
| `nups` | upvalue 数 | `Closure.nupvals`([01] §5.3) | ✅ |
| `func` | 函数值本身 | 栈/参数 | ✅ |

- **`what` 参数**:Lua 5.1 用字符串选要哪些字段(`"n"`=name,`"S"`=source,`"l"`=line,`"u"`=ups,`"f"`=func)。
  P1 可全填(忽略 `what` 选择,返回全部),或按 `what` 裁剪。
- **给 traceback 供料**:`debug.traceback` 内部对每帧调 `getinfo`(等价)取 `short_src`/`currentline`/`name`。
  P1 的 `traceback`(§7.2)直接遍历 CallInfo 不必经脚本可见的 `getinfo`,但 `getinfo` 暴露同样的信息给脚本。

### 13.3 P1 debug 库范围(记缺口,10 引用)

| 接口 | P1 | 说明 |
|---|---|---|
| `debug.traceback` | **✅ 必做** | xpcall 标准 handler;§13.1 |
| `debug.getinfo` | **△ 部分** | 基本字段(source/line/what/nups);`linedefined`/完整 `name` 依赖调试信息回填,简化 |
| `debug.sethook`/`gethook` | **❌ 不做** | 调试钩子(行/调用 hook),P1 不实现(roadmap §5 原则 4:debug 形状走 fallback) |
| `debug.getlocal`/`setlocal` | **❌ 不做** | 读写栈帧局部变量,依赖 LocVars 回填 + 完整符号执行 |
| `debug.getupvalue`/`setupvalue` | **△ 可选** | 读写 upvalue,依赖 UpvalNames |
| `debug.setmetatable`/`getmetatable` | **✅ 必做** | 已在 07 §1.3 定义(per-type 元表后门) |
| `debug.getregistry` | **❌ 不做** | registry 访问,P1 内部用不暴露 |
| `debug.traceback(co)` 跨协程 | **△ 简化** | 只支持当前 thread,跨 co 记缺口 |

> **P1 debug 库哲学**(roadmap §5 原则 4):debug 库是「不可升层、永远走解释」的典型形状。P1 只做
> **错误处理必需的 `debug.traceback`** 与 **introspection 基本的 `debug.getinfo`**,其余(hook/getlocal/...)
> **记缺口,P1 不做**。完整 debug 库是 P1 后(或永不,视宿主需求)的增量。**10 以此范围为准**。

---

## 14. 错误路径不在热路径(承 05 §9.5,给指针)

**性能无损论证见 [05](./05-interpreter-loop.md) §9.5**(本文不复述,给指针):错误传播是显式 `*LuaError` 返回,
热路径里 `if e != nil` 的 `e` 几乎总是 nil(正常脚本极少出错),分支预测器轻松命中,接近零成本;反观 panic
模型即便不出错也要为可能的 panic 准备 defer/recover 框架,反而拖累热路径。**显式返回是「错误路径让步、热路径
优先」的正确选择**(05 §9.5),与 roadmap §1「减少每指令开销」一致。

**本文新增的错误处理成本也全在冷路径**:

- **traceback 生成**(§7):只在 xpcall handler / 顶层未捕获 / 显式 `debug.traceback` 时做——都是冷路径
  (出错或主动诊断),不影响正常执行。
- **函数名推断**(§8):只在生成 traceback 或构造错误信息时做——冷路径。`getobjname` 的逆向符号执行虽不便宜,
  但每个错误最多做几次,且错误本就罕见。
- **位置前缀/变量名后缀**(§3/§8):只在构造 `LuaError` 时拼一次字符串——冷路径。

> **唯一需警惕的「热路径泄漏」**:`pcall` 是控制流(不只异常),可能在热循环里被频繁调用(如
> `for ... do pcall(f) end`)。但 `pcall` 成功路径(无错)**不生成 traceback、不推断函数名、不拼错误信息**
> ——它只是「起 fresh 帧 → execute → 检查返回 nil → 返回 true + 结果」,开销是一次 host→Lua 重入(05 §7.3),
> 与普通函数调用同量级。**只有 `pcall` 失败路径才触发本文的错误构造**,而失败本就罕见。所以「频繁 pcall」
> 不构成热路径错误成本(成功路径无错误处理开销)。

---

## 15. 不变式清单(实现与差分须守)

1. **显式返回不用 panic**(05 §9.1):错误经 `*LuaError` 在 helper 间 return 冒泡,**绝不** panic/recover 跨主循环。
   唯一的 recover 是顶层 host panic 兜底(§11,安全网,非正常路径)。
2. **CallInfo 清理在边界**(05 §9.3 / §2.2):出错帧不清理,只 `return e` 冒泡;protected 边界(pcall/xpcall/
   resume)一次性 `recoverToProtectionPoint`(回退 ciTop/top/nCcalls + closeUpvals)。批量,非逐帧 defer。
3. **错误值可任意类型,位置前缀只对 string + level≠0 加**(§1.2/§3.2):`error({})` 抛 table 不加前缀;
   解释器内在错误恒 string 带前缀。
4. **assert 抛裸 message,不加位置**(§4.1):`assert` 跳过 `where()`,与 `error(msg,1)` 形成对比(后者加前缀)。
   默认 `"assertion failed!"`。
5. **xpcall handler 在栈展开前调**(§6.2):捕获错误后、`recoverToProtectionPoint` 前,在**未清理的出错栈**上
   调 handler——这是 `debug.traceback` 能拿到完整栈的充要条件。**P1 关键决策**。
6. **xpcall 5.1 不传 args 给 f**(§6.1):`xpcall(f, h, ...)` 的额外参数被忽略(5.2+ 才传)。锁 5.1。
7. **pc→line 含 -1 偏移**(§3.5/§7.4):栈顶帧 `pc-1`,非栈顶帧 `savedPC-1`。traceback/error 行号正确性的命脉。
8. **位置前缀格式 `<source>:<line>: `**(§3.2):source 经 `chunkID`(§3.4),冒号后一空格。C 帧无前缀(`[C]`)。
9. **变量名后缀由 09 定,类型名层由 07 定**(§8.2):完整错误 = `<src>:<line>: attempt to X a <type> value (<kind> '<name>')`。
10. **函数名推断 P1 简化**(§8.3):必做 global/field/method(从常量池取名);应做 local(需 LocVars 回填);
    可选 upvalue/for-iter/metamethod;不做完整跨跳转 symbexec。退化为无后缀,类型名层仍完整。
11. **traceback 按需生成**(§7.3):抛出点不生成;只在 xpcall handler / 顶层未捕获 / 显式 debug.traceback 时生成。
12. **两类 stack overflow 分清**(§10):Lua 深度 → `stack overflow`(arena 上限);host↔Lua 重入 → `C stack overflow`
    (`nCcalls`=200,真 Go 栈)。
13. **host 不 panic,经 raise 抛错**(§3.3/§11):host 用 `raise`→`pendingErr`→`callHost` 返回 callError 路径;
    真 panic 只被顶层兜底捕获并报废 Thread。
14. **协程边界捕获错误**(§12):resume 是 protected 边界,co 内未捕获错误 → resume 返回 `(false, err)` + co 变 dead;
    `wrap` 则重抛(不捕获)。traceback 不跨 resume 缝合。
15. **错误措辞与官方 5.1 逐字节一致**(§9.3):措辞主体可信,边角标点/冠词/单复数**待 12 差分核对**,不编造。

---

## 16. 文档缺口 / 待决(记入 memory/doc-gaps)

- ~~对 [01](./01-value-object-model.md) §5.7 的回填请求~~:**已兑现**(§8.4)——01 §5.7 已增 `LocVars []LocalVar`,upvalue 名复用 `UpvalDescs.name`;§8 的 local/upvalue 推断可实现。
- **`getobjname` 完整 symbexec**:§8.3 P1 只做「线性逆推最近写指令」,不跨基本块/跳转。完整 Lua 5.1 `symbexec`
  (处理控制流合并、寄存器多路径写)P1 不做,记缺口。复杂控制流后的错误可能少变量名后缀,差分宽松匹配。
- **错误措辞精确格式**:§9.3 所有内在错误的冠词(`a`/`an`)/复数/标点,以及 `assertion failed!`、
  `error in error handling`、`'__tostring' must return a string`、`cannot resume dead coroutine` 等的精确文案,
  **待 12 差分核对**与官方 Lua 5.1 逐字节对齐。本文给骨架,不编造。
- **`chunkID` 截断规则**:§3.4 的 `LUA_IDSIZE`(=60)、`[string "..."]` 截断位置/省略号,**待 12 核对**逐字节一致。
- **C stack overflow 的错误处理余量**:§10 给 `pcall` 保留 C 栈 buffer 让错误能被处理(`nCcalls` 超 200 但允许
  错误路径短暂超到 ~220),精确余量值待 12 核对(对齐 Lua 5.1 `LUAI_MAXCCALLS` 之上的处理余量)。
- **`debug.traceback(co)` 跨协程**:§13.1/§13.3 P1 简化为只支持当前 thread,跨 co 回溯记缺口。
- **`debug.getinfo` 的 `linedefined`/`lastlinedefined`**:§13.2 需 Proto 持久化函数定义起止行(类似 LineInfo
  的额外调试字段),是否回填 01 待定;P1 可缺(返回 -1 或省字段)。
- **`LuaError.level` 字段冗余**:§1.1 指出 level 在构造位置前缀后即无意义,是否从 struct 移除待定(保留无害)。
- **xpcall 是否支持 5.2 传 args**:§6.1 P1 锁 5.1(不传)。若宿主生态需要 5.2 行为,是否提供开关待定,**默认 5.1**。
- **元方法触发帧的 traceback `what`**:§8.1 经元方法进入的帧(无显式 CALL)的函数名(`metamethod 'index'` 等)
  P1 可简化为 `?`,完整 5.1 行为记缺口。

---

相关:[01-value-object-model](./01-value-object-model.md)(§5.6 Thread errorJmp / §5.7 Proto LineInfo/Source,
LocVars/UpvalNames 回填) · [02-bytecode-isa](./02-bytecode-isa.md)(§5 LineInfo / §4 opcode) ·
[04-frontend-parser-codegen](./04-frontend-parser-codegen.md)(§5.9 locvars / §8.3 upvalDesc,§13 同一回填缺口) ·
[05-interpreter-loop](./05-interpreter-loop.md)(§9 错误传播定稿 / §7.3 fresh 边界 / §7.4 两类 overflow /
§1.2 CallInfo errfuncBase / §7.2 ci.tailcall / §7.5 host 帧 CallInfo) ·
[06-memory-gc](./06-memory-gc.md)(§10 `__gc` 终结器错误保护) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(§14.4 类型名层措辞 / 变量名增强下放本文) ·
[08-coroutines](./08-coroutines.md)(resume 错误边界,本文 §12 供其引用) ·
[10-stdlib](./10-stdlib.md)(error/assert/pcall/xpcall/debug 库 host functions) ·
[11-embedding-arena-abi](./11-embedding-arena-abi.md)(顶层 Program.Call 错误转 Go error / host panic 兜底) ·
[12-testing-difftest](./12-testing-difftest.md)(错误措辞 / traceback 行号逐字节核对) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
roadmap:`docs/design/roadmap.md` (§6 锁 Lua 5.1 / §5 原则 2 差分一致 / 原则 4 debug 走 fallback)
