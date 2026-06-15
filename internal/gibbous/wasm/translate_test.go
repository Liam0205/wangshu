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
	gcPendingAddr  uint32
	ciTransferAddr uint32
	ciDepthAddr    uint32
	ciSegBaseAddr  uint32
	openGuardAddr  uint32
	topAddr        uint32
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
func (m *mockHost) DoCall(base, pc, a, b, c int32) int64    { return int64(base) }
func (m *mockHost) TailCall(base, pc, a, b, c int32) int32  { return 0 }
func (m *mockHost) Closure(base, pc, a, bx int32) int32     { return 0 }
func (m *mockHost) Close(base, pc, a int32) int32           { return 0 }
func (m *mockHost) TForLoop(base, pc, a, c int32) int64     { return -2 }
func (m *mockHost) GlobalsRaw() uint64                      { return m.globalsRaw }
func (m *mockHost) GCPendingAddr() uint32                   { return m.gcPendingAddr }
func (m *mockHost) CITransferAddr() uint32                  { return m.ciTransferAddr }
func (m *mockHost) CIDepthAddr() uint32                     { return m.ciDepthAddr }
func (m *mockHost) CISegBaseAddr() uint32                   { return m.ciSegBaseAddr }
func (m *mockHost) OpenGuardAddr() uint32                   { return m.openGuardAddr }
func (m *mockHost) TopAddr() uint32                         { return m.topAddr }
func (m *mockHost) PopErrFrame()                            {}

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

// TestPW10_SlotCapAligned 兑现 compiler.go maxTableSlots 注释承诺:它必须与
// env 共享表实际容量 memadapter.TableSlots 一致。两值手工硬编码(wasm 包不反向
// import memadapter 避免 import 环倒置),本测是防「只改一处」的安全网——若
// maxTableSlots > TableSlots,Compile 会分配超出 env.table 容量的 slot,
// elementSection active 写 table[slot] 在实例化时越界失败,且表满哨兵判定失效。
func TestPW10_SlotCapAligned(t *testing.T) {
	if maxTableSlots != memadapter.TableSlots {
		t.Fatalf("maxTableSlots(%d) != memadapter.TableSlots(%d):两值须一致,否则越界 slot 实例化失败",
			maxTableSlots, memadapter.TableSlots)
	}
}

// TestPW10_SlotRegistration 验证 R1b:每个升层 Proto 编译时分配单调 slot,且其
// run 经 element 段真注册进共享 env.table[slot]——另一 caller module 经 env.table
// call_indirect 该 slot 能跨 module 调到这个 Proto 的 run(R3 直调的物理基础)。
func TestPW10_SlotRegistration(t *testing.T) {
	c, holder, cleanup := setupTranslator(t)
	defer cleanup()
	mem := holder.Memory()

	// 两个直线 Proto:各把 R0 搬到 R1(MOVE)再 RETURN。编译后各占一个 slot。
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

	// 单调分配:p0=slot0, p1=slot1。
	s0, ok0 := c.SlotOf(p0)
	s1, ok1 := c.SlotOf(p1)
	if !ok0 || !ok1 || s0 != 0 || s1 != 1 {
		t.Fatalf("slot allocation: p0=(%d,%v) p1=(%d,%v), want 0/1", s0, ok0, s1, ok1)
	}
	// 未编译的 Proto 无 slot。
	if _, ok := c.SlotOf(mkProto()); ok {
		t.Error("未编译 Proto 不应有 slot")
	}

	// 实测 element 注册生效:caller module 经 env.table call_indirect slot0,跨
	// module 调到 p0 的 run(签名 (i32 base)->(i32 status))。p0 run 把 R0→R1。
	// 在 base=0 处布 R0=0xABCD,call_indirect slot0 后 R1(offset 8)应得 R0 值。
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

// TestPW4_RejectUnsupportedOpcode 含未实装 opcode(VARARG 永不支持)的多 BB 被拒。
func TestPW4_RejectUnsupportedOpcode(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.VARARG, 0, 1, 0), // 永不支持(F1 排除 vararg)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("proto with VARARG should NOT be supported")
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

// TestPW5_GetTableInlineHit 证明 GETTABLE inline 快路径真跳哈希(不调助手)。
// 手工布表(头 + array 段 + node 段),poison h_gettable;命中则零助手调用。
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
	const arrOff = tblOff + 48 // 头 6 字
	const gen = 7
	const arrVal = uint64(0x4010_0000_0000_0000) // f64 4.0

	// 表头:asize=4 | hmask=0;arrayRef=arrOff;nodeRef=0;gen<<32
	mem.WriteUint64Le(tblOff+8, uint64(4))        // sizes: asize=4
	mem.WriteUint64Le(tblOff+16, uint64(arrOff))  // arrayRef(word2)
	mem.WriteUint64Le(tblOff+24, 0)               // nodeRef
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32) // gen
	// array 段:idx0 = arrVal
	mem.WriteUint64Le(arrOff+0, arrVal)

	tblRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	poison := 0
	mh := &mockHost{}
	c := NewCompiler(ctx, rt, mh)
	// 用 GetTable poison:复用 mockHost.getGlobalFn 不行,加一个计数器闭包不便;
	// 改为断言 R(A) 命中值即可(poison 通过让助手返回错值难做;这里靠 R(A) 正确性 +
	// 助手返回 0 不写 R(A) 来区分:把 R0 预置成 sentinel,命中改成 arrVal,
	// 助手 mock 返回 0 但不写 R(A) → R0 仍 sentinel → 区分 inline vs helper)。
	_ = poison

	// Proto: GETTABLE R0 R1 R2(t=R1, key=R2);RETURN R0 2
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		IC: make([]bytecode.ICSlot, 2),
	}
	// ArrayHit:Index=0(键 1);gen=7
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

// TestPW5_SelfInlineHit 证明 SELF inline:R(A+1):=R(B) self 传递 + R(A) 经 IC
// 直达方法槽(NodeHit const-key)。poison 靠 sentinel:mock 助手不写 R(A),命中
// 得方法值即证 inline 执行。
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
	const methodVal = uint64(0xFFFA_0000_0000_0042) // 假 closure value(tag=Function)

	mem.WriteUint64Le(tblOff+8, 0)                      // sizes: asize=0|hmask=0
	mem.WriteUint64Le(tblOff+16, 0)                     // arrayRef
	mem.WriteUint64Le(tblOff+24, uint64(nodeOff))       // nodeRef
	mem.WriteUint64Le(tblOff+40, uint64(gen)<<32)       // gen
	mem.WriteUint64Le(nodeOff+0, 0xFFFB_0000_0000_0009) // key(string-ish)
	mem.WriteUint64Le(nodeOff+8, methodVal)             // val(方法)
	mem.WriteUint64Le(nodeOff+16, 0xFFFFFFFF)           // next=-1

	tblRaw := uint64(value.TagTable)<<48 | uint64(tblOff)
	mh := &mockHost{}
	c := NewCompiler(ctx, rt, mh)

	// Proto: SELF R0 R2 K0(obj=R2, method 名=K0);RETURN R0 3(返回 R0,R1)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 0, 2, bytecode.MaxK), // C=MaxK = K0(常量键)
			bytecode.EncodeABC(bytecode.RETURN, 0, 3, 0),
		},
		Consts: []value.Value{value.Nil}, // K0 占位
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
	mem.WriteUint64Le(0, sentinel) // R0 sentinel(方法槽)
	mem.WriteUint64Le(8, sentinel) // R1 sentinel(self 槽 A+1)
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

// TestPW7_VarargRejected PW7-b:含 VARARG 的 Proto 不升层(白名单不含 VARARG,
// 02 §3.7.3「不可达路径不被走到」由白名单保证——vararg 函数 P2 F1 已排除,
// 即便漏判到达 P3,SupportsAllOpcodes 也返 false fallback 解释器)。
func TestPW7_VarargRejected(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()
	// 单 BB:VARARG + RETURN。
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
