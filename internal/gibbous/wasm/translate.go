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
// 入参 $base 占 local 0;翻译用的临时 i64/i32 从 1 起。
const (
	localBase  = 0 // param $base i32
	localTmp64 = 1 // i64 临时(load/store 中转)
	localTmp32 = 2 // i32 临时(status 等)
	numLocals  = 3 // 函数体声明的 local 总数(不含 param)
)

// translateError 表示某 Proto 无法被 PW2 翻译(控制流过复杂 / 含未实装
// opcode 形态)——Compile 据此返回 unsupported,P2 fallback。
type translateError struct{ reason string }

func (e *translateError) Error() string { return e.reason }

// translate 把 Proto.Code 翻译成 Wasm 函数体字节(不含 local decl 与末尾
// end,由 module 组装包裹)。返回 (body, error)。
//
// PW2:只支持单 BB Proto。多 BB(有跳转)返 translateError。
func (c *Compiler) translate(proto *bytecode.Proto) ([]byte, error) {
	cfg := buildCFG(proto)
	if len(cfg.blocks) != 1 {
		return nil, &translateError{reason: fmt.Sprintf(
			"p3 PW2: control flow not yet structurable (%d basic blocks; PW3 relooper)", len(cfg.blocks))}
	}

	em := newEmitter()
	for pc := int32(0); pc < int32(len(proto.Code)); pc++ {
		if err := c.emitOpcode(em, proto, pc); err != nil {
			return nil, err
		}
	}
	// 单 BB 末尾若不是 RETURN(理论上 codegen 总发尾 RETURN),兜底 return 0。
	em.i32Const(0)
	em.ret()
	return em.bytes(), nil
}

// emitOpcode 翻译一条指令(PW2 七个直线 opcode)。
func (c *Compiler) emitOpcode(em *emitter, proto *bytecode.Proto, pc int32) error {
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
	case bytecode.JMP:
		// 单 BB 内不应有 JMP(JMP 会切 BB);走到这里说明 translate 的
		// 单 BB 假设被违反(防御性)。
		return &translateError{reason: "p3 PW2: JMP in single-BB path (unexpected)"}
	case bytecode.RETURN:
		c.emitReturn(em, ins, pc)
	default:
		return &translateError{reason: fmt.Sprintf("p3 PW2: opcode %s not implemented (pc=%d)", op, pc)}
	}
	return nil
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

// emitReturn RETURN A B —— 返回 R(A..A+B-2)(02 §3.6.3,经助手 + Wasm return)。
//
//	(local.set $st (call $h_return (base, pc, A, B)))
//	(return (local.get $st))
func (c *Compiler) emitReturn(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.call(helperReturn)
	em.ret() // return status(h_return 的返回值)
}
