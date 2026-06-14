//go:build wangshu_p3

package wasm

// 闭包构造 + 作用域 upvalue 关闭(PW7,02-translation §3.7)。
//
// CLOSURE/CLOSE 全经助手:CLOSURE 涉及 arena 分配 + 开放/关闭 upvalue 链管理
// (gibbous 自身从不分配,00-overview §8);CLOSE 关闭开放 upvalue 是 Go 侧
// 开放链操作。两者复用解释器 makeClosure/closeUpvals,byte-equal 天然成立。
//
// CLOSURE 后随 SubNUps[Bx] 条伪指令(MOVE/GETUPVAL 描述 upvalue 捕获)由发射层
// 跳过翻译(translate.go emitOpcode 返回 skip),助手内 makeClosure 读 ci.pc 消化。

import "github.com/Liam0205/wangshu/internal/bytecode"

// emitClosure CLOSURE A Bx —— R(A) := closure(Proto[Bx])(02 §3.7.1)。
//
//	(local.set $st (call h_closure(base,pc,a,bx)))   ;; 助手内 makeClosure + setReg + safepoint
//	(if (i32.eq $st 1) (then (return 1)))            ;; ERR 冒泡(理论上 CLOSURE 不报错,防御)
//
// 无需 base 刷新:makeClosure 不进嵌套 Lua 帧、不 growStack(分配触发 arena.grow64
// 保持段偏移不变,形态 Y 安全)。
func (c *Compiler) emitClosure(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(bx)
	em.call(helperClosure)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitClose CLOSE A —— 关闭所有 ≥ R(A) 的开放 upvalue(02 §3.7.2,纯状态操作)。
//
//	(call h_close(base,pc,a))   ;; 助手内 closeUpvals;返回 status 恒 0,丢弃
//
// CLOSE 无错误无返回值;助手返回 i32 status(签名复用 typeForPrep),发射侧 drop。
func (c *Compiler) emitClose(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.call(helperClose)
	em.drop() // status 恒 0,丢弃
}
