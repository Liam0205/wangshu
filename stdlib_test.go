// stdlib end-to-end tests via public Run path (M12).
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func runOne(t *testing.T, src string) wangshu.Value {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) == 0 {
		return wangshu.Nil()
	}
	return results[0]
}

// TestStdlib_NumericArgCoercion pins two PUC coercion behaviors that
// FuzzOracleDiff caught wangshu being too strict about (#174/#175). PUC's
// luaL_checknumber / luaL_checkstring coerce across the number<->string
// boundary; wangshu's toNumberStr used a bare strconv.ParseFloat (rejecting
// Lua hex integers) and tonumber(x, base) rejected a non-string x outright.
func TestStdlib_NumericArgCoercion(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		// #174: string.rep count arg is a Lua hex-integer STRING "0X0" (=0).
		// luaL_checknumber coerces it; ParseLuaNumber accepts hex where
		// strconv.ParseFloat did not. rep by 0 yields the empty string.
		{"rep-hex-string-count", `return string.rep("ab", "0X0")`, ""},
		{"rep-hex-string-count-nonzero", `return string.rep("a", "0x3")`, "aaa"},
		// A plain decimal string still works (regression guard on the
		// parser swap).
		{"rep-decimal-string-count", `return string.rep("a", "2")`, "aa"},
		// #175: tonumber(number, base) — arg 1 is a NUMBER; luaL_checkstring
		// coerces 0 -> "0", parsed in base 2 -> 0.
		{"tonumber-number-arg-base", `return tonumber(0, "2")`, "0"},
		{"tonumber-number-arg-base16", `return tonumber(255, 16)`, "597"}, // "255" read as base-16 = 597
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runOne(t, tc.src).Display()
			if got != tc.want {
				t.Errorf("%s: %s -> %q, want %q", tc.name, tc.src, got, tc.want)
			}
		})
	}
}

func TestStdlib_Type(t *testing.T) {
	cases := map[string]string{
		`return type(nil)`:     "nil",
		`return type(true)`:    "boolean",
		`return type(1)`:       "number",
		`return type("hi")`:    "string",
		`return type(print)`:   "function",
		`return type({1,2,3})`: "table",
	}
	for src, want := range cases {
		got := runOne(t, src)
		if !got.IsString() || got.Str() != want {
			t.Errorf("%s -> %v, want %q", src, got.Display(), want)
		}
	}
}

func TestStdlib_ToString(t *testing.T) {
	got := runOne(t, `return tostring(123)`)
	if !got.IsString() || got.Str() != "123" {
		t.Errorf("tostring(123) = %v", got.Display())
	}
	got2 := runOne(t, `return tostring(nil)`)
	if !got2.IsString() || got2.Str() != "nil" {
		t.Errorf("tostring(nil) = %v", got2.Display())
	}
}

func TestStdlib_ToNumber(t *testing.T) {
	got := runOne(t, `return tonumber("42")`)
	if !got.IsNumber() || got.Number() != 42 {
		t.Errorf("tonumber('42') = %v", got.Display())
	}
	got2 := runOne(t, `return tonumber("zzz")`)
	if !got2.IsNil() {
		t.Errorf("tonumber('zzz') = %v, want nil", got2.Display())
	}
}

func TestStdlib_MathBasic(t *testing.T) {
	got := runOne(t, `return math.abs(-3) + math.floor(3.7) + math.ceil(2.2)`)
	if !got.IsNumber() || got.Number() != 3+3+3 {
		t.Errorf("got %v, want 9", got.Display())
	}
}

func TestStdlib_MathMaxMin(t *testing.T) {
	got := runOne(t, `return math.max(1,5,3) - math.min(1,5,3)`)
	if !got.IsNumber() || got.Number() != 4 {
		t.Errorf("got %v, want 4", got.Display())
	}
}

func TestStdlib_StringOps(t *testing.T) {
	got := runOne(t, `return string.upper("abc") .. "/" .. string.rep("x", 3)`)
	if !got.IsString() || got.Str() != "ABC/xxx" {
		t.Errorf("got %v, want 'ABC/xxx'", got.Display())
	}
}

func TestStdlib_StringSub(t *testing.T) {
	got := runOne(t, `return string.sub("hello", 2, 4)`)
	if !got.IsString() || got.Str() != "ell" {
		t.Errorf("got %v, want 'ell'", got.Display())
	}
	got2 := runOne(t, `return string.sub("hello", -3)`)
	if !got2.IsString() || got2.Str() != "llo" {
		t.Errorf("got %v, want 'llo'", got2.Display())
	}
}

func TestStdlib_AssertSuccess(t *testing.T) {
	got := runOne(t, `return assert(42, "should not error")`)
	if !got.IsNumber() || got.Number() != 42 {
		t.Errorf("got %v, want 42", got.Display())
	}
}

func TestStdlib_AssertFail(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`assert(false, "boom")`), "fail")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	_, err = prog.Run(st)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestStdlib_StringLowerReverseLen(t *testing.T) {
	got := runOne(t, `return string.lower("AbC") .. string.reverse("xyz") .. tostring(string.len("hello"))`)
	if !got.IsString() || got.Str() != "abczyx5" {
		t.Errorf("got %v, want 'abczyx5'", got.Display())
	}
}

func TestStdlib_SelectVariants(t *testing.T) {
	got := runOne(t, `return select("#", "a", "b")`)
	if !got.IsNumber() || got.Number() != 2 {
		t.Errorf("select('#') = %v, want 2", got.Display())
	}
	got2 := runOne(t, `return select(2, "a", "b", "c")`)
	if !got2.IsString() || got2.Str() != "b" {
		t.Errorf("select(2,...) first = %v, want 'b'", got2.Display())
	}
}

func TestStdlib_RawEqual(t *testing.T) {
	got := runOne(t, `
local t = {}
return tostring(rawequal(t, t)) .. tostring(rawequal({}, {}))`)
	if !got.IsString() || got.Str() != "truefalse" {
		t.Errorf("got %v, want 'truefalse'", got.Display())
	}
}

func TestStdlib_Print(t *testing.T) {
	// print writes to stdout; here we only verify it does not error and returns zero values.
	prog, err := wangshu.Compile([]byte(`print("hello", 42, nil, true)`), "p")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestStdlib_ToStringAllTypes(t *testing.T) {
	got := runOne(t, `return tostring(nil) .. tostring(true) .. tostring(false)`)
	if !got.IsString() || got.Str() != "niltruefalse" {
		t.Errorf("got %v", got.Display())
	}
}

func TestStdlib_MathExtended(t *testing.T) {
	cases := map[string]float64{
		`return math.fmod(7, 3)`:             1,
		`return math.pow(2, 10)`:             1024,
		`return math.atan(0)`:                0,
		`return math.asin(0)`:                0,
		`return math.acos(1)`:                0,
		`return math.deg(math.pi)`:           180,
		`return math.rad(180) - math.pi`:     0,
		`return math.log10(100)`:             2,
		`return select("#", math.modf(3.7))`: 2,
		`return (math.modf(3.7))`:            3,
		`return math.sqrt(16) + math.exp(0)`: 5,
		`return math.sin(0) + math.cos(0)`:   1,
		`return math.tan(0) + math.log(1)`:   0,
	}
	for src, want := range cases {
		got := runOne(t, src)
		if !got.IsNumber() || got.Number() != want {
			t.Errorf("%s = %v, want %v", src, got.Display(), want)
		}
	}
}

func TestStdlib_MathRandomDeterministic(t *testing.T) {
	// after randomseed the sequence is deterministic; in the range forms every value stays within bounds
	got := runOne(t, `
math.randomseed(7)
local a = math.random()
local b = math.random(10)
local c = math.random(5, 8)
return tostring(a >= 0 and a < 1) .. tostring(b >= 1 and b <= 10) .. tostring(c >= 5 and c <= 8)`)
	if got.Str() != "truetruetrue" {
		t.Errorf("got %v", got.Display())
	}
}

func TestStdlib_GsubTableRepl(t *testing.T) {
	got := runOne(t, `return (string.gsub("a b c", "%a", { a = "X", c = "Z" }))`)
	if !got.IsString() || got.Str() != "X b Z" {
		t.Errorf("got %v, want 'X b Z'", got.Display())
	}
}

func TestStdlib_OsDateGetenv(t *testing.T) {
	got := runOne(t, `return #os.date("%Y") == 4`)
	if !got.IsBool() || !got.Bool() {
		t.Errorf("os.date('%%Y') length: got %v", got.Display())
	}
	got2 := runOne(t, `return tostring(os.getenv("__WANGSHU_NOT_SET_ENV__"))`)
	if got2.Str() != "nil" {
		t.Errorf("unset env should be nil, got %v", got2.Display())
	}
	got3 := runOne(t, `return os.clock() >= 0 and os.time() > 0`)
	if !got3.IsBool() || !got3.Bool() {
		t.Errorf("os.clock/time: got %v", got3.Display())
	}
}

func TestStdlib_CoroutineRunningInside(t *testing.T) {
	got := runOne(t, `
local co = coroutine.create(function()
  return coroutine.running() ~= nil
end)
local _, inside = coroutine.resume(co)
return tostring(inside) .. tostring(coroutine.running() == nil)`)
	if got.Str() != "truetrue" {
		t.Errorf("got %v, want 'truetrue'", got.Display())
	}
}

func TestStdlib_StringFormatEdge(t *testing.T) {
	got := runOne(t, `return string.format("%c%c", 65, 66) .. string.format("%5.1f", 3.14)`)
	if !got.IsString() || got.Str() != "AB  3.1" {
		t.Errorf("got %q", got.Str())
	}
}

func TestStdlib_TonumberEdge(t *testing.T) {
	got := runOne(t, `return tostring(tonumber("  42  ")) .. tostring(tonumber(true))`)
	if got.Str() != "42nil" {
		t.Errorf("got %v", got.Display())
	}
}
