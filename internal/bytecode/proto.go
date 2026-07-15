// Proto layout (01 §5.7) — Go-heap-resident, referenced by integer ProtoID.
//
// Includes the back-filled fields finalized in the design docs: LocVars (09 §8.4) and UpvalDescs.Name (04 §8.3).
package bytecode

import "github.com/Liam0205/wangshu/internal/value"

// UpvalDesc describes how an upvalue is captured by an inner function (04 §8.3).
type UpvalDesc struct {
	Name    string // debug name (09 §8 function-name inference / traceback)
	InStack bool   // true: captures an enclosing local register; false: captures an enclosing upvalue
	Idx     uint8  // InStack=true: enclosing register number; false: enclosing upvalue index
}

// LocalVar describes a local variable's name and live range (04 §5.9 / 09 §8.4 back-fill).
type LocalVar struct {
	Name    string
	StartPC int32 // inclusive start
	EndPC   int32 // exclusive end [StartPC, EndPC)
}

// Proto is the immutable compilation unit (one per Lua function).
//
// Lazy interning of string literals (per 01 §5.7 / 06 §5.1 R6 rewrite): during codegen the string
// slots in Consts hold a Nil placeholder while the real literal text lives in StringLits;
// StringLitIdx[i] >= 0 means Consts[i] is a string placeholder whose text is at
// StringLits[StringLitIdx[i]]; = -1 means a true constant (Number/Bool/Nil).
// The first time a State executes this Proto it interns each StringLits entry into its own arena,
// yielding that State's private GCRef table (a GC root, R6). This lets one Program be reused across
// multiple States / goroutines (11 §1.4).
type Proto struct {
	Source      string // source name (chunkname), used as the error-traceback prefix
	LineDefined int32  // starting line of the function definition
	LineEnd     int32  // ending line of the function definition (the `end` line)

	NumParams uint8 // number of formal parameters
	IsVararg  bool  // whether this is a vararg function
	NeedsArg  bool  // LUA_COMPAT_VARARG: implicit arg table for vararg functions (5.1 defaults to compat; the main chunk has none)
	MaxStack  uint8 // register high-water mark (04 §5.3), reserved on the stack when the interpreter enters the frame

	Code         []Instruction // 32-bit instruction stream
	Consts       []value.Value // constant slots: numbers are boxed directly; string slots are Nil placeholders (lazily replaced by the State at load time)
	StringLits   []string      // string-literal text (collected during Compile, shared read-only across States)
	StringLitIdx []int32       // maps a string slot in Consts → its StringLits index; non-string slots = -1
	Protos       []uint32      // ProtoIDs of nested Protos (indices; see the protos registry)
	SubNUps      []uint8       // aligned with the Protos indices: upvalue count of each sub-function (= number of pseudo-instructions following CLOSURE; used for symbexec exact skipping, obtained officially via p->p[bx]->nups)
	UpvalDescs   []UpvalDesc

	// Debug info (optional; whether to keep it is chosen per [architecture] — P1 keeps it to support traceback / error variable-name suffixes).
	LineInfo []int32    // source line of each instruction (len(LineInfo) == len(Code), or 0 = no debug info)
	LocVars  []LocalVar // local variable names + live ranges (09 §8.4)

	// IC slots (02 §7, indexed by pc).
	// Length == len(Code); slots for non-IC instructions are idle (may be repurposed for P2 arithmetic IC dual-counting, see 02 §7).
	IC []ICSlot

	// Compilability: P2 static compilability decision (`docs/design/p2-bridge/03-compilability-analysis.md`
	// §5.5). Written once at Compile time, shared read-only across States (the Go memory model
	// automatically guarantees visibility of a write-once-before-any-reader). See internal/bridge.Compilability for the value meanings:
	//   0 = CompUnknown (default, P1-only build / Compile did not run AnalyzeProto)
	//   1 = CompCompilable
	//   2 = CompNotCompilable
	// Stored as uint8 rather than pulling in the bridge type, to avoid a bytecode → bridge back-dependency.
	Compilability uint8
	// CompReasons: rejection-reason bitmask when CompNotCompilable (bits for F1-F7, same semantics as
	// internal/bridge.ReasonsBitmap). For diagnostics/logging, not a hot path.
	CompReasons uint16

	// IntrinsicCallPCs lists the pcs of CALL instructions whose callee is
	// a recognized math intrinsic (issue #77): a direct `math.<name>(...)`
	// or a `local f = math.<name>; f(...)` alias, where <name> is in
	// MathIntrinsicNames. The frontend records these at compile time (it
	// has the AST names + the alias dataflow); the P4 native CALL-density
	// gate reads them to treat such calls as cheap inline ops (SQRTSD /
	// ROUNDSD / ...) rather than exit-reason round trips, so a short
	// function calling only math intrinsics still promotes. Compile-time
	// write-once, cross-State read-only (same discipline as Compilability).
	// Purely a promotion heuristic input — correctness rests on the
	// segment's runtime intrinsic-identity guard, so a false entry (e.g.
	// `math` shadowed at run time) only risks an unprofitable promotion,
	// never a wrong result.
	IntrinsicCallPCs []int32
}

// MathIntrinsicNames is the set of math.* function names the P4 native
// JIT emits inline (issue #77). The frontend consults it to mark
// IntrinsicCallPCs. Kept in sync with the stdlib's name->kind intrinsic
// table (stdlib.mathIntrinsics); stdlib's TestMathIntrinsicNamesInSync
// asserts they match so a new intrinsic can't be added to one side only.
var MathIntrinsicNames = map[string]bool{
	"sqrt":  true,
	"floor": true,
	"ceil":  true,
	"abs":   true,
	"max":   true,
	"min":   true,
}

// IsStringConst reports whether Consts[i] is a string literal placeholder.
// On the LOADK path the caller uses this to decide whether to go through State.programStringRefs or read Consts[i] directly.
func (p *Proto) IsStringConst(i int) bool {
	return i < len(p.StringLitIdx) && p.StringLitIdx[i] >= 0
}

// ICSlot is an inline cache slot (02 §7 + 05 §6 finalized, includes tableRef / dual-count repurposing).
//
// Table IC (GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF):
//   - Shape: the target table's gen generation
//   - Index: the hit slot (array index / node index)
//   - TableRef: low 32 bits of the target table's arena offset (identity check, not a GC root)
//   - Kind: 0/uninitialized  1/array hit  2/node hit  3/mono-meta  4/megamorphic
//   - Refill: table-swap/shape-change refill count (P2 follow-up optimization round #4 megamorphic
//     proactive detection, simplified version of 02 §6.2 scheme (B)): P1 ic.go accumulates it on the
//     miss-after-fill path (previously hit some table/shape, but the current operation targets a
//     different table / different gen); during P2 aggregation, if ≥ MegamorphicRefillThreshold it is
//     proactively translated to FBTableMega (02 §6.3).
//
// Arithmetic IC (fast/slow path counts for ADD..POW, UNM, CONCAT):
//   - field repurposing: Shape = numHits (fast-path hit count)
//     Index = metaHits (metamethod slow-path hit count)
//     TableRef idle (set to 0)
//     Refill idle (set to 0)
//     Kind still used with P2 type-feedback semantics
type ICSlot struct {
	Shape    uint32
	Index    uint32
	TableRef uint32
	Kind     uint8
	Refill   uint8 // P2+ #4 refill count (used by table IC; saturates at 255)
}

// IC kind constants (02 §7).
const (
	ICKindNone        uint8 = 0
	ICKindArrayHit    uint8 = 1
	ICKindNodeHit     uint8 = 2
	ICKindMonoMeta    uint8 = 3
	ICKindMegamorphic uint8 = 4
)

// ProtoID is an index into the State.protos registry.
type ProtoID uint32

// HostFnID is an index into the State.hostFns registry (Go heap; 01 §1).
type HostFnID uint32

// HostFnIDSentinel marks "this CallInfo frame belongs to a host function" (05 §1.2).
const HostFnIDSentinel uint32 = 0xFFFFFFFF
