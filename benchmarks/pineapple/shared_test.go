// shared_test.go:四路对照 benchmark 共用的 config builder + items maker。
//
// 无 build tag —— gopher / wangshu-p1 / wangshu-p3 三个文件都 import 同 package
// 都用同一份 helper。
package pineapple_bench

import (
	"context"
	"encoding/json"
	"testing"

	pine "github.com/Liam0205/pineapple/pine-go"

	// 注册 transform_by_lua + recall_static 等 operator(init() 副作用)
	_ "github.com/Liam0205/pineapple/pine-go/operators"
	_ "github.com/Liam0205/pineapple/pine-go/operators/lua"
)

// L2_arithmetic shape:per-item 算术 transform。对应 pineapple
// bench_lua_vs_go_test L2 用例,**boundary-dominated**(每 item 跨界,SetGlobal
// 1 字段 + Call 1 次 + 读返回值 1 次)——wangshu p3 升层后能否真见效的最佳
// 探测形态。
const (
	scriptArith = `function f() return item_price * 0.85 + 10.0 end`
	funcArith   = "f"
)

// makeItems 复刻 pineapple bench L2 itemGen。
func makeItems(n int) []any {
	items := make([]any, n)
	for i := range items {
		items[i] = map[string]any{"item_price": float64(100 + i)}
	}
	return items
}

// buildLuaConfig 复刻 pineapple bench buildLuaConfig:1 recall_static + 1
// transform_by_lua,组成最简 pipeline。DAG / I/O 开销几乎为 0,LuaOp 占比 ≈100%
// —— 避免 pipeline framework 稀释凸月差异。
//
// storageMode 选 row / column,对应 pineapple `storage_mode` 顶层字段。
// 空串等价默认(pineapple 当前默认 row)。
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

// mustBuildEngine:JSON 序列化 config 喂 pine.NewEngine,build 失败 b.Fatal。
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

// runBenchmark 通用 benchmark loop:reuse engine + per-iteration Execute。
//
// itemCount = 1000(对位 pineapple bench L2 的 N=1000 那档,boundary 主导且
// 足够 N 次 Call 触发 wangshu HotEntryThreshold 升层)。
//
// storageMode 选 "row" / "column" / ""(默认):对位 pineapple `storage_mode`
// 顶层字段。
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
