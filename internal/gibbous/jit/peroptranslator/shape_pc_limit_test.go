//go:build wangshu_p4 && amd64

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// AnalyzeShape keeps the pc of each instruction it replays in 16 bits. A Proto whose RETURN is at
// the last pc that fits is accepted, with that pc recorded whole; one instruction more and the
// Proto is not replayed at all.
func TestAnalyzeShape_ReturnPCLimit(t *testing.T) {
	build := func(retPC int) *bytecode.Proto {
		code := make([]bytecode.Instruction, 0, retPC+1)
		for len(code) < retPC {
			code = append(code, bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0))
		}
		code = append(code, bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0))
		return &bytecode.Proto{Code: code, NumParams: 1, MaxStack: 2}
	}
	at := AnalyzeShape(build(maxShapePC))
	if !at.ok {
		t.Fatalf("a Proto whose RETURN is at pc %d was rejected", maxShapePC)
	}
	if int(at.retPC) != maxShapePC {
		t.Errorf("retPC recorded as %d, want %d", at.retPC, maxShapePC)
	}
	if last := at.sideEffects[len(at.sideEffects)-1]; int(last.pc) != maxShapePC-1 {
		t.Errorf("the last replayed instruction is recorded at pc %d, want %d", last.pc, maxShapePC-1)
	}
	if AnalyzeShape(build(maxShapePC + 1)).ok {
		t.Errorf("a Proto whose RETURN is at pc %d was accepted: its pcs do not fit the 16-bit fields", maxShapePC+1)
	}
}
