//go:build wangshu_p4

// Package jit —— P4 编译器主体(wangshu_p4 build)。
//
// PJ0 阶段:SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent。bridge
// 注入本 Compiler 后,F7 闸门(`docs/design/p2-bridge/03-compilability-analysis.md`
// §3.7)对所有 Proto 判 NotCompilable,considerPromotion 进 TierStuck 吸收
// 态——PJ0 验收口径就是「所有 Proto 仍走 crescent」(承 00-overview.md §4
// PJ0 行)。
//
// PJ1 起 amd64 直线模板渐进上线,supported 表逐 PJ 扩。
package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compiler 实现 `bridge.P3Compiler` 接口(`p2-bridge/05-p3-p4-interface.md`
// §2)。P3/P4/mock 三方共享同一接口面,P2 实现端零修改——这是「共享前端」
// 物理体现(05 §0.3 + p4-method-jit/00-overview.md §1)。
type Compiler struct {
	// PJ1+ 字段位:
	//   - codePagePool *codePagePool  // exec mmap 代码页池(05 §2.1)
	//   - emitter      *amd64.Emitter // per-arch 发射器(06 §2.4)
	//   - state        *p4SpecState   // P4 投机子状态机(03 §4 方案 A)
	//
	// PJ0 留空——见 New() 注释。
}

// New 构造 P4 Compiler。
//
// PJ0:返回空 struct;后续 PJ 渐进填实(codePagePool / emitter / state)。
//
// 调用方:`internal/crescent/arena_p4.go` wireP4(`docs/design/p4-method-jit/00-overview.md`
// §4 PJ0 行 + P3 同款 arena_p3.go 注入协议范本)。
func New() *Compiler {
	return &Compiler{}
}

// SupportsAllOpcodes 检查 Proto 中所有 opcode 是否都在后端支持集内。
//
// **PJ0 实装:全返 false**(supported 表初空,保守缺省承
// `06-backends.md` §3.8 渐进白名单纪律)。bridge 注入本 Compiler 后所有
// Proto 经 F7 判 NotCompilable,considerPromotion 进 TierStuck——PJ0 验收
// 口径(00 §4 PJ0:「bridge 注入 P4Compiler 后 SupportsAllOpcodes 全 false ⇒
// 所有 Proto 仍走 crescent」)。
//
// PJ1 起逐 PJ 扩 supported 表(MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN
// → ADD/SUB/...)。
//
// 实装契约(`p2-bridge/05-p3-p4-interface.md` §2):
//   - O(N) 单遍扫 proto.Code;
//   - 调用纯只读,不修改 Proto;
//   - 不应 panic——遇到无法识别的 opcode 编号(38..63 预留区间)统一返
//     false(保守拒)。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	// PJ0:supported 表初空 ⇒ 全 false。
	// 不需要扫 proto.Code(任何 opcode 都不支持)。
	_ = proto
	return false
}

// Compile 把 Proto 编译成 GibbousCode(可执行产物)。
//
// **PJ0 实装:返回 ErrCompileNotImplemented**——bridge 不应在 PJ0 调到这里
// (因 SupportsAllOpcodes 全 false 已在 F7 拦下);本函数返错是防御性兜底,
// 若 force-all 类测试路径绕过 F7 也能 fallback 到 TierStuck。
//
// PJ1 起渐进填实:
//   - emitter 扫 proto.Code 发射 per-opcode 模板;
//   - codePagePool 分配 RW 段 → 写码 → mprotect RX;
//   - 包装 *p4Code 返回。
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	_ = proto
	_ = feedback
	return nil, ErrCompileNotImplemented
}

// ErrCompileNotImplemented:PJ0 占位错误——P4 后端尚未实装。
//
// bridge 收到此错误后把该 Proto 标 TierStuck(承
// `p2-bridge/04-try-compile-fallback.md` §3 转移规则:Compile 失败 ⇒ 永久
// 解释,不重试)。**PJ0 不应触发本错误**(SupportsAllOpcodes 已全 false),
// 触发即说明上游绕过了 F7 闸门(force-all 类测试或 bridge 实装变更),
// 防御性兜底。
var ErrCompileNotImplemented = errors.New("internal/gibbous/jit: PJ0 skeleton — Compile not implemented")
