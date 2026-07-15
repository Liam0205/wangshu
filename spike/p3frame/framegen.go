package p3frame

// PW10 "zero-crossing" milestone Stage 0 spike: validate the make-or-break
// hypothesis — whether Wasm-side frame build/teardown (segment-word writes +
// ciDepth word increment/decrement + maxOpenIdx guard) is genuinely faster
// than the current single h_call + single h_return (two host crossings),
// after subtracting the in-Wasm bookkeeping overhead.
//
// The two driver forms share the same leaf body + the same loop skeleton, and
// **differ only in how each call builds/tears down a frame**:
//
//   - inwasm  : call_indirect into leaf; frame build (write 4 segment words +
//     increment ciDepth) and teardown (read maxOpenIdx guard + decrement
//     ciDepth + reload) all happen inside Wasm, zero Go crossings on the hot
//     path. = the form this milestone must deliver.
//   - twocross: call h_call (imported Go, build) → call_indirect into leaf →
//     leaf returns → call h_return (imported Go, teardown). = the current
//     post-R3.5 gibbous→gibbous form (2 host crossings per call).
//
// The "frame build/teardown workload" of both forms is deliberately equivalent
// (both write the same 4 segment words + adjust ciDepth + run the maxOpenIdx
// guard); the difference is **only whether that work is done inside Wasm or
// crosses back to Go via the host** — isolating the cost of "the crossing
// itself". The leaf body is uniformly `leaf(x)=x*3+1` (same as p3indirect, so
// the dispatch baseline is comparable).
//
// Shared linear memory layout (env.memory; driver arg base = byte base of
// frame 0):
//   word @ ciDepthOff   : ciDepth (frame depth cursor)
//   word @ maxOpenOff   : maxOpenIdx (open-upvalue guard; the spike keeps it
//                         high so the fast path always passes)
//   segment @ segBase + depth*ciWords*8 : 4 words per frame (base/funcIdx/top/packed)
//
// Wasm binary format: https://webassembly.github.io/spec/core/binary/

const (
	ciWords        = 4      // 4 words per frame (matches production R2 segment layout)
	segBase        = 256    // CallInfo segment start byte offset (past the leading flag words)
	ciDepthOff     = 8      // ciDepth word byte offset
	maxOpenOff     = 16     // maxOpenIdx word byte offset
	segBaseWordOff = 24     // segBase mirror word byte offset (guarded form reads segment base from here at runtime)
	leafN          = 100000 // driver tight-loop leaf call count (amortizes the outer fn.Call)
)

// driverKind distinguishes the two forms.
type driverKind int

const (
	kindInwasm   driverKind = iota // frame build/teardown entirely inside Wasm
	kindTwocross                   // frame build/teardown via h_call/h_return, two host crossings
	kindGuarded                    // frame build/teardown entirely inside Wasm + real runtime guards (read segBase/maxOpen words + guard branches)
)

// buildFrameModule builds the spike module binary.
//
// Function index layout:
//
//	func 0 = imported host.h_call  (i32 depth)->(i32)     —— build frame (used by twocross)
//	func 1 = imported host.h_return(i32 depth)->()        —— teardown frame (used by twocross)
//	func 2 = leaf(x)=x*3+1
//	func 3 = driver_inwasm(base)
//	func 4 = driver_twocross(base)
//
// (Both drivers are compiled into the same module; both imported host funcs are
// always declared, the inwasm form just does not call them.)
func buildFrameModule() []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version

	// Type section:
	//   type0 (i32)->(i32)  leaf / driver / h_call
	//   type1 (i32)->()     h_return
	b = append(b, sec(0x01, concat(
		uleb(2),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0
		[]byte{0x60, 0x01, 0x7f, 0x00},       // type1
	))...)

	// Import section: env.memory + host.h_call(type0) + host.h_return(type1).
	b = append(b, sec(0x02, concat(
		uleb(3),
		importMemEntry("env", "memory"),
		importFuncEntry("host", "h_call", 0),
		importFuncEntry("host", "h_return", 1),
	))...)

	// Function section: leaf + driver_inwasm + driver_twocross + driver_guarded (all type0).
	b = append(b, sec(0x03, concat(uleb(4), uleb(0), uleb(0), uleb(0), uleb(0)))...)

	// Table section: 1 funcref table, min=1 (holds leaf for call_indirect).
	b = append(b, sec(0x04, concat(
		uleb(1), []byte{0x70}, []byte{0x00}, uleb(1),
	))...)

	// leaf func index = 2 (the 2 imported funcs take 0/1). driver_inwasm=3, driver_twocross=4, driver_guarded=5.
	const leafIdx = 2
	const driverInwasmIdx = 3
	const driverTwocrossIdx = 4
	const driverGuardedIdx = 5

	// Export section: three drivers.
	b = append(b, sec(0x07, concat(
		uleb(3),
		[]byte{0x0d}, []byte("driver_inwasm"), []byte{0x00}, uleb(driverInwasmIdx),
		[]byte{0x0f}, []byte("driver_twocross"), []byte{0x00}, uleb(driverTwocrossIdx),
		[]byte{0x0e}, []byte("driver_guarded"), []byte{0x00}, uleb(driverGuardedIdx),
	))...)

	// Element section: table[0] = leaf.
	b = append(b, sec(0x09, concat(
		uleb(1), []byte{0x00},
		[]byte{0x41, 0x00, 0x0b}, // offset (i32.const 0) end
		uleb(1), uleb(leafIdx),
	))...)

	// Code section: leaf + driver_inwasm + driver_twocross + driver_guarded.
	bodies := [][]byte{
		leafBody(),
		driverBody(kindInwasm),
		driverBody(kindTwocross),
		driverBody(kindGuarded),
	}
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)
	return b
}

// leafBody — leaf(x) = x*3 + 1 (same as p3indirect, pure arithmetic isolating dispatch cost).
func leafBody() []byte {
	return concat(
		[]byte{0x00},
		[]byte{
			0x20, 0x00, // local.get 0
			0x41, 0x03, // i32.const 3
			0x6c,       // i32.mul
			0x41, 0x01, // i32.const 1
			0x6a, // i32.add
		},
		[]byte{0x0b},
	)
}

// driverBody — driver(base) { acc=0; for n=leafN..1 { acc += <build frame><call leaf(n)><teardown frame> }; return acc }
//
// inwasm: frame build/teardown entirely inside Wasm (segment-word writes +
// ciDepth increment/decrement + maxOpenIdx guard).
// twocross: build calls h_call, teardown calls h_return (one host crossing each).
func driverBody(kind driverKind) []byte {
	// locals: $acc (local 1), $n (local 2). param $base = local 0.
	locals := []byte{0x01, 0x02, 0x7f} // 1 group of 2 i32

	// Initialize acc=0, n=leafN.
	pre := concat(
		[]byte{0x41, 0x00, 0x21, 0x01}, // i32.const 0; local.set $acc
		i32ConstSeq(leafN),             // i32.const leafN
		[]byte{0x21, 0x02},             // local.set $n
	)

	// Loop body: each iteration builds a frame → leaf(n) → tears down → acc += result.
	var buildSeq, teardownSeq []byte
	switch kind {
	case kindInwasm:
		buildSeq = inwasmBuild()
		teardownSeq = inwasmTeardown()
	case kindTwocross:
		buildSeq = twocrossBuild()
		teardownSeq = twocrossTeardown()
	case kindGuarded:
		buildSeq = guardedBuild()
		teardownSeq = guardedTeardown()
	}

	loop := concat(
		[]byte{0x02, 0x40, 0x03, 0x40},       // block $exit; loop $top
		[]byte{0x20, 0x02, 0x45, 0x0d, 0x01}, // local.get $n; i32.eqz; br_if $exit
		buildSeq,                             // build frame (stack-neutral)
		[]byte{
			0x20, 0x01, // local.get $acc
			0x20, 0x02, // local.get $n  (leaf arg)
			0x41, 0x00, // i32.const 0   (table index)
			0x11, 0x00, 0x00, // call_indirect type0 table0 → leaf(n)
			0x6a,       // i32.add (acc + leaf(n))
			0x21, 0x01, // local.set $acc
		},
		teardownSeq, // teardown frame (stack-neutral)
		[]byte{
			0x20, 0x02, 0x41, 0x01, 0x6b, 0x21, 0x02, // n = n - 1
			0x0c, 0x00, // br $top
			0x0b, 0x0b, // end loop; end block
			0x20, 0x01, // local.get $acc (return)
		},
	)
	return concat(locals, pre, loop, []byte{0x0b})
}

// --- inwasm form: frame build/teardown entirely inside Wasm ---

// inwasmBuild — build frame: depth = load(ciDepthOff); write 4 segment words @ segBase+depth*32;
// store(ciDepthOff, depth+1). Models the steady-state fast path of enterLuaFrame (no grow/vararg).
//
// Segment-word address = segBase + depth*ciWords*8. The spike uses a fixed depth (driver frame + leaf
// frame), so depth actually oscillates between 0 and 1; the address arithmetic is isomorphic to
// production (depth*32 + segBase).
func inwasmBuild() []byte {
	// Read depth onto the stack, compute segment base segBase+depth*32, write 4 i64 words (values are
	// constant placeholders, modeling the write cost of base/funcIdx/top/packed), then ciDepth++.
	// addr = segBase + depth*32
	return concat(
		// --- write segment words word0..3 (addr = segBase + depth*32 + w*8) ---
		segWordStore(0, 0x11), // word0 = 0x11 (models base|funcIdx)
		segWordStore(1, 0x22), // word1 = 0x22 (models top|pc)
		segWordStore(2, 0x33), // word2 = 0x33 (models packed flags)
		segWordStore(3, 0x44), // word3 = 0x44 (models cl)
		// --- ciDepth++ ---
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff (addr)
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff (pushed again as load addr)
		[]byte{0x28, 0x02, 0x00}, // i32.load align=2 offset=0 → depth
		[]byte{0x41, 0x01, 0x6a}, // i32.const 1; i32.add → depth+1
		[]byte{0x36, 0x02, 0x00}, // i32.store align=2 offset=0
	)
}

// inwasmTeardown — teardown frame: read maxOpenIdx guard (always passes in the spike) + ciDepth--.
// Models the steady-state fast path of DoReturn (no open upvalues, no multiple returns).
func inwasmTeardown() []byte {
	return concat(
		// --- maxOpenIdx guard: if load(maxOpenOff) != 0 then (here the spike keeps it 0 → not taken) ---
		// Read once to model the guard's i32.load cost (result dropped, the spike does not really branch).
		[]byte{0x41, maxOpenOff}, // i32.const maxOpenOff
		[]byte{0x28, 0x02, 0x00}, // i32.load
		[]byte{0x1a},             // drop (guard read cost, always passes without branching)
		// --- ciDepth-- ---
		[]byte{0x41, ciDepthOff}, // addr
		[]byte{0x41, ciDepthOff}, // load addr
		[]byte{0x28, 0x02, 0x00}, // i32.load → depth
		[]byte{0x41, 0x01, 0x6b}, // i32.const 1; i32.sub → depth-1
		[]byte{0x36, 0x02, 0x00}, // i32.store
	)
}

// --- guarded form: frame build/teardown entirely inside Wasm + real runtime guards (models production Stage 2/3 guard overhead) ---
//
// Difference from inwasm: the segment base is **read live** from the segBase word (models a relocatable
// production CI segment, where Wasm reads the ciSegBaseRef word to compute the address live instead of
// baking in an immediate); teardown includes a **real maxOpenIdx guard branch** (word read + compare +
// if, modeling "take the fast path only when there are no open upvalues"); build includes a **caller
// gibbous bit check** (read segment-frame word2 bit50, modeling "inline only when the caller is
// gibbous"). The guards always pass (the spike sets the words so the fast path is always taken); this
// measures whether "inline frame build/teardown with the full runtime guards" is still significantly
// faster than two host crossings.

// segWordStoreVar — same as segWordStore but the segment base is read live from the segBase word (not a constant segBase).
func segWordStoreVar(w int, val byte) []byte {
	return concat(
		// addr = load(segBaseWordOff) + depth*32 + w*8
		[]byte{0x41, segBaseWordOff},   // i32.const segBaseWordOff
		[]byte{0x28, 0x02, 0x00},       // i32.load → segBase (read live)
		[]byte{0x41, ciDepthOff},       // i32.const ciDepthOff
		[]byte{0x28, 0x02, 0x00},       // i32.load → depth
		[]byte{0x41, 0x20, 0x6c},       // i32.const 32; i32.mul → depth*32
		[]byte{0x6a},                   // add → segBase + depth*32
		i32ConstSeq(w*8), []byte{0x6a}, // + w*8
		[]byte{0x42, val},        // i64.const val
		[]byte{0x37, 0x03, 0x00}, // i64.store
	)
}

// guardedBuild — build frame + caller gibbous bit check (read segment-frame word2 and check live).
func guardedBuild() []byte {
	return concat(
		// caller gibbous bit check: read word2 of the depth frame (here depth is the caller),
		// take bit50 to model "inline only when the caller is gibbous" (always true in the spike).
		// addr = load(segBase) + depth*32 + 16 (word2)
		[]byte{0x41, segBaseWordOff}, []byte{0x28, 0x02, 0x00}, // load segBase
		[]byte{0x41, ciDepthOff}, []byte{0x28, 0x02, 0x00}, // load depth
		[]byte{0x41, 0x20, 0x6c}, []byte{0x6a}, // depth*32 + segBase
		[]byte{0x41, 0x10, 0x6a}, // + 16 (word2 offset)
		[]byte{0x29, 0x03, 0x00}, // i64.load → word2
		[]byte{0x42, 0x00},       // i64.const 0 (spike: does not really test bit50, read + drop to measure cost)
		[]byte{0x84},             // i64.or (consume word2 + 0, keep result)
		[]byte{0x1a},             // drop
		// segment-word writes (segment base read live) + ciDepth++
		segWordStoreVar(0, 0x11), segWordStoreVar(1, 0x22),
		segWordStoreVar(2, 0x33), segWordStoreVar(3, 0x44),
		[]byte{0x41, ciDepthOff}, []byte{0x41, ciDepthOff},
		[]byte{0x28, 0x02, 0x00}, []byte{0x41, 0x01, 0x6a}, []byte{0x36, 0x02, 0x00},
	)
}

// guardedTeardown — teardown frame + real maxOpenIdx guard branch (word read + if, always passes).
func guardedTeardown() []byte {
	return concat(
		// maxOpenIdx guard: if load(maxOpenOff) != 0 { (slow path, never taken in the spike) }
		[]byte{0x41, maxOpenOff}, []byte{0x28, 0x02, 0x00}, // load maxOpenIdx
		[]byte{0x04, 0x40}, // if (void)  —— real guard branch (always false, not taken)
		// slow-path body (empty in the spike; production falls back to h_return).
		[]byte{0x0b}, // end if
		// ciDepth-- (reading the segment base live does not affect the ciDepth word, decrement directly)
		[]byte{0x41, ciDepthOff}, []byte{0x41, ciDepthOff},
		[]byte{0x28, 0x02, 0x00}, []byte{0x41, 0x01, 0x6b}, []byte{0x36, 0x02, 0x00},
	)
}

// --- twocross form: frame build/teardown via host crossings ---

// twocrossBuild — call h_call(depth_placeholder). h_call (Go) does the equivalent frame-build work.
// Passes an i32 placeholder (models base), h_call returns an i32 (models the refreshed base, dropped here).
func twocrossBuild() []byte {
	return concat(
		[]byte{0x41, 0x00}, // i32.const 0 (placeholder arg)
		[]byte{0x10, 0x00}, // call h_call (imported func 0)
		[]byte{0x1a},       // drop return value
	)
}

// twocrossTeardown — call h_return(depth_placeholder). h_return (Go) does the equivalent frame-teardown work.
func twocrossTeardown() []byte {
	return concat(
		[]byte{0x41, 0x00}, // i32.const 0 (placeholder arg)
		[]byte{0x10, 0x01}, // call h_return (imported func 1)
	)
}

// segWordStore — i64.store memory[segBase + depth*32 + w*8] = const val.
// depth is loaded live from ciDepthOff. Stack-neutral (pushes addr + value itself, store consumes both).
func segWordStore(w int, val byte) []byte {
	// addr = segBase + depth*32 + w*8 ;  depth = load(ciDepthOff)
	return concat(
		// push store addr: segBase + w*8 + depth*32
		[]byte{0x41, ciDepthOff}, // i32.const ciDepthOff
		[]byte{0x28, 0x02, 0x00}, // i32.load → depth
		[]byte{0x41, 0x20},       // i32.const 32 (ciWords*8)
		[]byte{0x6c},             // i32.mul → depth*32
		i32ConstSeq(segBase+w*8), // i32.const (segBase + w*8)
		[]byte{0x6a},             // i32.add → final address
		// push value
		[]byte{0x42, val}, // i64.const val (single-byte sleb, val<64)
		// store(align=3 offset=0)
		[]byte{0x37, 0x03, 0x00},
	)
}

// i32ConstSeq — i32.const (supports multi-byte sleb128 for values >127).
func i32ConstSeq(v int) []byte {
	return concat([]byte{0x41}, sleb(int32(v)))
}

// --- wasm binary construction helpers (same as p3indirect) ---

func sec(id byte, payload []byte) []byte {
	return concat([]byte{id}, uleb(uint32(len(payload))), payload)
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x00}, uleb(typeIdx),
	)
}

func importMemEntry(mod, name string) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x02},       // kind=memory
		[]byte{0x00, 0x01}, // limits flags=0 min=1
	)
}

func uleb(v uint32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		out = append(out, c)
		if v == 0 {
			return out
		}
	}
}

func sleb(v int32) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		signBit := c&0x40 != 0
		if (v == 0 && !signBit) || (v == -1 && signBit) {
			out = append(out, c)
			return out
		}
		out = append(out, c|0x80)
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
