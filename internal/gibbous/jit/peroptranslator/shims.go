//go:build wangshu_p4

// shims.go - package-level Go function shims that mmap can call to invoke
// P4HostState methods. Each shim reads the host from jitCtx.hostRef and
// dispatches to the appropriate interface method.
//
// **Why shims (not direct method calls)**: interface method calls need
// a (itab, data) receiver - a single func-ptr can't express that. The
// shim packs the receiver reconstruction into a plain function so the
// mmap emit path only needs `mov rax, shimAddr; call rax`.
//
// **ABI**: All shims use Go ABIInternal (Go 1.17+ default). Register
// order for int args: RAX, RBX, RCX, RDI, RSI, R8, R9, R10, R11.
//   - Arg 0 = *JITContext (from RAX)
//   - Args 1..N = op-specific ints
//   - Return in RAX (status code)
//
// The mmap emit path must:
//   - Place *JITContext in RAX (from R15 via mov)
//   - Place op args in RBX/RCX/RDI/RSI/R8 as needed
//   - Emit emitRestoreGoG (mov r14, [r15+savedGoGOff]) BEFORE the call
//   - Emit `mov rax, shimAddr; call rax` (via emitHelperCall)
//   - Emit emitReloadVsBase (mov rbx, [r15+vsBaseOff]) AFTER the call to
//     restore RBX = vsBase (ABIInternal clobbers RBX)
package peroptranslator

import (
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// hostFromCtx reconstructs the P4HostState interface from jitCtx.hostRef.
// The [2]uintptr encoding matches Go's internal iface header: word0 = itab,
// word1 = data. Cast back via unsafe.
func hostFromCtx(ctx *jit.JITContext) jit.P4HostState {
	ref := ctx.HostRef()
	return *(*jit.P4HostState)(unsafe.Pointer(&ref))
}

// shimDoReturn: host.DoReturn(base, pc, a, b) int32
//
//go:noinline
func shimDoReturn(ctx *jit.JITContext, base, pc, a, b int32) int32 {
	return hostFromCtx(ctx).DoReturn(base, pc, a, b)
}

// shimGetUpval: R(retA) := host.GetUpval(base, b)
//
//go:noinline
func shimGetUpval(ctx *jit.JITContext, base, b, retA int32) int32 {
	h := hostFromCtx(ctx)
	v := h.GetUpval(base, b)
	h.SetReg(retA, v)
	return 0
}

// shimSetUpvalFromReg: host.SetUpvalFromReg(base, a, b) - writes upval b
// from register a. Never raises.
//
//go:noinline
func shimSetUpvalFromReg(ctx *jit.JITContext, base, a, b int32) int32 {
	hostFromCtx(ctx).SetUpvalFromReg(base, a, b)
	return 0
}

// shimArith: host.Arith(base, pc, op, b, c, a) int32
//
//go:noinline
func shimArith(ctx *jit.JITContext, base, pc, op, b, c, a int32) int32 {
	return hostFromCtx(ctx).Arith(base, pc, op, b, c, a)
}

// shimUnm: host.Unm(base, pc, b, a) int32
//
//go:noinline
func shimUnm(ctx *jit.JITContext, base, pc, b, a int32) int32 {
	return hostFromCtx(ctx).Unm(base, pc, b, a)
}

// shimLen: host.Len(base, pc, b, a) int32
//
//go:noinline
func shimLen(ctx *jit.JITContext, base, pc, b, a int32) int32 {
	return hostFromCtx(ctx).Len(base, pc, b, a)
}

// shimConcat: host.Concat(base, pc, a, b, c) int32
//
//go:noinline
func shimConcat(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).Concat(base, pc, a, b, c)
}

// shimEq: host.Eq(base, pc, b, c) int32 (packed bit0=result bit1=err)
//
//go:noinline
func shimEq(ctx *jit.JITContext, base, pc, b, c int32) int32 {
	return hostFromCtx(ctx).Eq(base, pc, b, c)
}

// shimCompare: host.Compare(base, pc, op, b, c) int32 (packed)
//
//go:noinline
func shimCompare(ctx *jit.JITContext, base, pc, op, b, c int32) int32 {
	return hostFromCtx(ctx).Compare(base, pc, op, b, c)
}

// shimGetTable: host.GetTable(base, pc, a, b, c) int32
//
//go:noinline
func shimGetTable(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).GetTable(base, pc, a, b, c)
}

// shimSetTable: host.SetTable(base, pc, a, b, c) int32
//
//go:noinline
func shimSetTable(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).SetTable(base, pc, a, b, c)
}

// shimGetGlobal: host.DoGetGlobal(base, pc, a, bx) int32
//
//go:noinline
func shimGetGlobal(ctx *jit.JITContext, base, pc, a, bx int32) int32 {
	return hostFromCtx(ctx).DoGetGlobal(base, pc, a, bx)
}

// shimSetGlobal: host.DoSetGlobal(base, pc, a, bx) int32
//
//go:noinline
func shimSetGlobal(ctx *jit.JITContext, base, pc, a, bx int32) int32 {
	return hostFromCtx(ctx).DoSetGlobal(base, pc, a, bx)
}

// shimNewTable: host.NewTable(base, pc, a, b, c) int32
//
//go:noinline
func shimNewTable(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).NewTable(base, pc, a, b, c)
}

// shimSetList: host.SetList(base, pc, a, b, c) int32
//
//go:noinline
func shimSetList(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).SetList(base, pc, a, b, c)
}

// shimForPrep: host.ForPrep(base, pc, a) int32
//
//go:noinline
func shimForPrep(ctx *jit.JITContext, base, pc, a int32) int32 {
	return hostFromCtx(ctx).ForPrep(base, pc, a)
}

// shimCall: host.CallBaseline(base, pc, a, b, c) int32
//
//go:noinline
func shimCall(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).CallBaseline(base, pc, a, b, c)
}

// shimTailCall: host.TailCall(base, pc, a, b, c) int32
//
//go:noinline
func shimTailCall(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).TailCall(base, pc, a, b, c)
}

// shimSelf: host.Self(base, pc, a, b, c) int32
//
//go:noinline
func shimSelf(ctx *jit.JITContext, base, pc, a, b, c int32) int32 {
	return hostFromCtx(ctx).Self(base, pc, a, b, c)
}

// shimClosure: host.Closure(base, pc, a, bx) int32
//
//go:noinline
func shimClosure(ctx *jit.JITContext, base, pc, a, bx int32) int32 {
	return hostFromCtx(ctx).Closure(base, pc, a, bx)
}

// shimClose: host.Close(base, pc, a) int32
//
//go:noinline
func shimClose(ctx *jit.JITContext, base, pc, a int32) int32 {
	return hostFromCtx(ctx).Close(base, pc, a)
}

// shimTForLoop: host.TForLoop(base, pc, a, c) int64
//
//go:noinline
func shimTForLoop(ctx *jit.JITContext, base, pc, a, c int32) int64 {
	return hostFromCtx(ctx).TForLoop(base, pc, a, c)
}

// -----------------------------------------------------------------------
// Arch-neutral shim address helpers. Each returns the entry PC of the
// corresponding shim by dereferencing the funcval pointer.
//
// These are defined here (in the shared file) rather than in emit_*.go
// so both amd64 and arm64 emit paths can reference the same names.
// -----------------------------------------------------------------------

func shimDoReturnAddr() uint64 {
	f := shimDoReturn
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimGetUpvalAddr() uint64 {
	f := shimGetUpval
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetUpvalFromRegAddr() uint64 {
	f := shimSetUpvalFromReg
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimArithAddr() uint64 {
	f := shimArith
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimUnmAddr() uint64 {
	f := shimUnm
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimLenAddr() uint64 {
	f := shimLen
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimConcatAddr() uint64 {
	f := shimConcat
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimEqAddr() uint64 {
	f := shimEq
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCompareAddr() uint64 {
	f := shimCompare
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimGetTableAddr() uint64 {
	f := shimGetTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetTableAddr() uint64 {
	f := shimSetTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimGetGlobalAddr() uint64 {
	f := shimGetGlobal
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetGlobalAddr() uint64 {
	f := shimSetGlobal
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimNewTableAddr() uint64 {
	f := shimNewTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetListAddr() uint64 {
	f := shimSetList
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimForPrepAddr() uint64 {
	f := shimForPrep
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCallAddr() uint64 {
	f := shimCall
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimTailCallAddr() uint64 {
	f := shimTailCall
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSelfAddr() uint64 {
	f := shimSelf
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimClosureAddr() uint64 {
	f := shimClosure
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCloseAddr() uint64 {
	f := shimClose
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimTForLoopAddr() uint64 {
	f := shimTForLoop
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}
