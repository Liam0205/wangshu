//go:build !wangshu_p3

// Package baseline contains the 3-tier benchmark scripts (12 §6):
// simple / arith / loop, run on both Wangshu and gopher-lua.
//
// Acceptance criteria (roadmap §4): all three tiers must be ≥2x over
// gopher-lua (ns/op ratio).
// Run: `make bench-p1` (or `make bench`). Benchmarks in this package are
// relative measures; absolute values vary by machine.
//
// build tag `!wangshu_p3`: the `_Wangshu` (crescent) / `_Gopher` benchmarks
// are compiled only in the p1 build, avoiding the p3 build's
// wangshu_profile sampling hooks polluting the crescent numbers and
// duplicating bench-p1 (a fix for the brittle convention caught in the
// issue #15 review). The `_Gibbous` benchmark carries its own wangshu_p3
// build tag in `baseline_gibbous_test.go`, mutually exclusive with this file.
package baseline

import (
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

// —— Three-tier scripts (shape from 12 §6; the loop tier is the column-kernel
// shape from 02 §8 / roadmap §1) ——

// simple: mostly MOVE / comparison / jump.
const simpleSrc = `
local a, b = 1, 2
local r = 0
if a < b then r = a else r = b end
return r
`

// arith: 5-term Horner polynomial (shape from roadmap §1 calibration
// measurement 1).
const arithSrc = `
local x = 1.5
local r = ((((x + 2) * x + 3) * x + 4) * x + 5) * x + 6
return r
`

// loop: summation loop (02 §8; column-kernel shape).
const loopSrc = `
local s = 0
for i = 1, 1000 do s = s + i * i end
return s
`

// PJ3-style empty-body for loop (matched against wangshu PJ3 FORLOOP inline):
// **shape parity** — gopher runs the same wrap-kernel x50 shape, exactly
// matching wangshu_jit's wrapKernelJIT (avoiding an apples-to-oranges
// workload mismatch).
const pj3EmptyLoop100Src = `local function kernel() for i = 1, 100 do end end
local t = 0; for _ = 1, 50 do t = kernel() or 0 end; return t`
const pj3EmptyLoop1000Src = `local function kernel() for i = 1, 1000 do end end
local t = 0; for _ = 1, 50 do t = kernel() or 0 end; return t`
const pj3EmptyLoop10000Src = `local function kernel() for i = 1, 10000 do end end
local t = 0; for _ = 1, 50 do t = kernel() or 0 end; return t`

// PJ3 body inline parity — `local s=0; for i=1,K do s=s+1 end; return s`
const pj3BodyAdd1000Src = `local function kernel() local s=0; for i=1,1000 do s=s+1 end; return s end
local t = 0; for _ = 1, 50 do t = kernel() end; return t`
const pj3BodyAdd10000Src = `local function kernel() local s=0; for i=1,10000 do s=s+1 end; return s end
local t = 0; for _ = 1, 50 do t = kernel() end; return t`

func benchWangshu(b *testing.B, src string) {
	prog, err := wangshu.Compile([]byte(src), "bench")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func benchGopher(b *testing.B, src string) {
	L := lua.NewState()
	defer L.Close()
	fn, err := L.LoadString(src)
	if err != nil {
		b.Fatalf("gopher compile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.Push(fn)
		if err := L.PCall(0, lua.MultRet, nil); err != nil {
			b.Fatalf("gopher run: %v", err)
		}
		L.Pop(L.GetTop())
	}
}

func BenchmarkSimple_Wangshu(b *testing.B) { benchWangshu(b, simpleSrc) }
func BenchmarkSimple_Gopher(b *testing.B)  { benchGopher(b, simpleSrc) }

func BenchmarkArith_Wangshu(b *testing.B) { benchWangshu(b, arithSrc) }
func BenchmarkArith_Gopher(b *testing.B)  { benchGopher(b, arithSrc) }

func BenchmarkLoop_Wangshu(b *testing.B) { benchWangshu(b, loopSrc) }
func BenchmarkLoop_Gopher(b *testing.B)  { benchGopher(b, loopSrc) }

// _GopherKernel: gopher runs the exact same wrapKernel(body)x50 shape
// as the P3 `_Gibbous`/`_WangshuKernel` benches (issue #93). The README
// baseline rows use these as the denominator for the P3 columns — a
// vararg top-level chunk never promotes, so the P3 columns must measure
// the kernel-wrapped shape; dividing that kernel-x50 number by the
// top-level-x1 gopher number understated P3 by ~50x. wrapKernel + the
// body constants live in baseline_kernel_test.go (tag-neutral).
func BenchmarkSimple_GopherKernel(b *testing.B) { benchGopher(b, wrapKernel(simpleBody)) }
func BenchmarkArith_GopherKernel(b *testing.B)  { benchGopher(b, wrapKernel(arithBody)) }
func BenchmarkLoop_GopherKernel(b *testing.B)   { benchGopher(b, wrapKernel(loopBody)) }

// PJ3-style empty-body for loop (gopher parity):
func BenchmarkPJ3EmptyLoop100_Gopher(b *testing.B)   { benchGopher(b, pj3EmptyLoop100Src) }
func BenchmarkPJ3EmptyLoop1000_Gopher(b *testing.B)  { benchGopher(b, pj3EmptyLoop1000Src) }
func BenchmarkPJ3EmptyLoop10000_Gopher(b *testing.B) { benchGopher(b, pj3EmptyLoop10000Src) }

// PJ3 body inline (`local s=0; for i=1,K do s=s+1 end; return s`) gopher parity:
func BenchmarkPJ3BodyAdd1000_Gopher(b *testing.B)  { benchGopher(b, pj3BodyAdd1000Src) }
func BenchmarkPJ3BodyAdd10000_Gopher(b *testing.B) { benchGopher(b, pj3BodyAdd10000Src) }
