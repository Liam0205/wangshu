package stdlib

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// TestMathIntrinsicNamesInSync guards the two-source-of-truth split for
// the issue #77 math intrinsics: stdlib's mathIntrinsics (name -> P4 kind,
// used to tag host closures at registration) and bytecode.MathIntrinsicNames
// (the name set the frontend uses to mark intrinsic CALL sites for the
// density-gate exemption). If a new intrinsic is added to one side only,
// the frontend would mark calls the backend can't emit (harmless — the
// runtime guard misses -> exit-reason) or fail to mark calls the backend
// can emit (the function wouldn't promote). Keep them identical.
func TestMathIntrinsicNamesInSync(t *testing.T) {
	for name, kind := range mathIntrinsics {
		if kind == jit.IntrinsicNone {
			t.Errorf("mathIntrinsics[%q] = IntrinsicNone (0); should be a real kind", name)
		}
		if !bytecode.MathIntrinsicNames[name] {
			t.Errorf("mathIntrinsics has %q but bytecode.MathIntrinsicNames does not", name)
		}
	}
	for name := range bytecode.MathIntrinsicNames {
		if _, ok := mathIntrinsics[name]; !ok {
			t.Errorf("bytecode.MathIntrinsicNames has %q but stdlib mathIntrinsics does not", name)
		}
	}
	if len(mathIntrinsics) != len(bytecode.MathIntrinsicNames) {
		t.Errorf("size mismatch: mathIntrinsics=%d bytecode.MathIntrinsicNames=%d",
			len(mathIntrinsics), len(bytecode.MathIntrinsicNames))
	}
}
