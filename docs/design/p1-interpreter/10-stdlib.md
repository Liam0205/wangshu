# P1:标准库清单 + host function 调用约定

> 状态:**设计阶段,可实现深度**。本文是望舒**标准库(stdlib)**与 **host function 实现者 API 契约**的单一事实源:
> host function 调用约定展开成 stdlib 实现者契约、`internal/stdlib` 的 luaL_* 等价 helper API、
> shadow stack 纪律的库级完成、base/string/table/math/os/io/coroutine 七个子库的完整清单与语义、
> Lua 5.1 模式匹配(pattern)matcher 设计、`string.format` 指令集、`table.sort` 比较器重入、
> io 文件句柄(full userdata + `__gc`)设计、openlibs 库加载、P1 范围裁剪表。
> 上游契约:[05-interpreter-loop](./05-interpreter-loop.md) §7.5/§7.6 **host function 调用约定**
> (`type HostFn func(vm *VM, th *Thread) (nret int)`、host 进 Go 栈同步执行、host 帧压 CallInfo 带哨兵 protoID、
> host 从栈读参写返回、host 内部回调 Lua 用 `callLuaFromHost` §7.3 重入、host 不 Go panic 走 `vm.raise` §9.4)——
> 本文把这套调用约定**展开成 stdlib 实现者的完整 API 契约**。
> 内存侧:[06-memory-gc](./06-memory-gc.md) §6.3 **shadow stack 使用约定**(新分配的中间对象写回 Lua 栈前必须
> `Push`/`defer Pop`;「下一次可能触发 GC 的分配之前,所有已持有但未上 Lua 栈/表的 arena 引用都要在 shadow stack 上」)——
> 本文把这条纪律具体化到每类会分配的库函数。
> 值/对象侧:[01-value-object-model](./01-value-object-model.md) §3 NaN-box(参数类型检查 `IsNumber`/tag)、
> §3.4 canonicalize(`tonumber`/string→number 入口规范化)、§5.1 String(string 库操作字节)、§5.2 Table(table 库)。
> 元方法侧:[07-metatables-metamethods](./07-metatables-metamethods.md) `tostring` 查 `__tostring`(§11)、
> coercion 的 `parseLuaNumber`(07 §5.2 定义「算术/数值 for/tonumber 共用一套数字解析」,本文 `tonumber` 用同一套)、
> `__index` for string 库(07 §3.4)。
> 错误侧:[09-errors-pcall](./09-errors-pcall.md) `error`/`assert`/`pcall`/`xpcall` 是 stdlib host functions,
> 但其**机制**在 09 定稿——本文把它们列入 base 库清单并**指向 09**(不重复机制,给指针);错误措辞与 09 §9 错误目录对齐。
> 协程侧:[08-coroutines](./08-coroutines.md)(**并行起草中,可能尚不存在**)coroutine 库机制在 08 定稿——
> 本文列清单 + 一行语义,**指向 08**(前向引用占位)。
> 嵌入侧:[11-embedding-arena-abi](./11-embedding-arena-abi.md) host function 注册 API(`State.Register`)、
> per-item 栈式 API(`Push`/`Pop`/`ToNumber`)——stdlib 就是用这套 host 机制实现的内建库,与 11 的 host 注册保持一致。
> 语言面锁 Lua 5.1(`docs/design/roadmap.md` (§6),5.2+ 特性 `rawlen`/`table.unpack`/`bit32`/`goto`/整数除法等显式排除/记口径)。

对应 Go 包:`internal/stdlib`(base/string/table/math/os/io/coroutine 子库的 host functions);
host 调用机制、`luaL_*` 等价 helper 与解释器同 `internal/crescent`(host 帧、`callLuaFromHost`、`vm.raise`);
公共注册门面 `State.Register`/`RegisterModule` 在 root package `wangshu`(见 [11](./11-embedding-arena-abi.md) §10)。

---

## 0. 本文在 P1 中的位置与设计张力

标准库是 Lua「可用」的下半身:语言核心(解释器 + 元方法 + 错误)提供**机制**,stdlib 提供**能用的内建函数**——
`print`/`type`/`pairs`/`string.format`/`table.insert`/`math.floor`/`os.time` 这些是任何真实脚本的地基。
roadmap §4 把 stdlib 定为 P1 的一部分(「stdlib 也是 host function 形式提供」),[architecture](../architecture.md) §5
构建顺序第 10 步「base 库 → 逐步补齐 string/table/math/...」。

本文的全部张力来自三条约束的夹击:

1. **stdlib 是「host function 的最大用户」,必须先把 host 调用约定展开成可实现的契约**。05 §7.5/§7.6 给了 host
   调用的**机制**(签名、进 Go 栈、压 CallInfo、读参写返回);但「stdlib 实现者怎么写一个库函数」需要一层
   **lauxlib 等价的 helper API**(`luaL_checknumber`/`luaL_checkstring`/`luaL_argerror`/...)——这是 C Lua 写库
   的标准脚手架,本文 §1/§2 定稿望舒版。**没有这层,每个库函数都要手写类型检查 + 错误构造,既啰嗦又不一致。**

2. **stdlib 行为必须与 Lua 5.1 逐字节一致,这是差分测试的重灾区**(roadmap §5 原则 2)。库行为细节极多:
   `string.format` 的数字格式、pattern 匹配结果、`tostring` 格式、`table.sort` 稳定性、`math` 精度——**每一处
   偏离都是差分失败**。本文对差分敏感项(format/pattern/tostring/sort)明确指向 [12](./12-testing-difftest.md) 核对,
   并对 5.2+ 特性显式标注「5.1 排除/口径」(否则「多实现一个 `table.unpack` 全局」就与官方 5.1 分叉)。

3. **shadow stack 纪律必须具体化到每类会分配的库函数**(06 §6.3)。Lua 解释器执行字节码时「栈即根」零登记
   (06 §6.1),但 **host function 在 Go 栈持有 arena 引用的窗口期必须显式 Push/Pop**。`string.format`/`table.concat`/
   `string.rep` 这些拼接新串的函数是纪律的高发区——本文 §3 给范例伪码,把「漏 Push 偶发崩溃」(最难调的 bug 类,
   06 §6.3)的风险点钉死。

> 一句话定位:本文是 **host 调用约定的实现者视角展开 + stdlib 全清单 + 差分敏感行为的口径锁定**。05 定「host 怎么
> 被调」,06 定「host 的 GC 纪律」,07 定「tostring/coercion 元方法」,09 定「error/pcall 机制」;本文把它们**收束成
> 「写一个 stdlib 库函数要遵守的完整契约」**,并逐库列出 P1 要实现什么、简化什么、缺什么。

---

## 1. host function 调用约定:实现者 API 契约(展开 05 §7.5/§7.6)

### 1.1 签名与执行模型回顾(指针 05,不重复论证)

05 §7.6 定稿 host function 签名与机制,本文复述**实现者必须知道的契约面**(论证见 05):

```go
// host function 签名(05 §7.6):从 thread 取参数,push 返回值,返回返回值个数。
type HostFn func(vm *VM, th *Thread) (nret int)
```

实现者契约(每条都是 05 的机制在「写库函数」视角的完成):

| 契约 | 内容 | 来源 |
|---|---|---|
| **同步 Go 调用** | host 被 `CALL` 调用时**进 Go 调用栈**(与 Lua-call-Lua 的 reentry 相反),执行完返回值就位,主循环不切 code、不 reentry(`callReturnedHost`) | 05 §7.5/§7.6 |
| **参数在栈上** | 调用时实参已在 thread 值栈 `[base, base+nargs)`;host 用栈式 API 读(§2 的 `arg(i)`/`checkNumber(i)`) | 05 §7.6 步骤 1 |
| **返回值压栈** | host 把返回值 push 到栈顶,`return nret` 告知个数;调用侧 `moveResults` 按调用点 `nresults` 裁剪(05 §7.2) | 05 §7.6 步骤 4 |
| **host 帧有 CallInfo** | host 帧也压一条 CallInfo(protoID = 哨兵 `0xFFFFFFFF`,05 §1.2 word2),供 `error`/traceback 显示 `[C]: in function 'xxx'`(09 §7) | 05 §7.6 步骤 2 |
| **回调 Lua 用 callLuaFromHost** | host 内部若要调用 Lua 函数(`table.sort` 比较器、`pcall` 的被保护函数、`string.gsub` 的替换函数),用 `vm.callLuaFromHost`(05 §7.3 重入,**这才加 Go 栈**) | 05 §7.3/§7.6 |
| **出错走 raise,不 Go panic** | host 出错调 `vm.raise(errval)`(09 §3.3),它不 Go panic,而是设 `pendingErr` 让 `callHost` 返回 `callError` 进入冒泡(09 §2)。host **绝不该 Go panic**(panic 绕过 CallInfo 清理,09 §11) | 05 §7.6 / §9.4 |
| **shadow stack 纪律** | host 新分配的中间对象写回 Lua 栈/表前必须 `Push`/`defer Pop`(06 §6.3,本文 §3) | 06 §6.3 |

### 1.2 参数索引约定(1-based,对齐 Lua C API)

host function 读参数用 **1-based 索引**(`arg(1)` 是第一个参数),与 Lua 5.1 C API(`lua_*`/`luaL_*` 的栈索引语义)
对齐,降低从 C Lua 移植 stdlib 的摩擦:

- **`arg(i)`**:第 `i` 个参数(1-based)。物理上 = `thread.valueStack[base + i - 1]`(base 是 host 帧的参数区起点)。
- **`nargs()`**:实际传入的参数个数(= 调用点 `CALL` 的 `B-1`,或 `B=0` 时到 top 的个数,05 §7.1)。
- **`arg(i)` 越界**:`i > nargs()` 时返回 `value.Nil`(Lua 语义:缺失参数视为 nil)。这让 `optInt(i, default)`
  类「可选参数」helper 自然实现(缺失即 nil → 用默认值,§2.3)。

> **为什么 1-based 而非 0-based**:Lua C API 全 1-based(`luaL_checknumber(L, 1)`)。望舒 stdlib 大量移植自
> Lua 5.1 `lbaselib.c`/`lstrlib.c`/`ltablib.c` 的逻辑,保持 1-based 让「参数 #1/#2」与 C 源码、与错误信息
> `bad argument #1`(§2.4)、与官方文档一一对应,**减少移植错位**。内部物理索引(`base+i-1`)的 -1 转换封装在
> 栈式 API 里,实现者不感知。

### 1.3 返回值约定:push 后 return nret

host 把返回值 **push 到当前栈顶**,然后 `return nret`(返回值个数):

```go
// 范例:type(v) —— 返回一个字符串
func hostType(vm *VM, th *Thread) int {
    v := th.arg(1)                       // 1-based:第一个参数
    if th.nargs() == 0 {
        return vm.argError(th, 1, "value expected")  // §2.4:bad argument #1 to 'type' (value expected)
    }
    th.pushString(typeName(v))           // push 返回值(typeName 见 §4 base.type)
    return 1                             // 返回 1 个值
}
```

- **push 顺序 = 返回值顺序**:多返回值按 push 顺序排列。`return a, b` 等价「pushA; pushB; return 2」。
- **`nret` 与调用点裁剪**:host 返回 `nret` 个值,但调用点 `CALL A B C` 的 `C-1` 决定实际接收几个(05 §7.2
  `moveResults` 多退少补)。host 只管 push 它要返回的全部,裁剪是调用侧的事——所以 `next`/`unpack` 这类「返回
  可变个数」的 host 直接 push 全部,`return nret`,由调用点按需取。
- **`return 0`**:无返回值(如 `print`、`table.insert`)。

### 1.4 host 内部回调 Lua(callLuaFromHost,重入)

某些 stdlib 函数需要在 host 内部**调用一个 Lua 函数**:

| 库函数 | 回调的 Lua 函数 | 重入点 |
|---|---|---|
| `table.sort(t, comp)` | 比较器 `comp(a, b)`(§5.4) | 每次比较 |
| `string.gsub(s, p, repl)` 当 repl 是 function | 替换函数 `repl(captures...)`(§4 string.gsub) | 每次匹配 |
| `pcall(f, ...)` / `xpcall(f, h, ...)` | 被保护函数 `f`、handler `h`(→ 09) | 一次 |
| `coroutine.resume` 实现内部 | 切到 co thread 跑(→ 08,机制不同,非普通 callLuaFromHost) | — |
| `__index`/`__call` 等元方法的 host 侧 | 元方法函数(→ 07 `callMM`) | — |

机制(05 §7.3):

```go
// host 内部调用一个 Lua 值(可能是 Lua closure 或另一个 host),取 nresults 个返回值。
// 这是 05 §7.3 的 callLuaFromHost:它压一个 fresh CallInfo(标 callStatus_fresh)+ 起一个新的
// execute Go 栈帧(host→Lua 才加 Go 栈,05 §7.3)。返回后 reentry 结束,控制权回 host。
func (vm *VM) callLuaFromHost(th *Thread, fn value.Value, args []value.Value, nresults int) ([]value.Value, *LuaError)
```

要点(实现者纪律):

- **callLuaFromHost 加 Go 栈**:每次调用起一个新 `execute`(05 §7.3),消耗一层 Go 栈与一个 `nCcalls`(05 §7.4
  C stack overflow 上限)。`table.sort` 的比较器在快排里被调 O(n log n) 次,但**每次比较是一次 callLuaFromHost
  返回后才下一次**(不嵌套累积 Go 栈),所以 Go 栈深度是 O(1) 不是 O(n log n)——见 §5.4 的重入分析。
- **callLuaFromHost 可能出错**:被调 Lua 函数内部可能 `error`。返回的 `*LuaError` 非 nil 时,host 必须把它
  **继续上抛**(`vm.raise` 或直接让 `callHost` 冒泡,§1.5)——host 不能吞掉比较器/替换函数的错误。
- **callLuaFromHost 触发 GC**:被调函数可能分配 → GC → 搬迁 arena。host 在 callLuaFromHost 前后持有的 arena
  引用必须在 shadow stack 上(§3),且**调用后重取**任何缓存的 arena 视图指针(类比 05 §1.3 reload stk)。

### 1.5 host 出错:vm.raise(指向 09)

host 出错的机制在 09 §3.3 定稿(`vm.raise` → `pendingErr` → `callHost` 返回 `callError` → 主循环冒泡)。本文
**不重复机制**,只给 stdlib 实现者的**使用面**:

```go
// host 抛 Lua 错误(09 §3.3)。它不 Go panic;设 pendingErr 并让 callHost 走 callError 冒泡。
// 调用后 host 应立即 return(后续代码不可达,但 Go 需要一个 return 语句)。
func (vm *VM) raise(errval value.Value) callResult

// 便捷:stdlib 参数错误(§2.4),拼 "bad argument #n to 'fname' (...)" 并 raise。
func (vm *VM) argError(th *Thread, argn int, extramsg string) int  // 返回 0(不可达占位)
```

- **stdlib 错误恒带位置前缀**:解释器内在错误与 stdlib host 错误的 `value` 是 string,经 09 §3 的 `where()`
  加 `<source>:<line>: ` 前缀。但**参数错误(`argError`)的位置语义特殊**:它归咎于**调用 stdlib 函数的那一层**
  (level=1 指向调用点),措辞 `bad argument #n to 'fname' (...)`(§2.4),对齐 Lua `luaL_argerror`。
- **host 出错后立即返回**:`raise`/`argError` 后 host 的剩余代码不可达(错误已进入冒泡),但 Go 语法要求 host
  函数有返回——惯例 `return vm.argError(...)`(argError 返回 0)或 `vm.raise(...); return 0`。

---

## 2. `internal/stdlib` 的 luaL_* 等价 helper API(对标 lauxlib)

写一个 stdlib 库函数的 99% 是「检查参数类型 + 取值 + 算 + push 结果」。Lua C 用 lauxlib(`luaL_*`)提供这层脚手架。
本节定稿望舒版——**这层 helper 让每个库函数短小一致,且把参数错误措辞收口到一处**(差分一致性,§2.4)。

### 2.1 helper API 全景(挂在 Thread / HostCtx 上)

helper 既可挂 `*Thread`(internal 实现者用),也经 `*HostCtx` 暴露给宿主扩展(11 §10.1 已给公共子集
`ArgNumber`/`ArgString`/`PushNumber`/`Raise`/`Pin`)。本节给 **internal 完整集**(stdlib 自身用),公共子集是其投影:

```go
// internal/crescent(与解释器同包,host 帧上下文)—— luaL_* 等价 helper。
// 这些方法操作「当前 host 帧」的参数区与栈顶,封装 1-based 索引与类型检查 + 错误构造。

// —— 参数个数与原始访问 ——
func (th *Thread) NArgs() int                      // = lua_gettop 相对 host 帧;实际参数个数
func (th *Thread) Arg(i int) value.Value           // 1-based;越界返回 Nil(§1.2)

// —— 强制类型检查(check 系列,失败 → argError,§2.4)——
func (th *Thread) CheckNumber(i int) float64       // luaL_checknumber:非 number/可转 string → 错
func (th *Thread) CheckInt(i int) int64            // luaL_checkinteger:check number 再取整(§2.2)
func (th *Thread) CheckString(i int) []byte        // luaL_checkstring:string 或 number(自动转串);返回字节(§2.5)
func (th *Thread) CheckTable(i int) value.GCRef    // luaL_checktype LUA_TTABLE:非 table → 错
func (th *Thread) CheckFunction(i int) value.Value // 非 function(且无 __call?见 §2.6)→ 错
func (th *Thread) CheckType(i int, t uint16)       // luaL_checktype:tag 不符 → 错

// —— 可选参数(opt 系列,缺失/nil → 默认值)——
func (th *Thread) OptNumber(i int, def float64) float64  // luaL_optnumber
func (th *Thread) OptInt(i int, def int64) int64         // luaL_optinteger
func (th *Thread) OptString(i int, def []byte) []byte    // luaL_optstring

// —— push 返回值(写栈顶)——
func (th *Thread) PushNil()
func (th *Thread) PushBool(b bool)
func (th *Thread) PushNumber(f float64)             // 过 canonicalize(01 §3.4)
func (th *Thread) PushInt(n int64)                  // = PushNumber(float64(n))(Lua 无整数,01 §3.3)
func (th *Thread) PushString(s []byte) value.GCRef  // intern 进 arena(06 §9.1),返回 GCRef 便于 Pin
func (th *Thread) PushValue(v value.Value)
func (th *Thread) PushGCString(ref value.GCRef)     // 已 intern 的串直接 push

// —— 错误(§2.4)——
func (vm *VM) ArgError(th *Thread, argn int, extramsg string) int   // bad argument #n to 'fn' (extramsg)
func (vm *VM) TypeError(th *Thread, argn int, tname string) int     // bad argument #n to 'fn' (tname expected, got X)
func (vm *VM) Errorf(th *Thread, format string, a ...any) int       // 通用:where()+fmt → raise

// —— shadow stack(§3,= 06 §6.2 的 ShadowStack.Push/Pop)——
func (vm *VM) Pin(v value.Value) int                // shadow stack Push;返回 handle
func (vm *VM) Unpin(handle int)                     // shadow stack Pop
```

### 2.2 CheckInt / CheckNumber:number 取整与 coercion

- **`CheckNumber(i)`**:取 `arg(i)`,若 `IsNumber` 直接返回 `AsNumber`;若是 string,走 **`toNumber`(07 §5.2 的
  `parseLuaNumber`,与算术/for 共用)**——成功返回,失败 `argError`「number expected, got string」。这兑现 07 §5.2
  「算术/for/tonumber 共用一套数字解析」:stdlib 的 number 参数也用同一套(差分一致)。
- **`CheckInt(i)`**:先 `CheckNumber`,再取整。Lua 5.1 `luaL_checkinteger` 的取整是 **`(lua_Integer)d`**(C 的
  double→整数截断,向零)。**关键 5.1 口径**:`string.sub("abc", 1.9)` 的 `1.9` 取整为 `1`(截断,非四舍五入)。
  **待 12 差分核对**:5.1 对「非整数 double 作整数参数」是否报错还是截断——5.1 `luaL_checkinteger` 直接截断不报错
  (`lua_number2int` 宏),**定稿:截断,不报错**(与官方一致)。超出范围(`> 2^63`)行为未定义,P1 截断到 int64 范围。
- **数字→整数边界**:Lua 5.1 number 是 double,`CheckInt` 返回 `int64` 仅是「取整后的整数视图」,值域受 double
  精度限(|v|>2^53 已不精确,11 §3.3.2 同理)。stdlib 内部用 int64 做索引/长度运算,但来源精度是 double 的。

### 2.3 OptInt / OptString:可选参数

`opt` 系列处理「参数可缺失,缺失用默认值」:

```go
// luaL_optinteger:arg(i) 缺失(i>nargs)或为 nil → 返回 def;否则 = CheckInt(i)。
func (th *Thread) OptInt(i int, def int64) int64 {
    if th.Arg(i) == value.Nil {        // 缺失或显式 nil(§1.2:越界返回 Nil)
        return def
    }
    return th.CheckInt(i)              // 非 nil 则强制检查(类型错仍报错)
}
```

- **缺失 vs nil 等同**:Lua 5.1 `luaL_opt*` 把「参数不存在」与「参数是 nil」一视同仁(都用默认值)。
  `string.sub("abc", 2)` 的第三参缺失 → `j` 默认 `-1`;`string.sub("abc", 2, nil)` 同样 → `-1`。
- **典型用途**:`string.sub(s, i, j)` 的 `j` 默认 -1;`string.rep(s, n, sep)` 的 `sep` 默认空串;`tonumber(s, base)`
  的 `base` 默认「无 base」(走 `parseLuaNumber`)。

### 2.4 参数错误措辞:`bad argument #n to 'fname' (...)`(差分敏感,收口一处)

**这是 stdlib 差分一致性的关键收口点**(09 §9.3 #17 列「`bad argument` 系列由各 host 构造」,本文定稿统一格式):

```go
// luaL_argerror 等价。格式与 Lua 5.1 lauxlib 逐字节对齐(待 12 核对标点)。
func (vm *VM) ArgError(th *Thread, argn int, extramsg string) int {
    fname := vm.currentHostFnName(th)    // 当前 host 函数名(§2.4.1:从调用点 getobjname 推断,或注册名)
    msg := fmt.Sprintf("bad argument #%d to '%s' (%s)", argn, fname, extramsg)
    // 位置前缀:归咎调用者(level=1),where() 加 "<source>:<line>: "(09 §3)
    return vm.raise(vm.internString(vm.where(th, 1) + msg))
}

// luaL_typerror 等价:类型不符的标准 extramsg。
func (vm *VM) TypeError(th *Thread, argn int, expected string) int {
    got := typeName(th.Arg(argn))         // 实际类型名(01 §3.3)
    return vm.ArgError(th, argn, fmt.Sprintf("%s expected, got %s", expected, got))
}
```

措辞模板(对齐 Lua 5.1,**精确标点待 12 核对**):

| 场景 | extramsg | 完整信息 |
|---|---|---|
| 类型不符 | `<expected> expected, got <got>` | `bad argument #1 to 'setmetatable' (table expected, got number)` |
| 值缺失 | `<expected> expected, got no value` | `bad argument #2 to 'concat' (string expected, got no value)` |
| 范围越界 | `index out of range` / `<n> too large` 等 | `bad argument #2 to 'sub' (...)` |
| 通用约束 | host 自构 | `bad argument #1 to 'format' (number expected, got string)` |

- **`got no value`**:参数完全缺失(`i > nargs`)时,Lua 5.1 用 `got no value`(而非 `got nil`)。区分「传了 nil」
  与「没传」:`f(nil)` 的 #1 是 `got nil`,`f()` 的 #1 是 `got no value`。**待 12 核对**此区分。
- **位置前缀归咎调用者**:`argError` 用 `where(th, 1)`——level=1 指向**调用 stdlib 函数的那个 Lua 帧**(因为
  错的是调用者传的参数)。这与 Lua 5.1 `luaL_argerror` 一致(它内部 `luaL_where(L, 1)`)。

#### 2.4.1 host 函数名 `fname` 的取得

`bad argument #n to '<fname>'` 里的 `<fname>` 怎么来?Lua 5.1 用 `luaL_argerror` 的调用栈推断(`getfuncname`,
09 §8):看调用 stdlib 函数的那条 CALL 指令,逆推被调函数名(`string.format` 经 `(field 'format')` 或注册全局名)。

- **P1 简化(对齐 09 §8.3)**:host 函数名优先用 **09 §8 的 `getobjname`**(从调用点逆推:`format` 在
  `string.format(...)` 是 field 'format';`print` 是 global 'print')。推断不出时,**兜底用 host 注册时的名字**
  (`State.Register("print", ...)` 记的 "print",或库函数表里的键名)。
- **为什么兜底注册名**:`getobjname` 在复杂调用形式(`local f = string.format; f(...)`)推断不出 field 名,
  此时 Lua 5.1 会显示 `?` 或推断的局部名。P1 兜底注册名更友好(总能给出 `format`),**与官方可能微差**(官方可能
  显示局部变量名),记 §15.2 缺口 + 12 核对。

### 2.5 CheckString:string 或 number,返回字节

Lua 5.1 `luaL_checkstring` 接受 **string 或 number**(number 自动转字符串,因为「数字能当字符串用」是 5.1 语义):

```go
// luaL_checkstring/luaL_checklstring。string 直接取字节;number 用 %.14g 转串(01 / 05 §4.6 同格式)。
// 返回 arena 内字节切片(只读;调用者不得改)。
func (th *Thread) CheckString(i int) []byte {
    v := th.Arg(i)
    switch {
    case value.Tag(v) == value.TagString:
        return th.vm.stringBytes(v)                 // arena String 字节(01 §5.1)
    case value.IsNumber(v):
        s := formatLuaNumber(value.AsNumber(v))     // %.14g(差分:与 tostring/CONCAT 同,§4 / 05 §4.6)
        ref := th.vm.internString(s)                // 转的串要 intern(它成了一个真 String)
        // ★ 注意:这里新建了 String,若调用者后续还要分配,需 Pin(§3)——但 CheckString 通常紧接使用,
        //   且结果会被 push 或立即用,实践中 number→string 的中间串风险低(见 §3.4 讨论)。
        return th.vm.stringBytes(value.MakeGC(value.TagString, ref))
    default:
        th.vm.TypeError(th, i, "string")            // string expected, got <type>
        return nil                                   // 不可达
    }
}
```

- **number 自动转串**:`string.len(123)` 合法(= `string.len("123")` = 3)。这是 5.1「string 库函数接受 number」
  的便利,源自 `luaL_checkstring` 的隐式转换。**与 `tostring` 区别**:这里用 `%.14g`(数字格式),不查 `__tostring`。
- **返回字节切片**:Lua string 是字节串(01 §5.1),`CheckString` 返回 `[]byte`(arena 内,只读)。string 库
  操作字节(§4 string 库),不假设 UTF-8。

### 2.6 CheckFunction / CheckTable:对象类型检查

- **`CheckTable(i)`**:`arg(i)` 必须 tag == TagTable,否则 `TypeError(i, "table")`。返回 GCRef 供直接操作表
  (`object.TableAt`)。**不接受**有 `__index` 的非 table(`table.insert` 要真表,5.1 语义)。
- **`CheckFunction(i)`**:必须是 function(Lua closure 或 host closure)。Lua 5.1 `table.sort` 的比较器、
  `pcall` 的 f **要求是 function**(不接受有 `__call` 的对象?——5.1 `pcall` 实际接受任意可调用,但 `luaL_checktype
  LUA_TFUNCTION` 严格要 function)。**待 12 核对**:P1 定稿 `CheckFunction` 严格要 function tag(与 `luaL_checktype`
  一致),`pcall`/`xpcall` 的 f 因 09 机制可能放宽(09 定),记口径。

---

## 3. shadow stack 纪律的库级完成(具体化 06 §6.3)

06 §6.3 给了 host function 的 shadow stack 使用约定;本节**具体化到每类会分配的库函数**,给 2-3 个范例伪码,
把「漏 Push 偶发崩溃」(06 §6.3:最难调的 bug 类)的风险点钉死。

### 3.1 哪些库函数分配 arena 对象(纪律高发区)

下表列出**会在 host 内部新分配 arena 对象**的库函数——它们是 shadow stack 纪律的适用对象:

| 库函数 | 分配什么 | 纪律风险点 |
|---|---|---|
| `string.format` | 拼接结果串 + **多个中间片段**(每个 `%` 指令产一段) | 高:多步拼接,中间串易漏 Pin(§3.3) |
| `string.rep` | 重复 n 次的大结果串(可能很大) | 中:一次性分配,但若先算 sep 再算主体需 Pin |
| `string.sub`/`upper`/`lower`/`reverse`/`byte`/`char` | 新结果串 | 中:多为一次分配后即 push |
| `string.gsub`/`gmatch`/`match`/`find` | 捕获子串、替换结果串、累积缓冲 | 高:循环匹配累积多个串(§3.4) |
| `table.concat` | 拼接结果串 + 中间累积 | 高:循环拼接(§3.2 范例) |
| `table.insert`/`remove` | 可能触发表 array 扩容 → rehash(新 array 段) | 中:rehash 在 RawSet 内,host 持有的其它引用要在栈上 |
| `string→number`/`tostring` | number→string 的结果串 | 低:单串,紧接 push |
| host 新建表(如自定义库返回 table) | Table 头 + array/node 段 | 中:建表后填充期间表要可达 |

**非分配库函数**(`type`/`rawget`/`rawequal`/`math.floor`/`select`/`next`/...)**不涉及 shadow stack 纪律**——
它们读参数、算、push 标量或已存在的对象引用,不新建 arena 对象,无中间持有窗口。

### 3.2 范例一:`table.concat`(循环拼接,逐步累积)

`table.concat(t, sep, i, j)` 把 `t[i]..sep..t[i+1]..sep..…..t[j]` 拼成一个串。它在循环里**逐步累积结果**,
每步可能分配 → 触发 GC → 累积的中间串必须在 shadow stack 上:

```go
// table.concat(t, sep, i, j):拼接表的数组段。展示 shadow stack 纪律(06 §6.3)。
func hostTableConcat(vm *VM, th *Thread) int {
    tref := th.CheckTable(1)                       // arg1 = 表
    sep := th.OptString(2, nil)                    // arg2 = 分隔符,默认空
    i := th.OptInt(3, 1)                           // arg3 = 起始,默认 1
    t := object.TableAt(vm.arena, tref)
    j := th.OptInt(4, int64(t.Len()))              // arg4 = 结束,默认 #t(border,01 §5.2)

    // —— 累积拼接 ——
    var buf bytesBuilder                            // Go 侧字节缓冲(在 Go 堆,非 arena,无 GC 顾虑)
    for k := i; k <= j; k++ {
        v := t.GetInt(int(k))                       // t[k]
        // 元素必须是 string 或 number(否则报错,Lua 5.1 语义)
        switch {
        case value.Tag(v) == value.TagString:
            buf.Write(vm.stringBytes(v))            // 读 arena String 字节(只读,不分配)
        case value.IsNumber(v):
            buf.WriteString(formatLuaNumber(value.AsNumber(v)))  // %.14g
        default:
            return vm.Errorf(th, "invalid value (at index %d) in table for 'concat'", k)
        }
        if k < j && sep != nil {
            buf.Write(sep)
        }
    }
    // —— 一次性 intern 最终结果(唯一的 arena 分配点)——
    th.PushString(buf.Bytes())                      // intern 进 arena + push;无中间 arena 串需 Pin
    return 1
}
```

**纪律分析(为什么这个实现安全)**:

- **关键技巧:用 Go 侧字节缓冲累积,只在最后 intern 一次**。中间拼接全在 Go 堆的 `bytesBuilder`(Go GC 管,
  内容是字节不含 arena 引用),**不在 arena 分配中间串**——所以循环里**没有「持有 arena 中间串 + 下一步分配」
  的窗口**,无需 Pin。这是 stdlib 拼接类函数的**首选模式**(对齐 Lua 5.1 `luaL_Buffer` 的思路:先在 C 缓冲累积,
  最后 `luaL_pushresult` 一次成串)。
- **唯一 arena 分配在末尾 `PushString`**:此时无其它持有的 arena 中间引用,GC 触发也无碍(结果串 push 后即在
  栈上可达)。
- **读 `t.GetInt`/`stringBytes` 不分配**:遍历表、读元素字节是纯读,不触发 GC。**但注意**:若 `t` 的元素读取
  涉及 `__index`(本例用 `GetInt` raw 读,不走元方法,与 Lua 5.1 `table.concat` 一致——它 raw 访问),则无重入。

### 3.3 范例二:`string.format`(多步拼接,Go 缓冲规避)

`string.format(fmt, ...)` 按格式串拼接,同样用 Go 缓冲规避 arena 中间串:

```go
// string.format(fmt, args...):格式化。展示「Go 缓冲累积 + 末尾一次 intern」规避 shadow stack。
func hostStringFormat(vm *VM, th *Thread) int {
    fmtb := th.CheckString(1)                       // 格式串字节
    var buf bytesBuilder                            // Go 堆缓冲
    argn := 2                                       // 下一个要消费的参数索引(%s/%d 各消费一个)
    i := 0
    for i < len(fmtb) {
        c := fmtb[i]; i++
        if c != '%' {
            buf.WriteByte(c)                        // 普通字节直接写
            continue
        }
        // 解析格式指令 %[flags][width][.prec]<conv>(§4 string.format 指令表)
        spec, conv, ni := parseFormatSpec(fmtb, i)  // 解析到转换字符
        i = ni
        switch conv {
        case 'd', 'i':
            buf.WriteString(formatInt(spec, th.CheckInt(argn))); argn++
        case 'f', 'g', 'e', 'G', 'E':
            buf.WriteString(formatFloat(spec, conv, th.CheckNumber(argn))); argn++
        case 's':
            // %s:对参数调 tostring(可能查 __tostring → 重入!见下纪律)
            s := vm.tostringForFormat(th, argn)     // §4 / 07 §11:可能 callLuaFromHost(__tostring)
            buf.Write(applyStringSpec(spec, s)); argn++
        case 'q':
            buf.Write(quoteString(th.CheckString(argn))); argn++  // %q:转义(§4)
        case 'x', 'X', 'o', 'c', 'u':
            buf.WriteString(formatIntConv(spec, conv, th.CheckInt(argn))); argn++
        case '%':
            buf.WriteByte('%')                      // %% → 字面 %
        default:
            return vm.Errorf(th, "invalid option '%%%c' to 'format'", conv)
        }
    }
    th.PushString(buf.Bytes())                      // 末尾一次 intern
    return 1
}
```

**纪律分析(`%s` 的 `__tostring` 重入是关键风险)**:

- **`%s` 可能重入 Lua**:`%s` 对参数调 `tostring`(§4),若该参数有 `__tostring` 元方法,会 `callLuaFromHost`
  (07 §11 / §1.4)→ **重入 + 可能 GC + 可能搬迁 arena**。此时 `buf`(Go 堆)安全,但**已读但未写入 buf 的 arena
  字节切片若是 arena 内的(如前一个 `%s` 的结果),在重入前要么已写进 Go buf(脱离 arena),要么 Pin**。
- **本实现的安全性**:每个 `%s` 的结果**立即 `buf.Write` 写进 Go 缓冲**(脱离 arena),不跨越下一次重入持有。
  `formatInt`/`formatFloat` 返回 Go string(不在 arena)。所以**没有「持有 arena 中间串跨重入/跨分配」的窗口**。
- **若改用 arena 累积串(反模式)**:假设把中间结果存成 arena String 逐步拼,则每次 `%s` 重入前必须 Pin 已累积的
  arena 串——**这正是 06 §6.3 纪律的适用场景**,但本实现用 Go 缓冲**从根本上规避**了它。**stdlib 拼接类一律
  优先 Go 缓冲**,把 shadow stack 用量降到「只有真正必须在 arena 持有跨分配引用时」(罕见)。

### 3.4 范例三:必须用 shadow stack 的场景(`string.gsub` 累积 + 重入)

少数函数无法纯用 Go 缓冲规避——`string.gsub(s, pat, repl)` 当 `repl` 是函数时,每次匹配调 `repl(captures)`
(重入),**捕获的子串是 arena String**,在调 `repl` 前必须 Pin:

```go
// string.gsub 的替换函数分支(简化):repl 是 Lua function 时,每次匹配调它。
// 捕获子串是 arena String,调 repl(重入,可能 GC)前必须 Pin。
func gsubFunctionRepl(vm *VM, th *Thread, captures []value.Value, repl value.Value) (value.Value, *LuaError) {
    // captures 里的字符串捕获是 arena String 引用(matcher 从 s 切出并 intern,§4 pattern)。
    // —— 调 repl 前 Pin 所有捕获(它们在 Go slice 里,GC 看不见,06 §5.1 R7)——
    handles := make([]int, 0, len(captures))
    for _, cap := range captures {
        if value.IsCollectable(cap) {               // string 捕获是可回收(01 §3.3)
            handles = append(handles, vm.Pin(cap))  // 登记为 GC 根(06 §6.2)
        }
    }
    defer func() {                                   // 调用后批量 Unpin(逆序)
        for k := len(handles) - 1; k >= 0; k-- { vm.Unpin(handles[k]) }
    }()
    // —— 重入:调 repl(captures...),取首返回值 ——(可能 GC;captures 已 Pin,安全)
    res, err := vm.callLuaFromHost(th, repl, captures, 1)
    if err != nil {
        return value.Nil, err                        // 替换函数出错:上抛(§1.4)
    }
    return res[0], nil
}
```

**纪律分析(为什么这里必须 Pin)**:

- **捕获子串在 Go slice,GC 看不见**:matcher 把匹配的子串从源串切出、intern 成 arena String,放进 `captures`
  这个 **Go slice**。06 §5.1 R7:GC 精确栈扫描看不见 Go 栈/slice 里的 arena 引用(GCRef 是整数)。
- **调 `repl` 会 GC**:`callLuaFromHost` 跑 Lua 替换函数,可能分配 → GC。若 `captures` 里的串没 Pin,GC 会
  误回收它们(它们除了 `captures` Go slice 外无其它引用)→ `repl` 收到悬垂 GCRef → 崩溃/脏读。
- **正确做法:调 `repl` 前 Pin captures,调后 Unpin**。这是 06 §6.3 纪律「下一次可能触发 GC 的分配之前,所有
  已持有但未上 Lua 栈/表的 arena 引用都要在 shadow stack 上」的精确兑现——captures 是「已持有但未上栈」的 arena
  引用,`callLuaFromHost`(内部分配)是「下一次可能触发 GC 的分配」。
- **替代方案:把 captures 先 push 到 Lua 栈**(成为 R5 自动可达,06 §5.1),则不需 Pin。但 gsub 的 captures 作为
  `callLuaFromHost` 的 args 本就要进栈——若 `callLuaFromHost` 实现是「先把 args 压栈再 execute」,则 args 压栈那一刻
  起就 R5 可达,Pin 可省。**定稿:`callLuaFromHost` 内部把 args 压栈后才 execute**(args 进栈即可达),故**上面的
  显式 Pin 在「args 已由 callLuaFromHost 压栈」前提下可简化**——但**在 args 压栈之前**(host 持有 captures 准备传入
  的窗口)仍需保证可达。**纪律本质不变:从「捕获 intern」到「captures 进 Lua 栈」之间的窗口要么无分配,要么 Pin。**

> **stdlib 纪律总则(三条范例归纳)**:① **优先 Go 缓冲累积**(concat/format/rep),把 arena 分配压缩到末尾一次,
> 根本规避中间持有窗口;② **不可避免地在 arena 持有中间引用跨分配/跨重入时,Pin/defer Unpin**(gsub 捕获);
> ③ **能尽早把引用送进 Lua 栈/表的就送**(进栈即 R5 可达,免 Pin)。差分测试的 **GC 压力 fuzz**(06 §11:GCPAUSE
> 设极小,每次分配 GC)是捕获漏 Pin 的主要自动化防线——漏 Pin 在正常 pacing 下偶发,高频 GC 下必现。

---

## 4. base 库(全局函数)

base 库的函数挂在 **globals 表**(直接全局可见,无需 `base.` 前缀)。下表是 Lua 5.1 base 库的完整清单 + 一行
语义 + 参数/返回。**机制在别处定稿的(error/assert/pcall/xpcall/coroutine)给指针,不重复**。

### 4.1 base 库清单(语义 + 参数/返回 + P1)

| 函数 | 语义 | 参数 → 返回 | P1 | 备注/指针 |
|---|---|---|---|---|
| `print(...)` | 各参数 `tostring` 后用 `\t` 分隔写 stdout,末尾 `\n` | `(...)` → 无 | ✅ | 经 `tostring`(查 `__tostring`,§4.2);写 stdout(io 库的 stdout,§8) |
| `type(v)` | 返回 v 的类型名 | `(v)` → string | ✅ | `"nil"`/`"boolean"`/`"number"`/`"string"`/`"table"`/`"function"`/`"userdata"`/`"thread"`(01 §3.3);light+full ud 都 `"userdata"` |
| `tostring(v)` | v 转字符串(查 `__tostring`) | `(v)` → string | ✅ | 机制 07 §11(`__tostring` + 默认格式);number 用 `%.14g`;table/function 地址格式差分豁免(§4.2) |
| `tonumber(v [, base])` | v 转数字;带 base 时按进制解析整数 | `(v [,base])` → number or nil | ✅ | 无 base 走 `parseLuaNumber`(07 §5.2,与算术共用);带 base 走另一条(§4.3) |
| `pairs(t)` | 返回 `next, t, nil`(遍历全表迭代三元组) | `(t)` → (function, table, nil) | ✅ | 返回 `next` host fn + t + nil;**不查 `__pairs`**(5.2+,07 §2.3 排除);遍历序见 §4.4 |
| `ipairs(t)` | 返回数组迭代三元组(从 1 连续到首个 nil) | `(t)` → (function, table, 0) | ✅ | 返回 ipairs 迭代器 host fn + t + 0;**不查 `__ipairs`**(5.2+ 引入 5.3 废,排除) |
| `next(t [, k])` | 表的下一个键值对(`k=nil` 取第一个) | `(t [,k])` → (key, val) or nil | ✅ | 遍历序 = 内部 array 段后 node 段顺序(§4.4,呼应 06 §9.3/§11) |
| `select(n, ...)` | `n` 是数字:返回从第 n 个起的可变参;`n=='#'`:返回参数个数 | `(n, ...)` → ... or number | ✅ | `select('#', ...)` 取个数;`select(2, a,b,c)` 返回 b,c;负 n 从尾数(5.1) |
| `rawget(t, k)` | 不经 `__index` 取 `t[k]` | `(t, k)` → value | ✅ | raw 表访问(01 §5.2),无元方法 |
| `rawset(t, k, v)` | 不经 `__newindex` 设 `t[k]=v` | `(t, k, v)` → t | ✅ | raw 表写(键 nil/NaN 报错,07 §4.3);返回 t |
| `rawequal(a, b)` | 不经 `__eq` 的原始相等 | `(a, b)` → boolean | ✅ | 01 §6 rawequal(number 浮点比/string GCRef 比/其它 bits 比) |
| `rawlen(v)` | — | — | **❌ 5.2 排除** | **5.1 无 `rawlen`**(5.2 引入)。P1 不提供;`#` 直接用 LEN(05 §4.3) |
| `setmetatable(t, mt)` | 设表 t 的元表(仅 table 首参) | `(t, mt)` → t | ✅ | 机制 07 §1.3(`__metatable` 保护 + bump gen);非 table 报错 |
| `getmetatable(v)` | 取 v 的元表(经 `__metatable` 保护) | `(v)` → table or nil | ✅ | 机制 07 §1.3 |
| `assert(v [, msg, ...])` | v 为假则抛 msg(默认 `"assertion failed!"`),否则返回所有参数 | `(v [,msg,...])` → ... | ✅ | **机制 → [09](./09-errors-pcall.md) §4**(裸 msg 不加位置,与 error 区别) |
| `error(msg [, level])` | 抛 msg 为错误,level 控制位置前缀 | `(msg [,level])` → 不返回 | ✅ | **机制 → [09](./09-errors-pcall.md) §3**(位置前缀仅 string+level≠0) |
| `pcall(f, ...)` | 保护调用 f,捕获错误 | `(f, ...)` → (bool, ...) | ✅ | **机制 → [09](./09-errors-pcall.md) §5**(protected 边界) |
| `xpcall(f, handler)` | 保护调用 f,出错时调 handler | `(f, handler)` → (bool, ...) | ✅ | **机制 → [09](./09-errors-pcall.md) §6**(5.1 不传 args 给 f) |
| `unpack(t [, i [, j]])` | 返回 `t[i], t[i+1], ..., t[j]`(多值) | `(t [,i,j])` → ... | ✅ | **5.1 是全局 `unpack`**(§4.5);5.2+ 移到 `table.unpack`——**5.1 用全局,记口径** |
| `_G` | 全局环境表自身 | (变量) | ✅ | globals 表本身(`_G._G == _G`);01 §1 / 05 §11.3 |
| `_VERSION` | Lua 版本字符串 | (变量) | ✅ | **恒 `"Lua 5.1"`**(roadmap §6 锁 5.1) |
| `collectgarbage(opt [, arg])` | GC 控制 | `(opt [,arg])` → 依 opt | ✅ | **→ [06](./06-memory-gc.md) 的 GC 控制**(§4.6:collect/count/step/setpause/setstepmul/stop/restart) |
| `gcinfo()` | 返回当前 arena 使用的 KB 数(5.1 遗留) | `()` → number | △ | **5.1 有 `gcinfo`**(已废弃但 5.1 存在);= `collectgarbage("count")` 的旧形式(§4.6) |
| `loadstring(s [, name])` | 编译字符串为函数(不执行) | `(s [,name])` → (function) or (nil, err) | △ | 用 `Compile`(11 §1.3);P1 范围见 §4.7 |
| `load(func [, name])` | 用 reader 函数分块编译 | `(func [,name])` → (function) or (nil, err) | △ | P1 简化(§4.7);5.1 的 `load` 取 reader 函数 |
| `loadfile([filename])` | 编译文件为函数 | `([fn])` → (function) or (nil, err) | △ | 依赖 io 文件读(§8);P1 部分(§4.7) |
| `dofile([filename])` | 编译并执行文件 | `([fn])` → ... | △ | = `loadfile` + 调用;P1 部分(§4.7) |
| `require(modname)` | 加载模块(module 系统) | `(modname)` → module | **❌/△ 缺口** | **module/package 系统 P1 缺口或极简**(§4.7);依赖 `package.loaders`/`package.path` |
| `module(name, ...)` | 声明模块(5.1 module 系统) | `(name, ...)` → 无 | **❌ 缺口** | 5.1 module 系统;P1 不做(§4.7) |

### 4.2 `print` 与 `tostring`(查 `__tostring`,指向 07)

```go
// print(...):各参数 tostring 后 \t 分隔,末尾 \n,写 stdout。
func hostPrint(vm *VM, th *Thread) int {
    n := th.NArgs()
    var buf bytesBuilder
    for i := 1; i <= n; i++ {
        if i > 1 { buf.WriteByte('\t') }
        s, err := vm.tostring(th, th.Arg(i))     // 查 __tostring(07 §11);可能重入 → 见下
        if err != nil { return vm.raiseErr(err) }
        buf.Write(s)
    }
    buf.WriteByte('\n')
    vm.writeStdout(buf.Bytes())                   // io 库 stdout(§8;P1 至少 os.Stdout)
    return 0
}
```

- **`tostring` 机制全在 07 §11**(查 `__tostring`、默认格式表)。本文不重复;`print` 与显式 `tostring(v)` 共用
  同一个 `vm.tostring(th, v)`。
- **`__tostring` 重入**:若某参数有 `__tostring`,`vm.tostring` 内部 `callLuaFromHost`(07 §11 / §1.4)→ 重入 +
  可能 GC。`print` 用 Go 缓冲 `buf` 累积(脱离 arena,§3.3),已写入 buf 的内容安全;每个参数的 `tostring` 结果
  立即 `buf.Write`,不跨下一次重入持有 arena 串。**安全,无需 Pin**。
- **默认格式的差分豁免**:`tostring({})`/`tostring(print)` 含对象地址(`table: 0x...`),与官方/gopher-lua 必然
  不同(arena 偏移 vs C 指针)——07 §11 已标「含地址的 tostring 输出差分需豁免」,本文 `print` 同此口径,指向
  [12](./12-testing-difftest.md) 定脱敏比较。

### 4.3 `tonumber`:无 base(共用 parseLuaNumber)/ 带 base(独立进制解析)

```go
// tonumber(v [, base]):无 base 走 parseLuaNumber(07 §5.2);带 base 走任意进制整数解析。
func hostTonumber(vm *VM, th *Thread) int {
    if th.Arg(2) == value.Nil {                   // 无 base 形式
        v := th.Arg(1)
        switch {
        case value.IsNumber(v):
            th.PushValue(v)                       // 已是 number,原样(含其规范 NaN,01 §3.4)
        case value.Tag(v) == value.TagString:
            f, ok := parseLuaNumber(vm.stringBytes(v))  // ★ 07 §5.2 共用:与算术/for 同一套
            if ok { th.PushNumber(f) } else { th.PushNil() }  // 解析失败 → nil(非报错)
        default:
            th.PushNil()                          // table/bool/... → nil
        }
        return 1
    }
    // —— 带 base 形式(2..36 进制整数)——
    s := th.CheckString(1)                        // 第一参必须能当串
    base := th.CheckInt(2)
    if base < 2 || base > 36 {
        return vm.ArgError(th, 2, "base out of range")
    }
    n, ok := parseIntBase(trimSpace(s), int(base))  // 独立解析:仅整数,无小数/指数;前后空白
    if ok { th.PushNumber(float64(n)) } else { th.PushNil() }
    return 1
}
```

- **无 base 用 `parseLuaNumber`**(07 §5.2):**与算术 coercion、数值 for 共用同一套数字解析**(07 §5.2 定稿,
  本文兑现)。接受十进制整数/小数/指数 + 十六进制整数(`0x`),前后空白;**不支持** `0x1p4` 十六进制浮点(5.2+,
  07 §5.2 排除)、`inf`/`nan` 字面量。差分由 12 钉死。
- **带 base 是另一条**(2..36 进制**整数**):`tonumber("ff", 16) == 255`,`tonumber("z", 36) == 35`。**仅整数**
  (不接受小数/指数),前后空白,大小写字母都认(`a-z`/`A-Z` = 10..35)。这是 `tonumber` 专属,**不属于算术
  coercion**(07 §5.2 已声明)。
- **失败返回 nil(非报错)**:`tonumber("abc") == nil`,`tonumber("10x") == nil`。这与 `CheckNumber`(失败报错)
  不同——`tonumber` 是「尝试转换」,失败是正常结果(返回 nil)。

### 4.4 `pairs`/`next`/`ipairs`:遍历序口径(呼应 06,指向 12)

- **`next(t, k)`** 是遍历的核心 host function:`k=nil` 返回第一个键值对;给 `k` 返回其后继。遍历顺序 = **先 array
  段(下标 1..asize 顺序)后 node 段(node 数组物理顺序)**(01 §5.2 布局)。`next` 在 node 段从「k 所在槽的下一个
  非空槽」继续。
- **`pairs(t)`** 返回三元组 `(next, t, nil)`,泛型 for(`for k,v in pairs(t)`)用它:每次 `TFORLOOP`(05 §10.2)
  调 `next(t, ctrl)`,ctrl 从 nil 起步进。**不查 `__pairs`**(5.2+ 引入,07 §2.3 排除)——`pairs` 直接返回 `next`。
- **`ipairs(t)`** 返回 `(ipairsIterator, t, 0)`:迭代器每次取 `t[i+1]`,遇 nil 停。**只遍历数组段连续部分**(从 1
  到首个 nil),不遍历 hash 部分。**不查 `__ipairs`**(5.2 引入 5.3 废,排除)。
- **遍历序的差分口径**(呼应 06 §9.3/§11):`pairs`/`next` 的产出顺序由 table 的 array/node 布局决定,而 node
  布局受**字符串哈希(06 §9.3 JSHash)+ rehash 算法 + Brent 变体**影响。06 §9.3 已把哈希环锁成与官方一致;但
  `pairs` 序是否要求逐字节一致**是验收口径问题**,**由 [12](./12-testing-difftest.md) 定**(严格逐字节 vs 排序后
  比较)。本文 `next`/`pairs` 的实现**遍历序与 01 §5.2 布局 + 06 §9.3 哈希一致**,把口径决策的剩余变量交 12。

### 4.5 `unpack`:5.1 全局(口径)

```go
// unpack(t [, i [, j]]):返回 t[i]..t[j] 多值。【5.1 是全局 unpack,5.2+ 移到 table.unpack】。
func hostUnpack(vm *VM, th *Thread) int {
    tref := th.CheckTable(1)
    t := object.TableAt(vm.arena, tref)
    i := th.OptInt(2, 1)
    j := th.OptInt(3, int64(t.Len()))             // 默认 #t(border,01 §5.2)
    if i > j { return 0 }                          // 空范围:无返回值
    n := int(j - i + 1)
    // 可能扩栈(返回 n 个值压栈):ensureStack(05 §1.4)
    for k := i; k <= j; k++ {
        th.PushValue(t.GetInt(int(k)))            // raw 取(不经 __index,5.1 unpack 语义)
    }
    return n                                        // 返回 n 个值(可变)
}
```

- **5.1 口径:`unpack` 是全局函数**(`unpack({1,2,3})` → `1, 2, 3`)。**5.2+ 把它移到 `table.unpack`**(全局
  `unpack` 在 5.2 废弃)。**P1 锁 5.1:提供全局 `unpack`,不提供 `table.unpack`**(若提供 table.unpack 会与官方
  5.1 多一个函数,差分时 `table.unpack` 在 5.1 是 nil)。**记口径**:差分基准是 5.1,全局 `unpack` 存在、
  `table.unpack` 不存在。
- **返回可变个数**:`unpack` push j-i+1 个值,`return n`。调用点(`CALL C=0` 到 top)接收全部(05 §7.2)。
- **大范围扩栈**:`unpack` 一个大表(`unpack(t)` t 有 1e6 元素)会压 1e6 个值上栈 → `ensureStack` 扩容(05 §1.4)。
  Lua 5.1 有 `unpack` 上限保护(`MAXUPVAL`?实为栈检查):超过栈上限报 `"too many results to unpack"`。**P1 同样
  在 ensureStack 撞上限时报错**(对齐 5.1)。**待 12 核对**措辞。

### 4.6 `collectgarbage` / `gcinfo`:GC 控制(指向 06)

`collectgarbage(opt, arg)` 是 GC 的脚本控制接口,**机制全在 [06](./06-memory-gc.md)**(GC pacing §8.3、collect §8.2、
STW §7.3)。本文只列 opt 字符串 → 行为映射(对齐 Lua 5.1):

| `opt` | 行为 | 返回 | P1 | 06 指针 |
|---|---|---|---|---|
| `"collect"`(默认) | 跑一次 full GC | 0 | ✅ | 06 §8.2 `collect()` |
| `"count"` | 返回当前 arena 使用的 KB 数(浮点,含小数 = 字节/1024) | number | ✅ | 06 §8.3 live 字节 / 1024 |
| `"step"` | 跑一步增量 GC(arg = 步长 KB) | bool(是否完成一轮) | △ | **P1 只 full GC**(06 §8.3:无增量步长);P1 把 `step` 实现为「触发一次 full GC,返回 true」(语义近似,记口径) |
| `"setpause"` | 设 GCPAUSE(arg = 百分比),返回旧值 | number(旧值) | ✅ | 06 §8.3 GCPAUSE(默认 200) |
| `"setstepmul"` | 设步长倍率,返回旧值 | number(旧值) | △ | P1 无增量,`setstepmul` 存值但不生效(记口径) |
| `"stop"` | 停止自动 GC | 0 | ✅ | P1:置 `gcEnabled=false`,Alloc 不主动触发(06 §8.2) |
| `"restart"` | 重启自动 GC | 0 | ✅ | 置 `gcEnabled=true` |

- **`gcinfo()`**(5.1 遗留全局):= `collectgarbage("count")` 的旧形式,返回 KB 数(**整数**,5.1 `gcinfo` 返回
  int KB)。**5.1 有此函数**(已废弃但存在),P1 提供以求 5.1 完整(差分基准里 `gcinfo` 非 nil)。
- **`"count"` 的差分问题**:返回的 KB 数 = arena 存活字节 / 1024,**与官方 Lua 的 C 堆字节数必然不同**(arena
  vs malloc)。这是**可观察但不可逐字节比的项**(类似 tostring 地址)——差分对 `collectgarbage("count")` 的数值
  **不严格比对**(脱敏/范围比较),指向 12。
- **`"step"`/`setstepmul` 的 P1 简化口径**:P1 只 full GC(06 §8.3),无增量步长。`"step"` 近似为「触发 full GC」,
  `setstepmul` 存而不用。脚本若依赖增量步进的精确语义会与官方微差,但**多数脚本只用 `collect`/`count`/`stop`**
  (增量控制罕见),P1 简化覆盖主流。记 §15.2 缺口。

### 4.7 `loadstring`/`load`/`loadfile`/`dofile`/`require`:加载与模块(P1 范围)

**这是 base 库 P1 范围裁剪的重点区**(任务点名:loadstring 用 Compile;require/module 系统 P1 简化或缺口):

| 函数 | P1 实现 | 依赖 | 缺口 |
|---|---|---|---|
| `loadstring(s [,name])` | ✅ **用 `Compile`**(11 §1.3)编译字符串 → 返回 Lua function(主 chunk closure);编译错返回 `(nil, errmsg)` | `Compile` | 编译进**当前 State 的 arena**(常量惰性 intern,11 §1.3);不执行,返回函数供调用 |
| `load(func [,name])` | △ **简化**:5.1 的 `load` 取一个 reader 函数,反复调它拿源码片段拼成完整 chunk 再编译。P1 实现 reader 循环 + Compile | `Compile` + reader 重入(callLuaFromHost) | reader 函数重入(§1.4);P1 可先支持「reader 返回完整串一次」简化形式,完整分块记缺口 |
| `loadfile([fn])` | △ **部分**:读文件字节(io,§8)+ Compile;无 fn 读 stdin | io 文件读 + Compile | 依赖 io 库文件读(§8 P1 范围);P1 至少支持读文件路径 |
| `dofile([fn])` | △ **部分**:= `loadfile` + 立即调用(callLuaFromHost) | loadfile + 重入 | 同 loadfile 依赖 |
| `require(modname)` | **❌/△ 缺口**:5.1 module 系统(`package.loaders`/`package.path`/`package.loaded`/`package.cpath`) | package 库 + 文件查找 | **P1 缺口或极简**:见下 |
| `module(name, ...)` | **❌ 缺口** | package 系统 | P1 不做 |

**`require`/`module`/`package` 系统的 P1 决策**:

> **P1 定稿(经评审:stdlib 范围基准 = 对齐 gopher-lua 提供面,兑现 drop-in 宣称)**。完整 5.1 `require`
> 需要:`package.path`/`package.cpath`(C 模块,望舒无 cgo → **不适用**,roadmap §0)/`package.loaders`/
> `package.loaded`/`package.preload`。
>
> - **P1 范围:对齐 gopher-lua 的 package 实现层级**——实现 `package.loaded`(缓存)+ `package.preload`
>   (宿主预注册模块)+「按 `package.path` 从文件系统查找 `.lua` 并 loadfile」的纯 Lua 加载器。
>   **不做 C 加载器**(无 cgo,gopher-lua 同样没有)。`module()` 以 gopher-lua 实际行为为准
>   (实现期核对其源码:有则对齐、无则同列缺口——**对齐基准是 gopher 面,不是官方完备性**)。
> - **为什么以 gopher-lua 为基准**:P1 的 drop-in 宣称(roadmap §8 / [11](./11-embedding-arena-abi.md) §9)
>   意味着「现有 gopher-lua 宿主的脚本换望舒能跑」——脚本能用到的库面上限就是 gopher-lua 提供面,
>   对齐它即兑现宣称;超出它的部分(官方有而 gopher 无)按 roadmap §5 原则 4 不做完备性。
>
> **记 §15.2 缺口**:实现期逐函数核对 gopher-lua 的 package/module 实际面,登记进裁剪表;
> conformance 测试中超出 gopher 面的 require 用例标注为已知限制。

---

## 5. string 库

### 5.0 string 库经 string 类型公共 metatable.__index 暴露(指向 07)

**关键机制(07 §1.1 / §3.4 定稿,本文兑现)**:string 库不仅是一张全局表 `string`,**它还是所有字符串值的方法
来源**——`("x"):upper()` 能工作,是因为 string 类型有一张**全类型共享的 metatable**,其 `__index = string`(库表)。

- string 库初始化(openlibs,§10):建库表 `string`(含 len/sub/.../gsub 等 host functions),建一张元表 `mt`,
  `mt.__index = string`,把 `mt` 设为 **`typeMetatables[STRING]`**(07 §1.2 per-type 共享元表)。
- `s:upper()` codegen 成 `SELF`(02 §4-11)→ `doSelf` 对 `s["upper"]` → `s` 非 table → `indexMeta`(07 §3.4)→
  取 `typeMetatables[STRING]` 的 `__index` = string 库表 → raw 命中 `string.upper`。完整走通见 **07 §3.4**。
- **本文不重复机制**(07 §3.4 已详述),只声明:string 库表既是 `string.upper(s)` 的来源,也是 `s:upper()` 的来源
  (同一张表,经 string 元表 `__index`)。IC 命中率极高(string 元表与库表常驻不变,07 §3.4)。

### 5.1 string 库清单(语义 + 参数/返回 + P1)

string 库**操作字节**(Lua string 是字节串,01 §5.1),索引 1-based,负索引从尾数(-1 = 末字节):

| 函数 | 语义 | 参数 → 返回 | P1 | 分配/纪律 |
|---|---|---|---|---|
| `string.len(s)` | 字节长 | `(s)` → number | ✅ | 读 String word1 len(01 §5.1),不分配。= `#s` |
| `string.sub(s, i [, j])` | 子串 `[i, j]`(1-based,负从尾) | `(s, i [,j])` → string | ✅ | 新串(§3 纪律:单分配,紧接 push) |
| `string.upper(s)` | 转大写(仅 ASCII A-Z) | `(s)` → string | ✅ | 新串;**仅 ASCII**(locale 见 §5.5) |
| `string.lower(s)` | 转小写(仅 ASCII) | `(s)` → string | ✅ | 新串;仅 ASCII |
| `string.rep(s, n)` | s 重复 n 次 | `(s, n)` → string | ✅ | 新串(可能很大,§5.3);n≤0 返回空串 |
| `string.reverse(s)` | 字节逆序 | `(s)` → string | ✅ | 新串 |
| `string.byte(s [, i [, j]])` | 返回 `[i,j]` 各字节的数值码 | `(s [,i,j])` → number... | ✅ | 多返回值(j-i+1 个 number);不分配串 |
| `string.char(...)` | 各数值码组成串 | `(...)` → string | ✅ | 新串;每参 `CheckInt` 且 0..255(否则报错) |
| `string.format(fmt, ...)` | 格式化(§5.2 指令表) | `(fmt, ...)` → string | ✅ | 新串(§3.3 范例);`%s` 查 `__tostring` 重入 |
| `string.find(s, pat [, init [, plain]])` | 查找模式(返回位置 + 捕获) | `(s, pat [,init,plain])` → (start, end, caps...) or nil | ✅ | **Lua pattern**(§6);plain=true 走纯文本查找 |
| `string.match(s, pat [, init])` | 匹配模式,返回捕获(或整体匹配) | `(s, pat [,init])` → caps... or whole or nil | ✅ | **Lua pattern**(§6);捕获子串分配 |
| `string.gmatch(s, pat)` | 返回迭代器,逐个产出匹配 | `(s, pat)` → function(迭代器) | ✅ | **Lua pattern**(§6);迭代器是 host closure 持状态 |
| `string.gsub(s, pat, repl [, n])` | 全局替换(repl 可为串/表/函数) | `(s, pat, repl [,n])` → (result, count) | ✅ | **Lua pattern**(§6);repl 函数重入 + Pin 捕获(§3.4) |
| `string.dump(f)` | 序列化函数字节码 | `(f)` → string | **❌/△ 缺口** | P1 可缺(§5.6);依赖字节码序列化 |

### 5.2 `string.format` 指令集(差分敏感,指向 12)

`string.format(fmt, ...)` 的格式指令 `%[flags][width][.precision]<conv>`,转换字符 `conv` 集(Lua 5.1
`lstrlib.c` `str_format`,**对齐 C `printf` 子集**):

| conv | 含义 | 参数取法 | 备注/差分 |
|---|---|---|---|
| `d` / `i` | 十进制有符号整数 | `CheckInt`(§2.2 截断取整) | 标准整数格式;flags/width/prec 透传 C 风格 |
| `u` | 十进制无符号整数 | `CheckInt` → 当 unsigned | 5.1 有 `%u`;5.4 废(P1 保留) |
| `o` | 八进制 | `CheckInt` | |
| `x` / `X` | 十六进制(小/大写) | `CheckInt` | |
| `c` | 字符(数值码 → 单字节) | `CheckInt` | `string.format("%c", 65)` → `"A"` |
| `f` / `F` | 定点浮点 | `CheckNumber` | **差分敏感**:精度/舍入须与 C `printf` 一致(§5.2.1) |
| `e` / `E` | 科学计数 | `CheckNumber` | 同上 |
| `g` / `G` | 通用浮点(自动选 f/e) | `CheckNumber` | 同上;默认精度 6 |
| `s` | 字符串(**调 `tostring`**) | `tostring`(查 `__tostring`,07 §11) | **重入风险**(§3.3);prec 截断字节数 |
| `q` | 带引号转义(可安全 `loadstring` 回读) | `CheckString` | **5.1 特定转义规则**(§5.2.2),差分敏感 |
| `%` | 字面 `%` | 无 | `%%` → `%` |

> **5.1 不支持的 conv**:`%a`/`%A`(十六进制浮点,C99 / Lua 5.2+)、长度修饰 `%lld` 等(Lua 自管整数宽,无需)。
> P1 对未知 conv 报 `invalid option '%X' to 'format'`(§3.3 范例),对齐 5.1。

#### 5.2.1 浮点格式的差分一致性(`%f`/`%g`/`%e`)

**这是 stdlib 差分的高发区**(任务点名:string.format 数字格式逐字节一致):

- **`%f`/`%g`/`%e` 必须与 C `printf` 逐字节一致**。Go 的 `fmt.Sprintf("%f", x)` 与 C `printf("%f", x)` 在**多数
  情况一致,但边角有差异**(如 Go 与 C 对 `%g` 的尾零处理、`%e` 的指数位数 `e+05` vs `e+5`、负零 `-0.000000`)。
- **定稿:P1 不直接用 Go `fmt`,而是实现一个 C `printf` 语义对齐的浮点格式化器**(或用 `strconv.AppendFloat`
  + 手工补齐 C 风格的 width/flags/指数位数),**逐字节对齐 Lua 5.1 用的 C `printf`**。这是因为 Lua 5.1 的
  `string.format("%g", x)` 直接转发给 C `lua_number2str`/`sprintf`,望舒要差分一致就必须复刻 C 行为。
- **关键差异点(实现须处理)**:① `%e`/`%g` 的指数至少 2 位(`1e+05` 非 `1e+5`,C 标准);② `%g` 去尾零但保留
  必要位;③ `%.14g`(默认 number→string 格式,05 §4.6)的精度;④ 负零、inf、nan 的输出(C 是 `inf`/`nan`,
  大小写依平台——**Lua 5.1 用 C 的,望舒定稿统一为 `inf`/`-inf`/`nan`,待 12 核对**)。
- **指向 [12](./12-testing-difftest.md)**:format 浮点的逐字节核对是差分套件的核心用例(各种 x + 各种 spec 的笛卡尔积)。

#### 5.2.2 `%q` 转义规则(差分敏感)

`%q` 把字符串转成「可被 Lua 安全读回」的带引号形式(Lua 5.1 `lstrlib.c` `addquoted`):

```
%q 转义(Lua 5.1):
  整体加双引号 "..."
  内部:  "  → \"      \  → \\      换行 → \ + 真换行符
          \r → \r      \0 → \0
          其它控制字符(<32)→ \ddd(十进制)
```

- **5.1 的 `%q` 精确转义集**:双引号、反斜杠、换行(5.1 把换行转成 `\` + 真换行符)、`\0`、`\r`、其它控制字符
  转 `\ddd`。**这套转义是差分敏感的**(每个特殊字符的转义形式须与 5.1 逐字节一致),P1 **不编造**,以 Lua 5.1
  `addquoted` 为准,指向 12 钉死。

### 5.3 `string.rep` / `string.sub`(shadow stack 纪律范例)

```go
// string.rep(s, n):s 重复 n 次。结果可能很大。
func hostStringRep(vm *VM, th *Thread) int {
    s := th.CheckString(1)                        // arena 字节(只读)
    n := th.CheckInt(2)
    if n <= 0 {
        th.PushString(nil)                        // n≤0 → 空串(5.1)
        return 1
    }
    // Go 缓冲累积(§3:规避 arena 中间串),末尾一次 intern
    var buf bytesBuilder
    buf.Grow(len(s) * int(n))                      // 预分配 Go 缓冲
    for k := int64(0); k < n; k++ {
        buf.Write(s)                               // 读 arena s 字节,写 Go buf
    }
    th.PushString(buf.Bytes())                     // 唯一 arena 分配(末尾)
    return 1
}
```

- **`string.rep` 用 Go 缓冲**(§3.2 模式):重复拼接全在 Go `buf`,末尾一次 `intern`。**注意 `s` 在循环中被反复
  读**:`s` 是 `CheckString(1)` 返回的 arena 字节切片;循环里 `buf.Write(s)` 是「读 arena 写 Go buf」**不分配
  arena 对象**,所以循环中**不触发 GC**,`s` 切片不会因 GC 搬迁失效。**安全**。
- **`string.sub(s, i, j)`** 的索引规整(5.1 语义):负索引从尾(`-1` = 末字节);`i` 钳到 ≥1,`j` 钳到 ≤len;
  `i > j` 返回空串。规整后 `[i, j]` 切片 + intern。**待 12 核对**边界(`sub("abc", -100)` 等极端索引)。

---

## 6. Lua 模式匹配(pattern matching)—— string 库最复杂部分

`find`/`match`/`gmatch`/`gsub` 用 **Lua patterns**(模式),**不是 POSIX/PCRE 正则**。Lua pattern 是一套更小、
更快、无回溯灾难担忧的自有语法(Lua 5.1 `lstrlib.c` 的 `match`/`do_match` 递归回溯)。**这是 string 库最复杂、
最差分敏感的部分**——matcher 必须与 5.1 `lstrlib.c` 语义逐字节一致(roadmap §5 原则 2),钉死在 12。

### 6.1 Lua pattern 语法(完整,对齐 5.1)

#### 6.1.1 字符类(character class)

| 类 | 匹配 | 说明 |
|---|---|---|
| `.` | 任意字符(含 `\0`) | |
| `%a` | 字母 | `isalpha`(ASCII;locale 见 §5.5) |
| `%d` | 数字 | `isdigit` |
| `%s` | 空白 | `isspace`(空格/制表/换行等) |
| `%w` | 字母数字 | `isalnum` |
| `%l` | 小写字母 | `islower` |
| `%u` | 大写字母 | `isupper` |
| `%p` | 标点 | `ispunct` |
| `%c` | 控制字符 | `iscntrl` |
| `%x` | 十六进制数字 | `isxdigit` |
| `%A` `%D` `%S` `%W` `%L` `%U` `%P` `%C` `%X` | **大写 = 对应小写类的补集** | `%A` = 非字母,等 |
| `%<其它字符>` | 转义该字符本身 | `%.` = 字面点,`%%` = 字面 `%`,`%(` = 字面括号 |
| `[set]` | 字符集(集合内任一) | `[abc]`、`[%a%d]`(类可入集)、`[0-9]`(范围)、`[^...]`(补集,首字符 `^`) |
| 字面字符 | 匹配自身 | 非魔术字符(`^$*+?.([%-` 之外)匹配自身 |

#### 6.1.2 量词(quantifier,跟在单字符类之后)

| 量词 | 含义 | 贪婪/最小 |
|---|---|---|
| `*` | 0 或多次 | **贪婪**(尽量多,失败回退) |
| `+` | 1 或多次 | **贪婪** |
| `-` | 0 或多次 | **最小**(尽量少,与 `*` 相反;Lua 特有,非正则的 `*?`) |
| `?` | 0 或 1 次 | 可选 |

> **Lua 量词只作用于单个字符类**(不像正则有 `(...)*` 分组量词)。`a*` 合法,`(ab)*` **不是** Lua pattern
> (Lua 无分组量词)——这是 Lua pattern 比正则简单、且**无指数回溯灾难**的关键。

#### 6.1.3 锚(anchor)与特殊构造

| 构造 | 含义 | 说明 |
|---|---|---|
| `^` | **模式起始锚**(仅在 pattern 第一个字符时) | `^abc` 仅匹配串首;pattern 中间的 `^` 是字面 `^` |
| `$` | **模式结束锚**(仅在 pattern 最后一个字符时) | `abc$` 仅匹配串尾;中间的 `$` 是字面 `$` |
| `()` | **捕获**(capture) | 捕获括号内匹配的子串(§6.1.4) |
| `()`(空捕获) | **位置捕获**(position capture) | 空括号捕获**当前位置**(一个数字,非子串),如 `()aa()` 捕获 aa 前后的位置 |
| `%b<x><y>` | **平衡匹配**(balanced match) | 匹配从 x 到配对 y 的平衡串(如 `%b()` 匹配配对括号);计数 x/y 平衡 |
| `%f[set]` | **前沿模式**(frontier pattern) | **5.1 有**;匹配「前一字符不在 set、当前字符在 set」的边界(零宽);串首前视为 `\0` |
| `%1`..`%9` | **反向引用**(back reference) | 匹配第 n 个捕获已捕获的子串(如 `(%a)%1` 匹配双字母 `aa`) |

> **5.1 确认有 `%f`**:任务点名「%f 前沿(5.1 有)」——Lua 5.1.x 的 `lstrlib.c` 确实有 `%f`(frontier pattern,
> 5.1 引入)。P1 实现它。`%b` 平衡匹配、`%1`-`%9` 反向引用、位置捕获 `()` 均为 5.1 特性,P1 全做。

### 6.2 matcher 设计概要(递归回溯,对齐 5.1 lstrlib.c)

**核心算法:递归回溯匹配**(Lua 5.1 `lstrlib.c` 的 `match` 函数)。matcher 维护一个 `MatchState`,从源串某位置
`s` 与模式某位置 `p` 开始尝试匹配,递归推进:

```go
// internal/stdlib —— pattern matcher 状态(对齐 Lua 5.1 lstrlib.c MatchState)
type matchState struct {
    src      []byte          // 源串(arena String 字节,只读)
    pat      []byte          // 模式串(arena String 字节,只读)
    level    int             // 当前已开启的捕获数
    capture  [maxCaptures]struct {
        init int             // 捕获起始位置(src 下标)
        len  int             // 捕获长度;特殊值 capUnfinished(-1)= 未闭合;capPosition(-2)= 位置捕获
    }
    // matchdepth:防递归过深(5.1 用 MAXCCALLS 等价上限,见下)
    matchDepth int
}

const (
    maxCaptures   = 32       // Lua 5.1 LUA_MAXCAPTURES = 32
    capUnfinished = -1
    capPosition   = -2
)

// match:从 src[s] 与 pat[p] 开始匹配。成功返回匹配结束位置(src 下标),失败返回 -1。
// 这是递归回溯的核心(对齐 5.1 lstrlib.c `match`)。
func (ms *matchState) match(s, p int) int {
    if ms.matchDepth--; ms.matchDepth == 0 {       // 防递归过深(5.1 MAXCCALLS)
        ms.error("pattern too complex")
    }
    defer func() { ms.matchDepth++ }()
    if p == len(ms.pat) {                           // 模式耗尽 → 匹配成功到 s
        return s
    }
    switch ms.pat[p] {
    case '(':
        if p+1 < len(ms.pat) && ms.pat[p+1] == ')' {
            return ms.startCapture(s, p+2, capPosition)   // 位置捕获 ()
        }
        return ms.startCapture(s, p+1, capUnfinished)     // 普通捕获 (
    case ')':
        return ms.endCapture(s, p+1)                       // 闭合捕获
    case '$':
        if p+1 == len(ms.pat) {                            // $ 在模式末尾 = 结束锚
            if s == len(ms.src) { return s }               // 仅当 src 也耗尽
            return -1
        }
        // 否则 $ 是字面字符,落到 default
    case '%':
        switch {
        case p+1 < len(ms.pat) && ms.pat[p+1] == 'b':
            return ms.matchBalance(s, p+2)                 // %bxy 平衡匹配
        case p+1 < len(ms.pat) && ms.pat[p+1] == 'f':
            return ms.matchFrontier(s, p+2)                // %f[set] 前沿
        case p+1 < len(ms.pat) && isDigit(ms.pat[p+1]):
            return ms.matchBackref(s, p+1)                 // %1..%9 反向引用
        }
        // 否则 %x 是转义字符类,落到 default 的单字符类匹配
    }
    // —— 默认:单字符类 + 可能的量词 ——
    ep := ms.classEnd(p)                                    // 单字符类的模式结束位置(跨过 %x / [set])
    matches := s < len(ms.src) && ms.singleMatch(ms.src[s], p, ep)  // 当前字符是否匹配该类
    if ep < len(ms.pat) {
        switch ms.pat[ep] {                                 // 量词
        case '?':
            if matches {
                if r := ms.match(s+1, ep+1); r != -1 { return r }  // 试匹配一次
            }
            return ms.match(s, ep+1)                        // 试匹配零次
        case '+':
            if matches { return ms.maxExpand(s+1, p, ep) }  // 1+ 贪婪
            return -1
        case '*':
            return ms.maxExpand(s, p, ep)                   // 0+ 贪婪
        case '-':
            return ms.minExpand(s, p, ep)                   // 0+ 最小
        }
    }
    // 无量词:必须匹配一次,推进
    if !matches { return -1 }
    return ms.match(s+1, ep)
}
```

**关键子过程**:

- **`maxExpand`(贪婪 `*`/`+`)**:尽量多匹配该字符类,然后从最长往回**回溯**——每次失败退一个字符,重试模式
  剩余部分。这是「贪婪 + 回溯」的核心。
- **`minExpand`(最小 `-`)**:从零次开始,每次试匹配模式剩余,失败才多吃一个字符。与 `maxExpand` 方向相反。
- **`singleMatch`**:判断 `src[s]` 是否匹配 `pat[p..ep)` 这个单字符类(`.`/`%a`/`[set]`/字面),含补集(大写类、
  `[^...]`)处理。
- **`startCapture`/`endCapture`**:开启/闭合捕获,记录 `capture[level].init/len`。位置捕获记 `capPosition`。
- **`matchBalance`(`%b`)**:从 `src[s]` 必须是 x,计数 x +1 / y -1,到计数归零的 y 位置。
- **`matchFrontier`(`%f`)**:零宽断言,检查 `src[s-1]`(串首前视为 `\0`)不在 set 且 `src[s]` 在 set。
- **`matchBackref`(`%1`-`%9`)**:取第 n 个已闭合捕获的子串,要求 `src[s..]` 以它开头。

> **递归回溯而非 NFA/DFA**:Lua pattern 用直接递归回溯(不编译成自动机)。优点:实现简单、与 5.1 语义天然一致、
> 捕获/位置捕获/反向引用/平衡匹配这些非正则特性自然支持。**无指数回溯灾难**:因为 Lua 量词只作用单字符类
> (无 `(...)*` 嵌套量词,§6.1.2),回溯空间受限。**P1 直接移植 5.1 `lstrlib.c` 的 `match` 递归结构**,逐字节
> 对齐语义。

### 6.3 顶层匹配驱动 + 锚处理

`find`/`match`/`gsub` 的顶层逻辑:从 `init` 位置起,**逐位置尝试 `match`**(除非 `^` 锚定串首):

```go
// string.find/match 的顶层匹配驱动(简化)。
func (ms *matchState) doMatch(init int) (matchStart, matchEnd int, ok bool) {
    anchor := len(ms.pat) > 0 && ms.pat[0] == '^'   // ^ 锚:仅试串首(init 位置)
    p := 0
    if anchor { p = 1 }                              // 跳过 ^
    s := init
    for {
        ms.level = 0                                 // 重置捕获
        ms.matchDepth = maxMatchDepth
        if e := ms.match(s, p); e != -1 {
            return s, e, true                        // 匹配成功:[s, e)
        }
        s++                                          // 失败:下一个起始位置
        if anchor || s > len(ms.src) {
            return 0, 0, false                       // ^ 锚定只试一次;或源串耗尽
        }
    }
}
```

- **`^` 锚定**:模式以 `^` 开头时只在 `init` 位置试一次(不滑动)。这是 `find`/`match` 区分「锚定 vs 任意位置」
  的关键。
- **逐位置滑动**:无 `^` 时,从 `init` 开始每个位置都试 `match`,首个成功即返回。
- **空匹配**:模式可匹配空串(如 `a*` 匹配零个 a),`gmatch`/`gsub` 需处理空匹配的位置推进(避免死循环:空匹配
  后强制前进一字符),对齐 5.1。

### 6.4 捕获的提取与四个函数的差异

匹配成功后,`capture[0..level)` 是捕获结果。提取规则(对齐 5.1 `push_captures`):

- **有捕获**:返回各捕获子串(位置捕获返回数字位置)。
- **无捕获**:返回**整体匹配**作为单一捕获(`match` 返回整体匹配串;`find` 返回 start/end)。

| 函数 | 返回 | 捕获处理 |
|---|---|---|
| `string.find(s,p,init,plain)` | `(start, end [, caps...])` 或 nil | 返回**位置** start/end(1-based);**有捕获**则追加捕获;`plain=true` 跳过 pattern 走 `bytes.Index`(纯文本) |
| `string.match(s,p,init)` | `caps...` 或 整体匹配 或 nil | 有捕获返回捕获,无捕获返回整体匹配串 |
| `string.gmatch(s,p)` | 迭代器函数 | 迭代器每次调返回下一个匹配的捕获(无则整体);**持状态**(下次搜索起点) |
| `string.gsub(s,p,repl,n)` | `(result, count)` | 每个匹配用 repl 替换(repl 是串/表/函数,§6.5);最多 n 次;返回结果串 + 替换次数 |

- **捕获子串分配**:每个捕获子串从 `src` 切出并 **intern 进 arena**(成为真 String)。这是 string 库的分配点
  (§3.1)——`match`/`gmatch`/`gsub` 产捕获串。**纪律**:多个捕获在 push 进 Lua 栈前持有于 Go slice → 若中间
  有分配(如下一个捕获 intern 或调 repl 函数),已持有的要 Pin(§3.4 gsub 范例)。
- **`gmatch` 迭代器是 host closure 持状态**:`gmatch(s, p)` 返回一个 host closure(01 §5.3 host 闭包),其
  upvalue 持 `(s, p, 当前搜索位置)`。每次调用推进位置、返回下一匹配。位置存在 host closure 的 upvalue(Value),
  随状态更新。

### 6.5 `string.gsub` 的 repl 三态(串/表/函数)

`gsub(s, pat, repl, n)` 的 `repl` 决定每个匹配如何被替换(对齐 5.1 `str_gsub`):

| repl 类型 | 替换为 | 说明 |
|---|---|---|
| **string** | 模板串,`%1`-`%9` 替换为对应捕获,`%0` = 整体匹配,`%%` = 字面 `%` | `gsub("hello","(l)","[%1]")` → `he[l][l]o` |
| **table** | `repl[capture1]`(用第一个捕获作键查表) | 查到非 nil/false 用之,否则保留原匹配 |
| **function** | `repl(captures...)` 的返回值 | **重入**(§1.4 / §3.4);返回 nil/false 保留原匹配,否则用返回值(须 string/number) |

- **string repl**:模板里 `%n` 引用捕获——这是**替换期**的捕获引用(区别于 pattern 里的反向引用 `%1`)。`%0`/`%%`
  特殊。结果用 Go 缓冲累积(§3.3),末尾 intern。
- **table/function repl**:用第一个捕获(或整体匹配)查表 / 调函数。**function repl 重入 + Pin 捕获**(§3.4 范例:
  调 repl 前 Pin captures,防 GC 回收)。返回 **nil/false** 时保留**原匹配文本**(5.1 语义:替换函数「不想替换就
  返回 nil」)。返回非 string/number 报错。
- **count 返回值**:`gsub` 第二返回值是**替换次数**(实际匹配数,受 n 限);`n` 缺省则替换所有。

### 6.6 pattern matcher 的 P1 范围与差分

**P1 全做 Lua 5.1 pattern 全集**(字符类、量词、锚、捕获、位置捕获、`%b`、`%f`、`%1`-`%9`)——**因为 pattern
是 string 库的核心,缺任何一个都会让大量真实脚本的 `find`/`gsub` 行为与 5.1 分叉**。这不是可裁剪项。

- **差分敏感性极高**:matcher 的每个分支(贪婪回溯顺序、空匹配推进、捕获边界、`%f` 的边界语义)都必须与 5.1
  `lstrlib.c` 逐字节一致。**P1 直接移植 5.1 的 `match` 递归结构**(§6.2),不重新设计算法,以求语义等价。
- **指向 [12](./12-testing-difftest.md)**:pattern 是差分套件的重点——大量 `(s, pattern)` 组合的匹配结果、捕获、
  位置、gsub 结果与 5.1 逐字节核对。这是「string 库最复杂部分」的验收主战场。
- **`matchDepth` 上限**:递归回溯有深度上限(5.1 `MAXCCALLS` 等价,防 `("x"):match(("a?"):rep(100).."b")` 类
  深递归爆栈)。超限报 `"pattern too complex"`(对齐 5.1)。**待 12 核对**精确上限与措辞。

---

## 7. table 库

### 7.1 table 库清单(语义 + 参数/返回 + P1)

| 函数 | 语义 | 参数 → 返回 | P1 | 备注 |
|---|---|---|---|---|
| `table.insert(t, [pos,] v)` | 插入 v(默认末尾,或指定 pos 处后移) | `(t, [pos,] v)` → 无 | ✅ | 两形式:`insert(t,v)` 末尾;`insert(t,pos,v)` 插入并后移(§7.2) |
| `table.remove(t [, pos])` | 移除并返回(默认末尾,或指定 pos 处前移) | `(t [,pos])` → removed value | ✅ | 边界语义(§7.2);返回被移除值 |
| `table.concat(t [, sep [, i [, j]]])` | 拼接数组段(§3.2 范例) | `(t [,sep,i,j])` → string | ✅ | Go 缓冲累积(§3.2);元素须 string/number |
| `table.sort(t [, comp])` | 原地排序数组段(§7.3/§7.4) | `(t [,comp])` → 无 | ✅ | comp 比较器重入(§7.4);默认用 `<`(`__lt`) |
| `table.maxn(t)` | 返回最大正数字键(可非连续) | `(t)` → number | ✅ | **5.1 有 `table.maxn`**(5.2+ 移除——记口径,§7.5) |
| `table.getn(t)` / `table.setn(t, n)` | 取/设序列长度(5.0 遗留) | — | **❌ 5.1 已废** | 5.0 遗留;**5.1 已 deprecated**(`getn` = `#t`,`setn` 空操作)——P1 可缺(§7.5) |

> **5.2+ 排除项**:`table.unpack`(5.1 是全局 `unpack`,§4.5)、`table.pack`(5.2 引入)、`table.move`(5.3)。
> P1 不提供它们(提供会与 5.1 多函数,差分时它们应是 nil)。

### 7.2 `table.insert` / `table.remove` 边界语义

```go
// table.insert:两形式。
//   insert(t, v)      → t[#t+1] = v(末尾追加)
//   insert(t, pos, v) → 把 [pos, #t] 后移一位,t[pos] = v(插入)
func hostTableInsert(vm *VM, th *Thread) int {
    tref := th.CheckTable(1)
    t := object.TableAt(vm.arena, tref)
    n := t.Len()                                   // #t(border,01 §5.2)
    switch th.NArgs() {
    case 2:                                         // insert(t, v):末尾
        t.SetInt(n+1, th.Arg(2))                    // t[n+1] = v(可能 rehash → bump gen,05 §6.5)
    case 3:                                         // insert(t, pos, v):插入
        pos := int(th.CheckInt(2))
        if pos < 1 || pos > n+1 {
            return vm.ArgError(th, 2, "position out of bounds")  // 5.1 边界检查
        }
        for k := n; k >= pos; k-- {                 // [pos, n] 后移一位
            t.SetInt(k+1, t.GetInt(k))
        }
        t.SetInt(pos, th.Arg(3))
    default:
        return vm.Errorf(th, "wrong number of arguments to 'insert'")
    }
    reloadAfterMaybeRehash(vm)                       // SetInt 可能 rehash 分配(safepoint 已在 SetInt 内)
    return 0
}
```

- **`insert` 两形式**:2 参 = 末尾追加;3 参 = 指定位置插入并后移。**参数个数区分**(`NArgs()`),5.1 语义。
- **`insert` 的 pos 边界**:`pos ∈ [1, n+1]`(n = #t)。越界报 `"bad argument #2 to 'insert' (position out of bounds)"`
  (**5.1 行为**;5.2 起 insert 的越界检查更严)。**待 12 核对**精确措辞。
- **`remove(t, pos)`**:移除 `t[pos]`,`[pos+1, n]` 前移一位,返回被移除值。`pos` 默认 `#t`(移除末尾)。`remove`
  空表(`#t==0`)返回 nil(5.1)。边界细节(`remove(t, 0)`/越界)**待 12 核对**。
- **不查元方法**:`insert`/`remove` 用 raw 访问(`GetInt`/`SetInt`,不经 `__index`/`__newindex`)——5.1 table 库
  直接操作数组段。`#t` 用 border(01 §5.2)。

### 7.3 `table.sort` 默认比较 + 算法(对齐 5.1)

`table.sort(t, comp)` **原地排序** `t[1..#t]`。默认用 `<`(即 `a < b`,会触发 `__lt`,07 §9.2);提供 `comp`
则用 `comp(a, b)`(返回真表示 a 应排在 b 前)。

```go
// table.sort(t, comp):原地快排数组段。comp 缺省用 lessThan(07 §9.2,可能 __lt)。
func hostTableSort(vm *VM, th *Thread) int {
    tref := th.CheckTable(1)
    t := object.TableAt(vm.arena, tref)
    n := t.Len()
    comp := th.Arg(2)                              // 可选比较器(nil = 默认 <)
    // 快排(对齐 5.1 ltablib.c 的 quicksort;非稳定)
    if e := vm.quicksort(th, t, 1, n, comp); e != nil {
        return vm.raiseErr(e)                       // 比较器出错冒泡(§7.4)
    }
    return 0
}
```

- **算法 = 快排(quicksort),对齐 5.1 `ltablib.c`**:Lua 5.1 用快排(带中位数取 pivot + 小区间插入排序优化)。
  **非稳定排序**(相等元素相对顺序不保证)。**P1 移植 5.1 `auxsort` 的快排结构**——这关系到「相等元素的最终
  顺序」是否与 5.1 一致(差分敏感,§7.6)。
- **默认比较用 `lessThan`**(07 §9.2):`a < b`,可触发 `__lt` 元方法(对象排序)。number 走 IEEE `<`,string 走
  字典序(05 §4.4)。
- **比较器 `comp` 的契约**:`comp(a, b)` 返回真 = 「a 在 b 前」。**comp 必须定义严格弱序**(5.1 不检查,若 comp
  不一致 Lua 5.1 可能报 `"invalid order function for sorting"` 或行为未定义)。**待 12 核对**:5.1 对无效比较器的
  检测(`auxsort` 有「partition 越界」检查报错)。

### 7.4 `table.sort` 比较器重入 + shadow stack 纪律(任务点名)

**这是 table 库最有设计含量的部分**(任务点名:sort 比较器触发元方法/重入,shadow stack 纪律):

`table.sort` 在快排过程中**反复调用比较器**(默认 `<` 触发 `__lt`,或自定义 `comp`)——每次比较都可能 **reentry
Lua / 调 host / 触发 GC**:

```go
// quicksort 的比较调用(简化):每次比较经 callLuaFromHost(comp 是 Lua/host)或 lessThan(默认 <)。
func (vm *VM) sortLess(th *Thread, a, b value.Value, comp value.Value) (bool, *LuaError) {
    if comp == value.Nil {
        return vm.lessThanValue(a, b)              // 默认 <(07 §9.2,可能 __lt reentry)
    }
    // 自定义比较器:callLuaFromHost(comp, {a, b})(05 §7.3 reentry,加 Go 栈 + nCcalls)
    res, err := vm.callLuaFromHost(th, comp, []value.Value{a, b}, 1)
    if err != nil {
        return false, err                          // 比较器出错:上抛(§1.4)
    }
    return value.Truthy(res[0]), nil               // 返回值取真值
}
```

**重入 + GC 纪律分析(关键)**:

1. **比较器调用 = reentry**:`comp(a, b)` 经 `callLuaFromHost`(05 §7.3)——**加一层 Go 栈 + 一个 `nCcalls`**
   (05 §7.4)。但**每次比较是「调用 → 返回 → 下次比较」串行**,不嵌套累积——所以**Go 栈深度是 O(1)**(同一时刻
   只有一层比较器在跑),**不是 O(n log n)**。这与 §1.4 一致:`callLuaFromHost` 返回后 reentry 结束,栈退回。
2. **比较器可能 GC + 搬迁 arena**:`comp` 内可分配 → GC → arena 搬迁。`quicksort` 操作的是 **table 的数组段**
   (arena 内对象,经 `t.GetInt`/`t.SetInt`)——**表本身在 arena,是 GC 根可达**(经 Lua 栈上的 `t` 参数,
   06 §5.1 R5),GC 不会回收它。**但 `quicksort` 在 Go 局部持有的「待比较的两个元素值 a/b」**:
   - 若 a/b 是 **arena 对象引用**(string/table 元素),它们在 Go 局部(`sortLess` 的参数)→ GC 看不见(06 §5.1 R7)
     → **调 comp(内部 GC)前必须保证 a/b 可达**。
   - **但 a/b 来自 `t.GetInt(i)`(表元素),它们仍在表里**(`t[i]`/`t[j]` 槽未被覆盖)→ **经表(R5 可达的 t)
     可达** → GC 不回收。所以**只要 a/b 还在表槽里(比较期间不移动它们),就无需额外 Pin**。
   - **风险点:partition 移动元素时**。快排 partition 会 `SetInt` 移动元素(交换 `t[i]`/`t[j]`)。若在「已从槽
     读出 a 到 Go 局部、但尚未写回 / 槽已被覆盖」的窗口里调 comp,a 可能不在任何表槽 → 需 Pin。**定稿纪律**:
     **partition 的「读出-比较-交换」序列中,被读到 Go 局部但暂时不在表槽的元素,在调 comp 前 Pin**(§3 纪律);
     或**安排算法使「比较时两元素都还在表槽里」**(更优:比较只读不移,移动在比较之后)。
3. **比较器修改表(诡异但合法)**:5.1 允许 comp 内修改正在排序的表(`t`)——这会导致未定义行为(Lua 5.1 不防)。
   P1 同 5.1:不防,可能 partition 越界报错(`"invalid order function"`)。**这不是纪律问题**(正确性由 5.1 语义
   兜底),只是行为与 5.1 一致即可。
4. **比较器出错冒泡**:comp 内 `error` → `callLuaFromHost` 返回 `*LuaError` → `sortLess` 返回它 → `quicksort`
   上抛 → `hostTableSort` `raiseErr` 冒泡(09 §2)。排序中途出错,表处于**部分排序状态**(5.1 同此,不回滚)。

> **定稿:sort 比较的纪律是「比较时两元素保持在表槽内(R5 可达),partition 移动在比较之后」**——这样默认情形
> **无需显式 Pin**(元素经表可达)。仅当算法实现把元素读出到 Go 局部并跨 comp 调用时,才 Pin(§3)。`quicksort`
> 实现应优先采用「先比较定序、后交换」的结构(5.1 `auxsort` 即如此:`sort_comp` 只读两元素,交换是独立步骤),
> 把 Pin 需求降到最低。**这是 sort 重入纪律的占优结构。**

### 7.5 `table.maxn` / `getn` / `setn`(5.1 口径)

- **`table.maxn(t)`**:返回 t 中**最大的正数字键**(可非连续,不同于 `#t`)。`{[1]=1,[100]=1}` 的 `maxn` = 100,
  `#t` 可能是 1。**5.1 有 `table.maxn`**(任务点名),遍历表找最大正数字键。**5.2+ 移除**(记口径:差分基准 5.1
  有 maxn,5.2+ 无)。P1 提供。
- **`table.getn(t)` / `table.setn(t, n)`**:Lua **5.0 遗留**,5.1 已 **deprecated**:`getn(t)` 等价 `#t`,`setn`
  是空操作(5.1 表无显式长度字段,01 §5.2)。**P1 可缺**(若提供:`getn` = `#t`,`setn` 空操作)。这两个 5.1
  程序极少用(已废),**记 §15.2 缺口**:P1 默认不提供,若 conformance 需要再补为 `#t`/空操作。

### 7.6 table.sort 稳定性的差分(指向 12)

- **快排非稳定**:相等元素的相对顺序在快排后不保证。**与 5.1 的差分**:5.1 用快排也非稳定,但**具体的 pivot
  选择 + partition 顺序决定了相等元素的最终排列**。**若 P1 的快排实现与 5.1 `auxsort` 的 pivot/partition 不一致,
  相等元素的最终顺序会与 5.1 不同 → 差分失败**(若差分把 sort 后的表逐元素比对)。
- **定稿:P1 移植 5.1 `ltablib.c` `auxsort` 的精确结构**(中位数 pivot、相同的 partition 扫描方向),使相等元素
  排列与 5.1 一致。**指向 [12](./12-testing-difftest.md)**:sort 差分用「含相等元素的数组 + 各种比较器」核对最终
  排列逐元素一致。这是 table 库的差分敏感项之一。

---

## 8. math 库

### 8.1 math 库清单(直接映射 Go math 包 + 语义差异)

math 库**绝大多数直接映射 Go `math` 包**(清单类精炼,不逐个展开伪码),重点标注**语义差异**(任务点名:
floor 返回 double、random seed/范围、fmod vs Lua `%`):

| 函数 | 语义 | Go 映射 | P1 | 5.1 / 差异备注 |
|---|---|---|---|---|
| `math.abs(x)` | 绝对值 | `math.Abs` | ✅ | |
| `math.ceil(x)` | 向上取整 | `math.Ceil` | ✅ | **返回 double**(不是整数,Lua 无整数,§8.2) |
| `math.floor(x)` | 向下取整 | `math.Floor` | ✅ | **返回 double**(§8.2) |
| `math.sqrt(x)` | 平方根 | `math.Sqrt` | ✅ | 负数 → NaN(规范化,01 §3.4) |
| `math.sin/cos/tan` | 三角 | `math.Sin/Cos/Tan` | ✅ | 弧度 |
| `math.asin/acos/atan` | 反三角 | `math.Asin/Acos/Atan` | ✅ | |
| `math.atan2(y, x)` | 二参反正切 | `math.Atan2` | ✅ | **5.1 有 `atan2`**(5.3 起 atan 可二参,atan2 废;5.1 保留) |
| `math.sinh/cosh/tanh` | 双曲 | `math.Sinh/Cosh/Tanh` | ✅ | **5.1 有**(5.3 移除——记口径) |
| `math.exp(x)` | e^x | `math.Exp` | ✅ | |
| `math.log(x)` | 自然对数 | `math.Log` | ✅ | **5.1 只一参**(5.2 起 `log(x,base)` 二参;5.1 单参) |
| `math.log10(x)` | 常用对数 | `math.Log10` | ✅ | **5.1 有 `log10`**(5.2+ deprecated——记口径) |
| `math.pow(x, y)` | x^y | `math.Pow` | ✅ | **5.1 有 `math.pow`**(5.2 deprecated,用 `^`;5.1 保留) |
| `math.fmod(x, y)` | C fmod(截断取余) | `math.Mod` | ✅ | **≠ Lua `%`**(§8.3):fmod 截断,`%` 是 floor 取模 |
| `math.modf(x)` | 拆整数/小数部分 | `math.Modf` | ✅ | 返回两值(整数部分 double + 小数部分) |
| `math.max(...)` | 最大值 | 遍历 `>` | ✅ | 至少一参;用数值比较 |
| `math.min(...)` | 最小值 | 遍历 `<` | ✅ | |
| `math.random([m [, n]])` | 伪随机 | `math/rand` | ✅ | 范围语义(§8.4) |
| `math.randomseed(x)` | 设种子 | `rand.Seed` | ✅ | (§8.4) |
| `math.huge` | +∞ | `math.Inf(1)` | ✅ | 常量(`HUGE_VAL`) |
| `math.pi` | π | `math.Pi` | ✅ | 常量 |
| `math.rad(x)` | 度→弧度 | `x*π/180` | ✅ | |
| `math.deg(x)` | 弧度→度 | `x*180/π` | ✅ | |
| `math.frexp(x)` | 拆尾数/指数 | `math.Frexp` | ✅ | 返回 (尾数, 指数) |
| `math.ldexp(m, e)` | `m * 2^e` | `math.Ldexp` | ✅ | |

> **5.2+ 排除/口径**:`math.tointeger`/`math.type`/`math.maxinteger`/`math.mininteger`(5.3 整数子类型相关)——
> **5.1 无整数子类型(roadmap §6),全排除**。`math.log(x, base)` 二参形式(5.2)——**P1 单参**(5.1)。

### 8.2 floor/ceil 返回 double(Lua 无整数)

**关键 5.1 口径**:`math.floor(3.7)` 返回 **`3.0`(double)**,不是整数 `3`——因为 Lua 5.1 number 恒 double
(01 §3.3,roadmap §6 无整数子类型)。

- Go 的 `math.Floor` 返回 `float64`,直接 `value.NumberValue(math.Floor(x))`——**天然是 double,无需转整数**。
- **与 5.3+ 的差异**:5.3 起 `math.floor` 返回**整数子类型**(`3` 而非 `3.0`)。P1 锁 5.1:返回 double。差分时
  `math.floor(3.7)` 在 5.1 是 `3`(但内部是 double,`tostring` 为 `"3"`),与 5.3 的整数 `3` 在 `tostring` 上
  **巧合一致**(都 `"3"`),但 `math.type` 不存在于 5.1(§8.1 排除),所以差分无碍。

### 8.3 `math.fmod` vs Lua `%`(语义差异,易错)

**`math.fmod` 与 Lua 的 `%` 运算符语义不同**(任务点名):

| | `math.fmod(x, y)` | Lua `x % y`(MOD opcode,05 §4.1) |
|---|---|---|
| 定义 | **截断取余**(C `fmod`):`x - trunc(x/y)*y` | **floor 取模**:`x - floor(x/y)*y`(05 §4.1) |
| 符号 | 结果符号同 **x**(被除数) | 结果符号同 **y**(除数) |
| 例 | `fmod(-5, 3)` = **-2** | `-5 % 3` = **1** |
| 例 | `fmod(5, -3)` = **2** | `5 % -3` = **-1** |

- **`math.fmod` = Go `math.Mod`**(C fmod 语义,截断)。**Lua `%` = `luaMod`**(05 §4.1 的 `a - floor(a/b)*b`)。
  **两者对负数结果不同**——这是高频混淆点。实现时 `math.fmod` 用 `math.Mod`,`%` 运算符用 05 §4.1 的 `luaMod`,
  **不可混用**。
- **差分敏感**:`math.fmod` 与 `%` 对负操作数的结果必须各自与 5.1 一致(5.1 `math.fmod` 转发 C `fmod`,`%` 用
  `luai_nummod`)。指向 12 核对负数用例。

### 8.4 `math.random` 范围 + seed(语义)

```go
// math.random([m [, n]]):三形式(对齐 5.1 math_random)。
func hostMathRandom(vm *VM, th *Thread) int {
    r := vm.randState.Float64()                    // [0, 1) 均匀(math/rand)
    switch th.NArgs() {
    case 0:                                         // random() → [0, 1)
        th.PushNumber(r)
    case 1:                                         // random(m) → [1, m] 整数
        m := th.CheckInt(1)
        th.PushNumber(float64(1 + int64(r*float64(m))))   // floor(r*m)+1 ∈ [1,m]
    case 2:                                         // random(m, n) → [m, n] 整数
        m, n := th.CheckInt(1), th.CheckInt(2)
        th.PushNumber(float64(m + int64(r*float64(n-m+1))))
    }
    return 1
}
```

- **三形式范围语义(5.1)**:① `random()` → 实数 `[0, 1)`;② `random(m)` → 整数 `[1, m]`;③ `random(m, n)` →
  整数 `[m, n]`(闭区间)。**注意 `random(m)` 是 `[1, m]` 而非 `[0, m)`**(易错)。
- **seed 与差分的根本矛盾**:`math.random` 的**具体输出序列**取决于 RNG 算法 + seed。**Go `math/rand` 的算法与
  Lua 5.1 用的 C `rand()`/`random()` 不同** → **相同 seed 产生不同序列** → **`math.random` 的输出无法与 5.1
  逐字节一致**。这是**根本性差分豁免项**(类似 tostring 地址):
  - **定稿:`math.random` 输出不纳入逐字节差分**(算法不同必然不同)。差分套件**排除依赖 random 具体值的用例**,
    或只验证**范围正确性**(`random(1,6) ∈ [1,6]`)而非具体序列。指向 [12](./12-testing-difftest.md) 定豁免。
  - **可选(若需更强一致)**:P1 可**复刻 Lua 5.1 用的 C `rand` 算法**(若平台 C `rand` 是已知 LCG)使序列一致——
    但 C `rand` 实现平台相关(glibc vs BSD 不同),5.1 本身跨平台 random 就不一致,**复刻无意义**。**定稿:用
    Go `math/rand`,random 序列差分豁免**(记 §15.2 缺口)。
- **`randomseed(x)`**:设 RNG 种子(`rand.Seed(int64(x))`)。同 seed 在**同一 VM 实现内**可复现,但跨实现(vs 5.1)
  不可复现(算法不同)。

---

## 9. os 库

### 9.1 os 库清单(纯 Go 实现,跨平台)

os 库**纯 Go 实现**(roadmap §0 禁 cgo),用 Go `time`/`os` 包。跨平台一致性是关注点:

| 函数 | 语义 | Go 映射 | P1 | 安全/差异备注 |
|---|---|---|---|---|
| `os.time([t])` | 当前时间戳(秒);给 table t 则按字段算 | `time.Now().Unix()` / `time.Date` | ✅ | t 是 `{year,month,day,hour,min,sec,...}`;返回 number(秒) |
| `os.clock()` | 进程 CPU 时间(秒,浮点) | `time` 进程时间 | ✅ | 高精度浮点;用于计时 |
| `os.date([fmt [, t]])` | 格式化时间(strftime 子集,§9.2) | `time.Format` + strftime 翻译 | ✅ | fmt 默认 `"%c"`;`*t`/`!*t` 前缀返回 table(§9.2) |
| `os.difftime(t2, t1)` | 时间差(秒) | `t2 - t1` | ✅ | 简单减法 |
| `os.getenv(name)` | 环境变量 | `os.Getenv` | ✅ | 不存在返回 nil;**嵌入场景可禁用**(§9.3) |
| `os.exit([code [, close]])` | 退出进程 | `os.Exit` | △ | **嵌入危险**(§9.3):宿主可禁用(退出宿主进程!) |
| `os.remove(filename)` | 删文件 | `os.Remove` | △ | **嵌入危险**(§9.3):文件系统副作用,可禁用 |
| `os.rename(old, new)` | 重命名 | `os.Rename` | △ | 同上 |
| `os.tmpname()` | 临时文件名 | `os.CreateTemp` 等 | △ | 跨平台差异;可禁用 |
| `os.execute([cmd])` | 执行 shell 命令 | `os/exec` | **❌/△ 危险** | **嵌入场景默认禁用**(§9.3):任意命令执行,安全风险最高 |
| `os.setlocale([loc [, cat]])` | 设 locale | (有限/缺) | **△/❌ 部分** | **纯 Go 无 C locale**(§9.4);P1 仅支持 `"C"`/缺 |
| `os.getenv`/`os.time` 等只读项 | — | — | ✅ | 无副作用,默认启用 |

### 9.2 `os.date` 格式串(strftime 子集)

`os.date(fmt, t)` 把时间戳 `t`(默认当前)按 `fmt` 格式化:

- **`fmt` 前缀**:① 无前缀 → 返回格式化字符串;② `*t` 前缀(`os.date("*t")`)→ 返回 **table**(字段 `year`/`month`/
  `day`/`hour`/`min`/`sec`/`wday`/`yday`/`isdst`);③ `!` 前缀(`!%c` / `!*t`)→ 用 **UTC**(否则本地时间)。
- **strftime 指令子集**(对齐 5.1 用的 C `strftime`,P1 翻译成 Go `time.Format` 或手工实现):

| 指令 | 含义 | 指令 | 含义 |
|---|---|---|---|
| `%Y` | 4 位年 | `%m` | 月(01-12) |
| `%d` | 日(01-31) | `%H` | 时(00-23) |
| `%M` | 分 | `%S` | 秒 |
| `%p` | AM/PM | `%A`/`%a` | 星期全/简名 |
| `%B`/`%b` | 月全/简名 | `%c` | 完整日期时间(默认) |
| `%x`/`%X` | 日期/时间 | `%j` | 年内天数 |
| `%w` | 星期(0-6) | `%%` | 字面 % |

- **实现策略**:Go `time.Format` 用的是 reference-time 布局(`2006-01-02`),**不是 strftime**。P1 需**把 strftime
  指令翻译成 Go 布局**(或手工按指令拼)。**差分敏感**:`%c`/`%x`/`%X` 的具体格式、月/星期名(英文 locale)须与
  5.1 C `strftime` 一致——**待 12 核对**。`%A`/`%B` 等 locale 相关项在纯 Go 下用**固定英文名**(§9.4 locale 缺)。
- **跨平台一致性**:纯 Go `time` 跨平台一致(不像 C `strftime` 依赖系统 locale),这**反而比 C Lua 更可控**——但
  也意味着与某个特定平台的 C Lua `os.date` 输出可能有微差(locale/时区),指向 12 定豁免口径。

### 9.3 安全:os.execute/exit 等的嵌入限制(任务点名,记口径)

**关键嵌入安全口径**(任务点名:os.execute/exit 在嵌入场景的限制,宿主可禁用):

os 库的部分函数有**进程级副作用或安全风险**,在嵌入式宿主(尤其跑**不可信脚本**)场景必须可控:

| 风险级 | 函数 | 风险 | P1 默认 |
|---|---|---|---|
| **最高** | `os.execute` | 任意 shell 命令执行 | **默认禁用**(或不注册);宿主显式开启才有 |
| **高** | `os.exit` | **退出宿主进程**(不只脚本!) | **默认禁用/改为抛错**;嵌入场景退出整个宿主是灾难 |
| **高** | `os.remove`/`os.rename`/`os.tmpname` | 文件系统副作用 | 默认禁用或受沙箱限制 |
| **中** | `os.getenv` | 信息泄露(环境变量) | 可禁用或白名单 |
| **低** | `os.time`/`os.clock`/`os.date`/`os.difftime` | 只读,无副作用 | 默认启用 |

> **定稿口径(经评审)**:**默认完整 os 库,对齐 gopher-lua 提供面**(drop-in 宣称要求默认面与 gopher 一致,
> gopher-lua 的 os 库含 execute/exit)。**下游禁用走 §12.1 的三层机制**:`Libs` 位掩码整库关(`^LibOS`)、
> `LibsSafe` 预设(无 os/io/package 的计算沙箱)、`Exclude` 函数级拔点(`{"os.execute","os.exit"}`)——
> 跑不可信脚本的宿主一行配置即可收紧,无需逐函数自己写包装。
>
> - **`os.exit` 的特别处理**:即便注册,嵌入场景的 `os.exit` 应**改为「抛一个特殊 Lua 错误 / 返回控制权给宿主」**
>   而非真 `os.Exit`(真退出会杀掉宿主整个进程,11 §8 的多 goroutine 宿主里更是灾难——一个脚本 `os.exit` 杀全进程)。
>   **定稿:P1 的 `os.exit` 默认不真退出**(可配);具体语义(抛错 vs 标记请求退出)记 §15.2 缺口。
> - **嵌入安全的责任分界**:望舒是**嵌入式** VM,默认面对齐 gopher(易迁移),**收紧的能力**(§12.1 三层开关)
>   是 VM 的责任,**收紧的决策**(信不信任脚本)是宿主的责任。

### 9.4 `os.setlocale`:纯 Go 无 C locale(部分/缺)

- **`os.setlocale(loc, cat)`** 在 C Lua 调 C `setlocale`,影响 `string.upper`/`%a` 字符类/`os.date` 月名等的
  **locale 相关行为**。**纯 Go 没有 C locale 机制**(Go 不暴露 `setlocale`)。
- **P1 定稿:`os.setlocale` 仅支持 `"C"` locale(返回 `"C"`),其它 locale 返回 nil(设置失败)**。这意味着:
  - `string.upper`/`lower` 仅 ASCII(§5.1);`%a`/`%d` 等字符类仅 ASCII(§6.1.1);`os.date` 月/星期名用固定英文。
  - **与 C Lua 的差异**:C Lua 在非 C locale 下 `string.upper` 可能处理重音字母、`os.date` 出本地化月名。望舒
    **不支持**(纯 Go + 锁 ASCII)。**这是已知 P1 限制**(记 §15.2 缺口),嵌入式场景 locale 依赖罕见。
- **为什么不做**:Go 标准库无 locale-aware 的字符分类/大小写(`unicode` 包是 Unicode 而非 C locale 语义);
  复刻 C locale 是大工程且差分基准(5.1)本身 locale 行为平台相关。**锁 C locale(ASCII)是纯 Go 的务实选择**,
  也让望舒跨平台行为**一致**(C Lua 反而因 locale 跨平台不一致)。

---

## 10. io 库(P1 范围决策:文件句柄 = full userdata + `__gc`)

io 库涉及**文件句柄**(full userdata + `__gc`,01 §5.5 / 06 §10),是 stdlib 里**有真实设计含量**的部分。本节
给设计 + **明确 P1 范围**(任务点名:P1 可简化,至少 io.write/io.read/print 够跑测试)。

### 10.1 io 库清单与 P1 范围

| 函数 | 语义 | P1 | 备注 |
|---|---|---|---|
| `io.write(...)` | 写各参数到默认输出(stdout) | ✅ **必做** | 跑测试最小集;各参数 string/number(`CheckString`) |
| `io.read([fmt...])` | 从默认输入(stdin)读 | ✅ **必做** | 最小集;格式 `"*l"`(行)/`"*n"`(数)/`"*a"`(全)/数字(n 字节) |
| `io.stdout`/`io.stdin`/`io.stderr` | 标准流(file handle) | ✅ **必做** | 预建的 file userdata(§10.2) |
| `io.open(filename [, mode])` | 打开文件,返回 file handle | △ **部分** | full userdata + `__gc`(§10.2);依赖文件系统 + 安全(§9.3) |
| `io.close([file])` | 关闭文件(默认默认输出) | △ | file 方法 `file:close` 的全局形式 |
| `io.lines([filename])` | 行迭代器 | △ | 迭代器(host closure);依赖 open |
| `io.input([file])`/`io.output([file])` | 取/设默认输入/输出 | △ | 默认流管理 |
| `io.type(obj)` | 判断是否 file handle(`"file"`/`"closed file"`/nil) | △ | 检查 userdata 的 metatable 身份 |
| `file:read(...)` | 从 file 读(同 io.read 格式) | △ | file 方法(§10.3) |
| `file:write(...)` | 写 file | △ | file 方法 |
| `file:close()` | 关闭 file | △ | file 方法;调底层 `os.File.Close` |
| `file:lines()` | 行迭代器 | △ | file 方法 |
| `file:seek([whence [, off]])` | 文件定位 | △ | `set`/`cur`/`end` + 偏移 |
| `file:flush()` | 刷新缓冲 | △ | |
| `io.popen`/`io.tmpfile` | 管道/临时文件 | **❌ 缺口** | popen 涉进程(安全,§9.3);P1 不做 |

**P1 范围定稿**(任务点名):

> **P1 io 库分两档**:
> - **必做(跑 conformance 测试所需)**:`io.write`、`io.read`、`io.stdout`/`io.stdin`/`io.stderr`、`print`(base,
>   经 stdout)。这是「脚本能输出、能读输入、测试能跑」的最小集。**不依赖完整 file handle 机制**(stdout/stdin
>   是预建的固定 file,§10.2)。
> - **部分实现(完整文件 IO)**:`io.open`/`file:read`/`file:write`/`file:close`/`file:seek`/`io.lines` 等完整
>   文件句柄操作——**P1 可部分实现或记缺口**。file handle 的 full userdata + `__gc` 机制(§10.2)是设计要点,
>   P1 至少把**机制骨架**搭好(让 stdout/stderr 走同一机制),完整文件操作按需补。
> - **缺口**:`io.popen`(进程管道,安全风险 §9.3)、`io.tmpfile`——**P1 不做**(记 §15.2 缺口)。
>
> **为什么这样裁**:① 测试套主要需要 `print`/`io.write` 输出(差分测试输出);② 嵌入式规则引擎(首个宿主)脚本极少
> 做文件 IO(数据经 arena 喂入,11 §3,不从文件读);③ 完整文件 IO 的 `__gc` 终结器(关文件)依赖 06 §10 的
> finalizer,P1 finalizer 是骨架(06 §10 P1 范围)。roadmap §5 原则 4:文件 IO 不是热路径核心,按需。

### 10.2 file handle 设计:full userdata + metatable(`__index` = 方法表,`__gc` = 关文件)

**文件句柄是 full userdata**(01 §5.5),带 metatable(任务点名设计):

```
file handle 设计:
  - file = full userdata(01 §5.5):
      payload = 句柄(经句柄表索引,11 §6,指向 Go 侧的 *os.File / *bufio.Reader/Writer)
                ——payload 不直存 Go 指针(01 §3.5 / §5.5),经句柄表(11 §6.2)
      metaRef → file metatable(VM 内建,所有 file 共享一张)
  - file metatable:
      __index = file 方法表({read=..., write=..., close=..., lines=..., seek=..., flush=...})
                → file:read() 经 __index 命中方法(07 §3,与 string 库同构)
      __gc     = 关文件终结器(06 §10):file 不可达时关底层 os.File(防泄漏 fd)
      __tostring = "file (0x...)" / "file (closed)"(07 §11)
      __metatable = 保护(可选,防脚本篡改 file 元表,07 §1.3)
```

要点(设计含量所在):

- **file 方法经 `__index` 表暴露**(与 string 库 §5.0 同构):`file:read(...)` codegen 成 `SELF`(02 §4-11)→
  `file["read"]` → file userdata 的 metatable `__index` = 方法表 → 命中 `read` host fn → 以 `(file, ...)` 调用。
  **复用 07 §3 的 `__index` 机制**,无新机制。
- **`__gc` 关文件(防 fd 泄漏)**:file 不可达且未显式 close 时,GC 的 finalizer(06 §10)调 `__gc(file)`,后者
  关底层 `os.File`。**这是 io 库依赖 06 §10 finalizer 的关键点**——也是 P1 finalizer 骨架(06 §10 P1 范围)要
  支持的真实用例。`__gc` 是 host function(关文件的 Go 逻辑),经 06 §10 的保护调用(终结器出错不崩 VM)。
- **payload 经句柄表**(11 §6.2):file userdata 的 payload **不直存 `*os.File`**(Go 指针,01 §3.5 禁止)。存
  **句柄表索引**(11 §6.2),`handles[idx]` = `*os.File`(或带 buffer 的包装)。host 方法(read/write)经
  `FromHandle`(11 §6.2)取回 `*os.File` 操作。
- **closed 状态**:file close 后,句柄表项置 nil 或标记 closed;后续 `file:read` 报 `"attempt to use a closed file"`
  (5.1 措辞,待 12 核对)。`io.type` 据此返回 `"file"`/`"closed file"`/nil。

### 10.3 `io.write`/`io.read`(P1 必做,最小集)

```go
// io.write(...):写各参数到默认输出(P1:stdout)。各参数 string/number。
func hostIoWrite(vm *VM, th *Thread) int {
    out := vm.defaultOutput()                      // 默认输出 file(P1:io.stdout = os.Stdout 包装)
    n := th.NArgs()
    for i := 1; i <= n; i++ {
        b := th.CheckString(i)                     // string 或 number(§2.5;number→%.14g)
        if err := out.Write(b); err != nil {
            return vm.Errorf(th, "io error: %v", err)  // IO 错误转 Lua 错误
        }
    }
    th.PushValue(vm.defaultOutputHandle())          // 5.1:io.write 返回默认输出 file(便于链式)
    return 1
}
```

- **`io.write` 最小集**:写 stdout(P1 的默认输出)。各参数 `CheckString`(string 或 number)。**返回默认输出
  file**(5.1:`io.write(...)` 返回 file 以便 `io.write("a"):write("b")` 链式)。
- **`io.read` 最小集**:从 stdin 读。格式:`"*l"`(一行,默认,去换行)、`"*L"`(一行带换行,5.2;5.1 无 `*L`——
  **排除**)、`"*n"`(一个数,用 `parseLuaNumber`)、`"*a"`(全部)、数字 n(n 字节)。P1 至少 `"*l"`/`"*a"`/`"*n"`。
- **stdout/stdin/stderr 是预建 file**(§10.2 机制):openlibs 时建三个 file userdata 包装 `os.Stdout`/`os.Stdin`/
  `os.Stderr`,存 `io.stdout`/`io.stdin`/`io.stderr`。`print`(base §4.2)也写 `io.stdout`(经 `vm.writeStdout`)。
- **IO 错误转 Lua 错误**:写/读失败时,Lua 5.1 的 io 函数返回 `(nil, errmsg)` 或抛错(依函数)。`io.write` 失败
  抛错;`file:read` EOF 返回 nil(非错)。**P1 对齐 5.1 的「失败返回 nil+msg vs 抛错」分工**,待 12 核对。

### 10.4 io 库与安全(承 §9.3)

io 库的文件操作(`io.open`/`io.lines`/`io.popen`)与 os 库的文件操作同属**嵌入安全敏感**(§9.3):

- **`io.open` 受文件系统访问控制**:嵌入式宿主可禁止脚本打开任意文件(沙箱)。禁用走 §12.1 三层机制:
  `Exclude: {"io.open","io.lines"}`(留 stdout/stdin/stderr 的最小 io)或整库 `^LibIO`。
- **`io.popen` 默认不做**(进程管道 = 命令执行,安全风险等同 `os.execute`,§9.3)。
- **默认输出可重定向**:宿主可把 `io.stdout` 的底层从 `os.Stdout` 换成宿主提供的 writer(捕获脚本输出而非直接
  写终端)——这对「宿主收集脚本 print 输出」有用,P1 经 `Options` / `State` 配置默认输出 writer(记 §15.2 缺口)。

---

## 11. coroutine 库(机制在 08,本文列清单 + 指针)

coroutine 库的**机制全在 [08-coroutines](./08-coroutines.md)** 定稿(Thread 状态机、resume/yield 在 reentry 模型
上的实现、跨 Thread 值栈搬运、yield 不能跨 host call)。本文**列清单 + 一行语义,指向 08**(不重复机制):

| 函数 | 语义 | 参数 → 返回 | P1 | 08 指针 |
|---|---|---|---|---|
| `coroutine.create(f)` | 创建协程(新 Thread,suspended) | `(f)` → thread | ✅ | **08 §4.1**(create:协程主函数与首帧) |
| `coroutine.resume(co, ...)` | 恢复协程,传参;返回 `(true, yields...)` 或 `(false, err)` | `(co, ...)` → (bool, ...) | ✅ | **08 §4.2/§4.3**(参数→返回值搬运);**协程内错误 → (false,err)** 见 08 §4.5 / 09 §12 |
| `coroutine.yield(...)` | 挂起当前协程,yield 值给 resume | `(...)` → (resume 的参数) | ✅ | **08 §3.4**(yield 从 host 触发 execute yield 返回);**不能跨 host call** 08 §5 |
| `coroutine.status(co)` | 协程状态字符串 | `(co)` → string | ✅ | **08 §2.1**(`"suspended"`/`"running"`/`"normal"`/`"dead"`) |
| `coroutine.wrap(f)` | 创建协程,返回「调用即 resume」的函数 | `(f)` → function | ✅ | **08**(wrap:错误**重抛**不捕获,异于 resume,见 09 §12.3) |
| `coroutine.running()` | 返回当前运行的协程(主线程返回 nil) | `()` → thread or nil | ✅ | **08 §8.3**(主线程 running 语义) |

- **`resume` 是 protected 边界**(09 §12):协程内未捕获错误 → `resume` 返回 `(false, err)` + 协程变 dead。
  机制在 09 §12 / 08 §4.5,本文指针。
- **`wrap` 的错误处理异于 resume**(09 §12.3):wrap 的函数**重抛**协程内错误(不捕获),让调用者的 pcall 捕获。
- **`yield` 不能跨 host call 边界**(08 §5,Lua 5.1 硬限制):协程不能在 stdlib host function 内部(如 `table.sort`
  的比较器、`string.gsub` 的替换函数)yield——会报 `"attempt to yield across metamethod/C-call boundary"`(5.1)。
  这影响 stdlib 实现:**stdlib host 内调 `callLuaFromHost`(§1.4)时,被调 Lua 函数若 yield,会撞这个限制**。
  机制 08 §5,本文标注:**stdlib 的回调点(sort comparator / gsub repl / pcall f)若被 yield 穿越,按 08 §5 报错**。
- **coroutine 库函数本身是 host functions**(与其它 stdlib 同机制),但 resume/yield 的**内部实现不是普通
  callLuaFromHost**——它切换 Thread 的 CallInfo 链(08 §3),机制特殊,本文指向 08。

> **前向引用说明**:本文起草时 08 可能尚未定稿;若 08 章节号变动,以 08 实际定稿为准。本文只承诺「coroutine 库
> 6 个函数的清单 + 语义」,机制细节(状态机、搬运、yield 冒泡、跨边界限制)**完全委托 08**。

---

## 12. 库加载 / openlibs(NewState 注册哪些库)

### 12.1 `luaL_openlibs` 等价物:openlibs

Lua 5.1 用 `luaL_openlibs(L)` 一次性打开所有标准库。望舒等价物在 `NewState`(11 §1.2 `Options.OpenLibs`)时
调用:

```go
// internal/stdlib —— 打开标准库(luaL_openlibs 等价)。NewState 按 Options.Libs 配置调用。
//
// 设计要求(经评审定稿):**下游必须能方便地禁用任意标准库**——逐库位开关 + 预设 + 函数级排除,
// 三层粒度,缺省对齐 gopher-lua 提供面(drop-in 宣称)。
func OpenLibs(vm *VM, cfg LibConfig) {
    if cfg.Libs&LibBase != 0      { openBase(vm) }      // base(§4);禁用后连 print/pairs 都没有——极端沙箱用
    if cfg.Libs&LibString != 0    { openString(vm) }    // string 库 + string 元表 __index(§5.0)
    if cfg.Libs&LibTable != 0     { openTable(vm) }     // table 库(§7)
    if cfg.Libs&LibMath != 0      { openMath(vm) }      // math 库(§8)
    if cfg.Libs&LibOS != 0        { openOS(vm) }        // os 库(§9,完整面)
    if cfg.Libs&LibIO != 0        { openIO(vm) }        // io 库(§10,完整面)
    if cfg.Libs&LibCoroutine != 0 { openCoroutine(vm) } // coroutine 库(§11,机制 08)
    if cfg.Libs&LibPackage != 0   { openPackage(vm) }   // package/require(§4.7,对齐 gopher 面)
    if cfg.Libs&LibDebug != 0     { openDebug(vm) }     // debug:traceback/getinfo(09 §13)
    // 函数级排除:在库注册完成后,按路径从全局/库表中删除(置 nil)。
    for _, path := range cfg.Exclude { removeByPath(vm, path) } // 如 "os.execute"、"io.open"、"loadstring"
}

// LibConfig:库加载配置(经 wangshu.Options 暴露,11 §1.2)。三层粒度:
//   1. 预设(常用一行搞定);2. Libs 位掩码(逐库);3. Exclude 路径表(逐函数)。
type Lib uint16
const (
    LibBase Lib = 1 << iota
    LibString
    LibTable
    LibMath
    LibOS
    LibIO
    LibCoroutine
    LibPackage
    LibDebug
)
const (
    // LibsDefault:缺省全开(不含 debug),对齐 gopher-lua 的 OpenLibs 提供面——drop-in 宣称的兑现。
    LibsDefault = LibBase | LibString | LibTable | LibMath | LibOS | LibIO | LibCoroutine | LibPackage
    // LibsSafe:计算型沙箱预设——无 os/io/package(脚本只能算,不能碰进程/文件系统/加载),
    //   适合跑不可信脚本的宿主。
    LibsSafe = LibBase | LibString | LibTable | LibMath | LibCoroutine
)

type LibConfig struct {
    Libs    Lib      // 库位掩码;零值按 LibsDefault 处理
    Exclude []string // 函数级排除路径(注册后删除),如 {"os.execute", "os.exit", "io.popen", "loadstring"}
}
```

**三层粒度的使用形式**(下游视角):

```go
// ① 缺省:gopher-lua 对齐面,什么都不配
wangshu.NewState(wangshu.Options{})
// ② 预设:沙箱一行
wangshu.NewState(wangshu.Options{Libs: wangshu.LibsSafe})
// ③ 细调:全开但拔掉两个危险函数
wangshu.NewState(wangshu.Options{Exclude: []string{"os.execute", "os.exit"}})
```

> **函数级排除的实现**:`removeByPath` 按 `"lib.fn"`/`"fn"` 路径在 globals 上 rawset nil(库表项删除)。
> 因为 stdlib 全部经 host 注册表 + globals 表暴露(§1),删除表项即彻底不可达——脚本无法绕过
> (没有 C API 后门)。排除发生在 openlibs 末尾、宿主代码运行前,无竞态。

### 12.2 全局表布点

各库在 globals 表的布点(对齐 5.1):

| 库 | globals 布点 | 形式 |
|---|---|---|
| base | **直接全局**(print/type/pairs/tostring/...) | 函数直接挂 globals;`_G`/`_VERSION` 是全局变量 |
| string | `string` 表 + **string 元表 `__index`**(§5.0) | `string.upper` + `s:upper()` 双入口 |
| table | `table` 表 | `table.insert` 等 |
| math | `math` 表 | `math.floor` 等;`math.pi`/`math.huge` 是表内常量 |
| os | `os` 表 | `os.time` 等(危险函数受配置) |
| io | `io` 表 + `io.stdout`/`stdin`/`stderr`(file userdata) | `io.write` + file 方法表(§10.2) |
| coroutine | `coroutine` 表 | `coroutine.create` 等 |
| debug | `debug` 表 | `debug.traceback`/`getinfo`(09 §13) |

- **base 恒开**(核心,无 base 无法跑任何脚本:`print`/`type`/`pairs` 是地基)。
- **string 库的特殊布点**:不仅建 `string` 表,还**设 string 类型元表**(§5.0,07 §1.2)——这是 string 库与其它
  库的关键差异(其它库只是全局表,string 库还改类型元表)。
- **库间依赖**:`loadstring`/`dofile`(base §4.7)依赖 `Compile`;`io.lines`/`loadfile` 依赖 io 文件读;`print`
  依赖 io 的 stdout。openlibs 顺序应保证依赖先于被依赖(base 的 loadstring 在 Compile 就绪后才有意义,实际
  Compile 是 VM 固有能力,无顺序问题;io 的 stdout 在 openIO 时建,print 运行期才用,无 init 顺序依赖)。

### 12.3 openlibs 与差分(库存在性)

- **差分基准是 Lua 5.1 的 `luaL_openlibs`**:5.1 默认打开 base/string/table/math/io/os/debug/package/coroutine。
  望舒 openlibs **打开同一组**(package 系统 P1 缺口,§4.7)——**库与函数的存在性必须与 5.1 一致**:5.1 有的
  全局/库函数望舒要有(否则脚本 `string.format` 报 nil),5.1 没有的(`table.unpack`/`rawlen`/5.2+ 项)望舒**不能
  有**(否则差分时它们在 5.1 是 nil)。
- **存在性差分用例**:遍历 5.1 的全部标准库函数名,验证望舒**同名存在 + 5.2+ 函数不存在**。这是 stdlib 差分的
  基础项(指向 12)。**P1 缺口的库**(package/部分 io/部分 debug)在存在性差分上**标注豁免**(已知 P1 未实现)。

---

## 13. stdlib 与差分测试(重灾区,指向 12)

**stdlib 行为必须与 Lua 5.1 逐字节一致**(roadmap §5 原则 2),且**这是差分测试的重灾区**(库行为细节多)。
本节汇总 stdlib 的差分敏感项,统一指向 [12-testing-difftest](./12-testing-difftest.md) 定口径与核对。

### 13.1 差分敏感项总表

| 项 | 函数 | 敏感点 | 口径 | 指针 |
|---|---|---|---|---|
| **数字格式** | `string.format` `%f/%g/%e`、`tostring`(number)、`table.concat`、CONCAT | 浮点格式逐字节(指数位数、尾零、负零、inf/nan) | **严格逐字节**(复刻 C printf,§5.2.1) | 12 |
| **pattern 匹配** | `find`/`match`/`gmatch`/`gsub` | 匹配结果、捕获、位置、贪婪回溯顺序、空匹配、`%f`/`%b`/反向引用 | **严格逐字节**(移植 5.1 lstrlib,§6.6) | 12 |
| **`%q` 转义** | `string.format` `%q` | 每个特殊字符的转义形式 | **严格**(以 5.1 addquoted 为准,§5.2.2) | 12 |
| **tostring 地址** | `tostring(table/function/thread/userdata)` | 含对象地址 `0x...` | **豁免**(arena 偏移 ≠ C 指针,脱敏比较,07 §11) | 12 |
| **pairs 序** | `pairs`/`next` | 遍历顺序(哈希/rehash/Brent 决定) | **口径待定**(严格 vs 排序后比;哈希环已对齐 06 §9.3) | 12 |
| **sort 稳定性** | `table.sort` | 相等元素最终排列(快排 pivot/partition) | **严格**(移植 5.1 auxsort,§7.6) | 12 |
| **math.random** | `math.random` | 具体随机序列 | **豁免**(Go rand ≠ C rand,只验范围,§8.4) | 12 |
| **fmod vs %** | `math.fmod`、`%` | 负数结果不同 | **严格各自一致**(§8.3) | 12 |
| **错误措辞** | 所有 `argError`/`Errorf` | `bad argument #n to 'fn' (...)` 标点/冠词/got no value | **严格**(对齐 5.1,§2.4;09 §9.3) | 12 |
| **os.date 格式** | `os.date` | `%c/%x/%X`、月/星期名、时区 | **部分豁免**(纯 Go vs C locale,§9.2/§9.4) | 12 |
| **collectgarbage count** | `collectgarbage("count")`、`gcinfo` | KB 数(arena ≠ C 堆) | **豁免**(数值脱敏,§4.6) | 12 |
| **库存在性** | 全部 | 5.1 有的存在、5.2+ 的不存在 | **严格**(§12.3) | 12 |
| **数字 coercion 边界** | `tonumber`、`CheckNumber` | `parseLuaNumber` 接受的串(十六进制整数、空白、不接受 0x1p4/inf/nan) | **严格**(与 07 §5.2 共用,12 钉死) | 12 |

### 13.2 差分敏感的根因分类

stdlib 差分项可归为三类(决定口径):

1. **可严格逐字节(必须对齐)**:format 整数/pattern/sort/错误措辞/库存在性/coercion——**算法可复刻**(移植
   5.1 源码逻辑),**无外部不可控因素**,差分套件**严格逐字节核对**。这是 stdlib 差分的主体。**实现纪律:移植
   5.1 对应 C 函数的精确逻辑,不重新设计**(§5.2/§6.6/§7.6 反复强调)。
2. **可严格但需复刻 C 行为(format 浮点)**:`%f/%g/%e` 依赖 C printf 的精确浮点格式——**Go fmt 与 C printf 有
   微差**,需**复刻 C printf 语义**(§5.2.1)才能严格一致。这是「严格但实现成本高」的项。
3. **根本豁免(不可控外部因素)**:① tostring 地址(arena 偏移 ≠ C 指针)② math.random(算法不同)③
   collectgarbage count(内存模型不同)④ os.date locale(纯 Go ≠ C locale)——**这些不可能逐字节一致**(底层
   机制本质不同),差分套件**脱敏/排除/范围比较**。

### 13.3 GC 压力 fuzz 捕获 shadow stack 漏 Pin(承 06 §11)

stdlib 的 shadow stack 纪律(§3)漏 Pin 是**最难调的 bug 类**(06 §6.3:GC 时机依赖分配量,非确定)。捕获手段
是 **GC 压力 fuzz**(06 §11):

- **机制**:把 GCPAUSE 设到极小(06 §8.3,每次/每几次分配就 full GC),反复跑大量 stdlib 调用(尤其分配类:
  format/concat/rep/gsub/match,§3.1),验证:① 输出与正常 pacing byte-equal;② 不崩溃。
- **为什么有效**:漏 Pin 的中间对象在正常 pacing 下偶发被回收(GC 恰好在持有窗口触发的概率低),高频 GC 下
  **必现**(每次分配都 GC,持有窗口必然撞上)。这是 §3 纪律的**主要自动化防线**(06 §11)。
- **指向 12**:GC 压力 fuzz 的具体 harness(强制高频 GC + stdlib 调用矩阵)在 12 定稿;本文 §3 的纪律范例
  (concat/format/gsub)是它的测试对象。

---

## 14. P1 stdlib 范围裁剪总表

下表是 stdlib 全函数的 **P1 范围**(必做 / 简化 / 缺口),明确 P1 必做(跑 conformance 所需)vs 可延后:

| 库 | ✅ 必做(conformance 所需) | △ 简化/部分 | ❌ 缺口(P1 不做) |
|---|---|---|---|
| **base** | print, type, tostring, tonumber, pairs, ipairs, next, select, rawget, rawset, rawequal, setmetatable, getmetatable, assert, error, pcall, xpcall, unpack, `_G`, `_VERSION`, collectgarbage(collect/count/stop/restart/setpause) | gcinfo, loadstring, load, loadfile, dofile, collectgarbage(step/setstepmul 近似) | **rawlen**(5.2), **require/module/package 系统**, **table.unpack/pack/move**(5.2+) |
| **string** | len, sub, upper, lower, rep, reverse, byte, char, format, find, match, gmatch, gsub(**全 pattern 全集** §6) | — | string.dump(字节码序列化) |
| **table** | insert, remove, concat, sort, maxn | getn, setn(=`#t`/空操作) | table.unpack/pack/move(5.2+) |
| **math** | abs, ceil, floor, sqrt, sin, cos, tan, asin, acos, atan, atan2, exp, log, log10, pow, fmod, modf, max, min, random, randomseed, huge, pi, rad, deg, frexp, ldexp, sinh, cosh, tanh | — | math.tointeger/type/maxinteger/mininteger(5.3 整数) |
| **os** | time, clock, date, difftime, getenv | exit(默认不真退出), remove, rename, tmpname, setlocale(仅 "C") | os.execute(默认禁用,安全) |
| **io** | **write, read, stdout, stdin, stderr**(最小集,跑测试) | open, close, lines, input, output, type, file:read/write/close/lines/seek/flush | io.popen, io.tmpfile |
| **coroutine** | create, resume, yield, status, wrap, running(**机制全在 08**) | — | — |
| **debug**(09 §13) | traceback, setmetatable, getmetatable | getinfo(部分字段) | sethook, getlocal/setlocal, getupvalue/setupvalue, getregistry |

**P1 必做的判定标准**:① 跑 Lua 5.1 conformance 测试套必需(base 核心 + string/table/math 全集 + io 最小输出);
② 首个宿主(规则引擎)脚本所需(标量/表/字符串/数学操作)。**可延后的判定标准**:① 嵌入式场景罕用(文件 IO
完整、module 系统、debug 高级);② 安全敏感默认关(os.execute);③ 5.2+ 特性(必须不做以保差分)。

**P1 stdlib 的核心取舍**(总结):

1. **string 库的 pattern 全集不可裁**(§6.6):pattern 是 string 库核心,缺任何特性都让大量脚本与 5.1 分叉。
   这是 P1 必做里**实现成本最高**的(递归回溯 matcher + 全部特殊构造),但**不可延后**。
2. **io 库砍到最小集**(§10.1):只 write/read/标准流;完整文件句柄(full userdata + `__gc`)搭骨架,完整文件
   操作延后。嵌入式场景文件 IO 弱,数据走 arena(11)。
3. **module/package 系统缺口**(§4.7):嵌入式宿主不从文件系统 require,脚本由宿主 Compile 提供。这是 P1 与
   「独立 Lua 解释器」的最大功能差距,但符合嵌入式定位(roadmap §8 首个宿主)。
4. **os 危险函数默认禁用**(§9.3):execute/exit/文件操作受安全配置控,默认安全子集。嵌入式 VM 不该让脚本杀
   宿主进程或跑 shell。
5. **5.2+ 特性主动不做**(全表):rawlen/table.unpack/整数 math/位运算等显式不实现,保差分与官方 5.1 一致。

---

## 15. 不变式清单 + 文档缺口 / 待决

### 15.1 不变式清单(实现与差分须守)

1. **host 签名统一**(§1.1):所有 stdlib 与宿主 host function 用 `HostFn = func(vm, th) (nret int)`(05 §7.6),
   1-based 参数索引(§1.2),push 返回值后 `return nret`(§1.3)。stdlib 与宿主 `Register` 一视同仁(11 §10)。
2. **host 出错走 raise,不 Go panic**(§1.5 / §1.4):stdlib 出错用 `vm.raise`/`argError`/`Errorf`(09 §3.3 机制);
   回调 Lua(comparator/repl/f)的错误必须上抛不吞;host 真 panic 只被顶层兜底(09 §11)。
3. **luaL_* helper 收口参数检查与错误**(§2):`Check*`/`Opt*`/`argError` 统一类型检查与 `bad argument` 措辞
   (§2.4),保差分一致。`CheckNumber`/`tonumber` 共用 `parseLuaNumber`(07 §5.2)。
4. **shadow stack 纪律**(§3):分配类 stdlib(format/concat/rep/gsub/match,§3.1)优先 Go 缓冲累积 + 末尾一次
   intern(§3.2/§3.3);不可避免地在 arena 持有中间引用跨分配/跨重入时 Pin/defer Unpin(§3.4 gsub);能尽早送
   Lua 栈/表的就送(免 Pin)。GC 压力 fuzz 是漏 Pin 的主防线(§13.3)。
5. **string 库经 string 元表 `__index` 暴露**(§5.0):`s:upper()` 经 `typeMetatables[STRING].__index = string` 库表
   (07 §3.4),与 `string.upper(s)` 同来源。
6. **pattern 是 Lua patterns 非正则**(§6):全 5.1 特性(字符类/量词含 `-` 最小/锚/捕获/位置捕获/`%b`/`%f`/
   反向引用),递归回溯移植 5.1 lstrlib(§6.2),无指数回溯灾难(量词只作用单字符类)。
7. **table.sort 比较器重入纪律**(§7.4):比较时两元素保持表槽内(R5 可达),partition 移动在比较之后,默认免
   Pin;比较器经 callLuaFromHost(Go 栈 O(1) 不累积);出错冒泡,表留部分排序态。
8. **数字格式 `%.14g` / format 浮点复刻 C printf**(§5.2.1):tostring/concat/CONCAT 的 number 用 `%.14g`(05 §4.6);
   format 的 `%f/%g/%e` 复刻 C printf 逐字节(不直接用 Go fmt)。
9. **math.fmod ≠ Lua `%`**(§8.3):fmod 截断(`math.Mod`),`%` floor 取模(`luaMod`,05 §4.1),负数结果不同,
   不可混用。floor/ceil 返回 double(§8.2)。
10. **5.2+ 特性主动不实现**(§4/§7/§8/§14):rawlen、table.unpack/pack/move、整数 math、位运算、`__pairs` 等
    显式不做;全局 `unpack` 在(5.1),`table.unpack` 不在;`_VERSION == "Lua 5.1"`。保差分与官方 5.1 一致。
11. **os 危险函数默认安全子集**(§9.3):execute/exit/文件操作受配置控,默认仅只读 time/clock/date/difftime;
    `os.exit` 默认不真退出宿主进程。
12. **io P1 最小集 + file = full userdata + `__gc`**(§10):必做 write/read/标准流;file handle 是 full userdata
    带 metatable(`__index`=方法表,`__gc`=关文件,经句柄表存 `*os.File`)。
13. **coroutine 机制全委托 08**(§11):本文只列 6 函数清单 + 语义;状态机/搬运/yield 冒泡/跨边界限制在 08;
    stdlib 回调点被 yield 穿越按 08 §5 报错。
14. **差分敏感项分三类**(§13.2):可严格逐字节(format 整数/pattern/sort/措辞/coercion/库存在性)、可严格但需
    复刻 C(format 浮点)、根本豁免(tostring 地址/random/count/os.date locale)。

### 15.2 文档缺口 / 待决(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **module/package 系统范围**(§4.7):`require`/`module`/完整 `package`(path/loaders/loaded/preload)P1 是极简
  (preload+loaded,无文件搜索)还是缺口,待首个宿主需求确认。无 cgo → 无 C 加载器(`package.cpath` 不适用)。
  conformance 若含 require 用例,标 P1 已知限制。
- **`collectgarbage` step/setstepmul 简化**(§4.6):P1 只 full GC(06 §8.3),`"step"` 近似为触发 full GC,
  `setstepmul` 存而不用。脚本依赖增量步进精确语义会微差,记口径。增量 GC 是 P3+。
- **`%q` / `%f`/`%g`/`%e` / pattern 的精确差分**(§5.2/§6.6):`%q` 转义集、format 浮点的 C printf 复刻细节
  (指数位数/尾零/inf-nan)、pattern 各构造的边角语义(空匹配推进、`%f` 边界、贪婪回溯顺序)、`matchDepth`
  上限与 `"pattern too complex"` 措辞——**全部待 12 差分核对**,本文给骨架不编造精确字节。
- **错误措辞精确格式**(§2.4):`bad argument #n to 'fn' (...)` 的标点、`got no value` vs `got nil`、各 stdlib
  的 `extramsg`、`fname` 推断(getobjname vs 注册名兜底,§2.4.1)——待 12 核对(对齐 09 §9.3)。
- **math.random 序列豁免**(§8.4):Go `math/rand` ≠ C `rand`,random 序列无法与 5.1 一致,差分豁免(只验范围)。
  是否复刻某 C rand 算法(无意义,C rand 平台相关)已否决。记口径。
- **os.date / os.setlocale 的 locale 差异**(§9.2/§9.4):纯 Go 无 C locale,`string.upper`/`%a`/`os.date` 月名锁
  ASCII/英文,与 C Lua 非 C locale 行为有差。`os.date` 的 `%c/%x/%X` 格式待 12 核对。已知 P1 限制。
- **io 完整度**(§10.1):io.open/file:read/write/seek/lines 等完整文件操作 P1 是部分实现还是缺口待定;
  io.popen/tmpfile P1 不做。默认输出可重定向(宿主捕获 print 输出)的配置接口待定(记缺口)。
- **`os.exit` 嵌入语义**(§9.3):P1 默认不真退出(防杀宿主进程);具体语义(抛特殊错误 vs 标记请求退出 vs
  返回控制权)待定,记缺口。
- **`CheckInt` 非整数 double 行为**(§2.2):P1 定「截断(向零),不报错」(对齐 5.1 `luaL_checkinteger`)。
  超 int64 范围行为待 12 核对。
- **`CheckFunction` 严格度 / pcall 的 f 放宽**(§2.6):`CheckFunction` 严格要 function tag;`pcall`/`xpcall` 的 f
  是否接受有 `__call` 的对象由 09 定,记口径。
- **`table.getn`/`setn` 是否提供**(§7.5):P1 默认不提供(5.1 已废);若 conformance 需要补为 `#t`/空操作。
- **`unpack` 大表上限措辞**(§4.5):`unpack` 超栈上限报错(`"too many results to unpack"`?)待 12 核对。
- **stdlib 与 08 的回调-yield 交互**(§11):stdlib 回调点(sort comparator / gsub repl / pcall f)被 yield 穿越
  时报错(08 §5),精确措辞与触发点待 08 定稿后校验。

---

相关:[01-value-object-model](./01-value-object-model.md)(NaN-box 类型检查 / canonicalize / String 字节 / Table) ·
[02-bytecode-isa](./02-bytecode-isa.md)(SELF/CALL 调用约定 / vararg) ·
[04-frontend-parser-codegen](./04-frontend-parser-codegen.md)(loadstring 经 Compile 的编译路径) ·
[05-interpreter-loop](./05-interpreter-loop.md)(§7.5/§7.6 host 调用约定 / callLuaFromHost / vm.raise / §10.2 callFixed) ·
[06-memory-gc](./06-memory-gc.md)(§6.3 shadow stack 使用约定 / §9.1 string intern / §10 finalizer / §11 GC 压力 fuzz) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(§3.4 string `__index` / §5.2 parseLuaNumber / §11 `__tostring`) ·
[08-coroutines](./08-coroutines.md)(coroutine 库机制 / §5 yield 不跨 host call) ·
[09-errors-pcall](./09-errors-pcall.md)(error/assert/pcall/xpcall 机制 / §9.3 bad argument 措辞 / §12 协程错误 / §13 debug 库) ·
[11-embedding-arena-abi](./11-embedding-arena-abi.md)(§10 host 注册 State.Register / per-item Push/Pop/ToNumber / §6 句柄表) ·
[12-testing-difftest](./12-testing-difftest.md)(format/pattern/tostring/sort/措辞逐字节核对 / GC 压力 fuzz / 库存在性) ·
[architecture](../architecture.md)(`internal/stdlib` 包布局 / 构建顺序第 10 步) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
roadmap:`docs/design/roadmap.md` (§4 stdlib 是 host function / §5 原则 2 差分一致 / 原则 4 不做完备性 / §6 锁 Lua 5.1)









