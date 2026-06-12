// Proto layout (01 §5.7) — Go-heap-resident, referenced by integer ProtoID.
//
// 含设计文档定稿的回填字段:LocVars(09 §8.4)与 UpvalDescs.Name(04 §8.3)。
package bytecode

import "github.com/Liam0205/wangshu/internal/value"

// UpvalDesc describes how an upvalue is captured by an inner function (04 §8.3).
type UpvalDesc struct {
	Name    string // 调试名(09 §8 函数名推断 / traceback 用)
	InStack bool   // true: 捕获外层局部寄存器;false: 捕获外层 upvalue
	Idx     uint8  // InStack=true:外层寄存器号;false:外层 upvalue 索引
}

// LocalVar describes a local variable's name and live range (04 §5.9 / 09 §8.4 回填)。
type LocalVar struct {
	Name    string
	StartPC int32 // 闭区间起点
	EndPC   int32 // 开区间终点 [StartPC, EndPC)
}

// Proto is the immutable compilation unit (one per Lua function).
//
// 字符串字面量惰性 intern(承 01 §5.7 / 06 §5.1 R6 改写):codegen 期间 Consts 中字符串
// 槽位置 Nil 占位,真实字面量原文在 StringLits;StringLitIdx[i] >= 0 表示 Consts[i] 是
// 字符串占位且原文在 StringLits[StringLitIdx[i]];= -1 表示真常量(Number/Bool/Nil)。
// 每个 State 首次执行该 Proto 时把 StringLits 逐个 intern 进自己的 arena,得到该 State
// 私有的 GCRef 表(GC 根,R6)。这使一个 Program 可被多 State / 多 goroutine 复用(11 §1.4)。
type Proto struct {
	Source      string // 源名(chunkname),用于错误回溯前缀
	LineDefined int32  // 函数定义起始行
	LineEnd     int32  // 函数定义结束行(`end` 行)

	NumParams uint8 // 形参个数
	IsVararg  bool  // 是否 vararg 函数
	NeedsArg  bool  // LUA_COMPAT_VARARG:vararg 函数隐式 arg 表(5.1 默认 compat;main chunk 无)
	MaxStack  uint8 // 寄存器水位线(04 §5.3),解释器进帧时备栈

	Code         []Instruction // 32-bit 指令流
	Consts       []value.Value // 常量槽:数字直接 boxed;字符串槽是 Nil 占位(由 State 装载期惰性替换)
	StringLits   []string      // 字符串字面量原文(Compile 期间收集,跨 State 共享只读)
	StringLitIdx []int32       // Consts 中字符串槽 → StringLits 下标的映射;非字符串槽 = -1
	Protos       []uint32      // 嵌套 Proto 的 ProtoID(下标,见 protos 注册表)
	SubNUps      []uint8       // 与 Protos 下标对齐:子函数 upvalue 数(= CLOSURE 后随伪指令数;symbexec 精确跳过用,官方经 p->p[bx]->nups 取)
	UpvalDescs   []UpvalDesc

	// 调试信息(可选;按 [架构] 选择是否保留——P1 保留以支持 traceback / 错误变量名后缀)。
	LineInfo []int32    // 每条指令对应的源行(len(LineInfo) == len(Code) 或为 0 = 无调试信息)
	LocVars  []LocalVar // 局部变量名 + 活跃区间(09 §8.4)

	// IC slots(02 §7,按 pc 索引)。
	// 长度 == len(Code);非 IC 指令对应槽空闲(可挪用为 P2 算术 IC 双计数,见 02 §7)。
	IC []ICSlot
}

// IsStringConst reports whether Consts[i] is a string literal placeholder.
// 调用方在 LOADK 路径根据这个判定决定走 State.programStringRefs 还是直接读 Consts[i]。
func (p *Proto) IsStringConst(i int) bool {
	return i < len(p.StringLitIdx) && p.StringLitIdx[i] >= 0
}

// ICSlot 是 inline cache 槽(02 §7 + 05 §6 定稿,含 tableRef / 双计数挪用)。
//
// 表 IC(GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF):
//   - Shape:目标表的 gen 代次
//   - Index:命中槽位(array 下标 / node 下标)
//   - TableRef:目标表 arena 偏移低 32 位(身份比对,非 GC 根)
//   - Kind:0/未初始化  1/array hit  2/node hit  3/mono-meta  4/megamorphic
//
// 算术 IC(ADD..POW、UNM、CONCAT 的快/慢路径计数):
//   - 字段挪用:Shape = numHits(快路径命中数)
//     Index = metaHits(元方法慢路径命中数)
//     TableRef 闲置(置 0)
//     Kind 仍按 P2 类型 feedback 语义使用
type ICSlot struct {
	Shape    uint32
	Index    uint32
	TableRef uint32
	Kind     uint8
}

// IC kind 常量(02 §7)。
const (
	ICKindNone        uint8 = 0
	ICKindArrayHit    uint8 = 1
	ICKindNodeHit     uint8 = 2
	ICKindMonoMeta    uint8 = 3
	ICKindMegamorphic uint8 = 4
)

// ProtoID is an index into the State.protos registry.
type ProtoID uint32

// HostFnID is an index into the State.hostFns registry (Go heap;01 §1).
type HostFnID uint32

// HostFnIDSentinel marks "this CallInfo frame belongs to a host function" (05 §1.2).
const HostFnIDSentinel uint32 = 0xFFFFFFFF
