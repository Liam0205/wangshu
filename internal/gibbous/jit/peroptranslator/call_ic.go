//go:build wangshu_p4

// call_ic.go — per-CALL-site inline cache (issue #50 Spike 1
// infrastructure).
//
// Each CALL bytecode in a Proto gets one CallIC slot indexed by pc. On
// first execution the exit-reason CALL dispatcher (translator_native_dispatch)
// records the callee proto meta (protoID + NumParams + MaxStack + Flags);
// subsequent executions can consult the slot from inside the mmap segment
// to gate an EmitCallInline fast path (Spike 2+).
//
// Spike 1 mission: land the plumbing (types + population + probes) with
// zero behavior change — emitCALL still lowers to the historical
// HelperCall exit-reason regardless of IC state, and Run's dispatcher
// simply fills the slot after invoking host.CallBaseline. Once the
// probes prove the slot gets populated on the call-heavy kernels, Spike
// 2 wires the segment-side guard + segment-side frame build.
package peroptranslator

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// CallIC is the per-CALL-site inline cache slot. Mono-only IC: a
// differing callee at the next call clears the slot (matches P3 wasm's
// shape-monomorphic policy). All heavy Lua benchmarks in scope
// (fib/binary-trees/fannkuch) issue each CALL against a stable callee.
//
// Fields are read/written with atomic ops so the mmap segment guard
// (Spike 2+) can inspect the slot under race with the Go-side dispatcher
// populate.
//
//   - CalleeProtoID: the callee Proto ID at the last known hit.
//     0 = unpopulated. Populated on first Hit; a subsequent miss with
//     a differing protoID clears it back to 0 (megamorphic → slow path
//     again forever, tracked via Flags).
//   - CalleeNumParams: proto.NumParams (0..255).
//   - CalleeMaxStack:  proto.MaxStack (0..255 in practice — Lua 5.1
//     luac never generates a Proto with MaxStack > 250 in benchmark
//     workloads, but we widen to 255 to keep the byte packing).
//   - Flags: CallICFlag* bits. FlagIsHost / FlagIsVararg / FlagNeedsArg
//     poison the slot. FlagStuck marks a shape-change past the budget
//     — the segment guard MUST always miss on Stuck.
type CallIC struct {
	CalleeProtoID   uint32
	CalleeNumParams uint8
	CalleeMaxStack  uint8
	Flags           uint8
	_               uint8

	// Hits: increments on every EmitCallInline fast-path hit
	// (Spike 2+). Currently unused (Spike 1 only populates the slot;
	// no fast path yet).
	Hits uint32
	// Misses: exit-reason path increments on shape change (differing
	// callee protoID) or host/vararg observation. Prove-the-path tests
	// assert Hits vs Misses distribution.
	Misses uint32
}

// Call-IC flag bits (single byte in Flags).
const (
	CallICFlagIsVararg uint8 = 1 << 0
	CallICFlagNeedsArg uint8 = 1 << 1
	CallICFlagIsHost   uint8 = 1 << 2
	// CallICFlagStuck marks the slot as permanently at the slow path:
	// host callee observed, or repeated shape change past the budget.
	CallICFlagStuck uint8 = 1 << 7
)

// callSitePCsFor walks proto.Code and returns pc values whose op is
// CALL. Used by TranslateProtoNative to size codeBufProto.CallICs with
// one slot per CALL site.
func callSitePCsFor(proto *bytecode.Proto) []int32 {
	if proto == nil || len(proto.Code) == 0 {
		return nil
	}
	var out []int32
	for pc := int32(0); pc < int32(len(proto.Code)); pc++ {
		if bytecode.Op(proto.Code[pc]) == bytecode.CALL {
			out = append(out, pc)
		}
	}
	return out
}

// PopulateCallIC records a Lua callee observation into the slot. Called
// from the exit-reason dispatcher after host.CallBaseline succeeds.
// Race-free with concurrent segment reads: all field writes go through
// atomic.Store; the segment guard reads via matching atomic.Load.
//
// Semantics:
//
//   - Slot empty (CalleeProtoID == 0): populate.
//   - Slot occupied with same protoID: no-op (monotonic).
//   - Slot occupied with different protoID: transition to Stuck (mono
//     IC — one shape change ends the fast-path budget).
//   - flagIsHost / flagIsVararg / flagNeedsArg: whichever caller
//     observed is OR'd; those callees can never fast-path.
//
// The dispatcher passes the actual observed protoID / meta; when the
// callee is a host closure it passes protoID=0 + flagIsHost. This keeps
// the API single-shape.
func (ic *CallIC) Populate(protoID uint32, numParams, maxStack, flags uint8) {
	// Host observation: mark Stuck and return.
	if flags&CallICFlagIsHost != 0 {
		atomic.StoreUint32(&ic.Misses, atomic.LoadUint32(&ic.Misses)+1)
		atomic.StoreUint32(&ic.CalleeProtoID, 0)
		storeByte(&ic.Flags, CallICFlagStuck|CallICFlagIsHost)
		return
	}
	prevID := atomic.LoadUint32(&ic.CalleeProtoID)
	prevFlags := loadByte(&ic.Flags)
	if prevFlags&CallICFlagStuck != 0 {
		// Already stuck — record miss for the probe, don't rewrite.
		atomic.StoreUint32(&ic.Misses, atomic.LoadUint32(&ic.Misses)+1)
		return
	}
	if prevID == 0 {
		// First observation: populate.
		ic.CalleeNumParams = numParams
		ic.CalleeMaxStack = maxStack
		storeByte(&ic.Flags, flags)
		atomic.StoreUint32(&ic.CalleeProtoID, protoID) // release: field values visible before ID.
		return
	}
	if prevID != protoID {
		// Shape change: transition to Stuck.
		atomic.StoreUint32(&ic.Misses, atomic.LoadUint32(&ic.Misses)+1)
		storeByte(&ic.Flags, prevFlags|CallICFlagStuck)
		atomic.StoreUint32(&ic.CalleeProtoID, 0)
		return
	}
	// Same shape: no-op (Spike 2+ increments Hits from the segment side).
}

// FlagsFromProto builds the flag byte from a Lua callee's proto meta.
// Never OR's IsHost — the caller distinguishes host vs Lua before
// calling Populate.
func FlagsFromProto(isVararg, needsArg bool) uint8 {
	var f uint8
	if isVararg {
		f |= CallICFlagIsVararg
	}
	if needsArg {
		f |= CallICFlagNeedsArg
	}
	return f
}

// storeByte / loadByte: aligned single-byte reads/writes are atomic on
// amd64/arm64 (the platforms this project targets); sync/atomic doesn't
// expose byte-sized primitives, so the go race detector treats these
// as races if we ever ran with -race across concurrent write and read.
// Segments (Spike 2+ readers) do a whole-uint32 load of the second
// 4-byte word (NumParams/MaxStack/Flags/pad) — the Flags byte is only
// read in Go, so a plain byte write is race-safe under the sequentially
// consistent Go memory model on those architectures.
func storeByte(p *uint8, v uint8) { *p = v }
func loadByte(p *uint8) uint8     { return *p }
