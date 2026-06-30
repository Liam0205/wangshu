//go:build wangshu_p4 && amd64 && linux

// translator_test.go — end-to-end smoke test for spike v0:
// front-end compiles `return 42`, the spike translator emits the equivalent
// machine code, MmapCode + CallJIT runs it, and we check the returned u64
// equals the NaN-boxed 42.
//
// This is the smallest possible exercise of the "Lua bytecode in → native
// code → real value out" flow. It does NOT validate anything the host
// needs (no value stack write-back, no DoReturn, no error propagation) —
// those land in spike v2 once SupportsAllOpcodes is wired up.

package peroptranslator

import (
	"math"
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
// end emits a Proto whose Code is exactly `LOADK A=0, K(Bx); RETURN A=0 B=2`
// — i.e. the shape spike v0 supports. The outer (vararg) Proto is ignored.
func kernelSource(body string) string {
	return "local function kernel()\n  " + body + "\nend\nreturn kernel()"
}

func TestSpike_Return42(t *testing.T) {
	proto := compileSource(t, kernelSource("return 42"))

	// Sanity-check the shape before handing it to the translator.
	if len(proto.Code) != 3 {
		// Frontend emits LOADK + RETURN + implicit-trailing RETURN A=0 B=1
		// (the implicit "no more values" sentinel). Spike v0 wants the
		// first two only — trim and re-wrap.
		t.Logf("proto.Code length is %d (expected 3 with trailing RETURN)", len(proto.Code))
	}
	if op := bytecode.Op(proto.Code[0]); op != bytecode.LOADK {
		t.Fatalf("expected pc=0 LOADK, got %s", op)
	}
	if op := bytecode.Op(proto.Code[1]); op != bytecode.RETURN {
		t.Fatalf("expected pc=1 RETURN, got %s", op)
	}

	// Hand the first two instructions to the translator. The implicit
	// trailing RETURN A=0 B=1 (dead code after explicit RETURN) is dropped
	// — spike v0 doesn't model trailing sentinels yet.
	trimmedProto := &bytecode.Proto{
		Code:      proto.Code[:2],
		Consts:    proto.Consts,
		NumParams: proto.NumParams,
		MaxStack:  proto.MaxStack,
	}

	stub, err := TranslateSpike(trimmedProto)
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

// TestSpike_VariousImmediates pushes a few numbers through the spike to make
// sure the NaN-box round-trip doesn't drift for unusual values (0, negatives,
// large floats).
func TestSpike_VariousImmediates(t *testing.T) {
	for _, body := range []string{
		"return 0",
		"return 1",
		"return -1",
		"return 0.5",
		"return 1e100",
	} {
		t.Run(body, func(t *testing.T) {
			proto := compileSource(t, kernelSource(body))
			trimmed := &bytecode.Proto{
				Code:      proto.Code[:2],
				Consts:    proto.Consts,
				NumParams: proto.NumParams,
				MaxStack:  proto.MaxStack,
			}
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
