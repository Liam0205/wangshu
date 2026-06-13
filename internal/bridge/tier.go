// TierState — single-direction absorbing state machine
// (`docs/design/p2-bridge/04-try-compile-fallback.md` §2).
package bridge

// TierState 升降层状态机(挂 ProfileData.TierState)。
//
// 三态枚举:
//   - TierInterp   起点:所有 Proto 起步;升层未发生
//   - TierGibbous  升层成功的吸收态:Proto 已编译,P3 trampoline 接管
//   - TierStuck    永久解释的吸收态:不可编译 / 编译失败 / 后端不支持
//
// **不变式**(04 §2.4 形式化论证):状态机是单向 + 吸收态,**没有任何**
// TierGibbous→TierInterp / TierStuck→TierInterp 反向边——即「零 deopt」的
// 字面体现:P2 阶段一个 Proto 从不「升完又回」(回边由 P4 才引入)。
//
// 数值约定:TierInterp = 0(Go 零值)。`pd := &ProfileData{}` 即等价于
// `pd.TierState == TierInterp`,与 profileTable 惰性建表一致。
type TierState uint8

const (
	TierInterp  TierState = iota // 0: 解释执行中(默认起点)
	TierGibbous                  // 1: 已升 gibbous(P3/P4 共享落点)
	TierStuck                    // 2: 永久解释(不可编译 / 编译失败)
)

func (t TierState) String() string {
	switch t {
	case TierInterp:
		return "interp"
	case TierGibbous:
		return "gibbous"
	case TierStuck:
		return "stuck"
	default:
		return "unknown"
	}
}
