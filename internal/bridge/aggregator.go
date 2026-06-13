// IC feedback aggregator (`docs/design/p2-bridge/02-ic-feedback.md` §6).
//
// 输入:Proto 的 ICSlot 数组(P1 已写,旁路供料);
// 输出:按 pc 索引的 PointFeedback 聚合产物(挂 ProfileData.Feedback)。
//
// **P2 写不消费**(02 §7):聚合器是纯读 ICSlot + 纯写 TypeFeedback,自己
// 不读任何 feedback 字段。
package bridge

import (
	"fmt"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// 聚合器配置(02 §5 默认值)。
const (
	// StableArithThreshold 算术 IC 稳定阈值:numHits/total ≥ 此值才判
	// FBArithStableNumber(02 §5.2,默认 0.99 偏保守一档,V8/SpiderMonkey
	// 实证区间 [0.97, 0.99])。
	StableArithThreshold float32 = 0.99

	// MinObservations 算术 IC 样本量下限:numHits+metaHits < 此值视为统计
	// 无意义,直接判 FBUnstable(02 §5.3,100 次给 ±10% 置信区间)。
	MinObservations uint64 = 100

	// MegamorphicRefillThreshold 表 IC 重填次数阈值(P2 后续优化轮 #4,
	// 02 §6.2 方案 (B) 简化版):同一 IC slot 经历过 N 次以上「miss-after-fill
	// 重填」(目标表/形不同 ⇒ 必须丢旧 slot 重建)即标 megamorphic。
	// 默认 3 次:首次填(Kind=0→1/2)不算,从第 1 次重填起累计;3 次重填
	// 大致对应「该点访问过 4 个不同表/形」,statistic 上已是多态。
	MegamorphicRefillThreshold uint8 = 3
)

// Aggregator 是 IC 反馈聚合器(02 §6.4)。无状态,纯函数封装;每次新建一个
// (跨 Proto 不共享内部状态——本设计当前无内部状态,封装是为未来扩展)。
type Aggregator struct {
	stableArithThreshold float32
	minObservations      uint64
	globalGen            atomic.Uint32 // generation 计数(02 §4.1),分配单调递增
}

// NewAggregator 用默认阈值构造一个聚合器。
func NewAggregator() *Aggregator {
	return &Aggregator{
		stableArithThreshold: StableArithThreshold,
		minObservations:      MinObservations,
	}
}

// Aggregate 把 Proto 的全 IC 观测聚合成一份 TypeFeedback(02 §6.4)。
//
// 性质:
//   - O(N) 单遍(N = len(Proto.Code));
//   - 无副作用——不写 ICSlot、不写 ProfileData(installFeedback 单独 CAS);
//   - 可重入——同一 Proto 多线程并发调用安全(都是只读 ICSlot,
//     race-tolerant 读;02 §5.4)。
func (a *Aggregator) Aggregate(proto *bytecode.Proto) *TypeFeedback {
	fb := &TypeFeedback{
		Points:     make([]PointFeedback, len(proto.Code)),
		Generation: a.globalGen.Add(1),
	}
	for pc, ins := range proto.Code {
		op := bytecode.Op(ins)
		slot := &proto.IC[pc]
		switch op {
		case bytecode.ADD, bytecode.SUB, bytecode.MUL,
			bytecode.DIV, bytecode.MOD, bytecode.POW,
			bytecode.UNM, bytecode.LT, bytecode.LE:
			fb.Points[pc] = a.extractArithFeedback(int32(pc), slot)
		case bytecode.GETTABLE:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opGetTable)
		case bytecode.SETTABLE:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSetTable)
		case bytecode.GETGLOBAL:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opGetGlobal)
		case bytecode.SETGLOBAL:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSetGlobal)
		case bytecode.SELF:
			fb.Points[pc] = a.extractTableFeedback(int32(pc), slot, opSelf)
		default:
			// 非 IC 指令:fb.Points[pc] 保持零值(Kind=FBUnstable, Confidence=0)
			// — P3/P4 应跳过此 pc。
		}
	}
	return fb
}

// opTableKind 标识表 IC 的 opcode 子类(02 §6.3)。
type opTableKind uint8

const (
	opGetTable opTableKind = iota
	opSetTable
	opGetGlobal
	opSetGlobal
	opSelf
)

// extractArithFeedback 算术 IC 聚合(02 §3.4)。
//
// 输入 ICSlot:
//   - Shape    = numHits
//   - Index    = metaHits
//   - Kind     = 0 未观测 / 1 已观测过
//
// 输出 PointFeedback:
//   - Kind = FBArithStableNumber(ratio ≥ 0.99 + total ≥ 100)
//     / FBUnstable(其它)
//   - Confidence = ratio(诊断带上,即便 Unstable)
func (a *Aggregator) extractArithFeedback(pc int32, slot *bytecode.ICSlot) PointFeedback {
	if slot.Kind == 0 {
		return PointFeedback{} // 跳过:未观测
	}
	// 算术 IC 上 Kind 必为 1(P2 聚合器只读 P1 写入,任何其它值都是 P1
	// 写入契约违反)。注意 race-tolerant 读:多 State 并发场景下 P1 仍
	// 在写,读到「Kind=1 但 Shape/Index 0」是合法瞬时态(02 §5.4)。
	if slot.Kind != 1 {
		panic(fmt.Sprintf("bridge: arith IC at pc=%d has kind=%d, expected 0 or 1",
			pc, slot.Kind))
	}
	numHits := atomic.LoadUint32(&slot.Shape) // 02 §5.4 race-tolerant 读
	metaHits := atomic.LoadUint32(&slot.Index)
	total := uint64(numHits) + uint64(metaHits)
	if total < a.minObservations {
		return PointFeedback{
			PC:           pc,
			Kind:         FBUnstable,
			Confidence:   0.0, // 显式 0,标识「样本不足」
			Observations: uint32(total),
		}
	}
	ratio := float32(numHits) / float32(total)
	pf := PointFeedback{
		PC:           pc,
		Confidence:   ratio,
		Observations: uint32(total),
	}
	if ratio >= a.stableArithThreshold {
		pf.Kind = FBArithStableNumber
	} else {
		pf.Kind = FBUnstable
	}
	return pf
}

// extractTableFeedback 表 / 全局 / SELF IC 聚合(02 §6.3)。
func (a *Aggregator) extractTableFeedback(pc int32, slot *bytecode.ICSlot, opType opTableKind) PointFeedback {
	if slot.Kind == 0 {
		return PointFeedback{} // 跳过:未观测
	}
	pf := PointFeedback{
		PC:           pc,
		Confidence:   1.0, // 02 §5.1:表 IC mono 即 1.0
		StableShape:  slot.Shape,
		StableIndex:  slot.Index,
		Observations: 1, // 02 §5.1:表 IC P1 不计数,填占位 1
	}
	// P2+ #4 megamorphic 主动识别:Refill 重填次数超阈值 ⇒ 该点多态,
	// 主动翻译为 FBTableMega(覆盖原本 mono kind 判定)。这弥补 P1 当前
	// 不主动写 ICKindMegamorphic 的差距(02 §6.2 方案 (A) → (B) 升级)。
	if slot.Refill >= MegamorphicRefillThreshold {
		pf.Kind = FBTableMega
		pf.Confidence = 0.0
		pf.StableShape = 0
		pf.StableIndex = 0
		return pf
	}
	switch slot.Kind {
	case bytecode.ICKindArrayHit, bytecode.ICKindNodeHit, bytecode.ICKindMonoMeta:
		switch opType {
		case opGetTable, opSetTable:
			pf.Kind = FBTableMono
		case opGetGlobal, opSetGlobal:
			pf.Kind = FBGlobalStable
		case opSelf:
			pf.Kind = FBSelfMono
		}
	case bytecode.ICKindMegamorphic:
		pf.Kind = FBTableMega
		pf.Confidence = 0.0
		pf.StableShape = 0
		pf.StableIndex = 0
	default:
		panic(fmt.Sprintf("bridge: table IC at pc=%d has unexpected kind=%d",
			pc, slot.Kind))
	}
	return pf
}

// installFeedback Bridge 端把聚合产物挂上 ProfileData(单 State 内只一次,
// 02 §4.5 + §5.5 不重聚合策略)。当前实装是非 atomic 写——profileTable 是
// State 私有,无并发竞争(01 §6.3 (B) 方案)。
//
// 多 State 共享 Proto 的并发 feedback 写入是另一维度的事(Proto 旁聚合表,
// 02 §5.5 CAS 安装)——P2 PB0/PB1/PB2 的 (B) 方案下不存在,(C) 启用时再补
// CAS。当前 ProfileData 的 Feedback 字段是 State 私有,普通赋值即可。
func (b *Bridge) installFeedback(proto *bytecode.Proto, fb *TypeFeedback) {
	pd := b.profileOf(proto)
	if pd.Feedback == nil {
		pd.Feedback = fb
	}
	// 已有 feedback 不覆盖(初版只聚合一次)
}
