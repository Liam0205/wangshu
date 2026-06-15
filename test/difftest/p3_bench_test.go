//go:build wangshu_p3 && wangshu_profile

// P3 性能轴实测(docs/design/p3-wasm-tier/08-testing-strategy.md V14-V16)。
//
// 经公共 force-all 入口对比同一循环密集核的 crescent(force=false)vs gibbous
// (force=true)。**实测口径**:每个 b.N 迭代新建 State + 反复跑 kernel,使核函数
// 升层后真走 wazero(首调 crescent,二调起 gibbous)。
//
// 仅记录现状用——性能轴(≥2x)若不达标,诚实记录,不阻断正确性轴交付(用户拍板
// 「先交正确性,性能拆后续」,见 §11 PW9 对账)。

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// loopKernel 循环密集核:被反复调用的内层函数 sumto 在 force-all 下升 gibbous。
const loopKernel = `
local function sumto(n) local s = 0; for i = 1, n do s = s + i * 2 - 1 end return s end
local function body() local t = 0; for k = 1, 200 do t = t + sumto(1000) end return t end
return body`

// 多形态核(V15 geomean 用):算术循环 / 表读写 / 嵌套调用 / 混合。
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

// BenchmarkP3_Kernels 多形态 crescent vs gibbous(V14 loop ≥2x / V15 geomean ≥1.5x)。
func BenchmarkP3_Kernels(b *testing.B) {
	for _, name := range []string{"loop", "table", "call", "mixed"} {
		src := benchKernels[name]
		b.Run(name+"/crescent", func(b *testing.B) { benchKernel(b, src, false) })
		b.Run(name+"/gibbous", func(b *testing.B) { benchKernel(b, src, true) })
	}
}
