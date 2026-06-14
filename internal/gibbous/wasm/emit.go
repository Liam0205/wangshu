//go:build wangshu_p3

package wasm

// wasm function body 字节编码器(02-translation §6.3 emitter)。
//
// 累积一个 Wasm 函数体的指令字节(不含 local decl 前缀与末尾 end,由
// module 组装阶段包裹)。提供 P3 翻译需要的指令子集 + LEB128 编码 +
// 基本块标签管理。
//
// Wasm 指令编码参考:https://webassembly.github.io/spec/core/binary/instructions.html

// wasmOp 常用 Wasm 指令 opcode(只列 P3 PW2 用到的;PW3 控制流补充)。
const (
	opReturn   byte = 0x0f
	opCall     byte = 0x10
	opLocalGet byte = 0x20
	opLocalSet byte = 0x21
	opI64Load  byte = 0x29
	opI64Store byte = 0x37
	opI32Const byte = 0x41
	opI64Const byte = 0x42
	opEnd      byte = 0x0b
)

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

// i64Store (align=3) offset:栈上 [addr, value]。
func (e *emitter) i64Store(offset uint32) { e.raw(opI64Store, 0x03); e.uleb(offset) }

// call $funcidx
func (e *emitter) call(funcidx uint32) { e.raw(opCall); e.uleb(funcidx) }

// ret(return 当前栈顶 i32 status)
func (e *emitter) ret() { e.raw(opReturn) }

// 注:结构化控制流(block/loop/br/br_if)与 i32 比较等指令原语留 PW3 控制流
// (relooper 结构化生成)落地时再加 —— 不预先引入未使用的 emit 方法。
