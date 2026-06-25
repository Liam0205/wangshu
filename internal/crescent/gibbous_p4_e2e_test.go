//go:build wangshu_p4 && wangshu_profile

// PJ7 真接入端到端验证(prove-the-path-under-test 第 8 实例修复):承
// `.code-review/from-7846604/from-7846604-to-68f27d2.md` 阻塞问题 1 ——
// 之前 wangshu_p4 build 缺 wangshu_profile,profileEnabled=false,P4 升层
// 守卫永远 false ⇒ make test-p4 全套绿色但 0 个测试真走 P4。
//
// 修复后(wangshu_p4 + wangshu_profile build),本测试经真实公共路径
// (Compile + Call + SetForceAllPromote)断言 doReturnHits > 0 = bridge 主
// 路径真触达 P4 GibbousCode.Run + DoReturn 弹帧。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// loadFnP4 编译 src 为 Program,装载主 chunk,返回 State + 主 closure。
//
// 与 gibbous_e2e_p3_test.go::loadFn 同款形态(后者是 wangshu_p3 build tag,
// 不在 wangshu_p4 build 范围)。
func loadFnP4(t *testing.T, src string) (*State, value.Value) {
	t.Helper()
	lx := lex.New([]byte(src), "p4-e2e")
	block, err := parse.Parse(lx, "p4-e2e")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "p4-e2e")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := New()
	cl := st.LoadProgram(mainID, protos)
	return st, value.MakeGC(value.TagFunction, cl)
}

// TestPJ7_P4PathReallyTriggered 经真实 LoadProgram + Call 路径(force-all)
// 验证 P4 经 bridge 主路径真升层 + p4Code.Run 真被调用 + DoReturn 真弹帧。
//
// **prove-the-path 命中证据**(承
// `llmdoc/guides/prove-the-path-under-test.md` 第 8 实例):本测试经
// SetForceAllPromote(true) 让所有 Compilable Proto 升层,反复调让
// gibbous-jit 路径真被走到。
//
// 关键探针:**doReturnHits 计数**——只有 `enterGibbous` → `p4Code.Run` →
// `host.DoReturn` 路径真走过 doReturnHits 才会 +1。若 P4 路径未触达,
// doReturnHits 永远 = 0(测试失败)。这是阻塞问题 1 的实证修复证据。
func TestPJ7_P4PathReallyTriggered(t *testing.T) {
	src := `
local function f() return 42 end
for i = 1, 100 do f() end
return 0`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = rets

	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("PromotionCount=%d, doReturnHits 增量=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("PromotionCount=0 → 没 Proto 升层(P4 Compile 未被 bridge 主路径触达)")
	}
	if hits == 0 {
		t.Fatal("PJ7 关键证据缺失:doReturnHits 增量 = 0 → P4 路径未真触达。" +
			"main chunk 经 doCall(f) 应触发 enterGibbous → p4Code.Run → host.DoReturn 全链路。")
	}
	t.Logf("PJ7 真接入证据:%d 个 Proto 升层 + %d 次 P4 DoReturn 调用(bridge → enterGibbous → p4Code.Run → host.DoReturn 全链路工作)", promoCount, hits)
}
