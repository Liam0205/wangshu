//go:build wangshu_p4

package jit

import (
	"errors"
	"math"
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// disposable 包装 cast：bridge.GibbousCode 接口未含 Dispose,但 p4Code 实装
// 持有 mmap 段需要释放。测试经类型断言取得包内方法。
type disposable interface {
	Dispose() error
}

func tryDispose(t *testing.T, gc bridge.GibbousCode) {
	t.Helper()
	if d, ok := gc.(disposable); ok {
		if err := d.Dispose(); err != nil {
			t.Errorf("Dispose failed: %v", err)
		}
	}
}

// mockP4Host 是 P4HostState 的测试替身——记录 SetReg / DoReturn 调用,供
// 单测断言 Run 路径写值正确。
//
// PJ7 真接入路径(p4Code.Run)经 host.SetReg 写 R(retA),host.DoReturn
// 弹帧。单测用本 mock 验证「mmap 段执行 + 拿值 + SetReg 路径走到 + 值正确」。
type mockP4Host struct {
	regs          map[int32]uint64 // 写入的 R(idx) → val
	doReturnCalls int
	lastReturnPC  int32
	lastReturnA   int32
	lastReturnB   int32
	upvals        map[int32]uint64 // 模拟 upvalue 表(GetUpval 用)
}

func newMockP4Host() *mockP4Host {
	return &mockP4Host{
		regs:   make(map[int32]uint64),
		upvals: make(map[int32]uint64),
	}
}

func (m *mockP4Host) DoReturn(base int32, pc int32, a int32, b int32) int32 {
	m.doReturnCalls++
	m.lastReturnPC = pc
	m.lastReturnA = a
	m.lastReturnB = b
	return 0
}

func (m *mockP4Host) SetReg(idx int32, val uint64) {
	m.regs[idx] = val
}

func (m *mockP4Host) GetUpval(base int32, b int32) uint64 {
	_ = base
	return m.upvals[b]
}

// compileWithHost 构造 *Compiler 注入 mock host 后调 Compile。
func compileWithHost(t *testing.T, p *bytecode.Proto) (bridge.GibbousCode, *mockP4Host) {
	t.Helper()
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	gc, err := c.Compile(p, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	return gc, host
}

// `docs/design/p4-method-jit/00-overview.md` §4 + `06-backends.md` §6.1):
//
//   - SupportsAllOpcodes 全 false(supported 表初空,06 §3.8 渐进白名单纪律);
//   - Compile 对单 LOADK+RETURN 形态返真实 GibbousCode(PJ2 真接入);其它
//     形态返 ErrCompileUnsupportedShape;
//   - 实现 bridge.P3Compiler 接口(编译期断言已在 code.go,本测试运行期再
//     验一道,prove-the-path 命中证据)。

// TestPJ0_NewReturnsCompiler 构造 Compiler 不 nil。
func TestPJ0_NewReturnsCompiler(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("PJ0: New() should return non-nil Compiler")
	}
}

// TestPJ0_ImplementsP3Compiler 实现 bridge.P3Compiler 接口。
func TestPJ0_ImplementsP3Compiler(t *testing.T) {
	c := New()
	var iface bridge.P3Compiler = c
	if iface == nil {
		t.Fatal("PJ0: Compiler should satisfy bridge.P3Compiler")
	}
}

// TestPJ7_SupportsAllOpcodesGate PJ7 真接入:LOADK+RETURN 单 BB 形态返 true,
// 其它返 false。
func TestPJ7_SupportsAllOpcodesGate(t *testing.T) {
	c := New()
	rejectCases := []struct {
		name string
		p    *bytecode.Proto
	}{
		{
			name: "nil",
			p:    nil,
		},
		{
			name: "empty",
			p:    &bytecode.Proto{Code: []bytecode.Instruction{}},
		},
		{
			name: "MOVE+RETURN A 不一致 + B=1(MOVE 形态需 A 一致 + B=2)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				},
			},
		},
		{
			name: "LOADK+RETURN B!=2",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // B=1 不返回值
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN A 不一致",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
	}
	for _, tc := range rejectCases {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if c.SupportsAllOpcodes(tc.p) {
				t.Errorf("PJ7: %q should NOT be supported", tc.name)
			}
		})
	}

	acceptCase := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{value.NumberValue(42)},
		StringLitIdx: []int32{-1},
	}
	if !c.SupportsAllOpcodes(acceptCase) {
		t.Error("PJ7: LOADK+RETURN single-BB shape should be supported")
	}
}

// TestPJ2_CompileLoadKReturnSucceeds PJ2 真接入实证:Compile 对「LOADK A K(0);
// RETURN A 1」形态发射真 mmap 段 + 包装 *p4Code,Run 经 callJITFull 拿值
// 写回 stack 的 R(A) 槽位。
//
// **prove-the-path 命中证据**(承
// `llmdoc/guides/prove-the-path-under-test.md`):本测试经真实 Compile 路径
// → 真 mmap 段 → callJITFull → stack 写回,白盒证明:
//  1. emitter 路径被走到(Compile 调 EmitMovRaxImm64 + EmitRet);
//  2. mmap+W^X 翻面工作(MmapCode 返 *CodePage);
//  3. callJITFull 跳进段 + 段内 mov+ret 工作(RAX = NaN-box const);
//  4. p4Code.Run 写回 stack 正确 NaN-box 值。
//
// 注意:**SupportsAllOpcodes 仍全 false** ⇒ 本路径不被 bridge 主路径走到;
// 本测试是 PJ2 内部 prove-the-path 验证 mmap 段被真走到 + 值正确。
func TestPJ2_CompileLoadKReturnSucceeds(t *testing.T) {
	cases := []struct {
		name  string
		konst value.Value
	}{
		{"number 0", value.NumberValue(0)},
		{"number 1", value.NumberValue(1)},
		{"number 3.14", value.NumberValue(3.14)},
		{"number -1", value.NumberValue(-1)},
		{"number Inf", value.NumberValue(math.Inf(1))},
		{"nil", value.Nil},
		{"bool true", value.BoolValue(true)},
		{"bool false", value.BoolValue(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Proto:LOADK 0 K(0); RETURN 0 2(R(0) = K(0); return R(0))。
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
				},
				Consts:       []value.Value{tc.konst},
				StringLitIdx: []int32{-1}, // 非字符串占位
			}
			gc, host := compileWithHost(t, proto)
			defer tryDispose(t, gc)

			// 经真实 Run 路径执行(host.SetReg 接收 R(retA) 值)。
			stack := make([]uint64, 4)
			status := gc.Run(stack, 0)
			if status != 0 {
				t.Errorf("Run status = %d, want 0(OK)", status)
			}
			// host.SetReg(0, val) 应该被调用,val == tc.konst
			got, ok := host.regs[0]
			if !ok {
				t.Fatal("SetReg(0, ...) not called")
			}
			if value.Value(got) != tc.konst {
				t.Errorf("SetReg(0, 0x%016x), want 0x%016x (%v)", got, uint64(tc.konst), tc.konst)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn called %d times, want 1", host.doReturnCalls)
			}
		})
	}
}

// TestPJ2_CompileLoadKReturnRetANonZero retA != 0 的形态(R(2) = K(0); return R(2))。
//
// 验证 retA 字段被正确传递 + Run 写到正确槽位(经 mock host.SetReg)。
func TestPJ2_CompileLoadKReturnRetANonZero(t *testing.T) {
	konst := value.NumberValue(42)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
		Consts:       []value.Value{konst},
		StringLitIdx: []int32{-1},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[2]
	if !ok {
		t.Fatal("SetReg(2, ...) not called")
	}
	if value.Value(got) != konst {
		t.Errorf("SetReg(2, 0x%x), want 0x%x", got, uint64(konst))
	}
	if _, ok := host.regs[0]; ok {
		t.Errorf("SetReg(0, ...) should NOT be called for retA=2")
	}
}

// TestPJ2_CompileBaseNonZero base != 0 的形态(模拟嵌套调用帧)。
//
// SetReg 接受 idx,Run 路径不再依赖 base 参数(host 经 thread.cur.base 算
// 真实位置)。本测试验 retA 经 SetReg 传给 host 时不变。
func TestPJ2_CompileBaseNonZero(t *testing.T) {
	konst := value.NumberValue(99)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{konst},
		StringLitIdx: []int32{-1},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	stack := make([]uint64, 8)
	status := gc.Run(stack, 16) // base = 16 字节(p4Code.Run 不再读 base 参数,SetReg 不依赖它)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("SetReg(0, ...) not called")
	}
	if value.Value(got) != konst {
		t.Errorf("SetReg(0, 0x%x), want 0x%x", got, uint64(konst))
	}
}

// TestPJ2_CompileRejectsNonShape 拒非 LOADK+RETURN 单 BB 形态(承 Compile
// 的形态检查)。
func TestPJ2_CompileRejectsNonShape(t *testing.T) {
	c := New()
	cases := []struct {
		name string
		p    *bytecode.Proto
	}{
		{
			name: "nil",
			p:    nil,
		},
		{
			name: "empty code",
			p:    &bytecode.Proto{Code: []bytecode.Instruction{}},
		},
		{
			name: "single RETURN B!=1(2 个返回值,不在 PJ7 形态内)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.RETURN, 0, 3, 0), // B=3 即返回 2 个值
				},
			},
		},
		{
			name: "MOVE+RETURN A 不一致 + B=1(MOVE 形态需 A 一致 + B=2,本 case 双重不命中)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				},
			},
		},
		{
			name: "LOADK+JMP(JMP 不在 PJ2 范围)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeAsBx(bytecode.JMP, 0, 0),
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN(retA != loadA)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0), // retA=1 ≠ loadA=0
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN(B != 2)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // B=1 不返回值
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc, err := c.Compile(tc.p, nil)
			if !errors.Is(err, ErrCompileUnsupportedShape) {
				t.Errorf("Compile should return ErrCompileUnsupportedShape, got %v", err)
			}
			if gc != nil {
				t.Errorf("Compile should return nil GibbousCode on unsupported shape, got %v", gc)
			}
		})
	}
}

// TestPJ2_CompileToleratesNilFeedback feedback nil 不 panic(承 P3Compiler
// 接口契约)。
func TestPJ2_CompileToleratesNilFeedback(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Compile must tolerate nil feedback (P3Compiler 接口契约), panicked: %v", r)
		}
	}()
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{value.NumberValue(0)},
		StringLitIdx: []int32{-1},
	}
	gc, _ := c.Compile(proto, nil)
	if gc != nil {
		tryDispose(t, gc)
	}
}
