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

// mockHost 是 HostState 的测试替身,记录 helper 调用并提供可控行为。
type mockHost struct {
	returnCalls    []retCall
	getUpvalFn     func(base, b int32) uint64
	globalsRaw     uint64
	getGlobalCalls int
	getGlobalFn    func(base, pc, a, bx int32) int32
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
func (m *mockHost) Safepoint(base, pc int32)  {}
func (m *mockHost) SetSavedPC(base, pc int32) {}

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
func (m *mockHost) GlobalsRaw() uint64                      { return m.globalsRaw }

// setupTranslator 建一个完整可执行的 P3 编译环境:wazero runtime + memadapter
// holder(提供 env.memory)+ Compiler(mock host)。
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

// TestPW2_TranslateMoveReturn 端到端:翻译 MOVE + RETURN 的 Proto,wazero
// 编译执行,验证 ① 编译成功 ② 运行不报错 ③ MOVE 真的把 R(B) 搬到 R(A)
// (经共见 memory 读回)。
func TestPW2_TranslateMoveReturn(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()

	// Proto: MOVE R1 R0; RETURN R1 2(返回 1 个值)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}

	// SupportsAllOpcodes 应放行(MOVE/RETURN 在白名单,单 BB,无字符串常量)
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("MOVE+RETURN proto should be supported")
	}

	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer func() { _ = gc.(*p3Code).Dispose() }()

	// 在共见 memory 里,模拟某帧 base=0:R0 = 0x12345678(在 offset 0)
	mem := holder.Memory()
	const r0val = uint64(0x1234_5678_9ABC_DEF0)
	mem.WriteUint64Le(0, r0val)  // R0 at base+0
	mem.WriteUint64Le(8, 0xFFFF) // R1 旧值

	// 跑 gibbous run(base=0)
	stack := make([]uint64, 1)
	status := gc.(*p3Code).Run(stack, 0)
	if status != 0 {
		t.Fatalf("run status = %d, want 0 (pendingErr=%v)", status, gc.(*p3Code).PendingErr())
	}

	// MOVE R1 R0 应把 R0 的值搬到 R1(offset 8)
	got, _ := mem.ReadUint64Le(8)
	if got != r0val {
		t.Errorf("MOVE failed: R1 = %#x, want %#x (R0)", got, r0val)
	}
}

// TestPW2_TranslateLoadNil 翻译 LOADNIL R0 R2(R0..R2 := nil)+ RETURN。
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
	// 先填非 nil 值
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

// TestPW2_ReturnHelperCalled 验证 RETURN 经 h_return 助手(pc 物化 + 返回值
// 回填语义在助手内,这里验助手被以正确参数调用)。
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
			bytecode.EncodeABC(bytecode.RETURN, 3, 2, 0), // RETURN R3, 2 (1 个返回值)
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

// TestPW4_AcceptMultiBB 含 JMP/TEST 的多 BB(if-then)Proto 现被支持
// (PW4 relooper 解锁可约简多 BB)。
func TestPW4_AcceptMultiBB(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	// TEST + JMP + MOVE + RETURN(if cond then R1:=R0 end;多 BB,可约简)
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

// TestPW4_RejectUnsupportedOpcode 含未实装 opcode(CALL 是 PW6)的多 BB 被拒。
func TestPW4_RejectUnsupportedOpcode(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.CALL, 0, 1, 1), // PW6,未实装
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("proto with CALL (PW6) should NOT be supported yet")
	}
}

// TestPW2_RejectStringConst 含字符串常量的 LOADK 被拒(编译期烧不出 GCRef)。
func TestPW2_RejectStringConst(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts:       []value.Value{value.Nil}, // 占位
		StringLitIdx: []int32{0},               // Consts[0] 是字符串占位
		StringLits:   []string{"hello"},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("proto with string const LOADK should NOT be supported in PW2")
	}
}

// TestPW3_ArithFastPath 双 number 算术快路径(Wasm 内直发 f64,不调助手):
// 验证 ADD/SUB/MUL/DIV/MOD 各 opcode 编译执行 + 结果 byte-equal value.NumberValue。
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
		{"MOD-neg", bytecode.MOD, -3, 5, 2}, // Lua 语义 a-floor(a/b)*b
		{"DIV-zero", bytecode.DIV, 1, 0, 0}, // +Inf,下面特判
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
			// byte-equal:与解释器侧 value.NumberValue(want) 逐位一致
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

// TestPW3_UnmNotFastPath UNM/NOT 直线翻译(快路径,不调助手)。
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
	// R2 = not(-5) = false(-5 是 truthy)
	r2, _ := mem.ReadUint64Le(16)
	if r2 != falseRawU64() {
		t.Errorf("NOT R2 = %#x, want false %#x", r2, falseRawU64())
	}
}

// TestPW5_GetGlobalInlineHit 证明 GETGLOBAL inline 快路径**真跳哈希**(不调助手)。
// 手工在共见 memory 里布一张 globals 表(头 6 字 + 1 个 node 槽),IC 快照预填
// NodeHit;poison 助手(调用即 return 1 失败)。若 Run 成功且 R(A) 得 node 值,
// 证明 inline gen 校验 + node 槽直读路径执行,**助手零调用**(getGlobalCalls==0)。
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

	// 表头放 memory offset 256(避开值栈低区);node 段紧随其后。
	const tblOff = 256
	const nodeOff = tblOff + 48 // 头 6 字 = 48 字节
	const gen = 42
	const nodeVal = uint64(0x3FF0_0000_0000_0000) // f64 1.0 的 bits(合法 number value)

	// word1: asize=0 | hmask=0(单 node);word3: nodeRef=nodeOff;word5: gen<<32
	mem.WriteUint64Le(tblOff+8, 0)                // sizes
	mem.WriteUint64Le(tblOff+16, 0)               // arrayRef
	mem.WriteUint64Le(tblOff+24, uint64(nodeOff)) // nodeRef(word3)
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32) // word5 gen
	// node 槽 0:key(任意)/val=nodeVal/next=-1
	mem.WriteUint64Le(nodeOff+0, 0xFFFB_0000_0000_0001) // key(string-ish,inline 不校验)
	mem.WriteUint64Le(nodeOff+8, nodeVal)               // val
	mem.WriteUint64Le(nodeOff+16, 0xFFFFFFFF)           // next=-1

	globalsRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	mh := &mockHost{
		globalsRaw:  globalsRaw,
		getGlobalFn: func(base, pc, a, bx int32) int32 { return 1 }, // poison:调即失败
	}
	c := NewCompiler(ctx, rt, mh)

	// Proto: GETGLOBAL R0 K0; RETURN R0 2。预填 IC[0] = NodeHit/gen=42/index=0。
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.GETGLOBAL, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts: []value.Value{value.Nil}, // K0 占位(inline 不读 key)
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
