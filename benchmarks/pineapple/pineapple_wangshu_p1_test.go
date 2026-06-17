//go:build !wangshu_p3 && !lua_gopher

// pineapple_wangshu_p1_test.go:wangshu 默认 build 作 pineapple 默认 lua backend
// 时的 benchmark(新月解释器路径,P3 dead-code)。
//
// 互斥 tag:
//   - !wangshu_p3:与 pineapple_wangshu_p3_test.go(wangshu_p3 && wangshu_profile)互斥
//   - !lua_gopher:与 pineapple_gopher_test.go(lua_gopher)互斥
//
// 跑法:`go test -bench=. ./benchmarks/pineapple/`(默认 tag,即本文件生效)。
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_WangshuP1_Row(b *testing.B)    { runBenchmark(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP1_Column(b *testing.B) { runBenchmark(b, "column") }

func BenchmarkPineappleLuaOp_WangshuP1_CommonRow(b *testing.B)    { runBenchmarkCommon(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP1_CommonColumn(b *testing.B) { runBenchmarkCommon(b, "column") }
