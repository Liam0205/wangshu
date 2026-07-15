//go:build wangshu_p3

package wasm

import (
	"context"

	"github.com/tetratelabs/wazero/api"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compile-time assertion: p3Code implements bridge.GibbousCode (Proto/Run/PendingErr).
var _ bridge.GibbousCode = (*p3Code)(nil)

// p3Code is P3's bridge.GibbousCode implementation (02-translation §5.3).
type p3Code struct {
	compiled api.Closer   // wazero CompiledModule (Close to release)
	module   api.Module   // instantiated module (Close to release)
	fn       api.Function // entry "run" (base i32) -> status i32
	proto    *bytecode.Proto
	ctx      context.Context

	// slot is this module's run slot number in the shared env.table (PW10 R3 Arch-2);
	// hasSlot false means the table-full sentinel (not registered) → gibbous→it falls
	// back to synchronous Run.
	slot    uint32
	hasSlot bool

	// pendingErr records a wazero internal error (rare; when run returns a non-nil err).
	pendingErr error
}

// Proto implements bridge.GibbousCode.
func (c *p3Code) Proto() *bytecode.Proto { return c.proto }

// Run is the crescent→gibbous entry (04-trampoline §2): passes base i32, wazero
// executes, returns status (0=OK / 1=ERR). One cross-layer hop (PW0 spike measured 36.7ns).
//
// stack reuse (CallWithStack zero-alloc path, PW0 spike measured 14.8ns): the caller
// passes a []uint64 of len≥1, stack[0]=base as input, and after return stack[0]=status.
func (c *p3Code) Run(stack []uint64, base uint32) int32 {
	stack[0] = uint64(base)
	if err := c.fn.CallWithStack(c.ctx, stack); err != nil {
		c.pendingErr = err
		return 1
	}
	return int32(stack[0])
}

// PendingErr returns the most recent wazero internal error from Run (read by the trampoline).
func (c *p3Code) PendingErr() error { return c.pendingErr }

// Slot implements bridge.GibbousCode (PW10 R3): returns run's slot number in the shared
// env.table + whether it is registered. hasSlot=false (table-full sentinel) ⟹ gibbous→it
// falls back to synchronous Run.
func (c *p3Code) Slot() (uint32, bool) { return c.slot, c.hasSlot }

// Dispose releases wazero resources (idempotent).
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
