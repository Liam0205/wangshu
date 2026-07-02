//go:build wangshu_p4

// ic_snapshot.go - race-tolerant snapshot of a Proto's IC slice for
// compile-time consumption by emit paths (e.g. emitInlineGetTableArrayHit).
//
// P1 crescent may still be updating proto.IC concurrently while the
// compiler snapshots it. Reading each field via atomic loads keeps the
// race detector quiet without imposing any real synchronisation cost,
// and any stale read still falls through the runtime guards to the
// shim (byte-equal to the P1 slow path). Mirrors P3 wasm's
// snapshotICSlot pattern in internal/gibbous/wasm/translate_table.go.

package peroptranslator

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// snapshotProtoIC returns a copy of proto.IC read with per-field
// atomic loads. Returns nil when proto.IC is empty.
func snapshotProtoIC(proto *bytecode.Proto) []bytecode.ICSlot {
	if proto == nil || len(proto.IC) == 0 {
		return nil
	}
	out := make([]bytecode.ICSlot, len(proto.IC))
	for i := range proto.IC {
		src := &proto.IC[i]
		out[i] = bytecode.ICSlot{
			Shape:    atomic.LoadUint32(&src.Shape),
			Index:    atomic.LoadUint32(&src.Index),
			TableRef: atomic.LoadUint32(&src.TableRef),
			// Kind and Refill are uint8; single-byte reads are
			// naturally atomic on the platforms this project
			// targets (amd64 / arm64). No explicit atomic API
			// for byte-sized fields in sync/atomic yet.
			Kind:   src.Kind,
			Refill: src.Refill,
		}
	}
	return out
}
