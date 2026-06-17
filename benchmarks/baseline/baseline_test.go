//go:build !wangshu_p3

// Package baseline contains the 3-tier benchmark scripts (12 §6):
// simple / arith / loop, run on both Wangshu and gopher-lua.
//
// 验收口径(roadmap §4):三档脚本全部 ≥2x over gopher-lua(ns/op 比)。
// 运行:`make bench-p1`(或 `make bench`)。本包基准是相对量;绝对值依机器而异。
//
// build tag `!wangshu_p3`:`_Wangshu`(crescent)/ `_Gopher` 基准只在 p1 build
// 编入,避免 p3 build 的 wangshu_profile 采样钩污染新月数字 + 与 bench-p1 重复
// (issue #15 review 抓出的 brittle convention 改良)。`_Gibbous` benchmark 在
// `baseline_gibbous_test.go` 里独自有 wangshu_p3 build tag,与本文件互斥。
package baseline

import (
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

// —— 三档脚本(12 §6 形状;循环档即 02 §8 / roadmap §1 的列内核形状)——

// simple:MOVE/比较/跳转为主。
const simpleSrc = `
local a, b = 1, 2
local r = 0
if a < b then r = a else r = b end
return r
`

// arith:Horner 5 次多项式(roadmap §1 校准测量 1 的形状)。
const arithSrc = `
local x = 1.5
local r = ((((x + 2) * x + 3) * x + 4) * x + 5) * x + 6
return r
`

// loop:求和循环(02 §8;列内核形状)。
const loopSrc = `
local s = 0
for i = 1, 1000 do s = s + i * i end
return s
`

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
