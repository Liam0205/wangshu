//go:build !wangshu_p3 && !wangshu_p4

// realworld_bench_test.go: file split out for the `_Wangshu` (crescent) /
// `_Gopher` benchmarks, with the `!wangshu_p3 && !wangshu_p4` build tag added
// to avoid the wangshu_profile sampling hooks of the p3/p4 build polluting the
// crescent numbers + duplicating bench-p1 (issue #15 review).
//
// The `_Gibbous` benchmarks live in `realworld_gibbous_test.go` (P3) and
// `realworld_gibbous_jit_test.go` (P4), each under its own build tag, mutually
// exclusive with this file.
package realworld

import (
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

func benchVM(b *testing.B, name string, wangshuSide bool) {
	src := loadScript(b, name)
	if wangshuSide {
		prog, err := wangshu.Compile(src, name)
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
		return
	}
	L := lua.NewState()
	defer L.Close()
	fn, err := L.LoadString(string(src))
	if err != nil {
		b.Fatalf("gopher compile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.Push(fn)
		if err := L.PCall(0, lua.MultRet, nil); err != nil {
			b.Fatalf("gopher run: %v", err)
		}
		L.SetTop(0)
	}
}

func BenchmarkFib_Wangshu(b *testing.B)          { benchVM(b, "fib", true) }
func BenchmarkFib_Gopher(b *testing.B)           { benchVM(b, "fib", false) }
func BenchmarkBinaryTrees_Wangshu(b *testing.B)  { benchVM(b, "binarytrees", true) }
func BenchmarkBinaryTrees_Gopher(b *testing.B)   { benchVM(b, "binarytrees", false) }
func BenchmarkSpectralNorm_Wangshu(b *testing.B) { benchVM(b, "spectralnorm", true) }
func BenchmarkSpectralNorm_Gopher(b *testing.B)  { benchVM(b, "spectralnorm", false) }
func BenchmarkFannkuch_Wangshu(b *testing.B)     { benchVM(b, "fannkuch", true) }
func BenchmarkFannkuch_Gopher(b *testing.B)      { benchVM(b, "fannkuch", false) }
func BenchmarkNBody_Wangshu(b *testing.B)        { benchVM(b, "nbody", true) }
func BenchmarkNBody_Gopher(b *testing.B)         { benchVM(b, "nbody", false) }
