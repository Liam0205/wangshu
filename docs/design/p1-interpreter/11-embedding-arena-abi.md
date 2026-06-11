# P1:嵌入 API 与 arena ABI(字段级 spec)

> 状态:**设计阶段,可实现深度**。本文是望舒**公共嵌入接口**(root package `wangshu`)与
> **arena ABI 二进制布局**的单一事实源:`Compile`/`Program`/`NewState`/`Arena` 门面、
> `Program.Call(state, arena, args)` 语义、arena 的类型化扁平列 / 字符串区 / presence bitmap 的
> **字段级二进制 spec**、VM 零拷贝读机制、per-item 栈式简易 API、host function 注册、
> lightuserdata 句柄表、Program/State 并发语义、gopher-lua drop-in 定位。
> **本文是全项目点名的最大文档缺口的落地**——[embedding-contract](../../../llmdoc/reference/embedding-contract.md)
> 与 [doc-gaps](../../../llmdoc/memory/doc-gaps.md) 第一条均指出:roadmap §8 仅给 arena ABI 概念,
> **字符串区编码、presence bitmap 位布局、`args` 与 arena 的精确关系未给 spec**;本文把它们定死到字段级。
> 上游契约:`docs/design/roadmap.md` (§8 宿主嵌入契约、§1 两个校准测量、§6 非目标),
> [embedding-contract](../../../llmdoc/reference/embedding-contract.md)。
> 值/对象侧:[01-value-object-model](./01-value-object-model.md)(§3 NaN-boxing、§3.4 canonicalize、
> §3.5 lightuserdata 限制点名本文、§1 host 注册表)。执行侧:[05-interpreter-loop](./05-interpreter-loop.md)
> (§1.4 enterLuaFrame、§7.3 host→Lua 重入、§7.6 HostFn 签名)。
> 内存侧:[06-memory-gc](./06-memory-gc.md)(§1 arena 组织、§6 shadow stack)。包布局见 [architecture](../architecture.md) §1。

对应 Go 包:**root package `wangshu`**(公共门面 `wangshu.go`,见 [architecture](../architecture.md) §1)、
`internal/abi`(arena ABI 列布局编解码,新增包)、`internal/crescent`(`Program.Call` 进入 execute、host 调用)、
`internal/value`(NaN-box,arena 列读出即时装箱)、`internal/arena`(VM 主 arena)。

---

## 0. 本文在 P1 中的位置:少数描述「公共 API」的文档

P1 的绝大多数文档([01](./01-value-object-model.md)..[10](./10-stdlib.md))写的是 `internal/` 实现细节,**外部不可见**([architecture](../architecture.md) §1:公共 API 只在 root package)。本文相反——它定义**宿主开发者唯一能看到的那一层**:`wangshu.Compile`、`wangshu.Program`、`wangshu.NewState`、`wangshu.Arena`。这层 API 的形状是**第 1 天就要对的契约**:一旦宿主依赖了 `Program.Call(state, arena, args)` 的签名与 arena 的二进制布局,后续 P2-P5 升层就不能破坏它(否则宿主代码全废)。所以本文与 [01](./01-value-object-model.md)(值表示第 1 天承诺)同属「不可逆决定」一类。

设计的全部张力来自一句话:**接口必须天然鼓励「列内核」形状,同时不牺牲 gopher-lua 级的易用性**。这两个目标拉向相反方向——列内核要求「批量数据经 arena、一次调用一次跨界」(高性能但需宿主改造),易用性要求「per-item 推栈、随手调用」(上手快但落在边界成本主导档)。本文的回答是**双轨 API**:arena 批量轨(§3-§5,主推)+ per-item 栈式轨(§7,对标 gopher-lua,明确标性能档位)。两轨共享同一个值世界([01](./01-value-object-model.md))与同一个解释器([05](./05-interpreter-loop.md))。

> 为什么这是「最大缺口」:roadmap §8 与 [embedding-contract](../../../llmdoc/reference/embedding-contract.md) 把 arena ABI 说成「类型化扁平列 + 字符串区 + presence bitmap」——这是**概念**,不是 spec。宿主程序员要写「往 arena 填一列 float64、填一列字符串、标某些槽为 null」的代码,必须知道**每个字节放什么**:列头多少字节、字符串区 offset 表怎么编码、bitmap 哪个 bit 对应哪个槽、`Program.Call` 的 `args` 和 arena 列在脚本里怎么分别拿到。本文 §3-§5 逐一定死。

设计意图回顾(不重复 [design-premises](../../../llmdoc/must/design-premises.md) 前提一的长论证,只给指针):两个校准测量(roadmap §1)钉死「per-item 跨界形态下边界成本主导,脚本本体再快也被吃光」;故接口被设计成**让宿主一次调用就把整批数据交给 VM,循环写在 Lua 内**。arena 是这个意图的物理载体——它让「整批列数据」有一个 VM 能零拷贝读的二进制形态。

---

## 1. 公共 API 门面(`wangshu.go`)

[architecture](../architecture.md) §1 规定 root package `wangshu` 只暴露四个东西:`Compile` / `Program` / `NewState`(返回 `State`)/ `Arena`。本节给它们的签名与语义;实现全部委托 `internal/`。

### 1.1 全景:四个公共类型/函数的关系

```
                  source code (Lua 5.1 文本)
                        │
                  wangshu.Compile(src, chunkname)
                        │ 词法→语法→codegen→可编译性探测(P1 占位)
                        ▼
                  *wangshu.Program    ── 不可变编译产物(主 Proto + 嵌套)
                        │                  可多 goroutine 共享(§8)
                        │
   宿主:每 goroutine 一个 ──► *wangshu.State  ── VM 实例(globals/registry/arena/host 注册表/句柄表)
                        │                  含可变状态,不可跨 goroutine 并发(§8)
                        │
   宿主:构造列数据 ──► *wangshu.Arena ── 宿主侧列容器(类型化扁平列 + 字符串区 + bitmap)
                        │
                        ▼
   prog.Call(state, arena, args)  ── 一次调用一次跨界;脚本内迭代 arena 列;返回 results / error
```

四者职责互斥:`Program` 是「编译好的代码」(无状态、不可变);`State` 是「一个 VM 的运行期世界」(有状态、单线程);`Arena` 是「一批要喂给脚本的列数据」(宿主构造、VM 零拷贝读);`Call` 是「把代码 + 数据在某个 VM 上跑一次」。

### 1.2 `NewState`:创建 VM 实例

```go
package wangshu

// Options 配置一个 State 的资源参数与行为开关。
type Options struct {
    InitialArenaBytes uint32 // VM 主 arena 初始容量(默认 64 KiB,见 06 §3)
    MaxArenaBytes     uint32 // 主 arena 上限(默认 4 GiB,bump/cap 是 uint32,06 §3)
    MaxCallDepth      int    // Lua 调用深度上限(CallInfo 数,05 §7.4;默认如 200000)
    MaxCCalls         int    // host→Lua 重入深度上限(真 Go 栈,05 §7.4;默认 200)
    GCPause           int    // GC pacing,存活量百分比(默认 200 = 2.0x,06 §8.3)
    Libs              Lib      // 标准库位掩码(零值=LibsDefault 对齐 gopher-lua 面;LibsSafe=计算沙箱;见 10 §12.1)
    Exclude           []string // 函数级排除路径(注册后删除),如 {"os.execute","io.open"}(10 §12.1 三层粒度)
}

// NewState 创建一个独立 VM 实例。它持有:
//   - globals 表(_ENV / R1 根,06 §5.1)
//   - registry 表(host 引用锚点,06 §5.1 R2)
//   - 主 arena(动态值世界:String/Table/Closure/...,01 §1)
//   - mainThread(主协程,06 §5.1 R3)
//   - host function 注册表([]HostFn,Go 堆,01 §1;整数 HostFnID 引用)
//   - 句柄表([]any,lightuserdata 间接索引,§6)
//   - string intern 表、GC collector(06)
// State 含可变状态,**每 goroutine 一个**(§8 并发语义)。
func NewState(opts Options) *State

type State struct { /* 不导出字段;内部持 *internal/crescent.VM */ }
```

`State` 是 [05](./05-interpreter-loop.md) 里 `VM` 的公共包装。`VM` 是 `internal/crescent` 的执行引擎,持有 [05](./05-interpreter-loop.md) §0 / [06](./06-memory-gc.md) §5.1 列举的全部运行期状态。`State` 只是把它包成公共类型,不暴露 `VM` 本身(防外部依赖实现)。

### 1.3 `Compile`:源码 → Program

```go
// Compile 把 Lua 5.1 源码编译为可执行的 Program。
//   - 词法(03)→ 语法(04)→ codegen(04)→ 主 Proto + 嵌套 Proto 树(01 §5.7)。
//   - 含**可编译性探测与层级决定**(roadmap §8 / embedding-contract):
//     P1 只有解释层,探测是 P2 的静态可编译性分析(roadmap §4 P2)的**占位**——
//     P1 恒返回「解释」,层级决定恒为 tier-0(crescent)。探测接口预留,P2 填充。
//   - chunkname 用于错误回溯的源名(09:traceback 显示 "chunkname:line")。
// 返回的 *Program **不可变、可跨 goroutine 共享**(§8)。编译错误(语法错)→ Go error。
func Compile(source []byte, chunkname string) (*Program, error)
```

要点:

- **Compile 不需要 State**。编译只产生不可变代码(Proto 住 Go 堆,[01](./01-value-object-model.md) §1),不碰任何 VM 运行期状态。这让「一次编译、多 State 复用」成立(§8):一个 `*Program` 可被多个 `*State`(多 goroutine)各自 `Call`。
  - **例外:字符串常量的 intern 归属**。[01](./01-value-object-model.md) §5.7 说 Proto 的 `Consts` 里字符串常量是「指向 arena 的 GCRef」。但 Compile 不持 State/arena,何来 arena?**定稿:P1 的 Proto 字符串常量在编译期先以 Go `[]byte`/`string` 形态留存(`Proto.Consts` 的字符串元素暂存原始字节),首次被某个 State `Call` 时**惰性 intern 进该 State 的 arena**(写回该 State 私有的常量 GCRef 缓存)。即 `Program` 持「常量的字节原文」,`State` 持「该 Program 在本 VM 内 intern 后的 GCRef 表」。这样 Program 跨 State 共享时,每个 State 有自己的常量串副本(各自 arena),互不干扰。**这是对 [01](./01-value-object-model.md) §5.7「字符串常量是 arena GCRef」的并发细化**,需回填 [01](./01-value-object-model.md)(§9 doc-gap)。
- **可编译性探测 P1 占位**:roadmap §8 把「可编译性探测与层级决定」放进 `Compile`,但那是 P2 能力(roadmap §4 P2「静态可编译性分析:varargs/coroutine/debug 标记不升层」)。P1 的 `Compile` 把这步实现为**恒真占位**:所有函数标 tier-0、恒「解释」。接口形状(返回的 Program 带每个 Proto 的「可升层标记」字段)P1 就定好,P2 只填充探测逻辑,不改 API。

### 1.4 `Program`:不可变编译产物

```go
// Program 是一次编译的产物:主 chunk 的 Proto + 其嵌套 Proto 树(01 §5.7)。
// 不可变:编译后内容固定,无可变状态,故可被多个 State / 多个 goroutine 并发 Call(§8)。
type Program struct {
    // 不导出:主 Proto + protos []*bytecode.Proto 注册表(01 §5.7)。
    // 每个 Proto 持 Code/Consts/Protos/UpvalDescs/...(01 §5.7)。
    // 可升层标记(P1 恒 tier-0)预留字段,P2 填充。
}

// MainProto 等内省接口可选暴露(用于工具 dump / REPL),P1 可不导出。
```

`Program` 对应 [01](./01-value-object-model.md) §5.7 的 `protos []*Proto` 注册表 + 主 Proto 下标。它是「代码侧住 Go 堆、经整数 ID 引用」([01](./01-value-object-model.md) §1)的公共载体——**代码不进 arena、不参与 GC mark-sweep**([06](./06-memory-gc.md) §0 铁律 2),所以 Program 天然可跨 goroutine 只读共享。

### 1.5 `Program.Call`:一次调用一次跨界

```go
// Call 在 state 上执行 prog 的主 chunk,一次调用 = 一次跨界(roadmap §8 列内核形状的 API 落地)。
//   - state:目标 VM(提供 globals/arena/host 注册表/句柄表)。
//   - arena:**批量列数据**(可为 nil = 无批量数据)。脚本内经暴露的 arena 句柄迭代(§5)。
//   - args:**标量调用参数**,传给主 chunk 的固定参数或 `...`(§4.3)。
// 返回:results = 主 chunk 的返回值(已从 VM arena 拷出为公共 Value,§4.4);
//       error = Lua 运行期错误转成的 Go error(§2,09 的 LuaError → Go error)。
func (prog *Program) Call(state *State, arena *Arena, args ...Value) ([]Value, error)
```

`Call` 的执行路径(衔接 [05](./05-interpreter-loop.md)):

```
prog.Call(state, arena, args):
  1. (首次)惰性 intern prog 的字符串常量进 state.arena(§1.3)。
  2. 把 arena(宿主侧列数据)登记/映射进 VM 可读视图(§5.1:零拷贝挂载或视图包装)。
  3. 构造主 chunk 的 Lua closure(主 Proto + _ENV upvalue = state.globals)。
  4. 把 args(标量参数)推到 mainThread 值栈,作为主 chunk 的实参/vararg(§4.3)。
  5. 把 arena 句柄作为一个特殊 Lua 值(userdata,§5.2)注入——
     脚本通过全局名(如 `arena`)或主 chunk 首参拿到它。
  6. enterLuaFrame(05 §1.4)+ vm.execute(th)(05 §2.3/§7.3:这是一个 host→Lua 的
     reentry 边界,标 callStatus_fresh,05 §7.3)。
  7. execute 返回:
       - nil:正常返回 → 把 mainThread 栈上的返回值拷出为 []Value(§4.4)→ (results, nil)。
       - *LuaError:转成 Go error(§2)→ (nil, err)。
```

**「一次调用一次跨界」的精确含义**:宿主调一次 `Call`,Go→VM 边界只跨一次(步骤 6 的 `execute` 入口);此后脚本在 VM 内对 arena 列的迭代(`for i=1,n do ... arena.col[i] ... end`)**全在 VM 内**,不再跨界。这正是 roadmap §1 / [design-premises](../../../llmdoc/must/design-premises.md) 前提一要的形状:边界成本被摊薄到「每批一次」,而非「每 item 一次」。对比 per-item 轨(§7):那里每个 `state.Call()` 都跨一次界,落在边界成本主导档。

---

## 2. 错误返回:Lua 错误 → Go error

[09](./09-errors-pcall.md)(待创建)定义 `LuaError`(`internal/crescent`,持 `value.Value` 错误值 + traceback + level)。公共边界要把它转成 Go `error`。

```go
// LuaError 是 Lua 运行期错误在公共 API 的表示(实现 error 接口)。
type LuaError struct {
    Value      Value  // 错误值(通常 string,也可是任意 Lua 值,Lua 语义;已拷出 arena,§4.4)
    Traceback  string // 调用栈回溯(09:chunkname:line + [C] 帧)
}
func (e *LuaError) Error() string  // 返回 Value 的字符串形式 + traceback 摘要
```

转换规则(`Call` 步骤 7):

- **未被 pcall 捕获的错误**一路冒泡到 `execute` 返回([05](./05-interpreter-loop.md) §9.3:无 pcall 保护时错误 return 到顶层 `Program.Call`),`Call` 把内部 `*crescent.LuaError` 包成公共 `*LuaError` 返回。
  - 错误值若是 arena 内的 String/Table,必须**先拷出**(§4.4)再放进公共 `LuaError.Value`——否则 `Call` 返回后 VM arena 可能 GC,公共 error 持悬垂 GCRef。
- **被 pcall 捕获的错误**不冒泡到 `Call`——它在脚本内被 `pcall` 转成 `(false, errval)` 返回值([05](./05-interpreter-loop.md) §9.3),`Call` 看到的是正常返回。
- **host function 内部 Go panic**:[05](./05-interpreter-loop.md) §9.4 的顶层 recover 兜底——`Call` 在最外层 `defer recover()`,把 host 的意外 Go panic 转成一个 `*LuaError`(标「VM 损坏」),并把 `State` 标记为不可继续使用(后续 `Call` 直接返回错误)。这是**安全网,不是正常路径**(host 不该 Go panic,应走 `vm.raise`,[05](./05-interpreter-loop.md) §9.4)。
- **编译错误**走 `Compile` 的 error 返回(§1.3),不经 `Call`。

> 设计纪律:公共 API 永不 Go panic 给宿主(除非宿主自己的 HostFn 有 bug 且被兜底 recover——那时也转成 error)。宿主调 `Compile`/`Call` 的错误处理是标准 Go `if err != nil`。

---

## 3. arena ABI 字段级 spec(本文核心,填最大缺口)

**这是全文重心。** roadmap §8 / [embedding-contract](../../../llmdoc/reference/embedding-contract.md) 只说「类型化扁平列 + 字符串区 + presence bitmap」;本节给每个字节的二进制布局,让宿主能据此实现填充代码、VM 能据此实现零拷贝读。

### 3.0 总体设计决策:arena 列数据住宿主侧 buffer,VM 零拷贝读其元素

**第一个关键决策**(任务点名「列数据住 VM 主 arena 还是宿主侧独立 buffer」,讨论两种方案):

| 方案 | 列数据物理位置 | VM 如何读 | 权衡 |
|---|---|---|---|
| **(A) 住 VM 主 arena** | 宿主把列写进 VM 的 `internal/arena`(与 String/Table 同一块线性内存) | 列就是 arena 内的字节,VM 直接 `words[]`/`bytes[]` 访问 | 列数据进 GC 视野(虽是裸字节,但占 arena、可能触发 GC pacing);宿主要拿到 VM arena 的写入权,**破坏封装**(arena 是 `internal/`) |
| **(B) 住宿主侧独立 buffer(选定)** | `wangshu.Arena` 自带 `[]float64`/`[]int64`/`[]byte`/`[]uint64`(普通 Go slice,Go 堆) | VM 读列元素时**即时 NaN-box** 成 Value(§4),不复制整列 | 列数据不进 VM arena、不进 GC 视野;宿主用纯 Go slice 构造(易用);**零拷贝读**=读一个元素就地装箱,整列不搬 |

**定稿:方案 (B)——arena 列数据住宿主侧 `wangshu.Arena` 的 Go slice,VM 零拷贝读其元素并即时 NaN-box。** 理由:

1. **封装**:宿主用公共 `wangshu.Arena`(普通 Go slice 字段)构造数据,不需要、也不应碰 `internal/arena`(VM 私有线性内存)。
2. **GC 解耦**:列数据(可能很大,百万行 float64)不进 VM 的 mark-sweep 视野([06](./06-memory-gc.md))——它们是宿主的 Go slice,由 Go GC 管(但内容是纯标量/字节,Go GC 也不追踪内部)。VM 的 GC 只管 arena 内的动态对象([06](./06-memory-gc.md) §0 铁律 2),不被宿主的大批量列拖累 pacing。
3. **零拷贝读的真实形态**:VM 执行 `arena.col[i]` 时,不是「把整列 col 拷进 VM arena 再读」,而是「读宿主 `[]float64` 的第 i 个元素(§3.5 的 `column.f64[i]`)这一个 float64,就地 `value.NumberValue(...)` 装箱成一个栈上的 Value」(§4)。**整列从不复制**——这就是 [embedding-contract](../../../llmdoc/reference/embedding-contract.md)「宿主直接写、VM 零拷贝读」的兑现。

> 与 roadmap §8 / [embedding-contract](../../../llmdoc/reference/embedding-contract.md)「arena 与 value-representation 的自管线性内存是同一份内存的不同视角」的关系——**澄清(本文定稿的细化)**:那句话的精确含义是「**逻辑上**两层共见同一批数据」,不是「**物理上**列数据躺在 VM arena 里」。P1 选 (B):列数据物理在宿主 buffer,VM 零拷贝读;「共见」体现在 VM 读列元素时直接装箱进值世界,无中间拷贝层。**P3 才把 arena 列映射进 Wasm linear memory**(§11),那时「物理同一块」更进一步(linear memory 既是 VM 值世界又承载列)。P1 的 (B) 与 P3 的映射不冲突:P1 定的**列布局 ABI**(下面 §3.1-§3.4)是「宿主如何组织一列数据」的逻辑 spec,P1 用 Go slice 承载、P3 用 linear memory 承载,**布局 spec 不变**(§11)。

**ABI 的双重身份(为什么仍要定二进制布局,即使 P1 用 Go slice)**:`wangshu.Arena` 的公共 API 是「类型化的 Go 方法」(§3.5,如 `AddFloatColumn`),宿主**不直接写二进制**。但本节仍给**二进制布局 spec**,因为:① 它是 **P3 Wasm linear memory 的布局**(§11)——P3 把列搬进 linear memory 时按此字节布局;② 它定义了「一列 = 类型 tag + 长度 + 数据 + presence bitmap + (字符串列)字符串区」的**逻辑结构**,P1 的 Go slice 实现就是这个逻辑结构的内存化。即:**逻辑布局 spec 是 ABI 契约,P1/P3 是它的两种物理承载**。下面先给逻辑/二进制布局(§3.1-§3.4),再给 P1 的 Go 承载(§3.5)。

### 3.1 一个 arena 的整体内存图

一个 arena 是**一组列(columns)+ 一个共享字符串区(string region)+ 每列一个 presence bitmap**。逻辑/二进制布局(P3 linear memory 形态;P1 用 §3.5 的 Go 结构等价承载):

```
arena(逻辑布局):
┌─────────────────────────────────────────────────────────────────┐
│ ArenaHeader                                                       │
│   magic       u32   = 0x57414E41 ("WANA")  校验                    │
│   version     u16   = 1                     ABI 版本                │
│   ncols       u16   列数                                           │
│   nrows       u32   行数(所有列等长 = nrows;见下「等长约定」)      │
│   strRegionOff u32  字符串区在 arena 的字节偏移(P3 linear mem 用)  │
│   strRegionLen u32  字符串区字节长                                  │
├─────────────────────────────────────────────────────────────────┤
│ ColumnDesc[ncols]   列描述符数组(每个 16 字节,见 §3.2)            │
│   [0]: {tag, nameOff, dataOff, bitmapOff}                         │
│   [1]: ...                                                        │
│   ...                                                             │
├─────────────────────────────────────────────────────────────────┤
│ 列数据区(各列的扁平数据,按 ColumnDesc.dataOff 定位):              │
│   col0 data: [] (float64/int64/bool/strslot,见 §3.3)             │
│   col0 presence bitmap: ceil(nrows/64) 个 u64(见 §3.4)           │
│   col1 data: ...                                                  │
│   ...                                                             │
├─────────────────────────────────────────────────────────────────┤
│ 字符串区(string region,所有字符串列共享,见 §3.3.4):              │
│   offsets[?]   u32 偏移表                                          │
│   bytes[?]     连续 UTF-8/任意字节池                               │
├─────────────────────────────────────────────────────────────────┤
│ 列名区(可选,name 字节池,ColumnDesc.nameOff 索引):                │
│   各列名字符串(便于脚本按名访问,§5.2)                            │
└─────────────────────────────────────────────────────────────────┘
```

**等长约定**:一个 arena 内**所有列行数相同 = `nrows`**(它表示「一批数据有 nrows 行,每行有 ncols 个字段」,是典型的列存表)。这简化脚本侧迭代:`for i=1,arena.nrows do ... end` 对所有列统一。变长列(不同列不同长)P1 不支持(记 §12 缺口;真实列内核负载是规整表,等长足够)。

### 3.2 ColumnDesc:列描述符(16 字节)

每列一个描述符,定位该列的类型、名字、数据、bitmap:

```
ColumnDesc(16 字节):
  tag        u8    元素类型 tag(见下表)
  flags      u8    bit0 = 该列有 presence bitmap(0 = 全 present,bitmapOff 无效)
                   bit1 = 该列只读(脚本不可写回,P1 全只读,见 §5.3)
                   bits[7:2] reserved
  _pad       u16   对齐填充
  nameOff    u32   列名在「列名区」的字节偏移(u32);名长以 NUL 结尾或前置长度(§3.2.1)
  dataOff    u32   该列数据区在 arena 的字节偏移
  bitmapOff  u32   该列 presence bitmap 在 arena 的字节偏移(flags.bit0=1 时有效)
```

**元素类型 tag**(`internal/abi` 的 `ColType`):

| tag | 名称 | 元素宽 | 数据区元素类型 | 读出为 Lua | canonicalize |
|---|---|---|---|---|---|
| `1` | `ColFloat64` | 8 B | `float64` | number(double) | **是**(§4.1,过 [01](./01-value-object-model.md) §3.4) |
| `2` | `ColInt64` | 8 B | `int64` | number(double);`|v| > 2^53` **超界报错**(§3.3.2 定稿) | — |
| `3` | `ColBool` | 1 B(打包见 §3.3.3) | `bool` | boolean | — |
| `4` | `ColString` | 8 B(strslot) | `StrSlot{off u32, len u32}` → 字符串区 | string | — |

> tag 集**封闭为这 4 个**(对齐 roadmap §8「`[]float64`/`[]int64`/`[]bool` + 字符串区」)。无 nested/struct 列(列内核负载是扁平标量列,roadmap §6 用 Arrow 数据搬家、不让 Lua 直访复杂 Go 对象)。未来若加 `ColBytes`(二进制 blob 列)走字符串区机制扩展,记 §12。

#### 3.2.1 列名编码

列名放「列名区」(arena 末段的字节池),`ColumnDesc.nameOff` 指向。编码:**前置 u16 长度 + 名字节**(`{nameLen u16, nameBytes[nameLen]}`),无 NUL 依赖。脚本按名访问列(`arena.price` → 找 name=="price" 的列)在挂载时(§5.1)建一张 `map[string]colIndex`(VM 侧 Go map),O(1) 查名。

### 3.3 类型化扁平列:数据区编码

每列数据区是**紧致的元素数组**,按 tag 的元素宽连续排布。`dataOff` 指向首元素。

#### 3.3.1 ColFloat64(`[]float64`)

```
data[dataOff..]: nrows 个 float64(IEEE-754 double,小端),连续。
  第 i 行 = data_as_f64[i]    (i ∈ [0, nrows))
```

读出(§4.1):`value.NumberValue(data_f64[i])`——**过 [01](./01-value-object-model.md) §3.4 canonicalize**:宿主可能写入了负 NaN(`0xFFF8..` 段的非规范 NaN),`NumberValue` 的 `f != f` 兜底把它规范成 `0x7FF8…`,**防止外部负 NaN 渗入值世界破坏 NaN-boxing tag 不变式**([01](./01-value-object-model.md) §3.4 点名「arena ABI 读入的 float 列必须规范化 NaN」)。这是 ColFloat64 读出的**唯一非平凡处理**,绝不可省。

#### 3.3.2 ColInt64(`[]int64`)——关键决策:精度边界

```
data[dataOff..]: nrows 个 int64(小端补码),连续。
```

**关键决策**(Lua number 是 double,int64 进 VM 转 double 会丢 >2^53 精度;**经评审定稿:超界报错,宁报错不错果**):

> **决策:P1 的 ColInt64 读出时检查 `|v| ≤ 2^53`——范围内转 double(`value.NumberValue(float64(v))`);超界抛运行期错误**(措辞建议 `int64 column value out of exact range (|v| > 2^53)`,带列名/行号,精确措辞待 [12](./12-testing-difftest.md) 登记——此错误是望舒扩展,官方/gopher 无对应,差分豁免)。Lua 5.1 无整数子类型(roadmap §6)是内在约束,但**静默丢精度对 ID 类数据(雪花 ID/纳秒时间戳,量级 2^63)意味着相等比较悄悄错果且差分测不出**——这违背贯穿原则 2 的精神(防静默错果),故 P1 选报错而非截断。

理由与权衡(三条路):

| 路 | 读出 | 优点 | 代价 |
|---|---|---|---|
| (i) 静默转 double | `NumberValue(float64(v))` | 实现最简 | `|v| > 2^53` 静默丢低位——ID 类数据**静默错果**,排查成本极高 |
| **(ii) 超界报错(P1 选定)** | 范围内同 (i);超界抛错 | 范围内零额外语义负担;**超界宁报错不错果**,错误在首次读出即暴露 | 每元素读出多一次范围比较(可接受:与 NaN canonicalize 同级别的逐元素开销);宿主须为大整数列显式改道 |
| (iii) 作为 lightuserdata 透传 | `LightUDValue(...)`,48-bit 截断 | 48-bit 内精确 | 脚本里不能直接算术(要 host 解包,per-item 跨界反前提一);48-bit 仍不够 full int64 |

**对宿主的契约**:ColInt64 适合「值域在 ±2^53 内的整数」(行号、小计数、价格分、百分比)。若宿主的 int64 是**大 ID / 纳秒时间戳 / 哈希值**,**禁止**走 ColInt64(会在首次读出时报错),应改用 **ColString 列**(整数格式化为字符串,相等比较语义精确)或 **句柄表 + lightuserdata**(§6,宿主自管解包)。报错而非静默,使「选错列类型」在联调期即暴露,而非在生产数据撞上 2^53 时静默错果。

> 记 §12 缺口:「ColInt64 是否需要 `ColInt64Exact` 变体(读出为不可直算的精确整数 box)」与「超界检查的批量优化(列级 min/max 预检替代逐元素比较)」待真实宿主反馈。

#### 3.3.3 ColBool(`[]bool`,位打包)

bool 列用**位打包**(每行 1 bit)而非每行 1 字节,省 8x 空间(列内核可能百万行):

```
data[dataOff..]: ceil(nrows/8) 字节,每 bit 一行:
  第 i 行的值 = (data[i>>3] >> (i&7)) & 1    (bit i,字节内小端位序)
```

读出(§4):`value.BoolValue((data[i>>3]>>(i&7))&1 == 1)`(`True`/`False`,[01](./01-value-object-model.md) §3.5)。

> **注意区分 ColBool 的位打包与 presence bitmap(§3.4)**:两者都是按位的,但语义不同。ColBool 的 bit = 「这行的布尔**值**是 true/false」;presence bitmap 的 bit = 「这行**有没有值**(1=present,0=null→nil)」。一个 ColBool 列同时有**值位数组**(data 区)和**presence bitmap**(若 flags.bit0=1):某行 present 但值为 false,与某行 null,是两件事(前者读出 `false`,后者读出 `nil`)。

#### 3.3.4 ColString(字符串区编码)——关键决策:offset 表 + 字节池

**字符串区编码**(任务点名,给精确布局):采用 **Arrow 风格:每列一个 strslot 数组(进数据区)指向共享字符串区的 offset 表 + 连续字节池**。

ColString 的**数据区**是 `nrows` 个 `StrSlot`(每个 8 字节):

```
data[dataOff..]: nrows 个 StrSlot(每个 8 字节):
  StrSlot:
    off  u32   该字符串在「字符串区字节池」的起始字节偏移(相对 strRegionOff)
    len  u32   该字符串字节长
  第 i 行字符串 = strRegion.bytes[ slot[i].off : slot[i].off + slot[i].len ]
```

**字符串区**(arena 内 `strRegionOff..strRegionOff+strRegionLen`,所有 ColString 列共享):

```
字符串区:
  bytes[]   连续字节池:所有字符串内容首尾相接(无分隔符;靠 StrSlot.{off,len} 切分)
            编码 = 任意字节(Lua 5.1 string 是字节串,非必 UTF-8;按字节存)
```

> **两种字符串区方案的选择(讨论)**:
> - **方案 α(选定):每列 StrSlot 数组(off,len 进数据区)+ 共享字节池**。优点:多列可共享同一字节池(去重相同字符串);StrSlot 定长 8 字节,随机访问第 i 行 O(1)。这是上面的定稿。
> - **方案 β:Arrow 标准的 `offsets[nrows+1] u32 + values bytes`(单调偏移,len 由相邻 offset 差算)**。优点:更省(每行 4 字节而非 8);len = `offsets[i+1]-offsets[i]`。缺点:要求同一列的字符串在字节池里**连续且有序**,多列不能共享字节池(每列独立 offsets+values)。
> **P1 定稿方案 α**(StrSlot{off,len}),因为它允许**跨列共享字节池 + 字符串去重**(同一个字符串在多列/多行只存一份字节,多个 StrSlot 指向它),这对「枚举值列」(如 1M 行的「状态」列只有 5 种取值)极省。方案 β 的 4 字节/行优势在 P1 不如 α 的去重收益。记 §12:若 P3 linear memory 更适配 β(Arrow 互操作),再评估。

读出(§4.2):**关键难点——Lua string 要 intern**([01](./01-value-object-model.md) §5.1 全 intern,相等串同一 GCRef)。arena 字符串区的字节**不是** intern 的 String 对象,读出时必须处理:

- **P1 定稿:读出 ColString 元素时,把 `strRegion.bytes[off:off+len]` 拷贝进 VM arena 并 intern**(`stringTable.Intern(bytes)`,[06](./06-memory-gc.md) §9.1)→ 得到一个真正的 String GCRef → NaN-box 成 string Value。**这一步有拷贝**(字节从宿主 buffer 拷进 VM arena 的 String 对象)。

这与「零拷贝读」矛盾吗?——**部分矛盾,记为口径**(任务点名「字符串零拷贝的难点」):

> **字符串零拷贝口径**:float64/int64/bool 列读出是**真零拷贝**(读一个标量就地装箱,无内存搬动)。但 **string 列读出无法零拷贝**——因为 Lua 5.1 要求字符串 intern([01](./01-value-object-model.md) §5.1:`rawequal` 退化为 GCRef 比较,依赖 intern),而 arena 字符串区的裸字节不是 intern 的 String 对象。**P1 定稿:string 列读出时拷贝进 arena intern**(每个被脚本读到的字符串元素,首次读时拷贝+intern,得 String GCRef)。
>
> **优化(可选,记 §12)**:① **intern 缓存**——同一 StrSlot 被反复读(循环里多次访问同一行的字符串列)时,缓存「StrSlot → 已 intern 的 GCRef」映射(VM 侧 Go map 或 arena 句柄),避免重复拷贝+intern;② **特殊不可变 String 视图对象**——给 String 加一种「外部字节视图」变体(word1 标志 + 指向宿主 buffer 的句柄,而非 arena 内字节),`rawequal` 对视图退化为「内容比较」而非 GCRef 比较。但视图破坏「string 相等 = GCRef 相等」的全局不变式([01](./01-value-object-model.md) §5.1 / §7 不变式 3),复杂度高,**P1 不做**。
>
> **倾向口径:P1 拷贝进 arena intern(正确性优先,语义最简),配 StrSlot→GCRef intern 缓存减少重复拷贝;特殊视图对象留作 P3+ 优化评估。** 这意味着「string 列是次于 float/int/bool 列的性能档」——若宿主的列内核大量读字符串列,字符串 intern 拷贝是成本点(但仍远优于 per-item 跨界,因为拷贝在 VM 内、不跨边界)。

### 3.4 presence bitmap:每列一个,每槽 1 bit

**presence bitmap 位布局**(任务点名,位序定死):每列一个 bitmap(`flags.bit0=1` 时存在),标记每行有值/null。

```
bitmap[bitmapOff..]: ceil(nrows/64) 个 u64(小端 u64 字数组):
  第 i 行是否 present = (bitmap_as_u64[i>>6] >> (i&63)) & 1
                       (bit i = 行 i;1 = 有值,0 = null → 读出为 Lua nil)
  位序:小端 u64 内,bit 0 = 行 (word*64+0),bit 63 = 行 (word*64+63)
```

**定死规则**:

- **单位 = u64 字数组**(不是字节数组),`ceil(nrows/64)` 个 u64。选 u64 而非 byte:与 [01](./01-value-object-model.md) 的 `words []uint64` 字视图一致,且 64-bit 批量检查(`if word == ^uint64(0)` 整段全 present 快判)更快。
- **bit i 对应行 i**:`word = bitmap[i>>6]`,`bit = (word >> (i&63)) & 1`。小端位序(bit 0 是最低位)。
- **1 = present(有值)**,**0 = null(读出为 Lua `nil`,[01](./01-value-object-model.md) §3.3 `value.Nil`)**。
- **flags.bit0=0(无 bitmap)** ⇒ 该列**全 present**(无 null),读出永不为 nil。这是常见情形(密集列)的优化:省掉 bitmap 存储与检查。

读出某行(§4 通用流程):

```
读 arena 第 c 列第 i 行:
  desc := ColumnDesc[c]
  if desc.flags.hasBitmap && (bitmap[i>>6]>>(i&63))&1 == 0:
      return value.Nil                         // null 槽 → Lua nil
  switch desc.tag:
    case ColFloat64: return value.NumberValue(data_f64[i])          // §3.3.1 + canonicalize
    case ColInt64:
        v := data_i64[i]
        if v > 1<<53 || v < -(1<<53) { return errInt64OutOfExactRange(col, i) } // §3.3.2 超界报错
        return value.NumberValue(float64(v))
    case ColBool:    return value.BoolValue((data[i>>3]>>(i&7))&1==1) // §3.3.3
    case ColString:  return internStrSlot(slot[i])                  // §3.3.4 拷贝+intern(口径)
```

> **null 与值的正交性**(承 §3.3.3 的 ColBool 提醒,推广到所有列):presence bitmap 与数据区**正交**。某行 null 时,数据区对应位置的字节**无意义**(宿主可填任意值,VM 不读它——bitmap=0 时直接返回 nil)。宿主填充时:present 行填真实值 + bitmap 置 1;null 行 bitmap 置 0(数据区可不填/填 0)。

### 3.5 P1 的 Go 承载:`wangshu.Arena` 类型

上面 §3.1-§3.4 是**逻辑/二进制布局 spec**(P3 linear memory 形态)。P1 用 `wangshu.Arena` 的 Go slice 字段**等价承载**这个逻辑结构(不实际序列化成上面那个连续字节块——P1 直接用 Go slice,P3 才序列化进 linear memory)。公共类型:

```go
package wangshu

// Arena 是宿主侧的列数据容器(公共类型)。宿主用类型化方法构造列;VM 零拷贝读其元素(§4)。
// P1:用 Go slice 承载 §3.1-§3.4 的逻辑布局(不序列化);P3:序列化进 Wasm linear memory(§11)。
type Arena struct {
    nrows uint32
    cols  []column // 每列一个 column(内部类型)
    names map[string]int // 列名 → 列下标(挂载时建,§5.1)
    // 共享字符串字节池(ColString 列的字符串区,§3.3.4)
    strBytes []byte         // 连续字节池
    // 列数据用类型化 slice(对应 §3.3 的数据区):
    // 每个 column 持其类型 + 指向下面某个 slice 的范围 + presence bitmap
}

// column(不导出):一列的承载。
type column struct {
    tag      colType         // §3.2 元素类型 tag
    name     string
    presence []uint64        // presence bitmap(nil = 全 present,§3.4);ceil(nrows/64) 个 u64
    f64      []float64       // ColFloat64 数据(§3.3.1);其余 tag 时为 nil
    i64      []int64         // ColInt64 数据(§3.3.2)
    boolBits []uint64        // ColBool 位打包数据(§3.3.3);ceil(nrows/64) 个 u64(P1 用 u64 打包)
    strSlots []strSlot       // ColString 的 StrSlot 数组(§3.3.4),off/len 进 strBytes
}
type strSlot struct{ off, len uint32 }

// —— 宿主构造列的公共 API(类型安全,宿主不碰二进制)——

// NewArena 创建一个 nrows 行的 arena;所有列必须等长 = nrows(§3.1 等长约定)。
func NewArena(nrows int) *Arena

// AddFloatColumn 加一列 float64。vals 长度必须 = nrows。
// present == nil 表示全 present(无 null);否则 present[i]==false ⇒ 第 i 行为 null(读出 nil)。
func (a *Arena) AddFloatColumn(name string, vals []float64, present []bool) error

// AddInt64Column 加一列 int64(读出转 double,注意 §3.3.2 精度边界 |v|>2^53 丢精度)。
func (a *Arena) AddInt64Column(name string, vals []int64, present []bool) error

// AddBoolColumn 加一列 bool。
func (a *Arena) AddBoolColumn(name string, vals []bool, present []bool) error

// AddStringColumn 加一列 string。vals 的字节被拷进 arena 共享字节池(§3.3.4);
// present[i]==false ⇒ 第 i 行 null。相同字符串自动去重(共享字节池,§3.3.4 方案 α)。
func (a *Arena) AddStringColumn(name string, vals []string, present []bool) error

// Rows 返回行数。
func (a *Arena) Rows() int
```

> 设计:宿主用 `AddFloatColumn` 等**类型化方法**构造列,把一个 Go `[]float64` 整体交给 arena(零拷贝引用 slice,不复制——`column.f64 = vals`)。`present []bool` 由 arena 内部打包成 §3.4 的 u64 bitmap(`AddFloatColumn` 里 `packPresence(present)`)。**宿主侧零二进制操作**,纯 Go slice;二进制布局(§3.1-§3.4)是 ABI 契约 + P3 形态,P1 宿主看不到它。

---

## 4. 零拷贝读机制:VM 读列即时 NaN-box

**呼应 [value-representation](../../../llmdoc/architecture/value-representation.md) / [embedding-contract](../../../llmdoc/reference/embedding-contract.md)「宿主直接写、VM 零拷贝读」。** VM 读 arena 列的某个元素时,**不复制整列**,而是把那一个底层 `float64`/`int64`/`bool`/字符串就地装箱成一个 Value(放进 thread 值栈某槽)。

### 4.1 数值/布尔列:真零拷贝即时装箱

```go
// internal/abi —— VM 读 arena 列元素,即时 NaN-box(零拷贝:不搬整列,只装箱一个元素)
//
// a: 宿主侧 *Arena 的 VM 视图;c: 列下标;i: 行下标。
func (v *ArenaView) ReadCell(c, i int) value.Value {
    col := &v.cols[c]
    // 1. presence 检查(§3.4):null → nil
    if col.presence != nil && (col.presence[i>>6]>>(uint(i)&63))&1 == 0 {
        return value.Nil
    }
    // 2. 按类型即时装箱(零拷贝:读一个元素,无整列复制)
    switch col.tag {
    case colFloat64:
        return value.NumberValue(col.f64[i])         // 含 canonicalize(01 §3.4),防外部负 NaN
    case colInt64:
        return value.NumberValue(float64(col.i64[i])) // 转 double(§3.3.2 精度边界)
    case colBool:
        return value.BoolValue((col.boolBits[i>>6]>>(uint(i)&63))&1 == 1)
    case colString:
        return v.internStr(c, i)                       // §4.2:拷贝+intern(非零拷贝)
    }
}
```

- **float64**:`value.NumberValue(col.f64[i])` 内含 `f != f` canonicalize([01](./01-value-object-model.md) §3.4)——**这是 arena float 列读入的强制规范化入口**([01](./01-value-object-model.md) §3.4 点名)。宿主可能在 `[]float64` 里写了负 NaN(`math.Float64frombits(0xFFF8...)`),不规范会被 VM 误判成 boxed tag(破坏 [01](./01-value-object-model.md) §3.2 判定),`NumberValue` 兜底规范成 `0x7FF8…`。
- **零拷贝的精确含义**:`col.f64[i]` 是宿主 `[]float64` 的一个元素的**就地读**(一次内存 load),装箱是一次寄存器位操作([01](./01-value-object-model.md) §3.5 `Float64bits`)。**整列 `col.f64` 从不被复制进 VM arena**——它一直是宿主的 Go slice,VM 只读它的元素。这就是「VM 零拷贝读」。

### 4.2 字符串列:拷贝 + intern(非零拷贝,口径见 §3.3.4)

```go
// internStr 把 arena 字符串区第 (c,i) 个字符串拷进 VM arena 并 intern,返回 string Value。
// 非零拷贝(Lua intern 要求,§3.3.4 口径);配 intern 缓存减少重复。
func (v *ArenaView) internStr(c, i int) value.Value {
    col := &v.cols[c]
    slot := col.strSlots[i]
    // intern 缓存:同一 slot 反复读(循环内多次访问同一行)只 intern 一次
    if gcref, ok := v.internCache[strCacheKey{c, i}]; ok {
        return value.MakeGC(value.TagString, gcref)
    }
    bytes := v.strBytes[slot.off : slot.off+slot.len]   // 宿主字节池切片(不拥有)
    gcref := v.vm.StringTable().Intern(bytes)            // 拷进 VM arena + intern(06 §9.1)
    v.internCache[strCacheKey{c, i}] = gcref
    return value.MakeGC(value.TagString, gcref)
}
```

string 列读出**有拷贝**(`Intern` 内 `allocString` 把字节拷进 VM arena 的 String 对象,[06](./06-memory-gc.md) §9.1),配 `internCache` 避免同 slot 重复 intern。这是 §3.3.4 定稿的「字符串零拷贝口径:拷贝进 arena intern + intern 缓存」。

### 4.3 args 与 arena 的关系(任务点名:精确语义)

`Program.Call(state, arena, args)` 里 **`args` 与 `arena` 是两个正交的输入通道**:

| 通道 | 是什么 | 脚本如何拿到 | 典型用途 |
|---|---|---|---|
| **`args ...Value`** | **标量调用参数**(少量、标量,传给主 chunk) | 主 chunk 的固定参数 / vararg `...`(§4.3.1) | 配置标量:阈值、模式开关、批次 ID、规则参数 |
| **`arena *Arena`** | **批量列数据**(大量、列存,nrows 行 × ncols 列) | 经暴露的 arena 句柄(userdata),`arena.colname[i]` 迭代(§5) | 待处理的整批数据:1M 行价格/数量/状态 |

**精确语义:脚本同时拿到 args 和 arena 列的方式**:

```lua
-- 主 chunk 源码(宿主 Compile 的 script):
-- args = {threshold=100.0}(标量)经主 chunk 参数拿到;
-- arena(批量列)经全局 `arena` 句柄拿到。
local threshold = ...          -- 主 chunk 的 vararg = Call 的 args(§4.3.1)
local n = arena:rows()         -- arena 句柄(全局注入,§5.2)
local cnt = 0
for i = 1, n do                -- 列内核形状:循环在 Lua 内,整批在 VM 内迭代
  if arena.price[i] ~= nil and arena.price[i] > threshold then  -- 读 arena 列(§4.1 零拷贝)+ 用 args 标量
    cnt = cnt + 1
  end
end
return cnt                     -- 返回值经 results 拷出(§4.4)
```

- **args 走主 chunk 参数/vararg**:`Call(state, arena, args...)` 的 `args` 被推到 mainThread 值栈,作为主 chunk closure 的实参。主 chunk 编译时(`Compile`)是 vararg 函数(顶层 chunk 在 Lua 5.1 即 vararg,[02](./02-bytecode-isa.md) §6 / [04](./04-frontend-parser-codegen.md)),`...` 即 args。脚本用 `local a, b = ...` 或 `select('#', ...)` 取(§4.3.1)。
- **arena 走全局句柄**:`Call` 步骤 5(§1.5)把 arena 句柄注入为 globals 表的一个全局名(默认 `arena`,可经 Options 配),脚本用 `arena.colname[i]` / `arena:rows()` 访问(§5.2)。arena 句柄是一个 full userdata([01](./01-value-object-model.md) §5.5),其 `__index` 元方法实现列访问(§5.2)。
- **为什么分两个通道**:args 是「少量标量配置」(每次 Call 可能不同的参数),arena 是「大批列数据」(本次 Call 要处理的数据)。分开让「配置」和「数据」各走最优路径:args 直接进栈(几个标量,零开销);arena 经句柄零拷贝读(大批量,不进栈不复制)。**这正是 roadmap §8「`Program.Call(arena, args)`」签名的语义落地**——arena 与 args 并列是两个参数,本文定死它们一个是批量列、一个是标量参数。

#### 4.3.1 args 推栈与主 chunk vararg 衔接

```
Call 步骤 4(§1.5):把 args 推栈。
  主 chunk 是 vararg 函数(IsVararg=true,01 §5.7)。
  args = [a0, a1, ..., a_{k-1}](公共 Value;若含 string/table,先 intern/构造进 arena)。
  推到 mainThread 值栈作为实参 → adjustVarargs(05 §7.1 / §8.5):
    NumParams=0(顶层 chunk 无固定形参)⇒ 全部 k 个 args 成为 vararg,存 base 之下负区(02 §6)。
  脚本 `...` 即这 k 个值;`select('#',...)` = k。
```

公共 `Value` 含 string/table 时,推栈前要转成 VM arena 内的对象(string→intern,table→在 arena 构造)。标量(number/bool/nil/lightuserdata)直接是 NaN-box Value,零转换。**args 应以标量为主**(roadmap §8 意图);若宿主在 args 里传大 table,那是反模式(应改用 arena 列)。

### 4.4 返回值拷出:results 脱离 VM arena

`Call` 返回的 `[]Value` 必须**脱离 VM arena**(因为 `Call` 返回后 VM 可继续被复用/GC,results 不能持悬垂 GCRef):

```
Call 步骤 7(正常返回):
  主 chunk RETURN 的返回值在 mainThread 栈顶 [base, base+nret)。
  逐个拷出为公共 Value:
    - number/bool/nil/lightuserdata:直接是 NaN-box Value,值语义,直接放进 results(无 GCRef,安全)。
    - string:把 arena String 的字节拷出成 Go string,包成公共 Value(StringValue 持 Go string,§4.5)。
    - table/function/thread/userdata:**不能直接拷出**(它们是 arena 内对象图)——
      P1 定稿:① 返回值应以标量/string 为主(列内核典型:返回计数、聚合结果、布尔判定);
              ② 若必须返回 table,P1 提供「深拷出成 Go map/slice」或「保留为不透明 handle」(§4.5,记缺口)。
```

> **public Value 的双重形态**(§4.5):公共 `wangshu.Value` 对标量是 NaN-box u64(值语义,跨 Call 安全);对 string 持 Go string(脱离 arena);对 table/function 等持「handle + 源 State 引用」(只在源 State 存活期有效)。详 §4.5。

### 4.5 公共 `Value` 类型

公共 `Value` 不能直接等于 `internal/value.Value`(那是 arena 内的 NaN-box,GCRef 出了 Call 就可能悬垂)。公共 `Value` 是一个**值语义包装**:

```go
package wangshu

// Value 是公共 API 的 Lua 值表示。对标量是值语义(安全跨 Call);
// 对引用类型(string/table)持脱离 arena 的副本或 handle。
type Value struct {
    // 不导出:kind + 标量 union + (string 时)Go string + (引用时)handle。
}

// 构造(宿主传 args 用):
func Nil() Value
func Bool(b bool) Value
func Number(f float64) Value          // 过 canonicalize(01 §3.4)
func String(s string) Value           // 持 Go string,Call 时 intern 进目标 arena
func LightUserdata(handle uint64) Value // §6 句柄表索引或固定 uintptr

// 提取(读 results 用):
func (v Value) IsNil() bool
func (v Value) AsNumber() (float64, bool)
func (v Value) AsString() (string, bool)   // string 返回 Go string 副本
func (v Value) AsBool() (bool, bool)
```

- **标量 Value**(nil/number/bool/lightuserdata):值语义,无 GCRef,跨 Call/跨 goroutine 安全(可缓存、可传)。
- **string Value**:持 **Go string**(脱离 arena)。作为 args 传入时,`Call` 把它 intern 进目标 State 的 arena([06](./06-memory-gc.md) §9.1);作为 results 拷出时,从 arena String 拷成 Go string。
- **table/function/thread/userdata Value**:P1 定稿**不在公共 Value 直接支持往返**(它们是 arena 对象图,深拷成本高且语义复杂)。列内核负载的 args/results 以标量+string 为主(传配置、收聚合)。若宿主确需往返 table,§4.4 的「深拷成 Go map/slice」或「不透明 handle」留作扩展(记 §12 缺口)。

---

## 5. arena 在脚本内的暴露形式(任务点名:精确暴露)

**关键决策**(任务点名「arena 句柄在脚本内的精确暴露形式」):arena 作为**一个 full userdata**([01](./01-value-object-model.md) §5.5)注入脚本,其 metatable 的 `__index`/`__len` 实现列访问。

### 5.1 挂载:把宿主 Arena 映射进 VM 可读视图

```
Call 步骤 2(§1.5):挂载 arena。
  1. 建 ArenaView(internal/abi):持有宿主 *Arena 的列 slice 引用(零拷贝,不复制数据)
     + names map(列名→下标,§3.2.1)+ internCache(§4.2)。
  2. 在 VM arena 分配一个 full userdata(01 §5.5),其 payload = 指向 ArenaView 的句柄
     (经句柄表,§6——userdata payload 不直存 Go 指针,01 §3.5 / §5.5)。
  3. 给该 userdata 挂 arena metatable(VM 内建,带 __index/__len/__newindex,§5.2)。
  4. 把该 userdata 存入 globals 表的 `arena` 全局名(或 Options 配的名)。
```

ArenaView 持宿主列 slice 的**引用**(`view.cols[c].f64 = hostArena.cols[c].f64`),零拷贝——VM 读列就是读宿主 slice 的元素(§4.1)。

### 5.2 脚本访问:`arena.colname[i]` 与迭代

arena userdata 的 metatable 实现(host functions,[05](./05-interpreter-loop.md) §7.6 / [07](./07-metatables-metamethods.md)):

```lua
arena.price          -- __index(arena, "price") → 返回一个「列代理」(见下)
arena.price[i]       -- 列代理的 __index(col, i) → ReadCell(price 列, i)(§4.1 零拷贝)
arena:rows()         -- __index(arena, "rows") → host 方法,返回 nrows
#arena               -- __len → nrows(可选,等价 arena:rows())
```

**两级访问设计**:

- `arena.colname` 触发 arena userdata 的 `__index(arena, name)`:查 `names` map 得列下标 c,返回一个**列代理对象**(轻量 userdata,payload = `{viewHandle, colIndex c}`)。
- `colproxy[i]` 触发列代理的 `__index(col, i)`:调 `view.ReadCell(c, i)`(§4.1)即时装箱返回。
- **优化:列代理缓存**——`arena.price` 反复取(循环外取一次更好)应返回同一个列代理(缓存 name→proxy),避免每次 `arena.price` 都建新代理。**脚本最佳实践**:`local price = arena.price; for i=1,n do ... price[i] ... end`(列代理取一次,循环内只 `[i]` 索引)——这把每行开销压到「一次 `__index(col,i)` → ReadCell」。

> **为什么用 userdata 而非全局表**(讨论:任务点名「userdata 或全局表」):
> - **方案 α(选定):arena = full userdata + metatable `__index`**。优点:① 列访问 `arena.col[i]` 走 `__index` 元方法,可**惰性即时装箱**(§4.1)——不预先把整列变成 Lua table(那会复制整列成 arena Table,违背零拷贝);② userdata 不可被脚本意外改结构(只读,§5.3);③ 与 Lua 习惯一致(C 扩展常把外部数据包成 userdata)。
> - **方案 β:arena = 一个全局 Lua table,每列是一个 Lua table**。缺点:要把每列**预先物化**成 Lua table(`{v1,v2,...,vn}`)——这是**复制整列进 VM arena Table**,正是零拷贝要避免的(1M 行 float 列变成 1M 槽的 arena Table,大量分配 + GC 压力)。否决。
> **定稿 α**:arena userdata + `__index` 惰性装箱,**零拷贝读的脚本侧落地**。列代理([col][i] 两级)让「读第 i 行某列」= 一次元方法 + 一次 ReadCell,无整列物化。

### 5.3 只读约定(P1)

P1 的 arena 列对脚本**只读**(`ColumnDesc.flags.bit1=1`,§3.2)。脚本 `arena.price[i] = x` 触发列代理的 `__newindex` → 报错 `"arena column is read-only"`。理由:① arena 是「宿主喂给脚本的输入数据」,语义上是只读快照;② 写回需要「把 VM Value 转回宿主列类型 + 写宿主 buffer」的反向通道,P1 不做(记 §12 缺口:「可写 arena 列(脚本产出列回写宿主)」留 P2+)。脚本的产出经**返回值**(§4.4)或**单独的输出 arena**(未来)交还宿主,不就地改输入 arena。

---

## 6. lightuserdata 与句柄表(任务点名:[01](./01-value-object-model.md) §3.5 点名)

[01](./01-value-object-model.md) §3.5 点名本文:lightuserdata payload 是 48-bit 不透明句柄,**不得直存 Go 堆指针**(Go GC 看不见会悬垂,违反 [01](./01-value-object-model.md) §3.5 / roadmap §2 写屏障税 / roadmap §6「不让 Lua 直访 Go 堆」)。宿主传 Go 对象有两条路。

### 6.1 两条路:Pinner 固定 vs 句柄表索引

| 路 | 机制 | lightuserdata payload | 优点 | 代价/风险 |
|---|---|---|---|---|
| **① `runtime.Pinner` 固定 + 传 uintptr** | 宿主 `pinner.Pin(obj)` 固定 Go 对象(防 GC 移动/回收),把 `uintptr(unsafe.Pointer(obj))` 存进 lightuserdata payload | 48-bit 地址(amd64/arm64 用户态地址在 48-bit 内) | 直接传地址,VM 取出 `uintptr` 还原指针(宿主侧 unsafe) | ① Pinner 生命周期管理(何时 Unpin)易错;② 5 级页表(57-bit 虚拟地址)罕见高地址超 48-bit,直存截断失效([01](./01-value-object-model.md) §3.5);③ `unsafe` 重 |
| **② 句柄表索引(推荐默认)** | State 持一张 `handles []any`,宿主 `state.PinHandle(obj)` 返回索引 `idx`,把 `idx` 存进 lightuserdata payload | 48-bit 整数索引(进 `handles`) | 无 unsafe;无 Pinner 生命周期细节(索引一直有效直到显式释放);无地址位数问题 | 多一次间接(payload→idx→`handles[idx]`);`handles` 占内存(显式释放或随 State 销毁) |

**定稿:默认走 ② 句柄表索引**(无 unsafe、无地址位数坑、生命周期清晰);① Pinner 作为「宿主已有 `*T` 且想零间接、且确信地址 48-bit 内」的高级选项(`state.PinPointer`)。这呼应 [01](./01-value-object-model.md) §3.5「以句柄表兜底」(5 级页表高地址直存不支持时,句柄表是兜底)。

### 6.2 句柄表 API

```go
package wangshu

// PinHandle 把一个任意 Go 值登记进 State 句柄表,返回一个 lightuserdata Value(payload = 句柄索引)。
// 句柄一直有效直到 UnpinHandle 或 State 销毁。脚本里它是 userdata(type()=="userdata",01 §3.3)。
func (s *State) PinHandle(obj any) Value     // 返回 LightUserdata Value

// FromHandle 在 HostFn 内还原:把 lightuserdata Value 还原为登记的 Go 值。
func (s *State) FromHandle(v Value) (any, bool)

// UnpinHandle 释放句柄(置 handles[idx]=nil,可被复用或留空)。不释放会随 State 一直持有。
func (s *State) UnpinHandle(v Value)
```

实现:`State.handles []any`(Go 堆,**是 GC 根**——对其中可能持有的 arena 引用?——不,句柄表存的是**Go 对象**,不是 arena GCRef;它由 Go GC 管,VM 的 mark-sweep 不扫它)。lightuserdata payload = `idx`(48-bit,`payloadMask`,[01](./01-value-object-model.md) §3.5);`handles[idx]` 是 Go 对象。脚本拿到的是不透明 userdata,只能传回 HostFn 解包(`FromHandle`)——**脚本不能直接解引用 Go 对象**(roadmap §6 非目标:不让 Lua 直访 Go 堆;句柄表是「受控间接」,Go 对象只在 HostFn 内被 Go 代码访问)。

### 6.3 full userdata vs lightuserdata 的选择([01](./01-value-object-model.md) §5.5)

| | lightuserdata([01](./01-value-object-model.md) §3.5) | full userdata([01](./01-value-object-model.md) §5.5) |
|---|---|---|
| 存什么 | 48-bit 句柄/索引(不可回收,无 metatable per-instance) | arena 内字节块 payload(可回收、可 GC、可设 per-instance metatable + `__gc`) |
| 适合 | 「指向一个 Go 对象」的轻量句柄(句柄表索引)——本节 §6.2 | 「VM 管理生命周期的不透明数据」(如 arena 句柄 §5.1、宿主分配的需 `__gc` 终结的资源) |
| GC | 不参与(标量,[01](./01-value-object-model.md) §3.3) | 参与 mark-sweep([06](./06-memory-gc.md));`__gc` 终结器([06](./06-memory-gc.md) §10) |

- **句柄表索引用 lightuserdata**(§6.2):轻量、不需 per-instance metatable、不需 VM 回收(Go 对象由句柄表+Go GC 管)。
- **arena 句柄(§5.1)、需 `__gc` 的宿主资源用 full userdata**:arena 句柄要挂 metatable(`__index` 列访问);需 `__gc` 的资源(宿主打开的文件/连接)要 VM 在回收时调终结器([06](./06-memory-gc.md) §10)。

---

## 7. per-item 简易 API(对标 gopher-lua,明确性能档位)

**任务点名:对标 gopher-lua 易用性,明确标性能档位。** 不强制 arena 的 per-item 栈式 API 照常提供([embedding-contract](../../../llmdoc/reference/embedding-contract.md)「不强制 arena 的简易 API」),但**文档明确标注它落在被边界成本主导的那一档**([design-premises](../../../llmdoc/must/design-premises.md) 前提一)。

### 7.1 栈式 API 草图(类 `lua_State` 栈机)

```go
package wangshu

// —— per-item 栈式 API:类似 lua_State 的虚拟栈机,对标 gopher-lua 易用性 ——
// 性能档位:走 per-item 跨界形态,落在被边界成本主导的那一档(design-premises 前提一)。
//          适合「偶尔调一下脚本」,不适合「循环里每 item 调一次」(那会被边界成本吃光,roadmap §1)。

// Push 系列:把值压入 State 的 per-item 操作栈(独立于 VM 值栈的公共栈视图)。
func (s *State) PushNil()
func (s *State) PushNumber(f float64)   // 过 canonicalize(01 §3.4)
func (s *State) PushString(str string)  // intern 进 arena
func (s *State) PushBool(b bool)
func (s *State) PushValue(v Value)

// Pop / To 系列:读栈。
func (s *State) ToNumber(idx int) (float64, bool)
func (s *State) ToString(idx int) (string, bool)
func (s *State) ToBool(idx int) bool
func (s *State) Pop(n int)
func (s *State) Top() int

// CallFn:取一个全局函数 / 栈上函数,以栈顶 nargs 个值为参数调用,nret 个返回值压栈。
//   每次 CallFn = 一次 Go→VM 跨界(05 §7.3 的 host→Lua 重入边界)。
func (s *State) GetGlobalFn(name string) error   // 把全局函数 name 压栈
func (s *State) CallFn(nargs, nret int) error    // 调用 + 错误转 Go error(§2)

// 便捷:直接按名调全局函数(Push args → CallFn → 读 results)。
func (s *State) CallGlobal(name string, args ...Value) ([]Value, error)
```

用法(对标 gopher-lua 的 `L.CallByParam` 风格):

```go
state := wangshu.NewState(wangshu.Options{})  // 零值 Libs = LibsDefault(对齐 gopher-lua 面,10 §12.1)
prog, _ := wangshu.Compile([]byte(`function score(x) return x*x+1 end`), "rules")
prog.Call(state, nil)                       // 先跑顶层定义 score 全局函数
// per-item 调用(每次跨界一次):
for _, x := range items {                   // ⚠ 这个循环在 Go 里 = per-item 跨界
    r, _ := state.CallGlobal("score", wangshu.Number(x))  // 每 item 一次 Go→VM 边界
    use(r[0])
}
```

### 7.2 性能档位的明确标注(契约)

> **per-item API 性能契约**(写进公共 godoc,宿主必读):上面那个 `for _, x := range items { state.CallGlobal("score", ...) }` 循环**每个 item 跨一次 Go→VM 边界**。由 [design-premises](../../../llmdoc/must/design-premises.md) 前提二,**边界跨越是几十~百 ns 的固定成本**;由前提一 / roadmap §1 的两个校准测量,**per-item 形态下边界成本主导,VM 本体再快也被钉死**(真 LuaJIT 只比 luajc 快 6% 就是这个效应)。
>
> **结论**:per-item API **方便但慢**(落在被边界成本主导档)。它适合:① 脚本调用频率低(每秒几千次以内,边界成本可忽略);② 原型/测试/REPL;③ gopher-lua drop-in 迁移期(§9)先跑通再优化。**高频热路径应改用 arena 批量轨**(§3-§5):把 Go 里的 `for item` 循环搬进 Lua(`for i=1,arena:rows()`),一次 `Program.Call(state, arena)` 处理整批——边界成本摊薄到每批一次。

**两轨对比表**(宿主选型依据):

| | arena 批量轨(§3-§5) | per-item 栈式轨(§7) |
|---|---|---|
| 跨界次数 | **每批一次**(`Program.Call` 一次) | **每 item 一次**(每 `CallGlobal` 一次) |
| 数据传递 | 列存零拷贝读(§4) | 每 item 推栈(标量装箱) |
| 循环在哪 | **Lua 内**(列内核形状) | Go 内(反列内核) |
| 性能档 | 兑现 roadmap 收益(边界摊薄) | 边界成本主导档([design-premises](../../../llmdoc/must/design-premises.md) 前提一) |
| 易用性 | 需宿主组织列数据 | 随手 Push/Call(gopher-lua 风格) |
| 推荐 | **高频/大批量(主推)** | 低频/原型/drop-in 迁移 |

> 两轨共享同一 VM([05](./05-interpreter-loop.md))、同一值世界([01](./01-value-object-model.md))。区别**纯在跨界粒度**——这正是 [design-premises](../../../llmdoc/must/design-premises.md) 前提一的工程兑现:接口不禁止 per-item(易用性),但用文档把性能后果讲清,**天然引导**宿主走列内核(arena 轨)。

---

## 8. Program / State 并发语义

**任务点名:Program 可共享、State 每 goroutine 一个。** 给线程安全约定。

### 8.1 并发约定表

| 类型 | 可变? | 并发 | 约定 |
|---|---|---|---|
| **`*Program`** | **不可变**(编译后固定) | **可多 goroutine 并发只读共享** | 一次 `Compile`,多 goroutine 各自 `Call`(各用自己的 State)。Program 持 Proto(Go 堆,代码不进 arena、不 GC,[01](./01-value-object-model.md) §1 / [06](./06-memory-gc.md) §0 铁律 2),只读 → 天然并发安全 |
| **`*State`** | **可变**(arena/栈/globals/句柄表/GC) | **每 goroutine 一个,不可并发** | 一个 State 含一个 VM 的全部可变状态(arena/mainThread 栈/globals/intern 表/collector)。**绝不可两个 goroutine 同时 `Call` 同一个 State**(数据竞争:arena bump、GC、栈)。每 goroutine `NewState` 一个独立 State |
| **`*Arena`(输入)** | 构造期可变,Call 期只读 | 构造完不再改 ⇒ 可被多个 State 的 Call 只读共享 | 宿主构造完 arena 后,可把同一个 `*Arena` 传给多个 goroutine 各自 State 的 `Call`(VM 只读列,§4;string 列 intern 进各自 arena,§4.2 互不干扰)。**Call 期间不可改 arena** |
| **`wangshu.Value`** | 标量值语义 / string 持 Go string | 标量+string 可并发只读 | 标量/string Value 跨 goroutine 安全(无 GCRef);table/function Value 不支持往返(§4.5) |

### 8.2 典型并发模式:一次编译,每 worker 一个 State

```go
prog, _ := wangshu.Compile(src, "chunk")   // 编译一次,Program 不可变可共享
arena := buildArena(batch)                 // 构造一次输入 arena(只读)
var wg sync.WaitGroup
for w := 0; w < runtime.NumCPU(); w++ {
    wg.Add(1)
    go func(shard *wangshu.Arena) {
        defer wg.Done()
        state := wangshu.NewState(opts)    // 每 goroutine 独立 State(独立 arena/VM)
        results, _ := prog.Call(state, shard)  // 各自跑,无共享可变状态
        collect(results)
    }(shardOf(arena, w))
}
wg.Wait()
```

**关键不变式**:跨 goroutine 共享的只有 **Program(不可变代码)** 与 **输入 Arena(只读数据)**;每 goroutine 的**可变世界(State)是私有的**。这与 [01](./01-value-object-model.md) §1「代码住 Go 堆经整数 ID 引用、值世界住 arena」直接对应——代码可共享(只读),值世界每 State 私有(可变)。**这是把「值表示第 1 天承诺」延伸到并发维度的结论。**

> State 不可并发是刻意的:VM 的 arena bump 分配、STW GC([06](./06-memory-gc.md) §7.3「单 goroutine 解释器下 STW 天然无需停顿协调」)、值栈都假设单线程。让 State 并发要么加锁(杀性能)要么重写 GC 为并发(P1 不做,[06](./06-memory-gc.md) §7.3)。**每 goroutine 一个 State 是纯 Go 嵌入的标准模式**(gopher-lua 的 `*LState` 同样每 goroutine 一个),迁移直观(§9)。

---

## 9. drop-in gopher-lua 定位

**任务点名:P1 解释器作为 gopher-lua drop-in 候选,给迁移说明(概念层)。** roadmap §8 / [embedding-contract](../../../llmdoc/reference/embedding-contract.md):「P1 解释器即可作为 gopher-lua 的 drop-in 候选」;首个目标宿主的 Go 运行时现用 gopher-lua(roadmap §8)。

### 9.1 形状对标

per-item 栈式 API(§7)刻意对标 gopher-lua 的 `*LState` 栈机,降低迁移摩擦:

| gopher-lua | 望舒 per-item 轨(§7) | 备注 |
|---|---|---|
| `lua.NewState()` | `wangshu.NewState(opts)` | 每 goroutine 一个(§8,两者一致) |
| `L.DoString(src)` / `L.DoFile` | `wangshu.Compile` + `prog.Call(state, nil)` | 望舒分离编译/执行(Compile 一次可多 Call) |
| `L.Push*` / `L.To*` / `L.Get/SetTop` | `state.Push*` / `state.To*` / `state.Top/Pop`(§7.1) | 栈机形状一致 |
| `L.CallByParam` / `L.PCall` | `state.CallGlobal` / `state.CallFn`(§7.1)+ Go error(§2) | 错误转 Go error |
| `L.SetGlobal` / `L.GetGlobal` | `state.SetGlobal` / `state.GetGlobalFn`(§7.1) | — |
| `L.Register(name, fn)` / `LGFunction` | `state.Register(name, HostFn)`(§10) | HostFn 签名见 [05](./05-interpreter-loop.md) §7.6 |
| `*lua.LTable` 操作 | 经 HostFn 内 VM table API(§10) | 表操作走 VM 内 API |

### 9.2 迁移路径(概念层)

1. **第一步:per-item 直迁**——把 gopher-lua 的 `*LState` 用法机械替换成望舒 per-item 轨(§7.1)。功能等价,**性能此时与 gopher-lua 同档或略优**(NaN-boxing 去装箱让单次调用快,但 per-item 边界成本仍在,§7.2)。这一步验证「行为 drop-in 兼容」(差分:同脚本同输出,[12](./12-testing-difftest.md))。
2. **第二步:热路径切 arena 轨**——把 profiling 出的高频「Go for 循环里调脚本」热点,改写成「循环进 Lua + `Program.Call(state, arena)`」(§3-§5)。**这一步才兑现 roadmap 收益**(边界摊薄,[design-premises](../../../llmdoc/must/design-premises.md) 前提一)——这是 drop-in 之后的**性能升级**,不是迁移必需。
3. **行为兼容保证**:望舒语义严格 Lua 5.1(roadmap §6),与 gopher-lua 的 Lua 5.1 实现差分逐字节一致([12](./12-testing-difftest.md) 把 gopher-lua 当差分基准,roadmap §7 prior art「gopher-lua:P1 的差分基准」)。

> drop-in 的精确含义:**API 形状兼容(per-item 轨)+ 语义兼容(Lua 5.1 差分一致)**,让宿主能「换库不改脚本、少改 Go 胶水」。性能升级(arena 轨)是可选的第二阶段。**P1 即可 drop-in**(per-item 轨 + 解释器完整),无需等编译层(P3+)。

---

## 10. host function 注册

**任务点名:宿主注册 Go 函数为 Lua 可调用。** `state.Register(name, HostFn)`(HostFn 签名见 [05](./05-interpreter-loop.md) §7.6);host fn 进 Go 堆注册表,整数 ID 引用([01](./01-value-object-model.md) §1);shadow stack 纪律([06](./06-memory-gc.md) §6.3)。

### 10.1 注册 API

```go
package wangshu

// HostFn 是宿主 Go 函数的签名(与 05 §7.6 一致):从 thread 取参数,push 返回值,返回返回值个数。
// 注意:这是 internal/crescent.HostFn 的公共别名;ctx 提供 arena / shadow stack / 参数访问。
type HostFn = func(ctx *HostCtx) (nret int)

// HostCtx 是 HostFn 的执行上下文(公共包装 05 §7.6 的 vm/th)。
type HostCtx struct { /* 不导出:*VM + *Thread */ }

// 参数/返回值访问(host 在栈上读参写返回,05 §7.6):
func (c *HostCtx) NArgs() int
func (c *HostCtx) ArgNumber(i int) (float64, bool)
func (c *HostCtx) ArgString(i int) (string, bool)
func (c *HostCtx) PushNumber(f float64)   // 过 canonicalize(01 §3.4)
func (c *HostCtx) PushString(s string)
func (c *HostCtx) Raise(msg string)        // 抛 Lua 错误(走 05 §9 错误返回,不 Go panic)
// shadow stack(06 §6.3 纪律):host 多步分配时登记临时 arena 引用为根
func (c *HostCtx) Pin(v Value) int         // = shadow stack Push(06 §6.2)
func (c *HostCtx) Unpin(handle int)        // = shadow stack Pop

// Register 把 fn 注册为全局名 name 的 Lua 可调用函数。
//   fn 进 State 的 host 函数注册表(Go 堆,01 §1),分配整数 HostFnID;
//   在 globals 表建一个 Host Closure(01 §5.3,hostFnID 引用)绑定到 name。
func (s *State) Register(name string, fn HostFn)

// RegisterModule 注册一组函数为一个 table(如自定义库)。
func (s *State) RegisterModule(name string, fns map[string]HostFn)
```

### 10.2 host fn 进 Go 堆注册表(整数 ID 引用)

承 [01](./01-value-object-model.md) §1:host function 是 Go 闭包,**无法装进 arena**(Go GC 看不见 arena 内的 Go 指针),故住 **Go 堆的 host 函数注册表**,由**整数 `HostFnID` 引用**:

```
state.Register("foo", fooFn):
  1. id := append(state.hostFns, fooFn); id = len-1   // Go 堆注册表,01 §1
  2. 在 arena 建一个 Host Closure(01 §5.3:flags bit0=1,word1 低 32 位 = hostFnID)
  3. globals["foo"] = MakeGC(TagFunction, hostClosureRef)
```

脚本调 `foo(...)` → [05](./05-interpreter-loop.md) §7.6 的 host 分支:`vm.hostFns[hostFnID](ctx)`(Go 调用,进 Go 栈,同步执行)。**HostFnID 是整数,不是 GCRef**——它指向 Go 堆注册表,绕开「GCRef 不能指 Go 堆」的约束([01](./01-value-object-model.md) §1 / §7 不变式 4)。

### 10.3 shadow stack 纪律([06](./06-memory-gc.md) §6.3)

HostFn 在 Go 栈持有 arena 引用的窗口期,**必须 `ctx.Pin`/`defer ctx.Unpin`**([06](./06-memory-gc.md) §6.3 的纪律):host 代码新分配的中间对象(如拼出的新串、新建的表)在写回 Lua 栈/表之前,处于 Go 栈持有窗口,若此时下一次分配触发 GC,未 Pin 的中间对象会被误回收([06](./06-memory-gc.md) §5.1 R7:GC 精确栈扫描看不见 arena 引用是整数)。规则([06](./06-memory-gc.md) §6.3 简记):**「下一次可能触发 GC 的分配之前,所有已持有但未上 Lua 栈/表的 arena 引用都要在 shadow stack 上」**。

```go
func myHostFn(ctx *wangshu.HostCtx) int {
    s1 := ctx.allocString(...)         // 假想:分配一个中间串(arena)
    h := ctx.Pin(s1); defer ctx.Unpin(h)  // 登记为根(06 §6.3),防下一步分配的 GC 回收 s1
    s2 := ctx.allocString(...)         // 这步可能触发 GC;s1 已 Pin,安全
    ctx.PushString(concat(s1, s2))     // 写回栈,之后 s1/s2 经栈可达
    return 1
}
```

> **public API 对 stdlib HostFn 与宿主 HostFn 一视同仁**:stdlib([10](./10-stdlib.md))的 host functions 与宿主 `Register` 的函数走完全相同的机制(同一 HostFn 签名、同一注册表、同一 shadow stack 纪律)。这让「stdlib 也是 host function 形式提供」(roadmap §4 P1)与「宿主扩展」统一。

---

## 11. 与 P3 的前瞻:arena = Wasm linear memory

**任务点名:这块 arena = P3 Wasm linear memory,P1 定的 ABI 布局 P3 直接映射。** 给指针。

[value-representation](../../../llmdoc/architecture/value-representation.md) 主线「两层共见同一块内存」在 P3 的形态:**P1 的 arena 列布局 ABI(§3.1-§3.4)直接映射为 P3 Wasm 编译层的 linear memory 布局**。

- **P1**:arena 列数据物理住宿主 Go slice(§3.0 方案 B),VM 零拷贝读元素装箱(§4)。「共见」是逻辑的(VM 读列即时进值世界)。
- **P3**(见 [../p3-wasm-tier](../p3-wasm-tier.md)):值世界 = Wasm linear memory(roadmap §4 P3「P1 的 arena 直接映射,两层共见」)。此时 arena 列**序列化进 linear memory**——按 §3.1-§3.4 的**二进制布局**(ArenaHeader/ColumnDesc/数据区/字符串区/bitmap),Wasm 编译码直接 `i32.load`/`f64.load` 读列元素。**P1 定的字节布局就是 P3 的 linear memory 布局**,无需重新设计 ABI。

> 这是「为什么 §3 即使 P1 用 Go slice 也要定二进制布局」的根本原因(§3.0 双重身份):**ABI 布局 spec 是 P1/P3 共同契约**。P1 用 Go slice 承载它(`wangshu.Arena` 的 column 字段是 §3.1-§3.4 逻辑结构的 Go 化),P3 用 linear memory 承载它(序列化成连续字节块)。**布局不变,承载不同**——这正是 [value-representation](../../../llmdoc/architecture/value-representation.md)「同一份内存的不同视角」「上编译层是纯增量」在 arena ABI 维度的兑现:P3 不重新定义列布局,只换承载介质。

- presence bitmap 的 u64 字数组布局(§3.4)、StrSlot{off,len}(§3.3.4)在 P3 直接是 linear memory 的字节,Wasm 读它们与 P1 读 Go slice **语义等价**(同 ABI)。
- ColFloat64 的 canonicalize([01](./01-value-object-model.md) §3.4)在 P3 同样必须(Wasm 读 linear memory 的 f64 也要规范化负 NaN)——ABI 的语义约束(不止布局)P3 一并继承。

---

## 12. 不变式清单 + 文档缺口 / 待决

### 12.1 不变式清单(实现与差分须守)

1. **一次调用一次跨界**:`Program.Call` 一次 = 一次 Go→VM 边界(§1.5);脚本内对 arena 列的迭代全在 VM 内,不跨界。这是 [design-premises](../../../llmdoc/must/design-premises.md) 前提一在 API 层的兑现。
2. **arena 列数据零拷贝读(数值/布尔)**:VM 读 ColFloat64/Int64/Bool 元素 = 就地读 + 即时装箱(§4.1),整列从不复制。string 列例外(拷贝+intern,§3.3.4 口径)。
3. **ColFloat64 读出强制 canonicalize**:arena float 列读入必过 [01](./01-value-object-model.md) §3.4 NaN 规范化(§3.3.1 / §4.1),防外部负 NaN 渗入破坏 NaN-boxing tag 不变式。
4. **presence bitmap 位序定死**:bit i = 行 i,小端 u64 字数组,1=present/0=null→nil(§3.4)。
5. **arena 列脚本侧只读**(P1):脚本不可写回输入 arena 列(§5.3);产出经返回值(§4.4)交还。
6. **代码可共享、值世界每 State 私有**:`Program`(代码,Go 堆,不可变)可多 goroutine 共享;`State`(值世界,arena,可变)每 goroutine 一个,不可并发(§8)。
7. **lightuserdata 不直存 Go 指针**:经句柄表索引(§6.2,默认)或 Pinner 固定 uintptr(§6.1),绝不裸存 Go 堆指针([01](./01-value-object-model.md) §3.5)。
8. **公共 Value 脱离 arena**:`Call` 返回的 results 不持悬垂 GCRef——标量值语义、string 持 Go string(§4.4 / §4.5)。
9. **host 不 Go panic**:HostFn 出错走 `ctx.Raise`([05](./05-interpreter-loop.md) §9),顶层 recover 仅安全网(§2)。
10. **arena ABI 布局 = P3 linear memory 布局**:§3.1-§3.4 的二进制布局是 P1/P3 共同契约,P3 直接映射(§11)。

### 12.2 文档缺口 / 待决(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **ColInt64 精度处理**(§3.3.2):P1 定「转 double,精度边界 |v|>2^53 丢精度」。是否需 `ColInt64Exact` 变体(读出为不可直算的精确整数 box,或拆高低 32 位双列)待真实宿主反馈——若宿主大量用大整数 ID/纳秒时间戳且需精确,当前方案不足。**倾向:先转 double + 文档契约,按反馈再加变体。**
- **字符串零拷贝口径**(§3.3.4 / §4.2):P1 定「拷贝进 arena intern + intern 缓存」;「特殊不可变 String 视图对象」(零拷贝读 arena 字符串区但破坏 string=GCRef 不变式)留 P3+ 评估。当前 string 列是次于数值列的性能档,**长期运行下 intern 缓存的命中率与内存**无数据,需实现后压测。
- **字符串区编码 α vs β**(§3.3.4):P1 定方案 α(StrSlot{off,len}+ 共享字节池 + 去重);Arrow 标准 β(offsets[n+1]+values,4 字节/行,不共享)在 P3 linear memory 与 Arrow 互操作时是否更优,留 P3 评估。
- **arena 列数据物理位置 A vs B**(§3.0):P1 定方案 B(宿主 Go slice,零拷贝读)。P3 转 linear memory(更接近 A 的「物理同一块」)。P1/P3 过渡的具体序列化时机与零拷贝边界(linear memory 与宿主 buffer 是否还需一次拷贝进 linear memory)留 [../p3-wasm-tier](../p3-wasm-tier.md) 定。
- **arena 在脚本内暴露形式的细节**(§5.2):定「full userdata + `__index` 两级(arena.col[i])+ 列代理缓存」。列代理对象的精确生命周期、`pairs(arena)` 是否支持(遍历所有列/行)、`ipairs(arena.col)` 迭代器是否提供,待 stdlib([10](./10-stdlib.md))与元表([07](./07-metatables-metamethods.md))协同定稿。
- **可写 arena 列(脚本产出回写宿主)**(§5.3):P1 全只读。脚本产出列回写宿主 buffer 的反向通道(VM Value → 宿主列类型 + 写 buffer)留 P2+。
- **公共 Value 对 table/function 的往返**(§4.4 / §4.5):P1 不支持 table/function 作 args/results 直接往返(列内核以标量+string 往返)。「深拷成 Go map/slice」或「不透明 handle」的精确设计待真实宿主需求驱动。
- **回填 [01](./01-value-object-model.md) §5.7**(§1.3):Proto 字符串常量的 intern 归属在并发下细化为「Program 持字节原文 + 每 State 私有常量 GCRef 缓存(惰性 intern)」。需 [01](./01-value-object-model.md) §5.7 / §9 确认该并发语义(原文只说「常量是 arena GCRef」,未涉及多 State 共享 Program 的情形)。
- **变长列(列间不等长)**(§3.1):P1 要求 arena 内所有列等长 = nrows。不等长列(稀疏/嵌套)P1 不支持,真实列内核负载是规整表,等长足够;若未来需要记缺口。
- **Options 默认值定标**:`MaxCallDepth`/`MaxCCalls`/`GCPause`/初始 arena 等默认值(§1.2)需与 [05](./05-interpreter-loop.md) §7.4 / [06](./06-memory-gc.md) §8.3 的对应常量对齐,**待差分核对**(本文给的默认值是建议,以 05/06 定稿为准)。

---

相关:[01-value-object-model](./01-value-object-model.md)(NaN-boxing/canonicalize/lightuserdata/host 注册表) ·
[02-bytecode-isa](./02-bytecode-isa.md)(顶层 chunk vararg/调用约定) ·
[05-interpreter-loop](./05-interpreter-loop.md)(enterLuaFrame/host→Lua 重入/HostFn 签名/错误返回) ·
[06-memory-gc](./06-memory-gc.md)(arena 组织/shadow stack/string intern) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(arena userdata 的 `__index`/`__newindex`) ·
[09-errors-pcall](./09-errors-pcall.md)(LuaError → Go error) ·
[10-stdlib](./10-stdlib.md)(host function 调用约定 / 与 Register 统一) ·
[12-testing-difftest](./12-testing-difftest.md)(gopher-lua 差分基准 / drop-in 行为兼容) ·
[../p3-wasm-tier](../p3-wasm-tier.md)(arena ABI = linear memory 布局) ·
[architecture](../architecture.md)(包布局:公共 API 在 root package) ·
[embedding-contract](../../../llmdoc/reference/embedding-contract.md)(本文落地的上游契约) ·
[design-premises](../../../llmdoc/must/design-premises.md)(前提一列内核 / 前提二边界成本) ·
[value-representation](../../../llmdoc/architecture/value-representation.md)(两层共见同一块内存)
