//go:build wangshu_p4 && !wangshu_p3

package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// newStateArena 建 State 主 arena —— wangshu_p4 build 下走纯 Go 堆 backing
// (`docs/design/p4-method-jit/05-system-pipeline.md` §3.5「P4 build 下 backing
// 切回纯 Go 堆,不经 wazero memory 中介,但偏移寻址协议同 P3」)。
//
// **与 P3 的根本不同**:P3 build 下 arena backing 收养 wazero linear memory
// (`internal/crescent/arena_p3.go`);P4 build 下 arena 在 Go 堆,P4 生成的
// 原生码经 unsafe.Pointer 直接读写 arena `[]byte` 起始指针(05 §3.5)。GCRef
// 偏移寻址在两种 backing 形态下语义同一,这正是 02 §1.6 「per-arch 是分到
// 本架构,非重写」纪律在 backing 维度的兑现。
//
// arenaOpts:wangshu.Options.{InitialArenaBytes,MaxArenaBytes} 透传(零值由
// arena.New 内部回落默认 64 KiB / 2 GiB,与 P3 同款常量保持一致)。
//
// 返回 (arena, cleanup, p3env):cleanup 默认 nil(P4 build arena 是纯 Go 堆,
// GC 自然回收,不需手动清理);p3env 恒 nil(P4 不引入 wazero 依赖,与 P3 字段
// 复用纪律对齐)。
func newStateArena(arenaOpts arena.Options) (*arena.Arena, func(), any) {
	return arena.New(arenaOpts), nil, nil
}

// wireP3 在 wangshu_p4 build 下 no-op(P3+P4 互斥 build tag,本 build 不启用 P3)。
//
// 承用户裁决「互斥 build tag 协议」(主助理决议) + `06-backends.md` §1
// + `07-p3-retirement.md` §5 缺省退役 P3。`wangshu_p3` 与 `wangshu_p4` 不允许
// 同时启用——互斥 tag 是 PJ0 阶段最简方案,与 P3 单字段 `b.p3` 注入对齐;PJ10
// P3 退役后 wireP3 整组文件可删,wireP4 完全接手。
func (st *State) wireP3() {}

// wireP4 构造 P4 Compiler 并注入 bridge(承
// `docs/design/p4-method-jit/00-overview.md` §4 PJ0 行 + P3 同款
// arena_p3.go::wireP3 注入协议范本)。
//
// **PJ7 真接入**(2026-06-25):
//  1. 构造 jit.Compiler;
//  2. 注入 bridge(b.SetP3Compiler);
//  3. **注入 P4HostState**(jit.SetP4HostState(st))——P4 的 p4Code.Run 在
//     mmap 段执行后调 host.DoReturn 完成弹帧 + 移结果(因 P4 简化形态
//     mmap 段不内调 host helper,需 Go 端拆帧——承
//     `docs/design/p4-method-jit/05-system-pipeline.md` §4 trampoline 三出口
//     协议中「Go 端拆帧」选项)。
//
// **PJ7 阶段 SupportsAllOpcodes 已开放**「LOADK A K(0); RETURN A 1」单 BB
// 白名单——bridge 主路径会经 considerPromotion → P4 Compile → installGibbous
// 升层该形态 Proto;crescent.doCall 检测 TierGibbous → enterGibbous → 经
// p4Code.Run 走 P4 路径。
func (st *State) wireP4() {
	c := jit.New()
	if c == nil {
		return
	}
	// PJ7 真接入:注入 *State 作 P4HostState(*State 实装 DoReturn,
	// gibbous_host.go 已有,与 P3 共用)。**per-Compiler 单例**(避免 global
	// hostState 多 State 并发写 race,V18 -race 友好)。
	c.SetHostState(st)
	st.bridge.SetP3Compiler(c)
}
