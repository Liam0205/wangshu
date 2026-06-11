// Package compile lowers AST to a bytecode.Proto using Lua 5.1-style register
// allocation (04 §5). funcState is the per-function compilation context: it
// owns the freereg/nactvar water line, the local/upvalue tables, the jump
// patch chains, and the constant dedup table.
//
// 设计:docs/design/p1-interpreter/04-frontend-parser-codegen.md §5-§9。
package compile

import (
	"fmt"
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// NoJump is the sentinel value terminating a JMP patch chain (04 §5.4).
const NoJump = -1

// codegen 全局上下文:跨 funcState 共享的 Proto 注册表与错误收集。
type codegen struct {
	source string
	protos []*bytecode.Proto // 顺序登记的所有子 Proto(主 chunk 在最后或最前由调用方决定)
}

// localVar 描述当前函数内一个局部变量(04 §5.1)。
type localVar struct {
	name    string
	startPC int32
	endPC   int32
}

// upvalDesc 是 codegen 期的 upvalue 描述(04 §8.3)。
type upvalDesc struct {
	name    string
	inStack bool
	idx     uint8
}

// blockCnt 跟踪当前语法块:break 链、作用域 nactvar 快照、是否捕获 upvalue。
type blockCnt struct {
	prev        *blockCnt
	breakList   int
	nactvarSnap int
	isLoop      bool
	hasUpval    bool
}

// constKey 是 addConst 用的去重 key(数字/字符串字面量分别编码)。
type constKey struct {
	kind uint8 // 0=number, 1=string, 2=nil, 3=true, 4=false
	bits uint64
	str  string
}

// funcState 是单函数 codegen 上下文(04 §5.1)。
type funcState struct {
	proto *bytecode.Proto
	prev  *funcState
	cg    *codegen

	freereg    int
	nactvar    int
	actvar     []int      // 活跃局部 → locvars 索引
	locvars    []localVar // 所有局部(含已退出的,留作调试)
	upvals     []upvalDesc
	bl         *blockCnt
	jpc        int // 链头 = 待回填到下一指令的 JMP 链(04 §5.4)
	lastTarget int // 最近的跳转目标 pc(用于安全合并指令优化,本 P1 暂仅作哨兵)

	consts map[constKey]int // 常量去重(04 §11)

	isVararg bool
}

// newFuncState 创建一个新的函数级 codegen 状态。
func newFuncState(cg *codegen, prev *funcState, source string, line int32) *funcState {
	fs := &funcState{
		proto: &bytecode.Proto{
			Source:      source,
			LineDefined: line,
		},
		prev:       prev,
		cg:         cg,
		jpc:        NoJump,
		lastTarget: -1,
		consts:     map[constKey]int{},
	}
	return fs
}

// emit appends a 32-bit instruction with the source line and an empty IC slot.
// 每次 emit 之前先 dischargeJpc:把 jpc 链全部回填到当前 pc。
func (fs *funcState) emit(line int32, instr bytecode.Instruction) int {
	fs.dischargeJpc()
	pc := len(fs.proto.Code)
	fs.proto.Code = append(fs.proto.Code, instr)
	fs.proto.LineInfo = append(fs.proto.LineInfo, line)
	fs.proto.IC = append(fs.proto.IC, bytecode.ICSlot{})
	return pc
}

// emitABC / emitABx / emitAsBx 是按格式发射的便捷封装。
func (fs *funcState) emitABC(line int32, op bytecode.OpCode, a, b, c int) int {
	return fs.emit(line, bytecode.EncodeABC(op, a, b, c))
}
func (fs *funcState) emitABx(line int32, op bytecode.OpCode, a, bx int) int {
	return fs.emit(line, bytecode.EncodeABx(op, a, bx))
}
func (fs *funcState) emitAsBx(line int32, op bytecode.OpCode, a, sbx int) int {
	return fs.emit(line, bytecode.EncodeAsBx(op, a, sbx))
}

// pc 返回下一条将发射指令的位置。
func (fs *funcState) pc() int { return len(fs.proto.Code) }

// reserveRegs 抬高水位线,并刷 MaxStack(04 §5.3)。
func (fs *funcState) reserveRegs(line int32, n int) {
	fs.checkStack(line, n)
	fs.freereg += n
	if fs.freereg > int(fs.proto.MaxStack) {
		fs.proto.MaxStack = uint8(fs.freereg)
	}
}

// checkStack 在水位 + n 超 MaxStack(250)时报「function or expression too complex」(04 §9)。
func (fs *funcState) checkStack(line int32, n int) {
	if fs.freereg+n > bytecode.MaxStack {
		raise(fs, line, "function or expression too complex")
	}
}

// freeReg 释放一个临时寄存器(必须是 ≥ nactvar 且为栈顶,即"上一次 reserve 的那一个")。
func (fs *funcState) freeReg(r int) {
	if r >= fs.nactvar && r == fs.freereg-1 {
		fs.freereg--
	}
}

// freeExp 若 e 是 ENonReloc 临时,归还其寄存器。
func (fs *funcState) freeExp(e *expDesc) {
	if e.k == eNonReloc {
		fs.freeReg(e.info)
	}
}

// addConst 去重并返回常量索引(04 §11)。字符串走惰性 intern 路径(Proto.StringLits)。
func (fs *funcState) addConst(line int32, key constKey, v value.Value, lit string) int {
	if idx, ok := fs.consts[key]; ok {
		return idx
	}
	idx := len(fs.proto.Consts)
	if idx > bytecode.MaxBx {
		raise(fs, line, "constant table overflow")
	}
	fs.proto.Consts = append(fs.proto.Consts, v)
	if key.kind == 1 { // string literal — lazy intern (Proto §字面量惰性 intern 注释)。
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
	// canonicalize NaN to mirror runtime NumberValue (04 §5.5)。
	if f != f {
		return constKey{kind: 0, bits: value.CanonNaN()}
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

// findLocal 在当前函数活跃局部里逆序查找(后声明的覆盖先声明)。返回寄存器号或 -1。
func (fs *funcState) findLocal(name string) int {
	for i := fs.nactvar - 1; i >= 0; i-- {
		if fs.locvars[fs.actvar[i]].name == name {
			return i
		}
	}
	return -1
}

// findUpval 在已登记的 upvalue 里查重名;返回索引或 -1。
func (fs *funcState) findUpval(name string) int {
	for i, u := range fs.upvals {
		if u.name == name {
			return i
		}
	}
	return -1
}

// addUpval 登记一个新 upvalue,返回索引。
func (fs *funcState) addUpval(line int32, name string, inStack bool, idx uint8) int {
	if len(fs.upvals) >= bytecode.MaxUpvalues {
		raise(fs, line, "too many upvalues")
	}
	fs.upvals = append(fs.upvals, upvalDesc{name: name, inStack: inStack, idx: idx})
	fs.proto.UpvalDescs = append(fs.proto.UpvalDescs, bytecode.UpvalDesc{Name: name, InStack: inStack, Idx: idx})
	return len(fs.upvals) - 1
}

// registerLocal 声明一个新局部变量(必须在 RHS 求值之后调用,04 §5.8)。
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

// removeVars 退到 level 个活跃局部,关闭它们的活跃区间(04 §5.9)。
func (fs *funcState) removeVars(level int) {
	for fs.nactvar > level {
		fs.nactvar--
		fs.locvars[fs.actvar[fs.nactvar]].endPC = int32(fs.pc())
	}
}

// enterBlock 进块。
func (fs *funcState) enterBlock(isLoop bool) {
	fs.bl = &blockCnt{
		prev:        fs.bl,
		breakList:   NoJump,
		nactvarSnap: fs.nactvar,
		isLoop:      isLoop,
	}
	if fs.freereg != fs.nactvar {
		// 设计期不变式 (04 §5.1):进块不变式
		// 出现违例属于 codegen bug,直接 panic 暴露(单测会捕获)。
		panic(fmt.Sprintf("compile: enterBlock invariant violated: freereg=%d nactvar=%d",
			fs.freereg, fs.nactvar))
	}
}

// leaveBlock 出块:闭合活跃区间、释放临时、CLOSE 开放 upvalue(04 §6.1)。
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

// innerLoopBlock 找最内层 isLoop=true 的块(用于 break)。
func (fs *funcState) innerLoopBlock() *blockCnt {
	for b := fs.bl; b != nil; b = b.prev {
		if b.isLoop {
			return b
		}
	}
	return nil
}

// ----- 跳转链 -----

// jump 发射一条 JMP,sBx 临时存 jpc 链入(链表嵌指令流,04 §5.4)。
func (fs *funcState) jump(line int32) int {
	jpc := fs.jpc
	fs.jpc = NoJump
	pc := fs.emitAsBx(line, bytecode.JMP, 0, NoJump)
	fs.concat(&pc, jpc)
	return pc
}

// getJump 读 pc 处 JMP 的链接,返回链中下一 pc 或 NoJump。
func (fs *funcState) getJump(pc int) int {
	off := bytecode.SBx(fs.proto.Code[pc])
	if off == NoJump {
		return NoJump
	}
	return pc + 1 + off
}

// fixJump 把 pc 处跳转(JMP/FORPREP/FORLOOP)的 sBx 改写为指向 dest 的相对偏移(04 §5.4)。
//
// 保留原 op/A 字段不变(使本 helper 也可用于 FORPREP/FORLOOP 这类带 sBx 的非 JMP 指令)。
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

// concat 把链 l2 接到 *l1 尾部(04 §5.4)。
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

// patchList 把整条链 list 全部回填到 target(经 patchListAux 退化无主 TESTSET)。
func (fs *funcState) patchList(list, target int) {
	if target == fs.pc() {
		fs.patchToHere(list)
		return
	}
	fs.patchListAux(list, target, bytecode.NoRegister, target)
}

// patchToHere 把 list 合并进 jpc(下一条指令时一起回填)。
func (fs *funcState) patchToHere(list int) {
	fs.lastTarget = fs.pc()
	fs.concat(&fs.jpc, list)
}

// dischargeJpc 在每次发射前把 jpc 全部回填到当前 pc。
//
// 对齐 Lua 5.1 dischargejpc:必须走 patchListAux(reg=NoRegister),让链上
// 无主 TESTSET 退化为 TEST——否则 TESTSET 的 A=255 占位会写越界寄存器。
func (fs *funcState) dischargeJpc() {
	if fs.jpc == NoJump {
		return
	}
	list := fs.jpc
	fs.jpc = NoJump
	fs.patchListAux(list, fs.pc(), bytecode.NoRegister, fs.pc())
}

// getLabel 返回当前 pc 并标记为跳转目标(刷新 lastTarget,顺带把待 jpc 回填)。
func (fs *funcState) getLabel() int {
	fs.lastTarget = fs.pc()
	fs.dischargeJpc()
	return fs.pc()
}

// ----- 错误辅助 -----

// CompileError carries source/line for compile-time diagnostics (04 §9).
type CompileError struct {
	Source string
	Line   int32
	Msg    string
}

func (e *CompileError) Error() string { return fmt.Sprintf("%s:%d: %s", e.Source, e.Line, e.Msg) }

// raise 通过 panic(*CompileError) 抛出,顶层 Compile 用 recover 捕获。
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
