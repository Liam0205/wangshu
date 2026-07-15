// shared_test.go: config builder + items maker shared by the four-way comparison benchmarks.
//
// No build tag — the three files gopher / wangshu-p1 / wangshu-p3 all import the same
// package and use the same helpers.
package pineapple_bench

import (
	"context"
	"encoding/json"
	"testing"

	pine "github.com/Liam0205/pineapple/pine-go"

	// register operators such as transform_by_lua + recall_static (init() side effects)
	_ "github.com/Liam0205/pineapple/pine-go/operators"
	_ "github.com/Liam0205/pineapple/pine-go/operators/lua"
)

// L2_arithmetic shape: per-item arithmetic transform. Corresponds to the pineapple
// bench_lua_vs_go_test L2 case, **boundary-dominated** (each item crosses the boundary:
// SetGlobal 1 field + Call once + read return value once) — the best probe for whether
// wangshu p3 promotion actually pays off.
const (
	scriptArith = `function f() return item_price * 0.85 + 10.0 end`
	funcArith   = "f"
)

// makeItems replicates pineapple bench L2 itemGen.
func makeItems(n int) []any {
	items := make([]any, n)
	for i := range items {
		items[i] = map[string]any{"item_price": float64(100 + i)}
	}
	return items
}

// buildLuaConfig replicates pineapple bench buildLuaConfig: 1 recall_static + 1
// transform_by_lua, forming the simplest pipeline. DAG / I/O overhead is nearly 0,
// LuaOp share ≈100% — avoids the pipeline framework diluting the gibbous difference.
//
// storageMode selects row / column, corresponding to pineapple's top-level `storage_mode` field.
// An empty string is equivalent to the default (pineapple currently defaults to row).
func buildLuaConfig(luaScript, luaFunc string, items []any, storageMode string) map[string]any {
	luaOp := map[string]any{
		"type_name":           "transform_by_lua",
		"lua_script":          luaScript,
		"function_for_item":   luaFunc,
		"function_for_common": "",
		"$metadata": map[string]any{
			"item_input":  []string{"item_price"},
			"item_output": []string{"item_result"},
		},
	}
	recall := map[string]any{
		"type_name": "recall_static",
		"recall":    true,
		"items":     items,
		"$metadata": map[string]any{
			"item_output": []string{"item_price"},
		},
	}
	cfg := map[string]any{
		"_PINEAPPLE_VERSION": pine.Version,
		"pipeline_config": map[string]any{
			"operators":    map[string]any{"recall": recall, "op": luaOp},
			"pipeline_map": map[string]any{"stage1": map[string]any{"pipeline": []string{"recall", "op"}}},
		},
		"pipeline_group": map[string]any{
			"main": map[string]any{"pipeline": []string{"stage1"}},
		},
		"flow_contract": map[string]any{},
	}
	if storageMode != "" {
		cfg["storage_mode"] = storageMode
	}
	return cfg
}

// mustBuildEngine: JSON-serialize the config and feed it to pine.NewEngine; b.Fatal on build failure.
func mustBuildEngine(b *testing.B, cfg map[string]any) *pine.Engine {
	b.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		b.Fatal(err)
	}
	eng, err := pine.NewEngine(data)
	if err != nil {
		b.Fatalf("pine.NewEngine: %v", err)
	}
	return eng
}

// runBenchmark is the generic benchmark loop: reuse engine + per-iteration Execute.
//
// itemCount = 1000 (matching the N=1000 tier of pineapple bench L2, boundary-dominated
// and enough Call invocations to trigger wangshu HotEntryThreshold promotion).
//
// storageMode selects "row" / "column" / "" (default): matches pineapple's top-level
// `storage_mode` field.
func runBenchmark(b *testing.B, storageMode string) {
	const itemCount = 1000
	items := makeItems(itemCount)
	eng := mustBuildEngine(b, buildLuaConfig(scriptArith, funcArith, items, storageMode))
	req := &pine.Request{Common: map[string]any{}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Execute(ctx, req); err != nil {
			b.Fatalf("engine.Execute: %v", err)
		}
	}
}
