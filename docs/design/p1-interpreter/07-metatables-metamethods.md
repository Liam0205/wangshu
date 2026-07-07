# P1 脊柱:元表与元方法语义

> 状态:**设计阶段,可实现深度**。本文是 Lua 5.1 **元表存储 + 元方法分发**的单一事实源:
> 元表挂在哪、`getmetatable`/`setmetatable`/`__metatable` 语义、元方法事件总表、
> `__index`/`__newindex` 链、算术/比较/连接/调用/长度元方法的查找顺序与触发条件、
> 字符串→数字 coercion 归属、`__mode` 弱表语义。
> 上游契约:[05-interpreter-loop](./05-interpreter-loop.md) 把多条**慢路径**显式下放给本文——
> §4.2 `arithMeta`、§4.3 `__unm`/`__len`、§4.4 `lessThan`/`__eq`/`__le`、§4.6 `__concat`、
> §6.3 `indexMeta`(`__index` 链)、§6.4 `__newindex`、§7.1 `callMeta`(`__call`);
> 本文逐一定义,**函数名与 05 严格一致**。值/对象侧:[01-value-object-model](./01-value-object-model.md)
> §5.2 Table `metaRef`/`flags bit0`、§5.5 Userdata `metaRef`、§5.1 String、§6 相等语义。
> 触发元方法的 opcode 见 [02-bytecode-isa](./02-bytecode-isa.md) §4。`__gc` 终结器语义在
> [06-memory-gc](./06-memory-gc.md) §10 定稿,本文只引用;`__mode` 弱表的 GC 协作细节本文定义语义、
> 向 06 提回填请求(§13)。语言面锁 Lua 5.1(`docs/design/roadmap.md` (§6),5.2+ 特性显式排除)。

对应 Go 包:元表分发逻辑在 `internal/crescent`(与解释器同包,慢路径 helper);
元表存储(`metaRef` 字段读写、`flags bit0`)在 `internal/object`;per-type 元表表挂 `State`。

---

## 0. 本文在 P1 中的位置与设计张力

元方法是 Lua「机制开放」的核心:`+`、`[]`、`..`、`()`、`<` 等运算符在原始类型不满足时**回退到用户定义的函数**。它把语言语义从「内建固定」变成「可被 metatable 改写」。

本文的全部张力来自两条硬约束的夹击:

1. **必须严格 Lua 5.1,不做 5.2+**(`roadmap.md` (§6))。元方法是 5.1 与 5.2/5.3/5.4 差异最密集的区域之一:`__le→__lt` 回退(5.1 有、5.4 删)、table 的 `__len`(5.2 才有)、`__pairs`(5.2)、table 的 `__gc`(5.2)、位运算元方法 `__band` 等(5.3)、`__close`(5.4)。**每一个 5.2+ 元方法都必须显式排除并说明它属于哪个版本**,否则差分基准会因「多支持了一个元方法」而与官方 5.1 行为分叉(roadmap §5 原则 2「层间逐字节差分」的反面:不仅 tier 间要一致,**与官方 5.1 参考实现也要一致**)。
2. **元方法查找在热路径上,但调用在冷路径上**(承 [05](./05-interpreter-loop.md) §3 / §4)。05 已把「双 number 算术」「raw 命中表」「`rawequal` 命中」等设为快路径,**只有快路径失败才进本文的慢路径**。所以本文的实现纪律是:**查找本身极廉价(只 `rawget` 一次 metatable,不分配),但一旦命中函数就可能 reentry/调 host/触发 GC**——这正是 05 §1.3「重载 stk」纪律的来源。

> 一句话定位:本文是 05 的**慢路径库**。05 负责「快路径判定 + 在哪 dispatch」,本文负责「快路径失败后,按 Lua 5.1 语义查哪个元方法、以什么顺序、用什么参数调用、不命中报什么错」。

---

## 1. 元表存储模型

### 1.1 谁能拥有元表 —— Lua 5.1 三档口径

Lua 5.1 中「值能否有元表」分三档,**必须分清**(这是 5.1 与直觉的偏差点):

| 类型 | 元表存储 | 谁能设 | 备注 |
|---|---|---|---|
| **table** | **每个 table 各自的 `metaRef`**([01](./01-value-object-model.md) §5.2 word4) | `setmetatable`(Lua 可见) | 最常用;`flags bit0` 是「有无 metatable」快判位 |
| **full userdata** | **每个 userdata 各自的 `metaRef`**([01](./01-value-object-model.md) §5.5 word2) | `debug.setmetatable` / 宿主 C-API | Lua 脚本不能直接 `setmetatable` userdata(`setmetatable` 仅接受 table 作首参) |
| **string** | **全类型共享一张** string metatable | 由 `string` 库初始化(`__index = string` 表) | 所有字符串共享同一张;`("x"):upper()` 即经此 |
| number / boolean / nil / function / thread / lightuserdata | **per-type 共享一张**(默认 nil) | **仅 `debug.setmetatable`**(Lua 5.1 行为) | 默认无元表;P1 提供 `typeMetatables` 槽位,见 §1.2 |

**关键 5.1 口径**:

- `setmetatable`(base 库)**只接受 table 作第一参数**;对非 table 调用报 `"bad argument #1 to 'setmetatable' (table expected, got <type>)"`。userdata 与其它原始类型的元表**只能经 `debug.setmetatable` 设置**(5.1 允许 `debug.setmetatable` 设任意类型的元表)。
- **string 的元表是全局共享的**:不是「每个 string 对象一份」,而是「string 类型一份」。`string` 库在初始化时建一张表 `mt`,`mt.__index = string`(string 库表自身),并把 `mt` 设为 string 类型元表。于是 `s:upper()` → `GETTABLE`/`SELF` 在 string 上查 `__index` → 命中 string 库的 `upper`(§3.4 详述)。
- number/bool/nil/function/thread 默认**无**元表。Lua 5.1 通过 `debug.setmetatable(v, mt)` 可给它们的**类型**设元表(影响该类型所有值)。这是 5.1 特有的开放性(5.2+ 仍保留 `debug.setmetatable`,但生态极少用)。P1 实现这个槽位以求 5.1 完整性,但 stdlib 默认全留空。

### 1.2 State 持有 per-type 元表表 `typeMetatables`

为统一存储「非 table/userdata 类型的共享元表」(string 与可由 `debug.setmetatable` 设的其它类型),`State` 持有一张定长数组,**按内部类型 tag 索引**:

```go
// internal/crescent —— State 持有 per-type 共享元表(Lua 5.1 debug.setmetatable 的承载)
// 索引 = 内部类型枚举(与 [01] §3.3 tag 对应,折叠成 0..8 连续小整数)
type State struct {
    // ...
    typeMetatables [LUA_NUMTYPES]value.GCRef // 每槽是一张 metatable Table 的 GCRef,或 0(无)
    // ...
}

const LUA_NUMTYPES = 9 // nil/bool/lightud/number/string/table/function/userdata/thread
```

**重要区分**:

- **table 与 full userdata 的元表 *不* 走 `typeMetatables`** —— 它们是 **per-instance**(每个对象自带 `metaRef`)。`typeMetatables[TABLE]` / `[USERDATA]` **恒为 0,不使用**(若误用会让「所有表共享一张元表」,错)。这两类的元表查找永远从对象的 `metaRef` 取。
- **string 走 `typeMetatables[STRING]`** —— 所有字符串共享。string 库初始化时写入此槽。
- number/bool/nil/function/thread/lightuserdata 走各自 `typeMetatables[tag]`,默认 0,仅 `debug.setmetatable` 写入。

**元表获取的统一入口** `getMetatable(v)`(本文的核心 helper,所有元方法查找都先调它):

```go
// 取值 v 的元表 GCRef(0 表示无元表)。这是元方法查找的唯一入口。
func (vm *VM) getMetatable(v value.Value) value.GCRef {
    switch value.Tag(v) {
    case value.TagTable:
        t := object.TableAt(vm.arena, value.GCRefOf(v))
        if t.HasMetatable() {            // flags bit0([01] §5.2),快判位:无元表则零成本短路
            return t.MetaRef()           // word4
        }
        return 0
    case value.TagUserdata:
        u := object.UserdataAt(vm.arena, value.GCRefOf(v))
        if u.HasMetatable() {            // flags bit0([01] §5.5)
            return u.MetaRef()           // word2
        }
        return 0
    default:
        // string / number / bool / nil / function / thread / lightuserdata
        // 走 per-type 共享元表;number 是「非 boxed」,tag 提取要走 typeIndexOf
        return vm.typeMetatables[typeIndexOf(v)]
    }
}
```

- `typeIndexOf(v)`:对 boxed 值取 `Tag(v)` 折叠;对 number(`IsNumber(v)`)返回 `NUMBER` 槽。这是唯一需要把「number 不是 boxed」纳入考量的地方([01](./01-value-object-model.md) §3.2)。
- **`flags bit0` 快判位的价值**:table/userdata 绝大多数**没有**元表。`HasMetatable()` 是一次 `flags & 1` 位测,**不命中即零成本返回 0**,后续所有元方法查找直接判定「无元方法」。这是 §11 IC/快判优化的基石(对应 05 §6 的「无元方法快速短路」)。

### 1.3 `getmetatable` / `setmetatable` 语义(含 `__metatable` 保护)

这两个是 base 库 host function([10-stdlib](./10-stdlib.md) 提供),语义本文定稿(10 引用):

**`getmetatable(v)`**:

```
getmetatable(v):
  mt := vm.getMetatable(v)            // §1.2 统一入口
  if mt == 0: return nil
  // __metatable 保护:若元表有 __metatable 字段,返回该字段值(隐藏真元表)
  prot := rawget(mt, "__metatable")
  if prot != nil: return prot
  return mt                            // 返回元表本身(table 值)
```

**`setmetatable(t, mt)`**:

```
setmetatable(t, mt):
  1. typecheck:t 必须是 table,否则报 "bad argument #1 ... (table expected, got <type>)"。
     mt 必须是 table 或 nil,否则报 "bad argument #2 ... (nil or table expected)"。
  2. __metatable 保护:若 t 当前元表存在且有 __metatable 字段,
     报 "cannot change a protected metatable"(5.1 措辞,待 12 差分核对)。
  3. 设置:t.metaRef = (mt==nil ? 0 : GCRefOf(mt));
     更新 t 的 flags bit0(mt!=nil ⇒ 置 1;mt==nil ⇒ 清 0)。
  4. **bump 该 table 的 gen 代次**(承 05 §6.5:改 metatable 失效相关 IC)。
  5. return t                          // setmetatable 返回 t 本身(便于链式 t=setmetatable({},mt))
```

要点:

- **`__metatable` 双重作用**(5.1 语义):① `getmetatable` 看到它就返回它的值而非真元表(对脚本隐藏真元表);② `setmetatable` 看到旧元表有它就**拒绝改元表**(报错保护)。这让库作者能「锁死」对象的元表。
- **第 4 步 bump gen 是与 05 的关键耦合**:05 §6.1 定义 table 有单调代次 `gen`,IC 命中靠「同表 + 同代次」校验。改 metatable 会改变「该表查 `__index` 等的结果」,所以**必须 bump gen 让缓存了该表元方法查找的 IC 失效**(05 §6.5 表格已列「`setmetatable`/清 metatable → 递增该表 gen」并指向本文)。**这是本文对 05 IC 失效契约的兑现点**。
- `debug.setmetatable(v, mt)`(debug 库):对 table/userdata 写其 `metaRef`(并 bump gen);对其它类型写 `typeMetatables[typeIndexOf(v)]`;**不做 `__metatable` 保护**(debug 库是「绕过保护」的后门,5.1 语义)。返回值是 v(5.1)。

---

## 2. 元方法分发总则

### 2.1 查找机制:`rawget(metatable, event)`

所有元方法查找的统一形式:**对值的元表 `rawget` 事件名字符串**,得到一个 Value:

```go
// 取值 v 的元方法(事件 event 对应的字段),不命中返回 Nil。
// event 是预先 intern 的事件名字符串常量(见 §2.3),rawget 是纯查找无副作用。
func (vm *VM) getMetamethod(v value.Value, event value.GCRef) value.Value {
    mt := vm.getMetatable(v)            // §1.2
    if mt == 0 {
        return value.Nil
    }
    return object.TableAt(vm.arena, mt).RawGet(eventKey(event)) // rawget,绝不触发 __index!
}
```

**关键性质**:

- **查找用 `rawget` 而非 `getMetatable[event]`**:元表自身的字段访问**不能再触发 `__index`**(否则无限递归)。元方法事件名直接 `rawget` 元表的 hash 部分。
- **查找零分配**:`rawget` 只读 table 的 node 段([01](./01-value-object-model.md) §5.2),不分配、不调用。所以**查找本身可以放在热路径的慢分支**,代价仅一次哈希查找(且事件名是 intern 串,比较是 GCRef 比较)。
- **事件名预 intern**:18 个事件名(§2.3)在 `State` 初始化时一次性 intern 成 GCRef,存 `State.eventNames[ev]`。查找时直接用其 GCRef 作 key,**不重复 intern**。

### 2.2 事件名常量集(`State.eventNames`)

```go
// internal/crescent —— 元方法事件枚举(P1 支持的全集 = Lua 5.1 元方法集)
type Event uint8
const (
    EvIndex Event = iota // __index
    EvNewIndex           // __newindex
    EvAdd                // __add
    EvSub                // __sub
    EvMul                // __mul
    EvDiv                // __div
    EvMod                // __mod
    EvPow                // __pow
    EvUnm                // __unm
    EvConcat             // __concat
    EvLen                // __len   (5.1: 仅 userdata 触发,§7)
    EvEq                 // __eq
    EvLt                 // __lt
    EvLe                 // __le
    EvCall               // __call
    EvToString           // __tostring (stdlib tostring/print 用,§10)
    EvMetatable          // __metatable (保护,§1.3;非「调用型」事件)
    EvMode               // __mode   (弱表,§12;非「调用型」事件)
    EvGC                 // __gc     (终结器,语义在 [06] §10;本文仅引用)
    numEvents
)
// State.eventNames [numEvents]value.GCRef —— 初始化时各 intern 一次
```

### 2.3 事件总表:事件 → 触发场景 → arity → 5.1 是否支持

下表是**唯一事实源**,逐 opcode/场景列出触发点、被调元方法的参数形式、Lua 5.1 是否支持:

| 事件 | 触发 opcode / 场景 | 被调 arity(参数) | 返回 | 5.1 支持 | 慢路径 helper(本文) |
|---|---|---|---|---|---|
| `__index` | `GETTABLE`/`GETGLOBAL`/`SELF` raw 查空 | table:`(t,k)` 取 `mt.__index[k]`;function:调 `__index(t,k)` | 1 值 | ✅ | `indexMeta`(§3) |
| `__newindex` | `SETTABLE`/`SETGLOBAL` raw 槽不存在 | table:写 `mt.__newindex[k]=v`;function:调 `__newindex(t,k,v)` | 无 | ✅ | `newindexMeta`(§4) |
| `__add` | `ADD` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__sub` | `SUB` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__mul` | `MUL` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__div` | `DIV` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__mod` | `MOD` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__pow` | `POW` 非双 number | `(a,b)` | 1 值 | ✅ | `arithMeta`(§5) |
| `__unm` | `UNM` 非 number | `(a, a)`(5.1 传两次同操作数,见 §6) | 1 值 | ✅ | `unmMeta`(§6) |
| `__concat` | `CONCAT` 含非 string/number | `(a,b)` | 1 值 | ✅ | `concatMeta`(§7 之前的 §8) |
| `__len` | `LEN` 且操作数是 **userdata** | `(a)` | 1 值 | ✅(**仅 userdata**) | `lenMeta`(§9) |
| `__eq` | `EQ` 且同为 table 或同为 userdata 且 rawequal 假 | `(a,b)` | 真值 | ✅ | `equalMeta`(§10) |
| `__lt` | `LT` 快路径失败 | `(a,b)` | 真值 | ✅ | `lessThan`(§10) |
| `__le` | `LE` 快路径失败 | `(a,b)`(无 `__le` 回退 `__lt`,见 §10) | 真值 | ✅(**有 `__lt` 回退**) | `lessEqual`(§10) |
| `__call` | `CALL`/`TAILCALL` callee 不可调用 | `(callee, args...)` | callee 的返回 | ✅ | `callMeta`(§11) |
| `__tostring` | `tostring(v)` / `print` | `(v)` | string | ✅(stdlib) | §12 |
| `__metatable` | `getmetatable`/`setmetatable` | —(字段,非调用) | — | ✅ | §1.3 |
| `__mode` | GC mark/cleartable | —(字段 `"k"`/`"v"`/`"kv"`) | — | ✅ | §13 |
| `__gc` | GC 终结(userdata 不可达) | `(ud)` | 无 | ✅(**仅 userdata**) | [06](./06-memory-gc.md) §10 |

**Lua 5.1 明确不支持(P1 必须不实现 —— 实现了会与官方 5.1 差分失败)**:

| 事件 | 引入版本 | 说明 | P1 处理 |
|---|---|---|---|
| `__len` on **table** | 5.2 | table 的 `#` 在 5.1 直接取 border,**不查 `__len`** | `LEN` on table 直接 border,**绝不查元表**(§9) |
| `__idiv` | 5.3 | 整除 `//` | 无该运算符(5.1 无 `//`),无元方法 |
| `__band`/`__bor`/`__bxor`/`__shl`/`__shr`/`__bnot` | 5.3 | 位运算符元方法 | 无该类运算符,无元方法 |
| `__pairs` / `__ipairs` | 5.2(`__ipairs` 5.3 又废) | 自定义 `pairs`/`ipairs` 迭代 | `pairs`/`ipairs` 直接走 `next`/数组步进,**不查元表**([10](./10-stdlib.md)) |
| `__gc` on **table** | 5.2 | table 终结器 | 仅 userdata 可 `__gc`([06](./06-memory-gc.md) §10) |
| `__close` | 5.4 | to-be-closed 变量(`<close>`) | 无该语法,无元方法 |
| `__name` | 5.3 | userdata 类型名(影响错误信息) | 不支持;错误信息用基础类型名(§14) |

> **为什么逐版本标注而非只说「只做 5.1」**:差分基准（[12](./12-testing-difftest.md)）拿官方 Lua 5.1 当 oracle。**多支持一个元方法 = 在某些脚本上行为与 5.1 分叉**(例如若误实现 table 的 `__len`,则 `#t` 在有 `__len` 的表上会调元方法而 5.1 取 border，输出不同)。所以「不支持」必须是**主动的、有据的**：在对应 opcode 的执行侧明确「这里不查元表」。这是 roadmap §5 原则 2 在元方法层的体现。

### 2.4 元方法调用的统一收尾:`callMM`

多数事件的慢路径在「查到元方法 `mm`」后,要**以约定参数调用 `mm` 并取约定个数返回值**。统一封装:

```go
// 调用元方法 mm,参数 args(已在临时栈区铺好),期望 nresults 个返回值落到 dst。
// mm 可能是 Lua closure(→ 05 §7.1 reentry)或 host function(→ 05 §7.5)。
// **调用后必须 reload stk**(05 §1.3 纪律:调用可能扩容栈/触发 GC/搬迁 arena)。
func (vm *VM) callMM(f *frame, mm value.Value, args []value.Value, dst int32, nresults int) *LuaError {
    // 1. 把 mm 与 args 压到当前 top 之上的临时调用区(不覆盖活跃寄存器)
    callBase := vm.pushTemp(f, mm, args)        // 返回临时帧 base
    // 2. 走与 doCall 同构的调用:mm 是 Lua closure → enterLuaFrame + reentry 子循环;
    //    host → 同步 Go 调用。callFixed 是 05 的定参调用入口(05 §10.2 TFORLOOP 一样的)。
    if e := vm.callFixed(f, callBase, len(args), nresults); e != nil {
        return e                                 // 错误冒泡(05 §9)
    }
    // 3. 取返回值搬到 dst(算术/index 等取 1 个;newindex 取 0 个)
    if nresults >= 1 {
        f.stk[dst] = f.stk[callBase]             // 首返回值
    }
    // 4. 调用已返回,但元方法体内可能分配/GC/扩容 → reload(callFixed 内部已 reload,此处确保 f.stk 最新)
    reloadFrame(f)
    return nil
}
```

> **重要纪律(贯穿本文所有慢路径)**:`callMM` 内部一旦调用元方法,`f.stk` 必须重取(05 §1.3)。本文所有调用元方法的 helper（`indexMeta`/`arithMeta`/...）在 `callMM` 返回后**不得再用调用前缓存的栈指针**。这是元方法对解释器最大的副作用面（§14 单列）。**查找不分配，调用才分配。**

---

## 3. `__index` 链(GETTABLE/GETGLOBAL/SELF 慢路径)

承 [05](./05-interpreter-loop.md) §6.3:`doGetTable` 在「R(B) 是 table 但 raw 查无该键」或「R(B) 非 table」时调 `indexMeta`。本节定稿 `indexMeta`。

### 3.1 Lua 5.1 `__index` 语义

`t[k]` 的完整求值(`luaV_gettable` 等价):

```
对 t[k]:
  loop (Lua 5.1 不限制深度,见 §3.3):
    if t 是 table:
      v := rawget(t, k)
      if v != nil: return v                 // raw 命中,直接返回(不查元表)
      mt := getMetatable(t)
      if mt == 0: return nil                 // 无元表 ⇒ raw nil 即最终 nil
      h := rawget(mt, "__index")
      if h == nil: return nil                // 有元表但无 __index ⇒ nil
    else:  // t 非 table(string/number/userdata/...)
      mt := getMetatable(t)
      if mt == 0:
        报错 "attempt to index a <type> value"  // 非 table 且无元表 ⇒ 错
      h := rawget(mt, "__index")
      if h == nil:
        报错 "attempt to index a <type> value"
    // 此时拿到 __index 字段 h:
    if h 是 function:
      return call h(t, k)                     // function:调 __index(t,k),返回其首值,终止
    else:
      t := h; continue loop                    // table(或任意可索引值):转而索引 h[k],继续循环
```

**两种 `__index` 形式**:

- **`__index` 是 function**:调 `__index(t, k)`,**用首返回值作为 `t[k]` 的结果,循环终止**。这是「计算式索引」(如代理表、惰性字段)。
- **`__index` 是 table**(最常见,OOP 继承):转去索引 `__index[k]`——**这会再次走完整 `t[k]` 逻辑**(`__index` 表自己可能又有 `__index`,形成继承链)。

### 3.2 `indexMeta` 实现

```go
// 承 05 §6.3:GETTABLE/GETGLOBAL/SELF 的慢路径。
// raw0/key:首次访问的对象与键;dst:结果落点 R(A);slot:IC slot(命中函数式 __index 可标 mono-meta)。
func (vm *VM) indexMeta(f *frame, dst int32, t value.Value, key value.Value, slot *ICSlot) *LuaError {
    for loopGuard := 0; ; loopGuard++ {
        var h value.Value
        if value.Tag(t) == value.TagTable {
            tbl := object.TableAt(vm.arena, value.GCRefOf(t))
            // 注:进入 indexMeta 的前提是首层 raw 已 miss;但 __index 链的后续层要重新 raw 查
            if v, ok := tbl.RawGetOK(key); ok && v != value.Nil {
                f.stk[dst] = v
                return nil                          // 链中某层 raw 命中
            }
            mt := vm.getMetatable(t)
            if mt == 0 {
                f.stk[dst] = value.Nil
                return nil                          // 表 + 无元表 ⇒ 结果 nil(合法,非错)
            }
            h = object.TableAt(vm.arena, mt).RawGet(vm.eventKey(EvIndex))
            if h == value.Nil {
                f.stk[dst] = value.Nil
                return nil                          // 有元表无 __index ⇒ nil
            }
        } else {
            // t 非 table:string/number/userdata/...
            mt := vm.getMetatable(t)
            if mt != 0 {
                h = object.TableAt(vm.arena, mt).RawGet(vm.eventKey(EvIndex))
            }
            if mt == 0 || h == value.Nil {
                return vm.indexError(f, t, key)     // "attempt to index a <type> value"(§14)
            }
        }
        // 拿到 __index 字段 h
        if vm.isCallable(h) {                       // function(Lua/host)或有 __call 的对象
            // 调 h(t, key),首返回值落 dst;终止
            return vm.callMM(f, h, []value.Value{t, key}, dst, 1)
        }
        // h 是 table(或其它可索引值):转去索引 h[key],继续循环
        t = h
        // 防御:见 §3.3,P1 用大上限兜底防纯 table 环
        if loopGuard > maxIndexChain {
            return vm.errorf(f, "'__index' chain too long; possible loop")
        }
    }
}
```

### 3.3 无限循环问题:Lua 5.1 不防、靠形式收敛

**Lua 5.1 不主动检测 `__index` 环**。若 `setmetatable(a, {__index=b})` 且 `setmetatable(b, {__index=a})` 且查一个两表都没有的键,官方 5.1 会**无限循环**(实际靠 C 栈耗尽 → `"C stack overflow"` 或 `"'__index' chain too long"`,版本相关)。

**P1 决策**:

- **`__index` 是 function 时无循环风险**:function 式 `__index` 调一次即终止(返回其首值),不递归回 `indexMeta` 的循环。绝大多数继承链最终落到 function 或落到「无 `__index` 的表」而终止。
- **纯 table 链**:理论可成环。P1 用一个**大上限 `maxIndexChain`**(如 2000,对齐 Lua 的 `MAXTAGLOOP=100`?**待 12 差分核对**:Lua 5.1 `lvm.c` 用 `MAXTAGLOOP=100` 限制 `__index`/`__newindex` 链长,超限报 `"'__index' chain too long; possible loop"`)兜底。**定稿倾向对齐 Lua 5.1 `MAXTAGLOOP=100`**,使报错时机与官方一致(差分一致性 > 宽容)。
- **不用「访问集合去环」**:那会改变报错语义(官方是「链太长」而非「检测到环」),且要分配集合(慢路径也不该分配集合)。固定上限计数是零分配的。

> **doc-gap**:`maxIndexChain` 的精确值需与 Lua 5.1 `MAXTAGLOOP`(=100)对齐并由 [12](./12-testing-difftest.md) 核对报错措辞。本文倾向 100。

### 3.4 string 的 `s:upper()` 经 string metatable

具体走通一遍(扣合 §1.1 的 string 共享元表):

1. `s:upper()` codegen 成 `SELF R(A) R(B=s) RK(C="upper")`([02](./02-bytecode-isa.md) §4-11):先 `R(A+1) := s`(self 传递),再 `R(A) := s["upper"]`。
2. `doSelf`(05 §6.4 SELF)对 `s["upper"]`:`s` 是 string,**非 table**,raw 查不适用 → 直接进 `indexMeta`。
3. `indexMeta`:`t=s` 非 table,取 `getMetatable(s)` = `typeMetatables[STRING]`(string 共享元表,§1.2)。该元表 `rawget("__index")` = string 库表(`string` 全局)。
4. `__index` 是 table(string 库表),转去 `string["upper"]` —— raw 命中 `string.upper`(host function)。
5. 结果 `string.upper` 落 `R(A)`;随后 `CALL` 以 `(s)` 为参数调它(self 已在 `R(A+1)`)。

**所以 string 方法链是「string 值 → string 元表 → `__index=string` 库表 → 方法」**,经一次 `indexMeta` 的 table-`__index` 分支完成。**IC 在此命中率极高**:string 元表与 string 库表常驻不变,`slot` 缓存后同一方法名直达(§11)。

---

## 4. `__newindex` 链(SETTABLE/SETGLOBAL 慢路径)

承 [05](./05-interpreter-loop.md) §6.4:`doSetTable` 在「R(A) 是 table 但 raw 槽不存在(该键当前为 nil)」或「R(A) 非 table」时调 `newindexMeta`。

### 4.1 Lua 5.1 `__newindex` 语义

`t[k] = v` 的完整求值(`luaV_settable` 等价):

```
对 t[k] = v:
  loop:
    if t 是 table:
      if rawget(t, k) != nil:                 // 键已存在(槽非空)
        rawset(t, k, v); return               // 直接改值,**不触发 __newindex**
      mt := getMetatable(t)
      if mt == 0:
        rawset(t, k, v); return               // 无元表 ⇒ raw 插入新键
      h := rawget(mt, "__newindex")
      if h == nil:
        rawset(t, k, v); return               // 有元表但无 __newindex ⇒ raw 插入
    else:  // t 非 table
      mt := getMetatable(t)
      if mt == 0:
        报错 "attempt to index a <type> value"
      h := rawget(mt, "__newindex")
      if h == nil:
        报错 "attempt to index a <type> value"
    // 拿到 __newindex 字段 h:
    if h 是 function:
      call h(t, k, v); return                 // function:调 __newindex(t,k,v),无返回,终止
    else:
      t := h; continue loop                     // table:转而对 h[k]=v,继续循环
```

**核心差异点(对比 `__index`)**:

- **触发条件是「raw 槽不存在」(键当前为 nil)**。键已存在(已有非 nil 值)⇒ **直接 rawset 改值,绝不触发 `__newindex`**。这是 `__newindex` 用于「只拦截新键写入」的关键(代理表、只读表、默认值表)。05 §6.4 的快路径正是「IC 命中已存在槽 → 直接写」,**只有写不存在的键才进本慢路径**。
- **`__newindex` 是 table**:转去 `h[k]=v`——同样会再走完整逻辑(`h` 自己可能有 `__newindex`)。注意此时**对 `h` 的写若 `h` 中该键也不存在,会继续往 `h` 的 `__newindex` 走**(可链)。
- **`__newindex` 是 function**:调 `__newindex(t,k,v)`,**无返回值**,终止。

### 4.2 `newindexMeta` 实现

```go
// 承 05 §6.4:SETTABLE/SETGLOBAL 的慢路径(raw 槽不存在或非 table)。
func (vm *VM) newindexMeta(f *frame, t value.Value, key value.Value, val value.Value) *LuaError {
    for loopGuard := 0; ; loopGuard++ {
        var h value.Value
        if value.Tag(t) == value.TagTable {
            tbl := object.TableAt(vm.arena, value.GCRefOf(t))
            if _, exists := tbl.RawGetOK(key); exists {       // 键已存在(非 nil)
                if e := vm.checkTableKey(key); e != nil { return e } // nil/NaN 键检查见下
                tbl.RawSet(key, val)                           // 直接改值,不触发 __newindex
                vm.maybeBumpGenOnRawSet(tbl)                   // rehash 才 bump(05 §6.5)
                return nil
            }
            mt := vm.getMetatable(t)
            if mt != 0 {
                h = object.TableAt(vm.arena, mt).RawGet(vm.eventKey(EvNewIndex))
            }
            if mt == 0 || h == value.Nil {
                // 无元表 或 有元表无 __newindex ⇒ raw 插入新键
                if e := vm.checkTableKey(key); e != nil { return e }
                tbl.RawSet(key, val)                           // 可能 rehash → bump gen(05 §6.5)
                vm.maybeBumpGenOnRawSet(tbl)
                reloadFrame(f)                                  // RawSet 可能分配/rehash → reload
                return nil
            }
        } else {
            mt := vm.getMetatable(t)
            if mt != 0 {
                h = object.TableAt(vm.arena, mt).RawGet(vm.eventKey(EvNewIndex))
            }
            if mt == 0 || h == value.Nil {
                return vm.indexError(f, t, key)                 // "attempt to index a <type> value"
            }
        }
        if vm.isCallable(h) {
            return vm.callMM(f, h, []value.Value{t, key, val}, 0 /*无返回*/, 0)
        }
        t = h
        if loopGuard > maxIndexChain {
            return vm.errorf(f, "'__newindex' chain too long; possible loop")
        }
    }
}
```

### 4.3 table 键合法性检查(`nil`/`NaN` 键)

**仅在「真正 rawset 进表」时检查**(承 [01](./01-value-object-model.md) §5.2):

- **`nil` 键**:`t[nil]=v` 报 `"table index is nil"`。
- **`NaN` 键**:`t[0/0]=v` 报 `"table index is NaN"`。
- 这两个检查**只在 rawset 路径**(键已存在改值时键非 nil/NaN 已被保证;新键插入时检查)。**注意**:`t[nil]` 的**读**(`__index`)不报错(返回 nil);只有**写**报错。`__newindex` 是 function 时,`t[nil]=v` 是否报错?——5.1 语义:若走到 function 式 `__newindex`,**不检查键**(键交给元方法处理)。检查只在「即将 rawset」前做。`checkTableKey` 因此只在上面 rawset 分支前调,不在 function 分支前调(正确)。
- `-0.0` 键归一化为 `+0.0`([01](./01-value-object-model.md) §5.2),不是错误。

---

## 5. 算术元方法(ADD/SUB/MUL/DIV/MOD/POW 慢路径)

承 [05](./05-interpreter-loop.md) §4.1/§4.2:`doArith` 快路径是「双 number 直算」,否则调 `arithMeta`。本节定稿 `arithMeta`,**含字符串→数字 coercion 前置**。

### 5.1 Lua 5.1 算术语义:coercion 优先,再元方法

`a op b`(op ∈ +/-/*///%/^)的完整顺序:

```
arith(a, b, op):
  // 1. coercion 前置:尝试把 a、b 各自转 number(仅 number 与「可转数字的 string」成功)
  na, oka := toNumber(a)               // §5.2 coercion 规则
  nb, okb := toNumber(b)
  if oka && okb:
    return na op nb                     // 双方都(经转换后)是 number ⇒ 直算,**不查元方法**
  // 2. 否则查元方法:先 a 的 metatable[event],再 b 的
  mm := getMetamethod(a, event)         // event = __add/__sub/...
  if mm == nil:
    mm = getMetamethod(b, event)
  if mm == nil:
    报错 "attempt to perform arithmetic on a <type> value"  // 用「非数字那个操作数」的类型(§14)
  return call mm(a, b)                   // 首返回值为结果
```

**关键顺序(5.1 语义,易错)**:

- **coercion 在元方法之前**。`"10" + 5` 中 `"10"` 可转 5 ⇒ 直接 `10+5=15`,**即便 string 有 `__add` 元方法也不调**(因为 coercion 成功了)。这是 5.1 的「字符串算术自动转数字」。
- **只要双方 coercion 后都是 number,就不进元方法**。只有「至少一方既非 number 也非可转数字 string」才查元方法。
- **查找顺序:先左(a)后右(b)**。`a` 有 `__add` 用 `a` 的;`a` 没有才看 `b` 的。两者都没 ⇒ 报错。
- **报错的类型名取「肇事操作数」**:取**第一个非数字(且非可转 string)的操作数**的类型。`{} + 1` 报 `"attempt to perform arithmetic on a table value"`(`{}` 是肇事者)。**待 12 差分核对**精确选取规则(5.1 取第一个非数字操作数)。

### 5.2 字符串→数字 coercion 规则(归属本文,与 tonumber 共用一套)

**coercion 归属定稿**:算术(本节)、数值 for(05 §10.1)、`tonumber`([10](./10-stdlib.md))**共用同一套** `toNumber` 规则。本文定义该规则,05 §13 缺口与 10 的 `tonumber` 以此为准:

```go
// 把 Value 转 number(用于算术 coercion / for / tonumber 无 base 形式)。
// 返回 (转换后数字, 是否成功)。number 直接成功;string 走 Lua 数字字面量语法子集;其它失败。
func toNumber(v value.Value) (float64, bool) {
    if value.IsNumber(v) {
        return value.AsNumber(v), true
    }
    if value.Tag(v) == value.TagString {
        return parseLuaNumber(stringBytes(v))    // §5.2 字面量解析
    }
    return 0, false                               // table/bool/nil/... 不可转
}
```

**`parseLuaNumber` 接受的语法(Lua 5.1 `luaO_str2d` / `strtod` 子集)**:

| 形式 | 例 | 说明 |
|---|---|---|
| 前后空白 | `" 10 "` → 10 | 允许前导/尾随空白(`isspace`),中间空白非法 |
| 十进制整数 | `"42"` → 42 | |
| 十进制小数 | `"3.14"`、`".5"`、`"5."` → | |
| 十进制指数 | `"1e3"`、`"1.5E-2"` → | `e`/`E` 指数 |
| 十六进制整数 | `"0x1A"`、`"0XFF"` → 26/255 | `0x`/`0X` 前缀;Lua 5.1 **不支持十六进制浮点**(`0x1p4` 是 5.2+,**排除**) |
| 符号 | `"-5"`、`"+3"` → | 可选前导符号 |
| 失败 | `"10x"`、`"0x"`、`""`、`"  "`、`"inf"`、`"nan"` → 失败 | 尾部有非法字符、空串、纯空白、`inf`/`nan` 字面量(5.1 不认)失败 |

**5.1 口径要点**:

- **十六进制浮点 `0x1.8p3`** 是 **5.2+**,P1 **不支持**(`parseLuaNumber` 对 `0x` 后只接受十六进制整数)。**待 12 差分核对**:5.1 的 `0x` 行为(`strtod` 在不同平台对 `0x` 浮点行为不一,Lua 5.1 官方 `lua_str2number` 用 `strtod`,但 5.1 词法/转换不承诺十六进制浮点)。**定稿:只支持十六进制整数**,与 5.1 最小语义一致。
- **转换结果走 `NumberValue`**([01](./01-value-object-model.md) §3.4):即便解析出 NaN/Inf(理论上 `parseLuaNumber` 不产 NaN,但指数溢出可产 Inf),也经 canonicalize,防负 NaN 渗入。
- **`-0.0`**:`"-0"` → `-0.0`(保留符号,Lua 语义)。
- **与 tonumber(s, base) 的区别**:`tonumber` 带 base 参数时走**另一条**(任意进制整数解析,不接受小数/指数),那是 `tonumber` 专属,**不属于算术 coercion**([10](./10-stdlib.md) 定义)。算术 coercion 只用上面的无 base 形式。

> **coercion 归属定稿**:`parseLuaNumber` 实现在 `internal/crescent`(或 `internal/object` 的数字工具),**算术/for/tonumber 三处共享同一函数**,保证三处对「什么字符串算数字」逐字节一致。这收口了 05 §13 与 §10.1 的「coercion 边界」缺口、以及 10 的 `tonumber` 行为。由 [12](./12-testing-difftest.md) 钉死(各种边界串)。

### 5.3 `arithMeta` 实现

```go
// 承 05 §4.2:ADD/SUB/MUL/DIV/MOD/POW 的慢路径(非双 number)。
// b,c 是已取好的 RK 操作数(05 §4.1 传入)。
func (vm *VM) arithMeta(f *frame, i Instruction, op arithOp, b, c value.Value) *LuaError {
    // 1. coercion 前置
    nb, okb := toNumber(b)
    nc, okc := toNumber(c)
    if okb && okc {
        r := applyArith(op, nb, nc)                 // 与 05 §4.1 快路径同一套 f64 运算
        f.stk[f.base+A(i)] = value.NumberValue(r)   // canonicalizeNaN
        return nil
    }
    // 2. 元方法:先左后右
    ev := arithEvent(op)                            // op → __add/__sub/...
    mm := vm.getMetamethod(b, vm.eventKey(ev))
    if mm == value.Nil {
        mm = vm.getMetamethod(c, vm.eventKey(ev))
    }
    if mm == value.Nil {
        // 报错:肇事者 = 第一个非数字(且非可转)操作数
        bad := b
        if okb { bad = c }                          // b 能转 ⇒ c 是肇事者
        return vm.arithError(f, bad)                 // "attempt to perform arithmetic on a <type> value"(§14)
    }
    // 3. 调元方法 mm(b, c),结果落 R(A)
    return vm.callMM(f, mm, []value.Value{b, c}, f.base+A(i), 1)
}
```

要点:

- **coercion 用原始 `b`/`c`**(coercion 不改变传给元方法的参数:若 coercion 失败走元方法,元方法收到的是**原始未转换**的 `b`/`c`,如原始 string,不是转换尝试的中间值)。
- **`applyArith` 与快路径共用**(05 §4.1 的 `switch op`),包括 `MOD` 的 `a - floor(a/b)*b`、`POW` 的 `math.Pow`、`DIV` 的 `/0=±Inf`——coercion 成功后走与快路径**逐字节一致**的运算,保证 `"10"/0` 与 `10/0` 同结果。
- **元方法调用后 reload**(`callMM` 已含)。

---

## 6. 一元 `__unm`(UNM 慢路径)

承 [05](./05-interpreter-loop.md) §4.3:`UNM`(`R(A):=-R(B)`)快路径是「R(B) 是 number → `-AsNumber`」,否则 `__unm`。

```go
// 承 05 §4.3:UNM 非 number 慢路径。
func (vm *VM) unmMeta(f *frame, i Instruction, a value.Value) *LuaError {
    // coercion 前置(与算术一致:"−"对可转字符串也先转)
    if n, ok := toNumber(a); ok {
        f.stk[f.base+A(i)] = value.NumberValue(-n)  // 注意 -0.0
        return nil
    }
    mm := vm.getMetamethod(a, vm.eventKey(EvUnm))
    if mm == value.Nil {
        return vm.arithError(f, a)                   // "attempt to perform arithmetic on a <type> value"
    }
    // **Lua 5.1 特殊**:__unm 调用传 (a, a) —— 操作数传两次(C 实现 luaV_arith 对一元也传两参占位)
    return vm.callMM(f, mm, []value.Value{a, a}, f.base+A(i), 1)
}
```

**5.1 口径**:

- **coercion 同样前置**:`-"5"` → `-5`(可转字符串先转再取负,不查 `__unm`)。
- **`__unm` 的 arity 是 `(a, a)`**:Lua 5.1 `luaV_arith` 对一元运算也按二元接口传参,**第二个参数重复第一个**(占位)。多数 `__unm` 实现只用第一个参数,但差分一致要求**传两次**(若用户的 `__unm` 检查第二参数会观察到)。**待 12 差分核对**此细节(5.1 `lvm.c` 的 `call_binTM(L, p1, p2, res, event)` 对 unm 传 `p1==p2==操作数`)。
- 报错用 `arithError`(与二元算术同措辞)。

---

## 7. `__len`(LEN 慢路径,5.1 仅 userdata)

承 [05](./05-interpreter-loop.md) §4.3 与 [02](./02-bytecode-isa.md) §4-20 注:**Lua 5.1 的 `__len` 仅对 userdata 生效**。`LEN`(`R(A):=#R(B)`)执行侧:

```
LEN on R(B):
  string   → 字节长(从 String word1 的 len 读,[01] §5.1),**不查 __len**
  table    → border(# 二分,[01] §5.2),**不查 __len**(table 的 __len 是 5.2+,排除!)
  userdata → 查 __len:有则调 __len(ud),返回其首值;无则报错 "attempt to get length of a userdata value"
  其它     → 报错 "attempt to get length of a <type> value"
```

`lenMeta`(仅 userdata 分支调用):

```go
// 承 05 §4.3:LEN on userdata。string/table 在 05 §4.3 已直接处理,不进此函数。
func (vm *VM) lenMeta(f *frame, i Instruction, a value.Value) *LuaError {
    // a 必是 userdata(05 §4.3 分派保证)
    mm := vm.getMetamethod(a, vm.eventKey(EvLen))
    if mm == value.Nil {
        return vm.lenError(f, a)                     // "attempt to get length of a userdata value"
    }
    return vm.callMM(f, mm, []value.Value{a}, f.base+A(i), 1)  // __len(ud),arity 1
}
```

**为什么 table 不查 `__len`(反复强调)**:这是 5.1 vs 5.2 最易踩的差异。5.2 起 `#t` 对有 `__len` 的表调元方法;**5.1 对 table 的 `#` 永远取 border,无视 `__len`**。P1 在 05 §4.3 的 LEN 分派中,**table 分支直接 border,根本不调 `lenMeta`**——`lenMeta` 只被 userdata 分支调用。若误让 table 走 `lenMeta`,带 `__len` 的表会与官方 5.1 差分失败(roadmap §6 锁 5.1)。

> **arity 注意**:5.1 `__len` 传 `(a)` 单参(不像 `__unm` 传两次)?——Lua 5.1 `luaV_len`?**5.1 实际无 `luaV_len`**(`__len` on userdata 经 `luaL_*`/手动);5.2 才有 `luaV_objlen`。**待 12 差分核对** 5.1 userdata `__len` 的精确 arity。本文定 `(a)` 单参(最自然),由差分钉死。

---

## 8. `__concat`(CONCAT 慢路径,右结合)

承 [05](./05-interpreter-loop.md) §4.6:`CONCAT`(`R(A):=R(B)..R(B+1)..…..R(C)`)**右结合**。快路径是「全 string/number 一次线性拼接」,慢路径遇非 string/number 操作数时**从右向左两两折叠**,每对触发 `__concat`。

### 8.1 Lua 5.1 `..` 语义

`a .. b`(单对)的求值(`luaV_concat` 等价):

```
concatPair(a, b):
  if (a 是 string 或 number) and (b 是 string 或 number):
    return tostring(a) .. tostring(b)        // 直接拼(number 用 %.14g 格式,05 §4.6),不查元方法
  mm := getMetamethod(a, __concat)
  if mm == nil:
    mm = getMetamethod(b, __concat)
  if mm == nil:
    报错 "attempt to concatenate a <type> value"  // 肇事者 = 第一个非 string/number 操作数
  return call mm(a, b)
```

**右结合体现在多操作数折叠顺序**。`a..b..c` 在有元方法时等价 `a..(b..c)`:

```
concat R(B..C):  // 一段连续寄存器
  从右向左:先 (R(C-1) .. R(C)) → 得 r1
            再 (R(C-2) .. r1)   → 得 r2
            ... 直到 (R(B) .. ...)
  每步 concatPair,可能触发 __concat。
```

### 8.2 `concatMeta` 与折叠实现

```go
// 本函数是 05 §4.6 `doConcat` 骨架的**完整定稿**(05 给伪码框架,慢路径 __concat 折叠下放本文,
// 关系同 doGetTable↔indexMeta:05 是执行入口,07 定稿其元方法分支)。
// 快路径(全 string/number 一次线性拼)与慢路径(从右向左折叠,每对可能触发 __concat)合并于此。
func (vm *VM) doConcat(f *frame, i Instruction) *LuaError {
    b, c := B(i), C(i)
    // 尝试快路径:扫 [R(B)..R(C)] 是否全 string/number(05 §4.6 优化,一次线性拼)
    if vm.allStringOrNumber(f, b, c) {
        return vm.concatFast(f, A(i), b, c)         // 全 string/number:总长 → 一次分配 → intern
    }
    // 慢路径:从右向左两两折叠
    // 累加器 acc 从最右操作数起;每步与左邻 concatPair
    acc := f.stk[f.base+c]
    for k := c - 1; k >= b; k-- {
        left := f.stk[f.base+k]
        r, e := vm.concatPair(f, left, acc)
        if e != nil { return e }
        acc = r
        reloadFrame(f)                               // concatPair 可能调元方法/分配 → reload
    }
    f.stk[f.base+A(i)] = acc
    return nil
}

func (vm *VM) concatPair(f *frame, a, b value.Value) (value.Value, *LuaError) {
    if isStringOrNumber(a) && isStringOrNumber(b) {
        s := vm.rawConcat(a, b)                       // number→%.14g,拼接,intern([01] §5.1)
        return s, nil
    }
    mm := vm.getMetamethod(a, vm.eventKey(EvConcat))
    if mm == value.Nil {
        mm = vm.getMetamethod(b, vm.eventKey(EvConcat))
    }
    if mm == value.Nil {
        bad := a
        if isStringOrNumber(a) { bad = b }
        return value.Nil, vm.concatError(f, bad)      // "attempt to concatenate a <type> value"(§14)
    }
    // 调 __concat(a, b),取首返回值。需临时落点:用 top 之上临时槽。
    var out value.Value
    e := vm.callMMInto(f, mm, []value.Value{a, b}, &out, 1)
    return out, e
}
```

要点:

- **右结合的可观察性**:仅当**有元方法时**结合顺序可观察(元方法被调用的顺序、中间结果)。**全 string/number 的快路径无结合性差异**(纯拼接,先拼哪两个结果一样),所以 05 §4.6 的「一次线性拼」是合法优化。
- **触发顺序**:`a..b..c`(b,c 非平凡)先合最右 `(b..c)`,再合 `a..(结果)`。这与 5.1 一致。
- **每对折叠后 reload**:`concatPair` 命中元方法会调用 → 可能分配/GC/扩容 → 折叠循环每步后 `reloadFrame`(05 §1.3)。
- **CONCAT 是 safepoint**(05 §5.2):分配新 String。快路径一次分配;慢路径每对 `rawConcat`/元方法都可能分配。
- **number→string 格式 `%.14g`**(05 §4.6):与差分基准逐字节一致,见 [12](./12-testing-difftest.md)。

---

## 9. 比较元方法 `__eq` / `__lt` / `__le`

承 [05](./05-interpreter-loop.md) §4.4:`EQ`/`LT`/`LE` 与紧随 JMP 配对。快路径见 05 §4.4(`rawequal`、双 number、双 string)。本节定稿三个比较元方法,**含 5.1 特有的 `__le→__lt` 回退**。

### 9.1 `__eq`:严格的触发条件

**Lua 5.1 `__eq` 触发条件(极严格,易错)**:

```
equal(a, b):
  if rawequal(a, b): return true             // [01] §6:number 浮点比、string GCRef 比、其它 bits 比
  // __eq 仅在「a、b 同为 table 或同为 userdata」时才查(5.1 要求同 primitive type)
  if not ((a 是 table and b 是 table) or (a 是 userdata and b 是 userdata)):
    return false                              // 类型不同,或非 table/userdata ⇒ rawequal 假即最终 false
  // 取 __eq:先 a 的,a 没有再 b 的
  mm := getMetamethod(a, __eq)
  if mm == nil:
    mm = getMetamethod(b, __eq)
  if mm == nil:
    return false                              // 都没 __eq ⇒ false
  return truthy(call mm(a, b))                // 结果取真值(转 bool)
```

**5.1 口径要点**:

- **只有「同为 table」或「同为 userdata」才查 `__eq`**。`1 == "1"` ⇒ 类型不同 ⇒ **直接 false,不查元方法**(number vs string 永不相等,也不调 `__eq`)。`{} == 5` ⇒ table vs number ⇒ false。**只有 `tableA == tableB`(两个不同 table 对象)或 `udA == udB` 才可能调 `__eq`**。
- **rawequal 为真直接返回 true**(同一对象 `a==a` 不调 `__eq`)。`__eq` 只在「两个**不同**的同类对象」间被查。
- **查找顺序先 a 后 b**;都没有则 false。
- **结果取真值**(`truthy`,[01](./01-value-object-model.md) §6):`__eq` 返回任意值,非 nil/false 即视为相等。
- **`EQ` 指令的 `bool(A)`**:05 §4.4 的 `比较结果 ≠ bool(A) ⇒ pc++`。`equalMeta` 返回 bool 结果,由 05 的 EQ case 套用 `bool(A)` 逻辑。

```go
// 承 05 §4.4:EQ 在 rawequal 为假且同为 table/userdata 时调。返回布尔(供 05 套 bool(A))。
func (vm *VM) equalMeta(f *frame, a, b value.Value) (bool, *LuaError) {
    // 调用方(05 EQ case)已确认:rawequal 假 且 (双 table 或 双 userdata)
    mm := vm.getMetamethod(a, vm.eventKey(EvEq))
    if mm == value.Nil {
        mm = vm.getMetamethod(b, vm.eventKey(EvEq))
    }
    if mm == value.Nil {
        return false, nil                       // 无 __eq ⇒ 不等
    }
    var out value.Value
    if e := vm.callMMInto(f, mm, []value.Value{a, b}, &out, 1); e != nil {
        return false, e
    }
    return value.Truthy(out), nil               // 真值
}
```

### 9.2 `__lt`:小于

```
lessThan(a, b):
  if a 是 number and b 是 number: return a < b   // 快路径(05 §4.4),IEEE(NaN 全 false)
  if a 是 string and b 是 string: return a <字典序 b
  // 混合类型:**不 coerce**(与算术不同!),直接查 __lt
  mm := getMetamethod(a, __lt)
  if mm == nil:
    mm = getMetamethod(b, __lt)
  if mm == nil:
    报错 (见 §9.4 比较错误)
  return truthy(call mm(a, b))
```

```go
// 承 05 §4.4:LT 快路径失败的慢路径(lessThan 是 05 点名的函数)。
func (vm *VM) lessThan(f *frame, a, b value.Value) (bool, *LuaError) {
    // 快路径(双 number / 双 string)由 05 §4.4 内联;此处是慢路径
    mm := vm.getMetamethod(a, vm.eventKey(EvLt))
    if mm == value.Nil {
        mm = vm.getMetamethod(b, vm.eventKey(EvLt))
    }
    if mm == value.Nil {
        return false, vm.compareError(f, a, b)  // §9.4
    }
    var out value.Value
    if e := vm.callMMInto(f, mm, []value.Value{a, b}, &out, 1); e != nil {
        return false, e
    }
    return value.Truthy(out), nil
}
```

**`a > b` 已被 codegen 转成 `b < a`**(04 codegen / 02 §4 记号):`LT` 指令只表达「小于」,源码的 `>` 在编译期交换操作数变成 `<`。所以解释器只需 `lessThan`,无需 `greaterThan`。

### 9.3 `__le`:小于等于 —— **Lua 5.1 的 `__le→__lt` 回退(关键!)**

**这是 5.1 与 5.4 的核心差异之一,必须写明**:

```
lessEqual(a, b):                              // Lua 5.1 语义
  if a 是 number and b 是 number: return a <= b
  if a 是 string and b 是 string: return a <=字典序 b
  // 混合:不 coerce,先查 __le
  mm := getMetamethod(a, __le)
  if mm == nil:
    mm = getMetamethod(b, __le)
  if mm != nil:
    return truthy(call mm(a, b))              // 有 __le:直接用
  // **5.1 特殊回退**:无 __le 但有 __lt ⇒ 用 not (b < a) 模拟 a <= b
  mm = getMetamethod(a, __lt)
  if mm == nil:
    mm = getMetamethod(b, __lt)
  if mm != nil:
    return not truthy(call mm(b, a))          // 注意:调 __lt(b, a),取反!
  报错 (见 §9.4)
```

```go
// 承 05 §4.4:LE 快路径失败的慢路径。**实现 5.1 的 __le→__lt 回退**。
func (vm *VM) lessEqual(f *frame, a, b value.Value) (bool, *LuaError) {
    // 1. 先找 __le
    mm := vm.getMetamethod(a, vm.eventKey(EvLe))
    if mm == value.Nil {
        mm = vm.getMetamethod(b, vm.eventKey(EvLe))
    }
    if mm != value.Nil {
        var out value.Value
        if e := vm.callMMInto(f, mm, []value.Value{a, b}, &out, 1); e != nil {
            return false, e
        }
        return value.Truthy(out), nil
    }
    // 2. **5.1 回退**:无 __le 但有 __lt ⇒ a<=b 等价 not(b<a),调 __lt(b, a) 取反
    mm = vm.getMetamethod(a, vm.eventKey(EvLt))
    if mm == value.Nil {
        mm = vm.getMetamethod(b, vm.eventKey(EvLt))
    }
    if mm != value.Nil {
        var out value.Value
        if e := vm.callMMInto(f, mm, []value.Value{b, a}, &out, 1); e != nil { // 注意 (b, a) 反序!
            return false, e
        }
        return !value.Truthy(out), nil          // 取反
    }
    // 3. 都没有 ⇒ 报错
    return false, vm.compareError(f, a, b)
}
```

**为什么这个回退必须写明且必须实现**:

- **Lua 5.1 有此回退,Lua 5.4 删除了它**(5.4 起 `__le` 必须显式定义,无 `__lt` 兜底)。P1 锁 5.1(roadmap §6),**必须实现回退**,否则「定义了 `__lt` 但没定义 `__le` 的对象做 `<=` 比较」会:5.1 成功(回退),P1 若不实现则报错 ⇒ 与官方 5.1 差分失败。
- **回退细节易错两处**:① 调的是 `__lt(b, a)`(**操作数反序**),不是 `__lt(a, b)`;② 结果**取反**(`a<=b ⟺ not(b<a)`)。这两个一起才是 `a<=b` 的正确模拟。
- **`a >= b` 转 `b <= a`**(codegen,类似 `>`→`<`):`LE` 只表达「小于等于」,源码 `>=` 交换操作数。所以解释器只需 `lessEqual`。

> **doc-gap / 待 12 差分核对**:5.1 `__le` 回退时 `__lt` 的查找顺序(回退里再次「先 a 后 b」找 `__lt`)与官方 `lvm.c` `lessequal` 的精确实现对齐,由 [12](./12-testing-difftest.md) 核对。本文按「先 a 后 b 找 `__lt`,调 `__lt(b,a)` 取反」实现。

### 9.4 比较错误措辞(混合类型且无元方法)

承 [05](./05-interpreter-loop.md) §4.4:**number vs string 比较不 coerce**(与算术不同),直接查元方法,没有则报错。错误措辞分两种(Lua 5.1):

```
compareError(a, b):
  if type(a) == type(b):
    报 "attempt to compare two <type> values"     // 同类型(如两个 table 都无 __lt)
  else:
    报 "attempt to compare <type-a> with <type-b>" // 不同类型(如 number 与 string)
```

- `1 < "2"` ⇒ number 与 string 不同类型且都无 `__lt` ⇒ `"attempt to compare number with string"`。
- `{} < {}`(都无 `__lt`)⇒ 同类型 ⇒ `"attempt to compare two table values"`。
- **此处是高频差分易错点**(05 §4.4 已标「易错,差分测试重点覆盖」):number/string 混合**绝不自动转**,与算术的 coercion 形成鲜明对比。**待 12 差分核对**两种措辞的精确格式。

---

## 10. `__call`(CALL/TAILCALL 慢路径)

承 [05](./05-interpreter-loop.md) §7.1:`doCall` 遇到 `R(A)` 不是 Lua closure 也不是 host closure 时,调 `callMeta`。`doTailCall`(05 §7.5)同理。

### 10.1 Lua 5.1 `__call` 语义

```
对 callee(args...) 当 callee 不可调用:
  mm := getMetamethod(callee, __call)
  if mm == nil:
    报错 "attempt to call a <type> value"
  // 有 __call:以 (callee, args...) 调用 mm —— callee 作为第一个参数前插
  return mm(callee, arg1, arg2, ...)
```

**关键:`callee` 被插入参数列表最前**。`obj(x, y)` 当 `obj` 有 `__call` ⇒ 实际调 `getmetatable(obj).__call(obj, x, y)`。参数整体右移一位,`callee` 占首位。

### 10.2 `callMeta` 实现

```go
// 承 05 §7.1/§7.5:CALL/TAILCALL 的 callee 不可调用慢路径。
func (vm *VM) callMeta(f *frame, i Instruction, callee value.Value) callResult {
    mm := vm.getMetamethod(callee, vm.eventKey(EvCall))
    if mm == value.Nil {
        vm.pendingErr = vm.callError(f, callee)     // "attempt to call a <type> value"(§14)
        return callError
    }
    if !vm.isCallable(mm) {
        // __call 本身不可调用(罕见:__call 是个 table)⇒ Lua 5.1 会再查 mm 的 __call?
        // 5.1:对 __call 的值再走一次 callMeta(可链,但极罕见)。P1 用大上限兜底(同 §3.3)。
        // 实务:几乎总是 function,这里仅防御。
    }
    // 把 callee 前插到参数列表:原 R(A+1..) 整体后移一位,callee 落 R(A+1) 之前的位置,
    // mm 落 R(A)。即:R(A)=mm, R(A+1)=callee, R(A+2..)=原 args。
    vm.insertCalleeAsFirstArg(f, A(i), callee, mm)
    // 之后等价于一次正常 CALL:mm(callee, args...),参数个数 +1
    return vm.doCallResolved(f, A(i), mm, /*nargs+1*/)
}
```

要点:

- **`callee` 前插实现**:在栈上把 `R(A)` 设为元方法 `mm`,把原 `callee` 与原参数整体作为 `mm` 的参数(`callee` 在最前)。参数个数 +1。需在 `R(A)` 之上腾一个槽(可能 ensureStack)。
- **复用 `doCall` 的解析**:前插后,`mm` 可能是 Lua closure(reentry)或 host(同步)——走 05 §7.1 的正常分支(`callEnteredLua`/`callReturnedHost`)。
- **`TAILCALL` 的 `__call`**:05 §7.5 的 `doTailCall` default 分支也调 `callMeta`。尾调用语义:`return obj(x)` 当 `obj` 有 `__call` ⇒ 尾调用 `mm(obj, x)`(仍复用帧,栈不增长)。
- **`__call` 本身不可调用**:5.1 会对 `mm` 再查 `__call`(可链)。极罕见,P1 用大上限兜底(同 §3.3 的 `MAXTAGLOOP`),实务上 `__call` 几乎总是 function。**待 12 差分核对**深层 `__call` 链行为。
- **错误**:`nil()` / `(5)()` / `"x"()` ⇒ 无 `__call` ⇒ `"attempt to call a <type> value"`(类型名 + 变量名信息见 §14 / [09](./09-errors-pcall.md))。

---

## 11. `__tostring`(stdlib tostring/print 慢路径)

`__tostring` 不是 opcode 触发,是 base 库 `tostring(v)` 与 `print`(经 `tostring`)触发。语义本文定义,[10](./10-stdlib.md) 引用:

```
tostring(v):                                  // base 库 host function
  mt := getMetatable(v)
  if mt != 0:
    mm := rawget(mt, "__tostring")
    if mm != nil:
      r := call mm(v)                          // 调 __tostring(v)
      if type(r) != string:
        报错 "'__tostring' must return a string"  // 5.1:__tostring 必须返回 string(待 12 核对措辞)
      return r
  // 无 __tostring:走默认格式
  return defaultToString(v)
```

**默认格式 `defaultToString`(无 `__tostring` 时,Lua 5.1)**:

| 类型 | 格式 | 例 |
|---|---|---|
| nil | `"nil"` | |
| boolean | `"true"`/`"false"` | |
| number | `%.14g`([01](./01-value-object-model.md) / 05 §4.6 同一格式) | `tostring(0.1)` → `"0.1"` |
| string | 原值 | |
| table | `"table: 0x%08x"`(地址) | `"table: 0x004a..."` —— **地址可观察,差分需豁免**(见下) |
| function | `"function: 0x%08x"`(或 `"function: builtin: ..."`) | |
| thread | `"thread: 0x%08x"` | |
| userdata | `"userdata: 0x%08x"` | |

**地址格式的差分问题**:`tostring({})` 含对象地址,**与官方/gopher-lua 必然不同**(arena 偏移 vs C 指针)。[12](./12-testing-difftest.md) 必须对「含 `0x...` 地址的 tostring 输出」做豁免(脱敏后比较,或排除此类用例)。本文标注此为**可观察但不可逐字节比的项**(类似 06 §11 的 `pairs` 序口径问题)。

> **`__tostring` 返回非 string**:5.1 报错(`tostring` 要求元方法返回字符串)。5.4 放宽(允许返回任意值再转)。P1 按 5.1 严格(返回非 string 报错)。**待 12 差分核对**。

---

## 12. `__metatable`(保护)

已在 §1.3 定义。归纳其双重语义(非「调用型」事件,是元表里的一个**字段**):

- **`getmetatable(v)`**:若 `v` 的元表有 `__metatable` 字段 ⇒ 返回**该字段的值**(隐藏真元表)。
- **`setmetatable(t, mt)`**:若 `t` **当前**元表有 `__metatable` 字段 ⇒ **报错** `"cannot change a protected metatable"`(拒绝改元表)。
- `debug.setmetatable` **绕过**保护(后门)。

用途:库作者锁死对象元表,防脚本篡改(如 `string` 库可保护 string 元表)。

---

## 13. `__mode` 弱表(GC 协作 —— 本文定义语义,向 06 提回填请求)

`__mode` 是元表里的**字段**(值是字符串 `"k"`/`"v"`/`"kv"`),把一个 table 标记为**弱表**:其键和/或值是弱引用,不阻止被引用对象被 GC 回收,回收后对应 entry 从表移除。**这是 GC 可观察行为**(06 §11 已点名「弱表 `__mode` 的 cleartable 是可观察行为」,但 06 §9.2 只提了一句未展开)。本节定义语义,GC 协作细节请 06 回填(§13.4)。

### 13.1 Lua 5.1 `__mode` 语义

```
table t 的元表有 __mode 字段(字符串):
  含 'k' ⇒ t 的**键**是弱引用(weak key)
  含 'v' ⇒ t 的**值**是弱引用(weak value)
  "kv" 或 "vk" ⇒ 键值都弱(全弱表)
其它字符(或无 __mode)⇒ 强表(正常)
```

**弱引用语义**:

- **weak value**(`"v"`):若某 entry 的**值**所指对象**只被弱表引用**(无其它强引用),GC 可回收该值对象,并**从弱表移除该 entry**(键值一起删)。典型用途:缓存(值是可重建的缓存对象)。
- **weak key**(`"k"`):若某 entry 的**键**所指对象只被弱表引用,GC 回收键对象并移除 entry。典型用途:对象属性表(键是对象,对象消失则属性自动清)。
- **weak kv**(`"kv"`):键**或**值任一被回收 ⇒ 移除 entry。

**只有可回收类型(table/userdata/function/thread/string,tag ∈ [0xFFFB,0xFFFF])的键/值才受弱引用影响**。number/bool/nil/lightuserdata 是**值类型,不可回收**,作为弱键/弱值时**永不被「回收」**(它们一直「存活」,entry 不因它们被移除)。Lua 5.1 语义:弱表中以「不可回收值」为键/值的 entry 不会因弱性被清(除非另一侧弱且被回收)。

### 13.2 `__mode` 何时读取、弱性何时确定

- **`__mode` 在 GC 的 mark 阶段读取**:GC 标记一个 table 时,查其元表 `__mode`(`rawget(mt, "__mode")`)决定它是否弱表、哪侧弱。
- **改 `__mode` 的时机**:Lua 5.1 允许运行期改 `__mode`(改元表的 `__mode` 字段)。但**弱性在「该表被 GC 标记的那一刻」确定**(读当时的 `__mode`)。中途改 `__mode` 在下一轮 GC 生效。**P1 STW** 下每轮 GC 开始时各表的 `__mode` 是确定的,无并发问题。
- **`__mode` 是字符串值**:`rawget(mt, "__mode")` 得一个 string,检查是否含 `'k'`/`'v'` 字节。

### 13.3 弱表的 GC 协作流程(本文定义,06 实现)

弱表打破了「mark 阶段顺着引用标黑」的常规:**弱引用的键/值在 mark 阶段不作为标记源**(不因为弱表引用它就标活),否则弱引用就和强引用一样了。流程(对齐 Lua 5.1 `lgc.c` 的 `traversetable` + `cleartable` + `atomic`):

```
mark 阶段(06 §5)遇到一个 table:
  mode := 读其元表 __mode(无元表/无 __mode ⇒ 强表,按 06 §5.2 正常扫 array/node 的 key+val+metaRef)
  if 强表:
    正常标记所有 key、val、metaRef(06 §5.2 现状)
  else:  // 弱表
    始终标记 metaRef(元表本身是强引用)
    if weak key 且 weak value("kv"):
      **key、val 都不标记**(都是弱);把该 table 登记到 GC 的 weaklist
    elif weak value("v"):
      **标记所有 key(键是强),不标记 val**;登记 weaklist
    elif weak key("k"):
      **标记所有 val(值是强),不标记 key**;登记 weaklist
    // 注:weak key 表里,值的存活还依赖键存活(见 cleartable);
    //     5.1 的 ephemeron 语义 P1 简化,见 §13.5

cleartable 阶段(mark 完成后、sweep 前,原子):
  for each table in weaklist:
    for each entry (k, v) in table:
      dead_k := isCollectable(k) && colorOf(k) == deadWhite  // 键所指对象本轮将被回收
      dead_v := isCollectable(v) && colorOf(v) == deadWhite
      if (weak_key && dead_k) || (weak_value && dead_v):
        从 table 移除该 entry(rawset k → nil,或标记槽为死)
        // 移除可能影响 pairs 序 → 可观察(06 §11)

sweep 阶段(06 §8):
  正常回收所有死白对象(此时弱表已 cleartable,死键/死值对象无强引用,被回收)
```

### 13.4 对 06 的回填请求(本文定义语义,06 实现 GC 协作)

**本文定义弱表的语义,但 GC 协作的具体完成需 06 回填**(06 §9.2 只说了一句「Lua 弱表 `__mode` 是 mark 时跳过弱引用、原子阶段 cleartable」,未展开)。请 06 增补:

1. **`Collector.weakList`**:GC 增一个弱表登记列表(类比 Lua `g->weak`)。mark 阶段(06 §5.3 `scanObject` 处理 Table 时)发现弱表 ⇒ 不按常规标记弱侧 ⇒ 把该 table GCRef 登记到 `weakList`。**对 06 §5.2 的修改**:Table 的扫描从「无条件扫 key+val+metaRef」改为「先查 `__mode`,弱侧不扫并登记 weakList」。
2. **`cleartable` 阶段**(06 §8.2 GC 主流程,mark 后 sweep 前,原子):遍历 `weakList`,对每个弱表遍历 entry,移除「弱侧对象本轮死白」的 entry。这是 06 §11 所说「弱表 cleartable 是可观察行为」的实现点。**对 06 §8.2 主流程的修改**:在 mark 与 sweep 之间插入 `cleartable()` 步骤(也是 `separateFinalizers` 之后,因为 finalizer 复活的对象不应被弱表 cleartable 误清——**顺序:mark → separateFinalizers(复活)→ cleartable(清弱表死 entry)→ sweep → runFinalizers**,待 06 确认顺序)。
3. **mark 算法**(06 §5.3)的 `scanObject(Table)` 分支需读 `__mode`:这要求 mark 能访问 table 的元表与其 `__mode` 字段(`rawget`)。本文的 `getMetamethod`/`getMetatable` 是 crescent 侧;06 的 mark 在 gc 侧——**需把 `__mode` 读取下沉为 object 侧 helper**(`object.Table.WeakMode() uint8`),供 gc 与 crescent 共用,不跨包依赖 crescent。

记入文档缺口(§15)与对 06 的回填请求。

### 13.5 P1 范围裁剪:ephemeron 简化

**Lua 5.1 的 weak key 表有 ephemeron 语义微妙处**:weak-key 表中,一个 entry 的**值**的存活应「依赖键的存活」(若键活,值才需标活;键死则值可回收)。完整 ephemeron 需要迭代式标记(Lua 5.2 引入正式 ephemeron 算法;5.1 的处理较简单但仍有「值依赖键」的考量)。

**P1 定稿(简化,标差分风险)**:

- P1 weak value:标记 key、不标记 val,cleartable 清死值 entry —— **完整正确**。
- P1 weak key:标记 val、不标记 key,cleartable 清死键 entry。**简化点**:把「值无条件标活」(只要 entry 还在且键活就标值)。这对「值强引用键」的循环弱表场景可能与 5.1 ephemeron 行为有微差。
- **多数真实负载**:弱表用于缓存(weak value)与对象属性(weak key,值是简单数据),P1 简化覆盖绝大多数。**ephemeron 完整语义 P1 不做,记缺口**(嵌入式宿主罕见依赖,roadmap §5 原则 4「不可分析形状走 fallback,不做完备性」)。**待 12 差分核对**:若差分基准含 ephemeron 循环用例,标记为已知差异。

> **doc-gap**:weak key 表的 ephemeron 完整语义(值的存活依赖键的存活)P1 简化为「键活则值无条件标活」。与 Lua 5.1 的精确差异需 [12](./12-testing-difftest.md) 用例核对;若触及,记为已知 P1 限制。

---

## 14. 元方法调用对解释器的影响(副作用面)

本节单列元方法对 05 主循环的**副作用纪律**,这是本文与 05 耦合最深处。

### 14.1 元方法可能 reentry / 调 host

元方法 `mm` 的两种身份(05 §7):

- **Lua closure**:`callMM` → `callFixed` → `enterLuaFrame`(05 §1.4)→ **reentry 子循环**(05 §7.1,不递归 Go 栈,压 CallInfo)。元方法体是 Lua 字节码,在同一 `execute` Go 帧里继续跑。
- **host function**:`callMM` → 同步 Go 调用(05 §7.5)。元方法是宿主/stdlib 的 Go 函数(如 `__index` 是 host 写的代理逻辑)。

**两种都可能**:① 分配(造新对象)→ 触发 GC;② 扩容值栈(`ensureStack`);③ arena 搬迁(扩容/GC compaction,虽 P1 不 compact)。

### 14.2 调用后必须 reload stk(贯穿纪律)

**所有调用元方法的 helper 在 `callMM`/`callMMInto`/`callFixed` 返回后,必须 `reloadFrame(f)`**(05 §1.3)。本文每个慢路径已标注。原因:元方法执行可能让 `f.stk`(arena 值栈的 Go slice 别名)失效——栈扩容或 GC 搬迁后旧 slice 悬垂(05 §1.5)。

**反例(必须避免)**:`arithMeta` 取 `b, c` 后调元方法,**不能**在调用后用调用前算的 `f.base+A(i)` 直接写 `f.stk`——必须 `reload` 后再写。本文伪码里 `callMM` 内部已 `reloadFrame`,调用方拿到的 `f.stk` 是最新的。

### 14.3 查找本身不分配(性能纪律)

**元方法查找(`getMetatable` + `rawget`)零分配、不调用**:

- `getMetatable`:一次 `flags & 1` 位测(table/userdata)或一次数组索引(`typeMetatables`)。
- `getMetamethod`:加一次 `rawget`(哈希查找,事件名是 intern 串,key 比较是 GCRef 比较)。
- **无元表时(`flags bit0=0`)直接短路返回**——绝大多数算术/索引的慢路径其实是「无元方法 → 报错或返回 nil」,不调用任何东西。

这保证「快路径失败」的代价分两级:**有元方法 → 调用(贵,但冷);无元方法 → 查找即止(廉价)**。05 §6 的 IC 与本节的 `flags bit0` 快判共同把「无元方法」短路到接近零成本。

### 14.4 错误信息的变量名(交 09)

元方法报错(`indexError`/`arithError`/`callError`/`compareError`/...)的措辞含**类型名**(本文给措辞模板),但 Lua 5.1 的错误信息还含**变量名/字段名信息**(如 `attempt to call field 'foo' (a nil value)`)。这部分靠**调试信息**(`Proto.LineInfo` + 寄存器→变量名映射)在 [09-errors-pcall](./09-errors-pcall.md) 定稿。**本文只负责「类型名层」的措辞**,变量名增强由 09 在错误构造时附加。

| 本文 helper | 措辞模板(类型名层) | 09 增强 |
|---|---|---|
| `indexError` | `attempt to index a <type> value` | `(field 'x')` / `(global 'x')` / `(local 'x')` |
| `arithError` | `attempt to perform arithmetic on a <type> value` | `(field 'x')` 等 |
| `concatError` | `attempt to concatenate a <type> value` | 同上 |
| `lenError` | `attempt to get length of a <type> value` | 同上 |
| `callError` | `attempt to call a <type> value` | `(global 'f')` / `(method 'm')` 等 |
| `compareError` | `attempt to compare two <type> values` / `attempt to compare <ta> with <tb>` | 一般无变量名 |

> **待 12 差分核对**:所有措辞的精确格式(冠词 `a`/`an`、复数、标点)以 Lua 5.1 参考实现为准,由 [12](./12-testing-difftest.md) 钉死。本文给的是骨架,**不编造精确标点**。

---

## 15. 不变式清单(实现与差分须守)

1. **查找用 rawget**:元方法事件名永远 `rawget` 元表,**绝不**触发 `__index`(防无限递归)。事件名预 intern,key 比较是 GCRef 比较。
2. **查找零分配**:`getMetatable`/`getMetamethod` 不分配、不调用;`flags bit0` 无元表快判短路。调用元方法才分配。
3. **调用后 reload**:任何元方法调用(`callMM` 等)返回后必 `reloadFrame`(05 §1.3),不得用调用前的 `f.stk`。
4. **改 metatable bump gen**:`setmetatable`/`debug.setmetatable` 改 table 元表后**递增该表 `gen`**(05 §6.1/§6.5),失效缓存其元方法查找的 IC。
5. **coercion 在算术、不在比较**:算术(+/-/...)与一元 `-` 对可转 string 先 coerce 再查元方法;**比较(`<`/`<=`)与 `==` 绝不 coerce**(number vs string 直接走元方法或 false/报错)。三处 coercion(算术/for/tonumber)共用 `parseLuaNumber`。
6. **`__eq` 严格触发**:仅「同为 table」或「同为 userdata」且 `rawequal` 假才查 `__eq`;类型不同或非 table/userdata ⇒ 直接 false。
7. **`__le→__lt` 回退(5.1)**:无 `__le` 但有 `__lt` ⇒ `a<=b` 用 `not __lt(b,a)`(反序 + 取反)。5.4 删此回退,**P1 必须有**。
8. **`__len` 仅 userdata**:table 的 `#` 取 border 不查 `__len`(5.2+ 才查);string 取字节长。LEN 的 table/string 分支根本不进 `lenMeta`。
9. **`__call` 前插 callee**:`callee(args)` 经 `__call` ⇒ `mm(callee, args...)`,callee 占首参。
10. **5.2+ 元方法主动不实现**:table 的 `__len`/`__gc`、`__pairs`/`__ipairs`、`__idiv`/位运算元方法、`__close`、`__name` 在对应 opcode/场景显式「不查元表」(§2.3),保证与官方 5.1 差分一致。
11. **`__metatable` 双保护**:`getmetatable` 返回其值(隐藏真元表);`setmetatable` 遇之报错(拒绝改);`debug.setmetatable` 绕过。
12. **弱表 cleartable 可观察**:`__mode` 弱表在 GC cleartable 移除死 entry,影响 `pairs` 序(可观察,差分需处理,06 §11)。

---

## 16. 文档缺口 / 待决(记入 memory/doc-gaps)

- **错误措辞精确格式**:§14 所有 `*Error` helper 的措辞(冠词/复数/标点/变量名增强)以 Lua 5.1 参考实现为准,**待 12 差分核对**。本文给骨架,不编造精确标点。变量名层(`field 'x'` 等)在 [09](./09-errors-pcall.md) 定稿。
- **`maxIndexChain` 值**:§3.3 `__index`/`__newindex`/`__call` 链的上限,**倾向对齐 Lua 5.1 `MAXTAGLOOP=100`**,报错措辞(`"'__index' chain too long; possible loop"`)待 12 核对。
- **`__unm` arity**:§6 定 `__unm` 传 `(a, a)`(5.1 `call_binTM` 对一元传两次操作数),**待 12 核对**;`__len` on userdata 定 `(a)` 单参(§7),亦待核对。
- **`__le→__lt` 回退的 `__lt` 查找顺序**:§9.3 回退里再找 `__lt` 的「先 a 后 b」顺序与官方 `lessequal` 对齐,**待 12 核对**。
- **十六进制浮点 coercion**:§5.2 定「`0x` 只接受十六进制整数,不支持 `0x1p4` 浮点」(5.2+ 特性),**待 12 核对** 5.1 `strtod` 在 `0x` 上的实际行为(可能平台相关)。
- **`__tostring` 返回非 string**:§11 按 5.1 严格报错;`tostring(table)` 的地址格式不可逐字节比(差分豁免),交 12 定口径。
- **弱表 ephemeron**:§13.5 weak key 表的「值存活依赖键存活」P1 简化为「键活则值无条件标活」。与 5.1 精确差异待 12 用例核对,若触及记为已知 P1 限制。

### 对 06 的回填请求(本文定义弱表语义,06 实现 GC 协作)——**已兑现**

承 §13.4,[06-memory-gc](./06-memory-gc.md) 已全部增补:

1. **`Collector.weakList`** + mark 阶段登记:06 §8.4 第 1 项 + §5.2 Table 行弱表例外。✅
2. **`clearWeakTables` 阶段**:06 §8.2 主流程已插入,顺序定稿 **mark → separateFinalizers(复活)→ clearWeakTables → sweep → runFinalizers**(06 §8.2 注采纳本文建议)。✅
3. **`object.Table.WeakMode() uint8`**:06 §8.4 已采纳为 object 侧 helper(STW 下 mark 期读元表安全)。✅
4. **可观察性**:cleartable 已纳入 06 §8.4 第 3 项差分口径。✅

### 对 01/02 的依赖确认(无新增字段)

- 本文**复用** [01](./01-value-object-model.md) §5.2 Table `metaRef`(word4)+ `flags bit0`、§5.5 Userdata `metaRef`(word2)+ `flags bit0`,**不新增字段**。
- 本文**复用** [01](./01-value-object-model.md) §5.2 的 `gen` 代次(05 的回填请求,**已落入 01 布局 word5 高 32 位**),`setmetatable` 调 `bumpGen()`。✅
- `typeMetatables[9]`(§1.2)是 **State 字段**(crescent 侧),非 arena 对象布局,不需 01/02 回填;其 GC 根地位**已由 [06](./06-memory-gc.md) §5.1 增补为 R9**。✅

---

相关:[01-value-object-model](./01-value-object-model.md) · [02-bytecode-isa](./02-bytecode-isa.md) ·
[05-interpreter-loop](./05-interpreter-loop.md) · [06-memory-gc](./06-memory-gc.md) ·
[09-errors-pcall](./09-errors-pcall.md) · [10-stdlib](./10-stdlib.md) ·
[12-testing-difftest](./12-testing-difftest.md) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
[value-representation](../../../llmdoc/architecture/value-representation.md) ·
roadmap:`docs/design/roadmap.md` (§6 非目标 / §5 原则 2 差分)
