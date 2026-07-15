//go:build lua_gopher

// pineapple_gopher_test.go: benchmark for pineapple using the gopher-lua backend
// (baseline reference; wangshu is not involved at all).
//
// How to run: `go test -tags=lua_gopher -bench=. ./benchmarks/pineapple/`.
// pineapple's `transform_by_lua` switches to gopher-lua via the `lua_gopher` build
// tag; wangshu is only imported here, never reached along the code path.
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_Gopher_Row(b *testing.B)    { runBenchmark(b, "row") }
func BenchmarkPineappleLuaOp_Gopher_Column(b *testing.B) { runBenchmark(b, "column") }

func BenchmarkPineappleLuaOp_Gopher_CommonRow(b *testing.B) { runBenchmarkCommon(b, "row") }
func BenchmarkPineappleLuaOp_Gopher_CommonColumn(b *testing.B) {
	runBenchmarkCommon(b, "column")
}
