//go:build wangshu_p3

package wasm

// wasm function body 字节编码器(02-translation §6.3 emitter)。
//
// 累积一个 Wasm 函数体的指令字节(不含 local decl 前缀与末尾 end,由
// module 组装阶段包裹)。提供 P3 翻译需要的指令子集 + LEB128 编码 +
// 基本块标签管理。
//
// Wasm 指令编码参考:https://webassembly.github.io/spec/core/binary/instructions.html

import "math"

// wasmOp 常用 Wasm 指令 opcode。
const (
	opBlock        byte = 0x02
	opLoop         byte = 0x03
	opIf           byte = 0x04
	opElse         byte = 0x05
	opEnd          byte = 0x0b
	opBr           byte = 0x0c
	opBrIf         byte = 0x0d
	opReturn       byte = 0x0f
	opCall         byte = 0x10
	opCallIndirect byte = 0x11
	opLocalGet     byte = 0x20
	opLocalSet     byte = 0x21
	opLocalTee     byte = 0x22
	opI64Load      byte = 0x29
	opI64Store     byte = 0x37
	opI32Const     byte = 0x41
	opI64Const     byte = 0x42

	// 比较 / 算术 / 类型转换(PW3)。
	opI32Eqz byte = 0x45
	opI32Eq  byte = 0x46
	opI32Ne  byte = 0x47
	opI32And byte = 0x71
	opI64And byte = 0x83
	opI64Eq  byte = 0x51
	opI64Ne  byte = 0x52
	opI64LtS byte = 0x53
	opI64LtU byte = 0x54

	opF64Eq        byte = 0x61
	opF64Lt        byte = 0x63
	opF64Le        byte = 0x65
	opF64Ge        byte = 0x66
	opF64Ne        byte = 0x62
	opF64Const     byte = 0x44
	opF64Add       byte = 0xa0
	opF64Sub       byte = 0xa1
	opF64Mul       byte = 0xa2
	opF64Div       byte = 0xa3
	opF64Floor     byte = 0x9c
	opF64Neg       byte = 0x9a
	opF64ConvI32U  byte = 0xb8 // f64.convert_i32_u
	opF64ReintI64  byte = 0xbf // f64.reinterpret_i64
	opI64ReintF64  byte = 0xbd // i64.reinterpret_f64
	opI32WrapI64   byte = 0xa7
	opI64ShrU      byte = 0x88
	opI64ExtendI32 byte = 0xac // i64.extend_i32_s(不用 _u 因 status 是 i32)

	// i32.store(PW10 零跨界 ③a:savedTop 写回 top mirror 字低 32 位)。
	opI32Store byte = 0x36

	// i32 算术 / 比较 + i64.eqz(PW10 零跨界 ③b:段帧动态寻址 + RETURN 守卫)。
	opI32Add byte = 0x6a
	opI32Sub byte = 0x6b
	opI32Mul byte = 0x6c
	opI32LtS byte = 0x48
	opI64Eqz byte = 0x50
)

// blockType 编码:0x40 = 空(无返回值),或单值类型(i32=0x7f 等)。
const btVoid byte = 0x40

// emitter 累积一个 gibbous 函数体的 Wasm 字节码。
type emitter struct {
	buf []byte
}

func newEmitter() *emitter { return &emitter{} }

func (e *emitter) bytes() []byte { return e.buf }

func (e *emitter) raw(b ...byte) { e.buf = append(e.buf, b...) }

// uleb / sleb:LEB128 编码(Wasm 立即数格式)。
func (e *emitter) uleb(v uint32) {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		e.buf = append(e.buf, c)
		if v == 0 {
			return
		}
	}
}

// sleb64 有符号 LEB128(i64.const 用)。
func (e *emitter) sleb64(v int64) {
	for {
		c := byte(v & 0x7f)
		v >>= 7 // 算术右移(Go int64 >> 是算术移位)
		// 符号位与 continuation 判定
		signBitSet := c&0x40 != 0
		if (v == 0 && !signBitSet) || (v == -1 && signBitSet) {
			e.buf = append(e.buf, c)
			return
		}
		e.buf = append(e.buf, c|0x80)
	}
}

func (e *emitter) sleb32(v int32) { e.sleb64(int64(v)) }

// --- 指令 emit ---

// localGet $idx
func (e *emitter) localGet(idx uint32) { e.raw(opLocalGet); e.uleb(idx) }

// localSet $idx
func (e *emitter) localSet(idx uint32) { e.raw(opLocalSet); e.uleb(idx) }

// i32Const v
func (e *emitter) i32Const(v int32) { e.raw(opI32Const); e.sleb32(v) }

// i64Const v(NaN-box u64 立即数:作 int64 编码,sleb64 处理符号扩展)。
func (e *emitter) i64Const(v uint64) { e.raw(opI64Const); e.sleb64(int64(v)) }

// i64Load (align=3 自然对齐) offset:栈顶是地址。
func (e *emitter) i64Load(offset uint32) { e.raw(opI64Load, 0x03); e.uleb(offset) }

// i32Load (align=2) offset:栈顶是地址。读 u64 标志字低 4 字节(小端,0/1 在低位)。
func (e *emitter) i32Load(offset uint32) { e.raw(0x28, 0x02); e.uleb(offset) }

// i32Store (align=2) offset:栈上 [addr, value]。写 mirror 字低 32 位(top 字)。
func (e *emitter) i32Store(offset uint32) { e.raw(opI32Store, 0x02); e.uleb(offset) }

// i64Store (align=3) offset:栈上 [addr, value]。
func (e *emitter) i64Store(offset uint32) { e.raw(opI64Store, 0x03); e.uleb(offset) }

// call $funcidx
func (e *emitter) call(funcidx uint32) { e.raw(opCall); e.uleb(funcidx) }

// callIndirect typeidx tableidx —— 经表间接调用(0x11 typeidx tableidx)。
// 栈顶须先压完实参再压 funcref 表索引(i32);typeidx 给被调签名,tableidx=0
// 是唯一 import 表。PW10 R3:gibbous→gibbous 经共享 env.table[slot] 直达。
func (e *emitter) callIndirect(typeidx, tableidx uint32) {
	e.raw(opCallIndirect)
	e.uleb(typeidx)
	e.uleb(tableidx)
}

// ret(return 当前栈顶 i32 status)
func (e *emitter) ret() { e.raw(opReturn) }

// drop 丢弃栈顶一个值(0x1a)。
func (e *emitter) drop() { e.raw(0x1a) }

// --- PW3 算术 / 比较 / 类型转换 / 结构化块 ---

func (e *emitter) localTee(idx uint32) { e.raw(opLocalTee); e.uleb(idx) }

// 二元 / 一元数值与比较指令(操作数已在 Wasm 栈上)。
func (e *emitter) f64Add()   { e.raw(opF64Add) }
func (e *emitter) f64Sub()   { e.raw(opF64Sub) }
func (e *emitter) f64Mul()   { e.raw(opF64Mul) }
func (e *emitter) f64Div()   { e.raw(opF64Div) }
func (e *emitter) f64Neg()   { e.raw(opF64Neg) }
func (e *emitter) f64Floor() { e.raw(opF64Floor) }
func (e *emitter) f64Ne()    { e.raw(opF64Ne) }
func (e *emitter) f64Lt()    { e.raw(opF64Lt) }
func (e *emitter) f64Le()    { e.raw(opF64Le) }
func (e *emitter) f64Ge()    { e.raw(opF64Ge) }
func (e *emitter) f64Eq()    { e.raw(opF64Eq) }

// f64Const 发 f64.const(8 字节小端 IEEE-754)。
func (e *emitter) f64Const(v float64) {
	e.raw(opF64Const)
	bits := math.Float64bits(v)
	for i := 0; i < 8; i++ {
		e.buf = append(e.buf, byte(bits>>(8*uint(i))))
	}
}
func (e *emitter) f64ReinterpretI64() { e.raw(opF64ReintI64) }
func (e *emitter) i64ReinterpretF64() { e.raw(opI64ReintF64) }
func (e *emitter) i64LtU()            { e.raw(opI64LtU) }
func (e *emitter) i64LtS()            { e.raw(opI64LtS) }
func (e *emitter) i64Ne()             { e.raw(opI64Ne) }
func (e *emitter) i64Eq()             { e.raw(opI64Eq) }
func (e *emitter) i64And()            { e.raw(opI64And) }
func (e *emitter) i64ShrU()           { e.raw(opI64ShrU) }
func (e *emitter) i32WrapI64()        { e.raw(opI32WrapI64) }
func (e *emitter) i32And()            { e.raw(opI32And) }
func (e *emitter) i32Ne()             { e.raw(opI32Ne) }
func (e *emitter) i32Eq()             { e.raw(opI32Eq) }
func (e *emitter) i32Eqz()            { e.raw(opI32Eqz) }

// i32 算术 / 比较 + i64.eqz(PW10 零跨界 ③b:段帧寻址 + 守卫;操作数已在栈上)。
func (e *emitter) i32Add() { e.raw(opI32Add) }
func (e *emitter) i32Sub() { e.raw(opI32Sub) }
func (e *emitter) i32Mul() { e.raw(opI32Mul) }
func (e *emitter) i32LtS() { e.raw(opI32LtS) }
func (e *emitter) i64Eqz() { e.raw(opI64Eqz) }

// ifVoid 开一个无返回值的 if 块(条件 i32 已在栈顶)。配对 elseOp/end。
func (e *emitter) ifVoid()       { e.raw(opIf, btVoid) }
func (e *emitter) elseOp()       { e.raw(opElse) }
func (e *emitter) end()          { e.raw(opEnd) }
func (e *emitter) brIf(d uint32) { e.raw(opBrIf); e.uleb(d) }

// block / loop:开一个无返回值的结构化块(PW4 relooper 嵌套)。配对 end。
//   - block:br N 跳到其 end(前向跳/汇合)
//   - loop :br N 跳到其起点(回边/continue)
func (e *emitter) block()      { e.raw(opBlock, btVoid) }
func (e *emitter) loop()       { e.raw(opLoop, btVoid) }
func (e *emitter) br(d uint32) { e.raw(opBr); e.uleb(d) }

// 注:结构化控制流(loop/block 多 BB)留 PW4 relooper;本组 if/else 仅用于
// 算术 opcode 的「快/慢路径」单 BB 内分支(不切 Lua pc)。
