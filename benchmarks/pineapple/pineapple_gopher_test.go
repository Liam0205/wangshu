//go:build lua_gopher

// pineapple_gopher_test.go:pineapple 用 gopher-lua backend 时的 benchmark
// (对照 baseline,wangshu 完全不参与)。
//
// 跑法:`go test -tags=lua_gopher -bench=. ./benchmarks/pineapple/`。
// pineapple `transform_by_lua` 经 `lua_gopher` build tag 切到 gopher-lua,
// wangshu 此时只是被 import 但不被路径触达。
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_Gopher(b *testing.B) {
	runBenchmark(b)
}
