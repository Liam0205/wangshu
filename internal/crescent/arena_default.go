//go:build !wangshu_p3 && !wangshu_p4

package crescent

import "github.com/Liam0205/wangshu/internal/arena"

// newStateArena builds the State's main arena.
//
// Default / wangshu_profile build: uses arena.DefaultBacking (Go heap make),
// without pulling in wazero. The P3 adoption path is in arena_p3.go
// (wangshu_p3 build); the P4 default build does not adopt (P4 uses a pure Go
// heap backing, see arena_p4.go).
//
// arenaOpts: wangshu.Options.{InitialArenaBytes,MaxArenaBytes} passed through
// (zero values fall back internally in arena.New to the 64 KiB / 2 GiB
// defaults).
//
// Returns (arena, cleanup, p3env): cleanup is nil for the default build; p3env
// is always nil (no wazero runtime/holder; wireP3 / wireP4 no-op accordingly).
func newStateArena(arenaOpts arena.Options) (*arena.Arena, func(), any) {
	return arena.New(arenaOpts), nil, nil
}

// wireP3 is a no-op in the default build (no gibbous backend; bridge.p3 stays
// nil → SupportsAllOpcodes always false → no Proto promotion, matching P1
// behavior).
func (st *State) wireP3() {}

// wireP4 is a no-op in the default build (P4 backend not enabled; bridge.p3
// stays nil or is taken over by wireP3).
//
// **P3+P4 mutually exclusive build tags** (user decision, 06-backends.md §1 +
// main-assistant decision): `wangshu_p3` and `wangshu_p4` may not be enabled at
// the same time. The two wireP3/wireP4 methods are independent, but in practice
// only one build tag path is enabled, and the single bridge.p3 field does not
// conflict. After PJ11 P3 retirement, the whole wireP3 file group can be
// deleted and wireP4 takes over everything.
func (st *State) wireP4() {}
