//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_tail_tfor_closure_test.go — issue #52: TAILCALL / TFORLOOP /
// CLOSURE / CLOSE admitted to opSupported (amd64 + arm64; the assertions
// are arch-neutral — promote + native run + byte-equal — so one file
// serves both arches under their respective native-runner build tags).
// These are the last four ops from the issue's sweep: each lowers to an
// exit-reason (HelperTailCall terminates the run like HelperReturn;
// HelperTForLoop hands the continue verdict back through exitArg0 like
// HelperCompareSlow; HelperClosure consumes the upvalue
// pseudo-instructions host-side, with the translator walks skipping
// them via nextRealPC; HelperClose is a plain host round trip).

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestPJ10_TailCall_LuaArm_Promotes: a kernel ending in `return f(x)`
// (TAILCALL to a Lua callee) used to keep the whole function on the
// interpreter. It now promotes; the native segment exits to
// HelperTailCall, host.TailCall replaces the frame and drives the callee
// chain to completion (tri-state 0), and Run returns without DoReturn.
func TestPJ10_TailCall_LuaArm_Promotes(t *testing.T) {
	src := `
local function helper(a, b) return a + b end
local function kernel(n)
  local x = n * 2 + 1
  return helper(x, 1)
end
local r
for j = 1, 5 do r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10tailcall")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeRun := peroptranslator.NativeRunCount.Load()
	beforeTail := peroptranslator.TailCallRunCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; TAILCALL kernel did not promote (still rejected?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the promoted proto never ran on the native path")
	}
	if peroptranslator.TailCallRunCount.Load() == beforeTail {
		t.Fatal("TailCallRunCount unchanged; TAILCALL never rode the HelperTailCall exit")
	}
	// kernel(20): x = 41, helper(41, 1) = 42.
	if len(res) != 1 || res[0].Display() != "42" {
		t.Fatalf("kernel(20) = %v, want [42]", res)
	}
}

// TestPJ10_TailCall_HostArm: `return tostring(x)` tail-calls a HOST
// callee — host.TailCall returns tri-state 2 (results at R(A..top),
// frame not popped) and Run finishes via DoReturn on the trailing dead
// RETURN's B=0 multret path. tostring rides an upvalue so the kernel
// avoids an extra cold GETGLOBAL site.
func TestPJ10_TailCall_HostArm(t *testing.T) {
	src := `
local ts = tostring
local function kernel(n)
  local x = n * 2 + 1
  return ts(x)
end
local r
for j = 1, 5 do r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10tailhost")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeTail := peroptranslator.TailCallRunCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; host-arm TAILCALL kernel did not promote")
	}
	if peroptranslator.TailCallRunCount.Load() == beforeTail {
		t.Fatal("TailCallRunCount unchanged; TAILCALL never rode the HelperTailCall exit")
	}
	if len(res) != 1 || res[0].Display() != "41" {
		t.Fatalf("kernel(20) = %v, want [41]", res)
	}
}

// TestPJ10_TailCall_MultiResults: the host arm's DoReturn rides the
// trailing RETURN's B=0 (return R(A..top)) form, so a tail call that
// produces MULTIPLE results must propagate all of them. unpack is a
// host fn returning 3 values here.
func TestPJ10_TailCall_MultiResults(t *testing.T) {
	src := `
local up = unpack
local function kernel(t)
  local x = 1 + 1
  return up(t)
end
local a, b, c
for j = 1, 5 do a, b, c = kernel({7, 8, 9}) end
return a, b, c
`
	prog, err := wangshu.Compile([]byte(src), "pj10tailmulti")
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
		t.Fatal("PromotionCount = 0; multi-result TAILCALL kernel did not promote")
	}
	if len(res) != 3 || res[0].Display() != "7" || res[1].Display() != "8" ||
		res[2].Display() != "9" {
		t.Fatalf("kernel = %v, want [7 8 9]", res)
	}
}

// TestPJ10_TForLoop_Promotes: a generic-for over `next, t` used to keep
// the whole function on the interpreter (TFORLOOP rejected). It now
// promotes; each iteration exits to HelperTForLoop (host.TForLoop
// invokes the iterator + writes the loop vars) and the in-segment
// resume block branches on the exitArg0 verdict — back edge on
// continue, exit BB on first-result nil. `next, t` (not ipairs) keeps
// the iterator-setup CALL out of the proto so the CALL-density gate
// doesn't interfere with what this test proves.
func TestPJ10_TForLoop_Promotes(t *testing.T) {
	src := `
local t = {10, 20, 30}
local nx = next
local function kernel()
  local sum = 0
  for i, v in nx, t do
    sum = sum + i * v
  end
  return sum
end
local r
for j = 1, 5 do r = kernel() end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10tforloop")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeRun := peroptranslator.NativeRunCount.Load()
	beforeDispatch := peroptranslator.DispatchHelperCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; TFORLOOP kernel did not promote (still rejected?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the promoted proto never ran on the native path")
	}
	if peroptranslator.DispatchHelperCount.Load() == beforeDispatch {
		t.Fatal("DispatchHelperCount unchanged; TFORLOOP never rode the exit-reason dispatch")
	}
	// 1*10 + 2*20 + 3*30 = 140 (array part iterates in order; sum is
	// order-independent anyway).
	if len(res) != 1 || res[0].Display() != "140" {
		t.Fatalf("kernel() = %v, want [140]", res)
	}
}

// TestPJ10_Closure_Close_Promotes: a loop body that builds a closure
// over a block-local (CLOSURE + upvalue pseudo-instruction + CLOSE at
// block exit) used to keep the whole function on the interpreter. It
// now promotes; CLOSURE exits to HelperClosure (host.makeClosure
// consumes the pseudo word; the translator's emit walk skipped it), and
// CLOSE exits to HelperClose (closeUpvals). Calling the closure AFTER
// the block closed the upvalue proves the closed-upvalue capture is
// byte-equal to the interpreter.
func TestPJ10_Closure_Close_Promotes(t *testing.T) {
	src := `
local fns = {}
local function kernel(n)
  local acc = 0
  for i = 1, n do
    do
      local x = i * 2
      local f = function() return x end
      fns[i] = f
      acc = acc + f() + i + x - 1 + 2 - 2 + 0 + 0 + 0
    end
  end
  return acc
end
local r
for j = 1, 5 do r = kernel(3) end
return r, fns[1](), fns[3]()
`
	prog, err := wangshu.Compile([]byte(src), "pj10closure")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeRun := peroptranslator.NativeRunCount.Load()
	beforeDispatch := peroptranslator.DispatchHelperCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; CLOSURE+CLOSE kernel did not promote (still rejected?)")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; the promoted proto never ran on the native path")
	}
	// The kernel is the only promotable proto with exit-reason ops (the
	// inner closure is GETUPVAL+RETURN, pure inline; the chunk is vararg
	// — rejected), so a dispatch increase proves the KERNEL ran natively
	// — and a native kernel run necessarily drove CLOSURE and CLOSE
	// through their helpers.
	if peroptranslator.DispatchHelperCount.Load() == beforeDispatch {
		t.Fatal("DispatchHelperCount unchanged; the CLOSURE+CLOSE kernel never rode the exit-reason dispatch")
	}
	// Per i: x = 2i, f() = x, term = x + i + x - 1 = 5i - 1. kernel(3):
	// sum(5i-1) for i=1..3 = 4 + 9 + 14 = 27. fns hold closed upvalues:
	// fns[1]() = 2, fns[3]() = 6.
	if len(res) != 3 || res[0].Display() != "27" || res[1].Display() != "2" ||
		res[2].Display() != "6" {
		t.Fatalf("kernel(3) = %v, want [27 2 6]", res)
	}
}

// TestPJ10_TForLoop_Pairs_StringKeys: pairs-style iteration over a
// hash-part table — exercises the HelperTForLoop exit arm through a
// longer mixed-key walk and the (c >= 1) loop-var write path with two
// vars. Uses `next, t` directly (pairs() would add a CALL).
func TestPJ10_TForLoop_Pairs_StringKeys(t *testing.T) {
	src := `
local t = { a = 1, b = 2, c = 3, d = 4 }
local nx = next
local function kernel()
  local n = 0
  local sum = 0
  for k, v in nx, t do
    n = n + 1
    sum = sum + v
  end
  return n, sum
end
local n, s
for j = 1, 5 do n, s = kernel() end
return n, s
`
	prog, err := wangshu.Compile([]byte(src), "pj10tforhash")
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
		t.Fatal("PromotionCount = 0; hash-part TFORLOOP kernel did not promote")
	}
	if len(res) != 2 || res[0].Display() != "4" || res[1].Display() != "10" {
		t.Fatalf("kernel() = %v, want [4 10]", res)
	}
}

// TestPJ10_TForLoop_IteratorRaises: an iterator that raises mid-loop
// must propagate the error out of the native run (HelperTForLoop's -1
// arm → dispatcher returns false → Run status 1 → pending err).
func TestPJ10_TForLoop_IteratorRaises(t *testing.T) {
	src := `
local boom = function(s, ctrl)
  if ctrl ~= nil and ctrl >= 2 then error("iter-boom") end
  if ctrl == nil then return 1, 10 end
  return ctrl + 1, 10
end
local function kernel()
  local sum = 0
  for i, v in boom, nil do
    sum = sum + i + v + 0 + 0 + 0 + 0 + 0 + 0 + 0
  end
  return sum
end
local ok, e = pcall(kernel)
for j = 1, 5 do ok, e = pcall(kernel) end
return tostring(ok), (e and e:match("iter%-boom")) or "?"
`
	prog, err := wangshu.Compile([]byte(src), "pj10tforraise")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) != 2 || res[0].Display() != "false" || res[1].Display() != "iter-boom" {
		t.Fatalf("kernel() = %v, want [false iter-boom]", res)
	}
}
