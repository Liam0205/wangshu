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
// **PJ0 阶段:Compiler 是空 struct,SupportsAllOpcodes 全 false** ⇒ 所有
// Proto 经 F7 闸门判 NotCompilable,considerPromotion 进 TierStuck——P4
// build 行为与 P1-only 等价(00 §4 PJ0 验收口径:「bridge 注入 P4Compiler
// 后 SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent」)。这是 PJ0
// 关键防线:本 build 全测试套(V1-V13/V17/V18)与 P1-only build byte-equal。
//
// PJ1+ 渐进填实(amd64 trampoline + 直线模板 + 投机 IC + OSR exit)。
func (st *State) wireP4() {
	c := jit.New()
	if c == nil {
		// 防御性兜底:Compiler 构造失败(P4 环境问题或 wangshu_p4 build tag 失配),
		// 不注入 bridge——bridge.p3 保持 nil,行为同 P1-only(F7 永久不通过)。
		return
	}
	st.bridge.SetP3Compiler(c)
}
