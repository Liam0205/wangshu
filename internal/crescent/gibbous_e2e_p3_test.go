//go:build wangshu_p3 && wangshu_profile

// PW2-d end-to-end acceptance (docs/design/p3-wasm-tier/04-trampoline.md §2 + 08 §V):
// when crescent doCall detects a Proto has been promoted to gibbous (wazero wasm),
// it goes through the trampoline into wazero to execute; the return value is written
// back via the shared value stack (arena=linear memory, VS0-c), byte-for-byte
// identical to interpreter execution.
//
// Only runs under wangshu_p3 && wangshu_profile builds: p3 provides the real gibbous
// Compiler + adopts wazero memory; profile enables the considerPromotion path.
//
// **Promotion driver**: at compile time AnalyzeProto without P3 injection always marks
// NotCompilable (see frontend/compile/analyze_on.go); runtime auto re-analysis is left
// for later (requires AST retention). This test manually SetCompilability as described in
// analyze_on.go §impact on PB7 (simulating "F7 allowed under real P3") + drives OnEnter
// past the threshold to trigger real promotion, exercising the real trampoline path.
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

// loadFn compiles src into a Program, loads the main chunk, and returns State + main closure.
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

// promoteProto manually drives a Proto through the real promotion path (SetCompilability +
// OnEnter past the threshold → considerPromotion → real gibbous Compile + installGibbous).
// Returns whether promotion succeeded (shapes not supported by SupportsAllOpcodes get Stuck,
// returning false).
//
// **forceAll bypasses the MinPromotableCodeLen guard** (issue #21): hot protos used in e2e
// tests are generally short (`return x`/`return a+1` style, 1-2 opcodes) and get blocked by
// the guard on the natural hotness path. promoteProto, as a testing helper, explicitly calls
// SetForceAllPromote to bypass the guard, matching forceAll's "test entry point may override
// perf optimizations" semantics.
func promoteProto(st *State, pid uint32) bool {
	proto := st.protos[pid]
	b := st.bridge
	b.SetCompilability(proto, bridge.CompCompilable, 0)
	b.SetForceAllPromote(true)
	defer b.SetForceAllPromote(false)
	for i := uint32(0); i < bridge.HotEntryThreshold+1; i++ {
		b.OnEnter(proto, true)
	}
	return b.GibbousCodeOf(proto) != nil
}

// TestPW2d_IdentityReturn end-to-end: after `local function id(x) return x end` is
// promoted to gibbous, id(v) goes through the trampoline into wazero; the return value
// is byte-for-byte identical to the interpreter.
func TestPW2d_IdentityReturn(t *testing.T) {
	src := `
local function id(x) return x end
return id
`
	st, mainCl := loadFn(t, src)
	// Run the main chunk to obtain the id closure (main chunk returns id).
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	idVal := rets[0]
	if value.Tag(idVal) != value.TagFunction {
		t.Fatalf("expected function, got %v", idVal)
	}
	idProto := object.ClosureProtoID(st.arena, value.GCRefOf(idVal))

	// Record the interpreter result before promotion (byte-equal baseline).
	want := float64(12345)
	base, e := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e != nil {
		t.Fatalf("interp call: %v", e)
	}
	if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != want {
		t.Fatalf("interp id(%v) = %v, want %v", want, base[0], want)
	}

	// Drive the real promotion.
	if !promoteProto(st, idProto) {
		t.Skip("id proto not supported by current gibbous whitelist (SupportsAllOpcodes false)")
	}

	// Post-promotion call: goes through the trampoline into wazero. Result must be byte-equal.
	got, e2 := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e2 != nil {
		t.Fatalf("gibbous call: %v", e2)
	}
	if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != want {
		t.Errorf("gibbous id(%v) = %v, want %v (byte-equal with interp)", want, got[0], want)
	}

	// Repeated calls: wazero module reuse is stable (the shared value stack computes the
	// correct base offset each time).
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

// TestPW2d_ConstReturn `local function k() return 42 end`: LOADK + RETURN; after
// promotion to gibbous, returns a numeric constant, byte-equal.
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

// TestPW2d_PromotionHappened verifies promotion actually happened (TierGibbous +
// GibbousCode loaded); otherwise the two tests above might pass as false positives via Skip.
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

// TestPW3_ArithE2E `local function f(a,b) return a+b end`: the ADD double-number
// fast path, after promotion to gibbous, jumps to wazero via the trampoline;
// the result is byte-for-byte identical to the interpreter.
func TestPW3_ArithE2E(t *testing.T) {
	cases := []struct {
		name string
		src  string
		args []value.Value
		want float64
	}{
		{"add", `local function f(a,b) return a+b end; return f`,
			[]value.Value{value.NumberValue(3), value.NumberValue(4)}, 7},
		{"sub", `local function f(a,b) return a-b end; return f`,
			[]value.Value{value.NumberValue(10), value.NumberValue(3)}, 7},
		{"muladd", `local function f(a,b) return a*b end; return f`,
			[]value.Value{value.NumberValue(6), value.NumberValue(7)}, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

			// Interpreter baseline.
			base, e := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e != nil {
				t.Fatalf("interp call: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp f = %v, want %v", base[0], tc.want)
			}

			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported by gibbous whitelist", tc.name)
			}
			// After promotion: jumps to wazero via the trampoline, byte-equal.
			got, e2 := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e2 != nil {
				t.Fatalf("gibbous call: %v", e2)
			}
			if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous f = %v, want %v (byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW3_ArithSlowPathE2E: a mixed-type case (string coercion) goes through
// the slow-path helper h_arith, byte-for-byte identical to the interpreter's
// doArithSlow.
func TestPW3_ArithSlowPathE2E(t *testing.T) {
	// "10" + 5: string coercion → 15 (Lua 5.1 arithmetic coercion)
	src := `local function f(a,b) return a+b end; return f`
	st, mainCl := loadFn(t, src)
	rets, _ := st.Call(value.GCRefOf(mainCl), nil, 1)
	fVal := rets[0]
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("proto not supported")
	}
	// "10" is a string: the gibbous ADD fast path fails IsNumber → h_arith slow-path coercion.
	strV := value.MakeGC(value.TagString, st.gc.Intern([]byte("10")))
	got, e := st.Call(value.GCRefOf(fVal), []value.Value{strV, value.NumberValue(5)}, 1)
	if e != nil {
		t.Fatalf("gibbous slow-path call: %v", e)
	}
	if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 15 {
		t.Errorf(`gibbous f("10",5) = %v, want 15 (string coercion via h_arith)`, got[0])
	}
}

// TestPW5a_GlobalIC PW5-a: GETGLOBAL/SETGLOBAL inline IC snapshot freezing.
// Before promotion the interpreter baseline is run to fill the IC (NodeHit) →
// after promotion the inline fast path (same gen reaches the node slot directly,
// skipping the hash) → byte-equal. Invalidation path: adding a new global
// triggers globals rehash → gen bump → inline gen check fails → falling back to
// the h_getglobal/h_setglobal helper is still correct.
func TestPW5a_GlobalIC(t *testing.T) {
	t.Run("getglobal-hit", func(t *testing.T) {
		src := `local function f() return gx end; return f`
		st, mainCl := loadFn(t, src)
		st.SetGlobal("gx", value.NumberValue(99))
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

		// Interpreter baseline (also fills GETGLOBAL's IC slot to NodeHit).
		base, e := st.Call(value.GCRefOf(fVal), nil, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != 99 {
			t.Fatalf("interp f() = %v, want 99", base[0])
		}

		if !promoteProto(st, pid) {
			t.Skip("f proto not supported by gibbous whitelist")
		}
		// inline IC fast path: same gen reaches the node slot directly.
		got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 99 {
			t.Errorf("gibbous f() = %v, want 99 (inline IC hit)", got[0])
		}

		// Change an existing global's value (changing the value does not bump gen, the IC keeps hitting).
		st.SetGlobal("gx", value.NumberValue(7))
		got2, _ := st.Call(value.GCRefOf(fVal), nil, 1)
		if !value.IsNumber(got2[0]) || value.AsNumber(got2[0]) != 7 {
			t.Errorf("gibbous f() after value change = %v, want 7", got2[0])
		}

		// Invalidation path: adding many globals triggers rehash → gen bump → inline check fails → helper is still correct.
		for i := 0; i < 32; i++ {
			st.SetGlobal("pad"+string(rune('a'+i)), value.NumberValue(float64(i)))
		}
		got3, e3 := st.Call(value.GCRefOf(fVal), nil, 1)
		if e3 != nil {
			t.Fatalf("gibbous after rehash: %v", e3)
		}
		if !value.IsNumber(got3[0]) || value.AsNumber(got3[0]) != 7 {
			t.Errorf("gibbous f() after rehash = %v, want 7 (helper fallback)", got3[0])
		}
	})

	t.Run("setglobal-hit", func(t *testing.T) {
		src := `local function f(v) gy = v; return gy end; return f`
		st, mainCl := loadFn(t, src)
		st.SetGlobal("gy", value.NumberValue(0)) // pre-store the key (the SETGLOBAL value-change fast path requires the key to exist)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

		base, e := st.Call(value.GCRefOf(fVal), []value.Value{value.NumberValue(11)}, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != 11 {
			t.Fatalf("interp f(11) = %v, want 11", base[0])
		}

		if !promoteProto(st, pid) {
			t.Skip("f proto not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), []value.Value{value.NumberValue(22)}, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 22 {
			t.Errorf("gibbous f(22) = %v, want 22 (inline SETGLOBAL+GETGLOBAL hit)", got[0])
		}
	})
}

// TestPW5b_TableIC PW5-b: GETTABLE/SETTABLE inline IC (key match).
// const-key NodeHit (t.x) / register-key ArrayHit (t[1]) hits the inline path,
// skipping the hash; before promotion the interpreter baseline is run to fill
// the IC + byte-equal differential test.
func TestPW5b_TableIC(t *testing.T) {
	run := func(t *testing.T, src string, setup func(*State) []value.Value, want float64) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		args := setup(st)
		base, e := st.Call(value.GCRefOf(fVal), args, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != want {
			t.Fatalf("interp = %v, want %v", base[0], want)
		}
		if !promoteProto(st, pid) {
			t.Skip("proto not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != want {
			t.Errorf("gibbous = %v, want %v (byte-equal)", got[0], want)
		}
	}

	t.Run("gettable-field", func(t *testing.T) {
		run(t, `local function f(t) return t.x end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(map[string]float64{"x": 42}, nil)
			return []value.Value{tv}
		}, 42)
	})
	t.Run("gettable-array", func(t *testing.T) {
		run(t, `local function f(t) return t[1] end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(nil, []float64{7, 8, 9})
			return []value.Value{tv}
		}, 7)
	})
	t.Run("settable-field", func(t *testing.T) {
		run(t, `local function f(t) t.x = 5; return t.x end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(map[string]float64{"x": 0}, nil)
			return []value.Value{tv}
		}, 5)
	})
}

// TestPW5d_NewTableSetList PW5-d: NEWTABLE/SETLIST via helper (allocate + bulk
// write + GC inside the helper). Construct the table {10,20,30} then fetch an
// element; promote to gibbous byte-equal.
func TestPW5d_NewTableSetList(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want float64
	}{
		// NEWTABLE + LOADK×3 (numbers) + SETLIST (B=3,C=1) + GETTABLE t[2]
		{"array-ctor", `local function f() local t={10,20,30} return t[2] end; return f`, 20},
		// array sum (NEWTABLE + SETLIST + for iteration)
		{"array-sum", `local function f() local t={1,2,3,4} local s=0 for i=1,4 do s=s+t[i] end return s end; return f`, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			st.SetGCStressMode(true) // allocation-heavy: freelist reuse exposes missed roots / residual values
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			base, e := st.Call(value.GCRefOf(fVal), nil, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (byte-equal)", got[0], tc.want)
			}
		})
	}
}

// newTableArg constructs a test table (string→number fields + an array
// segment) and returns its value.
func (st *State) newTableArg(fields map[string]float64, arr []float64) value.Value {
	asz := uint32(len(arr))
	t := st.allocTable(asz, roundUpPow2(uint32(len(fields))))
	for i, v := range arr {
		st.tableSetInt(t, uint32(i+1), value.NumberValue(v))
	}
	for k, v := range fields {
		st.SetTableField(t, k, value.NumberValue(v))
	}
	return value.MakeGC(value.TagTable, t)
}

// TestPW4_ControlFlowE2E PW4 relooper: a function with branches/loops, after
// promotion to gibbous, jumps to wazero via the trampoline, byte-for-byte
// identical to the interpreter.
func TestPW4_ControlFlowE2E(t *testing.T) {
	cases := []struct {
		name string
		src  string
		args []value.Value
		want float64
	}{
		// numeric for loop (FORPREP/FORLOOP + back-edge safepoint)
		{"sum-for", `local function f(n) local s=0 for i=1,n do s=s+i end return s end; return f`,
			[]value.Value{value.NumberValue(100)}, 5050},
		// if-then-else (TEST/JMP comparison + branch)
		{"abs-pos", `local function f(x) if x<0 then return -x else return x end end; return f`,
			[]value.Value{value.NumberValue(7)}, 7},
		{"abs-neg", `local function f(x) if x<0 then return -x else return x end end; return f`,
			[]value.Value{value.NumberValue(-7)}, 7},
		// comparison LT fast path + branch
		{"max", `local function f(a,b) if a<b then return b else return a end end; return f`,
			[]value.Value{value.NumberValue(3), value.NumberValue(8)}, 8},
		// nested for (PW4b shape, may Skip if the relooper does not support it)
		{"nested-for", `local function f(n) local s=0 for i=1,n do for j=1,n do s=s+1 end end return s end; return f`,
			[]value.Value{value.NumberValue(10)}, 100},
		// while loop
		{"while", `local function f(n) local s=0 local i=1 while i<=n do s=s+i i=i+1 end return s end; return f`,
			[]value.Value{value.NumberValue(10)}, 55},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

			// Interpreter baseline.
			base, e := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e != nil {
				t.Fatalf("interp call: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp f = %v, want %v", base[0], tc.want)
			}

			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported by gibbous relooper (fallback interp)", tc.name)
			}
			// After promotion: jumps to wazero via the trampoline, byte-equal.
			got, e2 := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e2 != nil {
				t.Fatalf("gibbous call: %v", e2)
			}
			if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous f = %v, want %v (byte-equal with interp)", got[0], tc.want)
			}
		})
	}
}

// TestPW6a_CallDispatch PW6-a: three-way dispatch of a CALL inside a gibbous
// frame, byte-equal. The outer f is promoted to gibbous and calls ① an
// un-promoted crescent helper ② another promoted gibbous ③ a host.
func TestPW6a_CallDispatch(t *testing.T) {
	t.Run("call-crescent", func(t *testing.T) {
		// f calls an un-promoted helper (crescent fresh-reentry path)
		src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
return f`
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		args := []value.Value{value.NumberValue(20)}
		base, e := st.Call(value.GCRefOf(fVal), args, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if value.AsNumber(base[0]) != 41 {
			t.Fatalf("interp f(20) = %v, want 41", base[0])
		}
		if !promoteProto(st, pid) {
			t.Skip("f not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if value.AsNumber(got[0]) != 41 {
			t.Errorf("gibbous f(20) = %v, want 41 (call crescent helper byte-equal)", got[0])
		}
	})

	t.Run("call-gibbous", func(t *testing.T) {
		// both f and helper are promoted (gibbous→gibbous via h_call then enterGibbous)
		src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
return f, helper`
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal, hVal := rets[0], rets[1]
		fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
		args := []value.Value{value.NumberValue(20)}
		base, _ := st.Call(value.GCRefOf(fVal), args, 1)
		if value.AsNumber(base[0]) != 41 {
			t.Fatalf("interp f(20) = %v, want 41", base[0])
		}
		if !promoteProto(st, hPid) || !promoteProto(st, fPid) {
			t.Skip("f/helper not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if value.AsNumber(got[0]) != 41 {
			t.Errorf("gibbous→gibbous f(20) = %v, want 41", got[0])
		}
	})
}

// TestPW6a_CallBaseRefresh PW6-a core: after the callee frame's deep recursion
// overflows the initial stack (64 slots) and triggers growStack segment
// relocation, gibbous continues computing and reads registers correctly using
// the refreshed base. Under GC stress, if base is not refreshed it would read
// an already-Freed old segment → value corruption/UAF.
func TestPW6a_CallBaseRefresh(t *testing.T) {
	// helper recursion depth 100 (one Lua frame per level; overflowing the
	// initial 64 slots is guaranteed to growStack); f calls helper then adds 7,
	// verifying that after returning f's registers (base-relative) still read
	// correctly.
	src := `
local function helper(n) if n <= 0 then return 0 else return helper(n-1) + 1 end end
local function f(a) local r = helper(100) return r + a end
return f`
	st, mainCl := loadFn(t, src)
	st.SetGCStressMode(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	fVal := rets[0]
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	args := []value.Value{value.NumberValue(7)}
	base, e := st.Call(value.GCRefOf(fVal), args, 1)
	if e != nil {
		t.Fatalf("interp: %v", e)
	}
	if value.AsNumber(base[0]) != 107 {
		t.Fatalf("interp f(7) = %v, want 107 (helper(100)=100 + 7)", base[0])
	}
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous: %v", e2)
	}
	if value.AsNumber(got[0]) != 107 {
		t.Errorf("gibbous f(7) = %v, want 107 (base 刷新后读对 a=7;若 base 陈旧则错)", got[0])
	}
}

// TestPW6b_TailCall PW6-b: TAILCALL inside a gibbous frame, byte-equal + constant
// stack depth. Tail recursion f(n,acc): after promotion, reuses the frame via
// h_tailcall, a proper tail call with O(1) stack (executeFrom iterates the
// tail-call chain inside the interpreter), depth 1e5 without overflow.
//
// Note: a deep tail-recursion baseline triggers natural promotion (back-edge
// crosses the threshold) → NotCompilable at compile time → the TierStuck
// absorbing state, making a subsequent promoteProto fail. So the oracle and
// gibbous each use an independent State: the gibbous State runs promoteProto
// before any deep-recursion run.
func TestPW6b_TailCall(t *testing.T) {
	src := `
local function f(n, acc)
  if n == 0 then return acc else return f(n-1, acc+n) end
end
return f`
	loadF := func() (*State, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0]
	}
	args := []value.Value{value.NumberValue(1000), value.NumberValue(0)}
	// oracle: an independent State runs the interpreter.
	stO, fO := loadF()
	base, e := stO.Call(value.GCRefOf(fO), args, 1)
	if e != nil {
		t.Fatalf("interp: %v", e)
	}
	if value.AsNumber(base[0]) != 500500 {
		t.Fatalf("interp f(1000,0) = %v, want 500500", base[0])
	}
	// gibbous: an independent State, run promoteProto before the deep recursion (to avoid natural-promotion stuck).
	st, fVal := loadF()
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous: %v", e2)
	}
	if value.AsNumber(got[0]) != 500500 {
		t.Errorf("gibbous f(1000,0) = %v, want 500500 (tail call byte-equal)", got[0])
	}

	// Deep tail recursion (1e5): a proper tail call does not overflow (if mistakenly treated as an ordinary CALL it would stack overflow).
	deep := []value.Value{value.NumberValue(100000), value.NumberValue(0)}
	gotDeep, e3 := st.Call(value.GCRefOf(fVal), deep, 1)
	if e3 != nil {
		t.Fatalf("gibbous deep tail recursion: %v (proper tail call 应 O(1) 栈)", e3)
	}
	if value.AsNumber(gotDeep[0]) != 5000050000 {
		t.Errorf("gibbous f(1e5,0) = %v, want 5000050000", gotDeep[0])
	}
}

// TestPW6c_ErrorCrossesGibbous PW6-c: an error crossing a gibbous frame bubbles
// up to the pcall boundary, byte-equal (error message + whether it is caught).
func TestPW6c_ErrorCrossesGibbous(t *testing.T) {
	// f calls helper, helper does arithmetic on nil and errors; the error
	// bubbles out of f via the h_call status chain, then is caught at the
	// ProtectedCall boundary. The gibbous and interpreter error messages are
	// byte-for-byte identical.
	src := `
local function helper(x) return x + 1 end   -- helper(nil) → 对 nil 算术报错
local function f(a) return helper(a) end
return f`
	loadF := func() (*State, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0]
	}
	badArg := []value.Value{value.Nil}

	// oracle: the interpreter runs f(nil), catching the error message via ProtectedCall.
	stO, fO := loadF()
	_, eO := stO.ProtectedCall(fO, badArg)
	if eO == nil {
		t.Fatal("interp f(nil) 应报错(对 nil 算术)")
	}
	wantMsg := eO.Msg

	// gibbous: after f is promoted, the error bubbles via the h_call status chain, ProtectedCall catches it, message is the same.
	st, fVal := loadF()
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	_, eG := st.ProtectedCall(fVal, badArg)
	if eG == nil {
		t.Fatal("gibbous f(nil) 应报错(错误经 status 链穿越 gibbous 帧冒泡)")
	}
	if eG.Msg != wantMsg {
		t.Errorf("gibbous 错误消息 = %q, want %q (byte-equal traceback)", eG.Msg, wantMsg)
	}
}

// TestPW7a_Closure PW7-a: CLOSURE creating a closure inside a gibbous frame,
// byte-equal. The outer f is promoted to gibbous, and its inner CLOSURE creates
// g capturing the local x (MOVE pseudo-instruction), then calls g.
func TestPW7a_Closure(t *testing.T) {
	cases := []struct {
		name string
		src  string
		arg  float64
		want float64
	}{
		// CLOSURE captures the stack local x (MOVE pseudo-instruction)
		{"capture-local", `
local function f(a)
  local x = a + 10
  local function g() return x * 2 end
  return g()
end
return f`, 5, 30},
		// nested capture: g captures x, h captures x via g's upvalue (GETUPVAL pseudo-instruction)
		{"capture-upval", `
local function f(a)
  local x = a
  local function g()
    local function h() return x + 1 end
    return h()
  end
  return g()
end
return f`, 7, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadF := func() (*State, value.Value) {
				st, mainCl := loadFn(t, tc.src)
				rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
				if err != nil {
					t.Fatalf("run main: %v", err)
				}
				return st, rets[0]
			}
			args := []value.Value{value.NumberValue(tc.arg)}
			stO, fO := loadF()
			base, e := stO.Call(value.GCRefOf(fO), args, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			st, fVal := loadF()
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			if !promoteProto(st, pid) {
				t.Skipf("%s f not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (CLOSURE byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW4b_TForLoop PW4b: TFORLOOP generic for inside a gibbous frame,
// byte-equal. The iterator is called across layers via h_tforloop (reusing
// callLuaFromHost); base is refreshed (an iterator call may growStack). Uses a
// custom iterator (the crescent tests have no stdlib, so ipairs/pairs are nil
// globals; the TFORLOOP opcode does not care about the iterator's origin, always
// R(A)(R(A+1),R(A+2)), and a custom iterator goes through the exact same
// TFORLOOP path).
func TestPW4b_TForLoop(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want float64
	}{
		// custom range iterator (stateless, closure counter)
		{"range-sum", `
local function range(n)
  local i = 0
  return function() i = i + 1; if i <= n then return i end end
end
local function f()
  local s = 0
  for x in range(5) do s = s + x end
  return s
end
return f`, 15},
		// classic stateless iterator (iter, state, control triple, simulating ipairs)
		{"stateful-iter", `
local function iter(t, i)
  i = i + 1
  local v = t[i]
  if v ~= nil then return i, v end
end
local function f()
  local t = {10, 20, 30, 40}
  local s = 0
  for i, v in iter, t, 0 do s = s + v end
  return s
end
return f`, 100},
		// deep iteration (overflows the initial stack, verifies base refresh)
		{"deep-iter", `
local function range(n)
  local i = 0
  return function() i = i + 1; if i <= n then return i end end
end
local function f()
  local s = 0
  for x in range(2000) do s = s + x end
  return s
end
return f`, 2001000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadF := func() (*State, value.Value) {
				st, mainCl := loadFn(t, tc.src)
				rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
				if err != nil {
					t.Fatalf("run main: %v", err)
				}
				return st, rets[0]
			}
			stO, fO := loadF()
			base, e := stO.Call(value.GCRefOf(fO), nil, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			st, fVal := loadF()
			st.SetGCStressMode(true) // iterator calls allocate heavily; freelist reuse exposes base/root problems
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			if !promoteProto(st, pid) {
				t.Skipf("%s f not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (TFORLOOP byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW8_CoroutineNoPromote PW8 thread-level tier rule: execution on a
// coroutine thread is not promoted (07-coroutine-thread-rule §2). A hot function
// inside a coroutine stays TierInterp after crossing the threshold (the onMain
// guard of the promotion-consideration entry blocks it); the same Proto is
// promoted normally when driven on the main thread.
func TestPW8_CoroutineNoPromote(t *testing.T) {
	src := `
local function hot(a) return a + 1 end
local function body()
  local s = 0
  for i = 1, 100000 do s = hot(s) end   -- hot 在协程内被反复调用(越入口阈值)
  return s
end
return hot, body`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	hotVal, bodyVal := rets[0], rets[1]
	hotPid := object.ClosureProtoID(st.arena, value.GCRefOf(hotVal))
	// hot is compilable (single BB ADD + RETURN); manually mark it Compilable to simulate real P3 F7 allowing it.
	st.bridge.SetCompilability(st.protos[hotPid], bridge.CompCompilable, 0)

	// Run body on a coroutine thread (body calls hot 100k times inside, far exceeding HotEntryThreshold).
	coID, ce := st.NewCoroutine(bodyVal)
	if ce != nil {
		t.Fatalf("NewCoroutine: %v", ce)
	}
	res, ok, re := st.Resume(coID, nil)
	if re != nil || !ok {
		t.Fatalf("Resume: ok=%v err=%v", ok, re)
	}
	if value.AsNumber(res[0]) != 100000 {
		t.Fatalf("coroutine body() = %v, want 100000", res[0])
	}
	// ★ hot is extremely hot inside the coroutine, but the thread-level tier rule keeps it un-promoted (the onMain guard blocks the promotion-consideration entry).
	if st.bridge.GibbousCodeOf(st.protos[hotPid]) != nil {
		t.Error("协程内 hot 函数不应升层(线程级 tier 规则;onMain 守卫应拦下 considerPromotion)")
	}

	// Control: the same Proto driven on the main thread is promoted successfully (proving hot itself is compilable, and the non-promotion above is
	// a thread gate, not incompilability).
	if !promoteProto(st, hotPid) {
		t.Fatal("hot 在主线程应能升层(单 BB ADD+RETURN),onMain=true 门禁放行")
	}
	if st.bridge.GibbousCodeOf(st.protos[hotPid]) == nil {
		t.Fatal("主线程升层后 GibbousCodeOf 应非 nil")
	}
}

// TestPW9_ForceAllPromoteReal verifies the real path of SetForceAllPromote
// (PW9-b's non-empty guarantee).
//
// **Why this matters**: the inter-layer differential suite
// (test/difftest/p3_test.go) relies on force-all to promote the kernel
// functions to gibbous; if force-all actually promotes no Proto, then
// crescent==gibbous degenerates into the false positive of crescent==crescent.
// This test asserts, via the **real public path** (SetForceAllPromote +
// repeated calls triggering OnEnter, not promoteProto's manual
// SetCompilability), that the kernel functions truly reach TierGibbous, locking
// down non-emptiness.
//
// It also verifies recheckCompilabilityRuntime: compile-time F7, lacking P3
// injection, burns hot into CompNotCompilable; after force-all re-checks (no
// F1-F6, and the real backend's SupportsAllOpcodes allows it), promotion
// succeeds — proving the "bypass the compile-time F7 placeholder, do not bypass
// F1-F6" logic works.
func TestPW9_ForceAllPromoteReal(t *testing.T) {
	src := `
local function hot(a) return a + 1 end
local function body()
  local s = 0
  for i = 1, 5 do s = hot(s) end
  return s
end
return hot, body`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	hotVal, bodyVal := rets[0], rets[1]
	hotPid := object.ClosureProtoID(st.arena, value.GCRefOf(hotVal))

	// Precondition: compile-time F7 (no P3 injection) should already have burned
	// hot into NotCompilable — this is the historical baggage that force-all must
	// climb over. If not, this test's "re-check" semantics lose their meaning.
	if st.bridge.CompilabilityOf(st.protos[hotPid]) == bridge.CompCompilable {
		t.Fatal("前置假设破:hot 编译期不应是 CompCompilable(无 P3 注入 F7 应触发)")
	}

	// Real public path: enable force-all, repeatedly call body (body calls hot) to drive OnEnter to trigger promotion.
	st.SetForceAllPromote(true)
	for k := 0; k < 3; k++ {
		if _, e := st.Call(value.GCRefOf(bodyVal), nil, 1); e != nil {
			t.Fatalf("body() call %d: %v", k, e)
		}
	}

	// ★ Under force-all, via recheckCompilabilityRuntime re-check + promotion: hot should already be TierGibbous.
	if st.bridge.GibbousCodeOf(st.protos[hotPid]) == nil {
		t.Fatal("force-all 下 hot 应升 gibbous(重判 F7 放行),但 GibbousCodeOf 为 nil —— 层间差分套将退化为假阳性")
	}

	// Result correctness: gibbous runs hot's five increments, body() == 5, consistent with the interpreter.
	got, e := st.Call(value.GCRefOf(bodyVal), nil, 1)
	if e != nil {
		t.Fatalf("gibbous body(): %v", e)
	}
	if value.AsNumber(got[0]) != 5 {
		t.Errorf("force-all body() = %v, want 5", got[0])
	}
}

// TestPW9_ForceAllRespectsStructuralGates verifies that force-all does **not**
// bypass the real F1-F6 gates.
//
// A vararg function (F1) must stay crescent even under force-all —
// recheckCompilabilityRuntime only clears the compile-time F7 placeholder, and
// the F1-F6 structural exclusions are preserved as-is (08 §2.3.1 / §2.2 "do not
// bypass the compilability gate").
func TestPW9_ForceAllRespectsStructuralGates(t *testing.T) {
	src := `
local function va(...) local a, b = ...; return (a or 0) + (b or 0) end
local function body()
  local s = 0
  for i = 1, 5 do s = s + va(i, i) end
  return s
end
return va, body`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	vaVal, bodyVal := rets[0], rets[1]
	vaPid := object.ClosureProtoID(st.arena, value.GCRefOf(vaVal))

	st.SetForceAllPromote(true)
	for k := 0; k < 3; k++ {
		if _, e := st.Call(value.GCRefOf(bodyVal), nil, 1); e != nil {
			t.Fatalf("body() call %d: %v", k, e)
		}
	}

	// ★ A vararg function is not promoted even under force-all (F1 real exclusion, recheck preserves ReasonVararg).
	if st.bridge.GibbousCodeOf(st.protos[vaPid]) != nil {
		t.Error("vararg 函数 force-all 下不应升层(F1 真实闸门;recheckCompilabilityRuntime 只清 F7 占位,保留 F1-F6)")
	}
}
