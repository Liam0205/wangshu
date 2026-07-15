//go:build wangshu_p3

package memadapter

// buildMemoryHolderModuleBinary builds the wasm module binary that declares and
// exports a memory plus one funcref table (hand-written, without pulling in a
// build-time wat2wasm tool, following the spike/p3boundary experience).
//
// Equivalent WAT:
//
//	(module
//	  (memory (export "memory") <initPage> <maxPage>)
//	  (table (export "table") <tableSlots> funcref))
//
// **table (PW10 Arch-2)**: the "promoted-function registry" shared across
// gibbous modules — each promoted Proto's module registers its own `run` into a
// slot via an active element segment, and a gibbous→gibbous CALL reaches across
// modules directly via `call_indirect <slot>`, avoiding a Go round-trip
// (verified by spike/p3indirect S-C). The table is declared once here, with
// capacity = tableSlots (the slot upper bound).
//
// Wasm binary format: magic(4)+version(4)+Table(4)+Memory(5)+Export(7).
func buildMemoryHolderModuleBinary(initPage, maxPage, tableSlots uint32) []byte {
	var b []byte
	// magic + version
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Table section (id=4): 1 funcref table, limits flags=0 (no max), min=tableSlots.
	// Fixed capacity (no max): an active element segment writing table[slot] requires min ≥ slot+1, so reserve enough.
	tablePayload := []byte{0x01, 0x70, 0x00} // count=1, elemtype funcref(0x70), flags=0(no max)
	tablePayload = append(tablePayload, uleb(tableSlots)...)
	b = append(b, section(0x04, tablePayload)...)

	// Memory section (id=5): 1 memory, limits flags=1 (has max), min, max
	memPayload := []byte{0x01, 0x01} // count=1, flags=1(min+max)
	memPayload = append(memPayload, uleb(initPage)...)
	memPayload = append(memPayload, uleb(maxPage)...)
	b = append(b, section(0x05, memPayload)...)

	// Export section (id=7): export "memory" = mem 0; "table" = table 0
	expPayload := []byte{0x02} // count=2
	expPayload = append(expPayload, byte(len("memory")))
	expPayload = append(expPayload, []byte("memory")...)
	expPayload = append(expPayload, 0x02, 0x00) // kind=mem(2), index=0
	expPayload = append(expPayload, byte(len("table")))
	expPayload = append(expPayload, []byte("table")...)
	expPayload = append(expPayload, 0x01, 0x00) // kind=table(1), index=0
	b = append(b, section(0x07, expPayload)...)

	return b
}

// section wraps one wasm section: id + uleb(len) + payload.
func section(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint32(len(payload)))...)
	return append(out, payload...)
}

// uleb encodes an unsigned LEB128.
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
