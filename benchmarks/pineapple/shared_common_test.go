// shared_common_test.go:对位 common-mode + per-item-mode 的 batch wrapper 假设。
//
// 设计动机:per-item mode 把 host loop 放在 Go 端,1000 items 触发 1000 次
// boundary 跨界。如果改成 batch wrapper 形态(host 灌整列,VM 内循环),
// boundary 从 1000 减到 1 —— pineapple `function_for_common` 模式恰好就是
// 这个形态(只是 user 自己写整列处理 lua_script,而非 adapter 自动 wrap)。
//
// 收益假设(本 spike 验证):
//  1. common mode 比 per-item mode 快 ~25%(boundary 跨界省 999 次)
//  2. column storage 在 common mode 下反败为胜(VM 内整列 SetGlobal 是 column
//     storage 的甜区,backing array 直接 zero-copy 提取)
//  3. wangshu p3 在 common mode 下因 VM 内层有真循环可能触发凸月升层
//
// 结果对 row/column×per-item/common 二维矩阵的实证支撑直接决定是否值得在
// pineapple cross-repo 推动 batch wrapper adapter 优化。
package pineapple_bench

import (
	"context"
	"testing"

	pine "github.com/Liam0205/pineapple/pine-go"
)

// scriptArithCommon 是 scriptArith 的 common-mode 对位:
// 在 VM 内对 item_price 列遍历 + 算术变换,返回结果列。
// 业务逻辑与 per-item scriptArith 等价,只是 host loop 翻进 VM。
//
// 注意 globals 名是 item_price 不是 item_prices——pineapple executeForCommon
// 把 `ItemInput` 字段名直接作 globals,投喂的是该字段的整列 list,所以脚本
// 里的 `item_price` 此时是个 array,需要 [i] 索引。
const (
	scriptArithCommon = `
function f()
  local n = #item_price
  local out = {}
  for i = 1, n do
    out[i] = item_price[i] * 0.85 + 10.0
  end
  return out
end
`
	funcArithCommon = "f"
)

// buildLuaConfigCommon 复刻 buildLuaConfig 但用 function_for_common 模式。
//
// 关键差别:metadata 用 common_output(不是 item_output),因为 common-mode
// 返回值是 common scope 的整列。但下游 pipeline 想用这列作 item-level 数据
// 仍需要 common→item 投影——pineapple 现成机制?
//
// 简化:只测 LuaOp.Execute 本身,不关心下游怎么消费 commonOutput,只看 ns/op
// 与 alloc 二维变化。
func buildLuaConfigCommon(luaScript, luaFunc string, items []any, storageMode string) map[string]any {
	luaOp := map[string]any{
		"type_name":           "transform_by_lua",
		"lua_script":          luaScript,
		"function_for_item":   "",
		"function_for_common": luaFunc,
		"$metadata": map[string]any{
			"item_input":    []string{"item_price"},
			"common_output": []string{"item_result"},
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

// runBenchmarkCommon 对位 runBenchmark 的 common-mode 版本。
func runBenchmarkCommon(b *testing.B, storageMode string) {
	const itemCount = 1000
	items := makeItems(itemCount)
	eng := mustBuildEngine(b, buildLuaConfigCommon(scriptArithCommon, funcArithCommon, items, storageMode))
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
