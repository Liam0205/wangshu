//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj3_forloop_e2e_test.go —— PJ3 字节级 FORLOOP inline 真升层
// e2e:`function() for i=1,K do end end`(全常量空 body)经 P4 升层后
// mmap 段内自循环,完整 idx 累加 + ucomisd limit + backward jmp 跑通。
//
// 这是 **PJ3 真接入主路径** 的物理证据(从 PJ2 单 op spec 模板跨进
// PJ3 字节级控制流 inline)——P4 首次在 mmap 段内**字节级跑循环**,
// 不经任何 host helper round-trip。

// TestPJ3_ForLoopEmpty_E2E_FastPath:全常量空 for 循环真升层。
func TestPJ3_ForLoopEmpty_E2E_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 100 do
  end
end
for i = 1, 50 do f() end
return 42`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("rets = %v, want 42(PJ3 FORLOOP inline 不影响 main chunk 返值)", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 模板未真编译——降级 host(prove-the-path 失败)")
	}
	t.Logf("SpecForLoopHits=%d / SpecRegKHits=%d / SpecRegRegHits=%d / SpecChainHits=%d",
		jit.SpecForLoopHits(), jit.SpecRegKHits(), jit.SpecRegRegHits(), jit.SpecChainHits())
}

// TestPJ3_ForLoopEmpty_E2E_SingleIter:`for i=1,1 do end`(单次迭代),
// 验证 FORLOOP idx 累加 + ucomisd 边界正确(idx=1=limit 时 cont).
func TestPJ3_ForLoopEmpty_E2E_SingleIter(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 1 do
  end
end
for i = 1, 50 do f() end
return 1`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1 {
		t.Errorf("rets = %v, want 1", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 单次迭代未真编译")
	}
}

// TestPJ3_ForLoopEmpty_E2E_LongLoop:`for i=1,1000 do end`(千次迭代),
// 测试 backward jmp 跑长循环.
func TestPJ3_ForLoopEmpty_E2E_LongLoop(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 1000 do
  end
end
for i = 1, 50 do f() end
return 1000`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1000 {
		t.Errorf("rets = %v, want 1000", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 千次循环未真编译")
	}
}
