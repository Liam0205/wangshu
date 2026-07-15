package p3indirect

// Hand-written / generated Wasm module binaries (PW10 spike). The three driver
// variants share the same leaf body + the same loop skeleton, and **differ only
// in the call mechanism**, so dispatch cost can be compared fairly:
//
//   - indirect: `call_indirect` to leaf within a single module (the target of this design)
//   - direct  : `call` directly to leaf within a single module (floor baseline, no table lookup)
//   - host    : `call` an imported Go function (= PW0 S3N variant, ~143ns cross-layer tax baseline)
//
// The leaf body is uniform: `leaf(x) = x*3 + 1` (pure arithmetic, isolating
// dispatch cost — the function body cost is identical across the three variants,
// so all difference lies in the call mechanism). The driver accumulates leaf(n)
// N times in a tight loop to amortize the outer fn.Call overhead; ns/op ÷ N =
// the amortized cost of a single dispatch.
//
// Wasm binary format: https://webassembly.github.io/spec/core/binary/

// callKind distinguishes the driver's call mechanism.
type callKind int

const (
	kindIndirect callKind = iota // call_indirect (this design)
	kindDirect                   // call (direct-call floor)
	kindHost                     // call imported (cross-layer tax baseline)
)

// buildModule generates one spike module binary.
//
//   - numLeaves: number of local leaf functions (≥1). Used by indirect/direct;
//     ignored by the host variant (host has no local leaf, only imports one).
//     numLeaves>1 is used only by S-B to stuff the module with more functions
//     and test how CompileModule scales with function count.
//   - kind: the driver's call mechanism.
//
// Function index layout:
//   - indirect/direct: func 0..numLeaves-1 = leaves; func numLeaves = driver.
//   - host: func 0 = imported h_leaf; func 1 = driver.
func buildModule(numLeaves int, kind callKind) []byte {
	if numLeaves < 1 {
		numLeaves = 1
	}
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version

	// Type section: 1 type (i32)->(i32).
	b = append(b, sec(0x01, concat(
		uleb(1),                              // count = 1
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // (i32)->(i32)
	))...)

	driverIdx := uint32(numLeaves) // indirect/direct: driver comes after the leaves

	if kind == kindHost {
		// Import section: env.h_leaf : type 0 (occupies func index 0).
		b = append(b, sec(0x02, concat(
			uleb(1),
			importFuncEntry("env", "h_leaf", 0),
		))...)
		// Function section: 1 local function (driver).
		b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...)
		driverIdx = 1 // import occupies 0, driver is 1
	} else {
		// Function section: numLeaves leaves + 1 driver, all type 0.
		fn := uleb(uint32(numLeaves + 1))
		for i := 0; i <= numLeaves; i++ {
			fn = append(fn, uleb(0)...)
		}
		b = append(b, sec(0x03, fn)...)
	}

	// Table + Element section: only indirect needs them (funcref table + fill).
	if kind == kindIndirect {
		// Table section: 1 funcref table, min=numLeaves.
		b = append(b, sec(0x04, concat(
			uleb(1),                 // count = 1
			[]byte{0x70},            // elemtype funcref
			[]byte{0x00},            // limits flags=0 (no max)
			uleb(uint32(numLeaves)), // min
		))...)
	}

	// Export section: export "driver".
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"),
		[]byte{0x00}, uleb(driverIdx), // kind=func, index
	))...)

	if kind == kindIndirect {
		// Element section: active segment 0, table[0..numLeaves-1] = leaf 0..numLeaves-1.
		el := concat(
			uleb(1),                  // 1 segment
			[]byte{0x00},             // flags=0 (active, table 0, offset expr)
			[]byte{0x41, 0x00, 0x0b}, // offset = (i32.const 0) end
			uleb(uint32(numLeaves)),  // num funcs
		)
		for i := 0; i < numLeaves; i++ {
			el = append(el, uleb(uint32(i))...)
		}
		b = append(b, sec(0x09, el)...)
	}

	// Code section: leaf body ×numLeaves (0 for the host variant) + driver body ×1.
	var bodies [][]byte
	if kind != kindHost {
		for i := 0; i < numLeaves; i++ {
			bodies = append(bodies, leafBody())
		}
	}
	bodies = append(bodies, driverBody(kind))
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)

	return b
}

// leafBody — leaf(x) = x*3 + 1. Returns a single code-section function body (localdecls + instrs + end).
func leafBody() []byte {
	return concat(
		[]byte{0x00}, // 0 local groups
		[]byte{
			0x20, 0x00, // local.get 0 ($x)
			0x41, 0x03, // i32.const 3
			0x6c,       // i32.mul
			0x41, 0x01, // i32.const 1
			0x6a, // i32.add
		},
		[]byte{0x0b}, // end
	)
}

// driverBody — driver(n) { acc=0; while n>0 { acc += <call> leaf(n); n-- } return acc }.
// The call mechanism switches only that small snippet based on kind.
func driverBody(kind callKind) []byte {
	// locals: 1 group of 1 i32 ($acc = local 1; $n = param local 0).
	locals := []byte{0x01, 0x01, 0x7f}

	// The call snippet (stack already holds [acc, n]; below computes leaf(n) then i32.add back into acc).
	var callSeq []byte
	switch kind {
	case kindIndirect:
		callSeq = []byte{
			0x41, 0x00, // i32.const 0   (table index)
			0x11, 0x00, 0x00, // call_indirect type=0 table=0
		}
	case kindDirect:
		callSeq = []byte{0x10, 0x00} // call 0 ($leaf)
	case kindHost:
		callSeq = []byte{0x10, 0x00} // call 0 (imported h_leaf)
	}

	body := concat(
		[]byte{
			0x02, 0x40, // block $exit (void)
			0x03, 0x40, // loop $top (void)
			0x20, 0x00, // local.get 0 ($n)
			0x45,       // i32.eqz
			0x0d, 0x01, // br_if $exit (depth 1)
			0x20, 0x01, // local.get 1 ($acc)
			0x20, 0x00, // local.get 0 ($n) — arg to leaf
		},
		callSeq, // compute leaf(n) onto the stack
		[]byte{
			0x6a,       // i32.add (acc + leaf(n))
			0x21, 0x01, // local.set 1 ($acc)
			0x20, 0x00, // local.get 0 ($n)
			0x41, 0x01, // i32.const 1
			0x6b,       // i32.sub
			0x21, 0x00, // local.set 0 ($n)
			0x0c, 0x00, // br $top (depth 0)
			0x0b,       // end loop
			0x0b,       // end block
			0x20, 0x01, // local.get 1 ($acc) — return value
		},
	)
	return concat(locals, body, []byte{0x0b}) // + end func
}

// --- wasm binary construction helpers (same as p3boundary modules.go) ---

func sec(id byte, payload []byte) []byte {
	return concat([]byte{id}, uleb(uint32(len(payload))), payload)
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x00}, uleb(typeIdx), // kind=func, type index
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

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
