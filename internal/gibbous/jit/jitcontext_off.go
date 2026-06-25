//go:build !wangshu_p4

package jit

// JITContext 默认 build 占位(P4 完全 dead-code,wireP4 no-op 时不应触达)。
//
// 与 compiler_off.go 同款形态——保持类型存在使 `internal/crescent` 等
// 持有 *JITContext 字段时(留 PJ2 实装)默认 build 仍可编译。
type JITContext struct{}

// NewJITContext 默认 build 返 nil(wireP4 据此跳过装载)。
func NewJITContext() *JITContext { return nil }

// SetPreemptFlag 默认 build no-op。
func (c *JITContext) SetPreemptFlag() {}

// ClearPreemptFlag 默认 build no-op。
func (c *JITContext) ClearPreemptFlag() {}

// PreemptFlagPending 默认 build 返 false。
func (c *JITContext) PreemptFlagPending() bool { return false }
