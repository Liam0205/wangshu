//go:build !wangshu_p3 && !wangshu_p4

package crescent

import "github.com/Liam0205/wangshu/internal/arena"

// newStateArena 建 State 主 arena。
//
// 默认 / wangshu_profile build:走 arena.DefaultBacking(Go 堆 make),不引入
// wazero。P3 收养路径见 arena_p3.go(wangshu_p3 build);P4 默认 build 不
// 收养(P4 走纯 Go 堆 backing,见 arena_p4.go)。
//
// arenaOpts:wangshu.Options.{InitialArenaBytes,MaxArenaBytes} 透传(零值由
// arena.New 内部回落默认 64 KiB / 2 GiB)。
//
// 返回 (arena, cleanup, p3env):cleanup 默认 build 为 nil;p3env 恒 nil
// (无 wazero runtime/holder;wireP3 / wireP4 据此 no-op)。
func newStateArena(arenaOpts arena.Options) (*arena.Arena, func(), any) {
	return arena.New(arenaOpts), nil, nil
}

// wireP3 默认 build no-op(无 gibbous 后端;bridge.p3 保持 nil →
// SupportsAllOpcodes 永 false → 无 Proto 升层,与 P1 行为一致)。
func (st *State) wireP3() {}

// wireP4 默认 build no-op(P4 后端未启用;bridge.p3 保持 nil 或由 wireP3
// 接管)。
//
// **P3+P4 互斥 build tag**(用户裁决,06-backends.md §1 + 主助理决策):
// `wangshu_p3` 与 `wangshu_p4` 不允许同时启用。两个 wireP3/wireP4 方法独立,
// 但实际只有一个 build tag 路径被启用,bridge.p3 单字段不冲突。PJ11 P3 退役
// 后 wireP3 整组文件可删,wireP4 接管全部。
func (st *State) wireP4() {}
