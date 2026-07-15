//go:build wangshu_p3

package wasm

// 翻译主流程 + 7 直线 opcode emit(02-translation §3.1 + §6.2)。
//
// **PW2 控制流范围**:relooper 分析层(cfg.go/relooper.go)已建好并验证,
// 但结构化生成层(任意 reducible CFG → 嵌套 block/loop + br depth)留 PW3
// 完整落地(那时有条件跳 + 循环的端到端反馈验证)。PW2 翻译器只处理
// **单 basic block 的 Proto**(无跳转 / 纯直线 + 尾 RETURN)——这覆盖
// PW2 完成定义「5-op Proto 升层 byte-equal」的最小可验收形态。
//
// 含 JMP 但 CFG 多于一个 BB 的 Proto:isStructurable 返 false → translate
// 返 unsupported → Compile 返 error → P2 fallback 该 Proto(保守,正确)。
// PW3 用完整 relooper 解锁多 BB。

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Wasm 函数 local 槽位分配(gibbous 函数体内用的临时 local)。
// 入参 $base 占 local 0;翻译用的临时从 1 起。声明顺序(module.go
// codeSectionEntry)必须与此一致:2×i64 + 1×i32 + 1×f64。
const (
	localBase = 0 // param $base i32
	localI64a = 1 // i64 临时 a(load/store 中转 / 算术操作数 vb)
	localI64b = 2 // i64 临时 b(算术操作数 vc)
	localI32  = 3 // i32 临时(helper status 等)
	localF64  = 4 // f64 临时(算术结果)
	localI32b = 5 // i32 临时 b(PW5 表字节地址)
	localI64c = 6 // i64 临时 c(PW5 键 / 槽值中转)

	// localSavedTop 是 caller 自恢复 top 的快照(PW10 零跨界 ③a)。函数 prologue
	// 读 top 镜像字一次存入(此刻 = 本帧 base+MaxStack 槽索引,enterLuaFrame 刚设);
	// 每个定额(C≠0)CALL 经 call_indirect 直调返回后写回 top 字——被调 emitReturn
	// 快路径(③b)不再恢复 caller top,由 caller 自恢复。存槽索引(grow 安全:被调
	// 嵌套 growStack 改 stackBaseW 但槽索引不变,免 stackBaseW 换算)。
	localSavedTop = 7

	// 兼容旧名(PW2 直线 opcode 用 localTmp64/localTmp32)。
	localTmp64 = localI64a
	localTmp32 = localI32
)

// translateError 表示某 Proto 无法被 PW2 翻译(控制流过复杂 / 含未实装
// opcode 形态)——Compile 据此返回 unsupported,P2 fallback。
type translateError struct{ reason string }

func (e *translateError) Error() string { return e.reason }

// translate 把 Proto.Code 翻译成 Wasm 函数体字节(不含 local decl 与末尾
// end,由 module 组装包裹)。返回 (body, error)。
//
// 单可达 BB 走 PW2/PW3 直线路径;多 BB 走 PW4 relooper 结构化生成。
func (c *Compiler) translate(proto *bytecode.Proto) ([]byte, error) {
	cfg := buildCFG(proto)
	reach := cfg.reachableBlocks()
	em := newEmitter()
	c.emitPrologue(em)

	if len(reach) == 1 {
		// 单可达 BB:直线翻译(死代码块——RETURN 后兜底 RETURN——不发射)。
		entry := cfg.blocks[cfg.entry]
		for pc := entry.startPC; pc < entry.endPC; {
			skip, err := c.emitOpcode(em, proto, pc)
			if err != nil {
				return nil, err
			}
			pc += 1 + int32(skip) // CLOSURE 跳过后随伪指令
		}
		em.i32Const(0)
		em.ret()
		return em.bytes(), nil
	}

	// 多 BB:PW4 relooper 结构化生成。
	plan, err := buildStructPlan(cfg)
	if err != nil {
		return nil, &translateError{reason: err.Error()}
	}
	if err := c.emitStructured(em, proto, cfg, plan); err != nil {
		return nil, &translateError{reason: err.Error()}
	}
	// 兜底 return 0(理论上每条出口 BB 已发 RETURN;防御 wasm 校验「函数末尾
	// 缺值」——结构化发射后控制流可能落到函数体末)。
	em.i32Const(0)
	em.ret()
	return em.bytes(), nil
}

// emitPrologue 发射函数入口序言(PW10 零跨界 ③a):快照 top 镜像字进 localSavedTop。
//
//	(local.set $savedTop (i32.load offset=topAddr (i32.const 0)))
//
// 此刻(run 入口,enterLuaFrame 刚 setTop(base+MaxStack))top 字 = 本帧 base+MaxStack
// 槽索引,正是本帧每个定额 CALL 返回后须恢复的 th.top。caller 据此自恢复,使被调
// emitReturn 快路径(③b)无须跨函数取 caller.MaxStack / 换算 stackBaseW。
//
// **③a 零行为变更**:本阶段被调仍走 helperReturn(DoReturn 已恢复同值 base+MaxStack),
// caller 写回 = 写同值,纯幂等;③b 落地后被调不再恢复,caller 写回成为唯一恢复点。
func (c *Compiler) emitPrologue(em *emitter) {
	em.i32Const(0)
	em.i32Load(c.host.TopAddr())
	em.localSet(localSavedTop)
}

// 层据后继与作用域栈处理)。终结指令(JMP / 比较 / FOR* / RETURN)不在
// emitOpcode 里发控制流——只有本层知道后继 BB 的 br depth。
func (c *Compiler) emitBlockBody(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, bb int, stack *[]scope) error {
	blk := cfg.blocks[bb]
	if blk.startPC >= blk.endPC {
		return nil
	}
	lastPC := blk.endPC - 1
	term := proto.Code[lastPC]
	termOp := bytecode.Op(term)

	// 直线前缀(终结指令之前的所有指令)。
	for pc := blk.startPC; pc < lastPC; {
		skip, err := c.emitOpcode(em, proto, pc)
		if err != nil {
			return err
		}
		pc += 1 + int32(skip) // CLOSURE 跳过后随伪指令
	}

	switch termOp {
	case bytecode.RETURN:
		// 自带 return,无后继边。
		_, err := c.emitOpcode(em, proto, lastPC)
		return err

	case bytecode.TAILCALL:
		// 尾调用复用帧(PW6-b):自闭 return(Lua 完成/host 落 RETURN/ERR 冒泡)。
		c.emitTailCall(em, proto.Code[lastPC], lastPC)
		return nil

	case bytecode.JMP:
		// 无条件跳:发射边到唯一后继。
		return c.emitJmpTerm(em, cfg, plan, stack, bb)

	case bytecode.EQ, bytecode.LT, bytecode.LE, bytecode.TEST, bytecode.TESTSET:
		return c.emitCompareTerm(em, proto, cfg, plan, stack, bb, term, lastPC)

	case bytecode.FORPREP:
		return c.emitForPrepTerm(em, cfg, plan, stack, bb, lastPC)

	case bytecode.FORLOOP:
		return c.emitForLoopTerm(em, proto, cfg, plan, stack, bb, term, lastPC)

	case bytecode.TFORLOOP:
		return c.emitTForLoopTerm(em, cfg, plan, stack, bb, lastPC)

	default:
		// 普通 op 因「下一条是 leader」切 BB(单后继 fallthrough)。先发该 op,
		// 再发 fallthrough 边。(普通 op skip=0;CLOSURE 不会落此分支——它后随
		// 伪指令,其 BB 边界在伪指令之后,CLOSURE 不是 BB 末指令。)
		if _, err := c.emitOpcode(em, proto, lastPC); err != nil {
			return err
		}
		if len(blk.succs) == 1 {
			return c.emitEdge(em, cfg, plan, *stack, bb, blk.succs[0])
		}
		if len(blk.succs) == 0 {
			return nil
		}
		return &translateError{reason: fmt.Sprintf("p4: unexpected %d succs after %s", len(blk.succs), termOp)}
	}
}

// emitJmpTerm JMP 终结:唯一后继(jumpTarget)。
func (c *Compiler) emitJmpTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int) error {
	blk := cfg.blocks[bb]
	if len(blk.succs) != 1 {
		return &translateError{reason: fmt.Sprintf("p4: JMP BB %d has %d succs", bb, len(blk.succs))}
	}
	return c.emitEdge(em, cfg, plan, *stack, bb, blk.succs[0])
}

// emitOpcode 翻译一条非终结直线指令。返回 (skip, err):skip = 本指令额外消耗的
// 后随指令数(CLOSURE 后随 SubNUps 条伪指令 = 数据非 opcode,须跳过不翻译),
// 其余 opcode skip=0。调用方据 skip 步进 pc(pc += 1 + skip)。
func (c *Compiler) emitOpcode(em *emitter, proto *bytecode.Proto, pc int32) (int, error) {
	ins := proto.Code[pc]
	op := bytecode.Op(ins)
	switch op {
	case bytecode.MOVE:
		c.emitMove(em, ins)
	case bytecode.LOADK:
		c.emitLoadK(em, proto, ins)
	case bytecode.LOADBOOL:
		c.emitLoadBool(em, proto, ins, pc)
	case bytecode.LOADNIL:
		c.emitLoadNil(em, ins)
	case bytecode.GETUPVAL:
		c.emitGetUpval(em, ins, pc)
	case bytecode.SETUPVAL:
		c.emitSetUpval(em, ins, pc)
	case bytecode.RETURN:
		c.emitReturn(em, ins, pc)
	case bytecode.TAILCALL:
		// 单 BB 路径(TAILCALL 后仅死代码 RETURN,reachableBlocks==1)的尾调用。
		// 多 BB 路径由 emitBlockBody 终结分派(两路均调 emitTailCall,自闭 return)。
		c.emitTailCall(em, ins, pc)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
		c.emitArith(em, proto, ins, pc)
	case bytecode.UNM:
		c.emitUnm(em, ins, pc)
	case bytecode.NOT:
		c.emitNot(em, ins)
	case bytecode.LEN:
		c.emitLen(em, ins, pc)
	case bytecode.CONCAT:
		c.emitConcat(em, ins, pc)
	case bytecode.GETGLOBAL:
		c.emitGetGlobal(em, proto, ins, pc)
	case bytecode.SETGLOBAL:
		c.emitSetGlobal(em, proto, ins, pc)
	case bytecode.GETTABLE:
		c.emitGetTable(em, proto, ins, pc)
	case bytecode.SETTABLE:
		c.emitSetTable(em, proto, ins, pc)
	case bytecode.SELF:
		c.emitSelf(em, proto, ins, pc)
	case bytecode.NEWTABLE:
		c.emitNewTable(em, ins, pc)
	case bytecode.SETLIST:
		c.emitSetList(em, ins, pc)
	case bytecode.CALL:
		c.emitCall(em, proto, ins, pc)
	case bytecode.CLOSE:
		c.emitClose(em, ins, pc)
	case bytecode.CLOSURE:
		// 后随 SubNUps[Bx] 条伪指令(MOVE/GETUPVAL,描述 upvalue 捕获)是数据非
		// opcode,翻译跳过(makeClosure 在助手内读它们);返回 skip 让调用方步 pc。
		c.emitClosure(em, ins, pc)
		bx := bytecode.Bx(ins)
		if bx < len(proto.SubNUps) {
			return int(proto.SubNUps[bx]), nil
		}
		return 0, nil
	default:
		return 0, &translateError{reason: fmt.Sprintf("p3 PW3: opcode %s not implemented (pc=%d)", op, pc)}
	}
	return 0, nil
}

// loadRK 把 RK 操作数(寄存器 R(rk) 或常量 K(rk-256))压到 Wasm 栈顶(i64)。
//   - 寄存器(rk<MaxK):i64.load offset=8*rk (base)
//   - 常量(rk≥MaxK):i64.const 常量 raw u64(字符串常量已被 SupportsAllOpcodes 拒)
func (c *Compiler) loadRK(em *emitter, proto *bytecode.Proto, rk int) {
	if rk < bytecode.MaxK {
		em.localGet(localBase)
		em.i64Load(8 * uint32(rk))
		return
	}
	em.i64Const(uint64(proto.Consts[rk-bytecode.MaxK]))
}

// emitArith ADD/SUB/MUL/DIV/MOD/POW —— 双 number 快路径(Wasm 内直发 f64 +
// NaN 规范化)+ 慢路径助手(02 §3.2.1)。
//
//	vb := RK(B); vc := RK(C)
//	if IsNumber(vb) && IsNumber(vc):
//	  r := f64(vb) op f64(vc); canonicalizeNaN(r); R(A) := r
//	else:
//	  status := h_arith(base,pc,op,b,c,a); if status==1 return 1
func (c *Compiler) emitArith(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := bytecode.B(ins)
	cc := bytecode.C(ins)
	op := bytecode.Op(ins)

	// POW 无 f64.pow 指令:整条走慢路径助手(Go math.Pow,byte-equal),
	// 不发快路径(02 §3.2.2:POW 基线走助手最简)。
	if op == bytecode.POW {
		c.emitArithSlow(em, op, b, cc, a, pc)
		return
	}

	// vb, vc → local
	c.loadRK(em, proto, b)
	em.localSet(localI64a)
	c.loadRK(em, proto, cc)
	em.localSet(localI64b)

	// IsNumber(vb) && IsNumber(vc):vb < qNanBoxBase && vc < qNanBoxBase
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// --- 快路径:f64 算术 ---
	c.emitArithFast(em, op, a)
	em.elseOp()
	// --- 慢路径:h_arith ---
	c.emitArithSlow(em, op, b, cc, a, pc)
	em.end()
}

// emitArithSlow 发射算术慢路径助手调用:h_arith(base,pc,op,b,c,a)→status;
// status==1 则 return 1(错误冒泡,04 §4.1)。
func (c *Compiler) emitArithSlow(em *emitter, op bytecode.OpCode, b, cc int, a uint32, pc int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(op))
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.i32Const(int32(a))
	em.call(helperArith)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitArithFast 发射双 number 快路径:f64(vb) op f64(vc) → 规范化 → store R(A)。
// 操作数在 localI64a/localI64b(POW 不走此路,无 f64.pow)。
func (c *Compiler) emitArithFast(em *emitter, op bytecode.OpCode, a uint32) {
	switch op {
	case bytecode.MOD:
		// Lua MOD:a - floor(a/b)*b。
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		em.f64Div()
		em.f64Floor()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		em.f64Mul()
		em.f64Sub() // (vb) - (floor(vb/vc)*vc)
	default:
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		switch op {
		case bytecode.ADD:
			em.f64Add()
		case bytecode.SUB:
			em.f64Sub()
		case bytecode.MUL:
			em.f64Mul()
		case bytecode.DIV:
			em.f64Div()
		}
	}
	// canonicalizeNaN:if r != r then r = canonNaN
	em.localTee(localF64)
	em.localGet(localF64)
	em.f64Ne()
	em.ifVoid()
	em.i64Const(canonNaNU64)
	em.f64ReinterpretI64()
	em.localSet(localF64)
	em.end()
	// store R(A) = i64.reinterpret(r)
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * a)
}

// emitUnm UNM A B —— R(A) := -R(B)(02 §3.2.3)。
// Fast path f64.neg + result guard; otherwise h_unm.
//
// Result guard (issue #107): f64.neg never produces a NEW NaN, but it
// flips canonNaN (0x7FF8...) into 0xFFF8_0000_0000_0000 — exactly
// value.Nil's bit pattern. `-(0%0)` on the unguarded fast path stored
// that Nil, and the next arithmetic op raised "attempt to perform
// arithmetic on a nil value". Mirror of the P4 emitUNM fix from the
// issue #37 port round (fuzz seed f7f0bb1a NaN-aliasing family): when
// the flipped bits land back in the tag space (>= qNanBoxBase), route
// to h_unm, whose host side canonicalizes via NumberValue. Fast-path
// condition = IsNumber(vb) && negged < qNanBoxBase.
func (c *Compiler) emitUnm(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	// negged = i64.reinterpret(f64.neg(f64.reinterpret(vb))). Garbage
	// bits for boxed non-numbers are harmless — the combined condition
	// below routes those to the slow path anyway.
	em.localGet(localI64a)
	em.f64ReinterpretI64()
	em.f64Neg()
	em.i64ReinterpretF64()
	em.localSet(localI64b)
	// IsNumber(vb) && negged < qNanBoxBase
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// Fast path: store negged directly.
	em.localGet(localBase)
	em.localGet(localI64b)
	em.i64Store(8 * a)
	em.elseOp()
	// Slow path: h_unm(base,pc,b,a)
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(a))
	em.call(helperUnm)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	em.end()
}

// emitNot NOT A B —— R(A) := not R(B)(02 §3.2.4,无元方法)。
// Truthy(v) = v != Nil && v != False;not Truthy → BoolValue。
func (c *Compiler) emitNot(em *emitter, ins bytecode.Instruction) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	// vt = (vb != Nil) && (vb != False)
	em.localGet(localI64a)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.localGet(localI64a)
	em.i64Const(falseRawU64())
	em.i64Ne()
	em.i32And()
	// if !vt then R(A)=True else R(A)=False
	em.i32Eqz()
	em.ifVoid()
	em.localGet(localBase)
	em.i64Const(trueRawU64())
	em.i64Store(8 * a)
	em.elseOp()
	em.localGet(localBase)
	em.i64Const(falseRawU64())
	em.i64Store(8 * a)
	em.end()
}

// emitLen LEN A B —— R(A) := #R(B)(02 §3.2.5)。全经 h_len(string 长度 /
// table border / 异类报错——内联过复杂,助手复用 execute.go LEN 段)。
func (c *Compiler) emitLen(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(a))
	em.call(helperLen)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitConcat CONCAT A B C —— R(A) := R(B)..…..R(C)(02 §3.2.6)。
// 全经 h_concat(复用 execute.go doConcat 全逻辑 + safepoint)。
func (c *Compiler) emitConcat(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	cc := uint32(bytecode.C(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(a))
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.call(helperConcat)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitMove MOVE A B —— R(A) := R(B)(02 §3.1.1)。
//
//	(i64.store offset=8*A (local.get $base)
//	  (i64.load offset=8*B (local.get $base)))
func (c *Compiler) emitMove(em *emitter, ins bytecode.Instruction) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase) // store addr base
	em.localGet(localBase) // load addr base
	em.i64Load(8 * b)      // load R(B)
	em.i64Store(8 * a)     // store R(A)
}

// emitLoadK LOADK A Bx —— R(A) := K(Bx)(02 §3.1.2)。
// 常量值在编译期已知,烧成 i64.const 立即数。
//
// **PW2 限制**:字符串常量是 State 私有惰性 intern(Proto.Consts 中是 Nil
// 占位,真值在装载期才填),编译期拿不到 GCRef——含字符串常量的 LOADK
// 暂不支持(返回 unsupported,P2 fallback)。数字/bool/nil 常量可烧。
func (c *Compiler) emitLoadK(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction) {
	bx := bytecode.Bx(ins)
	a := uint32(bytecode.A(ins))
	// 字符串常量:编译期是 Nil 占位(IsStringConst),真值 State 私有,不能烧。
	// 这种情况应已被 isCompilableConsts 在 SupportsAllOpcodes 拦下;此处防御。
	raw := uint64(proto.Consts[bx])
	em.localGet(localBase)
	em.i64Const(raw)
	em.i64Store(8 * a)
}

// emitLoadBool LOADBOOL A B C —— R(A) := bool(B); if C≠0 then pc++(02 §3.1.3)。
//
// PW2 单 BB 路径:C≠0 的「pc++」是控制流(切 BB),不会进单 BB 路径。
// C=0 时纯赋值。
func (c *Compiler) emitLoadBool(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := bytecode.B(ins)
	var v uint64
	if b != 0 {
		v = trueRawU64()
	} else {
		v = falseRawU64()
	}
	em.localGet(localBase)
	em.i64Const(v)
	em.i64Store(8 * a)
	// C≠0 的 pc++ 由 CFG 切 BB,单 BB 路径不处理(若出现说明 translate
	// 单 BB 假设被违反,但 LOADBOOL C≠0 会让 buildCFG 切 BB,不会到这)。
}

// emitLoadNil LOADNIL A B —— R(A..B) := nil(闭区间,02 §3.1.4)。
func (c *Compiler) emitLoadNil(em *emitter, ins bytecode.Instruction) {
	a := bytecode.A(ins)
	b := bytecode.B(ins)
	nilRaw := nilRawU64()
	for r := a; r <= b; r++ {
		em.localGet(localBase)
		em.i64Const(nilRaw)
		em.i64Store(8 * uint32(r))
	}
}

// emitGetUpval GETUPVAL A B —— R(A) := Upval(B)(02 §3.1.5,经助手)。
func (c *Compiler) emitGetUpval(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	// $vb = h_getupval(base, b)
	em.localGet(localBase)
	em.i32Const(b)
	em.call(helperGetUpval)
	// store R(A)
	em.localSet(localTmp64)
	em.localGet(localBase)
	em.localGet(localTmp64)
	em.i64Store(8 * a)
}

// emitSetUpval SETUPVAL A B —— Upval(B) := R(A)(02 §3.1.6,经助手)。
func (c *Compiler) emitSetUpval(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	// h_setupval(base, b, R(A))
	em.localGet(localBase)
	em.i32Const(b)
	em.localGet(localBase)
	em.i64Load(8 * a)
	em.call(helperSetUpval)
}

// emitReturn RETURN A B(02 §3.6.3)。PW10 零跨界 ③b:定额返回(B≠0 且 nret≤8)
// 发守卫快路径(Wasm 内拆帧免 h_return 跨界),任一守卫失败 br 回退 helperReturn;
// 变长返回(B==0 到 top)/ 超长 nret 只发 helperReturn。
//
// **逐字节镜像 DoReturn**(gibbous_host.go):nret=B-1 个结果 R(A..)→funcIdx 起,
// 减 ciDepth,写 caller base 中转字。**不碰 top**(caller 经 ③a savedTop 自恢复)、
// 不物化 pc(RETURN 不抛错,弹后段帧不再读)、无 safepoint(快路径无分配/GC 窗口)。
//
// **守卫**(任一失败 br $slow → helperReturn,逐字节回退):
//   - G5 ciDepth<2:无 gibbous caller 可直拆(最外帧 RETURN 回 crescent)→ Go 拆。
//     须最先判,否则后续读 caller 段帧(depth-2)越界。
//   - G3 openGuard≠0:本帧有开放 upvalue,closeUpvals 非 no-op → Go 关闭。
//   - G2 caller 段 word2 bit50==0:caller 非 gibbous(top/中转字须 Go 处理)→ Go。
//   - G4 callee 段 nresults≠nret:多退少补 / want-all(nresults=-1)→ Go 调整。
func (c *Compiler) emitReturn(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	nret := b - 1
	if b != 0 && nret >= 0 && nret <= maxReturnFast {
		c.emitReturnFast(em, a, nret)
	}
	// 回退(慢路径 / 守卫 miss 落点):h_return(base,pc,a,b)。
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.call(helperReturn)
	em.ret() // return status(h_return 的返回值)
}

// emitCIFrameAddr 发射「第 (depth + idxDelta) 帧的段字节地址」到 Wasm 栈(PW10 零跨界
// ③b)。地址 = load(ciSegBaseAddr) + (load(ciDepthAddr)+idxDelta)*ciFrameBytes。idxDelta
// 为 -1(callee 顶帧)或 -2(caller)。段基址/深度每次现读(段可重定位,免缓存悬垂)。
func (c *Compiler) emitCIFrameAddr(em *emitter, idxDelta int32) {
	em.i32Const(0)
	em.i32Load(c.host.CISegBaseAddr()) // segBase 字节基址
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr()) // depth
	em.i32Const(idxDelta)
	em.i32Add() // depth + idxDelta
	em.i32Const(ciFrameBytes)
	em.i32Mul() // *32
	em.i32Add() // segBase + (depth+idxDelta)*32
}

// emitReturnFast 发射 ③b 守卫快路径(见 emitReturn 文档)。在 block $slow 内:任一
// 守卫 br_if 0 落到 block 末(= emitReturn 的 helperReturn 回退);全过则快路径体
// return 0 退出函数。
func (c *Compiler) emitReturnFast(em *emitter, a, nret int32) {
	em.block() // $slow:守卫失败跳到此 block 末(深度 0)

	// G5:ciDepth < 2 → slow(无 gibbous caller 帧;须先判防 caller 段帧越界)。
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr())
	em.i32Const(2)
	em.i32LtS()
	em.brIf(0)

	// G3:openGuard ≠ 0 → slow(有开放 upvalue,closeUpvals 非 no-op)。
	em.i32Const(0)
	em.i32Load(c.host.OpenGuardAddr())
	em.brIf(0)

	// G2:caller 段 word2 bit50(gibbous)清 → slow。
	c.emitCIFrameAddr(em, -2) // caller 帧地址
	em.i64Load(ciWord2Off)
	em.i64Const(ciGibbousBit)
	em.i64And()
	em.i64Eqz() // (callerW2 & bit50)==0 → 1
	em.brIf(0)

	// G4:callee 段 nresults([47:32]) ≠ nret → slow(含 want-all nresults=-1)。
	c.emitCIFrameAddr(em, -1) // callee 帧地址
	em.i64Load(ciWord2Off)
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(0xffff)
	em.i32And() // nresults 16 位字段
	em.i32Const(nret)
	em.i32Ne()
	em.brIf(0)

	// --- 快路径体(逐字节镜像 DoReturn:moveResults → 中转字 → ciDepth--)---
	// moveResults:dstBase = funcIdx 字节址 = localBase - 8(base = funcIdx+1)。
	em.localGet(localBase)
	em.i32Const(8)
	em.i32Sub()
	em.localSet(localI32b) // dstBase
	// nret 个结果:mem[dstBase + 8k] = mem[localBase + 8*(a+k)](前向拷贝,源在目标之上,
	// 无写后读破坏——同 DoReturn for k:=0..nret 升序)。
	for k := int32(0); k < nret; k++ {
		em.localGet(localI32b)
		em.localGet(localBase)
		em.i64Load(uint32(8 * (a + k)))
		em.i64Store(uint32(8 * k))
	}

	// 中转字 = (stackBaseW+caller.base)*8 = localBase + (callerBase - calleeBase)*8
	// (两 base 槽自各自段 word0 低 32 读差值,免 stackBaseW;承 R3 base 刷新契约)。
	// 须在 ciDepth-- 之前(emitCIFrameAddr 现读 depth)。
	em.i32Const(0) // i32.store 地址操作数(基 0 + offset=ciTransferAddr)
	em.localGet(localBase)
	c.emitCIFrameAddr(em, -2) // caller 帧
	em.i32Load(0)             // callerBase(word0 低 32)
	c.emitCIFrameAddr(em, -1) // callee 帧
	em.i32Load(0)             // calleeBase
	em.i32Sub()               // callerBase - calleeBase
	em.i32Const(8)
	em.i32Mul() // *8
	em.i32Add() // localBase + (callerBase-calleeBase)*8
	em.i32Store(c.host.CITransferAddr())

	// ciDepth--(popCallInfo;快路径无 GC 窗口,顺序对 GC 不敏感,置最后)。
	em.i32Const(0) // store 地址操作数
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr())
	em.i32Const(1)
	em.i32Sub()
	em.i32Store(c.host.CIDepthAddr())

	// 快路径完成:return 0(OK status)。
	em.i32Const(0)
	em.ret()

	em.end() // $slow 末:落到此 = 守卫 miss,继续 emitReturn 的 helperReturn 回退
}
