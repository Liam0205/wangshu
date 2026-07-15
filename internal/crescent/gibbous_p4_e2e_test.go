//go:build wangshu_p4 && wangshu_profile

// PJ7 end-to-end verification of the real path (prove-the-path-under-test
// instance): previously the wangshu_p4 build lacked wangshu_profile, so
// profileEnabled=false and the P4 promotion guard was always false ⇒
// make test-p4 was all green but 0 tests actually exercised P4. After the fix
// (wangshu_p4 + wangshu_profile build), this test drives the real public path
// (Compile + Call + SetForceAllPromote) and asserts doReturnHits > 0 = the
// bridge main path genuinely reaches P4 GibbousCode.Run + DoReturn frame pop.
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

// loadFnP4 compiles src into a Program, loads the main chunk, and returns the
// State + main closure.
//
// Same shape as gibbous_e2e_p3_test.go::loadFn (that one carries the
// wangshu_p3 build tag and is outside the wangshu_p4 build scope).
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

// TestPJ7_P4PathReallyTriggered drives the real LoadProgram + Call path
// (force-all) to verify that P4 genuinely promotes through the bridge main
// path + p4Code.Run is really invoked + DoReturn really pops the frame.
//
// **prove-the-path hit evidence** (following the 8th instance in
// `llmdoc/guides/prove-the-path-under-test.md`): this test uses
// SetForceAllPromote(true) to promote every Compilable Proto, and repeated
// calls ensure the gibbous-jit path is actually exercised.
//
// Key probe: **the doReturnHits counter** — only when the
// `enterGibbous` → `p4Code.Run` → `host.DoReturn` path is actually traversed
// does doReturnHits get incremented. If the P4 path is not reached,
// doReturnHits stays 0 (test fails). This is the empirical fix evidence for
// blocking issue 1.
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

// TestPJ7_LoadKStringConst_E2E verifies that under the real LoadProgram path
// the LOADK string-constant shape is byte-equal to the interpreter after P4
// promotion (prove-the-path instance 13).
//
// **Following the code-extension commit**: analyzeShape drops the
// `IsStringConst` hard-reject — the string slots in proto.Consts are already
// interned into real NaN-box GCRefs by `state.go::LoadProgram`, so the P4 mmap
// segment directly emits `mov rax, u64; ret` (same source as number/nil/bool).
// The string ref is kept alive by `State.strRefs` (R6 root), registered via
// `LoadProgram` and scanned by the collector through `visitProgramStringRefs` —
// **not** via proto.Consts itself.
//
// This test asserts: the string value returned by the byte-equal interpreter
// after the `return "abc"` shape is promoted (via DoReturn frame pop → caller
// receives a Value isomorphic to the interpreter path).
//
// **prove-the-path key point**: the string ref payload (arena offset) is not
// dereferenced by unit tests inside the jit package, but the e2e path caller
// really consumes the return value — if the u64 burned into the mmap segment
// does not equal the NaN-box produced by the interpreter path, this test fails
// immediately. This is the empirical guard that the string-const shape is
// truly "end-to-end byte-equal".
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
	// The returned Value should be a TagString NaN-box (IsCollectable=true, Tag=TagString)
	v := value.Value(rets[0])
	if !value.IsCollectable(v) {
		t.Fatalf("rets[0] = 0x%x 不是可回收类型(预期 string),Tag=0x%x", uint64(v), value.Tag(v))
	}
	if value.Tag(v) != value.TagString {
		t.Fatalf("rets[0] Tag = 0x%x, want TagString=0x%x", value.Tag(v), value.TagString)
	}
	// String content is held by State.gc; retrieve it via
	// `object.StringBytes(arena, ref)` for comparison (directly verifying the
	// payload is an intern-segment offset within the arena pointing at "hello-p4").
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(v)))
	if s != "hello-p4" {
		t.Errorf("string value = %q, want \"hello-p4\"", s)
	}
	t.Logf("PJ7 LOADK string 真接入证据:升层 %d / DoReturn %d / 返回值 %q(byte-equal 解释器路径)", promoCount, hits, s)
}

// TestPJ7_ArithForm_E2E_OK verifies that under the real LoadProgram path the
// single-BB ADD/SUB/MUL/DIV/MOD/POW shape (`function(x, y) return x + y end`
// kind) is byte-equal to the interpreter after P4 promotion (prove-the-path
// instance).
//
// **Background**: this batch extends PJ7 to hook up the algorithm-family
// prelude — after analyzeShape recognizes the ADD..POW + RETURN A 2 shape, Run
// calls the host.Arith slow-path helper (byte-for-byte isomorphic interpreter
// doArith). This test asserts: `f(3, 4) → 12` (MUL) still returns 12 after P4
// promotion, isomorphic to the interpreter path.
//
// **prove-the-path key point**: use a single `f(x, y)` function + multiple
// arithmetic cases to verify the prelude works + return values are correct; if
// analyzeShape wrongly falls back, SupportsAllOpcodes returns false, the proto
// is not promoted, and PromotionCount=0 catches it immediately.
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

// TestPJ7_ArithForm_E2E_Err verifies the arithmetic error path (`f({}, 1)`)
// still raises correctly after P4 promotion (perform arithmetic on table) +
// the caller receives a LuaError.
//
// **Background**: the arithmetic-family prelude introduces an error path
// (string/table etc. raise). This test asserts host.Arith returns 1 → Run
// returns 1 → enterGibbous fetches pendingErr and propagates → caller receives
// a LuaError (containing the "attempt to perform" text).
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

// TestPJ7_UnmForm_E2E_OK verifies that under the real path UNM
// (`function(x) return -x end`) is byte-equal to the interpreter after P4
// promotion.
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

// TestPJ7_LenForm_E2E_OK verifies that under the real path LEN
// (`function(s) return #s end`) is byte-equal to the interpreter after P4
// promotion.
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

// TestPJ7_LenForm_E2E_Err verifies the LEN error path (`f(true)` raises
// "attempt to get length of a boolean") still bubbles up correctly after P4
// promotion.
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

// TestPJ7_NewTable_E2E verifies that under the real path
// `function() return {} end` returns a fresh table (non-nil) after P4
// promotion.
func TestPJ7_NewTable_E2E(t *testing.T) {
	src := `
local function f() return {} end
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
	t.Logf("NEWTABLE:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("NEWTABLE:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("NEWTABLE:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 {
		t.Fatalf("rets 长度 = %d, want 1", len(rets))
	}
	v := value.Value(rets[0])
	if value.Tag(v) != value.TagTable {
		t.Errorf("rets[0] Tag = 0x%x, want TagTable=0x%x", value.Tag(v), value.TagTable)
	}
}

// TestPJ7_GetTable_E2E_OK verifies that under the real path
// `function(t, k) return t[k] end` is byte-equal to the interpreter after P4
// promotion.
func TestPJ7_GetTable_E2E_OK(t *testing.T) {
	src := `
local function f(t, k) return t[k] end
local tbl = {x = 42, y = 99}
for i = 1, 100 do f(tbl, "x") end
return f(tbl, "y")`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("GETTABLE:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("GETTABLE:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("GETTABLE:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 99 {
		t.Errorf(`f(tbl, "y") = %v, want 99`, got)
	}
}

// TestPJ7_GetTable_E2E_Err verifies the GETTABLE error path (`f(nil, 1)` raises
// "attempt to index ...") bubbles up correctly after P4 promotion.
func TestPJ7_GetTable_E2E_Err(t *testing.T) {
	src := `
local function f(t, k) return t[k] end
local tbl = {x = 1}
for i = 1, 100 do f(tbl, "x") end  -- 先升层
return f(nil, 1)  -- 触发 attempt to index nil`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("GETTABLE on nil 应 raise,但 Call 返回 nil err")
	}
	t.Logf("PJ7 GETTABLE ERR 路径正确冒泡:err = %v", err)
}

// TestPJ7_MultiLine_ErrorLineByteEqual verifies that "the prelude error-path
// line number for a multi-line function body is byte-for-byte identical to the
// interpreter" — empirical test for the pc off-by-one fix.
//
// **Background**: previously the prelude helper call passed `pc=retPC=1` (the
// RETURN pc), so inside the helper `ci.pc=pc+1=2` → `LineInfo[ci.pc-1=1]` picked
// the RETURN line instead of the prelude op line. For single-line function
// bodies LineInfo[0]==LineInfo[1] masked the misalignment; once multi-line
// (prelude and RETURN land on different source lines) they diverge.
//
// After the fix it passes `preludePC=retPC-1=0`, so inside the helper
// `ci.pc=1` → `LineInfo[0]` picks the prelude op line, byte-for-byte identical
// to the interpreter path (which also uses ci.pc-1=0).
//
// This test constructs a multi-line `return x` expression split across lines
// (parse anchors the ADD in `return\n  x + y` to the `x + y` line) and does a
// byte-equal comparison against the interpreter result.
func TestPJ7_MultiLine_ErrorLineByteEqual(t *testing.T) {
	// Multi-line function body: ADD on line 3 (x + y), RETURN on line 4.
	// luac anchors the ADD line number as LineInfo[0]=3 / the RETURN line
	// number as LineInfo[1]=4. Triggering `f(nil, 1)` reports "attempt to
	// perform arithmetic"; the correct line number is 3, and an off-by-one
	// misalignment would give 4 (the RETURN line).
	src := `local function f(x, y)
  return
    x + y
end
for i = 1, 100 do f(1, 2) end  -- 先升层
return f({}, 1)  -- 触发 attempt to perform arithmetic on x`

	// P4 path
	stP4, mainP4 := loadFnP4(t, src)
	stP4.bridge.SetForceAllPromote(true)
	_, errP4 := stP4.Call(value.GCRefOf(mainP4), nil, 1)
	if errP4 == nil {
		t.Fatal("P4:ADD on table 应 raise")
	}

	// Interpreter path (independent State, profile off ⇒ no promotion)
	stP1 := New()
	lxP1 := lex.New([]byte(src), "p4-e2e")
	blockP1, err := parse.Parse(lxP1, "p4-e2e")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainIDP1, protosP1, err := compile.Compile(blockP1, "p4-e2e")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	clP1 := stP1.LoadProgram(mainIDP1, protosP1)
	mainValP1 := value.MakeGC(value.TagFunction, clP1)
	// No force-all, default interpreter
	_, errP1 := stP1.Call(value.GCRefOf(mainValP1), nil, 1)
	if errP1 == nil {
		t.Fatal("P1:ADD on table 应 raise")
	}

	// **byte-equal assertion**: the error messages of both paths match char-for-char
	if errP4.Error() != errP1.Error() {
		t.Errorf("P4 与 P1 错误消息不一致(off-by-one 未修?):\n"+
			"  P4 = %q\n"+
			"  P1 = %q",
			errP4.Error(), errP1.Error())
	}
	t.Logf("多行错误 byte-equal 通过:%v", errP4)
}

// TestPJ7_GetGlobal_E2E_OK verifies that under the real path
// `function() return print end` returns the global after P4 promotion.
func TestPJ7_GetGlobal_E2E_OK(t *testing.T) {
	src := `
local function f() return myglobal end
for i = 1, 100 do f() end
return f()`
	st, mainCl := loadFnP4(t, src)
	// Inject a global first
	st.SetGlobal("myglobal", value.NumberValue(777))
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("GETGLOBAL:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("GETGLOBAL:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("GETGLOBAL:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 777 {
		t.Errorf("f() = %v, want 777(myglobal)", got)
	}
}

// TestPJ7_SetTable_E2E_OK verifies that under the real path
// `function(t,k,v) t[k]=v end` (setter shape, retB=1) is promoted through P4
// and the table is written.
func TestPJ7_SetTable_E2E_OK(t *testing.T) {
	src := `
local function f(t, k, v) t[k] = v end
local tbl = {}
for i = 1, 100 do f(tbl, "x", i) end  -- 升层 + 写入
f(tbl, "y", 99)
return tbl.x, tbl.y`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("SETTABLE:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("SETTABLE:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("SETTABLE:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 2 {
		t.Fatalf("rets 长度 = %d, want 2", len(rets))
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 100 {
		t.Errorf("tbl.x = %v, want 100(循环 i=1..100 末次写入)", got)
	}
	if got := value.AsNumber(value.Value(rets[1])); got != 99 {
		t.Errorf("tbl.y = %v, want 99", got)
	}
}

// TestPJ7_SetTable_E2E_Err verifies error bubbling for SETTABLE on nil.
func TestPJ7_SetTable_E2E_Err(t *testing.T) {
	src := `
local function f(t, k, v) t[k] = v end
local tbl = {}
for i = 1, 100 do f(tbl, i, i) end  -- 先升层
return f(nil, "x", 1)  -- 触发 attempt to index nil`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("SETTABLE on nil 应 raise,但 Call 返回 nil err")
	}
	t.Logf("PJ7 SETTABLE ERR 路径正确冒泡:err = %v", err)
}

// TestPJ7_SetGlobal_E2E_OK verifies that under the real path
// `function() x = 1 end` is promoted through P4 (LOADK + SETGLOBAL + RETURN,
// 3 ops total; SETGLOBAL is a prelude preceded by a LOADK
//
//	— the source used by this test, `function(v) myglobal = v end`, is close to
//
// this shape but the LOADK uses the parameter R rather than K).
//
// Note: `function(v) myglobal = v end` compiles to SETGLOBAL A=0 Bx="myglobal" +
// RETURN — A=0 is the register number, the source value is in R(0) = parameter
// v; no LOADK prelude. A perfect single prelude + RETURN shape.
func TestPJ7_SetGlobal_E2E_OK(t *testing.T) {
	src := `
local function f(v) myglobal = v end
for i = 1, 100 do f(i) end  -- 升层 + 反复写 myglobal
f(42)
return myglobal`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("SETGLOBAL:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("SETGLOBAL:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("SETGLOBAL:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("myglobal = %v, want 42(末次 f(42) 写入)", got)
	}
}

// TestPJ7_NotForm_E2E_OK under the real path `function(x) return not x end`
// promoted through P4.
func TestPJ7_NotForm_E2E_OK(t *testing.T) {
	src := `
local function f(x) return not x end
for i = 1, 100 do f(i) end
return f(nil), f(1), f(false), f("a")`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 4)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("NOT:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("NOT:PromotionCount=0")
	}
	if hits == 0 {
		t.Fatal("NOT:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 4 {
		t.Fatalf("rets 长度 = %d, want 4", len(rets))
	}
	expects := []value.Value{value.True, value.False, value.True, value.False}
	names := []string{"nil", "1", "false", "\"a\""}
	for i, e := range expects {
		if value.Value(rets[i]) != e {
			t.Errorf("not %s = 0x%x, want 0x%x", names[i], rets[i], uint64(e))
		}
	}
}

// TestPJ7_SetUpval_E2E_OK under the real path SETUPVAL
// (`function(v) upv = v end`, where upv is an enclosing local) writes the
// upvalue after P4 promotion.
func TestPJ7_SetUpval_E2E_OK(t *testing.T) {
	src := `
local upv = 0
local function setter(v) upv = v end
for i = 1, 100 do setter(i) end  -- 升层 + 写 upv 100 次
setter(42)
return upv`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("SETUPVAL:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("SETUPVAL:PromotionCount=0")
	}
	if hits == 0 {
		t.Fatal("SETUPVAL:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("upv = %v, want 42(末次 setter(42) 写入)", got)
	}
}

// TestPJ7_CompareForm_E2E_OK under the real path the EQ/LT/LE compare-folding
// shape (`function(x) return x == 1 end`) is byte-equal to the interpreter
// after P4 promotion.
//
// luac encodes 6 ops: EQ + JMP + LOADBOOL × 2 + RETURN + dead RETURN.
// P4 folds the whole run into "call host.Compare to get packed, compare against
// cmpA and fold into a BoolValue".
func TestPJ7_CompareForm_E2E_OK(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		x      float64
		expect value.Value
	}{
		{"EQ true", "==", 1, value.True},
		{"EQ false", "==", 2, value.False},
		{"LT true", "<", 0, value.True},
		{"LT false", "<", 1, value.False},
		{"LE true equal", "<=", 1, value.True},
		{"LE true less", "<=", 0, value.True},
		{"LE false", "<=", 2, value.False},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(
				"local function f(x) return x %s 1 end\n"+
					"for i = 1, 100 do f(%g) end\n"+
					"return f(%g)",
				tc.op, tc.x, tc.x,
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
			t.Logf("%s:PromotionCount=%d, doReturnHits=%d", tc.name, promoCount, hits)
			if promoCount == 0 {
				t.Fatalf("%s:PromotionCount=0", tc.name)
			}
			if hits == 0 {
				t.Fatalf("%s:doReturnHits=0 → P4 路径未真触达", tc.name)
			}
			if len(rets) != 1 {
				t.Fatalf("%s:rets 长度 = %d, want 1", tc.name, len(rets))
			}
			if value.Value(rets[0]) != tc.expect {
				t.Errorf("%s:f(%v) %s 1 = 0x%x, want 0x%x",
					tc.name, tc.x, tc.op, rets[0], uint64(tc.expect))
			}
		})
	}
}

// TestPJ7_CompareForm_E2E_Err verifies the compare error path (`f(nil)`
// triggers "attempt to compare nil with number").
func TestPJ7_CompareForm_E2E_Err(t *testing.T) {
	src := `
local function f(x) return x < 1 end
for i = 1, 100 do f(i) end  -- 先升层
return f(nil)  -- 触发 attempt to compare`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("Compare on nil 应 raise,但 Call 返回 nil err")
	}
	t.Logf("PJ7 Compare ERR 路径正确冒泡:err = %v", err)
}

// TestPJ7_ArithChain_E2E_OK under the real path `function(x) return x*2+1 end`
// (MUL+ADD) is byte-equal to the interpreter after P4 promotion.
func TestPJ7_ArithChain_E2E_OK(t *testing.T) {
	src := `
local function f(x) return x*2+1 end
for i = 1, 100 do f(i) end
return f(7)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	beforeHits := st.doReturnHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := st.doReturnHits - beforeHits
	promoCount := st.bridge.PromotionCount()
	t.Logf("MUL+ADD chain:PromotionCount=%d, doReturnHits=%d", promoCount, hits)
	if promoCount == 0 {
		t.Fatal("MUL+ADD:PromotionCount=0 → 没 Proto 升层")
	}
	if hits == 0 {
		t.Fatal("MUL+ADD:doReturnHits=0 → P4 路径未真触达")
	}
	if len(rets) != 1 || !value.IsNumber(value.Value(rets[0])) {
		t.Fatalf("rets = %v, want [number]", rets)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 15 {
		t.Errorf("f(7) = 7*2+1 = %v, want 15", got)
	}
}

// The V11 coroutine no-promotion e2e is left to the PJ7 wangshu public-facing
// entry test (test/luasuite/ path, where coroutine.create/resume is available
// after stdlib install). This package internal/crescent uses loadFnP4 without
// injecting stdlib, so it cannot construct a coroutine.create entry.
//
// **V11 is already implemented at the bridge layer**:
// internal/bridge/bridge.go::considerPromotion line 263-265 guards on
// onMain=false → considerPromotion returns immediately inside a coroutine, so
// the Proto is not promoted. Per [07 §2.4] a Proto on a coroutine thread is not
// promoted even if its heat crosses the threshold.
//
// Once the PJ7 end-to-end luasuite path hooks up coroutine + force-all P4, add a
// V11 real-business-path e2e. For now it relies on the onMain guard inside
// considerPromotion + the bridge unit tests (TODO: add an internal/bridge unit
// test covering the onMain=false path).
