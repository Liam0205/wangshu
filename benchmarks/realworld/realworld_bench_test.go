//go:build !wangshu_p3 && !wangshu_p4

// realworld_bench_test.go:`_Wangshu`(crescent)/`_Gopher` benchmark 拆出文件,
// 加 `!wangshu_p3 && !wangshu_p4` build tag 避免 p3/p4 build 的 wangshu_profile
// 采样钩污染新月数字 + 与 bench-p1 重复(issue #15 review)。
//
// `_Gibbous` benchmark 在 `realworld_gibbous_test.go`(P3)与
// `realworld_gibbous_jit_test.go`(P4)里独享各自 build tag,与本文件互斥。
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
