//go:build wangshu_p4 && wangshu_profile && amd64 && linux

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// stringLoadKKernel is an arithmetic-heavy kernel that ALSO contains a
// string literal — the shape issue #69 unblocks. Before, the `"scale"`
// literal's LOADK sank the WHOLE function (including its arithmetic loop)
// to the interpreter; now the string LOADK bakes an interned imm64 so the
// proto promotes and the arithmetic runs natively, while the one string
// op (a table string-key read, hoisted out of the hot arithmetic) is a
// single exit-reason. This is the realistic "rule-script" win: a function
// isn't kicked off native wholesale by one string literal.
//
// The string op is deliberately OUT of the inner arithmetic so the win is
// visible — a kernel that is ITSELF string-op-dominated (per-iteration
// CONCAT/#s) stays ~interpreter-speed because those ops each exit-reason
// (that case is measured by the _Interp gap being ~0, not this bench).
const stringLoadKKernel = `
local function kernel(n)
  local cfg = { scale = 3 }
  local key = "scale"
  local s = cfg[key]
  local acc = 0
  for i = 1, n do
    acc = acc + i * s - (i - 1) * s + i % 7 + s
  end
  return acc
end
return kernel(200000)
`

// BenchmarkStringLoadK_P4 vs _Interp measures a string-literal-containing
// kernel on the native path (P4) vs forced onto the interpreter (pre-#69
// behavior). NOTE ON EXPECTATION: #69 is an ACCEPTANCE-surface change, not
// a per-op speedup. Before #69 this proto had NativeRunCount=0 (F7-a
// rejected it over the string literal); after, it promotes and runs
// natively (NativeRunCount>0, proven in e2e_string_loadk_test.go). Whether
// that makes THIS kernel faster depends on its non-string op mix — a body
// whose native-ized arithmetic barely beats interpreter dispatch shows
// roughly par here, while a proto that would otherwise sink ALL its work
// (loops, calls) to the interpreter over one literal is where the real win
// lands. This bench documents the shape and lets us track it; it is not a
// speedup claim.
func BenchmarkStringLoadK_P4(b *testing.B) {
	benchStringLoadK(b, true)
}

// BenchmarkStringLoadK_Interp: the same kernel forced onto the interpreter
// (no promotion) — the pre-#69 baseline.
func BenchmarkStringLoadK_Interp(b *testing.B) {
	benchStringLoadK(b, false)
}

func benchStringLoadK(b *testing.B, promote bool) {
	prog, err := wangshu.Compile([]byte(stringLoadKKernel), "strloadkbench")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(promote)
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}
