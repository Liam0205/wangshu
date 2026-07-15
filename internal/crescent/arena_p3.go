//go:build wangshu_p3 && !wangshu_p4

package crescent

import (
	"context"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
)

// p3Env holds the wazero Runtime + holder that share the same origin as the
// State arena under the wangshu_p3 build, so that wireP3 can build a gibbous
// Compiler sharing the same runtime / linear memory.
type p3Env struct {
	ctx    context.Context
	rt     wazero.Runtime
	holder *memadapter.MemoryHolder
}

// newStateArena builds the main State arena —— under the wangshu_p3 build it
// adopts the wazero linear memory as its backing
// (docs/design/p3-wasm-tier/03-memory-model.md §1).
//
// Flow: ① build a wazero Runtime (compiler mode) ② memadapter builds a
// MemoryHolder allocating linear memory ③ arena.Options injects
// holder.Backing() + InPlaceBacking=true (adoption semantics: grow expands in
// place without copying). Returns p3Env so that wireP3 can take the runtime to
// build the Compiler.
func newStateArena(arenaOpts arena.Options) (*arena.Arena, func(), any) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())

	initial := arenaOpts.InitialBytes
	if initial == 0 {
		initial = defaultInitialArenaBytes
	}
	maxB := arenaOpts.MaxBytes
	if maxB == 0 {
		maxB = defaultMaxArenaBytes
	}
	holder, err := memadapter.New(ctx, rt, initial, maxB)
	if err != nil {
		// A holder build failure is a P3 environment problem (wazero
		// unavailable / illegal memory limit) —— fail-fast, do not silently
		// fall back to the Go heap (that would make the P3 build silently run
		// as the P1 form).
		_ = rt.Close(ctx)
		panic("crescent: P3 build failed to adopt wazero memory: " + err.Error())
	}

	a := arena.New(arena.Options{
		InitialBytes:   initial,
		MaxBytes:       maxB,
		NewBacking:     holder.Backing(),
		InPlaceBacking: true, // adoption semantics: memory.grow expands in place, grow64 does not copy
	})

	cleanup := func() {
		_ = holder.Close()
		_ = rt.Close(ctx)
	}
	return a, cleanup, &p3Env{ctx: ctx, rt: rt, holder: holder}
}

// wireP3 builds the gibbous Compiler and injects the bridge (VS0-d / PW2-d).
//
// The Compiler shares the same wazero Runtime (p3Env.rt) with the State arena
// —— the gibbous module shares, via import "env" "memory", the block of linear
// memory held by holder (= where the State value stack lives), so the base
// byte offset passed by the trampoline addresses the real registers inside the
// gibbous wasm. The host abstraction injects *State (implementing HostState:
// GetUpval/SetUpval/DoReturn/...).
func (st *State) wireP3() {
	env, ok := st.p3env.(*p3Env)
	if !ok || env == nil {
		return
	}
	c := wasm.NewCompiler(env.ctx, env.rt, st)
	st.bridge.SetP3Compiler(c)
}

// Default values for arena capacity (aligned with the arena package defaults;
// the p3 build passes them explicitly to keep the holder and arena capacity
// consistent).
const (
	defaultInitialArenaBytes = 64 * 1024 // 64 KiB (same as arena.New zero value)
	defaultMaxArenaBytes     = 1 << 31   // 2 GiB (within arena.MaxBytes scale, within wasm32 4GiB)
)

// wireP4 is a no-op under the wangshu_p3 build (P3 and P4 are mutually
// exclusive build tags; this build does not enable P4).
func (st *State) wireP4() {}
