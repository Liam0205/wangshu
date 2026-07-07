# P1 脊柱:值表示与对象模型

> 状态:**设计阶段,可实现深度**。本文是全 VM 一致性的**单一事实源**:NaN-boxing 位布局、
> tag 编码、arena 寻址、GC 对象内存布局。所有其他文档(ISA、解释器、GC、stdlib、嵌入 ABI)
> 一律以本文的位定义为准。
> 上游约束:[value-representation](../../../llmdoc/architecture/value-representation.md)(第 1 天承诺)、
> [design-premises](../../../llmdoc/must/design-premises.md)(四项税)。包布局见 [architecture](../architecture.md)。

对应 Go 包:`internal/value`、`internal/arena`、`internal/object`。

---

## 1. 什么住 arena,什么住 Go 堆(对 roadmap §3 的工程细化)

roadmap §3 说「值世界放自管 arena」。落到实现,需精确区分**动态值世界**与**不可变代码**:

| 类别 | 住哪 | 理由 |
|---|---|---|
| 动态可回收对象:String / Table / Closure / Upvalue / Userdata / Thread | **arena** | 运行期被解释器与未来 gibbous 编译码**共同读写**;必须零拷贝共见(§3 premise) |
| 不可变代码:`Proto`、指令流 `[]Instruction`、常量表容器 | **Go 堆** | 只在**编译期**被各 tier 读取(翻译为 Wasm/原生),运行期不再读 Lua 指令;无需进 arena |
| 宿主注册的 host function | **Go 堆**(函数注册表) | Go 闭包无法装进 arena;由整数 ID 引用 |

**关键推论:**

- arena 内对象指向代码用**整数 ID**(`ProtoID` / `HostFnID`),不用 GCRef(GCRef 不能指向 Go 堆)。
- 常量表是 Go 侧 `[]Value`,其中**字符串常量元素是指向 arena 的 GCRef**(指向已 intern 的字符串)。因此 **Proto 注册表的常量 Value 是 GC 根**(否则常量字符串会被误扫)。见 [06-memory-gc](./06-memory-gc.md)。
- GC 只管理 arena 内的 6 类动态对象;代码不参与 mark-sweep。

> 这是对 [value-representation](../../../llmdoc/architecture/value-representation.md) 的细化,需作为决策记录归档(见文末 doc-gaps)。premise「编译层读写同一块内存」指的是**运行期值世界**,代码侧走整数 ID 不违背它。

---

## 2. arena 寻址模型

arena 是一段自管线性内存,同时提供两个视图(同一底层 backing,经 `unsafe` 别名):

- `bytes []byte` —— 字节视图,用于字符串内容、变长数据;
- `words []uint64` —— 字视图,用于对象头与 Value 字段(8 字节对齐)。

```
GCRef = 48-bit 字节偏移(byte offset)进 arena.bytes
  - 所有 GC 对象 8 字节对齐 ⇒ GCRef 低 3 bit 恒为 0
  - 对象头字访问:words[gcref >> 3]
  - 对象第 i 个字字段:words[(gcref >> 3) + i]
  - 字节访问:bytes[gcref : ...]
```

48-bit 字节偏移 = 256 TB 寻址空间,远超实际需求(实际 arena 通常 < 4 GB)。**GCRef 不是 Go 指针**——它对 Go GC 是普通整数,这正是绕开「写屏障税」(§2)的物理手段:arena 内的对象互相引用用 GCRef,Go GC 看不到也不需要管。

---

## 3. NaN-boxing 位布局(核心)

每个 Lua 值是一个 `uint64`(Go 类型 `type Value uint64`)。利用 IEEE-754 double 的 NaN 空间编码非数字值。

### 3.1 双精度浮点回顾

```
 bit: 63 | 62 .......... 52 | 51 ........................... 0
      [S] [   exponent 11   ] [        mantissa 52           ]
```
- `exponent == 0x7FF` 且 `mantissa != 0` ⇒ NaN。
- 我们把**符号位为 1 的 quiet NaN 空间**(高 16 位 ∈ `0xFFF8..0xFFFF`)征用为 boxed tag。

### 3.2 判定规则(单比较)

```
Value v 是 number  ⟺  v < 0xFFF8_0000_0000_0000   (uint64 无符号比较)
否则 v 是 boxed,其 tag = uint16(v >> 48)
```

验证边界(均 < `0xFFF8…`,正确归为 number):
- 有限 double、+0/-0、±Inf(`0x7FF0…` / `0xFFF0…`)、正 qNaN(`0x7FF8…`)。
- 唯一会冲突的是**负 NaN**(`0xFFF8..0xFFFF` 段)——由 §3.4 canonicalization 不变式排除。

### 3.3 Tag 表(全 8 个非数字类型,单一事实源)

Lua 5.1 类型集是**封闭的**(roadmap §6 拒绝 5.2+/整数子类型),恰好 8 个非数字内部表示,**正好填满** `0xFFF8..0xFFFF`——无空槽是设计意图,不是巧合。

| tag(hi16) | 内部类型 | payload[47:0] | `type()` 返回 | 可回收 |
|---|---|---|---|---|
| `0xFFF8` | nil | 0 | `"nil"` | 否 |
| `0xFFF9` | boolean | 0=false / 1=true | `"boolean"` | 否 |
| `0xFFFA` | lightuserdata | 48-bit 不透明句柄 | `"userdata"` | 否 |
| `0xFFFB` | string | GCRef | `"string"` | **是** |
| `0xFFFC` | table | GCRef | `"table"` | **是** |
| `0xFFFD` | function | GCRef(Closure) | `"function"` | **是** |
| `0xFFFE` | userdata(full) | GCRef | `"userdata"` | **是** |
| `0xFFFF` | thread | GCRef(Thread) | `"thread"` | **是** |
| —(其余) | number | (整个 64-bit 是 double) | `"number"` | 否 |

**契约性质(后续 tier 不得更改):**
- 可回收类型 = tag ∈ `[0xFFFB, 0xFFFF]`,**连续**⇒ `isCollectable(v) = v >= 0xFFFB_0000_0000_0000`(配合 `v` 已是 boxed)。
- 单类型检查恒为单次 16-bit 比较;`isNumber` 为单次 64-bit 比较。
- lightuserdata 与 full userdata 的 `type()` 都返回 `"userdata"`(Lua 5.1 语义)。

### 3.4 不变式:NaN 规范化(canonicalization)

> **值世界中任何 NaN 数字必须是规范正 qNaN** `0x7FF8_0000_0000_0000`。

理由:负 NaN(`0xFFF8..`)与 boxed tag 段重叠。x86/arm64 默认对无效运算产生正 qNaN,已满足;但以下入口必须显式规范化,防止外部负 NaN 渗入:
- 算术指令产生 NaN 时(`canonicalizeNaN(x)`:`if x!=x { return canonNaN }`);
- `tonumber` / 字符串转数字 / `string.unpack` / arena ABI 读入的 float 列;
- 嵌入 API `PushNumber`。

实现 helper:`func NumberValue(f float64) Value { if f != f { f = canonNaN } ; return Value(math.Float64bits(f)) }`。

### 3.5 常量与构造/提取(`internal/value` API 草图)

```go
const (
    qNanBoxBase   = 0xFFF8_0000_0000_0000
    payloadMask   = 0x0000_FFFF_FFFF_FFFF
    TagNil        = 0xFFF8
    TagBool       = 0xFFF9
    TagLightUD    = 0xFFFA
    TagString     = 0xFFFB
    TagTable      = 0xFFFC
    TagFunction   = 0xFFFD
    TagUserdata   = 0xFFFE
    TagThread     = 0xFFFF

    Nil   Value = 0xFFF8_0000_0000_0000
    False Value = 0xFFF9_0000_0000_0000
    True  Value = 0xFFF9_0000_0000_0001
    canonNaN uint64 = 0x7FF8_0000_0000_0000
)

func IsNumber(v Value) bool      { return v < qNanBoxBase }
func IsCollectable(v Value) bool  { return v >= 0xFFFB_0000_0000_0000 }
func tag(v Value) uint16          { return uint16(v >> 48) }

func NumberValue(f float64) Value // 见 §3.4,含 canonicalize
func AsNumber(v Value) float64     { return math.Float64frombits(uint64(v)) }

func BoolValue(b bool) Value        { if b { return True }; return False }
func AsBool(v Value) bool            { return v&1 == 1 }     // 仅当 tag==TagBool
func Truthy(v Value) bool            { return v != Nil && v != False } // Lua 真值:仅 nil/false 为假

func GCRefOf(v Value) GCRef          { return GCRef(uint64(v) & payloadMask) }
func MakeGC(t uint16, ref GCRef) Value { return Value(uint64(t)<<48 | uint64(ref)) }

func LightUDValue(handle uint64) Value // handle 必须 ≤ payloadMask
func AsLightUD(v Value) uint64          { return uint64(v) & payloadMask }
```

**lightuserdata 限制(重要):** payload 是 48-bit **不透明句柄**,不得直接装 Go 堆指针——否则 Go GC 看不见会悬垂(违反 §2 写屏障税与 roadmap §6「不让 Lua 直访 Go 堆」)。宿主若要传 Go 对象,须用 `runtime.Pinner` 固定后传 `uintptr`,或走句柄表索引。详见 [11-embedding-arena-abi](./11-embedding-arena-abi.md)。amd64/arm64 用户态地址 48-bit 内,5 级页表(57-bit)罕见高地址不支持直存,以句柄表兜底。

---

## 4. GC 对象通用头(GCHeader,1 字 = 8 字节)

每个 arena 内 GC 对象的首字是统一头:

```
word0 = GCHeader:
  bits [7:0]    otype   : OBJType 枚举(见下)
  bits [9:8]    color   : GC 三色(0=white0,1=white1,2=gray,3=black)
  bits [10]     fixed   : 1=永不回收(如被 intern 固定 / Proto 引用的常量串可标 fixed)
  bits [11]     hasGCNext: 链表标志(sweep 全对象链)
  bits [15:12]  flags   : 类型私有小标志(如 table 有无 metatable 的快判位)
  bits [63:16]  gcnext  : 48-bit 字节偏移,指向 sweep 链下一对象(0=链尾)
```

`OBJType`(与 §3.3 tag 对应但独立枚举,供堆遍历时识别):
```
OBJ_STRING=1  OBJ_TABLE=2  OBJ_CLOSURE=3  OBJ_USERDATA=4  OBJ_THREAD=5
OBJ_PROTO=6   OBJ_UPVAL=7   (Proto/Upval 见说明)
```
> 注:`Proto` 住 Go 堆(§1),故 `OBJ_PROTO` 仅在「未来把 Proto 移入 arena」时启用,P1 不分配。`Upvalue` 住 arena(运行期对象),`OBJ_UPVAL` 生效。

双白色(white0/white1)用于增量/分代 mark-sweep 的「当前白」翻转,P1 先实现 stop-the-world 也保留位以便演进。详见 [06-memory-gc](./06-memory-gc.md)。

---

## 5. 各对象内存布局

所有偏移以**字(8 字节)**为单位,从对象起始(GCHeader)算起。Value 字段均为 NaN-boxed `uint64`。

### 5.1 String(不可变,全 intern)

Lua 5.1 语义:所有字符串 intern,字符串相等 == GCRef 相等。

```
word0: GCHeader (otype=STRING)
word1: [31:0] hash32 | [63:32] len(字节长,≤4GB)
word2..: 内容字节,按 8 字节向上对齐填充;末尾补 1 个 NUL(便于 C 互操作,不计入 len)
```
- `hash32`:对字节做 FNV-1a 或 Lua 式分段哈希(短串全采样,长串步长采样)。短串(≤40 字节,可配)与长串都 intern,统一进 string table(见 [06-memory-gc](./06-memory-gc.md) 的 interning)。
- 相等:`s1 == s2 ⟺ GCRef 相等`。哈希查表:`hash32`。

### 5.2 Table(数组部分 + 哈希部分)

沿用 Lua 5.1 表算法(array part 存连续整数键 `1..n`,hash part 存其余)。

```
word0: GCHeader (otype=TABLE; flags bit0 = 有 metatable)
word1: [31:0] asize(数组槽数) | [63:32] hmask(哈希槽数-1,哈希槽数恒为 2 的幂)
word2: arrayRef  (GCRef→ Value[asize] 连续数组;asize=0 时为 0)
word3: nodeRef   (GCRef→ Node[hmask+1];空表为 0/指向公共 dummynode)
word4: metaRef   (GCRef→ metatable Table,或 Nil)
word5: [31:0] lastfree(哈希空闲槽搜索游标) | [63:32] gen(IC 代次,单调递增)
```

**`gen` 代次字段**(承 [05](./05-interpreter-loop.md) §6.6 回填请求,IC 失效机制的物理载体):任何改变表"形状"的操作(rehash、增删 metatable、键的数组↔哈希迁移)递增 `gen`;已存在键改值**不**递增。`object.Table` 暴露 `Gen()`/`bumpGen()`。初始 0。详见 [05](./05-interpreter-loop.md) §6。

**Node(哈希槽,3 字 = 24 字节):**
```
word0: key  (Value)
word1: val  (Value)
word2: [31:0] next(int32,链入同主位置冲突链的下一槽索引,-1=无) | [63:32] reserved
```

**键归类规则:**
- 数字键 `k`:若 `k == floor(k)` 且 `1 <= k <= asize`,走数组槽 `array[k-1]`;否则走哈希。
- `NaN` 不可作键(Lua 运行期报错 `table index is NaN`)。
- `nil` 不可作键(报错 `table index is nil`)。
- `-0.0` 归一化为 `+0.0` 后再哈希/比较。

**查找/插入:** 主位置 = `hash(key) & hmask`;Brent 变体处理冲突(占用者非主位置时让位)。rehash 时数组与哈希按 Lua 5.1 `luaH_resize` 重算最优 asize(使数组装填率 > 50%)。算法细节实现时对照 Lua 5.1 `ltable.c`,语义须与差分基准逐字节一致。

**`#`(长度)语义:** 返回一个 border `n`(`t[n]~=nil && t[n+1]==nil`);数组部分用二分,与 5.1 一致(非确定性边界对非序列表是允许的)。

### 5.3 Closure(函数)

分 Lua 闭包与 Host 闭包,用 `flags bit0` 区分。

```
Lua 闭包:
  word0: GCHeader (otype=CLOSURE; flags bit0=0 Lua)
  word1: [31:0] protoID(Go 侧 Proto 注册表索引) | [47:32] nupvals | [63:48] reserved
  word2..: upvalRef[nupvals]  (各 GCRef→ Upvalue 对象)

Host 闭包(C 闭包等价物):
  word0: GCHeader (otype=CLOSURE; flags bit0=1 Host)
  word1: [31:0] hostFnID(Go 侧 host 函数注册表索引) | [47:32] nupvals
  word2..: upval[nupvals]  (Value,直接捕获的值;Host 闭包的 upvalue 是普通 Value,非 Upvalue 对象)
```

### 5.4 Upvalue(仅 Lua 闭包用)

开放(指向某 thread 栈槽)/ 关闭(自持值)两态。

```
word0: GCHeader (otype=UPVAL; flags bit0 = 0 开放 / 1 关闭)
word1: 开放时: [31:0] stackIdx | [63:32] threadRef 低 32 位(定位 arena 内栈槽);  关闭时: 未用
word2: 开放时: nextOpen(GCRef→ 本 thread 开放 upvalue 降序链的下一节点,0=链尾);
       关闭时: value (Value,自持值)
```
实现约定:**开放 upvalue** 不复制值,逻辑上指向 `thread.stack[idx]`。为避免 GCRef 指 Go 栈,thread 的值栈本身在 arena(见 5.6),故开放 upvalue 用 `(threadRef, stackIdx)` 定位 arena 内栈槽。同一栈槽的多个开放 upvalue 共享同一对象,所有开放 upvalue 按 `stackIdx` **降序**串成 thread 上的 openupval 链——链指针就是开放态的 `word2 nextOpen`(承 [05](./05-interpreter-loop.md) §8.6 回填:开放时 word2 不存值,正好承载链指针;关闭时拷值入 word2、脱链,nextOpen 失义)。`CLOSE`/作用域退出时把值拷入 word2 并置关闭。完整链算法与关闭流程见 [05-interpreter-loop](./05-interpreter-loop.md) §8.3。

### 5.5 Userdata(full userdata)

```
word0: GCHeader (otype=USERDATA; flags bit0 = 有 metatable)
word1: [31:0] payloadLen(字节) | [63:32] reserved
word2: metaRef (GCRef→ metatable 或 Nil)
word3: envRef  (GCRef→ environment table 或 Nil;Lua 5.1 userdata 有 env)
word4..: payload 字节(payloadLen,8 字节对齐)
```
full userdata 的 payload 是宿主分配的不透明字节块,**住 arena**,可带 metatable + GC(可设 `__gc` 终结器)。宿主若需关联 Go 对象,经句柄表(见 lightuserdata 限制)。

### 5.6 Thread(协程 / coroutine)

```
word0: GCHeader (otype=THREAD)
word1: status(8: running/suspended/normal/dead) | flags
word2: valueStackRef (GCRef→ Value[stackCap] 值栈;寄存器即栈槽)
word3: [31:0] top(当前栈顶索引) | [63:32] stackCap
word4: callInfoRef (GCRef→ CallInfo 数组,调用帧栈)
word5: [31:0] ciTop | [63:32] ciCap
word6: openUpvalRef (GCRef→ 本 thread 开放 upvalue 链头,或 0)
word7: errorJmp / 状态机字段(pcall 保护点链,见 09-errors-pcall)
word8: resumeFrom / caller thread ref(resume 链)
```
**值栈即寄存器文件**:字节码的寄存器 `R(i)` = `thread.valueStack[base + i]`(`base` 为当前帧基址)。栈与 CallInfo 都在 arena,故跨界与 GC 都不触碰 Go 栈(扣合 §2 栈移动税)。栈扩容时整体搬迁并修正开放 upvalue 定位。详见 [08-coroutines](./08-coroutines.md)、[05-interpreter-loop](./05-interpreter-loop.md)。

### 5.7 Proto(住 Go 堆,经 ProtoID 引用)

逻辑结构(Go struct,非 arena;字段语义见 [02-bytecode-isa](./02-bytecode-isa.md)):
```go
type Proto struct {
    Code         []Instruction // uint32 指令流
    Consts       []Value       // 常量槽:数字直接 boxed;字符串槽存占位(见下),由 State 装载期惰性替换为 GCRef
    StringLits   []string      // 字符串字面量原文(Compile 期间收集,跨 State 不可变共享)
    StringLitIdx []int32       // Consts 中字符串槽 → StringLits 下标的映射;非字符串槽 = -1
    Protos       []ProtoID     // 嵌套函数原型
    UpvalDescs   []UpvalDesc   // upvalue 来源描述(inStack? idx + name 调试名,见 04 §8.3)
    NumParams    uint8
    IsVararg     bool
    MaxStack     uint8         // 该函数需要的寄存器数
    LineInfo     []int32       // 调试:每指令源行
    LocVars      []LocalVar    // 调试:局部变量名 + 活跃区间 [startpc,endpc)(04 §5.9 产出)
    Source       string        // 源名(chunkname),Go 堆;运行期 traceback 使用,不进 arena
}

type LocalVar struct { Name string; StartPC, EndPC int32 }
```
`LocVars` 承 [04](./04-frontend-parser-codegen.md) §13 与 [09](./09-errors-pcall.md) §8.4 的回填请求:codegen 的 `removeVars` 闭合活跃区间后写入(04 §5.9),供错误信息变量名后缀与 traceback 的 `local 'x'` 推断(09 §8)。upvalue 名复用 `UpvalDescs` 的 `name` 字段(04 §8.3 已含),不再单列 `UpvalNames`。LocVars 是 Go 堆调试数据,不入 arena、不参与 GC(`Name` 是 Go string)。

**字符串常量的惰性 intern**(承 [11](./11-embedding-arena-abi.md) §1.3 多 State 复用 Program 的并发承诺,M8 codegen 完成需要):

- codegen 阶段产出的 Proto 持 `StringLits []string`(原始字节,Go 堆),`Consts` 中字符串字面量槽位**留占位**(实际值由 `StringLitIdx[槽] = StringLits 下标` 间接寻址)。
- 占位 bit pattern:`Consts[i] = value.Nil`(表示"该槽待装载"),并由 `StringLitIdx[i] >= 0` 区分"是字符串占位"vs"是真 nil 常量";真 nil 常量令 `StringLitIdx[i] = -1`。
- `Program` 不可变共享:codegen 完成后 `StringLits` / `StringLitIdx` 写满,只读。
- 每个 `State` 首次执行某 Program 时(M13 装载路径):遍历 `StringLits` 逐个 intern 进 State arena,得到 GCRef 表 `programStringRefs map[*Proto][]value.Value`;运行期 `LOADK Bx` 命中字符串槽(`StringLitIdx[Bx] >= 0`)→ 读 State 的 GCRef 表;否则直接读 `Consts[Bx]`。
- 工程权衡:首次 Call 在每个 State 上做一次 N 个 intern(不可省,GCRef 是 arena 私有的);Program 只多持 N 个 Go string(L 字节级),不多持一份 String 对象副本。Lua C 实现"Proto 持 TString 指针"的方案在此被替换为"Proto 持原文 + State 持 GCRef 表",代价是 Consts 读取多一次间接。

`Source` 是 Go string(chunkname),不再是 GCRef——它只供 traceback 用,不需要进值世界。

`Program` / `State` 持有 `protos []*Proto` 注册表,`protoID` 即下标。`State.programStringRefs[*Proto]` 持有该 State 私有的字符串 GCRef 表(运行期 GC 根,加入 [06](./06-memory-gc.md) §5.1 的根集合,与 R6 同级)。

---

## 6. 相等、真值、哈希语义

- **真值**(`Truthy`):仅 `nil` 与 `false` 为假;`0`、`""`、`NaN` 均为真(Lua 语义)。
- **原始相等**(`rawequal`,无 metamethod):
  - 不同 tag 类(number vs 其它)⇒ 不等;
  - number:按 IEEE 数值比较(`+0.0 == -0.0` 为真,`NaN != NaN`);**注意**不能直接比 bits(因 `+0/-0` bits 不同、NaN 规范化后 bits 同但仍须 `!=`),走 `AsNumber` 浮点比较;
  - string:GCRef 相等(intern 保证);
  - 其它 boxed:`uint64` bits 相等。
- **`==` metamethod**:仅当两操作数同为 table 或同为 userdata 且 rawequal 为假时,查 `__eq`(Lua 5.1 要求两者同类型且各自有 `__eq`)。见 [07-metatables-metamethods](./07-metatables-metamethods.md)。
- **哈希键**:number 按数值(`-0.0`→`+0.0`),string 用 `hash32`,bool/nil/light/gc 用 `uint64` bits;`NaN`/`nil` 不可为键。

---

## 7. 不变式清单(实现与差分测试须守)

1. **值即 8 字节**:任何 Lua 值都是单一 `uint64`,栈/寄存器/表槽/upvalue 同构 —— 跨 tier 拷贝是 memmove。
2. **NaN 规范**:值世界内 NaN 恒为 `0x7FF8_0000_0000_0000`(§3.4)。
3. **string intern**:相等串同一 GCRef;`rawequal` 字符串退化为指针比较。
4. **GCRef 非 Go 指针**:arena 内引用全用 48-bit 字节偏移;代码引用全用整数 ID —— Go GC 不可见 arena 内部图(§2 写屏障税的兑现)。
5. **tag 封闭**:8 个非数字 tag 用满 `0xFFF8..0xFFFF`,新增类型须改本文且全 tier 同步(roadmap §6 已锁定不新增)。
6. **可回收连续**:`isCollectable ⟺ tag∈[0xFFFB,0xFFFF]`,GC 与 barrier 依赖此区间判定。

---

## 8. 文档缺口 / 待决(记入 memory/doc-gaps)

- **字符串哈希算法**:已由 [06-memory-gc](./06-memory-gc.md) §9.3 定稿为 **Lua 5.1 JSHash 分段采样**(否决 FNV-1a,理由:把哈希环锁成与官方逐位一致,为 `pairs` 序严格差分口径创造必要条件)。`pairs` 序的最终验收口径已由 [12-testing-difftest](./12-testing-difftest.md) 收口(混合口径)。**本缺口已关闭。**
- **arena/Proto 划分**对 roadmap §3「值世界全在 arena」是细化(代码走 Go 堆+整数 ID),应作为决策记录归档。
- `lastfree` 与 Brent 变体的精确实现留给 [06-memory-gc](./06-memory-gc.md)/codegen,本文只定布局。
- **已回填的字段增补**(原下游回填请求,均已落入本文布局):① Table `gen` 代次(§5.2,承 05 §6.6);② Upvalue 开放态 `nextOpen` 链指针(§5.4,承 05 §8.6);③ Proto `LocVars` 局部变量名表 + `UpvalDescs.name`(§5.7,承 04 §13 / 09 §8.4)。

---

相关:[02-bytecode-isa](./02-bytecode-isa.md) · [05-interpreter-loop](./05-interpreter-loop.md) ·
[06-memory-gc](./06-memory-gc.md) · [07-metatables-metamethods](./07-metatables-metamethods.md) ·
[11-embedding-arena-abi](./11-embedding-arena-abi.md) ·
[value-representation](../../../llmdoc/architecture/value-representation.md)
