// Heat counters and ProfileData (`docs/design/p2-bridge/01-profiling.md`).
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// 阈值常量(01 §5.1 建议值——不影响正确性,只影响何时编译,实测后定标)。
const (
	// HotBackEdgeThreshold:单个回边 pc 的回跳计数累计达此值即候选升层。
	// 1000 是「保守且足够快越」的折中(LuaJIT hotloop=56 / V8 OSR=256 都更低,
	// 但它们有 OSR;望舒走 try-compile 可保守一档)。
	HotBackEdgeThreshold uint32 = 1000

	// HotEntryThreshold:函数入口累计调用数阈值。200 次足以判热——典型
	// 形态:外层循环 1000 次每次调一个 helper,200 次时已确认 helper 是热点。
	HotEntryThreshold uint32 = 200

	// MinPromotableCodeLen:Proto 升层候选的最小 opcode 数下限。Proto.Code 长度
	// 严格小于此值时 OnEnter/OnBackEdge **仍累积 EntryCount/BackEdge counter**
	// (profile 诊断完整),但在阈值越过后**跳过 considerPromotion 调用**——这类
	// 「短工作量」proto 升层后 wasm dispatch + host↔wasm boundary 反噬大于解释器
	// dispatch 收益(实测 pineapple 形态 4-opcode 算术 f 升层后比解释器慢 19%,
	// 见 issue #21 profile 实证:cpu profile top 200 中 wasm 反噬主导,
	// 钩税路径可忽略)。守卫位置选「阈值后但 considerPromotion 前」而非
	// 「counter 累积前」,保留 profile_test 的诊断断言(EntryCount/MaxBackEdge
	// 准确反映短 proto 调用次数)。
	//
	// **10 经验初值**:覆盖纯算术 4-6 opcodes(GETGLOBAL/MUL/ADD/RETURN 类)+ 一点
	// 余量,不影响真正含循环 / 多步算术 / 表查找的 10+ opcodes 函数升层。后续可经
	// micro-benchmark(P3_Kernels 标定形态)精确定值。**不影响正确性**——升层
	// 决策是 perf 优化,short proto 留解释器路径与 P1-only build 行为等价。
	//
	// **与 F1-F7 闸门的关系**:F1-F6 是「不可编译形状」(vararg/coroutine/oversize
	// 等结构性排除),F7 是「后端能力查询」;MinPromotableCodeLen 是「可编译但不
	// 值得编译」——独立于可编译性判定,不写 ReasonsBitmap,只是 sampler 入口的
	// fast-path 守卫。
	MinPromotableCodeLen = 10
)

// ProfileData 是一个 Proto 在某 State 上的画像数据(01 §2.2)。
//
// 设计要点:
//   - 物理存储位置是 State 私有 profileTable(01 §6.3 (B) 方案),而非 Proto
//     旁字段——避免多 State 并发写计数器的 race,与 wangshu Program 跨 State
//     只读共享的并发约定一致(11 §1.4 / §8)。
//   - 不进 arena、不进 GC 根集合(01 §2.4):住 Go 堆,与 Proto 同生命期。
//   - 计数累积语义:跨调用累积函数级聚合(non-CallInfo-frame-level)。
//
// 字段所有权(单一事实源分工):
//   - EntryCount / BackEdge: 01-profiling 主管(回边 / 入口采样)
//   - Feedback              : 02-ic-feedback 主管(P2 写,P3/P4 读;P2 不消费)
//   - Compilable / Reasons  : 03-compilability-analysis 主管(Compile 时一次写,后续只读)
//   - TierState / CompileTried: 04-try-compile-fallback 主管(状态机字段)
type ProfileData struct {
	// —— 计数器(01-profiling §2.2)——
	EntryCount uint32   // 函数入口计数:每次 enterLuaFrame 自增
	BackEdge   []uint32 // 按回边 pc 索引的回跳计数(稠密数组,延迟分配)

	// —— IC 反馈(02-ic-feedback §4.5)——
	// 一次性聚合(P2 初版只在首次升层时聚合一次,02 §4.5);P3/P4 只读消费。
	Feedback *TypeFeedback

	// —— 可编译性(03-compilability-analysis §5.3)——
	// Compile 时一次写,运行期只读;并发读由 Go memory model 自动保证可见性
	// (write-once before any reader,03 §5.4)。
	Compilable Compilability
	Reasons    ReasonsBitmap // F1-F7 拒因位掩码(03 §5.3),用于诊断日志

	// —— 状态机(04-try-compile-fallback §3)——
	TierState    TierState // TierInterp / TierGibbous / TierStuck
	CompileTried bool      // 是否已尝试编译(防 TierStuck 反复重试,04 §3.2)

	// recheckedAtEntry dedups recheckCompilabilityRuntime while the
	// forceAll retry window holds a declined proto on TierInterp
	// (issue #40). It stores EntryCount+1 as of the last recheck
	// (0 = never ran). Promotion only takes effect on a later entry
	// (there is no OSR), so re-running the full backend analysis on
	// every back edge of the same entry buys nothing — on a declined
	// 2M-back-edge kernel it measured 22% CPU and 1.5 GB/op.
	// OnBackEdge clears it once when a back edge crosses
	// HotBackEdgeThreshold, granting one extra warm-IC recheck per pc.
	recheckedAtEntry uint32
}

// MaxBackEdge 返回该 ProfileData 中最大的单回边累计计数。
//
// 单回边越阈值近似「函数热」(01 §5.2):不必每次求和所有回边,只要某一个
// 回边累计够热,就认为函数值得编译。本函数主要用于诊断日志显示「累计 N
// 次回边」(04 §6.1 升层日志格式)。
func (pd *ProfileData) MaxBackEdge() uint32 {
	var m uint32
	for _, c := range pd.BackEdge {
		if c > m {
			m = c
		}
	}
	return m
}

// resetCountersForReuse 仅清回边/入口计数,保留状态机字段(TierState /
// Compilable / Reasons)。**当前未使用**——预留给 sync.Pool 短生命期 State
// 形态下的 (C) 双表混合方案(01 §6.4)。本期接口预设占位,真聚合实装等
// 实测发现 (B) 方案累积速度均分严重影响热阈值生效再启用。
//
//nolint:unused // 接口预设占位,(C) 方案启用时使用。
func (pd *ProfileData) resetCountersForReuse() {
	pd.EntryCount = 0
	for i := range pd.BackEdge {
		pd.BackEdge[i] = 0
	}
	pd.recheckedAtEntry = 0
}

// allocBackEdge 在首次回边命中时按 Code 长度延迟分配 backEdge 数组(01 §2.3)。
//
// 避免「冷 Proto 永远没有回边、却为它预留了一个数组」的浪费。返回 ProfileData
// 的方法链友好形态。
func (pd *ProfileData) allocBackEdge(proto *bytecode.Proto) {
	if pd.BackEdge == nil {
		pd.BackEdge = make([]uint32, len(proto.Code))
	}
}
