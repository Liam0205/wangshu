// stdlib end-to-end tests via public Run path (M12)。
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
		if !got.IsString() || got.String_() != want {
			t.Errorf("%s -> %v, want %q", src, got.GoString(), want)
		}
	}
}

func TestStdlib_ToString(t *testing.T) {
	got := runOne(t, `return tostring(123)`)
	if !got.IsString() || got.String_() != "123" {
		t.Errorf("tostring(123) = %v", got.GoString())
	}
	got2 := runOne(t, `return tostring(nil)`)
	if !got2.IsString() || got2.String_() != "nil" {
		t.Errorf("tostring(nil) = %v", got2.GoString())
	}
}

func TestStdlib_ToNumber(t *testing.T) {
	got := runOne(t, `return tonumber("42")`)
	if !got.IsNumber() || got.Number() != 42 {
		t.Errorf("tonumber('42') = %v", got.GoString())
	}
	got2 := runOne(t, `return tonumber("zzz")`)
	if !got2.IsNil() {
		t.Errorf("tonumber('zzz') = %v, want nil", got2.GoString())
	}
}

func TestStdlib_MathBasic(t *testing.T) {
	got := runOne(t, `return math.abs(-3) + math.floor(3.7) + math.ceil(2.2)`)
	if !got.IsNumber() || got.Number() != 3+3+3 {
		t.Errorf("got %v, want 9", got.GoString())
	}
}

func TestStdlib_MathMaxMin(t *testing.T) {
	got := runOne(t, `return math.max(1,5,3) - math.min(1,5,3)`)
	if !got.IsNumber() || got.Number() != 4 {
		t.Errorf("got %v, want 4", got.GoString())
	}
}

func TestStdlib_StringOps(t *testing.T) {
	got := runOne(t, `return string.upper("abc") .. "/" .. string.rep("x", 3)`)
	if !got.IsString() || got.String_() != "ABC/xxx" {
		t.Errorf("got %v, want 'ABC/xxx'", got.GoString())
	}
}

func TestStdlib_StringSub(t *testing.T) {
	got := runOne(t, `return string.sub("hello", 2, 4)`)
	if !got.IsString() || got.String_() != "ell" {
		t.Errorf("got %v, want 'ell'", got.GoString())
	}
	got2 := runOne(t, `return string.sub("hello", -3)`)
	if !got2.IsString() || got2.String_() != "llo" {
		t.Errorf("got %v, want 'llo'", got2.GoString())
	}
}

func TestStdlib_AssertSuccess(t *testing.T) {
	got := runOne(t, `return assert(42, "should not error")`)
	if !got.IsNumber() || got.Number() != 42 {
		t.Errorf("got %v, want 42", got.GoString())
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
