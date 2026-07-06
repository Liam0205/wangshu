//go:build wangshu_p4 && wangshu_profile && amd64 && linux

// e2e_test.go — PJ10 step 3 end-to-end through the wangshu public API.
//
// Validates the full PJ10 wiring: front-end compiles the source, P2
// bridge sees the kernel Proto as compilable (SupportsAllOpcodes hook
// answers true), considerPromotion calls Compile, which falls through
// to peroptranslator.TranslateProto, the resulting PerOpCode lands in
// gibbousCodes, crescent.doCall finds it, p4Code-equivalent Run executes
// the mmap stub + replays imm64s into R(retA+i) + invokes DoReturn. The
// host returns the N values to the outer return, and we read them back
// at the wangshu boundary.
//
// What this proves:
//   - PJ10 hook registration via init() works (peroptranslator import in
//     crescent.arena_p4.go wires it).
//   - The "shape PJ7 cannot do" (N > 1 constant returns) now promotes.
//   - The PJ7 byte-equal contract is unchanged: all existing test/
//     {conformance,difftest,luasuite} pass under the same build tags.
//     (Verified separately in the make test-p4 run; this file only adds
//     the PJ10 acceptance.)

package peroptranslator_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

func runForceAll(t *testing.T, body string) (results []string, promoted int) {
	t.Helper()
	src := "local function k()\n  " + body + "\nend\nlocal a, b, c = k()\nreturn a, b, c"
	prog, err := wangshu.Compile([]byte(src), "pj10e2e")
	if err != nil {
		t.Fatalf("compile %q: %v", body, err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run %q: %v", body, err)
	}
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Display()
	}
	return out, st.PromotionCount()
}

// TestPJ10_MultiReturnPromotes exercises the shape PJ7's analyzeShape
// rejects: `return K1, K2, ...` for N >= 2. Without the PJ10 hook,
// considerPromotion would tier-stuck the kernel Proto. With the hook,
// PromotionCount > 0 and the values come back correctly.
func TestPJ10_MultiReturnPromotes(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"return 42, 43", []string{"42", "43", "nil"}},
		{"return 1, 2, 3", []string{"1", "2", "3"}},
		{"return true, nil, false", []string{"true", "nil", "false"}},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			got, promoted := runForceAll(t, tc.body)
			if promoted == 0 {
				t.Fatalf("PromotionCount = 0; PJ10 hook did not promote the kernel")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d results, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestPJ10_SingleReturnStillWorks confirms that with PJ10 hooked up, the
// single-return shape PJ7 already handled still produces the right value
// (the bridge picks one path or the other — both must give the same
// answer, which is the PJ7 byte-equal contract we never want to break).
func TestPJ10_SingleReturnStillWorks(t *testing.T) {
	got, promoted := runForceAll(t, "return 42")
	if promoted == 0 {
		t.Fatal("PromotionCount = 0; kernel did not promote at all")
	}
	if got[0] != "42" {
		t.Errorf("result[0] = %q, want %q", got[0], "42")
	}
	// extras from `local a, b, c =` are nil.
	if !strings.Contains(strings.Join(got, ","), "nil") {
		t.Errorf("expected trailing nils, got %v", got)
	}
}

// TestPJ10_MoveReturn validates the MOVE head op: `return x, y` for
// kernel(x, y). PJ7 single-return identity `return x` is already
// covered by analyzeShape (RETURN A=0 B=2, retA = the param reg), but
// `return x, y` is a different shape (N>1 with N MOVE head ops) PJ7
// rejects — PJ10 should accept it via the slotKindReg path.
func TestPJ10_MoveReturn(t *testing.T) {
	src := "local function k(x, y)\n  return x, y\nend\nlocal a, b, c = k(10, 20)\nreturn a, b, c"
	prog, err := wangshu.Compile([]byte(src), "pj10move")
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the MOVE kernel")
	}
	want := []string{"10", "20", "nil"}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d", len(res), len(want))
	}
	for i, r := range res {
		if got := r.Display(); got != want[i] {
			t.Errorf("result[%d] = %q, want %q", i, got, want[i])
		}
	}
}

// TestPJ10_MixedMoveAndConst checks the heterogeneous case: head ops
// are a mix of MOVE (slotKindReg) and LOADK (slotKindConst). The
// frontend emits them in slot order; PJ10's AnalyzeShape must accept
// any in-order combination as long as each one targets R(retA + i).
func TestPJ10_MixedMoveAndConst(t *testing.T) {
	src := "local function k(x, y)\n  return x, 1, y, 2\nend\nlocal a, b, c, d = k(10, 20)\nreturn a, b, c, d"
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the mixed kernel")
	}
	want := []string{"10", "1", "20", "2"}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d: %v", len(res), len(want), res)
	}
	for i, r := range res {
		if got := r.Display(); got != want[i] {
			t.Errorf("result[%d] = %q, want %q", i, got, want[i])
		}
	}
}

// TestPJ10_GetUpval covers GETUPVAL: kernel reads from an outer-scope
// upvalue. Frontend emits GETUPVAL head ops with .B = upvalue index.
func TestPJ10_GetUpval(t *testing.T) {
	src := `
local outer1, outer2 = 100, 200
local function k()
  return outer1, outer2
end
local a, b = k()
return a, b
`
	prog, err := wangshu.Compile([]byte(src), "pj10upval")
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the GETUPVAL kernel")
	}
	want := []string{"100", "200"}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d", len(res), len(want))
	}
	for i, r := range res {
		if got := r.Display(); got != want[i] {
			t.Errorf("result[%d] = %q, want %q", i, got, want[i])
		}
	}
}

// TestPJ10_MultiArith covers the PJ10b head-op slotKindArith: kernels
// like `return a + b, a - b` emit two arithmetic ops + one RETURN B=3.
// PJ7's analyzeShape only handles a single arithmetic op + RETURN A 2;
// PJ10 routes each through host.Arith and returns N results.
func TestPJ10_MultiArith(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"return a + b, a - b", []string{"7", "-1"}},
		{"return a * b, a + 1, b - 2", []string{"12", "4", "2"}},
		{"return a + b, a + b", []string{"7", "7"}},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			src := "local function k(a, b)\n  " + tc.body + "\nend\nlocal r1, r2, r3 = k(3, 4)\nreturn r1, r2, r3"
			prog, err := wangshu.Compile([]byte(src), "pj10arith")
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
				t.Fatal("PromotionCount = 0; PJ10 hook did not promote the multi-arith kernel")
			}
			// Compare to tc.want; ignore extras the outer `local` slop adds.
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: got out-of-range, want %q", i, w)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_LoadNilMultiSlot covers LOADNIL A B where B > A — one op
// fills multiple return slots with nil. The frontend emits this for
// `return nil, nil, nil` when the locals span contiguous slots; PJ10a
// expands one LOADNIL into N per-slot sources at AnalyzeShape time.
func TestPJ10_LoadNilMultiSlot(t *testing.T) {
	got, promoted := runForceAll(t, "return nil, nil, nil")
	if promoted == 0 {
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the LOADNIL-multi kernel")
	}
	want := []string{"nil", "nil", "nil"}
	for i, w := range want {
		if i >= len(got) {
			t.Errorf("result[%d]: out-of-range, want %q", i, w)
			continue
		}
		if got[i] != w {
			t.Errorf("result[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestPJ10_LoadNilScratch covers the realistic wangshu-frontend shape:
// `local a, b, c; return a, b, c` — emits a single LOADNIL A=0 B=2 that
// pre-fills scratch slots, then MOVEs them into the RETURN window. The
// LOADNIL becomes a sideEffect (writes scratch regs) and the MOVEs are
// the head ops.
func TestPJ10_LoadNilScratch(t *testing.T) {
	src := `
local function k()
  local a, b, c
  return a, b, c
end
local x, y, z = k()
return x, y, z
`
	prog, err := wangshu.Compile([]byte(src), "pj10nilscratch")
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the LOADNIL-scratch kernel")
	}
	want := []string{"nil", "nil", "nil"}
	for i, w := range want {
		if i >= len(res) {
			t.Errorf("result[%d]: out-of-range, want %q", i, w)
			continue
		}
		if got := res[i].Display(); got != w {
			t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
		}
	}
}

// TestPJ10_SetUpvalSetter covers the "setter" shape PJ7's analyzeShape
// does not handle: a function whose only job is to write an upvalue and
// return nothing. Bytecode looks like
//
//	[0] SETUPVAL A=0 B=0   ; U(0) := R(0) (the lone param)
//	[1] RETURN A=0 B=1     ; no return values
//
// PJ10a accepts this via the sideEffects path: the side-effect op runs
// before the (empty) head-op replay, then DoReturn with B=1 pops the
// frame producing zero return values.
func TestPJ10_SetUpvalSetter(t *testing.T) {
	src := `
local outer = 0
local function set(v) outer = v end
set(99)
return outer
`
	prog, err := wangshu.Compile([]byte(src), "pj10setupval")
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the setter kernel")
	}
	if len(res) < 1 || res[0].Display() != "99" {
		t.Errorf("outer = %v, want 99", res)
	}
}

// TestPJ10_SetUpvalThenReturn covers the mixed shape: side-effect SETUPVAL
// followed by a head-op return. Bytecode looks like
//
//	[0] SETUPVAL A=0 B=0   ; U(0) := R(0) (param v)
//	[1] MOVE A=1 B=0       ; R(1) := R(0)  (or RETURN reads R(0) directly)
//	[2] RETURN ...
//
// The frontend may or may not emit the MOVE depending on register allocation;
// what matters is that the SETUPVAL precedes the RETURN-slot writes. Both
// the upvalue and the return value must come back correct.
func TestPJ10_SetUpvalThenReturn(t *testing.T) {
	src := `
local outer = 0
local function setAndReturn(v)
  outer = v
  return v + 1
end
local r = setAndReturn(10)
return r, outer
`
	prog, err := wangshu.Compile([]byte(src), "pj10setupvalret")
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
		t.Fatal("PromotionCount = 0; PJ10 hook did not promote the side-effect-plus-return kernel")
	}
	want := []string{"11", "10"}
	for i, w := range want {
		if i >= len(res) {
			t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
			continue
		}
		if got := res[i].Display(); got != w {
			t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
		}
	}
}

// TestPJ10_TForLoop covers the generic-for TFORLOOP block:
//
//	for k, v in iter, state, init do <body> end
//
// emits a CALL preamble (often pairs(t)), then JMP +N skipping the
// body, then TFORLOOP A C + JMP -M back-edge. AnalyzeShape recognizes
// this shape and routes body ops into bodyEffects. PerOpCode.runTForLoop
// calls host.TForLoop each iteration until it returns -2 (nil-terminator).
//
// Note: pairs/ipairs are NOT in the bridge's F2-b safe-call whitelist
// (they return coroutine-protocol-coupled iterators), so kernels that
// directly call them stay uncompilable. To exercise TFORLOOP via PJ10
// we provide a hand-written iterator that the bridge can statically
// confirm is yield-safe.
func TestPJ10_TForLoop(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "custom-iter-counter",
			src: `
local function makeIter(limit)
  local i = 0
  return function()
    i = i + 1
    if i > limit then return nil end
    return i, i * 10
  end
end
local function k(limit)
  local s = 0
  local it = makeIter(limit)
  for i, v in it do
    s = s + v
  end
  return s
end
return k(3)
`,
			want: []string{"60"}, // 10 + 20 + 30
		},
		{
			name: "custom-iter-empty",
			src: `
local function emptyIter() return nil end
local function k()
  local s = 0
  for i, v in emptyIter do
    s = s + v
  end
  return s
end
return k()
`,
			want: []string{"0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10tforloop")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the TFORLOOP kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_Close covers CLOSE: at the end of a block whose locals are
// captured by an inner closure, the frontend emits CLOSE A to close
// all open upvalues with stack index >= base+A. Single-BB shape works
// when CLOSE appears in the middle of a linear function body.
func TestPJ10_Close(t *testing.T) {
	src := `
local function k()
  local result
  do
    local x = 7
    local f = function() return x end
    result = f()
  end
  return result
end
return k()
`
	prog, err := wangshu.Compile([]byte(src), "pj10close")
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
		t.Fatal("PromotionCount = 0; PJ10 did not promote the CLOSE kernel")
	}
	if len(res) < 1 || res[0].Display() != "7" {
		t.Errorf("got %v, want [7]", res)
	}
}

// TestPJ10_Closure covers CLOSURE for inner function values:
//
//	local function k() local f = function() return 42 end return f() end
//
// emits CLOSURE A=0 Bx=0 in the outer kernel, followed by MOVE/TAILCALL.
// host.Closure creates the closure value and stores R(A); subsequent
// MOVE prepares the call.
func TestPJ10_Closure(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "no-upvalues-tailcall",
			src: `
local function k()
  local f = function() return 42 end
  return f()
end
return k()
`,
			want: []string{"42"},
		},
		{
			name: "closure-captures-upvalue",
			src: `
local function k(x)
  local f = function() return x end
  return f()
end
return k(99)
`,
			want: []string{"99"},
		},
		{
			name: "closure-returns",
			src: `
local function maker(x)
  return function() return x end
end
local f = maker(7)
return f()
`,
			want: []string{"7"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10closure")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the CLOSURE kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_AndOr covers the short-circuit `and`/`or` 3-op TESTSET
// diamond:
//
//	[pc+0] TESTSET A B C
//	[pc+1] JMP sBx=1
//	[pc+2] MOVE A B' OR LOADK A Bx'
//
// Net effect: A := (Truthy(R(B)) == bool(C)) ? R(B) : <else-arm>.
// C=0 corresponds to `x and y` (truthy of test wins), C=1 to `x or y`.
func TestPJ10_AndOr(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "and-truthy",
			src:  "local function k(x, y) return x and y end\nlocal r = k(true, 42)\nreturn r",
			want: []string{"42"},
		},
		{
			name: "and-falsy",
			src:  "local function k(x, y) return x and y end\nlocal r = k(false, 42)\nreturn r",
			want: []string{"false"},
		},
		{
			name: "or-truthy",
			src:  "local function k(x, y) return x or y end\nlocal r = k(99, nil)\nreturn r",
			want: []string{"99"},
		},
		{
			name: "or-falsy-fallback",
			src:  "local function k(x, y) return x or y end\nlocal r = k(false, 42)\nreturn r",
			want: []string{"42"},
		},
		{
			name: "or-with-const",
			src:  "local function k(x) return x or 0 end\nlocal r = k(nil)\nreturn r",
			want: []string{"0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10andor")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the and/or kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_ForLoop covers the numeric FORPREP/FORLOOP pair. The frontend
// emits `for i = lo, hi do <body> end` as:
//
//	<preamble: LOADK init/limit/step into R(A..A+2)>
//	FORPREP A sBx=fwd
//	<body>
//	FORLOOP A sBx=-fwd
//	<post-loop ops>
//
// AnalyzeShape detects this pair, splits the side-effects list at the
// FORPREP, and routes body ops into bodyEffects. At Run time the loop
// dispatcher iterates: host.ForPrep + step-and-check loop + bodyEffects
// replay each iteration.
func TestPJ10_ForLoop(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "sum-1-to-n",
			src: `
local function k(n)
  local s = 0
  for i = 1, n do
    s = s + i
  end
  return s
end
return k(10)
`,
			want: []string{"55"},
		},
		{
			name: "sum-fixed-range",
			src: `
local function k()
  local s = 0
  for i = 1, 100 do
    s = s + 1
  end
  return s
end
return k()
`,
			want: []string{"100"},
		},
		{
			name: "product-fixed",
			src: `
local function k()
  local p = 1
  for i = 1, 5 do
    p = p * 2
  end
  return p
end
return k()
`,
			want: []string{"32"},
		},
		{
			name: "step-2",
			src: `
local function k(n)
  local s = 0
  for i = 1, n, 2 do
    s = s + 1
  end
  return s
end
return k(10)
`,
			want: []string{"5"},
		},
		{
			name: "empty-loop",
			src: `
local function k()
  local s = 100
  for i = 1, 0 do
    s = s + 1
  end
  return s
end
return k()
`,
			want: []string{"100"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10forloop")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the FORLOOP kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_SetList covers SETLIST for array-literal construction:
// `return {1, 2, 3}` emits NEWTABLE + LOADK×N + SETLIST + RETURN. The
// SETLIST side effect populates the table's array section with the N
// scratch values prepared by the LOADKs.
func TestPJ10_SetList(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "three-elements",
			src: `
local function k() return {1, 2, 3} end
local t = k()
return t[1], t[2], t[3]
`,
			want: []string{"1", "2", "3"},
		},
		{
			name: "five-elements",
			src: `
local function k() return {10, 20, 30, 40, 50} end
local t = k()
return t[1], t[5]
`,
			want: []string{"10", "50"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10setlist")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the SETLIST kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_Self covers SELF for `obj:method(...)` method calls. SELF
// prepares the call window: R(A+1) := R(B) (self) and R(A) := R(B)[RK(C)]
// (the method). The subsequent CALL/TAILCALL then invokes the method.
func TestPJ10_Self(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "method-call-tailcall",
			src: `
local t = {}
function t:greet() return "hi" end
local function k(obj) return obj:greet() end
return k(t)
`,
			want: []string{"hi"},
		},
		{
			name: "method-call-with-arg",
			src: `
local t = {x = 10}
function t:add(n) return self.x + n end
local function k(obj, n) return obj:add(n) end
return k(t, 5)
`,
			want: []string{"15"},
		},
		{
			name: "method-call-statement",
			src: `
local counter = 0
local t = {}
function t:bump() counter = counter + 1 end
local function k(obj) obj:bump() end
k(t)
k(t)
return counter
`,
			want: []string{"2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10self")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the SELF kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_TailCall covers TAILCALL via host.TailCall side effect. The
// frontend emits `function() return f() end` as
//
//	[0] <preludes>
//	[1] TAILCALL A B C=0
//	[2] RETURN A B=0          ; dead (frame already gone if status=0)
//	[3] RETURN A=0 B=1        ; trailing dead
//
// Our analyzer recognizes the RETURN B=0 + preceding TAILCALL pair and
// dispatches via the tri-state host.TailCall helper: status 0 (Lua tail
// call) skips DoReturn, status 2 (host tail call) falls through to
// DoReturn with multret-to-top.
func TestPJ10_TailCall(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "no-args",
			src: `
local function inner() return 42 end
local function k() return inner() end
return k()
`,
			want: []string{"42"},
		},
		{
			name: "one-arg",
			src: `
local function inner(x) return x * 2 end
local function k(x) return inner(x) end
return k(21)
`,
			want: []string{"42"},
		},
		{
			name: "two-args",
			src: `
local function inner(a, b) return a + b end
local function k(a, b) return inner(a, b) end
return k(3, 4)
`,
			want: []string{"7"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10tailcall")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the TAILCALL kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_Call covers single-BB CALL forms via host.CallBaseline.
// The CALL op writes R(A..A+C-2); for any return slot landing in that
// range, a slotKindReg head op reads it back after the call returns.
// Call-as-statement (C=1, no results) just produces a side effect.
func TestPJ10_Call(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "single-arg-single-result",
			src: `
local function double(x) return x * 2 end
local function k(x) local r = double(x) return r end
return k(21)
`,
			want: []string{"42"},
		},
		{
			name: "two-args-single-result",
			src: `
local function add(a, b) return a + b end
local function k(a, b) local r = add(a, b) return r end
return k(3, 4)
`,
			want: []string{"7"},
		},
		{
			name: "no-args-single-result",
			src: `
local function answer() return 42 end
local function k() local r = answer() return r end
return k()
`,
			want: []string{"42"},
		},
		{
			name: "stmt-call-side-effect",
			src: `
local sum = 0
local function add(x) sum = sum + x end
local function k(x) add(x) end
k(7)
k(8)
return sum
`,
			want: []string{"15"},
		},
		{
			name: "k-args-const",
			src: `
local function f(a, b) return a * 10 + b end
local function k() local r = f(2, 3) return r end
return k()
`,
			want: []string{"23"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10call")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the CALL kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_CallIC_PopulatedByExitReason asserts the per-CALL-site
// inline cache (issue #50 Spike 1) is populated by the dispatcher's
// HelperCall path. Silent failure mode: populate skipped entirely, the
// segment guard in Spike 2 always misses, and the test suite stays green
// on the fallback slow path.
//
// Uses a two-CALL kernel — one recursive Lua CALL (mono, same protoID)
// plus one CALL to a separate helper — so the probe catches both the
// warmup (fresh slot → populated) and the stable-mono paths. fib(6) is
// enough to exercise the recursive call ~13 times.
func TestPJ10_CallIC_PopulatedByExitReason(t *testing.T) {
	// Race-detector build skips the exit-reason CALL path (see
	// runShimSegment's race gate); the probe assertion would only be
	// meaningful when the real dispatcher fires.
	// Kernel is arithmetic-dense enough to clear the PJ10 native
	// density gate (totalOps / callCount >= 16). One CALL site is
	// enough for the probe; adding more arith padding around the CALL
	// keeps the callee mono (same protoID every hit).
	prog, err := wangshu.Compile([]byte(`
local function zero() return 0 end
local function kernel(n)
  local s = 0
  for i = 1, n do
    local t = i + 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10
    s = s + t + zero()
  end
  return s
end
return kernel(20)
`), "pj10callic")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	beforePop := peroptranslator.CallICPopulateCount.Load()
	beforeWarm := peroptranslator.CallICWarmedCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// kernel(20) = sum over i=1..20 of (i+55) = (20*21/2) + 20*55 = 1310.
	if len(res) != 1 || res[0].Display() != "1310" {
		t.Fatalf("kernel(20) = %v, want [1310]", res)
	}
	popDelta := peroptranslator.CallICPopulateCount.Load() - beforePop
	warmDelta := peroptranslator.CallICWarmedCount.Load() - beforeWarm
	t.Logf("populate=%d warmed=%d promoted=%d fastHits=%d",
		popDelta, warmDelta, st.PromotionCount(),
		peroptranslator.CallInlineFastHitCount.Load())
	if popDelta == 0 {
		t.Fatal("CallICPopulateCount didn't move — dispatcher.populateCallIC " +
			"never fired; the emit-call-inline warmup path is broken")
	}
	if warmDelta == 0 {
		t.Fatal("CallICWarmedCount didn't move — no fresh slot transitioned " +
			"to populated; either every callee looked host, or ObserveCallCallee " +
			"returned zero across the board")
	}
}

// TestPJ10_CallInline_FastPathFires proves the issue #50 Spike 2
// segment-side EmitCallInline fast path actually fires (guard passes →
// HelperExecutePlainCall) after the IC warms up, and produces the same
// result the interpreter would.
//
// Silent failure mode: the guard could always miss (protoID compare
// off-by-one, Flags mask wrong, IC slot address stale) and every CALL
// would ride the historical HelperCall slow path — the result stays
// correct, so only a white-box fast-hit counter can tell the fast body
// ran. kernel(20) issues one 0-arg CALL per iteration against a stable
// mono Lua callee, so after the first (warmup) call all 19 remaining
// go through the fast body.
func TestPJ10_CallInline_FastPathFires(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local function zero() return 0 end
local function kernel(n)
  local s = 0
  for i = 1, n do
    local t = i + 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10
    s = s + t + zero()
  end
  return s
end
return kernel(20)
`), "pj10callinlinefast")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	before := peroptranslator.CallInlineFastHitCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) != 1 || res[0].Display() != "1310" {
		t.Fatalf("kernel(20) = %v, want [1310]", res)
	}
	fastDelta := peroptranslator.CallInlineFastHitCount.Load() - before
	t.Logf("callInlineFastHits = %d", fastDelta)
	if fastDelta == 0 {
		t.Fatal("CallInlineFastHitCount didn't move — the segment guard " +
			"never passed; EmitCallInline fast body is unreachable (protoID " +
			"compare / Flags mask / IC slot address bug)")
	}
	// Warm-up: the first call rides the slow path (IC empty), so fast
	// hits should be strictly fewer than the 20 total but clearly
	// nonzero (>= 15 of the 19 post-warmup iterations).
	if fastDelta < 15 {
		t.Errorf("callInlineFastHits = %d, want >= 15 (most post-warmup "+
			"iterations should take the fast body)", fastDelta)
	}
}

// TestPJ10_CallInline_NArgFixed proves the Spike 3 relaxation: the
// segment guard accepts N-arg fixed (non-vararg) Lua callees, not just
// 0-arg. The arity guard (cmp dl, nargs) must match the callee's
// NumParams against CALL.B-1. Uses a 2-param callee.
func TestPJ10_CallInline_NArgFixed(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local function addup(a, b) return a + b end
local function kernel(n)
  local s = 0
  for i = 1, n do
    local t = i + 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10
    s = s + addup(t, i)
  end
  return s
end
return kernel(20)
`), "pj10callinlinenarg")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	before := peroptranslator.CallInlineFastHitCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum over i=1..20 of ((i+55) + i) = sum(2i+55) = 2*210 + 20*55 = 1520.
	if len(res) != 1 || res[0].Display() != "1520" {
		t.Fatalf("kernel(20) = %v, want [1520]", res)
	}
	fastDelta := peroptranslator.CallInlineFastHitCount.Load() - before
	t.Logf("callInlineFastHits (2-arg) = %d", fastDelta)
	if fastDelta < 15 {
		t.Errorf("callInlineFastHits = %d, want >= 15 — the arity guard "+
			"rejected the 2-arg fixed callee (Spike 3 relaxation broken)", fastDelta)
	}
}

// TestPJ10_TableOps covers the single-BB table head ops:
//   - GETTABLE: R(A) := R(B)[RK(C)]   via host.GetTable
//   - GETGLOBAL: R(A) := Globals[K(Bx)] via host.DoGetGlobal
//   - NEWTABLE: R(A) := new table     via host.NewTable
//
// And the matching side-effect forms:
//   - SETTABLE: R(A)[RK(B)] := RK(C) via host.SetTable
//   - SETGLOBAL: Globals[K(Bx)] := R(A) via host.DoSetGlobal
//
// `_G.print` and `_G.foo = 1` style globals go through GETGLOBAL/
// SETGLOBAL paths; `t.x` / `t[k]` go through GETTABLE/SETTABLE.
func TestPJ10_TableOps(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "gettable-string-key",
			src:  "local function k(t) return t.x end\nlocal r = k({x = 42})\nreturn r",
			want: []string{"42"},
		},
		{
			name: "gettable-numeric-key",
			src:  "local function k(t) return t[1] end\nlocal r = k({10, 20, 30})\nreturn r",
			want: []string{"10"},
		},
		{
			name: "gettable-multi",
			src:  "local function k(t) return t[1], t[2] end\nlocal a, b = k({99, 88})\nreturn a, b",
			want: []string{"99", "88"},
		},
		{
			name: "newtable-empty",
			src:  "local function k() return {} end\nlocal t = k()\nreturn type(t)",
			want: []string{"table"},
		},
		{
			name: "settable-then-gettable",
			src: `
local function set(t, v) t.x = v end
local t = {x = 0}
set(t, 99)
return t.x
`,
			want: []string{"99"},
		},
		{
			name: "setglobal-getglobal",
			src: `
local function setg() pjtest_global = 123 end
local function getg() return pjtest_global end
setg()
return getg()
`,
			want: []string{"123"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10table")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the table kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_CmpDiamond covers the EQ/LT/LE comparison-as-bool shape.
// The frontend emits a fixed 4-op diamond:
//
//	[pc+0] EQ/LT/LE A B C
//	[pc+1] JMP sBx=1
//	[pc+2] LOADBOOL Adst 0 1
//	[pc+3] LOADBOOL Adst 1 0
//
// for boolean expressions like `return a == b`, `return a < b`. The
// per-op analyzer collapses this diamond into one slotKindCmp head op
// that calls host.Eq / host.Compare and folds the result via the A bit.
func TestPJ10_CmpDiamond(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "eq-true",
			src:  "local function k(a, b) return a == b end\nlocal r = k(7, 7)\nreturn r",
			want: []string{"true"},
		},
		{
			name: "eq-false",
			src:  "local function k(a, b) return a == b end\nlocal r = k(7, 8)\nreturn r",
			want: []string{"false"},
		},
		{
			name: "ne-true",
			src:  "local function k(a, b) return a ~= b end\nlocal r = k(7, 8)\nreturn r",
			want: []string{"true"},
		},
		{
			name: "ne-false",
			src:  "local function k(a, b) return a ~= b end\nlocal r = k(7, 7)\nreturn r",
			want: []string{"false"},
		},
		{
			name: "lt-true",
			src:  "local function k(a, b) return a < b end\nlocal r = k(3, 7)\nreturn r",
			want: []string{"true"},
		},
		{
			name: "lt-false",
			src:  "local function k(a, b) return a < b end\nlocal r = k(7, 3)\nreturn r",
			want: []string{"false"},
		},
		{
			name: "le-true",
			src:  "local function k(a, b) return a <= b end\nlocal r = k(7, 7)\nreturn r",
			want: []string{"true"},
		},
		{
			name: "gt-true",
			src:  "local function k(a, b) return a > b end\nlocal r = k(7, 3)\nreturn r",
			want: []string{"true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10cmp")
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
				t.Fatal("PromotionCount = 0; PJ10 did not promote the diamond kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_Concat covers the CONCAT head op. The frontend emits MOVE
// preambles that copy the operands into a contiguous scratch range,
// then CONCAT A B C reads R(B..C) inclusive and stores the joined
// result in R(A). Both string-string and number-string concat go
// through host.Concat (with __concat / number coercion living there).
func TestPJ10_Concat(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "two-strings",
			src:  "local function k(x, y) return x .. y end\nlocal r = k('foo', 'bar')\nreturn r",
			want: []string{"foobar"},
		},
		{
			name: "three-strings",
			src:  "local function k(x, y, z) return x .. y .. z end\nlocal r = k('a', 'b', 'c')\nreturn r",
			want: []string{"abc"},
		},
		{
			name: "number-string-coerce",
			src:  "local function k(x, y) return x .. y end\nlocal r = k(42, 'x')\nreturn r",
			want: []string{"42x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10concat")
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
				t.Fatal("PromotionCount = 0; PJ10 hook did not promote the CONCAT kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_UnaryHeadOps covers the PJ10a unary head ops: UNM/NOT/LEN.
// Each goes through a different path:
//   - UNM via host.Unm (string coercion + __unm metamethod live in
//     gibbous_host.go::Unm; may raise — but every test here gives a
//     numeric operand, so the fast path runs and no raise occurs).
//   - LEN via host.Len (string byte length / table border / raise on
//     other types).
//   - NOT via pure Go BoolValue(!Truthy(...)) — no host round-trip.
//
// `return -x, not y, #z` is the canonical shape (the V15b promotion
// probe earlier showed PJ7's analyzeShape rejects it at PromotionCount
// = 0); PJ10 should now accept it.
func TestPJ10_UnaryHeadOps(t *testing.T) {
	cases := []struct {
		name string
		// args are concatenated in order; src wraps them in `local
		// function k(x, y, z) ... end` and calls `k(args...)`.
		src  string
		want []string
	}{
		{
			name: "unm-single",
			src:  "local function k(x) return -x end\nlocal a = k(5)\nreturn a",
			want: []string{"-5"},
		},
		{
			name: "not-single",
			src:  "local function k(x) return not x end\nlocal a = k(true)\nreturn a",
			want: []string{"false"},
		},
		{
			name: "len-string",
			src:  "local function k(z) return #z end\nlocal a = k('hello')\nreturn a",
			want: []string{"5"},
		},
		{
			name: "mixed-unary",
			src: `
local function k(x, y, z) return -x, not y, #z end
local a, b, c = k(5, false, 'abc')
return a, b, c
`,
			want: []string{"-5", "true", "3"},
		},
		{
			name: "not-on-nil-param",
			src:  "local function k(x) return not x end\nlocal a = k(nil)\nreturn a",
			want: []string{"true"},
		},
		{
			name: "not-on-truthy-string",
			src:  "local function k(s) return not s end\nlocal a = k('x')\nreturn a",
			want: []string{"false"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10unary")
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
				t.Fatal("PromotionCount = 0; PJ10 hook did not promote the unary kernel")
			}
			for i, w := range tc.want {
				if i >= len(res) {
					t.Errorf("result[%d]: out-of-range, want %q (full: %v)", i, w, res)
					continue
				}
				if got := res[i].Display(); got != w {
					t.Errorf("result[%d] = %q, want %q (full: %v)", i, got, w, res)
				}
			}
		})
	}
}

// TestPJ10_DeferredHeadOrdering is the regression guard for fuzz seed
// 21c645c46a1268c6: PerOpCode.Run replays side effects FIRST and
// materialises deferred head-op sources LAST, so a side effect placed
// after a deferred head in bytecode order used to observe pre-head
// register state (NEWTABLE head + SETLIST effect → "SETLIST: not a
// table"; LOADK head + SETGLOBAL/SETTABLE effect → silent stale
// value). AnalyzeShape now demotes such heads into ordered replay
// effects with a register read-back. Each case asserts the promoted
// result matches the interpreter, and the SETGLOBAL/SETTABLE cases
// assert the observed VALUE (not just error-freeness) so a silent
// stale-read regression fails loudly.
func TestPJ10_DeferredHeadOrdering(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			// NEWTABLE is the demoted head; SETLIST reads R(0) after it.
			name: "setlist-reads-deferred-newtable",
			src: `local function f() return {7} end
local r
for i = 1, 5 do r = f() end
return r[1]`,
			want: "7",
		},
		{
			// LOADK is the demoted head; SETGLOBAL reads R(0) after it.
			name: "setglobal-reads-deferred-loadk",
			src: `local function h() local x = 7; gDeferred = x; return x end
for i = 1, 5 do h() end
return gDeferred`,
			want: "7",
		},
		{
			// LOADK is the demoted head; SETTABLE reads R(0) after it.
			name: "settable-reads-deferred-loadk",
			src: `local t = {}
local function h() local x = 9; t[1] = x; return x end
for i = 1, 5 do h() end
return t[1]`,
			want: "9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "pj10deferred")
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
				t.Fatal("PromotionCount = 0; kernel did not promote")
			}
			if len(res) == 0 {
				t.Fatalf("no results (want %q)", tc.want)
			}
			if got := res[0].Display(); got != tc.want {
				t.Errorf("result = %q, want %q", got, tc.want)
			}
		})
	}
}
