//go:build wangshu_p3

package wasm

// Table IC opcode translation (PW5, 02-translation §3.4, the peak of P3
// translation complexity).
//
// Core mechanism: **compile-time freezing of the IC snapshot** — at compile
// time we read Proto.IC[pc] to obtain (Kind, Shape=gen, Index, TableRef) and
// bake them into Wasm immediates; at runtime "same table, same generation
// (+ key match)" reaches the array/node slot directly, **skipping the hash**;
// on invalidation (rehash → gen bump / table swap / slot nil) it falls back to
// the helper (full lookup + metamethods, byte-equal, 06 §1/§2).
//
// Iron rule (06 §1): the fast-path checks are semantic dispatch, not
// speculative guards — the helper always sits at the end of the block as a
// fallback, so any check that fails drops into the helper and still yields the
// correct result, with zero deopt.
//
// Inline addressing of the table object (object/table.go layout,
// arena = linear memory, GCRef = byte offset):
//   taddr = GCRefOf(tbl) = the low 48 bits of the value; gen = high 32 bits of
//   word5 (offset40); nodeRef = word3 (offset24); array = word2 (offset16);
//   node slot has a 24-byte stride: key=+0 val=+8 next=+16; array slot =
//   arrayRef + idx*8.

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// icSnapshot is the compile-time-frozen IC snapshot (read race-tolerantly from
// Proto.IC[pc]).
type icSnapshot struct {
	kind     uint8
	gen      uint32 // Shape
	index    uint32 // Index (array subscript / node slot number)
	tableRef uint32 // TableRef (low 32 bits of the target table's arena offset)
}

// snapshotICSlot reads Proto.IC[pc] race-tolerantly (06 §4.3.1). Under
// concurrent multi-State access P1 may still be writing IC; reading a mix of
// old and new does not blow up — a runtime check failure falls back to the
// helper.
func snapshotICSlot(proto *bytecode.Proto, pc int32) icSnapshot {
	if int(pc) >= len(proto.IC) {
		return icSnapshot{}
	}
	slot := &proto.IC[pc]
	return icSnapshot{
		kind:     slot.Kind, // atomic byte read on the aligned uint8
		gen:      atomic.LoadUint32(&slot.Shape),
		index:    atomic.LoadUint32(&slot.Index),
		tableRef: atomic.LoadUint32(&slot.TableRef),
	}
}

// emitGenCheck emits the gen check condition: `(word5 >> 32) as i32 ==
// SNAP_GEN`. taddrConst is the table byte address as an i32 immediate. The
// result i32 is left on top of the stack (for ifVoid).
func (c *Compiler) emitGenCheck(em *emitter, taddrConst int32, snapGen uint32) {
	em.i32Const(taddrConst)
	em.i64Load(tblGenOff) // word5
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(int32(snapGen))
	em.i32Eq()
}

// emitHelperEpilogue5 emits the table IC slow-path helper call + error
// bubbling:
//
//	(local.set $st (call helper(base, pc, a, b, c)))
//	(if (i32.eq $st 1) (then (return 1)))
func (c *Compiler) emitHelperEpilogue5(em *emitter, helper uint32, pc, a, b, cc int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helper)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitHelperEpilogue4 is the same but with 4 args (getglobal/setglobal:
// base, pc, a, bx).
func (c *Compiler) emitHelperEpilogue4(em *emitter, helper uint32, pc, a, bx int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(bx)
	em.call(helper)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitGetGlobal GETGLOBAL A Bx —— R(A) := Gtable[K(Bx)] (02 §3.4.4).
//
// The globals table is constant (its address is baked as an immediate at
// compile time); the key is the constant K(Bx), constant for the same pc →
// the key check is skipped; globals is always NodeHit (asize=0). Inline: gen
// check → fetch node slot value → store if non-nil.
//
//	block $done
//	  if (gen == SNAP_GEN)
//	    $vb := i64.load[nodeRef + SNAP_INDEX*24+8]
//	    if ($vb != nil) { R(A) := $vb; br 2 }
//	  end
//	  <helper h_getglobal>(gen miss / slot nil)
//	end
func (c *Compiler) emitGetGlobal(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	snap := snapshotICSlot(proto, pc)
	taddr := int32(c.host.GlobalsRaw() & payloadMaskU64) // globals byte address (GCRef)

	// Inline only when the NodeHit snapshot is trustworthy; otherwise pure
	// helper (equivalent to having no IC, 06 §3).
	if snap.kind != bytecode.ICKindNodeHit {
		c.emitHelperEpilogue4(em, helperGetGlobal, pc, a, bx)
		return
	}

	valOff := snap.index*nodeStrideBytes + nodeValOff

	em.block() // $done
	c.emitGenCheck(em, taddr, snap.gen)
	em.ifVoid()
	// node base = wrap(i64.load[taddr+24])
	em.i32Const(taddr)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	em.localSet(localI32b)
	// $vb = i64.load[nodeRef + valOff]
	em.localGet(localI32b)
	em.i64Load(valOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.ifVoid()
	// R(A) := $vb; br 2 (jump out of $done, skipping the helper)
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(2)
	em.end() // if vb!=nil
	em.end() // if gen
	// slow path
	c.emitHelperEpilogue4(em, helperGetGlobal, pc, a, bx)
	em.end() // block $done
}

// emitSetGlobal SETGLOBAL A Bx —— Gtable[K(Bx)] := R(A) (02 §3.4.4).
//
// Fast path for modifying an existing key (changing the value does not bump
// gen, so the IC stays valid). Inline: gen check → current slot val non-nil
// (key exists) → write the new value. Delete / add key / gen miss → helper.
//
//	block $done
//	  if (gen == SNAP_GEN)
//	    $i32b := node base; if (i64.load[base+valOff] != nil)
//	      { i64.store[base+valOff] := R(A); br 2 }
//	  end
//	  <helper h_setglobal>
//	end
func (c *Compiler) emitSetGlobal(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	snap := snapshotICSlot(proto, pc)
	taddr := int32(c.host.GlobalsRaw() & payloadMaskU64)

	if snap.kind != bytecode.ICKindNodeHit {
		c.emitHelperEpilogue4(em, helperSetGlobal, pc, a, bx)
		return
	}

	valOff := snap.index*nodeStrideBytes + nodeValOff

	em.block() // $done
	c.emitGenCheck(em, taddr, snap.gen)
	em.ifVoid()
	// node base
	em.i32Const(taddr)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	em.localSet(localI32b)
	// current slot val != nil (key already exists)
	em.localGet(localI32b)
	em.i64Load(valOff)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.ifVoid()
	// i64.store[base+valOff] := R(A); br 2
	em.localGet(localI32b)
	em.localGet(localBase)
	em.i64Load(8 * uint32(a))
	em.i64Store(valOff)
	em.br(2)
	em.end() // if val!=nil
	em.end() // if gen
	c.emitHelperEpilogue4(em, helperSetGlobal, pc, a, bx)
	em.end() // block $done
}

// --- GETTABLE / SETTABLE (PW5-b, dynamic key matching) ---
//
// Control structure (constant br depth, avoiding deep-nesting counting):
//
//	block $done        ; depth 1 (success: skip the helper)
//	  block $slow      ; depth 0 (give up inline: fall to the helper)
//	    <table guard + key match + slot non-nil; any failure br_if 0 → $slow>
//	    <hit: store / load; br 1 → $done>
//	  end ; $slow
//	  <helper>
//	end ; $done
//
// Inline only covers byte-equal-provable forms: ① constant key (same table,
// same gen ⟹ the cached Index still maps to the same key, skipping the key
// match, same as GETGLOBAL); ② register key + ArrayHit (numeric f64 ==
// Index+1). Register key + NodeHit (normKey/keyEqual inline is fragile) /
// MonoMeta → pure helper.

// tableInlineable decides whether a given table IC point takes the inline fast
// path (otherwise pure helper). regKey=true means the key is a register
// (dynamic), false means a constant key.
func tableInlineable(snap icSnapshot, regKey bool) bool {
	switch snap.kind {
	case bytecode.ICKindArrayHit:
		return true // constant key skips matching; register key uses numeric match
	case bytecode.ICKindNodeHit:
		return !regKey // constant key only (register key NodeHit goes to helper)
	default:
		return false // None / MonoMeta / Mega
	}
}

// emitTableGuard emits the table guard: IsTable + same TableRef + same gen
// (any failure br_if 0). The stack is empty on entry; on exit:
// localI64c = table value (consumed), localI32b = table byte address
// (taddr, i32). Precondition: must be called inside block $slow (depth 0).
func (c *Compiler) emitTableGuard(em *emitter, regB int, snap icSnapshot) {
	// vt := R(B); localI64c = vt; localI32b = taddr (low 48 bits wrapped to i32)
	em.localGet(localBase)
	em.i64Load(8 * uint32(regB))
	em.localTee(localI64c)
	em.i64Const(payloadMaskU64)
	em.i64And()
	em.i32WrapI64()
	em.localSet(localI32b)
	// IsTable: (vt >> 48) == TagTable
	em.localGet(localI64c)
	em.i64Const(48)
	em.i64ShrU()
	em.i64Const(uint64(tagTableU64))
	em.i64Eq()
	em.i32Eqz()
	em.brIf(0) // → $slow
	// same TableRef: taddr(i32) == SNAP_TABLEREF
	em.localGet(localI32b)
	em.i32Const(int32(snap.tableRef))
	em.i32Eq()
	em.i32Eqz()
	em.brIf(0)
	// same gen: (i64.load[taddr+40] >> 32) wrap == SNAP_GEN
	em.localGet(localI32b)
	em.i64Load(tblGenOff)
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(int32(snap.gen))
	em.i32Eq()
	em.i32Eqz()
	em.brIf(0)
}

// emitArrayKeyMatch emits the numeric match for a register-key ArrayHit:
// IsNumber(key) and f64(key) == Index+1 (an arrayIndex hit ⟺ integer key ==
// Index+1). On failure br_if 0 → $slow.
func (c *Compiler) emitArrayKeyMatch(em *emitter, regC int, snap icSnapshot) {
	em.localGet(localBase)
	em.i64Load(8 * uint32(regC))
	em.localTee(localI64c)
	// IsNumber: key < qNanBoxBase
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32Eqz()
	em.brIf(0)
	// f64(key) == Index+1
	em.localGet(localI64c)
	em.f64ReinterpretI64()
	em.f64Const(float64(snap.index) + 1)
	em.f64Eq()
	em.i32Eqz()
	em.brIf(0)
}

// emitSlotAddr pushes the byte address of the hit slot onto the stack (i32):
//
//	ArrayHit: wrap(i64.load[taddr+16]) + Index*8
//	NodeHit:  wrap(i64.load[taddr+24]) + (Index*24+8)
//
// Returns the byte offset of that slot relative to the attached-block base
// address (used as the i64.load/store immediate), with the base on top of the
// stack. Implementation: push the base i32; the caller uses offset as the
// i64.load/store immediate.
func (c *Compiler) emitSlotBase(em *emitter, snap icSnapshot) uint32 {
	if snap.kind == bytecode.ICKindArrayHit {
		em.localGet(localI32b)
		em.i64Load(tblArrayOff)
		em.i32WrapI64()
		return snap.index * 8
	}
	// NodeHit
	em.localGet(localI32b)
	em.i64Load(tblNodeOff)
	em.i32WrapI64()
	return snap.index*nodeStrideBytes + nodeValOff
}

// emitGetTable GETTABLE A B C —— R(A) := R(B)[RK(C)] (02 §3.4.2).
func (c *Compiler) emitGetTable(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(cc))

	if !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperGetTable, pc, a, b, cc)
		return
	}

	em.block() // $done
	em.block() // $slow
	c.emitTableGuard(em, int(b), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(cc), snap)
	}
	// slot value → localI64c; non-nil check
	slotOff := c.emitSlotBase(em, snap)
	em.i64Load(slotOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // slot nil → $slow (possibly __index)
	// hit: R(A) := localI64c; br $done
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperGetTable, pc, a, b, cc)
	em.end() // $done
}

// emitSetTable SETTABLE A B C —— R(A)[RK(B)] := RK(C) (02 §3.4.3).
// Fast path for modifying an existing key (changing the value does not bump
// gen). Delete (val nil) / add / string-constant value → helper.
func (c *Compiler) emitSetTable(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(b))

	// The value RK(C) being a string constant → cannot bake a real GCRef at
	// compile time → drop the whole instruction to the helper.
	valStrConst := bytecode.IsK(int(cc)) && proto.IsStringConst(bytecode.KIdx(int(cc)))
	if valStrConst || !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperSetTable, pc, a, b, cc)
		return
	}

	em.block() // $done
	em.block() // $slow
	// Value non-nil (setting nil = delete → helper); load the value into
	// localI64a first (to avoid the guard overwriting localI64c).
	c.loadRK(em, proto, int(cc))
	em.localTee(localI64a)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // val nil → $slow
	c.emitTableGuard(em, int(a), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(b), snap)
	}
	// Current slot non-nil (key already exists, change-value fast path); push
	// slotBase onto the stack, kept as the base address for the later store.
	slotOff := c.emitSlotBase(em, snap)
	em.localTee(localI32b) // slot attached-block base → localI32b (reuse: taddr no longer needed after guard)
	em.i64Load(slotOff)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // current slot nil → $slow (adds a key, may rehash)
	// hit: slot := val (localI64a); br $done
	em.localGet(localI32b)
	em.localGet(localI64a)
	em.i64Store(slotOff)
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperSetTable, pc, a, b, cc)
	em.end() // $done
}

// emitSelf SELF A B C —— R(A+1) := R(B); R(A) := R(B)[RK(C)] (02 §3.4.5).
//
// The self transfer R(A+1):=R(B) must precede the IC lookup (execute.go:136);
// A+1≠B, so there is no conflict. The IC lookup is isomorphic to GETTABLE;
// miss → h_self (the helper performs the self transfer, idempotent). The
// method-name key is usually a string constant → NodeHit const-key (skips key
// matching).
func (c *Compiler) emitSelf(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	snap := snapshotICSlot(proto, pc)
	regKey := !bytecode.IsK(int(cc))

	// ① self transfer: R(A+1) := R(B) (unconditional, precedes the IC).
	em.localGet(localBase)
	em.localGet(localBase)
	em.i64Load(8 * uint32(b))
	em.i64Store(8 * uint32(a+1))

	if !tableInlineable(snap, regKey) {
		c.emitHelperEpilogue5(em, helperSelf, pc, a, b, cc)
		return
	}

	// ② R(A) := R(B)[RK(C)] (isomorphic to GETTABLE).
	em.block() // $done
	em.block() // $slow
	c.emitTableGuard(em, int(b), snap)
	if regKey && snap.kind == bytecode.ICKindArrayHit {
		c.emitArrayKeyMatch(em, int(cc), snap)
	}
	slotOff := c.emitSlotBase(em, snap)
	em.i64Load(slotOff)
	em.localTee(localI64c)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.i32Eqz()
	em.brIf(0) // slot nil → $slow
	em.localGet(localBase)
	em.localGet(localI64c)
	em.i64Store(8 * uint32(a))
	em.br(1) // → $done
	em.end() // $slow
	c.emitHelperEpilogue5(em, helperSelf, pc, a, b, cc)
	em.end() // $done
}

// --- NEWTABLE / SETLIST (PW5-d, no IC, pure helper) ---
//
// gibbous code itself never allocates (00-overview §8); allocation + GC +
// bulk write + possible rehash all complete synchronously inside the imported
// helper, GC is done by the time the helper returns, and the Wasm side is
// unaware. Inline translation is complex and low-yield (table construction is
// not the body of a hot loop) → the helper is simplest (02 §3.4.6/§3.4.7).

// emitNewTable NEWTABLE A B C —— R(A) := {} (via h_newtable; allocation +
// setReg + safepoint all inside the helper).
func (c *Compiler) emitNewTable(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	c.emitHelperEpilogue5(em, helperNewTable, pc, a, b, cc)
}

// emitSetList SETLIST A B C —— table-construction bulk array fill (via
// h_setlist; C=0 takes the next instruction as the large batch count, the
// helper fetches Proto.Code[pc] itself).
func (c *Compiler) emitSetList(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	c.emitHelperEpilogue5(em, helperSetList, pc, a, b, cc)
}

// --- CALL (PW6-a three-way dispatch + PW10 R3 call_indirect direct call) ---
//
// CALL A B C —— R(A)(R(A+1..A+B-1)), returns written back to R(A..A+C-2). Via
// h_call (DoCall) three-state dispatch (04-trampoline §3 + PW10 R3):
//   - < 0 (-1): error → self return 1 (status chain bubbles up, §4.1);
//   - even (multiple of 8): done —— host/crescent/__call/no-slot gibbous has
//     already run synchronously; the value = the refreshed byte offset of this
//     frame's base (PW6 base refresh), local.set $base to continue;
//   - odd (slot<<1)|1: indirect —— the callee is a gibbous-with-slot, DoCall
//     has pushed the frame + written the callee frame base into the transfer
//     word. This frame uses slot to reach the callee's run directly across
//     modules via `call_indirect <typeEntry> table0` (avoiding the ~143ns
//     double cross-layer re-entry of code.Run, the core PW10 R3 gain). The
//     callee run returns status: ≠0 ⟹ call h_callerr to pop leftover frames +
//     return 1; =0 ⟹ read this frame's refreshed base from the transfer word
//     (DoReturn has written it) and continue.
//
// **base refresh**: the callee (fallback synchronous path / the R3 direct-call
// path's DoReturn) may growStack, relocating the value-stack segment within the
// arena, invalidating this frame's $base. Both paths refresh base before
// continuing, avoiding a stale-base UAF.
func (c *Compiler) emitCall(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	xfer := c.host.CITransferAddr()

	// PW10 zero-cross ④ guarded fast path (④-ii will fill in the guards + frame
	// build body): only fixed-arity (B≠0 and C≠0) attempts a Wasm-internal frame
	// build + call_indirect to avoid the h_call cross-boundary. Any guard failure
	// br $slow → fallthrough to the R3 indirect slow path below (zero-behavior-
	// change fallback). At the ④-i stage the block body is empty; all guards pass
	// (empty) means fallthrough, behaving exactly like taking the slow path
	// directly —— a pure structural placeholder, ready for ④-ii to fill in the
	// guards (tag/host/slot/vararg/needsArg/arity/MaxStack headroom) + fast body
	// (segment-frame 4-word write + ciDepth++ + top word + call_indirect + error
	// handling + fastCallHits++).
	_ = proto // ④-ii uses proto.MaxStack as the caller frame headroom compile-time constant
	fastEligible := b != 0 && cc != 0
	if fastEligible {
		em.block() // $slow: guard-failure landing point; the block tail is followed by the slow path
		// ④-ii: emitCallFast(em, proto, a, b-1, cc-1) is emitted here
		em.end() // $slow block tail, fallthrough to the R3 indirect below
	}

	// ret = h_call(base,pc,a,b,c)
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helperCall)
	em.localTee(localI64c)
	// ret < 0 → error bubble
	em.i64Const(0)
	em.i64LtS()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// odd/even dispatch: ret & 1 (odd = indirect / even = done)
	em.localGet(localI64c)
	em.i64Const(1)
	em.i64And()
	em.i32WrapI64()
	em.ifVoid()
	// --- odd: call_indirect direct call ---
	// argument calleeBase = i32.load(xfer) (the callee frame base DoCall wrote)
	em.i32Const(0)
	em.i32Load(xfer)
	// table index slot = i32.wrap(ret >> 1)
	em.localGet(localI64c)
	em.i64Const(1)
	em.i64ShrU()
	em.i32WrapI64()
	em.callIndirect(typeEntry, 0)
	// status = the callee run's return value; ≠0 → pop leftover frames + bubble
	em.localTee(localI32)
	em.ifVoid()
	em.call(helperCallErr) // h_callerr: pop leftover gibbous callee frames
	em.i32Const(1)
	em.ret()
	em.end()
	// OK: refresh this frame's base = i32.load(xfer) (DoReturn wrote the refreshed caller base)
	em.i32Const(0)
	em.i32Load(xfer)
	em.localSet(localBase)
	// PW10 zero-cross ③a: caller self-restores top (only fixed-arity C≠0). After
	// the direct call returns, th.top must return to this frame's base+MaxStack
	// (localSavedTop snapshot, the slot index is invariant under grow). At the
	// ③a stage the callee's helperReturn has already restored the same value
	// (idempotent); when the ③b callee fast path no longer restores, this
	// write-back becomes the sole restore point. C==0 (multiple values to top)
	// does not write —— DoReturn has set top at the end of the multiple values,
	// and writing would corrupt it.
	//   (i32.store offset=topAddr (i32.const 0) (local.get $savedTop))
	if cc != 0 {
		em.i32Const(0)
		em.localGet(localSavedTop)
		em.i32Store(c.host.TopAddr())
	}
	em.elseOp()
	// --- even: done (fallback ran synchronously) refresh base = i32.wrap(ret) ---
	em.localGet(localI64c)
	em.i32WrapI64()
	em.localSet(localBase)
	// PW10 zero-cross ③b: the done arm also self-restores top (only fixed-arity
	// C≠0). The done arm is reachable when "the caller is gibbous, the callee has
	// no slot and ran synchronously via code.Run" —— in that case the callee's
	// emitReturn fast path (③b) goes via G2 (caller gibbous) and skips the top
	// restore → F must self-restore here. Under ③a, DoReturn has already restored
	// the same value (idempotent); the two arms are symmetric, sharing the same
	// savedTop snapshot as the OK arm.
	if cc != 0 {
		em.i32Const(0)
		em.localGet(localSavedTop)
		em.i32Store(c.host.TopAddr())
	}
	em.end()
}

// emitTailCall TAILCALL A B C —— tail call reuses the frame (PW6-b, 02 §3.6.2).
//
//	(local.set $i32 (call h_tailcall(base,pc,a,b,c)))
//	(if (i32.eq $i32 1) (then (return 1)))      ;; ERR bubbles up
//	(if (i32.eq $i32 2) (then                   ;; host tail call: emit trailing RETURN
//	      (return (call h_return(base,pc,a,0))) ))
//	(return 0)                                  ;; Lua tail call done, return directly
//
// status three states (gibbous_host.TailCall): 0=Lua tail call done / 1=ERR /
// 2=host (emit RETURN). TAILCALL is a BB-terminating instruction (no successor),
// carries its own return, so emitTailCall is self-closing (it does not depend on
// a trailing RETURN instruction —— the host path calls h_return itself).
func (c *Compiler) emitTailCall(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	cc := int32(bytecode.C(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.i32Const(cc)
	em.call(helperTailCall)
	em.localSet(localI32)
	// status==1 → return 1 (ERR)
	em.localGet(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// status==2 → host tail call: emit trailing RETURN A 0 (to top), return its status
	em.localGet(localI32)
	em.i32Const(2)
	em.i32Eq()
	em.ifVoid()
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(0) // B=0: return R(A..top) (host result multi-value window)
	em.call(helperReturn)
	em.ret()
	em.end()
	// status==0 → Lua tail call done, return 0 directly
	em.i32Const(0)
	em.ret()
}
