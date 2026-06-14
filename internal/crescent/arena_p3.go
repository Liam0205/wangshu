//go:build wangshu_p3

package crescent

import (
	"context"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
)

// p3Env 持 wangshu_p3 build 下与 State arena 同源的 wazero Runtime + holder,
// 供 wireP3 构造 gibbous Compiler 共享同一 runtime/linear memory。
type p3Env struct {
	ctx    context.Context
	rt     wazero.Runtime
	holder *memadapter.MemoryHolder
}

// newStateArena 建 State 主 arena —— wangshu_p3 build 下收养 wazero linear
// memory 作 backing(docs/design/p3-wasm-tier/03-memory-model.md §1)。
//
// 流程:① 建 wazero Runtime(编译模式)② memadapter 建 MemoryHolder 分配
// linear memory ③ arena.Options 注入 holder.Backing() + InPlaceBacking=true
// (收养语义:grow 原地扩不 copy)。返回 p3Env 供 wireP3 取 runtime 构造 Compiler。
func newStateArena() (*arena.Arena, func(), any) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())

	holder, err := memadapter.New(ctx, rt, defaultInitialArenaBytes, defaultMaxArenaBytes)
	if err != nil {
		// holder 建失败是 P3 环境问题(wazero 不可用 / 内存上限非法)——
		// fail-fast,不静默退回 Go 堆(那会让 P3 build 静默跑成 P1 形态)。
		_ = rt.Close(ctx)
		panic("crescent: P3 build failed to adopt wazero memory: " + err.Error())
	}

	a := arena.New(arena.Options{
		InitialBytes:   defaultInitialArenaBytes,
		MaxBytes:       defaultMaxArenaBytes,
		NewBacking:     holder.Backing(),
		InPlaceBacking: true, // 收养语义:memory.grow 原地扩,grow64 不 copy
	})

	cleanup := func() {
		_ = holder.Close()
		_ = rt.Close(ctx)
	}
	return a, cleanup, &p3Env{ctx: ctx, rt: rt, holder: holder}
}

// wireP3 构造 gibbous Compiler 并注入 bridge(VS0-d / PW2-d)。
//
// Compiler 与 State arena 共享同一 wazero Runtime(p3Env.rt)——gibbous module
// 经 import "env" "memory" 共享 holder 那块 linear memory(= State 值栈所在),
// 故 trampoline 传的 base 字节偏移在 gibbous wasm 里寻址到的就是真实寄存器。
// host 抽象注入 *State(实现 HostState:GetUpval/SetUpval/DoReturn/...)。
func (st *State) wireP3() {
	env, ok := st.p3env.(*p3Env)
	if !ok || env == nil {
		return
	}
	c := wasm.NewCompiler(env.ctx, env.rt, st)
	st.bridge.SetP3Compiler(c)
}

// arena 容量默认值(与 arena 包默认对齐;p3 build 显式传以保证 holder 与
// arena 容量口径一致)。
const (
	defaultInitialArenaBytes = 64 * 1024 // 64 KiB(arena.New 零值同值)
	defaultMaxArenaBytes     = 1 << 31   // 2 GiB(arena.MaxBytes 量级内,wasm32 4GiB 内)
)
