//go:build !wangshu_p4

package jit

// JITContext default-build placeholder (P4 is fully dead code here; must not be
// reached when wireP4 is a no-op).
//
// Same shape as compiler_off.go: keeping the type present lets the default build
// still compile when packages such as `internal/crescent` hold a *JITContext
// field (reserved for the PJ2 implementation).
type JITContext struct{}

// NewJITContext returns nil in the default build (wireP4 uses this to skip loading).
func NewJITContext() *JITContext { return nil }

// SetPreemptFlag is a no-op in the default build.
func (c *JITContext) SetPreemptFlag() {}

// ClearPreemptFlag is a no-op in the default build.
func (c *JITContext) ClearPreemptFlag() {}

// PreemptFlagPending returns false in the default build.
func (c *JITContext) PreemptFlagPending() bool { return false }
