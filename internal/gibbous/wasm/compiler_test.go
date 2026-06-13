//go:build wangshu_p3

package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// PW1 验收(02-translation §1.3 + 00-overview §4 PW1 完成定义):
// SupportsAllOpcodes 永远 false ⇒ 无 Proto 升层 ⇒ 与 P1-only byte-equal。

func newTestCompiler(t *testing.T) (*Compiler, func()) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	c := NewCompiler(ctx, rt)
	return c, func() { _ = rt.Close(ctx) }
}

// TestPW1_SupportsAllOpcodes_AlwaysFalse PW1 supported 全 false ⇒ 任何含
// 指令的 Proto 都判不支持(F7 拦下,无 Proto 升层)。
func TestPW1_SupportsAllOpcodes_AlwaysFalse(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	cases := []struct {
		name string
		code []bytecode.Instruction
	}{
		{"single MOVE", []bytecode.Instruction{bytecode.Instruction(uint32(bytecode.MOVE))}},
		{"single ADD", []bytecode.Instruction{bytecode.Instruction(uint32(bytecode.ADD))}},
		{"single RETURN", []bytecode.Instruction{bytecode.Instruction(uint32(bytecode.RETURN))}},
		{"multi op", []bytecode.Instruction{
			bytecode.Instruction(uint32(bytecode.LOADK)),
			bytecode.Instruction(uint32(bytecode.ADD)),
			bytecode.Instruction(uint32(bytecode.RETURN)),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &bytecode.Proto{Code: tc.code}
			if c.SupportsAllOpcodes(p) {
				t.Errorf("PW1 SupportsAllOpcodes must be false for %q (supported 全 false)", tc.name)
			}
		})
	}
}

// TestPW1_Compile_DeclinesGracefully PW1 Compile 占位返回 BackendDeclined
// error(不 panic,不崩溃)——即便 F7 漏判调到它,P2 也能转 TierStuck。
func TestPW1_Compile_DeclinesGracefully(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()

	p := &bytecode.Proto{Code: []bytecode.Instruction{
		bytecode.Instruction(uint32(bytecode.RETURN)),
	}}
	gc, err := c.Compile(p, nil)
	if gc != nil {
		t.Errorf("PW1 Compile must return nil GibbousCode, got %v", gc)
	}
	if err == nil {
		t.Fatal("PW1 Compile must return error (declined)")
	}
	ce, ok := err.(*bridge.CompileError)
	if !ok {
		t.Fatalf("expected *bridge.CompileError, got %T", err)
	}
	if ce.Kind != bridge.CompileErrBackendDeclined {
		t.Errorf("expected BackendDeclined, got %v", ce.Kind)
	}
}

// TestPW1_Compiler_ImplementsP3Compiler 编译期断言 Compiler 实现接口
// (静态保证 bridge.P3Compiler 契约完整)。
func TestPW1_Compiler_ImplementsP3Compiler(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	var _ bridge.P3Compiler = c // 不编译即接口不满足
}

// TestPW1_EmptyProto_SupportedVacuously 空 Proto(无指令)vacuously supported
// ——但 PW1 实际不会有空 Proto 过 F7(P2 不判空 Proto 为热点)。仅记录语义。
func TestPW1_EmptyProto_SupportedVacuously(t *testing.T) {
	c, cleanup := newTestCompiler(t)
	defer cleanup()
	if !c.SupportsAllOpcodes(&bytecode.Proto{Code: nil}) {
		t.Error("empty Proto should be vacuously supported (no unsupported op)")
	}
}
