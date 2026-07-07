//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_string_loadk_test.go — issue #69: string-constant LOADK accepted on
// the native path (F7-a gate removed). A proto with a live string literal
// (`s = "x"`, `t.name .. "-"`, `obj:method("key")`) used to fall out of
// native wholesale; now emitLOADK bakes the per-State interned
// MakeGC(TagString, ref) bits as a stable imm64 (same as EQ-K #56), so
// such protos promote and run natively while staying byte-equal to the
// interpreter.
//
// These tests exercise the string LOADK result being CONSUMED downstream
// (the LOADK-vs-EQ difference the spike flagged): CONCAT, #s (LEN),
// GETTABLE string key, and — most important for the GC risk — a kernel
// that triggers a collection AFTER the string is baked into a register, to
// prove the baked GCRef is traced (mark, not relocate) and stays valid.
//
// Assertions are arch-neutral (promote + native run + byte-equal), so one
// file serves both arches under their native-runner build tags.

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestPJ10_StringLoadK_Concat: a loop concatenating a string LITERAL with a
// number. Before #69 the `"n="` literal's LOADK sank the whole kernel to
// the interpreter; now it promotes and runs natively (LOADK bakes the
// string imm64, CONCAT rides HelperConcat). Byte-equal to the interpreter.
func TestPJ10_StringLoadK_Concat(t *testing.T) {
	src := `
local function kernel(n)
  local s = ""
  for i = 1, n do
    s = "v=" .. i
  end
  return s, #s
end
local a, b
for j = 1, 5 do a, b = kernel(20) end
return a, b
`
	prog, err := wangshu.Compile([]byte(src), "pj10strconcat")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeRun := peroptranslator.NativeRunCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; string-LOADK+CONCAT kernel did not promote (F7-a still rejecting?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the string-LOADK proto never ran on the native emit path")
	}
	// last i=20: s = "v=" .. 20 = "v=20"; #s = 4.
	if len(res) != 2 || res[0].Display() != "v=20" || res[1].Display() != "4" {
		t.Fatalf("kernel(20) = %v, want [v=20 4]", res)
	}
}

// TestPJ10_StringLoadK_TableKey: a string literal used as a GETTABLE key
// via a local. The `"count"` LOADK bakes an interned imm64; the table read
// uses it. Proves the baked string GCRef is a correct key for hashing.
func TestPJ10_StringLoadK_TableKey(t *testing.T) {
	src := `
local function kernel(n)
  local t = { count = 0 }
  local k = "count"
  local last = 0
  for i = 1, n do
    last = t[k] + i
  end
  return last
end
local r
for j = 1, 5 do r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10strkey")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; string-key kernel did not promote")
	}
	// t.count = 0 always; last = 0 + i, last iter i=20 => 20.
	if len(res) != 1 || res[0].Display() != "20" {
		t.Fatalf("kernel(20) = %v, want [20]", res)
	}
}

// TestPJ10_StringLoadK_GCStress: the GC-safety test. A kernel that bakes a
// string literal into a register, then allocates enough garbage
// (throwaway tables per iteration) to trigger a collection while that
// baked GCRef is live in a register. If the baked imm64 were not traced as
// a root (or if the arena relocated it), this would UAF or corrupt the
// string. Byte-equal survival proves the baked GCRef is correctly traced
// (mark, non-relocating) and separately rooted via st.strRefs.
func TestPJ10_StringLoadK_GCStress(t *testing.T) {
	src := `
local function kernel(n)
  local tag = "leaf"
  local acc = 0
  for i = 1, n do
    local garbage = { i, i, i, i, i, i, i, i }
    if tag == "leaf" then acc = acc + i end
  end
  return acc, tag
end
local a, b
for j = 1, 8 do a, b = kernel(200) end
return a, b
`
	prog, err := wangshu.Compile([]byte(src), "pj10strgc")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; GC-stress string kernel did not promote")
	}
	// tag is always "leaf", so acc = sum 1..200 = 200*201/2 = 20100.
	if len(res) != 2 || res[0].Display() != "20100" || res[1].Display() != "leaf" {
		t.Fatalf("kernel(200) = %v, want [20100 leaf]", res)
	}
}
