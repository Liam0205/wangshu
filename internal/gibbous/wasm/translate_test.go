//go:build wangshu_p3

package wasm

import (
	"context"
	"math"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
	"github.com/Liam0205/wangshu/internal/value"
)

// mockHost is a test double for HostState; it records helper calls and provides controllable behavior.
type mockHost struct {
	returnCalls        []retCall
	getUpvalFn         func(base, b int32) uint64
	globalsRaw         uint64
	gcPendingAddr      uint32
	loopBudgetAddr     uint32
	ciTransferAddr     uint32
	ciDepthAddr        uint32
	ciSegBaseAddr      uint32
	openGuardAddr      uint32
	topAddr            uint32
	protoCacheBaseAddr uint32
	fastCallHitsAddr   uint32
	getGlobalCalls     int
	getGlobalFn        func(base, pc, a, bx int32) int32
}

type retCall struct{ base, pc, a, b int32 }

func (m *mockHost) GetUpval(base, b int32) uint64 {
	if m.getUpvalFn != nil {
		return m.getUpvalFn(base, b)
	}
	return uint64(value.Nil)
}
func (m *mockHost) SetUpval(base, b int32, val uint64) {}
func (m *mockHost) DoReturn(base, pc, a, b int32) int32 {
	m.returnCalls = append(m.returnCalls, retCall{base, pc, a, b})
	return 0 // OK
}
func (m *mockHost) Safepoint(base, pc int32) int32 { return 0 }
func (m *mockHost) SetSavedPC(base, pc int32)      {}

func (m *mockHost) Arith(base, pc, op, b, c, a int32) int32 { return 0 }
func (m *mockHost) Unm(base, pc, b, a int32) int32          { return 0 }
func (m *mockHost) Len(base, pc, b, a int32) int32          { return 0 }
func (m *mockHost) Concat(base, pc, a, b, c int32) int32    { return 0 }
func (m *mockHost) Compare(base, pc, op, b, c int32) int32  { return 0 }
func (m *mockHost) Eq(base, pc, b, c int32) int32           { return 0 }
func (m *mockHost) ForPrep(base, pc, a int32) int32         { return 0 }

func (m *mockHost) GetTable(base, pc, a, b, c int32) int32 { return 0 }
func (m *mockHost) SetTable(base, pc, a, b, c int32) int32 { return 0 }
func (m *mockHost) DoGetGlobal(base, pc, a, bx int32) int32 {
	m.getGlobalCalls++
	if m.getGlobalFn != nil {
		return m.getGlobalFn(base, pc, a, bx)
	}
	return 0
}
func (m *mockHost) DoSetGlobal(base, pc, a, bx int32) int32 { return 0 }
func (m *mockHost) Self(base, pc, a, b, c int32) int32      { return 0 }
func (m *mockHost) NewTable(base, pc, a, b, c int32) int32  { return 0 }
func (m *mockHost) SetList(base, pc, a, b, c int32) int32   { return 0 }
func (m *mockHost) DoCall(base, pc, a, b, c int32) int64    { return int64(base) }
func (m *mockHost) TailCall(base, pc, a, b, c int32) int32  { return 0 }
func (m *mockHost) Closure(base, pc, a, bx int32) int32     { return 0 }
func (m *mockHost) Close(base, pc, a int32) int32           { return 0 }
func (m *mockHost) TForLoop(base, pc, a, c int32) int64     { return -2 }
func (m *mockHost) GlobalsRaw() uint64                      { return m.globalsRaw }
func (m *mockHost) GCPendingAddr() uint32                   { return m.gcPendingAddr }
func (m *mockHost) LoopBudgetAddr() uint32                  { return m.loopBudgetAddr }
func (m *mockHost) CITransferAddr() uint32                  { return m.ciTransferAddr }
func (m *mockHost) CIDepthAddr() uint32                     { return m.ciDepthAddr }
func (m *mockHost) CISegBaseAddr() uint32                   { return m.ciSegBaseAddr }
func (m *mockHost) OpenGuardAddr() uint32                   { return m.openGuardAddr }
func (m *mockHost) TopAddr() uint32                         { return m.topAddr }
func (m *mockHost) ProtoCacheBaseAddr() uint32              { return m.protoCacheBaseAddr }
func (m *mockHost) FastCallHitsAddr() uint32                { return m.fastCallHitsAddr }
func (m *mockHost) PopErrFrame()                            {}

// setupTranslator builds a complete, runnable P3 compilation environment: wazero runtime + memadapter
// holder (provides env.memory) + Compiler (mock host).
func setupTranslator(t *testing.T) (*Compiler, *memadapter.MemoryHolder, func()) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatalf("memadapter.New: %v", err)
	}
	c := NewCompiler(ctx, rt, &mockHost{})
	return c, holder, func() { _ = holder.Close(); _ = rt.Close(ctx) }
}

// TestPW10_SlotCapAligned honors the promise in compiler.go's maxTableSlots comment: it must
// match memadapter.TableSlots, the actual capacity of the shared env table. The two values are
// hardcoded by hand (the wasm package does not import memadapter in reverse, to avoid inverting the
// import cycle); this test is a safety net against "only one side got changed" — if
// maxTableSlots > TableSlots, Compile allocates a slot beyond env.table's capacity, the
// elementSection active write to table[slot] fails out of bounds at instantiation, and the
// table-full sentinel check breaks.
func TestPW10_SlotCapAligned(t *testing.T) {
	if maxTableSlots != memadapter.TableSlots {
		t.Fatalf("maxTableSlots(%d) != memadapter.TableSlots(%d):两值须一致,否则越界 slot 实例化失败",
			maxTableSlots, memadapter.TableSlots)
	}
}

// TestPW10_SlotRegistration verifies R1b: each promoted Proto is allocated a monotonic slot at
// compile time, and its run is really registered into the shared env.table[slot] via the element
// section — another caller module can call_indirect that slot through env.table to reach this
// Proto's run across modules (the physical basis for R3 direct calls).
func TestPW10_SlotRegistration(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()
	mem := holder.Memory()

	// Two straight-line Protos: each moves R0 to R1 (MOVE) then RETURN. After compilation each occupies one slot.
	mkProto := func() *bytecode.Proto {
		return &bytecode.Proto{Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}}
	}
	p0, p1 := mkProto(), mkProto()
	gc0, err := c.Compile(p0, nil)
	if err != nil {
		t.Fatalf("compile p0: %v", err)
	}
	defer func() { _ = gc0.(*p3Code).Dispose() }()
	gc1, err := c.Compile(p1, nil)
	if err != nil {
		t.Fatalf("compile p1: %v", err)
	}
	defer func() { _ = gc1.(*p3Code).Dispose() }()

	// Monotonic allocation: p0=slot0, p1=slot1.
	s0, ok0 := c.SlotOf(p0)
	s1, ok1 := c.SlotOf(p1)
	if !ok0 || !ok1 || s0 != 0 || s1 != 1 {
		t.Fatalf("slot allocation: p0=(%d,%v) p1=(%d,%v), want 0/1", s0, ok0, s1, ok1)
	}
	// An uncompiled Proto has no slot.
	if _, ok := c.SlotOf(mkProto()); ok {
		t.Error("未编译 Proto 不应有 slot")
	}

	// Verify element registration actually takes effect: the caller module call_indirects slot0
	// through env.table, reaching p0's run across modules (signature (i32 base)->(i32 status)).
	// p0 run does R0→R1. With R0=0xABCD laid out at base=0, after call_indirect slot0, R1 (offset 8)
	// should hold R0's value.
	const r0 = uint64(0xABCD_1234_5678_9EF0)
	mem.WriteUint64Le(0, r0)
	mem.WriteUint64Le(8, 0)

	caller := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		// type0 (i32)->(i32)
		0x01, 0x06, 0x01, 0x60, 0x01, 0x7f, 0x01, 0x7f,
		// import env.table (funcref flags=0 min=0)
		0x02, 0x0f, 0x01, 0x03, 'e', 'n', 'v', 0x05, 't', 'a', 'b', 'l', 'e', 0x01, 0x70, 0x00, 0x00,
		// func0 type0
		0x03, 0x02, 0x01, 0x00,
		// export "call0"
		0x07, 0x09, 0x01, 0x05, 'c', 'a', 'l', 'l', '0', 0x00, 0x00,
		// call0(base)= call_indirect[table0,type0]( base, (i32.const 0) )
		0x0a, 0x0b, 0x01, 0x09, 0x00, 0x20, 0x00, 0x41, 0x00, 0x11, 0x00, 0x00, 0x0b,
	}
	cmod, err := c.runtime.InstantiateWithConfig(c.ctx, caller,
		wazero.NewModuleConfig().WithName("pw10caller"))
	if err != nil {
		t.Fatalf("caller instantiate: %v", err)
	}
	defer func() { _ = cmod.Close(c.ctx) }()
	if _, err := cmod.ExportedFunction("call0").Call(c.ctx, 0); err != nil {
		t.Fatalf("call_indirect slot0 → p0.run: %v", err)
	}
	got, _ := mem.ReadUint64Le(8) // R1
	if got != r0 {
		t.Errorf("call_indirect slot0 did not run p0 (MOVE R1=R0): R1=%#x want %#x", got, r0)
	}
}

// TestPW2_TranslateMoveReturn end-to-end: translate a Proto of MOVE + RETURN, compile and run it
// with wazero, verifying (1) compilation succeeds (2) execution reports no error (3) MOVE really
// moves R(B) to R(A) (read back through shared memory).
func TestPW2_TranslateMoveReturn(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()

	// Proto: MOVE R1 R0; RETURN R1 2 (return 1 value)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}

	// SupportsAllOpcodes should let it through (MOVE/RETURN are on the whitelist, single BB, no string constants)
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("MOVE+RETURN proto should be supported")
	}

	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	// In shared memory, simulate some frame at base=0: R0 = 0x12345678 (at offset 0)
	mem := holder.Memory()
	const r0val = uint64(0x1234_5678_9ABC_DEF0)
	mem.WriteUint64Le(0, r0val)  // R0 at base+0
	mem.WriteUint64Le(8, 0xFFFF) // R1 old value

	// Run gibbous run (base=0)
	stack := make([]uint64, 1)
	status := gc.(*p3Code).Run(stack, 0)
	if status != 0 {
		t.Fatalf("run status = %d, want 0 (pendingErr=%v)", status, gc.(*p3Code).PendingErr())
	}

	// MOVE R1 R0 should move R0's value into R1 (offset 8)
	got, _ := mem.ReadUint64Le(8)
	if got != r0val {
		t.Errorf("MOVE failed: R1 = %#x, want %#x (R0)", got, r0val)
	}
}

// TestPW2_TranslateLoadNil translates LOADNIL R0 R2 (R0..R2 := nil) + RETURN.
func TestPW2_TranslateLoadNil(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 2, 0), // R0..R2 = nil
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("LOADNIL proto should be supported")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	mem := holder.Memory()
	// Prefill with non-nil values
	for i := uint32(0); i <= 2; i++ {
		mem.WriteUint64Le(i*8, 0xDEAD)
	}
	stack := make([]uint64, 1)
	if status := gc.(*p3Code).Run(stack, 0); status != 0 {
		t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
	}
	nilRaw := uint64(value.Nil)
	for i := uint32(0); i <= 2; i++ {
		got, _ := mem.ReadUint64Le(i * 8)
		if got != nilRaw {
			t.Errorf("LOADNIL R%d = %#x, want nil %#x", i, got, nilRaw)
		}
	}
}

// TestPW2_ReturnHelperCalled verifies RETURN goes through the h_return helper (pc materialization +
// return-value writeback semantics live inside the helper; here we verify the helper is called with
// the correct arguments).
func TestPW2_ReturnHelperCalled(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	mh := &mockHost{}
	c := NewCompiler(ctx, rt, mh)

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 3, 2, 0), // RETURN R3, 2 (1 return value)
		},
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	stack := make([]uint64, 1)
	gc.(*p3Code).Run(stack, 0)

	if len(mh.returnCalls) != 1 {
		t.Fatalf("h_return called %d times, want 1", len(mh.returnCalls))
	}
	rc := mh.returnCalls[0]
	if rc.a != 3 || rc.b != 2 || rc.pc != 0 {
		t.Errorf("h_return args = %+v, want a=3 b=2 pc=0", rc)
	}
}

// TestPW4_AcceptMultiBB verifies that a multi-BB (if-then) Proto containing
// JMP/TEST is now supported (PW4 relooper unlocks reducible multi-BB).
func TestPW4_AcceptMultiBB(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	// TEST + JMP + MOVE + RETURN (if cond then R1:=R0 end; multi-BB, reducible)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if !c.SupportsAllOpcodes(proto) {
		t.Error("reducible multi-BB proto (TEST+JMP) should be supported in PW4")
	}
}

// TestPW4_RejectUnsupportedOpcode verifies that a multi-BB Proto containing an
// unimplemented opcode (VARARG is never supported) is rejected.
func TestPW4_RejectUnsupportedOpcode(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.VARARG, 0, 1, 0), // never supported (F1 excludes vararg)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("proto with VARARG should NOT be supported")
	}
}

// TestPW2_RejectStringConst verifies that a LOADK with a string constant is
// rejected (a GCRef cannot be baked in at compile time).
func TestPW2_RejectStringConst(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts:       []value.Value{value.Nil}, // placeholder
		StringLitIdx: []int32{0},               // Consts[0] is a string placeholder
		StringLits:   []string{"hello"},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("proto with string const LOADK should NOT be supported in PW2")
	}
}

// TestPW3_ArithFastPath exercises the two-number arithmetic fast path (f64
// emitted directly inside Wasm, no helper call):
// verifies ADD/SUB/MUL/DIV/MOD each compile and execute with results
// byte-equal to value.NumberValue.
func TestPW3_ArithFastPath(t *testing.T) {
	cases := []struct {
		name string
		op   bytecode.OpCode
		x, y float64
		want float64
	}{
		{"ADD", bytecode.ADD, 3, 4, 7},
		{"SUB", bytecode.SUB, 10, 3, 7},
		{"MUL", bytecode.MUL, 6, 7, 42},
		{"DIV", bytecode.DIV, 20, 4, 5},
		{"MOD", bytecode.MOD, 17, 5, 2},
		{"MOD-neg", bytecode.MOD, -3, 5, 2}, // Lua semantics a-floor(a/b)*b
		{"DIV-zero", bytecode.DIV, 1, 0, 0}, // +Inf, special-cased below
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, holder, cleanup := setupTranslator(t)
			defer cleanup()
			// R0=x, R1=y; R2 := R0 op R1; RETURN R2 2
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(tc.op, 2, 0, 1),
					bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
				},
			}
			if !c.SupportsAllOpcodes(proto) {
				t.Fatalf("%s proto should be supported", tc.name)
			}
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			defer func() { _ = gc.(*p3Code).Dispose() }()

			mem := holder.Memory()
			mem.WriteUint64Le(0, uint64(value.NumberValue(tc.x)))
			mem.WriteUint64Le(8, uint64(value.NumberValue(tc.y)))
			stack := make([]uint64, 1)
			if status := gc.(*p3Code).Run(stack, 0); status != 0 {
				t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
			}
			gotRaw, _ := mem.ReadUint64Le(16) // R2
			// byte-equal: bit-for-bit identical to interpreter-side value.NumberValue(want)
			if tc.name == "DIV-zero" {
				// 1/0 = +Inf
				wantRaw := uint64(value.NumberValue(math.Inf(1)))
				if gotRaw != wantRaw {
					t.Errorf("%s = %#x, want +Inf %#x", tc.name, gotRaw, wantRaw)
				}
				return
			}
			wantRaw := uint64(value.NumberValue(tc.want))
			if gotRaw != wantRaw {
				t.Errorf("%s = %#x (%v), want %#x (%v)", tc.name,
					gotRaw, value.AsNumber(value.Value(gotRaw)), wantRaw, tc.want)
			}
		})
	}
}

// TestPW3_UnmNotFastPath verifies straight-line translation of UNM/NOT (fast path, no helper call).
func TestPW3_UnmNotFastPath(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()
	// R1 := -R0; R2 := not R1; RETURN R0 1
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.UNM, 1, 0, 0),
			bytecode.EncodeABC(bytecode.NOT, 2, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("UNM+NOT proto should be supported")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	mem := holder.Memory()
	mem.WriteUint64Le(0, uint64(value.NumberValue(5)))
	stack := make([]uint64, 1)
	if status := gc.(*p3Code).Run(stack, 0); status != 0 {
		t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
	}
	// R1 = -5
	r1, _ := mem.ReadUint64Le(8)
	if r1 != uint64(value.NumberValue(-5)) {
		t.Errorf("UNM R1 = %v, want -5", value.AsNumber(value.Value(r1)))
	}
	// R2 = not(-5) = false (-5 is truthy)
	r2, _ := mem.ReadUint64Le(16)
	if r2 != falseRawU64() {
		t.Errorf("NOT R2 = %#x, want false %#x", r2, falseRawU64())
	}
}

// TestPW5_GetGlobalInlineHit proves the GETGLOBAL inline fast path **really
// walks the hash** (no helper call). It hand-lays a globals table in shared
// memory (6-word header + 1 node slot), prefills the IC snapshot with NodeHit,
// and poisons the helper (any call returns 1 = failure). If Run succeeds and
// R(A) holds the node value, it proves the inline gen check + direct node-slot
// read path executed with **zero helper calls** (getGlobalCalls==0).
func TestPW5_GetGlobalInlineHit(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	mem := holder.Memory()

	// Place the table header at memory offset 256 (clear of the low value-stack region); the node segment follows it.
	const tblOff = 256
	const nodeOff = tblOff + 48 // 6-word header = 48 bytes
	const gen = 42
	const nodeVal = uint64(0x3FF0_0000_0000_0000) // bits of f64 1.0 (a valid number value)

	// word1: asize=0 | hmask=0 (single node); word3: nodeRef=nodeOff; word5: gen<<32
	mem.WriteUint64Le(tblOff+8, 0)                // sizes
	mem.WriteUint64Le(tblOff+16, 0)               // arrayRef
	mem.WriteUint64Le(tblOff+24, uint64(nodeOff)) // nodeRef (word3)
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32) // word5 gen
	// node slot 0: key (arbitrary) / val=nodeVal / next=-1
	mem.WriteUint64Le(nodeOff+0, 0xFFFB_0000_0000_0001) // key (string-ish, inline does not check)
	mem.WriteUint64Le(nodeOff+8, nodeVal)               // val
	mem.WriteUint64Le(nodeOff+16, 0xFFFFFFFF)           // next=-1

	globalsRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	mh := &mockHost{
		globalsRaw:  globalsRaw,
		getGlobalFn: func(base, pc, a, bx int32) int32 { return 1 }, // poison: any call fails
	}
	c := NewCompiler(ctx, rt, mh)

	// Proto: GETGLOBAL R0 K0; RETURN R0 2. Prefill IC[0] = NodeHit/gen=42/index=0.
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.GETGLOBAL, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts: []value.Value{value.Nil}, // K0 placeholder (inline does not read the key)
		IC:     make([]bytecode.ICSlot, 2),
	}
	proto.IC[0] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: gen, Index: 0, TableRef: uint32(tblOff)}

	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("GETGLOBAL+RETURN proto should be supported")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	stack := make([]uint64, 1)
	if status := gc.(*p3Code).Run(stack, 0); status != 0 {
		t.Fatalf("run status=%d (inline 快路径应不调 poison 助手;pendingErr=%v)",
			status, gc.(*p3Code).PendingErr())
	}
	if mh.getGlobalCalls != 0 {
		t.Errorf("h_getglobal 被调 %d 次,inline 快路径应跳哈希零调用", mh.getGlobalCalls)
	}
	got, _ := mem.ReadUint64Le(0) // R0
	if got != nodeVal {
		t.Errorf("GETGLOBAL inline R0 = %#x, want node val %#x", got, nodeVal)
	}
}

// TestPW5_GetTableInlineHit proves the GETTABLE inline fast path really walks the hash (no helper call).
// It hand-lays a table (header + array segment + node segment) and poisons h_gettable; a hit means zero helper calls.
func TestPW5_GetTableInlineHit(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	mem := holder.Memory()

	const tblOff = 512
	const arrOff = tblOff + 48 // 6-word header
	const gen = 7
	const arrVal = uint64(0x4010_0000_0000_0000) // f64 4.0

	// table header: asize=4 | hmask=0; arrayRef=arrOff; nodeRef=0; gen<<32
	mem.WriteUint64Le(tblOff+8, uint64(4))        // sizes: asize=4
	mem.WriteUint64Le(tblOff+16, uint64(arrOff))  // arrayRef (word2)
	mem.WriteUint64Le(tblOff+24, 0)               // nodeRef
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32) // gen
	// array segment: idx0 = arrVal
	mem.WriteUint64Le(arrOff+0, arrVal)

	tblRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	poison := 0
	mh := &mockHost{}
	c := NewCompiler(ctx, rt, mh)
	// Poisoning GetTable: reusing mockHost.getGlobalFn does not work, and adding a counter closure is awkward;
	// instead just assert on R(A)'s hit value (poisoning by making the helper return a wrong value is hard; here we
	// rely on R(A) correctness plus the helper returning 0 without writing R(A) to distinguish: preset R0 to a
	// sentinel, a hit changes it to arrVal, and the helper mock returns 0 without writing R(A) → R0 stays sentinel →
	// distinguishes inline vs helper).
	_ = poison

	// Proto: GETTABLE R0 R1 R2 (t=R1, key=R2); RETURN R0 2
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		IC: make([]bytecode.ICSlot, 2),
	}
	// ArrayHit: Index=0 (key 1); gen=7
	proto.IC[0] = bytecode.ICSlot{Kind: bytecode.ICKindArrayHit, Shape: gen, Index: 0, TableRef: uint32(tblOff)}

	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("GETTABLE proto should be supported")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	const sentinel = uint64(0xDEAD_BEEF_0000_0000)
	mem.WriteUint64Le(0, sentinel)               // R0 sentinel
	mem.WriteUint64Le(8, tblRaw)                 // R1 = table
	mem.WriteUint64Le(16, 0x3FF0_0000_0000_0000) // R2 = f64 1.0 (key 1 → ArrayHit idx0)

	stack := make([]uint64, 1)
	if status := gc.(*p3Code).Run(stack, 0); status != 0 {
		t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
	}
	got, _ := mem.ReadUint64Le(0)
	if got != arrVal {
		t.Errorf("GETTABLE inline ArrayHit R0 = %#x, want %#x (mock 助手不写 R(A),非 sentinel 即 inline 命中)", got, arrVal)
	}
}

// TestPW5_SelfInlineHit proves SELF inline: R(A+1):=R(B) self passthrough plus
// R(A) reaching the method slot directly via the IC (NodeHit const-key). Poisoning
// relies on a sentinel: the mock helper does not write R(A), so obtaining the
// method value on a hit proves inline execution.
func TestPW5_SelfInlineHit(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	holder, err := memadapter.New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	mem := holder.Memory()

	const tblOff = 768
	const nodeOff = tblOff + 48
	const gen = 3
	const methodVal = uint64(0xFFFA_0000_0000_0042) // fake closure value (tag=Function)

	mem.WriteUint64Le(tblOff+8, 0)                      // sizes: asize=0|hmask=0
	mem.WriteUint64Le(tblOff+16, 0)                     // arrayRef
	mem.WriteUint64Le(tblOff+24, uint64(nodeOff))       // nodeRef
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32)       // gen
	mem.WriteUint64Le(nodeOff+0, 0xFFFB_0000_0000_0009) // key (string-ish)
	mem.WriteUint64Le(nodeOff+8, methodVal)             // val (method)
	mem.WriteUint64Le(nodeOff+16, 0xFFFFFFFF)           // next=-1

	tblRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	mh := &mockHost{}
	c := NewCompiler(ctx, rt, mh)

	// Proto: SELF R0 R2 K0 (obj=R2, method name=K0); RETURN R0 3 (returns R0, R1)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 0, 2, bytecode.MaxK), // C=MaxK = K0 (constant key)
			bytecode.EncodeABC(bytecode.RETURN, 0, 3, 0),
		},
		Consts: []value.Value{value.Nil}, // K0 placeholder
		IC:     make([]bytecode.ICSlot, 2),
	}
	proto.IC[0] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: gen, Index: 0, TableRef: uint32(tblOff)}

	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("SELF proto should be supported")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	const sentinel = uint64(0xDEAD_BEEF_0000_0000)
	mem.WriteUint64Le(0, sentinel) // R0 sentinel (method slot)
	mem.WriteUint64Le(8, sentinel) // R1 sentinel (self slot A+1)
	mem.WriteUint64Le(16, tblRaw)  // R2 = obj

	stack := make([]uint64, 1)
	if status := gc.(*p3Code).Run(stack, 0); status != 0 {
		t.Fatalf("run status=%d pendingErr=%v", status, gc.(*p3Code).PendingErr())
	}
	r0, _ := mem.ReadUint64Le(0)
	r1, _ := mem.ReadUint64Le(8)
	if r1 != tblRaw {
		t.Errorf("SELF self 传递 R1 = %#x, want obj %#x", r1, tblRaw)
	}
	if r0 != methodVal {
		t.Errorf("SELF inline R0 = %#x, want method %#x (非 sentinel 即 inline 命中)", r0, methodVal)
	}
}

// TestPW7_VarargRejected PW7-b: a Proto containing VARARG is not promoted (the
// whitelist excludes VARARG; 02 §3.7.3 "unreachable paths are never taken" is
// guaranteed by the whitelist -- vararg functions are already excluded by P2 F1,
// and even if a misclassification reached P3, SupportsAllOpcodes returns false to
// fall back to the interpreter).
func TestPW7_VarargRejected(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()
	// single BB: VARARG + RETURN.
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.VARARG, 0, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("含 VARARG 的 Proto 不应被支持(白名单不含 VARARG)")
	}
}
