package jit

// Math intrinsic kinds (issue #77). When a CALL site's inline cache
// observes that the callee is one of a small set of pure-numeric stdlib
// host functions (math.sqrt, math.floor, ...), the P4 native segment can
// emit the operation inline (SQRTSD / ROUNDSD / FSQRT / ...) instead of
// exiting the segment to run the Go host closure and re-entering. The
// kind is looked up at IC-populate time from the callee host closure's
// hostFnID and cached in CallIC.IntrinsicID; the segment guards callee
// identity + argument IsNumber, so a miss falls to the existing
// exit-reason CALL path (slower, never wrong).
//
// 0 (IntrinsicNone) means "not a recognized intrinsic". The values are a
// stable on-wire contract: they are packed into ObserveCallCallee's
// return (bits 56..63) and stored in a single IC byte, so do not
// renumber them without updating both sides.
const (
	IntrinsicNone  uint8 = 0
	IntrinsicSqrt  uint8 = 1
	IntrinsicFloor uint8 = 2
	IntrinsicCeil  uint8 = 3
	IntrinsicAbs   uint8 = 4
	IntrinsicMax   uint8 = 5
	IntrinsicMin   uint8 = 6
)

// IntrinsicArgCount returns the fixed number of arguments a math
// intrinsic consumes: 1 for the unary ops (sqrt/floor/ceil/abs), 2 for
// max/min. Returns 0 for IntrinsicNone or any unrecognized kind. The
// emitter uses this to decide whether a given CALL site's static B (arg
// count) shape can host the intrinsic fast path.
func IntrinsicArgCount(kind uint8) int {
	switch kind {
	case IntrinsicSqrt, IntrinsicFloor, IntrinsicCeil, IntrinsicAbs:
		return 1
	case IntrinsicMax, IntrinsicMin:
		return 2
	}
	return 0
}
