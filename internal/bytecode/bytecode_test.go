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
	// 全字段最大值,不应跨字段污染。
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
	// 38..63 预留:String() 应返回 "INVALID"(也允许实现期改为 "RESERVED" 之类)。
	if OpCode(38).String() != "INVALID" {
		t.Logf("opcode 38 name = %q", OpCode(38).String())
	}
}

// Float-byte 编码:对照 Lua 5.1 lobject.c 的具体取值(几个手工锚点)。
func TestInt2Fb(t *testing.T) {
	cases := []struct {
		in  uint32
		out uint32
	}{
		{0, 0}, {1, 1}, {7, 7}, // <8 直透
		{8, 8}, {9, 9}, {15, 15}, // exp=1,mantissa 0..7 → fb 8..15
		{16, 16}, {17, 17}, // exp=2,mantissa 0
		{32, 24}, {64, 32},
	}
	for _, c := range cases {
		if got := Int2Fb(c.in); got != c.out {
			t.Errorf("Int2Fb(%d) = %d, want %d", c.in, got, c.out)
		}
	}
}

func TestFb2Int(t *testing.T) {
	// fb2int 是 int2fb 的"近似还原"——大值会向上取整;但小于 8 的值是精确恢复。
	for x := uint32(0); x < 8; x++ {
		if Fb2Int(Int2Fb(x)) != x {
			t.Errorf("small round trip lost: %d", x)
		}
	}
	// 对手工值核对:Fb2Int(8) = (8 | 0) << 0 = 8 — 但偏移要对。
	// fb2int(fb): (8 | (fb & 7)) << ((fb >> 3) - 1)
	// 注意 fb=8: (8|0) << 0 = 8 ✓
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

// TestProtoExample8: 02 §8 的 f(n) 求和示例,核对字节码序列与寄存器分配同构(P1 软承诺)。
//
// 这是 04 §10 黄金字节码测试的种子:M8 codegen 完成后从源码自动产出本序列;现在先用手工
// 构造的 Proto 验证 02 §8 的字节码可被本包正确编解码。
func TestProtoExample8_FByteCodeSequence(t *testing.T) {
	// 与 02 §8 / 04 §10.3 字节码逐字节一致。
	// R0=n 形参; R1=s; R2..R5=for 四槽(idx/limit/step/v); R6=临时
	code := []Instruction{
		EncodeABx(LOADK, 1, 0),     // LOADK R1 K0(0)        ; s = 0
		EncodeABx(LOADK, 2, 1),     // LOADK R2 K1(1)        ; init=1
		EncodeABC(MOVE, 3, 0, 0),   // MOVE  R3 R0           ; limit=n
		EncodeABx(LOADK, 4, 1),     // LOADK R4 K1(1)        ; step=1
		EncodeAsBx(FORPREP, 2, 2),  // FORPREP R2 -> L1
		EncodeABC(MUL, 6, 5, 5),    // MUL   R6 R5 R5        ; tmp = i*i
		EncodeABC(ADD, 1, 1, 6),    // ADD   R1 R1 R6        ; s = s + tmp
		EncodeAsBx(FORLOOP, 2, -3), // FORLOOP R2 -> L0       (热点回边)
		EncodeABC(RETURN, 1, 2, 0), // RETURN R1 2          ; return s
		EncodeABC(RETURN, 0, 1, 0), // RETURN R0 1          ; 隐式 return
	}
	// 检查字段还原。
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

// TestFb2Int_Full9BitDomain 对照官方 luaO_fb2int 在全部 9-bit 输入上等价。
// 历史 bug:缺 &31 掩码时 [256,511] 区间分歧——fb=256 官方 256/旧实现 0,
// fb=257 官方 257/旧实现移位 31 溢出。luac 同构软承诺下外部字节码可达此区间。
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
	// 关键分歧点显式钉住
	if Fb2Int(256) != 256 {
		t.Errorf("Fb2Int(256) = %d, want 256", Fb2Int(256))
	}
	if Fb2Int(257) != 257 {
		t.Errorf("Fb2Int(257) = %d, want 257", Fb2Int(257))
	}
}
