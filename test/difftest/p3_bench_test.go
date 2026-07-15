//go:build wangshu_p3 && wangshu_profile

// P3 performance-axis measurement (docs/design/p3-wasm-tier/08-testing-strategy.md V14-V16).
//
// Compares the same loop-heavy kernel via the shared force-all entry: crescent
// (force=false) vs gibbous (force=true). **Measurement protocol**: each b.N
// iteration builds a new State + repeatedly runs the kernel, so the kernel
// function truly goes through wazero after promotion (first call crescent,
// second call onward gibbous).
//
// For recording current status only — if the performance axis (≥2x) is not
// met, record it honestly; do not block correctness-axis delivery (user
// decision "ship correctness first, defer performance", see §11 PW9 reconcile).

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// loopKernel is a loop-heavy kernel: the repeatedly-called inner function
// sumto is promoted to gibbous under force-all.
const loopKernel = `
local function sumto(n) local s = 0; for i = 1, n do s = s + i * 2 - 1 end return s end
local function body() local t = 0; for k = 1, 200 do t = t + sumto(1000) end return t end
return body`

// Multi-form kernels (used by V15 geomean): arithmetic loop / table
// read-write / nested calls / mixed.
var benchKernels = map[string]string{
	"loop": loopKernel,
	"table": `
local function fill(n) local t = {}; for i = 1, n do t[i] = i * i end local s = 0; for i = 1, n do s = s + t[i] end return s end
local function body() local t = 0; for k = 1, 200 do t = t + fill(500) end return t end
return body`,
	"call": `
local function inner(x) return x * 3 + 1 end
local function mid(x) return inner(x) + inner(x + 1) end
local function body() local t = 0; for k = 1, 100000 do t = t + mid(k) end return t end
return body`,
	"mixed": `
local function process(d, n) local sum, mx = 0, 0; for i = 1, n do local v = d[i] * 2 + 1; sum = sum + v; if v > mx then mx = v end end return sum + mx end
local function body() local t = 0; for k = 1, 300 do local d = {}; for i = 1, 300 do d[i] = i end t = t + process(d, 300) end return t end
return body`,
}

func benchKernel(b *testing.B, src string, force bool) {
	prog, err := wangshu.Compile([]byte(src), "p3bench")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	bodyVals, err := prog.Run(st)
	if err != nil {
		b.Fatalf("run main: %v", err)
	}
	body := bodyVals[0]
	if _, _, e := callBody(st, body); e != nil {
		b.Fatalf("warmup: %v", e)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, e := callBody(st, body); e != nil {
			b.Fatalf("iter %d: %v", i, e)
		}
	}
}

func benchTiered(b *testing.B, force bool) { benchKernel(b, loopKernel, force) }

func callBody(st *wangshu.State, body wangshu.Value) ([]wangshu.Value, int, error) {
	out, err := st.Call(body)
	return out, len(out), err
}

func BenchmarkP3_LoopCrescent(b *testing.B) { benchTiered(b, false) }
func BenchmarkP3_LoopGibbous(b *testing.B)  { benchTiered(b, true) }

// BenchmarkP3_Kernels multi-form crescent vs gibbous (V14 loop ≥2x / V15 geomean ≥1.5x).
func BenchmarkP3_Kernels(b *testing.B) {
	for _, name := range []string{"loop", "table", "call", "mixed"} {
		src := benchKernels[name]
		b.Run(name+"/crescent", func(b *testing.B) { benchKernel(b, src, false) })
		b.Run(name+"/gibbous", func(b *testing.B) { benchKernel(b, src, true) })
	}
}
