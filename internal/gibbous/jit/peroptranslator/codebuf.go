//go:build wangshu_p4

// codebuf.go — two-pass assembler for PJ10 native. Emit phase records
// jmp/jcc/call rel32 fixups + BB label bindings, resolve phase patches
// each rel32 with the actual displacement.
//
// This is the classic label resolver pattern documented in doc §7.
// PJ10's translator emits BB by BB (rPostOrder); when it sees a JMP or a
// terminator with a target BB, it emits the instruction with a 32-bit
// zero placeholder and records the placeholder offset + target BB in
// fixups. Once all BBs are bound (their entry offsets are known via
// bindLabel), resolveLabels walks fixups and patches each with the
// signed 32-bit displacement from the end of the fixup instruction to
// the target BB's entry offset.
package peroptranslator

import (
	"encoding/binary"
	"fmt"
)

// codeBuf is the growing byte buffer + BB label + fixup state.
type codeBuf struct {
	bytes []byte

	// labels[bbID] = byte offset within `bytes` where BB bbID starts.
	// -1 means unbound (bbID has not yet been emitted).
	labels []int32

	// fixups records (patchOff, srcInstrEnd, targetBB) triples:
	//   patchOff:     offset within bytes where the 4-byte rel32 lives
	//   srcInstrEnd:  offset within bytes just past the emitted instr,
	//                 used as the origin for rel32 arithmetic
	//   targetBB:     BB whose entry offset is the displacement's target
	fixups []fixup

	// proto is the Proto being translated. Emit functions consult it for
	// K(Bx) values (LOADK), string constants, and other compile-time
	// data. May be nil for byte-level unit tests. Stored as a raw
	// pointer to avoid a bytecode import cycle in this file (cfg.go
	// already imports bytecode, so the concrete field can migrate here
	// later if the split becomes friction).
	proto *codeBufProto
}

// codeBufProto is a minimal shim holding the compile-time data an emit
// function may need. Populated by TranslateProtoNative before emit.
type codeBufProto struct {
	// Consts are the raw NaN-boxed constant values (Proto.Consts as
	// uint64). Emit_amd64.go's emitLOADK reads Consts[Bx].
	Consts []uint64

	// RetA, RetB, RetPC are captured from the sole RETURN instruction
	// during emit and lifted into nativeCode by TranslateProtoNative.
	// The mmap segment RETs with status 0 for the normal exit, and
	// nativeCode.Run's Go side calls host.DoReturn(RetPC, RetA, RetB)
	// to perform the frame teardown.
	RetA  int32
	RetB  int32
	RetPC int32
}

type fixup struct {
	patchOff    int32
	srcInstrEnd int32
	targetBB    int
}

// newCodeBuf constructs an empty buffer for a Proto with numBBs BBs.
func newCodeBuf(numBBs int) *codeBuf {
	labels := make([]int32, numBBs)
	for i := range labels {
		labels[i] = -1
	}
	return &codeBuf{labels: labels}
}

// pos returns the current byte offset (== len(bytes)).
func (b *codeBuf) pos() int32 { return int32(len(b.bytes)) }

// emit appends raw bytes to the buffer.
func (b *codeBuf) emit(p []byte) {
	b.bytes = append(b.bytes, p...)
}

// bindLabel records that BB bbID begins at the current position.
// It's an error to bind the same label twice — that would indicate a
// duplicate BB emit and the second binding would silently win.
func (b *codeBuf) bindLabel(bbID int) error {
	if bbID < 0 || bbID >= len(b.labels) {
		return fmt.Errorf("bindLabel: bbID %d out of range [0,%d)", bbID, len(b.labels))
	}
	if b.labels[bbID] != -1 {
		return fmt.Errorf("bindLabel: BB %d already bound at %d, retry at %d",
			bbID, b.labels[bbID], b.pos())
	}
	b.labels[bbID] = b.pos()
	return nil
}

// addFixup records a rel32 patch site to be resolved after all BBs bound.
// patchOff is where the 4 zero bytes are; srcInstrEnd is just past the
// jmp/jcc/call instruction (rel32 origin).
func (b *codeBuf) addFixup(patchOff, srcInstrEnd int32, targetBB int) {
	b.fixups = append(b.fixups, fixup{
		patchOff:    patchOff,
		srcInstrEnd: srcInstrEnd,
		targetBB:    targetBB,
	})
}

// resolveLabels walks fixups and patches each rel32 with the displacement
// from srcInstrEnd to labels[targetBB]. Errors if any target BB is unbound
// or the displacement doesn't fit in int32 (it always will for our sizes,
// but the check is cheap and catches emit bugs early).
func (b *codeBuf) resolveLabels() error {
	for i, fx := range b.fixups {
		if fx.targetBB < 0 || fx.targetBB >= len(b.labels) {
			return fmt.Errorf("resolveLabels: fixup %d targets BB %d out of range", i, fx.targetBB)
		}
		tgt := b.labels[fx.targetBB]
		if tgt == -1 {
			return fmt.Errorf("resolveLabels: fixup %d targets unbound BB %d", i, fx.targetBB)
		}
		disp64 := int64(tgt) - int64(fx.srcInstrEnd)
		if disp64 < -0x80000000 || disp64 > 0x7fffffff {
			return fmt.Errorf("resolveLabels: fixup %d displacement %d overflows int32", i, disp64)
		}
		disp32 := int32(disp64)
		binary.LittleEndian.PutUint32(b.bytes[fx.patchOff:fx.patchOff+4], uint32(disp32))
	}
	return nil
}
