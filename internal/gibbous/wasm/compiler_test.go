//go:build wangshu_p3

package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// PW1/PW2 Compiler basic acceptance. The SupportsAllOpcodes whitelist grows as PW progresses.

func newTestCompiler(t *testing.T) (*Compiler, func()) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	c := NewCompiler(ctx, rt, &mockHost{})
	return c, func() { _ = rt.Close(ctx) }
}

// TestCompiler_SupportsWhitelist PW2 whitelist: straight-line opcodes are supported, unimplemented ones are rejected.
func TestCompiler_SupportsWhitelist(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	// Single-BB straight-line opcodes → supported
	supported := []struct {
		name string
		code []bytecode.Instruction
	}{
		{"MOVE+RETURN", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}},
		{"LOADNIL+RETURN", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}},
		{"ADD+RETURN(PW3)", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.ADD, 0, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		}},
		{"GETTABLE+RETURN(PW5)", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 0, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}},
	}
	for _, tc := range supported {
		t.Run("yes/"+tc.name, func(t *testing.T) {
			if !c.SupportsAllOpcodes(&bytecode.Proto{Code: tc.code}) {
				t.Errorf("%q should be supported", tc.name)
			}
		})
	}

	// Unimplemented opcode (VARARG is never supported, 02 §1.3) → rejected
	notYet := []bytecode.Instruction{
		bytecode.EncodeABC(bytecode.VARARG, 0, 0, 0),
		bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
	}
	if c.SupportsAllOpcodes(&bytecode.Proto{Code: notYet}) {
		t.Error("VARARG should NOT be supported")
	}
}

// TestCompiler_ImplementsP3Compiler compile-time assertion that Compiler implements the interface.
func TestCompiler_ImplementsP3Compiler(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	var _ bridge.P3Compiler = c
}

// TestCompiler_PanicRecover a panic inside Compile is caught by defer recover
// and converted to *CompileError(BackendPanic), without crossing the interface (02 §1.4).
//
// Triggered with a malformed Proto that makes translate panic (Consts out of bounds:
// LOADK references a nonexistent constant index). SupportsAllOpcodes lets it through
// first (numeric-constant assumption), then Compile panics on the out-of-bounds
// Consts[bx] during translation → recover.
func TestCompiler_PanicRecover(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	// LOADK references Consts[5] but Consts is empty → translate emitLoadK panics out of bounds
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 5),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: nil, // empty → Consts[5] out of bounds
	}
	// SupportsAllOpcodes: LOADK is a non-string constant (StringLitIdx empty) → allowed; single BB.
	if !c.SupportsAllOpcodes(proto) {
		t.Skip("proto not supported, panic path not reachable")
	}
	gc, err := c.Compile(proto, nil)
	if gc != nil {
		t.Error("panic path should return nil GibbousCode")
	}
	if err == nil {
		t.Fatal("panic should be recovered to error")
	}
	if ce, ok := err.(*bridge.CompileError); !ok || ce.Kind != bridge.CompileErrBackendPanic {
		t.Errorf("expected BackendPanic CompileError, got %v", err)
	}
}

// TestCompiler_EmptyProtoVacuous an empty Proto is vacuously supported (no unsupported op).
func TestCompiler_EmptyProtoVacuous(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	if !c.SupportsAllOpcodes(&bytecode.Proto{Code: nil}) {
		t.Error("empty Proto should be vacuously supported")
	}
}
