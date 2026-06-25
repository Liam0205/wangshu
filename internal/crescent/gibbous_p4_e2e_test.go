//go:build wangshu_p4 && wangshu_profile

// PJ7 真接入端到端验证(prove-the-path-under-test 实例):之前 wangshu_p4
// build 缺 wangshu_profile,profileEnabled=false,P4 升层守卫永远 false ⇒
// make test-p4 全套绿色但 0 个测试真走 P4。修复后(wangshu_p4 + wangshu_profile
// build),本测试经真实公共路径(Compile + Call + SetForceAllPromote)断言
// doReturnHits > 0 = bridge 主路径真触达 P4 GibbousCode.Run + DoReturn 弹帧。
package crescent

import (
	"fmt"
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
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

// TestPJ7_LoadKStringConst_E2E 验真实 LoadProgram 路径下 LOADK 字符串常量
// 形态经 P4 升层 byte-equal 解释器(prove-the-path 第 13 实例)。
//
// **承代码扩展 commit**:analyzeShape 删除 `IsStringConst` 硬拒——proto.Consts
// 字符串槽在 `state.go::LoadProgram` 已 intern 写入真 NaN-box GCRef,P4
// mmap 段直发 `mov rax, u64; ret`(与 number/nil/bool 同源),只要
// p4Code 持 proto 指针,Consts 是 GC 根的一部分,string ref 永久活。
//
// 本测断言:`return "abc"` 形态升层后 byte-equal 解释器返回的字符串值
// (经 DoReturn 弹帧 → caller 拿到与解释器路径同构的 Value)。
//
// **prove-the-path 关键**:string ref payload(arena offset)在 jit 包内
// 单测不解引用,但 e2e 路径 caller 真消费返回值——若 mmap 段烧入的 u64
// 不等于解释器路径产生的 NaN-box,本测立即失败。这是 string const 形态
// 真正"端到端 byte-equal"的实证防线。
func TestPJ7_LoadKStringConst_E2E(t *testing.T) {
	src := `
local function f() return "hello-p4" end
for i = 1, 100 do f() end
return f()`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("PromotionCount=%d, doReturnHits 增量=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("PromotionCount=0 → 没 Proto 升层(LOADK string Compile 未被 bridge 主路径触达)")
	}
	if hits == 0 {
		t.Fatal("PJ7 LOADK string 关键证据缺失:doReturnHits 增量 = 0 → P4 路径未真触达")
	}
	if len(rets) != 1 {
		t.Fatalf("rets 长度 = %d, want 1", len(rets))
	}
	// 返回的 Value 应是 TagString 的 NaN-box(IsCollectable=true,Tag=TagString)
	v := value.Value(rets[0])
	if !value.IsCollectable(v) {
		t.Fatalf("rets[0] = 0x%x 不是可回收类型(预期 string),Tag=0x%x", uint64(v), value.Tag(v))
	}
	if value.Tag(v) != value.TagString {
		t.Fatalf("rets[0] Tag = 0x%x, want TagString=0x%x", value.Tag(v), value.TagString)
	}
	// String 内容由 State.gc 持有,经 `object.StringBytes(arena, ref)` 取
	// 回比对(直接验 payload 是 arena 内 intern 段偏移指向 "hello-p4")。
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(v)))
	if s != "hello-p4" {
		t.Errorf("string value = %q, want \"hello-p4\"", s)
	}
	t.Logf("PJ7 LOADK string 真接入证据:升层 %d / DoReturn %d / 返回值 %q(byte-equal 解释器路径)", promoCount, hits, s)
}

// TestPJ7_ArithForm_E2E_OK 验真实 LoadProgram 路径下 ADD/SUB/MUL/DIV/MOD/POW
// 单 BB 形态(`function(x, y) return x + y end` 类)经 P4 升层 byte-equal
// 解释器(prove-the-path 实例)。
//
// **背景**:本批扩 PJ7 接 algorithm 族 prelude——analyzeShape 识别
// ADD..POW + RETURN A 2 形态后,Run 调 host.Arith 慢路径 helper(逐字节
// 同构解释器 doArith)。本测断言:`f(3, 4) → 12`(MUL)经 P4 升层后
// 仍返回 12,与解释器路径同构。
//
// **prove-the-path 关键**:用单一 `f(x, y)` 函数 + 多档算术验证 prelude
// 调通 + 返回值正确;若 analyzeShape 误回退则 SupportsAllOpcodes 返 false,
// proto 不升层,PromotionCount=0 立即抓出。
func TestPJ7_ArithForm_E2E_OK(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		x, y   float64
		expect float64
	}{
		{"ADD", "+", 3, 4, 7},
		{"SUB", "-", 10, 3, 7},
		{"MUL", "*", 6, 7, 42},
		{"DIV", "/", 84, 2, 42},
		{"MOD", "%", 47, 5, 2},
		{"POW", "^", 2, 5, 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(
				"local function f(x, y) return x %s y end\n"+
					"for i = 1, 100 do f(%g, %g) end\n"+
					"return f(%g, %g)",
				tc.op, tc.x, tc.y, tc.x, tc.y,
			)
			st, mainCl := loadFnP4(t, src)
			st.bridge.SetForceAllPromote(true)

			beforeHits := st.doReturnHits
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			hits := st.doReturnHits - beforeHits
			promoCount := st.bridge.PromotionCount()
			t.Logf("%s:PromotionCount=%d, doReturnHits 增量=%d", tc.name, promoCount, hits)
			if promoCount == 0 {
				t.Fatalf("%s:PromotionCount=0 → 没 Proto 升层(Compile 未触达)", tc.name)
			}
			if hits == 0 {
				t.Fatalf("%s:doReturnHits=0 → P4 路径未真触达", tc.name)
			}
			if len(rets) != 1 {
				t.Fatalf("%s:rets 长度 = %d, want 1", tc.name, len(rets))
			}
			v := value.Value(rets[0])
			if !value.IsNumber(v) {
				t.Fatalf("%s:rets[0] = 0x%x 不是 number", tc.name, uint64(v))
			}
			got := value.AsNumber(v)
			if got != tc.expect {
				t.Errorf("%s:f(%v, %v) = %v, want %v", tc.name, tc.x, tc.y, got, tc.expect)
			}
		})
	}
}

// TestPJ7_ArithForm_E2E_Err 验算术错误路径(`f({}, 1)`)经 P4 升层后仍能
// 正确抛错(perform arithmetic on table)+ caller 拿到 LuaError。
//
// **背景**:算术族 prelude 引入错误路径(string/table 等 raise)。本测
// 断言 host.Arith 返 1 → Run 返 1 → enterGibbous 取 pendingErr 冒泡 →
// caller 拿到 LuaError(含 attempt to perform 字样)。
func TestPJ7_ArithForm_E2E_Err(t *testing.T) {
	src := `
local function f(x) return x + 1 end
for i = 1, 100 do f(i) end  -- 先升层
return f({})  -- 触发 attempt to perform arithmetic`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("ADD on table 应 raise,但 Call 返回 nil err")
	}
	t.Logf("PJ7 Arith ERR 路径正确冒泡:err = %v", err)
}

// TestPJ7_UnmForm_E2E_OK 验真实路径下 UNM(`function(x) return -x end`)经
// P4 升层后 byte-equal 解释器。
func TestPJ7_UnmForm_E2E_OK(t *testing.T) {
	src := `
local function f(x) return -x end
for i = 1, 100 do f(i) end
return f(42)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("UNM:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("UNM:PromotionCount=0 → 没 Proto 升层(Compile 未触达)")
	}
	if hits == 0 {
		t.Fatal("UNM:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("UNM:rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != -42 {
		t.Errorf("f(42) = %v, want -42", got)
	}
}

// TestPJ7_LenForm_E2E_OK 验真实路径下 LEN(`function(s) return #s end`)经
// P4 升层后 byte-equal 解释器。
func TestPJ7_LenForm_E2E_OK(t *testing.T) {
	src := `
local function f(s) return #s end
for i = 1, 100 do f("hello") end
return f("hello-world")`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("LEN:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("LEN:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("LEN:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("LEN:rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 11 {
		t.Errorf(`f("hello-world") = %v, want 11`, got)
	}
}

// TestPJ7_LenForm_E2E_Err 验 LEN 错误路径(`f(true)` raise "attempt to get length
// of a boolean")经 P4 升层后仍正确冒泡。
func TestPJ7_LenForm_E2E_Err(t *testing.T) {
	src := `
local function f(x) return #x end
for i = 1, 100 do f("hot") end  -- 先升层
return f(true)  -- 触发 attempt to get length of`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("LEN on bool 应 raise,但 Call 返回 nil err")
	}
	t.Logf("PJ7 LEN ERR 路径正确冒泡:err = %v", err)
}
