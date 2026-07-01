//go:build wangshu_p3

package wasm

// PW4 控制流终结边发射:比较(EQ/LT/LE/TEST/TESTSET)、FORPREP、FORLOOP。
// 这些是 BB 末终结指令,后继边由结构化生成层据作用域栈算 br depth(02 §3.3/§3.5)。
//
// 「比较 + JMP 合并」(02 §3.3.1):比较指令后必跟 JMP。CFG 把比较 BB 切成两
// 后继:succExec(pc+1 = 紧邻 JMP 的 BB,落它则执行 JMP 跳到 JMP target)、
// succSkip(pc+2 = 跳过 JMP 的 BB)。解释器语义 `if res != bool(A) then pc++`:
// res != boolA → 跳过 JMP(succSkip);res == boolA → 执行 JMP(succExec)。

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// compareSuccs 解析比较 BB 的两后继:返回 (succExec, succSkip)。
//   - succExec:落到 lastPC+1(执行紧邻 JMP)
//   - succSkip:落到 lastPC+2(pc++ 跳过 JMP)
func (c *Compiler) compareSuccs(cfg *cfg, lastPC int32) (succExec, succSkip int, err error) {
	idExec, ok1 := cfg.pcToBB[lastPC+1]
	idSkip, ok2 := cfg.pcToBB[lastPC+2]
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("p4: compare at pc=%d missing succ BB (exec=%v skip=%v)", lastPC, ok1, ok2)
	}
	return idExec, idSkip, nil
}

// emitCompareTerm 发射比较终结(EQ/LT/LE/TEST/TESTSET)+ 条件边(02 §3.3)。
func (c *Compiler) emitCompareTerm(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, stack *[]scope, bb int, ins bytecode.Instruction, lastPC int32) error {
	op := bytecode.Op(ins)
	a := bytecode.A(ins)
	succExec, succSkip, err := c.compareSuccs(cfg, lastPC)
	if err != nil {
		return err
	}

	if op == bytecode.TESTSET {
		return c.emitTestSetTerm(em, ins, plan, stack, bb, succExec, succSkip)
	}

	// 算比较结果 → localI32(0/1)。
	// boolField is the byte that the result is compared against:
	//   - EQ/LT/LE: A (Lua reference: `if (RK(B) <op> RK(C)) ~= A then pc++`)
	//   - TEST:     C (Lua reference: `if not (R(A) <=> C) then pc++`)
	boolField := a
	switch op {
	case bytecode.TEST:
		c.emitTruthy(em, bytecode.A(ins)) // Truthy(R(A)) → localI32
		boolField = bytecode.C(ins)
	case bytecode.LT, bytecode.LE, bytecode.EQ:
		if e := c.emitNumCompareOrHelper(em, proto, ins, lastPC); e != nil {
			return e
		}
	default:
		return fmt.Errorf("p4: unexpected compare op %s", op)
	}

	// if (vt != bool(boolField)) then 跳过 JMP(succSkip) else 执行 JMP(succExec)
	em.localGet(localI32)
	em.i32Const(boolToI32(boolField))
	em.i32Ne()
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	if e := c.emitEdge(em, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.elseOp()
	if e := c.emitEdge(em, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitTestSetTerm TESTSET A B C(02 §3.3.5):Truthy(R(B))==bool(C) → R(A):=R(B)
// 落 succExec(执行 JMP);否则跳过 JMP(succSkip)。
func (c *Compiler) emitTestSetTerm(em *emitter, ins bytecode.Instruction, plan *structPlan, stack *[]scope, bb, succExec, succSkip int) error {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	cc := bytecode.C(ins)
	// vb := R(B); vt := Truthy(vb)
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	c.emitTruthyOf(em, localI64a) // → localI32
	// if (vt == bool(C)) then R(A):=R(B); 执行 JMP else 跳过 JMP
	em.localGet(localI32)
	em.i32Const(boolToI32(cc))
	em.raw(0x46) // i32.eq
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	em.localGet(localBase)
	em.localGet(localI64a)
	em.i64Store(8 * a)
	if e := c.emitEdge(em, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.elseOp()
	if e := c.emitEdge(em, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitTruthy 算 Truthy(R(a)) → localI32(读寄存器版)。
func (c *Compiler) emitTruthy(em *emitter, a int) {
	em.localGet(localBase)
	em.i64Load(8 * uint32(a))
	em.localSet(localI64a)
	c.emitTruthyOf(em, localI64a)
}

// emitTruthyOf 算 Truthy(local) → localI32:v != Nil && v != False。
func (c *Compiler) emitTruthyOf(em *emitter, vlocal uint32) {
	em.localGet(vlocal)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.localGet(vlocal)
	em.i64Const(falseRawU64())
	em.i64Ne()
	em.i32And()
	em.localSet(localI32)
}

// emitNumCompareOrHelper LT/LE/EQ:双 number 快路径(f64 比较)+ 慢路径助手。
// 结果(0/1)留 localI32;慢路径错误时 return 1(状态冒泡)。
func (c *Compiler) emitNumCompareOrHelper(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) error {
	op := bytecode.Op(ins)
	b := bytecode.B(ins)
	cc := bytecode.C(ins)
	c.loadRK(em, proto, b)
	em.localSet(localI64a)
	c.loadRK(em, proto, cc)
	em.localSet(localI64b)
	// IsNumber(vb) && IsNumber(vc)
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// 快路径:f64 比较 → localI32
	em.localGet(localI64a)
	em.f64ReinterpretI64()
	em.localGet(localI64b)
	em.f64ReinterpretI64()
	switch op {
	case bytecode.LT:
		em.f64Lt()
	case bytecode.LE:
		em.f64Le()
	case bytecode.EQ:
		em.f64Eq()
	}
	em.localSet(localI32)
	em.elseOp()
	// 慢路径:EQ 先 raw bit 相等再 h_eq;LT/LE 直接 h_compare。
	if op == bytecode.EQ {
		c.emitEqSlow(em, b, cc, pc)
	} else {
		em.localGet(localBase)
		em.i32Const(pc)
		em.i32Const(int32(op))
		em.i32Const(int32(b))
		em.i32Const(int32(cc))
		em.call(helperCompare)
		c.emitUnpackCompare(em)
	}
	em.end()
	return nil
}

// emitEqSlow EQ 非双 number 慢路径:raw bit 相等(同 GCRef/bool/nil)直接 true,
// 否则走 h_eq(__eq 元方法,仅两 table)。结果 → localI32。
func (c *Compiler) emitEqSlow(em *emitter, b, cc int, pc int32) {
	// if vb == vc (raw) then localI32 := 1 else h_eq
	em.localGet(localI64a)
	em.localGet(localI64b)
	em.i64Eq()
	em.ifVoid()
	em.i32Const(1)
	em.localSet(localI32)
	em.elseOp()
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.call(helperEq)
	c.emitUnpackCompare(em)
	em.end()
}

// emitUnpackCompare 解 h_compare/h_eq 的 packed 返回(栈顶 i32):
// bit1=错误 → return 1;bit0 → localI32。
func (c *Compiler) emitUnpackCompare(em *emitter) {
	em.localTee(localI32)
	em.i32Const(2)
	em.i32And()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	em.localGet(localI32)
	em.i32Const(1)
	em.i32And()
	em.localSet(localI32)
}

// emitForPrepTerm FORPREP:经 h_forprep 校验三槽 + 预减,然后跳到 FORLOOP
// (唯一后继)。
func (c *Compiler) emitForPrepTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int, lastPC int32) error {
	a := bytecode.A(cfg.proto.Code[lastPC])
	em.localGet(localBase)
	em.i32Const(lastPC)
	em.i32Const(int32(a))
	em.call(helperForPrep)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// 跳到 FORLOOP(唯一后继)。
	blk := cfg.blocks[bb]
	if len(blk.succs) != 1 {
		return fmt.Errorf("p4: FORPREP BB %d has %d succs", bb, len(blk.succs))
	}
	return c.emitEdge(em, plan, *stack, bb, blk.succs[0])
}

// emitForLoopTerm FORLOOP(02 §3.5.2):三槽已被 FORPREP 规范为 number,快路径
// 全 f64。idx+=step;方向判界;continue 写回 idx/v + 回边 safepoint + br 回 loop;
// 否则落出 loop(退出)。
func (c *Compiler) emitForLoopTerm(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, stack *[]scope, bb int, ins bytecode.Instruction, lastPC int32) error {
	a := uint32(bytecode.A(ins))
	// 后继:回跳(jumpTarget)= 循环体;落出(lastPC+1)= 退出。
	idBody, ok1 := cfg.pcToBB[lastPC+1+int32(bytecode.SBx(ins))]
	idOut, ok2 := cfg.pcToBB[lastPC+1]
	if !ok1 || !ok2 {
		return fmt.Errorf("p4: FORLOOP at pc=%d missing succ (body=%v out=%v)", lastPC, ok1, ok2)
	}

	// idx = R(A) + R(A+2) → localF64
	em.localGet(localBase)
	em.i64Load(8 * a)
	em.f64ReinterpretI64()
	em.localGet(localBase)
	em.i64Load(8 * (a + 2))
	em.f64ReinterpretI64()
	em.f64Add()
	em.localSet(localF64)
	// cont = (step>=0) ? idx<=limit : idx>=limit → localI32
	c.emitForContinueTest(em, a)
	em.localGet(localI32)
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	// continue:写回 R(A)=idx, R(A+3)=idx
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * a)
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * (a + 3))
	// 回边 safepoint(P3 PW9 优化):inline 检查 gcPending 标志,非 0 才跨层调
	// h_safepoint。热循环每迭代无 GC due 时此分支恒不跳,零跨层(05 §3 / 08 §5.1.2)。
	// baseline memory-resident 下三槽已写回栈槽,GC 根经 R5 覆盖。
	c.emitGCPendingSafepoint(em, lastPC)
	// br 回 loop header(回边)。
	if e := c.emitEdge(em, plan, *stack, bb, idBody); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	// 不 continue:落出 loop = 退出循环 → idOut。结构化下 FORLOOP 是 loop 末,
	// 落出即出 loop;若 idOut 非 fallthrough 需 br(emitEdge 处理)。
	return c.emitEdge(em, plan, *stack, bb, idOut)
}

// emitForContinueTest 算数值 for 续行条件 → localI32:
// (step>=0) ? idx<=limit : idx>=limit。idx 在 localF64,step/limit 现读栈槽。
func (c *Compiler) emitForContinueTest(em *emitter, a uint32) {
	// step = R(A+2)
	em.localGet(localBase)
	em.i64Load(8 * (a + 2))
	em.f64ReinterpretI64()
	em.f64Const(0)
	em.f64Ge()
	em.ifVoid()
	// step>=0:idx <= limit
	em.localGet(localF64)
	em.localGet(localBase)
	em.i64Load(8 * (a + 1))
	em.f64ReinterpretI64()
	em.f64Le()
	em.localSet(localI32)
	em.elseOp()
	// step<0:idx >= limit
	em.localGet(localF64)
	em.localGet(localBase)
	em.i64Load(8 * (a + 1))
	em.f64ReinterpretI64()
	em.f64Ge()
	em.localSet(localI32)
	em.end()
}

// boolToI32 把 bytecode 的 A/C 标志(!=0)转 i32 0/1。
func boolToI32(v int) int32 {
	if v != 0 {
		return 1
	}
	return 0
}

// emitTForLoopTerm TFORLOOP A C(PW4b,02 §3.5.3):调迭代器 R(A)(R(A+1),R(A+2)),
// 结果落 R(A+3..A+2+C);首值非 nil → 控制变量 R(A+2):=首值,落回边 JMP(继续);
// 首值 nil → 跳过回边(退出)。全经 h_tforloop(跨层调迭代器,复用 callLuaFromHost)。
//
// h_tforloop 返回 i64:≥0=刷新后 base(继续,迭代器调用可能 growStack 段重定位)/
// -1=ERR / -2=退出。结构:
//
//	(local.set $i64c (call h_tforloop(base,pc,a,c)))
//	(if (i64.eq $i64c -1) (then (return 1)))            ;; ERR 冒泡
//	(if (i64.eq $i64c -2)
//	  (then <emitEdge succSkip 退出>)
//	  (else (local.set $base (i32.wrap $i64c)) <emitEdge succExec 回边>))
//
// succExec = lastPC+1(回边 JMP BB),succSkip = lastPC+2(退出 BB);同比较终结。
func (c *Compiler) emitTForLoopTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int, lastPC int32) error {
	succExec, succSkip, err := c.compareSuccs(cfg, lastPC)
	if err != nil {
		return err
	}
	a := bytecode.A(cfg.proto.Code[lastPC])
	cc := bytecode.C(cfg.proto.Code[lastPC])

	// $i64c = h_tforloop(base, pc, a, c)
	em.localGet(localBase)
	em.i32Const(lastPC)
	em.i32Const(int32(a))
	em.i32Const(int32(cc))
	em.call(helperTForLoop)
	em.localSet(localI64c)
	// ERR(== -1)→ return 1
	em.localGet(localI64c)
	em.i64Const(^uint64(0)) // -1 的 i64 位模式
	em.i64Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// 退出(== -2)→ succSkip;否则刷新 base + 回边 succExec
	em.localGet(localI64c)
	em.i64Const(^uint64(0) - 1) // -2 的 i64 位模式
	em.i64Eq()
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	if e := c.emitEdge(em, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.elseOp()
	// 继续:刷新 base(迭代器调用可能 growStack 段重定位,见 PW6 / design-vs-physics §2)
	em.localGet(localI64c)
	em.i32WrapI64()
	em.localSet(localBase)
	if e := c.emitEdge(em, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitGCPendingSafepoint 发回边 safepoint 的 inline gcPending 检查(P3 PW9):
//
//	(if (i32.load offset=gcPendingAddr (i32.const 0))
//	  (then (call h_safepoint (base) (pc))))
//
// gcPending 标志字由 collector 在 GC 状态转移点镜像(due 时置 1)。热循环无 GC due
// 时此分支恒不跳,零跨层——只在 GC 真正 due 时才跨层调 h_safepoint(否则每迭代无
// 条件跨层 ~143ns 吞掉消灭 dispatch 的收益,05 §3 / 08 §5.1.2)。
//
// 正确性:flag 保守覆盖「stressMode 或 bytesAllocSince≥threshold」(collector
// updateGCPending),GC 该触发时 flag 必为 1;跳过仅发生在「无分配 due」时,此时
// h_safepoint 的 MaybeCollect 本就 no-op,跳过等价。stressMode 下 flag 恒 1,每迭代
// 仍跨层 → GC 压力测试(V5/V13)语义不变。
func (c *Compiler) emitGCPendingSafepoint(em *emitter, pc int32) {
	addr := c.host.GCPendingAddr()
	em.i32Const(0)   // 地址操作数(基址 0 + offset=addr 立即数)
	em.i32Load(addr) // i32.load offset=gcPendingAddr:读标志字低 4 字节
	em.ifVoid()
	em.localGet(localBase)
	em.i32Const(pc)
	em.call(helperSafepoint)
	em.end()
}
