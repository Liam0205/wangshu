package p3indirect

// S-C: is cross-module call_indirect via a **shared imported funcref table**
// feasible (R1 architecture fork).
//
// S-B proved that "rebuild the whole module + new instance" works (Arch-1
// rebuild-all), but it is complex (generational instances + cross-generation
// dispatch guards). Arch-2 is simpler: keep one module per Proto, and have all
// modules share a single funcref table exported by env -- a new upgrade =
// instantiating a new module, which **self-registers** its own function into a
// table slot via its active element segment, avoiding recompilation of existing
// modules. The caller module's `call_indirect` on that table slot reaches the
// provider's function across modules.
//
// Make-or-break question: does wazero support "module B's active element segment
// filling the imported table exported by env + module C resolving a function
// defined in module B via call_indirect on the imported table". Passing ⇒ Arch-2
// (R1 greatly simplified); failing ⇒ fall back to Arch-1 (already proven).

// envTableMemModule — env module exporting memory + one funcref table (min=numSlots).
// All gibbous modules import both; the table is their shared "upgrade function registry".
func envTableMemModule(numSlots int) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Table section: 1 funcref table, flags=0 min=numSlots.
	b = append(b, sec(0x04, concat(
		uleb(1),
		[]byte{0x70},           // elemtype funcref
		[]byte{0x00},           // limits flags=0(no max)
		uleb(uint32(numSlots)), // min
	))...)
	// Memory section: 1 memory min=1 max=16.
	b = append(b, sec(0x05, concat(
		uleb(1),
		[]byte{0x01, 0x01, 0x10},
	))...)
	// Export section: table (kind=0x01 idx0) + memory (kind=0x02 idx0).
	b = append(b, sec(0x07, concat(
		uleb(2),
		[]byte{0x05}, []byte("table"), []byte{0x01, 0x00},
		[]byte{0x06}, []byte("memory"), []byte{0x02, 0x00},
	))...)
	return b
}

// providerModule — an "upgrade Proto" module: imports env.table + env.memory, defines
// leaf(x)=x*mulK+1, and self-registers leaf into table[slot] via an active element segment.
// No export needed (instantiation runs the element segment to fill the table).
//
// func index space: table/memory imports do not occupy func index ⟹ leaf = func 0.
func providerModule(slot int, mulK byte) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Type section: type0 (i32)->(i32).
	b = append(b, sec(0x01, concat(
		uleb(1),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},
	))...)
	// Import section: env.table + env.memory.
	b = append(b, sec(0x02, concat(
		uleb(2),
		importTableEntry("env", "table", 0),
		importMemEntry("env", "memory"),
	))...)
	// Function section: 1 func (leaf) type0.
	b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...)
	// Element section: active segment, table 0, offset=(i32.const slot), func[0]=leaf.
	b = append(b, sec(0x09, concat(
		uleb(1),                                       // 1 segment
		[]byte{0x00},                                  // flags=0(active, table 0, offset expr)
		[]byte{0x41}, sleb(int32(slot)), []byte{0x0b}, // offset = (i32.const slot) end
		uleb(1), // 1 funcidx
		uleb(0), // leaf = func 0
	))...)
	// Code section: leaf(x)=x*mulK+1.
	leaf := concat(
		[]byte{0x00}, // 0 locals
		[]byte{0x20, 0x00, 0x41, mulK, 0x6c, 0x41, 0x01, 0x6a}, // local.get0; const mulK; mul; const1; add
		[]byte{0x0b},
	)
	b = append(b, sec(0x0a, concat(uleb(1), uleb(uint32(len(leaf))), leaf))...)
	return b
}

// callerModule — driver(n) calls leaf via call_indirect on the imported table at slot,
// accumulating n times. imports env.table + env.memory; driver = func 0 (table/memory
// imports do not occupy func idx).
func callerModule(slot int) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	b = append(b, sec(0x01, concat(
		uleb(1),
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f}, // type0 (i32)->(i32)
	))...)
	b = append(b, sec(0x02, concat(
		uleb(2),
		importTableEntry("env", "table", 0),
		importMemEntry("env", "memory"),
	))...)
	b = append(b, sec(0x03, concat(uleb(1), uleb(0)))...) // driver func0 type0
	b = append(b, sec(0x07, concat(
		uleb(1),
		[]byte{0x06}, []byte("driver"), []byte{0x00, 0x00}, // export driver func0
	))...)
	// driver body: locals $acc; loop calling call_indirect table[slot] to accumulate.
	locals := []byte{0x01, 0x01, 0x7f}
	body := concat(
		[]byte{
			0x02, 0x40, // block $exit
			0x03, 0x40, // loop $top
			0x20, 0x00, // local.get $n
			0x45,       // i32.eqz
			0x0d, 0x01, // br_if $exit
			0x20, 0x01, // local.get $acc
			0x20, 0x00, // local.get $n (arg)
			0x41}, sleb(int32(slot)), // i32.const slot (table index operand)
		[]byte{0x11, 0x00, 0x00}, // call_indirect type0 table0
		[]byte{
			0x6a,       // i32.add
			0x21, 0x01, // local.set $acc
			0x20, 0x00, 0x41, 0x01, 0x6b, 0x21, 0x00, // n--
			0x0c, 0x00, // br $top
			0x0b,       // end loop
			0x0b,       // end block
			0x20, 0x01, // local.get $acc
		},
	)
	driver := concat(locals, body, []byte{0x0b})
	b = append(b, sec(0x0a, concat(uleb(1), uleb(uint32(len(driver))), driver))...)
	return b
}

// importTableEntry — import mod.name : table (funcref, flags=0 min).
func importTableEntry(mod, name string, min uint32) []byte {
	return concat(
		[]byte{byte(len(mod))}, []byte(mod),
		[]byte{byte(len(name))}, []byte(name),
		[]byte{0x01}, // kind=table
		[]byte{0x70}, // elemtype funcref
		[]byte{0x00}, // limits flags=0
		uleb(min),    // min
	)
}

// sleb — signed LEB128 (i32.const immediate). A small slot (<64) fits in a single byte,
// but this is the general encoding.
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
