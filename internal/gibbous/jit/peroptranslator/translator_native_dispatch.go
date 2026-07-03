//go:build wangshu_p4 && (amd64 || arm64)

// translator_native_dispatch.go - arch-shared half of the PJ10 native
// exit-reason protocol (issue #38 / #37).
//
// Both the amd64 and arm64 nativeCode types share the same dispatcher:
// the mmap segment packs (helperCode, a, b, c, pc) into jitCtx.exitArg0,
// sets the segment status to ExitInlineHelper, and RETs; Run's Go-side
// loop unpacks the request, invokes the corresponding host method, and
// reenters the segment at codePage + resumeOff. Only the emit side (how
// the packing instructions are encoded) is arch-specific.
package peroptranslator

import (
	"sync/atomic"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// DispatchHelperCount counts exit-reason round trips handled by
// dispatchHelper across all nativeCode instances. White-box hit counter
// for prove-the-path assertions: a test that expects an op to ride the
// exit-reason protocol (e.g. arm64 GETUPVAL) checks this increments —
// "output is correct" alone can't distinguish the native path from an
// interpreter fallback.
var DispatchHelperCount atomic.Int64

// dispatchHelper handles a single ExitInlineHelper request from the
// mmap segment. Returns true on success (segment can be re-entered
// at resumeOff), false on error (host method raised → caller returns
// status=1).
//
// The exit-reason protocol packs into exitArg0:
//
//	bits  0..15 : helper code (jit.HelperXxx)
//	bits 16..23 : op arg A (0-255)
//	bits 24..32 : op arg B (0-511)
//	bits 33..41 : op arg C (0-511)
//	bits 42..63 : op pc (0..4M)
//
// Each helper unpacks the fields it needs; the pc field is present
// for all helpers because host methods use it to materialise ci.pc.
func (c *nativeCode) dispatchHelper(base int32) bool {
	DispatchHelperCount.Add(1)
	arg0 := c.jitCtx.ExitArg0()
	helperCode := arg0 & jit.HelperCodeMask
	a := int32((arg0 >> 16) & 0xFF)
	b := int32((arg0 >> 24) & 0x1FF)
	cc := int32((arg0 >> 33) & 0x1FF)
	pc := int32((arg0 >> 42) & 0x3FFFFF)
	switch helperCode {
	case jit.HelperGetTable:
		if st := c.host.GetTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperSetTable:
		if st := c.host.SetTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperNewTable:
		if st := c.host.NewTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperSetList:
		if st := c.host.SetList(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperGetUpval:
		// GETUPVAL A B: R(A) := U(B). Never raises.
		c.host.SetReg(a, c.host.GetUpval(base, b))
	case jit.HelperSetUpval:
		// SETUPVAL A B: U(B) := R(A). Never raises.
		c.host.SetUpvalFromReg(base, a, b)
	case jit.HelperUnm:
		if st := c.host.Unm(base, pc, b, a); st != 0 {
			return false
		}
	case jit.HelperCall:
		if st := c.host.CallBaseline(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperGetGlobal:
		// bx split across b (low 9) and c (high 9) slots.
		if st := c.host.DoGetGlobal(base, pc, a, b|(cc<<9)); st != 0 {
			return false
		}
	case jit.HelperSetGlobal:
		if st := c.host.DoSetGlobal(base, pc, a, b|(cc<<9)); st != 0 {
			return false
		}
	case jit.HelperArithSlow:
		// Arith guard-miss slow path (arm64 emit; amd64 keeps its
		// in-segment shim fallback). The packed fields carry (a, b, c);
		// the op is re-derived from proto.Code[pc] — it doesn't fit the
		// packing and the bytecode is immutable, so the lookup is exact.
		op := int32(bytecode.Op(c.proto.Code[pc]))
		if st := c.host.Arith(base, pc, op, b, cc, a); st != 0 {
			return false
		}
	case jit.HelperCompareSlow:
		// Compare guard-miss slow path (LT/LE with non-number operands:
		// string ordering / __lt / __le metamethods). host.Compare
		// returns packed bit0=result, bit1=err. The result is handed
		// back to the segment through exitArg0: the compare emit's
		// resume block reads it and branches to the exec/skip successor
		// (the branch decision must happen inside the segment — the
		// dispatcher has no notion of BB targets).
		op := int32(bytecode.Op(c.proto.Code[pc]))
		packed := c.host.Compare(base, pc, op, b, cc)
		if packed&2 != 0 {
			return false
		}
		c.jitCtx.SetExitArg0(uint64(packed & 1))
	default:
		return false
	}
	return true
}

// hostIfaceHeader extracts the (itab, data) header from a P4HostState
// interface value. Same pattern as e2e_shim_ops_amd64_test.go's
// hostToIfaceHeader but callable from production code.
func hostIfaceHeader(h jit.P4HostState) [2]uintptr {
	return *(*[2]uintptr)(unsafe.Pointer(&h))
}
