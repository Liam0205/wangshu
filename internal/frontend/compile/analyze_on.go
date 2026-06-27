//go:build wangshu_profile

// 当 wangshu_profile build tag 启用时,Compile 在每个 FuncExpr 编完后调
// bridge.AnalyzeProto 把可编译性分析结果写进 Proto.Compilability +
// Proto.CompReasons(`docs/design/p2-bridge/03-compilability-analysis.md`
// §6.3 接线)。
package compile

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// analyzeCompilabilityWithOuter 由 compileFunc 在产 Proto 后调用——AST 用完即弃
// (03 §2.4 决策方案 ①):本函数返回后 fn 引用即可被 GC。
//
// 不持有 Bridge 实例:用一个临时 Bridge 跑 AnalyzeProto/AnalyzeProtoWithOuter,
// 目的只是借用它的 visitor + reasonsBitmap → Proto 字段写入逻辑。AnalyzeProto
// 内部既写 Bridge 的 profileTable(本函数退出即被 GC)也写
// Proto.Compilability / Proto.CompReasons(跨 State 共享只读)。
//
// **PJ5 scope-aware 扩展(2026-06-27)**:接受 outerFS,从 outerFS 链上收集
// 所有 localFnAsts 合并视图(近层覆盖远层),传给 AnalyzeProtoWithOuter
// 作为 outerLocalFuncs 上下文 — 让嵌套 closure 看到外层 local fn 仍能识别
// 为 known call,打通 P4 PJ5 真升层路径。outerFS=nil(主 chunk)时退化为
// AnalyzeProto 旧行为。
//
// **F7 行为**:本临时 Bridge 没注入 P3 编译器(b.p3 == nil)→ F7 永远
// 触发 → 所有 Compile-期分析的 Proto 都被标 CompNotCompilable +
// ReasonBackendUnsupp。这反映「编译期我们还不知道运行期会注入哪个 P3」
// 的现实。**运行期 considerPromotion 看到此占位位 + b.p3 已注入时调
// bridge.recheckCompilabilityRuntime 重判**(issue #18 修复),清掉编译期
// 烧的 F7 占位,对真实后端重查 SupportsAllOpcodes,F1-F6 结构性排除原样保留。
//
// **对 PB7 验收的影响**:byte-equal 差分仍成立——P1-only build(无
// wangshu_profile)整链路 no-op;p3 build 下结构性 NotCompilable(F1-F6)
// 仍永久解释,只是「编译期 F7 占位 + 运行期 P3 可处理」的子集才走升层
// 路径,与原 byte-equal 期望一致(F1-F6 子集行为不变)。
func analyzeCompilabilityWithOuter(fn *ast.FuncExpr, proto *bytecode.Proto, outerFS *funcState) {
	tmp := bridge.NewBridge()
	if outerFS == nil {
		tmp.AnalyzeProto(fn, proto)
		return
	}
	// 收集 outer 链上所有 funcState 的 localFnAsts 合并视图(近层覆盖远层)。
	outerLocals := map[string]*ast.FuncExpr{}
	// 从最远层到最近层逆序合并,内层覆盖外层(local scope 遮蔽语义)。
	var chain []*funcState
	for cur := outerFS; cur != nil; cur = cur.prev {
		chain = append(chain, cur)
	}
	// chain 现在是从内到外;反向遍历从外到内
	for i := len(chain) - 1; i >= 0; i-- {
		for name, fnAST := range chain[i].localFnAsts {
			outerLocals[name] = fnAST
		}
	}
	tmp.AnalyzeProtoWithOuter(fn, proto, outerLocals)
}
