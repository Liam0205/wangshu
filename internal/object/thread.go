// Userdata and Thread object layouts (01 §5.5 / §5.6).
//
// Userdata:
//
//	word0: GCHeader (otype=USERDATA; flags bit0 = has metatable)
//	word1: [31:0] payloadLen | [63:32] reserved
//	word2: metaRef
//	word3: envRef
//	word4..: payload bytes (payloadLen, 8-byte aligned)
//
// Thread:
//
//	word0: GCHeader (otype=THREAD)
//	word1: status(8) | flags
//	word2: valueStackRef
//	word3: top | stackCap
//	word4: callInfoRef
//	word5: ciTop | ciCap
//	word6: openUpvalRef
//	word7: errorJmp / state-machine field
//	word8: resumeFrom
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// ----- Userdata -----

const (
	udLenIdx     = 1
	udMetaIdx    = 2
	udEnvIdx     = 3
	udPayloadIdx = 4

	udFlagHasMeta uint8 = 1 << 0
)

func userdataWords(payloadLen uint32) uint32 {
	return 4 + (payloadLen+7)/8
}

// maxUDPayload is the upper bound on userdata payload (size-entry check: guards
// against payloadLen+7 wraparound).
const maxUDPayload = uint32(1)<<31 - 64

func AllocUserdata(a *arena.Arena, payloadLen uint32) arena.GCRef {
	if payloadLen > maxUDPayload {
		panic("object: userdata payload too long")
	}
	ref := allocateRaw(a, OBJ_USERDATA, userdataWords(payloadLen), 0)
	setWordAt(a, ref, udLenIdx, uint64(payloadLen))
	setWordAt(a, ref, udMetaIdx, 0)
	setWordAt(a, ref, udEnvIdx, 0)
	return ref
}

func UserdataLen(a *arena.Arena, ud arena.GCRef) uint32 {
	return uint32(wordAt(a, ud, udLenIdx))
}

func UserdataMetaRef(a *arena.Arena, ud arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, ud, udMetaIdx))
}

func SetUserdataMeta(a *arena.Arena, ud, meta arena.GCRef) {
	setWordAt(a, ud, udMetaIdx, uint64(meta))
	h := HeaderOf(a, ud)
	flags := FlagsOf(h)
	if meta == 0 {
		flags &^= udFlagHasMeta
	} else {
		flags |= udFlagHasMeta
	}
	SetHeader(a, ud, SetFlags(h, flags))
}

func UserdataEnvRef(a *arena.Arena, ud arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, ud, udEnvIdx))
}

func SetUserdataEnv(a *arena.Arena, ud, env arena.GCRef) {
	setWordAt(a, ud, udEnvIdx, uint64(env))
}

func UserdataPayload(a *arena.Arena, ud arena.GCRef) []byte {
	n := UserdataLen(a, ud)
	if n == 0 {
		return nil
	}
	off := uint32(ud) + udPayloadIdx*8
	return a.Bytes()[off : off+n]
}

// ----- Thread -----

// ThreadStatus values (01 §5.6 word1).
type ThreadStatus uint8

const (
	StatusRunning   ThreadStatus = 0
	StatusSuspended ThreadStatus = 1
	StatusNormal    ThreadStatus = 2
	StatusDead      ThreadStatus = 3
)

const (
	threadStatusIdx     = 1
	threadValueStackIdx = 2
	threadTopCapIdx     = 3
	threadCallInfoIdx   = 4
	threadCITopCapIdx   = 5
	threadOpenUpvalIdx  = 6
	threadErrorJmpIdx   = 7
	threadResumeFromIdx = 8
	threadHeadWords     = 9
)

// AllocThread reserves a Thread head + initial value stack + initial CallInfo array.
// stackCap = number of Value slots; ciCap = number of CallInfo entries (each 4 words; see 05 §1.2).
func AllocThread(a *arena.Arena, stackCap, ciCap uint32) arena.GCRef {
	const ciWordsPerEntry = 4 // 05 §1.2
	ref := allocateRaw(a, OBJ_THREAD, threadHeadWords, 0)
	stackRef := a.AllocWords(stackCap)
	for i := uint32(0); i < stackCap; i++ {
		a.SetWordAt(stackRef+arena.GCRef(i*8), uint64(value.Nil))
	}
	ciRef := a.AllocWords(ciCap * ciWordsPerEntry)

	setWordAt(a, ref, threadStatusIdx, uint64(StatusSuspended))
	setWordAt(a, ref, threadValueStackIdx, uint64(stackRef))
	setWordAt(a, ref, threadTopCapIdx, uint64(stackCap)<<32) // top=0, stackCap in high 32
	setWordAt(a, ref, threadCallInfoIdx, uint64(ciRef))
	setWordAt(a, ref, threadCITopCapIdx, uint64(ciCap)<<32) // ciTop=0, ciCap in high 32
	setWordAt(a, ref, threadOpenUpvalIdx, 0)
	setWordAt(a, ref, threadErrorJmpIdx, 0)
	setWordAt(a, ref, threadResumeFromIdx, 0)
	return ref
}

func ThreadStatusOf(a *arena.Arena, th arena.GCRef) ThreadStatus {
	return ThreadStatus(wordAt(a, th, threadStatusIdx) & 0xFF)
}

func SetThreadStatus(a *arena.Arena, th arena.GCRef, s ThreadStatus) {
	w := wordAt(a, th, threadStatusIdx)
	setWordAt(a, th, threadStatusIdx, (w&^0xFF)|uint64(s))
}

func ThreadValueStackRef(a *arena.Arena, th arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, th, threadValueStackIdx))
}

func ThreadTop(a *arena.Arena, th arena.GCRef) uint32 {
	return uint32(wordAt(a, th, threadTopCapIdx))
}

func ThreadStackCap(a *arena.Arena, th arena.GCRef) uint32 {
	return uint32(wordAt(a, th, threadTopCapIdx) >> 32)
}

func SetThreadTop(a *arena.Arena, th arena.GCRef, top uint32) {
	w := wordAt(a, th, threadTopCapIdx)
	setWordAt(a, th, threadTopCapIdx, uint64(top)|(w&0xFFFFFFFF00000000))
}

func ThreadCallInfoRef(a *arena.Arena, th arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, th, threadCallInfoIdx))
}

func ThreadCITop(a *arena.Arena, th arena.GCRef) uint32 {
	return uint32(wordAt(a, th, threadCITopCapIdx))
}

func ThreadCICap(a *arena.Arena, th arena.GCRef) uint32 {
	return uint32(wordAt(a, th, threadCITopCapIdx) >> 32)
}

func SetThreadCITop(a *arena.Arena, th arena.GCRef, top uint32) {
	w := wordAt(a, th, threadCITopCapIdx)
	setWordAt(a, th, threadCITopCapIdx, uint64(top)|(w&0xFFFFFFFF00000000))
}

func ThreadOpenUpvalHead(a *arena.Arena, th arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, th, threadOpenUpvalIdx))
}

func SetThreadOpenUpvalHead(a *arena.Arena, th arena.GCRef, head arena.GCRef) {
	setWordAt(a, th, threadOpenUpvalIdx, uint64(head))
}

func ThreadResumeFrom(a *arena.Arena, th arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, th, threadResumeFromIdx))
}

func SetThreadResumeFrom(a *arena.Arena, th arena.GCRef, caller arena.GCRef) {
	setWordAt(a, th, threadResumeFromIdx, uint64(caller))
}

// ThreadValueStackAt / SetThreadValueStackAt: read/write the i-th slot of the value stack.
func ThreadValueStackAt(a *arena.Arena, th arena.GCRef, i uint32) value.Value {
	return value.Value(a.WordAt(ThreadValueStackRef(a, th) + arena.GCRef(i*8)))
}

func SetThreadValueStackAt(a *arena.Arena, th arena.GCRef, i uint32, v value.Value) {
	a.SetWordAt(ThreadValueStackRef(a, th)+arena.GCRef(i*8), uint64(v))
}
