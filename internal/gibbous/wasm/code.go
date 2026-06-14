//go:build wangshu_p3

package wasm

import (
	"context"

	"github.com/tetratelabs/wazero/api"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// 编译期断言:p3Code 实现 bridge.GibbousCode(Proto/Run/PendingErr)。
var _ bridge.GibbousCode = (*p3Code)(nil)

// p3Code 是 P3 的 bridge.GibbousCode 实现(02-translation §5.3)。
type p3Code struct {
	compiled api.Closer   // wazero CompiledModule(Close 释放)
	module   api.Module   // 实例化的 module(Close 释放)
	fn       api.Function // 入口 "run"(base i32) -> status i32
	proto    *bytecode.Proto
	ctx      context.Context

	// pendingErr 记录 wazero 内部错误(罕见;run 返回非 nil err 时)。
	pendingErr error
}

// Proto 实现 bridge.GibbousCode。
func (c *p3Code) Proto() *bytecode.Proto { return c.proto }

// Run 是 crescent→gibbous 入口(04-trampoline §2):传 base i32,wazero 执行,
// 返回 status(0=OK / 1=ERR)。一次跨层(PW0 spike 实测 36.7ns)。
//
// stack 复用(CallWithStack 零分配路径,PW0 spike 实测 14.8ns):调用方传
// 一个 len≥1 的 []uint64,stack[0]=base 入参,返回后 stack[0]=status。
func (c *p3Code) Run(stack []uint64, base uint32) int32 {
	stack[0] = uint64(base)
	if err := c.fn.CallWithStack(c.ctx, stack); err != nil {
		c.pendingErr = err
		return 1
	}
	return int32(stack[0])
}

// PendingErr 返回最近一次 Run 的 wazero 内部错误(trampoline 读)。
func (c *p3Code) PendingErr() error { return c.pendingErr }

// Dispose 释放 wazero 资源(幂等)。
func (c *p3Code) Dispose() error {
	if c.module != nil {
		_ = c.module.Close(c.ctx)
		c.module = nil
	}
	if c.compiled != nil {
		_ = c.compiled.Close(c.ctx)
		c.compiled = nil
	}
	return nil
}
