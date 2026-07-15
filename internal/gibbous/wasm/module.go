//go:build wangshu_p3

package wasm

// gibbous module binary assembly (02-translation §7 wazero adaptation).
//
// Wraps the translation output (function body bytes, translate.go) into a
// complete Wasm module binary: import memory (the block adopted by the shared
// arena) + import helpers (env.h_*) + one exported entry function proto_entry.
//
// module structure (section order per the Wasm spec):
//   Type(1)     : function signatures (entry + each helper)
//   Import(2)   : env.memory + env.h_getupval/h_setupval/h_return/h_safepoint
//   Function(3) : type index of the local function (entry)
//   Export(7)   : export entry function "run"
//   Code(10)    : entry function body

// helper signatures (matching the import order in helpers_index.go).
// One type per helper; the entry function also has a type.
//
// type index layout:
//   type 0: (i32, i32) -> (i64)            h_getupval
//   type 1: (i32, i32, i64) -> ()          h_setupval
//   type 2: (i32, i32, i32, i32) -> (i32)  h_return
//   type 3: (i32, i32) -> (i32)            h_safepoint(base,pc -> status)
//   type 4: (i32) -> (i32)                 entry run(base) -> status
//   type 5: (i32,i32,i32,i32,i32,i32)->(i32) h_arith(base,pc,op,b,c,a)
//   type 6: (i32,i32,i32,i32) -> (i32)     h_unm(base,pc,b,a) / h_eq(base,pc,b,c)
//   type 7: (i32,i32,i32,i32) -> (i32)     h_len(base,pc,b,a)
//   type 8: (i32,i32,i32,i32,i32) -> (i32) h_concat(base,pc,a,b,c) / h_compare(base,pc,op,b,c)
//   type 9: (i32,i32,i32) -> (i32)         h_forprep(base,pc,a)

const (
	typeGetUpval  = 0
	typeSetUpval  = 1
	typeReturn    = 2
	typeSafepoint = 3
	typeEntry     = 4
	typeArith     = 5
	typeUnm       = 6 // same shape as typeReturn (i32×4->i32) but listed separately for readability
	typeLen       = 7
	typeConcat    = 8
	typeForPrep   = 9
	typeCall      = 10 // (i32×5)->(i64)  h_call returns new base / negative sentinel
	typeTForLoop  = 11 // (i32×4)->(i64)  h_tforloop returns new base / -1 ERR / -2 exit
	typeCallErr   = 12 // ()->()  h_callerr: pops leftover gibbous frames after a direct call_indirect failure (PW10 R3)
	numTypes      = 13
)

// Wasm value type encoding.
const (
	wvtI32 byte = 0x7f
	wvtI64 byte = 0x7e
	wvtF64 byte = 0x7c
)

// buildGibbousModuleBinary assembles the complete module binary.
//
// body is the entry function body produced by translate (without local decls
// and the trailing end).
// slot is the slot index of this module's run within the shared env.table
// (PW10 Arch-2): the Element segment actively writes table[slot] = run, so that
// gibbous->gibbous can reach across modules directly via call_indirect.
// When slot == maxTableSlots (table-full sentinel) no Element segment is
// emitted (to avoid an out-of-bounds write); the module still compiles and
// runs, it just does not enter the table (gibbous->it falls back to h_call).
func buildGibbousModuleBinary(body []byte, slot uint32) []byte {
	var b []byte
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic+version
	b = append(b, typeSection()...)
	b = append(b, importSection()...)
	b = append(b, functionSection()...)
	b = append(b, exportSection()...)
	if slot < maxTableSlots {
		b = append(b, elementSection(slot)...)
	}
	b = append(b, codeSectionEntry(body)...)
	return b
}

// elementSection actively registers the entry run (function index = numHelpers,
// the first local function after the imports) into slot of the shared env.table
// (PW10 Arch-2 self-registration).
//
//	(elem (i32.const slot) func $run)
//
// flags=0: active, table 0 (the sole imported table), offset given by a const expr.
func elementSection(slot uint32) []byte {
	var p []byte
	p = append(p, uleb32(1)...)                // 1 segment
	p = append(p, 0x00)                        // flags=0 (active, table 0, offset expr)
	p = append(p, 0x41)                        // i32.const
	p = append(p, sleb32Bytes(int32(slot))...) // offset = slot
	p = append(p, 0x0b)                        // end (offset expr done)
	p = append(p, uleb32(1)...)                // 1 funcidx
	p = append(p, uleb32(numHelpers)...)       // run = first local func after imports
	return sectionOf(0x09, p)
}

// sleb32Bytes signed LEB128 (i32.const immediate; slot is a small non-negative
// number, but goes through the generic encoding).
func sleb32Bytes(v int32) []byte {
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

// typeSection declares the 5 function types.
func typeSection() []byte {
	var p []byte
	p = append(p, uleb32(numTypes)...) // count

	// type 0: (i32,i32)->(i64)
	p = append(p, 0x60, 0x02, wvtI32, wvtI32, 0x01, wvtI64)
	// type 1: (i32,i32,i64)->()
	p = append(p, 0x60, 0x03, wvtI32, wvtI32, wvtI64, 0x00)
	// type 2: (i32,i32,i32,i32)->(i32)
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 3: (i32,i32)->(i32)  h_safepoint(base,pc -> status: 0 OK / 1 raise)
	p = append(p, 0x60, 0x02, wvtI32, wvtI32, 0x01, wvtI32)
	// type 4: (i32)->(i32)
	p = append(p, 0x60, 0x01, wvtI32, 0x01, wvtI32)
	// type 5: (i32,i32,i32,i32,i32,i32)->(i32)  h_arith
	p = append(p, 0x60, 0x06, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 6: (i32,i32,i32,i32)->(i32)  h_unm
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 7: (i32,i32,i32,i32)->(i32)  h_len
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 8: (i32,i32,i32,i32,i32)->(i32)  h_concat / h_compare
	p = append(p, 0x60, 0x05, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 9: (i32,i32,i32)->(i32)  h_forprep
	p = append(p, 0x60, 0x03, wvtI32, wvtI32, wvtI32, 0x01, wvtI32)
	// type 10: (i32,i32,i32,i32,i32)->(i64)  h_call(base,pc,a,b,c -> newbase/-1)
	p = append(p, 0x60, 0x05, wvtI32, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI64)
	// type 11: (i32,i32,i32,i32)->(i64)  h_tforloop(base,pc,a,c -> newbase/-1/-2)
	p = append(p, 0x60, 0x04, wvtI32, wvtI32, wvtI32, wvtI32, 0x01, wvtI64)
	// type 12: ()->()  h_callerr(PW10 R3: no params, no returns, pops leftover gibbous frames)
	p = append(p, 0x60, 0x00, 0x00)

	return sectionOf(0x01, p)
}

// importSection declares import memory + 4 helpers.
//
// memory comes from the "env" module (memadapter holder, a normal module that
// exports memory); helpers come from the "host" module (Go functions registered
// via wazero HostModuleBuilder).
// The two module names differ -- wazero HostModuleBuilder cannot export memory,
// and a normal module cannot register Go host functions, so memory and helpers
// belong to different modules
// (PW2-c cross-module memory sharing was already validated by a spike).
//
// The import order determines each helper's function index (constants in
// helpers_index.go): h_getupval=0, h_setupval=1, h_return=2, h_safepoint=3. The
// memory import does not occupy function index space.
func importSection() []byte {
	var p []byte
	// count = 1 memory + 1 table + 24 funcs = 26 (PW10 R3 adds env.h_callerr)
	p = append(p, uleb32(26)...)

	// import env.memory : memory (limits flags=0 min=1) -- the shared holder's memory
	p = append(p, importEntry("env", "memory", 0x02, []byte{0x00, 0x01})...)

	// import env.table : funcref table (PW10 Arch-2) -- the shared holder's uplevel
	// function registry. This module's run self-registers into a slot via the
	// element segment; a gibbous->gibbous CALL reaches the callee run across
	// modules directly via call_indirect on this table (R3 wiring). The imported
	// table does not occupy function index space (like memory), so helper / entry
	// func indices stay unchanged.
	// limits flags=0 min=0 (the import declares "at least 0"; env actually
	// provides TableSlots).
	p = append(p, importEntry("env", "table", 0x01, []byte{0x70, 0x00, 0x00})...)

	// import host.h_* : func (order = function index = constants in helpers_index.go)
	p = append(p, importFuncEntry("host", "h_getupval", typeGetUpval)...)
	p = append(p, importFuncEntry("host", "h_setupval", typeSetUpval)...)
	p = append(p, importFuncEntry("host", "h_return", typeReturn)...)
	p = append(p, importFuncEntry("host", "h_safepoint", typeSafepoint)...)
	p = append(p, importFuncEntry("host", "h_arith", typeArith)...)
	p = append(p, importFuncEntry("host", "h_unm", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_len", typeLen)...)
	p = append(p, importFuncEntry("host", "h_concat", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_compare", typeConcat)...) // (i32×5→i32)
	p = append(p, importFuncEntry("host", "h_eq", typeUnm)...)         // (i32×4→i32)
	p = append(p, importFuncEntry("host", "h_forprep", typeForPrep)...)
	// PW5 table IC helpers: gettable/settable/self/newtable/setlist are (base,pc,a,b,c)
	// = i32×5->i32 = typeConcat; getglobal/setglobal are (base,pc,a,bx) = i32×4->i32 = typeUnm.
	p = append(p, importFuncEntry("host", "h_gettable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_settable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_getglobal", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_setglobal", typeUnm)...)
	p = append(p, importFuncEntry("host", "h_self", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_newtable", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_setlist", typeConcat)...)
	p = append(p, importFuncEntry("host", "h_call", typeCall)...)
	p = append(p, importFuncEntry("host", "h_tailcall", typeConcat)...)   // (i32×5→i32 status)
	p = append(p, importFuncEntry("host", "h_closure", typeUnm)...)       // (i32×4→i32 status)
	p = append(p, importFuncEntry("host", "h_close", typeForPrep)...)     // (i32×3→i32 status)
	p = append(p, importFuncEntry("host", "h_tforloop", typeTForLoop)...) // (i32×4→i64)
	p = append(p, importFuncEntry("host", "h_callerr", typeCallErr)...)   // ()→()  PW10 R3

	return sectionOf(0x02, p)
}

// functionSection declares the type index of the local function (entry run).
// The entry is the function index after the imports = numHelpers (4).
func functionSection() []byte {
	var p []byte
	p = append(p, uleb32(1)...)         // count = 1 local function
	p = append(p, uleb32(typeEntry)...) // entry uses type 4
	return sectionOf(0x03, p)
}

// exportSection exports the entry function "run".
func exportSection() []byte {
	var p []byte
	p = append(p, uleb32(1)...) // count
	name := "run"
	p = append(p, byte(len(name)))
	p = append(p, name...)
	p = append(p, 0x00)                  // kind=func
	p = append(p, uleb32(numHelpers)...) // entry function index = 4 (first local after imports)
	return sectionOf(0x07, p)
}

// codeSectionEntry entry function code (local decl + body + end).
func codeSectionEntry(body []byte) []byte {
	// local decl (order determines local index, after param $base=0):
	//   group1: 2×i64 -> index 1,2 (localI64a/localI64b)
	//   group2: 2×i32 -> index 3,5 (localI32 helper status / localI32b table addr)
	//   group3: 1×f64 -> index 4 (localF64, arithmetic result)
	//   group4: 1×i64 -> index 6 (localI64c, PW5 key/slot value relay)
	//   group5: 1×i32 -> index 7 (localSavedTop, PW10 zero-cross ③a: caller self-restore top snapshot)
	// Note: local index is determined by declaration order (consecutive within a
	// group). Group2 declares 2×i32 taking 3,4? No -- group order is index order:
	// group1 i64 takes 1,2; group2 i32 takes 3,4; group3 f64 takes 5; group4 i64 takes 6.
	// To keep the existing PW2-PW4 indices (localI32=3 / localF64=4) unchanged, the
	// group order must be:
	//   group1 2×i64(1,2) / group2 1×i32(3) / group3 1×f64(4) / group4 1×i32(5) / group5 1×i64(6)
	//   / group6 1×i32(7, PW10 zero-cross ③a localSavedTop).
	var locals []byte
	locals = append(locals, uleb32(6)...) // 6 local groups
	locals = append(locals, uleb32(2)...) // 2 i64 -> index 1,2
	locals = append(locals, wvtI64)
	locals = append(locals, uleb32(1)...) // 1 i32 -> index 3 (localI32)
	locals = append(locals, wvtI32)
	locals = append(locals, uleb32(1)...) // 1 f64 -> index 4 (localF64)
	locals = append(locals, wvtF64)
	locals = append(locals, uleb32(1)...) // 1 i32 -> index 5 (localI32b, PW5 table addr)
	locals = append(locals, wvtI32)
	locals = append(locals, uleb32(1)...) // 1 i64 -> index 6 (localI64c, PW5 key/slot value)
	locals = append(locals, wvtI64)
	locals = append(locals, uleb32(1)...) // 1 i32 -> index 7 (localSavedTop, PW10 zero-cross ③a)
	locals = append(locals, wvtI32)

	funcBody := append([]byte{}, locals...)
	funcBody = append(funcBody, body...)
	funcBody = append(funcBody, opEnd)

	var p []byte
	p = append(p, uleb32(1)...) // count = 1 function
	p = append(p, uleb32(uint32(len(funcBody)))...)
	p = append(p, funcBody...)
	return sectionOf(0x0a, p)
}

// --- section / import encoding helpers ---

func sectionOf(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uleb32(uint32(len(payload)))...)
	return append(out, payload...)
}

// importEntry builds one import entry: mod_name + field_name + kind + desc.
//   - memory: kind=0x02, desc=limits(flags+min[+max])
//   - func:   kind=0x00, desc=type index(uleb)
func importEntry(mod, name string, kind byte, desc []byte) []byte {
	var p []byte
	p = append(p, byte(len(mod)))
	p = append(p, mod...)
	p = append(p, byte(len(name)))
	p = append(p, name...)
	p = append(p, kind)
	p = append(p, desc...)
	return p
}

func importFuncEntry(mod, name string, typeIdx uint32) []byte {
	return importEntry(mod, name, 0x00, uleb32(typeIdx))
}

// uleb32 unsigned LEB128 (for module assembly; same algorithm as emitter.uleb,
// kept as a standalone function to avoid depending on an emitter instance).
func uleb32(v uint32) []byte {
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
