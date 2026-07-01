//go:build wangshu_p4

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestPJ10Native_CFG_LinearReturn: a Proto with LOADK + RETURN is a single
// BB. reachableBlocks returns just the entry.
func TestPJ10Native_CFG_LinearReturn(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		MaxStack: 2,
	}
	c := buildCFG(proto)
	if len(c.blocks) != 1 {
		t.Fatalf("want 1 BB, got %d", len(c.blocks))
	}
	reach := c.reachableBlocks()
	if len(reach) != 1 || !reach[c.entry] {
		t.Fatalf("reachable = %v, want just entry (%d)", reach, c.entry)
	}
	if !c.isReducible() {
		t.Fatal("linear CFG must be reducible")
	}
}

// TestPJ10Native_CFG_IfElse: `if x then y=1 else y=2 end` — TEST + JMP +
// diamond. Two-BB diamond after the test.
func TestPJ10Native_CFG_IfElse(t *testing.T) {
	// Bytecode fragment:
	//   0: TEST     A=0 C=0    ; if R(0) then pc++ (falsy branch)
	//   1: JMP      sBx=2       ; skip truthy branch → pc=4
	//   2: LOADK    A=1 Bx=0    ; truthy: R(1) = 1
	//   3: JMP      sBx=1       ; skip else → pc=5
	//   4: LOADK    A=1 Bx=1    ; else: R(1) = 2
	//   5: RETURN   A=1 B=2 C=0 ; return R(1)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 2),
			bytecode.EncodeABx(bytecode.LOADK, 1, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABx(bytecode.LOADK, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
		MaxStack: 2,
	}
	c := buildCFG(proto)
	if len(c.blocks) < 4 {
		t.Fatalf("want at least 4 BBs (test, then, else, ret), got %d", len(c.blocks))
	}
	reach := c.reachableBlocks()
	if len(reach) < 4 {
		t.Fatalf("want at least 4 reachable BBs, got %d: %v", len(reach), reach)
	}
	if !c.isReducible() {
		t.Fatal("if/else CFG must be reducible")
	}
}

// TestPJ10Native_CodeBuf_LabelResolve: bind BB0 at offset 0, emit a
// 5-byte JMP with rel32 fixup targeting BB1, bind BB1 at offset 5. After
// resolve, the rel32 must be 0 (BB1 is right after the JMP).
func TestPJ10Native_CodeBuf_LabelResolve(t *testing.T) {
	buf := newCodeBuf(2)
	if err := buf.bindLabel(0); err != nil {
		t.Fatalf("bind BB0: %v", err)
	}
	// Emit `jmp rel32` = 5 bytes (0xe9 + 4-byte disp).
	jmpStart := buf.pos()
	buf.emit([]byte{0xe9, 0, 0, 0, 0})
	buf.addFixup(jmpStart+1, buf.pos(), 1)
	if err := buf.bindLabel(1); err != nil {
		t.Fatalf("bind BB1: %v", err)
	}
	if err := buf.resolveLabels(); err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	// rel32 payload at bytes[jmpStart+1..jmpStart+5] should be 0.
	for i := int32(1); i <= 4; i++ {
		if buf.bytes[jmpStart+i] != 0 {
			t.Errorf("rel32 byte %d = %x, want 0", i, buf.bytes[jmpStart+i])
		}
	}
}

// TestPJ10Native_CodeBuf_BackwardJump: bind BB0 at 0, skip 5 bytes for a
// dummy instruction, bind BB1 at 5, emit a jmp back to BB0 at offset 5.
// The jmp instruction is 5 bytes so its end is at offset 10.
// After resolve, the rel32 must be bb0_offset - jmp_end = 0 - 10 = -10.
func TestPJ10Native_CodeBuf_BackwardJump(t *testing.T) {
	buf := newCodeBuf(2)
	buf.bindLabel(0)
	buf.emit([]byte{0x90, 0x90, 0x90, 0x90, 0x90}) // 5 nops
	buf.bindLabel(1)
	jmpStart := buf.pos()
	buf.emit([]byte{0xe9, 0, 0, 0, 0})
	buf.addFixup(jmpStart+1, buf.pos(), 0)
	if err := buf.resolveLabels(); err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}
	got := int32(buf.bytes[jmpStart+1]) |
		int32(buf.bytes[jmpStart+2])<<8 |
		int32(buf.bytes[jmpStart+3])<<16 |
		int32(buf.bytes[jmpStart+4])<<24
	if got != -10 {
		t.Errorf("rel32 = %d, want -10", got)
	}
}
