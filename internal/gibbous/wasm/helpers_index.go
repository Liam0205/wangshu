//go:build wangshu_p3

package wasm

import "github.com/Liam0205/wangshu/internal/value"

// Wasm function indices for imported helpers (02-translation §6.4).
//
// The gibbous module imports a set of Go helpers (env.h_*); imported funcs
// occupy the leading slots of the Wasm function index space (imports precede
// local funcs). These constants are each helper's index in the import list,
// and must stay **strictly consistent** with the import declaration order in
// module assembly (module.go).
//
// Helpers used by PW2 (straight-line opcodes GETUPVAL/SETUPVAL/RETURN):
const (
	helperGetUpval  uint32 = iota // env.h_getupval (base i32, b i32) -> (i64)
	helperSetUpval                // env.h_setupval (base i32, b i32, val i64) -> ()
	helperReturn                  // env.h_return   (base i32, pc i32, a i32, b i32) -> (i32)
	helperSafepoint               // env.h_safepoint(base i32, pc i32) -> ()  (for PW4 back edges; declared ahead)
	helperArith                   // host.h_arith   (base,pc,op,b,c,a i32) -> (i32 status)  PW3
	helperUnm                     // host.h_unm     (base,pc,b,a i32) -> (i32 status)        PW3
	helperLen                     // host.h_len     (base,pc,b,a i32) -> (i32 status)        PW3
	helperConcat                  // host.h_concat  (base,pc,a,b,c i32) -> (i32 status)      PW3
	helperCompare                 // host.h_compare (base,pc,op,b,c i32) -> (i32 packed)     PW4
	helperEq                      // host.h_eq      (base,pc,b,c i32) -> (i32 packed)        PW4
	helperForPrep                 // host.h_forprep (base,pc,a i32) -> (i32 status)          PW4
	helperGetTable                // host.h_gettable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperSetTable                // host.h_settable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperGetGlobal               // host.h_getglobal(base,pc,a,bx i32) -> (i32 status)      PW5
	helperSetGlobal               // host.h_setglobal(base,pc,a,bx i32) -> (i32 status)      PW5
	helperSelf                    // host.h_self     (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperNewTable                // host.h_newtable (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperSetList                 // host.h_setlist  (base,pc,a,b,c i32) -> (i32 status)     PW5
	helperCall                    // host.h_call     (base,pc,a,b,c i32) -> (i64 newbase/-1) PW6
	helperTailCall                // host.h_tailcall (base,pc,a,b,c i32) -> (i32 0/1/2)       PW6
	helperClosure                 // host.h_closure  (base,pc,a,bx i32) -> (i32 status)       PW7
	helperClose                   // host.h_close    (base,pc,a i32) -> (i32 status)          PW7
	helperTForLoop                // host.h_tforloop (base,pc,a,c i32) -> (i64 newbase/-1/-2)  PW4b
	helperCallErr                 // host.h_callerr  () -> ()  PW10 R3: pop leftover gibbous frames after a failed call_indirect direct call
	numHelpers
)

// NaN-box raw u64 helpers: for burning immediates at compile time (translate.go).
func nilRawU64() uint64   { return uint64(value.Nil) }
func trueRawU64() uint64  { return uint64(value.True) }
func falseRawU64() uint64 { return uint64(value.False) }

// tagShift / tag constants: tag extraction for NOT/LEN (value.Tag = v>>48).
const (
	tagShiftBits = 48
	tagStringU64 = uint64(value.TagString) // 0xFFFB
	tagTableU64  = uint64(value.TagTable)  // 0xFFFC
)

// qNanBoxBase is the IsNumber decision boundary (value.IsNumber: v < 0xFFF8...).
const qNanBoxBase uint64 = 0xFFF8_0000_0000_0000

// canonNaNU64 is the canonical NaN (the value package's canonicalize target, 01 §3.4).
const canonNaNU64 uint64 = 0x7FF8_0000_0000_0000

// Constants for PW5 table IC inlining.
const (
	// payloadMaskU64 = the low-48-bit mask of GCRefOf (value.go payloadMask) --
	// the low 48 bits of a NaN-box value are the object's arena byte offset (GCRef).
	payloadMaskU64 uint64 = 0x0000_FFFF_FFFF_FFFF

	// Table object field byte offsets (object/table.go layout: word_n offset=8*n).
	tblArrayOff = 16 // word2: arrayRef
	tblNodeOff  = 24 // word3: nodeRef
	tblGenOff   = 40 // word5: lastfree | gen (gen in the high 32 bits)
	// Node slot stride (3 words = 24 bytes); key=+0 val=+8 next=+16.
	nodeStrideBytes = 24
	nodeValOff      = 8
)

// CI segment frame layout constants (PW10 zero-crossing ③b: in-Wasm RETURN
// frame teardown reading the segment frame dynamically). Kept **strictly
// consistent** with crescent state.go's ciWords / writeCISeg / packCIWord2
// layout: from VS0-e substep ②, each frame is 5 words = 40 bytes (word4 =
// nVarargs mirror); word0[31:0]=base|[63:32]=funcIdx; word1[31:0]=top|[63:32]=pc;
// word2 low 32=protoID, [47:32]=nresults(int16), bit50=gibbous; word3=cl;
// word4[15:0]=nVarargs. Frame byte address =
// load(ciSegBaseAddr) + depth*ciFrameBytes + word*8.
const (
	ciFrameBytes  = 40 // ciWords(5) * 8; VS0-e substep ②: 4→5
	ciWord1Off    = 8  // word1: top | pc
	ciWord2Off    = 16 // word2: protoID | nresults | flags
	ciGibbousBit  = uint64(1) << 50
	maxReturnFast = 8 // ③b fast-path nret upper bound for unrolling moveResults (beyond it uses helperReturn)
)
