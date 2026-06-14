//go:build wangshu_p3 && wangshu_profile

// PW2-d 端到端验收(docs/design/p3-wasm-tier/04-trampoline.md §2 + 08 §V):
// crescent doCall 检测到 Proto 已升 gibbous(wazero wasm)时经 trampoline
// 跳 wazero 执行,返回值经共见值栈(arena=linear memory,VS0-c)回填,与
// 解释器执行逐字节一致。
//
// 仅 wangshu_p3 && wangshu_profile build 跑:p3 提供真 gibbous Compiler +
// 收养 wazero memory;profile 启用 considerPromotion 升层路径。
//
// **升层驱动**:compile 期 AnalyzeProto 无 P3 注入恒标 NotCompilable(见
// frontend/compile/analyze_on.go);运行期自动重分析留后续(需 AST 保留)。
// 本测试按 analyze_on.go §对 PB7 影响 所述手工 SetCompilability(模拟「真
// P3 下 F7 放行」)+ 驱动 OnEnter 越阈值触发真升层,测真 trampoline 路径。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// loadFn 编译 src 为 Program,装载主 chunk,返回 State + 主 closure。
func loadFn(t *testing.T, src string) (*State, value.Value) {
	t.Helper()
	lx := lex.New([]byte(src), "pw2d")
	block, err := parse.Parse(lx, "pw2d")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "pw2d")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := New()
	cl := st.LoadProgram(mainID, protos)
	return st, value.MakeGC(value.TagFunction, cl)
}

// promoteProto 手工驱动一个 Proto 走真升层路径(SetCompilability + OnEnter
// 越阈值 → considerPromotion → 真 gibbous Compile + installGibbous)。
// 返回是否成功升层(SupportsAllOpcodes 不支持的形状会 Stuck,返 false)。
func promoteProto(st *State, pid uint32) bool {
	proto := st.protos[pid]
	b := st.bridge
	b.SetCompilability(proto, bridge.CompCompilable, 0)
	for i := uint32(0); i < bridge.HotEntryThreshold+1; i++ {
		b.OnEnter(proto)
	}
	return b.GibbousCodeOf(proto) != nil
}

// TestPW2d_IdentityReturn 端到端:`local function id(x) return x end` 升 gibbous
// 后,id(v) 经 trampoline 跳 wazero 执行,返回值与解释器逐字节一致。
func TestPW2d_IdentityReturn(t *testing.T) {
	src := `
local function id(x) return x end
return id
`
	st, mainCl := loadFn(t, src)
	// 跑主 chunk 拿到 id closure(主 chunk 返回 id)。
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	idVal := rets[0]
	if value.Tag(idVal) != value.TagFunction {
		t.Fatalf("expected function, got %v", idVal)
	}
	idProto := object.ClosureProtoID(st.arena, value.GCRefOf(idVal))

	// 升层前先记解释器结果(byte-equal 基线)。
	want := float64(12345)
	base, e := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e != nil {
		t.Fatalf("interp call: %v", e)
	}
	if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != want {
		t.Fatalf("interp id(%v) = %v, want %v", want, base[0], want)
	}

	// 驱动真升层。
	if !promoteProto(st, idProto) {
		t.Skip("id proto not supported by current gibbous whitelist (SupportsAllOpcodes false)")
	}

	// 升层后调用:经 trampoline 跳 wazero。结果须 byte-equal。
	got, e2 := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e2 != nil {
		t.Fatalf("gibbous call: %v", e2)
	}
	if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != want {
		t.Errorf("gibbous id(%v) = %v, want %v (byte-equal with interp)", want, got[0], want)
	}

	// 多次调用:wazero module 复用稳定(共见值栈每次 base 偏移正确)。
	for _, v := range []float64{0, -1, 3.14, 1e9} {
		r, e := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(v)}, 1)
		if e != nil {
			t.Fatalf("gibbous id(%v): %v", v, e)
		}
		if !value.IsNumber(r[0]) || value.AsNumber(r[0]) != v {
			t.Errorf("gibbous id(%v) = %v, want %v", v, r[0], v)
		}
	}
}

// TestPW2d_ConstReturn `local function k() return 42 end`:LOADK + RETURN
// 升 gibbous 后返回数字常量,byte-equal。
func TestPW2d_ConstReturn(t *testing.T) {
	src := `
local function k() return 42 end
return k
`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	kVal := rets[0]
	kProto := object.ClosureProtoID(st.arena, value.GCRefOf(kVal))

	if !promoteProto(st, kProto) {
		t.Skip("k proto not supported by gibbous whitelist")
	}
	got, e := st.Call(value.GCRefOf(kVal), nil, 1)
	if e != nil {
		t.Fatalf("gibbous k(): %v", e)
	}
	if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 42 {
		t.Errorf("gibbous k() = %v, want 42", got[0])
	}
}

// TestPW2d_PromotionHappened 验证升层真发生(TierGibbous + GibbousCode 装载),
// 否则上面两个测试可能因 Skip 假阳性通过。
func TestPW2d_PromotionHappened(t *testing.T) {
	src := `
local function id(x) return x end
return id
`
	st, mainCl := loadFn(t, src)
	rets, _ := st.Call(value.GCRefOf(mainCl), nil, 1)
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(rets[0]))
	if !promoteProto(st, pid) {
		t.Fatal("id(x) return x 应能升 gibbous(单 BB + RETURN),但 SupportsAllOpcodes 拒了")
	}
	if st.bridge.GibbousCodeOf(st.protos[pid]) == nil {
		t.Fatal("升层后 GibbousCodeOf 应返回非 nil")
	}
}
