//go:build wangshu_p4

package jit

import (
	"errors"
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// PJ0 阶段 Compiler 验收口径(承
// `docs/design/p4-method-jit/00-overview.md` §4 PJ0 行 + `06-backends.md`
// §6.1 PJ0 验收):
//
//   - SupportsAllOpcodes 全 false(supported 表初空,06 §3.8 渐进白名单纪律);
//   - Compile 返 ErrCompileNotImplemented(防御性兜底——bridge 不应在 PJ0
//     调到本函数,因 F7 已拦下;若 force-all 类测试绕过仍能 fallback);
//   - 实现 bridge.P3Compiler 接口(编译期断言已在 code.go,本测试运行期再
//     验一道—— interface satisfaction 漂移即编译失败,但显式 cast 让测试
//     形成 prove-the-path 命中证据)。

// TestPJ0_NewReturnsCompiler 构造 Compiler 不 nil(承 wireP4 注入路径)。
func TestPJ0_NewReturnsCompiler(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("PJ0: New() should return non-nil Compiler (wireP4 依赖此返非 nil 才注入 bridge)")
	}
}

// TestPJ0_ImplementsP3Compiler 实现 bridge.P3Compiler 接口(运行期断言)。
//
// 注:code.go 已有编译期 `var _ bridge.P3Compiler = (*Compiler)(nil)`,
// 本测试是 prove-the-path-under-test 纪律(`llmdoc/guides/prove-the-path-under-test.md`)
// 的 PJ0 应用——经 cast 路径走一遍,使「接口签名不漂」成为运行期可见证据。
func TestPJ0_ImplementsP3Compiler(t *testing.T) {
	c := New()
	var iface bridge.P3Compiler = c // 编译期断言
	if iface == nil {
		t.Fatal("PJ0: Compiler should satisfy bridge.P3Compiler")
	}
}

// TestPJ0_SupportsAllOpcodesAlwaysFalse PJ0 关键验收口径(00 §4 PJ0):
// 「bridge 注入 P4Compiler 后 SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent」。
//
// 任何 opcode 形态都返 false——这是「supported 表初空 + 保守缺省」纪律
// (06 §3.8)的 PJ0 实装。PJ1+ 渐进扩充时本测试会变,不在「永远 false」上
// 立死断言,而是验证「PJ0 状态」。
func TestPJ0_SupportsAllOpcodesAlwaysFalse(t *testing.T) {
	c := New()
	cases := []struct {
		name string
		code []bytecode.Instruction
	}{
		{
			name: "empty",
			code: []bytecode.Instruction{},
		},
		{
			name: "MOVE+RETURN(PJ1 即将支持)",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "LOADK+RETURN(PJ1 即将支持)",
			code: []bytecode.Instruction{
				bytecode.EncodeABx(bytecode.LOADK, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "ADD+RETURN(PJ2 即将支持)",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.ADD, 0, 0, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
			},
		},
		{
			name: "VARARG(F1 永不支持)",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.VARARG, 0, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if c.SupportsAllOpcodes(&bytecode.Proto{Code: tc.code}) {
				t.Errorf("PJ0: %q should NOT be supported (PJ0 supported 表初空 / 全 false 是验收口径)", tc.name)
			}
		})
	}
}

// TestPJ0_CompileReturnsNotImplemented PJ0 防御性兜底:Compile 返
// ErrCompileNotImplemented(bridge 不应在 PJ0 调到这里,但 force-all 类
// 测试可能绕过 F7;返错让 fallback 到 TierStuck,行为同 P3 编译失败)。
func TestPJ0_CompileReturnsNotImplemented(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, err := c.Compile(proto, nil)
	if gc != nil {
		t.Errorf("PJ0: Compile should return nil GibbousCode, got %v", gc)
	}
	if !errors.Is(err, ErrCompileNotImplemented) {
		t.Errorf("PJ0: Compile should return ErrCompileNotImplemented, got %v", err)
	}
}

// TestPJ0_CompileToleratesNilFeedback feedback nil 不 panic(承 P3Compiler
// 接口契约 `p2-bridge/05-p3-p4-interface.md` §2 「实现方必须容忍 nil」)。
func TestPJ0_CompileToleratesNilFeedback(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PJ0: Compile must tolerate nil feedback (P3Compiler 接口契约), panicked: %v", r)
		}
	}()
	c := New()
	proto := &bytecode.Proto{Code: []bytecode.Instruction{}}
	_, _ = c.Compile(proto, nil)
}
