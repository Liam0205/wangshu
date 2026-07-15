//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ7_LoadBoolReturnSucceeds real-path extension: LOADBOOL A B 0; RETURN A 1
// This form goes end-to-end byte-equal through P4 (value verified via mock host.SetReg).
func TestPJ7_LoadBoolReturnSucceeds(t *testing.T) {
	cases := []struct {
		name string
		B    int
		want value.Value
	}{
		{"true", 1, value.BoolValue(true)},
		{"false", 0, value.BoolValue(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.LOADBOOL, 0, tc.B, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
				},
			}
			gc, host := compileWithHost(t, proto)
			defer tryDispose(t, gc)

			stack := make([]uint64, 4)
			status := gc.Run(stack, 0)
			if status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			got, ok := host.regs[0]
			if !ok {
				t.Fatal("SetReg(0, ...) not called")
			}
			if value.Value(got) != tc.want {
				t.Errorf("SetReg(0, 0x%x), want 0x%x (%v)", got, uint64(tc.want), tc.want)
			}
		})
	}
}

// TestPJ7_LoadBoolPCBumpRejected LOADBOOL C != 0 is rejected (a single BB cannot skip pc).
func TestPJ7_LoadBoolPCBumpRejected(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADBOOL, 0, 1, 1), // C=1 triggers pc++
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should REJECT LOADBOOL C != 0(需 pc++,留 PJ8+ 控制流)")
	}
}

// TestPJ7_LoadNilReturnSucceeds real-path extension: LOADNIL A A; RETURN A 1
// This form goes end-to-end byte-equal through P4 (value verified via mock host.SetReg).
func TestPJ7_LoadNilReturnSucceeds(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("SetReg(0, ...) not called")
	}
	if value.Value(got) != value.Nil {
		t.Errorf("SetReg(0, 0x%x), want Nil 0x%x", got, uint64(value.Nil))
	}
}

// TestPJ7_LoadNilMultiRegisterRejected LOADNIL A B (A != B) is rejected: this simplified
// PJ7 form only supports assigning nil to a single register.
func TestPJ7_LoadNilMultiRegisterRejected(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 2, 0), // LOADNIL 0 2 (A=0, B=2, multi-register)
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should REJECT LOADNIL A B (A != B)")
	}
}
