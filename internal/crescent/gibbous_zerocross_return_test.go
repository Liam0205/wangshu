//go:build wangshu_p3 && wangshu_profile

// PW10 零跨界 ③b 端到端验收:gibbous→gibbous 定额 RETURN 经 Wasm 内守卫快路径
// 拆帧(免 h_return 跨界),与解释器逐字节一致 + 快路径真命中(非全程 helperReturn
// 回退的假绿)。
//
// 探针 = st.doReturnHits(DoReturn 入口 ++)。快路径命中时**不**经 DoReturn,故对
// gibbous→gibbous 定额返回该计数停滞;断言其增量 < 总 gibbous 返回数 ⟹ 快路径生效。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10ZeroCross_ReturnFastHit:g→f→helper 三级全升。f→helper 是真 gibbous→
// gibbous(f 跑 gibbous,caller=f 是 gibbous、定额 C=2、无开放 upvalue)⟹ helper 的
// RETURN 走 ③b 快路径,不经 DoReturn。验:① byte-equal(141)② 快路径命中(helper
// 返回那次 doReturnHits 不增)。
func TestPW10ZeroCross_ReturnFastHit(t *testing.T) {
	src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
local function g(n) return f(n) + 100 end
return g, f, helper`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 3)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	gVal, fVal, hVal := rets[0], rets[1], rets[2]
	gPid := object.ClosureProtoID(st.arena, value.GCRefOf(gVal))
	fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	args := []value.Value{value.NumberValue(20)}

	// g(20)=f(20)+100=(helper(20)+1)+100=41+100=141。
	beforeHits := st.doReturnHits
	beforeInd := st.indirectCalls
	got, e := st.Call(value.GCRefOf(gVal), args, 1)
	if e != nil {
		t.Fatalf("gibbous g(20): %v", e)
	}
	if value.AsNumber(got[0]) != 141 {
		t.Errorf("g(20) = %v, want 141 (byte-equal)", got[0])
	}
	indHits := st.indirectCalls - beforeInd
	drHits := st.doReturnHits - beforeHits

	// f→helper 经 call_indirect(≥1 次直调)。
	if indHits < 1 {
		t.Fatalf("call_indirect 未命中(indirectCalls 增 %d),③b 前提不成立", indHits)
	}
	// 关键断言:helper 的 RETURN(caller=f 是 gibbous、定额 nresults=nret=1、无开放
	// upvalue)走 ③b 快路径**不经 DoReturn**。本链共 3 次 RETURN 须经「拆帧到 caller」:
	//   - helper→f:caller f 是 gibbous ⟹ ③b 快路径(不计 DoReturn)
	//   - f→g:caller g 跑解释器(顶层 Call 入口)⟹ G2 miss 走 DoReturn(计 1)
	//   - g→trampoline:顶层 ⟹ 解释器 doReturn(非 host DoReturn,不计)
	// 故 ③b 生效 ⟹ drHits==1(仅 f→g);若 ③b 失效(helper 也走 DoReturn)⟹ drHits==2。
	if drHits != 1 {
		t.Fatalf("③b 快路径命中数异常:DoReturn 增 %d,期望 1(helper→f 走快路径、仅 f→g 经 "+
			"DoReturn)。增 2 = helper 也回退 DoReturn(快路径未命中);增 0 = f→g 误入快路径", drHits)
	}
	t.Logf("③b 命中:call_indirect 增 %d,DoReturn 增 %d(helper→f 快路径拆帧,f→g 经 DoReturn)", indHits, drHits)
}
