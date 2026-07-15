//go:build !wangshu_p3 && !lua_gopher

// pineapple_wangshu_p1_test.go: benchmark for the wangshu default build acting
// as pineapple's default lua backend (crescent interpreter path, P3 dead-code).
//
// Mutually exclusive tags:
//   - !wangshu_p3: mutually exclusive with pineapple_wangshu_p3_test.go (wangshu_p3 && wangshu_profile)
//   - !lua_gopher: mutually exclusive with pineapple_gopher_test.go (lua_gopher)
//
// How to run: `go test -bench=. ./benchmarks/pineapple/` (default tags, i.e. this file is in effect).
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_WangshuP1_Row(b *testing.B)    { runBenchmark(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP1_Column(b *testing.B) { runBenchmark(b, "column") }

func BenchmarkPineappleLuaOp_WangshuP1_CommonRow(b *testing.B) { runBenchmarkCommon(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP1_CommonColumn(b *testing.B) {
	runBenchmarkCommon(b, "column")
}
