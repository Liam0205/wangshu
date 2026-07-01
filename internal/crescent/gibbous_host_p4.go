//go:build wangshu_p4

package crescent

import (
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// RefreshJitCtxAddrs implements the batched P4HostState.RefreshJitCtxAddrs
// entry: compute the arena base pointer once, then derive all five
// arena-relative addresses (arenaBase / valueStackBase / ciDepthAddr /
// ciSegBaseAddr / topAddr) by simple offset arithmetic. Called on every
// JIT Run entry and replaces five separate getter calls that each
// recomputed arena.Words() + unsafe.Pointer.
//
// Arena grow protocol still holds: caller must call this on every Run
// entry; the values become stale once execution leaves the JIT world.
// Grow only happens on the slow path, so the JIT segment reloads via
// the jitContext on return.
func (st *State) RefreshJitCtxAddrs(ctx *jit.JITContext, base int32) {
	words := st.arena.Words()
	if len(words) == 0 {
		ctx.SetAllAddrs(0, 0, 0, 0, 0)
		return
	}
	arenaBase := uintptr(unsafe.Pointer(&words[0]))
	ctx.SetAllAddrs(
		arenaBase,
		arenaBase+uintptr(base),
		arenaBase+uintptr(st.ciDepthRef),
		arenaBase+uintptr(st.ciSegBaseRef),
		arenaBase+uintptr(st.topRef),
	)
}
