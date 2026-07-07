# P1:arena 分配器 + mark-sweep GC + shadow stack

> 状态:**设计阶段,可实现深度**。本文把 [01-value-object-model](./01-value-object-model.md) 定下的
> arena 寻址、GCHeader 布局、6 类可回收对象,落实为**可据此实现**的分配器与回收器:
> 线性内存组织、bump+freelist 分配、string interning、stop-the-world mark-sweep 三色、
> shadow stack 根管理、safepoint、写屏障接口占位、finalizer、GC pacing。
> 上游不变式一律以 [01](./01-value-object-model.md) 为准(本文不重定义位布局)。
> 战略动因(自写 GC 是 NaN-boxing 的必付代价;safepoint 限分配点与层边界;根放 shadow stack)见
> `docs/design/roadmap.md` (§3) 与 [design-premises](../../../llmdoc/must/design-premises.md) 前提四。

对应 Go 包:`internal/arena`(线性内存 + 分配器)、`internal/gc`(mark-sweep、shadow stack、safepoint、写屏障接口)。
依赖 `internal/value`(GCRef / Value 编解码)与 `internal/object`(对象布局读写 helper)。

---

## 0. 本文在依赖图中的位置

[architecture](../architecture.md) §3 的底座是 `arena`(物理内存)+ `value`(值编码);`object` 在其上定义布局;
**`gc` 管理 arena 内对象生命周期**。本文是「值世界地基」的最后一块(构建顺序第 5 步,见 [architecture](../architecture.md) §5),
必须在写解释器主循环([05-interpreter-loop](./05-interpreter-loop.md))前自洽——否则解释器无处分配对象。

三条铁律(贯穿本文,源 [architecture](../architecture.md) §4 / `docs/design/roadmap.md` (§2)):

1. **GCRef 不是 Go 指针**——arena 内对象互引用是 48-bit 字节偏移,对 Go GC 是普通整数。这正是绕开「写屏障税」的物理手段:Go GC 看不到也不需要管 arena 内部对象图。
2. **代码不参与 mark-sweep**——`Proto`/指令流/host 函数住 Go 堆,经整数 ID 引用([01](./01-value-object-model.md) §1)。GC 只回收 arena 内的 6 类动态对象。
3. **GC 不改变可观察行为**——回收是透明的;但字符串 intern 哈希与表 rehash 影响 `pairs` 遍历序,这是**唯一**可被差分测试观察到的 GC 相关行为,§11 单列。

---

## 1. arena 线性内存的组织

### 1.1 双视图 backing(承 [01](./01-value-object-model.md) §2)

arena 是一段连续内存,同一底层 backing 经 `unsafe` 别名出两个视图:

```go
// internal/arena
type Arena struct {
    bytes []byte    // 字节视图:字符串内容、userdata payload、变长数据
    words []uint64  // 字视图:GCHeader、Value 字段(8 字节对齐);len(words)==len(bytes)/8

    bump   uint32   // bump 指针:下一个未分配字节的偏移(始终 8 对齐)
    cap    uint32   // 当前容量(字节);== len(bytes)
    // freelist / size-class 表见 §1.4
    // sweep 链表头、GC 统计见 §4 / §8
}
```

两视图的别名建立(实现注意):用一个 `[]uint64` 作为**真实 backing**(保证 8 字节对齐与 GC 友好的零值),
再用 `unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)` 派生 `bytes`。
**不要**反过来从 `[]byte` 派生 `[]uint64`——`[]byte` 不保证 8 对齐起始地址,在某些平台读 `uint64` 会触发非对齐访问。
backing 本身是 Go 堆上的 `[]uint64`,但**其元素是纯整数**(NaN-boxed Value 与偏移),不含 Go 指针,故 Go GC 扫描这块 slice 时不追踪任何内部引用——这是 arena 对 Go GC「不可见对象图」的兑现。

> **backing 来源抽象为注入点**(承 [../p3-wasm-tier](../p3-wasm-tier/03-memory-model.md) §1 回填请求):backing 的分配收口到一个可替换函数 `newBacking(minBytes uint32) []uint64`——P1 实现为 `make`;P3 替换为「收养 wazero linear memory 的 buffer」适配器(使 arena 与 Wasm 两层共见同一块内存)。§3 的 grow 同经此注入点。**P1 实现期就按此留口**,避免 P3 在固化的分配器里动手术。

### 1.2 偏移 0 的保留(null GCRef)

`GCRef == 0` 约定为 **null 引用**(等价 C 的 `NULL`),语义上「无对象」(如空表的 `nodeRef=0`、sweep 链尾 `gcnext=0`、
关闭态 upvalue 不指栈)。因此 **arena 起始 8 字节(words[0])永久保留不分配**:`bump` 初值 = 8。
这样任何真实对象的 GCRef ≥ 8,与 null 无歧义;同时 GCRef 低 3 bit 恒 0 的不变式([01](./01-value-object-model.md) §2)对 null(0)也成立。

> 注意区分:`GCRef==0`(无对象) ≠ `value.Nil`(`0xFFF8_..`,是一个 NaN-boxed 的 nil **值**)。
> 表槽里存的是 Value(可能是 `Nil`);对象布局里的「子对象引用字段」存的是裸 GCRef(可能是 0)。两者不在同一层。

### 1.3 8 字节对齐与对象尺寸

所有 GC 对象按 8 字节对齐分配(GCHeader 是 1 字,Value 字段是字,字符串/payload 末尾按字对齐填充)。
对象**总字节数**由 otype + 变长部分决定,统一向上取整到 8 的倍数。各类对象的字数计算(承 [01](./01-value-object-model.md) §5):

| otype | 固定头部(字) | 变长部分 | 总字数公式 |
|---|---|---|---|
| String | 2(header + hash/len) | 内容字节 + 1 NUL,按字对齐 | `2 + ceil((len+1)/8)` |
| Table | 6(header..lastfree) | array/node 另行分配(见下) | `6`(表头固定) |
| Table 的 array | 0 | `Value[asize]` | `asize` |
| Table 的 node | 0 | `Node[nsize]`,每 Node 3 字 | `3*nsize` |
| Closure(Lua) | 2(header + proto/nup) | `upvalRef[nupvals]` | `2 + nupvals` |
| Closure(Host) | 2 | `upval[nupvals]`(Value) | `2 + nupvals` |
| Upvalue | 3(header + 定位 + value) | — | `3` |
| Userdata | 4(header..env) | payload 字节,按字对齐 | `4 + ceil(payloadLen/8)` |
| Thread | 9(header..resumeFrom) | valueStack/callInfo 另行分配 | `9`(thread 头固定) |
| Thread 的 valueStack | 0 | `Value[stackCap]` | `stackCap` |
| Thread 的 callInfo | 0 | `CallInfo[ciCap]`,每帧 k 字(见 [05](./05-interpreter-loop.md)) | `k*ciCap` |

**关键:Table 与 Thread 是「头 + 多个独立子分配」的复合结构。** 表头(6 字)、array 段、node 段是**三次独立 Alloc**,
各自有 GCRef,但 array/node 段**没有自己的 GCHeader**——它们是表头的私有附属物,不进 sweep 链、不被独立标记。
mark 阶段标记表头时,顺带遍历 array/node 内的 Value(见 §5.2)。sweep 回收表头时,array/node 段一并回收(其字节归还 freelist)。
Thread 的 valueStack/callInfo 同理。这是「逻辑对象」与「物理分配块」不是一一对应的体现,实现 Alloc/Free 时要分清:

- **带 GCHeader 的「头对象」**(String/Table/Closure/Upvalue/Userdata/Thread):进 sweep 链,被三色标记。
- **无 GCHeader 的「附属块」**(array/node/valueStack/callInfo):由头对象持有,跟随头对象的生命周期,**不**独立入链。

> 为什么不给 array/node 也加头?省一字开销 + 避免 sweep 链过长 + 它们永远只被唯一头对象引用(无共享),
> 跟随回收最简单。Lua 5.1 `ltable.c` 亦如此(array/node 是 Table 的内部 `luaM_*` 分配,不是独立 GCObject)。

---

## 2. 分配器:bump 指针 + size-class freelist

### 2.1 设计:bump 为主,freelist 复用

分配走两级:

1. **freelist 命中**:若请求字数落在某 size-class 且该 class 的 freelist 非空,弹出复用(O(1))。
2. **bump 分配**:freelist 落空,从 `bump` 处线性切走 `words*8` 字节,`bump += words*8`。
3. **bump 越界**:`bump + need > cap`,触发 **GC**(§7);GC 后仍不足则 **grow arena**(§3)再分配。

```go
// internal/arena —— 核心分配入口
//
// otype: 写入 GCHeader 的 OBJType(见 01 §4),用于 sweep 时堆遍历识别。
// words: 对象总字数(含 GCHeader,已按 §1.3 算好并 8 对齐)。
// 返回新对象的 GCRef(字节偏移);保证 words[ref>>3]..words[ref>>3+words-1] 可写。
//
// 关键约定:Alloc 是**唯一的 safepoint 候选点**(见 §7)。调用方必须保证调用前所有
// 活跃 Value 都从根可达(在 Lua 栈上,或已 push 进 shadow stack),因为 Alloc 内部可能触发 GC。
func (a *Arena) Alloc(otype value.OBJType, words uint32) value.GCRef {
    need := words * 8
    sc := sizeClass(words)
    if ref := a.freelistPop(sc); ref != 0 {
        a.initHeader(ref, otype)        // 写 GCHeader:otype + 当前白 + 清 gcnext
        a.linkSweep(ref)                // 挂入 sweep 链
        return ref
    }
    if a.bump+need > a.cap {
        a.gc.maybeCollect(need)         // 触发 GC(STW);见 §7
        if a.bump+need > a.cap {        // GC 后仍不足
            a.grow(a.bump + need)       // realloc;偏移不失效,见 §3
        }
    }
    ref := value.GCRef(a.bump)
    a.bump += need
    a.initHeader(ref, otype)
    a.linkSweep(ref)
    return ref
}
```

`initHeader` 把新对象 color 置为**当前分配白**(allocator white,§4.3),并清 `gcnext`;`linkSweep` 把对象挂到 sweep 全链头部
(`obj.gcnext = sweepHead; sweepHead = ref; obj.hasGCNext=1`)。新生对象天然是白色(未标记),若本轮 GC 已过 mark 阶段(STW 下不会发生,见 §7),需特别处理——STW 模型里 mark 与 mutator 不交错,无此问题。

### 2.2 size-class 划分

回收频繁的小对象(String、Node 组、小 Table 头、Upvalue)用 size-class freelist 复用,减少 bump 区碎片。
size-class 按**字数**分桶(不是字节;字数已 8 对齐天然粒度):

```
sizeClass(words):
  words ∈ [1..8]     → class = words-1        (1..8 字,逐字一桶,共 8 桶)
  words ∈ [9..16]    → class = 8 + (words-9)/2 (步长 2 字)
  words ∈ [17..32]   → class = 12 + (words-17)/4
  words ∈ [33..64]   → class = 16 + (words-33)/8
  words > 64         → LARGE(不进 freelist,见 §2.4)
```

每个 size-class 维护一个 freelist 头(arena 内的侵入式单链:回收的对象 word0 复用为「下一个空闲块的 GCRef」)。
**freelist 复用 word0 作 next 指针**——回收对象已死,其 GCHeader 内容不再有意义,可被覆盖为 freelist 链指针;
`freelistPop` 弹出时再由 `initHeader` 重写 word0 为新 GCHeader。非满桶的 size-class 把请求向上取整到桶的代表字数
(略微浪费,但保证同桶对象尺寸兼容复用)——故 freelist 复用要求**桶内尺寸统一**,实现时按桶代表字数分配。

> P1 简化:freelist 仅在 sweep 阶段回填(把白对象按 size-class 串入对应桶)。不做空闲块合并(coalescing)、
> 不做跨 class 切分。碎片由「bump + 偶尔 grow」吸收。增量/分代 GC(P3+)再评估是否需要 coalescing。

### 2.3 freelist 与 bump 的协同

一个 size-class 的内存来源有二:sweep 回填的死对象、bump 区从未分配过的处女地。分配优先 freelist(复用,热缓存友好),
落空才 bump。**sweep 后** freelist 被各 size-class 的死对象填满,后续分配大量命中 freelist,bump 增长放缓——
这是「回收后复用」的兑现:稳态下 arena 不必无限 grow,死对象空间被 freelist 循环利用。

### 2.4 大对象(LARGE)

字数 > 64(512 字节)的对象(大字符串、大 table 的 array/node 段、大 userdata payload、thread 值栈)
**不进 size-class freelist**(否则大块碎片囤积浪费)。大对象仍从 bump 区线性分配,sweep 回收时:

- P1:大对象死后其字节**暂不回收进 freelist**,留待整体 grow 的 realloc 时被压缩——但 P1 不做压缩(见 §3),
  故 P1 大对象死后空间**就地浪费到下次 full GC 后 compaction**。为避免 P1 大对象空间无限泄漏,P1 采取折中:
  **大对象也按精确字数串入一个「LARGE freelist(按字数排序的简单 free 块列表,首次适配)」**,sweep 回填,分配时首次适配复用。
  这是 P1 唯一的变长 freelist;小对象走 size-class 定长桶。
- 首次适配(first-fit)足够:大对象分配频率低,链表短,O(n) 扫描可接受。

> 决策记录:P1 不做 compaction(对象搬迁需重写所有指向它的 GCRef,等于实现「栈移动税」的 arena 版,P1 不付)。
> 偏移寻址使 compaction **未来可行**(搬迁后批量重写偏移,无需 Go 指针修正),留给 P3+ 增量 GC 评估。见 §10 缺口。

---

## 3. arena 扩容(grow):偏移寻址的额外红利

bump 越界且 GC 后仍不足时,整体扩容:

```go
func (a *Arena) grow(minBytes uint32) {
    newCap := a.cap * 2
    for newCap < minBytes { newCap *= 2 }   // 翻倍直到够用
    newWords := make([]uint64, newCap/8)
    copy(newWords, a.words)                   // 复制既有内容
    a.words = newWords
    a.bytes = unsafe.Slice((*byte)(unsafe.Pointer(&newWords[0])), len(newWords)*8)
    a.cap = newCap
    // freelist 头、sweep 链头、bump 全是「偏移」,无需修正!
}
```

**这是选偏移寻址(而非 Go 指针)的额外好处,点明:** grow 是 `make + copy` 的 realloc,新 backing 在 Go 堆的**新地址**。
若 arena 内对象用 **Go 指针**互引用,realloc 后全部指针失效,必须遍历整个对象图重写——这正是「栈移动税」的内存版灾难。
但望舒用 **48-bit 字节偏移**:对象 A 引用对象 B 存的是「B 距 arena 起点的字节数」,与 backing 的绝对地址无关。
realloc 后偏移语义不变(`words[ref>>3]` 自动指向新 backing 的同一逻辑位置),**所有 GCRef、freelist 链、sweep 链、bump 指针一字不改**。
这把「整体扩容」从 O(对象数) 的指针修正降为 O(字节数) 的一次 memcpy,且无正确性风险。

> 呼应 [01](./01-value-object-model.md) §2「GCRef 不是 Go 指针……这正是绕开写屏障税的物理手段」——
> 偏移寻址的设计初衷是规避写屏障税,grow 免修正是顺带白赚的第二个好处。

扩容策略:**翻倍**(`cap *= 2`),摊还 O(1)。初始容量可配(默认如 64 KiB);上限受 GCRef 48-bit 与 `bump uint32`(本设计 4 GiB)约束——
单 arena 最大 4 GiB(`bump`/`cap` 用 uint32)。超 4 GiB 需求(罕见)留待把 bump/cap 升 uint64 + GCRef 用满 48-bit(256 TiB),P1 不做。

---

## 4. GCHeader 与三色 / 双白机制(承 [01](./01-value-object-model.md) §4)

### 4.1 GCHeader 位布局回顾(不重定义,仅引用)

[01](./01-value-object-model.md) §4 已定 word0:
`[7:0] otype | [9:8] color | [10] fixed | [11] hasGCNext | [15:12] flags | [63:16] gcnext(48-bit 偏移)`。
本文据此实现标记与清扫。访问 helper(`internal/gc` 或 `internal/object`):

```go
func headerOf(a *Arena, ref value.GCRef) uint64        { return a.words[ref>>3] }
func setHeader(a *Arena, ref value.GCRef, h uint64)    { a.words[ref>>3] = h }
func colorOf(h uint64) uint8                            { return uint8(h>>8) & 0x3 }
func setColor(h uint64, c uint8) uint64                 { return h&^(0x3<<8) | uint64(c)<<8 }
func isFixed(h uint64) bool                             { return h&(1<<10) != 0 }
func gcnextOf(h uint64) value.GCRef                     { return value.GCRef(h >> 16) }
```

### 4.2 三色定义([01](./01-value-object-model.md) §4 的 color 编码)

color 2 bit,4 值:`0=white0, 1=white1, 2=gray, 3=black`。三色不变式标准语义:

- **白(white0/white1)**:未被本轮标记触达;mark 结束仍为白 ⇒ 不可达 ⇒ sweep 回收。
- **灰(gray)**:已触达但其子引用尚未全部扫描(在 mark 工作集 / gray stack 中)。
- **黑(black)**:已触达且子引用已全部扫描(存活,本轮不再处理)。

### 4.3 双白(white0/white1)为什么需要——为未来增量 GC 预留

P1 是 stop-the-world(§6),**理论上单白就够**(一轮 mark-sweep 内,白=死、非白=活)。但 [01](./01-value-object-model.md) §4 预留了双白,本文沿用,理由是**演进友好**(`docs/design/roadmap.md` (§4) 倍率演进):

未来增量 / 分代 GC 中,mark 与 mutator **交错**运行。设想单白:一轮 GC 进行中,mutator 新分配的对象若标白,
会被本轮 sweep 误回收(它根本没机会被标记)。**双白机制**解决之:

- 维护一个全局 `currentWhite ∈ {white0, white1}`,每轮 GC 结束时**翻转**(white0↔white1)。
- **本轮的「死白」= 上一轮的 currentWhite**(即翻转前的白,称 *otherWhite*);**新生对象标当前白(currentWhite)**。
- sweep 只回收 *otherWhite* 颜色的对象;当前白对象(本轮新生的、或上轮存活翻色的)被跳过、留到下轮。
- 这样 mutator 在 mark 进行中新分配的对象(标 currentWhite)**不会被本轮 sweep 误杀**——它颜色不等于死白。

P1 STW 下无交错,但仍实现翻转逻辑:每轮 GC 后 `currentWhite ^= 1`。这让 P1 的 sweep 代码与未来增量 sweep **同构**,
切换到增量时只需补写屏障(§9)与 mark 增量化,sweep 逻辑不动。代码层面:

```go
// internal/gc
type Collector struct {
    arena        *Arena
    currentWhite uint8   // 0 或 1;新分配对象的白色
    grayStack    []value.GCRef  // mark 工作集(P1 用显式栈,见 §5)
    // 根、shadow stack、finalize 队列、pacing 见后续节
}

func (c *Collector) allocWhite() uint8  { return c.currentWhite }            // initHeader 用
func (c *Collector) deadWhite() uint8   { return c.currentWhite ^ 1 }        // sweep 回收此色
func (c *Collector) flipWhite()         { c.currentWhite ^= 1 }              // 每轮 GC 末
func isDead(c *Collector, h uint64) bool { return colorOf(h) == c.deadWhite() } // 仅未标记的旧白
```

> **fixed 位**([01](./01-value-object-model.md) §4 bit10):标 `fixed` 的对象**永不回收**,sweep 跳过且不变色。
> 用于被 Proto 常量永久引用的字符串(可选优化:与其每轮当根扫,不如 intern 时标 fixed,sweep 直接跳过)。
> P1 是否启用 fixed 见 §5.1 决策。

---

## 5. mark 阶段:从根三色标记

### 5.1 根集合的完整枚举(本文定稿,05/12 引用)

GC 根 = **不经其它对象就能被程序直接触达的 arena 对象**。漏掉任一类 ⇒ 误回收存活对象(灾难)。完整枚举:

| # | 根来源 | 持有处 | 标记什么 | 备注 |
|---|---|---|---|---|
| R1 | **全局表**(globals / `_ENV`) | `State.globals GCRef` | 该 Table | Lua 5.1 全局环境;经 registry 或 State 字段持有 |
| R2 | **registry 表** | `State.registry GCRef` | 该 Table | C/host 侧存放引用的特殊表(`LUA_REGISTRYINDEX`);本身是强根 |
| R3 | **主线程**(main thread) | `State.mainThread GCRef` | 该 Thread(连带其栈/CallInfo,见 §5.2) | 主协程 |
| R4 | **所有活跃 thread** | 由 R3 经 resume 链 / 被引用可达,**或** 显式 running-thread 寄存器 | 各 Thread | 当前 running thread 必须当根(见下) |
| R5 | **当前 running thread 的值栈与 CallInfo** | running Thread 的 valueStackRef / callInfoRef | 栈上 `[0,top)` 的所有 Value + 各 CallInfo 引用的 closure/Value | **解释器执行现场**;§5.2 详述 |
| R6 | **Program 字符串常量在该 State 的 intern 表** | `State.programStringRefs[*Proto]` —— 每张表是该 Program 中所有字符串字面量在本 State arena intern 后的 GCRef(承 [01](./01-value-object-model.md) §5.7 字符串惰性 intern 决策) | 各字符串常量的 String GCRef | M8 起 `Proto.Consts` 不直接持字符串 GCRef(那样跨 State 无法共享 Program);本表是 State 私有的字符串解析缓存,字符串字面量的 GC 根在此 |
| R7 | **shadow stack** | `Collector.shadowStack`(host fn 执行期临时持有的 arena 引用) | 各登记的 GCRef / Value | §6 详述;host 在 Go 栈上持有的引用,GC 看不见,靠此登记 |
| R8 | **临时根 / 构造中的对象** | 解释器在多步构造(如 CONCAT 中间串、表构造中途)持有的尚未挂入任何对象的引用 | 这些临时 GCRef | 见 §7 safepoint 一致性;多数情况落在 R5(已在栈上)或 R7 |
| R9 | **per-type 元表槽** | `State.typeMetatables[9]`([07](./07-metatables-metamethods.md) §1.2:string 公共元表、debug.setmetatable 设的各类型元表) | 各槽指向的 metatable Table(非 0 槽) | 承 07 回填请求:不当根则 string 元表(挂着 string 库)会被误回收 |

**为什么 R5「值栈/CallInfo 本身在 arena 且被 GC 直接当根扫描」而 R7 shadow stack 另设——澄清(任务点名):**

- Thread 的 valueStack/callInfo **物理上住 arena**([01](./01-value-object-model.md) §5.6)。解释器执行 Lua 代码时,
  所有活跃寄存器值 = `valueStack[base..top)` 的槽,**它们已经在 arena 里**。GC 标记 Thread(R3/R4)时,
  顺着 valueStackRef 遍历栈槽(§5.2),自然覆盖所有活跃寄存器——**无需 shadow stack**。
  这是「Lua 栈即根」:解释器的工作集天然在被扫描的 arena 栈里。
- **shadow stack(R7)只为一种情况存在**:**host function(Go 写的 stdlib / 宿主回调)执行期间**,
  host 代码可能把某个 arena 对象的 GCRef 暂存在 **Go 局部变量**(Go 栈上)。此时若该对象不在任何 Lua 栈槽 / 表 / 全局里,
  它对 GC **不可达**(GC 不扫 Go 栈里的 arena 引用——GCRef 是整数,Go 精确栈扫描只认 Go 指针,看不出这是 arena 引用)。
  若此刻触发 GC,该对象会被误回收,host 代码随后用悬垂 GCRef 访问到已回收/已复用的内存 ⇒ 崩溃或脏读。
  **shadow stack 让 host 代码显式登记「我正持有这个 arena 引用」**,把它纳入根集合,防误杀。详见 §6。

> 一句话区分:**Lua 代码的活跃值靠「栈在 arena」被自动当根;host(Go)代码的活跃值靠「显式 push 进 shadow stack」当根。**
> 这是 `docs/design/roadmap.md` (§3)「根放 shadow stack」的精确含义——shadow stack 补的是 GC 精确栈扫描**看不见 arena 引用**的盲区
> (四项税的「GC 精确栈扫描」税在我们这儿的具体形式:Go 能精确扫 Go 栈,但 arena 引用是整数,Go 扫不出,VM 自己补)。

**fixed 串决策(R6 的优化):** R6 要求每轮 GC 都遍历每个 State 的 `programStringRefs` 标记字符串常量。两种实现:

- **(A)纯根扫描**:每轮 mark 时遍历 `state.programStringRefs`,标记每个 GCRef。简单,但 Program 多时每轮 O(字符串总数)。
- **(B)intern 时标 fixed**:字符串首次为某 Program 常量 intern 时,在其 GCHeader 标 `fixed`(§4.4),sweep 永久跳过,**无需每轮根扫**。
  代价:Proto 卸载(P1 无此操作,Proto 与 State 同生命周期)时这些串不随之回收——P1 可接受(Proto 不卸载)。

**P1 定稿:走 (A) 纯根扫描**(简单、与「Proto 不卸载」假设解耦、未来支持 Proto 卸载时无需改 sweep)。`fixed` 位**P1 保留但不使用**
(留给 §10 评估:若 Program 字符串常量很多导致根扫描成 mark 热点,再切 (B))。这与 [01](./01-value-object-model.md) §4「Proto 引用的常量串可标 fixed」是**可选**优化的措辞一致(「可标」非「必标」)。

### 5.2 各对象类型要扫的 GCRef 字段(本文定稿,逐类给出)

mark 的核心是「从一个对象出发,找出它引用的所有子 arena 对象,把它们标灰」。下表对 [01](./01-value-object-model.md) §5 的每类对象,
列出**要遍历的子引用字段**(只列指向 arena 对象的 GCRef / 含 GCRef 的 Value;整数 ID / 标量字段不扫):

| otype | 要扫的字段(承 [01](./01-value-object-model.md) §5 布局) | 说明 |
|---|---|---|
| **String** | **无** | 字符串是叶子,内容是字节,不引用任何对象。标黑即止 |
| **Table** | ① array 段 `Value[asize]` 每槽(若 `IsCollectable`)② node 段每 Node 的 `key`、`val`(若 `IsCollectable`)③ `metaRef`(若非 0)。**弱表例外**:先经 `WeakMode()` 判 `__mode`,弱侧不标记并登记 weakList(§8.4,承 07 §13) | array/node 是附属块(§1.3),由表头顺带遍历;遍历 node 全部 `nsize` 槽(含空槽,空槽 key/val 为 Nil 不可回收,跳过) |
| **Closure(Lua)** | `upvalRef[nupvals]` 每个(指向 Upvalue 对象) | flags bit0=0;protoID 是整数 ID,**不扫** |
| **Closure(Host)** | `upval[nupvals]` 每个(是 Value,若 `IsCollectable`) | flags bit0=1;hostFnID 是整数 ID,**不扫**;host upvalue 是直接 Value 非 Upvalue 对象 |
| **Upvalue** | ① `value`(word2,关闭态存值;若 `IsCollectable`)② 开放态:其指向的栈槽**不在此扫**(随 Thread 栈扫到) | 开放 upvalue 的「值」逻辑上是 `thread.stack[idx]`,该槽由 Thread 标记覆盖,避免重复;仅关闭态扫 word2 |
| **Userdata** | ① `metaRef`(若非 0)② `envRef`(若非 0) | payload 是不透明字节,**不扫**(宿主若在 payload 藏 GCRef,须自行经句柄表,不走 GC) |
| **Thread** | ① 值栈 `valueStack[0..top)` 每槽(若 `IsCollectable`)② CallInfo 数组 `[0..ciTop)` 每帧引用的 closure GCRef + 帧内保存的 Value ③ openUpvalRef 链(每个开放 Upvalue 对象)④ `resumeFrom`/caller thread ref(若非 0) | **最复杂**;栈只扫 `[0,top)`(top 之上是垃圾槽,不扫);CallInfo 帧结构见 [05](./05-interpreter-loop.md),含被调 closure 引用 |

**遍历原则:**
- 只对 `value.IsCollectable(v)`(tag ∈ `[0xFFFB,0xFFFF]`,[01](./01-value-object-model.md) §3.3)的 Value 取其 GCRef 并标灰;
  number / nil / bool / lightuserdata **不是对象,跳过**(`isCollectable` 区间判定是 mark 的快门,[01](./01-value-object-model.md) §7 不变式 6)。
- 子引用字段是裸 GCRef(如 metaRef)时,`!=0` 即标灰(0 是 null)。
- **String 是唯一的叶子类型**(无出边),标黑后无需入 gray stack 再处理——可优化为标记即黑(见 §5.3)。

### 5.3 mark 的工作算法(STW 下的简化三色)

P1 STW 用**显式 gray stack**(`Collector.grayStack []GCRef`)做迭代式标记(非递归,避免深对象图爆 Go 栈):

```
mark():
  grayStack 清空
  // 1. 标记所有根为灰(入 gray stack)
  for each root r in {R1..R9}:        // §5.1 枚举
      markValue(r)                     // 若可回收且当前是死白,置灰入栈
  // 2. 处理 gray stack 至空
  for len(grayStack) > 0:
      ref := grayStack.pop()
      h := headerOf(ref)
      setColor(h, BLACK); setHeader(ref, h)   // 出栈即黑
      scanObject(ref)                  // 按 §5.2 表遍历子引用,对每个子 markValue

markValue(v Value or GCRef):
  if 不可回收: return                  // number/nil/bool/light 或 null GCRef(0)
  ref := GCRefOf(v)
  h := headerOf(ref)
  if isFixed(h): return                // fixed 对象不参与(若启用)
  if colorOf(h) != deadWhite(): return // 已灰/黑/或当前白(已处理或新生),跳过
  setColor(h, GRAY); setHeader(ref, h)
  grayStack.push(ref)

scanObject(ref):
  按 otype 查 §5.2 表,对每个子引用字段调 markValue
  // String 无子引用,scanObject 是 no-op(可在入栈前直接标黑跳过,省一次入栈)
```

**标记起点的「死白」判定**:`markValue` 只对 `colorOf == deadWhite()`(§4.3,= 上轮 currentWhite,即本轮回收色)的对象置灰。
当前白(currentWhite,本轮新生对象的色)被视为「已知存活」直接跳过——STW 下 mark 前无新生对象,此分支不触发,但保持与增量同构。
mark 结束:存活对象全黑,死对象全保持 deadWhite,gray stack 空。

> **gray stack 溢出**:对象图极深(如长链表)时 gray stack 可能很大。P1 用 Go slice 自动扩容,可接受;
> 极端内存压力下(标记自身要分配 gray stack)是隐患,见 §10 缺口(未来可用「对象内 gray 链」复用 gcnext 位避免额外分配,Lua 5.1 即如此)。

---

## 6. shadow stack:GC 根管理(任务点名定稿)

### 6.1 形式定稿:Lua 栈即根 + host 显式 handle

承 §5.1 的澄清,shadow stack 的**最终形式**定为**二元**:

1. **Lua 执行现场不用 shadow stack**——解释器跑 Lua 字节码时,活跃值在 arena 内的 thread 值栈(R5),GC 顺着 Thread 根扫到,
   **零登记开销**。这是性能关键路径,绝不能让每条产生对象的指令都去 push/pop shadow stack(那会让 NaN-boxing 的零分配优势打折)。
2. **host function 执行期用显式 push/pop handle**——host(Go)代码在 Go 栈持有 arena 引用的窗口期,显式登记到 shadow stack。

为什么不统一用「显式 handle」(像某些 C 扩展的 `lua_pushvalue` 风格全程登记)?因为 Lua 解释器的活跃值**已经在被扫描的栈里**,
再登记是冗余 + 拖慢热路径。为什么不统一用「栈即根」(让 host 也把临时值塞进 Lua 栈)?host 代码是任意 Go 逻辑,
不总有现成 Lua 栈槽可塞(且塞了要管理 top,易错);显式 handle 更贴合 Go 代码的 `defer pop` 习惯。**二元形式是性能与人体工学的最优切分。**

### 6.2 shadow stack 数据结构与接口

```go
// internal/gc —— shadow stack:host 执行期临时根
//
// 仅在 host function / 宿主回调执行期间有非空内容;Lua 解释器循环内通常为空。
// 元素是 Value(含其 tag),mark 时对每个元素 markValue。
type ShadowStack struct {
    refs []value.Value
}

// Push 登记一个临时持有的 arena 引用为根,返回 handle(其实是栈深度,用于校验配对)。
// host 代码模式:ref := arena.NewString(...); h := ss.Push(ref); defer ss.Pop(h); ...用 ref...
func (s *ShadowStack) Push(v value.Value) int { s.refs = append(s.refs, v); return len(s.refs) }

// Pop 弹出到指定深度(配对校验:popTo 必须等于 Push 返回的 handle,防漏配对)。
func (s *ShadowStack) Pop(handle int) { s.refs = s.refs[:handle-1] }

// 供 mark 阶段遍历(§5.3 R7)。
func (s *ShadowStack) forEachRoot(fn func(value.Value)) {
    for _, v := range s.refs { fn(v) }
}
```

### 6.3 host function 的使用约定(写进 stdlib 调用约定,[10-stdlib](./10-stdlib.md) 引用)

host function 签名(见 [10-stdlib](./10-stdlib.md))拿到一个执行上下文,其中含 arena 与 shadow stack。约定:

- **host 从 Lua 栈读参数 → 操作 → 写返回值回 Lua 栈**,这个过程中**新分配**的中间对象(如 `string.format` 拼出的新串、
  `table.create` 新建的表)在「写回 Lua 栈之前」处于 Go 栈持有窗口,**必须 `Push`/`defer Pop`**。
- 一旦中间对象**写回到 Lua 栈槽 / 已存活的表 / 全局**,它就经 R5/R1 等可达,可从 shadow stack `Pop`(此后栈/表当根)。
- **多步分配**:host 连续分配 A 再分配 B(分配 B 时可能触发 GC),A 必须先 Push,否则分配 B 触发的 GC 会回收 A。
  规则简记:**「下一次可能触发 GC 的分配之前,所有已持有但未上 Lua 栈/表的 arena 引用都要在 shadow stack 上」**。

> 这条约定是 stdlib 实现的**纪律**(类似写 C 扩展时管理 `lua_State` 栈的纪律)。差分测试不直接测它,
> 但漏 Push 会导致偶发崩溃(GC 时机依赖分配量,非确定),属最难调的 bug 类——[12](./12-testing-difftest.md) 的 GC 压力 fuzz(强制高频 GC)是主要捕获手段(见 §11)。

---

## 7. safepoint 与 GC 触发时机

### 7.1 safepoint 限定(`docs/design/roadmap.md` (§3))

GC 只在两类受控位置介入(safepoint),**绝不异步打断**:

1. **分配点**:`Arena.Alloc`(§2.1)内存不足时触发 GC。这是**唯一**的主动 GC 入口(STW 下)。
2. **层边界**:解释器 ↔ 编译层(P3+)、VM ↔ host 的边界。P1 只有解释器层,层边界退化为「VM ↔ host 调用边界」——
   可作为 GC 检查点(host 调用前后栈一致,适合 GC),但 P1 主要靠分配点触发,层边界检查是**可选的额外触发机会**(用于长时间不分配的循环也能周期 GC)。

**不在任意 PC 抢占 GC**(对比 `docs/design/roadmap.md` (§2) 的「异步抢占税」):望舒自管 GC 是协作式的,
GC 只在 mutator 主动调 Alloc(或到达层边界检查点)时发生,故 GC 触发时 mutator 一定处于**已知的、值一致的状态**。

### 7.2 哪些 opcode 可能触发 GC(承 [02-bytecode-isa](./02-bytecode-isa.md))

解释器循环里,**会分配 arena 对象的 opcode** 是 GC 触发点(因其内部调 Alloc):

| opcode([02](./02-bytecode-isa.md) §4) | 分配什么 | GC 触发点 |
|---|---|---|
| `NEWTABLE` | Table 头 + array 段 + node 段(3 次 Alloc) | 是 |
| `CLOSURE` | Lua Closure 对象(+ 可能新建 Upvalue) | 是 |
| `CONCAT` | 拼接结果新字符串(可能多个中间串) | 是 |
| `CALL`/`TAILCALL`(host 被调) | host 内部可能分配(见 §6) | 间接 |
| 产生新字符串的算术/转换(如 `tostring`、数字→串) | 新字符串 | 是 |
| `SETLIST` | 可能触发表 array 段扩容(rehash → 新 array 段) | 是(扩容时) |
| `SETTABLE`/`SETGLOBAL`(触发 rehash) | 新 array/node 段(rehash) | 是(rehash 时) |

**关键不变式:GC 触发时,解释器寄存器/栈必须处于一致状态(所有活跃 Value 可达)。** 实现约束:

- 调用 Alloc **之前**,把所有「即将放入新对象、但暂存在 Go 局部的活跃 Value」要么已在 thread 栈槽(R5 自动可达),
  要么不存在这种情况。Lua opcode 的操作数都是寄存器(栈槽),天然在 R5 覆盖内——所以**多数 opcode 的 GC 安全是自动的**。
- **例外:多步构造的中间结果**。如 `CONCAT R(B)..R(C)` 右结合逐对拼接,中间串 `tmp = R(C-1)..R(C)` 在拼下一对前是临时值。
  实现选择:① 把中间串**写回某个寄存器槽**(在 R5 覆盖内,GC 安全),或 ② 用 shadow stack 临时登记。
  **P1 定稿:中间结果一律落寄存器槽**(CONCAT 把累积串写回 `R(A)` 或临时槽,逐步推进),不依赖 shadow stack——
  这样解释器主循环**完全不碰 shadow stack**(§6.1 形式 1),热路径零登记开销。详见 [05](./05-interpreter-loop.md) 的 CONCAT 实现。

### 7.3 STW 下的简单性

stop-the-world = GC 触发时**暂停 mutator,完整跑完 mark+sweep,再恢复**。在 P1 单 goroutine 解释器里,
「暂停 mutator」是**天然的**:Alloc 是普通函数调用,GC 在其内同步执行,执行 GC 时根本没有其它代码在跑——
无需真实的 stop-the-world 协调(无并发线程要停)。这是 P1 选 STW 的最大红利:

- **mark 看到的对象图是静止的**——遍历期间无 mutator 修改,无需写屏障(§9)维持三色不变式。
- **根集合是静止的**——R1..R9 在 GC 期间不变,枚举一次即可。
- **sweep 是静止的**——无并发分配干扰 sweep 链遍历。

整个 GC 是 Alloc 调用栈里的一段同步代码,**正确性推理退化为单线程顺序逻辑**。代价是 GC 暂停时间 = 全堆 mark+sweep
(对 P1 的「更好的 gopher-lua」目标可接受;增量化是 P3+ 的事,届时双白+写屏障+gray 链已就位,见 §4.3/§9)。

---

## 8. sweep 阶段与 GC 主流程

### 8.1 sweep:遍历 gcnext 链,回收死白

mark 后,沿 **sweep 全对象链**(`gcnext` 串起的所有带 GCHeader 对象,[01](./01-value-object-model.md) §4 bit11/gcnext)逐个判定:

```
sweep():
  prev := nil
  ref := sweepHead
  for ref != 0:
      h := headerOf(ref)
      next := gcnextOf(h)
      if colorOf(h) == deadWhite():        // 未被标记的旧白 ⇒ 死
          unlinkSweep(prev, ref)           // 从 sweep 链摘除
          freeObject(ref, otypeOf(h))      // 归还 freelist(§2),特殊处理 String/Userdata
      else:                                 // 灰不可能(mark 已清空 gray);黑或 fixed 或当前白 ⇒ 活
          h = setColor(h, currentWhite())  // 存活对象翻为「当前白」,准备下一轮
          setHeader(ref, h)
          prev = ref
      ref = next
  flipWhite()                               // §4.3:currentWhite ^= 1,下轮死白 = 本轮存活的当前白
```

**存活对象翻白**:本轮存活对象(黑)在 sweep 时翻成 `currentWhite`(翻转**前**的当前白);随后 `flipWhite` 翻转,
使下一轮的 `deadWhite` = 本轮的存活色——即「上轮存活、本轮再没被标记到」的对象成为下轮回收候选。这是双白翻转的闭环(§4.3)。

`freeObject` 按 otype 分派:
- **String**:从 string table 摘除(§9.2 的特殊处理),再归还字节到 size-class/LARGE freelist。
- **Table**:先回收 array 段、node 段(附属块,§1.3),再回收表头。
- **Thread**:先回收 valueStack、callInfo 段,再回收 thread 头(注意:dead thread 的开放 upvalue 应已关闭)。
- **Userdata**:若有 `__gc` 且尚未终结,**不在此回收**——转入 finalize 队列(§10),否则直接回收 payload + 头。
- 其余(Closure/Upvalue):直接归还。

### 8.2 GC 主流程(STW 单趟)

```go
// internal/gc
func (c *Collector) collect() {              // STW full GC,一趟跑完
    c.markRoots()                            // §5.1:枚举 R1..R9 入 gray
    c.markAll()                              // §5.3:gray stack 跑空,存活全黑;弱表弱侧不标记并登记 weakList(§8.4)
    c.separateFinalizers()                   // §10:把待终结 userdata 从死白中分出,标记其可达图(复活)
    c.clearWeakTables()                      // §8.4:遍历 weakList,移除弱侧已死的 entry(承 07 §13 弱表语义)
    c.sweep()                                // §8.1:回收死白,存活翻当前白,flipWhite
    c.runFinalizers()                        // §10:在安全点逐个调 __gc(创建逆序)
    c.updatePacing()                         // §8.3:据本轮存活量设下次触发阈值
}
```

> **阶段顺序定稿(承 [07](./07-metatables-metamethods.md) §13.4)**:`mark → separateFinalizers(复活)→ clearWeakTables → sweep → runFinalizers`。cleartable 在 separateFinalizers **之后**——finalizer 复活的对象已被重新标活,不会被弱表误清;在 sweep **之前**——entry 移除必须发生在弱侧对象内存被回收前(清的是「本轮判死」的引用)。

```go
// Alloc 内的触发判断(§2.1 maybeCollect):
func (c *Collector) maybeCollect(need uint32) {
    if c.arena.bump+need > c.threshold {     // 分配量驱动(§8.3),非「容量满才回收」
        c.collect()
    }
}
```

注意 `maybeCollect` 的触发条件是 **`bump > threshold`**(分配量驱动 pacing,§8.3),不是「bump 撞 cap」——
后者(撞 cap)是 grow 的条件(§3)。即:正常情况 GC 在 arena 用到 threshold 时主动回收(回收够则不 grow);
只有 GC 后存活仍逼近 cap 才 grow。两个阈值分工:**threshold 控制 GC 频率,cap 控制物理扩容**。

### 8.3 GC pacing / 触发阈值(分配量驱动)

承任务要求「分配达到上次存活量的某倍数触发 full GC」,对齐 Lua 的 `gcpause` 思路:

```
updatePacing():
  live := bump - freeBytesAfterSweep   // 本轮 sweep 后的存活字节(近似:bump 减去 freelist 总空闲)
  threshold = live * GCPAUSE / 100     // GCPAUSE 默认 200 ⇒ 存活量翻倍时再 GC
```

- `GCPAUSE`(默认 **200**,即 2.0x):存活 `live` 字节后,再分配 `live` 字节(共 `2*live`)触发下次 GC。
  与 Lua 5.1 `LUAI_GCPAUSE` 默认 200 一致(语义对齐,便于差分时 GC 行为可类比)。
- **首次触发**:`threshold` 初值 = 一个小常量(如 arena 初始容量,避免极早 GC)。
- P1 **只做 full GC**(无增量步长 `GCstepmul`);threshold 是「下次 full GC 的分配水位」。增量步长是 P3+ 的事。
- **存活量估计**:精确 live = sweep 时累加存活对象字节;或近似 = `bump - sum(freelist 空闲块)`。P1 用 sweep 时精确累加(简单准确)。

> pacing 影响 GC **频率**,不影响**正确性**与**可观察行为**(§11):无论何时 GC,存活对象不变、`pairs` 序不变。
> 但 GC 频率影响**性能**与**捕获 bug 的概率**——[12](./12-testing-difftest.md) 的「高频 GC 模式」(把 GCPAUSE 设到极小,
> 每次分配都 GC)是逼出「漏 push shadow stack / mark 漏字段」类 bug 的压力测试手段(§11)。

### 8.4 弱表(`__mode`)的 GC 协作(承 [07](./07-metatables-metamethods.md) §13 回填请求)

弱表**语义**由 [07](./07-metatables-metamethods.md) §13 定义(weak k/v/kv、cleartable、P1 的 ephemeron 简化),本节落实 GC 侧协作:

```go
// internal/gc —— Collector 增字段(类比 Lua 5.1 g->weak 链)
type Collector struct {
    // ...(§4.3 字段)
    weakList []value.GCRef   // 本轮 mark 中发现的弱表登记(每轮 GC 重建)
}
```

1. **mark 阶段登记**(修改 §5.2 Table 扫描行):`scanObject(Table)` 先经 `object.Table.WeakMode()`(07 §13.4 下沉的 object 侧 helper,读元表 `__mode` 解析 `'k'`/`'v'`)判定——非弱表照旧全扫;弱表**弱侧不标记**(weak value 不扫 val、weak key 不扫 key;P1 简化:weak key 表的 val 无条件标活,07 §13.5),并把该表 GCRef 登记进 `weakList`。metaRef 恒扫(元表自身是强引用)。
2. **clearWeakTables 阶段**(§8.2 主流程,位于 separateFinalizers 之后、sweep 之前):遍历 `weakList`,对每表遍历全部 entry,移除「弱侧对象为本轮死白」的 entry(key 或 val 是 collectable 且 `colorOf == deadWhite()`)。移除即把槽置空(node 的 key/val 置 Nil,数组槽置 Nil),**不**触发 rehash(GC 内不分配)。
3. **可观察性**:cleartable 移除 entry 影响后续 `pairs`/`next` 产出,与 `pairs` 序、`tostring` 地址同类,纳入 §11 / [12](./12-testing-difftest.md) 的差分口径。

> `WeakMode()` 在 mark 期间读元表是安全的:STW 下对象图静止;helper 下沉到 `internal/object` 避免 gc 包反向依赖 crescent(07 §13.4 第 3 项)。

---

## 9. string interning + 写屏障接口

### 9.1 string table 的组织

所有字符串 intern(短串长串都 intern,[01](./01-value-object-model.md) §5.1 已定),相等串共享唯一 GCRef,`rawequal` 退化为 GCRef 比较。
string table 是**开放在 GC 之外的索引结构**(Go 侧持有,但索引项是 arena GCRef):

```go
// internal/gc(或 internal/object)—— 字符串 intern 表
//
// 链式哈希(separate chaining):buckets[hash & mask] 是一条冲突链;
// 链用「String 对象内的 gcnext 之外的链字段」串接 —— 但 §1 String 布局没留 intern 链字段。
// 决策:intern 链复用 sweep 的 gcnext?不行(gcnext 是 sweep 链,语义冲突)。
// 故 string table 的冲突链放在 **Go 侧**:每个 bucket 是 []GCRef(Go slice),不侵入 String 对象。
type StringTable struct {
    buckets [][]value.GCRef  // buckets[h & mask];链式;元素是已 intern 串的 GCRef
    mask    uint32           // len(buckets)-1,2 的幂
    count   uint32           // 已 intern 串数,用于扩容判断(装填率 > 1 时 rehash buckets)
    arena   *Arena
}

// Intern:查表;命中返回既有 GCRef(0 拷贝);未命中则 Alloc 新 String、写内容、入表。
func (st *StringTable) Intern(b []byte) value.GCRef {
    h := hashString(b)                         // §9.3 定稿算法
    bk := h & st.mask
    for _, ref := range st.buckets[bk] {       // 遍历冲突链
        if st.equalContent(ref, h, b) {        // 比 hash + len + memcmp 内容
            return ref                          // 命中:复用
        }
    }
    ref := st.arena.allocString(h, b)          // 未命中:分配(可能触发 GC!见下)
    st.buckets[bk] = append(st.buckets[bk], ref)
    st.count++
    if st.count > st.mask { st.rehash() }      // 装填率控制
    return ref
}
```

**Intern 内分配触发 GC 的隐患**:`allocString` 可能触发 GC;若此刻 `b` 对应的新串尚未入任何根,
而 GC 又恰好…… 实则**安全**:新串还没分配出来,GC 回收的是别的死对象;新串分配成功后立即 `append` 入 bucket。
但若 `b` 是从某个**即将被回收的源串**借来的字节切片,需保证源在 Intern 期间存活——调用方责任(通常 `b` 来自 Lua 栈槽,R5 可达)。

### 9.2 string table 是 GC 根还是弱表?——定稿(任务点名,纠正二选一)

任务问「intern 表本身是 GC 根还是弱表」。核对 Lua 5.1 `lgc.c`/`lstring.c` 后定稿:**两者都不是,是「特殊 sweep」**。

- **不是强根**:若把 string table 当强根(标记所有 bucket 里的串),则任何曾被 intern 的串永不回收 ⇒ 字符串内存只增不减 ⇒ 泄漏。错误。
- **不是 Lua 概念的弱表**:Lua 的弱表(`__mode`)是用户可见的、`mark` 时跳过弱引用、原子阶段 `cleartable` 清死键值的机制;
  string table 不走这套(它不是一个 Lua Table,是 VM 内部索引)。
- **正确语义(Lua 5.1 `GCSsweepstring` 阶段)**:**字符串靠「其它根可达性」存活**——被某个活表的键/值、某个活栈槽、某个 Proto 常量引用的串,
  在 mark 阶段经 R1..R9 被正常标黑;**没有任何引用的串**在 mark 后保持死白。**sweep 时,遍历 string table 的每个 bucket,
  把死白的串从 bucket 链里摘除并回收**(Lua 的 `sweepwholelist(strt.hash[i])` + `freeobj` 递减 `strt.nuse`)。

**落到望舒实现:**

- string table **不参与 mark 的根枚举**(R1..R9 不含它)。串的存活完全由「是否被其它根可达」决定。
- string table **参与 sweep**:sweep 不仅走 gcnext 全链(§8.1),对 String 类对象,回收时**额外**从其所在 bucket 摘除。
  实现上,§8.1 的 `freeObject(String)` 调 `StringTable.remove(ref)`:算 `h & mask` 定位 bucket,从 `[]GCRef` 里删该元素。
- **等价性**:gcnext 全链已包含所有 String(它们都带 GCHeader、都 linkSweep),故沿 gcnext sweep 时自然遍历到每个死串,
  逐个从 bucket 摘除即可——**无需单独遍历 string table 的所有 bucket**(Lua 5.1 单独遍历 strt 是因其 GC 分阶段;
  望舒 STW 单趟 sweep 顺着 gcnext 一次处理所有对象含 String,更简单)。

> 一句话:**string table 是「弱可达索引」——不延长串的命，只在串死时负责把它从索引里摘掉。** 这是 Lua 5.1 字符串 GC 的精确语义。

### 9.3 hash 算法定稿:Lua 5.1 JSHash 分段采样(否决 FNV-1a)

[01](./01-value-object-model.md) §5.1 把算法选择留给本文(FNV-1a vs Lua 分段采样)。**定稿:采用 Lua 5.1 的 JSHash 分段采样。**

```go
// internal/object —— 与 Lua 5.1 lstring.c luaS_newlstr 的 hash 逐位一致
func hashString(b []byte) uint32 {
    l := uint32(len(b))
    h := l                                   // 种子 = 长度(不同长度起点不同)
    step := (l >> 5) + 1                      // 采样步长:≤31 字节全采样,更长则跳采
    for i := l; i >= step; i -= step {
        h ^= (h << 5) + (h >> 2) + uint32(b[i-1])  // JSHash 混合,反向遍历
    }
    return h
}
```

**算法事实(核对 Lua 5.1.5 `lstring.c`):** 种子 `h=len`;`step=(len>>5)+1`;反向 `h ^= (h<<5)+(h>>2)+byte`。
短串(≤31 字节)逐字节,长串最多采样 ~32 字节(`step` 增大跳采)。这是 Justin Sobel 哈希(JSHash)。32-bit 结果,正好填 [01](./01-value-object-model.md) §5.1 的 `hash32` 字段。

**为什么选它而不是 FNV-1a —— 差分一致性的可观察性分析(任务点名):**

`pairs` 遍历序由 table 的 node 段布局决定,node 主位置 = `hash(key) & hmask`([01](./01-value-object-model.md) §5.2)。
**字符串键的哈希值直接决定它落在 node 段哪个槽,从而决定 `pairs` 的产出顺序。** 这是 GC/内存层**唯一**泄漏到可观察行为的口子(§11):

- **若选 FNV-1a**:望舒字符串键的哈希分布与 Lua 5.1 / gopher-lua **不同** ⇒ 同一组字符串键插入同一个表,
  node 段槽位分布不同 ⇒ `pairs(t)` 的遍历顺序**与官方/gopher-lua 不一致**。Lua 语义允许 `pairs` 序未定义,
  但 [12-testing-difftest](./12-testing-difftest.md) 的逐字节差分若把「`pairs` 输出顺序」纳入对比口径,FNV-1a 会**天然差分失败**
  (输出顺序不同 ⇒ 非 byte-equal),逼迫差分 harness 对 `pairs` 输出做「排序后比较」的特殊豁免。
- **若选 Lua 5.1 JSHash**:望舒字符串键哈希**与官方逐位一致**(同种子同混合同采样)⇒ 在**相同 hmask** 下槽位分布一致 ⇒
  `pairs` 序更可能与官方一致 ⇒ 差分 harness 可对更多用例做**严格逐字节比较**,减少豁免面,更强地兑现 `docs/design/roadmap.md` (§5) 原则 2
  「层间逐字节差分是防投机错误静默错果的主防线」。

> **重要限定:hash 一致 ≠ `pairs` 序必然一致。** `pairs` 序还取决于 ① `hmask`(表的 node 段大小,由 rehash 算法 `luaH_resize` 决定)
> ② 冲突链顺序(Brent 变体的让位策略)③ 数组段与哈希段的遍历拼接顺序。**仅哈希算法一致不足以保证 `pairs` 全等**——
> 还需 rehash 算法、Brent 变体、遍历顺序都与 Lua 5.1 对齐(这些在 [01](./01-value-object-model.md) §5.2 已声明「对照 `ltable.c`,与差分基准逐字节一致」)。
> **选 JSHash 是「让 `pairs` 序可能一致」的必要不充分条件**;它把哈希这一环锁死与官方一致,把差分不一致的风险面缩小到 rehash/Brent/遍历这几环
> (那几环也都承诺对齐 Lua 5.1)。选 FNV-1a 则连哈希环都偏离,差分一致**几无可能**。故定稿 JSHash。

> **`pairs` 序是否要求逐字节一致是验收口径问题**(承 [01](./01-value-object-model.md) §8 与 [02](./02-bytecode-isa.md) 缺口):
> 最终由 [12-testing-difftest](./12-testing-difftest.md) 定。本文的职责是**把哈希环锁成与官方一致**,为「严格口径」留可能;
> 若 [12](./12-testing-difftest.md) 最终选「宽松口径」(`pairs` 输出排序后比较),JSHash 也无害(仍是良好分布的哈希)。**JSHash 是两种口径下的占优选择。**

**hash flooding 注意**:Lua 5.2+ 给 hash 加随机 seed 防 DoS([01](./01-value-object-model.md) 锁定 5.1,不引入)。望舒沿用 5.1 无 seed
(差分一致优先;嵌入式宿主负载非对抗性,DoS 风险低)。若未来宿主面对不可信脚本,可加 seed——但那会破坏与官方的哈希一致性(差分豁免),属权衡,记 §10 缺口。

### 9.4 写屏障接口(P1 空实现,为增量 GC 预留)

P1 STW **不需要写屏障**(§7.3:mark 期间 mutator 暂停,三色不变式自动成立)。但 [01](./01-value-object-model.md) §4 的 color 位、
`isCollectable` 区间([01](./01-value-object-model.md) §3.3/§7 不变式 6)**本就是为屏障设计的**。本文给出**接口占位**,P1 空实现,P3+ 增量/分代 GC 填充:

```go
// internal/gc —— 写屏障接口占位
//
// 语义(未来增量 GC):当把一个引用 child 写入已是 BLACK 的对象 parent 的某字段时,
// 三色不变式「黑对象不得指向白对象」可能被破坏(parent 已扫完不会再看 child,
// child 若是白会被误回收)。屏障修复之:或把 parent 重新标灰(back barrier),
// 或把 child 标灰(forward barrier)。
//
// P1:STW 无并发标记,parent 写入时要么 GC 没在跑(mutator 全程独占),
// 要么 GC 是同步原子的(Alloc 内一次跑完),不存在「黑指白且 mark 已略过」窗口 ⇒ 空实现。
func (c *Collector) writeBarrier(parent value.GCRef, child value.Value) {
    // P1: no-op.
    // P3+: if isBlack(parent) && isWhite(child) && c.incrementalMarking {
    //          c.markValue(child)            // forward barrier:把 child 标灰,纳入本轮
    //          // 或 makeGray(parent) 重扫
    //      }
}

// 屏障插桩点(P1 不调用,P3+ 在这些写操作后调 writeBarrier):
//   - SETTABLE / SETLIST:表槽写入(parent=table, child=value)
//   - SETUPVAL / upvalue 关闭:upvalue.value 写入
//   - SETGLOBAL:全局表槽写入
//   - setmetatable:metaRef 写入
//   凡「把一个可回收 child 存入一个 arena 对象 parent」的写,都是潜在屏障点。
```

**呼应 `docs/design/roadmap.md` (§2)「写屏障税」:** 四项税里的「裸指针写破坏并发 GC 三色不变式」指的是 **Go runtime 的** `gcWriteBarrier`
(写 Go 堆指针时编译器自动插的屏障)。望舒的 `writeBarrier` 是**我们自己的、arena 内的、逻辑层屏障**——
它操作的是 arena 内对象的 color 位,**完全不碰 Go 的 `runtime.gcWriteBarrier`**(那是 `docs/design/roadmap.md` (§6) 非目标:
「绝不 inline 复刻 `runtime.gcWriteBarrier`/`runtime.mallocgc` 等内部符号」)。因为 arena 内引用是 GCRef 整数(非 Go 指针),
写它们时 Go 编译器**根本不会**插 `gcWriteBarrier`(Go 只对 Go 指针写插屏障)——这正是 NaN-boxing + 偏移寻址绕开「写屏障税」的兑现:
**Go 的屏障税我们一分不付(没有 Go 指针写),自己的屏障(增量 GC 才需要)在 arena 内逻辑层自管。**

---

## 10. finalizer(`__gc`):full userdata 终结器

### 10.1 Lua 5.1 语义(核对参考实现)

只有 **full userdata** 可设 `__gc` 终结器([01](./01-value-object-model.md) §5.5;Lua 5.1 中 table 不支持 `__gc`,那是 5.2+,`docs/design/roadmap.md` (§6) 已排除)。语义:

- 带 `__gc` 的 userdata 不可达时**不立即回收**,而是放入**终结队列**;
- GC 周期**末尾**,对队列里每个 userdata 调其 `__gc(ud)`;
- **调用顺序 = 创建逆序**(Lua 5.1:后创建的先终结);
- 终结期间**停 GC 步**(防递归重入,保证顺序;Lua 的 `g->gcrunning=0`);
- **复活(resurrection)**:`__gc` 收到的 ud 及其可达对象在本轮被「复活」(必须存活以供终结器使用),
  内存通常下轮回收;若终结器把 ud 存到全局,则永久复活。

### 10.2 望舒实现

```go
// internal/gc —— 终结器支持
type Collector struct {
    // ...(§4.3 字段)
    finalizeList []value.GCRef  // 待终结 userdata(按需保持创建序;终结时逆序遍历)
    hasFinalizer map[value.GCRef]bool  // 或用 GCHeader flags 位标记「已登记终结」
}

// 登记:setmetatable(ud, mt) 且 mt.__gc 存在时,把 ud 标「需终结」并记入创建序。
func (c *Collector) markForFinalize(ud value.GCRef) { c.finalizeList = append(c.finalizeList, ud) }
```

GC 主流程(§8.2)的两个终结相关步骤:

1. **`separateFinalizers()`(mark 后、sweep 前)**:遍历 `finalizeList`,对其中 **mark 后仍是死白**(不可达)的 userdata:
   - 把它**从死白救出**——标记它**及其经 `__gc` 终结器可达的对象图**为存活(复活,防 sweep 回收它和它要用的数据);
   - 移入一个「本轮待运行终结器」子列表 `toRunFinalizers`(保持创建序);
   - 从 `finalizeList` 移除(已终结的不再二次终结——除非终结器把它再次 `setmetatable` 带 `__gc`,见 Lua 多次终结语义,P1 可选不支持重复登记)。
   仍可达的(未死)userdata 留在 `finalizeList` 等future轮。
2. **`runFinalizers()`(sweep 后)**:**逆序**遍历 `toRunFinalizers`,对每个调 `__gc(ud)`:
   - 在**安全点**调用(此刻 GC 已完成 mark+sweep,处于一致状态);
   - 调用期间**禁止再触发 GC**(置 `c.gcRunning=false` 或等价标志,Alloc 内 `maybeCollect` 检查此标志跳过)——
     防终结器内分配触发嵌套 GC 破坏顺序(对齐 Lua `GCTM` 停步语义);
   - 终结器是 host function(`__gc` 通常宿主用 C/Go 写),经 host 调用约定执行(§6.3);
   - 终结器抛错:Lua 5.1 用 `luaD_pcall` 保护,望舒同样**保护调用**(终结器错误不应崩 VM;记日志,见 [09-errors-pcall](./09-errors-pcall.md))。
   - 终结后该 userdata 的实际内存回收:**下一轮 GC**(本轮已复活;若终结器未永久持有,下轮成死白被回收)。

> **创建逆序的实现**:`finalizeList`/`toRunFinalizers` 按 `append` 顺序 = 创建/登记顺序;`runFinalizers` 从尾到头遍历即逆序。
> Lua 5.1 是创建逆序(5.3+ 改标记逆序,`docs/design/roadmap.md` (§6) 锁 5.1,用创建逆序)。

> **P1 范围裁剪**:full userdata + `__gc` 是嵌入 API 的能力,P1 stdlib 本身极少用(主要给宿主)。
> P1 可先实现「队列 + 逆序 + 停步 + 保护调用」骨架,**复活的可达图标记**(`separateFinalizers` 里标 ud 可达对象)
> 是正确性关键(否则终结器访问已回收数据),必须实现。多次终结(终结器复活后再登记)P1 可不支持(记 §11 缺口)。

---

## 11. arena/对象布局与差分测试的关系

**核心论断:GC 不应改变可观察行为。** Lua 程序无法直接观察「对象在 arena 的哪个偏移」「何时被回收」「freelist 复用了哪块」——
这些是 VM 内部状态。同一程序在「频繁 GC」与「从不 GC」下,**可观察输出必须 byte-equal**(这本身是差分测试的一个维度:
同一 Proto 在不同 GC pacing 下输出一致)。但有**两个**内存层行为泄漏到可观察面,[12-testing-difftest](./12-testing-difftest.md) 必须处理:

1. **字符串 intern 哈希 → `pairs` 序**(§9.3 已详析):字符串键哈希值决定其在 node 段槽位,影响 `pairs`/`next` 遍历序。
   选 JSHash(§9.3)把哈希环锁死与官方一致,是为「`pairs` 序与官方/gopher-lua 一致」创造必要条件。
2. **表 rehash → `pairs` 序**:插入/删除触发 `luaH_resize`,重算 asize/hmask,键重新散布,`pairs` 序随之变([01](./01-value-object-model.md) §5.2)。
   rehash **时机**由插入历史决定(与 GC 无关),但 rehash **结果布局**须与 Lua 5.1 `ltable.c` 一致(已在 [01](./01-value-object-model.md) §5.2 声明对齐)。

**对 [12-testing-difftest](./12-testing-difftest.md) 的接口要求(本文提出,12 定稿):**

- **`pairs`/`next` 序的口径**:逐字节严格 vs 排序后比较。本文(§9.3)已把哈希环对齐官方,使「严格口径」有可能;
  最终口径由 12 在跑通差分后定。**无论哪种口径,JSHash + 对齐的 rehash/Brent 都是占优选择**。
- **GC 压力 fuzz**:把 `GCPAUSE` 设到极小(每次/每几次分配就 full GC),反复跑同一程序,验证:
  ① 输出与「正常 pacing」byte-equal(GC 透明性);② 不崩溃(捕获「漏 push shadow stack(§6.3)/mark 漏扫字段(§5.2)」类 bug)。
  这是 §6.3 纪律、§5.2 完整性的**主要自动化防线**——这类 bug 在正常 pacing 下偶发难复现,高频 GC 下必现。
- **finalizer 顺序**:`__gc` 调用序(创建逆序,§10)若被测试观察(终结器打印),须与 Lua 5.1 一致——属差分用例。

> 与 [01](./01-value-object-model.md) §8 / [02](./02-bytecode-isa.md) §10 的缺口呼应:`pairs` 是否逐字节一致是验收口径问题,
> 本文把「哈希算法」这一可控变量锁成与官方一致(§9.3 定稿 JSHash),把口径决策的剩余变量缩到 rehash/Brent/遍历顺序(均已声明对齐),交 [12](./12-testing-difftest.md) 收口。

---

## 12. 文档缺口 / 待决(记入 memory/doc-gaps)

- **compaction(对象搬迁)未做**:P1 不做堆压缩(§2.4/§3),大对象死后空间靠 LARGE freelist 首次适配复用,不合并。
  偏移寻址使 compaction 未来可行(搬迁后批量重写偏移),但**搬迁需更新所有 GCRef**(含开放 upvalue 定位、IC slot 缓存的槽索引)——
  代价与收益待 P3+ 增量 GC 一并评估。当前缺口:**长期运行下大对象 freelist 的碎片化程度**无数据,需实现后压测。
- **gray stack 在内存压力下的分配**:mark 用 Go slice gray stack(§5.3),极端内存压力(GC 自身要扩 gray stack)是隐患。
  Lua 5.1 用「对象内 gray 链」(复用对象字段串灰对象,零额外分配)规避。望舒 P1 用 slice 简单,但**未定**是否在 OOM 边界稳健;
  未来可切「gcnext 兼作 gray 链」(sweep 链与 gray 链分时复用同一字段)。记缺口。
- **多次终结(finalizer resurrection 再登记)**:§10 P1 可不支持「终结器复活对象后再次 `__gc`」。Lua 5.1 支持(可多次终结)。
  与差分一致性的关系:若测试用例依赖多次终结,P1 会差分失败——需评估真实负载是否触及(嵌入式宿主罕见),记缺口。
- **hash seed(防 hash flooding)与差分一致的冲突**:§9.3 沿用 Lua 5.1 无 seed 哈希(差分一致优先)。若未来宿主面对不可信脚本需防 DoS,
  加 seed 会破坏与官方哈希一致性(`pairs` 序偏离,差分需豁免)。这是「安全 vs 差分严格口径」的权衡,当前定「无 seed」,记决策待重估。
- **string table 冲突链放 Go 侧的内存账**:§9.1 决策 bucket 用 Go slice(`[][]GCRef`)而非侵入 String 对象。
  好处:不占 String 对象字段、不与 sweep 链冲突;代价:这部分索引内存在 Go 堆(随串数增长),且 rehash buckets 是 Go 侧操作。
  **未定**:超大字符串集(百万串)下 Go 侧 bucket 内存与 rehash 开销是否需改「侵入式 + arena 内链」。记缺口。
- **层边界 safepoint 的具体形式**:§7.1 说层边界是可选 GC 检查点,但 P1 只有解释器层,层边界退化为 VM↔host 边界。
  「长时间纯计算不分配的循环如何周期 GC」——P1 靠分配点,无分配的死循环不会 GC(也无需,因没产生垃圾)。
  P3+ 跨层时 safepoint 形式(回边检查点 vs 调用边界,对齐 `docs/design/roadmap.md` (§2) 异步抢占税解法)在 [p3-wasm-tier](../p3-wasm-tier/03-memory-model.md) 定。
- **与 [05-interpreter-loop](./05-interpreter-loop.md) 的接口**:本文假设 05 提供「running thread 寄存器」「CallInfo 帧布局(mark 需知帧内哪些字是 Value)」
  「CONCAT/多步构造把中间结果落寄存器槽(§7.2)」。05 尚未创建,这些接口**待 05 定稿后回填校验**。

---

## 13. 本文定稿速查(供 05/12 引用)

| 决策 | 定稿 | 依据 |
|---|---|---|
| **hash 算法** | **Lua 5.1 JSHash 分段采样**(§9.3),否决 FNV-1a | 差分一致性:把哈希环锁成与官方逐位一致,为 `pairs` 序严格口径创造必要条件 |
| **shadow stack 形式** | **二元**:Lua 执行现场「栈即根」(零登记)+ host 执行期「显式 push/pop handle」(§6.1) | 性能(热路径零开销)+ 人体工学(host 的 `defer pop`);补 Go 精确栈扫描看不见 arena 引用的盲区 |
| **根集合** | **R1..R9**(§5.1):全局表 / registry / 主线程 / 活跃 thread / running 线程的栈+CallInfo / Proto 常量+源名 GCRef / shadow stack / 临时根 | 漏一类即误回收;R5 自动可达,R7 显式登记 |
| **STW 决策** | **P1 stop-the-world full GC**(§7.3),双白+三色+写屏障接口为 P3+ 增量预留(§4.3/§9.4) | 单 goroutine 解释器下 STW 天然无需停顿协调,正确性退化为单线程顺序逻辑 |
| **string table 性质** | **弱可达索引**(§9.2):不延命,串死时从索引摘除(Lua 5.1 `GCSsweepstring` 语义);**非强根、非 Lua 弱表** | 核对 Lua 5.1 `lgc.c`/`lstring.c` |
| **GC pacing** | 分配量驱动,`threshold = live * 200%`(§8.3),P1 仅 full GC | 对齐 Lua `LUAI_GCPAUSE=200` |
| **写屏障** | P1 空实现占位(§9.4);不碰 Go `gcWriteBarrier`(`docs/design/roadmap.md` (§6) 非目标) | 我们自己的 arena 内逻辑屏障,增量 GC 才填充 |

---

相关:[01-value-object-model](./01-value-object-model.md)(脊柱:位布局/对象布局) ·
[02-bytecode-isa](./02-bytecode-isa.md)(分配类 opcode) ·
[05-interpreter-loop](./05-interpreter-loop.md)(safepoint 在循环何处 / CallInfo 帧布局 / CONCAT 中间结果落槽) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(`__gc`/`__index` 等) ·
[09-errors-pcall](./09-errors-pcall.md)(终结器错误保护) ·
[10-stdlib](./10-stdlib.md)(host function 调用约定 + shadow stack 使用纪律) ·
[12-testing-difftest](./12-testing-difftest.md)(`pairs` 序口径 / GC 压力 fuzz) ·
[architecture](../architecture.md) · [value-representation](../../../llmdoc/architecture/value-representation.md) ·
[design-premises](../../../llmdoc/must/design-premises.md)
