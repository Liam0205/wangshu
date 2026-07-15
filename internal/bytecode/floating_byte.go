// int2fb / fb2int — Lua 5.1's "floating-point byte" encoding for NEWTABLE B/C
// and SETLIST (02 §4-10/§10 gap closure).
//
// A 9-bit fb encodes (mantissa: low 3 bits, exp: high 6 bits) as
// `(8 + mantissa) << exp` when exp>0, otherwise it is just mantissa (0..7).
// This lets 9 bits express an approximate capacity of 0..2^16+, trading
// precision for field width.
//
// The algorithm strictly matches Lua 5.1 lobject.c (luaO_int2fb / luaO_fb2int).
package bytecode

// Int2Fb encodes x ≥ 0 into a 9-bit fb approximation (overshoots when truncated).
func Int2Fb(x uint32) uint32 {
	e := uint32(0)
	if x < 8 {
		return x
	}
	for x >= (8 << 4) { // fast-forward large chunks
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
// Strictly matches the official luaO_fb2int: e = (x>>3) & 31 — for the high
// region [256,511] of the 9-bit field, a missing mask would shift by 31 bits and
// overflow (this is the real decode entry point under the luac-parity soft
// commitment).
func Fb2Int(x uint32) uint32 {
	e := (x >> 3) & 31
	if e == 0 {
		return x
	}
	return ((x & 7) + 8) << (e - 1)
}
