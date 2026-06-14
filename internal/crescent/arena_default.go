//go:build !wangshu_p3

package crescent

import "github.com/Liam0205/wangshu/internal/arena"

// newStateArena 建 State 主 arena。
//
// 默认 / wangshu_profile build:走 arena.DefaultBacking(Go 堆 make),不引入
// wazero。P3 收养路径见 arena_p3.go(wangshu_p3 build)。
//
// 返回 (arena, cleanup, p3env):cleanup 默认 build 为 nil;p3env 恒 nil
// (无 wazero runtime/holder;wireP3 据此 no-op)。
func newStateArena() (*arena.Arena, func(), any) {
	return arena.New(arena.Options{}), nil, nil
}

// wireP3 默认 build no-op(无 gibbous 后端;bridge.p3 保持 nil →
// SupportsAllOpcodes 永 false → 无 Proto 升层,与 P1 行为一致)。
func (st *State) wireP3() {}
