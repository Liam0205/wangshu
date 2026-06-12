// int2fb / fb2int — Lua 5.1's "floating-point byte" encoding for NEWTABLE B/C
// and SETLIST(02 §4-10/§10 缺口收口).
//
// 一个 9-bit fb 把 (mantissa: low 3 bits, exp: high 6 bits) 编码成
// `(8 + mantissa) << exp` 当 exp>0,否则就是 mantissa(0..7)。
// 这让 9 bits 表达 0..2^16+ 的近似容量,容差换字段宽。
//
// 算法严格对齐 Lua 5.1 lobject.c (luaO_int2fb / luaO_fb2int)。
package bytecode

// Int2Fb encodes x ≥ 0 into a 9-bit fb approximation (overshoots when truncated).
func Int2Fb(x uint32) uint32 {
	e := uint32(0)
	if x < 8 {
		return x
	}
	for x >= (8 << 4) { // 大块快进
		x = (x + 0xF) >> 4
		e += 4
	}
	for x >= 16 {
		x = (x + 1) >> 1
		e++
	}
	return ((e + 1) << 3) | (x - 8)
}

// Fb2Int decodes an fb back to its (rounded-up) integer value.
// 严格对齐官方 luaO_fb2int:e = (x>>3) & 31 —— 9-bit 字段的高位区
// [256,511] 若漏掩码会移位 31 位溢出(luac 同构软承诺下是真实解码入口)。
func Fb2Int(x uint32) uint32 {
	e := (x >> 3) & 31
	if e == 0 {
		return x
	}
	return ((x & 7) + 8) << (e - 1)
}
