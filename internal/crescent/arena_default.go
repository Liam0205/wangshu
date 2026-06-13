//go:build !wangshu_p3

package crescent

import "github.com/Liam0205/wangshu/internal/arena"

// newStateArena 建 State 主 arena。
//
// 默认 / wangshu_profile build:走 arena.DefaultBacking(Go 堆 make),不引入
// wazero。P3 收养路径见 arena_p3.go(wangshu_p3 build)。
//
// 返回 (arena, cleanup):cleanup 在 State 销毁时调(默认 build 为 nil)。
func newStateArena() (*arena.Arena, func()) {
	return arena.New(arena.Options{}), nil
}
