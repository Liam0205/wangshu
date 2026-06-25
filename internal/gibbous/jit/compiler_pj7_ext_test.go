//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ7_LoadBoolReturnSucceeds 真接入扩展:LOADBOOL A B 0; RETURN A 1
// 形态经 P4 端到端 byte-equal。
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
			c := New()
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.LOADBOOL, 0, tc.B, 0), // LOADBOOL 0 B 0
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),      // RETURN 0 2
				},
			}
			if !c.SupportsAllOpcodes(proto) {
				t.Fatal("SupportsAllOpcodes should accept LOADBOOL+RETURN")
			}
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)

			stack := make([]uint64, 4)
			status := gc.Run(stack, 0)
			if status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			got := value.Value(stack[0])
			if got != tc.want {
				t.Errorf("R(0) = 0x%x, want 0x%x (%v)", uint64(got), uint64(tc.want), tc.want)
			}
		})
	}
}

// TestPJ7_LoadBoolPCBumpRejected LOADBOOL C != 0 拒(单 BB 不能跳 pc)。
func TestPJ7_LoadBoolPCBumpRejected(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADBOOL, 0, 1, 1), // C=1 触发 pc++
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should REJECT LOADBOOL C != 0(需 pc++,留 PJ8+ 控制流)")
	}
}

// TestPJ7_LoadNilReturnSucceeds 真接入扩展:LOADNIL A A; RETURN A 1
// 形态经 P4 端到端 byte-equal。
func TestPJ7_LoadNilReturnSucceeds(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 0, 0), // LOADNIL 0 0(单寄存器赋 nil)
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("SupportsAllOpcodes should accept LOADNIL A A + RETURN")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got := value.Value(stack[0])
	if got != value.Nil {
		t.Errorf("R(0) = 0x%x, want Nil 0x%x", uint64(got), uint64(value.Nil))
	}
}

// TestPJ7_LoadNilMultiRegisterRejected LOADNIL A B (A != B) 拒——本 PJ7 简化
// 形态只支持单寄存器赋 nil。
func TestPJ7_LoadNilMultiRegisterRejected(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 2, 0), // LOADNIL 0 2 (A=0, B=2,多寄存器)
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should REJECT LOADNIL A B (A != B)")
	}
}
