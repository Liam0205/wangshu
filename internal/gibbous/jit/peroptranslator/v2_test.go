//go:build wangshu_p4 && amd64 && linux

// v2_test.go — spike v2 tests: emit `mov [rbx+A*8], rax; xor eax,eax; ret`
// and run it via callJITSpec, which sets up rbx = vsBase. The host's
// value-stack slice gets the Value written into slot R(A) directly; this
// is the first time the spike has interacted with the host's memory.

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

func TestSpikeV2_Return42WritesSlot0(t *testing.T) {
	proto := compileSource(t, kernelSource("return 42"))
	trimmed := trimToHeadPlusReturn(proto)

	stub, err := TranslateSpikeV2(trimmed)
	if err != nil {
		t.Fatalf("TranslateSpikeV2: %v", err)
	}
	defer stub.Dispose()

	// Allocate a 4-slot value stack pre-filled with a poison value so we
	// can distinguish "stub didn't write" from "stub wrote zero".
	const poison = 0xDEADBEEFDEADBEEF
	vs := []uint64{poison, poison, poison, poison}

	ret := stub.Run(vs)
	if ret != 0 {
		t.Errorf("Run returned RAX = 0x%016x, want 0 (spike v2 returns 0 = no exit)", ret)
	}
	want := uint64(value.NumberValue(42))
	if vs[0] != want {
		t.Errorf("vs[0] = 0x%016x, want 0x%016x (NaN-boxed 42)", vs[0], want)
	}
	// Other slots untouched.
	for i := 1; i < len(vs); i++ {
		if vs[i] != poison {
			t.Errorf("vs[%d] = 0x%016x, want 0x%016x (poison); spike v2 over-wrote it", i, vs[i], uint64(poison))
		}
	}
}

func TestSpikeV2_BoolNilWriteSlot0(t *testing.T) {
	cases := []struct {
		body string
		want uint64
	}{
		{"return true", uint64(value.True)},
		{"return false", uint64(value.False)},
		{"return nil", uint64(value.Nil)},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			proto := compileSource(t, kernelSource(tc.body))
			trimmed := trimToHeadPlusReturn(proto)

			stub, err := TranslateSpikeV2(trimmed)
			if err != nil {
				t.Fatalf("TranslateSpikeV2: %v", err)
			}
			defer stub.Dispose()

			const poison = 0xDEADBEEFDEADBEEF
			vs := []uint64{poison, poison}
			_ = stub.Run(vs)
			if vs[0] != tc.want {
				t.Errorf("body %q: vs[0] = 0x%016x, want 0x%016x", tc.body, vs[0], tc.want)
			}
			if vs[1] != poison {
				t.Errorf("body %q: vs[1] = 0x%016x got clobbered, want 0x%016x (poison)", tc.body, vs[1], uint64(poison))
			}
		})
	}
}

// TestSpikeV2_RepeatedRunReusesStub validates that the stub can be invoked
// multiple times (the mmap segment is read-only, no per-call state in the
// trampoline besides the stack-saved callee-saved regs).
func TestSpikeV2_RepeatedRunReusesStub(t *testing.T) {
	proto := compileSource(t, kernelSource("return 7"))
	trimmed := trimToHeadPlusReturn(proto)

	stub, err := TranslateSpikeV2(trimmed)
	if err != nil {
		t.Fatalf("TranslateSpikeV2: %v", err)
	}
	defer stub.Dispose()

	want := uint64(value.NumberValue(7))
	for iter := 0; iter < 100; iter++ {
		vs := []uint64{0}
		_ = stub.Run(vs)
		if vs[0] != want {
			t.Fatalf("iter %d: vs[0] = 0x%016x, want 0x%016x", iter, vs[0], want)
		}
	}
}
