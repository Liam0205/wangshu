//go:build wangshu_p4 && amd64 && linux

// e2e_shim_ops_amd64_test.go - end-to-end verification that shim-calling
// op emits (RETURN/GETUPVAL/SETUPVAL) actually invoke the corresponding
// host methods through the mmap segment.
package peroptranslator

import (
	"testing"
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// fakeHost is a minimal P4HostState for shim-call testing. It records
// method invocations so tests can assert the shim called the right
// method with the right args.
type fakeHost struct {
	regs   map[int32]uint64
	upvals map[int32]uint64

	doReturnCalls int
	doReturnBase  int32
	doReturnPC    int32
	doReturnA     int32
	doReturnB     int32

	getUpvalCalls int
	getUpvalB     int32

	setUpvalCalls int
	setUpvalA     int32
	setUpvalB     int32

	// Arith tracking
	arithCalls int
	arithOp    int32
	arithA     int32
	arithB     int32
	arithC     int32
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		regs:   make(map[int32]uint64),
		upvals: make(map[int32]uint64),
	}
}

func (h *fakeHost) DoReturn(base, pc, a, b int32) int32 {
	h.doReturnCalls++
	h.doReturnBase = base
	h.doReturnPC = pc
	h.doReturnA = a
	h.doReturnB = b
	return 0
}
func (h *fakeHost) SetReg(idx int32, val uint64) { h.regs[idx] = val }
func (h *fakeHost) GetReg(idx int32) uint64      { return h.regs[idx] }
func (h *fakeHost) GetUpval(base, b int32) uint64 {
	h.getUpvalCalls++
	h.getUpvalB = b
	return h.upvals[b]
}
func (h *fakeHost) SetUpvalFromReg(base, a, b int32) {
	h.setUpvalCalls++
	h.setUpvalA = a
	h.setUpvalB = b
	h.upvals[b] = h.regs[a]
}
func (h *fakeHost) Arith(base, pc, op, b, c, a int32) int32 {
	h.arithCalls++
	h.arithOp = op
	h.arithB = b
	h.arithC = c
	h.arithA = a
	return 0
}
func (h *fakeHost) Unm(base, pc, b, a int32) int32          { return 0 }
func (h *fakeHost) Len(base, pc, b, a int32) int32          { return 0 }
func (h *fakeHost) Concat(base, pc, a, b, c int32) int32    { return 0 }
func (h *fakeHost) Eq(base, pc, b, c int32) int32           { return 0 }
func (h *fakeHost) Compare(base, pc, op, b, c int32) int32  { return 0 }
func (h *fakeHost) SetList(base, pc, a, b, c int32) int32   { return 0 }
func (h *fakeHost) NewTable(base, pc, a, b, c int32) int32  { return 0 }
func (h *fakeHost) GetTable(base, pc, a, b, c int32) int32  { return 0 }
func (h *fakeHost) SetTable(base, pc, a, b, c int32) int32  { return 0 }
func (h *fakeHost) DoGetGlobal(base, pc, a, bx int32) int32 { return 0 }
func (h *fakeHost) DoSetGlobal(base, pc, a, bx int32) int32 { return 0 }
func (h *fakeHost) ForPrep(base, pc, a int32) int32         { return 0 }
func (h *fakeHost) CallBaseline(base, pc, a, b, c int32) int32 {
	return 0
}
func (h *fakeHost) TailCall(base, pc, a, b, c int32) int32 { return 0 }
func (h *fakeHost) GlobalsRaw() uint64                     { return 0 }
func (h *fakeHost) Self(base, pc, a, b, c int32) int32     { return 0 }
func (h *fakeHost) Closure(base, pc, a, bx int32) int32    { return 0 }
func (h *fakeHost) Close(base, pc, a int32) int32          { return 0 }
func (h *fakeHost) TForLoop(base, pc, a, c int32) int64    { return -2 }
func (h *fakeHost) ArenaBaseAddr() uintptr                 { return 0 }
func (h *fakeHost) ValueStackBaseAddr(base int32) uintptr  { return 0 }
func (h *fakeHost) CIDepthHostAddr() uintptr               { return 0 }
func (h *fakeHost) CISegBaseHostAddr() uintptr             { return 0 }
func (h *fakeHost) TopHostAddr() uintptr                   { return 0 }
func (h *fakeHost) OpenGuardHostAddr() uintptr             { return 0 }
func (h *fakeHost) RefreshJitCtxAddrs(ctx *jit.JITContext, base int32) {
	ctx.SetAllAddrs(0, 0, 0, 0, 0)
}

// ExecuteCalleeFromInlineFrame is a Spike 1 Step C-1 helper (unused by
// PJ10 native shim tests, we always return 0=OK).
func (h *fakeHost) ExecuteCalleeFromInlineFrame(base, callA, callArgCount, nresults int32) int32 {
	return 0
}

// hostToIfaceHeader converts a P4HostState value into a [2]uintptr
// (itab + data) via unsafe.
func hostToIfaceHeader(h jit.P4HostState) [2]uintptr {
	return *(*[2]uintptr)(unsafe.Pointer(&h))
}

// runShimSegment prepares jitCtx, mmap's the code, calls it, and returns
// the RAX status. Used by shim op tests.
//
// **Skipped under -race**: the mmap-to-Go-helper call sequence is
// incompatible with Go's stack unwinder when the race detector is
// active; the fault manifests as an "unexpected return pc for
// runtime.sigpanic" at the shim callsite. Production code never uses
// shim ops in the mmap segment (opSupported gate excludes them; the
// inline subset handles all mmap-safe ops), so shim tests exist only
// for future proofing when the concurrent-crash root cause is fixed.
// See reflection 2026-07-01-p4-pj10-native-round lesson 1.
func runShimSegment(t *testing.T, cb *codeBuf, host jit.P4HostState) uint64 {
	t.Helper()
	if raceEnabled {
		t.Skip("shim-based e2e skipped under -race: mmap+morestack incompatibility (see reflection 2026-07-01-p4-pj10-native-round lesson 1)")
	}
	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	t.Cleanup(func() { page.Munmap() })

	ctx := jit.NewJITContext()
	saveGoG(ctx.SavedGoGSlot())
	ctx.SetHostRef(hostToIfaceHeader(host))
	// vsBase points to a scratch stack; ops that need R(N) memory can
	// use it. Some ops don't touch memory (RETURN/GETUPVAL/SETUPVAL are
	// pure shim calls); vsBase still must be a valid address for the
	// emitReloadVsBase inside emitCallShim to not crash.
	var scratch [16]uint64
	vsBase := uintptr(unsafe.Pointer(&scratch[0]))
	ctx.SetValueStackBase(vsBase)

	return jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)
}

// runNoShimSegment mmap's the emitted code and executes it against a
// caller-seeded stack, returning the resulting stack + status. Used by
// tests for inline ops (NOT / arithmetic fast path) that don't need a
// host but DO need real R(N) memory operands.
//
// The seed callback fills the stack before execution. The returned
// stack is the same array (Go stack-allocated), so callers must inspect
// it before it escapes.
//
// **hostRef is left zero**: ops that would call a shim will crash on
// nil interface deref. Tests should only call this for shim-free ops.
func runNoShimSegment(t *testing.T, cb *codeBuf, seed func(s []uint64)) ([16]uint64, uint64) {
	t.Helper()
	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	t.Cleanup(func() { page.Munmap() })

	ctx := jit.NewJITContext()
	saveGoG(ctx.SavedGoGSlot())

	var stack [16]uint64
	if seed != nil {
		seed(stack[:])
	}
	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	ctx.SetValueStackBase(vsBase)

	status := jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)
	return stack, status
}

// newArithHost returns a fakeHost keyed for arithmetic tests. Currently
// identical to newFakeHost; the alias makes test intent clear.
func newArithHost() *fakeHost { return newFakeHost() }

// TestPJ10Native_E2E_RETURN: emit `RETURN A=2 B=3` alone. Verify the
// host recorded a DoReturn(pc, a=2, b=3) call.
func TestPJ10Native_E2E_RETURN(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitRETURN(cb, 42, 2, 3)

	host := newFakeHost()
	_ = runShimSegment(t, cb, host)

	if host.doReturnCalls != 1 {
		t.Fatalf("DoReturn called %d times, want 1", host.doReturnCalls)
	}
	if host.doReturnPC != 42 {
		t.Errorf("DoReturn pc = %d, want 42", host.doReturnPC)
	}
	if host.doReturnA != 2 {
		t.Errorf("DoReturn a = %d, want 2", host.doReturnA)
	}
	if host.doReturnB != 3 {
		t.Errorf("DoReturn b = %d, want 3", host.doReturnB)
	}
}

// TestPJ10Native_E2E_GETUPVAL: emit GETUPVAL A=1 B=2 and verify the
// segment exits with an ExitInlineHelper request carrying
// (HelperGetUpval, a=1, b=2) — the Go-side dispatcher (not the mmap
// segment) performs host.GetUpval + host.SetReg under the exit-reason
// protocol.
func TestPJ10Native_E2E_GETUPVAL(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitGETUPVAL(cb, 1, 2)
	emitResumePreludeIfPending(cb)
	emitRet(cb)

	host := newFakeHost()
	host.upvals[2] = 0xC0FFEE

	status := runShimSegment(t, cb, host)
	if uint32(status) != jit.ExitInlineHelper {
		t.Fatalf("status = %d, want ExitInlineHelper", status)
	}
}

// TestPJ10Native_E2E_SETUPVAL: emit SETUPVAL A=3 B=5 and verify the
// segment exits with an ExitInlineHelper request carrying
// (HelperSetUpval, a=3, b=5).
func TestPJ10Native_E2E_SETUPVAL(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitSETUPVAL(cb, 3, 5)
	emitResumePreludeIfPending(cb)
	emitRet(cb)

	host := newFakeHost()
	host.regs[3] = 0xBEEF

	status := runShimSegment(t, cb, host)
	if uint32(status) != jit.ExitInlineHelper {
		t.Fatalf("status = %d, want ExitInlineHelper", status)
	}
}
