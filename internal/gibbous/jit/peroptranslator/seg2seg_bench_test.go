//go:build wangshu_p4 && wangshu_profile && amd64 && linux

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// seg2segKernel is a call-heavy kernel whose callee `leaf` is a
// never-exits native segment (multi-BB FORLOOP, all inline ops), so it
// exercises the issue #50 Spike 5 segment-to-segment dispatch. The
// caller loops n times, each iteration calling leaf once.
const seg2segKernel = `
local function leaf(x)
  local a = x
  for i = 1, 4 do
    a = 1
    a = 2
    a = 3
    a = 4
  end
  return a
end
local function kernel(n)
  local s = 0
  for i = 1, n do
    local t = i + 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10
    s = s + leaf(t)
  end
  return s
end
return kernel(100000)
`

// BenchmarkSeg2Seg_On measures the call-heavy kernel with
// segment-to-segment dispatch active (the default). Compare to
// BenchmarkSeg2Seg_Off to isolate the win over the exit-reason path.
func BenchmarkSeg2Seg_On(b *testing.B) {
	benchSeg2Seg(b, true)
}

// BenchmarkSeg2Seg_Off measures the same kernel with segment-to-segment
// forced off, so every CALL rides the Spike 2-4 HelperExecutePlainCall
// exit-reason path instead.
func BenchmarkSeg2Seg_Off(b *testing.B) {
	benchSeg2Seg(b, false)
}

func benchSeg2Seg(b *testing.B, on bool) {
	restore := peroptranslator.SetSegToSegEnabledForTest(on)
	defer restore()

	prog, err := wangshu.Compile([]byte(seg2segKernel), "seg2segbench")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(true)
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}
