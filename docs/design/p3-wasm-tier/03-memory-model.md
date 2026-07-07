# P3-03 共见内存模型:arena 收养 wazero memory + 两层逐位同一

> 状态:**设计阶段,详细设计已齐备**(具体 wazero API 细节标注「待 spike 验证」,与 [01-spike-gate](./01-spike-gate.md) §4 共题完成)。本文是 P3 文档集对「值世界 = linear memory:两层共见的物理兑现」的单一事实源——把现稿 p3-wasm-tier §4 的 25 行结论,扩展为 arena 收养 wazero memory 的实代码骨架、值编码两层逐位同一的位级证据、grow 协议、GC 根零新增、Go 堆侧资产不进 linear memory 的精确划界、wazero memory 形式下的特殊场景(string/weak/finalizer/freelist),以及对 [06-memory-gc](../p1-interpreter/06-memory-gc.md) 与 [11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) 的回填请求。
>
> 上游契约:`docs/design/roadmap.md` (§3 值世界放自管 arena;§2 四项税);
> [00-overview](./00-overview.md)(§3 第 1 项耦合点 arena 收养、§9 不变式 8「arena = wazero memory」);
> [value-representation](../../../llmdoc/architecture/value-representation.md)(主线 — P3 是其物理兑现节点);
> [01-value-object-model](../p1-interpreter/01-value-object-model.md)(§2 arena 寻址 / §3 NaN-boxing / §3.4 canonicalize / §4 GCHeader / §5 各对象布局);
> [06-memory-gc](../p1-interpreter/06-memory-gc.md)(§1.1 双视图 backing + 注入点 / §3 grow / §5 GC 根)。
>
> 下游衔接:[02-translation](./02-translation.md)(寄存器=共见栈槽的物理依据);
> [04-trampoline](./04-trampoline.md)(跨层只传 `base i32`,余从共见栈槽自取);
> [05-safepoint-gc](./05-safepoint-gc.md)(GC 根零新增,locals 缓存写回纪律);
> [11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md)(arena ABI 在 P3 build 下零拷贝读路径不变)。

对应 Go 包:`internal/arena`(P3 build 下 `BackingFn` 注入点替换为 wazero adapter)、`internal/gibbous/wasm`(wazero Runtime / Memory 适配器,新增子包 `internal/gibbous/wasm/memadapter`)。

---

## 0. 定位:值表示一次定死的物理终点

### 0.1 一句话总结

P3 内存模型是「值表示一次定死、两层共享」(`docs/design/roadmap.md` §3、[value-representation](../../../llmdoc/architecture/value-representation.md))在物理层的**终点节点**:解释器侧 arena 的底层 backing 与 Wasm 侧 linear memory **是同一块物理内存的两个视图名**。crescent 写一个表槽,gibbous 读同一偏移;**无序列化、无拷贝、无影子副本**——这正是「编译层是纯增量」(roadmap §3)的物理含义。

### 0.2 三条物理事实(后续小节论证)

1. **同一块内存**:arena 的 `[]uint64` backing **就是**(P3 build 下)wazero memory 的底层 buffer;`arena.bump`/`cap` 与 wazero memory 的容量同步演进。
2. **同一套编码**:NaN-boxed `uint64`([01](../p1-interpreter/01-value-object-model.md) §3)在两层逐位同一,无 P3 私有的对象布局或字段顺序。GCRef = 48-bit 字节偏移,与 wasm32 寻址语义匹配。
3. **同一套 grow 协议**:`memory.grow` 后偏移寻址不变(`words[ref>>3]` 在新 buffer 上自动指向同一逻辑位置),所有 GCRef / freelist 链 / sweep 链 / bump 一字不改;Go 侧仅做视图 slice 重取。

### 0.3 与 [00-overview](./00-overview.md) §3「关键耦合点 1 (arena 收养 wazero memory)」的关系

[00-overview](./00-overview.md) §3 第 1 项把这件事列为 P3 实现期最易出错处,本文是其设计单一事实源:展开物理证据(§1-§3)、不变式(§7)、回填请求(§8)。**任何「在 wazero 与 arena 间引入序列化或拷贝」的实现倾向都直接判否**——它会让 [value-representation](../../../llmdoc/architecture/value-representation.md) 的「编译层是纯增量」物理基础崩塌。

### 0.4 与 P4(原生 JIT)的关系

P4 仍**复用**这块共见内存——原生码读写同一份 arena,不发明新值表示([../p4-method-jit/01-launch-judgment](../p4-method-jit/01-launch-judgment.md) §2 常规路径承诺「只换发射后端」)。P3 的内存模型一旦定稳,P4 阶段不动。**P4 唯一的差异是「不再经 wazero memory 中介」**:P4 build 下 `BackingFn` 切回 `DefaultBacking`(纯 Go 堆 `make`),原生码经 Go 侧暴露的 `[]byte` 起始指针读写。GCRef 偏移寻址在两种 backing 形式下语义同一,从而让发射后端的切换是局部变更。

### 0.5 本文与现稿 §4 的关系

现稿 p3-wasm-tier §4 是 25 行综述;本文是其展开式,覆盖:

| 现稿 §4 章节 | 本文位置 | 展开内容 |
|---|---|---|
| §4.1 同一块内存、同一套编码、同一套偏移 | §2 | NaN-box 两层位比较代码、GCRef 与 wasm32 寻址匹配证据、4 GiB 上限的隐性红利 |
| §4.2 backing 归属:arena 收养 wazero memory | §1 | NewState 时 wazero 分配、`arena.Options.NewBacking` 注入点实代码骨架、wazero memory adapter 实现、`memory.grow` 协议、build tag 分流 |
| §4.3 Go 堆侧资产不进 linear memory | §5 | Proto / 指令流 / host 注册表的精确划界,与 [01](../p1-interpreter/01-value-object-model.md) §1 自洽 |
| §6.2 GC 根:零新增机制 | §4 | R5 (running thread 栈 + CallInfo) 原样覆盖,根枚举代码一行不改 |
| §10 不变式 2/5 | §7 | 6 条不变式聚合 |
| §11 缺口前两条 | §8 | wazero memory 共享 API 详细形式、对 06 / 11 的回填 |

---

## 1. arena 收养 wazero memory(P3 起的关键迁移)

### 1.1 P1 形式:Go 堆 `[]uint64` backing(基线)

[06-memory-gc](../p1-interpreter/06-memory-gc.md) §1.1 与 [01-value-object-model](../p1-interpreter/01-value-object-model.md) §2 已定 P1 形式:

```
Arena {
    words []uint64    // 真实 backing,Go 堆上的 make([]uint64, n)
    bytes []byte      // unsafe.Slice 别名 words 的字节视图
    bump  uint32      // 下一个未分配字节偏移(始终 8 对齐)
    cap   uint32      // 当前容量(字节,= len(words)*8)
}
```

backing 的语义关键点(在 P3 完成前必须复读):

- **来自 Go 堆的 `make([]uint64, n)`**:Go GC 看得见这块 slice,但其元素是纯整数(NaN-boxed Value 与偏移),不含 Go 指针,故 Go GC 扫描时不追踪任何内部引用——arena 对 Go GC「不可见对象图」即由此兑现([06](../p1-interpreter/06-memory-gc.md) §1.1)。
- **grow = `make + copy` 的 realloc**([06](../p1-interpreter/06-memory-gc.md) §3):新 backing 在 Go 堆的**新地址**,但 GCRef 是 48-bit **字节偏移**,与 backing 的绝对地址无关——所有 GCRef / freelist 链 / sweep 链 / bump 一字不改。这是「偏移寻址(而非 Go 指针)的额外好处」最直接的兑现。
- **Go 堆的零值保证**:`make([]uint64, n)` 返回零值切片,`offset 0 = null GCRef`(语义上「无对象」)与三色 white0 颜色字节同时为零,故 backing 起始即合法初态(`bump = 8` 即 nullReserve)。

### 1.2 P3 起的形式:backing 来源改为「以 wazero memory 的底层 buffer 为来源」

P3 阶段的目标是让 wazero 生成码与解释器**共见同一块物理内存**。技术决策(承现稿 §4.2):

> **arena 的 backing 改为「收养 wazero Memory 的底层 buffer」**——`NewState` 时即经 wazero 分配 memory(P3 build 下,`P1` 形式下 wazero 仅作 allocator,无模块运行),arena 的 `words`/`bytes` 视图从该 buffer 派生;grow 走 `memory.grow`(偏移寻址使 grow 后所有 GCRef / 链表 / bump 一字不改,[06](../p1-interpreter/06-memory-gc.md) §3 的红利原样保留,仅 Go 侧视图 slice 重取)。

**为什么是「收养」而非「双份」**:wazero 的 linear memory 由其 Runtime 持有(`api.Memory`),Wasm 侧 `memory.grow` 按页扩;Go 堆侧若再持一份独立 backing,两块内存只能要么互相同步(每次写一份就拷贝到另一份,等于把整个值世界变成纯拷贝)、要么各跑各的(crescent 与 gibbous 写不同槽位,语义分裂)。两条都违背 [value-representation](../../../llmdoc/architecture/value-representation.md) 的「两层共见」承诺。**唯一可行解是 backing 来源唯一**——选 wazero memory 作为这唯一来源(P1 build 下退回 Go 堆 `make`)。

**为什么 wazero memory 能作为这唯一来源**:wazero 的 `api.Memory` 暴露 `Read`/`Write`/`UnsafeUnderlyingBuffer` 等方法,后者(在 wazero 当前版本)返回底层 buffer 的 `[]byte` 引用。这是 Go 端拿到 wazero 内部 backing 的**直接通道**——Go 端的所有读写经此 buffer 就是写到 wazero 的 linear memory 上。Wasm 侧的 `i64.load`/`i64.store` 经 linear memory 的 offset 操作就是读写同一 buffer。两侧物理同一,从而「同一字节、两层名称」。

> **风险标注**:`UnsafeUnderlyingBuffer` 的命名与签名因 wazero 版本而异(待 spike 验证,见 §3 与 [01-spike-gate](./01-spike-gate.md) §4)。若该 API 在 spike 时点不可用,候选 fallback 见 §3.1(import memory 形式)。

### 1.3 NewState 时即经 wazero 分配 memory(P3 build);P1-only build 仍走 `make`

P3 build 与 P1-only build 的初始化路径分流(编译期 build tag,详见 §1.8):

**P3 build 下的 `NewState`(伪代码,实代码骨架在 §1.4-§1.5)**:

```go
// internal/gibbous/wasm/memadapter (新增子包,P3 build 专属)
func NewArenaWithWazero(opts arena.Options, runtime wazero.Runtime) *arena.Arena {
    // 1. 用 wazero 编译一个仅 declare memory 的 stub module,实例化得到 api.Memory。
    //    memory 起始页 = ceil(opts.InitialBytes / 64KiB);max page 由 opts.MaxBytes 推算。
    mem := buildAndInstantiateMemoryHolder(runtime, opts.InitialBytes, opts.MaxBytes)

    // 2. 注入 BackingFn:每次 arena 需要 backing 时,从 wazero memory 派生 []uint64 视图。
    //    grow 走 §1.6 的协议——本注入点的 words 参数 ≤ memory 当前容量;
    //    超容由调用前 ensureCapacity(words*8) 触发 memory.grow。
    opts.NewBacking = func(words uint32) []uint64 {
        ensureCapacity(mem, uint64(words)*8)
        buf := mem.UnsafeUnderlyingBuffer() // []byte,长度 = mem.Size()
        return unsafeBytesToWords(buf)       // unsafe.Slice 别名,见 §1.5
    }

    a := arena.New(opts) // arena.New 内部调 BackingFn 拿到 backing,后续完全照常运行
    return a
}
```

**P1-only build 下的 `NewState`**:`opts.NewBacking == nil`,arena.New 走 `arena.DefaultBacking`(纯 Go 堆 `make`),完全不引入 wazero。这是 P1 至今的实际跑法——P1 已留好注入点,P3 实代码改动量集中在 `internal/gibbous/wasm/memadapter` 子包,arena 包零改动。

### 1.4 backing 注入点 `arena.Options.NewBacking`(P1 已留口)

P1 已经在 `internal/arena/arena.go` 实代码层完成了 `BackingFn` 注入点(承现稿 §11「对 06 的回填请求」与 [00-overview](./00-overview.md) §7 已完成义务表):

```go
// internal/arena/arena.go(已完成,引述于此)

// BackingFn 是 backing 内存的工厂。P1 默认实现为 make([]uint64, n);P3 替换为 wazero
// linear memory adapter(承 06 §1.1 与 p3-wasm-tier §4.2 回填请求)。
type BackingFn func(words uint32) []uint64

// DefaultBacking 是 P1 默认 backing 工厂(纯 Go 堆分配)。
func DefaultBacking(words uint32) []uint64 { return make([]uint64, words) }

// Options.NewBacking nil ⇒ DefaultBacking。
type Options struct {
    InitialBytes uint32
    MaxBytes     uint32
    NewBacking   BackingFn // P3 注入点
}
```

**注入点的契约**(P3 实现时必须遵守):

1. **`NewBacking(words uint32)` 必须返回长度恰好为 `words` 的 `[]uint64`**;短了 arena.New 会 panic(P1 已写防御),长了被截断为 `words`(P3 适配器内部允许底层 buffer 长于请求,但要确保返回视图是请求长度——见 §1.5 实代码骨架)。
2. **返回的 slice 必须保证起始 8 字节对齐**——这是 NaN-boxed `uint64` 字段读写的硬约束([01](../p1-interpreter/01-value-object-model.md) §2「GCRef 低 3 bit 恒为 0」与 [06](../p1-interpreter/06-memory-gc.md) §1.1「`[]byte` 起始地址不保证 8 对齐,在某些平台读 `uint64` 会触发非对齐访问」)。wazero memory 以 64 KiB 页为单位,起始天然 64 KiB 对齐,远超 8 字节门槛——本约束自动满足。
3. **返回的 slice 元素初值必须为零**——arena 的 nullReserve(`words[0] = 0`)与 GCHeader 的 white 颜色字节都依赖此。wazero memory 新分配页的初值是 0(Wasm spec 要求),`memory.grow` 扩展的页同样零填充——本约束自动满足。
4. **slice 在 arena 不主动调 `setBacking` 之前,其内容稳定**——也即 `NewBacking` 返回的视图在 wazero memory 不发生 `memory.grow` 之前不被替换。P3 适配器把视图替换严格收口在 `setBacking` 路径(§1.6),由 grow 协议触发。

### 1.5 wazero memory adapter 实现骨架(P3 端)

`internal/gibbous/wasm/memadapter` 子包实代码骨架(P3 build 专属;PW1 完成):

```go
//go:build wangshu_p3
// 注:实际 build tag 名待 PW1 完成时定;暂以 wangshu_p3 占位。

package memadapter

import (
    "context"
    "fmt"
    "unsafe"

    "github.com/tetratelabs/wazero"
    "github.com/tetratelabs/wazero/api"

    "wangshu/internal/arena"
)

// MemoryHolder 持有「为 arena 当 backing 用」的 wazero Memory 与其所属 Module。
//
// 设计点:
//   - 一份 MemoryHolder 服务一个 State 的主 arena(P1 多 State 共 Program,但 arena 单 State 私有,
//     故 MemoryHolder 也单 State 一份;不试图让多 State 共 wazero Runtime 复用一块 memory——
//     那会破坏 arena 单线程 mutator 假设)。
//   - module 仅声明 memory,不带任何函数;真正的 gibbous 翻译产物经独立 module 加载,
//     经 import memory 共享这块 memory(详见 04-trampoline §2 入口协议与 §3.1 候选方案)。
type MemoryHolder struct {
    runtime wazero.Runtime
    module  api.Module
    memory  api.Memory
    maxPage uint32 // 上限页数,= ceil(opts.MaxBytes / 64KiB)
}

// New 构造 MemoryHolder,分配 initialBytes 字节的 linear memory(向上对齐到 64KiB 页)。
func New(ctx context.Context, runtime wazero.Runtime, initialBytes, maxBytes uint32) (*MemoryHolder, error) {
    initPage := ceilDiv(initialBytes, 65536)
    maxPage := ceilDiv(maxBytes, 65536)
    if maxPage > 65536 { // wasm32 上限 4 GiB = 65536 页
        return nil, fmt.Errorf("memadapter: maxBytes %d exceeds wasm32 4GiB", maxBytes)
    }

    // 经 BinaryFormat 构造一个仅 export memory 的 stub module。memory 直接 declare,
    // 不 import,确保 wazero 端持有 buffer。详细 wat 见 §1.5.1。
    bin := buildMemoryHolderModuleBinary(initPage, maxPage)
    compiled, err := runtime.CompileModule(ctx, bin)
    if err != nil {
        return nil, fmt.Errorf("memadapter: compile holder module: %w", err)
    }
    mod, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
    if err != nil {
        return nil, fmt.Errorf("memadapter: instantiate holder: %w", err)
    }
    mem := mod.ExportedMemory("memory")
    if mem == nil {
        return nil, fmt.Errorf("memadapter: holder module exports no memory")
    }
    return &MemoryHolder{runtime: runtime, module: mod, memory: mem, maxPage: maxPage}, nil
}

// Backing 返回 arena.BackingFn 适配器。
func (h *MemoryHolder) Backing() arena.BackingFn {
    return func(words uint32) []uint64 {
        h.ensureBytes(uint64(words) * 8)
        return unsafeBytesToWords(h.underlyingBuffer(), words)
    }
}

// ensureBytes 在 wazero memory 容量不足时 grow。本方法是 grow 协议的入口
//(详见 §1.6)。返回前 memory 容量 ≥ needBytes。
func (h *MemoryHolder) ensureBytes(needBytes uint64) {
    cur := uint64(h.memory.Size())
    if cur >= needBytes {
        return
    }
    needPage := ceilDiv(uint32(needBytes), 65536)
    curPage := uint32(cur / 65536)
    if needPage > h.maxPage {
        panic(fmt.Sprintf("memadapter: need %d pages exceeds max %d", needPage, h.maxPage))
    }
    delta := needPage - curPage
    if _, ok := h.memory.Grow(delta); !ok {
        panic(fmt.Sprintf("memadapter: memory.grow(%d) failed", delta))
    }
}

// underlyingBuffer 返回 wazero memory 的底层 []byte 引用。
//
// 待 spike 验证:wazero 当前版本 api.Memory 是否提供 UnsafeUnderlyingBuffer 或等价方法。
// 若不可用,fallback 是用 Read([]byte, offset uint32, byteCount uint32) 拷贝
// ——但拷贝形式会破坏「两层共见」物理基础,届时需切到 import memory 形式(§3.1)。
func (h *MemoryHolder) underlyingBuffer() []byte {
    // 占位:具体调用以 spike 结果为准。
    return h.memory.UnsafeUnderlyingBuffer()
}

// unsafeBytesToWords 把 []byte 视图别名为 []uint64。承 [06 §1.1] 双视图协议:
// 不要反向派生(从 []byte 派生 []uint64 在某些平台读 uint64 会触发非对齐访问,
// wazero memory 以 64 KiB 页对齐,起始地址保证 8 对齐)。
func unsafeBytesToWords(b []byte, words uint32) []uint64 {
    if uintptr(unsafe.Pointer(&b[0]))%8 != 0 {
        panic("memadapter: wazero buffer not 8-byte aligned")
    }
    return unsafe.Slice((*uint64)(unsafe.Pointer(&b[0])), int(words))
}

func ceilDiv(a, b uint32) uint32 { return (a + b - 1) / b }
```

#### 1.5.1 `buildMemoryHolderModuleBinary` 构造的 stub module 形式

```wat
;; (P3 stub module:仅 declare memory,不带函数)
(module
  (memory (export "memory") $INIT_PAGE $MAX_PAGE))
```

`$INIT_PAGE` 与 `$MAX_PAGE` 在编码 binary 时经常量替换;wazero 实例化此 module 时分配 `$INIT_PAGE * 64KiB` 的 linear memory,其底层 buffer 即 arena 的 backing 来源。

> **设计澄清**:这个 stub module 只是 wazero 持有 memory 的容器,不参与翻译。真正的 gibbous 翻译产物(每 Proto 一个 module,[02-translation](./02-translation.md) §1.1)经 `(import "wangshu" "memory" (memory $INIT_PAGE))` 共享同一块 memory(详见 §3.1 与 [04-trampoline](./04-trampoline.md) §2)。

### 1.6 `memory.grow` 协议:grow 后视图重取,偏移寻址不变

P1 form 的 grow 是 `make + copy`,新 backing 在 Go 堆新地址但 GCRef 是偏移、零修正([06](../p1-interpreter/06-memory-gc.md) §3)。P3 form 的 grow 经 `wazero api.Memory.Grow(delta uint32)` 走 Wasm 自身机制:

```
P1 grow(minBytes):
    newCap = nextPow2(minBytes)
    newWords = make([]uint64, newCap/8)
    copy(newWords, a.words)
    a.setBacking(newWords)   // bytes 视图从 newWords 派生

P3 grow(minBytes):
    newPage = ceil(minBytes / 64KiB)
    delta = newPage - currentPage
    api.Memory.Grow(delta)   // wazero 内部完成 buffer 扩展(可能重分配,可能就地扩,wazero 自管)
    newBuf = api.Memory.UnsafeUnderlyingBuffer()  // 重取!buffer 的 Go 端切片可能变了
    newWords = unsafeBytesToWords(newBuf, newPage*64KiB/8)
    a.setBacking(newWords)
```

**关键不变式(grow 前后)**:

1. **GCRef / freelist / sweep 链 / bump 一字不改**——因为它们都是字节偏移,与 buffer 的绝对地址无关。这是 §0.2 第 3 项的精确含义。
2. **Wasm 侧 i32 地址语义不变**——`memory.grow` 后旧地址在 Wasm 侧仍指向同一逻辑位置。Wasm spec 要求 `memory.grow` 保留已有内容,新增页清零。这与 P1 grow 的「`copy(newWords, a.words)` 后零填充新区」语义同构。
3. **Go 端视图必须重取**——`UnsafeUnderlyingBuffer` 在 grow 后可能返回不同 `[]byte`(底层 buffer 重分配)。**任何长寿 `[]uint64` / `[]byte` 视图必须在 grow 后立即作废**;crescent 解释器对 arena 的访问全部经 `arena.Words()` / `arena.Bytes()` 取当前视图,不缓存——这条纪律 P1 已完成(`internal/arena/arena.go` 内 helper 经 `a.words`/`a.bytes` 字段访问,不外泄长寿引用)。
4. **grow 时刻必须无并发读者**——grow 期间替换 backing 视图,期间任何并发读会观察到不一致状态。P1 单线程 mutator 模型下天然满足;P3 需注意 wazero memory.grow 与 Wasm 函数执行的并发关系(详见 §1.7 与 [01-spike-gate](./01-spike-gate.md) §4)。

### 1.7 `memory.grow` 的并发约束:待 spike 验证

P1 单 mutator 假设下 grow 永不与执行并发——grow 触发于 Alloc 失败,而 Alloc 必在 mutator 单线程上下文。P3 引入 wazero 后,问题升一档:

- **wazero 生成码内部能否触发 `memory.grow`**?P3 选「gibbous 代码自身从不分配」基线(p3-wasm-tier §6.1):NEWTABLE / CONCAT / CLOSURE / rehash 全经 imported 助手回 Go,分配与 GC 都发生在助手内。**故 Wasm 侧的 `memory.grow` 永不直接触发**——这是「不分配」决策的一个隐性收益。
- **Go 端 grow 期间 Wasm 函数是否在跑**?P1 已有的「每帧 Alloc 是 safepoint」语义([06](../p1-interpreter/06-memory-gc.md) §7)在 P3 延续:grow 发生在助手(Go 侧)内,而当时 wazero 函数已让出执行(经 imported 函数回到 Go)。**故 Go 侧 grow 与 Wasm 侧执行天然互斥**——这是 imported 函数边界协议的另一个隐性收益。
- **wazero `memory.Grow` 自身是否线程安全**?**待 spike 验证**(链 [01-spike-gate](./01-spike-gate.md) §4 顺带项)。预期是「单 module 实例不并发跑,grow 调用安全」——这是 wazero 文档承诺的常态。若 spike 揭示更强约束(如必须在特定 goroutine 调),需在 §8 缺口记录并调整 §1.6 的 grow 触发点。

> 一句话:P3 基线下 grow 的并发图景与 P1 同构(单 mutator,grow 在助手内、Wasm 已让出执行)——但这条结论依赖「gibbous 代码不分配」与「imported 函数同步返回」两条决策稳定;若未来突破(如 Wasm 内 inline 分配),需重新审视。

### 1.8 P1-only build 与 P3 build 的 backing 来源在编译期分流(build tag)

实代码层:`internal/arena` 包不依赖 wazero;`internal/gibbous/wasm/memadapter` 子包独立(P3 build tag 下编译进二进制)。`NewState` 的拼装路径分流:

```go
// internal/wangshu/state.go(伪代码,示意 build tag 分流)

//go:build !wangshu_p3
// (默认/P1 build)
func newArena(opts arena.Options) *arena.Arena {
    // opts.NewBacking 不设,走 arena.DefaultBacking。
    return arena.New(opts)
}

//go:build wangshu_p3
// (P3 build)
func newArena(opts arena.Options) *arena.Arena {
    holder, err := memadapter.New(ctx, sharedRuntime, opts.InitialBytes, opts.MaxBytes)
    if err != nil { panic(err) }
    opts.NewBacking = holder.Backing()
    return arena.New(opts)
}
```

> **build tag 名待定**:暂以 `wangshu_p3` 占位,PW1 完成时与 `internal/gibbous/wasm` 包的 build tag 同批确定(参考 [implementation-progress](./implementation-progress.md))。
>
> **实代码 zero-cost 原则**:P1-only build 下二进制不链接 wazero;P3 build 下 wazero 是必备依赖。这条分流让 P1 仅靠 Go 标准库即可运行(对宿主部署友好),也让 P3 不强迫 P1 用户接受 wazero 的二进制体积。具体 build tag 命名与 go.mod 拓扑待 PW1 完成。

---

## 2. 值编码两层逐位同一

### 2.1 NaN-box `uint64` 在两层逐位同一

[01-value-object-model](../p1-interpreter/01-value-object-model.md) §3 的 NaN-boxing 是 P3 阶段唯一被「物理共见」承诺约束的位级契约。本节给出位级证据:解释器侧的判定与 Wasm 侧翻译产物**逐位同一**。

**解释器侧(承 [01](../p1-interpreter/01-value-object-model.md) §3.2 与 §3.5):**

```go
// internal/value/value.go(已完成)
const qNanBoxBase = 0xFFF8_0000_0000_0000

func IsNumber(v Value) bool { return uint64(v) < qNanBoxBase }
```

判定的物理形式:**单次 `uint64` 无符号比较**,`v < 0xFFF8_0000_0000_0000`。

**Wasm 侧(翻译产物,承 [02-translation](./02-translation.md) §3.2 ADD 示例):**

```wat
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vc (i64.load offset=8*C (local.get $base)))
(if (i32.and  ;; IsNumber×2:01 §3.2 的单比较,Wasm 直译
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  ...)
```

判定的物理形式:**`i64.lt_u` 与同一立即数 `0xFFF8000000000000` 比较**——常量逐位同一,语义同一,wazero 编译后是单条机器比较指令。

**逐位同一的物理证据**:`0xFFF8_0000_0000_0000`(Go const)与 `0xFFF8000000000000`(Wat literal)是**同一 64-bit 整数**的两种文本编码;`v < 0xFFF8…` 与 `i64.lt_u v 0xFFF8…` 是同一无符号比较语义。两层不为 NaN-box 引入任何私有变体——这是 [01](../p1-interpreter/01-value-object-model.md) §7 不变式 6「可回收连续」与 [00-overview](./00-overview.md) §9 不变式 2「值编码/GCRef 两层逐位同一」的兑现。

### 2.2 GCRef = 48-bit 字节偏移:在 Wasm 侧就是 linear memory 地址

[01](../p1-interpreter/01-value-object-model.md) §2 与 §3.5:

```go
type GCRef uint64                            // 48-bit 字节偏移(高 16 位为 0)
const payloadMask = 0x0000_FFFF_FFFF_FFFF

func GCRefOf(v Value) GCRef                   { return GCRef(uint64(v) & payloadMask) }
func MakeGC(t uint16, ref GCRef) Value        { return Value(uint64(t)<<48 | uint64(ref)) }
```

Wasm 侧:GCRef 取出后**直接当 i32 linear memory 地址用**——因为单 arena ≤ 4 GiB(§2.3 详述),48-bit 偏移的高 16 位永远为 0,低 32 位即 wasm32 地址。在 [02-translation](./02-translation.md) 的翻译里:

```wat
;; 取出 v 的 GCRef(取低 48 位即可,但实际 ≤ 32 位有效):
(local.set $ref (i32.wrap_i64 (i64.and (local.get $v)
                                       (i64.const 0x0000FFFFFFFFFFFF))))
;; 作为 linear memory 地址直接寻址:
(local.set $hdr (i64.load (local.get $ref)))   ;; 读 GCHeader
(local.set $val (i64.load offset=8 (local.get $ref)))  ;; 读 word1
```

**两层互换零翻译**:解释器读「GCRef + 字段偏移」是 `arena.words[(ref>>3) + i]`;Wasm 侧是 `i64.load offset=8*i $ref`。两者寻址同一字节、读出同一 `uint64`、解出同一 NaN-boxed 值。**无序列化、无装箱、无影子结构**——「编译层是纯增量」(roadmap §3)在指令级的兑现。

### 2.3 容量匹配:arena 的 bump/cap = uint32、单 arena ≤ 4 GiB,恰好匹配 wasm32 寻址

[06](../p1-interpreter/06-memory-gc.md) §3:

> 上限受 GCRef 48-bit 与 `bump uint32`(本设计 4 GiB)约束——单 arena 最大 4 GiB(`bump`/`cap` 用 uint32)。

P1 当时定 `bump`/`cap` 为 `uint32`,有两个看似无关的理由:① 4 GiB 对实际负载够用;② uint32 比 uint64 在 32-bit 平台上原子操作便宜。**P3 揭示了第三个理由——隐性红利**:

- **wasm32 的地址空间恰好 4 GiB**(`i32` 偏移 + 64 KiB 页 × 65536 页);
- arena 上限 4 GiB 与 wasm32 容量 4 GiB **恰好匹配**——一个 arena 等于一个 wasm32 linear memory 的全部容量,无需「分段」或「切片」;
- GCRef 48-bit 偏移在单 arena 内永远 ≤ 32 位有效,Wasm 侧用 i32 地址直接寻址,**无需跨 arena 边界处理**。

`internal/arena/arena.go` 的实代码注释已经记录:

```go
// MaxBytes 是单 arena 容量上限。
MaxBytes uint32 = 1 << 31 // 2 GiB(留一半 headroom 防 uint32 边界溢出,06 §3)
```

P1 实际取 2 GiB(留 headroom 防 uint32 计算溢出);wasm32 上限 4 GiB——P1 实际容量永远在 wasm32 的安全寻址范围内。**P3 不需要为 arena 容量上限做任何调整**。

> **4 GiB 上限突破(wasm64 升级)留 P3+ / P5**:Lua 5.1 的实际负载(脚本 + 表 + 字符串)在 4 GiB 内常态;真正触发 wasm64 升级的是 P5 trace JIT 多 arena 共存或巨型表场景。本期 [00-overview](./00-overview.md) §10 与 §8 缺口仅记录,不展开方案。

### 2.4 NaN 规范化两层一致:canonicalizeNaN 在解释器与 Wasm 翻译中的位级同步

[01](../p1-interpreter/01-value-object-model.md) §3.4 不变式:

> 值世界中任何 NaN 数字必须是规范正 qNaN `0x7FF8_0000_0000_0000`。

**解释器侧的 helper**:

```go
const canonNaN uint64 = 0x7FF8_0000_0000_0000

func NumberValue(f float64) Value {
    if f != f { f = math.Float64frombits(canonNaN) }
    return Value(math.Float64bits(f))
}
```

**Wasm 侧(承 [02-translation](./02-translation.md) §3.2 ADD 翻译产物)**:

```wat
;; 算术结果 $r,canonicalizeNaN
(if (f64.ne (local.get $r) (local.get $r))   ;; NaN 检测:任何 NaN x 满足 x != x
  (then (local.set $r (f64.reinterpret_i64 (i64.const 0x7FF8000000000000)))))
```

**位级同步证据**:

- 解释器侧的 `canonNaN`(Go const) = `0x7FF8_0000_0000_0000`;
- Wasm 侧的 `i64.const 0x7FF8000000000000` = 同一 64-bit 整数;
- `f64.reinterpret_i64` 与 `math.Float64frombits` 在 IEEE-754 双精度上是**位级恒等**变换;
- 两层 NaN 检测都用 `f != f`(IEEE-754 唯一不自反值),wazero 翻译为 `f64.ne $r $r` 单条机器指令。

**为什么必须双侧规范**:负 NaN(`0xFFF8_0000_0000_0000` 段)与 NaN-box tag 段重叠([01](../p1-interpreter/01-value-object-model.md) §3.4)。若 gibbous 代码产生未规范的 NaN(如外部 `tonumber` 入口、宿主 `PushNumber` 漏 canonicalize),会被错误识别为 boxed value,触发 tag 误判。**P3 翻译器的硬约束**:任何产生 NaN 的算术(ADD / SUB / MUL / DIV / MOD / POW),翻译产物必须包含 canonicalize 步骤——这是 [02-translation](./02-translation.md) §3.2 的纪律红线之一,差分 fuzz([12](../p1-interpreter/12-testing-difftest.md))会逐字节兜底。

### 2.5 GCHeader 与对象布局两层同一

承 [01](../p1-interpreter/01-value-object-model.md) §4 与 §5,P3 不引入任何私有的对象布局或字段顺序——这是「Wasm 侧不引入任何私有值表示」(现稿 §10 不变式 2)的字段级展开。具体兑现:

| 对象 | word0 / word1 / ... 字段 | P3 翻译里的形式 |
|---|---|---|
| GCHeader([01](../p1-interpreter/01-value-object-model.md) §4) | `[7:0] otype \| [9:8] color \| [10] fixed \| [11] hasGCNext \| [15:12] flags \| [63:16] gcnext` | `i64.load $ref` 直接读;位字段提取经 `i64.shr_u`/`i64.and` 与解释器一样的 |
| Table 头([01](../p1-interpreter/01-value-object-model.md) §5.2) | `word0=hdr; word1=asize\|hmask; word2=arrayRef; word3=nodeRef; word4=metaRef; word5=lastfree\|gen` | `i64.load offset=8*N $tableRef` 直接读 |
| Closure([01](../p1-interpreter/01-value-object-model.md) §5.3) | `word0=hdr; word1=protoID\|nupvals; word2..=upvalRef` | upvalue 读经 `i64.load offset=16+8*i $closureRef` |
| Upvalue([01](../p1-interpreter/01-value-object-model.md) §5.4) | 开放/关闭两态,`word2` 在两态意义不同 | 翻译经助手回 Go(避免 Wasm 侧重复实现两态切换状态机,基线安全) |
| Thread / 值栈 / CallInfo([01](../p1-interpreter/01-value-object-model.md) §5.6) | 值栈 `Value[stackCap]` | 寄存器 = 共见栈槽,P3 基线 memory-resident([02-translation](./02-translation.md) §2.2) |

**Wasm 侧不会出现 P3 私有的对象布局或字段顺序**——这是 §7 不变式 4 的具体含义,也是 [02-translation](./02-translation.md) 的所有 opcode 翻译都用 `offset=8*N` 直接寻址的物理依据(N 为字段在对象内的字偏移,与解释器一样的)。

---

## 3. wazero memory 共享 API 细节(待 spike 验证)

§1 已给出实代码骨架,但 wazero 与外部 Wasm module 共享 memory 的具体形式有候选方案差异。本节展开两条候选(§3.1 / §3.2)、buffer 稳定性约束(§3.3)、多 State 共享 Program 时的拓扑(§3.4)、与 PW0 spike 的同时完成(§3.5)。

### 3.1 import memory vs 宿主读 module memory:两条候选方案对比

**问题陈述**:gibbous 翻译产物(每 Proto 一个 module)如何与 stub holder module 共享同一块 memory?

**候选方案 A:gibbous module `import` memory(P3 基线倾向)**

```wat
;; gibbous 生成 module
(module
  (import "wangshu" "memory" (memory $mem 1 65536))   ;; 共享 holder 的 memory
  (import "wangshu" "h_call"  (func $h_call ...))     ;; 慢路径助手
  (import "wangshu" "h_arith" (func $h_arith ...))
  ;; ...
  (func $proto_N (param $base i32) (result i32)
    ;; 翻译产物经 i64.load offset=8*K (local.get $base) 读取共享 memory 的栈槽
    ...))
```

特点:

- 共享 memory **由 wazero 显式建模**为 import,gibbous module 的所有 `i64.load` / `i64.store` 直接操作 holder 的 memory。
- 多个 gibbous module 各自 import 同一块 memory,wazero 内部经 module 链接保证物理同一。
- 一致性:Go 端 `holder.UnsafeUnderlyingBuffer()` 与 gibbous module 内 `i64.load` 读写**完全同一**,无任何中介。

**候选方案 B:gibbous module 内自带 memory + 宿主读 module memory(对照,本期不选)**

```wat
;; gibbous 生成 module
(module
  (memory (export "memory") 1 65536)   ;; 自带 memory,export 给宿主
  ...)
```

宿主拿 `gibbousModule.ExportedMemory("memory")` 操作。这条路有两个问题:

1. **每 Proto 一 module 时,N 个 module 各持一份 memory** —— 与「值世界共见」承诺直接冲突。
2. **数据无法在 gibbous module 间共享** —— gibbous→gibbous 互调时(§2.1 优化形式的批量编译)需经宿主中转,丧失 P3 优化空间。

**P3 基线选 A**(import memory)。理由:候选 B 在「每 Proto 一 module + 共见 memory」的组合下根本不可行,而我们的基线翻译单位就是「每 Proto 一 module」([02-translation](./02-translation.md) §1.1)。

> **待 spike 验证**:wazero 的 import memory 在跨 module 共享上是否完全无开销?spike S2(带参往返,[01-spike-gate](./01-spike-gate.md) §1.2)已包含 memory 读写一次的形状,顺带验证此点。若 wazero 的 import memory 引入了非预期的间接(如每次访问一次 trampoline),需切到 §3.2 的 fallback。

### 3.2 buffer 稳定性:`memory.grow` 后 wazero 是否保留旧 buffer / 必须重取

**核心问题**:Go 端缓存的 `[]byte` 视图(`UnsafeUnderlyingBuffer` 返回值)在 `memory.grow` 后是否仍指向 wazero 当前 memory?

**Wasm spec 语义**(确定):`memory.grow(delta)` 保留已有内容,新增页清零。从 Wasm 内部看,旧的 `i32` 地址在 grow 后仍指向同一逻辑位置(内容不变,容量增加)。

**wazero 实现层语义**(待 spike 验证):

- 候选 a:wazero 在 grow 时**就地扩展底层 buffer**(类似 `realloc` 但保留地址)。Go 端的旧 `[]byte` 视图地址不变,但 `len` 滞后。
- 候选 b:wazero 在 grow 时**分配新 buffer 并 copy**。Go 端的旧 `[]byte` 视图被作废(指向旧 buffer,不是当前 wazero 内部 buffer),必须重取。

P3 适配器在 §1.6 的设计**保守按候选 b 处理**——每次 grow 后立即 `setBacking(unsafeBytesToWords(newBuf))` 强制重取。这条策略在两种 wazero 实现下都正确(候选 a 下重取是 no-op);代价是 Go 端任何代码不能持有长寿 backing 视图,必须经 `arena.Words()` / `arena.Bytes()` 取——这条纪律 P1 已完成(§1.6 第 3 项),P3 延续即可。

> **风险标注**:若 spike 揭示 wazero 在 grow 时不暴露稳定的 `UnsafeUnderlyingBuffer`(返回的视图无法可靠映射到当前 buffer),需切到「`memory.Read([]byte, offset, count)` 拷贝」形式——但这会破坏「两层共见」物理基础。届时 P3 直接判失败、走 [01-spike-gate](./01-spike-gate.md) §1.4 的「跳跃路径」(直跳 P4,P3 整体不发)。

### 3.3 多 State 共享 Program 时,每 State 一份 wazero Runtime + Memory(arena 单 State 私有)

[11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §1 已定 P1 的并发拓扑:

> Program 不可变、可跨 goroutine 共享;State 含可变状态,每 goroutine 一个。

P3 build 下扩展为:

```
Program (Go 堆,跨 State 共享) ──┬──► State1 (主 arena1) ──► wazero Runtime1 ──► Memory1
                                ├──► State2 (主 arena2) ──► wazero Runtime2 ──► Memory2
                                └──► ...
```

**关键拓扑约束**:

1. **arena 与 wazero memory 是 1:1**:每个 State 持一份独立 wazero memory(经独立 Runtime 实例化 holder module)。State 之间的值世界完全隔离——[11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §8 的「State 不可跨 goroutine 并发」不变式延续到 wazero memory 层。
2. **gibbous module 编译产物在 Program 内共享**:每 Proto 一 module 的 wazero `CompiledModule` 在 Program 持有(类似 P2 的 `GibbousCode` 缓存,见 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6),实例化时与各 State 的 Runtime 绑定。**这意味着「编译一次 Proto,N 个 State 各实例化一次该 module」**。实例化开销随 State 数线性增长,但分摊到长寿 State 上可忽略(P2 编译预算计入,见 [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §5)。
3. **wazero Runtime 为何不跨 State 共享**:wazero Runtime 持有 module 实例化状态;若多 State 共享 Runtime,则 holder module + gibbous modules 都被多 State 同时拿——孤立保证(每 State 私有 memory + 私有 module 实例)被破坏。**单 Runtime per State 是最简一致拓扑**。
4. **wazero Runtime 复用 vs 新建**:工程上可让 `Program` 内嵌一个「编译-only」Runtime(只用于 `CompileModule`),实例化时再交各 State 的私有 Runtime。这是优化项,本期不展开;基线下每 State 一独立 Runtime,编译与实例化都本地完成。

### 3.4 P3 spike 在 PW0(本文)同时验证:memory 共享、grow 跨边界一致、NaN-box 读写一致

[01-spike-gate](./01-spike-gate.md) §4(spike 顺带项)与本文 §3 的待验证项汇总:

| spike 项 | 验证内容 | 失败处置 |
|---|---|---|
| import memory 跨 module 一致性 | gibbous module1 写、module2 读、Go 端读,三层观察同一字节 | 切候选 B 不可行,直跳 P4 |
| `UnsafeUnderlyingBuffer` 在 grow 前后行为 | grow 后旧 buffer 视图作废、新 buffer 视图覆盖当前容量 | 切「Read 拷贝」形式破坏共见,直跳 P4 |
| NaN-box 位级同一 | Go 端写 `0x7FF8_0000_0000_0000`、Wasm 端读 `i64.load` 得同值 | 几乎不可能失败(IEEE-754 + Wasm spec 双重保证),失败说明实现 bug |
| `memory.grow` 并发 | grow 不与 Wasm 函数执行并发(基线已断言),wazero 在单线程序列调用安全 | 若 wazero 要求跨 goroutine 串行,需在适配器加锁 |
| GCRef 偏移寻址 grow 不变 | grow 前 ref=0x1000 写,grow 后 ref=0x1000 读得同值 | 几乎不可能失败(Wasm spec 保证 grow 保留内容);失败说明 unsafe slice 派生 bug |

**spike 完成标志**:全部 5 项过 → §1 / §2 全部生效,本文不变式(§7)在 PW1 起被实代码兜底。任一失败 → 走 [01-spike-gate](./01-spike-gate.md) §1.4 的三种出路(开工 / 跳 P4 / 边缘混合)。

---

## 4. GC 根:零新增机制

### 4.1 基线 memory-resident 下,gibbous 帧的活跃寄存器就是 thread 值栈槽

[02-translation](./02-translation.md) §2.2 选基线 (A) memory-resident:Lua 寄存器 `R(i)` 在 Wasm 翻译里就是 `i64.load offset=8*i (local.get $base)` 与 `i64.store offset=8*i (local.get $base) ($value)`。物理上**就是** thread 值栈槽([01](../p1-interpreter/01-value-object-model.md) §5.6 valueStackRef → arena 内 `Value[stackCap]`)。

**关键观察**:解释器执行时,GC 根 R5([06](../p1-interpreter/06-memory-gc.md) §5.1)定义为「running thread 的值栈与 CallInfo」——这条根**完全覆盖**所有 Lua 帧的活跃寄存器(包括 crescent 帧与 gibbous 帧),因为两类帧的活跃值都在同一值栈上。

### 4.2 [06](../p1-interpreter/06-memory-gc.md) §5.1 R5 (running thread 栈 + CallInfo) 原样覆盖,根枚举代码一行不改

P3 不需要为 gibbous 帧新增任何 GC 根类:

| 解释器(P1)的 R5 形式 | gibbous 帧(P3)的形式 | 是否需要新增根? |
|---|---|---|
| Lua 帧的活跃寄存器在 thread 值栈 `[base, top)` | gibbous 帧的活跃寄存器在 **同一 thread 值栈** `[base, top)`(memory-resident 基线) | ❌ 不需要 |
| CallInfo 引用的 closure 在 R5 范围内 | gibbous 帧同样压 CallInfo,引用同一 closure(bit50 标识但不影响根扫描) | ❌ 不需要 |
| 临时根(R8,如 CONCAT 中间串)经 shadow stack 或 valueStack 兜住 | gibbous 代码不分配,无临时根需求(分配全在 imported 助手内,R7 shadow stack 兜底) | ❌ 不需要 |

**[06](../p1-interpreter/06-memory-gc.md) §5.1 的根枚举代码不需要任何修改**——P3 完成后 R1..R9 一字不改,gibbous 帧自动被 R5 覆盖。这是基线方案最大的正确性红利。

### 4.3 这是基线方案最大的正确性红利

P1 选 NaN-boxed `uint64` + 自管 arena 时,「值表示一次定死」是核心承诺;基线 memory-resident 是这条承诺在「寄存器=共见栈槽」维度的兑现。**回报**:

- **GC 根扫描代码零修改**——[06](../p1-interpreter/06-memory-gc.md) §5.1 / §5.2 / §5.3 的全部 mark 算法在 P3 阶段不动一行。
- **shadow stack(R7)语义不变**——host function 经 imported 助手回 Go 时,执行的是 Go 代码,shadow stack 协议与 P1 一样的([06](../p1-interpreter/06-memory-gc.md) §6)。
- **GC trigger 语义不变**——[06](../p1-interpreter/06-memory-gc.md) §7 的两类 safepoint(分配点 + 层边界)在 P3 [05-safepoint-gc](./05-safepoint-gc.md) 加入第三类(回边),但分配点完全在 imported 助手内,层边界(crescent↔gibbous trampoline)是天然 safepoint——三者都不要求改 mark 阶段。

> **对比方案 (B) locals 缓存**(若启用,[02-translation](./02-translation.md) §2.2):缓存进 Wasm locals 的值对 GC **不可见**。任何可能触发 GC 的点(全部助手调用、回边 safepoint 命中)之前必须写回栈槽,详见 [05-safepoke-gc](./05-safepoint-gc.md) §4。这是 (B) 方案不被基线选中的物理原因——它把「零新增机制」的红利吃掉了。

---

## 5. Go 堆侧资产不进 linear memory

### 5.1 Proto / 指令流 / host 注册表住 Go 堆,经整数 ID 引用

[01](../p1-interpreter/01-value-object-model.md) §1 已定 P1 划界:

| 类别 | 住哪 | 理由 |
|---|---|---|
| 动态可回收对象:String / Table / Closure / Upvalue / Userdata / Thread | **arena**(P3 起 = wazero linear memory) | 运行期被解释器与 gibbous 编译码**共同读写** |
| 不可变代码:`Proto`、指令流 `[]Instruction`、常量表容器 | **Go 堆** | 只在**编译期**被各 tier 读取(翻译为 Wasm/原生),运行期不再读 Lua 指令 |
| 宿主注册的 host function | **Go 堆**(函数注册表) | Go 闭包无法装进 arena;由整数 ID 引用 |

P3 阶段这条划界**不变**——具体地:

- **`Proto`**:Go struct,经整数 `ProtoID` 引用([01](../p1-interpreter/01-value-object-model.md) §5.7)。gibbous 编译产物**根本不读 Proto.Code**(已翻译完了),因此 Proto 不需要进 linear memory。
- **指令流 `[]Instruction`(uint32)**:Go slice,与 `Proto` 同生命期。gibbous 翻译产物把这些指令**编译时刻**消费完毕,运行期不再触碰。
- **host 函数注册表 `[]HostFn`**:Go slice,经整数 `HostFnID` 引用([01](../p1-interpreter/01-value-object-model.md) §1)。gibbous 调 host function 经 imported 函数(详见 §5.2 与 [04-trampoline](./04-trampoline.md) §3)。
- **`State.programStringRefs`**:Go map,Program 在每个 State 内的字符串常量 GCRef 表([01](../p1-interpreter/01-value-object-model.md) §5.7)。**Map 的键 / 值都是整数(`*Proto` 指针 / `Value`)**,GCRef 元素本身指向 arena 字符串对象;但 map 容器在 Go 堆。gibbous 编译产物**编译时刻**就把字符串常量的 GCRef 烧成立即数([02-translation](./02-translation.md) §3.2 LOADK 翻译),运行期不查 map。
- **`Program` / `State` 注册表**:Go struct,持各 Proto 指针、host function 指针、State 配置项。这些都不需要进 linear memory。

### 5.2 gibbous 不读 Lua 指令(已翻译),调 host 经 imported 函数

P3 翻译器把 Lua 字节码**翻译为 Wasm 指令**——gibbous 运行时,`Proto.Code` 的字节码**已经不再被读**;所有运行期决策(MOVE / ADD / GETTABLE / CALL / 等)都已经是 Wasm 直线代码([02-translation](./02-translation.md) §3)。

**调 host function 的形式**(详见 [04-trampoline](./04-trampoline.md) §3):

```wat
;; CALL A B C 的翻译产物(基线,统一经调度助手)
(local.set $st (call $h_call (local.get $base) (i32.const PC)
                             (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
```

`$h_call` 是 imported Go 函数(在 holder + gibbous module 链接时注入),由 Go 端实现「按被调者分派」(crescent 帧 / 已编译 gibbous 帧 / host fn,详见 [04-trampoline](./04-trampoline.md) §3 表)。**Wasm 侧只传 `base`/`PC`/`A`/`B`/`C` 几个 i32**——参数 / 返回值全经共见值栈,不经 Wasm linear memory 之外的中介。

**host function 注册表的位置**:Go 堆,经 `vm.hostFns []HostFn` 索引(`HostFnID` 整数)。`$h_call` 的 Go 实现:

```go
// internal/gibbous/wasm 端 imported 函数(Go 实现)
func hCall(ctx context.Context, m api.Module, base, pc, a, b, c uint32) uint32 {
    vm := vmFromContext(ctx)
    // 1. 读 R(A) 槽(共见 memory):callee value
    callee := vm.thread.ValueStack[base/8 + a]
    switch closureKind(callee) {
    case kindLuaTierGibbous:
        // 经 trampoline 调 wazero gibbousCode[id].fn.Call(ctx, newBase)
        return doCallGibbousFromGibbous(vm, base, a, b, c, pc)
    case kindLuaTierInterp:
        // 走 vm.execute (05 §7.3 fresh reentry)
        return doCallCrescentFromGibbous(vm, base, a, b, c, pc)
    case kindHost:
        // host fn 注册表查表,经 callHost (05 §7.6)
        return doCallHostFromGibbous(vm, base, a, b, c, pc)
    }
    return 1
}
```

**整套机制不要求 Proto / Code / hostFns 进 linear memory**——它们都在 Go 堆,Go 端 helper 经普通 Go 代码访问,Wasm 端只看 `base`/`PC` 等纯整数参数。

### 5.3 两层共见的范围精确等于运行期值世界

把上述划界与 §1-§2 合起来,**arena = wazero linear memory** 的物理共见范围**精确等于运行期值世界**:

```
linear memory 内(crescent / gibbous 共见):
    String / Table / Closure / Upvalue / Userdata / Thread 的 6 类对象
    Thread 的 valueStack[] 与 callInfo[]
    Table 的 array[] / node[] 附属块
    String 的内容字节
    Userdata 的 payload 字节

Go 堆内(crescent / gibbous 不共见,经整数 ID 桥接):
    Proto / Instruction / 调试信息(LineInfo, LocVars, Source)
    host function 注册表
    State.programStringRefs map(键值都是整数)
    句柄表(lightuserdata 间接索引)
    wazero Runtime / CompiledModule(P3 build)
```

**这两份划界与 [01](../p1-interpreter/01-value-object-model.md) §1 自洽**——P1 第 1 天就把「值世界」与「代码」分两套存放,P3 的 wazero 共见只覆盖前者,后者保持 Go 堆形式不变。**这是值表示一次定死的字节级落实**:P3 不发明新值类型,也不把已有非值类型(代码 / 注册表)硬塞进共见内存。

> **对照「Wasm 内 inline 分配」的反例**:若有人提议「让 gibbous 直接在 linear memory 内 bump 分配」,后果是:① 分配协议(bump + freelist)需要在 Wasm 侧重写一份;② GC 触发的 safepoint 协议需要新增「Wasm 侧分配点」一类;③ NEWTABLE / CONCAT / CLOSURE 的元方法逻辑需要全部用 Wasm 重写。这是把整个 [06](../p1-interpreter/06-memory-gc.md) 端的复杂度搬到 Wasm 翻译器里——P3 阶段一律拒绝(roadmap §5 原则 3「每阶段一块硬骨头」)。详见 p3-wasm-tier §6.4 与 [05-safepoint-gc](./05-safepoint-gc.md) §1.1。

---

## 6. wazero memory 形式下的特殊场景

值世界的 6 类对象([01](../p1-interpreter/01-value-object-model.md) §5)在 P3 共见 memory 形式下,大部分场景与 P1 同构。本节单列**容易让人误认为需要特殊处理**的几个场景,逐一说明 P3 不需要新机制。

### 6.1 string 长生命期:string GCRef 在两层都是 offset,intern 表 P1 已完成

**问题**:string 是高频对象(LOADK / 字符串拼接 / table 键 / pairs 遍历都涉及)。P3 共见 memory 下,string 的物理形式是否需要变更?

**回答**:不需要。

- **string 的对象布局**([01](../p1-interpreter/01-value-object-model.md) §5.1):`word0 = GCHeader; word1 = hash32 | len; word2.. = 内容字节 + NUL`。这套布局在 P3 build 下**完全不变**——内容字节就在 wazero linear memory,gibbous 翻译产物经 `i64.load offset=16 $stringRef` 直接读 `word2..`。
- **string intern**([06](../p1-interpreter/06-memory-gc.md) §9):`State.stringIntern` 是 Go 端 hash table,key 是 (hash32, content),value 是 GCRef。P3 build 下 string 对象的内容字节在 linear memory(经 wazero memory 共见),intern 表的查表元数据(hash32 / content 的指纹)在 Go 端缓存(不需要重新查)。**intern 协议本身不变**——gibbous 代码若要 intern 新字符串,经 imported 助手回 Go 走标准 intern 路径([06](../p1-interpreter/06-memory-gc.md) §9.3)。
- **Program 字符串常量 intern**([01](../p1-interpreter/01-value-object-model.md) §5.7 + §1.3):每 State 首次执行某 Program 时遍历 `Program.StringLits` 逐个 intern 进 State arena,得 GCRef 表 `State.programStringRefs`。这条逻辑在 P3 build 下**走同一路径**——arena 已经是 wazero memory,intern 写入的字符串对象就在共见 memory 内,gibbous 编译时直接读 `programStringRefs` 取 GCRef 烧成立即数。
- **string 是 mark 阶段的叶子**([06](../p1-interpreter/06-memory-gc.md) §5.2):标黑即止,无子引用要扫。P3 不动这条。

### 6.2 weak table:P1 已支持,gibbous 读写不破坏 weak 协议

**问题**:`__mode = "k"`/`"v"`/`"kv"` 的弱表在 GC 期间需要特殊扫描([07-metatables-metamethods](../p1-interpreter/07-metatables-metamethods.md) §13);gibbous 帧若在 GC 期间访问弱表,是否会破坏 weak 协议?

**回答**:不会。

**关键观察**:

- **GC 是 STW**(P1 / P3 都是,[06](../p1-interpreter/06-memory-gc.md) §6 / [05-safepoint-gc](./05-safepoint-gc.md) §6)。GC 触发时,gibbous 帧已经让出执行(经 imported 助手回到 Go,而 GC 在 Alloc 内调,见 [05-safepoint-gc](./05-safepoint-gc.md) §3)。**gibbous 与 GC 不并发**。
- **gibbous 读弱表**:GETTABLE 翻译的快路径([02-translation](./02-translation.md) §3.4)是「同表同代次直读 array/node」,与解释器快路径一样的([05](../p1-interpreter/05-interpreter-loop.md) §6.3)。读取的是 strong 引用——弱键 / 弱值的回收只在 GC 收割阶段处理,执行期间读出的引用都是当时存活的。
- **gibbous 写弱表**:SETTABLE 翻译走助手(基线非快路径,因为写表涉及 metatable 调用与 rehash 触发,基线一律保守);助手内是普通 Go 代码,与 P1 写弱表一样的。

**结论**:gibbous 帧只读 strong 引用,不直接参与 weak 收割——weak 协议完全在 GC mark/sweep 阶段处理,P3 翻译产物不破坏其语义。

### 6.3 finalizer:`__gc` 元方法触发由 GC 内部调,gibbous 不直接调 finalizer

**问题**:userdata 的 `__gc` finalizer 在对象死亡时触发([07-metatables-metamethods](../p1-interpreter/07-metatables-metamethods.md));gibbous 帧若在执行中遇到 finalizer 触发,是否需要特殊跨层协议?

**回答**:不需要。

- **finalizer 触发时机**:GC sweep 阶段发现弱可达 userdata 时,把对象塞入 finalize 队列;**GC 结束后**(或下次 yieldPoint 时,P1 实现策略[06](../p1-interpreter/06-memory-gc.md) §10)由解释器主循环调度执行 finalizer。
- **gibbous 帧的位置**:GC 触发于 imported 助手内(分配点),gibbous 帧已让出执行;GC 结束后控制权返回 imported 助手,继续返回 gibbous 帧——**finalizer 的执行不在 gibbous 帧内**,而是被 GC 调度回主循环单独执行(走 crescent 解释器,因为 finalizer 本身可能是任意 Lua 代码)。
- **gibbous 翻译产物不需感知 finalizer**:整套机制对 gibbous 透明——它只看到「分配可能慢一点(GC)」与「分配后对象正常存活」。

### 6.4 freelist 复用:arena freelist 在 wazero memory 上仍按 size class 复用,与 P1 形式零差异

**问题**:[06-memory-gc](../p1-interpreter/06-memory-gc.md) §2 的 size-class freelist 在 P3 共见 memory 下是否需要重新设计?

**回答**:不需要。

- **freelist 是 arena 内的侵入式单链**([06](../p1-interpreter/06-memory-gc.md) §2.2):回收的对象 `word0` 复用为「下一个空闲块的 GCRef」。链头存在 Go 端 `Arena.freeHeads [numSizeClasses]GCRef`,链节点的 `word0` 在 arena 内。
- **P3 build 下**:链头仍在 Go 端(`Arena` struct 字段),链节点 `word0` 在 wazero memory(共见 backing)。**freelist 的逻辑形式完全不变**——`freelistPop` / `freelistPush` 经 `arena.words[ref>>3]` 读写,P3 build 下这块即 wazero memory。
- **size-class 划分**([06](../p1-interpreter/06-memory-gc.md) §2.2):20 个定长桶 + LARGE 首次适配链,`internal/arena/freelist.go` 已完成。P3 build 下不动。
- **gibbous 是否操作 freelist**:**不操作**——gibbous 不分配,所有 `Alloc` / `freelistPop` 调用都在 Go 端的 imported 助手内。从 freelist 视角看,gibbous 是只读 mutator(只读 arena 内对象,不改 freelist)。

> **对比一个错误的设计倾向**:有人可能想「让 gibbous 在快路径 inline freelistPop」以省一次跨层。这违反「gibbous 不分配」基线,且需要在 Wasm 侧重写 freelist 协议——P3 阶段一律不做(同 §5.3 反例的判断)。

---

## 7. 不变式清单(实现与差分须守)

聚合现稿 §10 不变式 2/5 与本文 §1-§6 的隐式约束,P3 共见内存模型必须满足:

1. **arena = wazero memory(同一块物理内存)**——P3 build 下 arena 的 backing 由 wazero memory 提供;P1-only build 下走 Go 堆 `make`。两种 build 下 arena 接口形式同一(经 `arena.Options.NewBacking` 注入点分流)。
2. **NaN-box / GCRef 两层逐位同一**——解释器的 `IsNumber`/`canonNaN`/`GCRefOf` 与 Wasm 翻译产物的 `i64.lt_u 0xFFF8…`/`i64.const 0x7FF8…`/`i32.wrap_i64 (i64.and …)` 在位级相同。Wasm 侧不引入任何私有值表示。
3. **`memory.grow` 后偏移寻址不变(GCRef 永远有效)**——grow 仅扩展容量,既有内容保留;Go 端视图必须 `setBacking` 重取,但 GCRef / freelist / sweep 链 / bump 一字不改。
4. **wazero memory 不出现 P3 私有的对象布局 / 字段顺序**——所有对象布局沿用 [01](../p1-interpreter/01-value-object-model.md) §5,gibbous 翻译以同样的字偏移寻址。
5. **P1-only build 与 P3 build 的 arena 接口一致(`NewBacking` 注入点)**——P3 替换的只有 backing 来源,arena 包不依赖 wazero;build tag 分流仅在 `internal/wangshu` 拼装层。
6. **multi-State Program 跨 State 共享但每 State 一份 wazero Memory**——arena 单 State 私有不变,wazero Runtime / Memory 也单 State 私有,gibbous CompiledModule 在 Program 持有但每 State 各自实例化。
7. **gibbous 不分配:NEWTABLE/CONCAT/CLOSURE/rehash 全经 imported 助手回 Go**——这是「memory.grow 不与 Wasm 执行并发」与「freelist 不被 Wasm 直接操作」两条简化的基础;违反将连锁破坏 GC 协议。
8. **GC 根零新增**——R5(running thread 栈 + CallInfo)在基线 memory-resident 下覆盖 gibbous 帧的全部活跃寄存器;[06](../p1-interpreter/06-memory-gc.md) §5 的根枚举代码在 P3 阶段不动一行。

---

## 8. 文档缺口 / 回填请求

### 8.1 对 [06-memory-gc](../p1-interpreter/06-memory-gc.md) 的回填请求

**P1 已完成的注入点**(对应 [00-overview](./00-overview.md) §7 已完成义务表):

- ✅ `arena.Options.NewBacking` 注入点——P1 实代码已完成 `internal/arena/arena.go` 的 `BackingFn` 类型与 `Options.NewBacking` 字段([06-memory-gc](../p1-interpreter/06-memory-gc.md) §1.1 文档已记录),P3 PW1 完成时直接使用,无需 P1 再改。
- ✅ grow 协议沿用——P1 grow 用 `make + copy` 写在 §3,P3 经 §1.6 协议替换为 `memory.grow + 重取视图`,但偏移寻址不变的原则共享。

**P3 PW1 完成时验证**(对 06 的「实代码确认」回填请求):

- 验证项 1:`internal/arena/arena_test.go` 加 `NewBacking` 注入测试,确保替换 backing 后 arena.Alloc / grow / freelist 行为与 `DefaultBacking` 完全等价(byte-equal)。
- 验证项 2:在 P3 PW1 写一个最小 `memadapter` 适配,跑 P1 测试集(`internal/arena/...`、`internal/gc/...`),全 pass 才认 P3 build 下 arena 行为不变。
- 这两条验证不要求修改 06 文档,只是 PW1 的实现验收清单。

### 8.2 对 [11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) 的回填请求

**11 §3-§5 的 arena ABI**(类型化扁平列 / 字符串区 / presence bitmap)在 P1 build 下定义于 Go 堆 backing 上。**P3 build 下,这块 ABI 是否仍然成立?**

**答案:成立,且零拷贝读路径不变**——理由:

- 11 §3-§5 定义的「宿主写、VM 零拷贝读」契约只规定**字段二进制布局**(例如 float64 列起始偏移、字符串区编码、bitmap 位序),不规定 backing 物理来源。P3 build 下 backing 来自 wazero memory,但**布局完全不变**——宿主仍按 11 §3-§5 的格式往 arena 写,VM(crescent / gibbous)仍按同一格式读。
- 「零拷贝」的物理含义在 P3 下加强:不仅 crescent 经 `arena.bytes/words` 直接读,gibbous 也经 wazero memory 共见同一 buffer 直接读——**两层零拷贝**。
- 宿主代码(在 Go 端)经 `arena.Bytes()` / `arena.Words()` 取视图写;P3 build 下这些视图来自 wazero memory 的 `UnsafeUnderlyingBuffer`。**写完之后 wazero 内部就能直接读到**——无需任何同步或刷新(因为 buffer 物理同一)。

**回填请求**(给 11 §3-§5 的下一次更新):

- 加一段「P3 build 下 backing 来自 wazero memory,但 ABI 布局不变,零拷贝读路径成立」的旁注,引用本文 §1.2-§1.6 与 §5 划界。
- 11 §1.3 的「字符串常量惰性 intern」已经覆盖了多 State 共享 Program 的场景;P3 build 下每 State 一 wazero Memory 的拓扑(本文 §3.3)不冲突——`programStringRefs` 仍按 State 私有。

### 8.3 wazero memory.grow 并发约束待 spike 验证

§1.7 与 §3.2 已展开。要点:

- 基线下 grow 不与 Wasm 执行并发——理论上 wazero 单调用线性化即可。
- 待 spike(PW0 §4)验证:wazero 文档承诺 + 实测一致性。
- 若 spike 揭示更强约束,在 [implementation-progress](./implementation-progress.md) 与 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 记录。

### 8.4 4 GiB 上限突破(wasm64 升级)留 P3+ / P5

§2.3 已展开。要点:

- P1 实际取 2 GiB(留 headroom),wasm32 上限 4 GiB——P1 容量永远在 wasm32 安全寻址范围内。
- 真正触发 wasm64 升级的是 P5 trace JIT 多 arena 共存或巨型表场景。
- 本期不展开方案,在 [00-overview](./00-overview.md) §10 风险与未决缺口汇总记录。

### 8.5 wazero `UnsafeUnderlyingBuffer` 的版本演进风险

§1.5 已展开。要点:

- 当前依赖 wazero 的 `UnsafeUnderlyingBuffer` 或等价方法暴露底层 `[]byte`。
- 若 wazero 未来版本变更此 API(如重命名、签名调整、deprecate),P3 适配器需同步更新。
- PW0 spike 同时锁定 wazero 版本号,记录于 `go.mod` 与 [implementation-progress](./implementation-progress.md);P3 build 升级 wazero 版本前需先做兼容性 spike。

### 8.6 集中于 doc-gaps 的待办

总结上述要点,记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md):

| 缺口编号 | 内容 | 触发节点 |
|---|---|---|
| P3-MM-1 | wazero memory 共享 API 实测(§3.5 五项 spike) | PW0 |
| P3-MM-2 | `UnsafeUnderlyingBuffer` 在 grow 前后行为(§3.2) | PW0 |
| P3-MM-3 | wazero `memory.Grow` 并发约束(§1.7) | PW0 |
| P3-MM-4 | `arena.Options.NewBacking` 在 P3 适配下的 byte-equal 等价测试(§8.1) | PW1 |
| P3-MM-5 | wasm64 升级路径(§2.3,4 GiB 上限突破) | P3+ / P5 |
| P3-MM-6 | wazero 版本固定与升级 spike 协议(§8.5) | PW0 起常驻 |

---

## 9. 与下游文档的章节映射回顾

按任务点名的现稿 §章节映射,本文章节实现:

| 现稿 §章节 | 本文位置 | 内容 |
|---|---|---|
| §4 值世界 = linear memory:两层共见的物理兑现(主体,行 211-235) | §1-§3、§5-§6 | arena 收养 / 两层逐位同一 / wazero 共享 API / Go 堆划界 / 特殊场景 |
| §6.2 GC 根:零新增机制 | §4 | R5 原样覆盖,根枚举代码不动一行 |
| §10 不变式 2/5 | §7 | 聚合 8 条不变式 |
| §11 缺口前两条(wazero memory 共享 API + 对 06 回填) | §8 | 6 项 doc-gaps 子项 |

子文档之间的协同:

- [02-translation](./02-translation.md) 引用本文 §2 的 NaN-box 两层位级证据 + §4 的 GC 根基线作为「memory-resident 基线为何成立」的物理依据。
- [04-trampoline](./04-trampoline.md) 引用本文 §1 的 `base` 跨层语义 + §5 的「Wasm 侧只传 base/PC」物理形式作为入口协议的物理依据。
- [05-safepoint-gc](./05-safepoint-gc.md) 继承本文 §4 的 GC 根零新增结论,扩展 locals 缓存(若启用)的写回纪律。
- [06-ic-feedback-consume](./06-ic-feedback-consume.md) 的 IC 快照固化(`tableRef + gen + kind + index`)以本文 §2.5 的 Table 头布局两层同一为前提。
- [07-coroutine-thread-rule](./07-coroutine-thread-rule.md) 与本文 §6 的 weak/finalizer/freelist 处理共享「gibbous 不并发执行 GC」的基线假设。

---

## 10. 实现期 PW1 验证清单(与 [implementation-progress](./implementation-progress.md) 对账)

PW1 完成本文 §1 的 arena 收养机制时,验收清单(节选):

| # | 验证项 | 通过条件 |
|---|---|---|
| MM1 | `internal/gibbous/wasm/memadapter` 子包骨架 | 编译通过 + 单测通过(stub holder module 实例化) |
| MM2 | `arena.Options.NewBacking` 注入下 arena 行为 byte-equal | 跑 `internal/arena/...` + `internal/gc/...` 全部测试,P3 build 与 P1-only build 输出 byte-equal |
| MM3 | wazero memory 共见验证(三层观察同一字节) | Go 端写 + Wasm 端读 + Wasm 端写 + Go 端读,四组组合都得同字节 |
| MM4 | `memory.grow` 跨边界一致 | grow 前后 GCRef 0x1000 处 Value 不变 |
| MM5 | NaN-box 位级一致 | Go `0x7FF8_0000_0000_0000` 写 + Wasm `i64.load` 读 = 同 64-bit |
| MM6 | build tag 分流验证 | P1-only build 不链接 wazero 二进制(`go build -tags ''` 通过 + `nm` 检查) |

PW1 阻塞验证:bridge 当前 mock P3 装载形式在 PW1 用真 P3 占位(`SupportsAllOpcodes` 全 false)替换,验证「无任何 Proto 升层」与 P1-only 等价。本文 §1 的 backing 收养在此验证下首次实证生效——尽管 gibbous 翻译器还没产出任何代码,但 wazero memory 已经接管 arena backing,P1 测试集仍 byte-equal——这是 §7 不变式 5 的 PW1 实证。

---

相关:
[00-overview](./00-overview.md) ·
[01-spike-gate](./01-spike-gate.md) ·
[02-translation](./02-translation.md) ·
[04-trampoline](./04-trampoline.md) ·
[05-safepoint-gc](./05-safepoint-gc.md) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md) ·
[07-coroutine-thread-rule](./07-coroutine-thread-rule.md) ·
[implementation-progress](./implementation-progress.md) ·
../p3-wasm-tier ·
[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) ·
[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) ·
[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) ·
[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) ·
[../architecture](../architecture.md) ·
[../../../llmdoc/architecture/value-representation](../../../llmdoc/architecture/value-representation.md) ·
[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)


---





