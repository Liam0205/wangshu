// TypeFeedback — IC 反馈聚合产物(`docs/design/p2-bridge/02-ic-feedback.md` §4)。
//
// **设计核心**:P2 写 feedback 不消费(02 §7);P3/P4 读消费,P3 可选 / P4 核心。
package bridge

// FeedbackKind 描述某 pc 上 IC 观测的稳定形态(02 §4.3)。
//
// **不对称消费**(02 §1.4 + 05 §1):
//   - P3 (try-compile):看 kind 决定「内联哪条快路径、固化哪份 IC 快照」,
//     失败走慢路径仍正确。confidence 字段对 P3 是可忽略的提示。
//   - P4 (投机 JIT):kind + confidence 都用,confidence ≥ 0.99 才发投机
//     模板;guard 失败 ⇒ deopt 回解释器。
//
// **零反馈不影响正确性**:某点 kind=FBUnstable / confidence=0 → P3/P4 都
// 退化为通用翻译(失去加速但仍正确)。
type FeedbackKind uint8

const (
	// FBUnstable —— 「不投机」标识。三种来源合并:
	//   1. 该点未被 IC 观测过(ICSlot.kind=0)
	//   2. 算术点比例不达标(<0.99)或样本量不足(<minObservations)
	//   3. 非 IC 点的默认填充(LOADK / MOVE / RETURN 等)
	FBUnstable FeedbackKind = iota

	// FBArithStableNumber —— 算术点恒为 number 操作数(≥99% numHits/total)。
	// P4 据此发 f64 快路径 + guard;guard 失败 deopt(P4 §IC 投机)。
	FBArithStableNumber

	// FBTableMono —— 表访问单态稳定(GETTABLE/SETTABLE,kind∈{1,2,3})。
	// P4 据此投机直达槽:guard「目标表 gen == stableShape」+ 直接索引。
	FBTableMono

	// FBTableMega —— 表访问 megamorphic(02 §6.3 kind=4 防御性翻译)。
	// 「别投机」明确标识——P4 见此点应当走通用查哈希路径。
	FBTableMega

	// FBGlobalStable —— 全局读恒定。GETGLOBAL/SETGLOBAL 的 globals 单一表,
	// node hit 即稳定;P4/P3 可常量化 stableIndex 槽位。
	FBGlobalStable

	// FBSelfMono —— 方法调用单态(SELF + 紧随 CALL 的方法分发点)。
	// P4 据此内联方法查找:guard metatable gen + 直达方法槽。
	FBSelfMono
)

func (k FeedbackKind) String() string {
	switch k {
	case FBArithStableNumber:
		return "ArithStableNumber"
	case FBTableMono:
		return "TableMono"
	case FBTableMega:
		return "TableMega"
	case FBGlobalStable:
		return "GlobalStable"
	case FBSelfMono:
		return "SelfMono"
	default:
		return "Unstable"
	}
}

// PointFeedback 是单 pc 上的反馈快照(02 §4.2)。
type PointFeedback struct {
	// PC 是该点在 Proto.Code 中的下标(冗余字段,等于 TypeFeedback.Points 索引)。
	// 保留给单点传递时不丢位置信息。
	PC int32

	// Kind 是该点的反馈类型。
	Kind FeedbackKind

	// Confidence ∈ [0.0, 1.0]。语义随 Kind 变化(02 §5.1):
	//   - FBArithStableNumber: numHits/(numHits+metaHits)(真比例)
	//   - FBTableMono / FBGlobalStable / FBSelfMono: 1.0(P1 mono IC 无降级)
	//   - FBTableMega: 0.0(明确「别投机」标识)
	//   - FBUnstable: 0.0 或诊断比例
	Confidence float32

	// StableShape:表/全局点的「稳定 shape」——ICSlot.shape(目标表 gen
	// 代次)的快照值。P4 投机直达槽时 guard「当前表 gen == stableShape」,
	// 失败则 deopt。算术点不填(0)。
	StableShape uint32

	// StableIndex:表/全局点的「稳定槽位下标」——ICSlot.index 快照。
	// P4 命中时直接索引此槽,不查哈希。算术点不填(0)。
	StableIndex uint32

	// Observations:聚合时累计的观测次数(算术点 = numHits+metaHits;
	// 表点 = 命中次数,P1 当前实装不单独计数,默认填占位 1)。
	// 下游可设最低样本量阈值(如 P3 「<100 次的点不发紧凑翻译」)。
	Observations uint32
}

// TypeFeedback 是一个 Proto 的全 IC 观测聚合产物(02 §4.1)。
//
// 按 pc 索引每个程序点的类型稳定性判断。**P2 产出但不自用**(02 §7)。
type TypeFeedback struct {
	// Points 按 pc 索引;长度 = len(Proto.Code);非 IC 点对应槽 Kind=FBUnstable,
	// Confidence=0,P3/P4 应跳过。
	Points []PointFeedback

	// Generation 是 feedback 快照的代次,每次 P2 重新聚合时递增;
	// P3/P4 拿到的快照若 Generation 落后于当前,说明聚合期间 P1 又新写了
	// 一批观测——但这不影响正确性(P3 通用翻译总能兜底,P4 guard 总会
	// 兜住运行期实际偏差)。
	Generation uint32
}
