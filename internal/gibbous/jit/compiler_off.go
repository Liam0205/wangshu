//go:build !wangshu_p4

// 默认 / wangshu_p3 / wangshu_profile build:P4 编译器空 stub(P4 完全 dead-code,
// 不引入 unsafe / syscall / asm 等 P4 专用依赖)。
//
// `internal/crescent/arena_default.go` wireP4 据此 no-op,bridge.p3 由 P3
// (若 wangshu_p3 build)注入或保持 nil(P1-only build)——与 P4 不互相干扰。
package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compiler 默认 build 空 stub(不实装 bridge.P3Compiler)。
//
// 接口实装由 wangshu_p4 build 提供(`compiler.go` 同名 struct);默认 build
// 下本 type 仅为命名占位,**不能注入 bridge.SetP3Compiler**——wireP4 默认
// build no-op,bridge.p3 由 P3 接管或保持 nil。
type Compiler struct{}

// New 默认 build 占位——返回 nil(wireP4 据此跳过注入)。
func New() *Compiler { return nil }

// SupportsAllOpcodes 默认 build 不应被调到(wireP4 不注入 bridge);
// 防御性返 false——若误调返 false 与「P4 未启用」语义一致。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	_ = proto
	return false
}

// ErrCompileOff 默认 build 占位错误——P4 未启用。
var ErrCompileOff = errors.New("internal/gibbous/jit: P4 not enabled (build without wangshu_p4)")

// Compile 默认 build 不应被调到(wireP4 not active);防御性返错让调用方
// fallback 到 TierStuck(承 P3Compiler 接口契约 error != nil ⇒ TierStuck)。
//
// 与 wangshu_p4 build 下的 ErrCompileUnsupportedShape 对位返错——确保
// 「不应被调到却被调到」时违约场景显式可见。
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	_ = proto
	_ = feedback
	return nil, ErrCompileOff
}
