//go:build wangshu_p3

package wasm

// wasm function body byte encoder (02-translation §6.3 emitter).
//
// Accumulates the instruction bytes of a Wasm function body (excluding the
// local decl prefix and trailing end, which the module assembly stage wraps).
// Provides the instruction subset P3 translation needs + LEB128 encoding +
// basic-block label management.
//
// Wasm instruction encoding reference: https://webassembly.github.io/spec/core/binary/instructions.html

import "math"

// wasmOp: common Wasm instruction opcodes.
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

	// Comparison / arithmetic / type conversion (PW3).
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
	opF64Gt        byte = 0x64
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
	opI64ExtendI32 byte = 0xac // i64.extend_i32_s (not _u, since status is i32)

	// i32.store (PW10 zero-crossing ③a: savedTop written back to the low 32 bits of the top mirror word).
	opI32Store byte = 0x36

	// i32 arithmetic / comparison + i64.eqz (PW10 zero-crossing ③b: segment-frame dynamic addressing + RETURN guard).
	opI32Add byte = 0x6a
	opI32Sub byte = 0x6b
	opI32Mul byte = 0x6c
	opI32LtS byte = 0x48
	opI64Eqz byte = 0x50

	// ④ emitCall guard fast path: i64.shl (frame-word packing) + i64.extend_i32_u (unsigned extension,
	// positive integers like slot/funcIdx → i64) + i32.gt_s (MaxStack headroom guard) + i32.ge_s (depth guard).
	opI64Shl        byte = 0x86
	opI64ExtendUI32 byte = 0xad // i64.extend_i32_u (unsigned i32→i64, high 32 bits zeroed)
	opI32GtS        byte = 0x4a
)

// blockType encoding: 0x40 = empty (no return value), or a single-value type (i32=0x7f, etc.).
const btVoid byte = 0x40

// emitter accumulates the Wasm bytecode of a gibbous function body.
type emitter struct {
	buf []byte
}

func newEmitter() *emitter { return &emitter{} }

func (e *emitter) bytes() []byte { return e.buf }

func (e *emitter) raw(b ...byte) { e.buf = append(e.buf, b...) }

// uleb / sleb: LEB128 encoding (Wasm immediate format).
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

// sleb64 signed LEB128 (used by i64.const).
func (e *emitter) sleb64(v int64) {
	for {
		c := byte(v & 0x7f)
		v >>= 7 // arithmetic shift right (Go int64 >> is an arithmetic shift)
		// sign bit and continuation check
		signBitSet := c&0x40 != 0
		if (v == 0 && !signBitSet) || (v == -1 && signBitSet) {
			e.buf = append(e.buf, c)
			return
		}
		e.buf = append(e.buf, c|0x80)
	}
}

func (e *emitter) sleb32(v int32) { e.sleb64(int64(v)) }

// --- instruction emit ---

// localGet $idx
func (e *emitter) localGet(idx uint32) { e.raw(opLocalGet); e.uleb(idx) }

// localSet $idx
func (e *emitter) localSet(idx uint32) { e.raw(opLocalSet); e.uleb(idx) }

// i32Const v
func (e *emitter) i32Const(v int32) { e.raw(opI32Const); e.sleb32(v) }

// i64Const v (NaN-box u64 immediate: encoded as int64, sleb64 handles sign extension).
func (e *emitter) i64Const(v uint64) { e.raw(opI64Const); e.sleb64(int64(v)) }

// i64Load (align=3 natural alignment) offset: top of stack is the address.
func (e *emitter) i64Load(offset uint32) { e.raw(opI64Load, 0x03); e.uleb(offset) }

// i32Load (align=2) offset: top of stack is the address. Reads the low 4 bytes of the u64 flag word (little-endian, 0/1 in the low bits).
func (e *emitter) i32Load(offset uint32) { e.raw(0x28, 0x02); e.uleb(offset) }

// i32Store (align=2) offset: stack holds [addr, value]. Writes the low 32 bits of the mirror word (top word).
func (e *emitter) i32Store(offset uint32) { e.raw(opI32Store, 0x02); e.uleb(offset) }

// i64Store (align=3) offset: stack holds [addr, value].
func (e *emitter) i64Store(offset uint32) { e.raw(opI64Store, 0x03); e.uleb(offset) }

// call $funcidx
func (e *emitter) call(funcidx uint32) { e.raw(opCall); e.uleb(funcidx) }

// callIndirect typeidx tableidx —— indirect call through the table (0x11 typeidx tableidx).
// The stack must push all arguments first, then the funcref table index (i32); typeidx gives the
// callee signature, tableidx=0 is the sole import table. PW10 R3: gibbous→gibbous reaches directly through the shared env.table[slot].
func (e *emitter) callIndirect(typeidx, tableidx uint32) {
	e.raw(opCallIndirect)
	e.uleb(typeidx)
	e.uleb(tableidx)
}

// ret (return the current top-of-stack i32 status)
func (e *emitter) ret() { e.raw(opReturn) }

// drop discards one value from the top of stack (0x1a).
func (e *emitter) drop() { e.raw(0x1a) }

// --- PW3 arithmetic / comparison / type conversion / structured blocks ---

func (e *emitter) localTee(idx uint32) { e.raw(opLocalTee); e.uleb(idx) }

// Binary / unary numeric and comparison instructions (operands already on the Wasm stack).
func (e *emitter) f64Add()   { e.raw(opF64Add) }
func (e *emitter) f64Sub()   { e.raw(opF64Sub) }
func (e *emitter) f64Mul()   { e.raw(opF64Mul) }
func (e *emitter) f64Div()   { e.raw(opF64Div) }
func (e *emitter) f64Neg()   { e.raw(opF64Neg) }
func (e *emitter) f64Floor() { e.raw(opF64Floor) }
func (e *emitter) f64Ne()    { e.raw(opF64Ne) }
func (e *emitter) f64Lt()    { e.raw(opF64Lt) }
func (e *emitter) f64Gt()    { e.raw(opF64Gt) }
func (e *emitter) f64Le()    { e.raw(opF64Le) }
func (e *emitter) f64Ge()    { e.raw(opF64Ge) }
func (e *emitter) f64Eq()    { e.raw(opF64Eq) }

// f64Const emits f64.const (8-byte little-endian IEEE-754).
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

// i32 arithmetic / comparison + i64.eqz (PW10 zero-crossing ③b: segment-frame addressing + guard; operands already on the stack).
func (e *emitter) i32Add() { e.raw(opI32Add) }
func (e *emitter) i32Sub() { e.raw(opI32Sub) }
func (e *emitter) i32Mul() { e.raw(opI32Mul) }
func (e *emitter) i32LtS() { e.raw(opI32LtS) }
func (e *emitter) i64Eqz() { e.raw(opI64Eqz) }

// PW10 zero-crossing ④: i64 shift + unsigned extension + i32 greater-than (frame-word packing + MaxStack headroom guard).
func (e *emitter) i64Shl()        { e.raw(opI64Shl) }
func (e *emitter) i64ExtendUI32() { e.raw(opI64ExtendUI32) }
func (e *emitter) i32GtS()        { e.raw(opI32GtS) }

// PW10 zero-crossing ④: i64 arithmetic (segment-frame-word packing + offset add) / logical or (flag-bit merge).
func (e *emitter) i64Add() { e.raw(0x7c) }
func (e *emitter) i64Or()  { e.raw(0x84) }

// ifVoid opens an if block with no return value (condition i32 already on top of stack). Pairs with elseOp/end.
func (e *emitter) ifVoid()       { e.raw(opIf, btVoid) }
func (e *emitter) elseOp()       { e.raw(opElse) }
func (e *emitter) end()          { e.raw(opEnd) }
func (e *emitter) brIf(d uint32) { e.raw(opBrIf); e.uleb(d) }

// block / loop: open a structured block with no return value (PW4 relooper nesting). Pairs with end.
//   - block: br N jumps to its end (forward jump / join)
//   - loop : br N jumps to its start (back edge / continue)
func (e *emitter) block()      { e.raw(opBlock, btVoid) }
func (e *emitter) loop()       { e.raw(opLoop, btVoid) }
func (e *emitter) br(d uint32) { e.raw(opBr); e.uleb(d) }

// Note: structured control flow (multi-BB loop/block) is left to the PW4 relooper; this group's
// if/else is only for "fast/slow path" branching within a single BB of an arithmetic opcode (does not split the Lua pc).
