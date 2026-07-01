//go:build !wangshu_p3 && !wangshu_p4

// heavy_bench_test.go:`_Wangshu`(crescent)/`_Gopher` benchmark 拆出文件,
// 加 `!wangshu_p3 && !wangshu_p4` build tag 避免 profile 采样钩污染新月数字。
//
// `_Gibbous` 在 `heavy_gibbous_test.go` 里(wangshu_p3 tag),`_GibbousJIT` 在
// `heavy_gibbous_jit_test.go` 里(wangshu_p4 tag),与本文件互斥。
package heavy

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

func BenchmarkHeavyArith_Wangshu(b *testing.B)     { benchVM(b, "heavy_arith", true) }
func BenchmarkHeavyArith_Gopher(b *testing.B)      { benchVM(b, "heavy_arith", false) }
func BenchmarkHeavyRecursion_Wangshu(b *testing.B) { benchVM(b, "heavy_recursion", true) }
func BenchmarkHeavyRecursion_Gopher(b *testing.B)  { benchVM(b, "heavy_recursion", false) }
func BenchmarkHeavyFloatloop_Wangshu(b *testing.B) { benchVM(b, "heavy_floatloop", true) }
func BenchmarkHeavyFloatloop_Gopher(b *testing.B)  { benchVM(b, "heavy_floatloop", false) }
