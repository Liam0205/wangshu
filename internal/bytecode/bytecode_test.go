package bytecode

import "testing"

func TestEncodeABC(t *testing.T) {
	i := EncodeABC(ADD, 5, 257, 100)
	if Op(i) != ADD {
		t.Errorf("op: %s", Op(i))
	}
	if A(i) != 5 || B(i) != 257 || C(i) != 100 {
		t.Errorf("ABC fields: A=%d B=%d C=%d", A(i), B(i), C(i))
	}
	if !IsK(B(i)) || IsK(C(i)) {
		t.Errorf("RK classification: B is K=%v C is K=%v", IsK(B(i)), IsK(C(i)))
	}
	if KIdx(B(i)) != 1 {
		t.Errorf("KIdx: %d", KIdx(B(i)))
	}
}

func TestEncodeABx(t *testing.T) {
	i := EncodeABx(LOADK, 7, 100000)
	if Op(i) != LOADK || A(i) != 7 || Bx(i) != 100000 {
		t.Errorf("ABx fields: op=%s A=%d Bx=%d", Op(i), A(i), Bx(i))
	}
}

func TestEncodeAsBx(t *testing.T) {
	for _, sbx := range []int{-100000, -1, 0, 1, 100000} {
		i := EncodeAsBx(JMP, 0, sbx)
		if SBx(i) != sbx {
			t.Errorf("AsBx round trip sBx=%d: got %d", sbx, SBx(i))
		}
	}
}

func TestFieldBoundaryEncoding(t *testing.T) {
	// All fields at maximum, must not bleed across fields.
	i := EncodeABC(VARARG, MaxA, MaxBC, MaxBC)
	if A(i) != MaxA || B(i) != MaxBC || C(i) != MaxBC || Op(i) != VARARG {
		t.Errorf("max ABC fields: op=%s A=%d B=%d C=%d", Op(i), A(i), B(i), C(i))
	}
	i2 := EncodeABx(LOADK, 0, MaxBx)
	if Bx(i2) != MaxBx {
		t.Errorf("max Bx: %d", Bx(i2))
	}
}

func TestFmtRouting(t *testing.T) {
	cases := []struct {
		op   OpCode
		want Format
	}{
		{MOVE, FmtABC}, {ADD, FmtABC}, {CALL, FmtABC}, {RETURN, FmtABC},
		{LOADK, FmtABx}, {GETGLOBAL, FmtABx}, {SETGLOBAL, FmtABx}, {CLOSURE, FmtABx},
		{JMP, FmtAsBx}, {FORLOOP, FmtAsBx}, {FORPREP, FmtAsBx},
	}
	for _, c := range cases {
		if FormatOf(c.op) != c.want {
			t.Errorf("FormatOf(%s) = %d, want %d", c.op, FormatOf(c.op), c.want)
		}
	}
}

func TestOpcodeNames(t *testing.T) {
	for op := MOVE; op <= VARARG; op++ {
		if op.String() == "" || op.String() == "INVALID" {
			t.Errorf("opcode %d has no name", op)
		}
	}
	// 38..63 reserved: String() should return "INVALID" (also allowed to become "RESERVED" or similar during implementation).
	if OpCode(38).String() != "INVALID" {
		t.Logf("opcode 38 name = %q", OpCode(38).String())
	}
}

// Float-byte encoding: checked against specific values from Lua 5.1 lobject.c (a few manual anchors).
func TestInt2Fb(t *testing.T) {
	cases := []struct {
		in  uint32
		out uint32
	}{
		{0, 0}, {1, 1}, {7, 7}, // <8 pass-through
		{8, 8}, {9, 9}, {15, 15}, // exp=1, mantissa 0..7 → fb 8..15
		{16, 16}, {17, 17}, // exp=2, mantissa 0
		{32, 24}, {64, 32},
	}
	for _, c := range cases {
		if got := Int2Fb(c.in); got != c.out {
			t.Errorf("Int2Fb(%d) = %d, want %d", c.in, got, c.out)
		}
	}
}

func TestFb2Int(t *testing.T) {
	// fb2int is the "approximate reconstruction" of int2fb — large values round up; but values below 8 recover exactly.
	for x := uint32(0); x < 8; x++ {
		if Fb2Int(Int2Fb(x)) != x {
			t.Errorf("small round trip lost: %d", x)
		}
	}
	// Cross-check against manual values: Fb2Int(8) = (8 | 0) << 0 = 8 — but the shift must be right.
	// fb2int(fb): (8 | (fb & 7)) << ((fb >> 3) - 1)
	// note fb=8: (8|0) << 0 = 8 ✓
	if Fb2Int(8) != 8 {
		t.Errorf("Fb2Int(8) = %d, want 8", Fb2Int(8))
	}
	if Fb2Int(16) != 16 {
		t.Errorf("Fb2Int(16) = %d, want 16", Fb2Int(16))
	}
	if Fb2Int(24) != 32 {
		t.Errorf("Fb2Int(24) = %d, want 32", Fb2Int(24))
	}
}

// TestProtoExample8: the f(n) summation example of 02 §8, checking the bytecode
// sequence and register allocation are isomorphic (P1 soft promise).
//
// This is the seed of the 04 §10 golden-bytecode test: after M8 codegen is done
// this sequence will be produced automatically from source; for now a hand-built
// Proto verifies the 02 §8 bytecode can be correctly encoded/decoded by this package.
func TestProtoExample8_FByteCodeSequence(t *testing.T) {
	// Byte-for-byte consistent with 02 §8 / 04 §10.3 bytecode.
	// R0=n param; R1=s; R2..R5=for four slots (idx/limit/step/v); R6=temp
	code := []Instruction{
		EncodeABx(LOADK, 1, 0),     // LOADK R1 K0(0)        ; s = 0
		EncodeABx(LOADK, 2, 1),     // LOADK R2 K1(1)        ; init=1
		EncodeABC(MOVE, 3, 0, 0),   // MOVE  R3 R0           ; limit=n
		EncodeABx(LOADK, 4, 1),     // LOADK R4 K1(1)        ; step=1
		EncodeAsBx(FORPREP, 2, 2),  // FORPREP R2 -> L1
		EncodeABC(MUL, 6, 5, 5),    // MUL   R6 R5 R5        ; tmp = i*i
		EncodeABC(ADD, 1, 1, 6),    // ADD   R1 R1 R6        ; s = s + tmp
		EncodeAsBx(FORLOOP, 2, -3), // FORLOOP R2 -> L0       (hot back-edge)
		EncodeABC(RETURN, 1, 2, 0), // RETURN R1 2          ; return s
		EncodeABC(RETURN, 0, 1, 0), // RETURN R0 1          ; implicit return
	}
	// Check field reconstruction.
	if Op(code[0]) != LOADK || A(code[0]) != 1 || Bx(code[0]) != 0 {
		t.Errorf("LOADK R1 K0 corrupted")
	}
	if Op(code[4]) != FORPREP || SBx(code[4]) != 2 {
		t.Errorf("FORPREP sBx corrupted: %d", SBx(code[4]))
	}
	if Op(code[7]) != FORLOOP || SBx(code[7]) != -3 {
		t.Errorf("FORLOOP backedge sBx corrupted: %d", SBx(code[7]))
	}
	if Op(code[8]) != RETURN || A(code[8]) != 1 || B(code[8]) != 2 {
		t.Errorf("RETURN corrupted")
	}
}

// TestFb2Int_Full9BitDomain checks equivalence with the official luaO_fb2int over
// all 9-bit inputs. Historical bug: without the &31 mask the [256,511] range
// diverges — fb=256 official 256/old impl 0, fb=257 official 257/old impl shifts
// by 31 and overflows. Under the luac isomorphism soft promise, external bytecode
// can reach this range.
func TestFb2Int_Full9BitDomain(t *testing.T) {
	official := func(x uint32) uint32 {
		e := (x >> 3) & 31
		if e == 0 {
			return x
		}
		return ((x & 7) + 8) << (e - 1)
	}
	for fb := uint32(0); fb < 512; fb++ {
		if got, want := Fb2Int(fb), official(fb); got != want {
			t.Fatalf("Fb2Int(%d) = %d, want %d (luaO_fb2int)", fb, got, want)
		}
	}
	// Pin the key divergence points explicitly
	if Fb2Int(256) != 256 {
		t.Errorf("Fb2Int(256) = %d, want 256", Fb2Int(256))
	}
	if Fb2Int(257) != 257 {
		t.Errorf("Fb2Int(257) = %d, want 257", Fb2Int(257))
	}
}
