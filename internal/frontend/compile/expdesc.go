// expdesc — 延迟物化的表达式描述(04 §5.2),与 Lua 5.1 lcode.c 同构。
//
// 表达式被先计算成 expDesc(描述"这个值现在在哪、怎么取"),到必须落寄存器/作 RK/参与
// 跳转时才 discharge。短路逻辑(and/or)与比较则同时驱动 t/f 跳转链。
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// expKind 列出表达式的延迟物化形态(04 §5.2)。
type expKind uint8

const (
	eVoid      expKind = iota // 无值占位
	eNil                      // 字面 nil(尚未发指令)
	eTrue                     // 字面 true
	eFalse                    // 字面 false
	eKNum                     // 数字字面量(尚未入常量池),值在 nval
	eK                        // 已在常量池,info = K 索引
	eLocal                    // 局部变量,info = 寄存器号
	eUpval                    // upvalue,info = upvalue 索引
	eGlobal                   // 全局,info = 名字的 K 索引
	eIndexed                  // t[k]:info = 表寄存器, aux = 键 RK
	eJmp                      // 比较 (EQ/LT/LE) 已发,info = JMP 指令 pc(其后 JMP 待定/已链)
	eRelocable                // 结果寄存器未定的指令(GETGLOBAL/GETTABLE/NEWTABLE/CALL单值),info = 指令 pc
	eNonReloc                 // 结果已落某寄存器,info = 寄存器号
	eCall                     // 函数调用,info = CALL 指令 pc(可改 C)
	eVararg                   // ... ,info = VARARG 指令 pc
)

// expDesc 是表达式描述符(04 §5.2)。
type expDesc struct {
	k    expKind
	info int     // 含义随 k(见上)
	aux  int     // eIndexed 的键 RK
	nval float64 // eKNum 的数字
	tJmp int     // 真则跳的回填链(NoJump=空)
	fJmp int     // 假则跳的回填链
}

func newExp(k expKind, info int) expDesc {
	return expDesc{k: k, info: info, tJmp: NoJump, fJmp: NoJump}
}

func (e *expDesc) hasJumps() bool { return e.tJmp != NoJump || e.fJmp != NoJump }

// dischargeVars 把 EGlobal/EUpval/ELocal/EIndexed 翻译成"可取值形式"(可能仍是 ERelocable)。
//
// 04 §5.3:对应 Lua 5.1 luaK_dischargevars。
func (fs *funcState) dischargeVars(line int32, e *expDesc) {
	switch e.k {
	case eLocal:
		e.k = eNonReloc
	case eUpval:
		pc := fs.emitABC(line, bytecode.GETUPVAL, 0, e.info, 0)
		e.k = eRelocable
		e.info = pc
	case eGlobal:
		pc := fs.emitABx(line, bytecode.GETGLOBAL, 0, e.info)
		e.k = eRelocable
		e.info = pc
	case eIndexed:
		// 先归还 RK 占用的寄存器(若有),再发 GETTABLE。
		if !bytecode.IsK(e.aux) {
			fs.freeReg(e.aux)
		}
		fs.freeReg(e.info)
		pc := fs.emitABC(line, bytecode.GETTABLE, 0, e.info, e.aux)
		e.k = eRelocable
		e.info = pc
	case eVararg, eCall:
		fs.setOneRet(e)
	}
}

// setOneRet 设置一个 Call/Vararg 表达式只取 1 个返回值。
func (fs *funcState) setOneRet(e *expDesc) {
	switch e.k {
	case eCall:
		// CALL 的 C = 取值数+1。1 值 ⇒ C=2。
		ins := fs.proto.Code[e.info]
		fs.proto.Code[e.info] = bytecode.SetC(ins, 2)
		e.k = eNonReloc
		e.info = bytecode.A(ins) // 调用结果落 R(A)
	case eVararg:
		// VARARG 的 B = 取值数+1。1 值 ⇒ B=2。
		ins := fs.proto.Code[e.info]
		fs.proto.Code[e.info] = bytecode.SetB(ins, 2)
		e.k = eRelocable
	}
}

// setReturns 设置一个 Call/Vararg 表达式的返回值数(用于 explist 末位多值)。
// nResults < 0 表示"到 top"(B/C=0)。
func (fs *funcState) setReturns(e *expDesc, nResults int) {
	switch e.k {
	case eCall:
		c := nResults + 1
		if nResults < 0 {
			c = 0
		}
		fs.proto.Code[e.info] = bytecode.SetC(fs.proto.Code[e.info], c)
	case eVararg:
		b := nResults + 1
		if nResults < 0 {
			b = 0
		}
		fs.proto.Code[e.info] = bytecode.SetB(fs.proto.Code[e.info], b)
	}
}

// openMultiRet 把末位多值源(eCall/eVararg)展开为 nResults 个值(<0 = 到 top),
// 返回 true 表示 e 确实是多值源(调用方走多值分支)。
//
// 同构逻辑单点收口——历史上 stmtReturn / compileArgList / exprTable 三处手写,
// 同族 bug 出了三次(eCall 误 SetA 把"函数所在槽"改写,返回值落错寄存器):
//   - eCall:CALL 的 A 已是 fnReg(结果落点),绝不可改写;
//   - eVararg:VARARG 发射时 A=0 占位,此处统一回填 A=freereg(多值起点)。
func (fs *funcState) openMultiRet(e *expDesc, nResults int) bool {
	switch e.k {
	case eCall:
		fs.setReturns(e, nResults)
		return true
	case eVararg:
		fs.setReturns(e, nResults)
		fs.proto.Code[e.info] = bytecode.SetA(fs.proto.Code[e.info], fs.freereg)
		return true
	}
	return false
}

// dischargeToAnyReg 把 e 物化到某寄存器(若已在则原地)。
func (fs *funcState) dischargeToAnyReg(line int32, e *expDesc) {
	if e.k != eNonReloc {
		fs.reserveRegs(line, 1)
		fs.dischargeToReg(line, e, fs.freereg-1)
	}
}

// exp2AnyReg 把 e 物化到某寄存器并返回寄存器号。
func (fs *funcState) exp2AnyReg(line int32, e *expDesc) int {
	fs.dischargeVars(line, e)
	if e.k == eNonReloc {
		if !e.hasJumps() {
			return e.info
		}
		if e.info >= fs.nactvar { // 是临时:可在原寄存器物化
			fs.exp2reg(line, e, e.info)
			return e.info
		}
	}
	fs.exp2NextReg(line, e)
	return e.info
}

// exp2NextReg 把 e 落到 freereg 并把水位 +1(变成 ENonReloc)。
func (fs *funcState) exp2NextReg(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	fs.freeExp(e)
	fs.reserveRegs(line, 1)
	fs.exp2reg(line, e, fs.freereg-1)
}

// dischargeToReg 把 e 落到指定寄存器 reg(不处理 t/f 跳转链)。
func (fs *funcState) dischargeToReg(line int32, e *expDesc, reg int) {
	fs.dischargeVars(line, e)
	switch e.k {
	case eNil:
		fs.emitABC(line, bytecode.LOADNIL, reg, reg, 0)
	case eTrue:
		fs.emitABC(line, bytecode.LOADBOOL, reg, 1, 0)
	case eFalse:
		fs.emitABC(line, bytecode.LOADBOOL, reg, 0, 0)
	case eK:
		fs.emitABx(line, bytecode.LOADK, reg, e.info)
	case eKNum:
		k := fs.numK(line, e.nval)
		fs.emitABx(line, bytecode.LOADK, reg, k)
	case eRelocable:
		fs.proto.Code[e.info] = bytecode.SetA(fs.proto.Code[e.info], reg)
	case eNonReloc:
		if reg != e.info {
			fs.emitABC(line, bytecode.MOVE, reg, e.info, 0)
		}
	case eJmp:
		// 留给 exp2reg 处理(此处不发指令)
		return
	default:
		// eVoid 不应到这
	}
	e.k = eNonReloc
	e.info = reg
}

// exp2reg 把"带跳转的表达式"物化到具体寄存器(04 §5.7)。
func (fs *funcState) exp2reg(line int32, e *expDesc, reg int) {
	fs.dischargeToReg(line, e, reg)
	if e.k == eJmp { // 比较自身的 JMP 计入 t 链
		fs.concat(&e.tJmp, e.info)
	}
	if e.hasJumps() {
		var pf, pt, final int
		if needValue(fs, e.tJmp) || needValue(fs, e.fJmp) {
			fj := NoJump
			if e.k != eJmp {
				fj = fs.jump(line)
			}
			pf = fs.codeLoadBool(line, reg, 0, 1) // false 并跳过下一条
			pt = fs.codeLoadBool(line, reg, 1, 0)
			fs.patchToHere(fj)
			final = fs.getLabel()
		} else {
			pf = NoJump
			pt = NoJump
			final = fs.getLabel()
		}
		fs.patchListAux(e.fJmp, final, reg, pf)
		fs.patchListAux(e.tJmp, final, reg, pt)
	}
	e.fJmp, e.tJmp = NoJump, NoJump
	e.k = eNonReloc
	e.info = reg
}

// codeLoadBool 发射 LOADBOOL R(A)=B,if C!=0 then pc++(返回 pc)。
func (fs *funcState) codeLoadBool(line int32, a, b, c int) int {
	return fs.emitABC(line, bytecode.LOADBOOL, a, b, c)
}

// needValue 判定链中是否含"非 TESTSET"的 JMP(那些需要 LOADBOOL 兜底落值)。
func needValue(fs *funcState, list int) bool {
	for ; list != NoJump; list = fs.getJump(list) {
		if list == 0 {
			return true
		}
		ins := fs.proto.Code[list-1]
		if bytecode.Op(ins) != bytecode.TESTSET {
			return true
		}
	}
	return false
}

// patchTestReg 若 list 前一条是 TESTSET,把它的 A=reg 并把跳转回填到 vtarget;
// 否则把 TESTSET 退化为 TEST 并把跳转回填到 dtarget(04 §5.4)。
//
// 返回 true 表示已处理(把这条 JMP 接到 vtarget),false 表示退化(接到 dtarget)。
func (fs *funcState) patchTestReg(node, reg int) bool {
	if node == 0 {
		return false
	}
	ctrl := node - 1
	ins := fs.proto.Code[ctrl]
	if bytecode.Op(ins) != bytecode.TESTSET {
		return false
	}
	if reg != bytecode.NoRegister && reg != bytecode.B(ins) {
		fs.proto.Code[ctrl] = bytecode.SetA(ins, reg)
	} else {
		// 退化为 TEST:A=B(源寄存器), C 不变
		fs.proto.Code[ctrl] = bytecode.EncodeABC(bytecode.TEST,
			bytecode.B(ins), 0, bytecode.C(ins))
	}
	return true
}

// patchListAux 遍历链:对每个节点用 patchTestReg(node, reg) 判定走 vtarget 还是 dtarget。
func (fs *funcState) patchListAux(list, vtarget, reg, dtarget int) {
	for list != NoJump {
		nxt := fs.getJump(list)
		if fs.patchTestReg(list, reg) {
			fs.fixJump(0, list, vtarget)
		} else {
			fs.fixJump(0, list, dtarget)
		}
		list = nxt
	}
}

// exp2RK 尽量把 e 折叠为 RK 形式(返回 RK 操作数);否则物化到寄存器返回寄存器号(04 §5.3)。
//
// 注意:nil/true/false 不入常量池(02 §5),走 RK 时会落寄存器(LOADNIL/LOADBOOL)。
func (fs *funcState) exp2RK(line int32, e *expDesc) int {
	fs.exp2Val(line, e)
	switch e.k {
	case eK:
		if e.info < bytecode.MaxK {
			return e.info + bytecode.MaxK
		}
	case eKNum:
		k := fs.numK(line, e.nval)
		if k < bytecode.MaxK {
			e.k = eK
			e.info = k
			return k + bytecode.MaxK
		}
		e.k = eK
		e.info = k
	}
	return fs.exp2AnyReg(line, e)
}

// exp2Val 若 e 带未决跳转链,先合流物化(给 exp2RK / exp2AnyReg 之前)。
func (fs *funcState) exp2Val(line int32, e *expDesc) {
	if e.hasJumps() {
		fs.exp2AnyReg(line, e)
	} else {
		fs.dischargeVars(line, e)
	}
}

// goIfTrue:若 e 为真则继续,为假则跳(把跳链入 e.fJmp)。配合 04 §5.6 短路。
//
// 严格对齐 5.1 luaK_goiftrue:VK/VKNUM/VTRUE 常真不跳;VFALSE 恒跳(短路值
// 恰为 false,LOADBOOL 物化正确);VNIL 落 default jumpOnCond(TESTSET 保留
// 原值——`nil and 2` 须返回 nil,LOADBOOL 会错产 false)。
func (fs *funcState) goIfTrue(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	var pc int
	switch e.k {
	case eK, eKNum, eTrue:
		pc = NoJump
	case eFalse:
		pc = fs.jump(line)
	case eJmp:
		fs.invertJmp(e)
		pc = e.info
	default:
		pc = fs.jumpOnCond(line, e, 0)
	}
	fs.concat(&e.fJmp, pc)
	fs.patchToHere(e.tJmp)
	e.tJmp = NoJump
}

// goIfFalse:若 e 为假则继续,为真则跳(把跳链入 e.tJmp)。
//
// 严格对齐 5.1 luaK_goiffalse:VNIL/VFALSE 常假不跳;VTRUE 恒跳(短路值恰为
// true);VK/VKNUM 落 default jumpOnCond(TESTSET 保留原值——`1 or 2` 须返回
// 1 而非 true)。
func (fs *funcState) goIfFalse(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	var pc int
	switch e.k {
	case eNil, eFalse:
		pc = NoJump
	case eTrue:
		pc = fs.jump(line)
	case eJmp:
		pc = e.info
	default:
		pc = fs.jumpOnCond(line, e, 1)
	}
	fs.concat(&e.tJmp, pc)
	fs.patchToHere(e.fJmp)
	e.fJmp = NoJump
}

// invertJmp:翻转一个比较指令的期望布尔(用于 not / goIfTrue)。
func (fs *funcState) invertJmp(e *expDesc) {
	pc := e.info
	ctrl := pc - 1
	ins := fs.proto.Code[ctrl]
	op := bytecode.Op(ins)
	a := bytecode.A(ins)
	fs.proto.Code[ctrl] = bytecode.EncodeABC(op, 1-a, bytecode.B(ins), bytecode.C(ins))
}

// jumpOnCond 发 TEST/TESTSET + JMP,返回 JMP 的 pc;cond=0 假则跳,1 真则跳。
func (fs *funcState) jumpOnCond(line int32, e *expDesc, cond int) int {
	if e.k == eRelocable {
		ins := fs.proto.Code[e.info]
		if bytecode.Op(ins) == bytecode.NOT {
			// 撤销那条 NOT,改为 TEST 取反 cond(对齐 Lua 5.1 jumponcond 优化)
			fs.proto.Code = fs.proto.Code[:e.info]
			fs.proto.LineInfo = fs.proto.LineInfo[:e.info]
			fs.proto.IC = fs.proto.IC[:e.info]
			return fs.condJump(line, bytecode.TEST, bytecode.B(ins), 0, 1-cond)
		}
	}
	fs.dischargeToAnyReg(line, e)
	fs.freeExp(e)
	return fs.condJump(line, bytecode.TESTSET, bytecode.NoRegister, e.info, cond)
}

// condJump 发一条比较型指令 + JMP,返回 JMP pc。
func (fs *funcState) condJump(line int32, op bytecode.OpCode, a, b, c int) int {
	fs.emitABC(line, op, a, b, c)
	return fs.jump(line)
}
