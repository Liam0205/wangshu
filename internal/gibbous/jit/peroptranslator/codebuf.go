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

	"github.com/Liam0205/wangshu/internal/bytecode"
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

	// pendingResumeOffFixups tracks patch offsets that need to be
	// resolved to "the offset of the next emitted op" — used by
	// exit-reason ops (GETTABLE and friends) that need the mmap
	// segment to advertise where to resume. resolveResumeOffPending
	// patches them all at the start of the next emit call.
	pendingResumeOffFixups []int

	// proto is the Proto being translated. Emit functions consult it for
	// K(Bx) values (LOADK), string constants, and other compile-time
	// data. May be nil for byte-level unit tests. Stored as a raw
	// pointer to avoid a bytecode import cycle in this file (cfg.go
	// already imports bytecode, so the concrete field can migrate here
	// later if the split becomes friction).
	proto *codeBufProto
}

// markResumeOffFixup records that the 4 bytes at patchOff in cb.bytes
// hold an imm32 that will be patched to the offset of the resume
// entry the next linear op / terminator emits. Used by exit-reason
// emits (e.g. emitGetTableExitOnly) so the mmap segment's
// `mov dword [r15+resumeOffOff], imm32` writes the actual resume
// entry offset once we know it.
//
// Resolution happens in emitResumePreludeIfPending (called from
// emitLinearOp / emitTerminator) — that helper also emits the
// `mov rbx, [r15+vsBaseOff]` reload that the resume entry needs
// (dispatcher may have refreshed vsBase during arena grow), so
// patching must be paired with the reload emit. Do not add a
// standalone "resolve without prelude" path: it would leave the
// resume entry without an rbx refresh and corrupt state under
// concurrent arena grow.
func (b *codeBuf) markResumeOffFixup(patchOff int) {
	b.pendingResumeOffFixups = append(b.pendingResumeOffFixups, patchOff)
}

// codeBufProto is a minimal shim holding the compile-time data an emit
// function may need. Populated by TranslateProtoNative before emit.
type codeBufProto struct {
	// Consts are the raw NaN-boxed constant values (Proto.Consts as
	// uint64). Emit_amd64.go's emitLOADK reads Consts[Bx].
	Consts []uint64

	// IC is a snapshot of Proto.IC (per-pc inline cache slot). The
	// GETTABLE emit consults IC[pc].Kind / Shape / TableRef to decide
	// whether to emit an inline ArrayHit fast path (with shape+gen
	// guards + runtime-index array lookup + shim fallback). Empty
	// slice = "no IC data" (unit tests / non table-heavy Protos) →
	// emit falls back to the plain shim.
	IC []bytecode.ICSlot

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
	// kind selects how resolveLabels writes the displacement. Default
	// (fixupKindRel32Bytes) matches the amd64 rel32 form: a signed
	// 32-bit byte displacement from srcInstrEnd. arm64 uses the two
	// arm64-specific kinds below (word-scale + limited bit width).
	kind fixupKind
}

// fixupKind selects the displacement encoding for a fixup. Different
// architectures encode rel jumps differently:
//
//   - amd64 rel32: bytes, sign-extended, no shift (5-byte E9 or 6-byte
//     0F 8x sequence).
//   - arm64 B rel26: (target - srcInstrEnd) / 4 into bits 0-25 of the
//     4-byte instruction word at patchOff.
//   - arm64 B.cond / CBNZ rel19: (target - srcInstrEnd) / 4 into bits
//     5-23 of the 4-byte instruction word at patchOff.
//
// srcInstrEnd is always the byte just past the branch instruction (for
// arm64 it's patchOff+4). We keep that field for consistency across
// arches; arm64 always has 4-byte instructions.
type fixupKind uint8

const (
	fixupKindRel32Bytes fixupKind = 0 // amd64 rel32
	fixupKindArm64B26   fixupKind = 1 // arm64 B rel26 (unconditional branch)
	fixupKindArm64Cond  fixupKind = 2 // arm64 B.cond rel19
)

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
		kind:        fixupKindRel32Bytes,
	})
}

// addFixupKind records a fixup with an explicit encoding kind. Used by
// arm64 emitters which need word-scaled rel26 / rel19 displacements
// rather than the amd64 rel32 byte form.
func (b *codeBuf) addFixupKind(patchOff, srcInstrEnd int32, targetBB int, kind fixupKind) {
	b.fixups = append(b.fixups, fixup{
		patchOff:    patchOff,
		srcInstrEnd: srcInstrEnd,
		targetBB:    targetBB,
		kind:        kind,
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
		switch fx.kind {
		case fixupKindRel32Bytes:
			if disp64 < -0x80000000 || disp64 > 0x7fffffff {
				return fmt.Errorf("resolveLabels: fixup %d rel32 disp %d overflows int32", i, disp64)
			}
			binary.LittleEndian.PutUint32(b.bytes[fx.patchOff:fx.patchOff+4], uint32(int32(disp64)))
		case fixupKindArm64B26:
			// arm64 B: target = PC + imm26*4; PC = patchOff (start of
			// the B instruction). srcInstrEnd is patchOff+4. Use
			// patchOff as the arm64 branch PC: word_disp = (tgt - patchOff)/4.
			wordDisp := (int64(tgt) - int64(fx.patchOff)) / 4
			if wordDisp < -(1<<25) || wordDisp >= (1<<25) {
				return fmt.Errorf("resolveLabels: fixup %d arm64 rel26 disp %d out of range", i, wordDisp)
			}
			insn := binary.LittleEndian.Uint32(b.bytes[fx.patchOff : fx.patchOff+4])
			insn &= 0xFC000000
			insn |= uint32(wordDisp) & 0x03FFFFFF
			binary.LittleEndian.PutUint32(b.bytes[fx.patchOff:fx.patchOff+4], insn)
		case fixupKindArm64Cond:
			// arm64 B.cond / CBNZ: target = PC + imm19*4; PC = patchOff.
			wordDisp := (int64(tgt) - int64(fx.patchOff)) / 4
			if wordDisp < -(1<<18) || wordDisp >= (1<<18) {
				return fmt.Errorf("resolveLabels: fixup %d arm64 rel19 disp %d out of range", i, wordDisp)
			}
			insn := binary.LittleEndian.Uint32(b.bytes[fx.patchOff : fx.patchOff+4])
			insn &= 0xFF00001F
			insn |= (uint32(wordDisp) & 0x7FFFF) << 5
			binary.LittleEndian.PutUint32(b.bytes[fx.patchOff:fx.patchOff+4], insn)
		default:
			return fmt.Errorf("resolveLabels: fixup %d unknown kind %d", i, fx.kind)
		}
	}
	return nil
}
