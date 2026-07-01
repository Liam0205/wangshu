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
func (h *fakeHost) Arith(base, pc, op, b, c, a int32) int32 { return 0 }
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
func runShimSegment(t *testing.T, cb *codeBuf, host jit.P4HostState) uint64 {
	t.Helper()
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
// shim called host.GetUpval(B=2) followed by host.SetReg(A=1, upvalue).
func TestPJ10Native_E2E_GETUPVAL(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitGETUPVAL(cb, 1, 2)
	emitRet(cb)

	host := newFakeHost()
	host.upvals[2] = 0xC0FFEE

	_ = runShimSegment(t, cb, host)

	if host.getUpvalCalls != 1 {
		t.Fatalf("GetUpval called %d times, want 1", host.getUpvalCalls)
	}
	if host.getUpvalB != 2 {
		t.Errorf("GetUpval b = %d, want 2", host.getUpvalB)
	}
	if got := host.regs[1]; got != 0xC0FFEE {
		t.Errorf("regs[1] = %x, want C0FFEE", got)
	}
}

// TestPJ10Native_E2E_SETUPVAL: emit SETUPVAL A=3 B=5 and verify the
// shim called host.SetUpvalFromReg(a=3, b=5).
func TestPJ10Native_E2E_SETUPVAL(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitSETUPVAL(cb, 3, 5)
	emitRet(cb)

	host := newFakeHost()
	host.regs[3] = 0xBEEF

	_ = runShimSegment(t, cb, host)

	if host.setUpvalCalls != 1 {
		t.Fatalf("SetUpvalFromReg called %d times, want 1", host.setUpvalCalls)
	}
	if host.setUpvalA != 3 || host.setUpvalB != 5 {
		t.Errorf("SetUpvalFromReg a=%d b=%d, want a=3 b=5",
			host.setUpvalA, host.setUpvalB)
	}
	if got := host.upvals[5]; got != 0xBEEF {
		t.Errorf("upvals[5] = %x, want BEEF", got)
	}
}
