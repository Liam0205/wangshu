//go:build wangshu_p3 && wangshu_profile

// Regression test for emitCompareTerm's TEST opcode C-field bug.
//
// Lua 5.1 reference: `TEST A C` semantics is `if not (R(A) <=> C) then pc++`,
// where C is the boolean to compare against (not A). The wasm translator's
// emitCompareTerm at internal/gibbous/wasm/translate_control.go used to compare
// against A by mistake, which inverted the if-branch decision whenever A != 0.
//
// Symptom (originally caught by benchmarks/realworld binarytrees parity, only
// after the matrix CI gap fix surfaced it):
//
//	local function check(tree)
//	  if tree[1] then                  -- TEST A=1 C=0 after GETTABLE R(1):=tree[1]
//	    return 1 + check(tree[1]) + check(tree[2])
//	  end
//	  return 1
//	end
//
// With A=1 instead of C, the branch decision was Truthy(R(1)) != 1 → invert,
// so falsy R(1) made the wasm function take the truthy branch — recursing
// past leaf nodes until something not-a-table got dereferenced.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10_TestOpcodeCField locks down the TEST opcode's C-field semantics
// in the wasm translator. The kernel uses `if R(A) then` where R(A) is a
// boolean false; the wasm-translated check must correctly take the else
// branch and return 1, byte-equal to the interpreter.
func TestPW10_TestOpcodeCField(t *testing.T) {
	src := `
local function check(tree)
  if tree[1] then
    return 1 + check(tree[1]) + check(tree[2])
  end
  return 1
end
local t = { {false, false}, {false, false} }
return check, t`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	checkFn, tVal := rets[0], rets[1]
	checkPid := object.ClosureProtoID(st.arena, value.GCRefOf(checkFn))

	// Interpreter baseline: check(t) = 1 + check({f,f}) + check({f,f}) = 3.
	base, eBase := st.Call(value.GCRefOf(checkFn), []value.Value{tVal}, 1)
	if eBase != nil {
		t.Fatalf("interp check(t): %v", eBase)
	}
	if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != 3 {
		t.Fatalf("interp check(t) = %v, want 3", base[0])
	}

	// Force promotion of check and re-run; result must stay 3.
	if !promoteProto(st, checkPid) {
		t.Skip("check not supported by current gibbous whitelist")
	}
	got, eHot := st.Call(value.GCRefOf(checkFn), []value.Value{tVal}, 1)
	if eHot != nil {
		t.Fatalf("gibbous check(t): %v", eHot)
	}
	if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 3 {
		t.Errorf("gibbous check(t) = %v, want 3 (TEST opcode C-field regression)", got[0])
	}
}
