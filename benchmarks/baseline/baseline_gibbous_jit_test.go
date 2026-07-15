//go:build wangshu_p4 && wangshu_profile

// gibbous-jit (P4) tier: baseline micro-benchmarks are promoted to native jit
// execution via force-all, for a three-way comparison against crescent + P3
// (gibbous-wasm).
//
// **Required build tags**: wangshu_p4 + wangshu_profile (the profile hooks must
// be active, otherwise considerPromotion is never triggered and
// SetForceAllPromote has no effect).
//
// **PJ7 simplified form** (per docs/design/p4-method-jit/implementation-progress.md
// §1 PJ7 line): the P4 PJ7 wired-in subset = single-BB "LOADK/LOADBOOL/LOADNIL +
// RETURN A 1" form. This baseline can only measure such minimal functions — more
// complex loop/arith cases need the PJ8+ full opcode family extension
// (MOVE/ADD/FORLOOP, etc., left to the next stage).
//
// Run: go test -tags "wangshu_p4 wangshu_profile" -bench 'Gibbous_JIT' ./benchmarks/baseline/

package baseline

import (
	"fmt"
	"testing"

	"github.com/Liam0205/wangshu"
)

// Wrap a single-value return into a non-vararg inner kernel called repeatedly
// (to avoid the vararg top level never being promoted).
//
// The kernel() form is the P4 PJ7 single-BB subset (LOADK + RETURN A 1) — the
// only form currently wired in. After kernel is promoted to P4 via the hotness
// threshold or force-all, it is called 50 times through the jit path, so its
// ns/op can be compared against the crescent baseline to demonstrate the actual
// P4 speedup.
func wrapKernelJIT(body string) string {
	return "local function kernel()\n" + body + "\nend\nlocal t = 0\nfor _ = 1, 50 do t = kernel() end\nreturn t"
}

// wrapKernelJITWithArg wraps a kernel that takes an argument (used for the
// reg-limit form: `for i=1,n do end`, where n is the argument reg R(0) = luac
// compiles MOVE R(A+1) R(0)). The kernel receives one argument N and runs the loop
// with N; the outer wrap × 50 calls kernel(N) to amortize the boundary.
func wrapKernelJITWithArg(body string, argName string, argValue int) string {
	return "local function kernel(" + argName + ")\n" + body + "\nend\nlocal t = 0\nfor _ = 1, 50 do t = kernel(" +
		fmt.Sprint(argValue) + ") end\nreturn t"
}

func benchGibbousJIT(b *testing.B, body string, force bool) {
	prog, err := wangshu.Compile([]byte(wrapKernelJIT(body)), "bench-jit")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // warmup promotion
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// benchGibbousJITWithArg is like benchGibbousJIT but wraps a kernel that takes an argument.
func benchGibbousJITWithArg(b *testing.B, body, argName string, argValue int, force bool) {
	src := wrapKernelJITWithArg(body, argName, argValue)
	prog, err := wangshu.Compile([]byte(src), "bench-jit-arg")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// kernel body —— P4 PJ7 wired-in subset (single BB "value production + RETURN" +
// two-stage arithmetic chaining + comparison folding, etc.).

// constant number return (LOADK + RETURN)
const constBody = `return 42`

// constant nil return (empty RETURN of length 1 form — equivalent to `function() end`)
const nilBody = `return nil`

// constant bool return (LOADBOOL + RETURN)
const boolBody = `return true`

// arithmetic family (single op): MUL + RETURN
const mulBody = `local x=7; return x*2`

// two-stage arithmetic chaining: MUL + ADD + RETURN (`x*2+1` form)
const chainBody = `local x=7; return x*2+1`

// comparison folding: EQ + JMP + LOADBOOL×2 + RETURN
const cmpBody = `local x=7; return x == 7`

// PJ2 speculative ADD form: R(B) + R(C) dual registers (both B/C ≤ 254) → hits spec
// template (mmap segment directly emits IsNumber guard×2 + movsd+addsd+movsd+ret at byte level).
const specAddBody = `local x=7; local y=11; return x+y`

// PJ2 speculative SUB/MUL/DIV form: reg+reg dual-number speculative template,
// byte-level SSE op = F2 0F 5C/59/5E C1 (SUBSD/MULSD/DIVSD respectively), same
// 92-byte template layout as ADD.
const specSubBody = `local x=11; local y=7; return x-y`
const specMulBody = `local x=6; local y=7; return x*y`
const specDivBody = `local x=42; local y=6; return x/y`

// PJ2 speculative reg-K form: in `R(B) op K`, K is burned into an imm64 direct-emit
// segment at compile time, only guarding the reg end (73-byte template, 19 bytes
// less than reg-reg). Common hot-path constant form; luac compiles `x+5` etc. into
// ADD A B(reg) C(>=256 = K idx).
const specRegKAddBody = `local x=7; return x+5`
const specRegKSubBody = `local x=10; return x-3`
const specRegKMulBody = `local x=7; return x*2`
const specRegKDivBody = `local x=42; return x/6`

// PJ2 speculative chain-KK two-stage chaining form: `R(B) op1 K1 op2 K2`, luac
// compiles `x*2+1` into a MUL+ADD chain (op1.C=K1 / op2.C=K2 / op2.B=retA
// intermediate value linkage). The chain template reuses xmm0 across both SSE
// binops, completing two arithmetic operations in one mmap segment call, saving one
// boundary crossing + one reg-stack transit.
const specChainMulAddBody = `local x=7; return x*2+1`
const specChainAddMulBody = `local x=7; return (x+1)*2`

// PJ3 FORLOOP byte-level inline: `function() for i=1,K do end end` form.
// P4 runs control flow (loops) at byte level inside the mmap segment for the first
// time, without any host helper round-trip.
// Template 69 bytes (float idx+step / ucomisd limit / backward jmp).
//
// **Note**: wrapKernelJIT wraps the body into a kernel, then `t = kernel()` calls it
// 50 times. To match analyzeForLoopForm's "three LOADK + FORPREP + FORLOOP +
// RETURN (empty)" 6-7 op closed form, the kernel body must be "for i=1,K do end"
// with no return value / other statements — `for ... end` (empty return implied)
// rather than `for ... end; return 0`.
const pj3ForLoop100Body = `for i = 1, 100 do end`
const pj3ForLoop1000Body = `for i = 1, 1000 do end`
const pj3ForLoop10000Body = `for i = 1, 10000 do end`

// PJ3 reg-limit hot path benchmark: `function(n) for i=1,n do end end`
// (luac compiles [1] MOVE A_init+1 0, i.e. limit = R(0) = argument n). The 117-byte
// template contains IsNumber guard + float loop + safepoint + deopt block; the fast
// path is the same byte-level inline as the all-constant empty body, differing only
// by one extra guard.
func BenchmarkGibbousJIT_PJ3RegLimit1000(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 1000, true)
}
func BenchmarkGibbousJIT_PJ3RegLimit1000Cresc(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 1000, false)
}
func BenchmarkGibbousJIT_PJ3RegLimit10000(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 10000, true)
}
func BenchmarkGibbousJIT_PJ3RegLimit10000Cresc(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 10000, false)
}

// PJ3 body inline benchmark: `local s=0; for i=1,K do s=s+1 end; return s`
// form (135-byte template with safepoint) — a real production hot path, a
// state-accumulating loop.
const pj3BodyAdd1000Body = `local s=0; for i=1,1000 do s=s+1 end; return s`
const pj3BodyAdd10000Body = `local s=0; for i=1,10000 do s=s+1 end; return s`

func BenchmarkGibbousJIT_PJ3BodyAdd1000(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd1000Body, true)
}
func BenchmarkGibbousJIT_PJ3BodyAdd1000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd1000Body, false)
}
func BenchmarkGibbousJIT_PJ3BodyAdd10000(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd10000Body, true)
}
func BenchmarkGibbousJIT_PJ3BodyAdd10000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd10000Body, false)
}

func BenchmarkGibbousJIT_Const(b *testing.B)      { benchGibbousJIT(b, constBody, true) }
func BenchmarkGibbousJIT_ConstCresc(b *testing.B) { benchGibbousJIT(b, constBody, false) }
func BenchmarkGibbousJIT_Nil(b *testing.B)        { benchGibbousJIT(b, nilBody, true) }
func BenchmarkGibbousJIT_NilCresc(b *testing.B)   { benchGibbousJIT(b, nilBody, false) }
func BenchmarkGibbousJIT_Bool(b *testing.B)       { benchGibbousJIT(b, boolBody, true) }
func BenchmarkGibbousJIT_BoolCresc(b *testing.B)  { benchGibbousJIT(b, boolBody, false) }
func BenchmarkGibbousJIT_Mul(b *testing.B)        { benchGibbousJIT(b, mulBody, true) }
func BenchmarkGibbousJIT_MulCresc(b *testing.B)   { benchGibbousJIT(b, mulBody, false) }
func BenchmarkGibbousJIT_Chain(b *testing.B)      { benchGibbousJIT(b, chainBody, true) }
func BenchmarkGibbousJIT_ChainCresc(b *testing.B) { benchGibbousJIT(b, chainBody, false) }
func BenchmarkGibbousJIT_Cmp(b *testing.B)        { benchGibbousJIT(b, cmpBody, true) }
func BenchmarkGibbousJIT_CmpCresc(b *testing.B)   { benchGibbousJIT(b, cmpBody, false) }

// PJ2 speculative ADD reg+reg form: real luajc-tier related data that hits the spec template.
func BenchmarkGibbousJIT_SpecAdd(b *testing.B)      { benchGibbousJIT(b, specAddBody, true) }
func BenchmarkGibbousJIT_SpecAddCresc(b *testing.B) { benchGibbousJIT(b, specAddBody, false) }

// PJ2 speculative SUB/MUL/DIV same P4 vs crescent comparison (hits the 92-byte spec template).
func BenchmarkGibbousJIT_SpecSub(b *testing.B)      { benchGibbousJIT(b, specSubBody, true) }
func BenchmarkGibbousJIT_SpecSubCresc(b *testing.B) { benchGibbousJIT(b, specSubBody, false) }
func BenchmarkGibbousJIT_SpecMul(b *testing.B)      { benchGibbousJIT(b, specMulBody, true) }
func BenchmarkGibbousJIT_SpecMulCresc(b *testing.B) { benchGibbousJIT(b, specMulBody, false) }
func BenchmarkGibbousJIT_SpecDiv(b *testing.B)      { benchGibbousJIT(b, specDivBody, true) }
func BenchmarkGibbousJIT_SpecDivCresc(b *testing.B) { benchGibbousJIT(b, specDivBody, false) }

// PJ2 speculative reg-K four tiers (73-byte template, single guard, K burned as imm64) P4 vs crescent.
func BenchmarkGibbousJIT_SpecRegKAdd(b *testing.B)      { benchGibbousJIT(b, specRegKAddBody, true) }
func BenchmarkGibbousJIT_SpecRegKAddCresc(b *testing.B) { benchGibbousJIT(b, specRegKAddBody, false) }
func BenchmarkGibbousJIT_SpecRegKSub(b *testing.B)      { benchGibbousJIT(b, specRegKSubBody, true) }
func BenchmarkGibbousJIT_SpecRegKSubCresc(b *testing.B) { benchGibbousJIT(b, specRegKSubBody, false) }
func BenchmarkGibbousJIT_SpecRegKMul(b *testing.B)      { benchGibbousJIT(b, specRegKMulBody, true) }
func BenchmarkGibbousJIT_SpecRegKMulCresc(b *testing.B) { benchGibbousJIT(b, specRegKMulBody, false) }
func BenchmarkGibbousJIT_SpecRegKDiv(b *testing.B)      { benchGibbousJIT(b, specRegKDivBody, true) }
func BenchmarkGibbousJIT_SpecRegKDivCresc(b *testing.B) { benchGibbousJIT(b, specRegKDivBody, false) }

// PJ2 speculative chain-KK two-stage chaining (92-byte template, single guard, dual
// K imm64, running segment by segment saves one boundary) P4 vs crescent.
func BenchmarkGibbousJIT_SpecChainMulAdd(b *testing.B) {
	benchGibbousJIT(b, specChainMulAddBody, true)
}
func BenchmarkGibbousJIT_SpecChainMulAddCresc(b *testing.B) {
	benchGibbousJIT(b, specChainMulAddBody, false)
}
func BenchmarkGibbousJIT_SpecChainAddMul(b *testing.B) {
	benchGibbousJIT(b, specChainAddMulBody, true)
}
func BenchmarkGibbousJIT_SpecChainAddMulCresc(b *testing.B) {
	benchGibbousJIT(b, specChainAddMulBody, false)
}

// PJ3 FORLOOP byte-level inline (69-byte template, self-loop inside the mmap
// segment) P4 vs crescent.
// This is P4's first "run control flow at byte level without a host helper" real
// large-speedup verification tier.
// Different K (100/1000/10000) verifies the real gain of backward jmp amortizing the boundary.
func BenchmarkGibbousJIT_PJ3For100(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop100Body, true)
}
func BenchmarkGibbousJIT_PJ3For100Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop100Body, false)
}
func BenchmarkGibbousJIT_PJ3For1000(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop1000Body, true)
}
func BenchmarkGibbousJIT_PJ3For1000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop1000Body, false)
}
func BenchmarkGibbousJIT_PJ3For10000(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop10000Body, true)
}
func BenchmarkGibbousJIT_PJ3For10000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop10000Body, false)
}

// PJ4 IC ArrayHit benchmark —— `function(t) return t[K] end` form byte-level inline.
//
// **wrap form choice**: PJ4 IC inline triggering requires the P1 interpreter to
// first fill IC[0]=ArrayHit, then on promotion analyzeGetTableArrayHit hits. This
// wrap uses an "inner kernel function + outer calls multiple times" form:
//
//   - inner kernel is the PJ4 form (2 op: GETTABLE + RETURN);
//   - outer first runs the P1 interpreter 50 times for warmup so IC[0] gets filled
//     (when force=true, warmup runs in the two prog.Run rounds before the benchmark
//     entry; the first round P1 fills IC, the second round force-all promotes inner
//     so analyzeGetTableArrayHit hits);
//   - inside the benchmark loop, inner is already a byte-level inline template.
//
// **Honest data** (Xeon 6982P, 200 iter):
//
//	PJ4IcArrayHit1     4654 ns/op   PJ4IcArrayHit1Cresc  4214 ns/op (slower 10%)
//	PJ4IcArrayHit2     4906 ns/op   PJ4IcArrayHit2Cresc  4248 ns/op (slower 15%)
//
// **Negative speedup is expected**: the P1 interpreter's icGetTable is already a
// few-Go-instruction fast path of "IC hit means direct array segment access"
// (skipping the full hash), completely equivalent to what the P4 byte-level IC
// inline template does. The difference is "P4 additionally pays callJITSpec
// trampoline in/out + register save/restore" (~50ns overhead), which P1 doesn't have.
//
// **The real speedup scenario is left to PJ5 CALL inline**: after also promoting
// outer to P4, the multiple GETTABLEs inside outer don't pay the doCall boundary,
// and IC inline only shows speedup under the combination of "no doCall crossing +
// byte-level direct array segment access". This tier is kept as SpecTableHits
// prove-the-path hit evidence (SpecTableHits is asserted > 0 by the PJ4 e2e test) +
// same-form P1 baseline comparison.
//
// vs gopher-lua: this tier doesn't add a gopher comparison (the single-inner-kernel
// form comparison caliber is per baseline_test.go's same wrap path).

// wrapKernelJITForPJ4IcArray: outer holds a table + runs 50 inner kernel(tbl) calls.
// kernel is the PJ4 IC ArrayHit promotion target (`function(tb) return tb[idx] end`).
// The first time P1 runs outer, GETTABLE inside kernel → icGetTable fills
// IC[0]=ArrayHit; the second time force-all promotes kernel,
// analyzeGetTableArrayHit hits the IC slot → byte-level inline compilation;
// subsequent prog.Run each outer × 50 calls emits byte level directly.
func wrapKernelJITForPJ4IcArray(idx int) string {
	return "local t = {42, 43, 44}\n" +
		"local function kernel(tb) return tb[" + fmt.Sprint(idx) + "] end\n" +
		"local r = 0\nfor _ = 1, 50 do r = kernel(t) end\nreturn r"
}

// benchGibbousJITPJ4Table runs the PJ4 IC ArrayHit benchmark form.
//
// **Key phase ordering**: first turn off force-all and run once to let the P1
// interpreter fill IC[0], then turn on force-all + run once to promote the inner
// kernel (IC[0] already filled → analyzeGetTableArrayHit hits → IC inline
// compilation). From the third time on, the b.N loop directly hits the promoted
// byte-level inline template.
//
// When force=false, neither warmup turns on force-all, the inner kernel is never
// promoted (length 2 < MinPromotableCodeLen=10, short-proto guard rejects); the b.N
// path still goes through the P1 interpreter icGetTable host helper, serving as the
// crescent-tier comparison baseline.
func benchGibbousJITPJ4Table(b *testing.B, idx int, force bool) {
	src := wrapKernelJITForPJ4IcArray(idx)
	prog, err := wangshu.Compile([]byte(src), "bench-jit-pj4-table")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil { // P1 warmup fills IC slot
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // force-all promotes inner kernel
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// PJ4 IC ArrayHit benchmark P4 vs crescent (force=true: P4 byte-level inline;
// force=false: under the same wrapper form, P1 interpreter icGetTable host helper path).
//
// idx=1 (t[1]) is the most common hot-path form; idx=2 verifies the numeric-key stableIndex difference.
func BenchmarkGibbousJIT_PJ4IcArrayHit1(b *testing.B) {
	benchGibbousJITPJ4Table(b, 1, true)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit1Cresc(b *testing.B) {
	benchGibbousJITPJ4Table(b, 1, false)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit2(b *testing.B) {
	benchGibbousJITPJ4Table(b, 2, true)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit2Cresc(b *testing.B) {
	benchGibbousJITPJ4Table(b, 2, false)
}

// PJ5 SELF method call inline benchmark P4 vs crescent.
// force=true: PJ5 SELF inline main path (host.Self + host.CallBaseline byte-equal P1);
// force=false: crescent interpreter SELF + CALL bytecode path.
//
// **Current performance expectation** (per §9.17): the PJ5 SELF inline path goes
// through host.Self / host.CallBaseline full P1 byte-equal — the same helper chain
// as the crescent interpreter, **so the performance difference mainly comes from P4
// promotion + DoReturn frame-pop vs interpreter main loop + setReg/reg identical
// operations**, with no significant speedup (PJ5 SELF inline is currently a
// "correctness wire-in" rather than a "performance speedup"). After the in-segment
// EmitSelfCallInline template is wired in (left to later sessions), skipping the
// host round-trip via IC NodeHit / ArrayHit guard can gain ≥2x speedup.
//
// This benchmark is used for baseline data collection — it serves as the comparison
// baseline when the spec template is later wired in.
func benchGibbousJITSelfCall(b *testing.B, force bool) {
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
local function kernel()
  caller(o, 42)
end
local t = 0
for _ = 1, 50 do kernel() end
return t`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // warmup promotion
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCall(b *testing.B)      { benchGibbousJITSelfCall(b, true) }
func BenchmarkGibbousJIT_PJ5SelfCallCresc(b *testing.B) { benchGibbousJITSelfCall(b, false) }

// PJ5 SELF + CALL spec template benchmark (per §9.10 EmitSelfNodeHit reuse).
//
// `caller(t) { t:m() }` 0-arg void form: warmup lets P1 fill SELF IC[1]=NodeHit +
// FBSelfMono, then force-all promotes caller → analyzeSelfCallSpecForm hits → the
// SELF segment goes through the byte-level EmitSelfNodeHit template (skipping the
// host.Self round-trip), the CALL segment still goes through host.CallBaseline.
//
// **vs non-spec version**: the non-spec version (benchGibbousJITSelfCall
// force=true) runs the whole thing through host.Self + host.CallBaseline; the spec
// version's SELF segment is byte-level inline, saving one host.Self cross-Go
// round-trip. spec=true vs spec=false (pure crescent) comparison can quantify the
// byte-level SELF segment speedup contribution.
func benchGibbousJITSelfCallSpec(b *testing.B, force bool) {
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 50 do caller(o) end
return count`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-spec")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	// Phase 1: warmup (no force) lets P1 fill SELF IC[1]=NodeHit + FBSelfMono
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // force-all promotes caller (spec hit)
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallSpec(b *testing.B)      { benchGibbousJITSelfCallSpec(b, true) }
func BenchmarkGibbousJIT_PJ5SelfCallSpecCresc(b *testing.B) { benchGibbousJITSelfCallSpec(b, false) }

// PJ5 SELF + CALL spec template — compute-intensive method body (verifies amortization effect).
//
// Per profile finding (.llmdoc-tmp/investigations/2026-06-28-pj5-self-call-segment-profile.md):
// PJ5SelfCallSpec's method body is too simple (single ADD count++), amplifying the
// trampoline share → P4 12% slower. This bench's method body contains a FORLOOP
// arithmetic loop (P4 PJ3 byte-level inline large speedup) → method body speedup
// dominates, the caller's SELF+CALL trampoline overhead is amortized, verifying "P4
// SELF+CALL amortizes the trampoline when the method body is compute-intensive".
func benchGibbousJITSelfCallHeavyBody(b *testing.B, force bool) {
	src := `
local o = { m = function(self) local s = 0; for i = 1, 100 do s = s + i end; return s end }
local function caller(t) return t:m() end
local total = 0
for i = 1, 50 do total = total + caller(o) end
return total`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-heavy")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil { // warmup
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallHeavyBody(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallHeavyBodyCresc(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody(b, false)
}

// PJ5 SELF + CALL spec template 1 reg arg + compute-intensive method body (real
// business comparison after covering 0..7 args — verifies the multi-arg spec
// template amortization effect works the same way).
func benchGibbousJITSelfCallHeavyBody1Arg(b *testing.B, force bool) {
	src := `
local o = { m = function(self, n) local s = 0; for i = 1, n do s = s + i end; return s end }
local function caller(t, v) return t:m(v) end
local total = 0
for i = 1, 50 do total = total + caller(o, 100) end
return total`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-heavy-1arg")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallHeavyBody1Arg(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody1Arg(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallHeavyBody1ArgCresc(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody1Arg(b, false)
}

// PJ5 SELF + CALL spec template — 3 reg args + compute-intensive method body (amortization full-tier comparison).
func benchGibbousJITSelfCallHeavyBody3Arg(b *testing.B, force bool) {
	src := `
local o = { m = function(self, n, mul, off) local s = 0; for i = 1, n do s = s + i * mul + off end; return s end }
local function caller(t, a, b, c) return t:m(a, b, c) end
local total = 0
for i = 1, 50 do total = total + caller(o, 100, 2, 3) end
return total`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-heavy-3arg")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallHeavyBody3Arg(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody3Arg(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallHeavyBody3ArgCresc(b *testing.B) {
	benchGibbousJITSelfCallHeavyBody3Arg(b, false)
}

// PJ5 SELF + CALL spec template N=4 return drop multi-ret form (per this batch's
// isValidSpecCallRetCount cC∈{1,3..16} extension): caller `local a,b,c,d = t:m()`
// form with a simple method body (count++) — verifies N=4 return spec template amortization.
func benchGibbousJITSelfCallSpecMultiRetN4(b *testing.B, force bool) {
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m() end
for i = 1, 100 do caller(nil, mt) end
return count`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-spec-multiret-n4")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN4(b *testing.B) {
	benchGibbousJITSelfCallSpecMultiRetN4(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN4Cresc(b *testing.B) {
	benchGibbousJITSelfCallSpecMultiRetN4(b, false)
}

// PJ5 SELF + CALL N=4 return + compute-intensive method body (amortization
// verification + multi-ret full picture).
// Per the previous batch's N=4 return simple method body being 1.094x slower, this
// batch verifies whether P4 overtakes cres when the method body contains a FORLOOP
// and the trampoline is amortized.
func benchGibbousJITSelfCallHeavyBodyMultiRetN4(b *testing.B, force bool) {
	src := `
local mt = { m = function(self) local s = 0; for i = 1, 100 do s = s + i end; return s, s*2, s*3, s*4 end }
local function caller(_, t) local a, b, c, d = t:m() end
local total = 0
for i = 1, 50 do caller(nil, mt); total = total + 1 end
return total`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-heavy-multiret-n4")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallHeavyBodyMultiRetN4(b *testing.B) {
	benchGibbousJITSelfCallHeavyBodyMultiRetN4(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallHeavyBodyMultiRetN4Cresc(b *testing.B) {
	benchGibbousJITSelfCallHeavyBodyMultiRetN4(b, false)
}

// PJ5 SELF + CALL N=8 return (per 7f5f641 N=8 boundary e2e same form): verifies the
// host.CallBaseline extra SetReg performance overhead under the
// isValidSpecCallRetCount cC=9 boundary (8 R(callA..) word stores, 4 more than N=4).
func benchGibbousJITSelfCallSpecMultiRetN8(b *testing.B, force bool) {
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4, 5, 6, 7, 8 end }
local function caller(_, t) local a, b, c, d, e, f, g, h = t:m() end
for i = 1, 100 do caller(nil, mt) end
return count`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self-spec-multiret-n8")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN8(b *testing.B) {
	benchGibbousJITSelfCallSpecMultiRetN8(b, true)
}
func BenchmarkGibbousJIT_PJ5SelfCallSpecMultiRetN8Cresc(b *testing.B) {
	benchGibbousJITSelfCallSpecMultiRetN8(b, false)
}
