//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ7_IdentityForm_NoOverwrite identity 形态(`function(x) return x end`)
// writeRetA=false 路径专属 prove-the-path 命中证据。
//
// **背景**:writeRetA=false 路径只有 e2e 隐性覆盖,缺 jit 包内显式正向单测;
// 加本测确保 identity 函数路径(R(A) 不被 mmap 段 dummy RAX 覆盖)有显式
// 命中证据。本测断言:
//   - SetReg 未被调用(R(A) 不被 mmap 段覆盖)
//   - DoReturn 调 1 次(弹帧路径走到)
//
// 这是 [[prove-the-path-under-test]] 实例——避免 writeRetA 逻辑误改后
// identity 函数静默错果(返参 → 返 nil)而 LOADK e2e 测试仍过的盲区。
func TestPJ7_IdentityForm_NoOverwrite(t *testing.T) {
	// `function(x) return x end` 编译为 RETURN 0 2(luac 优化形态,长度 1 / 2)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0), // 真执行:return R(0)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // dead(luac 尾部冗余)
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("identity 形态不应调 SetReg(R(0) 已是参数值,不应被覆盖),got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	// retA 应是 RETURN 的 A=0
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0", host.lastReturnA)
	}
	// retB = 2(返 1 个值)
	if host.lastReturnB != 2 {
		t.Errorf("DoReturn B = %d, want 2", host.lastReturnB)
	}
	// retPC = 0(首条 RETURN 是真执行的,长度 2 luac 优化形态)
	if host.lastReturnPC != 0 {
		t.Errorf("DoReturn PC = %d, want 0", host.lastReturnPC)
	}
}

// TestPJ7_EmptyFunctionForm_NoOverwrite `function() end` 单条 RETURN 0 1
// (B=1,0 返回值)形态,writeRetA=false + retB=1 双重不写槽。
func TestPJ7_EmptyFunctionForm_NoOverwrite(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // RETURN 0 1
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("空函数形态不应调 SetReg, got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	if host.lastReturnB != 1 {
		t.Errorf("DoReturn B = %d, want 1(0 返回值)", host.lastReturnB)
	}
}

// TestPJ7_MoveForm_RetargetA MOVE A B + RETURN A 2 形态:retA 应被设为 B
// (跳过 R(A) = R(B) 中转,直接返 R(B))。
func TestPJ7_MoveForm_RetargetA(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0), // MOVE 1 0(R(1) = R(0))
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("MOVE+RETURN 形态不应调 SetReg(R(B) 已是参数值), got regs=%v", host.regs)
	}
	// DoReturn 的 A 应是 MOVE 的 B(0,跳过中转直接返 R(0))
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0(retA 设为 MOVE B 跳过中转)", host.lastReturnA)
	}
}

// TestPJ7_GetUpvalForm_HostRoute GETUPVAL A B + RETURN A 2 形态:Run 调
// host.GetUpval + SetReg(prelude opcode 路径)。
func TestPJ7_GetUpvalForm_HostRoute(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 0, 0), // GETUPVAL 0 0
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	// 预设 upval[0] = NaN-box number 99
	expected := uint64(value.NumberValue(99))
	host.upvals[0] = expected
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("GETUPVAL 形态:Run 应经 host.SetReg(0, upval[0]) 写 R(0)")
	}
	if got != expected {
		t.Errorf("R(0) = 0x%x, want 0x%x(upval[0] 经 GetUpval 路径写入)", got, expected)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}
