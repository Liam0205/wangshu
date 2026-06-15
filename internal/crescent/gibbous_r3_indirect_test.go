//go:build wangshu_p3 && wangshu_profile

// PW10 R3 端到端验收:gibbous→gibbous CALL 经 call_indirect 跨 module 直调
// (免 h_call 双跨层 code.Run 重入),三向回退保留,错误每层补弹帧。
//
// 验三件事(对齐 spike DECISION.md §3 R3 定义):
//   - 直调真走到:promoteProto 双升 f+helper 后,indirectCalls 计数 > 0(非静默
//     回退 code.Run)。
//   - byte-equal:gibbous→gibbous 结果 + 错误消息与解释器逐字节一致(层间差分)。
//   - base 刷新:被调深递归 growStack 段重定位后,caller 经中转字读刷新后 base 续算。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10R3_IndirectHappyPath:g→f→helper 三级,全升 gibbous。顶层 g 经
// st.Call 由解释器入口跑(crescent→gibbous 进 g),g 调 f、f 调 helper 都是
// gibbous→gibbous 经 call_indirect 直达。结果 byte-equal + 直调命中计数 > 0。
//
// **为何三级**:st.Call 顶层闭包走解释器 enterLuaFrame(非 enterGibbous),故被
// 直接 Call 的函数跑 crescent;要 gibbous→gibbous 须让 gibbous 帧内发 CALL——即
// g 升 gibbous 后(经 crescent→gibbous 入口)在其体内调同样升层的 f。
func TestPW10R3_IndirectHappyPath(t *testing.T) {
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
	args := []value.Value{value.NumberValue(20)}

	// 解释器基线(升层前)。g(20) = f(20)+100 = (helper(20)+1)+100 = 41+100 = 141。
	base, e := st.Call(value.GCRefOf(gVal), args, 1)
	if e != nil {
		t.Fatalf("interp g(20): %v", e)
	}
	if value.AsNumber(base[0]) != 141 {
		t.Fatalf("interp g(20) = %v, want 141", base[0])
	}

	// 三升层 → g→f、f→helper 都经 call_indirect。
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	before := st.indirectCalls
	got, e2 := st.Call(value.GCRefOf(gVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous→gibbous g(20): %v", e2)
	}
	if value.AsNumber(got[0]) != 141 {
		t.Errorf("gibbous→gibbous g(20) = %v, want 141 (byte-equal)", got[0])
	}
	// g→f→helper:顶层 g 经解释器入口跑(crescent→gibbous 进 g 帧),g 体内调 f =
	// 第一条 gibbous→gibbous(但 g 跑解释器,故 g→f 仍是 crescent→gibbous 不计数);
	// f 跑 gibbous,f 体内调 helper = 真 gibbous→gibbous 经 call_indirect(计 1 次)。
	// 即 N 级链从顶层 Call 入口跑,只有最内 N-2 条边是 gibbous→gibbous(顶 crescent→
	// gibbous 进次内层,次内层起才在 gibbous 帧内发 CALL)。g→f→helper ⟹ 命中 1。
	if st.indirectCalls < before+1 {
		t.Fatalf("call_indirect 直调未命中(indirectCalls %d→%d,期望 ≥+1):疑似静默回退 code.Run，"+
			"R3 收益未兑现", before, st.indirectCalls)
	}
}

// TestPW10R3_IndirectErrorByteEqual:错误穿越 gibbous→gibbous(helper 对 nil
// 算术报错)冒泡,消息 + traceback 与**纯解释器** byte-equal(R3c-fix 在出错点
// 锚定行号 + 物化 traceback,使后续弹帧不影响错误位置 → gibbous 追平解释器)。
//
// R3c-fix 前此测因弹帧后 currentCI 偏移而行号漂移(已知回归);修复后 gibbous
// 错误位置/traceback 与解释器逐字节一致(优于 PW6c 既有 crescent→gibbous 基线的
// 截断 traceback)。oracle = 纯解释器(同入口 g(nil),全不升层)。
func TestPW10R3_IndirectErrorByteEqual(t *testing.T) {
	src := `
local function helper(x) return x + 1 end   -- helper(nil) → 对 nil 算术报错
local function f(a) local r = helper(a); return r end       -- 非尾 CALL(经 h_call/DoCall)
local function g(n) local r = f(n); return r + 0 end         -- 非尾 CALL
return g, f, helper`
	loadF := func() (*State, value.Value, value.Value, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 3)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0], rets[1], rets[2]
	}
	badArg := []value.Value{value.Nil}

	// oracle:纯解释器 g(nil)(全不升层),消息 + traceback 是 byte-equal 基准。
	stO, gO, _, _ := loadF()
	_, eO := stO.Call(value.GCRefOf(gO), badArg, 1)
	if eO == nil {
		t.Fatal("interp g(nil) 应报错(对 nil 算术)")
	}
	wantMsg := eO.Error()

	// R3:三升 g+f+helper,f→helper 经 call_indirect status 链 + PopErrFrame 补弹冒泡;
	// 错误经 raiseGibbous 在出错帧锚定行号 → 与解释器 byte-equal。
	st, gVal, fVal, hVal := loadF()
	gPid := object.ClosureProtoID(st.arena, value.GCRefOf(gVal))
	fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	before := st.indirectCalls
	_, eG := st.Call(value.GCRefOf(gVal), badArg, 1)
	if eG == nil {
		t.Fatal("gibbous→gibbous g(nil) 应报错(错误经 call_indirect status 链冒泡)")
	}
	if st.indirectCalls <= before {
		t.Fatalf("错误路径 call_indirect 未命中(疑似回退)")
	}
	if eG.Error() != wantMsg {
		t.Errorf("gibbous→gibbous 错误消息 = %q, want %q (与纯解释器 byte-equal)", eG.Error(), wantMsg)
	}
}

// TestPW10R3_IndirectBaseRefresh:被调 helper 深递归撑爆初始栈触发 growStack 段
// 重定位,caller f 经中转字读刷新后 base 续算寻址(免陈旧 base UAF)。g→f→helper
// 三级保证 f 跑 gibbous(f→helper 是 gibbous→gibbous,返回后 f 须刷新 base)。
func TestPW10R3_IndirectBaseRefresh(t *testing.T) {
	src := `
local function helper(n)
  if n <= 0 then return 0 end
  local sub = helper(n - 1)     -- 非尾 CALL:每层留活帧 → 深栈撑爆触发 growStack
  return sub + n
end
local function f(n)
  local marker = 777            -- 占本帧寄存器,helper 返回后须能读对(验 base 刷新)
  local s = helper(n)
  return s + marker
end
local function g(n) local r = f(n); return r end   -- 非尾 CALL 进 f
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

	// 先用浅深度(n=5)取解释器基线 + 升层(避免深跑触发 helper 自动升层尝试失败
	// 误标 TierStuck —— 深递归 self-CALL 形态的升层在本期偶失败,与 R3 无关)。
	shallow := []value.Value{value.NumberValue(5)}
	base, e := st.Call(value.GCRefOf(gVal), shallow, 1)
	if e != nil {
		t.Fatalf("interp g(5): %v", e)
	}
	wantShallow := value.AsNumber(base[0]) // = sum(1..5)+777 = 792

	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	// 升层后浅深度先验证直调命中 + byte-equal。
	before := st.indirectCalls
	gotShallow, e1 := st.Call(value.GCRefOf(gVal), shallow, 1)
	if e1 != nil {
		t.Fatalf("gibbous→gibbous g(5): %v", e1)
	}
	if st.indirectCalls <= before {
		t.Fatalf("call_indirect 未命中(疑似回退)")
	}
	if value.AsNumber(gotShallow[0]) != wantShallow {
		t.Fatalf("gibbous→gibbous g(5) = %v, want %v", gotShallow[0], wantShallow)
	}
	// 深深度(n=2000)驱动 helper 深递归撑爆初始栈 → growStack 段重定位;f 经中转字
	// 读刷新后 base 续算读 marker(777)。gibbous 结果须与解释器深度基线一致。
	deep := []value.Value{value.NumberValue(2000)}
	gotDeep, e2 := st.Call(value.GCRefOf(gVal), deep, 1)
	if e2 != nil {
		t.Fatalf("gibbous→gibbous g(2000) 深递归: %v(base 刷新失败?)", e2)
	}
	// 解释器深度基线:用新 State 重算(本 State 已全升层,不能再取解释器结果)。
	wantDeep := 2000.0*2001.0/2.0 + 777.0 // sum(1..2000)+marker
	if value.AsNumber(gotDeep[0]) != wantDeep {
		t.Errorf("gibbous→gibbous g(2000) = %v, want %v(base 刷新后读 marker 错 = UAF)",
			gotDeep[0], wantDeep)
	}
}
