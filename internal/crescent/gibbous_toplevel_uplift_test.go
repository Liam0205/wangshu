//go:build wangshu_p3 && wangshu_profile

// PW10 零跨界顶层升层端到端验收:callOnStack 顶层 cl 已升 gibbous-有-slot 时直接
// 走 enterGibbous(免顶层 enterLuaFrame+execute 解释器主循环 + 内层每条 CALL 都付
// 一次跨界税)。承 R3 反思 prove-the-path 纪律——byte-equal 不够,须实证路径触发。
//
// 探针 = st.doReturnHits + indirectCalls:
//   - 路径触发:顶层 cl 经 enterGibbous + code.Run 跑完,DoReturn 调用次数 ≥1
//     (顶层帧的最终 RETURN 必经 DoReturn,因 caller 是 host trampoline 而非 gibbous,
//     ③b 快路径 G2 必 miss);
//   - byte-equal:返回值与解释器路径一致。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10ZeroCross_TopLevelUplift 验顶层升层路径真触发(force-all 当前规则下 body
// 含 UnknownCall 不升,故构造叶函数 cl 单独升 + 直接 Call)。
func TestPW10ZeroCross_TopLevelUplift(t *testing.T) {
	src := `
local function leaf(x) return x * 2 + 3 end
return leaf`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	leafVal := rets[0]
	args := []value.Value{value.NumberValue(7)}

	// 解释器基线:leaf(7) = 7*2+3 = 17。
	base, e := st.Call(value.GCRefOf(leafVal), args, 1)
	if e != nil {
		t.Fatalf("interp leaf(7): %v", e)
	}
	if value.AsNumber(base[0]) != 17 {
		t.Fatalf("interp leaf(7) = %v, want 17", base[0])
	}

	// 手动升 leaf(force-all 自动路径在叶函数也跑 OnEnter,这里显式确保升层)。
	if !promoteProto(st, leafVal2pid(st, leafVal)) {
		t.Skip("leaf 升层不被支持(F1-F7 排除)")
	}

	// 升后调用:走顶层升层分支 → enterGibbous → code.Run → DoReturn 弹本帧(顶层
	// caller 非 gibbous,③b G2 miss 必走 helperReturn,DoReturn 计数必 +1)。
	beforeHits := st.doReturnHits
	got, e2 := st.Call(value.GCRefOf(leafVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous leaf(7): %v", e2)
	}
	if value.AsNumber(got[0]) != 17 {
		t.Errorf("gibbous leaf(7) = %v, want 17 (byte-equal)", got[0])
	}
	if st.doReturnHits <= beforeHits {
		t.Fatalf("顶层升层路径未触发:DoReturn 增 %d == 0(顶层走解释器 = 顶层升层分支"+
			"未命中 = 优化未生效)", st.doReturnHits-beforeHits)
	}
	t.Logf("顶层升层命中:DoReturn 增 %d(顶层 RETURN 经 helperReturn,证 leaf 走 wasm 入口)",
		st.doReturnHits-beforeHits)
}

func leafVal2pid(st *State, leafVal value.Value) uint32 {
	cl := value.GCRefOf(leafVal)
	return object.ClosureProtoID(st.arena, cl)
}
