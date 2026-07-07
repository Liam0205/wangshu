//go:build wangshu_p4

// p4state.go — P4 内部投机子状态机(承
// docs/design/p4-method-jit/04-osr-deopt.md §5 + §11 字段定义)。
//
// Scheme A: the P2 tier enum stays three-state (TierInterp / TierGibbous /
// TierStuck); P4 keeps its own per-proto sub-state field p4SpecState[proto]
// (P4Speculative / P4Deoptimized / P4StuckSpeculation) layered on top of P2
// TierGibbous, invisible to P2.
//
// STATUS (active on amd64): the SELF + CALL spec template (`obj:m()` OOP
// shape) is wired up on amd64 (archSupportsSpec() returns true). When a spec
// segment's guard fails (receiver reshaped / key degraded / NodeVal==nil),
// code.go's runSpecSelfCall calls onOSRExit(proto) to account the miss and
// fall back to host.Self; once the count reaches DeoptThreshold the proto
// flips to P4Deoptimized and the speculative code is withdrawn. This
// spec-template deopt accounting is live in production and is proven by
// TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt / _DeoptStorm
// (SpecP4DeoptHits grows).
//
// Two distinct deopt mechanisms must not be conflated: this file is the
// SPEC-TEMPLATE guard-miss deopt accounting (active); issue #50's seg2seg
// uses a separate "virtual frame + deopt-redo" — a seg2seg callee whose guard
// misses re-runs the equivalent interpreter semantics (deopt-redo) without
// touching this file. The function-level OSR materialization (rebuilding an
// interpreter frame) that issue #51 sketched never materialized; it was
// superseded by seg2seg's deopt-redo (ruling: 04-osr-deopt.md header +
// spike/p4callinline/DECISION.md).
package jit

import (
	"sync"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// P4SpecState 是 P4 内部投机子状态(承 04 §5.2 状态图)。
type P4SpecState uint8

const (
	// P4SpecUnknown:未跟踪 / 首次见(默认值,Proto 升 P4 后下次 install
	// 编译期才置 P4Speculative)
	P4SpecUnknown P4SpecState = iota

	// P4Speculative:已装 P4 投机版编译码,guard 在跑。OSR exit 单次失败
	// 不切状态(留观察,deopt 计数 +1)
	P4Speculative

	// P4Deoptimized:deopt 计数超阈值,P4 端撤投机版编译码,等解释期
	// IC 自然稀释 confidence 后重训练 + 重编译
	P4Deoptimized

	// P4StuckSpeculation:重编译次数超 MaxRecompileTries 仍反复 deopt,
	// P4 端拉黑投机(P2 看仍 TierGibbous,只是当前未装投机版 GibbousCode)
	P4StuckSpeculation
)

// String 返回状态名(用于日志 / 探针 readback)。
func (s P4SpecState) String() string {
	switch s {
	case P4Speculative:
		return "P4Speculative"
	case P4Deoptimized:
		return "P4Deoptimized"
	case P4StuckSpeculation:
		return "P4StuckSpeculation"
	default:
		return "P4SpecUnknown"
	}
}

// p4SpecEntry 是 p4SpecState[proto] 的 per-Proto 字段(承 04 §5.6 字段集)。
//
// **方案 A**(P4 自管):**不**挂 P2 ProfileData / TierState 任何字段——P2 视角
// 看 Proto 仍是 TierGibbous,P4 端独立子状态机叠加。
type p4SpecEntry struct {
	// state 是当前子状态(P4Speculative / P4Deoptimized / P4StuckSpeculation)。
	state P4SpecState

	// deoptCount 是累计 OSR exit 次数(承 04 §5.2 单次失败 += 1)。
	// 达 DeoptThreshold 切 P4Deoptimized 撤投机版编译码。
	deoptCount uint32

	// recompileCount 是累计重编译次数(承 04 §5.3 重编译次数硬上限)。
	// 达 MaxRecompileTries 切 P4StuckSpeculation 拉黑投机。
	recompileCount uint32
}

// p4SpecStateMap 是 proto → p4SpecEntry 映射。
//
// **package-level 全局 map**(承 PR #26 外部审查纠正注释 2026-06-28):
// 实装是 `var p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)` 包级
// 全局变量,**多 State 共享同一 map**——经 `sync.Mutex` 守护(load /
// store / increment 都过 lock),V18 -race 友好(承 R3 已立 race-free
// 纪律)。
//
// **多 State 并发安全性论证**:
//   - *bytecode.Proto 全局唯一(per-Proto 单例,frontend.compile 输出固定指针)
//   - 多 State 跑同一 Proto 时共享 p4SpecEntry 状态(deoptCount/state/
//     recompileCount 三字段),状态语义 per-Proto 而非 per-State,正确;
//   - 跨不同 Proto 经 map key 隔离,无冲突;
//   - 单 entry 内并发增 deoptCount 走 p4SpecMu 串行化,无 race;
//   - 重编译触发(deoptCount ≥ DeoptThreshold)虽可能多 State 并发触发,
//     但 onP4Install 路径同样过 lock,只首个状态转移生效,后续 state==
//     P4Speculative ⇒ 等价 idempotent。
//
// **历史注释**(被 2026-06-28 纠正):此前曾称「per-Compiler 单例(per-State
// 因 jit.Compiler 是 per-State)」,与实装不符。p4SpecState 是包级全局,
// 跨多 State 共享。
//
// **OSR exit 路径热度**:OSR exit 是冷路径(单帧投机失败才触发),lock 开销
// 可忽略。后续若 OSR exit 触发率高,可改 sync.Map(本批 v0 简化)。
var (
	p4SpecMu    sync.Mutex
	p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)
)

// DeoptThreshold 是单 Proto 累计 OSR exit 次数阈值(承 04 §5.2 + §5.6 校准)。
// **占位值**:实测期定标(承 04 §5.6:典型 3-5);本批 v0 用宽松值 16 防误触发。
const DeoptThreshold uint32 = 16

// MaxRecompileTries 是 Proto 累计重编译次数上限(承 04 §5.3 + §5.6 校准)。
// **占位值**:实测期定标(承 04 §5.3:典型 1-2);本批 v0 用 2。
const MaxRecompileTries uint32 = 2

// onOSRExit handles a single spec-template guard-miss event (04 §5.1).
//
// It bumps the deopt count by 1, and on reaching the threshold withdraws the
// speculative code and flips the proto to P4Deoptimized. Does not touch the
// P2 tierState (Scheme A).
//
// STATUS (active on amd64): called from code.go's runSpecSelfCall when a SELF
// spec segment guard fails. See the file header for the active-vs-superseded
// deopt mechanism distinction.
func onOSRExit(proto *bytecode.Proto) {
	if proto == nil {
		return
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		entry = &p4SpecEntry{state: P4Speculative}
		p4SpecState[proto] = entry
	}
	entry.deoptCount++
	if entry.deoptCount < DeoptThreshold {
		return // 单次失败,继续观察
	}
	// 阈值触达:撤 P4 投机版编译码 + 切 P4Deoptimized
	if entry.recompileCount >= MaxRecompileTries {
		entry.state = P4StuckSpeculation // 拉黑投机(P4 内吸收态)
		incSpecP4StuckHits()
		return
	}
	entry.state = P4Deoptimized
	entry.deoptCount = 0 // 计数清零,等 IC 自然稀释 confidence 后重训练 + 重编译
	incSpecP4DeoptHits()
}

// onP4Install registers a proto's first / recompiled P4 speculative code
// (04 §5.3 bumps recompileCount).
//
// Sets the proto state to P4Speculative, and bumps recompileCount on a
// recompile. Does not touch the P2 tierState (Scheme A).
//
// Call contract: invoked by compileSpecSelfCall after installing the SELF
// spec template (compiler.go). Active on amd64 for the OOP `obj:m()` shape.
func onP4Install(proto *bytecode.Proto) {
	if proto == nil {
		return
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		entry = &p4SpecEntry{state: P4Speculative}
		p4SpecState[proto] = entry
		return
	}
	// 重编译:状态从 P4Deoptimized 回 P4Speculative,recompileCount += 1
	if entry.state == P4Deoptimized {
		entry.state = P4Speculative
		entry.recompileCount++
	}
}

// P4SpecStateOf 返 proto 当前子状态(测试 + 调试用)。
func P4SpecStateOf(proto *bytecode.Proto) P4SpecState {
	if proto == nil {
		return P4SpecUnknown
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		return P4SpecUnknown
	}
	return entry.state
}

// ResetP4SpecState 把 p4SpecState 全清(测试 isolation 用)。
func ResetP4SpecState() {
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)
}

// specP4DeoptHits 是 P4Deoptimized 状态转移命中次数(prove-the-path 探针)。
var specP4DeoptHits uint64

// specP4StuckHits 是 P4StuckSpeculation 状态转移命中次数。
var specP4StuckHits uint64

// SpecP4DeoptHits 返累计 P4Deoptimized 状态转移命中次数(测试用)。
func SpecP4DeoptHits() uint64 { return atomic.LoadUint64(&specP4DeoptHits) }

// SpecP4StuckHits 返累计 P4StuckSpeculation 状态转移命中次数(测试用)。
func SpecP4StuckHits() uint64 { return atomic.LoadUint64(&specP4StuckHits) }

// incSpecP4DeoptHits 包内 ++(onOSRExit 触达 P4Deoptimized 时调)。
func incSpecP4DeoptHits() { atomic.AddUint64(&specP4DeoptHits, 1) }

// incSpecP4StuckHits 包内 ++(onOSRExit 触达 P4StuckSpeculation 时调)。
func incSpecP4StuckHits() { atomic.AddUint64(&specP4StuckHits, 1) }
