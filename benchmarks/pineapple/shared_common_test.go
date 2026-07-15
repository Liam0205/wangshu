// shared_common_test.go: tests the batch-wrapper hypothesis for common-mode vs.
// per-item-mode.
//
// Design motivation: per-item mode keeps the host loop on the Go side, so 1000 items
// trigger 1000 boundary crossings. If reshaped into a batch-wrapper form (host feeds
// the whole column, the loop runs inside the VM), the boundary count drops from 1000
// to 1 — pineapple's `function_for_common` mode is exactly this form (except the user
// writes the whole-column processing lua_script themselves, rather than the adapter
// auto-wrapping it).
//
// Benefit hypotheses (validated by this spike):
//  1. common mode is ~25% faster than per-item mode (boundary crossings save 999)
//  2. column storage turns from loser to winner under common mode (a whole-column
//     SetGlobal inside the VM is column storage's sweet spot, extracting the backing
//     array with zero-copy)
//  3. wangshu p3 under common mode may trigger a gibbous tier-up, because the inner
//     VM layer has a real loop
//
// The empirical support this gives to the row/column × per-item/common 2D matrix
// directly decides whether it's worth driving a batch-wrapper adapter optimization
// across the pineapple cross-repo.
package pineapple_bench

import (
	"context"
	"testing"

	pine "github.com/Liam0205/pineapple/pine-go"
)

// scriptArithCommon is the common-mode counterpart of scriptArith:
// inside the VM it iterates over the item_price column + applies an arithmetic
// transform, returning the result column. The business logic is equivalent to the
// per-item scriptArith, only the host loop is moved into the VM.
//
// Note the globals name is item_price, not item_prices — pineapple's
// executeForCommon uses the `ItemInput` field name directly as the globals name, and
// feeds the whole-column list of that field, so `item_price` in the script is an
// array here and needs [i] indexing.
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

// buildLuaConfigCommon replicates buildLuaConfig but uses the function_for_common mode.
//
// Key difference: metadata uses common_output (not item_output), because the
// common-mode return value is the whole column in common scope. But if a downstream
// pipeline wants to use this column as item-level data, it still needs a common→item
// projection — is there an existing pineapple mechanism for that?
//
// Simplification: only benchmark LuaOp.Execute itself; we don't care how downstream
// consumes commonOutput, only the 2D changes in ns/op and alloc.
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

// runBenchmarkCommon is the common-mode counterpart of runBenchmark.
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
