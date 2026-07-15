package p3indirect

// S-B module generation: validates the lifecycle feasibility of
// "incremental tier-up = recompile the whole module + hot-swap a new instance".
// The key physical fact (inherited from PW1 / 03-memory-model): all gibbous
// module instances import the same `env.memory` (the arena-adopted linear
// memory), so instances share a view of the same memory — many module
// instances can coexist, and the memory is their common substrate.
//
// S-B module shape: import env.memory (the shared substrate) + one driver(base)
// that accumulates leaf(i) and writes it back to memory[base], for
// cross-instance visibility verification. Optionally import host.h_promote (a
// re-entrant tier-up hook: mid-execution the driver returns to Go to trigger
// "compile new module + instantiate + call", simulating "tier up B while
// frame A is on the Go stack").

// buildMemModule generates a module that imports env.memory:
//
//	driver(base) {
//	  acc = 0
//	  for i = numLeaves down to 1 { acc += leaf(i) }   // leaf(x)=x*3+1
//	  if withPromote { call h_promote }                // re-entrant tier-up hook
//	  i64.store memory[base] = acc                     // write shared memory (visible across instances)
//	  return acc
//	}
//
// Function index layout (when withPromote): func 0 = imported h_promote; leaf 1..numLeaves;
// driver = numLeaves+1. Without withPromote: leaf 0..numLeaves-1; driver = numLeaves.
func buildMemModule(numLeaves int, withPromote bool) []byte {
	if numLeaves < 1 {
		numLeaves = 1
	}
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Type section: type0 (i32)->(i32) for leaf/driver; type1 ()->() for h_promote.
	b = append(b, sec(0x01, concat(
		uleb(2),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0 (i32)->(i32)
		[]byte{0x60, 0x00, 0x00},             // type1 ()->()
	))...)

	// Import section: env.memory (required), host.h_promote (optional).
	leafBase := uint32(0)
	imp := []byte{}
	nImp := uint32(1)
	imp = append(imp, importMemEntry("env", "memory")...)
	if withPromote {
		imp = append(imp, importFuncEntry("host", "h_promote", 1)...) // type1 ()->()
		nImp++
		leafBase = 1 // imported func occupies index 0
	}
	b = append(b, sec(0x02, concat(uleb(nImp), imp))...)

	driverIdx := leafBase + uint32(numLeaves)

	// Function section: numLeaves leaf + 1 driver, all type0.
	fn := uleb(uint32(numLeaves + 1))
	for i := 0; i <= numLeaves; i++ {
		fn = append(fn, uleb(0)...)
	}
	b = append(b, sec(0x03, fn)...)

	// Export section: driver.
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"),
		[]byte{0x00}, uleb(driverIdx),
	))...)

	// Code section: leaf ×numLeaves + driver.
	var bodies [][]byte
	for i := 0; i < numLeaves; i++ {
		bodies = append(bodies, leafBody())
	}
	bodies = append(bodies, memDriverBody(leafBase, numLeaves, withPromote))
	code := uleb(uint32(len(bodies)))
	for _, body := range bodies {
		code = append(code, concat(uleb(uint32(len(body))), body)...)
	}
	b = append(b, sec(0x0a, code)...)
	return b
}

// memDriverBody — driver(base): Σ leaf(i) accumulate → i64.store memory[base];
// optionally call h_promote mid-way. leafBase = func index of the first leaf.
func memDriverBody(leafBase uint32, numLeaves int, withPromote bool) []byte {
	// locals: $acc i32 (local 1). param $base = local 0.
	locals := []byte{0x01, 0x01, 0x7f}

	body := []byte{
		0x02, 0x40, // block $exit
		0x41, 0x00, // i32.const 0
		0x21, 0x01, // local.set $acc = 0
		// Use $base as the loop counter? No — $base must be kept as the store
		// address. A simpler approach:
		// directly call the first leaf and accumulate numLeaves times (unrolled),
		// avoiding an extra local.
	}
	// Unroll numLeaves leaf calls (numLeaves is small, S-B uses 1; manyleaves tests compile cost separately).
	for i := 0; i < numLeaves; i++ {
		body = append(body,
			0x20, 0x01, // local.get $acc
			0x41, byte(i+1), // i32.const (i+1)  —— leaf's arg (i<127 single-byte sleb)
			0x10, // call
		)
		body = append(body, uleb(leafBase+uint32(i))...) // leaf func index
		body = append(body, 0x6a, 0x21, 0x01)            // i32.add; local.set $acc
	}
	if withPromote {
		body = append(body, 0x10)       // call
		body = append(body, uleb(0)...) // h_promote = imported func 0
	}
	body = append(body,
		0x0b, // end block $exit
		// memory[base] = (i64)acc
		0x20, 0x00, // local.get $base (store addr)
		0x20, 0x01, // local.get $acc
		0xac,             // i64.extend_i32_s
		0x37, 0x03, 0x00, // i64.store align=3 offset=0
		0x20, 0x01, // local.get $acc (return)
	)
	return concat(locals, body, []byte{0x0b}) // + end func
}

// importMemEntry — import mod.name : memory (limits flags=0 min=1).
func importMemEntry(mod, name string) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x02},       // kind=memory
		[]byte{0x00, 0x01}, // limits flags=0, min=1
	)
}
