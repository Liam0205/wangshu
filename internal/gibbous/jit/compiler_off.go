//go:build !wangshu_p4

// Default / wangshu_p3 / wangshu_profile build: an empty stub for the P4
// compiler (P4 is entirely dead-code here, pulling in none of the P4-specific
// dependencies such as unsafe / syscall / asm).
//
// `internal/crescent/arena_default.go` wireP4 is a no-op accordingly; bridge.p3
// is either injected by P3 (in a wangshu_p3 build) or left nil (P1-only build)
// — neither interferes with P4.
package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compiler is an empty stub in the default build (does not implement
// bridge.P3Compiler).
//
// The interface implementation is provided by the wangshu_p4 build (the
// same-named struct in `compiler.go`); in the default build this type is only a
// named placeholder and **must not call bridge.SetP3Compiler** — wireP4 is a
// no-op in the default build, and bridge.p3 is taken over by P3 or left nil.
type Compiler struct{}

// New is a placeholder in the default build — returns nil (wireP4 skips
// injection accordingly).
func New() *Compiler { return nil }

// SupportsAllOpcodes should never be reached in the default build (wireP4 does
// not inject bridge); defensively returns false — a spurious call returning
// false matches the "P4 not enabled" semantics.
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	_ = proto
	return false
}

// ErrCompileOff is a placeholder error in the default build — P4 not enabled.
var ErrCompileOff = errors.New("internal/gibbous/jit: P4 not enabled (build without wangshu_p4)")

// Compile should never be reached in the default build (wireP4 not active);
// defensively returns an error so the caller falls back to TierStuck (honoring
// the P3Compiler interface contract: error != nil ⇒ TierStuck).
//
// This mirrors ErrCompileUnsupportedShape returned under the wangshu_p4 build —
// ensuring the "should never be reached yet was reached" contract-violation
// case is explicitly visible.
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	_ = proto
	_ = feedback
	return nil, ErrCompileOff
}
