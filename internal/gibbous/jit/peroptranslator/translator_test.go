//go:build wangshu_p4 && amd64 && linux

// translator_test.go — end-to-end smoke test for the spike:
// front-end compiles a tiny `return <const>` body, the spike translator
// emits the equivalent machine code, MmapCode + CallJIT runs it, and we
// check the returned u64 equals the NaN-boxed Value bit-for-bit.
//
// This is the smallest possible exercise of the "Lua bytecode in -> native
// code -> real value out" flow. It does NOT validate anything the host
// needs (no value-stack write-back, no DoReturn, no error propagation) —
// those land in spike v2 once SupportsAllOpcodes is wired up.

package peroptranslator

import (
	"math"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

func compileSource(t *testing.T, src string) *bytecode.Proto {
	t.Helper()
	lx := lex.New([]byte(src), "spike")
	blk, err := parse.Parse(lx, "spike")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, protos, err := compile.Compile(blk, "spike")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Find the first non-vararg Proto — that's our inner kernel for the
	// `return X` body. The top-level chunk is vararg and not what we want.
	for _, p := range protos {
		if !p.IsVararg {
			return p
		}
	}
	t.Fatalf("no non-vararg proto found among %d protos", len(protos))
	return nil
}

// kernelSource wraps the body in a non-vararg inner function so the front-
// end emits a Proto whose Code is exactly `<head op>; RETURN A=0 B=2` —
// i.e. the shape the spike supports. The outer (vararg) Proto is ignored.
func kernelSource(body string) string {
	return "local function kernel()\n  " + body + "\nend\nreturn kernel()"
}

// trimToHeadPlusReturn returns a clone of proto with Code shortened to
// [head op, RETURN B=2]. The frontend emits a trailing `RETURN A=0 B=1`
// sentinel as dead code after the explicit RETURN; the spike doesn't
// model that yet (CFG construction in v2 will drop unreachable BBs the
// same way P3 wasm does).
func trimToHeadPlusReturn(p *bytecode.Proto) *bytecode.Proto {
	return &bytecode.Proto{
		Code:      p.Code[:2],
		Consts:    p.Consts,
		NumParams: p.NumParams,
		MaxStack:  p.MaxStack,
	}
}

func TestSpike_Return42(t *testing.T) {
	proto := compileSource(t, kernelSource("return 42"))
	trimmed := trimToHeadPlusReturn(proto)

	stub, err := TranslateSpike(trimmed)
	if err != nil {
		t.Fatalf("TranslateSpike: %v", err)
	}
	defer func() {
		if err := stub.Dispose(); err != nil {
			t.Errorf("Dispose: %v", err)
		}
	}()

	got := stub.Call()
	want := uint64(value.NumberValue(42))
	if got != want {
		t.Errorf("Call() = 0x%016x, want 0x%016x (NaN-boxed 42)", got, want)
	}
	if math.Float64frombits(got) != 42 {
		t.Errorf("Call() bits do not decode to 42.0: got %v", math.Float64frombits(got))
	}
}

// TestSpike_VariousNumbers pushes a few numbers through the spike to make
// sure the NaN-box round-trip doesn't drift for unusual values (0, negatives,
// large floats).
func TestSpike_VariousNumbers(t *testing.T) {
	for _, body := range []string{
		"return 0",
		"return 1",
		"return -1",
		"return 0.5",
		"return 1e100",
	} {
		t.Run(body, func(t *testing.T) {
			proto := compileSource(t, kernelSource(body))
			trimmed := trimToHeadPlusReturn(proto)
			stub, err := TranslateSpike(trimmed)
			if err != nil {
				t.Fatalf("TranslateSpike: %v", err)
			}
			defer stub.Dispose()

			got := stub.Call()
			want := uint64(proto.Consts[0])
			if got != want {
				t.Errorf("Call() = 0x%016x, want 0x%016x", got, want)
			}
		})
	}
}

// TestSpike_BoolNil exercises LOADBOOL (true/false) and LOADNIL — the
// non-LOADK head ops added in spike v1. Each Lua body translates to a
// head op whose imm comes from a different source (proto.Consts for
// LOADK; the B field for LOADBOOL; a fixed Nil constant for LOADNIL).
func TestSpike_BoolNil(t *testing.T) {
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

			stub, err := TranslateSpike(trimmed)
			if err != nil {
				t.Fatalf("TranslateSpike: %v", err)
			}
			defer stub.Dispose()

			got := stub.Call()
			if got != tc.want {
				t.Errorf("body %q: Call() = 0x%016x, want 0x%016x", tc.body, got, tc.want)
			}
		})
	}
}

// TestSpike_RejectsUnsupportedShape locks in the error message contract
// for shapes the spike does not yet handle — string LOADK, LOADBOOL with
// the skip bit set, MOVE, the wrong RETURN shape, etc. These are negative
// tests so future spike-v2 work can expand the supported set without
// breaking the contract for callers that depend on a clean error.
func TestSpike_RejectsUnsupportedShape(t *testing.T) {
	cases := []struct {
		name   string
		proto  *bytecode.Proto
		needle string // substring expected somewhere in the error
	}{
		{
			name: "LOADBOOL_skip",
			proto: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.LOADBOOL, 0, 1, 1), // C=1: skip next
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
				},
			},
			needle: "LOADBOOL C!=0",
		},
		{
			name: "MOVE_unsupported",
			proto: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 0, 1, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
				},
			},
			needle: "unsupported head op",
		},
		{
			name: "RETURN_wrong_B",
			proto: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 3, 0), // B=3 = two retvals
				},
				Consts: []value.Value{value.NumberValue(1)},
			},
			needle: "RETURN B=2",
		},
		{
			name: "wrong_length",
			proto: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
				},
				Consts: []value.Value{value.NumberValue(1)},
			},
			needle: "Code length 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := TranslateSpike(tc.proto)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.needle)
			}
			if !strings.Contains(err.Error(), tc.needle) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.needle)
			}
		})
	}
}
