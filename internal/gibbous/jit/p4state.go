//go:build wangshu_p4

// p4state.go — P4 内部投机子状态机(承
// docs/design/p4-method-jit/04-osr-deopt.md §5 + §11 字段定义)。
//
// **方案 A 决议**(承 04 头注 + 03 §4.2 + 04 §11):**P2 三态枚举不变**
// (TierInterp / TierGibbous / TierStuck),**P4 在 internal/gibbous/jit 内部
// 维护独立子状态字段 p4SpecState[proto]**,枚举 P4Speculative / P4Deoptimized /
// P4StuckSpeculation,叠加在 P2 TierGibbous 之上,**P2 不感知**。
//
// **当前 PJ5 现状**:简化形态全走 host.CallBaseline / TailCall / Self 等 baseline
// doCall 路径(byte-equal P1 解释器),**无投机失败 deopt 路径**——本文件是
// OSR exit 协议的工程基础(p4SpecState map + DeoptThreshold / MaxRecompileTries
// 阈值占位),等段内 EmitCallInline 投机模板真接入时激活。
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
// **per-Compiler 单例**(per-State 因 jit.Compiler 是 per-State):多 State
// 并发安全 — 经 sync.Mutex 守护(load / store / increment 都过 lock)。
// V18 -race 友好(承 R3 已立 race-free 纪律)。
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

// onOSRExit 处理单帧 OSR exit 事件(承 04 §5.1 单次失败的三件事)。
//
// **职责**:把 deopt 计数 +1,达阈值时撤投机版编译码 + 切 P4Deoptimized。
// **不动 P2 tierState**(方案 A 严格遵守)。
//
// **当前 PJ5 现状**:OSR exit 路径在 PJ5 简化形态中**不存在**(全走 baseline
// host helper)。本函数是工程基础,等段内 EmitCallInline 投机模板真接入时激活。
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

// onP4Install 注册 Proto 首次 / 重编译 P4 投机版编译码(承 04 §5.3 重编译次数 +1)。
//
// **职责**:把 Proto 状态置 P4Speculative,recompileCount += 1(若是重编译)。
// **不动 P2 tierState**(方案 A 严格遵守)。
//
// **调用契约**:由 Compile 主路径在 installGibbous 完成 P4 投机版编译码安装
// 后调用。当前 PJ5 现状:所有 P4 GibbousCode 走 baseline doCall 非投机,
// 实际不应触发 — 留 spec inline 真接入时激活(占位接口)。
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
