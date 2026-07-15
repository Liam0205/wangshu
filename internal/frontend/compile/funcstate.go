// Package compile lowers AST to a bytecode.Proto using Lua 5.1-style register
// allocation (04 §5). funcState is the per-function compilation context: it
// owns the freereg/nactvar water line, the local/upvalue tables, the jump
// patch chains, and the constant dedup table.
//
// Design: docs/design/p1-interpreter/04-frontend-parser-codegen.md §5-§9.
package compile

import (
	"fmt"
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/value"
)

// NoJump is the sentinel value terminating a JMP patch chain (04 §5.4).
const NoJump = -1

// codegen is the global context: the Proto registry and error collection
// shared across funcStates.
type codegen struct {
	source string
	protos []*bytecode.Proto // all child Protos registered in order (whether the main chunk is last or first is up to the caller)
}

// localVar describes one local variable within the current function (04 §5.1).
type localVar struct {
	name    string
	startPC int32
	endPC   int32
}

// upvalDesc is the codegen-time upvalue descriptor (04 §8.3).
type upvalDesc struct {
	name    string
	inStack bool
	idx     uint8
}

// blockCnt tracks the current syntactic block: the break chain, the scope's
// nactvar snapshot, and whether it captures an upvalue.
type blockCnt struct {
	prev        *blockCnt
	breakList   int
	nactvarSnap int
	isLoop      bool
	hasUpval    bool
}

// constKey is the dedup key used by addConst (number and string literals are encoded separately).
type constKey struct {
	kind uint8 // 0=number, 1=string, 2=nil, 3=true, 4=false
	bits uint64
	str  string
}

// funcState is the per-function codegen context (04 §5.1).
type funcState struct {
	proto *bytecode.Proto
	prev  *funcState
	cg    *codegen

	freereg    int
	nactvar    int
	actvar     []int      // active locals → locvars index
	locvars    []localVar // all locals (including exited ones, kept for debugging)
	upvals     []upvalDesc
	bl         *blockCnt
	jpc        int // chain head = JMP chain pending patch onto the next instruction (04 §5.4)
	lastTarget int // most recent jump target pc (for the safe instruction-merge optimization; in this P1 it is only a sentinel for now)

	consts map[constKey]int // constant dedup (04 §11)

	// localFnAsts maps the local function name → AST for locals defined via
	// the `local function X` form within this funcState (extended by the P4 PJ5
	// scope-aware analyzer, so an inner Proto's AnalyzeProto knows the outer's
	// known local functions and recognizes a GETUPVAL+CALL+RETURN void call to
	// an outer local fn as a known rather than unknown call).
	//
	// **Scope**: only fns registered by stmtLocalFunc within this funcState
	// (LocalFuncStmt); the composite `local f = function() end` AssignStmt+FuncExpr
	// form is not tracked for now (left to the next commit to extend); global /
	// table-field fns are never tracked (they can be overwritten at runtime).
	localFnAsts map[string]*ast.FuncExpr

	// localAliasAsts tracks `local sqrt = math.sqrt`-style bindings: local
	// name -> the RHS expression, registered by stmtLocal when the RHS is
	// a NameExpr or lib.method IndexExpr shape. The analyzer (bridge.
	// AnalyzeProtoWithOuter) filters these through its stdlib whitelist —
	// this map only carries the dataflow fact "name X was bound to
	// expression E and never reassigned in this funcState". Same channel
	// discipline as localFnAsts: inner Protos inherit the merged outer
	// view so alias calls resolve across closure boundaries.
	localAliasAsts map[string]ast.Expr

	isVararg bool
}

// newFuncState creates a new function-level codegen state.
func newFuncState(cg *codegen, prev *funcState, source string, line int32) *funcState {
	fs := &funcState{
		proto: &bytecode.Proto{
			Source:      source,
			LineDefined: line,
		},
		prev:           prev,
		cg:             cg,
		jpc:            NoJump,
		lastTarget:     -1,
		consts:         map[constKey]int{},
		localFnAsts:    map[string]*ast.FuncExpr{},
		localAliasAsts: map[string]ast.Expr{},
	}
	return fs
}

// emit appends a 32-bit instruction with the source line and an empty IC slot.
// Before every emit, dischargeJpc first: patch the entire jpc chain onto the current pc.
func (fs *funcState) emit(line int32, instr bytecode.Instruction) int {
	fs.dischargeJpc()
	pc := len(fs.proto.Code)
	fs.proto.Code = append(fs.proto.Code, instr)
	fs.proto.LineInfo = append(fs.proto.LineInfo, line)
	fs.proto.IC = append(fs.proto.IC, bytecode.ICSlot{})
	return pc
}

// emitABC / emitABx / emitAsBx are convenience wrappers that emit by format.
func (fs *funcState) emitABC(line int32, op bytecode.OpCode, a, b, c int) int {
	return fs.emit(line, bytecode.EncodeABC(op, a, b, c))
}
func (fs *funcState) emitABx(line int32, op bytecode.OpCode, a, bx int) int {
	return fs.emit(line, bytecode.EncodeABx(op, a, bx))
}
func (fs *funcState) emitAsBx(line int32, op bytecode.OpCode, a, sbx int) int {
	return fs.emit(line, bytecode.EncodeAsBx(op, a, sbx))
}

// pc returns the position of the next instruction to be emitted.
func (fs *funcState) pc() int { return len(fs.proto.Code) }

// reserveRegs raises the water line and updates MaxStack (04 §5.3).
func (fs *funcState) reserveRegs(line int32, n int) {
	fs.checkStack(line, n)
	fs.freereg += n
	if fs.freereg > int(fs.proto.MaxStack) {
		fs.proto.MaxStack = uint8(fs.freereg)
	}
}

// checkStack raises "function or expression too complex" when the water line + n exceeds MaxStack (250) (04 §9).
func (fs *funcState) checkStack(line int32, n int) {
	if fs.freereg+n > bytecode.MaxStack {
		raise(fs, line, "function or expression too complex")
	}
}

// freeReg frees one temporary register (must be ≥ nactvar and at the top of stack, i.e. "the one reserved last").
func (fs *funcState) freeReg(r int) {
	if r >= fs.nactvar && r == fs.freereg-1 {
		fs.freereg--
	}
}

// freeExp returns e's register if e is an ENonReloc temporary.
func (fs *funcState) freeExp(e *expDesc) {
	if e.k == eNonReloc {
		fs.freeReg(e.info)
	}
}

// addConst dedups and returns the constant index (04 §11). Strings take the lazy intern path (Proto.StringLits).
func (fs *funcState) addConst(line int32, key constKey, v value.Value, lit string) int {
	if idx, ok := fs.consts[key]; ok {
		return idx
	}
	idx := len(fs.proto.Consts)
	if idx > bytecode.MaxBx {
		raise(fs, line, "constant table overflow")
	}
	fs.proto.Consts = append(fs.proto.Consts, v)
	if key.kind == 1 { // string literal — lazy intern (see Proto's lazy-literal-intern comment).
		litIdx := int32(len(fs.proto.StringLits))
		fs.proto.StringLits = append(fs.proto.StringLits, lit)
		fs.proto.StringLitIdx = append(fs.proto.StringLitIdx, litIdx)
	} else {
		fs.proto.StringLitIdx = append(fs.proto.StringLitIdx, -1)
	}
	fs.consts[key] = idx
	return idx
}

func numConstKey(f float64) constKey {
	// canonicalize NaN to mirror runtime NumberValue (04 §5.5).
	if f != f {
		return constKey{kind: 0, bits: value.CanonNaN()}
	}
	// Zero normalization: PUC addk dedups by numeric equality (luaH_set number
	// key), so +0.0 == -0.0 hits the same slot and physically stores whichever
	// zero arrived first (keeping its sign). Float64bits distinguishes the sign
	// bit of ±0, which would make the two occupy separate constant slots, so a
	// folded -0.0 no longer reuses the earlier +0.0 → tostring wrongly yields
	// "-0" (should be "0"). Normalizing ±0 to a shared key lets addConst's
	// first-come-first-served automatically preserve the first sign.
	if f == 0 {
		return constKey{kind: 0, bits: 0}
	}
	return constKey{kind: 0, bits: math.Float64bits(f)}
}
func strConstKey(s string) constKey { return constKey{kind: 1, str: s} }

func (fs *funcState) numK(line int32, f float64) int {
	return fs.addConst(line, numConstKey(f), value.NumberValue(f), "")
}
func (fs *funcState) strK(line int32, s string) int {
	return fs.addConst(line, strConstKey(s), value.Nil, s)
}

// findLocal searches the current function's active locals in reverse order (later declarations shadow earlier ones). Returns the register number or -1.
func (fs *funcState) findLocal(name string) int {
	for i := fs.nactvar - 1; i >= 0; i-- {
		if fs.locvars[fs.actvar[i]].name == name {
			return i
		}
	}
	return -1
}

// findUpval looks up a duplicate name among the registered upvalues; returns the index or -1.
func (fs *funcState) findUpval(name string) int {
	for i, u := range fs.upvals {
		if u.name == name {
			return i
		}
	}
	return -1
}

// addUpval registers a new upvalue and returns its index.
func (fs *funcState) addUpval(line int32, name string, inStack bool, idx uint8) int {
	if len(fs.upvals) >= bytecode.MaxUpvalues {
		raise(fs, line, "too many upvalues")
	}
	fs.upvals = append(fs.upvals, upvalDesc{name: name, inStack: inStack, idx: idx})
	fs.proto.UpvalDescs = append(fs.proto.UpvalDescs, bytecode.UpvalDesc{Name: name, InStack: inStack, Idx: idx})
	return len(fs.upvals) - 1
}

// registerLocal declares a new local variable (must be called after the RHS is evaluated, 04 §5.8).
func (fs *funcState) registerLocal(line int32, name string) int {
	if fs.nactvar >= bytecode.MaxLocVars {
		raise(fs, line, "too many local variables")
	}
	idx := len(fs.locvars)
	fs.locvars = append(fs.locvars, localVar{name: name, startPC: int32(fs.pc())})
	if fs.nactvar < len(fs.actvar) {
		fs.actvar[fs.nactvar] = idx
	} else {
		fs.actvar = append(fs.actvar, idx)
	}
	fs.nactvar++
	return fs.nactvar - 1
}

// removeVars pops back to level active locals, closing their live ranges (04 §5.9).
func (fs *funcState) removeVars(level int) {
	for fs.nactvar > level {
		fs.nactvar--
		fs.locvars[fs.actvar[fs.nactvar]].endPC = int32(fs.pc())
	}
}

// enterBlock enters a block.
func (fs *funcState) enterBlock(isLoop bool) {
	fs.bl = &blockCnt{
		prev:        fs.bl,
		breakList:   NoJump,
		nactvarSnap: fs.nactvar,
		isLoop:      isLoop,
	}
	if fs.freereg != fs.nactvar {
		// Design-time invariant (04 §5.1): the enter-block invariant.
		// A violation is a codegen bug, so panic to expose it directly (unit tests will catch it).
		panic(fmt.Sprintf("compile: enterBlock invariant violated: freereg=%d nactvar=%d",
			fs.freereg, fs.nactvar))
	}
}

// leaveBlock leaves a block: close live ranges, free temporaries, CLOSE open upvalues (04 §6.1).
func (fs *funcState) leaveBlock(line int32) {
	bl := fs.bl
	fs.removeVars(bl.nactvarSnap)
	if bl.hasUpval {
		fs.emitABC(line, bytecode.CLOSE, bl.nactvarSnap, 0, 0)
	}
	fs.bl = bl.prev
	fs.freereg = fs.nactvar
	if bl.isLoop {
		fs.patchToHere(bl.breakList)
	}
}

// ----- Jump chains -----

// jump emits a JMP; its sBx temporarily stores the jpc chain link (the linked list is embedded in the instruction stream, 04 §5.4).
func (fs *funcState) jump(line int32) int {
	jpc := fs.jpc
	fs.jpc = NoJump
	pc := fs.emitAsBx(line, bytecode.JMP, 0, NoJump)
	fs.concat(&pc, jpc)
	return pc
}

// getJump reads the link of the JMP at pc, returning the next pc in the chain or NoJump.
func (fs *funcState) getJump(pc int) int {
	off := bytecode.SBx(fs.proto.Code[pc])
	if off == NoJump {
		return NoJump
	}
	return pc + 1 + off
}

// fixJump rewrites the sBx of the jump at pc (JMP/FORPREP/FORLOOP) into a relative offset pointing to dest (04 §5.4).
//
// The original op/A fields are left unchanged (so this helper also works for non-JMP instructions carrying an sBx, like FORPREP/FORLOOP).
func (fs *funcState) fixJump(line int32, pc, dest int) {
	if dest == NoJump {
		fs.proto.Code[pc] = bytecode.SetSBx(fs.proto.Code[pc], NoJump)
		return
	}
	off := dest - (pc + 1)
	if off > bytecode.SBxBias || off < -bytecode.SBxBias {
		raise(fs, line, "control structure too long")
	}
	fs.proto.Code[pc] = bytecode.SetSBx(fs.proto.Code[pc], off)
}

// concat appends chain l2 to the tail of *l1 (04 §5.4).
func (fs *funcState) concat(l1 *int, l2 int) {
	if l2 == NoJump {
		return
	}
	if *l1 == NoJump {
		*l1 = l2
		return
	}
	cur := *l1
	for {
		nxt := fs.getJump(cur)
		if nxt == NoJump {
			break
		}
		cur = nxt
	}
	fs.proto.Code[cur] = bytecode.SetSBx(fs.proto.Code[cur], l2-(cur+1))
}

// patchList patches the entire chain list onto target (via patchListAux, degrading ownerless TESTSETs).
func (fs *funcState) patchList(list, target int) {
	if target == fs.pc() {
		fs.patchToHere(list)
		return
	}
	fs.patchListAux(list, target, bytecode.NoRegister, target)
}

// patchToHere merges list into jpc (patched together on the next instruction).
func (fs *funcState) patchToHere(list int) {
	fs.lastTarget = fs.pc()
	fs.concat(&fs.jpc, list)
}

// dischargeJpc patches the entire jpc onto the current pc before every emit.
//
// Aligned with Lua 5.1 dischargejpc: it must go through patchListAux(reg=NoRegister)
// so that ownerless TESTSETs on the chain degrade to TEST — otherwise a TESTSET's
// A=255 placeholder would write to an out-of-bounds register.
func (fs *funcState) dischargeJpc() {
	if fs.jpc == NoJump {
		return
	}
	list := fs.jpc
	fs.jpc = NoJump
	fs.patchListAux(list, fs.pc(), bytecode.NoRegister, fs.pc())
}

// getLabel returns the current pc and marks it as a jump target (refreshing lastTarget and incidentally patching the pending jpc).
func (fs *funcState) getLabel() int {
	fs.lastTarget = fs.pc()
	fs.dischargeJpc()
	return fs.pc()
}

// ----- Error helpers -----

// CompileError carries source/line for compile-time diagnostics (04 §9).
type CompileError struct {
	Source string
	Line   int32
	Msg    string
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("%s:%d: %s", bytecode.ChunkID(e.Source), e.Line, e.Msg)
}

// raise throws via panic(*CompileError); the top-level Compile catches it with recover.
func raise(fs *funcState, line int32, format string, args ...any) {
	src := ""
	if fs != nil && fs.proto != nil {
		src = fs.proto.Source
	}
	if src == "" && fs != nil && fs.cg != nil {
		src = fs.cg.source
	}
	panic(&CompileError{Source: src, Line: line, Msg: fmt.Sprintf(format, args...)})
}
