//go:build wangshu_p3

package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
	"github.com/Liam0205/wangshu/internal/value"
)

// mockHost 是 HostState 的测试替身,记录 helper 调用并提供可控行为。
type mockHost struct {
	returnCalls []retCall
	getUpvalFn  func(base, b int32) uint64
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

// TestPW2_RejectMultiBB 含 JMP 的多 BB Proto 被 SupportsAllOpcodes 拒
// (PW2 单 BB 限制;PW3 relooper 解锁)。
func TestPW2_RejectMultiBB(t *testing.T) {
	c, _, cleanup := setupTranslator(t)
	defer cleanup()

	// TEST + JMP + MOVE + RETURN(多 BB)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.TEST, 0, 0, 0),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("multi-BB proto (with JMP) should NOT be supported in PW2")
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
