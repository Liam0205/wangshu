// Package mock provides P3Compiler test doubles for P2 bridge users
// (`docs/design/p2-bridge/05-p3-p4-interface.md` §7 + 06 §11.1 RB-T4)。
//
// 三种 mock 行为变体(覆盖 PB7 端到端测试矩阵):
//
//   - DummyCompile: SupportsAllOpcodes=true / Compile 永远成功
//     (TierGibbous 路径验收)
//   - RejectAll:    SupportsAllOpcodes=false(F7 永远触发,所有 Proto
//     永久解释,等价 P1-only 行为)
//   - PanicOnce:    Compile 第一次 panic,后续不再触发(测 defer recover
//     兜底 + Stuck 不重试纪律)
//
// 为什么提到子包导出:bridge 主包的 _test.go 里已有同款 mocks(state_machine_test
// 与 std_logger_test),但 wangshu 主包 e2e 测试也需要——跨包共享走公共
// 导出(internal/bridge/mock),包路径仍在 internal 不破公共面。
package mock

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// DummyCompile P3:SupportsAllOpcodes=true,Compile 永远成功产空 GibbousCode。
type DummyCompile struct{}

func (DummyCompile) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (DummyCompile) Compile(p *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	return dummyCode{proto: p}, nil
}

// RejectAll P3:SupportsAllOpcodes=false,任何 Proto 都 F7 拒。
//
// 等价于 P3 还没注入(b.p3 == nil)的语义,但显式注入此 mock 在测试里更
// 清晰(避免 F7 无 P3 的特殊路径与「明确不支持」混淆)。
type RejectAll struct{}

func (RejectAll) SupportsAllOpcodes(_ *bytecode.Proto) bool { return false }
func (RejectAll) Compile(_ *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	// SupportsAllOpcodes=false 时 F7 应已拦在 AnalyzeProto 阶段——本路径
	// 理论上不可达。防御性返 err。
	return nil, &bridge.CompileError{
		Kind:   bridge.CompileErrBackendDeclined,
		Reason: "RejectAll mock: SupportsAllOpcodes=false should have stopped this",
	}
}

// PanicOnce P3:第一次 Compile panic(测 defer recover);后续 Compile 仍
// panic(Stuck 后不会再触发,所以「后续是否 panic」无法直接观察——但语义
// 上保持一致)。
type PanicOnce struct{}

func (PanicOnce) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (PanicOnce) Compile(_ *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	panic("synthetic backend bug for testing")
}

// dummyCode 是 GibbousCode 的最小实现(P2 视角不透明)。Run 是 no-op 占位
// (mock 不真执行;trampoline 端到端测试用真 P3 gibbous,见 wangshu_p3 build)。
type dummyCode struct{ proto *bytecode.Proto }

func (d dummyCode) Proto() *bytecode.Proto         { return d.proto }
func (d dummyCode) Run(_ []uint64, _ uint32) int32 { return 0 }
func (d dummyCode) PendingErr() error              { return nil }
