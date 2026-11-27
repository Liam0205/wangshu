//go:build wangshu_p4 && (amd64 || arm64)

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// The exit-reason word carries the pc of an instruction in 22 bits. A Proto whose last
// instruction is at the highest pc that fits is accepted; one instruction more and the native
// translator declines it.
func TestAnalyzeNative_PCLimit(t *testing.T) {
	build := func(n int) *bytecode.Proto {
		code := make([]bytecode.Instruction, n)
		for i := range code {
			code[i] = bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0)
		}
		code[n-1] = bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0)
		return &bytecode.Proto{Code: code, NumParams: 1, MaxStack: 2}
	}
	if !AnalyzeNative(build(maxNativePC + 1)) {
		t.Fatalf("a Proto whose last instruction is at pc %d was rejected", maxNativePC)
	}
	if AnalyzeNative(build(maxNativePC + 2)) {
		t.Errorf("a Proto with an instruction at pc %d was accepted: that pc does not fit the exit-reason word", maxNativePC+1)
	}
}
