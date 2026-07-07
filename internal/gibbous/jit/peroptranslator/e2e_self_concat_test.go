//go:build wangshu_p4 && wangshu_profile && amd64 && linux

// e2e_self_concat_test.go — issue #52: SELF + CONCAT admitted to
// opSupported. The whole pipeline (emit -> HelperSelf/HelperConcat
// exit-reason -> host.Self/host.Concat) already existed; the only change
// was letting AnalyzeNative accept protos that contain these ops instead
// of rejecting the whole proto. These tests prove such protos now
// promote to the native path, run there, and stay byte-equal to the
// interpreter (SELF/CONCAT ride the exit-reason dispatch, which is where
// the string alloc / method lookup / metamethod raise happens Go-side).

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestPJ10_Concat_Promotes: a loop body doing concatenation used to keep
// the whole function on the interpreter (CONCAT rejected). It now
// promotes; the native segment exits to HelperConcat per `..` and the
// result matches the interpreter. Concatenating NUMBERS (Lua coerces
// them) avoids the unrelated string-const-LOADK gate, isolating CONCAT.
func TestPJ10_Concat_Promotes(t *testing.T) {
	// kernel loops (multi-BB, promotable) and concatenates numeric parts
	// each step, so the proto mixes FORLOOP (native inline) + arithmetic
	// + CONCAT (exit-reason). No string literal => no F7-a LOADK reject.
	src := `
local function kernel(n)
  local s = 0
  for i = 1, n do
    s = i .. (i + 1)
  end
  return s, #s
end
local a, b
for j = 1, 5 do a, b = kernel(20) end
return a, b
`
	prog, err := wangshu.Compile([]byte(src), "pj10concat")
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
		t.Fatal("PromotionCount = 0; CONCAT kernel did not promote (still rejected?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the promoted proto never ran on the native path")
	}
	// last iter i=20: s = "20" .. "21" = "2021"; #s = 4.
	if len(res) != 2 || res[0].Display() != "2021" || res[1].Display() != "4" {
		t.Fatalf("kernel(20) = %v, want [2021 4]", res)
	}
}

// TestPJ10_Self_Promotes: a loop calling a method via `obj:inc()` used to
// keep the caller on the interpreter (SELF rejected). It now promotes;
// SELF exits to HelperSelf (IC method lookup) per call.
func TestPJ10_Self_Promotes(t *testing.T) {
	src := `
local obj = { total = 0 }
function obj:add(x) self.total = self.total + x end
local function kernel(n)
  for i = 1, n do
    obj:add(i)
  end
  return obj.total
end
local r
for j = 1, 5 do obj.total = 0; r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10self")
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
		t.Fatal("PromotionCount = 0; SELF kernel did not promote (still rejected?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the promoted proto never ran on the native path")
	}
	// sum 1..20 = 210.
	if len(res) != 1 || res[0].Display() != "210" {
		t.Fatalf("kernel(20) = %v, want [210]", res)
	}
}

// TestPJ10_Self_Concat_Mixed: a function mixing method call + numeric
// concat + arithmetic + loop — the "rule script" shape (minus string
// literals) that motivated issue #52. Before, one `:method()` or one
// `..` sank the whole function; now it goes native. String LITERALS
// still hit the separate F7-a LOADK gate (see the note below), so this
// case deliberately concatenates numbers to isolate SELF+CONCAT.
func TestPJ10_Self_Concat_Mixed(t *testing.T) {
	src := `
local row = { n = 0 }
function row:tag(i) return i .. (self.n + i) end
local function transform(n)
  local acc = 0
  for i = 1, n do
    acc = row:tag(i)
    row.n = row.n + i
  end
  return acc, row.n
end
local s, tot
for j = 1, 5 do row.n = 0; s, tot = transform(3) end
return s, tot
`
	prog, err := wangshu.Compile([]byte(src), "pj10mixed")
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
		t.Fatal("PromotionCount = 0; mixed SELF+CONCAT kernel did not promote")
	}
	// row.n after i: 0->0(tag uses n=0,i=1 => "1".."1"="11"); n becomes 1;
	// i=2: tag => "2"..(1+2)="2".."3"="23"; n becomes 3;
	// i=3: tag => "3"..(3+3)="3".."6"="36"; n becomes 6.
	// acc = last tag = "36"; row.n = 6.
	if len(res) != 2 || res[0].Display() != "36" || res[1].Display() != "6" {
		t.Fatalf("transform(3) = %v, want [36 6]", res)
	}
}

// NOTE (issue #52 scope boundary): a CONCAT / SELF proto that ALSO
// contains a live string-literal LOADK (`s = "x"`, `t.name .. "-"`) still
// stays on the interpreter — not because of SELF/CONCAT, but the
// pre-existing F7-a gate (string consts can't be baked as an imm64 in the
// mmap segment). Admitting SELF/CONCAT unblocks the numeric/table-key
// shapes; the string-literal shape needs a separate arena-relative LOADK
// change, tracked outside this issue.
