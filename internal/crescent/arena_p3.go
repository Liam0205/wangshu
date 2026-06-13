//go:build wangshu_p3

package crescent

import (
	"context"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/gibbous/wasm/memadapter"
)

// newStateArena 建 State 主 arena —— wangshu_p3 build 下收养 wazero linear
// memory 作 backing(docs/design/p3-wasm-tier/03-memory-model.md §1)。
//
// 流程:① 建 wazero Runtime(编译模式)② memadapter 建 MemoryHolder 分配
// linear memory ③ arena.Options 注入 holder.Backing() + InPlaceBacking=true
// (收养语义:grow 原地扩不 copy)。
//
// 返回 cleanup 在 State 销毁时关闭 holder + runtime。
//
// **注意**:wazero Runtime 这里建但 PW1 阶段除了 holder module 外不装载任何
// gibbous module(SupportsAllOpcodes 全 false,无 Proto 升层)。Runtime 还要
// 传给 Compiler(PW1-c 接线;PW2+ 翻译产物经它加载)——当前 State 暂存
// runtime 供后续取(见 State.p3Runtime 字段,p3 build 专属)。
func newStateArena() (*arena.Arena, func()) {
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
	return a, cleanup
}

// arena 容量默认值(与 arena 包默认对齐;p3 build 显式传以保证 holder 与
// arena 容量口径一致)。
const (
	defaultInitialArenaBytes = 64 * 1024 // 64 KiB(arena.New 零值同值)
	defaultMaxArenaBytes     = 1 << 31   // 2 GiB(arena.MaxBytes 量级内,wasm32 4GiB 内)
)
