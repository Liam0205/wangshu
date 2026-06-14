//go:build wangshu_p3

package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// PW1/PW2 Compiler 基础验收。SupportsAllOpcodes 白名单随 PW 推进扩充。

func newTestCompiler(t *testing.T) (*Compiler, func()) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	c := NewCompiler(ctx, rt, &mockHost{})
	return c, func() { _ = rt.Close(ctx) }
}

// TestCompiler_SupportsWhitelist PW2 白名单:直线 opcode 支持,未实装的拒。
func TestCompiler_SupportsWhitelist(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	// 单 BB 直线 opcode → 支持
	supported := []struct {
		name string
		code []bytecode.Instruction
	}{
		{"MOVE+RETURN", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}},
		{"LOADNIL+RETURN", []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.LOADNIL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		}},
	}
	for _, tc := range supported {
		t.Run("yes/"+tc.name, func(t *testing.T) {
			if !c.SupportsAllOpcodes(&bytecode.Proto{Code: tc.code}) {
				t.Errorf("%q should be supported in PW2", tc.name)
			}
		})
	}

	// 未实装 opcode(ADD 是 PW3)→ 拒
	notYet := []bytecode.Instruction{
		bytecode.EncodeABC(bytecode.ADD, 0, 0, 1),
		bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
	}
	if c.SupportsAllOpcodes(&bytecode.Proto{Code: notYet}) {
		t.Error("ADD (PW3) should NOT be supported in PW2")
	}
}

// TestCompiler_ImplementsP3Compiler 编译期断言 Compiler 实现接口。
func TestCompiler_ImplementsP3Compiler(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	var _ bridge.P3Compiler = c
}

// TestCompiler_PanicRecover Compile 内部 panic 被 defer recover 兜底转
// *CompileError(BackendPanic),不穿越接口(02 §1.4)。
//
// 用一个会让 translate panic 的畸形 Proto 触发(Consts 越界:LOADK 引用
// 不存在的常量索引)。SupportsAllOpcodes 先放行(数字常量假设),Compile
// 翻译时 Consts[bx] 越界 panic → recover。
func TestCompiler_PanicRecover(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	// LOADK 引用 Consts[5] 但 Consts 为空 → translate emitLoadK 越界 panic
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 5),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: nil, // 空 → Consts[5] 越界
	}
	// SupportsAllOpcodes:LOADK 非字符串常量(StringLitIdx 空)→ 放行;单 BB。
	if !c.SupportsAllOpcodes(proto) {
		t.Skip("proto not supported, panic path not reachable")
	}
	gc, err := c.Compile(proto, nil)
	if gc != nil {
		t.Error("panic path should return nil GibbousCode")
	}
	if err == nil {
		t.Fatal("panic should be recovered to error")
	}
	if ce, ok := err.(*bridge.CompileError); !ok || ce.Kind != bridge.CompileErrBackendPanic {
		t.Errorf("expected BackendPanic CompileError, got %v", err)
	}
}

// TestCompiler_EmptyProtoVacuous 空 Proto vacuously supported(无 unsupported op)。
func TestCompiler_EmptyProtoVacuous(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	if !c.SupportsAllOpcodes(&bytecode.Proto{Code: nil}) {
		t.Error("empty Proto should be vacuously supported")
	}
}
